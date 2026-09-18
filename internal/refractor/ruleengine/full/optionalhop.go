package full

import "reflect"

// OptionalHopColumn is one RETURN alias's answer to "can this column be null
// because an OPTIONAL MATCH bound nothing?" — the question a Weaver target's
// `row.<column>` Params template turns into a refusal at dispatch
// (internal/weaver/strategist.go resolveRowTemplate: a null or absent row
// column is a data error and the gap is skipped).
//
//   - OptionalVars: the variables the column's expression reads that are bound
//     ONLY by an OPTIONAL MATCH — a hop that can miss. Empty for a column read
//     off the anchor, a required MATCH, a parameter or a literal.
//   - NullSafe: the expression cannot evaluate to null however its hops bind —
//     a literal, or a `coalesce(...)` whose last argument is a non-null
//     literal. A null-safe column is safe to template regardless of
//     OptionalVars.
type OptionalHopColumn struct {
	Alias        string
	OptionalVars []string
	NullSafe     bool
}

// OptionalHopColumns reports every RETURN column with its optional-hop
// exposure. exhaustive is false when the query carries a WITH boundary whose
// alias provenance the general scope walk refused to trace (the same
// precondition ExistenceDependsOnNeighbour states) — a caller must read that
// as "could not tell", never as "no exposure".
func (cr *CompiledRule) OptionalHopColumns() (cols []OptionalHopColumn, exhaustive bool) {
	if cr == nil || cr.Query == nil {
		return nil, false
	}
	q := cr.Query
	if carriesWith(q.Clauses) && (!cr.withAliasResolved || cr.withScopeVerdict != "") {
		return nil, false
	}
	required := map[string]bool{}
	optional := map[string]bool{}
	exhaustive = true
	for _, c := range q.Clauses {
		m, isMatch := c.(*Match)
		if !isMatch {
			continue
		}
		bound := map[string]bool{}
		for _, p := range m.Patterns {
			if collectPatternVariableRefs(p, bound) {
				exhaustive = false
			}
		}
		for v := range bound {
			if m.Optional {
				optional[v] = true
			} else {
				required[v] = true
			}
		}
	}
	for _, c := range q.Clauses {
		r, isReturn := c.(*Return)
		if !isReturn {
			continue
		}
		for i, item := range r.Items {
			col := OptionalHopColumn{Alias: itemAliasAt(r.Items, i), NullSafe: exprNullSafe(item.Expr)}
			refs := map[string]bool{}
			if collectVariableRefsInto(item.Expr, refs) {
				exhaustive = false
			}
			for _, v := range sortedVariableNames(refs) {
				if optional[v] && !required[v] {
					col.OptionalVars = append(col.OptionalVars, v)
				}
			}
			cols = append(cols, col)
		}
		return cols, exhaustive
	}
	return nil, false
}

// exprNullSafe is the top-of-expression test: a non-null literal, or a
// coalesce whose LAST argument is one (coalesce returns the first non-null
// argument, so a non-null tail bounds it).
func exprNullSafe(e Expr) bool {
	switch x := e.(type) {
	case *Literal:
		return x.Value != nil
	case *FunctionCall:
		if len(x.Namespace) == 0 && x.Name == "coalesce" && len(x.Args) > 0 {
			return exprNullSafe(x.Args[len(x.Args)-1])
		}
	}
	return false
}

// ReturnColumnGuardsNonNull reports whether the RETURN column `guardAlias`
// cannot be true while the RETURN column `colAlias` is null — the property a
// level-triggered gap needs before a Weaver target may template that column
// as a Params value. The engine's own semantics supply the implications
// (values.go): a top-level AND conjunct `<expr> <> null` (or
// `NOT (<expr> = null)`) makes <expr> non-null; an ordering comparison
// `<a> < <b>` / `<=` / `>` / `>=` makes BOTH operands non-null (compareAny is
// false on a null operand); and a non-null property chain rooted at a
// variable v binds v, so v itself and v.key are non-null too. The column is
// guarded when its exact expression is implied non-null, or when it is the
// bare variable or `.key` of a variable some implied-non-null chain is
// rooted at. Either alias absent → false.
func (cr *CompiledRule) ReturnColumnGuardsNonNull(guardAlias, colAlias string) bool {
	if cr == nil || cr.Query == nil {
		return false
	}
	var guard, col Expr
	for _, c := range cr.Query.Clauses {
		r, isReturn := c.(*Return)
		if !isReturn {
			continue
		}
		for i, item := range r.Items {
			switch itemAliasAt(r.Items, i) {
			case guardAlias:
				guard = item.Expr
			case colAlias:
				col = item.Expr
			}
		}
		break
	}
	if guard == nil || col == nil {
		return false
	}
	var nonNull []Expr
	for _, conj := range andConjuncts(guard) {
		nonNull = append(nonNull, impliedNonNull(conj)...)
	}
	colRoot, colIsRootOrKey := rootVariableOfKeyOrSelf(col)
	for _, e := range nonNull {
		if reflect.DeepEqual(e, col) {
			return true
		}
		if colIsRootOrKey {
			if v, ok := chainRootVariable(e); ok && v == colRoot {
				return true
			}
		}
	}
	return false
}

// andConjuncts flattens nested AND trees into their conjunct list; any other
// expression is its own single conjunct.
func andConjuncts(e Expr) []Expr {
	if a, ok := e.(*AndOr); ok && a.Op == "AND" {
		var out []Expr
		for _, op := range a.Operands {
			out = append(out, andConjuncts(op)...)
		}
		return out
	}
	return []Expr{e}
}

// impliedNonNull lists the expressions one conjunct proves non-null when it
// holds.
func impliedNonNull(conj Expr) []Expr {
	switch x := conj.(type) {
	case *BinaryOp:
		switch x.Op {
		case "<>":
			if isNullLiteral(x.Right) {
				return []Expr{x.Left}
			}
			if isNullLiteral(x.Left) {
				return []Expr{x.Right}
			}
		case "<", "<=", ">", ">=":
			return []Expr{x.Left, x.Right}
		}
	case *Not:
		if b, ok := x.Operand.(*BinaryOp); ok && b.Op == "=" {
			if isNullLiteral(b.Right) {
				return []Expr{b.Left}
			}
			if isNullLiteral(b.Left) {
				return []Expr{b.Right}
			}
		}
	}
	return nil
}

// chainRootVariable resolves a bare variable or a property-access chain
// (v, v.a, v.a.b …) to the variable it is rooted at.
func chainRootVariable(e Expr) (string, bool) {
	for {
		switch x := e.(type) {
		case *VariableRef:
			return x.Name, true
		case *PropertyAccess:
			e = x.Target
		default:
			return "", false
		}
	}
}

// rootVariableOfKeyOrSelf answers for a column that is a bare variable or a
// variable's `.key` — the two shapes a bound node can never project null for.
func rootVariableOfKeyOrSelf(e Expr) (string, bool) {
	switch x := e.(type) {
	case *VariableRef:
		return x.Name, true
	case *PropertyAccess:
		if v, ok := x.Target.(*VariableRef); ok && x.Key == "key" {
			return v.Name, true
		}
	}
	return "", false
}

func isNullLiteral(e Expr) bool {
	l, ok := e.(*Literal)
	return ok && l.Value == nil
}
