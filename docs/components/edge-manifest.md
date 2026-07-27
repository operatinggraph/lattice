# edge-manifest

**Component reference** | Audience: operators + implementers

> `edge-manifest` is a **Capability Package** (`packages/edge-manifest`), not a platform engine — it has
> no frozen interface contract of its own. Its framing of record is
> `_bmad-output/implementation-artifacts/edge-showcase-app-design.md` §3 (✅ Andrew-ratified) and the
> *Edge-manifest + personal-lens consumer* row of `_bmad-output/planning-artifacts/backlog/lattice.md`.
> Update this page in the same commit as the code; drift between page and code is a documentation bug.

---

## Overview

`edge-manifest` is the world manifest the Facet edge app (design §4) renders from: six lenses
(`packages/edge-manifest/lenses.go`) — five **Personal Lenses** plus the `edgeManifestReadGrants`
capability-read lens that makes them deliverable — re-projecting data other packages already own —
identity, orchestration-base's tasks, service-domain's templates/instances, service-location's residence
graph — into the reserved `manifest.` key namespace, delivered per-actor over the shared
`lattice.sync.user.<actor>` SYNC transport (the `nats-subject` Personal Lens adapter, `edge-manifest
Fire 0`). It declares no DDLs and no permissions: every row is a read-side re-projection of state another
package's DDL already authored.

It is the **first production package** to use the `nats-subject`/Personal Lens adapter — that plumbing
shipped latent in Fire 0 (proven only by inline e2e tests, `internal/refractor/personal_lens_pl*_e2e_test.go`)
with zero real `packages/*` consumers until this one.

## The lenses (row schemas)

All rows carry the reserved `manifest.` key prefix (`internal/edge/store.go`'s `ApplyUpsert`/
`ApplyDelete` carry a matching exemption from the Contract #1 key-shape gate for this prefix — a
`manifest.*` key is a **projection-row key, not a Core-KV key**, the same posture `my-tasks.*` rows
already have on the nats-kv side).

| Lens | Key | Anchors on |
|---|---|---|
| `edgeIdentity` | `manifest.me` | the actor's own identity — display name, claimed status, roles, residence anchors |
| `edgeServices` | `manifest.svc.<tplId>` | service templates reachable via the actor's residence → `containedIn*` → `availableAt` chain |
| `edgeCatalog` | `manifest.op.<opMetaId>` | op metas reachable via a reachable service template's `permitsOperation` link; carries `viaServices` (Fire 2), the list of service keys that permit it — a pattern comprehension mirroring `service-location/lenses.go`'s `allowedOperations`, not a WITH/collect grouping stage |
| `edgeTasks` | `manifest.task.<taskId>` | tasks directly `assignedTo` the actor and still open |
| `edgeInstances` | `manifest.inst.<instId>` | service instances `providedTo` the actor ("my orders") |
| `edgeManifestReadGrants` | `cap-read.edgeManifest.<actor>` (nats-kv, `capability-kv`) | the D1 read-grant half (Contract #6 §6.14): projects each actor's readable manifest anchors so the fail-closed `readableAnchors` gate publishes the four non-self-anchored lenses — without it only `manifest.me` ever leaves the Refractor |
| `edgeStaffPanes` | `manifest.pane.<paneMetaId>` | pane meta-vertices reachable over `holdsRole` → `offeredTo` (facet-discovery-restoration-design.md §2.1): `{paneId, title, icon, sections}` where `sections` is a JSON string of section descriptors — the Protected server-pane's tables/columns/filters/dispatch targets as DATA. The client discovers panes by this prefix; the host's generic executor re-reads the same row from its own mirror and compiles the RLS-confined reads |

Vocabulary additions riding the op rows: `dispatchVisibleWhen` (`{field, equals}`, nullable) gates
OFFERING an op against the resolved target row's state — the state-machine-pair seam (pause/resume)
that previously forced clients to branch by op name. `manifest.ent` rows (and both session-typed hat
lenses) carry `typeLabel`, the per-type display word the renderer's label ladder consumes instead of
a hardcoded client-side type map.

This page + `lenses.go` are the normative as-built row shapes (design §3.2's JSON is the semantic
reference — as-built rows flatten its nesting, per its 2026-07-16 amendment; the `vocab` stamp is not
yet projected and activates at the vocabulary freeze). See design §3.3 for
the descriptor-vocabulary fields `edgeCatalog` reads back off each op meta's optional
`.presentation`/`.inputSchema`/`.fieldDescriptions`/`.dispatch`/`.sensitive` aspects (`pkgmgr.OpMetaSpec`,
`edge-manifest Fire 1 increment 1`) — an op meta that never adopted the vocabulary still projects a row,
just with those fields null (design §3.3: "ops without descriptors still render, degraded").

## v1 scope-downs (named, not silent)

The `full` cypher engine has no `UNION`, no list comprehension, and no string concatenation (`+` is
numeric-only) — which bounds how many independent reachability paths a single lens can dedup into one
row set without a bespoke multi-branch `collect(DISTINCT …) + collect(DISTINCT …)` per path. Three
narrowings follow, each a reasonable v1 cut rather than a correctness gap in what IS built:

- **`edgeIdentity`'s `roles`/`anchors` arrays** carry only `{key, …}` — no human-readable location
  type/label (there is no vertex-type-from-key function beyond `nanoIdFromKey`, and no string
  concatenation to synthesize one from the key's type segment).
- **`edgeCatalog`** covers only the service-`permitsOperation` reachability path. Role-standing-grant ops
  (Contract #6's permission table, not graph data) and open-task-`forOperation` ops are deferred — a
  task's own bound op already rides inline on its `edgeTasks` row, so the gap is "browse all my ops," not
  "complete my assigned task."
- **`edgeTasks`** covers only direct `assignedTo` tasks. FR28 role-queued tasks (`queuedFor` a role the
  actor holds) are deferred, mirroring the same multi-path-dedup limit.

A degenerate `collect(DISTINCT {…})` entry (e.g. `{key:null,name:null}` when an identity holds no role)
is expected, not a bug — the renderer obligation is the same one `my-tasks.*` rows already carry (design
§3.2): treat a null-keyed entry as absence and drop it client-side.

## Status

**Fire 0 + Fire 1 (incl. `make seed-edge-demo`'s demo topology) + Fire 2 shipped.** Structural install
verified (`make verify-package-edge-manifest`) and every lens's cypher parses under the real
`ruleengine/full` engine (`packages/edge-manifest/package_test.go`, including the Fire 2 `viaServices`
addition). The live projection e2e — a seeded tenant actually receiving all five row kinds over
`lattice.sync.user.<actor>` and completing the full write path — is proven in-browser against `cmd/facet`
(`make up-facet`); see `facet-app-ux.md` §5 for the walkthrough. `make seed-edge-demo` now also claims the
seeded tenant (`ClaimIdentity`, submitted as the tenant itself) so `manifest.me.claimed` is true from the
first hydrate, per `facet-app-ux.md` §3.0.
