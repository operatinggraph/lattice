# Weaver — separate the WORKLOAD population from the FAULT population in the issue cache

**Status: 📐 awaiting-Andrew (ratification)** · Designer fire 2026-09-01 · Lattice lane
Board row: *[Weaver] The per-row Health budget admits by ARRIVAL, so a routine `surface` family evicts
non-re-derivable facts* (★★ · M).

---

## For Andrew

**What it does, in two lines.** Weaver's per-target 500-slot issue budget is shared by two populations
with different cardinality laws: `surface` gap entries, one per *open row of business work*, and every
other per-row entry, one per *broken row*. A healthy backlog of 500 unclaimed tasks fills the budget and
every fault raised afterwards for that target is refused. This design takes the workload population out
of the issue cache entirely — a `surface` gap raises **one** issue per (target, gap column) carrying the
count of rows holding it open — so the budget bounds only the fault families it was sized for, and then
repairs the two log seams a refusal currently inverts.

**One judgement call, and it is yours.** Inc 1 trades **per-row identity in Health-KV for a surface gap**
for a per-column count. My recommendation is to take the trade: the identity is not lost, it is
*relocated to where it is already authoritative* (the projected row set the surface gap is computed
from), the only Health-side consumer already fails past 50 entries, and Loupe's entity page already
renders the same fact from the row itself (§7.2). But "which rows are awaiting a decision" is a product
question about what an operator reads on the Weaver page, so it is flagged rather than adjudicated.

**Frozen-contract change: yes, one clarifying sentence, staged UNCOMMITTED in `main`.**
`docs/contracts/10-orchestration-weaver.md` §10.8, the `surface` action row. Today it reads
*"raises a Contract #5 §5.5 `issues[]` entry keyed `issueCode`"* — singular, and on my reading already
per gap column. The edit makes the cardinality explicit (one entry per target + gap column, carrying the
count of rows currently holding the column open) so that what Inc 1 changes is a promise a reader can
check rather than an implementation detail nobody wrote down. Affected consumers: §6.

**No architectural fork.** No new engine, no new plane, no Core-KV read, no lens. The one alternative
that would have been a fork — a re-derivability-ranked admission policy, which is what the board row
prescribed — is **refuted** in §8, on the row's own scenario.

---

## 1. Problem + intent

### 1.1 What the row said, and what grounding changed

The row was filed by the `weaver-decline-retry-substrate-native` close pass
(`_bmad-output/implementation-artifacts/weaver-decline-retry-substrate-native-design.md:1327-1344`),
which stated the trade it was shipping rather than discovering it later. That filing was right about the
mechanism and wrong about two of its consequences. Both corrections are load-bearing, so they are stated
first.

| The row's claim | Verdict | Evidence |
|---|---|---|
| `rowIssueCapPerTarget` = 500 is **one** per-target budget over `gap:`/`data:`/`template:`/`sweep:`, admission-ordered | **Confirmed** | `health.go:62`; `rowIssueTarget` `evaluator.go:2075-2093`; `setSince` `health.go:236-257` |
| Released only when the target's per-row set reaches **zero** | **Confirmed** | `releaseRowIssueLocked` `health.go:282-291` |
| A high-volume `surface` family fills it | **Confirmed**, and this is the whole mechanism | `evaluator.go:316-334`; §3 C1 |
| A later `RowDataError` is refused and lost | **Confirmed** for the `data:` family and for `sweep:` `CorruptMark` | `evaluator.go:55-58`, `:101`; `temporal.go:120,137,158`; `reconciler.go:1152-1160` |
| …**"voiding §10.8's exhaustion-raise for that target"** | **FALSE** | `GapBudgetExhausted` is re-derived every sweep pass — `escalateExhaustedGap` is called from `sweepCount` (`reconciler.go:634`) and `reclaim` (`reconciler.go:943`), not only from lane-1 (`evaluator.go:195`), and `defaultSweepInterval` is one minute (`reconciler.go:21`). A refused exhaustion raise is **delayed, not lost**: it is re-attempted every minute and lands whenever a slot frees. Contract #10-substrate:189-191 says as much in the contract text — the fact is *"re-derived for as long as the suppression lasts"*. |
| "…**evicts** non-re-derivable facts" | **FALSE as worded** | `setSince` never evicts a tracked entry to admit a new one; it refuses the *new* key (`health.go:238-252`). There is no eviction anywhere in the cache. |

The board row is corrected as part of this fire (§10).

### 1.2 The defect, restated

The cap is a **memory-and-sort** bound — its own doc says so: *"the value's real job is to keep a
pathological target from making every heartbeat's sort proportional to its row count"* (`health.go:56-61`).
It was then given a **semantic** job it cannot do, because the population it bounds is not one population:

- **workload entries** — the `surface` arm (`evaluator.go:316-334`). One entry per open row. Population =
  the target's *open business backlog*. On a healthy system this number is large and is supposed to be.
- **fault entries** — everything else in `gap:`/`data:`/`template:`/`sweep:`. One entry per broken row.
  Population = what is *wrong*. This is the population 500 was sized for.

Mixing them means a healthy backlog starves fault reporting. `orchestration-base`'s `unroutedTasks` is a
single-gap-column, surface-only target (`packages/orchestration-base/targets.go:44`), so **500 unclaimed
role-queued tasks — no fault at all — fill the entire budget for that target.** `leaseApplicationComplete`
carries three surface columns (`packages/lease-signing/targets.go:140-149`), so ~167 concurrently-stuck
applications reach the same place.

### 1.3 The three consequences, per seam

Once a target is at cap, a refusal does not merely drop a Health entry — it *inverts the loudness
discipline* of the seam that raised it, differently for each seam, because every seam decides its log
level from cache state the cap has just made unreachable.

| Seam | Family it serves | Designed behaviour | Behaviour on a refused key | Net |
|---|---|---|---|---|
| `issues.set` bare (`evaluator.go:331`) | `gap:` surface | Health entry, **no log at any level** | nothing, in either plane | **silent** |
| `issues.set` after a `Warn` (`evaluator.go:55`, `:101`; `temporal.go:120`, `:137`) | `data:` | Warn + latch | Warn on that one delivery, then Ack — nothing re-raises | fact survives only in the log, for one line |
| `alert` (`temporal.go:158`; `reconciler.go:1159`) | `data:` error, `sweep:` | Error + latch, every call | Error survives; latch lost. `deleteCorrupt` has already **deleted** the corrupt key, so nothing recreates the fault | fact survives only in the log |
| `alertStanding` (`evaluator.go:1572`) | `gap:` `GapBudgetExhausted` | arrival at **Error**, continuation at **Debug** (`evaluator.go:1775-1781`) | `standingAs` is false for a key the cache never tracked, so **every** re-derivation logs at Error — one per sweep pass, ~1/min, for the life of the retry budget (≈128 h at the defaults) | **permanent Error flood**, arriving exactly when the target is worst off — the flood `alertStanding` exists to prevent |
| `alertPaced` (`evaluator.go:708`) | `template:` | loud on arrival, then one loud record per `logPaceInterval` (1 h) | `pacedRaise` returns `loud=false` unconditionally for a refused key (`health.go:381-389`), so the fault logs at **Debug forever** | **permanent silence** in both planes |

And a fourth, in the overflow entry itself: `c.refused[target]` counts **raises**, not distinct facts
(`health.go:241`), so under the `alertStanding` row above it grows by one per minute per refused gap and
never decreases until the target's whole per-row set drains. The entry's message — *"N further raises for
rows outside the tracked set were not recorded, and are not re-derivable until those rows project
again"* (`health.go:246-251`) — is therefore a cadence counter wearing a backlog number, and its
"not re-derivable" half is **affirmatively false** for the `template:` and exhaustion facts.

### 1.4 What is NOT broken (so the design does not go looking for it)

- **The aggregate verdict is honest.** A refused raise folds its severity into `refusedWorst`
  (`health.go:242-245`) and the overflow entry carries it, so `aggregateStatus` (`health.go:1099-1113`)
  reaches the same `degraded`/`unhealthy` verdict with or without the refused facts. An operator is never
  told a broken target is healthy.
- **The overflow entry is not truncated away.** `listingRank` pins it at tier 1, ahead of the per-entity
  families whose volume caused it (`health.go:944-968`).
- **`ReplayTarget` is the ratified recovery verb** for an Acked-and-declined row
  (`internal/weaver/control.go:392-473`), and it re-presents the target's whole current row set.

---

## 2. Grounding ledger

Every row is the code that *does* the thing, never a comment that describes it.

| Fact | Where |
|---|---|
| The budget constant, 500, and its stated job | `internal/weaver/health.go:46-62` |
| Admission decision + refusal + severity fold | `internal/weaver/health.go:236-257` |
| Release only at zero | `internal/weaver/health.go:282-291` |
| The separate pace budget, and its unconditional not-loud on refusal | `internal/weaver/health.go:372-389` |
| The family membership test — key SHAPE, two separators below the target | `internal/weaver/evaluator.go:2034-2093` |
| Family prefixes | `internal/weaver/evaluator.go:1836-1846` |
| The surface arm: raise + `Ack`, no log | `internal/weaver/evaluator.go:316-334` |
| Surface has no mark and no `__count`, so no sweep leg revisits it | `internal/weaver/evaluator.go:585-587`; `reconciler.go:701-707` |
| Exhaustion raise, and its three call sites | `internal/weaver/evaluator.go:1572`; `reconciler.go:634`, `:943`; `evaluator.go:195` |
| Sweep cadence = 1 minute | `internal/weaver/reconciler.go:21` |
| `alertStanding` arrival test | `internal/weaver/evaluator.go:1775-1781` |
| `alertPaced` loudness | `internal/weaver/evaluator.go:1819-1830` |
| Lane-1 consumer: `DeliverLastPerSubject`, `MaxAckPending` 1024, no explicit `AckWait` (30 s default) | `internal/weaver/engine.go:477-490`, `:49`; `internal/substrate/consumer_supervisor_pump.go:715` |
| Long redelivery floor = 5 min | `internal/substrate/consumer.go:86` |
| Surface entry retirement legs | `retireClosedGapIssues` `evaluator.go:1191-1194`; `clearClosedMarks` `evaluator.go:989`; prefix teardowns `evaluator.go:1911-1919` |
| Shared latch: Surface / GapBudgetExhausted / GapEscalatedToAugur all sit at `issueKeyGapEntity` | `internal/weaver/evaluator.go:1526-1560` (the `surfaceOnlyGap` guard exists *because* of this) |
| Listing tiers, and the test that forces a new family to be classified | `internal/weaver/health.go:944-1002` (`TestListingRank_EveryIssueFamilyIsClassified`) |
| Document cap = 50, severity-first, honest truncation entry | `internal/weaver/health.go:36`, `:921-942` |
| Loupe's per-entity issue attribution — substring match on the issue MESSAGE | `cmd/loupe/weaver.go:396-429`, called at `:1128` |
| Loupe's entity page already renders a surface gap's `open` state from the row | `cmd/loupe/weaver.go:1109-1119` |
| Loupe deliberately does NOT duplicate the Health issue into `/api/tasks` | `_bmad-output/implementation-artifacts/loupe-f17-queue-observability-ux.md:56` |
| `contractionStats` — the shipped precedent for a per-(target, entity) membership set with an honest lower-bound posture | `internal/weaver/contraction.go:18-76` |
| §10.8 `surface` clause | `docs/contracts/10-orchestration-weaver.md:167` |
| §10.8 exhaustion promise, and that it is re-derived | `docs/contracts/10-orchestration-weaver.md:71-72`, `:345-346`; `docs/contracts/10-orchestration-substrate.md:189-191` |
| Contract #5 §5.5 issue record + "resolved is simply absent" | `docs/contracts/05-health-kv.md:88-110` |

---

## 3. Censuses (run in this fire; re-runnable at Phase 0)

**C1 — every `surface` gap declaration in the package corpus, and its cardinality law.**

```sh
grep -rn 'Action: *"surface"' packages/ --include="*.go" | grep -v _test
```

Expected: **7** declarations across 4 packages, and **7 of 7 are workload-shaped** — their population is
open business work, never a fault:

| Package · file | Gap column | `issueCode` | Population |
|---|---|---|---|
| `orchestration-base/targets.go:44` | `missing_claim` | `UnroutedTasks` | open role-queued tasks past `expiresAt` |
| `orchestration-base/targets.go:54` | `missing_completion` | `StaleAssignedTask` | assigned-and-uncompleted tasks |
| `lease-signing/targets.go:140` | `missing_decision` | `LeaseDecisionAwaiting` | applications awaiting a landlord decision |
| `lease-signing/targets.go:140` | `missing_manager` | `LeaseUnitUnmanaged` | units with no manager |
| `lease-signing/targets.go:149` | `missing_onboarding` | `LeaseOnboardingAwaiting` | onboardings in flight |
| `privacy-base/targets.go:193` | `missing_vaultDestruction` | `ErasureVaultKeyNotDestroyed` | erasures mid-flight |
| `privacy-base/targets.go:198` | `missing_projectionNullify` | `ErasureProjectionsNotNullified` | erasures mid-flight |

The discriminator is not a hand-enumerated code list — it is **`ga.Action == actionSurface`**, which is
platform-owned and decided at the raise site (`evaluator.go:316`). `issueCode` is package-supplied and
therefore could never have been the discriminator; C1 exists to show that the platform-owned one and the
semantic one agree across the whole shipped corpus.

**C2 — every consumer of the per-ENTITY surface issue.** Per the removal-census rule this is per-entity,
includes tests, docs and examples, and classifies each hit.

```sh
grep -rn "UnroutedTasks\|StaleAssignedTask\|LeaseDecisionAwaiting\|issuesNaming" \
  --include="*.go" --include="*.md" --include="*.js" --include="*.html" . | grep -v '^./_bmad-output'
```

| Hit | Class | Survives Inc 1? |
|---|---|---|
| `cmd/loupe/weaver.go:1128` `d.Issues = issuesNaming(hbs, entityID)` | **the one live per-entity consumer** — matches the entity id as a whole word inside the issue *message* | Loses the surface entry. §7.2 prices it. |
| `cmd/loupe/weaver.go:184`, `:347` | reads `issues[]` by code/severity/message | yes — code and severity unchanged |
| `internal/unroutedconvergence/unroutedconvergence_test.go:247-252` | e2e: `hasIssue("UnroutedTasks")` / `issueCleared(...)` — asserts on the **code only** | yes, unchanged (build tag `unroutedconvergence`, `make test-unrouted-convergence`) |
| `internal/weaver/evaluator_internal_test.go:1303-1332`, `:1471`, `:1577` | asserts the entry at a per-entity key | **must be migrated** — owned by Inc 1 |
| `internal/weaver/replay_internal_test.go:388-416` | `standingAs` at a per-entity key | **must be migrated** — owned by Inc 1 |
| `internal/weaver/decline_retry_internal_test.go:1142-1163`, `health_internal_test.go:423` | cap/listing stress fixtures that *use* surface codes at per-entity keys as filler | **must be migrated** — Inc 1 re-seeds them with a fault family, which is what they are actually testing |
| `cmd/loupe/web_logic_weaver_test.go:203` | renders a `surface` gap's action | yes |
| `internal/pkgmgr/orchestrationguard_test.go:176`, `:189` | validates the *spec* shape | yes |
| `docs/observability/health-kv-schema.md:945-1039` | schema documentation | **must be updated** — Inc 1 |

**C3 — every raise site counted against the budget, with its re-derivability.** The table in §1.3 is that
census; it was derived by enumerating `issueKeyGapEntity` / `issueKeyDataEntity` / `issueKeyTemplateEntity`
/ `issueKeySweep` raises and reading the `substrate.Decision` on each path plus the sweep legs that
re-enter independently of delivery:

```sh
grep -rn "issueKeyGapEntity\|issueKeyDataEntity\|issueKeyTemplateEntity\|issueKeySweep" \
  internal/weaver/*.go | grep -v _test | grep -E "issues\.set|setSince|e\.alert"
```

Expected: 11 raise sites. **Re-derivable: 3** (`template:` on the 5-min long floor; the two exhaustion
codes on the 1-min sweep). **Lost on refusal: 8** (all of `data:`, `sweep:` `CorruptMark`, and `surface`).
`state.go`, `augur_dispatch.go`, `contraction.go` and `oscillation.go` raise nothing in these families.

**C4 — the cap's reachability, in entities.** Not a grep; the arithmetic the design rests on. One
violating entity holds one slot per open gap column. So a single-surface-column target needs **500**
concurrently-violating entities to fill the budget (`unroutedTasks`, `staleAssignedTasks`), and
`leaseApplicationComplete` needs **~167** entities holding all three surface columns open. No production
telemetry in the repo records an observed count; the claim this design rests on is the *shape* — that the
number tracks open work rather than faults — not a measured value.

---

## 4. The shape

### 4.1 Increment 1 — a `surface` gap raises one issue per (target, gap column)

**Today.** `evaluator.go:331` writes `issueKeyGapEntity(targetID, entityID, col)` — `gap:<t>.<e>.<col>` —
one entry per open row, each counted against the 500-slot budget, each carrying the message
`"target T entity E: row column C is true"`.

**After.** The surface arm records the entity in a per-(target, column) **member set** and writes one
issue at a new target-scoped key:

- key `gapOpen:<targetId>.<gapColumn>` — a **new family prefix**, `issuePrefixGapOpen`, added beside the
  nine in `evaluator.go:1836-1846`.
- code and severity: the package's declared `issueCode` / `issueSeverity`, unchanged.
- message: names the target, the column and the **count of rows this engine instance currently observes
  holding it open** — e.g. `target unroutedTasks: 137 rows have column missing_claim true`.
- the entry is written when the set is non-empty and retired when it empties.

Four properties fall out of the key shape and need no new code:

1. **It is not counted against the budget.** `rowIssueTarget` (`evaluator.go:2075-2093`) requires two
   separators below the target; `gapOpen:<t>.<col>` has one. The workload population leaves the budget by
   construction, not by a new exemption — the same test that already excludes the `__capped` overflow entry.
2. **The shared-latch collision disappears.** Surface, `GapBudgetExhausted` and `GapEscalatedToAugur`
   currently occupy one latch, which is why `escalateExhaustedGap` needs the `surfaceOnlyGap` bail
   (`evaluator.go:1526-1560`). After Inc 1 they are three separate keys. **The guard stays** — its
   *dispatch* reason is independent and still correct (a surface gap has no remediation chain, so its
   stranded count must not fire an Augur episode) — but its *latch* reason is dead, and the increment
   rewrites that half of its comment rather than leaving a false model of what holds the invariant up.
3. **The listing improves.** `perEntityIssueFamily` must classify the new prefix deliberately or
   `TestListingRank_EveryIssueFamilyIsClassified` fails (`health.go:991-1002`). It is classified as
   **target-scoped (tier 2)** — one entry that *explains* a backlog, ranked ahead of the per-row entries
   that merely count faults, which is exactly the split that function's doc comment says the tiers are for.
4. **Memory and the per-heartbeat sort both fall.** A `struct{}` set entry replaces a `healthIssue` with a
   ~60-byte message and a `since` string, and none of the set participates in `snapshot()`'s sort
   (`health.go:504-517`) or `boundIssues`' second sort.

**The member set mirrors `contractionStats` exactly** (`internal/weaver/contraction.go:18-76`), and its doc
comment is the reason to mirror it rather than an appeal to consistency: it already answers, for the same
population, the question this set must answer. Reading it (per the mirrors-X rule) supplies three rules the
new set inherits verbatim — only a *transition* changes the count, so a CDC redelivery of an unchanged row
is a no-op; a row is admitted only once observed open, never on a first non-open sighting; and the count is
an honest **lower bound** after a restart, because a lane-1 durable that survives resumes from its acked
floor and `ReplayTarget` is the verb that re-derives it in full. That last point is not a regression: the
per-entity entries it replaces have the identical restart residual, already documented as such
(`weaver-decline-retry-substrate-native-design.md:1346-1353`).

**Raise and retire ride the call sites that already exist.** The raise is `evaluator.go:331`'s own line.
The retirements are `retireClosedGapIssues` (`evaluator.go:1191-1194`, reached from `clearClosedMarks`
when *this* entity's column goes false or the row is tombstoned) and the two prefix teardowns
(`issueKeyTargetPrefixes`, `evaluator.go:1911-1919`, for a target leaving by Revoke or by
`reconcileConsumers` removal). Each becomes a set-remove; the issue entry is retired by whichever remove
empties the set. **The per-entity write and its clears are REMOVED, not left beside the new one** — a
second mechanism next to the first would leave the budget filler exactly where it is.

### 4.2 Increment 2 — a refusal must not invert a seam's loudness

Inc 1 stops the budget being reached by healthy work. It does not change what happens when a genuinely
broken target fills 500 fault slots, and there the log is the only surviving plane — so the log must
behave. Three changes, all small, all at the seam rather than in the cache:

1. **`setSince` and `pacedRaise` report the refusal.** Both already take the decision; neither tells the
   caller. Return it.
2. **`alertStanding` treats a refused key as standing.** Not tracked ⇒ `standingAs` is false ⇒ Error today,
   forever, once a minute. The rule that restores the intent is: a refused raise is loud **once per
   (target, family) per `logPaceInterval`**, and Debug otherwise. The arrival is still heard; the flood
   ends. This needs one small counter beside `refused`, keyed the same way — bounded by targets, not rows.
3. **`alertPaced` paces a refused key on the same (target, family) clock** rather than returning
   `loud=false` unconditionally. Today a refused `template:` fault is Debug for the life of the process;
   after, it is audible at least once an hour, which is what the seam promises everywhere else. Note the
   pace budget is a *second* 500-slot budget (`rowPaced`) released only by `prunePaced`/`clearPrefix`
   (`health.go:429-444`) — Inc 1 empties it of workload entries too, since the surface arm never used it.
4. **The overflow entry's message stops lying.** It says the refused facts *"are not re-derivable until
   those rows project again"*; C3 shows 3 of the 11 raise sites re-derive on their own (the 5-minute long
   floor and the 1-minute sweep). The message states what is true per family, and reports the refusal
   count as **refusals since the last heartbeat** rather than a monotone total that a re-derived fault
   inflates by one a minute.

### 4.3 What this design does NOT do

- It does not change `rowIssueCapPerTarget`'s value. After Inc 1 it bounds the fault population, which is
  what 500 was sized for; changing the number without a measured fault count would be a guess.
- It does not add admission ranking, eviction, or a second budget class in the cache. §8 refutes each.
- It touches no Core-KV read, no lens, no operation, no engine boundary. Weaver-internal, Health-KV only.

---

## 5. State-lifetime table (the one new stateful mechanism)

`surfaceStats` — per (target, gap column), the set of entity ids this engine instance observes holding the
column open. Lives in `internal/weaver/contraction.go` beside the structure it mirrors.

| Boundary | Rule |
|---|---|
| **created** | lazily, at a target+column's first observed-open row |
| **added** | in the `actionSurface` arm, on a *transition* to open only — a repeat delivery of an already-open row is a no-op (`contractionStats.observe`'s rule) |
| **removed** | (a) that entity's column observed false — `clearClosedMarks`' candidate walk → `retireClosedGapIssues` (`evaluator.go:1064`); (b) the entity tombstoned — **the same candidate walk, not the entity prefix clear**: a `gapOpen:` key carries no entity segment, so `clearClosedMarks`' `row == nil` prefix clears (`evaluator.go:1009-1011`) cannot reach one. On an empty row `markCandidateColumns` yields the playbook's gap keys, which is a **superset** of every column a surface entry can exist at — the surface arm requires a playbook `gaps` entry (`evaluator.go:316`) — so every membership is removed by the walk. The orphan-column reason the `gap:` prefix clear exists does not apply to this family; (c) the target leaving — Revoke or `reconcileConsumers` removal, via `issueKeyTargetPrefixes` extended with the new prefix |
| **issue entry** | written whenever the set is non-empty (message carries the live count); retired by whichever remove empties the set — never by a remove that merely shrinks it |
| **carried across a `clear`** | n/a — unlike the `paced` map there is no clock here; the set *is* the fact, and a removal is evidence the row closed |
| **ordering** | none needed: the set is a membership, not a sample, so there is no admission order to promise |
| **crash / restart** | empty, like the latch it replaces. Rebuilt by whatever lane-1 delivers afterwards, so the count is a **lower bound** until every open row re-projects; `ReplayTarget` is the verb that re-derives it in full. Identical residual to the shipped per-entity entries |
| **reconnect** | untouched — in-memory, no substrate dependency |
| **replay** (`ReplayTarget`, cold boot) | the target's whole current row set is re-presented, so the set re-derives exactly |
| **upgrade** | a live upgrade starts the set empty; the count climbs back as rows redeliver. The pre-existing per-entity entries do not survive the binary change either, so nothing is stranded |
| **loss of the structure** | degrades to a missing/low count, never to a wrong verdict — nothing gates on it, exactly as for `contractionStats` |

Inc 2's refusal counter: one integer per (target, family) beside `refused`/`refusedWorst`, created and
retired on those maps' existing legs (`retireRowIssueOverflowLocked`, `health.go:294-301`) — no new
lifetime.

---

## 6. Contract surface

**Change (staged UNCOMMITTED in `main`): `docs/contracts/10-orchestration-weaver.md` §10.8, `surface` row.**
One sentence, making the entry's cardinality explicit. Rationale for the edit rather than building to the
current text: the clause today says *"raises a Contract #5 §5.5 `issues[]` entry keyed `issueCode`"*, which
on my reading already promises one entry per gap, but the shipped implementation fans it out per row, and
Inc 1's whole payoff is that cardinality. A promise a consumer's behaviour depends on should not be
readable two ways.

The edit stays at promise altitude — an observable wire shape, no mechanism, no internal names — per the
public-contract rule.

**Affected consumers.**

- **Contract #5 §5.5 is unchanged.** A per-column entry is an ordinary issue record: `code`, `severity`,
  `message`, `since`. Its "a resolved issue is simply absent" semantics (`05-health-kv.md:110`) is what
  retires it when the set empties.
- **`internal/unroutedconvergence`** — asserts the code, not the key. Unchanged.
- **`cmd/loupe`** — §7.2.
- **Package authors** — nothing to change. `issueCode` / `issueSeverity` keep their meaning; no package
  declares a key.

**Build-to, not change:** §10.8's exhaustion promise (`:71-72`, `:345-346`) and #10-substrate's
re-derivation clause (`:189-191`) — the design confirms them and, in Inc 2, stops the re-derivation from
producing a log flood.

---

## 7. Reconciliation with the existing mental model

### 7.1 "Didn't we already handle this?"

Partly, and the part that exists is why the rest was invisible. The 2026-08-28 close pass added the budget,
the `refusedWorst` fold and `listingRank`'s tier-1 pin **so that the aggregate verdict and the existence of
the refusal survive** — and those work (§1.4). What was never addressed is that the budget's *population*
is two populations, and that a refusal changes what each seam logs. The prior work made the failure honest
at the document level; it did not stop it happening.

### 7.2 "Does this lose the operator something?" — the one real cost, priced

The per-entity surface entry has exactly one Health-side consumer: Loupe's entity detail page, which
attributes issues to an entity by substring-matching the entity id inside the issue **message**
(`cmd/loupe/weaver.go:396-429`, called at `:1128`). After Inc 1 a surface gap's entry no longer names an
entity, so it stops appearing there. Three reasons that is the right trade:

- **It already fails at exactly the scale this design is about.** Loupe reads the *published* document,
  which is truncated to 50 entries severity-first with per-entity families ranked **last**
  (`health.go:36`, `:921-968`). Past ~50 open issues the entity's own entry is almost certainly not in the
  document. The drill-down works only when the backlog is small enough not to matter.
- **The same page already shows the fact, from the row.** `weaverEntityGap` renders `State: "open"` for a
  gap column read directly off the weaver-targets row (`cmd/loupe/weaver.go:1109-1119`). For a surface gap
  — no mark, no dispatches — that *is* the whole fact the Health entry restated.
- **Loupe already ruled the same way once.** F17 explicitly declined to duplicate the `UnroutedTasks`
  Health issue into `/api/tasks`, on the grounds that it renders authoritatively on the Weaver component
  page and *"the per-row flag is the drill-down"*
  (`loupe-f17-queue-observability-ux.md:56`). Inc 1 makes the platform agree with that decision.

The residue worth naming: an operator who wants the *list* of open rows reads them from the target's row
set — the projection the surface gap is computed from — which is where they are authoritative and complete,
rather than from a truncated sample in a heartbeat.

### 7.3 "Does this introduce new state we already keep?"

Yes and no. It introduces one membership set, and the engine already keeps a structurally identical one:
`contractionStats.known`, one entry per currently-violating (target, entity), **uncapped**
(`contraction.go:39-76`). That matters twice over — it is the precedent to mirror, and it is evidence that
an uncapped per-open-row set is an accepted cost in this engine when the entries are cheap and nothing
gates on them. It could not simply be *reused*: `contractionStats` is keyed on the `violating` column and
rolled up per target, while this must be per gap column.

### 7.4 "Does this contradict the design that filed the row?"

No — it completes it. That design stated its trade openly and filed the residue
(`weaver-decline-retry-substrate-native-design.md:1336-1344`). It also named the clean resolution as *"a
per-row issue budget that admits by re-derivability rather than by arrival"*; §8 shows why that particular
shape does not work, on that design's own scenario. Two smaller drifts in it are corrected here as
documentation, not code: its §5 state table says the overflow entry retires *"as soon as `rowIssues` falls
back under the cap"* while the shipped code retires at **zero** (`health.go:282-291`), and its budget row
lists **three** per-entity families where the shipped test is a key shape covering four.

---

## 8. Alternatives

**Row 1 — do not have this thing: delete `rowIssueCapPerTarget` entirely.**
The honest first question, and it is refused — but not for the reason the constant's own comment gives.
Without the cap the issue map and both per-heartbeat sorts grow with the target's *broken*-row count, which
on a systemically broken 100k-row lens is ~100k `healthIssue` values (message + `since` + key ≈ 250 B each,
so ~25 MB per such target) sorted twice every 10 seconds. That is a real cost and the cap is the right
answer *for faults*. What the deletion argument does establish is that the cost is driven by the message
strings and the sort, not by membership — which is precisely why Inc 1 moves the workload population into a
`struct{}` set instead of arguing about the number 500. **A weaker version of this row is what Inc 1 is:
delete the cap's *harder* half by deleting the population that fills it.**

**Row 2 — re-derivability-ranked admission (what the board row prescribed). REFUTED.**
Three independent failures, any one fatal:

- **The named filler is itself non-re-derivable.** The row's scenario is a `surface` backlog crowding out a
  `RowDataError`. But the surface arm Acks (`evaluator.go:333`) and mints no mark or `__count`, so no sweep
  leg ever revisits it (`reconciler.go:701-707`) — it is in the *same* re-derivability class as the fact it
  is accused of crowding out. A ranking on re-derivability would not evict a single surface entry.
- **The contract citation that motivated the ranking is false.** §10.8's exhaustion fact *is* re-derived,
  every minute, from `sweepCount`/`reclaim`. Under a re-derivability ranking it would be the **first** thing
  refused — the policy would preferentially discard the one fact the row invoked the contract to protect.
- **Ranking implies eviction, and eviction thrashes.** An evicted re-derivable entry returns on its own
  cadence and is refused again; with `alertStanding` unchanged that is an Error log per eviction per minute,
  forever. The policy manufactures §1.3's worst row.

**Row 3 — a second budget class inside the cache (workload 500 + fault 500).**
Fixes the starvation without moving anything. Rejected: it keeps the O(open rows) population, its messages
and its sort — the exact cost the cap exists to bound — and doubles the tracked set to pay for it; it leaves
50 identical per-row warnings crowding the document; and it adds a budget where Inc 1 removes a population.
Adding a mechanism to patch a gap left by the previous mechanism is the signal to re-derive the base.

**Row 4 — raise the cap.** A number chosen against no measured fault count, which does not change that the
two populations share a budget. It postpones the same failure at higher memory.

**Row 5 — drop the `surface` Health entry altogether and let consumers read the rows.**
The strongest simplification, and it is the one Inc 1 stops just short of. Refused: §10.8 promises the
entry (*"raises a Contract #5 §5.5 `issues[]` entry"*) and `orchestration-base`'s primordial `unroutedTasks`
target exists to produce it; FR29's whole point is that a surface gap is *surfaced*. A per-column entry
keeps that promise at O(gap columns).

**Row 6 — make the surface entry per-column but omit the count** (one entry saying "one or more rows").
Cheaper by exactly the membership set. Refused: without membership there is nothing that knows when the
*last* row closes, so the entry would need a level re-derivation and a staleness sweep — more machinery than
the set, and a fact that goes quiet only on a timer. The count is also the number an operator actually
wants.

**Row 7 — fix only the seams (Inc 2 alone).** Makes the refusal audible without stopping it happening. It is
a real improvement and it is why Inc 2 exists — but on its own it leaves a healthy 500-task backlog
permanently suppressing a target's fault reporting, and answers that with a better log line.

**Combination check.** Rows 3 + 4 together (two classes, both larger) is the shape that survives longest
under objection, and it still loses to Inc 1 on every axis that matters: more memory, more sort, no
improvement to the document, and it keeps the workload population inside a bound whose whole justification
is fault volume. Row 7 combines with Inc 1 rather than competing — it *is* Inc 2.

**Each rejected alternative's objection, run back against the recommendation.** Row 2's objection is
"a policy that discards re-derivable facts discards the §10.8 fact" — Inc 1 discards nothing and the
exhaustion fact keeps its slot. Row 3's is "it adds a mechanism instead of removing a cause" — Inc 1 removes
a population and adds one set smaller than what it replaces. Row 5's is "it breaks a contract promise" —
hence the explicit cardinality edit rather than a silent reinterpretation. Row 6's is "no membership, no
retirement" — Inc 1 keeps membership.

---

## 9. Test strategy

Every test below is **owned by a named increment**; none is left unowned.

**Inc 1**

- `internal/weaver` unit: a surface gap opening on N entities produces exactly **one** issue at
  `gapOpen:<t>.<col>`, code/severity from the spec, message naming N; a second delivery of an
  already-open row does not change N (the transition rule); the entry retires only when the **last**
  entity's column closes, not the first.
- Budget non-interference — the point of the increment: with 600 entities holding a surface column open,
  `rowIssues[target]` is **0** and a subsequently raised `RowDataError` is **admitted**. This is the
  regression the board row describes, expressed as a test.
- Teardown, one vector each: entity column closes; entity tombstone (prefix clear); target `Revoke`;
  target unregistered via `reconcileConsumers`.
- `TestListingRank_EveryIssueFamilyIsClassified` extended: `gapOpen:` classified tier 2, and a document
  containing one `gapOpen:` entry plus >50 per-row faults lists the `gapOpen:` entry.
- Migrated fixtures (C2): `evaluator_internal_test.go`, `replay_internal_test.go`,
  `decline_retry_internal_test.go`, `health_internal_test.go`. The cap/listing fixtures re-seed with a
  **fault** family — what they were always testing.
- `internal/unroutedconvergence` (`make test-unrouted-convergence`, build tag) runs **unchanged** and must
  stay green; it is the e2e proof that the contract promise still holds end to end. This is a build-tagged
  harness outside `go test ./...` — Phase 0 must run it explicitly.
- `docs/observability/health-kv-schema.md` updated (C2).

**Inc 2**

- A refused `alertStanding` raise logs at the loud level once and at Debug for the rest of the window,
  with a fake clock — the direct inverse of §1.3's flood row.
- A refused `alertPaced` raise is loud once per interval rather than never.
- The overflow entry's count reflects refusals in the last heartbeat window and does not grow monotonically
  under a re-derived refusal.
- A mutation check on the first two: with the seam change reverted, the tests fail.

**Gates.** `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`,
`go test ./internal/weaver/... ./cmd/loupe/...`, `make test-unrouted-convergence`, and every
`scripts/lint-*.go`. No `packages/` content changes, so no manifest version bump.

---

## 10. Decomposition for the Steward

| Inc | What | Size | Posture-changing? |
|---|---|---|---|
| **1** | Move the `surface` population out of the issue cache: new `gapOpen:` family, `surfaceStats` member set, raise/retire on the existing call sites, per-entity write and clears **removed**, `surfaceOnlyGap`'s dead latch rationale rewritten, listing classification, fixture migration, schema doc | S–M | **Yes** — it changes an observable Health-KV shape and carries the contract edit. Full review depth. |
| **2** | Refusal-honest seams: `setSince`/`pacedRaise` report refusal; `alertStanding` and `alertPaced` keep their loudness discipline under refusal, paced per (target, family); overflow message corrected | S | No. Ordinary depth. |

Inc 1 is independently shippable and realizes the whole payoff on its own (the budget stops being reached
by healthy work). Inc 2 is independently shippable and improves the residual case whether or not Inc 1 has
landed. Neither is dead scaffolding: both have a live consumer today.

**Sequencing:** Inc 1 first — Inc 2's flood row is reached far less often once Inc 1 lands, and Inc 1's
fixture migration touches the same test files.

---

## 11. Risks + residuals

- **The count is a lower bound after a restart** until every open row re-projects, and `ReplayTarget` is
  the verb that fixes it. Inherited unchanged from the entries it replaces, and identical to
  `contractionStats`' shipped posture. Stated in the message wording so an operator is not misled.
- **A message whose value changes every pass sits at one latch key.** Safe here and only here: the surface
  arm uses bare `issues.set`, so it touches neither `standingAs` (which deliberately ignores messages) nor
  `pacedRaise` (which would re-arrive on a changing count). If a later change routes this raise through a
  pacing seam, the count must move to a metric. Worth a line in the code comment.
- **A surface column the PLAYBOOK drops, while rows still carry it true, leaves stale membership.**
  `markCandidateColumns` still yields the column from the row side, `boolColumnRead` reads it open, and
  the walk `continue`s without retiring — so the `gapOpen:` entry stands with a count that no longer
  corresponds to a dispatchable gap, until the column closes or the entity is deleted. This is the
  identical residual `retireClosedGapIssues`' own doc already names for the per-entity entries it
  replaces (`evaluator.go:1176-1190`: *"a gap the playbook dropped is undispatchable but may still read
  TRUE in the row"*), and the reason it is not "fixed" here is the same one that doc gives — only a leg
  that observed the column itself go false may retire the fact, or it flaps. Inc 1 inherits it; it does
  not create it. What Inc 1 *does* change is visibility: one stale per-column entry is now listed at
  tier 2 rather than N per-entity entries truncated away at tier 3, which makes the residual easier to
  notice, not harder.
- **`gapOpen:` must be added to `issueKeyTargetPrefixes`** or a revoked target strands its entries. The
  teardown test above is what catches it; `issueKeyTargetPrefixes`' own doc states the rule.
- **Multi-instance Weaver:** each instance publishes its own heartbeat with its own count, and Loupe already
  reports per-instance rather than merging (`cmd/loupe/weaver.go:319-337`, whose comment gives the reason).
  Unchanged by this design; noted because the count invites a merge that would be wrong.
- **Not addressed:** what an operator does *with* 500 concurrently-broken rows on one target. After Inc 1
  the cap only fires in that state, the overflow entry says so honestly, and `ReplayTarget` re-derives.
  Raising the number without a measured fault distribution is a guess, so it is deliberately not done.
