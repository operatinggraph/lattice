package full

import "strings"

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
	return collectVariableRefsAndPatterns(e, out, nil)
}

// collectVariableRefsAndPatterns is the walk itself, with an optional second
// output: every PathPattern the expression EVALUATES — a pattern predicate
// (`NOT (a)-->(:role)`) or a pattern comprehension's own pattern.
//
// One traversal serves both because two would be two chances to disagree about
// which nodes an expression reaches. The variable set alone cannot answer for
// the ANONYMOUS elements of those patterns: `(a)-->(:role)` binds nothing but
// `a`, so a caller asking "does this expression reach past the anchor" off the
// names would read no. patterns is nil for every caller that only needs names,
// and the append is then never made.
func collectVariableRefsAndPatterns(e Expr, out map[string]bool, patterns *[]PathPattern) bool {
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
		u1 := collectVariableRefsAndPatterns(x.Left, out, patterns)
		u2 := collectVariableRefsAndPatterns(x.Right, out, patterns)
		return u1 || u2
	case *AndOr:
		unknown := false
		for _, op := range x.Operands {
			if collectVariableRefsAndPatterns(op, out, patterns) {
				unknown = true
			}
		}
		return unknown
	case *Not:
		return collectVariableRefsAndPatterns(x.Operand, out, patterns)
	case *PatternExpr:
		notePattern(patterns, x.Pattern)
		return collectPatternVariableRefs(x.Pattern, out)
	case *FunctionCall:
		unknown := false
		for _, a := range x.Args {
			if collectVariableRefsAndPatterns(a, out, patterns) {
				unknown = true
			}
		}
		return unknown
	case *MapLiteral:
		unknown := false
		for _, k := range x.Keys {
			if collectVariableRefsAndPatterns(x.Values[k], out, patterns) {
				unknown = true
			}
		}
		return unknown
	case *ListLiteral:
		unknown := false
		for _, el := range x.Elements {
			if collectVariableRefsAndPatterns(el, out, patterns) {
				unknown = true
			}
		}
		return unknown
	case *PatternComprehension:
		notePattern(patterns, x.Pattern)
		unknown := collectPatternVariableRefs(x.Pattern, out)
		if collectVariableRefsAndPatterns(x.Where, out, patterns) {
			unknown = true
		}
		if collectVariableRefsAndPatterns(x.Projection, out, patterns) {
			unknown = true
		}
		return unknown
	case *CaseExpr:
		unknown := false
		for _, alt := range x.Alternatives {
			if collectVariableRefsAndPatterns(alt.When, out, patterns) {
				unknown = true
			}
			if collectVariableRefsAndPatterns(alt.Then, out, patterns) {
				unknown = true
			}
		}
		if collectVariableRefsAndPatterns(x.Else, out, patterns) {
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

// notePattern records a pattern an expression evaluates, when the caller asked
// for them.
func notePattern(patterns *[]PathPattern, p PathPattern) {
	if patterns != nil {
		*patterns = append(*patterns, p)
	}
}

// patternReachesAnonymousElement reports whether p touches a node or
// relationship NO VARIABLE NAMES.
//
// It is collectPatternVariableRefs' complement, and exists because that
// function's contract is the set of BINDINGS an evaluation depends on — a set
// that is silent about the elements the pattern still traverses under no name.
// `MATCH (a:x)-[:rel]->(:y)` binds only `a`, so a caller asking "does this
// pattern reach past the anchor" off the names alone reads no, for a pattern
// whose whole purpose is a hop to a `:y`.
//
// An unnamed element can never be the anchor — the anchor is a named binding —
// so any one of them is a reach past it. A relationship counts whether or not
// its endpoints are named: `(a)-[:x]->(a)` is a self-loop whose existence turns
// on a link, and a link is a thing another event can remove.
func patternReachesAnonymousElement(p PathPattern) bool {
	for _, n := range p.Nodes {
		if n.Variable == "" {
			return true
		}
	}
	for _, r := range p.Rels {
		if r.Variable == "" {
			return true
		}
	}
	return false
}

// renderPathPattern renders a pattern the way it was authored, so a refusal
// naming one is a string an author can find in their own cypher: labels and
// relationship types where the pattern declares them, variables where it binds
// them, and `()` / `--` where it declares neither.
func renderPathPattern(p PathPattern) string {
	var b strings.Builder
	for i, n := range p.Nodes {
		if i > 0 && i-1 < len(p.Rels) {
			b.WriteString(renderRelPattern(p.Rels[i-1]))
		}
		b.WriteString("(")
		b.WriteString(n.Variable)
		if n.Label != "" {
			b.WriteString(":" + n.Label)
			if n.LabelExpand {
				b.WriteString("*")
			}
		}
		b.WriteString(")")
	}
	return b.String()
}

func renderRelPattern(r RelPattern) string {
	body := "-"
	if r.Variable != "" || r.Type != "" {
		body = "-[" + r.Variable
		if r.Type != "" {
			body += ":" + r.Type
		}
		body += "]-"
	}
	switch r.Direction {
	case DirOut:
		return body + ">"
	case DirIn:
		return "<" + body
	default:
		return body
	}
}
