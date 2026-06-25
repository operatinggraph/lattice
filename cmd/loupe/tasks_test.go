package main

import "testing"

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
		rows := computeTasks(keys, get, "")
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
		rows := computeTasks(keys, get, "open")
		if len(rows) != 1 || rows[0].Status != "open" {
			t.Fatalf("status=open should return the single open task, got %+v", rows)
		}
	})

	t.Run("complete task renders with its op resolved; no assignee link is fine", func(t *testing.T) {
		rows := computeTasks(keys, get, "complete")
		if len(rows) != 1 {
			t.Fatalf("want 1 complete task, got %d", len(rows))
		}
		if rows[0].Operation.Key != "vtx.meta.opmeta000000000000" {
			t.Errorf("complete task op key: %q", rows[0].Operation.Key)
		}
		if rows[0].Assignee != "" {
			t.Errorf("complete task has no assignedTo link; assignee should be empty, got %q", rows[0].Assignee)
		}
	})

	// A dispatched userTask's forOperation points at the operation's DDL
	// meta-vertex, whose name lives on the ROOT as data.operationType with NO
	// .canonicalName aspect (package op DDLs carry none). The op label must fall
	// back to the root operationType so the inbox renders a name, not a blank.
	t.Run("op name falls back to root operationType when no canonicalName aspect", func(t *testing.T) {
		real := map[string][]byte{
			"vtx.task.taskreal0000000000":                                      []byte(`{"class":"task","data":{"status":"open","expiresAt":"2026-07-01T00:00:00Z"}}`),
			"lnk.task.taskreal0000000000.forOperation.meta.opddl00000000000000": []byte(`{}`),
			// The op DDL meta: operationType on the root, no canonicalName aspect.
			"vtx.meta.opddl00000000000000": []byte(`{"class":"meta.ddl.vertexType","data":{"operationType":"SignLease"}}`),
		}
		rget := func(key string) ([]byte, bool) { b, ok := real[key]; return b, ok }
		rkeys := make([]string, 0, len(real))
		for k := range real {
			rkeys = append(rkeys, k)
		}
		rows := computeTasks(rkeys, rget, "")
		if len(rows) != 1 {
			t.Fatalf("want 1 task, got %d", len(rows))
		}
		if rows[0].Operation.Name != "SignLease" {
			t.Errorf("op name should fall back to root operationType, got %q", rows[0].Operation.Name)
		}
	})
}
