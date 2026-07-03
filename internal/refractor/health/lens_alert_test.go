// Generalized (non-auth-plane) lens liveness backstop (Contract #5 §5.5 issues +
// §5.4 status) — lens-projection-liveness-design.md §3.3. Pure unit tests over
// the unexported evaluation path — no NATS. Mirrors caplens_alert_test.go.
package health

import (
	"strings"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

func lensSnap(name, status, reason string, lag uint64) LensLivenessStatus {
	return LensLivenessStatus{CanonicalName: name, RuleID: "lnk-" + name, Status: status, PauseReason: reason, ProjectionLag: lag}
}

func TestEvalLenses_NilProvider(t *testing.T) {
	h := &LatticeHeartbeater{}
	metric, issues := h.evalLenses(time.Now())
	if metric != nil || issues != nil {
		t.Fatalf("nil provider must yield (nil,nil), got metric=%v issues=%v", metric, issues)
	}
}

func TestEvalLenses_HealthyWithinThreshold(t *testing.T) {
	h := &LatticeHeartbeater{LensProvider: func() []LensLivenessStatus {
		return []LensLivenessStatus{lensSnap("clinicAppointments", "active", "", 5)}
	}}
	metric, issues := h.evalLenses(time.Now())
	if len(issues) != 0 {
		t.Fatalf("active within threshold must raise no issue, got %v", issues)
	}
	if got := metric["clinicAppointments"]["alert"]; got != "ok" {
		t.Fatalf("alert = %v, want ok", got)
	}
	if s := aggregateStatus(issues); s != "healthy" {
		t.Fatalf("status = %q, want healthy", s)
	}
}

// TestEvalLenses_PausedIsWarningDegraded is the one substantive difference from
// the cap path (design §3.3): a paused BUSINESS lens is severity warning
// (degraded), never error (unhealthy) — a single frozen business lens is a real
// outage for that vertical but must not nuke the whole Refractor instance.
func TestEvalLenses_PausedIsWarningDegraded(t *testing.T) {
	h := &LatticeHeartbeater{LensProvider: func() []LensLivenessStatus {
		return []LensLivenessStatus{lensSnap("clinicAppointments", "paused", "structural", 0)}
	}}
	metric, issues := h.evalLenses(time.Now())
	is, ok := issueByCode(issues, issueLensProjectionPaused)
	if !ok {
		t.Fatalf("expected %s issue, got %v", issueLensProjectionPaused, issues)
	}
	if is.Severity != "warning" {
		t.Fatalf("severity = %q, want warning (business-lens paused must degrade, not fail, the instance)", is.Severity)
	}
	if !strings.Contains(is.Message, "structural") || !strings.Contains(is.Message, "clinicAppointments") {
		t.Fatalf("message missing lens/reason: %q", is.Message)
	}
	if got := metric["clinicAppointments"]["alert"]; got != "paused" {
		t.Fatalf("alert = %v, want paused", got)
	}
	if s := aggregateStatus(issues); s != "degraded" {
		t.Fatalf("status = %q, want degraded (never unhealthy for a business lens)", s)
	}
}

func TestEvalLenses_LaggingIsWarningDegraded(t *testing.T) {
	h := &LatticeHeartbeater{
		LensLagRaiseCycles: 1, // exercise the severity mapping in one cycle
		LensProvider: func() []LensLivenessStatus {
			return []LensLivenessStatus{lensSnap("clinicAppointments", "active", "", 250)}
		}}
	metric, issues := h.evalLenses(time.Now())
	is, ok := issueByCode(issues, issueLensProjectionLagging)
	if !ok {
		t.Fatalf("expected %s issue, got %v", issueLensProjectionLagging, issues)
	}
	if is.Severity != "warning" {
		t.Fatalf("severity = %q, want warning", is.Severity)
	}
	if got := metric["clinicAppointments"]["alert"]; got != "lagging" {
		t.Fatalf("alert = %v, want lagging", got)
	}
	if s := aggregateStatus(issues); s != "degraded" {
		t.Fatalf("status = %q, want degraded", s)
	}
}

func TestEvalLenses_LagSpikeDoesNotFlap(t *testing.T) {
	lag := uint64(0)
	h := &LatticeHeartbeater{LensProvider: func() []LensLivenessStatus {
		return []LensLivenessStatus{lensSnap("clinicAppointments", "active", "", lag)}
	}}
	t0 := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)

	lag = 9999 // one-cycle spike, way over the default threshold
	metric, issues := h.evalLenses(t0)
	if len(issues) != 0 {
		t.Fatalf("a single over-threshold cycle must not raise (debounce), got %v", issues)
	}
	if got := metric["clinicAppointments"]["alert"]; got != "ok" {
		t.Fatalf("alert during the pending debounce = %v, want ok", got)
	}

	lag = 0 // drains on the next beat
	_, issues = h.evalLenses(t0.Add(10 * time.Second))
	if len(issues) != 0 {
		t.Fatalf("drained lens must stay clean, got %v", issues)
	}
}

func TestEvalLenses_RebuildingNoIssue(t *testing.T) {
	h := &LatticeHeartbeater{LensProvider: func() []LensLivenessStatus {
		return []LensLivenessStatus{lensSnap("clinicAppointments", "rebuilding", "", 9999)}
	}}
	metric, issues := h.evalLenses(time.Now())
	if len(issues) != 0 {
		t.Fatalf("rebuilding must raise no issue, got %v", issues)
	}
	if got := metric["clinicAppointments"]["alert"]; got != "ok" {
		t.Fatalf("alert = %v, want ok", got)
	}
}

func TestEvalLenses_SincePersistsThenResolves(t *testing.T) {
	paused := true
	h := &LatticeHeartbeater{LensProvider: func() []LensLivenessStatus {
		if paused {
			return []LensLivenessStatus{lensSnap("clinicAppointments", "paused", "manual", 0)}
		}
		return []LensLivenessStatus{lensSnap("clinicAppointments", "active", "", 0)}
	}}

	t0 := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	_, issues := h.evalLenses(t0)
	is0, ok := issueByCode(issues, issueLensProjectionPaused)
	if !ok {
		t.Fatal("first cycle must open the paused issue")
	}
	wantSince := substrate.FormatTimestamp(t0)
	if is0.Since != wantSince {
		t.Fatalf("since = %q, want %q", is0.Since, wantSince)
	}

	_, issues = h.evalLenses(t0.Add(30 * time.Second))
	is1, _ := issueByCode(issues, issueLensProjectionPaused)
	if is1.Since != wantSince {
		t.Fatalf("since advanced across heartbeats: %q != %q", is1.Since, wantSince)
	}

	paused = false
	_, issues = h.evalLenses(t0.Add(60 * time.Second))
	if len(issues) != 0 {
		t.Fatalf("resolved issue must be dropped, got %v", issues)
	}
	if len(h.openLensIssues) != 0 {
		t.Fatalf("open-issue set must be empty after resolution, got %v", h.openLensIssues)
	}
}

// TestEvalLenses_DoesNotDoubleIssueAuthPlane proves the two paths are
// independent: evaluating a lens through evalLenses does not touch the cap
// path's state (and vice versa), so wiring both providers with disjoint lens
// populations never double-issues or cross-prunes (design §5.1).
func TestEvalLenses_DoesNotDoubleIssueAuthPlane(t *testing.T) {
	h := &LatticeHeartbeater{
		CapabilityLensLagRaiseCycles: 1,
		CapabilityLensProvider: func() []CapabilityLensStatus {
			return []CapabilityLensStatus{snap("capabilityRoles", "active", "", 250)}
		},
		LensLagRaiseCycles: 1,
		LensProvider: func() []LensLivenessStatus {
			return []LensLivenessStatus{lensSnap("clinicAppointments", "active", "", 250)}
		},
	}
	now := time.Now()
	_, capIssues := h.evalCapabilityLenses(now)
	_, lensIssues := h.evalLenses(now)

	if _, ok := issueByCode(capIssues, issueCapabilityLensLagging); !ok {
		t.Fatalf("cap path must still raise on its own lens, got %v", capIssues)
	}
	if _, ok := issueByCode(lensIssues, issueLensProjectionLagging); !ok {
		t.Fatalf("general path must raise on its own lens, got %v", lensIssues)
	}
	if len(h.lagState) != 1 {
		t.Fatalf("cap lag state must track only the cap lens, got %v", h.lagState)
	}
	if len(h.lensLagState) != 1 {
		t.Fatalf("general lens-lag state must track only the business lens, got %v", h.lensLagState)
	}

	// A second cap-only cycle must not prune the general path's state, and
	// vice versa — the failure mode a shared map would have produced.
	h.evalCapabilityLenses(now.Add(10 * time.Second))
	if len(h.lensLagState) != 1 {
		t.Fatalf("cap-path eval must not prune the general lens-lag state, got %v", h.lensLagState)
	}
	h.evalLenses(now.Add(10 * time.Second))
	if len(h.lagState) != 1 {
		t.Fatalf("general-path eval must not prune the cap lens-lag state, got %v", h.lagState)
	}
}
