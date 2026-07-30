package processor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	starlarkjson "go.starlark.net/lib/json"
	starlarklib "go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/operatinggraph/lattice/internal/starlarksandbox"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// defaultScriptWallBudgetMs is NFR-P4's production budget: it targets 100ms
// p99, and this gives headroom for hot paths.
const defaultScriptWallBudgetMs = 250

// DefaultScriptWallBudget is the default wall-clock execution budget for a
// single script invocation, read once at process start from
// PROCESSOR_SCRIPT_WALL_MS (falling back to the NFR-P4 production budget
// when unset or invalid). Widening it is for test/CI harnesses that
// validate a whole package (100+ DDL mutations) in one script call under
// noisy parallel load — it does not change the production default.
var DefaultScriptWallBudget = defaultScriptWallBudgetFromEnv()

func defaultScriptWallBudgetFromEnv() time.Duration {
	if raw := os.Getenv("PROCESSOR_SCRIPT_WALL_MS"); raw != "" {
		if ms, err := strconv.Atoi(raw); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return defaultScriptWallBudgetMs * time.Millisecond
}

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

	// Own the tracker rather than trusting the caller to have wired one: a nil
	// tracker would silently downgrade a deferred HydrationMiss to whatever
	// error carried it out of Starlark, which is the fail-open direction.
	if sc.DeferredMiss == nil {
		sc.DeferredMiss = &deferredMissTracker{}
	}
	// Same posture for the live-read budget: an un-wired tracker must default
	// to a bounded budget, not to liveReadBudgetTracker's nil-receiver
	// "unlimited" behavior (that's the test-harness convenience, not the
	// production default).
	if sc.LiveReads == nil {
		sc.LiveReads = &liveReadBudgetTracker{budget: DefaultLiveReadBudget}
	}

	stateVal := vertexMapToStarlarkWithHydrated(sc)
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
	// A required-absent declared read the script touched outranks whatever
	// Starlark error carried it out: the caller must see the HydrationMiss step
	// 4 deferred, not a ScriptError naming an internal abort message. Checked on
	// the success path too — if a mapping ever swallowed the abort, the
	// operation still fails closed rather than committing having skipped a read
	// it declared its correctness depends on.
	if key := sc.DeferredMiss.missed(); key != "" {
		return ScriptResult{}, deferredHydrationMiss(key, rid)
	}
	if sErr != nil {
		return ScriptResult{}, classifyScriptError(sErr, rid)
	}

	result, err := parseScriptResult(out, rid)
	if err != nil {
		return ScriptResult{}, err
	}
	// The write side of the same dependence test. applyHydratedRevisions
	// defaults an update/tombstone's expectedRevision from the step-4 snapshot
	// and skips keys it never hydrated, leaving them UNCONDITIONED — so a
	// mutation naming a required-absent key must fault here, or deferring the
	// read would turn a rejection into an unconditioned write to a key that
	// does not exist.
	if key := firstRequiredAbsentMutation(result.Mutations, sc.RequiredAbsent); key != "" {
		sc.DeferredMiss.fault(key)
		return ScriptResult{}, deferredHydrationMiss(key, rid)
	}
	return result, nil
}

// deferredHydrationMiss builds the HydrationMiss for a declared read step 4
// recorded as absent. It carries the same code and MissingKey a hydration-time
// fault would, so an operation that depends on the key is rejected with the
// full diagnostic.
func deferredHydrationMiss(key, rid string) *HydrationError {
	return &HydrationError{Code: "HydrationMiss", MissingKey: key, OperationRequestID: rid}
}

// firstRequiredAbsentMutation returns the first required-absent key a mutation
// depends on, or "" when none does.
//
// A mutation depends on a required-absent key when it names it directly, and
// also when it writes something the key's vertex must exist for: an ASPECT of a
// required-absent vertex, or a LINK with a required-absent endpoint. Those two
// carry their subject's vertex key inside their own key (Contract #1 §1.1), and
// matching only the exact key would let an op write an aspect onto — or a link
// into — a vertex it declared it would read and that is not there. Step 6
// resolves an aspect's governing DDL through its parent vertex and falls back to
// the permissive default when that vertex resolves to nothing, so nothing
// downstream would catch it.
func firstRequiredAbsentMutation(mutations []MutationOp, requiredAbsent map[string]struct{}) string {
	if len(requiredAbsent) == 0 {
		return ""
	}
	required := func(key string) bool {
		_, ok := requiredAbsent[key]
		return ok
	}
	for i := range mutations {
		key := mutations[i].Key
		if required(key) {
			return key
		}
		switch substrate.ClassifyKey(key) {
		case substrate.KindAspect:
			if vk, _, _, _, ok := substrate.ParseAspectKey(key); ok && required(vk) {
				return vk
			}
		case substrate.KindLink:
			if t1, id1, _, t2, id2, ok := substrate.ParseLinkKey(key); ok {
				source := substrate.VertexPrefix + "." + t1 + "." + id1
				if required(source) {
					return source
				}
				target := substrate.VertexPrefix + "." + t2 + "." + id2
				if required(target) {
					return target
				}
			}
		}
	}
	return ""
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
		} else if _, hasDoc, _ := md.Get(starlarklib.String("document")); hasDoc {
			// A tombstone carries no document (Contract #3 §3.3) — one supplied
			// is not honored. Warn today; Fire 2 flips this to a reject once
			// warn sightings are clean (tombstone-body-preservation-design.md §5).
			slog.Default().Warn("tombstone mutation carries an unhonored document",
				"requestId", rid, "mutationIndex", i, "key", key)
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
//
// requiredAbsent/deferredMiss carry the fail-closed declared reads that were
// absent at the step-4 snapshot. A lookup of one is the operation depending on
// the key, so it raises the HydrationMiss step 4 deferred instead of reporting
// "not found" — `state[K]`, `K in state` and `state.get(K)` all route through
// Get, so all three fail closed identically.
//
// sensitiveReads is the step-6 external-egress guard's consumption tracker: the
// dict's values are readable sensitive data for a sensitive class, so the paths
// that hand a value to the script record the consumption (design
// sensitive-read-tracker-consumption §2).
//
// Do NOT give this type an `Items()` method. Implementing starlark's
// IterableMapping is the natural way to make `dict(state)` work, and it would
// silently turn `json.encode(state)` into a full dump of every hydrated document
// with no consume call at all: json's encoder tries IterableMapping FIRST
// (lib/json), and only falls through to Iterable — which yields key names alone —
// because that method is absent. A value-yielding surface has to record a
// whole-set consumption, like String/items/values do.
type stateMapValue struct {
	d              *starlarklib.Dict
	requiredAbsent map[string]struct{}
	deferredMiss   *deferredMissTracker
	sensitiveReads *sensitiveReadTracker
}

// String implements starlarklib.Value. A dict renders its VALUES, so this is a
// whole-set exposure exactly like items()/values(): `str(state)`, `repr(state)`,
// `"%s" % state`, `"{}".format(state)` and string concatenation all land here,
// and each hands the script every hydrated document's data — decrypted
// plaintext included — as a string it can put straight into an event. So it
// records a whole-set consumption (design
// sensitive-read-tracker-consumption §2.1), on the same footing as the
// value-yielding enumerators.
//
// That makes String side-effecting. No Go-side caller renders the state value
// today, and a spurious flip is the fail-closed direction — but don't reach for
// it in a log line, or every external-egress op that got logged would reject.
func (s *stateMapValue) String() string {
	s.sensitiveReads.consumeAll()
	return s.d.String()
}

func (s *stateMapValue) Type() string { return "state" }
func (s *stateMapValue) Freeze()                 { s.d.Freeze() }
func (s *stateMapValue) Truth() starlarklib.Bool { return s.d.Truth() }
func (s *stateMapValue) Hash() (uint32, error)   { return 0, fmt.Errorf("state is not hashable") }

// Get implements starlarklib.Mapping — supports `state[key]` and `key in state`.
func (s *stateMapValue) Get(k starlarklib.Value) (v starlarklib.Value, found bool, err error) {
	if ks, ok := k.(starlarklib.String); ok {
		if _, required := s.requiredAbsent[string(ks)]; required {
			s.deferredMiss.fault(string(ks))
			return nil, false, fmt.Errorf("state: declared read is absent: %s", string(ks))
		}
		// Naming a key is the script consuming that document. `in` routes through
		// Get too and is indistinguishable here, so membership counts as
		// consumption — conservative, and never weaker than flipping at hydration.
		s.sensitiveReads.consume(string(ks))
	}
	return s.d.Get(k)
}

// Iterate implements starlarklib.Iterable — supports `for k in state`.
//
// Enumeration names no key, so it is not a dependence on any particular one and
// does not fault: a required-absent key is simply not in the set, exactly as an
// undeclared key would not be. Faulting here on the mere existence of a
// required-absent key would not be a dependence test at all — it would reject
// on "some declared read was absent", which is the caller-visible answer to
// "does that key exist?" that deferring the miss exists to withhold.
//
// A prefix scan that finds nothing is therefore the script's own to handle, on
// the same footing as any guard: `find_assigned_link` in orchestration-base
// returns None and its caller fails closed (`UnknownAssignedLink`).
func (s *stateMapValue) Iterate() starlarklib.Iterator {
	return s.d.Iterate()
}

// stateAttrs is the allowlisted attribute surface of the `state` mapping: the
// four read accessors, each of which this wrapper has reasoned about. It is
// deliberately NOT the underlying dict's method set.
//
// A dict also carries `pop`, `popitem`, `setdefault`, `clear` and `update`.
// Three of those RETURN a stored value — `state.pop(K)` and
// `state.setdefault(K, d)` hand back the document under K, `state.popitem()`
// hands back an arbitrary pair — so delegating them would bypass both hooks Get
// carries: the required-absent fault (a declared-absent key would answer the
// default instead of `HydrationMiss`, softening a read the operation declared it
// depends on) and the sensitive-plaintext consumption record. Freezing the dict
// does not close them: `setdefault` on a key that already EXISTS never inserts,
// so it returns the value without tripping the frozen check.
//
// Default-deny rather than an enumerated blocklist: a future go.starlark.net
// that adds another value-returning dict method fails closed here instead of
// silently opening the same hole. Mutating the snapshot is meaningless anyway —
// the commit path reads ScriptContext.Hydrated, never this dict.
var stateAttrs = []string{"get", "items", "keys", "values"}

// AttrNames implements starlarklib.HasAttrs.
func (s *stateMapValue) AttrNames() []string {
	return stateAttrs
}

// Attr implements starlarklib.HasAttrs — delegates to the underlying dict for
// every attr except `get`, re-bound so it agrees with Get, and the two
// value-yielding enumerators, wrapped so they record consumption.
//
// A dict's own bound `get` closes over the dict, not over this wrapper, so
// delegating it would let `state.get(K)` answer None for a required-absent key:
// that call NAMES K, so it is a dependence on K and must fail closed like
// `state[K]`.
//
// `items`/`values` name nothing, so they do not fault on a required-absent key
// (Iterate's reasoning) — but they DO hand the script every hydrated document,
// already-decrypted plaintext included, so they record a whole-set consumption
// for step 6's external-egress guard (design
// sensitive-read-tracker-consumption §2.1). `keys` yields key names only and
// delegates untouched, on both counts.
func (s *stateMapValue) Attr(name string) (starlarklib.Value, error) {
	switch name {
	case "get":
		return starlarklib.NewBuiltin("get", func(_ *starlarklib.Thread, _ *starlarklib.Builtin, args starlarklib.Tuple, kwargs []starlarklib.Tuple) (starlarklib.Value, error) {
			var key, fallback starlarklib.Value = nil, starlarklib.None
			if err := starlarklib.UnpackPositionalArgs("get", args, kwargs, 1, &key, &fallback); err != nil {
				return nil, err
			}
			v, found, err := s.Get(key)
			if err != nil {
				return nil, err
			}
			if !found {
				return fallback, nil
			}
			return v, nil
		}), nil
	case "items", "values":
		delegate, err := s.d.Attr(name)
		if err != nil {
			return nil, err
		}
		fn, ok := delegate.(*starlarklib.Builtin)
		if !ok {
			return nil, fmt.Errorf("state.%s is not callable", name)
		}
		return starlarklib.NewBuiltin(name, func(thread *starlarklib.Thread, _ *starlarklib.Builtin, args starlarklib.Tuple, kwargs []starlarklib.Tuple) (starlarklib.Value, error) {
			s.sensitiveReads.consumeAll()
			return starlarklib.Call(thread, fn, args, kwargs)
		}), nil
	case "keys":
		return s.d.Attr(name)
	default:
		// Not in stateAttrs — no such attribute (default-deny; see stateAttrs).
		return nil, nil
	}
}

// vertexMapToStarlarkWithHydrated builds the `state` global for a script.
// Returns a *stateMapValue wrapping the key→VertexDoc dict, carrying the
// fail-closed declared reads that were absent at the step-4 snapshot.
func vertexMapToStarlarkWithHydrated(sc ScriptContext) *stateMapValue {
	d := new(starlarklib.Dict)
	for k, v := range sc.Hydrated {
		_ = d.SetKey(starlarklib.String(k), vertexDocToStarlark(v))
	}
	return &stateMapValue{
		d:              d,
		requiredAbsent: sc.RequiredAbsent,
		deferredMiss:   sc.DeferredMiss,
		sensitiveReads: sc.SensitiveReads,
	}
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
		// True only when step 3 proved `authContextTarget` (scope=self target ==
		// actor, or a task grant scoped to exactly that target). A guard that
		// exempts a caller from confinement must key on this, not on the mere
		// presence of `authContextTarget` — that one is a client-supplied hint
		// any scope=any holder can set to an arbitrary value.
		"authTargetValidated": starlarklib.Bool(op.AuthTargetValidated),
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
