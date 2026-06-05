package capabilityenv

import (
	"errors"
	"testing"

	"github.com/asolgan/lattice/internal/refractor/pipeline"
)

// TestMyTasksWrapper_NoOpenTasks_Deletes: a live identity whose openTasks
// collect carries only the degenerate (null-taskKey) artifact has zero open
// tasks → the wrapper signals ErrDeleteProjection keyed at my-tasks.identity.<id>
// so the key is hard-deleted (vanish-on-close). This is the FIX-1 genuine-
// absence mechanism.
func TestMyTasksWrapper_NoOpenTasks_Deletes(t *testing.T) {
	actorKey := "vtx.identity." + testIdentityActorID
	fn := NewMyTasksWrapper("vtx.meta.test-lens", func(string) uint64 { return 0 })

	row := map[string]any{
		"actorKey": actorKey,
		"openTasks": []any{
			map[string]any{"taskKey": nil, "assignee": actorKey, "forOperation": nil, "scopedTo": nil, "expiresAt": nil},
		},
	}
	env, keys, err := fn(row, map[string]any{}, makeParams())
	if !errors.Is(err, pipeline.ErrDeleteProjection) {
		t.Fatalf("expected ErrDeleteProjection for task-less identity, got %v", err)
	}
	if env != nil {
		t.Fatalf("delete signal must carry no envelope, got %v", env)
	}
	if got := keys["key"]; got != "my-tasks.identity."+testIdentityActorID {
		t.Fatalf("delete key = %v, want my-tasks.identity.%s", got, testIdentityActorID)
	}
}

// TestMyTasksWrapper_NullRowActor_FallsBackToParams: when the sole open task is
// filtered out, the lens cypher collapses the OPTIONAL chain and projects
// identity.key AS actorKey as NULL. The wrapper must fall back to the per-actor
// params["actorKey"] anchor to key the deletion — otherwise a just-closed task's
// row would skip (lingering key) instead of hard-deleting (vanish-on-close).
func TestMyTasksWrapper_NullRowActor_FallsBackToParams(t *testing.T) {
	actorKey := "vtx.identity." + testIdentityActorID
	fn := NewMyTasksWrapper("vtx.meta.test-lens", func(string) uint64 { return 0 })

	// actorKey is NULL in the row (the collapsed-chain artifact); openTasks empty.
	row := map[string]any{"actorKey": nil, "openTasks": []any{}}
	params := makeParams()
	params["actorKey"] = actorKey

	env, keys, err := fn(row, map[string]any{}, params)
	if !errors.Is(err, pipeline.ErrDeleteProjection) {
		t.Fatalf("expected ErrDeleteProjection via params fallback, got %v", err)
	}
	if env != nil {
		t.Fatalf("delete signal must carry no envelope, got %v", env)
	}
	if got := keys["key"]; got != "my-tasks.identity."+testIdentityActorID {
		t.Fatalf("delete key = %v, want my-tasks.identity.%s", got, testIdentityActorID)
	}
}

// TestMyTasksWrapper_EmptyCollect_Deletes: an empty openTasks array also yields
// a delete (no open task → the identity drops out of my-tasks).
func TestMyTasksWrapper_EmptyCollect_Deletes(t *testing.T) {
	actorKey := "vtx.identity." + testIdentityActorID
	fn := NewMyTasksWrapper("vtx.meta.test-lens", func(string) uint64 { return 0 })

	row := map[string]any{"actorKey": actorKey, "openTasks": []any{}}
	_, _, err := fn(row, map[string]any{}, makeParams())
	if !errors.Is(err, pipeline.ErrDeleteProjection) {
		t.Fatalf("expected ErrDeleteProjection for empty collect, got %v", err)
	}
}

// TestMyTasksWrapper_OpenTask_Projects: at least one real open task → the
// wrapper emits the envelope keyed my-tasks.identity.<id> carrying only the
// real open tasks (degenerate artifacts filtered out).
func TestMyTasksWrapper_OpenTask_Projects(t *testing.T) {
	actorKey := "vtx.identity." + testIdentityActorID
	fn := NewMyTasksWrapper("vtx.meta.test-lens", func(string) uint64 { return 0 })

	taskKey := "vtx.task." + testRoleActorID
	row := map[string]any{
		"actorKey": actorKey,
		"openTasks": []any{
			map[string]any{"taskKey": nil}, // degenerate artifact, dropped
			map[string]any{
				"taskKey":      taskKey,
				"assignee":     actorKey,
				"forOperation": "vtx.meta." + testRoleActorID,
				"scopedTo":     "vtx.leaseapp." + testIdentityActorID,
				"expiresAt":    "2030-01-01T00:00:00Z",
			},
		},
	}
	env, keys, err := fn(row, map[string]any{}, makeParams())
	if err != nil {
		t.Fatalf("wrapper returned error: %v", err)
	}
	if got := keys["key"]; got != "my-tasks.identity."+testIdentityActorID {
		t.Fatalf("key = %v, want my-tasks.identity.%s", got, testIdentityActorID)
	}
	if got := env["assignee"]; got != actorKey {
		t.Fatalf("assignee = %v, want %v", got, actorKey)
	}
	tasks, _ := env["openTasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("openTasks = %v, want exactly one real task", tasks)
	}
	got, _ := tasks[0].(map[string]any)
	if got["taskKey"] != taskKey {
		t.Fatalf("projected taskKey = %v, want %v", got["taskKey"], taskKey)
	}
}

// TestMyTasksWrapper_SkipsNonIdentityActor: a non-identity actorKey is dropped
// (ErrSkipProjection).
func TestMyTasksWrapper_SkipsNonIdentityActor(t *testing.T) {
	fn := NewMyTasksWrapper("vtx.meta.test-lens", func(string) uint64 { return 0 })
	row := map[string]any{"actorKey": "vtx.role." + testRoleActorID, "openTasks": []any{}}
	_, _, err := fn(row, map[string]any{}, makeParams())
	if err != pipeline.ErrSkipProjection {
		t.Fatalf("expected ErrSkipProjection for non-identity actor, got %v", err)
	}
}
