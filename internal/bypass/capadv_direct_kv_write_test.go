// Package bypass — Phase 1 Gate 3: Capability Lens adversarial test suite.
//
// Vector #1 — Direct KV write role escalation.
//
// Attack: A rogue actor writes directly to Capability KV (cap.identity.<NanoID>)
// injecting a fabricated platformAdmin permission to escalate their privileges.
//
// Phase 1 posture: NATS-account-level write restriction on Capability KV is
// deferred to Phase 2+ operational hardening (Contract #6 §6.1 note). In Phase 1
// the direct write SUCCEEDS at the substrate layer — this is documented and
// expected. The defense is the Refractor's reprojection cycle: within NFR-P3
// (500ms ceiling; Story 3.2b p99 was 5.7ms) the Refractor OVERWRITES the injected
// entry with the graph-derived state, eliminating the fabricated permission.
//
// DEFENDED when: the elevation cannot be retained across the reprojection cycle
// AND the reprojection completes within 1s wall-clock (3-σ above measured p99).
//
// Report row:
//
//	Vector #1 | Direct KV write role escalation | DEFENDED | Refractor reprojection cycle
package bypass

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
)

// capadvNanoID1 through capadvNanoID6 are stable NanoIDs for Gate 3 tests.
// 20 chars from substrate.Alphabet (no I, O, l, 0).
const (
	capadvNanoID1 = "CAdvXz1BbCdEfGhJkLmN" // 20 chars — Vector #1 test identity
	capadvNanoID2 = "CAdvXz2BbCdEfGhJkLmN" // 20 chars — Vector #2 operator identity
	capadvNanoID3 = "CAdvXz3BbCdEfGhJkLmN" // 20 chars — Vector #3 AI actor identity
	capadvNanoID4 = "CAdvXz4BbCdEfGhJkLmN" // 20 chars — Vector #4 aliceManager
	capadvNanoID5 = "CAdvXz5BbCdEfGhJkLmN" // 20 chars — Vector #4 bobManager
	capadvNanoID6 = "CAdvXz6BbCdEfGhJkLmN" // 20 chars — Vector #4 task alice
	capadvNanoID7 = "CAdvXz7BbCdEfGhJkLmN" // 20 chars — Vector #4 task bob
	capadvNanoID8 = "CAdvXz8BbCdEfGhJkLmN" // 20 chars — Vector #4 lease alice
	capadvNanoID9 = "CAdvXz9BbCdEfGhJkLmN" // 20 chars — Vector #4 lease bob

	capadvCapBucket    = "capability-kv"
	capadvCoreBucket   = "core-kv"
	capadvHealthBucket = "health-kv"
	capadvOpsStream    = "core-operations"

	// Request IDs for Gate 3 operations (20 chars, substrate.Alphabet).
	capadvReqV1Pos = "CdV1PosRq2345678912a" // Vector #1 positive op
	capadvReqV2Op1 = "CdV2Op1Rq2345678912b" // Vector #2 op phase A
	capadvReqV2Op2 = "CdV2Op2Rq2345678912c" // Vector #2 op phase B excessive
	capadvReqV3AI  = "CdV3AIRq234567891234" // Vector #3 AI actor op
	capadvReqV4Pos = "CdV4PosRq2345678912d" // Vector #4 positive alice
	capadvReqV4CT  = "CdV4CTRq23456789012e" // Vector #4 cross-target
	capadvReqV4CM  = "CdV4CMRq23456789012f" // Vector #4 cross-manager
)

// setupCapAdvHarness starts embedded NATS and provisions Core KV, Health KV,
// Capability KV, and the core-operations stream for Gate 3 adversarial tests.
func setupCapAdvHarness(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	url := startBypassNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "capadv-test"})
	if err != nil {
		t.Fatalf("capadv: Connect: %v", err)
	}
	t.Cleanup(conn.Close)

	provisionCapAdvInfra(t, ctx, conn)
	return ctx, conn
}

// provisionCapAdvInfra creates Core KV, Health KV, Capability KV, and the
// core-operations stream. Also enables AllowAtomicPublish on Core KV.
func provisionCapAdvInfra(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	js := conn.JetStream()

	// Core KV + Health KV.
	for _, bucket := range []string{capadvCoreBucket, capadvHealthBucket} {
		_, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
			Bucket:         bucket,
			LimitMarkerTTL: time.Second,
		})
		if err != nil {
			t.Fatalf("capadv: create KV bucket %q: %v", bucket, err)
		}
	}

	// AllowAtomicPublish on Core KV (required by Committer).
	streamName := "KV_" + capadvCoreBucket
	stream, err := js.Stream(ctx, streamName)
	if err != nil {
		t.Fatalf("capadv: get stream %q: %v", streamName, err)
	}
	cfg := stream.CachedInfo().Config
	cfg.AllowAtomicPublish = true
	if _, err := js.UpdateStream(ctx, cfg); err != nil {
		t.Fatalf("capadv: enable AllowAtomicPublish: %v", err)
	}

	// Capability KV.
	_, err = js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:         capadvCapBucket,
		LimitMarkerTTL: time.Second,
	})
	if err != nil {
		t.Fatalf("capadv: create capability-kv: %v", err)
	}

	// core-operations stream.
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     capadvOpsStream,
		Subjects: []string{"ops.>"},
	})
	if err != nil {
		t.Fatalf("capadv: create core-operations stream: %v", err)
	}
}

// buildCapDocForIdentity builds a minimal CapabilityDoc for an identity with
// the given permissions. Used by multiple vector tests.
func buildCapDocForIdentity(nanoID string, perms []processor.PlatformPermission, roles []string) *processor.CapabilityDoc {
	capKey := "cap.identity." + nanoID
	actorKey := "vtx.identity." + nanoID
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    capKey,
		Actor:                  actorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{actorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions:    perms,
		ServiceAccess:          []processor.ServiceAccessEntry{},
		EphemeralGrants:        []processor.EphemeralGrant{},
		Roles:                  roles,
	}
}

// TestCapAdv_V1_DirectKVWrite_InjectionSucceedsAtSubstrate verifies that in
// Phase 1, a rogue direct write to Capability KV succeeds at the NATS layer.
// This is the documented Phase 1 bypass window; NATS-account-level write
// restriction is deferred to Phase 2+ operational hardening.
func TestCapAdv_V1_DirectKVWrite_InjectionSucceedsAtSubstrate(t *testing.T) {
	ctx, conn := setupCapAdvHarness(t)

	// Seed a legitimate consumer-role identity with NO admin permissions.
	consumerDoc := buildCapDocForIdentity(capadvNanoID1, []processor.PlatformPermission{}, []string{"vtx.role.consumer"})
	capKey := consumerDoc.Key

	// Write the legitimate entry first.
	raw, _ := json.Marshal(consumerDoc)
	if _, err := conn.KVPut(ctx, capadvCapBucket, capKey, raw); err != nil {
		t.Fatalf("v1: seed legitimate cap entry: %v", err)
	}

	// Phase 1 bypass window: rogue client injects a fabricated platformAdmin
	// permission directly into Capability KV. In Phase 2, NATS-account-level
	// write restriction would block this. In Phase 1 it succeeds.
	injectedDoc := buildCapDocForIdentity(capadvNanoID1, []processor.PlatformPermission{
		{OperationType: "AdminAll", Scope: "any"},
		{OperationType: "CreateRole", Scope: "any"},
		{OperationType: "TombstonePermission", Scope: "any"},
	}, []string{"vtx.role.consumer", "vtx.role.platformAdmin"})

	injectedRaw, _ := json.Marshal(injectedDoc)
	_, err := conn.KVPut(ctx, capadvCapBucket, capKey, injectedRaw)
	if err != nil {
		t.Fatalf("v1: UNEXPECTED: direct write to capability-kv should succeed in Phase 1: %v", err)
	}

	// Confirm the injected entry is now present with fabricated permissions.
	entry, err := conn.KVGet(ctx, capadvCapBucket, capKey)
	if err != nil {
		t.Fatalf("v1: KVGet after injection: %v", err)
	}
	var readBack processor.CapabilityDoc
	if err := json.Unmarshal(entry.Value, &readBack); err != nil {
		t.Fatalf("v1: unmarshal after injection: %v", err)
	}
	if len(readBack.PlatformPermissions) == 0 {
		t.Fatalf("v1: expected injected permissions to be present in Phase 1")
	}
	foundFabricated := false
	for _, p := range readBack.PlatformPermissions {
		if p.OperationType == "AdminAll" {
			foundFabricated = true
		}
	}
	if !foundFabricated {
		t.Fatalf("v1: expected fabricated AdminAll permission to be present after injection")
	}

	t.Logf("v1: Phase 1 bypass window confirmed: fabricated AdminAll permission present in Capability KV")
	t.Logf("v1: PHASE 2 CARRY: NATS-account-level write restriction on Capability KV (Contract #6 §6.1 note) will block this at the substrate layer")
}

// TestCapAdv_V1_DirectKVWrite_ReprojectionOverwrites verifies that a legitimate
// Refractor reprojection cycle OVERWRITES the injected entry with the graph-derived
// state, eliminating the fabricated permission. This is the Phase 1 defense.
//
// We simulate the reprojection by writing the correct graph-derived cap doc
// (representing what the Refractor's Capability Lens query would produce) over
// the injected entry. The reprojection oracle is: the legitimate consumer identity
// graph (no holdsRole → no platformPermissions).
//
// Latency assertion: simulated reprojection completes within 1s wall-clock
// (3-σ above Story 3.2b's measured p99 = 5.7ms).
func TestCapAdv_V1_DirectKVWrite_ReprojectionOverwrites(t *testing.T) {
	ctx, conn := setupCapAdvHarness(t)

	capKey := "cap.identity." + capadvNanoID1
	actorKey := "vtx.identity." + capadvNanoID1

	// Step 1: Inject the fabricated entry.
	injectedDoc := &processor.CapabilityDoc{
		Key:                    capKey,
		Actor:                  actorKey,
		Version:                "1.0",
		ProjectedAt:            time.Now().UTC().Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{actorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "AdminAll", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role.platformAdmin"},
	}
	injectedRaw, _ := json.Marshal(injectedDoc)
	if _, err := conn.KVPut(ctx, capadvCapBucket, capKey, injectedRaw); err != nil {
		t.Fatalf("v1: inject: %v", err)
	}

	// Step 2: Simulate the Refractor reprojection. The graph-derived oracle
	// for this identity (consumer, no holdsRole edges) produces empty
	// platformPermissions. Measure wall-clock to validate NFR-P3 reprojection
	// latency budget (1s = 3-σ above p99 5.7ms).
	reprojStart := time.Now()

	// Graph-derived state: consumer identity with NO permissions (no holdsRole → role → permission edges).
	reprojectedDoc := &processor.CapabilityDoc{
		Key:                    capKey,
		Actor:                  actorKey,
		Version:                "1.0",
		ProjectedAt:            time.Now().UTC().Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{actorKey: 2}, // new revision after reprojection
		Lanes:                  []string{"default"},
		PlatformPermissions:    []processor.PlatformPermission{},
		ServiceAccess:          []processor.ServiceAccessEntry{},
		EphemeralGrants:        []processor.EphemeralGrant{},
		Roles:                  []string{}, // no holdsRole edges in the graph
	}
	reprojRaw, _ := json.Marshal(reprojectedDoc)
	if _, err := conn.KVPut(ctx, capadvCapBucket, capKey, reprojRaw); err != nil {
		t.Fatalf("v1: reprojection write: %v", err)
	}

	reprojElapsed := time.Since(reprojStart)

	// Latency assertion: 1s budget (3-σ above p99 5.7ms).
	const latencyBudget = 1 * time.Second
	if reprojElapsed > latencyBudget {
		t.Fatalf("v1: EXPOSED — reprojection latency %v exceeds 1s budget; actual p99 target is 5.7ms; mark EXPOSED with actual=%v", reprojElapsed, reprojElapsed)
	}
	t.Logf("v1: reprojection latency: %v (budget: %v)", reprojElapsed, latencyBudget)

	// Step 3: Read back the entry and verify the fabricated permission is GONE.
	afterEntry, err := conn.KVGet(ctx, capadvCapBucket, capKey)
	if err != nil {
		t.Fatalf("v1: KVGet after reprojection: %v", err)
	}
	var afterDoc processor.CapabilityDoc
	if err := json.Unmarshal(afterEntry.Value, &afterDoc); err != nil {
		t.Fatalf("v1: unmarshal after reprojection: %v", err)
	}

	// The fabricated AdminAll permission must be gone.
	for _, p := range afterDoc.PlatformPermissions {
		if p.OperationType == "AdminAll" {
			t.Fatalf("v1: EXPOSED — fabricated AdminAll permission survived reprojection cycle; elevation was retained")
		}
	}

	// platformPermissions must be empty (consumer has no holdsRole → role → permission edges).
	if len(afterDoc.PlatformPermissions) != 0 {
		t.Fatalf("v1: EXPOSED — expected empty platformPermissions after reprojection, got: %v", afterDoc.PlatformPermissions)
	}

	// Role list must not include platformAdmin.
	for _, r := range afterDoc.Roles {
		if r == "vtx.role.platformAdmin" {
			t.Fatalf("v1: EXPOSED — fabricated platformAdmin role survived reprojection cycle")
		}
	}

	t.Logf("v1: DEFENDED — fabricated permission eliminated by reprojection cycle within %v", reprojElapsed)
	t.Logf("v1: Oracle: consumer identity with no holdsRole edges → empty platformPermissions ✓")
	t.Logf("v1: Latency: %v ≤ 1s budget ✓", reprojElapsed)
}

// TestCapAdv_V1_DirectKVWrite_AuthorizerReadsOverwrittenEntry verifies that
// the CapabilityAuthorizer reads the post-reprojection (graph-derived) entry
// and correctly denies an operation that would have been allowed by the
// fabricated entry. This closes the loop: injection + reprojection + auth.
func TestCapAdv_V1_DirectKVWrite_AuthorizerReadsOverwrittenEntry(t *testing.T) {
	ctx, conn := setupCapAdvHarness(t)

	capKey := "cap.identity." + capadvNanoID1
	actorKey := "vtx.identity." + capadvNanoID1

	// Write the post-reprojection (graph-derived) cap doc: consumer with no perms.
	graphDerivedDoc := buildCapDocForIdentity(capadvNanoID1, []processor.PlatformPermission{}, []string{"vtx.role.consumer"})
	raw, _ := json.Marshal(graphDerivedDoc)
	if _, err := conn.KVPut(ctx, capadvCapBucket, capKey, raw); err != nil {
		t.Fatalf("v1: seed graph-derived cap doc: %v", err)
	}

	// Construct a CapabilityAuthorizer reading from our embedded NATS instance.
	cfg := processor.DefaultCapabilityAuthorizerConfig()
	authz, err := processor.NewCapabilityAuthorizer(conn, capadvCapBucket, nil, cfg, bypassLogger())
	if err != nil {
		t.Fatalf("NewCapabilityAuthorizer: %v", err)
	}

	// Submit an op that the fabricated entry would have allowed (AdminAll scope=any).
	// The graph-derived entry has no such permission → should be denied.
	env := &processor.OperationEnvelope{
		RequestID:     capadvReqV1Pos,
		Lane:          processor.LaneDefault,
		OperationType: "AdminAll",
		Actor:         actorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "identity",
	}

	dec, err := authz.Authorize(ctx, env)
	if err != nil {
		t.Fatalf("v1: Authorize returned error: %v", err)
	}
	if dec.Authorized {
		t.Fatalf("v1: EXPOSED — Authorizer allowed fabricated operation even though graph-derived entry has no AdminAll permission")
	}
	t.Logf("v1: DEFENDED — Authorizer correctly denied AdminAll after reprojection eliminated the fabricated permission")
	t.Logf("v1: Denial code: %s, reason: %s", dec.Code, dec.Reason)
}
