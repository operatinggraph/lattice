// Command starlark_spike is the runnable spike harness for Story 1.2.
//
// It validates go.starlark.net for use as the Lattice Processor's script execution
// layer across three areas:
//
//  1. Sandbox correctness — four forbidden operations must be rejected
//  2. API ergonomics — ScriptContext prototype, Contract #3-conforming output
//  3. Order-of-magnitude performance — 1,000 sequential invocations
//
// See README.md for the written findings report and Go/No-Go recommendation.
//
// Usage:
//
//	go run ./internal/spike/starlark/
package main

import (
	"fmt"
	"os"
	"runtime"
	"time"

	spike "github.com/operatinggraph/lattice/internal/spike/starlark"
)

func main() {
	fmt.Println("============================================================")
	fmt.Println("  Lattice Story 1.2 — Starlark Execution Spike")
	fmt.Println("  Library: go.starlark.net v0.0.0-20260326113308-fadfc96def35")
	fmt.Printf("  Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Printf("  Go: %s\n", runtime.Version())
	fmt.Printf("  Date: %s\n", time.Now().Format("2006-01-02"))
	fmt.Println("============================================================")
	fmt.Println()

	exitCode := 0

	// ---- Area 1: Sandbox Correctness ----
	sandboxResults := spike.RunSandboxCorrectnessTests()
	spike.PrintSandboxResults(sandboxResults)

	sandboxAllPass := true
	for _, r := range sandboxResults {
		if !r.Pass {
			sandboxAllPass = false
		}
	}
	if !sandboxAllPass {
		fmt.Fprintln(os.Stderr, "ERROR: sandbox correctness tests failed — see output above")
		exitCode = 1
	}

	// ---- Area 2: API Ergonomics ----
	if err := spike.RunAPIErgonomicsTest(); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: API ergonomics test failed: %v\n", err)
		exitCode = 1
	}

	// ---- Area 3: Performance ----
	perfResult, err := spike.RunPerfBenchmark()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: performance benchmark failed: %v\n", err)
		exitCode = 1
	}

	// ---- Summary ----
	fmt.Println("============================================================")
	fmt.Println("  SPIKE SUMMARY")
	fmt.Println("============================================================")
	fmt.Println()

	if sandboxAllPass {
		fmt.Println("[PASS] Sandbox Correctness — all forbidden operations rejected")
	} else {
		fmt.Println("[FAIL] Sandbox Correctness — see errors above")
	}

	fmt.Println("[PASS] API Ergonomics — ScriptContext → ScriptResult pipeline works")

	if perfResult != nil {
		fmt.Printf("[INFO] Performance — mean=%v  p95=%v  p99=%v  (1000 iterations)\n",
			perfResult.Mean, perfResult.P95, perfResult.P99)
	}

	fmt.Println()
	if exitCode == 0 {
		fmt.Println("Spike harness: CLEAN EXIT (all areas passed)")
	} else {
		fmt.Println("Spike harness: ERRORS DETECTED — review above")
	}

	os.Exit(exitCode)
}
