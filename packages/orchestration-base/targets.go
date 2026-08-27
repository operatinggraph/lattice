package orchestrationbase

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's three meta.weaverTargets.
//
// unroutedTasks (Contract #10 §10.1 FR29 — "unrouted tasks surface; never
// silently dropped"). Its one gap, missing_claim, maps to the §10.8
// `surface` action: no remediation is dispatched — the gap just
// raises/clears a named Health-KV issue (Contract #5 §5.5 issues[]) for as
// long as an open, role-queued task stays unclaimed past its own expiresAt.
// Manual intervention only; auto-escalation is an explicitly deferred
// follow-on (§10.1).
//
// staleAssignedTasks is unroutedTasks' mirror for the OTHER routing shape: a
// DIRECT assignment (not queued to a role) whose deadline has passed
// unclaimed by completion. It exists because a directly assigned task has no
// FR29-equivalent backstop of its own — unroutedTasks' required
// -[:queuedFor]-> match structurally never fires on one, so without this
// target a stuck direct assignment surfaces nowhere. Same `surface`-only
// posture and reasoning: CompleteTask stays legal for the assignee at any
// time (scope=self, permissions.go), so auto-cancelling would revoke a
// still-valid path rather than protect one.
//
// orphanedTaskGrants — a real REMEDIATION target, unlike the two above: its
// one gap, missing_operation, maps to `directOp`, dispatching
// CancelTask{taskKey} (ddls.go's transition_task, operator-granted,
// permissions.go) the moment orphanedTaskGrantsSpec (lenses.go) finds an
// open task whose forOperation link points at nothing live. Class pins the
// "task" DDL the same way wellness-domain's ReleaseOrphanedBooking target
// pins "booking" — CancelTask is unambiguous today but a directOp fails
// closed forever on the first other package that also claims the
// operationType, so it stays pinned regardless. Reads carries only
// row.taskKey: CancelTask's transition_task reads nothing but the task root
// itself (state[task_key]) to check vertex_alive + status.
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{
		{
			TargetID: "unroutedTasks",
			Description: "Every task queued to a role is claimed before its grant expires. A task whose deadline " +
				"passes unclaimed is flagged for an operator rather than quietly abandoned.",
			LensRef: "unroutedTasks",
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_claim": {Action: "surface", IssueCode: "UnroutedTasks", IssueSeverity: "warning"},
			},
		},
		{
			TargetID: "staleAssignedTasks",
			Description: "Every directly assigned task is completed before its grant expires. A task whose deadline " +
				"passes unfinished is flagged for an operator, since a direct assignment has no queue to " +
				"fall back into.",
			LensRef: "staleAssignedTasks",
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_completion": {Action: "surface", IssueCode: "StaleAssignedTask", IssueSeverity: "warning"},
			},
		},
		{
			TargetID: "orphanedTaskGrants",
			Description: "Every open task still points at an operation someone can actually run. A task whose " +
				"granted operation no longer exists is cancelled instead of sitting permanently " +
				"unactionable.",
			LensRef: "orphanedTaskGrants",
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_operation": {
					Action:    "directOp",
					Operation: "CancelTask",
					Class:     "task",
					Params:    map[string]string{"taskKey": "row.taskKey"},
					Reads:     []string{"row.taskKey"},
				},
			},
		},
	}
}
