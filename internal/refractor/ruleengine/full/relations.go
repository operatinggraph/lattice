package full

// ReferencedRelations returns the set of link relations a compiled query's
// patterns can traverse — the `<relation>` segment of Contract #1's
// `lnk.<typeA>.<idA>.<relation>.<typeB>.<idB>` for every link whose mutation
// can change the query's output. It is ReferencedLabels' companion on the
// other half of a link key: that one bounds which endpoint TYPES can bind,
// this one bounds which RELATIONS between them the query actually walks.
//
// exhaustive == false means the set is NOT authoritative and the caller must
// treat every relation as potentially relevant: an untyped relationship
// (`-[]->`, `-[r]->`) matches any relation. Conservative by construction — when
// in doubt, reproject.
//
// A variable-length hop does NOT cost exhaustiveness here, which is where this
// parts company with ReferencedLabels. That derivation must give up on a
// var-length hop because the intermediate NODES bind arbitrary types; the
// relation is different, because traverseRel re-applies the same rel.Type at
// every hop of the walk (executor.go's `rel.Type != "" && e.Name != rel.Type`
// inside the hop loop), so `-[:containedIn*0..7]->` traverses `containedIn` and
// nothing else. Only an untyped hop is unbounded, and that case is caught by
// the Type == "" arm below whatever its quantifier.
//
// An EMPTY set with exhaustive == true is a real, common, and maximally strong
// answer, not a "no data" fallback: a query with no relationship pattern at all
// (`MATCH (p:patient) WHERE p.demographics.data.fullName <> null RETURN p.key`)
// cannot be affected by ANY link. Callers must not collapse it with the
// non-exhaustive case.
//
// Unlike ReferencedLabels this needs no per-WITH-segment scoping: a relation is
// written literally on the relationship pattern that traverses it, so there is
// no re-reference form for a WITH to carry or drop. The walk is therefore a
// single pass over the same clause and expression shapes ReferencedLabels
// visits — kept in step with it deliberately, because a pattern position one
// derivation reads and the other does not would make the two sets disagree
// about the same link key.
func (cr *CompiledRule) ReferencedRelations() (relations map[string]struct{}, exhaustive bool) {
	if cr == nil || cr.Query == nil {
		return nil, false
	}
	relations = make(map[string]struct{})
	exhaustive = true

	// Declared ahead of addPattern so a pattern can descend into a node's or a
	// relationship's PROPERTY MAP: those are general expressions the executor
	// really evaluates (propsAllMatch -> evalExpr), and one may itself hold a
	// PatternExpr / PatternComprehension that traverses a relation. A walk that
	// stopped at p.Nodes and p.Rels would report an exhaustive set missing that
	// relation, and the consumer would never be told the link changed.
	var addExpr func(e Expr)
	addPattern := func(p PathPattern) {
		for _, n := range p.Nodes {
			for _, v := range n.Properties {
				addExpr(v)
			}
		}
		for _, r := range p.Rels {
			for _, v := range r.Properties {
				addExpr(v)
			}
			if r.Type == "" {
				exhaustive = false
				continue
			}
			relations[r.Type] = struct{}{}
		}
	}
	addExpr = func(e Expr) {
		switch x := e.(type) {
		case nil:
		case *PropertyAccess:
			addExpr(x.Target)
		case *BinaryOp:
			addExpr(x.Left)
			addExpr(x.Right)
		case *AndOr:
			for _, op := range x.Operands {
				addExpr(op)
			}
		case *Not:
			addExpr(x.Operand)
		case *PatternExpr:
			addPattern(x.Pattern)
		case *PatternComprehension:
			addPattern(x.Pattern)
			addExpr(x.Where)
			addExpr(x.Projection)
		case *FunctionCall:
			for _, a := range x.Args {
				addExpr(a)
			}
		case *MapLiteral:
			for _, v := range x.Values {
				addExpr(v)
			}
		case *ListLiteral:
			for _, el := range x.Elements {
				addExpr(el)
			}
		case *CaseExpr:
			for _, alt := range x.Alternatives {
				addExpr(alt.When)
				addExpr(alt.Then)
			}
			addExpr(x.Else)
		case *Literal, *ParameterRef, *VariableRef:
			// The LEAVES of the expression grammar: none of them can hold a
			// PathPattern, so there is nothing under them to walk. Named
			// explicitly rather than swept into the default arm below, because
			// that arm's job is to notice an expression shape this walk does not
			// model, and a leaf falling into it would take every query
			// non-exhaustive.
		default:
			// An Expr shape this walk does not model. It may carry a PathPattern
			// (PatternExpr and PatternComprehension already do), and a relation
			// reached only through it would be missing from an otherwise
			// authoritative set — which is the one error mode with no recovery:
			// the consumer never learns the link changed, and on the auth plane
			// that is a grant that never retracts. Reporting the set as
			// non-exhaustive costs delivered-then-skipped events and nothing
			// else, so the unmodelled case takes that side.
			//
			// Adding a case above is what removes the cost. Adding an Expr type
			// without one degrades this lens to the broad filter rather than
			// narrowing it wrongly.
			exhaustive = false
		}
	}

	for _, c := range cr.Query.Clauses {
		switch cl := c.(type) {
		case *Match:
			for _, p := range cl.Patterns {
				addPattern(p)
			}
			addExpr(cl.Where)
		case *Return:
			for _, it := range cl.Items {
				addExpr(it.Expr)
			}
		case *With:
			for _, it := range cl.Items {
				addExpr(it.Expr)
			}
			addExpr(cl.Where)
		}
	}
	return relations, exhaustive
}
