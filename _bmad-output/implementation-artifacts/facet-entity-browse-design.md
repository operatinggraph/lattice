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
