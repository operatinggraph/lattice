package health

import "testing"

// recordLagProgress mirrors Pipeline.recordRebuildProgress: stamp at first
// observation, re-stamp on any decrease, never on a plateau or an uptick.
// This pins the "still actively draining" clock that lets a reader
// distinguish a legitimate cold bring-up backlog (falling, however slowly)
// from a genuinely stuck one (flat).
func TestLagPoller_RecordLagProgress(t *testing.T) {
	lp := &LagPoller{}

	if !lp.lagProgressAt.IsZero() {
		t.Fatalf("lagProgressAt must start zero")
	}

	lp.recordLagProgress(2500)
	first := lp.lagProgressAt
	if first.IsZero() {
		t.Fatalf("first observation must stamp lagProgressAt")
	}
	if lp.lagOutstanding != 2500 {
		t.Fatalf("lagOutstanding = %d, want 2500", lp.lagOutstanding)
	}

	// A plateau (equal lag) must not advance the clock.
	lp.recordLagProgress(2500)
	if lp.lagProgressAt != first {
		t.Fatalf("plateau must not advance lagProgressAt")
	}

	// An uptick (a concurrent write growing the backlog) must not advance the
	// clock either — only a decrease counts as progress.
	lp.recordLagProgress(2600)
	if lp.lagProgressAt != first {
		t.Fatalf("an uptick must not advance lagProgressAt")
	}
	if lp.lagOutstanding != 2600 {
		t.Fatalf("lagOutstanding = %d, want 2600 (still tracks the latest observation)", lp.lagOutstanding)
	}

	// A genuine decrease must advance the clock.
	lp.recordLagProgress(2400)
	if !lp.lagProgressAt.After(first) {
		t.Fatalf("a decrease must advance lagProgressAt past %v, got %v", first, lp.lagProgressAt)
	}
	if lp.lagOutstanding != 2400 {
		t.Fatalf("lagOutstanding = %d, want 2400", lp.lagOutstanding)
	}

	// Draining to zero is still a decrease and must advance the clock.
	second := lp.lagProgressAt
	lp.recordLagProgress(0)
	if !lp.lagProgressAt.After(second) {
		t.Fatalf("draining to 0 must advance lagProgressAt past %v, got %v", second, lp.lagProgressAt)
	}
}
