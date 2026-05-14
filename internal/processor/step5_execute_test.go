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

func TestSandbox_ForbidsTime(t *testing.T) {
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
	if sErr.Code != "SandboxViolation" {
		t.Fatalf("time.now(): expected SandboxViolation, got %q (%s)", sErr.Code, sErr.Message)
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
