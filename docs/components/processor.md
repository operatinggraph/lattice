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
| **Capability KV** (Contract #6) | Capability KV bucket | Read at step 3 by `CapabilityAuthorizer`; key pattern `cap.identity.<actorId>` (Phase 1 shape); auth freshness checked against 5×NFR-P3 ceiling |
| **Adjacency KV** | `refractor-adjacency` | Not yet read by Processor; targeted for Story 4.6 `MergeIdentity` inbound-link enumeration (Phase 2 candidate) |

---

## Out-contracts (what it produces)

| Artifact | Destination | Notes |
|----------|-------------|-------|
| **Core KV mutations** (Contract #1 + #3) | Core KV bucket (`core-kv`) | Written as an atomic batch via `substrate.AtomicBatch`; each mutation is a `create`, `update`, or `tombstone` operation |
| **Events** (Contract #3 EventList) | JetStream `events.<class>` subjects on `core-events` stream | Published as an unconditional `substrate.PublishBatch` at step 9 after the commit is durable |
| **Idempotency tracker entries** (Contract #4) | Core KV at `vtx.op.<requestId>` | Written as part of the step-8 atomic batch; 24h TTL; provides step-2 dedup on re-delivery |
| **Operation replies** (Contract #2 §2.4) | Per-op reply-to inbox | `accepted` (post-step-8), `duplicate` (step-2 short-circuit), or `rejected` (any termination branch) |
| **Health KV signals** (Contract #5) | Health KV `health.processor.<instance>.*` | Heartbeat every 10s; per-op metrics (OpsConsumed / OpsCommitted / OpsDuplicates / OpsRejected / OpsMalformed); step-3 latency; capability staleness; auth trace records (Story 3.5); claim-attempt outcomes (Story 4.3); alerts under `health.alerts.security.*` |
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
| 3 | **Auth** | `Authorizer.Authorize` — in production, `CapabilityAuthorizer` reads `cap.identity.<actorId>` from Capability KV, checks lane authorization + permission match + freshness ceiling. Denied → term with reason, reply with structured denial (FR22 when `DenialBuilder` is wired). Auth trace emitted fire-and-forget via `AuthTraceEmitter` (FR23) for both allowed and denied decisions when configured. |
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

The table below lists the forward/compensating operation pairs for capability
management. This represents the post-Story-4.7 shape. Pre-4.7 (current Phase
1) state has more granular DDL operations (`CreateRole`, `CreatePermission`,
etc.) which collapse into `CreateMetaVertex` after Story 4.7's DDL
consolidation.

| Category | Forward | Compensating |
|----------|---------|-------------|
| Meta-vertex management | `CreateMetaVertex` | `TombstoneMetaVertex` |
| Meta-vertex update | `UpdateMetaVertex` | `UpdateMetaVertex` (with prior payload) |
| Role-permission grant | `GrantPermission` | `RevokePermission` |
| Identity-role assignment | `AssignRole` | `RevokeRole` |

---

## Failure modes

| Failure | Where | Resolution |
|---------|-------|------------|
| `ConflictError` | Step 8 revision-condition fail | Bubble `RevisionConflict` reply to caller; term |
| `DDLViolation` | Step 6 | Reply with `DDLViolation` code; term (no retry) |
| `SandboxViolation` / `ScriptError` | Step 5 | Reply with `ScriptFailed` code; term |
| `ScriptTimeout` | Step 5 | Reply with `ScriptFailed` code; term |
| `HydrationError` | Step 4 | Reply with `HydrationFailed` code; term |
| `AuthDenied` | Step 3 | Reply with `AuthDenied` / `LaneUnauthorized` / `AuthFreshnessExceeded` code; term; ack (no retry — this is a final decision) |
| `AuthInfrastructureFailure` | Step 3 | `InternalError` reply; nak (retryable) |
| `PublicationError` | Step 9 | Nak; JetStream re-delivers; step-2 dedup short-circuits mutation; step 9 re-runs |
| `MalformedEnvelope` | Step 1 | Reply with `EnvelopeMalformed` code (if reply inbox present); term |

---

## Auth modes

| Mode | Behavior |
|------|----------|
| `AuthModeCapability` (default) | Real `CapabilityAuthorizer`; reads Capability KV; checks lane + permission + freshness |
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
