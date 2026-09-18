package maintenancedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WorkOrderQueueRoleKey is the role queue every unresolved, unqueued work
// order is queued to — identity-domain's backOfHouse, by the same
// deterministic role key the showcase seed computes. It is the literal
// endpoint of the assignTask gap below rather than a row column: the queue
// is policy, not a fact the work order carries.
var WorkOrderQueueRoleKey = "vtx.role." + pkgmgr.RoleID("identity-domain", "backOfHouse")

// WeaverTargets returns the package's three meta.weaverTarget playbooks —
// frozen tables, deterministic. The package keeps its split at the op:
// ReportIssue mints the work order and never its task, ResolveWorkOrder
// writes the resolution and never cancels a task or tells anyone, so the
// queueing, backfill, cancellation and notice POLICIES live here as
// declarative targets, and each is a convergence the Weaver drives rather
// than a step a client has to take.
//
// workOrderQueue (docs/reviews/loftspace-maintenance-loop-2026-09-17.md
// decision 1; the reporter gap per loftspace-maintenance-loop-closes-2026-09-18.md
// decision 1):
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
//   - missing_reporter → directOp LinkWorkOrderReporter{workOrderKey:
//     row.entityKey} — the backfill of the reportedBy link on an order minted
//     before ReportIssue wrote it beside the root. Reads the anchor and its
//     .report (the stamp the op mints the link from); the link key itself is
//     data-derived, so nothing else is declared. Class pins the workOrder DDL
//     the way every directOp in the corpus pins its owner.
//
// A cancelled task re-opens the queue gap (the order is unresolved and
// nothing is queued to do it, so it is queued again); an expired-but-open
// task does not (it is still 'open' — that lapse is orchestration-base's
// unroutedTasks surfaced issue, an operator's call, not a re-queue); a
// resolution closes it, whoever resolved and whether or not a task ever
// existed.
//
// staleWorkOrderTasks (decision 3): an open task scopedTo a RESOLVED order is
// cancelled — orchestration-base's own directOp CancelTask{taskKey}, Class
// "task", exactly lease-signing's staleUserTasks dispatch. A task-path
// resolve auto-completes its task and never opens this; a landlord, operator
// or any other non-task resolver leaves it open, and this retires it rather
// than leaving the tech an AlreadyResolved refusal and, at +30 d, an
// UnroutedTasks issue.
//
// workOrderResolvedNotices (decision 5): a resolved order whose linked
// reporter is not the resolver and has not been told this resolution is told
// once — directOp RecordWorkOrderResolvedNotice{workOrderKey: row.entityKey,
// changeRef: row.resolvedAt}, clinic-reminders' appointmentChangeNotices
// dispatch. changeRef is the anchor's own aspect under the gap's `<> null`
// conjunct; the reporter is never a Param (it is read off the declared
// .report by the op). Reads [the anchor, .report, .resolution];
// OptionalReads [.resolvedNotice] — the marker is legitimately absent on
// the first (and, a resolution being terminal, in practice the only) notice.
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{
		workOrderQueueTarget(),
		staleWorkOrderTasksTarget(),
		workOrderResolvedNoticesTarget(),
	}
}

// workOrderQueueTarget's queue gap declares no reads of its own: the queue arm
// derives the CreateTask envelope's declared set itself — reads [queue,
// forOperation, target] (the three link endpoints the task DDL vertex_alive-
// checks), optionalReads [the stable task key]. Only a directOp gap's Reads
// reach the dispatched envelope, and a package OptionalReads on any other
// action collides with the engine's own and is refused at install. The
// reporter gap IS a directOp, so its Reads are the op's declared set.
func workOrderQueueTarget() pkgmgr.WeaverTargetSpec {
	return pkgmgr.WeaverTargetSpec{
		TargetID: WorkOrderQueueTarget,
		Description: "Every unresolved work order with no open task scoped to it is queued as a ResolveWorkOrder task " +
			"to the back-of-house role; a cancelled task re-queues the order, an open task past its expiry is " +
			"left to the unrouted-tasks issue rather than re-queued, and a resolution closes the order whoever " +
			"resolved it. Every work order whose report stamps a reporter no reportedBy link yet names gets " +
			"the link backfilled from that stamp.",
		LensRef: WorkOrderQueueTarget,
		Gaps: map[string]pkgmgr.GapActionSpec{
			"missing_task": {
				Action:    "assignTask",
				Operation: "ResolveWorkOrder",
				Queue:     WorkOrderQueueRoleKey,
				Target:    "row.entityKey",
			},
			"missing_reporter": {
				Action:    "directOp",
				Operation: "LinkWorkOrderReporter",
				Class:     workOrderVertexDDL,
				Params:    map[string]string{"workOrderKey": "row.entityKey"},
				Reads:     []string{"row.entityKey", "row.entityKey.report"},
			},
		},
	}
}

// staleWorkOrderTasksTarget cancels an open task whose order was resolved
// through a route the §10.6 auto-complete never sees — the same directOp
// CancelTask{taskKey} orchestration-base's orphanedTaskGrants and
// lease-signing's staleUserTasks already dispatch (no new op, no new
// permission; Class pins the "task" DDL so a directOp fails closed on the
// first other package that also claims the operationType).
func staleWorkOrderTasksTarget() pkgmgr.WeaverTargetSpec {
	return pkgmgr.WeaverTargetSpec{
		TargetID: StaleWorkOrderTasksTarget,
		Description: "An open task scoped to a work order that has since been resolved by someone other than the " +
			"task's claimant — a landlord at their own unit, an operator from the console — is cancelled instead " +
			"of sitting in the role queue until it expires.",
		LensRef: StaleWorkOrderTasksTarget,
		Gaps: map[string]pkgmgr.GapActionSpec{
			"missing_cancellation": {
				Action:    "directOp",
				Operation: "CancelTask",
				Class:     "task",
				Params:    map[string]string{"taskKey": "row.entityKey"},
				Reads:     []string{"row.entityKey"},
			},
		},
	}
}

// workOrderResolvedNoticesTarget tells a reporter once that someone else
// resolved their order — RecordWorkOrderResolvedNotice re-checks the row's
// resolvedAt against the live .resolution (StaleChange otherwise), refuses a
// self-resolution, writes the .resolvedNotice marker that closes the gap and
// emits the external.notification the bridge turns into a message.
func workOrderResolvedNoticesTarget() pkgmgr.WeaverTargetSpec {
	return pkgmgr.WeaverTargetSpec{
		TargetID: WorkOrderResolvedNoticesTarget,
		Description: "A reporter whose work order someone else resolved is told once, with the resolution notes; " +
			"a reporter who resolved their own order is not told.",
		LensRef: WorkOrderResolvedNoticesTarget,
		Gaps: map[string]pkgmgr.GapActionSpec{
			"missing_resolved_notice": {
				Action:        "directOp",
				Operation:     resolvedNoticeOp,
				Class:         workOrderVertexDDL,
				Params:        map[string]string{"workOrderKey": "row.entityKey", "changeRef": "row.resolvedAt"},
				Reads:         []string{"row.entityKey", "row.entityKey.report", "row.entityKey.resolution"},
				OptionalReads: []string{"row.entityKey.resolvedNotice"},
			},
		},
	}
}
