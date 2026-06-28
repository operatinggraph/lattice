package full

import "github.com/asolgan/lattice/internal/refractor/ruleengine"

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
//	ok == false → the event vertex is NOT this rule's anchor label (a
//	              secondary-node tombstone — the caller must re-execute so
//	              dependent rows refresh), the rule lacks a resolvable
//	              anchor/RETURN, or the lens keys its output on an aspect field
//	              absent from a root-tombstone payload (an anti-pattern). No
//	              Delete is emitted; the caller falls through to a re-execute.
//	ok == true  → keys is the Keys map to hand to a Delete EvalResult; it mirrors
//	              the upsert key map (the first RETURN item's alias → its value).
func (*Engine) AnchorDeleteResult(
	cr ruleengine.CompiledRule, eventKey, eventType string, eventProps map[string]any,
) (keys map[string]any, ok bool) {
	compiled, isFull := cr.(*CompiledRule)
	if !isFull || compiled == nil || compiled.Query == nil {
		return nil, false
	}
	q := compiled.Query

	// Anchor = the first MATCH pattern's first node. Its label discriminates an
	// anchor tombstone (retract) from a secondary-node tombstone (re-execute):
	// a provider/appointment tombstone is the anchor; a patient tombstone
	// reaching the appointment lens via forPatient is a secondary node.
	anchorVar, anchorLabel, found := anchorNode(q)
	if !found || anchorLabel == "" || eventType != anchorLabel {
		return nil, false
	}

	// Key column = the first RETURN item (the full engine's "first projection
	// item is the key" convention; see executor.applyReturn).
	first, found := firstReturnItem(q)
	if !found {
		return nil, false
	}
	alias := first.Alias
	if alias == "" {
		alias = projectionAutoAlias(first.Expr, 0)
	}

	val, resolved := resolveAnchorKeyValue(first.Expr, anchorVar, eventKey, eventProps)
	if !resolved {
		return nil, false
	}
	return map[string]any{alias: val}, true
}

// anchorNode returns the variable + label of the first MATCH clause's first
// node — the lens's anchor. ok is false when the query has no MATCH or its
// first pattern carries no node (neither occurs for a compiled lens).
func anchorNode(q *Query) (variable, label string, ok bool) {
	for _, c := range q.Clauses {
		m, isMatch := c.(*Match)
		if !isMatch {
			continue
		}
		if len(m.Patterns) == 0 || len(m.Patterns[0].Nodes) == 0 {
			return "", "", false
		}
		n := m.Patterns[0].Nodes[0]
		return n.Variable, n.Label, true
	}
	return "", "", false
}

// firstReturnItem returns the first projection item of the RETURN clause — the
// item the executor treats as the output key column.
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

// resolveAnchorKeyValue resolves the projected key value of a root-tombstone
// anchor without re-scanning Core KV. Supported shapes:
//
//   - <anchorVar>.key     → the vertex key (eventKey). Robust and
//     payload-independent — the IntoKey default and the shape of every shipped
//     plain lens (fetchNode injects props["key"] = <vertex key>).
//   - <anchorVar>.<field> → a root-body field, read from eventProps.
//
// Any other shape — an aspect access (<anchorVar>.<aspect>.data.<f>, whose
// Target is itself a PropertyAccess, not the anchor variable), a different
// variable, or a non-property expression — is unresolvable from a
// root-tombstone payload and yields ok=false so the caller falls through to a
// re-execute. Keying a read model on a mutable aspect field is already an
// anti-pattern (the key churns on every aspect edit), so that fall-through is
// correctness-preserving, not a functional loss.
func resolveAnchorKeyValue(expr Expr, anchorVar, eventKey string, eventProps map[string]any) (any, bool) {
	pa, isProp := expr.(*PropertyAccess)
	if !isProp {
		return nil, false
	}
	vr, isVar := pa.Target.(*VariableRef)
	if !isVar || vr.Name != anchorVar {
		return nil, false
	}
	if pa.Key == "key" {
		return eventKey, true
	}
	if eventProps != nil {
		if v, present := eventProps[pa.Key]; present {
			return v, true
		}
	}
	return nil, false
}
