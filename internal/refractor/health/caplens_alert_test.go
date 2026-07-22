// Capability-lens liveness/lag threshold model (Contract #5 §5.5 issues +
// §5.4 status). Pure unit tests over the unexported evaluation path — no NATS.
package health

import (
	"strings"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
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
	h := &LatticeHeartbeater{
		CapabilityLensLagRaiseCycles: 1, // exercise the severity mapping in one cycle
		CapabilityLensProvider: func() []CapabilityLensStatus {
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
		CapabilityLensLagThreshold:   5,
		CapabilityLensLagRaiseCycles: 1,
		CapabilityLensProvider: func() []CapabilityLensStatus {
			return []CapabilityLensStatus{snap("capabilityRoles", "active", "", 6)}
		},
	}
	_, issues := h.evalCapabilityLenses(time.Now())
	if _, ok := issueByCode(issues, issueCapabilityLensLagging); !ok {
		t.Fatalf("lag 6 over override threshold 5 must flag lagging, got %v", issues)
	}
}

func TestEvalCapabilityLenses_LagSpikeDoesNotFlap(t *testing.T) {
	// The filed bug: a single over-threshold heartbeat used to raise the warning
	// (and degrade status), then drop it the next cycle — a one-cycle spike flaps.
	// With the default raise-after-N debounce, a lone spike must NOT raise.
	lag := uint64(0)
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{snap("capabilityRoles", "active", "", lag)}
	}}
	t0 := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)

	lag = 9999 // one-cycle spike, way over the default threshold
	metric, issues := h.evalCapabilityLenses(t0)
	if len(issues) != 0 {
		t.Fatalf("a single over-threshold cycle must not raise (debounce), got %v", issues)
	}
	if got := metric["capabilityRoles"]["alert"]; got != "ok" {
		t.Fatalf("alert during the pending debounce = %v, want ok", got)
	}
	if s := aggregateStatus(issues); s != "healthy" {
		t.Fatalf("status after a lone spike = %q, want healthy (no flap)", s)
	}

	lag = 0 // drains on the next beat
	_, issues = h.evalCapabilityLenses(t0.Add(10 * time.Second))
	if len(issues) != 0 {
		t.Fatalf("drained lens must stay clean, got %v", issues)
	}
}

func TestEvalCapabilityLenses_LagRaisesAfterSustained(t *testing.T) {
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{snap("capabilityRoles", "active", "", 250)}
	}}
	t0 := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)

	// Cycles 1 and 2 (default raiseCycles = 3): over-threshold but not yet raised.
	for i := 0; i < 2; i++ {
		_, issues := h.evalCapabilityLenses(t0.Add(time.Duration(i) * 10 * time.Second))
		if len(issues) != 0 {
			t.Fatalf("cycle %d: must not raise before the streak is met, got %v", i+1, issues)
		}
	}
	// Cycle 3: the sustained backlog raises the warning, status degrades, and
	// `since` is stamped at this (the raise) cycle — not the first over-threshold one.
	raiseAt := t0.Add(2 * 10 * time.Second)
	_, issues := h.evalCapabilityLenses(raiseAt)
	is, ok := issueByCode(issues, issueCapabilityLensLagging)
	if !ok {
		t.Fatalf("cycle 3: sustained over-threshold must raise lagging, got %v", issues)
	}
	if is.Severity != "warning" {
		t.Fatalf("severity = %q, want warning", is.Severity)
	}
	if is.Since != substrate.FormatTimestamp(raiseAt) {
		t.Fatalf("since = %q, want the raise-cycle stamp %q", is.Since, substrate.FormatTimestamp(raiseAt))
	}
	if s := aggregateStatus(issues); s != "degraded" {
		t.Fatalf("status = %q, want degraded", s)
	}
}

func TestEvalCapabilityLenses_LagClearBandHoldsThenClears(t *testing.T) {
	// Raise immediately (raiseCycles=1); clear only at/below the lower band edge.
	lag := uint64(250)
	h := &LatticeHeartbeater{
		CapabilityLensLagThreshold:      100,
		CapabilityLensLagClearThreshold: 50,
		CapabilityLensLagRaiseCycles:    1,
		CapabilityLensProvider: func() []CapabilityLensStatus {
			return []CapabilityLensStatus{snap("capabilityRoles", "active", "", lag)}
		}}
	t0 := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)

	_, issues := h.evalCapabilityLenses(t0)
	if _, ok := issueByCode(issues, issueCapabilityLensLagging); !ok {
		t.Fatalf("lag over threshold must raise on the first cycle (raiseCycles=1), got %v", issues)
	}
	// Drop into the hysteresis band (>clearThreshold, ≤threshold): the issue HOLDS.
	lag = 80
	_, issues = h.evalCapabilityLenses(t0.Add(10 * time.Second))
	if _, ok := issueByCode(issues, issueCapabilityLensLagging); !ok {
		t.Fatalf("lag in the band (50<80≤100) must keep the issue raised, got %v", issues)
	}
	// Drop to/below the clear threshold: the issue clears.
	lag = 50
	_, issues = h.evalCapabilityLenses(t0.Add(20 * time.Second))
	if len(issues) != 0 {
		t.Fatalf("lag at the clear threshold must drop the issue, got %v", issues)
	}
}

func TestEvalCapabilityLenses_LagStateResetsOnPause(t *testing.T) {
	// A lens that pauses mid-streak must not carry the streak into a later active
	// cycle: the paused error dominates, and lag debounce restarts on resume.
	state := "active"
	lag := uint64(250)
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{snap("capabilityRoles", state, "structural", lag)}
	}}
	t0 := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)

	// Two active over-threshold cycles (one short of the default raise).
	h.evalCapabilityLenses(t0)
	h.evalCapabilityLenses(t0.Add(10 * time.Second))
	// Pause: the paused error is what surfaces, and the lag streak is wiped.
	state = "paused"
	_, issues := h.evalCapabilityLenses(t0.Add(20 * time.Second))
	if _, ok := issueByCode(issues, issueCapabilityLensPaused); !ok {
		t.Fatalf("paused lens must raise the paused error, got %v", issues)
	}
	// Resume active, still over threshold: must take a full fresh streak to raise,
	// so this first post-resume cycle is clean (proves the streak reset).
	state = "active"
	_, issues = h.evalCapabilityLenses(t0.Add(30 * time.Second))
	if len(issues) != 0 {
		t.Fatalf("resumed lens must restart the debounce (no carried streak), got %v", issues)
	}
}

func TestEvalCapabilityLenses_LagStatePrunesAbsentLens(t *testing.T) {
	lenses := []CapabilityLensStatus{snap("capabilityRoles", "active", "", 250)}
	h := &LatticeHeartbeater{
		CapabilityLensLagRaiseCycles: 1,
		CapabilityLensProvider:       func() []CapabilityLensStatus { return lenses },
	}
	h.evalCapabilityLenses(time.Now())
	if len(h.lagState) != 1 {
		t.Fatalf("lag state must track the live lens, got %v", h.lagState)
	}
	// The lens disappears from the snapshot: its debounce state is pruned.
	lenses = nil
	h.evalCapabilityLenses(time.Now())
	if len(h.lagState) != 0 {
		t.Fatalf("absent lens must be pruned from lag state, got %v", h.lagState)
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
