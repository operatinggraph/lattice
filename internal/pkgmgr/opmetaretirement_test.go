package pkgmgr

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// minimalTaskDDLScript is a deliberately minimal task-lifecycle stand-in for
// these tests — NOT packages/orchestration-base's real task DDL (that
// package imports pkgmgr, so importing it back here would close a cycle; see
// installer_test.go's identical note on testutil). It implements just enough
// of CreateTask/CancelTask to drive the retirement guard's real
// submitDirectOp/submitCancelTask path end to end: a forOperation link plus
// an open/cancelled status flip.
const minimalTaskDDLScript = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": data}}

def vertex_alive(state, key):
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def execute(state, op):
    ot = op.operationType
    p = op.payload
    if ot == "CreateTask":
        task_id = required_string(p, "taskId")
        task_key = "vtx.task." + task_id
        for_op = required_string(p, "forOperation")
        expires_at = required_string(p, "expiresAt")
        if not vertex_alive(state, for_op):
            fail("UnknownOperation: " + for_op)
        op_id = for_op.split(".")[2]
        forop_lnk = "lnk.task." + task_id + ".forOperation.meta." + op_id
        mutations = [
            make_vtx(task_key, "task", {"status": "open", "expiresAt": expires_at}),
            make_link(forop_lnk, task_key, for_op, "forOperation", "forOperation", {}),
        ]
        return {"mutations": mutations, "events": [], "response": {"primaryKey": task_key}}
    if ot == "CancelTask":
        task_key = required_string(p, "taskKey")
        if not vertex_alive(state, task_key):
            fail("UnknownTask: " + task_key)
        doc = state[task_key]
        status = doc.data["status"]
        if status != "open":
            fail("InvalidTransition: cannot cancel task in status " + status)
        mutations = [
            {"op": "update", "key": task_key, "expectedRevision": doc.revision,
             "document": {"class": "task", "isDeleted": False,
                          "data": {"status": "cancelled", "expiresAt": doc.data["expiresAt"]}}},
        ]
        return {"mutations": mutations, "events": [], "response": {"primaryKey": task_key}}
    fail("UnknownOperation: " + ot)
`

// taskLifecycleTestDef installs the minimal task-lifecycle stand-in DDL.
func taskLifecycleTestDef(version string) Definition {
	return Definition{
		Name:    "task-lifecycle-test-pkg",
		Version: version,
		DDLs: []DDLSpec{
			{
				CanonicalName:     "task",
				Class:             "meta.ddl.vertexType",
				PermittedCommands: []string{"CreateTask", "CancelTask"},
				Description:       "Minimal task lifecycle DDL for opmeta-retirement guard tests.",
				Script:            minimalTaskDDLScript,
				InputSchema:       `{"type":"object"}`,
				OutputSchema:      `{"type":"object"}`,
				FieldDescription: map[string]string{
					"taskId":       "bare NanoID",
					"forOperation": "vtx.meta.<id>",
					"expiresAt":    "RFC3339",
					"taskKey":      "vtx.task.<id>",
				},
				Examples: []ExampleSpec{
					{
						Name: "CreateTask example",
						Payload: map[string]any{
							"taskId": "aaaaaaaaaaaaaaaaaaaa", "forOperation": "vtx.meta.bbbbbbbbbbbbbbbbbbbb",
							"expiresAt": "2030-01-01T00:00:00Z",
						},
						ExpectedOutcome: "Creates a task.",
					},
				},
			},
		},
	}
}

func taskStatusForTest(t *testing.T, ctx context.Context, conn *substrate.Conn, taskKey string) string {
	t.Helper()
	doc := kvDoc(t, ctx, conn, taskKey)
	data, _ := doc["data"].(map[string]any)
	status, _ := data["status"].(string)
	return status
}

// TestOpMetaRetirement_E2E: install → CreateTask → upgrade dropping the op
// (undeclared) → refused, task untouched → re-run declaring
// RetireCancelsOpenTasks → task cancelled, op-meta tombstoned, upgrade lands
// — opmeta-retirement-open-task-guard-design.md §5's e2e vector.
func TestOpMetaRetirement_E2E(t *testing.T) {
	ctx, conn, inst := newDualLaneInstallerHarness(t)

	if _, err := inst.Install(ctx, taskLifecycleTestDef("0.1.0")); err != nil {
		t.Fatalf("install task-lifecycle stand-in: %v", err)
	}

	opPkgV1 := Definition{
		Name:    "opretire-test-pkg",
		Version: "0.1.0",
		OpMetas: []OpMetaSpec{{OperationType: "OpRetireSampleOp"}},
	}
	if _, err := inst.Install(ctx, opPkgV1); err != nil {
		t.Fatalf("install op package v1: %v", err)
	}
	opMetaKey := "vtx.meta." + entityNanoID(opPkgV1.Name, "opMeta:OpRetireSampleOp")

	taskID := "TskRetireE2ETestCase"
	taskKey := "vtx.task." + taskID
	reply, err := inst.submitDirectOp(ctx, processor.LaneDefault, "CreateTask", "task",
		deterministicNanoID(taskID, "", "seed-create-task"),
		map[string]any{"taskId": taskID, "forOperation": opMetaKey, "expiresAt": "2030-01-01T00:00:00Z"},
		&processor.ContextHint{Reads: []string{opMetaKey}})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if reply.Status != processor.ReplyStatusAccepted {
		t.Fatalf("CreateTask rejected: %s", replyError(reply))
	}
	if got := taskStatusForTest(t, ctx, conn, taskKey); got != "open" {
		t.Fatalf("seeded task status = %q, want open", got)
	}

	// v2 drops the op-meta with no declared disposition — refused, even
	// though nothing here inspected the referent count first (the
	// dev-green trap pinned by design §5).
	undeclared := Definition{Name: opPkgV1.Name, Version: "0.2.0"}
	if _, err := inst.Upgrade(ctx, undeclared); err == nil {
		t.Fatalf("Upgrade dropping an undeclared op-meta should be refused")
	}
	if got := taskStatusForTest(t, ctx, conn, taskKey); got != "open" {
		t.Fatalf("task status after the refused upgrade = %q, want open (untouched)", got)
	}
	if del, _ := kvDoc(t, ctx, conn, opMetaKey)["isDeleted"].(bool); del {
		t.Fatalf("op-meta should still be live after the refused upgrade")
	}

	// Re-run, this time declaring RetireCancelsOpenTasks: the guard cancels
	// the open referent, then the upgrade lands.
	declared := Definition{
		Name:                   opPkgV1.Name,
		Version:                "0.2.0",
		RetireCancelsOpenTasks: []string{"OpRetireSampleOp"},
	}
	res, err := inst.Upgrade(ctx, declared)
	if err != nil {
		t.Fatalf("Upgrade with RetireCancelsOpenTasks declared: %v", err)
	}
	if res.Tombstoned == 0 {
		t.Fatalf("expected the op-meta to be tombstoned, got %+v", res)
	}
	if got := taskStatusForTest(t, ctx, conn, taskKey); got != "cancelled" {
		t.Fatalf("task status after the upgrade = %q, want cancelled", got)
	}
	if del, _ := kvDoc(t, ctx, conn, opMetaKey)["isDeleted"].(bool); !del {
		t.Fatalf("op-meta %s should be tombstoned", opMetaKey)
	}
}

// TestUpgrade_OpMetaDropDeclaredCancelZeroReferents_Succeeds proves the
// declared-cancel path is a clean no-op enumeration when nothing references
// the dropped op — no CancelTask is ever submitted, and the upgrade lands.
func TestUpgrade_OpMetaDropDeclaredCancelZeroReferents_Succeeds(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	v1 := Definition{
		Name:    "opretire-zero-pkg",
		Version: "0.1.0",
		OpMetas: []OpMetaSpec{{OperationType: "OpRetireZeroOp"}},
	}
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("install v1: %v", err)
	}
	v2 := Definition{
		Name:                   v1.Name,
		Version:                "0.2.0",
		RetireCancelsOpenTasks: []string{"OpRetireZeroOp"},
	}
	res, err := inst.Upgrade(ctx, v2)
	if err != nil {
		t.Fatalf("Upgrade with zero referents should succeed: %v", err)
	}
	if res.Tombstoned == 0 {
		t.Fatalf("expected the op-meta to be tombstoned, got %+v", res)
	}
}

// TestUpgrade_OpMetaDropMovedOpsDeclared_Refused proves a MovedOps
// declaration for a dropped op always refuses today (§3 — reserved
// vocabulary, not implemented).
func TestUpgrade_OpMetaDropMovedOpsDeclared_Refused(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	v1 := Definition{
		Name:    "opretire-moved-pkg",
		Version: "0.1.0",
		OpMetas: []OpMetaSpec{{OperationType: "OpRetireMovedOp"}},
	}
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("install v1: %v", err)
	}
	v2 := Definition{
		Name:     v1.Name,
		Version:  "0.2.0",
		MovedOps: map[string]string{"OpRetireMovedOp": "successor-pkg"},
	}
	if _, err := inst.Upgrade(ctx, v2); err == nil {
		t.Fatalf("Upgrade declaring MovedOps for a dropped op should be refused (not yet supported)")
	}
}

// TestOpMetaOperationType exercises the op-meta recognizer across every
// committed-doc shape it must distinguish.
func TestOpMetaOperationType(t *testing.T) {
	cases := []struct {
		name      string
		committed map[string]any
		wantOT    string
		wantOK    bool
	}{
		{"nil doc", nil, "", false},
		{"op-meta", map[string]any{"class": opMetaClass, "data": map[string]any{"operationType": "Foo"}}, "Foo", true},
		{"wrong class", map[string]any{"class": "meta.lens", "data": map[string]any{"operationType": "Foo"}}, "", false},
		{"op-meta, no data", map[string]any{"class": opMetaClass}, "", false},
		{"op-meta, empty operationType", map[string]any{"class": opMetaClass, "data": map[string]any{"operationType": ""}}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ot, ok := opMetaOperationType(c.committed)
			if ot != c.wantOT || ok != c.wantOK {
				t.Fatalf("opMetaOperationType(%+v) = (%q, %v), want (%q, %v)", c.committed, ot, ok, c.wantOT, c.wantOK)
			}
		})
	}
}

// seedLinkAndTaskForTest writes a forOperation link + its task root directly
// to Core KV (bypassing the op pipeline — these tests exercise
// taskIsOpenReferent's read logic, not task-lifecycle mutation), returning
// the link key.
func seedLinkAndTaskForTest(t *testing.T, ctx context.Context, conn *substrate.Conn, taskID, opMetaID string, taskDeleted bool, status, expiresAt string, linkDeleted bool) (taskKey, linkKey string) {
	t.Helper()
	taskKey = "vtx.task." + taskID
	linkKey = "lnk.task." + taskID + ".forOperation.meta." + opMetaID

	taskDoc := map[string]any{
		"class":     "task",
		"isDeleted": taskDeleted,
		"data":      map[string]any{"status": status, "expiresAt": expiresAt},
	}
	taskBytes, err := json.Marshal(taskDoc)
	if err != nil {
		t.Fatalf("marshal task doc: %v", err)
	}
	if _, err := conn.KVPut(ctx, CoreBucket, taskKey, taskBytes); err != nil {
		t.Fatalf("KVPut %s: %v", taskKey, err)
	}

	linkDoc := map[string]any{
		"class":        "forOperation",
		"isDeleted":    linkDeleted,
		"sourceVertex": taskKey,
		"targetVertex": "vtx.meta." + opMetaID,
		"localName":    "forOperation",
		"data":         map[string]any{},
	}
	linkBytes, err := json.Marshal(linkDoc)
	if err != nil {
		t.Fatalf("marshal link doc: %v", err)
	}
	if _, err := conn.KVPut(ctx, CoreBucket, linkKey, linkBytes); err != nil {
		t.Fatalf("KVPut %s: %v", linkKey, err)
	}
	return taskKey, linkKey
}

// TestTaskIsOpenReferent covers every branch the design's test strategy
// names: open+unexpired counts; complete/cancelled/expired don't; an
// unparseable expiresAt counts (cancel rather than silently strand it); a
// soft-deleted link or task doesn't count.
func TestTaskIsOpenReferent(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	future := "2030-01-01T00:00:00Z"
	past := "2020-01-01T00:00:00Z"

	cases := []struct {
		name        string
		status      string
		expiresAt   string
		taskDeleted bool
		linkDeleted bool
		want        bool
	}{
		{"open, unexpired", "open", future, false, false, true},
		{"open, no expiresAt", "open", "", false, false, true},
		{"complete", "complete", future, false, false, false},
		{"cancelled", "cancelled", future, false, false, false},
		{"open, expired", "open", past, false, false, false},
		{"open, unparseable expiresAt counts", "open", "not-a-date", false, false, true},
		{"open, task soft-deleted", "open", future, true, false, false},
		{"open, link soft-deleted", "open", future, false, true, false},
	}
	for idx, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			taskID := taskIDForCase(idx)
			taskKey, linkKey := seedLinkAndTaskForTest(t, ctx, conn, taskID, "opMetaFixedTestKey99", c.taskDeleted, c.status, c.expiresAt, c.linkDeleted)
			got, err := inst.taskIsOpenReferent(ctx, taskKey, linkKey)
			if err != nil {
				t.Fatalf("taskIsOpenReferent: %v", err)
			}
			if got != c.want {
				t.Fatalf("taskIsOpenReferent(status=%q, expiresAt=%q, taskDeleted=%v, linkDeleted=%v) = %v, want %v",
					c.status, c.expiresAt, c.taskDeleted, c.linkDeleted, got, c.want)
			}
		})
	}
}

// taskIDForCase mints a distinct, valid 20-char NanoID-shaped id per test
// case (the canonical alphabet excludes I/l/O/0, substrate/keys/nanoid.go)
// so each case's task/link keys are independent.
func taskIDForCase(idx int) string {
	return "TskReferentCaseSpot" + string(rune('1'+idx))
}
