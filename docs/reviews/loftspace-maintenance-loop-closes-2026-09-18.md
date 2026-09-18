# LoftSpace — the maintenance loop closes both ways: a tenant sees their report through to its resolution, a landlord closes an order at their own unit (2026-09-18)

**Filed (PO, 2026-09-18, `bf4d4bef`), two rows built as one fire:**

- *"A tenant's reported issue is gone after the toast"* ★★ S, pkg + FE — "`ReportIssue`'s success callback is a no-op
  (`app.js:2186`); `landlordWorkOrdersRead` anchors the landlord only (`lenses.go:346`) and maintenance-domain fires no
  notice on `.resolution`. Live: Jordan's 09-17 report reads 0 rows as Jordan. A reporter-anchored lens + a resolved
  notice."
- *"A landlord cannot close a work order at their own unit"* ★★ S, pkg + FE — "`ResolveWorkOrder` grants operator + the
  tech's task grant only (`permissions.go:52`); no withdraw verb exists and the panel is read-only, no reporter
  (`app.js:5060`). Live: Nora sees 14 identical open urgent riser-valve orders at 10 Riverside Walk. A landlord `manages`
  leg + who reported it."

One fire: both rows are the un-closed halves of the loop `ea15ca76` opened ([design](loftspace-maintenance-loop-2026-09-17.md))
and land in the same three places — `maintenance-domain`, `loftspace-domain`, `cmd/loftspace-app`. Winston-adjudicated:
every mechanism mirrors a shipped pattern (the `require_manages` self leg, `staleUserTasks`' CancelTask gap, clinic's
`appointmentChangeNotices` notice loop, the `landlordWorkOrdersRead` protected lens + handler + panel). No contract surface.

## Grounding

- **Live census (2026-09-18, Core KV via `lattice graph keys`):** 19 `vtx.workorder.*` roots, 19 `.report`, 3 `.resolution`,
  20 `lnk.task.*.scopedTo.workorder.*`. `.report = {summary, priority, reportedAt, reportedBy}`; `.resolution = {notes,
  resolvedAt, resolvedBy}` — both stamp the actor as a KEY IN DATA ([ddls.go:610-612, 689-691](../../packages/maintenance-domain/ddls.go)).
  No link ties a work order to its reporter, so nothing can walk from an identity to its reports, and no lens in
  `packages/` joins on a data-stored key (the engine walks links; `reported_by` is projected as a bare column,
  [lenses.go:358](../../packages/loftspace-domain/lenses.go)). Jordan Ellis's live report is
  `vtx.workorder.whDpTDRwQiJmA6PfFhdF` (`reportedBy: vtx.identity.dzst9ZB6Q8Jhw4m9hHVG`).
- **`ResolveWorkOrder` today** ([ddls.go:620-695](../../packages/maintenance-domain/ddls.go)): liveness → `resource_bound =
  op.authTargetValidated and op.authContextTarget == wkey` (the task grant) → else operator exempt → else
  `enforce_workplace([workorder_location(wkey)])`; then the read-before-write terminal on `.resolution` (idempotent on
  equal notes, `AlreadyResolved` otherwise). Grants: `operator` scope=any only ([permissions.go:52](../../packages/maintenance-domain/permissions.go));
  op-meta dispatches `task`, `TargetField: workOrderKey`, reads `{payload.workOrderKey}`, optionalReads
  `{payload.workOrderKey}.resolution` ([permissions.go:105-121](../../packages/maintenance-domain/permissions.go)).
  A task-authorized resolve auto-completes the task ([internal/processor/autocomplete.go:48](../../internal/processor/autocomplete.go));
  an operator resolve leaves the queued task `open` — nothing cancels it, so the tech opens it later and is refused
  `AlreadyResolved`, and at +30 d it surfaces as `UnroutedTasks`.
- **The landlord self-leg precedent — `require_manages`** ([lease-signing/scripts.go:573-611](../../packages/lease-signing/scripts.go)):
  keyed on `op.authTargetValidated and op.authContextTarget == op.actor`, reads `lnk.identity.<actor>.manages.unit.<u>`,
  `AuthDenied` naming no unit. `DecideLeaseApplication`'s `{Scope: self, GrantsTo: [consumer]}` row
  ([permissions.go:143-147](../../packages/lease-signing/permissions.go)). The maintenance package's own leg selection
  rule (dossier, seventh minter; [ddls.go:545-603](../../packages/maintenance-domain/ddls.go)): select the bind by the
  caller's STATED target — `authContextTarget == actor` first, any other validated target refused, then the standing
  walk. `require_residence` reads its link from `state` alone (declared by the tenant dispatcher; undeclared = absent =
  refused).
- **The stale-task precedent — `staleUserTasks`** ([lease-signing/lenses.go:1373](../../packages/lease-signing/lenses.go),
  [targets.go:217-235](../../packages/lease-signing/targets.go)): a task-anchored lens (`{key: $actorKey}`, `status =
  'open'`) whose gap `missing_cancellation` dispatches orchestration-base's own `directOp CancelTask{taskKey: row.taskKey}`
  (`Class: task`, operator-granted — the Weaver actor holds it) when the task's own gap closed through another route.
- **The notice precedent — clinic-reminders** ([changenotice.go](../../packages/clinic-reminders/changenotice.go),
  [notifications.go](../../packages/clinic-reminders/notifications.go), [targets.go:59-96](../../packages/clinic-reminders/targets.go)):
  a level-triggered weaver-targets lens keyed on a recorded change value (`changeNotice.cancelledFor <> at`), one
  Weaver-actor op `RecordAppointmentChangeNotice{kind, changeRef}` that re-checks the change against the live aspect
  (`StaleChange`), writes the marker as create-or-bare-update, and emits `external.notification` with
  `instanceKey = idempotencyKey = externalRef = <key>:<kind>:<changeRef>`, `adapter: notification`, `replyOp`; the replyOp
  upserts `.changeNotification = {status, sentAt}` (audit only). The bridge reads `params` as opaque JSON
  ([internal/bridge/dispatch.go:201-227](../../internal/bridge/dispatch.go)); the recipient rides in `params`.
  Wellness's mirror ([wellness-reminders/changenotice.go](../../packages/wellness-reminders/changenotice.go)) carries the
  two build-time findings that bind here: the gap carries the OPTIONAL hop's `<> null` conjunct (now
  `lint-gap-params-optional-hop`), and the marker write is bare.
- **The read path** — `landlordWorkOrdersRead` ([loftspace-domain/lenses.go:346-377](../../packages/loftspace-domain/lenses.go)):
  protected Postgres, `DiffRetraction`, IntoKey `[work_order_id, landlord_id]`, anchors `[landlord] + covering
  buildings`; handler `handleLandlordWorkOrders` → `queryLandlordWorkOrders` under `set_config('lattice.actor_id')`
  ([cmd/loftspace-app/work_orders.go:23-113](../../cmd/loftspace-app/work_orders.go)); panel `renderLandlordWorkOrders`
  / `renderWorkOrderCard` / `workOrderState` ([app.js:5007-5102](../../cmd/loftspace-app/web/app.js)), goja-pinned
  (`work_order_state_test.go`). The tenant card is `renderApplicationCard` ([app.js:2089](../../cmd/loftspace-app/web/app.js));
  its report control is appended with an empty `onDone` (`:2186`); `submitReportIssue` (`:2774-2810`) already stages
  `sent`/`confirmed`. `state.applicant` is the signed-in identity's key; `landlordSubmit()` returns `{authContext:
  {target: state.applicant}}` (`:1136`). A pure self-anchored protected lens is `identityCredentialsRead`
  (`[nanoIdFromKey(u.key)] AS authz_anchors`, [identity-domain/lenses.go:180-187](../../packages/identity-domain/lenses.go)).
  An identity's `name` is a SENSITIVE aspect ([identity-domain/ddls.go:330](../../packages/identity-domain/ddls.go)) —
  projecting it makes a lens a Secure Lens (`applicant_name` in `landlordLeaseApplicationsRead`).
- **Facet's catalog** ([edge-manifest/lenses.go:653-682](../../packages/edge-manifest/lenses.go)): `edgeCatalogTail`
  withholds an op reached through a `scope=self` grant when the descriptor dispatches `standing`. `ResolveWorkOrder`'s
  descriptor dispatches `task`; a self grant on it would otherwise project a `manifest.op` row for every consumer whose
  form sends a task authContext the caller does not hold — the same unauthorizable shape.
- **Versions:** maintenance-domain 0.3.0 · loftspace-domain 0.15.0 · edge-manifest 0.17.19. `refresh-loftspace` diff-applies
  all three (edge-manifest via `reinstall-package`) and chains `provision-readpath` + the app rebuild
  ([Makefile:1757-1784](../../Makefile)). No `verify-package-maintenance-domain.go`; `verify-package-loftspace-domain.go`
  exists (check its lens/column pins).

## Decisions (Winston)

1. **A work order links to its reporter.** `ReportIssue` also writes `lnk.workorder.<wid>.reportedBy.identity.<actorId>`
   in the same batch (sentence: *workorder reportedBy identity*; the later-arriving vertex is the source — Contract #1
   §1.1). `.report.reportedBy` stays as the audit stamp; the LINK is what the read path and the notice walk. **The 19
   legacy orders converge through the package's own gap**: `workOrderQueue` gains `OPTIONAL MATCH (wo)-[:reportedBy]->(r:identity)`,
   columns `reportedBy` (the stamp) + `reporterLinked = (r.key <> null)`, gap `missing_reporter = reportedBy <> null AND
   NOT reporterLinked` → `directOp LinkWorkOrderReporter{workOrderKey: row.entityKey}` (`Reads: [row.entityKey,
   row.entityKey.report]`, `OptionalReads: [the link key is data-derived — none]`). The op reads `.report` from state
   (`(d)`), mints the link CreateOnly (`make_link` create), operator-granted (the Weaver actor). Two writers of one
   deterministic key, arbitrated by population: `ReportIssue` writes it atomically with the root (a new order never opens
   the gap); the backfill op runs only where the lens proves the link absent, and a CreateOnly collision is a refused
   no-op, not a rewrite. `violating` becomes `missing_task OR missing_reporter`.
2. **`ResolveWorkOrder` gains a landlord self leg.** `{ResolveWorkOrder, Scope: self, GrantsTo: [consumer]}`. Script, after
   liveness and BEFORE the terminal read (the existing oracle argument), selecting the bind by the caller's stated
   target: `if op.authContextTarget == op.actor:` → `require_manages_unit(wkey)`: the order's location must be a `unit`
   (`workorder_location`, the existing `(e)` walk) and `lnk.identity.<actor>.manages.unit.<uid>` must be live IN STATE
   (`# read-posture: (d)` — declared by the landlord dispatcher as an OptionalRead; undeclared or forged = absent =
   `AuthDenied`, no unit named, `require_manages`' shape); `elif resource_bound or actor_holds_operator(op.actor): pass`;
   `elif op.authTargetValidated: fail AuthDenied` (a validated target that is neither this order nor the caller — today
   that shape falls to the staff walk; it is refused outright, the `ReportIssue` leg-2 posture); `else: enforce_workplace`.
   A tenant holding `consumer` reaches the leg and is refused (no `manages` link); staff who are also landlords opt into
   the stricter bind by naming themselves. `.resolution.resolvedBy` records the landlord as any resolver.
3. **A resolved order's open task is cancelled by convergence.** New lens `staleWorkOrderTasks` (weaver-targets,
   `{key: $actorKey}` on the task, `t.data.status = 'open'`, `MATCH (t)-[:scopedTo]->(wo:workorder)`, column `resolvedAt`),
   gap `missing_cancellation = resolvedAt <> null` → `directOp CancelTask{taskKey: row.entityKey}` (`Class: task`, `Reads:
   [row.entityKey]`) — `staleUserTasks`' exact shape. Covers a landlord resolve, an operator resolve, and any future
   non-task resolver; a task-authorized resolve auto-completes and never opens it (pinned).
4. **`reporterWorkOrdersRead` in `maintenance-domain`** — the reporter relation is the package's own, vertical-neutral.
   Protected Postgres, `DiffRetraction`, IntoKey `[work_order_id]`, `MATCH (wo:workorder)-[:reportedBy]->(reporter:identity)
   OPTIONAL MATCH (wo)-[:locatedAt]->(u:unit) OPTIONAL MATCH (wo)<-[:scopedTo]-(t:task)` (anchor-first tails); columns
   `work_order_key, reporter_key, unit_key, unit_address, summary, priority, reported_at, resolved_at, resolution_notes,
   open_task_count, notice_sent_at (= wo.resolvedNotice.data.sentAt)`; `authz_anchors = [nanoIdFromKey(reporter.key)]`.
   A building-located staff report projects with a null unit.
5. **A resolution is told to its reporter once, keyed on `resolvedAt`.** Lens `workOrderResolvedNotices` (weaver-targets,
   actorAggregate on the order): `OPTIONAL MATCH (wo)-[:reportedBy]->(reporter:identity)`; columns `entityKey, reporterKey,
   resolvedAt, resolvedBy, resolvedFor (= wo.resolvedNotice.data.resolvedFor)`; gap `missing_resolved_notice = resolvedAt <>
   null AND reporterKey <> null AND resolvedBy <> reporterKey AND resolvedFor <> resolvedAt` → `directOp
   RecordWorkOrderResolvedNotice{workOrderKey: row.entityKey, changeRef: row.resolvedAt}` (`Reads: [row.entityKey,
   row.entityKey.report, row.entityKey.resolution]`, `OptionalReads: [row.entityKey.resolvedNotice]`; `row.resolvedAt` is
   the anchor's own aspect bound by the gap's `<> null` — clean under `lint-gap-params-optional-hop`). Nobody is told
   what they resolved themselves; a legacy order with no link is told once the backfill links it (the resolved ones
   among the 19 are staff-reported by the primordial admin — told, harmlessly, through the fake adapter). Op (Weaver-actor
   `operator` grant): re-checks `.resolution.resolvedAt == changeRef` else `StaleChange`; recipient = `.report.reportedBy`
   (declared; the link was minted from it) and refuses `SelfResolved` when it equals `resolvedBy`; writes `.resolvedNotice =
   {resolvedFor: changeRef, sentAt}` create-or-bare-update; emits `external.notification` `{instanceKey = idempotencyKey =
   externalRef = <wkey>:resolved:<changeRef>, adapter: notification, replyOp: RecordWorkOrderResolvedNotification, params:
   {workOrderKey, reporterKey, changeType: resolved, changeRef, summary, notes}}`. ReplyOp `RecordWorkOrderResolvedNotification
   {externalRef, status, result}` upserts `.resolvedNotification = {status, sentAt}` (clinic's replyOp verbatim, incl. its
   externalRef parse). Two new aspect-type DDLs (`workOrderResolvedNotice`, `workOrderResolvedNotification`); both ops
   join the vertex DDL's `PermittedCommands`; op-metas declared for discoverability (bare, no form — the loftspace-ledger
   replyOp posture).
6. **The landlord panel says who reported it and closes an order.** `landlordWorkOrdersRead` gains `reported_by_resident`
   (`[(wo)-[:reportedBy]->(r:identity)-[:residesIn]->(u) | r.key]` non-empty) — an identity's name is sensitive and this
   lens stays plain; the card reads *Reported by the resident* / *Reported by staff*, and, when the landlord's own
   applications list holds an approved application by `reportedBy` at that unit, the applicant's name in place of "the
   resident" (a client-side label over two projections the landlord already holds under RLS, goja-pinned). An unresolved
   card gains **Resolve** → `prompt` for notes → `submitOp({operationType: ResolveWorkOrder, class: workOrder, reads:
   [workOrderKey], optionalReads: [workOrderKey + ".resolution", "lnk.identity.<me>.manages.unit.<unitId>"], payload:
   {workOrderKey, notes}}, landlordSubmit())`, `sent`/`confirmed` staging, refusal-courtesy lines for every
   `ResolveWorkOrder` code at the new site, `loadLandlord`-style refresh on success.
7. **The tenant card lists their reports.** `GET /api/my/work-orders` (`queryReporterWorkOrders`, the RLS handler shape)
   → a **My reports** list on the approved-tenant card beneath the report control: `summary · priority · state
   (reported / queued / resolved, `workOrderState` reused) · reported <local instant> · resolution notes · "told <sentAt>"`.
   `submitReportIssue`'s `onDone` reloads that list. The app's wire struct pair (`reporterWorkOrderRow`) is the fixture
   source for the goja pin.
8. **Facet's catalog withholds a self grant on a `task` descriptor too.** `edgeCatalogTail`'s exclusion becomes `NOT
   (perm.data.scope = "self" AND (op.dispatch.data.authContext = "standing" OR op.dispatch.data.authContext = "task"))`
   — the same "no client-authorable shape" argument; `perm` null keeps the row as before. A self-on-task vector joins
   `lens_cypher_test.go`. edge-manifest 0.17.20.
9. **Non-goals.** No reporter/tenant withdraw verb (the ask names the landlord); no name column on a plain lens (a
   Secure Lens is a separate decision); no notice on `ReportIssue` itself or on queueing; `ResolveWorkOrder`'s op-meta stays
   `task`-dispatched (the landlord's dispatcher is loftspace-app, as the tenant's is for `ReportIssue`); no re-queue of a
   cancelled stale task (its gap is closed by the resolution); the seed is untouched (its `ReportIssue` gains the link
   through the op; its declared reads are unchanged).

## Build

**Scope sentence.** A tenant's report stays on their card — listed with its state and, once resolved, its notes and that
they were told; a landlord sees who reported each order at their units and can resolve one themselves, its queued task
cancelled by convergence; Facet offers neither self leg. Green: Jordan Ellis's 09-17 report reads on his own card (backfilled
link), a fresh report of his appears there on submit and, resolved by Nora Vance from her console, shows its notes and a
sent notice; Nora's panel says *Reported by the resident* on it and the queued task is `cancelled`.

**Touch-list (verified 2026-09-18).**
- `packages/maintenance-domain/`: `ddls.go` (`:31-97` vertex DDL `PermittedCommands` + two new aspect-type DDLs beside
  `:129-160`; `:545-613` ReportIssue link mint; `:620-695` ResolveWorkOrder self leg; new `LinkWorkOrderReporter`,
  `RecordWorkOrderResolvedNotice`, `RecordWorkOrderResolvedNotification` branches — the notice ops may live in a new
  `notices.go` the way clinic-reminders splits `changenotice.go`/`notifications.go`, if `DDLs()` composes them) ·
  `permissions.go` (`:38-57` grants; `:86-122` op-metas — add the self grant, three operator grants, two bare op-metas) ·
  `lenses.go` (`:94-108` `workOrderQueue` + BodyColumns `:30-31`; new `staleWorkOrderTasks`, `workOrderResolvedNotices`,
  `reporterWorkOrdersRead`) · `targets.go` (`:56-62`; two new targets) · `manifest.yaml` + `package.go:70` → 0.4.0 · tests:
  `lens_cypher_test.go` (gap pins per decision 1/3/5), `report_issue_self_leg_test.go` (link key literal pin), new
  `resolve_landlord_leg_test.go` (integration vectors), `package_test.go` (grant + PermittedCommands pins).
- `packages/loftspace-domain/`: `lenses.go:346-377` (+ Columns `:126-176`) · `landlord_work_orders_lens_test.go` · `manifest.yaml`
  + `package.go:51` → 0.16.0 · `scripts/verify-package-loftspace-domain.go` (check for column/lens pins).
- `packages/edge-manifest/`: `lenses.go:680-682` · `lens_cypher_test.go:435-480` (self-on-task vector) · `manifest.yaml` +
  `package.go:26` → 0.17.20.
- `cmd/loftspace-app/`: `work_orders.go` (+ `reporterWorkOrderRow`, `queryReporterWorkOrders`, `handleMyWorkOrders`) ·
  `server.go:86` (route) · `web/app.js` (`:2186` onDone; `:2089-2233` tenant card list; `:5007-5102` panel label + Resolve;
  a `reporterLabel(row, applications)` + `workOrderState` reuse) · goja pins (`work_order_state_test.go` shape) · `index.html`
  if the panel needs a container.
- `Makefile:1757-1784`: nothing new (maintenance-domain already in the chain; `provision-readpath` chained).

**Precedents.** Self leg: `lease-signing/scripts.go:573-611` + `maintenance-domain/ddls.go:493-522, 545-603`. Stale task:
`lease-signing/lenses.go:1373-1410` + `targets.go:217-235`. Notice: `clinic-reminders/changenotice.go`, `notifications.go`,
`targets.go:59-96`, `changenotice_op_test.go`, `changenotice_cypher_test.go`. Link mint pin:
`TestClauseSatisfaction_GovernsLinkKeyNamesTheLeaseappType`. Protected lens + handler + panel: `loftspace-domain/lenses.go:346-377`
+ `landlord_work_orders_lens_test.go` + `cmd/loftspace-app/work_orders.go` + `app.js:5007-5102`; landlord action:
`decideApplication` (`app.js:5744-5826`); tenant submit: `submitReportIssue` (`:2774`).

**Increments.** (1) maintenance-domain whole (decisions 1–5; `go test ./packages/maintenance-domain/ ./packages/orchestration-base/
./internal/refractor/ -run 'Corpus|Census|.'`). (2) loftspace-domain column + edge-manifest conjunct + app handler + FE +
pins (decisions 6–8; `go test ./packages/loftspace-domain/ ./packages/edge-manifest/ ./cmd/loftspace-app/`). Disjoint files —
built in parallel in one worktree. Then the gate loop (`go build ./...`, `make vet`, `golangci-lint run ./...`, every
`scripts/lint-*.go` under `STRICT=1`, `gofmt -l scripts/`, `DIFF_BASE=<base> lint-package-version`), the live rollout
(`refresh-loftspace` = diff-apply + `provision-readpath` + app cycle; `reinstall-package PKG=edge-manifest`; `lens health`
on `reporterWorkOrdersRead`; weaver needs no cycle — targets hot-reload), and the live proof above.

**Gotchas (dossier, part 5 — the entries this fire trips).** *The leg of a guard is every op that writes the guarded value*
— the self leg lists every field the write stamps (none new; `resolvedBy` is the actor on every leg); *select the bind by
the caller's stated target* (seventh minter — this package). *A dispatch declaration must name what the runtime binds* —
`row.resolvedAt` is the anchor's own aspect under the gap's `<> null`; `reporterKey` off the OPTIONAL hop is never a Param.
*For every ARM of the op pin the gap FALSE over the state it leaves* — resolved notice: FALSE after the marker write, FALSE
when resolver = reporter, FALSE with no link; stale task: FALSE on an auto-completed task; reporter backfill: FALSE on a
linked order. *A consumer's exclusion is grounded in the predicate that enforces it* — `status = 'open'` is
orchestration-base's fragment verbatim; the op's `SelfResolved` refusal is the lens's `resolvedBy <> reporterKey` verbatim.
*A mirror that drops a precedent's branch drops its invariant* — the notice op keeps clinic's `StaleChange` re-check and the
carry-the-other-fields marker (one field here; state it); the replyOp keeps the externalRef parse. *One deterministic key,
one writer* — decision 1 states the arbitration. *A negative test needs its positive vector proven first* — the self leg's
accepted vector (a managing landlord, link declared) before the four refusals (tenant at the unit, forged target, undeclared
link, building-located order). *A link key's type segment is what an outbound walk rebuilds the far endpoint from* — pin the
literal `lnk.workorder.<w>.reportedBy.identity.<i>`. FE: *a transport throw after a submit* — `sent`/`confirmed` on the
Resolve submit; *op names in Go comments* — `lint-app-op-descriptors`; *a new read-model column walks the app's struct pair*
(sixth sighting) — `reported_by_resident` reaches `landlordWorkOrderRow` and the pin builds from the wire struct;
*a "the person can also do it from X" claim is about X's render gate* — Resolve is offered only on an unresolved card;
*two courtesy surfaces name the same instant in different zones* — `reportedAt`/`resolvedAt` are instants, rendered
`localDateTime` everywhere. Standing checklist 1–6 walked: (1) the marker's lifetime is the order's; (2) census re-run
above; (3) revert-proof per increment; (4) nothing removed; (5) decision 1; (6) `staleUserTasks` verified against its own
rule (`lint-gap-params` clean on `row.taskKey`, the anchor).

**Scope-diff.** Every touch traces to the scope sentence. Additions the ask does not name and the mechanism requires: the
`reportedBy` link (no reporter walk exists without it; the ask's "reporter-anchored lens" is unbuildable on a stamp) and its
backfill gap (the ask's live instance predates the link); the stale-task cancel (the landlord leg's own consequence);
the Facet exclusion (the self grant's own consequence, the `ReportIssue` precedent). Declared dependency re-verified both
ways: `ea15ca76`'s queue + landlord lens are load-bearing and shipped; nothing here is load-bearing for anything unbuilt.

**Build note (2026-09-18).** Deviations from the brief, each an increment of the same mechanism: the reportedBy link is
minted AHEAD of the `.report` stamp (Refractor evaluates per CDC message and an atomic batch lands as N messages, so
stamp-before-link opened `missing_reporter` for one revision on every fresh report — a doomed backfill dispatch);
`staleWorkOrderTasks` is grounded on `resolvedBy <> assignee` (the task-path resolve's auto-complete is appended one
message after `.resolution`, so `resolvedAt <> null` alone drew a doomed `CancelTask` on the common tech path — reasoned
from the pipeline, not observed: `processor.log` holds no rejected `CancelTask` over its lifetime; the sibling
`staleUserTasks` carries the same shape) — a claimant who resolves on the STANDING path keeps their claimed task to
complete from their inbox (`CompleteTask` self grant), else it expires into `UnroutedTasks`; `reported_by_resident` is an
`OPTIONAL MATCH … residesIn … (u)` closing on the bound unit with `(r.key <> null)` (this engine has no `size()`; the far
node is constrained by `rel_traverse.go`), not a pattern comprehension; `LinkWorkOrderReporter` replies the LINK key as
`primaryKey` (the reply constraint refuses a vertex root for a link-only write); its `.report` read is `(a)` — the target
lists it in required `Reads`; `unit_address` is loftspace-domain's `.address` (`SetUnitAddress`), not location-domain's;
the two self-leg refusals share one `AuthDenied` text (no unit-vs-building oracle); `Depends` gains `orchestration-base`
(`CancelTask` by `Class: task`). Accepted bound, stated at the gap: a reporter whose identity ROOT is tombstoned keeps
`missing_reporter` open until the directOp retry budget parks that gap alone with a standing Health issue (no op
tombstones an identity root today). The landlord panel offers Resolve on the row's own `landlord_key` only (the lens also
anchors covering buildings); both new refreshes defer 800 ms behind projection and the work-orders load runs after the
applications it joins; "My reports" renders once per tenant view.

**Shipped `a8d26b2e` (merge of `6a2761ad`), CI green, live 2026-09-18.** `refresh-loftspace` (maintenance-domain 0.3.0 →
0.4.0, loftspace-domain 0.15.0 → 0.16.0, `provision-readpath`, the app cycled), `reinstall-package` edge-manifest 0.17.20;
weaver targets `staleWorkOrderTasks` / `workOrderResolvedNotices` active, `workOrderQueue` re-registered with both gaps.
The backfill's first 15 dispatches were `AuthDenied` on the capability-projection lag of the new grant (the documented
first-dispatch race) and cleared by `weaver revoke` + `enable workOrderQueue`; 19/19 legacy orders linked within a minute
after, the three resolved ones told (`.resolvedNotice` + the bridge's `.resolvedNotification {completed}`), the new
protected lens active (`read_reporter_work_orders`: 19 rows, 3 resolved, 3 told; `read_landlord_work_orders`: 18 rows, 1
by a resident). Through the app's own path (dev-login → session token → Gateway): Jordan Ellis's 09-17 report
`whDpTDRwQiJmA6PfFhdF` reads on `/api/my/work-orders` (1 row, was 0); his fresh report `vtx.workorder.CY2hf44PMgArj6JRbXyw`
lands with its link in the batch, is queued by the gap (`vtx.task.En3UTQGSgZPLAW3H5GQc`) with no backfill dispatch, and
reads on Nora Vance's console as *Reported by Jordan Ellis*; Jordan resolving it → `AuthDenied`; Nora with the manages
link undeclared → `AuthDenied`; declared → `.resolution` committed, the task `cancelled` by convergence, the notice sent
and its outcome recorded within 27 s, Jordan's card reading *resolved … — Replaced the hallway ballast · you were told*.
Rendered: Nora's Maintenance panel (19 cards, Resolve on 15 unresolved of her own) and Jordan's My reports, one tab,
closed. Native `prompt()` on Resolve was not driven in-browser; the op was proven through the Gateway.

**Review classification (two cold passes over the whole diff, 0 BLOCKING, 4 SHOULD-FIX, all fixed before merge):**
design-gap ×2 (the per-message transient of an atomic batch on `missing_reporter` and on the task-path resolve — a
`_packages.md` sighting under the every-ARM entry), implementation ×3 (a former resident labelled staff — the residence
boolean read as "ever" not "now"; both refreshes ahead of projection; the reporter label read before the applications
loaded — a `vertical-apps.md` sighting), brief-gap ×3 (six vectors added: another-unit link declared, operator naming
self, tombstoned link, identical-notes replay, reporter in a different unit, self-on-task catalog), convention ×3 (Depends,
one refusal text, the OPTIONAL-MATCH build-note line), review-over-reach ×1 (Resolve offered to a co-anchored building
staffer — gated on `landlord_key`).
