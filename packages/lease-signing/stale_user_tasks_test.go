package leasesigning

// Rule-engine proof of the staleUserTasks convergence cypher (lenses.go), the
// TASK-anchored companion to orphanedTaskGrants: a task's own gap can close
// through a route other than the assignee finishing it, and nothing in Weaver
// itself revokes the still-open, still-correctly-granted task. Mirrors
// orchestration-base/orphaned_task_grants_test.go's structure: one positive/
// negative pair per closure route this package's three userTask ops use, plus
// the status='open' gate and the discrimination guard.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// projectStaleAt runs staleUserTasksSpec for one task actor with an injected
// $now, mirroring lens_cypher_test.go's projectAt / orphaned_task_grants_test.go's
// projectOrphanedAt.
func (f *lensFixture) projectStaleAt(t *testing.T, taskName, now string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(staleUserTasksSpec)
	require.NoError(t, err, "staleUserTasks cypher must parse on the full engine")
	taskKey := "vtx.task." + f.ids[taskName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    taskKey,
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

const staleNow = "2026-09-03T00:00:00Z"

func TestStaleUserTasks_RecordIdentityPII_SsnNotYetRecorded_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "alice", "identity")
	f.seedTask(t, "onbtask", "recordpii", "RecordIdentityPII", "alice", "open")

	v := f.projectStaleAt(t, "onbtask", staleNow)[0].Values
	require.Equal(t, "vtx.task."+f.ids["onbtask"], v["taskKey"])
	require.Equal(t, false, v["missing_cancellation"], "no .ssn yet — the task is still the live remedy")
	require.Equal(t, false, v["violating"])
}

func TestStaleUserTasks_RecordIdentityPII_SsnAlreadyRecorded_Violating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "alice", "identity")
	f.aspect(t, "alice", "ssn", "ssn", map[string]any{"value": "123456789"})
	f.seedTask(t, "onbtask", "recordpii", "RecordIdentityPII", "alice", "open")

	v := f.projectStaleAt(t, "onbtask", staleNow)[0].Values
	require.Equal(t, true, v["missing_cancellation"], "ssn already recorded through another application — this task is obsolete")
	require.Equal(t, true, v["violating"])
}

func TestStaleUserTasks_SignLease_NotYetSigned_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "app", "leaseapp")
	f.seedTask(t, "sigtask", "signlease", "SignLease", "app", "open")

	v := f.projectStaleAt(t, "sigtask", staleNow)[0].Values
	require.Equal(t, false, v["missing_cancellation"], "no .signature yet — the task is still the live remedy")
	require.Equal(t, false, v["violating"])
}

func TestStaleUserTasks_SignLease_AlreadySigned_Violating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "app", "leaseapp")
	f.aspect(t, "app", "signature", "signature", map[string]any{"signedAt": "2026-06-10T00:00:00Z"})
	f.seedTask(t, "sigtask", "signlease", "SignLease", "app", "open")

	v := f.projectStaleAt(t, "sigtask", staleNow)[0].Values
	require.Equal(t, true, v["missing_cancellation"], "signature already present — this task is obsolete")
	require.Equal(t, true, v["violating"])
}

func TestStaleUserTasks_SetRenewalTerms_NotYetSet_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "renewal1", "renewal")
	f.seedTask(t, "termstask", "setterms", "SetRenewalTerms", "renewal1", "open")

	v := f.projectStaleAt(t, "termstask", staleNow)[0].Values
	require.Equal(t, false, v["missing_cancellation"], "no .terms yet — the task is still the live remedy")
	require.Equal(t, false, v["violating"])
}

func TestStaleUserTasks_SetRenewalTerms_AlreadySet_Violating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "renewal1", "renewal")
	f.aspect(t, "renewal1", "terms", "terms", map[string]any{"setAt": "2026-08-01T00:00:00Z", "rentAmount": 2200})
	f.seedTask(t, "termstask", "setterms", "SetRenewalTerms", "renewal1", "open")

	v := f.projectStaleAt(t, "termstask", staleNow)[0].Values
	require.Equal(t, true, v["missing_cancellation"], "terms already set through another route — this task is obsolete")
	require.Equal(t, true, v["violating"])
}

// TestStaleUserTasks_UnrelatedOperation_NeverViolating is the discrimination
// guard, the reason the predicate matches on op.data.operationType rather than
// merely on the presence of the closing aspect: a task for an operation this
// target does not own must never be cancelled, even when its scopedTo target
// happens to carry every closing aspect this cypher reads.
func TestStaleUserTasks_UnrelatedOperation_NeverViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "app", "leaseapp")
	f.aspect(t, "app", "signature", "signature", map[string]any{"signedAt": "2026-06-10T00:00:00Z"})
	f.seedTask(t, "othertask", "verifyguarantor", "VerifyGuarantor", "app", "open")

	v := f.projectStaleAt(t, "othertask", staleNow)[0].Values
	require.Equal(t, false, v["missing_cancellation"], "VerifyGuarantor is not one of the three ops this target owns")
	require.Equal(t, false, v["violating"])
}

func TestStaleUserTasks_CancelledNeverMatches(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "alice", "identity")
	f.aspect(t, "alice", "ssn", "ssn", map[string]any{"value": "123456789"})
	f.seedTask(t, "onbtask", "recordpii", "RecordIdentityPII", "alice", "cancelled")

	rows := f.projectStaleAt(t, "onbtask", staleNow)
	require.Empty(t, rows, "a cancelled task is excluded by the status='open' gate even with the gap already closed")
}

func TestStaleUserTasks_CompleteNeverMatches(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "app", "leaseapp")
	f.aspect(t, "app", "signature", "signature", map[string]any{"signedAt": "2026-06-10T00:00:00Z"})
	f.seedTask(t, "sigtask", "signlease", "SignLease", "app", "complete")

	rows := f.projectStaleAt(t, "sigtask", staleNow)
	require.Empty(t, rows, "a complete task is excluded by the status='open' gate — nothing left to converge")
}
