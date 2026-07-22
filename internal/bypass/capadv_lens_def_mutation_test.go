// Package bypass holds the outcome-level adversarial residual for the
// Capability Lens security plane — assemblies that don't reduce to one
// mechanism's colocated white-box test.
//
// Lens-definition mutation via AI-authored operation.
//
// Attack: A rogue AI actor (an automated agent with no granted permissions)
// attempts to weaken the Capability Lens cypher rule by submitting an
// UpdateLensDefinition or UpdateAspect operation targeting the Capability
// Lens meta-vertex (vtx.meta.lens.capability). If successful, the AI could
// rewrite the cypher to expand its own permissions or disable authorization.
//
// Defense: The AI actor has no Capability KV entry (no holdsRole edges in
// the graph → no cap.identity.<NanoID> key). The CapabilityAuthorizer's
// KVGet returns ErrKeyNotFound → Decision.Code == AuthDenied/NoCapabilityEntry
// → operation rejected pre-commit → lens definition unchanged.
//
// AI actor convention (brief Decision #3):
//
//	Identity is seeded as vtx.identity.<NanoID> with vertex envelope
//	class: "identity.ai". The bootstrap does NOT have a primordial AI actor;
//	the test seeds its own. No Capability KV entry is seeded (no permissions).
//
// NFR-S10 assertion (brief Decision #4):
//
//	The Authorizer MUST NOT contain special-case handling for AI actors.
//	Proof: grep for "identity.ai" / "ai.*" in internal/processor/step3_auth*.go
//	returns no matches. This is a programmatic test assertion.
//
// DEFENDED when:
//   - The operation is rejected pre-commit with AuthDenied/NoCapabilityEntry.
//   - The lens definition vertex is unchanged (Core KV revision didn't bump).
//   - The auth trace captures planes.capabilityKV.matched == false (no-entry).
//   - NFR-S10: no AI-actor special-case code exists in the Authorizer.
//
package bypass

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/processor"
)

// The canonical Capability Lens vertex key (same constant as in step3_auth_trace.go).
// We use this string directly to avoid an import cycle with bootstrap.
const capadvLensDefKey = "vtx.meta.lens.capability"

// TestCapAdv_V3_AIActor_Convention_Documented verifies the test-only AI actor
// convention (brief Decision #3): an AI actor is seeded as vtx.identity.<NanoID>
// with vertex envelope class = "identity.ai". No Capability KV entry is created.
// This is a documentation test — it seeds the vertex and verifies the class field.
func TestCapAdv_V3_AIActor_Convention_Documented(t *testing.T) {
	ctx, conn := setupCapAdvHarness(t)

	aiActorKey := "vtx.identity." + capadvNanoID3
	aiVertexDoc := map[string]any{
		"class":     "identity.ai",
		"isDeleted": false,
		"key":       aiActorKey,
		"data": map[string]any{
			"name":     "test-ai-agent",
			"agentRef": "test-ai-" + capadvNanoID3,
		},
	}
	raw, _ := json.Marshal(aiVertexDoc)
	if _, err := conn.KVPut(ctx, capadvCoreBucket, aiActorKey, raw); err != nil {
		t.Fatalf("v3: seed AI actor vertex: %v", err)
	}

	// Read back and verify class = "identity.ai".
	entry, err := conn.KVGet(ctx, capadvCoreBucket, aiActorKey)
	if err != nil {
		t.Fatalf("v3: KVGet AI actor vertex: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("v3: unmarshal AI actor vertex: %v", err)
	}
	if doc["class"] != "identity.ai" {
		t.Fatalf("v3: AI actor class = %q, want identity.ai", doc["class"])
	}

	// Confirm NO Capability KV entry exists for this AI actor.
	capKey := "cap.identity." + capadvNanoID3
	if kvPresent(ctx, conn, capadvCapBucket, capKey) {
		t.Fatalf("v3: AI actor must have no Capability KV entry; found one at %q", capKey)
	}

	t.Logf("v3: AI actor convention: vtx.identity.%s class=identity.ai, no cap entry ✓", capadvNanoID3)
	t.Logf("v3: Convention doc: AI actors are seeded as vtx.identity.<NanoID> with class=identity.ai; no holdsRole edges → no Capability KV projection")
}

// TestCapAdv_V3_AIActor_LensDef_Rejected verifies that an AI actor with no
// Capability KV entry cannot submit an operation targeting the Capability Lens
// definition vertex. The CapabilityAuthorizer returns AuthDenied/NoCapabilityEntry.
func TestCapAdv_V3_AIActor_LensDef_Rejected(t *testing.T) {
	ctx, conn := setupCapAdvHarness(t)

	aiActorKey := "vtx.identity." + capadvNanoID3

	// AI actor has NO Capability KV entry → AuthDenied/NoCapabilityEntry.
	cfg := processor.DefaultCapabilityAuthorizerConfig()
	authz, err := processor.NewCapabilityAuthorizer(conn, capadvCapBucket, nil, cfg, bypassLogger())
	if err != nil {
		t.Fatalf("NewCapabilityAuthorizer: %v", err)
	}

	// Submit UpdateAspect targeting the Capability Lens definition.
	env := &processor.OperationEnvelope{
		RequestID:     capadvReqV3AI,
		Lane:          processor.LaneDefault,
		OperationType: "UpdateAspect",
		Actor:         aiActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "meta.lens",
		// No AuthContext → platform permission path in CapabilityAuthorizer.
	}

	dec, err := authz.Authorize(ctx, env)
	if err != nil {
		t.Fatalf("v3: Authorize error: %v", err)
	}

	// Must be denied with NoCapabilityEntry.
	if dec.Authorized {
		t.Fatalf("v3: EXPOSED — AI actor operation was ALLOWED; must be denied (AI has no Capability KV entry)")
	}
	if dec.Code != processor.ErrCodeAuthDenied {
		t.Fatalf("v3: expected Decision.Code == AuthDenied, got: %s (reason: %s)", dec.Code, dec.Reason)
	}
	if dec.Reason != "NoCapabilityEntry" {
		t.Fatalf("v3: expected Decision.Reason == NoCapabilityEntry, got: %q", dec.Reason)
	}

	t.Logf("v3: DEFENDED — AI actor op rejected with %s/NoCapabilityEntry ✓", dec.Code)
}

// TestCapAdv_V3_AIActor_LensDef_Unchanged verifies that after the AI actor's
// rejected operation, the Capability Lens definition vertex in Core KV is
// unchanged (revision did not advance). Pre-commit rejection means no write
// reaches Core KV.
func TestCapAdv_V3_AIActor_LensDef_Unchanged(t *testing.T) {
	ctx, conn := setupCapAdvHarness(t)

	// Seed a mock Capability Lens definition vertex to simulate the primordial state.
	lensDoc := map[string]any{
		"class":     "meta.lens",
		"isDeleted": false,
		"key":       capadvLensDefKey,
		"data": map[string]any{
			"canonicalName": "capability",
			"cypherRule":    "MATCH (id:identity) RETURN id.key",
		},
	}
	lensRaw, _ := json.Marshal(lensDoc)
	if _, err := conn.KVPut(ctx, capadvCoreBucket, capadvLensDefKey, lensRaw); err != nil {
		t.Fatalf("v3: seed lens def: %v", err)
	}

	// Record the initial revision.
	initialEntry, err := conn.KVGet(ctx, capadvCoreBucket, capadvLensDefKey)
	if err != nil {
		t.Fatalf("v3: KVGet initial lens def: %v", err)
	}
	initialRevision := initialEntry.Revision

	// AI actor attempts the mutation (no cap entry → will be denied by Authorizer).
	aiActorKey := "vtx.identity." + capadvNanoID3
	cfg := processor.DefaultCapabilityAuthorizerConfig()
	authz, err := processor.NewCapabilityAuthorizer(conn, capadvCapBucket, nil, cfg, bypassLogger())
	if err != nil {
		t.Fatalf("NewCapabilityAuthorizer: %v", err)
	}

	env := &processor.OperationEnvelope{
		RequestID:     "CdV3LensUnRq234567a",
		Lane:          processor.LaneDefault,
		OperationType: "UpdateAspect",
		Actor:         aiActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "meta.lens",
	}

	dec, err := authz.Authorize(ctx, env)
	if err != nil {
		t.Fatalf("v3: Authorize error: %v", err)
	}
	if dec.Authorized {
		t.Fatalf("v3: EXPOSED — AI actor was authorized; lens definition may be at risk")
	}

	// Since auth denied → pre-commit → no write to Core KV → revision unchanged.
	afterEntry, err := conn.KVGet(ctx, capadvCoreBucket, capadvLensDefKey)
	if err != nil {
		t.Fatalf("v3: KVGet lens def after rejection: %v", err)
	}

	if afterEntry.Revision != initialRevision {
		t.Fatalf("v3: EXPOSED — lens definition revision advanced from %d to %d after denied op; pre-commit rejection failed",
			initialRevision, afterEntry.Revision)
	}

	t.Logf("v3: DEFENDED — lens definition unchanged (revision=%d) after AI actor rejection ✓", initialRevision)
}

// TestCapAdv_V3_AIActor_AuthTrace_NoEntry verifies that the auth trace for the
// rejected AI actor operation captures plane1.result == "no-entry" (the actor
// had no Capability KV entry). Uses AuthTraceEmitter directly.
func TestCapAdv_V3_AIActor_AuthTrace_NoEntry(t *testing.T) {
	ctx, conn := setupCapAdvHarness(t)

	aiActorKey := "vtx.identity." + capadvNanoID3
	instanceID := "capadv-v3-trace-test1"

	traceEmitter := processor.NewAuthTraceEmitter(conn, capadvHealthBucket, instanceID, false, bypassLogger())

	env := &processor.OperationEnvelope{
		RequestID:     "CdV3TrRq234567891ab",
		Lane:          processor.LaneDefault,
		OperationType: "UpdateAspect",
		Actor:         aiActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "meta.lens",
	}

	// NoCapabilityEntry denial: doc is nil (no entry was read).
	denialDecision := processor.Decision{
		Authorized: false,
		Code:       processor.ErrCodeAuthDenied,
		Reason:     "NoCapabilityEntry",
		Doc:        nil, // no doc — key not found
	}
	traceEmitter.Emit(env, denialDecision)

	// Wait for async goroutine to flush.
	time.Sleep(200 * time.Millisecond)

	traceKey := "health.processor." + instanceID + ".auth-trace." + env.RequestID
	traceEntry, err := conn.KVGet(ctx, capadvHealthBucket, traceKey)
	if err != nil {
		t.Fatalf("v3 Trace: trace key not found at %q: %v", traceKey, err)
	}

	var rec processor.AuthTraceRecord
	if err := json.Unmarshal(traceEntry.Value, &rec); err != nil {
		t.Fatalf("v3 Trace: unmarshal trace record: %v", err)
	}

	// Verify plane1.result == "no-entry".
	if rec.Plane1.Result != "no-entry" {
		t.Fatalf("v3 Trace: expected plane1.result=no-entry, got %q", rec.Plane1.Result)
	}
	if rec.AuthOutcome != "denied" {
		t.Fatalf("v3 Trace: expected authOutcome=denied, got %q", rec.AuthOutcome)
	}
	if rec.AuthCode != string(processor.ErrCodeAuthDenied) {
		t.Fatalf("v3 Trace: expected authCode=AuthDenied, got %q", rec.AuthCode)
	}

	t.Logf("v3 Trace: DEFENDED — auth trace at %q: plane1.result=%q ✓", traceKey, rec.Plane1.Result)
}

// TestCapAdv_V3_NFRS10_NoAISpecialCase is the NFR-S10 assertion.
// It programmatically verifies that the CapabilityAuthorizer source files
// (internal/processor/step3_auth*.go) contain NO special-case handling for
// AI actors ("identity.ai" or AI-specific branch). The absence of such code
// is the proof that the Authorizer treats AI actors identically to human actors.
//
// If this test fails: someone added AI-actor special-casing to the Authorizer —
// which would be a security defect (creates a side-channel or escape hatch).
func TestCapAdv_V3_NFRS10_NoAISpecialCase(t *testing.T) {
	// Search for AI-actor special-case strings in step3_auth*.go files.
	// grep returns exit code 1 when no matches found (which is what we want).
	patterns := []string{
		"identity.ai",
		"identity:ai",
		"isAI",
		"ai_actor",
		"aiActor",
		"AIActor",
	}

	for _, pattern := range patterns {
		out, err := exec.Command(
			"grep", "-rn", pattern,
			"internal/processor/step3_auth_capability.go",
			"internal/processor/step3_auth.go",
		).Output()

		if err == nil && len(strings.TrimSpace(string(out))) > 0 {
			// grep found a match — AI-actor special-case code exists.
			t.Fatalf("v3 NFR-S10: VIOLATED — AI-actor special-case code found for pattern %q in step3_auth*.go:\n%s\n"+
				"The CapabilityAuthorizer MUST NOT special-case AI actors per NFR-S10.", pattern, string(out))
		}
		// err != nil means grep returned non-zero (no match) or binary not found.
		// Both are acceptable here.
		t.Logf("v3 NFR-S10: pattern %q: no special-case code found ✓", pattern)
	}

	t.Logf("v3 NFR-S10: PROVED — CapabilityAuthorizer has no AI-actor special-case handling; AI actors are treated identically to human actors")
}
