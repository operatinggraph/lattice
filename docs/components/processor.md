# Processor

**Component reference** | Audience: implementers + architects | Last verified: 2026-05-19

---

## Overview

Processor is the sole authorized write surface to Core KV. Operations arrive
as JetStream messages on subjects `ops.<lane>.>`, flow through a deterministic
10-step commit pipeline, and result in atomic KV mutations plus published
events. Each operation is either accepted (commit durable, reply sent, message
acked), rejected with a structured reply (message term'd), or retried
(transient failure, message nak'd). **There is no read API** — read-side
concerns belong to Refractor (lens projections) or direct KV reads via CLI.
Nothing outside this pipeline may write to Core KV.

---

## What this component owns

| Path | Role |
|------|------|
| `internal/processor/` | Pipeline logic — all 10 steps, Starlark sandbox, DDL cache, authorizer, hydrator, committer, event publisher |
| `cmd/processor/` | Binary entry point; wires `MakePipeline` + JetStream consumer |

Key files:

- `commit_path.go` — `CommitPath.HandleMessage` drives the 10-step loop; `Deps` bundles all injected interfaces; `MakePipeline` is the production wiring entry point
- `step1_consume.go` — parses + validates the `OperationEnvelope` wire format
- `step3_auth.go` — `Authorizer` interface; `StubAuthorizer` (test-only after Story 3.3); `CapabilityAuthorizer` (production default); `SelectAuthorizerArgs` wiring entry point
- `step3_denial_response.go` — `DenialResponseBuilder` for FR22 structured denial replies (Story 3.4)
- `step3_auth_trace.go` — `AuthTraceEmitter` for FR23 three-plane auth trace records in Health KV (Story 3.5)
- `ddl_cache.go` — in-memory DDL cache; populated at startup via `KVListKeys` over `vtx.meta.>`, re-read on `vtx.meta.*` mutations
- `starlark_runner.go` — `StarlarkRunner.Run`; compiles + executes the DDL's `.script` aspect; maps Starlark errors to typed `ScriptError`
- `starlark_builtins.go` — builtin modules injected into the Starlark sandbox (`nanoid`, `crypto`, `strings`)
- `script_context.go` — `ScriptContext` struct; bridges hydrated state to Starlark globals
- `envelope.go` — `OperationEnvelope`, `Lane`, `ContextHint`, `AuthContext`, `ErrorCode` definitions; `ParseEnvelope` validates the wire contract
- `reply.go` — `OperationReply`, `BuildAcceptedReply*`, `BuildRejectedReply`, `BuildDuplicateReply`, `MarshalReply`
- `nfr_r1_test.go` — Gate 2 bypass test (no 10-step bypass; every write path verifiable)

---

## In-contracts (what it consumes)

| Contract | Source | Notes |
|----------|--------|-------|
| **Operation envelopes** (Contract #2) | JetStream `ops.<lane>.>` | Pulled by a durable JetStream push consumer; lane determines priority (default / meta / urgent / system) |
| **DDL meta-vertices** (Contract #1) | Core KV `vtx.meta.>` | Read into `DDLCache` at startup; cache is invalidated on any `vtx.meta.*` mutation to keep the pipeline current |
| **Capability KV** (Contract #6) | Capability KV bucket | Read at step 3 by `CapabilityAuthorizer`; key pattern `cap.identity.<actorId>` (Phase 1 shape). A missing entry denies (`NoCapabilityEntry`, fail-safe). There is **no per-operation projection-freshness gate** — `projectedAt` is deterministic provenance, not a TTL; the bounded staleness window is an accepted risk backstopped operationally (see Refractor Capability-Lens health) and, in future, by Gateway token revocation. |
| **Adjacency KV** | `refractor-adjacency` | Not yet read by Processor; targeted for Story 4.6 `MergeIdentity` inbound-link enumeration (Phase 2 candidate) |

---

## Out-contracts (what it produces)

| Artifact | Destination | Notes |
|----------|-------------|-------|
| **Core KV mutations** (Contract #1 + #3) | Core KV bucket (`core-kv`) | Written as an atomic batch via `substrate.AtomicBatch`; each mutation is a `create`, `update`, or `tombstone` operation |
| **Events** (Contract #3 EventList) | JetStream `events.<class>` subjects on `core-events` stream | Published as an unconditional `substrate.PublishBatch` at step 9 after the commit is durable |
| **Idempotency tracker entries** (Contract #4) | Core KV at `vtx.op.<requestId>` | Written as part of the step-8 atomic batch; 24h TTL; provides step-2 dedup on re-delivery |
| **Operation replies** (Contract #2 §2.4) | Per-op reply-to inbox | `accepted` (post-step-8), `duplicate` (step-2 short-circuit), or `rejected` (any termination branch) |
| **Health KV signals** (Contract #5) | Health KV `health.processor.<instance>.*` | Heartbeat every 10s; per-op metrics (OpsConsumed / OpsCommitted / OpsDuplicates / OpsRejected / OpsMalformed); step-3 latency; auth trace records (Story 3.5); claim-attempt outcomes (Story 4.3); alerts under `health.alerts.security.*` |
| **`OperationReply.Detail`** | Inline in accepted reply | Carries **commit-trace data only** — mutation count, event count, revision map, traceId. NOT business data. The script's `response` return key populates this field (Story 4.2). Sensitive tokens (e.g. claim keys) may appear here — field is NOT logged (NFR-S6/S7). The pre-correction Epic 4 usage as a business-data channel is being walked back in Story 4.6. |

---

## The 10-step write path

Each message delivered by the JetStream consumer enters `CommitPath.HandleMessage`
and exits with one of five outcomes: `accepted`, `duplicate`, `rejected`,
`malformed`, or `retryable` (nak for re-delivery).

| Step | Name | What happens |
|------|------|-------------|
| 1 | **Consume** | `parseEnvelopeFromMessage` — deserializes `OperationEnvelope` from the JetStream message; validates `requestId` (must be a valid 20-char NanoID), `lane` (must be a recognized enum value), `operationType`, `actor`, `submittedAt`, and `payload`. Malformed → term with reason; if a reply inbox is present, reply with `EnvelopeMalformed` code. |
| 2 | **Dedup** | `CheckDedup` — reads the tracker key `vtx.op.<requestId>` from Core KV. If already present, emit `DuplicateDetected` log + health marker, reply with `duplicate`, ack, return. On KV error, nak for re-delivery. |
| 3 | **Auth** | `Authorizer.Authorize` — in production, `CapabilityAuthorizer` reads `cap.identity.<actorId>` from Capability KV, checks lane authorization + permission match. A missing entry denies (`NoCapabilityEntry`). There is no projection-freshness gate (Story 1.5.4): a stale-but-permission-matching projection is allowed; `projectedAt` is recorded as provenance in the auth trace, not compared against a ceiling. `ephemeralGrants[].expiresAt` (a real grant TTL) is still enforced. Denied → term with reason, reply with structured denial (FR22 when `DenialBuilder` is wired). Auth trace emitted fire-and-forget via `AuthTraceEmitter` (FR23) for both allowed and denied decisions when configured. |
| 4 | **Hydrate** | `Hydrator.Hydrate` — loads `contextHint.Reads` (explicit per-key reads) + (post-Story 4.4) `ScanPrefixes` from Core KV into the `HydratedState` map. Soft cap: >1000 keys per prefix returns `HydrationError("scan-too-large")`. **NOTE**: `ScanPrefixes` is on the deprecation list — Story 4.6 replaces it with narrow `LinkScans`. |
| 5 | **Execute** | `Executor.Execute` — compiles and runs the DDL's `.script` aspect in the Starlark sandbox via `StarlarkRunner.Run`. Produces `ScriptResult{Mutations, Events, ResponseDetail}`. Timeout: 250ms wall budget + 1,000,000 step limit. |
| 6 | **Validate** | `Validator.Validate` — checks `permittedCommands` (operation type must be in the DDL's list), `sensitiveAspectScope` (script may not create underscore-prefixed aspects except system-reserved ones), and key-pattern checks from Story 1.7 + 1.9. `DDLViolation` → term, reply with `DDLViolation` code. |
| 7 | **Materialize events** | Assigns per-event NanoIDs to events in `ScriptResult.Events` before the commit. NanoIDs are generated via `substrate.NewNanoID()` — entropy is from `crypto/rand`, not PCG (the script's `nanoid` global uses a PCG seeded from the requestId for deterministic per-script behavior; step 7 uses real entropy). |
| 8 | **Commit** | `Committer.Commit` — calls `substrate.AtomicBatch` on the `core-kv` bucket. Batch includes all mutation ops + the tracker `vtx.op.<requestId>` as a create-only entry. Revision conditions on update ops; any condition failure → `ErrAtomicBatchRejected`. If the tracker was the conflicting key (concurrent re-delivery), short-circuit as duplicate. If a business mutation conflicted → `RevisionConflict` reply, term. On transient failure: nak. |
| 9 | **Publish** | `EventPublisher.Publish` — calls `substrate.PublishBatch` targeting `events.<class>` subjects on the `core-events` stream. All-or-nothing; if publish fails after the commit, nak so JetStream re-delivers — step 2 dedup short-circuits the mutation, and step 9 runs again. Reply to the caller is deferred until step 9 succeeds (Contract #2 §2.4 anchors durability at step 8, but observability of the full path requires step 9). |
| 10 | **Ack** | `Acker.Ack` — JetStream ack the original message. The explicit Acker boundary (introduced in Story 1.8) ensures the reply is already sent before the ack fires. Ack failure is non-fatal from the caller's perspective (commit + reply already durable); the message will be re-delivered and step-2 dedup short-circuits. |

---

## Starlark sandbox

The script in the DDL's `.script` aspect is compiled and executed in a
restricted Starlark environment for each operation. The sandbox is verified
by Gate 2 (`nfr_r1_test.go`).

### Globals injected

| Name | Type | Description |
|------|------|-------------|
| `state` | `stateMapValue` (dict-like) | Hydrated Core KV map; keys are KV key strings, values are `{key, class, isDeleted, data, [vertexKey, localName]}` structs. Supports `state[key]`, `key in state`, `for k in state`, and `state.keys_with_prefix(prefix)` (Story 4.4). |
| `op` | struct | Envelope view: `requestId`, `lane`, `operationType`, `actor`, `submittedAt`, `payload` (parsed dict). |
| `ddl` | dict | Resolved DDL map: `{canonicalName, permittedCommands}` per DDL entry. |
| `nanoid` | module | `nanoid.new()` — PCG-seeded deterministic NanoID generator (seed derived from `requestId` for reproducibility in tests). |
| `crypto` | module | `crypto.sha256(s) -> hex string`, `crypto.sha256NanoID(s) -> NanoID`, `crypto.constant_time_equal(a, b) -> bool`. Side-effect-free; used by `ClaimIdentity` for claim-key validation (Story 4.2/4.3). |
| `strings` | module | `strings.levenshtein(a, b) -> int`, `strings.levenshtein_ratio(a, b) -> float`. Pure string-math builtins (Story 4.4). **Deprecation**: these move to the Refractor's cypher executor as UDFs in Story 4.6. |

### Forbidden

- `load(...)` — `Thread.Load` is nil; compile-time rejection.
- `os`, `time`, `http`, and any other undeclared global — caught as `SandboxViolation` (compile-time resolve error from `starlark.SourceProgram`).
- Direct NATS access — Starlark cannot call NATS; all side effects are through the `mutations` + `events` + `response` return dict (Contract #3 §3.7).

### Return shape

```
{
  "mutations": [{op: "create"|"update"|"tombstone", key: "...", document: {...}}],
  "events":    [{class: "...", data: {...}}],
  "response":  {...}   # optional; becomes OperationReply.Detail
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

## Capability change operations (FR53 — Story 5.3)

### `.compensation` aspect as the FR53 contract surface

Every capability-change DDL meta-vertex carries a sixth self-description
aspect named `.compensation` (stored at `<metaKey>.compensation` in Core KV).
This aspect encodes the compensating (inverse) operation as a template
reference so that an operator or AI agent can construct a rollback without any
new Processor reply fields.

**The Processor commit response carries NO new field for compensation.** An
earlier design proposed a `compensatingOperation` field in `OperationReply`
(see `internal/processor/envelope.go`). This was rejected by the PO + architect
because:
1. It embeds routing logic inside the Processor response — coupling the write
   path to compensation semantics it should not own.
2. It implies the Processor knows the "inverse" of every operation, violating
   the single-responsibility principle of the commit path.
3. It requires new `OperationReply` fields, directly contradicting Guardrail 1
   (no new envelope fields).

The replacement design (Option A — canonical): the compensation contract lives
in the DDL meta-vertex as a sixth self-description aspect. The compensating
operation is constructed **client-side** by reading this aspect via
`aiagent.Traverser.ReadCompensation`, then substituting field references from
the original commit response. No new Processor code required.

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
      "metaKey": "{{detail.metaKey}}"
    },
    "revisionTemplate": {
      "metaKey": "{{revisions[detail.metaKey]}}"
    }
  }
}
```

Template variable substitution is **client-side only** (Guardrail 2 — no new
Processor read surface):
- `{{detail.metaKey}}` → value of `OperationReply.Detail["metaKey"]` (the
  created meta-vertex key, already returned by the MetaRoot DDL's `response`
  field).
- `{{revisions[detail.metaKey]}}` → value of
  `OperationReply.Revisions[<metaKey>]` (the per-key NATS revision from the
  atomic batch commit).

#### Phase 1 operation pairing table

| Forward operation | Compensating operation | Notes |
|---|---|---|
| `CreateMetaVertex` | `TombstoneMetaVertex` | Tombstones the newly-created meta-vertex. `expectedRevision` from commit response prevents racing compensating ops. |
| `UpdateMetaVertex` | `UpdateMetaVertex` | Restores the prior values of exactly the fields the forward op changed (see [`UpdateMetaVertex` field set](#updatemetavertex-field-set)). The `.compensation` aspect stores those prior values concretely (read from hydrated state at script execution time). |
| `TombstoneMetaVertex` | none (Phase 1 irreversible) | The tombstone cascades to the root **and every aspect** (`.compensation` included), so no live aspect survives the delete. There is no machine-readable compensation; re-creating the meta-vertex is the operator's responsibility (a fresh `CreateMetaVertex` with the prior payload mints a new NanoID). |
| rbac-domain `CreateRole` | rbac-domain `TombstoneRole` | Phase 2 scope — rbac-domain DDL carries its own `.compensation` aspect. |
| rbac-domain `AssignRole` / `GrantPermission` | rbac-domain `RevokeRole` / `RevokePermission` | Phase 2 scope. |

#### Client-side revert flow

Given a forward `CreateMetaVertex` op that committed successfully:

1. Operator (or AI agent) has: `metaKey` (from `Detail["metaKey"]`) and
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
  and `BatchOp.Revision = *m.ExpectedRevision` (see `internal/processor/step8_commit.go`
  lines 131–140). This gives atomic, substrate-level revision enforcement.
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

**`expectedRevision` (OCC) — single-aspect assertion.** An update may now touch
any subset of aspects, and each aspect has its own independent NATS revision
sequence. `expectedRevision` is therefore applied to the `make_update` of the
**first present field** in the canonical order `description, script,
permittedCommands, inputSchema, outputSchema, fieldDescription, examples,
spec` — never to `.compensation` (independent sequence; would cause spurious
conflicts). Multi-aspect atomic OCC across several changed aspects is a known
**Phase-2 limitation**.

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

## Package install / uninstall (Story 1.5.5 — M5/B2, F-001)

Capability-package install **and** uninstall route through the Processor as two
primordial kernel operations — `InstallPackage` / `UninstallPackage` — instead of
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
provenance authored by the install actor (an improvement over the old
bootstrap-identity substrate-direct stamp).

**Install guardrails** (`InstallPackage` is privileged — it must not be an
arbitrary-write backdoor):

- key-shape — every key matches an allowed Contract #1 pattern (`vtx.<type>.<id>`
  `[.aspect]`, `lnk.<…>`); anything else is rejected;
- protected-key — a key whose hydrated root carries `data.protected == true` is
  rejected (installs may not overwrite kernel entities);
- system-aspect — no aspect `localName` may start with `_` (mirrors the step-6
  `sensitiveAspectScope` convention);
- create-only — every install mutation op must be `create`.

**M5/B2 cache coherence (no restart).** All mutations land in ONE step-8 atomic
batch. The existing step-8 `vtx.meta.*` invalidation fires in-commit for the DDL
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

## Kernel protection (§3.4 — 1.5.2 residual)

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
| `PublicationError` | Step 9 | Nak; JetStream re-delivers; step-2 dedup short-circuits mutation; step 9 re-runs |
| `MalformedEnvelope` | Step 1 | Reply with `EnvelopeMalformed` code (if reply inbox present); term |

---

## Auth modes

| Mode | Behavior |
|------|----------|
| `AuthModeCapability` (default) | Real `CapabilityAuthorizer`; reads Capability KV; checks lane + permission (+ `ephemeralGrants` expiry). No projection-freshness gate (Story 1.5.4). |
| `AuthModeStub` | `StubAuthorizer`; always allows; emits `WARN` log + Health KV alert every 1000 calls. Test/dev only. |

The auth mode defaults to `AuthModeCapability` as of Story 3.3. `LATTICE_AUTH_MODE=stub` opts back in to the stub; production deployments that enable stub receive visible degradation signals in Health KV dashboards.

---

## Principles (binding)

- **Sole authorized write surface** (NFR-S2): every Core KV mutation passes through all 10 steps. Gate 2 (`nfr_r1_test.go`) verifies no bypass path exists.
- **No bypass**: even for capability management operations (Stories 5.x), mutations enter through the operation write path, not via direct KV writes.
- **Idempotent under retry**: the step-8 tracker provides dedup; re-delivered operations that already committed short-circuit at step 2 and receive a `duplicate` reply.
- **ContextHint is surgical**: `contextHint.Reads` specifies per-key pre-loads. `ScanPrefixes` is being walked back (deprecation target: Story 4.6 replaces with narrow `LinkScans`).
- **ResponseDetail is commit-trace, not business-data**: `OperationReply.Detail` carries only the script's `response` dict, which is intended for audit payloads (mutation count, revision map, traceId). It is NOT a query channel. Pre-correction Epic 4 usage as a business-data channel is being corrected in Story 4.6.
- **Starlark cannot touch NATS**: all side effects are declared via the mutations + events return shape (Contract #3 §3.7).

---

## What's deferred

- **Read-side capability authorization** (Phase 2): Refractor lenses produce Capability KV; Processor's step 3 checks it. Direct read-side authz (e.g., for CLI queries) is Phase 2.
- **Multi-cell routing** (Phase 3): the current pipeline is single-cell; operation routing across cells is Phase 3.
- **Real NATS auth** (Phase 2): the current connection uses no NATS account-level auth. Story 4.5 introduced a temporary stub-mode carry for `MergeIdentity` lane; full NATS auth is Phase 2.
- **`ScanPrefixes` removal** (Story 4.6): the current `ContextHint.ScanPrefixes` field is deprecated; Story 4.6 evicts it and introduces narrow `LinkScans` for `MergeIdentity` inbound-link enumeration.
- **`strings.levenshtein` in Starlark** (Story 4.6 eviction): the builtins in the Starlark `strings` module move to the Refractor's cypher executor as UDFs; they will be removed from the Starlark sandbox.
