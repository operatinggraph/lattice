package planner

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/guardgrammar"
)

// ErrNoPlan is the first-class "no plan derivable" outcome (design §3.1): the
// bounded search exhausting without satisfying the goal is a normal return
// value, not a panic — Synthesize's caller (the Fire-6 Strategist wiring)
// routes it into the existing unplannable escalation exactly like today's
// "no playbook entry" case.
var ErrNoPlan = errors.New("weaver/planner: no plan derivable")

// Action is one entry in the planner's closed catalog: an operation (or a
// Loom pattern treated as a macro-action, §3.3) declaring what it requires
// before it may fire (Precondition, nil = always available) and what its
// commit entails (Effects — §10.8 declared guard-grammar atoms, implicitly
// ANDed together).
type Action struct {
	// Ref is the actionRef used for both dispatch (Fire 5/6's playbook
	// materialization) and canonical tie-breaking (§3.1: lexicographic on
	// Ref when Cost ties). Must be unique within one catalog.
	Ref string
	// Cost ranks candidates and orders plans (§3.1: cost ascending is the
	// search's primary objective — Synthesize finds the cheapest-total-cost
	// plan, not the fewest-step one). Must be >= 0.
	Cost int
	// Precondition gates whether this action may be applied from a given
	// State. Nil means always available.
	Precondition *guardgrammar.Guard
	// Effects are the atoms this action's commit entails once dispatched;
	// each must be a concrete assertion (ApplyEffects / ErrUnsupportedEffect).
	Effects []*guardgrammar.Guard
}

// Step is one action of a synthesized Plan.
type Step struct {
	ActionRef string
}

// Plan is a synthesized, cost-ranked sequence of catalog actions that
// transforms the start State into one where Goal holds. A zero-length Steps
// slice means the goal already held in the start state.
type Plan struct {
	Steps []Step
	Cost  int
}

// frontierEntry is one node of the search: the State reached after Steps,
// and the total Cost paid to reach it.
type frontierEntry struct {
	state State
	steps []Step
	cost  int
}

// Synthesize performs bounded goal regression (STRIPS/GOAP-class uniform-cost
// search, §3.1): starting from start, it repeatedly applies catalog actions
// whose Precondition currently holds, until a reachable state satisfies goal,
// and returns the cheapest such sequence found within maxDepth steps.
//
// Determinism (§3.1, load-bearing): the catalog is copied and sorted (cost
// ascending, then Ref lexicographically) before search, so expansion order
// never depends on the caller's slice order; ties between equal-cost plans
// break on the lexicographic join of their action refs. No wall-clock is
// read. The same (goal, start, catalog, maxDepth) always yields the same
// Plan or the same ErrNoPlan.
//
// maxDepth bounds the number of actions in any candidate plan — a backstop
// against oscillating effects (an action whose effects undo another's) that
// would otherwise let the search run unbounded; it never itself signals an
// error, it simply prunes deeper branches from consideration.
func Synthesize(goal *guardgrammar.Guard, start State, catalog []Action, maxDepth int) (*Plan, error) {
	ordered := make([]Action, len(catalog))
	copy(ordered, catalog)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Cost != ordered[j].Cost {
			return ordered[i].Cost < ordered[j].Cost
		}
		return ordered[i].Ref < ordered[j].Ref
	})

	frontier := []frontierEntry{{state: start, steps: nil, cost: 0}}
	finalized := make(map[string]bool)

	for len(frontier) > 0 {
		bi := 0
		for i := 1; i < len(frontier); i++ {
			if lessEntry(frontier[i], frontier[bi]) {
				bi = i
			}
		}
		cur := frontier[bi]
		frontier = append(frontier[:bi], frontier[bi+1:]...)

		fp := fingerprint(cur.state)
		if finalized[fp] {
			// A cheaper (or equal-cost, canonically-earlier) path to this
			// exact state already settled it; this entry can add nothing.
			continue
		}
		finalized[fp] = true

		if EvalGuard(goal, cur.state) {
			return &Plan{Steps: cur.steps, Cost: cur.cost}, nil
		}
		if len(cur.steps) >= maxDepth {
			continue
		}
		for _, a := range ordered {
			if a.Precondition != nil && !EvalGuard(a.Precondition, cur.state) {
				continue
			}
			next, err := ApplyEffects(cur.state, a.Effects)
			if err != nil {
				// A catalog entry whose effects cannot be applied contributes
				// nothing to the search; the shape error surfaces at install
				// time (pkgmgr), not here.
				continue
			}
			if finalized[fingerprint(next)] {
				continue
			}
			steps := make([]Step, len(cur.steps)+1)
			copy(steps, cur.steps)
			steps[len(cur.steps)] = Step{ActionRef: a.Ref}
			frontier = append(frontier, frontierEntry{state: next, steps: steps, cost: cur.cost + a.Cost})
		}
	}
	return nil, ErrNoPlan
}

// lessEntry orders two frontier entries for the search's pop-minimum step:
// total cost ascending, then the joined action-ref sequence lexicographically
// — the same canonical tie-break Synthesize's result is guaranteed under.
func lessEntry(a, b frontierEntry) bool {
	if a.cost != b.cost {
		return a.cost < b.cost
	}
	return joinRefs(a.steps) < joinRefs(b.steps)
}

func joinRefs(steps []Step) string {
	refs := make([]string, len(steps))
	for i, s := range steps {
		refs[i] = s.ActionRef
	}
	return strings.Join(refs, "\x00")
}

// fingerprint returns a canonical, order-independent string identity for a
// State, used to dedupe search nodes (settle each distinct state at most
// once, at its cheapest reaching cost). Facts are sorted by Path so the
// fingerprint never depends on State's underlying map iteration order.
func fingerprint(s State) string {
	paths := make([]guardgrammar.Path, 0, len(s))
	for p := range s {
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool {
		if paths[i].Aspect != paths[j].Aspect {
			return paths[i].Aspect < paths[j].Aspect
		}
		return paths[i].Field < paths[j].Field
	})
	var b strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&b, "%s.%s=%T:%v;", p.Aspect, p.Field, s[p], s[p])
	}
	return b.String()
}
