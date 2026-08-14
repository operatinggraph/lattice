// The plain-lens divergence audit's health surface
// (lens-projection-divergence-audit-design.md §4.3). Sibling of
// lens_sweep_test.go, and deliberately asserting the two things that separate
// this detector from that one: it never repairs, and a lens it REFUSED must
// never read like a lens it audited and found clean.
package health

import (
	"strings"
	"testing"
	"time"
)

// lensAuditSnap is an active business lens carrying an enrolled audit.
func lensAuditSnap(name string, interval time.Duration) LensLivenessStatus {
	return LensLivenessStatus{
		CanonicalName:      name,
		RuleID:             "lns-" + name,
		Status:             "active",
		AuditEnrolled:      true,
		AuditInterval:      interval,
		AuditCoverageBasis: "key-type",
	}
}

// auditedThenHeld drives the temporal sequence a stall needs: a first beat where
// the lens has just audited — which is what establishes the staleness baseline,
// since the auditor is in-process and a lens the heartbeater has only just seen
// cannot already have an old verdict — then a beat `held` later with nothing
// since.
func auditedThenHeld(h *LatticeHeartbeater, start time.Time, held time.Duration, snaps ...LensLivenessStatus) (map[string]map[string]any, []issueRecord) {
	fresh := make([]LensLivenessStatus, len(snaps))
	for i, s := range snaps {
		s.AuditLastPassAt = start
		s.AuditSuppression, s.AuditSuppressionAt = "", time.Time{}
		fresh[i] = s
	}
	beat(h, start, fresh...)
	return beat(h, start.Add(held), snaps...)
}

func TestEvalLenses_DivergenceRaisesAndNamesTheClasses(t *testing.T) {
	snap := lensAuditSnap("listedUnits", 15*time.Minute)
	snap.Audited = 10
	snap.DivergentRows = map[string]int{"stale": 2, "retained": 1}
	snap.DivergentTotal = 3
	h := &LatticeHeartbeater{}
	metric, issues := beat(h, time.Now(), snap)

	is, ok := issueByCode(issues, issueLensProjectionDiverged)
	if !ok {
		t.Fatalf("expected %s, got %v", issueLensProjectionDiverged, issues)
	}
	if is.Severity != "warning" {
		t.Fatalf("severity = %q; a business lens's wrong read model degrades the instance, it never fails it", is.Severity)
	}
	for _, want := range []string{"listedUnits", "stale=2", "retained=1"} {
		if !strings.Contains(is.Message, want) {
			t.Fatalf("message does not carry %q: %q", want, is.Message)
		}
	}
	// The message has to say that nothing was repaired. An operator who reads
	// this as the sweep's divergence issue will wait for a heal that is never
	// coming.
	if !strings.Contains(is.Message, "NOTHING has been repaired") {
		t.Fatalf("message must state that the audit does not repair: %q", is.Message)
	}
	m := metric["listedUnits"]
	if m["alert"] != "diverged" {
		t.Fatalf("alert = %v, want diverged", m["alert"])
	}
	if m["divergentTotal"] != 3 {
		t.Fatalf("divergentTotal = %v, want 3", m["divergentTotal"])
	}
	if got, ok := m["divergentRows"].(map[string]int); !ok || got["missing"] != 0 || len(got) != 2 {
		t.Fatalf("divergentRows = %v; a class that never fired must be ABSENT, not zero", m["divergentRows"])
	}
}

func TestEvalLenses_AuditFieldsArePublishedEvenWhenClean(t *testing.T) {
	// "Audited, clean" is a verdict, and an absent field is indistinguishable
	// from a Refractor that computes no verdict at all — the exact ambiguity
	// this design exists to remove.
	now := time.Now()
	snap := lensAuditSnap("listedUnits", 15*time.Minute)
	snap.Audited = 10
	snap.AuditListingSize = 240
	snap.AuditLastPassAt = now
	snap.AuditCycleCompletedAt = now.Add(-time.Hour)
	h := &LatticeHeartbeater{}
	metric, issues := beat(h, now, snap)

	m := metric["listedUnits"]
	for _, field := range []string{
		"auditEnrolled", "audited", "divergentRows", "divergentTotal", "auditUnverified",
		"auditLastPassAt", "auditCycleCompletedAt", "auditCoverageBasis", "auditListingSize", "auditSuppression",
	} {
		if _, present := m[field]; !present {
			t.Fatalf("metric field %q is absent for a clean audited lens: %v", field, m)
		}
	}
	if m["auditCoverageBasis"] != "key-type" {
		t.Fatalf("auditCoverageBasis = %v; the coverage bound must be published, not assumed away", m["auditCoverageBasis"])
	}
	if m["alert"] != "ok" {
		t.Fatalf("alert = %v, want ok", m["alert"])
	}
	if len(issues) != 0 {
		t.Fatalf("a clean audit raises nothing: %v", issues)
	}
	// The audit's unverified count must NOT land on the sweep's key: they are
	// two detectors' verdicts about different anchors.
	if m["auditUnverified"] == nil {
		t.Fatalf("the audit's unverified count must have its own key")
	}
}

func TestEvalLenses_RefusedAuditPublishesTheReasonAndRaisesNothing(t *testing.T) {
	// The whole point of the enrolment gate, carried through to the alert plane:
	// "not audited" must be distinguishable from "audited, clean", and a lens
	// with no cadence can never read as late.
	snap := LensLivenessStatus{
		CanonicalName: "grantTable",
		RuleID:        "lns-grantTable",
		Status:        "active",
		AuditEnrolled: false,
		AuditRefusal:  "its target adapter cannot read a row back",
	}
	h := &LatticeHeartbeater{}
	metric, issues := auditedThenHeld(h, time.Now(), 400*time.Hour, snap)

	m := metric["grantTable"]
	if m["auditEnrolled"] != false {
		t.Fatalf("auditEnrolled = %v, want false", m["auditEnrolled"])
	}
	if m["auditRefusal"] != "its target adapter cannot read a row back" {
		t.Fatalf("auditRefusal = %v", m["auditRefusal"])
	}
	if _, present := m["divergentTotal"]; present {
		t.Fatalf("a refused lens must publish no verdict: %v", m)
	}
	if _, ok := issueByCode(issues, issueLensAuditStalled); ok {
		t.Fatalf("a refused lens has no cadence to be late against: %v", issues)
	}
	if m["alert"] != "ok" {
		t.Fatalf("alert = %v; a refusal is not an alert about this lens's rows", m["alert"])
	}
}

func TestEvalLenses_AuditStallRaisesWhenTheDetectorStopsTicking(t *testing.T) {
	interval := 15 * time.Minute
	snap := lensAuditSnap("listedUnits", interval)
	h := &LatticeHeartbeater{}
	start := time.Now()
	held := time.Duration(defaultCapabilitySweepStallCycles+1) * interval

	metric, issues := auditedThenHeld(h, start, held, snap)
	is, ok := issueByCode(issues, issueLensAuditStalled)
	if !ok {
		t.Fatalf("expected %s, got %v", issueLensAuditStalled, issues)
	}
	if is.Severity != "warning" {
		t.Fatalf("severity = %q, want warning on the business path", is.Severity)
	}
	if !strings.Contains(is.Message, "not ticking") {
		t.Fatalf("an unexplained stall must say the audit is not ticking: %q", is.Message)
	}
	if metric["listedUnits"]["alert"] != "audit-stalled" {
		t.Fatalf("alert = %v, want audit-stalled", metric["listedUnits"]["alert"])
	}
}

func TestEvalLenses_AuditStallNamesAFreshSuppressionReason(t *testing.T) {
	interval := 15 * time.Minute
	now := time.Now()
	snap := lensAuditSnap("listedUnits", interval)
	snap.AuditSuppression = "rebuild in flight"
	snap.AuditSuppressionAt = now.Add(time.Duration(defaultCapabilitySweepStallCycles+1) * interval)
	h := &LatticeHeartbeater{}
	_, issues := auditedThenHeld(h, now, time.Duration(defaultCapabilitySweepStallCycles+1)*interval, snap)

	is, ok := issueByCode(issues, issueLensAuditStalled)
	if !ok {
		t.Fatalf("expected %s, got %v", issueLensAuditStalled, issues)
	}
	if !strings.Contains(is.Message, "rebuild in flight") {
		t.Fatalf("a fresh suppression reason must be named: %q", is.Message)
	}
}

func TestEvalLenses_AuditStallDoesNotFireWhilePaused(t *testing.T) {
	// A paused lens suppresses the audit deliberately and indefinitely, and the
	// pause is already an issue of its own. Rebasing also stops a resumed lens
	// starting out stalled for the length of its pause.
	interval := 15 * time.Minute
	snap := lensAuditSnap("listedUnits", interval)
	snap.Status = StatusPaused
	h := &LatticeHeartbeater{}
	_, issues := auditedThenHeld(h, time.Now(), 400*interval, snap)
	if _, ok := issueByCode(issues, issueLensAuditStalled); ok {
		t.Fatalf("a paused lens must not read as a stalled audit: %v", issues)
	}
}

func TestEvalLenses_AuditVerdictsSurviveAnUnreadableEntry(t *testing.T) {
	// The audit writes nothing, so a divergence lost to a fault observing
	// something else is one nothing else will find again.
	snap := lensAuditSnap("listedUnits", 15*time.Minute)
	snap.Unreadable = "consumer pending count: connection closed"
	snap.Status = "unknown"
	snap.DivergentTotal = 2
	snap.DivergentRows = map[string]int{"missing": 2}
	h := &LatticeHeartbeater{}
	metric, issues := beat(h, time.Now(), snap)

	if _, ok := issueByCode(issues, issueLensProjectionDiverged); !ok {
		t.Fatalf("expected %s to survive an unreadable entry, got %v", issueLensProjectionDiverged, issues)
	}
	if metric["listedUnits"]["divergentTotal"] != 2 {
		t.Fatalf("the audit's verdict must be published across an unreadable cycle: %v", metric["listedUnits"])
	}
	// `unreadable` outranks `diverged`: the quieter value is a claim made on
	// evidence this cycle could not gather in full.
	if metric["listedUnits"]["alert"] != "unreadable" {
		t.Fatalf("alert = %v, want unreadable", metric["listedUnits"]["alert"])
	}
}

func TestEvalLenses_AuditUnverifiedRaisesTheAlertWithoutTheSweepsIssue(t *testing.T) {
	// The sweep owns LensAuditUnverified. One code written by two independent
	// detectors would be two `since` clocks disagreeing about when the condition
	// began, so the audit's unverified anchors surface through the single-valued
	// `alert` field and their own metric keys instead.
	snap := lensAuditSnap("listedUnits", 15*time.Minute)
	snap.AuditUnverified = 2
	snap.AuditLastUnverified = "tombstoned anchor whose row key is not derivable"
	h := &LatticeHeartbeater{}
	metric, issues := beat(h, time.Now(), snap)

	if _, ok := issueByCode(issues, issueLensAuditUnverified); ok {
		t.Fatalf("the audit must not raise the sweep's code: %v", issues)
	}
	m := metric["listedUnits"]
	if m["alert"] != "unverified" {
		t.Fatalf("alert = %v, want unverified", m["alert"])
	}
	if m["auditUnverified"] != 2 {
		t.Fatalf("auditUnverified = %v, want 2", m["auditUnverified"])
	}
	if m["auditUnverifiedReason"] != "tombstoned anchor whose row key is not derivable" {
		t.Fatalf("auditUnverifiedReason = %v", m["auditUnverifiedReason"])
	}
}
