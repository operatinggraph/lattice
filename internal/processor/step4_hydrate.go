package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/asolgan/lattice/internal/substrate"
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
//     only)
type HydratorImpl struct {
	Conn       *substrate.Conn
	CoreBucket string
	DDLs       *DDLCache
	Logger     *slog.Logger
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
	class, err := resolveClass(env)
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

	// 4. Hydrate contextHint.reads (optional).
	hydrated := make(map[string]VertexDoc)
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
			hydrated[key] = doc
		}
	}

	h.Logger.Info("step 4: hydrated",
		"requestId", rid,
		"class", class,
		"ddlKey", ddlKey,
		"contextHintCount", len(hydrated),
	)

	return HydratedState{
		Context: ScriptContext{
			Operation:    env,
			Hydrated:     hydrated,
			DDLLookup:    map[string]MetaVertex{class: metaVtx},
			ScriptSource: source,
			ScriptClass:  class,
		},
	}, nil
}

// resolveClass extracts the operation's class for DDL lookup.
//
// resolveClass extracts the DDL class name from the envelope. The top-level
// `class` field is consulted first; the payload's `class` field is the
// fallback. Every operation must declare its class.
func resolveClass(env *OperationEnvelope) (string, error) {
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
