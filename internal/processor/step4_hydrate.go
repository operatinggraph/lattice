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

// HydratorImpl is the Story 1.6 implementation of step 4 (JIT Hydrate).
//
// Responsibilities:
//  1. Resolve the operation's class — for Story 1.6 the class is the
//     `class` field on the envelope (top-level), or, if absent, the
//     `payload.class` field. A missing class is an error: every
//     committed operation MUST identify a DDL.
//  2. Hydrate the DDL meta-vertex and its `script` aspect for that
//     class. The DDL key for Story 1.6 is the logical name
//     `vtx.meta.<class>` (see CONTRACT-AMENDMENT-REQUEST 1.6); the
//     script source lives at `vtx.meta.<class>.script`. Missing script
//     → HydrationError(Code="NoScriptForClass").
//  3. Hydrate every key in envelope.contextHint.reads. Any miss →
//     HydrationError(Code="HydrationMiss"). An empty/missing
//     contextHint is permitted (e.g., pure-create operations).
type HydratorImpl struct {
	Conn       *substrate.Conn
	CoreBucket string
	Logger     *slog.Logger
}

// NewHydrator wires a real Hydrator.
func NewHydrator(conn *substrate.Conn, coreBucket string, logger *slog.Logger) *HydratorImpl {
	if conn == nil {
		panic("processor: NewHydrator requires Conn")
	}
	if coreBucket == "" {
		panic("processor: NewHydrator requires CoreBucket")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &HydratorImpl{Conn: conn, CoreBucket: coreBucket, Logger: logger}
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

	// 2. Hydrate DDL meta-vertex.
	ddlKey := metaVertexKeyForClass(class)
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
	metaVtx := MetaVertex{
		Key:           ddlKey,
		CanonicalName: class,
	}
	if pcAny, ok := ddlDoc.Data["permittedCommands"]; ok {
		if pcList, ok := pcAny.([]interface{}); ok {
			for _, c := range pcList {
				if s, ok := c.(string); ok {
					metaVtx.PermittedCommands = append(metaVtx.PermittedCommands, s)
				}
			}
		}
	}

	// 3. Hydrate script aspect.
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
	source, _ := scriptDoc.Data["source"].(string)
	if strings.TrimSpace(source) == "" {
		return HydratedState{}, &HydrationError{
			Code: "EmptyScript", MissingKey: scriptKey, OperationRequestID: rid,
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
// Story 1.6 strategy: the envelope's `class` field (added top-level for
// 1.6 — see CONTRACT-AMENDMENT-REQUEST 1.6) is consulted first. If
// absent, the payload's `class` field. If neither, we return an error
// — every operation must declare its class until the DDL cache lands
// in Story 1.10 and can infer it from operationType.
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
	return "", fmt.Errorf("operation envelope must carry a top-level `class` field (or payload.class) for Story 1.6 hydration")
}

// metaVertexKeyForClass returns the Story-1.6 logical DDL key. NOTE:
// canonical Contract #1 keys meta-vertices by NanoID with a
// `canonicalName` aspect — see CONTRACT-AMENDMENT-REQUEST 1.6 for the
// gap this bridges. Story 1.10 swaps this for a real DDL cache.
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
