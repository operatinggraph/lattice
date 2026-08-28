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
| V16 | **A NATS restart strands a quiet Nak'd population.** The redelivery timer (`o.ptmr`) is not persisted; on consumer recovery `setLeader → checkPending` bails at `o.ptmr == nil`, and the only arming sites are `trackPending` (an actual delivery — **one delivery re-arms the timer for the whole pending set**) and `processNak`. `o.rdq` is not persisted either. A lane-1 consumer whose pending set is entirely long-Nak'd rows and which receives no new delivery is never redelivered again until some row under its prefix projects, or the durable is recreated | `consumer.go:5895-5898` (the bail), `:1777`, `:5771-5772`, `:3178`, `:7034` |
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
| 8 | `GapWithoutPlaybook` (`:231`) | error alert + Ack | **error alert + Long** | Config error — a playbook fix produces **no new delivery**, so the loop is the only automatic uptake: picked up within one floor (§3.3). The standing re-raise also survives clear-races (§3.5) |
| 9 | `UnresolvedReference` (`:579`) | paced warning + NakWithDelay | **unchanged** | Genuinely transient mid-convergence; the 5 s class is deliberate |
| 10 | `TemplateDataError` (`:583`) | paced warning + **Ack** | **paced warning + Long** | Sits on the boundary — the fault is template × row, and one of its fix paths (a template/playbook edit) produces **no new delivery** — so the fix-path rule puts it in the config class. Today it Acks once and is never revisited. (~~Plausibly the clinic class itself~~ — struck 2026-08-28: Phase 0's static C2 rules `TemplateDataError` out for `clinicSiteBackfill`, whose `row.entityKey` always resolves; the row's decision is unaffected) |
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

Per the direction, the per-entity clear stays at its site — **narrowed to a column read as an
explicit bool `false`** (V19's `isBool`, threaded out of `boolColumn`): today the clear also fires
for a *non-bool* value — a read that is not evidence of closure — so a repeatedly re-projecting
broken row would clear the target-scoped latch at its projection rate. With the narrowing, the
flap is what the direction accepted: at most one clear per genuinely closing entity, re-raised
within ≤ one long floor by a still-open row's next delivery.

Re-raise conditionality, per code (the adversarial pass's enumeration):

| Code | Re-raises on a decline-loop redelivery? |
|---|---|
| `UnresolvedReference` / `PlaybookConfigError` | **Yes, unconditionally** — raised in `planGap`, reached by every delivery of an open un-suppressed row |
| `GapWithoutPlaybook` | **Conditionally** — the raise site is below the suppression and exhaustion gates, so a target whose *every* remaining open row is in-flight-suppressed or exhausted does not re-raise it. Residual, named: those populations are not dark (an in-flight row is owed by its mark/`reclaim`; an exhausted row carries its own standing exhaustion issue), but the `gapConfig:` latch itself can stay retired while such rows hold the column open |

`since` semantics (V14): `GapWithoutPlaybook` (via `alert`) re-mints `since` on a flap; the two
paced codes keep their original onset across flaps because the pace memory survives `clear`.

### 3.6 The issue-cache bound

With data errors Acking (§3.2's rule), no per-delivery data-error flag exists — the only
mechanical thread is `boolColumn`'s `isBool`-reporting sibling, used solely by the §3.5 clear
narrowing.

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
  rows. The cap is deliberately modest: above ~1 024 pending the server's `checkPending` walks the
  whole map per timer fire and defers under ack load (`consumer.go:5916-5921`, `:5964-5973`).
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
- **T10 (Inc 4, embedded NATS e2e — pins V16):** restart the embedded server under a quiet
  long-Nak'd row; assert no redelivery arrives on its own, then `ReplayTarget` recovers it. The
  test documents the strand and pins the verb as its repair.
- **T11 (Inc 4):** the verb does not re-pay Augur — a row with a standing escalation issue and a
  stale mark replays without a second reasoning dispatch.

---

## 14. Build decomposition for the Steward

Sequential; each independently green. Review depth: **Inc 2 and Inc 3 are posture-changing**
(decline semantics; a retired recovery branch replaced by the republish set) — full adversarial
pass; Inc 1 and Inc 4 standard.

- **Inc 1 — substrate.** `NakWithLongDelay` + `LongRedeliveryDelay` (all V8 touch points). Owns
  T1. Weaver-inert until Inc 2.
- **Inc 2 — decline classes.** §3.2's table + the `isBool` threading + the
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

## 16. Fire brief (build note, 2026-08-28) — the whole item, Inc 1→4

Compiled by the Lattice Steward at selection, from four read-only scouts over
`internal/substrate`, `internal/weaver`, `internal/processor` and the operator-verb layers.
One brief per ITEM: a later fire resuming an unfinished increment runs a delta-scout, not a
recompile.

### 1. Scope sentence (verbatim, §14)

> Inc 1 — substrate. `NakWithLongDelay` + `LongRedeliveryDelay` (all V8 touch points). Owns T1.
> Weaver-inert until Inc 2. · Inc 2 — decline classes. §3.2's table + the `isBool` threading + the
> row-3 raise/clear + the map-level cache bound + `MaxAckPending: 2000` + the §3.5 clear narrowing +
> the §8 severity demotion. Owns T3, T4, T5, T8. Phase 0 runs C1/C2/C5 with C2's stop-rule. ·
> Inc 3 — dispatch-path restructure. Early anti-storm ahead of `planGap`; retire `redelivered`; the
> republish set; the three comment rewrites. Owns T2, T9. · Inc 4 — the `ReplayTarget` verb. Engine
> verb + capability verb + Loupe surface (V20's pattern) + Augur re-fire suppression + the
> contraction/component-doc sentence rewrites. Owns T6, T7, T10, T11.

**Landing shape (§4 requires the doc to state which):** **land each increment on `main`.** Every
boundary is independently green and safe, and the invariant that keeps `main` correct across them is
that **Inc 1 is Weaver-inert** (a new Decision value no handler returns yet) and Incs 2/3/4 each
leave the engine at a complete dispatch posture — Inc 2 without Inc 3 keeps the shipped re-fire
branch, Inc 3 without Inc 4 keeps today's operator verb set. The remote container is ephemeral
(`agents/steward/REMOTE.md` §4), so holding four increments in one unpushed branch is the riskier
shape here.

### 2. Verified touch-list (checked live at 2026-08-28, against `c9a7df1`)

**Premise re-runs (the scope-diff gate's census rule). Every design count re-run live:**

| Census | Design says | Live now | Verdict |
|---|---|---|---|
| C1 — production weaver targets | 26 | **26** (`grep -rn 'TargetID:' --include='*.go' packages/ \| grep -v _test \| wc -l`) | ✅ exact |
| C6 — `substrate.Ack` sites in `evaluator.go` | 21, at 27/34/43/93/115/120/131/201/231/257/552/583/587/636/688/704/812/1344/1359/1388/1398 | **21, same lines, zero drift** | ✅ exact |
| V8 — `applyDecision` call sites | 4 | **4**: `substrate/consumer.go:319`, `:395`, `substrate/consumer_supervisor_pump.go:704`, `:710` | ✅ exact |
| V8 — switches over `Decision` | "two, both `default: → Ack`" (+ handleRow's two aggregation switches, named separately in §3.1) | **five**: the two appliers (`substrate/consumer.go:450`, `processor/commit_path.go:810`), the two weaver aggregators (`evaluator.go:172`, `:182`), **and one the design does not name — `internal/processor/outbox/consumer_decisions_test.go:29`'s `decisionName`** | ⚠️ **corrected**: a fifth switch exists. Benign (a test helper whose fall-through returns `Decision(%d)`, not Ack) but it is a switch a new value must reach, so T1 covers it. |
| C3 — post-preamble `msg.*` reads | `msg.Sequence` + `msg.NumDelivered`, the latter at exactly the retiring branch | **`:172`, `:270`, `:305` Sequence; `:346` NumDelivered** — the only NumDelivered read, and `redelivered` is read at exactly one site (`:633`) | ✅ exact |
| §6 — lane-1's shipped consumer envelope | `MaxAckPending` unset (server default 1000) | **confirmed**: `weaver/engine.go:414-425` `targetSpec` sets neither `MaxAckPending` nor `RedeliveryDelay` | ✅ |

Line drift against the design's citations, all resolved to current lines: `disposeJetstream`
`commit_path.go:850-869` → **`:810-829`**; `planGap` `:575-590` → **`:516-589`** (its `errData`
arm at `:580-583`, default at `:584-587`); `admitGap` → **`:600-614`**, its token consumed inside
`planGap` at **`:528`**; `fireEpisode` **`:629-694`**; `fire` **`:792-813`**;
`escalateExhaustedGap` **`:1325-1407`**; `clearClosedMarks` **`:839-933`**; `boolColumn`
**`:1085-1102`**; `intColumn` **`:1117-1146`**; `reclaim`'s backoff **`reconciler.go:142-162`**.

**Inc 1 — substrate (`NakWithLongDelay` + `LongRedeliveryDelay`)**

| File:line | Edit |
|---|---|
| `internal/substrate/consumer.go:39-57` | append `NakWithLongDelay` to the `Decision` iota (value 4), doc-commented like `NakWithDelay` |
| `internal/substrate/consumer.go:59-65` | add `DefaultLongRedeliveryDelay = 5 * time.Minute` beside `DefaultRedeliveryDelay` |
| `internal/substrate/consumer.go:449-472` | `applyDecision` grows a `longRedeliveryDelay` parameter + a `case NakWithLongDelay` floored at `DefaultLongRedeliveryDelay` **and at the consumer's own `RedeliveryDelay`** (§3.1: "floored at `DefaultRedeliveryDelay` if set lower") |
| `internal/substrate/consumer.go:319`, `:395` | both call sites pass the second floor |
| `internal/substrate/consumer_supervisor_pump.go:704`, `:710` | ditto; `:704` keeps `effectiveProbeInterval` for its own `NakWithDelay` |
| `internal/substrate/consumer.go:108-148` | `DurableConsumerConfig.LongRedeliveryDelay` |
| `internal/substrate/consumer_supervisor_spec.go:152-176` | `ConsumerSpec.LongRedeliveryDelay` |
| `internal/processor/commit_path.go:810-829` | explicit `case substrate.NakWithLongDelay` (today's `default:` would silently Ack) |
| `internal/processor/outbox/consumer_decisions_test.go:29-38` | the fifth switch — add the name |
| `internal/substrate/nak_with_delay_test.go:13-26` | extend the iota pin (`NakWithLongDelay != 4` → red) |

**Inc 2 — decline classes.** `internal/weaver/evaluator.go` rows 3/8/10/11 at `:43`, `:231`,
`:583`, `:587`; both aggregation switches `:172-178`, `:182-188` gain the explicit case with
precedence `Nak > NakWithDelay > NakWithLongDelay > Ack`; `boolColumn:1085-1102` threads its
existing `isBool` local out; `clearClosedMarks:853` narrows its clear to an explicit-bool-false
read; severity `"error"` → `"warning"` at `:228` (`alert`) and `:585` (`alertPaced`);
`weaver/engine.go:414-425` `targetSpec` gains `MaxAckPending: 2000` + `LongRedeliveryDelay`;
`internal/weaver/health.go` (`issueCache` `:130-135`, `set` `:147`, `snapshot` `:303-316`) gains the
per-target per-family map cap.

**Inc 3 — dispatch restructure.** `evaluator.go` `dispatchGap:204-347` (mark read + `found &&
!stale` early Ack ahead of `planGap:305`), `fireEpisode:629-694` (`redelivered` parameter deletes),
`fire:792-813` (republish-set insert on the `:800` Nak, removal on success), the three comments at
`:190-193`, `:776-778`, `reconciler.go:1126-1128`.

**Inc 4 — the verb.** Nine layers, verified end to end against `ResetRetryBudget`:
`weaver/control.go` (engine method + not-registered check, mirroring `:341-343`);
`weaver/control/service.go` (`engineControl` iface `:26-37`, `ControlResponse` `:61-69`, op const
beside `opResetBudget` `:128`, `targetOps` `:138`, `dispatchEndpoint` switch `:317-362`);
`internal/controlauth/ops.go:23-30` `WeaverOps`; `packages/console-operator/permissions.go:58` +
`manifest.yaml` (+ **version bump**, `package.go`'s mirroring constant) + `package_test.go:154`;
`internal/controlauth/checker_test.go:175` (the lock-step wiring test);
`cmd/loupe/control.go:57` `mutateOps`; `cmd/loupe/web/js/views/weaver.js`;
`internal/substrate/consumer_supervisor.go` `Reset`/`ResetAwaitReopen` `:197-261`, `resetMu`
`:41-62`.

### 3. Precedents to mirror

- New Decision value → **`NakWithDelay` itself** (`consumer.go:39-57`, `:59-65`, `:449-472`) — the
  same append-at-the-end + package-default + floor-fallback shape, pinned by the same test.
- Env-clamped tunable → **`weaver/engine.go:157-184`**'s sweep-interval clamps (zero → default,
  invalid → `logger.Warn` + clamp).
- The operator verb → **`ResetRetryBudget`** end to end (the `057286f` un-park fire), all nine
  layers above; its tests `control_internal_test.go:891-1043` and
  `control/service_test.go:357-413` are the fixture shapes.
- Per-target durable delete-then-create → **`substrate.ConsumerSupervisor.Reset`/`ResetAwaitReopen`
  under `resetMu`** (`consumer_supervisor.go:197-261`) — already the machinery `Revoke` and the
  registry use; no new mechanism.
- The issue-map cap → **`boundIssues`** (`health.go:616-637`) and `installer.go`'s
  `sampleWithOverflow`, per the dossier entry that minted `boundIssues`.

### 4. Increment order + runnable green checks

Each increment: build → vet → lint → its own tests → full suite → commit → CI.

```sh
export PATH="$(go env GOPATH)/bin:$PATH"          # golangci-lint v2.11.4, REMOTE.md §7
export POSTGRES_TEST_DSN="postgres://lattice:lattice_dev@127.0.0.1:5433/lattice?sslmode=disable"
go build ./... && make vet && golangci-lint run ./... && STRICT=1 go run ./scripts/lint-conventions.go
# Inc 1
go test -count=1 ./internal/substrate/... ./internal/processor/...
# Inc 2
go test -count=1 ./internal/weaver/...                      # T3, T4, T5, T8
# Inc 3
go test -count=1 ./internal/weaver/...                      # T2, T9
# Inc 4
go test -count=1 ./internal/weaver/... ./internal/controlauth/... ./cmd/loupe/... \
  ./packages/console-operator/...                            # T6, T7, T10, T11
DIFF_BASE=origin/main go run ./scripts/lint-package-version.go   # console-operator bump
# every increment, before commit — the whole tree plus the build-tagged harnesses the
# Decision-interface change reaches (CLAUDE.md: `go test ./...` is NOT the whole gate set)
go test ./... -p 4
make test-control-plane-authz && make test-unrouted-convergence && make test-augur-convergence
```

### 5. In-scope gotchas

- **`packages/console-operator` content edit ⇒ bump `manifest.yaml`'s version AND the `Version`
  constant mirroring it** (CLAUDE.md), verified by `lint-package-version.go`.
- **A Decision-enum change reaches build-tagged harnesses** that `go test ./...` never compiles —
  `make test-control-plane-authz` in particular drives a real `weaver control.Service` round-trip
  and is the gate Inc 4's transport layer must pass.
- **No frozen-contract change** (§10): Contract #10 §10.8's liveness bullet already promises this.
  If the build falsifies that, the contract edit becomes a branch commit per `REMOTE.md` §2 — it is
  not a reason to stop.
- **`docs/components/weaver.md` is updated in the same increment** as the behaviour it describes —
  the lane-1 decline classes (Inc 2), the `Enable` sentence (Inc 3/4), and `contraction.go:20-25`'s
  currently-false restart sentence (Inc 4).
- **Health-emission changes update the canonical Health-KV schema doc in the same change**
  (`agents/steward/SKILL.md` §4) — Inc 2 adds a `RowDataError` raise at a synthetic `body` column
  and demotes two codes' severity.

**Weaver's "Review keeps catching" dossier — the entries this fire trips, copied in verbatim
(`docs/components/weaver.md:970-1095`):**

- **A Health issue key is a LATCH: scope it to the fact it states, and split it only with every
  clear re-paired.** *Before adding a CLEAR, enumerate every OTHER leg that raises at that key* — a
  clear one leg believes against a raise another believes does not settle: the latch flaps,
  re-stamps its `since`, and defeats arrival-vs-repeat damping. Check: enumerate every raise and
  every clear — grep the family's key CONSTRUCTOR, not only the file you are editing — assert each
  raise still reaches each clear it had, and pin two entities on one column.
  **→ binds Inc 2's row-3 `issueKeyDataEntity(…, "body")` raise/clear AND the §3.5 narrowing.**
- **Segmenting a Health key by entity is safe only where a clear site names that exact COLUMN —
  enumerate the raise COLUMNS, not the raise functions.** A shared reader (`boolColumn`/`intColumn`)
  raises for whatever column its caller passes; six columns flowed through those two readers and one
  had a clear. The issue cap bounds the DOCUMENT, not the cache.
  **→ binds Inc 2's map-level cap and the new synthetic `body` column.**
- **A per-entity Health issue is unbounded, and the heartbeat is ONE KV value.** Aggregate status
  over ALL issues, then bound the listing, and select by SEVERITY, never key order.
  **→ binds Inc 2's §3.6 cap: it must not disturb `boundIssues`' honest total.**
- **An `error`-severity Health issue must not fire on a self-healing condition.**
  **→ this is exactly §8's demotion; assert no `error` remains at either raise site.**
- **A leg's arms are a lattice, not a list: every RETIRE belongs above every "cannot act" GUARD.**
  **→ binds Inc 3's move of the mark read/anti-storm ahead of `planGap` — the moved block must not
  strand a retire below a guard.**
- **A gap class is decided by the dispatch's SHAPE, never by its action name; a NEW dispatch seam
  inherits that classifier and the pacing built on it.** A mark's ABSENCE is not evidence the
  episode concluded.
  **→ binds Inc 3's republish set (a new re-fire seam) and Inc 4's replay (re-delivering rows whose
  marks may be live, stale, or gone).**
- **An operator verb that hands a gap to a reconciler arm must refuse exactly what that arm
  PERMANENTLY declines** — TRANSIENT declines the verb must accept, PERMANENT ones it must refuse
  and NAME. Minted four times on one verb.
  **→ binds Inc 4: `ReplayTarget` must refuse an unregistered target (design §3.3) and an unmanaged
  consumer (`"reset %q: not managed"`), each with its own message.**
- **A shared test fixture that always supplies an OPTIONAL input pins only the supplied case.**
  **→ binds every new T-test's fixture.**
- **Prove each changed line by reverting THAT LINE, not the feature — and where the claim is about
  WHERE a block sits, the mutation is a MOVE, not a revert.**
  **→ binds Inc 3's early-anti-storm proof specifically: revert proves nothing, the block must be
  MOVED back past `planGap` and the test must red.**
- **A fact ends by more routes than the one you are editing — enumerate the LEGS, not just the
  verb.** The issue families are prefix-keyed below the target; a route that clears only the key it
  owns strands every per-entity entry.
  **→ binds Inc 2's `body` key: it lives in the `data:` prefix family, so it inherits `Revoke`'s and
  `reconcileConsumers`' prefix clears — verify, don't assume.**

**Standing checklist (`agents/fire-brief-template.md`), all six live here:**
(1) the **republish set** and the **issue-map cap** are new state → each needs its state table
written before it is built (§5 has the republish set's; the cap's must be added);
(2) every census above was re-run live — one came back wrong (the fifth switch);
(3) each T-test's positive vector proven before its negative;
(4) **the retiring `NumDelivered` branch is a REPLACEMENT, not a deletion — enumerate everything it
was silently doing** (V18 names one job; C3 + the three comments are the enumeration, and the
adversarial pass must look for a second);
(5) one deterministic key, one writer — the republish set's key is `(targetID, entityID, col)`, the
same tuple the mark owns, so its arbitration with `clearClosedMarks` must be explicit;
(6) precedent may carry debt — `ResetRetryBudget` is the mirror **and it is itself incomplete**
(see part 6).

### 6. Adjacent finds

- **`resetBudget` is unreachable from Loupe.** `cmd/loupe/control.go:57` lists
  `mutateOps: setOf("disable", "enable", "revoke")` — `resetConfidence` and `resetBudget` exist in
  the engine, the transport, `controlauth` and `console-operator`, but Loupe's allow-list refuses
  them, so `weaver.md`'s "surfaced in Loupe" is false for both. **Absorbed into this run's batch**
  as part of Inc 4 (the same two lines that admit `replayTarget`), not filed.
- The design's V8 switch count was one short (part 2's table). **Fixed in Inc 1**, and this
  brief amends the doc's own §3.1 touch list.

### 7. Non-goals (the drift fence)

No automatic rebuild of any kind (per-boot, per-reconnect, per-update) — withdrawn by Andrew's
correction. No row sweep / declared-work enumerator (the shelved fallback). No `Term` for the
data-error class (§3.2 rejects it). No `AckWait` change (§6 withdrew it). No `MaxDeliver` bound
(V5's posture). `Enable` stays plain Resume. The unregistered-target exit stays Ack (§4.2). No
change to marks / OCC / idempotency / the sweep's legs.

### §12 Phase-0 censuses — run 2026-08-28, and C2's stop-rule adjudicated

**C1 — 26 production weaver targets.** Exact (`grep -rn 'TargetID:' --include='*.go' packages/ |
grep -v _test | wc -l`). **C6 — 21 `substrate.Ack` sites**, at the 21 line numbers §12 lists, zero
drift. **C3** narrows as predicted. All three re-run live against `c9a7df1`; the V8 switch count did
not hold (§16 part 2).

**C2 and C5 name a LIVE STACK this fire does not have.** The fire ran in a Claude Code remote
container (`agents/steward/REMOTE.md`): the stack there is fresh and empty, so no read of it can see
the production corpus C2 and C5 are about. C2 was therefore answered **statically, from the code and
the packages** — which turns out to answer it *better* than the live read would have, because the
question is which code path the rows took, and that path is in the repo.

**C2's answer: today's evaluator declines a `clinicSiteBackfill` violating row at NO exit — it
dispatches.** Traced end to end:

- `internal/refractor/ruleengine/full` does **not** implement three-valued `NULL` for `=`:
  `visitor.go:745-746` parses the `null` literal to a Go `nil`, and `values.go:117-120`, `:143-146`
  route `=` through `equalsAny`, which special-cases a nil on either side into equality-of-nilness.
  So `(site.key = null)` is an `IS NULL` check returning a **genuine Go bool** — `true` on the
  OPTIONAL MATCH miss, `false` when the link is there. Never `nil`.
- `adapter/natskv.go`'s `upsert`/`guardedBody` (`:218`, `:470-484`) marshal the row map flat, so the
  bool lands in the KV row JSON as `true`/`false` under its own key.
- Therefore `boolColumn` (`evaluator.go:1085-1102`) always takes its genuine-bool branch for
  `violating` and `missing_site` — neither the absent/nil clear-to-false branch nor the
  `RowDataError` branch is reachable for this lens.
- `entityKey` comes off the mandatory `MATCH` anchor (`lenses.go:770-771`), so `:131` cannot fire;
  `targets.go:17-40` declares `Gaps["missing_site"]` today, so `:231` cannot fire;
  `strategist.go:610-675` resolves `row.entityKey` cleanly, so neither `:583` nor `:587` can fire;
  `package.go:144` wires `WeaverTargets()` unconditionally, so `:34` cannot fire.

**So the 26-of-28 fact is not a live decline branch at all — it is a HISTORICAL decline made
permanent by the Ack.** Lane-1's durable is stable-named `DeliverLastPerSubject` with no per-boot
nonce, so an Acked row is never redelivered; the KV row's content has not changed since (no
re-projection ⇒ no new CDC message), and it has sat unevaluated ever since — regardless of the
playbook now being correct. The leading class for that one-time decline is `GapWithoutPlaybook`
under a package version predating `targets.go`'s `Gaps["missing_site"]` entry, which is exactly
what the held design's post-review note already recorded
([weaver-sweep-declared-work-enumeration-design.md](weaver-sweep-declared-work-enumeration-design.md):44-46).

**Stop-rule verdict: PASS, and the reading matters.** `GapWithoutPlaybook` is §3.2 **row 8**, which
this design moves to Long — not a row the table leaves at Ack (1/2/4/5/6), so Phase 0 does not stop.
But the corollary is sharper than the census expected: the Nak loop **cannot** reach this
population, because these rows are already Acked and will never be delivered again. §3.3's first
named job — *"the pre-existing Acked-decline residue … one verb invocation per affected target after
deploy"* — is therefore not a nice-to-have tail of this item. **It is the only thing that heals the
clinic 26, and the verticals row this item blocks stays blocked until `ReplayTarget clinicSiteBackfill`
is actually RUN against the live stack.** Incs 1–3 make the class never accumulate again; Inc 4 plus
that one operator run is what closes the existing damage. The run is live-stack work and this
container has no such stack — it is the item's one carried-forward action, and the board row says so.

**Residual, stated rather than assumed:** what a live C2/C5 would still add is the row *count* per
target and the per-target max/gaps-per-target that size §6's `MaxAckPending: 2000` and §7's
steady-state formula. Both are sizing inputs, not correctness inputs, and §6 already prices 2 000 at
~70× the worst observed stuck population; §12 C5's own re-derive trigger ("if a target exceeds
~2 000 rows, re-derive §6") stands as the check to run when a live stack is next available.

**§3.2 row 10's body claim is amended (2026-08-28, the falsified-claim rule).** The row's *"Plausibly
the clinic class itself"* aside is **wrong** and is struck: the trace above rules `TemplateDataError`
out for `clinicSiteBackfill`'s actual shape — `row.entityKey` always resolves. Row 10's *decision*
is unchanged and stands on its own stated grounds (the fault is template × row, and one of its fix
paths produces no new delivery, so the fix-path rule puts it in the config class); only the
speculative attribution to the clinic population is withdrawn.
