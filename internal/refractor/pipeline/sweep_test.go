package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// sweepBuildKey / sweepAnchorFromKey mirror the capabilityRoles descriptor's
// key shape (cap.roles.<type>.<id>), the production auth-plane lens the sweep
// runs against. The pair must round-trip: the sweep compares a computed key
// set against a listed one, so a one-sided rendering would report every actor
// as divergent.
func sweepBuildKey(actorKey string) string {
	return "cap.roles." + strings.TrimPrefix(actorKey, "vtx.")
}

func sweepAnchorFromKey(targetKey string) (string, bool) {
	rest, ok := strings.CutPrefix(targetKey, "cap.roles.")
	if !ok {
		return "", false
	}
	actorKey := "vtx." + rest
	vtxType, _, parsed := substrate.ParseVertexKey(actorKey)
	if !parsed || vtxType != "identity" {
		return "", false
	}
	return actorKey, true
}

// listingAdapter is a recordingAdapter that can also enumerate the target's
// live keys, so a test can pose both prefilter directions.
type listingAdapter struct {
	recordingAdapter
	keys    []string
	listErr error
}

func (a *listingAdapter) ListKeys(context.Context) ([]map[string]any, error) {
	if a.listErr != nil {
		return nil, a.listErr
	}
	out := make([]map[string]any, 0, len(a.keys))
	for _, k := range a.keys {
		out = append(out, map[string]any{"key": k})
	}
	return out, nil
}

const (
	sweepActorA = "vtx.identity.Tswp1AaaaaaaaaaaaaaZ"
	sweepActorB = "vtx.identity.Tswp2BbbbbbbbbbbbbbZ"
	sweepActorC = "vtx.identity.Tswp3CcccccccccccccZ"
)

// newSweepPipeline builds an actor-aggregate pipeline with a sweep plan
// installed and a real (empty) Core KV, so the missing-actor branch resolves
// against a genuine ErrKeyNotFound.
func newSweepPipeline(t *testing.T, adpt *listingAdapter, batch int) *Pipeline {
	t.Helper()
	coreKV, adjKV := newDeleteKeyKV(t)
	p := &Pipeline{
		ruleID:          "sweep-rule",
		coreKV:          coreKV,
		adjKV:           adjKV,
		engineKind:      ruleengine.EngineFull,
		fullEngine:      &full.Engine{},
		fullCR:          &full.CompiledRule{},
		actorEnumerator: NewActorEnumerator(adjKV, coreKV, "identity"),
		adpt:            adpt,
	}
	p.SetEnvelopeFn(func(row, keys, params map[string]any) (map[string]any, map[string]any, error) {
		return row, keys, nil
	})
	p.SetActorDeleteKey(sweepBuildKey)
	p.SetSweepPlan(SweepPlan{
		AnchorType:    "identity",
		BuildKey:      sweepBuildKey,
		AnchorFromKey: sweepAnchorFromKey,
		Interval:      time.Hour, // ticks are driven explicitly by the tests
		Batch:         batch,
	})
	return p
}

// writeAnchor seeds a Core KV identity vertex; deleted writes the tombstoned
// form (a live NATS-KV key carrying isDeleted).
func writeAnchor(t *testing.T, p *Pipeline, actorKey string, deleted bool) {
	t.Helper()
	body := map[string]any{"key": actorKey, "class": "identity", "data": map[string]any{}}
	if deleted {
		body["isDeleted"] = true
	}
	data, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = p.coreKV.Put(context.Background(), actorKey, data)
	require.NoError(t, err)
}

func TestRunSweep_NoPlanInstalled_ReturnsImmediately(t *testing.T) {
	// A plain, personal, convergence, or operation-aggregate lens never
	// receives a plan, which is what excludes it structurally. Starting the
	// goroutine unconditionally beside Run must therefore be free.
	p := newDeleteKeyPipeline(t, nil)
	require.Nil(t, p.Sweeper())
	done := make(chan struct{})
	go func() { p.RunSweep(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunSweep did not return for a pipeline with no sweep plan")
	}
}

func TestSweepCandidates_AnchorWithNoTargetKeyIsDivergent(t *testing.T) {
	adpt := &listingAdapter{keys: []string{sweepBuildKey(sweepActorA)}}
	p := newSweepPipeline(t, adpt, 10)
	writeAnchor(t, p, sweepActorA, false)
	writeAnchor(t, p, sweepActorB, false)

	anchors, targets, err := p.Sweeper().survey(context.Background())
	require.NoError(t, err)
	require.ElementsMatch(t, []string{sweepActorA, sweepActorB}, anchors)

	got := p.Sweeper().candidates(context.Background(), anchors, targets).actors
	// B is the definite divergence (the observed first-projection loss) and
	// must be picked first, ahead of the round-robin walk.
	require.Equal(t, sweepActorB, got[0])
}

func TestSweepCandidates_TombstonedAnchorIsNotADivergence(t *testing.T) {
	// A tombstoned anchor legitimately has no target key. Counting it as a
	// definite divergence would refill the batch every tick forever and starve
	// the deep verify.
	adpt := &listingAdapter{}
	p := newSweepPipeline(t, adpt, 1)
	writeAnchor(t, p, sweepActorA, true)  // tombstoned, no row — correct
	writeAnchor(t, p, sweepActorB, false) // live, no row — the real divergence

	anchors, targets, err := p.Sweeper().survey(context.Background())
	require.NoError(t, err)
	got := p.Sweeper().candidates(context.Background(), anchors, targets).actors
	require.Equal(t, []string{sweepActorB}, got,
		"the batch must go to the live unprojected anchor, not the tombstoned one")
}

func TestSweepCandidates_OrphanTargetKeyIsDivergent(t *testing.T) {
	// The over-grant direction: a row survives an anchor that is gone from
	// Core KV entirely. Nothing will ever re-drive it — its last event was the
	// one that was lost.
	orphan := sweepBuildKey(sweepActorC)
	adpt := &listingAdapter{keys: []string{orphan}}
	p := newSweepPipeline(t, adpt, 10)

	anchors, targets, err := p.Sweeper().survey(context.Background())
	require.NoError(t, err)
	require.Empty(t, anchors)

	got := p.Sweeper().candidates(context.Background(), anchors, targets).actors
	require.Equal(t, []string{sweepActorC}, got)
}

func TestSweepCandidates_ForeignKeysInASharedBucketAreNotClaimed(t *testing.T) {
	// capability-kv is shared by every auth-plane lens. Claiming a sibling's
	// key would have this lens retract rows it does not own.
	adpt := &listingAdapter{keys: []string{
		"cap.identity.Tswp3CcccccccccccccZ",         // the primary lens's key
		"cap.role-by-operation.lattice.role.assign", // the operation-aggregate index
		"cap.roles.service.Tswp3CcccccccccccccZ",    // right prefix, wrong anchor type
		"cap.roles.identity.Tswp3CcccccccccccccZ.x", // right prefix, not a vertex key
	}}
	p := newSweepPipeline(t, adpt, 10)

	anchors, targets, err := p.Sweeper().survey(context.Background())
	require.NoError(t, err)
	gotEmpty := p.Sweeper().candidates(context.Background(), anchors, targets).actors
	require.Empty(t, gotEmpty)
}

func TestSweepCandidates_CursorWalksAndResumes(t *testing.T) {
	// The deep verify is bounded and round-robin: each tick continues where
	// the last left off, so a large cell re-verifies fully over many ticks
	// instead of re-checking the same head every minute.
	all := []string{sweepActorA, sweepActorB, sweepActorC}
	keys := make([]string, 0, len(all))
	for _, a := range all {
		keys = append(keys, sweepBuildKey(a))
	}
	adpt := &listingAdapter{keys: keys}
	p := newSweepPipeline(t, adpt, 1)
	for _, a := range all {
		writeAnchor(t, p, a, false)
	}
	sw := p.Sweeper()

	anchors, targets, err := sw.survey(context.Background())
	require.NoError(t, err)
	require.Len(t, anchors, 3)

	seen := make([]string, 0, 3)
	for range all {
		got := sw.candidates(context.Background(), anchors, targets).actors
		require.Len(t, got, 1)
		seen = append(seen, got[0])
	}
	require.ElementsMatch(t, all, seen, "three bounded ticks must cover every anchor exactly once")

	// A fourth tick wraps rather than stalling at the end of the list.
	got := sw.candidates(context.Background(), anchors, targets).actors
	require.Equal(t, anchors[0], got[0])
}

func TestSweepCandidates_DeepVerifyKeepsAReservedSliceOfEveryBatch(t *testing.T) {
	// A prefilter candidate that recurs indefinitely — a heal that keeps
	// erroring, a soft-delete key still listed after retraction — must not be
	// able to refill the whole batch every tick. If it could, the round-robin
	// walk would stop advancing and the only detector for a stale-but-present
	// row would be silently disabled.
	adpt := &listingAdapter{}
	p := newSweepPipeline(t, adpt, 10)
	// Twenty live anchors, none projected: every one is a definite divergence,
	// which is exactly the case that could crowd the walk out.
	// The Contract #1 NanoID alphabet excludes I, l, O and 0, so the varying
	// segment is drawn from a fixed safe run rather than generated arithmetically.
	const idChars = "abcdefghijkmnpqrstu"
	anchors := make([]string, 0, len(idChars))
	for i := range idChars {
		actor := "vtx.identity.Tstv" + string(idChars[i]) + "aaaaaaaaaaaaaaa"
		writeAnchor(t, p, actor, false)
		anchors = append(anchors, actor)
	}
	sw := p.Sweeper()
	surveyed, targets, err := sw.survey(context.Background())
	require.NoError(t, err)
	require.Len(t, surveyed, len(anchors))

	got := sw.candidates(context.Background(), surveyed, targets).actors
	require.LessOrEqual(t, len(got), 10)
	require.NotEmpty(t, sw.Status().Cursor,
		"the deep verify must still reach its reserved slots and advance the cursor")

	first := sw.Status().Cursor
	sw.candidates(context.Background(), surveyed, targets)
	require.NotEqual(t, first, sw.Status().Cursor,
		"the cursor must keep advancing tick over tick under a saturated prefilter")
}

func TestSweepCandidates_CursorSurvivesAnchorRemoval(t *testing.T) {
	// The cursor is a key, not an index: the anchor it names can disappear
	// between ticks, and the walk must resume at the next key rather than
	// restart or stall.
	adpt := &listingAdapter{}
	p := newSweepPipeline(t, adpt, 1)
	sw := p.Sweeper()
	sw.mu.Lock()
	sw.status.Cursor = sweepActorB
	sw.mu.Unlock()

	anchors := []string{sweepActorA, sweepActorC} // B has been deleted
	got := sw.candidates(context.Background(), anchors, map[string]struct{}{}).actors
	require.Equal(t, []string{sweepActorC}, got)
}

func TestSweepPass_SuppressedWhileRebuilding(t *testing.T) {
	// A rebuild is a superset of the sweep (truncate + full rescan); running
	// both at once would have reconciliation writes race a replay.
	adpt := &listingAdapter{}
	p := newSweepPipeline(t, adpt, 10)
	writeAnchor(t, p, sweepActorA, false)
	p.rebuildInFlight.Store(true)

	p.Sweeper().pass(context.Background())
	require.Empty(t, adpt.upserts)
	require.Empty(t, adpt.deletes)
	require.Zero(t, p.Sweeper().Status().Reconciled)
}

func TestSweepPass_ConvergedWorldWritesNothing(t *testing.T) {
	// The zero-write steady state: with no anchors and no target keys there is
	// nothing to reconcile, and a sweep must cost reads only.
	adpt := &listingAdapter{}
	p := newSweepPipeline(t, adpt, 10)

	p.Sweeper().pass(context.Background())
	require.Empty(t, adpt.upserts)
	require.Empty(t, adpt.deletes)

	st := p.Sweeper().Status()
	require.Zero(t, st.Reconciled)
	require.Zero(t, st.DivergentStreak)
}

func TestSweepPass_HealsAnOrphanRowAndCountsIt(t *testing.T) {
	// End of the over-grant direction: the row is present, its anchor is gone,
	// so reconciliation retracts it and the heal is counted loudly.
	orphan := sweepBuildKey(sweepActorC)
	adpt := &listingAdapter{keys: []string{orphan}}
	adpt.present = true
	adpt.stored = map[string]any{"key": orphan}
	p := newSweepPipeline(t, adpt, 10)
	p.recordAppliedSeq(910)

	p.Sweeper().pass(context.Background())

	require.Len(t, adpt.deletes, 1)
	require.Equal(t, uint64(910), adpt.deletes[0].seq,
		"a reconciliation write carries the captured last-applied sequence, never MaxInt64")
	st := p.Sweeper().Status()
	require.Equal(t, uint64(1), st.Reconciled)
	require.Equal(t, 1, st.DivergentStreak)
}

func TestSweepRecord_StreakEscalatesAndClears(t *testing.T) {
	// The escalation input for CapabilityCoverageDivergence: one divergent
	// pass is a repaired incident, two in a row means events are still being
	// lost, and a clean pass clears the alert.
	p := newSweepPipeline(t, &listingAdapter{}, 10)
	sw := p.Sweeper()
	ctx := context.Background()

	sw.record(ctx, 2, nil)
	require.Equal(t, 1, sw.Status().DivergentStreak)
	require.Equal(t, uint64(2), sw.Status().Reconciled)

	sw.record(ctx, 1, nil)
	require.Equal(t, 2, sw.Status().DivergentStreak)
	require.Equal(t, uint64(3), sw.Status().Reconciled)

	sw.record(ctx, 0, nil)
	require.Zero(t, sw.Status().DivergentStreak)
	require.Equal(t, uint64(3), sw.Status().Reconciled,
		"the cumulative heal count is not reset by a clean pass")
}

func TestSweepSurvey_SkipsAspectKeysUnderTheAnchorPrefix(t *testing.T) {
	// The anchor listing is by key prefix, which also matches every aspect of
	// every anchor. Only the three-segment vertex root is an anchor.
	adpt := &listingAdapter{}
	p := newSweepPipeline(t, adpt, 10)
	writeAnchor(t, p, sweepActorA, false)
	_, err := p.coreKV.Put(context.Background(), sweepActorA+".demographics", []byte(`{"key":"x"}`))
	require.NoError(t, err)

	anchors, _, err := p.Sweeper().survey(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{sweepActorA}, anchors)
}

// unwritableTarget is the adapter posture the repair-failure signal exists for:
// the row is divergent, the sweep picks it up, and the write fails every time.
var errUnwritableTarget = errors.New("adapter: target write refused")

func newUnwritableOrphanPipeline(t *testing.T) *Pipeline {
	t.Helper()
	orphan := sweepBuildKey(sweepActorC)
	adpt := &listingAdapter{keys: []string{orphan}}
	adpt.present = true
	adpt.stored = map[string]any{"key": orphan}
	adpt.writeErr = errUnwritableTarget
	p := newSweepPipeline(t, adpt, 10)
	p.recordAppliedSeq(910)
	return p
}

func TestSweepPass_AFailedRepairDoesNotReadAsAConvergedPass(t *testing.T) {
	// A repair whose write errors heals nothing, so the heal count is 0 —
	// indistinguishable, on that count alone, from a pass where everything was
	// already right. The heal count therefore cannot be the only verdict, or a
	// row that is still wrong reads as converged.
	p := newUnwritableOrphanPipeline(t)

	p.Sweeper().pass(context.Background())

	st := p.Sweeper().Status()
	require.Zero(t, st.Reconciled, "a failed write heals nothing")
	require.Zero(t, st.DivergentStreak, "the heal count is genuinely zero — that is the blind spot")
	require.Equal(t, 1, st.FailingActors, "the actor whose repair failed is carried as unrepaired")
	require.Equal(t, 1, st.FailedStreak)
	require.Contains(t, st.LastFailure, errUnwritableTarget.Error(),
		"the health issue must be able to name a cause, not just a count")
}

func TestSweepPass_RepairFailureEscalatesAcrossPassesAndClearsOnSuccess(t *testing.T) {
	// The escalation input for CapabilityRepairFailing: consecutive failing
	// passes accumulate, and a subsequent successful reprojection retires both
	// the actor and the streak.
	p := newUnwritableOrphanPipeline(t)
	sw := p.Sweeper()
	ctx := context.Background()

	sw.pass(ctx)
	require.Equal(t, 1, sw.Status().FailedStreak)

	// The second consecutive failure is what crosses the error threshold. The
	// actor's first failure carries no backoff, so this pass re-attempts it.
	sw.pass(ctx)
	require.Equal(t, 2, sw.Status().FailedStreak)
	require.Equal(t, 1, sw.Status().FailingActors)

	// The target becomes writable again; the next attempted pass retracts the
	// orphan and the repair verdict clears completely.
	adpt := p.currentAdapter().(*listingAdapter)
	adpt.writeErr = nil
	for i := 0; i < 4 && sw.Status().FailingActors > 0; i++ {
		sw.pass(ctx)
	}
	st := sw.Status()
	require.Zero(t, st.FailingActors, "a successful reprojection retires the actor's failure")
	require.Zero(t, st.FailedStreak)
	require.Empty(t, st.LastFailure)
	require.Equal(t, uint64(1), st.Reconciled, "the heal is counted once the write lands")
}

func TestSweepPass_BackoffSuppressesTheRetryButNotTheSignal(t *testing.T) {
	// Backoff exists so a permanently unwritable row stops consuming a batch
	// slot every tick. It must never make the lens look healthier: an actor the
	// sweep declined to retry is still an actor whose row is wrong.
	p := newUnwritableOrphanPipeline(t)
	sw := p.Sweeper()
	adpt := p.currentAdapter().(*listingAdapter)
	ctx := context.Background()

	sw.pass(ctx) // failure 1 — no backoff, retried next pass
	sw.pass(ctx) // failure 2 — now sits out backoffPasses(2) == 1 pass
	attemptsBefore := len(adpt.deletes)

	sw.pass(ctx)
	require.Equal(t, attemptsBefore, len(adpt.deletes),
		"the backed-off actor must not be re-attempted this pass")
	st := sw.Status()
	require.Equal(t, 1, st.FailingActors,
		"a skipped retry still counts as unrepaired — suppressing the work must not suppress the signal")
	require.Equal(t, 3, st.FailedStreak)
}

func TestSweepBackoffPasses_ImmediateFirstRetryThenDoublingToACeiling(t *testing.T) {
	// A transient write error deserves the next tick; a persistent one must
	// back off, but never so far that a fixed target waits hours.
	require.Equal(t, uint64(0), backoffPasses(1))
	require.Equal(t, uint64(1), backoffPasses(2))
	require.Equal(t, uint64(2), backoffPasses(3))
	require.Equal(t, uint64(4), backoffPasses(4))
	require.Equal(t, uint64(16), backoffPasses(6))
	require.Equal(t, uint64(16), backoffPasses(50), "the ceiling holds")
}

func TestSweepPass_ASurveyFailureIsNotAConvergedPass(t *testing.T) {
	// A pass that could not read both sides of the comparison verified
	// nothing. Returning silently would make an unreadable target
	// indistinguishable from a clean bucket.
	adpt := &listingAdapter{listErr: errUnwritableTarget}
	p := newSweepPipeline(t, adpt, 10)
	writeAnchor(t, p, sweepActorA, false)

	p.Sweeper().pass(context.Background())

	st := p.Sweeper().Status()
	require.Equal(t, 1, st.FailedStreak)
	require.Zero(t, st.FailingActors, "a pass-level fault names no actor")
	require.Contains(t, st.LastFailure, errUnwritableTarget.Error())
}

func TestSweepRecord_TruncatesAnOversizeFailureText(t *testing.T) {
	// The failure text rides into a Health-KV document, which has a NATS
	// payload limit.
	p := newSweepPipeline(t, &listingAdapter{}, 10)
	sw := p.Sweeper()

	sw.record(context.Background(), 0, errors.New(strings.Repeat("x", maxFailureText*3)))

	require.LessOrEqual(t, len(sw.Status().LastFailure), maxFailureText+len("…"))
}

func TestSweepPass_AFailingActorThatLeavesTheGraphIsReaped(t *testing.T) {
	// A failing actor is re-attempted only if selection reaches it, and
	// selection draws solely from the two listings. An actor that leaves both
	// would otherwise hold CapabilityRepairFailing open for the life of the
	// process — an ordinary sequence, since an orphan retraction that errors
	// can still land through the CDC path before the retry.
	p := newUnwritableOrphanPipeline(t)
	sw := p.Sweeper()
	ctx := context.Background()

	sw.pass(ctx)
	require.Equal(t, 1, sw.Status().FailingActors)

	// The retraction lands by another path: the target key stops being listed,
	// and the anchor was never in Core KV to begin with.
	adpt := p.currentAdapter().(*listingAdapter)
	adpt.keys = nil

	sw.pass(ctx)
	st := sw.Status()
	require.Zero(t, st.FailingActors, "an actor that left the graph must not pin the alert open")
	require.Zero(t, st.FailedStreak)
	require.Empty(t, st.LastFailure)
}

func TestSweepCandidates_ABackedOffActorYieldsItsSlot(t *testing.T) {
	// Backing off is only worth doing if it frees capacity: a skipped actor
	// must hand its batch slot to the next divergent one, not spend it.
	adpt := &listingAdapter{}
	p := newSweepPipeline(t, adpt, 1)
	writeAnchor(t, p, sweepActorA, false)
	writeAnchor(t, p, sweepActorB, false)
	sw := p.Sweeper()
	ctx := context.Background()

	anchors, targets, err := sw.survey(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{sweepActorA, sweepActorB}, anchors)

	// Both anchors are divergent (neither has a target key) and the batch
	// holds one, so the first sorted anchor takes it.
	gotFirst := sw.candidates(ctx, anchors, targets).actors
	require.Equal(t, []string{sweepActorA}, gotFirst)

	sw.mu.Lock()
	sw.passNo = 7
	// Rewind the coverage walk so it starts at A again. Without this the
	// rotation would reach B on its own and the assertion below would pass
	// whether or not backing off actually yields the slot.
	sw.coverage.cursor = ""
	sw.mu.Unlock()
	sw.noteActorFailure(sweepActorA, errUnwritableTarget, 7)
	sw.noteActorFailure(sweepActorA, errUnwritableTarget, 7)

	gotAfterBackoff := sw.candidates(ctx, anchors, targets).actors
	require.Equal(t, []string{sweepActorB}, gotAfterBackoff,
		"the backed-off actor's slot must go to the next divergent actor")
	require.Equal(t, 1, len(sw.failing), "yielding the slot does not retire the failure")
}

// writeProjectableAnchor seeds a live identity anchor whose body carries the
// commit provenance the universal Core KV envelope records (Contract #1 §1.3),
// so Reproject can derive a deterministic projectedAt and actually reach a
// verdict on it instead of faulting.
func writeProjectableAnchor(t *testing.T, p *Pipeline, actorKey string) {
	t.Helper()
	body := map[string]any{
		"key":            actorKey,
		"class":          "identity",
		"data":           map[string]any{},
		"createdAt":      "2026-07-25T00:00:00Z",
		"lastModifiedAt": "2026-07-25T00:00:00Z",
	}
	data, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = p.coreKV.Put(context.Background(), actorKey, data)
	require.NoError(t, err)
}

// rowlessAnchors seeds n live identity anchors with no target row and returns
// them in sorted order. The Contract #1 NanoID alphabet excludes I, l, O and 0,
// so the varying segment is drawn from a fixed safe run.
func rowlessAnchors(t *testing.T, p *Pipeline, n int) []string {
	t.Helper()
	const idChars = "abcdefghijkmnpqrstu"
	require.LessOrEqual(t, n, len(idChars))
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		actor := "vtx.identity.Tstv" + string(idChars[i]) + "aaaaaaaaaaaaaaa"
		writeProjectableAnchor(t, p, actor)
		out = append(out, actor)
	}
	return out
}


// hintMissesForTest reads a prefilter hint's standing record under the
// sweeper's own lock.
func (s *Sweeper) hintMissesForTest(which string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if which == "orphan" {
		return s.orphan.misses
	}
	return s.coverage.misses
}

// floorHint drives a hint to its floor the way a pass would, and pins passNo
// off the re-test cadence so the floor is actually in force.
func floorHint(t *testing.T, sw *Sweeper, which string, h *hintState) {
	t.Helper()
	sw.mu.Lock()
	sw.passNo = 1
	sw.mu.Unlock()
	for i := 0; i < hintMissesBeforeFloor; i++ {
		sw.noteHintOutcome(which, h, 4, 0)
	}
	require.Equal(t, hintMissesBeforeFloor, sw.hintMissesForTest(which))
}

// seedOrphanKeys points the adapter at n target keys whose anchors do not
// exist, so every one of them is an orphan, and returns their actor keys sorted.
func seedOrphanKeys(t *testing.T, adpt *listingAdapter, n int) []string {
	t.Helper()
	const idChars = "abcdefghijkmnpqrstu"
	require.LessOrEqual(t, n, len(idChars))
	actors := make([]string, 0, n)
	for i := 0; i < n; i++ {
		actor := "vtx.identity.Tsto" + string(idChars[i]) + "aaaaaaaaaaaaaaa"
		adpt.keys = append(adpt.keys, sweepBuildKey(actor))
		actors = append(actors, actor)
	}
	return actors
}

func TestSweepCandidates_TheOrphanHintKeepsItsSlotsUnderASaturatedCoverageWalk(t *testing.T) {
	// The orphan hint is the only detector for a row whose anchor is gone: the
	// deep verify walks anchors, and an orphan's anchor is by definition not
	// among them. So a coverage walk that saturates the prefilter — the steady
	// state of any lens whose match filters — must not be able to starve it.
	//
	// The covered anchor sorts after the coverage walk's budget cut-off, so this
	// also proves the expected-key map is built over EVERY anchor: a map
	// truncated by that budget would hand this healthy actor to the orphan hint
	// to re-project.
	const orphanActor = "vtx.identity.Tstwzaaaaaaaaaaaaaaa"
	coveredActor := "vtx.identity.Tstvzaaaaaaaaaaaaaaa"
	adpt := &listingAdapter{keys: []string{
		sweepBuildKey(coveredActor),
		sweepBuildKey(orphanActor),
	}}
	p := newSweepPipeline(t, adpt, 5)
	rowless := rowlessAnchors(t, p, 10)
	writeProjectableAnchor(t, p, coveredActor)
	sw := p.Sweeper()

	anchors, targets, err := sw.survey(context.Background())
	require.NoError(t, err)
	require.Len(t, anchors, len(rowless)+1)
	require.Equal(t, coveredActor, anchors[len(anchors)-1],
		"the covered anchor must sort last, beyond the coverage walk's budget")

	sel := sw.candidates(context.Background(), anchors, targets)
	require.Contains(t, sel.actors, orphanActor,
		"a saturated coverage walk must not starve the only orphan detector")
	require.Contains(t, sel.fromOrphan, orphanActor)
	require.NotContains(t, sel.actors, coveredActor,
		"an anchor that has a row is not an orphan, however late it sorts")
}

func TestSweepCandidates_TheCoverageWalkRotatesInsteadOfRepickingTheSameHead(t *testing.T) {
	// Without a cursor of its own the coverage walk re-examines the same
	// sorted-first anchors every tick forever, so on a filtering lens the rest of
	// the anchor space is never reached at all.
	adpt := &listingAdapter{}
	p := newSweepPipeline(t, adpt, 2)
	rowless := rowlessAnchors(t, p, 5)
	sw := p.Sweeper()

	anchors, targets, err := sw.survey(context.Background())
	require.NoError(t, err)
	require.Equal(t, rowless, anchors)

	picked := make([]string, 0, len(rowless))
	for range rowless {
		sel := sw.candidates(context.Background(), anchors, targets)
		require.Len(t, sel.fromCoverage, 1,
			"a batch of two leaves the coverage walk one slot after the deep reserve")
		for actor := range sel.fromCoverage {
			picked = append(picked, actor)
		}
	}
	require.ElementsMatch(t, rowless, picked,
		"five bounded ticks must reach every row-less anchor exactly once")
}

func TestSweepCandidates_TheOrphanHintRotatesSoAnAlreadyRetractedKeyCannotHoldASlot(t *testing.T) {
	// An auth-plane target is guarded, so retracting a row writes a soft
	// tombstone that stays a live NATS-KV key and keeps appearing in the target
	// listing. A retracted orphan therefore never leaves the hint's set, and a
	// hint that always walked its sorted head would re-verify the same
	// already-retracted keys every tick while a genuinely stale row further down
	// the order was never reached at all.
	adpt := &listingAdapter{}
	p := newSweepPipeline(t, adpt, 1)
	orphans := seedOrphanKeys(t, adpt, 5)
	sw := p.Sweeper()

	anchors, targets, err := sw.survey(context.Background())
	require.NoError(t, err)
	require.Empty(t, anchors, "no anchor exists, so every listed key is an orphan")

	picked := make([]string, 0, len(orphans))
	for range orphans {
		sel := sw.candidates(context.Background(), anchors, targets)
		require.Len(t, sel.fromOrphan, 1)
		for actor := range sel.fromOrphan {
			picked = append(picked, actor)
		}
	}
	require.ElementsMatch(t, orphans, picked,
		"five bounded ticks must reach every orphan exactly once")
}

func TestSweepCandidates_AnUnproductiveCoverageWalkYieldsTheBatchToTheDeepVerify(t *testing.T) {
	// The coverage walk's premise — an anchor with no row is divergent — is a
	// hypothesis, not a property of the lens. Once passes have tested it and
	// found every candidate already correct, spending most of the batch
	// re-projecting row-less anchors buys nothing, while the deep verify (the only
	// detector for a present-but-stale row) runs at a fifth of its budget.
	adpt := &listingAdapter{}
	p := newSweepPipeline(t, adpt, 10)
	rowless := rowlessAnchors(t, p, 19)
	sw := p.Sweeper()

	anchors, targets, err := sw.survey(context.Background())
	require.NoError(t, err)
	require.Len(t, anchors, len(rowless))

	sel := sw.candidates(context.Background(), anchors, targets)
	require.Len(t, sel.actors, 10)
	require.Len(t, sel.fromCoverage, 8,
		"until the premise is tested the walk holds the whole prefilter")

	floorHint(t, sw, "coverage", &sw.coverage)

	sel = sw.candidates(context.Background(), anchors, targets)
	require.Len(t, sel.actors, 10, "the batch is still spent in full")
	require.Len(t, sel.fromCoverage, 2,
		"the walk keeps only its reserved floor once its premise has failed")

	// One healed candidate clears the record outright: a lens that covers its
	// anchors and then loses a batch of rows is back to full share on the pass
	// after the one that discovers the loss.
	sw.noteHintOutcome("coverage", &sw.coverage, 2, 1)
	sel = sw.candidates(context.Background(), anchors, targets)
	require.Len(t, sel.fromCoverage, 8)
}

func TestSweepCandidates_AnUnproductiveOrphanHintYieldsItsSlotsToo(t *testing.T) {
	// A standing set of already-retracted tombstone orphans re-projects to
	// nothing forever. Left unchecked it would monopolise the prefilter exactly
	// as the coverage walk could — the same defect, one direction over.
	adpt := &listingAdapter{}
	p := newSweepPipeline(t, adpt, 10)
	orphans := seedOrphanKeys(t, adpt, 19)
	sw := p.Sweeper()

	anchors, targets, err := sw.survey(context.Background())
	require.NoError(t, err)
	require.Empty(t, anchors)

	sel := sw.candidates(context.Background(), anchors, targets)
	require.Len(t, sel.fromOrphan, 10,
		"with no anchors to walk the deep verify reserves nothing, so the orphan "+
			"hint may spend the whole batch")
	require.Len(t, orphans, 19)

	floorHint(t, sw, "orphan", &sw.orphan)

	sel = sw.candidates(context.Background(), anchors, targets)
	require.Len(t, sel.fromOrphan, 2,
		"once its retractions stop writing, the hint keeps only its floor")
}

func TestSweepHintOutcome_AnErroringOrEmptyPassIsNotEvidence(t *testing.T) {
	// An erroring lens must not be mistaken for one whose premise is false, and a
	// pass that selected nothing through a hint says nothing either way. A single
	// unproductive pass is also not enough: both key listings are bounded feeds
	// that can come back short, and a row landing mid-pass reads as missing and
	// then as correct.
	sw := newSweepPipeline(t, &listingAdapter{}, 10).Sweeper()

	sw.noteHintOutcome("coverage", &sw.coverage, 0, 0)
	require.Zero(t, sw.hintMissesForTest("coverage"))

	sw.noteHintOutcome("coverage", &sw.coverage, 3, 0)
	require.Equal(t, 1, sw.hintMissesForTest("coverage"))
	require.False(t, sw.coverage.floored(1),
		"one unproductive pass is a transient, not a verdict")

	sw.noteHintOutcome("coverage", &sw.coverage, 3, 0)
	require.True(t, sw.coverage.floored(1))

	sw.noteHintOutcome("coverage", &sw.coverage, 0, 0)
	require.True(t, sw.coverage.floored(1),
		"no evidence leaves the standing record alone; it does not clear it")

	require.False(t, sw.coverage.floored(hintRetestPasses),
		"the full share comes back periodically, so a verdict formed from a "+
			"transient cannot hold for the life of the process")

	sw.noteHintOutcome("coverage", &sw.coverage, 2, 1)
	require.Zero(t, sw.hintMissesForTest("coverage"))
}

func TestSweepCandidates_EveryAnchorHasItsRowSoTheDeepVerifyTakesTheWholeBatch(t *testing.T) {
	// The invariant this must not break. Every anchor carrying a row is the
	// converged shape of a lens that projects one per anchor, and there the
	// selection is exactly what it always was: the coverage walk selects nothing,
	// so it gathers no evidence and never floors; the orphan hint reserves
	// nothing because there is nothing to retract; the deep verify gets the whole
	// batch.
	p := newSweepPipeline(t, &listingAdapter{}, 10)
	anchorList := rowlessAnchors(t, p, 19)
	adpt, ok := p.adpt.(*listingAdapter)
	require.True(t, ok)
	for _, a := range anchorList {
		adpt.keys = append(adpt.keys, sweepBuildKey(a))
	}
	sw := p.Sweeper()

	anchors, targets, err := sw.survey(context.Background())
	require.NoError(t, err)

	sel := sw.candidates(context.Background(), anchors, targets)
	require.Empty(t, sel.fromCoverage, "every anchor has its row; nothing is missing")
	require.Empty(t, sel.fromOrphan, "and no row outlives its anchor")
	require.Equal(t, anchorList[:10], sel.actors,
		"the deep verify takes the whole batch, in cursor order")
}

func TestSweepPass_AFilteringLensLearnsItsCoverageWalkIsUnproductive(t *testing.T) {
	// End to end through real passes: every row-less anchor re-projects to
	// nothing, so the passes write nothing, and the sweep records that this
	// lens's absent rows are correct rather than divergent. This is the only
	// coverage of the wiring from Reproject's verdict back into the hint.
	adpt := &listingAdapter{}
	p := newSweepPipeline(t, adpt, 10)
	// A filtering lens IS a rule whose match filters: these anchors hold no
	// role, so the rule binds nothing and each one correctly projects no row.
	eng := full.New()
	cr, err := eng.Parse(
		"MATCH (i:identity {key: $actorKey})-[:holdsRole]->(r:role) " +
			"RETURN i.key AS actorKey, collect(r.key) AS roles")
	require.NoError(t, err)
	fullCR, isFull := cr.(*full.CompiledRule)
	require.True(t, isFull)
	fullCR.KeyColumns = []string{"actorKey"}
	require.NoError(t, fullCR.ValidateKeyColumns())
	p.fullEngine, p.fullCR = eng, fullCR

	rowlessAnchors(t, p, 19)
	p.recordAppliedSeq(910)
	sw := p.Sweeper()

	for i := 0; i < hintMissesBeforeFloor; i++ {
		sw.pass(context.Background())
	}

	require.Empty(t, adpt.upserts)
	require.Empty(t, adpt.deletes)
	require.Zero(t, sw.Status().Reconciled)
	require.Zero(t, sw.Status().FailingActors,
		"the reprojections must have SUCCEEDED and written nothing — an error "+
			"path would leave no evidence and pass this test vacuously")
	require.Equal(t, hintMissesBeforeFloor, sw.hintMissesForTest("coverage"),
		"a pass whose every coverage candidate was already correct is the evidence")
}
