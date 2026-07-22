// Script-level unit tests for the task DDL CreateTask branch.
//
// These run the DDL's Starlark script directly through the Processor's
// StarlarkRunner — no NATS — so the validation/normalization branches
// (expiresAt RFC3339 normalize, empty type-segment guard) are exercised in
// isolation from the commit pipeline.
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
	tsReqID      = "BBscripttestHJKMNPQR"
	tsAssignee   = "vtx.identity.BBassigneeHJKMNPQRST"
	tsForOp      = "vtx.meta.BBapproveBpHJKMNPQRS"
	tsScopedTo   = "vtx.leaseapp.BBease4ppHJKMNPQRSTU"
	tsScopedID   = "BBease4ppHJKMNPQRSTU"
	tsAssigneeID = "BBassigneeHJKMNPQRST"
)

// taskScript returns the CreateTask DDL script body.
func taskScript(t *testing.T) string {
	t.Helper()
	for _, d := range orchestrationbase.DDLs() {
		if d.CanonicalName == "task" {
			return d.Script
		}
	}
	t.Fatal("task DDL not found")
	return ""
}

// aliveEndpoints hydrates the three CreateTask link endpoints as live vertices.
func aliveEndpoints() map[string]processor.VertexDoc {
	return map[string]processor.VertexDoc{
		tsAssignee: {Key: tsAssignee, Class: "identity", Data: map[string]any{"state": "claimed"}},
		tsForOp:    {Key: tsForOp, Class: "meta", Data: map[string]any{"operationType": "ApproveLeaseApplication"}},
		tsScopedTo: {Key: tsScopedTo, Class: "leaseapp", Data: map[string]any{"state": "pending"}},
	}
}

func runCreateTask(t *testing.T, scopedTo, expiresAt string, endpoints map[string]processor.VertexDoc) (processor.ScriptResult, error) {
	t.Helper()
	return runCreateTaskWith(t, map[string]any{
		"assignee":     tsAssignee,
		"forOperation": tsForOp,
		"scopedTo":     scopedTo,
		"expiresAt":    expiresAt,
	}, endpoints)
}

// taskKVReader is the on-demand kv.Read seam for the CreateTask idempotency
// branch (§10.3): the script reads the task key to decide create-vs-no-op. The
// `present` map names the keys that read as live tasks; every other key reads as
// absent (None). The default harness leaves it empty, so the task is absent and
// the script takes the create path — preserving every existing assertion.
type taskKVReader struct {
	present map[string]processor.VertexDoc
}

func (r taskKVReader) ReadVertex(_ context.Context, key string) (*processor.VertexDoc, error) {
	if d, ok := r.present[key]; ok {
		return &d, nil
	}
	return nil, nil
}

// runCreateTaskWith runs CreateTask with a caller-built payload so a test can
// add (or omit) the optional taskId field. The task key reads as absent.
func runCreateTaskWith(t *testing.T, payloadFields map[string]any, endpoints map[string]processor.VertexDoc) (processor.ScriptResult, error) {
	return runCreateTaskWithReader(t, payloadFields, endpoints, taskKVReader{})
}

// runCreateTaskWithReader is runCreateTaskWith with an explicit kv.Read seam so a
// test can make the task key read as an already-present (or deleted) task and
// exercise the §10.3 idempotency branch.
func runCreateTaskWithReader(t *testing.T, payloadFields map[string]any, endpoints map[string]processor.VertexDoc, reader processor.ScriptKVReader) (processor.ScriptResult, error) {
	t.Helper()
	payload, _ := json.Marshal(payloadFields)
	sc := processor.ScriptContext{
		Operation: &processor.OperationEnvelope{
			RequestID:     tsReqID,
			Lane:          processor.LaneDefault,
			OperationType: "CreateTask",
			Actor:         tsAssignee,
			SubmittedAt:   "2026-06-04T00:00:00Z",
			Payload:       payload,
		},
		Hydrated:     endpoints,
		ScriptSource: taskScript(t),
		ScriptClass:  "task",
		KVReader:     reader,
	}
	return processor.NewStarlarkRunner(0, 0).Run(context.Background(), sc)
}

// taskExpiresAt pulls the expiresAt scalar from the created task vertex mutation.
func taskExpiresAt(t *testing.T, res processor.ScriptResult) string {
	t.Helper()
	for _, m := range res.Mutations {
		if strings.HasPrefix(m.Key, "vtx.task.") {
			data, _ := m.Document["data"].(map[string]any)
			s, _ := data["expiresAt"].(string)
			return s
		}
	}
	t.Fatal("no task vertex mutation produced")
	return ""
}

// TestCreateTask_NormalizesOffsetExpiresAt: a +offset expiresAt is normalized
// to canonical UTC whole-second RFC3339 (the form the lens $now uses), so the
// lexical expiresAt > now comparison is sound.
func TestCreateTask_NormalizesOffsetExpiresAt(t *testing.T) {
	res, err := runCreateTask(t, tsScopedTo, "2026-06-04T23:00:00+09:00", aliveEndpoints())
	if err != nil {
		t.Fatalf("CreateTask with offset expiresAt: unexpected error: %v", err)
	}
	if got := taskExpiresAt(t, res); got != "2026-06-04T14:00:00Z" {
		t.Fatalf("normalized expiresAt = %q, want 2026-06-04T14:00:00Z", got)
	}
}

// TestCreateTask_NormalizesFractionalExpiresAt: fractional seconds are dropped.
func TestCreateTask_NormalizesFractionalExpiresAt(t *testing.T) {
	res, err := runCreateTask(t, tsScopedTo, "2026-06-04T14:00:00.987654Z", aliveEndpoints())
	if err != nil {
		t.Fatalf("CreateTask with fractional expiresAt: unexpected error: %v", err)
	}
	if got := taskExpiresAt(t, res); got != "2026-06-04T14:00:00Z" {
		t.Fatalf("normalized expiresAt = %q, want 2026-06-04T14:00:00Z", got)
	}
}

// TestCreateTask_MalformedExpiresAt_Rejected: a non-RFC3339 expiresAt is
// rejected as a structured ScriptError before any mutation is produced.
func TestCreateTask_MalformedExpiresAt_Rejected(t *testing.T) {
	_, err := runCreateTask(t, tsScopedTo, "next-tuesday", aliveEndpoints())
	if err == nil {
		t.Fatal("malformed expiresAt: expected rejection, got nil error")
	}
	if !strings.Contains(err.Error(), "InvalidArgument") {
		t.Fatalf("malformed expiresAt error = %q, want InvalidArgument", err.Error())
	}
}

// TestCreateTask_EmptyTypeSegment_Rejected: a scopedTo key whose type segment
// is empty (vtx..<id>) is rejected (FIX 3 parts_of guard).
func TestCreateTask_EmptyTypeSegment_Rejected(t *testing.T) {
	badScoped := "vtx.." + tsScopedID
	// Hydrate the malformed key as alive so the rejection is the shape guard,
	// not the vertex_alive check.
	eps := aliveEndpoints()
	delete(eps, tsScopedTo)
	eps[badScoped] = processor.VertexDoc{Key: badScoped, Class: "leaseapp"}

	_, err := runCreateTask(t, badScoped, "2026-06-04T14:00:00Z", eps)
	if err == nil {
		t.Fatal("empty type segment: expected rejection, got nil error")
	}
	if !strings.Contains(err.Error(), "empty type segment") {
		t.Fatalf("empty type segment error = %q, want 'empty type segment'", err.Error())
	}
}

// taskKey pulls the created task vertex key from the mutation set.
func taskKeyOf(t *testing.T, res processor.ScriptResult) string {
	t.Helper()
	for _, m := range res.Mutations {
		if strings.HasPrefix(m.Key, "vtx.task.") {
			return m.Key
		}
	}
	t.Fatal("no task vertex mutation produced")
	return ""
}

// TestCreateTask_SuppliedTaskId_UsedVerbatim: a caller-supplied bare-NanoID
// taskId mints vtx.task.<thatId> verbatim (the write-ahead seam, AC#3).
func TestCreateTask_SuppliedTaskId_UsedVerbatim(t *testing.T) {
	const suppliedID = "BBsuppliedHJKMNPQRST"
	res, err := runCreateTaskWith(t, map[string]any{
		"assignee":     tsAssignee,
		"forOperation": tsForOp,
		"scopedTo":     tsScopedTo,
		"expiresAt":    "2026-06-04T14:00:00Z",
		"taskId":       suppliedID,
	}, aliveEndpoints())
	if err != nil {
		t.Fatalf("CreateTask with supplied taskId: unexpected error: %v", err)
	}
	if got, want := taskKeyOf(t, res), "vtx.task."+suppliedID; got != want {
		t.Fatalf("task key = %q, want %q (supplied taskId used verbatim)", got, want)
	}
}

// TestCreateTask_PreexistingLiveTask_NoOp: when the task key already names a
// live task (a re-dispatch with the same stable taskId, §10.3), the script
// returns EMPTY mutations AND EMPTY events — a coherent no-op, so no duplicate
// task and no phantom taskCreated event.
func TestCreateTask_PreexistingLiveTask_NoOp(t *testing.T) {
	const suppliedID = "BBsuppliedHJKMNPQRST"
	taskKey := "vtx.task." + suppliedID
	reader := taskKVReader{present: map[string]processor.VertexDoc{
		taskKey: {Key: taskKey, Class: "task", IsDeleted: false, Data: map[string]any{"status": "open"}},
	}}
	res, err := runCreateTaskWithReader(t, map[string]any{
		"assignee":     tsAssignee,
		"forOperation": tsForOp,
		"scopedTo":     tsScopedTo,
		"expiresAt":    "2026-06-04T14:00:00Z",
		"taskId":       suppliedID,
	}, aliveEndpoints(), reader)
	if err != nil {
		t.Fatalf("CreateTask on a pre-existing task: unexpected error: %v", err)
	}
	if len(res.Mutations) != 0 {
		t.Fatalf("duplicate CreateTask must emit no mutations, got %d", len(res.Mutations))
	}
	if len(res.Events) != 0 {
		t.Fatalf("duplicate CreateTask must emit no events (no phantom taskCreated), got %d", len(res.Events))
	}
}

// TestCreateTask_DeletedTask_SelfHeals: a logically-deleted task at the key
// (isDeleted=true) does NOT suppress — the gap still needs a task, so the script
// falls through to a CAS-guarded revive (update, expectedRevision = the read
// revision), not a blind create (which would collide with the existing key's
// write history and RevisionConflict at commit — see create_task_test.go's
// TestCreateTask_DeletedTask_ReviveCommits for the full-pipeline proof).
func TestCreateTask_DeletedTask_SelfHeals(t *testing.T) {
	const suppliedID = "BBsuppliedHJKMNPQRST"
	taskKey := "vtx.task." + suppliedID
	reader := taskKVReader{present: map[string]processor.VertexDoc{
		taskKey: {Key: taskKey, Class: "task", IsDeleted: true, Data: map[string]any{}, Revision: 7},
	}}
	res, err := runCreateTaskWithReader(t, map[string]any{
		"assignee":     tsAssignee,
		"forOperation": tsForOp,
		"scopedTo":     tsScopedTo,
		"expiresAt":    "2026-06-04T14:00:00Z",
		"taskId":       suppliedID,
	}, aliveEndpoints(), reader)
	if err != nil {
		t.Fatalf("CreateTask on a deleted task: unexpected error: %v", err)
	}
	if got, want := taskKeyOf(t, res), taskKey; got != want {
		t.Fatalf("a deleted task must self-heal (revive); task key = %q, want %q", got, want)
	}
	var taskMut *processor.MutationOp
	for i := range res.Mutations {
		if res.Mutations[i].Key == taskKey {
			taskMut = &res.Mutations[i]
			break
		}
	}
	if taskMut.Op != "update" {
		t.Fatalf("revive mutation op = %q, want %q (a blind create would RevisionConflict on the still-present key)", taskMut.Op, "update")
	}
	if taskMut.ExpectedRevision == nil || *taskMut.ExpectedRevision != 7 {
		t.Fatalf("revive mutation expectedRevision = %v, want 7 (the tombstone's read revision)", taskMut.ExpectedRevision)
	}
}

// TestCreateTask_NoTaskId_Mints: an absent taskId mints a fresh task key (every
// existing admin/manual caller is unaffected — backward compatible).
func TestCreateTask_NoTaskId_Mints(t *testing.T) {
	res, err := runCreateTask(t, tsScopedTo, "2026-06-04T14:00:00Z", aliveEndpoints())
	if err != nil {
		t.Fatalf("CreateTask without taskId: unexpected error: %v", err)
	}
	if got := taskKeyOf(t, res); got == "vtx.task." || !strings.HasPrefix(got, "vtx.task.") {
		t.Fatalf("task key = %q, want a minted vtx.task.<id>", got)
	}
}

// TestCreateTask_DottedTaskId_Rejected: a taskId carrying a dot (not a bare
// NanoID) is rejected before any mutation — it would corrupt the vtx.task.<id>
// key shape (Contract #1).
func TestCreateTask_DottedTaskId_Rejected(t *testing.T) {
	_, err := runCreateTaskWith(t, map[string]any{
		"assignee":     tsAssignee,
		"forOperation": tsForOp,
		"scopedTo":     tsScopedTo,
		"expiresAt":    "2026-06-04T14:00:00Z",
		"taskId":       "vtx.task.injected",
	}, aliveEndpoints())
	if err == nil {
		t.Fatal("dotted taskId: expected rejection, got nil error")
	}
	if !strings.Contains(err.Error(), "bare NanoID") {
		t.Fatalf("dotted taskId error = %q, want 'bare NanoID'", err.Error())
	}
}

// TestCreateTask_EmptyTaskId_Rejected: a present-but-empty taskId is rejected
// (an explicit empty string is a caller error, distinct from omission).
func TestCreateTask_EmptyTaskId_Rejected(t *testing.T) {
	_, err := runCreateTaskWith(t, map[string]any{
		"assignee":     tsAssignee,
		"forOperation": tsForOp,
		"scopedTo":     tsScopedTo,
		"expiresAt":    "2026-06-04T14:00:00Z",
		"taskId":       "   ",
	}, aliveEndpoints())
	if err == nil {
		t.Fatal("empty taskId: expected rejection, got nil error")
	}
	if !strings.Contains(err.Error(), "taskId") {
		t.Fatalf("empty taskId error = %q, want a taskId rejection", err.Error())
	}
}
