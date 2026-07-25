// CapabilityRepairFailing — the verdict every other auth-plane signal misses.
// The consumer can be active and fully caught up, the sweep can be running its
// bounded pass on schedule, and the divergence streak can read zero, all while
// a row the sweep cannot write stays wrong indefinitely. The divergence code
// cannot cover this: a failed repair heals nothing, so it CLEARS that streak.
package health

import (
	"strings"
	"testing"
	"time"
)

func repairSnap(name string, failing, failedStreak int, lastFailure string) CapabilityLensStatus {
	return CapabilityLensStatus{
		CanonicalName:      name,
		RuleID:             "lnk-" + name,
		Status:             "active",
		ConsumerLag:        0,
		SweepFailingActors: failing,
		SweepFailedStreak:  failedStreak,
		SweepLastFailure:   lastFailure,
	}
}

func TestEvalCapabilityLenses_AFailingRepairIsNotOk(t *testing.T) {
	// An adapter write that fails forever must not leave the lens publishing
	// alert "ok". The heal count alone cannot see it: a failed heal counts as
	// zero, exactly like a pass that had nothing to do.
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{
			repairSnap("capabilityRoles", 1, capabilityRepairWarnStreak, "adapter: payload too large"),
		}
	}}
	metric, issues := h.evalCapabilityLenses(time.Now())

	if got := metric["capabilityRoles"]["alert"]; got != "repair-failing" {
		t.Fatalf("alert = %v, want repair-failing", got)
	}
	if got := metric["capabilityRoles"]["failingActors"]; got != 1 {
		t.Fatalf("failingActors = %v, want 1", got)
	}
	is, ok := issueByCode(issues, issueCapabilityRepairFailing)
	if !ok {
		t.Fatalf("expected %s, got %v", issueCapabilityRepairFailing, issues)
	}
	if is.Severity != "warning" {
		t.Fatalf("severity = %q, want warning at the raise threshold", is.Severity)
	}
	if !strings.Contains(is.Message, "payload too large") {
		t.Fatalf("message must name the cause, not just a count: %q", is.Message)
	}
	if s := aggregateStatus(issues); s != "degraded" {
		t.Fatalf("status = %q, want degraded", s)
	}
}

func TestEvalCapabilityLenses_RecurringRepairFailureEscalatesToError(t *testing.T) {
	// At the error threshold the row is not converging on its own — and unlike
	// a healed divergence it is wrong the whole time.
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{
			repairSnap("capabilityRoles", 2, capabilityRepairErrorStreak, "adapter: payload too large"),
		}
	}}
	_, issues := h.evalCapabilityLenses(time.Now())
	is, ok := issueByCode(issues, issueCapabilityRepairFailing)
	if !ok {
		t.Fatalf("expected %s, got %v", issueCapabilityRepairFailing, issues)
	}
	if is.Severity != "error" {
		t.Fatalf("severity = %q, want error", is.Severity)
	}
	if s := aggregateStatus(issues); s != "unhealthy" {
		t.Fatalf("status = %q, want unhealthy", s)
	}
}

func TestEvalCapabilityLenses_ACleanSweepRaisesNoRepairIssue(t *testing.T) {
	// A converged lens must not carry a repair alert, or a single transient
	// write error would alert forever.
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{repairSnap("capabilityRoles", 0, 0, "")}
	}}
	metric, issues := h.evalCapabilityLenses(time.Now())
	if _, ok := issueByCode(issues, issueCapabilityRepairFailing); ok {
		t.Fatalf("a converged sweep must raise no repair issue, got %v", issues)
	}
	if got := metric["capabilityRoles"]["alert"]; got != "ok" {
		t.Fatalf("alert = %v, want ok", got)
	}
}

func TestEvalCapabilityLenses_RepairFailureIsIndependentOfDivergence(t *testing.T) {
	// The two verdicts are orthogonal and must both be reachable: this snapshot
	// healed nothing and repaired nothing, which is exactly the state the
	// divergence code reads as converged.
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{
			repairSnap("capabilityRoles", 1, capabilityRepairWarnStreak, "adapter: target write refused"),
		}
	}}
	_, issues := h.evalCapabilityLenses(time.Now())
	if _, ok := issueByCode(issues, issueCapabilityCoverageDivergence); ok {
		t.Fatal("a zero divergent streak must raise no divergence issue")
	}
	if _, ok := issueByCode(issues, issueCapabilityLensLagging); ok {
		t.Fatal("an unrepaired row must not be reported as consumer lag")
	}
	if _, ok := issueByCode(issues, issueCapabilityRepairFailing); !ok {
		t.Fatal("the unrepaired row must be reported by the repair code")
	}
}

func TestEvalCapabilityLenses_PausedKeepsPrecedenceOverRepairFailing(t *testing.T) {
	// The sweep is suppressed while a lens is paused, so a lingering failure
	// there is a frozen artifact of the last active pass. The pause is the
	// operator-actionable fact and must stay the reported alert.
	snap := repairSnap("capabilityRoles", 1, 3, "adapter: target write refused")
	snap.Status = "paused"
	snap.PauseReason = "operator"
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{snap}
	}}
	metric, issues := h.evalCapabilityLenses(time.Now())
	if got := metric["capabilityRoles"]["alert"]; got != "paused" {
		t.Fatalf("alert = %v, want paused", got)
	}
	if _, ok := issueByCode(issues, issueCapabilityRepairFailing); !ok {
		t.Fatal("the repair issue is still raised; only the alert label defers to the pause")
	}
}

func TestEvalCapabilityLenses_RepairSinceIsStableAcrossHeartbeats(t *testing.T) {
	// Contract #5 §5.5: an open issue keeps its original `since` across beats.
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{
			repairSnap("capabilityRoles", 1, capabilityRepairWarnStreak, "adapter: target write refused"),
		}
	}}
	_, first := h.evalCapabilityLenses(time.Now())
	firstIssue, _ := issueByCode(first, issueCapabilityRepairFailing)
	_, second := h.evalCapabilityLenses(time.Now().Add(time.Minute))
	secondIssue, _ := issueByCode(second, issueCapabilityRepairFailing)
	if firstIssue.Since != secondIssue.Since {
		t.Fatalf("since drifted across heartbeats: %q → %q", firstIssue.Since, secondIssue.Since)
	}
}

func TestEvalCapabilityLenses_ASingleFailingPassRaisesNothing(t *testing.T) {
	// A failing anchor is retried on the very next pass, so an isolated write
	// error clears itself inside one interval. Alerting on it would flip the
	// whole instance to degraded for every blip. The gauge stays truthful
	// regardless — only the alert and the issue debounce.
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{repairSnap("capabilityRoles", 1, 1, "adapter: transient")}
	}}
	metric, issues := h.evalCapabilityLenses(time.Now())
	if _, ok := issueByCode(issues, issueCapabilityRepairFailing); ok {
		t.Fatalf("one failing pass must raise nothing, got %v", issues)
	}
	if got := metric["capabilityRoles"]["alert"]; got != "ok" {
		t.Fatalf("alert = %v, want ok below the raise threshold", got)
	}
	if got := metric["capabilityRoles"]["failingActors"]; got != 1 {
		t.Fatalf("failingActors = %v, want 1 — the gauge does not debounce", got)
	}
}

func TestEvalCapabilityLenses_APassLevelFaultNamesNoActorCount(t *testing.T) {
	// An unreadable survey verifies nothing but implicates no specific anchor.
	// The message must not claim "0 actor(s) unrepaired".
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{
			repairSnap("capabilityRoles", 0, capabilityRepairWarnStreak, "target adapter cannot enumerate keys"),
		}
	}}
	_, issues := h.evalCapabilityLenses(time.Now())
	is, ok := issueByCode(issues, issueCapabilityRepairFailing)
	if !ok {
		t.Fatalf("expected %s, got %v", issueCapabilityRepairFailing, issues)
	}
	if strings.Contains(is.Message, "0 actor(s)") {
		t.Fatalf("a pass-level fault must not report an actor count: %q", is.Message)
	}
	if !strings.Contains(is.Message, "verified nothing") {
		t.Fatalf("message must say what went wrong: %q", is.Message)
	}
}

func TestEvalCapabilityLenses_RepairFailingOutranksLagging(t *testing.T) {
	// A row that is wrong now beats a read-model that is merely behind. The
	// displaced lag is not lost: consumerLag stays in the same map and the lag
	// issue is still raised on its own debounce.
	snap := repairSnap("capabilityRoles", 1, capabilityRepairWarnStreak, "adapter: target write refused")
	snap.ConsumerLag = defaultCapabilityLensLagThreshold * 10
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{snap}
	}}
	var metric map[string]map[string]any
	// The lag signal debounces over raiseCycles beats; the repair alert must
	// hold precedence once the lag has actually raised.
	for i := 0; i < defaultCapabilityLensLagRaiseCycles+1; i++ {
		metric, _ = h.evalCapabilityLenses(time.Now())
	}
	if got := metric["capabilityRoles"]["alert"]; got != "repair-failing" {
		t.Fatalf("alert = %v, want repair-failing to outrank lagging", got)
	}
	if got := metric["capabilityRoles"]["consumerLag"]; got != snap.ConsumerLag {
		t.Fatalf("consumerLag = %v, want the lag value preserved", got)
	}
}
