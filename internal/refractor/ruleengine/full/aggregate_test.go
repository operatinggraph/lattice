package full

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// crossProductFixture seeds n identity vertices. A comma-separated
// `MATCH (a:identity), (b:identity)` over them binds n² rows from n writes —
// the cheapest honest stand-in for the unanchored fan-out that took a live
// evaluation past 20 GB.
func crossProductFixture(t *testing.T, n int) (adjKV, coreKV *substrate.KV, reg *fixtureRegistry) {
	t.Helper()
	adjKV, coreKV = startExecKVs(t)
	reg = newFixtureRegistry()
	for i := range n {
		putVertex(t, reg, coreKV, fmt.Sprintf("id%d", i), "identity", nil)
	}
	return adjKV, coreKV, reg
}

// TestCheckCancelled_SamplesTheContext pins the cancellation checkpoint's
// sampling contract: it must observe a cancelled context within one interval,
// and must not pay ctx.Err() on every call.
func TestCheckCancelled_SamplesTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ex := &executor{ctx: ctx}

	for i := 1; i < cancelCheckInterval; i++ {
		require.NoError(t, ex.checkCancelled(),
			"call %d falls between samples and must not touch the context", i)
	}
	err := ex.checkCancelled()
	require.Error(t, err, "the sampled call must observe the cancelled context")
	require.ErrorIs(t, err, context.Canceled)
}

// TestExec_CancelledContextAbortsALargeEvaluation proves the checkpoint is
// actually wired into the hot loops: once an evaluation is large enough to
// reach a sample, a cancelled context stops it.
//
// This is what bounds graceful shutdown. Every read is memoized, so a large
// MATCH + aggregation pass makes no further KV calls — without an in-loop
// checkpoint the evaluation runs to completion after SIGTERM while the
// consumer supervisor's Stop and the process's WaitGroup both block on it.
func TestExec_CancelledContextAbortsALargeEvaluation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV, _ := crossProductFixture(t, 40)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	eng := New()
	cr, err := eng.Parse(`MATCH (a:identity), (b:identity) RETURN count(a) AS n`)
	require.NoError(t, err)
	_, err = eng.ExecuteWith(ctx, cr, ruleengine.EventContext{}, adjKV, coreKV)
	require.Error(t, err, "a cancelled context must abort the evaluation, not run it to completion")
	require.ErrorIs(t, err, context.Canceled)
}

// TestExec_BindingBudgetRefusesRatherThanTruncates pins the runaway guard's
// disposition. A truncated binding set would write a short count() to the read
// model, indistinguishable downstream from a correct one; refusing surfaces as
// an evaluation error the pipeline redelivers.
func TestExec_BindingBudgetRefusesRatherThanTruncates(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV, _ := crossProductFixture(t, 40)
	const body = `MATCH (a:identity), (b:identity) RETURN count(a) AS n`

	eng := New().WithMaxBindings(100)
	cr, err := eng.Parse(body)
	require.NoError(t, err)
	_, err = eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{}, adjKV, coreKV)
	require.Error(t, err, "1600 bindings over a cap of 100 must refuse the evaluation")
	require.Contains(t, err.Error(), "over the cap of 100",
		"the error must name the cap so an operator can raise it")

	// The cap must bound the PEAK, not merely report it afterwards: the check
	// lives inside each fan-out loop, so the set can overshoot by at most one
	// inner expansion (40 here) — never by the whole cross product (1600), which
	// is what a check placed after the loop would have allowed.
	matches := regexp.MustCompile(`reached (\d+) rows`).FindStringSubmatch(err.Error())
	require.Len(t, matches, 2, "the error must report the row count it refused at")
	refusedAt, convErr := strconv.Atoi(matches[1])
	require.NoError(t, convErr)
	require.Less(t, refusedAt, 200,
		"refusal must happen near the cap, not after materializing the whole cross product")

	// The same query under a cap that accommodates it projects the true count —
	// the guard is a backstop, not a workload limit.
	raised := New().WithMaxBindings(10_000)
	out, err := raised.ExecuteWith(context.Background(), cr, ruleengine.EventContext{}, adjKV, coreKV)
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, int64(1600), out[0].Values["n"])

	// WithMaxBindings copies: the engine it derived from keeps its own cap, so a
	// shared engine cannot be reconfigured by a caller derived from it.
	_, err = eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{}, adjKV, coreKV)
	require.Error(t, err, "the 100-cap engine must be unchanged by the derived 10k-cap engine")
}

// TestExec_EmptyCollectProjectsEmptyList pins that an aggregate over zero
// neighbours projects [] and never null. The difference is visible in the
// projected read model as `[]` vs `null`, which a consumer filtering the list
// cannot treat alike.
func TestExec_EmptyCollectProjectsEmptyList(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)

	results := parseExec(t,
		`MATCH (i:identity {key: $k})
		 OPTIONAL MATCH (i)<-[:providedTo]-(s:service)
		 RETURN i.key AS who, collect(s.key) AS keys, collect(DISTINCT s.key) AS distinctKeys`,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "alice")}},
		adjKV, coreKV)

	require.Len(t, results, 1)
	for _, col := range []string{"keys", "distinctKeys"} {
		v, ok := results[0].Values[col].([]any)
		require.True(t, ok, "%s must be a list, got %T", col, results[0].Values[col])
		require.NotNil(t, v, "%s must project [], never null", col)
		require.Empty(t, v)
	}
}

// TestExec_IncrementalFoldOverLargeBindingSet drives count(), collect(DISTINCT)
// and max() together over a 1600-row binding set, pinning that the per-row fold
// reaches the same values the materialize-then-reduce shape did: count sees
// every row, DISTINCT collapses to the distinct set, and the extreme reduces
// across the whole group.
func TestExec_IncrementalFoldOverLargeBindingSet(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV, _ := crossProductFixture(t, 40)

	results := parseExec(t,
		`MATCH (a:identity), (b:identity)
		 RETURN count(a) AS n, collect(DISTINCT a.key) AS keys, max(a.key) AS latest`,
		ruleengine.EventContext{}, adjKV, coreKV)

	require.Len(t, results, 1)
	require.Equal(t, int64(1600), results[0].Values["n"], "count folds every binding row")
	keys, ok := results[0].Values["keys"].([]any)
	require.True(t, ok)
	require.Len(t, keys, 40, "collect DISTINCT retains the distinct set, not the cross product")
	require.NotNil(t, results[0].Values["latest"])
}
