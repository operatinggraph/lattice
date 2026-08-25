# Weaver — an exhausted gap's loud stop must outlive the suppression that causes it

> **✅ Winston-ratified — build-ready (2026-08-25).** Every open question in this document is an
> implementation call and is answered here (Steward §0/§2.5). **No frozen-contract change is proposed
> or required:** Contract #10 §10.8 already promises the behaviour twice, in as many words — this is a
> conformance fix, not an amendment. Where this banner and the body disagree, the banner wins.

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
| 2 | mark key `<t>.<e>.<g>` present in **this pass's** `listed` set | return | the mark leg owns this gap; never escalate twice in one pass. Uses the set `pass()` already builds (`reconciler.go:159`) — no extra KV read |
| 3 | target not registered | **return — never delete** | an unreplayed target is replay lag, not absence; deleting the budget here would re-arm every parked gap on every restart. (Dossier: *"an `error`-severity Health issue must not fire on a self-healing condition"* — the same trap, one severity down) |
| 4 | target disabled (`__control`) | return | mirrors `sweepMark` arm (d) and lane-1's Ack-skip: an operator freeze stops dispatch, and escalation is dispatch |
| 5 | row read → `ErrKeyNotFound` | delete count (rev-conditioned) + clear `issueKeyGapEntity` | the entity is gone. This is the orphan GC `state.go:64` says the TTL exists for, done promptly and properly |
| 6 | row value unparseable | return | never act on unreadable evidence (`sweepMark`'s posture, `reconciler.go:270`) |
| 7 | `missing_<g>` not true | delete count + clear issue | the gap closed without a mark to carry the level reconcile |
| 8 | row not `violating` | return | mirrors lane-1's L1 gate and `sweepMark`'s violating gate (`reconciler.go:481`) |
| 9 | `gapSuppressed` → `exhausted` | `escalateExhaustedGap` | **the fix.** Same site the mark leg calls (`reconciler.go:508`), now reachable from state that outlives the mark |
| 10 | `gapSuppressed` → suppressed, not exhausted (`inflight_<g>`) | return | a call is in flight; the lens will re-project when it lands, and lane 1 re-delivers |
| 11 | not suppressed, **no** mark | clear the issue, then **dispatch** | the re-arm. See §4 |

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

## 8. Build note

*(Appended per increment: checkpoint + deviations only — not a fire journal.)*
