package full

// WithLabelExpansion returns a shallow copy of cr carrying exp as its
// LabelExpansion. It never mutates cr in place (dynamic-type-taxonomy-
// design.md §4.3): rule state is published copy-on-write precisely so a
// reader can never observe a half-rewritten rule
// (internal/refractor/pipeline's ruleMu doc), and writing LabelExpansion
// onto a live *CompiledRule would race a concurrent evaluation reading it
// mid-swap. The Query AST is shared read-only between the original and the
// copy — nothing ever mutates a published rule's Query, so aliasing it is
// cheap and safe.
//
// A nil cr returns nil.
func WithLabelExpansion(cr *CompiledRule, exp map[string]map[string]struct{}) *CompiledRule {
	if cr == nil {
		return nil
	}
	next := *cr
	next.LabelExpansion = exp
	return &next
}

// ExpansionLabels returns the set of labels cr's query patterns reference
// with the `*` taxonomy-expansion sigil (NodePattern.LabelExpand) —
// wherever an oC_NodePattern can appear: MATCH/OPTIONAL MATCH patterns,
// WHERE existence tests and pattern comprehensions, and their nested
// expressions. useFullEngineBranches calls the taxonomy resolver only for
// the labels this returns, and calls it AT ALL only when the result is
// non-empty: a query with no `*` anywhere gets back an empty, non-nil set,
// so it never touches the resolver and its label derivation is bit-for-bit
// unchanged (§14 Fire A item 3's inertness guarantee).
func (cr *CompiledRule) ExpansionLabels() map[string]struct{} {
	out := map[string]struct{}{}
	if cr == nil || cr.Query == nil {
		return out
	}

	var addExpr func(e Expr)
	addPattern := func(p PathPattern) {
		for _, n := range p.Nodes {
			for _, v := range n.Properties {
				addExpr(v)
			}
			if n.LabelExpand && n.Label != "" {
				out[n.Label] = struct{}{}
			}
		}
		for _, r := range p.Rels {
			for _, v := range r.Properties {
				addExpr(v)
			}
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
		}
	}

	for _, c := range cr.Query.Clauses {
		switch cl := c.(type) {
		case *Match:
			for _, p := range cl.Patterns {
				addPattern(p)
			}
			addExpr(cl.Where)
		case *With:
			for _, it := range cl.Items {
				addExpr(it.Expr)
			}
			addExpr(cl.Where)
		case *Return:
			for _, it := range cl.Items {
				addExpr(it.Expr)
			}
		}
	}
	return out
}
