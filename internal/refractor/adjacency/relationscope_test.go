package adjacency_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// relationSet is the scope vocabulary NeighborsByRelation takes.
func relationSet(names ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		out[n] = struct{}{}
	}
	return out
}

// TestNeighborsByRelation_UnmarkedNodeFiltersTheDocument pins the ordinary
// node: the document is one key either way, so the narrowing is in the answer.
func TestNeighborsByRelation_UnmarkedNodeFiltersTheDocument(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	svc := nanoID(t, "svc")
	meta := nanoID(t, "meta")
	holder := nanoID(t, "hdr")

	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: substrate.LinkKey("service", svc, "instanceOf", "meta", meta),
		EdgeID:    "e-instanceOf", Name: "instanceOf",
		Direction: "outbound", NodeID: svc, OtherNodeID: meta, OtherType: "meta",
	}))
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: substrate.LinkKey("service", svc, "providedTo", "identity", holder),
		EdgeID:    "e-providedTo", Name: "providedTo",
		Direction: "outbound", NodeID: svc, OtherNodeID: holder, OtherType: "identity",
	}))

	all, _, err := adjacency.Neighbors(ctx, adjKV, coreKV, svc)
	require.NoError(t, err)
	require.Len(t, all, 2, "the unscoped read still sees both edges")

	scoped, rev, err := adjacency.NeighborsByRelation(ctx, adjKV, coreKV, svc, relationSet("providedTo"))
	require.NoError(t, err)
	require.Len(t, scoped, 1)
	assert.Equal(t, "providedTo", scoped[0].Name)
	assert.Equal(t, holder, scoped[0].OtherNodeID)
	assert.NotZero(t, rev, "an unmarked node reports its document's revision")
}

// TestNeighborsByRelation_EmptySetReadsNothing pins the shape the walk relies
// on for the vertex types no pattern position admits: no relation means no
// answer AND no read, which is why standing on a descriptor hub costs nothing.
func TestNeighborsByRelation_EmptySetReadsNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	node := nanoID(t, "node")
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: "lnk.identity." + node + ".holdsRole.role." + nanoID(t, "rrr"),
		EdgeID:    "e1", Name: "holdsRole",
		Direction: "outbound", NodeID: node, OtherNodeID: nanoID(t, "rrr"), OtherType: "role",
	}))

	// nil KV handles: reaching either one would be a read, and there is nothing
	// here to read for.
	edges, rev, err := adjacency.NeighborsByRelation(ctx, nil, nil, node, nil)
	require.NoError(t, err)
	assert.NotNil(t, edges, "an empty answer is still a non-nil slice")
	assert.Empty(t, edges)
	assert.Zero(t, rev)

	edges, _, err = adjacency.NeighborsByRelation(ctx, adjKV, coreKV, node, relationSet())
	require.NoError(t, err)
	assert.Empty(t, edges)
}

// TestNeighborsByRelation_UnmarkedNodeWithNoDocument answers empty rather than
// failing, exactly as Neighbors does for the same node.
func TestNeighborsByRelation_UnmarkedNodeWithNoDocument(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	edges, rev, err := adjacency.NeighborsByRelation(ctx, adjKV, coreKV, nanoID(t, "none"), relationSet("holdsRole"))
	require.NoError(t, err)
	assert.NotNil(t, edges)
	assert.Empty(t, edges)
	assert.Zero(t, rev)
}

// TestNeighborsByRelation_MarkedNodeReadsOnlyTheNamedRelations is the case the
// whole mechanism exists for: an overflow-marked hub's read enumerates Core KV,
// and a scoped read must bring back the named relations in BOTH directions and
// nothing else.
func TestNeighborsByRelation_MarkedNodeReadsOnlyTheNamedRelations(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	hub := nanoID(t, "hub")
	roleID := nanoID(t, "rrr")
	taskID := nanoID(t, "task")
	metaID := nanoID(t, "meta")

	outKey := seedLink(t, coreKV, "identity", hub, "holdsRole", "role", roleID, false)
	inKey := seedLink(t, coreKV, "task", taskID, "assignedTo", "identity", hub, false)
	// Two links the scope does not name: one outbound, one inbound.
	seedLink(t, coreKV, "identity", hub, "instanceOf", "meta", metaID, false)
	seedLink(t, coreKV, "task", taskID, "watchedBy", "identity", hub, false)
	// And a link on neither endpoint of the hub.
	seedLink(t, coreKV, "task", taskID, "forRole", "role", roleID, false)

	markNode(t, adjKV, hub)

	unscoped, _, err := adjacency.Neighbors(ctx, adjKV, coreKV, hub)
	require.NoError(t, err)
	require.Len(t, unscoped, 4, "the relation-blind read drains every link on the hub")

	edges, fingerprint, err := adjacency.NeighborsByRelation(ctx, adjKV, coreKV, hub,
		relationSet("holdsRole", "assignedTo"))
	require.NoError(t, err)
	require.Len(t, edges, 2)
	assert.NotZero(t, fingerprint)

	byID := map[string]adjacency.EdgeEntry{}
	for _, e := range edges {
		byID[e.EdgeID] = e
	}

	out, ok := byID[outKey]
	require.True(t, ok, "the outbound named relation comes back")
	assert.Equal(t, "outbound", out.Direction)
	assert.Equal(t, "holdsRole", out.Name)
	assert.Equal(t, roleID, out.OtherNodeID)
	assert.Equal(t, "role", out.OtherType)

	in, ok := byID[inKey]
	require.True(t, ok, "the inbound named relation comes back")
	assert.Equal(t, "inbound", in.Direction)
	assert.Equal(t, "assignedTo", in.Name)
	assert.Equal(t, taskID, in.OtherNodeID)
	assert.Equal(t, "task", in.OtherType)
}

// TestNeighborsByRelation_MarkedNodeDropsSoftTombstonedLinks pins the retraction
// path a marked node has no other transport for: the scoped read filters
// isDeleted exactly as the unscoped one does.
func TestNeighborsByRelation_MarkedNodeDropsSoftTombstonedLinks(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	hub := nanoID(t, "tmb")
	liveRole := nanoID(t, "keep")
	deadRole := nanoID(t, "gone")
	liveKey := seedLink(t, coreKV, "identity", hub, "holdsRole", "role", liveRole, false)
	seedLink(t, coreKV, "identity", hub, "holdsRole", "role", deadRole, false)
	markNode(t, adjKV, hub)

	edges, before, err := adjacency.NeighborsByRelation(ctx, adjKV, coreKV, hub, relationSet("holdsRole"))
	require.NoError(t, err)
	require.Len(t, edges, 2)

	seedLink(t, coreKV, "identity", hub, "holdsRole", "role", deadRole, true)

	edges, after, err := adjacency.NeighborsByRelation(ctx, adjKV, coreKV, hub, relationSet("holdsRole"))
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, liveKey, edges[0].EdgeID)
	assert.NotEqual(t, before, after, "a retraction must move the fingerprint of the read that saw it")
}

// TestNeighborsByRelation_MarkedNodeSelfLinkYieldsBothDirections keeps the
// scoped read's self-link handling identical to the unscoped one's: a link whose
// two endpoints are the same node is both an outbound and an inbound edge, and
// it matches both of the relation's filters.
func TestNeighborsByRelation_MarkedNodeSelfLinkYieldsBothDirections(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	hub := nanoID(t, "sfx")
	key := seedLink(t, coreKV, "identity", hub, "supervises", "identity", hub, false)
	seedLink(t, coreKV, "identity", hub, "instanceOf", "meta", nanoID(t, "meta"), false)
	markNode(t, adjKV, hub)

	edges, _, err := adjacency.NeighborsByRelation(ctx, adjKV, coreKV, hub, relationSet("supervises"))
	require.NoError(t, err)
	require.Len(t, edges, 2)
	assert.ElementsMatch(t, []string{"inbound", "outbound"},
		[]string{edges[0].Direction, edges[1].Direction})
	for _, e := range edges {
		assert.Equal(t, key, e.EdgeID)
	}
}

// TestNeighborsByRelation_MarkedNodeCrossesTheLatch reaches the marked path the
// way production does — through SetOverflowThresholds and a real Build — rather
// than by writing the mark by hand, so the scoped read is exercised against a
// node the latch itself marked.
func TestNeighborsByRelation_MarkedNodeCrossesTheLatch(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	defer adjacency.SetOverflowThresholds(2, 1<<20)()

	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	hub := nanoID(t, "atch")
	for i := 0; i < 3; i++ {
		ev := linkEdgeEvent(t, hub, i)
		seedLink(t, coreKV, "identity", hub, "holdsRole", "role", ev.OtherNodeID, false)
		require.NoError(t, adjacency.Build(ctx, adjKV, ev))
	}
	other := nanoID(t, "oth")
	otherKey := substrate.LinkKey("identity", hub, "assignedTo", "task", other)
	seedLink(t, coreKV, "identity", hub, "assignedTo", "task", other, false)
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: otherKey, EdgeID: otherKey, Name: "assignedTo",
		Direction: "outbound", NodeID: hub, OtherNodeID: other, OtherType: "task",
	}))
	require.True(t, isMarked(t, adjKV, hub), "the node must have latched")

	edges, _, err := adjacency.NeighborsByRelation(ctx, adjKV, coreKV, hub, relationSet("assignedTo"))
	require.NoError(t, err)
	require.Len(t, edges, 1, "only the named relation is read back off a marked node")
	assert.Equal(t, "assignedTo", edges[0].Name)
	assert.Equal(t, other, edges[0].OtherNodeID)
}
