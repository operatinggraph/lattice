package full

// CollectVariableRefs returns every graph-bound variable name e's evaluation
// depends on: a *VariableRef's own name, or the root *VariableRef of a
// *PropertyAccess chain. *Literal and *ParameterRef leaves are evaluation-
// constant and contribute nothing.
//
// unknown is true when the walk meets an expression form it does not
// recognise — fail-closed, since a caller attributing a RETURN column to a
// single walk (refractor-shared-keyspace-arbitration-design.md §13.2) must
// never silently under-count a dependency and misclassify a mixed column as
// single-owned. Same AST-walk technique as
// internal/refractor/projection's hasMultiBindingConjunctUnit
// (refractor-evaluation-consistency-design.md §13.3) — a second caller over
// the same full.Expr shapes, not a shared function, so each package's
// classifier can evolve against its own fail-closed default independently.
func CollectVariableRefs(e Expr) (names map[string]bool, unknown bool) {
	out := map[string]bool{}
	unknown = collectVariableRefsInto(e, out)
	return out, unknown
}

func collectVariableRefsInto(e Expr, out map[string]bool) bool {
	switch x := e.(type) {
	case nil:
		return false
	case *Literal:
		return false
	case *ParameterRef:
		return false
	case *VariableRef:
		out[x.Name] = true
		return false
	case *PropertyAccess:
		name, ok, unk := variableRefChainRoot(x)
		if ok {
			out[name] = true
		}
		return unk
	case *BinaryOp:
		u1 := collectVariableRefsInto(x.Left, out)
		u2 := collectVariableRefsInto(x.Right, out)
		return u1 || u2
	case *AndOr:
		unknown := false
		for _, op := range x.Operands {
			if collectVariableRefsInto(op, out) {
				unknown = true
			}
		}
		return unknown
	case *Not:
		return collectVariableRefsInto(x.Operand, out)
	case *PatternExpr:
		return collectPatternVariableRefs(x.Pattern, out)
	case *FunctionCall:
		unknown := false
		for _, a := range x.Args {
			if collectVariableRefsInto(a, out) {
				unknown = true
			}
		}
		return unknown
	case *MapLiteral:
		unknown := false
		for _, k := range x.Keys {
			if collectVariableRefsInto(x.Values[k], out) {
				unknown = true
			}
		}
		return unknown
	case *ListLiteral:
		unknown := false
		for _, el := range x.Elements {
			if collectVariableRefsInto(el, out) {
				unknown = true
			}
		}
		return unknown
	case *PatternComprehension:
		unknown := collectPatternVariableRefs(x.Pattern, out)
		if collectVariableRefsInto(x.Where, out) {
			unknown = true
		}
		if collectVariableRefsInto(x.Projection, out) {
			unknown = true
		}
		return unknown
	case *CaseExpr:
		unknown := false
		for _, alt := range x.Alternatives {
			if collectVariableRefsInto(alt.When, out) {
				unknown = true
			}
			if collectVariableRefsInto(alt.Then, out) {
				unknown = true
			}
		}
		if collectVariableRefsInto(x.Else, out) {
			unknown = true
		}
		return unknown
	default:
		// A future AST node type this walker was never updated for — signal
		// unknown up the call chain rather than silently under-counting.
		return true
	}
}

// variableRefChainRoot resolves a *PropertyAccess chain's root: the binding
// name when the chain terminates in a *VariableRef, ok=false with no binding
// when it terminates in a *Literal/*ParameterRef (evaluation-constant,
// contributes nothing), or unknown=true for any other terminal shape this
// walker cannot trace provenance for (fail-closed).
func variableRefChainRoot(e Expr) (name string, ok bool, unknown bool) {
	for {
		switch x := e.(type) {
		case *VariableRef:
			return x.Name, true, false
		case *PropertyAccess:
			e = x.Target
			continue
		case *Literal, *ParameterRef:
			return "", false, false
		default:
			return "", false, true
		}
	}
}

// collectPatternVariableRefs adds every variable name a path pattern's
// evaluation touches into out: the NodePattern/RelPattern variables the pattern
// BINDS, and the variables its inline PROPERTY MAPS read.
//
// The property maps are not decoration. A pattern reached through an expression
// is evaluated as a predicate or a comprehension (executor.existsAsPredicate,
// executor.evalPatternComprehension) and its property maps are ordinary
// expressions run through evalExpr — so `(:task {key: t.key})` makes the whole
// predicate depend on `t`, a binding nothing else in the pattern names. Walking
// only the pattern's own variables reported a SHORT dependency set with
// unknown=false, which is the one answer this walk must never give: a caller
// reasoning about which bindings an expression needs would conclude it needs
// none of them, and the fail-closed arm it relies on would never fire.
func collectPatternVariableRefs(p PathPattern, out map[string]bool) bool {
	unknown := false
	for _, n := range p.Nodes {
		if n.Variable != "" {
			out[n.Variable] = true
		}
		for _, v := range n.Properties {
			if collectVariableRefsInto(v, out) {
				unknown = true
			}
		}
	}
	for _, r := range p.Rels {
		if r.Variable != "" {
			out[r.Variable] = true
		}
		for _, v := range r.Properties {
			if collectVariableRefsInto(v, out) {
				unknown = true
			}
		}
	}
	return unknown
}
