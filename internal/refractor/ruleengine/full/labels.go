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
//
// A label excuses an unlabeled sighting only where it CONSTRAINS what survives,
// which is what the two label scopes below distinguish:
//
//   - A REQUIRED MATCH's label constrains the whole segment, backward and
//     forward: applied to an already-bound variable it drops the bindings that
//     fail it (executor.applyMatch), pruning an earlier whole-bucket seed down
//     to that type.
//   - An OPTIONAL MATCH's label constrains only from its own clause onward: the
//     path binds as a unit or null-binds every variable NEW to it, and a failed
//     match restores the row with any earlier binding intact
//     (executor.nullBindNewVars). So it can never justify an unlabeled sighting
//     that came before it.
//   - A label inside a WHERE or a pattern comprehension constrains nothing at
//     all: those bindings are discarded rather than threaded into the row
//     (executor.existsAsPredicate, executor.evalPatternComprehension), so a
//     later MATCH on the same name is a fresh whole-bucket seed.
//
// Both scopes feed the WITH carry, since a carried bare reference keeps its
// *nodeRef: downstream the variable is either that label's type or null, and a
// null binding cannot extend a required match. This matters for the generated
// read-grant lenses in particular, whose walk chains compile entirely to
// OPTIONAL MATCH under a single required head (pkgmgr/anchorwalk.go).
func (cr *CompiledRule) ReferencedLabels() (labels map[string]struct{}, exhaustive bool) {
	if cr == nil || cr.Query == nil {
		return nil, false
	}
	labels = make(map[string]struct{})
	exhaustive = true

	// Pass 1: every variable a REQUIRED MATCH labels anywhere in the CURRENT
	// WITH segment — an unlabeled `(u)` anywhere in that segment is a
	// re-reference to that binding, not a new any-type node
	// (`MATCH (u:unit)` … `MATCH (u)<-[:manages]-(l:identity)`). Forward-looking
	// as well as backward, because a required label prunes bindings a preceding
	// clause already made.
	labeledVars := make(map[string]struct{})
	// optionalLabeled holds what an OPTIONAL MATCH labels, accumulated in clause
	// order by pass 2 rather than collected up front, so it can excuse only the
	// unlabeled sightings that FOLLOW it. Segment-local: it empties at each WITH,
	// because a variable that WITH does not carry is unbound afterwards and a
	// later re-reference re-seeds through the whole-bucket scan.
	optionalLabeled := make(map[string]struct{})
	collectVarsInto := func(dst map[string]struct{}, p PathPattern) {
		for _, n := range p.Nodes {
			if n.Label != "" && n.Variable != "" {
				dst[n.Variable] = struct{}{}
			}
		}
	}
	labelConstrains := func(v string) bool {
		if _, ok := labeledVars[v]; ok {
			return true
		}
		_, ok := optionalLabeled[v]
		return ok
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
			if !labelConstrains(vr.Name) {
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
	//
	// addExpr is declared ahead of addPattern so a pattern can descend into a
	// node's or a relationship's PROPERTY MAP. Those are general expressions
	// the executor really evaluates (propsAllMatch -> evalExpr), and one may
	// hold a PatternExpr / PatternComprehension binding a type no other clause
	// mentions — which an exhaustive set has to contain.
	var addExpr func(e Expr)
	addPattern := func(p PathPattern) {
		for _, n := range p.Nodes {
			for _, v := range n.Properties {
				addExpr(v)
			}
			if n.Label == "" {
				if n.Variable == "" {
					exhaustive = false
					continue
				}
				if !labelConstrains(n.Variable) {
					exhaustive = false
				}
				continue
			}
			labels[n.Label] = struct{}{}
		}
		for _, r := range p.Rels {
			for _, v := range r.Properties {
				addExpr(v)
			}
			if r.MinHops != 1 || r.MaxHops != 1 {
				// Variable-length: intermediate hops bind arbitrary types.
				exhaustive = false
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
		case *Literal, *ParameterRef, *VariableRef:
			// The LEAVES of the expression grammar: none of them can hold a
			// PathPattern, so there is nothing under them to walk. Named
			// explicitly rather than swept into the default arm below, because
			// that arm's job is to notice an expression shape this walk does not
			// model, and a leaf falling into it would take every query
			// non-exhaustive.
		default:
			// An Expr shape this walk does not model. It may bind a node whose
			// type no other clause mentions (PatternExpr and PatternComprehension
			// already do), and a label reached only through it would be missing
			// from an otherwise authoritative set — which is the one error mode
			// with no recovery: the consumer never learns the vertex changed, and
			// on the auth plane that is a grant that never retracts. Reporting the
			// set as non-exhaustive costs delivered-then-skipped events and
			// nothing else, so the unmodelled case takes that side — the same
			// posture ReferencedRelations takes on the other half of a link key,
			// so neither dimension is authoritative over a shape the other has
			// given up on.
			//
			// Adding a case above is what removes the cost. Adding an Expr type
			// without one degrades this lens to the broad filter rather than
			// narrowing it wrongly.
			exhaustive = false
		}
	}
	// Both passes run per WITH segment, in source order: pass 1 must see the
	// whole segment before pass 2 judges any of its unlabeled nodes (a required
	// label may appear on a later clause of the same segment and still prune
	// this one's bindings), and pass 2 must run before the segment's own labeled
	// set is replaced by what its closing WITH carries forward.
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
			if m, isMatch := c.(*Match); isMatch && !m.Optional {
				for _, p := range m.Patterns {
					collectVarsInto(labeledVars, p)
				}
			}
		}
		for _, c := range segment {
			switch cl := c.(type) {
			case *Match:
				for _, p := range cl.Patterns {
					if cl.Optional {
						// A PATH binds whole or null-binds every variable new to
						// it, so its own labels hold over its own unlabeled
						// nodes. The unit is the path, NOT the clause: a clause's
						// comma-separated paths are threaded one into the next
						// (executor.matchPatterns), and a later path's failure
						// null-binds only what is still absent from the row
						// (executor.nullBindNewVars) — leaving an earlier path's
						// whole-bucket binding in place, of a type this clause's
						// later label never constrained.
						collectVarsInto(optionalLabeled, p)
					}
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
		// The carry folds both scopes into the next segment's required set, then
		// the optional scope empties: past this WITH an optionally-labeled
		// variable either survived as a carried binding or is unbound, and the
		// WITH's own WHERE already reads that carried scope.
		labeledVars = carryLabeled(closing)
		optionalLabeled = make(map[string]struct{})
		addExpr(closing.Where)
		start = end + 1
	}
	return labels, exhaustive
}
