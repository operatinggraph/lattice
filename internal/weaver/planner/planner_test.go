package planner

import (
	"errors"
	"testing"

	"github.com/operatinggraph/lattice/internal/guardgrammar"
)

func path(aspect, field string) guardgrammar.Path {
	return guardgrammar.Path{Aspect: aspect, Field: field}
}

func present(p guardgrammar.Path) *guardgrammar.Guard {
	return &guardgrammar.Guard{Kind: guardgrammar.KindPresent, Path: p}
}

func absentG(p guardgrammar.Path) *guardgrammar.Guard {
	return &guardgrammar.Guard{Kind: guardgrammar.KindAbsent, Path: p}
}

func equals(p guardgrammar.Path, v any) *guardgrammar.Guard {
	return &guardgrammar.Guard{Kind: guardgrammar.KindEquals, Path: p, Value: v, HasValue: true}
}

func allOf(children ...*guardgrammar.Guard) *guardgrammar.Guard {
	return &guardgrammar.Guard{Kind: guardgrammar.KindAllOf, Children: children}
}

func anyOf(children ...*guardgrammar.Guard) *guardgrammar.Guard {
	return &guardgrammar.Guard{Kind: guardgrammar.KindAnyOf, Children: children}
}

func notG(child *guardgrammar.Guard) *guardgrammar.Guard {
	return &guardgrammar.Guard{Kind: guardgrammar.KindNot, Children: []*guardgrammar.Guard{child}}
}

var signedAt = path("signature", "signedAt")
var outcome = path("outcome", "value")

func TestEvalGuard(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		guard *guardgrammar.Guard
		state State
		want  bool
	}{
		{"present_missing_key_is_false", present(signedAt), State{}, false},
		{"present_nonempty_string_is_true", present(signedAt), State{signedAt: "2026-07-04T00:00:00Z"}, true},
		{"present_empty_string_is_false", present(signedAt), State{signedAt: "   "}, false},
		{"present_nil_is_false", present(signedAt), State{signedAt: nil}, false},
		{"present_false_bool_is_true", present(signedAt), State{signedAt: false}, true},
		{"present_zero_number_is_true", present(signedAt), State{signedAt: float64(0)}, true},
		{"absent_missing_key_is_true", absentG(signedAt), State{}, true},
		{"absent_present_key_is_false", absentG(signedAt), State{signedAt: "x"}, false},
		{"equals_matches", equals(outcome, "completed"), State{outcome: "completed"}, true},
		{"equals_mismatches", equals(outcome, "completed"), State{outcome: "failed"}, false},
		{"equals_absent_never_matches_even_null", equals(outcome, nil), State{}, false},
		{"allOf_all_true", allOf(present(signedAt), equals(outcome, "completed")), State{signedAt: "x", outcome: "completed"}, true},
		{"allOf_one_false", allOf(present(signedAt), equals(outcome, "completed")), State{signedAt: "x", outcome: "failed"}, false},
		{"anyOf_one_true", anyOf(present(signedAt), equals(outcome, "completed")), State{outcome: "completed"}, true},
		{"anyOf_all_false", anyOf(present(signedAt), equals(outcome, "completed")), State{}, false},
		{"not_inverts", notG(present(signedAt)), State{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := EvalGuard(tc.guard, tc.state); got != tc.want {
				t.Errorf("EvalGuard(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestApplyEffects(t *testing.T) {
	t.Parallel()

	t.Run("present_sets_a_non_absent_value", func(t *testing.T) {
		t.Parallel()
		next, err := ApplyEffects(State{}, []*guardgrammar.Guard{present(signedAt)})
		if err != nil {
			t.Fatalf("ApplyEffects: %v", err)
		}
		if !EvalGuard(present(signedAt), next) {
			t.Errorf("expected %v present after applying its own present effect", signedAt)
		}
	})

	t.Run("absent_clears_a_previously_set_value", func(t *testing.T) {
		t.Parallel()
		start := State{signedAt: "x"}
		next, err := ApplyEffects(start, []*guardgrammar.Guard{absentG(signedAt)})
		if err != nil {
			t.Fatalf("ApplyEffects: %v", err)
		}
		if !EvalGuard(absentG(signedAt), next) {
			t.Errorf("expected %v absent after applying its own absent effect", signedAt)
		}
		if !EvalGuard(present(signedAt), start) {
			t.Errorf("ApplyEffects mutated the source state; it must clone")
		}
	})

	t.Run("equals_sets_the_comparand", func(t *testing.T) {
		t.Parallel()
		next, err := ApplyEffects(State{}, []*guardgrammar.Guard{equals(outcome, "completed")})
		if err != nil {
			t.Fatalf("ApplyEffects: %v", err)
		}
		if !EvalGuard(equals(outcome, "completed"), next) {
			t.Errorf("expected outcome=completed after applying its own equals effect")
		}
	})

	t.Run("allOf_applies_every_child", func(t *testing.T) {
		t.Parallel()
		next, err := ApplyEffects(State{}, []*guardgrammar.Guard{allOf(present(signedAt), equals(outcome, "completed"))})
		if err != nil {
			t.Fatalf("ApplyEffects: %v", err)
		}
		if !EvalGuard(allOf(present(signedAt), equals(outcome, "completed")), next) {
			t.Errorf("expected both allOf children applied")
		}
	})

	t.Run("anyOf_effect_is_rejected", func(t *testing.T) {
		t.Parallel()
		_, err := ApplyEffects(State{}, []*guardgrammar.Guard{anyOf(present(signedAt), present(outcome))})
		if !errors.Is(err, ErrUnsupportedEffect) {
			t.Errorf("ApplyEffects(anyOf) error = %v, want ErrUnsupportedEffect", err)
		}
	})

	t.Run("not_effect_is_rejected", func(t *testing.T) {
		t.Parallel()
		_, err := ApplyEffects(State{}, []*guardgrammar.Guard{notG(present(signedAt))})
		if !errors.Is(err, ErrUnsupportedEffect) {
			t.Errorf("ApplyEffects(not) error = %v, want ErrUnsupportedEffect", err)
		}
	})
}

// signLease mirrors the real lease-signing SignLease effect (Fire 1,
// packages/lease-signing/ddls.go): unconditionally asserts .signature present.
func signLease() Action {
	return Action{Ref: "SignLease", Cost: 1, Effects: []*guardgrammar.Guard{present(signedAt)}}
}

func TestSynthesize_GoalAlreadySatisfied(t *testing.T) {
	t.Parallel()
	start := State{signedAt: "x"}
	got, err := Synthesize(present(signedAt), start, []Action{signLease()}, 5)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if len(got.Steps) != 0 || got.Cost != 0 {
		t.Errorf("Synthesize on an already-satisfied goal = %+v, want a zero-step zero-cost plan", got)
	}
}

func TestSynthesize_SingleActionSatisfiesGoal(t *testing.T) {
	t.Parallel()
	got, err := Synthesize(present(signedAt), State{}, []Action{signLease()}, 5)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if len(got.Steps) != 1 || got.Steps[0].ActionRef != "SignLease" || got.Cost != 1 {
		t.Errorf("Synthesize = %+v, want [SignLease] cost 1", got)
	}
}

func TestSynthesize_ChainThroughPrecondition(t *testing.T) {
	t.Parallel()
	// openDoor has no precondition and asserts doorOpen; walkThrough requires
	// doorOpen and asserts arrived — a 2-hop chain neither action alone can
	// close, proving regression re-derives subgoals through preconditions.
	door := path("", "doorOpen")
	arrived := path("", "arrived")
	openDoor := Action{Ref: "openDoor", Cost: 1, Effects: []*guardgrammar.Guard{present(door)}}
	walkThrough := Action{
		Ref: "walkThrough", Cost: 1,
		Precondition: present(door),
		Effects:      []*guardgrammar.Guard{present(arrived)},
	}
	got, err := Synthesize(present(arrived), State{}, []Action{walkThrough, openDoor}, 5)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if len(got.Steps) != 2 || got.Steps[0].ActionRef != "openDoor" || got.Steps[1].ActionRef != "walkThrough" {
		t.Errorf("Synthesize = %+v, want [openDoor walkThrough]", got)
	}
	if got.Cost != 2 {
		t.Errorf("Synthesize cost = %d, want 2", got.Cost)
	}
}

func TestSynthesize_NoPlanDerivable(t *testing.T) {
	t.Parallel()
	_, err := Synthesize(present(outcome), State{}, []Action{signLease()}, 5)
	if !errors.Is(err, ErrNoPlan) {
		t.Errorf("Synthesize error = %v, want ErrNoPlan", err)
	}
}

func TestSynthesize_DepthBoundPrunesAnOtherwiseReachableGoal(t *testing.T) {
	t.Parallel()
	door := path("", "doorOpen")
	arrived := path("", "arrived")
	openDoor := Action{Ref: "openDoor", Cost: 1, Effects: []*guardgrammar.Guard{present(door)}}
	walkThrough := Action{Ref: "walkThrough", Cost: 1, Precondition: present(door), Effects: []*guardgrammar.Guard{present(arrived)}}
	_, err := Synthesize(present(arrived), State{}, []Action{openDoor, walkThrough}, 1)
	if !errors.Is(err, ErrNoPlan) {
		t.Errorf("Synthesize with maxDepth 1 on a 2-hop goal error = %v, want ErrNoPlan", err)
	}
}

func TestSynthesize_PrefersCheaperTotalCostOverFewerSteps(t *testing.T) {
	t.Parallel()
	// A single expensive action reaches the goal directly; two cheap actions
	// reach it via a 2-step chain at lower total cost. Synthesize must pick
	// the cheaper chain, proving it optimizes total cost, not step count.
	expensive := Action{Ref: "expensiveDirect", Cost: 5, Effects: []*guardgrammar.Guard{present(outcome)}}
	cheapStep1 := Action{Ref: "cheapStep1", Cost: 1, Effects: []*guardgrammar.Guard{present(signedAt)}}
	cheapStep2 := Action{Ref: "cheapStep2", Cost: 1, Precondition: present(signedAt), Effects: []*guardgrammar.Guard{present(outcome)}}

	got, err := Synthesize(present(outcome), State{}, []Action{expensive, cheapStep1, cheapStep2}, 5)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if got.Cost != 2 {
		t.Errorf("Synthesize.Cost = %d, want 2 (the cheaper chain)", got.Cost)
	}
	if len(got.Steps) != 2 || got.Steps[0].ActionRef != "cheapStep1" || got.Steps[1].ActionRef != "cheapStep2" {
		t.Errorf("Synthesize.Steps = %+v, want [cheapStep1 cheapStep2]", got.Steps)
	}
}

func TestSynthesize_TieBreaksLexicographicallyOnActionRef(t *testing.T) {
	t.Parallel()
	// Two equal-cost single-step actions both satisfy the goal independently;
	// the canonical tie-break (§3.1) must deterministically prefer the
	// lexicographically smaller actionRef.
	zebra := Action{Ref: "zebraAction", Cost: 1, Effects: []*guardgrammar.Guard{present(outcome)}}
	alpha := Action{Ref: "alphaAction", Cost: 1, Effects: []*guardgrammar.Guard{present(outcome)}}

	got, err := Synthesize(present(outcome), State{}, []Action{zebra, alpha}, 5)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if len(got.Steps) != 1 || got.Steps[0].ActionRef != "alphaAction" {
		t.Errorf("Synthesize.Steps = %+v, want [alphaAction] (lexicographic tie-break)", got.Steps)
	}
}

// TestSynthesize_CatalogPermutationStability proves the search's result
// depends only on the (goal, state, catalog-as-a-set, maxDepth) tuple, never
// on the order the caller happened to list the catalog in (§3.1: "no ...
// map-order dependence"). A real catalog is assembled from package-authored
// DDL effects across an unordered install set, so this must hold for the
// planner to be usable at all.
func TestSynthesize_CatalogPermutationStability(t *testing.T) {
	t.Parallel()
	door := path("", "doorOpen")
	catalog := []Action{
		{Ref: "walkThrough", Cost: 1, Precondition: present(door), Effects: []*guardgrammar.Guard{present(outcome)}},
		{Ref: "openDoor", Cost: 1, Effects: []*guardgrammar.Guard{present(door)}},
		{Ref: "expensiveDirect", Cost: 5, Effects: []*guardgrammar.Guard{present(outcome)}},
		{Ref: "alphaAction", Cost: 1, Effects: []*guardgrammar.Guard{present(door)}},
	}
	permutations := [][]int{
		{0, 1, 2, 3},
		{3, 2, 1, 0},
		{2, 0, 3, 1},
		{1, 3, 0, 2},
	}
	var first *Plan
	for i, perm := range permutations {
		ordered := make([]Action, len(perm))
		for j, idx := range perm {
			ordered[j] = catalog[idx]
		}
		got, err := Synthesize(present(outcome), State{}, ordered, 5)
		if err != nil {
			t.Fatalf("permutation %d: Synthesize: %v", i, err)
		}
		if first == nil {
			first = got
			continue
		}
		if got.Cost != first.Cost || len(got.Steps) != len(first.Steps) {
			t.Fatalf("permutation %d produced a different plan shape: %+v, want %+v", i, got, first)
		}
		for k := range got.Steps {
			if got.Steps[k] != first.Steps[k] {
				t.Fatalf("permutation %d produced a different plan: %+v, want %+v", i, got, first)
			}
		}
	}
}
