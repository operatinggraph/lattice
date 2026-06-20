//go:build leaseshortwindow

package leaseconvergence_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/bridge"
)

// The async-external-reply convergence capstone: an ASYNC background-check
// (FakeAsyncCheck, registered ONLY in this test harness — production stays
// synchronous, cmd/bridge is unchanged) driven through the REAL Weaver + lens +
// bridge, proving the dispatch-suppression machinery makes async safe — Weaver
// does not re-dispatch a still-pending (or repeatedly-failing) external call.
//
// The harness shrinks the relevant horizons so the slow paths actually tick
// within the test (mirroring how the leaseshortwindow tag shrinks the freshness
// window): a short Weaver MarkLease + SweepInterval so a mark's lease expires and
// the reconciler sweep ticks (the only thing that exercises skip site 2 — the
// load-bearing sweep re-dispatch path), and short bridge PollInterval/CallDeadline
// so the poll chain advances and the give-up timeout fires in bounded wall-clock.

// bgcheckRetryCap mirrors the lease-signing package's maxBgcheckRetries default
// (the §E retry budget, Andrew-ratified at 3, baked into the lens's
// maxretries_bgcheck column). It is duplicated here as an unexported test constant
// rather than exporting the package's private policy constant; the
// budget-exhaustion e2e asserts the bgcheck chain plateaus at exactly this many
// dispatches. If the package default changes, update this in lockstep.
const bgcheckRetryCap = 3

// asyncConvergeOpts returns the harness overrides for the async variant.
// pollsUntilResolved (baked into the adapter the caller passes) selects the
// behavior: a small value resolves the call after a few polls (the happy path); a
// value the poll chain can never reach before the CallDeadline keeps every attempt
// timing out (the retry / exhausted legs). callDeadline is the per-call give-up
// horizon.
//
// The MarkLease (2s) is deliberately SHORT relative to the no-double-dispatch hold
// (well above 2s in the caller) so the dispatch mark's lease expires and the
// reconciler sweep's reclaim leg (skip site 2 — the load-bearing one) actually
// runs DURING the hold. The hold also asserts sweepLastRunAt advanced, so the AC
// fails loudly rather than passing vacuously if the sweeper never ticked under CI
// load.
func asyncConvergeOpts(adapter *bridge.FakeAsyncCheck, callDeadline time.Duration) []harnessOpt {
	return []harnessOpt{
		func(hc *harnessConfig) {
			hc.bgcheckAsync = adapter
			// A short lease so the dispatch mark expires fast → the sweep's reclaim
			// leg runs (skip site 2). SweepInterval ≤ MarkLease (the engine clamps
			// otherwise), and the warm-up is clamped up to ≥ SweepInterval.
			hc.weaverMarkLease = 2 * time.Second
			hc.weaverSweepInterval = 500 * time.Millisecond
			hc.weaverSweepWarmup = 500 * time.Millisecond
			// A short poll cadence so a resolvable call resolves quickly.
			hc.bridgePollInterval = 500 * time.Millisecond
			hc.bridgeCallDeadline = callDeadline
		},
	}
}

// weaverSweepLastRun reads the Weaver heartbeat's sweepLastRunAt metric (the
// instant the reconciler sweep last completed a pass), returning the zero time
// when the heartbeat or metric is absent. It is the "the sweeper actually ran"
// witness for the no-double-dispatch AC: a hold that asserts NO second dispatch
// across a sweep tick must prove a sweep tick happened, or it could pass vacuously
// if the sweeper stalled.
func (h *harness) weaverSweepLastRun() time.Time {
	entry, err := h.conn.KVGet(h.ctx, bootstrap.HealthKVBucket, "health.weaver.lc-weaver")
	if err != nil {
		return time.Time{}
	}
	var doc struct {
		Metrics struct {
			SweepLastRunAt string `json:"sweepLastRunAt"`
		} `json:"metrics"`
	}
	if json.Unmarshal(entry.Value, &doc) != nil || doc.Metrics.SweepLastRunAt == "" {
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339Nano, doc.Metrics.SweepLastRunAt)
	if err != nil {
		return time.Time{}
	}
	return ts
}

// countBgcheckInstances counts backgroundCheck-family service instances providedTo
// the applicant — one per genuine externalTask dispatch (each dispatch mints a new
// claim vertex). It is the no-double-dispatch witness.
func (h *harness) countBgcheckInstances(applicantID string) int {
	n := 0
	for _, svcKey := range h.serviceOutcomes(applicantID) {
		fam := h.aspectData(svcKey, "family")
		if fam != nil && fam["value"] == "backgroundCheck" {
			n++
		}
	}
	return n
}

// failedBgcheckInstances counts backgroundCheck-family instances carrying a
// terminal `failed` outcome — the retry-budget accounting the lens caps on.
func (h *harness) failedBgcheckInstances(applicantID string) int {
	n := 0
	for _, svcKey := range h.serviceOutcomes(applicantID) {
		fam := h.aspectData(svcKey, "family")
		if fam == nil || fam["value"] != "backgroundCheck" {
			continue
		}
		if oc := h.aspectData(svcKey, "outcome"); oc != nil && oc["status"] == "failed" {
			n++
		}
	}
	return n
}

// TestAsyncConvergence_NoDoubleDispatch_AcrossSweepTick is the core AC: while an
// async bgcheck is legitimately in flight (a .dispatch marker, no .outcome, future
// deadline → the lens projects inflight_bgcheck=true), Weaver must NOT dispatch a
// SECOND external call — NOT on a CDC touch (skip site 1) and NOT when the dispatch
// mark's lease expires and the reconciler sweep ticks (skip site 2, the
// load-bearing one). The call then resolves on a later poll → exactly ONE external
// call, and the bgcheck gap closes.
func TestAsyncConvergence_NoDoubleDispatch_AcrossSweepTick(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the all-engines async convergence e2e in -short mode")
	}
	// PollsUntilResolved=6 at a 500ms cadence → resolves ~3s in, comfortably
	// before the 75s freshness window lapses but after several mark-lease (2s)
	// expiries + sweep (500ms) ticks have had the chance to re-dispatch.
	adapter := bridge.NewFakeAsyncCheck(6)
	// CallDeadline far beyond resolution so the happy path is a poll-resolve, not
	// a timeout.
	h := newHarness(t, asyncConvergeOpts(adapter, 60*time.Second)...)
	appKey, appID, applicantKey := h.seedApplicant()

	// Drive PII + sign so the only externalTask gaps are bgcheck + payment; the
	// payment (synchronous FakeStripe) converges normally.
	h.driveApplicantSteps(appKey, applicantKey)
	applicantID := applicantKey[len("vtx.identity."):]

	// Wait until the bgcheck is in flight: the lens projects inflight_bgcheck=true
	// (the call was submitted, no outcome yet) with the gap still open.
	require.Eventuallyf(t, func() bool {
		row := h.readRow(appID)
		return row != nil && rowBool(row, "inflight_bgcheck") && rowBool(row, "missing_bgcheck")
	}, 30*time.Second, 100*time.Millisecond, "the async bgcheck must reach the in-flight state (inflight_bgcheck=true, gap still open)")

	require.Equal(t, 1, h.countBgcheckInstances(applicantID), "exactly one bgcheck instance dispatched")
	bgHandle := h.bgcheckHandle(applicantID)
	require.NotEmpty(t, bgHandle)

	// Snapshot the sweep clock so the hold below can prove the reconciler sweep
	// actually ticked while the call was in-flight (Fix #4: the AC must not pass
	// vacuously if the sweeper stalled under CI load).
	sweepBefore := h.weaverSweepLastRun()

	// Hold across several mark-lease expiries (2s) + sweep ticks (500ms): the
	// in-flight call must NOT be re-dispatched. Exactly ONE bgcheck instance +
	// exactly ONE real vendor side-effect throughout (the side-effect counter is
	// keyed by the bare instance handle == idempotencyKey). The 10s hold spans
	// several full mark-lease (2s) windows, so the sweep's reclaim leg (skip site
	// 2) is exercised repeatedly.
	require.Neverf(t, func() bool {
		return h.countBgcheckInstances(applicantID) > 1 || adapter.SideEffects(bgHandle) > 1
	}, 10*time.Second, 300*time.Millisecond, "NO second dispatch while the call is in flight, even across a mark-lease expiry + sweep tick (skip site 2)")

	// The sweeper genuinely ran during the hold (its last-run instant advanced past
	// one full mark-lease, the 2s asyncConvergeOpts sets) — so the
	// no-double-dispatch guarantee above was actually tested against skip site 2,
	// not a stalled sweeper.
	require.Eventuallyf(t, func() bool {
		return h.weaverSweepLastRun().After(sweepBefore.Add(2 * time.Second))
	}, 15*time.Second, 200*time.Millisecond, "the reconciler sweep must complete a pass (past one mark-lease) during the in-flight hold, so skip site 2 is genuinely exercised")

	// The poll chain resolves the call → the gap closes and the application
	// converges. Still exactly one external call.
	h.drainUntilConverged(appID, 60*time.Second)
	require.Equal(t, 1, h.countBgcheckInstances(applicantID), "the resolved async call is the SAME single instance — no extra dispatch")
	require.Equal(t, 1, adapter.SideEffects(bgHandle), "exactly one real vendor side-effect for the whole async round-trip")
	row := h.readRow(appID)
	require.NotNil(t, row)
	require.False(t, rowBool(row, "missing_bgcheck"), "the resolved bgcheck closes its gap")
	require.False(t, rowBool(row, "inflight_bgcheck"), "a resolved call is no longer in flight")
}

// TestAsyncConvergence_Timeout_FailedThenOneRetry proves the blessed wedged-state
// behavior: a call that never resolves before its CallDeadline is posted a
// terminal `failed` outcome by the bridge timeout; inflight_bgcheck then flips
// false (the deadline passed) and Weaver dispatches a FRESH call (a new claim
// vertex / vendorRef — never a silent resubmit of the same one). Two-plus bgcheck
// instances accrue (the timed-out one + the retry), bounded to one fresh call per
// timeout — never a double-dispatch within a single in-flight window.
func TestAsyncConvergence_Timeout_FailedThenOneRetry(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the all-engines async convergence e2e in -short mode")
	}
	// The adapter never resolves within the short deadline, so the first call (and
	// each subsequent one) times out → a terminal failed outcome + a fresh retry.
	adapter := bridge.NewFakeAsyncCheck(1000)
	h := newHarness(t, asyncConvergeOpts(adapter, 4*time.Second)...)
	appKey, _, applicantKey := h.seedApplicant()
	h.driveApplicantSteps(appKey, applicantKey)
	applicantID := applicantKey[len("vtx.identity."):]

	// The first call times out → a terminal failed outcome lands on the first
	// bgcheck instance.
	require.Eventuallyf(t, func() bool {
		return h.failedBgcheckInstances(applicantID) >= 1
	}, 30*time.Second, 200*time.Millisecond, "the timed-out async call must post a terminal failed outcome")

	// A fresh retry dispatches (a NEW bgcheck instance): the failed one no longer
	// counts inflight (deadline passed + outcome present), so Weaver re-dispatches
	// exactly one fresh call. Two-plus instances total — bounded, not a storm.
	require.Eventuallyf(t, func() bool {
		return h.countBgcheckInstances(applicantID) >= 2
	}, 30*time.Second, 200*time.Millisecond, "a failed (timed-out) call must trigger a fresh retry")

	// The retry's adapter also never resolves in time, so the chain keeps failing —
	// but the count must climb at most ~one per timeout (4s), never double-dispatch
	// within a single in-flight window: within 5s the count grows by at most ~2.
	countAfterFirstRetry := h.countBgcheckInstances(applicantID)
	require.Neverf(t, func() bool {
		return h.countBgcheckInstances(applicantID) > countAfterFirstRetry+2
	}, 5*time.Second, 250*time.Millisecond, "retries are bounded to one fresh call per timeout — never a double-dispatch within one in-flight window")
}

// TestAsyncConvergence_BoundedRetry_Exhausted proves the §E mechanism-B retry
// budget end-to-end: after bgcheckRetryCap (3) failed dispatches the Weaver-state
// dispatch-count reaches the row's maxretries_bgcheck cap, Weaver STOPS
// auto-dispatching (no further bgcheck instance is minted — through EITHER skip
// site), and the gap STAYS violating (the "stop and escalate" terminal — not a
// silent reject). Every attempt times out (the adapter never resolves), so each
// CallDeadline mints one failed instance until the cap. The budget is Weaver-state
// (a dispatch-count, not a projected lens column), so the test asserts exhaustion
// BEHAVIORALLY — the bgcheck instance count plateaus at the cap.
func TestAsyncConvergence_BoundedRetry_Exhausted(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the all-engines async convergence e2e in -short mode")
	}
	adapter := bridge.NewFakeAsyncCheck(1000) // never resolves → every attempt times out
	// A short CallDeadline so three timeouts accrue within the test budget.
	h := newHarness(t, asyncConvergeOpts(adapter, 2*time.Second)...)
	appKey, appID, applicantKey := h.seedApplicant()
	h.driveApplicantSteps(appKey, applicantKey)
	applicantID := applicantKey[len("vtx.identity."):]

	// The chain dispatches up to bgcheckRetryCap fresh calls (each times out →
	// failed → a fresh retry), then the dispatch-count reaches the cap and Weaver
	// stops. Wait until at least bgcheckRetryCap bgcheck instances have been
	// minted (the cap's worth of attempts).
	require.Eventuallyf(t, func() bool {
		return h.countBgcheckInstances(applicantID) >= bgcheckRetryCap
	}, 90*time.Second, 250*time.Millisecond, "the chain must dispatch up to the retry cap (bgcheckRetryCap fresh calls)")

	// Once the budget is spent, Weaver stops auto-dispatching: the bgcheck instance
	// count must NOT grow beyond the cap (no op fires through either skip site).
	// Hold well past several mark-lease (2s) + CallDeadline (2s) windows so a
	// would-be sweep re-dispatch or a post-timeout retry would have fired if the
	// budget were not enforced.
	require.Neverf(t, func() bool {
		return h.countBgcheckInstances(applicantID) > bgcheckRetryCap
	}, 10*time.Second, 300*time.Millisecond, "no further bgcheck dispatch once the retry budget (bgcheckRetryCap) is spent")

	require.Equal(t, bgcheckRetryCap, h.countBgcheckInstances(applicantID),
		"exactly bgcheckRetryCap bgcheck instances — the budget caps the chain")
	require.GreaterOrEqual(t, h.failedBgcheckInstances(applicantID), bgcheckRetryCap-1,
		"all but possibly the last (still-pending) attempt have posted a terminal failed outcome")

	row := h.readRow(appID)
	require.NotNil(t, row)
	require.True(t, rowBool(row, "missing_bgcheck"), "the gap stays OPEN (no completed bgcheck)")
	require.True(t, rowBool(row, "violating"), "a budget-exhausted gap keeps the application violating — needs human escalation")
}
