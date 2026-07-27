# Facet staff worlds — front-desk / operations personas on the same binary

**Status: ✅ RATIFIED (Andrew, 2026-07-19) — "ratified with your recommendations."** FORK-S1 stands at B for v1, **with A ratified as a fast follow** — the named consumer arrived at ratification: *the maintenance tech down in the basement without internet* (now fire **F5**, §6). FORK-S2's §8 rewrite is applied to edge-showcase-app-design.md in the same commit. Build runs through the Verticals lane. Author: Winston (Designer fire, 2026-07-19).
**Board row:** [verticals.md](../planning-artifacts/backlog/verticals.md) — *Facet for staff — front-desk/operations worlds* (★★★ / XL), filed by Andrew's PO pass 2026-07-18 (`2847eb32`).
**Extends:** [edge-showcase-app-design.md](edge-showcase-app-design.md) (✅ ratified; Fires 0–3+5 shipped) — and **lifts one of its §8 non-goals** (see FORK-S2). Sibling precedents: [facet-entity-browse-design.md](facet-entity-browse-design.md) (✅ ratified — sibling-lens pattern, F4 read-grant discipline), [display-name-convention-design.md](display-name-convention-design.md) (✅ ratified — D3 draws exactly the PII boundary this design builds on), mixed-use-composition-design.md (✅ done — the shipped front-desk read models this design reuses).

---

## For Andrew (one-look ratification block)

**What it does (two lines).** A staff member signs into the *same* Facet binary and their world composes from `worksAt`/`holdsRole` the way a resident's composes from `residesIn`: a staff catalog (the ops their role grants, with forms), a claimable role-queued task inbox, and the front-desk/operations worklists the public site's #demo personas promise — with resident names arriving only through the RLS-confined Protected plane, never over the SYNC stream. No second app; the two canned demo personas become real.

**The one thing to understand before ratifying.** This design adds no new planes: the manifest half *un-defers two scope-downs the shipped code already names as deferred* (edgeCatalog's role-standing-grant path, edgeTasks' FR28 role-queued path), and the worklist half reads *already-shipped* staff read models (front-desk-\* KV, `landlordLeaseApplicationsRead`, `clinicAppointmentsRead`, portfolio-pulse) through the already-shipped `cmd/facet` Protected-read pattern (`credentials.go`). The genuinely new machinery is the staff spine (`worksAt`) and a **workplace-anchored read-grant producer** that finally gives staff a read posture narrower than root.

**Frozen-contract change: NONE.** The descriptor-vocabulary additions (`authContext:"standing"`, `queuedRole`) ride `docs/components/edge-manifest.md`, still a build-to spec (FORK-1's freeze trigger has not fired). The new grant slices use Contract #6 §6.14's own extension mechanism (per-producer `grant_source`, union across `cap-read.*` slices) — no shape change.

**FORK-S1 — how worklists reach the staff client. DECIDED (Andrew, 2026-07-19): B for v1, A as a ratified fast follow (F5).**
- **B. Protected pane (v1, F3):** `cmd/facet` serves worklist cards server-side from the shipped staff read models, RLS-confined as the signed-in staff actor — the exact shipped `credentials.go` pattern, and the only path on which resident *names* are correct (SecureColumns decrypt-at-projection for authorized readers). The mirror keeps the world *structure* (who am I, my catalog, my tasks); Protected context is a server-mediated pane in the same session.
- **A. Manifest-plane worklists (fast follow, F5):** per-row staff worklists as personal-lens SYNC rows — offline-capable, pure-mirror. The dead-scaffolding objection dissolved at ratification: the consumer is **the offline maintenance tech** (basement, no connectivity), and for that persona the standing constraint costs nothing — rows stay **nameless by D3** (identity PII is sealed at rest; EDGE.4 decrypt is self-only), and maintenance work is unit/equipment-scoped, not resident-PII-scoped. Note F2 already delivers the load-bearing offline half: role-queued work rides the mirror, and the shipped edge intent queue lets a disconnected device queue `ClaimTask`/completions and drain on reconnect. F5's delta is the per-row domain worklist lenses + their Path B read-grant fan-out + the offline-claim UX.
- The PII boundary between the two halves is D3's ratified split, not a new invention — it holds in both fires.

**FORK-S2 — the §8 non-goal lift. DECIDED (Andrew, 2026-07-19): lifted, bounded.** Role-derived, workplace-scoped, lens-projected worklists join Facet; arbitrary graph browsing and platform administration stay Loupe's (the demo's fourth persona remains "on the live stack this is Loupe"). edge-showcase-app-design.md §8 is **rewritten in place** (banner-rewrites-body rule) in the ratification commit; entity-browse F3's "not a graph browser" adjudication still grounds on the retained wording.

**Also for your attention (not forks):** F3 puts a *building* anchor column into two existing Protected tables' `authz_anchors` (additive; existing landlord/patient/self anchors unchanged), and F1 seeds a `frontDesk` role that is deliberately **not** granted the kernel `WildcardAnchor` — the first staff posture narrower than root-equivalent `operator`.

---

## 1. Problem + intent

The public site's #demo (`lattice-site/website/assets/js/demo.js`) sells **four** personas over one graph: resident (Maya), **Front desk** ("Applications to review · APP-19 · Maya → Unit 204", "Today's schedule · 6 appointments · Dr. Okafor"), **Operations** ("Renewals due", "Vacancy", "Provider utilization · aggregates, not private records"), operator (explicitly Loupe). The resident persona is real — Facet ships it. The operator persona is real — Loupe ships it. The two *staff* personas are canned: no real client composes a staff world, because every staff surface today is a bespoke vertical FE with a hardcoded world (the exact posture Facet's §1 thesis exists to end).

Andrew's PO pass (2026-07-18) asked: can Facet serve them? This design answers **yes, same binary** — a staff world is a *derived* world like any other; only the derivation spine differs (`worksAt`/`holdsRole` instead of `residesIn`). The renderer is already identity-generic (proven twice: the PWA and the SwiftUI spike render whatever manifest arrives).

**Adjudication note (why this went to the Designer, not back to the PO):** the demand side is done — Andrew filed the row himself, the demo cards are the product spec, and the staff jobs are already embodied in four shipped staff FEs the Vertical PO has exercised repeatedly. What was missing is architecture: the spine, the catalog transport, the cross-identity read posture. Two *ratified* docs already point here — display-name-convention-design.md §4 names "staff worlds" as the initiative that owns the class-3 "others" read spine, and edge-showcase-app-design.md §8 holds the non-goal only this design can lift.

## 2. Grounding ledger (what exists, verified in code this fire)

| Piece | State | Staff-worlds use |
|---|---|---|
| `worksAt` | **does not exist** (zero hits in `packages/ internal/ cmd/`) | the staff spine — new link, §3.1 |
| Staff roles | **exactly one: `operator`**, and it is root-equivalent — every vertical staff grant is `GrantsTo:["operator"]` scope=any, and the kernel grants it the platform write set (`internal/bootstrap/lenses.go:126`) **and** the all-access read `WildcardAnchor` (`lenses.go:339`) | today's "staff" = root; §3.2 mints the first narrow staff role |
| Non-bootstrap roles | shipped — identity-domain's `consumer` role is package-owned, minted deterministically (`pkgmgr.RoleID`), referenced by other packages via `GrantsTo:["consumer"]` | `frontDesk` mirrors it exactly |
| FR28 role-queue | shipped — `lnk.task.<id>.queuedFor.role.<id>`, exactly-one-of assignedTo/queuedFor, `ClaimTask` pull-assignment with atomic queuedFor→assignedTo swap (`orchestration-base/ddls.go:38-96`); the exact walk `(identity)-[:holdsRole]->(role)<-[:queuedFor]-(task)` already ships **verbatim** in the my-tasks aggregate cypher (`mytasks_queued_role_cypher_test.go`) | the staff inbox; only the **SYNC-plane projection** is missing — edgeTasks' doc comment names role-queued tasks a deferred scope-down |
| edgeCatalog role path | **named deferred scope-down** (`edge-manifest/lenses.go:62-66`): only the service-`permitsOperation` path ships | §3.4 un-defers it |
| rbac grant walk | shipped — `identity -holdsRole-> role <-grantedBy- perm` → `cap.roles.<actor>` `{operationType, scope}` (`rbac-domain/lenses.go:76`) | the write-authority the staff catalog must *mirror honestly* |
| Descriptor vocabulary | shipped, unfrozen (`OpDispatchSpec`, `internal/pkgmgr/definition.go:494`: authContext `"self"|"service"|"task"`); operator-scope ops deliberately skipped descriptor adoption (design §7.4) | staff ops need descriptors + a `"standing"` authContext value |
| Staff read models | shipped — front-desk-\* NATS-KV buckets (front-desk pkg: bookings / lease details / clinic-visit existence, keyed `leaseAppKey`, PII-narrowed by construction), `cafeTabSettlement`, `landlordLeaseApplicationsRead` (Postgres RLS; `applicant_name` SecureColumn decrypt-at-projection), `clinicAppointmentsRead` (`patient_name` per row), `clinicPatientsRead` (wildcard-only: empty `authz_anchors`), portfolio-pulse occupancy + attach-rate | FORK-S1 B reads these; nothing new is projected for v1 worklists except anchors (§3.5) |
| `cmd/facet` Protected reads | shipped — `credentials.go`: `FACET_PG_DSN`, NON-superuser SELECT-only role, RLS confines rows to the caller | the pane's transport precedent |
| D1 read plane | shipped — Path B `cap-read.<domain>.<actor>` KV slices (edgeManifestReadGrants), Path A Postgres `actor_read_grants` GrantTable + RLS set-membership; per-producer `grant_source`; effective set = union across slices (Contract #6 §6.14) | both halves extended, no shape change |
| Read-grant anchors are **global tokens** | `authz_anchors ∩ granted anchors`, table-blind (`internal/refractor/adapter/rls.go`) | **the trap §3.5 designs around** — granting staff a resident's identity-NanoID would open that resident's rows in *every* table (credentials included). Workplace anchoring exists to avoid exactly this |
| D3 PII split | ratified — self-name decrypts locally (EDGE.4, self-only session key); **others' names = Protected-lens territory**; never plaintext PII in a lens row | the reason FORK-S1 A cannot carry names, and B can |
| Renderer + engine limits | identity-generic renderer (two renderers proven); cypher engine: no UNION → sibling lens per path (entity-browse F2), `<> null` idiom, pattern-comprehension in RETURN | staff lenses are siblings, not widened MATCHes |

## 3. The shape

### 3.1 The staff spine: `identity worksAt location`

New link, owned by **service-location** beside `residesIn` (same family, same file, same op pattern): `WireWorksAt` / `UnwireWorksAt`, target validated `class=location`, cardinality multiple (one person, two buildings), link key `lnk.identity.<id>.worksAt.location.<id>` — sentence-reads "identity worksAt location", direction mirrors `residesIn` (identity source). It is a **plain topology link**: it feeds lens reachability and (in F3/F4) grant derivation, and is deliberately *not* wired into `cap.svc`'s availability join — `availableAt`/`residesIn` stay the only authorization-bearing inputs there (entity-browse F1 held the same line).

Not `location-domain` (owns location vertices, no identity relations), not a new package (a one-link package is ceremony), not rbac (employment is topology, not authority — a role says *what* you may do, `worksAt` says *where* your world is).

### 3.2 Staff roles: off root, fail-closed

> **Build-time correction (F1, Winston, 2026-07-19 — no new role is minted).** §2's ledger row "Staff roles: **exactly one: `operator`**" is wrong on both halves, and the fire found it before minting anything.
>
> Three staff role **vertices** already ship, declared by identity-domain beside `consumer` — **`frontOfHouse`** ("Front-of-house staff with visibility into resident-facing operations"), `backOfHouse`, and `identityProvisioner` (`packages/identity-domain/package.go`). And `frontOfHouse` is not an inert vertex: identity-domain **already grants it three ops** — `CreateUnclaimedIdentity`, `RotateClaimKey`, `RecordIdentityPII` (`packages/identity-domain/permissions.go`), the walk-in-registration beat. So a granted, narrower-than-root staff role has shipped all along; what was missing was any identity *holding* it.
>
> `frontOfHouse` is this design's `frontDesk` under another name: already minted, already resolvable cross-package (`cmd/lattice-pkg/main.go` registers all four identity-domain roles by canonical name in `Installer.RoleIDs`), and already narrower than root — both kernel producers gate explicitly on `canonicalName = 'operator'`, so it receives neither the platform write set nor the `WildcardAnchor` all-access read (`bootstrap/lenses.go`). **Adjudicated as lead (non-contract): reuse `frontOfHouse`; do not mint `frontDesk`.** A fifth role with an identical description in a different package would be permanently ambiguous, and the resident spine's own precedent points here — residents use identity-domain's `consumer`, so staff use its sibling in the same family. Everything §3.2 says about the role's *posture* holds verbatim; only the name changes. Read `frontDesk` as `frontOfHouse` throughout this document.
>
> **Carried into F3 — `RecordIdentityPII` needs a look.** The three inherited grants are pre-existing and untouched by F1, but F1 seeds the first identity that actually *holds* `frontOfHouse`, so they become live-reachable for the first time. Two are unambiguously the front-desk job; `RecordIdentityPII` is a PII **write** on an arbitrary identity, and this design's whole §3.5 argument is that staff reach resident PII only through workplace-anchored, RLS-confined reads. A write grant that predates the read spine sits outside that confinement. Not an F1 regression and not a blocker for the showcase (single-operator, and the op is the legitimate walk-in-registration path) — but F3 owns the PII boundary and should either scope this grant to the workplace or state plainly why an unscoped identity-PII write is correct for front desk. Flagging rather than silently inheriting it.

`frontOfHouse` — a package-declared role (`pkgmgr.Definition.Roles []RoleSpec`: minted in the same install batch with a deterministic NanoID, uninstall-reclaimed), carried by identity-domain, which owns the user-facing role family. Cross-package `GrantsTo:["frontOfHouse"]` resolution is the shipped `consumer` precedent (identity-domain declares it; clinic/wellness/café/lease-signing grant to it by canonical name). The v1 staff write surface re-attaches existing operator grants additively: `GrantsTo: ["operator", "frontDesk"]` on the demo-beat op set — loftspace `DecideLeaseApplication`; clinic `SetAppointmentStatus` (full transition surface, not the consumer cancel-slice), `RescheduleAppointment`; café `OpenTab`/`Charge`/`Settle`; wellness `CreateSession`. (The exact final list is confirmed per-vertical at build fire against each `permissions.go` — additions are one-line and additive; `operator` loses nothing.)

What `frontDesk` **never** receives: the kernel platform set (installs, meta writes — those project only for `operator`, `bootstrap/lenses.go:118-138`) and the `WildcardAnchor` all-access read (operator-only by the kernel producer's WHERE clause). A `frontDesk` holder with no `worksAt` link and no grants sees an empty world — every omission fails closed. The **Operations** persona needs *no* write grants at all in v1 (its cards are read-only aggregates); it is a `frontDesk` holder whose UX shows the aggregate pane — a second role is minted only when a write surface diverges.

### 3.3 The staff manifest half (SYNC plane — identity-anchored, non-PII)

Sibling lenses in `packages/edge-manifest` (the entity-browse precedent: no UNION ⇒ one lens per path; same `ns` ⇒ same renderer):

- **`edgeIdentity` (edit):** one added `OPTIONAL MATCH (identity)-[:worksAt]->(work)` + its container hop; `anchors` entries gain a `relation` literal stamped per walk (`'residesIn'` / `'worksAt'` — restoring the field the ratified §3.2 semantic reference always specified). Home can then group "At work: Riverside Building".
- **`edgeCatalogRoles` (new sibling):** the un-deferred role-standing-grant path — one `manifest.op.<opMetaId>` row per op-meta reachable via `(identity)-[:holdsRole]->(role)<-[:grantedBy]-(perm)-[:forOperation]->(op:meta)`, identical RETURN shape to `edgeCatalog`. Same `ns` + `entityId` ⇒ a template-reachable op and a role-granted op project the identical row (LWW-idempotent overlap, noted not feared). This also closes the named "browse all my ops" gap for residents (consumer-role grants project the same way).
- **`edgeTasksQueued` (new sibling):** the un-deferred FR28 path — one `manifest.task.<taskId>` row per open task via `(identity {key:$actorKey})-[:holdsRole]->(role:role)<-[:queuedFor]-(task:task)` — the my-tasks aggregate's own proven walk, re-emitted per-row — with edgeTasks' RETURN shape plus `queuedRole` (role key + canonicalName). The renderer's claim affordance submits the shipped `ClaimTask`; the platform's atomic queuedFor→assignedTo swap then retracts this row and materializes the `edgeTasks` one — the claim beat is pure existing machinery.
- **`edgeManifestStaffReadGrants` (new Path B slice):** `cap-read.edgeManifestStaff.<actor>` covering the two new anchor kinds (role-granted op-metas, role-queued tasks). A separate slice, not more branches on the existing producer: §6.14 unions slices, and the existing lens's own doc comment flags its cross-product fan-out — adding independent branches multiplies it. Same-commit discipline as entity-browse F4 (a missing grant silently empties the view).

**The catalog transport decision (resolved as lead — the honesty invariant decides it).** The role walk dead-ends at `perm.data.operationType` — a *string* — and the engine cannot string-join to the op-meta vertex. Two candidate transports: (i) **`permission forOperation meta`** — at install, when pkgmgr materializes a permission vertex + `grantedBy` link, it also links the permission to the op-meta it gates (both exist in the same install; the relation name reuses Contract #10's `task forOperation meta` precedent; sentence-reads correctly; permission is the later-arriving source). (ii) A "Front Desk console" *service template* `permitsOperation`-linked to staff op-metas, reached via a `worksAt` residence-walk mirror. (ii) is rejected on the design's own core invariant: the manifest *"only makes visibility honest, never permission"* — a template's `permitsOperation` list is curated presentation that can drift from what the actor's role actually grants, and the catalog would offer ops step-3 denies (or hide ops it allows). (i) makes the catalog *derive from the grant topology itself* — visibility mirrors permission by construction. Install-time wiring is mechanical and the site is exact: `internal/pkgmgr/build.go:286-289` already mints `lnk.permission.<id>.grantedBy.role.<id>` per grant; the `forOperation` link lands beside it, and `opMetaIDs` are deterministic in the same install (`build.go:229`). A permission whose op-meta doesn't exist (a package that declares no `OpMetaSpec` for that op) simply gets no link and stays catalog-invisible until one does: fail-closed, additive.

**Vocabulary (rides the unfrozen component spec, additive per its own evolution rules):** `dispatch.authContext` gains `"standing"` — "submit with no authContext object; authority is the standing role grant" (today's enum covers only self/service/task; staff ops are the fourth, oldest case). `manifest.task` rows gain `queuedRole`; `manifest.me.anchors` gain `relation`. Old renderers degrade unknown values per §3.3's rules. The staff op-metas adopt descriptors (presentation + per-op inputSchema + dispatch) — §7.4's deliberate operator-scope skip, now un-skipped for exactly the §3.2 op set.

### 3.4 The Protected worklist pane (FORK-S1 B — cross-identity, PII-correct)

`cmd/facet` gains staff handlers mirroring `credentials.go` (same DSN, same SELECT-only role, RLS as the signed-in staff actor):

- **Front desk:** *Applications to review* ← `landlordLeaseApplicationsRead` (pending-decision rows; `applicant_name` decrypts at projection only into rows the reader is granted); *Today's schedule* ← `clinicAppointmentsRead` (per-row `patient_name`, day-filtered); *unified resident context on lookup* ← the front-desk-\* NATS-KV buckets + `cafeTabSettlement` (host-level natsperm reads, same as `cafe-app`; PII-narrowed by construction — visit existence/time, never clinical content).
- **Operations:** occupancy + service-attach-rate (portfolio-pulse), provider utilization (`providerAppointmentsRead` aggregated host-side), vacancy (`availableListings`). Aggregates only — the demo card's own caption is the spec.

The FE renders these as a **Worklist screen archetype** (new, staff-only; Sally specs it at build per the Fire-2 convention) fed by `/api/staff/*` — clearly server-pane, not mirror rows; offline shows the pane unavailable while the manifest half keeps working. `clinicPatientsRead` (wildcard-only roster) is deliberately **not** in v1: the schedule rows already carry the day's patient names; the full-roster switcher stays a clinic-app/operator surface until a scoped anchor model for rosters exists.

### 3.5 The cross-identity read spine: workplace-anchored grants (the security heart)

The RLS trap this design refuses: anchors are **global tokens** — granting a front-desk actor each *resident's* NanoID would open those residents' rows in every Protected table (credentials included). Instead:

- **Anchor the worklist rows on the workplace.** `landlordLeaseApplicationsRead` and `clinicAppointmentsRead` each gain one derivable anchor in `authz_anchors`: the **building's NanoID** (loftspace: `unit -containedIn*-> building`; clinic: `provider -practicesAt-> building`). Additive — existing applicant/landlord/patient/provider anchors unchanged; both tables already run DiffRetraction, which handles the recomputed anchor set. Engine note: every shipped `authz_anchors` is a single-element literal (`[nanoIdFromKey(x)]`); the two-element form uses the proven concat idiom (`[nanoIdFromKey(x)] + collect(DISTINCT nanoIdFromKey(bldg.key))` — the `edgeManifestReadGrants` shape), and the missing-building degenerate entry must be proven non-matching by a §5 negative vector, not assumed.
- **`staffReadGrants` (new Path A GrantTable producer**, clinic-package-producer precedent, own `grant_source: 'cap-read.staff'`): one `(actor, anchor)` row per `(identity)-[:holdsRole]->(role[frontDesk])` × `(identity)-[:worksAt]->(building)` pair — the grant *is* the building NanoID, and it exists only while **both** links exist. Unwire `worksAt` or revoke the role ⇒ the grant row drops out of the projection ⇒ DiffRetraction deletes it (named per the retraction-transport rule: this is a row-set shrink, and unlike the clinic producers, no anchor-vertex tombstone covers an unwire — the diff **is** the transport). Both halves are shipped machinery — plain-lens DiffRetraction (`pkgmgr/definition.go:736`; GrantTable lenses are plain) and the grant writer's **seq-guarded** `Delete → RevokeGrant` keyed `(actor_id, anchor_id, grant_source)` (`adapter/read_path_adapters.go` — a stale replay cannot resurrect a revoked grant, per the §6.14 monotonic guard) — their composition on a grant-table target is F3's first named verification, and if the diff pipeline needs a small wiring for grant targets, that is an in-fire adapter addition, not a redesign. A building token opens exactly the workplace-scoped worklist rows across tables — the semantic the deployment actually means. Verified this fire: **no shipped lens anchors any row on a building NanoID today** (the full `authz_anchors` producer census: identity-credentials → identity; clinic → patient/provider/∅; lease-signing → applicant/landlord; loftspace → landlord/∅), so the new token class opens nothing pre-existing; any future lens that anchors on a building id is *by that choice* declaring its rows workplace-readable — recorded here as the convention.
- **Fail-closed audit:** no `worksAt` ⇒ no grant ⇒ empty pane. No `frontDesk` ⇒ same. Anchor column derivation misses (an unlinked unit) ⇒ that row carries no building token ⇒ invisible to staff, visible to its landlord/patient as today. Nothing in this design widens `operator`, touches `WildcardAnchor`, or grants an identity-class token.

Write-side confinement — "may this front-desk actor `DecideLeaseApplication` for a *different* building?" — is scope=any in v1 exactly as it is for today's operator FEs (no *new* over-grant; strictly narrower than the operator posture staff hold today), and is closed properly in F4 by worksAt-scoped Starlark guards (the café `applicationFor`-indirection precedent: resolve the target's building read-declared, require a caller `worksAt` link, AuthDenied otherwise). **F4 is a MUST before any multi-organization deployment** — v1's honest scope is the single-operator showcase.

### 3.6 Reconciliation with the existing mental model

- *Didn't we already build front-desk context?* The **read models**, yes (mixed-use fire; consumed by cafe-app's tab). What never existed is a staff *client world* — identity-derived discovery of "what is my work." This design reuses those read models unchanged as F3's data source.
- *Didn't we already project role-queued tasks?* In the **my-tasks aggregate** (a bucket read model staff FEs poll), yes — the walk ships there verbatim. Not on the personal SYNC plane, which is what a Facet device mirrors; edgeTasksQueued re-emits the same walk per-row over the shipped transport.
- *Isn't staff tooling Loupe's job?* No — Loupe is the platform inspector (the demo's *fourth* persona). Front-desk staff are business users of business surfaces; putting them in Loupe would hand them Core-KV inspection.
- *Does this duplicate the entity browse?* No — it reuses its patterns (sibling lenses, same-commit read grants, typed rows) on a different spine; the browse serves "pick a target for my op," this serves "what work exists at my workplace."
- *Does this contradict "the app is a pure function of its local mirror"?* It bends it where Inc 3 already bent it (`credentials.go`): Protected data was never going to ride the mirror — D3 ratified that split. The mirror stays the world-structure truth; the pane is session-scoped Protected context.
- *New state?* Only graph data via ordinary ops (worksAt links, a role vertex, permission links) and two lens-projected grant/anchor extensions. No new stores, no new planes, P2/P5 intact.

## 4. Alternatives considered (beyond the forks)

- **A second staff app** — rejected: the thesis is same-binary/different-identity, and the renderer needs zero staff knowledge (the pane + one screen archetype are data-driven).
- **Reach the staff catalog via a curated service template** — rejected on the honesty invariant (§3.3): visibility must derive from the grant topology, not parallel curation.
- **Grant staff per-resident identity anchors** — rejected as a structural over-grant (§3.5): anchors are table-blind global tokens.
- **A `frontDesk` wildcard read** ("staff read everything, like the FEs assume today") — rejected: recreates root-read one role over; the whole point of the first narrow staff role is that the read posture narrows with it.
- **Wire `worksAt` into `cap.svc` for a staff service catalog** — rejected for v1: `availableAt`'s join is authorization-bearing; staff authority is role grants, not the service path. Revisit only if a real "services offered to staff" demand appears.

## 5. Test strategy

Per fire: package unit + cypher tests (the entity-browse suite shapes: lens row shape, grant-producer rows, degenerate/tombstone filtering); `verify-package-edge-manifest` + `verify-package-service-location` (DDL/keys touched); RLS vectors in `internal/refractor/adapter` style for the building-anchor + `staffReadGrants` pair — including the **negative** vectors (no-worksAt ⇒ zero rows; building-B staff denied building-A rows; unwire retracts the grant row and a stale-seq replay cannot resurrect it; a row whose building walk resolved null carries no matchable token); renderer node vectors (`cmd/facet/web/*.test.mjs`) for claim + worklist + standing-dispatch; live green bars per fire on the showcase stack (Core-KV/RLS-confirmed, not FE toasts — the §7.9 standard).

## 6. Decomposition for the Steward (each independently shippable + green)

- **F1 [verticals · pkg] — spine + role + write surface.** ✅ **SHIPPED 2026-07-19.** `worksAt` Wire/Unwire in service-location; **no role minted** — `frontOfHouse` reused (see §3.2's build-time correction); `GrantsTo` widening on the §3.2 op set (lease-signing `DecideLeaseApplication`; clinic `RescheduleAppointment`/`SetAppointmentStatus`; café `OpenTab`/`Charge`/`Settle`; wellness `CreateSession`); showcase seed gains the staff persona (`worksAt` the showcase building, holds `frontOfHouse`, no `residesIn`, no `operator`). *Green:* the ten-op link surface + the three new `worksAt` integration vectors pass against the real Processor under capability auth, and the clinic permission test now pins the exact role SET per grant so the widening cannot silently leak past the two front-desk ops.
  - **Deferred to F2 (named consumer, not dropped):** descriptor adoption (op-metas + `"standing"` authContext) for the staff op set. F1's green bar does not need it — descriptors exist to be *rendered*, and their consumer is F2's `edgeCatalogRoles`. Shipping them a fire early would land an unrendered vocabulary with nothing reading it.
  - **Not built: the `FACET_DEMO_PERSONAS` staff card.** The seed exports `FACET_STAFF_NANOID`, so the card is a one-line env addition — but a demo-exposed staff persona reaches cross-identity surfaces, and F3 is what makes those surfaces RLS-correct. Wiring the card now would expose a staff login whose worklist pane does not yet exist. It belongs with F3, behind the existing demo checklist (§8's exposure risk).
- **F2 [verticals · pkg+FE] — the staff manifest.** ✅ **SHIPPED 2026-07-19.** `permission forOperation meta` install wiring (`internal/pkgmgr/build.go`, with the fail-closed no-op-meta case pinned); `edgeCatalogRoles` + `edgeTasksQueued` + `edgeIdentity` work-anchor edit + `edgeManifestStaffReadGrants` — all in the same commit, per the entity-browse F4 grant discipline; `DecideLeaseApplication` descriptor adoption carrying the new `authContext: "standing"`; renderer: work-anchor grouping, claimable-task affordance, standing dispatch. *Green:* all three new cyphers parse under the full engine (`TestPackage_SpecsParseUnderFullEngine`), 71 renderer vectors pass including 7 new staff ones, full `go test ./...` green.
  - **Two things the design under-specified, resolved in build.** (1) `ClaimTask` was granted only to `operator`; orchestration-base's own comment anticipated exactly this ("a vertical package's own role-queue role must ALSO grant that role ClaimTask"), so `frontOfHouse` was added there. The grant is safe platform-wide because the DDL script resolves the task's own `queuedFor` link and rejects `NotAuthorizedToClaim` unless the submitter holds THAT role — the permission is the outer gate, holding the queued role is the real confinement. (2) The claim affordance is `ClaimTask`'s **first production dispatcher** (the script's read-posture notes said as much), so it owes the declarations: the task as a required read, the claimant's own `assignedTo` link as an optional one (absent on a first claim, present on an idempotent re-claim).
  - **The staff mirror needed a control grant nobody had noticed.** §3.3 assumed the SYNC plane "just works" for a staff identity. It does not: `frontOfHouse` held none of control-authz's Personal Lens ops, so the staff device could not `personal.register` and synced **nothing** — the sync manager failed and retried forever, producing an entirely empty world rather than a degraded one. control-authz now grants that set to `frontOfHouse` (its own §3.4 confinement argument for `consumer` extends verbatim — the ops are identity-bound server-side). Live-confirmed: hydration completes on the showcase stack where it previously never did. **Generalized rule now pinned in the package test: any role whose holders sign into a mirroring client must be in that grantee set.**
  - **Deferred, with named consumers:** descriptors for café `Charge` and wellness `CreateSession` (both lack op-metas, so both stay catalog-invisible until one exists — fail-closed by construction, not a silent gap); Sally's Worklist screen archetype, which belongs with F3's pane rather than ahead of it.
  - **Open verification tail (F3 opens here).** The package plane is live-proven — `forOperation` links minted, and the staff vs. resident `cap-read` slices resolve to exactly the expected, disjoint op lists. The staff client, however, still shows an empty world after hydration: this actor has **no `manifest.*` rows projected yet**, because Personal Lens rows are CDC-triggered and the staff identity has not been mutated since the lens edits landed. That is an operational property of lens edits (existing actors need a triggering mutation or a Refractor rebuild to re-project), not a defect in the F2 cyphers, which parse and whose grant slices demonstrably project. Confirming it needs a role-queued task in the seed — F5's domain content, or a `ctrl.refractor.rebuild`. Recorded rather than papered over: **the renderer beats are unit-proven (7 vectors), not yet live-proven.**
- **F3 [verticals · pkg+FE] — the Protected worklist pane + read spine.** ✅ **SHIPPED** — read spine 2026-07-19 (`5c797e03` + `415e18f3`), the pane 2026-07-20 (`21130319`).
  - **The pane.** `GET /api/staff/worklist` serves both §3.4 sections in ONE transaction under a single txn-local `lattice.actor_id` (the session identity's bare NanoID). Neither SELECT carries a workplace or actor predicate — confinement is RLS over the building tokens `staffReadGrants` projects, and a test pins the absence so nobody later "tightens" it into an advisory `WHERE`. The schedule column list is deliberately narrower than the table (`reason`, `documented_at`, `follow_up_*`, `status_note` excluded): a front-desk worklist's business is visit existence and timing, and a second test makes widening it a deliberate PHI decision. The boot-env fallback session is refused (cross-identity PII, mirroring `credentials.go`). FE: the **Work** tab derives from a `worksAt` anchor on the manifest me-row — never curation — and an unreadable pane renders UNAVAILABLE rather than a falsely-empty worklist. *Green (live, showcase stack):* the staff actor reads exactly **1 of 3** appointments — the building-anchored one, `patient_name` decrypted for an authorized reader — while the two patient/provider-only-anchored rows stay invisible; an actor with no `worksAt` reads **0**; the Work tab is `hidden`/`display:none` for a resident and renders "Riverside Building" for staff. 7 renderer vectors + 7 Go handler vectors.
  - **Not built (deferred, named consumers):** the **Operations aggregates pane** (occupancy / provider utilization / vacancy — consumer: the site demo's Operations persona; different sources — portfolio-pulse + `availableListings`), and the **front-desk unified resident-context lookup** (consumer: the walk-in front-desk beat; sources: the `front-desk-*` NATS-KV buckets + `cafeTabSettlement`). Both are §3.4 scope this fire did not reach; neither blocks F4.
  - **F2's "open verification tail" is closed.** The staff client no longer hydrates an empty world: re-wiring `worksAt` is itself a mutation, which CDC-triggered the Personal-Lens projection (6,473 manifest messages on the staff SYNC subject). The renderer beats are now live-proven, not only unit-proven.
  - **The blocker below is FIXED** (`73557e8`, `e0ab660`), so `staffReadGrants` shipped with it. Live vector on the running stack: wire ⇒ grant live ⇒ the staff actor reads exactly 1 workplace-anchored appointment under RLS; `UnwireWorksAt` ⇒ grant `is_deleted=t` ⇒ the same SELECT returns **0**; every other producer's live grant count unchanged (cap-read 17, clinic.patient 2, clinic.provider 3, consoleOperator 1, root 6); re-wiring restores both. The producer lives in **service-location**, which owns `worksAt` — the link whose removal must revoke the grant.
  - **⚠️ The "anchors shipped" claim below was FALSE.** `c662dc54` is a **dangling commit on no branch** — it never reached `main`, while the Done log recorded it as shipped. The clinic half was reconstructed and the lease-signing half recovered from that commit (with both its tests). A Done-log SHA is not proof the code landed; `git merge-base --is-ancestor <sha> main` is.
  - **Shipped:** workplace anchors on `landlordLeaseApplicationsRead` (`unit -containedIn*-> building`) and `clinicAppointmentsRead` (`provider -practicesAt-> building`), plus vectors on both. These are **inert until a producer grants building tokens** — no actor holds one today, so this commit changes no read outcome. That is the point of shipping them alone: additive, safe, and they pin the hazard below for whoever builds next.
  - **The null-element hazard is real, and the design's suggested form would have hit it.** §3.5 proposed a two-element `authz_anchors`. That is WRONG here: when the building walk finds nothing the array gets a NULL element, and the Protected adapter's `toStringSlice` **rejects** a non-string element — failing the row's entire upsert, so the row vanishes for its own landlord/patient too. A missing building must cost a row its staff visibility, never its existence. Both lenses instead use a pattern **comprehension** (`[(u)-[:containedIn*1..]->(b:building) | nanoIdFromKey(b.key)]`), which yields `[]` rather than a null. Live-confirmed both ways: an appointment whose provider practises at the building carries `{patient, building}`; two whose providers do not carry `{patient}` only and still project.
  - **✅ RESOLVED 2026-07-19 (`73557e8`, `e0ab660`) — was: 🚨 BLOCKER — DiffRetraction does not compose with GrantTable, and fails SILENTLY.** The fix made `grant_source` a declared `LensSpec` field (enforced against every written row) so `ListKeys` can scope retraction to one producer's own rows, and made the mis-config fail closed at activation. Fixing it exposed the **same defect one layer out**: `ProtectedAdapter` wraps a `KeyLister` but did not re-declare the interface, so `landlordUnitsRead` and `landlordLeaseApplicationsRead` had *also* never retracted since they shipped — this design's §3.5 claim that "both tables already run DiffRetraction" was false for exactly that reason. Original finding follows. §3.5 named this composition as "F3's first named verification." It fails. `GrantWriterAdapter` does not implement `adapter.KeyLister`, and `pipeline.applyDiffRetraction` **returns unchanged** when the adapter is not a KeyLister — so a GrantTable lens that opts into DiffRetraction never retracts anything, with no error. Proven live end-to-end: with the producer installed, `UnwireWorksAt` left the grant row `is_deleted = f` and **the ex-staff actor could still read the workplace-scoped appointment row**. Fixing it needs the adapter to list keys scoped to its OWN `grant_source` — and the source is a projected column, not lens metadata, so it must first become a declared field. Unscoped listing would make one grant lens delete every other producer's rows, which is why this is a real decision and not a mechanical patch. **Two sub-findings worth fixing regardless:** (1) that silent pass-through should fail closed — a DiffRetraction lens on a non-KeyLister adapter is a configuration defect that currently presents as a working security mechanism; (2) `applyDiffRetraction`'s doc already calls this case "a configuration defect," so the intent exists, only the enforcement is missing.
  - **Also found 2026-07-19, same family, third call site (`3d93697`): a package could never re-add a removed entity.** Entity keys are deterministic in (package, kind, canonicalName), so re-adding `staffReadGrants` landed on the exact key its removal had tombstoned; `diffManifest` emitted a create, which asserts revision 0 over the subject's history, and reported the deterministic failure as "a concurrent write raced this upgrade — re-run." Fixed by reviving through an update under the same per-key OCC.
  - **✅ RESOLVED (`1e7f49c`) — was: `Wire*` cannot revive a tombstoned link** (pre-existing, unrelated to staff worlds). service-location's shared `wire()` helper emitted an `op: "create"` for a tombstoned link, and create asserts revision 0 → `RevisionConflict`. Every Wire op inherited it, so **`residesIn` had it too**: a resident who moved out (`UnwireResidesIn`) could never be moved back in. Fixed with `update` semantics for the revive case, plus the read declaration the Wire callers needed for the link key itself.
  - **Live-stack note — RESOLVED 2026-07-20.** Exercising the retraction vector had tombstoned the showcase staff persona's `worksAt` link, and while the revive bug stood, neither `WireWorksAt` nor a `seed-showcase` re-run could restore it (the re-mint collides on the fixed staff email index). With `1e7f49c` shipped, `seed-showcase`'s `ensureStaff` recovers the persona **by its `worksAt` link** and re-wires a tombstoned one rather than treating it as absent. Confirmed on the running stack: the link is live (`isDeleted:false`) and the `cap-read.staff` grant is back to exactly one row (staff actor → building anchor), with every other producer's grant count unchanged.
- **F4 [verticals · pkg] — write confinement (multi-tenant gate).** ✅ **SHIPPED 2026-07-20** (`96c371e9`, version bump `a3fa5318`). One canonical guard, byte-identical in all four packages, plus a per-domain resolver from the op's target to its location: `DecideLeaseApplication` (leaseapp→appliesToUnit→unit→containedIn\*→building), café `OpenTab`/`Charge`/`Settle` (the tab's own `.status.leaseAppKey`, then the same walk), clinic `RescheduleAppointment`/`SetAppointmentStatus` (appointment→withProvider→provider→practicesAt→building — the same edge `clinicAppointmentsRead` anchors its read token on, so read and write confinement agree by construction), wellness `CreateSession` (studio→locatedAt). Every location is resolved from the **target's own topology, never a payload field**. *Green:* full `go test ./...`, and live on the showcase stack — the staff persona opens a tab at Riverside (accepted), is **AuthDenied at a second building** (`vtx.building.kqH8xwQr…`), and the operator writes at both.
  - **Three build-time decisions, each the opposite of a simpler form that looked right.** (1) The exemption is **role-derived, not worksAt-derived**: exempting "an actor with no worksAt link" is perverse, because `UnwireWorksAt` would then *widen* a staff member's write surface from one building to every building. Root is instead proven by holding the primordial `operator` role — the same walk the kernel projects its own root grant from — so an unwired staff member is denied everywhere and any future narrow role is confined by default. (2) **A tombstoned link is ABSENT**: `kv.Read` returns the tombstone *document*, not `None` (step-4 hydrate routes only `ErrKeyNotFound` to `knownAbsent`) and `UnwireWorksAt` tombstones rather than deletes, so the `== None` form the existing café/clinic self-guards use would have let a moved-on staff member keep writing. (3) The guard binds the **standing path only** — a `scope=self` caller sets an authContext and is bound by its op's own ownership probe; a resident holds no `worksAt` link, and confining them by a staff rule would deny every self-service write. An unresolvable location denies for anyone but root, so removing a `containedIn` link is not a bypass.
  - **The root test walks the graph rather than substituting a baked-in role id, and that is forced, not chosen.** `var Package = pkgmgr.Definition{DDLs: DDLs()}` is evaluated at package **init**, while the primordial ids load at **runtime** (`bootstrap.LoadPrimordialNanoIDs`) — so no compile-time substitution can ever see the operator id. An attempt to inject it baked in the empty string and failed every guarded op; recorded so nobody re-tries it. The clean fix is platform-side (below), not package-side.
  - **A test-fidelity gap this fire found and closed.** Package tests seeded only a **cap doc** for their actor, never the `holdsRole` **link** the graph carries for the admin and the five service actors — so an operator-granted actor did not look like an operator to any script that asks the graph. `testutil.SeedHoldsRole` / `SeedLink` close it and the four suites now model production. Four vectors pin the behavior (`packages/cafe-domain/workplace_confinement_test.go`), including the unwired case that tells the role-derived design apart from the worksAt-derived one, and the unlocatable-target fail-closed default.
  - **Residuals (filed as rows, not left in prose):** the platform could expose the authorizer's already-resolved roles to the script (`op.actorRoles`), replacing a graph re-derivation — and one `kv.Links` round trip per guarded op — with the authoritative answer step 3 computed; the `== None` link probes in the shipped café/clinic **self**-guards are the same latent defect (harmless only because nothing tombstones `applicationFor` today). §3.2's carried-forward `RecordIdentityPII` question — **RESOLVED 2026-07-22** (`28c69837`): since F4's location-derived confinement can't reach a walk-in identity (no location to confine against), the boundary is the state machine instead — a STANDING (no authContext) frontOfHouse/backOfHouse grant may target only an unclaimed identity; operator and any task/self-scoped submission (e.g. lease-signing's onboarding userTask) are exempt, mirroring F4's own root-and-task carve-outs.
- **F5 [verticals · pkg+FE] — manifest-plane worklists (FORK-S1 A, the ratified fast follow).** Consumer: the offline maintenance tech. Per-row staff worklist lenses on the workplace spine (`worksAt → building` reachability; nameless rows per D3 — typed labels + `.presentation` context only), their Path B read-grant fan-out (same-commit discipline), and the offline-claim UX over the shipped intent queue (queue `ClaimTask`/complete disconnected, drain on reconnect). Domain content: a role-queued maintenance work-order task in the showcase seed proves the beat; the real work-order producer op set is a parallel PO-filed demand row (verticals board), not a blocker — §7.4's precedent. *Green:* a `maintenance`-role persona claims a queued work order and completes it **with networking severed mid-flow**, the drain landing both ops after reconnect, Core-KV-confirmed.
  - **Inc 1 — the domain content + its grants. ✅ SHIPPED 2026-07-21** (`5f2517ab`). New cross-vertical package `maintenance-domain`: `vtx.workorder` raised at a place by `ReportIssue`, closed by `ResolveWorkOrder`. Grants: orchestration-base `ClaimTask` and control-authz's Personal-Lens set both gain `backOfHouse` (the second is F2's pinned rule — without it the tech's device registers no interest and syncs nothing). `edge-manifest` 0.7.1: both task lenses' `scopedName` resolves a maintenance task's work-order `.report` summary, and `selfAnchors` gains `workplace` so `ReportIssue`'s `{me.workplace}` fills the staff form. Seed: a SECOND staff persona (backOfHouse) plus a day-rolling work order + queued task.
    - **The completion op the board row asked for does not exist, and must not.** `ResolveWorkOrder` IS the completion: the claimant performs it under `authContext.task` and the §10.6 auto-complete closes the task on the same commit (pinned by a vector). It is granted to `operator` only — SignLease's posture — because a standing `backOfHouse` grant would hand every holder every order in the building and make the claim ceremony decorative; a test pins the absence so it is not later helpfully "fixed".
    - **The offline requirement landed on terminality, not on transport.** `.resolution` is the read-before-write terminal marker, and an IDENTICAL-notes re-submit is an accepted no-op rather than a rejection: a drain retrying under a fresh requestId slips past the Contract #4 tracker, and failing it would lose the tech's work at exactly the moment the offline beat pays off. Differing notes still reject.
    - **Live on the showcase stack, driven through Facet's own intent queue** (`/api/enqueue`, the same durable path a disconnected device queues into — only the severing is missing): the tech's mirror carries `manifest.task` with `scopedName` = "Basement riser valve is weeping…" and `queuedRoleName: backOfHouse` (NOT a bare NanoID — the 0.7.1 lens edit); `cap.roles.<tech>` carries `ClaimTask` + `ReportIssue` + the five `ctrl.refractor.*`; `cap.ephemeral.<tech>` carries `ResolveWorkOrder` scoped to the work order **from the queued-role branch, before any claim**, which is the whole "authority arrives via the task" claim made concrete. Claim swapped `queuedFor`→`assignedTo` (old tombstoned); the resolve — with NO standing resolve grant anywhere in the tech's capability — wrote `.resolution` and the task went `status: complete` on the same commit. A drain replay (identical notes, fresh requestId) left `lastModifiedAt` byte-identical: an accepted no-op, live.
    - **Inc 2 — the domain worklist + the offline half. ✅ SHIPPED 2026-07-21** (`e269c27d`). `edgeStaffWorkOrders` projects one `manifest.work.<id>` row per work order at a place the actor worksAt, walking DOWN the workplace (`(work)<-[:containedIn*0..]-(place)<-[:locatedAt]-(wo)`) — the first inbound variable-length walk in the corpus, since every earlier one runs UP from a resident's residence. It is a separate lens from `edgeTasksQueued` because the two answer different questions: a task exists only if somebody queued one, while work at your building with no task on it is still state your world should show. `edgeManifestStaffReadGrants` gains the workorder anchor branch in the same commit. The renderer's Work screen now carries a mirror-backed section ABOVE the Protected pane and is deliberately its opposite: it renders in full offline, keeps Claim/Resolve live over the durable intent queue, and says "queued and syncs when you are back" instead of the pane's "unavailable".
    - **The green bar is met for resolve, and NOT for claim.** Live on the showcase stack: with `cmd/facet` restarted against a dead gateway, the tech's mirror still served the work order, the resolve enqueued, the drain retried on a loop and **Core KV stayed untouched**; on reconnect the drain landed it — `.resolution` written by the tech, the task `status: complete` via §10.6 auto-complete, and the `manifest.work` row flipped to `resolved` off that same aspect with no second write. That is the severed-network beat end to end. The **claim** half could not be driven live: see the finding below.
    - **🚨 Found live, filed, NOT Inc 2's defect — a second role-queued task never reaches the mirror.** A `CreateTask` queued to `backOfHouse` produced a correct `cap.ephemeral` grant and a correct `cap-read.edgeManifestStaff` slice carrying the task anchor, but no `manifest.task` row ever reached the tech's device — across a full `cmd/facet` restart and re-hydrate — while a STALE row for an already-`complete` task stayed on the mirror un-retracted. The shipped `edgeTasksQueued` spec is not at fault: driven against a real graph in the new `lens_cypher_test.go` harness it matches that exact topology and returns the row. Nor is the read gate: `manifest.work` rows pass it against the same slice at the same moment. So the row is lost between a correct lens and a correct grant — projection/delivery, `internal/*`, filed to the Lattice lane. Consequence for this fire, stated rather than glossed: **the Claim affordance is unit-proven (8 renderer vectors), not live-proven.**
      - **Root cause found + the "never reaches" half FIXED (Lattice Steward, 2026-07-22).** `control.Service.personalHydrator` (`internal/refractor/control/service.go`) was a single overwritten handle, not a per-ruleID registry — `cmd/refractor/main.go`'s install loop called `SetPersonalHydrator(p)` once per Personal Lens rule (edge-manifest ships ten), so after startup it pointed at whichever rule installed *last*, never `edgeTasksQueued`. The `personal.hydrate` RPC's cold catch-up therefore structurally could never reach that lens — this is why the tech's *first* task (created while the device was live-connected, delivered via the independent per-pipeline CDC fan-out) rendered fine, but a *second* task needing the cold rehydrate path after a restart never arrived. Fixed: `personalHydratorByRuleID map[string]Hydrator` + `RegisterPersonalHydrator(ruleID, h)`, mirroring `RegisterReprojector`; the RPC now fans out to every registered pipeline and reports the max revision (all Personal Lens rules share one SYNC stream). Proven by `TestControl_PersonalHydrate_MultipleLenses_FansOutToAll` (`internal/refractor/control/personal_hydrate_test.go`).
      - **The "stale row never retracts" half is a SEPARATE, deeper gap — NOT fixed here, needs design.** `NatsSubjectAdapter` (the Personal Lens target) is a push-only per-actor delta stream with no `KeyLister`/stored-key-state to diff against — unlike the existing Fire-3 `diffRetraction` mechanism (`pipeline.applyDiffRetraction`), which requires exactly that and is structurally gated off for every actor-enumerator (Personal Lens) pipeline (`evaluateForEntryRaw`, `internal/refractor/pipeline/evaluate.go:158`). Neither the live fan-out path nor cold `Hydrate` (`internal/refractor/pipeline/hydrate.go`) ever emits a `Delete` for an anchor that drops out of a multi-row personal lens's result set, and the Edge client's `OnHydrationComplete` callback (`internal/edge/sync/sync.go`) carries only a revision, not a key set, so it cannot reconcile locally either. Closing this needs an actual design decision on where reconciliation state lives (Refractor-side per-actor emitted-key tracking vs. client-side full-snapshot-replace semantics on `hydrationComplete`) — filed as its own row, not improvised inline. **That design has since been written and Andrew-ratified: [`personal-lens-retraction-design.md`](personal-lens-retraction-design.md), build-ready, `lattice.md` row `[Refractor] Personal Lens rows never retract`.**
      - **Re-tested live (Vertical Steward, 2026-07-22) — the delivery fix above is necessary but NOT sufficient; claim beat still not provable live.** Minted a genuinely fresh task (`vtx.task.UngZGQm4r4i4WiYPxkHv`, queued to `backOfHouse` off work order `vtx.workorder.o71d4EQ3rxwUjc8uaM18`) and restarted `cmd/facet` twice (12 minutes apart) to force cold `personal.hydrate`. Both times: `edge/sync: hydration complete` logged (the fixed fan-out ran, no error), yet the new task's `manifest.task` row never appeared, and a raw dump of all ~812 messages on `lattice.sync.user.hqxGU8z3DRHqj2DMep5C` showed **zero** ever mentioning it — while Health KV reported `refractor` healthy with `lensLags["1mPJFABzHffUwUkg1mPJ"]=0` (i.e. falsely "caught up") and the D1 read-grant doc (`cap-read.edgeManifestStaff.identity.hqxGU8z3DRHqj2DMep5C`, `capability-kv`) correctly listed the anchor as readable. Root-caused (read-only investigation, no code changed): `personalEnvelopeFn` (`internal/refractor/projection/personal.go:127-174`) gates every row — live **and** hydrate — on `capabilityread.IsReadable` (`internal/refractor/capabilityread/capabilityread.go:70-111`) against `cap-read.edgeManifestStaff.<actor>`, written by the *independent* sibling pipeline `edgeManifestStaffReadGrants` (`packages/edge-manifest/lenses.go:201-217`) with no cross-pipeline ordering; a miss returns `pipeline.ErrSkipProjection`, silently `continue`d in `executeFullForActor` (`internal/refractor/pipeline/evaluate.go:208-213`) — no log, no error, no health signal, indistinguishable from "cypher legitimately produced nothing." **This is the exact mechanism `personal-lens-retraction-design.md` §9 already names as an accepted risk for grant-*shrink*** ("the D1 gate races the sibling cap-read producer pipeline… retraction lands on the revoking event only if the cap-read producer projected first; otherwise on the next enumerating event or hydrate") — this re-test shows the same race also blocks grant-*growth* with no observed self-heal even after two hydrates and the grant document confirmed present by the second one, i.e. hydrate does not reliably re-close the gap the design's own risk language implies it should. Not patched inline: no periodic sweep exists for Personal Lens pipelines today (`reproject.go:14-19` explicitly excludes them — "the Personal Lens has its own Hydrate"), so a real fix is either the frame mechanism `personal-lens-retraction-design.md` already specifies (a full post-filter keyset re-emitted every evaluation, which would also close this delivery gap since it doesn't remember "already skipped") or a narrower fail-open retry — either is a decision for the mechanism's own build fire, not a quick edit here. **Practical read: the claim beat's live green bar is blocked on building `personal-lens-retraction-design.md`, not merely on today's Hydrate registry fix** — its own §5 acceptance criterion already names "the staff-worlds claim beat" as the live proof.
    - **✅ Claim beat live-proven end to end (Vertical Steward, 2026-07-22), once two independent bugs it exposed were fixed.** With `personal-lens-retraction-design.md`'s R1+R2 shipped, `edgeTasksQueued`'s row finally reached the tech's mirror — but claiming it still failed, uncovering a defect the retraction fix could never have surfaced on its own: **(1)** `scripts/seed-showcase.go`'s `ensureStaff` resolved the showcase persona by scanning `worksAt` the building, which became ambiguous the moment F5 Inc 1 gave the maintenance tech a second `worksAt` link to the same building — it silently returned the tech's identity as `FACET_STAFF_NANOID` (sorted first). Fixed to resolve by `holdsRole` instead, mirroring `ensureMaintenanceTech`'s own already-correct pattern (`35ca90f5`). **(2)** The claim button (`cmd/facet/web/app.js`, both the Home Tasks list and the Work screen's work-order list) built `ClaimTask`'s `payload.taskKey` and its required `contextHint` read from the manifest row's own SYNC-plane storage key (`manifest.task.<id>`) instead of the Core-KV vertex key `edgeTasksQueuedSpec` already projects at `data.taskKey` (`vtx.task.<id>`) — every claim attempt failed closed at the Processor's hydrate step with `HydrationMiss`, invisibly to the renderer vectors because the fixtures modeled the two keys correctly without any assertion pinning which one reached the button. Fixed to use `data.taskKey`, with new vectors in `staff_world.test.mjs` + `work_orders.test.mjs` pinning the distinction (`78927466`). With both fixed: claimed `vtx.task.4WnbNFZSYrTUQD2o91uU` (queued off today's day-rolling work order) with `cmd/facet` pointed at an unreachable gateway — the tap enqueued, toasted "Claimed", and Core KV stayed untouched (`queuedFor` still live) while the drain loop retried on a `connection refused` loop; on reconnect the drain landed it in one commit (`vtx.op.aaSZERZ66UrmGqXU4Y9t`) — `queuedFor` tombstoned, `assignedTo` the tech, the row re-projected through `edgeTasks` and the Claim button disappeared from the UI. The severed-network beat is now proven for **both** halves (resolve per Inc 2, claim here) — Inc 2's stated green bar is met.
    - **F5 CLOSED (Vertical Steward, 2026-07-22).** The domain worklist lens (`edgeStaffWorkOrders`) and its Path B grant fan-out shipped in Inc 2 (`e269c27d`), and the claim beat is now live-proven end to end (above) — F5's stated green bar (a `maintenance`-role persona claims a queued work order and completes it with networking severed mid-flow, the drain landing both ops after reconnect, Core-KV-confirmed) is met on both halves. Nothing further scoped.

Order: F1 → F2 → F3 → F4 (F2 and F3 are independent after F1 but share the seed persona; build F2 first so the pane lands in a world that already renders). F5 follows F2 and can parallel F3/F4.

## 7. Adversarial pass — RUN (this fire, 2026-07-19)

Eight load-bearing claims were verified against code before this doc was flagged; no deferred gate remains for the Steward. Held as written: cap-read slice **union** (`capabilityread.go:49-55` unions base + every `cap-read.*.<actor>`); `cap.roles` generality over any held role (`rbac-domain/lenses.go:76`); package-declared roles + cross-package `GrantsTo` (`pkgmgr.Definition.Roles`, the `consumer` precedent); the queued-role walk (shipped verbatim in the my-tasks cypher); `forOperation` install wiring (`build.go:229,286-289`); the over-grant hunt (building-NanoID anchor census clean). Amended after the pass: the two-element `authz_anchors` concat idiom + its null negative vector (§3.5, §5), and the DiffRetraction×GrantTable composition named as F3's first verification with its fallback (§3.5). One claim was *strengthened*: the grant writer's `Delete` is already seq-guarded against stale-replay resurrection — the §6.14 monotonic-guard obligation this design would otherwise have had to add.

## 8. Risks

- **Engine surprises in the new walks** (role-queued MATCH direction, forOperation off a permission vertex) — mitigated by the entity-browse method: prove each cypher in a `lens_cypher_test` before wiring.
- **Grant-slice union semantics** — §6.14 says union; F2 verifies `capabilityread.IsReadable` honors a second slice with a live vector before relying on it.
- **Anchor-derivation gaps** (units not `containedIn` a building in older seeds) — rows without a building token are staff-invisible, not wrongly visible; seeds get the links.
- **Demo exposure** — a hosted staff persona is still gated by the existing demo checklist (Loupe F20 precedent); nothing here changes exposure posture.

## 9. Read-side workplace confinement (F6) — build note (Winston, 2026-07-26)

§3.5 closed the cross-identity read spine for the **Protected/GrantTable** surfaces and F4 closed the
**write** side for café + wellness (`require_workplace` / `worksAt_covers`, `cafe-domain/ddls.go:441-509`,
`wellness-domain/ddls.go:973-1041`). What neither reached is the third surface: café's and wellness's own
read models are **plain open NATS-KV lenses**, so their read boundary lives in the app handler, and that
handler keeps only the *presence* of a `worksAt` anchor and discards its key
(`cmd/cafe-app/readauth.go:77`, `cmd/wellness-app/readauth.go:119`). A staffer wired to one building
therefore reads every building's rows — café's tabs/ledger/front-desk grid and every wellness studio's
roster. No lens projects the join that would narrow it.

**Scope sentence (verbatim from the lane row).** "A `worksAt` anchor to any location anywhere grants the
whole house — café's tabs/ledger/front-desk grid, and every wellness studio's roster. Both packages' staff
*writes* are workplace-confined and Facet's staff *reads* grant-confined; these read paths carry no
workplace term."

**Shape.** Mirror `worksAt_covers` on the read side by projecting, per row, the **set of locations that
cover it** — the row's own location plus every `containedIn` ancestor — and intersecting that set with the
staffer's `worksAt` keys in the handler. This is the read-model analog of the Starlark walk, not a second
semantic: `worksAt_covers` tests the location then walks up; the projected set is exactly that chain
materialized. Precedent for the construct in a lens **Spec** (not just an `AnchorWalk` chain):
`lease-signing/lenses.go:948` projects `[(u)-[:containedIn*1..]->(b:building) | nanoIdFromKey(b.key)]`, and
`service-location/lenses.go:135` matches `*0..` in a spec. `*0..` gives the depth-0 (own-location) entry the
`*1..` form would miss, which is what makes a staffer wired to an exact room match.

An empty covering set **denies** (a row whose topology is unwired is invisible to staff, visible to its own
subject as today) — the same fail-closed answer `require_workplace` gives an empty `location_keys` list.

**Increment order** (each independently green):
1. **Wellness roster** — `wellnessSessionsSpec` gains `coveringLocations`; `subjectHats` keeps the
   workplace keys; `mayReadRoster`'s unconditional `if hats.isStaff { return true }`
   (`cmd/wellness-app/bookings.go:152-155`) becomes an intersection. The instructor branch is untouched —
   it is already row-scoped by `instructorKey`.
2. **Café** — tabs, ledger, and the three front-desk handlers, whose location indirection is the lease's
   unit (`leaseapp_unit`, `cafe-domain/ddls.go:511`); spans `cafe-domain` + `cafe-ledger` lenses.

**Non-goals.** Wellness's `handleSessions` schedule grid stays open — it is the member browse catalog (a
member must see a session to book it), not a staff surface; confining it would break booking. Nothing here
touches the write guards, the Protected/RLS spine, or `operator`.

### 9.1 Increment 1 checkpoint — wellness roster CONFINED (`ad36e9e9`, 2026-07-26)

Shipped: `wellnessSessions` projects `coveringLocations`
(`[(s)-[:locatedAt]->(pl)-[:containedIn*0..8]->(c) | c.key]`, wellness-domain 0.13.0);
`subjectHats` keeps `workplaces` + `isOperator`; `mayReadRoster` intersects. Eleven roster tests,
including the discriminating staff pair (same staffer, two sessions differing only in location), the
containing-location positive, the multi-workplace caller, and the stale-row vector — a projection
carrying no `coveringLocations` key denies rather than waving through.

Two divergences from `worksAt_covers` were found by the adversarial pass and closed rather than noted:
the hop bound now matches `WORKPLACE_MAX_DEPTH`, and `operator` is exempt on the read side as it is on
the write side (`/v1/actor`'s `roles`). Three findings became lane rows instead — the picker UX, the
write-side single-parent walk, and the lens-rebuild question — rather than being widened into this fire.

**Live-verified on the running stack** (`90d823e5`), four ways on one endpoint and — for the last
three — one and the same session, so nothing rests on which row was asked for:

| caller | roles / anchors | session | result |
|---|---|---|---|
| front-desk staffer | `frontOfHouse`, `worksAt` Riverside Building | studio at Riverside | **200** |
| the same staffer | — | studio sitting nowhere | **403** |
| Loupe console actor | `consoleOperator`, no workplace | that same session | **403** |
| root | `operator` (primordial), no workplace | that same session | **200**, roster row returned |

That third row is the one worth keeping: `consoleOperator` is not root, and the exemption correctly
does not reach it. The read models confirm the term independently — showcase studios project
`["vtx.building.A9jnKK2bGwZNrfHHkLme"]`, the studio at a `containedIn`-less unit projects that unit
alone, and the ad-hoc studios earlier PO fires created without a location project `[]`.

The live run is also what caught the exemption comparing against the canonical name `"operator"` while
`/v1/actor` forwards role VERTEX KEYS — inert against a real Gateway, and green in the suite because
the fixture minted the name it was being compared to. Fixed to `bootstrap.RoleOperatorKey` with an
empty-key guard, and `TestMain` now loads the bootstrap ids so the vector runs against the real key.
The lesson generalizes past this fire: a fixture that invents the shape it asserts proves only itself.

**Worktree:** `.claude/worktrees/staff-read-workplace` (branch `fire/staff-read-workplace`).
**Next (increment 2, café):** the same shape over `cafe-domain` + `cafe-ledger` — tabs, ledger, and the
three front-desk handlers. Location indirection is the lease's unit (`leaseapp_unit`), so the covering
comprehension hangs off `(la)-[:appliesToUnit]->(unit)-[:containedIn*0..8]->(c)`. `cmd/cafe-app/readauth.go`
needs the same `workplaces`/`isOperator` change; five handlers gate on `hats.isStaff` today.

### 9.2 Increment 2 fire brief — café (Winston, 2026-07-26)

**Scope sentence (verbatim, lane row).** "A `worksAt` anchor to any location anywhere grants the whole
house — café's tabs/ledger/front-desk grid, and every wellness studio's roster. Staff *writes* are
workplace-confined; these read paths carry no workplace term."

**What the scout changed about the checkpoint.** Two corrections, both narrowing-consistent:

1. The front-desk grid's three lenses live in **`packages/front-desk`**, not `cafe-domain`
   (`front-desk/lenses.go:11,19,31` → `frontDeskBookings` / `frontDeskLeaseDetails` / `frontDeskVisits`).
   The checkpoint said "cafe-domain + cafe-ledger".
2. The count is **seven** `hats.isStaff` gate sites, not five: `tabs.go:120,139` · `leases.go:71` ·
   `residents.go:98` · `ledger.go:124` · `frontdesk.go:60,124,189`. `leases.go` and `residents.go` are
   the POS/front-desk **pickers** the three grid handlers join against by `leaseAppKey` — confining the
   grid while `/api/leases` still returns every building's lease keys would leave the same read open one
   hop away. Same mechanism, same surface; not an adjacent one.

**The unifier.** *Every* café staff read site keys on `leaseAppKey` — `tabSettlementProjection`,
`leaseAccountProjection`, `leaseApplicationProjection.entityKey`, `ledgerEntryProjection`, and all three
front-desk rows. So the workplace term does **not** belong on seven lenses across four packages (which
would also force an edit to the shared `lease-signing` lens). It belongs **once, on the lease**, resolved
per request into a covered-lease set the way `residentOwnLeases` (`residents.go:115`) already resolves the
resident's own set. Every handler then intersects on a key it already carries.

**Shape.** A new `cafeLeaseWorkplaces` lens in **cafe-domain**, one row per `leaseapp`:

```
MATCH (l:leaseapp)
RETURN l.key AS key, l.key AS leaseAppKey,
  [(l)-[:appliesToUnit]->(u)-[:containedIn*0..8]->(c) | c.key] AS coveringLocations
```

It sits in cafe-domain, not cafe-ledger, for two reasons. cafe-domain is the package that already owns
the write-side original this mirrors (`worksAt_covers` `ddls.go:441`, `leaseapp_unit` `ddls.go:511`), so
the two definitions read side by side. And cafe-ledger is separately installable: sourcing cafe-domain's
own read boundary from it would make that boundary depend on a filter declared in another package —
the exact objection `readauth.go:68-76` already raises about `identityAnchors`. Extending the existing
`cafeLeaseAccounts` was the cheaper option and is rejected on those grounds, not on cost.

`*0..` is load-bearing (depth-0 = the unit itself, so a staffer wired to the exact unit matches); the hop
bound is `WORKPLACE_MAX_DEPTH` = 8 (`ddls.go:416`) so neither side reaches a depth the other refuses.

**Verified touch-list.**
`packages/cafe-domain/{lenses.go,lens_cypher_test.go,manifest.yaml,package.go}` (0.7.5 → 0.8.0) ·
`cmd/cafe-app/{readauth.go,tabs.go,leases.go,residents.go,ledger.go,frontdesk.go}` + their tests.

**Increment order** (each green on its own): (a) the lens + cypher tests; (b) `readauth.go` —
`isStaff bool` → `workplaces []string` + `isOperator` + `covers()`, mirroring wellness's shipped shape
incl. `bootstrap.RoleOperatorKey` (the `90d823e5` correction — `/v1/actor` forwards role KEYS, not names);
(c) a `staffCoveredLeases` resolver + the seven gate sites; (d) handler tests.

**Dependencies re-verified both ways.** The Refractor **auto-creates** a nats-kv bucket on lens load
(`cafe-ledger/lenses.go:11`) — no `provision-readpath`, which is a Protected/GrantTable step only.
`appliesToUnit` is the live leaseapp→unit relation (`leaseapp_unit`, `front-desk/lenses.go:102`). The
two-hop `fixed → variable-length` comprehension form is already shipped and cypher-proven by increment 1.

**Fail-closed answer.** An empty covering set denies for anyone but an operator — the same answer
`require_workplace` gives an empty `location_keys`. A lease with no `appliesToUnit`, and a projection
written before this column existed, both decode to an empty set and are refused rather than waved through.

**Non-goals.** `menuCatalog` stays open — it is the member self-order browse catalog, so confining it
breaks self-order (the same call the wellness schedule grid got). No write guard, no Protected/RLS
surface, no `operator` change, no `lease-signing` lens edit, and no change to the resident/self branches.

### 9.3 Increment 2 SHIPPED — café confined, F6 CLOSED (`48a06798`, 2026-07-26)

`cafeLeaseWorkplaces` (cafe-domain 0.8.0) projects each lease's covering locations; `readauth.go`
resolves one `leaseVisibility` per request and all seven read sites — tabs (list + named lease), leases,
residents, ledger, and the three front-desk grid handlers — intersect on the `leaseAppKey` they already
carry. The seven repeated `hats.isStaff` tests collapsed into one rule, which is the structural point:
the workplace term went missing from all seven at once precisely because the test was written seven times.

The rule is `operator ⇒ everything`, else `own ∪ workplace-covered`. Fifteen new handler tests, each a
positive/negative pair on the same endpoint for the same caller, plus six cypher tests on the lens.
Mutating `admits()` to always-true reds 15 of them.

**Three findings from the adversarial pass were fixed rather than noted:**

- **The hop bound was off by one, in the direction of over-grant.** `*0..N` admits depths 0..N inclusive,
  while the Starlark walk's `range(WORKPLACE_MAX_DEPTH)` tests 0..7 — so `*0..8` admitted a staffer nine
  levels up whose writes `require_workplace` refuses. §9.1 recorded this bound as *closed* in increment 1;
  it was not, and the wellness lens shipped with it. Both are now `*0..7` and pinned by a chain built one
  level deeper than either side accepts (`1532a6c5`).
- **A staffer who also rents lost their own lease.** Resolving the staff half *instead of* the resident
  half made two complementary hats mutually exclusive — the opposite of what `require_workplace`'s own
  comment says ("each binds the path the other cannot see"). Now a union.
- **The operator's discriminating sibling test was inert.** It asserted that a non-operator role confers
  no exemption while the fixture could only ever emit the operator key, so the caller under test carried
  *no* roles — and its 403 came from the resident branch, not the role term. The fixture now mints
  arbitrary role keys and the caller holds a workplace. This is the second time this exact fixture shape
  has produced a green-but-inert check (cf. §9.1); a fixture that cannot express the wrong answer proves
  nothing.

**Deliberate asymmetry worth keeping:** the front-desk handlers still tolerate a missing *front-desk*
bucket (that package is an optional cross-vertical join) but 502 on a missing *workplaces* bucket — an
empty grid would read as "nobody is here today" rather than "this app cannot tell who you may see."

**Residual, filed:** the multi-parent divergence now runs the other way. Both read lenses union every
`containedIn` branch; both write-side walks keep only the last parent per level. The union is the correct
half and the single-branch walk is the bug — it keeps the existing `worksAt_covers` lane row, updated.
Unreachable in today's single-parent topology.

## 10. `worksAt_covers` follows every containment parent — fire brief (Winston, 2026-07-26)

**Scope sentence (verbatim, from the lane row):** *the upward walk keeps the LAST non-deleted
`containedIn` target per level and discards the rest, so a location with two parents confines a staffer
wired to the other one out of writes they hold — while both read-side `coveringLocations` lenses union
every branch, the correct half. Denial not leak, unreachable in today's single-parent topology; the sides
should agree.*

**Scope-diff gate.** Narrow-only against §9.3's filed residual, with one correction the row itself got
wrong: the row (and §9.3) name **three** packages. A live grep names **nine copies of the function across
seven packages** — the guard is pasted per *script*, not per package, so wellness-domain alone holds three.
Fixing the three the row names would leave six copies of the same defect and a corpus that disagrees with
itself. The fire covers all nine. Nothing else is in scope: no new confinement, no call-site changes, no
touch to the read side (which is already correct).

**Verified touch-list** (`file:line`, checked live at `e4940151`):

| Site | Constants | `def worksAt_covers` |
|---|---|---|
| `packages/cafe-domain/ddls.go` | 414–416 | 441–468 |
| `packages/cafe-ledger/scripts.go` | 192–194 | 219–246 |
| `packages/clinic-domain/ddls.go` | 1608–1610 | 1635–1662 |
| `packages/clinic-reminders/visitseries.go` | 403–405 | 427–455 |
| `packages/lease-signing/scripts.go` | 203–205 | 230–257 |
| `packages/maintenance-domain/ddls.go` | 265–267 | 292–319 |
| `packages/wellness-domain/ddls.go` (studio) | 806–808 | 833–860 |
| `packages/wellness-domain/ddls.go` (session) | 1086–1088 | 1113–1140 |
| `packages/wellness-domain/ddls.go` (booking) | 1552–1553 | 1555–1582 |

All nine bodies are byte-identical but for comment prose (clinic-reminders' differs; the logic does not).
Fourteen op call sites route through them — cafe OpenTab/Charge/VoidCharge/Settle, cafe-ledger
CreditCafeAccount, clinic CreateAppointment/Reschedule/SetStatus, clinic-reminders Start/ChangeVisitSeries,
lease-signing DecideLeaseApplication, maintenance ReportIssue, wellness CreateStudio/CreateSession/
CreateBooking/CancelBooking — none of which change.

**Precedent to mirror.** The read side is the specification: `[(l)-[:appliesToUnit]->(u)-[:containedIn*0..7]->(c) | c.key]`
(`cafe-domain/lenses.go:106`, `wellness-domain/lenses.go:182,240`) unions every branch to depth 0..7. The
write side must compute the same covering set. `range(WORKPLACE_MAX_DEPTH)` with `WORKPLACE_MAX_DEPTH = 8`
already tests 8 levels = depths 0..7, so the *bound* matches (`1532a6c5` pinned it); only the *breadth*
diverges.

**The change.** The linear `cur = one parent` walk becomes a breadth-first frontier that expands every
non-deleted `containedIn` target, deduped through a `seen` list and capped at `WORKPLACE_MAX_NODES = 64`
distinct nodes in addition to the existing depth and per-level page bounds. Exhausting any bound falls
through to `return False` — a denial, matching how an unresolvable topology already fails closed. A
malformed (non-3-segment) key stops *its own branch* rather than aborting the walk, which preserves today's
denial for the single-candidate case that `wellness-domain/ddls.go:921` relies on.

**This can only convert false denials into allows, and only ones the read side already grants.** In a
single-parent topology every input produces a byte-identical answer, which is why no existing test moves.

**Increment order + green checks.** One increment: all nine bodies, seven version bumps
(`package.go` + `manifest.yaml`), then the new multi-parent test. `go build ./... && make vet &&
golangci-lint run ./... && STRICT=1 go run ./scripts/lint-conventions.go && STRICT=1 go run
./scripts/lint-package-version.go` plus `go test ./packages/...`.

**Gotcha in scope.** Every package whose script text changes needs a version bump or the install no-ops
(`feedback_package_edit_needs_version_bump`) — seven of them, each in two files that a drift test pins.

**Test.** No existing test builds a multi-parent containment topology (verified across
`{cafe-domain,cafe-ledger,wellness-domain}/workplace_confinement_test.go`), so today's suite cannot see
the defect. The fire adds one that wires a unit under *two* buildings and a staffer to the branch the old
walk discarded — red before, green after.

**Non-goals.** Collapsing the nine copies into a shared prelude (its own lane row, `★ M`); any change to
`require_workplace`, `workplace_exempt`, `actor_holds_operator`, or the read-side lenses.

### 10.1 Adversarial pass — the central claim was REFUTED, and the fire grew to answer it

The brief above asserted: *"This can only convert false denials into allows, and only ones the read side
already grants."* A refute-first review falsified it in both directions. Both counterexamples were
verified against the engine before being accepted, and both were fixed rather than noted — the whole
point of the item is *the two sides should agree*, so shipping a change justified by parity while
knowingly leaving a case where parity fails would have been worse than not shipping.

- **The walk transited TOMBSTONED ancestors — new over-permission the read side does not grant.** The
  walk tested `isDeleted` on each `containedIn` *link* but never on the ancestor *vertex*.
  `TombstoneLocation` (location-domain) emits `[make_tombstone(loc_key)]` with **no link cascade**, so a
  decommissioned floor keeps live links and only its own flag marks it gone — while the read side stops
  dead there (`full/executor.go`'s `fetchNode` returns `nil` for a soft-deleted node, and the walk
  `continue`s). So a retired floor still conferred write authority over everything it contained: write
  authority outliving the read it mirrors, and outliving the operator action that revoked it. The defect
  pre-dated this fire for every single-parent chain; following every branch would have made it
  deterministic instead of ordering-dependent. The walk now tests the ancestor vertex too.
- **The node budget converted old ALLOWS into DENIES.** `WORKPLACE_MAX_NODES` truncated the frontier
  before depth 7 in a wide DAG, where the old linear walk always reached level 7 — reintroducing the very
  read/write divergence being closed, in the wide regime. It also over-ran its own bound by up to a page
  (63 + 20 = 83, not 64) because the check sat outside the append loop. The bound is now charged before
  the liveness read, so it holds exactly, and the comment states plainly that the node cap is the one
  bound the read side does **not** share and that exhausting it denies — fail-closed, and set far above
  any real topology.

**What the reviewers could not refute**, recorded so the next author does not re-litigate it: depth
accounting is right (both walks test 0..7, matching `*0..7`); the malformed-key `continue` cannot fail
open (only the caller-supplied seed can be malformed, and at level 0 the frontier holds nothing else, so
it still denies); Starlark semantics are sound and precedented (`x in list`, `list.append` on locals —
starlark-go's post-load freeze does not reach function locals); and the read side remains a superset of
the write side on reachability, tombstones aside.

**Also corrected here:** the pasted comment cited "the read-side lenses" as if all seven packages had
one. Only cafe-domain and wellness-domain project a `coveringLocations` set; maintenance-domain ships no
lenses at all and lease-signing's containment walk is `*1..` (unbounded, excluding level 0). The comment
now names the two that do rather than implying seven. A cypher test in cafe-domain was also still
narrating the old write-side behaviour as a live divergence (a CLAUDE.md no-changelog violation, now
describing the two halves of one rule).

**Test posture, stated honestly.** Three behavioural tests land in wellness-domain — multi-parent
coverage, the tombstoned-ancestor denial, and a cyclic topology — each mutation-tested. The other eight
copies have **no behavioural coverage of their own**; their proof is S10 plus byte-identity. That is the
deliberate trade (one prelude would make it moot — its lane row is open), and it is why S10's own
blind spots got closed in this fire rather than later. One of the three is weaker than it first read: the
cycle test stays green with the visited set deleted, because the *depth* bound is what guarantees
termination — the test comment now says so instead of claiming to pin `seen`.

## 11. The domain resolvers test their hop vertices — fire brief (Winston, 2026-07-26)

**Scope sentence (from the board row, verbatim):** *"The resolvers feeding `require_workplace` its
candidates — `studio_locations`, `session_locations`, `sites_for_provider`, `leaseapp_unit`,
`account_unit` — test only LINK liveness, never the target vertex, and `TombstoneLocation` cascades to no
links. `worksAt_covers` now checks every location vertex itself, so it catches the location case; a
tombstoned studio/provider/unit still resolves for anything else reading them."*

**Why the location case being closed does not close this.** §10 put a vertex test on every node
`worksAt_covers` stands on, so a tombstoned **location** confers nothing. But a resolver's job is to
*produce* those candidate locations, and it produces them by walking through vertices of other types
that nothing tests: a **provider**, a **studio**, a **lease**. Those hops are invisible to
`worksAt_covers` — by the time it runs, the dead vertex has already been transited and only its live
locations remain. The confinement it computes is therefore the dead entity's ex-topology.

**Two of the five holes are reachable by a shipped operation today**, not defensively:

| Resolver | Untested hop | Op that kills it | Effect |
|---|---|---|---|
| `sites_for_provider` (clinic-domain `ddls.go:1801`, clinic-reminders `visitseries.go:518`) | the `provider` vertex, before enumerating `practicesAt` | `TombstoneProvider` (`clinic-domain/ddls.go:1295` — mutations are `[make_tombstone(prkey)]`, no cascade) | a tombstoned provider still returns its sites, so staff at a building it no longer practises at keep authority over its appointments + visit series |
| `session_locations` (wellness-domain `ddls.go:1826`) | the `studio` at the `atStudio` hop | `TombstoneStudio` (DDL doc states "no cascade onto its sessions") | a session at a decommissioned studio still confers that studio's ex-locations |
| `studio_locations` (wellness-domain `ddls.go:1317`) | the `studio_key` itself | `TombstoneStudio` | same, one hop shorter |
| `account_unit` (cafe-ledger `scripts.go:356`) | the `lease` at the `heldFor` hop | none today | defensive |
| `leaseapp_unit` (cafe-domain `ddls.go:580`, lease-signing `scripts.go:369`) | the returned `unit` | none today | defensive for the `require_workplace` consumer (`worksAt_covers` re-reads the unit anyway) but **not** for lease-signing's `require_manages(decide_unit, …)` (`scripts.go:653`), which tests the `manages` link and never the unit vertex |

**The rule, applied uniformly rather than per-site.** Every vertex a resolver *transits* or *returns* is
tested for liveness — not only the ones a tombstone op exists for. `worksAt_covers`' own doc gives the
reason: *"a guard where a dead ancestor confers nothing but a dead starting location confers everything
would be exactly the kind of inconsistency the next reader copies wrongly."* The resolvers are precisely
what the next author copies, so they get the whole rule, and the two defensive sites cost one bounded
read each.

**Precedent to mirror:** `worksAt_covers`' inline vertex test (`node = kv.Read(cur); if node == None or
node.isDeleted: continue`), which exists because a tombstone is a *document*, not an absence —
`step4_hydrate` routes only `ErrKeyNotFound` to `knownAbsent`, so `== None` alone reads a tombstone as
live.

**Shape: one helper, joined to the S10 pin.** Seven inline copies of a security-relevant liveness test
across six packages is the exact shape the previous two fires spent themselves collapsing (`c35eb3be`,
`1946ce92`). So the test lands as `vertex_live(key)` — the standalone, live-KV form, named to sit beside
the existing declared-reads `vertex_alive(state, key)` — and joins `guardHelpers` +
`guardHelperFloors` in `lint-package-standard` so the next copy has to agree with the others. Read
posture is class **(e)**: a per-candidate follow-up read off a bounded `kv.Links` enumeration, the same
posture `worksAt_covers` already annotates for its own vertex read (Contract #2 §2.5).

`worksAt_covers` is deliberately **not** refactored onto the helper: it is digest-pinned at 9 copies, its
inline read sits inside a bounded walk with its own node budget, and rewriting nine pinned bodies buys no
correctness. Its comment gains a pointer to the standalone form instead.

**Direction is fail-closed by construction.** Every change only ever *removes* a candidate from the list
`require_workplace` receives, and an empty list is already a denial for anyone but an operator
(`enforce_workplace`: *"an unwired topology fails closed rather than falling open"*). No branch widens.

**Increment order + green checks.** One increment; the helper and its call sites must ship together.
1. `vertex_live` into the six scripts + the five resolvers' hops (`go build ./...`).
2. `guardHelpers` / `guardHelperFloors` entry (`STRICT=1 go run ./scripts/lint-package-standard.go`).
3. Behavioural tests in wellness-domain (tombstoned studio denies) + clinic-domain (tombstoned provider
   denies), each mutation-tested by reverting the resolver and watching them fail.
4. Package version bumps — an edit at the same version no-ops on a running stack.
5. `make vet`, `golangci-lint run ./...`, `STRICT=1 go run ./scripts/lint-conventions.go`,
   `go test ./packages/...`, `make verify-package-*` for the touched packages.

**In-scope gotchas.** (a) A package edit needs a **version bump** or the install diff-applies nothing.
(b) `sites_for_provider` and `leaseapp_unit` each have two copies — both must change identically even
though neither is digest-pinned, or the next S10 sweep inherits a divergence. (c) The new helper must not
be named `vertex_alive`; that name is taken by the `state`-based form and the two are not
interchangeable — one reads declared `contextHint.reads`, the other live KV.

**Non-goals.** Making `TombstoneProvider` / `TombstoneStudio` cascade onto their links (a no-cascade rule
is deliberate and stated in both DDLs — the readers anchor on the live root); auditing the read-side
lenses for the same hop (they anchor on live roots, and the full engine's `fetchNode` yields nothing for
a soft-deleted node); refactoring `worksAt_covers`.

### 11.1 SHIPPED — and the adversarial pass found a sixth resolver

`6b74deaf`. The brief's scope sentence said five resolvers; the count was wrong, and the
adversarial pass is what caught it.

**`renewal_unit` (`lease-signing/renewal_scripts.go`) has the same shape and the worse consumer.**
It walks `renewal -renews-> leaseapp -appliesToUnit-> unit` and feeds `require_manages` on three
legs — `SetRenewalTerms`, `VerifyGuarantor`, `CancelRenewal`. `WithdrawLeaseApplication` soft-deletes
the leaseapp and *deliberately* leaves its links in place, and a landlord's `manages` link outlives
the application entirely, so a withdrawn application went on authorizing all three. This is worse
than the studio/provider cases because `require_manages` tests the `manages` LINK and nothing else —
there is no downstream `worksAt_covers` to re-read the vertex and catch it. It is fixed here, and the
count in this section's table should be read as **six**.

**Two transited vertices were also missed on the first pass** — the leaseapp hop in both
`leaseapp_unit` copies, which `account_unit`'s sibling in the same diff *did* test. The
cafe-domain copy is live-reachable: a tab's `.status.leaseAppKey` survives the withdraw, and
`Charge`/`VoidCharge`/`Settle` prove only the tab alive.

**A behavioural consequence, stated rather than discovered later.** Retiring an entity now takes its
outstanding work with it: after `TombstoneProvider`, front-desk staff can no longer set status on or
reschedule that provider's remaining appointments; after `TombstoneStudio`, they can no longer cancel
already-sold seats at it. Operators can (they are exempt), and a patient's own self-cancel is
unaffected (it rides `workplace_exempt`). This is the correct direction — a dead entity confers
nothing — but the cleanup affordance is now operator-only, and no cascade was shipped to make it
unnecessary. Filed as its own row rather than decided here: whether retirement should cascade, or
should refuse while work is outstanding, is a product call, and both tombstone DDLs state the
no-cascade rule deliberately.

**Read posture, corrected.** The first annotation claimed class (e) — a data-derived follow-up read —
at all sites. That is false at three of them: `studio_locations`' `studio` and `sites_for_provider`'s
provider (from `CreateAppointment` / `StartVisitSeries`) are payload fields a declared read has
*already* proved live, so there the read is a redundant re-proof, not a data-derived one. The helper
now says so. It is still the right place for the test: a resolver cannot see which caller it has, and
`sites_for_provider`'s fourth caller — the appointment's own link — is exactly the one that needs it.
`lint-conventions` checks that an annotation exists and parses, never that its class is truthful, so
this was not something a gate would have caught.

**Coverage, stated honestly.** Three behavioural tests over the **two** consumers —
`require_workplace` (wellness studio, clinic provider) and `require_manages` (lease-signing withdrawn
application) — each mutation-tested by reverting its resolver and watching the write come back
`accepted`. The other five copies have no behavioural coverage of their own; their proof is S10
byte-identity of `vertex_live` (floor 8) plus the uniform application rule. That is the same trade
§10 made, with the same caveat: S10 pins the helper's body, not which hops a resolver chose to feed
it, so the *application* of the rule rests on review.

**One weak assertion fixed in passing.** `TestLandlord_RenewalOpsConfinedToManagedUnit` checked that
a denied `SetRenewalTerms` had not written `.renewalTerms` — an aspect that op has never written (it
writes `.terms`). The check could not fail. It can now.

## 12. The instructor + serviceprovider hats get their record-administering op — fire brief (Winston, 2026-07-27)

**Scope sentence (from the board row, verbatim):** *"No op in `packages/` declares dispatch class or
targetType `instructor`/`serviceprovider` — wellness targets a `session`, service-domain a `service`
instance — so those two bindings render an inert Facet hat chip while the clinic `provider` hat has three
ops. Either the two domains gain record-administering ops (profile/availability, mirroring
`SetProviderHours`) or the hat surface is clinic-only by design."*

**The fork, resolved (Winston, product call — §0 decide-don't-defer).** The hat surface is **not**
clinic-only by design. The bound-entity spine (persona-worlds W0/W5) exists precisely so a bound entity's
binding renders as a hat whose detail surfaces its record-administering ops; Sam's instructor hat and Kai's
serviceprovider hat are live on the demo login and render inert today. Both domains gain the op.

**Grounding ledger (verified live this fire).**

| Fact | Where |
|---|---|
| No op anywhere in `packages/` declares `TargetType` `instructor` or `serviceprovider` — the 16 declared target types are tab/cafeaccount/provider/appointment/identity/unit/task/session/booking/service | corpus sweep of `Dispatch.TargetType` |
| The chip is inert because `hatOps` requires **both** `dispatchClass === a.type` **and** `dispatchTargetType === a.type` | `cmd/facet/web/app.js:132-138` |
| `instructor` / `serviceprovider` vertex DDL CanonicalNames are exactly the strings the filter needs | `wellness-domain/ddls.go:27`, `service-domain/ddls.go:22` |
| All three bind-ops grant the **same generic `provider` role**, so a grant to `provider` is reachable by a clinic provider too — the in-script standing guard is what confines, not the grant | `wellness-domain/ddls.go:2405`, `service-domain/ddls.go:660`, `clinic-domain/ddls.go:1462` |
| `.profile` is **write-once on both**: its aspect DDL permits only the Create op, so a bound persona cannot correct their own display name | `wellness-domain/ddls.go:589-598`, `service-domain/ddls.go:419-441` |
| The edit has real consumers — `wellnessSessions` projects `instructorName` to members, `wellnessInstructors` to the staff scheduling form, and `edgeIdentity` projects the serviceprovider `displayName` as its own hat-chip label | `wellness-domain/lenses.go:239`, `:98-113`, `edge-manifest/lenses.go:520` |
| Neither target script defines `actor_holds_operator` / `ROLE_PAGE_LIMIT` — both must be added, copied verbatim from the same-package precedent | `wellness-domain/ddls.go:808`, `service-domain/ddls.go:846` |

**Precedent to mirror, exactly: `SetProviderHours`** (not `SetProviderProfile` — that one is operator-only,
has no op-meta and no standing guard, so it is the wrong half of the precedent). Four coupled parts:
op-meta `Dispatch{Class, AuthContext:"standing", TargetField, TargetType}` (`clinic-domain/opmetas.go:144-165`)
· permission `Scope:"any", GrantsTo:["operator","provider"]` (`permissions.go:86-95`) · in-script standing
guard `if not actor_holds_operator(op.actor): if not actor_bound_to_<entity>(...): fail("AuthDenied: …")`
(`ddls.go:1305-1319`) · the `identifiedBy` probe declared read-posture (d) in the dispatcher's
`OptionalReads` (`ddls.go:1208-1221`).

**Touch-list (verified `file:line`).**

- `packages/wellness-domain/`: `ddls.go` (instructor vertex `PermittedCommands` :412 · `instructorProfile`
  aspect `PermittedCommands` :591 · InputSchema/FieldDescription/Examples :427-470 · script branch +
  two guards in `instructorDDLScriptTemplate` :2232) · `opmetas.go` (new meta) · `permissions.go` ·
  `package.go:100` version · `package_test.go` + `opmetas_test.go` structure pins.
- `packages/service-domain/`: `ddls.go` (serviceprovider vertex `PermittedCommands` :371 ·
  `serviceProviderProfile` aspect `PermittedCommands` :425 · schema block :385-415 · `OpMetas()` :126 ·
  script branch + two guards in `serviceProviderDDLScriptTemplate` :524) · `permissions.go` ·
  `package.go:78` version · `package_test.go` structure pins (DDL/perm/meta counts are asserted).

**Increment order + green checks.** Inc 1 wellness `SetInstructorProfile` → Inc 2 service-domain
`SetServiceProviderProfile`. Each: `go build ./...` · `make vet` · `golangci-lint run ./...` ·
`STRICT=1 go run ./scripts/lint-conventions.go` · `go run ./scripts/lint-package-standard.go` ·
`go run ./scripts/lint-package-version.go` · `go test ./packages/<pkg>/...`.

**In-scope gotchas.** (a) A grant to `provider` is reachable by **all three** bound archetypes — without the
standing guard, Osei (clinic) could rewrite Sam's instructor profile; the negative test must use a
*different bound archetype*, not an unbound stranger, or it passes for the wrong reason. (b) `parts_of` is
S10-pinned — the copies must stay digest-identical. (c) A package edit needs a **version bump** or the
reinstall silently no-ops. (d) `.profile` upsert **replaces** the aspect, so `displayName` stays required —
dropping it would null the `instructorName` column the member-facing lens depends on.

**Non-goals (deliberate).** No availability/hours op. The board row offers "profile/availability", but
clinic's hours feed a slot picker that consumes them; wellness has **no** availability machinery — no lens
projects instructor availability and no op enforces it, so an hours aspect would be dead scaffolding with no
nameable consumer. Profile only, both domains, symmetric. No `consumer`-role backfill (its own board row).
**No FE change** — `hatOps` is generic, so the chips go live on the op-metas alone; verify, don't build.

**Shipped** — `0485d145` (wellness `SetInstructorProfile`, 0.17.0) + `2c41318b` (service-domain
`SetServiceProviderProfile`, 0.10.0). Both dispatch `Class = TargetType = <entity vertex DDL>`, which is
what `hatOps` requires, so the two chips resolve with no FE change. Each carries three tests: the bound
persona edits its own record (accept), the same persona on another's record (deny), and — the vector that
matters — an actor holding the identical generic `provider` role bound elsewhere (deny), which is the shape
a bound clinician arrives in.

**Adversarial review — no exploitable bypass.** Attacked key-segment injection through `parts_of` (closed:
3-segment exact match, and step 6 re-validates the NanoID), tombstoned and wrong-class links at the probe
key (no non-operator op can write one), operator-role forgery (all ten rbac ops are operator-only), and
undeclared/mis-declared `optionalReads` (an omitted declaration falls through to a live read and returns the
true answer; a mis-declared key simply misses the hydration cache — there is no way to seed a false
positive). The new ops are strictly tighter than the clinic precedent, which leaves its equivalent probe
undeclared.

Four review findings were fixed before merge: two comments narrating change relative to a prior state
(CLAUDE.md's no-changelog rule), service-domain's aspect-DDL docs left behind its new PermittedCommand,
test envelopes declaring a `.profile` read production does not (so the tests were committing under OCC while
production commits unconditioned), and no test proving the **operator** branch *accepts* — a guard broken to
always-deny would have passed everything. The last is now pinned in both packages.

One finding was filed rather than fixed here: `actor_holds_operator` paged `holdsRole` at 50 and discarded
the cursor, fail-closed above that ceiling — closed 2026-07-27 (`af302004`) across all 15 S10-pinned copies
corpus-wide, following the cursor up to `MAX_ROLE_PAGES=4` pages before denying.

**Not built, deliberately:** no availability/hours op for either domain (nothing reads instructor or provider
availability, so it would be scaffolding with no consumer), and no `consumer`-role backfill — that remains
its own row, and this fire is evidence for how it should go: record-administering self-service rides the
entity `provider` role plus an in-script binding guard, not a `consumer` scope=self grant.

## 13. A pre-existing identity acquires the `consumer` grant the Gateway promises it — fire brief (Winston, 2026-07-27)

**Scope sentence (from the board row, verbatim):** *"`ProvisionConsumerIdentity` is first-touch only, so the
Gateway cannot backfill `consumer` onto a pre-existing seeded identity — only `seedTenant`/`seedLandlord`
grant it. The bound provider / serviceprovider / instructor personas therefore hold their entity role alone
and can reach no `consumer` scope=self grant. Needs a sweep of which self-service ops that actually closes
off."*

**The sweep the row asks for (done this fire).** Twenty ops carry a `consumer` scope=self grant, and a bound
persona holding only the generic `provider` role can reach **none** of them: `ClaimIdentity`,
`InitiateCredentialLink`, `CompleteCredentialLink`, `UnlinkCredential` (identity-domain) · `CreateAppointment`,
`RescheduleAppointment`, `SetAppointmentStatus` (clinic) · `CreateLeaseApplication`,
`WithdrawLeaseApplication`, `DecideLeaseApplication`, `SetApplicantProfile`, `SetRenewalTerms`,
`VerifyGuarantor`, `CancelRenewal` (lease-signing) · `SetListingStatus` (loftspace) · `CreateBooking`,
`CancelBooking` (wellness) · `OpenTab`, `Charge`, `Settle` (café). §12 established that a bound persona's
*record-administering* op rides the entity role plus a binding guard — this row is the complement: everything
a bound persona does **as a resident/patient/member of the building**, which is exactly the `consumer` plane.

**Grounding ledger (verified `file:line` this fire).**

| Fact | Where |
|---|---|
| The already-exists branch returns a blanket no-op — the sole blocker; nothing else in the op is gated on first touch | `packages/identity-domain/ddls.go:809-815` |
| The Gateway submits for **any** verified actor; its `provisioned` set is a bounded latency cache whose false miss "just re-runs the idempotent op", so correctness never depended on skipping | `internal/gateway/gateway.go:139-166`, `:628-631` |
| The single runtime dispatcher declares `Reads:[consumerRoleKey]` + `OptionalReads:[actorID]` — no link key | `internal/gateway/gateway.go:664-667` |
| The premise is already documented in-tree as a workaround: a seeded persona "must be granted it here" via `AssignRole` | `scripts/seed-showcase.go:1191-1194` |
| All three bind-ops grant the entity `provider` role only, never `consumer` | `clinic-domain/ddls.go:1447-1462`, `service-domain/ddls.go:795-817`, `wellness-domain/ddls.go:2491-2514` |
| Bound personas are minted `CreateUnclaimedIdentity`, so they sit at `state=unclaimed` and are never claimed — the grant must **not** gate on claim state or it would exclude the exact personas at issue | `scripts/seed-showcase.go:854-858`, `:925-929`, `:1046` |
| `identity.provisioned` has no `meta.ddl.eventType` entry and none is required for emission (the §3.4 validator is a no-op) | `packages/identity-domain/revocation.go:113-118` |

**Precedent to mirror: `AssignRole`'s `grant_link`** (`rbac-domain/ddls.go:164-175`, `:352-374`) — the
three-state grant read off the hydrated snapshot: alive → empty mutations, tombstoned → `revive_link` update,
absent → create. Reviving needs the tombstoned revision, which is why the deterministic link key must be a
caller-declared `optionalReads` entry (`revive_link`'s own comment, `:149-158`).

**The one deliberate divergence from that precedent — the grant is absent-only, never revive-aware.**
`AssignRole`'s revive branch exists because an **operator** explicitly re-grants. Here the caller is an
automatic per-request pre-flight, so reviving would mean a `RevokeRole` on `consumer` is undone by the
revoked actor's very next request — a revocation that silently does not hold. Tombstoned therefore stays
tombstoned: absent → create, alive → no-op, tombstoned → no-op. (`RevokeActor`'s token kill-switch is a
different mechanism and already refuses the request upstream at 403; this protects the *role* revocation.)

**Why the declaration is load-bearing, not hygiene.** Without the link key in the read set, an already-granted
actor hydrates it as *absent*, the script emits a `create`, and create-only conditioning asserts revision 0
against a live key → `RevisionConflict` on every authenticated request. Fail-closed (no wrong grant is ever
written) but it would turn a silent no-op into permanent op churn, so the dispatcher declaration and the
script branch ship together. The existing
`TestProvisionConsumerIdentity_AlreadyProvisioned_Idempotent` declares no `ContextHint` and therefore
**must** be updated in the same increment — its failure is the proof the declaration carries weight.

**Touch-list (verified `file:line`).**

- `packages/identity-domain/ddls.go` — hoist the `consumerRoleKey` literal pin + role-vertex read above the
  existence check (`:824-833`), then replace the blanket no-op (`:809-815`) with the three-state ensure;
  op-meta `ExpectedOutcome` (`:205-207`) states "Already-provisioned actor: no-op"; `package.go:33` version.
- `internal/gateway/gateway.go:664-667` — add the deterministic
  `lnk.identity.<actorId>.holdsRole.role.<roleId>` to `OptionalReads` (class (d): a first-touch actor
  legitimately has none, so never `Reads`).
- `scripts/seed-showcase.go:1182-1198` — the comment asserts a seeded persona "can never acquire the role
  that way"; it must describe what the code does now, and the explicit `AssignRole` still earns its keep for
  a persona that never signs in.
- Tests: `packages/identity-domain/provision_test.go` (backfill onto an identity holding another role ·
  revoked-grant-stays-revoked · declare the link in the idempotent test) ·
  `internal/gateway/gateway_test.go` (pin the `OptionalReads` entry so a refactor cannot silently drop it).

**Increment order + green checks.** Inc 1 script + dispatcher + tests (one unit — they are correctness-coupled
per the paragraph above). `go build ./...` · `make vet` · `golangci-lint run ./...` ·
`STRICT=1 go run ./scripts/lint-conventions.go` · `go run ./scripts/lint-package-standard.go` ·
`go run ./scripts/lint-package-version.go` · `go test ./packages/identity-domain/... ./internal/gateway/...`.

**In-scope gotchas.** (a) Do **not** gate on `state`: the personas are `unclaimed` (ledger above), so a
claimed-only guard would silently exclude every one of them. (b) Do gate on the identity vertex being alive —
a tombstoned identity must not acquire a role. (c) The backfill branch may return
`primaryKey = <link key>` only because the link is in its own write footprint; the pre-existing branch
returned no response precisely because `targetActorKey` is not. (d) A package edit needs a version bump or
the reinstall silently no-ops. (e) The role key stays pinned to the package literal — the payload field is
never trusted, since `operator` may also call this op.

**Non-goals (deliberate).** No new role, no change to which ops grant `consumer`, no change to
`ClaimIdentity` (it already grants at claim time), and no removal of `seed-showcase`'s explicit `AssignRole`
(a seeded persona that never authenticates still needs it). Not a widening of who may call the op: the
grant's subject is always the authenticated actor the Gateway verified.

**Shipped** — `5c914479` (identity-domain 0.10.0). The already-exists branch ensures the
`holdsRole → consumer` grant instead of returning a blanket no-op, so the Gateway's promise now holds for
every actor it verifies rather than only for identities it minted. Absent → create, present → no-op, and a
**tombstoned grant stays tombstoned**: `AssignRole` revives because an operator is explicitly re-granting,
whereas this caller fires on every request, so reviving would mean a `RevokeRole` is undone by the revoked
actor's next request. A tombstoned identity acquires nothing. Five tests: the bound persona gains the grant
without being re-provisioned (state stays `unclaimed`, the entity role survives), the revoked grant is not
revived, the dead identity gains nothing, re-provision leaves a live grant alone, and — the one that pins a
design choice rather than a behavior — an already-granted actor no-ops even with the consumer role vertex
tombstoned, because the role-liveness read sits inside the granting branches.

**The adversarial pass earned its keep — it found a brick, not a nit.** `ClaimIdentity` grants the *same*
deterministic link key with an unconditional `create`, which asserts revision 0 and fails the whole atomic
claim batch if the link exists. Since the pre-flight now creates that link on any authenticated touch, a
single sign-in by an unclaimed persona would have left the person holding its claim secret **permanently
unable to claim it** — silently, and only in the dev/nanoid-mode sign-in the demo actually uses. The
collision was already real: `seed-showcase` avoids the real ceremony in three places *because of it*
(`seedTenant`, `seedStaff`, `seedLandlord`). Fixed by making that grant an **upsert**, which needs no
declared read — an `update` conditions on the revision the key is read at in step 8, or commits
unconditioned when genuinely absent (`internal/processor/step8_commit.go:191-215`). A three-state read was
rejected as unviable: `ClaimIdentity`'s dispatchers include browser clients that cannot compute the
deterministic consumer-role key a declared read would require. Regression-tested, and the test was
mutation-checked — it fails with the `create` form restored.

**Two claims were verified by breaking them on purpose,** because both are the kind of assertion that reads
as boilerplate and silently rots: dropping the `optionalReads` declaration fails
`AlreadyProvisioned_Idempotent` (proving the declaration is load-bearing), and hoisting the role read back to
the top fails the role-vertex-health test. Neither test passes for free.

**Residual, filed as its own row:** the seeded showcase personas remain `unclaimed` forever — the seeder
flips `.state` via `UpdateIdentityState` because it cannot run the device-credential ceremony — so nothing
in the demo world exercises the real claim path end-to-end, and the collision this fire fixed was therefore
invisible to every gate. `TestClaimIdentity_ConsumerGrantAlreadyHeld_Succeeds` covers it at the package
level; the live path has no walker. **Superseded by §13.1 — the walker now exists, and found a second,
distinct brick this residual predicted but could not see.**

## 13.1 The live path has a walker now, and it found a live capability-projection gap (Winston, 2026-07-27)

**Scope sentence (from the board row, verbatim):** *"Nothing walks the real claim ceremony"* — built
`scripts/verify-claim-ceremony.go` / `make test-claim-ceremony`, mirroring `verify-real-actor-write-auth.go`:
mints a staff-minted unclaimed identity, mints a brand-new RS256 dev token for a never-before-seen device
subject, and drives `ClaimIdentity` through the real Gateway (retrying through the known
`isTransientAuthLag` race `cmd/facet/claim.go` already handles), then a second never-before-seen device
replaying the same claim.

**The walker works, and it found a real, reproducible gap the unit-level regression above cannot see.**
Confirmed live, twice, with different NanoIDs each time:

| Fact | Evidence |
|---|---|
| The claim itself succeeds: `.state` → `claimed`, `.claimKey` tombstoned | `verify-claim-ceremony.go` assertions, both runs |
| The R2 grant link (`lnk.identity.<target>.holdsRole.role.<consumer>`) lands correctly in Core KV — `isDeleted:false`, correct source/target | `lattice graph read`, both runs |
| `cap.roles.<target>` (or `cap.identity.<target>`) is **never created** — `NotFound`, still absent minutes later, **after** a completed `capabilityRoles` sweep pass (`sweepLastPassAt` postdates the write) | `lattice query cap`, `lattice lens lag` |
| It is not a Terminal failure reaching the DLQ — `REFRACTOR_DLQ_CAPABILITYROLES` (or any `REFRACTOR_DLQ_*`) stream does not exist | `nats stream ls` against the live stack |
| The device credential's OWN grant (written by `ProvisionConsumerIdentity`, same request, same link shape) projects correctly and promptly every time | `verify-claim-ceremony.go`, both runs |

**Leading hypothesis (grounded, not confirmed — needs the live NATS-level check below).** §13's own fix
made this exact write a genuinely **unconditioned** `Put` — `internal/processor/step8_commit.go:191-215`,
`prior[m.Key].Found == false` for a first-ever grant, so `HasRevision` stays `false` — the *first live
exercise* of that code path against a real Refractor (the unit-level regression tests it against
`testutil`'s harness pipeline, not the production CDC/lens-fanout path). That unconditioned member still
rides inside the SAME atomic multi-key batch as the conditioned `.state`/`.claimKey` writes, published via
NATS's atomic-batch headers (`Nats-Batch-Id`/`-Sequence`/`-Commit`, `internal/substrate/batch.go:335-360` —
per `docs/vendors.md`, atomic batch is NATS 2.12+, our pin 2.14, no ADR number on file for it yet). Refractor's
own fan-out dispatch was verified (read-only, this fire) to key purely on KV key-shape + body, never
op-type, and JetStream stream-sequence ordering is monotonic — so a **batch-commit vs per-subject-watch
delivery gap specific to an unconditioned member of an atomic batch** is the lead suspect. **Ground this in
the pinned nats-server 2.14 source / `nats-io/nats-architecture-and-design` ADRs before touching code**
(CLAUDE.md's vendor rule) — do not guess at a fix.

**Not filed as a residual — filed as its own row** (this is what the walker was built to catch, and it
caught it): see `lattice.md` `[Refractor] A live claim's own consumer grant never projects into Capability
KV`. Reproduce with `make test-claim-ceremony` against a running `make up-full-capability` stack.
