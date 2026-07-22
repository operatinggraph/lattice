package weaver

import (
	"context"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/guardgrammar"
)

func TestTrailingAlternation(t *testing.T) {
	t.Parallel()
	events := func(ids ...string) []oscillationEvent {
		out := make([]oscillationEvent, len(ids))
		for i, id := range ids {
			out[i] = oscillationEvent{targetID: id}
		}
		return out
	}
	cases := []struct {
		name   string
		ring   []oscillationEvent
		count  int
		wantA  string
		wantB  string
		wantOK bool
	}{
		{"tooShort", events("a", "b", "a"), 4, "", "", false},
		{"cleanAlternationABAB", events("a", "b", "a", "b"), 4, "a", "b", true},
		{"cleanAlternationBABA", events("b", "a", "b", "a"), 4, "b", "a", true},
		{"repeatBreaksAlternation", events("a", "a", "b", "a"), 4, "", "", false},
		{"thirdTargetInWindowBreaksIt", events("a", "b", "c", "b"), 4, "", "", false},
		{"longerRingUsesOnlyTrailingWindow", events("x", "a", "b", "a", "b"), 4, "a", "b", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, b, ok := trailingAlternation(tc.ring, tc.count)
			if ok != tc.wantOK || a != tc.wantA || b != tc.wantB {
				t.Fatalf("trailingAlternation(%v, %d) = (%q, %q, %v), want (%q, %q, %v)",
					tc.ring, tc.count, a, b, ok, tc.wantA, tc.wantB, tc.wantOK)
			}
		})
	}
}

// TestOscillationStats_RecordDetectsAlternationOnce proves record() stays
// silent through a single back-and-forth (ordinary sequential remediation by
// two owners), fires exactly once a second round-trip completes the
// oscillationMinAlternations window, and clears the path's ring afterward so
// the SAME fight is never reported twice in a row.
func TestOscillationStats_RecordDetectsAlternationOnce(t *testing.T) {
	t.Parallel()
	o := newOscillationStats()
	path := guardgrammar.Path{Aspect: "foo", Field: "x"}
	now := time.Now()

	sequence := []string{"targetA", "targetB", "targetA"}
	for _, id := range sequence {
		if _, _, ok := o.record(path, id, now); ok {
			t.Fatalf("record(%q) fired before the alternation window filled", id)
		}
	}
	a, b, ok := o.record(path, "targetB", now)
	if !ok || a != "targetA" || b != "targetB" {
		t.Fatalf("record(targetB) = (%q, %q, %v), want (targetA, targetB, true)", a, b, ok)
	}

	// The ring was cleared on detection: the same two events replayed must not
	// immediately re-fire (they'd only form a 2-event tail, below the
	// window).
	if _, _, ok := o.record(path, "targetA", now); ok {
		t.Fatalf("record fired again immediately after a fresh clear")
	}
}

// TestEngine_BumpOscillation_FightingTargetsFrozen is the Fire 7 acceptance
// fixture (design weaver-planner-mandate-design.md §3.4, decomposition table
// row 7: "fighting-targets fixture frozen + one causal-pair issue"). Two
// targets whose declared-effects ops alternately assert the SAME aspect path
// are, after the confirmed alternation window, both disabled via the
// existing `__control` seam and named in one TargetOscillation Health issue —
// never a new dispatch.
func TestEngine_BumpOscillation_FightingTargetsFrozen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newControlHarness(t, ctx)

	opA, opB := testNanoID(t), testNanoID(t)
	h.engine.source.handle(opMetaVertexEvent(t, opA, "OpA"))
	h.engine.source.handle(opEffectsAspectEvent(t, opA, []string{`{"present":"subject.foo.data.x"}`}))
	h.engine.source.handle(opMetaVertexEvent(t, opB, "OpB"))
	h.engine.source.handle(opEffectsAspectEvent(t, opB, []string{`{"present":"subject.foo.data.x"}`}))

	h.seedTarget(&Target{TargetID: "targetA", LensRef: "lensA", Gaps: map[string]GapAction{}})
	h.seedTarget(&Target{TargetID: "targetB", LensRef: "lensB", Gaps: map[string]GapAction{}})

	for _, round := range []struct{ targetID, op string }{
		{"targetA", "OpA"}, {"targetB", "OpB"}, {"targetA", "OpA"}, {"targetB", "OpB"},
	} {
		h.engine.bumpOscillation(ctx, round.targetID, round.op)
	}

	if !h.engine.isTargetDisabled("targetA") {
		t.Fatalf("targetA must be frozen (disabled) after the confirmed fight")
	}
	if !h.engine.isTargetDisabled("targetB") {
		t.Fatalf("targetB must be frozen (disabled) after the confirmed fight")
	}

	var found *healthIssue
	for _, is := range h.engine.issues.snapshot() {
		if is.Code == "TargetOscillation" {
			cp := is
			found = &cp
			break
		}
	}
	if found == nil {
		t.Fatalf("expected one TargetOscillation issue, issues = %+v", h.engine.issues.snapshot())
	}
	if found.Severity != "error" {
		t.Fatalf("TargetOscillation severity = %q, want error", found.Severity)
	}
}
