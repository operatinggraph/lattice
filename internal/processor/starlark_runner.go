package processor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	starlarklib "go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
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
// Failure modes mapped to ScriptError:
//   - compile failure                      → Code="ScriptError"
//   - resolve error (undefined name `os`)  → Code="SandboxViolation"
//   - runtime error                        → Code="ScriptError"
//   - context cancelled / wall budget hit  → Code="ScriptTimeout"
//   - return value not Contract #3-shaped  → Code="InvalidReturnShape"
func (r *StarlarkRunner) Run(ctx context.Context, sc ScriptContext) (ScriptResult, error) {
	rid := sc.Operation.RequestID

	// Build globals.
	globals := starlarklib.StringDict{
		"state":  vertexMapToStarlarkWithHydrated(sc.Hydrated),
		"op":     operationEnvelopeToStarlark(sc.Operation),
		"ddl":    ddlMapToStarlark(sc.DDLLookup),
		"nanoid": nanoidModule(rid),
		// crypto.sha256(s) — pure SHA-256 hash builtin (Story 4.2).
		// Deterministic, side-effect-free: safe under sandbox principles.
		"crypto": cryptoModule(),
		// strings.levenshtein + strings.levenshtein_ratio — pure string-math
		// builtins (Story 4.4). Deterministic, side-effect-free.
		"strings": stringsModule(),
	}

	// Compile. Resolve errors (referencing `os`, `time`, etc. without binding)
	// fire here because go.starlark.net resolves names at compile time when
	// `globals.Has` is supplied as the predeclared probe.
	//nolint:staticcheck // SA1019: SourceProgramOptions migration deferred; current API verified safe in Story 1.6
	_, prog, err := starlarklib.SourceProgram("<script>", sc.ScriptSource, globals.Has)
	if err != nil {
		return ScriptResult{}, classifyStarlarkError(err, rid)
	}

	// Per-call thread with cancellation wired to ctx + wall budget.
	wallCtx, cancel := context.WithTimeout(ctx, r.WallBudget)
	defer cancel()

	thread := &starlarklib.Thread{
		Name: "processor:" + rid,
		// Load is intentionally nil — `load(...)` calls fail.
	}
	thread.SetMaxExecutionSteps(uint64(r.MaxSteps))

	// Cancel the Starlark thread when ctx fires.
	cancelCh := make(chan struct{})
	defer close(cancelCh)
	go func() {
		select {
		case <-wallCtx.Done():
			thread.Cancel(wallCtx.Err().Error())
		case <-cancelCh:
		}
	}()

	// Define globals (compiles and runs top-level statements like `def execute`).
	defined, err := prog.Init(thread, globals)
	if err != nil {
		return ScriptResult{}, classifyStarlarkError(err, rid)
	}

	executeFn, ok := defined["execute"]
	if !ok {
		return ScriptResult{}, &ScriptError{
			Code:               "InvalidReturnShape",
			Message:            "script must define an `execute(state, op)` function",
			OperationRequestID: rid,
		}
	}

	out, err := starlarklib.Call(thread, executeFn, starlarklib.Tuple{
		globals["state"], globals["op"],
	}, nil)
	if err != nil {
		// If the wall budget fired, prefer the timeout classification.
		if wallCtx.Err() != nil && errors.Is(wallCtx.Err(), context.DeadlineExceeded) {
			return ScriptResult{}, &ScriptError{
				Code:               "ScriptTimeout",
				Message:            fmt.Sprintf("script exceeded wall budget %s", r.WallBudget),
				OperationRequestID: rid,
			}
		}
		return ScriptResult{}, classifyStarlarkError(err, rid)
	}

	return parseScriptResult(out, rid)
}

// classifyStarlarkError maps go.starlark.net error types onto our typed
// ScriptError. The key signal for "sandbox violation" is starlark's
// resolve.ErrorList (compile-time) or an EvalError whose backtrace shows
// an undefined name — we treat unbound globals as SandboxViolation
// because the only way a script references an undefined global is by
// trying to use a forbidden module (os, time, http, ...).
func classifyStarlarkError(err error, rid string) *ScriptError {
	msg := err.Error()

	// Resolve errors arrive as resolve.ErrorList — go.starlark.net's
	// SourceProgram wraps them. The string contains "undefined:" for the
	// classic sandbox violation case.
	if strings.Contains(msg, "undefined:") {
		line, col := extractStarlarkPosition(err)
		return &ScriptError{
			Code:               "SandboxViolation",
			Message:            msg,
			Line:               line,
			Column:             col,
			OperationRequestID: rid,
		}
	}
	// `load` calls fail with "cannot load <module>: load not implemented".
	if strings.Contains(msg, "load not implemented") || strings.Contains(msg, "load:") {
		line, col := extractStarlarkPosition(err)
		return &ScriptError{
			Code:               "SandboxViolation",
			Message:            msg,
			Line:               line,
			Column:             col,
			OperationRequestID: rid,
		}
	}
	// ClaimKeyInvalid: structured error from the ClaimIdentity script branch
	// (Story 4.3). The script calls fail("ClaimKeyInvalid: <outcome>") where
	// outcome is the internal diagnostic detail. We parse it here so the
	// executor can emit the specific outcome to Health KV before stripping it
	// from the caller reply (NFR-S6 anti-enumeration).
	if idx := strings.Index(msg, "ClaimKeyInvalid: "); idx >= 0 {
		detail := strings.TrimSpace(msg[idx+len("ClaimKeyInvalid: "):])
		// Strip any trailing ") or similar Starlark backtrace decoration.
		if nl := strings.IndexAny(detail, "\n)"); nl >= 0 {
			detail = strings.TrimSpace(detail[:nl])
		}
		line, col := extractStarlarkPosition(err)
		return &ScriptError{
			Code:               "ClaimKeyInvalid",
			Message:            "ClaimKeyInvalid",
			Detail:             detail,
			Line:               line,
			Column:             col,
			OperationRequestID: rid,
		}
	}
	// Anything else — syntax error, runtime fail() call, division by zero, etc.
	line, col := extractStarlarkPosition(err)
	return &ScriptError{
		Code:               "ScriptError",
		Message:            msg,
		Line:               line,
		Column:             col,
		OperationRequestID: rid,
	}
}

// extractStarlarkPosition tries to pull line/column from a starlark
// EvalError or SyntaxError. Returns (0,0) if not available.
func extractStarlarkPosition(err error) (int, int) {
	type positioner interface{ Position() (string, int, int) }
	var p positioner
	if errors.As(err, &p) {
		_, line, col := p.Position()
		return line, col
	}
	var evalErr *starlarklib.EvalError
	if errors.As(err, &evalErr) && len(evalErr.CallStack) > 0 {
		pos := evalErr.CallStack[len(evalErr.CallStack)-1].Pos
		return int(pos.Line), int(pos.Col)
	}
	return 0, 0
}

// parseScriptResult converts the Starlark return value into a ScriptResult.
// The script must return {"mutations": [...], "events": [...]} per
// Contract #3 §3.1. An optional "response" dict key carries structured
// data to surface in the success reply (Story 4.2 extension).
//
// NOTE: ResponseDetail is NOT logged by the executor (NFR-S6/S7 —
// it may carry sensitive tokens such as plaintext claim keys).
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
	// Optional "response" dict — Story 4.2 extension. Never validate strictly;
	// absent = nil; non-dict = silently ignored (don't break existing scripts).
	var detail map[string]any
	if respRaw, found, _ := d.Get(starlarklib.String("response")); found {
		if respDict, ok := respRaw.(*starlarklib.Dict); ok {
			detail = starlarkDictToGoMap(respDict)
		}
	}
	return ScriptResult{Mutations: muts, Events: evs, ResponseDetail: detail}, nil
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
				Message: fmt.Sprintf("mutations[%d].op must be create|update|tombstone, got %q", i, op),
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
						Message: fmt.Sprintf("mutations[%d].document must be a dict", i),
						OperationRequestID: rid}
				}
				m.Document = starlarkDictToGoMap(dd)
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

// stateMapValue is the Starlark `state` global exposed to scripts.
//
// It wraps a *starlarklib.Dict (the hydrated vertex/aspect map) and adds a
// `keys_with_prefix(prefix)` method. The wrapper passes all dict operations
// (subscript, `in`, `.get()`, etc.) through to the underlying dict so
// existing scripts remain unaffected. Story 4.4 adds `keys_with_prefix` to
// support the ScanIdentityDuplicates enumeration path.
//
// Interface compliance:
//
//	Mapping   — via Get (supports `state[key]` and `key in state`)
//	Iterable  — via Iterate (supports `for k in state`)
//	HasAttrs  — via Attr (supports `state.keys_with_prefix(...)`)
type stateMapValue struct {
	d    *starlarklib.Dict
	keys []string // ordered snapshot of keys for keys_with_prefix
}

func (s *stateMapValue) String() string        { return s.d.String() }
func (s *stateMapValue) Type() string          { return "state" }
func (s *stateMapValue) Freeze()               { s.d.Freeze() }
func (s *stateMapValue) Truth() starlarklib.Bool { return s.d.Truth() }
func (s *stateMapValue) Hash() (uint32, error) { return 0, fmt.Errorf("state is not hashable") }

// Get implements starlarklib.Mapping — supports `state[key]` and `key in state`.
func (s *stateMapValue) Get(k starlarklib.Value) (v starlarklib.Value, found bool, err error) {
	return s.d.Get(k)
}

// Iterate implements starlarklib.Iterable — supports `for k in state`.
func (s *stateMapValue) Iterate() starlarklib.Iterator {
	return s.d.Iterate()
}

// AttrNames implements starlarklib.HasAttrs.
func (s *stateMapValue) AttrNames() []string {
	return []string{"keys_with_prefix"}
}

// Attr implements starlarklib.HasAttrs — exposes `state.keys_with_prefix`.
func (s *stateMapValue) Attr(name string) (starlarklib.Value, error) {
	if name == "keys_with_prefix" {
		return starlarklib.NewBuiltin("keys_with_prefix", func(_ *starlarklib.Thread, _ *starlarklib.Builtin, args starlarklib.Tuple, kwargs []starlarklib.Tuple) (starlarklib.Value, error) {
			if len(args) != 1 || len(kwargs) != 0 {
				return nil, errBuiltin("state.keys_with_prefix(prefix) takes exactly 1 positional argument")
			}
			prefix, ok := args[0].(starlarklib.String)
			if !ok {
				return nil, errBuiltin("state.keys_with_prefix: prefix must be a string, got " + args[0].Type())
			}
			p := string(prefix)
			result := starlarklib.NewList(nil)
			for _, k := range s.keys {
				if strings.HasPrefix(k, p) {
					_ = result.Append(starlarklib.String(k))
				}
			}
			return result, nil
		}), nil
	}
	// Delegate other attribute accesses (like .get, .keys, etc.) to the dict.
	return s.d.Attr(name)
}

// vertexMapToStarlarkWithHydrated builds the `state` global for a script.
// Returns a *stateMapValue wrapping the key→VertexDoc dict. The wrapper
// exposes keys_with_prefix in addition to all standard dict operations.
func vertexMapToStarlarkWithHydrated(m map[string]VertexDoc) *stateMapValue {
	d := new(starlarklib.Dict)
	keys := make([]string, 0, len(m))
	for k, v := range m {
		fields := starlarklib.StringDict{
			"key":       starlarklib.String(v.Key),
			"class":     starlarklib.String(v.Class),
			"isDeleted": starlarklib.Bool(v.IsDeleted),
			"data":      goMapToStarlarkDict(v.Data),
		}
		if v.VertexKey != "" {
			fields["vertexKey"] = starlarklib.String(v.VertexKey)
		}
		if v.LocalName != "" {
			fields["localName"] = starlarklib.String(v.LocalName)
		}
		_ = d.SetKey(starlarklib.String(k), starlarkstruct.FromStringDict(starlarkstruct.Default, fields))
		keys = append(keys, k)
	}
	return &stateMapValue{d: d, keys: keys}
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
	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlarklib.StringDict{
		"requestId":     starlarklib.String(op.RequestID),
		"lane":          starlarklib.String(string(op.Lane)),
		"operationType": starlarklib.String(op.OperationType),
		"actor":         starlarklib.String(op.Actor),
		"submittedAt":   starlarklib.String(op.SubmittedAt),
		"payload":       starlarkstruct.FromStringDict(starlarkstruct.Default, payloadFields),
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
			"canonicalName":     starlarklib.String(v.CanonicalName),
			"permittedCommands": perm,
		}))
	}
	return d
}

func goMapToStarlarkDict(m map[string]interface{}) *starlarklib.Dict {
	d := new(starlarklib.Dict)
	for k, v := range m {
		_ = d.SetKey(starlarklib.String(k), goValueToStarlark(v))
	}
	return d
}

func goValueToStarlark(v interface{}) starlarklib.Value {
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
		// Try to preserve int-typed JSON numbers (Go decodes all JSON
		// numbers as float64).
		if x == float64(int64(x)) {
			return starlarklib.MakeInt64(int64(x))
		}
		return starlarklib.Float(x)
	case map[string]interface{}:
		return goMapToStarlarkDict(x)
	case []interface{}:
		l := starlarklib.NewList(nil)
		for _, item := range x {
			_ = l.Append(goValueToStarlark(item))
		}
		return l
	default:
		return starlarklib.String(fmt.Sprintf("%v", x))
	}
}

func starlarkValueToGo(v starlarklib.Value) interface{} {
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
		return starlarkDictToGoMap(x)
	case *starlarklib.List:
		out := make([]interface{}, x.Len())
		for i := 0; i < x.Len(); i++ {
			out[i] = starlarkValueToGo(x.Index(i))
		}
		return out
	default:
		return x.String()
	}
}

func starlarkDictToGoMap(d *starlarklib.Dict) map[string]interface{} {
	out := make(map[string]interface{}, d.Len())
	for _, item := range d.Items() {
		k, ok := item[0].(starlarklib.String)
		if !ok {
			continue
		}
		out[string(k)] = starlarkValueToGo(item[1])
	}
	return out
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
