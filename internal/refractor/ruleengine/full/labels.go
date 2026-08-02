package full

// ReferencedLabels returns the set of vertex-type labels a compiled query's
// patterns can bind — the types whose vertices, aspects, or links can affect
// the query's output. The plain pipeline uses it to skip aspect/link
// reprojection for events on types the lens cannot read (e.g. a `meta` aspect
// mutation never changes a `MATCH (b:book)` lens's rows), bounding the
// per-event re-execute cost to the lenses that can actually be affected.
//
// exhaustive == false means the set is NOT authoritative and the caller must
// treat every type as potentially relevant: an unlabeled node pattern that is
// not a re-reference to a variable labeled elsewhere binds any type, and a
// variable-length relationship traverses intermediate nodes of arbitrary
// type. Conservative by construction — when in doubt, reproject.
//
// Re-reference is scoped to the WITH segment, because the executor's binding
// scope is: a WITH rebuilds every binding from its projection items alone
// (executor.projectItems),
// so a variable the WITH does not carry is unbound downstream and an unlabeled
// pattern node re-using that name re-seeds through the whole-bucket scan, which
// admits any type. Only a variable carried through as itself — `WITH a` or
// `WITH a AS b`, never `WITH a.x AS a` — keeps its label downstream, under the
// name the WITH gives it. `WITH *` carries no items through the visitor, so it
// carries no label either, which matches an executor that projects nothing.
func (cr *CompiledRule) ReferencedLabels() (labels map[string]struct{}, exhaustive bool) {
	if cr == nil || cr.Query == nil {
		return nil, false
	}
	labels = make(map[string]struct{})
	exhaustive = true

	// Pass 1: every variable that carries a label anywhere in the CURRENT WITH
	// segment — an unlabeled `(u)` later in that segment is a re-reference to
	// that binding, not a new any-type node
	// (`MATCH (u:unit)` … `MATCH (u)<-[:manages]-(l:identity)`).
	labeledVars := make(map[string]struct{})
	collectVars := func(p PathPattern) {
		for _, n := range p.Nodes {
			if n.Label != "" && n.Variable != "" {
				labeledVars[n.Variable] = struct{}{}
			}
		}
	}
	var collectVarsExpr func(e Expr)
	collectVarsExpr = func(e Expr) {
		switch x := e.(type) {
		case *PatternExpr:
			collectVars(x.Pattern)
		case *PatternComprehension:
			collectVars(x.Pattern)
			collectVarsExpr(x.Where)
			collectVarsExpr(x.Projection)
		case *Not:
			collectVarsExpr(x.Operand)
		case *AndOr:
			for _, op := range x.Operands {
				collectVarsExpr(op)
			}
		case *BinaryOp:
			collectVarsExpr(x.Left)
			collectVarsExpr(x.Right)
		}
	}
	// carryLabeled returns the labeled variables that survive a WITH, under the
	// names the WITH gives them. An item that is not a bare variable reference
	// projects a value, not the node binding, so it carries no label — and a
	// labeled variable the WITH omits stops counting as labeled downstream.
	carryLabeled := func(w *With) map[string]struct{} {
		next := make(map[string]struct{})
		for _, it := range w.Items {
			vr, isVar := it.Expr.(*VariableRef)
			if !isVar {
				continue
			}
			if _, wasLabeled := labeledVars[vr.Name]; !wasLabeled {
				continue
			}
			name := it.Alias
			if name == "" {
				name = vr.Name
			}
			next[name] = struct{}{}
		}
		return next
	}

	// Pass 2: build the label set; an unlabeled node is exhaustive only as a
	// re-reference to a labeled variable.
	addPattern := func(p PathPattern) {
		for _, n := range p.Nodes {
			if n.Label == "" {
				if n.Variable == "" {
					exhaustive = false
					continue
				}
				if _, isRef := labeledVars[n.Variable]; !isRef {
					exhaustive = false
				}
				continue
			}
			labels[n.Label] = struct{}{}
		}
		for _, r := range p.Rels {
			if r.MinHops != 1 || r.MaxHops != 1 {
				// Variable-length: intermediate hops bind arbitrary types.
				exhaustive = false
			}
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
	// Both passes run per WITH segment, in source order: pass 1 must see the
	// whole segment before pass 2 judges any of its unlabeled nodes (a label
	// may appear on a later clause of the same segment), and pass 2 must run
	// before the segment's own labeled set is replaced by what its closing WITH
	// carries forward.
	clauses := cr.Query.Clauses
	for start := 0; start < len(clauses); {
		end := start
		for end < len(clauses) {
			if _, isWith := clauses[end].(*With); isWith {
				break
			}
			end++
		}
		segment := clauses[start:end]
		var closing *With
		if end < len(clauses) {
			closing = clauses[end].(*With)
		}

		for _, c := range segment {
			if m, isMatch := c.(*Match); isMatch {
				for _, p := range m.Patterns {
					collectVars(p)
				}
				collectVarsExpr(m.Where)
			}
		}
		for _, c := range segment {
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
			}
		}
		if closing == nil {
			break
		}
		// The WITH's items are evaluated over the bindings the segment produced,
		// so they are judged in the segment's scope; its WHERE filters the
		// already-projected rows (executor.applyWith), so it is judged in the
		// carried scope — a variable this WITH drops is unbound by the time its
		// own WHERE runs.
		for _, it := range closing.Items {
			addExpr(it.Expr)
		}
		labeledVars = carryLabeled(closing)
		addExpr(closing.Where)
		start = end + 1
	}
	return labels, exhaustive
}
