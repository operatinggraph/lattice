// Package bypass — Phase 1 Gate 3: Capability Lens adversarial test suite.
//
// Vector #2 — Projection lag window. ACCEPTED-WINDOW posture.
//
// Attack: An actor's cap entry is stale (not yet reprojected after a RevokeRole
// commit). A rogue actor exploits the CDC-to-projection lag window to submit
// operations that the revoked identity should no longer be permitted to perform.
//
// Posture (Story 1.5.4 §3.3): the per-operation projection-freshness gate has
// been REMOVED. A stale-but-permission-matching projection no longer denies at
// the Processor. The bounded staleness window is an ACCEPTED risk:
//   - Normal lag (<500ms p99, NFR-S7): event-driven reprojection converges; the
//     stale-but-recent entry is acceptable and the action is observable in the
//     auth-trace.
//   - Excessive lag / projector grossly behind: accepted at the Processor (no
//     per-op denial). The projector-death case is enforced operationally
//     (Refractor Capability-Lens health) and, for hard identity/session
//     revocation, by the Gateway JWT-revocation path (future).
//
// The NoCapabilityEntry check still denies a missing doc (covered by the
// direct-KV-write vector, not here).
//
// Approach: We inject a fake projectedAt directly into the cap entry rather than
// wall-clock manipulation — simpler + deterministic. projectedAt is now
// deterministic provenance ("as-of input state"), not a freshness ceiling.
//
// ACCEPTED-WINDOW when: a grossly-stale projection still ALLOWS the operation on
// a permission match (no denial), and the cap doc + auth-trace remain observable.
//
// Report row:
//
//	Vector #2 | Projection lag window | ACCEPTED-WINDOW | bounded; operational + Gateway enforcement (1.5.4)
package bypass

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
)

// fixedClock is a test-only Clock that returns a fixed time. Retained for the
// ephemeralGrant-expiry comparisons that still use the injected clock.
type fixedClock struct {
	t time.Time
}

func (c fixedClock) Now() time.Time { return c.t }

// buildStalecapDoc builds a CapabilityDoc with an injected projectedAt that is
// `age` before `now`. Used to simulate CDC-to-projection lag.
func buildStaleCapDoc(nanoID string, perms []processor.PlatformPermission, roles []string, now time.Time, age time.Duration) *processor.CapabilityDoc {
	capKey := "cap.identity." + nanoID
	actorKey := "vtx.identity." + nanoID
	projectedAt := now.Add(-age)
	return &processor.CapabilityDoc{
		Key:                    capKey,
		Actor:                  actorKey,
		Version:                "1.0",
		ProjectedAt:            projectedAt.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{actorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions:    perms,
		ServiceAccess:          []processor.ServiceAccessEntry{},
		EphemeralGrants:        []processor.EphemeralGrant{},
		Roles:                  roles,
	}
}

// seedStaleCapEntry writes a stale CapabilityDoc to Capability KV.
func seedStaleCapEntry(t *testing.T, ctx context.Context, conn *substrate.Conn, doc *processor.CapabilityDoc) {
	t.Helper()
	js := conn.JetStream()
	capKV, err := js.KeyValue(ctx, capadvCapBucket)
	if err != nil {
		t.Fatalf("v2: open capability-kv: %v", err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("v2: marshal cap doc: %v", err)
	}
	capKey := doc.Key
	if _, err := capKV.Put(ctx, capKey, raw); err != nil {
		t.Fatalf("v2: seed stale cap entry %q: %v", capKey, err)
	}
}

// TestCapAdv_V2_ProjectionLag_NormalLag_Allowed verifies that an operation from
// an actor whose cap entry is stale within the normal lag window is ALLOWED on a
// permission match. The op proceeds and is observable in the auth-trace.
func TestCapAdv_V2_ProjectionLag_NormalLag_Allowed(t *testing.T) {
	ctx, conn := setupCapAdvHarness(t)

	now := time.Now().UTC()
	// Normal lag: 100ms.
	normalLag := 100 * time.Millisecond
	perms := []processor.PlatformPermission{
		{OperationType: "CreateRole", Scope: "any"},
	}
	staleDoc := buildStaleCapDoc(capadvNanoID2, perms, []string{"vtx.role.operator"}, now, normalLag)
	seedStaleCapEntry(t, ctx, conn, staleDoc)

	clock := fixedClock{t: now}
	cfg := processor.DefaultCapabilityAuthorizerConfig()
	authz, err := processor.NewCapabilityAuthorizer(conn, capadvCapBucket, clock, cfg, bypassLogger())
	if err != nil {
		t.Fatalf("NewCapabilityAuthorizer: %v", err)
	}

	env := &processor.OperationEnvelope{
		RequestID:     capadvReqV2Op1,
		Lane:          processor.LaneDefault,
		OperationType: "CreateRole",
		Actor:         "vtx.identity." + capadvNanoID2,
		SubmittedAt:   now.Format(time.RFC3339),
		Class:         "role",
	}

	dec, err := authz.Authorize(ctx, env)
	if err != nil {
		t.Fatalf("v2 NormalLag: Authorize error: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("v2 NormalLag: expected ALLOWED for normal lag (%v), got denied: code=%s reason=%s",
			normalLag, dec.Code, dec.Reason)
	}
	if dec.Resolved == nil || dec.Resolved.ProjectedAt != staleDoc.ProjectedAt {
		t.Fatalf("v2 NormalLag: expected projectedAt threaded as provenance; got %+v", dec.Resolved)
	}

	t.Logf("v2 NormalLag: ACCEPTED-WINDOW — normal lag (%v) allowed; projectedAt observable as provenance", normalLag)
}

// TestCapAdv_V2_ProjectionLag_ExcessiveLag_NoLongerDenies verifies that an
// operation from an actor whose cap entry is grossly stale is STILL ALLOWED on a
// permission match — the per-operation freshness gate was removed (Story 1.5.4
// §3.1). The bounded window is an accepted risk; enforcement of the
// projector-death case is operational + Gateway (future), not a per-op denial.
func TestCapAdv_V2_ProjectionLag_ExcessiveLag_NoLongerDenies(t *testing.T) {
	ctx, conn := setupCapAdvHarness(t)

	now := time.Now().UTC()
	// Grossly stale: 1 hour — far beyond any former ceiling.
	excessiveLag := time.Hour
	perms := []processor.PlatformPermission{
		{OperationType: "CreateRole", Scope: "any"},
	}
	staleDoc := buildStaleCapDoc(capadvNanoID2, perms, []string{"vtx.role.operator"}, now, excessiveLag)
	seedStaleCapEntry(t, ctx, conn, staleDoc)

	clock := fixedClock{t: now}
	cfg := processor.DefaultCapabilityAuthorizerConfig()
	authz, err := processor.NewCapabilityAuthorizer(conn, capadvCapBucket, clock, cfg, bypassLogger())
	if err != nil {
		t.Fatalf("NewCapabilityAuthorizer: %v", err)
	}

	env := &processor.OperationEnvelope{
		RequestID:     capadvReqV2Op2,
		Lane:          processor.LaneDefault,
		OperationType: "CreateRole",
		Actor:         "vtx.identity." + capadvNanoID2,
		SubmittedAt:   now.Format(time.RFC3339),
		Class:         "role",
	}

	dec, err := authz.Authorize(ctx, env)
	if err != nil {
		t.Fatalf("v2 ExcessiveLag: Authorize error: %v", err)
	}

	// ACCEPTED-WINDOW: grossly-stale projection no longer denies; the operation
	// proceeds on the permission match.
	if !dec.Authorized {
		t.Fatalf("v2 ExcessiveLag: expected ALLOWED (freshness gate removed); got denied: code=%s reason=%s",
			dec.Code, dec.Reason)
	}
	// The cap doc remains observable: projectedAt is threaded as provenance.
	if dec.Resolved == nil || dec.Resolved.ProjectedAt != staleDoc.ProjectedAt {
		t.Fatalf("v2 ExcessiveLag: expected projectedAt threaded as provenance; got %+v", dec.Resolved)
	}

	t.Logf("v2 ExcessiveLag: ACCEPTED-WINDOW — grossly-stale projection (%v) allowed on permission match; bounded window accepted (operational + Gateway enforcement)", excessiveLag)
}

// TestCapAdv_V2_ProjectionLag_AuthTraceVerifiable verifies that the auth trace
// for a stale-projection allow is written to Health KV and queryable. The trace
// captures projectedAt (provenance), keeping the accepted window observable per
// NFR-S7. Trace key: health.processor.<instance>.auth-trace.<requestId>.
func TestCapAdv_V2_ProjectionLag_AuthTraceVerifiable(t *testing.T) {
	ctx, conn := setupCapAdvHarness(t)

	seedCapAdvDDL(t, ctx, conn)

	now := time.Now().UTC()
	excessiveLag := time.Hour
	perms := []processor.PlatformPermission{
		{OperationType: "CreateRole", Scope: "any"},
	}
	staleDoc := buildStaleCapDoc(capadvNanoID2, perms, []string{"vtx.role.operator"}, now, excessiveLag)
	seedStaleCapEntry(t, ctx, conn, staleDoc)

	instanceID := "capadv-v2-trace-test1"

	// traceAllowDecisions=true: the stale-projection allow is the observable
	// signal now that the freshness gate no longer denies.
	traceEmitter := processor.NewAuthTraceEmitter(conn, capadvHealthBucket, instanceID, true, bypassLogger())

	staleProjAt := now.Add(-excessiveLag).Format(time.RFC3339Nano)
	doc := &processor.CapabilityDoc{
		Key:                    "cap.identity." + capadvNanoID2,
		Actor:                  "vtx.identity." + capadvNanoID2,
		Version:                "1.0",
		ProjectedAt:            staleProjAt,
		ProjectedFromRevisions: map[string]uint64{"vtx.identity." + capadvNanoID2: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions:    perms,
		ServiceAccess:          []processor.ServiceAccessEntry{},
		EphemeralGrants:        []processor.EphemeralGrant{},
		Roles:                  []string{"vtx.role.operator"},
	}

	env := &processor.OperationEnvelope{
		RequestID:     "CdV2TrRq2345678912h",
		Lane:          processor.LaneDefault,
		OperationType: "CreateRole",
		Actor:         "vtx.identity." + capadvNanoID2,
		SubmittedAt:   now.Format(time.RFC3339),
		Class:         "role",
	}

	// A stale-but-permission-matching projection now produces an ALLOW; the
	// trace records the allow and the stale projectedAt provenance.
	allowDecision := processor.Decision{
		Authorized: true,
		Resolved: &processor.ResolvedPermission{
			CapKey:      doc.Key,
			ProjectedAt: staleProjAt,
		},
	}
	traceEmitter.Emit(env, allowDecision)

	time.Sleep(200 * time.Millisecond)

	traceKey := "health.processor." + instanceID + ".auth-trace." + env.RequestID
	traceEntry, err := conn.KVGet(ctx, capadvHealthBucket, traceKey)
	if err != nil {
		t.Fatalf("v2 Trace: trace key not found at %q: %v", traceKey, err)
	}

	var rec processor.AuthTraceRecord
	if err := json.Unmarshal(traceEntry.Value, &rec); err != nil {
		t.Fatalf("v2 Trace: unmarshal trace record: %v", err)
	}

	if rec.AuthOutcome != "allowed" {
		t.Fatalf("v2 Trace: expected authOutcome=allowed, got %q", rec.AuthOutcome)
	}
	// Plane 1 must capture the projected-at provenance from the stale doc.
	if rec.Plane1.ProjectedAt != staleProjAt {
		t.Fatalf("v2 Trace: plane1.projectedAt mismatch: got %q, want %q", rec.Plane1.ProjectedAt, staleProjAt)
	}

	t.Logf("v2 Trace: ACCEPTED-WINDOW — auth trace present at %q for stale-projection allow; plane1.projectedAt=%q observable", traceKey, rec.Plane1.ProjectedAt)
}

// seedCapAdvDDL writes a minimal role DDL meta-vertex to Core KV so the
// CommitPath hydrator can resolve the "role" class. Mirrors the pattern
// from role_mgmt_integration_test.go.
func seedCapAdvDDL(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	const script = `
def execute(state, op):
    ot = op.operationType
    p = op.payload
    if ot == "CreateRole":
        role_id = nanoid.new()
        role_key = "vtx.role." + role_id
        return {"mutations": [{"op": "create", "key": role_key, "document": {"class": "role", "isDeleted": False, "data": {"name": p.name}}}], "events": [{"class": "rbac.roleCreated", "data": {"roleKey": role_key}}]}
    if ot == "ApproveLeaseApplication":
        return {"mutations": [], "events": [{"class": "loftspace.leaseApproved", "data": {"target": p.get("target","")}}]}
    fail("capadv DDL: unknown op: " + ot)
`
	ddlDoc := map[string]any{
		"class":     "meta.ddl.vertexType",
		"isDeleted": false,
		"data": map[string]any{
			"canonicalName":     "role",
			"permittedCommands": []string{"CreateRole", "ApproveLeaseApplication"},
		},
	}
	scriptDoc := map[string]any{
		"class":     "meta.script",
		"isDeleted": false,
		"data":      map[string]any{"source": script},
	}
	ddlBytes, _ := json.Marshal(ddlDoc)
	scriptBytes, _ := json.Marshal(scriptDoc)

	if _, err := conn.KVPut(ctx, capadvCoreBucket, "vtx.meta.role", ddlBytes); err != nil {
		t.Fatalf("capadv: seed DDL: %v", err)
	}
	if _, err := conn.KVPut(ctx, capadvCoreBucket, "vtx.meta.role.script", scriptBytes); err != nil {
		t.Fatalf("capadv: seed DDL script: %v", err)
	}
}
