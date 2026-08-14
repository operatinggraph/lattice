package full

import (
	"context"
	"strings"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
)

// AnchorDeleteResult reports the projection (delete) key that a now-tombstoned
// event vertex previously projected to, for a root-tombstone CDC event on a
// plain (non-actor-aware) projection lens. It mirrors the simple engine's
// deleteResult and the actor-aware pipeline's tombstone shortcut: a soft-deleted
// anchor must retract the row it projected, which the upsert-only full-engine
// re-scan path otherwise leaves stale (the scan returns zero rows for the
// tombstoned anchor but never a Delete).
//
// eventKey/eventType/eventProps describe the tombstoned vertex (the CDC event):
// eventKey is its Core KV key, eventType its Contract #1 vertex type, eventProps
// its stored root body.
//
// The delete key is resolved over EVERY key column (the rule's threaded
// Into.Key, mirroring the upsert path; the legacy single first-RETURN-item key
// when no columns are threaded), evaluated against a read-free binding of the
// tombstoned anchor — so a composite-key lens (e.g. a GrantTable lens keyed on
// (actor_id, anchor_id, grant_source)) retracts the exact row it projected, and
// a function-call key like nanoIdFromKey(identity.key) resolves identically to
// the upsert path with no re-scan of the now-deleted vertex.
//
//	ok == false → the event vertex is NOT this rule's anchor label (a
//	              secondary-node tombstone — the caller must re-execute so
//	              dependent rows refresh), the rule lacks a resolvable
//	              anchor/RETURN, or some key column cannot be resolved without a
//	              Core-KV read (e.g. an aspect field absent from a root-tombstone
//	              payload — an anti-pattern) or resolves to a node rather than a
//	              scalar. No Delete is emitted; the caller falls through to a
//	              re-execute.
//	ok == true  → keys is the complete Keys map to hand to a Delete EvalResult,
//	              mirroring the upsert key map (every key column → its value).
func (e *Engine) AnchorDeleteResult(
	cr ruleengine.CompiledRule, eventKey, eventType string, eventProps map[string]any,
) (keys map[string]any, ok bool) {
	return e.AnchorProjectionKey(cr, eventKey, eventType, eventProps)
}

// AnchorProjectionKey resolves the projection key an event vertex projects to
// (or projected to), read-free from the vertex's stored root body alone. It is
// the key derivation shared by the two plain-lens retraction triggers: the
// root-tombstone Delete (AnchorDeleteResult) and the filter-retraction
// presence check (an anchor that stays alive but drops out of the matched set
// on a WHERE flip / keyed-aspect deletion / required-link removal).
//
// The ok contract is the safety keystone: ok == true iff the event vertex is
// this rule's anchor label AND every key column resolves read-free from the
// anchor binding to a scalar — which holds exactly when the lens projects at
// most one row per anchor, keyed by the anchor (the output-collision guard
// enforces ≤1 non-delete row per anchor-derived key). A neighbor-keyed or
// multi-row lens (a key column bound to a non-anchor variable, or needing a
// Core-KV read) returns ok == false, so a caller can never derive — and never
// delete — a key it cannot prove is the anchor's single row.
//
// The contract has two halves, and only the second needs the event: the
// structural half (no WITH, a labeled anchor, every key column anchor-only) is
// answered by anchorProjectionShape, which a caller holding a compiled rule
// alone can ask directly (HasAnchorOnlyKeyColumns).
func (*Engine) AnchorProjectionKey(
	cr ruleengine.CompiledRule, eventKey, eventType string, eventProps map[string]any,
) (keys map[string]any, ok bool) {
	compiled, isFull := cr.(*CompiledRule)
	if !isFull {
		return nil, false
	}
	shape, structural := compiled.anchorProjectionShape()
	if !structural {
		return nil, false
	}

	// The anchor's label discriminates an anchor tombstone (retract) from a
	// secondary-node tombstone (re-execute): a provider/appointment tombstone
	// is the anchor; a patient tombstone reaching the appointment lens via
	// forPatient is a secondary node.
	//
	// When the anchor pattern carries the `*` taxonomy-expansion sigil, the
	// discriminator is set membership against the resolved downward closure
	// instead of string equality — §5.1 site 3, anchor retraction, the most
	// dangerous of the four: left as equality, an abstract-anchored lens
	// whose anchor tombstones as a leaf type never matches anchorLabel, so
	// the retraction never fires and the row goes stale — a grant that never
	// revokes, on a grant-producing lens.
	if shape.anchorExpand {
		if _, hit := shape.expandedSet[eventType]; !hit {
			return nil, false
		}
	} else if eventType != shape.anchorLabel {
		return nil, false
	}

	// A read-free executor binding the anchor var to its tombstoned vertex. A nil
	// coreKV makes any key expression that needs an aspect point-read report
	// unresolvable (errCoreKVReadDisabled) instead of re-scanning the now-deleted
	// vertex; every other shape (literal, anchor .key / root field, pure function
	// over them — e.g. nanoIdFromKey) resolves exactly as the upsert path does.
	ex := &executor{ctx: context.Background()}
	b := binding{shape.anchorVar: &nodeRef{key: eventKey, props: eventProps}}

	out := make(map[string]any, len(shape.cols))
	for _, col := range shape.cols {
		v, err := ex.evalExpr(b, shape.keyExprs[col])
		if err != nil {
			// Needs a Core-KV read (aspect access) or otherwise unresolvable —
			// conservative fall-through to a re-execute, never a wrong Delete.
			return nil, false
		}
		if _, isNode := v.(*nodeRef); isNode {
			// A bare node variable is not a scalar key value (the upsert path would
			// project a degenerate key) — fall through.
			return nil, false
		}
		if v == nil {
			// A nil key value (e.g. an unset root field) addresses no
			// derivable row — its upserts were equally degenerate, and a
			// Delete on a nil-valued key is adapter-rendering-dependent.
			// Fall through rather than emit an ambiguous key.
			return nil, false
		}
		out[col] = v
	}
	if len(out) == 0 {
		// Defensive: an empty key map must never become a Delete predicate.
		// Unreachable today (cols always resolves to ≥1 column), but the
		// blast radius of an unqualified delete warrants the guard.
		return nil, false
	}
	return out, true
}

// anchorProjectionShape is the half of AnchorProjectionKey's ok contract that
// is decidable from the compiled rule alone: no WITH clause, a labeled anchor
// pattern (carrying a resolved expansion set when it bears the `*` sigil), and
// every key column a RETURN alias whose expression references no variable but
// the anchor's. It carries the anchor descriptors and the resolved key-column
// expressions on, so the per-event half evaluates exactly what the structural
// half admitted.
type anchorProjectionShape struct {
	// anchorVar and anchorLabel are the anchor pattern's variable and label —
	// the binding a key column may reference, and the type an event must carry
	// to BE this anchor.
	anchorVar   string
	anchorLabel string
	// anchorExpand is the `*` taxonomy-expansion sigil on the anchor pattern,
	// and expandedSet the resolved downward closure LabelExpansion holds for
	// anchorLabel. They travel together because the sigil alone decides which
	// reading applies: an expanding anchor is answered by set membership even
	// when its resolved set is empty, never by falling back to string equality
	// against the abstract label, which would admit an event no leaf-typed
	// anchor can ever be.
	anchorExpand bool
	expandedSet  map[string]struct{}
	// cols are this rule's key columns — the threaded Into.Key composite, else
	// the legacy first-RETURN-item alias — and keyExprs the RETURN expression
	// producing each one.
	cols     []string
	keyExprs map[string]Expr
}

// anchorProjectionShape resolves the structural half of the ok contract, or
// reports false when the rule cannot satisfy it under ANY event.
func (cr *CompiledRule) anchorProjectionShape() (anchorProjectionShape, bool) {
	if cr == nil || cr.Query == nil {
		return anchorProjectionShape{}, false
	}
	q := cr.Query

	// A WITH clause can re-project or re-bind variables (`WITH y AS u`), so a
	// RETURN expression's variable NAME no longer proves it binds the anchor —
	// the name-based scope check below would be defeated. No live plain lens
	// uses WITH (the WITH lenses are actor-aggregates, excluded upstream);
	// reject wholesale rather than model re-binding.
	for _, c := range q.Clauses {
		if _, isWith := c.(*With); isWith {
			return anchorProjectionShape{}, false
		}
	}

	// Anchor = the first MATCH pattern's first node.
	anchorVar, anchorLabel, anchorExpand, found := anchorNode(q)
	if !found || anchorLabel == "" {
		return anchorProjectionShape{}, false
	}
	// A LabelExpand anchor whose label has no entry in LabelExpansion refuses
	// (fail closed) rather than falling back to the bare-label reading.
	var expandedSet map[string]struct{}
	if anchorExpand {
		set, hasSet := cr.LabelExpansion[anchorLabel]
		if !hasSet {
			return anchorProjectionShape{}, false
		}
		expandedSet = set
	}

	// Key columns: the threaded Into.Key (multi-column composite), else the
	// legacy first-RETURN-item alias (single-key behaviour, unchanged for any
	// un-threaded caller). Mirrors applyReturn's key construction.
	cols := cr.KeyColumns
	if len(cols) == 0 {
		first, ok := firstReturnItem(q)
		if !ok {
			return anchorProjectionShape{}, false
		}
		alias := first.Alias
		if alias == "" {
			alias = projectionAutoAlias(first.Expr, 0)
		}
		cols = []string{alias}
	}

	exprByAlias := returnExprByAlias(q)
	keyExprs := make(map[string]Expr, len(cols))
	for _, col := range cols {
		expr, present := exprByAlias[col]
		if !present {
			// A key column that is not a RETURN alias is an anti-pattern caught at
			// activation; defensively fall through rather than emit a partial key.
			return anchorProjectionShape{}, false
		}
		if !exprReferencesOnlyVariable(expr, anchorVar) {
			// A key column bound to a NON-anchor variable (a neighbor-keyed /
			// multi-row lens, e.g. landlord_id off a manages walk) is not
			// derivable from the anchor alone. The evaluator would silently
			// resolve the unbound variable to nil (the OPTIONAL-MATCH
			// contract) and yield a WRONG partial key, so reject
			// structurally before evaluating.
			return anchorProjectionShape{}, false
		}
		keyExprs[col] = expr
	}

	return anchorProjectionShape{
		anchorVar:    anchorVar,
		anchorLabel:  anchorLabel,
		anchorExpand: anchorExpand,
		expandedSet:  expandedSet,
		cols:         cols,
		keyExprs:     keyExprs,
	}, true
}

// HasAnchorOnlyKeyColumns reports whether this rule keys its output on its
// anchor alone: no WITH clause, a labeled anchor pattern, and every key column
// an expression over that anchor's binding and nothing else
// (exprReferencesOnlyVariable, which refuses aggregates and every traversal
// form). That is per-anchor closure — the lens projects at most one row per
// anchor, keyed by the anchor — asked of the compiled rule alone, with no
// event to bind.
//
// It is AnchorProjectionKey's own structural half, shared rather than
// restated, so the two can never answer the closure question differently. It
// is deliberately WEAKER than a full AnchorProjectionKey call: whether a
// particular event's key columns EVALUATE read-free to non-nil scalars is a
// property of that event's stored body, not of the lens, and a caller asking
// per-lens (the plain arm's narrowing licence,
// plain-lens-neighbour-anchor-derivation-design.md §5.1) has no event to
// evaluate against. A caller that needs the key itself must still call
// AnchorProjectionKey and honour its ok.
func (cr *CompiledRule) HasAnchorOnlyKeyColumns() bool {
	_, ok := cr.anchorProjectionShape()
	return ok
}

// ProjectsOneRowPerAnchor reports whether this rule's output rows PARTITION by
// anchor: every output row belongs to exactly one anchor, and every value in it
// is computed from that anchor's own bindings.
//
// It is HasAnchorOnlyKeyColumns plus the conjunct closure alone does not carry
// — that a key column IDENTIFIES which anchor the row is for. The second half
// is what a per-anchor re-evaluation actually depends on: the executor groups a
// projection by its non-aggregating items (projectItems), and the key columns
// are part of every row's grouping key. A key unique per anchor confines each
// group to ONE root binding, so an aggregate inside that row
// (collect/count/max/min) spans only that anchor's own matches. A key that is
// NOT unique per anchor — a literal, or a non-identifying property such as a
// name or a status — merges several roots into one group, and an evaluation
// seeded at a single anchor computes that row from a fraction of its inputs: a
// TRUNCATED row, silently narrower than the whole-corpus evaluation's.
//
// Identity is read syntactically and conservatively: a key column identifies
// the anchor when it is the anchor's own Contract #1 key (`anchor.key`), or
// nanoIdFromKey over it — the engine's one key-preserving derivation
// (evalFunctionCall), and the shape the grant-table lenses key on. Every other
// function the engine has is either an aggregate or lossy (levenshteinDist,
// levenshteinRatio, type), so admitting a call by its argument alone would call
// a distance a key. A new injective key derivation must be named here to be
// recognized; until it is, a lens using it is refused, which costs breadth and
// never soundness.
//
// This is the WRITE licence's closure conjunct
// (plain-lens-neighbour-anchor-derivation-design.md §5.1). It is deliberately
// stronger than AnchorProjectionKey's ok contract, which answers a different
// question — "can this event's row key be derived read-free" — for a retraction
// already scoped to a single anchor by its caller, and so needing no claim
// about how rows group.
func (cr *CompiledRule) ProjectsOneRowPerAnchor() bool {
	shape, ok := cr.anchorProjectionShape()
	if !ok {
		return false
	}
	for _, col := range shape.cols {
		if exprIdentifiesVariable(shape.keyExprs[col], shape.anchorVar) {
			return true
		}
	}
	return false
}

// exprIdentifiesVariable reports whether e resolves to a value unique to the
// vertex bound to v — v's own Contract #1 key, or nanoIdFromKey over it (which
// strips the `vtx.<type>.` prefix and stays unique within a type). Every other
// shape reports false: an unrecognized future node type, a lossy function, and
// a property that merely happens to be distinct in today's data alike.
func exprIdentifiesVariable(e Expr, v string) bool {
	switch x := e.(type) {
	case *PropertyAccess:
		ref, isVar := x.Target.(*VariableRef)
		return isVar && ref.Name == v && x.Key == "key"
	case *FunctionCall:
		if strings.ToLower(x.Name) != "nanoidfromkey" {
			return false
		}
		for _, a := range x.Args {
			if exprIdentifiesVariable(a, v) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// exprReferencesOnlyVariable reports whether every variable an expression
// references is the given one — the structural precondition for resolving a
// key column read-free from the anchor binding alone. Pattern forms
// (existence tests, comprehensions) always require traversal, so they are
// never anchor-only. Conservative by construction: an unrecognized future
// node type reports false (fall through to linger, never a wrong Delete).
func exprReferencesOnlyVariable(e Expr, allowed string) bool {
	switch x := e.(type) {
	case nil:
		return true
	case *Literal:
		return true
	case *ParameterRef:
		// Parameters resolve from the executor's param map, not a variable
		// binding; the read-free executor carries none, so evaluation
		// surfaces MissingParameterError and the caller falls through.
		return true
	case *VariableRef:
		return x.Name == allowed
	case *PropertyAccess:
		return exprReferencesOnlyVariable(x.Target, allowed)
	case *BinaryOp:
		return exprReferencesOnlyVariable(x.Left, allowed) && exprReferencesOnlyVariable(x.Right, allowed)
	case *AndOr:
		for _, op := range x.Operands {
			if !exprReferencesOnlyVariable(op, allowed) {
				return false
			}
		}
		return true
	case *Not:
		return exprReferencesOnlyVariable(x.Operand, allowed)
	case *FunctionCall:
		switch strings.ToLower(x.Name) {
		case "collect", "count", "max", "min":
			// An aggregator's value depends on the grouped row set, which the
			// read-free single-anchor binding fabricates (collect → [v],
			// count → 1) — the one-row-per-anchor premise cannot hold for an
			// aggregate key. Never derivable.
			return false
		}
		for _, a := range x.Args {
			if !exprReferencesOnlyVariable(a, allowed) {
				return false
			}
		}
		return true
	case *MapLiteral:
		for _, v := range x.Values {
			if !exprReferencesOnlyVariable(v, allowed) {
				return false
			}
		}
		return true
	case *ListLiteral:
		for _, el := range x.Elements {
			if !exprReferencesOnlyVariable(el, allowed) {
				return false
			}
		}
		return true
	case *CaseExpr:
		for _, alt := range x.Alternatives {
			if !exprReferencesOnlyVariable(alt.When, allowed) || !exprReferencesOnlyVariable(alt.Then, allowed) {
				return false
			}
		}
		return exprReferencesOnlyVariable(x.Else, allowed)
	default:
		// PatternExpr, PatternComprehension, and any future node: traversal-
		// dependent or unknown — not derivable from the anchor binding.
		return false
	}
}

// AnchorLabel reports the vertex type of this rule's anchor — the label on the
// first MATCH clause's first node pattern. It is the same derivation
// AnchorProjectionKey/AnchorDeleteResult use to decide whether an event vertex
// IS this rule's anchor, exposed for a caller that needs the label alone: the
// pipeline's event-seeding eligibility (refractor-footprint-reduction-design.md
// §D2) arms seeding only for events on this type.
//
// ok is false when the query has no MATCH clause, its first pattern carries no
// node, or the anchor pattern is unlabeled — an unlabeled anchor binds any
// vertex type, so no event type identifies it.
func (cr *CompiledRule) AnchorLabel() (string, bool) {
	if cr == nil || cr.Query == nil {
		return "", false
	}
	n, found := anchorPattern(cr.Query)
	if !found || n.Label == "" {
		return "", false
	}
	return n.Label, true
}

// AnchorLabelExpand reports whether the anchor pattern AnchorLabel names
// carries the `*` taxonomy-expansion sigil (NodePattern.LabelExpand) — the
// signal useFullEngineBranches needs to decide whether
// ruleState.seedAnchorLabels holds the bare anchor label alone or its
// resolved downward closure. False (including when there is no anchor) is
// the correct default: a query with no `*` anywhere always takes the bare-
// label branch at every call site that consults this method.
func (cr *CompiledRule) AnchorLabelExpand() bool {
	if cr == nil || cr.Query == nil {
		return false
	}
	n, found := anchorPattern(cr.Query)
	if !found {
		return false
	}
	return n.LabelExpand
}

// anchorPattern returns the first MATCH clause's first node pattern — the
// lens's anchor, and the single derivation every anchor-scoped mechanism
// shares (retraction key resolution, engine event seeding, the pipeline's
// seeding eligibility). ok is false when the query has no MATCH or its first
// pattern carries no node (neither occurs for a compiled lens).
func anchorPattern(q *Query) (NodePattern, bool) {
	for _, c := range q.Clauses {
		m, isMatch := c.(*Match)
		if !isMatch {
			continue
		}
		if len(m.Patterns) == 0 || len(m.Patterns[0].Nodes) == 0 {
			return NodePattern{}, false
		}
		return m.Patterns[0].Nodes[0], true
	}
	return NodePattern{}, false
}

// anchorNode returns the variable + label of the lens's anchor pattern, plus
// whether that label carries the `*` taxonomy-expansion sigil
// (NodePattern.LabelExpand) — the flag AnchorProjectionKey needs to decide
// between a bare-label equality check and a resolved-set membership check.
func anchorNode(q *Query) (variable, label string, expand bool, ok bool) {
	n, found := anchorPattern(q)
	if !found {
		return "", "", false, false
	}
	return n.Variable, n.Label, n.LabelExpand, true
}

// firstReturnItem returns the first projection item of the RETURN clause — the
// item the executor treats as the output key column when no key columns are
// threaded.
func firstReturnItem(q *Query) (ProjectionItem, bool) {
	for _, c := range q.Clauses {
		r, isReturn := c.(*Return)
		if !isReturn {
			continue
		}
		if len(r.Items) == 0 {
			return ProjectionItem{}, false
		}
		return r.Items[0], true
	}
	return ProjectionItem{}, false
}

// returnExprByAlias maps each RETURN item's effective output alias (the explicit
// alias, else the auto-alias — matching applyReturn/projectItems) to its
// expression, so a key column named in Into.Key can be resolved to the
// expression that produces it.
func returnExprByAlias(q *Query) map[string]Expr {
	for _, c := range q.Clauses {
		r, isReturn := c.(*Return)
		if !isReturn {
			continue
		}
		out := make(map[string]Expr, len(r.Items))
		for i, item := range r.Items {
			alias := item.Alias
			if alias == "" {
				alias = projectionAutoAlias(item.Expr, i)
			}
			out[alias] = item.Expr
		}
		return out
	}
	return map[string]Expr{}
}
