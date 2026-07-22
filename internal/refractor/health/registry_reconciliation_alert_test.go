// Registry-reconciliation issue evaluation
// (refractor-lens-registry-restart-integrity-design.md §4 Fire B step 2).
// Pure unit tests over the unexported evaluation path — no NATS. Mirrors
// lens_alert_test.go / caplens_alert_test.go.
package health

import (
	"strings"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

func TestEvalRegistryReconciliation_NilProvider(t *testing.T) {
	h := &LatticeHeartbeater{}
	issues := h.evalRegistryReconciliation(time.Now())
	if issues != nil {
		t.Fatalf("nil provider must yield nil, got %v", issues)
	}
}

func TestEvalRegistryReconciliation_EmptySnapshotNoIssue(t *testing.T) {
	h := &LatticeHeartbeater{RegistryReconciliationProvider: func() []string { return nil }}
	issues := h.evalRegistryReconciliation(time.Now())
	if len(issues) != 0 {
		t.Fatalf("empty snapshot must raise no issue, got %v", issues)
	}
}

func TestEvalRegistryReconciliation_MissingRaisesError(t *testing.T) {
	h := &LatticeHeartbeater{RegistryReconciliationProvider: func() []string {
		return []string{"AbCdEfGhJkMnPqRsTuVw"}
	}}
	issues := h.evalRegistryReconciliation(time.Now())
	is, ok := issueByCode(issues, issueLensRegistryIncomplete)
	if !ok {
		t.Fatalf("expected %s issue, got %v", issueLensRegistryIncomplete, issues)
	}
	if is.Severity != "error" {
		t.Fatalf("severity = %q, want error (an incomplete registry is a real outage, not just degraded)", is.Severity)
	}
	if !strings.Contains(is.Message, "AbCdEfGhJkMnPqRsTuVw") {
		t.Fatalf("message missing the missing lens id: %q", is.Message)
	}
}

func TestEvalRegistryReconciliation_MessageCapsLongList(t *testing.T) {
	missing := make([]string, 15)
	for i := range missing {
		missing[i] = "lens" + string(rune('a'+i))
	}
	h := &LatticeHeartbeater{RegistryReconciliationProvider: func() []string { return missing }}
	issues := h.evalRegistryReconciliation(time.Now())
	is, ok := issueByCode(issues, issueLensRegistryIncomplete)
	if !ok {
		t.Fatalf("expected issue, got %v", issues)
	}
	if !strings.Contains(is.Message, "15 lens") {
		t.Fatalf("message must state the true count even when the list is capped: %q", is.Message)
	}
	if !strings.Contains(is.Message, "...") {
		t.Fatalf("message must indicate truncation for a long list: %q", is.Message)
	}
}

func TestEvalRegistryReconciliation_SincePersistsThenResolves(t *testing.T) {
	missing := []string{"AbCdEfGhJkMnPqRsTuVw"}
	h := &LatticeHeartbeater{RegistryReconciliationProvider: func() []string { return missing }}

	t0 := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	issues := h.evalRegistryReconciliation(t0)
	is0, ok := issueByCode(issues, issueLensRegistryIncomplete)
	if !ok {
		t.Fatal("first cycle must open the issue")
	}
	wantSince := substrate.FormatTimestamp(t0)
	if is0.Since != wantSince {
		t.Fatalf("since = %q, want %q", is0.Since, wantSince)
	}

	issues = h.evalRegistryReconciliation(t0.Add(10 * time.Minute))
	is1, _ := issueByCode(issues, issueLensRegistryIncomplete)
	if is1.Since != wantSince {
		t.Fatalf("since advanced across cycles: %q != %q", is1.Since, wantSince)
	}

	missing = nil
	issues = h.evalRegistryReconciliation(t0.Add(20 * time.Minute))
	if len(issues) != 0 {
		t.Fatalf("resolved issue must be dropped, got %v", issues)
	}
	if h.openRegistryIssueSince != "" {
		t.Fatalf("openRegistryIssueSince must clear on resolution, got %q", h.openRegistryIssueSince)
	}

	// Re-opening after resolution must mint a FRESH since, not resurrect the old one.
	missing = []string{"AbCdEfGhJkMnPqRsTuVw"}
	t1 := t0.Add(30 * time.Minute)
	issues = h.evalRegistryReconciliation(t1)
	is2, _ := issueByCode(issues, issueLensRegistryIncomplete)
	if is2.Since != substrate.FormatTimestamp(t1) {
		t.Fatalf("re-opened issue since = %q, want a fresh %q", is2.Since, substrate.FormatTimestamp(t1))
	}
}

// TestEvalRegistryReconciliation_IndependentFromLensIssues proves the
// registry-reconciliation issue's own since-state doesn't share (and so
// can't be cross-cleared by) evalLenses' openLensIssues map — the same
// independence guarantee TestEvalLenses_DoesNotDoubleIssueAuthPlane proves
// between the cap and general lens paths.
func TestEvalRegistryReconciliation_IndependentFromLensIssues(t *testing.T) {
	h := &LatticeHeartbeater{
		LensLagRaiseCycles: 1,
		LensProvider: func() []LensLivenessStatus {
			return []LensLivenessStatus{lensSnap("clinicAppointments", "paused", "manual", 0)}
		},
		RegistryReconciliationProvider: func() []string { return []string{"AbCdEfGhJkMnPqRsTuVw"} },
	}
	now := time.Now()
	_, lensIssues := h.evalLenses(now)
	registryIssues := h.evalRegistryReconciliation(now)

	if _, ok := issueByCode(lensIssues, issueLensProjectionPaused); !ok {
		t.Fatalf("lens path must still raise its own issue, got %v", lensIssues)
	}
	if _, ok := issueByCode(registryIssues, issueLensRegistryIncomplete); !ok {
		t.Fatalf("registry path must raise its own issue, got %v", registryIssues)
	}

	// A second lens-only cycle must not touch the registry issue's since.
	h.evalLenses(now.Add(10 * time.Second))
	if h.openRegistryIssueSince == "" {
		t.Fatal("lens-path eval must not clear the registry-reconciliation issue")
	}
}
