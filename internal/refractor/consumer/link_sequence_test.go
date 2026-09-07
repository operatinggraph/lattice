package consumer_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/consumer"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// The adjacency index refuses an edge write staler than what it already holds,
// and the number that verdict is made against is the delivering message's
// backing-stream sequence. That number is threaded from the transport into the
// event, and a threaded value that is asserted only to be "set" is not
// asserted at all: stamping a constant, or the wrong message's position, would
// leave every ordering test in the tree green while the guard did nothing in
// production. So each pin below drives a REAL message through the real
// Bootstrapper and asserts the persisted EdgeEntry.Seq equals the sequence that
// message was published at — read back from the Put, not assumed.

// seqNanoID is a canonical 20-character NanoID for seed data.
func seqNanoID(t *testing.T, prefix string) string {
	t.Helper()
	id := prefix
	for len(id) < substrate.NanoIDLength {
		id += "a"
	}
	require.True(t, substrate.IsValidNanoID(id), "seed id %q must be a canonical NanoID", id)
	return id
}

// storedEdges reads a node's persisted adjacency entries.
func storedEdges(t *testing.T, adjKV *substrate.KV, nodeID string) []adjacency.EdgeEntry {
	t.Helper()
	entry, err := adjKV.Get(context.Background(), subjects.AdjKey(nodeID))
	if err != nil {
		require.ErrorIs(t, err, substrate.ErrKeyNotFound)
		return nil
	}
	var doc adjacency.AdjValue
	require.NoError(t, json.Unmarshal(entry.Value, &doc))
	return doc.Edges
}

// runBootstrapper starts a Bootstrapper over coreBucket and blocks until it has
// drained what the stream already held.
func runBootstrapper(ctx context.Context, t *testing.T, conn *substrate.Conn, coreBucket string, adjKV *substrate.KV) *consumer.Bootstrapper {
	t.Helper()
	b := consumer.NewBootstrapper(conn, coreBucket, adjKV)
	go func() { _ = b.Run(ctx) }()
	select {
	case <-b.Ready():
	case <-ctx.Done():
		t.Fatalf("timed out waiting for the bootstrapper over %q to drain", coreBucket)
	}
	return b
}

// TestProcessLinkEnvelope_StampsTheDeliveringMessagesSequence is the link arm's
// threading pin. Both endpoints of the link are checked, because the two
// directional events are stamped independently and an arm that lost the stamp
// would leave that endpoint's edge unguarded while the other looked correct.
func TestProcessLinkEnvelope_StampsTheDeliveringMessagesSequence(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	js, nc := startJS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	coreKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-linkseq"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-linkseq"})
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, "adj-linkseq")
	require.NoError(t, err)

	// A few unrelated keys first, so the link's own sequence is a number no
	// other quantity in the fixture happens to equal — a stamp of "1", or of a
	// per-key revision, would otherwise be indistinguishable from the right one.
	for range 4 {
		_, perr := coreKV.Put(ctx, "vtx.identity."+seqNanoID(t, "pad"), []byte(`{"isDeleted":false}`))
		require.NoError(t, perr)
	}

	srcID, dstID := seqNanoID(t, "src"), seqNanoID(t, "dst")
	linkKey := substrate.LinkKey("identity", srcID, "holdsRole", "role", dstID)
	rev, err := coreKV.Put(ctx, linkKey, []byte(`{"isDeleted":false}`))
	require.NoError(t, err)

	// For a NATS KV bucket the backing stream IS the bucket, so the revision the
	// Put reports is the stream sequence the message will be delivered at. Tie
	// the two together explicitly rather than relying on the identity.
	last, err := conn.StreamLastSequence(ctx, "KV_core-linkseq")
	require.NoError(t, err)
	require.Equal(t, rev, last, "the link is the newest message in the stream")
	require.Greater(t, rev, uint64(1), "and its position is not the trivial one")

	runBootstrapper(ctx, t, conn, "core-linkseq", adjKV)

	for _, endpoint := range []struct{ name, nodeID string }{
		{"the source endpoint's outbound arm", srcID},
		{"the destination endpoint's inbound arm", dstID},
	} {
		require.Eventually(t, func() bool { return len(storedEdges(t, adjKV, endpoint.nodeID)) == 1 },
			10*time.Second, 10*time.Millisecond, "%s never indexed the link", endpoint.name)

		edges := storedEdges(t, adjKV, endpoint.nodeID)
		require.Len(t, edges, 1)
		assert.Equal(t, linkKey, edges[0].EdgeID)
		assert.Equal(t, rev, edges[0].Seq,
			"%s must carry the sequence of the message that delivered the link, not merely some sequence",
			endpoint.name)
	}
}

// TestProcessMsg_LegacyArmStampsOnlyItsOwnKey pins the invariant the floors'
// recoverability rests on: a floor for edge L is only ever set from a message
// on subject L.
//
// The link arm gets that for free — the event's EdgeID IS the key it arrived
// on. The legacy arm does not: it takes the EdgeID from the message BODY, so an
// unrelated key far ahead in the stream could otherwise pin a floor on an edge
// whose own key sits far behind it, and no future event for that edge — replay
// and rebuild included — could ever reach it again. The buckets run at the KV
// default History 1, so a key's live revision is always at or above every
// sequence it has produced; that is what makes a self-keyed floor always
// clearable and a foreign-keyed one permanent.
func TestProcessMsg_LegacyArmStampsOnlyItsOwnKey(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	js, nc := startJS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	coreKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-legacyseq"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-legacyseq"})
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, "adj-legacyseq")
	require.NoError(t, err)

	foreignNode, ownNode := seqNanoID(t, "nfr"), seqNanoID(t, "nwn")

	// An event whose body names an EdgeID that is NOT this message's key.
	foreign := adjacency.CoreKVEvent{
		CoreKvKey: "core.foreign", EdgeID: "someOtherEdge", Name: "HAS_PARTY",
		Direction: "outbound", NodeID: foreignNode, OtherNodeID: seqNanoID(t, "ofr"),
	}
	body, err := json.Marshal(foreign)
	require.NoError(t, err)
	_, err = coreKV.Put(ctx, "edge.carrierKey", body)
	require.NoError(t, err)

	// An event whose body names its own key as the EdgeID.
	const ownKey = "edge.ownKey"
	own := adjacency.CoreKVEvent{
		CoreKvKey: "core.own", EdgeID: ownKey, Name: "HAS_PARTY",
		Direction: "outbound", NodeID: ownNode, OtherNodeID: seqNanoID(t, "own"),
	}
	body, err = json.Marshal(own)
	require.NoError(t, err)
	ownRev, err := coreKV.Put(ctx, ownKey, body)
	require.NoError(t, err)

	runBootstrapper(ctx, t, conn, "core-legacyseq", adjKV)

	require.Eventually(t, func() bool { return len(storedEdges(t, adjKV, ownNode)) == 1 },
		10*time.Second, 10*time.Millisecond, "the legacy arm never indexed its edge")

	foreignEdges := storedEdges(t, adjKV, foreignNode)
	require.Len(t, foreignEdges, 1)
	assert.Zero(t, foreignEdges[0].Seq,
		"a body-named EdgeID cannot borrow the carrier key's position — an event that cannot honour "+
			"the invariant stays unsequenced, which is this arm's behaviour with no guard at all")

	ownEdges := storedEdges(t, adjKV, ownNode)
	require.Len(t, ownEdges, 1)
	assert.Equal(t, ownRev, ownEdges[0].Seq,
		"an EdgeID that IS the message's own key honours the invariant, so it is stamped")
}
