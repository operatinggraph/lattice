package leasesigning

import (
	"testing"
	"time"
)

// TestRenewalWindowHours_MatchesRenewalWindow pins the invariant
// renewal_window.go / renewal_window_short.go's doc comments both promise:
// renewalWindowHours (the bare int the renewal DDL script splices in to
// compute SetRenewalTerms' termMonths floor) MUST equal renewalWindow (the Go
// duration string spliced in for the actual date-math substitution) parsed to
// whole hours. A drift between the two constants would silently desync the
// termMonths floor from the real renewalOpensAt math this build compiles.
func TestRenewalWindowHours_MatchesRenewalWindow(t *testing.T) {
	d, err := time.ParseDuration(renewalWindow)
	if err != nil {
		t.Fatalf("renewalWindow %q does not parse as a Go duration: %v", renewalWindow, err)
	}
	gotHours := int(d.Hours())
	if gotHours != renewalWindowHours {
		t.Fatalf("renewalWindowHours = %d, but renewalWindow %q is %d hours — the two constants must stay in lockstep",
			renewalWindowHours, renewalWindow, gotHours)
	}
	// The conversion must be exact (no fractional hour lost to truncation) —
	// a sub-hour duration would silently truncate to 0 and defeat the floor.
	if d != time.Duration(renewalWindowHours)*time.Hour {
		t.Fatalf("renewalWindow %q is not a whole-hour duration (got %v); renewalWindowHours requires an exact hour count", renewalWindow, d)
	}
}
