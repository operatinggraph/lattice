# Weaver — separate the WORKLOAD population from the FAULT population in the issue cache

**Status: ✅ RATIFIED by Andrew 2026-09-02 — "agreed - take the trade": a `surface` gap raises one counted entry per (target, gap column); per-row identity stays in the target's projected row set. One fire, size M, Increment 1 first. Contract #10 §10.8's `surface` clause of record is in §6 and lands WITH the fire's commit (a cardinality promise the runtime does not yet keep is not committed ahead of its build — Andrew, 2026-09-01); nothing is staged. Winston's adjudications: the `gapOpen:` key family; nested per-(target, family) refusal counters with a heartbeat-boundary reset; the tombstone leg clears every column set; `since` on the per-column entry means when the column last went from no open rows to some.** · Designer fire 2026-09-01 · Lattice lane
Board row: *[Weaver] One per-target issue budget is shared by the WORKLOAD and FAULT populations*
(★★ · M).

---

## For Andrew

**What it does, in two lines.** Weaver's per-target 500-slot issue budget is shared by two populations
with different cardinality laws: `surface` gap entries, one per *open row of business work*, and every
other per-row entry, one per *broken row*. A healthy backlog of 500 unclaimed tasks fills the budget and
every fault raised afterwards for that target is refused. This design takes the workload population out
of the issue cache entirely — a `surface` gap raises **one** issue per (target, gap column) carrying the
count of open rows that instance observes — so the budget bounds only the fault families it was sized
for, and then repairs the two log seams a refusal currently inverts.

**One judgement call, and it is yours.** Inc 1 trades **per-row identity in Health-KV for a surface gap**
for a per-column count. My recommendation is to take the trade: the identity is not lost, it is
*relocated to where it is already authoritative* (the projected row set the surface gap is computed
from), the one surface that loses it already fails past 50 entries, and Loupe's entity page already
renders the same fact from the row itself. Loupe's other two issue surfaces and every other Health-KV
reader improve or are unchanged (§7.2). But "which rows are awaiting a decision" is a product question
about what an operator reads on the Weaver page, so it is flagged rather than adjudicated.

**Frozen-contract change: yes, one clarifying sentence, staged UNCOMMITTED in `main`.**
`docs/contracts/10-orchestration-weaver.md` §10.8, the `surface` action row. Today it reads
*"raises a Contract #5 §5.5 `issues[]` entry keyed `issueCode`"* — singular, and on my reading already
per gap column. The edit makes the cardinality explicit (one entry per target + gap column, carrying a
count the contract bounds honestly rather than over-promises — §6 has the exact sentence) so that what
Inc 1 changes is a promise a reader can check rather than an implementation detail nobody wrote down.
Affected consumers: §6.

**No architectural fork.** No new engine, no new plane, no Core-KV read, no lens. The one alternative
that would have been a fork — a re-derivability-ranked admission policy, which is what the filing
prescribed — is **refuted** in §8, on the filing's own scenario.

---

## 1. Problem + intent

### 1.1 What the filing said, and what grounding changed

The row was filed by the `weaver-decline-retry-substrate-native` close pass
(`_bmad-output/implementation-artifacts/weaver-decline-retry-substrate-native-design.md:1327-1344`),
which stated the trade it was shipping rather than discovering it later. That filing was right about the
mechanism and wrong about three of its consequences. All three corrections are load-bearing, so they are
stated first.

| The filing's claim | Verdict | Evidence |
|---|---|---|
| `rowIssueCapPerTarget` = 500 is **one** per-target budget over `gap:`/`data:`/`template:`/`sweep:`, admission-ordered | **Confirmed** | `health.go:62`; `rowIssueTarget` `evaluator.go:2075-2093`; `setSince` `health.go:236-257` |
| Released only when the target's per-row set reaches **zero** | **CONFLATES two things.** A *slot* is released on every `clear` of a tracked per-row entry, so admission resumes the moment the target is back under the cap. What waits for zero is the *overflow entry* and the refusal accounting behind it | `releaseRowIssueLocked` `health.go:282-291` (decrement, unconditional) vs `retireRowIssueOverflowLocked` `health.go:296-301` (called only at zero); `docs/observability/health-kv-schema.md:1035-1036` states both halves — slots free as entries retire, *"admission resumes as soon as the target is back under the cap"* |
| A high-volume `surface` family fills it | **Confirmed**, and this is the whole mechanism | `evaluator.go:316-334`; §3 C1 |
| A later `RowDataError` is refused and lost | **Confirmed** for the `data:` family and for `sweep:` `CorruptMark` | `evaluator.go:55-58`, `:101`; `temporal.go:120,137,158`; `reconciler.go:1152-1160` |
| …**"voiding §10.8's exhaustion-raise for that target"** | **FALSE** | `GapBudgetExhausted` is re-derived every sweep pass — `escalateExhaustedGap` is called from `sweepCount` (`reconciler.go:634`) and `reclaim` (`reconciler.go:943`), not only from lane-1 (`evaluator.go:195`), and `defaultSweepInterval` is one minute (`reconciler.go:21`). A refused exhaustion raise is **delayed, not lost**: it is re-attempted every minute and lands whenever a slot frees. Contract #10-substrate:189-191 says as much in the contract text — the fact is *"re-derived for as long as the suppression lasts"*. |
| "…**evicts** non-re-derivable facts" | **FALSE as worded** | `setSince` never evicts a tracked entry to admit a new one; it refuses the *new* key (`health.go:238-252`). There is no eviction anywhere in the cache. |

The lane row carries the corrected mechanism (`backlog/lattice.md:60`). Its one remaining inaccuracy is
the same slot/overflow conflation row 2 above fixes — the row still reads *"released only at zero"* — and
the lead corrects that phrase at ratification, with the contract edit and the row's state.

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
| `alertStanding` (`evaluator.go:1572`) | `gap:` `GapBudgetExhausted` | arrival at **Error**, continuation at **Debug** (`evaluator.go:1775-1781`) | `standingAs` is false for a key the cache never tracked, so **every** re-derivation that finds the target still at cap logs at Error — one per sweep pass, ~1/min | **Error flood** for as long as the target is at cap on each pass, arriving exactly when the target is worst off — the flood `alertStanding` exists to prevent |
| `alertPaced` (`evaluator.go:708`) | `template:` | loud on arrival, then one loud record per `logPaceInterval` (1 h) | `pacedRaise` returns `loud=false` unconditionally for a refused key (`health.go:381-389`), so the fault logs at **Debug forever** | **permanent silence** in both planes |

How long that flood runs is a property of the *filler*, not of the seam. Each repaired row hands a slot
back (`releaseRowIssueLocked`), so on a target whose per-row entries churn the flood is intermittent —
loud only on the passes that find the cap full. The surface population is the case with no churn: a
static backlog of 500 open rows frees nothing until the work itself closes, so the flood runs for the
life of that backlog, and for an exhaustion fact that is up to the retry budget's own span (≈128 h at
the defaults). Unbounded is the worst case, not the general one — and it is exactly the case Inc 1
removes.

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
| `setLocked` — the write past the cap decision; it PRESERVES an existing `since` | `internal/weaver/health.go:259-268` |
| `clear` — retires the entry and DELETES its `since` (so a re-raise after the last close is a fresh arrival) | `internal/weaver/health.go:331-343` |
| Slot released on every `clear`; only the OVERFLOW entry waits for zero | `releaseRowIssueLocked` `internal/weaver/health.go:282-291`; `retireRowIssueOverflowLocked` `:296-301`; `docs/observability/health-kv-schema.md:1035-1036` |
| The separate pace budget, and its unconditional not-loud on refusal | `internal/weaver/health.go:372-389` |
| The family membership test — key SHAPE, two separators below the target | `internal/weaver/evaluator.go:2034-2093` |
| Family prefixes — **ten** of them | `internal/weaver/evaluator.go:1835-1846` |
| The surface arm: raise + `Ack`, no log | `internal/weaver/evaluator.go:316-334` |
| Surface has no mark and no `__count`, so no sweep leg revisits it | `internal/weaver/evaluator.go:585-587`; `reconciler.go:701-707` |
| Exhaustion raise, and its three call sites | `internal/weaver/evaluator.go:1572`; `reconciler.go:634`, `:943`; `evaluator.go:195` |
| Sweep cadence = 1 minute | `internal/weaver/reconciler.go:21` |
| `alertStanding` arrival test | `internal/weaver/evaluator.go:1775-1781` |
| `alertPaced` loudness | `internal/weaver/evaluator.go:1819-1830` |
| Lane-1 consumer: one durable PER TARGET under a shared name prefix, `DeliverLastPerSubject`, `MaxAckPending` 1024, no explicit `AckWait` (30 s default) | `internal/weaver/engine.go:477-490`, `:49`; `internal/substrate/consumer_supervisor_pump.go:715` |
| `markCandidateColumns` on a TOMBSTONE yields the playbook's gap keys only — the row contributes nothing | `internal/weaver/evaluator.go:1696-1712` |
| The `gap:` prefix clear on a tombstone, which retires an orphan-column entry no per-key clear can reach | `internal/weaver/evaluator.go:1009-1011` |
| An Augur escalation dispatches (`actionDirectOp`), so it is never a `surface` arm | `internal/weaver/strategist.go:660-673` |
| Long redelivery floor = 5 min | `internal/substrate/consumer.go:86` |
| Surface entry retirement legs | `retireClosedGapIssues` `evaluator.go:1191-1194`, called from `clearClosedMarks`' candidate walk `evaluator.go:1064`; the `row == nil` prefix clears `evaluator.go:1009-1011`; prefix teardowns `evaluator.go:1911-1919` |
| Shared latch: Surface / GapBudgetExhausted / GapEscalatedToAugur all sit at `issueKeyGapEntity` | `internal/weaver/evaluator.go:1526-1560` (the `surfaceOnlyGap` guard exists *because* of this) |
| Listing tiers, and the test that forces a new family to be classified | `internal/weaver/health.go:944-1002` (`TestListingRank_EveryIssueFamilyIsClassified`) |
| Document cap = 50, severity-first, honest truncation entry | `internal/weaver/health.go:36`, `:921-942` |
| Loupe's issue attribution — whole-WORD match on the issue MESSAGE, three call sites | `issuesNaming` + `messageNamesToken` `cmd/loupe/weaver.go:388-428`, called at `:746` (target detail), `:1128` (entity detail), `:1244` (targets list) |
| The targets-list chip and sort rank fed by that third call site | `cmd/loupe/web/js/views/weaver.js:104`; `cmd/loupe/web/js/logic/weaver.js:36` |
| Loupe reports Weaver heartbeats **per instance** and deliberately never merges them | `cmd/loupe/weaver.go:315-318` |
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
| `cmd/loupe/weaver.go:746` `Issues: issuesNaming(hbs, targetID)` | target detail's issue panel — matches the TARGET id | yes, and improves: a truncated sample of identical entries becomes one per column. §7.2 |
| `cmd/loupe/weaver.go:1244` `issues := issuesNaming(hbs, t.TargetID)` → `Issues: len(issues)` | targets list — feeds the "N issues" chip (`web/js/views/weaver.js:104`) and the sort rank (`web/js/logic/weaver.js:36`) | yes — the count falls to one per column and the target may sort down. No code change needed. §7.2 |
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
  ten in `evaluator.go:1835-1846`.
- code and severity: the package's declared `issueCode` / `issueSeverity`, unchanged.
- message: names the target, the column and the **count of rows this engine instance currently observes
  holding it open** — e.g. `target unroutedTasks: 137 rows have column missing_claim true`.
- the entry is rewritten on **every** membership change, add and remove alike, so the count an operator
  reads is the count the instance holds; only the remove that empties the set retires it (§5).

Four properties fall out of the key shape and need no new code:

1. **It is not counted against the budget.** `rowIssueTarget` (`evaluator.go:2075-2093`) requires two
   separators below the target; `gapOpen:<t>.<col>` has one. The workload population leaves the budget by
   construction, not by a new exemption — the same test that already excludes the `__capped` overflow entry.
   Because the *shape* is what carries the property, §9 asserts the one input that would break it: a gap
   column carrying a `.` gives the key two separators and would re-enter the budget.
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
floor and `ReplayTarget` is the verb that re-derives it in full (`contraction.go:25-32` states exactly
that, for exactly this reason, about the count it already keeps). That last point is not a regression: the
per-entity entries it replaces have the identical restart residual, already documented as such
(`weaver-decline-retry-substrate-native-design.md:1346-1353`).

**Raise and retire ride the call sites that already exist.** The raise is `evaluator.go:331`'s own line.
The retirements are `retireClosedGapIssues` (`evaluator.go:1191-1194`, reached from `clearClosedMarks`'
candidate walk when *this* entity's column goes false), `clearClosedMarks`' own `row == nil` leg for a
tombstoned entity (`evaluator.go:1009-1011`, where the `gap:` prefix clear already runs), and the two
prefix teardowns (`issueKeyTargetPrefixes`, `evaluator.go:1911-1919`, for a target leaving by Revoke or by
`reconcileConsumers` removal). Each becomes a set-remove — the tombstone one across all of the target's
column sets, for the reason §5 gives — and the issue entry is retired by whichever remove empties a set.
**The per-entity write and its clears are REMOVED, not left beside the new one** — a second mechanism next
to the first would leave the budget filler exactly where it is.

**One shipped doc comment is corrected in the same increment.** `rowIssueCapPerTarget`'s own comment says
the cap bounds *"the `data:` and `template:` families together"* (`health.go:46-48`) while `rowIssueTarget`
covers **four** — `gap:` and `sweep:` as well — and says so at length in its own comment
(`evaluator.go:2034-2074`). A builder reading the constant is told the bound is narrower than it is, which
is the exact misreading that makes the surface population look harmless. §7.4 names the same drift in the
prior design doc; this fixes it where it is actually read.

### 4.2 Increment 2 — a refusal must not invert a seam's loudness

Inc 1 stops the budget being reached by healthy work. It does not change what happens when a genuinely
broken target fills 500 fault slots, and there the log is the only surviving plane — so the log must
behave. Three changes, all small, all at the seam rather than in the cache:

1. **`setSince` and `pacedRaise` report the refusal.** Both already take the decision; neither tells the
   caller. Return it.
2. **`alertStanding` treats a refused key as standing.** Not tracked ⇒ `standingAs` is false ⇒ Error once
   a minute for as long as the target sits at cap. The rule that restores the intent is: a refused raise is
   loud **once per (target, family) per `logPaceInterval`**, and Debug otherwise. The arrival is still
   heard; the flood ends. This needs one small clock beside `refused`, keyed per **(target, family)** — a
   nested `map[target]map[family]…`, so `retireRowIssueOverflowLocked`'s existing `delete(m, target)`
   (`health.go:296-301`) drains it whole and no new teardown leg is invented. Bounded by targets × the four
   per-row families, not by rows.
3. **`alertPaced` paces a refused key on the same (target, family) clock** rather than returning
   `loud=false` unconditionally. Today a refused `template:` fault is Debug for the life of the process;
   after, it is audible at least once an hour, which is what the seam promises everywhere else. Note the
   pace budget is a *second* 500-slot budget (`rowPaced`) released only by `prunePaced`/`clearPrefix`
   (`health.go:429-444`) — Inc 1 empties it of workload entries too, since the surface arm never used it.
4. **The overflow entry's message stops lying.** It says the refused facts *"are not re-derivable until
   those rows project again"*; C3 shows 3 of the 11 raise sites re-derive on their own (the 5-minute long
   floor and the 1-minute sweep). The message states what is true per family, and reports the refusal
   count as **refusals since the last heartbeat** rather than a monotone total that a re-derived fault
   inflates by one a minute. That per-window count needs a boundary, and there is no existing one to
   borrow: `snapshot()` (`health.go:504-517`) is a pure read with no reset leg. So the windowed counter is
   reset where the heartbeat consumes it, and §5 carries its lifetime rather than treating it as free.

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
| **removed** | (a) that entity's column READ false — `clearClosedMarks`' candidate walk, and each of the reconciler sweep's two gap-close legs, via `retireSurfaceMembership` (amended 2026-09-04, close review: the membership is its own retirement, not `retireClosedGapIssues`' business, and every leg reaching it stands behind `boolColumnRead`'s `readable` verdict — `boolColumn`'s conservative false on a present non-bool is not evidence a column closed); (b) the entity tombstoned — on `row == nil`, the entity is removed from **every** column set of that target, which is the in-memory analogue of the `gap:` prefix clear the same leg already runs (`evaluator.go:1009-1011`). The candidate walk cannot carry this: on a tombstone `markCandidateColumns` yields `target.Gaps` alone (the empty row contributes nothing — `evaluator.go:1696-1712`), so a column the playbook has since **dropped** never yields, and a membership recorded while it was still declared would leak with the entity gone and no leg able to reach it. That is precisely the orphan-column hazard the `gap:` prefix clear exists for, and the same answer applies; (c) the target leaving — Revoke or `reconcileConsumers` removal, via `issueKeyTargetPrefixes` extended with the new prefix; **(d) the row delivered non-violating** (amended 2026-09-04, close review) — a row whose `violating` reads false holds no open work by the lens's own verdict, and `handleRow` returns at the L1 gate before the dispatch loop, so a membership recorded while it was violating would otherwise strand; the removal is `removeEntity` across every column set of the target, for the same reach reason as (b), since a gap column may still read true beside `violating: false`. It takes the same `readable` guard as (a) (amended 2026-09-04, close review): a present non-bool `violating` states nothing, and this is the widest removal there is |
| **issue entry** | rewritten on **every** membership change — add and remove alike — through `issues.set`, so the count is always the set's current size. `setLocked` preserves an existing `since` (`health.go:259-268`), so a rewrite restates the count without disturbing the entry's age. Only the remove that empties the set retires it |
| **carried across a `clear`** | n/a for a clock — unlike the `paced` map there is none here; the set *is* the fact, and a removal is evidence the row closed. What a retirement does carry is the `since`: `clear` deletes `c.since[key]` (`health.go:331-343`), so when the last row closes and a new one later opens, the entry arrives with a **fresh** `since`. That is the intended reading — see §6 |
| **ordering** | none needed: the set is a membership, not a sample, so there is no admission order to promise |
| **crash / restart** | empty, like the latch it replaces. Rebuilt by whatever lane-1 delivers afterwards, so the count is a **lower bound** until every open row re-projects; `ReplayTarget` is the verb that re-derives it from what the re-presented rows state. Identical residual to the shipped per-entity entries |
| **reconnect** | untouched — in-memory, no substrate dependency |
| **replay** (`ReplayTarget`, cold boot) | the target's whole current row set is re-presented, so the set re-derives it from what the re-presented rows state (amended 2026-09-04) |
| **upgrade** | a live upgrade starts the set empty; the count climbs back as rows redeliver. The pre-existing per-entity entries do not survive the binary change either, so nothing is stranded |
| **loss of the structure** | degrades to a missing/low count, never to a wrong verdict — nothing gates on it, exactly as for `contractionStats` |

**Why the tombstone sweep over the target's own column sets is complete.** A membership can only be
recorded at a column whose playbook entry declares `action: "surface"` (`evaluator.go:316`), so the sets
of one target are bounded by its declared columns and the entity appears in no other. The one path that
dispatches at a column the playbook does not name — an Augur escalation — returns `Action: actionDirectOp`
(`strategist.go:660-673`), never `actionSurface`, so it can never open a membership behind the playbook's
back. The raise side is airtight; only the *remove* side needed the orphan handling above, because the
playbook may change between the two.

**Inc 2 adds two pieces of state, and one of them has a real lifetime.**

| State | Boundary | Rule |
|---|---|---|
| the refused-raise loud clock, per (target, family) | created | lazily, at that (target, family)'s first refused raise |
| | read/written | `alertStanding` and `alertPaced` both consult it; loud once per `logPaceInterval`, Debug otherwise |
| | retired | with the target's refusal accounting — a nested map under `target`, so `retireRowIssueOverflowLocked`'s `delete(m, target)` (`health.go:296-301`) drains it; no new teardown leg |
| | restart | empty ⇒ the next refused raise is an arrival and is loud, which is the correct posture for a fresh process |
| the windowed refusal count, per (target, family) | created | lazily, alongside the clock |
| | **reset** | at the heartbeat boundary — the count is *"refusals since the last heartbeat"*, and `snapshot()` (`health.go:504-517`) is a pure read with no reset leg, so the reset is added where the heartbeat consumes the number, not inferred from a read |
| | retired | as above, with the target |
| | loss | the overflow message under-reports one window; the monotone `refused` total it replaces in the message is not the aggregate verdict, which rides `refusedWorst` and is untouched |

---

## 6. Contract surface

**Change (staged UNCOMMITTED in `main`): `docs/contracts/10-orchestration-weaver.md` §10.8, `surface` row.**
One sentence, making the entry's cardinality explicit. Rationale for the edit rather than building to the
current text: the clause today says *"raises a Contract #5 §5.5 `issues[]` entry keyed `issueCode`"*, which
on my reading already promises one entry per gap, but the shipped implementation fans it out per row, and
Inc 1's whole payoff is that cardinality. A promise a consumer's behaviour depends on should not be
readable two ways.

**The count clause must not over-promise, and the exact replacement text is this.** A contract sentence
saying the entry carries *"the number of rows currently holding the column open"* is a promise the engine
cannot keep, for two shipped reasons. A lane-1 durable that survives a restart resumes from its persisted
ack floor, so a row already acked and not since re-projected is never re-counted — the count is a lower
bound until every open row projects again. And lane-1 durables are per target under a shared name
(`engine.go:477-490`), so with more than one Weaver the target's rows shard across instances and each
instance's heartbeat carries its own count — which is why Loupe reports Weaver heartbeats per instance and
deliberately refuses to merge them (`cmd/loupe/weaver.go:315-318`). The clause of record, replacing the
staged one:

> carrying the count of open rows that heartbeat's instance currently observes — a lower bound while rows
> re-project after a restart, and per-instance where more than one Weaver runs

**That sentence lands with the fire's commit**, not at ratification (Andrew, 2026-09-01: a promise the runtime does not yet keep is not committed ahead of its build, and never with a transitional note); this document is its text of record until then. It stays at promise altitude — an observable wire shape, no
mechanism, no internal names — per the public-contract rule.

**Affected consumers.**

- **Contract #5 §5.5 is unchanged.** A per-column entry is an ordinary issue record: `code`, `severity`,
  `message`, `since` — exactly the four fields §5.5 defines (`05-health-kv.md:92-105`), and exactly what
  the shipped struct carries (`health.go:86-96`). Its "a resolved issue is simply absent" semantics
  (`05-health-kv.md:110`) is what retires it when the set empties.
- **`since` means "when this column last went from no open rows to some".** §5.5 defines `since` as when
  the issue *"first arose"*, and for this entry that is what it is: a retirement deletes the key's `since`
  (`health.go:331-343`), so the next open row after the last one closes arrives as a new issue with a fresh
  stamp, while a count that merely changes leaves the stamp alone. That is the honest reading of "first
  arose" for a level-reconciled per-column fact, it is what an operator wants (how long has this backlog
  been non-empty, not how long since the count last moved), and it needs no contract wording beyond the
  clause above.
- **The count rides in `message`, and a machine-readable count would need a Contract #5 change.** §5.5's
  record has no numeric field, so the number is prose — the same posture Loupe already reads Weaver's
  target-scoped facts with, by parsing the message text (`cmd/loupe/weaver.go:388-395`). A future consumer
  that wants the count programmatically is asking for a new §5.5 field, which is a Contract #5 amendment
  and out of scope here. Naming it now is cheaper than having it discovered by whoever tries.
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

### 7.2 "Does this lose the operator something?" — every surface, priced

`issuesNaming` (`cmd/loupe/weaver.go:388-428`) attributes a heartbeat issue to a page by matching a token
as a **whole word** inside the issue *message*. It has **three** call sites, and Inc 1 lands differently on
each:

| Loupe surface | Call site | What it shows today | After Inc 1 |
|---|---|---|---|
| **Entity detail** — the per-entity issue panel | `:1128` (`issuesNaming(hbs, entityID)`) | the row's own `gap:` entry, whose message names the entity | **the surface entry stops appearing** — the new message names the target and column, not an entity. This is the real cost, priced below |
| **Target detail** — the target's issue panel | `:746` (`issuesNaming(hbs, targetID)`) | one entry per open row, up to the document cut | **one entry per gap column**, naming the count. Strictly better: the panel goes from a truncated sample of identical warnings to the whole fact |
| **Targets list** — `Issues: len(issues)` + `frozenBy(issues)` | `:1244` | a count of matched entries, driving the "N issues" chip (`web/js/views/weaver.js:104`) and the sort rank `if (t.issues > 0) return 3` (`web/js/logic/weaver.js:36`) | the chip falls from a truncated ~50 to **one per gap column**. A target whose only issues were surface entries keeps a non-zero count, so it still ranks above the quiet ones — but it **may sort down** relative to a target carrying many fault entries, which is the correct ordering once the number means faults |

**No Loupe code change is required for any of the three.** `messageNamesToken` matches the target id as a
whole word, and the proposed message begins `"target unroutedTasks: …"` (§4.1) — the same
`"target <targetId> …"` convention every target-scoped message already follows, and the convention
`issuesNaming`'s own doc comment names as the attribution mechanism. A Loupe-lane row to re-tune the chip's
wording or the sort rank for the new cardinality is **optional**, not a prerequisite, and is not filed here.

**The one real loss is the entity panel, and three reasons it is the right trade:**

- **It already fails at exactly the scale this design is about.** Loupe reads the *published* document,
  which is truncated to 50 entries severity-first with per-entity families ranked **last**
  (`health.go:36`, `:921-968`). Past ~50 open issues the entity's own entry is almost certainly not in the
  document. The drill-down works only when the backlog is small enough not to matter.
- **The same page already shows the fact, from the row.** `weaverEntityGap` renders `State: "open"` for a
  gap column read directly off the weaver-targets row (`cmd/loupe/weaver.go:1109-1119`). For a surface gap
  — no mark, no dispatches — that *is* the whole fact the Health entry restated.
- **Loupe already ruled the same way once.** F17 explicitly declined to duplicate the `UnroutedTasks`
  Health issue into `/api/tasks`, on the grounds that it renders authoritatively on the Weaver component
  page: *"The inbox's per-row `stuck` flag is the actionable **drill-down** from that issue"*
  (`loupe-f17-queue-observability-ux.md:55-59`). Inc 1 makes the platform agree with that decision.

The residue worth naming: an operator who wants the *list* of open rows reads them from the target's row
set — the projection the surface gap is computed from — which is where they are authoritative and complete,
rather than from a truncated sample in a heartbeat.

**Every other Health-KV reader of these issues, and what each sees.** None needs a change; the row exists so
that "no other consumer" is a checked claim rather than an assumption.

| Reader | What it reads | After Inc 1 |
|---|---|---|
| Loupe **component health page** | flattens each `issues[]` entry to `"[severity] Code: message"` and takes the worst severity (`cmd/loupe/health.go:131-189`) | N identical lines collapse to one per column — an improvement; severity rollup unchanged |
| Loupe **system map** and **lenses** pages | the component node's status + issue lines from the same flattening (`cmd/loupe/systemmap.go:476`; `cmd/loupe/lenses.go:99`) | severity rollup only; unchanged |
| `lattice health` **CLI** | `issueSeverities` — severity strings only (`cmd/lattice/health/health.go:210-227`) | unchanged |
| **Lamplighter** skill | dedups candidate findings on the issue **code**, and is told explicitly not to dedup on identifiers that appear only in the message (`agents/lamplighter/SKILL.md:38,55,74`) | unchanged — the code is package-declared and Inc 1 does not touch it. Fewer duplicate entries to sift |
| `internal/unroutedconvergence` | `hasIssue("UnroutedTasks")` / `issueCleared` — asserts the **code** only, one entity (`unroutedconvergence_test.go:186-250`) | **passes unchanged**, and is the e2e proof the contract promise still holds |
| `internal/healthkv/completeness_test.go` | key presence only, `integration` tag | unchanged |

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
back under the cap"* while the shipped code retires it only at **zero** (`releaseRowIssueLocked`
`health.go:282-291` calls `retireRowIssueOverflowLocked` `:296-301` on that condition alone — the *slot*
is what frees under the cap), and its budget row lists **three** per-entity families where the shipped test
is a key shape covering four. The second of those drifts also sits in the shipped `rowIssueCapPerTarget`
comment, which is the copy a builder actually reads — Inc 1 fixes it there (§4.1), because a correction
that lands only in a design doc does not reach the next person.

### 7.5 "Does anything in flight change what the count means?"

One thing, and it converges. `expiry-as-a-recorded-fact-design.md` (RATIFIED 2026-09-01) converts
`unroutedTasks` and `staleAssignedTasks` — both `surface` targets, and the two whose backlog this design's
worked example is built on — from reading `$now` in the lens to reading a **recorded** lapse. After both
land, a task's membership in the per-column set begins at its expiry *marker*, not at its `expiresAt`
instant, so between the two the count reads one lower than a wall clock would say. It closes on the next
marker commit; the severity is `warning`; and the count is already CDC-latency-dependent, so this adds a
second small latency to a number that was never instantaneous. The sibling design names the same
interaction from its side. Neither increment needs to sequence around the other.

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

**Row 2 — re-derivability-ranked admission (what the filing prescribed). REFUTED.**
Three independent failures, any one fatal:

- **The named filler is itself non-re-derivable.** The filing's scenario is a `surface` backlog crowding out a
  `RowDataError`. But the surface arm Acks (`evaluator.go:333`) and mints no mark or `__count`, so no sweep
  leg ever revisits it (`reconciler.go:701-707`) — it is in the *same* re-derivability class as the fact it
  is accused of crowding out. A ranking on re-derivability would not evict a single surface entry.
- **The contract citation that motivated the ranking is false.** §10.8's exhaustion fact *is* re-derived,
  every minute, from `sweepCount`/`reclaim`. Under a re-derivability ranking it would be the **first** thing
  refused — the policy would preferentially discard the one fact the filing invoked the contract to protect.
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
- **The count tracks every membership change, not just the last one.** With N entities open, one closing
  leaves the entry standing with a message reading **N−1** — and its `since` unchanged. This is the vector
  that separates the design from the obvious wrong implementation, in which a remove that merely shrinks
  the set leaves a stale-high count on the board until the whole backlog drains.
- **A gap column carrying a `.` stays out of the budget.** The key-shape guard is what keeps the workload
  population out (§4.1 property 1), and a dotted column is the one input that would give
  `gapOpen:<t>.<col>` a second separator below the target. Assert `rowIssues[target]` is **0** for it.
  Two adjacent properties are free and need no assertion: `target.Gaps` is a map keyed by column, so one
  column yields exactly one `issueCode`; and `"gapOpen:"` is not prefix-matched by `"gap:"`.
- Budget non-interference — the point of the increment: with 600 entities holding a surface column open,
  `rowIssues[target]` is **0** and a subsequently raised `RowDataError` is **admitted**. This is the
  regression the board row describes, expressed as a test.
- Teardown, one vector each: entity column closes; entity tombstone; target `Revoke`; target unregistered
  via `reconcileConsumers`. Plus the vector the tombstone leg exists for — a membership recorded, the
  **column then dropped from the playbook**, then the entity tombstoned: the set must still lose the
  entity, because the candidate walk no longer yields that column (§5 removal leg (b)).
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
- **The window actually resets.** Two heartbeats with refusals only before the first: the second entry
  reads **0** for the window. This is the assertion that catches the counter being wired to a read
  (`snapshot()` has no reset leg) instead of to the boundary.
- **The counter drains with its target.** Refusals recorded under two families of one target, then the
  target's per-row set empties: `retireRowIssueOverflowLocked` leaves nothing behind for either family —
  the nested keying's whole justification.
- A mutation check on the first two: with the seam change reverted, the tests fail.

**Gates.** `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`,
`go test ./internal/weaver/... ./cmd/loupe/...`, `make test-unrouted-convergence`, and every
`scripts/lint-*.go`. `make test-unrouted-convergence` is the **only** build-tagged harness either
increment's interfaces reach — the change is confined to the issue cache and the evaluator's raise/retire
legs, and touches no engine or service interface a control-plane or convergence double implements. No
`packages/` content changes, so no manifest version bump.

---

## 10. Decomposition for the Steward

**One fire, size M, Inc 1 first.** Both increments land in the same two files (`internal/weaver/health.go`
and `internal/weaver/evaluator.go`), share the same four fixture files, and Inc 2's seam repairs read the
refusal branch Inc 1 stops reaching — so splitting them would mean two fires editing the same functions and
the same test files, with the second re-deriving the first's premises. They are decomposed here for review
order and gate depth, not for scheduling.

| Inc | What | Size | Posture-changing? |
|---|---|---|---|
| **1** | Move the `surface` population out of the issue cache: new `gapOpen:` family, `surfaceStats` member set, raise/retire on the existing call sites, per-entity write and clears **removed**, tombstone removal across the target's column sets, `surfaceOnlyGap`'s dead latch rationale rewritten, `rowIssueCapPerTarget`'s family list corrected, listing classification, fixture migration, schema doc | S–M | **Yes** — it changes an observable Health-KV shape and carries the contract edit. Full review depth. |
| **2** | Refusal-honest seams: `setSince`/`pacedRaise` report refusal; `alertStanding` and `alertPaced` keep their loudness discipline under refusal, paced per (target, family); overflow message corrected, with a windowed refusal count reset at the heartbeat boundary | S | No. Ordinary depth. |

Neither is dead scaffolding: both have a live consumer today. Inc 1 realizes the whole payoff on its own
(the budget stops being reached by healthy work) and goes first — Inc 2's flood row is reached far less
often once it lands, and its fixture migration touches the same test files Inc 2's tests extend
(`evaluator_internal_test.go`, `replay_internal_test.go`, `decline_retry_internal_test.go`,
`health_internal_test.go`).

---

## 11. Risks + residuals

- **The count is a lower bound after a restart** until every open row re-projects, and `ReplayTarget` is
  the verb that fixes it. Inherited unchanged from the entries it replaces, and identical to
  `contractionStats`' shipped posture. Stated in the message wording so an operator is not misled.
- **A message whose value changes on every membership change sits at one latch key.** Safe here and only
  here: the surface arm uses bare `issues.set`, so it touches neither `standingAs` (which deliberately
  ignores messages) nor `pacedRaise` (which would re-arrive on a changing count). If a later change routes
  this raise through a pacing seam, the count must move to a metric. Worth a line in the code comment.
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
  notice, not harder. The *tombstone* half of the same hazard — a dropped column whose entity then goes
  away — is closed outright by §5's removal leg (b), which is why that leg does not ride the candidate walk.
- **`gapOpen:` must be added to `issueKeyTargetPrefixes`** or a revoked target strands its entries. The
  teardown test above is what catches it; `issueKeyTargetPrefixes`' own doc states the rule.
- **Multi-instance Weaver:** lane-1 durables are per target under a shared name (`engine.go:477-490`), so
  with more than one Weaver a target's rows shard across instances and each instance's heartbeat carries
  the count *it* observes. Loupe already reports per-instance rather than merging
  (`cmd/loupe/weaver.go:315-318`, whose comment gives the reason). Unchanged by this design; called out
  because a count is exactly the kind of number that invites a merge, and the merge would be wrong. The
  contract clause in §6 says so on the wire rather than leaving it to a reader's assumption.
- **Not addressed:** what an operator does *with* 500 concurrently-broken rows on one target. After Inc 1
  the cap only fires in that state, the overflow entry says so honestly, and `ReplayTarget` re-derives.
  Raising the number without a measured fault distribution is a guess, so it is deliberately not done.

### Ratification pass

Due diligence against the pinned sources changed the following, and each is folded into the body above
rather than left as an erratum:

1. **§6 — the contract's count clause.** The staged sentence promised *"the number of rows currently
   holding the column open"*, which the engine cannot keep: a surviving lane-1 durable resumes from its
   acked floor, and lane-1 durables are per target, so with more than one Weaver the rows shard. §6 now
   carries the exact replacement clause of record; it lands with the fire's commit.
2. **§5 — the count went stale-high on every close but the last.** The retirement leg is a *remove*, so an
   entry written only on raise would sit at N while the set fell. The entry is rewritten on every
   membership change, add and remove alike, and §9 asserts the N−1 vector.
3. **§7.2 — three Loupe surfaces, not one.** `issuesNaming` is called from entity detail, target detail and
   the targets list; only the first was priced. All three are now, along with every other Health-KV reader
   and what each sees. No Loupe code change is required.
4. **§1.1 / §1.3 — "released only at zero" conflated the slot with the overflow entry.** A slot frees on
   every clear; only the overflow entry waits for zero. The Error flood is therefore bounded by how long
   the target stays at cap, which for a static surface backlog is the whole backlog's life — the worst
   case, not the general one.
5. **§4.2 / §5 — the windowed refusal count needed a lifetime.** `snapshot()` has no reset leg, so
   "refusals since the last heartbeat" gets an explicit boundary; the counter and its loud clock are keyed
   per (target, family) as a nested map, so the existing per-target teardown drains them.
6. **§5 — removal leg (b) leaked a membership.** A column dropped from the playbook after a membership was
   recorded never yields from the candidate walk on a tombstone. The leg now removes the entity from every
   column set of the target, the in-memory analogue of the prefix clear it replaces.
7. **§5 / §6 — `since` semantics named.** A retirement deletes the key's `since`, so a reopened column
   arrives with a fresh stamp: the entry's `since` means *when this column last went from no open rows to
   some*, and that is stated where a consumer reads it.
8. **§4.1 / §7.4 — two slips.** There are **ten** family prefixes, not nine; and
   `rowIssueCapPerTarget`'s own comment names two families where the key-shape test covers four. Inc 1
   fixes the shipped comment, not just the design doc that first flagged it.
9. **§9 — a dotted gap column** would give the `gapOpen:` key a second separator and re-enter the budget;
   Inc 1 asserts it.
10. **§10 — one fire.** Both increments land in the same two files and share the same four fixture files,
    so they ship together, Inc 1 first, size M.
11. **§7.5 — sibling interaction.** The ratified expiry-as-a-recorded-fact design converts both
    `orchestration-base` surface targets to a recorded lapse, which makes the count marker-latency-
    dependent between deadline and marker. It converges; neither design sequences around the other.
12. **Two quotes now match their sources.** The header quotes the lane row's current title, and §10 no
    longer carries a board item — the row correction landed with this design's own commit, and the one
    phrase still outstanding is named in §1.1. §7.2's F17 citation is requoted verbatim.

---

### Fire brief (build note, 2026-09-04) — one fire, Inc 1 then Inc 2

**1. Scope (verbatim, ratification banner).** *"a `surface` gap raises one counted entry per (target, gap column);
per-row identity stays in the target's projected row set. One fire, size M, Increment 1 first. Contract #10
§10.8's `surface` clause of record is in §6 and lands WITH the fire's commit."* Green bar: §9's Inc 1 + Inc 2
vectors, `make test-unrouted-convergence` unchanged and green, all `scripts/lint-*.go` STRICT, CI green.

**2. Verified touch-list (checked live at `3f77318b`).** Every §2 ledger row re-verified; only `temporal.go`
rotted (+7 lines: raises at `:127`, `:144`, `:165`). Censuses re-run: **C1 = 7** (`privacy-base/targets.go`
now `:194`, `:199`), **C3 = 11**, `gapOpen` unused anywhere.
- `internal/weaver/health.go` — `rowIssueCapPerTarget` + comment `:46-62` · cache struct `:188-197`
  (`refused`, `refusedWorst`) · `set` `:213` · `setSince` `:236-257` · `setLocked` `:259-268` ·
  `releaseRowIssueLocked` `:282-291` · `retireRowIssueOverflowLocked` `:296-301` · `clear` `:331-343` ·
  `pacedRaise` `:380-407` (refusal `return false, now` at `:386`) · `clearPrefix` `:463-501` ·
  `snapshot` `:504-519` · heartbeat consumer `emit` `:709-710` (`prunePaced` then `snapshot`) ·
  `listingRank` `:957-968` · `perEntityIssueFamily` `:995-1002`.
- `internal/weaver/evaluator.go` — surface arm `:316-334` · `surfaceOnlyGap` `:585` · `clearClosedMarks`
  `:989` (tombstone leg + three prefix clears `:1005-1011`; candidate walk retire `:1064`; surface no-mark
  branch `:1066-1069`) · `retireClosedGapIssues` `:1176-1194` · `surfaceOnlyGap` guard in
  `escalateExhaustedGap` `:1542-1560` · `markCandidateColumns` `:1696-1712` · `alert` `:1753` ·
  `alertStanding` `:1775-1782` · `alertPaced` `:1819-1830` · prefix consts `:1835-1846` ·
  `perEntityIssuePrefixes` `:1864-1869` · `targetScopedIssuePrefixes` `:1875-1882` ·
  `issueKeyTargetPrefixes` `:1911-1919` · `issueKeyGapEntity` `:1927` · `rowIssueTarget` `:2075-2093`.
- `internal/weaver/contraction.go` — `contractionStats` + doc `:18-77` (the mirror; `surfaceStats` lands here).
- `internal/weaver/engine.go` — `Engine` fields `:280-292` (`contraction` at `:288`; constructor `:381`) ·
  `reconcileConsumers` teardown `:570-590` (`issueKeyTargetPrefixes` loop `:579`, `republish.clearTarget`).
- `internal/weaver/control.go` — Revoke teardown `:244` · `ReplayTarget` `:460`.
- `internal/weaver/reconciler.go` — `defaultSweepInterval` `:21` · `escalateExhaustedGap` calls `:634`,
  `:943` · surface skip `:701-707` · `retireClosedGapIssues` callers `:568`, `:792`, `:1237`.
- Tests to migrate: `evaluator_internal_test.go` `:1303-1332`, `:1471`, `:1577`, `:2037`, `:2052`, `:2414` ·
  `replay_internal_test.go` `:388-416` · `decline_retry_internal_test.go` `:181`, `:379`, `:394`,
  `:1142-1163` · `health_internal_test.go` `:333`, `:423`, `:457` · `listing_rank_internal_test.go:15`.
- Docs: `docs/observability/health-kv-schema.md` `:943-1042` · `docs/components/weaver.md:1174` (Actions row,
  `surface` sentence) · `docs/contracts/10-orchestration-weaver.md:169` (§6 clause, lands with the commit).
- `cmd/loupe` — **no change** (§7.2); `web_logic_weaver_test.go:203` renders the action, unaffected.

**3. Precedents.** Member set = `contractionStats.observe` (`contraction.go:60-77`: transition-only, admit on
first open sighting, lower-bound posture). Family prefix + teardown = the ten consts + `issueKeyTargetPrefixes`
(`evaluator.go:1835-1919`). Tier classification = `targetScopedIssuePrefixes` + the pin
`TestListingRank_EveryIssueFamilyIsClassified`. Target-scoped message convention `"target <id>: …"` =
`GapWithoutPlaybook` (`evaluator.go:305`). Nested per-target teardown = `retireRowIssueOverflowLocked`'s
`delete(m, target)`. Heartbeat-boundary hook = `prunePaced` at `health.go:709`.

**4. Increments + green checks (Winston's implementation decisions, recorded here).**
- **Inc 1.** (a) `issuePrefixGapOpen = "gapOpen:"`, `issueKeyGapOpen(t, col)`; add to `targetScopedIssuePrefixes`
  (tier 2) and to `issueKeyTargetPrefixes`. `rowIssueTarget`'s switch does not match it, so it leaves the budget
  by construction; §9's dotted-column vector pins that. (b) `surfaceStats` in `contraction.go`:
  `map[target]map[col]*surfaceColumn{code, severity, members map[entity]struct{}}`; methods `add(t, col, e, code,
  sev) (n int, changed bool)`, `remove(t, col, e) (col surfaceColumn, changed bool)`, `removeEntity(t, e) []changed
  columns`, `removeTarget(t)`. Code/severity are stored at add so a remove can rewrite the entry without the
  `*Target` (`retireClosedGapIssues` has none). (c) Engine helper `reflectSurface(t, col, sc)`: `n>0` ⇒
  `issues.set(issueKeyGapOpen, sev, code, "target T: N rows have column C true")`; `n==0` ⇒ `issues.clear`.
  Written only on a membership *change* (a repeat delivery is a no-op, the transition rule). (d) Surface arm
  `:331` calls `add` + reflect; the per-entity `issues.set` is deleted. (e) `retireClosedGapIssues` adds
  `remove` + reflect — its existing `issues.clear(issueKeyGapEntity…)` **stays**: that latch still carries
  `GapBudgetExhausted` / `GapEscalatedToAugur`, and so does the tombstone leg's `gap:` prefix clear (the design's
  "its clears are removed" means the *surface* writes; the `gap:` clears serve the two exhaustion codes and are
  not surface-specific). The sweep legs at `reconciler.go:568/792/1237` reach the same helper; a surface column
  has no mark, so their `remove` is a no-op there and harmless. (f) Tombstone leg `:1005`: `removeEntity` across
  the target's column sets + reflect each. (g) Both teardowns (`control.go:244`, `engine.go:579`):
  `surface.removeTarget(id)` beside `republish.clearTarget` — the prefix clear retires the entries, this retires
  the set. `ReplayTarget` needs no clear: DeliverLastPerSubject re-presents every current row incl. tombstones,
  so the set re-derives. (h) Rewrite the latch half of the `surfaceOnlyGap` guard comment (`:1542-1560`);
  fix `rowIssueCapPerTarget`'s family list to four (`gap:`/`data:`/`template:`/`sweep:`); rewrite the surface arm
  comment and `clearClosedMarks`' `:1066` comment. (i) Fixture migration per C2; cap/listing stress fixtures
  re-seed with `RowDataError` at `issueKeyDataEntity`. (j) Schema doc: new key-table row, the "N entries carrying
  the SAME code" paragraph rewritten to the per-column entry, `gap:` row's codes drop the surface codes, budget
  paragraph says four families + `gapOpen:` excluded. `weaver.md:1174` surface sentence. Contract §10.8 `surface`
  row: append the §6 clause after "…at `issueSeverity` (default `warning`)". Green:
  `go test ./internal/weaver/... -count=1`, `make test-unrouted-convergence`.
- **Inc 2.** (a) `rowIssueFamily(key) (target, family string, ok bool)` beside `rowIssueTarget` (family = the
  prefix const). (b) Cache state: drop the monotone `refused map[string]int`; add `refusedWindow
  map[target]map[family]int` and `refusedLoudAt map[target]map[family]time.Time`; `retireRowIssueOverflowLocked`
  deletes both `[target]`. (c) `setSince` returns `refused bool`; the refusal increments the window and rewrites
  the overflow entry with the honest message: *"target T: per-row issue tracking reached its cap of 500 entries;
  N raises for untracked rows were refused since the last heartbeat (data: a · gap: b · template: c · sweep: d).
  Refused template: and exhaustion facts re-derive on their own cadence and land when a slot frees; refused data:
  and sweep: facts are not re-derivable until those rows project again."* `set` returns the same bool.
  (d) `refusedLoud(key, now) bool` — loud iff no stamp or `now-stamp >= logPaceInterval`, stamping when loud.
  (e) `alertStanding`: `standing := standingAs(); refused := set(); refused ⇒ level by refusedLoud; standing ⇒
  Debug; else Error`. (f) `pacedRaise` refusal path returns `c.refusedLoudLocked(target, family, now), now`
  instead of `false, now`. (g) `rollRefusalWindow(now)` on the cache: rewrite every standing overflow entry's
  message from the window counts, then zero them; called from `emit` immediately before `prunePaced`
  (`health.go:709`). (h) Schema doc: overflow message + the "refused" paragraph; cache-struct lifetime table
  in `health.go:160-197` updated. Green: `go test ./internal/weaver/... -count=1` incl. §9 Inc 2 vectors with a
  fake clock; revert-proof (e) and (f).
- **Fire close.** `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`, `go test
  ./internal/weaver/... ./cmd/loupe/... ./internal/healthkv/...`, `make test-unrouted-convergence`, every
  `scripts/lint-*.go` with `STRICT=1`. No `packages/` edit ⇒ no version bump. Cycle `bin/weaver` via
  `pkill -x weaver && make orchestration` (MERGED ≠ RUNNING).

**5. Gotchas + dossier (copied).** CLAUDE.md: no history comments; Health-emission change ⇒ schema doc in the
same commit; contract clause lands with the commit (Andrew 2026-09-01, no transitional note). Weaver dossier
entries this fire trips: *A Health issue key is a LATCH — enumerate every OTHER leg that raises at that key before
adding a clear* (the `gap:` latch is shared with the exhaustion codes: keep its clears); *A per-entity Health issue
is unbounded, and the heartbeat is ONE KV value — select the listing by SEVERITY, classify the new family
deliberately*; *A fact ends by more routes than the one you are editing — enumerate the LEGS* (lane-1 close,
tombstone, sweep row-gone/count/mark legs, Revoke, reconcileConsumers); *Prove each changed line by reverting
THAT LINE*; *A presence assertion cannot pin a clear whose caller re-raises in the same pass — the STAMP is the
observable* (§9's N−1 vector asserts `since` unchanged); *A shared test fixture that always supplies an OPTIONAL
input pins only the supplied case*. Standing checklist #1 (lifetime table = §5, done), #2 (censuses re-run
above), #3 (revert-proof every seam change in the worktree, never the shared tree), #4 (the per-entity surface
write is REPLACED: obligations = raise, close-retire, tombstone-retire, target-teardown-retire, listing tier,
budget membership — each accounted in part 4), #5 (one writer per `gapOpen:` key: the reflect helper), #6.

**6. Adjacent finds.** None at Phase 0 beyond the design's own §11 residuals, all inherited and priced there.

**7. Non-goals.** No change to `rowIssueCapPerTarget`'s value, no Loupe code, no `packages/` edit, no new
Contract #5 field (the count rides in `message`), no admission ranking or eviction (§8).
