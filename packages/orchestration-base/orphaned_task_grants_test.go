package orchestrationbase

// Rule-engine proof of the orphanedTaskGrants convergence lens, driven
// through the `full` engine against an embedded NATS Core/Adjacency KV —
// reuses unrFixture (lens_cypher_test.go).
//
//   - OPEN, LIVE OP: not violating — the grant still resolves to something.
//   - OPEN, OP TOMBSTONED: violating; missing_operation true — the row Weaver
//     dispatches CancelTask{taskKey} against.
//   - OPEN, NO forOperation LINK AT ALL: same as tombstoned from this lens's
//     point of view — CreateTask requires forOperation, so a missing link is
//     never a legitimate state, only ever a downstream retraction.
//   - ALREADY CANCELLED: the status='open' gate excludes it — zero rows.
//   - ALREADY COMPLETE: same status='open' gate — zero rows, even with a
//     tombstoned op — a finished task has nothing left to converge.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// tombstoneVtx marks a previously-seeded vertex isDeleted, mirroring
// wellness-domain/lens_cypher_test.go's own helper — CancelTask never runs in
// this fixture, so a "the op died" scenario is staged directly.
func (f *unrFixture) tombstoneVtx(t *testing.T, name string) {
	t.Helper()
	id := f.ids[name]
	key := "vtx." + f.types[id] + "." + id
	body := map[string]any{"key": key, "class": f.types[id], "isDeleted": true, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

// projectOrphanedAt runs the anchored orphanedTaskGrants spec for one task
// with an injected $now, mirroring unrFixture.projectAt.
func (f *unrFixture) projectOrphanedAt(t *testing.T, taskName, now string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(orphanedTaskGrantsSpec)
	require.NoError(t, err, "orphanedTaskGrants cypher must parse on the full engine")
	taskKey := "vtx.task." + f.ids[taskName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    taskKey,
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

func TestOrphanedTaskGrants_OpenWithLiveOp_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": "2026-07-01T12:00:00Z"})
	f.vtx(t, "op1", "meta", nil)
	f.edge(t, "forOperation", "task1", "op1")

	rows := f.projectOrphanedAt(t, "task1", unrNow)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "vtx.task."+f.ids["task1"], v["taskKey"])
	require.Equal(t, false, v["missing_operation"], "forOperation still resolves — nothing to cancel")
	require.Equal(t, false, v["violating"])
}

func TestOrphanedTaskGrants_OpenWithTombstonedOp_Violating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": "2026-07-01T12:00:00Z"})
	f.vtx(t, "op1", "meta", nil)
	f.edge(t, "forOperation", "task1", "op1")
	f.tombstoneVtx(t, "op1")

	v := f.projectOrphanedAt(t, "task1", unrNow)[0].Values
	require.Equal(t, true, v["missing_operation"], "the bound op-meta was tombstoned out from under an open task")
	require.Equal(t, true, v["violating"])
}

func TestOrphanedTaskGrants_OpenWithNoForOperationLink_Violating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": "2026-07-01T12:00:00Z"})

	v := f.projectOrphanedAt(t, "task1", unrNow)[0].Values
	require.Equal(t, true, v["missing_operation"], "forOperation is required at CreateTask — an absent link is never legitimate")
	require.Equal(t, true, v["violating"])
}

func TestOrphanedTaskGrants_CancelledNeverMatches(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "task1", "task", map[string]any{"status": "cancelled", "expiresAt": "2026-07-01T12:00:00Z"})
	f.vtx(t, "op1", "meta", nil)
	f.edge(t, "forOperation", "task1", "op1")
	f.tombstoneVtx(t, "op1")

	rows := f.projectOrphanedAt(t, "task1", unrNow)
	require.Empty(t, rows, "a cancelled task is excluded by the status='open' gate even with a dead op")
}

func TestOrphanedTaskGrants_CompleteNeverMatches(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "task1", "task", map[string]any{"status": "complete", "expiresAt": "2026-07-01T12:00:00Z"})
	f.vtx(t, "op1", "meta", nil)
	f.edge(t, "forOperation", "task1", "op1")
	f.tombstoneVtx(t, "op1")

	rows := f.projectOrphanedAt(t, "task1", unrNow)
	require.Empty(t, rows, "a completed task is excluded by the status='open' gate — nothing left to converge")
}
