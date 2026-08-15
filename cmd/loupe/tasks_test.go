package main

import (
	"testing"
	"time"
)

// testNow is a fixed clock for the deterministic stuck check. It sits after the
// open task's 2026-07-01 expiry, so a role-queued open task past that expiry is
// stuck while an assigned open task is not.
var testNow = time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)

func TestComputeTasks(t *testing.T) {
	store := map[string][]byte{
		// An OPEN task: assignee + forOperation + scopedTo links, op meta with a
		// human label.
		"vtx.task.taskopen0000000000":                                      []byte(`{"class":"task","data":{"status":"open","expiresAt":"2026-07-01T00:00:00Z"}}`),
		"lnk.task.taskopen0000000000.assignedTo.identity.idabc00000000000": []byte(`{}`),
		"lnk.task.taskopen0000000000.forOperation.meta.opmeta000000000000": []byte(`{}`),
		"lnk.task.taskopen0000000000.scopedTo.leaseapp.app000000000000000": []byte(`{}`),
		"vtx.meta.opmeta000000000000":                                      []byte(`{"class":"meta.ddl.aspectType","data":{}}`),
		"vtx.meta.opmeta000000000000.canonicalName":                        []byte(`{"data":{"value":"RecordIdentityPII"}}`),
		"vtx.meta.opmeta000000000000.description":                          []byte(`{"data":{"value":"Record the applicant's identity PII."}}`),

		// A COMPLETE task — must sort after the open one, and be excluded by the
		// status=open filter.
		"vtx.task.taskdone0000000000":                                      []byte(`{"class":"task","data":{"status":"complete","expiresAt":"2026-06-01T00:00:00Z"}}`),
		"lnk.task.taskdone0000000000.forOperation.meta.opmeta000000000000": []byte(`{}`),

		// A non-task vertex — must be ignored.
		"vtx.identity.idabc00000000000": []byte(`{"class":"identity","data":{}}`),
	}
	get := func(key string) ([]byte, bool) { b, ok := store[key]; return b, ok }
	keys := make([]string, 0, len(store))
	for k := range store {
		keys = append(keys, k)
	}

	t.Run("all tasks, open sorts first, links + op label resolved", func(t *testing.T) {
		rows := computeTasks(keys, get, "", testNow)
		if len(rows) != 2 {
			t.Fatalf("want 2 tasks, got %d: %+v", len(rows), rows)
		}
		open := rows[0]
		if open.Key != "vtx.task.taskopen0000000000" {
			t.Errorf("open task should sort first, got %q", open.Key)
		}
		if open.Status != "open" || open.ExpiresAt != "2026-07-01T00:00:00Z" {
			t.Errorf("unexpected status/expiry: %+v", open)
		}
		if open.Assignee != "vtx.identity.idabc00000000000" {
			t.Errorf("assignee not link-sourced: %q", open.Assignee)
		}
		if open.Assignment != "assigned" {
			t.Errorf("assignment kind should be assigned, got %q", open.Assignment)
		}
		if open.Available == nil || *open.Available != true {
			t.Errorf("assigned task with no availability aspect should read available, got %v", open.Available)
		}
		if open.Stuck {
			t.Errorf("an assigned (not role-queued) task is never stuck: %+v", open)
		}
		if open.ScopedTo != "vtx.leaseapp.app000000000000000" {
			t.Errorf("scopedTo not link-sourced: %q", open.ScopedTo)
		}
		if open.Operation.Key != "vtx.meta.opmeta000000000000" {
			t.Errorf("operation not link-sourced: %q", open.Operation.Key)
		}
		if open.Operation.Name != "RecordIdentityPII" {
			t.Errorf("operation name not resolved from canonicalName: %q", open.Operation.Name)
		}
		if open.Operation.Description != "Record the applicant's identity PII." {
			t.Errorf("operation description not resolved: %q", open.Operation.Description)
		}
	})

	t.Run("status filter limits to one status", func(t *testing.T) {
		rows := computeTasks(keys, get, "open", testNow)
		if len(rows) != 1 || rows[0].Status != "open" {
			t.Fatalf("status=open should return the single open task, got %+v", rows)
		}
	})

	t.Run("complete task renders with its op resolved; no assignee link is fine", func(t *testing.T) {
		rows := computeTasks(keys, get, "complete", testNow)
		if len(rows) != 1 {
			t.Fatalf("want 1 complete task, got %d", len(rows))
		}
		if rows[0].Operation.Key != "vtx.meta.opmeta000000000000" {
			t.Errorf("complete task op key: %q", rows[0].Operation.Key)
		}
		if rows[0].Assignee != "" {
			t.Errorf("complete task has no assignedTo link; assignee should be empty, got %q", rows[0].Assignee)
		}
		if rows[0].Assignment != "" {
			t.Errorf("a task with neither assignedTo nor queuedFor has no assignment kind, got %q", rows[0].Assignment)
		}
		if rows[0].Available != nil {
			t.Errorf("no assignee ⇒ availability is nil, got %v", rows[0].Available)
		}
	})

	// A dispatched userTask's forOperation points at the operation's DDL
	// meta-vertex, whose name lives on the ROOT as data.operationType with NO
	// .canonicalName aspect (package op DDLs carry none). The op label must fall
	// back to the root operationType so the inbox renders a name, not a blank.
	t.Run("op name falls back to root operationType when no canonicalName aspect", func(t *testing.T) {
		real := map[string][]byte{
			"vtx.task.taskreal0000000000":                                       []byte(`{"class":"task","data":{"status":"open","expiresAt":"2026-07-01T00:00:00Z"}}`),
			"lnk.task.taskreal0000000000.forOperation.meta.opddl00000000000000": []byte(`{}`),
			// The op DDL meta: operationType on the root, no canonicalName aspect.
			"vtx.meta.opddl00000000000000": []byte(`{"class":"meta.ddl.vertexType","data":{"operationType":"SignLease"}}`),
		}
		rget := func(key string) ([]byte, bool) { b, ok := real[key]; return b, ok }
		rkeys := make([]string, 0, len(real))
		for k := range real {
			rkeys = append(rkeys, k)
		}
		rows := computeTasks(rkeys, rget, "", testNow)
		if len(rows) != 1 {
			t.Fatalf("want 1 task, got %d", len(rows))
		}
		if rows[0].Operation.Name != "SignLease" {
			t.Errorf("op name should fall back to root operationType, got %q", rows[0].Operation.Name)
		}
	})
}

// TestComputeTasks_QueuePlane covers the FR28/FR29 queue plane: role-queued
// (pull) assignment, assignee availability, and the stuck/unrouted flag + its
// top-of-inbox sort.
func TestComputeTasks_QueuePlane(t *testing.T) {
	store := map[string][]byte{
		// An open, role-queued task past its own expiry with no claim → STUCK.
		"vtx.task.taskstuck000000000":                                    []byte(`{"class":"task","data":{"status":"open","expiresAt":"2026-07-01T00:00:00Z"}}`),
		"lnk.task.taskstuck000000000.queuedFor.role.roleclerk0000000000": []byte(`{}`),

		// An open, role-queued task NOT yet expired → queued, not stuck.
		"vtx.task.taskqueued00000000":                                    []byte(`{"class":"task","data":{"status":"open","expiresAt":"2026-07-05T00:00:00Z"}}`),
		"lnk.task.taskqueued00000000.queuedFor.role.roleclerk0000000000": []byte(`{}`),

		// An open, assigned task whose assignee has declared unavailable.
		"vtx.task.taskassigned000000":                                      []byte(`{"class":"task","data":{"status":"open","expiresAt":"2026-07-09T00:00:00Z"}}`),
		"lnk.task.taskassigned000000.assignedTo.identity.idbusy0000000000": []byte(`{}`),
		"vtx.identity.idbusy0000000000.availability":                       []byte(`{"data":{"available":false}}`),
	}
	get := func(key string) ([]byte, bool) { b, ok := store[key]; return b, ok }
	keys := make([]string, 0, len(store))
	for k := range store {
		keys = append(keys, k)
	}

	rows := computeTasks(keys, get, "", testNow)
	if len(rows) != 3 {
		t.Fatalf("want 3 tasks, got %d: %+v", len(rows), rows)
	}

	// Stuck sorts to the very top, ahead of the other open tasks.
	if rows[0].Key != "vtx.task.taskstuck000000000" || !rows[0].Stuck {
		t.Fatalf("stuck/unrouted task must sort first and be flagged, got %+v", rows[0])
	}
	if rows[0].Assignment != "queued" || rows[0].QueuedFor != "vtx.role.roleclerk0000000000" {
		t.Errorf("stuck task should be role-queued, got assignment=%q queuedFor=%q", rows[0].Assignment, rows[0].QueuedFor)
	}
	if rows[0].Available != nil {
		t.Errorf("a role-queued task has no single assignee ⇒ availability nil, got %v", rows[0].Available)
	}

	byKey := map[string]taskRow{}
	for _, r := range rows {
		byKey[r.Key] = r
	}

	// The not-yet-expired queued task is queued but not stuck.
	if q := byKey["vtx.task.taskqueued00000000"]; q.Stuck || q.Assignment != "queued" {
		t.Errorf("un-expired queued task: want queued & not stuck, got %+v", q)
	}

	// The assigned task exposes the unavailable routing gate and is never stuck
	// (stuck is a role-queue concept).
	a := byKey["vtx.task.taskassigned000000"]
	if a.Assignment != "assigned" {
		t.Errorf("assigned task assignment kind: %q", a.Assignment)
	}
	if a.Available == nil || *a.Available != false {
		t.Errorf("assignee declared unavailable ⇒ available=false, got %v", a.Available)
	}
	if a.Stuck {
		t.Errorf("an assigned task is never stuck, got %+v", a)
	}
}
