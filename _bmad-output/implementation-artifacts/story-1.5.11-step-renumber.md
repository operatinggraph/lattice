# Story 1.5.11 — Relocate event publishing out of the commit path; renumber the commit path to 9 steps

**Status:** SPEC — adjudicated (Winston). DS builds to §0; where §2–§3 of the CS draft conflict with §0, §0 wins.
**Recommended implementation tier:** Sonnet (mechanical, well-bounded, precision-critical).
**Module surface:** `internal/processor`, `internal/processor/outbox`, `internal/bootstrap`, `internal/substrate`, `internal/bypass`, `internal/testutil`, `cmd/processor`, plus `internal/processor/doc.go`, the live docs (`docs/components/*`, `docs/index.md`), the architecture Commit Path section, and `prd.md` (two present-tense lines).
**No new behavior.** Rename / relocation / renumber + doc-truth. `go build ./...` is clean at baseline.

---

## 0. ADJUDICATION — FINAL (Winston). DS builds to THIS.

The CS draft (§2–§7 below) is sound and exhaustive; ratifications and the few overrides:

**A — Publisher move: RATIFIED.** `internal/processor/step9_publish.go` → `internal/processor/outbox/publisher.go` (`package outbox`). Move `EventPublisherImpl`, `NewEventPublisher`, `PublicationError`, `EventSubject`. Core types (`Event`, `EventList`, `EventSpec`, `BuildEventList`, `OperationEnvelope`, `OutboxAspect*`, `TrackerKey`, …) STAY in `package processor`; reference them `processor.`-qualified in `publisher.go`. In `outbox/consumer.go` de-qualify (`*EventPublisherImpl`, `NewEventPublisher`). `processor` must NOT import `outbox` — `go build ./...` is the cycle gate.

**A-tests — OVERRIDE the CS "option (i)". Use option (ii) everywhere: no `package processor` test reconstructs the publish.** Rationale: the commit path's job ends at ack; publishing is the outbox consumer's job, already covered by `outbox/consumer_test.go`. Commit-path tests must assert the commit path's real output — the **persisted `vtx.op.<id>.events` aspect** — not reach into `core-events`. Concretely:
- `captureNFRState` (`nfr_r1_test.go:71-124`): DELETE the "model the consumer" block (`:94-106`, the `NewEventPublisher().Publish()` + `KVDelete`) AND the core-events drain (`:108-122`). Replace with: read the persisted outbox aspect and set `eventCount = len(aspect.Data.Events)` (0 if the aspect is absent — zero-event op). `assertMatchesBaseline`'s `eventCount == 1` invariant is preserved, now meaning "the commit persisted exactly one faithful event in the outbox aspect." This removes the only `package processor` use of the publisher.
- `step10_e2e_test.go` (→ `step9_e2e_test.go`): same — assert the persisted outbox aspect, drop the inline publish/core-events leg.
- `step9_publish_test.go` → `outbox/publisher_test.go` (moves WITH the publisher, `package outbox`): this is the publisher's OWN unit test — it legitimately publishes to `core-events` and asserts `EventSubject` sanitization. Rewrite its scaffolding onto the outbox test helpers (`setup(t)`, literal NanoIDs, local logger); keep all assertions.

**B — `EventPublisher` interface: OVERRIDE CS (delete, do not move).** It has zero interface-typed consumers (the consumer holds the concrete `*EventPublisherImpl`); an interface with one impl and no users is vestigial. Delete it from `steps_4_10_stub.go`; `EventPublisherImpl` stands alone as a concrete type in `outbox/publisher.go`. (We just removed dead publish scaffolding in 1.5.10 — stay consistent.)

**C — `steps_4_10_stub.go` → `step_interfaces.go`: RATIFIED.** After (B) it holds `Hydrator`/`Executor`/`Validator`/`Committer`/`CommitAck`/`Acker`/`AckerFactory`/`DefaultAckerFactory` + stub impls (no `EventPublisher`); fix the `CommitAck.Events` doc comment (events are persisted at step 8 + published by the outbox consumer, not "step 9"); `Acker (step 10)` → `(step 9)`.

**Test naming — OVERRIDE CS to avoid two `…Step9` tests:**
- `TestNFR_R1_FaultAtStep10` (ack) → **`TestNFR_R1_FaultAtStep9`** (ack IS step 9 now).
- the existing `TestNFR_R1_FaultAtStep9` (which 1.5.10 already rewrote to assert the persisted outbox aspect surviving redelivery — it is NOT a numbered commit step) → **`TestNFR_R1_CrashBeforeOutboxPublish`**.
- `nfrFaultStep10Ack` → `nfrFaultStep9Ack`; `faultinjector.go` `FaultStep10Ack` → `FaultStep9Ack` (+ all uses + the `"step10-ack"` string literal).

**D — Substrate/bootstrap "step 9" comments: RATIFIED** — reword to reference the outbox consumer (do NOT renumber as a commit step), per CS §3-v.

**prd.md: RATIFIED update.** `:164` and `:577` are present-tense current-state architecture descriptions ("Processor 10-step commit path", "10-step commit path") → change to **9-step**. (epics.md "10-step" hits are historical Story-1.8 AC text — LEAVE, per CS.)

**Architecture diagram `lattice-architecture.md:741`: RATIFIED** — `→ NATS publish to core-events` → `→ outbox consumer publishes to core-events`.

**`newPipelineWithRealEvents` helper:** it no longer wires a publisher (`Deps.Events` is gone). If trivially clear, DS may rename it to `newNFRPipeline`; not required — do not let it expand scope.

**Scope guardrail (Andrew, binding): do NOT rewrite historical artifacts** — every `_bmad-output/implementation-artifacts/story-*-handoff-brief.md`, `*-cr-*.md`, prior story specs (incl. 1.5.10), `token-usage-tracker.md`, `*-RESUME.md`, and the point-in-time reports stay as-is (CS §4). False-positive "step 9/10" sites to LEAVE: `aiagent/gate4_rollback_test.go`, `hellolattice/hellolattice_test.go`, and `lattice-architecture.md:83,129,524,592,1029,186`.

**Gate:** `go build ./... && go vet ./...` clean; the grep-clean AC (§5 item 6) with exactly the exceptions enumerated; full faithful preamble green (verify-package ×3, Gate 2/3/5); `go test ./internal/processor/... ./internal/processor/outbox/...` green incl. the outbox `-count` stress.

---

## 1. Context & rationale

Story 1.5.10 (commit `9fec07f`) made event publishing **outbox-only**. The synchronous "step 9: publish events" was removed from the Processor commit path: the faithful `EventList` is now persisted as the `vtx.op.<id>.events` aspect inside the step-8 atomic batch, and a durable consumer (`internal/processor/outbox/consumer.go`) publishes it to `core-events`, acking only after a confirmed publish. The commit path no longer publishes events synchronously (verify: `internal/processor/commit_path.go:323-328` — the in-line comment "Event publication is outbox-only … There is no in-commit publish"; there is no `EventPublisher` call in `HandleMessage`).

Consequently the naming/numbering across the codebase and docs is now **stale and misleading**:

- The publisher (`EventPublisherImpl`, `NewEventPublisher`, `PublicationError`, `EventSubject`) still lives in `package processor` as `step9_publish.go`, but its **only caller** is the outbox consumer (`internal/processor/outbox/consumer.go:61,171`). It no longer belongs in the commit-path package and is no longer a "step 9".
- The commit path's last step is **ack**, currently numbered "step 10" (`step10_ack.go`, `commit_path.go:340-349`). With publish gone from the *sequence*, ack is the **9th and final** synchronous step.
- `doc.go`, the live component docs, and the architecture Commit Path section all describe a "10-step commit path" with a numbered publish step. That is no longer true.

This story makes the code and docs tell the truth: a **9-step** synchronous commit path (consume → … → commit → ack), with event publishing happening **asynchronously in the outbox consumer**, not as a numbered commit step. The publisher moves to live beside its only caller.

**Scope discipline (anti-sloppiness):** the change inventory in §3 is exhaustive and file:line-anchored so a Sonnet dev executes it mechanically. §4 enumerates the historical artifacts that MUST NOT be touched. §7 enumerates the grep false-positives (e.g. `AC6 step 9` in an unrelated story test, HTTP/array `9`/`10`) that must be left alone.

---

## 2. Decisions §0 (A–D)

### A. Publisher package move & imports — **RECOMMEND, Winston must ratify (consequential)**

**Move `internal/processor/step9_publish.go` → `internal/processor/outbox/publisher.go`** and change its package clause from `package processor` to `package outbox`.

Symbols that **move** to `package outbox` (they are publisher-internal — verified no caller outside the publisher + its test + the outbox consumer):
- `EventPublisherImpl` (struct)
- `NewEventPublisher` (constructor)
- `PublicationError` (+ `Error()`, `Unwrap()`)
- `EventSubject` (function — grep confirms it is referenced **only** inside `step9_publish.go`; `internal/processor/step9_publish.go:96,148,163`. Not used anywhere else in `package processor`.)

Symbols that **stay** in `package processor` (core script-result / envelope types, used widely):
- `Event`, `EventList` (defined `step7_events.go:15,26`; used across processor, outbox, tests)
- `EventList.EventClasses()` (`step7_events.go:75`; used by `step8_commit.go:122`)
- `EventSpec`, `ScriptResult`, `OperationEnvelope`, `BuildEventList`, `OutboxAspectKey`, `ParseOutboxAspect`, `NewOutboxAspect`, `TrackerKey`

After the move, in `publisher.go` (now `package outbox`) those stay-types are referenced **qualified**: `processor.OperationEnvelope`, `processor.EventList`, `processor.Event` (in `Publish`'s signature and body). The publisher keeps importing `internal/substrate` (already imports it). It must add an import of `github.com/operatinggraph/lattice/internal/processor`.

In `internal/processor/outbox/consumer.go`, the publisher is now a **same-package** symbol — drop the `processor.` qualifier:
- `internal/processor/outbox/consumer.go:40` — `publisher *processor.EventPublisherImpl` → `publisher *EventPublisherImpl`
- `internal/processor/outbox/consumer.go:61` — `processor.NewEventPublisher(conn, logger)` → `NewEventPublisher(conn, logger)`
- (`consumer.go:171` `c.publisher.Publish(...)` is a method call on the field — unchanged.)

**Import-cycle check (critical):** current direction is `outbox → processor` (`consumer.go:21`). `processor` must NOT import `outbox`. After the move, `outbox/publisher.go` imports `processor` — same direction, no cycle. **`processor` gains no import of `outbox`.** This is preserved because the publisher's *callers that remain in `package processor`* (the in-package tests, decision A-tests below) are being removed/relocated, not rewired to import outbox. Confirm post-change with `go build ./...` (a cycle is a hard compile error).

**Every current caller of `NewEventPublisher` / `EventPublisherImpl` and its disposition:**

| # | Caller (file:line) | Package | Disposition |
|---|---|---|---|
| 1 | `internal/processor/outbox/consumer.go:40,61` | `outbox` | **Keep**, drop `processor.` qualifier (same package now). |
| 2 | `internal/processor/outbox/consumer.go:171` (`c.publisher.Publish`) | `outbox` | Unchanged (method on field). |
| 3 | `internal/processor/step9_publish_test.go` (whole file: `:46,56,107,129` + `EventSubject`/`PublicationError`) | `processor` | **Move the file** → `internal/processor/outbox/publisher_test.go`, `package outbox`. Rewrite to use `outbox`-local symbols + qualify processor types (see A-tests below). |
| 4 | `internal/processor/nfr_r1_test.go:99` (`pub := NewEventPublisher(...)` inside `captureNFRState`) | `processor` | **Replace the modeled-consumer block** — see A-tests below. After publisher leaves `package processor`, this call won't compile. |
| 5 | `internal/processor/step10_e2e_test.go:84` (`pub := NewEventPublisher(...)`) | `processor` | **Replace the modeled-consumer block** — see A-tests below. |

**A-tests — the sloppiness risk, resolved precisely.** Three `package processor` test files construct the publisher. After the move they cannot (cross-package import would create a cycle: `processor`-test → `outbox` → `processor`). Per-file resolution:

- **`step9_publish_test.go` → MOVE to `internal/processor/outbox/publisher_test.go` (`package outbox`).** This test *is* the publisher's unit test; it belongs with the publisher. Rewrite:
  - `package processor` → `package outbox`.
  - `EventSubject`, `NewEventPublisher`, `PublicationError` → unqualified (same package now).
  - `ScriptResult`, `EventSpec`, `EventList`, `BuildEventList`, `MutationOp` → `processor.` qualified.
  - **Helper dependencies:** it currently uses `setupTestPipeline`, `provisionEvents`, `testLogger`, `newTestEnvelope`, `testNanoID1`, `testNanoID2` — all `package processor` test helpers NOT available in `package outbox`. The `outbox` test package already has equivalents in `consumer_test.go`: `setup(t)` (returns ctx+conn, provisions Core KV + core-events), `startEmbeddedNATS`. Rewrite the moved tests to use the `outbox` package's `setup`/helpers, a local logger (`slog.New(...)` as `consumer_test.go:121` does), and literal NanoIDs (as `consumer_test.go:144-150` does). `provisionEvents` here is redundant — `outbox`'s `setup` already provisions `core-events` (`consumer_test.go:67-75`). `TestEventSubject_Sanitization` needs no NATS at all.
  - **Winston ratify:** the rewrite of test scaffolding (helper substitution) is the one non-trivial part. Recommend the dev keep each test's *assertions* identical and only swap the setup/helper calls. If a clean rewrite is disproportionate, the fallback is to keep `TestEventSubject_Sanitization` + the four publisher behavior tests in `outbox` using `setup()` + literals; do not drop coverage.

- **`nfr_r1_test.go` (`captureNFRState`, lines 86-106) and `step10_e2e_test.go` (lines 81-90):** both contain an identical "model the durable outbox consumer in-package" block: read the persisted outbox aspect, `NewEventPublisher(...).Publish(...)`, then `KVDelete` the aspect. This was a workaround because `package processor` can't import `outbox`. **The publisher leaving the package breaks the workaround, but the underlying need remains: these tests assert events reach `core-events`.** Resolution options (RECOMMEND option (i)):
  - **(i) RECOMMENDED — drive the aspect onto `core-events` without the publisher.** These tests only need *an* event on the stream to count it. Replace the `NewEventPublisher(...).Publish(...)` call with a direct `substrate.PublishBatch` (or `conn.JetStream().Publish` per event) of the aspect's events to `events.<class>`, then `KVDelete` the aspect. This removes the `package processor` dependency on the moved publisher while preserving the assertion (event count == 1). The subject derivation that `EventSubject` provided can be inlined as `"events." + ev.EventType` for these test events (the test event classes — `identity.created` — are already subject-safe; no sanitization needed). **Winston ratify** the inline-subject simplification.
  - (ii) Alternative — delete the modeled-consumer block entirely and assert only the *persisted outbox aspect* (which `nfr_r1_test.go:427-445` and `step10_e2e_test.go:67-79` already do directly), dropping the "event lands on core-events" leg from these two tests on the grounds that `internal/processor/outbox/consumer_test.go` already covers the publish leg end-to-end. Lower-coverage; only if (i) proves fiddly.
  - Either way, `captureNFRState`'s `eventCount` assertion (`assertMatchesBaseline`, `nfr_r1_test.go:142`) must still pass — option (i) keeps it.

> **Net A:** publisher.go + publisher_test.go in `outbox`; consumer.go de-qualified; two `package processor` integration tests stop calling the (now-absent) publisher and instead publish the aspect's events directly via substrate. No import cycle. The ONLY judgment calls flagged for Winston: the test-helper rewrite in publisher_test.go and the inline-subject simplification in the two integration tests.

### B. `EventPublisher` interface fate (`steps_4_10_stub.go:56-61`) — **RECOMMEND: move to `package outbox` (NOT delete)**

The `EventPublisher` interface is defined in `internal/processor/steps_4_10_stub.go:59-61`. Grep confirms it has **no interface-typed consumer** anywhere — `Deps` (commit_path) does not carry an `EventPublisher` field (verified: `newPipelineWithRealEvents` and the NFR `Deps` literal wire `Committer`, `AckerFactory`, etc., but no publisher), and the outbox consumer holds the **concrete** `*EventPublisherImpl`, not the interface (`consumer.go:40`).

Options:
- **(i) RECOMMENDED — move the interface to `package outbox`** (into `publisher.go`, renamed appropriately, e.g. keep `EventPublisher`). It documents the publish seam and is a natural home alongside the impl. Zero risk. If the outbox consumer is ever made test-double-able, the interface is right there.
- (ii) Delete it (concrete `*EventPublisherImpl` suffices; no current interface use). Slightly smaller surface but loses the documented seam.

Recommend **(i)** — cheap, keeps the seam, and removing it from `package processor` is required regardless (the impl that satisfied it is leaving). **Winston ratify** move-vs-delete; either is acceptable and both remove it from `package processor`.

### C. `steps_4_10_stub.go` rename — **RECOMMEND: rename to `step_interfaces.go`**

The "4_10" in the filename encoded "interfaces for steps 4 through 10". After (B) the `EventPublisher` interface leaves and the remaining interfaces span steps 4–9 (Hydrator/Executor/Validator/Committer/Acker/AckerFactory). A numeric range in the filename is now both wrong (4_10) and fragile (would be 4_9, and would re-break on the next renumber).

Recommend **`internal/processor/step_interfaces.go`** (non-numeric, future-proof). Acceptable alternative: `steps_4_9_stub.go` (keeps the convention but must then say 4–9 and re-changes on future renumbers — discouraged). After (B) the file contains: `Hydrator`, `Executor`, `Validator`, `Committer`, `CommitAck`, `Acker`, `AckerFactory`, `DefaultAckerFactory`, and the stub impls (`StubValidator`, `StubCommitter`, `NewStubCommitter`). The `EventPublisher` interface (lines 56-61) and the `// EventPublisher (step 9)` comment are **removed** (moved per B). Also fix the `CommitAck.Events` doc comment (`:41-43`) which says "so step 9 can publish" — reword to "so the outbox consumer can publish" (see §3-iii). **Winston ratify** the filename choice.

### D. Substrate/bootstrap "step 9" comments — **RECOMMEND: reword to reference the outbox consumer, do NOT renumber**

These are substrate-/bootstrap-layer explanatory comments that reference "the Processor's step 9 publish". Publishing is no longer a numbered commit step — it is the outbox consumer. **Do not renumber to "step 9 of 9"; reword to drop the step number and reference the outbox publish.** Exact rewordings in §3-v. Files: `internal/substrate/batch.go:213`, `internal/substrate/publish_batch_test.go:12`, `internal/bootstrap/primordial.go:143,145`, `internal/testutil/pipeline.go:83,85`, `internal/bypass/helpers.go:116`, `internal/testutil/faultinjector.go:207`.

---

## 3. Exhaustive change inventory

> Notation: file renames use `git mv` semantics (dev runs `git mv old new` via Bash, then edits the package clause / contents). "old → new" gives the exact text edit. Line numbers are from the baseline working tree; the dev should grep-confirm each before editing (edits earlier in a file shift later line numbers).

### (i) File renames + package move

| Old path | New path | Edits after move |
|---|---|---|
| `internal/processor/step9_publish.go` | `internal/processor/outbox/publisher.go` | `package processor` → `package outbox`; add import `github.com/operatinggraph/lattice/internal/processor`; qualify stay-types: `*OperationEnvelope`→`*processor.OperationEnvelope`, `EventList`→`processor.EventList` (signature of `Publish`), `events.EventClasses()` stays (method on `processor.EventList`), `ev.EventType`/`ev.EventID` stay (fields of `processor.Event`); renumber comments (§3-ii rows for this file). |
| `internal/processor/step9_publish_test.go` | `internal/processor/outbox/publisher_test.go` | `package processor` → `package outbox`; rewrite helper scaffolding to `outbox` test helpers (`setup`, literal NanoIDs, local logger); qualify `processor.ScriptResult`/`EventSpec`/`EventList`/`BuildEventList`/`MutationOp`; drop redundant `provisionEvents` (use `setup`); keep all assertions. (Decision A-tests.) |
| `internal/processor/step10_ack.go` | `internal/processor/step9_ack.go` | renumber comments/strings (§3-ii). Symbols `AckerImpl`/`NewAcker` are NOT numbered — unchanged. |
| `internal/processor/step10_ack_test.go` | `internal/processor/step9_ack_test.go` | grep for "step 10"/"step10" inside and renumber (baseline shows no numbered ref in the file beyond the filename; `:38` `js.Publish` is unrelated — leave). |
| `internal/processor/step10_e2e_test.go` | `internal/processor/step9_e2e_test.go` | renumber comments + the modeled-consumer block (Decision A-tests, option (i)); rename `TestE2E_FullTenStepCommitPath` → `TestE2E_FullNineStepCommitPath`; §3-ii rows. |
| `internal/processor/steps_4_10_stub.go` | `internal/processor/step_interfaces.go` | remove `EventPublisher` interface (per B → move to publisher.go); fix `CommitAck.Events` comment; renumber `Acker (step 10)` → `(step 9)` (§3-ii). |

### (ii) Commit-path renumber (10 → 9) — code symbols, comments, log fields, test names

| File:line | Old text | New text |
|---|---|---|
| **`internal/processor/commit_path.go`** :340 | `// --- Step 10: explicit Acker boundary. ---` | `// --- Step 9: explicit Acker boundary. ---` |
| :343 | `cp.deps.Logger.Warn("step 10: ack failed", ...)` | `"step 9: ack failed"` |
| :349 | `cp.deps.Logger.Info("step 10: ack", ...)` | `"step 9: ack"` |
| **`step9_ack.go`** (was step10_ack.go) :11 | `// AckerImpl is the step-10 implementation.` | `// AckerImpl is the step-9 implementation.` |
| :13 | `// makes step 10 testable for fault injection` | `// makes step 9 testable for fault injection` |
| :14 | `// wrapped by FailAfterN can simulate a crash exactly at step 10.` | `… exactly at step 9.` |
| :18 | `// MessageAcker; the commit_path invokes step 10 only after step 9` | `// MessageAcker; the commit_path invokes step 9 only after step 8` — **NOTE:** the old "after step 9 returns nil" referred to the publish step; ack now follows **commit (step 8)** directly. Reword line 18-19 to: `// the commit_path invokes step 9 (ack) only after step 8 (commit) succeeds and the reply is sent.` |
| :33 | `// Ack implements Acker (step 10).` | `// Ack implements Acker (step 9).` |
| :39 | `return fmt.Errorf("step 10: ack called with nil msg")` | `"step 9: ack called with nil msg"` |
| :42 | `return fmt.Errorf("step 10: ack: %w", err)` | `"step 9: ack: %w"` |
| **`step_interfaces.go`** (was steps_4_10_stub.go) :41-43 | `// the EventList built during step 8 so step 9 can publish the exact same / event IDs that were recorded in the tracker` | `// the EventList built during step 8 so the outbox consumer can publish the / exact same event IDs that were recorded in the tracker` |
| :56-61 | `// EventPublisher (step 9) — fans events out to core-events. … type EventPublisher interface { Publish(...) }` | **REMOVE** (moved to publisher.go per B). |
| :63-65 | `// Acker (step 10) — acks the JetStream message. AckerImpl is the production / implementation (step10_ack.go); …` | `// Acker (step 9) — acks the JetStream message. AckerImpl is the production / implementation (step9_ack.go); …` |
| **`publisher.go`** (was step9_publish.go) :34 | `// EventPublisherImpl is the step-9 implementation.` | `// EventPublisherImpl is the outbox publisher.` (publishing is not a numbered step) |
| :79 | `// Publish implements EventPublisher (step 9). It batch-publishes the` | `// Publish batch-publishes the` (or `// Publish implements EventPublisher. It batch-publishes the` if B-move keeps the interface) |
| :81-82 | `// at step 8 and passed here so event IDs are identical to those recorded in / the idempotency tracker.` | unchanged (step 8 is still step 8). |
| :85 | `p.Logger.Info("step 9: no events to publish", ...)` | `p.Logger.Info("outbox: no events to publish", ...)` |
| :93 | `return fmt.Errorf("step 9: marshal event %s: %w", ...)` | `"outbox: marshal event %s: %w"` |
| :113 | `p.Logger.Info("step 9: events published", ...)` | `p.Logger.Info("outbox: events published", ...)` |
| :123 | `p.Logger.Warn("step 9: batch publish failed; retrying", ...)` | `p.Logger.Warn("outbox: batch publish failed; retrying", ...)` |
| **`step7_events.go`** :14 | `// Events are published to core-events at step 9 via substrate.PublishBatch.` | `// Events are published to core-events by the outbox consumer via substrate.PublishBatch.` |
| :25 | `// ScriptResult at step 7 (BuildEventList) and published at step 9.` | `// ScriptResult at step 7 (BuildEventList) and published by the outbox consumer.` |
| **`step8_commit.go`** :112-113 | `// so step 9 publishes identical event IDs to those recorded in the tracker.` | `// so the outbox consumer publishes identical event IDs to those recorded in the tracker.` |
| **`step9_e2e_test.go`** (was step10_e2e_test.go) :15 | `// TestE2E_FullTenStepCommitPath is the Story 1.8 capstone:` | `// TestE2E_FullNineStepCommitPath is the Story 1.8 capstone:` |
| :17 | `// it through all 10 commit-path steps, and asserts:` | `// it through all 9 commit-path steps, and asserts:` |
| :24 | `// Step ordering is asserted at the log level (steps 1-10 in order)` | `// Step ordering is asserted at the log level (steps 1-9 in order)` |
| :26 | `func TestE2E_FullTenStepCommitPath(t *testing.T) {` | `func TestE2E_FullNineStepCommitPath(t *testing.T) {` |
| :81-90 | modeled-consumer block (`pub := NewEventPublisher...; pub.Publish...`) | Decision A-tests option (i): replace `NewEventPublisher(...).Publish(ctx, env, aspect.Data.Events)` with a direct substrate publish of `aspect.Data.Events` to `events.<EventType>` (then keep `KVDelete`). Update comment "Model the durable outbox consumer …" to "Drive the persisted aspect's events onto core-events (the outbox consumer's job) …". |
| :128-129 | `// 1.7 real Committer + Story 1.8 real EventPublisher + real Acker.` | `// 1.7 real Committer + outbox-published events + real Acker.` (no EventPublisher in the commit path anymore) |
| **`nfr_r1_test.go`** :32 | `nfrFaultStep10Ack nfrFaultLabel = "step10-ack"` | `nfrFaultStep9Ack nfrFaultLabel = "step9-ack"` (rename the const + its 2 uses at `:472`, `:494` region; the label STRING `"step10-ack"` → `"step9-ack"`). |
| :42 | `// nfrCleanBaseline runs the full 10-step happy path` | `// nfrCleanBaseline runs the full 9-step happy path` |
| :86-106 | modeled-consumer block in `captureNFRState` (`pub := NewEventPublisher...`) | Decision A-tests option (i): same direct-substrate-publish replacement as step9_e2e_test. Update the comment block (`:86-93`) to drop "In-package tests cannot import internal/processor/outbox (cycle), so we model the consumer here" → "Drive the persisted aspect onto core-events directly (the outbox consumer owns this in production)". |
| :380-383 | `// inside EventPublisher (Story / 1.8 step 9). We inject at the Committer seam:` | `// inside the outbox publisher (Story 1.8). We inject at the Committer seam:` (drop "step 9") |
| :408 | `// TestNFR_R1_FaultAtStep9: event publication is now outbox-only` | unchanged in spirit — this test name is a *fault-at-the-commit-boundary* name, not a commit-path step number; the body asserts the crash-between-commit-and-publish case. **Leave the test name `TestNFR_R1_FaultAtStep9`** (renaming it would imply ack; it is genuinely about the publish boundary, which is conceptually "after step 8"). Winston ratify: keep name. |
| :467 | `// TestNFR_R1_FaultAtStep10: Acker fails first call.` | `// TestNFR_R1_FaultAtStep9Ack: Acker fails first call.` — **and** rename the func `:471` `TestNFR_R1_FaultAtStep10` → `TestNFR_R1_FaultAtStep9Ack` (ack is now step 9). Use the `…Step9Ack` suffix to disambiguate from the publish-boundary `TestNFR_R1_FaultAtStep9` above. **Winston ratify** the disambiguating name. |
| :468 | `// logs and returns Accepted (step 8+9 already succeeded).` | `// logs and returns Accepted (step 8 commit already durable).` |
| :471 | `func TestNFR_R1_FaultAtStep10(t *testing.T) {` | `func TestNFR_R1_FaultAtStep9Ack(t *testing.T) {` |
| :472 | `trip := nfrOneShotTrip(nfrFaultStep10Ack)` | `trip := nfrOneShotTrip(nfrFaultStep9Ack)` |
| :480 | `…"proc-test-step10"…` | `…"proc-test-step9"…` (cosmetic durable tag; keep consistent) |
| :502 | `Durable: testDurable + "-step10",` | `…+ "-step9",` |
| :511 | `// First delivery: step 10 ack fails → commit_path logs and returns` | `// First delivery: step 9 ack fails → …` |
| :517 | `captureNFRState(t, ctx, conn, env.RequestID, "nfr-step10-events")` | `…"nfr-step9-events"` (cosmetic durable tag) |
| **`step45_e2e_test.go`** :11 | `// stubbed step 6+8+9 swallow the empty result; step 10 acks.` | `// stubbed step 6+8 swallow the empty result; step 9 acks.` (publish no longer a step) |
| **`cmd/processor/main.go`** :4 | `// the full 10-step commit path on each delivered operation envelope.` | `// the full 9-step commit path on each delivered operation envelope.` |
| **`internal/testutil/faultinjector.go`** :38 | `FaultStep10Ack FaultLabel = "step10-ack"` | `FaultStep9Ack FaultLabel = "step9-ack"` — rename const + its use at `:222` (`FailAfterN(n, FaultStep10Ack)`). Grep for `FaultStep10Ack` across the repo before renaming (it is exported from testutil). |
| :207 | `// FaultyAcker wraps an inner Acker (Story 1.8 step 10).` | `// FaultyAcker wraps an inner Acker (Story 1.8 step 9).` |

> **`internal/aiagent/gate4_rollback_test.go` — DO NOT TOUCH.** Its "step 9"/"step 10" / "AC6 step N" refs (`:139,157,173,225,506,527`) are **Story 5.3 AC6** sub-step numbering for a different acceptance criterion, unrelated to the Processor commit-path numbering. False positives. Leave entirely.
> **`internal/hellolattice/hellolattice_test.go` — DO NOT TOUCH.** Its "Step 9"/"Step 10" (`:694,704`) are narrative steps of a tutorial test scenario, not commit-path steps. False positives.
> **`packages/identity-domain/state_machine_test.go:6` — UPDATE.** `// IdentityMerged guard end-to-end through the 10-step Processor` is a current-state description of the Processor → `// … through the 9-step Processor`. (Confirm in-context it refers to the commit path; baseline shows it does.)

### (iii) `doc.go` + live docs

**`internal/processor/doc.go`:**

| :line | Old | New |
|---|---|---|
| :4 | `// through the 10-step commit path, and atomically commits mutations +` | `// through the 9-step commit path, and atomically commits mutations +` |
| :7 | `// The 10-step commit path:` | `// The 9-step commit path:` |
| :17 | `//	step 9: batch-publish events to core-events JetStream` | **REMOVE this line.** Add after the step list: `// Event publishing is NOT a commit step: the faithful EventList is persisted` `// in the step-8 atomic batch (vtx.op.<id>.events) and published asynchronously` `// by the durable outbox consumer (internal/processor/outbox).` |
| :18 | `//	step 10: ack the JetStream message` | `//	step 9: ack the JetStream message` |
| :27 | `//	internal/processor/steps_4_10_stub.go – stub interfaces for downstream steps` | `//	internal/processor/step_interfaces.go – interfaces for downstream steps` (per C) |
| :28 | `//	internal/processor/commit_path.go    – top-level driver wiring 1-3 + stubbed 4-10` | `//	internal/processor/commit_path.go    – top-level driver wiring 1-9` (publish is no longer a wired step) |

> **Bonus accuracy fix (in-scope, low-risk):** `doc.go` does not list the new `internal/processor/outbox` package in its Wire layout. Recommend adding `//	internal/processor/outbox/         – durable outbox consumer + event publisher`. Winston ratify (nice-to-have).

**`docs/components/_index.md`:** `:16` `…operation write path, 10-step commit pipeline` → `…9-step commit pipeline`.

**`docs/index.md`:** `:32` `Processor — 10-step commit pipeline, lane consumers, …` → `Processor — 9-step commit pipeline, lane consumers, …`.

**`docs/components/processor.md`:**

| :line | Old | New |
|---|---|---|
| :11 | `10-step commit pipeline, and result in atomic KV mutations plus published` | `9-step commit pipeline, and result in atomic KV mutations plus asynchronously published` |
| :24 | `Pipeline logic — all 10 steps, Starlark sandbox, … committer, event publisher` | `Pipeline logic — all 9 steps, Starlark sandbox, … committer` (event publisher moved to outbox; mention `internal/processor/outbox/` separately if desired) |
| :29 | `drives the 10-step loop;` | `drives the 9-step loop;` |
| :40 | `Gate 2 bypass test (no 10-step bypass; …)` | `Gate 2 bypass test (no 9-step bypass; …)` |
| :60 | `\| **Events** … \| Published as an unconditional substrate.PublishBatch at step 9 after the commit is durable \|` | `\| **Events** … \| Persisted in the step-8 atomic batch (vtx.op.<id>.events) and published asynchronously by the durable outbox consumer via substrate.PublishBatch \|` |
| :68 | `## The 10-step write path` | `## The 9-step write path` |
| :84 (the whole "9 \| Publish" table row) | the `\| 9 \| **Publish** \| EventPublisher.Publish … \|` row | **REMOVE the row from the numbered table.** Add a short paragraph after the table: "**Event publishing (asynchronous, not a numbered step).** The faithful EventList is persisted in the step-8 atomic batch as `vtx.op.<id>.events`; the durable outbox consumer (`internal/processor/outbox`) publishes it to `events.<class>` on `core-events`, acking only after a confirmed publish. See Story 1.5.10." |
| :85 (`\| 10 \| **Ack** …`) | `\| 10 \| **Ack** \| Acker.Ack …` | `\| 9 \| **Ack** \| Acker.Ack …` (renumber the row to 9; keep cell text). |
| :421 | `\| PublicationError \| Step 9 \| Nak; JetStream re-delivers; step-2 dedup short-circuits mutation; step 9 re-runs \|` | `\| PublicationError \| Outbox publish \| Nak; outbox consumer redelivers and republishes the persisted EventList (at-least-once) \|` |
| :439 | `every Core KV mutation passes through all 10 steps.` | `every Core KV mutation passes through all 9 steps.` |

> Also scan `docs/components/processor.md` for any prose calling step 7 "Materialize events" vs step 7 "Validate EventList" mismatch — **out of scope** for this story (do not fix step-7 naming here; it predates 1.5.11). Only renumber 10→9 and de-step the publish row.

### (iv) Architecture doc Commit Path (`_bmad-output/planning-artifacts/lattice-architecture.md`)

This is the **canonical** commit-path description and IS in scope.

| :line | Old | New |
|---|---|---|
| :179 | `9. **Publish events** — publish validated events to core-events JetStream` | **REMOVE from the numbered list.** |
| :180 | `10. **Ack** — acknowledge the core-operations JetStream message` | `9. **Ack** — acknowledge the core-operations JetStream message` |
| after :180 | — | Add a note: `> **Event publishing is asynchronous (not a numbered commit step).** Per Story 1.5.10, the faithful EventList is persisted in the step-8 atomic batch (vtx.op.<id>.events) and published to core-events by a durable outbox consumer, acking only on confirmed publish. The synchronous commit path is 9 steps; publishing is decoupled.` |
| :184 | crash-recovery prose: `…acked after the entire commit path completes (step 10). If the Processor crashes between step 8 (atomic batch) and step 9 (event publish), … the Processor must still publish events before acking…` | Reword to the outbox model: `…acked after the commit path completes (step 9, ack). The faithful EventList is persisted atomically in step 8; a durable outbox consumer publishes it to core-events. A crash between commit and publish is recovered by the outbox consumer's redelivery — the synchronous path no longer publishes, so dedup on redelivery simply acks.` (Align with the existing 1.5.10 callout at :186.) |
| :635 | `│ ├── commit.go # 10-step commit path implementation` | `│ ├── commit_path.go # 9-step commit path implementation` (also corrects the stale filename `commit.go` → `commit_path.go`). |
| :738 | `→ processor/commit.go runs 10-step commit path` | `→ processor/commit_path.go runs 9-step commit path` |
| :741 | `→ NATS publish to core-events` (in the same flow diagram) | reword to `→ outbox consumer publishes to core-events` (publishing is no longer in the synchronous processor flow). **Winston ratify** — this is a sequence diagram; the dev should keep it readable. |
| :801 | `Processor/logic: covered by 10-step commit path, …` | `…covered by 9-step commit path, …` |

> **NOT to change in this file:** `:83,129,524,592,1029` — these mention "publish"/"published" in unrelated contexts (CDC contracts, NATS benchmarks, Translator HTTP→NATS publish, fault-harness description). The fault-harness line `:129` ("crash between atomic batch and event publish") is still accurate (the outbox consumer's crash-between-commit-and-publish) — leave. `:186` (the 1.5.10 outbox callout) is already correct — leave. `:179-180,184` are the only step-numbered lines.

### (v) Substrate/bootstrap/testutil/bypass comment rewording (Decision D — reword, do NOT renumber)

| File:line | Old | New |
|---|---|---|
| `internal/substrate/batch.go` :213 | `// core-events stream's events.> filter for the Processor's step 9.` | `// core-events stream's events.> filter, published by the Processor's outbox consumer.` |
| `internal/substrate/publish_batch_test.go` :12 | `// events.> subjects the Processor's step 9 publishes onto. We enable` | `// events.> subjects the Processor's outbox consumer publishes onto. We enable` |
| `internal/bootstrap/primordial.go` :142-143 | `// substrate.PublishBatch step-9 path (see / internal/processor/step9_publish.go).` | `// substrate.PublishBatch outbox path (see / internal/processor/outbox/publisher.go).` |
| `internal/bootstrap/primordial.go` :145 | `Description: "Core events stream — Processor publishes business events here at step 9",` | `Description: "Core events stream — the Processor's outbox consumer publishes business events here",` |
| `internal/testutil/pipeline.go` :83-87 | `// core-events stream — step 9 publishes business events (e.g. … Without it step 9 fails and naks for redelivery, …` | `// core-events stream — the outbox consumer publishes business events (e.g. … Without it the outbox publish fails and naks for redelivery, …` (reword both "step 9" mentions; keep the rest). |
| `internal/bypass/helpers.go` :3 | `// impossible against the 10-step Processor commit path (Stories 1.3-1.9).` | `// impossible against the 9-step Processor commit path (Stories 1.3-1.9).` |
| `internal/bypass/helpers.go` :116 | `// full commit path including step 9 is exercised).` | `// full commit path including the outbox publish is exercised).` |

### (vi) Test renames + test-logic relocation — summary (detail in (i),(ii),A-tests)

- `step9_publish_test.go` → `outbox/publisher_test.go` (`package outbox`), helper-rewritten. **Moves out of `package processor`.**
- `step10_ack_test.go` → `step9_ack_test.go` (rename only; no numbered refs inside per baseline grep — confirm).
- `step10_e2e_test.go` → `step9_e2e_test.go`, func renamed `…FullTenStep…` → `…FullNineStep…`, modeled-consumer block de-published (option (i)).
- `nfr_r1_test.go`: const `nfrFaultStep10Ack`→`nfrFaultStep9Ack`, func `TestNFR_R1_FaultAtStep10`→`TestNFR_R1_FaultAtStep9Ack`, modeled-consumer block de-published, comment renumbers. `TestNFR_R1_FaultAtStep9` (publish-boundary) name kept.
- `faultinjector.go`: const `FaultStep10Ack`→`FaultStep9Ack` (+ use at `:222`).
- No NEW test files. No deleted coverage (publisher unit tests relocate intact; integration tests keep their event-count assertions via direct substrate publish).

---

## 4. Explicitly EXCLUDED — point-in-time historical records (DO NOT rewrite)

Renumbering these would falsify the historical record. **Leave entirely**, even where they say "step 9"/"step 10"/"10-step":

- Every `_bmad-output/implementation-artifacts/story-*-handoff-brief.md`
- Every `_bmad-output/implementation-artifacts/*-cr-*.md` (code-review records)
- `_bmad-output/implementation-artifacts/story-1.5.10-event-outbox.md` and all prior story specs (1.5.1–1.5.9, 1.8, etc.) — historical; their "step 9 publish" references describe the world at the time they were written.
- `_bmad-output/implementation-artifacts/token-usage-tracker.md`
- `WINSTON-RESUME.md` (and any `*-RESUME.md`)
- `processor.log` (untracked runtime log)
- `_bmad-output/planning-artifacts/gate5-external-tester-report.md`, `implementation-readiness-report-2026-04-10.md`, `sprint-change-proposal-2026-05-28.md`, `MORPH-DEVIATIONS.md`, `refractor-gap-analysis.md`, `materializer-morph-plan.md` — point-in-time reports.

**`epics.md` / `prd.md` — per-line ruling (these are requirement/epic records; default to LEAVE; update only true current-state architecture descriptions):**

| File:line | Text | Ruling |
|---|---|---|
| `epics.md:529` | "…steps 9 and 10 of the Processor commit path — JetStream event publication and JetStream ack — … the complete 10-step commit path…" | **LEAVE.** This is the Story 1.8 user-story statement — a historical epic record of what 1.8 delivered. Renumbering would falsify the epic. |
| `epics.md:537,541,549,550,591` | "step 9 (event publication)", "step 10 (JetStream ack)", "10-step happy path", "all 10 steps", "10-step Processor commit path" | **LEAVE.** All within Story 1.8 acceptance criteria — historical. |
| `prd.md:164` | "Stream 1 (Core): Processor 10-step commit path, …" | **RECOMMEND UPDATE → "9-step commit path"** *if* Winston treats the PRD's architecture-summary bullets as living current-state. **Winston ratify** — borderline: it is a PRD scope bullet, arguably historical. Default if unsure: leave + add no note. |
| `prd.md:577` | "10-step commit path" (in a feature/scope list) | Same ruling as `prd.md:164` — **Winston ratify**; recommend update only if treating PRD as living. |

> Recommendation: **leave `epics.md` entirely** (pure historical epic/story text). For `prd.md:164,577`, defer to Winston; the safe default is leave (the PRD is a point-in-time planning artifact and 1.5.10/1.5.11 are post-PRD hardening). The LIVE source of architectural truth is `lattice-architecture.md` (updated in §3-iv) and the `docs/` component docs (§3-iii) — those are the must-update surfaces.

---

## 5. Acceptance criteria (testable)

1. **Build/vet clean:** `go build ./...` exits 0; `go vet ./...` exits 0.
2. **Publisher relocated:** `internal/processor/step9_publish.go` and `internal/processor/step9_publish_test.go` **do not exist**. `internal/processor/outbox/publisher.go` exists and declares `package outbox` with `EventPublisherImpl`, `NewEventPublisher`, `PublicationError`, `EventSubject`. `internal/processor/outbox/publisher_test.go` exists (`package outbox`).
3. **Ack renumbered:** `internal/processor/step9_ack.go` exists (`step10_ack.go` gone); `step9_ack_test.go`, `step9_e2e_test.go` exist (`step10_*` gone).
4. **Interfaces file renamed (C):** `internal/processor/step_interfaces.go` exists; `steps_4_10_stub.go` gone; it no longer declares `EventPublisher` (per B).
5. **No import cycle:** `processor` does not import `outbox` (`go list -deps`/build proves it). `outbox` imports `processor` (unchanged direction).
6. **Grep-clean of OLD numbering in code + live docs.** After the change, these must return **zero** hits except the enumerated false-positives:
   - `grep -rn "10-step\|10 step\|step 10\|step10\|Step 10\|Step10\|steps_4_10\|4_10\|FullTenStep\|Step10Ack\|FaultStep10" --include="*.go"` → **0** hits in `internal/**` and `cmd/**`.
   - `grep -rn "step 9\|step9\|step-9\|Step 9" --include="*.go"` → only **allowed** hits: the publisher's own `outbox`-package code if any "step" wording survived (recommend none — reworded to "outbox:"), `TestNFR_R1_FaultAtStep9` (publish-boundary test name, intentionally kept), and unrelated `js.Publish`/array tokens. **Disallowed:** any "step 9: publish", "step-9 path", "Acker (step 10)", etc.
   - `grep -rn "10-step\|step 9\|step 10" docs/components/processor.md docs/index.md docs/components/_index.md` → **0** hits (all renumbered to 9 / de-stepped).
   - Architecture: `lattice-architecture.md` Commit Path list (`:169-180` region) has **9** numbered steps ending in "Ack"; no numbered "Publish events" step; the async-publish note present.
   - **Allowed exceptions (must remain unchanged):** `internal/aiagent/gate4_rollback_test.go` (AC6 sub-steps), `internal/hellolattice/hellolattice_test.go` (tutorial steps), every file in §4 (historical artifacts), `lattice-architecture.md:83,129,524,592,1029,186`.
7. **Docs say 9-step:** `doc.go`, `docs/components/processor.md` (title + summary + table), `lattice-architecture.md` all describe a 9-step commit path with asynchronous outbox publish.
8. **Faithful CI preamble green:** `verify-package` ×3 (or the project's pinned preamble), Gate 2 / Gate 3 / Gate 5 all pass. (Per project workflow — Winston runs the faithful preamble before commit.)

---

## 6. Test plan

1. **`go build ./... && go vet ./...`** — first gate; catches the import cycle, a missed qualifier, a dangling symbol after the move.
2. **`go test ./internal/processor/... -count=1`** — the renamed/edited commit-path tests must all pass:
   - `TestE2E_FullNineStepCommitPath` (was FullTenStep) — event still lands on core-events via the direct-substrate-publish replacement.
   - `TestNFR_R1_FaultAtStep9` (publish boundary, name kept) and `TestNFR_R1_FaultAtStep9Ack` (was FaultAtStep10) — both pass; `captureNFRState`/`assertMatchesBaseline` `eventCount == 1` holds.
   - `step45_e2e_test`, `step8_*`, `step7_*` unaffected by behavior.
3. **`go test ./internal/processor/outbox/... -count=1`** — the moved publisher tests (`publisher_test.go`: `TestEventSubject_Sanitization`, `TestEventPublisher_NoEventsShortCircuits`, `TestEventPublisher_HappyPath`, `TestEventPublisher_RetriesOnTransientFailure`, `TestEventPublisher_FailureSurfacesPublicationError`) pass in `package outbox`, plus the existing `consumer_test.go` suite.
4. **Outbox stress:** `go test ./internal/processor/outbox/... -run TestOutbox -count=20` — confirm no flake from the relocation (publisher + consumer now same package).
5. **Full module:** `go test ./... -count=1` — picks up `testutil`/`bypass`/`substrate`/`bootstrap`/`aiagent`/`packages` consumers of the renamed `FaultStep9Ack` const and the reworded comments.
6. **Faithful CI preamble** (Winston): `verify-package` ×3 + Gate 2/3/5.

---

## 7. Risks & false-positive traps

1. **Import cycle (highest):** if any `package processor` test or file is left calling the moved publisher, the fix-up that wires it to `outbox` would create `processor → outbox → processor`. **Mitigation:** the A-tests resolution removes ALL `package processor` references to the publisher (no `package processor` code imports `outbox`). `go build ./...` is the hard check — a cycle fails compilation outright.
2. **A test that genuinely still needs the publisher:** `nfr_r1_test.go` and `step10_e2e_test.go` *modeled the consumer* via the publisher. Option (i) (direct `substrate.PublishBatch` of `aspect.Data.Events` to `events.<EventType>`) preserves their event-count assertion without the publisher. **Risk:** the inline subject must match `EventSubject` output for the test classes. Test classes are `identity.created` (already subject-safe → `events.identity.created`), so `"events." + ev.EventType` is exact. Documented as a Winston-ratify simplification.
3. **Mechanical renumber hitting unrelated "step 9"/"step 10":** enumerated false-positives in §3-ii note + §5 AC6: `gate4_rollback_test.go` (AC6 sub-steps for Story 5.3), `hellolattice_test.go` (tutorial steps), `js.Publish`/`conn.JetStream().Publish` (substrate publish calls, not "step 9"), array indices / HTTP codes (none found, but the dev must NOT blind-`sed`). **Mitigation:** every edit in §3 is line-anchored and quotes exact old→new text; the dev edits by exact-match, never by global replace of the bare token "9"/"10".
4. **`FaultStep10Ack` / `nfrFaultStep10Ack` are referenced at multiple sites:** rename the const AND all uses (`faultinjector.go:38,222`; `nfr_r1_test.go:32,472`-region). Grep the whole repo for each const before renaming. Missing a use = compile error (caught by build), but a stale STRING literal `"step10-ack"` is silent — grep for the literal too.
5. **Stale filename refs in docs/comments:** `doc.go:27` refers to `steps_4_10_stub.go`; `step_interfaces.go`-comment (was `steps_4_10_stub.go:64`) and `bootstrap/primordial.go:143` refer to `step9_publish.go`/`step10_ack.go`. After file renames these path strings dangle — §3 lists each. Grep `grep -rn "step9_publish\|step10_ack\|steps_4_10" .` (excluding §4 historical) must be 0 after.
6. **`TestNFR_R1_FaultAtStep9` vs `TestNFR_R1_FaultAtStep9Ack` collision:** keeping the publish-boundary test as `…Step9` and renaming the ack test to `…Step9Ack` avoids a duplicate-function-name compile error. The dev must NOT rename the publish-boundary test to `…Step9` blindly (it already IS that) nor the ack test to a bare `…Step9`. Flagged for Winston in §3-ii.
7. **`prd.md`/`epics.md` over-reach:** the temptation to "fix all 10-step refs" would falsify historical epics. §4 rules: leave `epics.md`; defer `prd.md:164,577` to Winston. Do not touch handoff briefs / CR records / trackers.
8. **`packages/identity-domain/state_machine_test.go:6`** "10-step Processor" — confirmed a current-state Processor description → update to 9-step (in scope, §3-ii note). Verify in-context it is not quoting a historical story before editing.

---

## 8. Execution order (suggested, for the Sonnet dev)

1. `git mv` the six files (§3-i). Fix package clauses + imports in `publisher.go`/`publisher_test.go`.
2. De-qualify the publisher in `outbox/consumer.go`. Move the `EventPublisher` interface (B) into `publisher.go`; remove it from the renamed `step_interfaces.go`.
3. `go build ./...` — fix until clean (this surfaces cycle/qualifier/symbol errors fast).
4. Renumber commit-path code + comments (§3-ii) and the two integration tests' modeled-consumer blocks (option (i)).
5. `go test ./internal/processor/... ./internal/processor/outbox/... -count=1`.
6. Rename consts/labels in `faultinjector.go` (+ uses); `go test ./... -count=1`.
7. Docs: `doc.go`, `docs/**`, `lattice-architecture.md`, substrate/bootstrap/testutil/bypass comments (§3-iii,iv,v).
8. Grep-clean verification (§5 AC6). `go vet ./...`.
9. Hand to Winston for the faithful preamble + commit. **Do not commit.**
