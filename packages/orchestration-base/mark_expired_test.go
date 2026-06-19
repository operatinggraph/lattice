package orchestrationbase_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
)

// TestMarkExpired_UnconditionedUpdate_RepeatsAcrossCycles is the C2 correction's
// load-bearing case: MarkExpired writes the freshnessExpiry marker as an
// UNCONDITIONED update, so a SECOND fire on the same entity (a second freshness
// cycle) overwrites the standing marker rather than conflicting. A `create`
// would reject the second fire — silently turning the eager re-open into a
// one-shot. Both fires commit; the marker carries the latest expiredAt.
func TestMarkExpired_UnconditionedUpdate_RepeatsAcrossCycles(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "me-cycles")

	// Seed an entity of an arbitrary anchor type (leaseapp) — MarkExpired is
	// type-agnostic, so the concrete type is just data here.
	entityKey := "vtx.leaseapp.BBmarkexpiredHJKMNPQ"
	seedVertex(t, ctx, conn, entityKey, "leaseapp", map[string]any{})
	markerKey := entityKey + ".freshnessExpiry"

	// First lapse: the marker is created (the entity had none).
	first := "2026-06-18T14:00:00Z"
	submitMarkExpired(t, ctx, conn, cp, cons, "MEcycle000001", entityKey, first)
	got := readDoc(t, ctx, conn, markerKey)
	if data, _ := got["data"].(map[string]any); data["expiredAt"] != first {
		t.Fatalf("after first fire, marker expiredAt = %v, want %q", got["data"], first)
	}

	// Second lapse (a NEW freshness cycle): a different requestId + a later
	// expiredAt. The UNCONDITIONED update must OVERWRITE the standing marker —
	// not conflict. (A create-based marker would land OutcomeRejected here.)
	second := "2026-06-18T15:30:00Z"
	submitMarkExpired(t, ctx, conn, cp, cons, "MEcycle000002", entityKey, second)
	got = readDoc(t, ctx, conn, markerKey)
	if data, _ := got["data"].(map[string]any); data["expiredAt"] != second {
		t.Fatalf("after second fire, marker expiredAt = %v, want %q (overwrite, not conflict)", got["data"], second)
	}
}

// TestMarkExpired_TypeAgnostic proves the SAME MarkExpired DDL serves a
// different anchor type (an identity vertex) — the script names no concrete
// type, resolving the entity solely from payload.entityKey.
func TestMarkExpired_TypeAgnostic(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "me-typeagnostic")

	entityKey := "vtx.identity.BBmarkexpidHJKMNPQRS"
	seedVertex(t, ctx, conn, entityKey, "identity", map[string]any{"state": "claimed"})

	submitMarkExpired(t, ctx, conn, cp, cons, "MEtype0000001", entityKey, "2026-06-18T16:00:00Z")
	got := readDoc(t, ctx, conn, entityKey+".freshnessExpiry")
	if cls, _ := got["class"].(string); cls != "freshnessExpiry" {
		t.Fatalf("marker class = %q, want freshnessExpiry", cls)
	}
}

// TestMarkExpired_ClassInferredFromOperationType is the RF#1↔RF#3 join: MarkExpired
// dispatched with NO `class` field (exactly what Weaver's temporal lane sends)
// resolves its DDL via the operationType→class reverse index — freshnessMarker
// is the sole vertexType DDL admitting MarkExpired (the freshnessExpiry
// aspect-type DDL also lists it, but aspectType DDLs are excluded from the
// index), so inference is unambiguous and the marker commits.
func TestMarkExpired_ClassInferredFromOperationType(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "me-inferclass")

	entityKey := "vtx.leaseapp.BBmarkenferHJKMNPQRS"
	seedVertex(t, ctx, conn, entityKey, "leaseapp", map[string]any{})

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("MEinfer000001"),
		Lane:          processor.LaneDefault,
		OperationType: "MarkExpired",
		Actor:         otStaffActorKey,
		SubmittedAt:   "2026-06-18T17:00:00Z",
		// Class deliberately OMITTED — must be inferred from operationType.
		Payload: json.RawMessage(`{"entityKey":"` + entityKey + `","expiredAt":"2026-06-18T17:00:00Z"}`),
		// The DDL hydrates the entity ROOT (the target-existence guard); the
		// marker write itself is still unconditioned (no OCC on the marker).
		ContextHint: &processor.ContextHint{Reads: []string{entityKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	got := readDoc(t, ctx, conn, entityKey+".freshnessExpiry")
	if cls, _ := got["class"].(string); cls != "freshnessExpiry" {
		t.Fatalf("class-inferred MarkExpired marker class = %q, want freshnessExpiry", cls)
	}
}

// TestMarkExpired_AbsentEntity_Rejected is the C1 target-existence guard: a
// MarkExpired whose entityKey points at a vertex that does NOT exist must be
// rejected (NotFound) — no marker is written onto an absent parent. The marker
// aspect is non-sensitive, so step-6's sensitiveAspectScope never fires; this
// script-level vertex_alive on the hydrated root is the sole guard.
func TestMarkExpired_AbsentEntity_Rejected(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "me-absent")

	// Deliberately NOT seeded: the entity does not exist in Core KV.
	absentKey := "vtx.leaseapp.BBmarkabsentHJKMNPQ"

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("MEabsent00001"),
		Lane:          processor.LaneDefault,
		OperationType: "MarkExpired",
		Actor:         otStaffActorKey,
		SubmittedAt:   "2026-06-18T18:00:00Z",
		Class:         "freshnessMarker",
		Payload:       json.RawMessage(`{"entityKey":"` + absentKey + `","expiredAt":"2026-06-18T18:00:00Z"}`),
		// The DDL hydrates the root — which is absent, so the guard fails closed.
		ContextHint: &processor.ContextHint{Reads: []string{absentKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	// The marker aspect must NOT have been written.
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, absentKey+".freshnessExpiry"); err == nil {
		t.Fatalf("a marker aspect was written onto an absent entity (%s.freshnessExpiry must not exist)", absentKey)
	}
}

// TestMarkExpired_TombstonedEntity_Rejected proves the C1 guard also fires for a
// tombstoned (isDeleted) parent — a stale firing whose entity was deleted after
// the timer armed must not resurrect a marker on the dead vertex.
func TestMarkExpired_TombstonedEntity_Rejected(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "me-tomb")

	entityKey := "vtx.leaseapp.BBmarktombHJKMNPQRS"
	// Seed it tombstoned.
	dead := map[string]any{"class": "leaseapp", "isDeleted": true, "data": map[string]any{}}
	b, _ := json.Marshal(dead)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, entityKey, b); err != nil {
		t.Fatalf("seed tombstoned entity: %v", err)
	}

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("MEtomb000001"),
		Lane:          processor.LaneDefault,
		OperationType: "MarkExpired",
		Actor:         otStaffActorKey,
		SubmittedAt:   "2026-06-18T18:30:00Z",
		Class:         "freshnessMarker",
		Payload:       json.RawMessage(`{"entityKey":"` + entityKey + `","expiredAt":"2026-06-18T18:30:00Z"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{entityKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestMarkExpired_NonOperatorActor_Denied drives the REAL capability auth path
// (CapabilityPipeline, AuthModeCapability) for an actor whose cap doc grants
// other ops but NOT MarkExpired: the op must be DENIED at step 3 — proving the
// scope:any MarkExpired grant is correctly gated to the operator-equivalent
// service actor and not open to any caller. (The e2e runs AuthModeStub, so this
// is the place the grant is actually exercised against the validator.)
func TestMarkExpired_NonOperatorActor_Denied(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "me-denied")

	// A second actor with a cap doc that grants CreateTask but NOT MarkExpired,
	// and is NOT in the operator role — so the MarkExpired operator grant cannot
	// authorize it.
	const nonOpActorID = "BBnonopActHJKMNPQRST"
	const nonOpActorKey = "vtx.identity." + nonOpActorID
	const nonOpCapKey = "cap.identity." + nonOpActorID
	now := time.Now().UTC()
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key:                    nonOpCapKey,
		Actor:                  nonOpActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{nonOpActorKey: 1},
		Lanes:                  []string{"default"},
		// Grants an unrelated op; deliberately omits MarkExpired. No operator role.
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateTask", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{},
	})

	entityKey := "vtx.leaseapp.BBmarkdenyHJKMNPQRS"
	seedVertex(t, ctx, conn, entityKey, "leaseapp", map[string]any{})

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("MEdeny000001"),
		Lane:          processor.LaneDefault,
		OperationType: "MarkExpired",
		Actor:         nonOpActorKey,
		SubmittedAt:   "2026-06-18T19:00:00Z",
		Class:         "freshnessMarker",
		Payload:       json.RawMessage(`{"entityKey":"` + entityKey + `","expiredAt":"2026-06-18T19:00:00Z"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{entityKey}},
	}
	testutil.PublishOp(t, conn, env)
	// Auth denial surfaces as OutcomeRejected (step 3 terminates the op).
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	// And the marker must NOT have been written (the denial precedes any commit).
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, entityKey+".freshnessExpiry"); err == nil {
		t.Fatalf("a denied MarkExpired wrote a marker (%s.freshnessExpiry must not exist)", entityKey)
	}
}

// submitMarkExpired publishes one MarkExpired op (with explicit class) and drives
// it to OutcomeAccepted.
func submitMarkExpired(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, reqSeed, entityKey, expiredAt string) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(reqSeed),
		Lane:          processor.LaneDefault,
		OperationType: "MarkExpired",
		Actor:         otStaffActorKey,
		SubmittedAt:   expiredAt,
		Class:         "freshnessMarker",
		Payload:       json.RawMessage(`{"entityKey":"` + entityKey + `","expiredAt":"` + expiredAt + `"}`),
		// The DDL hydrates the entity ROOT for the target-existence guard.
		ContextHint: &processor.ContextHint{Reads: []string{entityKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
}
