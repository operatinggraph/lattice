// Package ruleengine defines the engine-neutral interface, registry, and
// supporting types shared by the v1 "simple" engine (Materializer-derived)
// and the v2 "full" engine (ANTLR-vendored openCypher; stubbed in 3.1a,
// implemented in 3.1b).
//
// Story 3.1a scope: this package provides the boundary contract and the
// selection-logic that resolves a Lens to one of the two engines. ANTLR
// types stay isolated inside internal/refractor/ruleengine/full/cypher and
// MUST NOT leak through this package.
package ruleengine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Engine names. Add new constants here when more engines land.
const (
	EngineSimple = "simple"
	EngineFull   = "full"
)

// CompiledRule is the engine-specific compiled representation of a rule body.
// It is an opaque marker interface: callers pass it back to the same engine
// that produced it via Execute. 3.1a's selection-logic does not need to peek
// inside this value, so the engine-neutral interface intentionally hides the
// concrete type (simple.QueryPlan today; full's executor plan in 3.1b).
type CompiledRule interface {
	// EngineName returns the engine that produced this compiled rule. Useful
	// for debugging mis-routed Execute calls.
	EngineName() string
}

// EventContext carries the per-event inputs an engine needs to project a
// result. Concrete shape is intentionally minimal in 3.1a; 3.1b will extend.
type EventContext struct {
	// NodeKey is the KV key of the entity that changed (e.g. "agreement:42").
	NodeKey string
	// NodeProps holds the current properties of the changed entity.
	NodeProps map[string]any
}

// ProjectionResult is one row produced by Execute. 3.1a does not consume
// this — callers still use the simple engine's concrete EvalResult shape —
// but the type is part of the shared interface so 3.1b can converge on it.
type ProjectionResult struct {
	Key    map[string]any
	Values map[string]any
	Delete bool
}

// ParseError carries a structured failure from an engine's Parse() call so
// the selection-logic can report which engine(s) rejected the rule body.
type ParseError struct {
	Engine  string
	Message string
}

func (e *ParseError) Error() string {
	if e == nil {
		return "<nil parse error>"
	}
	return fmt.Sprintf("[%s] %s", e.Engine, e.Message)
}

// RuleEngine is the common interface both the simple and full engines satisfy.
type RuleEngine interface {
	Name() string
	Parse(ruleBody string) (CompiledRule, error)
	Execute(ctx context.Context, cr CompiledRule, ec EventContext) (ProjectionResult, error)
}

// LensDefinition is the engine-agnostic view of a Lens used by SelectForLens.
// We avoid importing the lens package here to prevent an import cycle —
// lens/schema.go calls into ruleengine.
type LensDefinition struct {
	// ID is the Lens identifier (used only for log/error context).
	ID string
	// RuleBody is the raw match/rule text passed to Engine.Parse.
	RuleBody string
	// RuleEngine is the explicit engine selector. "" (absent) triggers
	// simple-then-full fallback per Decision #3 of the 3.1 brief.
	RuleEngine string
}

// SelectionError carries one or more ParseErrors collected during engine
// resolution. The Errors slice is ordered by attempt: for the absent-fallback
// path it is [simple, full].
type SelectionError struct {
	LensID string
	Errors []*ParseError
}

func (s *SelectionError) Error() string {
	if s == nil {
		return "<nil selection error>"
	}
	parts := make([]string, 0, len(s.Errors))
	for _, e := range s.Errors {
		parts = append(parts, e.Error())
	}
	return fmt.Sprintf("lens %q: no engine accepted rule (%d attempted): %v",
		s.LensID, len(s.Errors), parts)
}

// Registry holds the engines available to selection logic.
type Registry interface {
	Get(name string) (RuleEngine, bool)
	List() []string
	SelectForLens(lens LensDefinition) (resolved RuleEngine, compiled CompiledRule, attempted []string, err error)
}

// staticRegistry is the default Registry implementation backed by a map.
type staticRegistry struct {
	mu      sync.RWMutex
	engines map[string]RuleEngine
}

// NewRegistry returns a Registry seeded with the given engines.
func NewRegistry(engines ...RuleEngine) Registry {
	r := &staticRegistry{engines: make(map[string]RuleEngine, len(engines))}
	for _, e := range engines {
		if e == nil {
			continue
		}
		r.engines[e.Name()] = e
	}
	return r
}

func (r *staticRegistry) Get(name string) (RuleEngine, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.engines[name]
	return e, ok
}

func (r *staticRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.engines))
	for n := range r.engines {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// SelectForLens implements Decision #3 of the 3.1 handoff brief:
//
//   - explicit "simple": try simple only; failure → SelectionError.
//   - explicit "full":   try full only;   failure → SelectionError.
//   - absent ("" ):      try simple; on failure try full; both failing →
//     SelectionError carrying both ParseErrors in [simple, full] order.
//
// On success the resolved engine and its CompiledRule are returned along
// with the list of engine names attempted (caller logs as `attemptedEngines`).
func (r *staticRegistry) SelectForLens(lens LensDefinition) (RuleEngine, CompiledRule, []string, error) {
	switch lens.RuleEngine {
	case EngineSimple:
		return r.tryOne(lens, EngineSimple)
	case EngineFull:
		return r.tryOne(lens, EngineFull)
	case "":
		// fallback: simple, then full
		eng, cr, attempted, err := r.tryOne(lens, EngineSimple)
		if err == nil {
			return eng, cr, attempted, nil
		}
		eng2, cr2, attempted2, err2 := r.tryOne(lens, EngineFull)
		merged := append(attempted, attempted2...)
		if err2 == nil {
			return eng2, cr2, merged, nil
		}
		// Both failed — merge the parse errors in [simple, full] order.
		var simpleErrs, fullErrs *SelectionError
		if errors.As(err, &simpleErrs) && errors.As(err2, &fullErrs) {
			return nil, nil, merged, &SelectionError{
				LensID: lens.ID,
				Errors: append(simpleErrs.Errors, fullErrs.Errors...),
			}
		}
		return nil, nil, merged, &SelectionError{
			LensID: lens.ID,
			Errors: []*ParseError{
				{Engine: EngineSimple, Message: err.Error()},
				{Engine: EngineFull, Message: err2.Error()},
			},
		}
	default:
		return nil, nil, nil, &SelectionError{
			LensID: lens.ID,
			Errors: []*ParseError{{
				Engine:  lens.RuleEngine,
				Message: fmt.Sprintf("unknown ruleEngine %q (valid: simple, full, or empty)", lens.RuleEngine),
			}},
		}
	}
}

func (r *staticRegistry) tryOne(lens LensDefinition, name string) (RuleEngine, CompiledRule, []string, error) {
	attempted := []string{name}
	eng, ok := r.Get(name)
	if !ok {
		return nil, nil, attempted, &SelectionError{
			LensID: lens.ID,
			Errors: []*ParseError{{
				Engine:  name,
				Message: fmt.Sprintf("engine %q not registered", name),
			}},
		}
	}
	cr, err := eng.Parse(lens.RuleBody)
	if err != nil {
		pe, ok := err.(*ParseError)
		if !ok {
			pe = &ParseError{Engine: name, Message: err.Error()}
		}
		return nil, nil, attempted, &SelectionError{
			LensID: lens.ID,
			Errors: []*ParseError{pe},
		}
	}
	return eng, cr, attempted, nil
}
