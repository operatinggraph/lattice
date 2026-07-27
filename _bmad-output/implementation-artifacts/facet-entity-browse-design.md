# Facet entity browse — giving `dispatch.targetType` something to resolve against

**Status:** ✅ SHIPPED 2026-07-19 · `7341ad73` — built per §4 and live-verified end-to-end: the showcase tenant browsed the Nearby view, opened Vinyasa Flow, and the booking committed through the real Processor (booking vertex + forSession link + seat claim in Core KV). One naming deviation: the row's schedule instant is `startsAt`, not `when` — WHEN is a CASE keyword in the engine's lexer. (Ratified 2026-07-19, interactive; v1 scope widened at ratification: clinic **providers included** alongside sessions (F2) — the demand trigger F2 waited on arrived with the design itself.)
**Board row:** [verticals.md](../planning-artifacts/backlog/verticals.md) — "Facet has no browse surface for bookable entities" (★★ / M)
**Extends:** [edge-showcase-app-design.md](edge-showcase-app-design.md) §3.2, §3.3, §4.2 — and reconciles its §8 non-goal (below).

## 1. The problem, precisely

Wellness's "Book a class" renders as a degraded card, not a button. The cause is not a bug and not a
permission — it is a missing *noun*.

`CreateBooking` declares `Dispatch{TargetField: "session", TargetType: "session"}`
([wellness-domain/opmetas.go:45-51](../../packages/wellness-domain/opmetas.go)). The renderer gates on it at
[cmd/facet/web/app.js:540-542](../../cmd/facet/web/app.js):

```js
if (d.dispatchTargetField && !resolveTargetKey(d, ctx)) { return `<div class="degraded-card">…` }
```

`resolveTargetKey` ([app.js:1048-1070](../../cmd/facet/web/app.js)) tries `ctx.entityKey` → `ctx.scopedTo` →
`ctx.serviceKey` → `me().identityKey` → `selfAnchorKey(want)`. For `want === "session"` every branch misses:
no manifest row carries a `vtx.session.*` key, and `session` is not a self-anchor type
(`edgeIdentity` collects only `leaseapp`, [edge-manifest/lenses.go:213](../../packages/edge-manifest/lenses.go)).

The code already names the seam — [app.js:1040-1042](../../cmd/facet/web/app.js):

> `ctx.entityKey` is the seam a browse surface fills in (open a session, then "Book a class" resolves);
> **nothing populates it yet**, which is precisely why those ops read as unofferable rather than broken.

So the work is: **project browsable entity rows into the manifest, and let the renderer set `ctx.entityKey`
from one.** The descriptor half is already shipped and correct — nothing about dispatch needs changing.

## 2. Why this could not just be built (the grounding that closes the obvious route)

The obvious implementation is "reuse the residence spine": `edgeServices` reaches service templates via
`identity -residesIn-> loc -containedIn*0..-> container <-availableAt- (tpl:service)`. Extend the same walk to
studios, get sessions one hop further, done.

**That route is closed.** `availableAt`'s source is restricted to a live **service template** by
`WireAvailableAt` ([service-location/ddls.go](../../packages/service-location/ddls.go); the restriction is
restated at [service-location/lenses.go:75](../../packages/service-location/lenses.go)), and the `(svc:service)`
class filter on the `capabilityServiceAccess` cypher exists specifically to hold if "a non-service vertex were
ever wired an `availableAt` edge" ([lenses.go:91](../../packages/service-location/lenses.go)). `availableAt` is
not a generic "is located at" relation — it is an **authorization-bearing** edge feeding the `cap.svc.<actor>`
grant projection. Widening its source to admit studios would quietly widen service-access authZ. That is not a
lens change in the Verticals lane; it is a security-relevant change to a shared cross-vertical package.

Two further constraints found while grounding, both of which shape the answer:

- **A studio has no location link at all.** `studioVertexTypeDDL` mints `vtx.studio.<NanoID>` with root `{}` and
  a `.profile` aspect; the only wellness link into it is `session -atStudio-> studio`
  ([wellness-domain/ddls.go](../../packages/wellness-domain/ddls.go)). There is nothing to walk *from* the
  actor's residence *to* a session today.
- **A clinic provider already is location-anchored** — `provider -practicesAt-> building`
  ([clinic-domain/lenses.go:446](../../packages/clinic-domain/lenses.go)). The two verticals the board row names
  are therefore in *different* states, which matters for scoping (§4).
- **The lens engine has no `UNION`** and no cross-branch list comprehension (stated repeatedly in
  [edge-manifest/lenses.go:66-69, 358-367](../../packages/edge-manifest/lenses.go)). Independent `OPTIONAL MATCH`
  branches cross-product. A lens that emits **one row per entity** (unlike the read-grant lens, which collects
  into arrays) therefore cannot carry two unrelated entity kinds in one cypher without emitting ambiguous rows.

## 3. Forks and resolutions

### F1 — How does a bookable entity become reachable from the actor? **(recommended: B)**

- **A. Widen `availableAt` to non-service sources.** Rejected. It is the authZ edge behind `cap.svc.<actor>`
  (§2); widening it to make a *browse* surface work trades a security boundary for a rendering convenience.
  The manifest "affects visibility, never permission" ([edge-showcase-app-design.md](edge-showcase-app-design.md)
  §4.5) — this option inverts that.
- **B. A per-vertical location link, owned by the vertical.** wellness-domain gains
  `studio locatedAt location` (its own link DDL, its own op param on `CreateStudio`), carrying no authZ meaning.
  Reachability becomes `residesIn → containedIn*0.. → container <-locatedAt- studio <-atStudio- session`.
  Stays entirely in the Verticals lane, touches no shared package, and leaves `availableAt` exactly as it is.
  Link name passes the Contract #1 sentence test ("studio locatedAt location") and direction is correct (the
  studio is the later-arriving source, the location pre-exists).
- **C. Reach sessions through the service template that already permits the op.** Attractive — it needs no new
  link — but there is no `service → session` relation, and inventing one makes the *service* catalog carry
  instance data. Rejected as a worse-shaped version of B.

### F2 — One generic entity lens, or one per kind? **(ratified: one lens per kind; v1 ships sessions AND providers)**

The no-`UNION` constraint (§2) means a single cypher covering both sessions and providers would cross-product
them, so each kind gets **its own lens**, in the same style this package already uses for its other narrowings
(`edgeCatalog` covers only the service-`permitsOperation` path; `edgeTasks` only direct `assignedTo`). v1 ships
**both kinds** — `edgeEntitySessions` and `edgeEntityProviders` — the provider walk being cheap because
`practicesAt` already exists and needs no DDL work. (As drafted this fork deferred providers "when demanded";
Andrew widened v1 at ratification, the demand having arrived with his clinic report.)

The generic vocabulary is in the **row shape**, not the cypher: `manifest.ent.<id>` rows carry
`{entityKey, entityType, title, subtitle, when}`. The renderer browses by `entityType` and never learns what a
session is — it matches `entityType` against `dispatchTargetType` and sets `ctx.entityKey`. Adding a kind is a
lens, not a renderer change. That is what keeps this from being a wellness feature wearing a generic name.

### F3 — Does this violate §8's "it is not a graph browser"? **(Winston adjudication: no)**

[edge-showcase-app-design.md](edge-showcase-app-design.md) §8 non-goals Facet as "a graph browser". Reading that
as forbidding this surface would be a misread, and the distinction is worth stating so it is not relitigated:

- A **graph browser** navigates arbitrary vertices and relations — the operator surface Loupe owns, and the
  thing §8 is refusing.
- **This** is a lens-declared, reachability-bounded, *typed* row set — structurally identical to `manifest.svc`
  (service templates the actor can reach) and `manifest.inst` (instances provided to the actor), which §8
  plainly does not forbid. It exposes exactly the entities a **declared `dispatch.targetType` already needs**,
  and nothing else. No new visibility is created that the descriptor vocabulary did not already imply.

This is a non-contract design call and is resolved here as lead. §8 stands unamended; this doc is the record of
where its boundary falls.

### F4 — The Refractor D1 gate (not a fork — a trap to not fall into)

`manifest.ent` rows anchor on a vertex **other than** the recipient identity, so they are subject to Refractor's
`readableAnchors` fail-closed gate. Without a matching anchor entry in `cap-read.edgeManifest.<actor>`, every row
is **silently dropped** — no error, an empty browse view, and a plausible-looking "the lens must be wrong" wild
goose chase. This is documented as the exact bug that made Fire 1's lenses invisible
([edge-manifest/lenses.go:8-25](../../packages/edge-manifest/lenses.go)). `edgeManifestReadGrants`
([lenses.go:368-382](../../packages/edge-manifest/lenses.go)) must gain the session anchor in the same commit as
the lens.

## 4. Build decomposition

One fire, in this order (each step's failure is diagnosable before the next is attempted):

1. **wellness-domain — `studio locatedAt location`.** Link DDL + optional `location` param on `CreateStudio`
   (validated alive + `class=location`, mirroring how `CreateSession` validates `studio`). No cascade.
2. **Seeds.** `seed-classic-demo` + `seed-showcase` locate their studio in the showcase world, or the browse view
   is correct and empty. (A studio with no location is legal and simply un-browsable — the right default.)
   Providers need no seed work if the clinic seeds already wire `practicesAt` — verify, and wire it if absent.
3. **edge-manifest — the entity lenses.** `edgeEntitySessions`: `IntoKey: ["__actor","ns","entityId"]`,
   `ns = "manifest.ent"`, anchored on the session, row `{entityKey, entityType: 'session', title, subtitle, when}`.
   `edgeEntityProviders`: same row shape with `entityType: 'provider'`, anchored on the provider, reached
   `residesIn → containedIn*0.. → container <-practicesAt- provider`. In both, `entityType` is a **literal
   stamped per walk**, exactly as `selfAnchors` stamps its type
   ([lenses.go:186-198](../../packages/edge-manifest/lenses.go)), never parsed from the key.
4. **edge-manifest — `edgeManifestReadGrants`** gains the session **and provider** anchor branches (§3 F4).
   Same commit as 3.
5. **cmd/facet/web — the browse view.** A sixth view listing `manifest.ent` rows grouped by `entityType`;
   selecting one sets `ctx.entityKey` and renders that entity's offerable ops through the **existing**
   `opButton`/`resolveTargetKey` path. No change to dispatch resolution — this only feeds it.
6. **Verify live** on the showcase stack: "Book a class" turns from a degraded card into a working form, and the
   booking lands in Core KV (not just an FE toast — the standard set by the café self-service proof, §7.9).

Gates: `go build ./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run ./scripts/lint-conventions.go`,
`go test ./packages/... ./cmd/facet/...`, `make verify-package-wellness-domain` + `verify-package-edge-manifest`
(DDL/keys touched), plus the `cmd/facet/web/*.test.mjs` node vectors for the renderer half.

**Hot-reload note:** steps 1–4 edit existing DDLs and add a lens — both diff-apply on a live stack via F-004
(`make reinstall-package`). Step 1 adds a **link** DDL, not a new entity, so it does not need a fresh bootstrap.

## 5. What this deliberately does not do

- Does not touch `availableAt`, `capabilityServiceAccess`, or any service-access authZ (§3 F1).
- Does not make Facet a graph browser (§3 F3): only entities a declared `dispatch.targetType` names are ever
  projected, bounded by the actor's own residence reachability.
- Does not change the descriptor vocabulary. `targetType` shipped in `dda7ad98` and is already correct.

## 6. Build note — Inc 4's descriptors get their nouns (2026-07-26)

**Scope sentence** (verbatim, [verticals.md](../planning-artifacts/backlog/verticals.md)): *"`resolveTargetKey`
matches `TargetType` against projected entity types + selfAnchors; the context carries no `tab`, `patient`,
`visitseries`, `task` or `studio`, so the Inc 4 descriptors degrade instead of offering a button. `ClaimTask` is
one line — `ctx.taskKey` is already populated but is not among the candidates. The others need the entity
projected or an anchor declared."* Coupled in the same function, so the same fire: *"`identity` targetType
resolves to the submitter, not a degrade."*

This is §3 F2's "one lens per kind" applied to two more kinds. Nothing here is a new mechanism.

**Verified touch-list** (`file:line` read live at `a472fc9e`):

| Where | What |
|---|---|
| [app.js:1635](../../cmd/facet/web/app.js) | candidate list — `ctx.taskKey` absent, so `ClaimTask`'s `targetType: "task"` never resolves though the task row already carries it |
| [app.js:1638-1641](../../cmd/facet/web/app.js) | the `want === "identity"` fallback to `me().identityKey` — the degrade gate at [app.js:793](../../cmd/facet/web/app.js) can never fire for that type |
| [lenses.go:151-205](../../packages/edge-manifest/lenses.go) | the three `manifest.ent` lens specs to mirror |
| [lenses.go:596-680](../../packages/edge-manifest/lenses.go) | their tails; `edgeEntityBookings` is the precedent for an inherently-private (own-link, not locality) row set |
| [coverage_proof_test.go:127-208](../../packages/edge-manifest/coverage_proof_test.go) | the resident + staff worlds a new lens must be seeded into, or its coverage claim is vacuous |
| [package_test.go:45-58, 70, 176-183](../../packages/edge-manifest/package_test.go) | lens-name set, the 17-lens count pin, the `manifest.<ns>` pin |
| [package.go:20](../../packages/edge-manifest/package.go) · [manifest.yaml](../../packages/edge-manifest/manifest.yaml) | version bump + the declared-lens list (a same-version edit no-ops on install) |

**Precedents to mirror.** `edgeEntityBookings` for a private own-link walk (`(identity)<-[:bookedBy]-`);
`edgeStaffWorkOrders` for the workplace spine (`(identity)-[:worksAt]->(work)<-[:containedIn*0..]-(place)`);
`edgeEntityProviders` for a row shape with **no `startsAt`** — [app.js:259-264](../../cmd/facet/web/app.js)'s
`isUpcoming` hides a time-anchored row once its instant passes, and neither a tab nor a studio is a scheduled
thing.

**Increment order, each with its own green check:**

1. **`resolveTargetKey`** — add `ctx.taskKey` to the candidate list; delete the `identity` fallback so an
   unresolvable identity target degrades like every other type. `RecordIdentityPII` is the only op declaring
   `targetType: "identity"` ([identity-domain/opmetas.go:135](../../packages/identity-domain/opmetas.go)) and it
   is offered from a task whose `scopedTo` **is** the identity, so the candidate loop still resolves it;
   `ClaimIdentity` is already pinned as declaring no `targetType`
   ([identity-domain/package_test.go:193-202](../../packages/identity-domain/package_test.go)). Green:
   `node --test cmd/facet/web/`.
2. **`edgeEntityTabs`** — `(identity)<-[:applicationFor]-(la:leaseapp)<-[:openFor]-(tab:tab)`, base grant domain,
   open tabs only (a presentation narrowing; the walk grants regardless, keeping grant ⊇ projection). Resolves
   café `Charge` / `Settle` / `VoidCharge`. Green: `go test ./packages/edge-manifest/...`.
3. **`edgeEntityStudios`** — the `edgeStaffWorkOrders` workplace spine with `<-[:locatedAt]-(studio:studio)`,
   staff grant domain. Resolves wellness `CreateSession`. Green: same.

**In-scope gotchas.** The engine has no `UNION`, so a second kind is a sibling lens, never another branch
(§3 F2). `entityType` is a literal stamped per walk — the engine has no vertex-type-from-key function. A new
lens without its read-grant branch is silently dropped by D1 (§3 F4); the `AnchorWalk` declaration is what
generates that branch, and `coverage_proof_test.go` is what proves it.

**Non-goals (and why).** `patient` and `visitseries` stay unresolved. A `manifest.ent` row for a patient would
put a patient's display name on the broadcast SYNC plane, which is exactly what D3 forbids and what
`6b1c667c` ("patient names out of open clinic nats-kv lenses") deliberately undid; `visitseries` hangs off the
patient and inherits the question. That is a design call about clinical reachability on the edge plane, not an
execution step — it stays filed, not freelanced here.

### 6.1 As built (`c84d1eb5`)

Both lenses shipped as scoped, with three corrections the adversarial review forced.

**The tab grant grows without bound, and the grant side cannot narrow with the projection.** The tail filters
to open tabs; the walk cannot, because a status lives on an *aspect* and an `AnchorWalk` chain expresses node
patterns only. `Settle` flips `.status.value` and leaves the tab alive rather than tombstoning it, so unlike
`edgeEntityBookings` — whose cancelled rows self-clear because the vertex tombstones — `edgeManifestReadGrants`
keeps granting every tab a resident ever opened. The generated producer cross-products its branches before
`collect(DISTINCT …)`, which is the fan-out `ReadGrantDomainSpec`'s own doc names as the reason domains are
split. Grant ⊇ projection still holds and nothing is over-read; what grows is the producer's row count per
resident. Filed as its own row rather than papered over here — the fix is either a terminal-state tombstone in
cafe-domain or an aspect predicate the walk vocabulary does not have, and both are decisions, not edits.

**The newly-reachable tab ops failed closed for a two-lease resident.** `Charge` and `Settle` carry their
ownership probe in `dispatch.optionalReads`, and `unresolvableSelfAnchor` scanned only `contextParams` — so an
unanswerable `{me.leaseapp}` substituted a hole, `wholeKey` dropped the malformed key, the probe was never
declared and the script refused the visitor's own tab with an `AuthDenied` they could do nothing about. An
optionalRead is absence-tolerant of the *key*, not of the *template*; both are now scanned, with the `:id`
rendering modifier stripped rather than read as part of the anchor type. This was pre-existing but only became
reachable when the tab got a browse row, which is exactly when it became this fire's problem.

**`ClaimTask` does not flow through this path yet, and the build note above overstated it.** `ctx.taskKey` is
the correct candidate and `openTaskDetail` populates it, but that surface offers the task's own
`forOperationKey`, and `ClaimTask` is never a task's `forOperation` — it is submitted by the hardcoded
`claimTask()` affordance, which works. The candidate is right and currently unexercised; its consumer is a
task-detail surface that offers task-targeted ops, filed as its own row.

**Scope actually discharged.** `tab` (café `Charge` / `Settle` / `VoidCharge`) and `studio` (wellness
`CreateSession`) now resolve. `patient` and `visitseries` remain, for the D3 reason stated above. Of the café
trio only `Settle` and `VoidCharge` are drivable end-to-end from Facet today: `Charge` also requires a
`menuItemKey`, and no lens projects menu items, so it renders as a free-text vertex key — filed.

**Live on the dev stack.** `edge-manifest` 0.11.0→0.12.0 diff-applied in place (12 created, 5 updated, no
teardown); Refractor registered both lenses and projected them, the install-time backlog drained (798→0 and
547→0) and the `CapabilityCoverageDivergence` the two re-shaped producers raised while re-converging 45 + 131
actors cleared with it — Refractor back to `healthy`, both lenses `alert: ok`. The showcase tenant's live
manifest carries 1016 `manifest.ent` rows (sessions, providers, bookings) and **no tab row**, which is the
correct answer: all three of that lease's tabs are settled, and the tail projects open ones only.

A *positive* tab row was not demonstrable live, and the reason is the data, not the code: every identity in
this stack holding an open tab is a PO-created café walk-in with no Facet control grant
(`personal.register: actor lacks the control grant`) or no showcase provisioning, and the one provisioned
tenant's tabs are all settled. The positive path — own open tab projects, a neighbour's does not, a settled
one clears — is proven deterministically against the real engine in
`TestEdgeEntityTabs_ProjectsOnlyTheActorsOwnOpenTab`.

## 7. Build note — the menu item becomes a noun the form can pick (2026-07-26)

**Scope sentence** (verbatim, [verticals.md](../planning-artifacts/backlog/verticals.md)): *"`Charge` resolves
its tab now, but its other required field is a `vtx.menuitem.<NanoID>` and no lens projects menu items, so the
descriptor form renders a free-text vertex key nobody can type. `Settle` + `VoidCharge` drive end-to-end;
`Charge` does not. Either a `menuitem` browse lens off the café's location or an enum sourced from the
catalog."*

**The fork the row left open, resolved: the browse lens, not the enum.** A JSON-Schema `enum` is a
compile-time closed set — every one of the ~30 enums under `packages/` is a status/decision vocabulary. A café
catalog is *data*, minted and retired by operators at runtime, so an enum would either be stale the moment an
item is added or would require regenerating a package version per menu change. The lens is also the option
that generalizes: the identical gap sits on `RescheduleAppointment{provider, patient}`
([clinic-domain/opmetas.go:90-97](../../packages/clinic-domain/opmetas.go)), which renders two raw vertex keys
as free text today for exactly the same reason.

**What the row's own premise turned out to under-state (grounded, not substituted).** Two facts make this
larger than "add a lens", and both are *inside* the scope sentence rather than adjacent to it:

1. **A menu item has no links at all.** `CreateMenuItem`'s mutation list is `make_vtx` + `make_aspect` and
   nothing else ([cafe-domain/ddls.go:1013-1016](../../packages/cafe-domain/ddls.go)) — a bare
   `vtx.menuitem.<NanoID>`. Every `manifest.ent` lens is an `AnchorWalk`, and the walk grammar admits only
   rooted linear relationship patterns with a named relation
   ([internal/pkgmgr/anchorwalk.go:693-763](../../internal/pkgmgr/anchorwalk.go)). So "a browse lens off the
   café's location" is not expressible until the location edge exists. Minting it is the first increment, not
   a separate item.
2. **`menuItemKey` is not `Charge`'s dispatch target — `tabKey` is.** So the noun does not arrive through
   `resolveTargetKey`; it arrives through the per-FIELD widget. That widget already exists as a stub:
   `renderField` branches on `schema["x-entityRef"]` ([app.js:1545-1548](../../cmd/facet/web/app.js)) but
   discards the declared vertex type, and `onGlobalInput` sources candidates from a hardcoded
   `[...services(), ...instances()]` ([app.js:1858-1861](../../cmd/facet/web/app.js)). No op-meta anywhere in
   `packages/` sets `x-entityRef` today, so the branch has never run. Making it type-driven is what connects
   the new lens to the field.

**Verified touch-list** (`file:line` read live at `cccc581c`):

| Where | What |
|---|---|
| [cafe-domain/ddls.go:1004-1019](../../packages/cafe-domain/ddls.go) | `CreateMenuItem` branch — gains a required `locationKey`, a live+class check, and the `servedAt` link |
| [cafe-domain/ddls.go:217-259](../../packages/cafe-domain/ddls.go) | the `menuitem` vertexType DDL — description, InputSchema, FieldDescriptions, Examples all name the new field |
| [maintenance-domain/ddls.go:239-244,467-469](../../packages/maintenance-domain/ddls.go) · [service-location/ddls.go:258-263](../../packages/service-location/ddls.go) | the two precedents for validating a caller-supplied location key — `require_live_typed` / `require_live_location`; mirror, do not invent |
| [cafe-domain/ddls.go:762](../../packages/cafe-domain/ddls.go) | `OpenTab`'s `make_link` — the in-file precedent for link direction + key shape |
| [cafe-domain/opmetas.go:98-99](../../packages/cafe-domain/opmetas.go) | `Charge`'s self-slice InputSchema — `menuItemKey` gains `"x-entityRef": "menuitem"` |
| [edge-manifest/lenses.go:172-191](../../packages/edge-manifest/lenses.go) | `edgeEntityProviders` — the exact mirror: residence spine, one hop down to a thing at the place, `domainBase` |
| [edge-manifest/lenses.go:404](../../packages/edge-manifest/lenses.go) | `chainResidence`, shared by textual identity — the new walk must reuse the constant or the producer stops factoring the prefix |
| [edge-manifest/package_test.go:51,69-70,186](../../packages/edge-manifest/package_test.go) | lens-name set · the hardcoded lens COUNT (19→20) and its doc comment · the `ns` literal map |
| [edge-manifest/coverage_proof_test.go:127-208](../../packages/edge-manifest/coverage_proof_test.go) | the resident world the new lens must be seeded into, or its coverage claim is vacuous |
| [app.js:1545-1548,1786-1794,1850-1864](../../cmd/facet/web/app.js) | the entity-ref widget: render · pick · candidate source |
| [app.js:844-858](../../cmd/facet/web/app.js) | `renderBrowse` — see the F3 note below |
| [app.js:868-876](../../cmd/facet/web/app.js) | `entityMeta` already renders any `*Cents` column as money — so the lens projects `priceCents` and the price needs no renderer change |
| [scripts/seed-classic-demo.go:146-149](../../scripts/seed-classic-demo.go) | both `CreateMenuItem` calls pass `nil` ContextHint — they must now declare the `locationKey` read (Contract #2 §2.5) |

**F3 re-checked, and one line added to keep it literally true.** §3 F3 ratified that browse "is not a graph
browser: only entities a declared `dispatch.targetType` names are ever projected." No op targets `menuitem`, so
a menu-item row would appear in Browse under a "Menu items" heading with "Nothing to do here yet." — the exact
graph-browser drift F3 forbids. The lens is still right (`x-entityRef` is a declared vocabulary term the client
resolves against, the same as `targetType`), so the principle widens on the *vocabulary* side and `renderBrowse`
gains the filter that F3 always described: render a type only when some op's `dispatchTargetType` names it.
The rows stay in state for the picker; they stop being a browse category.

**Increment order + green check per step**

1. **cafe-domain** — required `locationKey`, `require_live_typed`-style guard, `menuitem servedAt location`
   link, DDL prose/schema/examples, version 0.8.4→0.9.0, `createMenuItem` test helper + the 5 tests built on
   it, both seed calls. → `go test ./packages/cafe-domain/ ./scripts/...`
2. **edge-manifest** — `edgeEntityMenuItems` (walk `chainResidence` + `(container)<-[:servedAt]-(item:menuitem)`,
   `domainBase`) + tail, the four `package_test.go` pins, a dedicated coverage-proof test, manifest.yaml +
   package.go + the file's own lens-count prose, version 0.12.0→0.13.0. → `go test ./packages/edge-manifest/`
3. **Facet FE** — `renderField` carries the declared type onto the element; `onGlobalInput` sources from
   `entitiesByType(type)` (services/instances stay the source for those two types) and offers candidates on an
   empty query so a two-item catalog is pickable without guessing a search term; `renderBrowse` filter.
   → `node --check`, then live.
4. **Gates** — `go build ./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run ./scripts/lint-conventions.go`,
   `lint-lens-anchors`, `lint-package-standard`, `lint-package-version`, `make verify-package-cafe-domain` /
   `-edge-manifest`, `go test ./...`.

**Non-goals.** No change to `menuCatalog` or `cmd/cafe-app` — that app's own picker already reads the KV read
model and is unaffected (`menu.go:54-55`, a public catalog). No per-field `Sensitive`. No `CreateMenuItem`
op-meta (operator-only, trusted-tool). No backfill: menu items minted before this change carry no `servedAt`
link and simply will not browse until re-minted — named here rather than papered over, and the demo seed
re-mints them.

**Adjacent finds, filed now rather than at admit.** `docs/components/edge-manifest.md:14-15` still says "six
lenses" and names none of the entity lenses, against its own header rule that the page updates in the same
commit as the code — pre-existing drift, filed as its own row. The `RescheduleAppointment{provider, patient}`
raw-key fields ([clinic-domain/opmetas.go:90-97](../../packages/clinic-domain/opmetas.go)) become buildable the
moment increment 3 lands, and are filed as the mechanism's second consumer.

### 7.1 As built (`87010105`)

Three increments landed as briefed. Review changed the code in two places and settled a third as posture.

**The key shape was wrong in the docs and in the fixtures.** The brief and the first cut both said
`locationKey` holds a `vtx.location.<NanoID>`. location-domain mints `vtx.{unit,building,property}.<NanoID>`
with `class=location` — the level is the key's TYPE segment, the class is invariant
([location-domain/ddls.go:31-33,75](../../packages/location-domain/ddls.go)). So the descriptor advertised a
shape production never produces, and `seedLocation` was proving the link key against that same fiction: the
`servedAt` link real callers mint is `…servedAt.unit.<id>`, which no test had ever seen. Docs and fixtures now
name `vtx.<locationType>`, and the class check (not the type segment) is what the script enforces — which is
why `parts_of` is called with an empty `want_type`, mirroring service-location.

**A picked entity-ref survived being typed over.** The value submitted lives in a hidden input, which the
browser excludes from constraint validation. Pick "Latte", then type "Croissant" without picking, and the form
submitted `menuItemKey: <Latte>` under the label "Croissant" — the wrong item charged, with nothing on screen
to show it. Typing now clears the hidden value, so the required-field guard catches the diverged state instead
of the server rejecting a submission whose failing control the visitor cannot see.

**`servedAt` is a presentation bound, not an authorization one — stated, not fixed.** `Charge` accepts any
live `menuitem`, and `menuCatalog` is a global read model any signed-in session may list
([cafe-app/menu.go:54-55](../../cmd/cafe-app/menu.go)), so a resident who reads another building's item key can
charge it to their own tab. This predates the fire, but the fire is what makes the picker *look* like a guard,
so `require_menu_item_price` now says plainly that it is not. Confining the write needs the item's `servedAt`
target, which `Charge`'s payload cannot derive, and "may a resident order from a café they do not live at" is a
product question before it is a mechanism — filed as its own row rather than answered here.

The base cap-read producer's anchor-type pin ([composed_test.go:219](../../packages/edge-manifest/composed_test.go))
failed on the first run, exactly as designed: `menuitem` joins `tab` as a resident-reachable anchor, declared
rather than absorbed. The trace that matters for a walk appended to a shared producer — that no existing
branch's clauses or variable bindings change, and that `collect(DISTINCT …)` keeps a new trailing OPTIONAL
MATCH from inflating any prior branch's set — was checked against `generateProducerSpec`'s factoring and
renaming, and holds because the walk is appended last and `item` is a name no other `domainBase` walk binds.

**Live.** cafe-domain 0.8.4→0.9.0 and edge-manifest 0.12.0→0.13.0 diff-applied in place (10 updated; 6 created
+ 5 updated), Refractor loaded `edgeEntityMenuItems` with no divergence, and `bin/{facet,cafe-app,
loftspace-app,loupe}` were rebuilt from the merge and cycled against the still-running stack. The POSITIVE
picker path is not demonstrable on this stack and the reason is the data, not the code: every seeded menu item
predates `servedAt` and so carries no link, exactly the non-goal named above — the demo seed re-mints them
with one. That path is proven deterministically against the real engine in
`TestEdgeEntityMenuItems_IsBoundedByTheResidenceChain`, the same posture §6 took for the café tab row.

## 8. Build note — a tab's two relations get their two lifetimes (2026-07-27)

**Scope sentence.** Bound the `edgeEntityTabs` read grant to a resident's *currently open* tabs by splitting
the tab→lease relation into the two facts it has been conflating: a permanent `chargedTo` the settlement lens
anchors on, and a transient `openFor` that `Settle` retracts.

**The defect.** `edgeEntityTabs`' walk is `(identity)<-[:applicationFor]-(la:leaseapp)`,
`(la)<-[:openFor]-(tab:tab)` ([edge-manifest/lenses.go:218-227](../../packages/edge-manifest/lenses.go)), and
`Settle` leaves the `openFor` link standing ([cafe-domain/ddls.go:901-955](../../packages/cafe-domain/ddls.go)).
So the compiled grant producer collects every tab the lease has *ever* held, for the lease's whole life, while
the presentation tail filters to `status = "open"`. Grant ⊇ projection still holds — there is no over-read —
but the `cap-read.edgeManifest.<actor>` slice this resident appears in grows without bound, and each entry
rides the producer's cross-branch fan-out. §6's own comment names this and says bounding it is a lane item
rather than something to fake with a key-list; this is that item.

**Why the obvious two fixes are both wrong.** *Narrow the grant side* is inexpressible: a tab's status lives on
an ASPECT and a `Walk.Chain` clause is a node pattern
([pkgmgr/anchorwalk.go:40-45](../../internal/pkgmgr/anchorwalk.go)). *Retract `openFor` at `Settle`* is a money
bug: `cafeTabSettlement` opens `MATCH (t)-[:openFor]->(l:leaseapp)` as a REQUIRED match and reads the lease's
`cafeLedgerAccount` guard off it ([cafe-domain/lenses.go:146-157](../../packages/cafe-domain/lenses.go)), so a
settled tab would project no row at all, `EmptyBehavior: "delete"` would drop the target, and Weaver would
never dispatch `CreateAccount` / `DebitAccount` — the convergence fires exactly when the link would vanish.
Tombstoning the tab VERTEX fails the same way and for the same reason.

**The shape.** One link was carrying two facts with different lifetimes, which is why neither could be fixed
without breaking the other:

- `tab chargedTo leaseapp` — permanent. Minted by `OpenTab`, never retracted; the ledger spine
  `cafeTabSettlement` anchors on. Mirrors the `account heldFor lease` anchor the three ledgers already mint
  ([cafe-ledger/scripts.go:105](../../packages/cafe-ledger/scripts.go)). Direction per Contract #1 §1.1 — the
  later-arriving tab is the source.
- `tab openFor leaseapp` — transient, keeping its honest name. Minted by `OpenTab`, tombstoned by `Settle`, so
  the walk's reach *is* the open set and the tail's `WHERE` becomes belt-and-braces rather than the only bound.

This is the link form of a lifecycle the package already runs: the `cafeOpenTabGuard` aspect is claimed by
`OpenTab` and released by `Settle` over and over across a lease's life
([cafe-domain/ddls.go:182-204](../../packages/cafe-domain/ddls.go)). Two links between one pair is not a
denormalized index — both are first-class relations, and neither stores a key in an aspect.

**Increment order + green checks.**

1. `cafe-domain` script — `make_link_tombstone` (mirroring
   [clinic-domain/site.go:233](../../packages/clinic-domain/site.go): `op: update`, full envelope,
   unconditioned, same as `Settle`'s existing guard release); `OpenTab` mints `chargedTo` alongside `openFor`;
   `Settle` tombstones `openFor`. Both link keys are derivable from `tab_key` + the `.status`-denormalized
   `leaseAppKey`, so no new declared read. → `go test ./packages/cafe-domain/`
2. `cafeTabSettlement` re-anchors on `chargedTo`; `lens_cypher_test` fixtures seed both links, plus a new case
   proving a settled tab with `openFor` retracted still converges. → same package test
3. `edge-manifest` — the Walk is UNCHANGED; only `edgeEntityTabsTail`'s comment, which currently documents the
   unbounded growth as accepted. `coverage_proof_test` gains the grant-side half: a tab whose `openFor` is gone
   is not in `readableAnchors`. → `go test ./packages/edge-manifest/`
4. Version bumps + `manifest.yaml`; `make verify-package-cafe-domain`.

**In-scope gotchas.** A tombstoned link envelope removes both directional adjacency entries
([refractor/consumer/bootstrap_test.go:261](../../internal/refractor/consumer/bootstrap_test.go)), which is
what makes retraction actually drop the hop. `Settle` derives `lease_id` only inside its `authContextTarget`
branch today — the link key needs it unconditionally.

**Known data boundary, not a defect.** A tab opened before this version carries no `chargedTo`, so its
settlement stops converging. `Charge` / `Settle` / the resident surfaces are unaffected (they read `.status`,
not links). Recoverable by a reseed, which the demo box already does nightly — the same class as the seed items
already on the lane, and not worth a legacy dual-match that would preserve the conflated shape forever.

**Non-goals.** No walk-grammar change, no aspect-reachable grant narrowing, no key-list index, no linkType DDL
(`openFor` has never declared one, and this fire is not the place to change that convention for one package).

### 8.1 As built (`7a52a673`)

Shipped as briefed, with two narrowings and one adjacent fix.

**No new Starlark helper was needed.** The brief planned to add `make_link_tombstone` mirroring
[clinic-domain/site.go:233](../../packages/clinic-domain/site.go). The package's existing generic
`make_tombstone(key)` already works on a link key — `rbac-domain/ddls.go:385` and
`service-location/ddls.go:319` both call it exactly that way — and the adjacency removal derives source and
target from the six-segment key rather than the envelope, so the full-envelope form buys nothing here. One
call, no helper.

**`Settle` now derives `lease_id` through `parts_of` rather than `.split(".")[2]`.** The link key needs it
unconditionally, and the raw split only existed inside the `authContextTarget` branch. Routing it through the
S10-pinned helper means a malformed `leaseAppKey` on the tab's own `.status` fails closed instead of
constructing a garbage key — it cannot happen (OpenTab validates before writing it) but the guard is free.

**Every test was verified to fail against the previous hop before being trusted.** Reverting the lens to
`openFor` fails `SurvivesTheOpenForRetraction` ("should have 1 item(s), but has 0"),
`OpenForAloneDoesNotAnchor` (projects a row it must not), and the four `mkTab`-driven settled cases. The
grant-side test was falsified the other way — re-adding an `openFor` edge to the settled tab makes it fail —
so it pins the hop, not the fixture.

**A masking defect surfaced during that falsification and was fixed.** Six cases indexed
`f.projectAt(t, …)[0]`, which panics on an empty projection and aborts the whole test binary: the first
falsification run reported ONE failure when six were real, and the missing five looked like passes.
`valuesAt` requires exactly one row instead, so a dropped projection reports its own cause and the rest of
the package still runs.

**The retraction transport was checked, not assumed.** A shrinking grant only works if Refractor re-projects
the producer when a link is tombstoned — an upsert-only path would leave the withdrawn anchor standing, which
is the over-grant failure mode this whole change is about. It holds, and is already e2e-guarded platform-side:
`refractor_capability_linkfanout_e2e_test.go:257-281` drives both an `isDeleted: true` overwrite and a
physical `DEL` and asserts the capability doc shrinks and is ultimately deleted.

**Blast radius checked.** `edgeEntityTabs` and `cafeTabSettlement` are the only two lenses anchored on `tab`,
nothing else in `packages/` walks `openFor`, and no lens uses an untyped or variable-length hop that a new
`chargedTo` edge could widen (the walk grammar forbids untyped hops outright —
[anchorwalk.go:693-708](../../internal/pkgmgr/anchorwalk.go)). edge-manifest's composed spec is byte-identical;
its 0.13.0→0.13.1 bump exists only because `lint-package-version` counts any non-test, non-markdown diff, which
is the correct conservatism given lens specs live in string literals.

**Adjacent find, filed.** `packages/cafe-domain/README.md` claims one vertex type and one aspect type and
names neither `menuitem`/`menuItemPrice`, `cafeOpenTabGuard`, `menuCatalog`, nor `cafeLeaseWorkplaces` — it
predates three shipped fires. Only the two link lines this change falsified were corrected; the rest is filed
as its own row rather than folded into a security-adjacent diff.

## 9. Build note — the D3 non-goal reopened: `patient`/`visitseries` close through the Protected pane, not the mirror (2026-07-27)

**Scope sentence** (verbatim, [verticals.md](../planning-artifacts/backlog/verticals.md), now in the Done
log): *"`StartVisitSeries` (`patient`) and Pause/Resume (`visitseries`) still degrade: a `manifest.ent` row
for a patient puts a patient's name on the broadcast SYNC plane, which D3 forbids, and a visit series
inherits it."* §6's non-goal called this "a design call about clinical reachability on the edge plane... it
stays filed, not freelanced here." It was filed blocked, but never actually filed as a `lattice.md` primitive
request — re-grounding it found the "new posture" it was waiting on already exists.

**The re-grounding.** D3 itself (`display-name-convention-design.md` §0) names the channel for
other-identity display: "Protected-lens territory," not a new mechanism. `facet-staff-worlds-design.md` F3
(shipped 2026-07-19/20, *before* this item was ever marked blocked) built exactly that for Facet — a
session-scoped, RLS-confined `GET /api/staff/worklist` pane (`cmd/facet/staff.go`, mirroring
`credentials.go`), reading Protected Postgres tables that never ride the mirror. The read model this needed
was already shipped too: `visitSeriesRead` ([visitseries.go](../../packages/clinic-reminders/visitseries.go)),
serving both patient-self and (operator-only) staff views in `cmd/clinic-app`. Two gaps remained, both
mechanical extensions of F3's own pattern, not new design: `visitSeriesRead`'s `authz_anchors` carried only
the patient anchor (no building token, so `frontOfHouse` — who already holds the three ops' permission —
couldn't see a target to act on), and the Protected pane was read-only (no row had ever driven an `opButton`).

**What shipped.** `visitSeriesReadSpec`'s `authz_anchors` gains the identical comprehension
`clinicAppointmentsReadSpec` already carries (`[nanoIdFromKey(p.key)] +
[(pr)-[:practicesAt]->(b:building) | nanoIdFromKey(b.key)]`) — additive, patient-self and operator reads
unchanged, proven by a leak-check test (a provider-less series never picks up an unrelated series' building).
`cmd/facet/staff.go`'s worklist query gains a third read (`read_visit_series`) inside the same transaction,
plus a `patient_key` column on the existing appointments read. `app.js`'s worklist rows now supply
`ctx.entityKey` straight into the *existing* `opButton`/`resolveTargetKey` seam the mirror browse view
(`openEntityDetail`) already used — no new dispatch mechanism, just a second row source feeding it.
`StartVisitSeries`'s `providerKey` field also gained `x-entityRef: "provider"` (`edgeEntityProviders` already
projects providers — they're public directory data, not PII) so the form the new button opens is fillable,
not just offerable.

**Adversarial review (3 independent passes) found and this fixed one real defect.** `interval_days` and
`active` were scanned into non-nullable Go fields; the lens's `MATCH (s:visitseries)` admits a vertex whose
`.series`/`.progress` haven't landed (theoretically, between mint and the atomic aspect-write reaching the
projection — never observed live, but the vertex+`forPatient`-link state is reachable by the schema), and a
NULL there would fail the whole row's `Scan` — taking the applications and schedule sections down with it,
not just degrading the series one. `cmd/clinic-app/visitseries.go`'s own reader of this same table already
`COALESCE`s exactly these columns; `staff.go` now matches it. An `entity_key` `ORDER BY` tiebreaker was added
for the same reason (same precedent). Two more findings were traced to ground truth and are **not** defects:
a forged `data-entity-key` cannot bypass the unchanged `enforce_workplace` write-path guard
(`TestFrontDesk_VisitSeries_ForgedTargetCannotSkipConfinement` already proves it; this diff touches no
write-path code), and a NULL-bound `pr` in the anchor comprehension fails closed by construction
(`internal/refractor/ruleengine/full/executor.go`'s `nullBindNewVars`/`matchPath`), not by scanning the
keyspace.

**Known residual, filed rather than fixed here.** `active` fuses "not explicitly paused" with "not past
`activeUntil`" — a series that reached its natural end (never paused) reads `active=false` and now renders a
"Resume" button that succeeds but changes nothing observable. Pre-existing in the shared formula (also true
of `cmd/clinic-app`'s patient-self view today), newly *reachable* now Facet renders a button on it. Fixing it
well means projecting a raw `paused` (or `activeUntil`) column and a three-state client badge — a small but
separate decision from "make the trio resolve at all," filed as its own row (verticals.md).

**Resolved** (Winston, 2026-07-27, verticals.md "A naturally-ended visit series still shows a working Resume
button"): `visitSeriesReadSpec` (`clinic-reminders/visitseries.go`) replaces the fused `active` boolean with a
raw `series_status` text column — `"active" | "paused" | "ended"`, precedence sequential (paused is judged
first, so a series paused before it would have ended still reads "paused", never "ended"). `cmd/clinic-app`'s
`renderMySeriesCard` now offers Pause/Resume only for `"active"`/`"paused"` and neither for `"ended"`; the
`clinic-reminders` Pause/Resume op-metas' `dispatch.visibleWhen` moved from `{field:"active"}` to
`{field:"series_status"}` so the SAME fix reaches Facet's staff worklist pane (`edge-manifest`
`panes.go`'s `visitSeries` section) with no app change, exactly as the descriptor-driven design intends.
clinic-reminders 0.7.2, edge-manifest 0.14.7.

**Non-goals.** No change to the mirror (`manifest.ent`/`edge-manifest`) — `patient` still can never ride it,
by design, permanently. No change to `RescheduleAppointment`'s free-text `patient` field
([verticals.md Done log](../planning-artifacts/backlog/verticals.md), `0badf04e`) — `x-entityRef` pickers only
ever read the mirror, so this fix's worklist-row-specific channel doesn't generalize to them; a generic
Protected-pane-backed picker widget would be new mechanism, not filed here.

**Superseded in mechanism, 2026-07-27 (facet-discovery-restoration-design.md).** The behavior above stands,
but its implementation moved out of app code: the third read is now a section of the `staffWorklist` pane
DESCRIPTOR (`packages/edge-manifest/panes.go`), executed by `cmd/facet`'s generic pane executor; the
Pause/Resume pairing is expressed as `dispatch.visibleWhen {active}` on the two op-metas
(`packages/clinic-reminders/visitseries.go`) rather than op-name lookups in `app.js`. `staff.go` no longer
exists; `scripts/lint-facet-discovery.go` blocks its reintroduction.

Gates: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`, `STRICT=1 lint-conventions`,
`lint-lens-anchors`, `lint-package-standard`, `lint-package-version` all clean; `go test
./cmd/facet/... ./packages/clinic-reminders/...` and the full `node --test cmd/facet/web/*.test.mjs` (127
vectors) green; `make verify-package-clinic-reminders` — 47/47 assertions — against the live shared stack.
