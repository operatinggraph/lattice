# Weaver planner mandate — dispatcher → solver — design

**Status:** ✅ **Andrew-ratified 2026-07-04 — both forks accepted.** Surface frozen in Contract #10
§10.3/§10.8 (merged to `main`); the engine is **build-pending** across the 9 fires in §8. The Lattice
Steward builds from Fire 1.
**Component:** Weaver (`internal/weaver`) · **Stream:** Lattice (Stream 2) · **Size:** XL (9 build fires)
**Designer fire:** Winston, 2026-07-04 (commissioned directly by Andrew — exploration session, not a
Surveyor-filed row) · **Builds on:** Contract #10 §10.2/§10.3/§10.8, the §10.3 mark/lease/budget machinery
(`internal/weaver/state.go`, `reconciler.go`), the Strategist seam (`internal/weaver/strategist.go`), the
Augur (escalation + dispatch, `augur-design.md` / `augur-dispatch-pickup-design.md`), Loom definition
pinning (§10.5, `docs/components/loom.md`).
**Contract change:** **RATIFIED — Contract #10 §10.3 + §10.8** (additive, opt-in), merged to `main`.
Affected consumers: Weaver engine (Strategist/Evaluator), `pkgmgr` install validation, package authors
(`lease-signing` reference), the Augur package.

---

## Ratification (Andrew, 2026-07-04)

Ratified with both recommendations accepted. The two forks resolved as recommended, and the contract
surface merged to `main`:

- **Fork 1 — selection-altitude re-expansion: ACCEPTED.** Weaver re-expands its *selection* altitude
  (choosing *what* to dispatch); the 2026-06-18 13.1 "collapse to detect → `triggerLoom`" is read as an
  *I/O-placement* decision, not a *selection-intelligence* one. The 13.1 execution placement is untouched
  — external I/O stays Loom + bridge, and Weaver still never reaches an external system or holds an adapter.
- **Fork 2 — build strategy: ACCEPTED in-place.** Evolve the existing engine (the best-conformed one,
  arch-review 2026-07-02) via **shadow mode** (Fire 4) + **per-target `mode` cutover**, reversible through
  the existing control plane. No parallel Weaver 2.0; no double-dispatch fencing on the shared buckets.
- **`effects` placement: kept in §10.8** (Weaver is the consumer) — the offered relocation to a DDL
  self-description contract was declined.
- **Contract:** §10.3 reserved-key-shapes block (incl. the as-built `__control`/`__count` drift fixes) +
  the §10.8 Planner-extension subsection are **frozen on `main`**. The other four
  `contract-10-weaver-text-reconciliation` drift spots remain on that backlog row.

**Amendment (2026-07-05, Andrew-ratified in the renewal ratify session):** the first consumer
(LoftSpace lease renewal — `loftspace-lease-renewal-goal-authored-target-design.md`) revised three of
this design's ratified execution sentences: **per-leg dispatch** through the frozen action vocabulary
(pin-until-leg-complete; pin-release = the leg's `__effect` close-credit + per-leg dispatch-count
reset), **per-leg mark pinning** (leg boundaries mint fresh marks — §10.3 clauses revised in lockstep),
and the **per-gap `actions` catalog** as the synthesis domain (an op effect alone carries no dispatch
binding; the global auto-catalog is reserved). The compile-to-`plan-<hash>`-pattern shape is **RESERVED
for op-only single-actor plans** — unbuilt, no consumer. The §2/§3.2/§3.3/§8 passages describing it are
struck in place below. The renewal build itself (R1 dispatch wiring → R2 package → R3 FE) is
**lane-consolidated into this lane** (Andrew, anti-ping-pong; the verticals row was removed).

**Next:** Fires 1–8 shipped. **Fire 9 (the Augur floor) is 🏗️ building** — its brief is the last build note
in this doc (2026-09-13). Of Fire 9's three deliverables, (a) `unplannable`(extended)/`exhausted` → Augur
shipped inside the two escalation-episode fires (`weaver-escalation-episode-three-doors-design.md`,
`weaver-exhausted-gap-leg-boundary-design.md`; `augur.model` is threaded end to end — `strategist.go`
`augurEscalation` → `.gap.model` → `external.augur` params → the adapter, `FakeAugur` honouring it); the fire
builds (b) plan-shaped proposals dispatched per leg and (c) the playbook-promotion proposal.

**🏗️ Fire 6 CHECKPOINT (2026-07-04, Increment 1 of 2):** Increment 1 shipped — the runtime op-effects
catalog Fire 4/5's own comments flagged as missing (Fire 1 validated `Effects` at install time but never
persisted them; nothing existed for a planner to read). `pkgmgr.buildInstallBatch` now materializes each
op-meta vertex's declared Effects onto a sibling `.effects` aspect; `validateEffects` now also requires a
matching `OpMetaSpec` (fail-closed — an effect with nowhere to materialize would silently never reach any
catalog); the Weaver registry indexes it (order-independent join against the op-meta envelope) and exposes
`effectsCatalog() []planner.Action`. Zero dispatch-decision change; full detail in
`docs/components/weaver.md` "Op-effects runtime catalog" section.

**✅ Increment 2 (2026-07-04) shipped — State-schema gap resolved.** `rowState` (Fire 4/5) mapped every
lens row column onto a **root** guard-grammar path (`subject.data.<column>`), but a declared op **Effect**
(e.g. `SignLease`'s `subject.signature.data.signedAt`) asserts an **aspect** path — disjoint keys in
`planner.State`, so a goal authored against the same real-world fact an op's Effects assert would read as
perpetually absent, driving the search to synthesize a **spurious** plan even when the goal was already
met. The design doc's own "fresh Core-KV read" leading candidate was **rejected**: it contradicts §10.8's
own frozen "pure function of (row, catalog, `__effect` window)" framing and would be a new Weaver-side
Core-KV read of exactly the kind Andrew has held Processor-exclusive (Loom's guard-precondition read is the
one tolerated, provisional exception — effect-probing is explicitly not that exception,
[[feedback_no_new_engine_corekv_reads]]). **Resolution: a lens-column → aspect-path bridge, zero new
reads** — a real lens already projects an aspect field onto a plain row column today
(`packages/lease-signing/lenses.go:551`), so a gap's (additive, optional) **per-gap** `goalColumns` field
(`{"signedAt": "subject.signature.data.signedAt"}`) tells `rowState` which columns to key at their real
aspect path instead of the default root mapping; install-validated (`parseGoalColumns`: aspect-qualified,
unique values, and every path must be referenced by that gap's own `goal` — an unreferenced entry is as
inert as a typo and now rejected rather than silently degrading) and cached as `goalColumnPaths`,
mirroring `goalGuard`'s parse-once pattern. **Scoped per-gap, not target-wide**, closing a real 3-layer
review finding: a shared target-level map would have let two gaps reusing the same column name for
unrelated facts silently rebase onto whichever gap declared it. The mirror-image mistake — an
aspect-shaped `candidates[].pre`, which `rankCandidates` can never bridge — is now rejected too. Proven
end to end (`goal_state_internal_test.go`) against the real lease-signing shape: without the bridge, an
already-signed application's row synthesizes a spurious one-step `SignLease` plan; with it, the same row
correctly resolves the goal as already met. Full detail: `docs/components/weaver.md` "Goal-regression
State-schema bridge" section. **Contract #10 §10.8 needs a small additive amendment** documenting
`goalColumns` (staged uncommitted, flagged for Andrew — no fork, no dispatch-decision change, purely the
row-keying convention this increment adds).

**Increment 3 grounding pass (2026-07-04): HELD — no real consumer exists, dead-scaffolding test fails.**
Surveyed every gap in every installed package (`lease-signing`, `clinic-reminders`, `objects-base`):
every one closes via a **single** dispatch — `assignTask`, `directOp`, or a delegated `triggerLoom`
pattern. `missing_signature` (the design's own leading candidate) is confirmed a genuine single-step
human task (`assignTask SignLease`); forcing a `goal` onto it would synthesize a one-step plan
duplicating what `candidates`/the static table already express — exactly the "just to exercise the
machinery" case this doc already warned against, and the same dead-scaffolding doctrine that shelved
Fire 6's speculative-validation lane (§7.3). Goal regression's entire value-add over Fire 5's `candidates`
is **chaining ≥2 ops**; no installed package has a gap that needs it yet. **Holding, not building dark:**
plan-vertex compilation, runtime `meta.loomPattern` authoring, plan GC, and dispatch-time re-validation
stay unbuilt until a real multi-op gap surfaces (Surveyor/vertical-PO signal, not manufactured). Re-check
this gate whenever a new package/gap is filed with ≥2 ops that could close one gap.
*(2026-07-05: the re-check trigger is superseded — see the verification pass below: under the §10.8
decomposition doctrine a ≥2-op gap cannot arise on its own; the unblock is the goal-authored renewal
demand, filed.)*

**🔎 Verification pass (2026-07-05, Andrew-commissioned, Winston):** Fires 1–5 + 6 Inc1–2 verified
against the design's load-bearing invariants — planner determinism (canonical ordering, no wall-clock,
catalog-permutation stability), mark-pinned episode stability including the single-mark-read threading
through `resolvePlannedAction` (the pre-build gate's fix, re-confirmed in code at `evaluator.go:214` /
`reconciler.go:484`), the no-new-Core-KV-reads doctrine (Inc2's bridge over the rejected fresh-read),
shadow inert-by-construction (`shadowCompare` has no path to `fireEpisode`); `./internal/weaver/...`
green. **Verdict: matches the design's letter and spirit; Inc1/Inc2 corrected real gaps in the design's
own text in the right direction.** Three findings:

1. **`goalColumns` contract amendment — pending Andrew's LOCAL review, not drifted.** The §10.8 edit
   sits as an uncommitted diff on Andrew's machine (the uncommitted-in-main protocol working as
   intended), alongside other pending changes to the contract file he reviews before committing.
   Cross-checked the staged text against `goal_state_internal_test.go`: all five validation clauses
   (per-gap scope, aspect-qualified-only, root-shaped rejected, unique values, goal-referenced-only,
   plus the aspect-shaped `candidates[].pre` rejection) match the shipped engine exactly — commit-ready
   from the engine's side. *Lesson for remote verification passes: a remote container cannot see
   Andrew's local staged proposals — absence-from-`main` is NOT evidence of loss; ask before filing
   drift.*
2. **Adoption blind spot — the planner has no production consumer at all.** No installed target
   declares `mode`/`candidates`/`goal` (grep across `packages/*/targets.go`); every production dispatch
   still runs the frozen table. Fires 4–5 are engine-complete, fixture-exercised only. The spirit of
   the mandate ("remediation stops being a static lookup") is built but not yet *lived* anywhere.
3. **The Inc3 hold is structural, not temporal — its re-check trigger could never fire on its own.**
   Andrew's hypothesis, confirmed in refined form: not "one violation true at a time" (the four
   applicant gaps are concurrently true) but **one closer per gap**, mandated by §10.8's own
   dependency-gating doctrine ("a dependent gap simply isn't `true` until its prerequisite closes") —
   lens authors are *required* to pre-decompose every chain into single-op gaps, so a ≥2-op gap
   structurally cannot arise under the current authoring convention. The human does the goal
   regression at authoring time.

**Unblock (Andrew, 2026-07-05):** (a) **renewal demand filed** on `verticals.md` — LoftSpace lease
renewal as the **first goal-authored target** (per-tenant variable chain: conditional fresh bgcheck /
guarantor re-verify / rent-adjustment approval / renewal signature); its design must include the small
§10.8 doctrine rider blessing goal-first authoring as the sanctioned *alternative* to gating-cypher
decomposition. (b) **Inc3's machinery lands with its first real consumer** — the renewal target or
Fire 9's plan-shaped Augur proposals (novel gaps have no pre-authored decomposition by definition),
whichever arrives first. (c) **Fires 7–8 are unblocked now** — orthogonal to synthesis since Fire 2.
(d) When lease-signing is next touched, give Fire 5 a production consumer: `missing_payment`
`candidates` = [collectPayment flow (cheap), operator `assignTask` fallback] — the existing
"budget-spent → needs-human" terminal, made dispatchable.

**Fire 7 SHIPPED (2026-07-05):** the contraction monitor + oscillation detector (§3.4), both purely
in-memory and heartbeat-surfaced — zero dispatch-decision change. Contraction: `contraction.go`'s
`contractionStats` tracks each target's current violating-row count incrementally from lane-1 delivery,
samples it into a bounded ring on the sweep's own cadence, and the heartbeat classifies the ring
shrinking/steady/diverging (`metrics.contractionTrajectory`). Oscillation: `oscillation.go` joins a
dispatched actionRef to the aspect path(s) its declared `.effects` assert (`targetSource.effectPathsFor`,
`registry.go`) at the same two fresh-dispatch seams `bumpEffectDispatch` already uses; a trailing 4-event
strict two-target alternation on one path freezes both targets via the existing `Engine.Disable`
`__control` seam and raises one `TargetOscillation` issue naming the pair — proven end to end in
`oscillation_internal_test.go`'s fighting-targets fixture (design table's Fire 7 acceptance). Fire 8
(admission control) is next.

**Fire 8 SHIPPED (2026-07-05):** priority-fair token-bucket admission control (§3.4), purely in-memory
and wired at `planGap` — the seam both lane-1 dispatch and the reconciler reclaim share. A target's
optional `admission` block (`globalRate` / `adapterRates`, `admission.go`) is consulted only once a gap's
action has resolved (so the adapter to check is known), right before `buildPlan`; a denial returns
`NakWithDelay` with no mark, no plan, and no Health issue (ordinary pacing, never a fault). Contention is
broken by the optional §10.2 `priority` row column (higher first, default 0): every `admit()` call both
submits its own request and cooperatively drains the bucket's shared priority queue — a lower-priority
id's own redelivery cannot jump a higher-priority id already waiting; the token is granted to the
higher-priority id and collected on ITS own next call. A grant nobody ever collects (e.g. a closed gap
that never redelivers again) is reclaimed and refunded after `admissionGrantTTL`. Proven end to end in
`admission_internal_test.go`'s 3000-row burst fixture (design table's Fire 8 acceptance: "3k-row fixture
paced + priority-ordered"): a 50/sec budget admits its first 50 from free burst capacity, then drains the
remaining 2950 in rate-bounded waves that always serve the highest still-outstanding priority first.
Contract: the §10.2 `priority` column + the `admission` block land per §4's already-ratified "lands with
the fire" rider (isolated diff hunk, committed independently of the separate pending R1/Fire-3 edits
already in that file). Fire 9 (the Augur floor) is next.

**Pre-build gate (run 2026-07-04, in the Fire 5 session):** the self-imposed adversarial pass over
episode-stability under reclaim, focused specifically on the hazard the existing dispatch pipeline
structurally invited — `dispatchGap`/`planGap` resolve a plan from the target's playbook *before*
`fireEpisode` ever reads the mark, and the reconciler's `reclaim` re-derives its dispatch from the
*current* `target.Gaps[col]` on every sweep. For the frozen table this is harmless (`ga.Action` is a
static config value). For `mode:"planned"` candidates, it is not: `rankCandidates`' inputs (`__effect`
close-rate) are live and time-varying, so re-ranking on a redelivery or a sweep reclaim could silently
pick a DIFFERENT candidate than the one the open episode's `requestId`/`claimId` was derived against — a
config/data-shaped bug, not a crash, so it would have shipped quietly. Fixed load-bearing in the
implementation (not just tested around): `resolvePlannedAction` is the single choke point both callers
route through, and it takes the mark's *current* recorded `Action` as an explicit `pinnedAction` input —
non-empty means reuse verbatim (no re-rank), empty means a genuinely fresh episode. Both `dispatchGap` and
`reclaim` read the mark exactly once and thread that one snapshot through both the resolution and the
fire decision, closing the double-read race a naive port of Fire 4's `shadowCompare` call site would have
reintroduced. Proven in `planned_dispatch_internal_test.go`'s `TestReclaim_ReusesPinnedCandidate_NotFreshRank`
(a reclaim fires the pinned candidate even though a fresh rank over the current candidate list would
prefer the cheaper one) and `TestResolvePlannedAction_PinnedEpisodeReusesChoice`. Plan-vertex GC races are
Fire 6's concern (Fire 5 dispatches no plan vertices — only single-step candidate selection); revisit this
gate's plan-vertex-GC half when Fire 6 lands.

---

## 1. Problem + intent

Detection is declarative (targets are Lenses, D4); **remediation is not** — it is a hand-authored static
table. `packages/lease-signing/targets.go:38-44`: five gaps, five hardcoded actions; the *sequencing* lives
in the lens cypher (`missing_listingLeased` opens only when the applicant gaps closed AND the landlord
approved). Semantically that is a guarded linear procedure — each gap column a guard, each playbook entry
a step — i.e. exactly what a Loom pattern with guards expresses, plus a standing CDC trigger and a retry
budget. The Strategist (`strategist.go`) is a lookup, not a strategist: Weaver decides *whether/when*
(marks, leases, budgets, timers) but never *what*.

What the table can never express, and this design adds:

1. **Per-entity paths** — two violating rows in different states need different remediation chains; today
   every row of a gap fires the identical action.
2. **Selection under feedback** — multiple ways to close a gap (charge saved card / email flow / concierge
   task), chosen by precondition + observed close-rate + cost, falling through as candidates fail. Today:
   one action, then a spent budget parks at "needs human."
3. **Cross-row / cross-target sight** — contraction ("is the violation set shrinking?"), oscillation (two
   targets fighting over one aspect — today the only damper is both retry budgets silently exhausting),
   admission control (3k-row backfill = thundering herd into the bridge).
4. **A deterministic tier below the Augur** — today L3 jumps straight from "no playbook entry" to an AI
   proposal; most stuck gaps are plannable from declared effects without a model call.

The mandate, one line: *given a goal predicate and the graph's own catalog of operations, synthesize,
verify, and continuously re-plan the path that closes a gap — and prove the system is converging.*

## 2. Grounding — the existing pattern this extends (and must not disturb)

- **The dispatch seam is one function.** Lane-1 delivery → L1/L2 evaluate → `strategist.go` maps
  `gaps[col]` → `buildPlan` → Actuator fire-and-forget with episode-deterministic `requestId`. The planner
  replaces only the *mapping*; everything downstream (mark CAS-create, lease, sweep reclaim, dispatch-count
  budget, `inflight_<g>`) is untouched.
- **Episode identity is mark-anchored and MUST stay decision-stable.** A sweep reclaim re-dispatches the
  *same* episode (`claimId` preserved across reclaims; requestId from mark revision — §10.3). Therefore
  **the mark pins the planner's choice for the episode's lifetime**: the mark value already carries
  `action`; in planner mode it carries the chosen actionRef / plan hash, and a reclaim re-dispatches the
  **pinned** choice — it never replans mid-episode. Replanning happens only at episode boundaries
  (close→reopen), where a fresh mark is minted anyway. This keeps the plan a pure function without racing
  the confidence stats: stats feed *new* episodes only.
- **The guard grammar is the only predicate vocabulary** (§10.5: `absent`/`present`/`equals` +
  `allOf`/`anyOf`/`not`, two path shapes, pinned absence semantics). Effects and planner preconditions
  reuse it verbatim — one grammar, one evaluator lineage. The Starlark escape hatch stays RESERVED.
- **Core-KV reads stay Processor-side** (Andrew reflex, 2026-06-28): the planner evaluates preconditions
  against the **lens row** (§10.2 columns) — the lens already projects what the playbook needs; a
  precondition needing a column the lens doesn't project is an install-time validation error, exactly the
  §10.2↔§10.8 column-seam rule. **No new Weaver Core-KV reads.** (Loom's guard eval remains the only
  sanctioned non-Processor reader; we do not widen it.)
- **[SUPERSEDED 2026-07-05 — renewal-design ratification; see the Ratification addendum.]** ~~Loom
  pinning executes synthesized plans with zero Loom changes (plan → `meta.loomPattern` `plan-<hash>` →
  `triggerLoom`).~~ The first consumer proved a compiled pattern cannot express a multi-actor chain
  (§10.5 pins a userTask's assignee to the one instance subject): **execution is per-leg through the
  frozen action vocabulary** (§10.8 as amended); the compile-to-pattern shape is **RESERVED for op-only
  single-actor plans**, unbuilt. Zero Loom changes holds a fortiori.
- **The Augur is already the AI boundary with a human gate** (§10.8, ratified 2026-06-27): `unplannable`
  escalation → structured-output proposal → `vtx.augurProposal` → human review → `proposedOp` dispatch
  with deterministic re-validation. The planner slots **below** it as the deterministic tier;
  `unplannable`'s meaning extends naturally to "no playbook entry **and no derivable plan**." The dormant
  `exhausted` trigger (backlog `weaver-exhausted-escalation-and-model`) gets its engine path in Fire 9.

## 3. The shape

### 3.1 Determinism is load-bearing (why no LLM in the decision path)

The plan is a **pure function of (row snapshot, catalog snapshot, confidence snapshot)** with canonical
tie-breaking (cost asc, then actionRef lexicographic; bounded regression depth; no wall-clock, no map-order
dependence). Required, not stylistic: reclaim/replay machinery assumes re-deciding reproduces the decision
(§2, mark-pinning). Plan synthesis is classical goal regression over a closed catalog of dozens of actions
— STRIPS/GOAP-class search, a few hundred lines of exhaustively table-testable Go. AI never plans;
uncertainty is handled by **level-triggered replanning** (a failed candidate leaves the gap violating →
next episode replans from the new state with updated confidence — the escalation ladder emerges, unauthored).

### 3.2 The declared surface (the §10.8 extension — full text in the staged contract edit)

- **Op DDL `effects`** (additive): guard-grammar predicates the op's commit entails on its target subject.
  Install-validated (parseable, legal paths, reject-wholesale — same doctrine as pattern load). Fire 1
  declares them for the lease-signing ops (`SignLease` → `.signature` present, etc.).
- **`meta.weaverTarget` additions** (all additive, install-validated):
  - `mode: "shadow" | "planned"` (target-level; **absent = frozen behavior, byte-identical**),
  - per-gap `candidates: [{action…}, …]` — Fire 5 selection among an explicit, package-authored set,
  - per-gap `goal: <guard>` — Fire 6 synthesis when no candidate list is given.
  - **Precedence per gap: explicit single `action` > `candidates` > `goal`** — the operator override is
    always the authored action, and today's targets never change behavior.
- **Effect bookkeeping** (`weaver-state`, new reserved shape `<targetId>.__effect.<gapColumn>.<actionRef>`,
  disjoint from marks by the same reserved-underscore argument as `__control`): per-pair dispatch/close
  counters over a **sliding window of the last K=20 episodes** (ring in the value; K config-tunable like
  `MarkLease`). Updated on the two real dispatch legs (lane-1 fire + sweep reclaim) and on the gap-close
  path (`clearClosedMarks`) — the same seams the dispatch-count uses. Event-keyed, never clock-sampled;
  deterministic. GC'd by the sweep's existing orphan legs.
- **Plan vertices**: **[SUPERSEDED 2026-07-05 — Ratification addendum]** — no plan vertices in the
  ratified per-leg execution (the mark carries the plan hash, diagnostic-only); reserved with the
  op-only compile-to-pattern shape.

### 3.3 Selection and synthesis (Fires 5–6, the engine change)

Fire 5 (`candidates`): the Strategist asks the planner to pick ONE candidate — preconditions evaluated
against the row, ranked by (satisfied-preconditions, close-rate window, declared cost, canonical
tie-break). Downstream unchanged: same mark (pinning the pick), same `maxretries_<g>` budget now bounding
the *gap across candidates*, same actuator. Fire 6 (`goal`) **[as amended 2026-07-05 — Ratification
addendum]**: goal regression over the gap's **declared per-gap `actions` catalog** (not a global
installed-effects catalog — an op effect carries no dispatch binding); the plan executes **per leg**
(each episode dispatches `Steps[0]`'s action binding; pin releases on effects-hold). Dispatch-time
re-validation mirrors the `proposedOp` precedent per leg (action vocabulary, live-registry resolution,
Weaver-authority) — the planner gets no scope the table didn't have.

### 3.4 Diagnostics and arbitration (Fires 7–8, cross-row sight)

- **Contraction monitor:** per-target violating-row trajectory over a sweep-cadence window →
  `shrinking | steady | diverging` heartbeat state; "dispatches commit but closes never arrive" (readable
  from `__effect`) raises the *lens/effect-mismatch* Health issue — today's documented silent failure
  ("a Lens that only projects past deadlines … surfaces as violation never flips", weaver.md) made loud.
- **Oscillation detector:** two targets whose dispatched ops alternately rewrite the same aspect within a
  window → freeze both via the existing `__control` disable seam + one Health issue naming the causal
  pair. Freeze-and-alert only; never a new dispatch.
- **Admission control:** a dispatch scheduler between evaluator and actuator — declared budgets
  (per-adapter rate / global concurrency; config + optional package data) and an optional §10.2 priority
  column (prefix-convention class, like `freshUntil`). Default: no budget declared = today's behavior.

### 3.5 The AI boundary (Fire 9 — unchanged posture, new floor)

Planner failure ("no plan derivable") flows into the **existing** `unplannable` escalation; `exhausted`
finally gets wired (threading `augur.model` — closes the arch-review finding). Augur proposals may carry
plan-shaped sequences, dispatched via the Fire-6 plan-vertex path under the existing proposal-scoped
requestId + human gate. A synthesized plan crossing a deterministic success threshold (window-complete,
zero failures) emits a **playbook-promotion proposal** into the same review queue — remediation knowledge
compounds under review. `augur.autoApply` stays Andrew-gated, untouched.

## 4. Contract surface

**Ratified + merged to `main` (2026-07-04):** **§10.8** new "Planner extension" subsection (3.2 above,
normative); **§10.3** reserved-key-shapes block (documents as-built `__control` + `__count`, adds
`__effect`); a ratified revision-history row. NOT amended now (land with their fires, additive): the
§10.2 priority-column convention (Fire 8), plan-vertex GC detail (Fire 6 may refine constants).
Deliberately NOT touched: §10.5/§10.6 (zero Loom changes), the action table, augur block semantics,
§10.4.

## 5. Reconciliation with the existing mental model

- *Didn't the Augur already make Weaver smart?* The Augur is the **AI** tier for gaps with **no playbook
  entry**, behind a human gate, one action per proposal. The planner is the **deterministic** tier below
  it: most stuck gaps are derivable from declared effects without a model call, a review queue, or a
  human. The Augur keeps exactly the residue it was designed for.
- *Don't budgets/`inflight_<g>`/backoff already govern dispatch?* They govern **whether/when**; nothing
  governs **what** — selection is still a 1:1 table. All of that machinery carries forward unchanged
  beneath the planner (and the budget's meaning sharpens: bounding a gap across candidates).
- *Is this the 13.1 collapse reversed?* Execution placement: no — bridge/Loom placement byte-identical.
  Selection altitude: yes, deliberately — Fork 1, flagged above.
- *New state?* One new `weaver-state` shape (`__effect`); everything else reuses marks, `__control`,
  `meta.loomPattern`, the §10.8 vertex. No new buckets, no new event families, no Weaver events.
- *Does a parallel in-flight design touch this seam?* Grepped the 📐/🏗️ set 2026-07-04: no other design
  touches `strategist.go`/`buildPlan` or §10.8. Closest neighbors — `contract-10-weaver-text-reconciliation`
  (this design folds only the reserved-shapes spot; the row stays open for the other four) and the
  Chronicler's weaver-lifecycle-events pre-build note (orthogonal; this design adds no events).

## 6. Alternatives considered

- **A. Planning outside Weaver (new component, or Loom growing branches).** Rejected: a planner needs
  Weaver's exact inputs (rows, marks, budgets, close observations) — outside Weaver it's a second
  convergence engine with a coordination seam; inside Loom it breaks "linear only, conditional paths →
  Weaver" (D2/D3) and re-invents Weaver inside Loom. Could a variant beat in-place? Only if Weaver were
  unhealthy — it's the best-conformed engine audited.
- **B. Parallel Weaver 2.0 + switchover** (the tempting one). Rejected: re-ports hardened machinery,
  double-dispatch fencing on shared buckets, big-bang cutover risk. Shadow mode + per-target `mode` flag
  delivers the same confidence-before-cutover reversibly. A variant — v2 engine on a *separate* targets
  bucket — still forks every §10.2 producer (Refractor routing) for no capability gain.
- **C. LLM-as-planner.** Rejected: breaks replay determinism (§3.1) and puts a model call on the hot
  dispatch path; the closed-catalog problem doesn't need it. The AI stays at the Augur boundary.
- **D. Richer playbooks (branching/conditionals in §10.8).** Rejected: re-invents Loom inside a table and
  still can't do per-entity synthesis, feedback selection, or cross-target sight.
- **E. Starlark effects.** Rejected for now: plan-time entailment becomes undecidable; atom-vocabulary
  covers the known verticals. Revisit only with a concrete op whose effect is inexpressible (then prefer
  adding the missing *atom* — the identifier-representation reflex: extend the primitive, don't contort).

## 7. Resolved questions (decide-don't-defer)

1. **Effect inference from op Starlark?** Declared-only now; static-analysis *suggestion* is a Surveyor
   backlog line, not this design.
2. **Shadow-divergence surface?** Heartbeat counters (`plannerShadowAgree`/`Diverge`) + a per-target
   Health doc carrying the last N divergences. NOT a lens/event stream — Weaver emits no events (arch
   review), and shadow is diagnostic, not business truth.
3. **Speculative validation (Processor validate-only lane)?** **Deferred — dead-scaffolding test failed:**
   its only consumer is Fire 6, replan-on-level + dispatch-time re-validation (the `proposedOp` precedent)
   already bound the damage, and a validate-only lane is a Processor feature with no second consumer.
   Design shelved as a named follow-up, not built dark.
4. **Confidence constants?** Sliding window K=20 episodes per (target, gap, actionRef), config-tunable;
   no decay clock (event-keyed window IS the decay). Fire 5's brief may tune K; the mechanism is fixed.

## 8. Decomposition for the Steward (the fire ladder)

Value early, dispatch-risk late: Fires 1–4 change **no** dispatch decision. Every fire updates
`docs/components/weaver.md` (+ touched contract text) in the same commit. Handoff briefs are authored
just-in-time per fire from this doc.

| Fire | Scope | Acceptance (proves) | Risk |
|---|---|---|---|
| 1 | Op-DDL `effects` + pkgmgr validation; lease-signing ops declared | malformed-effect install rejection loud; zero engine change | none |
| 2 | `__effect` bookkeeping on both dispatch legs + close path; heartbeat metrics; lens/effect-mismatch Health issue | counters survive restart; sweep GC; never-closing fixture raises issue | none |
| 3 | `internal/weaver/planner` pure library (goal regression, canonical determinism, no-plan as value) | table tests + catalog-permutation stability property | none |
| 4 | §10.8 parse/validation of `mode`/`candidates`/`goal`; shadow compare + divergence surface | shadow target dispatches byte-identically; divergence visible | none |
| 5 | `mode: planned` single-step selection among `candidates`; mark pins the pick | decline-twice fixture falls A→B unaided; mode-absent targets byte-identical; revert = control-plane flag | low |
| 6 | *(amended 2026-07-05)* Goal regression over per-gap `actions` → **per-leg dispatch** (pin-release on effects-hold, leg `__effect` credit, per-leg count reset); pkgmgr authoring surface; no plan vertices/GC (reserved) | different states → different plans; same state → same plan; reclaim re-fires the pinned leg; effects-hold advances; zero Loom diffs | medium |
| 7 | Contraction monitor + oscillation detector (freeze via `__control` + Health) | fighting-targets fixture frozen + one causal-pair issue | low |
| 8 | Admission control: dispatch scheduler, budgets, §10.2 priority column (contract rider lands here) | 3k-row fixture paced + priority-ordered; no-budget = unchanged | medium |
| 9 | `unplannable`(extended)/`exhausted` → Augur; plan-shaped proposals via Fire-6 path; promotion proposals | no-plan fixture → proposal; approved plan dispatches once; promotion at threshold | medium |

**Pre-build gate (self-imposed, per the ratified-≠-build-ready rule):** before Fire 5 (first behavioral
fire), an adversarial pass over Fires 5–6 focused on episode-stability under reclaim and plan-vertex GC
races — run it in the Fire-5 session and record it in this doc.

## 9. Test strategy / migration

Unit: planner table tests (incl. determinism property), guard-entailment cases, `__effect` window math.
E2E (ephemeral stack): the per-fire fixtures above, plus the standing invariant "every mode-absent target
byte-identical to pre-change dispatch" asserted across the suite. Migration: none — every surface is
additive + opt-in; no data migration; `__effect` keys appear lazily; a rollback is `mode` removal (the
next episode uses the table; in-flight episodes are mark-pinned and drain unchanged).

### Fire 9 brief (build note, 2026-09-13 — Steward, remote fire `claude/relaxed-rubin-e79fru`)

**1. Scope sentence (verbatim, §8 row 9).** *`unplannable`(extended)/`exhausted` → Augur; plan-shaped
proposals via Fire-6 path; promotion proposals. Acceptance: no-plan fixture → proposal; approved plan dispatches
once; promotion at threshold.* Narrowed by what shipped since ratification: deliverable (a) is built and pinned
(`escalation_doors_internal_test.go`, `augur_escalation_internal_test.go` — `TestAugurEscalation_ExhaustedTriggerSymmetric`,
`goal_dispatch_internal_test.go`'s no-derivable-plan escalation), and `augur.model` reaches the adapter
(`strategist.go` `augurEscalation` `params["model"]` → `ddls.go` `.gap.model` + `external.augur` `params.model` →
`fake_augur.go` `proposalFor`). This fire builds **(b)** and **(c)**. "Via the Fire-6 path" is read under the
2026-07-05 amendment (per-leg execution through the frozen action vocabulary; no plan vertices), so a plan-shaped
proposal executes **leg by leg on the `augurDispatch` target**, each leg an ordinary `proposedOp` episode.

**2. Verified touch-list (checked live at `0775e9a`).** `internal/bridge/augur_proposal.go:25-48` (`AugurProposal`;
gains `Steps []AugurStep{Action, Params}`, additive), `:54-76` codec unchanged; `internal/bridge/fake_augur.go:60-79`
trigger subjects (+ a plan subject), `:158-211` `proposalFor` (+ the two-step benign plan; `SetProposal` override
already admits any shape). `packages/augur/ddls.go`: DDL doc `:60-68` + description `:86-115` + `InputSchema`/
`FieldDescription`/`Examples` `:117-228`; `RecordProposal` `:539-654` (decode `:589-606`, §5 boundary `:607-622`,
aspect writes `:630-641`); `revalidate_for_approval` `:408-444`; `RecordProposalDispatch` `:732-784` (guard `:761`,
flip `:772-778`); `CreateAugurReasoningClaim` actor guard `:463` (the shape the new op's guard mirrors).
`packages/augur/lenses.go`: `augurDispatchPending` `:47-62` (`BodyColumns` `:58`) + spec `:95-107`; `augurProposals`
spec `:134-151`. `packages/augur/permissions.go` (one grant per op — +1). `packages/augur/package.go:106` +
`manifest.yaml:2` (0.5.1 → 0.6.0; `PermittedCommands` `ddls.go:85` +1). `scripts/verify-package-augur.go:51`
`augOps` (+1; its permission-vertex count follows). `internal/weaver/augur_dispatch.go:42-115`
(`buildProposedOpPlan`; `:112-113` requestId + followUp), `:175-190` `recordDispatchOutcomePlan`.
`internal/weaver/actuator.go:201-212` (`deriveProposalDispatchRequestID` / `…FlipRequestID`; `deriveID` `:225` folds a
`uint64` — leg 0 must stay byte-identical to today). `internal/weaver/state.go:118-129` (`mark`; +`ProposalLeg int
json:"proposalLeg,omitempty"`), `create :168-181`, `replace :250-261`, `deleteRevision :304`; `recordEffectClose
:873-920`, `effectWindowSize :780`, `effectStats :782-793`. `internal/weaver/evaluator.go`: `dispatchGap :380`,
mark read `:506-514`, leg release `:536-545` (the seam the proposal-leg release sits beside), `fireEpisode :1085`
(`replace :1148`, `create :1175`), gap-close credit `:1508`, `releaseCompletedLeg :1723` (credit `:1796`),
`advanceReleasedLeg :1950`. `internal/weaver/reconciler.go`: reclaim leg release `:1166-1198`, `replace :1490`.
`internal/weaver/strategist.go:669-706` (`augurEscalation` — the target meta key + actor the promotion op mirrors).
`internal/weaver/planner_shadow.go:178` (`rankCandidates` — the only `__effect` window reader; the threshold reads the
same store). `cmd/loupe/review.go:287-304` (`augurProposalCols` +`proposedSteps`/`dispatchLeg`). Docs:
`docs/contracts/10-orchestration-augur.md:44-67` (additive clause), `docs/components/augur.md:97-118,161-177`,
`docs/components/weaver.md:1328` (Actions row) + `:1334` (status row). Tests to extend: `augur_dispatch_internal_test.go`,
`packages/augur/proposal_test.go`, `packages/augur/lens_cypher_test.go`, `internal/bridge/fake_augur_test.go`,
`internal/augurconvergence/augurconvergence_test.go` (a plan-shaped episode: proposal lands `pending` with N steps).

**3. Precedents to mirror.** Per-leg release: `releaseCompletedLeg`'s **marked** branch (`deleteRevision` at
`markRev`, conflict → leave it) then fall through as a fresh episode, at BOTH seams (lane 1 `:536`; reclaim `:1166`).
Leg-scoped ids: `deriveID(ns, handle, uint64(leg))` (the `revision` argument), namespaces unchanged. Mark field:
`EscalatedFrom`/`Escalation` at all three writers (`state.go:168/:250`, `evaluator.go:1148/:1175`,
`reconciler.go:1490`). New op's actor guard: `CreateAugurReasoningClaim`'s `op.actor != primordialActor["weaver"]`.
Promotion emission: the temporal lane's markless deterministic-requestId submit (`MarkExpired`,
`docs/components/weaver.md` §"Lane 3") — `act.submit`, no mark, no booking. Lens booleans with `AND`/`<>`:
`packages/cafe-domain/lenses.go:347-348`. Legacy-tolerant DDL reads: `RecordProposalDispatch`'s
`rd["reviewedAt"] if … in rd else ""` (`ddls.go:764`).

**4. Increment order + green checks.** **Inc A — plan-shaped proposals** (builder tier **opus**: a new mark field, a
new release seam, a DDL state machine). Shape (Winston, decided): the structured output gains an ordered `steps`
list; `RecordProposal` normalises every proposal to `.proposed {action, params, steps}` where `steps` has ≥1 entry
(≤8) and `action/params` mirror `steps[0]`; the §5 boundary runs per step (vocab + default-deny scope; a failing
step names its index) and `revalidate_for_approval` re-runs it per step; `.review` gains `leg` (legs dispatched so
far, 0 at record); `augurDispatchPending` projects `proposedSteps` + `dispatchLeg` and `augurProposals` the same;
`buildProposedOpPlan` dispatches `steps[dispatchLeg]` (null `steps` → the legacy `proposedAction/proposedParams`,
leg 0) under `deriveProposalDispatchRequestID(handle, leg)` with the flip carrying `leg`; `RecordProposalDispatch`
refuses a `leg` ≠ `review.leg` (`InvalidDispatchTransition`), advances `leg` and stays `approved` while legs remain,
flips `dispatched` + stamps `dispatchedAt` on the last, and an `invalid` outcome on any leg invalidates the whole
proposal (no half-plan continues); the mark carries `proposalLeg`, and a `proposedOp` mark whose `proposalLeg` <
the row's `dispatchLeg` is released (revision-conditioned) and the next leg dispatched fresh — at lane 1 and at the
reclaim. Ordering: Weaver publishes every op to ONE `ops.<lane>` (`actuator.go:113`) and the Processor drains a
lane serially, so leg N+1 (published after leg N's flip re-projected the row) is processed after leg N; a leg's
success is not verified (fire-and-forget, as today's single-step dispatch) — a rejected leg leaves the origin gap
violating, which re-escalates and a fresh proposal supersedes. Green: `go test ./internal/weaver/ ./internal/bridge/
./packages/augur/ -count=1`; revert-proofs: the third mark writer's threading, the leg guard in the DDL, the lane-1
release, the reclaim release, the per-step scope check (a foreign key in step 2 alone → invalid); a legacy-shape
vector (no `steps`, no `leg`) dispatches exactly as before. **Inc B — promotion** (**opus**: a new emission seam +
latch): after a close credit lands for a `mode:"planned"` gap (`:1508` gap-close, `:1796` leg release), read that
ref's `__effect` window; when it holds `effectWindowSize` entries and every one is closed, submit
`RecordPromotionProposal` (new augur op, Weaver directOp, actor-guarded) under `deriveID("promotion:",
targetId+"\x00"+gap+"\x00"+ref, 0)` with no mark — the op mints `vtx.augurproposal.<handle>` (handle deterministic
from the same triple, CreateOnly = the durable latch) with `.gap {targetId, entityId: <the target's meta key>,
gapColumn, trigger: "promotion"}`, `.proposed {action: "promotePlaybook", params: {targetId, gapColumn, actionRef,
window, closed}, steps: []}`, `.rationale`, `.confidence {score: 1.0}`, `.provenance {model: "weaver"}`, `.review
{state: pending, leg: 0}`, `forCandidate`/`forTarget` links to the target meta; an in-memory once-latch per triple
keeps repeats off the wire (a repeat collapses on the tracker anyway). `augurDispatchPending`'s `violating` becomes
`(review.state = "approved") AND (gap.trigger <> "promotion")` — an approved promotion is a recorded, human-ratified
recommendation for the package author, never a dispatch. Green: a fixture that closes one goal leg K times emits
exactly one proposal (K−1 closes emit none; a 21st close emits none; `resetConfidence` then K more emits none — the
handle collapses); the lens vector pins `violating=false` for an approved promotion. **Then:** `go build ./...`,
`make vet`, `golangci-lint run ./...`, every `scripts/lint-*.go` STRICT (`lint-weaver-classify-by-shape`,
`lint-conventions`, `lint-package-standard`, `lint-package-version` with `DIFF_BASE`), `internal/refractor` corpus
census pins (`go test ./internal/refractor/ -run Census -count=1` under `POSTGRES_TEST_DSN` — the lens edits move
column counts), `make test-augur-convergence`, `make test-control-plane-authz`, full `go test ./... -p 4` with
`POSTGRES_TEST_DSN`. Docs + the contract clause land with Inc B. One cold **opus** adversarial pass over the whole
diff at close.

**5. In-scope gotchas.** Package edit ⇒ manifest + `Version` bump in lockstep, `verify-package-augur` `augOps` +
permission count, `PermittedCommands` +1. The contract clause is additive and lands with the build (CLAUDE.md's
2026-09-01 exception: an observable promise the runtime now keeps) — `📐` banners are not used for it. No
history-narrating comments. A weaver fixture `targetId` ≤ 20 chars. Read-posture: every `kv.Read` the new op adds
is `(a)` with the key in Weaver's `contextHint.reads`. Dossier entries that bind (`docs/components/weaver.md`
§"Review keeps catching", verbatim leads + checks): (1) *A gap class is decided by the dispatch's SHAPE, never by
its action name … for every new seam that fires an episode, name the classifier it calls and the pacing rule it
inherits* — the proposal-leg release fires nothing itself; the fresh leg goes through `planGap`/`fireEpisode`
(CAS-create, `proposedOp`'s collapse-only reclaim class). (3) *A shared test fixture that always supplies an
OPTIONAL input pins only the supplied case … require one vector that omits each* — `steps`, `leg`, `proposalLeg`
are all optional on legacy documents: one vector omits each. (5) *A value grammar extended at a shared resolver
reaches EVERY field that resolver serves* — a step's params pass the same raw-string refusals as a single proposal's
(`validateProposedDispatch`), per step. (6) *A Health issue key is a LATCH …* — no new issue key here. (12) *A new
field on a record is a claim about every WRITER of it … grep every writer, list them, name which pin covers each*
— `mark.ProposalLeg`: three writers, three pins. Standing checklist items 1–6 walked; #1 (the once-latch: created
on first emission, reset on restart — harmless, the vertex is the durable latch; the `leg` counter: created at
record, carried through review, advanced by the flip, terminal at `len(steps)`), #3 (every plumbing increment
revert-proved — the `steps` threading, the `leg` threading, the mark field), #5 (one deterministic handle, one
writer — the promotion vertex is CreateOnly under a single Weaver submitter).

**6. Adjacent finds.** No `packages/*/targets.go` declares `Goal:` or `Mode:` (census, 2026-09-13): the promotion
producer has no production goal-mode gap to fire on today — engine-complete and fixture-exercised, the posture
Fires 4–5 shipped under; not a defect, recorded here. Loupe's review view renders `proposedParams`; the plan's
`steps` column reaches its wire shape in this fire, its rendering is the Loupe lane's (`cmd/loupe/web`) — a
column, not a defect.

**7. Non-goals.** `augur.autoApply` (Andrew-gated); a real model-backed adapter; effects-hold leg release for a
proposal (no row predicate exists for the origin candidate on the `augurDispatch` row — recorded above as the
ordering rule instead); `candidates` pkgmgr authoring; the reserved `plan-<hash>` shape; any change to the two
escalation doors or their pacing.

**Scope-diff gate:** parts 2–4 trace to §8 row 9's three deliverables — (a) verified shipped and narrowed out, (b)
and (c) built; "via Fire-6 path" resolved by the ratified amendment to per-leg execution, no plan vertices; nothing
substituted. Dependencies both ways: the two escalation-episode designs' seams are live at head (part 2); nothing
in Fires 1–8 is edited.
