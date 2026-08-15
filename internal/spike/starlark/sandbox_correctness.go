package starlark_spike

import (
	"fmt"
	"strings"
)

// SandboxTestResult records the outcome of a single forbidden-operation test.
type SandboxTestResult struct {
	Name       string // test name
	Script     string // the Starlark script attempted
	ShouldFail bool   // expected to be rejected
	Failed     bool   // actual: was it rejected?
	ErrorMsg   string // the exact error message from the interpreter
	Pass       bool   // test passed (expectation matched reality)
}

// RunSandboxCorrectnessTests runs all four forbidden-operation tests
// and returns results for each. A test passes when a forbidden script is rejected
// and a permitted script succeeds.
func RunSandboxCorrectnessTests() []SandboxTestResult {
	ctx := ScriptContext{
		Operation: OperationEnvelope{
			RequestID:     "Rm7q3pntwzkfbcxv5p9j",
			Lane:          "default",
			OperationType: "CreateIdentity",
			Actor:         "vtx.identity.St6mP3qBn4rT8wYxK7Vc",
			SubmittedAt:   "2026-05-13T10:00:00.000Z",
			Payload:       map[string]interface{}{"name": "Test User"},
		},
		Hydrated:  map[string]VertexDoc{},
		DDLLookup: map[string]MetaVertex{},
	}

	tests := []struct {
		name       string
		script     string
		shouldFail bool
	}{
		{
			name: "Forbidden: external HTTP call via load()",
			// Starlark's `load` statement is the mechanism for importing modules.
			// go.starlark.net provides a thread-level load function hook.
			// The default thread has no load hook, so any load() call fails.
			script: `
load("net/http", "get")
def execute(state, op):
    return {"mutations": [], "events": []}
`,
			shouldFail: true,
		},
		{
			name: "Forbidden: filesystem read via open()",
			// Starlark has no built-in open() function. Attempting to call it
			// will raise a NameError because it is not in the global environment.
			script: `
def execute(state, op):
    f = open("/etc/passwd")
    return {"mutations": [], "events": []}
`,
			shouldFail: true,
		},
		{
			name: "Forbidden: os.Getenv equivalent",
			// Starlark has no 'os' module. Attempting to reference 'os' raises
			// a NameError. The global environment only contains what RunScript
			// explicitly adds (state, op, ddl) plus Starlark's safe built-ins.
			script: `
def execute(state, op):
    secret = os.getenv("SECRET_KEY")
    return {"mutations": [], "events": []}
`,
			shouldFail: true,
		},
		{
			name: "Forbidden: non-deterministic time call",
			// Starlark has no built-in time module. Attempting to call time.now()
			// or access 'time' raises a NameError.
			script: `
def execute(state, op):
    now = time.now()
    return {"mutations": [], "events": []}
`,
			shouldFail: true,
		},
		{
			name: "Permitted: arithmetic and string operations",
			// This verifies the sandbox is not over-restricted — safe operations work.
			script: `
def execute(state, op):
    x = 1 + 2
    s = "hello " + "world"
    items = [i for i in range(3)]
    return {"mutations": [], "events": []}
`,
			shouldFail: false,
		},
		{
			name: "Permitted: reading op fields and state",
			// Verifies the script can access op and state globals as intended.
			script: `
def execute(state, op):
    actor = op.actor
    op_type = op.operationType
    return {"mutations": [], "events": []}
`,
			shouldFail: false,
		},
	}

	var results []SandboxTestResult
	for _, tt := range tests {
		_, err := RunScript(tt.script, ctx)
		failed := err != nil

		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}

		pass := failed == tt.shouldFail
		results = append(results, SandboxTestResult{
			Name:       tt.name,
			Script:     strings.TrimSpace(tt.script),
			ShouldFail: tt.shouldFail,
			Failed:     failed,
			ErrorMsg:   errMsg,
			Pass:       pass,
		})
	}
	return results
}

// PrintSandboxResults prints a human-readable summary of sandbox test results.
func PrintSandboxResults(results []SandboxTestResult) {
	fmt.Println("=== SANDBOX CORRECTNESS TESTS ===")
	fmt.Println()

	allPass := true
	for _, r := range results {
		status := "PASS"
		if !r.Pass {
			status = "FAIL"
			allPass = false
		}

		fmt.Printf("[%s] %s\n", status, r.Name)
		if r.ShouldFail {
			fmt.Printf("       Expected: rejected\n")
			if r.Failed {
				fmt.Printf("       Actual:   rejected\n")
				fmt.Printf("       Error:    %s\n", r.ErrorMsg)
			} else {
				fmt.Printf("       Actual:   SUCCEEDED (unexpected — sandbox breach!)\n")
			}
		} else {
			fmt.Printf("       Expected: success\n")
			if r.Failed {
				fmt.Printf("       Actual:   failed (unexpected — over-restricted!)\n")
				fmt.Printf("       Error:    %s\n", r.ErrorMsg)
			} else {
				fmt.Printf("       Actual:   succeeded\n")
			}
		}
		fmt.Println()
	}

	if allPass {
		fmt.Println("Sandbox correctness: ALL TESTS PASSED")
	} else {
		fmt.Println("Sandbox correctness: SOME TESTS FAILED — review above")
	}
	fmt.Println()
}
