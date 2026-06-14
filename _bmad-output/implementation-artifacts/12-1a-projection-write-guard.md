# Story 12.1a: Monotonic projection-write guard — security/correctness plane (D-INTEGRITY)

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the platform security owner,
I want every guarded per-actor projection write to be rejected if a newer projection for that key already landed,
so that a retried or reordered stale projection can never resurrect a revoked ephemeral grant (security plane) or a closed task (correctness plane).

## Context — the confirmed-reachable bug

This is a **confirmed-reachable security bug, not theoretical** (decision record, D-INTEGRITY). The exact chain, grounded in the current tree:

1. Refractor's retry queue captures an **evaluation result (a row)**, not a re-evaluation: `enqueueRetry` snapshots `capturedResult := result` (`internal/refractor/pipeline/pipeline.go:691`) and the replay `WriteFn` calls `a.Upsert(rctx, capturedResult.Keys, capturedResult.Row)` (`pipeline.go:710`; the delete branch is `a.Delete(rctx, capturedResult.Keys)` at `:708`).
2. The retry queue runs the `WriteFn` on its **own goroutine** (`internal/refractor/failure/retry.go`, `RetryQueue.Run`) — concurrent with the main consumer. This makes the key race real, not theoretical.
3. `CreateTask` fans out multiple CDC events; each independently triggers an actor reprojection. Sequence: an **open-era** `Upsert` (grant/task present) fails transiently → captured into the retry queue. The task later closes; the close event reprojects the actor → zero real grants → `ErrDeleteProjection` → `Delete` succeeds (`internal/refractor/capabilityenv/envelope.go:168` for ephemeral, `:269` for my-tasks).
4. The retry of the captured open-era `Upsert` fires **after** the delete and re-writes the revoked grant. **No further CDC event re-deletes it** — the task is already closed, so nothing re-triggers that actor's reprojection.

On `cap.ephemeral.<actor>` (`capabilityEphemeral` lens) this resurrects a **revoked ephemeral grant on the security plane** (Contract #6 is security-critical: "A bug here equals privilege escalation"). On `my-tasks.<actor>` (`myTasks` lens) it resurrects a closed task (a queryable surface lies; no auth consequence). The 7.1/7.2 E2E masks it today with a `requireQuiescentRevision` settle-wait (`refractor_mytasks_e2e_test.go:211`/`:229`).

**The fix:** stamp every guarded actor-aggregate write with a monotonic `projectionSeq` = the **JetStream stream sequence of the triggering CDC message**, and make the NATS-KV adapter write conditionally (CAS) so a lower-seq replay is rejected as an idempotent no-op. A `Delete` becomes a soft tombstone carrying `projectionSeq` so the high-water mark survives physical absence.

The contract amendments are **already ratified and APPLIED** (commit `29b9536`) — Contract #6 §6.2/§6.3/§6.8 and Contract #10 §10.1. This story **builds to** the frozen contract; it does not edit it.

## Scope (read before implementing — do NOT scope-creep into 12.1b)

**IN scope (12.1a):**
- NATS-KV adapter **only** (`internal/refractor/adapter/natskv.go`).
- The **two at-risk lenses only**: `capabilityEphemeral` (security plane) and `myTasks` (correctness plane).
- `projectionSeq` plumbed **end-to-end**: `handle` already has `msg.Sequence` (= `msg.Metadata().Sequence.Stream`, populated in `internal/substrate/consumer.go:55`) → stamp it onto `simple.EvalResult` (new field) → pass it to the adapter write — covering **both** the fan-out path and the inline path (and the retry-replay path, which carries it automatically via `capturedResult`).
- CAS write + **bounded re-read-on-conflict loop** in the NATS-KV adapter.
- Soft tombstone `{isDeleted:true, projectionSeq:<seq>}` on guarded keys.
- Flip the `my-tasks` E2E vanish-on-close assertion to assert a tombstone; remove the `requireQuiescentRevision` settle-wait.
- A **fail-without/pass-with adversarial regression test** that reproduces the captured-retry resurrection and asserts rejection — lands in the **Gate 3 (DEFENDED)** suite (`internal/bypass/`), must FAIL on `main` (no guard) and PASS with the guard.
- Gate 2 (BLOCKED) + Gate 3 (DEFENDED) pass.

**OUT of scope (these are 12.1b / later — do NOT touch):**
- The primary `capability` lens guard (`cap.<actor>`). 12.1b.
- The guard ↔ `Rebuild` reconciliation (force-truncate-on-guarded-bucket OR documented bypass flag). 12.1b. **Do not attempt to make `Rebuild(truncate=false)` work against a guarded bucket here** — that interaction is 12.1b's whole subject. If your change makes an existing rebuild test fail, that is a signal to STOP and flag it, not to fix it here.
- `capabilityRoleIndex` is **explicitly excluded** from the guard family — it is keyed by `operationType`, not actor (operation-aggregate, not actor-aggregate). Do not add the guard to it.
- The Postgres adapter is **OUT** — it implements the extended write signature as a **pass-through / no-guard** and that fact is documented in code (describing what the code does now, not narrating a change).

## Acceptance Criteria

(Verbatim backbone from `phase-2-epics.md` § "Story 12.1a", re-grounded to the current tree.)

1. **`projectionSeq` is plumbed end-to-end.** The triggering CDC message's stream sequence (`msg.Sequence`, already populated from `msg.Metadata().Sequence.Stream`) is threaded into `simple.EvalResult` as a new field, so the retry-queue capture (`capturedResult := result`) carries it; the pipeline passes it to the adapter on every write — for **both** the fan-out path and the inline path.
2. **The `adapter.Adapter` interface is extended** to carry the ordering token on writes (`Upsert`/`Delete` gain a `projectionSeq uint64` parameter, or an `EvalResult`-shaped arg). `Delete`'s nil-row case is handled because the token is **not** carried in the row body. The **Postgres adapter is explicitly out of scope** — it implements the new signature as a pass-through/no-guard (documented in code); only `NatsKVAdapter` enforces the guard.
3. **The NATS-KV adapter writes conditionally via CAS, not read-then-write**: `Get` (current revision + stored `projectionSeq`) → drop the write as an idempotent no-op when `incoming.projectionSeq ≤ stored.projectionSeq` → else `Update` with `ExpectedRevision`. On a revision-conflict error, **re-read and re-compare in a bounded loop** (the concurrent retry-goroutine writer makes this load-bearing, not theoretical).
4. **A `Delete` on a guarded key becomes a soft tombstone** `{isDeleted:true, projectionSeq:<seq>}` so the high-water mark survives physical absence. Step-3 already denies on both an absent key and a tombstone (no grants → no match), so auth semantics are unchanged (Contract #6 §6.8).
5. **The `my-tasks` E2E vanish-on-close assertion is flipped**: `refractor_mytasks_e2e_test.go` currently asserts the key is **absent** after close (`require.Eventually(... key absent → vanished on close ...)`, ~`:218–222`) — with the soft tombstone it asserts the key is a **tombstone** (the doc exists and `isDeleted == true`). The `requireQuiescentRevision` settle-wait (call at `:211`, helper at `:229`) is **removed** (the masked race is now structurally closed; delete the now-dead helper too).
6. **A fail-without/pass-with adversarial regression test** reproduces the exact captured-retry resurrection (enqueue an open-era `Upsert`, commit a close-`Delete`, fire the retry) and asserts the stale replay is **rejected** (the key stays a tombstone / absent of grants). The test must **FAIL against `main`** (no guard — the stale write resurrects the grant) and **PASS with the guard**. It lands in the **Gate 3 (DEFENDED)** adversarial suite (`internal/bypass/`, a `TestCapAdv_*` test wired into the `TestGate3_Report` roll-up), **not** a lone unit test.
7. **The change is built to the already-applied Contract #6 §6.2/§6.3/§6.8 amendment** (`projectionSeq` field, interface change, CAS, soft-tombstone-carries-watermark) and **Contract #10 §10.1** (my-tasks consumer MUST skip `isDeleted` tombstones — forward obligation; no production reader exists yet, only the E2E). These contracts are FROZEN and already ratified (commit `29b9536`); do not edit them.
8. **Gate 2 (BLOCKED) + Gate 3 (DEFENDED) pass.** `make test-bypass` (all BLOCKED) and `make test-capability-adversarial` (all DEFENDED) are green, including the new vector.

## Tasks / Subtasks

- [x] **Task 1 — add `ProjectionSeq` to `simple.EvalResult`** (AC: #1)
  - [x] Add `ProjectionSeq uint64` to `EvalResult` (`internal/refractor/ruleengine/simple/evaluator.go:45`). Document it as the JetStream stream sequence of the triggering CDC message; zero = unguarded/unknown.
  - [x] Confirm the field survives the retry capture: `capturedResult := result` (`pipeline.go:691`) is a struct copy, so the new field is captured for free.

- [x] **Task 2 — thread `msg.Sequence` into every EvalResult before write** (AC: #1)
  - [x] In `handle` (`pipeline.go:451`), `msg.Sequence` is the stream sequence. Stamp it onto each `EvalResult` returned by `evaluateForEntry` before handing to `writeResults`. Cleanest single chokepoint: stamp inside `writeResults` (`pipeline.go:628`), which already receives `msg` — set `results[i].ProjectionSeq = msg.Sequence` for every result before the write loop. This covers the inline path, the link fan-out path (`evalLinkFanOut` → `writeResults`, `:575`), and the aspect fan-out path (`evalAspectFanOut` → `writeResults`, `:596`) in one place.
  - [x] Pass the seq to the adapter at both call sites in `writeResults` (`adpt.Upsert(...)` `:637`, `adpt.Delete(...)` `:635`) and in the retry replay `WriteFn` (`pipeline.go:708`/`:710`) — there it comes from `capturedResult.ProjectionSeq` (the **original**, lower seq, which is exactly what must lose to a later real reprojection).
  - [x] **Adjacency-watch path:** `onAdjacencyUpdate` (`pipeline.go:960–967`) writes results too but is **not** message-driven (no `msg.Sequence`). See Open Questions — resolve how this path stamps the seq (likely `ProjectionSeq == 0` ⇒ adapter treats as "always-write" OR reads-current-and-bumps). Do not leave it silently writing seq 0 if seq 0 means "win unconditionally."

- [x] **Task 3 — extend the `adapter.Adapter` interface** (AC: #2)
  - [x] Add the ordering token to `Upsert`/`Delete` in `internal/refractor/adapter/adapter.go`. Recommended: `Upsert(ctx, keys, row, projectionSeq uint64)` and `Delete(ctx, keys, projectionSeq uint64)`. (Decide param vs. EvalResult-shaped arg — see Open Questions; param is the smaller blast radius.)
  - [x] Update the compile-time assertion and every implementation + call site. Implementations: `NatsKVAdapter` (`natskv.go`), `PostgresAdapter` (`postgres*.go`), and any in-test fakes/mocks under `internal/refractor/**/*_test.go` and `internal/bypass/`.

- [x] **Task 4 — make `NatsKVAdapter` enforce the guard (CAS + bounded loop)** (AC: #3, #4)
  - [x] The adapter must know **whether a given lens is guarded**. `capabilityEphemeral` and `myTasks` are guarded; the primary `capability`, `capabilityRoleIndex`, and all Postgres targets are NOT (in this story). Add a construction-time `guarded bool` (or a `GuardMode`) to `New(...)` (`natskv.go:30`), set true only for the two guarded lenses in `cmd/refractor/main.go` (the `case "capabilityEphemeral"` and `case "myTasks"` blocks, `main.go:279`/`:296`). See Open Questions on how `buildAdapter` learns guard-ness (it currently only sees the generic `lens.Rule`, not the canonical-name switch).
  - [x] **Unguarded path = today's behavior unchanged** (unconditional `Put`/`Delete`), ignoring `projectionSeq`. Do not regress the primary `capability` or `capabilityRoleIndex` lenses.
  - [x] **Guarded `Upsert`:** `Get(key)` → if found and parsed `stored.projectionSeq ≥ incoming` → drop (idempotent no-op, return nil). Else `Update(key, data, jetstream.LastRevision(entry.Revision()))`. On `jetstream.ErrKeyExists`/revision-conflict → re-`Get` and re-compare in a **bounded loop** (cap iterations, e.g. a small constant; on exhaustion return a classified error so failure handling kicks in). When the key is absent, `Create` (or `Update` with revision 0 semantics) — handle the absent→create race the same bounded way.
  - [x] **Guarded `Delete` (soft tombstone):** write `{isDeleted:true, projectionSeq:<incoming>}` under the **same CAS+bounded-loop** discipline (a stale lower-seq delete must also lose). Reuse the existing `DeleteModeSoft` tombstone-write shape (`natskv.go:91–104`) but add `projectionSeq` and route it through the CAS path. Note: the two guarded lenses today run with the default (hard) delete mode via `ErrDeleteProjection`; the guard makes the guarded path always soft-tombstone regardless of the lens's `deleteMode` (the watermark must survive). Confirm this does not change the unguarded lenses' delete semantics.
  - [x] Stamp `projectionSeq` into the **Upsert body** as well so it round-trips (the contract §6.2 shows `projectionSeq` as a top-level envelope field). The adapter writes the row as-is today; ensure the seq lands in the persisted doc (either the adapter injects it, or the envelope/EvalResult carries it into `Row`). See Open Questions — adapter-injects keeps the envelope wrappers untouched.

- [x] **Task 5 — flip the my-tasks E2E + remove the settle-wait** (AC: #5)
  - [x] `internal/refractor/refractor_mytasks_e2e_test.go`: change the vanish-on-close block (~`:213–222`) from asserting the key is **absent** to asserting the key **exists and is an `isDeleted:true` tombstone** with a `projectionSeq` ≥ the open-era projection's seq.
  - [x] Remove the `requireQuiescentRevision(t, ctx, myTasksKV, expectedKey)` call (`:211`) and delete the now-dead helper (`:229–250`). Update the file's header comment so it describes the tombstone behavior the code asserts **now** (no "renamed/was/previously" narration — house rule).
  - [x] This test currently requires the Docker stack; keep it in the same build tags / run path it already uses.

- [x] **Task 6 — adversarial Gate 3 regression test (fail-without/pass-with)** (AC: #6, #8)
  - [x] Add a `TestCapAdv_V5_*` test under `internal/bypass/` (mirror the structure of `capadv_projection_lag_test.go` and the existing `TestCapAdv_V*` tests). It must drive the **real captured-retry chain**, not a hand-rolled stale write: enqueue/replay an open-era `Upsert` for `cap.ephemeral.<actor>` (and/or `my-tasks.<actor>`), commit a close-`Delete`, then fire the retry and assert the stale replay is **rejected** (key stays a tombstone; step-3 denies / no resurrected grant).
  - [x] Wire the new vector into the `TestGate3_Report` roll-up (`internal/bypass/gate3_test.go`): add a row to the `rows` slice (Result `DEFENDED`) and update the `cleared == 4` gate count and the `%d/4` report strings to the new vector count. **This is a hardcoded count, not a length-derived one** — missing it makes the gate silently under-count. See Open Questions.
  - [x] **Prove fail-without:** before wiring the guard (or with the guard disabled), confirm the test FAILS against `main` (the stale replay resurrects the grant). After the guard lands, it PASSES. Record both runs in the Dev Agent Record.
  - [x] Confirm `make test-capability-adversarial` regenerates `gate3-report.txt` cleanly and the suite is green.

- [x] **Task 7 — verification gates** (AC: #8)
  - [x] `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`.
  - [x] `make test-bypass` (Gate 2 — all BLOCKED), `make test-capability-adversarial` (Gate 3 — all DEFENDED, including the new vector).
  - [x] Targeted packages: `go test ./internal/refractor/... ./internal/refractor/adapter/... ./internal/refractor/pipeline/... ./internal/bypass/... -count=1` (plus the my-tasks E2E, which needs the Docker stack — `make up` first).

## Dev Notes

### Where `projectionSeq` comes from (do not reinvent)

`substrate.Message` **already** carries `Sequence uint64` (`internal/substrate/consumer.go:55`), populated from `meta.Sequence.Stream` (`consumer.go:213–214`). This is exactly the JetStream stream sequence of the triggering CDC message. `handle(ctx, msg)` (`pipeline.go:451`) has it; `writeResults(ctx, msg, key, results)` (`pipeline.go:628`) has it. **Do not** use `reporter.ActiveSequence()` (`pipeline.go:695`) — that is the **rule-version** sequence (the active lens-def's seq), NOT the triggering CDC message's seq. Confusing the two would stamp the same value across all events and break ordering entirely.

### The three write paths (all must carry the seq)

1. **Inline / fan-out write loop** — `writeResults` (`pipeline.go:632–667`), `adpt.Upsert`/`adpt.Delete` at `:637`/`:635`. Reached by `handle` (`:545`), `evalLinkFanOut` (`:575`), `evalAspectFanOut` (`:596`).
2. **Retry replay** — `enqueueRetry`'s `WriteFn` (`pipeline.go:705–711`); `capturedResult` carries the **original** (lower) seq, which is the whole point.
3. **Adjacency-watch** — `onAdjacencyUpdate` (`pipeline.go:960–967`); **not** message-driven. Resolve per Open Questions before relying on it.

Stamping in `writeResults` covers paths 1 and 2 (the retry captures the already-stamped result). Path 3 needs an explicit decision.

### Adapter guard mechanism

- `jetstream.KeyValue` supports CAS via `Update(ctx, key, value, lastRevision)` (returns `ErrKeyExists` / revision mismatch on conflict) and `Create` (fails if the key exists). `Get` returns `entry.Revision()`. Use these — do not invent a lock.
- The guard is **adapter-local** and **per-lens**: only `NatsKVAdapter` for the two guarded lenses enforces. The Postgres adapter implements the wider signature as a pass-through (no guard) — Postgres targets are exempt by contract (§6.2: "not present/enforced … on Postgres targets").
- Soft-tombstone reuse: the existing `DeleteModeSoft` branch (`natskv.go:91–104`) already writes `{isDeleted:true, projectedAt:…}` via `Put`. The guarded path extends that doc with `projectionSeq` and routes the write through CAS. Keep `projectedAt` in the tombstone (existing consumers/tools may read it) and **add** `projectionSeq`.

### Step-3 / auth invariants (regression-prevention)

- Absence and `isDeleted:true` tombstone are **equivalent for authorization** — both yield no grants, so step-3 denies in both cases. There is **no step-3 behavior change** (Contract #6 §6.8). Do not add tombstone-handling to the Processor in this story; the existing absent/denied paths already cover it (the ephemeral/my-tasks readers see "no grants"). If you find a step-3 reader that would treat a tombstone as a live doc, STOP and flag it (it would be a real bug, but likely 12.1b territory).
- The `my-tasks` bucket has **no production reader yet** (only the E2E). Contract #10 §10.1 records the forward obligation that any future UI/query reader MUST skip `isDeleted` docs. No code to write for that here beyond the E2E assertion.

### Envelope wrappers — leave them alone where possible

`NewEphemeralWrapper` (`envelope.go:149`) and `NewMyTasksWrapper` (`envelope.go:244`) emit `ErrDeleteProjection` for zero-real-grants/zero-open-tasks, which the pipeline turns into an `EvalResult{Delete:true}` (`evaluate.go:134–139`, `:187–193`). The seq is stamped downstream (in `writeResults`), so the wrappers need **no change** for the delete path. The cleanest place to inject `projectionSeq` into the persisted **upsert body** is the adapter (it already has the seq param), keeping the wrappers untouched — confirm in Open Questions.

### House rules (CLAUDE.md)

- **No history/changelog comments in code.** No `// Story 12.1a …`, `// was …`, `// previously hard-delete …`. Comments describe what the code does now. (This is the single most-violated rule.)
- **Contract #1 key shapes.** Guarded keys are `cap.ephemeral.<idType>.<id>` and `my-tasks.<idType>.<id>` (built by `EphemeralKey`/`MyTasksKey`, `envelope.go:213`/`:311`). Do not alter the key shape.
- **New docs go in `/docs`** (close to the code), not `_bmad-output/`. If you add a code-doc note (e.g. a refractor component note about the guard), put it under `docs/`.
- **Sub-agents do not commit/push/branch** — leave changes in the working tree for Winston to adjudicate.

### Project Structure Notes

- All implementation lives under `internal/refractor/**` (`adapter/`, `pipeline/`, `ruleengine/simple/`), the lens wiring in `cmd/refractor/main.go`, the adversarial test in `internal/bypass/`, and the E2E in `internal/refractor/refractor_mytasks_e2e_test.go`. No new top-level packages.
- The guard config (`guarded bool`) flows from the canonical-name switch in `startPipeline` (`main.go:256–313`) but the adapter is built in `buildAdapter` (`main.go:161`), which only sees `lens.Rule`. Bridging these is the main wiring decision — see Open Questions.

### References

- [Source: _bmad-output/planning-artifacts/epics/phase-2-epics.md#Story 12.1a] — ACs (verbatim backbone), scope split vs. 12.1b.
- [Source: docs/decisions/projection-plane-decomposition.md#D-INTEGRITY] — bug chain, `projectionSeq` rationale, rejected alternatives, party-review findings #1–#7, #11.
- [Source: docs/contracts/06-capability-kv.md#6.2] — projection-write integrity guard (Phase 2 amendment, applied commit 29b9536).
- [Source: docs/contracts/06-capability-kv.md#6.3] — `projectionSeq` field spec; guarded-key list; Postgres/role-by-operation exemptions.
- [Source: docs/contracts/06-capability-kv.md#6.8] — soft tombstone carries the watermark; absence ≡ tombstone for auth.
- [Source: docs/contracts/10-orchestration-surfaces.md#10.1] — my-tasks tombstone consumer obligation (skip `isDeleted`).
- [Source: cmd/refractor/CONTRACT-AMENDMENT-REQUEST.md] — Request 4 (guard) + Request 4b (my-tasks consumer obligation).
- Code: `internal/refractor/pipeline/pipeline.go` — `handle` :451, `writeResults` :628, `enqueueRetry` :688, retry `WriteFn` :705–711, `onAdjacencyUpdate` :960–967.
- Code: `internal/refractor/failure/retry.go` — `RetryQueue.Run` (separate goroutine, single-caller invariant).
- Code: `internal/refractor/adapter/adapter.go` — `Adapter` interface; `internal/refractor/adapter/natskv.go` — `Upsert`/`Delete`/`DeleteModeSoft`; `internal/refractor/adapter/deletemode.go`.
- Code: `internal/refractor/ruleengine/simple/evaluator.go:45` — `EvalResult`.
- Code: `internal/refractor/capabilityenv/envelope.go` — `NewEphemeralWrapper` :149, `NewMyTasksWrapper` :244, `EphemeralKey` :213, `MyTasksKey` :311.
- Code: `internal/substrate/consumer.go:52–61` — `Message.Sequence`; `:213–214` — populated from `meta.Sequence.Stream`.
- Code: `cmd/refractor/main.go` — `buildAdapter` :161, `startPipeline` canonical-name switch :256–313.
- Test: `internal/refractor/refractor_mytasks_e2e_test.go` — vanish-on-close :213–222, `requireQuiescentRevision` :211/:229–250.
- Test: `internal/bypass/` — `capadv_projection_lag_test.go` (structure to mirror), `gate3_test.go` (`TestGate3_Report` roll-up, hardcoded `cleared == 4` count at :133).
- Make: `Makefile` — `test-bypass` :146, `test-capability-adversarial` :160, `verify-kernel` :62.

## Previous Story Intelligence

This is the first story of Epic 12 (12.1a). Relevant prior-art carried forward:

- **Epic 10 (just completed)** established the session-per-story loop and the inter-story credit-window gate; no code dependency, but the workflow rules in CLAUDE.md apply (Winston commits to `main`, watches CI, sub-agents don't commit).
- **Story 7.1/7.2** introduced the disjoint `cap.ephemeral.<actor>` key and the `my-tasks` lens, and surfaced **this exact resurrection race** plus the package-can't-be-added-without-core-edit problem that motivates the rest of Epic 12. The 7.2 E2E's `requireQuiescentRevision` settle-wait is the masking crutch this story removes.
- **Story 1.5.4** removed the per-operation projection-freshness gate (`projectedAt` is provenance, not a freshness ceiling) — which is *why* `projectedAt` cannot order these writes and `projectionSeq` is needed. Gate 3 vector #2 ("Projection lag window") documents the ACCEPTED-WINDOW posture; do not confuse the new vector #5 (resurrection, DEFENDED) with vector #2 (lag, accepted).

## Git Intelligence

- `29b9536` — ratified + applied the Epic 12 contract amendments (CAR Requests 4–7). **The contracts this story builds to are already frozen-and-applied.** Build to them; raise a CONTRACT-AMENDMENT-REQUEST only if a genuine gap appears (do not edit the frozen contract in-flight).
- Recent commits show the established pattern: implementation under `internal/`, adversarial proof in `internal/bypass/`, docs under `docs/`, status tracked in the story file.

## Open Questions

1. **Adapter signature: `projectionSeq uint64` param vs. `EvalResult`-shaped arg?** The epic AC allows either. A plain `uint64` param is the smaller blast radius (touches every `Adapter` impl + call site once). An `EvalResult`-shaped arg future-proofs for 12.1b but widens the interface now. **Recommendation: `uint64` param.** Confirm before implementing — it ripples through all fakes/mocks.

2. **How does `buildAdapter` learn a lens is guarded?** The guard flag is naturally known at the canonical-name switch in `startPipeline` (`main.go:256`), but the adapter is constructed in `buildAdapter` (`main.go:161`), which only receives `lens.Rule`. Options: (a) pass `guarded` into `buildAdapter` by inspecting `r.CanonicalName` inside it (`capabilityEphemeral`/`myTasks` → true); (b) build the adapter unguarded and add a `SetGuarded`/wrap step in the switch; (c) drive it from a lens-config/meta-aspect. **Leaning (a)** (smallest, keeps the canonical-name list in one file) — but flagging because it couples `buildAdapter` to canonical names. (Note: a declarative `projectionKind` aspect is the 12.3 mechanism — do NOT pull that forward here.)

3. **Where does `projectionSeq` get written into the persisted upsert body?** Contract §6.2 shows it as a top-level envelope field. Cleanest: the **adapter injects** `projectionSeq` into the marshalled doc on guarded writes (keeps `NewEphemeralWrapper`/`NewMyTasksWrapper` untouched). Alternative: thread it through the envelope params (`evaluate.go:120`/`:164` build `params`) and have the wrappers add it. **Recommendation: adapter-injects.** Confirm.

4. **Adjacency-watch path (`onAdjacencyUpdate`, `pipeline.go:960`) has no `msg.Sequence`.** It is not JetStream-message-driven. If it writes a guarded key with `projectionSeq == 0` and 0 means "lowest possible," every adj-watch write would lose to any prior real write (silently dropped) — or, if 0 means "win," it would clobber the watermark. Does the adj-watch path ever target the two guarded lenses (`capabilityEphemeral`/`myTasks`)? If not, this is moot (assert/guard against it). If it can, we need a defined seq source (e.g. read-current-and-keep, i.e. re-assert without advancing the watermark). **Needs a decision before merge** — do not leave it ambiguous.

5. **CAS bounded-loop cap + exhaustion behavior.** What iteration cap, and on exhaustion: return a `failure.CatTransient` error (so it re-enqueues to the retry queue — but that could loop forever under sustained contention) or `CatInfra` (pauses the pump)? The retry-goroutine contention is bounded in practice, but the cap + classification should be explicit. Suggest a small constant (e.g. 5) and `CatTransient` with a log — confirm.

6. **Gate 3 roll-up count is hardcoded.** `TestGate3_Report` hardcodes `cleared == 4` and `%d/4` strings (`gate3_test.go:133–137`). Adding vector #5 requires bumping these to 5. Worth flagging as a fragile spot; consider deriving the count from `len(rows)` while here (small, in-scope cleanup) — confirm whether to make that robustness change or just bump the literals.

---

## Adjudication (Winston, 2026-06-13) — all six resolved; build to these

1. **Adapter signature → `projectionSeq uint64` param** on `Upsert`/`Delete` (not an `EvalResult` arg). Smallest blast radius; the token rides outside the row body so `Delete`'s nil-row case is handled. `EvalResult` gains a `projectionSeq uint64` field so the retry-queue capture replays the *original* (lower) seq. Update every `Adapter` impl + fake/mock once. Postgres adapter: accept the param, ignore it (pass-through, no guard).

2. **Guard wiring → Option (b):** `buildAdapter` builds the adapter **unguarded** and stays generic (it must NOT learn canonical names); the existing canonical-name `switch` in `startPipeline` (`main.go:256`) flags the two guarded lenses via a `SetGuarded(true)` (or equivalent wrap) on the NatsKV adapter. Rationale: keep ALL canonical-name coupling in the one switch that **12.4 deletes** — do not seed new canonical-name knowledge into `buildAdapter`, and do NOT pull forward the 12.3 declarative `projectionKind` mechanism.

3. **`projectionSeq` into the body → adapter-injects** on guarded writes. `NewEphemeralWrapper`/`NewMyTasksWrapper` stay untouched. The guard owner (NatsKV adapter) is the cohesive place to stamp the top-level field.

4. **Adjacency-watch path (`handleAdjUpdate`, started unconditionally at `pipeline.go:315`) — CONFIRMED a second resurrection vector for guarded keys.** It writes via `adpt.Upsert`/`adpt.Delete` with no stream sequence. Binding rule: **a guarded watermark may be advanced/cleared ONLY by a stream-sequenced write.** Therefore the adj-watch path, on a guarded key, must **never resurrect a tombstone or absent key, and never regress/advance the watermark**:
   - **First, verify redundancy:** the stream consumer's `evaluateLinkFanOut` (carries `msg.Metadata().Sequence.Stream`) already reprojects affected actors on link CDC for the guarded lenses. If the adj-watch path is fully redundant for `capabilityEphemeral`/`myTasks` (expected), **skip guarded-key writes on the adj-watch path entirely** and assert/log if one is ever produced.
   - **If not fully redundant:** write in **preserve-watermark mode** — on an existing live key, refresh body while keeping the stored `projectionSeq` unchanged; on an absent key OR a tombstone, **skip** (no create/resurrect — absence/tombstone is an authoritative deny only a stream write may lift).
   - **Gate 3:** add an adversarial vector proving an adj-watch reprojection cannot resurrect a closed/revoked guarded key (parallel to the retry-queue vector).

5. **CAS bounded loop → cap 8; on exhaustion return `failure.CatTransient`** (re-enqueue — the retry queue's existing backoff + DLQ ceiling bounds it; no true infinite loop) with a `warn` log naming the key. Do NOT use `CatInfra` (would pause the whole pump on a single hot key).

6. **Gate 3 count → derive from `len(rows)`** (the small in-scope robustness fix) so vector #5 doesn't silently under-count. If the test structure makes that awkward, bump the literals to `5` and leave a one-line comment — but prefer the derived count.

---

## Dev Agent Record (Amelia)

### Implementation summary

Built to Winston's six adjudications exactly. The guard is adapter-local and per-lens: `NatsKVAdapter` gains a `guarded bool` (off by default; `SetGuarded(true)` flips it), the canonical-name switch in `cmd/refractor/main.go` enables it for `capabilityEphemeral` + `myTasks` via a small `enableProjectionGuard` helper (keeping `buildAdapter` free of canonical-name knowledge — adjudication #2). `projectionSeq` is plumbed end-to-end: `simple.EvalResult` gains a `ProjectionSeq uint64` field; `writeResults` stamps `msg.Sequence` onto every result before the write loop (one chokepoint covering inline + link fan-out + aspect fan-out); the retry-replay `WriteFn` replays `capturedResult.ProjectionSeq` (the original lower seq). Guarded writes are CAS (`Get` → drop if `stored ≥ incoming` → `Update`/`Create` with revision precondition, bounded loop cap 8 reusing `substrate.IsRevisionConflict`); a guarded `Delete` is always a soft tombstone `{isDeleted, projectedAt, projectionSeq}`; the adapter injects `projectionSeq` into the upsert body (wrappers untouched — adjudication #3). On CAS exhaustion the adapter warns naming the key and returns a plain error, which `failure.Classify` routes as `CatTransient` (adjudication #5 — no `CatInfra`, no pump pause).

### Open Question #4 resolution — adjacency-watch path (skip, with redundancy evidence)

Resolved as **skip guarded-key writes entirely** on the adj-watch path (adjudication #4, first branch). Redundancy evidence: `handleAdjUpdate` (pipeline.go) point-reads the Core KV node and calls the same `evaluateForEntry` → `evaluateFanOut`/`reprojectActors` machinery the stream consumer uses; for the guarded lenses every adjacency entry is written by the bootstrapper in response to the *same* link/aspect CDC events that the stream consumer's `evalLinkFanOut`/`evalAspectFanOut` already reproject — carrying `msg.Sequence`. The adj-watch trigger is therefore a redundant secondary reprojection of actors the stream path already covers with a real stream sequence. `handleAdjUpdate` now type-asserts `interface{ Guarded() bool }` on the active adapter and, when guarded, logs and returns before any write. The monotonic guard is the backstop even if that skip were ever bypassed: an adj-watch write would carry `projectionSeq == 0`, which is the lowest possible watermark and is rejected against any non-zero stored watermark (proven by `TestCapAdv_V5_AdjWatch_CannotAdvanceWatermark`). Binding rule honored: a guarded watermark is advanced/cleared only by a stream-sequenced write.

### Gate 3 — fail-without / pass-with proof

- `TestCapAdv_V5_StaleReplay_FailsWithoutGuard` drives the captured-retry chain (open-era Upsert seq 100 → close Delete seq 200 → replay Upsert seq 100) against an **unguarded** adapter — the grant resurrects (asserts `liveGrantResurrected == true`). This is the on-`main` behavior, pinned.
- `TestCapAdv_V5_StaleReplay_DefendedWithGuard` runs the identical chain against the **guarded** adapter — the stale replay is rejected; the key stays an `isDeleted` tombstone with the close-era watermark (200).
- `TestCapAdv_V5_AdjWatch_CannotAdvanceWatermark` proves a seq-0 (non-stream-sequenced) write cannot resurrect a tombstoned guarded key.
- All three wired into `TestGate3_Report` as vector #5 (DEFENDED); the gate count is now derived from `len(rows)` (adjudication #6), reported as **5/5 cleared (4 DEFENDED, 1 ACCEPTED-WINDOW)**.

### Verification (all run; outputs captured)

- `go build ./...` — exit 0.
- `make vet` — exit 0.
- `golangci-lint run ./...` — `0 issues.`
- `make verify-kernel` — `ALL ASSERTIONS PASSED`.
- `make test-bypass` (Gate 2) — `PHASE 1 GATE 2: PASSED (4/4 BLOCKED)`.
- `make test-capability-adversarial` (Gate 3) — `PHASE 1 GATE 3: PASSED (5/5 cleared — 4 DEFENDED, 1 ACCEPTED-WINDOW)`; `gate3-report.txt` regenerated cleanly with vector #5.
- `go test ./internal/refractor/... -count=1` — all packages `ok`, including `TestRefractor_MyTasksLens_E2E` (flipped to tombstone assertion; settle-wait removed).
- New unit tests: `TestNatsKVAdapter_Guarded_*` (5) + `TestNatsKVAdapter_Unguarded_IgnoresProjectionSeq` — all PASS.

### Deviations / notes

- Reused `substrate.IsRevisionConflict` for CAS-conflict detection rather than hand-rolling it (canonical helper; handles older NATS server error-string forms too). No import cycle (`substrate` does not depend on `adapter`).
- CAS-exhaustion returns a plain error rather than importing `failure` into the adapter — `failure.Classify` defaults unrecognized errors to `CatTransient`, which is exactly the adjudicated routing, and keeps the adapter package free of a `failure` dependency.
- Per-lens guard wiring uses a type-assertion in `handleAdjUpdate` (`interface{ Guarded() bool }`) and a concrete `*adapter.NatsKVAdapter` assertion in `enableProjectionGuard`; the `Adapter` interface itself was kept minimal (only `Upsert`/`Delete` gained the seq param).

### File List

- `internal/refractor/ruleengine/simple/evaluator.go` — added `ProjectionSeq uint64` to `EvalResult`.
- `internal/refractor/adapter/adapter.go` — `Upsert`/`Delete` interface signatures gain `projectionSeq uint64`.
- `internal/refractor/adapter/natskv.go` — `guarded` flag + `SetGuarded`/`Guarded`; guarded CAS Upsert/Delete; `guardedWrite`/`guardedBody`/`storedProjectionSeq`; soft-tombstone-with-watermark; seq injection into body.
- `internal/refractor/adapter/postgres.go` — `Upsert`/`Delete` accept and ignore `projectionSeq` (pass-through, documented).
- `internal/refractor/pipeline/pipeline.go` — stamp `msg.Sequence` in `writeResults`; pass seq to adapter at all write sites + retry `WriteFn`; skip guarded-key writes on the adjacency-watch path.
- `internal/refractor/fixture/runner.go` — pass `result.ProjectionSeq` through fixture writes.
- `cmd/refractor/main.go` — `enableProjectionGuard` helper; enable guard for `capabilityEphemeral` + `myTasks`.
- `internal/refractor/refractor_mytasks_e2e_test.go` — `SetGuarded(true)`; flipped vanish-on-close to tombstone assertion; removed `requireQuiescentRevision` call + helper; refreshed header comment.
- `internal/bypass/capadv_projection_resurrection_test.go` — **new** Gate 3 vector #5 (fail-without/pass-with + adj-watch).
- `internal/bypass/gate3_test.go` — vector #5 row; gate count derived from `len(rows)`.
- `internal/refractor/adapter/natskv_test.go` — guarded-adapter unit tests; existing call sites updated for the new seq param.
- `internal/refractor/adapter/postgres_test.go` — call sites updated for the new seq param.
- `internal/refractor/pipeline/pipeline_test.go`, `internal/refractor/pipeline/supervisor_adapt_internal_test.go` — fake adapters updated to the new interface signature.
