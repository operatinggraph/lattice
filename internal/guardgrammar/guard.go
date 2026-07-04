// Package guardgrammar implements the §10.5 declarative guard grammar — the
// ONE parser shared by every consumer of the grammar: Loom step guards
// (Contract #10 §10.5), op-DDL `effects` (Contract #10 §10.8 Planner
// extension, install-time validated by pkgmgr), and the Weaver planner's
// precondition/goal predicates (Fires 3/5/6). A guard is a pure declarative
// predicate over a subject's current state — atoms {absent|present: <path>}
// and {equals: {path, value}}, composable with {allOf|anyOf|not} into ONE
// boolean (never branching). Parsing has no I/O and no engine dependency;
// evaluating a Guard against live Core KV state is each engine's own concern
// (internal/loom's evalGuard) — this package only parses and represents the
// AST.
//
// The Starlark escape hatch ({reads, starlark}) is RESERVED: it is recognized
// at parse time and rejected with a precise sentinel — the shared pure-evaluator
// extraction lands only when the first Starlark guard is authored (§10.5), so it
// is out of scope here.
package guardgrammar

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrMalformedGuard is the generic parse failure for a guard whose shape does
// not match the declarative grammar (unknown key, multiple keys, wrong types,
// empty composition list, bad path shape). It is wrapped with positional detail
// by callers; match it via errors.Is.
var ErrMalformedGuard = errors.New("malformed guard")

// ErrStarlarkReserved is the distinct sentinel for the reserved Starlark escape
// hatch ({reads, starlark}). It is NOT a generic malformed-guard error: a
// Starlark guard is well-formed but its evaluator is not yet built (§10.5).
var ErrStarlarkReserved = errors.New("starlark guards are reserved, not yet supported")

// Kind discriminates a parsed guard node.
type Kind int

const (
	KindAbsent Kind = iota
	KindPresent
	KindEquals
	KindAllOf
	KindAnyOf
	KindNot
)

// Guard is the parsed AST of a §10.5 declarative guard. Exactly one shape is
// populated per node, selected by Kind.
type Guard struct {
	Kind Kind

	// Path is set for KindAbsent / KindPresent / KindEquals.
	Path Path
	// Value is the comparand for KindEquals (any JSON scalar).
	Value any
	// HasValue records that an explicit value (possibly JSON null) was supplied
	// for KindEquals — distinguishing {equals:{path,value:null}} from a value
	// the JSON omitted (the latter is malformed).
	HasValue bool

	// Children is set for KindAllOf / KindAnyOf (>= 1) and KindNot (exactly 1).
	Children []*Guard
}

// Path is a parsed §10.5 path. Exactly two shapes are legal:
//
//   - subject.data.<field>           → root: read <field> from the subject root
//     vertex's own data envelope.       (Aspect == "")
//   - subject.<aspect>.data.<field>  → aspect: point-read <subjectKey>.<aspect>
//     and read <field> from its data.   (Aspect != "")
type Path struct {
	Aspect string // "" → root vertex; else the aspect localName
	Field  string // the data.<field> leaf
}

// guardEnvelope is the wire shape a guard object may take. A well-formed guard
// object has exactly ONE of the declarative keys set (or the reserved Starlark
// pair). Decoding into RawMessage lets Parse enforce the one-key rule and
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

// Parse parses one §10.5 guard from raw JSON. It rejects (ErrMalformedGuard)
// any object that is not exactly one declarative shape, and recognizes the
// reserved Starlark pair as ErrStarlarkReserved. Composition is parsed
// recursively. Parse does NO Core KV access — it is a pure shape/validate
// pass run at load time (Loom pattern CDC-load, pkgmgr op-DDL effects install).
func Parse(raw json.RawMessage) (*Guard, error) {
	// Reject anything that is not a JSON object up front (an atom path is carried
	// as a string VALUE under a key, never a bare string guard).
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, fmt.Errorf("%w: guard must be a JSON object", ErrMalformedGuard)
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
		return nil, fmt.Errorf("%w: %v", ErrMalformedGuard, err)
	}

	// The reserved Starlark shape: either key present routes to the reserved
	// sentinel (well-formed-but-unsupported, NOT generic malformed).
	if env.Reads != nil || env.Starlark != nil {
		return nil, ErrStarlarkReserved
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
			"absent|present|equals|allOf|anyOf|not (got %d)", ErrMalformedGuard, set)
	}

	switch {
	case env.Absent != nil:
		p, err := ParsePath(*env.Absent)
		if err != nil {
			return nil, err
		}
		return &Guard{Kind: KindAbsent, Path: p}, nil
	case env.Present != nil:
		p, err := ParsePath(*env.Present)
		if err != nil {
			return nil, err
		}
		return &Guard{Kind: KindPresent, Path: p}, nil
	case env.Equals != nil:
		return parseEquals(env.Equals)
	case env.AllOf != nil:
		children, err := parseGuardList(env.AllOf)
		if err != nil {
			return nil, err
		}
		return &Guard{Kind: KindAllOf, Children: children}, nil
	case env.AnyOf != nil:
		children, err := parseGuardList(env.AnyOf)
		if err != nil {
			return nil, err
		}
		return &Guard{Kind: KindAnyOf, Children: children}, nil
	default: // env.Not != nil
		child, err := Parse(env.Not)
		if err != nil {
			return nil, err
		}
		return &Guard{Kind: KindNot, Children: []*Guard{child}}, nil
	}
}

// checkNoDuplicateKeys walks raw's full token stream and rejects (as
// ErrMalformedGuard) any JSON object — at any depth, including nested
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
			return fmt.Errorf("%w: %v", ErrMalformedGuard, err)
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
					return fmt.Errorf("%w: duplicate key %q in guard object", ErrMalformedGuard, t)
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
func parseEquals(raw json.RawMessage) (*Guard, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var body equalsBody
	if err := dec.Decode(&body); err != nil {
		return nil, fmt.Errorf("%w: equals body: %v", ErrMalformedGuard, err)
	}
	if body.Path == nil {
		return nil, fmt.Errorf("%w: equals requires a path", ErrMalformedGuard)
	}
	p, err := ParsePath(*body.Path)
	if err != nil {
		return nil, err
	}
	// value must be present in the JSON (a missing value is malformed). A literal
	// JSON null is a legal value the evaluator handles (an absent path never
	// equals null; a present null field does).
	hasValue := json.Valid(raw) && rawHasKey(raw, "value")
	if !hasValue {
		return nil, fmt.Errorf("%w: equals requires a value", ErrMalformedGuard)
	}
	// The comparand must be a JSON scalar (string/number/bool/null). The §10.5
	// grammar's path always reads a leaf field of a data envelope — a leaf can
	// never decode to an object or array, so an object/array comparand could
	// never match (the evaluator's jsonValuesEqual falls through to a strict
	// mismatch for those types). Reject at load time rather than ship a guard
	// that silently never fires.
	switch body.Value.(type) {
	case nil, string, float64, bool:
		// scalar (or JSON null) — legal.
	default:
		return nil, fmt.Errorf("%w: equals value must be a JSON scalar (string, number, bool, or null), got %T", ErrMalformedGuard, body.Value)
	}
	return &Guard{Kind: KindEquals, Path: p, Value: body.Value, HasValue: true}, nil
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
func parseGuardList(raw json.RawMessage) ([]*Guard, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("%w: composition must be an array of guards: %v", ErrMalformedGuard, err)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("%w: empty composition list", ErrMalformedGuard)
	}
	out := make([]*Guard, 0, len(items))
	for _, it := range items {
		g, err := Parse(it)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}

// ParsePath parses a §10.5 explicit path into its (aspect, field) shape.
// Exactly two forms are legal:
//
//	subject.data.<field>          → {Aspect:"", Field:<field>}
//	subject.<aspect>.data.<field> → {Aspect:<aspect>, Field:<field>}
//
// Any other shape (no subject. prefix, an aspect path without .data., a deeper
// nested field, a link-walk-shaped path) is malformed. <field> is a single
// segment — the grammar reads a leaf field of the data envelope, not a nested
// path (a nested predicate is a Starlark/Weaver concern, out of scope).
func ParsePath(p string) (Path, error) {
	p = strings.TrimSpace(p)
	rest, ok := strings.CutPrefix(p, "subject.")
	if !ok {
		return Path{}, fmt.Errorf("%w: path %q must start with %q", ErrMalformedGuard, p, "subject.")
	}
	segs := strings.Split(rest, ".")
	switch len(segs) {
	case 2:
		// data.<field>
		if segs[0] != "data" || segs[1] == "" {
			return Path{}, fmt.Errorf("%w: root path %q must be subject.data.<field>", ErrMalformedGuard, p)
		}
		return Path{Aspect: "", Field: segs[1]}, nil
	case 3:
		// <aspect>.data.<field>
		if segs[0] == "" || segs[1] != "data" || segs[2] == "" {
			return Path{}, fmt.Errorf("%w: aspect path %q must be subject.<aspect>.data.<field>", ErrMalformedGuard, p)
		}
		return Path{Aspect: segs[0], Field: segs[2]}, nil
	default:
		return Path{}, fmt.Errorf("%w: path %q is not subject.data.<field> or subject.<aspect>.data.<field>", ErrMalformedGuard, p)
	}
}
