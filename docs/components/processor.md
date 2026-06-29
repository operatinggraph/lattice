# Processor

**Component reference** | Audience: implementers + architects

---

## Overview

Processor is the sole authorized write surface to Core KV. Operations arrive
as JetStream messages on subjects `ops.<lane>.>`, flow through a deterministic
9-step commit pipeline, and result in atomic KV mutations plus asynchronously
published events. Each operation is either accepted (commit durable, reply sent,
message acked), rejected with a structured reply (message term'd), or retried
(transient failure, message nak'd). **There is no read API** — read-side concerns
belong to Refractor (lens projections) or direct KV reads via CLI. Nothing
outside this pipeline may write to Core KV.

---

## What this component owns

| Path | Role |
|------|------|
| `internal/processor/` | Pipeline logic — all 9 steps, Starlark sandbox, DDL cache, authorizer, hydrator, committer |
| `internal/processor/outbox/` | Durable outbox consumer + event publisher |
| `cmd/processor/` | Binary entry point; wires `MakePipeline` + JetStream consumer |

Key files:

- `commit_path.go` — `CommitPath.dispatch` runs the 9-step loop and returns an ack `Decision`; `SupervisedHandler` adapts it to a `substrate.ConsumerSupervisor` (the production delivery path); `HandleMessage` is the in-process adapter (test harness) that applies the `Decision` to a `jetstream.Msg`; `Deps` bundles all injected interfaces; `MakePipeline` is the production wiring entry point
- `step1_consume.go` — parses + validates the `OperationEnvelope` wire format
- `step3_auth.go` — `Authorizer` interface; `StubAuthorizer` (test-only); `CapabilityAuthorizer` (production default); `SelectAuthorizerArgs` wiring entry point
- `step3_denial_response.go` — `DenialResponseBuilder` for FR22 structured denial replies
- `step3_auth_trace.go` — `AuthTraceEmitter` for FR23 three-plane auth trace records in Health KV
- `ddl_cache.go` — in-memory DDL cache; populated at startup via `KVListKeys` over `vtx.meta.>`, re-read on `vtx.meta.*` mutations
- `starlark_runner.go` — `StarlarkRunner.Run`; compiles + executes the DDL's `.script` aspect; maps Starlark errors to typed `ScriptError`; injects the sandbox globals
- `starlark_builtins.go` — pure builtin modules injected into the Starlark sandbox (`nanoid`, `crypto`, `time`, `json`)
- `starlark_kv.go` — the `kv.Read(key)` builtin (Contract #2 §2.5 lazy on-demand Core KV read) + the `connKVReader` adapter backing it
- `script_context.go` — `ScriptContext` struct (incl. the `ScriptKVReader` seam); bridges hydrated state to Starlark globals
- `envelope.go` — `OperationEnvelope`, `Lane`, `ContextHint`, `AuthContext`, `ErrorCode` definitions; `ParseEnvelope` validates the wire contract
- `reply.go` — `OperationReply`, `BuildAcceptedReply*`, `BuildRejectedReply`, `BuildDuplicateReply`, `MarshalReply`
- `nfr_r1_test.go` — Gate 2 bypass test (no 9-step bypass; every write path verifiable)

---

## In-contracts (what it consumes)

| Contract | Source | Notes |
|----------|--------|-------|
| **Operation envelopes** (Contract #2) | JetStream `ops.<lane>.>` | Pulled by a durable JetStream push consumer; lane determines priority (default / meta / urgent / system) |
| **DDL meta-vertices** (Contract #1) | Core KV `vtx.meta.>` | Read into `DDLCache` at startup; cache is invalidated on any `vtx.meta.*` mutation to keep the pipeline current |
| **Capability KV** (Contract #6) | Capability KV bucket | Read at step 3 by `CapabilityAuthorizer`; key pattern `cap.identity.<actorId>`. A missing entry denies (`NoCapabilityEntry`, fail-safe). There is **no per-operation projection-freshness gate** — `projectedAt` is deterministic provenance, not a TTL; the bounded staleness window is an accepted risk backstopped operationally (see Refractor Capability-Lens health) and, in future, by Gateway token revocation. |

---

## Out-contracts (what it produces)

| Artifact | Destination | Notes |
|----------|-------------|-------|
| **Core KV mutations** (Contract #1 + #3) | Core KV bucket (`core-kv`) | Written as an atomic batch via `substrate.AtomicBatch`; each mutation is a `create`, `update`, or `tombstone` operation |
| **Events** (Contract #3 EventList) | JetStream `events.<domain>.<eventName>` subjects on `core-events` stream (every class is `<domain>.<eventName>`, enforced at step 7) | Persisted in the step-8 atomic batch (`vtx.op.<id>.events`) and published asynchronously by the durable outbox consumer via `substrate.PublishBatch` |
| **Idempotency tracker entries** (Contract #4) | Core KV at `vtx.op.<requestId>` | Written as part of the step-8 atomic batch; 24h TTL; provides step-2 dedup on re-delivery |
| **Operation replies** (Contract #2 §2.4) | Per-op reply-to inbox | `accepted` (post-step-8), `duplicate` (step-2 short-circuit), or `rejected` (any termination branch) |
| **Health KV signals** (Contract #5) | Health KV `health.processor.<instance>.*` | Heartbeat every 10s; per-op metrics (OpsConsumed / OpsCommitted / OpsDuplicates / OpsRejected / OpsMalformed); step-3 latency; auth trace records; claim-attempt outcomes; alerts under `health.alerts.security.*` |
| **`OperationReply.PrimaryKey` + `Revisions`** | Inline in accepted reply | Commit-trace identifiers the Processor itself produced. `primaryKey` is the operation's single principal entity, surfaced via the closed `response: {"primaryKey": <key>}` script-return schema and **validated by the Processor to be within the committed write footprint** (a committed key, or the vertex root of one). `revisions` is the per-key revision map (its key set IS the committed mutation set). There is no arbitrary `detail` map: the write reply is not a read channel and carries no script-returned data or secrets (Contract #2 §2.4 / §2.7). |

---

## The 9-step write path

The operation consumer runs on a `substrate.ConsumerSupervisor` — the same
supervised pump Loom/Weaver/Refractor use. A single `processor-main` durable
filtered to the four lane subjects (`ops.{default,urgent,system,meta}`) delivers
each operation to `CommitPath.dispatch`, which runs the steps below, publishes any
client reply, and returns an ack `Decision` the supervisor applies — the supervisor
owns disposition, the commit path owns the reply. Each message exits with one of
five outcomes: `accepted`, `duplicate`, `rejected`, `malformed`, or `retryable`
(`NakWithDelay` — redelivered on a bounded backoff floor, never a hot-loop). The
in-process test harness drives the same `dispatch` through `HandleMessage`,
applying the returned `Decision` to the JetStream message itself (`Ack` via the
explicit step-9 Acker boundary). *(A single all-lanes consumer is a Phase-1
simplification; per-lane consumers — real per-lane `lane_lag`, independent
draining, `meta` serialized — are the design-of-record adoption tracked in the
Lattice backlog.)*

| Step | Name | What happens |
|------|------|-------------|
| 1 | **Consume** | `parseEnvelopeFromBody` — deserializes `OperationEnvelope` from the delivered message body; validates `requestId` (must be a valid 20-char NanoID), `lane` (must be a recognized enum value), `operationType`, `actor`, `submittedAt`, and `payload`. Malformed → term with reason; if a reply inbox is present, reply with `EnvelopeMalformed` code. |
| 2 | **Dedup** | `CheckDedup` — reads the tracker key `vtx.op.<requestId>` from Core KV. If already present, emit `DuplicateDetected` log + health marker, reply with `duplicate`, return `Ack`. On KV error, return `NakWithDelay` (redeliver on the backoff floor). |
| 3 | **Auth** | `Authorizer.Authorize` — in production, `CapabilityAuthorizer` reads `cap.identity.<actorId>` from Capability KV, checks lane authorization + permission match. A missing entry denies (`NoCapabilityEntry`). There is no projection-freshness gate: a stale-but-permission-matching projection is allowed; `projectedAt` is recorded as provenance in the auth trace, not compared against a ceiling. `ephemeralGrants[].expiresAt` (a real grant TTL) is still enforced. Denied → term with reason, reply with structured denial (FR22 when `DenialBuilder` is wired). Auth trace emitted fire-and-forget via `AuthTraceEmitter` (FR23) for both allowed and denied decisions when configured. |
| 4 | **Hydrate** | `Hydrator.Hydrate` — loads `contextHint.Reads` (explicit per-key reads) from Core KV into the `HydratedState` map. The graph topology an op needs is delivered as declared command parameters, not discovered by scanning: a Lens projects topology into its own bucket, the client reads the lens, and the resulting keys travel back in `ContextHint.Reads`. The script validates each declared key (envelope class, endpoint touch, not tombstoned) before acting on it. |
| 5 | **Execute** | `Executor.Execute` — compiles and runs the DDL's `.script` aspect in the Starlark sandbox via `StarlarkRunner.Run`. Produces `ScriptResult{Mutations, Events, ResponseDetail}`. Timeout: 250ms wall budget + 1,000,000 step limit. |
| 6 | **Validate** | `Validator.Validate` — checks `permittedCommands` (operation type must be in the DDL's list), `sensitiveAspectScope` (script may not create underscore-prefixed aspects except system-reserved ones), and key-pattern checks. `DDLViolation` → term, reply with `DDLViolation` code. |
| 7 | **Materialize events** | Assigns per-event NanoIDs to events in `ScriptResult.Events` before the commit. NanoIDs are generated via `substrate.NewNanoID()` — entropy is from `crypto/rand`, not PCG (the script's `nanoid` global uses a PCG seeded from the requestId for deterministic per-script behavior; step 7 uses real entropy). **Enforces the event-domain model:** every event `class` must be `<domain>.<eventName>` (Contract #3 §3.4) — a dot-free class (no domain segment) is rejected; the Event document's `domain` field is set from the class's first segment. |
| 8 | **Commit** | `Committer.Commit` — calls `substrate.AtomicBatch` on the `core-kv` bucket. Batch includes all mutation ops + the tracker `vtx.op.<requestId>` as a create-only entry + the faithful EventList at `vtx.op.<id>.events`. Revision conditions on update ops; any condition failure → `ErrAtomicBatchRejected`. If the tracker was the conflicting key (concurrent re-delivery), short-circuit as duplicate. If a business mutation conflicted → `RevisionConflict` reply, term. On transient failure: `NakWithDelay` (redeliver on the backoff floor). |
| 9 | **Dispose** | The commit path returns its ack `Decision` (`Ack` on success) after the reply is published; the `ConsumerSupervisor` applies it. The in-process adapter applies the same `Decision` to the JetStream message, routing `Ack` through the explicit step-9 `Acker` boundary (the NFR-R1 crash-at-ack fault-injection seam). Ack failure is non-fatal (commit + reply already durable); the message redelivers and step-2 dedup short-circuits. |

**Event publishing (asynchronous, not a numbered step).** The faithful EventList
persisted in the step-8 atomic batch as `vtx.op.<id>.events` is published by the
durable outbox consumer (`internal/processor/outbox`) to `events.<domain>.<eventName>`
on `core-events` (the class is `<domain>.<eventName>`, so the subject's second segment
is the domain consumers partition on), acking only after a confirmed publish. Because
the EventList is the exact list the script returned (not reconstructed from committed
keys), redelivery republishes the *real* events.

---

## Starlark sandbox

The script in the DDL's `.script` aspect is compiled and executed in a
restricted Starlark environment for each operation. The sandbox is verified
by Gate 2 (`nfr_r1_test.go`).

### Globals injected

| Name | Type | Description |
|------|------|-------------|
| `state` | `stateMapValue` (dict-like) | Hydrated Core KV map; keys are KV key strings, values are `{key, class, isDeleted, data, [vertexKey, localName]}` structs. Supports `state[key]`, `key in state`, and `for k in state`. |
| `op` | struct | Envelope view: `requestId`, `lane`, `operationType`, `actor`, `submittedAt`, `payload` (parsed dict). |
| `ddl` | dict | Resolved DDL map: `{canonicalName, permittedCommands}` per DDL entry. |
| `nanoid` | module | `nanoid.new()` — PCG-seeded deterministic NanoID generator (seed derived from `requestId` for reproducibility in tests). |
| `crypto` | module | `crypto.sha256(s) -> hex string`, `crypto.sha256NanoID(s) -> NanoID`, `crypto.constant_time_equal(a, b) -> bool`. Side-effect-free; used by `ClaimIdentity` for claim-key validation. |
| `json` | module | Standard Starlark `json.decode(s)` / `json.encode(v)`. Pure (no I/O, deterministic); used where a script parses a JSON payload field into a structured dict (e.g. a Lens `.spec`). |
| `kv` | module | `kv.Read(key) -> doc-struct \| None` — Contract #2 §2.5 lazy on-demand Core KV read. The **one non-pure builtin**: serves a `contextHint`-prefetched key from the hydrated `state` cache (no round-trip) and otherwise does a single live key GET. Absent / hard-tombstoned → `None`; a logically-deleted vertex (`isDeleted=true`) → a present doc carrying the flag. Bounded by the wall budget. The opt-in read-before-create idempotency seam — **not** a scan or read-model hook (read models are lenses, P5). |

#### `kv.Read` semantics (§2.5)

- **Cache-first.** A key listed in `contextHint.reads` is pre-fetched at step 4 and served from `state` at the step-4 OCC snapshot — `kv.Read` cannot force a fresher re-read of an already-hydrated key (echoing the snapshot revision as `expectedRevision` is what keeps the commit's OCC check sound). A key *not* declared falls through to a single on-demand GET (incurs latency, §2.5).
- **Absence is graceful.** Unlike a `contextHint` miss (a fatal `HydrationMiss`), `kv.Read` of an absent / hard-tombstoned key returns `None`, so a script can branch present-vs-absent — the read-before-create pattern a `createIfAbsent` mutation cannot express (events stay coherent with mutations because the script decides both in one branch).
- **Non-deterministic by design.** It reads *live* state, so a replayed (at-least-once) operation can branch differently. That is intentional: the Processor — not replay determinism — is the idempotency authority; the deterministic id + the `CreateOnly` commit backstop resolve the publish→commit race (Contract #10 §10.3 / [userTask-dispatch-idempotency design](../../_bmad-output/implementation-artifacts/usertask-dispatch-idempotency-design.md) §4.3–4.4).

### Forbidden

- `load(...)` — `Thread.Load` is nil; compile-time rejection.
- `os`, `http`, and any other undeclared global — caught as `SandboxViolation` (compile-time resolve error from `starlark.SourceProgram`). (`time` is bound, but only as the pure `time.rfc3339_*` helpers — never the host clock.)
- Arbitrary NATS / I/O — Starlark cannot open connections, scan, or write. The **only** substrate touch is the read-side `kv.Read(key)` single-key GET (§2.5); all **side effects** (writes, events) are still declared via the `mutations` + `events` + `response` return dict (Contract #3 §3.7) and applied by the committer at step 8.

### Return shape

```
{
  "mutations": [{op: "create"|"update"|"tombstone", key: "...", document: {...}}],
  "events":    [{class: "...", data: {...}}],
  "response":  {"primaryKey": "..."}   # optional; CLOSED schema — only primaryKey
                                       # permitted; must be a committed key or the
                                       # vertex root of one. Surfaced as
                                       # OperationReply.PrimaryKey.
}
```

### Error codes

| Code | Cause |
|------|-------|
| `SandboxViolation` | Reference to undefined global (`os`, `time`, etc.) or `load` call |
| `ScriptError` | Runtime fail() call, syntax error, division by zero, etc. |
| `ScriptTimeout` | Wall budget (250ms) exceeded |
| `InvalidReturnShape` | Script did not return a dict, or `mutations`/`events` are malformed |
| `ClaimKeyInvalid` | `fail("ClaimKeyInvalid: <outcome>")` from `ClaimIdentity` script — generic code, no detail exposed to caller (NFR-S6 anti-enumeration) |

---

## Capability change operations (FR53)

### `.compensation` aspect as the FR53 contract surface

Every capability-change DDL meta-vertex carries a sixth self-description
aspect named `.compensation` (stored at `<metaKey>.compensation` in Core KV).
This aspect encodes the compensating (inverse) operation as a template
reference so that an operator or AI agent can construct a rollback without any
new Processor reply fields.

**The Processor commit response carries no compensation field — by design:**

1. A compensation field would embed routing logic inside the Processor response,
   coupling the write path to compensation semantics it should not own.
2. It would imply the Processor knows the "inverse" of every operation, violating
   the single-responsibility principle of the commit path.
3. It would require new `OperationReply` fields, contradicting Guardrail 1 (no new
   envelope fields).

Instead, the compensation contract lives in the DDL meta-vertex as a sixth
self-description aspect. The compensating operation is constructed
**client-side** by reading this aspect via `aiagent.Traverser.ReadCompensation`,
then substituting field references from the original commit response. No Processor
code participates in the rollback.

#### `.compensation` aspect shape (canonical)

```json
{
  "class": "compensation",
  "vertexKey": "vtx.meta.<NanoID>",
  "localName": "compensation",
  "isDeleted": false,
  "data": {
    "inverseOperationType": "TombstoneMetaVertex",
    "payloadTemplate": {
      "metaKey": "{{primaryKey}}"
    },
    "revisionTemplate": {
      "metaKey": "{{revisions[primaryKey]}}"
    }
  }
}
```

Template variable substitution is **client-side only** (Guardrail 2 — no new
Processor read surface):
- `{{primaryKey}}` → value of `OperationReply.PrimaryKey` (the operation's
  principal entity — e.g. the meta-vertex key — validated by the Processor to be
  within the committed write footprint).
- `{{revisions[primaryKey]}}` → value of `OperationReply.Revisions[<primaryKey>]`
  (resolves only for create ops, where `primaryKey` is itself a committed key) —
  the per-key NATS revision from the atomic batch commit.
- `{{payload.<field>}}` → value of the forward op's own request payload field
  (used where the inverse op has no single principal key — e.g. the
  InstallPackage→UninstallPackage pair sources `name` from `{{payload.name}}`).

#### Kernel meta-vertex operation pairing

The `.compensation` aspect surface covers the kernel meta-vertex operations:

| Forward operation | Compensating operation | Notes |
|---|---|---|
| `CreateMetaVertex` | `TombstoneMetaVertex` | Tombstones the newly-created meta-vertex. `expectedRevision` from commit response prevents racing compensating ops. |
| `UpdateMetaVertex` | `UpdateMetaVertex` | Restores the prior values of exactly the fields the forward op changed (see [`UpdateMetaVertex` field set](#updatemetavertex-field-set)). The `.compensation` aspect stores those prior values concretely (read from hydrated state at script execution time). |
| `TombstoneMetaVertex` | none (irreversible) | The tombstone cascades to the root **and every aspect** (`.compensation` included), so no live aspect survives the delete. There is no machine-readable compensation; re-creating the meta-vertex is the operator's responsibility (a fresh `CreateMetaVertex` with the prior payload mints a new NanoID). |

Domain packages that ship their own forward/inverse op pairs (e.g. `rbac-domain`:
`CreateRole`↔`TombstoneRole`, `AssignRole`↔`RevokeRole`,
`GrantPermission`↔`RevokePermission`) handle reversal through those paired
operations, **not** through the `.compensation` aspect — that mechanism is specific
to the kernel meta-vertex ops above.

#### Client-side revert flow

Given a forward `CreateMetaVertex` op that committed successfully:

1. Operator (or AI agent) has: `metaKey` (from `OperationReply.PrimaryKey`) and
   `revisions[metaKey]` (from `OperationReply.Revisions`).
2. Operator calls `aiagent.Traverser.ReadCompensation(ctx, metaKey)` —
   reads `<metaKey>.compensation` from Core KV.
3. Operator substitutes template variables with commit-response values to
   construct the `TombstoneMetaVertex` payload with `expectedRevision`.
4. Operator submits via Processor (same write path, same lane).
5. State reverts; Capability KV reprojection updates within NFR-P3 lag; no
   platform restart required.

#### Conflict handling

The `TombstoneMetaVertex` and `UpdateMetaVertex` Starlark scripts accept an
optional `expectedRevision` integer field. When present:
- The Starlark pre-flight check validates it is an integer.
- The revision condition is propagated to `mutation["expectedRevision"]`, which
  the `CommitterImpl.Commit` at step 8 translates to `BatchOp.HasRevision = true`
  and `BatchOp.Revision = *m.ExpectedRevision` (see
  `internal/processor/step8_commit.go`). This gives atomic, substrate-level
  revision enforcement.
- If the caller passes `force: true` in the payload, the revision assertion is
  skipped (last-writer-wins).

Revision mismatch surfaces as `RevisionConflict` at the NATS layer — the same
error code returned for any other revision-conditioned update conflict.

### `UpdateMetaVertex` field set

`UpdateMetaVertex` hot-fixes a meta-vertex's self-description aspects **in
place**, preserving the vertex's `metaKey` identity. It never mints a new
NanoID, so every caller holding the old key keeps working — there is no need
for a `TombstoneMetaVertex` + `CreateMetaVertex` cycle to correct a DDL/Lens
script.

**Updatable fields** (each optional; mutate only those present in the payload):

| Meta-vertex class | Updatable payload fields | Aspect written | Validation |
|---|---|---|---|
| `meta.ddl.*` | `description` | `.description` `{"text": v}` | non-empty string |
| `meta.ddl.*` | `script` | `.script` `{"source": v}` | non-empty string |
| `meta.ddl.*` | `permittedCommands` | `.permittedCommands` `{"commands": v}` | list of strings |
| `meta.ddl.*` | `inputSchema` | `.inputSchema` `{"schema": v}` | non-empty string |
| `meta.ddl.*` | `outputSchema` | `.outputSchema` `{"schema": v}` | non-empty string |
| `meta.ddl.*` | `fieldDescription` | `.fieldDescription` `{"fieldDescriptions": v}` | dict |
| `meta.ddl.*` | `examples` | `.examples` `{"examples": v}` | list |
| `meta.lens` | `description` | `.description` `{"text": v}` | non-empty string |
| `meta.lens` | `spec` | `.spec` (decoded dict, verbatim) | JSON object string with `cypherRule`, `targetType`, `targetConfig` — same validation as the `CreateMetaVertex` lens branch |

**Identity and immutability rules:**

- **`metaKey` is preserved.** It is read from the payload and reused verbatim;
  the vertex root key and `canonicalName` are untouched.
- **`canonicalName` is immutable.** It is the stable logical identity. If the
  caller includes it in the payload it is **ignored** — neither mutated nor
  treated as an error.
- **`compensation` is script-managed**; callers never set it directly.
- **An empty update is rejected.** If no updatable field is present (e.g. only
  `metaKey`, or only the ignored `canonicalName`), the script fails with
  `InvalidArgument: UpdateMetaVertex: no updatable fields provided`. Absent
  fields are never blanked.

**`ContextHint.Reads` requirement.** Beyond the vertex root key (needed for the
liveness check), the caller MUST declare `<metaKey>.<field>` in
`ContextHint.Reads` for **each field it intends to update**, so the Hydrator
loads the prior aspect document into `state`. The script reads those prior
values to build the `.compensation` `payloadTemplate`, which carries `metaKey`
plus the prior value of **only the changed fields**. If a changed field's prior
value is absent or malformed in state (typically a missing `ContextHint.Reads`
declaration), the forward op **fails** with `InvalidArgument: <field>: prior
value unavailable for compensation` rather than baking a `null` prior — a null
prior would produce an un-submittable rollback. For `spec`, the prior `.spec`
aspect dict is re-encoded to a JSON string so a compensating `UpdateMetaVertex`
can resubmit it.

**`expectedRevision` (OCC) — single-aspect assertion.** An update may touch
any subset of aspects, and each aspect has its own independent NATS revision
sequence. `expectedRevision` is therefore applied to the `make_update` of the
**first present field** in the canonical order `description, script,
permittedCommands, inputSchema, outputSchema, fieldDescription, examples,
spec` — never to `.compensation` (independent sequence; would cause spurious
conflicts). Multi-aspect atomic OCC across several changed aspects is a known
limitation.

### `TombstoneMetaVertex` cascade and cache eviction

A tombstone must leave Core KV and the DDL cache fully coherent: no orphaned
aspect keys and no stale cache entry that keeps hydrating a deleted class.

**Cascade tombstone (Starlark `TombstoneMetaVertex` branch).** After the
`vertex_alive` liveness check, the script emits a `make_tombstone` for the root
`vtx.meta.<id>` key **and for every aspect key of the meta-vertex's class**. The
class is read from the hydrated root (`getattr(root, "class")`); `meta.lens`
selects the lens aspect set, everything else the DDL set:

| Class | Aspect keys cascaded (in addition to the root) |
|---|---|
| `meta.ddl.*` | `.canonicalName`, `.permittedCommands`, `.description`, `.script`, `.inputSchema`, `.outputSchema`, `.fieldDescription`, `.examples`, `.compensation` |
| `meta.lens` | `.canonicalName`, `.description`, `.spec`, `.compensation`, `.targetBucket`, `.cypherRule`, `.outputSchema` (union of DDL-created and primordial-seeded lens aspects — tombstoning an aspect a given lens lacks writes a harmless `isDeleted` entry) |

`.compensation` is tombstoned like any other aspect — no Go code reads
`.compensation` from Core KV post-commit (the compensating-op contract is
resolved client-side from the forward op's reply, Guardrail 1), so removing it
breaks nothing and yields a fully-coherent delete.

The root tombstone is `mutations[0]`, so `expectedRevision` (when present, and
not bypassed by `force: true`) is asserted on the **root only**. Aspect
tombstones are unconditional: each aspect has an independent NATS revision
sequence, so a shared revision assertion would cause spurious conflicts. The
`MetaVertexTombstoned` event is emitted with the `metaKey`.

> Residual: aspect keys orphaned by tombstones committed **before** this cascade
> shipped are not retroactively cleaned; a background GC sweep is out of scope.

**Cache eviction (`DDLCache.loadMetaVertex`).** The cached root document carries
an `isDeleted` flag. Immediately after unmarshaling the root — **before** any
aspect read or `canonicalName` resolution — a tombstoned root (`isDeleted ==
true`) returns absent (`ref, false, nil`). Because `Invalidate` re-runs
`loadMetaVertex`, this drops the entry from both `byName` and `byMetaPK` and
never re-inserts it; a direct load of a tombstoned vertex also reports absent.
The net effect: after a `TombstoneMetaVertex` commits, `Lookup` /
`LookupByMetaKey` report absent and follow-up operations on the class are no
longer hydrated (they fall through to the permissive-default / `NoDDLForClass`
path).

**Step-8 invalidation dedup.** A cascade emits many `vtx.meta.<id>.*` mutations
that all normalize to the same 3-segment root. The post-commit invalidation loop
collapses the committed `vtx.meta.*` mutation keys to their distinct roots and
calls `DDLCache.Invalidate` **once per root** (`Invalidate` is idempotent; this
just avoids redundant Core KV reads).

---

## Package install / uninstall

Capability-package install **and** uninstall route through the Processor as two
primordial kernel operations — `InstallPackage` / `UninstallPackage` — rather than
writing to the substrate directly. They are seeded as protected primordial DDL
meta-vertices (`internal/bootstrap/install_ddl.go`,
`internal/bootstrap/primordial.go`). The full install/uninstall contract is in
[`docs/contracts/08-package-install.md`](../contracts/08-package-install.md); this
section covers the Processor-side behavior.

**Thin script over a fat manifest.** The client (`internal/pkgmgr`) pre-computes
the complete mutation set — every DDL/lens/permission/grant/role/index key — and
ships it as **logical documents** (`{class, data, isDeleted}`, no provenance) in
the op payload. The kernel script iterates that set, enforces guardrails, and
emits it as the op's mutations. The Processor stamps `createdAt`/`createdBy`/
`createdByOp` at step 8 from the install actor, so installed entities carry real
provenance authored by the install actor.

**Install guardrails** (`InstallPackage` is privileged — it must not be an
arbitrary-write backdoor):

- key-shape — every key matches an allowed Contract #1 pattern (`vtx.<type>.<id>`
  `[.aspect]`, `lnk.<…>`); anything else is rejected;
- protected-key — a key whose hydrated root carries `data.protected == true` is
  rejected (installs may not overwrite kernel entities);
- system-aspect — no aspect `localName` may start with `_` (mirrors the step-6
  `sensitiveAspectScope` convention);
- create-only — every install mutation op must be `create`.

**Cache coherence (no restart).** All mutations land in ONE step-8 atomic batch.
The existing step-8 `vtx.meta.*` invalidation fires in-commit for the DDL
meta-vertices in that batch, so a class the package just declared is usable
immediately on the same running Processor — no restart, no manual refresh. (Test:
`packages/rbac-domain/install_flow_test.go::TestInstallFlow_M5B2_DomainOpWithoutRestart`
installs `rbac-domain` against a DDL cache that did not contain the `rbac` class at
refresh time, then commits a `CreateRole` op on that just-declared class.)

**Uninstall** reads the package's `.manifest` aspect (`declaredKeys`) and submits
`UninstallPackage`, which tombstones each declared key (cascade-style) and rejects
any protected key (defense in depth). The script accepts an optional per-key
`expectedRevision` for OCC; the client currently submits tombstones
**unconditionally** — see the [package-install contract](../contracts/08-package-install.md)
and `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` for the documented window and the
per-key-revision follow-up.

## Kernel protection (§3.4)

Primordial kernel entities are **protected** from update and tombstone. Bootstrap
seeds `protected: true` in the **root vertex document `data`** (not a separate
aspect) of: the meta-root DDL, the `InstallPackage` / `UninstallPackage` DDLs,
both Capability lenses, the operator role, the primordial admin identity, and the
primordial meta-permissions.

The meta-root DDL's `UpdateMetaVertex` and `TombstoneMetaVertex` branches read the
hydrated root and, when `data.protected == true`, `fail("ProtectedMetaVertex:
<key>")` — so an operation cannot disable auth (the Capability lens) or the kernel
(the meta-root DDL) by tombstoning or rewriting it. `UninstallPackage` applies the
same rejection to any declared key whose root is protected. (Test:
`packages/rbac-domain/install_flow_test.go::TestInstallFlow_ProtectedMetaVertexRejected`
asserts both `TombstoneMetaVertex` and `UpdateMetaVertex` against the protected
meta-root DDL are rejected and the target is left unmutated.)

The caller must declare the target `metaKey` in `ContextHint.Reads` (already
required by the `vertex_alive` liveness check), so the root document — and its
`protected` flag — is in the script's hydrated `state`.

---

## Failure modes

| Failure | Where | Resolution |
|---------|-------|------------|
| `ConflictError` | Step 8 revision-condition fail | Bubble `RevisionConflict` reply to caller; term |
| `DDLViolation` | Step 6 | Reply with `DDLViolation` code; term (no retry) |
| `SandboxViolation` / `ScriptError` | Step 5 | Reply with `ScriptFailed` code; term |
| `ScriptTimeout` | Step 5 | Reply with `ScriptFailed` code; term |
| `HydrationError` | Step 4 | Reply with `HydrationFailed` code; term |
| `AuthDenied` | Step 3 | Reply with `AuthDenied` / `LaneUnauthorized` / `AuthContextMismatch` code; term; ack (no retry — this is a final decision) |
| `AuthInfrastructureFailure` | Step 3 | `InternalError` reply; nak (retryable) |
| `PublicationError` | Outbox publish | Nak; outbox consumer redelivers and republishes the persisted EventList (at-least-once) |
| `MalformedEnvelope` | Step 1 | Reply with `EnvelopeMalformed` code (if reply inbox present); term |

---

## Auth modes

| Mode | Behavior |
|------|----------|
| `AuthModeCapability` (default) | Real `CapabilityAuthorizer`; reads Capability KV; checks lane + permission (+ `ephemeralGrants` expiry). No projection-freshness gate. |
| `AuthModeStub` | `StubAuthorizer`; always allows; emits `WARN` log + Health KV alert every 1000 calls. Test/dev only. |

The auth mode defaults to `AuthModeCapability`. `LATTICE_AUTH_MODE=stub` opts back
in to the stub; production deployments that enable stub receive visible
degradation signals in Health KV dashboards.

---

## Principles (binding)

- **Sole authorized write surface** (NFR-S2): every Core KV mutation passes through all 9 steps. Gate 2 (`nfr_r1_test.go`) verifies no bypass path exists.
- **No bypass**: even for capability management operations, mutations enter through the operation write path, not via direct KV writes.
- **Idempotent under retry**: the step-8 tracker provides dedup; re-delivered operations that already committed short-circuit at step 2 and receive a `duplicate` reply.
- **ContextHint is surgical**: `contextHint.Reads` specifies per-key pre-loads — the script never scans Core KV. Topology is discovered by the client (via a Lens) and declared as read keys.
- **The reply is not a read channel**: the only script-influenced reply field is `primaryKey`, drawn from the closed `response: {"primaryKey": <key>}` schema and validated to be within the committed write footprint (a committed key or the vertex root of one). There is no arbitrary `detail` map; read-derived signals travel on business events, and one-time secrets are never returned (Contract #2 §2.7).
- **Starlark cannot touch NATS**: all side effects are declared via the mutations + events return shape (Contract #3 §3.7).

---

## What's deferred

- **Read-path authorization** (🔭 Designed — ratified 2026-06-27, build-pending): the write path is capability-checked at step 3 (Refractor lenses produce Capability KV; the Processor reads it). Authorizing read-side queries directly — e.g. CLI / Gateway reads and the `cap.svc` service-access path — is the **D1** design: Postgres-RLS as the enforcement boundary, a minimal JWT read-actor seam, and a decomposed Capability-Read lens (core base + per-package read-grant lenses unioned via `actor_read_grants`).
- **Multi-cell routing** (Phase 3): the current pipeline is single-cell; operation routing across cells is Phase 3.
- **NATS account-level auth** (🔭 Designed — ratified 2026-06-27): the current connection uses no NATS account-level auth. NATS account-level write restriction on Capability KV — substrate-level enforcement beneath the overwrite-by-reprojection guarantee — is the **NATS account write-restriction** design (per-component NKey users; only the Processor's connection may write `$KV.core-kv.>`); **Fire 1 shipped** (the dark, no-op credential seam, `75e9acc`), the enforcement turn-on (Fire 2) is pending.
- **Multi-aspect atomic OCC** for `UpdateMetaVertex`: `expectedRevision` is asserted on a single aspect; atomic OCC across several changed aspects in one update is deferred.
