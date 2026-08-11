package processor

import (
	"context"
	"fmt"
	"slices"

	starlarkjson "go.starlark.net/lib/json"
	starlarklib "go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/operatinggraph/lattice/internal/processor/opwire"
	"github.com/operatinggraph/lattice/internal/starlarksandbox"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// declaredReads is the effective declared-read set step 4 hydrates: the
// envelope's three classes plus whatever the owning DDL's `derive_reads`
// contributed (Contract #2 §2.5 class (g)).
//
// It is a value the Hydrator computes and consumes; the ENVELOPE is never
// rewritten. A derived key is not something the submitter said, so it must not
// come back out of the envelope as though it were: the reply, the audit trail,
// and every OCC retry read the envelope the client actually sent.
type declaredReads struct {
	Reads         []string
	OptionalReads []string
	EgressReads   []string
	// EgressAbsenceTolerant names the egressReads keys the descriptor floor
	// made absence-tolerant (Contract #2 §2.5). Step 4 acts on it in exactly
	// one place — a MISSING key is recorded known-absent instead of
	// required-absent — and never for a key that is PRESENT, which still
	// authors the `$sensitiveRef` the egress disposition promises.
	//
	// That one step-4 difference is not where its effect ends, because both
	// absence maps have readers downstream and a key is in one set or the
	// other. A floored key that turns out to be missing therefore also:
	//
	//   - stops being a write-side dependence. firstRequiredAbsentMutation
	//     (starlark_runner.go) faults a mutation that names — or writes an
	//     aspect or link onto — a REQUIRED-absent key, because
	//     applyHydratedRevisions leaves such a write unconditioned. A
	//     known-absent key is not in that set, so a script that writes it
	//     proceeds.
	//   - becomes eligible for the absent-conditioned create retry.
	//     absentConditionedCreates (commit_path.go) collects `create`
	//     mutations on KNOWN-absent keys, whose CreateOnly carries the step-4
	//     absence as its assertion, and the commit path re-probes exactly
	//     those on a conflict.
	//
	// Both are the ordinary treatment of a declared `optionalReads` key that
	// was not found, which is the point: the floor makes a floored egress key's
	// ABSENCE behave like an optional read's, and it changes nothing else.
	//
	// It is a separate set rather than a list move because the two egress
	// halves must part company: relocating the key into optionalReads would
	// hand the script decrypted plaintext where the submitter asked for a ref
	// the bridge opens.
	//
	// Lifetime: derived inside one Hydrate call, from that call's envelope and
	// the descriptor as the cache holds it at that instant. It is built by
	// applyDescriptorFloor at the head of step 4, read by the egress loop at
	// the end of it, and dropped with the declaredReads value. Nothing
	// persists it, and step 4 re-derives it on every OCC retry from the same
	// two inputs.
	EgressAbsenceTolerant map[string]struct{}
}

// declaredReadsFromEnvelope is the un-derived set — what step 4 hydrated
// before class (g) existed.
func declaredReadsFromEnvelope(env *OperationEnvelope) declaredReads {
	if env.ContextHint == nil {
		return declaredReads{}
	}
	return declaredReads{
		Reads:         env.ContextHint.Reads,
		OptionalReads: env.ContextHint.OptionalReads,
		EgressReads:   env.ContextHint.EgressReads,
	}
}

// deriveReads runs the DDL's optional `derive_reads(op)` entrypoint and merges
// its result into the envelope-declared set (Contract #2 §2.5 class (g)).
//
// It runs at the HEAD of step 4 — after step-3 authorization, before the first
// Core KV GET — so a derived key is hydrated, snapshotted and OCC-conditioned
// exactly like a key the submitter declared. Step 4 sits inside the OCC retry
// loop, so a pure derivation recomputes an identical set on every attempt;
// purity is what the stubs below enforce rather than request.
//
// A script that declares no derivation costs nothing: the caller resolves the
// entrypoint off the already-compiled program (deriveReadsProgram) and never
// reaches here.
//
// Every failure is a *HydrationError — terminal, fail-closed, and already
// mapped to ErrCodeHydrationFailed. A derivation that cannot be trusted must
// not silently degrade to "the submitter's set was all there was": that is the
// class-(b) undeclared read this whole posture exists to eliminate.
// base is the envelope's declared set AS THE CALLER HAS ALREADY ADJUSTED IT,
// never a fresh read of env.ContextHint, and that is load-bearing: step 4
// applies the descriptor floor (Contract #2 §2.5) to the envelope's own
// declaration before calling here, and re-deriving base from the envelope
// would discard that demotion — letting a derived `reads` entry re-harden a
// key the descriptor declared optional, by the very rule below that exists to
// stop derivation hardening anything.
func deriveReads(ctx context.Context, prog *starlarksandbox.Program, env *OperationEnvelope, base declaredReads, budget starlarksandbox.Budget) (declaredReads, error) {
	rid := env.RequestID

	// One op value, bound BOTH as the call argument and as the `op` global.
	// Building it twice re-parses the payload twice per derivation, per OCC
	// attempt, and hands the two bindings distinct structs for no reason — a
	// derivation comparing `op` against its own parameter should see one value.
	opValue := deriveReadsOpValue(env)
	out, sErr := starlarksandbox.Run(ctx, prog, deriveReadsEntrypoint,
		starlarklib.Tuple{opValue}, deriveReadsGlobals(opValue), budget)
	if sErr != nil {
		return declaredReads{}, &HydrationError{
			Code:               "DeriveReadsFailed",
			OperationRequestID: rid,
			Cause:              fmt.Errorf("derive_reads: %s", sErr.Message),
		}
	}

	derived, err := parseDerivedReads(out)
	if err != nil {
		return declaredReads{}, &HydrationError{
			Code: "DeriveReadsInvalid", OperationRequestID: rid,
			Cause: fmt.Errorf("derive_reads: %w", err),
		}
	}
	return mergeDerivedReads(base, derived, rid)
}

// deriveReadsGlobals binds the pre-pass's globals: the same NAME set the
// script was compiled against (scriptGlobalNames), with the impure modules
// replaced by stubs that fail when CALLED.
//
// Stubs, not unbound names. The sandbox resolves globals at compile time and
// the pre-pass shares the main pass's compiled program, so removing `kv` would
// not scope a restriction to this pass — it would fail to compile any module
// that mentions `kv` anywhere, killing every operation on that DDL.
//
//   - `kv` fails because a derivation that reads state is not a derivation, it
//     is a read, and a read must be declared like one. Letting it through would
//     also break the determinism the OCC retry loop depends on.
//   - `nanoid` fails because its PCG is seeded from the requestId
//     (nanoidModule): a `nanoid.new()` here would seed identically to the main
//     pass's module and hand step 5's first id away to the pre-pass.
//
// `state` is an empty mapping rather than the hydrated one, and truthfully so:
// the pre-pass runs BEFORE hydration, so there is no state to expose. `ddl` is
// likewise empty — the derivation's input is the op, per the contract's
// `derive_reads(op)` signature.
func deriveReadsGlobals(opValue *starlarkstruct.Struct) starlarklib.StringDict {
	return starlarklib.StringDict{
		"state":  starlarklib.NewDict(0),
		"op":     opValue,
		"ddl":    starlarklib.NewDict(0),
		"nanoid": failingModule("nanoid", []string{"new", "short"}),
		"crypto": cryptoModule(),
		"time":   timeModule(),
		"json":   starlarkjson.Module,
		"kv":     failingModule("kv", []string{"Read", "Links"}),
	}
}

// failingModule builds a module whose every member errors when called, naming
// the derivation so a package author sees why rather than a bare traceback.
//
// The members must EXIST (a missing attribute is an AttributeError that reads
// like a typo, and a script legitimately holding a reference to `kv.Read`
// without calling it on this path would break) and must fail on CALL.
func failingModule(module string, members []string) *starlarkstruct.Struct {
	dict := make(starlarklib.StringDict, len(members))
	for _, member := range members {
		name := member
		dict[name] = starlarklib.NewBuiltin(name, func(*starlarklib.Thread, *starlarklib.Builtin, starlarklib.Tuple, []starlarklib.Tuple) (starlarklib.Value, error) {
			return nil, fmt.Errorf("%s.%s is not available inside %s: a derivation that reads state is a read, and must be declared as one",
				module, name, deriveReadsEntrypoint)
		})
	}
	return starlarkstruct.FromStringDict(starlarkstruct.Default, dict)
}

// deriveReadsOpValue is the `op` argument the contract specifies:
// {operationType, actor, payload}. Deliberately narrower than the step-5 `op`
// struct — a derivation is a function of what the submitter sent, not of the
// request's identity (requestId seeds the nanoid PCG) or of step-3's auth
// outcome (which would make the derived read set vary by grant).
func deriveReadsOpValue(env *OperationEnvelope) *starlarkstruct.Struct {
	payloadFields := starlarklib.StringDict{}
	if len(env.Payload) > 0 {
		if m, ok := jsonToGenericMap(env.Payload); ok {
			for k, v := range m {
				payloadFields[k] = goValueToStarlark(v)
			}
		}
	}
	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlarklib.StringDict{
		"operationType": starlarklib.String(env.OperationType),
		"actor":         starlarklib.String(env.Actor),
		"payload":       starlarkstruct.FromStringDict(starlarkstruct.Default, payloadFields),
	})
}

// derivedReads is one derivation's raw output, before merging.
type derivedReads struct {
	Reads         []string
	OptionalReads []string
}

// parseDerivedReads reads the {"reads": [...], "optionalReads": [...]} return
// shape. Both keys are optional; an empty return is a legitimate "this payload
// derives nothing" (the `if not name: return {}` branch the contract's own
// example takes).
//
// Every entry is checked against the Contract #1 key grammar HERE rather than
// at hydration, so a malformed derived key is attributed to the derivation
// that produced it instead of surfacing later as a Core KV miss on a key
// nobody declared.
func parseDerivedReads(val starlarklib.Value) (derivedReads, error) {
	if val == starlarklib.None {
		return derivedReads{}, nil
	}
	d, ok := val.(*starlarklib.Dict)
	if !ok {
		return derivedReads{}, fmt.Errorf("must return a dict, got %s", val.Type())
	}
	for _, item := range d.Items() {
		k, ok := item[0].(starlarklib.String)
		if !ok || (string(k) != "reads" && string(k) != "optionalReads") {
			return derivedReads{}, fmt.Errorf("return permits only the 'reads' and 'optionalReads' keys, got %s", item[0].String())
		}
	}
	reads, err := derivedKeyList(d, "reads")
	if err != nil {
		return derivedReads{}, err
	}
	optional, err := derivedKeyList(d, "optionalReads")
	if err != nil {
		return derivedReads{}, err
	}
	return derivedReads{Reads: reads, OptionalReads: optional}, nil
}

func derivedKeyList(d *starlarklib.Dict, field string) ([]string, error) {
	raw, found, _ := d.Get(starlarklib.String(field))
	if !found || raw == starlarklib.None {
		return nil, nil
	}
	list, ok := raw.(*starlarklib.List)
	if !ok {
		return nil, fmt.Errorf("'%s' must be a list, got %s", field, raw.Type())
	}
	out := make([]string, 0, list.Len())
	for i := 0; i < list.Len(); i++ {
		s, ok := list.Index(i).(starlarklib.String)
		if !ok {
			return nil, fmt.Errorf("'%s'[%d] must be a string, got %s", field, i, list.Index(i).Type())
		}
		key := string(s)
		if substrate.ClassifyKey(key) == substrate.KindUnknown {
			return nil, fmt.Errorf("'%s'[%d] = %q is not a Contract #1 key (3-segment vertex, 4-segment aspect, or 6-segment link)", field, i, key)
		}
		out = append(out, key)
	}
	return out, nil
}

// mergeDerivedReads folds a derivation's output into the envelope-declared
// set under the three rules Contract #2 §2.5 states for class (g).
//
// WEAKEST WINS. A derived key that collides with one the envelope already
// declared keeps the ENVELOPE's disposition. Hardening a declared
// `optionalReads` key into a fail-closed `reads` one would fault
// `HydrationMiss` on exactly the read-before-create branch class (d) exists
// for — the dedup path is the main consumer of class (g), so this is not a
// corner case, it is the common one. The rule applies to a derived/derived
// collision too: the contract states it only against the envelope, but a key
// appearing in both derived lists is the same ambiguity with the same
// hardening hazard, and resolving it the other way would make a derivation's
// behaviour depend on list order.
//
// EGRESS EXCLUSION RE-CHECKED. ParseEnvelope rejects a key declared under
// `egressReads` and also under reads/optionalReads, because the plaintext
// hydration loop runs first and would silently demote the egress disposition.
// Derivation happens after parse, so the same collision has to be caught here
// — and it faults naming the derivation, rather than surfacing as an opaque
// step-6 external-egress rejection.
//
// CEILING COUNTED, FAULTED AT RUNTIME. Derived keys count toward
// opwire.MaxDeclaredReads. The count is of DISTINCT keys, matching
// distinctKeys' existing semantics: the ceiling has always bounded Core KV
// round trips rather than mentions, so a derived duplicate of a declared key
// must not consume the budget twice. A breach is a step-4 fault and not
// `EnvelopeMalformed` — the keys are not envelope-supplied, so rejecting the
// envelope would blame the submitter for the package's derivation.
func mergeDerivedReads(base declaredReads, derived derivedReads, rid string) (declaredReads, error) {
	if len(derived.Reads) == 0 && len(derived.OptionalReads) == 0 {
		return base, nil
	}

	declared := make(map[string]struct{}, len(base.Reads)+len(base.OptionalReads))
	for _, k := range base.Reads {
		declared[k] = struct{}{}
	}
	for _, k := range base.OptionalReads {
		declared[k] = struct{}{}
	}
	egress := make(map[string]struct{}, len(base.EgressReads))
	for _, k := range base.EgressReads {
		egress[k] = struct{}{}
	}

	// Optional first, so weakest-wins holds for a derived/derived collision:
	// a key in both derived lists is claimed here and skipped below.
	//
	// The two lists are CLONED before anything is appended. `merged := base`
	// alone copies the slice headers, so an append lands in the envelope's own
	// JSON-decoded backing array whenever that array has spare capacity —
	// writing a derived key into storage the envelope owns, which is exactly
	// what this file's header promises never happens. Harmless in today's
	// sequential, pure-derivation path, but the invariant is stated as
	// absolute, so it is enforced rather than argued.
	merged := base
	merged.Reads = slices.Clone(base.Reads)
	merged.OptionalReads = slices.Clone(base.OptionalReads)
	claimed := map[string]struct{}{}
	appendDerived := func(keys []string, dst *[]string) error {
		for _, key := range keys {
			if key == "" {
				continue
			}
			if _, dup := egress[key]; dup {
				return &HydrationError{
					Code: "DeriveReadsEgressConflict", MissingKey: key, OperationRequestID: rid,
					Cause: fmt.Errorf("derive_reads returned %q, which the envelope declared in egressReads (ambiguous disposition)", key),
				}
			}
			if _, dup := declared[key]; dup {
				continue // envelope's disposition stands
			}
			if _, dup := claimed[key]; dup {
				continue // already taken by the weaker derived list
			}
			claimed[key] = struct{}{}
			*dst = append(*dst, key)
		}
		return nil
	}
	if err := appendDerived(derived.OptionalReads, &merged.OptionalReads); err != nil {
		return declaredReads{}, err
	}
	if err := appendDerived(derived.Reads, &merged.Reads); err != nil {
		return declaredReads{}, err
	}

	if total := len(distinctAcrossClasses(merged)); total > opwire.MaxDeclaredReads {
		return declaredReads{}, &HydrationError{
			Code: "DeclaredReadCeilingExceeded", OperationRequestID: rid,
			Cause: fmt.Errorf("derive_reads: merged declared set is %d keys across reads/optionalReads/egressReads, exceeding the %d ceiling",
				total, opwire.MaxDeclaredReads),
		}
	}
	return merged, nil
}

// distinctAcrossClasses returns the distinct keys of a merged declared set.
func distinctAcrossClasses(d declaredReads) map[string]struct{} {
	out := make(map[string]struct{}, len(d.Reads)+len(d.OptionalReads)+len(d.EgressReads))
	for _, list := range [][]string{d.Reads, d.OptionalReads, d.EgressReads} {
		for _, k := range list {
			if k == "" {
				continue
			}
			out[k] = struct{}{}
		}
	}
	return out
}
