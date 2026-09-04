package pipeline

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// scopedLeaseIDs are the three anchors the fixture actor holds. Valid 20-char
// NanoIDs, so the "anchor" alias each row carries is a Contract #1 vertex key
// the scope's own parse accepts.
var scopedLeaseIDs = []string{
	"Lmn4kPmRtw9nbCxz5vQ2",
	"Mpq3TmZpq7RvwNsY2Hc9",
	"Nrs7RvwKx3TmZpq2Hc9L",
}

// newScopedPersonalFixture builds a personal pipeline over an actor holding
// three leases, so a scoped publish has rows to admit AND rows to withhold —
// a one-row actor could not tell a working scope from a broken one.
func newScopedPersonalFixture(t *testing.T) (*Pipeline, *fakePersonalTarget) {
	t.Helper()
	target := &fakePersonalTarget{}
	coreKV, adjKV := newDeleteKeyKV(t)
	p := newPersonalPipelineOn(t, coreKV, adjKV, target)

	putPersonalVertex(t, coreKV, substrate.VertexKey("identity", personalActorA), "identity", nil)
	edges := make([]adjacency.EdgeEntry, 0, len(scopedLeaseIDs))
	for i, leaseID := range scopedLeaseIDs {
		putPersonalVertex(t, coreKV, substrate.VertexKey("lease", leaseID), "lease",
			map[string]any{"id": "lease-" + string(rune('a'+i))})
		linkKey := putPersonalLink(t, coreKV, "identity", personalActorA, "holds", "lease", leaseID)
		edges = append(edges, adjacency.EdgeEntry{
			CoreKvKey:   linkKey,
			EdgeID:      linkKey,
			Name:        "holds",
			Direction:   "outbound",
			OtherNodeID: leaseID,
			OtherType:   "lease",
		})
	}
	putPersonalAdjacency(t, adjKV, personalActorA, edges)
	return p, target
}

// putPersonalLink writes one Contract #1 link envelope the full engine reads
// the edge's own body from, and returns its key.
func putPersonalLink(t *testing.T, kv *substrate.KV, srcType, srcID, relation, dstType, dstID string) string {
	t.Helper()
	linkKey := substrate.LinkKey(srcType, srcID, relation, dstType, dstID)
	body, err := json.Marshal(map[string]any{
		"key": linkKey, "class": relation, "isDeleted": false,
		"sourceVertex": substrate.VertexKey(srcType, srcID),
		"targetVertex": substrate.VertexKey(dstType, dstID),
		"localName":    relation,
	})
	require.NoError(t, err)
	_, err = kv.Put(context.Background(), linkKey, body)
	require.NoError(t, err)
	return linkKey
}

// putPersonalAdjacency writes the node's adjacency document, which is what the
// traversal reads — the CDC write path builds it in production, and these
// fixtures never run a consumer.
func putPersonalAdjacency(t *testing.T, adjKV *substrate.KV, nodeID string, edges []adjacency.EdgeEntry) {
	t.Helper()
	body, err := json.Marshal(adjacency.AdjValue{Edges: edges})
	require.NoError(t, err)
	_, err = adjKV.Put(context.Background(), subjects.AdjKey(nodeID), body)
	require.NoError(t, err)
}

// upsertKeys is the "key" of every upsert the target received, which is what a
// scoped publish is measured in.
func (f *fakePersonalTarget) upsertKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.upserts))
	for _, keys := range f.upserts {
		k, _ := keys["entityId"].(string)
		out = append(out, k)
	}
	return out
}

// frameEntityIDs is the entityId of every key one frame names.
func frameEntityIDs(t *testing.T, frame personalFrame) []string {
	t.Helper()
	out := make([]string, 0, len(frame.keys))
	for _, keys := range frame.keys {
		id, ok := keys["entityId"].(string)
		require.True(t, ok, "every framed key carries the lens's key column")
		out = append(out, id)
	}
	return out
}

// TestReprojectPersonalActor_ScopeNonePublishesTheFrameAlone is T3's ScopeNone
// arm (personal-lens-delta-publication-design.md §10).
//
// The healer's ordinary pass takes it: nothing reads what a pass published, and
// the frame is the product of both inclusion gates, so a frames-only pass
// re-asks exactly what the standing healer exists to re-ask. Both halves are
// asserted together — withholding the rows is worthless if the frame stops
// naming them, because the client prunes every key the frame omits.
func TestReprojectPersonalActor_ScopeNonePublishesTheFrameAlone(t *testing.T) {
	p, target := newScopedPersonalFixture(t)

	require.NoError(t, p.ReprojectPersonalActor(context.Background(), personalActorA, ScopeNone()))

	assert.Empty(t, target.upsertKeys(), "a frames-only pass writes no row")
	frames := target.snapshot()
	require.Len(t, frames, 1, "and still publishes exactly one authoritative frame")
	assert.ElementsMatch(t, []string{"lease-a", "lease-b", "lease-c"}, frameEntityIDs(t, frames[0]),
		"the frame names every row the actor holds — a key it omitted would be pruned on the device")
}

// TestReprojectPersonalActor_ScopeAnchorsPublishesOnlyTheNamedAnchorsRow is
// T3's ScopeAnchors arm: a grant landing for one anchor republishes that
// anchor's row and nothing else's, while the frame still names the whole set.
func TestReprojectPersonalActor_ScopeAnchorsPublishesOnlyTheNamedAnchorsRow(t *testing.T) {
	p, target := newScopedPersonalFixture(t)

	scope := ScopeAnchors([]string{scopedLeaseIDs[1]})
	require.NoError(t, p.ReprojectPersonalActor(context.Background(), personalActorA, scope))

	assert.Equal(t, []string{"lease-b"}, target.upsertKeys(),
		"exactly the rows the scope admits are written")
	frames := target.snapshot()
	require.Len(t, frames, 1)
	assert.ElementsMatch(t, []string{"lease-a", "lease-b", "lease-c"}, frameEntityIDs(t, frames[0]),
		"the two withheld rows are unchanged on the device, and the frame is what keeps them there")
}

// TestReprojectPersonalActor_ScopeAnchorsAdmitsEveryNamedAnchor pins that the
// set is a set: a coalesced scope naming two anchors publishes both rows.
func TestReprojectPersonalActor_ScopeAnchorsAdmitsEveryNamedAnchor(t *testing.T) {
	p, target := newScopedPersonalFixture(t)

	scope := ScopeAnchors([]string{scopedLeaseIDs[0], scopedLeaseIDs[2]})
	require.NoError(t, p.ReprojectPersonalActor(context.Background(), personalActorA, scope))

	assert.ElementsMatch(t, []string{"lease-a", "lease-c"}, target.upsertKeys())
}

// TestReprojectPersonalActor_ScopeAllPublishesEveryRow is the unchanged
// behaviour every caller that passes no scope still gets — asserted through
// BOTH the explicit constructor and the zero value, because the zero value is
// what a forgotten scope reproduces and its failure on the wire is silent.
func TestReprojectPersonalActor_ScopeAllPublishesEveryRow(t *testing.T) {
	for name, scope := range map[string]PublishScope{
		"ScopeAll":       ScopeAll(),
		"the zero value": {},
	} {
		t.Run(name, func(t *testing.T) {
			p, target := newScopedPersonalFixture(t)

			require.NoError(t, p.ReprojectPersonalActor(context.Background(), personalActorA, scope))

			assert.ElementsMatch(t, []string{"lease-a", "lease-b", "lease-c"}, target.upsertKeys())
			frames := target.snapshot()
			require.Len(t, frames, 1)
			assert.Len(t, frameEntityIDs(t, frames[0]), 3)
		})
	}
}

// TestPersonalFramePublish_AdvancesTheFreshnessClock pins which publishes are
// real output on the read-model's last-touch clock.
//
// A SIGNALLED reprojection's frame stamps it — the frame is the whole answer to
// a drain signal, an interest change or a content cycle whenever the admitted
// row set is empty. The standing healer's frames-only pass does NOT: it reaches
// every registered personal lens every pass, so a stamp there would advance
// every personal lens's clock forever and LensProjectionStalled — lag sustained
// AND lastProjectedAt not advancing — could never fire on one again.
func TestPersonalFramePublish_AdvancesTheFreshnessClock(t *testing.T) {
	t.Run("a frames-only healer pass does NOT stamp it", func(t *testing.T) {
		p, target := newScopedPersonalFixture(t)
		require.True(t, p.Progress().LastProjectedAt.IsZero(), "nothing has been published yet")

		require.NoError(t, p.ReprojectPersonalActor(context.Background(), personalActorA, ScopeNone()))

		require.Len(t, target.snapshot(), 1, "the pass did publish its frame")
		assert.True(t, p.Progress().LastProjectedAt.IsZero(),
			"the healer re-asks the inclusion gates on its own clock; stamping here is what silences LensProjectionStalled")
	})

	t.Run("a signalled whole-actor reprojection stamps it", func(t *testing.T) {
		p, _ := newScopedPersonalFixture(t)
		require.True(t, p.Progress().LastProjectedAt.IsZero())

		require.NoError(t, p.ReprojectPersonalActor(context.Background(), personalActorA, ScopeAll()))

		assert.False(t, p.Progress().LastProjectedAt.IsZero())
	})

	t.Run("a signalled grant-scoped reprojection stamps it", func(t *testing.T) {
		p, _ := newScopedPersonalFixture(t)
		require.True(t, p.Progress().LastProjectedAt.IsZero())

		scope := ScopeAnchors([]string{scopedLeaseIDs[1]})
		require.NoError(t, p.ReprojectPersonalActor(context.Background(), personalActorA, scope))

		assert.False(t, p.Progress().LastProjectedAt.IsZero())
	})

	t.Run("a hydrate stamps it", func(t *testing.T) {
		p, _ := newScopedPersonalFixture(t)
		require.True(t, p.Progress().LastProjectedAt.IsZero())

		_, err := p.Hydrate(context.Background(), personalActorA)
		require.NoError(t, err)

		assert.False(t, p.Progress().LastProjectedAt.IsZero())
	})

	t.Run("a signalled reprojection of an empty actor stamps it too", func(t *testing.T) {
		// The row loop writes nothing for an actor that holds no rows, so the
		// frame is the ONLY thing that can advance the clock here — which is
		// what makes this vector the one a row-only stamp fails. It is also the
		// retraction: an actor who may now read nothing is retracted BY the
		// frame, so a signalled pass that produces it has produced output.
		target := &fakePersonalTarget{}
		p, _ := newPersonalTestPipeline(t, target)

		require.NoError(t, p.ReprojectPersonalActor(context.Background(), personalActorA, ScopeAll()))

		require.Len(t, target.snapshot(), 1, "an actor that reads nothing is still framed")
		assert.False(t, p.Progress().LastProjectedAt.IsZero())
	})
}
