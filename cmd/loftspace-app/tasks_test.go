package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestTasksFromRow_FlattensAndSkipsDegenerate(t *testing.T) {
	// Fixture mirrors the REAL actor-aggregate envelope shape the my-tasks lens
	// writes: the anchor identity is at the row root under `assignee` (the lens
	// ActorField) alongside `key` + envelope metadata — there is NO `actorKey`
	// field (that is the raw cypher RETURN alias the envelope renames). Matching the
	// real shape is the regression guard for the field-name bug that silently
	// dropped every row.
	var mt myTasksRow
	raw := `{"key":"my-tasks.identity.alice","assignee":"vtx.identity.alice","projectionSeq":42,"openTasks":[` +
		`{"taskKey":"vtx.task.t1","assignee":"vtx.identity.alice","forOperation":"vtx.meta.sign","operationName":"SignLease","operationDescription":"Sign your lease","scopedTo":"vtx.leaseapp.app1","expiresAt":"2026-09-01T00:00:00Z"},` +
		`{"taskKey":"vtx.task.t2","assignee":"vtx.identity.alice","forOperation":"vtx.meta.pii","operationName":"RecordIdentityPII","operationDescription":"Provide your SSN and date of birth","scopedTo":"vtx.identity.alice","expiresAt":"2026-08-01T00:00:00Z"},` +
		`{"taskKey":null}]}`
	if err := json.Unmarshal([]byte(raw), &mt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	rows := tasksFromRow(mt)
	if len(rows) != 2 {
		t.Fatalf("want 2 tasks (degenerate null-key row dropped), got %d (%+v)", len(rows), rows)
	}
	// stable sort by soonest expiry → the PII task (Aug) before the sign task (Sep)
	if rows[0].TaskKey != "vtx.task.t2" || rows[1].TaskKey != "vtx.task.t1" {
		t.Errorf("expiry sort: got %q, %q", rows[0].TaskKey, rows[1].TaskKey)
	}
	if rows[0].OperationName != "RecordIdentityPII" || rows[0].ScopedTo != "vtx.identity.alice" {
		t.Errorf("PII task: want RecordIdentityPII scoped to the identity, got %+v", rows[0])
	}
	if rows[1].OperationName != "SignLease" || rows[1].ScopedTo != "vtx.leaseapp.app1" {
		t.Errorf("sign task: want SignLease scoped to the leaseapp, got %+v", rows[1])
	}
	if rows[1].OperationDescription != "Sign your lease" {
		t.Errorf("self-describing description should survive: got %q", rows[1].OperationDescription)
	}
}

// TestTasksFromRow_QueuedRoleThreadsThrough: a role-queued, not-yet-assigned
// row (lenses.go:242-267's qtask branch — assignee null, queuedRole set) must
// keep queuedRole through tasksFromRow, so the FE can offer Claim instead of
// silently rendering it as an ordinary (and un-completable) assigned task.
func TestTasksFromRow_QueuedRoleThreadsThrough(t *testing.T) {
	var mt myTasksRow
	raw := `{"key":"my-tasks.identity.theo","assignee":"vtx.identity.theo","openTasks":[` +
		`{"taskKey":"vtx.task.wo1","assignee":null,"forOperation":"vtx.meta.resolve","operationName":"ResolveWorkOrder","operationDescription":"Resolve a work order","scopedTo":"vtx.workorder.wo1","expiresAt":"2026-09-01T00:00:00Z","queuedRole":"vtx.role.backOfHouse"}]}`
	if err := json.Unmarshal([]byte(raw), &mt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	rows := tasksFromRow(mt)
	if len(rows) != 1 {
		t.Fatalf("want 1 task, got %d (%+v)", len(rows), rows)
	}
	if rows[0].QueuedRole != "vtx.role.backOfHouse" {
		t.Errorf("queuedRole should survive tasksFromRow: got %+v", rows[0])
	}
	if rows[0].Assignee != "" {
		t.Errorf("a role-queued row has no assignee yet: got %q", rows[0].Assignee)
	}
}

func TestTasksFromRow_EmptyOpenTasks(t *testing.T) {
	var mt myTasksRow
	if err := json.Unmarshal([]byte(`{"key":"my-tasks.identity.carol","assignee":"vtx.identity.carol","openTasks":[{"taskKey":null}]}`), &mt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rows := tasksFromRow(mt); len(rows) != 0 {
		t.Errorf("carol has no open tasks: want 0 rows, got %d (%+v)", len(rows), rows)
	}
}

func TestHandleTasks_NoAuthPosture_401(t *testing.T) {
	s := noPostureServer(t)
	rec := sessionGET(s, s.handleTasks, "/api/tasks", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandleTasks_NoCookie_401(t *testing.T) {
	s, _ := devSessionServer(t, nil)
	rec := sessionGET(s, s.handleTasks, "/api/tasks", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no session cookie)", rec.Code)
	}
}

func TestHandleTasks_ForgedCookie_401(t *testing.T) {
	s, _ := devSessionServer(t, nil)
	forged := &http.Cookie{Name: s.session.CookieName(), Value: "not.a.valid.jwt"}
	rec := sessionGET(s, s.handleTasks, "/api/tasks", forged)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (forged cookie)", rec.Code)
	}
}

// TestHandleTasks_ValidSession_NoConn_502: a signed-in actor with no NATS
// connection gets a clean 502, never a nil-pointer panic (mirrors
// handleApplications' pgPool nil-check for the KV-backed my-tasks read model).
func TestHandleTasks_ValidSession_NoConn_502(t *testing.T) {
	s, cookieFor := devSessionServer(t, nil) // session set, conn nil
	rec := sessionGET(s, s.handleTasks, "/api/tasks", cookieFor("Hj4kPmRtw9nbCxz5vQ2y"))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (no NATS conn)", rec.Code)
	}
}
