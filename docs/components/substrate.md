# Substrate

**Component reference** | Audience: implementers + architects | Last verified: 2026-05-19

---

## Overview

Substrate is Lattice's NATS / KV / NanoID primitive layer. Every other
component (Processor, Refractor, bootstrap, identity ops, CLI tooling) depends
on it. Substrate has no upstream Lattice dependencies — only the NATS Go
client (`nats-io/nats.go`). Its job is to expose typed, contract-aware
operations: key shape construction + parsing, atomic batch publish, KV CRUD,
NanoID generation, and the typed sentinel errors the rest of the platform
branches on. It is **not** a general NATS wrapper — only the operations
Lattice components share architecturally live here. Component-specific helpers
(JetStream consumer management, watch helpers, NATS Services) belong in the
component, not in substrate.

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
| `keys.go` | `VertexKey`, `AspectKey`, `LinkKey`, `ClassifyKey`, `ParseVertexKey`, `ParseAspectKey`, `ParseLinkKey`; segment validators |
| `envelope.go` | `NewDocumentEnvelopeAt`, `AspectEnvelope`, `LinkEnvelope` — document wire shapes |
| `conn.go` | `Connect`, `Wrap`, `*Conn`; `NATS()` and `JetStream()` escape hatches; lazy `buckets` cache |
| `kv.go` | `KVGet`, `KVPut`, `KVCreate`, `KVUpdate`, `KVListKeys`, `KVPutWithTTL`, `KVDelete` |
| `batch.go` | `AtomicBatch`, `PublishBatch`, `BatchOp`, `PublishOp`, `BatchAck`, `PublishBatchAck`; raw-protocol implementation |
| `errors.go` | `ErrKeyNotFound`, `ErrRevisionConflict`, `ErrAtomicBatchRejected` |

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
| `Connect(ctx, ConnectOpts) (*Conn, error)` | Establish a new NATS + JetStream connection. `ConnectOpts` fields: `URL`, `Name`, `MaxReconnects`, `ReconnectWait`. Defaults to `nats://localhost:4222` if URL is empty. |
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
| `KVPutWithTTL(ctx, bucket, key, value, ttl) (sequence, error)` | Write with a per-message TTL via the `Nats-TTL` header. Bucket must have `AllowMsgTTL` enabled. Used for op-tracker entries (Contract #4 §4.3 — 24h TTL). Same mechanism as `AtomicBatch`'s per-op TTL. |
| `KVDelete(ctx, bucket, key) error` | Soft-delete (writes a delete marker). Subsequent reads return `ErrKeyNotFound`. |

### Atomic batch

`(*Conn).AtomicBatch(ops []BatchOp, timeout time.Duration) (*BatchAck, error)`

Publishes a slice of `BatchOp` as a single NATS JetStream atomic batch. All
ops commit or none do. Implements the raw-NATS protocol (Nats-Batch-Id,
Nats-Batch-Sequence, Nats-Batch-Commit headers) described in the Story 1.1
spike, because the nats.go client does not expose a high-level batch API.

Constraints:
- All ops must target the **same bucket** (cross-bucket atomicity is not
  supported by the NATS atomic batch primitive — documented in the Story 1.1
  spike; Story 4.6 confirms this constraint remains for Phase 1).
- The target bucket's underlying `KV_<bucket>` stream must have
  `AllowAtomicPublish` enabled (Core KV is provisioned this way by bootstrap).

`BatchOp` fields:
- `CreateOnly bool` — "key must not exist" (revision condition = 0)
- `HasRevision bool` + `Revision uint64` — expected current revision
- `TTL time.Duration` — per-op TTL (for op trackers)
- `Bucket`, `Key`, `Value`

On failure, error wraps `ErrAtomicBatchRejected`.

### Publish batch

`(*Conn).PublishBatch(ops []PublishOp, timeout time.Duration) (*PublishBatchAck, error)`

Publishes a slice of `PublishOp` as a single unconditional JetStream atomic
batch to arbitrary subjects (no revision conditions, no per-key TTL). Used by
Processor's step 9 to publish events to `events.<class>` subjects on the
`core-events` stream. All-or-nothing; order preserved via `Nats-Batch-Sequence`.

All subjects must belong to the same JetStream stream.

`PublishOp` fields: `Subject`, `Data`, `Header` (optional extra headers).

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

## Post-Story-4.7 planned additions

These helpers are not in the current codebase. They are described here so
Winston can place them correctly in substrate when they ship.

| Planned symbol | Description |
|----------------|-------------|
| `(*Conn).AdjacencyForNode(nodeKey string) -> []string` | Reads Refractor's `refractor-adjacency` bucket; returns list of EdgeIDs (= link keys) for all inbound + outbound edges of the given node. Required by Story 4.6 `MergeIdentity` for inbound-link enumeration without a global `lnk.*` scan. |
| `(*Conn).SubscribeKVChanges(bucket, keyPrefix, durableName) -> <-chan KVEvent` | Durable JetStream consumer over the `KV_<bucket>` backing stream, filtered to `keyPrefix`. Replaces `kv.Watch` in Refractor's `corekv_source.go` per Story 2.4b. Will land in substrate because Refractor's consumer unification (Story 2.4b) uses it, and the pattern may be reused by other components in Phase 2. |

---

## What's deferred

| Feature | Phase | Notes |
|---------|-------|-------|
| Real NATS auth | Phase 2 | Current `substrate.Connect` inherits connection auth from the environment; no account-level enforcement. Story 3.7 Vector #1 (fabricated-KV-write attack) noted the gap; full enforcement is Phase 2. |
| Inner-package migration of Refractor packages to use substrate uniformly | Story 2.4b (partial) | Deviation 5 carry: some Refractor sub-packages hold their own JetStream / KV handles rather than going through `substrate.Conn`. Story 2.4b absorbs some of this; full migration is ongoing. |
| Cross-bucket atomic batch | Not planned (NATS limitation) | Cross-bucket atomicity is not supported by the NATS atomic batch primitive (Story 1.1 spike). Callers that need cross-bucket coordination must implement application-level compensation. |
