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
// A RETURN item can name a WITH alias rather than an aggregate directly — a
// staged actorAggregate producer (internal/pkgmgr/anchorwalk.go) folds each
// walk's collect(...) into its own `WITH … AS grantSliceN` before the final
// RETURN concatenates the slices (`grantSlice0 + grantSlice1 + …`). Classifying
// that RETURN expression as written would miss every MapLiteral (they live in
// the WITH items, not the RETURN) and misclassify the whole producer as one
// shared multi-binding unit. So before classification, every RETURN item's
// expression is resolved through the query's WITH clauses: each `*full.
// VariableRef` naming a WITH alias is substituted with that alias's defining
// expression, recursively, walking `cr.Query.Clauses` in order so a later WITH's
// alias shadows an earlier one. A bare pass-through (`WITH identity, grantSlice0,
// …` restating a binding without redefining it) does not shadow — it resolves to
// whatever defined that name earlier, which is what lets grantSlice0 resolve all
// the way back to its collect(...). The compiled AST itself is never mutated;
// resolution builds a fresh expression tree.
//
// Defaults to VALIDATE (true), never to exempt, on every uncertain shape:
// a nil/queryless compiled rule, a query with no RETURN clause, any
// expression form the walker does not recognise, and a WITH-alias resolution
// that hits its recursion-depth cap (a genuine alias cycle or pathological
// chain). Getting this backwards would silently exempt a lens from the
// auth-plane read-consistency check it needs — a combination-grant that
// never gets caught, not a lens that merely pays an avoidable validation
// cost. See plan.go's Compile for the caller-side fail-closed conjunct (not
// the full engine's compiled artifact, or the artifact assertion fails →
// validate as well).
func hasMultiBindingConjunctUnit(cr *full.CompiledRule) bool {
	if cr == nil || cr.Query == nil {
		return true
	}
	defs := map[string]full.Expr{}
	sawReturn := false
	verdict := false
	for _, c := range cr.Query.Clauses {
		switch clause := c.(type) {
		case *full.With:
			mergeWithAliasDefs(defs, clause)
		case *full.Return:
			sawReturn = true
			resolved, unknown := resolveReturn(clause, defs)
			if unknown || classifyReturn(resolved) {
				verdict = true
			}
		}
	}
	if !sawReturn {
		return true
	}
	return verdict
}

// withItemAlias computes one WITH/RETURN projection item's effective output
// name — the engine's own alias rule (full's unexported projectionAutoAlias,
// mirrored here since this package cannot reach it): an explicit AS wins; a
// bare *full.VariableRef auto-aliases to its own name; a *full.PropertyAccess
// auto-aliases to its property key; anything else gets a positional name no
// later clause can reference by name, so it reports ok=false.
func withItemAlias(item full.ProjectionItem) (string, bool) {
	if item.Alias != "" {
		return item.Alias, true
	}
	switch x := item.Expr.(type) {
	case *full.VariableRef:
		return x.Name, true
	case *full.PropertyAccess:
		return x.Key, true
	default:
		return "", false
	}
}

// mergeWithAliasDefs folds one WITH clause's items into defs, in place. A bare
// pass-through item (its expression is a *full.VariableRef naming the SAME
// alias it projects under — exactly `WITH identity, grantSlice0, …`) carries a
// binding forward without redefining it, so it must not clobber an earlier
// stage's real definition; it is skipped. Every other item is a genuine
// (re)definition and overwrites whatever defs already held for that alias.
func mergeWithAliasDefs(defs map[string]full.Expr, w *full.With) {
	for _, item := range w.Items {
		alias, ok := withItemAlias(item)
		if !ok {
			continue
		}
		if vr, isVar := item.Expr.(*full.VariableRef); isVar && vr.Name == alias {
			continue
		}
		defs[alias] = item.Expr
	}
}

// resolveReturn builds a fresh *full.Return with every item's expression
// resolved through defs (resolveWithAliases), leaving r untouched. unknown is
// true when any item's resolution hit the recursion-depth cap; the caller
// must treat that as an uncertain shape (validate) rather than classify a
// partially-resolved tree.
func resolveReturn(r *full.Return, defs map[string]full.Expr) (result *full.Return, unknown bool) {
	items := make([]full.ProjectionItem, len(r.Items))
	for i, it := range r.Items {
		resolved, unk := resolveWithAliases(it.Expr, defs, 0)
		if unk {
			return nil, true
		}
		items[i] = full.ProjectionItem{Expr: resolved, Alias: it.Alias}
	}
	return &full.Return{Distinct: r.Distinct, Items: items}, false
}

// maxAliasResolveDepth bounds resolveWithAliases's recursion. A genuine cycle
// between WITH aliases (or any pathologically deep chain) hits this cap
// instead of looping forever; hitting it is itself an uncertain shape, so the
// caller fails closed (validate) rather than trust a truncated resolution.
const maxAliasResolveDepth = 64

// resolveWithAliases returns e with every *full.VariableRef whose name is a
// key of defs substituted for that alias's defining expression, recursively,
// building an entirely new tree — e and everything defs points into are never
// mutated. unknown is true once depth exceeds maxAliasResolveDepth, or for any
// expression form this walker does not recognise (mirroring collectBindingsInto's
// own fail-closed default below).
//
// A name whose definition is a bare *full.VariableRef of the SAME name (the
// pass-through mergeWithAliasDefs normally keeps out of defs in the first
// place) resolves to itself rather than recursing again — a defensive guard
// against a trivial one-step self-reference reaching this far.
func resolveWithAliases(e full.Expr, defs map[string]full.Expr, depth int) (full.Expr, bool) {
	if depth > maxAliasResolveDepth {
		return nil, true
	}
	switch x := e.(type) {
	case nil:
		return nil, false
	case *full.Literal, *full.ParameterRef:
		return e, false
	case *full.VariableRef:
		def, ok := defs[x.Name]
		if !ok {
			return e, false
		}
		if vr, isVar := def.(*full.VariableRef); isVar && vr.Name == x.Name {
			return e, false
		}
		return resolveWithAliases(def, defs, depth+1)
	case *full.PropertyAccess:
		target, unk := resolveWithAliases(x.Target, defs, depth+1)
		if unk {
			return nil, true
		}
		return &full.PropertyAccess{Target: target, Key: x.Key}, false
	case *full.BinaryOp:
		l, u1 := resolveWithAliases(x.Left, defs, depth+1)
		r, u2 := resolveWithAliases(x.Right, defs, depth+1)
		if u1 || u2 {
			return nil, true
		}
		return &full.BinaryOp{Op: x.Op, Left: l, Right: r}, false
	case *full.AndOr:
		operands := make([]full.Expr, len(x.Operands))
		for i, op := range x.Operands {
			r, unk := resolveWithAliases(op, defs, depth+1)
			if unk {
				return nil, true
			}
			operands[i] = r
		}
		return &full.AndOr{Op: x.Op, Operands: operands}, false
	case *full.Not:
		operand, unk := resolveWithAliases(x.Operand, defs, depth+1)
		if unk {
			return nil, true
		}
		return &full.Not{Operand: operand}, false
	case *full.PatternExpr:
		// A pattern's own node/rel variables are fresh bindings, not references
		// to an outer WITH alias — nothing inside it to substitute.
		return e, false
	case *full.FunctionCall:
		args := make([]full.Expr, len(x.Args))
		for i, a := range x.Args {
			r, unk := resolveWithAliases(a, defs, depth+1)
			if unk {
				return nil, true
			}
			args[i] = r
		}
		return &full.FunctionCall{Namespace: x.Namespace, Name: x.Name, Distinct: x.Distinct, Args: args}, false
	case *full.MapLiteral:
		values := make(map[string]full.Expr, len(x.Values))
		for _, k := range x.Keys {
			r, unk := resolveWithAliases(x.Values[k], defs, depth+1)
			if unk {
				return nil, true
			}
			values[k] = r
		}
		return &full.MapLiteral{Keys: x.Keys, Values: values}, false
	case *full.ListLiteral:
		elements := make([]full.Expr, len(x.Elements))
		for i, el := range x.Elements {
			r, unk := resolveWithAliases(el, defs, depth+1)
			if unk {
				return nil, true
			}
			elements[i] = r
		}
		return &full.ListLiteral{Elements: elements}, false
	case *full.PatternComprehension:
		where, u1 := resolveWithAliases(x.Where, defs, depth+1)
		proj, u2 := resolveWithAliases(x.Projection, defs, depth+1)
		if u1 || u2 {
			return nil, true
		}
		return &full.PatternComprehension{Variable: x.Variable, Pattern: x.Pattern, Where: where, Projection: proj}, false
	case *full.CaseExpr:
		alts := make([]full.CaseWhenThen, len(x.Alternatives))
		for i, alt := range x.Alternatives {
			when, u1 := resolveWithAliases(alt.When, defs, depth+1)
			then, u2 := resolveWithAliases(alt.Then, defs, depth+1)
			if u1 || u2 {
				return nil, true
			}
			alts[i] = full.CaseWhenThen{When: when, Then: then}
		}
		els, u3 := resolveWithAliases(x.Else, defs, depth+1)
		if u3 {
			return nil, true
		}
		return &full.CaseExpr{Alternatives: alts, Else: els}, false
	default:
		// A future AST node type this walker was never updated for — signal
		// unknown up the call chain rather than resolving through it blind.
		return nil, true
	}
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
