package adjacency_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestNeighborsScoped_MarkedNodeReadsOnlyTheRelation pins the marked arm: the
// answer holds only the named relation's edges, and whole=false says so — the
// discriminator a caller memoizing the answer needs, since a scoped
// fingerprint cannot stand in for a whole read's.
func TestNeighborsScoped_MarkedNodeReadsOnlyTheRelation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	hub := nanoID(t, "hub")
	markNode(t, adjKV, hub)
	held := seedLink(t, coreKV, "identity", hub, "holdsRole", "role", nanoID(t, "rza"), false)
	seedLink(t, coreKV, "identity", hub, "worksAt", "org", nanoID(t, "org"), false)

	edges, fingerprint, whole, err := adjacency.NeighborsScoped(ctx, adjKV, coreKV, hub, relationSet("holdsRole"))
	require.NoError(t, err)
	require.False(t, whole, "a marked node answering a scoped read does not answer whole")
	require.NotZero(t, fingerprint, "a marked node's fingerprint is a hash, never the absent-document 0")
	require.Len(t, edges, 1)
	require.Equal(t, held, edges[0].EdgeID)
	require.Equal(t, "holdsRole", edges[0].Name)
}

// TestNeighborsScoped_UnmarkedNodeAnswersWhole pins the unmarked arm: a
// document is one key however many relations a caller asks for, so narrowing
// the read would cost one read per relation and save nothing. rels is ignored,
// the whole document comes back unfiltered, and both the edges and the
// fingerprint are exactly what Neighbors returns for the same node — the
// property that lets a caller file the answer under the node rather than under
// the relation.
func TestNeighborsScoped_UnmarkedNodeAnswersWhole(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	node := nanoID(t, "svc")
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: substrate.LinkKey("service", node, "instanceOf", "meta", nanoID(t, "meta")),
		EdgeID:    "e-instanceOf", Name: "instanceOf",
		Direction: "outbound", NodeID: node, OtherNodeID: nanoID(t, "meta"), OtherType: "meta",
	}))
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: substrate.LinkKey("service", node, "providedTo", "identity", nanoID(t, "hdr")),
		EdgeID:    "e-providedTo", Name: "providedTo",
		Direction: "outbound", NodeID: node, OtherNodeID: nanoID(t, "hdr"), OtherType: "identity",
	}))

	wantEdges, wantRev, err := adjacency.Neighbors(ctx, adjKV, coreKV, node)
	require.NoError(t, err)
	require.Len(t, wantEdges, 2)

	edges, fingerprint, whole, err := adjacency.NeighborsScoped(ctx, adjKV, coreKV, node, relationSet("instanceOf"))
	require.NoError(t, err)
	require.True(t, whole)
	require.Equal(t, wantEdges, edges, "the unmarked arm answers with the whole document, unfiltered")
	require.Equal(t, wantRev, fingerprint, "and with a fingerprint comparable with Neighbors'")
}

// TestNeighborsScoped_UnmarkedNodeWithNoDocument pins the absent document: an
// empty (non-nil) answer, fingerprint 0, and whole=true — 0 being a recorded
// fingerprint, not a missing one, so a node that gains a document later reads
// as drift.
func TestNeighborsScoped_UnmarkedNodeWithNoDocument(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	edges, fingerprint, whole, err := adjacency.NeighborsScoped(ctx, adjKV, coreKV, nanoID(t, "gone"), relationSet("holdsRole"))
	require.NoError(t, err)
	require.True(t, whole)
	require.NotNil(t, edges)
	require.Empty(t, edges)
	require.Zero(t, fingerprint)
}

// TestNeighborsScoped_EmptyRelationsIsAnError pins the refusal on both node
// shapes. A quiet empty answer here would be a short edge list for the one
// node class where a short list is the failure the overflow latch exists to
// prevent, and no caller could tell it apart from a hub that genuinely holds
// no links of the relations it asked for. The refusal is decided before the
// node is read at all, so it does not depend on the node's shape.
func TestNeighborsScoped_EmptyRelationsIsAnError(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	hub := nanoID(t, "hub")
	markNode(t, adjKV, hub)
	seedLink(t, coreKV, "identity", hub, "holdsRole", "role", nanoID(t, "rza"), false)

	plain := nanoID(t, "svc")
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: substrate.LinkKey("service", plain, "instanceOf", "meta", nanoID(t, "meta")),
		EdgeID:    "e-instanceOf", Name: "instanceOf",
		Direction: "outbound", NodeID: plain, OtherNodeID: nanoID(t, "meta"), OtherType: "meta",
	}))

	for _, tc := range []struct {
		name   string
		nodeID string
		rels   map[string]struct{}
	}{
		{"marked node, nil rels", hub, nil},
		{"marked node, empty rels", hub, relationSet()},
		{"unmarked node, nil rels", plain, nil},
		{"unmarked node, empty rels", plain, relationSet()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := adjacency.NeighborsScoped(ctx, adjKV, coreKV, tc.nodeID, tc.rels)
			require.Error(t, err, "an empty scope must be refused, never answered with an empty edge list")
			require.ErrorContains(t, err, "at least one relation is required")
		})
	}

	// The contrast that makes the refusal meaningful: NeighborsByRelation
	// keeps its own documented empty-set contract, reading nothing and
	// answering with no edges, because there the empty set is the caller
	// saying it has proven it follows nothing.
	edges, fingerprint, err := adjacency.NeighborsByRelation(ctx, adjKV, coreKV, hub, relationSet())
	require.NoError(t, err)
	require.NotNil(t, edges)
	require.Empty(t, edges)
	require.Zero(t, fingerprint)
}

// TestNeighborsScoped_MarkedNodeNeedsCoreKV pins that a marked node with no
// Core KV handle is an error, never a silently short edge list: the handle is
// optional only for a caller that never reaches a marked node.
func TestNeighborsScoped_MarkedNodeNeedsCoreKV(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, _ := startKVs(t)

	hub := nanoID(t, "hub")
	markNode(t, adjKV, hub)

	_, _, _, err := adjacency.NeighborsScoped(ctx, adjKV, nil, hub, relationSet("holdsRole"))
	require.Error(t, err)
	require.ErrorContains(t, err, "needs a Core KV handle")
}

// TestNeighborsByRelation_WidenedReadFingerprintsOnlyTheScope pins the
// fingerprint of a read that could not be narrowed at the subject level. A
// relation name that is not a single subject token cannot be pinned in a
// filter, so linkFiltersFor widens the READ to the unscoped pair — but the
// answer is still cut to the scope in memory, and the fingerprint has to
// follow the answer. Hashing everything the widened filters matched would pin
// relations the caller never asked for, so an out-of-scope write would report
// drift, and one relation's fingerprint would depend on whether some other
// relation's name happened to be spellable.
func TestNeighborsByRelation_WidenedReadFingerprintsOnlyTheScope(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	hub := nanoID(t, "hub")
	markNode(t, adjKV, hub)
	held := seedLink(t, coreKV, "identity", hub, "holdsRole", "role", nanoID(t, "rza"), false)
	seedLink(t, coreKV, "identity", hub, "worksAt", "org", nanoID(t, "org"), false)

	// "not a.token" carries a separator, so it cannot stand as one subject
	// segment and the whole read widens to the unscoped pair.
	scope := relationSet("holdsRole", "not a.token")

	edges, fingerprint, err := adjacency.NeighborsByRelation(ctx, adjKV, coreKV, hub, scope)
	require.NoError(t, err)
	require.Len(t, edges, 1, "the answer is still cut to the scope, whatever the filters matched")
	require.Equal(t, held, edges[0].EdgeID)

	// A write to a relation outside the scope. The widened read matches it;
	// the fingerprint must not.
	seedLink(t, coreKV, "identity", hub, "worksAt", "org", nanoID(t, "orgb"), false)

	after, afterFingerprint, err := adjacency.NeighborsByRelation(ctx, adjKV, coreKV, hub, scope)
	require.NoError(t, err)
	require.Equal(t, edges, after)
	require.Equal(t, fingerprint, afterFingerprint,
		"a write to a relation outside the scope must not move a scoped fingerprint, even when the READ was widened")

	// The control: a write INSIDE the scope does move it.
	seedLink(t, coreKV, "identity", hub, "holdsRole", "role", nanoID(t, "rzb"), false)
	_, movedFingerprint, err := adjacency.NeighborsByRelation(ctx, adjKV, coreKV, hub, scope)
	require.NoError(t, err)
	require.NotEqual(t, fingerprint, movedFingerprint, "a write inside the scope is drift")
}
