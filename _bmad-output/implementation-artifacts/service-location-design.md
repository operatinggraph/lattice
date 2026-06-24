# service-location + location-domain — design (rev. 3)

**Status:** design (pre-build). **Owner:** Winston (lead). **Source brief:** `packages/service-location/CONCEPT.md`.
**Builds to (FROZEN, never edited):** Contract #6 §6.1 / §6.5 / §6.8 / §6.9 / §6.10 / §6.12 / §6.13; Contract #1 §1.1.

## 0. Revision history

- **rev.1 → rev.2** — a 2-lens adversarial design review found a never-tested §6.10 over-grant (the
  multi-level-exclusion cypher pinned the matched location), a backwards `availableAt` direction, a wrong
  `Bucket` value, and an undeclared `service-domain` collision. All fixed; see the cypher (§3) + §4.
- **rev.2 → rev.3 (this rev, with Andrew)** — confirmed the auth model is faithful (service-location is the
  *location* grant scheme, one of three union'd sources; not an auth redesign). Two decomposition decisions:
  **(a)** `availableAt` moves out of `service-domain` (it's an availability *assertion* / auth-topology, not
  a catalog property, and is dormant today — written, read by nothing); **(b)** a new **`location-domain`**
  base package owns the spatial model, mirroring `identity-domain`. So this is now **two new packages + a
  small `service-domain` refactor**. Per-operation availability overrides (§6.10 item 3) are **deferred**
  (Andrew) — v1 ships service-level availability + the operation list, full stop.

## 1. Goal

Fill the reserved-but-empty `cap.svc.<actor>` keyspace — the **location** grant scheme — completing Epic
12's "packages own their projections" decomposition. service-location is a capability-contribution package
exactly like `rbac-domain` (`cap.roles.*`) and `orchestration-base` (`cap.ephemeral.*`): the Processor's
step-3 check reads one disjoint key per operation; the three grant sources *union* into an actor's
capability and never intersect (Contract #6 §6.10 item 4). It is the residence-based service-access model
for the Loftspace vertical.

## 2. Decomposition — a clean DAG of three base domains + one access scheme

| Layer | Package | Owns | Deps |
|---|---|---|---|
| Base — *who* | `identity-domain` ✅ | identity vertex types | — |
| Base — *where* | **`location-domain`** 🆕 | `unit`/`building`/`property` (class `location`); `containedIn` (location→location) | core |
| Base — *what* | `service-domain` ♻️ | `service` (template/instance); `instanceOf`; `providedTo`; `providedBy` — **`availableAt` removed** | identity-domain, orchestration-base |
| Scheme | **`service-location`** 🆕 | `residesIn` (identity→location); `availableAt` + `unavailableAt` (service→location); `permitsOperation` (service→op-meta); the **`capabilityServiceAccess`** lens + the core service-key re-point | location-domain, service-domain |

**Why this split.** `service-domain` becomes truly **location-unaware** (bare catalog + runs).
`location-domain` is a genuine base domain (places have their own lifecycle, will grow — rooms, parking,
amenities — and are reusable by future features), mirroring how `identity-domain` underpins `rbac-domain`.
`service-location` is then a *thin access scheme* composing the three bases — not a package conflating "the
spatial model" with "the access projection." `availableAt`+`unavailableAt` are the availability *pair* and
live together in the scheme. **`residesIn` lives in service-location** (residence-as-access-input), keeping
`location-domain` a dependency-light pure place-graph.

**The `service-domain` refactor is cheap + safe:** `availableAt` is dormant — `CreateServiceTemplate` writes
it only when supplied and *nothing reads it* (confirmed: the sole would-be consumer is the unbuilt cap.svc
lens). Drop the `availableAt` param + link-write from `CreateServiceTemplate`; service-location adds a
`WireServiceAvailableAt` op. No functional change to anything live.

**`vtx.service.*` is shared but disjoint:** `service-domain` templates (class `service.<x>.template`, carry
`availableAt`) are the bookable services the lens projects; instances (class `service.<x>.instance`, carry
`providedTo`, never `availableAt`) and lease-signing's claim vertices are excluded by the lens's
template-class guard (§3). The lens *enforces* the boundary, not just relies on the invariant.

## 3. The `capabilityServiceAccess` lens (service-location)

Spec mirrors `rbac-domain`'s `capabilityRoles`:

```go
{
    CanonicalName:  "capabilityServiceAccess",
    Class:          "meta.lens",
    Adapter:        "nats-kv",
    Bucket:         "capability-kv",          // auth-plane: enables BFS-activation + the §6.2 resurrection guard
    Engine:         "full",
    Spec:           capabilityServiceAccessSpec,
    ProjectionKind: "actorAggregate",
    Output: &pkgmgr.OutputDescriptorSpec{
        AnchorType: "identity", OutputKeyPattern: "cap.svc.{actorSuffix}",
        BodyColumns: []string{"serviceAccess"}, EmptyBehavior: "delete", Freshness: "auto",
    },
},
```

**The cypher** — directions match the as-built model (`availableAt`/`unavailableAt` are `service→location`;
`containedIn` is `child→parent`; `residesIn` is `identity→location`); the exclusion uses **fresh** variables
so it sees a *closer* `unavailableAt`; `svc` is class-guarded to templates:

```cypher
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(loc0)-[:containedIn*0..]->(loc)<-[:availableAt]-(svc)
WHERE svc.class = $templateClass
  AND NOT (identity)-[:residesIn]->(ex0)-[:containedIn*0..]->(exLoc)<-[:unavailableAt]-(svc)
RETURN
  identity.key AS actorKey,
  collect(DISTINCT {
    service: svc.key, serviceClass: svc.class, resolvedVia: [loc.key],
    allowedOperations: [(svc)-[:permitsOperation]->(op) | {operationType: op.data.operationType}]
  }) AS serviceAccess
```

- **§6.10 item 1 (multi-level exclusion).** `ex0`/`exLoc` are fresh (never bound elsewhere), so the
  existential re-walks the actor's *entire* residence→containment chain for any `unavailableAt` from the
  bound `svc`. `traverseRel` only pins an *already-bound* target (`executor.go:613-619`); fresh vars seed
  independently. **MUST be proven by a real executor fixture in SL** (penthouse/laundry) — it has never been
  tested in this codebase.
- **§6.10 item 2 (transitive availability).** `(loc0)-[:containedIn*0..]->(loc)` walks residence→ancestors
  (bounded depth 10); `*0..` includes direct.
- **§6.10 item 3 (per-operation overrides) — DEFERRED (Andrew).** v1 ships the `permitsOperation` list →
  `allowedOperations`; per-operation `availableAt`/`unavailableAt` refinement is out of v1, documented (not
  silently skipped).
- **Template guard.** `svc.class = $templateClass` ensures only catalog templates project — never
  instances/claim vertices.
- **Activation.** The `containedIn*0..` hop isn't subset-safe for the narrow compiler, so
  `InstallActorAggregate` Warn-and-proceeds with BFS + (auth-plane ⇒) the resurrection guard
  (`driver.go:168-196`). Inherited carried obligation: a future forest-driven fan-out story must give this
  lens compiler coverage or reformulate, else it then fails activation.

## 4. The core change — re-point the service auth key (+ one contract touchpoint)

Add `serviceKeyFromActor → "cap.svc." + <suffix>` (mirrors `ephemeralKeyFromActor`/`rolesKeyFromActor`),
swap the `service` entry's `keyDerivation` (`step3_auth_matcher.go:112`). **Unconditional** — system actors
never select the service path (`ac.Service != ""`, which they never set); verify `service_actor_auth_parity_test.go`
+ add a system-actor service deny-by-absence test. One-key-per-path preserved.

**Contract touchpoint — §6.12 / FR22 `actorRoles` on service denials.** After the re-point, `cap.svc.<actor>`
carries no `roles`, so service-op denials no longer surface `actorRoles`. Under the residence-based scheme
that's arguably *more* correct (a service denial should explain residence/availability, not roles). **Decision
(Andrew, settled): accept it; edit Contract #6 §6.12 IN PLACE in SL.2, left UNCOMMITTED for Andrew's review
(NOT a CAR — Andrew's explicit call).** Allow/deny outcomes unaffected — only denial-response richness changes.

## 5. Ops / permissions / manifests (mirror `rbac-domain`)

- **location-domain:** Create/Tombstone for `unit`/`building`/`property` (or a generic `location` DDL with a
  `locationType` discriminator — implementer's call; distinct types preferred for Loupe clarity); wire/unwire
  `containedIn` (validate both ends are locations). Permissions `grantsTo: [operator]`, scope `any`.
- **service-domain (refactor):** drop `availableAt` from `CreateServiceTemplate` (param + link-write + its
  test fixtures); keep `providedBy`/`instanceOf`/`providedTo`.
- **service-location:** wire/unwire `residesIn` (validate target ∈ location types), `availableAt` /
  `unavailableAt` (source = a service template, target = a location), `permitsOperation` (source = service,
  target = op-meta). `residesIn` cardinality: allow multiple (the fresh-var exclusion is residence-set-aware).
  Endpoint-class validation is at the op — do not rely on the lens's untyped match.

## 6. Stories

- **SL.1 — `location-domain` package.** Location vertex DDL(s) + `containedIn` wire-op (with endpoint-class
  validation) + permissions + manifest + package.go. Install-flow test (co-installs with identity-domain).
  Non-security; **a thorough lead review suffices** (no lens, no auth-plane) — stated explicitly per the
  scale-review house rule.
- **SL.2 — `service-location` + the `service-domain` `availableAt` refactor + the lens (full 3-layer; security-plane).**
  `residesIn`/`availableAt`/`unavailableAt`/`permitsOperation` wire-ops; the `service-domain` refactor; the
  `capabilityServiceAccess` lens (fixed cypher) + the `serviceKeyFromActor` re-point; **§6.10 fixtures authored
  fresh against the real executor** (penthouse/laundry exclusion via the fresh-var existential; building-level
  transitive availability; instance-not-swept template guard); the FR22 §6.12 CAR; reconcile the bypass + auth
  service oracles to the re-pointed key; a service-plane resurrection test; e2e through the real Processor
  service path; `service_actor_auth_parity_test.go` + bootstrap-quiescent E2E stay green.

## 7. Contract touchpoints

**One:** §6.12 / FR22 `actorRoles`-on-service-denial (§4) → an **in-place edit to Contract #6 §6.12** in SL.2,
left UNCOMMITTED for Andrew's review (not a CAR — Andrew's call). Everything else is build-to-FROZEN.

## 8. Verification gates

`go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`, `make verify-package-location-domain`
+ `verify-package-service-location` + `verify-package-service-domain` (co-install), `make test-bypass`
(Gate 2 BLOCKED — service ops deny-by-absence until availability is projected, then authorize),
`make test-capability-adversarial` (Gate 3 DEFENDED — §6.10 vectors incl. multi-level exclusion against the
**real** lens), and the package `go test`s.
