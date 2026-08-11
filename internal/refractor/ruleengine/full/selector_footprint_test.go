package full

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// Each test below re-derives the CURRENT edge-identity set for a selector
// against a fresh adjacency read inline — exactly the comparison
// pipeline.footprintValid performs (evaluate.go), reproduced here since
// footprintValid lives in package pipeline, which itself imports package
// full (UseFullEngine) — importing it back from a full-package test would
// cycle.

// TestExecutor_SelectorScopedFootprint_UnrelatedEdgeAdded_NoDrift pins
// §13.4's core claim: a walk following ONE typed relation (queuedFor) on a
// node footprints only that selector's matched edge set, so an unrelated
// relation (grantedBy) landing on the SAME node between footprint capture
// and validation does not change what the selector re-derives — no drift,
// even though the node's whole adjacency document DID change underneath it.
func TestExecutor_SelectorScopedFootprint_UnrelatedEdgeAdded_NoDrift(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "role", "role", nil)
	putVertex(t, reg, coreKV, "task1", "task", nil)
	putEdge(t, reg, adjKV, "queuedFor", "task1", "role")

	eng := New()
	cr, err := eng.Parse(`MATCH (r:role {key: $k})<-[:queuedFor]-(t:task) RETURN t.key AS taskKey`)
	require.NoError(t, err)

	fired := false
	hookCtx := WithFootprintCapturedHook(context.Background(), func() {
		fired = true
		// An UNRELATED grantedBy edge lands on the SAME role node —
		// defect 2's exact mechanism (a shared hub node under write
		// pressure from a relation the walk never followed).
		putVertex(t, reg, coreKV, "perm1", "permission", nil)
		putEdge(t, reg, adjKV, "grantedBy", "perm1", "role")
	})

	_, fp, err := eng.ExecuteWithFootprint(hookCtx, cr,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "role")}},
		adjKV, coreKV)
	require.NoError(t, err)
	require.True(t, fired, "the mid-evaluation mutation must have run")

	roleID := reg.idByName["role"]
	sel, ok := fp.EdgeSelectors[roleID]
	require.True(t, ok, "the role node must carry a recorded selector — traverseRel's only caller is fetchEdges' sole call site")
	require.False(t, sel.Fallback, "a typed queuedFor hop must not fall back to whole-document comparison")
	require.Len(t, sel.Matched, 1, "exactly one selector was consulted on this node")

	edges, _, err := adjacency.Neighbors(context.Background(), adjKV, coreKV, roleID)
	require.NoError(t, err)
	for selector, wantIDs := range sel.Matched {
		require.Equal(t, "queuedFor", selector.RelType)
		gotIDs := map[string]struct{}{}
		for _, e := range edges {
			if e.Name != selector.RelType || !adjacency.DirectionMatches(e.Direction, selector.Direction) {
				continue
			}
			gotIDs[e.EdgeID] = struct{}{}
		}
		require.Equalf(t, wantIDs, gotIDs,
			"the queuedFor-matched ID set must be UNCHANGED by the unrelated grantedBy write — selector %+v", selector)
	}
}

// TestExecutor_SelectorScopedFootprint_RelatedEdgeAdded_Drift is the
// positive twin: a SECOND queuedFor edge landing on the same node DOES
// change the queuedFor selector's matched set — the selector-scoping must
// never mask a change the evaluation actually depended on.
func TestExecutor_SelectorScopedFootprint_RelatedEdgeAdded_Drift(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "role", "role", nil)
	putVertex(t, reg, coreKV, "task1", "task", nil)
	putEdge(t, reg, adjKV, "queuedFor", "task1", "role")

	eng := New()
	cr, err := eng.Parse(`MATCH (r:role {key: $k})<-[:queuedFor]-(t:task) RETURN t.key AS taskKey`)
	require.NoError(t, err)

	fired := false
	hookCtx := WithFootprintCapturedHook(context.Background(), func() {
		fired = true
		// A SECOND task queues for the SAME role — a relevant change to
		// exactly the selector this walk consulted.
		putVertex(t, reg, coreKV, "task2", "task", nil)
		putEdge(t, reg, adjKV, "queuedFor", "task2", "role")
	})

	_, fp, err := eng.ExecuteWithFootprint(hookCtx, cr,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "role")}},
		adjKV, coreKV)
	require.NoError(t, err)
	require.True(t, fired)

	roleID := reg.idByName["role"]
	sel, ok := fp.EdgeSelectors[roleID]
	require.True(t, ok)
	require.False(t, sel.Fallback)

	edges, _, err := adjacency.Neighbors(context.Background(), adjKV, coreKV, roleID)
	require.NoError(t, err)
	drifted := false
	for selector, wantIDs := range sel.Matched {
		gotIDs := map[string]struct{}{}
		for _, e := range edges {
			if e.Name != selector.RelType || !adjacency.DirectionMatches(e.Direction, selector.Direction) {
				continue
			}
			gotIDs[e.EdgeID] = struct{}{}
		}
		if len(gotIDs) != len(wantIDs) {
			drifted = true
			continue
		}
		for id := range gotIDs {
			if _, still := wantIDs[id]; !still {
				drifted = true
			}
		}
	}
	require.True(t, drifted, "a second queuedFor edge on the SAME node must register as drift on the queuedFor selector")
}

// TestExecutor_SelectorScopedFootprint_UntypedHop_FallsBackAndDriftsOnEither
// proves the fallback is genuinely coarser, not accidentally also scoped: an
// untyped hop (bare `<--`, no relationship type — RelPattern.Type=="")
// consumes every edge on the node regardless of type, so it must fall back
// to whole-document revision comparison, which drifts on EITHER a related
// or an unrelated edge landing on the node.
func TestExecutor_SelectorScopedFootprint_UntypedHop_FallsBackAndDriftsOnEither(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "role", "role", nil)
	putVertex(t, reg, coreKV, "task1", "task", nil)
	putEdge(t, reg, adjKV, "queuedFor", "task1", "role")

	eng := New()
	// Bare arrow, no [:type] — RelPattern.Type stays "" (untyped: consumes
	// every edge on the node regardless of relation name).
	cr, err := eng.Parse(`MATCH (r:role {key: $k})<--(x) RETURN x.key AS xkey`)
	require.NoError(t, err)

	assertFallbackDrifts := func(t *testing.T, mutate func()) {
		t.Helper()
		fired := false
		hookCtx := WithFootprintCapturedHook(context.Background(), func() {
			fired = true
			mutate()
		})
		_, fp, err := eng.ExecuteWithFootprint(hookCtx, cr,
			ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "role")}},
			adjKV, coreKV)
		require.NoError(t, err)
		require.True(t, fired)

		roleID := reg.idByName["role"]
		sel, ok := fp.EdgeSelectors[roleID]
		require.True(t, ok)
		require.True(t, sel.Fallback, "an untyped hop must fall back to whole-document comparison")

		wantRev := fp.EdgeRevisions[roleID]
		_, gotRev, err := adjacency.Neighbors(context.Background(), adjKV, coreKV, roleID)
		require.NoError(t, err)
		require.NotEqualf(t, wantRev, gotRev,
			"the whole-document revision must have moved (fallback path) after the mutation")
	}

	t.Run("unrelated edge", func(t *testing.T) {
		assertFallbackDrifts(t, func() {
			putVertex(t, reg, coreKV, "permU", "permission", nil)
			putEdge(t, reg, adjKV, "grantedBy", "permU", "role")
		})
	})
	t.Run("related edge", func(t *testing.T) {
		assertFallbackDrifts(t, func() {
			putVertex(t, reg, coreKV, "taskR", "task", nil)
			putEdge(t, reg, adjKV, "queuedFor", "taskR", "role")
		})
	})
}

// markNodeOverflowed sets a node's overflow latch, so its edges come from Core
// KV's link keyspace instead of its adjacency document.
func markNodeOverflowed(t *testing.T, adjKV *substrate.KV, nodeID string) {
	t.Helper()
	_, err := adjKV.Create(context.Background(), subjects.AdjMarkKey(nodeID), []byte(`{"degree":9999}`))
	require.NoError(t, err)
}

// putLink writes a Contract #1 link envelope into Core KV WITHOUT indexing it
// into the adjacency document — the state of every edge on a latched node,
// where the write path stops absorbing edges and Core KV is the only record.
func putLink(t *testing.T, reg *fixtureRegistry, coreKV *substrate.KV, name, fromName, toName string) string {
	t.Helper()
	key, _ := putLinkBody(t, reg, coreKV, name, fromName, toName, map[string]any{"isDeleted": false})
	return key
}

// TestExecutor_TraversesAnOverflowMarkedNode runs a real query across a node
// whose adjacency document holds nothing, because it is latched. Everything
// the walk needs — the neighbour's id, its TYPE (to rebuild its vertex key for
// the labelled pattern node), the relation, the direction — has to come out of
// the link keys alone, and the footprint has to come back with the selector
// the hop consulted.
func TestExecutor_TraversesAnOverflowMarkedNode(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "role", "role", nil)
	putVertex(t, reg, coreKV, "task1", "task", nil)
	putVertex(t, reg, coreKV, "task2", "task", nil)

	roleID := reg.idByName["role"]
	markNodeOverflowed(t, adjKV, roleID)
	putLink(t, reg, coreKV, "queuedFor", "task1", "role")
	putLink(t, reg, coreKV, "queuedFor", "task2", "role")

	eng := New()
	cr, err := eng.Parse(`MATCH (r:role {key: $k})<-[:queuedFor]-(t:task) RETURN t.key AS taskKey`)
	require.NoError(t, err)

	rows, fp, err := eng.ExecuteWithFootprint(context.Background(), cr,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "role")}},
		adjKV, coreKV)
	require.NoError(t, err)

	got := make([]string, 0, len(rows))
	for _, row := range rows {
		got = append(got, row.Values["taskKey"].(string))
	}
	require.ElementsMatch(t, []string{vtxKey(reg, "task1"), vtxKey(reg, "task2")}, got,
		"a latched node's neighbours must resolve through the Core KV fallback")

	sel, ok := fp.EdgeSelectors[roleID]
	require.True(t, ok, "the hop must still record its selector on a marked node")
	require.False(t, sel.Fallback)
	require.NotZero(t, fp.EdgeRevisions[roleID], "and carry the fallback's fingerprint")
}
