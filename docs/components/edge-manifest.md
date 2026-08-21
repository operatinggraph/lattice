# edge-manifest

**Component reference** | Audience: operators + implementers

> `edge-manifest` is a **Capability Package** (`packages/edge-manifest`), not a platform engine — it has
> no frozen interface contract of its own. Its framing of record is
> `_bmad-output/implementation-artifacts/edge-showcase-app-design.md` §3 (✅ Andrew-ratified), the entity
> lenses per `facet-entity-browse-design.md`, the staff siblings per `facet-staff-worlds-design.md` §3.3,
> the provider-hat siblings per `persona-worlds-design.md` Fire W0, the `edgeStaffPanes`/`Panes()`
> descriptor lens per `facet-discovery-restoration-design.md` §2.1, and the *Edge-manifest + personal-lens
> consumer* row of `_bmad-output/planning-artifacts/backlog/lattice.md`.
> Update this page in the same commit as the code; drift between page and code is a documentation bug.

---

## Overview

`edge-manifest` is the world manifest the Facet edge app (design §4) renders from: **fifteen Personal
Lenses** (`packages/edge-manifest/lenses.go`) re-projecting data other packages already own — identity,
orchestration-base's tasks, service-domain's templates/instances, service-location's residence graph,
wellness/clinic/café domain state, role-standing grants, maintenance work orders, and the provider-hat
archetypes — into the reserved `manifest.` key namespace, delivered per-actor over the shared
`lattice.sync.user.<actor>` SYNC transport (the `nats-subject` Personal Lens adapter, `edge-manifest
Fire 0`). It also declares **three generated read-grant producer lenses** (one per `ReadGrantDomain`), one
**plain `nats-kv` lens** (`opCatalog`, the staff-plane op-descriptor read model — see below), and one
**server pane** (a Protected/RLS descriptor — a different mechanism, see "Server panes" below). It
declares no DDLs and no permissions: every row is a read-side re-projection of state another package's DDL
already authored.

It is the **first production package** to use the `nats-subject`/Personal Lens adapter — that plumbing
shipped latent in Fire 0 (proven only by inline e2e tests, `internal/refractor/personal_lens_pl*_e2e_test.go`)
with zero real `packages/*` consumers until this one.

Fourteen of the fifteen Personal Lenses are **non-self-anchored**: each keys its rows on a vertex other
than the recipient identity (a service template, an op meta, a task, an instance, a session, a provider, a
booking, a tab, a studio, a menu item, a work order, an appointment, a pane meta). Refractor's D1 gate
(`internal/refractor/projection/personal.go` → `capabilityread.IsReadable`) drops such a row unless the
actor's unioned `cap-read.<domain>.<actor>` slices list the anchor's bare NanoID — silently, fail-closed, by
design (Contract #6 §6.14 Path B). Each such lens declares its actor→anchor reachability ONCE, as one or
more `AnchorWalk`s (`lenses.go`'s `Walks` field — `edgeEntitySessions` and `edgeCatalog` each carry two, one
per reachability path to the same anchor kind, compiled to independent branches and merged by output key,
refractor-shared-keyspace-arbitration-design.md §13), and `pkgmgr` compiles both the lens's own cypher and
the read-grant producer that grants the anchors, from that declaration.

## The lenses (row schemas)

All rows carry the reserved `manifest.` key prefix (`internal/edge/store.go`'s `ApplyUpsert`/
`ApplyDelete` carry a matching exemption from the Contract #1 key-shape gate for this prefix — a
`manifest.*` key is a **projection-row key, not a Core-KV key**, the same posture `my-tasks.*` rows
already have on the nats-kv side).

**Resident/base lenses** (`ReadGrantDomain: edgeManifest`) — reachable via the actor's own residence
`containedIn` chain, or self-anchored:

| Lens | Key | Anchors on |
|---|---|---|
| `edgeIdentity` | `manifest.me` | the actor's own identity (self-anchored) — display name, claimed status, roles, residence/workplace anchors, the `{me.<type>}` self-anchor set (leaseapp/workplace/provider/instructor/serviceprovider) |
| `edgeServices` | `manifest.svc.<tplId>` | service templates reachable via the actor's residence → `containedIn*` → `availableAt` chain |
| `edgeCatalog` | `manifest.op.<opMetaId>` | op metas reachable via a reachable service template's `permitsOperation` link; carries `viaServices`, the list of service keys that permit it — **or** via a held role's `grantedBy` permission → `forOperation` (the role-standing-grant catalog path, a second `Walk` in the `edgeManifestStaff` domain; see below) |
| `edgeTasks` | `manifest.task.<taskId>` | open tasks directly `assignedTo` the actor |
| `edgeInstances` | `manifest.inst.<instId>` | service instances `providedTo` the actor ("my orders") |
| `edgeEntitySessions` | `manifest.ent.<sessionId>` | wellness class sessions reachable via residence → the studio's `locatedAt` place (`entityType: "session"`, a `dispatch.targetType: "session"` browse target) — **or** via the actor's own bound instructor's `ledBy`-inverse sessions (the provider-hat "my classes to teach" path, a second `Walk` in the `edgeManifestProvider` domain; see below) |
| `edgeEntityProviders` | `manifest.ent.<providerId>` | clinic providers `practicesAt` a reachable location (`entityType: "provider"`) |
| `edgeEntityBookings` | `manifest.ent.<bookingId>` | wellness bookings the actor themself made (`bookedBy`, NOT residence-scoped — inherently private) |
| `edgeEntityTabs` | `manifest.ent.<tabId>` | the actor's own OPEN café tabs, via their lease's `openFor` link (inherently private) |
| `edgeEntityStudios` | `manifest.ent.<studioId>` | wellness studios at a staff actor's workplace (the `CreateSession` browse target — workplace-scoped, not residence) |
| `edgeEntityMenuItems` | `manifest.ent.<itemId>` | café menu items `servedAt` a place reachable via residence (the `Charge` `menuItemKey` picker; not itself a dispatch target) |

**Staff lenses** (`ReadGrantDomain: edgeManifestStaff`) — reachable via a `holdsRole` link:

| Lens | Key | Anchors on |
|---|---|---|
| `edgeCatalog` (2nd `Walk`) | `manifest.op.<opMetaId>` | op metas reachable via a held role's `grantedBy` permission → `forOperation` — the role-standing-grant catalog path; this domain's member is `edgeCatalog`'s second `AnchorWalk`, not a standalone lens (formerly the sibling `edgeCatalogRoles`, folded in per refractor-shared-keyspace-arbitration-design.md §13.7 build order (c) — `viaServices` is anchor-derived so both branches compute it identically, `viaRole`/`viaRoleName` are walk-owned by this branch alone; an op reachable both ways projects one merged row) |
| `edgeTasks` (2nd `Walk`) | `manifest.task.<taskId>` | open tasks `queuedFor` a role the actor holds (FR28) — carries `queuedRole`/`queuedRoleName`; this domain's member is `edgeTasks`' second `AnchorWalk`, not a standalone lens (formerly the sibling `edgeTasksQueued`, folded in per refractor-shared-keyspace-arbitration-design.md §13.7 build order (d) — `assignee` is re-derived via its own `assignedTo` OPTIONAL MATCH off the task so it stays anchor-derived, `queuedRole`/`queuedRoleName` are walk-owned by this branch alone); claiming one (`ClaimTask`) swaps its `queuedFor` for `assignedTo`, so the same row's `assignee` resolves and `queuedRole` goes null |
| `edgeStaffPanes` | `manifest.pane.<paneMetaId>` | pane meta-vertices reachable over `holdsRole` → `offeredTo` — `{paneId, title, icon, sections}`, the server-pane DESCRIPTOR (not pane rows; see "Server panes" below) |
| `edgeStaffWorkOrders` | `manifest.work.<workOrderId>` | maintenance work orders at a place the actor `worksAt` (or a place contained in it) — domain-state view, independent of whether a task was ever queued for it |

**Provider-hat lenses** (`ReadGrantDomain: edgeManifestProvider`, persona-worlds-design.md Fire W0) —
reachable via the actor's own inbound `identifiedBy` binding to a provider-archetype vertex:

| Lens | Key | Anchors on |
|---|---|---|
| `edgeProviderSchedule` | `manifest.ent.<appointmentId>` | a bound clinic provider's own appointments (`withProvider`) — "my schedule" |
| `edgeProviderQueue` | `manifest.ent.<instanceId>` | a bound service provider's own instance queue (`providedBy` → `instanceOf`) — "what runs do I need to complete" |
| `edgeEntitySessions` (2nd `Walk`) | `manifest.ent.<sessionId>` | a bound instructor's own led sessions (`ledBy`) — "my classes to teach"; this domain's member is `edgeEntitySessions`' second `AnchorWalk`, not a standalone lens (formerly the sibling `edgeInstructorSessions`, folded in per refractor-shared-keyspace-arbitration-design.md §13.7 build order (b) — same anchor kind, byte-identical RETURN, a resident who is ALSO the instructor of a session reachable both ways projects one idempotent row) |

**Generated read-grant producers** — one `actorAggregate` lens per `ReadGrantDomain`, compiled by `pkgmgr`
from the `Walk` declarations above rather than hand-written (`lenses.go`'s `ReadGrantDomains()`); without
them Refractor's D1 `readableAnchors` gate silently drops every row the corresponding lenses project:

| Lens | Key | Grants |
|---|---|---|
| `edgeManifestReadGrants` | `cap-read.edgeManifest.<actor>` (nats-kv, `capability-kv`) | every resident/base-lens anchor the actor's residence chain reaches |
| `edgeManifestStaffReadGrants` | `cap-read.edgeManifestStaff.<actor>` | every staff-lens anchor a role the actor holds reaches |
| `edgeManifestProviderReadGrants` | `cap-read.edgeManifestProvider.<actor>` | every provider-hat-lens anchor the actor's own provider/instructor/serviceprovider binding reaches |

**The staff-plane op catalog** (`opCatalog`, staff-descriptor-rendering-design.md §2.1) is this package's
one PLAIN lens — an ordinary `nats-kv` read model rather than a Personal Lens, and the only member of the
lens slice that is neither per-actor nor a cap-read producer:

| Lens | Key | Bucket | Projects |
|---|---|---|---|
| `opCatalog` | the op's `operationType` (`IntoKey`) | `op-catalog` (`OpCatalogBucket`, auto-created on lens load) | one row per op meta: the whole descriptor vocabulary `edgeCatalog` reads (`presentation.*`, `inputSchema`, `fieldDescriptions`, `dispatch.*`, `sensitive`) plus `grantedToRoles`, the canonical names of every role whose permission `forOperation`s it |

It exists because a **staff application is not an edge node**: it has no per-actor SYNC transport and
cannot read Core KV (P5), so `manifest.op.*` is unreachable to it and the op descriptors its forms need
had nowhere to come from. `cmd/loftspace-app` reads this bucket through a thin `/api/op-catalog` proxy and
renders its task-completion modal from the row — a descriptor edit plus `refresh-loftspace` changes the
form with no app rebuild. Rows carry no person data by construction (an op meta is a package-declared
descriptor), so the bucket is open and the lens declares no read-path posture.

Three properties of its cypher are load-bearing and each is mutation-tested in
`packages/edge-manifest/lens_cypher_test.go`. It carries **no `WITH` clause** — `anchorProjectionShape`
(`internal/refractor/ruleengine/full/anchor_delete.go`) refuses any query with one, which would silently
disable the anchor-tombstone Delete and leave a retired op's row describing an operation the Processor no
longer accepts (`edgeCatalogTail`, the natural copy-paste source, opens with `WITH op, role`). Its key
column is `op.data.operationType`, a **ROOT vertex field**, so it resolves read-free from the tombstoned
body — an aspect-sourced key would project fine and never retract. And the role join is an **`OPTIONAL
MATCH`**: required, an op no permission grants yields zero rows and vanishes from the catalog instead of
degrading to an empty `grantedToRoles`. A bare op meta still projects, with every descriptor column null —
the consuming renderer reads "no `inputSchema`" as *not renderable* and declines to offer the op, which a
missing row could not say. Because the lens references the `permission`/`role` labels, a permission or role
event reaches it through the unseeded whole-corpus rescan rather than an anchored seed; that is
install-frequency work, not steady-state.

Three domains rather than one: §6.14 unions every cap-read slice into the actor's effective readable set, so
a reachability path not every actor has (staff role-standing grants, provider-hat bindings) lives in its own
slice — the §6.14 blast-radius unit, so a path most actors never take neither grows nor invalidates the
base slice every actor holds. An identity with no such binding simply gets an empty slice, deleted by the
generated producer's `EmptyBehavior` + realness filter.

Vocabulary additions riding the op rows: `ceremonyMintedSecretHashField` / `ceremonyRevealTitle` /
`ceremonyRevealHelp` (all nullable) declare a MINT-AND-REVEAL ceremony — the named field carries the
sha256 of a secret the client mints, submits, and shows to a person once; a client that cannot
perform the ceremony must not offer the op at all. `dispatchVisibleWhen` (`{field, equals}`, nullable) gates
OFFERING an op against the resolved target row's state — the state-machine-pair seam (pause/resume)
that previously forced clients to branch by op name. `manifest.ent` rows (and both session-typed hat
lenses) carry `typeLabel`, the per-type display word the renderer's label ladder consumes instead of
a hardcoded client-side type map.

This page + `lenses.go` are the normative as-built row shapes (design §3.2's JSON is the semantic
reference — as-built rows flatten its nesting, per its 2026-07-16 amendment; the `vocab` stamp is not
yet projected and activates at the vocabulary freeze). See design §3.3 for
the descriptor-vocabulary fields `edgeCatalog` (both `Walk`s) read back off each op meta's optional
`.presentation`/`.inputSchema`/`.fieldDescriptions`/`.dispatch`/`.sensitive` aspects (`pkgmgr.OpMetaSpec`,
`edge-manifest Fire 1 increment 1`) — an op meta that never adopted the vocabulary still projects a row,
just with those fields null (design §3.3: "ops without descriptors still render, degraded").

## Server panes

`Panes()` (`panes.go`) declares a second, unrelated mechanism riding in the same package:
**server-pane descriptors** (`facet-discovery-restoration-design.md` §2.1). A pane names the Protected
read-model sections a staff client renders — table, projected columns, filter, ordering, dispatch target —
as DATA (`pkgmgr.PaneSpec`), so the edge client's HOST executes panes generically against the RLS-confined
Postgres read models (`read_landlord_lease_applications`, `read_clinic_appointments`, `read_visit_series`),
and a new staff workflow ships as a descriptor edit with zero app change. This is **Path A / RLS**, not the
Personal-Lens/`nats-subject` mechanism the fifteen lenses above use — `edgeStaffPanes` (in the lens table)
projects only the pane's DESCRIPTOR (id/title/icon/sections) so the client can discover and render it; the
actual pane ROWS are read separately by the host from Postgres, confined by the reader's workplace grants.
One pane ships today: `staffWorklist` (front-desk applications-to-review + today's clinic schedule +
recurring visit series), offered to the `frontOfHouse` role.

## v1 scope-downs

The `full` cypher engine has no `UNION`, no list comprehension, and no string concatenation (`+` is
numeric-only) — which bounds how many independent reachability paths a single lens can dedup into one
row set without a bespoke multi-branch `collect(DISTINCT …) + collect(DISTINCT …)` per path. Named
narrowings, each a reasonable v1 cut rather than a correctness gap in what IS built:

- **`edgeIdentity`'s `anchors`/`roles` arrays** carry no human-readable location TYPE segment (there is no
  vertex-type-from-key function beyond `nanoIdFromKey`, and no string concatenation to synthesize one from
  the key's type segment) — the renderer derives type from the key client-side.
- **Still deferred:** the open-task-`forOperation` catalog path — a task's own bound op already rides inline
  on its `edgeTasks` row, so that gap is "browse all my ops," never "complete my task."

A degenerate `collect(DISTINCT {…})` entry (e.g. `{key:null,name:null}` when an identity holds no role)
is expected, not a bug — the renderer obligation is the same one `my-tasks.*` rows already carry (design
§3.2): treat a null-keyed entry as absence and drop it client-side.

## Status

**Fire 0 + Fire 1 + Fire 2 + the `facet-entity-browse-design.md` entity lenses + the
`facet-staff-worlds-design.md` staff lenses + the `persona-worlds-design.md` Fire W0 provider-hat lenses +
the `facet-discovery-restoration-design.md` `edgeStaffPanes`/`Panes()` descriptor lens +
`staff-descriptor-rendering-design.md` Inc 1's `opCatalog` shipped.** Structural
install verified (`make verify-package-edge-manifest`) and every lens's cypher parses under the real
`ruleengine/full` engine (`packages/edge-manifest/package_test.go`). The live projection e2e — a seeded
tenant actually receiving every row kind over `lattice.sync.user.<actor>` and completing the full write
path — is proven in-browser against `cmd/facet` (`make up-facet`); see `facet-app-ux.md` §5 for the
walkthrough. `make seed-edge-demo` / `make seed-showcase` also claim the seeded tenant (`ClaimIdentity`,
submitted as the tenant itself) so `manifest.me.claimed` is true from the first hydrate, per
`facet-app-ux.md` §3.0.
