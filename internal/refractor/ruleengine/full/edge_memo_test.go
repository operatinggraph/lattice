package full

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
)

// TestExec_EdgeReadIsRepeatableWithinOneEvaluation pins the adjacency twin of
// TestExec_AspectReadIsRepeatableWithinOneEvaluation: once an evaluation has
// read a node's edge list, a later access inside that SAME evaluation must
// observe the same edges, even though a concurrent commit has since appended
// to the node's adjacency document. Before the edge memo, every relationship
// hop read Adjacency KV live — so a variable-length traversal revisiting a
// frontier node (or two separate MATCH clauses walking through it) could
// observe two different edge lists for one node inside one evaluation, the
// link-read half of the class the node memo (e8d78278) closed for vertices.
func TestExec_EdgeReadIsRepeatableWithinOneEvaluation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "hub", "identity", nil)
	putVertex(t, reg, coreKV, "svcA", "service", nil)
	putEdge(t, reg, adjKV, "providedTo", "svcA", "hub")

	ex := &executor{
		ctx:           t.Context(),
		adjKV:         adjKV,
		coreKV:        coreKV,
		nodes:         map[string]*nodeRef{},
		edges:         map[string][]adjacency.EdgeEntry{},
		edgeRevisions: map[string]uint64{},
	}

	hubID := reg.idByName["hub"]
	first, err := ex.fetchEdges(hubID)
	require.NoError(t, err)
	require.Len(t, first, 1, "hub starts with one inbound providedTo edge")
	firstRevision := ex.edgeRevisions[hubID]

	// A commit lands mid-evaluation: a second service starts providing to hub.
	putVertex(t, reg, coreKV, "svcB", "service", nil)
	putEdge(t, reg, adjKV, "providedTo", "svcB", "hub")

	second, err := ex.fetchEdges(hubID)
	require.NoError(t, err)
	require.Len(t, second, 1,
		"a second access inside ONE evaluation must observe the edge list the evaluation already saw")
	require.Equal(t, firstRevision, ex.edgeRevisions[hubID],
		"the memoized revision must not move within one evaluation")

	// The memo is evaluation-scoped, never global: the NEXT evaluation must see
	// the committed edge, or the read model would never catch up.
	next := &executor{
		ctx:           t.Context(),
		adjKV:         adjKV,
		coreKV:        coreKV,
		nodes:         map[string]*nodeRef{},
		edges:         map[string][]adjacency.EdgeEntry{},
		edgeRevisions: map[string]uint64{},
	}
	fresh, err := next.fetchEdges(hubID)
	require.NoError(t, err)
	require.Len(t, fresh, 2, "a fresh evaluation must observe both providedTo edges")
}

// TestExec_NodeRevisionCapturedOnRead pins the vertex half of the footprint
// primitive: fetchNode's memoized nodeRef carries the Core KV revision it was
// read at, so a later validating caller (the footprint-validation seam this
// primitive is groundwork for) can detect a mid-evaluation write without a
// second read.
func TestExec_NodeRevisionCapturedOnRead(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	_, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	unit := putVertex(t, reg, coreKV, "unit", "unit", nil)

	ex := newTestExecutor(nil, coreKV)
	ref, err := ex.fetchNode(unit)
	require.NoError(t, err)
	require.NotNil(t, ref)
	require.NotZero(t, ref.revision, "a present key's memoized revision must be the KV revision it was read at")

	original := ref.revision
	// A commit lands: overwrite the vertex, bumping its revision.
	putVertex(t, reg, coreKV, "unit", "unit", map[string]any{"touched": true})

	// The evaluation-scoped memo still hands back the ORIGINAL nodeRef and
	// revision — repeatable-read, mirroring TestExec_AspectReadIsRepeatableWithinOneEvaluation.
	again, err := ex.fetchNode(unit)
	require.NoError(t, err)
	require.Equal(t, original, again.revision)

	next := newTestExecutor(nil, coreKV)
	fresh, err := next.fetchNode(unit)
	require.NoError(t, err)
	require.NotEqual(t, original, fresh.revision, "a fresh evaluation must observe the bumped revision")
}
