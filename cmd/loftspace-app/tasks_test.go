package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestTasksFromRow_EmptyOpenTasks(t *testing.T) {
	var mt myTasksRow
	if err := json.Unmarshal([]byte(`{"key":"my-tasks.identity.carol","assignee":"vtx.identity.carol","openTasks":[{"taskKey":null}]}`), &mt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rows := tasksFromRow(mt); len(rows) != 0 {
		t.Errorf("carol has no open tasks: want 0 rows, got %d (%+v)", len(rows), rows)
	}
}

func TestHandleTasks_NoAuthConfigured_401(t *testing.T) {
	s := &server{logger: discardLogger(), natsTimeout: testTimeout} // authn nil
	rec := httptest.NewRecorder()
	s.handleTasks(rec, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandleTasks_NoToken_401(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	s.handleTasks(rec, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no bearer)", rec.Code)
	}
}

func TestHandleTasks_ForgedToken_401(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	r.Header.Set("Authorization", "Bearer not.a.valid.jwt")
	s.handleTasks(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (forged token)", rec.Code)
	}
}

// TestHandleTasks_ValidToken_NoConn_502: a verified actor with no NATS connection
// gets a clean 502, never a nil-pointer panic (mirrors handleApplications' pgPool
// nil-check for the KV-backed my-tasks read model).
func TestHandleTasks_ValidToken_NoConn_502(t *testing.T) {
	s := devAuthServer(t) // authn set, conn nil
	tok, _, err := s.devSigner.mint("Hj4kPmRtw9nbCxz5vQ2y")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	s.handleTasks(rec, r)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (no NATS conn)", rec.Code)
	}
}
