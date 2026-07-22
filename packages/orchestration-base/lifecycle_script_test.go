// Script-level unit tests for the task DDL lifecycle branches (ReAssignTask,
// CompleteTask, CancelTask). They drive the DDL Starlark directly through the
// StarlarkRunner — no NATS — so the state-machine guards, OCC handle, and the
// atomic link re-point are exercised in isolation from the commit pipeline.
package orchestrationbase_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
)

const (
	lcTaskID    = "BBxctaskHJKMNPQRSTUV"
	lcTaskKey   = "vtx.task." + lcTaskID
	lcOldAssID  = "BBuxdassignHJKMNPQRS"
	lcOldAssign = "vtx.identity." + lcOldAssID
	lcNewAssID  = "BBnewassignHJKMNPQRS"
	lcNewAssign = "vtx.identity." + lcNewAssID
)

func lcTaskScript(t *testing.T) string {
	t.Helper()
	for _, d := range orchestrationbase.DDLs() {
		if d.CanonicalName == "task" {
			return d.Script
		}
	}
	t.Fatal("task DDL not found")
	return ""
}

// runLifecycle drives one lifecycle op against a hydrated state.
func runLifecycle(t *testing.T, op string, payload map[string]any, hydrated map[string]processor.VertexDoc) (processor.ScriptResult, error) {
	t.Helper()
	pb, _ := json.Marshal(payload)
	sc := processor.ScriptContext{
		Operation: &processor.OperationEnvelope{
			RequestID:     "BBlc" + op + "QRSTUVWX",
			Lane:          processor.LaneDefault,
			OperationType: op,
			Actor:         "vtx.identity.BBoperatorHJKMNPQRST",
			SubmittedAt:   "2026-06-04T00:00:00Z",
			Payload:       pb,
		},
		Hydrated:     hydrated,
		ScriptSource: lcTaskScript(t),
		ScriptClass:  "task",
	}
	return processor.NewStarlarkRunner(0, 0).Run(context.Background(), sc)
}

func taskRoot(status string, rev uint64) processor.VertexDoc {
	return processor.VertexDoc{
		Key: lcTaskKey, Class: "task", Revision: rev,
		Data: map[string]any{"status": status, "expiresAt": "2026-06-05T00:00:00Z"},
	}
}

func oldAssignedLink() processor.VertexDoc {
	lnk := "lnk.task." + lcTaskID + ".assignedTo.identity." + lcOldAssID
	return processor.VertexDoc{
		Key: lnk, Class: "assignedTo", VertexKey: lcTaskKey, LocalName: "assignedTo",
		Data: map[string]any{},
	}
}

func mutationByOpKey(res processor.ScriptResult, op, key string) (processor.MutationOp, bool) {
	for _, m := range res.Mutations {
		if m.Op == op && m.Key == key {
			return m, true
		}
	}
	return processor.MutationOp{}, false
}

// --- ReAssignTask ---

// TestReAssign_Success: an open task with a known old link re-points the
// assignedTo link atomically (old tombstoned, new created), asserts the task
// root OCC revision, and emits TaskReAssigned.
func TestReAssign_Success(t *testing.T) {
	oldLnk := "lnk.task." + lcTaskID + ".assignedTo.identity." + lcOldAssID
	newLnk := "lnk.task." + lcTaskID + ".assignedTo.identity." + lcNewAssID
	hydrated := map[string]processor.VertexDoc{
		lcTaskKey:   taskRoot("open", 7),
		oldLnk:      oldAssignedLink(),
		lcNewAssign: {Key: lcNewAssign, Class: "identity", Data: map[string]any{"state": "claimed"}},
	}
	res, err := runLifecycle(t, "ReAssignTask",
		map[string]any{"taskKey": lcTaskKey, "newAssignee": lcNewAssign}, hydrated)
	if err != nil {
		t.Fatalf("ReAssignTask: unexpected error: %v", err)
	}
	// Old link tombstoned.
	if _, ok := mutationByOpKey(res, "tombstone", oldLnk); !ok {
		t.Fatalf("missing tombstone of old assignedTo link %s; muts=%+v", oldLnk, res.Mutations)
	}
	// New link created (task = source).
	nm, ok := mutationByOpKey(res, "create", newLnk)
	if !ok {
		t.Fatalf("missing create of new assignedTo link %s", newLnk)
	}
	if got, _ := nm.Document["sourceVertex"].(string); got != lcTaskKey {
		t.Fatalf("new link sourceVertex = %q, want %q (task is source)", got, lcTaskKey)
	}
	if got, _ := nm.Document["targetVertex"].(string); got != lcNewAssign {
		t.Fatalf("new link targetVertex = %q, want %q", got, lcNewAssign)
	}
	// Root OCC update asserts the read revision; status stays open.
	rm, ok := mutationByOpKey(res, "update", lcTaskKey)
	if !ok {
		t.Fatalf("missing OCC update of task root")
	}
	if rm.ExpectedRevision == nil || *rm.ExpectedRevision != 7 {
		t.Fatalf("task root update expectedRevision = %v, want 7", rm.ExpectedRevision)
	}
	data, _ := rm.Document["data"].(map[string]any)
	if got, _ := data["status"].(string); got != "open" {
		t.Fatalf("reassign must keep status open, got %q", got)
	}
	// Event.
	if len(res.Events) != 1 || res.Events[0].Class != "orchestration.taskReAssigned" {
		t.Fatalf("events = %+v, want one TaskReAssigned", res.Events)
	}
	if got, _ := res.Events[0].Data["newAssignee"].(string); got != lcNewAssign {
		t.Fatalf("TaskReAssigned newAssignee = %q, want %q", got, lcNewAssign)
	}
	if got, _ := res.Events[0].Data["oldAssignee"].(string); got != lcOldAssign {
		t.Fatalf("TaskReAssigned oldAssignee = %q, want %q", got, lcOldAssign)
	}
}

// TestReAssign_AbsentNewAssignee_Rejected: the no-orphan invariant — a reassign
// to a non-existent identity is rejected with no mutation.
func TestReAssign_AbsentNewAssignee_Rejected(t *testing.T) {
	oldLnk := "lnk.task." + lcTaskID + ".assignedTo.identity." + lcOldAssID
	hydrated := map[string]processor.VertexDoc{
		lcTaskKey: taskRoot("open", 3),
		oldLnk:    oldAssignedLink(),
		// new assignee NOT hydrated (absent)
	}
	_, err := runLifecycle(t, "ReAssignTask",
		map[string]any{"taskKey": lcTaskKey, "newAssignee": lcNewAssign}, hydrated)
	if err == nil || !strings.Contains(err.Error(), "UnknownAssignee") {
		t.Fatalf("absent new assignee: want UnknownAssignee rejection, got %v", err)
	}
}

// TestReAssign_NotOpen_Rejected: a reassign of a complete/cancelled task is
// rejected (TaskNotOpen).
func TestReAssign_NotOpen_Rejected(t *testing.T) {
	for _, st := range []string{"complete", "cancelled"} {
		oldLnk := "lnk.task." + lcTaskID + ".assignedTo.identity." + lcOldAssID
		hydrated := map[string]processor.VertexDoc{
			lcTaskKey:   taskRoot(st, 4),
			oldLnk:      oldAssignedLink(),
			lcNewAssign: {Key: lcNewAssign, Class: "identity", Data: map[string]any{"state": "claimed"}},
		}
		_, err := runLifecycle(t, "ReAssignTask",
			map[string]any{"taskKey": lcTaskKey, "newAssignee": lcNewAssign}, hydrated)
		if err == nil || !strings.Contains(err.Error(), "TaskNotOpen") {
			t.Fatalf("reassign of %s task: want TaskNotOpen rejection, got %v", st, err)
		}
	}
}

// --- CompleteTask / CancelTask ---

// TestComplete_OpenToComplete: open→complete with OCC revision + TaskCompleted.
func TestComplete_OpenToComplete(t *testing.T) {
	hydrated := map[string]processor.VertexDoc{lcTaskKey: taskRoot("open", 11)}
	res, err := runLifecycle(t, "CompleteTask", map[string]any{"taskKey": lcTaskKey}, hydrated)
	if err != nil {
		t.Fatalf("CompleteTask: unexpected error: %v", err)
	}
	rm, ok := mutationByOpKey(res, "update", lcTaskKey)
	if !ok {
		t.Fatalf("missing root update")
	}
	if rm.ExpectedRevision == nil || *rm.ExpectedRevision != 11 {
		t.Fatalf("expectedRevision = %v, want 11", rm.ExpectedRevision)
	}
	data, _ := rm.Document["data"].(map[string]any)
	if got, _ := data["status"].(string); got != "complete" {
		t.Fatalf("status = %q, want complete", got)
	}
	if len(res.Events) != 1 || res.Events[0].Class != "orchestration.taskCompleted" {
		t.Fatalf("events = %+v, want one TaskCompleted", res.Events)
	}
	if got, _ := res.Events[0].Data["taskKey"].(string); got != lcTaskKey {
		t.Fatalf("TaskCompleted taskKey = %q, want %q", got, lcTaskKey)
	}
}

// TestCancel_OpenToCancelled: open→cancelled with TaskCancelled.
func TestCancel_OpenToCancelled(t *testing.T) {
	hydrated := map[string]processor.VertexDoc{lcTaskKey: taskRoot("open", 5)}
	res, err := runLifecycle(t, "CancelTask", map[string]any{"taskKey": lcTaskKey}, hydrated)
	if err != nil {
		t.Fatalf("CancelTask: unexpected error: %v", err)
	}
	rm, _ := mutationByOpKey(res, "update", lcTaskKey)
	data, _ := rm.Document["data"].(map[string]any)
	if got, _ := data["status"].(string); got != "cancelled" {
		t.Fatalf("status = %q, want cancelled", got)
	}
	if len(res.Events) != 1 || res.Events[0].Class != "orchestration.taskCancelled" {
		t.Fatalf("events = %+v, want one TaskCancelled", res.Events)
	}
}

// TestComplete_OfCancelled_Rejected is the AC's named invariant: cannot
// complete a cancelled task.
func TestComplete_OfCancelled_Rejected(t *testing.T) {
	hydrated := map[string]processor.VertexDoc{lcTaskKey: taskRoot("cancelled", 9)}
	_, err := runLifecycle(t, "CompleteTask", map[string]any{"taskKey": lcTaskKey}, hydrated)
	if err == nil || !strings.Contains(err.Error(), "InvalidTransition") {
		t.Fatalf("complete-of-cancelled: want InvalidTransition rejection, got %v", err)
	}
}

// TestCancel_OfComplete_Rejected is the symmetric guard.
func TestCancel_OfComplete_Rejected(t *testing.T) {
	hydrated := map[string]processor.VertexDoc{lcTaskKey: taskRoot("complete", 9)}
	_, err := runLifecycle(t, "CancelTask", map[string]any{"taskKey": lcTaskKey}, hydrated)
	if err == nil || !strings.Contains(err.Error(), "InvalidTransition") {
		t.Fatalf("cancel-of-complete: want InvalidTransition rejection, got %v", err)
	}
}

// TestComplete_SameState_Rejected: re-completing an already-complete task is
// rejected (Adjudication #4 — admin ops reject same-state re-transition).
func TestComplete_SameState_Rejected(t *testing.T) {
	hydrated := map[string]processor.VertexDoc{lcTaskKey: taskRoot("complete", 9)}
	_, err := runLifecycle(t, "CompleteTask", map[string]any{"taskKey": lcTaskKey}, hydrated)
	if err == nil || !strings.Contains(err.Error(), "TaskAlreadyInState") {
		t.Fatalf("complete-of-complete: want TaskAlreadyInState rejection, got %v", err)
	}
}

// TestCancel_SameState_Rejected: symmetric to the above.
func TestCancel_SameState_Rejected(t *testing.T) {
	hydrated := map[string]processor.VertexDoc{lcTaskKey: taskRoot("cancelled", 9)}
	_, err := runLifecycle(t, "CancelTask", map[string]any{"taskKey": lcTaskKey}, hydrated)
	if err == nil || !strings.Contains(err.Error(), "TaskAlreadyInState") {
		t.Fatalf("cancel-of-cancelled: want TaskAlreadyInState rejection, got %v", err)
	}
}
