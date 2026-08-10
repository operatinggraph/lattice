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
