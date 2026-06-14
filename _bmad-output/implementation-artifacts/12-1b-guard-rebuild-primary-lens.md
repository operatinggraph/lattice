# Story 12.1b: Guard ↔ rebuild reconciliation + primary capability lens (D-INTEGRITY pt.2)

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the platform operator,
I want a rebuild to correctly restore guarded projections despite the monotonic write-guard, and the primary `capability` lens to be guarded with the same mechanism,
so that the integrity guard never silently prevents a rebuild from rewriting live state, and a retried/reordered stale write can never resurrect a revoked grant on the *primary* `cap.<actor>` doc either.

## Context — what 12.1a left for 12.1b (read before implementing)

12.1a (commits `0d8063c` + `6c19f3d`, Status `done`) landed the monotonic projection-write guard on the **two at-risk lenses only** — `capabilityEphemeral` (`cap.ephemeral.<actor>`, security plane) and `myTasks` (`my-tasks.<actor>`, correctness plane). The mechanism is complete and frozen-to-contract:

- `simple.EvalResult` carries `ProjectionSeq uint64`; the pipeline stamps `msg.Sequence` onto every result in `writeResults`; the retry-replay `WriteFn` replays `capturedResult.ProjectionSeq` (the original lower seq).
- `adapter.Adapter.Upsert`/`Delete` carry a `projectionSeq uint64` param. Only `NatsKVAdapter` enforces; Postgres is a documented pass-through.
- `NatsKVAdapter` has a `guarded bool` (off by default), flipped per-lens by `SetGuarded(true)`. Guarded writes go through `guardedWrite` — CAS (`Get` → drop if `stored ≥ incoming` → `Update`/`Create` with revision precondition, bounded loop cap `guardCASMaxAttempts = 8`, reusing `substrate.IsRevisionConflict`). A guarded `Delete` is always a soft tombstone `{isDeleted:true, projectedAt, projectionSeq}`. `projectionSeq` is injected into the upsert body (`guardedBody`). A seq-0 write is dropped as a fail-closed no-op.
- The canonical-name switch in `cmd/refractor/main.go` wires the guard via `enableProjectionGuard(adpt, r.ID)` — called in `case "capabilityEphemeral":` (`main.go:297`) and `case "myTasks":` (`main.go:322`). **`enableProjectionGuard` fail-closes** if the adapter is not `*NatsKVAdapter`.

**12.1a deliberately deferred two things to this story** (its Scope section, "OUT of scope (these are 12.1b)"):

1. **Guard ↔ `Rebuild` reconciliation.** 12.1a's own scope note: *"Do not attempt to make `Rebuild(truncate=false)` work against a guarded bucket here — that interaction is 12.1b's whole subject."* The guard makes `Rebuild(truncate=false)` replay historical **lower-seq** events that the guard rejects against live **high-seq** watermarks → the rebuild silently restores nothing (rejected-write holes). This story resolves it with **one explicit, tested rule**.

2. **The primary `capability` lens guard.** 12.1a touched only the two disjoint package-owned keys. The primary `cap.<actor>` doc (the fan-out aggregate over identity/roles/services) is **not** guarded today: `case "capability":` (`main.go:257`) does **not** call `enableProjectionGuard`. The same captured-retry resurrection chain is reachable on `cap.<actor>` — and that doc is the *primary* security surface step-3 reads. This story extends the same CAS+tombstone guard to it.

**`capabilityRoleIndex` is explicitly EXCLUDED** from the guard family — it is keyed by `operationType`, not actor (`cap.role-by-operation.<op>`, an operation-aggregate via `NewRoleIndexWrapper`), with a different resurrection profile. This story states the exclusion in an AC and does not hand-wave it.

The contract amendments this story builds to are **already ratified and APPLIED** (commit `29b9536`): Contract #6 §6.2 (guard, including the *"Rebuild interaction (Story 12.1b)"* bullet), §6.3 (`projectionSeq` field; guarded-key list including `cap.<actor>`; `cap.role-by-operation` exemption), §6.8 (soft tombstone carries the watermark). This story **builds to** the frozen contract; it does **not** edit it.

## The rebuild-reconciliation rule (DECIDED — force-truncate on a guarded bucket)

The epic AC offers two options; **this story picks force-truncate** and ACs it. The decision and its evidence:

- **Current code is doubly broken for guarded NATS-KV buckets.** `NatsKVAdapter` does **NOT** implement `adapter.Truncater` today (only the interface exists, in `adapter.go:28`; no `func (a *NatsKVAdapter) Truncate`). So `Pipeline.Rebuild(ctx, truncate)` (`pipeline.go:357`) behaves as follows on a guarded capability bucket:
  - `truncate=false` → replays historical lower-seq events; the guard rejects each against the live high-seq watermark → **restores nothing** (silent holes).
  - `truncate=true` → the truncate branch (`pipeline.go:371-382`) finds the adapter does **not** satisfy `Truncater`, logs *"truncate=true but adapter does not implement Truncater; skipping"*, and proceeds **as if `truncate=false`** → same silent-holes failure.
- **Force-truncate resolves both with one mechanism.** Implement `Truncate` on `NatsKVAdapter` (purge every key in the bucket, **clearing the watermarks with the data**) and make a guarded-bucket rebuild **force `truncate=true`**. After truncation, the stream replays in seq order, **the first replay write wins** (empty bucket → `Create` succeeds; subsequent higher-seq writes monotonically advance), and the steady state is byte-identical to a from-scratch projection. This is exactly the contract's *"a rebuild replays in stream order → highest-seq write wins → identical steady state"* (§6.2) and *"guarded buckets force `truncate=true` (watermark cleared with the data)"* (§6.2 Rebuild-interaction bullet).
- **Why force-truncate over a bypass flag.** A bypass flag would have to be a per-adapter mutable bypass-state that the guard reads on every write. But the **retry-queue goroutine writes concurrently** with the rebuild replay (this is the *exact* race 12.1a's CAS loop exists to handle). A bypass window that suppresses the guard would let a stale captured retry land **during** the rebuild and survive — reopening the resurrection window the guard closes, with no monotonic protection during the window. Force-truncate keeps the guard **always on** (no bypass state, no window) and is strictly simpler: one new `Truncate` method + one force-true rule. It also matches the contract's stated default.
- **Definition of "guarded bucket" for the force rule.** A bucket is guarded iff its adapter reports `Guarded() == true` (the existing 12.1a accessor, `natskv.go:69`). The rebuild path consults that — it does **not** learn lens canonical names (keeping the 12.4-deletable canonical-name knowledge in the one switch).

**Outcome guaranteed by the rule:** a `Rebuild` on a guarded bucket either (a) runs with `truncate` forced true and **correctly restores every key** (post-rebuild projection == from-scratch projection, no holes), so it never silently restores nothing. A `Rebuild(truncate=false)` request on a guarded bucket is **not** honored as-requested — it is upgraded to `truncate=true` (the operator's intent "rebuild this bucket" is preserved; the no-data-loss footgun is removed). This satisfies the epic AC's *"must EITHER be rejected with a clear error OR correctly restore every key"* via the "correctly restore" branch.

## Acceptance Criteria

(Verbatim backbone from `phase-2-epics.md` § "Story 12.1b", re-grounded to the current tree. Epic line refs are stale — the re-grounded refs below are authoritative.)

1. **The guard ↔ rebuild conflict is resolved by one explicit, tested rule: force-truncate on a guarded bucket.** `NatsKVAdapter` implements `adapter.Truncater` (purge all keys in the target bucket, clearing the `projectionSeq` watermarks along with the data). `Pipeline.Rebuild(ctx, truncate)` (`pipeline.go:357`), when the active adapter reports `Guarded() == true`, **forces `truncate=true`** regardless of the requested value, so the replay starts from an empty bucket and the highest-seq write wins — no rejected-write holes. The force decision keys off `Guarded()` (or `Truncater` presence + guarded), **not** on a lens canonical name.

2. **A `Rebuild(truncate=false)` on a guarded bucket correctly restores every key (the AC's "correctly restore" branch).** It is **not** silently a no-op and does **not** restore nothing. The forced truncate clears the stale high-seq watermarks so the historical lower-seq replays land cleanly. (We choose the "correctly restore" branch over "reject with a clear error"; the rationale is recorded in the rule section above.)

3. **A test drives a rebuild of `capabilityEphemeral`/`my-tasks` after live traffic and asserts the post-rebuild projection equals a from-scratch projection (no rejected-write holes).** Concretely: seed a guarded bucket with live high-seq watermark state (an upsert at a high seq, and/or a tombstone), then exercise the rebuild path (force-truncate + replay of the historical lower-seq events) and assert the bucket's final contents are **identical** to projecting those same events into a fresh empty bucket — every key present, none missing, none stuck as a stale tombstone. The test must FAIL if the force-truncate rule is removed (rebuild leaves holes) and PASS with it.

4. **The guard is extended to the primary `capability` lens** (`cap.<actor>`, the fan-out aggregate over identity/roles/services). `case "capability":` (`main.go:257`) calls `enableProjectionGuard(adpt, r.ID)` — mirroring the `capabilityEphemeral` (`:297`) and `myTasks` (`:322`) wiring — so the **same** CAS + soft-tombstone mechanism (no new adapter code) protects the primary per-actor capability doc. No new guard machinery is written: this is a one-call wiring change reusing 12.1a's `SetGuarded`/`guardedWrite`.

5. **`capabilityRoleIndex` is explicitly EXCLUDED from the guard family, decided not hand-waved.** `case "capabilityRoleIndex":` (`main.go:269`) does **not** call `enableProjectionGuard` and is left unguarded. The exclusion is recorded **in code** (a comment on the `capabilityRoleIndex` case stating *why*: keyed by `operationType` — `cap.role-by-operation.<op>` — it is an operation-aggregate, not an actor-aggregate, with a different resurrection profile; per Contract #6 §6.2/§6.3 it is NOT a guarded key) and asserted by a test that the role-index adapter reports `Guarded() == false`. The comment describes what the code does now (no history/changelog narration — house rule).

6. **A fail-without/pass-with adversarial regression test proves the primary `capability` lens is now defended.** Mirror 12.1a's vector #5 structure (`internal/bypass/capadv_projection_resurrection_test.go`): drive the captured-retry chain against the **primary** `cap.<actor>` key (open-era upsert seq N → close-era delete seq M>N → replay open-era upsert seq N). Against an **unguarded** adapter the grant resurrects (fail-without, pins main-without-this-story); against the **guarded** adapter it is rejected (the key stays a tombstone at the close-era watermark). The defended case lands in the **Gate 3 (DEFENDED)** suite and is wired into the `TestGate3_Report` roll-up (`internal/bypass/gate3_test.go`) — either as a new vector row OR by extending vector #5's coverage to the primary key, **stated explicitly** in the test. The gate count is already derived from `len(rows)` (12.1a adjudication #6), so a new row updates the gate automatically.

7. **The change is built to the already-applied Contract #6 §6.2/§6.3/§6.8 amendment** (commit `29b9536`): §6.2's *"Rebuild interaction (Story 12.1b)"* bullet (force-truncate OR bypass — this story picks force-truncate), §6.3's guarded-key list (`cap.<actor>` IS a guarded key; `cap.role-by-operation.<op>` is NOT), §6.8 (soft tombstone). These contracts are FROZEN and already ratified — do **not** edit them. A genuine gap is raised via `cmd/refractor/CONTRACT-AMENDMENT-REQUEST.md`, never an in-flight contract edit.

8. **Gate 2 (BLOCKED) + Gate 3 (DEFENDED) pass**, plus the full verification gate set (see Task 6 / DoD). `make test-bypass` (all BLOCKED) and `make test-capability-adversarial` (all DEFENDED, including the new/extended primary-lens vector) are green, and `gate3-report.txt` regenerates cleanly.

## Tasks / Subtasks

- [ ] **Task 1 — implement `Truncate` on `NatsKVAdapter`** (AC: #1)
  - [ ] Add `func (a *NatsKVAdapter) Truncate(ctx context.Context) error` to `internal/refractor/adapter/natskv.go` so `NatsKVAdapter` satisfies `adapter.Truncater` (`adapter.go:28`). Add a compile-time assertion `var _ Truncater = (*NatsKVAdapter)(nil)` next to the existing `var _ Adapter = ...` (`natskv.go:18`).
  - [ ] Implement it as: list all keys in `a.kv` (`a.kv.ListKeys(ctx)`), and for each key **`Purge`** it (`a.kv.Purge(ctx, key)`). `Purge` removes all prior revisions and leaves a **delete marker** as the latest revision — crucially, a subsequent `a.kv.Get(key)` then returns `jetstream.ErrKeyNotFound`, so the guard's absent→`Create` path (`guardedWrite`, `natskv.go:194-205`) fires cleanly on the first replay and `storedProjectionSeq` never sees a stale watermark. A plain `Delete` is equivalent for the guard (also a marker → `Get` is `ErrKeyNotFound`), but `Purge` additionally drops the history so the bucket does not accumulate revision bloat across repeated rebuilds. Verify the `Get`-returns-`ErrKeyNotFound`-after-`Purge` behavior in the Truncate test (it is the property the force-truncate correctness depends on).
  - [ ] Optionally call `a.kv.PurgeDeletes(ctx)` after the per-key purges to also clear the delete markers (fully empty bucket). Not required for correctness (the markers read as absent), but keeps `ListKeys` clean for a follow-up rebuild. See Open Questions #2.
  - [ ] Document in the method comment (what it does now): clears the bucket so a guarded rebuild's stream replay starts from an empty high-water state and the highest-seq write wins (Contract #6 §6.2). No history/changelog narration.
  - [ ] Confirm `Truncate` is independent of `guarded` — it purges whatever bucket the adapter owns. (An unguarded NATS-KV lens that already requested `truncate=true` now actually truncates instead of silently skipping; verify this is acceptable — it is the behavior the `truncate=true` flag always promised. See Open Questions #1.)

- [ ] **Task 2 — force `truncate=true` for a guarded bucket in `Pipeline.Rebuild`** (AC: #1, #2)
  - [ ] In `Pipeline.Rebuild(ctx, truncate)` (`pipeline.go:357`), before the truncate branch (`:371`), detect a guarded adapter and force the flag: if the active adapter reports `Guarded() == true`, set the effective truncate to `true` (and log at info that the guarded bucket forces truncate so the rebuild cannot leave rejected-write holes — descriptive, not historical).
  - [ ] Detect guardedness without coupling the pipeline to canonical names: type-assert the active adapter against `interface{ Guarded() bool }` (the same pattern `handleAdjUpdate` already uses for the adjacency-watch skip — see `pipeline.go` adj-watch path) on `p.currentAdapter()`. Do **not** import `cmd/refractor` knowledge or canonical-name strings into the pipeline.
  - [ ] The forced-true value flows into the existing truncate branch (`:371-382`), which now finds `NatsKVAdapter` **does** implement `Truncater` (Task 1) and calls `Truncate`. Confirm the existing "adapter does not implement Truncater; skipping" warn path (`:379`) is no longer hit for guarded NATS-KV buckets.
  - [ ] Leave the unguarded-bucket behavior unchanged: a non-guarded adapter honors the requested `truncate` value exactly as today.

- [ ] **Task 3 — wire the guard onto the primary `capability` lens** (AC: #4)
  - [ ] In `cmd/refractor/main.go`, `case "capability":` (`:257`), after the envelope/fan-out/latency wiring, call `enableProjectionGuard(adpt, r.ID)` exactly as `capabilityEphemeral` (`:297`) and `myTasks` (`:322`) do — including the same `if err != nil { logger.Error(...); return }` fail-closed handling. Add a comment on the call stating the primary `cap.<actor>` doc is the primary step-3 auth surface and is guarded against stale-replay resurrection (Contract #6 §6.2). No history/changelog narration.
  - [ ] No adapter changes are needed: `enableProjectionGuard` flips `SetGuarded(true)` and the existing `guardedWrite`/`guardedBody`/`storedProjectionSeq` machinery handles `cap.<actor>` identically to the disjoint keys (the body shape differs but the guard is body-shape-agnostic — it only reads/writes the top-level `projectionSeq`).
  - [ ] **Confirm the primary lens already plumbs `projectionSeq`.** The pipeline stamps `msg.Sequence` in `writeResults` for *every* lens (12.1a), so the primary lens's writes already carry a real seq; flipping `guarded` is sufficient. Verify the `capability` lens's actor-disappearance delete path (it uses the **default** actor-delete-key = the primary `cap.<actor>` doc, no `SetActorDeleteKey` call) routes through the guarded `Delete` → soft tombstone, like the other guarded lenses.

- [ ] **Task 4 — explicitly exclude `capabilityRoleIndex` (decided, in code)** (AC: #5)
  - [ ] Leave `case "capabilityRoleIndex":` (`main.go:269`) with **no** `enableProjectionGuard` call. Add a comment there stating *why* it is unguarded: it is keyed by `operationType` (`cap.role-by-operation.<op>`), an operation-aggregate not an actor-aggregate; per Contract #6 §6.2/§6.3 it is NOT a guarded key — a different resurrection profile (no per-actor revoke/resurrect race). Describe current behavior only (house rule: no "was/now/previously").
  - [ ] Add a test asserting the role-index lens's adapter reports `Guarded() == false` (a guard-family-membership assertion, so a future careless edit that guards it is caught). Place it where the adapter is constructible in test (unit test in `cmd/refractor` if feasible, else a focused adapter-level assertion documenting the exclusion).

- [ ] **Task 5 — rebuild-equivalence test + primary-lens adversarial Gate 3 vector** (AC: #3, #6, #8)
  - [ ] **Rebuild-equivalence test (AC #3).** Add a test (adapter-level or bypass-level, wherever a real NATS-KV bucket + the rebuild path are reachable) that: (a) projects a sequence of guarded events into a bucket carrying live high-seq watermarks (upsert at high seq + a tombstone), (b) runs the rebuild force-truncate path (`Truncate` then replay the historical **lower-seq** events), (c) asserts the resulting bucket is **byte-equal / key-equal** to projecting the same events into a fresh empty bucket — every key present, none missing, no stale tombstone left behind. Prove it would FAIL without the force-truncate (replay holes) and PASS with it. Use `capabilityEphemeral`/`my-tasks` shaped keys per the AC.
  - [ ] **Primary-lens adversarial vector (AC #6).** Extend `internal/bypass/capadv_projection_resurrection_test.go`: add a fail-without/pass-with pair (or parameterize the existing helpers) driving the captured-retry chain against the **primary** `cap.<actor>` key (e.g. `cap.identity.<id>`) — unguarded resurrects (fail-without), guarded rejects (pass-with, key stays a tombstone at the close-era watermark). Mirror the existing `TestCapAdv_V5_*` helpers (`runCapturedRetryChain`, `liveGrantResurrected`).
  - [ ] **Wire into the Gate 3 roll-up.** In `internal/bypass/gate3_test.go`, either add a new vector row (Result `DEFENDED`) to the `rows` slice OR extend vector #5's `Vector`/`Enforcement` strings to state the primary `cap.<actor>` coverage — **state which in the test**. The gate count derives from `len(rows)` (`:101`), so a new row updates the gate automatically; if extending #5 instead, no count change is needed.
  - [ ] **Prove fail-without/pass-with** for the primary-lens vector and record both runs in the Dev Agent Record. Confirm `make test-capability-adversarial` regenerates `gate3-report.txt` cleanly.

- [ ] **Task 6 — verification gates** (AC: #8)
  - [ ] `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`.
  - [ ] `make test-bypass` (Gate 2 — all BLOCKED), `make test-capability-adversarial` (Gate 3 — all DEFENDED, including the primary-lens vector).
  - [ ] Targeted packages: `go test ./internal/refractor/... ./internal/refractor/adapter/... ./internal/refractor/pipeline/... ./internal/bypass/... -count=1`. The rebuild-equivalence and primary-lens vector tests may need a real NATS server (the `internal/bypass` harness `setupCapAdvHarness` already provides one; the my-tasks E2E needs the Docker stack — `make up` first if you run it).

## Dev Notes

### Reuse 12.1a — do NOT reinvent the guard

The entire guard mechanism already exists and is frozen-to-contract. 12.1b adds **no new guard logic**:
- The primary-lens guard is a **one-line wiring change** (`enableProjectionGuard(adpt, r.ID)` in `case "capability":`). `SetGuarded`/`guardedWrite`/`guardedBody`/`storedProjectionSeq` (`natskv.go:60-276`) handle the primary `cap.<actor>` doc unchanged — the guard reads/writes only the top-level `projectionSeq`, so the doc's section shape (platformPermissions/serviceAccess/roles) is irrelevant to it.
- The rebuild fix is a **new `Truncate` method** (Task 1) + a **force-true rule** (Task 2). Do not touch `guardedWrite`.

### Why force-truncate, not a bypass flag (the decided rule — see the dedicated section above)

`NatsKVAdapter` does not implement `Truncater` today, so a guarded rebuild is **already broken both ways** (`truncate=false` → guard rejects lower-seq replays → holes; `truncate=true` → silently skips truncate → same holes). Force-truncate fixes both with one mechanism and keeps the guard always-on. A bypass flag would have to be honored by the concurrent retry-queue writer too, reopening the resurrection window during the rebuild — rejected.

### Rebuild plumbing facts (grounded)

- `Pipeline.Rebuild(ctx context.Context, truncate bool) error` is at `pipeline.go:357` (the epic cites `:437` — **stale/wrong**). The truncate branch is `:371-382`: it type-asserts the **current** adapter (`p.currentAdapter()`) against `adapter.Truncater` and calls `Truncate`, else logs *"truncate=true but adapter does not implement Truncater; skipping"* (`:379`).
- The adapter can be hot-swapped (lens-def adapt), so resolve guardedness from `p.currentAdapter()` at rebuild time, not a cached field.
- `Rebuild` returns nil immediately and runs the rescan asynchronously (delete-recreate-swap the durable via `p.supervisor.Reset`). The truncate happens synchronously **before** the durable reset (`:371` before `:384`), so the bucket is empty before any replay lands — the ordering the force rule relies on is already correct.

### Adapter guardedness detection (no canonical names in the pipeline)

`NatsKVAdapter` exposes `Guarded() bool` (`natskv.go:69`) — added in 12.1a precisely so non-`cmd` code can ask "is this guarded?" without knowing lens names. `handleAdjUpdate` already type-asserts `interface{ Guarded() bool }` for the adj-watch skip; reuse that exact pattern in `Rebuild`. Do **not** seed canonical-name knowledge into the pipeline or `buildAdapter` (the canonical-name switch is what 12.4 deletes).

### Primary `capability` lens specifics

- Wiring is in `cmd/refractor/main.go` `case "capability":` (`:257`). It installs `NewWrapper` (`capabilityenv` envelope), `NewActorEnumerator` fan-out, and a latency buffer — but **no** `enableProjectionGuard` (the gap this story closes) and **no** `SetActorDeleteKey` (so actor-disappearance deletes the **default** primary `cap.<actor>` doc, which is exactly the key we now guard).
- The primary doc body is built by `capabilityenv.NewWrapper`/`capabilityKey` (`envelope.go:48`/`:91`). The guard injects `projectionSeq` as a top-level field via `guardedBody` (`natskv.go:231`) — the wrapper is **untouched** (adapter-injects, 12.1a adjudication #3).
- The primary lens already carries `projectionSeq` on its writes (12.1a stamps `msg.Sequence` in `writeResults` for every lens). Flipping `guarded` is the only change.

### `capabilityRoleIndex` exclusion (the decided rationale, for the code comment)

`capabilityRoleIndex` projects via `NewRoleIndexWrapper` (`envelope.go:334`) to `cap.role-by-operation.<operationType>` — keyed by **operation type, not actor**. It is an operation-aggregate consumed only by the denial-response builder (§6.1). There is no per-actor revoke→resurrect race (a role's operation coverage is recomputed from the role graph, not subject to the open-era/close-era capture window that affects per-actor task/grant projections). Contract #6 §6.2/§6.3 list it as explicitly **NOT** a guarded key. Guarding it would add a watermark with no resurrection threat to defend and complicate its rebuild. Decision: leave unguarded, comment the why, assert `Guarded()==false`.

### Step-3 / auth invariants (regression prevention)

- Guarding the primary `cap.<actor>` doc adds soft tombstones on its delete path. Absence and an `isDeleted:true` tombstone are **equivalent for authorization** — step-3 denies in both cases (Contract #6 §6.8, unchanged by 12.1a). **Do not add tombstone-handling to the step-3 processor in this story** — the existing absent/denied paths already cover it. If you find a step-3 reader of `cap.<actor>` that would treat a tombstone as a live capability doc, **STOP and flag it** (that would be a real bug). 12.1a's note says the same for the disjoint keys; it now applies to the primary doc — verify `step3_auth_capability.go` treats an `isDeleted` `cap.<actor>` as no-capability (deny), not as a live doc.

### House rules (CLAUDE.md)

- **No history/changelog comments in code.** No `// Story 12.1b …`, `// was unguarded`, `// now truncates`. Comments describe what the code does now. (Single most-violated rule.)
- **Contract #1 key shapes.** Primary key is `cap.identity.<id>` (built by `capabilityKey`, `envelope.go:91`). Do not alter the key shape.
- **New docs go in `/docs`** (close to the code), not `_bmad-output/`. If you add a code-doc note on the rebuild force-truncate rule, put it under `docs/` (e.g. `docs/components/refractor.md`, which already documents the rebuild/truncate behavior).
- **Sub-agents do not commit/push/branch** — leave changes in the working tree for Winston to adjudicate.

### Project Structure Notes

- Implementation: `internal/refractor/adapter/natskv.go` (new `Truncate`), `internal/refractor/pipeline/pipeline.go` (force-truncate in `Rebuild`), `cmd/refractor/main.go` (`enableProjectionGuard` on `capability`; comment on `capabilityRoleIndex`).
- Tests: `internal/refractor/adapter/natskv_test.go` (Truncate + role-index `Guarded()==false`), a rebuild-equivalence test (adapter- or bypass-level), `internal/bypass/capadv_projection_resurrection_test.go` (primary-lens vector), `internal/bypass/gate3_test.go` (row/string).
- No new top-level packages. No contract edits. No `cmd`-knowledge leaking into `internal/refractor/pipeline`.

### References

- [Source: _bmad-output/planning-artifacts/epics/phase-2-epics.md#Story 12.1b] — ACs (verbatim backbone), the force-truncate-OR-bypass choice, primary-lens + role-index-exclusion requirements. (Epic line refs `:437` are stale — see re-grounded refs below.)
- [Source: docs/decisions/projection-plane-decomposition.md#D-INTEGRITY] — bug chain, `projectionSeq` rationale; party-review finding #4 (rebuild → 12.1b) and #7 (role-index excluded). Sequencing: 12.1b ← 12.1a.
- [Source: docs/contracts/06-capability-kv.md#6.2] — guard + the *"Rebuild interaction (Story 12.1b)"* bullet (force-truncate OR bypass); guarded-key list includes `cap.<actor>`; `cap.role-by-operation` NOT guarded. Applied commit `29b9536`.
- [Source: docs/contracts/06-capability-kv.md#6.3] — `projectionSeq` field spec; guarded-key list; Postgres/role-by-operation exemptions.
- [Source: docs/contracts/06-capability-kv.md#6.8] — soft tombstone carries the watermark; absence ≡ tombstone for auth.
- [Source: cmd/refractor/CONTRACT-AMENDMENT-REQUEST.md] — Request 4 item 5 (rebuild interaction, deferred to 12.1b).
- [Source: _bmad-output/implementation-artifacts/12-1a-projection-write-guard.md] — the as-merged guard (mechanism, `enableProjectionGuard`, `SetGuarded`/`Guarded`, `guardedWrite`); 12.1a's "OUT of scope (12.1b)" list; the Adjudication + Dev Agent Record.
- Code: `internal/refractor/pipeline/pipeline.go` — `Rebuild` :357, truncate branch :371-382, `currentAdapter()`, adj-watch `Guarded()` type-assert pattern.
- Code: `internal/refractor/adapter/adapter.go` — `Adapter` :12, `Truncater` :28.
- Code: `internal/refractor/adapter/natskv.go` — compile assert :18, `New` :53, `SetGuarded` :63, `Guarded` :69, `Upsert`/`Delete` :94/:128, `guardedWrite` :179, `guardedBody` :231, `storedProjectionSeq` :251.
- Code: `cmd/refractor/main.go` — `buildAdapter` :161, `case "capability"` :257, `case "capabilityRoleIndex"` :269, `case "capabilityEphemeral"` :279 (guard at :297), `case "myTasks"` :303 (guard at :322), `enableProjectionGuard` :516-531.
- Code: `internal/refractor/capabilityenv/envelope.go` — `NewWrapper` :48, `capabilityKey` :91, `NewRoleIndexWrapper` :334, `EphemeralKey` :213, `MyTasksKey` :311.
- Test: `internal/bypass/capadv_projection_resurrection_test.go` — vector #5 structure, `runCapturedRetryChain` :77, `liveGrantResurrected` :99, `TestCapAdv_V5_*` :120/:146/:192, `setupCapAdvHarness` (NATS harness).
- Test: `internal/bypass/gate3_test.go` — `rows` slice, `total := len(rows)` :101, report writer :120-151.
- Test: `internal/refractor/adapter/natskv_test.go` — guarded unit tests :204-319 (structure to mirror for Truncate + role-index assertion).
- Make: `Makefile` — `test-bypass`, `test-capability-adversarial`, `verify-kernel`.

## Previous Story Intelligence

12.1a (this epic's first story, `done`) is the direct foundation. Carry forward:

- **The guard is complete and frozen-to-contract** — 12.1b reuses it (one wiring call for the primary lens; no new guard code). 12.1a's Adjudication #2 mandated: keep canonical-name knowledge in the one `switch` (12.4 deletes it); the pipeline asks `Guarded()`, not lens names. **Apply the same discipline in `Rebuild`.**
- **12.1a explicitly deferred both of this story's deliverables** and even pinned the warnings: *"Do not attempt to make `Rebuild(truncate=false)` work against a guarded bucket here"* and *"The primary `capability` lens guard … 12.1b."* This story is the planned follow-up — no scope ambiguity.
- **`Guarded()` was added in 12.1a for exactly this** — non-`cmd` code asks the adapter whether it is guarded (used by the adj-watch skip). Reuse it in `Rebuild`.
- **The seq-0 fail-closed drop + CAS bounded loop (cap 8)** already handle the concurrent retry-goroutine writer. The force-truncate rule must **not** introduce a bypass that defeats that protection — keep the guard on, clear the bucket instead.
- **12.1a's adj-watch resolution** (skip guarded-key writes on the non-stream path; a guarded watermark is advanced/cleared only by a stream-sequenced write) applies unchanged to the primary lens now that it is guarded. The primary lens uses `NewActorEnumerator` fan-out (stream-driven) — verify the adj-watch skip covers `capability` too (the `Guarded()` type-assert in `handleAdjUpdate` is generic, so it should).

## Git Intelligence

- `29b9536` — ratified + applied the Epic 12 contract amendments (CAR Requests 4–7). The §6.2 *"Rebuild interaction (Story 12.1b)"* bullet and the §6.3 guarded-key list (`cap.<actor>` guarded; `cap.role-by-operation` exempt) are **already frozen** — build to them.
- `0d8063c` + `6c19f3d` — Story 12.1a (the guard). Established the pattern: guard logic in `internal/refractor/adapter/natskv.go`, wiring in `cmd/refractor/main.go` via `enableProjectionGuard`, adversarial proof in `internal/bypass/`, gate count derived from `len(rows)`.
- Recent commits show the established pattern: implementation under `internal/`, adversarial proof in `internal/bypass/`, docs under `docs/`, status tracked in the story file.

## Open Questions

1. **`Truncate` on `NatsKVAdapter` affects ALL NATS-KV lenses, not just guarded ones.** Once `NatsKVAdapter` implements `Truncater`, an **unguarded** NATS-KV lens that requests `Rebuild(truncate=true)` will now actually purge the bucket instead of silently skipping (today's behavior). This is arguably a latent bug-fix (the flag finally does what it says) and is the desired semantics, but it is a behavior change for unguarded NATS-KV targets that previously relied on the silent-skip. **Recommendation: accept it** (it is correct, and `truncate=true` is operator-requested) and note it in the Dev Agent Record / `docs/components/refractor.md`. Confirm before merge — flag if any test or operator runbook depends on the old silent-skip.

2. **`Purge` vs `Delete` (+ optional `PurgeDeletes`) per key in `Truncate`.** Both `Purge` and `Delete` leave a delete-marker as the latest revision, so after either, `Get` returns `ErrKeyNotFound` and the guard's absent→`Create` path fires cleanly — both are correct for the force-truncate property. `Purge` additionally removes prior revisions (no history bloat across repeated rebuilds); an optional `PurgeDeletes` afterward clears the markers entirely. **Recommendation: `Purge` per key, optionally `PurgeDeletes` to finish.** Confirm whether the extra `PurgeDeletes` is wanted or over-engineering for the expected rebuild cadence.

3. **Should a `Rebuild(truncate=false)` on a guarded bucket be silently upgraded to truncate, or surfaced to the operator?** The chosen rule upgrades silently (preserves "rebuild this bucket" intent, removes the footgun). The epic AC permits *"rejected with a clear error OR correctly restore every key"*; we pick "correctly restore" via silent upgrade. An alternative is to **log a warning** (not an error) that truncate was forced, so the operator's async-ack/log shows it. **Recommendation: force + info-log** (visible, non-failing). Confirm whether the control-service async ack should also reflect the forced truncate (likely out of scope — the ack is already `started:true`).

4. **New Gate 3 vector row vs. extend vector #5?** The primary-lens adversarial proof can be a **new** `rows` entry (e.g. vector #6, DEFENDED) or fold into vector #5's strings (which already says "retry + adj-watch" and §6.2/§6.8). A new row is more legible in `gate3-report.txt` and the `len(rows)`-derived count handles it automatically; extending #5 keeps the report compact. **Recommendation: new row** for the primary-lens coverage (distinct surface, distinct enforcement statement). Confirm — minor, dev's call if green either way.

5. **Where does the rebuild-equivalence test live?** It needs a real NATS-KV bucket and ideally the `Pipeline.Rebuild` path. Options: (a) adapter-level (construct a `NatsKVAdapter`, call `Truncate` + replay, assert key-equality vs. fresh bucket) — simplest, exercises Task 1+the replay-wins property but not the `Rebuild` force-true wiring; (b) bypass-level using `setupCapAdvHarness` — closer to end-to-end; (c) pipeline-level integration. **Recommendation: at least (a) for the equivalence property + a focused unit test for the `Rebuild` force-true decision** (assert that a guarded adapter forces truncate). Confirm the split is acceptable vs. a single heavier integration test.

---

## Adjudication (Winston, 2026-06-14) — all five resolved; build to these

**Rebuild rule → FORCE-TRUNCATE on a guarded bucket (the recommended option, ratified).** The security argument is decisive: a documented bypass-flag would let the retry-queue goroutine (which writes *concurrently* with the rebuild replay — the exact race 12.1a's CAS defends) land a stale captured write mid-rebuild and survive, reopening the resurrection window. Force-truncate keeps the guard always-on; first replay write wins; steady state == from-scratch (§6.2 deterministic-replay property). Implement `Truncate` on `NatsKVAdapter` and force `truncate=true` when `adapter.Guarded()==true`, reusing the `interface{ Guarded() bool }` type-assert already at `pipeline.go:978` (the pipeline never learns canonical names).

1. **`Truncate` affects all NATS-KV lenses → ACCEPT.** Making `truncate=true` actually purge (vs today's silent skip) is a latent bug-fix and is operator-requested, so it is correct for unguarded lenses too. **Document it in `docs/components/refractor.md`** (the rebuild/truncate semantics row). Dev MUST grep tests + any operator runbook for reliance on the old silent-skip and flag if found; absent that, accept.

2. **`Truncate` impl → `Purge` per key; NO `PurgeDeletes`.** `Purge` leaves a delete-marker → `Get` returns `ErrKeyNotFound` → the guard's absent→`Create` replay path fires cleanly (exactly what we need) and clears prior revisions. Skip `PurgeDeletes` — over-engineering for the rebuild cadence; revisit only if history bloat is observed.

3. **Guarded `Rebuild(truncate=false)` → force + info-log (the "correctly restore every key" branch).** Silently upgrade to truncate (removes the footgun, preserves "rebuild this bucket" intent) and emit an **info-log** that truncate was forced (operator-visible, non-failing). Do NOT change the control-service async ack (already `started:true` — out of scope).

4. **Gate 3 coverage → new row, but make it the REBUILD vector, not a #5 clone.** The mechanically-identical static guard on `cap.<actor>` adds little over vector #5; the genuinely-new adversarial surface is the **rebuild path**. Add **vector #6 (DEFENDED): "Guarded-projection rebuild integrity"** — proving (a) a rebuild of a guarded bucket restores every key (no rejected-write holes, force-truncate working) AND (b) a stale concurrent retry during/after the rebuild cannot resurrect. The primary-lens static guard extension is covered by extending existing fixtures to include `cap.<actor>` (no separate row needed). `len(rows)` auto-counts → 6/6.

5. **Test placement → (a) + the focused `Rebuild` force-true unit test, AND the vector-#6 bypass-level proof from (4).** Adapter-level equivalence (Truncate + replay == fresh bucket) + a unit test asserting a guarded adapter forces `truncate=true` in `Rebuild` + the Gate-3 #6 end-to-end proof. That split is accepted; no single heavy integration test required.

**Scope reminder:** rebuild reconciliation + primary `capability` lens guard + explicit `capabilityRoleIndex` exclusion ONLY. No D-PIPELINE compiler (12.2+), no decomposition (12.5+).

---

## Dev Agent Record (Amelia, 2026-06-14)

### Files changed

- `internal/refractor/adapter/natskv.go` — added `Truncate(ctx)` (purge per key via `kv.Purge`; `ErrKeyNotFound`-tolerant) + `var _ Truncater = (*NatsKVAdapter)(nil)` compile assertion. Per adjudication #2: `Purge`, no `PurgeDeletes`.
- `internal/refractor/pipeline/pipeline.go` — `Rebuild` now forces `truncate=true` when the current adapter reports `Guarded()==true`, via the `interface{ Guarded() bool }` type-assert (same pattern as the adj-watch skip). Emits an info-log that truncate was forced (adjudication #3). Pipeline never learns canonical names.
- `cmd/refractor/main.go` — `case "capability":` now calls `enableProjectionGuard(adpt, r.ID)` with the same fail-closed `if err != nil { logger.Error(...); return }` handling as the other guarded lenses; added a descriptive comment. `case "capabilityRoleIndex":` left unguarded with a comment stating why (operation-aggregate, not actor-aggregate; not a guarded key per §6.2/§6.3).
- `docs/components/refractor.md` — new "Rebuild & truncate semantics" subsection documenting the force-truncate rule and the Purge-leaves-marker property (adjudication #1: the all-NATS-KV-lens truncate behavior is documented).
- `internal/refractor/adapter/natskv_test.go` — Truncate tests (`Get`→`ErrKeyNotFound` after purge; empty-bucket no-op), adapter-level rebuild-equivalence test (Truncate+replay == fresh bucket) + its fail-without sibling, role-index `Guarded()==false` exclusion assertion, `dumpBucket` helper.
- `internal/refractor/pipeline/rebuild_force_truncate_internal_test.go` (new) — focused unit test: a guarded adapter forces truncate in `Rebuild` even with `truncate=false`; unguarded honors the request; unguarded truncates when explicitly requested.
- `internal/bypass/capadv_rebuild_integrity_test.go` (new) — Gate 3 vector #6 ("Guarded-projection rebuild integrity", DEFENDED): (a) guarded rebuild restores every key (force-truncate, post-rebuild == from-scratch, with an inline fail-without hole proof) and (b) a post-rebuild stale retry cannot resurrect the primary `cap.identity.<id>` doc. Drives the PRIMARY key, covering the primary-lens guard extension per adjudication #4 (no separate #5-clone row).
- `internal/bypass/gate3_test.go` — added vector #6 row; `len(rows)` auto-counts to 6/6.

### Decisions / adjudication compliance

- Rebuild rule = force-truncate on `Guarded()==true` (no bypass flag). ✓
- `Truncate` = `Purge` per key, no `PurgeDeletes`. ✓
- Guarded `Rebuild(truncate=false)` = force + info-log; control-service ack untouched. ✓
- Gate 3 = new vector #6 (rebuild integrity), not a #5 clone; primary-lens static guard covered by vector #6 driving `cap.identity.<id>`. ✓
- Tests = adapter-level equivalence + focused `Rebuild` force-true unit test + vector-#6 bypass proof. ✓
- `capabilityRoleIndex` left unguarded with in-code rationale + `Guarded()==false` assertion. ✓

### Fail-without / pass-with proofs

- Vector #6 (a) restore: `TestCapAdv_V6_GuardedRebuild_RestoresEveryKey` — fail-without (lower-seq replay against an un-truncated live watermark is rejected → hole) and pass-with (force-truncate → post-rebuild bucket key-equal to from-scratch) both asserted in one test. PASS.
- Vector #6 (b) resurrection-safety: `TestCapAdv_V6_GuardedRebuild_ConcurrentStaleRetryCannotResurrect` — a post-rebuild stale retry stays a tombstone at the close-era watermark. PASS.
- Adapter-level: `TestNatsKVAdapter_Truncate_RebuildEquivalence` (pass-with) + `..._FailsWithoutTruncate` (fail-without). PASS.

### Step-3 regression check (Dev Notes directive)

Verified `internal/processor/step3_auth_capability.go` + `capability_doc.go`: a guarded soft-tombstone body `{isDeleted:true, projectedAt, projectionSeq}` parses cleanly (no `DisallowUnknownFields`) into a `CapabilityDoc` with empty permission sections → all matchers deny. No step-3 reader treats a tombstone as a live doc. No step-3 change made (matches the directive). No bug found.

### OQ#1 grep (silent-skip reliance)

Grepped `*.go` + `*.md` for `does not implement Truncater` / `silently skip` / `truncate.*skip`. No production code or test relies on the old Truncater silent-skip. The remaining warn line (`pipeline.go`) is a legitimate fallback for non-NATS-KV adapters (Postgres has no `Truncater`). The latent behavior change (`truncate=true` now actually purges for unguarded NATS-KV lenses) is documented in `docs/components/refractor.md`. Accepted per adjudication #1; nothing to flag.

### Verification (all run inline, foreground)

- `go build ./...` — PASS
- `make vet` — PASS
- `golangci-lint run ./...` — 0 issues
- `make verify-kernel` — ALL ASSERTIONS PASSED
- `make test-bypass` — Gate 2 PASSED (4/4 BLOCKED)
- `make test-capability-adversarial` — Gate 3 PASSED (6/6 cleared — 5 DEFENDED incl. new #6, 1 ACCEPTED-WINDOW); `gate3-report.txt` regenerated
- `go test ./internal/refractor/... ./internal/bypass/... -count=1` — all PASS
