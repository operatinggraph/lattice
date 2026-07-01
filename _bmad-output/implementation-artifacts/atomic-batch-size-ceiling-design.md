# Design — Atomic-batch size ceiling (typed `BatchTooLarge` rejection + enforced bound)

**Status: 📐 awaiting-Andrew (ratification).**
**Author: Winston (Designer fire, 2026-07-01)**
**Backlog row:** `planning-artifacts/backlog/lattice.md` → *Component maintenance* → "[Core] Atomic-batch size ceiling undocumented + unenforced".
**Grounded demand:** Surveyor-filed 2026-07-01 (Core survey). Closes the long-standing **P3 deferred-capability obligation** stated verbatim in `lattice-architecture.md:210`, `:300`, `:1028`: *"NATS has byte/op limits per batch. The maximum mutation size a single operation can produce must be determined and documented."* The Story 1.1 batch spike (`internal/spike/nats-batch/README.md`) validated batch *behavior* (TTL-in-batch, revision atomicity, multi-subject, TTL markers) but **explicitly never measured the size/max-keys ceiling** — that dimension is still open, and today an over-limit op surfaces a raw NATS protocol error at step 8, not a typed Processor rejection.

---

## For Andrew (one-look ratification)

**What it does (two lines).** Determines, *enforces*, and documents the maximum a single operation's atomic batch may be, so an over-limit op fails **fail-closed with a typed `BatchTooLarge` operation-reply** (with structured `details`) instead of leaking a raw substrate/NATS error at step 8. Two bounds, both grounded in **NATS 2.14** (our pin): **message count ≤ 1000 per batch** (ADR-50 — the server abandons the batch with error `10199` above this) and **per-value bytes ≤ the connection's negotiated `max_payload`** (default 1 MiB; unset in `deploy/nats-server.conf`).

**The one design decision (no architectural fork).** *Where* the bound lives and *what a legitimate over-limit op does.* I put a **mechanical pre-flight guard in the substrate** (`AtomicBatch`/`PublishBatch` reject with typed sentinels *before* any NATS publish, so no raw protocol error ever escapes) and **map those sentinels to a new `BatchTooLarge` reply code at the Processor** — one coupled change, not two half-features. For a *legitimate* business op that genuinely needs >998 mutations or a >1 MiB value, the architecture (`:210`) floats a "saga/compensation pattern." **My recommendation: v1 = fail-closed typed rejection only; the saga/chunked-decomposition path is deferred (dead scaffolding until a real consumer needs it).** No live script today approaches either bound (Core-KV values are lean JSON — large blobs already live off-graph in the Object Store, Contract #7 §7.2 — and the largest cascade in the codebase is nowhere near 1000 keys). Building saga machinery before a driver exists is exactly the over-engineering the "simplest extension" rule warns against.

**Frozen-contract change (staged UNCOMMITTED in `main`).** Two files, both *additive* (no existing code/section changes meaning):
- `docs/contracts/02-operation-envelope.md` **§2.6** — one new row in the closed error-code enumeration: `BatchTooLarge` (step 8). The enumeration is explicitly declared extensible ("*Phase 2+ may add codes; existing codes are immutable*"), so this is a sanctioned addition, not a break.
- `docs/contracts/03-mutation-batch-event-list.md` — a new **§3.9.1 "Atomic-batch size ceiling"** under the existing §3.9 (Substrate Batch Helpers), stating the two bounds, the per-op mutation budget (≤ 998), and the typed rejection.

**These edits are in different files/sections from the three other in-flight uncommitted edits** — the `delete`-verb design touches `03` §3.2/§3.3/§3.8 (I add a *new* §3.9.1, no overlap), and the script-read-posture design touches `02` §2.2/§2.5 (I add a *new* §2.6 row, no overlap). All three stay unstaged; my commit carries **only** the design doc + the board row. Affected consumers: the Processor commit path (a new typed branch), the substrate batch helpers, and package authors (a documented budget). The diff is the proposal — review it.

**No architectural fork** (Gateway / D1 read-path-auth / Vault / multi-cell / HA-NATS untouched). **No auth-surface change** — the guard is a mechanical size check downstream of step-3 auth and step-6 write-scope; it grants nothing and reveals nothing.

---

## 1. Problem and intent

### 1.1 The bound exists in NATS but is unenforced in Lattice

Every write in Lattice commits through **one** primitive: `substrate.Conn.AtomicBatch` (`internal/substrate/batch.go`), driven by the Processor's step-8 committer (`internal/processor/step8_commit.go`). The committer builds one `BatchOp` per mutation, appends the idempotency tracker (`CreateOnly`, 24h TTL) and — when the op emits events — the transactional-outbox aspect, then submits the whole slice as a single NATS JetStream atomic batch.

NATS bounds that batch in two independent ways (both confirmed against our pin, **NATS 2.14**, `go.mod` `nats-server/v2 v2.14.0`; see `docs/vendors.md`):

1. **Message-count ceiling — 1000 messages per batch.** ADR-50 (*JetStream Batch Publishing*, tagged `2.12, 2.14`): *"Each batch can have maximum 1000 messages."* Exceeding it → the server **abandons the batch** and returns error **`10199`** ("Batch publish sequence exceeds server limit (default 1000)"). This is not configurable.
2. **Per-message byte ceiling — `max_payload`.** Each atomic-batch member is an ordinary NATS message, subject to the server's `max_payload` (NATS default **1 MiB = 1,048,576 bytes**). `deploy/nats-server.conf` does **not** override it, so the dev/prod floor is the 1 MiB default. The `nats.go` client rejects an over-`max_payload` publish **client-side** (`nats.ErrMaxPayload`) before it hits the wire.

Neither bound is checked anywhere in Lattice. `AtomicBatch` validates only *bucket uniformity* and *non-empty keys* (`batch.go:97-107`); there is no count check and no per-value size check. So:

- An op whose Starlark produces >999 mutations (a large cascade — e.g. "tombstone this hub and every one of its N links") builds a >1000-message batch → NATS returns `10199` → the committer's `errors.Is(batchErr, substrate.ErrAtomicBatchRejected)` branch catches it, but there is **no `ConflictError`** (it is not a revision conflict), so it falls through to the generic redelivery path (`commit_path.go:467`) and **JetStream redelivers forever** — a deterministic op that can never succeed, hot-looping on the backoff floor.
- An op whose Starlark emits a single value >1 MiB (an aspect stuffed with a large embedded document) → `nc.PublishMsg` returns `nats.ErrMaxPayload` → wrapped as `ErrAtomicBatchRejected` → same fall-through → same redelivery-forever.

Either way the operator sees a raw substrate error and an op stuck in redelivery, never a clean `rejected` reply. That is the filed bug.

### 1.2 Why this is the honest "determined + documented" resolution of P3's deferred item

`lattice-architecture.md:300` scoped the Stream-0 spike to validate *"size ceiling, revision condition semantics, maximum keys per batch."* The spike (Story 1.1) landed a clean **GO** on revision semantics and multi-subject batching but its four behavioral tests (`internal/spike/nats-batch/README.md`) **never exercised the count or byte ceiling** — the "maximum keys per batch" question was left for "determined and documented" later. `:1028` restates the obligation ("*must validate that NATS atomic batch supports the mutation sizes implied by 10–100 ops/sec at up to 100K keys*"). This design discharges it: the bound is now **determined** (1000 msgs / `max_payload` bytes, cited to ADR-50 + the pin), **enforced** (fail-closed, typed), and **documented** (Contract #3 §3.9.1).

### 1.3 Intent

Add a **mechanical, fail-closed size guard** at the substrate batch primitive and a **typed `BatchTooLarge` operation-reply** at the Processor, so that:

- No raw NATS size/payload error ever escapes the substrate.
- An over-limit op **terminates** (no redelivery — a redelivery is deterministically hopeless) with an actionable reply naming the bound it broke, the limit, and the actual value.
- The per-op mutation budget is documented for package authors.

Prefer the **simplest extension** that closes the bug; defer saga/decomposition until a real over-limit consumer exists.

---

## 2. The shape

This is a Core/substrate + Processor change on the **write path** only. No new vertices, aspects, links, lenses, ops, or orchestration. P1/P2/P5 and Contract #1 key-shapes are untouched (the guard inspects the batch the committer already built).

### 2.1 Substrate layer — the mechanical guard (the authority)

Add to `internal/substrate` (co-located with `batch.go`):

```go
// MaxBatchMessages is the maximum number of messages a single JetStream
// atomic batch may contain, per NATS 2.14 (ADR-50: "Each batch can have
// maximum 1000 messages"; the server abandons an over-limit batch with
// err_code 10199). Not server-configurable. The Processor's batch =
// business mutations + the idempotency tracker + (optional) the outbox
// aspect, so a single operation's business-mutation budget is
// MaxBatchMessages - 2 = 998 (see Contract #3 §3.9.1).
const MaxBatchMessages = 1000

// ValueHeadroomBytes is reserved below the negotiated max_payload for the
// message's batch/revision/TTL headers and the Processor's commit-time
// provenance injection (createdAt/By/ByOp, lastModified*). The per-value
// ceiling is derived from the LIVE negotiated max_payload rather than a
// hardcoded 1 MiB, so a production max_payload override is honored
// automatically.
const ValueHeadroomBytes = 4 * 1024

var (
    // ErrBatchTooLarge is returned by AtomicBatch/PublishBatch when the op
    // count exceeds MaxBatchMessages. NOT wrapped in ErrAtomicBatchRejected
    // (it is a pre-flight guard, never a NATS rejection).
    ErrBatchTooLarge = errors.New("substrate: atomic batch exceeds message-count ceiling")
    // ErrValueTooLarge is returned when a single op's value exceeds the
    // per-message payload ceiling (negotiated max_payload - ValueHeadroomBytes).
    ErrValueTooLarge = errors.New("substrate: batch op value exceeds payload ceiling")
)
```

`AtomicBatch` (and `PublishBatch`) gain a pre-flight guard *before* the NanoID/message-build loop:

```go
if len(ops) > MaxBatchMessages {
    return nil, fmt.Errorf("%w: %d messages > %d",
        ErrBatchTooLarge, len(ops), MaxBatchMessages)
}
limit := int(c.nc.MaxPayload()) - ValueHeadroomBytes
for i, op := range ops {
    if !op.Delete && len(op.Value) > limit {
        return nil, fmt.Errorf("%w: op[%d] key=%q value=%d bytes > %d",
            ErrValueTooLarge, i, op.Key, len(op.Value), limit)
    }
}
```

`c.nc.MaxPayload()` is the server-negotiated payload ceiling (`nats.go` `Conn.MaxPayload() int64`, populated from the server `INFO` at connect). `Delete` ops carry no body and are skipped. The guard is a straight extension of the existing validation loop in `AtomicBatch` (`batch.go:97-107`) — the *simplest place* the bound belongs, because it is a substrate-level truth about the substrate's own limits and it protects **both** batch callers (the Processor commit and the outbox publisher) from one spot.

**Write-guard precision.** This guard is **not** an OCC/CAS condition and does **not** touch the revision-conditioned `Nats-Expected-Last-Subject-Sequence` headers — those still guard each key exactly as before (create→0, update/tombstone/delete→hydrated revision). The size guard is a pure structural pre-check that either lets the whole batch through unchanged or rejects it before publish; it never partially commits and never alters the atomicity or revision semantics the Story 1.1 spike validated.

### 2.2 Processor layer — the typed reply

`step8_commit.go`'s `Commit` already wraps a NATS-rejected batch in `ConflictError` (for `ErrAtomicBatchRejected`). Add a sibling typed error and branch. Because the substrate guard returns `ErrBatchTooLarge`/`ErrValueTooLarge` **un-wrapped** (they are pre-flight, not NATS rejections), they bypass the existing `ErrAtomicBatchRejected` branch — I add an explicit mapping:

In `step8_commit.go`, after the `c.Conn.AtomicBatch` call, before the `ErrAtomicBatchRejected` check:

```go
if errors.Is(batchErr, substrate.ErrBatchTooLarge) || errors.Is(batchErr, substrate.ErrValueTooLarge) {
    return CommitAck{}, &BatchTooLargeError{
        Reason:             batchTooLargeReason(batchErr),  // "mutationCount" | "valueSize"
        Limit:              /* the crossed limit */,
        Actual:             /* len(ops) or the offending value size */,
        Key:                /* offending key, valueSize only */,
        OperationRequestID: rid,
        Cause:              batchErr,
    }
}
```

with a new typed error mirroring `ConflictError`/`ProtectedKeyError`:

```go
type BatchTooLargeError struct {
    Reason             string // "mutationCount" | "valueSize"
    Limit              int
    Actual             int
    Key                string // valueSize only
    OperationRequestID string
    Cause              error
}
```

In `commit_path.go`, add a branch **before** the `ErrAtomicBatchRejected` block (parallel to the `ProtectedKeyError` branch at `commit_path.go:402`), terminating with no redelivery — a redelivery of a deterministic op produces the identical over-limit batch and can never succeed:

```go
var btlErr *BatchTooLargeError
if errors.As(err, &btlErr) {
    cp.deps.Metrics.OpsRejected.Add(1)
    cp.deps.Logger.Info("step 8: batch-too-large rejection",
        "requestId", env.RequestID, "reason", btlErr.Reason,
        "limit", btlErr.Limit, "actual", btlErr.Actual, "key", btlErr.Key)
    cp.replyTo(msg, BuildRejectedReply(env.RequestID, ErrCodeBatchTooLarge,
        btlErr.Error(), map[string]any{
            "reason": btlErr.Reason, "limit": btlErr.Limit,
            "actual": btlErr.Actual, "key": btlErr.Key,
        }))
    return OutcomeRejected, substrate.Term
}
```

with the new code constant in `envelope.go`:

```go
// ErrCodeBatchTooLarge is the step-8 rejection when a single operation's
// atomic batch exceeds the message-count ceiling (>998 business mutations)
// or a value exceeds the payload ceiling (Contract #2 §2.6 / #3 §3.9.1).
// Terminal — a redelivery reproduces the identical over-limit batch.
ErrCodeBatchTooLarge ErrorCode = "BatchTooLarge"
```

**One code, structured `details`.** Rather than two codes (count vs. bytes) I use one `BatchTooLarge` with `details.reason ∈ {mutationCount, valueSize}`, matching the contract's established "each code paired with structured details appropriate to the failure mode" convention (mirrors how `RevisionConflict` carries `conflictingKey` and `ProtectedKey` carries `key/root/op`).

### 2.3 Read path / write path

**Read path (P5):** untouched — no application reads anything here.
**Write path (P2):** untouched in principle — the Processor is still the sole Core-KV writer via the same atomic batch; the guard only decides *whether* that batch is admissible. The `directOp` and outbox publish paths inherit the same substrate guard (`PublishBatch`).

### 2.4 The outbox asymmetry (decided, not deferred)

`PublishBatch` (the transactional-outbox event publisher, `batch.go:230`) gets the **same** guard, but its failure mode differs and I resolve it explicitly: outbox publish is **post-commit** — the op already committed and the faithful `EventList` is already durable in the `vtx.op.<id>.events` outbox aspect. An oversized event batch there is not an operation-reply concern (the client already got its `accepted`); it is an **outbox-consumer error, logged + retried**. In practice it is unreachable: the event count is bounded by the committed mutation count (which already passed the ≤1000 guard), and event payloads are lean (Contract-#1 convention: events carry vertex-ID references, not embedded context — `lattice-architecture.md:199`). The guard on `PublishBatch` is therefore a **backstop** that keeps the substrate self-consistent, not a new failure surface. No new reply code is needed for it.

---

## 3. Contract surface (change-vs-build-to)

| Contract | Section | Change or build-to | Detail |
|---|---|---|---|
| **#2 Operation Envelope** | **§2.6 Error Code Enumeration** | **CHANGE (additive)** | One new row: `BatchTooLarge` \| "A single operation's atomic batch exceeded the message-count ceiling (>998 mutations) or a value exceeded the payload ceiling" \| Step 8. Sanctioned by §2.6's own "extensible … existing codes immutable" clause. |
| **#3 Mutation Batch / Event List** | **new §3.9.1 (under §3.9)** | **CHANGE (additive)** | "Atomic-batch size ceiling": the two bounds (1000 msgs; `max_payload` bytes), the ≤998 business-mutation budget, the `BatchTooLarge` typed rejection, and the note that a legitimate over-budget op must be decomposed by the author (saga deferred). |
| **#3** | §3.9 (Substrate Batch Helpers) | build-to | The guard extends the existing helper; no change to the committed-revision derivation. |
| **#1 Key-shapes** | — | build-to | Untouched. |
| **#4 §4.3** (tracker TTL) | — | build-to | The tracker is one of the counted messages; the ≤998 budget already reserves for it. |
| `lattice-architecture.md:210/300/1028` | Deferred-capability note | **resolved-by-reference** | Planning-owned (do not edit while designing). The design *discharges* the obligation; the planning lead / Andrew may mark it resolved at ratification. |

Both contract edits are staged **UNCOMMITTED** in `main` (the diff is the proposal). They do not overlap the three other in-flight uncommitted edits (delete-verb `03` §3.2/§3.3/§3.8; script-read-posture `02` §2.2/§2.5). My commit stages **only** the design doc + the board.

---

## 4. Migration + test strategy

**Migration:** none. No data shape, DDL, lens, or bucket changes. The guard is a pure pre-flight check on the batch the committer already assembles; every op that commits today (all well under both bounds) is unaffected. No bootstrap version bump.

**Backward-compatibility:** the new reply code is additive; existing clients that switch on the closed enumeration treat an unknown code as a generic rejection (the enumeration was declared extensible from the start). No existing op changes outcome.

**Tests:**
- **Substrate unit (`batch_test.go`):** (a) a 1001-op batch → `ErrBatchTooLarge`, nothing published (assert via a fake/embedded server that no messages landed); (b) a single op with `len(Value) > MaxPayload()-headroom` → `ErrValueTooLarge`; (c) a 1000-op batch and a value exactly at the ceiling → **pass** (boundary); (d) `Delete` ops (no body) skip the value check; (e) `PublishBatch` mirrors (a)/(b).
- **Processor step-8 (`step8_commit_test.go` / `commit_occ_test.go` neighbor):** a `ScriptResult` with 1001 mutations → `Commit` returns `*BatchTooLargeError{Reason:"mutationCount", Limit:1000, Actual:1001}`; a mutation whose marshaled value exceeds the ceiling → `Reason:"valueSize"`, `Key` set.
- **Commit-path (`commit_path` integration):** an op producing an over-count batch → a `rejected` reply with `code=BatchTooLarge`, `details.reason="mutationCount"`, and **outcome = Term** (assert no redelivery — the anti-hot-loop guarantee). This is the regression test for the filed "raw error + stuck redelivery" bug.
- **Conformance (`conformance_test.go`):** add `BatchTooLarge` to the reply-code enumeration assertion so the closed set stays in sync with the contract.
- **Gates:** `go build ./...`, `make vet`, `golangci-lint run ./...`, `go test ./internal/substrate/... ./internal/processor/...`. No Gate-2/Gate-3 change (no bypass/capability surface touched).

**A note on the boundary test (c):** `nc.MaxPayload()` in the embedded-server fixture returns the test server's configured `max_payload`; the test computes the ceiling from it rather than hardcoding 1 MiB, so it stays correct if the fixture's `max_payload` differs from prod.

---

## 5. Risks + alternatives

**R1 — Is 1000 the right number, and is it stable across NATS versions?** It is the documented NATS 2.14 server limit (ADR-50), matched to our pin; a future NATS bump that changes it would change the constant (one line, cited). The constant is conservative-by-construction: it mirrors the server's own hard limit, so the guard can never be *looser* than NATS — at worst a future NATS raises the limit and our guard rejects a batch the server would now accept, which is a safe (fail-closed) direction and a trivial follow-up. **Accepted.**

**R2 — Deriving the byte ceiling from `nc.MaxPayload()` vs. a fixed 1 MiB constant.** Deriving from the live negotiated value is strictly more robust: it honors a production `max_payload` override automatically and keeps the embedded-server tests correct. The 4 KiB headroom covers batch/revision/TTL headers + provenance injection; even a 0-headroom off-by-a-little would only make the guard marginally stricter than the server (fail-closed). **Chosen: derive from `MaxPayload()`.**

**R3 — Should the guard live at step-6 (validate) instead of step-8 (commit)?** A step-6 pre-check could reject earlier with a `SchemaViolation`-adjacent reply, but the mutation *values* (with provenance injected) are not finalized until step-8's `buildMutationValue`, so a byte check at step-6 would be on the wrong bytes. The count is knowable at step-6 but splitting the two checks across two steps is worse than one authority. **Chosen: single substrate guard, mapped at step-8** — the values are final and both bounds are checked in one place.

**R4 — Dead-scaffolding check on the saga path.** The architecture floats "saga/compensation" for a legitimate over-limit op. **Applied the test and rejected building it now:** no live consumer produces anything near 998 mutations or a 1 MiB value (blobs are off-graph; cascades are small). A saga/chunked-op decomposition is a genuine future increment but it needs a real driver to shape it (chunk-boundary semantics, cross-chunk atomicity, partial-failure compensation are all consumer-specific). Building it speculatively is the exact anti-pattern the "simplest extension" rule forbids. **Deferred (Fire 2, gated on a driver).**

**Alternative A — reuse `RevisionConflict`/`InternalError`.** Rejected: semantically wrong (it is neither a conflict nor an internal failure) and it leaves the op stuck in redelivery (the filed bug). A distinct terminal code is the point.

**Alternative B — document-only, no enforcement.** Rejected: the architecture says "determined **and** documented," and the filed symptom is the *unenforced* raw-error path, not a missing paragraph.

**Alternative C — enforce byte-only or count-only.** Rejected: both bounds are real and independently reachable; the count check is O(1) and the byte check is O(N) over already-built values — both cheap.

---

## 6. Fire-by-fire decomposition (for the Steward)

**Fire 1 — the ceiling (one coupled, shippable unit).** Substrate + Processor + contract, built in this internal order, shipped together (the guard is inert without the typed reply, and the reply needs the guard — this is coupled work, one fire):
1. Substrate: `MaxBatchMessages`, `ValueHeadroomBytes`, `ErrBatchTooLarge`, `ErrValueTooLarge`; pre-flight guard in `AtomicBatch` + `PublishBatch`; substrate unit tests (boundary + over-limit + Delete-skip).
2. Processor: `BatchTooLargeError` type + step-8 mapping; `ErrCodeBatchTooLarge` const; `commit_path.go` terminal branch; step-8 + commit-path tests (incl. the **no-redelivery** regression assertion).
3. Contract commits (ratified): `02` §2.6 row + `03` §3.9.1 — committed **with** Fire 1 per the ratified-contract-commit rule (a ratified edit ships with its build, not held).
4. Conformance test sync.
Independently green; closes the filed bug end-to-end (typed reply, no stuck redelivery, documented budget).

**Fire 2 — saga / chunked-op decomposition (DEFERRED — do not build without a driver).** For a legitimate business op that must exceed 998 mutations or 1 MiB: an author-facing decomposition pattern (chunk-boundary + cross-chunk compensation). **Dead scaffolding today** — listed so the obligation is visible, gated on a concrete >998-mutation / >1 MiB consumer appearing (e.g. a future bulk-import or a very-high-degree cascade). Revive on demand.

---

## 7. Adversarial pre-build gate (discharged)

I ran a self-adversarial pass (edge-case + acceptance lenses) on this design before calling it build-ready; findings folded in above:
- **Edge — the outbox path.** Initially unaddressed; §2.4 now decides it explicitly (same guard, post-commit logged-retry, not a reply code).
- **Edge — the byte check on `Delete` ops.** A `delete` (once that verb lands) carries no body; the guard skips `op.Delete` so a valid delete-heavy reclaim batch is never falsely rejected on size. Interaction with the in-flight delete-verb design is benign (both are additive; the count guard still bounds a delete-heavy batch at 1000, which is correct).
- **Acceptance — "determined + documented."** The two bounds are cited to ADR-50 + the `max_payload` default and matched to the NATS 2.14 pin (`docs/vendors.md`); §3.9.1 documents them. Obligation `:210/:300/:1028` discharged.
- **Acceptance — terminal vs. retry.** The rejection is `substrate.Term` (no redelivery), asserted by a regression test — this is the specific fix for the "stuck in redelivery" half of the filed symptom, not just the raw-error half.
- **Correctness — guard cannot be looser than NATS.** Both bounds mirror the server's own limits (count = the server's 1000; bytes = the negotiated `max_payload`), so the guard is always ≤ what NATS enforces (fail-closed). Verified against ADR-50 and the `nats.go` client-side `max_payload` check.

No open questions remain; every decision above is resolved.
