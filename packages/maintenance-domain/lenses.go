package maintenancedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WorkOrderQueueTarget is the §10.8 TargetID == the workOrderQueue lens's
// OutputKeyPattern prefix — the §10.2↔§10.8 binding Weaver reads. Both the
// target spec and the lens descriptor are built from it so the two cannot
// drift apart (lease-signing's TenancyEndTarget idiom).
const WorkOrderQueueTarget = "workOrderQueue"

// Lenses returns the workOrderQueue lens (docs/reviews/loftspace-maintenance-
// loop-2026-09-17.md decision 1): the workorder-anchored frozen-table
// convergence lens that turns an unresolved, unqueued work order into queued
// WORK. The package keeps its split at the OP — ReportIssue mints the work
// order and never its task — and carries the queueing POLICY here, as a
// declarative target, the way lease-signing's tenancyEnd relists a unit.
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName:  WorkOrderQueueTarget,
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           workOrderQueueSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "workorder",
				OutputKeyPattern: WorkOrderQueueTarget + ".{actorSuffix}",
				BodyColumns: []string{
					"violating", "missing_task", "entityKey", "resolvedAt", "openTaskCount",
				},
				EmptyBehavior: "delete",
				KeyColumn:     "entityId",
				Freshness:     "auto",
			},
		},
	}
}

// workOrderQueueSpec anchors on EVERY work order (a required MATCH —
// actorAggregate re-executes per anchor) and projects one gap:
//
//   - missing_task — the work order is unresolved and no OPEN task is scoped
//     to it: resolvedAt (the .resolution aspect's own stamp, absent until
//     ResolveWorkOrder writes the terminal marker) is null AND openTaskCount
//     is zero. → assignTask ResolveWorkOrder, queued to backOfHouse
//     (targets.go).
//   - violating is missing_task itself (Contract #10 §10.2 — Weaver
//     dispatches only violating rows).
//
// openTaskCount is the otherLiveTenancyCount CASE shape (lease-signing's
// tenancy_end_lenses.go) over orchestration-base's own open-task predicate —
// `t.data.status = 'open'` is the fragment its unroutedTasks lens matches on,
// verbatim, so what this lens counts as "queued" is exactly what that lens
// counts as routable. The task fan is walked INBOUND across scopedTo
// (task→workorder) as an OPTIONAL MATCH, so an order with no task at all
// still projects its row (openTaskCount = 0) rather than dropping out of the
// anchor set; count(DISTINCT CASE …) over the fan's keys means several tasks
// on one order never multiply the row.
//
// What the three task states do to the gap, and why each is the platform's
// own posture rather than this package's:
//
//   - open → closed. A queued task IS the work; re-queueing would race the
//     claimant. This holds whether or not the task's expiresAt has passed:
//     expiry is not a status transition (the task root stays 'open'), and an
//     open role-queued task past its expiresAt is orchestration-base's
//     unroutedTasks target's surfaced issue — an operator's intervention by
//     design, not a re-queue from here.
//   - cancelled → RE-OPENED. CancelTask is the platform's "this task is not
//     going to be done"; the order is still unresolved, so it is queued
//     again under a fresh stable task id.
//   - complete WITH a resolution → closed by the resolution: the §10.6
//     auto-complete flips the task on ResolveWorkOrder's own commit, which
//     writes .resolution in the same batch, and a resolution written on the
//     standing path with no task at all closes the gap the same way, whoever
//     resolved.
//   - complete WITHOUT a resolution → RE-OPENED. orchestration-base's
//     CompleteTask (operator- and self-granted) writes status = complete on
//     its own, with no resolution behind it; the order is still unresolved
//     and nobody holds it, so it is queued again under a fresh stable task
//     id. This is level-triggered and human-paced — every re-queue needs a
//     fresh human completion, so there is no Weaver storm, and an order
//     nobody actually fixes keeps returning to the queue until someone
//     resolves it.
//
// '= null' is the full engine's null test (ruleengine/full values.go
// equalsAny: null = null is true, any value = null is false). resolvedAt is
// carried through the aggregating WITH as a scalar off the anchor's own
// aspect, so the RETURN compares carried values and the cypher references no
// clock parameter at all — a work order's queue state is a pure function of
// its subgraph.
const workOrderQueueSpec = `
MATCH (wo:workorder {key: $actorKey})
OPTIONAL MATCH (wo)<-[:scopedTo]-(t:task)
WITH
  wo.key                        AS entityKey,
  wo.resolution.data.resolvedAt AS resolvedAt,
  count(DISTINCT CASE WHEN t.data.status = 'open' THEN t.key ELSE null END) AS openTaskCount
RETURN
  entityKey AS actorKey,
  entityKey,
  resolvedAt,
  openTaskCount,
  ((resolvedAt = null) AND (openTaskCount = 0)) AS missing_task,
  ((resolvedAt = null) AND (openTaskCount = 0)) AS violating
`
