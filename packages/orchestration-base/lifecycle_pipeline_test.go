// End-to-end lifecycle-op tests through the full Processor pipeline. They seed
// a real task (via CreateTask) then drive ReAssignTask / CompleteTask /
// CancelTask, asserting the committed Core KV effect, the emitted event, and
// the OCC behaviour (hydrated revision flows into the update mutation).
package orchestrationbase_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// seedTask runs CreateTask and returns the committed task key + the three
// endpoint ids/keys.
func seedTask(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, assigneeID string) (taskKey, taskID, assigneeKey, opID, targetType, targetID string) {
	t.Helper()
	assigneeKey = "vtx.identity." + assigneeID
	opID = "BBapproveBpHJKMNPQRS"
	opKey := "vtx.meta." + opID
	targetType = "leaseapp"
	targetID = "BBease4ppHJKMNPQRSTU"
	targetKey := "vtx." + targetType + "." + targetID
	seedVertex(t, ctx, conn, assigneeKey, "identity", map[string]any{"state": "claimed"})
	seedVertex(t, ctx, conn, opKey, "meta", map[string]any{"operationType": "ApproveLeaseApplication"})
	seedVertex(t, ctx, conn, targetKey, targetType, map[string]any{"state": "pending"})

	reqID := testutil.GenReqID(label)
	taskID = taskIDFromRequestID(reqID)
	taskKey = "vtx.task." + taskID
	expiresAt := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateTask",
		Actor:         otStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "task",
		Payload: json.RawMessage(`{"assignee":"` + assigneeKey + `","forOperation":"` + opKey +
			`","scopedTo":"` + targetKey + `","expiresAt":"` + expiresAt + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{assigneeKey, opKey, targetKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return taskKey, taskID, assigneeKey, opID, targetType, targetID
}

func taskStatus(t *testing.T, ctx context.Context, conn *substrate.Conn, taskKey string) string {
	t.Helper()
	doc := readDoc(t, ctx, conn, taskKey)
	data, _ := doc["data"].(map[string]any)
	s, _ := data["status"].(string)
	return s
}

func linkExists(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) bool {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		return false
	}
	var doc map[string]any
	_ = json.Unmarshal(entry.Value, &doc)
	if del, _ := doc["isDeleted"].(bool); del {
		return false
	}
	return true
}

// TestCompleteTask_E2E: open→complete commits and emits TaskCompleted.
func TestCompleteTask_E2E(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "complete-e2e")

	taskKey, _, _, _, _, _ := seedTask(t, ctx, conn, cp, cons, "CmpltSeed0001", "BBcmpAssignHJKMNPQRS")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("CmpltOp000001"),
		Lane:          processor.LaneDefault,
		OperationType: "CompleteTask",
		Actor:         otStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "task",
		Payload:       json.RawMessage(`{"taskKey":"` + taskKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{taskKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := taskStatus(t, ctx, conn, taskKey); got != "complete" {
		t.Fatalf("task status = %q, want complete", got)
	}
	assertTrackerEvent(t, ctx, conn, env.RequestID, "orchestration.taskCompleted")
}

// TestCompleteTask_Redelivery_E2E_DedupNoDoubleEmit: a CompleteTask op that is
// redelivered (same RequestID) is short-circuited by the step-2 dedup tracker
// (Contract #4 §4.2) — the status stays complete and TaskCompleted is recorded
// exactly once, never appended a second time. This is the §0.4 ruling: honest
// redeliveries are absorbed by the dedup tracker, not re-committed.
func TestCompleteTask_Redelivery_E2E_DedupNoDoubleEmit(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "complete-redeliver")

	taskKey, _, _, _, _, _ := seedTask(t, ctx, conn, cp, cons, "RedlvSeed0001", "BBredAssignHJKMNPQRS")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("RedlvOp000001"),
		Lane:          processor.LaneDefault,
		OperationType: "CompleteTask",
		Actor:         otStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "task",
		Payload:       json.RawMessage(`{"taskKey":"` + taskKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{taskKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := taskStatus(t, ctx, conn, taskKey); got != "complete" {
		t.Fatalf("task status after first complete = %q, want complete", got)
	}
	assertTrackerEvent(t, ctx, conn, env.RequestID, "orchestration.taskCompleted")
	if n := trackerEventCount(t, ctx, conn, env.RequestID, "orchestration.taskCompleted"); n != 1 {
		t.Fatalf("TaskCompleted recorded %d times after first commit, want 1", n)
	}

	// Redeliver the identical op (same RequestID). Step-2 dedup short-circuits.
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeDuplicate)

	if got := taskStatus(t, ctx, conn, taskKey); got != "complete" {
		t.Fatalf("task status after redelivery = %q, want complete (unchanged)", got)
	}
	// The dedup short-circuit must not append a second TaskCompleted, nor
	// fabricate any other lifecycle event onto this op's tracker.
	if n := trackerEventCount(t, ctx, conn, env.RequestID, "orchestration.taskCompleted"); n != 1 {
		t.Fatalf("TaskCompleted recorded %d times after redelivery, want 1 (no double-emit)", n)
	}
	assertTrackerNotEvent(t, ctx, conn, env.RequestID, "orchestration.taskCancelled")
}

// TestCancelTask_E2E: open→cancelled commits and emits TaskCancelled.
func TestCancelTask_E2E(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "cancel-e2e")

	taskKey, _, _, _, _, _ := seedTask(t, ctx, conn, cp, cons, "CnclSeed00001", "BBcnxAssignHJKMNPQRS")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("CnclOp0000001"),
		Lane:          processor.LaneDefault,
		OperationType: "CancelTask",
		Actor:         otStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "task",
		Payload:       json.RawMessage(`{"taskKey":"` + taskKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{taskKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := taskStatus(t, ctx, conn, taskKey); got != "cancelled" {
		t.Fatalf("task status = %q, want cancelled", got)
	}
	assertTrackerEvent(t, ctx, conn, env.RequestID, "orchestration.taskCancelled")
}

// TestCompleteTask_OfCancelled_E2E_Rejected is the named AC invariant through
// the full pipeline: cancel the task, then a CompleteTask is rejected and the
// status stays cancelled.
func TestCompleteTask_OfCancelled_E2E_Rejected(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "complete-of-cancelled")

	taskKey, _, _, _, _, _ := seedTask(t, ctx, conn, cp, cons, "CoCSeed000001", "BBcocAssignHJKMNPQRS")

	cancelEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("CoCCancel0001"),
		Lane:          processor.LaneDefault,
		OperationType: "CancelTask",
		Actor:         otStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "task",
		Payload:       json.RawMessage(`{"taskKey":"` + taskKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{taskKey}},
	}
	testutil.PublishOp(t, conn, cancelEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	completeEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("CoCComplete01"),
		Lane:          processor.LaneDefault,
		OperationType: "CompleteTask",
		Actor:         otStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "task",
		Payload:       json.RawMessage(`{"taskKey":"` + taskKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{taskKey}},
	}
	testutil.PublishOp(t, conn, completeEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	if got := taskStatus(t, ctx, conn, taskKey); got != "cancelled" {
		t.Fatalf("task status after rejected complete = %q, want cancelled", got)
	}
}

// TestReAssignTask_E2E: re-points the assignedTo link atomically (old gone,
// new present), task stays open, TaskReAssigned emitted.
func TestReAssignTask_E2E(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "reassign-e2e")

	oldAssigneeID := "BBraUxdAssignHJKMNPQ"
	taskKey, taskID, _, _, _, _ := seedTask(t, ctx, conn, cp, cons, "RAsgSeed00001", oldAssigneeID)

	newAssigneeID := "BBraNewAssignHJKMNPQ"
	newAssigneeKey := "vtx.identity." + newAssigneeID
	seedVertex(t, ctx, conn, newAssigneeKey, "identity", map[string]any{"state": "claimed"})

	oldLnk := "lnk.task." + taskID + ".assignedTo.identity." + oldAssigneeID
	newLnk := "lnk.task." + taskID + ".assignedTo.identity." + newAssigneeID

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("RAsgOp000001"),
		Lane:          processor.LaneDefault,
		OperationType: "ReAssignTask",
		Actor:         otStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "task",
		Payload:       json.RawMessage(`{"taskKey":"` + taskKey + `","newAssignee":"` + newAssigneeKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{taskKey, oldLnk, newAssigneeKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if linkExists(t, ctx, conn, oldLnk) {
		t.Fatalf("old assignedTo link %s must be removed after reassign", oldLnk)
	}
	if !linkExists(t, ctx, conn, newLnk) {
		t.Fatalf("new assignedTo link %s must exist after reassign", newLnk)
	}
	if got := taskStatus(t, ctx, conn, taskKey); got != "open" {
		t.Fatalf("task status after reassign = %q, want open", got)
	}
	assertTrackerEvent(t, ctx, conn, env.RequestID, "orchestration.taskReAssigned")
}

// TestReAssignTask_AbsentNewAssignee_E2E_Rejected: the no-orphan invariant
// through the full pipeline — a reassign to a non-existent identity is
// rejected, and the original link survives.
func TestReAssignTask_AbsentNewAssignee_E2E_Rejected(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "reassign-absent")

	oldAssigneeID := "BBraAbsUxdAsgHJKMNPQ"
	taskKey, taskID, _, _, _, _ := seedTask(t, ctx, conn, cp, cons, "RAsgAbsSeed01", oldAssigneeID)

	missingAssignee := "vtx.identity.BBraMissingHJKMNPQRS"
	oldLnk := "lnk.task." + taskID + ".assignedTo.identity." + oldAssigneeID

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("RAsgAbsOp0001"),
		Lane:          processor.LaneDefault,
		OperationType: "ReAssignTask",
		Actor:         otStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "task",
		Payload:       json.RawMessage(`{"taskKey":"` + taskKey + `","newAssignee":"` + missingAssignee + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{taskKey, oldLnk, missingAssignee}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	if !linkExists(t, ctx, conn, oldLnk) {
		t.Fatalf("old assignedTo link %s must survive a rejected reassign", oldLnk)
	}
}
