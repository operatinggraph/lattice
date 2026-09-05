// The neighbour-retraction transport's health surface
// (secure-plain-lens-retraction-and-audit-design.md §4.4, §5): what carries a
// retraction when a NEIGHBOUR event drops one of a plain lens's rows, and the
// one reading that says the transport is declared and not carrying.
package health

import (
	"strings"
	"testing"
	"time"
)

func TestEvalLenses_RetractionTransportIsPublishedOnlyWhenOwed(t *testing.T) {
	// A lens whose rows no neighbour can drop owes no transport, and its
	// snapshot carries none. The field must then be ABSENT rather than empty:
	// an empty string on the wire reads as a missing transport, which is the
	// gap the whole mechanism exists to close.
	owes := lensAuditSnap("renewalsRead", 15*time.Minute)
	owes.RetractionTransport = "derivation"
	owed := lensAuditSnap("clinicPatients", 15*time.Minute)

	h := &LatticeHeartbeater{}
	metric, issues := beat(h, time.Now(), owes, owed)

	if got := metric["renewalsRead"]["retractionTransport"]; got != "derivation" {
		t.Fatalf("retractionTransport = %v, want derivation", got)
	}
	if _, present := metric["clinicPatients"]["retractionTransport"]; present {
		t.Fatalf("a lens owed no transport must publish no transport field: %v", metric["clinicPatients"])
	}
	if len(issues) != 0 {
		t.Fatalf("an armed transport raises nothing: %v", issues)
	}
}

func TestEvalLenses_DisarmedAuditRaisesTheTransportWarningOnce(t *testing.T) {
	// The deployment kill switch is one operator decision, so it is reported as
	// one issue listing what it voided — not as N per-lens alerts, which would
	// bury the decision under its own consequences.
	a := lensAuditSnap("renewalsRead", 15*time.Minute)
	a.RetractionTransport = RetractionTransportAuditDisarmed
	b := lensAuditSnap("visitSeriesRead", 15*time.Minute)
	b.RetractionTransport = RetractionTransportAuditDisarmed
	carrying := lensAuditSnap("leaseApplicationsRead", 15*time.Minute)
	carrying.RetractionTransport = "derivation"

	h := &LatticeHeartbeater{}
	metric, issues := beat(h, time.Now(), a, b, carrying)

	is, ok := issueByCode(issues, issueLensRetractionTransportDisarmed)
	if !ok {
		t.Fatalf("expected %s, got %v", issueLensRetractionTransportDisarmed, issues)
	}
	if is.Severity != "warning" {
		t.Fatalf("severity = %q; the read models stay readable and merely keep rows the graph no longer supports", is.Severity)
	}
	for _, want := range []string{"DISARMED", "renewalsRead", "visitSeriesRead"} {
		if !strings.Contains(is.Message, want) {
			t.Fatalf("message does not carry %q: %q", want, is.Message)
		}
	}
	if strings.Contains(is.Message, "leaseApplicationsRead") {
		t.Fatalf("a lens whose transport IS carrying must not be listed as voided: %q", is.Message)
	}
	// The condition is deployment-wide, so no lens's own alert moves — an
	// operator reading a per-lens alert would be reading N faults where there
	// is one setting.
	for _, name := range []string{"renewalsRead", "visitSeriesRead"} {
		if metric[name]["alert"] != "ok" {
			t.Fatalf("%s alert = %v; the kill switch is a deployment condition, not a per-lens fault", name, metric[name]["alert"])
		}
		if metric[name]["retractionTransport"] != RetractionTransportAuditDisarmed {
			t.Fatalf("%s must still publish the disarmed transport beside the issue: %v", name, metric[name])
		}
	}
}

func TestEvalLenses_RetractionTransportSurvivesAnUnreadableEntry(t *testing.T) {
	// The transport is derived from the in-process pipeline, not from the
	// health entry, so a fault observing the entry says nothing about it — the
	// same rule the sweep and audit verdicts above it follow.
	snap := lensAuditSnap("renewalsRead", 15*time.Minute)
	snap.RetractionTransport = RetractionTransportAuditDisarmed
	snap.Unreadable = "lens health entry: boom"

	h := &LatticeHeartbeater{}
	metric, issues := beat(h, time.Now(), snap)

	if metric["renewalsRead"]["retractionTransport"] != RetractionTransportAuditDisarmed {
		t.Fatalf("the transport must survive an unreadable entry: %v", metric["renewalsRead"])
	}
	if _, ok := issueByCode(issues, issueLensRetractionTransportDisarmed); !ok {
		t.Fatalf("expected %s beside the unreadable issue, got %v", issueLensRetractionTransportDisarmed, issues)
	}
}

func TestEvalLenses_ARunningLensWithNoTransportIsAnError(t *testing.T) {
	// The backstop for a lens the activation gate never reached. It is a
	// per-lens ERROR, not the deployment-wide warning above: the cause is this
	// lens's own shape, the remedy is this lens's own cypher or declaration, and
	// unlike a divergence nothing downstream will ever name the rows it keeps.
	gap := lensAuditSnap("leaseApplicationsRead", 15*time.Minute)
	gap.RetractionTransport = RetractionTransportNone
	unknown := lensAuditSnap("visitSeriesRead", 15*time.Minute)
	unknown.RetractionTransport = RetractionTransportUnclassified
	carrying := lensAuditSnap("renewalsRead", 15*time.Minute)
	carrying.RetractionTransport = "derivation"

	h := &LatticeHeartbeater{}
	metric, issues := beat(h, time.Now(), gap, unknown, carrying)

	is, ok := issueByCode(issues, issueLensRetractionTransportMissing)
	if !ok {
		t.Fatalf("expected %s, got %v", issueLensRetractionTransportMissing, issues)
	}
	if is.Severity != "error" {
		t.Fatalf("severity = %q; a lens running past the gate that should have refused it is not a degradation", is.Severity)
	}
	for _, want := range []string{"leaseApplicationsRead", "visitSeriesRead"} {
		if !strings.Contains(is.Message, want) {
			t.Fatalf("message does not carry %q: %q", want, is.Message)
		}
	}
	if strings.Contains(is.Message, "renewalsRead") {
		t.Fatalf("a lens whose transport IS carrying must not be listed: %q", is.Message)
	}
	for _, name := range []string{"leaseApplicationsRead", "visitSeriesRead"} {
		if metric[name]["alert"] != "retraction-transport-missing" {
			t.Fatalf("%s alert = %v; the gap is this lens's own fault and belongs on its own alert", name, metric[name]["alert"])
		}
	}
	if metric["leaseApplicationsRead"]["retractionTransport"] != RetractionTransportNone {
		t.Fatalf("the value must travel beside the alert: %v", metric["leaseApplicationsRead"])
	}
	if metric["visitSeriesRead"]["retractionTransport"] != RetractionTransportUnclassified {
		t.Fatalf("the value must travel beside the alert: %v", metric["visitSeriesRead"])
	}
	// The positive vector: a carrying transport raises nothing and keeps its
	// own alert, or a green result above would be an evaluator that alarms on
	// every lens that publishes the field at all.
	if metric["renewalsRead"]["alert"] != "ok" {
		t.Fatalf("a carrying transport must not alert: %v", metric["renewalsRead"])
	}
}

func TestEvalLenses_TheTransportGapSurvivesAnUnreadableEntry(t *testing.T) {
	// The verdict comes from the in-process pipeline, not from the health
	// entry, so a fault observing the entry says nothing about it — and this is
	// the one verdict that says the lens should not be running at all.
	snap := lensAuditSnap("leaseApplicationsRead", 15*time.Minute)
	snap.RetractionTransport = RetractionTransportNone
	snap.Unreadable = "lens health entry: boom"

	h := &LatticeHeartbeater{}
	metric, issues := beat(h, time.Now(), snap)

	if metric["leaseApplicationsRead"]["alert"] != "retraction-transport-missing" {
		t.Fatalf("the gap must outrank unreadable: %v", metric["leaseApplicationsRead"])
	}
	if _, ok := issueByCode(issues, issueLensRetractionTransportMissing); !ok {
		t.Fatalf("expected %s beside the unreadable issue, got %v", issueLensRetractionTransportMissing, issues)
	}
}
