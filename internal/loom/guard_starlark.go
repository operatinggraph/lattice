package loom

import (
	"fmt"
	"time"

	starlarkjson "go.starlark.net/lib/json"
	starlarklib "go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/asolgan/lattice/internal/starlarksandbox"
)

// This file builds the frozen Starlark values a §10.5 `{reads, starlark}`
// guard predicate reads (design doc §2.2) and the pure module set it may
// call. It has NO Core KV access of its own — guard_eval.go does the
// hydration and hands this file already-decoded envelope bodies.

// guardStarlarkWallBudget / guardStarlarkMaxSteps bound one guard predicate
// call. A guard is a pure point-read predicate (no impure kv.Read, no I/O),
// so both budgets are far tighter than the Processor's script budget
// (starlark_runner.go's 250ms/1e6 steps) — generous enough for a real
// predicate, tight enough that a pathological guard fails fast rather than
// stalling engine's transition loop.
const (
	guardStarlarkWallBudget = 50 * time.Millisecond
	guardStarlarkMaxSteps   = 100_000
)

// guardStarlarkGlobals returns the predeclared-name set a guard predicate may
// reference: the pure, deterministic module set only (design doc §2.3's
// determinism table) — no `kv`, no `nanoid`, no host clock. Used both to
// compile-check a guard at parse time (starlarksandbox.Validate) and to
// evaluate it (starlarksandbox.Execute); the two calls share this so a
// script that resolves at parse time is guaranteed to resolve identically at
// eval time.
func guardStarlarkGlobals() starlarklib.StringDict {
	return starlarklib.StringDict{
		"crypto": starlarkstruct.FromStringDict(starlarkstruct.Default, starlarksandbox.CryptoBuiltins()),
		"time":   starlarkstruct.FromStringDict(starlarkstruct.Default, starlarksandbox.TimeBuiltins()),
		"json":   starlarkjson.Module,
	}
}

// starlarkDataDict wraps one vertex/aspect's decoded `data` envelope (a JSON
// object) as a Starlark value supporting BOTH attribute access
// (`subject.data.<field>`) and subscript access (`subject.data["<field>"]`)
// — the guard-path grammar's dot-string syntax (design doc §2.2) resolves
// literally as Starlark dot-notation, so `data` must expose arbitrary keys as
// attributes, not just the builtin methods a plain *starlark.Dict exposes
// (get/keys/items/...). Attribute access to a key the map does not contain
// reads as None — the same "absent" reading the declarative grammar gives a
// missing field (guard_eval.go:absent) — so a guard author can write either
// `subject.data.age` or `subject.data.get("age")`. Subscript access
// (`data["age"]`) keeps ordinary dict semantics (a missing key errors),
// matching how `state[key]` already behaves for the Processor's scripts.
type starlarkDataDict struct {
	d *starlarklib.Dict
}

func newStarlarkDataDict(data map[string]any) *starlarkDataDict {
	return &starlarkDataDict{d: starlarksandbox.GoMapToStarlarkDict(data)}
}

func (s *starlarkDataDict) String() string                { return s.d.String() }
func (s *starlarkDataDict) Type() string                  { return "data" }
func (s *starlarkDataDict) Freeze()                       { s.d.Freeze() }
func (s *starlarkDataDict) Truth() starlarklib.Bool       { return s.d.Truth() }
func (s *starlarkDataDict) Hash() (uint32, error)         { return 0, fmt.Errorf("data is not hashable") }
func (s *starlarkDataDict) Iterate() starlarklib.Iterator { return s.d.Iterate() }

// Get implements starlarklib.Mapping — `data["field"]` and `"field" in data`.
// Ordinary dict semantics: a missing key is not found (the caller's index
// expression raises, matching state[key] elsewhere in the codebase).
func (s *starlarkDataDict) Get(k starlarklib.Value) (v starlarklib.Value, found bool, err error) {
	return s.d.Get(k)
}

// AttrNames implements starlarklib.HasAttrs. It reports only the dict's
// builtin method names (get/keys/items/...) — the field-shaped attribute
// path (below) intentionally never errors, so it needs no name to report.
func (s *starlarkDataDict) AttrNames() []string { return s.d.AttrNames() }

// Attr implements starlarklib.HasAttrs — `data.field`. A builtin dict method
// name (get/keys/items/...) wins first (so `.get(...)` keeps working); any
// other name is looked up as a data key, reading as None when absent rather
// than raising (mirroring the declarative grammar's absent() semantics — see
// the type doc above).
func (s *starlarkDataDict) Attr(name string) (starlarklib.Value, error) {
	if v, err := s.d.Attr(name); v != nil || err != nil {
		return v, err
	}
	v, found, err := s.d.Get(starlarklib.String(name))
	if err != nil {
		return nil, err
	}
	if !found {
		return starlarklib.None, nil
	}
	return v, nil
}

var (
	_ starlarklib.Value    = (*starlarkDataDict)(nil)
	_ starlarklib.Mapping  = (*starlarkDataDict)(nil)
	_ starlarklib.Iterable = (*starlarkDataDict)(nil)
	_ starlarklib.HasAttrs = (*starlarkDataDict)(nil)
)

// starlarkVertexNode projects one decoded Core KV aspect envelope body into
// the Starlark value a guard sees at `subject.<aspect>`: None for a missing
// / soft-deleted envelope (body == nil — guardResolver.envelope already
// filters tombstones), else a struct exposing .data/.class/.isDeleted. The
// ROOT vertex is handled separately (buildStarlarkSubject, below) —
// `subject` itself is never allowed to collapse to None the way an aspect
// does.
func starlarkVertexNode(body map[string]any) starlarklib.Value {
	if body == nil {
		return starlarklib.None
	}
	data, _ := body["data"].(map[string]any)
	class, _ := body["class"].(string)
	isDeleted, _ := body["isDeleted"].(bool)
	return &starlarkSubject{
		data:      newStarlarkDataDict(data),
		class:     class,
		isDeleted: isDeleted,
		aspects:   nil,
	}
}

// starlarkSubject is the Starlark value bound to a guard predicate's
// `subject` parameter (and, recursively, to each aspect it embeds) — a
// struct-shaped value exposing `.data` / `.class` / `.isDeleted`, PLUS one
// attribute per aspect localName declared in the guard's `reads` list.
// Unlike starlarkstruct.Struct, attribute access to an undeclared name reads
// as None instead of raising: design doc §2.2 — "an aspect not in `reads` is
// simply not in `subject` → reads as absent (None)." aspects only ever
// contains entries for names the guard declared in `reads`; each entry is
// itself either a starlarkSubject (aspect present) or starlark.None (aspect
// absent/soft-deleted) — so an undeclared name and a declared-but-absent
// aspect converge on the same None the guard author observes.
type starlarkSubject struct {
	data      *starlarkDataDict
	class     string
	isDeleted bool
	aspects   map[string]starlarklib.Value
}

func (s *starlarkSubject) String() string { return fmt.Sprintf("subject(class=%q)", s.class) }
func (s *starlarkSubject) Type() string   { return "subject" }
func (s *starlarkSubject) Freeze() {
	s.data.Freeze()
	for _, v := range s.aspects {
		v.Freeze()
	}
}
func (s *starlarkSubject) Truth() starlarklib.Bool { return starlarklib.True }
func (s *starlarkSubject) Hash() (uint32, error)   { return 0, fmt.Errorf("subject is not hashable") }

func (s *starlarkSubject) AttrNames() []string {
	names := []string{"data", "class", "isDeleted"}
	for name := range s.aspects {
		names = append(names, name)
	}
	return names
}

// Attr implements starlarklib.HasAttrs. See the type doc: an aspect name not
// in s.aspects reads as None, never an AttributeError.
func (s *starlarkSubject) Attr(name string) (starlarklib.Value, error) {
	switch name {
	case "data":
		return s.data, nil
	case "class":
		return starlarklib.String(s.class), nil
	case "isDeleted":
		return starlarklib.Bool(s.isDeleted), nil
	}
	if v, ok := s.aspects[name]; ok {
		return v, nil
	}
	return starlarklib.None, nil
}

var (
	_ starlarklib.Value    = (*starlarkSubject)(nil)
	_ starlarklib.HasAttrs = (*starlarkSubject)(nil)
)

// buildStarlarkSubject projects the subject root vertex body + its
// requested aspect bodies into the frozen `subject` value a guard predicate
// receives. Unlike an aspect (starlarkVertexNode, which DOES collapse to
// None when its envelope is absent), `subject` itself is NEVER None — it is
// the pattern instance's known triggering entity, always addressable, same
// as the declarative grammar's `subject.data.<field>` never requires the
// root vertex to exist to be evaluated (guard_eval.go's resolve() treats a
// missing root exactly like a missing aspect: the FIELD reads absent, not
// the whole subject). The canonical example (Contract §10.5:
// `def guard(subject): return subject.profile.data.age >= 18`) never
// defensively checks `subject != None` first — only `subject.profile !=
// None` (design doc §2.2) — confirming this asymmetry is the intended
// shape. rootBody nil (missing/soft-deleted root) degrades subject.data to
// an empty projection (every field reads absent) without hiding the
// aspects namespace. aspectBodies carries one entry per aspect name in the
// guard's `reads` list (guard_eval.go already resolved absence to a nil
// body per key), so a `reads`-declared-but-absent aspect and an undeclared
// aspect both read as None (starlarkSubject.Attr, above).
func buildStarlarkSubject(rootBody map[string]any, aspectBodies map[string]map[string]any) starlarklib.Value {
	var data map[string]any
	var class string
	var isDeleted bool
	if rootBody != nil {
		data, _ = rootBody["data"].(map[string]any)
		class, _ = rootBody["class"].(string)
		isDeleted, _ = rootBody["isDeleted"].(bool)
	}
	sub := &starlarkSubject{
		data:      newStarlarkDataDict(data),
		class:     class,
		isDeleted: isDeleted,
		aspects:   make(map[string]starlarklib.Value, len(aspectBodies)),
	}
	for name, body := range aspectBodies {
		sub.aspects[name] = starlarkVertexNode(body)
	}
	return sub
}
