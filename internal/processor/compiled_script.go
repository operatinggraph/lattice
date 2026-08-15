package processor

import (
	"sync"

	starlarkjson "go.starlark.net/lib/json"
	starlarklib "go.starlark.net/starlark"

	"github.com/operatinggraph/lattice/internal/starlarksandbox"
)

// scriptGlobalNames is the predeclared-name set every DDL script is compiled
// against — the single declaration both passes over a script derive their
// globals from.
//
// It is one list rather than two dicts because the sandbox resolves globals at
// COMPILE time: a program compiled against one name set and Init'd with
// another is a resolve mismatch, and one compiled program now serves both the
// step-4 `derive_reads` pre-pass and the step-5 `execute` call. The two passes
// bind DIFFERENT VALUES under these names (the pre-pass stubs `kv`/`nanoid`
// and has no hydrated state), which is exactly the point — same names, same
// resolution, different bindings.
//
// scriptGlobalsAgreeWithNames in the tests pins the runner's dict against this
// list, because drift would not fail at build time: it would surface as an
// obscure failure on a live operation.
var scriptGlobalNames = []string{
	"state", "op", "ddl", "nanoid", "crypto", "time", "json", "kv",
	"primordialActor",
}

// scriptGlobalNameSet is scriptGlobalNames as the predeclared-name probe
// Compile takes.
var scriptGlobalNameSet = func() map[string]struct{} {
	s := make(map[string]struct{}, len(scriptGlobalNames))
	for _, n := range scriptGlobalNames {
		s[n] = struct{}{}
	}
	return s
}()

func isScriptGlobal(name string) bool {
	_, ok := scriptGlobalNameSet[name]
	return ok
}

// deriveReadsEntrypoint is the optional top-level function a DDL script
// defines to declare its class-(g) script-derived reads (Contract #2 §2.5).
const deriveReadsEntrypoint = "derive_reads"

// CompiledScript is one DDL script's compiled program, shared by every pass
// over that script and by every operation that runs it.
//
// It exists because the same source is now compiled for two different
// purposes — the step-4 `derive_reads` pre-pass and the step-5 `execute`
// call — and a compile is not cheap: identity-domain's DDL is ~960 lines, and
// step 4 sits inside the OCC retry loop. Compiling per pass per attempt would
// multiply the platform's most expensive script by four for the ops that
// declare a derivation.
//
// The compile is LAZY and happens at most once per cache generation: the DDL
// cache builds one of these per meta-vertex at load and replaces it on
// Invalidate, so an edited script recompiles and a stale program can never
// outlive the source it was compiled from. A compile FAILURE is cached
// alongside the program — a broken script must fail every operation
// identically, not re-pay the compile to fail again.
//
// Concurrency: the compile is guarded by sync.Once, and the resulting
// *starlarksandbox.Program holds no execution state, so concurrent operations
// share one program safely.
type CompiledScript struct {
	source string

	once sync.Once
	prog *starlarksandbox.Program
	err  *starlarksandbox.SandboxError
}

// newCompiledScript wraps a script source. The compile is deferred to the
// first program() call, so wrapping a source a caller never runs costs
// nothing — which is what lets the DDL cache build one of these for every
// meta-vertex at load without compiling the whole corpus.
func newCompiledScript(source string) *CompiledScript {
	return &CompiledScript{source: source}
}

// program compiles on first call and returns the shared program thereafter.
func (c *CompiledScript) program() (*starlarksandbox.Program, *starlarksandbox.SandboxError) {
	c.once.Do(func() {
		c.prog, c.err = starlarksandbox.Compile(c.source, isScriptGlobal)
	})
	return c.prog, c.err
}

// compiledFor returns the program compiled from source, reusing cached when it
// was compiled from that same source and compiling fresh when it was not.
//
// The source string, not the carried program, is the authority. A
// ScriptContext carries BOTH a ScriptSource and the program step 4 compiled,
// and nothing stops a caller from replacing one without the other — test
// harnesses do exactly that, hydrating against a fixture DDL and then running
// an inline script through the resulting context. Preferring the carried
// program there would silently run the script the caller did NOT ask for,
// which is the worst possible way for these two fields to disagree.
func compiledFor(cached *CompiledScript, source string) *CompiledScript {
	if cached != nil && cached.source == source {
		return cached
	}
	return newCompiledScript(source)
}

// deriveReadsProgram returns the compiled program when this script declares
// the optional `derive_reads` entrypoint, and ok=false when it does not.
//
// It answers "is there a derivation, and what runs it?" in ONE call rather
// than two, so there is no state between the question and the answer for a
// caller to get wrong — and no second code path for a compile failure to take.
//
// A script that fails to COMPILE reports ok=false: the pre-pass is skipped and
// the compile error surfaces where it always has, at step 5, with the line and
// column a script author needs — rather than being re-attributed to a
// derivation the script may not even declare.
func (c *CompiledScript) deriveReadsProgram() (*starlarksandbox.Program, bool) {
	if c == nil {
		return nil, false
	}
	prog, err := c.program()
	if err != nil {
		return nil, false
	}
	if !prog.DefinesTopLevel(deriveReadsEntrypoint) {
		return nil, false
	}
	return prog, true
}

// scriptGlobals builds the step-5 global bindings. Its key set is
// scriptGlobalNames by construction.
func scriptGlobals(sc ScriptContext, stateVal starlarklib.Value, opVal starlarklib.Value) starlarklib.StringDict {
	return starlarklib.StringDict{
		"state":  stateVal,
		"op":     opVal,
		"ddl":    ddlMapToStarlark(sc.DDLLookup),
		"nanoid": nanoidModule(sc.Operation.RequestID),
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
		// primordialActor["<engine>"] — the bootstrap-seeded identity key of a
		// trusted platform engine (see primordialActorToStarlark). Pure, frozen
		// data, identical for every operation in the process: an op whose grant
		// is `Scope:"any"` but whose semantics assume ONE engine submitter pins
		// `op.actor` against it before touching the subject its payload names.
		"primordialActor": primordialActorToStarlark(sc.PrimordialActors),
	}
}
