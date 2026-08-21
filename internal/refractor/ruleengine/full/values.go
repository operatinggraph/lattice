package full

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// resolveProperty reads property `key` off target, implementing the Lattice
// property model: vertices carry the envelope (key/class/provenance) plus link
// topology; business data lives in aspects (and, by exception, in a vertex's
// own `data` envelope — e.g. permissions).
//
// For a vertex nodeRef, a name present in the root body returns that value
// directly (envelope fields, and root `data`). A name ABSENT from the root body
// is treated as an ASPECT reference: the aspect key <nodeKey>.<key> is
// point-read and its body returned, so a lens rule navigates an aspect-stored
// field explicitly as node.<aspect>.data.<field> (e.g. role.canonicalName.data.value).
// Aspect bodies returned this way are plain maps, so any further navigation uses
// ordinary map access — only the first hop off a vertex resolves an aspect.
func (ex *executor) resolveProperty(target any, key string) (any, error) {
	nr, ok := target.(*nodeRef)
	if !ok || nr == nil {
		return propertyOf(target, key), nil
	}
	if v, present := nr.props[key]; present {
		return v, nil
	}
	if key == "key" {
		return nr.key, nil
	}
	if nr.rel != "" {
		// A relationship binding carries the link's key and relation name and
		// nothing else, so any other property comes off the LINK's own body:
		// a point-read of the link key ITSELF, not the <nodeKey>.<key> aspect
		// key the vertex arm below builds. `r.data` yields the body's data
		// object, and a field of it is ordinary map navigation from there.
		//
		// The projectable set is enforced HERE as well as at parse
		// (relbinding.go), and enforced as an error: any other name would
		// otherwise serve a link's envelope out of a query the parse gate is
		// supposed to have refused, and answering it with nil would be the very
		// silent column that gate exists to make impossible. Parse is the
		// primary gate; this is the one that has to hold if a route ever
		// reaches evaluation without passing it.
		if !relPropertyProjectable(key) {
			return nil, fmt.Errorf(
				"full engine: relationship %q has no projectable property %q — a bound relationship "+
					"projects its link key, its payload (data) and its relation name", nr.rel, key)
		}
		if ex.coreKV == nil {
			return nil, errCoreKVReadDisabled
		}
		lref, err := ex.fetchNode(nr.key)
		if err != nil {
			return nil, err
		}
		if lref == nil {
			// An absent or tombstoned link has no properties — the same nil a
			// missing field resolves to (Cypher missing-property semantics).
			return nil, nil
		}
		return lref.props[key], nil
	}
	// Absent from the root body → aspect reference: point-read <nodeKey>.<key>.
	// A nil coreKV is the read-free key-resolution mode (the anchor-tombstone
	// delete path): an aspect that would require a Core-KV read is reported
	// unresolvable rather than panicking on a re-scan of the now-deleted vertex.
	if ex.coreKV == nil {
		return nil, errCoreKVReadDisabled
	}
	aref, err := ex.fetchNode(nr.key + "." + key)
	if err != nil {
		return nil, err
	}
	if aref == nil {
		return nil, nil
	}
	return aref.props, nil
}

// propertyOf resolves target.key for various target shapes (nodeRef, map,
// or nil). Returns nil for null targets and missing keys.
func propertyOf(target any, key string) any {
	switch t := target.(type) {
	case nil:
		return nil
	case *nodeRef:
		if t == nil {
			return nil
		}
		if v, ok := t.props[key]; ok {
			return v
		}
		if key == "key" {
			return t.key
		}
		return nil
	case map[string]any:
		return t[key]
	}
	return nil
}

func truthy(v any) bool {
	if v == nil {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return true
}

func evalBinary(op string, l, r any) (any, error) {
	switch op {
	case "=":
		return equalsAny(l, r), nil
	case "<>":
		return !equalsAny(l, r), nil
	case "<", ">", "<=", ">=":
		return compareAny(op, l, r)
	case "+":
		// String concat or numeric add — defer to numeric when both numeric,
		// otherwise list concat when both lists.
		if ll, ok := l.([]any); ok {
			if rr, ok := r.([]any); ok {
				out := make([]any, 0, len(ll)+len(rr))
				out = append(out, ll...)
				out = append(out, rr...)
				return out, nil
			}
		}
		return numericOp(op, l, r)
	case "-", "*", "/", "%":
		return numericOp(op, l, r)
	}
	return nil, fmt.Errorf("full engine: unsupported binary op %q", op)
}

func equalsAny(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	// Numeric coercion: int64 vs float64.
	if af, aok := toFloat(a); aok {
		if bf, bok := toFloat(b); bok {
			return af == bf
		}
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case float64:
		return x, true
	case float32:
		return float64(x), true
	}
	return 0, false
}

func compareAny(op string, l, r any) (bool, error) {
	if l == nil || r == nil {
		return false, nil
	}
	if lf, ok := toFloat(l); ok {
		if rf, ok := toFloat(r); ok {
			switch op {
			case "<":
				return lf < rf, nil
			case ">":
				return lf > rf, nil
			case "<=":
				return lf <= rf, nil
			case ">=":
				return lf >= rf, nil
			}
		}
	}
	ls, lok := l.(string)
	rs, rok := r.(string)
	if lok && rok {
		switch op {
		case "<":
			return ls < rs, nil
		case ">":
			return ls > rs, nil
		case "<=":
			return ls <= rs, nil
		case ">=":
			return ls >= rs, nil
		}
	}
	return false, nil
}

func numericOp(op string, l, r any) (any, error) {
	lf, lok := toFloat(l)
	rf, rok := toFloat(r)
	if !lok || !rok {
		return nil, fmt.Errorf("full engine: numeric op %q on non-numeric (%T, %T)", op, l, r)
	}
	switch op {
	case "+":
		return lf + rf, nil
	case "-":
		return lf - rf, nil
	case "*":
		return lf * rf, nil
	case "/":
		if rf == 0 {
			return nil, errors.New("full engine: division by zero")
		}
		return lf / rf, nil
	case "%":
		if rf == 0 {
			return nil, errors.New("full engine: modulo by zero")
		}
		return float64(int64(lf) % int64(rf)), nil
	}
	return nil, fmt.Errorf("full engine: unsupported numeric op %q", op)
}

// normalizeForKey produces a stable string representation of a value, used as
// the identity basis for WITH/RETURN grouping and for DISTINCT deduplication.
// It is purely in-memory — never persisted, never compared across processes —
// so the encoding is free to change.
//
// The encoding is INJECTIVE: distinct values must never render alike. Two
// values that collide are silently merged into one group, or one is dropped
// from a DISTINCT list — data loss with no error anywhere. Free text reaches
// here (a lens collects `presentation.data.name` into a map), so a rendering
// that simply interleaved delimiters would let an authored name impersonate
// structure: `{name: "a,key:b"}` must not render as `{name:a,key:b}` does.
//
// Every leaf therefore carries a TYPE TAG, and every variable-length token is
// LENGTH-PREFIXED, which makes the rendering unambiguously parseable and hence
// injective — a string can no longer forge a separator, and `1`, `1.0`, `"1"`
// and `true` stay distinct from each other and from `"<nil>"`.
func normalizeForKey(v any) string {
	var b strings.Builder
	writeNormalizedKey(&b, v)
	return b.String()
}

// writeNormalizedKey appends v's injective rendering to b.
func writeNormalizedKey(b *strings.Builder, v any) {
	// token writes a length-prefixed, therefore self-delimiting, string.
	token := func(tag byte, s string) {
		b.WriteByte(tag)
		b.WriteString(strconv.Itoa(len(s)))
		b.WriteByte(':')
		b.WriteString(s)
	}
	switch x := v.(type) {
	case nil:
		b.WriteByte('z')
	case string:
		token('s', x)
	case bool:
		if x {
			b.WriteByte('T')
		} else {
			b.WriteByte('F')
		}
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('m')
		b.WriteString(strconv.Itoa(len(keys)))
		b.WriteByte('{')
		for _, k := range keys {
			token('k', k)
			writeNormalizedKey(b, x[k])
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('l')
		b.WriteString(strconv.Itoa(len(x)))
		b.WriteByte('[')
		for _, el := range x {
			writeNormalizedKey(b, el)
		}
		b.WriteByte(']')
	case *nodeRef:
		if x == nil {
			b.WriteByte('z')
			return
		}
		token('n', x.key)
	case int64:
		token('i', strconv.FormatInt(x, 10))
	case float64:
		token('f', strconv.FormatFloat(x, 'g', -1, 64))
	default:
		// Any other type the engine produces still renders, tagged by its Go
		// type so two different types can never share a rendering.
		token('x', fmt.Sprintf("%T=%v", v, v))
	}
}
