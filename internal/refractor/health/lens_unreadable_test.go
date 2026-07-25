// The business-lens unreadable-liveness report (lens-projection-liveness-design.md
// §10). Pure unit tests over the unexported evaluation path — no NATS. Mirrors
// the CapabilityLensUnreadable half of caplens_alert_test.go on the
// general-lens side.
package health

import (
	"strings"
	"testing"
	"time"
)

func TestEvalLenses_UnreadableIsReportedNotDropped(t *testing.T) {
	h := &LatticeHeartbeater{LensProvider: func() []LensLivenessStatus {
		return []LensLivenessStatus{{
			CanonicalName: "clinicAppointments",
			RuleID:        "lns-1",
			Status:        "unknown",
			Unreadable:    "lens health entry: stream unavailable",
		}}
	}}
	metric, issues := h.evalLenses(time.Now())

	entry, ok := metric["clinicAppointments"]
	if !ok {
		t.Fatalf("an unreadable lens must keep its place in the metric map, got %v", metric)
	}
	// An explicit null, not a zero: "we could not read the lag" and "the lag is
	// 0" are opposite facts and must not render identically.
	if lag, present := entry["projectionLag"]; !present || lag != nil {
		t.Fatalf("projectionLag = %v, want an explicit nil", lag)
	}
	if got := entry["alert"]; got != "unreadable" {
		t.Fatalf("alert = %v, want unreadable", got)
	}
	is, ok := issueByCode(issues, issueLensProjectionUnreadable)
	if !ok {
		t.Fatalf("expected %s, got %v", issueLensProjectionUnreadable, issues)
	}
	if is.Severity != "warning" {
		t.Fatalf("severity = %q, want warning", is.Severity)
	}
	if !strings.Contains(is.Message, "stream unavailable") {
		t.Fatalf("message must carry the read error: %q", is.Message)
	}
}

// An unreadable cycle must not be scored as a lagging one either: it advances
// no streak, so a lag alert can only ever be raised by cycles that actually
// measured the lag. Matches the auth-plane path, which resets the same way.
func TestEvalLenses_UnreadableCycleAdvancesNoLagStreak(t *testing.T) {
	lag := uint64(500)
	unreadable := ""
	h := &LatticeHeartbeater{
		LensLagRaiseCycles: 2,
		LensProvider: func() []LensLivenessStatus {
			return []LensLivenessStatus{{
				CanonicalName: "clinicAppointments", RuleID: "lns-1",
				Status: "active", ProjectionLag: lag, Unreadable: unreadable,
			}}
		},
	}
	h.evalLenses(time.Now()) // cycle 1 of 2 — streak building, no issue yet
	unreadable = "consumer pending count: timeout"
	if _, issues := h.evalLenses(time.Now()); func() bool {
		_, raised := issueByCode(issues, issueLensProjectionLagging)
		return raised
	}() {
		t.Fatalf("an unreadable cycle must not complete a lag streak")
	}
	unreadable = ""
	h.evalLenses(time.Now()) // streak restarts at 1
	if _, issues := h.evalLenses(time.Now()); func() bool {
		_, raised := issueByCode(issues, issueLensProjectionLagging)
		return raised
	}() == false {
		t.Fatalf("expected %s once the streak completes on readable cycles", issueLensProjectionLagging)
	}
}
