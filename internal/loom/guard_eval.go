package loom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	starlarklib "go.starlark.net/starlark"

	"github.com/operatinggraph/lattice/internal/starlarksandbox"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// evalGuard evaluates a parsed §10.5 guard against the subject's CURRENT Core KV
// state and returns the resulting single boolean (true = the guard's step runs).
//
// Hydration is per-evaluation with NO cross-evaluation cache: each evalGuard
// call point-reads the subject root vertex and each referenced aspect from Core
// KV at step entry. WITHIN one call the resolver dedupes GETs per distinct key
// (a composite guard referencing two fields of the same aspect fetches it once).
// This dedup is a correctness requirement, not just an optimization: it pins the
// whole guard's evaluation to ONE snapshot of each key, so a concurrent write
// mid-evaluation cannot make allOf/anyOf straddle two states of the same key.
//
// Today there is no cache ACROSS steps/transitions — a guard-heavy pattern
// re-reads the subject on every step entry; a per-transition or per-instance
// hydration cache would cut that GET volume if it ever showed up as a hotspot.
//
// A missing root vertex, missing aspect, soft-deleted (isDeleted) envelope, or
// missing field all resolve to "absent" (§10.5) — none is an error; absence is
// exactly what {absent} tests for. Only an unexpected Core KV / decode failure
// returns a non-nil error.
func evalGuard(ctx context.Context, conn *substrate.Conn, coreKVBucket, subjectKey string, g *guard) (bool, error) {
	r := &guardResolver{
		ctx:          ctx,
		conn:         conn,
		coreKVBucket: coreKVBucket,
		subjectKey:   subjectKey,
		envelopes:    make(map[string]map[string]any),
		fetched:      make(map[string]bool),
	}
	return r.eval(g)
}

// guardResolver holds the per-evaluation snapshot cache (one fetched envelope
// per distinct Core KV key) so a single guard evaluation sees one snapshot of
// each key.
type guardResolver struct {
	ctx          context.Context
	conn         *substrate.Conn
	coreKVBucket string
	subjectKey   string

	// envelopes maps a Core KV key → its decoded body (nil for missing or
	// soft-deleted, the absent sentinel). fetched records which keys have been
	// read this evaluation so a second reference is served from the snapshot.
	envelopes map[string]map[string]any
	fetched   map[string]bool
}

func (r *guardResolver) eval(g *guard) (bool, error) {
	switch g.kind {
	case guardAbsent:
		absent, err := r.absent(g.path)
		if err != nil {
			return false, err
		}
		return absent, nil
	case guardPresent:
		absent, err := r.absent(g.path)
		if err != nil {
			return false, err
		}
		return !absent, nil
	case guardEquals:
		return r.equals(g)
	case guardAllOf:
		for _, c := range g.children {
			ok, err := r.eval(c)
			if err != nil {
				return false, err
			}
			if !ok {
				return false, nil
			}
		}
		return true, nil
	case guardAnyOf:
		for _, c := range g.children {
			ok, err := r.eval(c)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil
	case guardNot:
		ok, err := r.eval(g.children[0])
		if err != nil {
			return false, err
		}
		return !ok, nil
	case guardStarlark:
		return r.evalStarlark(g)
	default:
		return false, fmt.Errorf("loom: unknown guard kind %d", g.kind)
	}
}

// resolve returns the value at a path and whether it was found at all (a
// found:false means the key/aspect/field was missing or soft-deleted). The
// returned value is the raw JSON-decoded field value when found.
func (r *guardResolver) resolve(p guardPath) (value any, found bool, err error) {
	key := r.subjectKey
	if p.aspect != "" {
		key = r.subjectKey + "." + p.aspect
	}
	body, err := r.envelope(key)
	if err != nil {
		return nil, false, err
	}
	if body == nil {
		// Missing or soft-deleted vertex/aspect → every field under it is absent.
		return nil, false, nil
	}
	data, ok := body["data"].(map[string]any)
	if !ok {
		// No data envelope → the field cannot be present.
		return nil, false, nil
	}
	v, ok := data[p.field]
	if !ok {
		return nil, false, nil
	}
	return v, true, nil
}

// absent reports whether a path is absent per the pinned §10.5 semantics:
// null / missing / soft-deleted (isDeleted) / (for strings) empty-after-trim.
// "0" / false / 0 are PRESENT (only emptiness/nullness/missingness/soft-delete
// count — never "falsy").
func (r *guardResolver) absent(p guardPath) (bool, error) {
	v, found, err := r.resolve(p)
	if err != nil {
		return false, err
	}
	if !found {
		return true, nil
	}
	switch tv := v.(type) {
	case nil:
		// JSON null field value → absent.
		return true, nil
	case string:
		return strings.TrimSpace(tv) == "", nil
	default:
		// Numbers (incl. 0), bools (incl. false), objects, arrays → present.
		return false, nil
	}
}

// equals implements {equals:{path,value}} per §10.5: an absent path never
// equals anything (including null/empty-string value); a present path compares
// type-aware to value (numbers numerically regardless of int/float JSON
// encoding, strings/bools directly).
func (r *guardResolver) equals(g *guard) (bool, error) {
	absent, err := r.absent(g.path)
	if err != nil {
		return false, err
	}
	if absent {
		return false, nil
	}
	v, found, err := r.resolve(g.path)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	return jsonValuesEqual(v, g.value), nil
}

// evalStarlark evaluates a §10.5 {reads, starlark} guard: hydrates the
// subject root + each aspect named in g.starlarkReads via r.envelope (so it
// shares the resolver's one-snapshot-per-key memoization with any
// declarative sibling in the same composite guard), builds the frozen
// `subject` value (guard_starlark.go), and calls the shared sandbox leaf with
// entrypoint "guard". parseGuard already compile-checked the script
// (starlarksandbox.Validate, at parse time), so a sandbox failure here can
// only be a data-dependent runtime failure inside the predicate body (e.g. a
// guard that dereferences `.data` on a None aspect without checking presence
// first — the documented authoring hazard, design doc §2.2) or a wall/step
// budget trip on a pathological guard; either is a genuine evaluation error,
// not a false result.
func (r *guardResolver) evalStarlark(g *guard) (bool, error) {
	rootBody, err := r.envelope(r.subjectKey)
	if err != nil {
		return false, err
	}
	aspectBodies := make(map[string]map[string]any, len(g.starlarkReads))
	for _, aspect := range g.starlarkReads {
		body, err := r.envelope(r.subjectKey + "." + aspect)
		if err != nil {
			return false, err
		}
		aspectBodies[aspect] = body
	}

	subjectVal := buildStarlarkSubject(rootBody, aspectBodies)
	subjectVal.Freeze()

	out, sErr := starlarksandbox.Execute(r.ctx, g.starlarkSource, "guard",
		starlarklib.Tuple{subjectVal}, guardStarlarkGlobals(),
		starlarksandbox.Budget{Wall: guardStarlarkWallBudget, MaxSteps: guardStarlarkMaxSteps})
	if sErr != nil {
		return false, fmt.Errorf("loom: starlark guard eval %s: %s", sErr.Code, sErr.Message)
	}
	b, ok := out.(starlarklib.Bool)
	if !ok {
		return false, fmt.Errorf("loom: starlark guard must return a bool, got %s", out.Type())
	}
	return bool(b), nil
}

// envelope point-reads a Core KV key once per evaluation and returns its decoded
// body, or nil for a missing / soft-deleted (isDeleted) / null-body entry
// (mirroring Refractor's fetchNode tombstone check,
// internal/refractor/ruleengine/full/executor.go:453-476, re-implemented
// loom-local: loom imports only substrate/* + stdlib, per doc.go). The result
// is memoized so repeated references to the same key within one guard see one
// snapshot.
func (r *guardResolver) envelope(key string) (map[string]any, error) {
	if r.fetched[key] {
		return r.envelopes[key], nil
	}
	body, err := r.fetchEnvelope(key)
	if err != nil {
		return nil, err
	}
	r.envelopes[key] = body
	r.fetched[key] = true
	return body, nil
}

func (r *guardResolver) fetchEnvelope(key string) (map[string]any, error) {
	entry, err := r.conn.KVGet(r.ctx, r.coreKVBucket, key)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("loom: guard hydrate %q: %w", key, err)
	}
	var body map[string]any
	if err := json.Unmarshal(entry.Value, &body); err != nil {
		return nil, fmt.Errorf("loom: guard decode %q: %w", key, err)
	}
	// A JSON null body decodes to a nil map → treat as absent/tombstone.
	if body == nil {
		return nil, nil
	}
	if deleted, _ := body["isDeleted"].(bool); deleted {
		return nil, nil
	}
	return body, nil
}

// jsonValuesEqual compares a Core-KV-decoded value (a, from encoding/json:
// float64/string/bool/nil/...) to a guard's comparand (b, decoded the same way)
// type-aware. Numbers compare numerically (both are float64 after JSON decode);
// strings and bools compare directly; null equals null.
func jsonValuesEqual(a, b any) bool {
	switch av := a.(type) {
	case nil:
		return b == nil
	case float64:
		bv, ok := b.(float64)
		return ok && av == bv
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	default:
		// Objects/arrays are not legal equals comparands in the §10.5 grammar
		// (the path reads a leaf field); fall back to a strict mismatch.
		return false
	}
}
