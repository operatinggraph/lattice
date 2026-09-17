package maintenancedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WorkOrderQueueRoleKey is the role queue every unresolved, unqueued work
// order is queued to — identity-domain's backOfHouse, by the same
// deterministic role key the showcase seed computes. It is the literal
// endpoint of the assignTask gap below rather than a row column: the queue
// is policy, not a fact the work order carries.
var WorkOrderQueueRoleKey = "vtx.role." + pkgmgr.RoleID("identity-domain", "backOfHouse")

// WeaverTargets returns the workOrderQueue meta.weaverTarget playbook
// (docs/reviews/loftspace-maintenance-loop-2026-09-17.md decision 1) — frozen
// table, one gap, deterministic. The package keeps its split at the op:
// ReportIssue mints the work order and never its task, so the queueing
// POLICY lives here as a declarative target, and "a work order becomes work"
// is a convergence the Weaver drives rather than a step the reporter's
// client has to take.
//
//   - missing_task → assignTask ResolveWorkOrder, Queue backOfHouse, Target
//     row.entityKey — Weaver's queue arm submits orchestration-base's
//     CreateTask{queue, forOperation: <ResolveWorkOrder's op-meta>,
//     scopedTo: <the work order>, expiresAt, taskId} under its service actor
//     (CreateTask is operator-granted), with a claimId-seeded stable taskId so
//     a re-dispatch collapses on the task it already minted. The task is
//     queued for the ROLE and scopedTo the work order — the row's own key.
//     The queue carries no location: any holder of backOfHouse anywhere may
//     ClaimTask it (the claim checks holdsRole only) and resolve it on the
//     task path (ResolveWorkOrder's task-path resource bind skips the
//     worksAt walk); a location-scoped queue is a filed platform primitive,
//     not something this target can express.
//
// A cancelled task re-opens the gap (the order is unresolved and nothing is
// queued to do it, so it is queued again); an expired-but-open task does not
// (it is still 'open' — that lapse is orchestration-base's unroutedTasks
// surfaced issue, an operator's call, not a re-queue); a resolution closes
// it, whoever resolved and whether or not a task ever existed.
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{workOrderQueueTarget()}
}

// workOrderQueueTarget's one gap declares no reads of its own: the queue arm
// derives the CreateTask envelope's declared set itself — reads [queue,
// forOperation, target] (the three link endpoints the task DDL vertex_alive-
// checks), optionalReads [the stable task key]. Only a directOp gap's Reads
// reach the dispatched envelope, and a package OptionalReads on any other
// action collides with the engine's own and is refused at install.
func workOrderQueueTarget() pkgmgr.WeaverTargetSpec {
	return pkgmgr.WeaverTargetSpec{
		TargetID: WorkOrderQueueTarget,
		Description: "Every unresolved work order with no open task scoped to it is queued as a ResolveWorkOrder task " +
			"to the back-of-house role; a cancelled task re-queues the order, an open task past its expiry is " +
			"left to the unrouted-tasks issue rather than re-queued, and a resolution closes the order whoever " +
			"resolved it.",
		LensRef: WorkOrderQueueTarget,
		Gaps: map[string]pkgmgr.GapActionSpec{
			"missing_task": {
				Action:    "assignTask",
				Operation: "ResolveWorkOrder",
				Queue:     WorkOrderQueueRoleKey,
				Target:    "row.entityKey",
			},
		},
	}
}
