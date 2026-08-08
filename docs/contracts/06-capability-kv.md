# Contract #6 — Capability KV Shape

Capability KV is what makes the architecture's O(1) authorization promise real. The Capability Lens (a Refractor projection authored as `class: "meta.lens"`) walks graph topology — actor → roles → permissions, actor → residence → services-availableAt-with-exclusions, actor → assigned-tasks → granted-operations — and writes the resolved per-actor capability set as a flat document. The Processor at commit step 3 reads a single key from Capability KV; no graph traversal in the hot path.

This contract is **security-critical** per the architecture's "Capability Lens is a security-critical projection" note. A bug here equals privilege escalation. The cypher rule (Story 3.x) and the bypass test suite (Stories 1.11 and 3.x — Capability Lens 4 attack vectors gate) are joint owners of correctness.

### Source of Truth

**The shapes in this contract are produced by the capability lenses' `RETURN` clauses** — the
bootstrap-seeded primordial anchor plus the package-owned slices (§6.1). Any change to a producing
cypher OR this contract must update the other in the same operation; the contract-conformance tests
are the safety net.

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
2026-06-17).** Adjudicated and recorded, with the full session narrative, in
`docs/decisions/projection-plane-decomposition.md` (D-PROJECTION + D-CONSUMER). The mechanism — the
declarative `projectionKind: actorAggregate` plan compiler (§6.13) on the write side and the generic
one-key-per-path auth-hook dispatcher (Contract #2 §2.8) on the read side — lets each grant type live
at its own disjoint key with **no core edit**:

- **`cap.roles.<actor>`** — role/permission grants, projected by `rbac-domain`'s `capabilityRoles`
  lens. `capabilityRoleIndex` (FR22 denial source) is `rbac-domain`-owned too; both degrade to empty
  when `rbac-domain` is absent.
- **`cap.svc.<actor>`** — service-access grants, projected by the service package that owns the
  residence scheme (`service-location`'s `capabilityServiceAccess` lens, §6.5). The key space degrades
  to registered-but-empty (absence = denial, §6.8) when no service package is installed.

After the decomposition the bootstrap `capability` cypher is the **narrow primordial-identity
anchor**: it designates root by the **primordial `holdsRole → operator` topology** — a bounded
single-link existence check projecting the literal set of root-equivalent platform grants core must
project even when no RBAC package is installed. The `operator` role, its permission vertices, and the
system identities' `holdsRole` links are **core-seeded** (Contract #7 §7.2), so the anchor couples to
the primordial operator topology, not to the rbac-domain package — root capability is established by
graph topology, never by class-based special-casing (§7.2). `data.protected` is **not** a capability
designator; it retains only its anti-brick meaning (the step-8 update/tombstone guard).

**Step-3 key derivation (normative).** Step 3 path-dispatches **before** the read:

- The **ordinary-actor** platform path and every scoped (task / service) path read **exactly one**
  disjoint key (§2.8) — ordinary actor → `cap.roles.<actor>`, task → `cap.ephemeral.<actor>`,
  service → `cap.svc.<actor>` — preserving the single-GET **user hot path**.
- The **system-actor** platform path (a kernel-seeded root-topology identity, the same
  `holdsRole → operator` predicate) is the one bounded exception: it reads the core `cap.<actor>`
  anchor **∪** `cap.roles.<actor>` and **unions** them (concat `platformPermissions`, union `lanes`).
  The anchor is the rbac-independent kernel floor; `cap.roles.<actor>` the rbac-derived extension.
  The union is **deny-closed** — a grant appears iff some slice grants it; both keys absent →
  `AuthDenied` (§6.8) — and bounded to the kernel-seeded root actors, never the user hot path.
- **Absent `rbac-domain`**, the derivation degrades to `cap.<actor>` for all actors (floor only;
  ordinary actors deny by absence).

**Privileged lanes are core-policy-owned, not anchor-exclusive (scoped-privileged-lane-grants-design.md,
mechanism C1).** A `cap.roles.<actor>` entry MAY carry a privileged (`meta`/`urgent`/`system`) per-op
`lanes` value (§6.4) — honored **only** when `{operationType, lane}` is on the core-owned allowlist (a
Processor constant, `privilegedLaneAllowlist`); an unlisted privileged grant is **stripped to
`default`** and raises a `PrivilegedLaneGrantRejected` Health issue. Core decides what may ever be
privileged; a package only assigns an allowlisted grant to a role. The anchor's doc-level `lanes` is
unaffected — root keeps all four lanes regardless of the allowlist.

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
    "vtx.lease.op4Nb2mPq6rTwzKxVyP7": 3,
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
      "resolvedVia": ["vtx.lease.op4Nb2mPq6rTwzKxVyP7"],
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
retried or reordered stale projection can never resurrect a revoked grant on the security plane (the
confirmed-reachable exposure is recorded in `docs/decisions/projection-plane-decomposition.md`,
D-INTEGRITY).

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
- **Rebuild interaction:** a guarded bucket's rebuild **forces `truncate=true`** — the purge clears the
  watermarks with the data, the stream replays from empty, and the highest-seq write wins
  (`docs/components/refractor.md`, rebuild table).
- **Non-CDC writes (reconciliation and shred — the only two sanctioned token classes):** a write with no
  triggering CDC message must still carry a `projectionSeq`, and exactly two token classes are sanctioned.
  (1) A **reconciliation write** (the auth-plane reproject verb / convergence sweep) is stamped with the
  pipeline's **last-applied stream sequence captured before re-evaluation** — a subordinate token: every
  CDC event not yet reflected in the reconciliation read carries a strictly greater sequence and
  overwrites it under the `≤`-rejects rule, so reconciliation can heal a missing/stale doc but can never
  outrank real stream truth. (2) A **shred nullification** (`keyShredded` listener) is stamped `MaxInt64`
  — a terminal, always-wins authority. These must never be swapped: a reconciliation write stamped
  `MaxInt64` would permanently freeze the key against all future CDC writes. Any further non-CDC write
  class requires a contract change, not a new ad-hoc token.

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
| `lanes` | no | Optional array of lanes this grant authorizes (default `["default"]` when absent). The step-3 lane gate checks `env.Lane` against the **matched permission's** `lanes` on the platform path (falling back to the doc-level `lanes`, §6.3, for entries without their own) — **landed**. A **privileged** lane (`meta`/`urgent`/`system`) in a package-projected (`cap.roles`) grant is honored **only if** `{operationType, lane}` is on the core privileged-lane allowlist (a Processor constant); otherwise it is stripped to `default` and a `PrivilegedLaneGrantRejected` Health issue is raised (§6.1). The **anchor** doc's lanes are unaffected (root keeps all four). |

Processor dispatch (when `authContext.service` is null AND `authContext.task` is null):
1. Scan `platformPermissions[]` for matching `operationType`
2. Validate scope:
   - `any` → allow
   - `self` → require `authContext.target == actor`
   - `specific` → require `authContext.target` exact-match on the scope's allowed targets — **platform-path `specific` is currently a deny-stub** (returns `AuthContextMismatch`, "not implemented"); full impl deferred to **Phase 3** (see §6.7 note + Contract #10 §10.8 `StartLoomPattern`). Distinct from task/ephemeral `target` matching, which **is** implemented.
   - `owned` → deferred to Phase 2 (requires ownership-link model)
3. Gate the lane (see `lanes` above) — a scope-approved match still denies (`LaneUnauthorized`) if the resolved lane set excludes `authContext`'s submission lane.
4. → allow or deny

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
  `expiresAt` ← `task.data.expiresAt`.
- The bootstrap `capability` cypher carries no `task` MATCHes and no `ephemeralGrants` section;
  §6.10 item 5 is satisfied by this lens.
- **Step-3:** the task-dispatch branch reads `cap.ephemeral.<actor>` — a **single GET, no fallback**;
  matching per the table above. A task-path no-match denies with `AuthContextMismatch` and emits **no
  `actorRoles`** (the denial builder returns early for that code — no `cap.<actor>` second read).

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

The capability cyphers walk business-graph link names. The recommended conventions (`containedIn` /
`availableAt` / `unavailableAt` / `leases` / `residesIn` / `assignedTo` / `reportsTo`) ship with the
reference implementation — `docs/hello-lattice.md`, appendix. They are conventions, not
platform-reserved names: a cypher is authored against whichever link vocabulary a deployment
standardizes on.

### 6.10 Cypher Rule — Required Behaviors (Epic 3 Acceptance Criteria)

The Capability Lens cypher rule (the data of a `vtx.meta.<id>` with `class: "meta.lens"`) is built in Epic 3. Its required behaviors, captured here so Epic 3's acceptance criteria can reference this contract:

1. **Multi-level containment exclusion.** An `unavailableAt` at any level of the actor's containment
   chain wins over `availableAt` at a higher level — a whole-path check, never direct links only.
2. **Direct and transitive availability.** `availableAt` a location grants access at that location and
   at every location contained within it.
3. **Operation-level overrides.** An operation vertex's own `availableAt`/`unavailableAt` links filter
   AFTER service-level resolution; `serviceAccess[].allowedOperations[]` reflects the result.
4. **Role specialization.** Role-derived `platformPermissions[]` and location-derived `serviceAccess[]`
   are independent; an actor holding both shows both.
5. **Task-derived ephemeral grants (FR56)** — produced by the `orchestration-base` `capabilityEphemeral`
   lens (§6.6 amendment), including `reportsTo` manager delegation (two-hop limit).

6. **Adversarial test coverage (Phase 1 Gate 3).** The Capability Lens 4 attack vectors must be tested and rejected:
   - Direct manipulation of `vtx.role.*` to grant unauthorized permissions
   - Submission with `authContext.service` referencing a service not in `serviceAccess[]`
   - Use of a `vtx.task.*` reference after its `expiresAt` has passed
   - Cross-vertex permission bleed: actor having access to service X attempting an operation on service Y

### 6.11 Service Availability Windows — Deferred

Temporal availability (windows, recurring schedules, closures) is **out of Capability KV scope**: the
cypher evaluates static graph topology only, and rejection on temporal grounds belongs to the
operation's own logic or a future mechanism ratified in its own session.

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
      "deniedServiceClass": "service.cleaning.standard"
    }
  }
}
```

The denial is structural: it names what was denied (`deniedService` from `authContext.service`;
`deniedServiceClass` from a single denial-time read of the service vertex's `.class` aspect, §6.5); it
does **not** enumerate what IS available — that is the application's read-model question (P5).

**`actorRoles` on a service denial.** A service-op denial does **not** surface `actorRoles` — the
service path reads `cap.svc.<actor>`, which carries `serviceAccess[]` only; roles never participate in
the service grant. `actorRoles` remains populated on **role-derived** (platform-path) denials, where
the role projection is what was evaluated.

### 6.13 Actor-aggregate projection (the Output descriptor)

A per-actor aggregating lens is driven by **declarative aspects**, never core Go keyed on the lens
canonical name. A `meta.lens` definition opts in with the aspect **`projectionKind: "actorAggregate"`**;
Refractor compiles a `ProjectionPlan{Execution, Output}` (plus an `AuthPlane` classification flag):

- **Execution** — evaluate the lens for a bound `$actorKey` (the existing per-actor eval).
- **Fan-out** — on a changed vertex / link / aspect the affected anchors are enumerated by the **broad
  adjacency BFS** (`ActorEnumerator`), a **sound superset** that can never miss an affected anchor: it
  over-reprojects rather than under-reprojecting a security-plane lens, and is **unconditional** (identical
  for auth-plane and business lenses). *There is no compiled narrow-invalidation plan member — the
  Story-12.2 reverse-traversal compiler was retired (`retire-simple-engine`) in favor of the
  always-correct broad BFS; the narrow path was an efficiency optimization whose per-lens coverage analysis
  (and the activation gate guarding its incompleteness) was not worth the complexity.*
- **Output descriptor** (lens-definition aspects) — replaces the four Go wrappers:

  | Aspect | Meaning |
  |--------|---------|
  | `anchorType` | actor vertex type (or inferred from `MATCH (x:identity {key:$actorKey})`) |
  | `outputKeyPattern` | constrained key template, e.g. `cap.ephemeral.{actorSuffix}` |
  | `bodyColumns` | which RETURN aliases form the document body |
  | `emptyBehavior` | `delete` \| `softDelete` \| `emptyDoc` \| `skip` (empty-result handling) |
  | `realnessFilter` | `{ field }` — drop degenerate collect artifacts (e.g. `{taskKey:null}`); generalizes `realEphemeralGrants` / `realOpenTasks` |
  | `freshness` | `auto` — stamp `projectionSeq` (§6.2 guard) + the widened `projectedFromRevisions` (§6.3) |
  | `entryKeyColumn` | optional; per-entry output mode (§6.14 read-grant slices). Names the field of the (single) list body column that keys each entry: each **real** entry writes its own guarded key `<outputKey>.<entryKeyValue>` instead of one aggregate document, retraction is a per-actor prefix diff with tombstones-first ordering, and write failures retry as **actor re-evaluations, never raw-write replays** (an absent-key replay would resurrect a revoked entry past its revocation). Entry-key values are validated key tokens (fail-closed). |

- **Auth-plane classification (fail closed where it counts):** a lens is **auth-plane** when it projects
  into `capability-kv` (the `cap.*` write-authorization surface) **or** into a Postgres grant table
  (`actor_read_grants`, §6.14 read-authorization) — derived from the bucket/target, never a canonical-name
  list. Auth-plane makes the lens **guarded**: its writes carry the §6.2 monotonic-seq guard (guarded
  tombstone) so a stale CDC replay can never resurrect a revoked grant, and it alerts at the auth-plane
  (error) heartbeat severity rather than the business-lens (warning) tier. **Activation fails closed** on
  an invalid Output descriptor, or when a guarded lens's target adapter cannot enforce the write guard
  (e.g. a non-NATS-KV target) — a guarded lens must never run unguarded. Because fan-out is broad BFS for
  every lens, there is **no construct-coverage activation gate**.
- **One mechanism, not two:** `emptyBehavior: softDelete` reuses the §6.2 guard's tombstone.
- **`capabilityRoleIndex` is NOT an `actorAggregate`** — it is keyed by `operationType`. It keeps a
  bespoke path or gets a separate `operationAggregate` kind (decided in Story 12.4).

The Story 12.4 acceptance gate: installing a **brand-new** actor-aggregate package lens via
`InstallPackage` projects + invalidates correctly with **zero** edits under `cmd/` or
`internal/refractor/capabilityenv/`.

#### Amendment — scalar passthrough body columns (ratified 2026-06-18)

A body column's handling is decided by the **shape of its RETURN value at projection time** (no
descriptor field — opt-in by value shape):

- A **list** (`[]`) value is **realness-filtered** (the roster behavior) — degenerate null-collect
  entries are dropped.
- A **scalar** (bool / string / number / `nil`) value projects **verbatim**, bypassing the realness
  filter. A `nil` scalar projects as a genuine **null** (present field, null value) — **never** coerced
  to `[]`.

This is what lets a §10.2 convergence lens project scalar `violating` / `missing_*` / param columns
end-to-end (with §10.2's Option (b) bare-NanoID `keyColumn`). Roster lenses declare list body columns
and are unaffected. The empty-anchor retract paths are distinct per cause: a **tombstoned** anchor
retracts via the actor-disappearance delete; a **live** anchor whose required MATCH stops yielding a
row retracts via the doc-mode zero-row retraction; a lens designating a **scalar** `realnessFilter`
column still drives the `emptyBehavior` retract when that scalar is absent.

### 6.14 Read-path authorization (D1) — `cap-read.*` + authz-anchor

> **Status: ✅ Andrew-ratified (2026-06-27).** Read-path mirror of the write-path Capability KV.
> Design: `_bmad-output/implementation-artifacts/read-path-authorization-d1-design.md`.

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

**Producer key space (disjoint, same Capability KV bucket — mirrors `cap.roles`/`cap.svc`/`cap.ephemeral`).
One key per (actor, granted anchor)** — the NATS-KV slice is a *keyed set*, not an aggregate document, so no
single value grows with grant cardinality (an aggregated roster document exceeded NATS `max_payload` for a
well-connected actor, permanently freezing that actor's grant set — revocations stopped landing, fail-OPEN):
```
cap-read.<actor-vertex-key-suffix>.<anchorId>            # core base lens: self-grant (cap-read.identity.<id>.<id>) + primordial read scope
cap-read.roles.<actor-vertex-key-suffix>.<anchorId>      # rbac-domain package: role-derived read scope
cap-read.residence.<actor-vertex-key-suffix>.<anchorId>  # loftspace package: residesIn/leases/containedIn → readable units/leases
cap-read.<domain>.<actor-vertex-key-suffix>.<anchorId>   # each package projects its own domain's readable anchors, one key per anchor
```
Core owns only the **base** `cap-read.<actor>.<anchorId>` lens (self-anchor + primordial root scope —
references no package vocabulary). Every domain read-grant is a **separate `actorAggregate` lens shipped by
the package that owns the relationship** (opting into the §6.13 `entryKeyColumn` per-entry output mode) —
the same package→core dependency direction the Epic-12 write-side decomposition established. Each such lens
is auth-plane and inherits the **`projectionSeq` write-ordering guard** (§6.2), the **soft-tombstone on
delete** (§6.8), and **fail-closed activation** (§6.13) — now **per key**, the exact KV twin of the per-row
guard `actor_read_grants` carries below. Two obligations are specific to the per-entry mode: **retraction is
an explicit per-actor prefix diff** (an entry absent from a fresh evaluation is guard-tombstoned,
tombstones-first, in the same pass — a row-set shrink has no automatic overwrite retraction), and **write
failures retry as actor re-evaluations, never raw-write replays** (a replayed `Create` at an absent key
carries no watermark to lose against and would resurrect a revoked grant).

**Per-anchor NATS-KV entry shape (`cap-read[.<source>].<actorSuffix>.<anchorId>`).** Each producer projects
one entry per anchor it grants; the actor's effective readable set is the union of live (non-tombstoned)
entries across every slice — membership is a key lookup, never a roster scan.
```json
{
  "key":           "cap-read.residence.identity.Hj4kPmRtw9nbCxz5vQ2y.Lk2Pn6mQrtwzKbcXvP3T",
  "actor":         "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y",
  "version":       "1.0",
  "projectedAt":   "2026-07-25T14:32:18.142Z",
  "projectionSeq": 10481,
  "anchorType":    "unit",
  "via":           ["residesIn"]
}
```

| Field | Required | Purpose |
|-------|----------|---------|
| *(key suffix)* `anchorId` | yes (in the key) | The granted resource's **bare NanoID** (extracted via `nanoIdFromKey` — see the representation note below). Lives in the key, not the body: the reader answers "may actor read anchor?" with an exact GET (base) plus one `cap-read.*.<actorSuffix>.<anchorId>` filtered listing (domains), skipping `isDeleted` entries. Reader inputs are validated against subject metacharacters fail-closed. |
| `anchorType` / `via` | yes | Audit-only metadata (vertex type; justifying link path — the read analog of §6.5 `resolvedVia`). Never part of the membership match. |
| `key`/`actor`/`version`/`projectedAt`/`projectionSeq` | yes | As §6.3 (read-path mirror; `projectionSeq` is the per-key §6.2 guard token; `projectedFromRevisions` is not carried on per-anchor entries — it was aggregate provenance). |

**Join semantics (normative) — the anchor is an opaque match token, the bare NanoID (Andrew,
2026-06-29).** Enforcement asks only "is this row's anchor ∈ the actor's granted anchors?" — it never
dereferences the vertex and never branches on type. `anchorId` is the resource's **bare NanoID**
(globally unique by construction), produced by **`nanoIdFromKey(<vertexKey>)`**, a fail-closed cypher
function. **The membership join matches NanoID-to-NanoID** — `actor_read_grants.anchor_id` and the
row's `authzAnchors` both carry bare NanoIDs, compared directly with **no `anchor_type`
concatenation**; `anchorType` is audit-only metadata, never part of the match. (Contrast §6.5's
`service`/`resolvedVia`, which carry full vertex keys because they are write-path read-hint addresses
the Processor dereferences — a different use; never conflate the two.)

**The merge point — the Postgres `actor_read_grants` table (Path A).** Every read-grant lens **also**
projects to a shared table whose primary key carries the **contributing lens** so producers stay disjoint:
```
actor_read_grants(actor_id, anchor_id, grant_source, projection_seq, is_deleted)   PRIMARY KEY (actor_id, anchor_id, grant_source)
```
`anchor_id` is the resource's **bare NanoID** (the opaque match token; no `anchor_type` column — RLS never
matches on type). `grant_source` (the lens canonical name, e.g. `cap-read.residence`) makes each lens **own
its rows** — a revoke from one package deletes only that package's rows, never another's, exactly like the
write-side disjoint key prefixes. `projection_seq` carries the §6.2/§6.8 monotonic guard (upsert/delete
applies only when incoming seq > stored, per row key) so a stale CDC replay cannot resurrect a revoked grant.
**A revoke is a seq-guarded soft tombstone (`is_deleted = true`), not a hard `DELETE`** — the monotonic guard
above requires the revoked row's `projection_seq` to be **retained** (a hard delete discards the watermark, so
a later stale re-insert at a lower seq would resurrect the grant); the row and its seq are kept, and the
membership lookup/RLS policy filters live grants (`AND NOT is_deleted`). This reuses the standard Postgres
soft-delete convention. RLS then **unions across all sources natively** via the set-membership policy (a row
visible if **any** of its `authz_anchors` NanoIDs is granted by a live grant): `USING (EXISTS (SELECT 1 FROM
unnest(authz_anchors) a WHERE a IN (SELECT anchor_id FROM actor_read_grants WHERE actor_id =
current_setting('lattice.actor_id', true) AND NOT is_deleted)))`. No app-side multi-key union; the table *is*
the merge.

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

**Enforcement — Postgres RLS (Path A) is the boundary; Path B is transitional only.** The read boundary
authenticates the reader (a signed JWT keyed to the Identity vertex → verified `actor_id`, checked
against the token-revocation KV), then:

- **Protected data → Postgres RLS.** The read model lives in a Postgres table with `authz_anchors
  text[]` + the set-membership policy above; the boundary sets `SET LOCAL lattice.actor_id` per
  session; enforcement is DB-native and **unbypassable by app code**. **Every protected table carries
  `ENABLE` AND `FORCE ROW LEVEL SECURITY`** — with FORCE on, *any* policy/column mistake over-denies (a
  visible fail-closed outage, never a silent leak). Tables are **provisioned out-of-band** (a
  migration); Refractor issues no DDL. Instead it **verifies the posture at lens activation and on the
  periodic heartbeat** — FORCE RLS enabled; the required columns (`authz_anchors text[]`,
  `projection_seq bigint`, `is_deleted boolean`, `deleted_at timestamptz`, key + body); a `FOR SELECT`
  policy present — and **pauses the lens fail-closed** on any absence (`PauseInfra` →
  `CapabilityLensPaused`), auto-resuming on a passing re-probe. Mechanism detail:
  `docs/components/refractor.md` (Protected read-model provisioning). The generated `USING` clause
  denies a tombstoned row (`NOT is_deleted`) before evaluating membership.
- **`actor_read_grants` is `projectionSeq`-guarded** (the read-auth source of truth): an upsert/delete
  applies only when its incoming seq exceeds the stored one, per `(actor_id, anchor_id,
  grant_source)`, so a stale CDC replay cannot resurrect a revoked grant. Each lens touches **only its
  own `grant_source` rows**; a package uninstall retracts them via the standard lens-eviction.
  (An ordinary non-protected business table may be last-writer-wins — the Postgres adapter is
  guard-exempt there.)
- **A protected table's own Delete is always a seq-guarded soft tombstone** (`is_deleted = true,
  deleted_at = NOW()`, `projection_seq` bumped, conditioned on exceeding the stored seq) — **never a
  hard `DELETE`**, regardless of the lens's declared `deleteMode`: a hard delete would discard the
  watermark, leaving nothing for the guard to compare against, so a stale replay would resurrect the
  row unconditionally. A fresh higher-seq Upsert revives a tombstoned row (`is_deleted ← false`).
- **The wildcard anchor `'*'` — an all-access grant.** A grant row `(actor_id, '*', grant_source)` —
  the reserved anchor `'*'`, never a real NanoID (the alphabet excludes it) — reads every row of every
  protected table. The generated policy checks it first: `EXISTS (SELECT 1 FROM actor_read_grants
  WHERE actor_id = current_setting('lattice.actor_id', true) AND anchor_id = '*' AND NOT is_deleted)
  OR EXISTS (SELECT 1 FROM unnest(authz_anchors) a WHERE a IN (SELECT anchor_id FROM
  actor_read_grants WHERE actor_id = current_setting('lattice.actor_id', true) AND NOT is_deleted))`.
  Still a seq-guarded, revocable, attributable grant row — never an RLS bypass.
- **NATS-KV read-gateway filter (Path B) — transitional scaffold only.** A migration boundary MAY
  union the `cap-read.*.<actor>` slices and filter NATS-KV rows. Not a sanctioned end-state: a
  `protected: true` read model served from NATS-KV is a **forbidden, lint-failable state** — the lint
  gate requires it to target Postgres (RLS-enforced). Public/operational read models stay on NATS-KV.

**No entry = no read; no public-by-omission.** Absence of any `cap-read.*` grant for the actor — or a
row none of whose `authzAnchors` is a granted anchor — denies the read, mirroring §6.8. A read model is
public **only** by an explicit `public: true` declaration, **never** by forgetting an anchor.
