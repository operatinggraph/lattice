// Package ruleengine defines the engine-neutral interface, registry, and
// supporting types the v2 "full" engine (ANTLR-vendored openCypher)
// implements. ANTLR types stay isolated inside
// internal/refractor/ruleengine/full/cypher and MUST NOT leak through this
// package.
package ruleengine

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Engine names. Add new constants here when more engines land.
const (
	EngineFull = "full"
)

// CompiledRule is the engine-specific compiled representation of a rule body.
// It is an opaque marker interface: callers pass it back to the same engine
// that produced it via Execute. The selection-logic does not need to peek
// inside this value, so the engine-neutral interface intentionally hides the
// concrete type (full's executor plan).
type CompiledRule interface {
	// EngineName returns the engine that produced this compiled rule. Useful
	// for debugging mis-routed Execute calls.
	EngineName() string
}

// EventContext carries the per-event inputs an engine needs to project a
// result. Parameters is used by the full engine to bind `$name` references.
type EventContext struct {
	// NodeKey is the KV key of the entity that changed (e.g. "agreement:42").
	NodeKey string
	// NodeProps holds the current properties of the changed entity.
	NodeProps map[string]any
	// Parameters resolves `$name` references in the rule body.
	// May be nil/empty — engines must tolerate missing maps and return a
	// typed MissingParameterError when an unbound parameter is referenced.
	Parameters map[string]any
	// SeedAnchor narrows the evaluation to ONE anchor: the Core KV vertex key
	// the query's anchor pattern — the first MATCH clause's first node — binds
	// to, instead of scanning every vertex of that pattern's type. Set by a
	// caller that knows the event is a mutation of the anchor itself, so the
	// evaluation only needs to re-derive that anchor's own rows
	// (refractor-footprint-reduction-design.md §D2).
	//
	// Empty (the zero value) means no seeding: the anchor pattern builds its
	// candidate set by scan. An engine that cannot prove the
	// seed key binds the anchor pattern (an unlabeled anchor, a label the key's
	// own vertex type does not match, an anchor already carrying a `key`
	// property) ignores it and scans, so a seed can only ever narrow an
	// evaluation the caller has already proved narrowable.
	SeedAnchor string
}

// MissingParameterError indicates a `$name` reference in the rule body was
// not bound by the caller via EventContext.Parameters. The full engine
// surfaces this so callers can distinguish a missing-wiring bug from a
// graph-shape failure.
type MissingParameterError struct {
	Name string
}

// Error implements the error interface.
func (e *MissingParameterError) Error() string {
	return fmt.Sprintf("missing parameter $%s", e.Name)
}

// ProjectionResult is one row produced by Execute. Callers normalise this
// into EvalResult via pipeline.evaluateForEntry.
type ProjectionResult struct {
	Key    map[string]any
	Values map[string]any
	Delete bool
}

// EvalFootprint is the read-surface certificate one full-engine ExecuteWith
// call produces: every Core KV key (vertex, aspect, or link) the evaluation read,
// paired with the KV revision observed (0 for a key that was absent), and
// every adjacency node it read, paired with the fingerprint
// adjacency.Neighbors returned for it. A validating caller re-reads every
// entry after the evaluation and compares against the recorded value to
// detect a mid-evaluation write to anything the row depended on — an
// absence flipping to present (or the reverse) counts as a moved value,
// since 0 is itself a recorded revision, not a missing map entry.
type EvalFootprint struct {
	// NodeRevisions maps a Core KV key the evaluation point-read — a vertex, an
	// aspect, or a link whose payload a lens dereferenced off a bound
	// relationship — to the revision it was read at. The key shape plays no
	// part: an entry is whatever key the evaluation asked for, validated by
	// re-reading that key.
	NodeRevisions map[string]uint64
	// EdgeRevisions maps an adjacency NodeID to the fingerprint
	// adjacency.Neighbors returned when the evaluation read that node's
	// edges: an ordinary node's document revision, or — for a node whose
	// edge count or document size has crossed adjacency's overflow
	// threshold — a hash over the Core KV link set adjacency.Neighbors
	// enumerated in place of a document (see adjacency.Neighbors). Either
	// way the value is opaque to this package: a validating caller only
	// ever compares it for equality against a fresh read.
	EdgeRevisions map[string]uint64
	// EdgeSelectors maps an adjacency NodeID (same keyspace as EdgeRevisions)
	// to the selector-scoped read-surface record (§13.4): which (relation
	// type, direction) pairs the walk consulted on that node, and which edge
	// identities passed each selector. A validating caller re-applies the
	// recorded selectors to a fresh read instead of comparing the node's
	// whole edge set, so a write to an UNRELATED relation on a shared hub
	// node (a role, an op-meta, a location) does not read as drift.
	// EdgeRevisions remains the fallback comparison for any node whose
	// entry here has Fallback set (or is altogether absent — a defensive
	// case, since every fetchEdges call goes through traverseRel's
	// recording).
	EdgeSelectors map[string]EdgeSelectorFootprint
}

// EdgeSelector is one (relation type, direction) pair a traversal filtered
// an adjacency node's edge list by. Direction uses the SAME vocabulary
// full.Direction.String() already produces ("out"/"in"/"both") — represented
// here as a plain string, not full.Direction, because this package is
// engine-neutral and must not import the full engine's AST types.
type EdgeSelector struct {
	RelType   string
	Direction string
}

// EdgeSelectorFootprint is the selector-scoped read-surface record for one
// adjacency node: which (type,direction) selectors the walk consulted on it,
// and which edge identities passed each selector.
//
// Fallback is set when any hop on this node used an untyped selector
// (RelType == "") that consumes every edge regardless of type, or a
// variable-length hop whose expansion cannot be attributed to a single
// relation name — validation then falls back to comparing the node's whole-
// edge-set fingerprint (EvalFootprint.EdgeRevisions) instead of this
// narrower selector-scoped comparison, coarser being the always-safe
// direction.
type EdgeSelectorFootprint struct {
	Fallback bool
	Matched  map[EdgeSelector]map[string]struct{} // selector -> matched EdgeIDs
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

// RuleEngine is the interface the full engine satisfies.
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
	// RuleEngine is the explicit engine selector. Must be "full" or absent
	// (absent resolves to "full"); any other value is a SelectionError.
	RuleEngine string
}

// SelectionError carries one or more ParseErrors collected during engine
// resolution.
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

// SelectForLens resolves a lens's engine: "full" or absent (which resolves
// to "full") try the full engine; any other value is a SelectionError.
//
// On success the resolved engine and its CompiledRule are returned along
// with the list of engine names attempted (caller logs as `attemptedEngines`).
func (r *staticRegistry) SelectForLens(lens LensDefinition) (RuleEngine, CompiledRule, []string, error) {
	switch lens.RuleEngine {
	case EngineFull, "":
		return r.tryOne(lens, EngineFull)
	default:
		return nil, nil, nil, &SelectionError{
			LensID: lens.ID,
			Errors: []*ParseError{{
				Engine:  lens.RuleEngine,
				Message: fmt.Sprintf("unknown ruleEngine %q (valid: full or empty)", lens.RuleEngine),
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
