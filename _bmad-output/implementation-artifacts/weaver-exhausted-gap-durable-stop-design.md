# Weaver — an exhausted gap's loud stop must outlive the suppression that causes it

> **✅ Winston-ratified — build-ready (2026-08-25).** Every open question in this document is an
> implementation call and is answered here (Steward §0/§2.5). Where this banner and the body disagree,
> the banner wins.
>
> **CORRECTED 2026-08-25 — this banner's original "no frozen-contract change is proposed or required"
> was WRONG, and a cold review caught it.** The *behaviour* is a conformance fix — Contract #10 §10.8
> promises "a loud stop, never a silent park" twice and this fire delivers it. But §10.3's reserved-key
> paragraph (`10-orchestration-substrate.md`) states *"The reconciler sweep skips all three (never
> enumerated as `CorruptMark`)"*, and the count leg enumerates `__count` and can raise `CorruptMark` on
> it. That sentence is **already self-contradicted** by the same table's `__effect` row ("GC'd by the
> sweep's orphan legs"), so this fire widens pre-existing drift rather than creating it — which is a
> reason to correct the text, never to ship past it. The correction is prepared as a proposal branch,
> `claude/contract-10-weaver-state-sweep-enumeration` (branch-vs-main diff IS the proposal; ratify =
> Andrew merges), and flagged on the board. Per §0 the code still ships at L2: the behaviour is
> revertible, the bucket is weaver-private, and no consumer outside `internal/weaver` observes it.

**Board row:** `[Weaver] An exhausted gap's loud stop dies at restart and cannot be un-parked`
(`backlog/lattice.md`, Component maintenance, ★★ · M).

**Filed as `📐 needs designer pass · no-pattern: durable re-armable exhausted-gap state + un-park path`.
That gate is REFUTED here, on the record** (board's honest-designer-gate rule: *"If a precedent exists in
the touched file it is a steward `📋`, not a designer `📐`"*). Three precedents, all in the files this
fire edits:

| The claimed-absent pattern | The precedent that already ships |
|---|---|
| durable exhausted-gap state | `state.go:263` `countKeySuffix` — the `…__count` dispatch-count IS durable per-(target, entity, gap) state, TTL 128 h (`state.go:64`) |
| re-derivation of an in-process alert from durable state | `reconciler.go:153` `sweeper.pass()` — *every* Weaver issue family is in-process only (`health.go:76`) and is re-derived by re-evaluation each pass; `sweepEffect` is a second, differently-keyed orphan leg in that same loop (`reconciler.go:168`) |
| an operator verb that drains reserved weaver-state keys | `cmd/lattice/weaver/weaver.go:239` `reset-confidence <targetId>` → `markStore.deleteEffectWindows` (`state.go:718`) |

## 1. The defect, grounded

Contract #10 §10.8 states the promise twice:

> *"…then raises the §10.8 `GapBudgetExhausted` standing issue — **a loud stop, never a silent park**"*
> — `docs/contracts/10-orchestration-weaver.md:71`
>
> *"Budget exhaustion on a planned gap raises a standing Health issue at the suppression site
> (**never a silent park**)."* — `docs/contracts/10-orchestration-weaver.md:312`

The implementation raises the loud stop correctly and then loses it, by two independent routes.

**The alert is in-process.** `escalateExhaustedGap` (`evaluator.go:1091`) raises via
`e.alert(issueKeyGapEntity(...), "warning", "GapBudgetExhausted", …)` at `evaluator.go:1104`. `issueCache`
(`health.go:76`) is a bare `map[string]healthIssue` behind a mutex — process-local, no durable backing.
Weaver's startup (`engine.go:333` `Engine.Start` → `seedDisabledTargets`, `control.go:43`) rebuilds the
`__control` disabled-set and **nothing else**. So a restart drops the alert, and only a re-evaluation of
the same gap can re-raise it.

**Neither re-evaluation leg survives.** There are exactly two:

- *Lane 1* (`evaluator.go:156`) fires on a CDC delivery of the row. An exhausted gap produces no row
  change of its own, so a quiet row is never re-delivered.
- *The sweep* (`reconciler.go:508`) is the other, and the code says so itself: *"the sweep is the ONLY
  dispatch leg that still visits a row with no fresh CDC deliveries, so it is this site — not lane-1 —
  that must actually close the §10.8 'never a silent park' promise for a row that has gone quiet."*
  But the sweep reaches that site only from `sweepMark`, i.e. **only while a mark exists at
  `<targetId>.<entityId>.<gapColumn>`.**

**And the mark is the one thing an exhausted gap stops refreshing.** A mark's TTL is
`markTTLBackstopFactor × MarkLease` = 2 × 30 min = **60 min** (`state.go:44`, `reconciler.go:17`), re-armed
*only* on a reclaim (`state.go:211`). The exhausted branch returns without reclaiming — deliberately, and
documented at `evaluator.go:1089`: *"it never touches this gap's own (already-exhausted, possibly
already-expired) mark."* So the mark decays and is TTL-deleted, and with it the last leg that could
re-raise the alert.

**The suppression, meanwhile, persists.** `gapSuppressed` (`evaluator.go:1017`) suppresses on
`count >= capN`, reading the `…__count` key whose TTL is `dispatchCountTTLBackstopFactor × MarkLease`
= 256 × 30 min ≈ **128 h** (`state.go:64`). And `pass()` explicitly skips it (`reconciler.go:177`):
*"The sweep never enumerates, reclaims, or deletes either; the count's gap-close reset and its long TTL
backstop are its only lifecycle."*

**The measured asymmetry — this is the whole bug:**

| | lifetime | what it does |
|---|---|---|
| the alert's carrier (the mark) | **≤ 60 min**, or **0** across a restart | the only thing that re-raises `GapBudgetExhausted` |
| the suppression (the count) | **≈ 128 h** | keeps the gap from ever dispatching |

⇒ a window of **≈ 127 hours (≈ 5.3 days)** — immediate after any restart — in which the gap is
**suppressed, un-dispatched, and silent**. That is precisely the silent park §10.8 forbids.

**And it is a closed loop.** The count clears on gap-close (`evaluator.go:829` `deleteDispatchCount`),
gap-close needs a remediation dispatch, and dispatch is what the count suppresses. Absent an operator
changing `maxretries_<g>` in the package, nothing in the system re-arms it — there is no un-park verb
anywhere in Weaver (grep: no `unpark`, no `rearm`, no `acknowledge`).

## 2. The invariant this fire installs

> **An alert that explains a suppression must be re-derivable from the same durable state that causes
> the suppression, for exactly as long as that suppression lasts.**

The corollary decides the whole design: **do not add new state.** The count already *is* the durable
record of the suppression, with the right key granularity and the right lifetime. The alert must be
re-derived *from the count*, not stored beside it. This satisfies the standing checklist's first
rule (*"new state needs a lifetime, not a data structure"*) by introducing no new state and therefore
no new lifetime.

## 3. Design — a count-anchored sweep leg

`pass()` currently routes weaver-state keys three ways: `…__effect…` → `sweepEffect`, `…__control` /
`…__count` → skip, everything else → `sweepMark`. **The `…__count` arm stops being a skip and becomes
`sweepCount`** — a fourth leg in the same loop, mirroring `sweepEffect`'s shape exactly (reserved-marker
split, level reconcile against the current row, revision-conditioned delete).

`sweepCount(ctx, key, listed)` — arms in order, each with its stated reason:

| # | condition | action | why |
|---|---|---|---|
| 1 | key does not split into `<t>.<e>.<g>.__count` | `deleteCorrupt` + `CorruptMark` alert | weaver-state is weaver-private; garbage otherwise lives forever (`sweepMark` arm (a)) |
| 1b | count **value** does not unmarshal | `deleteCorrupt` + `CorruptMark` alert | both sibling legs delete a corrupt value; this leg must too, and here it is not cosmetic. A garbled body makes `getDispatchCount` error, `gapSuppressed` read its safe (dispatchable) side, and `incrementDispatchCount`'s read-modify-write fail the same way — so the budget can never accumulate and a `directOp` gap retries unbounded, which is the exact outcome `defaultDirectOpRetryBudget` exists to prevent. Deleting re-arms the budget from 0, which is what the garbled body already yields, but leaves a key that can accumulate again |
| 2 | mark key `<t>.<e>.<g>` present in **this pass's** `listed` set | return | the mark leg owns this gap; never escalate twice in one pass. Uses the set `pass()` already builds (`reconciler.go:159`) — no extra KV read |
| 3 | target not registered | **return — never delete** | an unreplayed target is replay lag, not absence; deleting the budget here would re-arm every parked gap on every restart. (Dossier: *"an `error`-severity Health issue must not fire on a self-healing condition"* — the same trap, one severity down) |
| 4 | target disabled (`__control`) | return | mirrors `sweepMark` arm (d) and lane-1's Ack-skip: an operator freeze stops dispatch, and escalation is dispatch |
| 5 | row read → `ErrKeyNotFound` | **clear `issueKeyGapEntity` only — never delete the count** | **CORRECTED 2026-08-25 after cold review.** The original arm deleted the count here and was wrong. Absence is not evidence: a Refractor lens rebuild purges a target's rows and re-replays them, and a registered, enabled target reads row-gone for every entity inside that window. The thing destroyed is the retry *bound itself*, so a mass delete re-arms exactly the storm `defaultDirectOpRetryBudget` exists to prevent — while the delete buys only prompt GC of a ~20-byte key that the 128 h TTL (`state.go:64`) already collects risk-free. Arm 3 states this rule one arm earlier; this arm must not break it. Contrast arm 7: a *present* row whose column reads false is positive evidence, and keeps its delete |
| 5b | row read fails for any reason other than `ErrKeyNotFound` | Warn + return | a transient KV error is not evidence the entity is gone; mirrors `sweepMark`'s read-failure posture |
| 6 | row value unparseable | return | never act on unreadable evidence (`sweepMark`'s posture, `reconciler.go:270`) |
| 6b | row carries no `entityKey` | return | `escalateExhaustedGap`'s augur arm feeds `entityKey` to `planGap`; the mark leg reaches that call only past the reclaim's non-empty guard, and this leg has no mark to have been guarded. Lane 1 raises `RowDataError` and declines to dispatch a violating row with no `entityKey` (`evaluator.go:87`), so this leg must not dispatch one either — never act on incomplete evidence |
| 7 | `missing_<g>` not true | delete count + clear issue | the gap closed without a mark to carry the level reconcile |
| 8 | row not `violating` | return | mirrors lane-1's L1 gate and `sweepMark`'s violating gate (`reconciler.go:481`) |
| 9 | `gapSuppressed` → `exhausted` | `escalateExhaustedGap` | **the fix.** Same site the mark leg calls (`reconciler.go:508`), now reachable from state that outlives the mark |
| 10 | `gapSuppressed` → suppressed, not exhausted (`inflight_<g>`) | return | a call is in flight; the lens will re-project when it lands, and lane 1 re-delivers |
| 11 | not suppressed, **no** mark | clear the issue, then **dispatch** | the re-arm. See §4 |

**Arm ORDER is load-bearing, and `sweepMark` already fixes it (CORRECTED 2026-08-25 after cold
review).** `sweepMark` runs its level reconcile *unconditionally, above* the registry and `__control`
gates, and says why: *"Runs regardless of the target's `__control` freeze: closing an already-satisfied
gap is cleanup, never new dispatch"* (`reconciler.go:231`). The first build put the registry and freeze
gates above the level reconcile, so a frozen target's closed gap kept its spent budget and a standing
issue **forever** — and on re-enable a reopened gap was instantly suppressed against a budget spent by
a chain that had already closed. The leg therefore orders: **corrupt key → level reconcile (arms 5, 7)
→ mark-listed → registry → freeze → corrupt body → entityKey → orphan column (8b) → violating → the
acting arms.** (An earlier revision of this list put the corrupt body *above* the freeze gate,
contradicting the sentence that follows it; the build's own test caught the contradiction before the
code did. The reason governs, and it only holds below both gates.) Arm 8b sits above the `violating`
gate: its verdict — Weaver no longer manages this gap — does not depend on `violating`, and below that
gate the clear it exists for is unreachable for a non-violating row whose orphan column is still true.
The corrupt-*body* delete moves
*below* the registry and freeze gates for the same reason those gates exist — destroying durable state
during replay lag or under an operator freeze is exactly what they forbid, and a rolling upgrade
writing a body an older build cannot parse is the realistic trigger. The corrupt-*key* delete cannot be
gated (an unsplittable key names no target) and stays first, which is `sweepMark`'s posture for
unattributable garbage.

Arms 5, 7 and 11 also give the **clear** that pairs with each raise — the dossier's standing demand
(*"for every raise, name the clear that retires that exact column, and pair the retirement with the
READ so it is level-driven"*). Today `issueKeyGapEntity`'s clears (`evaluator.go:784`) are reachable
only through a mark or a delivery; after this fire the count leg reaches every one of them.

### 3.1 What this deliberately does NOT do

- **No new key, no new bucket, no new TTL.** If a future reader finds one, this design was not followed.
- **No change to when a gap is suppressed.** `gapSuppressed` is read, never edited. Its verdict, its
  default-budget fallback and its fail-to-dispatchable posture are all untouched.
- **No new issue code.** `GapBudgetExhausted` at `warning` is re-raised on the existing
  `issueKeyGapEntity` latch, so `docs/observability/health-kv-schema.md` needs no schema change. The
  heartbeat listing is already severity-bounded and overflow-named
  (`TestEmit_BoundsTheListingWithoutHidingTheCause`) — the dossier's *"a per-entity Health issue is
  unbounded and the heartbeat is ONE KV value"* entry is satisfied by the existing cap, and this fire
  adds no new per-entity family. It does raise the *population* of an existing family: entities whose
  rows have gone quiet can now hold a standing issue where before they silently held none. That is the
  point of the fire, and it is bounded by the number of parked gaps.

### 3.2 The orphan-column arm, and the reasoning this section originally got wrong

**STRUCK 2026-08-25 — this section originally argued the leg needs no orphan-column arm. Three cold
reviewers independently refuted it, two by execution.** The original argument was that `action` is
consulted only for the cap *fallback*, so a dropped playbook column merely loses the `directOp` engine
default. That reasoning missed two things: a row-declared `maxretries_<g>` escalates **regardless** of
`action`, and `escalateExhaustedGap` on an augur-configured target **re-creates the mark and bumps the
count**, which re-arms the count's own 128 h TTL. The result was a **non-terminating loop** — the count
leg escalates, `fireEpisode` creates a mark, the mark leg's orphan arm deletes it, the count leg
escalates again — dispatching a real `CreateAugurReasoningClaim` every ~30–60 min, forever, for a gap
Weaver no longer manages. Without augur it was instead a permanent false alarm with no reachable clear
(arm 7 needs the column to go false; an orphan column stays true).

**Arm 8b, therefore: the target is registered but `target.Gaps` does not name `gapColumn` → return.**
Never escalate, and — per arm 5's rule — never delete the count

> **AMENDED 2026-08-25 (close pass).** Arm 8b originally *cleared* `issueKeyGapEntity` before returning.
> That contradicted lane 1, which raises `GapBudgetExhausted` at the very same latch for an orphan
> column: `openGapColumns` (`evaluator.go:1229`) enumerates **every** true `missing_*` column, playbook
> or not, so a half-migrated package (playbook drops the gap, lens keeps `maxretries_<g>`) has one leg
> raising and the other clearing. Verified flapping raise→clear→raise, which also re-stamps `since` on
> every re-raise — against Contract #5 §5.5's "open since it first arose" — and makes every raise an
> *arrival*, defeating arrival-vs-repeat log damping. **The arm returns without clearing.** Stopping the
> escalation was the blocking fix; the latch stays owned by lane 1 exactly as on `main`, and that
> condition already carries its own config-keyed `GapWithoutPlaybook` diagnostic. Teaching lane 1 the
> orphan-column rule is a dispatch-semantics change outside this fire's ratified scope.
either: a partially replayed target is present in the registry with an intermediate definition that may
not yet name the gap (`reconciler.go:628` says so in as many words), so a missing key is absence-shaped
evidence. The TTL collects it. `reclaim`'s orphan arm deletes the *mark* there, which is an anti-storm
gate the next dispatch recreates; a count is the bound itself, and the two are not comparable.

### 3.2b The log-volume fix is CALL-SITE scoped — `alert` itself is not this fire's to change

Re-deriving the alert every pass turns one `Error` record per parked gap per *mark lifetime* into one
per *sweep pass*, for the budget's whole life — roughly 7,680 records per parked `(target, entity, gap)`
at the defaults. The latch is correctly idempotent (`issueCache.set` preserves `since`); only the paired
log is not.

**A mid-build attempt to fix this inside `alert` — logging `Error` on arrival and `Debug` on a repeat,
comparing severity and code — was WRONG and is reverted.** It changed a shared FR29 primitive for every
issue family in the component to solve one leg's symptom, and §7's drift fence never named `alert`. The
regression it caused is instructive: `TimerDataError` is **event-shaped, not level-driven** — it raises
once per discrete dropped fired-timer, same severity and code, a *different message* each time. With
`Message` excluded from the comparison every drop after the first logged at `Debug`, which the
production logger (`LevelInfo`) discards, while the single Health slot keeps only the latest — so
distinct faults were recorded nowhere at all. `issueKeyTimer("")` has no clear site, so they were
permanently silent. Adding `Message` to the comparison is not the fix either: a message embedding a
varying value would then spam by construction.

**The damping belongs at `escalateExhaustedGap`'s call site**, which is the only raise this fire made
128× louder. Every other family keeps `main`'s behaviour exactly.

### 3.2a One decision the arm table does not otherwise state

**A duplicate augur escalation is not observable on the ops stream, by design.**
`escalateExhaustedGap`'s live-mark guard (`evaluator.go:1131`) makes the second call in a window find
the first's fresh mark and Ack — load-bearing anti-storm behaviour, not a defect. It means arm 2's
"exactly one escalation per pass" claim must be observed on the alert/log surface rather than by
counting dispatched ops. Any future test of that claim inherits the same constraint.

### 3.3 Comments this leg falsifies, corrected with it

Routing `…__count` to a real leg makes four standing comments untrue. Each is corrected in the same
increment — an unamended comment is a wrong instruction to the next reader:

- `pass()`'s `…__count` skip rationale (*"The sweep never enumerates, reclaims, or deletes either"*).
- `sweeper.pass()`'s own doc (*"pass sweeps every current mark"*) — a four-leg loop.
- the `sweeper` struct doc (*"the bucket holds ONLY marks, bounded by the in-flight count"*) — false
  for `__count`, `__control` and `__effect`, and false before this fire too.
- `countInFlight`'s parenthetical claiming the sweep applies "the same guard" to counts.
- `deleteByTargetPrefix` and `Engine.Revoke` (`control.go:182`) each enumerate the reserved shapes their
  prefix delete removes and each omits `…__count`, which shares the `<targetId>.` prefix and is deleted.

## 4. The re-arm (arm 11), and why the verb needs it

An operator verb that deletes the count is inert on its own: deleting a weaver-state key produces no
row change, so no CDC delivery follows, and the sweep's only dispatch leg is the mark leg — which has
no mark to visit. The gap would be un-suppressed and still un-dispatched: a *quieter* silent park.

So the count leg carries the dispatch arm. When the count stands, the target is registered and active,
the gap is open and violating, `gapSuppressed` returns false, and **no mark exists**, the gap is by
definition in the state lane-1 would dispatch on the next delivery — and no delivery is coming. The leg
dispatches, through the same `dispatchGap` path the mark leg's reclaim uses. The mark CAS-create is the
OCC (`state.go:113`), so a concurrent lane-1 delivery collapses on it exactly as two evaluations already
do; the loser drops.

This arm also closes the *other* remedy the scout found: raising `maxretries_<g>` in the package
re-projects the row and does produce a delivery, so that path already worked — but only because the row
changed. Arm 11 makes the re-arm work when nothing about the row changes.

## 5. The operator verb

`lattice weaver reset-budget <targetId> <entityId> <gapColumn>` → `Engine.ResetRetryBudget`, mirroring
`reset-confidence` (`cmd/lattice/weaver/weaver.go:239`) and `deleteEffectWindows` (`state.go:718`):
revision-conditioned delete, tolerant of a key that vanished mid-scan, reports what it removed.

Scope is one gap, not one target: the count is per-(target, entity, gap), the issue latch is keyed the
same way (`evaluator.go:1255`), and a target-wide reset would re-arm parks an operator never looked at.

The verb deletes the count and nothing else. It does not clear the issue and does not dispatch — the
next sweep pass (≤ 1 min, `reconciler.go:21`) does both, through arm 11. One writer, one path: the
verb states intent, the level reconcile enacts it. (Checklist rule 5: *one deterministic key, one
writer.*)

## 6. Increment order + green bar

| Inc | What | Risk class | Review |
|---|---|---|---|
| 1 | `sweepCount` arms 1–10; `pass()` routes `…__count`; correct `deleteByTargetPrefix`'s comment, which enumerates three reserved shapes but deletes four (counts share the `<targetId>.` prefix) | **posture-changing** — new enforcement point in the sweep | full 3-layer adversarial |
| 2 | arm 11 (the re-arm dispatch) | **posture-changing** — new dispatch leg | full 3-layer adversarial |
| 3 | `Engine.ResetRetryBudget` + `lattice weaver reset-budget` + `docs/components/weaver.md` §9 | mechanical, mirrors `reset-confidence` | lead review |
| — | close | — | **one cumulative adversarial pass over the whole item diff** |

**Green bar, as runnable commands:**

```sh
go build ./...
make vet
golangci-lint run ./...            # see agents/steward/REMOTE.md §7 for the toolchain pin
STRICT=1 go run ./scripts/lint-conventions.go
go run scripts/lint-board.go       # exits 0 on FAIL — READ THE OUTPUT (REMOTE.md §1)
go test ./internal/weaver/... ./cmd/lattice/... -count=1
go test ./... -p 4                 # with POSTGRES_TEST_DSN exported (REMOTE.md §3)
```

**Premises re-verified live at compile time (2026-08-25), all pinned:** `defaultMarkLease = 30m`
(`reconciler.go:17`) · `defaultSweepInterval = 1m` (`reconciler.go:21`) · `markTTLBackstopFactor = 2`
(`state.go:44`) · `dispatchCountTTLBackstopFactor = 256` (`state.go:64`) ·
`defaultDirectOpRetryBudget = 3` (`evaluator.go:982`) · `…__count` skipped by the sweep
(`reconciler.go:177`) · no un-park verb exists (grep `unpark|rearm|acknowledge` in `internal/weaver`
→ 0 hits).

**Proof obligations** (dossier: *"prove each changed line by reverting THAT LINE, not the feature"*):

- A test that seeds a count with **no mark** on a still-violating exhausted gap, runs a pass, and
  asserts `GapBudgetExhausted` is raised — and that reverting the `pass()` routing line alone reds it.
- A restart-shaped test: raise, drop the `issueCache` (fresh engine over the same bucket), sweep,
  assert the issue is back.
- Arm 2's ordering claim is a **move**, not a revert: move the `listed`-set check below the escalation
  and assert a double-escalation test reds. (Dossier: *"where the claim is about WHERE a block sits,
  the mutation is a MOVE."*)
- Arm 3's negative needs its positive vector first: prove the leg *does* act for a registered target
  before asserting it does not for an unregistered one.

## 7. Non-goals (the drift fence)

Not touched: `gapSuppressed`'s verdict or defaults; the mark lease/TTL constants; the Augur escalation
path's own anti-storm mark; `inflight_<g>` semantics; the heartbeat listing cap; any `docs/contracts/*`
file; `internal/weaver`'s planner, contraction monitor, oscillation detector or temporal lane.

## 7a. Landing shape + checkpoint (multi-fire)

**Landing shape: land each increment on `main`** (the second of the two sound shapes). The invariant
that keeps `main` correct across the boundary is stated and testable: **through Increment 1 the count
leg only ever READS, RETIRES, or ESCALATES — it opens no new dispatch path.** Arm 11's re-arm dispatch
is the first line that can dispatch from this leg, and it does not exist yet (`reconciler.go` falls
through instead). So `main` after Increment 1 is a strict improvement — the loud stop survives a mark's
expiry and a restart — with no new way for Weaver to act on the world.

### Checkpoint — 2026-08-25

- **Branch:** `claude/great-lamport-q85azx` (remote container; no worktree — `REMOTE.md` §1).
- **Done:** Increment 1 complete — `sweepCount` arms (a)–(l), `splitCountKey`, `retireCorrupt` wired
  into all three sweep legs, `deleteCorrupt` shape-aware, `gapSuppressed` split (proved
  behaviour-preserving over 3,780 input classes by the close pass), six falsified comments corrected.
  Four cold reviews: three on Increment 1, one cumulative close pass. Every gate pinned by reverting
  that gate alone (M1–M13).
- **Next:** Increment 2 — arm 11, the re-arm dispatch (`reconciler.go`, the fall-through at the end of
  `sweepCount`). Then Increment 3 — `Engine.ResetRetryBudget` + `lattice weaver reset-budget`,
  mirroring `reset-confidence` (`cmd/lattice/weaver/weaver.go:239`).
- **Not yet delivered:** the board row's second half, *"and cannot be un-parked"*. §4's argument stands
  — the operator verb is inert without arm 11, so Increments 2 and 3 land together or not at all.
- **Open for Andrew:** the Contract #10 §10.3 text, on proposal branch
  `claude/contract-10-weaver-state-sweep-enumeration`. The code ships at L2 ahead of it, matching the
  four prior `🔭 flag-for-Andrew` precedents on this board.

## 8. Build note

*(Appended per increment: checkpoint + deviations only — not a fire journal.)*
