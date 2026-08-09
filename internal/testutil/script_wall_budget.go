package testutil

import (
	"time"

	"github.com/operatinggraph/lattice/internal/processor"
)

// installFixtureScriptWall is the Starlark wall budget a test binary that drives
// real package installs runs under.
//
// A package install validates a whole package — 100+ DDL mutations — inside a
// single script call, where a production user op validates one. Measured on an
// unloaded box the longest such call is 40-60ms, comfortably inside NFR-P4's
// 250ms production budget; under the CPU contention of a parallel suite on a
// shared runner the same call stretches past it and the install fails
// `ScriptTimeout: script exceeded wall budget 250ms` in a package the change
// never touched. The margin here is deliberately ~100x the real cost: this
// bounds a runaway script in a fixture, it does not measure latency.
//
// NFR-P4 itself is unaffected — the production default in
// processor.DefaultScriptWallBudget is what every binary outside a test still
// runs under. The widening reaches every test binary that links this package,
// which is wider than the set that drives installs; what it must not reach is a
// test that ASSERTS the budget, and it does not. The wall-budget assertions live
// in internal/processor (starlark_kv_test.go's SlowRead/SlowList ScriptTimeout
// cases, step4_hydrate's b.Wall) and internal/starlarksandbox
// (TestExecute_WallBudget_ScriptTimeout); neither package imports internal/testutil,
// so both are still held to the production default.
const installFixtureScriptWall = 5 * time.Second

// init widens the Starlark wall budget for the test binary linking this package.
//
// The budget belongs to the FIXTURE, not to whoever launched `go test`. Supplied
// from outside — PROCESSOR_SCRIPT_WALL_MS in a Makefile target or a CI job — a
// test's determinism becomes a property of how it was invoked, and the invocation
// that most needs it is the one least likely to carry it: `go test ./<pkg>/
// -count=1`, the single-package re-run CLAUDE.md's triage rule mandates, is typed
// by hand with no environment at all. Setting it here holds for every route into
// the fixture, and confines the widening to the binaries that link it rather than
// every package in the suite.
//
// Assignment (not os.Setenv) because processor reads the environment once, in its
// own package init, which has already run by the time this one does. Setting it
// here happens-before every test in the binary: `go test` runs package inits, then
// TestMain/the test functions.
func init() {
	processor.DefaultScriptWallBudget = installFixtureScriptWall
}
