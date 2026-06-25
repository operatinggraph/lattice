package main

import "testing"

func TestComputeTasks_ScopesToApplicantAndFlattens(t *testing.T) {
	entries := map[string]string{
		// alice — two open tasks: a SignLease (scoped to her leaseapp) and a
		// RecordIdentityPII (scoped to her identity, the userTask invariant).
		"my-tasks.identity.alice": `{"actorKey":"vtx.identity.alice","openTasks":[` +
			`{"taskKey":"vtx.task.t1","assignee":"vtx.identity.alice","forOperation":"vtx.meta.sign","operationName":"SignLease","operationDescription":"Sign your lease","scopedTo":"vtx.leaseapp.app1","expiresAt":"2026-09-01T00:00:00Z"},` +
			`{"taskKey":"vtx.task.t2","assignee":"vtx.identity.alice","forOperation":"vtx.meta.pii","operationName":"RecordIdentityPII","operationDescription":"Provide your SSN and date of birth","scopedTo":"vtx.identity.alice","expiresAt":"2026-08-01T00:00:00Z"}]}`,
		// bob — one open task
		"my-tasks.identity.bob": `{"actorKey":"vtx.identity.bob","openTasks":[` +
			`{"taskKey":"vtx.task.t3","assignee":"vtx.identity.bob","forOperation":"vtx.meta.sign","operationName":"SignLease","scopedTo":"vtx.leaseapp.app3","expiresAt":"2026-10-01T00:00:00Z"}]}`,
		// carol — a live identity with no open tasks: the lens leaves a degenerate
		// {taskKey:null} collect artifact that must be dropped, not rendered.
		"my-tasks.identity.carol": `{"actorKey":"vtx.identity.carol","openTasks":[{"taskKey":null}]}`,
	}
	get := fakeKV(entries)

	alice := computeTasks(keysOf(entries), get, "vtx.identity.alice")
	if len(alice) != 2 {
		t.Fatalf("alice: want 2 tasks, got %d (%+v)", len(alice), alice)
	}
	// stable sort by soonest expiry → the PII task (Aug) before the sign task (Sep)
	if alice[0].TaskKey != "vtx.task.t2" || alice[1].TaskKey != "vtx.task.t1" {
		t.Errorf("expiry sort: got %q, %q", alice[0].TaskKey, alice[1].TaskKey)
	}
	if alice[0].OperationName != "RecordIdentityPII" || alice[0].ScopedTo != "vtx.identity.alice" {
		t.Errorf("PII task: want RecordIdentityPII scoped to the identity, got %+v", alice[0])
	}
	if alice[1].OperationName != "SignLease" || alice[1].ScopedTo != "vtx.leaseapp.app1" {
		t.Errorf("sign task: want SignLease scoped to the leaseapp, got %+v", alice[1])
	}
	if alice[1].OperationDescription != "Sign your lease" {
		t.Errorf("self-describing description should survive: got %q", alice[1].OperationDescription)
	}

	bob := computeTasks(keysOf(entries), get, "vtx.identity.bob")
	if len(bob) != 1 || bob[0].TaskKey != "vtx.task.t3" {
		t.Fatalf("bob: want only t3, got %+v", bob)
	}

	// carol — the degenerate no-task artifact yields zero rows, not a null task.
	if carol := computeTasks(keysOf(entries), get, "vtx.identity.carol"); len(carol) != 0 {
		t.Errorf("carol has no open tasks: want 0 rows, got %d (%+v)", len(carol), carol)
	}

	// no applicant filter → every identity's open tasks (3 real tasks)
	if all := computeTasks(keysOf(entries), get, ""); len(all) != 3 {
		t.Fatalf("unfiltered: want 3 tasks, got %d", len(all))
	}

	// an applicant with no projection row → empty, not a nil-panic
	if none := computeTasks(keysOf(entries), get, "vtx.identity.nobody"); len(none) != 0 {
		t.Errorf("unknown applicant: want 0 rows, got %d", len(none))
	}
}

func TestComputeTasks_SkipsUndecodable(t *testing.T) {
	entries := map[string]string{
		"my-tasks.identity.alice": `not json`,
		"my-tasks.identity.bob": `{"actorKey":"vtx.identity.bob","openTasks":[` +
			`{"taskKey":"vtx.task.t3","operationName":"SignLease","scopedTo":"vtx.leaseapp.app3"}]}`,
	}
	got := computeTasks(keysOf(entries), fakeKV(entries), "")
	if len(got) != 1 || got[0].TaskKey != "vtx.task.t3" {
		t.Fatalf("want only the decodable row's task, got %+v", got)
	}
}
