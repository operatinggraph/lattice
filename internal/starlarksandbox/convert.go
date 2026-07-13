package starlarksandbox

import (
	"fmt"
	"sort"

	starlarklib "go.starlark.net/starlark"
)

// GoMapToStarlarkDict converts a json.Unmarshal-style generic map into a
// Starlark dict, recursively. Keys are inserted in SORTED order rather than
// Go's randomized map-iteration order: go.starlark.net's Dict preserves
// insertion order for iteration (`for k in d`, `.keys()`/`.items()`,
// `json.encode(d)`), so an unsorted insertion would make a script that
// observes iteration order see a different result across two calls over the
// byte-identical input map — this leaf is used by callers (the Loom guard
// predicate, guard_starlark.go) that require the SAME output every time for
// the SAME input, not merely per-key value correctness.
func GoMapToStarlarkDict(m map[string]interface{}) *starlarklib.Dict {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	d := new(starlarklib.Dict)
	for _, k := range keys {
		_ = d.SetKey(starlarklib.String(k), GoValueToStarlark(m[k]))
	}
	return d
}

// GoValueToStarlark converts a single json.Unmarshal-shaped Go value
// (string/bool/int/int64/float64/map[string]interface{}/[]interface{}/nil)
// into its Starlark equivalent. A whole-valued float64 (every JSON number
// decodes as float64 in Go) converts to a Starlark Int so a script
// comparing `x == 5` sees an integer, not a float; a fractional float64
// stays a Float. Any other Go type falls back to its Sprintf string form
// rather than being dropped or panicking — this keeps a script from
// silently consuming a half-converted value.
func GoValueToStarlark(v interface{}) starlarklib.Value {
	switch x := v.(type) {
	case nil:
		return starlarklib.None
	case string:
		return starlarklib.String(x)
	case bool:
		return starlarklib.Bool(x)
	case int:
		return starlarklib.MakeInt(x)
	case int64:
		return starlarklib.MakeInt64(x)
	case float64:
		if x == float64(int64(x)) {
			return starlarklib.MakeInt64(int64(x))
		}
		return starlarklib.Float(x)
	case map[string]interface{}:
		return GoMapToStarlarkDict(x)
	case []interface{}:
		l := starlarklib.NewList(nil)
		for _, item := range x {
			_ = l.Append(GoValueToStarlark(item))
		}
		return l
	default:
		return starlarklib.String(fmt.Sprintf("%v", x))
	}
}

// StarlarkValueToGo converts a Starlark value back into its Go
// json.Unmarshal-shaped equivalent — the inverse of GoValueToStarlark. An
// Int too wide for int64 degrades to its decimal string rather than
// silently truncating to a wrong number. A value with no structural Go
// analogue (e.g. a Tuple) renders to its String() form.
func StarlarkValueToGo(v starlarklib.Value) interface{} {
	switch x := v.(type) {
	case starlarklib.NoneType:
		return nil
	case starlarklib.String:
		return string(x)
	case starlarklib.Bool:
		return bool(x)
	case starlarklib.Int:
		i, ok := x.Int64()
		if !ok {
			return x.String()
		}
		return i
	case starlarklib.Float:
		return float64(x)
	case *starlarklib.Dict:
		return StarlarkDictToGoMap(x)
	case *starlarklib.List:
		out := make([]interface{}, x.Len())
		for i := 0; i < x.Len(); i++ {
			out[i] = StarlarkValueToGo(x.Index(i))
		}
		return out
	default:
		return x.String()
	}
}

// StarlarkDictToGoMap converts a Starlark dict into a Go generic map. Only
// string-keyed entries carry over — a non-string key (legal in Starlark,
// illegal as a JSON object key) is skipped, not coerced.
func StarlarkDictToGoMap(d *starlarklib.Dict) map[string]interface{} {
	out := make(map[string]interface{}, d.Len())
	for _, item := range d.Items() {
		k, ok := item[0].(starlarklib.String)
		if !ok {
			continue
		}
		out[string(k)] = StarlarkValueToGo(item[1])
	}
	return out
}
