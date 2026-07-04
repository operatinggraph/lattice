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

**Next:** Fires 1–5 shipped (op-DDL `effects`; `__effect` confidence window; the pure `planner` library;
`mode`/`candidates`/`goal` parsing + shadow compare; `mode:"planned"` candidate-selection dispatch). The
Lattice Steward builds **Fire 6** (goal-regression synthesis dispatch) next from §8.

**🏗️ Fire 6 CHECKPOINT (2026-07-04, Increment 1 of 2):** Increment 1 shipped — the runtime op-effects
catalog Fire 4/5's own comments flagged as missing (Fire 1 validated `Effects` at install time but never
persisted them; nothing existed for a planner to read). `pkgmgr.buildInstallBatch` now materializes each
op-meta vertex's declared Effects onto a sibling `.effects` aspect; `validateEffects` now also requires a
matching `OpMetaSpec` (fail-closed — an effect with nowhere to materialize would silently never reach any
catalog); the Weaver registry indexes it (order-independent join against the op-meta envelope) and exposes
`effectsCatalog() []planner.Action`. Zero dispatch-decision change; full detail in
`docs/components/weaver.md` "Op-effects runtime catalog" section.

**Increment 2 (next fire) must resolve first, before any dispatch/plan-vertex/GC work**: a real
**State-schema gap** the catalog's own shape surfaces. `rowState` (Fire 4/5) maps a lens row onto **root**
guard-grammar paths (`subject.data.<column>`) — the space a `goal`/`pre` guard is authored against — but a
declared op **Effect** (e.g. `SignLease`'s `subject.signature.data.signedAt`) asserts an **aspect** path.
These are disjoint keys in `planner.State`: `planner.Synthesize`'d search could never let a real catalog
action's effects satisfy a row-authored goal, so naively wiring Fire 6 dispatch on top of `rowState` alone
would silently return `ErrNoPlan` for every real target — a config/data-shaped bug that ships quietly,
exactly the class of hazard the Fire 5 pre-build gate existed to catch. Leading candidate: build the
goal-regression starting `State` from a fresh read of the candidate subject's aspects (not the lens row),
keyed to match the catalog's path space — ground this against a real target's `goal` declaration (none
ship yet; Fire 5 only shipped `candidates`) before committing to the shape. Only once this is resolved do
plan-vertex compilation (`plan-<hash>`), the new op/DDL surface for Weaver to author a `meta.loomPattern`
vertex at runtime (no existing precedent — package install is the only place one is created today), plan
GC, and dispatch-time re-validation become safe to build.

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
- **Loom pinning executes synthesized plans with zero Loom changes.** A plan compiles to an ordinary
  linear pattern; Weaver submits it as a `meta.loomPattern` vertex **via the Processor** (P2, auditable),
  keyed by content hash (`plan-<hash>`), then `triggerLoom` as today. The instance pins at start (§10.5)
  and drains under its definition regardless of later GC. Re-derivation of the same plan hits the same
  vertex — re-fires collapse instead of multiplying patterns.
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
- **Plan vertices**: `meta.loomPattern` named `plan-<hash(canonical plan JSON)>`, submitted via Processor,
  GC'd when no live instance pins them and no current (state, catalog) re-derives them.

### 3.3 Selection and synthesis (Fires 5–6, the engine change)

Fire 5 (`candidates`): the Strategist asks the planner to pick ONE candidate — preconditions evaluated
against the row, ranked by (satisfied-preconditions, close-rate window, declared cost, canonical
tie-break). Downstream unchanged: same mark (pinning the pick), same `maxretries_<g>` budget now bounding
the *gap across candidates*, same actuator. Fire 6 (`goal`): full goal regression over the installed
catalog (ops with `effects` + Loom patterns as macro-actions); the resulting chain compiles to a plan
vertex + `triggerLoom`. Dispatch-time re-validation mirrors the `proposedOp` precedent (action vocabulary,
live-registry resolution, Weaver-authority) — the planner gets no scope the table didn't have.

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
| 6 | Goal regression → `plan-<hash>` vertices → `triggerLoom`; plan GC; dispatch-time re-validation | different states → different plans; same state → same vertex; GC proven; zero Loom diffs | medium |
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
