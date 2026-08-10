// Package full's executor walks the Refractor-native AST against Core KV
// (vertex/aspect data) and Adjacency KV (edges) to produce projection rows.
//
// The design stays close to the AST — there is no separate "plan" stage
// between Parse and Execute. Execution proceeds clause-by-clause over a
// list of bindings; each binding maps variable names to either a *nodeRef
// (graph node) or any other value (post-WITH alias).
//
// All Core KV reads filter `isDeleted: true` per Contract #1. All edge
// lookups go through Adjacency KV via the adjacency package.
package full

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// maxVarLengthHops is the sanity cap for variable-length traversals when the
// AST records MaxHops=-1 (unbounded). Without a cap a cycle-free but deep
// graph could trigger pathological BFS.
const maxVarLengthHops = 10

// errCoreKVReadDisabled signals that an expression needs a Core-KV read while
// the executor is in read-free mode (coreKV == nil — the anchor-tombstone
// delete key resolution). The caller treats it as "key column unresolvable" and
// falls through to a re-execute, never a wrong Delete.
var errCoreKVReadDisabled = errors.New("full engine: Core KV read disabled (read-free key resolution)")

// nodeRef is the executor's in-memory handle to a Core KV vertex.
// A nil nodeRef represents an OPTIONAL MATCH null binding.
//
// revision is the Core KV revision the entry was read at (0 for an absent
// key) — the read-surface footprint a validating caller compares after
// evaluation to detect a mid-evaluation write to this key.
type nodeRef struct {
	key      string
	props    map[string]any
	revision uint64
}

// binding maps variable names to values. Variable bindings established by
// MATCH/OPTIONAL MATCH carry *nodeRef values; WITH aliases may carry any
// scalar, list, map, or *nodeRef.
type binding map[string]any

// executor carries per-call mutable state for one Execute invocation.
type executor struct {
	ctx        context.Context
	adjKV      *substrate.KV
	coreKV     *substrate.KV
	params     map[string]any
	keyColumns []string

	// seedAnchor is the Core KV vertex key the anchor pattern's candidate set
	// narrows to for this evaluation (EventContext.SeedAnchor), armed only
	// when seedAnchorFor proved the query's anchor pattern can be point-seeded
	// by it. Empty means every pattern scans as usual.
	//
	// It is consumed exactly once — takeSeedAnchor clears it at the first
	// candidate set this evaluation builds by scan, which is the anchor by
	// construction (the first MATCH clause's first node is the first pattern
	// any evaluation seeds). Clearing is what keeps a later MATCH clause, an
	// OPTIONAL MATCH, or a sibling comma-separated pattern of the same label
	// from also collapsing to the event vertex.
	seedAnchor string

	// nodes memoizes fetchNode by Core KV key for the life of ONE evaluation,
	// giving the executor repeatable-read semantics: every access to a given
	// key inside one Execute observes the same value, whatever commits land
	// while the evaluation runs.
	//
	// This is a correctness requirement, not a cache. An aspect hop
	// (`u.listing.data.status`) that misses the vertex root body resolves as a
	// live point-read, and projectItems evaluates every non-aggregate WITH
	// column once per binding row — so one column is read once per row of the
	// OPTIONAL MATCH cross-product. Without the memo a commit landing between
	// two of those reads yields two different values for one column; because
	// the non-aggregate columns ARE the grouping key, that splits a single
	// anchor into two groups, and an actor-aggregate lens then emits two rows
	// sharing one anchor-derived output key. The pipeline's collision guard
	// fails that actor closed, so the projection update is dropped entirely.
	// Memoizing by key closes the class: a group splits only when the same
	// expression yields two values, and the same expression over the same node
	// is always the same key.
	//
	// A nil entry (absent / tombstoned / null-body) is memoized too — an
	// absent-then-created aspect splits a group exactly as a changed one does.
	nodes map[string]*nodeRef

	// edges memoizes adjacency.Neighbors by adjacency NodeID for the life of
	// ONE evaluation — the link twin of nodes, closing the same class of
	// defect for relationship hops: without it, every hop through a given
	// node reads live from Adjacency KV, so two hops through the same node
	// inside one evaluation (a variable-length traversal revisiting a
	// frontier node, or two separate MATCH clauses walking through it) could
	// observe two different edge lists for it. edgeRevisions records the KV
	// revision each memoized node's adjacency document was read at (0 =
	// absent) — the adjacency half of the read-surface footprint a
	// validating caller compares after evaluation.
	edges         map[string][]adjacency.EdgeEntry
	edgeRevisions map[string]uint64

	// edgeSelectors records, per adjacency node, the selector-scoped read
	// surface §13.4 adds: which (relation type, direction) pairs traverseRel
	// consulted on that node, and which edge identities passed each one —
	// the narrower unit footprintValid compares against instead of the
	// node's whole-document revision, so a write to an unrelated relation on
	// a shared hub node no longer reads as drift. Populated alongside edges/
	// edgeRevisions; never populated for a node this evaluation never
	// traversed a typed relationship through (fetchEdges alone, with no
	// traverseRel selector recording, leaves no entry here — footprint()
	// falls back to whole-document comparison for it, same as Fallback).
	edgeSelectors map[string]*ruleengine.EdgeSelectorFootprint

	// maxBindings caps the binding set any one stage of this evaluation may
	// materialize; 0 disables the cap. steps counts checkpoint calls so the
	// context check can be sampled rather than paid per row.
	maxBindings int
	steps       int

	// labelExpansion is the compiled rule's LabelExpansion, threaded through
	// so nodeMatches and the re-check inside seedNodes can resolve a
	// LabelExpand-flagged pattern without a graph read (§4.4 — the hot path
	// stays a map lookup). Nil for a rule with no `*` pattern, matching
	// CompiledRule.LabelExpansion's own zero value.
	labelExpansion map[string]map[string]struct{}
}

// cancelCheckInterval is how many checkpoint calls pass between context
// cancellation checks. A runaway evaluation is runaway because of its row
// count, so sampling bounds shutdown latency without paying a mutex-guarded
// ctx.Err() on every row of every stage.
const cancelCheckInterval = 1024

// checkCancelled reports the evaluation's context cancellation, sampled every
// cancelCheckInterval calls.
//
// Every read this evaluation makes is memoized, so once the candidate set is
// built a large MATCH + aggregation pass issues no further KV calls and is
// pure in-process work. Without a checkpoint inside it, a runaway evaluation
// runs to completion (or takes the host down) after SIGTERM: the consumer
// supervisor's Stop and the process's final WaitGroup both block on the
// in-flight handler, so nothing can exit until the evaluation finishes on its
// own. Aborting here surfaces as an evaluation error, which the pipeline
// disposes as a redelivery — never as an empty result set, so no projection
// target is retracted on the way down.
func (ex *executor) checkCancelled() error {
	ex.steps++
	if ex.steps%cancelCheckInterval != 0 {
		return nil
	}
	if err := ex.ctx.Err(); err != nil {
		return fmt.Errorf("full engine: evaluation cancelled: %w", err)
	}
	return nil
}

// checkBindings is the per-stage checkpoint: cancellation plus the binding-set
// cap, called wherever a stage's materialized binding slice has just grown.
// Each such slice is resident for the stage's life, so capping every one of
// them is what bounds the evaluation's heap.
//
// It errors rather than truncating. A truncated binding set silently produces
// a WRONG projection row — a short count(), a partial collect() — and writes it
// to the read model, where nothing downstream can tell it from a correct one.
// A refused evaluation is redelivered and visible. (This is the opposite call
// from the actor enumerator's cap, which truncates-and-warns because it bounds
// who gets notified, not the content of a row.)
func (ex *executor) checkBindings(rows int) error {
	if err := ex.checkCancelled(); err != nil {
		return err
	}
	if ex.maxBindings <= 0 || rows <= ex.maxBindings {
		return nil
	}
	slog.Warn("full engine: binding-set cap exceeded; evaluation refused",
		"rows", rows, "cap", ex.maxBindings)
	return fmt.Errorf(
		"full engine: binding set reached %d rows, over the cap of %d (raise REFRACTOR_MAX_BINDINGS if this query is legitimate)",
		rows, ex.maxBindings)
}

// ExecuteWith runs cr against the given Core and Adjacency KVs, binding
// `$name` references from ec.Parameters. Called by the pipeline for each
// CDC event on the full-engine path.
//
// Returns one ProjectionResult per result row. Empty result => zero rows.
// ExecuteWithFootprint is the sibling entry point that also returns the
// evaluation's read-surface footprint; ExecuteWith exists unchanged so every
// pre-existing caller (package lens tests across packages/*, and any
// production caller uninterested in footprint validation) keeps compiling
// against the same two-return shape.
func (e *Engine) ExecuteWith(
	ctx context.Context,
	cr ruleengine.CompiledRule,
	ec ruleengine.EventContext,
	adjKV, coreKV *substrate.KV,
) ([]ruleengine.ProjectionResult, error) {
	results, _, err := e.ExecuteWithFootprint(ctx, cr, ec, adjKV, coreKV)
	return results, err
}

// ExecuteWithFootprint runs cr exactly as ExecuteWith does, additionally
// returning this evaluation's read-surface footprint (every vertex/aspect key
// and adjacency node it read, with the revision observed) — the certificate a
// validating caller (executeFullForActor in package pipeline) compares
// against current KV state after evaluation to detect a mid-evaluation write.
func (e *Engine) ExecuteWithFootprint(
	ctx context.Context,
	cr ruleengine.CompiledRule,
	ec ruleengine.EventContext,
	adjKV, coreKV *substrate.KV,
) ([]ruleengine.ProjectionResult, ruleengine.EvalFootprint, error) {
	compiled, ok := cr.(*CompiledRule)
	if !ok {
		return nil, ruleengine.EvalFootprint{}, fmt.Errorf("full engine: expected *CompiledRule, got %T", cr)
	}
	if compiled.Query == nil {
		return nil, ruleengine.EvalFootprint{}, errors.New("full engine: compiled rule has nil query")
	}

	ex := &executor{
		ctx:            ctx,
		adjKV:          adjKV,
		coreKV:         coreKV,
		params:         ec.Parameters,
		keyColumns:     compiled.KeyColumns,
		seedAnchor:     seedAnchorFor(compiled.Query, ec.SeedAnchor, compiled.LabelExpansion),
		nodes:          map[string]*nodeRef{},
		edges:          map[string][]adjacency.EdgeEntry{},
		edgeRevisions:  map[string]uint64{},
		edgeSelectors:  map[string]*ruleengine.EdgeSelectorFootprint{},
		maxBindings:    e.maxBindings,
		labelExpansion: compiled.LabelExpansion,
	}

	bindings := []binding{{}}
	var lastReturn *Return

	for _, clause := range compiled.Query.Clauses {
		switch c := clause.(type) {
		case *Match:
			next, err := ex.applyMatch(bindings, c)
			if err != nil {
				return nil, ruleengine.EvalFootprint{}, err
			}
			bindings = next
		case *With:
			next, err := ex.applyWith(bindings, c)
			if err != nil {
				return nil, ruleengine.EvalFootprint{}, err
			}
			bindings = next
		case *Return:
			lastReturn = c
		}
	}

	if lastReturn == nil {
		return nil, ruleengine.EvalFootprint{}, errors.New("full engine: query missing RETURN clause")
	}
	results, err := ex.applyReturn(bindings, lastReturn)
	if err != nil {
		return nil, ruleengine.EvalFootprint{}, err
	}
	footprint := ex.footprint()
	if hook := footprintCapturedHook(ctx); hook != nil {
		hook()
	}
	return results, footprint, nil
}

// footprint returns the read-surface certificate this evaluation observed —
// every Core KV key it read (via the node memo, absent = revision 0) and
// every adjacency node it read (via the edge memo, absent = revision 0). Both
// memos are already populated for the life of the evaluation (see the nodes/
// edges field docs), so building the footprint costs no extra reads.
func (ex *executor) footprint() ruleengine.EvalFootprint {
	nodeRevs := make(map[string]uint64, len(ex.nodes))
	for key, ref := range ex.nodes {
		if ref == nil {
			nodeRevs[key] = 0
			continue
		}
		nodeRevs[key] = ref.revision
	}
	edgeRevs := make(map[string]uint64, len(ex.edgeRevisions))
	for nodeID, rev := range ex.edgeRevisions {
		edgeRevs[nodeID] = rev
	}
	edgeSelectors := make(map[string]ruleengine.EdgeSelectorFootprint, len(ex.edgeSelectors))
	for nodeID, sel := range ex.edgeSelectors {
		edgeSelectors[nodeID] = *sel
	}
	return ruleengine.EvalFootprint{NodeRevisions: nodeRevs, EdgeRevisions: edgeRevs, EdgeSelectors: edgeSelectors}
}

// Execute satisfies ruleengine.RuleEngine. It is the single-row convenience
// Execute satisfies ruleengine.RuleEngine but cannot operate on a real graph
// because the engine-neutral signature does not carry KV handles. The pipeline
// calls ExecuteWith directly. Returning a typed error keeps the contract honest.
func (e *Engine) Execute(_ context.Context, _ ruleengine.CompiledRule, _ ruleengine.EventContext) (ruleengine.ProjectionResult, error) {
	return ruleengine.ProjectionResult{}, errors.New(
		"full engine: Execute requires KV handles — call ExecuteWith from the pipeline")
}

// --- MATCH ---

func (ex *executor) applyMatch(bindings []binding, m *Match) ([]binding, error) {
	var out []binding
	for _, b := range bindings {
		expanded, err := ex.matchPatterns(b, m.Patterns, m.Optional)
		if err != nil {
			return nil, err
		}
		// Apply WHERE. For OPTIONAL MATCH, WHERE filters MATCH'd rows but if
		// all matches are filtered out, the optional null-binding preserves
		// the original binding (Cypher OPTIONAL MATCH ... WHERE semantics).
		var passing []binding
		hadNonNullMatch := false
		for _, nb := range expanded {
			// "Non-null match" = at least one newly introduced pattern var
			// is bound to a real *nodeRef (not the null sentinel) in this
			// expansion that wasn't bound in b.
			if isNonNullExpansion(b, nb, m.Patterns) {
				hadNonNullMatch = true
				if m.Where != nil {
					v, err := ex.evalExpr(nb, m.Where)
					if err != nil {
						return nil, err
					}
					if !truthy(v) {
						continue
					}
				}
				passing = append(passing, nb)
			} else {
				// Null-preserving row — keep regardless of WHERE for OPTIONAL,
				// drop for required (which shouldn't produce null expansions).
				if m.Optional {
					passing = append(passing, nb)
				}
			}
		}
		if m.Optional && hadNonNullMatch {
			// Drop the null-preserving fallback rows when at least one real
			// match exists for THIS source binding.
			filtered := passing[:0]
			for _, nb := range passing {
				if isNonNullExpansion(b, nb, m.Patterns) {
					filtered = append(filtered, nb)
				}
			}
			// All real matches were excluded by WHERE: the OPTIONAL MATCH yields
			// no neighbor, so the anchor survives with the pattern variables bound
			// null (Cypher OPTIONAL MATCH ... WHERE semantics). The null fallback
			// is constructed from the source binding — the expansion set holds only
			// the now-filtered real rows when the pattern matched real neighbors, so
			// there is no null row there to recover.
			if len(filtered) == 0 {
				filtered = append(filtered, nullBindNewVars(b, m.Patterns))
			}
			passing = filtered
		}
		out = append(out, passing...)
		if err := ex.checkBindings(len(out)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// isNonNullExpansion reports whether nb is a "real" match expansion of b
// (i.e. at least one newly introduced variable from patterns is bound to a
// non-nil *nodeRef in nb but absent in b).
func isNonNullExpansion(b, nb binding, patterns []PathPattern) bool {
	for _, p := range patterns {
		for _, n := range p.Nodes {
			if n.Variable == "" {
				continue
			}
			if _, had := b[n.Variable]; had {
				continue
			}
			if ref, ok := nb[n.Variable].(*nodeRef); ok && ref != nil {
				return true
			}
		}
		for _, r := range p.Rels {
			if r.Variable == "" {
				continue
			}
			if _, had := b[r.Variable]; had {
				continue
			}
			if ref, ok := nb[r.Variable].(*nodeRef); ok && ref != nil {
				return true
			}
		}
	}
	return false
}

// nullBindNewVars clones b and binds every pattern variable in patterns that b
// does not already carry to the OPTIONAL-MATCH null sentinel ((*nodeRef)(nil)).
// It is the null fallback an OPTIONAL MATCH preserves when a pattern matches no
// neighbor at all (matchPatterns) OR when a WHERE excludes every real neighbor
// (applyMatch): both cases keep the source binding and null the newly introduced
// variables, the correct Cypher OPTIONAL MATCH semantics.
func nullBindNewVars(b binding, patterns []PathPattern) binding {
	nb := cloneBinding(b)
	for _, p := range patterns {
		for _, n := range p.Nodes {
			if n.Variable == "" {
				continue
			}
			if _, has := nb[n.Variable]; !has {
				nb[n.Variable] = (*nodeRef)(nil)
			}
		}
		for _, r := range p.Rels {
			if r.Variable == "" {
				continue
			}
			if _, has := nb[r.Variable]; !has {
				nb[r.Variable] = (*nodeRef)(nil)
			}
		}
	}
	return nb
}

// matchPatterns expands a binding across all comma-separated patterns in a
// single MATCH/OPTIONAL MATCH clause. For OPTIONAL MATCH that yields zero
// expansions, the original binding is preserved with null assignments for
// any newly introduced variables.
func (ex *executor) matchPatterns(b binding, patterns []PathPattern, optional bool) ([]binding, error) {
	current := []binding{b}
	for _, p := range patterns {
		var next []binding
		for _, cb := range current {
			expansions, err := ex.matchPath(cb, p)
			if err != nil {
				return nil, err
			}
			if len(expansions) == 0 && optional {
				// Null-bind every new variable introduced by this path.
				next = append(next, nullBindNewVars(cb, []PathPattern{p}))
			} else {
				next = append(next, expansions...)
			}
			if err := ex.checkBindings(len(next)); err != nil {
				return nil, err
			}
		}
		current = next
	}
	return current, nil
}

// matchPath expands binding b across one PathPattern. Returns zero or more
// new bindings — one per matched path.
func (ex *executor) matchPath(b binding, p PathPattern) ([]binding, error) {
	if len(p.Nodes) == 0 {
		return []binding{b}, nil
	}

	// First node: either bound (existing variable) or seed by scan.
	first := p.Nodes[0]
	var heads []binding
	if first.Variable != "" {
		if existing, ok := b[first.Variable]; ok {
			ref, _ := existing.(*nodeRef)
			if ref == nil {
				// Null binding cannot extend.
				return nil, nil
			}
			if !ex.nodeMatches(ref, first) {
				return nil, nil
			}
			if propsMatchErr := ex.checkProps(ref, first); propsMatchErr != nil {
				return nil, propsMatchErr
			}
			ok, err := ex.propsAllMatch(b, ref, first)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, nil
			}
			heads = []binding{b}
		}
	}
	if heads == nil {
		// Need to seed: scan Core KV for nodes matching label + props.
		seeds, err := ex.seedNodes(b, first)
		if err != nil {
			return nil, err
		}
		for _, s := range seeds {
			nb := cloneBinding(b)
			if first.Variable != "" {
				nb[first.Variable] = s
			}
			heads = append(heads, nb)
			if err := ex.checkBindings(len(heads)); err != nil {
				return nil, err
			}
		}
	}

	// Walk relationships.
	for i, rel := range p.Rels {
		toNode := p.Nodes[i+1]
		var next []binding
		for _, h := range heads {
			fromRef := ex.currentNode(h, p.Nodes[i])
			if fromRef == nil {
				continue
			}
			reached, err := ex.traverseRel(h, fromRef, rel, toNode)
			if err != nil {
				return nil, err
			}
			next = append(next, reached...)
			if err := ex.checkBindings(len(next)); err != nil {
				return nil, err
			}
		}
		heads = next
	}
	return heads, nil
}

// currentNode resolves the *nodeRef bound to nodePattern's variable (after
// seeding). For unnamed pattern nodes we fall back to the rel's "from" side
// in traverseRel, so this returns nil only when the variable name doesn't
// resolve.
func (ex *executor) currentNode(b binding, n NodePattern) *nodeRef {
	if n.Variable == "" {
		return nil
	}
	r, _ := b[n.Variable].(*nodeRef)
	return r
}

// nodeMatches checks label match. A pattern label IS the Contract #1 vertex key
// type: it matches iff the node's key parses as `vtx.<type>.<id>` and `<type>`
// equals the label, OR — when the pattern carries the `*` taxonomy-expansion
// sigil (NodePattern.LabelExpand) — `<type>` is a member of the label's
// resolved downward closure (dynamic-type-taxonomy-design.md §5.1 site 1).
// Fine-grained classification lives in the body's `class` field (Contract #1
// §1) and is matched with a property predicate — `MATCH (n {class:
// "service.laundry.template"})` — never with a label. A polymorphic TYPE
// question is the label's job, not the body's: `(l:location*)` expands against
// the declared taxonomy to the concrete key types, which is what a body
// predicate cannot do at all now that a location's class is its own key type.
//
// This is the reading of a label that every other mechanism already applies,
// and each of them depends on the binder agreeing: the labeled seed scan
// (seedNodes), event seeding (seedAnchorBinds), anchor retraction and the D2
// seeding eligibility the pipeline reads off it (anchor_delete.go's
// AnchorLabel), and the narrowing derivation (ReferencedLabels, consumed by the
// plain reproject gate, the client relevance gate and the actor-aware narrowed
// filter). A binder admitting a second resolution would let those narrow on a
// label set the executor does not honor — on the auth plane, a grant that never
// updates and never retracts.
//
// The lookup is keyed on the PATTERN's own LabelExpand flag, never on the
// label string alone: a query may bind both `(a:location)` and
// `(b:location*)`, and only the second ever consults LabelExpansion. A
// LabelExpand pattern whose label has no entry in LabelExpansion (the
// expansion was never resolved for it) matches nothing — fail closed, never
// fall back to the bare-label reading.
//
// Returns true when the pattern label is empty.
func (ex *executor) nodeMatches(ref *nodeRef, n NodePattern) bool {
	if n.Label == "" {
		return true
	}
	if ref == nil {
		return false
	}
	vtype, _, ok := substrate.ParseVertexKey(ref.key)
	if !ok {
		return false
	}
	if n.LabelExpand {
		set, hasSet := ex.labelExpansion[n.Label]
		if !hasSet {
			return false
		}
		_, hit := set[vtype]
		return hit
	}
	return vtype == n.Label
}

// checkProps is a thin alias retained for readability.
func (ex *executor) checkProps(_ *nodeRef, _ NodePattern) error { return nil }

// propsAllMatch evaluates each property predicate in n against ref.
func (ex *executor) propsAllMatch(b binding, ref *nodeRef, n NodePattern) (bool, error) {
	if len(n.Properties) == 0 {
		return true, nil
	}
	for k, vexpr := range n.Properties {
		want, err := ex.evalExpr(b, vexpr)
		if err != nil {
			return false, err
		}
		got, ok := ref.props[k]
		if !ok {
			// Try "key" alias against Core KV key itself.
			if k == "key" {
				got = ref.key
			} else {
				return false, nil
			}
		}
		if !equalsAny(got, want) {
			return false, nil
		}
	}
	return true, nil
}

// seedNodes returns all Core KV vertices matching n's label + properties.
// Three ways to build that candidate set, in order:
//
//   - a "key" property with a resolvable expression → a point lookup;
//   - an armed anchor seed (EventContext.SeedAnchor) on the anchor pattern →
//     a point lookup on the event vertex, since the caller has proved the
//     event is a mutation of that anchor and no other anchor's rows can have
//     changed with it;
//   - otherwise a listing — a labeled pattern via a server-side "vtx.<label>."
//     prefix filter bounded by that type's own vertex count, an unlabeled one
//     via the whole bucket since any type may bind it — filtered by shape,
//     label, and properties.
func (ex *executor) seedNodes(b binding, n NodePattern) ([]*nodeRef, error) {
	// Fast path: property "key" with a resolvable expression → point read.
	if keyExpr, ok := n.Properties["key"]; ok {
		val, err := ex.evalExpr(b, keyExpr)
		if err != nil {
			return nil, err
		}
		s, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("full engine: node property 'key' must resolve to string, got %T", val)
		}
		return ex.pointCandidate(b, n, s)
	}

	// Anchor seeding: take (and clear) the armed seed at the first candidate
	// set built by scan — the anchor pattern. The seedAnchorBinds re-check is
	// defensive: arming already proved this exact pattern point-seedable, and
	// a seed that somehow reached a pattern it cannot bind falls back to the
	// scan below rather than narrowing the wrong pattern.
	if seed := ex.takeSeedAnchor(); seed != "" && seedAnchorBinds(n, seed, ex.labelExpansion) {
		return ex.pointCandidate(b, n, seed)
	}

	// Generic path: list candidates, then filter by shape, label, and
	// properties. A labeled pattern only ever binds vertices of that exact
	// type, so listing is scoped to the "vtx.<label>." prefix — a
	// server-side JetStream subject filter (ListKeysPrefix) bounded by that
	// type's own vertex count — instead of every key in the bucket; an
	// unlabeled pattern can bind any type and still lists everything. A list
	// failure MUST surface, not degrade to zero seeds: downstream, the
	// filter-retraction presence check treats an anchor's absence from the
	// derived row set as authoritative and emits a Delete, so a swallowed
	// error here would retract live rows on a transient substrate blip. An
	// empty bucket/prefix returns an empty slice with no error and seeds
	// nothing.
	//
	// A LabelExpand pattern cannot be expressed as ONE prefix — an abstract
	// label's own "vtx.<label>." prefix has no instances at all (§3.2), and
	// the label's resolved closure is a SET of concrete types, not a single
	// string. So this lists once per concrete member instead. An unresolved
	// expansion (no entry in ex.labelExpansion) binds nothing, matching the
	// fail-closed posture the other three label-equality sites already take
	// — never fall back to scanning the bare (abstract) prefix, which would
	// silently seed zero candidates from every unseeded evaluation of a
	// `*`-anchored lens (boot, Rebuild, a neighbour-triggered re-execute) and
	// disagree with a seeded evaluation of the SAME lens that reaches the
	// leaf via pointCandidate above — the disagreement filter-retraction
	// reads as a mass revoke.
	var keys []string
	var err error
	switch {
	case n.LabelExpand:
		set, hasSet := ex.labelExpansion[n.Label]
		if !hasSet {
			return nil, nil
		}
		for vt := range set {
			vtKeys, lerr := ex.coreKV.ListKeysPrefix(ex.ctx, substrate.VertexPrefix+"."+vt+".")
			if lerr != nil {
				err = lerr
				break
			}
			keys = append(keys, vtKeys...)
		}
	case n.Label != "":
		keys, err = ex.coreKV.ListKeysPrefix(ex.ctx, substrate.VertexPrefix+"."+n.Label+".")
	default:
		keys, err = ex.coreKV.ListKeys(ex.ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("full engine: seed scan: %w", err)
	}
	var refs []*nodeRef
	for _, k := range keys {
		cls := substrate.ClassifyKey(k)
		if n.Label != "" {
			// The label-scoped prefix also matches 4-segment aspect keys
			// sharing the same string prefix (vtx.<label>.<id>.<localName>)
			// and, for a malformed id, a KindUnknown key that merely starts
			// with the right tokens — only a true vertex key is a candidate.
			if cls != substrate.KindVertex {
				continue
			}
		} else if cls != substrate.KindVertex && cls != substrate.KindUnknown {
			// The whole-bucket scan additionally admits KindUnknown: an
			// unlabeled pattern imposes no label constraint at all, so a key
			// whose shape is not a Contract #1 vertex key is still a
			// candidate and only the property predicates can exclude it.
			continue
		}
		ref, err := ex.fetchNode(k)
		if err != nil {
			return nil, err
		}
		if ref == nil {
			continue
		}
		if !ex.nodeMatches(ref, n) {
			continue
		}
		ok, err := ex.propsAllMatch(b, ref, n)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// pointCandidate builds the candidate set for a pattern narrowed to ONE Core
// KV key — shared by the `{key: …}` fast path and the anchor-seeded path. A
// missing or soft-deleted vertex, a key whose vertex does not carry the
// pattern's label, or a property predicate the vertex fails all yield zero
// candidates: exactly what a scan that matched nothing yields, so a narrowed
// pattern is never distinguishable from a scanned one by its result.
func (ex *executor) pointCandidate(b binding, n NodePattern, key string) ([]*nodeRef, error) {
	ref, err := ex.fetchNode(key)
	if err != nil {
		return nil, err
	}
	if ref == nil {
		return nil, nil
	}
	if !ex.nodeMatches(ref, n) {
		return nil, nil
	}
	ok, err := ex.propsAllMatch(b, ref, n)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return []*nodeRef{ref}, nil
}

// takeSeedAnchor returns the armed anchor seed and clears it, so exactly one
// pattern can consume it. Taken unconditionally by the first scan-seeded
// pattern (see executor.seedAnchor) rather than only when it applies: a seed
// that outlived its own pattern must be spent, never left armed for the next
// one.
func (ex *executor) takeSeedAnchor() string {
	seed := ex.seedAnchor
	ex.seedAnchor = ""
	return seed
}

// seedAnchorFor reports the vertex key this evaluation's anchor pattern may
// narrow to, or "" when the seed does not apply and every pattern scans as
// usual. Only the query's ANCHOR — the first MATCH clause's first node
// pattern, the same derivation AnchorProjectionKey uses — is ever seedable;
// later MATCH clauses, OPTIONAL MATCH clauses, and sibling comma-separated
// patterns keep their own candidate sets whatever the event was.
func seedAnchorFor(q *Query, seedKey string, exp map[string]map[string]struct{}) string {
	if seedKey == "" || q == nil {
		return ""
	}
	n, ok := anchorPattern(q)
	if !ok || !seedAnchorBinds(n, seedKey, exp) {
		return ""
	}
	return seedKey
}

// seedAnchorBinds reports whether one vertex key can stand in for pattern n's
// entire candidate set. Two structural conditions:
//
//   - n is labeled with the key's own Contract #1 vertex type — or, when n
//     carries the `*` taxonomy-expansion sigil, the key's type is a member
//     of the label's resolved downward closure (exp[n.Label]) — §5.1 site 2,
//     the engine half of event seeding. An unlabeled pattern binds any type
//     (so one vertex is not its candidate set), and a pattern labeled with a
//     type outside what n admits is not the event's pattern at all — a
//     neighbor-type event says nothing about which anchors changed. A
//     LabelExpand pattern whose label has no entry in exp matches nothing —
//     fail closed, never the bare-label reading.
//   - n carries no `key` property. Such a pattern already resolves to a point
//     read of its own key, which the seed must not silently displace.
func seedAnchorBinds(n NodePattern, seedKey string, exp map[string]map[string]struct{}) bool {
	if n.Label == "" {
		return false
	}
	if _, keyed := n.Properties["key"]; keyed {
		return false
	}
	vtype, _, ok := substrate.ParseVertexKey(seedKey)
	if !ok {
		return false
	}
	if n.LabelExpand {
		set, hasSet := exp[n.Label]
		if !hasSet {
			return false
		}
		_, hit := set[vtype]
		return hit
	}
	return vtype == n.Label
}

// fetchNode reads a Core KV vertex, returning nil for missing or soft-deleted.
// fetchNode reads the vertex (or aspect) at key, memoized for the life of this
// evaluation — see executor.nodes for why repeatable-read is a correctness
// requirement here. A read error is never memoized: it is transport, not a
// value, so a retry within the same evaluation may legitimately succeed.
func (ex *executor) fetchNode(key string) (*nodeRef, error) {
	if ref, ok := ex.nodes[key]; ok {
		return ref, nil
	}
	ref, err := ex.readNode(key)
	if err != nil {
		return nil, err
	}
	// The read-free key-resolution path (anchor_delete) builds an executor with
	// no memo; it resolves at most one key per call, so a nil map just means no
	// memoization rather than a second read.
	if ex.nodes != nil {
		ex.nodes[key] = ref
	}
	return ref, nil
}

// readNode performs the uncached Core KV point-read behind fetchNode.
func (ex *executor) readNode(key string) (*nodeRef, error) {
	entry, err := ex.coreKV.Get(ex.ctx, key)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("full engine: get %q: %w", key, err)
	}
	var props map[string]any
	if err := json.Unmarshal(entry.Value, &props); err != nil {
		return nil, fmt.Errorf("full engine: unmarshal %q: %w", key, err)
	}
	// A JSON "null" body unmarshals to a nil map. Treat as absent/tombstone —
	// a null-body entry is likely a corrupted or transitional write.
	if props == nil {
		return nil, nil
	}
	if deleted, _ := props["isDeleted"].(bool); deleted {
		return nil, nil
	}
	props["key"] = key
	return &nodeRef{key: key, props: props, revision: entry.Revision}, nil
}

// fetchEdges returns adjNodeID's edge list, memoized for the life of this
// evaluation — see executor.edges for why repeatable-read is a correctness
// requirement here, mirroring fetchNode/nodes for adjacency reads. A read
// error is never memoized: it is transport, not a value. Mirrors fetchNode's
// nil-map guard: the read-free key-resolution executor (anchor_delete) never
// traverses a relationship, so this only matters defensively.
func (ex *executor) fetchEdges(adjNodeID string) ([]adjacency.EdgeEntry, error) {
	if edges, ok := ex.edges[adjNodeID]; ok {
		return edges, nil
	}
	edges, revision, err := adjacency.Neighbors(ex.ctx, ex.adjKV, ex.coreKV, adjNodeID)
	if err != nil {
		return nil, err
	}
	if ex.edges != nil {
		ex.edges[adjNodeID] = edges
		ex.edgeRevisions[adjNodeID] = revision
	}
	return edges, nil
}

// recordEdgeSelector records, for adjNodeID, the (rel.Type, rel.Direction)
// selector this hop consulted against edges — the selector-scoped read-
// surface unit (§13.4) a validating caller later re-applies to a fresh read
// instead of comparing adjNodeID's whole adjacency-document revision, so a
// write to an unrelated relation on a shared hub node no longer reads as
// drift.
//
// An untyped selector (rel.Type == "") consumes every edge on the node
// regardless of type — it marks the node's entry Fallback instead of
// recording a Matched set, since whole-document revision comparison already
// covers (and is the only sound comparison for) an untyped read. Once a
// node's entry is Fallback, a later typed hop on the same node records
// nothing further: the coarser comparison already subsumes it.
//
// Called once per (node, hop-iteration) from traverseRel's per-node edge
// loop; a node revisited via the same selector later in the same evaluation
// (a variable-length walk crossing it twice, or two separate MATCH clauses)
// just unions into the same Matched set — idempotent, no special-casing
// needed.
func (ex *executor) recordEdgeSelector(adjNodeID string, rel RelPattern, edges []adjacency.EdgeEntry) {
	if ex.edgeSelectors == nil {
		return
	}
	entry := ex.edgeSelectors[adjNodeID]
	if entry == nil {
		entry = &ruleengine.EdgeSelectorFootprint{}
		ex.edgeSelectors[adjNodeID] = entry
	}
	if rel.Type == "" {
		entry.Fallback = true
		return
	}
	if entry.Fallback {
		return
	}
	sel := ruleengine.EdgeSelector{RelType: rel.Type, Direction: rel.Direction.String()}
	ids := entry.Matched[sel]
	if ids == nil {
		ids = map[string]struct{}{}
		if entry.Matched == nil {
			entry.Matched = map[ruleengine.EdgeSelector]map[string]struct{}{}
		}
		entry.Matched[sel] = ids
	}
	for _, e := range edges {
		if e.Name != rel.Type {
			continue
		}
		if !adjacency.DirectionMatches(e.Direction, rel.Direction.String()) {
			continue
		}
		ids[e.EdgeID] = struct{}{}
	}
}

// traverseRel expands one relationship hop (possibly variable-length).
func (ex *executor) traverseRel(b binding, from *nodeRef, rel RelPattern, to NodePattern) ([]binding, error) {
	minHops := rel.MinHops
	maxHops := rel.MaxHops
	if maxHops < 0 || maxHops > maxVarLengthHops {
		maxHops = maxVarLengthHops
	}
	if minHops < 0 {
		minHops = 0
	}

	type frontier struct {
		node *nodeRef
		seen map[string]struct{}
	}
	starts := []frontier{{node: from, seen: map[string]struct{}{from.key: {}}}}

	var matched []*nodeRef
	// Hop 0 means "from itself" — admit if minHops==0 and to filters allow.
	admit := func(ref *nodeRef) (bool, error) {
		if !ex.nodeMatches(ref, to) {
			return false, nil
		}
		ok, err := ex.propsAllMatch(b, ref, to)
		if err != nil {
			return false, err
		}
		return ok, nil
	}

	if minHops == 0 {
		ok, err := admit(from)
		if err != nil {
			return nil, err
		}
		if ok {
			matched = append(matched, from)
		}
	}

	current := starts
	for hop := 1; hop <= maxHops; hop++ {
		var nextFrontier []frontier
		for _, f := range current {
			// Adjacency KV is indexed by bare NodeID, not full Contract #1
			// vertex keys. When f.node.key is a Contract #1 vtx key, extract
			// the NodeID; otherwise treat the key as a bare NodeID (test /
			// legacy Materializer fixture path).
			adjLookupID := f.node.key
			if _, nodeID, ok := substrate.ParseVertexKey(f.node.key); ok {
				adjLookupID = nodeID
			}
			edges, err := ex.fetchEdges(adjLookupID)
			if err != nil {
				return nil, fmt.Errorf("full engine: neighbors(%s): %w", adjLookupID, err)
			}
			ex.recordEdgeSelector(adjLookupID, rel, edges)
			for _, e := range edges {
				if rel.Type != "" && e.Name != rel.Type {
					continue
				}
				if !adjacency.DirectionMatches(e.Direction, rel.Direction.String()) {
					continue
				}
				// Reconstruct the OTHER endpoint's Core KV key. If the edge
				// carries OtherType (Contract #1 link convention), build the
				// full vtx key; otherwise the OtherNodeID itself is the
				// Core KV key (Materializer-style fixture path).
				otherCoreKey := e.OtherNodeID
				if e.OtherType != "" {
					otherCoreKey = substrate.VertexPrefix + "." + e.OtherType + "." + e.OtherNodeID
				}
				if _, seen := f.seen[otherCoreKey]; seen {
					continue
				}
				neighbor, err := ex.fetchNode(otherCoreKey)
				if err != nil {
					return nil, err
				}
				if neighbor == nil {
					continue
				}
				if hop >= minHops {
					ok, err := admit(neighbor)
					if err != nil {
						return nil, err
					}
					if ok {
						matched = append(matched, neighbor)
					}
				}
				// Extend frontier for next hop.
				ns := make(map[string]struct{}, len(f.seen)+1)
				for k := range f.seen {
					ns[k] = struct{}{}
				}
				ns[neighbor.key] = struct{}{}
				nextFrontier = append(nextFrontier, frontier{node: neighbor, seen: ns})
			}
		}
		current = nextFrontier
		if len(current) == 0 {
			break
		}
	}

	// Deduplicate matched by key — same target reachable via multiple paths.
	seen := map[string]bool{}
	var unique []*nodeRef
	for _, m := range matched {
		if seen[m.key] {
			continue
		}
		seen[m.key] = true
		unique = append(unique, m)
	}

	out := make([]binding, 0, len(unique))
	for _, n := range unique {
		// If the destination variable is already bound in this binding, the
		// traversal must arrive at the same node (constrained-target case,
		// e.g. `(report)<-[:reportsTo]-(identity)` where identity is already
		// bound from a prior clause).
		if to.Variable != "" {
			if existing, ok := b[to.Variable]; ok {
				ex, _ := existing.(*nodeRef)
				if ex == nil || ex.key != n.key {
					continue
				}
			}
		}
		nb := cloneBinding(b)
		if to.Variable != "" {
			nb[to.Variable] = n
		}
		out = append(out, nb)
	}
	return out, nil
}

// --- WITH ---

func (ex *executor) applyWith(bindings []binding, w *With) ([]binding, error) {
	projected, err := ex.projectItems(bindings, w.Items)
	if err != nil {
		return nil, err
	}
	if w.Where != nil {
		var filtered []binding
		for _, b := range projected {
			v, err := ex.evalExpr(b, w.Where)
			if err != nil {
				return nil, err
			}
			if truthy(v) {
				filtered = append(filtered, b)
			}
		}
		projected = filtered
	}
	return projected, nil
}

// projectItems evaluates each ProjectionItem against the inbound bindings.
// If any item is an aggregating expression (e.g. collect), the result is
// grouped by the non-aggregating items.
func (ex *executor) projectItems(bindings []binding, items []ProjectionItem) ([]binding, error) {
	// Decide aggregating vs non-aggregating per item.
	itemAggregating := make([]bool, len(items))
	anyAggregating := false
	for i, it := range items {
		if containsAggregator(it.Expr) {
			itemAggregating[i] = true
			anyAggregating = true
		}
	}
	itemAlias := func(i int) string {
		if items[i].Alias != "" {
			return items[i].Alias
		}
		return projectionAutoAlias(items[i].Expr, i)
	}

	if !anyAggregating {
		out := make([]binding, 0, len(bindings))
		for _, b := range bindings {
			if err := ex.checkCancelled(); err != nil {
				return nil, err
			}
			nb := binding{}
			for i, it := range items {
				v, err := ex.evalExpr(b, it.Expr)
				if err != nil {
					return nil, err
				}
				nb[itemAlias(i)] = v
			}
			out = append(out, nb)
		}
		return out, nil
	}

	// Group: compute the grouping key per row, folding each aggregating item
	// into its group as the row is visited. Only the running result is held —
	// the rows' argument values are never accumulated, so a count() or max()
	// over a large MATCH costs a scalar per group rather than one boxed value
	// per binding row.
	type groupAcc struct {
		row  binding
		aggs []aggFold // indexed by item; nil for a non-aggregating item
	}
	groups := map[string]*groupAcc{}
	var order []string
	for _, b := range bindings {
		if err := ex.checkCancelled(); err != nil {
			return nil, err
		}
		// Build grouping key
		keyParts := make([]string, 0, len(items))
		groupVals := map[int]any{}
		for i, it := range items {
			if itemAggregating[i] {
				continue
			}
			v, err := ex.evalExpr(b, it.Expr)
			if err != nil {
				return nil, err
			}
			groupVals[i] = v
			keyParts = append(keyParts, fmt.Sprintf("%d=%v", i, normalizeForKey(v)))
		}
		k := strings.Join(keyParts, "|")
		g, ok := groups[k]
		if !ok {
			g = &groupAcc{row: binding{}, aggs: make([]aggFold, len(items))}
			for i, v := range groupVals {
				g.row[itemAlias(i)] = v
			}
			for i, it := range items {
				if !itemAggregating[i] {
					continue
				}
				f, err := newAggFold(it.Expr)
				if err != nil {
					return nil, err
				}
				g.aggs[i] = f
			}
			groups[k] = g
			order = append(order, k)
		}
		for i := range items {
			if !itemAggregating[i] {
				continue
			}
			if err := g.aggs[i].add(ex, b); err != nil {
				return nil, err
			}
		}
	}

	out := make([]binding, 0, len(order))
	for _, k := range order {
		g := groups[k]
		for i := range items {
			if !itemAggregating[i] {
				continue
			}
			v, err := g.aggs[i].value()
			if err != nil {
				return nil, err
			}
			g.row[itemAlias(i)] = v
		}
		out = append(out, g.row)
	}
	// out is empty here iff bindings was empty on entry: every binding that
	// reaches this point lands in exactly one group (the grouping loop above
	// unconditionally creates/reuses a group for each b), so a non-empty
	// bindings always yields a non-empty out. A required MATCH that bound
	// zero rows (an unanchored scan with no matching vertex) has no anchor to
	// attach an aggregate to — zero rows in must project zero rows out, never
	// a synthetic all-null row: a fabricated row's nil-derived key/anchor
	// columns are unwritable by a protected adapter (an anchor column is
	// never nil) and would suppress the target-retraction a genuinely empty
	// result set is supposed to drive. (Cypher's "collect() on empty input
	// yields []" applies to a per-anchor OPTIONAL MATCH whose neighbor set is
	// empty; that anchor's binding is preserved by applyMatch's null-binding
	// fallback and already produced a real group above — see
	// TestExec_MaxNoMatchIsNull.)
	return out, nil
}

func projectionAutoAlias(e Expr, idx int) string {
	switch x := e.(type) {
	case *VariableRef:
		return x.Name
	case *PropertyAccess:
		return x.Key
	}
	return fmt.Sprintf("_col%d", idx)
}

// containsAggregator returns true if the expression tree contains a
// recognized aggregator (collect, count, max, min).
func containsAggregator(e Expr) bool {
	found := false
	walkExprAll(e, func(x Expr) {
		if fc, ok := x.(*FunctionCall); ok {
			switch strings.ToLower(fc.Name) {
			case "collect", "count", "max", "min":
				found = true
			}
		}
	})
	return found
}

// --- RETURN ---

func (ex *executor) applyReturn(bindings []binding, r *Return) ([]ruleengine.ProjectionResult, error) {
	rows, err := ex.projectItems(bindings, r.Items)
	if err != nil {
		return nil, err
	}
	// Deduplicate rows when RETURN DISTINCT is specified; order is preserved
	// (first occurrence wins).
	//
	// Rows are compared by the same injective rendering the grouping path keys
	// on, NOT by json.Marshal. A node-valued column holds a *nodeRef, whose
	// fields are all unexported, so it marshals to `{}` — two rows differing
	// only in which node they bound would render identically and collapse into
	// one. normalizeForKey renders a node as its key, and every leaf carries a
	// type tag, so distinct rows stay distinct. It also cannot fail, which
	// retires a dropped marshal error on a path that has no way to report one.
	if r.Distinct {
		seen := make(map[string]struct{}, len(rows))
		deduped := rows[:0]
		for _, row := range rows {
			key := normalizeForKey(map[string]any(row))
			if _, exists := seen[key]; !exists {
				seen[key] = struct{}{}
				deduped = append(deduped, row)
			}
		}
		rows = deduped
	}
	out := make([]ruleengine.ProjectionResult, 0, len(rows))
	for _, row := range rows {
		values := map[string]any{}
		for k, v := range row {
			values[k] = v
		}
		// Build the projection key. When the rule's key columns are threaded
		// (from Rule.Into.Key at activation), build the complete multi-column
		// key — every key column is a RETURN alias, so its value is in the
		// projected row. This mirrors the simple engine and lets a composite-key
		// lens (e.g. a GrantTable lens keyed on actor_id/anchor_id/grant_source)
		// hand the adapter every key column it requires. With no key columns
		// threaded, fall back to the first RETURN item — byte-identical to the
		// single-key lenses that have always used this path.
		keyMap := map[string]any{}
		if len(ex.keyColumns) > 0 {
			for _, col := range ex.keyColumns {
				keyMap[col] = values[col]
			}
		} else if len(r.Items) > 0 {
			alias := r.Items[0].Alias
			if alias == "" {
				alias = projectionAutoAlias(r.Items[0].Expr, 0)
			}
			keyMap[alias] = values[alias]
		}
		out = append(out, ruleengine.ProjectionResult{Key: keyMap, Values: values})
	}
	return out, nil
}

// --- expression evaluation ---

func (ex *executor) evalExpr(b binding, e Expr) (any, error) {
	switch x := e.(type) {
	case nil:
		return nil, nil
	case *Literal:
		return x.Value, nil
	case *ParameterRef:
		if ex.params == nil {
			return nil, &ruleengine.MissingParameterError{Name: x.Name}
		}
		v, ok := ex.params[x.Name]
		if !ok {
			return nil, &ruleengine.MissingParameterError{Name: x.Name}
		}
		return v, nil
	case *VariableRef:
		if v, ok := b[x.Name]; ok {
			return v, nil
		}
		return nil, nil
	case *PropertyAccess:
		target, err := ex.evalExpr(b, x.Target)
		if err != nil {
			return nil, err
		}
		return ex.resolveProperty(target, x.Key)
	case *BinaryOp:
		l, err := ex.evalExpr(b, x.Left)
		if err != nil {
			return nil, err
		}
		r, err := ex.evalExpr(b, x.Right)
		if err != nil {
			return nil, err
		}
		return evalBinary(x.Op, l, r)
	case *AndOr:
		if x.Op == "AND" {
			for _, op := range x.Operands {
				v, err := ex.evalExpr(b, op)
				if err != nil {
					return nil, err
				}
				if !truthy(v) {
					return false, nil
				}
			}
			return true, nil
		}
		if x.Op == "XOR" {
			trueCount := 0
			for _, op := range x.Operands {
				v, err := ex.evalExpr(b, op)
				if err != nil {
					return nil, err
				}
				if truthy(v) {
					trueCount++
				}
			}
			return trueCount == 1, nil
		}
		// OR
		for _, op := range x.Operands {
			v, err := ex.evalExpr(b, op)
			if err != nil {
				return nil, err
			}
			if truthy(v) {
				return true, nil
			}
		}
		return false, nil
	case *Not:
		// Anti-pattern: NOT (path) — evaluate as existence predicate.
		if pe, ok := x.Operand.(*PatternExpr); ok {
			exists, err := ex.existsAsPredicate(b, pe.Pattern)
			if err != nil {
				return nil, err
			}
			return !exists, nil
		}
		v, err := ex.evalExpr(b, x.Operand)
		if err != nil {
			return nil, err
		}
		return !truthy(v), nil
	case *PatternExpr:
		return ex.existsAsPredicate(b, x.Pattern)
	case *FunctionCall:
		return ex.evalFunctionCall(b, x)
	case *MapLiteral:
		out := make(map[string]any, len(x.Keys))
		for _, k := range x.Keys {
			v, err := ex.evalExpr(b, x.Values[k])
			if err != nil {
				return nil, err
			}
			out[k] = v
		}
		return out, nil
	case *ListLiteral:
		out := make([]any, 0, len(x.Elements))
		for _, el := range x.Elements {
			v, err := ex.evalExpr(b, el)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case *PatternComprehension:
		return ex.evalPatternComprehension(b, x)
	case *CaseExpr:
		for _, alt := range x.Alternatives {
			cond, err := ex.evalExpr(b, alt.When)
			if err != nil {
				return nil, err
			}
			if truthy(cond) {
				return ex.evalExpr(b, alt.Then)
			}
		}
		if x.Else != nil {
			return ex.evalExpr(b, x.Else)
		}
		return nil, nil
	}
	return nil, fmt.Errorf("full engine: unsupported expression %T", e)
}

func (ex *executor) evalFunctionCall(b binding, fc *FunctionCall) (any, error) {
	// During projection without grouping, collect()/count() are evaluated
	// row-locally by projectItems → the per-row aggregate fold. Outside that path
	// (e.g. inside another expression) treat collect as a no-op wrapper that
	// returns the single arg's value wrapped in a list.
	name := strings.ToLower(fc.Name)
	switch name {
	case "collect":
		if len(fc.Args) == 0 {
			return []any{}, nil
		}
		v, err := ex.evalExpr(b, fc.Args[0])
		if err != nil {
			return nil, err
		}
		if v == nil {
			return []any{}, nil
		}
		return []any{v}, nil
	case "count":
		return int64(1), nil
	case "max", "min":
		// Row-local (no grouping, or nested inside another expression):
		// the extreme of a single row's value is that value. Grouping goes
		// through projectItems → the per-row aggregate fold instead. max/min are
		// unary aggregators; a multi-arg call is a query error, not a silent
		// "use the first arg".
		if len(fc.Args) != 1 {
			return nil, fmt.Errorf("full engine: %s takes exactly 1 argument, got %d", name, len(fc.Args))
		}
		return ex.evalExpr(b, fc.Args[0])
	case "levenshteindist":
		// levenshteinDist(a, b) → int — classical Wagner-Fischer edit distance.
		// Pure / deterministic / O(N*M) time + O(min(N,M)) space.
		// Both args must be strings; nil args return nil.
		if len(fc.Args) != 2 {
			return nil, fmt.Errorf("full engine: levenshteinDist takes exactly 2 arguments")
		}
		av, err := ex.evalExpr(b, fc.Args[0])
		if err != nil {
			return nil, err
		}
		bv, err := ex.evalExpr(b, fc.Args[1])
		if err != nil {
			return nil, err
		}
		if av == nil || bv == nil {
			return nil, nil
		}
		as, aok := av.(string)
		bs, bok := bv.(string)
		if !aok || !bok {
			return nil, fmt.Errorf("full engine: levenshteinDist arguments must be strings, got %T and %T", av, bv)
		}
		return int64(levenshteinDistance(as, bs)), nil
	case "levenshteinratio":
		// levenshteinRatio(a, b) → float64 in [0.0, 1.0].
		// 1.0 when identical (incl. both empty); 0.0 when one is empty
		// and other is non-empty.
		if len(fc.Args) != 2 {
			return nil, fmt.Errorf("full engine: levenshteinRatio takes exactly 2 arguments")
		}
		av, err := ex.evalExpr(b, fc.Args[0])
		if err != nil {
			return nil, err
		}
		bv, err := ex.evalExpr(b, fc.Args[1])
		if err != nil {
			return nil, err
		}
		if av == nil || bv == nil {
			return nil, nil
		}
		as, aok := av.(string)
		bs, bok := bv.(string)
		if !aok || !bok {
			return nil, fmt.Errorf("full engine: levenshteinRatio arguments must be strings, got %T and %T", av, bv)
		}
		la, lb := len(as), len(bs)
		maxLen := la
		if lb > maxLen {
			maxLen = lb
		}
		if maxLen == 0 {
			return float64(1.0), nil
		}
		dist := levenshteinDistance(as, bs)
		return 1.0 - float64(dist)/float64(maxLen), nil
	case "nanoidfromkey":
		// nanoIdFromKey(vertexKey) → the bare NanoID (the <id> segment of a
		// vtx.<type>.<id> vertex key) — the §6.14 opaque-match-token anchor
		// representation for read-path authorization (D1).
		//
		// Fail-closed: only a well-formed vertex key (exactly three
		// dot-segments, leading "vtx", non-empty type + id) yields a NanoID;
		// an aspect key (vtx.<type>.<id>.<localName>), a link key (lnk.…), or
		// any malformed input ERRORS rather than emitting a wrong anchor — an
		// auth-plane lens must never project a token that could match the wrong
		// resource, so a bad shape fails the projection (deny) instead of
		// silently degrading. A nil arg returns nil (mirrors levenshtein).
		if len(fc.Args) != 1 {
			return nil, fmt.Errorf("full engine: nanoIdFromKey takes exactly 1 argument, got %d", len(fc.Args))
		}
		v, err := ex.evalExpr(b, fc.Args[0])
		if err != nil {
			return nil, err
		}
		if v == nil {
			return nil, nil
		}
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("full engine: nanoIdFromKey argument must be a string, got %T", v)
		}
		return nanoIDFromVertexKey(s)
	case "coalesce":
		// coalesce(a, b, ...) → the first argument that is not Cypher NULL.
		// The shared-anchor composition primitive (pkgmgr Walks, composeDataLensSpec):
		// each walk's OPTIONAL MATCH binds its own scoped copy of the declared anchor
		// variable, at most one non-null per row, and a WITH clause folds them back to
		// the walk-declared name via coalesce.
		if len(fc.Args) == 0 {
			return nil, fmt.Errorf("full engine: coalesce takes at least 1 argument")
		}
		for _, arg := range fc.Args {
			v, err := ex.evalExpr(b, arg)
			if err != nil {
				return nil, err
			}
			if !isNullBound(v) {
				return v, nil
			}
		}
		return nil, nil
	}
	return nil, fmt.Errorf("full engine: unsupported function %q", fc.Name)
}

// isNullBound reports whether v is Cypher NULL: a nil interface, or a node
// variable's OPTIONAL-MATCH null sentinel ((*nodeRef)(nil)) — a bare `v ==
// nil` misses the latter because a typed nil pointer boxed in `any` is
// itself a non-nil interface value.
func isNullBound(v any) bool {
	if v == nil {
		return true
	}
	if ref, ok := v.(*nodeRef); ok {
		return ref == nil
	}
	return false
}

// nanoIDFromVertexKey extracts the bare NanoID (the <id> segment) from a
// vtx.<type>.<id> vertex key, fail-closed: only a well-formed vertex key —
// exactly three dot-segments with a leading "vtx" and non-empty type + id —
// yields a NanoID. An aspect key (vtx.<type>.<id>.<localName>, four segments),
// a link key (lnk.…, six segments), or any malformed string ERRORS rather than
// returning a wrong token, so an auth-plane lens can never project an anchor
// that matches the wrong resource. (Contract #1 §1.1 vertex key shape.)
func nanoIDFromVertexKey(s string) (string, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 || parts[0] != "vtx" || parts[1] == "" || parts[2] == "" {
		return "", fmt.Errorf("full engine: nanoIdFromKey requires a vtx.<type>.<id> vertex key, got %q", s)
	}
	return parts[2], nil
}

// levenshteinDistance computes the classical Wagner-Fischer edit distance
// between strings a and b. Uses a rolling-row approach for O(min(|a|,|b|))
// space. Cost: insert=1, delete=1, substitute=1. Operates over runes so
// multi-byte UTF-8 sequences count as single characters.
func levenshteinDistance(a, b string) int {
	ra := []rune(a)
	rb := []rune(b)
	if len(ra) > len(rb) {
		ra, rb = rb, ra
	}
	n, m := len(ra), len(rb)
	if n == 0 {
		return m
	}
	prev := make([]int, n+1)
	curr := make([]int, n+1)
	for j := 0; j <= n; j++ {
		prev[j] = j
	}
	for i := 1; i <= m; i++ {
		curr[0] = i
		for j := 1; j <= n; j++ {
			cost := 1
			if rb[i-1] == ra[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			curr[j] = del
			if ins < curr[j] {
				curr[j] = ins
			}
			if sub < curr[j] {
				curr[j] = sub
			}
		}
		prev, curr = curr, prev
	}
	return prev[n]
}

// evalPatternComprehension implements `[(x)-[:t]->(y) | projection]`.
// It re-walks the pattern starting from the current binding, evaluating the
// projection expression for each match and returning the resulting list.
func (ex *executor) evalPatternComprehension(b binding, pc *PatternComprehension) (any, error) {
	matches, err := ex.matchPath(b, pc.Pattern)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(matches))
	for _, m := range matches {
		if pc.Where != nil {
			v, err := ex.evalExpr(m, pc.Where)
			if err != nil {
				return nil, err
			}
			if !truthy(v) {
				continue
			}
		}
		v, err := ex.evalExpr(m, pc.Projection)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// existsAsPredicate returns true if pattern has at least one match starting
// from the current binding. Used by NOT (path) anti-pattern WHERE.
func (ex *executor) existsAsPredicate(b binding, p PathPattern) (bool, error) {
	matches, err := ex.matchPath(b, p)
	if err != nil {
		return false, err
	}
	return len(matches) > 0, nil
}

// --- helpers ---

func cloneBinding(b binding) binding {
	nb := make(binding, len(b))
	for k, v := range b {
		nb[k] = v
	}
	return nb
}

// propertyOf resolves target.key for various target shapes (nodeRef, map,
// or nil). Returns nil for null targets and missing keys.
// resolveProperty reads property `key` off target, implementing the Lattice
// property model: vertices carry the envelope (key/class/provenance) plus link
// topology; business data lives in aspects (and, by exception, in a vertex's
// own `data` envelope — e.g. permissions).
//
// For a vertex nodeRef, a name present in the root body returns that value
// directly (envelope fields, and root `data`). A name ABSENT from the root body
// is treated as an ASPECT reference: the aspect key <nodeKey>.<key> is
// point-read and its body returned, so a lens rule navigates an aspect-stored
// field explicitly as node.<aspect>.data.<field> (e.g. role.canonicalName.data.value).
// Aspect bodies returned this way are plain maps, so any further navigation uses
// ordinary map access — only the first hop off a vertex resolves an aspect.
func (ex *executor) resolveProperty(target any, key string) (any, error) {
	nr, ok := target.(*nodeRef)
	if !ok || nr == nil {
		return propertyOf(target, key), nil
	}
	if v, present := nr.props[key]; present {
		return v, nil
	}
	if key == "key" {
		return nr.key, nil
	}
	// Absent from the root body → aspect reference: point-read <nodeKey>.<key>.
	// A nil coreKV is the read-free key-resolution mode (the anchor-tombstone
	// delete path): an aspect that would require a Core-KV read is reported
	// unresolvable rather than panicking on a re-scan of the now-deleted vertex.
	if ex.coreKV == nil {
		return nil, errCoreKVReadDisabled
	}
	aref, err := ex.fetchNode(nr.key + "." + key)
	if err != nil {
		return nil, err
	}
	if aref == nil {
		return nil, nil
	}
	return aref.props, nil
}

func propertyOf(target any, key string) any {
	switch t := target.(type) {
	case nil:
		return nil
	case *nodeRef:
		if t == nil {
			return nil
		}
		if v, ok := t.props[key]; ok {
			return v
		}
		if key == "key" {
			return t.key
		}
		return nil
	case map[string]any:
		return t[key]
	}
	return nil
}

func truthy(v any) bool {
	if v == nil {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return true
}

func evalBinary(op string, l, r any) (any, error) {
	switch op {
	case "=":
		return equalsAny(l, r), nil
	case "<>":
		return !equalsAny(l, r), nil
	case "<", ">", "<=", ">=":
		return compareAny(op, l, r)
	case "+":
		// String concat or numeric add — defer to numeric when both numeric,
		// otherwise list concat when both lists.
		if ll, ok := l.([]any); ok {
			if rr, ok := r.([]any); ok {
				out := make([]any, 0, len(ll)+len(rr))
				out = append(out, ll...)
				out = append(out, rr...)
				return out, nil
			}
		}
		return numericOp(op, l, r)
	case "-", "*", "/", "%":
		return numericOp(op, l, r)
	}
	return nil, fmt.Errorf("full engine: unsupported binary op %q", op)
}

func equalsAny(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	// Numeric coercion: int64 vs float64.
	if af, aok := toFloat(a); aok {
		if bf, bok := toFloat(b); bok {
			return af == bf
		}
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case float64:
		return x, true
	case float32:
		return float64(x), true
	}
	return 0, false
}

func compareAny(op string, l, r any) (bool, error) {
	if l == nil || r == nil {
		return false, nil
	}
	if lf, ok := toFloat(l); ok {
		if rf, ok := toFloat(r); ok {
			switch op {
			case "<":
				return lf < rf, nil
			case ">":
				return lf > rf, nil
			case "<=":
				return lf <= rf, nil
			case ">=":
				return lf >= rf, nil
			}
		}
	}
	ls, lok := l.(string)
	rs, rok := r.(string)
	if lok && rok {
		switch op {
		case "<":
			return ls < rs, nil
		case ">":
			return ls > rs, nil
		case "<=":
			return ls <= rs, nil
		case ">=":
			return ls >= rs, nil
		}
	}
	return false, nil
}

func numericOp(op string, l, r any) (any, error) {
	lf, lok := toFloat(l)
	rf, rok := toFloat(r)
	if !lok || !rok {
		return nil, fmt.Errorf("full engine: numeric op %q on non-numeric (%T, %T)", op, l, r)
	}
	switch op {
	case "+":
		return lf + rf, nil
	case "-":
		return lf - rf, nil
	case "*":
		return lf * rf, nil
	case "/":
		if rf == 0 {
			return nil, errors.New("full engine: division by zero")
		}
		return lf / rf, nil
	case "%":
		if rf == 0 {
			return nil, errors.New("full engine: modulo by zero")
		}
		return float64(int64(lf) % int64(rf)), nil
	}
	return nil, fmt.Errorf("full engine: unsupported numeric op %q", op)
}

// normalizeForKey produces a stable string representation of a value, used as
// the identity basis for WITH/RETURN grouping and for DISTINCT deduplication.
// It is purely in-memory — never persisted, never compared across processes —
// so the encoding is free to change.
//
// The encoding is INJECTIVE: distinct values must never render alike. Two
// values that collide are silently merged into one group, or one is dropped
// from a DISTINCT list — data loss with no error anywhere. Free text reaches
// here (a lens collects `presentation.data.name` into a map), so a rendering
// that simply interleaved delimiters would let an authored name impersonate
// structure: `{name: "a,key:b"}` must not render as `{name:a,key:b}` does.
//
// Every leaf therefore carries a TYPE TAG, and every variable-length token is
// LENGTH-PREFIXED, which makes the rendering unambiguously parseable and hence
// injective — a string can no longer forge a separator, and `1`, `1.0`, `"1"`
// and `true` stay distinct from each other and from `"<nil>"`.
func normalizeForKey(v any) string {
	var b strings.Builder
	writeNormalizedKey(&b, v)
	return b.String()
}

// writeNormalizedKey appends v's injective rendering to b.
func writeNormalizedKey(b *strings.Builder, v any) {
	// token writes a length-prefixed, therefore self-delimiting, string.
	token := func(tag byte, s string) {
		b.WriteByte(tag)
		b.WriteString(strconv.Itoa(len(s)))
		b.WriteByte(':')
		b.WriteString(s)
	}
	switch x := v.(type) {
	case nil:
		b.WriteByte('z')
	case string:
		token('s', x)
	case bool:
		if x {
			b.WriteByte('T')
		} else {
			b.WriteByte('F')
		}
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('m')
		b.WriteString(strconv.Itoa(len(keys)))
		b.WriteByte('{')
		for _, k := range keys {
			token('k', k)
			writeNormalizedKey(b, x[k])
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('l')
		b.WriteString(strconv.Itoa(len(x)))
		b.WriteByte('[')
		for _, el := range x {
			writeNormalizedKey(b, el)
		}
		b.WriteByte(']')
	case *nodeRef:
		if x == nil {
			b.WriteByte('z')
			return
		}
		token('n', x.key)
	case int64:
		token('i', strconv.FormatInt(x, 10))
	case float64:
		token('f', strconv.FormatFloat(x, 'g', -1, 64))
	default:
		// Any other type the engine produces still renders, tagged by its Go
		// type so two different types can never share a rendering.
		token('x', fmt.Sprintf("%T=%v", v, v))
	}
}

// walkExprAll applies f to every expression node reachable from root.
// Independent of the test-only walker in parse_test.go so production code
// doesn't depend on test helpers.
func walkExprAll(root Expr, f func(Expr)) {
	if root == nil {
		return
	}
	f(root)
	switch e := root.(type) {
	case *AndOr:
		for _, op := range e.Operands {
			walkExprAll(op, f)
		}
	case *Not:
		walkExprAll(e.Operand, f)
	case *BinaryOp:
		walkExprAll(e.Left, f)
		walkExprAll(e.Right, f)
	case *PropertyAccess:
		walkExprAll(e.Target, f)
	case *FunctionCall:
		for _, a := range e.Args {
			walkExprAll(a, f)
		}
	case *MapLiteral:
		for _, k := range e.Keys {
			walkExprAll(e.Values[k], f)
		}
	case *ListLiteral:
		for _, el := range e.Elements {
			walkExprAll(el, f)
		}
	case *PatternComprehension:
		walkExprAll(e.Where, f)
		walkExprAll(e.Projection, f)
	case *CaseExpr:
		for _, alt := range e.Alternatives {
			walkExprAll(alt.When, f)
			walkExprAll(alt.Then, f)
		}
		walkExprAll(e.Else, f)
	}
}
