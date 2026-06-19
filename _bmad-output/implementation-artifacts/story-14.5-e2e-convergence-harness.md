# Story 14.5 — e2e convergence harness + `test-lease-convergence` gate (the final Epic-14 story)

**Status:** review — landing with Story 14.6 (CI pending). Story 14.6 (orchestration dispatch integration) built + 3-layer-reviewed + fix-forward applied, so the harness drops the dispatch shim and AC#1 (faithful orchestration through the live bridge) + AC#2b (eager re-open via the real MarkExpired DDL) are now genuinely Met. AC#2a (FR58, with a positive control) / AC#3 (D5-by-gate) / AC#4 (gate+CI) Met. H1 (window margin, now window-derived ~8m harness ctx) + M3 (leaseconvergence build-tag-gated, runs once) fixed in 14.6. The `applyMatch` engine bugfix landed standalone earlier (e560188). Flips to `done` after CI green. (Historical: the e2e originally surfaced the dispatch gap as 3 red flags — see "Post-review adjudication (Winston)" at the foot.)
**Epic:** 14 — Loftspace Lease-Application Reference Vertical (the closing story)
**Tier:** Opus — the **Epic 14 capstone + the one engine fix Epic 14 still owes**. Two halves, interdependent: (1) a **drain-then-assert e2e harness** that drives a fresh lease application to **steady-state convergence through the LIVE bridge** (Weaver playbook → `triggerLoom` onboarding + bgcheck/payment `externalTask` → live `internal/bridge` → `replyOp` reproject → temporal freshness → sign task), plus the **at-most-once external-effect (FR58)** and **D5 gate-enforcement** proofs; and (2) the **EAGER bgcheck-freshness auto-reopen** carried from 14.4 — projecting a single scalar `freshUntil` column per anchor so Weaver's temporal `@at` lane re-touches the row the instant freshness lapses, which **requires an engine change 14.4 deliberately did not make** (the `internal/refractor/ruleengine/full` OPTIONAL-MATCH null-restore fix, §0.B). A new **`test-lease-convergence` CI gate** wires the e2e into CI. Review: **full 3-layer adversarial** (Blind Hunter / Edge Case Hunter / Acceptance Auditor) per `bmad-code-review` — this touches a guarded engine (Refractor) AND authors a cross-engine e2e on the orchestration plane. Plus the gates in §7.

**Epic spec:** `_bmad-output/planning-artifacts/epics/phase-2-epics.md` → "Story 14.5: e2e convergence harness + `test-lease-convergence` gate" (~735–750) + the Epic 14 framing (~662–671) + the build order (14.1/14.2/14.3 → 14.4 → **14.5**, 14.5 unblocks 13.5). Read it for the four ACs (verbatim in §1).

**Binding grounding (FROZEN / OWNED — read, build TO, do NOT edit):**
- **Contract #10** (`docs/contracts/10-orchestration-surfaces.md`) — **§10.2** (the `weaver-targets` row + the **`freshUntil` engine-recognized convention column**, lines ~147–149 + the §10.2/R3 revision row ~976; the `missing_bgcheck = NOT EXISTS(check WHERE date > now − window)` retraction note ~166); **§10.4** (ADR-51 message scheduling — the `@at` one-shot + the per-target-per-entity `schedule.weaver.timer.<targetId>.<entityId>` subject + the R2 revision row ~976 — the lane the eager freshness rides); **§10.5/§10.6** (the externalTask completion model — `completionDomains: ["orchestration"]`, the `replyOp` emits `orchestration.externalTaskCompleted{externalRef}`, the creation-deadline disarms on instanceOp-commit; the revision rows ~980); **§10.8** (the playbook). **FROZEN — build to them.** (The §10.2 `freshUntil` convention is the contract surface the eager-reopen half implements; it is already engine-recognized — Weaver reads it today, see §0.A — so no §10.2 amendment is needed; only the Refractor *projection* of that column is missing.)
- **Contract #6** (`docs/contracts/06-*.md`) §6.13 — the **scalar-passthrough amendment (CAR E6)** 14.4 landed: an actorAggregate lens body column whose RETURN value is a scalar projects verbatim (not realness-filtered to a list). The `freshUntil` column the eager-reopen half adds is **another scalar body column** riding this same passthrough — no further Contract #6 change anticipated (confirm — Q3).
- **Contract #1 §1.1** (`docs/contracts/01-key-shapes.md`) — key shapes; the lowercase `leaseapp` type segment; the link sentence rule. **FROZEN.**
- **Contract #4** (`docs/contracts/04-*.md`) — the `vtx.op.<requestId>` op-tracker dedup (the FR58 foundation the at-most-once proof leans on: `deriveReplyRequestID` + create-only outcome collapse on this tracker). **FROZEN.**
- **D5 — task/service DDL data placement (LOCKED)** (`_bmad-output/planning-artifacts/lattice-architecture.md` ~1167) — minimum data in the vertex root, business data in **aspects**. The service instance's external outcome lives in the **`.outcome` aspect**; the `leaseapp`/`service` vertex root `data` stays minimal. **14.5's headline is that the harness ASSERTS this (AC #3 — D5 "enforced by gate, not review").** Planning artifact — do **not** edit.

**Grounding (the code you build ON — read; the harness/gate/engine-fix are yours to author):**
- **The package under test (14.4, DONE) — `packages/lease-signing/` IN FULL:** `lenses.go` (the `leaseApplicationComplete` actorAggregate convergence lens + the bgcheck-freshness **PREDICATE**; read the long doc comment ~104–119 — it spells out *exactly* what 14.5 must land: the eager `freshUntil` column + the two engine-change options); `patterns.go` (bgcheck/payment `externalTask` + onboarding `userTask`, all `completionDomains: ["orchestration"]`); `targets.go` (the §10.8 playbook); `ddls.go` + `scripts.go` (the `leaseapp` DDL + `CreateLeaseApplication`/`SignLease`; the externalTask `instanceOp` `CreateLeaseServiceInstance` that mints `vtx.service.<handle>` + emits `external.<adapter>`; the **READ-FREE** `replyOp` `RecordLeaseServiceOutcome` that stamps `validUntil = completedAt + bgcheckFreshnessWindow` via `time.rfc3339_add` and emits `orchestration.externalTaskCompleted{externalRef}`; the `bgcheckFreshnessWindow = "5m"` constant at `scripts.go:287` — already deliberately short for 14.5's e2e); `package.go`, `manifest.yaml`, `README.md` (read the **"Deferred to 14.5 — eager auto-reopen-at-expiry"** section ~106–126 + the **"Freshness"** section ~79–104 — they ARE the spec for the carried work, including the FR58 drop-→-double-act hazard); and the existing tests (`lease_signing_test.go`, `lens_unit_test.go`, `lens_cypher_test.go` — esp. `TestLeaseApplicationComplete_PaymentInstanceNoBgcheck_NoDrop` at `lens_cypher_test.go:408`, the no-drop regression 14.5 builds on).
- **The bridge (13.4, DONE) — `internal/bridge/`:** `dispatch.go` (`handleExternal` ~86–181; `externalEvent` ~26–44 — the event body the instanceOp produces; the `{externalRef, result}`-only reply payload ~164–167 — the §0.B constraint the replyOp already honors), `actuator.go` (`submit` ~55–76 — posts the replyOp PAYLOAD-ONLY, NO `authContext`, under the root-equivalent bridge actor), `token.go` (`deriveReplyRequestID` ~26 — FR58 determinism), `fake_background_check.go` + `fake_stripe.go` (the Fake adapters; their registered names are `backgroundCheck`/`stripe` per `cmd/bridge/main.go`). **The e2e DRIVES this live bridge** (14.4's tests used direct outcome-aspect writes; 14.5 is the bridge-driven proof). `engine.go` (`NewEngine`/`Start`/`RegisterAdapter` — how the harness boots the bridge) + `export_test.go` + `fr58_test.go` (the bridge-only FR58 proof — the at-most-once pattern 14.5 extends end-to-end) + **`bridge_e2e_test.go` (the closest existing full-bridge-loop harness — mirror its `startNATS`/`provision`/`startBridge`/`publishExternalEvent`/`fakeProcessor`-Contract-#4-dedup shape; 14.5 swaps the fake Processor for the REAL one).**
- **Loom externalTask (13.2, DONE) — `internal/loom/`:** `engine.go` (`submitExternalTask`, `onExternalTaskDeadline`; the `StepTimeout`/`CreateTaskTimeout` deadline knobs ~62–75 — the creation-deadline disarms on instanceOp-commit), `pattern.go` (the `Step`/`externalTask` shape ~28–48), `external_e2e_test.go` (the externalTask seam e2e — the `waitExternalHandle`/`submitReplyOp` idioms; the real DDLs replace its fixtures in 14.5).
- **Weaver temporal lane (the freshness `@at` mechanism) — `internal/weaver/`:** `temporal.go` IN FULL — `freshUntilColumn = "freshUntil"` (~38), `scheduleFreshness` (~94 — reads the row's `freshUntil`, publishes the per-target-per-entity `@at` schedule, re-arms idempotently on every delivery), `handleFiredTimer` (~193 — the fired-timer → `MarkExpired` op under a §10.4 deterministic requestId, with a read-before-act guard), `currentFreshUntil` (~314). `evaluator.go` — `handleRow` (~21 — the lane-1 row handler; the `scheduleFreshness` call ~69; **`clearClosedMarks` ~433 — the FR58 hazard: a dropped/empty-body row clears EVERY mark → re-dispatch → a second externalTask**), `boolColumn` (~452). **The mechanism Weaver needs is ALREADY THERE — Weaver reads `freshUntil` and schedules the `@at` today; 14.5's job is to make Refractor PROJECT that column (§0.A/§0.B).**
- **The full rule engine — `internal/refractor/ruleengine/full/executor.go`:** **`applyMatch` ~118–177 — THE BUG the eager-freshness half must fix (§0.B).** Read it line-by-line. `equalsAny` (`= null` IS the null test — never "correct" it to `IS NULL`), `compareAny` (string `>` is lexicographic = chronological on RFC3339-UTC — the freshness compare), `matchPatterns`/`matchPath` (~210+ — the null-binding path). The 14.4 lens cypher (`packages/lease-signing/lenses.go` ~120–140) is the working one-fan-no-WHERE shape; the eager `freshUntil` change must NOT regress it.
- **Refractor projection — `internal/refractor/projection/`:** `output.go` (`BuildKey`/`KeyColumn` — 14.2's bare-NanoID key), `driver.go` (the actorAggregate `EnvelopeFn` — the CAR-E6 scalar-passthrough path the `freshUntil` column rides). `internal/refractor/pipeline/evaluate.go` — `executeFullForActor` (~168 — **sets `params["now"] = now.Format(time.RFC3339)` at ~177**, the `$now` the freshness predicate reads), `guardOutputKeyCollision` (~239–273 — the §0.C one-row-per-anchor fail-closed guard the lens must keep satisfying).
- **The existing convergence/e2e harnesses to mirror:** `internal/refractor/refractor_leasesigning_scalar_e2e_test.go` (**the closest — the 14.4 dev's harness: installs the REAL lease-signing lens via the real `InstallPackage` → meta-lane Processor → atomic commit, activates the live `lens.CoreKVSource` watch, wires the production `projection.InstallActorAggregate`, writes the leaseapp/identity/service fixture into Core KV, asserts the projected `weaver-targets` row carries scalar columns**); `internal/refractor/refractor_keycolumn_convergence_e2e_test.go` (the 14.2 keyColumn e2e); `internal/weaver/weaver_e2e_test.go` (the Weaver lane-1/lane-3 wiring + the `core-schedules` `AllowMsgSchedules`+`MaxMsgsPerSubject:1` provisioning the temporal lane needs); `internal/loom/external_e2e_test.go` + `internal/bridge/bridge_e2e_test.go` (the externalTask + live-bridge loops). **14.5's harness is the UNION of these — the first test that boots Processor + Refractor + Loom + Weaver + the live bridge together against one installed package.**
- **CI gate conventions — `Makefile`:** read `test-bypass` (~145), `test-capability-adversarial` (~159), `test-hello-lattice` (~172 — the closest *integration* gate template: `-tags integration`, env vars, `-p 1 -count=1 -timeout 30m`, a Docker stack via `make up`), `test-health-completeness` (~183), `test-rollback` (~189 — the closest *self-contained embedded-NATS* gate template), `verify-kernel` (~62), `.PHONY` (~18). **The new `test-lease-convergence` gate must match whichever template fits its posture (§0.D / Q5).**
- **Where packages register for install:** `cmd/lattice-pkg/main.go` (~28–48 — `lease-signing` + `orchestration-base` + `service-domain` already registered; the install chain rbac → identity → orchestration-base → service-domain → lease-signing).

**Depends on:** **14.4 + 13.4** (both DONE, CI green). 14.4 = the `lease-signing` package (lens + patterns + the externalTask DDLs + the freshness PREDICATE). 13.4 = the live bridge (the `Fake*` adapters + the `{externalRef, result}` reply + `deriveReplyRequestID`). **Also leans on** 14.2 (keyColumn), 13.2 (Loom externalTask), 9.x (Weaver lanes 1+3), 7.x (the task model). **Forward:** 14.5 green **unblocks 13.5** (retire Weaver's nudge — that story's AC confirms lease-signing used `triggerLoom`, never a `nudge` gap, during the coexistence window; 14.5 is the proof the `triggerLoom` path converges end-to-end).

**Workflow:** you are the DS (dev) sub-agent. Repo root, no worktree. Do **NOT** commit/push/branch. Do **NOT** edit frozen contracts (`docs/contracts/*`) or planning artifacts (`epics/*.md`, `lattice-architecture.md`, `prd.md`, the change proposals). New docs/notes go in the **package README** (`packages/lease-signing/README.md`), `/docs`, or a `docs/components/*` file — never `_bmad-output/`. A genuine frozen-contract gap → a `cmd/<area>/CONTRACT-AMENDMENT-REQUEST.md` entry + flag at the TOP of your closing summary (note: Andrew may amend a contract in-place this session — still flag the gap explicitly; do not edit the contract yourself). Leave all changes in the working tree for Winston.

> **TOP-OF-STORY FLAGS — read before you start. There are FIVE binding overrides; they govern the whole story.**
>
> 1. **The eager-freshness half NEEDS an engine change — it is IN SCOPE, not deferred again (§0.A/§0.B).** 14.4 shipped the freshness *predicate* (a stale bgcheck re-opens **lazily** on the next reprojection). 14.5 lands the **eager** auto-reopen: a single scalar `freshUntil` column projected per anchor so Weaver's temporal `@at` lane re-touches the row the instant it lapses. Projecting that scalar cleanly requires fixing the `internal/refractor/ruleengine/full/executor.go` `applyMatch` OPTIONAL-MATCH null-restore bug **OR** adding a list→scalar reducer. **Recommended: approach (a) — the executor null-restore fix** (it is a real latent bug worth fixing, and it then unlocks a dedicated family-filtered bgcheck OPTIONAL MATCH that reads `freshUntil` as a scalar). The final pick is the dev's with rationale (Q1). This is a **guarded-engine change** — full 3-layer review.
> 2. **The FR58 drop-→-double-act hazard MUST be encoded as a trap/AC (§0.C).** A dropped `weaver-targets` row makes `clearClosedMarks` (`internal/weaver/evaluator.go` ~433) wipe **ALL** gap marks → re-dispatch → a **second** bgcheck Loom instance → **FR58 double-act** (a second real external call). So the eager-freshness change MUST **preserve the anchor row** (never drop it to null/empty), and the harness MUST assert **no double-dispatch** when the window lapses and re-arms. **Build on the existing no-drop regression** (`TestLeaseApplicationComplete_PaymentInstanceNoBgcheck_NoDrop`, `lens_cypher_test.go:408`) — extend it to the eager-`freshUntil` shape so the new dedicated bgcheck match still never drops the anchor in the payment-but-no-bgcheck transient window.
> 3. **The e2e drives the LIVE bridge, end-to-end (§0.E).** Unlike 14.4 (direct outcome-aspect writes), 14.5's harness boots the **real** `internal/bridge` engine with the **real** `Fake*` adapters registered, and lets the full loop run: `CreateLeaseApplication` → Refractor projects the violating row → Weaver dispatches `triggerLoom`(bgcheck/payment/onboarding) + `assignTask`(SignLease) → Loom submits the `instanceOp` + emits `external.<adapter>` → **the live bridge calls the adapter + posts the `replyOp`** → the replyOp records `.outcome` + emits `orchestration.externalTaskCompleted` → Loom completes the pattern → Refractor reprojects → `violating` flips false. **Drain-then-assert:** observe `violating` flip false AND **remain** false (steady state) within a bounded window (Quinn's pattern — §0.F).
> 4. **D5 is GATE-ASSERTED, not review-asserted (§0.G / AC #3).** The harness itself asserts (a) the service instance's external outcome lives in the **`.outcome` aspect** and (b) the `leaseapp`/`service` vertex root `data` stays minimal (`{}`). This is an **automated assertion inside the `test-lease-convergence` gate**, so a future regression that fattens root data fails CI, not a reviewer's eye.
> 5. **Type-agnostic engines stay type-agnostic (§0.H / invariant a).** The Refractor engine fix (the `applyMatch` null-restore) is **generic** — it fixes OPTIONAL-MATCH semantics for ALL cyphers, names no type, and is proven by a type-neutral rule-engine test (NOT a `leaseapp`/`service` fixture). The `leaseapp`/`service` concrete types live ONLY in `packages/lease-signing` + the e2e harness (the harness is a *test* of the real vertical, so it legitimately uses the real types — like 14.4's tests). **No `leaseapp`/`service` literal may leak into `internal/*` engine/non-test code** — the 14.4 `TestLeaseAppType_AbsentFromCore` invariant-a guard must still pass.

---

## 0. THE HEADLINE — prove the vertical converges end-to-end through the live bridge AND land the one engine fix that makes freshness eager (read first; it governs everything)

14.5 is the **capstone**: every brick is shipped (the lens, the patterns, the externalTask DDLs, the live bridge, the Weaver lanes, the temporal `@at` mechanism), and 14.4 proved the seams via **direct writes**. 14.5 proves they **compose end-to-end through the live bridge to a stable steady state**, proves the external effect is **at-most-once** under retry, gate-enforces **D5**, and lands the **one engine change** 14.4 explicitly carried forward (the eager `freshUntil` projection, gated on the executor null-restore fix). Get these seven facts right and the gate is green.

### 0.A — The eager-freshness mechanism is HALF-built upstream: Weaver reads `freshUntil`, Refractor does not project it yet

This is the crux of the carried work, and it is **smaller than it looks** because the consuming side is done:

- **Weaver ALREADY consumes `freshUntil`.** `internal/weaver/temporal.go` defines `freshUntilColumn = "freshUntil"` (~38) and `scheduleFreshness` (~94) reads `row["freshUntil"]`, parses it as RFC3339, and publishes a per-target-per-entity `@at` schedule on `schedule.weaver.timer.<targetId>.<entityId>` (§10.4). `handleFiredTimer` (~193) converts the firing into a `MarkExpired` op (which re-touches the row → reprojection → the freshness predicate re-evaluates → the gap re-opens). **All of this works today** — there is simply no lens projecting a `freshUntil` column for it to read.
- **Refractor does NOT project `freshUntil` yet.** The 14.4 lens (`packages/lease-signing/lenses.go`) deliberately omits the `freshUntil` column. Its doc comment (~104–119) spells out exactly why: projecting a single scalar `freshUntil` per anchor (the bgcheck's `validUntil`) **cleanly** needs an engine capability the `full` engine lacks — either a list→scalar reducer (to reduce `validUntil` over the providedTo fan), or a **dedicated family-filtered bgcheck OPTIONAL MATCH** that reads `freshUntil` as a scalar (which is unsafe today because of the `applyMatch` bug, §0.B).
- **So 14.5's eager-freshness half is exactly: (1) fix the engine so a dedicated bgcheck match is safe (§0.B), (2) add the `freshUntil` column to the lens cypher, (3) prove Weaver schedules the `@at` and the row re-opens eagerly at lapse.** No `internal/weaver` change is needed (the consumer is done); no §10.2 amendment is needed (`freshUntil` is already the engine-recognized convention column). **Confirm no Weaver change is needed — Q2.**

### 0.B — The `applyMatch` OPTIONAL-MATCH null-restore bug (the engine fix; approach (a), recommended)

`internal/refractor/ruleengine/full/executor.go` `applyMatch` (~118–177) implements OPTIONAL MATCH ... WHERE. The intent (comment ~125–127): "WHERE filters MATCH'd rows but if all matches are filtered out, the optional null-binding preserves the original binding." The bug is in the restore (~154–172):

```go
if m.Optional && hadNonNullMatch {
    // Drop the null-preserving fallback rows when at least one real match exists.
    filtered := passing[:0]
    for _, nb := range passing {
        if isNonNullExpansion(b, nb, m.Patterns) { filtered = append(filtered, nb) }
    }
    // If all real matches got filtered by WHERE, restore the null fallback.
    if len(filtered) == 0 {
        for _, nb := range expanded {
            if !isNonNullExpansion(b, nb, m.Patterns) { filtered = append(filtered, nb); break }  // ← searches `expanded` for a null row
        }
    }
    passing = filtered
}
```

**The defect:** the restore loop searches `expanded` for a null-preserving row. But when the pattern matched **only real neighbors** (`matchPath` returned non-empty, so `matchPatterns` never emitted a null-bound expansion — see `matchPatterns` ~214–239: the null-bind branch only fires when `len(expansions) == 0`), there **is no null row in `expanded`** to find. So when a WHERE filters out *all* the real matches, `filtered` stays empty → the **anchor row drops entirely**. For the lease lens this is the "applicant has a payment neighbor but no fresh bgcheck → a dedicated `OPTIONAL MATCH (id)<-[:providedTo]-(bg:service) WHERE bg.family = 'backgroundCheck' AND bg.outcome.validUntil > $now` filters the sole neighbor → the leaseapp anchor vanishes" case — and a vanished `weaver-targets` row reads to Weaver as an entity deletion (§0.C).

**The fix (approach a, recommended):** when all real matches are WHERE-filtered, **construct the null fallback from the source binding `b`** (null-bind every newly-introduced pattern variable, exactly as `matchPatterns`'s `len(expansions) == 0` branch does) instead of searching `expanded` for a row that may not exist. Factor the null-bind into a shared helper both call sites use. This makes a fully-filtered OPTIONAL MATCH preserve the anchor with nulls — the correct Cypher semantics — for **every** cypher, not just the lease lens. **It is a generic, type-agnostic engine fix (§0.H).**

**Why approach (a) over (b):** option (b) — add a list→scalar reducer (`max`/`head`/`coalesce`/`UNWIND`, all verified unsupported per the 14.4 README) so `collect(validUntil)` reduces to a scalar without a dedicated match — is a larger, more speculative grammar extension, and it leaves the latent `applyMatch` bug unfixed (a real correctness hole any future filtered-optional cypher would hit). **(a) fixes a real bug and is the smaller surface.** **The dev makes the final call with rationale (Q1)** — but the brief recommends (a) and the rest of this story is written assuming it.

### 0.C — The FR58 drop-→-double-act hazard (the trap the eager change must NOT introduce)

`internal/weaver/evaluator.go` `clearClosedMarks` (~433) clears every gap mark for a row when the row's `missing_*` columns are not true — **and an empty-body row (the §10.2 deletion tombstone) clears EVERY mark** (`handleRow` ~37–45 + `clearClosedMarks`'s `row == nil` path). If the eager-`freshUntil` change drops the anchor row (the §0.B bug, un-fixed), the sequence is:

1. payment instanceOp commits + reprojects **before** bgcheck's (a real transient window — payment and bgcheck dispatch in parallel);
2. a **dedicated** bgcheck OPTIONAL MATCH with a WHERE filters the (absent or not-yet-fresh) bgcheck neighbor → **the anchor row drops** (the un-fixed bug);
3. Weaver sees an empty/absent row → `clearClosedMarks` wipes the `missing_bgcheck` mark;
4. the row re-appears (bgcheck instance lands) → `missing_bgcheck` true again, **no in-flight mark** → Weaver **re-dispatches** `triggerLoom(backgroundCheck)` → a **SECOND** bgcheck Loom instance → a **second real external call** → **FR58 double-act**.

**So the §0.B fix is load-bearing for FR58, not just correctness.** Once `applyMatch` preserves the anchor with nulls, the row never drops, the mark is never wrongly cleared, and the re-arm at freshness-lapse is a *clean* re-dispatch of ONE instance (the prior one's outcome is stale, the mark was cleared by the legitimate freshness flip, exactly one new call). **The harness MUST assert: when the bgcheck window lapses and the gap re-opens, exactly ONE new external call results (not two), AND in the payment-before-bgcheck transient window the anchor row never drops.** The 14.4 no-drop regression test is the unit-level guard; the e2e is the integration-level proof.

### 0.D — The new gate's posture: embedded-NATS in-process, NOT a Docker stack (recommended)

The existing gates split two ways: **Docker-stack integration** (`test-bypass`, `test-capability-adversarial`, `test-hello-lattice` — `make up` then `go test -tags integration`) vs **self-contained embedded-NATS** (`test-rollback` — `go test` against an in-process `natstest.RunServer`, no Docker). **Every harness this story mirrors (`refractor_leasesigning_scalar_e2e_test.go`, `weaver_e2e_test.go`, `loom/external_e2e_test.go`, `bridge/bridge_e2e_test.go`) uses embedded NATS in-process** — they boot the engines as goroutines against one `natstest` server, no Docker. **Recommended: `test-lease-convergence` is a self-contained embedded-NATS gate** (mirror `test-rollback`'s Makefile shape: no `make up`, `go test ./<harness-pkg>/... -run <Test> -v -p 1 -count=1 -timeout <N>m`), because (1) it is faster + hermetic, (2) it matches the e2e harnesses' established pattern, and (3) it avoids the Docker-stack flakiness noted for `test-hello-lattice`. **Confirm the posture + the exact target body — Q5.** (If the e2e genuinely needs the Docker stack — e.g. it must exercise the *deployed* bridge process rather than an in-process `bridge.NewEngine` — say so and mirror `test-hello-lattice` instead; but the in-process bridge engine is the established, hermetic choice.)

### 0.E — The harness is the UNION of five existing harnesses (the first all-engines-together e2e)

No existing test boots **Processor + Refractor + Loom + Weaver + the live bridge** against one installed package. 14.5's harness composes them:

- **Install** the chain (rbac → identity → orchestration-base → service-domain → **lease-signing**) via the real `InstallPackage` op path (mirror `refractor_leasesigning_scalar_e2e_test.go`'s installer wiring + `cmd/lattice-pkg/main.go`'s registry).
- **Boot the engines** as goroutines against one embedded NATS: the **Processor** (commits ops, runs the DDL scripts), **Refractor** (the live `lens.CoreKVSource` watch + `projection.InstallActorAggregate` for the convergence lens → `weaver-targets`), **Loom** (the trigger/relay/deadline consumers + the orchestration consumer that advances externalTask/userTask completion), **Weaver** (lane-1 `handleRow` dispatch + lane-3 `scheduleFreshness`/`handleFiredTimer`), and the **live bridge** (`bridge.NewEngine` with `FakeBackgroundCheck`+`FakeStripe` registered).
- **Provision** every bucket/stream each engine needs — critically `core-schedules` with `AllowMsgSchedules: true` + `MaxMsgsPerSubject: 1` (the temporal lane; mirror `weaver_e2e_test.go`'s `provision`) so the eager-`@at` actually fires.
- **Drive** one `CreateLeaseApplication` (applicant identity with all gaps open) and let orchestration run unattended.

This is a substantial harness — budget for it. It is one coherent test file (or a small harness package); see §0.F for the assert strategy and §6 for the test list.

### 0.F — Drain-then-assert (Quinn's pattern): converge AND stay converged

A naive "wait until `violating == false`" is flaky and incomplete — it can catch a transient false before a later gap re-opens, and it does not prove **steady state**. Quinn's drain-then-assert:

1. **Drain:** poll the `weaver-targets` row until `violating` flips `false` within a bounded deadline (generous — the loop crosses five engines + a `5m`-window-independent bgcheck; the bgcheck `validUntil` is far enough ahead during the converge phase that it counts). Fail loudly with the last-seen row + per-engine Health KV issues on timeout.
2. **Assert steady:** after the flip, **hold** for a settle window and assert `violating` **stays** false (no oscillation — no gap re-opens, no duplicate dispatch re-violates). Read the row repeatedly; assert it is stable.
3. **The eager-freshness leg is its OWN drain-then-assert:** after steady-state, exercise the **short** `bgcheckFreshnessWindow` — either use the shipped `5m` and `t.Skip` under `-short`, OR (recommended, Q4) make the window **test-tunable** to seconds so the e2e watches the bgcheck `validUntil` lapse → Weaver's `@at` fires → `MarkExpired` → the row re-opens `missing_bgcheck` → Weaver re-dispatches **ONE** bgcheck → the bridge re-completes → `violating` re-converges. Assert the re-open happened **eagerly** (driven by the `@at`, not by an incidental CDC touch) AND exactly **one** new external call occurred (the §0.C FR58 assertion).

**The window tunability (Q4):** `bgcheckFreshnessWindow` is a package constant (`scripts.go:287`, currently `"5m"`). For the e2e to watch a lapse in bounded wall-clock, either (a) keep `5m` and accept a `5m+` e2e (too slow for CI), or (b) make the window injectable for the test (a build-tagged override, a package var the test sets, or a second short-window pattern). **Recommended: a test-injectable window** (smallest e2e). **Confirm the mechanism — Q4** (and keep the production default at a sane real value, not seconds).

### 0.G — D5 gate-assertion (AC #3 — the headline that distinguishes 14.5 from 14.4)

14.4 asserted D5 in *package* tests (reviewer-adjacent). 14.5's AC #3 is explicit: **"the harness asserts the instance's outcome lives in an aspect, root `data` minimal — D5 enforced by gate, not review."** So the e2e, after the bridge round-trip, reads Core KV and asserts: (a) `vtx.service.<handle>.outcome` exists with `{status, completedAt, validUntil}` (the aspect); (b) `vtx.service.<handle>` root `data` is `{}` (minimal); (c) `vtx.leaseapp.<id>` root `data` is `{}` (the signature is in the `.signature` aspect, not root). Because this runs inside `test-lease-convergence`, a regression that fattens root data **fails the gate**.

### 0.H — Type-agnostic engines stay type-agnostic (invariant a)

The Refractor `applyMatch` fix is **generic** (OPTIONAL-MATCH semantics for all cyphers) and is proven by a **type-neutral rule-engine test** (a throwaway cypher over a generic fixture, NOT `leaseapp`/`service`) — mirror the existing `internal/refractor/ruleengine/full` test style. The `leaseapp`/`service` types appear ONLY in `packages/lease-signing` + the e2e harness (a *test*, legitimately using the real vertical's types). The 14.4 `TestLeaseAppType_AbsentFromCore` guard (asserting `leaseapp`/op tokens are absent from `internal/*` non-test code) must still pass — the engine fix adds no concrete-type literal. **Note this in your summary** so a reviewer does not flag the harness's use of real types (it is the real vertical's e2e, by design — epics invariant a was already proven type-blind in Epic 13).

---

## 1. The four ACs (verbatim) + adjudication

### The ACs (from `phase-2-epics.md` ~739–748)

> **Given** a fresh lease application with all gaps violating, from `InstallPackage` on an otherwise minimal core
> **When** orchestration runs (Weaver → `triggerLoom` onboarding + bgcheck/payment `externalTask` → bridge → result ops reproject → temporal freshness → sign task)
> **Then** a **drain-then-assert** harness observes `violating` flip `false` and **remain** false (steady state) within a bounded window
> **And** a **retried external call does not double-act** (FR58 end-to-end through the bridge); the bgcheck freshness predicate is exercised via a short ADR-51 window
> **And** the harness **asserts the instance's outcome lives in an aspect, root `data` minimal** (D5 enforced by gate, not review)
> **And** a new **`test-lease-convergence` CI gate** is added (Gate 2/3/5 don't cover an external-I/O idempotency loop)

### Adjudication — what each AC binds

- **AC #1 → §2 Items A+B (the drain-then-assert e2e harness).** "fresh lease application, all gaps violating, from `InstallPackage` on a minimal core" = the harness installs the chain + creates one `CreateLeaseApplication` for an applicant with no PII/bgcheck/payment/signature (§2 Item A). "orchestration runs (Weaver → triggerLoom onboarding + bgcheck/payment externalTask → bridge → result ops reproject → temporal freshness → sign task)" = the full live-bridge loop boots and runs unattended (§0.E, §2 Item A). "drain-then-assert … `violating` flip false and **remain** false (steady state) within a bounded window" = §0.F's two-phase poll-then-hold (§2 Item B).
- **AC #2 → §2 Items C+D (FR58 end-to-end + the short-window freshness).** "a retried external call does not double-act (FR58 end-to-end through the bridge)" = drive a **redelivery** of an `external.<adapter>` event (or restart/re-publish leg) through the live bridge and assert exactly **one** external effect (one `.outcome` aspect, one Loom completion) — leaning on `deriveReplyRequestID` determinism + the create-only `.outcome` collapse on the Contract #4 tracker (§2 Item C). "the bgcheck freshness predicate is exercised via a short ADR-51 window" = the eager-`freshUntil` leg: project the column, let the short window lapse, assert the `@at` fires + the row re-opens + exactly one re-dispatch (§2 Item D, the §0.A/§0.B/§0.C centerpiece).
- **AC #3 → §2 Item E + §0.G (D5 gate-asserted).** The harness asserts the `.outcome` aspect carries the outcome and the `leaseapp`/`service` root `data` stays `{}` — inside the gate, so it is CI-enforced (§2 Item E).
- **AC #4 → §2 Item F + §0.D (the `test-lease-convergence` gate).** A new Makefile target wiring the harness into CI, matching the established gate conventions (§2 Item F).

### The two Epic-13/14 invariants on these ACs (Andrew; epics ~579–581 — they apply to Epic 14)

- **(a) type-agnostic engines — CONSUMED + PRESERVED, not re-proven.** Epic 13 proved the engines/bridge are type-blind via a non-`service` fixture. 14.5's engine fix (`applyMatch`) is **generic** and proven type-neutrally (§0.H); the harness uses the real `service`/`leaseapp` types because it is the real vertical's e2e. **No concrete-type literal enters `internal/*` non-test code — the 14.4 invariant-a guard still passes.** Note this in your summary.
- **(b) D5 — GATE-ENFORCED here (the AC #3 headline).** The `.outcome` aspect carries the external outcome; the `leaseapp`/`service` root `data` stays minimal; **the gate asserts it** (§0.G). This is the strongest D5 statement in the codebase — a regression fails CI.

### Scope boundary

**In scope:**
1. **The drain-then-assert e2e harness** — a new test (file or small harness package; author's call, §9) that boots Processor + Refractor + Loom + Weaver + the live bridge against one embedded-NATS server, installs the real chain, drives one lease application, and observes end-to-end convergence to a stable steady state (§2 Items A+B).
2. **The FR58 end-to-end proof** — a redelivered external call through the live bridge yields exactly one external effect (§2 Item C).
3. **The eager bgcheck-freshness auto-reopen** — the carried 14.4 work: (a) the `internal/refractor/ruleengine/full/executor.go` `applyMatch` null-restore fix (recommended) OR a list→scalar reducer (Q1); (b) the `freshUntil` scalar column added to the `leaseApplicationComplete` lens cypher; (c) a type-neutral rule-engine test for the engine fix; (d) the e2e leg exercising the short window → `@at` fire → eager re-open → exactly-one re-dispatch (§2 Item D, §0.A/§0.B/§0.C).
4. **The D5 gate-assertion** — the harness asserts the `.outcome` aspect + root-minimal, CI-enforced (§2 Item E).
5. **The `test-lease-convergence` Makefile gate** — a new target matching the established conventions, wired wherever the other `test-*` gates are invoked in CI (§2 Item F, §0.D).
6. **The no-drop regression extended** — `TestLeaseApplicationComplete_PaymentInstanceNoBgcheck_NoDrop` (or a sibling) updated to the eager-`freshUntil` lens shape, proving the new dedicated bgcheck match still never drops the anchor in the payment-before-bgcheck window (§2 Item D, §0.C).
7. **Doc updates** — `packages/lease-signing/README.md`'s "Deferred to 14.5" section flipped to "shipped in 14.5" with the final mechanism; `docs/components/weaver.md` (or `refractor.md`) note on the `applyMatch` fix + the eager-`freshUntil` projection if a component doc covers it. New docs → README/`/docs`, never `_bmad-output/`.

**Out of scope (do NOT build):**
- **NO retiring the nudge / `internal/weaver/nudge` deletion** — that is **13.5** (unblocked BY 14.5's green, but a separate story). 14.5 proves the `triggerLoom` path converges; it does not remove the nudge plane.
- **NO new vertex types / new ops / new patterns / new playbook** — the package is DONE (14.4). 14.5 adds **one lens column** (`freshUntil`) + **one engine fix** + **a harness + a gate**. If you find you need a new op/pattern/DDL, that is a smell — the convergence should run on 14.4's surface (Q6).
- **NO bridge / adapter change** — the live bridge + the `Fake*` adapters are DONE (13.4). The harness *drives* them; it does not modify them. (The `status="completed"` demo simplification + the `{externalRef, result}`-only reply are 14.4/13.4 settled facts — see the 14.4 README LOUD FLAG; 14.5 does not revisit them.)
- **NO Loom / Weaver / Processor engine change** — the only engine change is the **Refractor `applyMatch` fix** (§0.B). Weaver already consumes `freshUntil` (§0.A); Loom's externalTask is done (13.2); the Processor runs the existing DDLs. **A proposed Loom/Weaver/Processor change is a RED FLAG (Q6)** — surface it, do not implement.
- **NO §10.2 contract amendment for `freshUntil`** — it is already the engine-recognized convention column (§10.2/R3, ~976). (If the Contract #6 §6.13 scalar-passthrough needs a clarifying note for the `freshUntil` scalar, that is a small amendment — flag it, Q3 — but the mechanism already exists.)
- **NO Postgres read-model, NO `serviceAccess`/`cap.svc` read-path auth, NO Vault/KMS/crypto-shred** — all Phase-3-deferred (charter; 14.1/14.4 scope boundaries).
- **NO production-window-to-seconds change** — keep `bgcheckFreshnessWindow`'s production default a sane real value; the e2e uses a *test-injectable* short window (§0.F / Q4), not a permanently-shrunk constant.

---

## 2. The mechanism — item-by-item (DS builds to THIS)

### Item A — the harness boot (install + all engines + the live bridge)

Mirror `internal/refractor/refractor_leasesigning_scalar_e2e_test.go` for the install + Refractor wiring, `internal/weaver/weaver_e2e_test.go` for the Weaver lanes + `core-schedules` provisioning, `internal/loom/external_e2e_test.go` for the Loom externalTask wiring, and `internal/bridge/bridge_e2e_test.go` for the live-bridge boot. The harness:

1. **Embedded NATS** (`natstest.RunServer`, JetStream, `t.TempDir()`).
2. **Provision** every bucket + stream: `core-kv`, `core-events` (`AllowAtomicPublish` — the outbox), `core-operations` (ops.>), `weaver-targets`, `weaver-state`, `health-kv`, and **`core-schedules`** (`AllowMsgSchedules: true`, `MaxMsgsPerSubject: 1`, file storage, limits retention — the §10.4 temporal lane the eager `@at` rides). Mirror `weaver_e2e_test.go`'s `provision` exactly for the schedules stream.
3. **Install** rbac → identity → orchestration-base → service-domain → **lease-signing** via the real `InstallPackage` path (the meta-lane Processor + the installer). Pull the package definitions from their real packages (`leasesigning.Package` etc., as `cmd/lattice-pkg/main.go` does) — install the **shipped** declarations, not fixtures (this is the dogfood proof).
4. **Boot the engines** (each as a goroutine under a test-scoped context, `t.Cleanup(cancel)`): Processor, Refractor (activate the live lens watch + `projection.InstallActorAggregate` for `leaseApplicationComplete`), Loom (trigger/relay/deadline + orchestration consumers), Weaver (lane-1 + lane-3), and the live **bridge** (`bridge.NewEngine`, `RegisterAdapter("backgroundCheck", FakeBackgroundCheck)`, `RegisterAdapter("stripe", FakeStripe)`). Use the real config shapes; keep heartbeat/redelivery fast (mirror `bridge_e2e_test.go`'s fast-cadence config) so the test reads Health + redelivers quickly.
5. **Seed** the applicant identity (`vtx.identity.<id>`, alive) — NO PII aspects (so `missing_onboarding` is true), and `CreateLeaseApplication{applicant}` (so `missing_signature` is true and no bgcheck/payment instances exist → all four gaps open, `violating == true`).

> **The harness is the heaviest artifact in the story.** It is acceptable for it to be one large, well-commented test file (or a `internal/<area>/leaseconvergence` harness package the gate runs) — see §9. Lean on the five existing harnesses' helpers; do not reinvent NATS/provision/install boilerplate.

### Item B — the drain-then-assert convergence proof (AC #1)

After Item A drives the application:
- **Drain:** poll `KVGet(weaver-targets, "leaseApplicationComplete.<leaseAppId>")` until the row's `violating == false`, within a bounded deadline. On timeout, fail with the last-seen row JSON + a dump of each engine's Health KV issues (the loud-failure diagnostic). The bgcheck `validUntil` during converge is far-future-enough to count (the window only matters for Item D).
- **Assert steady:** after the flip, hold for a settle window (e.g. a few seconds / several CDC cycles) and assert `violating` stays `false` across repeated reads — no oscillation. Also assert each `missing_*` is false at steady state.
- This single test is the AC #1 capstone: it proves Weaver dispatched all four remediations, Loom ran the two externalTasks + the onboarding userTask, the live bridge completed the two external calls, the SignLease task closed the signature gap, and Refractor reprojected to a stable converged row.

### Item C — the FR58 end-to-end proof (AC #2, first clause)

Extend the bridge-only FR58 pattern (`internal/bridge/fr58_test.go`) end-to-end:
- Drive a **redelivery** of one `external.<adapter>` event through the live bridge (republish the same event, OR exercise the bridge's NakWithDelay redelivery leg) — and assert exactly **one** external effect lands: one `vtx.service.<handle>.outcome` aspect (the create-only collapse), one `orchestration.externalTaskCompleted` Loom completion, one Loom pattern completion. The mechanism is already correct (`deriveReplyRequestID(instanceKey)` → same replyOp requestId → collapses on the `vtx.op.<requestId>` tracker; the replyOp's `.outcome` is create-only). This test PROVES it through the live loop (vs. 14.4's direct-write tests + the bridge-only fr58_test).
- This can be a distinct assertion within the main harness or a sibling test reusing the boot. Keep the "exactly one external effect" witness explicit (count the `.outcome` writes / the completion events).

### Item D — the eager bgcheck-freshness auto-reopen (AC #2, second clause — the §0.A/§0.B/§0.C centerpiece)

The carried work, in three sub-parts:

**D.1 — the engine fix (`internal/refractor/ruleengine/full/executor.go` `applyMatch`, §0.B; approach a recommended).** When all real matches of an OPTIONAL MATCH are WHERE-filtered, construct the null fallback from the source binding `b` (null-bind every newly-introduced pattern variable) rather than searching `expanded` for a null row that may not exist. Factor the null-bind into a helper shared with `matchPatterns`'s existing `len(expansions)==0` branch. **Prove it with a type-neutral rule-engine test** (a throwaway cypher: a required anchor + a dedicated `OPTIONAL MATCH … WHERE` that filters the sole neighbor → assert the anchor row is preserved with nulls, not dropped). Mirror the `internal/refractor/ruleengine/full` test style. **No concrete lease type in this test (§0.H).** (If the dev picks approach b — a list→scalar reducer — the engine test proves the reducer instead; flag the deviation, Q1.)

**D.2 — the lens column (`packages/lease-signing/lenses.go`).** Add a single scalar `freshUntil` body column to `leaseApplicationCompleteSpec` + `Output.BodyColumns` — the bgcheck's `validUntil`, projected per anchor so Weaver's temporal lane reads it. With D.1's fix, a dedicated family-filtered bgcheck OPTIONAL MATCH (`OPTIONAL MATCH (id)<-[:providedTo]-(bg:service)` with a WHERE selecting the completed bgcheck) can read `bg.outcome.data.validUntil` as a scalar without dropping the anchor when no fresh bgcheck exists (it null-restores to a null `freshUntil`, which Weaver treats as "no timer to arm" — `scheduleFreshness` ~99–106 clears on a nil column). **Keep the existing one-fan-no-WHERE columns intact** (the `missing_*`/`violating`/`entityKey`/`applicant` scalars must still project one-row-per-anchor — do not regress the §0.C/`guardOutputKeyCollision` guarantee). The cleanest shape is likely the existing single providedTo fan for the `missing_*` counts PLUS a second dedicated bgcheck OPTIONAL MATCH (now safe) for the `freshUntil` scalar — validate the exact cypher against the `full` grammar + `guardOutputKeyCollision` (Q1). Update the lens doc comment (replace the "Deferred to 14.5" paragraph with the shipped mechanism — no history comment, describe what it does now).

**D.3 — the e2e eager-reopen leg (the harness, §0.F step 3).** With a **test-injectable short** `bgcheckFreshnessWindow` (Q4): after steady-state, let the bgcheck `validUntil` lapse; assert (a) Weaver's `@at` schedule fired (the row carried `freshUntil`, `scheduleTimer` published, `handleFiredTimer` ran `MarkExpired` — observe via the `weaver-state`/the row re-touch / the temporal counters); (b) the row re-opened `missing_bgcheck` **eagerly** (driven by the `@at`, within ~the short window, not by an incidental later CDC touch); (c) **exactly ONE** new bgcheck external call resulted (the §0.C FR58 assertion — count the new `.outcome` writes / new `external.backgroundCheck` events; assert it is one, not two); (d) the loop re-converges (`violating` flips false again). Also extend the **unit-level no-drop regression** (`TestLeaseApplicationComplete_PaymentInstanceNoBgcheck_NoDrop`, `lens_cypher_test.go:408`) to the new lens shape — assert the dedicated bgcheck match preserves the anchor (one row, `freshUntil` null) when the applicant has a payment instance but no bgcheck yet.

### Item E — the D5 gate-assertion (AC #3, §0.G)

Inside the harness (so it runs in the gate), after the bridge round-trip:
- `KVGet(core-kv, "vtx.service.<handle>.outcome")` → exists, `{status:"completed", completedAt, validUntil}`.
- `KVGet(core-kv, "vtx.service.<handle>")` → root `data == {}` (parse the envelope, assert the `data` field is empty).
- `KVGet(core-kv, "vtx.leaseapp.<id>")` → root `data == {}` (the signature is in `vtx.leaseapp.<id>.signature`, not root).
- Make these `require.*` assertions so a root-data regression fails the gate.

### Item F — the `test-lease-convergence` Makefile gate (AC #4, §0.D)

Add a `.PHONY: test-lease-convergence` target. Recommended posture (Q5): **self-contained embedded-NATS** (mirror `test-rollback`):
```make
## test-lease-convergence — Story 14.5 external-I/O idempotency + convergence gate.
## Self-contained: embedded NATS, no Docker stack. Drives a lease application to
## steady-state convergence through the live bridge (Loom externalTask + bridge +
## temporal freshness + tasks), proves the external effect is at-most-once (FR58),
## and asserts D5 (outcome in aspect, root data minimal).
.PHONY: test-lease-convergence
test-lease-convergence:
	go test ./<harness-pkg>/... -run <TestLeaseConvergence...> -v -p 1 -count=1 -timeout <N>m
```
- Add `test-lease-convergence` to the `.PHONY` line (~18) and wherever the other `test-*` gates are invoked in CI (check `.github/workflows/*` — grep for `test-bypass`/`test-capability-adversarial` to find the CI invocation site and add the new gate alongside, matching the established pattern). **Do NOT remove or weaken any existing gate.**
- If the harness must run under a Docker stack instead (Q5), mirror `test-hello-lattice`'s shape (`make up` + `-tags integration` + env vars) — but the embedded-NATS posture is recommended.

---

## 3. The completion-lie traps (what "looks done" but isn't) — the §6 tests target each

1. **The eager re-open is actually LAZY (the §0.A trap).** If the `freshUntil` column is added but the e2e never proves the `@at` *fired* (only that the gap eventually re-opened), a lazy re-open (an incidental CDC touch re-evaluating the predicate) passes a weak test while the eager mechanism is dead. **§6 test D asserts the `@at` schedule was published + fired** (the temporal lane ran), not merely that the gap re-opened — the only assertion that distinguishes eager from lazy.
2. **The anchor drops → FR58 double-act (the §0.C trap).** If D.1's fix is wrong (or skipped, relying on approach b done badly), the dedicated bgcheck match drops the anchor in the payment-before-bgcheck window → `clearClosedMarks` → a second bgcheck call. **§6 test D's "exactly one new external call" assertion + the extended no-drop unit test** are the only catches — a naive convergence test would still go green (it converges, just via two calls).
3. **The D5 assertion is review-only, not gate-only (the AC #3 trap).** If the D5 checks live in a 14.4-style package test rather than inside the `test-lease-convergence` harness, AC #3 ("enforced by gate, not review") is unmet. **§6 test E lives in the gate's harness** so a root-data regression fails CI.
4. **The "convergence" is direct-write, not bridge-driven (the §0.E trap).** If the harness shortcuts the bridge (direct `.outcome` writes, like 14.4), it is not the 14.5 proof. **The harness MUST boot the live `bridge.NewEngine` + the real `Fake*` adapters and let the `external.<adapter>` → adapter → `replyOp` loop run** — assert the bridge actually dispatched (the bridge's `dispatched` metric / the `replyOp` landing via the bridge actor, not a test write).
5. **The gate doesn't run in CI (the AC #4 trap).** A Makefile target that exists but is not wired into the CI workflow is not a gate. **Wire it into `.github/workflows/*` alongside the existing `test-*` gates** and note the wiring in the summary.

---

## 4. Forward fit (note, do NOT build)

- **13.5 (retire the nudge)** is unblocked by 14.5's green. Its AC confirms lease-signing authored a `triggerLoom` gap (never a `nudge` gap) — 14.5's e2e is the living proof the `triggerLoom` external path converges end-to-end, which is the evidence 13.5 leans on to delete the nudge plane safely. **14.5 does not touch the nudge plane.**
- **Phase 3 plug-ins** the e2e makes concrete (note in the README, do not build): a structured adapter result (the `status="completed"` simplification's plug-in point — 14.4 README LOUD FLAG), a real freshness window in production (the test-injectable short window stays test-only), the Postgres read-path, `serviceAccess` read auth.

---

## 5. Required reading (DS does the deep reads; do not expect them pre-loaded)

- **THE CARRIED-WORK SPEC (read first — it IS the eager-freshness brief):** `packages/lease-signing/README.md` "Freshness" (~79–104) + "Deferred to 14.5 — eager auto-reopen-at-expiry" (~106–126); `packages/lease-signing/lenses.go` doc comment (~104–119) + the cypher (~120–140); `packages/lease-signing/scripts.go` (`bgcheckFreshnessWindow` ~275–287, the replyOp `validUntil` stamp ~364–404).
- **THE ENGINE FIX (read line-by-line):** `internal/refractor/ruleengine/full/executor.go` `applyMatch` (~118–177), `isNonNullExpansion` (~179–208), `matchPatterns` (~210–250), `matchPath`, `equalsAny`, `compareAny`. The existing `internal/refractor/ruleengine/full` tests (the type-neutral test style D.1 mirrors).
- **THE TEMPORAL LANE (the consumer side — already done):** `internal/weaver/temporal.go` IN FULL (`freshUntilColumn`, `scheduleFreshness`, `handleFiredTimer`, `currentFreshUntil`); `internal/weaver/evaluator.go` (`handleRow`, **`clearClosedMarks` — the §0.C hazard**, `boolColumn`). Contract #10 §10.2 (`freshUntil` ~147–149 + R3 ~976) + §10.4 (ADR-51 + R2 ~976).
- **THE LIVE BRIDGE + FR58:** `internal/bridge/dispatch.go` (`handleExternal`, `externalEvent`, the `{externalRef, result}` reply ~164–167), `actuator.go` (`submit`), `token.go` (`deriveReplyRequestID`), `fake_background_check.go` + `fake_stripe.go`, `engine.go` (`NewEngine`/`Start`/`RegisterAdapter`), `fr58_test.go` (the bridge-only at-most-once proof 14.5 extends), `bridge_e2e_test.go` (the full-loop harness shape). Contract #4 (the `vtx.op.<requestId>` dedup).
- **THE HARNESS TEMPLATES:** `internal/refractor/refractor_leasesigning_scalar_e2e_test.go` (install + Refractor + scalar projection), `internal/weaver/weaver_e2e_test.go` (Weaver lanes + `core-schedules` provisioning), `internal/loom/external_e2e_test.go` (Loom externalTask loop), `internal/bridge/bridge_e2e_test.go` (live bridge). `cmd/lattice-pkg/main.go` (the install chain + the registry).
- **THE GATE CONVENTIONS:** `Makefile` (`test-rollback` ~189 — the self-contained template; `test-hello-lattice` ~172 — the integration template; `.PHONY` ~18); `.github/workflows/*` (the CI invocation site for the existing `test-*` gates — grep `test-bypass`).
- **THE GROUNDING (read; build TO; do NOT edit):** Contract #10 §10.2/§10.4/§10.5/§10.6/§10.8; Contract #6 §6.13 (scalar passthrough); Contract #1 §1.1; Contract #4; **D5** (`lattice-architecture.md` ~1167); the epics §14 (`phase-2-epics.md` ~662–750).
- **HOUSE RULES:** `CLAUDE.md` — NO history/changelog comments in code (when you flip the README's "Deferred to 14.5" → "shipped", and when you replace the lens doc comment, describe what it does NOW — no `// was deferred …`, `// 14.5 added …`); the verification-gate list (§7); docs → README/`/docs`, not `_bmad-output/`; frozen contracts are build-to.

---

## 6. Tests (the convergence proof + the FR58 + the eager-freshness + the D5 gate-assertion + the engine-fix unit) — first-class

The harness is the centerpiece — it proves the SHIPPED package + the new engine fix end-to-end. Lettered to match §2.

- **Test A+B — `TestLeaseConvergence_DrainThenAssert_SteadyState` (AC #1; §2 A+B).** The full boot (install chain + all engines + live bridge), one `CreateLeaseApplication` all-gaps-open, drain until `violating == false`, then hold and assert it stays false + every `missing_*` false at steady state. Loud-failure dump (last row + Health issues) on timeout. **This is the capstone.**
- **Test C — `TestLeaseConvergence_FR58_RetriedExternalCall_AtMostOnce` (AC #2 first clause; §2 C).** A redelivered `external.<adapter>` event through the live bridge → exactly one `.outcome` aspect + one completion + one pattern completion. The end-to-end FR58 proof (extends the bridge-only `fr58_test.go`).
- **Test D — `TestLeaseConvergence_BgcheckFreshness_EagerReopen_NoDoubleAct` (AC #2 second clause; §2 D; the §0.A/§0.B/§0.C centerpiece).** With the test-injectable short window: steady-state → window lapses → assert the `@at` fired (eager, not lazy — §3 trap #1) → `missing_bgcheck` re-opens → **exactly ONE** new bgcheck external call (§3 trap #2 / §0.C FR58) → re-converges. Plus the extended **unit** no-drop regression (`TestLeaseApplicationComplete_PaymentInstanceNoBgcheck_NoDrop` updated to the `freshUntil` lens shape — the anchor never drops in the payment-before-bgcheck window).
- **Test (engine) — `TestApplyMatch_OptionalWhereFiltersAllNeighbors_PreservesAnchor` (§2 D.1, §0.B/§0.H).** A type-neutral rule-engine test: a required anchor + a dedicated `OPTIONAL MATCH … WHERE` that filters the sole neighbor → the anchor row is preserved with nulls (not dropped). No concrete lease type. Lives in `internal/refractor/ruleengine/full`. **This is the guarded-engine-fix proof.**
- **Test E — `TestLeaseConvergence_D5_OutcomeInAspect_RootMinimal` (AC #3; §2 E, §0.G).** Inside the gate's harness: `.outcome` aspect carries the outcome; `vtx.service.<handle>` + `vtx.leaseapp.<id>` root `data` are `{}`. CI-enforced.
- **Test (gate wiring) — `make test-lease-convergence` runs green** + the target is wired into `.github/workflows/*` (AC #4; §2 F, §3 trap #5).
- **Regression — every existing test stays green.** `packages/lease-signing/...` (the lens still projects one-row-per-anchor with the new `freshUntil` column — `guardOutputKeyCollision` not tripped; `TestLeaseAppType_AbsentFromCore` still passes), `internal/refractor/...` (the `applyMatch` fix regresses no existing cypher — `myTasks`/`capabilityEphemeral`/the keyColumn e2e), `internal/weaver/...`, `internal/loom/...`, `internal/bridge/...`, `internal/pkgmgr/...`, `packages/{service-domain,identity-domain,orchestration-base}/...`. **A regression in the existing OPTIONAL-MATCH cyphers is the highest risk of the engine fix — run the full refractor + ruleengine/full suites.**

### Test posture

The harness uses embedded NATS in-process (no Docker), booting the real engines + the live bridge — so the convergence + the at-most-once + the eager-freshness + the D5 assertions are genuinely end-to-end. `t.Skip` under `-short` for the heavy e2e (mirror `refractor_leasesigning_scalar_e2e_test.go`'s `testing.Short()` skip). Flake-retry per Deviation 14 is allowed; a flake claim without a re-run is a drift signal. The engine-fix unit test + the no-drop unit test are fast (no NATS). **Run Gate 2 + Gate 3** (§7) — the engine fix is on the projection plane and the convergence loop crosses the capability plane.

---

## 7. Verification gates (run before handing back; record each + result in the closing summary)

- `go build ./...` — includes the harness + the engine fix + the lens change.
- `make vet`
- `golangci-lint run ./...`
- `make verify-kernel` — no kernel-topology change is made; run it to prove no regression (requires `make up`).
- **`make test-lease-convergence`** — **the story's new gate + centerpiece** (tests A+B, C, D, E green end-to-end through the live bridge).
- **`go test ./internal/refractor/... -count=1`** — the `applyMatch` fix regresses no cypher (the keyColumn e2e, the scalar lease e2e, `myTasks`, `capabilityEphemeral`) + the new type-neutral `applyMatch` unit test passes.
- **`go test ./internal/refractor/ruleengine/full/... -count=1`** — the engine-fix unit test + every existing rule-engine test (the highest-risk regression surface).
- **`go test ./packages/lease-signing/... -count=1`** — the lens still projects one-row-per-anchor with the new `freshUntil` column; the extended no-drop regression; `TestLeaseAppType_AbsentFromCore` still passes (invariant a).
- **`go test ./internal/weaver/... ./internal/loom/... ./internal/bridge/... ./internal/pkgmgr/... -count=1`** — the engines the harness drives are untouched (only Refractor changed) and still pass.
- **`go test ./packages/service-domain/... ./packages/identity-domain/... ./packages/orchestration-base/... -count=1`** — the dependency packages still pass (regression).
- **`make test-bypass` (Gate 2 — all BLOCKED)** — the engine fix touches the projection plane; confirm no bypass opens. Expect all BLOCKED.
- **`make test-capability-adversarial` (Gate 3 — all DEFENDED)** — the convergence loop crosses the capability plane; confirm no regression. Expect all DEFENDED.
- **`make verify-package-*`** is out-of-band (CI runs it) — run `make verify-package-identity` + `-hygiene` if the lens/DDL touch re-installs lease-signing's deps; the lens change is package content, so run them to confirm no cross-package regression.
- The full **3-layer adversarial review** is Winston's gate (Blind Hunter / Edge Case Hunter / Acceptance Auditor) per `bmad-code-review` — earned by the guarded-engine change + the cross-engine e2e. The **Acceptance Auditor** checks all four ACs + the eager-vs-lazy distinction (§3 trap #1) + the FR58-no-double-act (§0.C) + the D5-gate-not-review (AC #3) + the gate-wired-into-CI (AC #4); the **Edge Case Hunter** probes the `applyMatch` fix against EVERY existing optional-match cypher (regression), the payment-before-bgcheck anchor-drop window (§0.C), the `@at` fired-vs-stale-replay path (the `handleFiredTimer` read-before-act), the short-window boundary (a bgcheck lapsing mid-converge), and the redelivery-during-eager-reopen race; **Blind Hunter** on the diff. **Note it in your summary.**

**Why Gate 2 + Gate 3 run here:** the engine fix is on the projection plane and the convergence loop dispatches real ops across the capability plane — the gates confirm the new surface holds the bypass/capability boundary. (If you judge a gate genuinely does not exercise the change, say so explicitly so it can be overridden — but default to running both.)

---

## 8. House-rules checklist (bake into the work)

- **NO history/changelog comments in code.** When you flip `README.md`'s "Deferred to 14.5" → the shipped mechanism and replace the lens doc comment, describe what the code does NOW — no `// 14.5 …`, `// was deferred …`, `// previously lazy …`. git blame is the record.
- **NO sprints.** Session-per-story.
- **Frozen contracts (`docs/contracts/*`) are build-to.** The `freshUntil` convention (§10.2) + the scalar passthrough (§6.13) already exist — do not edit. A genuine gap → a `cmd/<area>/CONTRACT-AMENDMENT-REQUEST.md` entry + a top-of-summary flag (Andrew may amend in-place — still flag it; do not edit the contract yourself).
- **New docs → `packages/lease-signing/README.md`, `/docs`, or `docs/components/*`** — never `_bmad-output/`.
- **Type-agnostic engines (invariant a):** the `applyMatch` fix names no type; the `leaseapp`/`service` types stay in the package + the harness (a test). The 14.4 `TestLeaseAppType_AbsentFromCore` guard must still pass.
- **Sub-agents never commit/push/branch.** Leave the working tree for Winston.

---

## 9. If too large / a split

This story is **medium–large**: an engine fix + a lens column + a heavy cross-engine harness + a gate. It is **one coherent capstone**, and the halves are interdependent (the eager-freshness leg of the harness needs the engine fix + the lens column; the convergence harness is the substrate both ride). **Prefer the single pass.** The natural (but unnecessary) seam, if the harness proves slow to stabilize:
- **14.5a** = the **engine fix + the lens `freshUntil` column + the type-neutral `applyMatch` unit test + the extended no-drop unit test** (the eager-freshness MECHANISM, provable without the full e2e — fast). Land this first; it is the carried 14.4 debt and the guarded-engine change.
- **14.5b** = the **drain-then-assert harness + the FR58 end-to-end + the eager-reopen e2e leg + the D5 gate-assertion + the `test-lease-convergence` gate** (the END-TO-END proof on the live bridge).

**If split, land 14.5a first** (it makes freshness eager + fixes the latent bug), then 14.5b (the e2e capstone). **Do not split the engine fix from its type-neutral unit test** (the guarded-engine change must be proven where it lives). But the single pass is preferred — the e2e is the whole point, and it exercises the engine fix in situ.

---

## 10. Open Questions (assumptions made autonomously — Winston to confirm; Q1/Q4/Q5 are the load-bearing ones)

These are the decisions taken while drafting (the create-story ran autonomously). Each carries a **recommendation**; the dev proceeds on the recommendation unless Winston overrides. **Q1, Q4, and Q5 most warrant Winston's eye.**

- **Q1 — the engine fix is approach (a): the `applyMatch` OPTIONAL-MATCH null-restore fix (construct the null fallback from the source binding), NOT approach (b) a list→scalar reducer.** RECOMMENDED + assumed (§0.B). **Why:** (a) fixes a real latent correctness bug (a fully-WHERE-filtered optional drops the anchor for ANY cypher, not just the lease lens), is the smaller surface, and directly unlocks the dedicated family-filtered bgcheck match the `freshUntil` column needs. (b) is a larger, more speculative grammar extension (`max`/`head`/`coalesce`/`UNWIND` — all verified unsupported per the 14.4 README) and leaves the latent bug unfixed. **The dev makes the final call with rationale** — but the brief recommends (a), and §2 D.2's cypher design assumes (a). **Confirm Winston agrees, and confirm the exact `freshUntil` cypher shape** (the single-fan-for-counts + dedicated-bgcheck-match-for-freshUntil shape) against the `full` grammar + `guardOutputKeyCollision`.
- **Q2 — NO `internal/weaver` change is needed (Weaver already consumes `freshUntil` end-to-end).** RECOMMENDED + assumed (§0.A). `temporal.go` already reads `freshUntil`, schedules the `@at`, and runs `handleFiredTimer` → `MarkExpired`. 14.5 only makes Refractor PROJECT the column. **Confirm** by reading `temporal.go` + `handleRow`'s `scheduleFreshness` call — if a Weaver gap surfaces (e.g. `MarkExpired` does not actually re-touch the row in a way that re-evaluates the predicate), that is a RED FLAG (Q6). **Default: zero Weaver change.**
- **Q3 — NO further Contract #6 §6.13 / Contract #10 §10.2 amendment is needed for the `freshUntil` scalar.** RECOMMENDED + assumed. `freshUntil` is already the §10.2 engine-recognized convention column, and the CAR-E6 scalar passthrough (Contract #6 §6.13, landed in 14.4) already projects scalar body columns verbatim — `freshUntil` is just another scalar. **Confirm** the passthrough handles a *nullable* scalar (the `freshUntil` is null when no fresh bgcheck exists) without coercing the whole row — if it needs a clarifying note, that is a small amendment to flag, not block on. **Default: no amendment.**
- **Q4 — the e2e uses a TEST-INJECTABLE short `bgcheckFreshnessWindow` (seconds), keeping the production default a sane real value.** RECOMMENDED + assumed (§0.F step 3). The shipped constant is `"5m"` (`scripts.go:287`) — short enough that 14.4's author intended 14.5 to watch a lapse, but `5m` is too slow for CI. **Confirm the injection mechanism:** (a) a package var the test overrides (simplest, but the constant is currently `const` — would become a `var`); (b) a build-tagged test override; (c) a second short-window pattern installed only in the harness. **Default: make `bgcheckFreshnessWindow` a `var` with the `5m` default, overridable in the e2e to a few seconds** (smallest change; the production default stays `5m`). **Winston should weigh in** on whether shrinking it to seconds in-test is acceptable vs. a more isolated mechanism.
- **Q5 — `test-lease-convergence` is a self-contained embedded-NATS gate (mirror `test-rollback`), NOT a Docker-stack gate.** RECOMMENDED + assumed (§0.D). Every harness this story mirrors uses embedded NATS in-process; it is faster, hermetic, and avoids the `test-hello-lattice` Docker-stack flakiness. **Confirm** the posture + that the gate is wired into `.github/workflows/*` alongside the existing `test-*` gates. **Default: embedded-NATS, mirror `test-rollback`'s Makefile shape + wire into CI.** (If the e2e must exercise the *deployed* bridge process rather than an in-process `bridge.NewEngine`, mirror `test-hello-lattice` instead — but the in-process bridge engine is the established choice.)
- **Q6 — ZERO non-Refractor engine change (no Loom/Weaver/Processor/bridge change).** RECOMMENDED + assumed (§1 scope). The only engine change is the Refractor `applyMatch` fix. Weaver/Loom/bridge are DONE; the package is DONE (one lens column added). **A proposed Loom/Weaver/Processor/bridge change is a RED FLAG — surface it as blocking, do not implement.** **Default + expected: Refractor fix + lens column + harness + gate only.**
- **Q7 — the harness lives as a test in an appropriate `internal/<area>` package (or a small dedicated harness package the gate targets), using the real `leaseapp`/`service` types.** RECOMMENDED + assumed (§0.E/§0.H, §9). The harness is the union of five existing harnesses; place it where the imports resolve cleanly (it imports Processor + Refractor + Loom + Weaver + bridge + the packages — likely a new `internal/leaseconvergence` test package or a top-level `e2e` package to avoid an import cycle). **Confirm the placement** — a package that can import all five engines + the package definitions without a cycle. **Default: a dedicated harness test package (e.g. `internal/leaseconvergence` or `test/e2e`) the gate runs by path.**
- **Q8 — the FR58 redelivery is driven by republishing the same `external.<adapter>` event (or the bridge's NakWithDelay leg), NOT by a process restart.** RECOMMENDED + assumed (§2 C). Republishing the event re-drives the bridge on the same `instanceKey` → same `deriveReplyRequestID` → collapses on the tracker; this is the hermetic way to prove at-most-once without a restart dance. **Confirm** republish (vs. a more elaborate crash-recovery sim) is the accepted FR58 e2e proof. **Default: republish the external event + assert exactly one effect.**

---

## Dev Agent Record

### Agent Model Used

Amelia (dev sub-agent), claude-opus-4-8.

### Debug Log References

The all-engines harness surfaced three dispatch-plane integration gaps (see RED FLAGS
in Completion Notes), diagnosed via in-harness probes: `NoDDLForClass` (class-less
dispatch), `UnknownApplicant` (instanceOp carried no ContextHint.Reads), and a stalled
orchestration loop (the transactional-outbox publisher was not booted). The convergence
loop runs once all three are compensated at the harness boundary (the outbox publisher is
real production wiring; the class + reads normalizer compensates for the engine gaps).

### Completion Notes List

- **Engine fix = approach (a), red-green proven.** Fixed `internal/refractor/ruleengine/full/executor.go`
  `applyMatch`: when a WHERE filters every real neighbor of an OPTIONAL MATCH, the anchor
  is null-restored from the source binding via a shared `nullBindNewVars` helper (also used
  by `matchPatterns`'s `len(expansions)==0` branch) — NOT a list→scalar reducer. The
  type-neutral test `TestApplyMatch_OptionalWhereFiltersAllNeighbors_PreservesAnchor`
  (`optional_where_restore_test.go`, generic anchor/widget types) was RED before the fix
  (0 rows — anchor dropped) and GREEN after (1 row, optional column null). Full
  `./internal/refractor/...` + `./internal/refractor/ruleengine/full/...` suites green. The
  fix changed one pinned-behavior test: `TestMyTasksCypher_CompleteTask_NullsActorKey` →
  renamed `…_PreservesActorKey` (myTasks closed-task now projects the live anchor key, not
  null; vanish-on-close is preserved through a cleaner path — `…_CompleteTask_FiltersToNull`
  + the driver `…_FallsBackToParams` tests still pass).
- **Final `freshUntil` cypher (≤1 row/anchor, passes `guardOutputKeyCollision`):** kept the
  single no-WHERE `providedTo` fan for the `missing_*` counts UNCHANGED; carried `id` through
  the aggregation WITH as `applicantNode`; ADDED a dedicated `OPTIONAL MATCH (applicantNode)<-[:providedTo]-(bg:service) WHERE bg.family.data.value = 'backgroundCheck' AND bg.outcome.data.status = 'completed' AND bg.outcome.data.validUntil > $now` projecting `bg.outcome.data.validUntil AS freshUntil`. At most one completed-fresh bgcheck exists per application (FR58), so the dedicated match yields ≤1 row; null-restores to a null `freshUntil` when none. Verified one-row-per-anchor through the live Refractor (the scalar e2e + the convergence harness both project it cleanly).
- **Short-window = compile-time build-tag override (A2).** Removed the `const bgcheckFreshnessWindow`
  from `scripts.go`; split into `freshness_window.go` (`//go:build !leaseshortwindow` → `"5m"`,
  production default) + `freshness_window_short.go` (`//go:build leaseshortwindow` → `"30s"`).
  The gate compiles with `-tags leaseshortwindow`. Tuned: converge ≈6s, settle hold 5s, so a
  30s window does not lapse mid-converge yet the `@at` fires (~30s) within the test's bounded
  wait. Three consecutive gate runs green (~44s each), not flaky.
- **Gate = self-contained embedded-NATS (A3).** `make test-lease-convergence` mirrors
  `test-rollback` (no Docker, in-process `bridge.NewEngine`); added to `.PHONY`; wired into
  `.github/workflows/ci.yml` as a named step before "Run all tests".
- **Harness package:** `internal/leaseconvergence` (compiles importing Processor + Refractor +
  Loom + Weaver + bridge + the package definitions; no import cycle).
- **FR58 via republish (A7), single pass (A8).** Tests A+B/C/E green through the live bridge:
  drain-then-assert steady state, redelivered external event → exactly one effect, D5
  gate-asserted (outcome in aspect, root data {}). Test D (eager freshness) runs under
  `-tags leaseshortwindow` and asserts the eager chain up to the MarkExpired dispatch.
- **RED FLAGS (surfaced, not papered over — see top of dev summary): three dispatch-plane
  integration gaps the first all-engines e2e exposed (all hidden by per-engine fakeProcessors):**
  (1) Weaver/Loom dispatch ops with NO `class` field → the Processor's class-only DDL lookup
  rejects them (`NoDDLForClass`); (2) Loom's externalTask instanceOp carries no
  `ContextHint.Reads`, so `CreateLeaseServiceInstance`'s `vertex_alive(subject)` fails
  (`UnknownApplicant`); (3) `MarkExpired` has NO DDL anywhere (`docs/components/weaver.md`
  designates it "package data, Epic 14" but 14.4 did not create it; §1/Q6 forbid 14.5 adding
  a new op). The harness compensates for (1)+(2) with a documented dispatch-envelope
  normalizer + boots the real transactional-outbox publisher; (3) blocks only the final
  MarkExpired→reproject→re-open step of the eager leg, so test D asserts up to the MarkExpired
  dispatch. **No contract gap for the null `freshUntil` scalar** (A5 verified: it projects as a
  clean null and Weaver's `scheduleFreshness` clears the timer).
- **Gate results:** `go build`, `make vet`, `golangci-lint run ./...` (0 issues),
  `./internal/refractor/...`, `./internal/refractor/ruleengine/full/...`,
  `./packages/lease-signing/...`, `./internal/{weaver,loom,bridge,pkgmgr}/...`,
  `./packages/{service-domain,identity-domain,orchestration-base}/...`,
  `make test-lease-convergence` (×3), `make verify-kernel`, `make test-bypass` (Gate 2: 4/4
  BLOCKED), `make test-capability-adversarial`/TestCapAdv (Gate 3: 6/6 cleared),
  `make verify-package-{identity,rbac,identity-hygiene}`, and `go test ./... -p 1` — all green.

### File List

- `internal/refractor/ruleengine/full/executor.go` (modified — `applyMatch` null-restore + `nullBindNewVars` helper; `matchPatterns` refactored onto it)
- `internal/refractor/ruleengine/full/optional_where_restore_test.go` (new — type-neutral applyMatch unit tests)
- `internal/refractor/ruleengine/full/mytasks_cypher_test.go` (modified — pinned-behavior test updated to the corrected null-restore semantics)
- `packages/lease-signing/lenses.go` (modified — `freshUntil` scalar column + dedicated bgcheck match; BodyColumns; doc comment flipped to shipped)
- `packages/lease-signing/scripts.go` (modified — removed the inline `bgcheckFreshnessWindow` const)
- `packages/lease-signing/freshness_window.go` (new — production window, `//go:build !leaseshortwindow`)
- `packages/lease-signing/freshness_window_short.go` (new — short window, `//go:build leaseshortwindow`)
- `packages/lease-signing/lens_cypher_test.go` (modified — `freshUntil` assertions + extended no-drop regression)
- `internal/leaseconvergence/harness_test.go` (new — the all-engines harness boot + helpers)
- `internal/leaseconvergence/convergence_test.go` (new — tests A+B, C, D, E)
- `internal/leaseconvergence/window_default_test.go` (new — `shortFreshnessWindow=false`, `//go:build !leaseshortwindow`)
- `internal/leaseconvergence/window_short_test.go` (new — `shortFreshnessWindow=true`, `//go:build leaseshortwindow`)
- `Makefile` (modified — `test-lease-convergence` target + `.PHONY`)
- `.github/workflows/ci.yml` (modified — lease-convergence gate wired into CI)
- `packages/lease-signing/README.md` (modified — Deferred→shipped; bridge-driven e2e note)
- `docs/components/refractor.md` (modified — OPTIONAL MATCH null-restore semantics note)

---

## Questions for the lead (Winston) — collected from the autonomous run

The story was drafted autonomously (no mid-run checkpoints). The load-bearing open decisions, consolidated for one pass:

1. **(Q1, load-bearing) Engine-fix approach.** Confirm **approach (a)** — the `applyMatch` OPTIONAL-MATCH null-restore fix (construct the null fallback from the source binding instead of searching `expanded`) — over **(b)** a list→scalar reducer. (a) fixes a real latent bug + is the smaller surface; (b) is a larger grammar extension that leaves the bug unfixed. Also confirm the resulting `freshUntil` cypher shape (single providedTo fan for the `missing_*` counts + a dedicated family-filtered bgcheck OPTIONAL MATCH for the scalar `freshUntil`) against the `full` grammar + `guardOutputKeyCollision`.
2. **(Q4, load-bearing) Short-window injection.** Confirm making `bgcheckFreshnessWindow` (currently `const "5m"`, `scripts.go:287`) a **`var` overridable in the e2e to a few seconds**, production default unchanged at `5m` — vs. a more isolated mechanism (build-tag override / a second short-window harness pattern). The e2e needs to watch a bgcheck lapse in bounded wall-clock.
3. **(Q5, load-bearing) Gate posture.** Confirm `test-lease-convergence` is a **self-contained embedded-NATS gate** (mirror `test-rollback`: no Docker, in-process `bridge.NewEngine`) — vs. a Docker-stack integration gate (mirror `test-hello-lattice`, exercising the deployed bridge process). Embedded-NATS matches every harness this story mirrors and is hermetic.
4. **(Q2/Q6) Engine-change scope.** Confirm the expectation of **zero non-Refractor engine change** — Weaver already consumes `freshUntil` end-to-end (§0.A), Loom's externalTask + the bridge are done, the package needs only one lens column. A proposed Loom/Weaver/Processor/bridge change is a RED FLAG to surface, not implement.
5. **(Q3) Contract amendments.** Confirm **no further Contract #6 §6.13 / Contract #10 §10.2 amendment** is needed for the `freshUntil` scalar (both surfaces already exist) — flagging only if the scalar-passthrough needs a clarifying note for a *nullable* scalar column.
6. **(Q7) Harness placement.** Confirm a **dedicated harness test package** (e.g. `internal/leaseconvergence` or `test/e2e`) that can import Processor + Refractor + Loom + Weaver + bridge + the package definitions without an import cycle, using the real `leaseapp`/`service` types (the real vertical's e2e — invariant a preserved by the type-neutral engine test, not the harness).
7. **(Q8) FR58 e2e mechanism.** Confirm the at-most-once proof is driven by **republishing the same `external.<adapter>` event** (same `instanceKey` → same `deriveReplyRequestID` → tracker collapse) — vs. a process-restart crash-recovery simulation.
8. **(cross-cutting) Single pass vs. split.** The story recommends a **single pass**; the only natural seam is 14.5a (engine fix + lens column + unit tests) → 14.5b (the e2e harness + gate). Confirm single-pass, or sanction the split with 14.5a first.

---

## Lead adjudication (Winston) — answers to the open questions (BUILD TO THESE)

All eight resolved. Where I add a constraint beyond the recommendation, it is **binding**.

- **A1 (Q1) — Approach (a), confirmed: fix `applyMatch` null-restore.** Construct the null fallback from the source binding (factor a shared null-bind helper with `matchPatterns`'s `len(expansions)==0` branch); do NOT add a list→scalar reducer. **Binding discipline:** (i) write the type-neutral failing rule-engine test FIRST and watch it go red, THEN fix — prove the drop reproduces before you fix it; (ii) the `applyMatch` change is the single highest regression risk in this story — run the FULL `./internal/refractor/...` AND `./internal/refractor/ruleengine/full/...` suites and eyeball every existing OPTIONAL-MATCH cypher (`myTasks`, `capabilityEphemeral`, the keyColumn e2e, the scalar-lease e2e) for behavior change; (iii) **cypher shape** = keep the existing single `providedTo` fan for the `missing_*` counts UNCHANGED, ADD one dedicated family-filtered bgcheck `OPTIONAL MATCH … WHERE` projecting `bg.outcome.data.validUntil AS freshUntil`. That dedicated match must yield **≤1 row per anchor** or `guardOutputKeyCollision` fails closed — the vertical dispatches at most one bgcheck per application (FR58), so ≤1 completed bgcheck instance exists; **assert/comment that at-most-one assumption** and validate the full cypher against `guardOutputKeyCollision` before wiring the e2e.
- **A2 (Q4) — Compile-time build-tag override, NOT a runtime `var`.** I checked the code: `bgcheckFreshnessWindow` (`scripts.go:287`, `const "5m"`) is interpolated into `leaseServiceReplyDDLScript` by a package-level `var … = fmt.Sprintf(...)` at **package-init time**, and `Package`/`DDLs()` capture that script at init too — so a test that mutates a runtime `var` after init would never reach the already-built script (and would race under `-race`). Instead: move the const into a build-tag split — `//go:build !leaseshortwindow` → `"5m"` (the production default, unchanged) and `//go:build leaseshortwindow` → a short value; the `test-lease-convergence` gate compiles the e2e with `-tags leaseshortwindow`. The harness then installs the **exact shipped `Package` var**, merely compiled with the short window (most faithful; no permanent test-only exported API; no mutable global). **Window-value trap (binding):** ONE compile-time window governs BOTH the converge phase and the lapse phase of the e2e binary, so pick it comfortably larger than (time-to-converge + the steady-state settle hold) — otherwise the bgcheck lapses mid-converge and the steady-state "remain false" assert flakes — yet small enough to watch a lapse in bounded wall-clock (low tens of seconds; tune empirically). Sanctioned fallback ONLY if threading the tag proves genuinely awkward: a parameterized `PackageWith(window)` builder with `Package = PackageWith("5m")` — flag it if you take it.
- **A3 (Q5) — Self-contained embedded-NATS gate, confirmed** (mirror `test-rollback`; in-process `bridge.NewEngine`, no Docker). Wire it into `.github/workflows/*` alongside the existing `test-*` gates (grep `test-bypass` for the site) AND add it to the `Makefile` `.PHONY`. A target that is not invoked by CI does not satisfy AC #4 (§3 trap #5).
- **A4 (Q2/Q6) — Zero non-Refractor engine change, confirmed.** The only engine change is the Refractor `applyMatch` fix. Weaver already consumes `freshUntil`; Loom/bridge are done; the package gains one lens column. A proposed Loom/Weaver/Processor/bridge change is a RED FLAG — surface it as blocking, do not implement. **Caveat:** if you find Weaver's `MarkExpired`/`handleFiredTimer` does not actually re-touch the row such that the freshness predicate re-evaluates (the Q2 risk), STOP and surface it — do not paper over it with a Weaver edit.
- **A5 (Q3) — No contract amendment expected, confirmed.** `freshUntil` is the §10.2 engine-recognized column; CAR-E6 (§6.13) already projects scalar body columns verbatim. **Verify the nullable case:** when no fresh bgcheck exists, `freshUntil` projects as null and Weaver's `scheduleFreshness` clears the timer (no arm) — confirm the scalar passthrough emits a clean null (not `[]`, not a coerced row). If and only if that null case needs a clarifying sentence in §6.13, FLAG it in your closing summary — I (Winston) may amend the contract in-place (uncommitted) per Andrew; you do NOT edit `docs/contracts/*` yourself.
- **A6 (Q7) — Dedicated harness package, confirmed.** Place it where it imports Processor + Refractor + Loom + Weaver + bridge + the package definitions without an import cycle (`internal/leaseconvergence` or `test/e2e` — your call by what compiles cleanly). Real `leaseapp`/`service` types are fine there (it is the real vertical's e2e). The type-neutral `applyMatch` unit test lives in `internal/refractor/ruleengine/full` (no concrete type) and is what preserves invariant a; the 14.4 `TestLeaseAppType_AbsentFromCore` guard must still pass.
- **A7 (Q8) — FR58 via republish, confirmed.** Re-drive the same `external.<adapter>` event (same `instanceKey` → same `deriveReplyRequestID` → Contract #4 tracker collapse / create-only `.outcome` conflict) and assert exactly one external effect. Not a process-restart sim.
- **A8 (single pass vs split) — Single pass, confirmed.** Land it as one capstone. Split to 14.5a (mechanism: engine fix + lens column + the two unit tests) → 14.5b (harness + gate) ONLY if the harness will not stabilize — and say so explicitly before splitting.

---

## Post-review adjudication (Winston) — 2026-06-18

Full 3-layer review complete (Blind Hunter / Edge Case Hunter / Acceptance Auditor — all consistent). The DS implementation is **technically excellent and honest** (it surfaced the gaps rather than papering over them). Verdict:

**Authored work — reviewed-CLEAN, accepted:**
- The `applyMatch` OPTIONAL-MATCH-WHERE null-restore fix (`nullBindNewVars`) is correct (Blind Hunter enumerated every shape); my-tasks vanish-on-close AND capabilityEphemeral expired-grant revocation are preserved (Edge Case Hunter traced both to `ErrDeleteProjection`). rbac/identity optionals carry no WHERE → untouched. **Committed standalone as a decoupled bugfix: e560188** (CI-validated in isolation).
- The `freshUntil` lens cypher (≤1 row/anchor, null handled), FR58 (Met, genuine), D5-by-gate (Met), gate+CI (Met), type-agnostic + house rules clean.

**Two ACs BLOCKED on a foundational dispatch gap (the three red flags — all real, product-level):** the orchestration dispatch plane (Loom/Weaver) has only ever been tested vs `fakeProcessor` (keyed by operationType); the real Processor resolves by `class` + needs `ContextHint.Reads`, neither of which the engines set on dispatch — so AC#1 converges only via the harness's documented `normalizeDispatch` shim, and AC#2b's eager re-open is proven only "up to MarkExpired dispatch" (`MarkExpired` has no DDL). These are **out of 14.5's authored scope** and are carved into **Story 14.6 (orchestration dispatch integration)**: RF#1 (Processor operationType→class reverse index — the Contract #2 §2.1 index that was always anticipated, never built; Contract #2 to be amended in-place, uncommitted), RF#2 (engine-dispatched ops supply `ContextHint.Reads` — affects Weaver `CreateTask` + the Loom instanceOp), RF#3 (a `MarkExpired` DDL — polymorphic-entity design). See memory `project_dispatch_plane_gap`.

**Landing plan:** the vertical (freshUntil lens + harness + gate) stays UNCOMMITTED — shipping `freshUntil` before a `MarkExpired` DDL would emit failing @ats in prod, and a shim-green gate shouldn't enter CI. 14.6 builds the dispatch fix (design → team review → launch), then this vertical lands faithfully (shim dropped) with H1 (window margin) + M3 (run-once) fixed, and 14.5 → done. **13.5 (retire the nudge) also waits on 14.6** (its proof leans on the faithful triggerLoom convergence).
