package projection

import "github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"

// bindingSet is a small set of variable names an expression's evaluation
// depends on — the currency hasMultiBindingConjunctUnit counts in.
type bindingSet map[string]struct{}

// hasMultiBindingConjunctUnit reports whether cr's RETURN clause emits at
// least one value-tuple whose fields conjoin more than one graph binding —
// the derived, compile-time scope predicate refractor-evaluation-
// consistency-design.md §13.3 adds to needsFootprintValidation. A conjunct
// unit is one value-tuple a consumer matches as a whole: the RETURN's
// top-level non-aggregate items form ONE shared unit (U₀), and every
// MapLiteral found anywhere inside an aggregate (collect(...)) item forms
// its own unit. The verdict is true iff any unit references two or more
// distinct bindings.
//
// Defaults to VALIDATE (true), never to exempt, on every uncertain shape:
// a nil/queryless compiled rule, a query with no RETURN clause, and any
// expression form the walker does not recognise. Getting this backwards
// would silently exempt a lens from the auth-plane read-consistency check
// it needs — a combination-grant that never gets caught, not a lens that
// merely pays an avoidable validation cost. See plan.go's Compile for the
// caller-side fail-closed conjunct (not the full engine's compiled
// artifact, or the artifact assertion fails → validate as well).
func hasMultiBindingConjunctUnit(cr *full.CompiledRule) bool {
	if cr == nil || cr.Query == nil {
		return true
	}
	var returns []*full.Return
	for _, c := range cr.Query.Clauses {
		if r, ok := c.(*full.Return); ok {
			returns = append(returns, r)
		}
	}
	if len(returns) == 0 {
		return true
	}
	// Multiple RETURN branches (engine UNION, not in this grammar today):
	// classify each and OR the verdicts — an unclassifiable branch
	// validates the whole.
	verdict := false
	for _, r := range returns {
		if classifyReturn(r) {
			verdict = true
		}
	}
	return verdict
}

// classifyReturn implements one RETURN clause's verdict: U₀ (the union of
// bindings referenced across all top-level non-aggregate items) has ≥2
// distinct bindings, OR any aggregate item's own MapLiteral unit(s) do, OR
// any fail-closed condition fired while computing either.
func classifyReturn(r *full.Return) bool {
	u0 := bindingSet{}
	unknown := false
	for _, item := range r.Items {
		if containsCollect(item.Expr) {
			continue // aggregate items contribute their own units below, not to U₀
		}
		if collectBindingsInto(item.Expr, u0) {
			unknown = true
		}
	}
	if unknown {
		return true
	}
	if len(u0) >= 2 {
		return true
	}
	for _, item := range r.Items {
		if !containsCollect(item.Expr) {
			continue
		}
		if classifyAggregateItem(item.Expr) {
			return true
		}
	}
	return false
}

// containsCollect reports whether e's expression tree contains a
// *FunctionCall named "collect" anywhere — walking through the shapes a
// collect can be nested inside: `+` concatenation of several collects, CASE
// result arms, and list literal elements.
func containsCollect(e full.Expr) bool {
	switch x := e.(type) {
	case nil:
		return false
	case *full.FunctionCall:
		if x.Name == "collect" {
			return true
		}
		for _, a := range x.Args {
			if containsCollect(a) {
				return true
			}
		}
		return false
	case *full.BinaryOp:
		return containsCollect(x.Left) || containsCollect(x.Right)
	case *full.CaseExpr:
		for _, alt := range x.Alternatives {
			if containsCollect(alt.Then) {
				return true
			}
		}
		return containsCollect(x.Else)
	case *full.ListLiteral:
		for _, el := range x.Elements {
			if containsCollect(el) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// classifyAggregateItem walks INTO an aggregate projection item's expression
// (the collect(...) call's arguments, and anything wrapping the collect: +,
// CASE arms, list literals) and computes one unit per MapLiteral found at
// any depth inside it. The item's verdict is true iff any such unit has ≥2
// distinct bindings, or the walk hit an unrecognised expression form.
func classifyAggregateItem(e full.Expr) bool {
	var units []bindingSet
	unknown := false
	findMapUnits(e, &units, &unknown)
	if unknown {
		return true
	}
	for _, u := range units {
		if len(u) >= 2 {
			return true
		}
	}
	return false
}

// findMapUnits walks e looking for MapLiteral nodes at any depth, appending
// one bindingSet unit per MapLiteral found to *units (via
// mapLiteralOwnUnit) and setting *unknown when the walk meets an expression
// form it cannot see through. A MapLiteral nested inside another
// MapLiteral's field value is a SEPARATE additional unit — mapLiteralOwnUnit
// already includes the nested map's bindings in the OUTER unit (it recurses
// through collectBindingsInto), and the recursive call below into that same
// field additionally emits the nested map's own unit, on top of — not
// instead of — the outer's.
func findMapUnits(e full.Expr, units *[]bindingSet, unknown *bool) {
	switch x := e.(type) {
	case nil:
		return
	case *full.MapLiteral:
		u, unk := mapLiteralOwnUnit(x)
		*units = append(*units, u)
		if unk {
			*unknown = true
		}
		for _, k := range x.Keys {
			findMapUnits(x.Values[k], units, unknown)
		}
	case *full.FunctionCall:
		for _, a := range x.Args {
			findMapUnits(a, units, unknown)
		}
	case *full.BinaryOp:
		findMapUnits(x.Left, units, unknown)
		findMapUnits(x.Right, units, unknown)
	case *full.CaseExpr:
		for _, alt := range x.Alternatives {
			findMapUnits(alt.Then, units, unknown)
		}
		findMapUnits(x.Else, units, unknown)
	case *full.ListLiteral:
		for _, el := range x.Elements {
			findMapUnits(el, units, unknown)
		}
	case *full.PatternComprehension:
		findMapUnits(x.Projection, units, unknown)
	default:
		// A VariableRef / PropertyAccess / Literal / ParameterRef / PatternExpr /
		// AndOr / Not leaf cannot itself carry a nested MapLiteral in this
		// engine's grammar — nothing further to search inside it for map-unit
		// discovery.
	}
}

// mapLiteralOwnUnit computes the unit a MapLiteral's own field-value
// expressions contribute — the bindings referenced by m.Values, ranged over
// m.Keys for determinism.
func mapLiteralOwnUnit(m *full.MapLiteral) (bindingSet, bool) {
	out := bindingSet{}
	unknown := false
	for _, k := range m.Keys {
		if collectBindingsInto(m.Values[k], out) {
			unknown = true
		}
	}
	return out, unknown
}

// collectBindingsInto adds every binding e's evaluation depends on into out,
// and reports whether the walk met an expression form it does not
// recognise. A binding is contributed by a *VariableRef (its .Name), or by
// the root *VariableRef at the end of a *PropertyAccess chain (.Target
// recursively); *Literal and *ParameterRef contribute nothing (they are
// evaluation-constant, never graph-bound).
func collectBindingsInto(e full.Expr, out bindingSet) bool {
	switch x := e.(type) {
	case nil:
		return false
	case *full.Literal:
		return false
	case *full.ParameterRef:
		return false
	case *full.VariableRef:
		out[x.Name] = struct{}{}
		return false
	case *full.PropertyAccess:
		name, ok, unk := propertyChainRoot(x)
		if ok {
			out[name] = struct{}{}
		}
		return unk
	case *full.BinaryOp:
		u1 := collectBindingsInto(x.Left, out)
		u2 := collectBindingsInto(x.Right, out)
		return u1 || u2
	case *full.AndOr:
		unknown := false
		for _, op := range x.Operands {
			if collectBindingsInto(op, out) {
				unknown = true
			}
		}
		return unknown
	case *full.Not:
		return collectBindingsInto(x.Operand, out)
	case *full.PatternExpr:
		return collectPatternBindings(x.Pattern, out)
	case *full.FunctionCall:
		unknown := false
		for _, a := range x.Args {
			if collectBindingsInto(a, out) {
				unknown = true
			}
		}
		return unknown
	case *full.MapLiteral:
		unknown := false
		for _, k := range x.Keys {
			if collectBindingsInto(x.Values[k], out) {
				unknown = true
			}
		}
		return unknown
	case *full.ListLiteral:
		unknown := false
		for _, el := range x.Elements {
			if collectBindingsInto(el, out) {
				unknown = true
			}
		}
		return unknown
	case *full.PatternComprehension:
		unknown := collectPatternBindings(x.Pattern, out)
		if collectBindingsInto(x.Where, out) {
			unknown = true
		}
		if collectBindingsInto(x.Projection, out) {
			unknown = true
		}
		return unknown
	case *full.CaseExpr:
		unknown := false
		for _, alt := range x.Alternatives {
			if collectBindingsInto(alt.When, out) {
				unknown = true
			}
			if collectBindingsInto(alt.Then, out) {
				unknown = true
			}
		}
		if collectBindingsInto(x.Else, out) {
			unknown = true
		}
		return unknown
	default:
		// A future AST node type this walker was never updated for — signal
		// unknown up the call chain rather than silently under-counting.
		return true
	}
}

// propertyChainRoot resolves a *PropertyAccess chain's root: the binding
// name when the chain terminates in a *VariableRef, ok=false with no
// binding when it terminates in a *Literal/*ParameterRef (evaluation-
// constant, contributes nothing), or unknown=true for any other terminal
// shape this walker cannot trace provenance for (fail-closed).
func propertyChainRoot(e full.Expr) (name string, ok bool, unknown bool) {
	for {
		switch x := e.(type) {
		case *full.VariableRef:
			return x.Name, true, false
		case *full.PropertyAccess:
			e = x.Target
			continue
		case *full.Literal, *full.ParameterRef:
			return "", false, false
		default:
			return "", false, true
		}
	}
}

// collectPatternBindings adds the NodePattern/RelPattern variable names a
// path pattern binds into out, for every node/rel whose Variable is
// non-empty. Used by both *PatternExpr (an existence test) and
// *PatternComprehension (its outer anchor plus its own internal bindings).
func collectPatternBindings(p full.PathPattern, out bindingSet) bool {
	for _, n := range p.Nodes {
		if n.Variable != "" {
			out[n.Variable] = struct{}{}
		}
	}
	for _, r := range p.Rels {
		if r.Variable != "" {
			out[r.Variable] = struct{}{}
		}
	}
	return false
}
