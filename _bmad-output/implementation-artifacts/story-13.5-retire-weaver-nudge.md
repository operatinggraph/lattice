# Story 13.5 — Retire Weaver's Two-Phase Nudge path (tear down the dead external-I/O plane)

**Status:** done — CI green (run 27805935424, 89dd4b1, 2026-06-18). Full 3-layer review unanimous ship-ready (no over-deletion, FR58 preserved via the bridge, all 8 ACs met). The Two-Phase Nudge plane is gone: `internal/weaver/nudge/` deleted, `weaver-claims` retired (bootstrap v8→v9), Weaver imports neither bridge nor nudge (AC#3), Gate 2/3 + verify-kernel + test-lease-convergence all green. **Closes Epic 13 + Phase 2.** (Contract #07 §7.7 weaver-claims-bucket removal amended in-place but left UNCOMMITTED for Andrew.)
**Epic:** 13 — External I/O Bridge (the **FINAL** Epic-13 story). Re-homes external idempotent I/O out of Weaver (Epic 10, superseded) into Loom + the generic `bridge`; this story removes the now-dead Weaver nudge machinery.
**Tier:** Opus — a **plane teardown** (delete a whole dispatch path + a primordial bucket + lockstep kernel-verify enumerations). The risk is *what silently depended on it*, so the gate is **full 3-layer adversarial review** + Gate 2 + Gate 3 + verify-kernel, proving convergence stays DEFENDED with the nudge gone.

## Why this story exists

External idempotent I/O (FR58 / NFR-S11) shipped in Epic 10 as Weaver's **Two-Phase Nudge** protocol: a `nudge` GapAction dispatched a one-shot external call from Weaver's stateless lane-1 and recorded the outcome via a "resolve op," backed by a `weaver-claims` KV bucket (claim → execute → resolve). The reference vertical (Epic 11/14) proved that placement was wrong — the resolve op could not address a candidate entity distinct from the nudge `subject`, and a Starlark DDL op cannot read `authContext` — so external I/O was **re-homed** to **Loom's `externalTask` step + a new generic `bridge` component** (the RATIFIED change: `sprint-change-proposal-2026-06-18.md`).

The bridge plane is now **proven end-to-end**: Stories 13.2–13.4 + 13.6 shipped CI-green (Loom `externalTask`, the bridge service actor, the bridge component + the FR58 crash/retry proof, the deadline/completion symmetry), and **Stories 14.5 + 14.6 landed CI-green** — the lease vertical converges end-to-end through the **real** Processor via `triggerLoom` (a pattern whose body is an `externalTask`), **never** a `nudge`. The move-then-delete sequencing is satisfied: the `Fake*` adapters and the entire `bridge.{Adapter,Registry,Request,Result}` contract already live in `internal/bridge/` (relocated in 13.4), and no package authors a `nudge` gap. So the nudge plane can now be torn down with **no window where neither external path works** — exactly the deferral condition the epic set ("BLOCKED until 14.5 green").

This is a **subtractive** story: it ships when the dead code, the dead bucket, and the dead docs are gone and *everything that survives still works* — `triggerLoom` / `assignTask` / `directOp`, the reconciler/sweeper, `weaver-state`, the control API, lane-3 temporal, and the FR58 guarantee (now carried by the bridge).

## Acceptance Criteria

Reproduced from `_bmad-output/planning-artifacts/epics/phase-2-epics.md` "Story 13.5" (≈641–660), decomposed:

1. **The nudge dispatch path is deleted.** `internal/weaver/nudge/` (the whole package), the `fireNudge`/`recoverNudge`/`nudgeDispatch`/`nudgeDecision`/`checkClaimWedge` call sites in `internal/weaver/evaluator.go`, the `actionNudge` strategist case + the `nudgePlan`/`plan.nudge` shape in `internal/weaver/strategist.go`, and the engine's nudge wiring (`claims`/`adapters`/`nudger` fields, `RegisterAdapter`, `resolveFunc`, `resolveProbe`, `WeaverClaimsBucket`/`ClaimRetention` config) in `internal/weaver/engine.go` are removed. `actionNudge` (the constant) and `deriveResolveRequestID` are gone.
2. **`weaver-claims` is fully retired.** The `WeaverClaimsBucket` primordial constant + its provisioning entry (`internal/bootstrap/primordial.go`), **both** kernel-verify enumerations (`scripts/verify-kernel.go` **and** `internal/bootstrap/verify.go`) drop the claims bucket **in lockstep**, and the **bootstrap-file `version` bumps** (8 → 9: `checkVersion` + `lattice.bootstrap.json` + the version-history comment, in lockstep — dropping a provisioned bucket changes the kernel topology a stale file would not match).
3. **The temporary `internal/weaver` → `internal/bridge` import is removed.** 13.4 left a temporary `internal/weaver`→`internal/bridge` import so the relocated `Fake*` adapters still compiled against the engine's nudge wiring (the `bridge.Registry` field). After teardown, **`internal/weaver` (non-test) imports neither `internal/bridge` nor `internal/weaver/nudge`** — verified by grep. (`internal/bridge` already owns the adapter contract + the `Fake*` shims; nothing in the bridge depends back on weaver.)
4. **Nudge wiring is removed from the binary + the installer guard.** `cmd/weaver/main.go` drops the `internal/bridge` import + the `RegisterAdapter("stripe"/"backgroundCheck", …)` block. `internal/pkgmgr/orchestrationguard.go` drops the `actionNudge` case (constant + `validateGapAction` branch); the unknown-action error message + the doc comments listing `nudge` are updated to the surviving set (`triggerLoom | assignTask | directOp`). `internal/pkgmgr/{build,definition}.go` doc comments that list `nudge` are updated.
5. **Docs describe the post-teardown present state** (no history comments): `docs/components/weaver.md` drops the Two-Phase Nudge section + the `weaver-claims` rows + the `nudge`-action descriptions (the `missing_bgcheck`/`missing_payment` rows become `triggerLoom` of an `externalTask` pattern). Any other doc still documenting the nudge path is updated. (Frozen contracts `docs/contracts/10-orchestration-surfaces.md` §10.3/§10.8 were **already amended 2026-06-18 (13.1)** — do **NOT** edit them.)
6. **What survives is untouched and proven:** the reconciler/sweeper + `weaver-state` are **KEPT** (they serve `triggerLoom`/`assignTask`/`directOp`); Weaver lanes 1 (convergence dispatch) + 3 (temporal) + the control API are unchanged; `triggerLoom`/`assignTask`/`directOp` GapActions stay. **Gate 3 convergence stays DEFENDED** with the nudge gone; `grep -rn "weaver-claims\|nudge" scripts/ Makefile .github/ internal/bootstrap/` is **clean** (including the two `internal/bootstrap/loom_state_bucket_test.go` comment mentions — see §0.E).
7. **Coexistence-window AC — no package authored a `nudge` gap** during the 13.4→14.5 window. `grep -rn` over `packages/*` WeaverTargets/GapActions confirms every external-remediation gap is `triggerLoom` (lease-signing's `missing_bgcheck`/`missing_payment`/`missing_onboarding`) and **no `Action: "nudge"`** exists anywhere. (Verified at draft time: lease-signing uses `triggerLoom` + `assignTask` only — see §0.G.) If any package still declares a `nudge` action, **STOP and surface it to the lead** — it is a blocker to the teardown.
8. **FR58 still holds through the bridge.** `make test-lease-convergence` (14.5's e2e) + `internal/bridge/fr58_test.go` stay green — the at-most-once external-effect guarantee is carried by the bridge, untouched by the nudge removal.

## ⚠️ §0 — Traps & ground truth (verified against the code at draft time — build to these)

### 0.A — KEEP `weaver-state`, the reconciler/sweeper, `markStore`, and the `mark.claimId` *field*. Delete only the nudge-specific *machinery*.
This is the single highest-risk place to over-delete. The retirement is **bounded** (`sprint-change-proposal-2026-06-18.md` M3):
- **`internal/weaver/state.go` (`markStore`)** stays — it is the §10.3 dispatch-OCC for *every* action. Delete **`createNudge`** (the nudge-only CAS-create that mints a `claimId`). **`replace`** and **`replaceCarryingClaim`** both stay, BUT note `replace` currently delegates to `replaceCarryingClaim(..., "", ...)`. Decision for dev: either (a) keep `replaceCarryingClaim` as the impl with the blank-claimId path now the only caller, or (b) inline it into `replace` and delete `replaceCarryingClaim` (cleaner — no non-nudge caller passes a claimId once `reclaimNudge` is gone). **Recommend (b)** — fold `replaceCarryingClaim`'s body into `replace`, drop the `claimID` param. The `mark.ClaimID` JSON field is part of the **frozen §10.3 value shape** (`{...,claimId?,...}`, `omitempty`) — **KEEP the struct field** (a frozen-shape field, harmless when always empty); just stop writing it. Update the `mark` doc comment to describe the present state (no "Epic 10 mints it" narration).
- **`internal/weaver/reconciler.go` (`sweeper`)** stays. Delete: the `reclaimNudge` method, the `ga.Action == actionNudge` corrupt-claim guard (≈295–305), and the `if ga.Action == actionNudge { s.reclaimNudge(...) }` branch (≈318–321) — the reclaim falls through to the plain `marks.replace` + `e.fire` path for all surviving actions. **KEEP** `defaultClaimRetention` only if still referenced after the engine `ClaimRetention` config is gone — it is **not** (it backed `Config.ClaimRetention`), so **delete `defaultClaimRetention`** and **`claimWedgeBound`** (only `checkClaimWedge` used it). Keep `defaultMarkLease`/`defaultSweepInterval`/`defaultSweepOrphanWarmup`.

### 0.B — `internal/weaver/evaluator.go`: surgically excise the nudge branches; the plain-submit path is the survivor.
`fireEpisode` (≈225) currently branches: an in-flight nudge mark → `recoverNudge`; a fresh nudge mark → `createNudge` + `fireNudge`; everything else → `marks.create` + `fire`. **Remove the `action == actionNudge` branches** so `fireEpisode` is just: read mark → if in-flight && redelivered → `fire` (re-publish); if not in-flight → `marks.create` → `fire`. The `action`/`entityKey` params stay (the plain path uses `entityKey` for `marks.create`). Delete the now-orphaned helpers: `nudgeDispatch`, `fireNudge`, `recoverNudge`, `checkClaimWedge`, `nudgeDecision`, and the issue-key helpers `issueKeyNudge` + `issueKeyNudgeWedge`. **KEEP** `issueKeyGap` + `issueKeyData` (used by the surviving paths). The `errors`/`time` imports may become unused once `nudgeDecision`/`checkClaimWedge` are gone — let `goimports`/`make vet` catch it; do not pre-trim by guesswork.

### 0.C — `internal/weaver/strategist.go`: delete the `actionNudge` case + the `nudgePlan` carrier; the `plan` struct loses its `nudge` field.
Remove the `case actionNudge:` block in `buildPlan` (≈216–253), the `actionNudge` constant (≈14), the `nudgePlan` type (≈90–95), and the `nudge *nudgePlan` field on `plan` (≈80) + its doc paragraph (≈64–69). The `plan` struct keeps `operationType`/`authTarget`/`payload`/`reads`. `buildPlan`'s `default:` already returns `errConfig` for an unknown action — a `nudge` action declared by some future package now hits that path (fail-closed), which is correct post-retirement.

### 0.D — `internal/weaver/engine.go`: remove BOTH the `nudge` import and the `bridge` import + all nudge fields/methods.
Delete the imports `internal/bridge` (≈19) and `internal/weaver/nudge` (≈21). Delete the `Engine` fields `claims`, `adapters`, `nudger` (≈210–212). Delete the `NewEngine` lines that build `adapters := bridge.NewRegistry()`, `claims := nudge.NewClaimStore(...)`, and set `claims/adapters/nudger` (≈297–306). Delete the methods `RegisterAdapter` (≈505–515), `resolveFunc` (≈517–546), `resolveProbe` (≈548–591). Delete `Config.WeaverClaimsBucket` (≈38–42) + `Config.ClaimRetention` (≈43–47) and their `withDefaults` lines (≈146–151). **Fix the `Start` comment at ≈338** ("An empty ActorKey would publish nudge ops under actor:\"\"…") to drop the "nudge" word — the check stays, the prose updates to "remediation ops." **`engine.go` must not import `internal/bridge` or `internal/weaver/nudge` after this.** (This is AC#3's grep target.)

### 0.E — The "clean grep" AC includes two test-comment mentions in `internal/bootstrap/`.
`grep -rn "weaver-claims\|nudge" scripts/ Makefile .github/ internal/bootstrap/` (AC#6) currently also hits `internal/bootstrap/loom_state_bucket_test.go:18` and `:48` (comments: "matching its weaver-state/weaver-claims siblings" / "like weaver-state/weaver-claims"). Update those comments to drop `weaver-claims` (e.g. "matching its weaver-state sibling") so the grep is genuinely clean. The grep is over `internal/bootstrap/` (not all of `internal/`), so the `docs/` and `internal/weaver/` references are out of that specific grep's scope — but they are still removed by ACs #1/#5.

### 0.F — Bootstrap version bump is REQUIRED and is 8 → 9. `PrimordialVertexKeyCount` does NOT move.
The bootstrap file is currently `version: "8"` (`internal/bootstrap/nanoid.go checkVersion`, `lattice.bootstrap.json`). Dropping a provisioned KV bucket changes the kernel topology, so AC#2 requires a version bump: set `checkVersion` to accept `"9"` (and reject `"8"` with the regenerate message), bump `lattice.bootstrap.json` `"version"` to `"9"`, and add a one-line `// Version 9 …` to the version-history comment (≈324). **Do NOT touch `PrimordialVertexKeyCount` (= 29)** — that counts top-level *vertex* keys (identities/roles/perms), not KV buckets; `weaver-claims` is a bucket, not a vertex key, so the count is unchanged. (Cross-check: the bridge identity in 13.3 *did* move that count; a bucket does not.)

### 0.G — Coexistence-window AC is already satisfied at draft time (re-verify in dev).
`grep -rn "nudge\|Action:" packages/` at draft: **lease-signing uses `triggerLoom` (`missing_onboarding`/`missing_bgcheck`/`missing_payment`) + `assignTask` (`missing_signature`) only** (`packages/lease-signing/targets.go:26–29`); the single `nudge` hit is a comment ("the retired nudge action is never used"). No package declares `Action: "nudge"`. The dev must re-run `grep -rn 'Action:.*nudge\|"nudge"' packages/` and assert zero gap-action hits; if any exist → blocker (AC#7).

### 0.H — Tests: delete the nudge-only files + the nudge-only *tests within kept files*; the mark/reclaim tests for surviving actions STAY.
- **Delete whole files:** `internal/weaver/nudge/protocol_test.go`, `internal/weaver/nudge_dispatch_internal_test.go`, `internal/weaver/reconciler_nudge_internal_test.go`, `internal/weaver/state_nudge_internal_test.go`.
- **Delete specific tests in kept files:** `internal/weaver/weaver_e2e_test.go` → `TestWeaverE2E_NudgeStub` (≈1155–1215, the `action:"nudge"` fixture). `internal/weaver/requestid_internal_test.go` → `TestDeriveResolveRequestID_DeterministicAndDisjoint` (≈58–92, tests the deleted `deriveResolveRequestID`).
- **Triage carefully — DO NOT mass-delete by "claim" hits:** in `weaver_e2e_test.go` and `reconciler_internal_test.go`/`evaluator_internal_test.go`/`state_internal_test.go`/`boundary_test.go`, most `claim`/`reclaim` hits refer to **mark *claiming*** (the dispatch-OCC) and the **non-nudge reclaim** path — those tests cover the SURVIVING reconciler and **must stay green**. `reconciler_internal_test.go` (46 raw hits) is mostly the kept reclaim/orphan/corrupt-mark suite; only delete assertions that exercise `reclaimNudge`/`createNudge`/`checkClaimWedge` specifically. `boundary_test.go:13`/`state_internal_test.go:123`/`evaluator_internal_test.go` hits are comments or mark-claiming — keep, but update any comment that says "nudge" to present-state prose (no history comment).
- **`export_test.go`** exports nothing nudge-related (verified) — no change expected there.

### 0.I — House rules (binding on every edit).
- **NO history/changelog comments in code.** Never `// Story 13.5 …`, `// Replaces nudge`, `// Was: createNudge`, `// removed the nudge path`, etc. Every comment describes the present state for a reader who never knew a nudge existed. (This is the most-violated rule in this repo — and a teardown is the highest-temptation moment for it. git blame + the commit message are the record.)
- **Frozen contracts are build-to.** `docs/contracts/10-orchestration-surfaces.md` §10.3/§10.8 are **already amended** for this retirement — do **NOT** edit any `docs/contracts/*`. A genuine gap → a `cmd/<area>/CONTRACT-AMENDMENT-REQUEST.md` + a top-of-summary flag (not expected here).
- **New docs → `/docs`.** Do not edit `_bmad-output/planning-artifacts/*`.
- **Sub-agents never commit/push/branch.** Leave changes in the working tree; the lead (Winston) reviews/adjudicates/commits direct to `main` and watches CI.

## Tasks

1. **Delete the nudge package.** `rm -r internal/weaver/nudge/` (claims.go, doc.go, protocol.go, protocol_test.go). This is the only `internal/weaver/*`→`internal/bridge` importer besides `engine.go`.
2. **`engine.go` (§0.D):** remove the `bridge` + `nudge` imports; the `claims`/`adapters`/`nudger` fields; the `NewEngine` wiring; `RegisterAdapter`/`resolveFunc`/`resolveProbe`; `Config.WeaverClaimsBucket`/`Config.ClaimRetention` + their defaults; de-nudge the `Start` ActorKey comment. **Assert post-edit: `internal/weaver` non-test code imports neither `internal/bridge` nor `internal/weaver/nudge`** (AC#3).
3. **`evaluator.go` (§0.B):** excise the nudge branches in `fireEpisode`; delete `nudgeDispatch`/`fireNudge`/`recoverNudge`/`checkClaimWedge`/`nudgeDecision` + `issueKeyNudge`/`issueKeyNudgeWedge`. Keep the plain-submit path + `issueKeyGap`/`issueKeyData`.
4. **`strategist.go` (§0.C):** delete the `actionNudge` case + constant, the `nudgePlan` type, the `plan.nudge` field + its doc.
5. **`state.go` (§0.A):** delete `createNudge`; fold `replaceCarryingClaim` into `replace` (drop the `claimID` param) — recommend; keep the `mark.ClaimID` frozen-shape field but stop writing it; update the `mark` doc to present-state.
6. **`reconciler.go` (§0.A):** delete `reclaimNudge`, the `actionNudge` corrupt-claim guard, the nudge reclaim branch; delete `defaultClaimRetention` + `claimWedgeBound`. Reclaim now always takes the plain `replace` + `fire` path.
7. **`actuator.go`:** delete `deriveResolveRequestID` (≈144–155). Keep `submit`/`scheduleTimer`/`deriveEpisodeRequestID`/`deriveEpisodeTaskID`/`deriveTimerRequestID`/`deriveID` and the `opEnvelope` (all serve surviving actions).
8. **`cmd/weaver/main.go` (§0):** delete the `internal/bridge` import + the `RegisterAdapter("stripe"/"backgroundCheck", …)` loop (≈106–118). The engine no longer exposes `RegisterAdapter`.
9. **`internal/pkgmgr/orchestrationguard.go` (AC#4):** delete the `actionNudge` constant + the `case actionNudge:` in `validateGapAction`; update the unknown-action error to `(triggerLoom | assignTask | directOp)`; update the `validateGapAction` doc comment (drop the "nudge needs Adapter + Operation" line). Update `internal/pkgmgr/{build,definition}.go` doc comments that list `nudge`.
10. **Retire `weaver-claims` (AC#2):** `internal/bootstrap/primordial.go` — delete the `WeaverClaimsBucket` const (≈27) + its `ProvisionBuckets` row (≈73). `internal/bootstrap/verify.go` — drop `WeaverClaimsBucket` from the bucket-enumeration (≈168). `scripts/verify-kernel.go` — drop `bootstrap.WeaverClaimsBucket` from the bucket-enumeration (≈275). **In lockstep.**
11. **Bootstrap version bump 8 → 9 (§0.F):** `nanoid.go checkVersion` (accept "9", reject "8" with regenerate msg) + the version-history comment; `lattice.bootstrap.json` `"version": "9"`. Do NOT touch `PrimordialVertexKeyCount`.
12. **Clean the `internal/bootstrap/` test comments (§0.E):** drop `weaver-claims` from `loom_state_bucket_test.go:18,48` so AC#6's grep is clean.
13. **Docs (AC#5):** `docs/components/weaver.md` — delete the "Two-Phase Nudge" section (≈193–260), the superseded-banner reference to it, the `weaver-claims` bucket rows (≈43–44, ≈398, the §10.3/§10.8 status-table nudge cells ≈431–434), the nudge entries in the component/IO tables (≈378, ≈98, ≈88–89 → `triggerLoom` of an `externalTask`). Rewrite remaining prose to present-state (no history comments). Scan `docs/components/loom.md`/`bridge.md` for any lingering nudge prose and fix.
14. **Tests (§0.H):** delete the 4 whole nudge files; delete `TestWeaverE2E_NudgeStub` + `TestDeriveResolveRequestID_DeterministicAndDisjoint`; triage `reconciler_internal_test.go`/`evaluator_internal_test.go`/`state_internal_test.go`/`boundary_test.go` for nudge-only assertions vs. kept mark/reclaim coverage; update any nudge-mentioning comments in kept tests to present-state.
15. **Coexistence-window proof (AC#7):** `grep -rn 'Action:.*nudge\|"nudge"' packages/` → assert zero gap-action declarations. If non-zero → STOP, surface to lead.
16. **Run the full gate set** (below) and confirm Gate 3 DEFENDED + `make test-lease-convergence` green + the AC#6 clean grep.

## Verification (gates — all must pass before declaring done)

- `go build ./...`
- `make vet`
- `golangci-lint run ./...` (will catch any now-unused import/symbol the teardown orphaned)
- `make verify-kernel` (the lockstep bucket-enumeration + bootstrap-version change is exactly what this guards)
- `make test-bypass` — **Gate 2, all BLOCKED**
- `make test-capability-adversarial` — **Gate 3, all DEFENDED** (the load-bearing proof that convergence/auth did not depend on the nudge plane)
- `make test-lease-convergence` — FR58 still green **through the bridge** (the nudge removal left it intact)
- `go test ./internal/weaver/...` and `go test ./internal/bridge/...` (the two planes directly in scope)
- `go test ./internal/pkgmgr/...` and `go test ./internal/bootstrap/...` (the guard + the provisioning/verify change)
- full `go test ./...`
- **Acceptance greps:** `grep -rn "weaver-claims\|nudge" scripts/ Makefile .github/ internal/bootstrap/` → **clean**; `grep -rn 'import' internal/weaver/*.go | grep -E 'internal/bridge|internal/weaver/nudge'` (non-test) → **empty**; `grep -rn 'Action:.*nudge\|"nudge"' packages/` → **no gap-action hit**.
- **Review:** **full 3-layer adversarial** (Blind Hunter diff-only, Edge Case Hunter diff+repo, Acceptance Auditor diff+spec+contracts). A plane teardown's whole risk is a hidden dependency on the deleted path — the independent lenses are non-negotiable here.

## Files to DELETE vs. MODIFY (the teardown map)

**DELETE (whole files / dirs):**
- `internal/weaver/nudge/` (entire dir: `claims.go`, `doc.go`, `protocol.go`, `protocol_test.go`)
- `internal/weaver/nudge_dispatch_internal_test.go`
- `internal/weaver/reconciler_nudge_internal_test.go`
- `internal/weaver/state_nudge_internal_test.go`

**MODIFY (surgical removals — keep the survivors):**
- `internal/weaver/engine.go` — imports (`bridge`,`nudge`), `claims`/`adapters`/`nudger` fields, NewEngine wiring, `RegisterAdapter`/`resolveFunc`/`resolveProbe`, `Config.WeaverClaimsBucket`/`ClaimRetention` + defaults, the `Start` ActorKey comment
- `internal/weaver/evaluator.go` — `fireEpisode` nudge branches; delete `nudgeDispatch`/`fireNudge`/`recoverNudge`/`checkClaimWedge`/`nudgeDecision`/`issueKeyNudge`/`issueKeyNudgeWedge`
- `internal/weaver/strategist.go` — `actionNudge` case + const, `nudgePlan`, `plan.nudge`
- `internal/weaver/state.go` — delete `createNudge`; fold `replaceCarryingClaim`→`replace`; keep `mark.ClaimID` field (frozen shape), stop writing it
- `internal/weaver/reconciler.go` — delete `reclaimNudge`, nudge corrupt-claim guard, nudge reclaim branch, `defaultClaimRetention`, `claimWedgeBound`
- `internal/weaver/actuator.go` — delete `deriveResolveRequestID`
- `internal/weaver/weaver_e2e_test.go` — delete `TestWeaverE2E_NudgeStub`
- `internal/weaver/requestid_internal_test.go` — delete `TestDeriveResolveRequestID_DeterministicAndDisjoint`
- `internal/weaver/reconciler_internal_test.go`, `evaluator_internal_test.go`, `state_internal_test.go`, `boundary_test.go` — triage nudge-only assertions; update nudge-mentioning comments (most content is kept mark/reclaim coverage)
- `cmd/weaver/main.go` — `internal/bridge` import + the `RegisterAdapter` block
- `internal/pkgmgr/orchestrationguard.go` — `actionNudge` const + `validateGapAction` case + error msg + doc
- `internal/pkgmgr/build.go`, `internal/pkgmgr/definition.go` — doc comments listing `nudge`
- `internal/bootstrap/primordial.go` — `WeaverClaimsBucket` const + provisioning row
- `internal/bootstrap/verify.go` — claims bucket in the verify enumeration
- `internal/bootstrap/nanoid.go` — `checkVersion` 8→9 + version-history comment
- `internal/bootstrap/loom_state_bucket_test.go` — drop `weaver-claims` from the 2 comments
- `lattice.bootstrap.json` — `"version": "9"`
- `docs/components/weaver.md` — drop the Two-Phase Nudge section, `weaver-claims` rows, nudge-action cells (present-state rewrite)
- `docs/components/loom.md`, `docs/components/bridge.md` — scan/fix any lingering nudge prose

**KEEP (explicitly — these survive the teardown):**
- `internal/weaver/state.go` `markStore` + the `mark.ClaimID` frozen-shape field; `reconciler.go` sweeper (non-nudge reclaim/orphan/corrupt paths); `actuator.go` (`submit`/`scheduleTimer`/episode+task+timer derivations); the control API (`control.go`, `internal/weaver/control/*`); lane-3 temporal (`temporal.go`); `internal/bridge/*` (the relocated adapter contract + `Fake*` + the FR58 proof) — untouched.

## Questions for the lead

1. **`replaceCarryingClaim` — fold or keep?** §0.A recommends folding it into `replace` (drop the `claimID` param) since no surviving caller passes a non-blank claimId. Confirm you want the fold (cleaner) vs. keeping the helper with a dead param. Low-risk either way; flagging because it touches the `markStore` API surface that the kept reconciler tests exercise.
2. **`mark.ClaimID` field — keep or drop?** §10.3's frozen value shape is `{...,claimId?,...}` (`omitempty`). I default to **keeping** the struct field (a frozen-shape field, never written, harmless) so we build *to* the contract rather than narrowing it. If you'd rather drop the field from the Go struct (the JSON stays absent either way via `omitempty`), say so — but that arguably diverges from the frozen shape, so I left it in.
3. **Coexistence-window AC — confirmed clean at draft.** No package declares `Action: "nudge"`; lease-signing uses `triggerLoom`/`assignTask` only (`packages/lease-signing/targets.go`). I did not find any OUTSIDE-weaver consumer of the nudge path or `weaver-claims` beyond the enumerated set (pkgmgr guard, bootstrap provisioning/verify, verify-kernel, cmd/weaver adapter wiring, docs, and the two bootstrap test comments). If you know of an out-of-tree package or a downstream that authors a `nudge` gap, that is the one blocker that would defer this story.
4. **Bootstrap version 8 → 9.** I'm treating the dropped `weaver-claims` bucket as a kernel-topology change that requires the version bump (AC#2 says "bootstrap-file version bump"). Confirm 9 is the next value (it's currently 8 from 13.3's bridge identity). `make down && make up` will be needed locally after this lands — noting it for your CI watch.
