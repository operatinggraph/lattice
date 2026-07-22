package planner

import (
	"errors"
	"fmt"
	"strings"

	"github.com/operatinggraph/lattice/internal/guardgrammar"
)

// ErrUnsupportedEffect is returned when a declared effect guard is not a
// concrete assertion (present/absent/equals, or an allOf of those) — an
// anyOf/not effect cannot be turned into a definite fact, so it cannot be
// applied to a planning State. pkgmgr's install-time validation only checks
// that an effect PARSES under the §10.5 grammar (internal/pkgmgr/effects.go);
// this narrower shape rule is the planner's own concern.
var ErrUnsupportedEffect = errors.New("weaver/planner: effect is not a concrete assertion (must be present, absent, equals, or an allOf of those)")

// State is a snapshot of facts the planner reasons over, keyed by the same
// guard-grammar Path a goal/precondition/effect addresses. It stands in for
// whatever a real dispatch would read (a lens row today; §3.2) — this
// package neither knows nor cares where the facts came from.
//
// A missing key means the same as a KV-absent field (nil, an empty/whitespace
// string, or simply never having been asserted) — mirroring
// internal/loom/guard_eval.go's absent() semantics exactly, so a guard
// authored against real Core KV state evaluates identically here.
type State map[guardgrammar.Path]any

// clone returns an independent copy of s so applying effects never mutates a
// state another search branch still holds a reference to.
func (s State) clone() State {
	out := make(State, len(s))
	for k, v := range s {
		out[k] = v
	}
	return out
}

// absent reports whether path p is absent in s, using the §10.5 semantics
// pinned in internal/loom/guard_eval.go: missing / nil / (for strings)
// empty-after-trim. Numbers (including 0) and bools (including false) are
// present.
func (s State) absent(p guardgrammar.Path) bool {
	v, ok := s[p]
	if !ok {
		return true
	}
	switch tv := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(tv) == ""
	default:
		return false
	}
}

// EvalGuard evaluates a parsed §10.5 guard against State, mirroring
// internal/loom/guard_eval.go's evalGuard node-for-node but reading from the
// in-memory snapshot instead of Core KV. Pure: no I/O, always terminates (the
// guard AST is finite and non-cyclic by construction — internal/guardgrammar
// only builds trees from parsed JSON).
func EvalGuard(g *guardgrammar.Guard, s State) bool {
	switch g.Kind {
	case guardgrammar.KindAbsent:
		return s.absent(g.Path)
	case guardgrammar.KindPresent:
		return !s.absent(g.Path)
	case guardgrammar.KindEquals:
		if s.absent(g.Path) {
			return false
		}
		return jsonValuesEqual(s[g.Path], g.Value)
	case guardgrammar.KindAllOf:
		for _, c := range g.Children {
			if !EvalGuard(c, s) {
				return false
			}
		}
		return true
	case guardgrammar.KindAnyOf:
		for _, c := range g.Children {
			if EvalGuard(c, s) {
				return true
			}
		}
		return false
	case guardgrammar.KindNot:
		return !EvalGuard(g.Children[0], s)
	default:
		return false
	}
}

// ApplyEffects returns a new State with every effect atom in effects
// asserted true, leaving s untouched. Each effect must be a concrete
// assertion — present, absent, equals, or an allOf composed of those
// (ErrUnsupportedEffect otherwise); anyOf/not cannot be turned into a
// definite fact and are rejected rather than silently no-op'd.
func ApplyEffects(s State, effects []*guardgrammar.Guard) (State, error) {
	next := s.clone()
	for _, e := range effects {
		if err := applyEffect(next, e); err != nil {
			return nil, err
		}
	}
	return next, nil
}

func applyEffect(s State, g *guardgrammar.Guard) error {
	switch g.Kind {
	case guardgrammar.KindPresent:
		s[g.Path] = true
	case guardgrammar.KindAbsent:
		delete(s, g.Path)
	case guardgrammar.KindEquals:
		s[g.Path] = g.Value
	case guardgrammar.KindAllOf:
		for _, c := range g.Children {
			if err := applyEffect(s, c); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%w: kind %d", ErrUnsupportedEffect, g.Kind)
	}
	return nil
}

// jsonValuesEqual compares two guard-grammar values (State entries and
// Guard.Value are always one of nil/string/float64/bool — the JSON scalar
// set the grammar parses, per guardgrammar.Parse's equals-value validation)
// type-aware, mirroring internal/loom/guard_eval.go's jsonValuesEqual.
func jsonValuesEqual(a, b any) bool {
	switch av := a.(type) {
	case nil:
		return b == nil
	case float64:
		bv, ok := b.(float64)
		return ok && av == bv
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	default:
		return false
	}
}
