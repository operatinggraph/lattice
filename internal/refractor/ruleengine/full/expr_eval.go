package full

import (
	"fmt"
	"strings"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
)

// --- expression evaluation ---

func (ex *executor) evalExpr(b binding, e Expr) (any, error) {
	switch x := e.(type) {
	case nil:
		return nil, nil
	case *Literal:
		return x.Value, nil
	case *ParameterRef:
		if ex.params == nil {
			return nil, &ruleengine.MissingParameterError{Name: x.Name}
		}
		v, ok := ex.params[x.Name]
		if !ok {
			return nil, &ruleengine.MissingParameterError{Name: x.Name}
		}
		return v, nil
	case *VariableRef:
		if v, ok := b[x.Name]; ok {
			return v, nil
		}
		return nil, nil
	case *PropertyAccess:
		target, err := ex.evalExpr(b, x.Target)
		if err != nil {
			return nil, err
		}
		return ex.resolveProperty(target, x.Key)
	case *BinaryOp:
		l, err := ex.evalExpr(b, x.Left)
		if err != nil {
			return nil, err
		}
		r, err := ex.evalExpr(b, x.Right)
		if err != nil {
			return nil, err
		}
		return evalBinary(x.Op, l, r)
	case *AndOr:
		if x.Op == "AND" {
			for _, op := range x.Operands {
				v, err := ex.evalExpr(b, op)
				if err != nil {
					return nil, err
				}
				if !truthy(v) {
					return false, nil
				}
			}
			return true, nil
		}
		if x.Op == "XOR" {
			trueCount := 0
			for _, op := range x.Operands {
				v, err := ex.evalExpr(b, op)
				if err != nil {
					return nil, err
				}
				if truthy(v) {
					trueCount++
				}
			}
			return trueCount == 1, nil
		}
		// OR
		for _, op := range x.Operands {
			v, err := ex.evalExpr(b, op)
			if err != nil {
				return nil, err
			}
			if truthy(v) {
				return true, nil
			}
		}
		return false, nil
	case *Not:
		// Anti-pattern: NOT (path) — evaluate as existence predicate.
		if pe, ok := x.Operand.(*PatternExpr); ok {
			exists, err := ex.existsAsPredicate(b, pe.Pattern)
			if err != nil {
				return nil, err
			}
			return !exists, nil
		}
		v, err := ex.evalExpr(b, x.Operand)
		if err != nil {
			return nil, err
		}
		return !truthy(v), nil
	case *PatternExpr:
		return ex.existsAsPredicate(b, x.Pattern)
	case *FunctionCall:
		return ex.evalFunctionCall(b, x)
	case *MapLiteral:
		out := make(map[string]any, len(x.Keys))
		for _, k := range x.Keys {
			v, err := ex.evalExpr(b, x.Values[k])
			if err != nil {
				return nil, err
			}
			out[k] = v
		}
		return out, nil
	case *ListLiteral:
		out := make([]any, 0, len(x.Elements))
		for _, el := range x.Elements {
			v, err := ex.evalExpr(b, el)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case *PatternComprehension:
		return ex.evalPatternComprehension(b, x)
	case *CaseExpr:
		for _, alt := range x.Alternatives {
			cond, err := ex.evalExpr(b, alt.When)
			if err != nil {
				return nil, err
			}
			if truthy(cond) {
				return ex.evalExpr(b, alt.Then)
			}
		}
		if x.Else != nil {
			return ex.evalExpr(b, x.Else)
		}
		return nil, nil
	}
	return nil, fmt.Errorf("full engine: unsupported expression %T", e)
}

func (ex *executor) evalFunctionCall(b binding, fc *FunctionCall) (any, error) {
	// During projection without grouping, collect()/count() are evaluated
	// row-locally by projectItems → the per-row aggregate fold. Outside that path
	// (e.g. inside another expression) treat collect as a no-op wrapper that
	// returns the single arg's value wrapped in a list.
	name := strings.ToLower(fc.Name)
	switch name {
	case "collect":
		if len(fc.Args) == 0 {
			return []any{}, nil
		}
		v, err := ex.evalExpr(b, fc.Args[0])
		if err != nil {
			return nil, err
		}
		if v == nil {
			return []any{}, nil
		}
		return []any{v}, nil
	case "count":
		return int64(1), nil
	case "max", "min":
		// Row-local (no grouping, or nested inside another expression):
		// the extreme of a single row's value is that value. Grouping goes
		// through projectItems → the per-row aggregate fold instead. max/min are
		// unary aggregators; a multi-arg call is a query error, not a silent
		// "use the first arg".
		if len(fc.Args) != 1 {
			return nil, fmt.Errorf("full engine: %s takes exactly 1 argument, got %d", name, len(fc.Args))
		}
		return ex.evalExpr(b, fc.Args[0])
	case "levenshteindist":
		// levenshteinDist(a, b) → int — classical Wagner-Fischer edit distance.
		// Pure / deterministic / O(N*M) time + O(min(N,M)) space.
		// Both args must be strings; nil args return nil.
		if len(fc.Args) != 2 {
			return nil, fmt.Errorf("full engine: levenshteinDist takes exactly 2 arguments")
		}
		av, err := ex.evalExpr(b, fc.Args[0])
		if err != nil {
			return nil, err
		}
		bv, err := ex.evalExpr(b, fc.Args[1])
		if err != nil {
			return nil, err
		}
		if av == nil || bv == nil {
			return nil, nil
		}
		as, aok := av.(string)
		bs, bok := bv.(string)
		if !aok || !bok {
			return nil, fmt.Errorf("full engine: levenshteinDist arguments must be strings, got %T and %T", av, bv)
		}
		return int64(levenshteinDistance(as, bs)), nil
	case "levenshteinratio":
		// levenshteinRatio(a, b) → float64 in [0.0, 1.0].
		// 1.0 when identical (incl. both empty); 0.0 when one is empty
		// and other is non-empty.
		if len(fc.Args) != 2 {
			return nil, fmt.Errorf("full engine: levenshteinRatio takes exactly 2 arguments")
		}
		av, err := ex.evalExpr(b, fc.Args[0])
		if err != nil {
			return nil, err
		}
		bv, err := ex.evalExpr(b, fc.Args[1])
		if err != nil {
			return nil, err
		}
		if av == nil || bv == nil {
			return nil, nil
		}
		as, aok := av.(string)
		bs, bok := bv.(string)
		if !aok || !bok {
			return nil, fmt.Errorf("full engine: levenshteinRatio arguments must be strings, got %T and %T", av, bv)
		}
		la, lb := len(as), len(bs)
		maxLen := la
		if lb > maxLen {
			maxLen = lb
		}
		if maxLen == 0 {
			return float64(1.0), nil
		}
		dist := levenshteinDistance(as, bs)
		return 1.0 - float64(dist)/float64(maxLen), nil
	case "nanoidfromkey":
		// nanoIdFromKey(vertexKey) → the bare NanoID (the <id> segment of a
		// vtx.<type>.<id> vertex key) — the §6.14 opaque-match-token anchor
		// representation for read-path authorization (D1).
		//
		// Fail-closed: only a well-formed vertex key (exactly three
		// dot-segments, leading "vtx", non-empty type + id) yields a NanoID;
		// an aspect key (vtx.<type>.<id>.<localName>), a link key (lnk.…), or
		// any malformed input ERRORS rather than emitting a wrong anchor — an
		// auth-plane lens must never project a token that could match the wrong
		// resource, so a bad shape fails the projection (deny) instead of
		// silently degrading. A nil arg returns nil (mirrors levenshtein).
		if len(fc.Args) != 1 {
			return nil, fmt.Errorf("full engine: nanoIdFromKey takes exactly 1 argument, got %d", len(fc.Args))
		}
		v, err := ex.evalExpr(b, fc.Args[0])
		if err != nil {
			return nil, err
		}
		if v == nil {
			return nil, nil
		}
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("full engine: nanoIdFromKey argument must be a string, got %T", v)
		}
		return nanoIDFromVertexKey(s)
	case "type":
		// type(r) → the relation name of the relationship r is bound to: the
		// <relation> segment of its Contract #1 link key, which is the link's
		// localName and, for an object attachment, the slot a detach must name.
		//
		// Fail-closed in nanoIdFromKey's shape. Cypher NULL — an OPTIONAL MATCH
		// that found no relationship, or a variable this row never bound — is
		// NULL, the one answer a caller can tell apart from a relation name.
		// Anything that is not a relationship (a node binding, a scalar, a
		// list) ERRORS: a column that silently stopped naming a relation is
		// indistinguishable from a row that genuinely had none, which is the
		// diagnosis-free null this projection exists to replace.
		if len(fc.Args) != 1 {
			return nil, fmt.Errorf("full engine: type takes exactly 1 argument, got %d", len(fc.Args))
		}
		v, err := ex.evalExpr(b, fc.Args[0])
		if err != nil {
			return nil, err
		}
		if isNullBound(v) {
			return nil, nil
		}
		ref, ok := v.(*nodeRef)
		if !ok {
			return nil, fmt.Errorf("full engine: type argument must be a relationship, got %T", v)
		}
		if ref.rel == "" {
			return nil, fmt.Errorf("full engine: type argument must be a relationship, got the node bound at %q", ref.key)
		}
		return ref.rel, nil
	case "coalesce":
		// coalesce(a, b, ...) → the first argument that is not Cypher NULL.
		// The shared-anchor composition primitive (pkgmgr Walks, composeDataLensSpec):
		// each walk's OPTIONAL MATCH binds its own scoped copy of the declared anchor
		// variable, at most one non-null per row, and a WITH clause folds them back to
		// the walk-declared name via coalesce.
		if len(fc.Args) == 0 {
			return nil, fmt.Errorf("full engine: coalesce takes at least 1 argument")
		}
		for _, arg := range fc.Args {
			v, err := ex.evalExpr(b, arg)
			if err != nil {
				return nil, err
			}
			if !isNullBound(v) {
				return v, nil
			}
		}
		return nil, nil
	}
	return nil, fmt.Errorf("full engine: unsupported function %q", fc.Name)
}
