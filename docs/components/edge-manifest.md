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

`edge-manifest` is the world manifest the Facet edge app (design §4) renders from: **eighteen Personal
Lenses** (`packages/edge-manifest/lenses.go`) re-projecting data other packages already own — identity,
orchestration-base's tasks, service-domain's templates/instances, service-location's residence graph,
wellness/clinic/café domain state, role-standing grants, maintenance work orders, and the provider-hat
archetypes — into the reserved `manifest.` key namespace, delivered per-actor over the shared
`lattice.sync.user.<actor>` SYNC transport (the `nats-subject` Personal Lens adapter, `edge-manifest
Fire 0`). It also declares **three generated read-grant producer lenses** (one per `ReadGrantDomain`) and
one **server pane** (a Protected/RLS descriptor — a different mechanism, see "Server panes" below). It
declares no DDLs and no permissions: every row is a read-side re-projection of state another package's DDL
already authored.

It is the **first production package** to use the `nats-subject`/Personal Lens adapter — that plumbing
shipped latent in Fire 0 (proven only by inline e2e tests, `internal/refractor/personal_lens_pl*_e2e_test.go`)
with zero real `packages/*` consumers until this one.

Seventeen of the eighteen Personal Lenses are **non-self-anchored**: each keys its rows on a vertex other
than the recipient identity (a service template, an op meta, a task, an instance, a session, a provider, a
booking, a tab, a studio, a menu item, a work order, an appointment, a pane meta). Refractor's D1 gate
(`internal/refractor/projection/personal.go` → `capabilityread.IsReadable`) drops such a row unless the
actor's unioned `cap-read.<domain>.<actor>` slices list the anchor's bare NanoID — silently, fail-closed, by
design (Contract #6 §6.14 Path B). Each such lens declares its actor→anchor reachability ONCE, as an
`AnchorWalk` (`lenses.go`'s `Walk` field), and `pkgmgr` compiles both the lens's own cypher and the
read-grant producer that grants the anchors, from that one declaration.

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
| `edgeCatalog` | `manifest.op.<opMetaId>` | op metas reachable via a reachable service template's `permitsOperation` link; carries `viaServices`, the list of service keys that permit it |
| `edgeTasks` | `manifest.task.<taskId>` | open tasks directly `assignedTo` the actor |
| `edgeInstances` | `manifest.inst.<instId>` | service instances `providedTo` the actor ("my orders") |
| `edgeEntitySessions` | `manifest.ent.<sessionId>` | wellness class sessions reachable via residence → the studio's `locatedAt` place (`entityType: "session"`, a `dispatch.targetType: "session"` browse target) |
| `edgeEntityProviders` | `manifest.ent.<providerId>` | clinic providers `practicesAt` a reachable location (`entityType: "provider"`) |
| `edgeEntityBookings` | `manifest.ent.<bookingId>` | wellness bookings the actor themself made (`bookedBy`, NOT residence-scoped — inherently private) |
| `edgeEntityTabs` | `manifest.ent.<tabId>` | the actor's own OPEN café tabs, via their lease's `openFor` link (inherently private) |
| `edgeEntityStudios` | `manifest.ent.<studioId>` | wellness studios at a staff actor's workplace (the `CreateSession` browse target — workplace-scoped, not residence) |
| `edgeEntityMenuItems` | `manifest.ent.<itemId>` | café menu items `servedAt` a place reachable via residence (the `Charge` `menuItemKey` picker; not itself a dispatch target) |

**Staff lenses** (`ReadGrantDomain: edgeManifestStaff`) — reachable via a `holdsRole` link:

| Lens | Key | Anchors on |
|---|---|---|
| `edgeCatalogRoles` | `manifest.op.<opMetaId>` | op metas reachable via a held role's `grantedBy` permission → `forOperation` — the role-standing-grant catalog path (same row shape as `edgeCatalog`; an op reachable both ways projects an idempotent duplicate) |
| `edgeTasksQueued` | `manifest.task.<taskId>` | open tasks `queuedFor` a role the actor holds (FR28) — carries `queuedRole`/`queuedRoleName`; claiming one (`ClaimTask`) moves it to `edgeTasks` |
| `edgeStaffPanes` | `manifest.pane.<paneMetaId>` | pane meta-vertices reachable over `holdsRole` → `offeredTo` — `{paneId, title, icon, sections}`, the server-pane DESCRIPTOR (not pane rows; see "Server panes" below) |
| `edgeStaffWorkOrders` | `manifest.work.<workOrderId>` | maintenance work orders at a place the actor `worksAt` (or a place contained in it) — domain-state view, independent of whether a task was ever queued for it |

**Provider-hat lenses** (`ReadGrantDomain: edgeManifestProvider`, persona-worlds-design.md Fire W0) —
reachable via the actor's own inbound `identifiedBy` binding to a provider-archetype vertex:

| Lens | Key | Anchors on |
|---|---|---|
| `edgeProviderSchedule` | `manifest.ent.<appointmentId>` | a bound clinic provider's own appointments (`withProvider`) — "my schedule" |
| `edgeProviderQueue` | `manifest.ent.<instanceId>` | a bound service provider's own instance queue (`providedBy` → `instanceOf`) — "what runs do I need to complete" |
| `edgeInstructorSessions` | `manifest.ent.<sessionId>` | a bound instructor's own led sessions (`ledBy`) — "my classes to teach" |

**Generated read-grant producers** — one `actorAggregate` lens per `ReadGrantDomain`, compiled by `pkgmgr`
from the `Walk` declarations above rather than hand-written (`lenses.go`'s `ReadGrantDomains()`); without
them Refractor's D1 `readableAnchors` gate silently drops every row the corresponding lenses project:

| Lens | Key | Grants |
|---|---|---|
| `edgeManifestReadGrants` | `cap-read.edgeManifest.<actor>` (nats-kv, `capability-kv`) | every resident/base-lens anchor the actor's residence chain reaches |
| `edgeManifestStaffReadGrants` | `cap-read.edgeManifestStaff.<actor>` | every staff-lens anchor a role the actor holds reaches |
| `edgeManifestProviderReadGrants` | `cap-read.edgeManifestProvider.<actor>` | every provider-hat-lens anchor the actor's own provider/instructor/serviceprovider binding reaches |

Three domains rather than one: §6.14 unions every cap-read slice into the actor's effective readable set, so
a reachability path not every actor has (staff role-standing grants, provider-hat bindings) lives in its own
slice and its branches never join the base producer's cross-branch fan-out. An identity with no such binding
simply gets an empty slice, deleted by the generated producer's `EmptyBehavior` + realness filter.

Vocabulary additions riding the op rows: `dispatchVisibleWhen` (`{field, equals}`, nullable) gates
OFFERING an op against the resolved target row's state — the state-machine-pair seam (pause/resume)
that previously forced clients to branch by op name. `manifest.ent` rows (and both session-typed hat
lenses) carry `typeLabel`, the per-type display word the renderer's label ladder consumes instead of
a hardcoded client-side type map.

This page + `lenses.go` are the normative as-built row shapes (design §3.2's JSON is the semantic
reference — as-built rows flatten its nesting, per its 2026-07-16 amendment; the `vocab` stamp is not
yet projected and activates at the vocabulary freeze). See design §3.3 for
the descriptor-vocabulary fields `edgeCatalog`/`edgeCatalogRoles` read back off each op meta's optional
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
Personal-Lens/`nats-subject` mechanism the eighteen lenses above use — `edgeStaffPanes` (in the lens table)
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
- **`edgeCatalog`/`edgeTasks`** cover only the service-`permitsOperation` / direct-`assignedTo` reachability
  paths; their role-derived counterparts are **shipped as sibling lenses** (`edgeCatalogRoles`,
  `edgeTasksQueued`) rather than folded into the same cypher — this engine has no UNION, so a second
  independent path in one query would cross-product it. **Still deferred:** the open-task-`forOperation`
  catalog path — a task's own bound op already rides inline on its `edgeTasks`/`edgeTasksQueued` row, so
  that gap is "browse all my ops," never "complete my task."

A degenerate `collect(DISTINCT {…})` entry (e.g. `{key:null,name:null}` when an identity holds no role)
is expected, not a bug — the renderer obligation is the same one `my-tasks.*` rows already carry (design
§3.2): treat a null-keyed entry as absence and drop it client-side.

## Status

**Fire 0 + Fire 1 + Fire 2 + the `facet-entity-browse-design.md` entity lenses + the
`facet-staff-worlds-design.md` staff lenses + the `persona-worlds-design.md` Fire W0 provider-hat lenses +
the `facet-discovery-restoration-design.md` `edgeStaffPanes`/`Panes()` descriptor lens shipped.** Structural
install verified (`make verify-package-edge-manifest`) and every lens's cypher parses under the real
`ruleengine/full` engine (`packages/edge-manifest/package_test.go`). The live projection e2e — a seeded
tenant actually receiving every row kind over `lattice.sync.user.<actor>` and completing the full write
path — is proven in-browser against `cmd/facet` (`make up-facet`); see `facet-app-ux.md` §5 for the
walkthrough. `make seed-edge-demo` / `make seed-showcase` also claim the seeded tenant (`ClaimIdentity`,
submitted as the tenant itself) so `manifest.me.claimed` is true from the first hydrate, per
`facet-app-ux.md` §3.0.
