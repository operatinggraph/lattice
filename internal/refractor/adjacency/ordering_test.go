package adjacency_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// The ordering guard. A Contract #1 link key IS its own EdgeID, and it is
// stable across a revoke → re-grant, so the same EdgeID names two different
// versions of the same relationship. Three writers index it off one Core KV
// stream with no ordering guarantee between them, and a Nak'd message is
// redelivered behind whatever was behind it — so a staler view of an edge can
// reach the index after a newer one, in both directions. Every case below is
// driven by explicit sequence arguments: the interleaving under test is the
// order the numbers name, never the order the calls happen to run in.

// nodeDocument reads a node's whole adjacency document, floors included —
// documentEdges' view stops at the edge list, and half of this guard lives in
// the floors an absent edge leaves behind.
func nodeDocument(t *testing.T, adjKV *substrate.KV, nodeID string) adjacency.AdjValue {
	t.Helper()
	entry, err := adjKV.Get(context.Background(), subjects.AdjKey(nodeID))
	if err != nil {
		require.ErrorIs(t, err, substrate.ErrKeyNotFound)
		return adjacency.AdjValue{}
	}
	var doc adjacency.AdjValue
	require.NoError(t, json.Unmarshal(entry.Value, &doc))
	return doc
}

// edgeEvent is one directional create for edgeID at sequence seq, in the shape
// EventsForLink produces.
func edgeEvent(edgeID, nodeID string, seq uint64) adjacency.CoreKVEvent {
	return adjacency.CoreKVEvent{
		CoreKvKey:   edgeID,
		EdgeID:      edgeID,
		Name:        "holdsRole",
		Direction:   "outbound",
		NodeID:      nodeID,
		OtherNodeID: "nodeB",
		OtherType:   "role",
		Seq:         seq,
	}
}

// removalEvent is the retraction of edgeID at sequence seq.
func removalEvent(edgeID, nodeID string, seq uint64) adjacency.CoreKVEvent {
	evt := edgeEvent(edgeID, nodeID, seq)
	evt.IsDeleted = true
	return evt
}

// TestBuild_StaleRemovalCannotDeleteALiveEdge is the under-grant direction: a
// revocation delayed behind the re-grant that superseded it must not delete the
// live edge the re-grant created. Without the guard the removal matches on
// EdgeID alone, drops the entry, and the relationship is absent to every
// executor walk, the enumerator and the CDC prefix diff until something else
// happens to rewrite the node.
func TestBuild_StaleRemovalCannotDeleteALiveEdge(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	require.NoError(t, adjacency.Build(ctx, adjKV, edgeEvent("e1", "nodeA", 200)))
	require.NoError(t, adjacency.Build(ctx, adjKV, removalEvent("e1", "nodeA", 100)),
		"a refused write is declined, not failed — a redelivery would only present it again")

	edges, _, err := adjacency.Neighbors(ctx, adjKV, coreKV, "nodeA")
	require.NoError(t, err)
	require.Len(t, edges, 1, "the re-grant at 200 outranks the revocation at 100 — the edge survives")
	assert.Equal(t, "e1", edges[0].EdgeID)

	assert.Empty(t, nodeDocument(t, adjKV, "nodeA").Removals,
		"a declined removal records no floor: it never happened as far as this node is concerned")
}

// TestBuild_StaleCreateCannotResurrectARemovedEdge is the over-grant direction,
// and the more dangerous one: a revocation that does not stick. The removal
// leaves no entry behind, so the floor that refuses the delayed create has
// nowhere to live except beside the edge list — which is exactly why the
// document keeps removal floors at all.
func TestBuild_StaleCreateCannotResurrectARemovedEdge(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	require.NoError(t, adjacency.Build(ctx, adjKV, removalEvent("e1", "nodeA", 200)))
	require.NoError(t, adjacency.Build(ctx, adjKV, edgeEvent("e1", "nodeA", 100)))

	edges, _, err := adjacency.Neighbors(ctx, adjKV, coreKV, "nodeA")
	require.NoError(t, err)
	assert.Empty(t, edges, "the revocation at 200 outranks the create at 100 — the edge stays revoked")

	assert.Equal(t, map[string]uint64{"e1": 200}, nodeDocument(t, adjKV, "nodeA").Removals,
		"the removal's own sequence is the floor the stale create was measured against")
}

// TestBuild_NewerRemovalAndNewerCreateStillApply is the positive vector for
// both arms. A guard that refuses everything would pass the two stale cases
// above and be worthless; what makes it a guard rather than a freeze is that an
// event ABOVE the floor still lands, on either arm.
func TestBuild_NewerRemovalAndNewerCreateStillApply(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	t.Run("a newer removal deletes an edge the floor does not protect", func(t *testing.T) {
		require.NoError(t, adjacency.Build(ctx, adjKV, edgeEvent("e1", "nodeRem", 200)))
		require.NoError(t, adjacency.Build(ctx, adjKV, removalEvent("e1", "nodeRem", 300)))

		edges, _, err := adjacency.Neighbors(ctx, adjKV, coreKV, "nodeRem")
		require.NoError(t, err)
		assert.Empty(t, edges, "300 clears a floor of 200 — the revocation lands")
	})

	t.Run("a newer create supersedes the removal that preceded it", func(t *testing.T) {
		require.NoError(t, adjacency.Build(ctx, adjKV, removalEvent("e1", "nodeCre", 200)))

		fresh := edgeEvent("e1", "nodeCre", 300)
		fresh.Name = "regranted"
		require.NoError(t, adjacency.Build(ctx, adjKV, fresh))

		edges, _, err := adjacency.Neighbors(ctx, adjKV, coreKV, "nodeCre")
		require.NoError(t, err)
		require.Len(t, edges, 1, "300 clears a floor of 200 — the re-grant lands")
		assert.Equal(t, "regranted", edges[0].Name)
	})

	t.Run("a redelivery of the message that set the floor is idempotent", func(t *testing.T) {
		require.NoError(t, adjacency.Build(ctx, adjKV, edgeEvent("e1", "nodeDup", 200)))
		require.NoError(t, adjacency.Build(ctx, adjKV, edgeEvent("e1", "nodeDup", 200)))

		edges, _, err := adjacency.Neighbors(ctx, adjKV, coreKV, "nodeDup")
		require.NoError(t, err)
		assert.Len(t, edges, 1, "equal sequences are one message twice — applying it again changes nothing")
	})
}

// TestBuild_UnsequencedEventsBehaveExactlyAsBefore pins the compatibility
// direction of the >= comparison. An event carrying no sequence meets a floor
// of 0 and applies, so a caller that has no stream position to offer — and a
// document written before entries carried one — indexes exactly as an unguarded
// build would. Were the comparison >, every such path would silently stop
// applying and the index would shrink toward empty.
func TestBuild_UnsequencedEventsBehaveExactlyAsBefore(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	require.NoError(t, adjacency.Build(ctx, adjKV, edgeEvent("e1", "nodeA", 0)))
	require.NoError(t, adjacency.Build(ctx, adjKV, removalEvent("e1", "nodeA", 0)))

	edges, _, err := adjacency.Neighbors(ctx, adjKV, coreKV, "nodeA")
	require.NoError(t, err)
	assert.Empty(t, edges, "an unsequenced removal is a pure drop, as it has always been")

	assert.Empty(t, nodeDocument(t, adjKV, "nodeA").Removals,
		"an event that names no position in the stream makes no ordering claim, so it records no floor")

	require.NoError(t, adjacency.Build(ctx, adjKV, edgeEvent("e1", "nodeA", 0)))
	edges, _, err = adjacency.Neighbors(ctx, adjKV, coreKV, "nodeA")
	require.NoError(t, err)
	assert.Len(t, edges, 1, "and nothing the removal left behind can block the next unsequenced create")
}

// TestBuild_UpsertClearsTheRemovalFloor pins that a floor does not outlive the
// entry that supersedes it. Once the edge is back, the entry's own sequence is
// what defends it; keeping the removal's number too would leave one edge
// guarded by two floors that drift apart on the next write.
func TestBuild_UpsertClearsTheRemovalFloor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, _ := startKVs(t)

	require.NoError(t, adjacency.Build(ctx, adjKV, removalEvent("e1", "nodeA", 200)))
	require.Equal(t, map[string]uint64{"e1": 200}, nodeDocument(t, adjKV, "nodeA").Removals)

	require.NoError(t, adjacency.Build(ctx, adjKV, edgeEvent("e1", "nodeA", 300)))

	doc := nodeDocument(t, adjKV, "nodeA")
	assert.Empty(t, doc.Removals, "the applied upsert takes the floor over into its own entry")
	require.Len(t, doc.Edges, 1)
	assert.Equal(t, uint64(300), doc.Edges[0].Seq, "and that entry now carries it")
}

// TestBuild_RemovalFloorsAreCappedAndDropTheStalest pins the bound. A node's
// whole edge list lives in one KV document, so unbounded auxiliary state is how
// this index fails — the floors have to be capped for the same reason the edge
// list has an overflow latch. Which floors go is the load-bearing half: the
// LOWEST sequences, because a floor's only job is to refuse a staler event and
// the events racing the stalest floors can no longer be in flight.
func TestBuild_RemovalFloorsAreCappedAndDropTheStalest(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, _ := startKVs(t)

	const (
		removals = 160
		capacity = 128
	)
	for i := 1; i <= removals; i++ {
		edgeID := fmt.Sprintf("e%03d", i)
		require.NoError(t, adjacency.Build(ctx, adjKV, removalEvent(edgeID, "nodeA", uint64(i))))
	}

	floors := nodeDocument(t, adjKV, "nodeA").Removals
	require.Len(t, floors, capacity, "the map is bounded whatever the removal traffic")

	want := map[string]uint64{}
	for i := removals - capacity + 1; i <= removals; i++ {
		want[fmt.Sprintf("e%03d", i)] = uint64(i)
	}
	assert.Equal(t, want, floors, "the survivors are the highest sequences — the freshest floors")

	// A floor arriving already staler than everything held is the one the cap
	// sheds, so a late removal cannot evict a fresher floor by displacing it.
	require.NoError(t, adjacency.Build(ctx, adjKV, removalEvent("eLate", "nodeA", 5)))
	assert.Equal(t, want, nodeDocument(t, adjKV, "nodeA").Removals,
		"the stalest floor is dropped even when it is the one just recorded")
}
