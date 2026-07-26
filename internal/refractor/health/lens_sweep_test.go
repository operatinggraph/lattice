// The convergence sweep's verdicts on the business-lens path
// (lens-projection-liveness-design.md §15). Sibling of caplens_divergence_test.go /
// caplens_repair_failure_test.go / caplens_sweep_stall_test.go, with the one
// substantive difference asserted throughout: a business lens degrades the
// instance, it never fails it.
package health

import (
	"strings"
	"testing"
	"time"
)

// lensSweepSnap is an active business lens carrying sweep verdicts, on the
// auth-plane cadence unless a test says otherwise.
func lensSweepSnap(name string, interval time.Duration) LensLivenessStatus {
	return LensLivenessStatus{
		CanonicalName: name,
		RuleID:        "lns-" + name,
		Status:        "active",
		SweepInterval: interval,
	}
}

// beat evaluates one heartbeat with the given snapshots.
func beat(h *LatticeHeartbeater, at time.Time, snaps ...LensLivenessStatus) (map[string]map[string]any, []issueRecord) {
	h.LensProvider = func() []LensLivenessStatus { return snaps }
	return h.evalLenses(at)
}

// sweptThenHeld drives the temporal sequence every stall goes through: a first
// beat where each lens has just swept — which is what establishes the
// heartbeater's staleness baseline, since a sweeper is in-process and a lens the
// heartbeater has only just seen cannot already have an old verdict — then a
// beat `held` later where the sweep has produced nothing since. Returns the
// second beat's metric and issues.
func lensSweptThenHeld(h *LatticeHeartbeater, start time.Time, held time.Duration, snaps ...LensLivenessStatus) (map[string]map[string]any, []issueRecord) {
	fresh := make([]LensLivenessStatus, len(snaps))
	for i, s := range snaps {
		s.SweepLastPassAt = start
		s.SweepSuppression, s.SweepSuppressionAt = "", time.Time{}
		fresh[i] = s
	}
	beat(h, start, fresh...)
	return beat(h, start.Add(held), snaps...)
}

func TestEvalLenses_SweepDivergenceIsWarningAtEveryStreak(t *testing.T) {
	// The cap path escalates a second consecutive divergent pass to error. Here
	// it stays a warning however long it runs: the read model is wrong, which
	// is that vertical's outage, not the instance's.
	for _, streak := range []int{1, 2, 7} {
		snap := lensSweepSnap("myTasks", time.Minute)
		snap.SweepDivergentStreak = streak
		snap.SweepReconciled = uint64(streak * 3)
		h := &LatticeHeartbeater{LensProvider: func() []LensLivenessStatus {
			return []LensLivenessStatus{snap}
		}}
		_, issues := h.evalLenses(time.Now())
		is, ok := issueByCode(issues, issueLensCoverageDivergence)
		if !ok {
			t.Fatalf("streak %d: expected %s, got %v", streak, issueLensCoverageDivergence, issues)
		}
		if is.Severity != "warning" {
			t.Fatalf("streak %d: severity = %q, want warning at every streak length", streak, is.Severity)
		}
		if !strings.Contains(is.Message, "myTasks") {
			t.Fatalf("streak %d: message does not name the lens: %q", streak, is.Message)
		}
		if s := aggregateStatus(issues); s != "degraded" {
			t.Fatalf("streak %d: status = %q, want degraded", streak, s)
		}
	}
}

func TestEvalLenses_SweepRepairFailureRaisesAndStaysWarning(t *testing.T) {
	// The verdict every other signal misses: the consumer is caught up, nothing
	// healed, and the row is still wrong.
	snap := lensSweepSnap("identityAnchors", time.Minute)
	snap.SweepFailedStreak = capabilityRepairErrorStreak + 4
	snap.SweepFailingActors = 3
	snap.SweepLastFailure = "target write refused"
	h := &LatticeHeartbeater{LensProvider: func() []LensLivenessStatus {
		return []LensLivenessStatus{snap}
	}}
	metric, issues := h.evalLenses(time.Now())
	is, ok := issueByCode(issues, issueLensRepairFailing)
	if !ok {
		t.Fatalf("expected %s, got %v", issueLensRepairFailing, issues)
	}
	if is.Severity != "warning" {
		t.Fatalf("severity = %q, want warning well past the cap path's error streak", is.Severity)
	}
	for _, want := range []string{"identityAnchors", "3 actor(s)", "target write refused"} {
		if !strings.Contains(is.Message, want) {
			t.Fatalf("message missing %q: %q", want, is.Message)
		}
	}
	if got := metric["identityAnchors"]["alert"]; got != "repair-failing" {
		t.Fatalf("alert = %v, want repair-failing", got)
	}
	if s := aggregateStatus(issues); s != "degraded" {
		t.Fatalf("status = %q, want degraded — a business lens must never take the instance unhealthy", s)
	}
}

func TestEvalLenses_ASingleFailedPassRaisesNothing(t *testing.T) {
	// A failing anchor is retried on the very next pass, so an isolated write
	// error clears itself inside one interval.
	snap := lensSweepSnap("myTasks", time.Minute)
	snap.SweepFailedStreak = 1
	snap.SweepFailingActors = 1
	h := &LatticeHeartbeater{LensProvider: func() []LensLivenessStatus {
		return []LensLivenessStatus{snap}
	}}
	_, issues := h.evalLenses(time.Now())
	if _, ok := issueByCode(issues, issueLensRepairFailing); ok {
		t.Fatalf("one failing pass must not raise: %v", issues)
	}
}

func TestEvalLenses_SweepStallIsRaisedAndNamesItsCause(t *testing.T) {
	// Every other verdict describes the last completed pass, so a sweep that
	// stops passing keeps republishing them.
	start := time.Now()
	held := time.Hour
	at := start.Add(held)
	unexplained := lensSweepSnap("myTasks", time.Minute)
	unexplained.SweepLastPassAt = start
	explained := lensSweepSnap("identityAnchors", time.Minute)
	explained.SweepLastPassAt = start
	explained.SweepSuppression = "rebuild in flight"
	explained.SweepSuppressionAt = at.Add(-30 * time.Second)

	h := &LatticeHeartbeater{}
	metric, issues := lensSweptThenHeld(h, start, held, unexplained, explained)
	is, ok := issueByCode(issues, issueLensSweepStalled)
	if !ok {
		t.Fatalf("expected %s, got %v", issueLensSweepStalled, issues)
	}
	if is.Severity != "warning" {
		t.Fatalf("severity = %q, want warning", is.Severity)
	}
	if !strings.Contains(is.Message, "not ticking") {
		t.Fatalf("an unexplained stall must say the sweep is not ticking: %q", is.Message)
	}
	if !strings.Contains(is.Message, "rebuild in flight") {
		t.Fatalf("an explained stall must carry its cause: %q", is.Message)
	}
	if got := metric["myTasks"]["alert"]; got != "sweep-stalled" {
		t.Fatalf("alert = %v, want sweep-stalled", got)
	}
}

func TestEvalLenses_AStaleSuppressionReasonDoesNotExplainTheStall(t *testing.T) {
	// The reason describes the last tick, so a sweep wedged INSIDE a tick keeps
	// publishing the previous one's — which would otherwise present a wedged
	// sweep as merely suppressed.
	start := time.Now()
	snap := lensSweepSnap("myTasks", time.Minute)
	snap.SweepLastPassAt = start
	snap.SweepSuppression = "rebuild in flight"
	snap.SweepSuppressionAt = start
	h := &LatticeHeartbeater{}
	_, issues := lensSweptThenHeld(h, start, time.Hour, snap)
	is, ok := issueByCode(issues, issueLensSweepStalled)
	if !ok {
		t.Fatalf("expected %s, got %v", issueLensSweepStalled, issues)
	}
	if !strings.Contains(is.Message, "not ticking") {
		t.Fatalf("a stale suppression reason must not explain the stall: %q", is.Message)
	}
}

func TestEvalLenses_ALensWithNoSweeperIsNeverStalled(t *testing.T) {
	// A lens the install gate did not enrol has no cadence to be late against.
	// Reporting it stalled would raise a permanent warning about a detector
	// that was never meant to run.
	start := time.Now()
	snap := lensSweepSnap("unscopable", 0)
	snap.SweepLastPassAt = time.Time{}
	h := &LatticeHeartbeater{}
	// Two beats a day apart: the first establishes the baseline, so the second
	// is measured against a genuinely old one rather than against itself.
	beat(h, start, snap)
	metric, issues := beat(h, start.Add(24*time.Hour), snap)
	if _, ok := issueByCode(issues, issueLensSweepStalled); ok {
		t.Fatalf("a lens with no sweeper must not read as stalled: %v", issues)
	}
	if got := metric["unscopable"]["sweepEnrolled"]; got != false {
		t.Fatalf("sweepEnrolled = %v, want false — a lens the gate declined must say so", got)
	}
}

func TestEvalLenses_APausedLensIsExemptFromTheStallClock(t *testing.T) {
	// Suppression while paused is deliberate and indefinite, and the pause is
	// already an issue in its own right. Rebasing also stops a resumed lens
	// starting out stalled for the length of its pause.
	start := time.Now()
	h := &LatticeHeartbeater{}

	// Beat 1 — active and freshly swept: this is what establishes the baseline
	// at `start`, so the later beats measure against a real one.
	active := lensSweepSnap("myTasks", time.Minute)
	active.SweepLastPassAt = start
	beat(h, start, active)

	// Beat 2 — paused for an hour with no verdict since. Long past the stall
	// window, and it must still not read as stalled.
	paused := active
	paused.Status = "paused"
	paused.PauseReason = "structural"
	_, issues := beat(h, start.Add(time.Hour), paused)
	if _, ok := issueByCode(issues, issueLensSweepStalled); ok {
		t.Fatalf("a paused lens must not also read as stalled: %v", issues)
	}

	// Beat 3 — resumed a second later, still carrying the hour-old last pass.
	// Without the rebase the pause itself would be counted against it and the
	// lens would come back already stalled.
	resumed := paused
	resumed.Status = "active"
	resumed.PauseReason = ""
	_, issues = beat(h, start.Add(time.Hour+time.Second), resumed)
	if _, ok := issueByCode(issues, issueLensSweepStalled); ok {
		t.Fatalf("a lens resuming from a pause must not start out stalled: %v", issues)
	}
}

func TestEvalLenses_SweepVerdictsSurviveAnUnreadableEntry(t *testing.T) {
	// An unreadable health entry is an observation fault, not a sweep one. The
	// cap path reads the sweeper first for this reason; so does this one.
	snap := lensSweepSnap("myTasks", time.Minute)
	snap.Status = "unknown"
	snap.Unreadable = "lens health entry: boom"
	snap.SweepFailedStreak = capabilityRepairWarnStreak
	snap.SweepFailingActors = 2
	h := &LatticeHeartbeater{LensProvider: func() []LensLivenessStatus {
		return []LensLivenessStatus{snap}
	}}
	_, issues := h.evalLenses(time.Now())
	if _, ok := issueByCode(issues, issueLensRepairFailing); !ok {
		t.Fatalf("a live repair failure must not be lost to an unreadable entry: %v", issues)
	}
	if _, ok := issueByCode(issues, issueLensProjectionUnreadable); !ok {
		t.Fatalf("the unreadable entry must still be reported: %v", issues)
	}
}

func TestEvalLenses_PauseOutranksTheSweepAlertButBothIssuesStand(t *testing.T) {
	// Alert precedence is a single-slot summary; the issues array is where
	// nothing is displaced.
	snap := lensSweepSnap("myTasks", time.Minute)
	snap.Status = "paused"
	snap.PauseReason = "structural"
	snap.SweepFailedStreak = capabilityRepairWarnStreak
	snap.SweepFailingActors = 1
	h := &LatticeHeartbeater{LensProvider: func() []LensLivenessStatus {
		return []LensLivenessStatus{snap}
	}}
	metric, issues := h.evalLenses(time.Now())
	if got := metric["myTasks"]["alert"]; got != "paused" {
		t.Fatalf("alert = %v, want paused", got)
	}
	if _, ok := issueByCode(issues, issueLensRepairFailing); !ok {
		t.Fatalf("the repair failure must still raise its own issue: %v", issues)
	}
	if s := aggregateStatus(issues); s != "degraded" {
		t.Fatalf("status = %q, want degraded", s)
	}
}

func TestEvalLenses_TheStallClockIsNotSharedWithTheCapPath(t *testing.T) {
	// Both paths key their staleness baseline by lens name and each prunes
	// against its own lens set. One shared map would have each cycle evicting
	// the other path's baselines, and a lens whose baseline is re-stamped every
	// beat can never read as stalled.
	start := time.Now()
	business := lensSweepSnap("myTasks", time.Minute)
	business.SweepLastPassAt = start
	h := &LatticeHeartbeater{
		CapabilityLensProvider: func() []CapabilityLensStatus {
			return []CapabilityLensStatus{{
				CanonicalName: "capabilityRoles", RuleID: "cap-roles", Status: "active",
				SweepInterval: time.Minute, SweepLastPassAt: start,
			}}
		},
	}
	// The cap path runs first in a real beat and prunes its own map — on every
	// beat, including the one that establishes the business baseline.
	h.evalCapabilityLenses(start)
	beat(h, start, business)
	h.evalCapabilityLenses(start.Add(time.Hour))
	_, issues := beat(h, start.Add(time.Hour), business)
	if _, ok := issueByCode(issues, issueLensSweepStalled); !ok {
		t.Fatalf("the business stall verdict must survive the cap path's pruning: %v", issues)
	}
}
