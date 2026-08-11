package full

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// seedPeakRowsCorpus writes one identity with taskCount inbound `assignedTo`
// tasks and roleCount outbound `holdsRole` roles, and returns the identity's
// Contract #1 vertex key. The two fan-outs differ so a test can tell which
// stage a reported peak came from.
func seedPeakRowsCorpus(t *testing.T, adjKV, coreKV *substrate.KV, taskCount, roleCount int) string {
	t.Helper()
	reg := newFixtureRegistry()
	identityKey := putVertex(t, reg, coreKV, "peakAlice", "identity", nil)
	for i := 0; i < taskCount; i++ {
		name := "peakTask" + string(rune('a'+i))
		putVertex(t, reg, coreKV, name, "task", nil)
		putEdge(t, reg, adjKV, "assignedTo", name, "peakAlice")
	}
	for i := 0; i < roleCount; i++ {
		name := "peakRole" + string(rune('a'+i))
		putVertex(t, reg, coreKV, name, "role", nil)
		putEdge(t, reg, adjKV, "holdsRole", "peakAlice", name)
	}
	return identityKey
}

// peakRowsCypher expands one anchor across its tasks, folds them into a single
// aggregated row, and then expands again across a NARROWER second fan-out. The
// per-stage binding counts are therefore 1 → tasks → 1 → roles: with tasks the
// widest of them, a reported peak that equalled the first stage, the final
// stage, or the last stage to be measured would each name a different number.
const peakRowsCypher = `MATCH (i:identity {key: $k}) ` +
	`OPTIONAL MATCH (i)<-[:assignedTo]-(t:task) ` +
	`WITH i, collect(DISTINCT t.key) AS tasks ` +
	`OPTIONAL MATCH (i)-[:holdsRole]->(r:role) ` +
	`RETURN i.key AS key, collect(DISTINCT r.key) AS roles`

// TestExecuteWithStats_ReportsTheHighWaterMarkNotTheFinalRowCount pins that
// PeakBindingRows is the widest binding set the evaluation ever materialized,
// not the row count it ended on and not the one it started from.
func TestExecuteWithStats_ReportsTheHighWaterMarkNotTheFinalRowCount(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	const tasks, roles = 7, 2
	identityKey := seedPeakRowsCorpus(t, adjKV, coreKV, tasks, roles)

	eng := New()
	cr, err := eng.Parse(peakRowsCypher)
	require.NoError(t, err)

	results, _, stats, err := eng.ExecuteWithStats(context.Background(), cr,
		ruleengine.EventContext{Parameters: map[string]any{"k": identityKey}}, adjKV, coreKV)
	require.NoError(t, err)

	// The projection collapses to one row per anchor — so the final binding
	// count is 1, and the peak must not be it.
	require.Len(t, results, 1)
	gotRoles, ok := results[0].Values["roles"].([]any)
	require.True(t, ok, "roles must project as a list: %#v", results[0].Values["roles"])
	require.Len(t, gotRoles, roles)

	require.Equal(t, tasks, stats.PeakBindingRows,
		"peak must be the widest stage (%d task rows), not the first stage (1), "+
			"the final row count (%d), or the last stage measured (%d roles)",
		tasks, len(results), roles)
}

// TestExecuteWithStats_RefusedEvaluationStillReportsItsPeak pins the operator
// consumer: a cap refusal is exactly when peak rows matters, so the number must
// survive the error return instead of being lost with the discarded results.
//
// The uncapped run in the first half is the positive vector — it proves the
// corpus really does reach 7 rows, so the capped half's assertion is measuring
// a refusal rather than an unrelated failure.
func TestExecuteWithStats_RefusedEvaluationStillReportsItsPeak(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	const tasks = 7
	identityKey := seedPeakRowsCorpus(t, adjKV, coreKV, tasks, 2)
	ec := ruleengine.EventContext{Parameters: map[string]any{"k": identityKey}}

	cr, err := New().Parse(peakRowsCypher)
	require.NoError(t, err)

	_, _, uncapped, err := New().ExecuteWithStats(context.Background(), cr, ec, adjKV, coreKV)
	require.NoError(t, err, "the same corpus must evaluate cleanly with no cap")
	require.Equal(t, tasks, uncapped.PeakBindingRows)

	const rowCap = 3
	results, _, refused, err := New().WithMaxBindings(rowCap).
		ExecuteWithStats(context.Background(), cr, ec, adjKV, coreKV)
	require.Error(t, err, "a %d-row binding set must be refused under a cap of %d", tasks, rowCap)
	require.ErrorContains(t, err, "over the cap")
	require.Nil(t, results, "a refused evaluation returns no rows")

	require.Equal(t, tasks, refused.PeakBindingRows,
		"the refusal must report the row count that tripped the cap, not zero")
	require.Greater(t, refused.PeakBindingRows, rowCap,
		"the reported peak is what an operator compares against the cap")
}

// TestExecuteWithStats_ZeroWhenNothingMaterializes pins that the gauge reports
// a real zero for an evaluation whose anchor matched nothing, rather than
// inheriting a number from anywhere else.
func TestExecuteWithStats_ZeroWhenNothingMaterializes(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	seedPeakRowsCorpus(t, adjKV, coreKV, 7, 2)

	cr, err := New().Parse(peakRowsCypher)
	require.NoError(t, err)

	results, _, stats, err := New().ExecuteWithStats(context.Background(), cr,
		ruleengine.EventContext{Parameters: map[string]any{"k": "vtx.identity." + c1NanoID("noSuchAnchor")}},
		adjKV, coreKV)
	require.NoError(t, err)
	require.Empty(t, results)
	require.Equal(t, 0, stats.PeakBindingRows)
}

// TestExecuteWithFootprint_DelegatesWithoutChangingItsContract pins that the
// pre-existing three-return entry point still answers identically to the
// stats-carrying one it now delegates to.
func TestExecuteWithFootprint_DelegatesWithoutChangingItsContract(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	identityKey := seedPeakRowsCorpus(t, adjKV, coreKV, 7, 2)
	ec := ruleengine.EventContext{Parameters: map[string]any{"k": identityKey}}

	eng := New()
	cr, err := eng.Parse(peakRowsCypher)
	require.NoError(t, err)

	wantRows, wantPrint, _, err := eng.ExecuteWithStats(context.Background(), cr, ec, adjKV, coreKV)
	require.NoError(t, err)
	gotRows, gotPrint, err := eng.ExecuteWithFootprint(context.Background(), cr, ec, adjKV, coreKV)
	require.NoError(t, err)

	require.Equal(t, wantRows, gotRows)
	require.Equal(t, wantPrint.NodeRevisions, gotPrint.NodeRevisions)
	require.Equal(t, wantPrint.EdgeRevisions, gotPrint.EdgeRevisions)
}
