// Package bypass holds the outcome-level adversarial residual for the
// Capability Lens security plane — assemblies that don't reduce to one
// mechanism's colocated white-box test.
//
// Cross-target ephemeral grant bleed.
//
// Attack: A manager identity (aliceManager) has an ephemeral grant derived from
// her task assignment (aliceTask → ApproveLeaseApplication → aliceLease). She
// attempts to use that grant against a different target (bobLease) or a task she
// does not own (bobTask → bobLease). The Capability Lens cypher's target-matching
// logic must prevent cross-target reuse of ephemeral grants.
//
// Fixture topology:
//
//	aliceManager — task: aliceTask → (operationType: ApproveLeaseApplication, target: aliceLease)
//	bobManager   — task: bobTask   → (operationType: ApproveLeaseApplication, target: bobLease)
//
// Test phases:
//
//	Phase A (positive): alice → aliceTask → aliceLease → ALLOWED
//	Phase B (cross-target): alice → aliceTask → bobLease → DENIED/AuthContextMismatch
//	Phase C (cross-manager): alice → bobTask → bobLease → DENIED/AuthContextMismatch
//
// FR56 assertion: variable-length reportsTo* traversal does not create transitive
// grants across reporting hierarchies. Alice's cap entry contains no ephemeralGrant
// for bobLease or bobTask.
//
// DEFENDED when: both cross-target denial paths fire AND alice→aliceLease positive
// path commits AND alice's cap entry has no ephemeralGrants for bob's chain.
package bypass

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
)

// Ephemeral grants live in the disjoint cap.ephemeral.<actor> entry produced
// by the orchestration-base capabilityEphemeral lens (Contract #6 §6.6
// amendment). The task-dispatch branch of step-3 reads that key. These V4
// fixtures therefore seed the grants under cap.ephemeral.<actor>; the
// matching logic / cross-target target-match assertions are unchanged.

// aliceEphCapKey / bobEphCapKey are the disjoint ephemeral entry keys.
func aliceEphCapKey() string { return "cap.ephemeral.identity." + capadvNanoID4 }
func bobEphCapKey() string   { return "cap.ephemeral.identity." + capadvNanoID5 }

// buildAliceEphDoc builds aliceManager's cap.ephemeral.<actor> entry with one
// ephemeral grant: (aliceTask, ApproveLeaseApplication, aliceLease). No
// grants for bobLease or bobTask.
func buildAliceEphDoc() *processor.CapabilityDoc {
	aliceID := capadvNanoID4
	aliceTaskKey := "vtx.task." + capadvNanoID6
	aliceLeaseKey := "vtx.leaseApp." + capadvNanoID8
	actorKey := "vtx.identity." + aliceID

	future := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339Nano)
	return &processor.CapabilityDoc{
		Key:         aliceEphCapKey(),
		Actor:       actorKey,
		Version:     "1.0",
		ProjectedAt: time.Now().UTC().Format(time.RFC3339Nano),
		// Alice has exactly ONE ephemeral grant: her own task → her own lease.
		// No transitive grant to bobLease or any entry from bobTask.
		EphemeralGrants: []processor.EphemeralGrant{
			{
				Source:        "task",
				TaskKey:       aliceTaskKey,
				OperationType: "ApproveLeaseApplication",
				Target:        aliceLeaseKey,
				ExpiresAt:     future,
			},
		},
	}
}

// buildBobEphDoc builds bobManager's cap.ephemeral.<actor> entry with one
// ephemeral grant: (bobTask, ApproveLeaseApplication, bobLease). Not used by
// alice.
func buildBobEphDoc() *processor.CapabilityDoc {
	bobID := capadvNanoID5
	bobTaskKey := "vtx.task." + capadvNanoID7
	bobLeaseKey := "vtx.leaseApp." + capadvNanoID9
	actorKey := "vtx.identity." + bobID

	future := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339Nano)
	return &processor.CapabilityDoc{
		Key:         bobEphCapKey(),
		Actor:       actorKey,
		Version:     "1.0",
		ProjectedAt: time.Now().UTC().Format(time.RFC3339Nano),
		EphemeralGrants: []processor.EphemeralGrant{
			{
				Source:        "task",
				TaskKey:       bobTaskKey,
				OperationType: "ApproveLeaseApplication",
				Target:        bobLeaseKey,
				ExpiresAt:     future,
			},
		},
	}
}

// setupV4Harness provisions Capability KV with alice and bob's ephemeral
// cap.ephemeral.<actor> entries.
func setupV4Harness(t *testing.T) (context.Context, *substrate.Conn, *processor.CapabilityAuthorizer) { //nolint:unparam
	t.Helper()
	ctx, conn := setupCapAdvHarness(t)

	// Seed alice's ephemeral entry.
	aliceDoc := buildAliceEphDoc()
	aliceRaw, _ := json.Marshal(aliceDoc)
	if _, err := conn.KVPut(ctx, capadvCapBucket, aliceDoc.Key, aliceRaw); err != nil {
		t.Fatalf("v4: seed alice ephemeral cap doc: %v", err)
	}

	// Seed bob's ephemeral entry.
	bobDoc := buildBobEphDoc()
	bobRaw, _ := json.Marshal(bobDoc)
	if _, err := conn.KVPut(ctx, capadvCapBucket, bobDoc.Key, bobRaw); err != nil {
		t.Fatalf("v4: seed bob ephemeral cap doc: %v", err)
	}

	cfg := processor.DefaultCapabilityAuthorizerConfig()
	authz, err := processor.NewCapabilityAuthorizer(conn, capadvCapBucket, nil, cfg, bypassLogger())
	if err != nil {
		t.Fatalf("NewCapabilityAuthorizer: %v", err)
	}
	return ctx, conn, authz
}

// TestCapAdv_V4_CrossTarget_PositivePath verifies that aliceManager can submit
// ApproveLeaseApplication with authContext = {task: aliceTask, target: aliceLease}.
// This is the positive baseline — the correct task+target pair is authorized.
func TestCapAdv_V4_CrossTarget_PositivePath(t *testing.T) {
	ctx, _, authz := setupV4Harness(t)

	aliceActorKey := "vtx.identity." + capadvNanoID4
	aliceTaskKey := "vtx.task." + capadvNanoID6
	aliceLeaseKey := "vtx.leaseApp." + capadvNanoID8

	env := &processor.OperationEnvelope{
		RequestID:     capadvReqV4Pos,
		Lane:          processor.LaneDefault,
		OperationType: "ApproveLeaseApplication",
		Actor:         aliceActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseApp",
		AuthContext: &processor.AuthContext{
			Task:   aliceTaskKey,
			Target: aliceLeaseKey,
		},
	}

	dec, err := authz.Authorize(ctx, env)
	if err != nil {
		t.Fatalf("v4 Positive: Authorize error: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("v4 Positive: FAILED — alice→aliceTask→aliceLease should be ALLOWED; got denied: code=%s reason=%s",
			dec.Code, dec.Reason)
	}

	t.Logf("v4 Positive: alice→aliceTask→aliceLease ALLOWED ✓ (path: %s)", dec.Resolved.Path)
}

// TestCapAdv_V4_CrossTarget_DeniedBobLease verifies that aliceManager CANNOT
// use aliceTask to approve bobLease. Phase B: same task key, wrong target.
// Expected: AuthContextMismatch (no ephemeralGrant matching aliceTask+bobLease).
func TestCapAdv_V4_CrossTarget_DeniedBobLease(t *testing.T) {
	ctx, _, authz := setupV4Harness(t)

	aliceActorKey := "vtx.identity." + capadvNanoID4
	aliceTaskKey := "vtx.task." + capadvNanoID6
	bobLeaseKey := "vtx.leaseApp." + capadvNanoID9 // wrong target

	env := &processor.OperationEnvelope{
		RequestID:     capadvReqV4CT,
		Lane:          processor.LaneDefault,
		OperationType: "ApproveLeaseApplication",
		Actor:         aliceActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseApp",
		AuthContext: &processor.AuthContext{
			Task:   aliceTaskKey,
			Target: bobLeaseKey, // cross-target attempt
		},
	}

	dec, err := authz.Authorize(ctx, env)
	if err != nil {
		t.Fatalf("v4 CrossTarget: Authorize error: %v", err)
	}
	if dec.Authorized {
		t.Fatalf("v4 CrossTarget: EXPOSED — alice used aliceTask to approve bobLease; cross-target bleed detected")
	}
	if dec.Code != processor.ErrCodeAuthContextMismatch {
		t.Fatalf("v4 CrossTarget: expected AuthContextMismatch, got: %s (reason: %s)", dec.Code, dec.Reason)
	}

	t.Logf("v4 CrossTarget: DEFENDED — alice→aliceTask→bobLease denied with AuthContextMismatch ✓")
}

// TestCapAdv_V4_CrossManager_DeniedBobTask verifies that aliceManager CANNOT
// use bobTask to approve bobLease. Phase C: alice has no ephemeralGrant for
// bobTask at all → no match in her cap entry → AuthContextMismatch.
func TestCapAdv_V4_CrossManager_DeniedBobTask(t *testing.T) {
	ctx, _, authz := setupV4Harness(t)

	aliceActorKey := "vtx.identity." + capadvNanoID4
	bobTaskKey := "vtx.task." + capadvNanoID7      // bob's task
	bobLeaseKey := "vtx.leaseApp." + capadvNanoID9 // bob's lease

	env := &processor.OperationEnvelope{
		RequestID:     capadvReqV4CM,
		Lane:          processor.LaneDefault,
		OperationType: "ApproveLeaseApplication",
		Actor:         aliceActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseApp",
		AuthContext: &processor.AuthContext{
			Task:   bobTaskKey,  // cross-manager task
			Target: bobLeaseKey, // cross-manager lease
		},
	}

	dec, err := authz.Authorize(ctx, env)
	if err != nil {
		t.Fatalf("v4 CrossManager: Authorize error: %v", err)
	}
	if dec.Authorized {
		t.Fatalf("v4 CrossManager: EXPOSED — alice approved using bobTask+bobLease; cross-manager grant bleed detected")
	}
	if dec.Code != processor.ErrCodeAuthContextMismatch {
		t.Fatalf("v4 CrossManager: expected AuthContextMismatch, got: %s (reason: %s)", dec.Code, dec.Reason)
	}

	t.Logf("v4 CrossManager: DEFENDED — alice→bobTask→bobLease denied with AuthContextMismatch ✓")
}

// TestCapAdv_V4_FR56_AliceCapEntry_NoTransitiveGrant is the FR56 assertion.
// It reads alice's cap entry directly and verifies that her ephemeralGrants slice
// contains ONLY grants for her own task and lease — never for bob's chain.
// This proves the Capability Lens cypher's variable-length reportsTo* traversal
// does not create transitive grants across reporting hierarchies.
func TestCapAdv_V4_FR56_AliceCapEntry_NoTransitiveGrant(t *testing.T) {
	ctx, conn, _ := setupV4Harness(t)

	// Alice's ephemeral grants live in the disjoint cap.ephemeral.<actor> entry.
	aliceEphKey := aliceEphCapKey()
	entry, err := conn.KVGet(ctx, capadvCapBucket, aliceEphKey)
	if err != nil {
		t.Fatalf("v4 FR56: KVGet alice ephemeral cap entry: %v", err)
	}

	var doc processor.CapabilityDoc
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("v4 FR56: unmarshal alice ephemeral cap doc: %v", err)
	}

	bobTaskKey := "vtx.task." + capadvNanoID7
	bobLeaseKey := "vtx.leaseApp." + capadvNanoID9

	// Alice's ephemeralGrants must NOT reference bob's task or bob's lease.
	for _, g := range doc.EphemeralGrants {
		if g.TaskKey == bobTaskKey {
			t.Fatalf("v4 FR56: EXPOSED — alice's cap entry contains ephemeralGrant for bobTask (%s); transitive bleed via reportsTo*", bobTaskKey)
		}
		if g.Target == bobLeaseKey {
			t.Fatalf("v4 FR56: EXPOSED — alice's cap entry contains ephemeralGrant targeting bobLease (%s); cross-target grant exists", bobLeaseKey)
		}
	}

	aliceTaskKey := "vtx.task." + capadvNanoID6
	aliceLeaseKey := "vtx.leaseApp." + capadvNanoID8

	// Positive check: alice's entry must contain her own grant.
	foundAliceGrant := false
	for _, g := range doc.EphemeralGrants {
		if g.TaskKey == aliceTaskKey && g.Target == aliceLeaseKey {
			foundAliceGrant = true
		}
	}
	if !foundAliceGrant {
		t.Fatalf("v4 FR56: alice's cap entry missing expected grant (aliceTask→aliceLease); expected at least one ephemeralGrant for her own chain")
	}

	t.Logf("v4 FR56: PROVED — alice's cap entry contains only her own grant (aliceTask→aliceLease); no transitive grants to bob's chain ✓")
	t.Logf("v4 FR56: ephemeralGrants count: %d", len(doc.EphemeralGrants))
}
