package consumer

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestProcessMsg_UnwritableKeyIsTerminal pins the classification rider: a
// NodeID that clears processMsg's own NATS-reserved-character screen
// (".", "*", ">", whitespace) can still be outside the narrower charset the
// jetstream KV client enforces client-side ([-/_=.a-zA-Z0-9]) — "!" is a
// legal NATS subject-token character but not a legal KV key character. Such
// a key can never become writable on redelivery, so processMsg must Term it
// rather than Nak it into an endless retry loop.
func TestProcessMsg_UnwritableKeyIsTerminal(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	_, nc := natsfixture.Server(t)

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-invalidkey-test"})
	require.NoError(t, err)

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, "adj-invalidkey-test")
	require.NoError(t, err)

	b := NewBootstrapper(conn, "core-invalidkey-test", adjKV)

	evt := adjacency.CoreKVEvent{
		CoreKvKey: "core.e1", EdgeID: "e1", Name: "rel",
		Direction: "outbound", NodeID: "bad!node", OtherNodeID: "other",
	}
	body, err := json.Marshal(evt)
	require.NoError(t, err)

	decision := b.processMsg(ctx, substrate.Message{
		Subject: "$KV.core-invalidkey-test.edge.e1",
		Body:    body,
	})
	assert.Equal(t, substrate.Term, decision,
		"an unwritable NodeID must Term, never loop forever on Nak")
}

// The link-bridge path (processLinkEnvelope) carries the identical
// classification, but has no test of its own here: ParseLinkKey requires
// both endpoint segments to already be valid 20-char NanoIDs
// (keys.IsValidNanoID), and that alphabet (internal/substrate/keys/nanoid.go)
// is a strict subset of the jetstream KV client's own key charset — so a
// NodeID reaching adjacency.Build through the link bridge can never be the
// kind of malformed value this rider exists for. The classification is
// applied there for uniformity with the legacy path, not because it is
// reachable.

// TestRaiseAppliedSeq_IsAMonotoneMaximum pins the cursor's arithmetic. A Nak'd
// message is redelivered out of order, so a cursor that ASSIGNED would be
// walked backwards by an old redelivery and would then report progress the
// index has already passed — the direction a reader compares against its own
// ordering token and acts on.
//
// Also pins the disposition rule from the other side: the cursor starts at 0
// and is raised only by what is applied, so a bootstrapper that has retired
// nothing answers with the refusing value.
func TestRaiseAppliedSeq_IsAMonotoneMaximum(t *testing.T) {
	b := &Bootstrapper{}
	require.Equal(t, uint64(0), b.AppliedSeq())

	b.raiseAppliedSeq(10)
	require.Equal(t, uint64(10), b.AppliedSeq())

	b.raiseAppliedSeq(4)
	require.Equal(t, uint64(10), b.AppliedSeq(), "a redelivered older message must not walk the cursor back")

	b.raiseAppliedSeq(10)
	require.Equal(t, uint64(10), b.AppliedSeq(), "re-applying the same sequence is not progress")

	b.raiseAppliedSeq(11)
	require.Equal(t, uint64(11), b.AppliedSeq())
}

// TestHandle_OnlyARetiringDispositionRaisesTheCursor pins which dispositions
// move the cursor. Ack and Term both RETIRE a message — the index reflects
// everything up to that sequence once either is returned, since a Term discards
// a message the index can never apply. A Nak leaves the message owed, so moving
// the cursor past it would claim the index reflects work it still has to do.
func TestHandle_OnlyARetiringDispositionRaisesTheCursor(t *testing.T) {
	ctx := context.Background()

	t.Run("Ack raises", func(t *testing.T) {
		b := &Bootstrapper{ready: make(chan struct{})}
		// An empty body on a non-link key is a KV tombstone: acked and skipped.
		require.Equal(t, substrate.Ack, b.handle(ctx, substrate.Message{Subject: "node.x", Sequence: 7, NumPending: 1}))
		require.Equal(t, uint64(7), b.AppliedSeq())
	})

	t.Run("Term raises", func(t *testing.T) {
		b := &Bootstrapper{ready: make(chan struct{})}
		// A body no decoder can read is discarded outright — never retried, so
		// the index will never reflect it and the cursor may pass it.
		require.Equal(t, substrate.Term, b.handle(ctx, substrate.Message{Subject: "node.x", Body: []byte("{not json"), Sequence: 9, NumPending: 1}))
		require.Equal(t, uint64(9), b.AppliedSeq())
	})

	t.Run("Nak leaves the cursor where it is", func(t *testing.T) {
		if testing.Short() {
			t.Skip("requires NATS JetStream")
		}
		// An adjacency bucket that has gone away under a live handle makes
		// adjacency.Build fail with a plain error, which processMsg Naks — the
		// message is still owed, so the index has not applied it.
		_, nc := natsfixture.Server(t)
		js, err := jetstream.New(nc)
		require.NoError(t, err)
		_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-nak-cursor"})
		require.NoError(t, err)
		conn, err := substrate.Wrap(nc)
		require.NoError(t, err)
		adjKV, err := conn.OpenKV(ctx, "adj-nak-cursor")
		require.NoError(t, err)
		require.NoError(t, js.DeleteKeyValue(ctx, "adj-nak-cursor"))

		b := NewBootstrapper(conn, "core-nak-cursor", adjKV)
		evt := adjacency.CoreKVEvent{CoreKvKey: "core.e1", EdgeID: "e1", Name: "HAS_PARTY", Direction: "outbound", NodeID: "nodeA", OtherNodeID: "nodeB"}
		body, err := json.Marshal(evt)
		require.NoError(t, err)
		require.Equal(t, substrate.Nak, b.handle(ctx, substrate.Message{Subject: "node.e1", Body: body, Sequence: 11, NumPending: 1}))
		require.Equal(t, uint64(0), b.AppliedSeq(), "an owed message must not move the index's progress cursor")
	})
}
