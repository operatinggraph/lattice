package full

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
)

// TestExecutor_Footprint_CapturesNodeAndEdgeRevisions pins footprint()'s
// contract: it renders the executor's already-populated node and edge memos
// into a ruleengine.EvalFootprint with no extra reads — a present node/edge
// carries the revision it was read at, and an absent node (memoized nil)
// carries revision 0.
func TestExecutor_Footprint_CapturesNodeAndEdgeRevisions(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	hub := putVertex(t, reg, coreKV, "hub", "identity", nil)
	putVertex(t, reg, coreKV, "svcA", "service", nil)
	putEdge(t, reg, adjKV, "providedTo", "svcA", "hub")

	ex := newTestExecutor(adjKV, coreKV)
	hubRef, err := ex.fetchNode(hub)
	require.NoError(t, err)
	require.NotNil(t, hubRef)

	hubID := reg.idByName["hub"]
	_, err = ex.fetchEdges(hubID)
	require.NoError(t, err)

	absentRef, err := ex.fetchNode("vtx.identity.doesNotExistAAAAAAAAA")
	require.NoError(t, err)
	require.Nil(t, absentRef)

	fp := ex.footprint()
	require.Equal(t, hubRef.revision, fp.NodeRevisions[hub])
	require.NotZero(t, fp.NodeRevisions[hub])
	require.Contains(t, fp.NodeRevisions, "vtx.identity.doesNotExistAAAAAAAAA")
	require.Zero(t, fp.NodeRevisions["vtx.identity.doesNotExistAAAAAAAAA"])
	require.Equal(t, ex.edgeRevisions[hubID], fp.EdgeRevisions[hubID])
	require.NotZero(t, fp.EdgeRevisions[hubID])
}

// TestExecuteWithFootprint_HookFiresAfterFootprintBuilt pins the evaluator
// pause hook (WithFootprintCapturedHook): it fires exactly once per
// ExecuteWithFootprint call, after the footprint is fully built, and the
// footprint returned still reflects the PRE-hook state even when the hook
// itself commits a mutation to a footprinted key — proving the footprint is a
// snapshot taken at read time, not a live view. This is the seam a future
// E2E scripted-interleave test (the capabilityEphemeral role-queue tear,
// evaluation-consistency-design.md §9) commits a mid-evaluation mutation
// through; that test is out of scope for this increment (needs real lens
// installs), so this pins only the hook mechanism itself.
func TestExecuteWithFootprint_HookFiresAfterFootprintBuilt(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	unit := putVertex(t, reg, coreKV, "unit", "unit", nil)

	eng := New()
	cr, err := eng.Parse(`MATCH (u:unit {key: $k}) RETURN u.key AS k`)
	require.NoError(t, err)

	calls := 0
	ctx := WithFootprintCapturedHook(context.Background(), func() {
		calls++
		// Commit a mutation to the footprinted key from inside the hook — the
		// footprint ExecuteWithFootprint is about to return must still carry
		// the revision it read, not this one.
		putVertex(t, reg, coreKV, "unit", "unit", map[string]any{"touched": true})
	})

	_, footprint, err := eng.ExecuteWithFootprint(ctx, cr,
		ruleengine.EventContext{Parameters: map[string]any{"k": unit}}, adjKV, coreKV)
	require.NoError(t, err)
	require.Equal(t, 1, calls, "the hook must fire exactly once per ExecuteWithFootprint call")

	entry, gerr := coreKV.Get(context.Background(), unit)
	require.NoError(t, gerr)
	require.NotEqual(t, footprint.NodeRevisions[unit], entry.Revision,
		"the mutation committed inside the hook must have landed AFTER the footprint was captured")
}

// TestExecuteWith_NeverInvokesTheHook pins that the pre-existing ExecuteWith
// entry point — every packages/* lens test's call shape, untouched by this
// increment — never even looks at a footprint hook installed on its ctx: it
// is a thin wrapper over ExecuteWithFootprint that discards the footprint,
// but the hook itself is invoked unconditionally by the shared core, so this
// pins that ExecuteWith still fires it (proving the wrapper truly shares the
// core rather than diverging).
func TestExecuteWith_StillInvokesTheHookThroughTheSharedCore(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	unit := putVertex(t, reg, coreKV, "unit", "unit", nil)

	eng := New()
	cr, err := eng.Parse(`MATCH (u:unit {key: $k}) RETURN u.key AS k`)
	require.NoError(t, err)

	calls := 0
	ctx := WithFootprintCapturedHook(context.Background(), func() { calls++ })

	_, err = eng.ExecuteWith(ctx, cr,
		ruleengine.EventContext{Parameters: map[string]any{"k": unit}}, adjKV, coreKV)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
}
