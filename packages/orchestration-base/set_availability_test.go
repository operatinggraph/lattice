// SetAvailability + CreateTask availability-gated routing integration tests
// (FR28 Fire 2, Contract #10 §10.1).
//
// Same harness as create_task_test.go: seed the kernel, install
// rbac-domain + identity-domain + orchestration-base through the Processor,
// submit ops, assert outcomes.
package orchestrationbase_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// TestSetAvailability_Success writes the identity's availability aspect.
func TestSetAvailability_Success(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "sa-success")

	identityKey := "vtx.identity.BBidentityHJKMNPQRST"
	seedVertex(t, ctx, conn, identityKey, "identity", map[string]any{"state": "claimed"})

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("SASuccess0001"),
		Lane:          processor.LaneDefault,
		OperationType: "SetAvailability",
		Actor:         otStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "task",
		Payload:       json.RawMessage(`{"identity":"` + identityKey + `","available":false}`),
		ContextHint:   &processor.ContextHint{Reads: []string{identityKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	doc := readDoc(t, ctx, conn, identityKey+".availability")
	data, _ := doc["data"].(map[string]any)
	if got, _ := data["available"].(bool); got != false {
		t.Fatalf("availability.data.available = %v, want false", got)
	}
}

// TestSetAvailability_Toggle proves the aspect is an unconditioned
// create-if-absent/overwrite-if-present upsert: flipping available back and
// forth twice must not conflict on a stale expectedRevision.
func TestSetAvailability_Toggle(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "sa-toggle")

	identityKey := "vtx.identity.BBswappedHJKMNPQRSTU"
	seedVertex(t, ctx, conn, identityKey, "identity", map[string]any{"state": "claimed"})

	for i, available := range []bool{false, true, false} {
		env := &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID("SAToggle" + boolTag(available) + string(rune('A'+i))),
			Lane:          processor.LaneDefault,
			OperationType: "SetAvailability",
			Actor:         otStaffActorKey,
			SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
			Class:         "task",
			Payload:       json.RawMessage(`{"identity":"` + identityKey + `","available":` + boolJSON(available) + `}`),
			ContextHint:   &processor.ContextHint{Reads: []string{identityKey}},
		}
		testutil.PublishOp(t, conn, env)
		testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

		doc := readDoc(t, ctx, conn, identityKey+".availability")
		data, _ := doc["data"].(map[string]any)
		if got, _ := data["available"].(bool); got != available {
			t.Fatalf("after toggle to %v: availability.data.available = %v", available, got)
		}
	}
}

// TestSetAvailability_UnknownIdentity_Rejected: the no-orphan discipline
// this DDL applies everywhere -- the identity must exist and be alive.
func TestSetAvailability_UnknownIdentity_Rejected(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "sa-unknown")

	missingIdentity := "vtx.identity.BBmissingHJKMNPQRSTU"

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("SAUnknown0001"),
		Lane:          processor.LaneDefault,
		OperationType: "SetAvailability",
		Actor:         otStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "task",
		Payload:       json.RawMessage(`{"identity":"` + missingIdentity + `","available":false}`),
		ContextHint:   &processor.ContextHint{Reads: []string{missingIdentity}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestSetAvailability_MissingAvailable_Rejected: available is a required
// bool, not silently defaulted (a routing input must be an explicit choice).
func TestSetAvailability_MissingAvailable_Rejected(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "sa-missingbool")

	identityKey := "vtx.identity.BBzxvmqrpHJKMNPQRSTU"
	seedVertex(t, ctx, conn, identityKey, "identity", map[string]any{"state": "claimed"})

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("SANoBool00001"),
		Lane:          processor.LaneDefault,
		OperationType: "SetAvailability",
		Actor:         otStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "task",
		Payload:       json.RawMessage(`{"identity":"` + identityKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{identityKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestCreateTask_UnavailableAssignee_FallsBackToQueue: SetAvailability(false)
// on the assignee, then CreateTask with both assignee+queue -- the task must
// route queuedFor the role, NOT assignedTo the (alive but unavailable)
// assignee.
func TestCreateTask_UnavailableAssignee_FallsBackToQueue(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "ct-unavail-fallback")

	assigneeID := "BBoffdaysHJKMNPQRSTU"
	assigneeKey := "vtx.identity." + assigneeID
	roleID := "BBteamqueueHJKMNPQRS"
	roleKey := "vtx.role." + roleID
	opKey := "vtx.meta.BBapproveBpHJKMNPQRS"
	targetKey := "vtx.leaseapp.BBease4ppHJKMNPQRSTU"
	seedVertex(t, ctx, conn, assigneeKey, "identity", map[string]any{"state": "claimed"})
	seedVertex(t, ctx, conn, roleKey, "role", nil)
	seedVertex(t, ctx, conn, opKey, "meta", map[string]any{"operationType": "ApproveLeaseApplication"})
	seedVertex(t, ctx, conn, targetKey, "leaseapp", map[string]any{"state": "pending"})

	// Mark the assignee unavailable first.
	saEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("CTUnavailSA01"),
		Lane:          processor.LaneDefault,
		OperationType: "SetAvailability",
		Actor:         otStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "task",
		Payload:       json.RawMessage(`{"identity":"` + assigneeKey + `","available":false}`),
		ContextHint:   &processor.ContextHint{Reads: []string{assigneeKey}},
	}
	testutil.PublishOp(t, conn, saEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	reqID := testutil.GenReqID("CTUnavailCT01")
	taskID := taskIDFromRequestID(reqID)
	expiresAt := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)

	ctEnv := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateTask",
		Actor:         otStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "task",
		Payload: json.RawMessage(`{"assignee":"` + assigneeKey + `","queue":"` + roleKey + `","forOperation":"` + opKey +
			`","scopedTo":"` + targetKey + `","expiresAt":"` + expiresAt + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{assigneeKey, roleKey, opKey, targetKey}},
	}
	testutil.PublishOp(t, conn, ctEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	queuedLnk := "lnk.task." + taskID + ".queuedFor.role." + roleID
	if got, _ := readDoc(t, ctx, conn, queuedLnk)["targetVertex"].(string); got != roleKey {
		t.Fatalf("queuedFor targetVertex = %q, want %q (routed to queue, not the unavailable assignee)", got, roleKey)
	}

	assignedLnk := "lnk.task." + taskID + ".assignedTo.identity." + assigneeID
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, assignedLnk); err == nil {
		t.Fatalf("assignedTo link %q must NOT exist -- the unavailable assignee must not be assigned", assignedLnk)
	}
}

// TestCreateTask_UnavailableAssignee_NoQueue_Rejected: an unavailable
// assignee with no queue given must reject RoutingFailed -- no silent drop.
func TestCreateTask_UnavailableAssignee_NoQueue_Rejected(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "ct-unavail-noqueue")

	assigneeKey := "vtx.identity.BBnoqueueHJKMNPQRSTU"
	opKey := "vtx.meta.BBapproveBpHJKMNPQRS"
	targetKey := "vtx.leaseapp.BBease4ppHJKMNPQRSTU"
	seedVertex(t, ctx, conn, assigneeKey, "identity", map[string]any{"state": "claimed"})
	seedVertex(t, ctx, conn, opKey, "meta", map[string]any{"operationType": "ApproveLeaseApplication"})
	seedVertex(t, ctx, conn, targetKey, "leaseapp", map[string]any{"state": "pending"})

	saEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("CTNoQSA000001"),
		Lane:          processor.LaneDefault,
		OperationType: "SetAvailability",
		Actor:         otStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "task",
		Payload:       json.RawMessage(`{"identity":"` + assigneeKey + `","available":false}`),
		ContextHint:   &processor.ContextHint{Reads: []string{assigneeKey}},
	}
	testutil.PublishOp(t, conn, saEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	expiresAt := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	ctEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("CTNoQCT000001"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateTask",
		Actor:         otStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "task",
		Payload: json.RawMessage(`{"assignee":"` + assigneeKey + `","forOperation":"` + opKey +
			`","scopedTo":"` + targetKey + `","expiresAt":"` + expiresAt + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{assigneeKey, opKey, targetKey}},
	}
	testutil.PublishOp(t, conn, ctEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestCreateTask_AvailableAssignee_DirectAssign_EvenWithQueueGiven: an
// available assignee wins over a given queue (the queue is pure fallback,
// never preferred).
func TestCreateTask_AvailableAssignee_DirectAssign_EvenWithQueueGiven(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "ct-avail-wins")

	assigneeID := "BBonshiftHJKMNPQRSTU"
	assigneeKey := "vtx.identity." + assigneeID
	roleKey := "vtx.role.BBteamqueueHJKMNPQRS"
	opKey := "vtx.meta.BBapproveBpHJKMNPQRS"
	targetKey := "vtx.leaseapp.BBease4ppHJKMNPQRSTU"
	seedVertex(t, ctx, conn, assigneeKey, "identity", map[string]any{"state": "claimed"})
	seedVertex(t, ctx, conn, roleKey, "role", nil)
	seedVertex(t, ctx, conn, opKey, "meta", map[string]any{"operationType": "ApproveLeaseApplication"})
	seedVertex(t, ctx, conn, targetKey, "leaseapp", map[string]any{"state": "pending"})

	// Explicitly mark available=true (not just absent) to prove the
	// aspect-present-and-true path also wins, not just the absent-aspect path.
	saEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("CTAvailSA0001"),
		Lane:          processor.LaneDefault,
		OperationType: "SetAvailability",
		Actor:         otStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "task",
		Payload:       json.RawMessage(`{"identity":"` + assigneeKey + `","available":true}`),
		ContextHint:   &processor.ContextHint{Reads: []string{assigneeKey}},
	}
	testutil.PublishOp(t, conn, saEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	reqID := testutil.GenReqID("CTAvailCT0001")
	taskID := taskIDFromRequestID(reqID)
	expiresAt := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	ctEnv := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateTask",
		Actor:         otStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "task",
		Payload: json.RawMessage(`{"assignee":"` + assigneeKey + `","queue":"` + roleKey + `","forOperation":"` + opKey +
			`","scopedTo":"` + targetKey + `","expiresAt":"` + expiresAt + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{assigneeKey, roleKey, opKey, targetKey}},
	}
	testutil.PublishOp(t, conn, ctEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	assignedLnk := "lnk.task." + taskID + ".assignedTo.identity." + assigneeID
	if got, _ := readDoc(t, ctx, conn, assignedLnk)["targetVertex"].(string); got != assigneeKey {
		t.Fatalf("assignedTo targetVertex = %q, want %q (available assignee must win over queue)", got, assigneeKey)
	}
}

func boolJSON(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func boolTag(b bool) string {
	if b {
		return "T00001"
	}
	return "F00001"
}
