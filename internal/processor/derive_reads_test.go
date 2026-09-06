package processor

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
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
	h := withPrimordialActors(NewHydrator(conn, testCoreBucket, testLogger()))
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
	env := asPrimordialEngine(newTestEnvelope(testNanoID1))
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
	merged, err := mergeDerivedReads(base, derivedReads{OptionalReads: []string{deriveTestKeyA, deriveTestKeyB}}, nil, "rid")
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

// TestDeriveReads_StateAndDDLFailClosed is the determinism rule applied to the
// two MAPPING globals, and it needs stating separately from the module stubs
// because a mapping degrades where a module faults. An empty dict ANSWERS
// every question the pre-pass cannot honestly answer — `state.get(k)` is None,
// `k in state` is False, `len(state)` is 0 — so a derivation reaching for
// hydrated state would derive a WRONG read set with nothing on the wire to say
// so, where `kv.Read` is loud about the same reach.
//
// Every access form is pinned because each is a DIFFERENT interpreter path and
// a type can close one while leaving another silent: subscript resolves
// through Mapping.Get, membership through Container.Has (the interpreter's
// Mapping arm for `in` discards Get's error), the accessors through
// HasAttrs.Attr.
func TestDeriveReads_StateAndDDLFailClosed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, expr string }{
		{"state[k]", `state["` + deriveTestKeyA + `"]`},
		{"state.get(k)", `state.get("` + deriveTestKeyA + `")`},
		{"k in state", `("` + deriveTestKeyA + `" in state)`},
		{"k not in state", `("` + deriveTestKeyA + `" not in state)`},
		{"state.keys()", `state.keys()`},
		{"state.items()", `state.items()`},
		{"state.values()", `state.values()`},
		{"ddl[k]", `ddl["identity"]`},
		{"ddl.get(k)", `ddl.get("identity")`},
		{"k in ddl", `("identity" in ddl)`},
		{"ddl.keys()", `ddl.keys()`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := hydrateWithDerivation(t, `
def derive_reads(op):
    x = `+tc.expr+`
    return {}
`, newTestEnvelope(testNanoID1))
			if err == nil {
				t.Fatalf("%s inside derive_reads must fail closed, not answer a miss", tc.name)
			}
			if code := hydrationErrCode(t, err); code != "DeriveReadsFailed" {
				t.Fatalf("code = %q, want DeriveReadsFailed", code)
			}
			if !strings.Contains(err.Error(), "not available inside derive_reads") {
				t.Fatalf("the error must explain why the mapping is unavailable: %v", err)
			}
		})
	}
}

// TestDeriveReads_StateAndDDLAreNotSilentlyEmptyCollections covers the two
// surfaces the binding cannot supply its own message for: an Iterator has no
// error to return and `len` reads a length, so both are left UNIMPLEMENTED and
// the interpreter raises its own refusal built out of Type(). Empty-and-quiet
// is the outcome that must not happen — a `for k in state` that yields nothing
// derives nothing and reports nothing.
func TestDeriveReads_StateAndDDLAreNotSilentlyEmptyCollections(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, body string }{
		{"for k in state", "    for k in state:\n        pass\n"},
		{"for k in ddl", "    for k in ddl:\n        pass\n"},
		{"len(state)", "    n = len(state)\n"},
		{"len(ddl)", "    n = len(ddl)\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := hydrateWithDerivation(t,
				"\ndef derive_reads(op):\n"+tc.body+"    return {}\n",
				newTestEnvelope(testNanoID1))
			if err == nil {
				t.Fatalf("%s must fail rather than treat the binding as an empty collection", tc.name)
			}
			if code := hydrationErrCode(t, err); code != "DeriveReadsFailed" {
				t.Fatalf("code = %q, want DeriveReadsFailed", code)
			}
			// The message is the interpreter's, so the reason has to ride on
			// the type name — that is what makes an unavailable binding
			// distinguishable from a script bug in a production log.
			if !strings.Contains(err.Error(), "unavailable-inside-derive_reads") {
				t.Fatalf("the refusal must name the derivation through the type: %v", err)
			}
		})
	}
}

// TestDeriveReads_MentioningStateAndDDLElsewhereStillCompilesAndRuns is the
// other half of the binding rule, and the one that decides whether the fix is
// shippable at all.
//
// The sandbox resolves globals at COMPILE time and the pre-pass shares the main
// pass's compiled program, so a name that is merely UNBOUND in the pre-pass
// would fail every module mentioning it — killing every operation on that DDL,
// not just the derivation. So the names stay bound and only reaching INTO them
// fails.
//
// The module-level reference is the load-bearing half and is deliberately at
// module level: `prog.Init` runs the whole top level in BOTH passes, so a name
// the pre-pass did not bind raises there, before any entrypoint is reached. The
// helper functions are the other shape — a reference the pre-pass compiles and
// never evaluates, which must stay compilable — and step 5 running the same
// program afterwards is what shows the refusal is scoped to the pre-pass.
func TestDeriveReads_MentioningStateAndDDLElsewhereStillCompilesAndRuns(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	seedDeriveDDL(t, ctx, conn, `
BOUND_NAMES = [type(state), type(ddl)]

def gate(key):
    return key in state and state[key] != None

def class_of(name):
    return ddl[name].canonicalName

def derive_reads(op):
    return {"optionalReads": ["`+deriveTestKeyA+`"]}
`)
	h := NewHydrator(conn, testCoreBucket, testLogger())
	state, err := h.Hydrate(ctx, newTestEnvelope(testNanoID1))
	if err != nil {
		t.Fatalf("a script mentioning state/ddl outside the derivation must still compile and derive: %v", err)
	}
	if _, ok := state.Context.KnownAbsent[deriveTestKeyA]; !ok {
		t.Fatalf("the derived key did not join the declared set; knownAbsent=%v", state.Context.KnownAbsent)
	}
	// The MAIN pass runs the SAME compiled program with the real mapping bound,
	// which is what shows the pre-pass's refusal is scoped to the pre-pass.
	if _, err := NewStarlarkRunner(0, 0).Run(ctx, state.Context); err != nil {
		t.Fatalf("step 5 must still execute the same program against the hydrated state: %v", err)
	}
}

// TestDeriveReads_DerivedRequiredKeyUnderTheDescriptorFloorIsRefused closes the
// hole the envelope arm structurally cannot see.
//
// applyDescriptorFloor demotes DECLARED keys, and mergeDerivedReads' "envelope's
// disposition stands" rule keeps a derived entry from re-hardening one. A key
// the envelope NEVER declared falls between the two: the demotion pass has no
// subject for it and the merge has no disposition to defer to, so a derived
// `reads` key the same package's descriptor calls absence-tolerant would land
// fail-closed, out from under the floor every declared key is held to.
//
// It is REFUSED rather than demoted. The DDL's derivation and the DDL's
// descriptor are two statements by ONE package about one key, and silently
// picking the descriptor turns the HydrationMiss the script's author demanded
// into a silent None — the dangerous direction
// (descriptor-floor-template-coverage-design §8).
func TestDeriveReads_DerivedRequiredKeyUnderTheDescriptorFloorIsRefused(t *testing.T) {
	t.Parallel()
	env := newTestEnvelope(testNanoID1)
	floored := DispatchTemplates{OptionalReads: []string{deriveTestKeyA}}
	resolver := func(templates DispatchTemplates) *descriptorFloorResolver {
		return newDescriptorFloorResolver(templates, env, testLogger())
	}

	// The fault names the derivation but NOT the key. classifyStepError copies
	// MissingKey into the reply's `details.missingKey` and the reply message
	// carries err.Error(), so either one would hand the caller the package's own
	// derivation over an input of their choosing — and class (g) exists because
	// these are exactly the keys a submitter cannot express. The operator gets
	// the key from the resolver's Warn instead.
	t.Run("a derived requirement the floor covers faults, naming no key on the wire", func(t *testing.T) {
		logger, log := capturingLogger()
		_, err := mergeDerivedReads(declaredReads{}, derivedReads{Reads: []string{deriveTestKeyA}},
			newDescriptorFloorResolver(floored, env, logger), testNanoID1)
		if err == nil {
			t.Fatalf("a derived required key the descriptor calls absence-tolerant must fault, not stand")
		}
		var hErr *HydrationError
		if !errors.As(err, &hErr) {
			t.Fatalf("want *HydrationError, got %T: %v", err, err)
		}
		if hErr.Code != "DeriveReadsFloorContradiction" {
			t.Fatalf("code = %q, want DeriveReadsFloorContradiction", hErr.Code)
		}
		if hErr.MissingKey != "" {
			t.Fatalf("MissingKey = %q, want empty — classifyStepError puts it in details.missingKey", hErr.MissingKey)
		}
		if strings.Contains(err.Error(), deriveTestKeyA) {
			t.Fatalf("the reply carries err.Error(); it must not quote the derived key: %v", err)
		}
		if !strings.Contains(err.Error(), "derive_reads") {
			t.Fatalf("the fault must blame the package's derivation, not the submitter: %v", err)
		}
		if !strings.Contains(log.String(), deriveTestKeyA) {
			t.Fatalf("log = %q, want the derived key — the operator's only copy of it", log.String())
		}
	})

	t.Run("the descriptor's own required template excludes it", func(t *testing.T) {
		templates := DispatchTemplates{Reads: []string{deriveTestKeyA}, OptionalReads: []string{deriveTestKeyA}}
		merged, err := mergeDerivedReads(declaredReads{}, derivedReads{Reads: []string{deriveTestKeyA}},
			resolver(templates), testNanoID1)
		if err != nil {
			t.Fatalf("required-wins: a key the descriptor also declares under `reads` is not floored: %v", err)
		}
		if !slices.Contains(merged.Reads, deriveTestKeyA) {
			t.Fatalf("Reads = %v, want the derived requirement admitted", merged.Reads)
		}
	})

	t.Run("a derived OPTIONAL key contradicts nothing", func(t *testing.T) {
		merged, err := mergeDerivedReads(declaredReads{}, derivedReads{OptionalReads: []string{deriveTestKeyA}},
			resolver(floored), testNanoID1)
		if err != nil {
			t.Fatalf("the derivation agreeing with the descriptor is the ordinary case: %v", err)
		}
		if !slices.Contains(merged.OptionalReads, deriveTestKeyA) {
			t.Fatalf("OptionalReads = %v, want the derived key", merged.OptionalReads)
		}
	})

	t.Run("a key in both derived lists takes the weaker disposition", func(t *testing.T) {
		merged, err := mergeDerivedReads(declaredReads{},
			derivedReads{Reads: []string{deriveTestKeyA}, OptionalReads: []string{deriveTestKeyA}},
			resolver(floored), testNanoID1)
		if err != nil {
			t.Fatalf("weakest-wins claims the key as optional before the floor is asked: %v", err)
		}
		if slices.Contains(merged.Reads, deriveTestKeyA) || !slices.Contains(merged.OptionalReads, deriveTestKeyA) {
			t.Fatalf("got %+v, want the key optional only", merged)
		}
	})

	t.Run("an envelope-declared key keeps the envelope's disposition", func(t *testing.T) {
		base := declaredReads{OptionalReads: []string{deriveTestKeyA}}
		merged, err := mergeDerivedReads(base, derivedReads{Reads: []string{deriveTestKeyA}},
			resolver(floored), testNanoID1)
		if err != nil {
			t.Fatalf("the declared arm is unchanged by this refusal: %v", err)
		}
		if slices.Contains(merged.Reads, deriveTestKeyA) {
			t.Fatalf("Reads = %v, want the envelope's weaker disposition to stand", merged.Reads)
		}
	})

	t.Run("no descriptor floors nothing", func(t *testing.T) {
		merged, err := mergeDerivedReads(declaredReads{}, derivedReads{Reads: []string{deriveTestKeyA}},
			nil, testNanoID1)
		if err != nil {
			t.Fatalf("most of the corpus carries no descriptor; the rule has no subject: %v", err)
		}
		if !slices.Contains(merged.Reads, deriveTestKeyA) {
			t.Fatalf("Reads = %v, want the derived requirement admitted", merged.Reads)
		}
	})
}

// TestDeriveReads_FloorContradictionExclusionIsNotSubmitterSteerable is the
// refusal's integrity property, and it is inherited rather than re-derived: the
// only thing that suppresses the refusal is resolveDescriptorRequired's
// exclusion set, which is a function of the descriptor's own templates and the
// step-3-authenticated identity and refuses to be built out of a
// `{payload.<field>}` template.
//
// The positive control is the same descriptor with a literal required template:
// it proves the exclusion genuinely can suppress this refusal, so the hostile
// arm is testing the steering rather than a suppression that never worked.
func TestDeriveReads_FloorContradictionExclusionIsNotSubmitterSteerable(t *testing.T) {
	t.Parallel()
	env := newTestEnvelope(testNanoID1)
	env.Payload = json.RawMessage(`{"name":"Andrew","probe":"` + deriveTestKeyA + `"}`)
	derived := derivedReads{Reads: []string{deriveTestKeyA}}

	control := DispatchTemplates{Reads: []string{deriveTestKeyA}, OptionalReads: []string{deriveTestKeyA}}
	if _, err := mergeDerivedReads(declaredReads{}, derived,
		newDescriptorFloorResolver(control, env, testLogger()), testNanoID1); err != nil {
		t.Fatalf("the descriptor's own literal required template must suppress the refusal: %v", err)
	}

	// The same descriptor, the same key, one payload field aimed at it.
	logger, log := capturingLogger()
	steered := DispatchTemplates{Reads: []string{"{payload.probe}"}, OptionalReads: []string{deriveTestKeyA}}
	_, err := mergeDerivedReads(declaredReads{}, derived,
		newDescriptorFloorResolver(steered, env, logger), testNanoID1)
	if err == nil {
		t.Fatalf("a payload field must not be able to buy an exclusion from the refusal")
	}
	if code := hydrationErrCode(t, err); code != "DeriveReadsFloorContradiction" {
		t.Fatalf("code = %q, want DeriveReadsFloorContradiction", code)
	}
	if !strings.Contains(log.String(), "{payload.probe}") || !strings.Contains(log.String(), "excludes no key") {
		t.Fatalf("log = %q, want a Warn naming the payload-derived required template it refused to honour", log.String())
	}
}

// TestDeriveReads_DescriptorFloorReachesTheMergeThroughStep4 is the wiring
// proof the unit tests above cannot give: step 4 must build ONE floor and hand
// the same value to both arms. A merge that is handed nothing refuses nothing,
// and every assertion above would still pass.
func TestDeriveReads_DescriptorFloorReachesTheMergeThroughStep4(t *testing.T) {
	t.Parallel()
	hydrate := func(t *testing.T, optionalReads string) (HydratedState, error) {
		t.Helper()
		ctx, conn, _, _, _ := setupTestPipeline(t)
		seedDeriveDDL(t, ctx, conn,
			"def derive_reads(op):\n    return {\"reads\": [\""+deriveTestKeyA+"\"]}\n")
		root := "vtx.meta." + tplID
		seedOpMeta(t, ctx, conn, tplID, "CreateIdentity",
			dispatchAspectLists(root, `[]`, optionalReads, false), false)
		cache := NewDDLCache(conn, testCoreBucket, testLogger())
		if err := cache.Refresh(ctx); err != nil {
			t.Fatalf("refresh: %v", err)
		}
		h := NewHydratorWithCache(conn, testCoreBucket, cache, testLogger())
		return h.Hydrate(ctx, newTestEnvelope(testNanoID1))
	}

	t.Run("floored by the live descriptor", func(t *testing.T) {
		t.Parallel()
		_, err := hydrate(t, `["`+deriveTestKeyA+`"]`)
		if err == nil {
			t.Fatalf("the descriptor the cache holds must reach the merge")
		}
		if code := hydrationErrCode(t, err); code != "DeriveReadsFloorContradiction" {
			t.Fatalf("code = %q, want DeriveReadsFloorContradiction", code)
		}
		// The reply's own shape, through the real mapping the commit path uses:
		// neither details.missingKey nor the message may carry a key the
		// submitter could not have expressed.
		wireCode, details := classifyStepError(err)
		if wireCode != ErrCodeHydrationFailed {
			t.Fatalf("wire code = %v, want %v", wireCode, ErrCodeHydrationFailed)
		}
		if got := details["missingKey"]; got != "" {
			t.Fatalf("details.missingKey = %v, want empty — the derived key must not reach the caller", got)
		}
		if strings.Contains(err.Error(), deriveTestKeyA) {
			t.Fatalf("the reply message carries err.Error(); it must not quote the derived key: %v", err)
		}
	})

	t.Run("a descriptor naming other keys floors nothing", func(t *testing.T) {
		t.Parallel()
		state, err := hydrate(t, `["`+deriveTestKeyB+`"]`)
		if err != nil {
			t.Fatalf("a floor that does not cover the derived key must not fault it: %v", err)
		}
		if _, absent := state.Context.RequiredAbsent[deriveTestKeyA]; !absent {
			t.Fatalf("requiredAbsent = %v, want the derived key still fail-closed", state.Context.RequiredAbsent)
		}
	})
}

// TestDeriveReads_UnavailableBindingsAreFalsy pins the one surface that has no
// error channel and therefore must not CHANGE its answer.
//
// Truth returns a Bool; there is no way to raise from it. So the binding has to
// pick a silent answer, and the only safe pick is the one the empty mapping it
// stands in for already gave: falsy. Answering truthy would silently move every
// derivation that branches on `state` onto the other branch — an invisible
// wrong read set, which is the failure the binding exists to prevent, not a
// place to reintroduce it.
func TestDeriveReads_UnavailableBindingsAreFalsy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, expr string
		truthy     bool
	}{
		{"if state", "state", false},
		{"not state", "not state", true},
		{"bool(state)", "bool(state)", false},
		{"if ddl", "ddl", false},
		{"not ddl", "not ddl", true},
		{"bool(ddl)", "bool(ddl)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state, err := hydrateWithDerivation(t, `
def derive_reads(op):
    if `+tc.expr+`:
        return {"optionalReads": ["`+deriveTestKeyA+`"]}
    return {"optionalReads": ["`+deriveTestKeyB+`"]}
`, newTestEnvelope(testNanoID1))
			if err != nil {
				t.Fatalf("truthiness must not fault — there is no error channel to fault through: %v", err)
			}
			want, other := deriveTestKeyB, deriveTestKeyA
			if tc.truthy {
				want, other = deriveTestKeyA, deriveTestKeyB
			}
			if _, ok := state.Context.KnownAbsent[want]; !ok {
				t.Fatalf("%s took the wrong branch: knownAbsent=%v, want %q", tc.expr, state.Context.KnownAbsent, want)
			}
			if _, ok := state.Context.KnownAbsent[other]; ok {
				t.Fatalf("%s derived %q; the binding must answer exactly as the empty mapping it replaces", tc.expr, other)
			}
		})
	}
}

// TestDeriveReads_UnknownAttributeBehavesLikeNoSuchMember is the loudness
// floor on the attribute surface, and it runs in both directions.
//
// Attr must answer an unlisted name with "no such member", not with a value.
// Answering every name with a failing builtin would make `state.zzz` QUIETER
// than it is on any other Starlark value (a loud AttributeError becomes a
// builtin nobody called), and would break the two universe functions that
// decide a branch: go.starlark.net's getattr reaches its default only when Attr
// errors or returns nil, and hasattr reports whether Attr produced a value. The
// repo's dominant idiom is `getattr(x, field, None)`, so a truthy answer for a
// member that does not exist sends a derivation down the wrong branch with
// nothing raised — while the four real accessors must still raise when called.
func TestDeriveReads_UnknownAttributeBehavesLikeNoSuchMember(t *testing.T) {
	t.Parallel()

	t.Run("state.zzz is the interpreter's own AttributeError", func(t *testing.T) {
		t.Parallel()
		_, err := hydrateWithDerivation(t, `
def derive_reads(op):
    x = state.zzz
    return {}
`, newTestEnvelope(testNanoID1))
		if err == nil {
			t.Fatalf("an attribute the binding does not have must be as loud as on any other value")
		}
		if code := hydrationErrCode(t, err); code != "DeriveReadsFailed" {
			t.Fatalf("code = %q, want DeriveReadsFailed", code)
		}
		if !strings.Contains(err.Error(), "has no .zzz field or method") {
			t.Fatalf("want the AttributeError, got: %v", err)
		}
		if strings.Contains(err.Error(), "not available inside derive_reads") {
			t.Fatalf("an unlisted name must not resolve to the failing builtin: %v", err)
		}
	})

	// Each case branches on the universe function's answer, so the assertion is
	// about the value a derivation actually sees rather than about an error.
	for _, tc := range []struct {
		name, expr string
		truthy     bool
	}{
		{"getattr default for an unknown name", `getattr(state, "zzz", None) != None`, false},
		{"hasattr is False for an unknown name", `hasattr(state, "zzz")`, false},
		{"hasattr is True for a real accessor", `hasattr(state, "get")`, true},
		{"getattr resolves a real accessor", `getattr(state, "get", None) != None`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state, err := hydrateWithDerivation(t, `
def derive_reads(op):
    if `+tc.expr+`:
        return {"optionalReads": ["`+deriveTestKeyA+`"]}
    return {"optionalReads": ["`+deriveTestKeyB+`"]}
`, newTestEnvelope(testNanoID1))
			if err != nil {
				t.Fatalf("hasattr/getattr cannot raise; they must answer: %v", err)
			}
			want := deriveTestKeyB
			if tc.truthy {
				want = deriveTestKeyA
			}
			if _, ok := state.Context.KnownAbsent[want]; !ok {
				t.Fatalf("%s took the wrong branch: knownAbsent=%v, want %q", tc.expr, state.Context.KnownAbsent, want)
			}
		})
	}
}

// TestDeriveReads_UnavailableBindingRenderingAndHashing covers the two Value
// methods a derivation can reach that are neither an access nor a branch.
//
// String is the one that matters: this value lands in script-authored error
// text and in Go-side logs, and rendering as `{}` there asserts the very thing
// the binding refuses to say — that the pre-pass looked and found nothing.
// Hash keeps the binding out of a dict key, where it could otherwise be stored
// and compared later as though it were a value.
func TestDeriveReads_UnavailableBindingRenderingAndHashing(t *testing.T) {
	t.Parallel()

	t.Run("str() names the binding and never renders as an empty mapping", func(t *testing.T) {
		t.Parallel()
		_, err := hydrateWithDerivation(t, `
def derive_reads(op):
    fail("rendered:" + str(state))
`, newTestEnvelope(testNanoID1))
		if err == nil {
			t.Fatalf("fail() must surface the rendering")
		}
		if !strings.Contains(err.Error(), "rendered:<state unavailable inside derive_reads>") {
			t.Fatalf("want the binding rendered with its reason, got: %v", err)
		}
		if strings.Contains(err.Error(), "rendered:{}") {
			t.Fatalf("an empty-dict rendering is the misleading answer this binding exists to refuse: %v", err)
		}
	})

	t.Run("the binding is not hashable", func(t *testing.T) {
		t.Parallel()
		_, err := hydrateWithDerivation(t, `
def derive_reads(op):
    d = {state: 1}
    return {}
`, newTestEnvelope(testNanoID1))
		if err == nil {
			t.Fatalf("the binding must not be storable as a dict key")
		}
		if !strings.Contains(err.Error(), "not hashable") {
			t.Fatalf("want the unhashable refusal, got: %v", err)
		}
	})

	// AttrNames is load-bearing twice over: Attr is derived from it, so a name
	// missing here is an AttributeError rather than the fail-closed builtin, and
	// it must track the surface the MAIN pass exposes so an accessor added to
	// the hydrated mapping cannot land unstubbed in the pre-pass.
	t.Run("the attribute surface tracks the hydrated mapping's", func(t *testing.T) {
		t.Parallel()
		want := (&stateMapValue{}).AttrNames()
		got := failingMapping{name: "state"}.AttrNames()
		if !slices.Equal(got, want) {
			t.Fatalf("AttrNames = %v, want the hydrated mapping's %v", got, want)
		}
		for _, name := range want {
			v, err := failingMapping{name: "state"}.Attr(name)
			if err != nil || v == nil {
				t.Fatalf("Attr(%q) = (%v, %v), want the fail-closed builtin", name, v, err)
			}
		}
		v, err := failingMapping{name: "state"}.Attr("zzz")
		if v != nil || err != nil {
			t.Fatalf(`Attr("zzz") = (%v, %v), want (nil, nil) — "no such member"`, v, err)
		}
	})
}

// TestDeriveReads_EgressConflictOutranksTheFloorRefusal pins which of two
// terminal faults a key that trips both reports.
//
// Both are fail-closed, so the choice is about what the caller is told. The
// egress conflict names its key, and safely: that key came out of the
// ENVELOPE's own egressReads, so the submitter already holds it. The floor
// refusal deliberately names nothing. Reporting the egress arm first therefore
// gives the caller the actionable fault instead of an opaque one — and hoisting
// the floor refusal above it would silently swap a diagnosable rejection for a
// blank one.
func TestDeriveReads_EgressConflictOutranksTheFloorRefusal(t *testing.T) {
	t.Parallel()
	env := newTestEnvelope(testNanoID1)
	base := declaredReads{EgressReads: []string{deriveTestKeyA}}
	floored := DispatchTemplates{OptionalReads: []string{deriveTestKeyA}}

	_, err := mergeDerivedReads(base, derivedReads{Reads: []string{deriveTestKeyA}},
		newDescriptorFloorResolver(floored, env, testLogger()), testNanoID1)
	if err == nil {
		t.Fatalf("a key tripping both refusals must still fault")
	}
	if code := hydrationErrCode(t, err); code != "DeriveReadsEgressConflict" {
		t.Fatalf("code = %q, want DeriveReadsEgressConflict — the fault whose key the submitter already declared", code)
	}
}
