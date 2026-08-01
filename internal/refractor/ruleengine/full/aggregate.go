package full

import (
	"errors"
	"fmt"
	"strings"
)

// aggFold folds one aggregating projection item incrementally: each binding
// row is added as it is produced and only the running result is retained.
//
// The retained state is what bounds an evaluation's heap. count() and
// max()/min() hold a scalar; collect() holds exactly the list it returns, and
// collect(DISTINCT ...) holds only the distinct values. Nothing holds the
// per-row argument values themselves, which is what a large MATCH multiplies.
type aggFold interface {
	add(ex *executor, b binding) error
	value() (any, error)
}

// newAggFold builds the fold tree for one aggregating projection item,
// mirroring the expression tree: a FunctionCall folds its own argument, a
// BinaryOp folds each side independently and combines the two at value().
//
// Folds are built per group, on that group's first row, so a query whose
// required MATCH bound zero rows builds none and therefore reports no arity or
// unsupported-aggregator error — preserving projectItems' zero-rows-in,
// zero-rows-out contract.
func newAggFold(e Expr) (aggFold, error) {
	switch x := e.(type) {
	case *FunctionCall:
		f := &callFold{call: x}
		switch {
		case strings.EqualFold(x.Name, "collect"):
			f.kind = aggCollect
			// Non-nil empty: an empty collect() projects [], never null.
			f.list = []any{}
		case strings.EqualFold(x.Name, "count"):
			f.kind = aggCount
		case strings.EqualFold(x.Name, "max"), strings.EqualFold(x.Name, "min"):
			if len(x.Args) != 1 {
				return nil, fmt.Errorf("full engine: %s takes exactly 1 argument, got %d",
					strings.ToLower(x.Name), len(x.Args))
			}
			f.kind = aggExtreme
			f.extreme.op = ">"
			if strings.EqualFold(x.Name, "min") {
				f.extreme.op = "<"
			}
		default:
			return nil, fmt.Errorf("full engine: unsupported aggregator %q", x.Name)
		}
		if x.Distinct {
			f.seen = map[string]struct{}{}
		}
		return f, nil
	case *BinaryOp:
		left, err := newAggFold(x.Left)
		if err != nil {
			return nil, err
		}
		right, err := newAggFold(x.Right)
		if err != nil {
			return nil, err
		}
		return &binOpFold{op: x.Op, left: left, right: right}, nil
	}
	return nil, errors.New("full engine: unsupported aggregate expression")
}

type aggKind int

const (
	aggCollect aggKind = iota
	aggCount
	aggExtreme
)

// callFold folds one aggregator call.
//
// DISTINCT binds HERE, on the aggregator call that carries it, rather than on
// the RETURN item as a whole. An item is often a composition — `collect(...) +
// collect(...)`, the shape every generated read-grant producer takes — where
// each call carries its own DISTINCT and the item itself carries none. Reading
// the flag off the item would silently drop every one of those, and because the
// branches of such a query are independent OPTIONAL MATCHes whose bindings are
// their cross product, each branch's list would then be inflated by the product
// of all the others' cardinalities.
//
// Deduplication is by normalizeForKey's canonical rendering — the same identity
// basis the grouping key uses, so a map value (the shape every read-grant
// branch collects) compares by content rather than by Go reference. First
// occurrence wins, so the result order stays the binding order.
type callFold struct {
	call    *FunctionCall
	kind    aggKind
	seen    map[string]struct{} // non-nil iff the call carries DISTINCT
	list    []any               // aggCollect
	count   int64               // aggCount
	extreme extremeFold         // aggExtreme
}

func (f *callFold) add(ex *executor, b binding) error {
	if len(f.call.Args) == 0 {
		return nil
	}
	v, err := ex.evalExpr(b, f.call.Args[0])
	if err != nil {
		return err
	}
	if f.seen != nil {
		sig := normalizeForKey(v)
		if _, dup := f.seen[sig]; dup {
			return nil
		}
		f.seen[sig] = struct{}{}
	}
	switch f.kind {
	case aggCollect:
		// Drop nulls (Cypher semantics).
		if v != nil {
			f.list = append(f.list, v)
		}
	case aggCount:
		if v != nil {
			f.count++
		}
	case aggExtreme:
		return f.extreme.add(v)
	}
	return nil
}

func (f *callFold) value() (any, error) {
	switch f.kind {
	case aggCollect:
		return f.list, nil
	case aggCount:
		return f.count, nil
	case aggExtreme:
		return f.extreme.value(), nil
	}
	return nil, fmt.Errorf("full engine: unsupported aggregator %q", f.call.Name)
}

// binOpFold folds a composed aggregate item (`collect(...) + collect(...)`):
// each side folds independently over every row and the operator applies once,
// at value().
type binOpFold struct {
	op    string
	left  aggFold
	right aggFold
}

func (f *binOpFold) add(ex *executor, b binding) error {
	if err := f.left.add(ex, b); err != nil {
		return err
	}
	return f.right.add(ex, b)
}

func (f *binOpFold) value() (any, error) {
	if f.op != "+" {
		return nil, fmt.Errorf("full engine: unsupported aggregator op %q", f.op)
	}
	leftVal, err := f.left.value()
	if err != nil {
		return nil, err
	}
	rightVal, err := f.right.value()
	if err != nil {
		return nil, err
	}
	ll, lok := leftVal.([]any)
	rr, rok := rightVal.([]any)
	if !lok || !rok {
		// The '+' aggregator-op path concatenates two collect() lists. A scalar
		// child (max/min) is not a list — arithmetic over scalar aggregators
		// (max(a)+max(b)) is not supported; fail loudly rather than silently
		// returning an empty list.
		return nil, fmt.Errorf("full engine: aggregator op %q requires list (collect) operands, got %T and %T",
			f.op, leftVal, rightVal)
	}
	out := make([]any, 0, len(ll)+len(rr))
	out = append(out, ll...)
	out = append(out, rr...)
	return out, nil
}

// extremeFold holds the running max (op ">") or min (op "<") using the engine's
// own ordering (compareAny: numeric when both sides are numeric, otherwise
// lexicographic on strings — so ISO-8601 timestamps reduce chronologically).
// Nulls are dropped (Cypher semantics); all-null / empty input yields null.
// Incomparable values follow compareAny (no swap), matching how the engine
// orders them in WHERE.
type extremeFold struct {
	op   string
	acc  any
	have bool
}

func (f *extremeFold) add(v any) error {
	if v == nil {
		return nil
	}
	if !f.have {
		f.acc, f.have = v, true
		return nil
	}
	swap, err := compareAny(f.op, v, f.acc)
	if err != nil {
		return err
	}
	if swap {
		f.acc = v
	}
	return nil
}

func (f *extremeFold) value() any {
	if !f.have {
		return nil
	}
	return f.acc
}
