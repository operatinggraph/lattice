package maintenancedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// The §10.8 TargetIDs == each convergence lens's OutputKeyPattern prefix —
// the §10.2↔§10.8 binding Weaver reads. Both the target spec and the lens
// descriptor are built from the constant so the two cannot drift apart
// (lease-signing's TenancyEndTarget idiom).
const (
	// WorkOrderQueueTarget queues an unresolved, untasked order and backfills
	// a missing reporter link.
	WorkOrderQueueTarget = "workOrderQueue"
	// StaleWorkOrderTasksTarget cancels an open task whose order was resolved
	// through a route the §10.6 auto-complete never sees.
	StaleWorkOrderTasksTarget = "staleWorkOrderTasks"
	// WorkOrderResolvedNoticesTarget tells a reporter once that their order
	// was resolved by someone else.
	WorkOrderResolvedNoticesTarget = "workOrderResolvedNotices"

	// ReporterWorkOrdersReadLens is the reporter-anchored protected read
	// model — the rows a tenant's own card lists.
	ReporterWorkOrdersReadLens = "reporterWorkOrdersRead"
)

// Lenses returns the package's three convergence lenses and its one read
// model. The package keeps its split at the OP — ReportIssue mints the work
// order and never its task; ResolveWorkOrder writes the resolution and never
// cancels a task or tells anyone — and carries the queueing, cancellation and
// notice POLICIES here, as declarative targets, the way lease-signing's
// tenancyEnd relists a unit and clinic-reminders' appointmentChangeNotices
// tells a patient.
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
					"violating", "missing_task", "missing_reporter", "entityKey", "resolvedAt", "openTaskCount",
					"reportedBy", "reporterLinked",
				},
				EmptyBehavior: "delete",
				KeyColumn:     "entityId",
				Freshness:     "auto",
			},
		},
		{
			// staleWorkOrderTasks — the TASK-anchored companion to
			// workOrderQueue, lease-signing's staleUserTasks shape: a task whose
			// own gap already closed through a route the §10.6 auto-complete
			// never sees. A task-authorized ResolveWorkOrder completes its task
			// on the same commit; a landlord's self-leg resolve, an operator's
			// standing resolve, or any future non-task resolver writes the
			// .resolution and leaves the queued task open forever — the tech
			// opens it later and is refused AlreadyResolved, and at +30 d it
			// surfaces as UnroutedTasks. One row per open task scopedTo a work
			// order; missing_cancellation is true exactly when that order
			// carries a resolution written by someone OTHER than the task's
			// assignee (staleWorkOrderTasksSpec states why the assignee
			// conjunct is load-bearing). Reuses orchestration-base's own
			// directOp CancelTask{taskKey} verbatim (targets.go) — no new op,
			// no new permission: CancelTask is already operator-granted
			// platform-wide.
			CanonicalName:  StaleWorkOrderTasksTarget,
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           staleWorkOrderTasksSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "task",
				OutputKeyPattern: StaleWorkOrderTasksTarget + ".{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_cancellation", "entityKey", "resolvedAt", "resolvedBy", "assignee"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
			},
		},
		{
			// workOrderResolvedNotices — the ORDER-anchored notice gap,
			// clinic-reminders' appointmentChangeNotices shape: a reporter is
			// told once that their order was resolved, keyed on the
			// resolution's own stamp. The gap opens on a resolved order whose
			// reportedBy link names someone other than the resolver and whose
			// .resolvedNotice marker does not yet carry this resolvedAt; the
			// directOp RecordWorkOrderResolvedNotice (targets.go) writes the
			// marker and emits the send. Nobody is told what they resolved
			// themselves; an order with no reportedBy link (a legacy order
			// awaiting workOrderQueue's backfill) is told once the link lands.
			CanonicalName:  WorkOrderResolvedNoticesTarget,
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           workOrderResolvedNoticesSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "workorder",
				OutputKeyPattern: WorkOrderResolvedNoticesTarget + ".{actorSuffix}",
				BodyColumns: []string{
					"violating", "missing_resolved_notice", "entityKey", "reporterKey", "resolvedAt", "resolvedBy", "resolvedFor",
				},
				EmptyBehavior: "delete",
				KeyColumn:     "entityId",
			},
		},
		{
			// reporterWorkOrdersRead — the protected Postgres read model of a
			// reporter's own work orders (a tenant's "My reports" list): one
			// row per work order with a reportedBy link, anchored on the
			// reporter's identity alone (identity-domain's
			// identityCredentialsRead self-anchor shape, `[nanoIdFromKey
			// (reporter.key)] AS authz_anchors`). The reporter relation is
			// this package's own and vertical-neutral, so the lens lives here
			// rather than in a vertical's package. The unit is OPTIONAL: a
			// building-located order (a staff report) projects with a null
			// unit rather than dropping out. unit_address reads the unit's
			// .address aspect (loftspace-domain's SetUnitAddress writes it;
			// null where no vertical has set one). open_task_count is the
			// CASE-inside-count shape workOrderQueue draws over
			// orchestration-base's own `t.data.status = 'open'` fragment;
			// resolved_at / resolution_notes read .resolution; notice_sent_at
			// reads the .resolvedNotice marker RecordWorkOrderResolvedNotice
			// writes, so the card can say the reporter was told.
			//
			// DIFF RETRACTION: the row walks `reportedBy` / `locatedAt` /
			// `scopedTo` structurally, so an unwired link needs Refractor's
			// target-diff retraction path, not anchor-self.
			CanonicalName:  ReporterWorkOrdersReadLens,
			Class:          "meta.lens",
			Adapter:        "postgres",
			Table:          "read_reporter_work_orders",
			Engine:         "full",
			Spec:           reporterWorkOrdersReadSpec,
			Protected:      true,
			DiffRetraction: true,
			IntoKey:        []string{"work_order_id"},
			Columns: []pkgmgr.PostgresColumn{
				{Name: "work_order_key", Type: "text"},
				{Name: "reporter_key", Type: "text"},
				{Name: "unit_key", Type: "text"},
				{Name: "unit_address", Type: "text"},
				{Name: "summary", Type: "text"},
				{Name: "priority", Type: "text"},
				{Name: "reported_at", Type: "text"},
				{Name: "resolved_at", Type: "text"},
				{Name: "resolution_notes", Type: "text"},
				{Name: "open_task_count", Type: "double precision"},
				{Name: "notice_sent_at", Type: "text"},
			},
		},
	}
}

// workOrderQueueSpec anchors on EVERY work order (a required MATCH —
// actorAggregate re-executes per anchor) and projects two gaps:
//
//   - missing_task — the work order is unresolved and no OPEN task is scoped
//     to it: resolvedAt (the .resolution aspect's own stamp, absent until
//     ResolveWorkOrder writes the terminal marker) is null AND openTaskCount
//     is zero. → assignTask ResolveWorkOrder, queued to backOfHouse
//     (targets.go).
//   - missing_reporter — the .report stamps a reporter (reportedBy is
//     non-null) but no reportedBy link names one (reporterKey is null, so
//     reporterLinked is false). → directOp LinkWorkOrderReporter (targets.go),
//     the backfill for an order minted before ReportIssue wrote the link in
//     the same batch as the root. A fresh order never opens it — ReportIssue
//     emits the link AHEAD of the stamp, so at every CDC message of its batch
//     one of the two is still absent (the order invariant stated at the
//     mint); the backfill is the second writer of the deterministic link
//     key, arbitrated by population — it runs only where this lens proves
//     the link absent, and its CreateOnly write is refused, not rewritten,
//     on a collision. BOUND: a reporter whose identity ROOT is tombstoned
//     keeps this gap open — the walk cannot bind a dead endpoint while the
//     stamp stays — so the backfill collides on every dispatch until the
//     directOp's default retry budget parks the row with a standing Health
//     issue. Stated as the bound it is: no op in the corpus tombstones an
//     identity root today, so the shape is not constructible.
//   - violating is missing_task OR missing_reporter (Contract #10 §10.2 —
//     Weaver dispatches only violating rows, then reads each gap column).
//
// openTaskCount is the otherLiveTenancyCount CASE shape (lease-signing's
// tenancy_end_lenses.go) over orchestration-base's own open-task predicate —
// `t.data.status = 'open'` is the fragment its unroutedTasks lens matches on,
// verbatim, so what this lens counts as "queued" is exactly what that lens
// counts as routable. The task fan is walked INBOUND across scopedTo
// (task→workorder) as an OPTIONAL MATCH, so an order with no task at all
// still projects its row (openTaskCount = 0) rather than dropping out of the
// anchor set; count(DISTINCT CASE …) over the fan's keys means several tasks
// on one order never multiply the row, and the single reportedBy hop beside
// the fan cannot multiply it either (one link per order).
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
//     again under a fresh stable task id. A task the staleWorkOrderTasks
//     target cancelled is the exception in effect, not in rule: that target
//     cancels only on a resolved order, and a resolution closes this gap.
//   - complete WITH a resolution → closed by the resolution: the §10.6
//     auto-complete flips the task on ResolveWorkOrder's own commit, which
//     writes .resolution in the same batch, and a resolution written on the
//     standing or landlord path with no task at all closes the gap the same
//     way, whoever resolved.
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
// equalsAny: null = null is true, any value = null is false). resolvedAt,
// reportedBy and reporterKey are carried through the aggregating WITH as
// scalars off the anchor's own aspect / its one-hop link, so the RETURN
// compares carried values and the cypher references no clock parameter at
// all — a work order's queue state is a pure function of its subgraph.
const workOrderQueueSpec = `
MATCH (wo:workorder {key: $actorKey})
OPTIONAL MATCH (wo)<-[:scopedTo]-(t:task)
OPTIONAL MATCH (wo)-[:reportedBy]->(r:identity)
WITH
  wo.key                        AS entityKey,
  wo.resolution.data.resolvedAt AS resolvedAt,
  wo.report.data.reportedBy     AS reportedBy,
  r.key                         AS reporterKey,
  count(DISTINCT CASE WHEN t.data.status = 'open' THEN t.key ELSE null END) AS openTaskCount
RETURN
  entityKey AS actorKey,
  entityKey,
  resolvedAt,
  openTaskCount,
  reportedBy,
  (reporterKey <> null) AS reporterLinked,
  ((resolvedAt = null) AND (openTaskCount = 0)) AS missing_task,
  ((reportedBy <> null) AND (reporterKey = null)) AS missing_reporter,
  (((resolvedAt = null) AND (openTaskCount = 0)) OR ((reportedBy <> null) AND (reporterKey = null))) AS violating
`

// staleWorkOrderTasksSpec is the TASK-anchored convergence cypher for
// staleWorkOrderTasks (Lenses(), above): one row per open task scopedTo a
// work order. The status='open' gate is orchestration-base's own routable-
// task fragment verbatim (an already complete/cancelled task has nothing left
// to converge — orphanedTaskGrantsSpec's own reasoning); the scopedTo walk is
// a required MATCH, so a task scoped to anything but a work order projects
// no row at all rather than a never-closing false.
//
// The gap is grounded in the RECORDED fact the §10.6 auto-complete rests on:
// a task-authorized resolver IS the task's assignee. missing_cancellation is
// `resolvedAt <> null AND NOT (resolvedBy = assignee)` — the order carries a
// resolution written by someone other than whoever holds the task. The
// assignee conjunct is what keeps the gap shut across the task path's own
// commit: ResolveWorkOrder under the task grant writes .resolution and the
// Processor APPENDS the task's status=complete to the same batch, so
// Refractor sees .resolution land one CDC message before the task flips —
// an open task beside a resolution, for one revision. With resolvedBy = the
// assignee that message reads false; without the conjunct it would fire a
// doomed CancelTask (InvalidTransition on the already-complete task) on every
// tech resolve. An unclaimed role-queued task has no assignee (the OPTIONAL
// assignedTo hop binds nothing, assignee null), and under this engine
// `resolvedBy = null` is false, so a resolution by anyone opens the gap —
// the landlord and operator routes. A claimed task whose order a DIFFERENT
// actor resolved opens it the same way.
//
// The hole this leaves, stated: a claimant who resolves their own claimed
// order on the STANDING path (staff grant, no task authContext) leaves the
// claimed task open with resolvedBy = assignee — false here by design. That
// task is theirs to complete from their inbox (orchestration-base's
// CompleteTask self grant), and it expires into UnroutedTasks otherwise.
const staleWorkOrderTasksSpec = `
MATCH (t:task {key: $actorKey})
  WHERE t.data.status = 'open'
MATCH (t)-[:scopedTo]->(wo:workorder)
OPTIONAL MATCH (t)-[:assignedTo]->(a:identity)
WITH
  t.key                         AS entityKey,
  wo.resolution.data.resolvedAt AS resolvedAt,
  wo.resolution.data.resolvedBy AS resolvedBy,
  a.key                         AS assignee
RETURN
  entityKey AS actorKey,
  entityKey,
  resolvedAt,
  resolvedBy,
  assignee,
  ((resolvedAt <> null) AND NOT (resolvedBy = assignee)) AS missing_cancellation,
  ((resolvedAt <> null) AND NOT (resolvedBy = assignee)) AS violating
`

// workOrderResolvedNoticesSpec is the ORDER-anchored notice cypher for
// workOrderResolvedNotices (Lenses(), above): one row per work order, the gap
// true exactly when the order is resolved (resolvedAt non-null), a reporter is
// linked (the OPTIONAL reportedBy hop bound — the `<> null` conjunct is what
// keeps reporterKey off the row's Params and lets the op read the recipient
// from the declared .report instead), the resolver is not the reporter
// (resolvedBy <> reporterKey — the op's SelfResolved refusal is this conjunct
// verbatim), and the .resolvedNotice marker does not yet carry this
// resolvedAt (resolvedFor <> resolvedAt; a null resolvedFor compares unequal,
// so a never-told order opens it). Every value is carried through the WITH
// as a scalar, so the RETURN references no clock.
const workOrderResolvedNoticesSpec = `
MATCH (wo:workorder {key: $actorKey})
OPTIONAL MATCH (wo)-[:reportedBy]->(reporter:identity)
WITH
  wo.key                             AS entityKey,
  reporter.key                       AS reporterKey,
  wo.resolution.data.resolvedAt      AS resolvedAt,
  wo.resolution.data.resolvedBy      AS resolvedBy,
  wo.resolvedNotice.data.resolvedFor AS resolvedFor
RETURN
  entityKey AS actorKey,
  entityKey,
  reporterKey,
  resolvedAt,
  resolvedBy,
  resolvedFor,
  ((resolvedAt <> null) AND (reporterKey <> null) AND (resolvedBy <> reporterKey) AND (resolvedFor <> resolvedAt)) AS missing_resolved_notice,
  ((resolvedAt <> null) AND (reporterKey <> null) AND (resolvedBy <> reporterKey) AND (resolvedFor <> resolvedAt)) AS violating
`

// reporterWorkOrdersReadSpec — see the Lenses() declaration above for the
// shape rationale. Anchored on the reportedBy link (a required MATCH, so an
// order with no reporter link projects nothing — there is no identity to
// anchor a row on), the unit and the task fan walked as OPTIONAL MATCHes.
// The aggregating WITH extracts every RETURN column as a passthrough alias
// first, since a WITH containing an aggregation (count()) must extract every
// non-aggregated column it carries at the same stage. authz_anchors is the
// reporter alone: this is a self-anchored surface, not a portfolio one.
const reporterWorkOrdersReadSpec = `MATCH (wo:workorder)-[:reportedBy]->(reporter:identity)
OPTIONAL MATCH (wo)-[:locatedAt]->(u:unit)
OPTIONAL MATCH (wo)<-[:scopedTo]-(t:task)
WITH
  wo.key                          AS entityKey,
  reporter.key                    AS reporterKey,
  u.key                           AS unitKey,
  u.address.data.line1            AS unitAddress,
  wo.report.data.summary          AS summary,
  wo.report.data.priority         AS priority,
  wo.report.data.reportedAt       AS reportedAt,
  wo.resolution.data.resolvedAt   AS resolvedAt,
  wo.resolution.data.notes        AS resolutionNotes,
  wo.resolvedNotice.data.sentAt   AS noticeSentAt,
  count(DISTINCT CASE WHEN t.data.status = 'open' THEN t.key ELSE null END) AS openTaskCount
RETURN
  nanoIdFromKey(entityKey)        AS work_order_id,
  entityKey                       AS work_order_key,
  reporterKey                     AS reporter_key,
  unitKey                         AS unit_key,
  unitAddress                     AS unit_address,
  summary                         AS summary,
  priority                        AS priority,
  reportedAt                      AS reported_at,
  resolvedAt                      AS resolved_at,
  resolutionNotes                 AS resolution_notes,
  openTaskCount                   AS open_task_count,
  noticeSentAt                    AS notice_sent_at,
  [nanoIdFromKey(reporterKey)]    AS authz_anchors
`
