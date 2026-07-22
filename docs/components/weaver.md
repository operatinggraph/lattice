# Weaver

**Component reference** | Audience: implementers + architects

> Decisions of record live in `_bmad-output/planning-artifacts/lattice-architecture.md` →
> "Phase 2 Architecture — Orchestration Core" (D3, D4). Data shapes are frozen in Contract #10
> (§10.2 target rows, §10.3 weaver-state, §10.4 scheduling, §10.8 target+playbook) — where this
> page and the contract diverge, the contract governs. Update this page in the same commit as the
> code; drift is a documentation bug.
>
> This page describes **what Weaver is**. A per-surface ledger of what is built vs. deferred lives
> in [Implementation status](#implementation-status) at the end.

External idempotent I/O (FR58 / NFR-S11) is **not** a Weaver concern: a target that needs an
external call remediates with **`triggerLoom`** of a Loom **`externalTask`** pattern, and the
**bridge** component (`docs/components/bridge.md`) executes the call idempotently. Weaver detects
and dispatches; it never reaches an external system itself.

The engine ships **zero domain knowledge** — targets and playbooks are package data; domain
literals appear only in tests and fixtures.

---

## Overview

Weaver is the **convergence engine** — it drives the graph toward declared **target states** by
detecting discrepancies and remediating them, optionally by triggering Loom utilities. It is
the declarative counterpart to Loom (brainstorming #122): **Weaver decides *what* is missing;
Loom executes a *fixed procedure* to fill a gap.** A "Lease Application complete" is a *target
state*, not a workflow.

Crucially, **a Weaver target is a Lens** — Weaver is a **consumer of the Refractor**, never its
own cypher runtime. The Refractor projects "currently-violating" rows; Weaver reacts.

Weaver is an **internal service actor** at root-equivalent capability. It **submits operations
through the Processor** (never writes Core KV directly). Its only direct write is to the
`weaver-state` bucket (dispatch/bookkeeping marks, Contract #10 §10.3).

---

## Pipeline

```
Sensorium (3-lane intake — an in-process multiplexer; no durable weaver-work bucket in Phase 2, §10.3)
   → Evaluator (L1 re-confirm + in-flight dedup; L2 hydrate + classify gap + select playbook)
   → Strategist (playbook registry: gap-type → action)
   → Actuator (OCC ops via Processor; trigger Loom via op; external I/O via triggerLoom of an externalTask)
```

Each Phase-2 lane replays from its own durable **source** (lane 1 from `weaver-targets`, lane 3
from `core-schedules`) with dedup in `weaver-state`, so a separate normalized `weaver-work` queue
would be redundant — it is deferred with lane 2 (§10.3).

### The 3 lanes

Each lane exists because the others structurally cannot see its violations:

| Lane | Trigger | Rationale |
|------|---------|-----------|
| **1. Violation-driven** | a row in a target Lens's output (CDC over the target KV) | the main path; Refractor keeps the target live |
| **2. Event-driven (targeted-audit)** | a `core-events` event → re-evaluate only the touched subgraph | for targets too costly to keep continuously projected |
| **3. Temporal** | a fired `@at` schedule on `core-schedules` (`schedule.weaver.timer.fired.>`, ADR-51 / §10.4) | time-derived violations emit no CDC (e.g. "bgcheck older than 90d") |

### Evaluator tiers

| Tier | Job | Status |
|------|-----|--------|
| **L1** | re-confirm row is still violating; drop if already in-flight (`weaver-state` mark) | ✅ |
| **L2** | hydrate context, classify the specific gap, select playbook input | ✅ |
| **L3** | AI-assisted reasoning for ambiguous/novel discrepancies — the **Augur** | ✅ Shipped (escalation + human review + approved-proposal dispatch; the autonomy boundary, `augur.autoApply`, stays Andrew-gated) |

### Strategist — playbook registry (package data)

A **playbook** maps gap-type → action. Engine is a generic dispatcher; playbooks ship in the
package (`lease-signing`):

| Gap (example) | Action |
|---------------|--------|
| `missing_onboarding` | `triggerLoom` of the onboarding pattern → **triggers Loom** (op-based, D3) |
| `missing_bgcheck` | `triggerLoom` of a background-check `externalTask` pattern (the bridge runs the call) |
| `missing_payment` | `triggerLoom` of a collect-payment `externalTask` pattern (the bridge runs the call) |
| `missing_signature` | `assignTask` — assign a sign **task** to the applicant |

### Actuator

- **OCC** — every op carries a revision-condition (substrate per-key revisions) so
  two ticks can't double-apply.
- **Triggers Loom via an op** — auditable, idempotent ledger entry (not a Go call; engines share
  only `substrate/*`).
- **External actions** — `triggerLoom` of a Loom `externalTask` pattern: the call is dispatched and
  executed idempotently by the **bridge** (`docs/components/bridge.md`), never by Weaver itself.

---

## Targets as Lenses (D4)

Targets project **one row per *candidate* entity with a `violating` boolean** (+ gap columns),
**not** row-only-when-violating — a gap closing flips the flag via a normal **upsert** (already
supported); only true entity deletion deletes a row (`IsDeleted`, already handled). This **avoids
forcing Refractor retraction work** in Phase 2.

The rows land in one shared, primordial **`weaver-targets`** NATS-KV bucket (the existing `nats_kv`
adapter), keyed `<targetId>.<entityId>` — the entity **NanoID**, never the dotted vertex key (the
full `vtx.<type>.<id>` rides the value's `entityKey`). Per the frozen §10.2 shape:

```
bucket: weaver-targets
key:    <targetId>.<entityId>
value:  { entityKey, violating, missing_onboarding, missing_bgcheck, missing_payment,
          missing_signature, <param columns, e.g. applicant>, freshUntil?, projectedAt }
```

Weaver does a **filtered `<targetId>.>` watch** per target it manages (lane 1) and **acts only on
`violating == true`**. A satisfied entity has `violating=false`; the row vanishes only on true
deletion. (True "emit-only-when-violating" + Refractor negative/filter-retraction projection is a
**deferred** scale-time capability.)

The freshness rule lives **in the target cypher**, not the engine: `missing_bgcheck = NOT
EXISTS(check WHERE date > now − window)`, and the cypher projects the next deadline as the optional
`freshUntil` column the temporal lane arms a timer from (below).

**A bespoke, package-defined obligation is a sanctioned target shape, not a new mechanism.**
"Executable Paper" clauses (`vtx.clause` — a prose obligation with a Starlark/cypher predicate and a
formula, owned by a vertical package) are an ordinary candidate entity: the clause vertex is the row,
and `missing_charge` / `missing_inspection` are ordinary `missing_<g>` gap columns computed by the
clause-satisfaction lens exactly like any other target. Weaver never evaluates the clause itself — it
still only watches `violating` and dispatches the playbook action (`directOp DebitAccount` for a
computational gap, `assignTask` for a judgment one); the clause's bespoke-ness lives entirely in the
package's DDL + lens + playbook, never in a Weaver-side runtime. See
`_bmad-output/implementation-artifacts/semantic-contracts-executable-paper-design.md`.

---

## Temporal lane — NATS scheduled messages (ADR-51, Contract #10 §10.4)

Time-derived violations emit no CDC, so Weaver converts **time into an op** using NATS native
message scheduling on the platform-wide **`core-schedules`** stream (`AllowMsgSchedules: true`,
subject root `schedule.>`, provisioned at bootstrap — not Weaver-owned):

```
Lens projects the deadline: row column freshUntil = resolve + window (RFC3339)
  → on each row delivery the Actuator publishes @at(freshUntil) on core-schedules,
       subject  schedule.weaver.timer.<targetId>.<entityId>
       header   Nats-Schedule-Target: schedule.weaver.timer.fired.<targetId>.<entityId>
  ... NATS holds it (durable across restart; re-publish to the same subject replaces) ...
  → at expiry the scheduler republishes the payload BACK INTO core-schedules at the fired subject
  → the weaver-temporal durable (filtered schedule.weaver.timer.fired.>) converts it to a
       MarkExpired op via the Processor (deterministic requestId: schedule subject + fire instant)
  → CDC + outbox event → target Lens re-projects → the freshness gap flips violating → lane-1 remediates
```

- **The freshness window lives in the target cypher, never the engine**: the Lens computes
  `resolve + window` and projects it as the engine-recognized **optional row column `freshUntil`**
  (RFC3339 string; a §10.2 free-form param column by carriage). The engine converts time→op only —
  a non-string/unparseable `freshUntil`, or one without an `entityKey`, surfaces a `RowDataError`
  Health issue and skips scheduling; a **past** `freshUntil` never schedules (any previously-armed
  firing is already durable in the stream; a Lens that only projects past deadlines is a package
  bug that surfaces as "violation never flips").
- **Level-driven scheduling, no edge detection**: every row delivery carrying a future
  `freshUntil` re-publishes the schedule — idempotent under one-schedule-per-subject replace —
  so re-doing the entity before expiry **replaces** the prior timer, and restart replay re-arms
  for free. A schedule-publish failure Naks the row on the bounded delay cadence.
- **Per-target-per-entity timer slot**: the subject carries both dot-free tokens
  (`schedule.weaver.timer.<targetId>.<entityId>`), so two targets watching the same entity hold
  independent timers. The `fired` token is reserved in this subject space — a targetId literally
  named `fired` is refused at scheduling time with a loud Health issue.
- **No weaver-state mark, no lease, for the fired→op conversion** — the §10.4 deterministic
  `requestId` is the dedup: a redelivered firing collapses on the Contract #4
  `vtx.op.<requestId>` tracker; a re-armed timer's new fire instant is a genuinely new op.
  Marks/OCC remain lane-1 remediation-dispatch machinery.
- **Never injected into `core-events` directly** — the transactional outbox stays the sole event
  producer; the fired message becomes a normal **op** (`MarkExpired`, payload
  `{entityKey, targetId, expiredAt}`, submitted under Weaver's service-actor authority with no
  `authContext`; the op's DDL/grants are package data).
- **Accepted Phase 2 bounds** (operator-visible, self-healing):
  - A `MarkExpired` **rejected at the Processor** is not re-attempted by Weaver (fire-and-forget,
    nothing leases it) — the freshness flip then waits for the next CDC touch of the entity. An
    op-*publish* failure IS retried (Nak → the same requestId).
  - With `MaxMsgsPerSubject: 1` on `core-schedules`, a **newer firing at the same fired subject
    rolls up an older one** the consumer has not yet processed — only the latest conversion is
    delivered, which is level-correct (the newest `expiredAt` supersedes; both would poke the
    same entity).
  - **No cancel/purge**: a deleted entity or removed target leaves a pending timer armed; the
    stray fire produces one `MarkExpired` for an absent entity — rejected/no-op at the
    Processor, harmless (no mark, no retry).
- The **fixed `weaver-temporal` durable**'s ack floor is the missed-while-down recovery: fired
  messages persist under limits retention and the durable resumes from its floor on restart.
  Phase 2 is single-instance; multi-instance fan-out is a Phase 3 concern. Heartbeat metrics
  `timersScheduled` / `timersFired` count the lane's two legs since start. No custom scheduler
  subsystem.
- **The recurring leg — the platform sweep (`@every`, `internal/weaver/sweep_schedule.go`)**: the
  same `core-schedules` stream carries the reconciler sweep's cadence. `armSweepSchedule` runs on
  every engine start (idempotent — per-subject rollup converges a restart, an interval change, or a
  second replica to one schedule) and publishes a durable `@every` schedule at
  `schedule.weaver.sweep` firing to `schedule.weaver.sweep.fired`; the fixed **`weaver-sweep`**
  durable runs one level-reconcile pass per fired occurrence (`MaxAckPending: 1`, so a replica
  never self-overlaps a pass; the handler Acks unconditionally — a redelivered occurrence is one
  harmless extra pass, so the §10.4 per-occurrence `requestId` rule is moot here). The fired
  subject lies outside `schedule.weaver.timer.fired.>`, so the two durables never overlap.
  `substrate.ScheduleEvery` enforces the `@every` 1s floor (a sub-second `SweepInterval` is clamped
  up; the warm pass at engine start covers cold-start promptness). Recurring-schedule semantics —
  persist + re-fire, per-subject replace, `Nats-Schedule-Next: purge` cancellation — are Contract
  #10 §10.4.
- **There is no op-vertex pruner and none is needed** (#47 is realized by the two legs above; #49
  retired): NATS per-key TTL GCs the `vtx.op.<requestId>` idempotency trackers and the
  transactional-outbox tombstone clears `.events` — Contract #4 §4.3; no third op-vertex class
  accumulates.

---

## Dispatch suppression — `inflight_<g>` (Lens) + the `maxretries_<g>`-bounded dispatch-count (Weaver-state)

A gap is a `missing_<g>` §10.2 bool. Weaver suppresses re-dispatch of a gap on **two** grounds — a
remediation already in flight, and a spent retry budget — sourced differently:

- **`inflight_<g>` — a Lens column.** A Lens may project, **per gap**, the engine-recognized
  **dispatch-suppression companion** `inflight_<g>` (the **prefix-swap** convention). It is a §10.2
  `BodyColumn` Weaver reads to alter behaviour, the **same mechanism class as `freshUntil`** — **not**
  a `gaps` key, so the gap-column scans (`openGapColumns`, `markCandidateColumns`, which match the
  `missing_` prefix only) never treat it as a gap or write a mark at it. An **absent or non-bool**
  `inflight_<g>` reads `false` (via `boolColumn`, which surfaces a non-bool as a `RowDataError`).
- **The retry budget — a Weaver-state dispatch-count bounded by `maxretries_<g>`.** Weaver keeps a
  per-`(targetId, entityId, gapColumn)` **dispatch-count** in `weaver-state`
  (`<targetId>.<entityId>.<gapColumn>.__count`, a reserved key shape disjoint from marks and the
  `__control` marker). It is **incremented on each actual dispatch** — the lane-1 CAS-create-and-fire
  and the sweep's reclaim, so it tracks one-per-anti-storm-window real attempts from **both** dispatch
  legs — and **reset (deleted) on gap-close** by `clearClosedMarks` (the same level-reconciled path
  that deletes the mark). The Lens supplies only the **cap**: an integer `maxretries_<g>` column
  (package policy baked into the cypher, like the freshness window). The budget term suppresses when
  `dispatchCount(target, entity, gap) >= maxretries_<g>`.

The budget lives in Weaver-state, not the Lens, because a true reset-on-success ("failures since the
last success") is **not expressible as a lens predicate**: a lifetime `count(failed) >= cap` never
resets, so a check that fails to the cap, then completes, then goes stale would wedge the renewal
forever. Gap-close **is** the reset, and it lives where Weaver owns the close path. The count is
**chain-scoped**: it persists across mark-lease/TTL expiries (a reclaim must accumulate, not reset),
with a long TTL backstop (`dispatchCountTTLBackstopFactor × MarkLease`, far larger than the mark's)
**only** to GC an orphaned count whose gap-close was never observed — never to expire mid-chain.

The gate (`gapSuppressed` = `inflight_<g>` **OR** dispatch-count `>= maxretries_<g>`) is read in
**both** dispatch legs:

1. the **lane-1 dispatch loop** (`evaluator.go`, before `dispatchGap`), and
2. the **sweep `reclaim`** (`reconciler.go`, beside the `violating` gate) — the **load-bearing**
   one: a mark-lease expiry → reclaim is the actual re-dispatch path for a long-pending remediation,
   so the lane-1 skip alone would not stop the sweeper.

While either ground holds, Weaver **does not (re-)dispatch** the gap's remediation — but the gap
**stays violating** (the entity is genuinely unsatisfied); only re-dispatch is suppressed. Every
default is the **safe (dispatch) side**: an absent/non-bool `inflight_<g>`, an absent/non-positive
`maxretries_<g>`, or a transient count-read failure all leave the gap dispatchable, so a missing or
garbled input never silently wedges a real gap. Once the budget is spent the gap is the
operator-/Loupe-visible **"needs human escalation"** terminal — a human-submitted remediation that
*completes* closes the gap, which deletes the count, so a later reopen starts a fresh budget.

The lease-signing convergence lens is the reference user — for the bgcheck/payment external-call gaps
it projects `inflight_<g>` (a service instance with a `.dispatch` marker **present** and no
`.outcome` — presence-based, not deadline-bounded, so a stuck-pending call is never double-dispatched
against the vendor) and the constant `maxretries_<g>` caps; Weaver's dispatch-count enforces the
bounded retry against those caps.

---

## Planner-mandate effect bookkeeping — `__effect` (Contract #10 §10.3/§10.8, Fire 2)

Weaver keeps a per-`(targetId, gapColumn, actionRef)` **confidence window** in `weaver-state`
(`<targetId>.__effect.<gapColumn>.<actionRef>`, a reserved key shape disjoint from marks, `__control`,
and `__count` — the same reserved-underscore-token argument). Unlike the dispatch-count (scoped to one
`(targetId, entityId, gapColumn)` chain), the effect window aggregates **across every entity** that
dispatches a given `(target, gap, actionRef)` — it is the future ranking input for Fire 5's `candidates`
selection (close-rate), not a dispatch gate.

The value is a **FIFO ring capped at `effectWindowSize` (K=20)**: each real dispatch appends one
`false` (pending) entry; a gap-close flips the **oldest still-pending** entry to `true` (FIFO matching
across entities, not per-episode pairing — Fire 5's ranking only reads the aggregate close-rate).
Eviction past the cap ages out old episodes with no clock sampling — the sliding window is the decay.
Written at the **same two dispatch legs the dispatch-count uses** (the lane-1 CAS-create-won path and
the sweep's reclaim) and the level-reconciled gap-close path (`clearClosedMarks`, which reads the
mark's `Action` **before** deleting it so the close lands against the same `actionRef` the dispatch
recorded).

Unlike the dispatch-count's TTL backstop, `__effect` keys carry **no TTL** — they are GC'd by a
dedicated sweep leg (`sweeper.sweepEffect`) mirroring the mark orphan legs' registry-warm-up gate: a
window whose target is uninstalled or whose gap column the current playbook no longer declares is
deleted; a full-target removal is also covered for free by `deleteByTargetPrefix` (Disable/Enable/
Revoke), since `__effect` keys share the `<targetId>.` prefix.

The heartbeat (cadence, never per-message) scans every `__effect` window and raises a
**`LensEffectMismatch`** issue (`warning`) for any whose window is **full (K dispatches) with zero
observed closes** — the loud surface for "dispatches commit but closes never arrive" (a stale/wrong
guard, a lens projecting the wrong column, or a remediation that silently no-ops), clearing once a
close lands or the window ages back below K. `metrics.effectMismatches` carries the live count.

Fires 4–9 (`mode`/`candidates`/`goal` parsing + shadow compare, selection, goal regression,
diagnostics, admission control, the Augur floor) remain — see
`weaver-planner-mandate-design.md` §8. Fire 2 changes **no** dispatch decision.

---

## Planner goal-regression library — `internal/weaver/planner` (Contract #10 §10.8, Fire 3)

A new, pure standalone package (`internal/weaver/planner`; imported by nothing yet — Fires 5/6 wire it
into the Strategist) implementing the §3.1 bounded goal-regression search: `Synthesize(goal, start,
catalog, maxDepth)` performs uniform-cost STRIPS/GOAP-class search over a closed `[]Action` catalog
(each an `actionRef` + `Cost` + optional `Precondition` guard + `Effects` — the §10.5 guards a commit
entails), returning the cheapest-total-cost sequence that makes `goal` hold, or the sentinel `ErrNoPlan`
— a normal return value, not a panic, exactly Fire 6's future `unplannable` routing needs.

The package reuses `internal/guardgrammar`'s AST verbatim (no new grammar) via a pure in-memory
`State` (`map[guardgrammar.Path]any`) and an `EvalGuard`/`ApplyEffects` pair that mirror
`internal/loom/guard_eval.go`'s absent/present/equals/allOf/anyOf/not semantics node-for-node but read
a snapshot instead of Core KV — no I/O, no engine dependency, so `pkgmgr` or a future test harness can
run it standalone. `ApplyEffects` only accepts concrete assertions (present/absent/equals, or an allOf
of those); an anyOf/not effect is rejected (`ErrUnsupportedEffect`) since it cannot be turned into a
definite fact.

Determinism is enforced, not assumed: the catalog is copied and canonically sorted (cost ascending,
then `actionRef` lexicographically) before every search, so the caller's slice order never affects the
result; ties between equal-cost plans break on the lexicographic join of their action-ref sequence.
`maxDepth` bounds search depth as a backstop against oscillating effects. No dispatch wiring yet — the
Strategist does not call this package (Fire 5 candidate selection, Fire 6 full synthesis).

---

## `mode`/`candidates`/`goal` parsing + shadow compare (Contract #10 §10.8, Fire 4)

`meta.weaverTarget` gains three optional, install-validated fields (`registry.go`): a target-level
`mode` (`"shadow" | "planned"`, absent = every target installed before this fire, byte-identical), and
per-gap `candidates` (`[]GapCandidate` — an alternative-action list, each with `action`/`pattern`/
`subject`/.../`params`/`reads` mirroring `GapAction`, plus an optional `pre` guard and a `cost`) and
`goal` (a raw §10.5 guard). `validateTarget` rejects the whole target on a malformed `mode`, a
candidate with no `action` or a negative `cost`, or a `pre`/`goal` that fails `guardgrammar.Parse` —
the parsed guards are cached on the registered `Target` (`preGuard`/`goalGuard`) so nothing re-parses
per dispatch. **Zero dispatch change**: `dispatchGap` still reads `ga.Action` exactly as before; a
target with none of these fields set behaves identically to pre-Fire-4.

**Shadow compare** (`planner_shadow.go`) is the diagnostic Fire 4 actually runs: for a `mode:"shadow"`
target whose gap declares `candidates`, `Engine.shadowCompare` independently ranks them — eligible
first (`pre` evaluated against the row, each column addressed as `subject.data.<column>`, the
row-is-`planner.State` convention `internal/weaver/planner` already documents), then higher windowed
close-rate (reading the Fire-2 `__effect` bookkeeping via the new `markStore.effectCloseRate`), then
lower `cost`, then lexicographic `actionRef` — and compares the pick to `ga.Action` (the table's actual
dispatch). The comparison runs **after** the real dispatch decision is made and **never feeds back into
it**: `shadowCompare` reads `ga`, ranks, and records; it has no path to `fireEpisode`. Agree/diverge
counters and a bounded (`shadowDivergenceHistory` = 10) per-target divergence log are in-memory only
(`shadowStats`, reset on restart — "diagnostic, not business truth", design §7) and surface on the
heartbeat as `metrics.plannerShadow.<targetId>.{agree,diverge,recentDivergences}`, present only once a
target has run at least one comparison. `goal`-based shadow comparison is deliberately **not** run here
— ranking a synthesized plan needs the installed op-effects catalog, which first exists at runtime with
Fire 6's engine work; `goal` is parsed + validated now so Fire 6 has nothing left to reject at install
time.

Fires 5–9 (selection dispatch, goal-regression synthesis + dispatch, diagnostics, admission control, the
Augur floor) remain — see `weaver-planner-mandate-design.md` §8. Fire 4 changes **no** dispatch decision.

---

## `mode:"planned"` candidate selection dispatch (Contract #10 §10.8, Fire 5)

The first fire that changes a real dispatch decision: on a target in `mode:"planned"`, a gap whose
playbook entry has no explicit `action` (candidates-only) now actually dispatches — the explicit-`action`
precedence and every other mode (absent, `shadow`) are untouched, so every target installed before this
fire, and every gap that still names an explicit `action`, is byte-identical.

`Engine.resolvePlannedAction` (`strategist.go`) is the single choke point both the lane-1 evaluator
(`evaluator.go: dispatchGap`/`planGap`) and the reconciler sweep (`reconciler.go: reclaim`) route through
before `buildPlan`. It reuses Fire 4's `rankCandidates` verbatim (eligible-first, then windowed
close-rate, then cost, then lexicographic `actionRef`) — but only for a **genuinely fresh episode** (no
mark yet). The load-bearing branch is the other one: **an episode that already has a mark reuses that
mark's recorded `Action` verbatim, never re-ranking** — a reclaim/redelivery of an open episode must fire
the *same* choice it was dispatched with, even if a fresh rank over current confidence stats would now
prefer a different candidate (design §2: "the mark pins the planner's choice for the episode's
lifetime"). Both callers read the mark **once**, up front, and thread that single snapshot through both
the resolution and the fire decision — a double read could let the two disagree across a legitimate
close→reopen. A pin whose candidate the playbook no longer declares (edited out mid-episode) is a
`PlaybookConfigError`, alerted and left for the next sweep — never silently re-ranked. A fresh episode
with every candidate's precondition false is a bounded per-row `TemplateDataError`, not a systemic one.

Proven in `planned_dispatch_internal_test.go`: a fresh dispatch picks the ranked winner unaided (no
human, no Augur); a mode-absent/`shadow` candidates-only gap still hits the pre-Fire-5 config-error path
byte-identically; a reclaim re-dispatches the **pinned** candidate even when the cheaper one would win a
fresh rank (the pre-build-gate's episode-stability risk, closed); a vanished pin alerts and leaves the
mark standing.

Fires 6–9 (goal-regression synthesis + dispatch, diagnostics, admission control, the Augur floor) remain
— see `weaver-planner-mandate-design.md` §8.

---

## Op-effects runtime catalog (Contract #10 §10.8, Fire 6 Increment 1)

Fire 1 declared op-DDL `Effects` and install-validated them, but discarded the parsed guards after
validation — nothing persisted them anywhere a runtime reader could reach, so the registry's own Fire-4/5
comments noted "the catalog this needs first exists at runtime with Fire 6's engine work." This increment
closes that gap; it makes **no** dispatch-decision change (byte-identical to Fire 5).

`pkgmgr.buildInstallBatch` now flattens every DDL's `Effects` map and, for each `OpMetaSpec` whose
`OperationType` carries a non-empty entry, emits a sibling `.effects` aspect on that op-meta vertex
(`vtx.meta.<opId>.effects`, `{"guards": [...]}` — the raw §10.5 guard-grammar predicates verbatim). An op
with no declared Effects emits nothing extra. `validateEffects` now also rejects an Effects operationType
with no matching `OpMetaSpec` in the same package — such an effect would have nowhere to materialize onto
and would silently never reach any catalog (fail-closed, same doctrine as every other install validator).

The Weaver registry (`registry.go`) indexes this aspect independently of the op-meta vertex envelope
(`indexOpEffects`, keyed by vertex id) — the two CDC keys may arrive in either order, so `effectsCatalog()`
joins them by id at read time rather than buffering. `effectsCatalog()` returns one `planner.Action` per
operationType that has both an indexed op-meta vertex and a parsed (non-empty) `.effects` entry,
deterministically sorted by `Ref`: `Cost` is uniformly 1 (no declared per-op cost surface exists yet) and
`Precondition` is left nil — dispatch-time re-validation (mirroring the `proposedOp` precedent, same as
every other action) is what actually gates whether an op may fire; the planner gets no scope the frozen
table didn't have (design §3.3). A malformed `.effects` body is logged and dropped, never surfaced as a
live path (pkgmgr's install-time validation already rejects that shape before it can reach Core KV).

This increment made no dispatch-decision change. Increment 2 (below) resolves the State-schema question
this catalog raised; the actual `planner.Synthesize` dispatch wiring is R1 (see the "Goal-regression
dispatch + per-leg pin/release" section below).

## Goal-regression State-schema bridge (Fire 6 Increment 2)

Increment 1 surfaced a real gap: `rowState` (Fire 4/5) maps a lens row's columns onto **root** guard-grammar
paths (`subject.data.<column>`) — the convention a `pre` guard is authored against — but a declared op
**Effect** (e.g. `SignLease`'s `subject.signature.data.signedAt`) asserts an **aspect** path
(`Path{Aspect:"signature", ...}`). These are disjoint keys in `planner.State`; without a bridge, a goal
authored against the same real-world fact an op's Effects assert would silently never read as satisfied —
worse, it would drive the search to synthesize a **spurious** plan (re-run `SignLease` on an
already-signed application) because the aspect-qualified fact is invisible under the row's untagged root
key.

**Resolution: a lens-column → aspect-path bridge, not a new Core-KV read.** Contract #10 §10.8 already
commits synthesis to being "a pure function of (row, catalog, `__effect` window)" — a live read of the
candidate subject's aspects would violate that framing and widen Weaver's Core-KV footprint, which Andrew
has held as Processor-exclusive (Loom's guard-precondition read is the one tolerated, provisional
exception; effect-probing reads are explicitly not that exception). A real target's own lens already
projects aspect fields onto plain row columns today (`packages/lease-signing/lenses.go:551`:
`app.signature.data.signedAt AS signedAt`) — the row already carries the fact, it just loses the aspect
tag when flattened. So the fix is purely a keying decision:

- A gap's (additive, optional) **`goalColumns`** field maps a row column name to the aspect-qualified
  guard-grammar path it actually represents (`{"signedAt": "subject.signature.data.signedAt"}`).
  **Scoped per-gap, not target-wide** — two gaps in one target may reuse the same column name for
  unrelated facts, and a shared target-level map would silently rebase both onto whichever gap declared
  it. Install-time validation (`parseGoalColumns`, `registry.go`) requires each value to parse under §10.5
  and be aspect-qualified (a root-shaped entry is redundant — a column absent from the map already
  addresses `subject.data.<column>` by default), requires values to be unique (two columns mapping to the
  same path would make `rowState`'s result depend on Go's nondeterministic map-iteration order over the
  row), and requires every declared path to actually appear somewhere in the gap's own `goal` — an entry
  the goal never references is exactly as inert as a typo'd column name, and this is the catchable half of
  that mistake (there's no lens schema here to check the column name itself against). Parsed paths cache
  on the gap as `goalColumnPaths`, mirroring `goalGuard`'s own parse-once pattern. The mirror-image
  mistake — an aspect-shaped `candidates[].pre` — is rejected too: `rankCandidates` evaluates `pre`
  against `rowState(row, nil)` (root-only, no bridge), so an aspect-shaped `pre` could never be satisfied
  and would silently make that candidate permanently ineligible.
- `rowState(row, aspectCols)` (`planner_shadow.go`) takes the parsed map and keys a listed column at its
  real `Path{Aspect, Field}` instead of the default root mapping; an absent-from-the-map column is
  unaffected. Fire 4/5's candidate ranking passes `nil` (unchanged, root-only).

Proven in `goal_state_internal_test.go` against the real lease-signing shape: a row reflecting an
already-signed application, without the bridge, synthesizes a spurious one-step `SignLease` plan (the
aspect fact is invisible under the wrong key); with the bridge, the same row correctly resolves the goal
as already met (zero-step plan). Zero new Weaver Core-KV reads either way.

This increment resolved the schema and proved it in isolation, ahead of R1's actual dispatch wiring (see
below). A package now authors a `goalColumns`/`goal` pair via `pkgmgr.GapActionSpec` directly (the R1
pkgmgr-authoring increment below) — a raw-JSON-installed target is no longer the only way to exercise it.

## Goal gap actions catalog (Fire 6 Increment 3, parse + validate only)

The first-consumer fit (LoftSpace lease-renewal design) revised how a `goal` gap's search catalog is
sourced: **not** Increment 1's global `effectsCatalog()` (every installed op's declared `Effects`,
catalog-wide) but a **per-gap, package-authored `actions` list** — the ratified planner-mandate design's
"installed catalog" framing is revised here (2026-07-05, contract text staged uncommitted awaiting
Andrew) because an op's declared Effect alone carries no **dispatch binding** (no assignee, no params, no
pattern) — a global auto-catalog would synthesize plans it could never actually dispatch. Each `actions`
entry instead couples one dispatch binding (the same action-contract shape `candidates` already uses —
`action`/`pattern`/`subject`/`adapter`/`operation`/`assignee`/`target`/`params`/`reads`) with the
planner-facing triple `Synthesize` needs: a `ref` (unique per gap, used for both dispatch and the
canonical tie-break), an optional `pre`, required `effects`, and an optional `cost` (defaults to 1 — unlike
`candidates`, an unauthored cost here still has to contribute a real weight to a multi-step plan's total).

**Install validation (`validateActionsCatalog`, `registry.go`):** required in both directions — a `goal`
with an empty (or absent) `actions` catalog rejects the target (nothing to plan over), and a non-empty
`actions` with no `goal` rejects too (nothing to plan toward). Every `ref` must be unique within the gap.
Every `pre`/`effects` guard must parse under §10.5 and be **row-reachable** — root-shaped, or aspect-shaped
AND one of this gap's own `goalColumns` bridge values (`requireRowReachable`): unlike `candidates[].pre`
(root-only, no bridge exists), an `actions[].pre`/`.effects` MAY address a bridged aspect path because the
goal gap's `planner.State` already carries it. Every `effects` atom must be a **concrete** assertion —
`planner.ApplyEffects` against an empty `State` is reused (not re-implemented) to reject an `anyOf`/`not`
effect at install rather than let it surface only as a buried `ErrUnsupportedEffect` deep in a future
search. Parsed guards cache on the entry (`preGuard`/`effectGuards`), mirroring `goalGuard`'s parse-once
pattern. Proven in `actions_catalog_internal_test.go`.

This increment was parse + validate only, zero dispatch-decision change (the same posture Fires 1/3/4
shipped at); the dispatch wiring itself is the next section.

## Goal-regression dispatch + per-leg pin/release (Fire 6, R1 engine wiring)

The first fire that dispatches a `goal` gap for real: on a `mode:"planned"` target, a gap with no
explicit `action`, no `candidates`, and a `goal` set now synthesizes and dispatches — every other shape
(explicit `action`, `candidates`-only, non-planned mode) is untouched and byte-identical.

**`Engine.resolveGoalAction` (`strategist.go`)** is `resolvePlannedAction`'s goal branch (the seam its own
Fire-4/5 doc comment already pointed at): a genuinely fresh episode (`pinnedAction == ""`) builds
`planner.State` via `rowState(row, ga.goalColumnPaths)`, builds one `planner.Action` per `actions` entry
(`Cost` defaulting to 1, `Precondition`/`Effects` from the entry's cached `preGuard`/`effectGuards`), and
calls `planner.Synthesize(ga.goalGuard, state, catalog, len(ga.Actions)+2)` — the `maxDepth` R1 fixes at
catalog-size-plus-slack (`loftspace-lease-renewal-goal-authored-target-design.md` §4.3). The winning
plan's `Steps[0]` materializes into a dispatchable `GapAction` (`catalogEntryGapAction`, `candidateGapAction`'s
goal-branch twin) via `buildPlan` exactly like any other action shape. **An in-flight episode
(`pinnedAction != ""`) reuses that exact catalog entry verbatim — no re-rank, no re-plan** — mirroring
Fire 5's pin discipline for the identical idempotency reason (a mid-episode re-derivation could swap the
dispatched action under the same requestId/claimId). `resolvePlannedAction`/`planGap` now return the
resolved **actionRef** alongside the `GapAction`: for every pre-existing shape this is unchanged
(`ga.Action`, or the picked candidate's `Action`); a goal leg's ref is its own catalog **`Ref`** instead,
decoupled from its dispatch contract type — load-bearing because a real catalog (the renewal design's
Target B) has multiple legs sharing one contract type (three `assignTask` legs to different assignees)
that must stay individually pin-matchable and individually credited in the `__effect` window.

**`Engine.releaseCompletedLeg` (`evaluator.go`)** is the leg-boundary counterpart to `clearClosedMarks`'
gap-boundary clearing: it checks whether the currently-pinned leg's declared `effects` all hold against
`rowState(row, ga.goalColumnPaths)` and, if so, revision-conditionally deletes the mark (`markStore.
deleteRevision` — new, mirroring `replace`/`deleteMark`'s existing conflict-skip discipline so a mark a
concurrent path already released/advanced is left alone rather than blindly cleared), resets the gap's
per-chain dispatch-count, and credits the finished leg's `__effect` close. **A release is a leg boundary,
not a gap boundary** — the gap's own `missing_<g>` column may still be `true` (more legs remain), so both
call sites treat a release as "now dispatch a genuinely fresh episode from the advanced state," never as
"done." `dispatchGap` (lane-1) does this in the same call: release, then fall through to `planGap` with
`pinnedAction=""`. The reconciler's `reclaim` does the same explicitly — release, then call `planGap` and
`fireEpisode` (its `inFlight=false` branch) itself — because **the sweep enumerates marks, not rows**: a
release-then-return would leave the gap markless and invisible until an unrelated future row write
happened to touch that entity again, which nothing guarantees (the write that satisfied the leg's effect
may be the last one for a while).

**`ErrNoPlan` → the augur `unplannable` escalation.** A fresh `Synthesize` that exhausts the search, or a
pinned ref the current `actions` catalog no longer names (indistinguishable from a redelivered episode
previously escalated to the augur — its dispatch lives outside the catalog's `Ref` space entirely), both
flag the returned `*planError` `unplannable`. `planGap` retries this through the exact same
`augurEscalation(..., escalateUnplannable, ...)` the pre-existing "no playbook entry" dead-end already
uses (Contract #10 §10.8: "its meaning extends to 'no playbook entry AND no derivable plan'; no new
trigger token") before falling through to the ordinary `TemplateDataError`/`PlaybookConfigError`
disposition — a target with that escalation policy redirects a stuck goal chain to AI reasoning instead of
alerting forever.

Proven in `goal_dispatch_internal_test.go`: a fresh episode synthesizes a multi-leg chain and dispatches
its first leg (unit-level, and end-to-end through `handleRow`); a pinned episode reuses its leg verbatim
even when a fresh rank would prefer a different one (both at the resolver and through a live reclaim); a
completed leg releases and the SAME delivery/sweep pass advances to the next leg under a fresh mark and
claimId; a goal no catalog action can ever reach escalates to the augur reasoning op instead of alerting.

**Deferred as follow-up polish, not a functional gap:** the oscillation detector's ref→declared-effects
bridge for a goal leg (the generic `maxretries_<g>` budget-suppression mechanism already applies to every
gap shape, goal included; a goal-leg-specific Health issue at the suppression site is the remaining polish).

## pkgmgr authoring — `mode`/`goal`/`goalColumns`/`actions` (R1 pkgmgr increment)

The engine-side wiring above landed ahead of any package being able to author it — until this increment, a
goal-mode target could only be installed via raw JSON, bypassing `pkgmgr` entirely (a gap that also spanned
Fires 4/5's `mode`/`candidates` surfaces). `pkgmgr.WeaverTargetSpec` gained `Mode`; `pkgmgr.GapActionSpec`
gained `Goal`/`GoalColumns`/`Actions` (`[]ActionCatalogEntrySpec`, field-for-field mirroring the engine's
`ActionCatalogEntry`) — `Candidates` (Fire 5) authoring remains a separate, not-yet-built pkgmgr surface,
out of scope here. `weaverTargetSpecBody`/`gapActionBody` (`build.go`) emit the new fields verbatim
(`json.RawMessage` values pass through `map[string]any` unchanged, the same pattern `DDLSpec.Effects`
already uses) so the installed body matches exactly what the engine's registry parses.

Install-time validation (`plannerfields.go`) mirrors the engine's `validateTarget` mode check,
`parseGoalColumns`, and `validateActionsCatalog` (including the **row-reachability** rule for `pre`/
`effects` — root-shaped, or aspect-shaped and bridged by this gap's `goalColumns`) so a package that would
fail the engine's own CDC-load validation fails loudly at install instead, same doctrine as `validateEffects`
for op-DDL `Effects`. One adaptation the mirror required: a goal-authored gap legitimately declares no
top-level `action` (dispatch comes entirely from the `actions` catalog via goal regression — the engine's
own `buildPlan` Mode==planned branch falls through to goal resolution exactly when `ga.Action == ""`), so
`validateGapAction`'s action-table check now runs only when an explicit `Action` is authored; a gap with
neither an `Action` nor a `Goal` is rejected instead (nothing tells the engine what to do). The
capability-materializer's AI-authored-target mirror (`capabilitymaterializer.go`) stays a deliberately
restricted subset — it does not expose the goal-authoring surface, same posture as its existing `augur`
exclusion — so its `GapActionArtifact`→`GapActionSpec` conversion switched from a bare type conversion to
an explicit field list (the two types no longer share an identical field sequence).

Proven in `plannerfields_test.go`: the renewal target's actual shape (a root-mapped fact, a
`goalColumns`-bridged aspect fact, the terminal-leg `pre`) installs clean; every install-time rejection
(mode unknown, goal without actions and vice versa, malformed guard, root-shaped/duplicate/unreferenced
`goalColumns`, duplicate/empty `ref`/`action`, negative cost, missing/non-concrete/unreachable
`effects`, unreachable `pre`) is exercised; `weaverTargetSpecBody`'s emitted shape is asserted directly.
Every pre-existing `pkgmgr` weaver-target test still passes unchanged (byte-identical for every
non-goal-authored gap).

---

## Contraction monitor + oscillation detector (Fire 7)

Cross-row / cross-target diagnostics (Contract #10 §10.8 Planner extension design §3.4) — purely
in-memory, heartbeat-surfaced, and **never** alter what dispatches. Both mirror the existing
`shadowStats` pattern (Fire 4): a process restart resets them, and lane-1's `DeliverLastPerSubject`
replay of every current row re-derives true state from scratch.

**Contraction monitor** (`contraction.go`) tracks, per target, the CURRENT count of rows this engine
instance has observed violating — incremental, updated on every lane-1 delivery from the same
"violating or not" cadence mark-clearing already runs at (`handleRow`), never a KV scan. The reconciler
sweep (`sweeper.pass`) appends each registered target's current count to a bounded 5-sample trajectory
ring on its own cadence; the heartbeat classifies the ring as `shrinking` / `steady` / `diverging`
(`metrics.contractionTrajectory`) — a target whose violating-row count never stops climbing is loudly
visible without waiting for `__effect`'s zero-closes signal (Fire 2) to also fire. The already-shipped
`LensEffectMismatch` issue ("dispatches commit but closes never arrive") remains `__effect`'s job; the
trajectory here is a metric, not an alert.

**Oscillation detector** (`oscillation.go`) joins a dispatched `actionRef` to the aspect path(s) its
declared `.effects` concretely assert (`targetSource.effectPathsFor` — present/absent/equals leaves,
walking `allOf`; an `anyOf`/`not` effect names no definite written path and is skipped), at the exact
two fresh-dispatch seams `bumpEffectDispatch` already uses (the CAS-create-won lane-1 path and the
sweep's reclaim — never a redelivery re-fire). Per aspect path, a bounded 8-event ring records the
dispatching `targetID`; once the trailing 4 events show a strict two-target alternation (`A,B,A,B` — one
ordinary back-and-forth is normal cross-owner remediation, only a REPEATING pattern is a fight), both
targets are frozen via the existing `Engine.Disable` `__control` seam and ONE `TargetOscillation` Health
issue names the causal pair + the contested path — freeze-and-alert only, never a new dispatch. The
path's ring is cleared on detection so the same fight reports once, not once per subsequent dispatch.

Proven end to end in `oscillation_internal_test.go`'s fighting-targets fixture: two targets dispatching
different ops that both assert `subject.foo.data.x` are both disabled and one issue names the pair after
the confirmed alternation.

Fire 9 (the Augur floor) remains: `weaver-planner-mandate-design.md` §8.

---

## Admission control (Fire 8)

A dispatch-pacing layer (`admission.go`) between the Strategist's plan resolution and the Actuator's
fire — purely in-memory and process-local, mirroring `shadowStats`/`contractionStats`/`oscillationStats`:
a restart resets every bucket, and the §10.3 mark CAS-create (the actual correctness/anti-storm gate)
is completely untouched — admission control only paces WHICH already-resolved dispatches fire now vs.
on a later redelivery, never whether a dispatch is safe to repeat.

A target's optional `admission` block (`Target.Admission`, install-validated) declares one or both axes:
`globalRate` bounds the target's total dispatch rate; `adapterRates` bounds gaps whose resolved action
declares a matching `GapAction.Adapter` (a field already on the wire since Fire 5's `candidates` but
never consumed until now) — an adapter-specific rate takes precedence over the global one for a gap that
declares it, mirroring the explicit-beats-general `action`/`candidates`/`goal` precedence. **Absent
(every target before this fire) is unbounded — byte-identical dispatch, no row read.**

Each axis is a `tokenBucket`: refills continuously at the declared rate up to a one-second burst
capacity, and — the defining property under contention — is **priority-fair**: an optional bare `priority`
§10.2 row column (like `freshUntil`; absent/non-numeric reads as 0, the lowest tier) orders WHO gets a
scarce token first. Every `admit()` call both submits its own request and cooperatively drains the shared
priority queue (no ticker, matching this file's inline-processing style) — a lower-priority id's own
redelivery must never jump a higher-priority id already waiting; the token is instead *granted* to the
higher-priority id, collected on that id's own next call (no double-consumption). A grant nobody ever
collects (a legitimately closed gap that never redelivers again) is reclaimed and refunded to the bucket
after `admissionGrantTTL`, so a wave of closed episodes cannot slowly starve the budget. The pending queue
is capped (`admissionPendingCap`) as a soft, lossy overflow bound — an evicted id simply re-queues on its
next redelivery; no mark or episode state lives in the scheduler, so eviction is never a correctness
hazard.

Wired at the ONE seam both fresh-dispatch legs share — `planGap`, called by both lane-1's `dispatchGap`
and the reconciler's `reclaim` — right after `resolvePlannedAction` resolves the gap's action (so the
adapter to check is known) and before `buildPlan`: a denial returns `NakWithDelay` with **no mark, no
plan, no Health issue** — ordinary pacing is not a fault, only the `admissionAdmitted`/`admissionDeferred`
heartbeat counters move. Proven end to end in `admission_internal_test.go`'s 3000-row burst fixture
(design table's Fire 8 acceptance: "3k-row fixture paced + priority-ordered"): a declared 50/sec budget
admits its first 50 from free burst capacity, then drains the remaining 2950 in rate-bounded waves that
always serve the highest still-outstanding priority first.

Fire 9 (the Augur floor) remains: `weaver-planner-mandate-design.md` §8.

---

## Control plane (FR30)

Operators manage Weaver's currently-registered convergence targets via a `nats-io/nats.go/micro`
Services responder (`internal/weaver/control`), mirroring Refractor's control plane
(`internal/refractor/control`), plus a `lattice weaver` CLI command group
(`cmd/lattice/weaver/`). `cmd/weaver` starts the listener alongside the engine.

### Subjects

| Subject | Operation |
|---------|-----------|
| `lattice.ctrl.weaver.list` (exact) | `list` — every registered target: `targetId`, `lensRef`, sorted playbook `gaps` columns, and `state` (`active` \| `disabled`) |
| `lattice.ctrl.weaver.<targetId>.disable` | `disable` — pause dispatch for `<targetId>` |
| `lattice.ctrl.weaver.<targetId>.enable` | `enable` — resume dispatch for `<targetId>` |
| `lattice.ctrl.weaver.<targetId>.revoke` | `revoke` — immediate cleanup + disable for `<targetId>` |
| `lattice.ctrl.weaver.<targetId>.resetConfidence` | `resetConfidence` — drain `<targetId>`'s `__effect` confidence windows; returns `windowsDeleted` |

The three mutating verbs form one operator-severity ladder: `disable` (pause, delete nothing) ·
`resetConfidence` (delete advisory confidence only) · `revoke` (delete everything under the target
prefix + disable).

`TargetSummary.state` is a 2-value enum — there is no durable "revoked" state; `revoke` is a
strict superset of `disable` (see below) and reports `"disabled"`.

### Dispatch-skip marker and in-memory cache

Durable truth for the disabled state is the `<targetId>.__control` key in `weaver-state`
(Contract #10 §10.3 bucket; reserved-leading-underscore shape — `__control` can never collide
with a `<targetId>.<entityId>.<gapColumn>` mark, because `entityId`s are NanoIDs and
`substrate.Alphabet` contains no underscore). The reconciler sweep (`internal/weaver/reconciler.go`)
explicitly skips `__control`-suffixed keys — it is not a §10.3 mark and is never enumerated as
`CorruptMark` or deleted by a sweep pass.

The engine maintains an in-memory `disabledTargetSet`, seeded at `Start` by scanning
`weaver-state` for `*.__control` markers (`seedDisabledTargets`) and updated synchronously by
`Disable`/`Enable`/`Revoke` — the same "in-memory cache rebuilt from durable backing" pattern as
the target registry (`targetSource`) and `consumerStateCache`. The hot-path remediation guard
(`handleRow`'s dispatch leg) reads this in-memory set — no per-message KV read.

The disabled state suppresses **only remediation**, not violation-detection bookkeeping. A
disabled target still:

- clears resolved marks (`clearClosedMarks`, run unconditionally before the disabled-skip);
- arms/re-arms freshness timers (`scheduleFreshness`, lane-3 — keeps lane-3 state current so an
  instant re-enable loses no deadline);
- records freshness expiries (`handleFiredTimer` still submits `MarkExpired` for an already-armed
  timer — state-recording, already gated by the read-before-act row-presence/renewed guards).

What it does NOT do while disabled: create a new in-flight mark or run any Strategist/Actuator
remediation (`triggerLoom`/`assignTask`/`directOp`). On `enable`, lane-3/clearing state is
already current and remediation resumes for whatever is still violating — nothing is lost across a
disable→enable window, and no row re-touch is required.

### `disable` / `enable`

`disable <targetId>` writes the `<targetId>.__control` marker (and updates the in-memory set)
**first**, **then** calls `substrate.ConsumerSupervisor.Pause` on the target's lane-1 KV-CDC
durable (`PauseManual` — survives restart via the existing `HealthSink` pause-restore, the same
mechanism the supervised consumers use). `enable <targetId>` calls `Resume` **first**, **then** deletes the
marker and clears the in-memory set, and re-runs `reconcileConsumers` so a consumer removed by a
prior `revoke` is restored immediately rather than waiting for the next registry event. Both
return an error if `targetId` is not currently registered.

**Fail-safe-to-inert ordering.** The `__control` marker is the authority for the remediation-skip;
the `HealthSink` pause-restore is independent and governs only lane-1 pumping. The write order
makes every partial failure / restart window land on "still disabled (inert)", never "acting when
the operator said stop":

- `disable` writes the marker before the pause — a partial failure (marker set, pause failed or the
  process died) is remediation-inert (`handleRow` already skips), which is safe.
- `enable` resumes before deleting the marker — a partial failure (resumed, marker still present) is
  still remediation-inert; the operator re-issues `enable` to heal.

On restart the `__control` marker is authoritative for the remediation-skip (re-seeded into the
in-memory set by `seedDisabledTargets`).

### `revoke`

`revoke <targetId>` is a **strict superset of `disable`**: it (a) removes the target's lane-1
durable entirely (`ConsumerSupervisor.Remove` — durable deleted, mirroring
`reconcileConsumers`'s removal path), drops the engine's last-applied fingerprint
(`e.targets[targetId]`), and deletes the consumer's health-sink entry, (b) deletes every
`weaver-state` key with prefix `<targetId>.` — every in-flight `<targetId>.<entityId>.<gapColumn>`
mark **and** the `<targetId>.__control` marker — and clears the target's standing Health issues,
then (c) **re-writes** the `<targetId>.__control` disabled marker. Step (a)'s `e.targets` drop is
what lets the next `reconcileConsumers` pass re-Add the consumer (it now sees `running==false`);
step (c) means that re-added consumer comes up inert — because the target is still
`meta.weaverTarget`-registered (`revoke` does not unregister it or touch its Lens definition),
dispatch stays inert until an explicit `enable` (which clears the marker and re-runs reconcile).
Unlike `disable`/`enable`, `revoke` on an unregistered/unknown `targetId` is **not** an error —
idempotent, mirroring `ConsumerSupervisor.Remove`'s no-op-if-unmanaged posture, and still writes
the disabled marker so a future registration of that `targetId` starts disabled.

**Uninstall vs. revoke.** A `revoke` keeps the target registered and disabled. A genuine uninstall
(the target leaving `targetSource` — e.g. its Lens is retired) is the `reconcileConsumers` removal
branch, which deletes the consumer, its health-sink entry, **and** the `<targetId>.__control`
marker, and prunes the in-memory set — so a re-install of the same `targetId` does not silently
come up disabled and no orphan marker leaks in `weaver-state`.

**`revoke` is immediate-cleanup, not standing suppression of re-registration** — it does not
prevent the target from being re-installed via a fresh `meta.weaverTarget` vertex, and it does
not retire the target's underlying Lens. Fully decommissioning a target requires also retiring
its Lens (out of this story's scope — an op-path/Refractor concern).

### `resetConfidence`

`resetConfidence <targetId>` deletes every `<targetId>.__effect.<gapColumn>.<actionRef>` confidence
window (Contract #10 §10.3) and **nothing else** — in-flight marks, `…__count` retry budgets, the
`__control` marker, and every other target's windows all survive, and the target's enable/disable
state is untouched. It returns how many windows it removed.

Why it exists: a full `__effect` window with zero observed closes raises a standing
`LensEffectMismatch` on the heartbeat, and the sweep deliberately never content-resets a *live*
(target, gapColumn) pair — so a window polluted by a bookkeeping bug (or by a genuinely stuck
episode re-booked across many sweep passes) has no exit path and the warning stands forever.
`revoke` would clear it but also deletes every live mark and the lane-1 durable, whose
`DeliverLastPerSubject` re-add fires fresh episodes with fresh `claimId`s — minting duplicate
userTasks beside the open ones. `resetConfidence` is the narrow verb between the two.

Each delete is conditioned on the revision read in that pass: a dispatch or close landing
mid-drain wins and survives as honest new history, so the count can under-report and a rerun is
the remedy (rerunning is idempotent — a drained target reports `0`). An unregistered `targetId`
is an error, mirroring `disable`/`enable`; an orphaned window whose target is gone is the sweep's
job, not an operator's.

The windows are advisory-only — `flagEffectMismatches`' heartbeat scan and the planner's
`effectCloseRate`, both of which read a missing key as "no data" rather than a zero close rate. So
after a reset the next heartbeat scan lists nothing for the target and the standing issues clear
through the existing reconciliation loop; windows rebuild honestly from the next genuine episode.
A window that legitimately refills to capacity with zero closes is the alert telling the truth —
the remedy there is on the data side, not another reset.

### Capability authorization

`internal/weaver/control` ships a `StubCapabilityChecker` (allow-all, logs every call) — mirroring
`internal/refractor/control`'s stub posture. Full Capability-KV integration of the control plane
(FR30) is 🔭 Designed (ratified 2026-06-27), build-pending — a shared checker across all three control
planes (Weaver / Refractor / Loom), sequenced behind read-path auth (D1) whose JWT actor-identity seam
it reuses.

---

## What this component owns

| Path | Role |
|------|------|
| `internal/weaver/` | Engine: Sensorium, 3-lane work stream, Evaluator tiers, Strategist dispatcher, Actuator |
| `internal/weaver/control/` | Operator control plane (FR30): `list`/`disable`/`enable`/`revoke`/`resetConfidence` NATS Services responder |
| `cmd/weaver/` | Binary entry point (extractable; shares only `substrate/*`) — starts the control-plane listener alongside the engine |
| `cmd/lattice/weaver/` | `lattice weaver list\|disable\|enable\|revoke\|reset-confidence` CLI command group |

**Package data:** target Lens cypher, playbook definitions, gap→action mappings (`lease-signing`).

---

## In / Out contracts

| Direction | Contract | Notes |
|-----------|----------|-------|
| In | `weaver-targets` `<targetId>.>` KV-CDC durable | lane 1 (primary input — **not** the core-events consumer) |
| In | `events.<domain>.>` per-domain consumer | lane 2 targeted-audit only (Phase 3) |
| In | `schedule.weaver.timer.fired.>` on `core-schedules` | lane 3 (ADR-51 scheduled messages; fixed durable `weaver-temporal`) |
| Out | ops via `core-operations` (`ops.<lane>`) | fire-and-forget; OCC `expectedRevision` payload; trigger-Loom |
| Out | `@at` schedules via `core-schedules` (`schedule.weaver.timer.<targetId>.<entityId>`) | lane 3 scheduling leg; replace-on-reschedule (one schedule per subject) |
| Out (own) | `weaver-state` bucket | in-flight convergence marks (anti-storm); per-key TTL backstop (2× lease) + reconciler sweep |
| In/Out | `micro.Service` endpoints at `lattice.ctrl.weaver.<targetId>.<op>` and `lattice.ctrl.weaver.list` | Control plane (FR30): operator `list`/`disable`/`enable`/`revoke`/`resetConfidence` — see "Control plane" below |

---

## Failure modes

| Mode | Behavior |
|------|----------|
| Re-trigger storm | violation persists until gap closes *and* re-projects → the `weaver-state` in-flight mark suppresses re-trigger |
| **Actuator crash mid-flight** | in-flight marks carry a **TTL/lease**; the reconciler sweep reclaims expired leases so a target is never wedged. *(Tested: `TestWeaverE2E_MidFlightKill` kills the episode between CAS-create and publish and proves the re-attempt.)* |
| External call retried/failed | not a Weaver concern — `triggerLoom` of an `externalTask` hands the call to the bridge, which de-dups on the `idempotencyKey` so a retry produces at most one external effect (FR58; see `docs/components/bridge.md`) |
| Target too costly to keep live | lane-2 on-demand evaluation (deferred-exercise) |

---

## Principles that apply

- **P2** — Processor is the sole Core KV writer; Weaver is a client. Claims/state are
  operational KV, not Core KV (P1).
- **P4** — Weaver enforces declarative convergence invariants; Starlark enforces single-op
  invariants only.
- **Weaver targets ARE Lenses** — Weaver consumes the Refractor; it is not a cypher runtime.
- **Module boundary** — `weaver` imports only `substrate/*`; triggers Loom via NATS/op.

## Implementation status

What ships today in `internal/weaver` + `cmd/weaver`, and what is deliberately deferred:

| Surface | Status |
|---------|--------|
| **Lane 1 (violation-driven)** | ✅ Shipped. One **supervised KV-CDC durable per target** on the `KV_weaver-targets` backing stream (`$KV.weaver-targets.<targetId>.>`, `DeliverLastPerSubject`) via `substrate.ConsumerSupervisor` — never a raw `kv.Watch`. Desired-vs-running reconcile over the `meta.weaverTarget` registry: removal deletes the JetStream durable; a spec change Resets it. |
| **Target registry** | ✅ Shipped. `meta.weaverTarget` CDC source (Core KV `vtx.meta.>`), §10.8 install-time validations (`missing_*` gaps keys, `targetId` uniqueness + dot-free), reject-and-alert (Health KV issue), never silent. |
| **Dispatch OCC (§10.3)** | ✅ Shipped in the full frozen shape: the `weaver-state` mark (`<targetId>.<entityId>.<gapColumn>`) is a **CAS-create** carrying `claimedAt`/`leaseExpiresAt`/`heldBy` and a **NATS per-key TTL at 2× the lease** (the backstop — a dead reconciler can never wedge a gap forever); mark-clearing is **level-reconciled on each watch update AND each reconciler sweep**. The mark carries the frozen `claimId` field (`omitempty`) but it is always empty: every action dispatches through a single fire-and-forget `ops.<lane>` submit, so no id is ever minted onto the mark. |
| **Reconciler sweep** | ✅ Shipped (`internal/weaver/reconciler.go`; cadence: `internal/weaver/sweep_schedule.go`): one pass per fired occurrence of a **durable `@every` schedule** on `core-schedules` (`schedule.weaver.sweep` → `schedule.weaver.sweep.fired`, the fixed `weaver-sweep` durable, `MaxAckPending: 1`; armed idempotently on every start via `substrate.ScheduleEvery`, 1s floor, plus a warm pass at start — the cadence survives restart, is operator-visible in the stream, and fires once across replicas; default 1m, clamped ≤ the lease so expiry is always observed before the TTL backstop; lease default 30m, both `Config`-tunable) over every mark — prompt level-clearing of closed gaps, orphan reclaim (target removed, column dropped from row + playbook), corrupt-mark delete+alert (the issue retires once the key stays gone), and **expired-lease reclaim as a fresh episode**: a revision-conditioned **in-place replace** of the mark (fresh lease, re-armed TTL → new revision → new `requestId`), so the key is never absent across a reclaim and only **violating** rows re-dispatch (mirroring lane-1's L1 gate). A re-fired reclaim cannot duplicate the userTask artifact: the mark's per-open-episode `claimId` is preserved across every reclaim (§10.3), so `triggerLoom`/`assignTask` re-dispatch the **same** Loom instance / task and the consumer collapses the repeat. Because that repeat is therefore pure waste (a no-op commit + a fresh Contract #4 tracker every sweep) for an episode held open for days, repeat reclaims of those two **collapse-only** userTask actions are **paced by an exponential backoff** keyed on the mark's own `claimedAt` + dispatch-count (`backoffInterval`: first reclaim at lease-expiry, then ≈ lease → 2× → 4× …, capped at the 24h tracker horizon) — surfaced via the `sweepReclaimsSuppressed` heartbeat metric. `directOp`/external reclaim re-dispatch is the **intended bounded retry** (governed by `inflight_<g>`/`maxretries_<g>`) and is **never** backed off. All sweep deletes are revision-conditioned; both orphan legs are gated for a warm-up window after start (`SweepOrphanWarmup`, default 5m — a registry-replay-readiness proxy). |
| **Actuator** | ✅ Shipped as **fire-and-forget publish** to `ops.<lane>` with a deterministic per-dispatch-episode `requestId` (derived from the mark's current revision — its CAS-create, or the sweep's reclaim replace; Contract #4 collapses re-fires). **No request-reply, no command outbox** — Weaver has no cursor advance to dual-write; recovery is the mark + level-reconcile + lease: a rejected/lost op leaves the mark in place and the sweep re-attempts it at lease expiry. The op payload carries the row's `expectedRevision` (the OCC revision-condition); `triggerLoom` resolves the live `meta.loomPattern` vertex for `authContext.target` (pattern-as-target). |
| **Actions** | `triggerLoom` (→ `StartLoomPattern` op, never a Go call), `assignTask` (→ `CreateTask` with episode-deterministic `taskId`), `directOp` ✅. External idempotent I/O is `triggerLoom` of a Loom `externalTask` pattern — the **bridge** executes the call (`docs/components/bridge.md`); Weaver never holds an adapter. `proposedOp` ✅ (the Augur's Fire 2b "Augur dispatch") sources its op + params from the ROW — not playbook config — for the primordial `augurDispatch` target only: it materialises an approved proposal's `proposedAction`/`proposedParams` after a dispatch-time re-validation (action vocabulary + default-deny scope to the TRUSTED candidate + live-registry resolution), fires it under a **proposal-scoped deterministic `requestId`** (collapse-only under a sweep reclaim, unlike every other action's episode-scoped id), then submits `RecordProposalDispatch` as a same-dispatch follow-up (a failed follow-up publish self-heals on the next sweep, never Naks the episode). `surface` ✅ (FR29 — `orchestration-base`'s `unroutedTasks` target) dispatches **no op and creates no mark**: while the gap column stays true it raises a named `IssueCode`/`IssueSeverity` Health-KV issue (default severity `warning`); the issue clears via the ordinary level-reconciled mark-clearing pass once the row stops naming the column (`clearClosedMarks` special-cases it — a surface gap never had a mark to clean up). Manual-intervention-only; unlike every other action it never touches `ops.<lane>`. An action the playbook names outside this set fails closed (a loud `PlaybookConfigError` Health issue), never a silent skip. |
| **Health** | ✅ Contract #5 heartbeat at `health.weaver.<instance>` (metrics: `consumers`, `targets`, `marksInFlight`, `sweepReclaims`, `sweepOrphansDeleted`, `sweepCorrupt`, `sweepLastRunAt`, `timersScheduled`, `timersFired`) + per-consumer pause-state docs at `health.weaver.consumer-state.<name>` (consumer-scoped, not instance-scoped — survives a restart under a new instance); config/data errors surface as issues. |
| **Lane 3 (temporal)** | ✅ Shipped (Contract #10 §10.4). One **fixed supervised durable** `weaver-temporal` on `core-schedules` filtered `schedule.weaver.timer.fired.>`; the lane-1 row handler's **scheduling leg** re-arms `@at(freshUntil)` per delivery (level-driven, replace-on-reschedule); the fired→op conversion submits `MarkExpired` under the **deterministic timer `requestId`** (schedule subject + fire instant) with **no weaver-state mark**. See "Temporal lane" above for the convention column and the accepted Phase 2 bounds. |
| **Control API/CLI (Pause/Resume surface)** | ✅ Shipped (FR30). `internal/weaver/control` exposes `list`/`disable`/`enable`/`revoke`/`resetConfidence` over a `nats-io/nats.go/micro` Services responder; `lattice weaver` CLI group. See "Control plane" above. |
| **Lane 2 (event-targeted-audit) + `weaver-work`** | ⏳ Phase 3 (§10.3: no durable bucket today). |
| **Real target Lens via Refractor + playbook package data** | ✅ Shipped — the `lease-signing` reference vertical provides a real convergence target + §10.8 playbook; the engine also runs against test-written §10.2 fixture rows. |
| **Planner mandate (dispatcher → solver)** | 🏗️ Building (Contract #10 §10.8 "Planner extension", ratified 2026-07-04). Fire 1 ✅: op-DDL `Effects` (`internal/pkgmgr` `DDLSpec.Effects`, §10.5 guard-grammar predicates a commit entails, parsed by the new standalone `internal/guardgrammar` package) + install-time validation (`validateEffects`); the `lease-signing` package declares `SignLease`→`.signature present` and `RecordLeaseServiceOutcome`→`.outcome present`. Fire 2 ✅: the `__effect` confidence window (see above). Fire 3 ✅: the pure `internal/weaver/planner` goal-regression library (see above) — table-tested, catalog-permutation-stable. Fire 4 ✅: `mode`/`candidates`/`goal` install-validated parsing + the shadow-compare diagnostic (see above) — zero dispatch-decision change; the Strategist's real dispatch reads only `ga.Action`. Fire 5 ✅: `mode:"planned"` candidate selection actually dispatches (see above) — the first fire that changes a real decision; mark-pinned across reclaim, byte-identical for every other mode/explicit-action gap. Fire 6 Increments 1–3 ✅: the runtime op-effects catalog, the goal-regression State-schema (row↔aspect) bridge, and the per-gap `actions` planning-catalog parse/install-validation (see above) — all zero dispatch-decision change. Fire 6 R1 ✅: `resolveGoalAction`'s dispatch wiring — `planner.Synthesize` over a gap's `actions` catalog, per-leg dispatch + pin, and `releaseCompletedLeg`'s effects-hold leg-advance (see "Goal-regression dispatch + per-leg pin/release" above) — the first fire a `goal` gap actually dispatches. Fire 6 R1 pkgmgr ✅: `pkgmgr` authoring fields for `mode`/`goal`/`goalColumns`/`actions` (see "pkgmgr authoring" above) — a package now authors a goal-mode target directly; `Candidates` (Fire 5) authoring remains a separate, unbuilt pkgmgr surface. Fire 7 ✅: the contraction monitor + oscillation detector (see above) — heartbeat-surfaced diagnostics only, zero dispatch-decision change (a goal leg's own ref→effects oscillation bridge is unwired follow-up polish). Fire 8 ✅: admission control (dispatch-pacing token bucket + §10.2 `priority` column). Fire 9 (Augur floor) remains: `_bmad-output/implementation-artifacts/weaver-planner-mandate-design.md` §8. |

---

## Deferred (Phase 3+)

- Refractor negative/filter-retraction projection (true emit-only-when-violating).
- Lane-2 on-demand evaluation (built, unexercised in demo).
- The Augur's autonomy boundary (`augur.autoApply` — a proposal skipping the human gate) — parsed + validated, not enabled; Andrew-gated (Fire 3).
- Real external adapters (Stripe/background-check) — Phase 3 integration.
