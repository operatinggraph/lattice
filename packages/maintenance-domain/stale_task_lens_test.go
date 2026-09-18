package maintenancedomain

// Rule-engine proof of the staleWorkOrderTasks convergence cypher — the
// task-anchored gap that cancels an open task whose order was resolved by
// someone other than its assignee. Pinned per arm of the op that writes the
// gap's input: TRUE for an unclaimed queued task on an order a landlord or
// operator resolved, TRUE for a task the tech claimed on an order the
// landlord resolved, FALSE for the task path's own intermediate message (the
// tech claimed it and resolved it, .resolution landed, status still open for
// one revision), FALSE for an open task on an unresolved order (the queued
// work), no row for a task already complete or cancelled, and no row for a
// task scoped to something that is not a work order.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/lenstest"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// projectStaleTasks runs staleWorkOrderTasksSpec anchored on the named task
// and returns every row (zero or one).
func (f *lensFixture) projectStaleTasks(t *testing.T, taskName string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(staleWorkOrderTasksSpec)
	require.NoError(t, err, "staleWorkOrderTasks cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": "vtx.task." + f.ids[taskName],
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// claim wires the assignedTo link ClaimTask writes (task → identity).
func (f *lensFixture) claim(t *testing.T, taskName, whoName string) {
	t.Helper()
	if _, ok := f.ids[whoName]; !ok {
		f.vtx(t, whoName, "identity", nil)
	}
	f.edge(t, "assignedTo", taskName, whoName)
}

// TestStaleWorkOrderTasks_UnclaimedTaskOnResolvedOrderOpensTheGap is the gap
// vector: the role-queued task is still open and unclaimed (no assignee) and
// the order carries a resolution — a landlord's or an operator's — so
// resolvedBy = assignee is false under the engine's null rule and the task
// is cancelled.
func TestStaleWorkOrderTasks_UnclaimedTaskOnResolvedOrderOpensTheGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedWorkOrder(t, "wo")
	f.seedTask(t, "task", "wo", "open")
	f.resolve(t, "wo")

	rows := f.projectStaleTasks(t, "task")
	require.Len(t, rows, 1, "exactly one row per open task anchor")
	v := rows[0].Values
	require.Equal(t, true, v["missing_cancellation"], "an unclaimed open task on a resolved order is stale")
	require.Equal(t, true, v["violating"])
	require.Equal(t, "vtx.task."+f.ids["task"], v["entityKey"], "row.entityKey is the task — the CancelTask target")
	require.Equal(t, "2026-07-21T11:30:00Z", v["resolvedAt"])
	require.Equal(t, "vtx.identity."+lenstest.NanoID("tech"), v["resolvedBy"])
	require.Nil(t, v["assignee"], "the OPTIONAL assignedTo hop bound nothing")
	_, isBool := v["missing_cancellation"].(bool)
	require.True(t, isBool, "missing_cancellation must be a strict bool for Weaver's openGapColumns")
}

// TestStaleWorkOrderTasks_ClaimedTaskResolvedByAnotherOpensTheGap: the tech
// claimed the task, then the landlord resolved the order from their own
// console — the claimant's open task is stale.
func TestStaleWorkOrderTasks_ClaimedTaskResolvedByAnotherOpensTheGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedWorkOrder(t, "wo")
	f.seedTask(t, "task", "wo", "open")
	f.claim(t, "task", "tech")
	f.aspect(t, "wo", "resolution", "workOrderResolution", map[string]any{
		"notes": "Replaced the washer myself.", "resolvedAt": "2026-07-21T11:30:00Z", "resolvedBy": "vtx.identity." + lenstest.NanoID("landlord")})

	rows := f.projectStaleTasks(t, "task")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "vtx.identity."+f.ids["tech"], v["assignee"])
	require.Equal(t, true, v["missing_cancellation"], "resolved by someone other than the assignee")
	require.Equal(t, true, v["violating"])
}

// TestStaleWorkOrderTasks_TaskPathIntermediateMessageStaysClosed is the
// task path's own commit as Refractor sees it: the tech claimed the task and
// resolved it under the task grant, .resolution (resolvedBy = the tech) has
// landed, and the Processor-appended status=complete has not yet — an open
// task beside a resolution for one revision. resolvedBy = assignee, so the
// gap stays shut and no doomed CancelTask is dispatched.
func TestStaleWorkOrderTasks_TaskPathIntermediateMessageStaysClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedWorkOrder(t, "wo")
	f.seedTask(t, "task", "wo", "open")
	f.claim(t, "task", "tech")
	f.resolve(t, "wo")

	rows := f.projectStaleTasks(t, "task")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, v["assignee"], v["resolvedBy"], "the task-authorized resolver IS the assignee")
	require.Equal(t, false, v["missing_cancellation"], "the auto-complete is in flight; nothing to cancel")
	require.Equal(t, false, v["violating"])
}

// TestStaleWorkOrderTasks_OpenTaskOnUnresolvedOrderStaysClosed: the queued
// work, nothing to retire.
func TestStaleWorkOrderTasks_OpenTaskOnUnresolvedOrderStaysClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedWorkOrder(t, "wo")
	f.seedTask(t, "task", "wo", "open")

	rows := f.projectStaleTasks(t, "task")
	require.Len(t, rows, 1)
	require.Equal(t, false, rows[0].Values["missing_cancellation"], "an unresolved order's open task is the work")
	require.Equal(t, false, rows[0].Values["violating"])
	require.Nil(t, rows[0].Values["resolvedAt"])
}

// TestStaleWorkOrderTasks_CompleteTaskBesideResolutionProjectsNoRow is the
// task-path arm: ResolveWorkOrder under the task grant writes .resolution and
// the §10.6 auto-complete flips the task on the same commit, so the anchor's
// status gate excludes it — no row, nothing to cancel. A cancelled task is
// excluded the same way.
func TestStaleWorkOrderTasks_CompleteTaskBesideResolutionProjectsNoRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, status := range []string{"complete", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			f := newLensFixture(t)
			f.seedWorkOrder(t, "wo")
			f.seedTask(t, "task", "wo", status)
			f.resolve(t, "wo")

			rows := f.projectStaleTasks(t, "task")
			require.Empty(t, rows, "a %s task is not open; the status gate excludes it", status)
		})
	}
}

// TestStaleWorkOrderTasks_TaskScopedElsewhereProjectsNoRow: the scopedTo walk
// is a required MATCH to a workorder, so a task scoped to some other vertex
// type projects no row rather than a never-closing false.
func TestStaleWorkOrderTasks_TaskScopedElsewhereProjectsNoRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "app", "leaseapp", nil)
	f.vtx(t, "task", "task", map[string]any{"status": "open", "expiresAt": "2026-08-20T00:00:00Z"})
	f.edge(t, "scopedTo", "task", "app")

	require.Empty(t, f.projectStaleTasks(t, "task"))
}

// TestStaleWorkOrderTasks_SpecProjectsEveryBodyColumn: every declared
// BodyColumn is a RETURN alias of the cypher.
func TestStaleWorkOrderTasks_SpecProjectsEveryBodyColumn(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedWorkOrder(t, "wo")
	f.seedTask(t, "task", "wo", "open")
	rows := f.projectStaleTasks(t, "task")
	require.Len(t, rows, 1)
	for _, l := range Lenses() {
		if l.CanonicalName != StaleWorkOrderTasksTarget {
			continue
		}
		for _, col := range l.Output.BodyColumns {
			_, ok := rows[0].Values[col]
			require.True(t, ok, "BodyColumn %q is not projected by the cypher", col)
		}
	}
}
