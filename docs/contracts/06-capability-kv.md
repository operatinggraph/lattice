# Contract #6 — Capability KV Shape

Capability KV is what makes the architecture's O(1) authorization promise real. The Capability Lens (a Refractor projection authored as `class: "meta.lens"`) walks graph topology — actor → roles → permissions, actor → residence → services-availableAt-with-exclusions, actor → assigned-tasks → granted-operations — and writes the resolved per-actor capability set as a flat document. The Processor at commit step 3 reads a single key from Capability KV; no graph traversal in the hot path.

This contract is **security-critical** per the architecture's "Capability Lens is a security-critical projection" note. A bug here equals privilege escalation. The cypher rule (Story 3.x) and the bypass test suite (Stories 1.11 and 3.x — Capability Lens 4 attack vectors gate) are joint owners of correctness.

### Source of Truth

**The shape defined in this contract is *produced by* the Capability Lens cypher query's `RETURN` clause.** The bootstrap-seeded cypher query at `vtx.meta.lens.capability` is the *source of truth* for what gets written to Capability KV. This contract serves two derived functions:

1. **Read-side contract** — the Processor at step 3 needs to know the shape to read it correctly. This contract documents the shape the cypher RETURN produces so Processor's reader code is grounded in a stable expectation.
2. **Test oracle** — Story 3.2's contract-conformance test runs the bootstrap cypher query against a seeded graph and asserts the output structure matches the shape below. This test catches schema drift if anyone modifies the Capability Lens cypher query without updating this contract (or vice versa).

**Schema drift mitigation:** Any change to either the bootstrap cypher query OR this contract must update the other in the same operation. The contract-conformance test in Story 3.2 is the safety net.

### 6.1 Bucket and Key Pattern

**Bucket:** A dedicated NATS KV bucket separate from Core KV, Health KV, and Weaver buckets. Owned by Refractor as a Lens target store — Refractor is the sole writer; Processor reads only.

**Key patterns:**
```
cap.<actor-vertex-key-suffix>             # primary per-actor entry
cap.role-by-operation.<operationType>     # secondary role-coverage index
cap.ephemeral.<actor-vertex-key-suffix>   # per-actor ephemeral task grants (Phase 2, Story 7.1 — see §6.6 amendment)
cap.roles.<actor-vertex-key-suffix>       # per-actor rbac role/permission grants (Phase 2, Story 12.6 — rbac-domain-owned; see decomposition note below)
cap.svc.<actor-vertex-key-suffix>         # per-actor service-access grants (Phase 2, Story 12.7 — service-package-owned; key space registered-but-may-be-empty until a service package projects)
```

**Primary entry** — Where `<actor-vertex-key-suffix>` is the actor's vertex key with the `vtx.` prefix dropped. Examples:

```
cap.identity.Hj4kPmRtw9nbCxz5vQ2y
cap.identity.St6mP3qBn4rT8wYxK7Vc
```

Phase 1 indexes capabilities by actor (one key per actor). Each entry contains the three-section permission model (§6.2). A by-operation actor index (Phase 2 — for Gateway pre-flight checks) is a separate addressable space; not in Phase 1 scope.

**Secondary role-coverage index** — populated by a separate bootstrap Lens (`vtx.meta.lens.capabilityRoleIndex`) projecting to the same Capability KV bucket. Used exclusively by Processor's denial-response construction (Story 3.4) to populate the `rolesCarryingPermission` field of `AuthDenied` responses without graph traversal on the denial path. Each entry contains a flat list of role names whose permission grants include the operation type. Example:

```
cap.role-by-operation.BookExecutiveCleaning
  → {"roles": ["penthouseResident", "platformAdmin"], "projectedAt": "..."}
```

**Architectural note on multi-Lens pattern.** The two key spaces are produced by **two separate Lens definitions**, both seeded at primordial bootstrap (Contract #7), both projecting to the same Capability KV bucket with disjoint key prefixes. This follows Lattice's standard pattern from the architectural decisions: *each Lens has one RETURN producing one shape; multi-output patterns are expressed as additional Lenses, not as Lens-internal complexity* (lattice-architecture.md §"Multi-target Lens adapters"; brainstorming session items #38, #39, #61). The same pattern applies to Phase 2+ Personal Lens fan-out and Postgres RLS link mirroring.

**Phase 2 extends this to a *package-owned* producer.** The `cap.ephemeral.*` key space is produced by a **third Lens (`capabilityEphemeral`) shipped by the `orchestration-base` package** — not seeded at bootstrap. This is the first instance of the **contract-contribution model**: core owns the Capability KV bucket + the step-3 reader; *packages project the grant types they own* into disjoint key spaces. It is what lets the bootstrap `capability` cypher **stop referencing the package-owned `task` type** (the dependency direction becomes package→core). `capabilityEphemeral` is its first proof-of-pattern.

**Phase 2 decomposition — the god-cypher split to package-owned disjoint keys (Epic 12 — COMPLETE
2026-06-17).** The decomposition is adjudicated (`docs/decisions/projection-plane-decomposition.md`,
D-PROJECTION + D-CONSUMER) and **landed**. The mechanism — a declarative `projectionKind: actorAggregate`
plan compiler (§6.13, Story 12.3/12.4) on the write side and a **generic one-key-per-path auth-hook
dispatcher** on the read side (Contract #2 §2.8, Story 12.5) — lets each grant type live at its own
disjoint key with **no core edit**:

- **`cap.roles.<actor>`** — `rbac-domain` projects the role/permission grants (Story 12.6, **done**);
  the bootstrap `capability` cypher **dropped its `holdsRole`/`grantedBy`/`role`/`permission` MATCHes**.
  An ordinary actor's role-derived platform grants now read from this key; a kernel-seeded primordial
  identity reads the core anchor (below). `capabilityRoleIndex` (FR22 denial source) is `rbac-domain`-
  owned too, degrading to empty when `rbac-domain` is absent.
- **`cap.svc.<actor>`** — service-access grants. **Path B taken (Story 12.7, folded into 12.6):** no
  `service-location` package exists, so the bootstrap cypher's `containedIn`/`availableAt`/
  `unavailableAt`/`permitsOperation` MATCHes were **simply deleted** with no replacement projection;
  the service matcher kind + key space stay registered-but-empty (absence = denial, §6.8) until a real
  service package projects into them — a pure package addition, no core edit.

After the decomposition the bootstrap `capability` cypher is the **narrow primordial-identity anchor**
(`WHERE identity.data.protected = true` → a literal set of the root-equivalent platform grants core
must project even when no RBAC package is installed) — core references no rbac or service/location grant
vocabulary, owning only the bucket, the key conventions, and the step-3 dispatcher. Step-3 preserves its
single-GET hot path because it path-dispatches **before** the read: each path reads exactly one disjoint
key by actor class (§2.8 amendment) — primordial identity → the core `cap.<actor>` anchor, ordinary
actor → `cap.roles.<actor>`.

### 6.2 Document Shape

```json
{
  "key": "cap.identity.Hj4kPmRtw9nbCxz5vQ2y",
  "actor": "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y",
  "version": "1.0",
  "projectedAt": "2026-05-12T14:32:18.142Z",
  "projectionSeq": 10472,
  "projectedFromRevisions": {
    "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y": 47,
    "vtx.meta.capabilityLensDefinition": 12,
    "vtx.unit.penthouse-Lk2Pn6mQrtwzKbcXvP3T": 8,
    "vtx.lease.Op4Nb2mPq6rTwzKxVyP7": 3,
    "vtx.role.penthouseResident": 5
  },
  "lanes": ["default"],

  "platformPermissions": [
    {
      "operationType": "ClaimIdentity",
      "scope": "self"
    },
    {
      "operationType": "UpdateIdentityContact",
      "scope": "self"
    }
  ],

  "serviceAccess": [
    {
      "service": "vtx.service.executive-cleaning-NanoID",
      "resolvedVia": ["vtx.unit.penthouse-Lk2Pn6mQrtwzKbcXvP3T"],
      "allowedOperations": [
        { "operationType": "BookExecutiveCleaning" },
        { "operationType": "CancelBooking" },
        { "operationType": "ViewSchedule" }
      ]
    },
    {
      "service": "vtx.service.payRent-NanoID",
      "resolvedVia": ["vtx.lease.Op4Nb2mPq6rTwzKxVyP7"],
      "allowedOperations": [
        { "operationType": "InitiatePayment" },
        { "operationType": "ViewBalance" },
        { "operationType": "SetupAutopay" }
      ]
    }
  ],

  "ephemeralGrants": [
    {
      "source": "task",
      "taskKey": "vtx.task.Rm7q3pntwzkfbcxv5p9j",
      "operationType": "ApproveLeaseApplication",
      "target": "vtx.lease.applicant-NanoID",
      "expiresAt": "2026-05-13T14:00:00.000Z"
    }
  ],

  "roles": [
    "vtx.role.penthouseResident",
    "vtx.role.leaseholderInGoodStanding"
  ]
}
```

#### Phase 2 amendment — projection-write integrity guard (`projectionSeq`, Story 12.1)

Actor-aggregate capability projections are written under a **monotonic write-ordering guard** so a
retried or reordered stale projection can never resurrect a revoked grant on the security plane. The
exposure is confirmed-reachable on `cap.ephemeral.<actor>`: Refractor's retry queue replays a *captured
row* (not a re-evaluation) from a **separate goroutine**, so a stale "open-era" `Upsert` can land after
a close-`Delete` and re-write a revoked grant, with no further CDC event to re-delete it.

- **`projectionSeq`** (integer) is stamped on every guarded write = the **JetStream stream sequence of
  the triggering CDC message**. It is a total order maintained by the substrate, plan-independent, and
  deterministic-replay-safe (a rebuild replays in stream order → highest-seq write wins → identical
  steady state). It supersedes `projectedAt`/`projectedFromRevisions` *as the ordering key* — those are
  anchor-provenance-derived and identical across the open/close reprojections of an unchanged actor
  vertex, so they cannot order a task-driven reprojection.
- **Guarded keys** (actor-aggregate classes): `cap.<actor>`, `cap.ephemeral.<actor>`,
  `my-tasks.<actor>` (Contract #10 §10.1), and — as they land — the decomposed `cap.roles.<actor>` /
  `cap.svc.<actor>`. **`cap.role-by-operation.<op>` is NOT guarded** — it is an operation-aggregate
  (keyed by `operationType`, not actor), with a different resurrection profile.
- **Write semantics:** a write to a guarded key is **rejected as an idempotent no-op when
  `incoming.projectionSeq ≤ stored.projectionSeq`**. The compare-and-set is **atomic against the target
  key's KV revision** (`Update` with `ExpectedRevision`), with a **bounded re-read-on-conflict loop**
  (load-bearing: the retry queue writes concurrently with the main consumer).
- **Enforcement is adapter-local:** only the NATS-KV adapter enforces the guard; the Postgres adapter is
  exempt (implements the extended write signature as a pass-through, no guard).
- **Rebuild interaction (Story 12.1b):** a `Rebuild(truncate=false)` replays historical lower-seq events
  that the guard would reject against live high-seq watermarks. Resolution: guarded buckets either force
  `truncate=true` (watermark cleared with the data) or the rebuild bypasses the guard for the replay —
  defined and tested in 12.1b.

See §6.8 for the soft-tombstone that carries the watermark across a delete.

### 6.3 Field Specification

**Top-level envelope:**

| Field | Required | Purpose |
|-------|----------|---------|
| `key` | yes | Echo of the Capability KV key |
| `actor` | yes | Full vertex key of the actor |
| `version` | yes | Document schema version. Phase 1 = `"1.0"`. Consumers branch on this; the contract evolves under Stream 3 oversight. |
| `projectedAt` | yes | **Deterministic provenance** ("as-of input state"): the anchor actor vertex's `lastModifiedAt` (Contract #1 §1.3), not a wall-clock read at projection time. Same input → same value across replay/rebuild. RFC3339 string. Consumed by monitoring + the Processor auth trace; it is **not** a freshness ceiling — the Processor performs no per-operation projection-age check (Story 1.5.4). It is **not** the write-ordering key (see `projectionSeq`). |
| `projectionSeq` | yes on guarded keys (Phase 2, Story 12.1) | **Monotonic write-ordering token** = the JetStream stream sequence of the triggering CDC message. A guarded-key write whose `projectionSeq ≤` the stored value is rejected as an idempotent no-op (§6.2 amendment). Present on the actor-aggregate classes (`cap.<actor>`, `cap.ephemeral.<actor>`, `my-tasks.<actor>`, and the decomposed `cap.roles`/`cap.svc` as they land); **not** present/enforced on `cap.role-by-operation.<op>` or on Postgres targets. Survives a delete via the §6.8 soft-tombstone. |
| `projectedFromRevisions` | yes | Map of source-vertex-key → revision-at-projection — the **coherence/debug** datum (consistency-window detection in the bypass suite), **not** the write-ordering guard (that is `projectionSeq`). **Phase 2 widening (Story 12.3):** covers the full contributing source set the compiled plan read — the actor's identity vertex, the lens-definition vertex, and the roles/tasks/services/links that *contributed a binding*. **Scope:** v1 covers contributing sources; covering sources that were *read-then-excluded* (e.g. a now-closed task) needs full-executor touched-then-dropped instrumentation — Story 12.3 states whether that is in-scope or a follow-up. (Phase 1 stamped only the actor + lens-def revisions.) |
| `lanes` | yes | Array of JetStream lanes the actor may submit to. Subset of `["default", "meta", "urgent", "system"]`. |
| `platformPermissions` | yes (may be empty `[]`) | Standing operation permissions not scoped to a service. See §6.4. |
| `serviceAccess` | yes (may be empty `[]`) | Service-scoped operation permissions. The cypher rule pre-resolves availability via graph topology. See §6.5. |
| `ephemeralGrants` | yes (may be empty `[]`) | Task-derived, time-bounded, target-specific grants (FR56). See §6.6. **Phase 2:** relocated out of this doc to its own `cap.ephemeral.<actor>` entry — see §6.6 amendment. |
| `roles` | yes (may be empty `[]`) | Vertex keys of role vertices the actor currently holds. Used by Processor for FR22 structural denial responses. |

### 6.4 platformPermissions[]

Each entry describes a system-level operation not scoped to any service.

| Field | Required | Purpose |
|-------|----------|---------|
| `operationType` | yes | Operation-type identifier, matched by **exact string equality** (no casing constraint is enforced). **Business** operations are conventionally PascalCase verb-noun (Contract #2 §2.1 — `CreateIdentity`, `ClaimIdentity`). **Platform control** operations use the reserved **`ctrl.<comp>.<verb>`** namespace (e.g. `ctrl.weaver.disable`, `ctrl.refractor.rebuild`, `ctrl.loom.pause`) — mirroring the `lattice.ctrl.<comp>.<verb>` control subject taxonomy and keeping control grants unmistakably distinct from business ops. |
| `scope` | yes | One of `any`, `self`, `owned`, `specific`. See §6.7. (Platform control ops use `any` — blanket per-verb grants; platform-path `specific` is a deny-stub, §6.7, so per-target control scoping is deferred to when `specific` is implemented.) |

Processor dispatch (when `authContext.service` is null AND `authContext.task` is null):
1. Scan `platformPermissions[]` for matching `operationType`
2. Validate scope:
   - `any` → allow
   - `self` → require `authContext.target == actor`
   - `specific` → require `authContext.target` exact-match on the scope's allowed targets — **platform-path `specific` is currently a deny-stub** (returns `AuthContextMismatch`, "not implemented"); full impl deferred to **Phase 3** (see §6.7 note + Contract #10 §10.8 `StartLoomPattern`). Distinct from task/ephemeral `target` matching, which **is** implemented.
   - `owned` → deferred to Phase 2 (requires ownership-link model)
3. → allow or deny

### 6.5 serviceAccess[]

Each entry describes the actor's resolved access to one service vertex, with the operations they may invoke on it. The cypher rule pre-resolved availability/unavailability via graph topology before writing the entry.

| Field | Required | Purpose |
|-------|----------|---------|
| `service` | yes | Vertex key of the service. |
| `resolvedVia` | yes | Array of vertex keys that justify access (e.g., the unit, the building, the lease). For auditability and debuggability — answers "why does this actor have access to this service?" |
| `allowedOperations` | yes | Array of operations the actor may invoke on this service. Each entry has `operationType`. |

The residence-based scheme (`service-location`'s `capabilityServiceAccess` lens) does **not** project a `serviceClass` field: it could only echo the bare root `class` (`service`) — the rich `service.<x>.<variant>` discriminator lives in the service's `.class` aspect, which a projection cypher cannot reach (the root `class` field shadows the like-named aspect). A structural denial that needs the rich class reads the service vertex's `.class` aspect by key at denial time.

Processor dispatch (when `authContext.service` is set):
1. Scan `serviceAccess[]` for entry where `service == authContext.service`
2. If not found → `AuthContextMismatch`
3. Scan that entry's `allowedOperations[]` for matching `operationType`
4. If not found → `AuthDenied`
5. → allow

### 6.6 ephemeralGrants[]

Each entry describes a time-bounded, target-specific authorization derived from a task assignment (FR56).

| Field | Required | Purpose |
|-------|----------|---------|
| `source` | yes | Grant source. Phase 1: `"task"`. Reserved for future grant sources. |
| `taskKey` | yes | Vertex key of the task that justifies this grant. |
| `operationType` | yes | Operation type permitted by the grant. |
| `target` | yes | Specific entity the grant applies to (e.g., the lease application being approved). |
| `expiresAt` | yes | ISO 8601 expiry timestamp. Processor enforces `expiresAt > now` at lookup time. |

Processor dispatch (when `authContext.task` is set):
1. Scan `ephemeralGrants[]` for entry where ALL of: `taskKey == authContext.task`, `operationType == envelope.operationType`, `target == authContext.target`, `expiresAt > now`
2. If not found → `AuthContextMismatch`
3. → allow

#### Phase 2 amendment — ephemeral grants relocate to their own entry + lens (a1, Story 7.1)

The Phase-1 shape above (an `ephemeralGrants[]` *section inside the per-actor `cap.<actor>` doc*,
produced by the bootstrap `capability` god-cypher) is **superseded for Phase 2** by an extraction
that removes the `task` package type from the core/bootstrap cypher. The grant **field shape is
unchanged**; what changes is its *container, key, producer, and source paths*:

- **New entry**, projected by the **`orchestration-base`-owned `capabilityEphemeral` lens** (not
  bootstrap), to the disjoint key `cap.ephemeral.<actor-suffix>`:
  ```json
  {
    "key":         "cap.ephemeral.identity.Hj4kPmRtw9nbCxz5vQ2y",
    "actor":       "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y",
    "version":     "1.0",
    "projectedAt": "2026-05-12T14:32:18.142Z",
    "projectionSeq": 10472,
    "ephemeralGrants": [
      { "source": "task",
        "taskKey": "vtx.task.Rm7q3pntwzkfbcxv5p9j",
        "operationType": "ApproveLeaseApplication",
        "target": "vtx.lease.applicant-NanoID",
        "expiresAt": "2026-05-13T14:00:00.000Z" }
    ]
  }
  ```
- **Link-sourced** (Contract #10 §10.1 — task relationships are links, not fields): the lens walks
  `(identity)<-[:assignedTo]-(task)` (+ `reportsTo` 2-hop for manager delegation), then
  `operationType` ← `(task)-[:forOperation]->(op)`, `target` ← `(task)-[:scopedTo]->(t)`,
  `expiresAt` ← `task.data.expiresAt`. *(Was: `task.data.grantedOperationType` / `task.data.targetKey`
  fields — corrected anti-pattern.)*
- **Bootstrap `capability` cypher drops its two `task` OPTIONAL MATCHes** and the `ephemeralGrants`
  section of `cap.<actor>` (it goes empty / is removed there). §6.10 item 5 is satisfied by the new
  lens instead.
- **Step-3 (`step3_auth_capability.go`):** the `task`-dispatch branch (`matchEphemeralGrant`) reads
  `cap.ephemeral.<actor>` (it needs only grants) — a **single GET, no fallback**. The **matching logic
  is unchanged**. A task-path no-match denies with `AuthContextMismatch`; the denial builder
  (`BuildDenialDetails`) returns early for that code and emits **no `actorRoles`**, so there is **no
  `cap.<actor>` second read** on this path. (Earlier drafts claimed a roles-fallback-on-denial — that
  was based on a false premise about the denial shape and is dropped.)
- **Conformance:** the §6.6 contract-conformance test moves with the lens (now asserts the
  `cap.ephemeral.<actor>` entry against the `orchestration-base` `capabilityEphemeral` cypher); the
  bootstrap `capability` conformance test drops its `ephemeralGrants` expectations.

Rationale + the broader god-cypher decomposition (auth-hooks consumer side, rbac/service projections):
Contract #10 §10.1/§10.7 + lattice-architecture.md future-ADR open item.

### 6.7 Scope Enumeration

| Scope | Meaning | Phase |
|-------|---------|-------|
| `any` | Operation permitted on any target — broadest scope. | Phase 1 |
| `self` | Operation permitted only when `authContext.target == actor`. | Phase 1 |
| `specific` | Operation permitted only on a named target list (declared by the permission entry). | **Task/ephemeral path** (match on the grant's `target`): **implemented**. **Platform path** (`matchPlatformPermission`): **deny-stub** — `AuthContextMismatch`, full impl **deferred to Phase 3** (Contract #10 §10.8 external `StartLoomPattern` callers). |
| `owned` | Operation permitted on vertices the actor "owns" via a defined ownership link. | Phase 2 (requires ownership-link model) |

### 6.8 "No Entry = No Access"

If Processor at step 3 fetches `cap.<actor>` and receives no document (key does not exist), the operation is denied with `AuthDenied`. **Absence of a capability projection means no access** — there is no anonymous/public capability fallback.

The Capability Lens must produce a projection for every identity that may submit operations, including AI agents and internal service actors. The bootstrap identity gets its projection at platform initialization via primordial meta-vertices (Contract #7).

This is the architecture's NFR-S2 boundary: the Capability Lens is the sole authorization surface. Anything not in the projection is denied.

**Phase 2 — soft tombstone on guarded keys (Story 12.1).** A `Delete` on a **guarded** key (the
actor-aggregate classes — §6.2 amendment) is written as a **soft tombstone**
`{ "isDeleted": true, "projectionSeq": <seq> }` so the high-water mark survives physical absence (a
stale lower-seq replay arriving after the delete is still rejected). **Absence and tombstone are
equivalent for authorization** — both yield no grants, so step-3 denies in both cases; there is **no
step-3 behavior change**. A non-auth consumer of a guarded bucket (e.g. `my-tasks`) MUST treat an
`isDeleted: true` document as absence and skip it (Contract #10 §10.1 forward obligation).

### 6.9 Recommended Business Link Names

The Capability Lens cypher rule references business-graph link names to walk topology. The following names are **recommended conventions** shipped with the canonical reference implementation ("Hello Lattice", FR55). Operators may define their own link types and rewrite the cypher rule to match; the names below are not platform-reserved, only convention.

| Link name | Used between | Semantics |
|-----------|--------------|-----------|
| `containedIn` | Location vertices (unit → building, room → unit, building → property) | Physical or logical containment; transitive |
| `availableAt` | Service vertex → location vertex | Service is offered at this location (and by default, at locations contained within) |
| `unavailableAt` | Service vertex → location vertex | Explicit exclusion override; closer exclusion wins over distant availability |
| `leases` | Identity → lease vertex | Actor holds a lease; lease references a unit via `containedIn` from the unit side |
| `residesIn` | Identity → location vertex | Actor resides at this location (independent of lease — guests, family, etc.) |
| `assignedTo` | Task vertex → identity vertex | Task is assigned to the actor; grants ephemeral capability per FR56 |
| `reportsTo` | Identity → identity | Reporting chain for manager-delegated task auth per FR56 |

These are recommendations only. The cypher rule (Story 3.x) is authored against whichever link conventions a deployment standardizes on.

### 6.10 Cypher Rule — Required Behaviors (Epic 3 Acceptance Criteria)

The Capability Lens cypher rule (the data of a `vtx.meta.<id>` with `class: "meta.lens"`) is built in Epic 3. Its required behaviors, captured here so Epic 3's acceptance criteria can reference this contract:

1. **Multi-level containment exclusion.** An `unavailableAt` link at any level of an actor's containment chain wins over `availableAt` at a higher level. The rule must check the entire containment path between the actor's location and the exclusion's target, not just direct links. Test case: penthouse resident with building-level `availableAt: laundry` and penthouse-level `unavailableAt: laundry` → laundry NOT in `serviceAccess[]`.

2. **Direct and transitive availability.** A service `availableAt` a location grants access to actors at that location AND at locations contained within it. The rule walks `containedIn` from the actor's location upward, collecting availability at each level. Test case: resident of any unit in a building can access `availableAt: building` services.

3. **Operation-level overrides.** Individual operation vertices linked to a service may have their own `availableAt`/`unavailableAt` links that override service-level resolution. The rule applies operation-level filtering AFTER service-level resolution; `serviceAccess[].allowedOperations[]` reflects the result.

4. **Role specialization.** Permissions derived from `vtx.role.*` linked to the identity contribute to `platformPermissions[]` independent of location-scoped service access. An actor may have both location-derived service access AND role-derived platform permissions; both must appear in their projection.

5. **Task-derived ephemeral grants (FR56).** Tasks `assignedTo` the actor produce `ephemeralGrants[]` entries with `expiresAt` populated from the task's `dueAt` or expiry aspect. Manager delegation: tasks assigned to direct reports (via `reportsTo`) produce ephemeral grants for the manager. Two-hop traversal limit at Phase 1; deeper delegation chains are Phase 2+. **Phase 2 (a1):** this behavior moves to the `orchestration-base` `capabilityEphemeral` lens (key `cap.ephemeral.<actor>`); the bootstrap `capability` cypher no longer produces ephemeral grants. See §6.6 amendment.

6. **Adversarial test coverage (Phase 1 Gate 3).** The Capability Lens 4 attack vectors must be tested and rejected:
   - Direct manipulation of `vtx.role.*` to grant unauthorized permissions
   - Submission with `authContext.service` referencing a service not in `serviceAccess[]`
   - Use of a `vtx.task.*` reference after its `expiresAt` has passed
   - Cross-vertex permission bleed: actor having access to service X attempting an operation on service Y

### 6.11 Service Availability Windows — Deferred

Service vertices may eventually carry temporal availability aspects — e.g., `availableFrom`/`availableUntil` aspects, recurring schedules ("laundry 6am–10pm"), holiday closures, maintenance windows. **These are explicitly OUT of Capability KV scope.**

The cypher rule at Phase 1 evaluates service availability based purely on static graph topology (the existence of `availableAt` / `unavailableAt` links at projection time). If a service is temporally closed but the graph topology still says it's available, the projection will say it's available; rejection on temporal grounds is the responsibility of the operation itself (Starlark business logic) or a Phase 2 mechanism.

The shape and Lattice integration of service availability windows requires a **separate architecture session**. This is tracked as a Phase 2 design open item — not a Phase 1 gap.

### 6.12 FR22 Denial Response — Worked Example

When the penthouse resident attempts `BookLaundryService` targeting `vtx.service.laundry-NanoID`:

```json
{
  "status": "rejected",
  "error": {
    "code": "AuthContextMismatch",
    "message": "Service not available for this actor.",
    "details": {
      "operationType": "BookLaundryService",
      "deniedService": "vtx.service.laundry-NanoID",
      "deniedServiceClass": "service.cleaning.standard",
      "availableServiceClasses": [
        "service.cleaning.executive",
        "service.financial.rentPayment"
      ]
    }
  }
}
```

The denial response is structural (per Journey 5's design): names what was denied and what IS available.

**`actorRoles` on a service denial.** A service-op denial does **not** surface `actorRoles`. The service auth path reads the disjoint `cap.svc.<actor>` key projected by the `service-location` package's `capabilityServiceAccess` lens (the residence-based grant scheme), and that document carries `serviceAccess[]` only — no `roles`. Under the residence scheme this is the more faithful denial: a service denial is explained by residence and availability (the `deniedService` / `deniedServiceClass` / `availableServiceClasses` fields), not by the actor's roles, which never participate in the service grant. The `actorRoles` field remains populated on **role-derived** (platform-path) denials, where the role projection is what was evaluated.

### 6.13 Implementation Notes

**For the AI agent implementing Story 3.x (Capability Lens cypher rule):**

- The Lens definition is a `vtx.meta.<id>` with `class: "meta.lens"`. Its aspects include `canonicalName: "capability"`, `targetBucket: "capability-kv"`, `cypherRule: "..."`, and the schema for the output document.
- The cypher rule produces one output document per identity, keyed by `cap.<actor-vertex-suffix>`.
- The rule must handle the six behaviors enumerated in §6.10.
- Output documents must follow the shape in §6.2 exactly — Processor's parser is strict about field names and types.

**For the AI agent implementing Story 1.4 (Processor — Consume, Dedup, Auth Stub):**

Phase 1 stub implementation:
- Step 3 reads `cap.<actor-vertex-suffix>` from Capability KV
- If missing → `AuthDenied`
- If present: dispatch per Contract #2 §2.8 logic (task → service → platform path selection)
- The stub may always-allow if the deployment is configured with `LATTICE_AUTH_STUB=allow-all` for early development — but production deployments enforce strictly. The bypass test suite (Story 1.11) must run with the real Capability Lens, not the stub.

**For the bypass test suite (Stories 1.11 and 3.x):**

The Capability Lens 4 attack vectors (Phase 1 Gate 3) test against the real Lens output, not the stub. Test data: a graph that exercises each attack vector listed in §6.10 item 6.

**For the AI agent implementing Story 12.3/12.4 (declarative actor-aggregate projection):**

A per-actor aggregating lens is driven by **declarative aspects**, not core Go keyed on the lens
canonical name (the per-`CanonicalName` `switch` in `cmd/refractor/main.go` and the bespoke
`internal/refractor/capabilityenv/` wrappers are **deleted** in Story 12.4). A `meta.lens` definition
opts in with a new aspect **`projectionKind: "actorAggregate"`**; Refractor then compiles a
`ProjectionPlan{Execution, Invalidation, Output}`:

- **Execution** — evaluate the lens for a bound `$actorKey` (the existing per-actor eval).
- **Invalidation** — a **compiled reverse-traversal plan** derived from the lens MATCH that yields the
  affected anchors from a changed vertex / link / aspect, replacing the broad `ActorEnumerator` BFS.
  The covered-construct set is validated by the Story 12.2 spike.
- **Output descriptor** (lens-definition aspects) — replaces the four Go wrappers:

  | Aspect | Meaning |
  |--------|---------|
  | `anchorType` | actor vertex type (or inferred from `MATCH (x:identity {key:$actorKey})`) |
  | `outputKeyPattern` | constrained key template, e.g. `cap.ephemeral.{actorSuffix}` |
  | `bodyColumns` | which RETURN aliases form the document body |
  | `emptyBehavior` | `delete` \| `softDelete` \| `emptyDoc` \| `skip` (empty-result handling) |
  | `realnessFilter` | `{ field }` — drop degenerate collect artifacts (e.g. `{taskKey:null}`); generalizes `realEphemeralGrants` / `realOpenTasks` |
  | `freshness` | `auto` — stamp `projectionSeq` (§6.2 guard) + the widened `projectedFromRevisions` (§6.3) |

- **Fail closed on the security plane:** an **auth-plane** `actorAggregate` lens whose MATCH uses a
  construct the narrow invalidation compiler does not cover **fails activation**; a non-auth lens falls
  back to broad BFS with a warning.
- **One mechanism, not two:** `emptyBehavior: softDelete` reuses the §6.2 guard's tombstone.
- **`capabilityRoleIndex` is NOT an `actorAggregate`** — it is keyed by `operationType`. It keeps a
  bespoke path or gets a separate `operationAggregate` kind (decided in Story 12.4).

The Story 12.4 acceptance gate: installing a **brand-new** actor-aggregate package lens via
`InstallPackage` projects + invalidates correctly with **zero** edits under `cmd/` or
`internal/refractor/capabilityenv/`.

#### Phase 2 amendment — scalar passthrough body columns (CAR E6, ratified 2026-06-18, Andrew)

The `bodyColumns` above were originally **roster** columns: each is a `collect(DISTINCT {...})` **list**,
and `realnessFilter` drops the degenerate null-collect artifact an OPTIONAL-match cypher leaves for an
actor with no real rows. A §10.2 **convergence** lens (External I/O Bridge) instead projects **scalar**
columns — `violating` (bool), each `missing_<gap>` (bool), and the `row.<col>` param columns the §10.8
playbook templates (`entityKey`, `applicant` — strings) — which Weaver reads as scalars (`boolColumn`
requires a Go `bool`; param columns resolve as strings).

An actorAggregate Output descriptor's body columns **MAY therefore be scalar passthroughs**, detected by
the **shape of the RETURN value at projection time** (no new descriptor field — opt-in by value shape):

- A body column whose RETURN value is a **list** (`[]`) is **realness-filtered** (the roster behavior,
  unchanged) — degenerate null-collect entries are dropped.
- A body column whose RETURN value is a **scalar** (bool / string / number / `nil`) projects **verbatim**:
  the raw value as-is, bypassing the realness filter. A `nil` scalar projects as a genuine **null**
  (present field, null value), so a downstream bool reads `false` and a string param reads absent —
  **never** coerced to `[]`. (Before this amendment every body column ran through the list filter, so a
  scalar projected as `[]` and Weaver's `boolColumn` could not read it.)

This is what lets a §10.2 convergence lens project the scalar `violating` / `missing_*` / param columns
Weaver reads end-to-end — together with the §10.2 Option (b) `keyColumn` (bare-NanoID row key), it makes a
convergence lens projectable through Refractor's actorAggregate path.

The change is **additive and opt-in by value shape**: existing roster lenses (`my-tasks`,
`capabilityEphemeral`, the bootstrap `capability`) declare list body columns and are **unaffected**
(byte-for-byte identical projections + delete-when-empty). The empty-actor delete path is preserved: a
convergence lens that disappears (its required anchor MATCH yields no row) still retracts via the
actor-disappearance delete at `BuildKey(actorKey)`; a lens that designates a **scalar** `realnessFilter`
column (e.g. `entityKey` non-null = anchor alive) still drives the `emptyBehavior` retract when that scalar
is absent. Landed in Refractor's Epic-12 Output-descriptor machinery
(`internal/refractor/projection/driver.go` `EnvelopeFn`); no frozen-contract widening.

### 6.14 Read-path authorization (D1) — `cap-read.*` + authz-anchor

> **Status: ✅ Andrew-ratified (2026-06-27).** Read-path mirror of the write-path Capability KV.
> Design: `_bmad-output/implementation-artifacts/read-path-authorization-d1-design.md`
> (`lattice-architecture.md` D1; Contract #10 §10.2; brainstorming #38/#61/#118). **Forks resolved by
> Andrew:** **(1) enforcement = Postgres-RLS (Path A) is *the* boundary for protected data**; the NATS-KV
> read-gateway filter (Path B) is **transitional scaffold only** — once RLS ships, a `protected: true` read
> model served from NATS-KV is a **lint-failable** state (see "Enforcement" below). **(2)** the minimal
> JWT read-actor auth seam ships as **D1 increment 1**; the full internet-facing Gateway is deferred to ops.

Contract #6 above is the **write-path** authorization surface (the Processor reads it at commit step 3).
**Reads** have no such boundary — a lens target can be read directly, bypassing the Capability boundary
(NFR-S2 / D1). This section adds the **read-path mirror**, following the **same contract-contribution model
as §6.1** (core owns the bucket + the read boundary + the key conventions; **packages project the read
grants they own** into disjoint key spaces) — *not* a single god-cypher.

**The read mirror is decomposed exactly like the write side (§6.1).** Read auth differs from write auth in
one structural way: write auth asks "may I do op X?" and the step-3 reader **dispatches to the one** grant
key for that op (single GET, boolean). Read auth asks "which rows may I see?" and needs the **union** of
*every* package's read grants for the actor — so it cannot be dispatched away; it must merge. The merge is
therefore pushed into the **Postgres `actor_read_grants` table** (below), where RLS unions it natively.

**Producer key space (disjoint, same Capability KV bucket — mirrors `cap.roles`/`cap.svc`/`cap.ephemeral`).**
```
cap-read.<actor-vertex-key-suffix>            # core base lens: self + primordial read scope only
cap-read.roles.<actor-vertex-key-suffix>      # rbac-domain package: role-derived read scope
cap-read.residence.<actor-vertex-key-suffix>  # loftspace package: residesIn/leases/containedIn → readable units/leases
cap-read.<domain>.<actor-vertex-key-suffix>   # each package projects its own domain's readable anchors (e.g. clinic provider→patient)
```
Core owns only the **base** `cap-read.<actor>` lens (self-anchor + primordial root scope — references no
package vocabulary). Every domain read-grant is a **separate `actorAggregate` lens shipped by the package
that owns the relationship** — the same package→core dependency direction the Epic-12 write-side
decomposition established. Each such lens is auth-plane and inherits the **`projectionSeq` write-ordering
guard** (§6.2), the **soft-tombstone on delete** (§6.8), and **fail-closed activation** (§6.13) — verbatim.

**Per-lens NATS-KV document shape (`cap-read.<source>.<actor>`).** Each producer projects the slice it
owns; the union across slices is the actor's full readable set.
```json
{
  "key":           "cap-read.residence.identity.Hj4kPmRtw9nbCxz5vQ2y",
  "actor":         "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y",
  "version":       "1.0",
  "projectedAt":   "2026-06-26T14:32:18.142Z",
  "projectionSeq": 10481,
  "readableAnchors": [
    { "anchorType": "unit",  "anchorId": "Lk2Pn6mQrtwzKbcXvP3T", "via": ["residesIn"] },
    { "anchorType": "lease", "anchorId": "Op4Nb2mPq6rTwzKxVyP7", "via": ["leases"] }
  ]
}
```

| Field | Required | Purpose |
|-------|----------|---------|
| `readableAnchors` | yes (may be empty `[]`) | The resource anchors **this lens** grants. Each entry: `anchorType` (vertex type — audit/convenience only, see note), `anchorId` (the resource's **bare NanoID**, extracted from its vertex key via `nanoIdFromKey` — see the representation note below), `via` (justifying link path — the read analog of §6.5 `resolvedVia`, for auditability). The actor's effective set is the **union over all `cap-read.*.<actor>` slices**. |
| `key`/`actor`/`version`/`projectedAt`/`projectionSeq` | yes | As §6.3 (read-path mirror). |

> **Representation note (`anchorId`) — the anchor is an opaque match token = the bare NanoID (Andrew,
> 2026-06-29).** An RLS anchor is an **opaque membership token**, *not* a dereferenceable address: enforcement
> only asks "is this row's anchor ∈ the actor's granted anchors?" — it never reads the vertex, never branches
> on type. So `anchorId` is the resource's **bare NanoID** (`Lk2Pn6mQrtwzKbcXvP3T`), which is **globally
> unique by construction** and therefore a sufficient token; **`anchor_type` plays no role in the match.**
> (Contrast §6.5 `serviceAccess.service`/`resolvedVia`, which carry the **full vertex key** `vtx.<type>.<id>`
> *because there it is a write-path **read-hint address** the Processor dereferences/hydrates by* — a
> genuinely different use; the two are not the same kind of thing and must not be conflated.) The bare NanoID
> is produced by **`nanoIdFromKey(<vertexKey>)`** — a targeted, fail-closed cypher function added to the
> auth-plane engine to strip the `vtx.<type>.` prefix (the engine had no string function, which is why **D1.1
> shipped the full key as a pre-function interim** — its lens is revised to `nanoIdFromKey` once the function
> lands; see the Lattice-lane prerequisite). **The membership join matches NanoID-to-NanoID** — both
> `actor_read_grants.anchor_id` and the read-model row's `authzAnchors` carry bare NanoIDs; the policy compares
> them directly with **no `anchor_type` concatenation**. `anchorType` is retained on the NATS-KV
> `readableAnchors` doc as **audit-only metadata** (which vertex type a grant is for) — never in the match.

**The merge point — the Postgres `actor_read_grants` table (Path A).** Every read-grant lens **also**
projects to a shared table whose primary key carries the **contributing lens** so producers stay disjoint:
```
actor_read_grants(actor_id, anchor_id, grant_source, projection_seq)   PRIMARY KEY (actor_id, anchor_id, grant_source)
```
`anchor_id` is the resource's **bare NanoID** (the opaque match token; no `anchor_type` column — RLS never
matches on type). `grant_source` (the lens canonical name, e.g. `cap-read.residence`) makes each lens **own
its rows** — a revoke from one package deletes only that package's rows, never another's, exactly like the
write-side disjoint key prefixes. `projection_seq` carries the §6.2/§6.8 monotonic guard (upsert/delete
applies only when incoming seq > stored, per row key) so a stale CDC replay cannot resurrect a revoked grant.
RLS then **unions across all sources natively** via the set-membership policy (a row visible if **any** of its
`authz_anchors` NanoIDs is granted): `USING (EXISTS (SELECT 1 FROM unnest(authz_anchors) a WHERE a IN (SELECT
anchor_id FROM actor_read_grants WHERE actor_id = current_setting('lattice.actor_id', true))))`. No app-side
multi-key union; the table *is* the merge.

**Authz-anchor convention (protected-by-default; `authzAnchors` is a set).** A business read-model target
is **protected by default** — readable only through the authz boundary — **unless it explicitly declares
`public: true`** (an auditable opt-out for genuinely public/operational models, e.g. a public listings
index). A protected target projects an **`authzAnchors`** column: a **set** of **bare NanoIDs** (e.g.
`["Lk2Pn6mQrtwzKbcXvP3T", "Qz7Rp2mN…"]`, extracted via `nanoIdFromKey`) — the same opaque-token
representation as `actor_read_grants.anchor_id` (the join is NanoID-to-NanoID, § representation note).
**A row is readable if the actor holds a grant for ANY anchor in its set.** The set admits **coarse /
hierarchical** grants without per-leaf materialization: a building manager holds one grant for the
**building's** NanoID, and each unit-scoped row carries both its leaf anchor (the **unit's** NanoID) **and**
its container anchors (the **building's** NanoID); a provider holds the **patient** NanoIDs they cover, and
each appointment row carries its **patient's** NanoID. A target that is
**neither** `public: true` **nor** projects a resolvable `authzAnchors` **fails closed** (activation/lint
error; on Postgres, deny-all — see Enforcement) — **omission denies, never silently serves**, mirroring
§6.8. The conventions-lint audits only the small explicit-`public: true` set; it deliberately cannot infer
intent for an un-declared target — which is exactly why the default is *protect*, not *publish*. Generalizes
Contract #10 §10.2's "carries the D1 authz anchor **there** [the Postgres read-path]" to **any** protected
target.

**Enforcement (Andrew-ratified Fork 1 — Path A is the boundary; Path B is transitional only).** The read
boundary authenticates the reader (D1 increment 1: a signed JWT keyed to the Identity vertex → verified
`actor_id`; checked against the token-revocation KV — brainstorm #118/#111), then:
- **Protected data → Postgres-RLS (the enforcement boundary).** The business read model lives in a Postgres
  table with an `authz_anchors` column (a set — e.g. `text[]`) + a **set-membership** RLS policy:
  `USING (EXISTS (SELECT 1 FROM unnest(authz_anchors) a WHERE a IN (SELECT anchor_id FROM
  actor_read_grants WHERE actor_id = current_setting('lattice.actor_id', true))))` (a row is visible if **any**
  of its anchors — bare NanoIDs — is granted). The boundary sets `SET LOCAL lattice.actor_id` per session; enforcement is
  DB-native and **unbypassable by app code**. **Every protected table is created with `ENABLE ROW LEVEL
  SECURITY` AND `FORCE ROW LEVEL SECURITY`**, so a table whose policy was never generated **denies all rows**
  (a fail-closed outage, never a silent leak). This is the destination for **all** protected read models.
- **`actor_read_grants` is `projectionSeq`-guarded (the read-auth source of truth).** Unlike business
  read-model tables (which may be last-writer-wins — the Postgres adapter is guard-exempt there), the grant
  table inherits the §6.2/§6.8 **monotonic-seq guarantee**: an upsert/delete applies only when its incoming
  `projectionSeq` exceeds the stored one (per `(actor_id, anchor_id, grant_source)`), so a stale
  CDC replay **cannot resurrect a revoked grant**. Each lens's projection upserts/tombstones **only its own
  `grant_source` rows** (so revoking one source never wipes another's coexisting grant), and a package
  uninstall retracts its `grant_source` rows via the standard lens-eviction.
- **NATS-KV read-gateway filter (Path B) — transitional scaffold only.** During a migration a boundary MAY
  GET the `cap-read.*.<actor>` slices, union them, and filter NATS-KV rows whose `authzAnchor` ∉ that union.
  **This is not a sanctioned end-state:** once Postgres-RLS ships, **a `protected: true` read model served
  from NATS-KV is a forbidden, lint-failable state** — the `protected: true` conventions-lint gate is
  extended from "must project `authzAnchor`" to "must target Postgres (RLS-enforced)." Public/operational
  read models stay on NATS-KV (they declare no `authzAnchor`).

**No entry = no read; no public-by-omission.** Absence of any `cap-read.*` grant for the actor — or a row
none of whose `authzAnchors` is a granted anchor — denies the read, mirroring §6.8. There is **no
anonymous/public-read fallback by omission**: a read model is public **only** by an explicit `public: true`
declaration, **never** by forgetting an anchor. (The earlier "no `authzAnchor` ⇒ public-read" rule was
default-open — a forgotten column silently world-published a protected model; the read path now denies on
absence, exactly as the write path does.)

**Affected consumers:** Refractor (the core base `capabilityRead` lens + the Postgres `actor_read_grants`
multi-target); each **package** (ships its own `cap-read.<domain>` read-grant lens — `rbac-domain`,
`loftspace`, `clinic`, …); the read boundary (Postgres RLS; the transitional NATS-KV filter); every
protected business read-model lens (must project `authzAnchor`, and — post-RLS — must target Postgres). No
change to the write-path §6.2–§6.13.
