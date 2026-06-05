package full

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/refractor/ruleengine"
)

// myTasksCypher mirrors orchestration-base's myTasks lens spec so the
// engine-level WHERE-filter behaviour is pinned here (the e2e relies on a
// closed task producing a null-task row that the wrapper deletes).
const myTasksCypher = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
  WHERE task.data.status = 'open'
OPTIONAL MATCH (task)-[:forOperation]->(op)
OPTIONAL MATCH (task)-[:scopedTo]->(tgt)
RETURN
  identity.key AS actorKey,
  collect(DISTINCT {
    taskKey: task.key,
    assignee: identity.key,
    forOperation: op.key,
    scopedTo: tgt.key,
    expiresAt: task.data.expiresAt
  }) AS openTasks
`

// realTaskKeys returns the non-null taskKeys in an openTasks collect — the
// subset the my-tasks envelope wrapper keeps (a null/absent taskKey is a
// degenerate artifact the wrapper drops).
func realTaskKeys(row map[string]any) []any {
	tasks, _ := row["openTasks"].([]any)
	out := []any{}
	for _, e := range tasks {
		m, _ := e.(map[string]any)
		if tk := m["taskKey"]; tk != nil {
			out = append(out, tk)
		}
	}
	return out
}

// TestMyTasksCypher_OpenTask_Projects: an OPEN task assigned to the identity
// projects a row with a non-null taskKey.
func TestMyTasksCypher_OpenTask_Projects(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	putVertex(t, reg, coreKV, "t1", "task", map[string]any{"data": map[string]any{"status": "open", "expiresAt": "2030-01-01T00:00:00Z"}})
	putVertex(t, reg, coreKV, "op1", "meta", map[string]any{"data": map[string]any{"operationType": "Approve"}})
	putVertex(t, reg, coreKV, "tgt1", "leaseapp", nil)
	putEdge(t, reg, adjKV, "assignedTo", "t1", "alice")
	putEdge(t, reg, adjKV, "forOperation", "t1", "op1")
	putEdge(t, reg, adjKV, "scopedTo", "t1", "tgt1")

	results := parseExec(t, myTasksCypher,
		ruleengine.EventContext{Parameters: map[string]any{"actorKey": vtxKey(reg, "alice")}},
		adjKV, coreKV)
	require.Len(t, results, 1)
	require.Equal(t, []any{vtxKey(reg, "t1")}, realTaskKeys(results[0].Values),
		"an open task must project with its taskKey")
}

// TestMyTasksCypher_CompleteTask_FiltersToNull: when the assigned task is
// COMPLETE, the WHERE filter excludes it; the OPTIONAL-match null fallback
// yields a single row whose collected taskKey is null — which the my-tasks
// envelope wrapper treats as zero open tasks → ErrDeleteProjection (vanish-on-
// close). This is the engine-level guarantee the e2e depends on.
func TestMyTasksCypher_CompleteTask_FiltersToNull(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	putVertex(t, reg, coreKV, "t1", "task", map[string]any{"data": map[string]any{"status": "complete", "expiresAt": "2030-01-01T00:00:00Z"}})
	putVertex(t, reg, coreKV, "op1", "meta", map[string]any{"data": map[string]any{"operationType": "Approve"}})
	putVertex(t, reg, coreKV, "tgt1", "leaseapp", nil)
	putEdge(t, reg, adjKV, "assignedTo", "t1", "alice")
	putEdge(t, reg, adjKV, "forOperation", "t1", "op1")
	putEdge(t, reg, adjKV, "scopedTo", "t1", "tgt1")

	results := parseExec(t, myTasksCypher,
		ruleengine.EventContext{Parameters: map[string]any{"actorKey": vtxKey(reg, "alice")}},
		adjKV, coreKV)
	require.Len(t, results, 1, "a live identity always yields exactly one row")
	require.Empty(t, realTaskKeys(results[0].Values),
		"a completed task must filter out (zero real taskKeys) so the wrapper deletes the key")
}

// TestMyTasksCypher_CompleteTask_NullsActorKey pins the engine behaviour the
// MyTasksWrapper compensates for: when the anchored identity's only task is
// filtered out, the collapsed OPTIONAL chain projects identity.key AS actorKey
// as NULL (not the anchor key). The wrapper therefore falls back to
// params["actorKey"] to key the deletion — without that fallback a just-closed
// task's row would skip (linger) instead of hard-deleting.
func TestMyTasksCypher_CompleteTask_NullsActorKey(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	putVertex(t, reg, coreKV, "t1", "task", map[string]any{"data": map[string]any{"status": "complete", "expiresAt": "2030-01-01T00:00:00Z"}})
	putVertex(t, reg, coreKV, "op1", "meta", map[string]any{"data": map[string]any{"operationType": "Approve"}})
	putVertex(t, reg, coreKV, "tgt1", "leaseapp", nil)
	putEdge(t, reg, adjKV, "assignedTo", "t1", "alice")
	putEdge(t, reg, adjKV, "forOperation", "t1", "op1")
	putEdge(t, reg, adjKV, "scopedTo", "t1", "tgt1")
	results := parseExec(t, myTasksCypher,
		ruleengine.EventContext{Parameters: map[string]any{"actorKey": vtxKey(reg, "alice")}},
		adjKV, coreKV)
	require.Len(t, results, 1, "a live identity always yields exactly one row")
	require.Nil(t, results[0].Values["actorKey"],
		"a filtered-out sole task collapses the chain so actorKey projects null")
	require.Empty(t, realTaskKeys(results[0].Values))
}
