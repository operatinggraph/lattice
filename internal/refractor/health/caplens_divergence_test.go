// CapabilityCoverageDivergence — the auth-plane convergence sweep's alert
// (capability-projection-reconciliation-design.md §3.2). The lag codes watch
// the consumer; this one watches the truth, so it must fire on a lens that is
// active and fully caught up.
package health

import (
	"strings"
	"testing"
	"time"
)

func divergenceSnap(name string, streak int, reconciled uint64) CapabilityLensStatus {
	return CapabilityLensStatus{
		CanonicalName:        name,
		RuleID:               "lnk-" + name,
		Status:               "active",
		ConsumerLag:          0,
		SweepReconciled:      reconciled,
		SweepDivergentStreak: streak,
	}
}

func TestEvalCapabilityLenses_CleanSweepRaisesNothing(t *testing.T) {
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		// Healed in the past, converged now: the counter stays visible but the
		// issue is closed, or a single historical incident would alert forever.
		return []CapabilityLensStatus{divergenceSnap("capabilityRoles", 0, 7)}
	}}
	metric, issues := h.evalCapabilityLenses(time.Now())
	if _, ok := issueByCode(issues, issueCapabilityCoverageDivergence); ok {
		t.Fatalf("a converged sweep must raise no divergence issue, got %v", issues)
	}
	if got := metric["capabilityRoles"]["reconciled"]; got != uint64(7) {
		t.Fatalf("reconciled = %v, want 7", got)
	}
}

func TestEvalCapabilityLenses_OneDivergentPassIsAWarning(t *testing.T) {
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{divergenceSnap("capabilityRoles", 1, 1)}
	}}
	_, issues := h.evalCapabilityLenses(time.Now())
	is, ok := issueByCode(issues, issueCapabilityCoverageDivergence)
	if !ok {
		t.Fatalf("expected %s, got %v", issueCapabilityCoverageDivergence, issues)
	}
	if is.Severity != "warning" {
		t.Fatalf("severity = %q, want warning", is.Severity)
	}
	if !strings.Contains(is.Message, "capabilityRoles") {
		t.Fatalf("message missing the lens name: %q", is.Message)
	}
	if s := aggregateStatus(issues); s != "degraded" {
		t.Fatalf("status = %q, want degraded", s)
	}
}

func TestEvalCapabilityLenses_RecurringDivergenceEscalatesToError(t *testing.T) {
	// Two consecutive divergent sweeps mean events are still being lost — the
	// sweep is papering over an ongoing gap, not repairing a past one.
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{divergenceSnap("capabilityRoles", capabilityDivergenceErrorStreak, 4)}
	}}
	_, issues := h.evalCapabilityLenses(time.Now())
	is, ok := issueByCode(issues, issueCapabilityCoverageDivergence)
	if !ok {
		t.Fatalf("expected %s, got %v", issueCapabilityCoverageDivergence, issues)
	}
	if is.Severity != "error" {
		t.Fatalf("severity = %q, want error", is.Severity)
	}
	if s := aggregateStatus(issues); s != "unhealthy" {
		t.Fatalf("status = %q, want unhealthy", s)
	}
}

func TestEvalCapabilityLenses_DivergenceIsIndependentOfLagAndPause(t *testing.T) {
	// The whole point of the sweep: a lens with zero lag and an active status
	// can still have a hole in its projection.
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{divergenceSnap("capabilityRoles", 1, 1)}
	}}
	metric, issues := h.evalCapabilityLenses(time.Now())
	if got := metric["capabilityRoles"]["alert"]; got != "ok" {
		t.Fatalf("alert = %v, want ok (the consumer itself is healthy)", got)
	}
	if _, ok := issueByCode(issues, issueCapabilityLensLagging); ok {
		t.Fatal("a divergence must not be reported as consumer lag")
	}
	if _, ok := issueByCode(issues, issueCapabilityCoverageDivergence); !ok {
		t.Fatal("a healthy, caught-up consumer must still report its coverage divergence")
	}
}

func TestEvalCapabilityLenses_DivergenceSinceIsStableAcrossHeartbeats(t *testing.T) {
	// Contract #5 §5.5: an open issue keeps its original `since` across beats.
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{divergenceSnap("capabilityRoles", 1, 1)}
	}}
	_, first := h.evalCapabilityLenses(time.Now())
	firstIssue, _ := issueByCode(first, issueCapabilityCoverageDivergence)
	_, second := h.evalCapabilityLenses(time.Now().Add(time.Minute))
	secondIssue, _ := issueByCode(second, issueCapabilityCoverageDivergence)
	if firstIssue.Since != secondIssue.Since {
		t.Fatalf("since drifted across heartbeats: %q → %q", firstIssue.Since, secondIssue.Since)
	}
}
