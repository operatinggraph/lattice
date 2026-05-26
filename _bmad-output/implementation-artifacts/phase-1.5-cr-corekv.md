# Core KV / Substrate CR Report — Phase 1.5

## Summary

- Files reviewed: 7 (`internal/substrate/batch.go`, `internal/substrate/conn.go`, `internal/substrate/doc.go`, `internal/substrate/envelope.go`, `internal/substrate/errors.go`, `internal/substrate/keys.go`, `internal/substrate/kv.go`, `internal/substrate/nanoid.go`, `internal/substrate/subscribe.go`) + `CONTRACT-AMENDMENT-REQUEST.md`
- P0 findings: 0
- P1 findings: 3
- P2 findings: 6
- Nit findings: 5
- History comments: 5 (concentrated in `batch.go`)

---

## P0 Findings

None.

---

## P1 Findings

### [F-001] `AtomicBatch` and `PublishBatch` accept no context — caller cancellation is impossible; timeout is caller-supplied but cannot be shortened by upstream context

**File:** `internal/substrate/batch.go:77` / `internal/substrate/batch.go:189`

**What:** Both `AtomicBatch` and `PublishBatch` accept a `timeout time.Duration` parameter rather than a `context.Context`. Inside `publishAtomicBatch`, fire-and-forget messages are sent via `nc.PublishMsg(m)` (which has no cancellation hook) and the commit message is sent via `nc.RequestMsg(m, timeout)`. If the caller's context is cancelled while the batch is in flight, neither the in-flight publishes nor the pending `RequestMsg` will be interrupted. The process will hang until `timeout` elapses.

This was flagged from the Bootstrap/Kernel CR (N-004) as a substrate-side limitation. At that layer it was a Nit because the hardcoded 30s comes from bootstrap's call site. On the substrate side it is a P1: every caller — Processor commit step, bootstrap, any future write path — inherits this inability to honour upstream context cancellation. A SIGTERM during a high-load batch commit will cause a graceless shutdown delay of up to `timeout` seconds with no way for callers to shortcircuit it.

**Why it matters:** In production the Processor runs with a 5s timeout and issues AtomicBatch on every committed operation. A 5s hang per commit on a NATS partition event multiplied across goroutines will exhaust connection resources. In bootstrap the 30s timeout makes clean container shutdown (Docker `stop` SIGTERM → SIGKILL default of 10s) race with the batch. Neither caller can respect a `context.WithTimeout` or `context.WithDeadline` passed from the top of the call tree.

**Cross-CR note:** The Bootstrap CR flagged the bootstrap side (N-004) as a Nit. This substrate finding is a separate P1 — the contract surface is here.

**Suggested fix:** Change the signature of `AtomicBatch` and `PublishBatch` to accept `ctx context.Context` as the first parameter. Use `ctx` to construct the `RequestMsg` deadline (via `nats.Conn.RequestMsgWithContext` or manual context-to-timeout conversion) and to check `ctx.Err()` before each `PublishMsg`. For the fire-and-forget messages the only practical mitigation without nats.go `PublishMsgWithContext` support is to check `ctx.Err()` before sending each one. This allows callers to use `context.WithTimeout` end-to-end without a separate `timeout` parameter.

---

### [F-002] `bucket()` in `conn.go` has a TOCTOU race: double-open is possible under concurrent callers for a new bucket name

**File:** `internal/substrate/conn.go:107-122`

**What:** The lock pattern in `bucket()` is: unlock → open → lock → store. Specifically:

```go
c.mu.Unlock()           // (1) released here
kv, err := c.js.KeyValue(ctx, name)   // (2) open (potentially slow)
// ...
c.mu.Lock()             // (3) re-acquired
c.buckets[name] = kv    // (4) stored
c.mu.Unlock()
```

Between (1) and (3), a second concurrent call for the same bucket name also passes the `ok` check at the top (the bucket is not in the map yet), also calls `c.js.KeyValue()`, and then races to write into `c.buckets[name]`. The net result: two separate `jetstream.KeyValue` handles are created for the same bucket. The second writer's handle silently overwrites the first in the map, but callers who captured the first handle from their own call continue using it. If the two handles diverge (e.g., if `KeyValue()` has any per-handle internal state or if the first handle was used before being evicted), behaviours can diverge silently.

In practice `jetstream.KeyValue` handles are stateless wrappers around the stream name, so this race is unlikely to cause data corruption. But it is a correctness gap in the contract surface: `bucket()` is documented as "lazily caches … on the first call per bucket" — the current code does not honour "first call."

**Why it matters:** The race matters more when `KeyValue()` is slow (NATS latency, large stream metadata) — the window for concurrent opens widens. Under high first-connect concurrency (Processor startup enumerating multiple buckets in parallel goroutines) each bucket may be opened multiple times.

**Suggested fix:** Standard check-then-open-with-lock pattern. Either:
1. Hold the lock across the open: accept the lock contention cost (open is a network call but buckets are few and opens are one-time-per-process).
2. Use a `sync.Map` or a "double-checked locking with `sync.Once`-per-bucket" pattern to allow parallel opens of *different* buckets but serialize per-bucket:
```go
c.mu.Lock()
if kv, ok := c.buckets[name]; ok {
    c.mu.Unlock()
    return kv, nil
}
c.mu.Unlock()
// potential duplicate open — use a singleflight or per-name once
```
The simplest correct fix is to hold `c.mu` across the `KeyValue` call. The lock contention is negligible given buckets are opened once per process.

---

### [F-003] `isRevisionConflict` is unexported — bootstrap duplicates it with weaker logic; no shared contract for revision-conflict detection

**File:** `internal/substrate/kv.go:169-181`

**What:** `isRevisionConflict` is package-private. The Bootstrap/Kernel CR (F-008) found that `internal/bootstrap/primordial.go` duplicates the same string-matching logic but **without** the `errors.Is(err, jetstream.ErrKeyExists)` typed sentinel check. The bootstrap duplicate is therefore strictly weaker and relies entirely on NATS error message strings, which have changed between NATS server versions.

The substrate package is explicitly documented as the shared contract surface ("All Lattice components … must use substrate rather than duplicating low-level NATS code"). Failing to export this function directly contradicts that principle and guarantees drift.

**Why it matters:** Any future package that does a direct `jetstream.KeyValue.Create` call (rather than going through `substrate.KVCreate`) will need to detect revision conflicts. Without an exported helper it will either miss the typed sentinel or duplicate the string matching. The bootstrap case is the already-confirmed instance of this occurring.

**Suggested fix:** Export `isRevisionConflict` as `IsRevisionConflict(err error) bool`. The Bootstrap CR F-008 recommends callers use it; this is the substrate side of that recommendation. Since it currently only needs to be visible for testing the transition, a lighter-weight export of `ErrRevisionConflict` and `ErrKeyNotFound` as sentinel-wrapping helpers is also acceptable, but the function export is cleaner.

---

## P2 Findings

### [F-004] `KVGet` returns the value bytes for a tombstoned key (NATS KV delete marker) — the contract is unspecified and the current behavior is wrong

**File:** `internal/substrate/kv.go:27-46`

**What:** After `KVDelete`, NATS JetStream creates a "delete marker" entry: a message with empty body and a `KV-Operation: DEL` header. The `jetstream.KeyValue.Get` call in `KVGet` returns `jetstream.ErrKeyNotFound` for this case, so `KVGet` correctly returns `ErrKeyNotFound` after a `KVDelete`. This path is fine.

The problem is the soft-delete path: if a caller uses `KVPut` (or `AtomicBatch`) to write an envelope with `"isDeleted": true` (the Lattice logical-delete pattern used by Processor commit), the entry is still physically present. `KVGet` returns this entry with `Value` containing the tombstoned envelope and `KVEntry.IsDeleted` is... not set. `KVEntry` has no `IsDeleted` field.

Callers who want to distinguish "key not found" from "key found but logically deleted" must unmarshal the returned `Value` and inspect `isDeleted` themselves. This is invisible from the `KVEntry` struct — the field is present in `KVEvent` (subscribe path) but absent in `KVEntry` (read path). This asymmetry between the read and subscribe contract surfaces is an API design gap that will cause subtle consumer bugs.

**Why it matters:** A caller doing `entry, err := c.KVGet(...); if err != nil { ... }` after a logical delete succeeds with `err == nil`, receiving an envelope with `isDeleted=true`. Unless the caller also parses the Value JSON, it has no signal that the entity was deleted. In the Processor's get-then-update path, acting on a logically-deleted entity produces a write with `isDeleted=true` that silently resurrects the entity.

**Suggested fix:** Add an `IsDeleted bool` field to `KVEntry`. Populate it by performing a minimal JSON probe in `KVGet` (same pattern as `decodeKVMessage` in `subscribe.go`). This makes the read and subscribe contracts symmetric. Alternatively, add a `KVGetAlive` variant that returns `ErrKeyNotFound` when the envelope's `isDeleted` is true, and document in `KVGet` that it does not perform this check.

---

### [F-005] `normalizePrefix` silently produces an exact-match filter when a bare literal with no trailing dot is passed — subscriber gets fewer events than expected with no error

**File:** `internal/substrate/subscribe.go:131-144`

**What:** Per the doc comment: "Bare literal — match it exactly. (Callers wanting 'prefix and children' should pass a trailing '.'.)". If a caller passes `"vtx.meta"` (no trailing dot), `normalizePrefix` returns `"vtx.meta"` unchanged, and `filterSubject` becomes `"$KV.core-kv.vtx.meta"`. This matches **only** the key `vtx.meta` itself — not `vtx.meta.anything`. A caller expecting to watch all meta-vertices by passing `"vtx.meta"` instead of `"vtx.meta."` will create a consumer that receives no events and blocks silently.

The doc comment does describe this behaviour, but it is a latent API footgun: the distinction between `"vtx.meta"` and `"vtx.meta."` is invisible to callers and produces no warning.

**Why it matters:** The Processor's DDL cache (`KVListKeys` + `SubscribeKVChanges`) uses the prefix `"vtx.meta."` (correct). But any future caller that passes `"vtx.meta"` — an easy typo — will silently subscribe to nothing. The channel will remain open, `mc.Next()` will block indefinitely, and the caller will see no events with no error.

**Suggested fix:** When the prefix is a literal that does not end with `.`, `>`, or `*`, either:
1. Return an error from `SubscribeKVChanges` requiring the caller to be explicit about match semantics; or
2. Append `">"` by default (watch prefix-and-children), since the common use-case is prefix matching. Document the exact-match case as requiring an explicit suffix like `"exact:vtx.meta"`.

Option 1 (fail on ambiguous input) is safer for a contracts surface.

---

### [F-006] `KVDelete` has no revision-conditioned variant — callers cannot prevent a delete-after-write race

**File:** `internal/substrate/kv.go:153-163`

**What:** `KVDelete` calls `kv.Delete(ctx, key)` unconditionally. NATS `jetstream.KeyValue` exposes `DeleteLastRevision(key, lastRevision uint64)` (or the equivalent `Delete` with a `jetstream.LastRevision` option in newer nats.go versions) that allows a conditional delete: "delete only if current revision is X." Substrate does not expose this.

A Processor that reads an entry at revision R, validates it can be deleted, then calls `KVDelete` — the deletion will proceed even if another write occurred between the read and the delete, silently deleting a newer entry than the caller inspected.

**Why it matters:** Without conditional delete, the Processor's soft-delete path (writing `isDeleted=true` via `AtomicBatch` with `HasRevision: true`) is the only safe delete pattern. `KVDelete` as currently exposed is a footgun for any caller that cares about the state they validated before deciding to delete. This is particularly important for Refractor consumers that need to issue deletes under optimistic concurrency.

**Suggested fix:** Add `KVDeleteRevision(ctx context.Context, bucket, key string, expectedRevision uint64) error` that wraps `kv.Delete(ctx, key, jetstream.LastRevision(expectedRevision))`. Document that `KVDelete` is unconditional and should only be used for operational (non-concurrency-sensitive) cleanup. Or rename `KVDelete` to `KVDeleteUnconditioned` to force callers to be explicit.

---

### [F-007] `KVListKeys` silently returns deleted/tombstoned keys — callers that iterate the result for live entries get stale keys

**File:** `internal/substrate/kv.go:110-125`

**What:** `kv.ListKeys(ctx)` in NATS JetStream KV returns all keys that have ever existed in the bucket (including tombstoned/deleted ones), not just live keys. The doc comment says "returns all keys present in bucket" — which is ambiguous about whether deleted keys are included. In practice, `jetstream.KeyValue.ListKeys()` returns only keys whose last entry is not a delete marker (i.e., it does filter tombstones). But it does **not** filter logical-deletes (envelopes with `isDeleted: true`).

The Processor's DDL cache (identified as the primary user of `KVListKeys`) will receive meta-vertex keys for tombstoned DDL entries. On subsequent `KVGet`, the caller gets back an envelope with `isDeleted: true` and must handle it — but the F-004 gap means `KVEntry` doesn't surface this field, making the filter logic error-prone.

**Why it matters:** The DDL cache at startup iterates `KVListKeys` to build its initial state. If any tombstoned meta-vertex keys are returned and the caller does not manually unmarshal and filter `isDeleted`, the cache is populated with dead entries. This is compounded by F-004.

**Suggested fix:** In the doc comment for `KVListKeys`, explicitly state: "Returns all keys with live (non-tombstone) entries. Does NOT filter logically-deleted envelopes (isDeleted=true). Callers that only want live entities must inspect KVEntry.Value." This is a documentation fix given that full filtering would require a per-key get. The root fix is F-004 (add `IsDeleted` to `KVEntry`).

---

### [F-008] `NewDocumentEnvelope` and `NewDocumentEnvelopeAt` do not validate inputs — empty `class`, empty `actor`, or empty `opTracker` produces a structurally invalid envelope with no error

**File:** `internal/substrate/envelope.go:63-83`

**What:** Both constructors accept `class`, `actor`, and `opTracker` as arbitrary strings with no validation. An empty `class` produces `"class": ""` in the serialized JSON, which would be rejected by any downstream schema validator or Processor field check — but the envelope itself is returned successfully. An empty `actor` or `opTracker` produces `"createdBy": ""` and similar, violating the Contract #1 §1.3 mandatory-field requirement.

The `Update` / `UpdateAt` methods have the same gap for `actor` and `opTracker`.

**Why it matters:** The envelope constructors are the creation path for all Core KV values. A future caller (e.g., a new Capability Package) that accidentally passes an empty string for `class` will write a structurally invalid envelope to Core KV without any indication at write time. The error will surface much later, at read time, in a different component.

**Suggested fix:** Add precondition panics (matching the "programmer errors panic" principle already established in `keys.go`) for empty `class`, `actor`, and `opTracker`:
```go
if class == "" {
    panic("substrate: NewDocumentEnvelopeAt: class must not be empty")
}
if actor == "" {
    panic("substrate: NewDocumentEnvelopeAt: actor must not be empty")
}
if opTracker == "" {
    panic("substrate: NewDocumentEnvelopeAt: opTracker must not be empty")
}
```
The same guards should be added to `UpdateAt`. These are analogous to the `mustValidateType` / `mustValidateNanoID` panics in `keys.go`.

---

## Nit Findings

### [N-001] `CanonicalLinkOrder` is referenced in `LinkKey`'s doc comment but does not exist in this package

**File:** `internal/substrate/keys.go:52-54`

The `LinkKey` doc comment says: "Use `CanonicalLinkOrder` if you only have (a, b) with timestamps." No function named `CanonicalLinkOrder` exists in `internal/substrate/`. Either the function was planned and never implemented, or it lives elsewhere without being cross-referenced. Callers who need to establish youngerness/olderness for link construction have no documented substrate helper to call.

**Fix:** Either implement `CanonicalLinkOrder(aKey, aCreatedAt, bKey, bCreatedAt string) (younger, older string)` in `keys.go`, or remove the reference from the `LinkKey` doc comment.

---

### [N-002] `Revision` and `Sequence` fields on `KVEvent` are always equal — the distinction is unexplained and will confuse future consumers

**File:** `internal/substrate/subscribe.go:222-231`

Both `evt.Revision` and `evt.Sequence` are set from `meta.Sequence.Stream`. The comment on `KVEvent.Revision` says "KV revision (== JetStream sequence for KV-backed streams)" and `KVEvent.Sequence` says "JetStream sequence of the underlying message" — they are the same value by construction. Carrying both fields implies they could diverge in some scenario, but no such scenario is documented or tested.

**Fix:** Remove one field (prefer `Revision` for KV semantic alignment) and update callers. Or add a comment explicitly explaining that for KV-backed streams the two are always equal and both are kept for semantic clarity. The subscribe test at `subscribe_test.go:71` asserts `evt.Sequence == rev` which is also the KV revision — confirming no divergence is expected.

---

### [N-003] `ctx` is ignored in `Connect` — the comment "reserved for future use" is unbounded

**File:** `internal/substrate/conn.go:74`

```go
_ = ctx // reserved for future use (e.g., bounded connect via JS API ping)
```

`nats.Connect` is a blocking call with no context support in the underlying nats.go API. The ignored `ctx` means a slow NATS connect (DNS resolution timeout, firewall drop) cannot be cancelled by the caller. This is a known nats.go limitation, but labelling it "reserved for future use" without a timeframe leaves the footgun invisible to callers who assume `Connect` respects their deadline.

**Fix:** Add a comment explicitly noting that `nats.Connect` does not accept a context and that callers should set `ConnectOpts.MaxReconnects` and `ReconnectWait` to bound the retry loop rather than relying on context cancellation.

---

### [N-004] `_ = streamName` and `_ = durableName` in `runKVSubscription` suppress "reserved for diagnostic logging" fields but the suppression comment does not explain why they are passed at all

**File:** `internal/substrate/subscribe.go:170-171`

Both `streamName` and `durableName` are passed as parameters to `runKVSubscription` but immediately discarded. The logger calls within the function already have access to `durableName` via the parameter. `streamName` is used nowhere. These are dead parameters.

**Fix:** Remove `streamName` from the `runKVSubscription` signature (it can be reconstructed from `bucket` as `"KV_" + bucket` if ever needed for logging). `durableName` is legitimately needed for the logger calls — remove the `_ = durableName` line.

---

### [N-005] `publishAtomicBatch` returns `fmt.Errorf("unreachable")` as a final fallback — dead code that will confuse future readers

**File:** `internal/substrate/batch.go:265`

The loop `for i, m := range messages` with a `len(messages) > 0` guard (checked at line 239) means the loop body always executes the last-message branch and returns. The final `return nil, fmt.Errorf("unreachable")` will never be reached. The Go compiler does not flag this as dead code.

**Fix:** Replace with `panic("substrate: publishAtomicBatch: unreachable")` to make intent explicit and ensure it surfaces immediately if the logic ever changes, or remove it entirely and rely on the compiler's exhaustive return check (the function already returns on every path through the loop).

---

## History Comments

All five occurrences are in `batch.go`. They are Story-reference comments that record rationale from the Story 1.1 spike rather than documenting current state.

| File:Line | Comment excerpt | Severity | Suggested action |
|---|---|---|---|
| `batch.go:60-62` | `// Story 1.1 spike findings (the nats.go client does not expose a high-level PublishBatch).` | Nit | Remove story reference; keep the factual content about nats.go API gap |
| `batch.go:71-72` | `// (Story 1.1 spike documented this); pass one bucket per call.` | Nit | Remove story reference; state constraint only |
| `batch.go:156` | `// publish to JetStream (Story 1.8 step 9).` | Nit | Remove story reference |
| `batch.go:187-188` | `// Story 1.1 spike findings (Behavioral Test 3b documented this).` | Nit | Remove story reference; keep the "all-or-nothing" fact |
| `batch.go:231-232` | `// from the Story 1.1 spike. All-but-last messages are fire-and-forget` | Nit | Remove story reference; keep the protocol description |

---

## Adversarial Coverage Notes

**NFR-SC2 audit — keys embed no cell identity:**
All key construction paths in `keys.go` produce keys of the form `vtx.<type>.<id>`, `vtx.<type>.<id>.<localName>`, or `lnk.<type1>.<id1>.<linkName>.<type2>.<id2>`. No segment encodes a cell, node, region, or deployment identifier. `VertexKey`, `AspectKey`, `LinkKey`, `ParseVertexKey`, `ParseAspectKey`, `ParseLinkKey`, and `ClassifyKey` all operate exclusively on type, id, and localName segments. NFR-SC2 is satisfied.

**Goroutine safety of `Conn`:**
The `bucket()` cache uses `sync.Mutex`. The buckets map is only read/written under the mutex (except for the TOCTOU race documented in F-002). `Close()` calls `nc.Close()` which is goroutine-safe per nats.go's contract. `KV*` methods are goroutine-safe given the mutex. `AtomicBatch` and `PublishBatch` use only `c.nc` directly (no shared mutable state beyond the connection itself); nats.go's `*Conn` is goroutine-safe. No additional race conditions found beyond F-002.

**`Create` with `expectedRevision=0` semantics:**
`KVCreate` delegates to `kv.Create()` which NATS maps to a `Nats-Expected-Last-Subject-Sequence: 0` condition — create-if-absent. This is correctly documented in `BatchOp`. The `CreateOnly: true` field on `BatchOp` also sets `Nats-Expected-Last-Subject-Sequence: 0`. The two spellings are equivalent at the wire level, which is correctly documented.

**Get on tombstoned key:**
`KVGet` after `KVDelete` correctly returns `ErrKeyNotFound` (tested in `substrate_test.go:134-137`). The soft-delete path (envelope with `isDeleted=true`) is the gap documented in F-004.

**Empty value bytes:**
`KVPut` with `value = nil` or `value = []byte{}` is valid at the NATS level; an empty body is a NATS KV delete marker (if sent via certain paths). `KVPut` delegates directly to `kv.Put` without a length check. Sending `nil` value via `KVPut` will write an empty message, which NATS JetStream KV will interpret as a delete marker depending on the message operation header. Callers who mean "write empty JSON object" should pass `[]byte("{}")`. This is a documentation gap rather than a code defect, but worth noting.

**Value exceeding NATS message size limit:**
No size validation occurs before `kv.Put`, `kv.Create`, `kv.Update`, or `publishAtomicBatch`. NATS will reject oversized messages at the server. The error is wrapped through the generic "substrate: KV put …" path. No explicit `ErrMessageTooLarge` sentinel exists. This is acceptable for a v1 surface — oversized messages are an application-level concern — but worth documenting.

**Key with NATS-illegal characters (`*`, `>`, whitespace):**
`VertexKey`, `AspectKey`, and `LinkKey` validate all segments through `mustValidateType`, `mustValidateNanoID`, and `mustValidateLocalName`. These validators use explicit character range checks that exclude `.`, `*`, `>`, whitespace, and all non-alphanumeric characters. A key produced by any of these builders cannot contain NATS-illegal characters. Keys passed directly to `KVPut` / `KVGet` without going through a builder are not validated — but this is consistent with the stated design principle ("keys are constructed from typed Go values … never from untrusted input").

**AtomicBatch with zero ops:**
`AtomicBatch([]BatchOp{}, timeout)` returns an error immediately (line 79: `"empty op list"`). Correct.

**AtomicBatch with one op:**
One-op batch: `msgs` has length 1. The loop body executes `i = 0`, which satisfies `i < len(messages)-1` as false (0 < 0 is false), so the last-message branch runs immediately. `Nats-Batch-Commit: 1` is set and `RequestMsg` is called. Semantically correct — a single-message atomic batch is valid NATS protocol.

**NanoID generation under high parallelism:**
`newNanoIDN` uses `crypto/rand.Read` which is goroutine-safe per the `crypto/rand` package docs. The local `buf` and `id` slices are stack-allocated per call. No shared mutable state. Safe under any degree of parallelism.

**Subscribe watch reconnect after disconnect:**
`SubscribeKVChanges` creates a durable JetStream consumer with `CreateOrUpdateConsumer`. On NATS reconnection, the nats.go library re-establishes the underlying connection and the `Messages()` iterator will resume from the last-acked sequence (the durable consumer's ack floor is server-side). The `runKVSubscription` goroutine's `mc.Next()` will return an error on disconnect; the loop then logs a warning and returns, closing the channel. Callers who observe a closed channel must reinvoke `SubscribeKVChanges`. This is the documented behaviour ("Unrecoverable subscription errors … close the channel — callers should treat channel close as the signal that the subscription is gone."). No auto-reconnect within the goroutine itself — callers must handle reconnection at their level. This is architecturally intentional but not explicitly called out in the doc comment as a reconnect responsibility handed to callers.

**Concurrent Put on same key with same revision:**
For `KVUpdate` with the same `expectedRevision`, NATS atomically applies the first writer and rejects the second with a revision conflict. The second caller receives `ErrRevisionConflict`. This is the expected CAS (compare-and-swap) semantics.

**Key round-trip invariant (`Parse(Construct(x)) == x`):**
Verified by inspection: `VertexKey(t, id)` → `ParseVertexKey` → `(t, id, true)`. `AspectKey(vtxKey, ln)` → `ParseAspectKey` → `(vtxKey, t, id, ln, true)`. `LinkKey(t1, id1, ln, t2, id2)` → `ParseLinkKey` → `(t1, id1, ln, t2, id2, true)`. Round-trip holds cleanly because all builders use `.`-joined segments and all parsers use `strings.Split(key, ".")`.

**Typed error wrapping with `errors.Is`:**
`ErrKeyNotFound` and `ErrRevisionConflict` are wrapped via `fmt.Errorf("%w: ...", ErrXxx, ...)`. `errors.Is` traverses the `%w` chain, so `errors.Is(err, ErrKeyNotFound)` works correctly at all call sites. `ErrAtomicBatchRejected` is similarly wrapped. All three sentinels support `errors.Is` semantics as documented. Verified against the test in `substrate_test.go:81-84` (ErrKeyNotFound), `substrate_test.go:95-97` (ErrRevisionConflict), and `substrate_test.go:193-196` (ErrAtomicBatchRejected).

**Revision monotonicity:**
`KVUpdate` returns the new revision after a successful CAS. Tests at `substrate_test.go:118-120` and `substrate_test.go:127-129` assert `rev2 > rev1` and `rev3 > rev2`. NATS JetStream guarantees monotonically increasing per-stream sequence numbers, which serve as KV revisions. No gap found.

**Envelope validation coverage:**
`NewDocumentEnvelope` / `NewDocumentEnvelopeAt` set all required fields per Contract #1 §1.3 (`class`, `isDeleted`, `createdAt`, `createdBy`, `createdByOp`, `lastModifiedAt`, `lastModifiedBy`, `lastModifiedByOp`). The `key` field is left empty by the constructors and must be populated by the caller (documented in the `NewDocumentEnvelope` comment). The test at `envelope_test.go:54-64` enumerates all 10 required fields and asserts their presence in JSON. The only gap is that callers who forget to set `Key` produce an envelope with `"key": ""` — no panic guard (see F-008 for the general validation gap on constructor inputs).
