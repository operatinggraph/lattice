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
| **Capability KV** (Contract #6) | Capability KV bucket | Read at step 3 by `CapabilityAuthorizer`, at keys chosen by **actor class** (Contract #6 §6.1, `internal/capabilitykv.ClassAwarePlatformKey`): a kernel-seeded system actor reads the UNION of its `cap.<type>.<id>` anchor and `cap.roles.<type>.<id>`; **every other actor reads `cap.roles.<type>.<id>` alone**. The system-actor set is graph-derived (`bootstrap.SystemActorKeys` — identities holding the primordial `operator` role through a live `holdsRole` link), so class is a live property of the graph, not of the key string. With rbac-domain absent the platform entry falls back to `cap.<type>.<id>` for every actor. A missing entry denies (`NoCapabilityEntry`, fail-safe). There is **no per-operation projection-freshness gate** — `projectedAt` is deterministic provenance, not a TTL; the bounded staleness window is an accepted risk backstopped operationally (see Refractor Capability-Lens health) and, in future, by Gateway token revocation. |

**Lane authorization (Contract #2 §2.3).** Step 3 also enforces per-lane submission rights: the
declared `env.Lane` must be in the actor's granted lanes (`doc.lanes` on the platform path; the scoped
service/task paths grant the `default` lane only) — a mismatch is rejected `LaneUnauthorized` (§2.6)
before the operationType matcher, with **no extra KV read** (the lane authority is the doc the platform
path already fetched, or an implicit `default` for service/task). An empty granted set denies every
lane (fail-closed). The protected kernel actors hold all four lanes; ordinary actors hold `default`
only. See `step3_auth_capability.go`.

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
supervised pump Loom/Weaver/Refractor use. One durable **per lane**
(`processor-{default,urgent,system,meta}`, each filtered to its `ops.<lane>`
subject; built by `processor.LaneSpecs`) delivers each operation to
`CommitPath.dispatch`, which runs the steps below, publishes any client reply, and
returns an ack `Decision` the supervisor applies — the supervisor owns
disposition, the commit path owns the reply. Each message exits with one of five
outcomes: `accepted`, `duplicate`, `rejected`, `malformed`, or `retryable`
(`NakWithDelay` — redelivered on a bounded backoff floor, never a hot-loop). The
in-process test harness drives the same `dispatch` through `HandleMessage`,
applying the returned `Decision` to the JetStream message itself (`Ack` via the
explicit step-9 Acker boundary).

Lanes drain on independent pumps, so an `urgent` op never queues behind a
`default` backlog (priority isolation), and each lane's durable carries its own
backlog so Contract #5 §5.4 `lane_lag.{default,urgent,system,meta}` is per-lane
real (`lane_lag_total` is their sum). The `meta` lane — DDL mutations — is pinned
to a single pump **and** `MaxAckPending=1` (Contract #2 §2.3), so a meta-vertex
commit and its synchronous DDL-cache invalidation never race a second concurrent
DDL mutation; non-`meta` lanes run concurrently with `meta` and stay safe via the
RWMutex-guarded value-copy DDL-cache snapshot + step-8 OCC. On startup the
Processor retires the legacy single `processor-main` durable
(`substrate.DeleteStreamConsumer`, idempotent); its un-acked messages redeliver to
the per-lane durables, idempotent via the step-2 dedup tracker.

Each lane runs **N concurrent pump goroutines** (`substrate.ConsumerSpec.Workers`),
all binding the lane's single durable — JetStream load-balances the pull consumer
across them, delivering each message to exactly one worker. The counts come from
`processor.LaneConsumers(os.Getenv)`: the `LATTICE_PROCESSOR_LANES_<LANE>_CONSUMERS`
override (`LANE` = `DEFAULT|URGENT|SYSTEM|META`) over the defaults **`default=2`,
`urgent=4`, `system=2`, `meta=1`** (a malformed or sub-1 value keeps the lane
default). `meta` is **fail-closed clamped to one worker** regardless of any
override — its serialization (single pump + `MaxAckPending=1`) is never widened.
Within a non-`meta` lane two operations can therefore commit **concurrently**;
causal dependencies stay correct through the step-8 OCC (`expectedRevision` per
mutation) and the operation's `reads`, **not** through lane FIFO ordering (which
the lanes never guaranteed).

| Step | Name | What happens |
|------|------|-------------|
| 1 | **Consume** | `parseEnvelopeFromBody` — deserializes `OperationEnvelope` from the delivered message body; validates `requestId` (must be a valid 20-char NanoID), `lane` (must be a recognized enum value), `operationType`, `actor`, `submittedAt`, and `payload`. Malformed → term with reason; if a reply inbox is present, reply with `EnvelopeMalformed` code. |
| 2 | **Dedup** | `CheckDedup` — reads the tracker key `vtx.op.<requestId>` from Core KV. If already present, emit `DuplicateDetected` log + health marker, reply with `duplicate`, return `Ack`. On KV error, return `NakWithDelay` (redeliver on the backoff floor). |
| 3 | **Auth** | `Authorizer.Authorize` — in production, `CapabilityAuthorizer` reads the actor's Capability-KV projection through the class-aware routing above (system actor → `cap.<type>.<id>` ∪ `cap.roles.<type>.<id>`; every other actor → `cap.roles.<type>.<id>` alone), then checks lane authorization + permission match. A missing entry denies (`NoCapabilityEntry`). There is no projection-freshness gate: a stale-but-permission-matching projection is allowed; `projectedAt` is recorded as provenance in the auth trace, not compared against a ceiling. `ephemeralGrants[].expiresAt` (a real grant TTL) is still enforced. Denied → term with reason, reply with structured denial (FR22 when `DenialBuilder` is wired). Auth trace emitted fire-and-forget via `AuthTraceEmitter` (FR23) for both allowed and denied decisions when configured. |
| 4 | **Hydrate** | `Hydrator.Hydrate` — runs the DDL's optional top-level `derive_reads(op)` FIRST when the script declares one (Contract #2 §2.5 class **(g)**, `derive_reads.go`): a key that is a deterministic function of the payload under the *package's* own semantics (a normalized-contact index hash) is one the submitter cannot express, so the owning script declares it and the Processor merges it into the declared set before the first Core KV GET. It runs in the same sandbox against the same compiled program as step 5, with `kv` and `nanoid` bound as fail-closed **stubs** — a derivation that reads state is a read and must be declared as one, and `nanoid`'s PCG is requestId-seeded so a call here would collide with step 5's first id. Derived keys are validated against the Contract #1 grammar, merged **weakest-wins** against the envelope's own declaration, re-checked against `egressReads`, and counted toward the declared-read ceiling (a breach is a step-4 runtime fault, not `EnvelopeMalformed` — the keys are not envelope-supplied). A DDL declaring no `derive_reads` pays nothing. Then loads `contextHint.Reads` (explicit per-key reads, fail-closed: a missing key is recorded *required-absent* and faults `HydrationMiss` at the first point the operation depends on it — a `kv.Read`, a `state` membership access, or a mutation naming it. Faulting during hydration instead would make step 4 an existence oracle: `contextHint` is client-supplied and step 3 authorizes without inspecting it) and `contextHint.OptionalReads` (absence-tolerant, Contract #2 §2.5 class (d): a missing key is recorded *known-absent*, so `kv.Read` serves `None` from the step-4 snapshot with no live GET — the declared read-before-create / dedup pattern) from Core KV into the `HydratedState` map. `contextHint.Enumerations` is shape-validated at step 1 but **never hydrated** — it is metadata for the Edge mirror-coverage gate + the read-posture lint; a `kv.Links` enumeration stays a bounded paged live read (§2.5.1). The graph topology an op needs is delivered as declared command parameters, not discovered by scanning: a Lens projects topology into its own bucket, the client reads the lens, and the resulting keys travel back in `ContextHint.Reads`. The script validates each declared key (envelope class, endpoint touch, not tombstoned) before acting on it. |
| 5 | **Execute** | `Executor.Execute` — compiles and runs the DDL's `.script` aspect in the Starlark sandbox via `StarlarkRunner.Run`. Produces `ScriptResult{Mutations, Events, ResponseDetail}`. Timeout: 250ms wall budget + 1,000,000 step limit. |
| 5.5 | **Key shapes + prior documents** | `validateMutationKeyShapes` then `Committer.ReadPrior` — a batch-wide pre-pass puts every mutation key through the Contract #1 grammar (and refuses a `class` that is present and not a JSON string) BEFORE the first read, because a malformed key handed to `KVGet` answers with an error a read stage must treat as retryable, which would turn a terminal `keyPattern` refusal into an unbounded redelivery loop. Then one bounded concurrent pass reads the stored document behind every `update`/`tombstone` MUTATION key — what step 6's stored-class gate reads, and what step 8's body preservation and revision fallback read — off the script's own live-read budget. The 3-segment protected ROOTS the step-8 guards consult are not in this pass: step 8 reads those itself, at commit time, so a root that turns protected between validation and the batch is still refused (a mutation key needs no such re-read, because the batch is conditioned on the revision read here and a concurrent write makes it conflict). Round trips are unchanged either way: each key is read once. A read fault here is retryable (`NakWithDelay`), never a refusal and never a permissive pass-through. |
| 6 | **Validate** | `Validator.Validate` — checks `permittedCommands`, `sensitiveAspectScope` (script may not create underscore-prefixed aspects except system-reserved ones), and key-pattern checks. `DDLViolation` → term, reply with `DDLViolation` code. **A mutation is governed by the DDL of every class it touches:** for an `update` or a `tombstone`, the class of the document STORED at the key (from step 5.5's read) — so a documentless tombstone is gated by the entity it removes, and a re-typing update by the entity it rewrites; for a `create` or an `update`, the class the document declares. An absent key resolves nothing and stays permissive. A stored body the gate cannot read — one that did not decode, or whose `class` is not a string — refuses an `update` with `DDLViolation{storedClass}` (a rewrite of content the gate cannot read is not governable) and admits a `tombstone`: the tombstone carries no document and writes no readable content forward, so nothing can be laundered through it, and an already-corrupt key must stay removable through the operation plane. A meta-rooted key resolves its stored class by the exact class lookup alone — the kernel types its own meta-vertices and seeds no `instanceOf` edge for them — so a package uninstall or meta-vertex tombstone cascade pays no chain reads. Platform-authored mutations appended AFTER step 6 are outside this gate by design and by necessity: the task auto-completion's task-root update (`autocomplete.go`) and step 6.5's `piiKey` create are Processor-authored documents of fixed shape, and gating them on the target class's `permittedCommands` would refuse every task-bound or first-sensitive-write business operation. The Contract #1 §1.5 `instanceOf` type-authority walk (`step6_resolve_ddl.go`, `maxInstanceOfHops` = 4, visited-set cycle guard) resolves each hop's target from the current atomic batch, the hydrated working set, or an on-demand read, ignoring tombstoned `instanceOf` links — except for a STORED class, whose authority is resolved against the committed graph alone, so the same batch cannot un-type the entity by tombstoning its own `instanceOf` link. The stored-class walk, and an update's declared-class walk, run off the script's live-read budget; a degraded (read-faulted) resolution is retryable, never the permissive default. |
| 6.5 | **Encrypt** | `step65_encrypt.go` — before the batch is built, aspects the DDL marks `sensitive` are encrypted through the Vault so they land as **ciphertext at rest** in Core KV (crypto-shred boundary). A no-op when the Vault is unconfigured or the operation writes no sensitive aspect. |
| 7 | **Materialize events** | Assigns per-event NanoIDs to events in `ScriptResult.Events` before the commit. NanoIDs are generated via `substrate.NewNanoID()` — entropy is from `crypto/rand`, not PCG (the script's `nanoid` global uses a PCG seeded from the requestId for deterministic per-script behavior; step 7 uses real entropy). **Enforces the event-domain model:** every event `class` must be `<domain>.<eventName>` (Contract #3 §3.4) — a dot-free class (no domain segment) is rejected; the Event document's `domain` field is set from the class's first segment. |
| 8 | **Commit** | `Committer.Commit` — takes step 5.5's prior-document map, tops it up with the protected roots its own guards read (fresh, at commit time) plus any `update`/`tombstone` key appended after validation (the task auto-completion's injected update), and calls `substrate.AtomicBatch` on the `core-kv` bucket. Batch includes all mutation ops + the tracker `vtx.op.<requestId>` as a create-only entry + the faithful EventList at `vtx.op.<id>.events`. Revision conditions on update ops; any condition failure → `ErrAtomicBatchRejected`. If the tracker was the conflicting key (concurrent re-delivery), short-circuit as duplicate. If a business mutation conflicted, the commit path re-hydrates and retries up to `defaultMaxCommitAttempts` (the §3.2 OCC bounded internal retry) before a terminal `RevisionConflict` reply. A task-bound op (carrying `authContext.task`) additionally folds §10.6 task auto-completion into the same batch (`commitWithTaskAutoComplete`). On transient failure: `NakWithDelay` (redeliver on the backoff floor). |
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
| `state` | `stateMapValue` (dict-like) | Hydrated Core KV map; keys are KV key strings, values are `{key, class, isDeleted, data, [vertexKey, localName]}` structs. Supports `state[key]`, `key in state`, and `for k in state`. Contains `reads` keys plus the *present* `optionalReads` keys; an optionalReads key that was absent at step 4 is not in `state` — the script reads it via `kv.Read`, which serves `None` from the known-absent snapshot. |
| `op` | struct | Envelope view: `requestId`, `lane`, `operationType`, `actor`, `submittedAt`, `payload` (parsed dict). |
| `ddl` | dict | Resolved DDL map: `{canonicalName, permittedCommands}` per DDL entry. |
| `nanoid` | module | `nanoid.new()` — PCG-seeded deterministic NanoID generator (seed derived from `requestId` for reproducibility in tests). |
| `crypto` | module | `crypto.sha256(s) -> hex string`, `crypto.sha256NanoID(s) -> NanoID`, `crypto.constant_time_equal(a, b) -> bool`. Side-effect-free; used by `ClaimIdentity` for claim-key validation. |
| `json` | module | Standard Starlark `json.decode(s)` / `json.encode(v)`. Pure (no I/O, deterministic); used where a script parses a JSON payload field into a structured dict (e.g. a Lens `.spec`). |
| `kv` | module | `kv.Read(key) -> doc-struct \| None` — Contract #2 §2.5 lazy on-demand Core KV read. The **one non-pure builtin**: serves a `contextHint`-prefetched key from the hydrated `state` cache (no round-trip) and otherwise does a single live key GET. Absent / hard-tombstoned → `None`; a logically-deleted vertex (`isDeleted=true`) → a present doc carrying the flag. Bounded by the wall budget. The opt-in read-before-create idempotency seam — **not** a scan or read-model hook (read models are lenses, P5). |

#### `kv.Read` semantics (§2.5)

- **Cache-first.** A key listed in `contextHint.reads` is pre-fetched at step 4 and served from `state` at the step-4 OCC snapshot — `kv.Read` cannot force a fresher re-read of an already-hydrated key (echoing the snapshot revision as `expectedRevision` is what keeps the commit's OCC check sound). A key declared in `contextHint.optionalReads` and found **absent** at step 4 is served as `None` from the same snapshot (the *known-absent* record — no live GET, and a `create` derived off that absence is retry-attributable: a lost `CreateOnly` race re-hydrates, sees the key present, and the script re-branches). A key *not* declared at all falls through to a single on-demand GET (incurs latency, §2.5 — class (b) debt when the key was knowable at submit time).
- **Absence is graceful.** Unlike a read of a `contextHint.reads` key recorded required-absent (a fatal `HydrationMiss`), `kv.Read` of an undeclared absent / hard-tombstoned key returns `None`, so a script can branch present-vs-absent — the read-before-create pattern a `createIfAbsent` mutation cannot express (events stay coherent with mutations because the script decides both in one branch). Declaring the key in `optionalReads` keeps this pattern **and** makes it declared/snapshotted/Edge-predictable — the §2.5 read-posture norm.
- **The op-meta descriptor's disposition is a floor the envelope cannot raise** (§2.5, `descriptor_floor.go`, applied at the head of step 4 before `derive_reads`). Where an operation's `Dispatch` descriptor declares a key under `optionalReads`, an envelope naming that same key under `reads` is **demoted** rather than honoured; an `egressReads` key is marked absence-tolerant in place instead of moved, because relocating it would swap a bridge-opened `$sensitiveRef` for plaintext. A template compiling to a key *shape* or to nothing at all floors nothing, and a `{payload.<field>}` template contributes no *exclusion* from the floor — an exclusion the submitter can address is a bypass, not a precedence rule.
- **For the NFR-S6 operations the declared set is CLOSED, not merely floored** (`refuseUndeclaredContextHint`, same file and same point in step 4). `ClaimIdentity` and `CompleteCredentialLink` may declare only the keys their own descriptor names — resolved to concrete keys; a shape admits nothing — and an envelope naming anything else, including **any** `egressReads` key (which no `OpDispatchSpec` can name — there is no such field) or **any** enumeration, is refused before hydration. An `OpDispatchSpec` *can* name an enumeration (`Dispatch.Enumerations`), so for these two operations the declaration is refused at package install as well: left to the floor alone it would install clean and then fault every submission terminally, collapsed to the generic reply below with the cause visible only in the log. The reason is the equalization, not the key: the rejection causes of these two operations are made to cost the same over the keys the DESCRIPTOR names, and a key the submitter adds is work nothing equalized — its cost turns on whether it exists, whether it is sensitive and whether it is tombstoned. Refusing every `egressReads` key also keeps `decryptSensitiveDoc`'s tombstone-dependent egress refusal unreachable for them. An operation with no descriptor admits nothing and refuses every declared key — a visible over-deny. Derived (class-(g)) keys are the DDL's own and are outside the rule. The refusal is a `HydrationError` carrying **no key**: the refused key is the submitter's own probe, so it goes to the Processor log alone, and the reply collapses to the generic `ClaimKeyInvalid` like every other rejection of these operations.
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

## NFR-S6 anti-enumeration: the wire shape AND the cost

`ClaimIdentity` and `CompleteCredentialLink` answer a caller who holds no valid
secret. Both take a target key straight from the payload, both are granted to
every consumer, and neither is rate-limited — so *anything* that varies with the
target's state is an identity-existence oracle. Contract #9 states the
requirement without qualification: **"All failure modes collapse to the generic
`ClaimKeyInvalid` reply code (NFR-S6 anti-enumeration); specific outcomes surface
only via Health KV."** That is two obligations, not one.

The operation set is declared in `internal/processor/nfr_s6_wire_shape.go`
(`nfrS6Operations`) and is keyed on **operationType**, never on the error code a
particular failure happened to produce.

### 1. One wire shape

Every rejection of these operations answers `ClaimKeyInvalid`, nil details, and
one fixed message — including failures the script never reached, such as a step-4
hydrate or decrypt fault on the operation's own declared keys. The step name and
the underlying error text are withheld too: the step alone separates a fault from
a refusal, and a hydrate fault's text quotes the very key the caller was probing.
The real code, message and details go to the Processor log; the specific outcome
goes to `health.processor.<instance>.claim-attempts.<outcome>`, with
`internal-fault` covering the faults the script never reached.

### 2. One cost per rejection

Identical replies are still separable if they arrive at different times. Measured
at n=3000/cause, the three `ClaimIdentity` rejection causes differ by 0.3–0.7 ms
of mean service time, a bias that is monotone, invariant to load, and worth
roughly 12 requests to extract. The difference is removed at the two places it is
made, so there is nothing left to hide.

**The package script fails once, at the bottom.** `packages/identity-domain`'s
`ClaimIdentity` and `CompleteCredentialLink` branches evaluate every guard
whatever the ones before it decided, accumulate the first failing condition's
outcome word (`first_outcome`), and raise it in a single terminal `fail`. Every
cause therefore executes the same instructions, including the `crypto.sha256` and
the `crypto.constant_time_equal` — against fixed-length stand-ins when the real
operands are absent, since `crypto.constant_time_equal` returns on a length
mismatch before comparing. `TestClaimScript_GuardsFailOnceRatherThanCascading`
pins the single-exit shape; an added early return reds it.

**The engine's tombstoned sensitive read costs what a live one costs.**
`decryptSensitiveDoc`'s `IsDeleted` non-egress arm performs the live path's whole
decrypt sequence — `ciphertextFromData`, `vault.KeyHolder`, `readPiiKeyEnvelope`
(the round trip that dominates), `Decrypt`, the unmarshal — and discards the
plaintext **and every error along the way**, then delivers the same scrubbed
tombstone it always delivers. Without that, an already-claimed identity's
tombstoned `.claimKey` costs ~0.36 ms less than an unclaimed one's live aspect,
which is the enumeration signal exactly. Swallowing the errors is what keeps a
shredded key envelope from turning a tombstoned read into an `InternalError` whose
reachability depends on the target's state.

**What is not equalized, by decision.** An absent target returns fewer messages
from the batched `KVGetMulti` than a present one — four fewer messages, parses and
decrypt entries — because a `multi_last` response carries one message per *matched*
subject. Equalizing it would need the engine to fabricate a synthetic snapshot per
operation, which is more per-operation coupling than the mechanism it would
protect. So the surviving question is **"does this key exist?"**, never "is it
claimed?", and the price is ~0.63 ms for `ClaimIdentity` — a present target pays the
envelope round trip whichever arm it takes, an absent one pays nothing — and ~1.0 ms
for `CompleteCredentialLink`, whose descriptor names a second sensitive aspect. The
*maximum* pairwise gap is what it always was; equalizing claimed-vs-wrong-key raises
the *surviving* one, because it closes that pair by adding the round trip to the
cheaper arm rather than removing it from the dearer.

Against a ~17 ms loaded p99 that is still a *statistical* channel needing many
samples, and it answers a **confirmation** oracle rather than an enumeration one:
the caller must already hold a `vtx.identity.<NanoID>`, and that keyspace is ~2¹¹⁷.
The deterministic single-request oracle is the wire code, and §1 closes it.

Two further per-rejection terms, named because a residue table that claims to be
exhaustive and is not is worse than no table:

- **The Health-KV claim-attempts write is inside the measured interval.**
  `handleStubFailure` records the outcome (`RecordClaimAttempt`, `health_alerts.go`)
  *before* `replyRejection` publishes, and that is a blocking `KVGet` plus a blocking
  `KVPutWithTTL` on a key whose last segment is the outcome word. Every
  script-decided cause pays the identical two round trips, so it adds variance and
  no per-cause offset — variance costs an attacker samples rather than saving them —
  which is why it stays where it is. The post-commit rejection classes (step 6 / 6.5 / 8)
  skip it and answer sooner, but each sits past the secret comparison.
- **A tombstoned sensitive read now costs one `readPiiKeyEnvelope` round trip
  platform-wide**, not only on these two operations — that is what makes the arms
  equal, and it is the ratified trade (design §4.2). It carries no metric and no log,
  and needs none: each of the discard's early returns mirrors the live path's own exit
  at the same step, so the two arms stop together or not at all.

See `_bmad-output/implementation-artifacts/nfr-s6-release-quantum-payload-design.md`
§6.3 and §8 for the trade and the threat model it is priced against.

### 3. One declared-read set

§2's equality is a property of a **fixed** key set and only of a fixed one, and
`contextHint` is the submitter's own lever on which keys those are:
`opwire.MaxDeclaredReads` permits 1000 declared keys, the Gateway copies
`contextHint` into the envelope verbatim, step 3 never inspects it, and every
declared key resolves inside step-4 hydration. A key a submitter adds is work
nothing has equalized — its cost turns on whether it exists, whether it is
sensitive and whether it is tombstoned, which are the three facts §2 takes out of
the descriptor-named set. So for these two operations the declared set is
**closed**: the envelope may name only what the operation's op-meta descriptor
names, and anything else is refused at the head of step 4
(`refuseUndeclaredContextHint`, see the `kv.Read` semantics above for the rule and
its edges).

Closing it is also what keeps the one remaining state-dependent arm out of reach.
`decryptSensitiveDoc` refuses a tombstoned sensitive aspect outright under the
**egress** disposition — a capability over a dead aspect must not leave the
Processor — while serving a live one; refusing every declared `egressReads` key on
these two operations is what makes that arm unreachable for them.

Both descriptors already name the entire legitimate set: the six dispatch sites
across the two operations — `cmd/facet/claim.go`, `cmd/lattice/identity`,
`scripts/verify-claim-ceremony.go`, `scripts/verify-erasure-ceremony.go`,
`cmd/facet/credentials.go` and `cmd/loftspace-app/credentials_link.go` — build
their hint from `internal/identityceremony`, whose builders emit exactly the KEYS
those templates compile to (the templates themselves live in
`packages/identity-domain/opmetas.go`).

The refusal is an ordinary step-4 `HydrationError`, so it inherits §1's posture —
the generic `ClaimKeyInvalid` with nil details — and it carries no key, because
the key it would carry is the probe.

### Operator signals

The plane has no runtime counter of its own, and does not need one: the question
that was watched at runtime — whether a rejection's work outran a masking window — is
a question only a window poses. What replaces it is gated where each half lives. The
script half is structural and CI-gated. The engine half is conditional on the discard
completing, and completes exactly when the live path would have (§2) — so a stack can
still lose the property one way the old mechanism could not: **the script half rides in
a versioned package artifact.** A stack running an `identity-domain` older than 0.20.9
has the guard cascade back, and nothing at runtime says so; the installed package
version is the thing to read when this plane is in question.

| Signal | What it tells you |
|---|---|
| `health.processor.<instance>.claim-attempts.<outcome>` (`health_alerts.go`) | The real cause behind each collapsed reply — `invalid-key`, `no-target`, `wrong-state`, `flagged`, `merged`, `credential-not-provisioned`, `credential-already-bound`, `erased`, `internal-fault`, `platform-refused`, `success`. A climbing `invalid-key` against a flat `success` is the brute-force signature; a climbing `internal-fault` is a fault the script never reached and is the one to page on; a climbing `platform-refused` is the platform rejecting before or after the script adjudicated (DDL violation, protected key, package scope, oversized batch, exhausted revision conflict) — read the `NFR-S6 rejection collapsed` WARN beside it for which. The counter spans the whole equalized set (`ClaimIdentity` and `CompleteCredentialLink`), so `success` is a real denominator for both; `flagged` remains `ClaimIdentity`-only, since only its script mints that word. |
| The `NFR-S6 rejection collapsed to the generic reply shape` WARN (`commit_path.go`) | The only per-rejection record of the actual code, message and details, since the caller is told none of them. |
| The `contextHint declares a read this operation's descriptor does not name` WARN (`descriptor_floor.go`) | A submitter reaching past the closed declared-read set. `admitted=0` beside it means the descriptor itself failed to resolve — an over-deny, not a probe. |
| `TestClaimScript_GuardsFailOnceRatherThanCascading` (`packages/identity-domain`) | CI, not runtime: the single-exit shape of both branches. An added early return reds it. |
| `TestDecryptSensitiveDoc_DeletedNonEgress_StillDecryptsForTimingEqualization` (`internal/processor`) | CI: the tombstoned sensitive arm still performs the live path's decrypt. Removing the discard reds it on the Decrypt count. |

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
`expectedRevision` for OCC, and the client supplies it: before submitting it
`KVGet`s each declared key and conditions that key's tombstone on the observed
revision, so a concurrent write to a declared key fails the whole atomic batch
loudly (`ErrUninstallConflict`) — the package stays fully installed, never a
partial state — see the
[package-install contract](../contracts/08-package-install.md) per-key-OCC note.

## Kernel protection (Contract #8 §8.4)

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
| Any rejection of an NFR-S6 operation | Steps 4–8 | Reply collapses to `ClaimKeyInvalid` with nil details; real cause to the log + Health KV (see *NFR-S6 anti-enumeration* above) |
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

---

## Review keeps catching (dossier)

Same contract as every dossier: fire briefs copy the applicable entries into part 5
(`agents/fire-brief-template.md`); the item-close review appends new ones (`agents/steward/SKILL.md` §4);
**capped at 12 one-liners**; an entry retires when a lint/test gate mechanizes it.

- **A mechanism whose margin the SUBMITTER prices is not a margin** — the retired entry above closed the
  *disposition* a client may declare; nothing closes the *volume*. `opwire.MaxDeclaredReads` is 1000, the
  Gateway copies `contextHint` verbatim (`gateway.go:823-830`) and step 3 never inspects it, so every declared
  read resolves inside step-4 hydration — i.e. inside whatever window a timing defence draws around it.
  The first ClaimIdentity reply floor was sized against a measured loaded p99 and was defeatable in ONE request
  by padding reads until the work outran it and the already-late branch published raw service time. Minted:
  claim-rejection timing oracle (`624d445`), cold review. **Followed to its end** (NFR-S6 quantum deletion):
  quantizing to a lattice removed the escape branch but not the boundary, and closing the declared set bounded
  one axis while `payload` bytes stayed submitter-sized and deep-decoded three times inside the same window.
  The answer to "who controls the work inside it" stayed *the submitter* on every axis but the one just closed,
  so the durable fix was to delete the window and equalize the causes where the difference is made — the script
  fails once at the bottom, the tombstoned sensitive read pays the live read's decrypt. Check, sharpened: for
  any defence expressed as a duration — floor, budget, deadline, timeout — name who controls the work inside
  it, per AXIS and not once. If a submitter controls **any** axis, a constant cannot hold it, and the question
  to ask before reaching for a bigger constant is whether the difference can be removed where it is made
  instead. The closed declared-read set survives the deletion, but on a different footing: it is what keeps the
  equalized set fixed, not what bounds a window.
- **An admission rule keyed on "this execution surfaced X" must exclude what the caller already NAMED** —
  `kv.Links`'s subject filter pins the hub to one end of every link it returns (`out` filters on the hub as
  source, `in` as target), so recording both endpoints made "a walk surfaced this vertex" true of the hub for
  free. The read-drift guard admits any read whose vertex root a walk surfaced, so every operation carrying
  the standard `holdsRole` confinement walk got a free pass on every later read of the actor — `.piiKey`
  included — and the one regression the guard exists to catch was the one it could not see. Minted:
  read-drift ratchet, cold review; the walked-set was 1,331 of 1,619 walks wide. Check: for any
  "discovered by" / "reached via" / "already covered" set, name in one sentence what the CALLER supplied to
  build it, and prove the set excludes that — with a test that reads the named thing itself and expects the
  control to fire. A set derived from an input the subject chose is not evidence about the subject.
- **A gate's negative test must first prove its positive vector reaches the gate** — two taxonomy gates
  shipped with tests that passed by planting keys the Processor itself refuses upstream (vacuous pass).
  Minted: dynamic-type-taxonomy items 1 + 5; **seen a third time at B1** in a new shape — five packages'
  negatives were refused by an arm other than the one under test, and the *accepted* side of a widened class
  set had no vector at all. Check: standing checklist #3 (revert-the-fix discipline). **Mechanize on the next
  sighting in a DIFFERENT item** (all three so far are this one): for each `fail(` reachable from a `require_*`
  helper, require the package's tests to assert that failure's own error prefix AND one accepted submit
  through the same helper. **Fourth sighting, and the first in a DIFFERENT item** (derived-reads plane tail,
  `44d42a7`), in three places at once and in a Go shape the mechanization above does not reach: a
  `failingMapping` method that could be reverted with no test failing, a lint self-test case that pinned
  neither word boundary it named, and an exported-branch test that passed with the branch deleted because a
  lower layer already trimmed. Sharpened check, which is what caught all three: revert-proving the FIX is
  not sufficient — mutate every OTHER surface of the mechanism the fix introduces (each interface method,
  each regex boundary, each guard) and treat a surviving mutant as an unpinned behaviour, not a
  cosmetic gap.
  **FIFTH sighting** (claim-rejection timing floor, `624d445`): the mechanism was gated inside
  `handleStubFailure`, and every one of its 459 test lines drove that function through a stub `Hydrator` —
  the step-4 call site. Every real `ClaimKeyInvalid` is minted by the script at step 5, so the one call site
  carrying all production traffic could be re-anchored (destroying the property) with the whole file green.
  Not lint-mechanizable; the mandated TEST SHAPE is: when a mechanism is gated at a function with several
  call sites, enumerate them and assert the one production actually reaches, not the one a stub makes easy.
  Corollary the same fire paid for: a LOWER-BOUND assertion cannot detect a mechanism anchored too late,
  because anchoring late makes a reply later, not earlier — the discriminator must be alignment or a
  two-case comparison.
  **SIXTH sighting** (NFR-S6 quantum deletion), in the shape that no output-based test can reach: the
  property was "every rejection cause executes the same instructions", and the mutation that breaks it —
  wrapping the two `crypto` calls in a condition and setting the outcome in the `else` — leaves every
  observable identical. Both operations' full behavioural suites stayed green under it; only a structural
  assertion over the shipped script text reds. Mandated shape: when the property under test is UNIFORMITY
  OF WORK rather than a value, the gate must read the artifact, not its outputs — assert the call sites
  exist, exactly once, at the unnested indent — because the disabling mutation is output-invariant by
  construction.
- **A tombstone retains the prior document, so a reader that does not filter `isDeleted` sees a revoked
  declaration as live** — `ddl_cache`'s custody reader filters and says why; the `script` and
  `permittedCommands` readers three blocks away did not, so an upgrade that stops emitting an aspect leaves it
  readable forever. Minted: dynamic-type-taxonomy B1 (concrete→abstract upgrade returned `Abstract:true` with
  the old script and all five commands). Check: the aspect-disposition assertion on an UPGRADE path, not just
  a fresh install.
- **Declaring one operationType on N sibling classes makes `ClassForCommand` drop it** — `buildByCommand`
  marks it ambiguous (`ddl_cache.go:526-583`), so every submitter must name a concrete class and the failure
  is silent until a live submit. Minted: dynamic-type-taxonomy B1 (30 submitters, inert only because each
  already passed an explicit class that then became invalid). Check: none yet.
- **Starlark WALL binds before the live-read budget** — one `kv.Read` per listed key; CI masks the wall
  (`PROCESSOR_SCRIPT_WALL_MS=5000`), so a locally-green script can still be wall-bound live. Minted:
  sensitive-param egress work. Check: none yet.
- **A gate that consults the in-flight batch must resolve LAST-write-wins** — the substrate commits
  duplicate keys last-write-wins (`batch.go`), so a first-match scan classifies on a mutation that never
  reaches Core KV, and a decoy placed ahead of the real write steers the gate at will. Minted:
  dynamic-type-taxonomy C1.2 (`classOf`, and the reserved-name gate's own kind lookup).
  Check: every loop over `result.Mutations` that picks a winner scans to the end.
- **A name-scoped gate must not carve out by meta-vertex KIND** — `Refresh` indexes every meta-vertex
  carrying a `canonicalName` into `byName` regardless of class, and `validateAbstractKeySegments` resolves
  a type segment through it with no kind filter, so a lens can hold a name the gate believed only a
  vertexType DDL could. Minted: dynamic-type-taxonomy C1.2 (a lens named `meta`, declared abstract, bricked
  every `vtx.meta.*` write). Check: the gate keys on the name as indexed, not on the declarer's class.
- **A per-key index that stores only the UNION cannot withdraw a contributor** — a rebuild that recomputes
  one key from the currently-indexed aggregate re-reads the very contributor it is withdrawing, so a
  tombstoned or edited declaration survives every per-key invalidate while any peer claims the same key.
  Minted: auth-plane descriptor floor (`unionFloorFromPeers` read `byOpType[op]`; `byName` carried the same
  shape, where a tombstone dropped a canonicalName a second root still declared). Check: keep per-root truth
  and derive the aggregate from it, and prove it with a TWO-claimant test asserting byte-equality against a
  full `Refresh` — a single-claimant test passes vacuously.
  **Second sighting**, on the same index and in the direction that decides a security answer: a union is a
  WIDENING, so it is the safe direction for a floor (more demotion) and the dangerous one for a closed
  declared-read set (more admission), and `floorsByOpType`'s merged entry could not tell its two consumers
  apart. Check: when one aggregate feeds both a widening and a narrowing consumer, carry the contributor
  COUNT on the entry (`DispatchTemplates.Claimants`) and let the narrowing consumer fail closed on any union
  it cannot attribute — an aggregate that erases who contributed is not a fact the narrowing side may read.
- **A corpus-wide authoring gate does not bind the runtime install path** — `scripts/lint-*.go` rules iterate
  `pkgregistry`, but an approved capability proposal materializes a one-artifact `Definition` under an
  arbitrary package name that the registry never sees, and per-`Definition` validation is trivially unique.
  Minted: auth-plane S11 (a second package could claim an `operationType` a registered package owns and union
  its read floor into it). Check: for any lint rule about package content, name the `pkgmgr` counterpart that
  enforces the same invariant at install — and put it in the shared batch builder so fresh install, upgrade
  and dry-run all see it.
- **A guard whose SUBJECT is computed from submitter-supplied input is not a guard** — the §2.5 floor's
  required-wins exclusion set was resolved from the descriptor's `reads` templates against the caller's own
  envelope, and every live descriptor's required templates are `{payload.<field>}`-rooted, so a submitter
  could place the key they were probing into that field and buy an exemption from the control — in both the
  demotion and the egress arms. Minted: descriptor-floor template coverage (`387ad81`); found independently
  by two cold reviews, one of which executed it against `cafe-domain` Charge's real descriptor. Note the
  shape: the mechanism was correct, its *input set* was attacker-addressable, and the code and design both
  described the set as "structural". Check: for any exclusion / allow / exemption set, state in one sentence
  what it is a function of; if a submitter-controlled field appears anywhere in that derivation, the control
  is submitter-revocable and the sentence is the bug. Pair it with a test that steers the field and asserts
  the control still fires.
- **"Degrade instead of refuse" on a cache load path is fail-open when the cache has ONE load point** —
  `DDLCache.Refresh` runs at construction, so a meta root skipped on a read error is missing for the process
  lifetime, not until a retry; and because the failing read is the ROOT read every loader starts from, the
  class is lost as a DDL too. Step 4 still admits the op on its vertex class, step 6.5 finds no aspect class
  and `continue`s, and a sensitive aspect commits as plaintext behind one WARN. Minted: auth-plane close-pass
  fix round (the adjudication that asked for the skip was the lead's, and a cold pass on the fix round caught
  it). Check: before trading a refusal for a warning, name the caller that will retry — if the only caller is
  construction, "degrade" means "forever", and a bounded retry then refuse is the shape.
