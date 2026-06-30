# Contract #2 — Operation Envelope

The operation envelope is the message format a client publishes to `core-operations` JetStream. It is the only way to introduce state changes into Core KV (no exceptions — see architectural principle P2). This contract defines its shape, lane semantics, reply contract, and implementation requirements.

### 2.1 Envelope Shape

```json
{
  "requestId": "Rm7q3pntwzkfbcxv5p9j",
  "lane": "default",
  "operationType": "CreateIdentity",
  "actor": "vtx.identity.St6mP3qBn4rT8wYxK7Vc",
  "submittedAt": "2026-04-11T14:32:18.142Z",
  "payload": {
    "name": "Andrew Solgan",
    "email": "andrew@lattice.example"
  },
  "contextHint": {
    "reads": [
      "vtx.identity.St6mP3qBn4rT8wYxK7Vc",
      "vtx.meta.mP3qBn4rT8wYxK7Vc6St2"
    ]
  }
}
```

### 2.2 Field Specification

| Field | Required | Type | Mutability | Purpose |
|-------|----------|------|------------|---------|
| `requestId` | yes | string (20-char NanoID, custom alphabet per Contract #1) | immutable | Client-generated idempotency key. The matching `vtx.op.<requestId>` tracker is committed atomically with the operation's mutations (commit step 8). Resubmitting the same `requestId` is the dedup path. |
| `lane` | yes | string (enum: `default`, `meta`, `urgent`, `system`) | immutable | Determines JetStream subject (`ops.<lane>.>`) and consumer routing. See §2.3. |
| `operationType` | yes | string (PascalCase verb-noun) | immutable | Operation's type. Used by Starlark dispatch and by `permittedCommands` enforcement at commit step 6. Examples: `CreateIdentity`, `ClaimIdentity`, `AssignReportingChain`. |
| `actor` | yes | string (full vertex key, e.g., `vtx.identity.<NanoID>`) | immutable | Identity vertex submitting the operation. Used for Capability KV auth lookup (commit step 3) and provenance fields on resulting documents. |
| `submittedAt` | yes | string (ISO 8601) | immutable | Client-side submission timestamp. Useful for debugging and audit. **NOT** used by the Processor for ordering — JetStream sequence is authoritative. |
| `payload` | yes | object | immutable | Operation-specific data. Shape varies by `operationType`. Schema validated by Starlark dispatch (not by envelope schema; envelope is type-agnostic). May be empty `{}` for parameterless operations. |
| `contextHint` | optional | object with `reads: string[]` | immutable | JIT Hydration directive — declared read set. Lists Core KV keys the Starlark script will read. Processor pre-fetches these at commit step 4. If absent, Processor falls back to lazy on-demand reads (with latency penalty under load). See §2.5. |

**`actor` form:** Full vertex key including the `vtx.` prefix. Short forms (`identity.<id>`) are reserved for HTTP headers in Phase 2 (Gateway translates to full key before envelope submission).

**Optional field — `class` (optional, `omitempty`):**

Story 1.6 introduced an optional top-level `class` field on the operation envelope to let the Hydrator resolve the operation's DDL during the window before the full DDL cache could derive class from `operationType`. Story 1.7 brought the DDL cache forward; the operationType→class reverse index it anticipated is now **built** (the DDL cache's `byCommand` map): when an envelope omits `class`, the Hydrator resolves the DDL from `operationType` — the single **vertexType** DDL whose `permittedCommands` admit it. The index is integrity-safe by two disciplines: it is built from **vertexType (script-bearing) DDLs only** (an aspectType DDL lists an op in `permittedCommands` purely as a step-6 write gate, never as the executing script, so it is never a class-inference target), and a **global ambiguity guard** drops any op admitted by more than one vertexType DDL (such an op still requires an explicit `class` — the inference never guesses). Resolution is **auth-neutral**: authorization (step 3) precedes class resolution (step 4) and keys on `operationType` + actor + authContext, never on `class`, so making `class` optional cannot widen the auth surface; the inferred class is exactly the DDL `permittedCommands` step 6 already enforces.

`class` is therefore **engine-optional**: engine-dispatched ops (Loom/Weaver) and clients MAY omit it and rely on operationType→class derivation; an explicit `class` (or `payload.class`) still takes precedence when supplied.

**Discriminator class vs. script-authority DDL (the `instanceOf` type model).** The envelope `class` selects the operation's **script DDL** (which Starlark runs) — resolved here from `operationType`. The **resulting vertex's** stored `class` is its **type/subtype discriminator** (P7), which may be a fine-grained dotted string (`service.backgroundCheck.instance`) that is *not* itself a registered DDL. These are legitimately distinct: an op may write a vertex whose class differs from the op's resolved script-DDL canonical name. The **write-gate** (`permittedCommands`, commit step 6) resolves *that vertex's* governing DDL by its own class — by exact lookup, falling back to its `instanceOf` type authority per Contract #1 §1.5 step 5. Authorization (step 3) keys on `operationType` + actor + `authContext` and is unaffected by either class, so the discriminator never enters the auth path.

| Field | Required | Type | Mutability | Purpose |
|-------|----------|------|------------|---------|
| `class` | optional | string (DDL canonical name, e.g., `"identity"`) | immutable | Tells the Hydrator/Validator which DDL meta-vertex applies to this operation. Precedence: top-level `class` → `payload.class` → derived from `operationType` via the DDL cache's operationType→class reverse index (the single vertexType DDL that admits it; ambiguous/unindexed ops still require an explicit `class`). The field is `omitempty` in the wire format. |

See `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` (Story 1.6 entry, resolved in Story 1.7) for the full disposition record.

### 2.3 Lanes and JetStream Subject Mapping

Phase 1 reserves four lanes. Operations on each lane publish to a corresponding JetStream subject prefix; the Processor's lane consumers subscribe to the matching subjects.

| Lane | JetStream Subject | Consumer Semantics | Use Case |
|------|-------------------|---------------------|----------|
| `default` | `ops.default.>` | Standard parallel consumer; bulk of operator and AI traffic | Normal business operations |
| `meta` | `ops.meta.>` | **Serialized** consumer (concurrency = 1); DDL cache invalidation synchronous with commit | DDL changes; Lens definition changes; event schema changes. Serialization prevents concurrent DDL races. |
| `urgent` | `ops.urgent.>` | Priority parallel consumer with higher weight in scheduling | Time-sensitive business operations (e.g., security overrides, emergency revocations). Operator-defined criteria — platform does not auto-promote. |
| `system` | `ops.system.>` | Parallel consumer dedicated to internal service actors | Loom/Weaver/admin tool operations. Separating these from `default` prevents internal automation from competing with user-facing operations for consumer capacity. |

**Lane authorization:** Submitting to a lane is itself capability-controlled. The Capability Lens grants per-lane submission rights. Most actors hold `default` only. `meta` requires operator/admin capability. `urgent` requires explicit grant. `system` is reserved for internal service actors. A submission to a lane the actor lacks capability for is rejected at commit step 3 (auth check) before any further processing.

**Deferred lane reservations** (post-Phase 1):
- `replay` — for the Replay tool's operations during disaster recovery; keeps replays from competing with live traffic
- Operator-custom lanes — Phase 2+ may permit DDL-driven lane registration

### 2.4 Reply Envelope

`core-operations` uses JetStream's request-reply pattern. The Processor returns a reply envelope **after commit step 8 (atomic batch commit)** — at which point the operation is durable, but events are still being published (step 9) and projections have not yet caught up.

```json
{
  "requestId": "Rm7q3pntwzkfbcxv5p9j",
  "opTrackerKey": "vtx.op.Rm7q3pntwzkfbcxv5p9j",
  "status": "accepted",
  "committedAt": "2026-04-11T14:32:18.215Z"
}
```

For errors:

```json
{
  "requestId": "Rm7q3pntwzkfbcxv5p9j",
  "opTrackerKey": null,
  "status": "rejected",
  "error": {
    "code": "AuthDenied",
    "message": "Actor lacks permission for operation type 'CreateLease' on lane 'default'",
    "details": {
      "missingPermission": "lease.create",
      "actorRole": "consumer"
    }
  }
}
```

For dedup-detected resubmits:

```json
{
  "requestId": "Rm7q3pntwzkfbcxv5p9j",
  "opTrackerKey": "vtx.op.Rm7q3pntwzkfbcxv5p9j",
  "status": "duplicate",
  "originalCommittedAt": "2026-04-11T14:32:18.215Z"
}
```

**Reply field specification:**

| Field | Required | Notes |
|-------|----------|-------|
| `requestId` | yes | Echo of submitted requestId |
| `opTrackerKey` | yes for `accepted`/`duplicate`; null for `rejected` | Vertex key of the idempotency tracker. Client polls this for Read-Your-Own-Writes convergence (per architecture's MVP RYOW mitigation). |
| `status` | yes | `accepted` (committed), `duplicate` (already committed via prior submission), `rejected` (validation/auth failure — no commit) |
| `committedAt` | for `accepted` | Timestamp of step 8 commit |
| `originalCommittedAt` | for `duplicate` | Timestamp of original commit |
| `decision` | for `accepted` | `"committed"` on a fresh commit |
| `revisions` | optional, for `accepted` | Per-key revision map (`{key: revision}`) returned by the substrate after the atomic batch. **The committed mutation key set IS the key set of this map.** Useful for client RYOW polling and for addressing any committed key. |
| `primaryKey` | optional, for `accepted` | The single principal Core KV key the operation wrote (e.g. the created identity/role/permission vertex, or a link key). The Processor **validates that `primaryKey` is within the committed write footprint** — either a committed key, or the 3-segment vertex root of a committed key (so an aspect-only update names its principal vertex, not an internal aspect). A script can only name an entity it actually wrote. Multi-key operations with no single principal entity (InstallPackage / UninstallPackage / MergeIdentity) omit it; clients read the full key set from `revisions`. |
| `error` | for `rejected` | Structured error: `code` (machine-readable), `message` (human-readable), `details` (structured context). Error codes are enumerated; see §2.6. |

There is **no `detail` field**. The reply carries only commit-trace identifiers
the Processor itself produced (`primaryKey`, `revisions`) — never arbitrary,
script-returned data. The write path is not a read channel: read-derived signals
travel on business events (e.g. `IdentityCreated.data.duplicate`), and one-time
secrets are never returned (see §2.7).

### 2.7 Closed `response` script-return schema

A Starlark operation script MAY return a top-level `response` dict to name the
operation's principal committed key. The schema is **closed**: the only permitted
key is `primaryKey` (a string).

- Any other key in `response` is a fail-closed `ScriptFailed` /
  `InvalidReturnShape` error at parse time, before commit.
- When set, the Processor validates `primaryKey` is within the committed write
  footprint — a committed key, or the 3-segment vertex root of a committed key
  (letting aspect-only updates name their principal vertex). Otherwise the
  operation is rejected with `DDLViolation`.
- Absent `response` / absent `primaryKey` is allowed (the reply simply omits
  `primaryKey`).

This makes the synchronous reply incapable of carrying arbitrary or sensitive
data. Claim secrets follow Option C: the **client** mints the secret, submits
only its `sha256` hash (`claimKeyHash`) in the op payload, and Lattice stores the
hash verbatim — the plaintext never enters Lattice and is never returned.

**The reply does NOT wait for:**
- Event publication (step 9) — fire-and-forget after atomic commit
- Projection convergence — client polls `opTrackerKey` for that
- Lens-target store write — client polls the relevant Lens for query convergence

**Why reply after step 8 rather than step 10:** Durability is guaranteed by step 8 (atomic batch with revision conditions). Events are validated *before* step 8 (step 7), so if the operation reached step 8 it produced valid events. Step 9 (publish) is retried on Processor restart via the redelivery + dedup path. The client's "is my operation done?" question is honestly answered at step 8.

### 2.5 Context Hint Semantics

The `contextHint.reads` array declares Core KV keys the Starlark script will read. At commit step 4 (Hydrate), the Processor pre-fetches these into the working set cache.

**When provided:**
- Processor fetches all declared keys in parallel (NATS KV batch read)
- Working set cache is populated before Starlark execution begins
- Starlark reads hit the cache; no Core KV round-trips during script execution
- Reads of keys NOT in `contextHint` still work (fall through to on-demand fetch) but incur latency

**When absent:**
- Processor uses lazy on-demand reads during Starlark execution
- Each `kv.Read()` call from Starlark performs a Core KV fetch
- Per-operation latency increases proportional to read count
- At MVP scale (10–100 ops/sec) this is tolerable; under sustained load it becomes a bottleneck

**Convention:** SDK tools and AI agent integrations SHOULD populate `contextHint` whenever the read set is determinable at submission time. The platform does not enforce its presence.

**Future evolution (post-Phase 1):** Static analysis of Starlark scripts may auto-derive read sets, eliminating the need for callers to populate `contextHint` explicitly. Not in scope for Phase 1.

### 2.5.1 Bounded Link Enumeration (`kv.Links`)

`contextHint.reads` + `kv.Read` cover only **known-key** reads (a single named key, single-key GET). A guard that enforces a **set** or **range** constraint needs the *set* of a vertex's neighbors — an enumeration whose membership is not known at submission time, so it cannot be pre-declared in `contextHint.reads`. The Starlark `kv` module exposes exactly one such primitive:

```
kv.Links(hubKey, relation, direction, cursor=None, limit=N) -> (page, nextCursor)
#   direction: "out" (hub is the link SOURCE) | "in" (hub is the link TARGET)
#   page:      list[linkDoc] (this page's links); nextCursor: opaque token or None when exhausted
```

It returns the Core KV canonical links incident to `hubKey` under `relation` in the requested `direction`. Each `linkDoc` carries the standard link envelope projection (`key`, `class`, `isDeleted`, `data`, `revision`, `sourceVertex`, `targetVertex`); logically-deleted (tombstoned) links are **returned** carrying `isDeleted` (the script decides), as with `kv.Read`. Order within a page is unspecified.

This is the **one sanctioned relaxation** of the otherwise known-key-reads-only write path. It is bounded, paged, lazy, and scoped:

- **Bound to the hub's vertex id in BOTH directions — a server-side subject-filtered list.** The canonical link key is `lnk.<sourceType>.<sourceId>.<relation>.<targetType>.<targetId>` (Contract #1 §1.1; source first). The hub's vertex id is a **fixed token** in either direction, so the read is bounded by the hub's degree *in that direction*, never the link space:
  - `direction:"out"` (hub is the source) → key filter `lnk.<hubType>.<hubId>.<relation>.>` (hub id in the prefix; `>` matches `<targetType>.<targetId>`).
  - `direction:"in"` (hub is the target) → key filter `lnk.*.*.<relation>.<hubType>.<hubId>` (hub id in the suffix; the two `*` wildcard `<sourceType>.<sourceId>`, one token each; `<relation>` fixed).
  Both are evaluated by NATS as a server-side subject filter (`$KV.<core-kv>.<keyFilter>`) — the server returns only matching keys, so **neither direction scans the keyspace**. The link keeps its natural §1.1 direction; the guard chooses the `direction` matching where the hub sits in that link (the inbound filter relies on NATS subject `*` wildcards being valid at any token position).
- **Paged, not fail-closed-capped.** A high-degree hub (e.g. a service template with many `instanceOf` instances pointing back) is enumerated **page by page** via the opaque `cursor`/`nextCursor` — the call **never silently truncates** and **never fail-closes a legitimately high-degree hub**. `limit` bounds each page; the guard pages until `nextCursor` is `None`. (A guard that must page a very-high-degree hub bears that cost explicitly — a visible authoring choice, not a hidden cap.)
- **Lazy.** The enumeration + per-key reads happen **only when the script calls `kv.Links`**, and only for the pages it pulls — never eagerly pre-hydrated (a wildcard/prefix filter has no exact-key form to pre-declare in `contextHint.reads`). Reads run under the per-invocation wall-budget context and count against the script timeout (NFR-P4).
- **Core KV links only.** `kv.Links` reads **only** the Core KV canonical link keyspace. It never reads the Refractor Adjacency KV (which remains Refractor-private — `lattice-architecture.md`) and never a lens/read-model (P5: applications read lenses; the write path reads its own Core KV). `hubKey` must be a 3-segment vertex key; the constructed filter is always under `lnk.` — no `vtx.`/aspect prefixes, no other bucket.
- **Not a serialization point.** `kv.Links` returns the **currently-committed** matching links; it is **not** snapshot-isolated and does **not** itself serialize concurrent writers, and a paged enumeration may observe an add/remove between pages. A guard enforcing a constraint over the returned set **MUST** additionally contend a shared OCC-guarded key (a per-hub scalar epoch both concurrent writers bump) for correctness: a concurrent mutation bumps the epoch, the step-8 OCC CAS fails, and the op re-hydrates and re-enumerates. The enumeration recovers the *set*; the OCC-guarded scalar recovers the *lock*.
- **Live read, not replay-stable.** Like `kv.Read`, `kv.Links` reads live Core KV — two runs of the same `requestId` may observe different sets. The deterministic id + the step-8 OCC commit are the idempotency authority, not replay determinism.

`kv.Links` is the bounded write-path complement to graph adjacency (the read-side/fan-out role the Refractor Adjacency KV serves). It exists so vertex→vertex relationships live in **links** (§1.1, decision #2), never denormalized as key-lists in aspect `data`. It reads links **in their natural §1.1 direction** — it does **not** require authoring a relation against the growth-order convention.

### 2.6 Error Code Enumeration (Initial Set)

The reply envelope's `error.code` is one of a closed enumeration. Phase 1 codes:

| Code | Meaning | Commit Step |
|------|---------|-------------|
| `EnvelopeMalformed` | Operation envelope failed schema validation (missing required field, invalid type, etc.) | Pre-step-1 (Processor entry) |
| `LaneUnauthorized` | Actor lacks capability to submit to declared lane | Step 3 |
| `AuthDenied` | Actor lacks capability for operationType on target entities | Step 3 |
| `AuthContextMismatch` | `authContext` declared an auth path that doesn't match actor's capability projection (e.g., `service` set but service not in `serviceAccess[]`; `task` set but task not in `ephemeralGrants[]` or target mismatch) | Step 3 |
| `StarlarkExecutionFailed` | Script raised an error or attempted forbidden I/O | Step 5 |
| `StarlarkExecutionTimeout` | Script exceeded execution time budget (NFR-P4) | Step 5 |
| `SchemaViolation` | MutationBatch failed DDL JSON Schema validation | Step 6 |
| `WriteScopeViolation` | Mutation outside declared `permittedCommands` for affected DDL | Step 6 |
| `SensitivityViolation` | Sensitive aspect attached to non-identity-anchored vertex | Step 6 |
| `EventSchemaViolation` | EventList contained event failing event DDL validation | Step 7 |
| `RevisionConflict` | Atomic batch rejected due to concurrent revision change; retries exhausted | Step 8 |
| `MetaLaneCollision` | DDL change conflicts with concurrent meta-lane mutation | Step 8 (meta lane only) |
| `CellMoved` | Multi-cell (Phase 3): the target vertex has been migrated to another cell and this cell has drained it; the write was not applied. `details.newCell` carries the cell the caller must refresh the routing index to and re-route through. The `410 Gone`-equivalent stray-write rejection (no data is lost — the caller re-submits to the correct cell). | Pre-step-1 (cell-router check); migration drain window only |
| `InternalError` | Unrecoverable Processor failure not covered by above codes | Any step |

Each code is paired with a human-readable `message` and structured `details` appropriate to the failure mode. The enumeration is extensible — Phase 2+ may add codes; existing codes are immutable contract.

### 2.8 Auth Context

Service-scoped operations and task-derived operations require auth information beyond the basic envelope. The optional `authContext` field carries this information, declaring which auth path the Processor should follow at commit step 3.

**Envelope shape with authContext:**

```json
{
  "requestId": "Rm7q3pntwzkfbcxv5p9j",
  "lane": "default",
  "operationType": "BookExecutiveCleaning",
  "actor": "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y",
  "authContext": {
    "service": "vtx.service.executive-cleaning-NanoID",
    "task": null,
    "target": null
  },
  "submittedAt": "2026-05-12T14:32:18.142Z",
  "payload": { "date": "2026-05-15", "slot": "morning" },
  "contextHint": { "reads": [ ... ] }
}
```

**Field semantics:**

| Field | When populated | Purpose |
|-------|----------------|---------|
| `authContext.service` | Service-scoped operations | Vertex key of the service the operation is invoked on. Processor scans `cap.<actor>.serviceAccess[]` for matching `service`. See Contract #6 §6.3. |
| `authContext.task` | Task-derived operations (FR56) | Vertex key of the task that justifies the temporary authorization. Processor scans `cap.<actor>.ephemeralGrants[]` for matching `taskKey` plus `target` plus `expiresAt > now`. |
| `authContext.target` | (a) Task-derived operations needing scope-target match; (b) platform operations with `scope: "self"` or `scope: "specific"` | The specific entity the operation acts on. For `scope: "self"`, Processor enforces `target == actor`. |

All three fields are optional. `null`, omitted, or the entire `authContext` block absent all mean "not applicable for that path."

**Processor dispatch at step 3:**

```
if authContext.task is set:
    look up ephemeralGrants[] entry where taskKey == authContext.task
    AND the entry's operationType matches the envelope's operationType
    AND the entry's target matches authContext.target
    AND expiresAt > now
    → allow or deny (AuthDenied / AuthContextMismatch)

elif authContext.service is set:
    look up serviceAccess[] entry where service == authContext.service
    AND allowedOperations[] contains the envelope's operationType
    → allow or deny

else:
    look up platformPermissions[] entry matching the envelope's operationType
    validate scope:
        scope=any    → allow
        scope=self   → require authContext.target == actor
        scope=owned  → deferred to Phase 2
    → allow or deny
```

Task auth takes precedence over service auth, which takes precedence over platform auth. An actor may hold multiple auth paths to the same operation; they explicitly declare which path they're invoking via `authContext`. This makes the auth path inspectable at the wire level and testable in adversarial suites.

**Phase 2 amendment — generic auth-hook dispatcher, one-key-per-path (Story 12.5, D-CONSUMER).** As the
bootstrap god-cypher decomposes into package-owned disjoint Capability-KV keys (Contract #6 §6.1, Epic
12), step-3 stops scanning sections of a single `cap.<actor>` document and instead **dispatches over a
data-driven registry**. The model (party-review-pinned):

- **Core owns a fixed set of matcher *kinds*** — the existing `task` (ephemeral-grant), `service`
  (service-access), and `platform` (platform-permission) logics become the seed matcher kinds,
  re-expressed with **identical** behavior. Matcher kinds are core Go; Lattice packages remain
  **data-only** and never ship matcher code.
- **A package declares, as install-time data**, which matcher kind authorizes its grant type and which
  **disjoint Capability-KV key** that path reads (+ the field mapping). The dispatch table is data, not
  a `switch` naming `task`/`service`.
- **One-key-per-path invariant (preserves the single-GET hot path):** path selection happens **before**
  the read (as today), and each path maps to **exactly one** disjoint key — so exactly one GET per
  `Authorize` call. **Two packages contributing the same path is a config error** (or requires upstream
  merge); the dispatcher never fans a single path into N reads. The denial-path `actorRoles` second
  read stays off the hot path.

The precedence order (task → service → platform) and the forgery-resistance property below are
unchanged. The dispatch pseudocode above describes the Phase-1 single-document form; the Phase-2 form
reads the path-specific disjoint key via the registered hook. Full shape: Contract #6 §6.1/§6.13 +
`cmd/processor/CONTRACT-AMENDMENT-REQUEST.md`.

**Forgery resistance:**

`authContext` is a *hint about which auth path to check*, not a claim of authorization. An actor can submit any value in `authContext.service` — but unless that service appears in their actual `serviceAccess[]` projection (produced by the Capability Lens), the check fails. The routing-via-`authContext` does not grant access; it only selects which subsection of the capability projection to consult. Bypass test suite (Story 1.11 / Story 3.x) MUST include test cases proving that mismatched `authContext` values are rejected.

**Worked examples:**

```json
// Service operation (penthouse resident books executive cleaning)
"authContext": { "service": "vtx.service.executive-cleaning-NanoID" }

// Task-derived (manager approves lease application)
"authContext": {
  "task": "vtx.task.Rm7q3pntwzkfbcxv5p9j",
  "target": "vtx.lease.Op4Nb2mPq6rTwzKxVyP7"
}

// Self-scoped platform operation (resident updates own email)
"authContext": { "target": "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y" }

// Unscoped platform operation (admin creates new DDL) — authContext omitted entirely
```

### 2.9 Implementation Notes

**For the AI agent implementing Story 1.5 (`internal/substrate`):**

- `package envelope` — Go struct definitions for `OperationEnvelope` and `OperationReply`, including the enumerated `Lane` and `Status` types and the `ErrorCode` enum. JSON marshaling with strict required-field validation (rejects unknown fields).
- Envelope JSON Schema file committed alongside Go types — used by SDK validation and by Processor's pre-step-1 envelope check.

**For the AI agent implementing Story 1.4 (Processor — Consume, Dedup, Auth Stub):**

- Pre-step-1: validate envelope against schema; on failure, return `EnvelopeMalformed` reply without further processing.
- Step 1: consume from the configured lane subject. Each Processor instance subscribes to one or more lane subjects per its configuration.
- `meta` lane consumer is configured with `MaxAckPending=1` (serialized); other lanes are configured for parallelism per deployment sizing.
- Step 2 (dedup): read `vtx.op.<requestId>`. If found with `isDeleted: false`, return `duplicate` reply with `originalCommittedAt` from the tracker. If found with `isDeleted: true`, treat as not-found (allow resubmission — operator-driven retry path).
- Step 3 (auth): two checks happen here — (a) actor capability for the lane, (b) actor capability for the operationType on the read/write set. Both come from Capability KV lookups.

**For the AI agent implementing Story 1.7 (Processor — Event Publication & Fault Injection):**

- Reply envelope publication happens **between step 8 (commit) and step 9 (events)**. If reply publication fails (NATS reply subject closed), the operation is still durably committed — log the failure to Health KV and proceed with event publication. Client will discover the commit via polling `opTrackerKey` on next attempt with the same requestId (dedup will return the now-committed tracker).
- Event publication failures after reply are recoverable via JetStream redelivery (the `core-operations` message isn't acked until step 10).
