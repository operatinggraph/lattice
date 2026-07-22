// Script-level unit tests for the task DDL's FR28 additions: CreateTask's
// assignee-or-queue routing, and the new ClaimTask op.
//
// These run the DDL's Starlark script directly through the Processor's
// StarlarkRunner — no NATS — with fake ScriptKVReader/ScriptLinkLister seams
// so the kv.Read / kv.Links branches are exercised in isolation.
package orchestrationbase_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
)

const (
	ctQueueRole   = "vtx.role.BBqueueroleHJKMNPQRS"
	ctQueueRoleID = "BBqueueroleHJKMNPQRS"
	ctClaimant    = "vtx.identity.BBclaimantHJKMNPQRST"
	ctClaimantID  = "BBclaimantHJKMNPQRST"
	ctTaskID      = "BBqueuedtaskHJKMNPQR"
	ctTaskKey     = "vtx.task." + ctTaskID
	ctQueuedLnk   = "lnk.task." + ctTaskID + ".queuedFor.role." + ctQueueRoleID
	ctAssignedLnk = "lnk.task." + ctTaskID + ".assignedTo.identity." + ctClaimantID
	ctHoldsRole   = "lnk.identity." + ctClaimantID + ".holdsRole.role." + ctQueueRoleID
)

// --- CreateTask: assignee-or-queue routing (FR28) ---

// TestCreateTask_QueueOnly_CommitsQueuedFor: assignee absent, queue given+alive
// -> the task commits queuedFor (not assignedTo).
func TestCreateTask_QueueOnly_CommitsQueuedFor(t *testing.T) {
	eps := map[string]processor.VertexDoc{
		tsForOp:    {Key: tsForOp, Class: "meta", Data: map[string]any{"operationType": "ApproveLeaseApplication"}},
		tsScopedTo: {Key: tsScopedTo, Class: "leaseapp", Data: map[string]any{"state": "pending"}},
		ctQueueRole: {Key: ctQueueRole, Class: "role"},
	}
	res, err := runCreateTaskWith(t, map[string]any{
		"queue":        ctQueueRole,
		"forOperation": tsForOp,
		"scopedTo":     tsScopedTo,
		"expiresAt":    "2026-06-04T14:00:00Z",
	}, eps)
	if err != nil {
		t.Fatalf("CreateTask with queue only: unexpected error: %v", err)
	}
	taskKey := taskKeyOf(t, res)
	wantQueuedLnk := "lnk.task." + strings.TrimPrefix(taskKey, "vtx.task.") + ".queuedFor.role." + ctQueueRoleID
	if _, ok := mutationByOpKey(res, "create", wantQueuedLnk); !ok {
		t.Fatalf("missing create of queuedFor link %s; muts=%+v", wantQueuedLnk, res.Mutations)
	}
	for _, m := range res.Mutations {
		if strings.HasPrefix(m.Key, "lnk.") && strings.Contains(m.Key, ".assignedTo.") {
			t.Fatalf("queue-only CreateTask must NOT commit an assignedTo link, got %s", m.Key)
		}
	}
	if len(res.Events) != 1 {
		t.Fatalf("events = %+v, want exactly one", res.Events)
	}
	if got := res.Events[0].Data["queue"]; got != ctQueueRole {
		t.Fatalf("taskCreated event queue = %v, want %q", got, ctQueueRole)
	}
	if got := res.Events[0].Data["assignee"]; got != nil {
		t.Fatalf("queue-only taskCreated event assignee = %v, want nil", got)
	}
}

// TestCreateTask_AssigneeWinsOverQueue: both given+alive -> assignee wins
// (byte-compatible with pre-FR28 behavior); no queuedFor link is committed.
func TestCreateTask_AssigneeWinsOverQueue(t *testing.T) {
	eps := aliveEndpoints()
	eps[ctQueueRole] = processor.VertexDoc{Key: ctQueueRole, Class: "role"}
	res, err := runCreateTaskWith(t, map[string]any{
		"assignee":     tsAssignee,
		"queue":        ctQueueRole,
		"forOperation": tsForOp,
		"scopedTo":     tsScopedTo,
		"expiresAt":    "2026-06-04T14:00:00Z",
	}, eps)
	if err != nil {
		t.Fatalf("CreateTask with both assignee and queue: unexpected error: %v", err)
	}
	for _, m := range res.Mutations {
		if strings.HasPrefix(m.Key, "lnk.") && strings.Contains(m.Key, ".queuedFor.") {
			t.Fatalf("assignee-given CreateTask must NOT commit a queuedFor link, got %s", m.Key)
		}
	}
	if got := res.Events[0].Data["assignee"]; got != tsAssignee {
		t.Fatalf("taskCreated event assignee = %v, want %q", got, tsAssignee)
	}
}

// TestCreateTask_NeitherAssigneeNorQueue_RoutingFailed: no silent drop -- the
// op rejects when neither endpoint resolves.
func TestCreateTask_NeitherAssigneeNorQueue_RoutingFailed(t *testing.T) {
	eps := map[string]processor.VertexDoc{
		tsForOp:    {Key: tsForOp, Class: "meta"},
		tsScopedTo: {Key: tsScopedTo, Class: "leaseapp"},
	}
	_, err := runCreateTaskWith(t, map[string]any{
		"forOperation": tsForOp,
		"scopedTo":     tsScopedTo,
		"expiresAt":    "2026-06-04T14:00:00Z",
	}, eps)
	if err == nil || !strings.Contains(err.Error(), "RoutingFailed") {
		t.Fatalf("neither assignee nor queue: want RoutingFailed rejection, got %v", err)
	}
}

// TestCreateTask_UnknownQueue_Rejected: a dead/absent role is rejected
// (no-orphan invariant extends to the queue endpoint).
func TestCreateTask_UnknownQueue_Rejected(t *testing.T) {
	eps := map[string]processor.VertexDoc{
		tsForOp:    {Key: tsForOp, Class: "meta"},
		tsScopedTo: {Key: tsScopedTo, Class: "leaseapp"},
		// ctQueueRole deliberately NOT hydrated (absent).
	}
	_, err := runCreateTaskWith(t, map[string]any{
		"queue":        ctQueueRole,
		"forOperation": tsForOp,
		"scopedTo":     tsScopedTo,
		"expiresAt":    "2026-06-04T14:00:00Z",
	}, eps)
	if err == nil || !strings.Contains(err.Error(), "UnknownQueue") {
		t.Fatalf("absent queue role: want UnknownQueue rejection, got %v", err)
	}
}

// --- ClaimTask ---

// fakeClaimLinkLister is an in-memory processor.ScriptLinkLister returning a
// canned page regardless of the filter -- each test configures exactly the
// queuedFor link (if any) ClaimTask's kv.Links(taskKey, "queuedFor", "out")
// call should observe.
type fakeClaimLinkLister struct {
	links []processor.LinkDoc
}

func (f fakeClaimLinkLister) ListLinks(_ context.Context, _, _ string, _ int) ([]processor.LinkDoc, string, error) {
	return f.links, "", nil
}

// runClaimTask runs ClaimTask as claimant (op.actor), with hydrated (declared)
// state for the task root and the given kv.Read/kv.Links seams.
func runClaimTask(t *testing.T, claimant string, hydrated map[string]processor.VertexDoc, reader processor.ScriptKVReader, lister processor.ScriptLinkLister) (processor.ScriptResult, error) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"taskKey": ctTaskKey})
	sc := processor.ScriptContext{
		Operation: &processor.OperationEnvelope{
			RequestID:     "BBclaimreqHJKMNPQRST",
			Lane:          processor.LaneDefault,
			OperationType: "ClaimTask",
			Actor:         claimant,
			SubmittedAt:   "2026-06-04T00:00:00Z",
			Payload:       payload,
			// read-posture — no production dispatcher yet (hard case 3,
			// script-read-posture-design §13); the test envelope carries the
			// declarations the future UI/dispatcher inherits. OptionalReads
			// is class (d) (every runClaimTask caller claims as ctClaimant);
			// Enumerations declares the op's one class-(e) kv.Links call
			// (the queuedFor lookup) as metadata.
			ContextHint: &processor.ContextHint{
				OptionalReads: []string{ctAssignedLnk},
				Enumerations: []processor.EnumerationHint{
					{Hub: ctTaskKey, Relation: "queuedFor", Direction: "out"},
				},
			},
		},
		Hydrated:     hydrated,
		ScriptSource: taskScript(t),
		ScriptClass:  "task",
		KVReader:     reader,
		LinkLister:   lister,
	}
	return processor.NewStarlarkRunner(0, 0).Run(context.Background(), sc)
}

func openTaskState() map[string]processor.VertexDoc {
	return map[string]processor.VertexDoc{
		ctTaskKey: {Key: ctTaskKey, Class: "task", Revision: 3,
			Data: map[string]any{"status": "open", "expiresAt": "2030-01-01T00:00:00Z"}},
	}
}

// TestClaimTask_RoleHolder_Success: a role-holder claims a queued task --
// queuedFor tombstones, assignedTo(claimant) is created, OCC-guarded root
// touch, and a TaskClaimed event.
func TestClaimTask_RoleHolder_Success(t *testing.T) {
	lister := fakeClaimLinkLister{links: []processor.LinkDoc{
		{Key: ctQueuedLnk, Class: "queuedFor", SourceVertex: ctTaskKey, TargetVertex: ctQueueRole},
	}}
	reader := taskKVReader{present: map[string]processor.VertexDoc{
		ctHoldsRole: {Key: ctHoldsRole, Class: "holdsRole"},
	}}
	res, err := runClaimTask(t, ctClaimant, openTaskState(), reader, lister)
	if err != nil {
		t.Fatalf("ClaimTask by a role-holder: unexpected error: %v", err)
	}
	if _, ok := mutationByOpKey(res, "tombstone", ctQueuedLnk); !ok {
		t.Fatalf("missing tombstone of queuedFor link %s; muts=%+v", ctQueuedLnk, res.Mutations)
	}
	nm, ok := mutationByOpKey(res, "create", ctAssignedLnk)
	if !ok {
		t.Fatalf("missing create of assignedTo link %s", ctAssignedLnk)
	}
	if got, _ := nm.Document["targetVertex"].(string); got != ctClaimant {
		t.Fatalf("assignedTo targetVertex = %q, want %q", got, ctClaimant)
	}
	rm, ok := mutationByOpKey(res, "update", ctTaskKey)
	if !ok || rm.ExpectedRevision == nil || *rm.ExpectedRevision != 3 {
		t.Fatalf("task root OCC update missing/wrong revision: %+v", rm)
	}
	if len(res.Events) != 1 || res.Events[0].Class != "orchestration.taskClaimed" {
		t.Fatalf("events = %+v, want one taskClaimed", res.Events)
	}
	if got := res.Events[0].Data["role"]; got != ctQueueRole {
		t.Fatalf("taskClaimed role = %v, want %q", got, ctQueueRole)
	}
}

// TestClaimTask_NonRoleHolder_Rejected: the claimant does not hold the
// queued role -> NotAuthorizedToClaim, no mutations.
func TestClaimTask_NonRoleHolder_Rejected(t *testing.T) {
	lister := fakeClaimLinkLister{links: []processor.LinkDoc{
		{Key: ctQueuedLnk, Class: "queuedFor", SourceVertex: ctTaskKey, TargetVertex: ctQueueRole},
	}}
	reader := taskKVReader{} // holdsRole link absent
	_, err := runClaimTask(t, ctClaimant, openTaskState(), reader, lister)
	if err == nil || !strings.Contains(err.Error(), "NotAuthorizedToClaim") {
		t.Fatalf("non-role-holder claim: want NotAuthorizedToClaim rejection, got %v", err)
	}
}

// TestClaimTask_AlreadyClaimedByOther_Rejected: the queuedFor link is gone
// (someone else claimed it) and the claimant has no assignedTo link of their
// own -> TaskAlreadyClaimed.
func TestClaimTask_AlreadyClaimedByOther_Rejected(t *testing.T) {
	lister := fakeClaimLinkLister{links: nil} // queuedFor already tombstoned/gone
	reader := taskKVReader{}                  // claimant's own assignedTo link absent
	_, err := runClaimTask(t, ctClaimant, openTaskState(), reader, lister)
	if err == nil || !strings.Contains(err.Error(), "TaskAlreadyClaimed") {
		t.Fatalf("already-claimed-by-other: want TaskAlreadyClaimed rejection, got %v", err)
	}
}

// TestClaimTask_IdempotentReclaimBySameActor_NoOp: a re-claim by the actor
// who already holds the assignedTo link is a coherent no-op (empty
// mutations AND events), mirroring CreateTask's redispatch idempotency.
func TestClaimTask_IdempotentReclaimBySameActor_NoOp(t *testing.T) {
	lister := fakeClaimLinkLister{links: nil} // queuedFor already tombstoned by the first claim
	reader := taskKVReader{present: map[string]processor.VertexDoc{
		ctAssignedLnk: {Key: ctAssignedLnk, Class: "assignedTo", VertexKey: ctTaskKey, LocalName: "assignedTo"},
	}}
	res, err := runClaimTask(t, ctClaimant, openTaskState(), reader, lister)
	if err != nil {
		t.Fatalf("idempotent re-claim: unexpected error: %v", err)
	}
	if len(res.Mutations) != 0 {
		t.Fatalf("idempotent re-claim must emit no mutations, got %d", len(res.Mutations))
	}
	if len(res.Events) != 0 {
		t.Fatalf("idempotent re-claim must emit no events, got %d", len(res.Events))
	}
}

// TestClaimTask_NotOpen_Rejected: a complete/cancelled task cannot be
// claimed.
func TestClaimTask_NotOpen_Rejected(t *testing.T) {
	for _, st := range []string{"complete", "cancelled"} {
		hydrated := map[string]processor.VertexDoc{
			ctTaskKey: {Key: ctTaskKey, Class: "task", Revision: 1,
				Data: map[string]any{"status": st, "expiresAt": "2030-01-01T00:00:00Z"}},
		}
		lister := fakeClaimLinkLister{}
		reader := taskKVReader{}
		_, err := runClaimTask(t, ctClaimant, hydrated, reader, lister)
		if err == nil || !strings.Contains(err.Error(), "TaskNotOpen") {
			t.Fatalf("claim of %s task: want TaskNotOpen rejection, got %v", st, err)
		}
	}
}

// TestClaimTask_UnknownTask_Rejected: a taskKey the caller failed to declare
// (or that never existed) fails closed.
func TestClaimTask_UnknownTask_Rejected(t *testing.T) {
	_, err := runClaimTask(t, ctClaimant, map[string]processor.VertexDoc{}, taskKVReader{}, fakeClaimLinkLister{})
	if err == nil || !strings.Contains(err.Error(), "UnknownTask") {
		t.Fatalf("undeclared task: want UnknownTask rejection, got %v", err)
	}
}
