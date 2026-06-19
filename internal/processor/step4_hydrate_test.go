package processor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// Step-4 unit tests run against an embedded NATS + Core KV harness
// reusing the integration test helpers from integration_test.go.

func TestHydrate_HappyPath_ContextHintAndDDL(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	h := NewHydrator(conn, testCoreBucket, testLogger())

	// Pre-seed the actor vertex referenced via contextHint.
	actorKey := "vtx.identity." + testNanoID2
	actorDoc := []byte(`{"class":"identity","isDeleted":false,"data":{"name":"Andrew"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, actorKey, actorDoc); err != nil {
		t.Fatalf("seed actor: %v", err)
	}

	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{Reads: []string{actorKey}}

	state, err := h.Hydrate(ctx, env)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	sc := state.Context
	if sc.ScriptClass != "identity" {
		t.Fatalf("ScriptClass = %q, want identity", sc.ScriptClass)
	}
	if sc.ScriptSource == "" {
		t.Fatalf("ScriptSource empty after hydrate")
	}
	if _, ok := sc.Hydrated[actorKey]; !ok {
		t.Fatalf("actor not hydrated: %+v", sc.Hydrated)
	}
	if sc.Hydrated[actorKey].Class != "identity" {
		t.Fatalf("actor class = %q", sc.Hydrated[actorKey].Class)
	}
	if _, ok := sc.DDLLookup["identity"]; !ok {
		t.Fatalf("DDL not in lookup: %+v", sc.DDLLookup)
	}
}

func TestHydrate_HydrationMiss_ContextHintKey(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	h := NewHydrator(conn, testCoreBucket, testLogger())

	env := newTestEnvelope(testNanoID1)
	missingKey := "vtx.identity.MissingMissingMissing"
	env.ContextHint = &ContextHint{Reads: []string{missingKey}}

	_, err := h.Hydrate(ctx, env)
	if err == nil {
		t.Fatalf("expected HydrationError, got nil")
	}
	var hErr *HydrationError
	if !errors.As(err, &hErr) {
		t.Fatalf("expected *HydrationError, got %T: %v", err, err)
	}
	if hErr.Code != "HydrationMiss" {
		t.Fatalf("Code = %q, want HydrationMiss", hErr.Code)
	}
	if hErr.MissingKey != missingKey {
		t.Fatalf("MissingKey = %q, want %q", hErr.MissingKey, missingKey)
	}
}

// TestHydrate_ClassInferredFromOperationType is the RF#1 dispatch case: an op
// envelope with NO `class` field (and no payload.class) resolves its DDL from
// the operationType via the cache's reverse index — exactly what an engine
// dispatch envelope omits. The harness seeds vtx.meta.identity admitting
// CreateIdentity, so the dispatched op hydrates as class=identity.
func TestHydrate_ClassInferredFromOperationType(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	h := NewHydratorWithCache(conn, testCoreBucket, cache, testLogger())

	env := newTestEnvelope(testNanoID1)
	env.Class = "" // dispatched op: class omitted, must be inferred.

	state, err := h.Hydrate(ctx, env)
	if err != nil {
		t.Fatalf("Hydrate (class inferred): %v", err)
	}
	if state.Context.ScriptClass != "identity" {
		t.Fatalf("ScriptClass = %q, want identity (inferred from CreateIdentity)", state.Context.ScriptClass)
	}
	if state.Context.ScriptSource == "" {
		t.Fatalf("ScriptSource empty after class-inferred hydrate")
	}
}

// TestHydrate_MissingClass_UnindexedOpStillFails confirms RF#1 does not weaken
// the fail-closed behavior for a genuinely unresolvable op: an envelope with no
// class whose operationType is admitted by no DDL still errors MissingClass
// (unchanged behavior).
func TestHydrate_MissingClass_UnindexedOpStillFails(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	h := NewHydratorWithCache(conn, testCoreBucket, cache, testLogger())

	env := newTestEnvelope(testNanoID1)
	env.Class = ""
	env.Payload = json.RawMessage(`{"name":"Andrew"}`)
	env.OperationType = "NoSuchUnindexedOp"

	_, err := h.Hydrate(ctx, env)
	if err == nil {
		t.Fatalf("expected MissingClass error for an unindexed op with no class")
	}
	var hErr *HydrationError
	if !errors.As(err, &hErr) || hErr.Code != "MissingClass" {
		t.Fatalf("expected MissingClass HydrationError, got %T: %v", err, err)
	}
}

func TestHydrate_NoScriptForClass(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	h := NewHydrator(conn, testCoreBucket, testLogger())

	// Seed a DDL for class "naked" but no script aspect.
	if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.meta.naked",
		[]byte(`{"class":"meta.ddl.vertexType","isDeleted":false,"data":{"canonicalName":"naked"}}`)); err != nil {
		t.Fatalf("seed naked DDL: %v", err)
	}

	env := newTestEnvelope(testNanoID1)
	env.Class = "naked"

	_, err := h.Hydrate(ctx, env)
	var hErr *HydrationError
	if !errors.As(err, &hErr) {
		t.Fatalf("expected *HydrationError, got %T: %v", err, err)
	}
	if hErr.Code != "NoScriptForClass" {
		t.Fatalf("Code = %q, want NoScriptForClass", hErr.Code)
	}
}

func TestHydrate_NoDDLForClass(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	h := NewHydrator(conn, testCoreBucket, testLogger())

	env := newTestEnvelope(testNanoID1)
	env.Class = "neverseeded"

	_, err := h.Hydrate(ctx, env)
	var hErr *HydrationError
	if !errors.As(err, &hErr) {
		t.Fatalf("expected *HydrationError, got %T: %v", err, err)
	}
	if hErr.Code != "NoDDLForClass" {
		t.Fatalf("Code = %q, want NoDDLForClass", hErr.Code)
	}
}

func TestHydrate_EmptyContextHintAllowed(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	h := NewHydrator(conn, testCoreBucket, testLogger())

	env := newTestEnvelope(testNanoID1)
	env.ContextHint = nil

	state, err := h.Hydrate(ctx, env)
	if err != nil {
		t.Fatalf("Hydrate(nil contextHint): %v", err)
	}
	if len(state.Context.Hydrated) != 0 {
		t.Fatalf("Hydrated should be empty, got %v", state.Context.Hydrated)
	}
	if state.Context.ScriptSource == "" {
		t.Fatalf("DDL/script should still hydrate when contextHint is nil")
	}
}

func TestHydrate_ClassFromPayload(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	h := NewHydrator(conn, testCoreBucket, testLogger())

	env := newTestEnvelope(testNanoID1)
	env.Class = "" // remove top-level
	env.Payload = json.RawMessage(`{"class":"identity","name":"Andrew"}`)

	state, err := h.Hydrate(ctx, env)
	if err != nil {
		t.Fatalf("Hydrate via payload.class: %v", err)
	}
	if state.Context.ScriptClass != "identity" {
		t.Fatalf("ScriptClass = %q", state.Context.ScriptClass)
	}
}

func TestHydrate_MissingClass(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	h := NewHydrator(conn, testCoreBucket, testLogger())

	env := newTestEnvelope(testNanoID1)
	env.Class = ""
	env.Payload = json.RawMessage(`{"name":"Andrew"}`)

	_, err := h.Hydrate(ctx, env)
	var hErr *HydrationError
	if !errors.As(err, &hErr) {
		t.Fatalf("expected *HydrationError, got %T: %v", err, err)
	}
	if hErr.Code != "MissingClass" {
		t.Fatalf("Code = %q, want MissingClass", hErr.Code)
	}
}

// Ensure the parsed VertexDoc carries the key for downstream consumers.
func TestHydrate_VertexDocCarriesKey(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	h := NewHydrator(conn, testCoreBucket, testLogger())

	actorKey := "vtx.identity." + testNanoID2
	if _, err := conn.KVPut(ctx, testCoreBucket, actorKey,
		[]byte(`{"class":"identity","isDeleted":false,"data":{"name":"A"}}`)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{Reads: []string{actorKey}}

	state, err := h.Hydrate(context.Background(), env)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if state.Context.Hydrated[actorKey].Key != actorKey {
		t.Fatalf("VertexDoc.Key = %q, want %q", state.Context.Hydrated[actorKey].Key, actorKey)
	}
}
