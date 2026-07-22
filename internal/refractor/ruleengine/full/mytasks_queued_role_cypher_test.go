package full

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
)

// myTasksQueuedRoleCypher mirrors orchestration-base's myTasks lens spec
// (FR28 addition) so the engine-level role-queue fan-out behavior is pinned
// here, the same way myTasksCypher pins the direct-assignment path.
const myTasksQueuedRoleCypher = `
MATCH (identity:identity {key: $actorKey})

OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
  WHERE task.data.status = 'open'
OPTIONAL MATCH (task)-[:forOperation]->(op)
OPTIONAL MATCH (task)-[:scopedTo]->(tgt)

OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)<-[:queuedFor]-(qtask:task)
  WHERE qtask.data.status = 'open'
OPTIONAL MATCH (qtask)-[:forOperation]->(qop)
OPTIONAL MATCH (qtask)-[:scopedTo]->(qtgt)

RETURN
  identity.key AS actorKey,
  collect(DISTINCT {
    taskKey: task.key,
    assignee: identity.key,
    forOperation: op.key,
    scopedTo: tgt.key,
    expiresAt: task.data.expiresAt,
    queuedRole: null
  }) + collect(DISTINCT {
    taskKey: qtask.key,
    assignee: null,
    forOperation: qop.key,
    scopedTo: qtgt.key,
    expiresAt: qtask.data.expiresAt,
    queuedRole: role.key
  }) AS openTasks
`

// queuedRoleFor pulls the (taskKey, queuedRole) of the entries carrying a
// non-null queuedRole -- the role-queue fan-out rows, as opposed to any
// direct-assignment rows.
func queuedRoleFor(row map[string]any) []map[string]any {
	tasks, _ := row["openTasks"].([]any)
	out := []map[string]any{}
	for _, e := range tasks {
		m, _ := e.(map[string]any)
		if m["taskKey"] != nil && m["queuedRole"] != nil {
			out = append(out, m)
		}
	}
	return out
}

// TestMyTasksQueuedRoleCypher_FansOutToEveryHolder: a task queued to a role
// (queuedFor) projects into EVERY identity holding that role's inbox --
// FR28's "anyone on the team can pick it up" semantics.
func TestMyTasksQueuedRoleCypher_FansOutToEveryHolder(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	putVertex(t, reg, coreKV, "bob", "identity", nil)
	putVertex(t, reg, coreKV, "leasingTeam", "role", nil)
	putVertex(t, reg, coreKV, "t1", "task", map[string]any{"data": map[string]any{"status": "open", "expiresAt": "2030-01-01T00:00:00Z"}})
	putVertex(t, reg, coreKV, "op1", "meta", map[string]any{"data": map[string]any{"operationType": "Approve"}})
	putVertex(t, reg, coreKV, "tgt1", "leaseapp", nil)
	putEdge(t, reg, adjKV, "holdsRole", "alice", "leasingTeam")
	putEdge(t, reg, adjKV, "holdsRole", "bob", "leasingTeam")
	putEdge(t, reg, adjKV, "queuedFor", "t1", "leasingTeam")
	putEdge(t, reg, adjKV, "forOperation", "t1", "op1")
	putEdge(t, reg, adjKV, "scopedTo", "t1", "tgt1")

	for _, holder := range []string{"alice", "bob"} {
		results := parseExec(t, myTasksQueuedRoleCypher,
			ruleengine.EventContext{Parameters: map[string]any{"actorKey": vtxKey(reg, holder)}},
			adjKV, coreKV)
		require.Len(t, results, 1, "%s: a live identity always yields exactly one row", holder)
		queued := queuedRoleFor(results[0].Values)
		require.Len(t, queued, 1, "%s: must see exactly one queued-role entry", holder)
		require.Equal(t, vtxKey(reg, "t1"), queued[0]["taskKey"], "%s: queued taskKey", holder)
		require.Equal(t, vtxKey(reg, "leasingTeam"), queued[0]["queuedRole"], "%s: queuedRole", holder)
	}
}

// TestMyTasksQueuedRoleCypher_NonHolderSeesNothing: an identity that does
// NOT hold the queued role sees no entry for that task -- the fan-out is
// bounded to actual role-holders.
func TestMyTasksQueuedRoleCypher_NonHolderSeesNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	putVertex(t, reg, coreKV, "carol", "identity", nil)
	putVertex(t, reg, coreKV, "leasingTeam", "role", nil)
	putVertex(t, reg, coreKV, "t1", "task", map[string]any{"data": map[string]any{"status": "open", "expiresAt": "2030-01-01T00:00:00Z"}})
	putVertex(t, reg, coreKV, "op1", "meta", map[string]any{"data": map[string]any{"operationType": "Approve"}})
	putVertex(t, reg, coreKV, "tgt1", "leaseapp", nil)
	putEdge(t, reg, adjKV, "holdsRole", "alice", "leasingTeam")
	putEdge(t, reg, adjKV, "queuedFor", "t1", "leasingTeam")
	putEdge(t, reg, adjKV, "forOperation", "t1", "op1")
	putEdge(t, reg, adjKV, "scopedTo", "t1", "tgt1")

	results := parseExec(t, myTasksQueuedRoleCypher,
		ruleengine.EventContext{Parameters: map[string]any{"actorKey": vtxKey(reg, "carol")}},
		adjKV, coreKV)
	require.Len(t, results, 1, "a live identity always yields exactly one row")
	require.Empty(t, queuedRoleFor(results[0].Values), "carol does not hold leasingTeam, must see no queued entry")
}
