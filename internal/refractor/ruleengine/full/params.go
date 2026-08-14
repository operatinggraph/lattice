package full

// ReferencesParam reports whether the compiled query references the named query
// parameter ($now, $projectedAt, $actorKey, …), and whether the walk that
// decided was exhaustive.
//
// exhaustive == false means the answer is NOT authoritative and every caller
// must treat it as "assume it does". A non-exhaustive walk therefore reports
// (false, false) rather than a bare false, and a caller that reads only the
// first return value converts an unmodelled query shape into a confident
// "no" — which is why both values travel together.
//
// The flag is only as good as the walk, so this covers every syntactic position
// a parameter can occupy: a MATCH's WHERE, a RETURN or WITH projection item, a
// WITH's own WHERE, a node or relationship PROPERTY MAP, and — through the
// expression walk — a CASE alternative, a function argument, a map or list
// literal, a pattern comprehension's pattern/predicate/projection, and any
// NOT (…) sub-expression. Anything else, at either the clause or the expression
// level, reports exhaustive == false.
//
// It mirrors ReferencedLabels' shape and its exhaustive-flag discipline
// (labels.go), and takes the same side on the one error mode with no recovery:
// an unmodelled node kind must degrade the ANSWER, never the walk's confidence
// in it. The consumer (the plain-lens Auditor's enrolment, which refuses a lens
// whose recompute would legitimately differ from the stored row) reads a
// non-exhaustive walk as a refusal, so the cost of an unmodelled shape is a
// published refusal rather than a lens reported divergent forever.
//
// Scope is deliberately narrower than ReferencedLabels': a parameter reference
// is a syntactic fact about the query text, so no WITH-segment scoping,
// re-binding analysis or label carry is involved.
func (cr *CompiledRule) ReferencesParam(name string) (referenced, exhaustive bool) {
	if cr == nil || cr.Query == nil {
		return false, false
	}
	w := paramWalk{name: name, exhaustive: true}
	for _, c := range cr.Query.Clauses {
		switch cl := c.(type) {
		case *Match:
			for _, p := range cl.Patterns {
				w.pattern(p)
			}
			w.expr(cl.Where)
		case *With:
			for _, it := range cl.Items {
				w.expr(it.Expr)
			}
			w.expr(cl.Where)
		case *Return:
			for _, it := range cl.Items {
				w.expr(it.Expr)
			}
		default:
			// A Clause shape this walk does not model. It may carry an
			// expression holding the parameter, and the caller must not read
			// the absence of a sighting as proof.
			w.exhaustive = false
		}
	}
	return w.found, w.exhaustive
}

// paramWalk accumulates one ReferencesParam walk: whether the parameter was
// seen, and whether every node the walk descended through was modelled.
type paramWalk struct {
	name       string
	found      bool
	exhaustive bool
}

// pattern descends a path pattern's node and relationship property maps. Those
// are general expressions the executor really evaluates (propsAllMatch →
// evalExpr), so `(u {key: $now})` is a genuine parameter reference and a
// pattern comprehension nested inside one can hold another.
func (w *paramWalk) pattern(p PathPattern) {
	for _, n := range p.Nodes {
		for _, v := range n.Properties {
			w.expr(v)
		}
	}
	for _, r := range p.Rels {
		for _, v := range r.Properties {
			w.expr(v)
		}
	}
}

func (w *paramWalk) expr(e Expr) {
	switch x := e.(type) {
	case nil:
	case *ParameterRef:
		if x.Name == w.name {
			w.found = true
		}
	case *PropertyAccess:
		w.expr(x.Target)
	case *BinaryOp:
		w.expr(x.Left)
		w.expr(x.Right)
	case *AndOr:
		for _, op := range x.Operands {
			w.expr(op)
		}
	case *Not:
		w.expr(x.Operand)
	case *PatternExpr:
		w.pattern(x.Pattern)
	case *PatternComprehension:
		w.pattern(x.Pattern)
		w.expr(x.Where)
		w.expr(x.Projection)
	case *FunctionCall:
		for _, a := range x.Args {
			w.expr(a)
		}
	case *MapLiteral:
		for _, v := range x.Values {
			w.expr(v)
		}
	case *ListLiteral:
		for _, el := range x.Elements {
			w.expr(el)
		}
	case *CaseExpr:
		for _, alt := range x.Alternatives {
			w.expr(alt.When)
			w.expr(alt.Then)
		}
		w.expr(x.Else)
	case *Literal, *VariableRef:
		// The LEAVES that cannot hold a parameter: named explicitly rather than
		// swept into the default arm, whose job is to notice a shape this walk
		// does not model. A leaf falling into it would take every query
		// non-exhaustive.
	default:
		// An Expr shape this walk does not model — it may hold the parameter,
		// so the answer stops being authoritative. Adding a case above is what
		// removes the cost; adding an Expr type without one degrades callers to
		// their fail-closed branch rather than answering wrongly.
		w.exhaustive = false
	}
}
