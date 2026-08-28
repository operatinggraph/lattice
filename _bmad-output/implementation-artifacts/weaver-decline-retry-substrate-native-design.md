# Weaver — substrate-native decline retry: Nak'd declines standing, replay as an operator verb

> **✅ RATIFIED — Andrew, 2026-08-27.** Build-ready; the Lattice Steward builds §14's four
> increments in order.
>
> **What was ratified.** The **standing** mechanism is the Nak loop alone — a declined violating
> row of a **config-error** class is Nak'd on a long (5 m) redelivery floor instead of Acked, so
> it re-evaluates against current config until fixed; **data-error** classes Ack with a standing
> level Health issue, because their fix necessarily arrives as a re-projection that supersedes
> any pending state anyway (§3.2's fix-path rule). There is **no automatic durable rebuild
> anywhere**: the replay — delete-then-create of one target's lane-1 durable so
> `DeliverLastPerSubject` re-delivers its current row set — exists only as the **manual
> `ReplayTarget` operator verb** (joining Disable/Enable/Revoke/ResetRetryBudget in `control.go`,
> surfaced in Loupe), used once after ship to heal the pre-existing Acked-decline population and
> thereafter as operator repair. `Enable` stays plain Resume. The `NumDelivered` re-fire branch
> retires, with its one legitimate job preserved by the republish set (§3.4).
>
> **The two open questions, resolved by this ratification** (no separate answer was given; the
> design's own recommendation stands in each case — flagged here so the builder is not guessing):
>
> 1. **§8 severity — demote.** `GapWithoutPlaybook` and `PlaybookConfigError` become `warning` at
>    their raise sites, so a package-authoring typo degrades Weaver rather than pinning it
>    `unhealthy`. Ships in Inc 2.
> 2. **The update-rebuild lever — stays dropped.** No target-update-triggered rebuild; the Nak
>    loop's automatic fix-uptake plus the verb cover it.
>
> **Provenance.** Directed by Andrew at the row-sweep hold (2026-08-27), corrected the same day on
> replay scope (*"a manual, Loupe solution but not as the standing, new per-target durable on
> every boot"*), and again on decline taxonomy (data vs. transient/config) and the `Term`
> alternative — both folded into §3.2. Phase 0's keystone (Nak'd pending state vs. per-subject
> compaction) is verified in the pinned `nats-server` 2.14.0 source (§2 V3). Two adversarial
> passes ran and are folded (§15). No architectural fork; **no frozen-contract change** —
> Contract #10 §10.8's liveness bullet already promises what this implements (§10). One deviation
> from the hold-direction's letter besides the correction, argued in §4: the unregistered-target
> exit stays Ack.
>
> **Build gate:** Inc 2's Phase 0 runs censuses C1/C2/C5 (§12) and **stops** if C2's stranded
> class lands in a row §3.2 leaves at Ack. **Revive trigger for the shelved
> [row-sweep fallback](weaver-sweep-declared-work-enumeration-design.md):** §2 V7's KV
> history-1 pin failing (T4 is its gate).

**Author:** Winston (Designer fire, 2026-08-27; replay scope corrected by Andrew live, same day).
**Size: M.**
**Board row:** `[Weaver] A declined violating row is Acked once and never revisited` — ★★★
(`_bmad-output/planning-artifacts/backlog/lattice.md`).
**Supersedes the shape of:** [weaver-sweep-declared-work-enumeration-design.md](weaver-sweep-declared-work-enumeration-design.md)
(HELD 2026-08-27 — its §1 problem statement, §2 grounding ledger and §3 leg-coverage table are
incorporated by reference; this doc does not restate them).
**Blocks:** `verticals.md` — *29 of 63 appointments carry no site and `clinicSiteBackfill` closes
the gap for none*.

---

## 1. The direction, quoted and answered clause by clause

From the held doc's banner, plus Andrew's live correction, with where each clause lands:

1. *"`handleRow`'s transient/data-error decline exits return `NakWithDelay` (per-class backoffs,
   `MaxDeliver` unbounded — the substrate omits the bound at ≤0) instead of Ack."*
   → §3.1–§3.2. Delivered with one refinement: **two configured delay floors** (the existing 5 s
   transient floor and a new long decline floor), because the substrate deliberately carries the
   delay on the consumer's config, never on the Decision (§2 V8). `MaxDeliver` unbounded is
   already the substrate's unconditional posture (§2 V5). Two exits the clause literally covers
   stay Ack, argued in §3.2 rows 5/2 and §4.
2. *"Lane-1 durables adopt the registry's own per-boot-nonce replay …"* — **superseded by
   Andrew's correction** (quoted in the banner): a durable rebuild is acceptable only as a
   **manual, Loupe-invoked** operation, never a standing per-boot mechanism. → §3.3: the
   `ReplayTarget` operator verb. What each formerly-automatic trigger becomes — and why the Nak
   loop makes most of them unnecessary — is enumerated there. The false
   `contraction.go:22-25` restart claim is **rewritten to the honest semantics** (not made true
   by machinery): counts rebuild from stuck-row redeliveries and fresh projections; a full
   rebuild is what the verb provides.
3. *"`dispatchGap`'s `NumDelivered > 1` blanket re-fire branch retires — redelivery becomes
   routine, so lease-gated `reclaim` becomes the sole re-fire authority."* → §3.4. Retired —
   together with the one legitimate job the adversarial pass proved it was doing (making `fire`'s
   publish-failure Nak effective), which moves to a small in-memory republish set. The anti-storm
   check also moves **ahead of `planGap`**, so a marked row no longer burns an admission token
   per redelivery.
4. *"The `gapConfig:` latch stays per-entity-cleared; an over-eager clear self-heals within one
   backoff period because a still-open row's Nak loop re-raises it."* → §3.5. Held to, with the
   clear narrowed to an explicit `false` read and the re-raise conditionality of one of the three
   codes stated.

**Problem, symptoms, and today's leg coverage:** the held design's §1–§3, unchanged and
re-verified. One root: a lane-1 Ack is final, the sweep only iterates dispatch residue, so a quiet
violating row gets exactly one evaluation ever. Three filed symptoms: the clinic 26, the
`gapConfig:` latch that can only be retired per-entity, and the contraction monitor reading ~0
after every warm restart.

---

## 2. Grounding ledger (the redesign's own rows)

The held design's §2 ledger (26 rows) is incorporated; rows below are new. Pinned to the code that
does the thing. NATS paths are in `nats-server@v2.14.0/server/`.

| # | Fact | Citation |
|---|---|---|
| V1 | A delayed Nak **keeps the message in `o.pending`** — `p.Timestamp = now − AckWait + d`, persisted via `updateDelivered`, redelivery at ~now+d **independent of AckWait**; `trackPending` updates the entry in place, so the MaxAckPending slot is held continuously until acked or Term'd | `consumer.go:3122-3188` (`processNak`), `:5744-5756`, `:5937`, `:5964-5967` |
| V2 | `getNextMsg` serves **redeliveries before the MaxAckPending stall check** — a declined backlog keeps cycling; only NEW deliveries stall at the cap. The corollary is head-of-line: each floor tick expires the stuck set as a batch, and the single serial worker chews it before new rows (§7 prices it) | `consumer.go:4753-4795` precede the `o.maxp` check at `:4796-4801`; `checkPending` batches at `:5970-5973` |
| V3 | **Phase-0 keystone.** A pending message **removed from the stream** (per-subject compaction on a KV overwrite) is `processTerm`'d — **eagerly**: `fileStore.removeMsgViaLimits` → the store callback → `storeUpdates`' `md == -1` branch → `decStreamPending` → `go o.processTerm(...)` for a pending seq; the redelivery-attempt path (`ErrStoreMsgNotFound`/`errDeletedMsg` → `processTermLocked`) is the backstop. The overwriting revision is a fresh message and delivers normally | `filestore.go:4927-4932`, `:5903-5908`; `stream.go:5161-5172`; `consumer.go:6768-6791`; backstop `:4779-4792` |
| V4 | Every explicit-ack consumer that does not set MaxAckPending gets the server default **1000**; the supervisor sets it only when `spec.MaxAckPending > 0`. Lane-1 runs at 1000 today. No stream/account/server limit clamps a higher value in this deployment | `consumer.go:576`, `:668-677`, `:834-842`; `substrate/consumer_supervisor.go:608-610`; `bootstrap/primordial.go:111-124`; `deploy/nats-server.conf` |
| V5 | The supervisor **never sets MaxDeliver** — retry count unbounded by deliberate posture, with a pinning test; the server defaults it to −1 and the redelivery cap is inert at 0 | `substrate/consumer_supervisor.go:24-25`, `:587`; `consumer_supervisor_test.go:68-105`; `consumer.go:585-591`, `:4756` |
| V6 | `DeliverPolicy` is **non-updatable** (*"deliver policy can not be updated"*) and an update resumes from the persisted ack floor — delete-then-create is the only replay route. `DeliverLastPerSubject` **requires** a filter subject and honors the prefix filter; at `MaxMsgsPer == 1` the server skips the skip-list and scans from `FirstSeq` (a filtered full scan — same delivered set) | `consumer.go:2435`, `:919-921`, `:6171-6190`; `weaver/registry.go:26-34` |
| V7 | `weaver-targets` keeps KV **history 1** — per-subject compaction on every overwrite is the live semantics. **Caveat: V3 and the verb's replay shape both depend on this pin.** At history ≥2 an overwrite no longer trips `removeMsgViaLimits`, a Nak'd stale revision keeps redelivering beside the fresh one, and §9's "only the latest revision can redeliver" fails — this design's revive-the-fallback trigger. The pin today is an implicit default plus a bootstrap-package test; this design makes it explicit and Weaver-owned (§13 T4) | `bootstrap/primordial.go:111-116` (implicit), `weaver_targets_bucket_test.go:57-59`; V3's chain |
| V8 | The Decision's delay is **configuration, not data**; `applyDecision` has **four** call sites and there are **two** switches over `Decision` in the repo, both with `default: → Ack` — a missed site on a new Decision value is a **silent no-op, not a compile error** (no `exhaustive` linter). The iota layout is pinned by a test | `substrate/consumer.go:50-56`, `:448-471`, `:319`, `:395`; `consumer_supervisor_pump.go:704`, `:710`; `processor/commit_path.go:850-869`; `substrate/nak_with_delay_test.go:13-26` |
| V9 | `Disable` = `supervisor.Pause` (pump paused before opening the iterator; rows neither delivered nor acked — the already-Nak'd set holds its slots for the freeze regardless of any exit's Decision); `Enable` = `Resume` + marker clear + `reconcileConsumers`. Resume resumes from the ack floor — **acked** rows never redeliver, but **Nak'd-pending** rows do, on their own timestamps | `weaver/control.go:138-177`; `substrate/consumer_supervisor.go:419-433`; `consumer_supervisor_pump.go:501`, `:552-560`, `:626-642` |
| V10 | The registry fires `updateCB` on every re-delivery of a registered target's meta vertex (no source-side diff), but at **boot** every vertex's first delivery takes `loadCB`, so per-boot registry replay causes no spurious update events. The engine's consumer-Reset branch is unreachable today (name-derived fingerprint) | `weaver/registry.go:608-625`, `:629-643`; `engine.go:385-386`, `:427-448` |
| V11 | Supervisor machinery: `Add/Remove/Reset/ResetAwaitReopen`; `Reset` = `recreateDurable` (a `js.DeleteConsumer` + create) + pump reopen, **managed consumers only** (`"reset %q: not managed"`); per-consumer `resetMu` serializes the delete-create pair. A consumer deleted under a live pump does not kill it — the pump reopens on capped backoff | `substrate/consumer_supervisor.go:98`, `:167`, `:211-231`, `:273`, `:333-357`; `consumer_supervisor_pump.go:507-536`, `:645-649` |
| V12 | `handleRow`/`dispatchGap`/`planGap` decline exits and today's Decisions: malformed key `:27` Ack; unregistered target `:34` Ack; unparseable body `:43` Ack (log only); tombstone `:93` Ack; disabled `:115` Ack; violating-false `:120` Ack; missing entityKey `:131` Ack (level-driven warning); `GapWithoutPlaybook` `:231` error alert + Ack; seq-0 defer `:273` NakWithDelay; `UnresolvedReference` `:579` paced warning + NakWithDelay; `TemplateDataError` `:583` paced warning + **Ack**; `PlaybookConfigError` `:587` paced error + Ack. The full `substrate.Ack` census is **21 sites** (§12 C6) | `weaver/evaluator.go` at the cited lines |
| V13 | `clearClosedMarks` runs in the preamble on **every** delivery; per **closed** (non-surface) candidate column it pays `marks.get` + a mark delete + `deleteDispatchCount` — and both deletes are **unconditional KV publishes** (DEL markers even when nothing was there). Open columns cost zero | `evaluator.go:56`, `:869-919`; `state.go:225-232`, `:445-452` |
| V14 | `issues.set` preserves an existing `since`; a clear-then-set mints a fresh one — but `alertPaced`'s pace memory **survives `clear`**, so the paced codes keep their original onset across flaps. The heartbeat **document** is already bounded severity-first with an overflow entry (`boundIssues`, cap 50) over a status computed on the full set; what is unbounded is the in-memory `issues` **map** and `snapshot()`'s per-heartbeat sort | `health.go:155-165`, `:196-200`, `:253`, `:302-315`, `:35`, `:485-501`, `:616-682` |
| V15 | `escalateExhaustedGap` raises its standing issue and returns Ack on the raise path (no pending slot held) — but its stale-mark arm **clears the stale mark and fires a fresh Augur reasoning episode** (a paid model call), and `fireEpisode`'s stale-mark arm re-publishes an episode: a replay re-fires these for every violating row whose mark has aged past its lease (§3.3 prices and bounds it for the verb) | `evaluator.go:1325-1398`, `:1362-1385`, `:643-700` |
| V16 | ⚠️ **FALSIFIED 2026-08-28 — see V16a.** **A NATS restart strands a quiet Nak'd population.** The redelivery timer (`o.ptmr`) is not persisted; on consumer recovery `setLeader → checkPending` bails at `o.ptmr == nil`, and the only arming sites are `trackPending` (an actual delivery — **one delivery re-arms the timer for the whole pending set**) and `processNak`. `o.rdq` is not persisted either. A lane-1 consumer whose pending set is entirely long-Nak'd rows and which receives no new delivery is never redelivered again until some row under its prefix projects, or the durable is recreated | `consumer.go:5895-5898` (the bail), `:1777`, `:5771-5772`, `:3178`, `:7034` |
| V16a | **V16 IS FALSE for this deployment (build-time verification, 2026-08-28).** V16's enumeration of the `o.ptmr` arming sites missed a third: `applyState` re-arms it on startup whenever restored pending is non-empty — *"Setup tracking timer if we have restored pending"*, `if o.isLeader() && len(o.pending) > 0 { o.resetPtmr(delay) }` at a 500 ms–1.5 s delay. It is reached on every start via `setLeader(true)` → `readStoredState()` → `applyState`, and `o.leader.Swap` runs *before* that call so `isLeader()` is already true; for a standalone (R1) server — every shipped deployment — `setLeader(true)` is unconditional at consumer construction. Nak'd timestamps ARE persisted (`processNak` → `updateDelivered`), so `checkPending` re-arms and expires each entry on its own backdated deadline. **Consequences:** there is no restart strand, §3.3's justification #2 is void, §5's Nak'd-pending row loses its restart caveat, and **T10 as written asserts a behaviour the server does not have** — it must be rewritten to assert the OPPOSITE (a quiet long-Nak'd row DOES redeliver after a server restart, unaided) before Inc 4 is built | `nats-server@v2.14.0/server/consumer.go:3297-3308` (`applyState`), `:1678`, `:1607`, `:1434-1435`, `:3175-3177` |
| V17 | The supervisor pump prefetches **one message** (`pumpPullMaxMessages = 1`, with the buffered-AckWait concern stated as the reason) and heartbeats `InProgress()` every AckWait/2 for the whole handler run — so neither a replay burst nor a slow handler can age a delivered row out of AckWait | `consumer_supervisor_pump.go:28-58`, `:669`, `:719-770` |
| V18 | `fire`'s publish failure returns **Nak** — *"the mark already exists, so the redelivery re-derives the SAME requestId and re-publishes"* — i.e. the `NumDelivered != 1` re-fire branch is the **only** thing that makes that guaranteed immediate redelivery do work; `reclaim`'s own comment also leans on it. Retiring the branch without replacing this leg swallows every publish failure until reclaim, whose repeat backoff ladder is `base × 2^(count−1)` capped at 24 h | `evaluator.go:776-778`, `:795-800`, `:190-193`; `reconciler.go:993`, `:1126-1128`, `:30`; `engine.go:185-186` |
| V19 | `boolColumn` raises `RowDataError` only for a **present, non-bool** value and returns the zero bool; `scheduleFreshness` raises `RowDataError` at three sites and deliberately **returns true (→ Ack)**, with the anti-Nak reasoning in its own comment; `intColumn` raises for `maxretries_<g>`/priority | `evaluator.go:1085-1101`, `:1117-1145`; `temporal.go:120`, `:137`, `:154-158` |
| V20 | The operator-verb family the replay verb joins — Disable/Enable/Revoke/ResetConfidence/ResetRetryBudget — lives in `control.go` with a shipped transport + capability-verb pattern (the exhausted-gap un-park fire, `057286f`). The engine already creates and deletes durables on `KV_weaver-targets` (held ledger row 7), so the verb adds no new natsperm surface beyond its own operator capability | `weaver/control.go:87-460`; Done-log `057286f` |

---

## 3. The shape

The standing mechanism is the Nak loop; the replay is a verb. No new durable state, no new key
family, no walk, no automatic rebuild.

### 3.1 Two configured delay floors, selected per decline class

The substrate gains one Decision value and one tunable, honoring V8:

- `substrate.NakWithLongDelay` — identical semantics to `NakWithDelay`, applied with the consumer's
  `LongRedeliveryDelay` floor. **Appended after `NakWithDelay` in the iota** (the layout is
  test-pinned, V8).
- `ConsumerSpec.LongRedeliveryDelay` / `DurableConsumerConfig.LongRedeliveryDelay`, default
  `DefaultLongRedeliveryDelay = 5 * time.Minute` (floored at `DefaultRedeliveryDelay` if set
  lower); env-clamped like the sweep intervals (`engine.go:157-184`).

**Complete touch-point list (V8 — a missed site silently Acks):** the Decision enum +
`applyDecision` and its **four** call sites (`consumer.go:319`, `:395`;
`consumer_supervisor_pump.go:704`, `:710` — the signature grows a second floor), the two config
structs, `processor/commit_path.go:850`'s `disposeJetstream` switch (an explicit case), the iota
pin test, and `handleRow`'s two per-gap aggregation switches (§3.2). T1 asserts the new value
routes to `NakWithDelay(long)` and **not** to a `default:` Ack, at every switch.

Why 5 minutes: the long floor is pure re-poll cadence — fix uptake rides it or beats it (§3.3),
and what it prices is the `gapConfig:` re-raise bound (§3.5) and the steady-state cost of a stuck
population (§7). **Retry count is deliberately indefinite** (V5's unbounded-MaxDeliver posture):
a Nak'd row retries until the handler Acks, the message is superseded (V3), or the durable is
deleted — the bounds are the floor's cadence, the `maxretries` budget on *dispatch* attempts, and
fix-uptake ending the loop; a give-up cap would be the silent park §10.8 forbids, and the
standing Health issue is the escalation while the loop runs. Two honest edges: for a lens that keeps **re-projecting** a still-broken row,
the loop rides the projection rate, not the floor (V3 is the floor's escape hatch — bounded by a
projection cost the platform already pays); and the floor is a *redelivery* floor, so the whole
stuck set expires as a batch each period and is drained serially (V2, §7).

### 3.2 The decline-class table

**The classifying rule (Andrew's push-back, folded): the class follows WHERE THE FIX CAN COME
FROM.** The substrate offers no "acked-but-declined" state — Ack, Nak(+delay), Term, InProgress
are the whole menu, and the pending set is the only substrate-level "owed" tracking — so the
taxonomy is three-way:

- **Transient** → `NakWithDelay` (5 s): the retry itself fixes it.
- **Config error** → `NakWithLongDelay` (5 m): the fix arrives via a registry/target/template
  change that produces **no new row delivery** — the Nak loop is the only automatic uptake path.
  This is the only class the long floor exists for.
- **Data error** → **Ack + a standing level `RowDataError` issue**: every fix necessarily
  arrives as a **re-projection**, and V3 guarantees the fresh revision supersedes any pending
  state eagerly — so a Nak here buys no retry value, only restart-surviving audibility, at a
  pending-slot and per-cycle KV cost. This is also the codebase's own argued posture
  (`temporal.go:154-156`, V19), and the same footing as exhausted rows: the standing issue IS
  the invariant's *escalated* branch. Residual, named: the in-memory issue is lost at a Weaver
  restart and not re-derived for a quiet row until re-projection or `ReplayTarget` — unchanged
  from today. **`Term` considered and rejected for this class** (Andrew's ratification
  question): delivery-wise it is identical to Ack (per-message; the fresh revision delivers
  either way), its only delta is a best-effort terminated *advisory* — lost unless new capture
  infrastructure subscribes, and unable to express the fix's *clear* — and its substrate
  meaning ("poison, event-loss-accepting") mislabels a row whose next revision is the retry.
  The Health issue is the honest "acked but declined" ledger.

The per-row table, outcome decided per row. "Long" = `NakWithLongDelay`. Scope: `handleRow`,
`dispatchGap`, `planGap`, `escalateExhaustedGap`, `fireEpisode` — the full 21-site Ack census is
C6 (§12); sites not listed here are success paths or anti-storm/CAS-lost drops, enumerated there.

| # | Exit / class | Today | New | Why |
|---|---|---|---|---|
| 1 | Malformed row key (`:27`) | Ack | **Ack** | Not data — redelivery can never fix a key |
| 2 | Target not registered (`:34`) | Ack | **Ack** (unchanged — §4.2) | The reachable cases are teardown/rename/rejected-target; a Long here holds pending forever for a target that will never register |
| 3 | Body does not parse (`:43`) | Ack, log only | **Ack + raise `RowDataError`** at `issueKeyDataEntity(target, entity, "body")`, **cleared on the next successful parse** of the same row (immediately after `json.Unmarshal` succeeds — nothing else can clear a synthetic column) | Data error — only a re-projection can fix it, and it will deliver (V3). The delta over today is the standing audibility |
| 4 | Tombstone (`:93`) | Ack | **Ack** | Correct — nothing owed |
| 5 | Disabled target (`:115`) | Ack | **Ack** | §4.1. On `Enable`, Resume redelivers the Nak'd-pending set on its own timestamps (V9); Acked residue is the verb's or re-projection's |
| 6 | `violating` reads a genuine bool `false` (`:120`) | Ack | **Ack** | Nothing owed |
| 7 | Row-data errors blocking evaluation — non-bool `violating`; a violating row's missing `entityKey` echo (`:131`); a non-bool `missing_*` read | Ack (issues already level-raised, V19) | **Ack — unchanged** | Data errors: every fix is a re-projection, which delivers (V3). The raises stay level-driven at their existing sites; `scheduleFreshness`'s and `intColumn`'s raisers keep their shipped posture for the same reason |
| 8 | `GapWithoutPlaybook` (`:231`) | error alert + Ack | **paced `warning` + Ack — AMENDED 2026-08-28, see below** | Originally specified as a Long on the config-error rule. Reverted at build time: the exit has a population whose fix can never arrive, so a Long parks it forever — §4.2's own argument, never applied to this row |
| 9 | `UnresolvedReference` (`:579`) | paced warning + NakWithDelay | **unchanged** | Genuinely transient mid-convergence; the 5 s class is deliberate |
| 10 | `TemplateDataError` (`:583`) | paced warning + **Ack** | **paced warning + Long** | Sits on the boundary — the fault is template × row, and one of its fix paths (a template/playbook edit) produces **no new delivery** — so the fix-path rule puts it in the config class. Plausibly the clinic class itself; today it Acks once and is never revisited |
| 11 | `PlaybookConfigError` (`:587`) | paced error + Ack | **paced error + Long** | Config error — same as row 8 |
| 12 | Seq-0 metadata defer (`:273`) | NakWithDelay | **unchanged** | Metadata arrives on redelivery |
| 13 | Suppressed in-flight (`inflight_<g>`) | skip | **unchanged** | The mark owns it; `reclaim` is the authority |
| 14 | Exhausted → escalate (`:1344-1398`) | standing issue + Ack (V15) | **unchanged** | Escalated IS the invariant's third branch; no pending slot held |
| 15 | Admission deferral (`:535`) | NakWithDelay paces | **unchanged** | Contract #10 §10.8: "ordinary pacing, not a fault" |
| 16 | Infra failures (`:56`, `:104`, mark read/CAS errors) | NakWithDelay | **unchanged** | The 5 s transient class |
| 17 | `releaseCompletedLeg` KV-failure → anti-storm Ack (`:1045-1049` → `:636`) | Ack, silent | **unchanged, now named** | Pre-existing and out of scope; listed so C6 classifies every site |

`handleRow`'s per-gap aggregation gains the third accumulator with precedence
`Nak > NakWithDelay > NakWithLongDelay > Ack` — **both** existing `switch` blocks
(`evaluator.go:172-180`, `:181-188`) gain an explicit case; their `default:` arms are exactly
where a missed value silently downgrades to Ack (V8), so T3's mutations flip each.

**Row 8 AMENDED 2026-08-28 (build-time adversarial pass) — `GapWithoutPlaybook` stays at Ack.**
The row-8 Long was specified on the fix-path rule: a playbook edit produces no new delivery, so the
loop is the only automatic uptake. That is true *when a playbook edit is coming*. It is false for a
column the package **deliberately** projects with no `gaps` entry — a shipped, live pattern:
`packages/lease-signing/lenses.go:810` and `:812` project `missing_decision` and `missing_manager`,
both ORed into `violating` (`:815`), and neither is among `leaseApplicationComplete`'s seven declared
gaps (`packages/lease-signing/targets.go:91-102`). The lens's own doc states the intent — *"maps to NO
playbook entry … so it keeps the row violating without dispatching anything"*. `leaseApplicationComplete`
sets `Augur.Escalate: ["exhausted"]`, so `augurEscalation` declines and the row takes this exit.

A Long there parks a row for the whole human-latency window of a landlord decision — unbounded, and
literally forever for `missing_manager`, which nothing in Weaver ever closes. It holds a
`MaxAckPending` slot and re-runs the full `clearClosedMarks` preamble every floor: with nine
candidate columns, eight of them closed, that is 8 reads + 16 unconditional KV writes per stuck row
per floor, forever, for a configuration that is already correct.

This is exactly §4.2's argument for leaving the unregistered-target exit at Ack — *"a Long here holds
pending forever for a target that will never register"* — and it was never applied to this row. It is
now: **row 8 keeps Ack**, and gains only §8's severity demotion and a paced raise seam (§8 as
amended). Rows 10 and 11 keep the Long: `PlaybookConfigError`'s populations all have a real package
edit as the fix, and `TemplateDataError` was ratified onto the config side knowingly.

What row 8 gives up is automatic uptake for a genuinely-missing playbook entry. That is the smaller
loss: the motivating symptom (the clinic population) is a row-10/11 class, and the sanctioned way to
express an intentionally undispatched column already exists — a `surface` gap, which Acks with its
own standing issue (row 6 of the C6 classification). Making row 8's Long sound needs that expression
to be *required*, i.e. a gate asserting lens-projected `missing_*` ⊆ declared gaps, with the two
lease-signing columns converted to `surface`. Filed as its own unit; it is a package-behaviour change
(a `surface` gap raises per entity) that wants its own review, not a rider on this one.

**Fix-uptake is automatic for every Nak'd class.** A Nak'd row's redelivery is a fresh
`handleRow` against the **current** registry target and the **current** row body — so a playbook
fix, an augur-block addition, or a corrected projection reaches every declined row within one
floor (or immediately, via V3, when the row re-projects), with no rebuild anywhere. This is what
makes the standing loop sufficient and the replay a repair verb rather than a dependency.

### 3.3 The replay verb — `ReplayTarget`, manual, Loupe-invoked (Andrew's correction)

**The verb.** `Engine.ReplayTarget(ctx, targetID)` joins the `control.go` operator family (V20):
it delete-then-creates the target's lane-1 durable under the supervisor's `resetMu`
(`supervisor.Reset` when managed; a not-registered target errors loudly, mirroring
`Enable`'s check), so `DeliverLastPerSubject` re-delivers the target's current row set through the
unchanged evaluation ladder. Transport + operator capability mirror `ResetRetryBudget` (the
un-park fire's shipped pattern); Loupe surfaces it beside Enable/Disable. Stable durable name —
no nonce, no prune, stable health keys (§4.3).

**What it is for** — the populations the standing loop cannot reach, each named:

1. **The pre-existing Acked-decline residue** — every row declined-and-Acked before this ships
   (the clinic 26). One verb invocation per affected target after deploy; C2's census names the
   targets. (A cold boot — `make down && make up` — also replays everything, as today.)
2. **The V16 strand** — a NATS-only restart leaves a quiet target's Nak'd-pending set with no
   armed redelivery timer. Any single delivery under the target's prefix re-arms the whole set
   (V16), so an active lens heals itself; a fully-quiet target is repaired by the verb. The
   operator signal: a target whose consumer shows `num_ack_pending > 0` with no redeliveries
   after a server restart — a Lamplighter surface, named here, not built here.
3. **Acked residue of the narrow windows** — the Disable marker-write window, a class the table
   leaves at Ack whose premise later proves wrong. The verb is the general "re-enumerate this
   target" repair.

**What was deliberately NOT built (the correction's substance):** no per-boot rebuild, no
Enable-triggered rebuild (`Enable` stays exactly today's Resume + reconcile — and under this
design Resume now *does* resume remediation for every Nak'd-pending row, which is most of what
the old "Enable can't redeliver" complaint was about), no target-update rebuild (the Nak loop
picks the fix up within one floor for every declined row; the banner flags the residue), no
reconnect-triggered rebuild (V16's repair is the verb + the natural re-arm). Rationale beyond the
correction itself: every automatic trigger was a standing burst — O(all rows of the target)
through V13's preamble cost, plus stale-mark episode re-fires (V15, including a paid Augur call)
— paid on events (boots, deploys, reconnects) that are not evidence anything needs
re-enumeration. The verb pays it exactly when an operator has evidence.

**What one invocation costs — stated, bounded (V15):** O(current rows of the target) through the
full preamble, and for a violating row whose mark is stale (lease-expired) it re-fires the episode
— budget-counted (`bumpDispatchCount` → exhaustion), with the Augur arm **suppressed when the
escalation issue already stands** (a level check at the raise site; re-deriving a standing fact
must not re-pay the model — ships with the verb, T11).

**Multi-replica note (Weaver is single-instance in every shipped deployment):** lane-1 is a shared
**pull** durable, so N replicas **split** the replayed rows; the verb's cross-replica
delete-then-create drops the other replica's in-flight ack (the row is re-owed; benign). `resetMu`
is per-process; the verb is operator-paced, so concurrent invocations are not a designed load.

### 3.4 The `NumDelivered` re-fire branch retires; anti-storm moves ahead of `planGap`

Two composed changes to `dispatchGap`/`fireEpisode`:

**(a) Early anti-storm.** The mark read moves ahead of `planGap`: after `releaseCompletedLeg`, a
row with `found && !stale` **Acks immediately** — no `planGap`, no `admitGap`. This retires the
re-fire branch *and* fixes a real interaction the adversarial pass found: `admitGap` consumes a
token inside `planGap` before the anti-storm decision, so under routine redelivery a marked row
would have burned an admission token per cycle per gap. With the early Ack, a marked row costs
one mark read per cycle and nothing else.

**(b) The publish-failure leg is replaced, not dropped (V18 — the branch's one legitimate job).**
`fire`'s publish failure Naks, and today that guaranteed immediate redelivery re-fires the episode
only because of the retiring branch. The replacement: an in-memory **republish set** keyed
`(targetID, entityID, col)` — an entry is added when `fire`'s publish fails, and the early-Ack arm
in (a) re-fires the existing episode (preserved claimId → same requestId → collapses on the
Contract #4 tracker) **iff** the row's key is in the set, removing the entry on a successful
publish. Restart loses the set → the reclaim ladder is the backstop (≤ lease for the first
reclaim). This is deliberately **not** compensation-by-mark-delete: a publish failure can be
*ambiguous* (op accepted, reply lost), and deleting the mark would let a redelivery mint a second
episode — the preserved-claimId re-publish is the only idempotent shape under ambiguity, which is
exactly why today's code was built on it (V18). The three comments that lean on the old mechanism
(`evaluator.go:776-778`, `:190-193`, `reconciler.go:1126-1128`) are rewritten in the same
increment.

After (a)+(b), `msg.NumDelivered` has no reader (C3 narrows to `msg.Sequence` only), and the
`redelivered` parameter deletes.

### 3.5 The `gapConfig:` latch self-heals (symptom 2)

Per the direction, the per-entity clear stays at its site — **narrowed to a WELL-FORMED read**,
threaded out of `boolColumn`: the clear also fires today for a *non-bool* value — a read that is
not evidence of closure — so a repeatedly re-projecting broken row would clear the target-scoped
latch at its projection rate. With the narrowing, the flap is what the direction accepted: at most
one clear per genuinely closing entity, re-raised within ≤ one long floor by a still-open row's
next delivery.

**Amended 2026-08-28 at build time — the predicate is WELL-FORMED, not `isBool`.** This section
first specified V19's `isBool`. That is wrong and must not be built: `boolColumn` returns `false`
for three distinct reads, and `isBool` is false for two of them.

| read | `isBool` | evidence the gap CLOSED? |
|---|---|---|
| column absent / nil (`evaluator.go:1088-1091`) | **false** | **yes** — `evaluator.go:867-869`: "a row that stopped reporting the column closed it" |
| present, genuine `false` | true | yes |
| present, **not a bool** | false | **no** — the only read the narrowing targets |

An `isBool` gate would therefore stop clearing the latch for a column that **disappears from the
projection**, and `evaluator.go:874-882` records that this site is the *only* clear a
`GapWithoutPlaybook` / `UnresolvedReference` / `PlaybookConfigError` can reach when a column simply
stops being reported — so the gate would strand those issues permanently, the dossier's
"every RETIRE belongs above every cannot-act GUARD" failure. The shipped predicate is
**well-formed = absent, nil, or a genuine bool**; only present-and-not-a-bool is refused.

Re-raise conditionality, per code (the adversarial pass's enumeration):

| Code | Re-raises on a decline-loop redelivery? |
|---|---|
| `UnresolvedReference` / `PlaybookConfigError` | **Yes, unconditionally** — raised in `planGap`, reached by every delivery of an open un-suppressed row |
| `GapWithoutPlaybook` | **Conditionally** — the raise site is below the suppression and exhaustion gates, so a target whose *every* remaining open row is in-flight-suppressed or exhausted does not re-raise it. Residual, named: those populations are not dark (an in-flight row is owed by its mark/`reclaim`; an exhausted row carries its own standing exhaustion issue), but the `gapConfig:` latch itself can stay retired while such rows hold the column open |

`since` semantics (V14): `GapWithoutPlaybook` (via `alert`) re-mints `since` on a flap; the two
paced codes keep their original onset across flaps because the pace memory survives `clear`.

### 3.6 The issue-cache bound

With data errors Acking (§3.2's rule), no per-delivery data-error flag exists — the only
mechanical thread is `boolColumn`'s well-formedness return (§3.5 as amended), used solely by the
§3.5 clear narrowing.

**The cache bound, re-scoped (V14):** the heartbeat *document* is already capped severity-first
with an overflow entry (`boundIssues`, 50) — that machinery stays the sole document-level cap.
What is actually unbounded is the in-memory `issues` **map** and the per-heartbeat sort over it: a
verb replay of a systemically-broken 100k-row lens would grow both without limit. The bound this
design ships: the **per-entity data/template issue families are capped per target in the map
itself** (insertion refused past the cap, one overflow counter entry per target maintained in
place), so the map and the sort stay bounded while `boundIssues` continues to see an honest set.

---

## 4. Deviations argued

### 4.1 Disabled rows Ack; `Enable` stays plain Resume

- A Nak loop buys nothing during a freeze: `Disable` pauses the pump before the iterator (V9), so
  rows are neither delivered nor acked while frozen — and the already-Nak'd set holds its pending
  slots for the whole freeze regardless of the exit's Decision.
- On `Enable`, Resume redelivers every Nak'd-pending row on its own timestamps (V9) — so under
  this design "remediation resumes for whatever is still violating" becomes true for the entire
  declined population automatically. The residue (rows Acked in the marker-write window) is small
  and covered by re-projection or the verb; the component-doc sentence is qualified accordingly.

### 4.2 The unregistered-target exit stays Ack

The exit's reachable populations are a teardown in progress, a `targetId` rename, and a
**rejected** target (never unregistered by `rejectTarget`). For the last, a Long would hold
pending slots forever against a target that will never register, and a failed durable `Remove`
(no retry ticker) would leave an orphan consumer Nak-looping its whole row set indefinitely —
today's Ack is self-limiting. Any real loss is re-enumerable by the verb.

### 4.3 Stable-name delete-then-create in the verb, not a nonce

Same JetStream rule (V6). The registry's nonce exists because that durable is per-instance anyway
and a prune pass cleans predecessors. Lane-1 durables are per-target, shared, already have
delete-recreate machinery (V11), and their names key the per-consumer health sinks — a nonce would
churn health keys and need its own prune. The rule both mechanisms honor is
DeliverPolicy-at-first-create.

---

## 5. State-lifetime table

| State | Scope | Created / reset / carried |
|---|---|---|
| The republish set (§3.4b) | process, in-memory, per (target, entity, col) | entry added on a `fire` publish failure; removed on that key's next successful publish and by `clearClosedMarks`' mark clear; **lost on restart → reclaim ladder is the backstop**; evicted with the target on unregister/revoke |
| Nak'd-pending set + delay timestamps | JetStream consumer (server-side) | the substrate's own machinery — created by the Nak, freed by ack/Term (V1, V3), discarded whole by the verb's durable delete (§3.3); the V16 restart-strand is repaired by the verb or any fresh delivery; no engine mirror exists |

No cursor, no cycle set, no observed-column set, no budget, no eviction sweep, no boot-phase
machinery — the held design's six structures, dissolved. Loss of anything above degrades to a
paced reclaim or an operator replay, never a wrong verdict.

---

## 6. Consumer-envelope changes

Re-derived, not inherited (V4, V17):

- **`AckWait`: unchanged (30 s default).** The first draft raised it to 2 m on a
  buffered-replay-churn premise the pump already defeats twice over — `pumpPullMaxMessages = 1`
  and the `keepAckAlive` heartbeat (V17) — and the raise would have quadrupled crash-recovery
  redelivery latency for nothing. Withdrawn.
- **`MaxAckPending`: set explicitly to 2 000** (today: server default 1 000, V4). Nak'd-pending
  declines hold slots (V1); V2/V3 keep the decline cycle and fix-uptake flowing at the cap, so the
  starved class is **new entities** on a target already carrying ≥cap distinct simultaneously-stuck
  rows. ~~The cap is deliberately modest: above ~1 024 pending the server's `checkPending` walks the
  whole map per timer fire and defers under ack load.~~ **Corrected 2026-08-28:** the citations are
  real and on-path but the mechanism attribution is wrong, and wrong in the direction that matters.
  The pending-map walk (`consumer.go:5917`) is **unconditional at every size** — nothing about it
  begins at 1 024. What `len(o.pending) > 1024` gates (`:5916`) is the opposite: an early **bail**
  out of the expiry walk when acks are already in flight (`:5918-5921` → `resetPtmr(100ms)`,
  return). So 2 000 deliberately sits *above* the threshold whose guard exists to protect the very
  redelivery walk this design depends on. It is kept, because lane 1 is serial (`Workers` unset) so
  in-flight acks are one at a time and the bail is a 100 ms **deferral, not a loss** — but "modest"
  was the wrong word for a value chosen above the threshold it cited, and the real reason 2 000 is
  safe is the serial lane, not the size.
  2 000 is ~70× today's worst observed stuck population — and with data errors Acking (§3.2),
  the pending mass is the config-error classes only; C5 re-derives it if the corpus says
  otherwise. Honest limits, both stated: the >cap new-entity stall (signal: `num_ack_pending`
  pinned at the cap — Lamplighter surface), and the V2 head-of-line drain — each floor tick
  redelivers the stuck set as a batch through one serial worker, so new-row latency degrades by
  ~(stuck rows × per-row cost) once per floor (~30 s per 5 m at 1 000 stuck rows and ~30 ms/row).
  Lane-1 stays deliberately serial (`Workers` unset); the floor is sized against that.
- **`MaxDeliver`: untouched** — unbounded, test-pinned (V5).
- **`LongRedeliveryDelay`: default 5 m** (§3.1), env-clamped.

---

## 7. Cost

- **Steady state:** the loop's population is the **config-error classes only** (rows 8/10/11 —
  data errors Ack out per §3.2's rule). Per stuck row per long floor, one `handleRow` **plus
  V13's preamble term** —
  `1 read + 2 unconditional KV writes` per **closed** (non-surface) candidate column, serialized
  on the target's worker. Formula: `O(stuck rows × closed candidate columns)` KV ops per floor.
  For the shipped corpus this is small (most targets declare 1–3 gaps); C5 measures `gaps per
  target` so the number is derived, not assumed. A healthy target costs zero, and **nothing
  happens at boot** — the standing cost is the loop and only the loop. (An optimization — skip
  the two deletes when the adjacent `marks.get` already said not-found — is noted for the
  builder; not load-bearing.)
- **The verb:** O(current rows of the target) per invocation, operator-paced, plus the bounded
  stale-mark re-fires (§3.3).
- **Op traffic:** *reduced in steady state* — §3.4a stops marked rows re-entering `planGap` (and
  the retired branch's per-redelivery re-publish), which under routine redelivery would have
  grown with the stuck population.
- **The clinic 26:** newly-declining rows self-heal from ship time; the pre-existing 26 are
  healed by one `ReplayTarget clinicSiteBackfill` (or the next cold boot), then dispatched
  through the unchanged mark/budget ladder. Census C2 re-derives the class at build time — its
  stop-rule is "any class §3.2 leaves at Ack" (rows 1/2/4/5/6), so a surprise class fails
  Phase 0 loudly.

---

## 8. Severity — RATIFIED: demote both to `warning`

§3.2 rows 8/11 make `GapWithoutPlaybook` and `PlaybookConfigError` standing (the Nak loop
re-raises for as long as the fact holds). Both are `error`-severity today, and `aggregateStatus`
maps any `error` to `unhealthy` over the full issue set (`health.go:734-748`) — so a
package-authoring typo would pin Weaver `unhealthy` until the package is fixed, while it
dispatches normally for every other target. Contract #5 §5.2 defines `unhealthy` as *"cannot
fulfil its primary responsibility"*, and the codebase draws the line itself for a sibling per-row
fault at `evaluator.go:122-128` (*"a warning (degraded), never an error"*).

**AMENDED 2026-08-28 — the demotion has a second consumer of severity, and it was not derived.**
`aggregateStatus` is not the only reader. `boundIssues` selects the 50 listed issues **severity-first,
ties broken on key order** (`health.go`, `severityRank`: `error` = 0, everything else = 1), and the
unbounded per-entity families key on `data:` and `gap:`, which sort *ahead of* `gapConfig:`. While
these two codes were `error` they were listed unconditionally; as `warning`s they fall into the same
rank as the flood and lose the tiebreak to it — so the entry that EXPLAINS a fault is evicted by the
fault's own per-row noise, cross-target, since the issue set is per Weaver instance.
`docs/observability/health-kv-schema.md` states the surviving guarantee in words the demotion
falsifies: *"the unbounded families are all `warning`s, and in key order they sort ahead of the
entries that explain a fault … Selecting by key alone would let sixty unrouted tasks evict the one
`error` naming the cause."*

The demotion is kept (its `aggregateStatus` argument is sound); what is repaired is the ranking that
`error` was doing by accident. `boundIssues` now ranks **bounded, target-scoped families
(`gapConfig:`, `consumer:`, `target:`, `timer:`) ahead of the unbounded per-entity families,
independently of severity** — the doc's actual intent, stated directly instead of riding on a
severity that was free to change. `UnresolvedReference` was already `warning` and already had this
exposure; the same repair covers it.

**Decision (Andrew, 2026-08-27): both codes are demoted to `warning` at their raise sites** — one
severity for all callers, so the change reaches lane 1 as well as the decline loop. Ships in
Inc 2, with the raise-site change and its lane-1 effect covered by T3.

---

## 9. Reconciliation with the existing mental model

- **"Isn't this what alternatives A and B already were — and weren't they rejected?"** B alone
  ("Nak the declines") was rejected partly because it cannot reach rows declined before the
  change — under the corrected scope that residue is explicitly the verb's job, a one-time
  operator repair, not a standing mechanism. §11 prices the pieces.
- **"Doesn't Nak'ing a row that re-projects leave a stale retry racing the fresh state?"** No —
  V3: the overwrite Term's the pending old revision eagerly; only the latest revision of a
  subject can ever redeliver. **This holds only at KV history 1** — V7 names the pin; T4 turns it
  into a Weaver-owned gate whose failure is this design's revive-the-fallback trigger.
- **"Is the sweep now redundant?"** No — untouched. Its legs reconcile *dispatch residue*; this
  design fixes the *pre-dispatch* population. The held §3's 127-hour count-no-mark window is
  closed here too: such a row is violating and un-suppressed, so on its next delivery it is
  either dispatchable or exhausted-escalated, and the Nak loop guarantees a next delivery for
  every declined row.
- **"New state — do we keep that state somewhere already?"** The retry state IS JetStream's
  pending set (§5) — with its two real limits (V16's restart strand, V7's history pin) named and
  owned rather than discovered.
- **"Does anything else read `NumDelivered`?"** Held census C3 re-verified: post-preamble message
  reads are `msg.Sequence` and `msg.NumDelivered` only, the latter at exactly the retiring
  branch. After §3.4, `msg.Sequence` only — T2 pins it.
- **"Doesn't the contraction monitor still read wrong after a warm restart?"** Its false comment
  is deleted and replaced with the honest semantics: counts rebuild from stuck-row redeliveries
  and fresh projections; a full per-target rebuild is what `ReplayTarget` provides. The monitor
  is diagnostics (held ledger row 13: nothing gates on it), so best-effort-with-honest-doc is the
  right posture — machinery to make it exact was the row-sweep shape Andrew held.

---

## 10. Contract surface — none; three adjudications recorded

- **Contract #10 §10.8's liveness bullet already promises this behavior.** An implementation
  honoring a frozen promise needs no amendment (the held §10 withdrew the same edit for the same
  reason).
- **§10.8 admission ("Absent … is unbounded — byte-identical dispatch, no row read"):** §3.4a
  makes this *more* true — today a redelivered marked row burns an admission token before the
  anti-storm drop; with the early Ack it no longer enters `planGap` at all.
- **§10.3 anti-storm:** marks/OCC/idempotency untouched; the retired re-fire branch is engine
  behavior no §10.3 sentence names, and the §3.4b republish set preserves the claimId exactly as
  §10.3 requires for userTask gaps.
- **Component doc** (`docs/components/weaver.md`, committed with the build): the lane-1 section
  gains the decline classes and the verb; the contraction restart sentence
  (`contraction.go:23-25` — currently false, V6) and the `Enable` sentence are rewritten to the
  now-true mechanisms (§4.1, §9).

---

## 11. Alternatives

**Row 1 — do not have the thing.** Accept that a declined row is revisited only by re-projection.
Forbidden by the frozen liveness bullet (§10) — the current behavior is a bug against a ratified
promise, with a live victim blocking a verticals row. Rejected.

**Row 2 — the held row sweep** (DD complete, kept as fallback). Rejected by Andrew's doctrine and
by measure: six cross-pass state structures, an extraction seam, a budget/cursor/cycle apparatus,
perpetual O(all rows)/5 m — versus one in-memory set and O(stuck rows). Its fallback trigger
(Phase-0 substrate semantics do not cooperate) did not fire — with one live pin (V7) whose failure
would revive it.

**Row 3 — standing automatic replay (per-boot / per-reconnect / per-update durable rebuild).**
The first draft's shape; **withdrawn on Andrew's correction**, and on its own merits once priced:
every automatic trigger is an O(all rows) burst with stale-mark episode re-fires, paid on events
(boots, deploys, reconnects) that are not evidence anything needs re-enumeration — while the Nak
loop already delivers automatic fix-uptake for every declined row. The manual verb pays the same
cost exactly when an operator has evidence. Residue the automatic variants uniquely covered: the
pre-design Acked population (one-time; the verb), and V16 strands on fully-quiet targets (the
verb, with a named Lamplighter signal).

**Row 4 — a durable "declined" marker** (held §11 C, re-priced). Edge-triggered state about a
level fact with a loss window in the under-remediation direction, strictly dominated: JetStream's
pending set already IS that marker. Rejected.

**Row 5 — rewrite the N consumers** (fix the lenses to re-project). 26 targets across 13 packages
share the hole, and a re-projection fix masks rather than closes. Rejected on demand breadth.

**Row 6 — keep the re-fire branch** (drop direction clause 3). Under routine redelivery it
re-publishes every in-flight episode per stuck-row floor and burns an admission token per cycle
besides (§3.4a). Its one legitimate job (the publish-failure retry, V18) is preserved by the
republish set at strictly narrower scope. Rejected.

---

## 12. Executable censuses

- **C1 (carried, build Phase 0):** production weaver targets —
  `grep -rn 'TargetID:' --include='*.go' packages/ | grep -v _test | wc -l` → 26 this fire;
  cross-check distinct values (9 literals + 17 constants).
- **C2 (carried, build Phase 0, live stack):** the clinic population — keys under
  `clinicSiteBackfill.`, violating count, `weaver-state` keys under the same prefix, and **which
  §3.2 row** the stranded rows take. **Stop-rule: any class the table leaves at Ack** (rows
  1/2/4/5/6) fails Phase 0 loudly — stop and re-derive. The affected-target list doubles as the
  post-deploy `ReplayTarget` run-book.
- **C3 (carried, narrowed):** post-preamble `msg.` reads — after §3.4, `msg.Sequence` only.
  Ships as T2.
- **C5 (build Phase 0, live stack):** total `weaver-targets` rows, per-target max, and **declared
  gaps per target** — sizes the verb's burst, the §7 steady-state formula, and the §6 cap. If a
  target exceeds ~2 000 rows, re-derive §6 before building.
- **C6 (run this fire — the review artifact):** `grep -n 'substrate.Ack' internal/weaver/evaluator.go`
  → **21 sites**: `27, 34, 43, 93, 115, 120, 131, 201, 231, 257, 552, 583, 587, 636, 688, 704,
  812, 1344, 1359, 1388, 1398`. Classification: §3.2 rows cover 27/34/43/93/115/120/131 (rows
  1-7), 231 (row 8), 583 (row 10), 587 (row 11), 1344/1359 (row 14), 1388/1398 (escalation-mark
  lease-live / CAS-conflict — the escalation episode is owed by its mark, same footing as row
  13); 201 is the aggregation tail; 552 and 812 are success paths (not declines); 636/688/704
  are the anti-storm / CAS-lost drops (the winner owns the episode); 257 is the surface-gap raise
  (the issue IS the escalation, level re-raised on every delivery). Every future edit to this set
  re-runs the census against the table.

---

## 13. Test strategy (fixture + mutation disciplines carried from the held §13 unchanged)

- **T1 (Inc 1):** substrate — `NakWithLongDelay` routes to `NakWithDelay(long)` at **every**
  Decision switch (the four `applyDecision` call sites and `disposeJetstream`), asserted not-Ack;
  iota pin test extended; mutation: revert any one case to `default` → red.
- **T2 (Inc 3):** pinning — post-preamble message reads are `msg.Sequence` only; `redelivered`
  gone (C3).
- **T3 (Inc 2):** per-class Decisions — one test per changed §3.2 row (3, 8, 10, 11),
  mutation-checked at each site **and** at both aggregation switches; plus the fix-path-rule
  boundary tests (a data-error row — non-bool column, missing echo — returns **Ack** with its
  issue raised; a disabled target's rows return Ack regardless) and the row-3 raise/clear test.
- **T4 (Inc 2, embedded NATS e2e — pins V1+V3+V7):** a declined row is Nak'd-pending; overwriting
  the key frees the slot (eager Term) and the fresh revision delivers and dispatches once fixed.
  Includes the **explicit `History: 1` assertion, Weaver-owned** — its reddening is the
  revive-the-fallback trigger.
- **T5 (Inc 2, e2e):** automatic fix-uptake — a row declined `GapWithoutPlaybook`, the target
  updated with the entry, the row dispatches within one floor **with no rebuild and no
  re-projection** (§3.2's fix-uptake claim, pinned).
- **T6 (Inc 4, e2e):** the verb — rows declined-and-**Acked** under the old path (fixture),
  `ReplayTarget` re-delivers the current set and every still-violating row dispatches; invoking
  it on an unregistered target errors loudly; a replayed row with a live mark takes the
  anti-storm Ack (no duplicate dispatch).
- **T7 (Inc 4):** `Enable` after a freeze — Nak'd-pending rows redeliver after Resume with no
  rebuild (V9 pinned at the Weaver level).
- **T8 (Inc 2):** latch self-heal — close entity A (clear fires), re-raise within one floor from
  B's loop; close B, final retirement, no re-raise. Plus the narrowing test: a **non-bool**
  column read does NOT clear the latch.
- **T9 (Inc 3):** publish-failure retry — `fire` fails, the republish set re-fires the same
  claimId on the immediate redelivery; with the set cleared (simulated restart), reclaim recovers
  within one lease. Mutation: drop the set-check → the redelivery must NOT re-publish and the
  test must fail on promptness.
- **T10 (Inc 4, embedded NATS e2e) — REWRITTEN 2026-08-28, V16 is false (see V16a).** The original
  asserted that a quiet long-Nak'd row does NOT redeliver after a server restart. The pinned server
  re-arms the redelivery timer from restored pending state on startup, so that assertion is false and
  would only ever be "made to pass" by weakening it. The test now pins the TRUE behaviour: restart the
  embedded server under a quiet long-Nak'd row and assert the redelivery DOES arrive unaided, on the
  row's own backdated deadline. The verb keeps T6 as its pin; it is a repair for the Acked residue,
  not for a strand that does not exist.
- **T11 (Inc 4):** the verb does not re-pay Augur — a row with a standing escalation issue and a
  stale mark replays without a second reasoning dispatch.

---

## 14. Build decomposition for the Steward

Sequential; each independently green. Review depth: **Inc 2 and Inc 3 are posture-changing**
(decline semantics; a retired recovery branch replaced by the republish set) — full adversarial
pass; Inc 1 and Inc 4 standard.

- **Inc 1 — substrate.** `NakWithLongDelay` + `LongRedeliveryDelay` (all V8 touch points). Owns
  T1. Weaver-inert until Inc 2.
- **Inc 2 — decline classes.** §3.2's table + the well-formedness threading (§3.5 as amended) + the
  row-3 raise/clear + the map-level cache bound + `MaxAckPending: 2000` + the §3.5 clear narrowing +
  (if ratified) the §8 severity demotion. Owns T3, T4, T5, T8. Phase 0 runs C1/C2/C5 with C2's
  stop-rule.
- **Inc 3 — dispatch-path restructure.** Early anti-storm ahead of `planGap`; retire
  `redelivered`; the republish set; the three comment rewrites. Owns T2, T9.
- **Inc 4 — the `ReplayTarget` verb.** Engine verb + capability verb + Loupe surface (V20's
  pattern) + Augur re-fire suppression + the contraction/component-doc sentence rewrites. Owns
  T6, T7, T10, T11. Post-deploy: run the verb per C2's affected-target list (the one-time heal).

---

## 15. Adversarial pass — run and folded (2026-08-27); replay scope corrected by Andrew

Two independent reviewers (substrate-mechanics lens; engine-control-flow lens) ran against the
draft; all findings folded. Andrew then corrected the replay scope live (automatic rebuild → the
manual verb), which resolved several findings by removal. The load-bearing ones and where they
landed:

- NATS-restart strands the Nak'd population (`o.ptmr` never re-armed) → V16; repaired by the verb
  + the one-delivery re-arm, with a named Lamplighter signal (§3.3, T10). (The draft's automatic
  reconnect-replay answer was withdrawn with the rest of the automatic rebuilds.)
- Retiring the re-fire branch silently broke `fire`'s publish-failure retry → the **republish
  set** (V18, §3.4b, T9), chosen over mark-delete compensation because ambiguous publish failures
  make compensation mint duplicate episodes.
- `TemplateDataError` (`:583`) was missing from the class table and is plausibly the clinic class
  → row 10; C2's stop-rule generalized.
- Row 7 as first drafted Nak-looped rows owing nothing (non-bool sibling columns, disabled
  targets) and contradicted `scheduleFreshness`'s own anti-Nak decision → first a three-site
  flag + precedence rules; then Andrew's fix-path rule (below) resolved it wholesale — data
  errors Ack, so the flag and its precedence machinery deleted.
- The draft's `AckWait: 2m` rationale was defeated by the pump's own prefetch-1 + `keepAckAlive`
  (V17) → withdrawn.
- `MaxAckPending: 10000` ignored the server's >1024 `checkPending` scan behavior → 2 000 (§6).
- Marked rows burned an admission token per redelivery inside `planGap` → early anti-storm
  (§3.4a).
- The steady-state cost table omitted V13's per-cycle preamble term; the C6 census undercounted
  (21, not 16) with 8 unclassifiable sites → §7 formula; §12 C6 classification.
- The latch clear fired on non-bool reads (an unbounded periodic flap) and `GapWithoutPlaybook`'s
  re-raise is gate-conditional → the `isBool` narrowing + the named residual (§3.5).
- Findings specific to the withdrawn automatic rebuilds (Enable-Reset ordering, update-Reset
  double-replay, boot-burst pricing, multi-replica rolling-deploy multiplication) are resolved by
  their removal; their content survives as the §11 Row 3 pricing.

Andrew's second live push-back (same day) reshaped §3.2: the class taxonomy now keys on the fix
path — data errors Ack with a standing issue (their fix necessarily arrives as a superseding
re-projection), only config errors ride the long Nak loop — which also deleted the per-delivery
data-error flag and its precedence machinery; and the indefinite-retry posture (unbounded
MaxDeliver) is now stated explicitly with its bounds (§3.1).

**Checkpoint:** design complete · adversarial pass run and folded · replay scope corrected by
Andrew (2026-08-27) and folded · decline taxonomy corrected and folded · **✅ RATIFIED (Andrew,
2026-08-27) — build-ready**; §8 demote and the dropped update-rebuild lever are settled in the
banner.

---

## 16. Build fire brief (Phase 0, 2026-08-28)

Compiled once for the whole item (`agents/fire-brief-template.md`); resumes run a delta-scout, not
a recompile. Environment: Claude Code remote container (`agents/steward/REMOTE.md`) — native
Postgres on :5433, no shared live stack.

### 1. Scope sentence (verbatim, §14)

> **Inc 1 — substrate.** `NakWithLongDelay` + `LongRedeliveryDelay` (all V8 touch points). Owns
> T1. Weaver-inert until Inc 2. **Inc 2 — decline classes.** §3.2's table + the `isBool` threading
> + the row-3 raise/clear + the map-level cache bound + `MaxAckPending: 2000` + the §3.5 clear
> narrowing + (if ratified) the §8 severity demotion. Owns T3, T4, T5, T8. **Inc 3 — dispatch-path
> restructure.** Early anti-storm ahead of `planGap`; retire `redelivered`; the republish set; the
> three comment rewrites. Owns T2, T9. **Inc 4 — the `ReplayTarget` verb.** Engine verb +
> capability verb + Loupe surface + Augur re-fire suppression + the contraction/component-doc
> sentence rewrites. Owns T6, T7, T10, T11.

Green bar: `go build ./...`, `make vet`, `golangci-lint run ./...`,
`STRICT=1 go run ./scripts/lint-conventions.go`, `go test ./internal/weaver/... ./internal/substrate/...
./internal/processor/...` with `POSTGRES_TEST_DSN` set, plus the build-tagged harnesses the
`substrate.Decision` signature change reaches (below).

### 2. Census results — every design premise re-run live

| Census | Design's expectation | Live result | Verdict |
|---|---|---|---|
| C1 — production weaver targets | 26 (9 literals + 17 constants) | **26** (9 quoted + 17 constants) | ✅ confirmed |
| C6 — `substrate.Ack` sites in `evaluator.go` | 21, at `27,34,43,93,115,120,131,201,231,257,552,583,587,636,688,704,812,1344,1359,1388,1398` | **21, at exactly those lines** | ✅ zero drift |
| C5 (static half) — declared gaps per target | "most targets declare 1–3 gaps" | **9 of 20 target-specs declare ≥2; `leaseApplicationComplete` declares 7, `ErasureCompleteTarget` 5** | ❌ **this cell first recorded "every target declares exactly 1" — that was a broken counting script, corrected 2026-08-28.** §7's per-floor cost is `O(stuck rows × closed candidate columns)` and the multiplier reaches 8, not 1 |
| C5 (live half) — `weaver-targets` row counts, per-target max | needs a live stack | **not runnable in this container** | ⛔ carried, below |
| C2 — the clinic population + its decline class | needs a live stack | **not runnable in this container** | ⛔ carried, below |
| C3 — post-preamble `msg.` reads | `msg.Sequence` only after §3.4 | deferred to Inc 3 (it *is* T2) | — |

**C2/C5-live are not runnable here and are not faked.** They read a live Core-KV population
(the clinic 26/29) that exists only on the attended stack; this container's stack is fresh and
empty (`REMOTE.md` §3). What this changes and what it does not:

- **It does not gate Inc 1–4's code.** Every §3.2 reclassification, the severity demotion, the
  latch narrowing, the cache bound, the restructure and the verb are correct independent of which
  class the clinic rows took — C2's stop-rule is a *design-validity* check ("does this design fix
  its motivating symptom?"), not a safety check on the diff.
- **It does gate the one-time heal.** C2's stop-rule and its affected-target run-book are carried
  forward as a **post-deploy gate on the attended stack**, to be run before the one-time
  `ReplayTarget` invocation: if the stranded class lands in a row §3.2 leaves at Ack
  (rows 1/2/4/5/6), the clinic symptom is NOT closed by this item and needs a follow-on row.
  Recorded here rather than filed as a residual because it has no code deliverable.

### 3. Verified touch-list (checked live at HEAD; design citations re-verified)

**Inc 1 — substrate**

| Site | Design cited | **Actual** | What |
|---|---|---|---|
| `internal/substrate/consumer.go` | `:50-56` | **`:39-57`** | `Decision` type + iota; append `NakWithLongDelay` (=4) after `NakWithDelay` |
| `internal/substrate/consumer.go` | `:448-471` | **`:449-472`** | `applyDecision`; `default:` at **`:468`** is the silent-Ack arm |
| `internal/substrate/consumer.go` | `:319`, `:395` | **`:319`, `:395`** | the two `applyDecision` call sites (signature grows a second floor) |
| `internal/substrate/consumer.go` | — | **`:59-65`**, **`:145-148`** | `DefaultRedeliveryDelay`; `DurableConsumerConfig.RedeliveryDelay` |
| `internal/substrate/consumer.go` | — | **`:233`** | `runDurableLoop` call — carries the floor through |
| `internal/substrate/consumer_supervisor_spec.go` | — | **`:174-176`** | `ConsumerSpec.RedeliveryDelay` |
| `internal/substrate/consumer_supervisor_pump.go` | `:704`, `:710` | **`:704`, `:710`** | `:704` passes a literal `NakWithDelay` + probe interval; `:710` the handler decision + `spec.RedeliveryDelay` |
| `internal/processor/commit_path.go` | `:850-869` | **`:809-829`** | `disposeJetstream`; `default:` at **`:823`** is the second silent-Ack arm |
| `internal/processor/outbox/consumer_decisions_test.go` | *not in the design* | **`:28-40`** | **third** `Decision` switch (`decisionName`) — a test diagnostic, not an Ack risk, but it stringifies an unnamed value as `Decision(4)` |
| `internal/substrate/nak_with_delay_test.go` | `:13-26` | **`:14-26`** | the iota pin — extend, do not rewrite |

**Inc 2 — weaver decline classes** (all 21 C6 sites verified; only these change)

| Site | Row | Change |
|---|---|---|
| `evaluator.go:40-43` | 3 | on `json.Unmarshal` failure raise `RowDataError` at `issueKeyDataEntity(targetID, entityID, "body")`; **clear it immediately after a successful `Unmarshal` of the same row** |
| `evaluator.go:229-231` | 8 | `alert(..., "error", "GapWithoutPlaybook", ...)` → **`"warning"`** (§8) and `substrate.Ack` → **`NakWithLongDelay`** |
| `evaluator.go:580-583` | 10 | `TemplateDataError` `substrate.Ack` → **`NakWithLongDelay`** (severity stays `warning`) |
| `evaluator.go:584-587` | 11 | `PlaybookConfigError` `"error"` → **`"warning"`** (§8) and `substrate.Ack` → **`NakWithLongDelay`** |
| `evaluator.go:171-179`, `:182-188` | — | both aggregation switches gain an explicit `case substrate.NakWithLongDelay:`; accumulators at `:134-135` gain the third (`longDelayed`), precedence `Nak > NakWithDelay > NakWithLongDelay > Ack` |
| `evaluator.go:1085-1102` | §3.5 | `boolColumn` threads WELL-FORMEDNESS out to its caller — not `isBool`; see §3.5's 2026-08-28 amendment |
| `evaluator.go:883-884` | §3.5 | the `gapConfig:` clear fires **only** on an explicit bool `false` read |
| `engine.go:414-425` | §6 | `targetSpec` gains `MaxAckPending: 2000` — **currently unset** (server default 1000, V4) |
| `health.go:132`, `:147-164`, `:303-316` | §3.6 | per-target cap on the per-entity `data:`/`template:` issue families in the map itself, with one overflow counter entry per target maintained in place; `boundIssues` (`:616-637`) untouched |

Issue-key helpers verified present: `issueKeyDataEntity` `evaluator.go:1637`, `issueKeyTemplateEntity`
`:1662`, `issueKeyGapEntity` `:1589`, `issueKeyGapConfig` `:1599`.

**Inc 3** — `evaluator.go:632-636` (anti-storm), `:776-778`, `:190-193`, `reconciler.go:1126-1128`
(the three comments), plus the `redelivered` parameter's whole thread.
**Inc 4** — `internal/weaver/control.go:87-460` (verb family), `contraction.go:22-25` (the false
restart sentence), `docs/components/weaver.md` (lane-1 section + the `Enable` sentence).

### 4. Precedents to mirror

- `NakWithLongDelay` mirrors `NakWithDelay`'s own shape at `consumer.go:449-472` — the floor is
  read from config and clamped in `applyDecision`, never carried on the Decision (V8).
- `LongRedeliveryDelay`'s clamp mirrors `engine.go:157-184`'s pattern verbatim:
  `if x <= 0 { x = default }` then `if x < floor { x = floor; Logger.Warn(...) }`.
- The `MaxAckPending` application already exists at `consumer_supervisor.go:608-610` (guarded on
  `> 0`) and is pinned by `lane_seam_test.go:14-40` — Inc 2 only sets the spec field.
- Inc 4's verb mirrors `ResetRetryBudget` in `control.go`; `supervisor.Reset` (`recreateDurable`
  under `resetMu`) is at `consumer_supervisor.go:211-231`.
- The overflow-counter shape mirrors `boundIssues`' own overflow entry (`health.go:616-637`) and
  `installer.go`'s `sampleWithOverflow`.

### 5. In-scope gotchas

**Environment / gates**

- `go test ./...` is NOT the gate set. This item changes a `substrate` interface signature, which
  reaches every build-tagged harness: run `make test-unrouted-convergence` (a Weaver target e2e —
  the closest thing to this item's own e2e), `make test-lease-convergence`,
  `make test-augur-convergence`, `make test-object-gc`, `make test-system-actor-capability`,
  `make test-control-plane-authz`. Enumerate with
  `grep -rl "^//go:build " --include=*_test.go internal/`.
- `POSTGRES_TEST_DSN` must be set or the suite is falsely green (`REMOTE.md` §3).
- `golangci-lint` must be the CI-pinned v2.11.4 built with go1.26.1, from `$(go env GOPATH)/bin`
  ahead of the stale system binary (`REMOTE.md` §7).
- **No `exhaustive` linter is enabled** (`.golangci.yml` — `default: standard`, only `errcheck`
  disabled). A missed `case` is a silent Ack that no gate catches. T1 is the only mechanism.
- No `packages/` content changes in Inc 1–3, so no manifest version bump. Inc 4's capability verb
  does touch a package — bump its manifest **and** the mirroring `Version` constant, and run
  `DIFF_BASE=<base-sha> go run ./scripts/lint-package-version.go`.
- Fixture `targetId`s stay **under 20 characters** — `lint-conventions` reads a 20-char value on an
  `…ID` identifier as a NanoID.

**Weaver dossier — `docs/components/weaver.md`, the entries this fire trips**

1. **A Health issue key is a LATCH: scope it to the fact it states, and split it only with every
   clear re-paired.** Row 3 adds a raise at `issueKeyDataEntity(t, e, "body")` — a *synthetic*
   column no other leg raises at. **Before adding its clear, enumerate every other leg that raises
   at that key**: `boolColumn:1097` and `intColumn:1141` both raise `RowDataError` at
   `issueKeyDataEntity` for *real* columns. `"body"` cannot collide with a real column name only
   if no lens ever projects a column literally named `body` — state that premise or pick a
   name that cannot collide.
2. **A fact ends by more routes than the one you are editing — enumerate the LEGS, not the verb.**
   The row-3 clear must also be reachable from the sweep and from teardown, or a row that stops
   being delivered strands the issue. `clearClosedMarks` runs on a DELIVERY; the sweep observes
   the same endings at `deleteMark`/`deleteCount`/the row-gone arm.
3. **A presence assertion cannot pin a clear whose caller re-raises in the same pass — the STAMP
   is the observable.** T3's row-3 raise/clear test and T8's latch test both assert on `since`,
   never on membership.
4. **Segmenting a Health key by entity is safe only where a clear site names that exact COLUMN.**
   The §3.6 cap exists because these families are already O(entities); the cap bounds the CACHE,
   `boundIssues` bounds the DOCUMENT — do not conflate them.
5. **An `error`-severity Health issue must not fire on a self-healing condition.** This is exactly
   §8's demotion; it also means the new Long loop must not introduce a *new* `error`.
6. **Prove each changed line by reverting THAT LINE, not the feature** — and anchor each mutation
   to its own function (three identical-looking sites exist). Where the claim is about WHERE a
   block sits (Inc 3's early anti-storm), **the mutation is a MOVE, not a revert**.
7. **A leg's arms are a lattice: every RETIRE belongs above every "cannot act" GUARD.** Inc 2's
   new Long returns sit below the disabled-target and registry guards — confirm no retire is
   pushed below a guard by the reordering.

**Substrate dossier — the entries this fire trips**

8. **Narrowing a JetStream consumer's filter strands its pending set** — Inc 4's verb is a
   delete-then-create, which is the sanctioned escape; do not reach for an update.
9. **A server-immutable consumer field needs delete-then-create in BOTH directions** —
   `DeliverPolicy` is non-updatable (V6); the verb must not degrade to an update on any path.
10. **A vendor-behaviour claim in a comment needs a pinned `file:line` on a path this code
    actually executes.** Every V-row citation copied into a comment is version-matched to
    `nats-server@v2.14.0` and must stay so.

**Standing checklist (the six)**

1. New state needs a LIFETIME: Inc 3's republish set — its state table is §5, already written
   (added on publish failure, removed on success and by `clearClosedMarks`' mark clear, lost on
   restart → reclaim ladder, evicted with the target).
2. Every census is a premise → §2 above; C2/C5-live carried explicitly.
3. A negative test needs its positive vector proven first; every fix proven by reverting it.
4. Removal needs a transport AND an observer — Inc 3 *replaces* the `NumDelivered` branch, so
   enumerate everything it silently did (V18 names one; look for more) and account for each.
5. One deterministic key, one writer — the republish set's key is `(targetID, entityID, col)`.
6. Precedent may carry debt — verify a mirrored pattern against the rule it claims to follow.

### 6. Adjacent finds

- **The third `Decision` switch** (`internal/processor/outbox/consumer_decisions_test.go:28-40`)
  the design's V8 census missed. Absorbed into Inc 1 (it is one `case`), not filed.
- **No `exhaustive` linter.** V8's "a missed site is a silent no-op, not a compile error" is
  therefore permanent, not incidental. Absorbed as a brief gotcha + T1's mutation coverage; a
  standing gate for it is out of this item's ratified scope and, if it proves warranted at the
  close pass, files as its own row.

### 7. Non-goals (the drift fence)

The row sweep (shelved fallback, §11 Row 2); any automatic durable rebuild (per-boot, per-update,
per-reconnect — withdrawn, §11 Row 3); `AckWait` (withdrawn, §6); `MaxDeliver` (untouched, V5);
rows 1/2/4/5/6/7/9/12–17 of §3.2; the `weaver-targets` KV `History: 1` pin itself (T4 *asserts*
it, does not change it); Contract #10 (§10 — no contract surface).

### Scope-diff gate — PASSED

Parts 2–4 diff item-by-item against part 1 with no widening. Two narrowings recorded: the
`decisionName` case (part 6) is an addition *inside* Inc 1's stated "all V8 touch points", not a
new mechanism; and C2/C5-live are carried as a post-deploy gate rather than a build-time stop,
argued in part 2. Declared dependencies re-verified both ways: Inc 2 depends on Inc 1's Decision
value (load-bearing); Inc 3 and Inc 4 do **not** depend on C2 (verified — no code path reads it).
