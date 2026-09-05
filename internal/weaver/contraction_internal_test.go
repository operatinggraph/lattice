package weaver

import "testing"

func TestClassifyTrajectory(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ring []int
		want string
	}{
		{"tooShort", []int{5}, trajectorySteady},
		{"strictlyShrinking", []int{10, 8, 6, 4, 2}, trajectoryShrinking},
		{"strictlyDiverging", []int{0, 2, 4, 6, 8}, trajectoryDiverging},
		{"flat", []int{5, 5, 5, 5}, trajectorySteady},
		{"reversesMidWindow", []int{5, 2, 5, 2}, trajectorySteady},
		{"plateauThenDrop", []int{5, 5, 5, 2}, trajectoryShrinking},
		{"plateauThenRise", []int{2, 2, 2, 5}, trajectoryDiverging},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyTrajectory(tc.ring); got != tc.want {
				t.Fatalf("classifyTrajectory(%v) = %q, want %q", tc.ring, got, tc.want)
			}
		})
	}
}

// TestContractionStats_ObserveTracksTransitionsOnly proves the current
// per-target count only moves on a TRUE state transition — a repeat
// delivery of the same violating value (the common CDC-redelivery case) must
// not double-count, and a row never before seen non-violating must not enter
// `known` (bounded memory: only currently-violating rows are tracked).
func TestContractionStats_ObserveTracksTransitionsOnly(t *testing.T) {
	t.Parallel()
	c := newContractionStats()

	c.observe("t1", "e1", false) // first sighting, non-violating: no-op
	if got := c.current["t1"]; got != 0 {
		t.Fatalf("current[t1] = %d, want 0 after a non-violating first sighting", got)
	}

	c.observe("t1", "e1", true) // becomes violating
	c.observe("t1", "e2", true) // a second violating row
	if got := c.current["t1"]; got != 2 {
		t.Fatalf("current[t1] = %d, want 2", got)
	}

	c.observe("t1", "e1", true) // redelivery of the same state: no-op
	if got := c.current["t1"]; got != 2 {
		t.Fatalf("current[t1] = %d, want 2 after a redelivered no-change", got)
	}

	c.observe("t1", "e1", false) // gap closes
	if got := c.current["t1"]; got != 1 {
		t.Fatalf("current[t1] = %d, want 1 after e1 closes", got)
	}
}

// TestContractionStats_SampleAndSnapshot proves the sweep-cadence sample
// ring feeds the heartbeat's classification end to end: a target whose
// violating count is driven down across successive samples reports
// "shrinking".
func TestContractionStats_SampleAndSnapshot(t *testing.T) {
	t.Parallel()
	c := newContractionStats()
	counts := []int{4, 3, 2, 1, 0}
	for _, n := range counts {
		c.current["t1"] = n
		c.sample([]string{"t1"})
	}
	snap := c.snapshot()
	if got := snap["t1"]; got != trajectoryShrinking {
		t.Fatalf("snapshot()[t1] = %q, want %q", got, trajectoryShrinking)
	}
	if got := len(c.samples["t1"]); got != contractionWindowSize {
		t.Fatalf("ring length = %d, want it capped at %d (5 samples pushed)", got, contractionWindowSize)
	}
}

// discardReflection is the reflect callback for a test that seeds or drives
// surfaceStats directly and asserts on the SET rather than on the Health entry
// the set writes.
func discardReflection(surfaceReflection) {}

// TestSurfaceStats_AddIsATransition pins the rule the count rests on: only a
// TRANSITION writes. A CDC redelivery of a row already in the set under the same
// issue identity must report no change AND invoke no reflect callback — the two
// are separate observables, and a reflect fired on an unchanged set would rewrite
// the entry on every redelivery, which is the write the whole design exists to
// avoid on a steady backlog.
func TestSurfaceStats_AddIsATransition(t *testing.T) {
	t.Parallel()
	s := newSurfaceStats()
	reflects := 0
	count := func(surfaceReflection) { reflects++ }

	if changed := s.add("t1", "missing_claim", "e1", "UnroutedTasks", "warning", count); !changed {
		t.Fatal("the first add of a member must report a change")
	}
	if reflects != 1 {
		t.Fatalf("reflect invocations after the first add = %d, want 1", reflects)
	}
	if changed := s.add("t1", "missing_claim", "e1", "UnroutedTasks", "warning", count); changed {
		t.Fatal("a second add of the same member under the same code/severity must report no change")
	}
	if reflects != 1 {
		t.Fatalf("reflect invocations after the redelivery = %d, want 1: an unchanged set must not "+
			"rewrite the entry", reflects)
	}
	if n := s.count("t1", "missing_claim"); n != 1 {
		t.Fatalf("count = %d, want 1 — a set, not a tally", n)
	}
}

// TestSurfaceStats_AddCarriesAReauthoredIdentity is the other half of the
// transition rule, and it is not symmetric with the one above. The entry's
// identity — the package's declared issueCode and issueSeverity — is recorded at
// the add, so a package re-author reaches the wire only through an add. If
// membership alone decided the change, a target whose backlog is steady (every
// open row already in the set) would go on publishing the OLD code and severity
// for as long as no row opened or closed, which for a parked backlog is forever.
func TestSurfaceStats_AddCarriesAReauthoredIdentity(t *testing.T) {
	t.Parallel()
	s := newSurfaceStats()
	var last surfaceReflection
	capture := func(r surfaceReflection) { last = r }

	s.add("t1", "missing_claim", "e1", "UnroutedTasks", "warning", capture)
	s.add("t1", "missing_claim", "e2", "UnroutedTasks", "warning", capture)

	if changed := s.add("t1", "missing_claim", "e1", "UnroutedTasks", "error", capture); !changed {
		t.Fatal("a redelivery under a re-authored severity must report a change")
	}
	if last.severity != "error" || last.count != 2 {
		t.Fatalf("reflection = %+v, want the re-authored severity at the unchanged count of 2", last)
	}
	if changed := s.add("t1", "missing_claim", "e1", "StaleClaims", "error", capture); !changed {
		t.Fatal("a redelivery under a re-authored code must report a change")
	}
	if last.code != "StaleClaims" || last.count != 2 {
		t.Fatalf("reflection = %+v, want the re-authored code at the unchanged count of 2", last)
	}
}
