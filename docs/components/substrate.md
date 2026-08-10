# Substrate

**Component reference** | Audience: implementers + architects

---

## Overview

Substrate is Lattice's NATS / KV / NanoID primitive layer. Every other
component (Processor, Refractor, bootstrap, identity ops, CLI tooling) depends
on it. Substrate has no upstream Lattice dependencies — only the NATS Go
client (`nats-io/nats.go`). Its job is to expose typed, contract-aware
operations: key shape construction + parsing, atomic batch publish, KV CRUD,
the off-graph object (blob) store, message publish + `@at`/`@every` scheduling,
durable + supervised consumers and the KV-CDC watch, NanoID generation (incl.
deterministic derivation), and the typed sentinel errors the rest of the
platform branches on. It is **not** a general NATS wrapper — only the operations
Lattice components share architecturally live here. Component-specific responders
(the NATS Services / `micro` control planes) belong in the component, not in
substrate.

---

## What this component owns

| Path | Role |
|------|------|
| `internal/substrate/` | Single Go package; no sub-packages |

Key files:

| File | Contents |
|------|---------|
| `doc.go` | Package godoc; design-principles summary |
| `nanoid.go` | `NewNanoID`, `NewShortCode`, `IsValidNanoID`, `IsValidShortCode`, `Alphabet`, `NanoIDLength`, `ShortCodeLength` |
| `derive.go` | `DeriveNanoID`, `SHA256NanoID`, `NanoIDFromPCG` — **deterministic** id derivation (a fixed namespace+input → a stable NanoID; the basis of deterministic requestIds) |
| `keys.go` | `VertexKey`, `AspectKey`, `LinkKey`, `ClassifyKey`, `ParseVertexKey`, `ParseAspectKey`, `ParseLinkKey`; segment validators |
| `envelope.go` | `NewDocumentEnvelopeAt`, `AspectEnvelope`, `LinkEnvelope` — document wire shapes |
| `conn.go` | `Connect`, `Wrap`, `*Conn`; `NATS()` and `JetStream()` escape hatches; lazy `buckets` cache |
| `kv.go` | `KVGet`, `KVPut`, `KVCreate`, `KVUpdate`, `KVListKeys`, `KVPutWithTTL`, `KVDelete` |
| `kv_multi.go` | `KVGetMulti` — batched multi-subject direct get (raw `multi_last` protocol + stability-verified 413 fallback) |
| `kvhandle.go` | `KV` (an opened bucket handle) + `(*Conn).OpenKV` — used where a caller holds one bucket handle across many reads (e.g. Refractor control's validate sampler) |
| `batch.go` | `AtomicBatch`, `PublishBatch`, `BatchOp`, `PublishOp`, `BatchAck`, `PublishBatchAck`; raw-protocol implementation |
| `publish.go` | `Publish`, `PublishCore`, `ScheduleEvery`, `CancelSchedule`, `DeriveScheduleOccurrenceRequestID` — the publish + `@every` schedule surface (see [scheduling.md](./scheduling.md)) |
| `object.go` | `ObjectPut`, `ObjectGet`, `ObjectGetInfo`, `ObjectDelete`, `ObjectList`, `ObjectStoreExists`, `ObjectInfo` — the off-graph blob plane (Contract #7 §7.2 / #10 §10.8) |
| `stream.go` | `EnsureStream`, `StreamSpec` — idempotent JetStream stream provisioning (the sanctioned counterpart to the **rejected** `EnsureKV`; bucket provisioning stays in bootstrap) |
| `subscribe.go` | `SubscribeKVChanges`, `WatchKVUpdates`, `KVEvent`, `SubscribeKVOptions`, plus durable management (`PruneStaleDurables`, `DeleteDurable`, `DeleteStreamConsumer`) — the CDC watch surface |
| `errors.go` | `ErrKeyNotFound`, `ErrRevisionConflict`, `ErrAtomicBatchRejected` |
| `consumer.go` | `Decision` (`Ack`/`Nak`/`Term`/`NakWithDelay`), `DefaultRedeliveryDelay`, `Message`, `HandlerFunc`, `DurableConsumerConfig`, `RunDurableConsumer`, `applyDecision` |
| `consumer_supervisor.go` | `ConsumerSupervisor`, `NewConsumerSupervisor`, `Add`/`Remove`/`Reset`/`ResetAwaitReopen`/`Stop`/`UpdateSpec`/`Pause`/`Resume`/`PendingForConsumer` |
| `consumer_supervisor_spec.go` | `ConsumerSpec`, `DeliverPolicy`, `FailureClass`, `PauseReason`, `HealthStatus`, `HealthSink`, `SupervisedHandler`, `ClassifyFunc`, `ProbeFunc`, `DefaultProbeInterval` |
| `consumer_supervisor_pump.go` | The supervised pump loop: drain, classify, pause/probe/resume, health restore |

---

## Exported surface

### NanoID

| Symbol | Description |
|--------|-------------|
| `Alphabet` | 58-char custom alphabet: A–Z, a–z, 0–9 minus visually ambiguous chars `I`, `l`, `O`, `0`. Verified at `init()`. |
| `NanoIDLength = 20` | Canonical primary-key length |
| `ShortCodeLength = 8` | Human-facing short-code length; not a primary key (Contract #1 §1.1) |
| `NewNanoID() (string, error)` | Generate a 20-char NanoID from `crypto/rand`. Rejection sampling against a 6-bit mask; acceptance rate 58/64 ≈ 90.6%. Error only on `crypto/rand` failure (treated as unrecoverable by callers). |
| `NewShortCode() (string, error)` | Generate an 8-char display reference. Not a primary key. |
| `IsValidNanoID(s string) bool` | Report whether `s` is exactly 20 chars from `Alphabet`. Used by `ParseEnvelope` to validate `requestId` at step 1. |
| `IsValidShortCode(s string) bool` | Report whether `s` is exactly 8 chars from `Alphabet`. |

### Key construction

Key builders validate their inputs and **panic on programmer errors** — keys
are constructed from typed Go values inside the platform; the parser is the
trust boundary (untrusted input is never passed directly to a key builder).

| Function | Output shape | Notes |
|----------|-------------|-------|
| `VertexKey(type, id)` | `vtx.<type>.<id>` | `type` must match `[a-z][a-z0-9]*`; `id` must be a valid NanoID |
| `AspectKey(vtxKey, localName)` | `vtx.<type>.<id>.<localName>` | `vtxKey` must be a valid 3-segment vertex key; `localName` must match `[a-z_][a-zA-Z0-9]*` |
| `LinkKey(type1, id1, linkName, type2, id2)` | `lnk.<type1>.<id1>.<linkName>.<type2>.<id2>` | Callers pass endpoints in the DDL-declared direction (source side first, target side second per Contract #1 §1.1); substrate does NOT validate or re-sort. There is no auto-ordering by type, NanoID, or `createdAt`. |

### Key parsing and classification

| Function | Description |
|----------|-------------|
| `ClassifyKey(key) KeyKind` | Returns `KindVertex` (3 segments), `KindAspect` (4 segments), `KindLink` (6 segments), or `KindUnknown`. Validates segment shapes. |
| `ParseVertexKey(key) (type, id, ok)` | Extract type + id from a vertex key |
| `ParseAspectKey(key) (vtxKey, type, id, localName, ok)` | Extract all four components from an aspect key |
| `ParseLinkKey(key) (type1, id1, linkName, type2, id2, ok)` | Extract all six components from a link key |

### Document envelope

| Function | Description |
|----------|-------------|
| `NewDocumentEnvelopeAt(class, data, ts)` | Construct the standard document envelope `{class, data, updatedAt}` used for vertex/aspect writes |
| `AspectEnvelope(localName, data, ts)` | Convenience wrapper for aspect-specific envelopes |
| `LinkEnvelope(linkName, data, ts)` | Convenience wrapper for link envelopes |

### Connection

| Symbol | Description |
|--------|-------------|
| `Connect(ctx, ConnectOpts) (*Conn, error)` | Establish a new NATS + JetStream connection. `ConnectOpts` fields: `URL`, `Name`, `Timeout`, `MaxReconnects`, `ReconnectWait`. Defaults to `nats://localhost:4222` if URL is empty. `Timeout` bounds every dial's handshake — the initial connect **and** every automatic reconnect alike (nats.go applies the same budget to both) — zero ⇒ a substrate default (10s), deliberately not nats.go's bare 2s. The **initial** connect additionally gets a small bounded retry that `MaxReconnects`/`ReconnectWait` do not apply to (those govern only reconnects, after this first connection succeeds), skipping any auth-time rejection (wrong/expired/revoked credential) since retrying an unchanged credential cannot succeed. `NKeySeedFile` and `CredsFile` are both validated eagerly, before the retry loop is ever entered — a missing or malformed file is a permanent, one-shot error, not something the retry loop mistakes for a transient stall. `ctx` bounds that retry loop between attempts — a spent or cancelled `ctx` stops a further attempt from starting — but never shrinks `Timeout` itself, since `Timeout` is baked into the connection for its whole life and reused by every future reconnect; a single attempt already under way still cannot be interrupted early (the nats.go API has no hook for that). |
| `Wrap(nc *nats.Conn) (*Conn, error)` | Adapt an existing `*nats.Conn` into a substrate `*Conn`. Useful when callers need custom `nats.Options` beyond `ConnectOpts`. |
| `(*Conn).NATS() *nats.Conn` | Escape hatch to the underlying NATS connection. Use only when no typed substrate helper exists. |
| `(*Conn).JetStream() jetstream.JetStream` | Escape hatch to the underlying JetStream context. |
| `(*Conn).Close()` | Shut down the connection. Safe to call multiple times. |

`*Conn` lazily caches `jetstream.KeyValue` handles per bucket (locked `buckets` map). Callers never open KV handles directly.

### KV operations

All KV operations take a `context.Context`, `bucket` name, and `key`. Errors
are wrapped with operation context for log correlation.

| Function | Description |
|----------|-------------|
| `KVGet(ctx, bucket, key) (*KVEntry, error)` | Read a key. Returns `ErrKeyNotFound` (wrapped) if absent. `KVEntry` exposes `Bucket`, `Key`, `Value`, `Revision`, `Timestamp`. |
| `KVPut(ctx, bucket, key, value) (revision, error)` | Unconditional write. Rare in Lattice — Processor always uses create-or-conditional-update inside an atomic batch. |
| `KVCreate(ctx, bucket, key, value) (revision, error)` | Write only if key does not already exist. Returns `ErrRevisionConflict` if key exists. |
| `KVUpdate(ctx, bucket, key, value, expectedRevision) (revision, error)` | Write only if current revision matches. Returns `ErrRevisionConflict` on mismatch, `ErrKeyNotFound` if key was purged. |
| `KVListKeys(ctx, bucket) ([]string, error)` | Return all keys in bucket. Order unspecified. Used by DDL cache at Processor startup to enumerate `vtx.meta.>`. Heavy on large buckets — scope to bounded key sets only. |
| `KVGetMulti(ctx, bucket, keys) (map[string]*KVEntry, error)` | Batched read of every LIVE entry among `keys` (exact keys and/or NATS wildcard filters) in one atomic, point-in-time snapshot — the raw `multi_last` direct-get protocol (ADR-31, `docs/vendors.md`), computed under the stream's read lock. Result is keyed by resolved key; an absent key is simply missing from the map (no per-key error). ≤1,024 combined matched subjects rides the fast path; beyond that, transparently falls back to a stability-verified (double-drain, compared) ephemeral-consumer read. `KV.GetMulti` is the bucket-handle delegate. |
| `KVPutWithTTL(ctx, bucket, key, value, ttl) (sequence, error)` | Write with a per-message TTL via the `Nats-TTL` header. Bucket must have `AllowMsgTTL` enabled. Used for op-tracker entries (Contract #4 §4.3 — 24h TTL). Same mechanism as `AtomicBatch`'s per-op TTL. |
| `KVDelete(ctx, bucket, key) error` | Soft-delete (writes a delete marker). Subsequent reads return `ErrKeyNotFound`. |

### Atomic batch

`(*Conn).AtomicBatch(ctx context.Context, ops []BatchOp) (*BatchAck, error)`

Publishes a slice of `BatchOp` as a single NATS JetStream atomic batch. All
ops commit or none do. Implements the raw-NATS protocol (Nats-Batch-Id,
Nats-Batch-Sequence, Nats-Batch-Commit headers) directly, because the nats.go
client does not expose a high-level batch API.

Constraints:
- All ops must target the **same bucket** (cross-bucket atomicity is not
  supported by the NATS atomic batch primitive).
- The target bucket's underlying `KV_<bucket>` stream must have
  `AllowAtomicPublish` enabled (Core KV is provisioned this way by bootstrap).

`BatchOp` fields:
- `CreateOnly bool` — "key must not exist" (revision condition = 0)
- `HasRevision bool` + `Revision uint64` — expected current revision
- `TTL time.Duration` — per-op TTL (for op trackers)
- `Bucket`, `Key`, `Value`

On failure, error wraps `ErrAtomicBatchRejected`.

### Publish batch

`(*Conn).PublishBatch(ctx context.Context, ops []PublishOp) (*PublishBatchAck, error)`

Publishes a slice of `PublishOp` as a single unconditional JetStream atomic
batch to arbitrary subjects (no revision conditions, no per-key TTL). Used by
the Processor's durable outbox consumer to publish events to `events.<class>`
subjects on the `core-events` stream. All-or-nothing; order preserved via
`Nats-Batch-Sequence`.

All subjects must belong to the same JetStream stream.

`PublishOp` fields: `Subject`, `Data`, `Header` (optional extra headers).

### Publish & schedule

The plain-message surface (not the atomic-batch path) — used by schedulers and by components that emit to a stream or core subject.

| Function | Description |
|----------|-------------|
| `Publish(ctx, subject, data, header) error` | A JetStream publish with optional headers. The `@at` schedule path sets `Nats-Schedule` / `Nats-Schedule-Target` here (see [scheduling.md](./scheduling.md)). |
| `PublishCore(ctx, subject, data) error` | A plain **core-NATS** publish — no JetStream, no persistence (request/reply, control-plane-style fire-and-forget). |
| `ScheduleEvery(ctx, subject, target, interval, payload) error` | Arm a recurring `@every` schedule. `interval` must be a whole `>= 1s` duration (`minScheduleInterval`, the server's own floor). |
| `CancelSchedule(ctx, subject) error` | Cancel a pending schedule for `subject` (publishes the purge sentinel). |
| `DeriveScheduleOccurrenceRequestID(scheduleSubject, occurrence) string` | Deterministic requestId for one firing of a recurring schedule, so a redelivered firing dedups on the Contract #4 tracker. |

### Object store (off-graph blob plane)

The large-file / blob surface — bytes live in a JetStream **Object Store**, off the vertex graph, addressed by `vtx.object.*` vertices minted by the Processor (Contract #7 §7.2, #10 §10.8). Writers publish on `$O.<bucket>.>`; the object-store-manager runs the owner-cascade GC.

| Function | Description |
|----------|-------------|
| `ObjectPut(ctx, bucket, name, r, maxBytes) (ObjectInfo, error)` | Stream bytes into `bucket` under `name`, capped at `maxBytes`. Returns `ObjectInfo` (size, digest, …). |
| `ObjectGet(ctx, bucket, name) (io.ReadCloser, ObjectInfo, error)` | Open a blob for reading. |
| `ObjectGetInfo(ctx, bucket, name) (ObjectInfo, error)` | Metadata only (no bytes). |
| `ObjectDelete(ctx, bucket, name) error` | Remove a blob (the GC delete path). |
| `ObjectList(ctx, bucket) ([]ObjectInfo, error)` | Enumerate a bucket's objects. |
| `ObjectStoreExists(ctx, bucket) error` | Readiness probe for the object bucket. |

### Stream provisioning

`(*Conn).EnsureStream(ctx, spec StreamSpec) error` idempotently creates-or-updates a JetStream stream. This is the **sanctioned** provisioning helper (asymmetric with the deliberately-**rejected** `EnsureKV` — KV-bucket provisioning stays in bootstrap, not an ad-hoc substrate primitive). `StreamSpec` describes the stream name, subjects, and flags.

### Durable KV change consumer

`(*Conn).SubscribeKVChanges(ctx, bucket, keyPrefixes []string, durableName string, opts SubscribeKVOptions) (<-chan KVEvent, error)`

Creates a durable JetStream consumer over the `KV_<bucket>` backing stream,
filtered to `keyPrefixes` (must be non-empty). A single-element slice uses the
singular `FilterSubject` field; `len(keyPrefixes) > 1` uses `FilterSubjects`
(plural — nats-server treats the two as mutually exclusive configs) instead,
so an event under ANY of the prefixes surfaces. Every existing durable's
config must not change shape on a redeploy that doesn't add a second prefix,
which the singular-field-for-one-prefix rule guarantees. Re-invoking with the
same `durableName` resumes from the
persisted ack position, so a restarted consumer continues rather than
replaying from the start. This is the CDC primitive Refractor's `corekv_source`
uses to watch Core KV — `["vtx.meta.", "lnk.meta.*.subtypeOf.>"]` on one
consumer, so a lens-definition change and a dynamic-type-taxonomy change
share a single total order (dynamic-type-taxonomy-design.md §6.1).

`KVEvent` fields: `Bucket`, `Key`, `Value`, `Revision` (KV revision = backing
stream sequence), `IsDeleted` (envelope `isDeleted`, or true on a KV tombstone).
The event carries enough to reconstruct post-mutation state without an extra
`Get`.

`SubscribeKVOptions` (zero value is valid — replay-from-new, AckExplicit,
MaxDeliver=10):
- `IncludeHistory bool` — replay every existing entry under each of `keyPrefixes` from the start of the stream (default false = new mutations only).
- `AckPolicy jetstream.AckPolicy` — overrides the default `AckExplicitPolicy`; most callers leave it zero.
- `MaxDeliver int` — redelivery bound on Nak; defaults to 10 when zero.
- `Logger *slog.Logger` — diagnostics sink; defaults to `slog.Default()`.

### Durable consumer (ack-disciplined)

`(*Conn).RunDurableConsumer(ctx, cfg DurableConsumerConfig, handler HandlerFunc) error`

A minimal ack-disciplined durable consumer: it binds a durable JetStream
consumer to a stream + filter subject, drives a ctx-cancellable message loop
(reopening the iterator on transient error), hands each message to `handler`,
and applies the `Decision` the handler returns. It blocks until `ctx` is
cancelled. Re-running with the same `cfg.Durable` resumes from the last-acked
sequence; the consumer is **not** deleted on shutdown (its persisted position is
the point of "durable").

The defining property — the one `SubscribeKVChanges` lacks — is
**caller-controlled ack keyed on downstream success**. The handler returns one
of three decisions, applied after it returns (never before — the handler runs to
completion, *then* the ack is applied):

- `Ack` — message processed; advance the durable ack floor.
- `Nak` — transient failure; JetStream redelivers (at-least-once preserved).
- `Term` — poison message; never redelivered (event-loss-accepting — log loudly first).

`Message` fields handed to the handler: `Subject`, `Body`, `Sequence`
(backing-stream sequence, for diagnostics), `NumDelivered`, `NumPending`, plus
`ReplySubject` and `Header(key) string` for request-reply consumers. **Read-from-body
discipline:** the handler reads routing/identity from `Body`, not `Subject`.
`Subject` is provided **only** for mechanical key recovery (e.g. the outbox strips
`"$KV.<bucket>."` to recover the Core KV key for its tombstone delete) and
diagnostics — never design a consumer that parses the subject for identity.
`ReplySubject` + `Header` exist for the first request-reply consumer (the Processor
commit path, which answers the submitting client via the `Lattice-Reply-Inbox`
header); event/CDC consumers leave them unused.

`DurableConsumerConfig` fields:
- `Stream string` — the JetStream stream (e.g. `"KV_core-kv"`).
- `FilterSubject string` — delivery filter (e.g. `"$KV.core-kv.vtx.op.*.events"`).
- `Durable string` — durable name; same name resumes from last ack.
- `MaxDeliver int` — redelivery bound on Nak; **`<= 0` omits the bound (JetStream default = unlimited)**. (Contrast `SubscribeKVChanges`, which defaults `MaxDeliver=10`.)
- `Logger *slog.Logger` — diagnostics sink; defaults to `slog.Default()`.

`DeliverPolicy` is fixed at `DeliverAllPolicy` and `AckPolicy` at
`AckExplicitPolicy` — both are baked in, not config knobs. Empty-body messages
are delivered to the handler (the primitive is policy-free about body content);
the handler decides what they mean (the outbox acks-and-skips KV
tombstone/PURGE markers).

This primitive does **not** include pause/resume, lag polling, `Reset()` /
redelivery-policy switching, `DeliverLastPerSubjectPolicy`, per-rule consumer
management, channel-based delivery, or consumer deletion on shutdown. Those are
component-specific (Refractor owns them) and stay out of substrate per the
"architecturally common only" principle below — this primitive is the minimal
common need shared by the outbox (today) and the forward Loom/Weaver flow
engines.

**`RunDurableConsumer` vs. `SubscribeKVChanges`:** the latter is a channel-based
**auto-ack** consumer — it acks each event *after the caller reads it off the
channel*, which cannot express "ack only if my downstream publish succeeded."
`RunDurableConsumer` is the sibling primitive for callers that need
caller-controlled ack/nak/term keyed on downstream confirmation. They are
distinct primitives, not two modes of one.

### ConsumerSupervisor (supervised pump)

`RunDurableConsumer` stays dumb on purpose: one-shot bind, pump, ack, done. A
caller that needs supervision — pause/resume, an infra-recovery probe loop,
health-state persisted across restarts, or a durable whose config can change
underneath it (`Reset`) — does not graft that onto layer 1. Instead it hands a
`ConsumerSpec` to a `ConsumerSupervisor`, which owns the pump loop itself.

| Layer | Type | Content |
|-------|------|---------|
| 1 | `RunDurableConsumer` | one-shot bind + pump + ack; untouched, no supervision |
| 2 | `ConsumerSupervisor` | mechanism: spec registry + reconcile, composable pause state machine, `NakWithDelay` backoff floor, HealthSink persist/restore |
| 3 (caller) | `ConsumerSpec` hooks | policy: `Classify`, `Probe`, the message handler |

`NewConsumerSupervisor(conn *Conn) *ConsumerSupervisor` constructs a supervisor
over a connection's package-internal JetStream handle. No `jetstream` (or
`nats.go`) type appears anywhere on the supervisor's exported surface — Loom
and Weaver, like Refractor, import only `substrate/*`.

#### Spec registry + reconcile

Each `ConsumerSpec` is a full, caller-supplied description of one supervised
consumer — stream, `FilterSubject` (or `FilterSubjects`, the multi-filter set for
a durable that must cover several discrete subjects that no single wildcard
captures; mutually exclusive with `FilterSubject`), durable name (`Name`, also the
registry key), `DeliverPolicy` (`DeliverAll` or `DeliverLastPerSubject` — a
substrate-owned enum, never `jetstream.DeliverPolicy`), `DeliverGroup` (queue
group, NFR12 fan-out across instances), `RedeliveryDelay`, `ProbeInterval`,
`AckWait`, `MaxAckPending` (caps un-acked in-flight messages; `1` forces
server-side serialization — the Processor's `meta` lane uses it for the Contract
#2 §3.7 DDL-serialization guarantee; `0` leaves the JetStream default), `Workers`
(see below), plus the `Handler`/`Classify`/`Probe`/`Health`/`Logger` hooks. The
supervisor hard-codes nothing about stream shape — it is agnostic between
event-stream durables (`events.<domain>.>`) and KV-CDC durables
(`$KV.<bucket>.>`).

`Workers` gives a consumer **intra-durable concurrency**: a value above one starts
N pump goroutines that all bind the **same** durable, and JetStream load-balances
the pull consumer across them (each message delivered to exactly one worker;
`MaxAckPending` caps total in-flight across all of them). Each worker is an
independent pump with its own pause state machine, sharing only the durable — so
no worker races another. An operator `Pause`/`Resume` is lane-wide (it fans out to
every worker); an infra/structural pause stays per-worker (self-healing per
worker). `Workers` ≤ 1 (the default, and every Loom/Weaver/Refractor consumer) is
a single pump with the JetStream default prefetch untouched; a fan-out worker
bounds its prefetch (`fanOutPullMaxMessages`) so the server can fairly distribute a
finite backlog instead of letting one iterator hoard it. This is the **pull**-
consumer equivalent of a queue group; the push-only `DeliverGroup` field is
unrelated to it. The Processor's per-lane fan-out
(`LATTICE_PROCESSOR_LANES_<LANE>_CONSUMERS`) is the first user.

| Method | Behaviour |
|--------|-----------|
| `Add(ctx, spec) error` | Registers spec, idempotently creates the durable (`CreateOrUpdateConsumer`), and starts the supervised pump goroutine. A `Name` already managed is a no-op — use `Reset` to recreate a durable whose config changed. |
| `Remove(ctx, name) error` | Stops the pump **and deletes** the server-side durable. No-op if `name` is not managed. The operator-retiring-a-consumer intent. |
| `Reset(ctx, name) error` | Deletes and recreates the durable for `name`, preserving the spec's delivery policy (and `DeliverGroup`, redelivery floor, etc.) — unconditional delete, `ErrConsumerNotFound`-tolerant (TOCTOU-safe), then recreate. Signals the pump to re-open its iterator against the new durable and returns **without waiting for it**. Pair with `UpdateSpec` to change `FilterSubject` (or other config) before resetting. The delete+recreate pair is serialized **per consumer** (`managedConsumer.resetMu`), because an interleaved pair — one caller's delete landing between the other's delete and create — makes the server fail the create outright. Different durables still recreate concurrently, and the mutex covers the pair and nothing else (not the reopen, not the drain). |
| `ResetAwaitReopen(ctx, name, wait) error` | `Reset` plus a bounded, **best-effort** wait for each pump to have re-opened (consumer **and** messages iterator). For a caller that holds a bounded resource across the transition, since `Reset` alone returns midway through the handover. A *barrier*, not a proof: the acknowledgement is snapshotted **before** the reopen is requested, so an already-completed open cannot satisfy it — but an open still **in flight** against the old durable can, releasing the caller early (same consequence as the timeout below: a resource released early, nothing left inconsistent). Bounds the **handover, not the drain** — the redelivery behind the reopened iterator is ordinary pump traffic and a busy consumer never goes idle. The acknowledgement is a channel **close**, so overlapping waiters on one consumer are all released by one reopen and none can take another's. A worker holding any pause reason is signalled but **not waited on** (a paused pump cannot reopen until Resume). Expiry logs and returns `nil` — the durable is recreated either way and the pump retries on its own backoff; a cancelled `ctx` returns `ctx.Err()`. |
| `UpdateSpec(name, mutate) error` | Replaces the desired spec for an already-managed consumer without recreating the durable — typically followed by `Reset` to apply the change. |
| `Stop()` | Stops every pump but **never deletes** any durable — a durable's persisted position is the point of its durability. After `Stop`, further `Add` calls are rejected. Callers that want delete-on-shutdown call `Remove` per consumer from their own adapter layer (this is Refractor's shutdown policy, not the supervisor's). |
| `PendingForConsumer(ctx, name) (uint64, error)` | Returns the pending (un-delivered) message count for the named durable — a substrate-typed accessor so callers (e.g. Refractor's rebuild lag-watch) need no `jetstream.Consumer` handle. |

The supervisor never sets `MaxDeliver` on any consumer it creates: retry
*cadence* is bounded (via `NakWithDelay`, below) but retry *count* never is.

#### Composable pause state machine

Each managed consumer tracks a **set** of active pause reasons, not a single
value — the pump runs only when the set is empty:

| Reason | Cleared by |
|--------|-----------|
| `PauseInfra` | A passing `Probe`, automatically (the probe loop) |
| `PauseStructural` | An operator `Resume` only |
| `PauseManual` | An operator `Resume` only |

A manual (operator) pause is never cleared by a passing probe — composability
means `PauseManual` and `PauseInfra` can both be set, and a probe success
clears only `PauseInfra`. `Pause(ctx, name)` adds `PauseManual` and is
idempotent. `Resume(ctx, name)` clears `PauseManual` + `PauseStructural` and
force-exits an in-flight probe loop, so processing resumes without waiting for
the next probe tick.

`Resume` only clears reasons that were active at the moment it was called: a
pause reason added *after* a `Resume` — e.g. a structural escalation the probe
loop discovers, or a fresh infra failure on the next pump iteration — is not
retroactively cleared by that earlier `Resume`. The new failure re-enters its
own pause state and needs its own `Resume`.

When a `ClassInfra`-classified error pauses the pump, the supervisor enters a
probe loop: it polls the spec's `Probe(ctx)` hook at `ProbeInterval` (default
`DefaultProbeInterval = 10s`) until it returns nil (clears `PauseInfra`) or an
error that `Classify` maps to `ClassStructural` (escalates to
`PauseStructural`, which then blocks awaiting `Resume`). Structural and manual
pauses block awaiting `Resume` or ctx-done, exactly like the probe loop's exit
conditions.

#### Policy hooks

Policy stays with the caller via three `ConsumerSpec` hooks:

- **`Classify(err) FailureClass`** — maps a handler/probe error to
  `ClassTransient` (default for nil/unrecognised `Classify`; redeliver),
  `ClassTerminal` (handler disposes; supervisor doesn't `Term` it — policy
  stays with the caller), `ClassInfra` (pause + probe loop), or
  `ClassStructural` (pause awaiting `Resume`). This mirrors a caller's own
  4-tier taxonomy (e.g. Refractor's `failure.Category`) without the supervisor
  importing any caller package.
- **`Probe(ctx) error`** — checks whether a paused-on-infra dependency has
  recovered. A nil `Probe` makes an infra pause behave like a structural one
  (no automatic recovery).
- **`SupervisedHandler(ctx, msg Message) (Decision, error)`** — the message
  handler. A nil error means the handler disposed of the message itself
  (success, skip, terminal-to-DLQ, or a deferred retry-queue enqueue); the
  returned `Decision` (`Ack`/`Nak`/`NakWithDelay`/`Term`) is applied to the
  JetStream message. A non-nil error routes through `Classify`:
  `ClassInfra`/`ClassStructural` pause the pump and leave the message
  un-acked/un-naked so JetStream redelivers it on resume (mirroring the
  "do NOT ack/nak on infra/structural" contract); `ClassTransient`/
  `ClassTerminal` fall back to the returned `Decision`. The handler MUST be
  idempotent — at-least-once delivery means the same message can arrive again
  after a `Nak`, a pause/resume, or a crash-before-ack.

`Message` carries `Subject`, `Body`, `Sequence`, `NumDelivered` (the JetStream
delivery count, 1 on first delivery), `NumPending`, and — for request-reply
consumers — `ReplySubject` and `Header(key)` — enough for a supervised handler to
reason about redelivery and answer a caller without a `jetstream.Msg`.

#### HealthSink — persist + restore

`HealthSink` is a small, caller-keyed interface — `SetActive(ctx)`,
`SetPaused(ctx, reason, lastErr)`, `Load(ctx) (HealthStatus, PauseReason,
error)`. The supervisor never invents or namespaces health keys: the caller
supplies both the key and the bucket via its `HealthSink` implementation
(Refractor's existing bare-`<ruleId>` Health KV key stays byte-identical; Loom
/ Weaver later use `health.loom.<instance>` / `health.weaver.<target>`). A nil
`Health` skips all health I/O — the supervisor still runs.

Every state transition is persisted through the sink; sink errors are logged,
never fatal. When multiple pause reasons are active, the persisted reason
follows precedence **manual > structural > infra** (today's pump never
persists two reasons at once — this only governs the composable machine's
restore tie-break; an unpersisted lower-precedence reason simply re-presents
on the next pump failure and re-enters its own pause path — self-healing).

At startup, `Add` restores from the sink with these exact semantics:

- Status ≠ `"paused"` (including unrecognised statuses and an interrupted
  `"rebuilding"`) → active, pump immediately.
- `"paused"` + `PauseInfra` → re-enter the probe loop.
- `"paused"` + `PauseStructural` / `PauseManual` → block awaiting `Resume`.
- A malformed entry (nil reason) or a `Load` error → log a warning and treat
  as active.

#### NakWithDelay (backoff)

`Decision` gained a fourth value, appended to the end of the iota
(`Ack=0`/`Nak=1`/`Term=2`/`NakWithDelay=3` — binary-additive; every existing
`Ack`/`Nak`/`Term` caller compiles and behaves identically):

- **`NakWithDelay`** — a transient failure that must not hot-loop:
  `applyDecision` maps it to `msg.NakWithDelay(delay)`, where `delay` is the
  consumer's `RedeliveryDelay` (a fixed, per-spec, **non-exponential**
  redelivery floor — never carried on the `Decision` itself). A zero
  `RedeliveryDelay` falls back to the package default,
  `DefaultRedeliveryDelay = 5s`, rather than degrading silently to plain `Nak`
  (a handler returning `NakWithDelay` has explicitly said "do not hot-loop";
  silent immediate redelivery would reintroduce exactly that spin).

The redelivery floor bounds retry *cadence*; retry *count* is never bounded —
the supervisor never sets `MaxDeliver` on any consumer it creates.

### Sentinel errors

| Error | Returned by |
|-------|------------|
| `ErrKeyNotFound` | `KVGet`, `KVUpdate`, `KVDelete` when key is absent |
| `ErrRevisionConflict` | `KVCreate` (key already exists), `KVUpdate` (revision mismatch) |
| `ErrAtomicBatchRejected` | `AtomicBatch`, `PublishBatch` on any failure; wraps the underlying error for diagnostics |

All three are plain `errors.New` sentinels wrapped with context via
`fmt.Errorf("%w: ...")`. Callers use `errors.Is` to branch on them.

---

## Principles (binding)

- **Substrate exposes only operations that are architecturally common across components.** NATS Services framework helpers, JetStream-consumer-management helpers, and watch helpers that are component-specific belong in the component, not in substrate. Adding component-specific logic to substrate requires a design decision.
- **Key shape validation is centralized in `ClassifyKey` + `IsValidNanoID`.** No other code may re-implement these checks. All key-creation entry points in the platform go through the substrate key builders.
- **All Lattice components consume substrate; substrate consumes only `nats-io/nats.go`.** This dependency direction is a hard constraint. Circular imports (substrate importing from Processor, Refractor, etc.) are forbidden.
- **Programmer errors panic; operational failures return typed sentinel errors.** Key builders panic on invalid input because key construction only occurs from trusted typed values inside the platform — the upstream parser is the trust boundary.

---

## What's deferred

| Feature | Phase | Notes |
|---------|-------|-------|
| NATS account-level auth | 🔭 Designed (ratified 2026-06-27) | `substrate.Connect` inherits connection auth from the environment; there is no account-level write enforcement. Closing the fabricated-KV-write surface at the substrate level (defended today only by Refractor overwrite-by-reprojection) is the **NATS account write-restriction** design: per-component NKey users with scoped publish permissions. **Fire 1 shipped** (`Connect`'s `NKeySeedFile`/`CredsFile` credential seam — dark + no-op by default, `75e9acc`); the enforcement turn-on (per-component `nats-server.conf` + Gate 2/3 flips, Fire 2) is pending. |
| Inner-package migration of Refractor sub-packages to substrate | ✅ Done | `internal/refractor` (non-test) now reaches NATS only through `substrate.Conn` — the sole residual raw `nats.go`/`jetstream` handle is `control/service.go`'s `micro.Service` responder (the accepted exception). The one remaining raw `js.CreateKeyValue` lives in `cmd/refractor` (target-bucket provisioning, out of inner-package scope — provisioning belongs to bootstrap, no `substrate.EnsureKV`). |
| `AdjacencyForNode` substrate helper | Not built | A standalone `(*Conn).AdjacencyForNode` was contemplated for inbound-link enumeration but never needed — the adjacency index is read directly by Refractor's cypher executor. Revisit only if another component needs adjacency lookups outside Refractor. |
| Cross-bucket atomic batch | Not planned (NATS limitation) | Cross-bucket atomicity is not supported by the NATS atomic batch primitive. Callers that need cross-bucket coordination must implement application-level compensation. |

---

## Review keeps catching (dossier)

Same contract as every dossier: fire briefs copy the applicable entries into part 5
(`agents/fire-brief-template.md`); the item-close review appends new ones (`agents/steward/SKILL.md` §4);
**capped at 12 one-liners**; an entry retires when a lint/test gate mechanizes it.

- **Narrowing a JetStream consumer's filter strands its pending set** — messages pending under the old
  filter never redeliver under the new one; widen-then-drain or recreate the consumer. Minted: the
  JetStream filter-narrowing incident. Check: none yet.
- **The batch CAS is per-subject** (`Nats-Expected-Last-Subject-Sequence`), not whole-stream —
  different-key writes never serialize, so don't design contention remedies for a lock that does not
  exist. Minted: `substrate/batch.go` grounding (designer SKILL §2 trial). Check: none yet.
- **RETIRED (the model of a retired entry):** hand-rolled embedded-NATS test fixtures inherit nats.go's
  2-second no-retry handshake — mechanized: `lint-conventions` blocks bare `nats.Connect` in tests; use
  `internal/natsfixture`.
- **A multi-round or incremental read must RETRACT, not merely skip, a tombstone** — a delete marker
  arriving in a later round than the entry it kills leaves the accumulated map holding a hard-deleted key,
  and downstream that is an over-grant. A single-round read hides the class entirely, because the marker is
  always the subject's last message. Minted: adjacency Shape B, twice in one item — `consumer/bootstrap.go`
  acking an empty body before classifying the key, and `drainDirectGetFallback` skipping markers once it
  accumulated across rounds. Check: every multi-phase read gets a case deleting a key BETWEEN the phases,
  not before them.
- **A process-local memo of server-owned state must name its invalidation boundary** — a `sync.Once` or map
  caching a value the server owns has no path back when the server changes it. Minted: adjacency Shape B —
  a mark cache that outlived a wiped bucket (deleted), and a `MaxPayload` memo whose "fixed for the
  connection's lifetime" premise the pinned nats.go contradicts via `processAsyncInfo` (reverted). Check:
  for any such memo, name the boundary that moves the source and either follow it or say why it cannot move.
- **A vendor-behaviour claim in a comment needs a pinned `file:line`** — this package cites
  `nats-server/server/*.go:NNN` for every protocol constant; the one comment that asserted a nats.go
  contract without a citation was wrong. Minted: adjacency Shape B (`valueSizeLimit`'s memo justification).
  Check: a claim about vendor behaviour carries the pinned source location, or it is a hypothesis.
- **A reopen/retry loop needs a connection-closed exit of its own** — an "is it really gone" probe is a
  round trip, so it cannot answer over a closed connection, and its unanswerable case resolves to keep
  trying: right for a stall, unbounded for a close. The loop then spins at its backoff for as long as the
  caller's ctx outlives its connection, which for every test that closes a fixture before cancelling is the
  rest of the process — it reddened an unrelated package by starvation. Minted: dynamic-type-taxonomy C2.5.
  Check: the loop tests `nc.IsClosed()` BEFORE any probe, since the probe is what cannot decide it.
