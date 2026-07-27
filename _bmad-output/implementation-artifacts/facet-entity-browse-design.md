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
