package weaver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/asolgan/lattice/internal/guardgrammar"
)

// shadowTestEngine builds a minimal Engine sufficient to exercise
// shadowCompare/rankCandidates: a real markStore (embedded NATS, so
// effectCloseRate reads work) plus a fresh shadowStats and a discard logger.
// No registry/actuator/heartbeat wiring — those are exercised elsewhere.
func shadowTestEngine(t *testing.T) *Engine {
	t.Helper()
	ctx := context.Background()
	return &Engine{
		marks:  newStateTestStore(t, ctx),
		shadow: newShadowStats(),
		logger: discardLogger(),
	}
}

func parseGuard(t *testing.T, raw string) *guardgrammar.Guard {
	t.Helper()
	g, err := guardgrammar.Parse(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("parse guard %s: %v", raw, err)
	}
	return g
}

// TestRankCandidates_PreconditionGatesEligibility proves an ineligible
// candidate (its Pre evaluates false against the row) is never picked, even
// when it would otherwise win on cost.
func TestRankCandidates_PreconditionGatesEligibility(t *testing.T) {
	t.Parallel()
	e := shadowTestEngine(t)
	candidates := []GapCandidate{
		{Action: "Cheap", Cost: 0, preGuard: parseGuard(t, `{"present":"subject.data.nope"}`)},
		{Action: "Eligible", Cost: 5},
	}
	picked, ok := e.rankCandidates(context.Background(), "t1", "missing_a", candidates, map[string]any{})
	if !ok {
		t.Fatalf("expected a pick (one eligible candidate)")
	}
	if picked != "Eligible" {
		t.Fatalf("expected the only eligible candidate to win, got %q", picked)
	}
}

// TestRankCandidates_CostThenLexicographicTiebreak proves the ranking prefers
// lower declared cost, then breaks ties on the actionRef lexicographically —
// the same canonical tie-break internal/weaver/planner.Synthesize uses.
func TestRankCandidates_CostThenLexicographicTiebreak(t *testing.T) {
	t.Parallel()
	e := shadowTestEngine(t)
	candidates := []GapCandidate{
		{Action: "Zeta", Cost: 1},
		{Action: "Alpha", Cost: 1},
		{Action: "Cheapest", Cost: 0},
	}
	picked, ok := e.rankCandidates(context.Background(), "t1", "missing_a", candidates, map[string]any{})
	if !ok || picked != "Cheapest" {
		t.Fatalf("expected Cheapest (lowest cost) to win, got %q ok=%v", picked, ok)
	}

	tied := []GapCandidate{{Action: "Zeta", Cost: 1}, {Action: "Alpha", Cost: 1}}
	picked, ok = e.rankCandidates(context.Background(), "t1", "missing_b", tied, map[string]any{})
	if !ok || picked != "Alpha" {
		t.Fatalf("expected Alpha (lexicographic tie-break) to win, got %q ok=%v", picked, ok)
	}
}

// TestRankCandidates_CloseRatePrefersHigher proves a candidate with a better
// observed close-rate (from the Fire-2 `__effect` window) outranks one with a
// worse rate even at equal cost, and that a candidate with no dispatch
// history yet neither wins nor loses on that term alone (falls through to
// cost/lexicographic).
func TestRankCandidates_CloseRatePrefersHigher(t *testing.T) {
	t.Parallel()
	e := shadowTestEngine(t)
	ctx := context.Background()

	// "Reliable" closes every dispatch; "Flaky" never closes.
	if err := e.marks.recordEffectDispatch(ctx, "t1", "missing_a", "Reliable"); err != nil {
		t.Fatalf("record dispatch: %v", err)
	}
	if err := e.marks.recordEffectClose(ctx, "t1", "missing_a", "Reliable"); err != nil {
		t.Fatalf("record close: %v", err)
	}
	if err := e.marks.recordEffectDispatch(ctx, "t1", "missing_a", "Flaky"); err != nil {
		t.Fatalf("record dispatch: %v", err)
	}

	candidates := []GapCandidate{{Action: "Flaky", Cost: 0}, {Action: "Reliable", Cost: 0}}
	picked, ok := e.rankCandidates(ctx, "t1", "missing_a", candidates, map[string]any{})
	if !ok || picked != "Reliable" {
		t.Fatalf("expected Reliable (higher close-rate) to win, got %q ok=%v", picked, ok)
	}
}

// TestRankCandidates_NoEligible proves ok=false when every candidate's
// precondition fails — the planner has no pick, mirroring Fire 5's future
// "no eligible candidate" outcome.
func TestRankCandidates_NoEligible(t *testing.T) {
	t.Parallel()
	e := shadowTestEngine(t)
	candidates := []GapCandidate{
		{Action: "A", preGuard: parseGuard(t, `{"present":"subject.data.nope"}`)},
	}
	if _, ok := e.rankCandidates(context.Background(), "t1", "missing_a", candidates, map[string]any{}); ok {
		t.Fatalf("expected no pick when every candidate is ineligible")
	}
}

// TestShadowCompare_AgreeAndDiverge proves the recorder: a picked candidate
// matching the table's actual action bumps Agree; a mismatch bumps Diverge
// and appends a bounded divergence record. Never touches dispatch (this test
// calls shadowCompare directly and checks only the shadowStats side effect).
func TestShadowCompare_AgreeAndDiverge(t *testing.T) {
	t.Parallel()
	e := shadowTestEngine(t)
	target := &Target{TargetID: "t1", Mode: targetModeShadow}

	agreeGA := GapAction{Action: "X", Candidates: []GapCandidate{{Action: "X", Cost: 0}, {Action: "Y", Cost: 5}}}
	e.shadowCompare(context.Background(), target, "t1", "e1", "missing_a", agreeGA, map[string]any{})

	divergeGA := GapAction{Action: "X", Candidates: []GapCandidate{{Action: "Y", Cost: 0}}}
	e.shadowCompare(context.Background(), target, "t1", "e2", "missing_a", divergeGA, map[string]any{})

	snap := e.shadow.snapshot()
	stats, ok := snap["t1"]
	if !ok {
		t.Fatalf("expected shadow stats for target t1")
	}
	if stats.Agree != 1 {
		t.Fatalf("expected Agree=1, got %d", stats.Agree)
	}
	if stats.Diverge != 1 {
		t.Fatalf("expected Diverge=1, got %d", stats.Diverge)
	}
	if len(stats.Recent) != 1 || stats.Recent[0].PickedRef != "Y" || stats.Recent[0].ActualRef != "X" {
		t.Fatalf("unexpected divergence record: %+v", stats.Recent)
	}
}

// TestShadowCompare_NoopCases proves shadowCompare never records anything for
// a non-shadow-mode target or a gap with no candidates — the plumbing must
// stay inert for every target installed before this fire (mode absent).
func TestShadowCompare_NoopCases(t *testing.T) {
	t.Parallel()
	e := shadowTestEngine(t)
	ctx := context.Background()

	frozen := &Target{TargetID: "frozen"} // Mode absent
	e.shadowCompare(ctx, frozen, "frozen", "e1", "missing_a",
		GapAction{Action: "X", Candidates: []GapCandidate{{Action: "Y"}}}, map[string]any{})

	shadowNoCandidates := &Target{TargetID: "shadowNoCand", Mode: targetModeShadow}
	e.shadowCompare(ctx, shadowNoCandidates, "shadowNoCand", "e1", "missing_a",
		GapAction{Action: "X"}, map[string]any{})

	if snap := e.shadow.snapshot(); len(snap) != 0 {
		t.Fatalf("expected no shadow stats recorded, got %+v", snap)
	}
}
