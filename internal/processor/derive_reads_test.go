package processor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/starlarksandbox"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// Contract #2 §2.5 class (g): a DDL script may declare a top-level
// `derive_reads(op)` whose returned keys join the declared read set at the
// head of step 4. These tests pin the contract's five rules — determinism via
// fail-closed stubs, Contract #1 grammar validation, weakest-wins merge, the
// re-checked egressReads exclusion, and ceiling accounting as a step-4
// runtime fault — plus the two cost claims the design rests on.

const deriveTestKeyA = "vtx.identityindex." + testNanoID1
const deriveTestKeyB = "vtx.identityindex." + testNanoID2

// seedDeriveDDL writes an `identity` DDL whose script carries the supplied
// derive_reads body (empty for none) alongside a no-op execute.
func seedDeriveDDL(t *testing.T, ctx context.Context, conn *substrate.Conn, deriveBody string) {
	t.Helper()
	src := "def execute(state, op):\n    return {\"mutations\": [], \"events\": []}\n"
	if deriveBody != "" {
		src += "\n" + deriveBody
	}
	body, err := json.Marshal(map[string]any{
		"class": "meta.script", "isDeleted": false,
		"data": map[string]any{"source": src},
	})
	if err != nil {
		t.Fatalf("marshal script: %v", err)
	}
	ddlDoc := []byte(`{"class":"meta.ddl.vertexType","isDeleted":false,"data":{"canonicalName":"identity","permittedCommands":["CreateIdentity"]}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.meta.identity", ddlDoc); err != nil {
		t.Fatalf("seed DDL: %v", err)
	}
	if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.meta.identity.script", body); err != nil {
		t.Fatalf("seed script: %v", err)
	}
}

// hydrateWithDerivation seeds a DDL carrying deriveBody and hydrates env
// through the real Hydrator (no DDL cache — the shadow-key path, which
// compiles per operation and still shares the program across both passes).
func hydrateWithDerivation(t *testing.T, deriveBody string, env *OperationEnvelope) (HydratedState, error) {
	t.Helper()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	seedDeriveDDL(t, ctx, conn, deriveBody)
	h := NewHydrator(conn, testCoreBucket, testLogger())
	return h.Hydrate(ctx, env)
}

func hydrationErrCode(t *testing.T, err error) string {
	t.Helper()
	var hErr *HydrationError
	if !errors.As(err, &hErr) {
		t.Fatalf("want *HydrationError, got %T: %v", err, err)
	}
	return hErr.Code
}

// TestDeriveReads_WellFormedKeyJoinsTheDeclaredSet is the happy path: a
// derived key is hydrated exactly like a declared one, and — being returned
// under optionalReads — its absence is recorded known-absent rather than
// faulting, which is the read-before-create branch class (g) exists to serve.
func TestDeriveReads_WellFormedKeyJoinsTheDeclaredSet(t *testing.T) {
	t.Parallel()
	env := newTestEnvelope(testNanoID1)
	state, err := hydrateWithDerivation(t, `
def derive_reads(op):
    name = op.payload.name
    return {"optionalReads": ["vtx.identityindex." + crypto.sha256NanoID("name:" + name)]}
`, env)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	want := "vtx.identityindex." + substrate.SHA256NanoID("name:Andrew")
	if _, ok := state.Context.KnownAbsent[want]; !ok {
		t.Fatalf("derived key %q not in the declared set; knownAbsent=%v", want, state.Context.KnownAbsent)
	}
	// The envelope is NOT rewritten: a derived key is not something the
	// submitter said, and the reply/audit read what the client sent.
	if env.ContextHint != nil {
		t.Fatalf("derive_reads must not write back to the envelope, got %+v", env.ContextHint)
	}
}

// TestDeriveReads_MalformedKeyFailsClosed pins Contract #1 grammar validation
// at the derivation, not at hydration: a bad key must be attributed to the
// script that produced it rather than surfacing later as a miss on a key
// nobody declared.
func TestDeriveReads_MalformedKeyFailsClosed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, key string }{
		{"not a key at all", "definitely-not-a-key"},
		{"five segments", "lnk.identity.a.holdsRole.role"},
		{"bad nanoid", "vtx.identity.short"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := hydrateWithDerivation(t, `
def derive_reads(op):
    return {"reads": ["`+tc.key+`"]}
`, newTestEnvelope(testNanoID1))
			if err == nil {
				t.Fatalf("a malformed derived key must fail the operation closed")
			}
			if code := hydrationErrCode(t, err); code != "DeriveReadsInvalid" {
				t.Fatalf("code = %q, want DeriveReadsInvalid", code)
			}
			if !strings.Contains(err.Error(), "derive_reads") {
				t.Fatalf("the error must name the derivation: %v", err)
			}
		})
	}
}

// TestDeriveReads_ImpureModulesAreFailClosedStubs is the determinism rule.
// Both modules must be PRESENT (the pre-pass shares the main pass's compiled
// program, so an unbound name would fail to compile the whole module and kill
// every op on the DDL) and must fail when CALLED.
func TestDeriveReads_ImpureModulesAreFailClosedStubs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, call string }{
		{"kv.Read", `kv.Read("vtx.identity." + "` + testNanoID2 + `")`},
		{"kv.Links", `kv.Links("vtx.identity." + "` + testNanoID2 + `", "holdsRole", "out")`},
		{"nanoid.new", `nanoid.new()`},
		{"nanoid.short", `nanoid.short()`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := hydrateWithDerivation(t, `
def derive_reads(op):
    x = `+tc.call+`
    return {}
`, newTestEnvelope(testNanoID1))
			if err == nil {
				t.Fatalf("%s inside derive_reads must fail closed", tc.name)
			}
			if code := hydrationErrCode(t, err); code != "DeriveReadsFailed" {
				t.Fatalf("code = %q, want DeriveReadsFailed", code)
			}
			if !strings.Contains(err.Error(), "not available inside derive_reads") {
				t.Fatalf("the error must explain why the module is stubbed: %v", err)
			}
		})
	}
}

// TestDeriveReads_StubsCoverEveryRealMember is the drift guard on the stub
// sets, and it is the difference between "complete today" and "complete".
//
// failingModule takes a hand-written member list. Adding a builtin to the real
// `kv` or `nanoid` module without adding it here would leave that member
// UNSTUBBED in the pre-pass — and because the pre-pass shares the main pass's
// compiled program, an unstubbed member is not an unbound name that fails
// loudly: it is simply absent from the struct, so a derivation calling it gets
// an attribute error rather than the fail-closed message, and a member backed
// by a live reader could become reachable. Compare against the real modules'
// own attribute sets so the next builtin cannot land silently.
func TestDeriveReads_StubsCoverEveryRealMember(t *testing.T) {
	t.Parallel()
	realKV := kvModule(ScriptContext{})
	realNanoID := nanoidModule(testNanoID1)
	stubs := deriveReadsGlobals(deriveReadsOpValue(newTestEnvelope(testNanoID1)), nil)

	for _, tc := range []struct {
		module string
		real   interface{ AttrNames() []string }
	}{
		{"kv", realKV},
		{"nanoid", realNanoID},
	} {
		stub, ok := stubs[tc.module].(interface{ AttrNames() []string })
		if !ok {
			t.Fatalf("the pre-pass's %q binding exposes no attribute set", tc.module)
		}
		have := map[string]struct{}{}
		for _, n := range stub.AttrNames() {
			have[n] = struct{}{}
		}
		for _, n := range tc.real.AttrNames() {
			if _, ok := have[n]; !ok {
				t.Fatalf("%s.%s exists on the real module but is not stubbed in the derive_reads pre-pass — add it to failingModule", tc.module, n)
			}
		}
	}
}

// TestDeriveReads_PureModulesStillWork is the other half of the stub rule: the
// derivation's whole purpose is to call the SAME crypto builtin the main
// script calls, so gating the impure modules must not gate the pure ones.
func TestDeriveReads_PureModulesStillWork(t *testing.T) {
	t.Parallel()
	state, err := hydrateWithDerivation(t, `
def derive_reads(op):
    h = crypto.sha256NanoID("name:" + op.payload.name)
    _ = time.rfc3339_utc("2026-08-03T00:00:00Z")
    _ = json.encode({"a": 1})
    return {"optionalReads": ["vtx.identityindex." + h]}
`, newTestEnvelope(testNanoID1))
	if err != nil {
		t.Fatalf("the pure modules must remain available: %v", err)
	}
	if len(state.Context.KnownAbsent) != 1 {
		t.Fatalf("knownAbsent = %v, want the one derived key", state.Context.KnownAbsent)
	}
}

// TestDeriveReads_WeakestWins pins the merge precedence in the direction that
// can regress. A derived `reads` entry colliding with a declared
// `optionalReads` key must keep the ENVELOPE's weaker disposition: hardening
// it would fault HydrationMiss on exactly the dedup branch class (d) exists
// for, which is class (g)'s main consumer.
func TestDeriveReads_WeakestWins(t *testing.T) {
	t.Parallel()
	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{OptionalReads: []string{deriveTestKeyA}}

	state, err := hydrateWithDerivation(t, `
def derive_reads(op):
    return {"reads": ["`+deriveTestKeyA+`"]}
`, env)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if _, ok := state.Context.KnownAbsent[deriveTestKeyA]; !ok {
		t.Fatalf("the envelope's optionalReads disposition must stand; knownAbsent=%v requiredAbsent=%v",
			state.Context.KnownAbsent, state.Context.RequiredAbsent)
	}
	if _, ok := state.Context.RequiredAbsent[deriveTestKeyA]; ok {
		t.Fatalf("a derived `reads` entry must not harden a declared optionalReads key")
	}
}

// TestDeriveReads_WeakestWinsAcrossDerivedLists applies the same rule to a key
// the derivation returns under BOTH lists. The contract states weakest-wins
// only against the envelope; resolving a derived/derived collision the other
// way would make the outcome depend on list order.
func TestDeriveReads_WeakestWinsAcrossDerivedLists(t *testing.T) {
	t.Parallel()
	state, err := hydrateWithDerivation(t, `
def derive_reads(op):
    return {"reads": ["`+deriveTestKeyA+`"], "optionalReads": ["`+deriveTestKeyA+`"]}
`, newTestEnvelope(testNanoID1))
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if _, ok := state.Context.KnownAbsent[deriveTestKeyA]; !ok {
		t.Fatalf("a key in both derived lists must take the weaker disposition; knownAbsent=%v requiredAbsent=%v",
			state.Context.KnownAbsent, state.Context.RequiredAbsent)
	}
}

// TestDeriveReads_EgressCollisionFaultsAtStep4 pins the re-checked exclusion.
// ParseEnvelope runs before derivation, so a derived key colliding with an
// egressReads key is invisible to it; without the step-4 re-check the
// plaintext hydration loop wins and silently demotes the egress disposition,
// surfacing (if at all) as an opaque step-6 rejection.
func TestDeriveReads_EgressCollisionFaultsAtStep4(t *testing.T) {
	t.Parallel()
	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{EgressReads: []string{deriveTestKeyA}}

	_, err := hydrateWithDerivation(t, `
def derive_reads(op):
    return {"optionalReads": ["`+deriveTestKeyA+`"]}
`, env)
	if err == nil {
		t.Fatalf("a derived key colliding with egressReads must fault at step 4")
	}
	if code := hydrationErrCode(t, err); code != "DeriveReadsEgressConflict" {
		t.Fatalf("code = %q, want DeriveReadsEgressConflict", code)
	}
	if !strings.Contains(err.Error(), "derive_reads") {
		t.Fatalf("the error must name the derivation: %v", err)
	}
}

// TestDeriveReads_CeilingFaultsAtStep4NotAtParse pins the accounting rule. The
// envelope alone is under the ceiling — so parse accepts it — and the merged
// set is over it, which must be a step-4 runtime fault rather than
// EnvelopeMalformed: the derived keys are not envelope-supplied, so rejecting
// the envelope would blame the submitter for the package's derivation.
func TestDeriveReads_CeilingFaultsAtStep4NotAtParse(t *testing.T) {
	t.Parallel()
	env := newTestEnvelope(testNanoID1)
	// A derivation returning one key per index in a wide range: cheap to
	// express, and the ids are valid NanoIDs by construction.
	_, err := hydrateWithDerivation(t, `
ALPHABET = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

def derive_reads(op):
    keys = []
    for i in range(1200):
        keys.append("vtx.identityindex." + crypto.sha256NanoID("k%d" % i))
    return {"optionalReads": keys}
`, env)
	if err == nil {
		t.Fatalf("a merged set over the ceiling must fault")
	}
	if code := hydrationErrCode(t, err); code != "DeclaredReadCeilingExceeded" {
		t.Fatalf("code = %q, want DeclaredReadCeilingExceeded", code)
	}
}

// TestDeriveReads_CeilingCountsDistinctKeys pins the count as DISTINCT keys,
// matching distinctKeys' existing semantics: the ceiling has always bounded
// Core KV round trips rather than mentions, so a derived duplicate of a
// declared key must not consume the budget twice.
func TestDeriveReads_CeilingCountsDistinctKeys(t *testing.T) {
	t.Parallel()
	base := declaredReads{Reads: []string{deriveTestKeyA}}
	merged, err := mergeDerivedReads(base, derivedReads{OptionalReads: []string{deriveTestKeyA, deriveTestKeyB}}, "rid")
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if got := len(distinctAcrossClasses(merged)); got != 2 {
		t.Fatalf("distinct count = %d, want 2 (the duplicate must not be counted twice)", got)
	}
}

// TestDeriveReads_AbsentDerivationCostsNothing is the design's opt-in claim,
// asserted rather than stated — and asserted in a way that needs no counter in
// production code. A wall budget of 1ns cannot survive an Init, so:
//
//   - a DDL with NO derive_reads hydrates fine, proving the pre-pass never ran;
//   - the same DDL WITH one fails on that budget, proving the budget is real
//     and the pre-pass does run when the entrypoint is present.
//
// Together those two are what "no invocation, no cost" means operationally.
func TestDeriveReads_AbsentDerivationCostsNothing(t *testing.T) {
	t.Parallel()
	run := func(t *testing.T, deriveBody string) error {
		t.Helper()
		ctx, conn, _, _, _ := setupTestPipeline(t)
		seedDeriveDDL(t, ctx, conn, deriveBody)
		h := NewHydrator(conn, testCoreBucket, testLogger())
		h.DeriveBudget = starlarksandbox.Budget{Wall: time.Nanosecond, MaxSteps: 1}
		_, err := h.Hydrate(ctx, newTestEnvelope(testNanoID1))
		return err
	}

	t.Run("no derivation, unrunnable budget, still fine", func(t *testing.T) {
		t.Parallel()
		if err := run(t, ""); err != nil {
			t.Fatalf("a DDL declaring no derive_reads must not pay the pre-pass: %v", err)
		}
	})
	t.Run("a derivation on the same budget aborts", func(t *testing.T) {
		t.Parallel()
		err := run(t, "def derive_reads(op):\n    return {}\n")
		if err == nil {
			t.Fatalf("the pre-pass budget must actually bound the derivation")
		}
		if code := hydrationErrCode(t, err); code != "DeriveReadsFailed" {
			t.Fatalf("code = %q, want DeriveReadsFailed", code)
		}
	})
}

// TestDeriveReads_CompiledProgramIsSharedNotRecompiled pins the cost claim
// structurally: one CompiledScript per DDL-cache generation, one compile
// inside it, and the very same pointer handed to step 5 — so the second pass
// adds no compile.
func TestDeriveReads_CompiledProgramIsSharedNotRecompiled(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	seedDeriveDDL(t, ctx, conn, "def derive_reads(op):\n    return {}\n")

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	ref, ok := cache.Lookup("identity")
	if !ok {
		t.Fatalf("identity not cached")
	}
	if ref.Script == nil {
		t.Fatalf("the cache entry must carry a compiled-script holder")
	}

	first, sErr := ref.Script.program()
	if sErr != nil {
		t.Fatalf("compile: %v", sErr)
	}
	second, _ := ref.Script.program()
	if first != second {
		t.Fatalf("program() must return the one shared compile, not recompile")
	}

	// Lookup hands out a VALUE copy; the holder must still be the same one,
	// or every operation would compile its own.
	again, _ := cache.Lookup("identity")
	if again.Script != ref.Script {
		t.Fatalf("each Lookup copy must share one compiled-script holder")
	}

	// And step 4 must hand that same holder to step 5.
	h := NewHydratorWithCache(conn, testCoreBucket, cache, testLogger())
	state, err := h.Hydrate(ctx, newTestEnvelope(testNanoID1))
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if state.Context.Compiled != ref.Script {
		t.Fatalf("step 4 must carry the cache's compiled program into step 5")
	}
}

// TestCompiledFor_SourceIsTheAuthority pins the guard that keeps a carried
// program from diverging from the source its context names. A ScriptContext
// holds both, and a caller may replace one without the other (test harnesses
// hydrate against a fixture DDL and then run an inline script) — preferring
// the carried program there would silently run a script nobody asked for.
func TestCompiledFor_SourceIsTheAuthority(t *testing.T) {
	t.Parallel()
	cached := newCompiledScript("def execute(state, op):\n    return {}\n")
	if got := compiledFor(cached, cached.source); got != cached {
		t.Fatalf("a matching source must reuse the compiled program")
	}
	other := compiledFor(cached, "def execute(state, op):\n    return {\"mutations\": []}\n")
	if other == cached {
		t.Fatalf("a differing source must compile what the context names")
	}
	if got := compiledFor(nil, "x = 1\n"); got == nil {
		t.Fatalf("a nil holder must still yield a compilable one")
	}
}

// TestScriptGlobals_KeySetMatchesTheCompileNameSet is the drift guard the two
// passes depend on. The sandbox resolves globals at COMPILE time, so a runner
// dict that gains or loses a name relative to scriptGlobalNames would produce
// a program resolved against one set and initialized with another — a failure
// that would surface on a live operation, not at build time.
func TestScriptGlobals_KeySetMatchesTheCompileNameSet(t *testing.T) {
	t.Parallel()
	sc := ScriptContext{Operation: newTestEnvelope(testNanoID1)}
	globals := scriptGlobals(sc, nil, nil)
	if len(globals) != len(scriptGlobalNames) {
		t.Fatalf("globals has %d names, scriptGlobalNames has %d", len(globals), len(scriptGlobalNames))
	}
	for _, name := range scriptGlobalNames {
		if _, ok := globals[name]; !ok {
			t.Fatalf("scriptGlobalNames declares %q but scriptGlobals does not bind it", name)
		}
	}
	// The pre-pass binds the same names, with the impure ones stubbed.
	pre := deriveReadsGlobals(deriveReadsOpValue(newTestEnvelope(testNanoID1)), nil)
	if len(pre) != len(scriptGlobalNames) {
		t.Fatalf("the pre-pass binds %d names, want %d", len(pre), len(scriptGlobalNames))
	}
	for _, name := range scriptGlobalNames {
		if _, ok := pre[name]; !ok {
			t.Fatalf("the pre-pass does not bind %q, so it cannot share the compiled program", name)
		}
	}
}

// TestParseDerivedReads_RejectsUnknownReturnKeys keeps the return shape closed:
// an unrecognized key is almost certainly a typo for one of the two, and
// accepting it silently would drop the keys the author meant to declare.
func TestParseDerivedReads_RejectsUnknownReturnKeys(t *testing.T) {
	t.Parallel()
	_, err := hydrateWithDerivation(t, `
def derive_reads(op):
    return {"egressReads": ["`+deriveTestKeyA+`"]}
`, newTestEnvelope(testNanoID1))
	if err == nil {
		t.Fatalf("an unrecognized return key must fail closed")
	}
	if code := hydrationErrCode(t, err); code != "DeriveReadsInvalid" {
		t.Fatalf("code = %q, want DeriveReadsInvalid", code)
	}
}

// TestDeriveReads_EmptyReturnIsLegitimate — the contract's own example takes
// an `if not name: return {}` branch, so a payload that derives nothing is a
// normal outcome and not a fault.
func TestDeriveReads_EmptyReturnIsLegitimate(t *testing.T) {
	t.Parallel()
	env := newTestEnvelope(testNanoID1)
	env.Payload = json.RawMessage(`{}`)
	state, err := hydrateWithDerivation(t, `
def derive_reads(op):
    if not hasattr(op.payload, "name"):
        return {}
    return {"optionalReads": ["vtx.identityindex." + crypto.sha256NanoID(op.payload.name)]}
`, env)
	if err != nil {
		t.Fatalf("deriving nothing must not fault: %v", err)
	}
	if len(state.Context.KnownAbsent) != 0 || len(state.Context.RequiredAbsent) != 0 {
		t.Fatalf("nothing should have been declared: %v / %v", state.Context.KnownAbsent, state.Context.RequiredAbsent)
	}
}
