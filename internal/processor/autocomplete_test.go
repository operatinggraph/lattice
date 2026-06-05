package processor

import (
	"testing"
)

// TestTaskKeyFromTaskPathDecision: only a task-path decision with a non-nil
// ephemeral grant yields the grant's TaskKey; every other shape yields "".
func TestTaskKeyFromTaskPathDecision(t *testing.T) {
	cases := []struct {
		name string
		rp   *ResolvedPermission
		want string
	}{
		{"nil", nil, ""},
		{"platform-path", &ResolvedPermission{Path: "platform"}, ""},
		{"service-path", &ResolvedPermission{Path: "service"}, ""},
		{"task-path-no-grant", &ResolvedPermission{Path: "task"}, ""},
		{
			"task-path-with-grant",
			&ResolvedPermission{Path: "task", EphemeralGrant: &EphemeralGrant{TaskKey: "vtx.task.ABCDEFGHJKMNPQRSTUVW"}},
			"vtx.task.ABCDEFGHJKMNPQRSTUVW",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := taskKeyFromTaskPathDecision(tc.rp); got != tc.want {
				t.Fatalf("taskKeyFromTaskPathDecision = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestInjectTaskAutoCompletion: the injection appends exactly one mutation +
// one event to a COPY of the result and leaves the original untouched (no
// second assembly path; the existing batch builder carries it).
func TestInjectTaskAutoCompletion(t *testing.T) {
	rev := uint64(42)
	orig := ScriptResult{
		Mutations:  []MutationOp{{Op: "update", Key: "vtx.leaseapp.X"}},
		Events:     []EventSpec{{Class: "LeaseApproved"}},
		PrimaryKey: "vtx.leaseapp.X",
	}
	ac := taskAutoCompletion{
		taskKey:  "vtx.task.T",
		open:     true,
		revision: rev,
		mutation: MutationOp{Op: "update", Key: "vtx.task.T", ExpectedRevision: &rev},
		event:    EventSpec{Class: "TaskCompleted", Data: map[string]interface{}{"taskKey": "vtx.task.T"}},
	}
	got := injectTaskAutoCompletion(orig, ac)

	if len(orig.Mutations) != 1 || len(orig.Events) != 1 {
		t.Fatalf("original result mutated: muts=%d events=%d", len(orig.Mutations), len(orig.Events))
	}
	if len(got.Mutations) != 2 || got.Mutations[1].Key != "vtx.task.T" {
		t.Fatalf("expected the task update appended; muts=%+v", got.Mutations)
	}
	if got.Mutations[1].ExpectedRevision == nil || *got.Mutations[1].ExpectedRevision != rev {
		t.Fatalf("injected mutation must carry the CAS revision %d", rev)
	}
	if len(got.Events) != 2 || got.Events[1].Class != "TaskCompleted" {
		t.Fatalf("expected TaskCompleted appended; events=%+v", got.Events)
	}
	if got.PrimaryKey != orig.PrimaryKey {
		t.Fatalf("primaryKey changed: %q != %q", got.PrimaryKey, orig.PrimaryKey)
	}
}
