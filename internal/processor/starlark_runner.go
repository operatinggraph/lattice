package processor

import (
	"context"
	"fmt"
	"strings"
	"time"

	starlarklib "go.starlark.net/starlark"
	starlarkjson "go.starlark.net/lib/json"
	"go.starlark.net/starlarkstruct"

	"github.com/operatinggraph/lattice/internal/starlarksandbox"
)

// DefaultScriptWallBudget is the default wall-clock execution budget for a
// single script invocation. NFR-P4 targets 100ms p99; the wall budget here
// gives headroom for hot paths. Configurable via PROCESSOR_SCRIPT_WALL_MS.
const DefaultScriptWallBudget = 250 * time.Millisecond

// DefaultScriptMaxSteps is the secondary safeguard against infinite loops
// in Starlark. Set generously — the wall-clock is the primary fence.
const DefaultScriptMaxSteps = 1_000_000

// StarlarkRunner compiles and executes a script against a ScriptContext.
// Construction is cheap; reuse one instance across many operations.
type StarlarkRunner struct {
	WallBudget time.Duration
	MaxSteps   int64
}

// NewStarlarkRunner returns a runner with the default budgets.
func NewStarlarkRunner(wallBudget time.Duration, maxSteps int64) *StarlarkRunner {
	if wallBudget <= 0 {
		wallBudget = DefaultScriptWallBudget
	}
	if maxSteps <= 0 {
		maxSteps = DefaultScriptMaxSteps
	}
	return &StarlarkRunner{WallBudget: wallBudget, MaxSteps: maxSteps}
}

// Run executes the script in sc.ScriptSource with sc as the input. The
// returned ScriptResult is the parsed return value of the script's
// `execute(state, op)` function.
//
// The compile+thread+cancellation harness lives in internal/starlarksandbox
// (the shared verified-pure sandbox leaf); Run builds the Processor-specific
// globals (state/op/ddl/nanoid/crypto/time/json/kv), calls
// starlarksandbox.Execute, and maps its generic *SandboxError onto the
// Processor's own *ScriptError (which additionally carries the
// ClaimKeyInvalid side-channel — see classifyScriptError).
//
// Failure modes mapped to ScriptError:
//   - compile failure                      → Code="ScriptError"
//   - resolve error (undefined name `os`)  → Code="SandboxViolation"
//   - runtime error                        → Code="ScriptError"
//   - context cancelled / wall budget hit  → Code="ScriptTimeout"
//   - return value not Contract #3-shaped  → Code="InvalidReturnShape"
func (r *StarlarkRunner) Run(ctx context.Context, sc ScriptContext) (ScriptResult, error) {
	rid := sc.Operation.RequestID

	stateVal := vertexMapToStarlarkWithHydrated(sc.Hydrated)
	opVal := operationEnvelopeToStarlark(sc.Operation)

	// Build globals.
	globals := starlarklib.StringDict{
		"state":  stateVal,
		"op":     opVal,
		"ddl":    ddlMapToStarlark(sc.DDLLookup),
		"nanoid": nanoidModule(rid),
		// crypto.sha256(s) — pure SHA-256 hash builtin. Deterministic,
		// side-effect-free: safe under sandbox principles.
		"crypto": cryptoModule(),
		// time.rfc3339_utc(s) — parse + normalize an RFC3339 timestamp to
		// canonical UTC whole-second form. Pure: no wall-clock read, output
		// is a function of the input only. Lets ops validate + normalize
		// caller-supplied timestamps so lexical comparisons against the
		// Refractor's `$now` are sound. Does NOT expose the host clock.
		"time": timeModule(),
		// json.decode(s) / json.encode(v) — standard Starlark JSON module.
		// Pure (no I/O, deterministic): safe under sandbox principles.
		// Used by MetaRootDDLScript's meta.lens branch to parse the spec
		// payload field into a structured dict for the .spec aspect data.
		"json": starlarkjson.Module,
		// kv.Read(key) — Contract #2 §2.5 lazy on-demand Core KV read. Unlike the
		// pure modules above this is the ONE builtin that performs (potentially)
		// a NATS round-trip AND is intentionally NON-deterministic: it serves
		// contextHint-prefetched keys from the hydrated cache and otherwise reads
		// LIVE Core KV state. A hard-deleted/absent key reads as None; a
		// logically-deleted key (isDeleted=true) reads as a present doc carrying
		// the flag. The opt-in read seam for the read-before-create idempotency
		// pattern — not a read model (P5). It reads its execution-scoped context
		// via starlarksandbox.ContextFromThread (see starlark_kv.go), so a slow
		// round-trip counts against the same wall budget Execute enforces.
		"kv": kvModule(sc),
	}

	out, sErr := starlarksandbox.Execute(ctx, sc.ScriptSource, "execute", starlarklib.Tuple{stateVal, opVal}, globals, starlarksandbox.Budget{
		Wall:     r.WallBudget,
		MaxSteps: r.MaxSteps,
	})
	if sErr != nil {
		return ScriptResult{}, classifyScriptError(sErr, rid)
	}

	return parseScriptResult(out, rid)
}

// classifyScriptError maps a starlarksandbox.SandboxError onto the
// Processor's own *ScriptError, adding the one Processor-specific
// reclassification the generic leaf does not (and should not) know about:
// ClaimKeyInvalid, a structured error from the ClaimIdentity script branch.
// The script encodes a specific outcome (e.g. "invalid-key", "wrong-state")
// in a fail("ClaimKeyInvalid: <outcome>") message; this parses it into the
// Detail side-channel so the executor can emit the specific outcome to
// Health KV before stripping it from the caller reply (NFR-S6
// anti-enumeration: callers see only Code="ClaimKeyInvalid", no detail).
func classifyScriptError(sErr *starlarksandbox.SandboxError, rid string) *ScriptError {
	msg := sErr.Message
	// Only reclassify a generic ScriptError — matching the original single-
	// file classifier's priority order, where undefined:/load: were checked
	// (and returned) before ClaimKeyInvalid was ever considered. A
	// SandboxViolation/ScriptTimeout/InvalidReturnShape is never
	// reinterpreted as ClaimKeyInvalid even if its message happens to
	// contain that substring.
	if idx := strings.Index(msg, "ClaimKeyInvalid: "); sErr.Code == starlarksandbox.ScriptError && idx >= 0 {
		detail := strings.TrimSpace(msg[idx+len("ClaimKeyInvalid: "):])
		// Strip any trailing ") or similar Starlark backtrace decoration.
		if nl := strings.IndexAny(detail, "\n)"); nl >= 0 {
			detail = strings.TrimSpace(detail[:nl])
		}
		return &ScriptError{
			Code:               "ClaimKeyInvalid",
			Message:            "ClaimKeyInvalid",
			Detail:             detail,
			Line:               sErr.Line,
			Column:             sErr.Column,
			OperationRequestID: rid,
		}
	}
	return &ScriptError{
		Code:               string(sErr.Code),
		Message:            sErr.Message,
		Line:               sErr.Line,
		Column:             sErr.Column,
		OperationRequestID: rid,
	}
}

// parseScriptResult converts the Starlark return value into a ScriptResult.
// The script must return {"mutations": [...], "events": [...]} per
// Contract #3 §3.1. An optional "response" dict carries a CLOSED schema whose
// only permitted key is "primaryKey" (a string). Any other key is a
// fail-closed InvalidReturnShape error: the write path is not a read channel,
// so a script may only point at a key it committed — it cannot return
// arbitrary data. Absent "response" / absent "primaryKey" is allowed (empty).
func parseScriptResult(val starlarklib.Value, rid string) (ScriptResult, error) {
	d, ok := val.(*starlarklib.Dict)
	if !ok {
		return ScriptResult{}, &ScriptError{
			Code:               "InvalidReturnShape",
			Message:            fmt.Sprintf("script must return a dict, got %s", val.Type()),
			OperationRequestID: rid,
		}
	}
	muts, err := parseMutations(d, rid)
	if err != nil {
		return ScriptResult{}, err
	}
	evs, err := parseEvents(d, rid)
	if err != nil {
		return ScriptResult{}, err
	}
	primaryKey, err := parseResponse(d, rid)
	if err != nil {
		return ScriptResult{}, err
	}
	return ScriptResult{Mutations: muts, Events: evs, PrimaryKey: primaryKey}, nil
}

// parseResponse parses the optional, closed "response" dict. The only
// permitted key is "primaryKey" (string); any other key fails closed. Absent
// "response" or absent "primaryKey" yields an empty string.
func parseResponse(d *starlarklib.Dict, rid string) (string, error) {
	respRaw, found, _ := d.Get(starlarklib.String("response"))
	if !found {
		return "", nil
	}
	respDict, ok := respRaw.(*starlarklib.Dict)
	if !ok {
		return "", &ScriptError{
			Code:               "InvalidReturnShape",
			Message:            fmt.Sprintf("'response' must be a dict, got %s", respRaw.Type()),
			OperationRequestID: rid,
		}
	}
	for _, item := range respDict.Items() {
		k, ok := item[0].(starlarklib.String)
		if !ok || string(k) != "primaryKey" {
			return "", &ScriptError{
				Code:               "InvalidReturnShape",
				Message:            fmt.Sprintf("'response' permits only the 'primaryKey' key, got %q", item[0].String()),
				OperationRequestID: rid,
			}
		}
	}
	raw, found, _ := respDict.Get(starlarklib.String("primaryKey"))
	if !found {
		return "", nil
	}
	s, ok := raw.(starlarklib.String)
	if !ok {
		return "", &ScriptError{
			Code:               "InvalidReturnShape",
			Message:            fmt.Sprintf("'response.primaryKey' must be a string, got %s", raw.Type()),
			OperationRequestID: rid,
		}
	}
	return string(s), nil
}

func parseMutations(d *starlarklib.Dict, rid string) ([]MutationOp, error) {
	raw, found, _ := d.Get(starlarklib.String("mutations"))
	if !found {
		return nil, nil
	}
	list, ok := raw.(*starlarklib.List)
	if !ok {
		return nil, &ScriptError{Code: "InvalidReturnShape",
			Message: "'mutations' must be a list", OperationRequestID: rid}
	}
	out := make([]MutationOp, 0, list.Len())
	for i := 0; i < list.Len(); i++ {
		md, ok := list.Index(i).(*starlarklib.Dict)
		if !ok {
			return nil, &ScriptError{Code: "InvalidReturnShape",
				Message: fmt.Sprintf("mutations[%d] must be a dict", i), OperationRequestID: rid}
		}
		op, err := dictString(md, "op")
		if err != nil {
			return nil, &ScriptError{Code: "InvalidReturnShape",
				Message: fmt.Sprintf("mutations[%d]: %s", i, err.Error()), OperationRequestID: rid}
		}
		if op != "create" && op != "update" && op != "tombstone" {
			return nil, &ScriptError{Code: "InvalidReturnShape",
				Message:            fmt.Sprintf("mutations[%d].op must be create|update|tombstone, got %q", i, op),
				OperationRequestID: rid}
		}
		key, err := dictString(md, "key")
		if err != nil {
			return nil, &ScriptError{Code: "InvalidReturnShape",
				Message: fmt.Sprintf("mutations[%d]: %s", i, err.Error()), OperationRequestID: rid}
		}
		m := MutationOp{Op: op, Key: key}
		if op == "create" || op == "update" {
			docRaw, hasDoc, _ := md.Get(starlarklib.String("document"))
			if hasDoc {
				dd, ok := docRaw.(*starlarklib.Dict)
				if !ok {
					return nil, &ScriptError{Code: "InvalidReturnShape",
						Message:            fmt.Sprintf("mutations[%d].document must be a dict", i),
						OperationRequestID: rid}
				}
				m.Document = starlarkDictToGoMap(dd)
			}
		}
		// Extract optional expectedRevision integer so step8_commit.go can
		// propagate the revision assertion to AtomicBatch.
		if rev, found, _ := md.Get(starlarklib.String("expectedRevision")); found && rev != starlarklib.None {
			if revInt, ok := rev.(starlarklib.Int); ok {
				if v, ok := revInt.Uint64(); ok {
					m.ExpectedRevision = &v
				}
			}
		}
		out = append(out, m)
	}
	return out, nil
}

func parseEvents(d *starlarklib.Dict, rid string) ([]EventSpec, error) {
	raw, found, _ := d.Get(starlarklib.String("events"))
	if !found {
		return nil, nil
	}
	list, ok := raw.(*starlarklib.List)
	if !ok {
		return nil, &ScriptError{Code: "InvalidReturnShape",
			Message: "'events' must be a list", OperationRequestID: rid}
	}
	out := make([]EventSpec, 0, list.Len())
	for i := 0; i < list.Len(); i++ {
		ed, ok := list.Index(i).(*starlarklib.Dict)
		if !ok {
			return nil, &ScriptError{Code: "InvalidReturnShape",
				Message: fmt.Sprintf("events[%d] must be a dict", i), OperationRequestID: rid}
		}
		class, err := dictString(ed, "class")
		if err != nil {
			return nil, &ScriptError{Code: "InvalidReturnShape",
				Message: fmt.Sprintf("events[%d]: %s", i, err.Error()), OperationRequestID: rid}
		}
		ev := EventSpec{Class: class, Data: map[string]interface{}{}}
		dataRaw, hasData, _ := ed.Get(starlarklib.String("data"))
		if hasData {
			if dd, ok := dataRaw.(*starlarklib.Dict); ok {
				ev.Data = starlarkDictToGoMap(dd)
			}
		}
		out = append(out, ev)
	}
	return out, nil
}

// ---- Starlark value conversion ----
//
// The pure Go<->Starlark converters (goValueToStarlark / goMapToStarlarkDict /
// starlarkDictToGoMap) live in internal/starlarksandbox (GoValueToStarlark /
// GoMapToStarlarkDict / StarlarkDictToGoMap); these are thin unexported
// aliases kept so the rest of this file's call sites are unchanged. Their
// pinning tests (incl. the Starlark->Go direction, unused as an unexported
// alias here) live at internal/starlarksandbox/convert_test.go.

func goValueToStarlark(v interface{}) starlarklib.Value {
	return starlarksandbox.GoValueToStarlark(v)
}

func goMapToStarlarkDict(m map[string]interface{}) *starlarklib.Dict {
	return starlarksandbox.GoMapToStarlarkDict(m)
}

func starlarkDictToGoMap(d *starlarklib.Dict) map[string]interface{} {
	return starlarksandbox.StarlarkDictToGoMap(d)
}

// stateMapValue is the Starlark `state` global exposed to scripts.
//
// It wraps a *starlarklib.Dict (the hydrated vertex/aspect map). The wrapper
// passes all dict operations (subscript, `in`, `.get()`, etc.) through to
// the underlying dict so existing scripts remain unaffected.
//
// Interface compliance:
//
//	Mapping   — via Get (supports `state[key]` and `key in state`)
//	Iterable  — via Iterate (supports `for k in state`)
type stateMapValue struct {
	d *starlarklib.Dict
}

func (s *stateMapValue) String() string          { return s.d.String() }
func (s *stateMapValue) Type() string            { return "state" }
func (s *stateMapValue) Freeze()                 { s.d.Freeze() }
func (s *stateMapValue) Truth() starlarklib.Bool { return s.d.Truth() }
func (s *stateMapValue) Hash() (uint32, error)   { return 0, fmt.Errorf("state is not hashable") }

// Get implements starlarklib.Mapping — supports `state[key]` and `key in state`.
func (s *stateMapValue) Get(k starlarklib.Value) (v starlarklib.Value, found bool, err error) {
	return s.d.Get(k)
}

// Iterate implements starlarklib.Iterable — supports `for k in state`.
func (s *stateMapValue) Iterate() starlarklib.Iterator {
	return s.d.Iterate()
}

// AttrNames implements starlarklib.HasAttrs — delegates to the underlying
// dict so `state.get`, `state.keys`, etc. continue to work.
func (s *stateMapValue) AttrNames() []string {
	return s.d.AttrNames()
}

// Attr implements starlarklib.HasAttrs — delegates to the underlying dict.
// All dict-native attrs flow through (no custom attributes are added).
func (s *stateMapValue) Attr(name string) (starlarklib.Value, error) {
	return s.d.Attr(name)
}

// vertexMapToStarlarkWithHydrated builds the `state` global for a script.
// Returns a *stateMapValue wrapping the key→VertexDoc dict.
func vertexMapToStarlarkWithHydrated(m map[string]VertexDoc) *stateMapValue {
	d := new(starlarklib.Dict)
	for k, v := range m {
		_ = d.SetKey(starlarklib.String(k), vertexDocToStarlark(v))
	}
	return &stateMapValue{d: d}
}

// vertexDocToStarlark projects a single VertexDoc into the Starlark struct a
// script reads — the shared shape behind both a `state[key]` entry and a
// `kv.Read(key)` result, so a script consumes either identically (.data.<f>,
// .class, .isDeleted, .revision, and the aspect-only .vertexKey/.localName when
// set).
func vertexDocToStarlark(v VertexDoc) starlarklib.Value {
	fields := starlarklib.StringDict{
		"key":       starlarklib.String(v.Key),
		"class":     starlarklib.String(v.Class),
		"isDeleted": starlarklib.Bool(v.IsDeleted),
		"data":      goMapToStarlarkDict(v.Data),
		"revision":  starlarklib.MakeUint64(v.Revision),
	}
	if v.VertexKey != "" {
		fields["vertexKey"] = starlarklib.String(v.VertexKey)
	}
	if v.LocalName != "" {
		fields["localName"] = starlarklib.String(v.LocalName)
	}
	return starlarkstruct.FromStringDict(starlarkstruct.Default, fields)
}

func operationEnvelopeToStarlark(op *OperationEnvelope) *starlarkstruct.Struct {
	payloadFields := starlarklib.StringDict{}
	if len(op.Payload) > 0 {
		// op.Payload is a json.RawMessage — parse lazily into a generic
		// map for Starlark exposure.
		if m, ok := jsonToGenericMap(op.Payload); ok {
			for k, v := range m {
				payloadFields[k] = goValueToStarlark(v)
			}
		}
	}
	authContextTarget := ""
	authContextService := ""
	if op.AuthContext != nil {
		authContextTarget = op.AuthContext.Target
		authContextService = op.AuthContext.Service
	}
	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlarklib.StringDict{
		"requestId":          starlarklib.String(op.RequestID),
		"lane":               starlarklib.String(string(op.Lane)),
		"operationType":      starlarklib.String(op.OperationType),
		"actor":              starlarklib.String(op.Actor),
		"submittedAt":        starlarklib.String(op.SubmittedAt),
		"payload":            starlarkstruct.FromStringDict(starlarkstruct.Default, payloadFields),
		"authContextTarget":  starlarklib.String(authContextTarget),
		"authContextService": starlarklib.String(authContextService),
	})
}

func ddlMapToStarlark(m map[string]MetaVertex) *starlarklib.Dict {
	d := new(starlarklib.Dict)
	for k, v := range m {
		perm := starlarklib.NewList(nil)
		for _, c := range v.PermittedCommands {
			_ = perm.Append(starlarklib.String(c))
		}
		_ = d.SetKey(starlarklib.String(k), starlarkstruct.FromStringDict(starlarkstruct.Default, starlarklib.StringDict{
			"canonicalName": starlarklib.String(v.CanonicalName),
			// metaKey is the DDL's meta-vertex key (vtx.meta.<NanoID>). A script
			// uses it to write an instanceOf link to its own type authority
			// (lnk.<root>.instanceOf.meta.<id>) so the step-6 write-gate resolver
			// reaches this DDL for a fine-grained-class vertex (Contract #1 §1.5
			// instanceOf terminal — the producer half of the instanceOf type model,
			// Contract #2 §2.1). The script context already carries this key; this
			// only surfaces it to Starlark.
			"metaKey":           starlarklib.String(v.Key),
			"permittedCommands": perm,
		}))
	}
	return d
}

func dictString(d *starlarklib.Dict, key string) (string, error) {
	val, found, err := d.Get(starlarklib.String(key))
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("missing required field %q", key)
	}
	s, ok := val.(starlarklib.String)
	if !ok {
		return "", fmt.Errorf("field %q must be string, got %s", key, val.Type())
	}
	return strings.TrimSpace(string(s)), nil
}
