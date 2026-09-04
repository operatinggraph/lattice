package full

// Alias resolution across WITH boundaries: the substitution that lets a
// key-column predicate ask its questions of an expression over PATTERN
// VARIABLES even when a WITH stands between the RETURN and the MATCH.
//
// A WITH rebuilds every row from its projection items alone
// (executor.projectItems), so a RETURN naming `entityKey` says nothing by its
// name about which pattern variable produced the value. The provenance is one
// substitution away: `WITH app.key AS entityKey` followed by
// `RETURN nanoIdFromKey(entityKey) AS app_id` composes to
// `nanoIdFromKey(app.key)`, which a caller can then judge exactly as it judges
// a WITH-free query.
//
// The resolver is deliberately NARROWER than withscope.go's varScan. varScan
// must enumerate every variable reference in a query, so it models every
// expression node; this must RECONSTRUCT a value, so it models only the shapes
// a key column can legitimately be — nil, Literal, ParameterRef, VariableRef,
// PropertyAccess and FunctionCall — and reports every other node as unmodelled.
// Unmodelled is a refusal, never a passthrough: leaving an unresolved node in
// place would hand a caller an expression whose variable names it would then
// read as pattern variables, which is the fail-open shape.
//
// Resolution is driven FROM the consumer, not from the WITH list. A sibling
// item nobody reaches may be as unmodelled as it likes; only an item some
// consumed expression actually resolves through can refuse.

// aliasBinding is one WITH boundary's answer for one alias it binds: the
// expression, over pattern variables, that produces the value — or a refusal,
// when some node on the way to it is a shape this resolver does not model.
//
// The two states are distinct fields rather than a nil expression, because a
// missing answer and an empty one must not collide: an alias whose provenance
// is unknown has to refuse, while a nil expression is a value like any other.
type aliasBinding struct {
	// expr is the resolved expression, authoritative only while unmodelled is
	// false.
	expr Expr
	// unmodelled marks that some node between this alias and the pattern
	// variables underneath it is outside the shapes this resolver reconstructs.
	unmodelled bool
}

// analyseWithAliases builds one environment per WITH clause of q, in clause
// order: the names that boundary binds, mapped to the expression producing
// each, already resolved against the preceding boundary's environment.
//
// A name a boundary carries under itself — `WITH a`, or its no-op
// `WITH a AS a` — gets an entry only when an EARLIER boundary computed it: a
// pattern variable carried through still binds what it always bound, and
// mapping its name to itself would recur (evalExpr on a VariableRef returns
// the binding). Carrying an earlier boundary's resolution forward instead is
// what lets a chain of WITHs compose.
//
// Item naming mirrors executor.projectItems by calling projectionAutoAlias
// rather than restating it, so an environment and the rows the executor builds
// cannot disagree about what a column is called.
//
// Depth is bounded by the clause list: each boundary resolves only against the
// previous, already-resolved environment, so there is no fixpoint and no cycle
// to guard.
//
// A query with no WITH yields nil — the empty environment list every consumer
// reads as "the RETURN already names pattern variables".
func analyseWithAliases(q *Query) []map[string]aliasBinding {
	if q == nil {
		return nil
	}
	var envs []map[string]aliasBinding
	prev := map[string]aliasBinding{}
	for _, c := range q.Clauses {
		w, isWith := c.(*With)
		if !isWith {
			continue
		}
		env := make(map[string]aliasBinding, len(w.Items))
		for i, it := range w.Items {
			if vr, isVar := it.Expr.(*VariableRef); isVar && (it.Alias == "" || it.Alias == vr.Name) {
				if carried, known := prev[vr.Name]; known {
					env[vr.Name] = carried
				}
				continue
			}
			name := it.Alias
			if name == "" {
				name = projectionAutoAlias(it.Expr, i)
			}
			resolved, ok := substituteAliases(it.Expr, prev)
			env[name] = aliasBinding{expr: resolved, unmodelled: !ok}
		}
		envs = append(envs, env)
		prev = env
	}
	return envs
}

// resolveThroughWithAliases rewrites e — a RETURN expression — into an
// expression over pattern variables, by substituting through the LAST WITH
// boundary's environment (which already carries every earlier boundary's
// resolution). ok is false when e reaches an alias whose provenance is
// unmodelled, or contains a node shape this resolver does not reconstruct.
//
// With no WITH in the query there is nothing to substitute and e is returned
// untouched: its names are already the pattern's own, and narrowing the shapes
// a WITH-free key column may take is no part of what this resolution is for.
func resolveThroughWithAliases(e Expr, envs []map[string]aliasBinding) (Expr, bool) {
	if len(envs) == 0 {
		return e, true
	}
	return substituteAliases(e, envs[len(envs)-1])
}

// substituteAliases replaces every variable reference e makes to a name env
// binds with the expression producing that name, recursing into the shapes a
// value can be built from. A name env does not bind is a pattern variable (or
// a binding carried under its own name) and is left as it stands.
//
// ok is false on any node outside the modelled set, and on any alias env binds
// to an unmodelled provenance. The default arm is the point of the switch: an
// expression node added to the AST without a case here must refuse rather than
// silently resolve to itself.
func substituteAliases(e Expr, env map[string]aliasBinding) (Expr, bool) {
	switch x := e.(type) {
	case nil:
		return nil, true
	case *Literal:
		return x, true
	case *ParameterRef:
		return x, true
	case *VariableRef:
		bound, isAlias := env[x.Name]
		if !isAlias {
			return x, true
		}
		if bound.unmodelled {
			return nil, false
		}
		return bound.expr, true
	case *PropertyAccess:
		target, ok := substituteAliases(x.Target, env)
		if !ok {
			return nil, false
		}
		if target == x.Target {
			return x, true
		}
		return &PropertyAccess{Target: target, Key: x.Key}, true
	case *FunctionCall:
		args := make([]Expr, len(x.Args))
		changed := false
		for i, a := range x.Args {
			sub, ok := substituteAliases(a, env)
			if !ok {
				return nil, false
			}
			args[i] = sub
			if sub != a {
				changed = true
			}
		}
		if !changed {
			return x, true
		}
		return &FunctionCall{Namespace: x.Namespace, Name: x.Name, Distinct: x.Distinct, Args: args}, true
	default:
		return nil, false
	}
}

// carriesWith reports whether clauses contain a WITH boundary at all — the
// precondition every alias-resolution question has, and the shortcut for the
// far commoner query that has none.
func carriesWith(clauses []Clause) bool {
	for _, c := range clauses {
		if _, isWith := c.(*With); isWith {
			return true
		}
	}
	return false
}
