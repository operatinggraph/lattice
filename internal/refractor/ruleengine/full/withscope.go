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
//     the graph gains a hop that no single row ever walks. Unless the
//     re-reference re-walks the very chain that bound the name (below), the real
//     dependency is then a BUCKET rather than a link, so an adjacency walk
//     cannot see it and the derived anchor set is SMALLER than the truth — on
//     the auth plane, a revocation that never reprojects.
//   - A dropped name no later clause mentions strands nothing. Every position
//     the walk uses keeps its hops, and the anchor is the `$actorKey` PARAMETER
//     rather than a row column, so a WITH may drop even the anchor's own
//     variable and leave the graph exactly as walkable as before.
//
// So the refusal is not "the query carries a WITH" but "a WITH dropped a name a
// later clause still uses ANY OTHER WAY than by re-walking the very chain that
// bound it".
//
// That last clause is the one narrowing, and it is what the read-grant
// producers need: pkgmgr's generateProducerSpec stages one WITH per walk and
// several walks re-open the same residence chain, so a name is dropped at one
// boundary and re-bound at the next by a textually identical MATCH from a
// carried head. Both hazards above are closed for that shape, not merely the
// second one. The head is still bound AT THE POINT THE EXECUTOR REACHES THE
// PATTERN — the judgement is positional, pattern by pattern, for the reason
// judgeMatch states — so executor.matchPath walks the re-binding from it rather
// than seeding a whole-bucket scan, and the row's dependency really is the link
// path. And because the chain is identical through named intermediates,
// hopIndexBuilder.position merges the two occurrences by name into one position
// whose incident hops are already the same hops: Hops gains duplicates only,
// Dist (computed from Hops) is unchanged, and AnchorSideSeeds — Dist's only
// consumer — drops no seed. judgeMatch states the conditions and each one's
// reason, and admitPattern applies them.
//
// Everything the model cannot resolve exactly refuses with its own reason
// instead: over-refusing costs one BFS fallback, and under-refusing costs a
// grant that outlives its revocation.

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
	// dropped is every name some WITH has since let go of. A name once stranded
	// stays stranded until a later clause re-binds it over the very chain that
	// bound it in the first place, which is what makes a drop at one boundary
	// and a re-reference after a LATER one still read as the hazard it is.
	//
	// bound records HOW each name was introduced, so a re-reference can be
	// checked against it rather than guessed at.
	seen := map[string]struct{}{}
	dropped := map[string]struct{}{}
	bound := map[string]nodeBinding{}

	for _, c := range clauses {
		w, isWith := c.(*With)
		if !isWith {
			s := newVarScan()
			s.clause(c)
			if m, isMatch := c.(*Match); isMatch {
				if v := judgeMatch(m, seen, dropped, bound); v != "" {
					return withReReference(v)
				}
			} else if v := firstIn(s.names, dropped); v != "" {
				// A RETURN reads the scope the last boundary left; it binds
				// nothing, so it has no re-binding to judge positionally.
				return withReReference(v)
			}
			absorb(seen, s.names)
			recordBindings(bound, c, s.names)
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

		// A boundary introduces names of its own — a projected alias, and any
		// name its WHERE binds — and none of them is a chain a re-reference can
		// be re-checked against, so each is recorded as inadmissible unless a
		// MATCH pattern already recorded it.
		recordBindings(bound, c, carried)
		recordBindings(bound, c, items.names)
		recordBindings(bound, c, where.names)
	}
	return ""
}

// judgeMatch returns the stranded name m re-references without re-binding it,
// or "" when every one of them is admitted.
//
// IT IS POSITIONAL, PATTERN BY PATTERN, AND THAT IS THE WHOLE CORRECTNESS OF
// IT. matchPath evaluates a clause's patterns left to right, so a name pattern
// k reads is bound by whatever ran BEFORE pattern k — a re-binding written at
// pattern k+1 happens strictly after the use and cannot excuse it. Judging the
// clause's names as one flat set would do exactly that, and
// `OPTIONAL MATCH (u:unit)-[:locatedAt]->(w:studio), (a)-[:residesIn]->(u:unit)`
// is the shape it lets through: pattern 1 runs with `u` unbound and bucket-scans
// the whole unit bucket, and when pattern 2 yields nothing nullBindNewVars
// leaves that scanned binding in place (it nulls only variables NOT already
// bound), so the row survives carrying a `u` no link relates to the anchor. The
// index would call that Complete while the derived anchor set misses every
// resident of every other unit.
//
// So each pattern is judged against `dropped` as it stands the moment the
// executor reaches it — its own admissions applied first, since a pattern may
// re-bind the very name it goes on to use — and only then does the next pattern
// see them. The one part of a pattern that runs BEFORE its own admissions is its
// HEAD node's property map, which matchPath evaluates at the seed rather than
// after the hop; it is judged first for that reason. The clause's WHERE is
// judged last, against the final state, because applyMatch evaluates it only
// once matchPatterns has returned.
//
// The reported name is deterministic: the first pattern that refuses wins (AST
// order), and within it the lexically first refusable name (firstIn).
//
// The four things a re-binding has to be before it is admitted, and why each
// one is load-bearing:
//
//   - It sits in a MATCH clause's own PATTERN list, at a NON-HEAD node
//     position. A head position is the seed matchPath scans a bucket for; a
//     reference from a WHERE, a projection item, or an expression-embedded
//     pattern binds nothing the outer row keeps.
//   - The pattern's HEAD is a named variable already in scope at this pattern.
//     That is what makes the re-binding a walk rather than a scan: matchPath
//     starts at Nodes[0], so a bound head means the row's dependency really is
//     the link path.
//   - The chain from that head to the position — every relationship and every
//     intervening node, plus the position itself — is identical to the chain
//     that first bound the name, from the same head VARIABLE NAME, field for
//     field (sameRelPattern / sameNodePattern).
//   - That first binding was itself a single unambiguous non-head introduction
//     through NAMED intermediates (nodeBinding.admissible).
//
// Identity of the chain is what closes the merge hazard the package doc names.
// hopIndexBuilder.position merges the two occurrences by name into ONE position
// whose incident hops are already the same hops, so Hops gains only duplicates,
// Dist — computed from Hops — is unchanged, and AnchorSideSeeds, Dist's only
// consumer, drops no seed. The graph the walk reads is the graph it read
// before the re-binding existed.
func judgeMatch(m *Match, seen, dropped map[string]struct{}, bound map[string]nodeBinding) string {
	for _, p := range m.Patterns {
		// The HEAD node's own property map is judged before this pattern's
		// admissions, and the rest of the pattern after. matchPath evaluates the
		// head's properties at the SEED (propsAllMatch), before it crosses the
		// hop that re-binds anything, so `(a {tag: u.key})-[:residesIn]->(u)`
		// reads `u` while it is still unbound however cleanly the hop beside it
		// re-binds it. Judging the whole pattern after its own admissions would
		// let that read through.
		if len(p.Nodes) > 0 {
			hs := newVarScan()
			for _, v := range p.Nodes[0].Properties {
				hs.expr(v)
			}
			if v := firstIn(hs.names, dropped); v != "" {
				return v
			}
		}

		admitPattern(p, seen, dropped, bound)

		// Everything else this pattern reads: its node and relationship
		// variables, and every expression in the remaining property maps — a
		// property value is evaluated while the pattern is walked
		// (propsAllMatch → evalExpr), so a stranded name reaching one is read
		// exactly as early as a bare reference is.
		ps := newVarScan()
		ps.pattern(p)
		if v := firstIn(ps.names, dropped); v != "" {
			return v
		}

		// A name this pattern introduces is in scope for the next one in the
		// same clause, which is the head test in admitPattern reading the
		// executor's own left-to-right evaluation order. A name still in
		// dropped stays there: it is seen but stranded, and the head test
		// refuses it.
		for _, n := range p.Nodes {
			if n.Variable != "" {
				seen[n.Variable] = struct{}{}
			}
		}
	}

	where := newVarScan()
	where.expr(m.Where)
	return firstIn(where.names, dropped)
}

// admitPattern returns to scope every stranded name p re-binds over exactly the
// chain that bound it in the first place. It runs before p's own references are
// judged, because a pattern may re-bind the very name it then uses; it never
// looks past p, because the next pattern has not run yet.
func admitPattern(p PathPattern, seen, dropped map[string]struct{}, bound map[string]nodeBinding) {
	if len(p.Nodes) == 0 {
		return
	}
	// An ANONYMOUS head names nothing, and a head no clause has yet put in
	// scope is not bound when this pattern runs; either way matchPath seeds the
	// pattern from a bucket rather than walking it, so it admits nothing. The
	// two are one test because "" is never a member of seen — every site that
	// inserts a name skips the empty one — so an anonymous head reads as
	// not-in-scope on its own.
	head := p.Nodes[0].Variable
	if _, headSeen := seen[head]; head == "" || !headSeen {
		return
	}
	// A head the boundary stranded is bound by a bucket scan of its own, and
	// everything reached from it inherits that.
	if _, headStranded := dropped[head]; headStranded {
		return
	}
	for i := 1; i < len(p.Nodes); i++ {
		v := p.Nodes[i].Variable
		if v == "" {
			continue
		}
		if _, stranded := dropped[v]; !stranded {
			continue
		}
		if !rebindsIdentically(bound[v], head, p, i) {
			continue
		}
		delete(dropped, v)
		seen[v] = struct{}{}
	}
}

// rebindsIdentically reports whether p's chain from head to Nodes[i] repeats
// b's chain exactly. Relationship variables never reach here — only node
// positions are re-admitted — so a stranded relationship name is always
// refused.
func rebindsIdentically(b nodeBinding, head string, p PathPattern, i int) bool {
	if !b.admissible || b.head != head {
		return false
	}
	if len(b.rels) != i || len(b.nodes) != i || i > len(p.Rels) {
		return false
	}
	for k := 0; k < i; k++ {
		if !sameRelPattern(b.rels[k], p.Rels[k]) {
			return false
		}
		if !sameNodePattern(b.nodes[k], p.Nodes[k+1]) {
			return false
		}
	}
	return true
}

// nodeBinding records HOW a name was introduced: the variable heading the MATCH
// pattern that bound it, and the exact chain of relationship and node patterns
// running from that head to the position carrying the name. It is what a later
// re-reference of a stranded name is checked against.
//
// admissible is false for every introduction this walk cannot re-check that
// way — a name bound at a pattern's HEAD (there is no chain to repeat), a
// relationship variable, a name an expression-embedded pattern or a projection
// introduces, a name two patterns bound over chains that differ, and a chain
// running through an UNNAMED intermediate node. A re-reference of such a name
// is refused outright.
//
// The unnamed-intermediate case is what keeps "the re-binding adds only
// duplicate hops" literally true. hopIndexBuilder.position merges by NAME and
// mints a FRESH class for every sighting of an anonymous node, so a chain like
// `(a)-[:residesIn]->(:place)-[:containedIn]->(z)` re-opened after a boundary
// would add a position and two hops that are not duplicates of anything — a
// second, parallel route between the same two named ends. Nothing found makes
// that route unsound on its own, but the soundness argument this whole
// admission rests on is the duplicate-only claim, so a shape that falsifies the
// claim is refused rather than argued about separately. The generated producers
// name every intermediate, so this costs the corpus nothing.
type nodeBinding struct {
	head       string
	rels       []RelPattern
	nodes      []NodePattern
	admissible bool
}

// recordBindings folds c's own introductions into bound. referenced is the set
// of names the caller's scan of c collected: every one of them that no MATCH
// pattern position accounts for is recorded as inadmissible, so a name whose
// provenance this walk never read exactly can never be re-admitted later.
func recordBindings(bound map[string]nodeBinding, c Clause, referenced map[string]struct{}) {
	if m, isMatch := c.(*Match); isMatch {
		for _, p := range m.Patterns {
			head := ""
			if len(p.Nodes) > 0 {
				head = p.Nodes[0].Variable
			}
			for i, n := range p.Nodes {
				if n.Variable == "" {
					continue
				}
				if i == 0 {
					// A head position introduces a name only when nothing has
					// bound it yet, and then by a bucket scan with no chain to
					// repeat. Where the name is already bound this is a
					// REFERENCE to that binding — matchPath starts the walk
					// there — so it must not disturb the chain already recorded.
					noteFirstBinding(bound, n.Variable, nodeBinding{})
					continue
				}
				if head == "" || i > len(p.Rels) || !allNamed(p.Nodes[1:i]) {
					noteBinding(bound, n.Variable, nodeBinding{})
					continue
				}
				noteBinding(bound, n.Variable, nodeBinding{
					head:       head,
					rels:       p.Rels[:i],
					nodes:      p.Nodes[1 : i+1],
					admissible: true,
				})
			}
		}
	}
	for name := range referenced {
		noteFirstBinding(bound, name, nodeBinding{})
	}
}

// allNamed reports whether every node in nodes carries a variable — the test
// for the intermediates of a binding chain, which nodeBinding's own doc states
// the reason for.
func allNamed(nodes []NodePattern) bool {
	for _, n := range nodes {
		if n.Variable == "" {
			return false
		}
	}
	return true
}

// noteFirstBinding records b only for a name nothing has introduced yet. Every
// later sighting of the name is a reference to the binding already recorded.
func noteFirstBinding(bound map[string]nodeBinding, name string, b nodeBinding) {
	if name == "" {
		return
	}
	if _, known := bound[name]; !known {
		bound[name] = b
	}
}

// noteBinding keeps the FIRST introduction of a name and demotes it to
// inadmissible the moment a second one disagrees with it. Two patterns binding
// one name over different chains leave nothing a re-reference could be checked
// against — whichever chain it repeats, the other occurrence is a different
// binding that hopIndexBuilder.position would nonetheless merge onto it.
func noteBinding(bound map[string]nodeBinding, name string, b nodeBinding) {
	prev, known := bound[name]
	if !known {
		bound[name] = b
		return
	}
	if !prev.admissible || !b.admissible || !sameBinding(prev, b) {
		bound[name] = nodeBinding{}
	}
}

// sameBinding reports whether two introductions of one name are the same head
// and the same chain, field for field.
func sameBinding(a, b nodeBinding) bool {
	if a.head != b.head || len(a.rels) != len(b.rels) || len(a.nodes) != len(b.nodes) {
		return false
	}
	for i := range a.rels {
		if !sameRelPattern(a.rels[i], b.rels[i]) {
			return false
		}
	}
	for i := range a.nodes {
		if !sameNodePattern(a.nodes[i], b.nodes[i]) {
			return false
		}
	}
	return true
}

// sameNodePattern and sameRelPattern compare every field the AST carries for
// the element (ast.go's NodePattern / RelPattern), the intermediate VARIABLE
// NAMES included. That is stricter than "the same relation types, directions,
// ranges and labels": two chains spelled with different intermediate names bind
// different positions in the pattern graph, and the whole point of the
// comparison is that the re-binding lands on the positions the first binding
// already established.
func sameNodePattern(a, b NodePattern) bool {
	return a.Variable == b.Variable &&
		a.Label == b.Label &&
		a.LabelExpand == b.LabelExpand &&
		samePropertyMaps(a.Properties, b.Properties)
}

func sameRelPattern(a, b RelPattern) bool {
	return a.Variable == b.Variable &&
		a.Type == b.Type &&
		a.Direction == b.Direction &&
		a.MinHops == b.MinHops &&
		a.MaxHops == b.MaxHops &&
		samePropertyMaps(a.Properties, b.Properties)
}

func samePropertyMaps(a, b map[string]Expr) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, present := b[k]
		if !present || !sameExpr(av, bv) {
			return false
		}
	}
	return true
}

// sameExpr reports whether two property-map expressions are the same
// expression, structurally. It answers FALSE for any shape it cannot decide
// exactly — an embedded pattern among them — so a chain it cannot compare is
// not identical, and the re-reference standing on it is refused. Same
// default-deny disposition as varScan.refuse, reached the same way: what the
// walk cannot resolve costs one BFS fallback, and what it guesses at costs a
// grant that outlives its revocation.
func sameExpr(a, b Expr) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	switch x := a.(type) {
	case *Literal:
		// A typed-nil *Literal is not `e == nil`, so the guard above does not
		// catch it and reading Value would panic. No visitor path produces one;
		// answering "not identical" is what the rest of this predicate does with
		// everything it cannot decide.
		y, ok := b.(*Literal)
		if !ok || x == nil || y == nil {
			return false
		}
		return sameLiteralValue(x.Value, y.Value)
	case *ParameterRef:
		y, ok := b.(*ParameterRef)
		return ok && x.Name == y.Name
	case *VariableRef:
		y, ok := b.(*VariableRef)
		return ok && x.Name == y.Name
	case *PropertyAccess:
		y, ok := b.(*PropertyAccess)
		return ok && x.Key == y.Key && sameExpr(x.Target, y.Target)
	case *BinaryOp:
		y, ok := b.(*BinaryOp)
		return ok && x.Op == y.Op && sameExpr(x.Left, y.Left) && sameExpr(x.Right, y.Right)
	case *AndOr:
		y, ok := b.(*AndOr)
		return ok && x.Op == y.Op && sameExprs(x.Operands, y.Operands)
	case *Not:
		y, ok := b.(*Not)
		return ok && sameExpr(x.Operand, y.Operand)
	case *FunctionCall:
		y, ok := b.(*FunctionCall)
		if !ok || x.Name != y.Name || x.Distinct != y.Distinct || len(x.Namespace) != len(y.Namespace) {
			return false
		}
		for i := range x.Namespace {
			if x.Namespace[i] != y.Namespace[i] {
				return false
			}
		}
		return sameExprs(x.Args, y.Args)
	case *MapLiteral:
		y, ok := b.(*MapLiteral)
		if !ok || len(x.Keys) != len(y.Keys) {
			return false
		}
		for i := range x.Keys {
			if x.Keys[i] != y.Keys[i] {
				return false
			}
		}
		return samePropertyMaps(x.Values, y.Values)
	case *ListLiteral:
		y, ok := b.(*ListLiteral)
		return ok && sameExprs(x.Elements, y.Elements)
	case *CaseExpr:
		y, ok := b.(*CaseExpr)
		if !ok || len(x.Alternatives) != len(y.Alternatives) {
			return false
		}
		for i := range x.Alternatives {
			if !sameExpr(x.Alternatives[i].When, y.Alternatives[i].When) ||
				!sameExpr(x.Alternatives[i].Then, y.Alternatives[i].Then) {
				return false
			}
		}
		return sameExpr(x.Else, y.Else)
	default:
		return false
	}
}

func sameExprs(a, b []Expr) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameExpr(a[i], b[i]) {
			return false
		}
	}
	return true
}

// sameLiteralValue compares the primitive values a Literal is allowed to hold
// (ast.go: bool, int64, float64, string, nil). Anything else answers false
// rather than reaching `==`, which panics on an uncomparable dynamic type.
func sameLiteralValue(a, b any) bool {
	switch av := a.(type) {
	case nil:
		return b == nil
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case int64:
		bv, ok := b.(int64)
		return ok && av == bv
	case float64:
		bv, ok := b.(float64)
		return ok && av == bv
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	default:
		return false
	}
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
