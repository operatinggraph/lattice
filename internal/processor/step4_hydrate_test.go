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
	t.Parallel()
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

// TestHydrate_RequiredAbsent_RecordedNotFaulted pins the deferral at its
// source: step 4 runs before any authorization the declared key is subject to,
// so an absent `reads` key is RECORDED rather than faulted. Hydration must
// succeed — a fault here would let any actor holding any operation grant test
// the existence of any Core KV key. The fault belongs at first use
// (TestRequiredAbsent_* in starlark_required_absent_test.go).
func TestHydrate_RequiredAbsent_RecordedNotFaulted(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	h := NewHydrator(conn, testCoreBucket, testLogger())

	env := newTestEnvelope(testNanoID1)
	missingKey := "vtx.identity.MissingMissingMissing"
	env.ContextHint = &ContextHint{Reads: []string{missingKey}}

	state, err := h.Hydrate(ctx, env)
	if err != nil {
		t.Fatalf("hydrate must not fault on an untouched absent declared read: %v", err)
	}
	sc := state.Context
	if _, ok := sc.RequiredAbsent[missingKey]; !ok {
		t.Fatalf("RequiredAbsent = %v, want it to record %q", sc.RequiredAbsent, missingKey)
	}
	// Fail-closed, not absence-tolerant: it must NOT be demoted to the
	// optionalReads disposition (which would read as None), and must never
	// appear in `state`.
	if _, ok := sc.KnownAbsent[missingKey]; ok {
		t.Fatalf("a `reads` miss must not land in KnownAbsent (that is the optionalReads disposition)")
	}
	if _, ok := sc.Hydrated[missingKey]; ok {
		t.Fatalf("an absent key must never be hydrated")
	}
	if sc.DeferredMiss == nil {
		t.Fatalf("DeferredMiss tracker must be wired so the runner can raise the deferred fault")
	}
	if got := sc.DeferredMiss.missed(); got != "" {
		t.Fatalf("nothing touched the key yet, so missed() = %q, want \"\"", got)
	}
}

// TestHydrate_ClassInferredFromOperationType is the RF#1 dispatch case: an op
// envelope with NO `class` field (and no payload.class) resolves its DDL from
// the operationType via the cache's reverse index — exactly what an engine
// dispatch envelope omits. The harness seeds vtx.meta.identity admitting
// CreateIdentity, so the dispatched op hydrates as class=identity.
func TestHydrate_ClassInferredFromOperationType(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// TestDistinctKeys is what makes the declared-read ceiling
// (opwire.MaxDeclaredReads) a bound on Core KV round trips rather than on
// mentions. Nothing rejects a duplicate declaration and the lists are
// client-supplied, so a key named N times must cost one resolution — otherwise
// a declared set sitting well inside the ceiling buys arbitrary hydration just
// by repeating itself.
//
// This is asserted on the helper, not through Hydrate: the hydration result is
// three MAPS, so a per-mention re-GET produces the identical end state and an
// end-state assertion would pass whether or not the dedup exists.
func TestDistinctKeys(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		declared []string
		want     []string
	}{
		{"nil is nil", nil, nil},
		{"empty is nil", []string{}, nil},
		{"order is declaration order", []string{"c", "a", "b"}, []string{"c", "a", "b"}},
		{"repeats collapse to the first mention", []string{"a", "b", "a", "a", "b"}, []string{"a", "b"}},
		{"one key many times is one key", []string{"a", "a", "a", "a", "a", "a", "a", "a"}, []string{"a"}},
		// "" is the no-fault sentinel for the tracker and the mutation check,
		// so it must never survive into a resolution.
		{"blanks are dropped", []string{"", "a", "", "b", ""}, []string{"a", "b"}},
		{"only blanks is nil", []string{"", "", ""}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := distinctKeys(tc.declared)
			if len(got) != len(tc.want) {
				t.Fatalf("distinctKeys(%q) = %q, want %q", tc.declared, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("distinctKeys(%q) = %q, want %q", tc.declared, got, tc.want)
				}
			}
		})
	}
}

// TestHydrate_RepeatedKeyResolvesOnce is the end-to-end companion: a declared
// set full of repeats still hydrates correctly through the real loops, in every
// class and both dispositions. It cannot prove the round-trip count (see
// TestDistinctKeys for that) — it proves the dedup did not change what the
// script sees.
func TestHydrate_RepeatedKeyResolvesOnce(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	h := NewHydrator(conn, testCoreBucket, testLogger())

	presentKey := "vtx.identity." + testNanoID2
	if _, err := conn.KVPut(ctx, testCoreBucket, presentKey,
		[]byte(`{"class":"identity","isDeleted":false,"data":{"name":"A"}}`)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	absentReadKey := "vtx.identity.AbsentAbsentAbsent1"
	absentEgressKey := "vtx.identity.AbsentAbsentAbsent2"
	absentOptionalKey := "vtx.identity.AbsentAbsentAbsent3"

	repeat := func(key string, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = key
		}
		return out
	}

	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{
		Reads:         append(repeat(presentKey, 8), repeat(absentReadKey, 8)...),
		OptionalReads: repeat(absentOptionalKey, 8),
		// Disjoint from the other two lists — ParseEnvelope refuses an
		// overlap, so a repeat within egressReads is the only duplicate that
		// can reach that loop.
		EgressReads: repeat(absentEgressKey, 8),
	}

	state, err := h.Hydrate(ctx, env)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	sc := state.Context
	if _, ok := sc.Hydrated[presentKey]; !ok || len(sc.Hydrated) != 1 {
		t.Fatalf("Hydrated = %v, want exactly the present key", sc.Hydrated)
	}
	for _, k := range []string{absentReadKey, absentEgressKey} {
		if _, ok := sc.RequiredAbsent[k]; !ok {
			t.Fatalf("RequiredAbsent = %v, want it to record %q fail-closed", sc.RequiredAbsent, k)
		}
	}
	if _, ok := sc.KnownAbsent[absentOptionalKey]; !ok || len(sc.KnownAbsent) != 1 {
		t.Fatalf("KnownAbsent = %v, want exactly the absent optionalReads key", sc.KnownAbsent)
	}
}
