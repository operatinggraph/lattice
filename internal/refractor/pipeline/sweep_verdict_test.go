package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// declineAllAdapter is a listingAdapter whose guarded writes are all declined
// by the watermark — the shape a target takes when the sweep's ordering token
// has come to rest at or below every stored row's watermark.
type declineAllAdapter struct {
	listingAdapter
	deleteCalls int
}

func (a *declineAllAdapter) DeleteWithOutcome(_ context.Context, keys map[string]any, seq uint64) (adapter.DeleteOutcome, error) {
	a.deleteCalls++
	a.deletes = append(a.deletes, recordedWrite{keys: keys, seq: seq})
	return adapter.DeleteOutcome{DeclinedByWatermark: true}, nil
}

func (a *declineAllAdapter) Delete(_ context.Context, keys map[string]any, seq uint64) error {
	a.deleteCalls++
	a.deletes = append(a.deletes, recordedWrite{keys: keys, seq: seq})
	return nil
}

// TestSweepPass_CountsBlockedInsteadOfHealing is the sweep-level statement of
// the defect. The guard declines every repair, so the pass changes nothing —
// and a heal counted for a row that never moved is worse than no signal at
// all: a permanently unrepairable auth row reporting itself healed once a
// minute consumes the very signal that would expose it.
//
// Fails without the honest verdict: Reconciled climbs by the actor count and
// Blocked is 0.
func TestSweepPass_CountsBlockedInsteadOfHealing(t *testing.T) {
	adpt := &declineAllAdapter{listingAdapter: listingAdapter{
		recordingAdapter: recordingAdapter{
			stored:  map[string]any{"key": "cap.roles.identity.x"},
			present: true,
		},
		keys: []string{sweepBuildKey(sweepActorA), sweepBuildKey(sweepActorB)},
	}}
	p := newSweepPipeline(t, &adpt.listingAdapter, 10)
	p.adpt = adpt
	p.recordAppliedSeq(4242)

	sw := p.Sweeper()
	sw.pass(context.Background())

	st := sw.Status()
	require.Equal(t, 2, st.Blocked, "both actors held a divergence the guard refused to repair")
	require.Equal(t, 1, st.BlockedStreak)
	require.Contains(t, st.LastBlocked, "stored watermark")
	require.Zero(t, st.Reconciled, "nothing was healed, so nothing may be counted as healed")
	require.Zero(t, st.DivergentStreak)
	require.Zero(t, st.FailingActors, "a declined write is not an ERROR — there is nothing to retry")
	require.Equal(t, 2, adpt.deleteCalls, "the writes are still attempted; only the report changes")

	// A clean pass clears the streak, so the counter discriminates rather than
	// latching on.
	adpt.keys = nil
	sw.pass(context.Background())
	require.Zero(t, sw.Status().Blocked)
	require.Zero(t, sw.Status().BlockedStreak)
}

// TestSweepPass_AbandonsOnRuleSwap pins the per-caller disposition the
// supersession refusal takes in the sweep: abandon the whole pass, and charge
// NO actor a failure strike. Charging one would push it into backoffPasses and
// delay the genuine post-rebuild heal — punishing an actor for a reload it had
// nothing to do with.
func TestSweepPass_AbandonsOnRuleSwap(t *testing.T) {
	eng := full.New()
	crA, err := eng.Parse(raceSwapSpecA)
	require.NoError(t, err)
	crB, err := eng.Parse(raceSwapSpecB)
	require.NoError(t, err)

	adpt := &swapListingAdapter{listingAdapter: listingAdapter{
		recordingAdapter: recordingAdapter{
			stored:  map[string]any{"key": "cap.roles.identity.x"},
			present: true,
		},
		keys: []string{sweepBuildKey(sweepActorA), sweepBuildKey(sweepActorB)},
	}}
	p := newSweepPipeline(t, &adpt.listingAdapter, 10)
	p.adpt = adpt
	p.recordAppliedSeq(4242)
	p.SetMultiEnvelopeFn(func(row, keys, params map[string]any) ([]Envelope, error) {
		return nil, nil
	})
	p.UseFullEngine(eng, crA)

	// The MATCH reload lands on the pipeline's own read, mid-evaluation of the
	// first actor — the window the guard covers.
	adpt.swap = func() { p.UseFullEngineBranches(eng, crB, nil) }

	sw := p.Sweeper()
	sw.pass(context.Background())

	st := sw.Status()
	require.Empty(t, adpt.deletes, "no actor may be written under the retired rule")
	require.Empty(t, adpt.upserts)
	require.Zero(t, st.FailingActors, "a reload is nobody's fault; a strike would delay the real heal")
	require.Zero(t, st.FailedStreak)
	require.Zero(t, st.Reconciled)
}

// swapListingAdapter fires a rule swap on the pipeline's first row read-back,
// which happens inside multiEntryRetractions during reprojectActors.
type swapListingAdapter struct {
	listingAdapter
	swap  func()
	fired bool
}

func (a *swapListingAdapter) GetRow(ctx context.Context, keys map[string]any) (map[string]any, bool, error) {
	if !a.fired && a.swap != nil {
		a.fired = true
		a.swap()
	}
	return a.listingAdapter.GetRow(ctx, keys)
}

// TestSweep_BlockedVerdictPersistsAcrossPassesThatDoNotReExamineIt is the
// multi-pass proof that the blocked verdict is standing per-anchor state, not a
// per-pass tally.
//
// A pass examines at most `batch` anchors chosen by cursor round-robin, so on
// any corpus larger than one batch most passes do not re-examine a given
// anchor. A per-pass counter therefore reads zero on those passes: the issue
// vanishes from the heartbeat, its streak resets, and the escalation to error
// becomes structurally unreachable — publishing a clean verdict over a live
// permission set the graph no longer grants, for all but one pass per rotation.
//
// Fails without the fix: pass 2 reports Blocked 0 and BlockedStreak 0.
func TestSweep_BlockedVerdictPersistsAcrossPassesThatDoNotReExamineIt(t *testing.T) {
	adpt := &declineAllAdapter{listingAdapter: listingAdapter{
		recordingAdapter: recordingAdapter{
			stored:  map[string]any{"key": "cap.roles.identity.x"},
			present: true,
		},
		keys: []string{sweepBuildKey(sweepActorA)},
	}}
	// Batch 1: each pass can examine exactly one anchor, so the second pass
	// necessarily rotates away from the first's.
	p := newSweepPipeline(t, &adpt.listingAdapter, 1)
	p.adpt = adpt
	p.recordAppliedSeq(4242)

	sw := p.Sweeper()
	sw.pass(context.Background())
	require.Equal(t, 1, sw.Status().Blocked, "pass 1 finds the unrepairable row")
	require.Equal(t, 1, sw.Status().BlockedStreak)

	// Pass 2 rotates to a different anchor (the corpus grew), so it never
	// re-examines the blocked one. Its verdict must survive.
	adpt.keys = append(adpt.keys, sweepBuildKey(sweepActorB))
	sw.pass(context.Background())

	st := sw.Status()
	require.GreaterOrEqual(t, st.Blocked, 1,
		"a pass that did not re-examine the anchor must not clear its verdict")
	require.GreaterOrEqual(t, st.BlockedStreak, 2,
		"the streak must be able to reach the error threshold on a real corpus")
	require.NotEmpty(t, st.LastBlocked)
}

// TestSweep_AbandonedPassDoesNotClearAStandingVerdict pins the other half: an
// abandoned pass verified almost nothing, so it may neither clear a verdict it
// never re-derived nor stamp itself as a fresh, fault-free tick. Before this
// was fixed, one rule swap turned a lens holding an unrepairable row into a
// fully green report — and the rebuild the swap triggers then suppresses every
// following tick, so that green is what stays published.
func TestSweep_AbandonedPassDoesNotClearAStandingVerdict(t *testing.T) {
	adpt := &declineAllAdapter{listingAdapter: listingAdapter{
		recordingAdapter: recordingAdapter{
			stored:  map[string]any{"key": "cap.roles.identity.x"},
			present: true,
		},
		keys: []string{sweepBuildKey(sweepActorA)},
	}}
	p := newSweepPipeline(t, &adpt.listingAdapter, 10)
	p.adpt = adpt
	p.recordAppliedSeq(4242)

	sw := p.Sweeper()
	sw.pass(context.Background())
	require.Equal(t, 1, sw.Status().Blocked)
	before := sw.Status().LastPassAt

	// The next pass abandons on a rule swap before it can re-verify anything.
	sw.recordAbandoned(context.Background(), 0, "rule swapped mid-pass")

	st := sw.Status()
	require.Equal(t, 1, st.Blocked, "the standing verdict survives an abandoned pass")
	require.Equal(t, 1, st.BlockedStreak)
	require.NotEmpty(t, st.LastBlocked)
	require.Equal(t, before, st.LastPassAt,
		"an abandoned pass reached no verdict, so the liveness clock must keep aging")
	require.Equal(t, "rule swapped mid-pass", st.Suppression)
}
