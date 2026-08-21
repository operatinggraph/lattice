package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/operatinggraph/lattice/internal/starlarksandbox"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

// HydratorImpl is the step-4 (JIT Hydrate) implementation. The DDL cache
// resolves canonicalName → MetaVertexRef (NanoID-keyed), exposing the
// meta-vertex's canonicalName, permittedCommands, script, and sensitivity
// flag in one map lookup.
//
// Responsibilities:
//  1. Resolve the operation's class — `class` envelope field first,
//     `payload.class` fallback. Missing class is a HydrationError.
//  2. Lookup the class in the DDL cache. Miss → NoDDLForClass.
//  3. Load the script source (carried on the cache entry). Empty
//     script → EmptyScript / NoScriptForClass depending on whether
//     the underlying DDL declared a script aspect at all.
//  4. Hydrate every key in envelope.contextHint.reads (known-key reads only;
//     a missing key is recorded *required-absent* and faults HydrationMiss
//     where the operation first touches it) and every key in
//     envelope.contextHint.optionalReads (absence-tolerant: a missing key
//     is recorded known-absent, Contract #2 §2.5 class (d)).
type HydratorImpl struct {
	Conn       *substrate.Conn
	CoreBucket string
	DDLs       *DDLCache
	Logger     *slog.Logger
	// DeriveBudget bounds one `derive_reads` pre-pass (Contract #2 §2.5
	// class (g)). It is sized against the SAME Init the step-5 pass pays,
	// not "well under" it: the pre-pass runs the module's whole top level,
	// because that is where the script's own helper functions are defined.
	// Zero uses the step-5 defaults.
	DeriveBudget starlarksandbox.Budget
	// Vault backs decrypt-on-read for sensitive aspects pulled into the
	// Starlark context (Contract #3 §3.10, the step-6.5 encrypt hook's read
	// counterpart). Nil disables decryption: a hydrated sensitive aspect's
	// data stays opaque ciphertext (the safe default for a pipeline that
	// never wires PII, e.g. most test harnesses). Production wiring
	// (MakePipeline) always sets it.
	Vault vault.Vault
	// PrimordialActors are the trusted platform engines' bootstrap-seeded
	// identity keys, keyed by engine name ("loom"). Carried verbatim onto every
	// ScriptContext this Hydrator builds, where they become the script's
	// `primordialActor` global (see primordialActorToStarlark). Production
	// wiring (MakePipeline, from AuthWiring) always sets it; unset binds the
	// empty string per name, which fails an actor comparison closed.
	PrimordialActors map[string]string
}

// NewHydrator wires a real Hydrator. The DDL cache parameter is
// optional for tests that exercise contextHint-only paths; production
// wiring always supplies a populated cache.
func NewHydrator(conn *substrate.Conn, coreBucket string, logger *slog.Logger) *HydratorImpl {
	return NewHydratorWithCache(conn, coreBucket, nil, logger)
}

// NewHydratorWithCache injects the DDL cache. Kept separate from NewHydrator
// so existing tests that exercise contextHint-only paths can omit the cache.
func NewHydratorWithCache(conn *substrate.Conn, coreBucket string, cache *DDLCache, logger *slog.Logger) *HydratorImpl {
	if conn == nil {
		panic("processor: NewHydrator requires Conn")
	}
	if coreBucket == "" {
		panic("processor: NewHydrator requires CoreBucket")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &HydratorImpl{Conn: conn, CoreBucket: coreBucket, DDLs: cache, Logger: logger}
}

// deriveBudget is the pre-pass's execution budget, defaulting to the step-5
// budgets when unset.
func (h *HydratorImpl) deriveBudget() starlarksandbox.Budget {
	b := h.DeriveBudget
	if b.Wall <= 0 {
		b.Wall = DefaultScriptWallBudget
	}
	if b.MaxSteps <= 0 {
		b.MaxSteps = DefaultScriptMaxSteps
	}
	return b
}

// Hydrate implements Hydrator.
func (h *HydratorImpl) Hydrate(ctx context.Context, env *OperationEnvelope) (HydratedState, error) {
	rid := env.RequestID

	// 1. Resolve class.
	class, err := resolveClass(env, h.DDLs)
	if err != nil {
		return HydratedState{}, &HydrationError{
			Code: "MissingClass", OperationRequestID: rid, Cause: err,
		}
	}

	// 2. Resolve DDL meta-vertex: prefer the DDL cache when wired.
	// Falls back to the shadow-key read so tests without a cache work.
	var (
		ddlKey   string
		metaVtx  MetaVertex
		source   string
		compiled *CompiledScript
	)
	if h.DDLs != nil {
		ref, ok := h.DDLs.Lookup(class)
		if !ok {
			return HydratedState{}, &HydrationError{
				Code: "NoDDLForClass", MissingKey: "vtx.meta.<" + class + ">", OperationRequestID: rid,
			}
		}
		ddlKey = ref.MetaVertexKey
		metaVtx = MetaVertex{
			Key:               ref.MetaVertexKey,
			CanonicalName:     ref.CanonicalName,
			PermittedCommands: ref.PermittedCommands,
		}
		source = ref.ScriptSource
		if strings.TrimSpace(source) == "" {
			return HydratedState{}, &HydrationError{
				Code: "NoScriptForClass", MissingKey: ref.MetaVertexKey + ".script", OperationRequestID: rid,
			}
		}
		// The cache-held compiled program: compiled at most once per cache
		// generation and shared by the derive_reads pre-pass below AND by
		// step 5's execute call.
		compiled = ref.Script
	} else {
		// Fallback: Story-1.6 shadow-key path.
		ddlKey = metaVertexKeyForClass(class)
		ddlEntry, err := h.Conn.KVGet(ctx, h.CoreBucket, ddlKey)
		if err != nil {
			if errors.Is(err, substrate.ErrKeyNotFound) {
				return HydratedState{}, &HydrationError{
					Code: "NoDDLForClass", MissingKey: ddlKey, OperationRequestID: rid,
				}
			}
			return HydratedState{}, fmt.Errorf("step4: read DDL %s: %w", ddlKey, err)
		}
		ddlDoc, err := parseVertexDoc(ddlEntry.Value, ddlKey)
		if err != nil {
			return HydratedState{}, fmt.Errorf("step4: parse DDL %s: %w", ddlKey, err)
		}
		metaVtx = MetaVertex{Key: ddlKey, CanonicalName: class}
		if pcAny, ok := ddlDoc.Data["permittedCommands"]; ok {
			if pcList, ok := pcAny.([]interface{}); ok {
				for _, c := range pcList {
					if s, ok := c.(string); ok {
						metaVtx.PermittedCommands = append(metaVtx.PermittedCommands, s)
					}
				}
			}
		}
		scriptKey := ddlKey + ".script"
		scriptEntry, err := h.Conn.KVGet(ctx, h.CoreBucket, scriptKey)
		if err != nil {
			if errors.Is(err, substrate.ErrKeyNotFound) {
				return HydratedState{}, &HydrationError{
					Code: "NoScriptForClass", MissingKey: scriptKey, OperationRequestID: rid,
				}
			}
			return HydratedState{}, fmt.Errorf("step4: read script %s: %w", scriptKey, err)
		}
		scriptDoc, err := parseVertexDoc(scriptEntry.Value, scriptKey)
		if err != nil {
			return HydratedState{}, fmt.Errorf("step4: parse script %s: %w", scriptKey, err)
		}
		source, _ = scriptDoc.Data["source"].(string)
		if strings.TrimSpace(source) == "" {
			return HydratedState{}, &HydrationError{
				Code: "EmptyScript", MissingKey: scriptKey, OperationRequestID: rid,
			}
		}
		// No cache to hold the program, so it is compiled per operation — the
		// cost step 5 already paid before the program was shareable. Both
		// passes still share this one, so the compile count does not rise.
		compiled = newCompiledScript(source)
	}

	// 3. Run the DDL's optional `derive_reads(op)` — Contract #2 §2.5 class
	// (g). It runs HERE: after step-3 authorization, before the first Core KV
	// GET, so a derived key is hydrated, snapshotted and OCC-conditioned
	// exactly like one the submitter declared. A DDL that declares none is
	// untouched — the entrypoint is read off the already-compiled program, so
	// the check costs no invocation.
	declared := declaredReadsFromEnvelope(env)
	// The descriptor floor is applied FIRST, to the envelope's own declaration
	// (Contract #2 §2.5, "the descriptor's disposition is a floor the envelope
	// cannot raise"). Before derive_reads, so mergeDerivedReads' "the
	// envelope's disposition stands" rule sees the DEMOTED set and a derived
	// `reads` entry cannot re-harden what the floor just softened. See
	// applyDescriptorFloor for the full precedence, and for why an
	// unresolvable descriptor demotes nothing rather than everything.
	if templates, hasDescriptor := h.DDLs.DispatchReadTemplates(env.OperationType); hasDescriptor {
		declared = applyDescriptorFloor(declared, templates, env, h.Logger)
	}
	if prog, ok := compiled.deriveReadsProgram(); ok {
		derived, err := deriveReads(ctx, prog, env, declared, h.deriveBudget(), h.PrimordialActors)
		if err != nil {
			return HydratedState{}, err
		}
		declared = derived
	}

	// 4. Hydrate contextHint.reads (fail-closed) + contextHint.optionalReads
	// (absence-tolerant) + contextHint.egressReads (fail-closed, ref-if-
	// sensitive) — Contract #2 §2.5 read posture. An `optionalReads` key that
	// is missing is recorded *known-absent* so kv.Read serves None from the
	// step-4 snapshot (the class-(d) read-before-create / dedup pattern) with
	// no live GET. A missing `reads` or `egressReads` key is recorded
	// *required-absent*: still fail-closed, but the HydrationMiss is raised
	// where the operation first touches the key rather than here.
	//
	// Recording rather than faulting is what keeps this loop from being an
	// existence oracle. contextHint is client-supplied and step 3 authorizes
	// without inspecting it, so a fault here would answer "does this key
	// exist?" for any actor holding any operation grant, over any key, before a
	// script runs. Touching the key requires naming it in the payload, which is
	// the path the operation's own guards stand on.
	// The three declared-read lists are hydrated from ONE atomic snapshot
	// (KVGetMulti), not three sequential per-key GET passes: a batched read
	// locks the whole matched set under the stream's read lock, so no key —
	// whichever list names it — can straddle a concurrent create or purge
	// the way three separate live GETs could.
	//
	// KVGetMulti treats an unrecognized string as a NATS subject FILTER, not
	// a rejected key — unlike the single-key KVGet path this replaces, which
	// incidentally rejected "*"/">" via nats.go's own key-charset check.
	// contextHint is client-supplied and step 3 authorizes without
	// inspecting it (see above), so every declared key is validated as a
	// well-formed Contract #1 key BEFORE it can reach the batched primitive:
	// a wildcard (or any other malformed string) here must fail loudly, not
	// silently turn one declared read into a full-bucket scan.
	readKeys := distinctKeys(declared.Reads)
	optionalReadKeys := distinctKeys(declared.OptionalReads)
	egressReadKeys := distinctKeys(declared.EgressReads)
	unionKeys := make([]string, 0, len(readKeys)+len(optionalReadKeys)+len(egressReadKeys))
	unionKeys = append(unionKeys, readKeys...)
	unionKeys = append(unionKeys, optionalReadKeys...)
	unionKeys = append(unionKeys, egressReadKeys...)
	unionKeys = distinctKeys(unionKeys)
	for _, key := range unionKeys {
		if substrate.ClassifyKey(key) == substrate.KindUnknown {
			return HydratedState{}, &HydrationError{
				Code: "InvalidReadKey", MissingKey: key, OperationRequestID: rid,
			}
		}
	}
	snapshot, err := h.Conn.KVGetMulti(ctx, h.CoreBucket, unionKeys)
	if err != nil {
		return HydratedState{}, fmt.Errorf("step4: get-multi (%d keys): %w", len(unionKeys), err)
	}

	hydrated := make(map[string]VertexDoc)
	var knownAbsent map[string]struct{}
	var requiredAbsent map[string]struct{}
	// A key may appear twice in one list, or across lists (nothing rejects
	// either). These two keep the disposition maps disjoint, with PRESENT
	// winning: a hydrated doc is a real read the script can serve, whereas a
	// stale absence would fault an operation whose key is sitting right
	// there in the working set. Every loop below resolves against the SAME
	// snapshot, so "present" and "absent" already agree for a repeated key
	// before these guards run — they restate the disjointness invariant,
	// not a race between reads. The "" guard at each loop head (distinctKeys'
	// blank filter) is what keeps the empty string out of requiredAbsent —
	// both the tracker and the mutation check use "" as their no-fault
	// sentinel.
	markRequiredAbsent := func(key string) {
		if _, present := hydrated[key]; present {
			return
		}
		if requiredAbsent == nil {
			requiredAbsent = map[string]struct{}{}
		}
		requiredAbsent[key] = struct{}{}
	}
	markHydrated := func(key string, doc VertexDoc) {
		hydrated[key] = doc
		delete(requiredAbsent, key)
	}
	tracker := &sensitiveReadTracker{}
	// memo backs the execution's governing-DDL resolution (Fire 1 Inc 2b) —
	// shared by pointer across every decrypt call below AND the script's
	// later lazy kv.Read seam (connKVReader), so a walk node reached by more
	// than one declared/lazy read this execution resolves once.
	memo := &ddlResolutionMemo{}
	var egressKeys map[string]struct{}
	for _, key := range readKeys {
		entry, ok := snapshot[key]
		if !ok {
			markRequiredAbsent(key)
			continue
		}
		doc, err := parseVertexDoc(entry.Value, key)
		if err != nil {
			return HydratedState{}, fmt.Errorf("step4: parse %s: %w", key, err)
		}
		doc.Revision = entry.Revision
		if err := decryptSensitiveDoc(ctx, h.Conn, h.CoreBucket, h.DDLs, h.Vault, &doc, false, tracker, rid, memo); err != nil {
			return HydratedState{}, fmt.Errorf("step4: decrypt %s: %w", key, err)
		}
		markHydrated(key, doc)
	}
	for _, key := range optionalReadKeys {
		// A key in both lists keeps the fail-closed `reads` semantics: it
		// either hydrated above or was recorded required-absent, so it is
		// never demoted to absence-tolerant by a duplicate optionalReads
		// entry — a demotion would let the weaker declaration decide, and
		// `optionalReads` must never soften a read the op depends on
		// (Contract #2 §2.5 authoring rule).
		if _, ok := hydrated[key]; ok {
			continue
		}
		if _, ok := requiredAbsent[key]; ok {
			continue
		}
		entry, ok := snapshot[key]
		if !ok {
			if knownAbsent == nil {
				knownAbsent = map[string]struct{}{}
			}
			knownAbsent[key] = struct{}{}
			continue
		}
		doc, err := parseVertexDoc(entry.Value, key)
		if err != nil {
			return HydratedState{}, fmt.Errorf("step4: parse %s: %w", key, err)
		}
		doc.Revision = entry.Revision
		if err := decryptSensitiveDoc(ctx, h.Conn, h.CoreBucket, h.DDLs, h.Vault, &doc, false, tracker, rid, memo); err != nil {
			return HydratedState{}, fmt.Errorf("step4: decrypt %s: %w", key, err)
		}
		markHydrated(key, doc)
	}
	for _, key := range egressReadKeys {
		if egressKeys == nil {
			egressKeys = map[string]struct{}{}
		}
		egressKeys[key] = struct{}{}
		// ParseEnvelope already rejects a key declared under egressReads
		// AND EITHER reads or optionalReads, and mergeDerivedReads re-runs
		// that exclusion over the derived keys parse could not see, so this
		// cannot re-hydrate an already-cached read (plaintext or
		// known-absent) under a weaker/conflicting disposition.
		if _, ok := hydrated[key]; ok {
			continue
		}
		entry, ok := snapshot[key]
		if !ok {
			// The descriptor floor reaches egressReads too, but ONLY here, at
			// absence (Contract #2 §2.5; applyDescriptorFloor's precedence
			// note 3). RequiredAbsent is a flat map that does not record which
			// list named a key, so an absent egress key faults HydrationMiss
			// with details.missingKey exactly as an absent `reads` key does —
			// the same oracle reached through the third list. A floored key
			// records known-absent instead. Its PRESENT behaviour is untouched
			// below: the ref disposition is never demoted, because handing the
			// script plaintext where the submitter asked for a bridge-opened
			// ref would be a worse trade than the fault it avoids.
			if _, tolerant := declared.EgressAbsenceTolerant[key]; tolerant {
				if knownAbsent == nil {
					knownAbsent = map[string]struct{}{}
				}
				knownAbsent[key] = struct{}{}
				continue
			}
			markRequiredAbsent(key)
			continue
		}
		doc, err := parseVertexDoc(entry.Value, key)
		if err != nil {
			return HydratedState{}, fmt.Errorf("step4: parse %s: %w", key, err)
		}
		doc.Revision = entry.Revision
		if err := decryptSensitiveDoc(ctx, h.Conn, h.CoreBucket, h.DDLs, h.Vault, &doc, true, tracker, rid, memo); err != nil {
			return HydratedState{}, fmt.Errorf("step4: decrypt %s: %w", key, err)
		}
		markHydrated(key, doc)
	}

	h.Logger.Info("step 4: hydrated",
		"requestId", rid,
		"class", class,
		"ddlKey", ddlKey,
		"contextHintCount", len(hydrated),
		"knownAbsentCount", len(knownAbsent),
		"requiredAbsentCount", len(requiredAbsent),
	)

	return HydratedState{
		Context: ScriptContext{
			Operation:      env,
			Hydrated:       hydrated,
			KnownAbsent:    knownAbsent,
			RequiredAbsent: requiredAbsent,
			DeferredMiss:   &deferredMissTracker{},
			DDLLookup:      map[string]MetaVertex{class: metaVtx},
			ScriptSource:   source,
			ScriptClass:    class,
			Compiled:       compiled,
			// Back the script's lazy kv.Read() (§2.5) with a single-key reader
			// over the same Conn + Core bucket used for hydration. A read of a
			// key not pre-fetched via contextHint falls through to this.
			KVReader: connKVReader{conn: h.Conn, bucket: h.CoreBucket, ddls: h.DDLs, vault: h.Vault, egressKeys: egressKeys, tracker: tracker, requestID: rid, memo: memo},
			// Back the script's kv.Links() (§2.5.1) with a bounded link lister
			// over the same Conn + Core bucket — the op-time set-valued enumeration.
			LinkLister:        connLinkLister{conn: h.Conn, bucket: h.CoreBucket},
			PrimordialActors:  h.PrimordialActors,
			SensitiveReads:    tracker,
			LiveReads:         &liveReadBudgetTracker{budget: DefaultLiveReadBudget},
			DDLResolutionMemo: memo,
		},
	}, nil
}

// distinctKeys returns the declared keys of one contextHint read class in
// declaration order, with blanks and repeats dropped.
//
// It is what makes the declared-read ceiling (opwire.MaxDeclaredReads) a bound
// on Core KV round trips rather than on mentions: nothing rejects a duplicate
// declaration and the lists are client-supplied, so a key named N times would
// otherwise cost N GETs — plus, for a sensitive class, N key-envelope GETs and
// N decrypts — from a declared set sitting well inside the ceiling. Resolving
// each key once is also the more consistent snapshot, since a second probe of
// the same key can straddle a concurrent create or purge and disagree with the
// first.
//
// The blank filter is load-bearing beyond tidiness: "" is the no-fault
// sentinel used by both the tracker and the mutation check, so it must never
// reach requiredAbsent.
func distinctKeys(declared []string) []string {
	if len(declared) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(declared))
	out := make([]string, 0, len(declared))
	for _, key := range declared {
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

// resolveClass extracts the operation's class for DDL lookup. Precedence:
//  1. the top-level `class` envelope field (explicit client hint),
//  2. the payload's `class` field (explicit client hint, legacy fallback),
//  3. the DDL cache's operationType→class reverse index (Contract #2 §2.1):
//     a dispatched op that omits `class` resolves to the single vertexType DDL
//     that admits its operationType. An ambiguous or unindexed operationType
//     misses here and the explicit-class requirement stands.
//
// The reverse-index step is auth-neutral: authorization (step 3) precedes class
// resolution (step 4) and keys on operationType + actor + authContext, never on
// class, so inferring the class cannot widen the auth surface. The inferred
// class is exactly the DDL whose permittedCommands admit the operationType — the
// same gate step 6 enforces — so it cannot run a wrong-script integrity
// mismatch either (the ambiguity guard rejects the >1-claimant case).
func resolveClass(env *OperationEnvelope, cache *DDLCache) (string, error) {
	if env.Class != "" {
		return env.Class, nil
	}
	if len(env.Payload) > 0 {
		var p map[string]json.RawMessage
		if err := json.Unmarshal(env.Payload, &p); err == nil {
			if raw, ok := p["class"]; ok {
				var s string
				if err := json.Unmarshal(raw, &s); err == nil && s != "" {
					return s, nil
				}
			}
		}
	}
	if cache != nil {
		if class, ok := cache.ClassForCommand(env.OperationType); ok {
			return class, nil
		}
	}
	return "", fmt.Errorf("operation envelope must carry a top-level `class` field (or payload.class)")
}

// metaVertexKeyForClass returns the shadow-key DDL path used when the DDL
// cache is not wired (test fallback). Canonical Contract #1 meta-vertices
// are keyed by NanoID; this form is for test fixtures keyed by class name.
func metaVertexKeyForClass(class string) string {
	return "vtx.meta." + class
}

// parseVertexDoc parses a Core KV value as a VertexDoc. The substrate
// stores documents as JSON.
func parseVertexDoc(data []byte, key string) (VertexDoc, error) {
	var doc VertexDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return VertexDoc{}, fmt.Errorf("unmarshal %s: %w", key, err)
	}
	doc.Key = key
	if doc.Data == nil {
		doc.Data = map[string]interface{}{}
	}
	return doc, nil
}
