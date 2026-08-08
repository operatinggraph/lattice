package health

// The reader for the SecureRedactions counter. Without these, the counter is a
// mute measurement: the pipeline writes it into the lens's health entry and
// nothing turns it into a signal an operator ever sees
// (retention-class-key-custody-design.md §6.2, fork F2).

import (
	"testing"
	"time"
)

func redactionSnap(name string, redactions uint64) LensLivenessStatus {
	return LensLivenessStatus{
		CanonicalName:    name,
		RuleID:           "lns-" + name,
		Status:           "active",
		SecureRedactions: redactions,
	}
}

// A lens that is active, unpaused and fully caught up — every liveness signal
// green — while serving rows whose secure columns it could not resolve. This is
// the exact state no other field in the snapshot can express, and the reason the
// counter exists.
func TestEvalLenses_SecureRedactionIsRaisedOnAnOtherwiseHealthyLens(t *testing.T) {
	h := &LatticeHeartbeater{}
	metric, issues := beat(h, time.Now(), redactionSnap("clinicEncountersRead", 3))

	is, ok := issueByCode(issues, issueLensSecureRedaction)
	if !ok {
		t.Fatalf("an unresolved secure column must raise an issue; got %+v", issues)
	}
	if is.Severity != "error" {
		t.Fatalf("a confidently-wrong read model is an error, not a warning; got %q", is.Severity)
	}
	if got := metric["clinicEncountersRead"]["alert"]; got != "secure-redaction" {
		t.Fatalf("the lens's own alert must reflect it; got %v", got)
	}
	if got := metric["clinicEncountersRead"]["secureRedactions"]; got != uint64(3) {
		t.Fatalf("the count must be carried in the metric, not just the message; got %v", got)
	}
}

// Zero is silent — which is the whole corpus today, and a legitimate shred
// projecting null never increments the counter. If a lawful erasure raised this,
// the signal would fire constantly and mean nothing.
func TestEvalLenses_NoRedactionsRaisesNothing(t *testing.T) {
	h := &LatticeHeartbeater{}
	metric, issues := beat(h, time.Now(), redactionSnap("clinicPatientsRead", 0))

	if _, ok := issueByCode(issues, issueLensSecureRedaction); ok {
		t.Fatalf("a lens with no redactions must raise nothing; got %+v", issues)
	}
	if got := metric["clinicPatientsRead"]["alert"]; got != "ok" {
		t.Fatalf("alert must stay ok; got %v", got)
	}
	if _, present := metric["clinicPatientsRead"]["secureRedactions"]; present {
		t.Fatal("a zero count must not be emitted, so the metric stays quiet for the whole current corpus")
	}
}

// The count is CUMULATIVE, so the issue must persist across cycles in which no
// new redaction happens. A delta-based signal would go quiet while the wrong
// rows were still being served — the null stays in the read model until whatever
// made the value unresolvable is fixed and the lens reprojects.
func TestEvalLenses_SecureRedactionPersistsAcrossQuietCycles(t *testing.T) {
	h := &LatticeHeartbeater{}
	start := time.Now()
	snap := redactionSnap("clinicEncountersRead", 2)

	beat(h, start, snap)
	_, issues := beat(h, start.Add(10*time.Minute), snap) // same count, no new failures

	if _, ok := issueByCode(issues, issueLensSecureRedaction); !ok {
		t.Fatalf("the issue must persist while the redacted rows are still being served; got %+v", issues)
	}
}
