# Story 13.6 — externalTask deadline + completion symmetry (corrects 13.2 §10.6)

**Status:** done
**Epic:** 13 — External I/O Bridge (orchestration core)
**Tier:** Opus — guarded engine (`internal/loom`), correctness/wedge plane. Review: full 3-layer adversarial + `make verify-kernel`.
**Sequencing:** lands **before Epic 14 (14.4)** — it changes the externalTask completion convention the real `replyOp` DDL (14.4) must build to. It is NOT gated behind 13.5.
**Commit posture (this story only):** **DO NOT COMMIT ANYTHING.** Andrew is reviewing the whole change (contract amendment + this story + the code) uncommitted. The dev leaves the working tree dirty; Winston does **not** commit at the end either.

---

## 0. Why this story exists

Story 13.2 implemented the `externalTask` deadline on the **systemOp** model (the deadline's "instanceOp committed → **advance** the cursor" branch). That is wrong: an `instanceOp` commit only means *the claim vertex was created and the `external.<adapter>` event emitted* — **not** that the external call completed. On any slow/dead bridge or a `StepTimeout` shorter than the external round-trip, the deadline fires, sees the committed instanceOp, and **advances the cursor before the result lands** — breaking wait-for-result (a later outcome-branching step reads stale/absent data).

**The fix (already applied to the FROZEN contract + component docs, uncommitted — build to it):** `externalTask` is symmetric to a **`userTask`**, not a systemOp. Contract #10 §10.5/§10.6 + `docs/components/{loom,bridge}.md` were amended 2026-06-18 (see the §10.6 "externalTask creation path" subsection + the revision-history entry). This story implements the engine + test changes to match. **Read the amended contract first — it is the spec.**

The amendment also corrected the stale "`externalRef` is the full `vtx.<type>.<id>` key" §10.6 wording to the **opaque bare-NanoID handle** Loom actually mints (the Story 13.2 §0 resolution). No engine change is needed for that wording fix — the code already uses the bare handle — but the dev should be aware the contract now matches the code on this point.

---

## 1. The design (adjudicated — build to THIS; do not re-litigate)

externalTask becomes the **userTask shape** for both the deadline and the completion:

| | userTask (precedent) | externalTask (this story) |
|---|---|---|
| dispatch (machine action) | `CreateTask` | `instanceOp` |
| bounded creation-deadline on | task-vertex creation | **`instanceOp` commit** |
| probe to detect "created" | `taskVertexExists(vtx.task.<taskId>)` | **`trackerExists(vtx.op.<instanceOpRequestId>)`** (Loom cannot read the package-typed claim vertex, so it probes the instanceOp's own Contract #4 tracker) |
| on "created" | **disarm → unbounded** human wait | **disarm → unbounded** bridge wait |
| completion signal | `orchestration.taskCompleted{taskKey}` (commit-path auto-injected) | **`orchestration.externalTaskCompleted{externalRef}`** (emitted by the `replyOp` DDL) |
| advances on | the completion event ONLY | the completion event ONLY — **never the deadline** |
| completionDomains | `["orchestration"]` | `["orchestration"]` |
| rejected/lost dispatch | `FailPattern` (FR29) | `FailPattern` (FR29) |

The completion event is **emitted by the `replyOp` DDL** (not platform-injected like `taskCompleted`) because the `replyOp` is a purpose-built completion op, unlike a userTask's oblivious business op — outcome symmetric, mechanism fits the completer. The `replyOp` DDL is **14.4's** work; in this story it is a **test fixture** (see § 3).

---

## 2. Code changes (exact)

### 2.1 `internal/loom/engine.go` — `submitExternalTask` (~line 941–974)
- Change the deadline armed in the transition from `e.cfg.StepTimeout` to **`e.cfg.CreateTaskTimeout`** (line ~966). The deadline now bounds the **`instanceOp` submission** (a machine action), exactly like a userTask's `CreateTask` creation-deadline — not the external round-trip. Update the adjacent comment to say so (present-tense; no history narration).

### 2.2 `internal/loom/engine.go` — `onExternalTaskDeadline` (~line 1238–1272)
Rewrite so it mirrors `onUserTaskDeadline` (~1175–1219). The **only behavioral change** is the first branch:
- **tracker present** (`trackerExists(opRequestID)`) → the `instanceOp` committed: the claim vertex exists and the `external.<adapter>` event was emitted, so the bridge will (eventually, at-least-once + idempotent) reply → **`disarmDeadline`** (cursor/token untouched), log "instanceOp committed; disarming creation-deadline for unbounded bridge wait", and stop. **DO NOT `advance`.** (This is the bug fix — it replaces the old "advance + check completionDomains" branch.)
- **tracker absent, `outboxExists(opRequestID)`** → relay not yet delivered → `rearmDeadline(..., e.cfg.CreateTaskTimeout)` (use `CreateTaskTimeout`, matching the arm). Do not fail.
- **tracker absent, outbox absent** → `instanceOp` rejected/lost → `fail(...)` on the pending handle token (FR29; keep the existing `FailPattern` path + alert).
- Rewrite the function doc comment: it currently states "there is no disarm-and-go-unbounded branch" — that is now FALSE; the fix ADDS exactly that branch. Describe the userTask-symmetric behavior (bounded creation-deadline → disarm on instanceOp-commit → unbounded bridge wait; advance only on `orchestration.externalTaskCompleted`). No "was/previously" narration.
- Keep the CAS-on-running idempotency (advance/fail verify the pending token) — unchanged.

### 2.3 `internal/loom/pattern.go` — load-time warn (mirror userTask)
- Add `hasExternalTaskStep()` (mirror `hasUserTaskStep`, ~line 114) and `externalTaskCompletionUnobservable()` (mirror `userTaskCompletionUnobservable`, ~line 127): a pattern with an `externalTask` step whose effective `Domains()` omits the `orchestration` domain → true. (The `orchestration.externalTaskCompleted` event lands on `orchestration`; a pattern that omits it would never observe its externalTask completions — the same misconfiguration the userTask warn guards.) Reuse `userTaskCompletionDomain` ("orchestration") — it is the same constant; consider renaming it to a neutral `orchestrationCompletionDomain` if both callers read cleaner, or just reuse it as-is.

### 2.4 `internal/loom/source.go` — wire the warn (~line 224)
- Where `dispatchSpec` logs the `userTaskCompletionUnobservable` warn, add the symmetric `externalTaskCompletionUnobservable` warn (its own loud `Warn` log line naming the pattern + its domains). Same posture: warn, do not reject.

### 2.5 No change needed
- `correlationKeys` / `eventBody` already carry `payload.externalRef` (13.2) — the `orchestration.externalTaskCompleted` event correlates through the existing third key on the existing `loom-orchestration` consumer. **Do not add a new correlation key or consumer.**
- `deriveInstanceID` / the bare-handle mint — unchanged.

---

## 3. Test changes (the 13.2 externalTask fixtures + tests move to the userTask model)

The 13.2 externalTask e2e fixtures live in `internal/loom/loom_e2e_test.go` (the `fakeProcessor`) and `internal/loom/external_e2e_test.go`. Update them:

- **Fixture `replyOp`:** instead of emitting whatever package-domain completion event it emitted before, the fixture `replyOp` now emits **`orchestration.externalTaskCompleted`** carrying **`payload.externalRef = <handle>`** (on the `orchestration` domain), in addition to recording the outcome aspect (keep the D5 aspect assertion). The externalTask test pattern declares **`completionDomains: ["orchestration"]`**.
- **REWRITE the deadline "committed but no reply" test** (13.2's `TestExternalE2E_CommittedNoReplyAdvancesViaProbe` or equivalent): it currently asserts the cursor **advances** when the instanceOp committed but no reply arrived. It must now assert the deadline **disarms** → the instance stays **`running`** at the **same cursor** (NOT advanced, NOT failed), and a subsequent deadline marker is a no-op (unbounded wait). Rename it accordingly (e.g. `TestExternalE2E_CommittedNoReply_DisarmsToUnboundedWait`). **This is the test that pins the bug fix** — it must fail against the old "advance" behavior.
- **Happy path:** advance happens **only** when `orchestration.externalTaskCompleted{externalRef}` is delivered (after the unbounded wait). Assert the full park → instanceOp-commit → (deadline disarms) → externalTaskCompleted → advance loop, with the **non-`service`** `vtx.widget.<handle>` fixture type (invariant a — keep it).
- **Keep:** rejected `instanceOp` → `FailPattern`; not-yet-relayed (outbox present) → re-arm.
- **New unit test:** `externalTaskCompletionUnobservable` — a pattern with an `externalTask` step + `completionDomains` omitting `orchestration` → the warn fires (mirror the userTask warn test). And `hasExternalTaskStep` true/false.
- **Idempotency:** redelivered `orchestration.externalTaskCompleted` after advance → no second advance (pointer-presence guard) — keep/confirm.

---

## 4. Required reading (DS does the deep reads)
- **The amended contract (the spec):** `docs/contracts/10-orchestration-surfaces.md` §10.6 — the **externalTask correlation row**, the new **"externalTask creation path — bounded creation-deadline + instanceOp-tracker probe"** subsection, and the §10.5 **"Completion is symmetric to a userTask"** bullet. Plus the 2026-06-18 revision-history entry (the rationale).
- The **userTask precedent** you mirror: `internal/loom/engine.go` `onUserTaskDeadline` (~1175–1219), `submitUserTask` (the `CreateTaskTimeout` arm), `internal/loom/pattern.go` `hasUserTaskStep`/`userTaskCompletionUnobservable`, `internal/loom/source.go` the `userTaskCompletionUnobservable` warn site.
- The **current externalTask code** you change: `submitExternalTask` (~941), `onExternalTaskDeadline` (~1238), `internal/loom/state.go` `disarmDeadline`/`rearmDeadline`.
- The **13.2 fixtures/tests** you update: `internal/loom/loom_e2e_test.go` (`fakeProcessor`), `internal/loom/external_e2e_test.go`.
- `docs/components/loom.md` "External steps (`externalTask`)" (already amended — the code must match the page).

---

## 5. Verification gates (run in the foreground; record each + result)
- `go build ./...`
- `make vet`
- `golangci-lint run ./...`
- `make verify-kernel` (guarded engine — prove no regression)
- `go test ./internal/loom/... ./internal/pkgmgr/... -count=1`
- The full 3-layer adversarial review is Winston's gate (correctness/wedge plane).

---

## 6. Closing summary (DS appends; DO NOT COMMIT)
Deliverables vs § 2/§ 3; exact files changed (`git status --short`); test count (incl. the rewritten deadline test); every gate + result; confirmation the rewritten deadline test FAILS against the old advance-behavior (i.e. it genuinely pins the fix); any deviation; any open question. **Leave everything uncommitted.**

### Completion notes (Amelia, 2026-06-18)

**Deliverables vs §2/§3 — all met.**

§2 code (all in `internal/loom`):
- **§2.1** `submitExternalTask` (engine.go) now arms `e.cfg.CreateTaskTimeout` (was `StepTimeout`) in the transition; doc comment + the inline arm comment rewritten to the bounded-creation framing (present tense, no history narration). The `CreateTaskTimeout` config field doc was broadened to cover both async-completer kinds (userTask + externalTask).
- **§2.2** `onExternalTaskDeadline` (engine.go) rewritten to mirror `onUserTaskDeadline`: **tracker present → `disarmDeadline`** (unbounded bridge wait; NO advance — the behavioral fix); tracker absent + outbox present → `rearmDeadline(CreateTaskTimeout)`; neither → `fail` (FR29 / FailPattern). Function doc comment fully rewritten to the userTask-symmetric behavior; the `onDeadline` dispatch doc comment updated to match. CAS-on-running idempotency unchanged (fail path verifies the pending token).
- **§2.3** `pattern.go`: added `hasExternalTaskStep()` + `externalTaskCompletionUnobservable()` mirroring the userTask pair. Renamed `userTaskCompletionDomain` → `orchestrationCompletionDomain` (neutral; both callers read cleaner) and updated its doc comment + both callers.
- **§2.4** `source.go`: added the symmetric `externalTaskCompletionUnobservable` Warn next to the userTask warn (loud Warn naming the pattern + its domains; pattern not rejected).
- **§2.5** No change to `correlationKeys`/`eventBody` (confirmed they already extract `payload.externalRef` as the third key) or `deriveInstanceID`.

§3 tests (`internal/loom`):
- `fakeProcessor` replyOp (loom_e2e_test.go) now emits **`orchestration.externalTaskCompleted`** carrying `payload.externalRef = handle` (the `fixtureReplyEvent` constant moved from `widget.resolved` → `orchestration.externalTaskCompleted`); the outcome aspect + D5 assertions are kept.
- `externalPattern` declares **`completionDomains: ["orchestration"]`** (was `["widget"]`).
- **Rewrote** `TestExternalE2E_CommittedNoReplyAdvancesViaProbe` → **`TestExternalE2E_CommittedNoReply_DisarmsToUnboundedWait`**: asserts the deadline **disarms** → instance stays `running` at cursor 0 (not advanced, not failed), no `patternCompleted`/`patternFailed` announced, then the bridge's later `externalTaskCompleted` advances it (proving the wait was unbounded, not lost).
- Happy path (`TestExternalE2E_RunsToCompletion`) advances only on `orchestration.externalTaskCompleted`, non-`service` `vtx.widget.<handle>` fixture type kept; idempotency (redelivered completion → no second advance) kept.
- `TestExternalE2E_RejectedInstanceOpFails` and `TestExternalE2E_NotYetRelayedRearms` switched their short-deadline config from `StepTimeout` → `CreateTaskTimeout` (matching the new arm) so their deadlines still fire promptly.
- New unit tests in pattern_test.go: `TestHasExternalTaskStep` (true/false) and `TestExternalTaskCompletionUnobservable` (warn fires when an externalTask pattern omits orchestration).

**Exact files changed (`git status --short`)** — my code changes are the loom files only; the docs + story file were already-uncommitted before I started (not touched by me):
```
 M docs/components/bridge.md                       (pre-existing — contract amendment, untouched by me)
 M docs/components/loom.md                         (pre-existing — contract amendment, untouched by me)
 M docs/contracts/10-orchestration-surfaces.md     (pre-existing — FROZEN amendment, untouched by me)
 M internal/loom/engine.go                          (my change)
 M internal/loom/external_e2e_test.go               (my change)
 M internal/loom/loom_e2e_test.go                   (my change)
 M internal/loom/pattern.go                         (my change)
 M internal/loom/pattern_test.go                    (my change)
 M internal/loom/source.go                          (my change)
?? _bmad-output/implementation-artifacts/story-13.6-externaltask-deadline-symmetry.md  (this story file)
```

**Test count:** `internal/loom` 161 test funcs (incl. the rewritten deadline test + 2 new unit tests), `internal/pkgmgr` 77. Both packages green.

**Gates (foreground, each PASS):**
- `go build ./...` → PASS
- `make vet` → PASS
- `golangci-lint run ./...` → PASS (0 issues)
- `make verify-kernel` → PASS (ALL ASSERTIONS PASSED)
- `go test ./internal/loom/... ./internal/pkgmgr/... -count=1` → PASS (loom 55.2s, pkgmgr 0.98s)

**Fix is pinned (verified):** with `onExternalTaskDeadline`'s tracker-present branch temporarily reverted to the old `advance`, `TestExternalE2E_CommittedNoReply_DisarmsToUnboundedWait` **FAILED** (`status` got `complete`, expected `running`; the temp "advance via deadline probe" log fired). The fix was then restored and the test passes. The rewritten test genuinely pins the disarm behavior.

**Deviations:** none material. Two micro-extensions beyond the literal §2 list, both within scope: (1) renamed `userTaskCompletionDomain` → `orchestrationCompletionDomain` (the story explicitly offered this option for two callers); (2) updated the `onDeadline` dispatch doc comment and the `CreateTaskTimeout` config-field doc comment so they no longer describe the old externalTask=systemOp framing (present-tense, no history narration).

**Open questions:** none new. Q1 (CreateTaskTimeout reuse) and Q2 (externalTaskCompleted eventType DDL registration is 14.4's concern) stand as written.

**Left entirely uncommitted** per the story's commit posture.

---

## Open Questions
- **Q1 — `CreateTaskTimeout` reuse vs a dedicated `ExternalTaskCreateTimeout`.** This story reuses `CreateTaskTimeout` (the bounded machine-action creation-wait — identical semantics for the instanceOp commit). If a future deployment needs a different bound for external vs task creation, a dedicated config field can be added then; not now.
- **Q2 — orchestration.externalTaskCompleted eventType DDL.** The event is package data emitted by the `replyOp` DDL (14.4). Whether it also gets a `meta.ddl.eventType` registration (like `orchestration.taskCompleted`, if that is registered) is a **14.4** concern; 13.6 only consumes the event (via the existing `payload.externalRef` correlation) and tests it with a fixture. Flag for the 14.4 author.
