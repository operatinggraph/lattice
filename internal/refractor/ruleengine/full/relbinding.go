package full

// Relationship bindings — what a lens may read off a relationship variable,
// and the refusal for everything else.
//
// A walk binds a relationship variable to the LINK it crossed: the link's
// Contract #1 key and the relation name, both already in the adjacency entry.
// The projectable surface is `type(r)` and `r.key` from the binding itself,
// plus `r.data.<field>` off a point-read of the link document. Every other
// dereference resolves to Cypher NULL, and a null is the one answer an author
// cannot tell apart from a row that genuinely had no value: the column just
// comes out empty, with no diagnostic anywhere. So the shapes with no value to
// give are refused at PARSE, where the author is still looking at the query,
// rather than executed into a silent column of nulls.
//
// Four shapes are refused:
//
//   - A relationship variable on a variable-length hop. A multi-hop expansion
//     crosses a different number of links per row and there is no single one to
//     bind; `*0..` admits the source vertex itself, crossing none at all.
//   - A dereference of a bound relationship variable other than `.key` or
//     `.data`.
//   - A BARE use of a relationship variable anywhere but a WITH item — `RETURN
//     r`, `count(r)`, `collect(r)`, `r` in a grouping key. A relationship is
//     bound for its identity and its payload, not as a value: rendered it is an
//     empty object, counted it silently changes what the count means.
//   - A reference to a name a WITH stopped carrying. The name is unbound there,
//     so every read of it is the same diagnosis-free null — and "the WITH forgot
//     to carry `r`" is the likeliest way to write one.
//
// Every position an expression can be evaluated from is walked: a clause's
// WHERE, its projection items, and the inline property maps of its patterns
// (which seedNodes and propsAllMatch evaluate). A WITH item that could hand the
// binding on carries it under the item's output name, so a read on the far side
// of a rename is judged as the read of a relationship it is.
//
// The walk is inert for a query that binds no relationship variable, which is
// nearly the whole corpus: it collects the bindings first and returns before
// looking at a single expression when there are none.
//
// It is the PRIMARY gate, not the only one. resolveProperty applies the same
// projectable-property rule at evaluation, so a shape that ever reaches it by
// another route errors rather than serving a link's envelope.

import (
	"fmt"
	"sort"
	"strings"
)

// RelBinding is one relationship variable a query's patterns bind, and what
// the query reads off it.
type RelBinding struct {
	// Variable is the name the pattern binds the relationship to.
	Variable string
	// Type is the relation the hop filters on, and is empty for an untyped hop
	// — which binds whichever relation the walk actually crossed.
	Type string
	// Reads names what the query takes off the binding, sorted: "type" for the
	// relation name, "key" for the link key, "data" for the link's payload. The
	// first two are free; "data" is a Core-KV point-read per traversed edge, so
	// this is the read-cost surface of naming a relationship as well as its
	// projection surface.
	Reads []string
}

// RelBindings returns the relationship variables this rule's patterns bind and
// what it reads off each, sorted by variable name. It is the diagnostic form
// of the same walk Parse gates on, so a census can pin which lenses bind a
// relationship — and which pay for a link read — rather than deriving that
// population from the cypher text.
func (cr *CompiledRule) RelBindings() []RelBinding {
	collected, _ := collectRelBindings(cr.Query)
	surface, _ := relSurfaceWalk(cr.Query, collected)
	out := make([]RelBinding, 0, len(collected))
	for name, rb := range collected {
		entry := rb.RelBinding
		for read := range surface[name] {
			entry.Reads = append(entry.Reads, read)
		}
		sort.Strings(entry.Reads)
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Variable < out[j].Variable })
	return out
}

// relBindingReject returns the reason to refuse q, or "" when every
// relationship variable it binds is bindable and every dereference of one
// resolves to a value.
func relBindingReject(q *Query) string {
	bound, modelled := collectRelBindings(q)
	if !modelled {
		// An expression shape this walk cannot enumerate may hide either half of
		// what it judges — a pattern binding a relationship on a variable-length
		// hop, or a read off one. It is judged BEFORE the "binds nothing" exit,
		// because a shape the walk cannot see into is exactly where an unseen
		// binding would be: concluding "binds nothing" from a walk that did not
		// finish is the fail-open answer.
		//
		// Every expression and clause the AST defines has a case in this walk, so
		// this is reachable only from a node type added to the AST without one —
		// which is the point: a new node type fails loudly here rather than
		// silently widening what a relationship may be read for.
		return "this query carries a shape the relationship-binding walk cannot enumerate, so a " +
			"relationship binding it may hide — or a read off one — cannot be ruled out"
	}
	if len(bound) == 0 {
		return ""
	}
	for _, name := range sortedRelVarNames(bound) {
		rb := bound[name]
		if rb.varLength {
			return fmt.Sprintf(
				"relationship variable `%s` is bound on a variable-length hop (%s) — an expansion of "+
					"several hops crosses no single relationship, and a zero-hop one crosses none at all, "+
					"so there is nothing to bind; drop the variable or write a fixed single hop",
				name, rb.hops())
		}
	}
	_, reject := relSurfaceWalk(q, bound)
	return reject
}

// relBinding is one collected binding plus what the collection observed about
// the hop that carries it.
type relBinding struct {
	RelBinding
	varLength bool
	minHops   int
	maxHops   int
}

func (rb relBinding) hops() string {
	if rb.maxHops < 0 {
		return fmt.Sprintf("*%d..", rb.minHops)
	}
	return fmt.Sprintf("*%d..%d", rb.minHops, rb.maxHops)
}

// collectRelBindings returns every relationship variable q's patterns bind,
// keyed by variable name, and whether the walk saw the whole query. A name
// bound by more than one pattern keeps the first hop's shape, and a
// variable-length hop anywhere on the name wins — the refusal is about the
// name, so the strictest sighting of it is the one that decides.
func collectRelBindings(q *Query) (map[string]relBinding, bool) {
	bound := map[string]relBinding{}
	modelled := true
	if q == nil {
		return bound, true
	}
	for _, c := range q.Clauses {
		if !forEachPatternInClause(c, func(p PathPattern) {
			for _, r := range p.Rels {
				if r.Variable == "" {
					continue
				}
				varLength := r.MinHops != 1 || r.MaxHops != 1
				if _, seen := bound[r.Variable]; seen && !varLength {
					continue
				}
				bound[r.Variable] = relBinding{
					RelBinding: RelBinding{Variable: r.Variable, Type: r.Type},
					varLength:  varLength,
					minHops:    r.MinHops,
					maxHops:    r.MaxHops,
				}
			}
		}) {
			modelled = false
		}
	}
	return bound, modelled
}

func sortedRelVarNames(bound map[string]relBinding) []string {
	names := make([]string, 0, len(bound))
	for name := range bound {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// relSurfaceWalk walks the query in clause order and answers both questions
// about its relationship variables at once: what it READS off each (keyed by
// the pattern variable that bound it), and the reason to refuse the first use
// that has no value behind it.
//
// Every position the executor evaluates an expression from is walked: a
// clause's WHERE, its projection items, and its patterns' inline property maps
// (seedNodes and propsAllMatch evaluate those against the current binding, so a
// relationship read there reaches Core KV exactly as one in a RETURN does).
//
// Scope follows the executor's. A MATCH adds the relationship variables its own
// patterns bind. A WITH rebuilds every row from its projection items alone, so
// a relationship survives it only under the name an item gives it, a name no
// item projects is UNBOUND afterwards, and a carried name maps back to the
// pattern variable it came from — a read through a rename is a read of the
// relationship it actually is.
func relSurfaceWalk(q *Query, bound map[string]relBinding) (map[string]map[string]struct{}, string) {
	surface := map[string]map[string]struct{}{}
	if q == nil {
		return surface, ""
	}
	// inScope maps a name currently bound to a relationship to the pattern
	// variable it originates from; dropped holds the names that were such a
	// binding and are no longer bound to anything.
	inScope := map[string]string{}
	dropped := map[string]struct{}{}
	record := func(origin, read string) {
		if surface[origin] == nil {
			surface[origin] = map[string]struct{}{}
		}
		surface[origin][read] = struct{}{}
	}
	for _, c := range q.Clauses {
		// A pattern re-introduces every variable it names, relationship or node,
		// so a name a later MATCH binds afresh is no longer the dropped one.
		forEachPatternInClause(c, func(p PathPattern) {
			for _, n := range p.Nodes {
				delete(dropped, n.Variable)
			}
			for _, r := range p.Rels {
				if r.Variable == "" {
					continue
				}
				delete(dropped, r.Variable)
				if _, isRel := bound[r.Variable]; isRel {
					inScope[r.Variable] = r.Variable
				}
			}
		})

		// A bare relationship variable is a carry only as a WITH item's whole
		// expression; everywhere else it is a use of the relationship as a value.
		bareIsCarry := false
		var exprs []Expr
		switch cl := c.(type) {
		case *Match:
			exprs = append(exprs, cl.Where)
			for _, p := range cl.Patterns {
				exprs = append(exprs, patternPropertyExprs(p)...)
			}
		case *With:
			bareIsCarry = true
			for _, it := range cl.Items {
				exprs = append(exprs, it.Expr)
			}
		case *Return:
			for _, it := range cl.Items {
				exprs = append(exprs, it.Expr)
			}
		}
		for _, e := range exprs {
			if reject := walkRelUses(e, inScope, dropped, record, bareIsCarry); reject != "" {
				return surface, reject
			}
		}

		if cl, isWith := c.(*With); isWith {
			carried := carriedRelVars(cl, inScope)
			projected := withOutputNames(cl)
			for name := range inScope {
				if _, stillNamed := projected[name]; !stillNamed {
					dropped[name] = struct{}{}
				}
			}
			// A name this WITH projects is bound again on the far side of it,
			// whatever it held before — a value under an old relationship's
			// name is a value, not a stranded binding.
			for name := range projected {
				delete(dropped, name)
			}
			inScope = carried
			// A WITH's own WHERE filters the rows it has already projected, so it
			// reads the carried scope, not the incoming one.
			if reject := walkRelUses(cl.Where, inScope, dropped, record, false); reject != "" {
				return surface, reject
			}
		}
	}
	return surface, ""
}

// patternPropertyExprs returns every expression a pattern evaluates: the inline
// property maps on its nodes and relationships.
func patternPropertyExprs(p PathPattern) []Expr {
	var out []Expr
	for _, n := range p.Nodes {
		for _, v := range n.Properties {
			out = append(out, v)
		}
	}
	for _, r := range p.Rels {
		for _, v := range r.Properties {
			out = append(out, v)
		}
	}
	return out
}

// withOutputNames returns every name w's projection puts in scope, by the same
// naming executor.projectItems applies — alias when present, the item's own
// auto-alias otherwise. A name absent from this set is unbound after w.
func withOutputNames(w *With) map[string]struct{} {
	out := make(map[string]struct{}, len(w.Items))
	for i, it := range w.Items {
		name := it.Alias
		if name == "" {
			name = projectionAutoAlias(it.Expr, i)
		}
		out[name] = struct{}{}
	}
	return out
}

// carriedRelVars returns the relationship bindings still bound after w, under
// the names w's projection gives them, each still mapped to the pattern
// variable it came from.
//
// The question is asked the safe way round: an item carries the BINDING unless
// its value provably is not one. A property access and a type() call yield a
// scalar; a bare variable is the binding itself; anything else that so much as
// mentions a relationship is assumed to hand it on, because more than one
// executor arm returns its argument unchanged (coalesce returns the argument it
// selects, a CASE returns the branch it takes). Enumerating the shapes that DO
// pass a binding through is how a gate acquires a hole.
//
// walkRelUses refuses every one of those "anything else" items before this runs
// — a relationship is not a value — so today only the bare-variable arm can
// carry. The conservative arm is what keeps that true if the refusal is ever
// relaxed.
func carriedRelVars(w *With, inScope map[string]string) map[string]string {
	carried := map[string]string{}
	for i, it := range w.Items {
		origin, carries := itemCarriesRelBinding(it.Expr, inScope)
		if !carries {
			continue
		}
		name := it.Alias
		if name == "" {
			name = projectionAutoAlias(it.Expr, i)
		}
		carried[name] = origin
	}
	return carried
}

// itemCarriesRelBinding reports whether e hands a relationship binding on, and
// which pattern variable's binding it is. A mention of more than one takes the
// first by name: such an item is refused as a value-use before it is reached,
// so the choice only ever decides an attribution nothing observes.
func itemCarriesRelBinding(e Expr, inScope map[string]string) (string, bool) {
	if vr, isVar := e.(*VariableRef); isVar {
		origin, isRel := inScope[vr.Name]
		return origin, isRel
	}
	if exprYieldsAValue(e) {
		return "", false
	}
	origins := relOriginsMentioned(e, inScope)
	if len(origins) == 0 {
		return "", false
	}
	return origins[0], true
}

// exprYieldsAValue reports whether e's result is a value in its own right — a
// scalar, a list, a map — rather than possibly being one of its operands handed
// back unchanged.
//
// The enumeration is deliberately on THIS side. A shape nobody listed defaults
// to "may hand its operand on", so a function added later that returns an
// argument — as coalesce does, as max/min do, as a CASE does by taking a branch
// — is carried rather than silently dropped from the gate's view. Listing the
// pass-through shapes instead would make every future addition a hole.
func exprYieldsAValue(e Expr) bool {
	switch x := e.(type) {
	case *Literal, *ParameterRef, *PropertyAccess, *BinaryOp, *AndOr, *Not,
		*PatternExpr, *MapLiteral, *ListLiteral, *PatternComprehension:
		// A map or a list may CONTAIN a binding, but the item's own value is
		// the container; navigating back into one lands in resolveProperty,
		// which applies the same projectable-property rule this walk does.
		return true
	case *FunctionCall:
		switch strings.ToLower(x.Name) {
		case "type", "count", "collect", "levenshteindist", "levenshteinratio", "nanoidfromkey":
			return true
		}
		return false
	}
	return false
}

// relOriginsMentioned returns the pattern variables whose relationship bindings
// e references anywhere, sorted.
func relOriginsMentioned(e Expr, inScope map[string]string) []string {
	seen := map[string]struct{}{}
	forEachExpr(e, func(sub Expr) {
		vr, isVar := sub.(*VariableRef)
		if !isVar {
			return
		}
		if origin, isRel := inScope[vr.Name]; isRel {
			seen[origin] = struct{}{}
		}
	})
	out := make([]string, 0, len(seen))
	for origin := range seen {
		out = append(out, origin)
	}
	sort.Strings(out)
	return out
}

// walkRelUses records every read e makes off an in-scope relationship variable
// and reports the first use that has no value behind it.
//
// bareIsCarry exempts e ITSELF when it is a bare relationship variable — the
// WITH-item position, where a bare variable carries the binding forward rather
// than using it as a value. A bare variable anywhere else is refused.
//
// The walk is pre-order, so a node that legitimises a nested variable reference
// (the target of an allowed dereference, the argument of type()) is always seen
// before that reference and marks it exempt.
func walkRelUses(e Expr, inScope map[string]string, dropped map[string]struct{},
	record func(origin, read string), bareIsCarry bool,
) string {
	reject := ""
	exempt := map[Expr]struct{}{}
	forEachExpr(e, func(sub Expr) {
		if reject != "" {
			return
		}
		switch x := sub.(type) {
		case *PropertyAccess:
			vr, isVar := x.Target.(*VariableRef)
			if !isVar {
				return
			}
			if _, wasRel := dropped[vr.Name]; wasRel {
				reject = droppedRelReject(vr.Name)
				return
			}
			origin, isRel := inScope[vr.Name]
			if !isRel {
				return
			}
			exempt[x.Target] = struct{}{}
			if !relPropertyProjectable(x.Key) {
				reject = fmt.Sprintf(
					"relationship variable `%s` is dereferenced as `%s.%s` — a bound relationship projects "+
						"its link key (`%s.key`), its payload (`%s.data.<field>`) and its relation name "+
						"(`type(%s)`), and any other property would render as a silent null",
					vr.Name, vr.Name, x.Key, vr.Name, vr.Name, vr.Name)
				return
			}
			record(origin, x.Key)
		case *FunctionCall:
			if !strings.EqualFold(x.Name, "type") || len(x.Args) != 1 {
				return
			}
			vr, isVar := x.Args[0].(*VariableRef)
			if !isVar {
				return
			}
			if _, wasRel := dropped[vr.Name]; wasRel {
				reject = droppedRelReject(vr.Name)
				return
			}
			if origin, isRel := inScope[vr.Name]; isRel {
				exempt[x.Args[0]] = struct{}{}
				record(origin, "type")
			}
		case *VariableRef:
			if _, ok := exempt[sub]; ok {
				return
			}
			if _, wasRel := dropped[x.Name]; wasRel {
				reject = droppedRelReject(x.Name)
				return
			}
			if _, isRel := inScope[x.Name]; !isRel {
				return
			}
			if bareIsCarry && sub == e {
				// `WITH r` / `WITH r AS link`: the item carries the binding
				// forward under its output name, which stays gated there.
				return
			}
			reject = fmt.Sprintf(
				"relationship variable `%s` is used as a value — a bound relationship is not one. It "+
					"renders as an empty object where a row expects a value, and counting or collecting it "+
					"changes what the aggregate means; project `type(%s)`, `%s.key` or `%s.data.<field>` "+
					"instead",
				x.Name, x.Name, x.Name, x.Name)
		}
	})
	return reject
}

func droppedRelReject(name string) string {
	return fmt.Sprintf(
		"relationship variable `%s` is referenced after a WITH that does not carry it — the name is "+
			"unbound there, so every read of it renders as a silent null; carry `%s` through the WITH "+
			"or drop the reference",
		name, name)
}

// relPropertyProjectable reports whether a property named off a bound
// relationship resolves to a value. `key` is the link's Contract #1 key, held
// in the binding itself and free; `data` is the link's own payload, which
// costs one point-read of the link document per dereferenced edge. Every other
// name is refused rather than served — a lens reads a relationship for its
// identity and its payload, and the rest of a link's envelope is either
// already in the key or is provenance no consumer projects.
func relPropertyProjectable(key string) bool {
	return key == "key" || key == "data"
}

// forEachPatternInClause visits every path pattern reachable from c, including
// the ones nested in its expressions, and reports whether it saw the whole
// clause.
func forEachPatternInClause(c Clause, visit func(PathPattern)) bool {
	modelled := true
	walkExpr := func(e Expr) {
		if !forEachExpr(e, func(sub Expr) {
			switch x := sub.(type) {
			case *PatternExpr:
				visit(x.Pattern)
			case *PatternComprehension:
				visit(x.Pattern)
			}
		}) {
			modelled = false
		}
	}
	switch cl := c.(type) {
	case *Match:
		for _, p := range cl.Patterns {
			visit(p)
		}
		walkExpr(cl.Where)
	case *With:
		for _, it := range cl.Items {
			walkExpr(it.Expr)
		}
		walkExpr(cl.Where)
	case *Return:
		for _, it := range cl.Items {
			walkExpr(it.Expr)
		}
	default:
		modelled = false
	}
	return modelled
}

// forEachExpr visits e and every expression nested in it, and reports whether
// it recognised every node it reached. An unrecognised node is not descended
// into, so the visit is short: the callers that judge a relationship binding
// treat that as a refusal rather than as an absence of findings.
//
// executor.walkExprAll traverses the same tree and is deliberately not reused
// here. It reports nothing about what it could not see, and it descends into
// no pattern — both right for the variable collection it feeds, where a missed
// node costs a wider read, and wrong here, where a missed node is a
// relationship dereference that ships as a silent null.
func forEachExpr(e Expr, visit func(Expr)) bool {
	if e == nil {
		return true
	}
	visit(e)
	switch x := e.(type) {
	case *Literal, *ParameterRef, *VariableRef:
		return true
	case *PropertyAccess:
		return forEachExpr(x.Target, visit)
	case *BinaryOp:
		return forEachExpr(x.Left, visit) && forEachExpr(x.Right, visit)
	case *AndOr:
		modelled := true
		for _, op := range x.Operands {
			modelled = forEachExpr(op, visit) && modelled
		}
		return modelled
	case *Not:
		return forEachExpr(x.Operand, visit)
	case *PatternExpr:
		return forEachPatternProperties(x.Pattern, visit)
	case *FunctionCall:
		modelled := true
		for _, a := range x.Args {
			modelled = forEachExpr(a, visit) && modelled
		}
		return modelled
	case *MapLiteral:
		modelled := true
		for _, k := range x.Keys {
			modelled = forEachExpr(x.Values[k], visit) && modelled
		}
		return modelled
	case *ListLiteral:
		modelled := true
		for _, el := range x.Elements {
			modelled = forEachExpr(el, visit) && modelled
		}
		return modelled
	case *PatternComprehension:
		modelled := forEachPatternProperties(x.Pattern, visit)
		modelled = forEachExpr(x.Where, visit) && modelled
		return forEachExpr(x.Projection, visit) && modelled
	case *CaseExpr:
		modelled := true
		for _, alt := range x.Alternatives {
			modelled = forEachExpr(alt.When, visit) && modelled
			modelled = forEachExpr(alt.Then, visit) && modelled
		}
		return forEachExpr(x.Else, visit) && modelled
	}
	return false
}

// forEachPatternProperties visits the expressions a pattern's inline property
// maps carry — the only expressions a pattern nested inside another expression
// contributes.
func forEachPatternProperties(p PathPattern, visit func(Expr)) bool {
	modelled := true
	for _, n := range p.Nodes {
		for _, v := range n.Properties {
			modelled = forEachExpr(v, visit) && modelled
		}
	}
	for _, r := range p.Rels {
		for _, v := range r.Properties {
			modelled = forEachExpr(v, visit) && modelled
		}
	}
	return modelled
}
