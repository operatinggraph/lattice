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
// (`-[]->`, `-[r]->`) matches any relation, and a variable-length relationship
// traverses intermediate hops whose relations the pattern never names.
// Conservative by construction — when in doubt, reproject.
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

	addPattern := func(p PathPattern) {
		for _, r := range p.Rels {
			if r.MinHops != 1 || r.MaxHops != 1 {
				// Variable-length: intermediate hops traverse relations this
				// pattern does not name.
				exhaustive = false
			}
			if r.Type == "" {
				exhaustive = false
				continue
			}
			relations[r.Type] = struct{}{}
		}
	}
	var addExpr func(e Expr)
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
