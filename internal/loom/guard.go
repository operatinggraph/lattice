package loom

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/operatinggraph/lattice/internal/starlarksandbox"
)

// Guard grammar (Contract #10 §10.5). A guard is a pure predicate over the
// subject's current state, either DECLARATIVE — atoms {absent|present: <path>}
// and {equals: {path, value}}, composable with {allOf|anyOf|not} into ONE
// boolean (never branching) — or the Starlark escape hatch ({reads, starlark},
// guard.go's guardStarlark branch + guard_eval.go + guard_starlark.go). Either
// way a guard is rebuildable by replaying it against current Core KV state,
// which is what makes the instance cursor crash-recoverable (§10.6): a guard
// has no side effects and is deterministic.

// errMalformedGuard is the generic parse failure for a guard whose shape does
// not match the grammar (unknown key, multiple keys, wrong types, empty
// composition list, bad path shape, a starlark script that fails to compile
// or has the wrong entrypoint shape). It is wrapped with positional detail by
// validate(); callers match it via errors.Is.
var errMalformedGuard = errors.New("malformed guard")

// guardKind discriminates a parsed guard node.
type guardKind int

const (
	guardAbsent guardKind = iota
	guardPresent
	guardEquals
	guardAllOf
	guardAnyOf
	guardNot
	guardStarlark
)

// guard is the parsed AST of a §10.5 guard. Exactly one shape is populated
// per node, selected by kind.
type guard struct {
	kind guardKind

	// path is set for guardAbsent / guardPresent / guardEquals.
	path guardPath
	// value is the comparand for guardEquals (any JSON scalar).
	value any
	// hasValue records that an explicit value (possibly JSON null) was supplied
	// for guardEquals — distinguishing {equals:{path,value:null}} from a value
	// the JSON omitted (the latter is malformed).
	hasValue bool

	// children is set for guardAllOf / guardAnyOf (>= 1) and guardNot (exactly 1).
	children []*guard

	// starlarkSource / starlarkReads are set for kind guardStarlark — the
	// {reads, starlark} escape hatch (§10.5). starlarkSource is the raw
	// `def guard(subject): ...` script text, already compile-checked by
	// parseStarlarkGuard (starlarksandbox.Validate). starlarkReads names the
	// subject aspect localNames evalGuard hydrates before calling it (root is
	// always hydrated regardless of this list).
	starlarkSource string
	starlarkReads  []string
}

// guardPath is a parsed §10.5 path. Exactly two shapes are legal:
//
//   - subject.data.<field>           → root: read <field> from the subject root
//     vertex's own data envelope.       (aspect == "")
//   - subject.<aspect>.data.<field>  → aspect: point-read <subjectKey>.<aspect>
//     and read <field> from its data.   (aspect != "")
type guardPath struct {
	aspect string // "" → root vertex; else the aspect localName
	field  string // the data.<field> leaf
}

// guardEnvelope is the wire shape a guard object may take. A well-formed guard
// object has exactly ONE of the declarative keys set (or the reserved Starlark
// pair). Decoding into RawMessage lets parseGuard enforce the one-key rule and
// recurse on composites.
type guardEnvelope struct {
	Absent  *string         `json:"absent"`
	Present *string         `json:"present"`
	Equals  json.RawMessage `json:"equals"`
	AllOf   json.RawMessage `json:"allOf"`
	AnyOf   json.RawMessage `json:"anyOf"`
	Not     json.RawMessage `json:"not"`

	// Reserved Starlark escape hatch (recognized, rejected).
	Reads    json.RawMessage `json:"reads"`
	Starlark *string         `json:"starlark"`
}

// equalsBody is the {path, value} object of an equals atom.
type equalsBody struct {
	Path  *string `json:"path"`
	Value any     `json:"value"`
}

// parseGuard parses one §10.5 guard from raw JSON. It rejects (errMalformedGuard)
// any object that is not exactly one declarative shape, or (for the {reads,
// starlark} escape hatch) fails to compile-check as a well-formed
// `def guard(subject): ...` predicate. Composition is parsed recursively.
// parseGuard does NO Core KV access — it is a pure shape/validate pass, run
// both at pattern-load time (validate()) and, cheaply, on every step
// evaluation (engine.go's advanceToRunnableStep re-parses from the step's raw
// JSON each time, same as every other guard kind — no cross-call cache).
func parseGuard(raw json.RawMessage) (*guard, error) {
	// Reject anything that is not a JSON object up front (an atom path is carried
	// as a string VALUE under a key, never a bare string guard).
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, fmt.Errorf("%w: guard must be a JSON object", errMalformedGuard)
	}

	// Guards are untrusted package data (§10.5) — reject any object in the tree
	// (the guard itself, or a nested composite/equals body) that repeats a key.
	// encoding/json's decoder silently last-wins on duplicate object keys
	// (`{"absent":"X","absent":"Y"}` would parse as Y), which would let a guard
	// author write something that is silently NOT what they wrote. This is a
	// load-time rejection, not an evaluator concern.
	if err := checkNoDuplicateKeys(raw); err != nil {
		return nil, err
	}

	var env guardEnvelope
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		// An unknown top-level key (DisallowUnknownFields) or a type mismatch on a
		// known key both land here — both are malformed shapes.
		return nil, fmt.Errorf("%w: %v", errMalformedGuard, err)
	}

	// Either key present routes to the Starlark escape hatch — recognized
	// distinctly from the declarative atoms below so a malformed starlark
	// guard (e.g. `reads` with no `starlark`) gets a precise error instead of
	// falling through to "exactly one of absent|present|...".
	if env.Reads != nil || env.Starlark != nil {
		return parseStarlarkGuard(env.Reads, env.Starlark)
	}

	// Exactly one declarative key must be set.
	set := 0
	if env.Absent != nil {
		set++
	}
	if env.Present != nil {
		set++
	}
	if env.Equals != nil {
		set++
	}
	if env.AllOf != nil {
		set++
	}
	if env.AnyOf != nil {
		set++
	}
	if env.Not != nil {
		set++
	}
	if set != 1 {
		return nil, fmt.Errorf("%w: a guard object must have exactly one of "+
			"absent|present|equals|allOf|anyOf|not (got %d)", errMalformedGuard, set)
	}

	switch {
	case env.Absent != nil:
		p, err := parseGuardPath(*env.Absent)
		if err != nil {
			return nil, err
		}
		return &guard{kind: guardAbsent, path: p}, nil
	case env.Present != nil:
		p, err := parseGuardPath(*env.Present)
		if err != nil {
			return nil, err
		}
		return &guard{kind: guardPresent, path: p}, nil
	case env.Equals != nil:
		return parseEquals(env.Equals)
	case env.AllOf != nil:
		children, err := parseGuardList(env.AllOf)
		if err != nil {
			return nil, err
		}
		return &guard{kind: guardAllOf, children: children}, nil
	case env.AnyOf != nil:
		children, err := parseGuardList(env.AnyOf)
		if err != nil {
			return nil, err
		}
		return &guard{kind: guardAnyOf, children: children}, nil
	default: // env.Not != nil
		child, err := parseGuard(env.Not)
		if err != nil {
			return nil, err
		}
		return &guard{kind: guardNot, children: []*guard{child}}, nil
	}
}

// checkNoDuplicateKeys walks raw's full token stream and rejects (as
// errMalformedGuard) any JSON object — at any depth, including nested
// composites and an `equals` body — that repeats a key. encoding/json's
// Decode silently last-wins on duplicate keys; a guard is untrusted package
// data, so a repeated key is rejected at load time rather than silently
// resolved to "whichever came last".
func checkNoDuplicateKeys(raw json.RawMessage) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	var stack []*dupKeyFrame
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: %v", errMalformedGuard, err)
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				stack = append(stack, &dupKeyFrame{kind: '{', seen: make(map[string]bool), isKeyPos: true})
			case '[':
				stack = append(stack, &dupKeyFrame{kind: '['})
			case '}', ']':
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
			}
			// A closed container was itself a value within its parent object —
			// toggle the parent's key/value position.
			if (t == '}' || t == ']') && len(stack) > 0 && stack[len(stack)-1].kind == '{' {
				stack[len(stack)-1].isKeyPos = !stack[len(stack)-1].isKeyPos
			}
		case string:
			top := top(stack)
			if top != nil && top.kind == '{' && top.isKeyPos {
				if top.seen[t] {
					return fmt.Errorf("%w: duplicate key %q in guard object", errMalformedGuard, t)
				}
				top.seen[t] = true
				top.isKeyPos = false
				continue
			}
			if top != nil && top.kind == '{' {
				// This string is a value; the next token in this object is a key.
				top.isKeyPos = true
			}
		default:
			// number, bool, nil — a value; if the parent object is in
			// key-position waiting for THIS value, flip back to key-position.
			top := top(stack)
			if top != nil && top.kind == '{' && !top.isKeyPos {
				top.isKeyPos = true
			}
		}
	}
	return nil
}

// dupKeyFrame is one entry per currently-open JSON container in
// checkNoDuplicateKeys's token walk. kind == '{' carries the set of keys seen
// so far in that object plus whether the NEXT token (if any) is a key (true)
// or that key's value (false); kind == '[' carries no state — array elements
// are never object keys.
type dupKeyFrame struct {
	kind     byte
	seen     map[string]bool
	isKeyPos bool
}

// top returns the innermost open container frame, or nil if the stack is empty.
func top(stack []*dupKeyFrame) *dupKeyFrame {
	if len(stack) == 0 {
		return nil
	}
	return stack[len(stack)-1]
}

// parseEquals parses an {path, value} body, requiring both an explicit path and
// an explicit (possibly null) value.
func parseEquals(raw json.RawMessage) (*guard, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var body equalsBody
	if err := dec.Decode(&body); err != nil {
		return nil, fmt.Errorf("%w: equals body: %v", errMalformedGuard, err)
	}
	if body.Path == nil {
		return nil, fmt.Errorf("%w: equals requires a path", errMalformedGuard)
	}
	p, err := parseGuardPath(*body.Path)
	if err != nil {
		return nil, err
	}
	// value must be present in the JSON (a missing value is malformed). A literal
	// JSON null is a legal value the evaluator handles (an absent path never
	// equals null; a present null field does).
	hasValue := json.Valid(raw) && rawHasKey(raw, "value")
	if !hasValue {
		return nil, fmt.Errorf("%w: equals requires a value", errMalformedGuard)
	}
	// The comparand must be a JSON scalar (string/number/bool/null). The §10.5
	// grammar's path always reads a leaf field of a data envelope — a leaf can
	// never decode to an object or array, so an object/array comparand could
	// never match (guard_eval.go's jsonValuesEqual falls through to a strict
	// mismatch for those types). Reject at load time rather than ship a guard
	// that silently never fires.
	switch body.Value.(type) {
	case nil, string, float64, bool:
		// scalar (or JSON null) — legal.
	default:
		return nil, fmt.Errorf("%w: equals value must be a JSON scalar (string, number, bool, or null), got %T", errMalformedGuard, body.Value)
	}
	return &guard{kind: guardEquals, path: p, value: body.Value, hasValue: true}, nil
}

// parseStarlarkGuard parses the {reads, starlark} escape hatch. `starlark`
// must be a non-empty script defining `def guard(subject): ...` (checked by
// compiling it — starlarksandbox.Validate — against the same pure-module
// globals AND budget evalGuard evaluates it with, guardStarlarkGlobals() /
// guardStarlarkWallBudget / guardStarlarkMaxSteps, so a script that
// compile-checks here is guaranteed to resolve identically at eval time, and
// a pathological top-level statement — Validate's Init phase runs the
// script's top-level statements, not just the entrypoint body — fails fast
// rather than hanging: parseGuard re-parses (and so re-validates) a Starlark
// guard on EVERY step-transition attempt, not just once at pattern-install
// time, so an unbudgeted Validate would hang the engine's transition loop on
// every single attempt). `reads` is optional — omitted or empty means the
// guard reads only the subject root (still always hydrated); when present,
// every entry must be a non-empty aspect localName.
func parseStarlarkGuard(rawReads json.RawMessage, starlark *string) (*guard, error) {
	if starlark == nil || strings.TrimSpace(*starlark) == "" {
		return nil, fmt.Errorf("%w: a starlark guard requires a non-empty \"starlark\" script", errMalformedGuard)
	}
	var reads []string
	if rawReads != nil {
		if err := json.Unmarshal(rawReads, &reads); err != nil {
			return nil, fmt.Errorf("%w: \"reads\" must be an array of aspect names: %v", errMalformedGuard, err)
		}
		for _, r := range reads {
			if strings.TrimSpace(r) == "" {
				return nil, fmt.Errorf("%w: \"reads\" entries must be non-empty aspect names", errMalformedGuard)
			}
		}
	}
	if sErr := starlarksandbox.Validate(*starlark, "guard", 1, guardStarlarkGlobals(),
		starlarksandbox.Budget{Wall: guardStarlarkWallBudget, MaxSteps: guardStarlarkMaxSteps}); sErr != nil {
		return nil, fmt.Errorf("%w: starlark guard %s: %s", errMalformedGuard, sErr.Code, sErr.Message)
	}
	return &guard{kind: guardStarlark, starlarkSource: *starlark, starlarkReads: reads}, nil
}

// rawHasKey reports whether a JSON object literal contains the named top-level
// key (used to distinguish an explicit value:null from an omitted value).
func rawHasKey(raw json.RawMessage, key string) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}

// parseGuardList parses a non-empty JSON array of sub-guards (the allOf/anyOf
// composition list). An empty array is a malformed guard (§10.5: allOf([])/
// anyOf([]) are validate-time errors).
func parseGuardList(raw json.RawMessage) ([]*guard, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("%w: composition must be an array of guards: %v", errMalformedGuard, err)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("%w: empty composition list", errMalformedGuard)
	}
	out := make([]*guard, 0, len(items))
	for _, it := range items {
		g, err := parseGuard(it)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}

// parseGuardPath parses a §10.5 explicit path into its (aspect, field) shape.
// Exactly two forms are legal:
//
//	subject.data.<field>          → {aspect:"", field:<field>}
//	subject.<aspect>.data.<field> → {aspect:<aspect>, field:<field>}
//
// Any other shape (no subject. prefix, an aspect path without .data., a deeper
// nested field, a link-walk-shaped path) is malformed. <field> is a single
// segment — the grammar reads a leaf field of the data envelope, not a nested
// path (a nested predicate is a Starlark/Weaver concern, out of scope).
func parseGuardPath(p string) (guardPath, error) {
	p = strings.TrimSpace(p)
	rest, ok := strings.CutPrefix(p, "subject.")
	if !ok {
		return guardPath{}, fmt.Errorf("%w: path %q must start with %q", errMalformedGuard, p, "subject.")
	}
	segs := strings.Split(rest, ".")
	switch len(segs) {
	case 2:
		// data.<field>
		if segs[0] != "data" || segs[1] == "" {
			return guardPath{}, fmt.Errorf("%w: root path %q must be subject.data.<field>", errMalformedGuard, p)
		}
		return guardPath{aspect: "", field: segs[1]}, nil
	case 3:
		// <aspect>.data.<field>
		if segs[0] == "" || segs[1] != "data" || segs[2] == "" {
			return guardPath{}, fmt.Errorf("%w: aspect path %q must be subject.<aspect>.data.<field>", errMalformedGuard, p)
		}
		return guardPath{aspect: segs[0], field: segs[2]}, nil
	default:
		return guardPath{}, fmt.Errorf("%w: path %q is not subject.data.<field> or subject.<aspect>.data.<field>", errMalformedGuard, p)
	}
}
