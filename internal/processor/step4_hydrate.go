package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/vault"
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
//  4. Hydrate every key in envelope.contextHint.reads (known-key reads
//     only; a missing key faults HydrationMiss) and every key in
//     envelope.contextHint.optionalReads (absence-tolerant: a missing key
//     is recorded known-absent, Contract #2 §2.5 class (d)).
type HydratorImpl struct {
	Conn       *substrate.Conn
	CoreBucket string
	DDLs       *DDLCache
	Logger     *slog.Logger
	// Vault backs decrypt-on-read for sensitive aspects pulled into the
	// Starlark context (Contract #3 §3.10, the step-6.5 encrypt hook's read
	// counterpart). Nil disables decryption: a hydrated sensitive aspect's
	// data stays opaque ciphertext (the safe default for a pipeline that
	// never wires PII, e.g. most test harnesses). Production wiring
	// (MakePipeline) always sets it.
	Vault vault.Vault
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
		ddlKey  string
		metaVtx MetaVertex
		source  string
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
	}

	// 4. Hydrate contextHint.reads (fail-closed) + contextHint.optionalReads
	// (absence-tolerant) — Contract #2 §2.5 read posture. A `reads` key that is
	// missing faults HydrationMiss; an `optionalReads` key that is missing is
	// recorded *known-absent* so kv.Read serves None from the step-4 snapshot
	// (the class-(d) read-before-create / dedup pattern) with no live GET.
	hydrated := make(map[string]VertexDoc)
	var knownAbsent map[string]struct{}
	if env.ContextHint != nil {
		for _, key := range env.ContextHint.Reads {
			if key == "" {
				continue
			}
			entry, err := h.Conn.KVGet(ctx, h.CoreBucket, key)
			if err != nil {
				if errors.Is(err, substrate.ErrKeyNotFound) {
					return HydratedState{}, &HydrationError{
						Code: "HydrationMiss", MissingKey: key, OperationRequestID: rid,
					}
				}
				return HydratedState{}, fmt.Errorf("step4: read %s: %w", key, err)
			}
			doc, err := parseVertexDoc(entry.Value, key)
			if err != nil {
				return HydratedState{}, fmt.Errorf("step4: parse %s: %w", key, err)
			}
			doc.Revision = entry.Revision
			if err := decryptSensitiveDoc(ctx, h.Conn, h.CoreBucket, h.DDLs, h.Vault, &doc); err != nil {
				return HydratedState{}, fmt.Errorf("step4: decrypt %s: %w", key, err)
			}
			hydrated[key] = doc
		}
		for _, key := range env.ContextHint.OptionalReads {
			if key == "" {
				continue
			}
			// A key in both lists keeps the fail-closed `reads` semantics: it
			// either hydrated above or already faulted, so it is never demoted
			// to absence-tolerant by a duplicate optionalReads entry.
			if _, ok := hydrated[key]; ok {
				continue
			}
			// A duplicate optionalReads entry already probed absent: skip the
			// second live GET (nil-map read is safe).
			if _, ok := knownAbsent[key]; ok {
				continue
			}
			entry, err := h.Conn.KVGet(ctx, h.CoreBucket, key)
			if err != nil {
				if errors.Is(err, substrate.ErrKeyNotFound) {
					if knownAbsent == nil {
						knownAbsent = map[string]struct{}{}
					}
					knownAbsent[key] = struct{}{}
					continue
				}
				return HydratedState{}, fmt.Errorf("step4: read %s: %w", key, err)
			}
			doc, err := parseVertexDoc(entry.Value, key)
			if err != nil {
				return HydratedState{}, fmt.Errorf("step4: parse %s: %w", key, err)
			}
			doc.Revision = entry.Revision
			if err := decryptSensitiveDoc(ctx, h.Conn, h.CoreBucket, h.DDLs, h.Vault, &doc); err != nil {
				return HydratedState{}, fmt.Errorf("step4: decrypt %s: %w", key, err)
			}
			hydrated[key] = doc
		}
	}

	h.Logger.Info("step 4: hydrated",
		"requestId", rid,
		"class", class,
		"ddlKey", ddlKey,
		"contextHintCount", len(hydrated),
		"knownAbsentCount", len(knownAbsent),
	)

	return HydratedState{
		Context: ScriptContext{
			Operation:    env,
			Hydrated:     hydrated,
			KnownAbsent:  knownAbsent,
			DDLLookup:    map[string]MetaVertex{class: metaVtx},
			ScriptSource: source,
			ScriptClass:  class,
			// Back the script's lazy kv.Read() (§2.5) with a single-key reader
			// over the same Conn + Core bucket used for hydration. A read of a
			// key not pre-fetched via contextHint falls through to this.
			KVReader: connKVReader{conn: h.Conn, bucket: h.CoreBucket, ddls: h.DDLs, vault: h.Vault},
			// Back the script's kv.Links() (§2.5.1) with a bounded link lister
			// over the same Conn + Core bucket — the op-time set-valued enumeration.
			LinkLister: connLinkLister{conn: h.Conn, bucket: h.CoreBucket},
		},
	}, nil
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
