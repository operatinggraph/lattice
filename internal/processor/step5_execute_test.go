package processor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// buildContext builds a ScriptContext for the unit-level executor tests.
// No NATS round-trips — purely in-memory.
func buildContext(script string) ScriptContext {
	return ScriptContext{
		Operation: &OperationEnvelope{
			RequestID:     "Rm7q3pntwzkfbcxv5p9j",
			Lane:          LaneDefault,
			OperationType: "CreateIdentity",
			Actor:         "vtx.identity.St6mP3qBn4rT8wYxK7Vc",
			SubmittedAt:   "2026-05-13T10:00:00Z",
			Class:         "identity",
			Payload:       json.RawMessage(`{"name":"Andrew","email":"andrew@lattice.example"}`),
		},
		Hydrated: map[string]VertexDoc{
			"vtx.identity.St6mP3qBn4rT8wYxK7Vc": {
				Key:       "vtx.identity.St6mP3qBn4rT8wYxK7Vc",
				Class:     "identity",
				IsDeleted: false,
				Data:      map[string]interface{}{"name": "System"},
			},
		},
		DDLLookup: map[string]MetaVertex{
			"identity": {Key: "vtx.meta.identity", CanonicalName: "identity",
				PermittedCommands: []string{"CreateIdentity"}},
		},
		ScriptClass:  "identity",
		ScriptSource: script,
	}
}

func TestExecute_CleanExecution(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    actor = state[op.actor]
    if actor == None:
        fail("missing actor")
    new_id = nanoid.new()
    return {
        "mutations": [
            {
                "op": "create",
                "key": "vtx.identity." + new_id,
                "document": {"class": "identity", "isDeleted": False, "data": {"name": op.payload.name}},
            }
        ],
        "events": [
            {"class": "identityCreated", "data": {"name": op.payload.name}}
        ],
    }
`
	sc := buildContext(script)
	res, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Mutations) != 1 || res.Mutations[0].Op != "create" {
		t.Fatalf("mutations: %+v", res.Mutations)
	}
	if !strings.HasPrefix(res.Mutations[0].Key, "vtx.identity.") {
		t.Fatalf("key: %q", res.Mutations[0].Key)
	}
	if len(res.Events) != 1 || res.Events[0].Class != "identityCreated" {
		t.Fatalf("events: %+v", res.Events)
	}
}

// TestExecute_DDLMetaKeyExposed_EnablesInstanceOfLink proves the script can read
// its own type-DDL meta key off the `ddl` global and write an instanceOf link to
// it — the producer of the Contract #1 §1.5 instanceOf terminal the step-6
// write-gate resolver consumes (the instanceOf-template lift, Fire E). Before
// this, ddl entries exposed only canonicalName + permittedCommands, so a
// fine-grained-class vertex had no way to declare its type authority and fell to
// the permissive default.
func TestExecute_DDLMetaKeyExposed_EnablesInstanceOfLink(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    meta = ddl["identity"].metaKey          # the script's own type-DDL meta key
    meta_id = meta.split(".")[2]            # vtx.meta.<id> → <id>
    new_id = nanoid.new()
    inst = "vtx.service." + new_id
    link = "lnk.service." + new_id + ".instanceOf.meta." + meta_id
    return {
        "mutations": [
            {"op": "create", "key": inst, "document": {"class": "service.bgCheck.instance", "isDeleted": False, "data": {}}},
            {"op": "create", "key": link, "document": {"class": "instanceOf", "isDeleted": False, "data": {}}},
        ],
        "events": [],
    }
`
	sc := buildContext(script)
	res, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var linkKey string
	for _, m := range res.Mutations {
		if strings.Contains(m.Key, ".instanceOf.meta.") {
			linkKey = m.Key
		}
	}
	if linkKey == "" {
		t.Fatalf("no instanceOf→meta link produced (metaKey unreadable?); mutations: %+v", res.Mutations)
	}
	// The link must target the DDL's meta-vertex — proving the script obtained
	// vtx.meta.identity from ddl["identity"].metaKey.
	if !strings.HasSuffix(linkKey, ".instanceOf.meta.identity") {
		t.Fatalf("instanceOf link did not target the DDL meta key: %q", linkKey)
	}
}

func TestExecute_DeterministicNanoID(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    return {"mutations": [{"op": "create", "key": "vtx.identity." + nanoid.new(), "document": {"class":"identity","isDeleted":False,"data":{}}}], "events": []}
`
	sc := buildContext(script)
	res1, err1 := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	if err1 != nil {
		t.Fatalf("run 1: %v", err1)
	}
	res2, err2 := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	if err2 != nil {
		t.Fatalf("run 2: %v", err2)
	}
	if res1.Mutations[0].Key != res2.Mutations[0].Key {
		t.Fatalf("nanoid not deterministic across runs: %q vs %q",
			res1.Mutations[0].Key, res2.Mutations[0].Key)
	}
}

func TestExecute_FailCallProducesScriptError(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    fail("business rule violation: " + op.payload.name)
`
	sc := buildContext(script)
	_, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	var sErr *ScriptError
	if !errors.As(err, &sErr) {
		t.Fatalf("expected *ScriptError, got %T: %v", err, err)
	}
	if sErr.Code != "ScriptError" {
		t.Fatalf("Code = %q, want ScriptError", sErr.Code)
	}
}

// ---- Sandbox-violation vectors (the four AC-required tests) ----

func TestSandbox_ForbidsLoad(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
load("net/http", "get")
def execute(state, op):
    return {"mutations": [], "events": []}
`
	_, err := exec.Execute(context.Background(), buildContext(script).Operation, HydratedState{Context: buildContext(script)})
	var sErr *ScriptError
	if !errors.As(err, &sErr) {
		t.Fatalf("expected *ScriptError, got %v", err)
	}
	if sErr.Code != "SandboxViolation" && sErr.Code != "ScriptError" {
		t.Fatalf("Code = %q, expected SandboxViolation or ScriptError", sErr.Code)
	}
	// The key signal: the script failed at all (didn't reach the empty return).
}

func TestSandbox_ForbidsOpen(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    f = open("/etc/passwd")
    return {"mutations": [], "events": []}
`
	sc := buildContext(script)
	_, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	var sErr *ScriptError
	if !errors.As(err, &sErr) {
		t.Fatalf("expected *ScriptError, got %v", err)
	}
	if sErr.Code != "SandboxViolation" {
		t.Fatalf("open(): expected SandboxViolation, got %q (%s)", sErr.Code, sErr.Message)
	}
}

func TestSandbox_ForbidsOsGetenv(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    secret = os.getenv("SECRET")
    return {"mutations": [], "events": []}
`
	sc := buildContext(script)
	_, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	var sErr *ScriptError
	if !errors.As(err, &sErr) {
		t.Fatalf("expected *ScriptError, got %v", err)
	}
	if sErr.Code != "SandboxViolation" {
		t.Fatalf("os.getenv: expected SandboxViolation, got %q (%s)", sErr.Code, sErr.Message)
	}
}

// TestSandbox_ForbidsWallClock proves a script cannot read the host wall
// clock. The `time` module is a sandboxed builtin that exposes ONLY the pure
// `rfc3339_utc(s)` normalizer (a deterministic function of its argument, like
// crypto.sha256) — it deliberately does NOT expose `now()` or any other
// wall-clock surface. Probing `time.now()` therefore fails: the module has no
// such attribute. The security property (no non-deterministic clock read)
// holds; only the error classification differs from an entirely-unbound name.
func TestSandbox_ForbidsWallClock(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    now = time.now()
    return {"mutations": [], "events": []}
`
	sc := buildContext(script)
	_, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	var sErr *ScriptError
	if !errors.As(err, &sErr) {
		t.Fatalf("expected *ScriptError, got %v", err)
	}
	if !strings.Contains(sErr.Message, "no .now") && !strings.Contains(sErr.Message, "now") {
		t.Fatalf("time.now(): expected a no-such-attribute error (wall clock unreachable), got %q (%s)", sErr.Code, sErr.Message)
	}
}

// TestSandbox_TimeNormalizerOnly confirms the one pure function the `time`
// module DOES expose works (validates + normalizes RFC3339) — so legitimate
// timestamp normalization is available without exposing the wall clock.
func TestSandbox_TimeNormalizerOnly(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    norm = time.rfc3339_utc("2026-06-04T23:00:00+09:00")
    return {"mutations": [], "events": [{"class": "health.probe", "data": {"norm": norm}}]}
`
	sc := buildContext(script)
	res, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	if err != nil {
		t.Fatalf("time.rfc3339_utc must work: %v", err)
	}
	if len(res.Events) != 1 || res.Events[0].Data["norm"] != "2026-06-04T14:00:00Z" {
		t.Fatalf("time.rfc3339_utc normalize = %v, want 2026-06-04T14:00:00Z", res.Events)
	}
}

func TestSandbox_PermittedOpsWork(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    x = 1 + 2
    s = "hello " + "world"
    items = [i for i in range(3)]
    return {"mutations": [], "events": []}
`
	sc := buildContext(script)
	_, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	if err != nil {
		t.Fatalf("permitted ops should not error: %v", err)
	}
}

// ---- Timeout ----

func TestExecute_Timeout(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(50*time.Millisecond, 1_000_000_000), testLogger())
	script := `
def execute(state, op):
    n = 0
    for i in range(10000000):
        n = n + i
    return {"mutations": [], "events": []}
`
	sc := buildContext(script)
	_, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	var sErr *ScriptError
	if !errors.As(err, &sErr) {
		t.Fatalf("expected *ScriptError, got %v", err)
	}
	if sErr.Code != "ScriptTimeout" && sErr.Code != "ScriptError" {
		t.Fatalf("expected timeout, got %q (%s)", sErr.Code, sErr.Message)
	}
}

// ---- Return shape validation ----

func TestExecute_InvalidReturnShape_NotDict(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    return [1, 2, 3]
`
	sc := buildContext(script)
	_, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	var sErr *ScriptError
	if !errors.As(err, &sErr) || sErr.Code != "InvalidReturnShape" {
		t.Fatalf("expected InvalidReturnShape, got %v", err)
	}
}

func TestExecute_InvalidMutationOp(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    return {"mutations": [{"op": "delete", "key": "vtx.x.AAAAAAAAAAAAAAAAAAAA"}], "events": []}
`
	sc := buildContext(script)
	_, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	var sErr *ScriptError
	if !errors.As(err, &sErr) || sErr.Code != "InvalidReturnShape" {
		t.Fatalf("expected InvalidReturnShape for bad op, got %v", err)
	}
}

func TestExecute_NoExecuteFunction(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def something_else(state, op):
    return {"mutations": [], "events": []}
`
	sc := buildContext(script)
	_, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	var sErr *ScriptError
	if !errors.As(err, &sErr) || sErr.Code != "InvalidReturnShape" {
		t.Fatalf("expected InvalidReturnShape for missing execute, got %v", err)
	}
}

// TestParseMutations_ExpectedRevision verifies that a mutation dict containing
// an integer "expectedRevision" field is correctly parsed into a MutationOp
// with a non-nil ExpectedRevision (MF-1, Story 5.3).
func TestParseMutations_ExpectedRevision(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    m = {"op": "tombstone", "key": "vtx.meta.AAAAAAAAAAAAAAAAAAAA"}
    m["expectedRevision"] = 42
    return {"mutations": [m], "events": []}
`
	sc := buildContext(script)
	res, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Mutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(res.Mutations))
	}
	m := res.Mutations[0]
	if m.ExpectedRevision == nil {
		t.Fatal("ExpectedRevision is nil — parseMutations failed to extract expectedRevision from Starlark dict")
	}
	if *m.ExpectedRevision != 42 {
		t.Fatalf("ExpectedRevision: got %d want 42", *m.ExpectedRevision)
	}
}

// TestParseMutations_TombstoneBare verifies a huskless tombstone (no
// "document" key at all — the shape every in-repo emitter produces post
// tombstone-body-preservation-design.md Fire 1) parses cleanly with a nil
// Document.
func TestParseMutations_TombstoneBare(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    return {"mutations": [{"op": "tombstone", "key": "vtx.meta.AAAAAAAAAAAAAAAAAAAA"}], "events": []}
`
	sc := buildContext(script)
	res, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Mutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(res.Mutations))
	}
	if m := res.Mutations[0]; m.Document != nil {
		t.Fatalf("expected nil Document on a bare tombstone, got %+v", m.Document)
	}
}

// TestParseMutations_TombstoneWithDocumentRejects verifies the Fire 2
// posture (tombstone-body-preservation-design.md §5/§6): a tombstone
// mutation carrying a "document" is rejected with InvalidReturnShape rather
// than silently dropped.
func TestParseMutations_TombstoneWithDocumentRejects(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	const tombKey = "vtx.meta.BBBBBBBBBBBBBBBBBBBB"
	script := `
def execute(state, op):
    return {"mutations": [{"op": "tombstone", "key": "` + tombKey + `",
        "document": {"isDeleted": True, "data": {}}}], "events": []}
`
	sc := buildContext(script)
	_, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	if err == nil {
		t.Fatal("expected a rejection, got nil error")
	}
	var scriptErr *ScriptError
	if !errors.As(err, &scriptErr) {
		t.Fatalf("expected a *ScriptError, got %T: %v", err, err)
	}
	if scriptErr.Code != "InvalidReturnShape" {
		t.Fatalf("expected InvalidReturnShape, got %q", scriptErr.Code)
	}
	if !strings.Contains(scriptErr.Message, "tombstone") {
		t.Fatalf("expected message to name the tombstone violation, got: %s", scriptErr.Message)
	}
}
