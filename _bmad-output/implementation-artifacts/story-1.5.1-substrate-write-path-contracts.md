# Story 1.5.1 — Substrate write-path contracts

**Phase 1.5 (Hardening Block) · Wave A · Foundational — sequence first**
**Tier:** Opus
**Author:** Winston · **Date:** 2026-05-28
**Sources:** Core KV CR **F-001** (`phase-1.5-cr-corekv.md`), Processor CR **P2-001** (`phase-1.5-cr-processor.md`)

---

## 🔴 MANDATORY OPERATING RULES (READ FIRST)

1. **Work in the repo root** `/Users/andrewsolgan/Documents/GitHub/Lattice`. Do NOT create or operate in `.claude/worktrees/*`.
2. **Do NOT commit or push.** Leave changes in the working tree. Winston reviews, commits, and watches CI.
3. **Do NOT edit planning artifacts** (`_bmad-output/planning-artifacts/{data-contracts.md,epics.md,lattice-architecture.md,MORPH-DEVIATIONS.md}`). If a contract gap surfaces, append a note to `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` and continue with a different deliverable. New/updated docs land in `/docs` (here: `docs/contracts/`), never planning-artifacts.
4. **No history comments.** Do NOT write `// Story 1.5.1`, `// Replaces`, `// Previously …`, `// was 30s` etc. Git blame is the record. Comments describe current behavior only.
5. **Halt and escalate** if you find yourself in any of these loops:
   - Re-attempting the same operation after 3+ failures
   - Making changes you immediately revert
   - Re-reading the same files looking for an answer that isn't there
   - Cycling between two failed approaches
   - An unresolved test failure after 2 genuine debug attempts
   Append the blocker to `CONTRACT-AMENDMENT-REQUEST.md` and stop. Token budget is tracked, NOT enforced — do not halt on token count.
6. **Append a closing summary** to the bottom of THIS file when done: deliverables checklist (checked against §6), files touched, verification-gate results (paste the tail of each), and any deviations/CARs.

---

## 1. Goal

Harden the substrate write-path contract so it is cancellation-aware and so committed revisions flow back to callers:

- `AtomicBatch` and `PublishBatch` accept `context.Context` (drop the bare `timeout time.Duration` parameter). Upstream cancellation/deadline propagates end-to-end.
- `AtomicBatch` returns **per-key committed revisions** in `BatchAck`.
- The Processor commit path wires those revisions into `OperationReply.Revisions` (today always nil — the schema promise is unfulfilled).
- Bootstrap drops its hardcoded 30s timeout in favor of the caller-supplied context deadline.

**Why this is foundational:** it unblocks the Story 5.3 compensation contract (compensating-op templates consume committed revisions as inputs) and removes the substrate-wide inability to honor a SIGTERM/`context.WithDeadline` during a high-load batch commit.

---

## 2. Required context — read these ONLY

- `internal/substrate/batch.go` — `AtomicBatch`, `PublishBatch`, `publishAtomicBatch`, `BatchAck`, `PublishBatchAck`, `pubAckResponse` (the `seq` + `count` fields are the key to revision derivation).
- `internal/processor/reply.go` — reply builders.
- `internal/processor/envelope.go` — `OperationReply` struct (the `Revisions map[string]uint64` field already exists, doc-commented, always nil).
- `internal/processor/step8_commit.go` — `CommitterImpl.Commit` (the production AtomicBatch call site, has `ctx` + `c.Timeout` in scope) and `CommitAck`.
- `internal/processor/commit_path.go` ~lines 290–330 — where `BuildAcceptedReplyWithDetail` is called.
- `internal/processor/step9_publish.go` ~line 141 — `PublishBatch` call site (`p.Timeout` in scope).
- `internal/processor/steps_4_10_stub.go` ~line 127 — stub Committer AtomicBatch call (5s).
- `internal/bootstrap/primordial.go` ~line 233 — the 30s hardcode inside `SeedPrimordial(ctx)`.
- `internal/pkgmgr/installer.go` lines 189, 409 — `Install`/`Uninstall` AtomicBatch calls (both have `ctx`; `DefaultBatchTimeout` = 30s).
- `packages/identity-domain/seed.go` ~line 101 — seed AtomicBatch call (15s).
- Test call sites to migrate: `internal/substrate/substrate_test.go` (154, 190, 218), `internal/substrate/publish_batch_test.go` (41, 85, 102), `packages/identity-domain/state_machine_test.go` (~265).
- `docs/contracts/03-mutation-batch-event-list.md` — the batch/reply contract doc (update to reflect both changes).

Do **not** read large planning artifacts.

---

## 3. Design decisions (LOCKED by Winston)

### 3.1 Context replaces timeout

New signatures:

```go
func (c *Conn) AtomicBatch(ctx context.Context, ops []BatchOp) (*BatchAck, error)
func (c *Conn) PublishBatch(ctx context.Context, ops []PublishOp) (*PublishBatchAck, error)
```

- `publishAtomicBatch` takes `ctx` and drops its `timeout` param.
- Commit (last) message: send via `nc.RequestMsgWithContext(ctx, m)` instead of `nc.RequestMsg(m, timeout)`.
- Fire-and-forget (all-but-last) messages: check `ctx.Err()` before each `nc.PublishMsg(m)`; return the wrapped error if cancelled. (nats.go has no `PublishMsgWithContext`; the pre-send check is the available mitigation — this is the approach the CR suggested.)
- **Callers own their deadline.** Each call site wraps its context with the budget it previously passed as `timeout`:
  - `step8_commit.go`: `bctx, cancel := context.WithTimeout(ctx, c.Timeout); defer cancel()` → `AtomicBatch(bctx, ops)`.
  - `step9_publish.go`: same with `p.Timeout`. (Place the `WithTimeout` so a fresh deadline applies per retry attempt, matching today's behavior where each attempt got the full `p.Timeout`.)
  - `steps_4_10_stub.go`: `context.WithTimeout(ctx, 5*time.Second)`.
  - `installer.go` (both sites): `context.WithTimeout(ctx, DefaultBatchTimeout)`.
  - `identity-domain/seed.go`: `context.WithTimeout(ctx, 15*time.Second)`.
  - `primordial.go`: **no `WithTimeout` wrapper.** Pass the inherited `ctx` straight through — the 30s hardcode is removed. `cmd/bootstrap/main.go` already builds `readyCtx` from `BOOTSTRAP_READY_TIMEOUT_SEC` (default 30s); that deadline now governs the batch. Delete the stale comment block at primordial.go ~228–232 that explains the old limitation.

### 3.2 Per-key revisions from the contiguous-sequence guarantee

An atomic batch commits all N messages as a contiguous block; the commit ack returns the **last** message's stream sequence (`pubAckResponse.Sequence`) and the batch size (`pubAckResponse.BatchSize`). For a Core KV bucket, an entry's revision **equals** its stream sequence. Therefore:

```
firstSeq := ack.Sequence - ack.BatchSize + 1
revisions[ops[i].Key] = firstSeq + uint64(i)   // for i in 0..N-1
```

- Add `Revisions map[string]uint64` to `BatchAck`. Populate it in `AtomicBatch` by walking `ops` in order.
- **Defensive guard:** only derive when `ack.BatchSize == uint64(len(ops))` and `ack.Sequence+1 >= ack.BatchSize`. If the invariant doesn't hold, leave `Revisions` nil and log nothing (the caller already has the commit ack). Do not fabricate revisions.
- Duplicate keys within one batch: last-write-wins (map overwrite in `ops` order) — acceptable; mutations are one-op-per-key in practice.
- `PublishBatchAck` gets **no** revisions field — events are not KV entries; revision has no meaning there.

> **VERIFY THIS EMPIRICALLY (non-negotiable):** the contiguous-sequence + revision==stream-sequence assumption is the load-bearing premise. In `substrate_test.go`, after a successful `AtomicBatch`, read each key back via the KV API (`kv.Get(ctx, key)`) and assert `entry.Revision() == ack.Revisions[key]`. If this assertion fails on live NATS, STOP and append a CAR — the fallback design is a per-key follow-up read inside `AtomicBatch`, which is a different (heavier) implementation and needs Winston sign-off before you build it.

### 3.3 Wiring revisions into the reply

- `CommitAck` (step8) gains a `Revisions map[string]uint64` field. Populate it from `ack.Revisions`, **filtered to the operation's mutation keys only** (exclude the internal tracker key). Build the filtered map from `result.Mutations` keys.
- Replace the `BuildAcceptedReplyWithDetail(...)` call in `commit_path.go` with a builder that carries **both** detail and revisions. Add:

```go
func BuildAcceptedReplyWithRevisions(requestID string, committedAt time.Time,
    detail map[string]any, revisions map[string]uint64) OperationReply
```

  built on `BuildAcceptedReplyWithDetail`, additionally setting `r.Revisions = revisions` when non-empty. Keep `BuildAcceptedReplyWithDetail` (still the simplest path; the new builder composes it). The stub committer path does not need revisions.
- `OperationReply.Revisions` is already declared with the correct `json:"revisions,omitempty"` shape — no struct change there.

---

## 4. Out of scope (do NOT touch)

- DDL cache eviction on tombstone (Story 1.5.2 / P2-002).
- `UpdateMetaVertex` expansion (Story 1.5.3).
- Routing installs through the Processor (Story 1.5.5) — `pkgmgr` still calls `AtomicBatch` directly here; you are only migrating its **signature**, not its architecture.
- Conformance suite (Story 1.5.7) — you add unit/integration coverage for the new behavior, but the frozen conformance harness is a later story.
- The `ExpectedRevision`-defaulting "hardening deferred" note in step8 (line ~135) — leave it.

---

## 5. Verification gates (run all; paste tails into the closing summary)

```
go build ./...
make vet
golangci-lint run ./...
make up && make verify-bootstrap        # substrate + bootstrap regression
go test ./internal/substrate/... -count=1
go test ./internal/processor/... -p 1 -count=1
go test ./... -p 1 -count=1
make test-bypass                          # Gate 2 — must stay all-DEFENDED
make test-capability-adversarial          # Gate 3 — must stay all-BLOCKED
```

Flake-retry per Deviation 14 is allowed (re-run once); a flake *claim* without a re-run is a drift signal.

---

## 6. Deliverables checklist

- [ ] `AtomicBatch(ctx, ops)` / `PublishBatch(ctx, ops)` — timeout param dropped, ctx threaded into `publishAtomicBatch` (RequestMsgWithContext + pre-publish `ctx.Err()` checks).
- [ ] `BatchAck.Revisions map[string]uint64` populated from contiguous-sequence derivation, with the defensive guard.
- [ ] Empirical KV-readback assertion in `substrate_test.go` proving `entry.Revision() == ack.Revisions[key]`.
- [ ] `CommitAck.Revisions` (mutation-keys-only) + `BuildAcceptedReplyWithRevisions`; `commit_path.go` populates `OperationReply.Revisions` on accepted replies.
- [ ] All non-test call sites migrated (step8, step9, stub, installer ×2, seed, primordial) with correct per-site deadline wrapping; primordial's 30s + stale comment removed.
- [ ] All test call sites migrated.
- [ ] `docs/contracts/03-mutation-batch-event-list.md` updated: batch helpers take context; `OperationReply.Revisions` is now populated (with the derivation note).
- [ ] All §5 gates green.

---

## 7. Closing summary

### Deliverables checklist (against §6)

- [x] `AtomicBatch(ctx, ops)` / `PublishBatch(ctx, ops)` — timeout param dropped; ctx threaded into `publishAtomicBatch`, which uses `RequestMsgWithContext` on the commit message and a pre-publish `ctx.Err()` check on each fire-and-forget message.
- [x] `BatchAck.Revisions map[string]uint64` populated via `deriveRevisions` from the contiguous-sequence derivation, with the defensive guard (`batchSize == len(ops)` and `lastSeq+1 >= batchSize`; nil otherwise, no fabrication).
- [x] Empirical KV-readback assertion in `substrate_test.go` (`TestAtomicBatch_Commits`): reads each key via `KVGet` and asserts `entry.Revision == ack.Revisions[k]`. **Passed on live NATS** — premise holds, no CAR required.
- [x] `CommitAck.Revisions` (mutation-keys-only via `mutationRevisions`, tracker key excluded) + `BuildAcceptedReplyWithRevisions`; `commit_path.go` now populates `OperationReply.Revisions` on accepted replies.
- [x] All non-test call sites migrated with correct per-site deadline wrapping: step8 (`c.Timeout`), step9 (`p.Timeout`, per-retry-attempt), stub (5s), installer install + uninstall (`DefaultBatchTimeout`), identity-domain seed (15s), primordial (inherited `ctx`, no wrapper — 30s hardcode + stale comment block removed).
- [x] All test call sites migrated: `substrate_test.go` (×3), `publish_batch_test.go` (×3), `state_machine_test.go` (×1, unused `time` import removed).
- [x] `docs/contracts/03-mutation-batch-event-list.md` updated — new §3.9 documents context-aware helpers, the revision derivation, and reply propagation.
- [x] All §5 gates green (tails below).

### Files touched

- `internal/substrate/batch.go` — signatures, `BatchAck.Revisions`, `deriveRevisions`, `publishAtomicBatch` ctx threading.
- `internal/processor/step8_commit.go` — `bctx` wrap, `CommitAck.Revisions`, `mutationRevisions`.
- `internal/processor/steps_4_10_stub.go` — `CommitAck.Revisions` field, stub committer ctx wrap.
- `internal/processor/step9_publish.go` — per-attempt `bctx` wrap.
- `internal/processor/reply.go` — `BuildAcceptedReplyWithRevisions`.
- `internal/processor/commit_path.go` — accepted reply now carries revisions.
- `internal/bootstrap/primordial.go` — inherited ctx, removed 30s + stale comment.
- `internal/pkgmgr/installer.go` — install + uninstall ctx wraps.
- `packages/identity-domain/seed.go` — ctx wrap.
- `internal/substrate/substrate_test.go`, `internal/substrate/publish_batch_test.go`, `packages/identity-domain/state_machine_test.go` — call-site migration + KV-readback assertion.
- `docs/contracts/03-mutation-batch-event-list.md` — new §3.9.

### Verification-gate tails

- `go build ./...` — clean (no output).
- `make vet` — passed (no errors).
- `golangci-lint run ./...` — `0 issues.`
- `make up && make verify-kernel` (verify-bootstrap was renamed verify-kernel) — `verify-kernel: ALL ASSERTIONS PASSED`. Bootstrap ran clean; readiness gate satisfied. (Primordial seeding reported "already done on prior run" — idempotent path; the new ctx-only signature builds and bootstrap completed.)
- `go test ./internal/substrate/... -count=1` — `ok  github.com/operatinggraph/lattice/internal/substrate  1.451s` (includes the empirical KV-readback assertion).
- `go test ./internal/processor/... -p 1 -count=1` — `ok  github.com/operatinggraph/lattice/internal/processor  20.111s`.
- `go test ./... -p 1 -count=1` — all packages `ok` (no FAIL).
- `make test-bypass` — `PHASE 1 GATE 2: PASSED (4/4 BLOCKED)`.
- `make test-capability-adversarial` — `PHASE 1 GATE 3: PASSED (4/4 DEFENDED)`.

### Deviations / CARs

- None. No CAR was needed — the empirical revision==stream-sequence assertion passed on live NATS, so the contiguous-sequence derivation is used (no follow-up-read fallback).
- §5 lists `make verify-bootstrap`; the repo's current equivalent target is `make verify-kernel` (Makefile comment notes it "Replaces the old verify-bootstrap target"). Ran verify-kernel.
- `internal/spike/nats-batch/*` has its own local `publishAtomicBatch(nc, ..., timeout)` (separate package, not the substrate helper) — left untouched; it is not a call site of `substrate.Conn`.
