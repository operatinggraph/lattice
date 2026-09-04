package full

// withScopeReject is AnchorHopIndex's WITH conjunct: the question of whether a
// WITH boundary strands a binding the pattern graph then misreads.
//
// A WITH rebuilds every row from its projection items alone
// (executor.projectItems), so a variable it does not carry is UNBOUND
// afterwards. Two things follow from that, and only the first is a hazard:
//
//   - A later clause re-referencing a dropped name gets a FRESH binding. Where
//     the name heads a pattern, executor.matchPath seeds it through seedNodes'
//     whole-bucket scan; where it merely appears again, hopIndexBuilder.position
//     merges two unrelated executor bindings into a single pattern position and
//     the graph gains a hop that no single row ever walks. Either way the real
//     dependency is a BUCKET rather than a link, so an adjacency walk cannot see
//     it and the derived anchor set is SMALLER than the truth — on the auth
//     plane, a revocation that never reprojects.
//   - A dropped name no later clause mentions strands nothing. Every position
//     the walk uses keeps its hops, and the anchor is the `$actorKey` PARAMETER
//     rather than a row column, so a WITH may drop even the anchor's own
//     variable and leave the graph exactly as walkable as before.
//
// So the refusal is not "the query carries a WITH" but "a WITH dropped a name a
// later clause still uses". Everything the model cannot resolve exactly refuses
// with its own reason instead: over-refusing costs one BFS fallback, and
// under-refusing costs a grant that outlives its revocation.

import "fmt"

// withScopeReject returns the reason to refuse, or "" when every WITH boundary
// in clauses is harmless. Clause order is load-bearing and matches the
// executor's.
func withScopeReject(clauses []Clause) string {
	if !carriesWith(clauses) {
		return ""
	}

	// One scan of the whole query up front, for two reasons. It proves every
	// clause and expression in it is a shape this walk models — a single
	// unmodelled node anywhere means some later clause's referenced set may be
	// short, and a short set is exactly the under-refusal to avoid. And it
	// yields the NODE-pattern variable names, which the carry rules below test
	// their introduced names against.
	whole := newVarScan()
	for _, c := range clauses {
		whole.clause(c)
	}
	if !whole.ok {
		return withCannotModel(fmt.Sprintf("the query carries %s, whose variable references this walk cannot enumerate", whole.unmodelled))
	}
	nodeVars := whole.nodes

	// seen is every name that has been in scope at any point up to here, and
	// dropped is every name some WITH has since let go of. Both only grow: a
	// name once stranded is stranded for the rest of the query, which is what
	// makes a drop at one boundary and a re-reference after a LATER one still
	// read as the hazard it is.
	seen := map[string]struct{}{}
	dropped := map[string]struct{}{}

	for _, c := range clauses {
		w, isWith := c.(*With)
		if !isWith {
			s := newVarScan()
			s.clause(c)
			if v := firstIn(s.names, dropped); v != "" {
				return withReReference(v)
			}
			absorb(seen, s.names)
			continue
		}

		// `WITH *` reaches the AST as a projection body with no items at all
		// (visitor.visitWith), so the surviving set is not merely unknown — it
		// is indistinguishable from "carries nothing". Naming the shape beats
		// reporting every variable as dropped.
		if len(w.Items) == 0 {
			return withCannotModel("a `WITH *`, which the AST records as an empty projection list rather than a carried set")
		}

		// The items are evaluated over the rows the PRECEDING segment produced,
		// so an earlier boundary's dropped name reaching one is already the
		// hazard — judged here, before this boundary's own carry is computed.
		items := newVarScan()
		for _, it := range w.Items {
			items.expr(it.Expr)
		}
		if v := firstIn(items.names, dropped); v != "" {
			return withReReference(v)
		}

		carried, reject := withCarries(w, nodeVars)
		if reject != "" {
			return reject
		}
		for n := range seen {
			if _, kept := carried[n]; !kept {
				dropped[n] = struct{}{}
			}
		}
		absorb(seen, carried)

		// The WITH's own WHERE filters the already-projected rows
		// (executor.applyWith), so it reads the CARRIED scope: a name this
		// boundary just let go of is unbound by the time its own WHERE runs.
		where := newVarScan()
		where.expr(w.Where)
		if v := firstIn(where.names, dropped); v != "" {
			return withReReference(v)
		}
		absorb(seen, where.names)
	}
	return ""
}

// withCarries returns the names still bound after w, or the reason w's
// projection list cannot be modelled. It mirrors executor.projectItems' own
// naming — alias when present, projectionAutoAlias otherwise — by calling the
// executor's function rather than restating it, so the index and the executor
// cannot disagree about what a row is called.
//
// A bare `WITH a` (and its no-op `WITH a AS a`) carries the BINDING: evalExpr on
// a VariableRef returns whatever was bound, node reference included, under the
// same name, so every downstream sighting of `a` is the same position the
// builder already has. Every other item shape is refused rather than modelled:
//
//   - A rename, `WITH a AS b`, carries the binding under a name the builder
//     keys nothing to. Downstream `b` becomes a phantom position with no hops,
//     and where `b` is already some other pattern's variable it grafts onto that
//     one instead — a hop asserted between vertices no row ever relates, which
//     can shorten a Dist and send AnchorSideSeeds to the wrong endpoint.
//   - A computed item, `WITH count(x) AS n`, carries a VALUE and no binding,
//     which is harmless on its own — `n` heads no pattern the builder can
//     resolve, and a pattern headed by it is refused as ungrounded. It is
//     refused only when its name collides with a pattern variable, where the
//     builder would read the value as that variable's node.
func withCarries(w *With, nodeVars map[string]struct{}) (map[string]struct{}, string) {
	carried := make(map[string]struct{}, len(w.Items))
	for i, it := range w.Items {
		vr, isVar := it.Expr.(*VariableRef)
		switch {
		case isVar && (it.Alias == "" || it.Alias == vr.Name):
			carried[vr.Name] = struct{}{}
		case isVar:
			if _, isNode := nodeVars[vr.Name]; isNode {
				return nil, withCannotModel(fmt.Sprintf("a WITH renaming the pattern variable `%s` to `%s`", vr.Name, it.Alias))
			}
			if _, isNode := nodeVars[it.Alias]; isNode {
				return nil, withCannotModel(fmt.Sprintf("a WITH renaming `%s` onto `%s`, which is a pattern variable", vr.Name, it.Alias))
			}
			carried[it.Alias] = struct{}{}
		default:
			name := it.Alias
			if name == "" {
				name = projectionAutoAlias(it.Expr, i)
			}
			if _, isNode := nodeVars[name]; isNode {
				return nil, withCannotModel(fmt.Sprintf("a WITH projecting a computed value under `%s`, which is also a pattern variable", name))
			}
			carried[name] = struct{}{}
		}
	}
	return carried, ""
}

func withReReference(v string) string {
	return fmt.Sprintf("a WITH dropped `%s` and a later clause re-references it — the rebind is a bucket scan, not a link", v)
}

func withCannotModel(shape string) string {
	return "the WITH scope walk cannot model " + shape
}

// firstIn returns the lexically first name of names that is also in set, or ""
// when they are disjoint. Lexically first rather than first-encountered so the
// refusal an operator reads is the same string on every run over the same
// query — map iteration order is not.
func firstIn(names, set map[string]struct{}) string {
	hit := ""
	for n := range names {
		if _, bad := set[n]; !bad {
			continue
		}
		if hit == "" || n < hit {
			hit = n
		}
	}
	return hit
}

func absorb(dst, src map[string]struct{}) {
	for n := range src {
		dst[n] = struct{}{}
	}
}

// varScan collects the variable names a clause REFERENCES, and separately the
// subset of them that a NODE PATTERN introduces or re-references.
//
// ok is false once it meets a clause or expression node it has no case for.
// The default-deny arm is the point of the type switches: a new AST node added
// without a case here would otherwise silently shorten a referenced set, and a
// short set is a WITH drop that goes unnoticed. unmodelled names the shape, so
// the refusal tells an operator which one.
type varScan struct {
	names      map[string]struct{}
	nodes      map[string]struct{}
	ok         bool
	unmodelled string
}

func newVarScan() *varScan {
	return &varScan{names: map[string]struct{}{}, nodes: map[string]struct{}{}, ok: true}
}

func (s *varScan) refuse(shape any) {
	if !s.ok {
		return
	}
	s.ok = false
	s.unmodelled = fmt.Sprintf("a %T", shape)
}

func (s *varScan) name(n string) {
	if n != "" {
		s.names[n] = struct{}{}
	}
}

func (s *varScan) clause(c Clause) {
	switch cl := c.(type) {
	case *Match:
		for _, p := range cl.Patterns {
			s.pattern(p)
		}
		s.expr(cl.Where)
	case *With:
		for _, it := range cl.Items {
			s.expr(it.Expr)
		}
		s.expr(cl.Where)
	case *Return:
		for _, it := range cl.Items {
			s.expr(it.Expr)
		}
	default:
		s.refuse(c)
	}
}

func (s *varScan) pattern(p PathPattern) {
	for _, n := range p.Nodes {
		s.name(n.Variable)
		if n.Variable != "" {
			s.nodes[n.Variable] = struct{}{}
		}
		for _, v := range n.Properties {
			s.expr(v)
		}
	}
	for _, r := range p.Rels {
		s.name(r.Variable)
		for _, v := range r.Properties {
			s.expr(v)
		}
	}
}

func (s *varScan) expr(e Expr) {
	switch x := e.(type) {
	case nil, *Literal, *ParameterRef:
	case *VariableRef:
		s.name(x.Name)
	case *PropertyAccess:
		s.expr(x.Target)
	case *BinaryOp:
		s.expr(x.Left)
		s.expr(x.Right)
	case *AndOr:
		for _, op := range x.Operands {
			s.expr(op)
		}
	case *Not:
		s.expr(x.Operand)
	case *PatternExpr:
		s.pattern(x.Pattern)
	case *PatternComprehension:
		// The optional path binding is comprehension-local — the outer row never
		// holds it — so counting it can only make some later WITH read the name
		// as dropped and refuse. That is the safe direction, and cheaper than
		// reasoning about whether a lens could reuse the name downstream.
		s.name(x.Variable)
		s.pattern(x.Pattern)
		s.expr(x.Where)
		s.expr(x.Projection)
	case *FunctionCall:
		for _, a := range x.Args {
			s.expr(a)
		}
	case *MapLiteral:
		for _, v := range x.Values {
			s.expr(v)
		}
	case *ListLiteral:
		for _, el := range x.Elements {
			s.expr(el)
		}
	case *CaseExpr:
		for _, alt := range x.Alternatives {
			s.expr(alt.When)
			s.expr(alt.Then)
		}
		s.expr(x.Else)
	default:
		s.refuse(e)
	}
}
