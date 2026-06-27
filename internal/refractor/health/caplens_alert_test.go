// Capability-lens liveness/lag threshold model (Contract #5 §5.5 issues +
// §5.4 status). Pure unit tests over the unexported evaluation path — no NATS.
package health

import (
	"strings"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

func snap(name, status, reason string, lag uint64) CapabilityLensStatus {
	return CapabilityLensStatus{CanonicalName: name, RuleID: "lnk-" + name, Status: status, PauseReason: reason, ConsumerLag: lag}
}

func issueByCode(issues []issueRecord, code string) (issueRecord, bool) {
	for _, is := range issues {
		if is.Code == code {
			return is, true
		}
	}
	return issueRecord{}, false
}

func TestEvalCapabilityLenses_NilProvider(t *testing.T) {
	h := &LatticeHeartbeater{}
	metric, issues := h.evalCapabilityLenses(time.Now())
	if metric != nil || issues != nil {
		t.Fatalf("nil provider must yield (nil,nil), got metric=%v issues=%v", metric, issues)
	}
}

func TestEvalCapabilityLenses_HealthyWithinThreshold(t *testing.T) {
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{snap("capabilityRoles", "active", "", 5)}
	}}
	metric, issues := h.evalCapabilityLenses(time.Now())
	if len(issues) != 0 {
		t.Fatalf("active within threshold must raise no issue, got %v", issues)
	}
	if got := metric["capabilityRoles"]["alert"]; got != "ok" {
		t.Fatalf("alert = %v, want ok", got)
	}
	if s := aggregateStatus(issues); s != "healthy" {
		t.Fatalf("status = %q, want healthy", s)
	}
}

func TestEvalCapabilityLenses_PausedIsErrorUnhealthy(t *testing.T) {
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{snap("capabilityRoles", "paused", "structural", 0)}
	}}
	metric, issues := h.evalCapabilityLenses(time.Now())
	is, ok := issueByCode(issues, issueCapabilityLensPaused)
	if !ok {
		t.Fatalf("expected %s issue, got %v", issueCapabilityLensPaused, issues)
	}
	if is.Severity != "error" {
		t.Fatalf("severity = %q, want error", is.Severity)
	}
	if !strings.Contains(is.Message, "structural") || !strings.Contains(is.Message, "capabilityRoles") {
		t.Fatalf("message missing lens/reason: %q", is.Message)
	}
	if got := metric["capabilityRoles"]["alert"]; got != "paused" {
		t.Fatalf("alert = %v, want paused", got)
	}
	if s := aggregateStatus(issues); s != "unhealthy" {
		t.Fatalf("status = %q, want unhealthy", s)
	}
}

func TestEvalCapabilityLenses_LaggingIsWarningDegraded(t *testing.T) {
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{snap("capabilityRoleIndex", "active", "", 250)}
	}}
	metric, issues := h.evalCapabilityLenses(time.Now())
	is, ok := issueByCode(issues, issueCapabilityLensLagging)
	if !ok {
		t.Fatalf("expected %s issue, got %v", issueCapabilityLensLagging, issues)
	}
	if is.Severity != "warning" {
		t.Fatalf("severity = %q, want warning", is.Severity)
	}
	if got := metric["capabilityRoleIndex"]["alert"]; got != "lagging" {
		t.Fatalf("alert = %v, want lagging", got)
	}
	if s := aggregateStatus(issues); s != "degraded" {
		t.Fatalf("status = %q, want degraded", s)
	}
}

func TestEvalCapabilityLenses_ThresholdOverride(t *testing.T) {
	h := &LatticeHeartbeater{
		CapabilityLensLagThreshold: 5,
		CapabilityLensProvider: func() []CapabilityLensStatus {
			return []CapabilityLensStatus{snap("capabilityRoles", "active", "", 6)}
		},
	}
	_, issues := h.evalCapabilityLenses(time.Now())
	if _, ok := issueByCode(issues, issueCapabilityLensLagging); !ok {
		t.Fatalf("lag 6 over override threshold 5 must flag lagging, got %v", issues)
	}
}

func TestEvalCapabilityLenses_RebuildingNoIssue(t *testing.T) {
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		// A high lag during a rebuild must NOT flag lagging (only active does).
		return []CapabilityLensStatus{snap("capabilityRoles", "rebuilding", "", 9999)}
	}}
	metric, issues := h.evalCapabilityLenses(time.Now())
	if len(issues) != 0 {
		t.Fatalf("rebuilding must raise no issue, got %v", issues)
	}
	if got := metric["capabilityRoles"]["alert"]; got != "ok" {
		t.Fatalf("alert = %v, want ok", got)
	}
}

func TestEvalCapabilityLenses_SincePersistsThenResolves(t *testing.T) {
	paused := true
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		if paused {
			return []CapabilityLensStatus{snap("capabilityRoles", "paused", "manual", 0)}
		}
		return []CapabilityLensStatus{snap("capabilityRoles", "active", "", 0)}
	}}

	t0 := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	_, issues := h.evalCapabilityLenses(t0)
	is0, ok := issueByCode(issues, issueCapabilityLensPaused)
	if !ok {
		t.Fatal("first cycle must open the paused issue")
	}
	wantSince := substrate.FormatTimestamp(t0)
	if is0.Since != wantSince {
		t.Fatalf("since = %q, want %q", is0.Since, wantSince)
	}

	// Second cycle, later time, still paused: `since` must NOT advance.
	_, issues = h.evalCapabilityLenses(t0.Add(30 * time.Second))
	is1, _ := issueByCode(issues, issueCapabilityLensPaused)
	if is1.Since != wantSince {
		t.Fatalf("since advanced across heartbeats: %q != %q", is1.Since, wantSince)
	}

	// Resolve: next cycle drops the issue.
	paused = false
	_, issues = h.evalCapabilityLenses(t0.Add(60 * time.Second))
	if len(issues) != 0 {
		t.Fatalf("resolved issue must be dropped, got %v", issues)
	}
	if len(h.openCapIssues) != 0 {
		t.Fatalf("open-issue set must be empty after resolution, got %v", h.openCapIssues)
	}
}

func TestEvalCapabilityLenses_MultiplePausedAggregateAndCanonicalFallback(t *testing.T) {
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{
			snap("capabilityRoles", "paused", "infra", 0),
			{RuleID: "lnk-bare", Status: "paused", PauseReason: "structural"}, // empty canonical → key by ruleID
		}
	}}
	metric, issues := h.evalCapabilityLenses(time.Now())
	is, ok := issueByCode(issues, issueCapabilityLensPaused)
	if !ok {
		t.Fatalf("expected aggregated paused issue, got %v", issues)
	}
	if !strings.Contains(is.Message, "capabilityRoles") || !strings.Contains(is.Message, "lnk-bare") {
		t.Fatalf("aggregated message must name both lenses: %q", is.Message)
	}
	if _, ok := metric["lnk-bare"]; !ok {
		t.Fatalf("empty canonical name must fall back to ruleID key; metric=%v", metric)
	}
}

func TestAggregateStatus_ErrorBeatsWarning(t *testing.T) {
	got := aggregateStatus([]issueRecord{
		{Code: "A", Severity: "warning"},
		{Code: "B", Severity: "error"},
	})
	if got != "unhealthy" {
		t.Fatalf("status = %q, want unhealthy (error dominates)", got)
	}
}
