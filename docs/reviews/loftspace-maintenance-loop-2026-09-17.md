# LoftSpace — a reported problem becomes work its tenant can raise and its landlord can see (2026-09-17)

**Filed (PO, 2026-09-16, `16af6c9d`), two rows built as one fire:**

- *"A reported issue never becomes work"* ★★ S, pkg — "`ReportIssue` mints the work order and deliberately not its task
  (`package.go:12`); only the seed submits `CreateTask`. Live: 17 of 18 work orders are seed-minted with tasks; the one
  reported through the app ("Kitchen tap is dripping", 07-29) has had no task for 49 days. A `missing_task` gap on an
  unresolved, unqueued order queues `ResolveWorkOrder` to `backOfHouse` at the building."
- *"A tenant cannot report a problem with their own home, and their landlord never sees it"* ★★ M, pkg + FE —
  "`ReportIssue` grants `operator` + the staff roles behind the workplace guard (`permissions.go:31`); the app offers
  the form to staff only (`app.js:3011`); the landlord console lists no work orders. A `consumer` self grant behind the
  tenancy self probe, "Report an issue" on the tenant card, a landlord lens of orders at units they `manages`."

One fire because the second row is hollow without the first (a tenant's report that nobody is queued to fix) and both
land in `maintenance-domain` + `cmd/loftspace-app`. Winston-adjudicated: every mechanism mirrors a shipped pattern
(the `assignTask` gap, the `GiveNotice` self leg, the `landlordUnitsRead` protected lens) — except one **frozen-contract
widening** (decision 2), prepared uncommitted for Andrew per CLAUDE.md.

## Grounding

- **Live census (2026-09-17, Core KV):** 18 `vtx.workorder.*` roots, 18 `.report`, 3 `.resolution`; 17 `locatedAt` a
  unit, 1 a building; 17 carry a `lnk.task.*.scopedTo.workorder.*`; the one without is `8Du4wP7x1Nmm96zxHvP5` — the
  PO's premise holds exactly.
- **The op and its guard.** `ReportIssue` validates `location` as a live `unit|building|property`
  ([ddls.go:250-273](../../packages/maintenance-domain/ddls.go)), then `if not workplace_exempt(): require_workplace([loc])`
  ([ddls.go:530-531](../../packages/maintenance-domain/ddls.go)); `workplace_exempt()` is
  `op.authTargetValidated or actor_holds_operator(op.actor)` ([ddls.go:434](../../packages/maintenance-domain/ddls.go)).
  The script's own comment at the site says it: ReportIssue carries an op-meta, so a validated target makes the
  exemption reachable — "add a resource bind here before" any validated path exists. A `Scope: self` grant IS a
  validated path (`authTargetValidated = true`, target = the actor), so the self leg must bind the actor to the
  reported unit or it exempts every tenant from every location check.
- **The self-leg precedent.** `GiveNotice`: `AuthContext: "self"`, the actor-keyed deterministic link declared as an
  OptionalRead (`lnk.leaseapp.{id}.applicationFor.identity.{actor:id}`,
  [permissions.go:609-649](../../packages/lease-signing/permissions.go)); the script binds
  `op.authTargetValidated and op.authContextTarget == op.actor` then reads the link as `# read-posture: (d)`
  ([scripts.go:1698-1705](../../packages/lease-signing/scripts.go)); the FE hand-builds the submit with
  `reads`/`optionalReads` per hat and `{ authContext: { target: state.applicant } }`
  ([app.js:2600-2640](../../cmd/loftspace-app/web/app.js)). The residence spine is `lnk.identity.<id>.residesIn.unit.<u>`,
  wired at approval since `a8299552` — the tenancy self probe the PO names.
- **Tasks.** `CreateTask{queue: vtx.role.<id>, forOperation: vtx.meta.<opMeta>, scopedTo, expiresAt (required), taskId?}`
  ([orchestration-base/ddls.go:110-118](../../packages/orchestration-base/ddls.go)); a queue is ROLE-only —
  `lnk.task.<id>.queuedFor.role.<roleId>` ([ddls.go:39](../../packages/orchestration-base/ddls.go)) — "at the building" is
  the claimant's own confinement (the roster + `ResolveWorkOrder`'s task-path bind), exactly the shape the seed mints
  (`queue: backOfHouse`, [seed-showcase.go:493-503](../../scripts/seed-showcase.go)). `CreateTask` is `operator`-granted
  ([permissions.go:50](../../packages/orchestration-base/permissions.go)) — Weaver's service actor holds it. Task root is
  `{status ∈ open|complete|cancelled, expiresAt}`; expiry is not a transition — an open role-queued task past its
  `expiresAt` is `orchestration-base`'s `unroutedTasks` → `surface UnroutedTasks` (Contract #10 weaver §action table),
  operator-intervention by design.
- **Weaver's `assignTask`.** Resolves `operation` → the op-meta key, `assignee`, `target`; submits `CreateTask` with a
  claimId-seeded stable `taskId` (re-dispatch collapses on the existing task), `expiresAt = now + 30 d`, reads
  `[assignee, forOperation, target]`, optionalReads `[stable task key, assignee.availability]`
  ([strategist.go:262-310](../../internal/weaver/strategist.go)). No `queue` arm; `directOp CreateTask` cannot carry a
  time-derived `expiresAt` nor the stable id. Install validation requires `Assignee`
  ([orchestrationguard.go:841-845](../../internal/pkgmgr/orchestrationguard.go)); the materialized body and the engine's
  registry carry `assignee` ([build.go:850](../../internal/pkgmgr/build.go), [registry.go:84](../../internal/weaver/registry.go));
  the dispatch-identity fields list it ([registry.go:897](../../internal/weaver/registry.go)). Contract #10 weaver pins
  `assignTask | { operation, assignee, target }` ([10-orchestration-weaver.md:173](../contracts/10-orchestration-weaver.md)).
- **The landlord read path.** `landlordUnitsRead` (loftspace-domain): protected Postgres, `DiffRetraction`, IntoKey
  `[unit_id, landlord_id]`, `MATCH (u:unit)<-[:manages]-(landlord:identity)`, `authz_anchors = [landlord] + covering
  buildings` ([lenses.go:86-202](../../packages/loftspace-domain/lenses.go)); the app reads it through the RLS session
  (`queryLandlordApplications`, [unit_applications.go:235-284](../../cmd/loftspace-app/unit_applications.go)). A lens walks
  another package's types without a `Depends` edge (lease-signing's `landlordLeaseApplicationsRead` walks `manages`,
  `object`, `service`). The open-task fragment is orchestration-base's own `WHERE t.data.status = 'open'`
  ([lenses.go:234](../../packages/orchestration-base/lenses.go)).
- **Install chains.** `install-maintenance` (orchestration-base → location-domain → maintenance-domain) is reached only
  from `install-showcase-domains`; `install-loftspace` / `refresh-loftspace` install neither maintenance-domain nor
  orchestration-base's task lenses beyond what `refresh-loftspace` already force-installs (`orchestration-base` is its
  first line, Makefile 1756). A new protected lens pauses until `provision-readpath`; `refresh-loftspace` chains it.

## Decisions (Winston)

1. **`workOrderQueue` lens + `workOrderQueueTarget` in `maintenance-domain`.** The package keeps its split at the OP —
   `ReportIssue` still mints no task — and gains the queueing POLICY as a declarative convergence target, the way
   `tenancyEnd` relists a unit. Anchor `(wo:workorder)`; `OPTIONAL MATCH (t:task)-[:scopedTo]->(wo)`; columns
   `entityKey`, `resolvedAt = wo.resolution.data.resolvedAt`, `openTaskCount = count(DISTINCT CASE WHEN t.data.status =
   'open' THEN t.key ELSE null END)` (the `otherLiveTenancyCount` CASE shape over orchestration-base's own status
   fragment), `missing_task = (resolvedAt = null) AND (openTaskCount = 0)`, `violating = missing_task`. A cancelled task
   re-opens the gap (re-queued); an expired-but-open one does not (that is `unroutedTasks`' surfaced issue, not a
   re-queue — decided with the platform, not against it); a resolution closes it whoever resolved. Gap:
   `missing_task: {Action: assignTask, Operation: ResolveWorkOrder, Queue: "vtx.role." + pkgmgr.RoleID("identity-domain",
   "backOfHouse"), Target: row.entityKey}` — the literal role key the seed uses. Package `0.2.12 → 0.3.0`.
2. **Weaver `assignTask` gains a `Queue` arm** (`internal/pkgmgr` + `internal/weaver`): `GapActionSpec.Queue` (and the
   materialized/registry/planner mirrors), install validation "exactly one of `Assignee` / `Queue`", the plan reads
   `[queue, forOperation, target]`, optionalReads `[stable task key]` only (no `.availability` — CreateTask reads it on
   the assignee branch alone), payload `queue` in place of `assignee`; `queue` joins the dispatch-identity string
   fields. `CreateTask`'s documented alternative endpoint, nothing new at the op. **Contract #10 weaver's action table
   row becomes `assignTask | { operation, assignee | queue, target }` — edited in `main`, UNCOMMITTED, for Andrew**
   (the build is otherwise complete; the runtime accepts the arm the moment it merges, so the clause is an observable
   promise held out of the tree until ratified). Augur's `proposedOp` re-validation keeps `assignee`-only (a proposal
   is not a playbook; widening it is a separate decision).
3. **`consumer` self grant on `ReportIssue`, bound to residence.** `Permissions()` adds `{ReportIssue, Scope: "self",
   GrantsTo: [consumer]}`. The script's `ReportIssue` branch: `if op.authTargetValidated:` — refuse `AuthDenied` unless
   `op.authContextTarget == op.actor`; refuse `NotResident` unless `ltype == "unit"` and
   `lnk.identity.<actor>.residesIn.unit.<lid>` is alive (`# read-posture: (d)` — the tenant leg's declared OptionalRead);
   `elif not actor_holds_operator(op.actor): enforce_workplace([loc], …)`. The self leg binds the validated target
   to the actor AND the actor to the unit — the "resource bind" the script's own comment demanded; a staff self
   submission is refused (staff submit standing). The op-meta stays `standing` (`{me.workplace}` — Facet's staff form
   is untouched); the tenant leg's dispatcher is loftspace-app's hand-built submit, which declares the link.
   `.report.reportedBy` already records the actor; nothing more is stored.
4. **"Report an issue" on the tenant card**, mirroring `renderGiveNoticeControl` + `submitGiveNotice`: offered while
   `row.landlordApproved && !row.tenancyEndedAt` (a residing tenant; the op's `NotResident` is the truth if the spine
   has not converged), summary + priority, `submitOp({operationType: ReportIssue, class: workOrder, reads:
   [row.unitKey], optionalReads: ["lnk.identity.<me>.residesIn.unit.<unitId>"], payload: {summary, priority, location:
   row.unitKey}}, {authContext: {target: state.applicant}})`, the `sent`/`confirmed` throw staging, refusal-courtesy lines
   for every ReportIssue code at the new site. No tenant-side list of past reports (the ask names the button; a
   self-anchored report lens is a PO row, not this fire's).
5. **`landlordWorkOrdersRead` in `loftspace-domain`** beside `landlordUnitsRead` (same protected/`DiffRetraction`/anchor
   shape, IntoKey `[work_order_id, landlord_id]`): `MATCH (wo:workorder)-[:locatedAt]->(u:unit)<-[:manages]-(landlord:identity)
   OPTIONAL MATCH (t:task)-[:scopedTo]->(wo)`; columns `work_order_key, landlord_key, unit_key, unit_address, summary,
   priority, reported_at, reported_by, resolved_at, resolution_notes, open_task_count`; `authz_anchors = [landlord] +
   covering buildings`. Units only, as filed (the one building-located order is staff-reported and not a landlord's).
   Handler `GET /api/landlord/work-orders` mirrors `queryLandlordApplications`; the landlord console gains a
   **Maintenance** panel, newest first, each row `unit address · summary · priority · state` where the state is a goja-
   pinned `workOrderState(row)` ∈ `resolved` (resolved_at) / `queued` (open_task_count > 0) / `unqueued`. Package
   `loftspace-domain` version bump.
6. **The seed stops minting the task** (`seed-showcase.go:488-505`): with the gap live the seed's own `CreateTask` races
   the Weaver for the same order (two open tasks); the seed reports the issue and the gap queues it — the print names
   the mechanism. The seed's `alive(taskKey)` idempotence guard goes with it.
7. **`maintenance-domain` joins the LoftSpace chains** — `install-loftspace` and `refresh-loftspace` install it after
   `location-domain` (its only `Depends`), the residence-spine decision 5 rule: a chain that ships a client of an op
   installs the package that owns the op. `refresh-loftspace` already chains `provision-readpath` for the new protected
   lens.
8. **Non-goals.** No re-queue of an expired-open task (decision 1); no building-located orders on the landlord panel;
   no tenant list of reports; no change to `ResolveWorkOrder`'s grants; `proposedOp` stays assignee-only; Facet's
   staff `ReportIssue` form unchanged.

## Build

**Scope sentence.** A `missing_task` gap queues every unresolved, unqueued work order to `backOfHouse`; a tenant reports
an issue at the unit they reside in from their own card; a landlord sees the work orders at the units they manage,
with whether each is queued or resolved. Green: `8Du4wP7x…` carries an open task live; Jordan Ellis's report lands as a
work order at his unit and appears on Nora Vance's console.

**Touch-list (verified 2026-09-17).**
- Weaver arm: `internal/pkgmgr/definition.go:425-432,596-606` (`Queue` on `GapActionSpec` + the planner entry) ·
  `capabilitymaterializer.go:188,745` · `build.go:850,941` · `plannerfields.go:156` · `orchestrationguard.go:824-848`
  (+ its test) · `internal/weaver/registry.go:84,225,284,897` · `strategist.go:262-310` (+ `strategist_test.go`) ·
  `augur_dispatch.go:276-308` (comment only: assignee-only stays) · the `TestCreateTaskReads_MatchDDLScript` pin.
- Package: `packages/maintenance-domain/{ddls.go:426-531,package.go,permissions.go,manifest.yaml}` + new
  `lenses.go` / `targets.go` + tests (`lens_test.go` cypher pins: gap true on an unresolved untasked order, false with an
  open task, false with a resolution, true again with only a cancelled task; a self-leg integration vector: resident
  accepted, non-resident `NotResident`, building `NotResident`, forged target `AuthDenied`, undeclared link fails closed;
  `package_test.go` grant pins).
- LoftSpace: `packages/loftspace-domain/{lenses.go,package.go,manifest.yaml}` + `landlord_work_orders_lens_test.go` ·
  `cmd/loftspace-app/` new `work_orders.go` handler (+ route) · `web/app.js` (tenant control beside
  `renderGiveNoticeControl` ~2653; landlord panel beside `renderLandlordRLSUnits` ~4710) · a goja pin test ·
  `scripts/seed-showcase.go:488-505` · `Makefile` `install-loftspace` / `refresh-loftspace`.
- Contract (uncommitted): `docs/contracts/10-orchestration-weaver.md:173`.

**Precedents.** Gap + lens: `lease-signing/tenancy_end_lenses.go:170-201` (CASE count), `tenancy_end_targets.go:44-75`
(target struct), `targets.go:151` (`assignTask`). Self leg: `lease-signing/scripts.go:1698-1705` + `permissions.go:609-649`
+ `app.js:2600-2640`. Landlord lens + handler: `loftspace-domain/lenses.go:86-202` + `landlord_units_lens_test.go` +
`cmd/loftspace-app/unit_applications.go:235-284`. Panel render: `app.js:4670-4752`.

**Increments.** (1) Weaver `Queue` arm + pkgmgr validation + tests (`go test ./internal/weaver/ ./internal/pkgmgr/`,
`make test-lease-convergence`). (2) maintenance-domain: lens + target + self grant + script + tests; version 0.3.0
(`go test ./packages/maintenance-domain/ ./internal/refractor/ -run 'Corpus|Census|.'`). (3) loftspace-domain lens +
tests; handler + FE + goja pin; seed; Makefile chains. Then the gate loop (`go build ./...`, `make vet`, `golangci-lint`,
every `scripts/lint-*.go` under `STRICT=1`, `gofmt -l scripts/`, `DIFF_BASE` package-version lint), the live rollout
(rebuild `bin/lattice-pkg` + `bin/lattice` first — install validation carries the arm — cycle `weaver` via
`pkill -x weaver && make orchestration`, `reinstall-package` maintenance-domain, `refresh-loftspace`, `lens health` on the
new protected lens), and the live proof.

**Gotchas (dossier, part 5).** *A dispatch declaration must name what the runtime binds* — `row.entityKey` is the
anchor's own key. *For every ARM of the op pin the gap FALSE over the state it leaves* — open task → false;
resolution → false; cancelled-only → true. *A consumer's exclusion is grounded in the predicate that enforces it* —
`status = 'open'` is orchestration-base's fragment verbatim. *The "leg" of a guard is every op that writes the guarded
value* — the self leg lists every field the write stamps (none new; `reportedBy` is the actor on both legs). *A mirror
that drops a precedent's branch drops its invariant* — the self leg keeps the forged-target refusal
(`authcontext_target_forgery_test.go`'s shape) and the undeclared-link fail-closed vector. FE: *a staff-leg descriptor
context that passes no `me`* — the tenant submit declares the actor-keyed link per hat; *transport throw after a submit* —
`sent`/`confirmed` staging; *op names in Go comments* — run `lint-app-op-descriptors`; *markup escaping* — summary and
notes are tenant/staff-typed strings through `esc()`. Standing checklist 1–6 walked; the seed's dropped `CreateTask`
is the "two writers" item (checklist 5) resolved by removing the second writer.

**Scope-diff.** Every touch traces to the scope sentence. The Weaver `Queue` arm is the one addition the ask does not
name and is the mechanism the ask's `CreateTask(queue: backOfHouse)` requires (grounding: no arm, no `directOp` route);
the seed change removes a second writer the gap would race. Declared dependency re-verified both ways: the residence
spine (`a8299552`) is load-bearing for decision 3 and shipped; nothing here is load-bearing for anything unbuilt.
