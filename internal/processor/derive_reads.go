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
//
// floor is the same resolved descriptor floor that demotion ran against, and
// it is passed rather than re-resolved for the same reason base is: the two
// arms must agree about which keys the descriptor calls absence-tolerant. It
// covers the case demotion structurally cannot — a key the envelope never
// declared. Nil where the operation has no descriptor, which floors nothing.
func deriveReads(ctx context.Context, prog *starlarksandbox.Program, env *OperationEnvelope, base declaredReads, floor *descriptorFloorResolver, budget starlarksandbox.Budget, primordialActors map[string]string) (declaredReads, error) {
	rid := env.RequestID

	// One op value, bound BOTH as the call argument and as the `op` global.
	// Building it twice re-parses the payload twice per derivation, per OCC
	// attempt, and hands the two bindings distinct structs for no reason — a
	// derivation comparing `op` against its own parameter should see one value.
	opValue := deriveReadsOpValue(env)
	out, sErr := starlarksandbox.Run(ctx, prog, deriveReadsEntrypoint,
		starlarklib.Tuple{opValue}, deriveReadsGlobals(opValue, primordialActors), budget)
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
	return mergeDerivedReads(base, derived, floor, rid)
}

// deriveReadsGlobals binds the pre-pass's globals: the same NAME set the
// script was compiled against (scriptGlobalNames), with everything the
// derivation must not reach replaced by a binding that fails on ACCESS.
//
// Stubs, not unbound names. The sandbox resolves globals at compile time and
// the pre-pass shares the main pass's compiled program, so removing `kv` would
// not scope a restriction to this pass — it would fail to compile any module
// that mentions `kv` anywhere, killing every operation on that DDL. The same
// holds for `state` and `ddl`: every name stays bound, and only reaching into
// one fails.
//
//   - `kv` fails because a derivation that reads state is not a derivation, it
//     is a read, and a read must be declared like one. Letting it through would
//     also break the determinism the OCC retry loop depends on.
//   - `nanoid` fails because its PCG is seeded from the requestId
//     (nanoidModule): a `nanoid.new()` here would seed identically to the main
//     pass's module and hand step 5's first id away to the pre-pass.
//   - `state` and `ddl` fail for `kv`'s reason, and they FAIL rather than bind
//     empty. The pre-pass runs before hydration, so there is nothing to expose
//     — but an empty mapping ANSWERS the question instead of refusing it:
//     `state.get(k)` is None, `k in state` is False, `len(state)` is 0. A
//     derivation reaching for hydrated state would then derive a wrong read set
//     with nothing on the wire to say so, which is the class-(b) undeclared read
//     this whole posture exists to eliminate, wearing a miss as a disguise.
//
// `primordialActor` binds its REAL values, unlike `state`/`ddl`: it is process
// configuration, not hydration output, so it is as available before hydration
// as `crypto` or `time` and binding an empty stand-in would be the only
// untruthful entry in the dict.
func deriveReadsGlobals(opValue *starlarkstruct.Struct, primordialActors map[string]string) starlarklib.StringDict {
	return starlarklib.StringDict{
		"state":           failingMapping{name: "state"},
		"op":              opValue,
		"ddl":             failingMapping{name: "ddl"},
		"nanoid":          failingModule("nanoid", []string{"new", "short"}),
		"crypto":          cryptoModule(),
		"time":            timeModule(),
		"json":            starlarkjson.Module,
		"kv":              failingModule("kv", []string{"Read", "Links"}),
		"primordialActor": primordialActorToStarlark(primordialActors),
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

// failingMapping binds a mapping-shaped global the pre-pass cannot honestly
// answer, so that every way of reaching into it raises the reason instead of
// reporting a miss.
//
// It mirrors failingModule's POSTURE, not its type. A module is only reachable
// by calling one of its members, so failing on call covers it; a MAPPING is
// reached by subscript, by membership, by attribute and by iteration, and none
// of those is a call. Each surface therefore fails on its own terms:
//
//   - starlark.Mapping — `state[k]`. getIndex propagates Get's error verbatim.
//   - starlark.Container — `k in state`. The interpreter's `in` matches
//     Container BEFORE Mapping and returns Has's error, where the Mapping arm
//     DISCARDS Get's error ("we cannot distinguish true errors from key not
//     found") and answers a bare False. A mapping that is not also a Container
//     is therefore silent for `in` however loudly Get fails, which is the one
//     access this type most has to refuse: `if k in state` is how a script asks
//     whether a key was hydrated.
//   - starlark.HasAttrs — `state.get(k)`, `.keys()`, `.items()`, `.values()`.
//     Attr answers ANY name with a builtin that fails when called, on the same
//     reasoning failingModule's members exist: an attribute that is simply
//     missing is an AttributeError that reads like a typo. AttrNames reports
//     the read accessors a hydrated mapping carries (stateAttrs), so `dir()`
//     describes the surface the main pass has rather than a bare struct.
//
// Iteration is deliberately NOT implemented. `for k in state` then raises the
// interpreter's own "<type> value is not iterable" — which is why Type() spells
// the reason out, since that message is built from it. An Iterator cannot
// return an error, so implementing Iterable could only yield nothing, and
// yielding nothing is precisely the silent answer this type exists to refuse.
//
// Truth() answers True for the same reason. `if state:` cannot fail — Truth has
// no error to return — so the binding takes the branch that goes on to touch
// the mapping and fails there, rather than the empty-dict branch that quietly
// derives nothing.
type failingMapping struct {
	// name is the global's name, so an error names what the script reached for
	// rather than an anonymous mapping.
	name string
}

// unavailable is the one message every surface raises; access renders the
// expression the script wrote, so the author sees their own syntax back.
func (m failingMapping) unavailable(access string) error {
	return fmt.Errorf("%s is not available inside %s: the pre-pass runs before hydration, and a derivation that reads state is a read, and must be declared as one",
		access, deriveReadsEntrypoint)
}

func (m failingMapping) String() string {
	return "<" + m.name + " unavailable inside " + deriveReadsEntrypoint + ">"
}

// Type carries the reason because the interpreter builds its own
// not-iterable and no-len messages out of it, and those are the surfaces this
// type cannot supply a message for itself.
func (m failingMapping) Type() string {
	return m.name + "-unavailable-inside-" + deriveReadsEntrypoint
}

func (m failingMapping) Freeze()                 {}
func (m failingMapping) Truth() starlarklib.Bool { return starlarklib.True }
func (m failingMapping) Hash() (uint32, error)   { return 0, fmt.Errorf("%s is not hashable", m.name) }
func (m failingMapping) AttrNames() []string     { return stateAttrs }
func (m failingMapping) Has(k starlarklib.Value) (bool, error) {
	return false, m.unavailable(k.String() + " in " + m.name)
}

func (m failingMapping) Get(k starlarklib.Value) (starlarklib.Value, bool, error) {
	return nil, false, m.unavailable(m.name + "[" + k.String() + "]")
}

func (m failingMapping) Attr(name string) (starlarklib.Value, error) {
	access := m.name + "." + name
	return starlarklib.NewBuiltin(name, func(*starlarklib.Thread, *starlarklib.Builtin, starlarklib.Tuple, []starlarklib.Tuple) (starlarklib.Value, error) {
		return nil, m.unavailable(access)
	}), nil
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
// THE DESCRIPTOR FLOOR REFUSES A DERIVED REQUIREMENT IT CONTRADICTS. A key the
// envelope never declared reaches here with no disposition to defer to, so the
// weakest-wins rule above has no subject for it and applyDescriptorFloor never
// saw it — a derived `reads` key the operation's own descriptor lists under
// `optionalReads` would otherwise be appended fail-closed, out from under the
// floor every envelope-declared key is held to.
//
// It is REFUSED, not demoted. Two authorities inside ONE package disagree about
// one key: the DDL's derivation says the operation depends on it, the same
// package's descriptor says its absence is ordinary. Demoting picks the
// descriptor and turns the HydrationMiss the script's author demanded into a
// silent None — the dangerous direction, and the same reasoning that makes the
// floor refuse to demote on doubt (descriptor_floor.go, "direction of
// failure"). So the operation faults closed naming the derivation, exactly as
// the egress collision above does, and the package fixes its own contradiction.
//
// A submitter CAN provoke this by steering a `{payload.<field>}` optionalReads
// template onto a derived key. That is a self-DoS on their own operation in the
// fail-closed direction, never a bypass: the exclusion that would suppress the
// refusal is the one resolveDescriptorRequired builds, and it refuses
// payload-derived and pattern-shaped templates precisely so a request cannot
// address it.
//
// CEILING COUNTED, FAULTED AT RUNTIME. Derived keys count toward
// opwire.MaxDeclaredReads. The count is of DISTINCT keys, matching
// distinctKeys' existing semantics: the ceiling has always bounded Core KV
// round trips rather than mentions, so a derived duplicate of a declared key
// must not consume the budget twice. A breach is a step-4 fault and not
// `EnvelopeMalformed` — the keys are not envelope-supplied, so rejecting the
// envelope would blame the submitter for the package's derivation.
func mergeDerivedReads(base declaredReads, derived derivedReads, floor *descriptorFloorResolver, rid string) (declaredReads, error) {
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
	appendDerived := func(keys []string, dst *[]string, failClosed bool) error {
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
			// The floor is asked only about a key that would land fail-closed
			// and is genuinely new. A key the OPTIONAL list already claimed has
			// taken the weaker disposition by weakest-wins and contradicts
			// nothing; a key the envelope declared kept the envelope's
			// disposition and was floored on the envelope's own pass.
			if failClosed && floor.floored(key) {
				return &HydrationError{
					Code: "DeriveReadsFloorContradiction", MissingKey: key, OperationRequestID: rid,
					Cause: fmt.Errorf("derive_reads returned %q under reads, which this operation's own descriptor declares absence-tolerant under optionalReads (the package contradicts itself)", key),
				}
			}
			claimed[key] = struct{}{}
			*dst = append(*dst, key)
		}
		return nil
	}
	if err := appendDerived(derived.OptionalReads, &merged.OptionalReads, false); err != nil {
		return declaredReads{}, err
	}
	if err := appendDerived(derived.Reads, &merged.Reads, true); err != nil {
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
