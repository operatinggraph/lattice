# Weaver — one escalation episode, three doors: the `unplannable` doors take the model the `exhausted` door has

**Status: ✅ RATIFIED (Winston-adjudicated, per the 2026-08-20 delegation) — 2026-09-05.** No architectural
fork; no frozen-contract change (§6 quotes the sentences this builds to). One fire, size **S–M**, two increments
(§11), `📋 ready` for the Lattice Steward. The adversarial pass this design flags for itself was run in the
same fire (§14: 2 BLOCKING + 7 MAJOR + 6 MINOR, every one folded into the body below; the shape of the release
and the pacing document's lifetime changed under it). · Designer fire 2026-09-05 · Lattice lane

Board row: *[Weaver] The `unplannable` / no-playbook escalation doors have no pacing or booking model*
(★★ · S–M · filed 2026-09-05 by the exhausted-gap fire's close pass,
[weaver-exhausted-gap-leg-boundary-design.md](weaver-exhausted-gap-leg-boundary-design.md) §12/§14).

---

## For Andrew

**What it does, in two lines.** Weaver has three doors to the Augur reasoning tier — a spent retry budget,
a gap column with no playbook entry, and a goal gap from whose current state no plan derives — and only the
first has an *episode model* (booked against nothing, re-fired on a paced backoff, recorded on Health, its
lost-publish obligation withdrawn). This design extracts that model into one seam every door calls, gives the
two `unplannable` doors the one thing they structurally lack — a **release** when the gap can act again — and
makes an escalation mark declare its class so no path has to infer it from an action string.

**No architectural fork.** Every decision is mechanism inside `internal/weaver`, on seams the exhausted-gap
design (Andrew-ratified 2026-09-04) already defined. The one product-shaped stance — *a derivable plan
pre-empts a standing escalation; the reasoning tier is the fallback, never the path* — is inherited verbatim
from that design (its clause [d]: "the gap re-plans regardless of the proposal's state; the stale proposal stays
reviewable and rejectable") and from the Augur design's own risk row ("L3 is the **fallback**, not the path").
Where an escalation stands over a plan leg whose human task may still be open, this design does **not** widen
that stance: such an escalation releases only at that leg's boundary, exactly as today.

**No contract change.** Contract #10 (Augur) promises that the block "redirects that dead-end to the
AI-reasoning tier"; §10.3 promises a leased mark whose expiry makes the gap reclaimable; the §10.8 planner
extension promises that "no plan derivable" flows into `unplannable`. This design is the runtime keeping those
three sentences for two doors that today keep only the first. Nothing in `docs/contracts/` is staged.

**The premise, corrected by census (§3).** *No shipped target escalates `unplannable`* — all three `augur`
blocks in `packages/` escalate `exhausted` only — so both doors this row names are **latent**, and the row's
mechanism description is wrong in two places (§1.1). The design is still worth a fire: the defect class is the
one the parent fire just fixed one door over (a rejected reasoning op re-fired unpaced for as long as the gap
stands), the first `unplannable` opt-in inherits a *permanent park* (§1.2 door 3), and the unification removes
the flag-threading the parent fire added at six sites. **Row 1(b) of §8 — strike `unplannable` from the contract
and delete both doors — is the honest alternative and is yours to take at any time: zero consumers means zero
migration.** It is not recommended, because the planner mandate's designed exit for "no plan derivable" is this
door.

---

## 1. Problem + intent

### 1.1 What the filing said, verbatim, split at every clause

From the board row (filed by commit `bb9beedc`, 2026-09-05):

> Both doors fire a reasoning claim **[a]** the reclaim re-arms every mark lease; **[b]** a plan-time-escalated
> goal gap re-plans only when its mark leaves; **[c]** the no-entry door books the count + an `__effect` window,
> **[d]** which is also its only bound.

| Clause | Verdict (§1.2) | Section |
|---|---|---|
| [a] the reclaim re-arms every mark lease | **True for door 3, false for door 2.** Door 3's mark is re-armed every lease, unpaced, and re-pins the escalation. Door 2's mark is **deleted** by the reclaim's orphan-column arm at every lease expiry; lane 1 then fires a fresh (rejected) claim on the next row delivery. | §1.2, §4.3 |
| [b] re-plans only when its mark leaves | **Under-claims.** Door 3 never re-plans through the reclaim (it re-pins the escalation from `rec.Action`), and a row change is dropped by the live mark — so a never-planned goal gap re-plans only at gap close→reopen or a displaced leg's boundary. A permanent park. | §1.2, §4.3 |
| [c] books the count + an `__effect` window | **True.** Door 2 rides the ordinary path with `escalated=false`. | §1.2, §4.1 |
| [d] which is also its only bound | **False.** The count never bounds it: the lane-1 loop reads the zero `GapAction` for a column with no entry, and the default `directOp` cap engages only for a literal `directOp` action — so the gap is never `exhausted`. The `__effect` window is the only surface that notices: `LensEffectMismatch` once 20 slots are pending, of which the gap's own close credits at most one. | §1.2 |

### 1.2 The three doors, by mechanism (every cell cited)

The Augur tier is entered by `augurEscalation` (`strategist.go:644`), which builds one `directOp` of the
target's reasoning op (default `CreateAugurReasoningClaim`) with a **deterministic** `instanceKey`
(`deriveAugurHandle`, `actuator.go:189` — `(targetId, entityId, gapColumn)`). The op is **create-only on
the claim root**: a second submission for the same gap is rejected (`packages/augur/ddls.go:512` — "A
redelivered instanceOp conflicts create-only on the root vertex and is rejected"). So a re-fire of an
escalation is never a second reasoning call; it is either the one recovery a claim that was *never minted*
has (the parent design's §4.3), or pure churn.

| | **Door 1 — `exhausted`** | **Door 2 — no playbook entry** | **Door 3 — no plan derivable (goal gap)** |
|---|---|---|---|
| Entry | `escalateExhaustedGap` (`evaluator.go:1943`), from all three suppression sites (lane 1 `:236`, sweep reclaim `reconciler.go:1047`, count leg `:694`) | `dispatchGap`'s `!ok` arm (`evaluator.go:311-356`): `ga = esc`, then **the ordinary path** | `planGap`'s substitution (`evaluator.go:731-736`) on `resolveGoalAction`'s two `unplannable` errors (`strategist.go:571-578` pin not in catalog; `:592-597` `ErrNoPlan`) — `escalated=true` threaded to `fireEpisode` |
| Books the retry budget? | no (`fireEpisode` `!escalation`, `evaluator.go:975-1000`) | **yes** — `resolvePlannedAction` returns a named action unchanged (`strategist.go:431`), so `escalated` is false and `bumpDispatchCount(attempt=true)` runs | no |
| Books an `__effect` window? | no | **yes** — `<target>.__effect.<col>.directOp` gains a pending slot per fire; the gap's close credits at most one (`clearClosedMarks`, `evaluator.go:1296`) | no |
| Mark records | `action: directOp`, `escalatedFrom: <leg>` (goal gaps only) | `action: directOp`, nothing else — indistinguishable from a static `directOp` leg | `action: directOp`, `escalatedFrom` (the displaced leg, from `count.Leg` when no mark was held) |
| Live mark, lane 1 | Ack (`:2068`) | Ack — the anti-storm drop (`:495`) | Ack — the anti-storm drop |
| Lease expiry, sweep | routed by `exhausted` (`reconciler.go:1047`) → re-fire **paced** on `escalatedAt × reclaims` (`evaluator.go:2094-2103`) | `target.Gaps[col]` is `!ok` → **orphan-column delete** (`reconciler.go:915-927`, after warm-up), Warn `orphanColumn`, `orphansDeleted++` | pin `rec.Action="directOp"` fails to resolve → `dispatchAction = rec.Action` (`reconciler.go:1084-1096`) → not collapse-only → **unpaced** re-arm every lease; `planGap(pinned "directOp")` (`:1176`) re-escalates; Warn `mark reclaimed` per lease; a rejected op per lease |
| Quiet row (count leg) | routed by `exhausted` (`:694`) → same paced re-fire | no count document is consulted for it (arm (j), `:688-693`) | no count document exists (books nothing) → nothing re-fires a dead claim |
| Release (the gap can act again) | the leg boundary (`releaseCompletedLeg`, `:1998`) + the `resetBudget` verb | **none** — an entry added by a re-author is honoured only once the live mark leaves | **none** — a row from which a plan now derives is dropped by the live mark; the reclaim re-pins the escalation; only a displaced leg's boundary or gap close→reopen re-plans |
| Health | `GapEscalatedToAugur` at `issueKeyGapEntity` (`:2175`) | nothing (the target-scoped `GapWithoutPlaybook` is cleared, `:353`) | nothing |
| Lost-publish obligation | withdrawn (`:2131`) | kept | kept |
| Cap / bound | the budget it spent | **none**: the loop's `ga` is the zero `GapAction` (`:196`), and the default cap "is consulted for a `directOp` action only" (`:1759`); `__effect` trips `LensEffectMismatch` at `effectWindowSize=20` (`state.go:729`) | none needed — books nothing |

**Live evidence for door 1's model (2026-09-05 census, §3 C4):** `weaver-state` holds **0 marks** and one
count document — `renewalComplete.QomdjY7hAGS6mHvN9d2j.missing_renewalComplete.__count` =
`{"count":320,"reclaims":4,"escalatedAt":"2026-09-05T12:04:50Z"}`. The escalation's mark has TTL'd
(`markTTLBackstopFactor=2` × 30 min) and the re-fire is pacing on the *document* — the parent design's
reason for keying pacing there ("the mark it re-fires from is the very thing a dead episode may have lost")
holding on the running stack. Doors 2 and 3 have no document to pace on, which is the design's one genuinely
new decision (§4.4).

### 1.3 Why this is the class the parent fire fixed, one door over

The parent's clause [c] was "265 dispatches against a budget of 6 … each re-fire is a rejected op booked into
the gap's count and an `__effect` window" — door 1 before 2026-09-05. Door 2 today is that sentence verbatim
(booked into the count and an `__effect` window, rejected op per fire) and door 3 is its unpaced half (a
rejected op per lease). A reviewer's finding is a class; the parent's close pass classified this as a
design gap and filed it rather than folding it, and this is the fold.

---

## 2. Grounding ledger

| Claim | Code |
|---|---|
| The escalation op is create-only per gap; a re-fire is rejected while the claim vertex lives | `packages/augur/ddls.go:512`; `deriveAugurHandle` `actuator.go:189` |
| Door 1's episode half: live-mark Ack, paced re-fire, stale clear, plan+fire, republish withdrawal, `bookEscalation`, latch | `evaluator.go:2068`, `:2094-2103`, `:2085-2092`, `:2105-2109`, `:2131`, `:2142`, `:2175` |
| `bookEscalation` refuses to create an absent document; `incrementDispatchCount` refuses a re-arm create | `state.go:538-551`; `:420-435` ("a count key that exists and reads 0 is the ONE state the sweep's count leg treats as an operator's un-park") |
| Lane 1 holds the count at revision 0 for a goal gap and a no-entry column | `evaluator.go:1859-1881` (`gapSuppressionTerms`: `needsCount` only for a positive `maxretries_<g>` or a literal `directOp`), `:217-222`, `:535-536` (`count, _, _ =`) |
| `releaseCompletedLeg`: marked branch = mark delete as mutex, blind count delete, latch clear; markless branch refuses at `countRev == 0` | `evaluator.go:1508-1519`, `:1571-1581`; `:1540-1547` |
| The count leg's un-park arm requires `Count == 0` **and** a resolvable, non-collapse-only leg; arm (j) sits above the `violating` gate | `reconciler.go:752`, `:779-790`; `:688-693` vs `:695` |
| Arm (j)'s stated reason an escalation would not terminate there | `reconciler.go:536-545` |
| The `resetBudget` verb refuses a no-entry gap first and a non-resolving gap later | `control.go:598-601`, `:625-627` (`reArmDeclines`) |
| The reclaim classifies by the resolved pin and falls back to `rec.Action` on a resolution error | `reconciler.go:1084-1096`; gate `scripts/lint-weaver-classify-by-shape.go` |
| The reclaim deletes a mark whose column the playbook does not name, **above** the `entityKey` guard and the count read | `reconciler.go:915-927`; `:952-961`; `:970-978` |
| The reclaim passes the mark's action as the plan pin, rewrites the mark with `replace` directly, and consumes `escalated` three times | `reconciler.go:1170-1176`; `:1226`; `:1218-1222`, `:1231`, `:1244` |
| `legOf` reads `escalatedFrom`, then a catalog ref, then the count's leg; `displacedLeg` is goal-only; `escalationLeg` with no mark reads `count.Leg` | `evaluator.go:1601-1640` |
| `bookDispatch` restarts `Count`, `Reclaims`, `EscalatedAt` only on a goal leg change with a non-empty stored leg | `state.go:495-521` |
| Lane 1 reads the zero `GapAction` for a no-entry column; the default cap needs a literal `directOp` | `evaluator.go:193-196`, `:1759` |
| The backoff series and its exponent | `reconciler.go:167-197` (`backoffInterval(reclaims)`: base = `MarkLease` 30 min, cap 24 h; `reclaims ≤ 1` → base) |
| The mark struct and its writers | `state.go:102-123`; `create :158`; `replace :238` — called from `fireEpisode` (`evaluator.go:948`) and the reclaim (`reconciler.go:1226`) |
| The `escalated` flag threaded from `planGap` at every seam | `evaluator.go:517/538`, `:2001/2008`, `reconciler.go:796/818`, `:996/1005`, `:1176/1218-1244` |
| `unplannable` is set at exactly two sites, both in `resolveGoalAction` (goal gaps only; never a candidates gap or a non-planned target) | `strategist.go:571-578`, `:592-597` |
| Loupe decodes marks and count documents without `DisallowUnknownFields` | `cmd/loupe/weaver.go:208-228` (verified by the parent fire's touch list) |
| Contract sentences built to | `docs/contracts/10-orchestration-augur.md:10-13`; `10-orchestration-weaver.md:345-347`; `10-orchestration-substrate.md` §10.3 `:133-157` |

Import cycles: none — every edge is inside `internal/weaver`. Build-tagged doubles: no interface changes
(`retryBudgetStore` unchanged; `mark` gains a field; `fireEpisode`'s `escalation bool` becomes a string, an
unexported method with no double — `grep -rl "^//go:build" internal/weaver` → none reach it).

---

## 3. Censuses (run in this fire; the build's Phase 0 re-runs them)

**C1 — targets that escalate, by trigger.**

```sh
grep -rn 'Escalate' packages --include='*.go' | grep -v _test
```
```
packages/wellness-ledger/targets.go:109:   Augur: &pkgmgr.AugurSpec{Escalate: []string{"exhausted"}},
packages/lease-signing/targets.go:119:     Augur: &pkgmgr.AugurSpec{Escalate: []string{"exhausted"}},
packages/lease-signing/renewal_targets.go:144: Augur: &pkgmgr.AugurSpec{Escalate: []string{"exhausted"}},
```
**3 targets, 3 × `exhausted`, 0 × `unplannable`.** Doors 2 and 3 have no shipped consumer. (The install gate
`registry.go:1163` admits both tokens; the corpus uses one.)

**C2 — the doors, by call site.** `grep -n "augurEscalation(" internal/weaver/*.go | grep -v _test` → three:
`evaluator.go:319` (door 2), `:733` (door 3), `:2013` (door 1). Exactly the three rows of §1.2.

**C3 — the `escalated` threading the unification removes.** `grep -n "fireEpisode(" internal/weaver/*.go |
grep -v _test | grep -v "func "` → 5 sites; `planGap(` → 6 sites; the reclaim consumes the flag at three more
lines without calling `fireEpisode` (§2). After §4, `fireEpisode` is still called from the same 5 (with the
trigger string, empty for an ordinary episode), 4 of the 6 `planGap` callers gain a two-line route, and the
reclaim's three consumers become unconditional (§4.1).

**C4 — live `weaver-state` (2026-09-05, `nats … kv ls weaver-state`).** `marks=0 counts=1 effects=40
control=1`; the one count document is §1.2's. No escalation-shaped mark of any door is live; no
`__effect.<col>.directOp` window belongs to a no-entry column (the two `directOp` windows are
`leaseExpiry.missing_renewalCycle` and `cafeStaleTabSettlement.missing_staleat`, both static playbook entries).
**Migration population: zero** (§9).

**C5 — readers of a count document that branch on `Count == 0` or on its presence** (the §4.4 decision's
governed set), by declaration — `grep -n "Count\b\|getDispatchCount\|dispatchCountEntry\|countRev"
internal/weaver/*.go | grep -v _test`: `reconciler.go:752` (arm (n)'s zero test), `:779-790` (arm (n)'s
resolve + collapse-only refusals), `control.go:355-395` (`ResetRetryBudget`, behind `reArmDeclines`
`:598-627`), `evaluator.go:1801-1815` (`gapSuppressed` — `0 < cap`, never suppressed), `:1540-1547`
(`releaseCompletedLeg`'s markless branch: presence at `countRev`), `state.go:420-435`
(`incrementDispatchCount`'s own refusal to create at 0), `state.go:495-521` (`bookDispatch`'s restart rule),
and Loupe's weaver page (a render, not a decision). Every decision-taking reader is walked in §4.4.

**C6 — raises and clears at `issueKeyGapEntity`.** `grep -n "issueKeyGapEntity" internal/weaver/*.go |
grep -v _test` → `evaluator.go:755` (clear, plan built), `:1388` (clear, `retireClosedGapIssues`), `:1580`
(clear, `releaseCompletedLeg`), `:2023` (raise `GapBudgetExhausted`), `:2035` (guarded clear), `:2175`
(raise `GapEscalatedToAugur`), `reconciler.go:666` (clear, corrupt count body). Seven sites; §4.1 item 7
names all seven and adds one clear, at the class release, for the reason `:1580` gives.

---

## 4. The shape

### 4.1 One seam: `escalateGap(esc, trigger)`

The lower half of `escalateExhaustedGap` — everything from the live-mark disposition down — becomes
`(*Engine).escalateGap(ctx, target, targetID, entityID, entityKey, col string, esc GapAction, trigger string,
row, rowRevision, rec, markRev, found, count, countRev) substrate.Decision`, **unchanged in behaviour**, and
`escalateExhaustedGap` becomes its first caller (surface guard → mark read → leg-boundary release → policy
check / `GapBudgetExhausted` → `escalateGap(esc, escalateExhausted, …)`). The caller owns the policy check and
passes the `esc` it built, so `augurEscalation` runs once per escalation and the policy is read in one place per
door. The seam owns, for every trigger:

1. **Live mark → Ack** (the episode is in flight; `:2068`'s reasoning unchanged).
2. **Paced re-fire** — level-tested against `count.EscalatedAt` with `backoffInterval(count.Reclaims)`
   (`:2094-2103`), the stale mark left standing. `reclaims` is 1 after the first fire, so the second fire waits
   the base (30 min), the third 1 h, … to the 24 h cap — door 1's series exactly.
3. **Stale-mark clear**, revision-conditioned (`:2085-2092`).
4. `planGap(esc)` + `fireEpisode(…, escalation=trigger, legScoped, displacedLeg)` — booked against neither the
   budget nor an `__effect` window (`fireEpisode` unchanged on this).
5. **Republish withdrawal** on a publish failure (`:2131`; the reasons there apply to every trigger — an
   escalation's retry is this seam's re-fire, and the obligation is unsafe once the gap can dispatch again).
6. `bookEscalation` on a real CAS-create (`:2142`) — **amended, §4.4**.
7. The `GapEscalatedToAugur` latch at `issueKeyGapEntity` (`:2175`), its message naming the trigger
   (*"… has no playbook entry / no derivable plan / exhausted its retry budget, and was escalated to Augur
   reasoning"*). The key's seven sites (§3 C6) stay; the class release (§4.3) adds one clear, for the reason
   `releaseCompletedLeg` gives at `:1483-1488`: the plan that follows a release can fail to build, and the
   record would otherwise stand over a gap that has left the reasoning tier. The latch cannot flap: a repeated
   `set` never re-stamps `since` (`issueCache.setSince`), and lane 1 and the count leg set the same value.

**The two `unplannable` doors route to it instead of continuing down the ordinary path:**

- **Door 2 (`dispatchGap`).** The `!ok` arm keeps its un-escalated branch verbatim (`GapWithoutPlaybook`,
  long floor). Its escalated branch no longer does `ga = esc`; it keeps `esc`, clears the target-scoped
  `GapWithoutPlaybook` as today, and falls through **past the `msg.Sequence == 0` defer and the mark read** (the
  existing pin `TestDispatchGap_AugurPolicyRetiresGapWithoutPlaybook` expects the metadata defer first), then
  `return e.escalateGap(esc, escalateUnplannable, …, rec, markRev, found, count, countRev)` — with `(count,
  countRev)` read **with its revision** at the `:535-536` site (today `count, _, _ =`), because §4.4's document
  exists for this shape and the seam's pacing reads it.
- **Door 3 (`planGap`).** `planGap` no longer substitutes a plan. On `perr.unplannable`, when the policy
  escalates, it returns `(nil, "", esc, escalate=true, Ack)` and the caller routes: `if escalate { return
  e.escalateGap(esc, escalateUnplannable, …) }`. When the policy does not escalate, today's disposition
  (`TemplateDataError`, long floor) is unchanged. The four callers that can reach it — `dispatchGap` (`:517`),
  the exhausted door's release arm (`:2001`), the reclaim's leg-advance (`reconciler.go:996`) and the reclaim
  proper (`:1176`) — each gain the route. The `escalated bool` return goes away, and with it: the threading
  into `fireEpisode` at `:538`, `:2008`, `:818`, `:1005`; the reclaim's three consumers (`:1218-1222`
  displaced-leg rewrite, `:1231` count guard, `:1244` `__effect` guard) become unconditional — sound because an
  escalation mark is routed **above** that block (§4.3) and a fresh `unplannable` needs `pinnedAction == ""`
  while the reclaim always pins (`:1176`), so no escalation can reach `:1226`; the comments at `:796-822` and
  `:1227-1246` are rewritten to say that (their "the threading stays because …" halves are what the edit
  deletes, and a comment that describes deleted threading is the history-narrating shape CLAUDE.md forbids).
  The count leg's re-arm (`:796`) plans a ref `resolvedLegAction` just resolved, so it cannot be unplannable —
  restated in its comment without the threading clause.

### 4.2 The mark declares its class

`mark` gains `Escalation string \`json:"escalation,omitempty"\`` — the trigger (`unplannable` |
`exhausted`), written at **all three** mark-write seams: `create` (`state.go:158`) and `fireEpisode`'s stale
re-arm `replace` (`evaluator.go:948`) from `fireEpisode`'s non-empty `escalation` argument, and the reclaim's
own `replace` (`reconciler.go:1226`), which threads `rec.Escalation` forward exactly as it threads
`rec.EscalatedFrom` at `:1218-1222` — the seam the parent fire's build note records as having dropped
`escalatedFrom` once ("`replace` dropping the field": implementation-bug ×5, first entry). *The document
declares its own class; the key only addresses* (Andrew, 2026-08-22).

The field is what makes §4.3 sound. Today an escalation mark is `action: directOp` and nothing else (§1.2), so
"this mark is an escalation" could only be inferred from "its action fails to resolve" — which is also what a
**removed catalog ref** under an open leg looks like, and a release keyed on that inference would dispatch a
fresh leg beside a still-open human task. With the field, that removed-ref case stops being an escalation:

- `resolveGoalAction`'s pinned-not-in-catalog arm (`strategist.go:571-578`) becomes `errConfig` — "pinned plan
  leg %q is not in the goal's actions catalog" — the sibling of the candidates arm's identical verdict
  (`:441-446`). Its comment's "two indistinguishable causes" is what the field distinguishes: every escalation
  mark is routed by §4.3 **before any pin is resolved**, so the only thing that reaches this arm with a vanished
  pin is a re-authored playbook (a config error, handled as one: `PlaybookConfigError` at `warning`, long floor;
  the reclaim leaves the expired mark, `reconciler.go:1177-1181`) — or an **old-shape escalation mark**, below.
  `TestResolveGoalAction_PinVanished_FlagsUnplannable` (`goal_dispatch_internal_test.go:85`) is amended to pin
  the config verdict.

**Routing predicate: `rec.Escalation != ""` — any trigger.** The class decides the *release test* (§4.3), never
whether the mark is routed at all. An `exhausted`-class mark can reach the general reclaim and lane-1 paths
(the budget re-armed under a standing escalation — `resetBudget`, or a raised `maxretries_<g>` — the scenario
`evaluator.go:2118-2129` names), and routing only the `unplannable` class would leave it to die on the
`errConfig` verdict above with no release and no re-fire.

**Old-shape marks** (no field): none are live (§3 C4). A door-1 mark minted between this design and the
binary cycle is routed by `exhausted` at every suppression site while its budget is spent; if it is un-parked
inside the mark's remaining life (≤ 1 h, `markTTLBackstopFactor × lease`) it takes the `errConfig` arm above —
a `warning`-severity `PlaybookConfigError` for at most that hour, after which the mark TTLs and arm (n) or a
delivery dispatches, which is today's un-park disposition. Every test fixture that plants a `directOp`
escalation mark (`escalationMark`, `exhausted_gap_leg_internal_test.go:99-110`, and the inline creates at
`evaluator_internal_test.go:~980`) gains the field; one vector keeps an old-shape mark to pin the paragraph
above.

`legOf` / `displacedLeg` / `escalationLeg` are unchanged: `escalatedFrom` remains the displaced leg and
`escalation` the class. **An `unplannable` escalation may carry a displaced leg**: with no mark held,
`escalationLeg` reads `count.Leg` (`evaluator.go:1601-1640`), which is non-empty whenever the chain booked a
leg before a lost mark forced a fresh synthesis that found no path. §4.3 is written for that.

### 4.3 The release — the level test the `unplannable` doors lack

Door 1's release is the leg boundary: a fact about the row, tested *above* the augur-policy check at every
suppression site ("a retire above every cannot-act guard"). This design adds a second release, and orders the
two so the first always wins:

**Rule 1 — an escalation that stands over a plan leg releases only at that leg's boundary.** `legOf(ga, rec,
count)` non-empty (`escalatedFrom`, or the count document's `leg`) ⇒ the only release is
`releaseCompletedLeg` (effects hold), which every suppression site already tests first (lane 1 `:474`, the
exhausted door `:1998`, the reclaim `reconciler.go:989`, the count leg through `escalateExhaustedGap`). "The gap
is plannable now" is **not** a release for it: the displaced leg's artifact (an `assignTask`, a parked Loom
instance) may be open, and a fresh episode beside it mints a duplicate — the hazard arm (n)'s second condition
refuses (`:786-790`) and §10.8's "replanning happens only at leg boundaries" forbids. Unchanged from today.

**Rule 2 — an escalation over no leg releases when the gap can act again**, by class:
- `unplannable`: the gap **resolves now** — door 2: `target.Gaps[col]` exists (any action, `surface`
  included: the ordinary path's `surface` arm holds no mark, so the release is right); door 3:
  `resolvedLegAction` (`strategist.go:492-505` — a pure regression, no admission token, no issue clear)
  returns no `unplannable` error.
- `exhausted`: the gap is **not exhausted now** — which is the only way its mark reaches the general path at
  all (a suppression site routes an exhausted gap to door 1 first), so at the general path the class release
  holds by construction: an un-park.

Both are level tests over monotone state the engine already holds (the registry, the current row, the count),
race-free by construction. **The release is one write: delete the mark, revision-conditioned on `markRev` —
the mutex, the marked branch of `releaseCompletedLeg` (`:1508-1519`).** It deletes **no count document**
(§4.4: the document stays, and is what makes a quiet row reachable). It clears `issueKeyGapEntity` (§4.1 item
7). A conflict means a concurrent pass owns the key: Ack, the next pass re-tests. Then, per site:

- **Lane 1 (`dispatchGap`).** After the mark read and the Rule-1 leg release: `if found && rec.Escalation
  != ""` → Rule 2's test for its class. Released → `found=false, rec=nil`, continue down the ordinary path as a
  genuinely fresh episode (its CAS-create books `Count` 0→1 over the kept document — §4.4's restart rule). Not
  released (still unplannable) → `escalateGap(esc, unplannable, rec, …)` (live → Ack; expired → paced re-fire).
- **Sweep reclaim.** After the Rule-1 leg release and the `violating` / `suppressed` gates, **before** the
  collapse-only classification (so `collapseOnlyReclaim` never sees an escalation mark and the lint gate is
  untouched): `if rec.Escalation != ""` → Rule 2. Released → **delete the mark and dispatch nothing from the
  reclaim.** A released gap is a markless open gap, and the two seams that already own one carry the
  collapse-only refusal the reclaim would have to re-implement: a delivery (lane 1) and arm (n) for a quiet row
  (`:752-790`, `Count == 0` holds for a document over no leg). This is exactly today's disposition for an
  un-parked gap once its escalation mark TTLs, made prompt. Not released → `escalateGap(esc, unplannable,
  rec, …)` — paced, no Warn `mark reclaimed`, no booking. The **orphan-column arm** (`:915-927`) gains one guard:
  a mark carrying `escalation: unplannable`, for a target whose policy still escalates `unplannable`, is a
  standing door-2 episode — the arm does not delete it and the mark falls through to the `surfaceOnlyGap` check
  (false on the zero `GapAction`), the `entityKey` guard (`:952-961`) and the single count read (`:970-978`),
  then to this branch; with the policy removed it is stranded and is deleted there as today.
- **Count leg (quiet row).** A markless document stamped `escalatedAt`, not `exhausted` (else arm (l) routes it
  to door 1), is an escalation whose mark has gone — the normal state between paced re-fires (§4.4). The leg
  routes it **below the `violating` (`:695`) and suppression (`:699`) gates and above the `Count != 0` test
  (`:752`)**: Rule 1 first (`releaseCompletedLeg`'s markless branch on `count.Leg` at `countRev`, then the
  leg-advance dispatch — the shape `escalateExhaustedGap` runs from this leg at `:1998-2010`, shared as one
  helper); then, for a document over no leg, Rule 2: resolves → fall into arm (n) unchanged (its `Count == 0`
  holds — a leg-less document never booked an attempt — and its collapse-only refusal applies); still
  unplannable and the policy escalates → `escalateGap(esc, unplannable, no mark, count, countRev)`; policy
  does not escalate → return (the standing `TemplateDataError` is lane 1's). **Door 2 at arm (j)** (`:688-693`,
  which sits *above* the `violating` gate): the route restates both gate conjuncts inline — the row's
  `violating` true, the column open in the row, `gapSuppressedWithCount` not suppressed — and requires the
  policy to escalate `unplannable`. Arm (j)'s recorded reason for escalating nothing — *"on an augur-escalating
  target an escalation would not even terminate: it re-creates the mark and re-arms the count, the mark leg's
  own orphan arm deletes that mark, and the next pass escalates again"* (`:536-545`) — is retired by the
  orphan-arm guard above, and the comment says so. The two routes differ in warm-up exposure: door 1's `:694`
  sits above `warmedUp`; the new routes sit where arm (n) does, below it, which is the safe side.

### 4.4 Where the `unplannable` doors' pacing lives

Door 1 paces on the count document because a spent budget guarantees one exists. An `unplannable` escalation
over no leg is reached with **no document by construction** (it books nothing, and a gap with no plan has
mounted no attempt), and `bookEscalation` refuses to create one (`state.go:548`) for a reason that names door
1's world: "a count key that exists and reads 0 is the ONE state the sweep's count leg treats as an operator's
un-park".

**Decision: `bookEscalation` creates the document for the `unplannable` trigger** — `{count:0, reclaims:1,
escalatedAt}` — and keeps refusing for `exhausted` (where absence is impossible and a created zero would
indeed read as an un-park). An `unplannable` escalation over a displaced leg finds a document with `Count > 0`
and **updates** it (today's path) — that document is then routed by §4.3's count-leg branch above the
`Count != 0` test, so a dead claim over a leg is retried on a quiet row too. **The release never deletes the
document**, because the document is the only handle the count leg has on a quiet row (§4.3), and because
deleting it conditioned on a revision lane 1 does not hold (`countRev == 0` at `:535-536`) would make the
release Ack forever for exactly the gaps it exists for — the reviewer's first BLOCKING.

**The fresh chain does not inherit the escalation's pacing.** `bookDispatch` (`state.go:495-521`) gains one
rule: an attempt booked over an **escalation-only document** — `Count == 0 && Leg == "" && EscalatedAt != ""` —
restarts `Reclaims` and `EscalatedAt` (the same restart a goal leg change performs), because the re-arm
history and the last escalation belong to the escalation, per that function's own comment. An **un-park
document** carries the leg it was on (`resetDispatchCount` keeps `Leg`), so it does not match and keeps
inheriting its pacing — the parent's ratified §4.3 stance, unchanged. The one shape the discriminator
reclassifies is an un-parked *static `directOp`* gap (its stored leg is `""` by `bookDispatch`'s
`directOp → ""` mapping): its fresh attempt restarts the pair too. Priced: a `directOp` reclaim is never paced
by `Reclaims` (it is the intended bounded retry, `reconciler.go:1124-1130`), and a reset `EscalatedAt` means
its next exhaustion escalates at once rather than against a stamp from before the un-park — the right instant.

The hazard `bookEscalation`'s comment names is walked against every decision-taking reader of a zero or of
presence (§3 C5):

| Reader | On an `unplannable` escalation's document | Verdict |
|---|---|---|
| count leg arm (n), `reconciler.go:752` → `:779-790` | §4.3 routes an `escalatedAt`-stamped document above the zero test; a leg-less one whose gap still resolves nothing declines at `:780`; one whose gap now resolves takes arm (n) with its collapse-only refusal | never fires a markless episode for a gap that has no plan; fires the derivable plan for a quiet row whose mark is gone — the release such a row needs (the parent's "rowless un-park" precedent: the arm skips a rowless gap and the next delivery dispatches), booked `Count` 0→1 with pacing restarted; the Warn's `reason: budgetReArm` is a label, not a decision |
| the same arm on a document with `Count > 0` (an escalation over a leg, markless) | routed above the zero test to Rule 1; `Count != 0` never reached for it | no hole for a dead claim over a leg |
| `ResetRetryBudget`, `control.go:355` | `reArmDeclines` refuses first: door 2 at `:598-601` ("no gaps entry"), door 3 at `:625-627` ("its plan resolves no action for this row") | the verb refuses exactly what the arm declines — the standing rule |
| `gapSuppressed` / `WithCount` | `0 < cap` → not suppressed, never `exhausted` | unchanged |
| `releaseCompletedLeg`'s markless branch, `evaluator.go:1540-1547` | presence at `countRev` is now true for a goal gap that has only ever escalated; `pinnedAction` (= `count.Leg`) is empty for it, so the function returns false at its first line | unchanged |
| `incrementDispatchCount`, `state.go:420-435` | a fresh leg after a release finds the kept document and updates it (0→1); the `!attempt` create refusal is untouched | unchanged |
| `bookDispatch` | the restart rule above | the escalation's tally never paces the fresh chain |

The document's other two fields are then exactly door 1's: `escalatedAt` the level the re-fire is tested
against, `reclaims` the exponent. The mark's own TTL (1 h) is shorter than every wait past the second, so a
standing escalation is normally **markless** between re-fires — which is why the pacing cannot live on the
mark (§8 row 4), why the count leg is a re-fire site (§4.3), and why the reclaim's paced-mark TTL widening
(`reconciler.go:1157-1163`, inside the collapse-only block an escalation mark never enters) does not apply.

### 4.5 What stays exactly as it is

`fireEpisode`'s booking rule (an escalation books neither tally), `backoffInterval`, `collapseOnlyReclaim`
and its lint gate, `staleMark` / `externalDispatchGap` (an escalation mark is routed before either is
consulted; neither ever classified one as external — door 2 has no entry, door 3's goal gap has an empty
`Action`), the `surface` guards, the admission gate (an escalation's plan still draws a token — door 1's
behaviour), the oscillation record (still bumped: the reasoning op writes real aspect paths), every Health key,
and Rule 1 (the leg boundary) at all four sites.

---

## 5. State-lifetime table

| Fact | Created | Reset / ended | Carried | Ordered / conditioned | Never-written row |
|---|---|---|---|---|---|
| `mark.escalation` | at the escalation's CAS-create or re-arm (`create`, and both `replace` seams, with a non-empty trigger) | with the mark: the class release (§4.3), the leg release, gap close (`clearClosedMarks`), orphan (policy removed) / target-removed arms, TTL | across every re-arm (both `replace` callers thread it like `escalatedFrom`) | the mark's revision; every delete that reads it is revision-conditioned | an old-shape mark: none live (§3 C4); a door-1 mark minted before the cycle is routed by `exhausted`, and un-parked inside its hour takes the `errConfig` arm (§4.2) — pinned by one vector |
| the `unplannable` pacing document `{count:0, reclaims, escalatedAt}` | first real CAS-create of an `unplannable` escalation over no leg (`bookEscalation`, `created` only); an escalation over a leg updates the leg's document | gap close (both close paths delete the count), TTL (`256 × lease` = 128 h, re-armed by each re-fire's write); **never by a release** | `reclaims` / `escalatedAt` across re-fires; **not** into the fresh chain (`bookDispatch`'s restart over an escalation-only document) | CAS loop as today | an escalation whose CAS-create lost or whose publish failed writes nothing — the next re-fire is unpaced, door 1's disposition (`:2136-2139`) |
| `GapEscalatedToAugur` for doors 2/3 | on a real fire, message per trigger | the seven sites of §3 C6 plus the class release's clear | a latch | set, not alerted (door 1's rule); `since` never re-stamped | a paced pass that fires nothing sets nothing — the standing record is the earlier fire's |

Reset paths that break shared numbering: a `weaver-state` wipe (`make down`) removes marks and documents
together, so no half-state survives; a re-authored playbook that adds the entry (door 2) or a catalog action
(door 3) is the **release**, not a reset.

---

## 6. Contract surface — builds to, no change

- Contract #10 (Augur), *Augur escalation*: "The block redirects that dead-end to the AI-reasoning tier" and
  the `escalate` grammar `["unplannable" | "exhausted", …]` — the two triggers are equal members of one list;
  this design makes the runtime treat them as one class.
- Contract #10 §10.3 (`10-orchestration-substrate.md:133-157`): a leased suppression whose expiry "makes the
  gap reclaimable". §10.3 partitions reclaims into the userTask class (collapse onto the open artifact) and the
  External class (a genuinely new attempt), and lists `directOp` under the latter; the reasoning claim is a
  `directOp` whose consumer collapses it (create-only), a third shape the text does not name. That reading is
  door 1's, shipped 2026-09-05 and ratified with it — this design carries it to the other doors and adds no
  reading of its own.
- Contract #10 §10.8 planner extension (`10-orchestration-weaver.md:345-347`): "'no plan derivable' flows into
  the existing `augur.escalate` `unplannable` trigger … no new trigger token."

A consumer observes, against the current text: fewer rejected reasoning ops, a Health latch that already exists
for the sibling trigger, and a gap that acts again when it can. None of those is a promise the text does not
already make. Nothing is staged in `docs/contracts/`.

---

## 7. Reconciliation with the existing mental model

- *Didn't the exhausted-gap fire just do this?* It gave door 1 the episode model and threaded a flag so doors 2
  and 3 would **book** nothing (door 3 does; door 2 never reached the flag, §1.2). Pacing, Health, republish
  withdrawal and any release were door 1's alone; this design moves them to the seam.
- *Doesn't the anti-storm mark already bound a live escalation?* Yes, for one lease. The harm is what happens
  at and after expiry: door 3 re-pins the escalation forever, door 2 is orphan-deleted and re-fired per delivery.
- *Does this add state we already keep?* One field on the mark (its class), door 1's pacing document written
  for a second trigger, and one restart rule in `bookDispatch`. No new bucket, key shape, or reader.
- *The reclaim never re-plans mid-episode (§10.8 "a sweep reclaim re-dispatches the pinned leg verbatim").* An
  escalation over no leg is not a leg; it is the fallback for having none, and re-resolving it is the boundary
  of a leg that never existed. An escalation over a leg keeps that leg's pin and releases only at its boundary
  (Rule 1) — the contract's sentence, kept.

---

## 8. Alternatives

| # | Alternative | Verdict |
|---|---|---|
| 1(a) | **Do not have this thing — leave doors 2 and 3 as they are.** Zero consumers; the harms are latent. | Rejected. The first `unplannable` opt-in inherits a permanent park (door 3) and an unbounded attempt tally with a false `LensEffectMismatch` (door 2) — the class the parent fire fixed for door 1, whose symptom Andrew filed from a live PO run. The fix *removes* threading. |
| 1(b) | **Do not have this thing — strike `unplannable` from `augur.escalate` and delete both doors** (~90 lines + 5 tests; contract edit). Zero consumers, zero migration, more code out than §4 puts in. | Not recommended: the planner mandate's designed exit for "no plan derivable" is this door (contract `:345-347`), and `mode:"goal"` targets are the growth path. A frozen-contract strike is Andrew's; listed so it is a one-look option. |
| 2 | Add a fourth class to `collapseOnlyReclaim` (`escalation`) so the ladder paces door 3. | Rejected. Pacing would key on the mark's `ClaimedAt` for doors 2/3 and on the count document for door 1 — two mechanisms for one fact; door 2 is still orphan-deleted, no release, no latch, no withdrawal; and the class still needs the mark to declare itself (the lint gate refuses a `rec.Action` argument). |
| 3 | Classify an escalation mark by shape alone ("its action fails to resolve") — no new field. | Rejected on soundness (§4.2): a removed catalog ref under an open leg has the same shape, and a release keyed on it dispatches a fresh leg beside an open human task. |
| 4 | Pace the `unplannable` re-fire on the **mark** (a re-fire tally on it) instead of creating a count document. | Rejected. The mark TTLs (1 h) below every backoff step past the second; the live census (§1.2) shows door 1 pacing on the document with its mark gone. Losing the mark would reset the exponent every hour. |
| 5 | Delete the pacing document at the release, revision-conditioned (the first draft). | Rejected — refuted in review: lane 1 holds the count at revision 0 for both door shapes (`needsCount` is false for `Action == ""`), so a conditioned delete conflicts forever; a blind delete would take the count leg's only handle on a quiet row. The kept document plus `bookDispatch`'s restart rule (§4.4) is both simpler and complete. |
| 6 | Give door 2 a synthesized playbook entry at install (materialize the escalation as the gap's action). | Rejected. The door exists for a column with no entry; and door 3's trigger is a runtime resolution install cannot see. |
| 7 | Gate the re-fire on a lens-projected `escalated_<g>` companion (lease-signing projects one from the proposal vertex, `lenses.go:708-722`) — P5-clean, like `inflight_<g>`. | Not built. It is package-opt-in and the engine default must be sound without it; lease-signing's column is read by the FE stepper only. A future design may read it as a suppression companion; no consumer asks today. |
| 8 | Rewrite the N consumers. | N = 0. |

Each rejection was run back against the recommendation: the recommendation needs no new class in the ladder
(2), no shape inference (3), no mark-carried pacing (4), no conditioned count delete (5), no package edit (6, 7).

---

## 9. Migration / compatibility

- **Mark field:** additive, `omitempty`; Loupe's decoders tolerate it (§2). Live population of escalation marks:
  0 (§3 C4). A door-1 mark minted between design and cycle: §4.2's old-shape paragraph (≤ 1 h, `warning`).
- **Count documents:** door 1's shape is unchanged; the one live document is door 1's. A `Count == 0` document
  now has two writers (the operator verb and an `unplannable` escalation); every decision-taking reader is
  walked in §4.4 and distinguishes them by the resolution or the stored leg, never by the zero.
- **No package edit, no contract edit, no bucket or key-shape change, no Loupe change.** Adoption is a weaver
  binary cycle (`make orchestration`, as the parent fire did).
- **Behaviour change for a re-authored goal catalog** (§4.2): a vanished pin is now `PlaybookConfigError`
  (long floor, no dispatch) instead of an `unplannable` escalation. No live target escalates `unplannable`, so
  today that arm already ends in `TemplateDataError` — the change is which config code names it.

---

## 10. Test strategy

| Test (owner) | Pins | Inc |
|---|---|---|
| `TestEscalateGap_NoEntryDoorBooksNothing` | door 2 via lane 1: the fire creates `{count:0, reclaims:1, escalatedAt}` and no `__effect` slot; the mark carries `escalation:"unplannable"`; `GapEscalatedToAugur` stands with the no-entry message; `GapWithoutPlaybook` cleared; the `msg.Sequence == 0` defer still first | 1 |
| `TestEscalateGap_NoPlanDoorBooksNothing` | door 3: the same on the `TestGoalMode_UnplannableEscalatesToAugur` fixture; `TestPlanGap_UnplannableEscalation_PreservesLegAndBooksNothing` amended for the seam | 1 |
| `TestBookEscalation_CreatesTheDocumentForUnplannableOnly` | absent document: `unplannable` → created; `exhausted` → `booked=false` (today's pin kept); a present document with `Count > 0` is updated for either trigger | 1 |
| `TestEscalateGap_UnplannableRefireIsPaced` | an expired escalation mark inside the window → Ack, no op, `reclaims` unchanged; past the window → one op, `reclaims+1`, `escalatedAt` advanced; the stale mark cleared revision-conditioned; the same with **no** mark | 1 |
| `TestEscalateGap_PublishFailureWithdrawsTheObligation` (doors 2/3) | the `replay_internal_test.go:361` pin re-run through the seam for each trigger | 1 |
| `TestResolveGoalAction_PinVanished_IsAConfigError` | amends `:85`: `errConfig`, not `unplannable`; the reclaim leaves the expired mark and alerts `PlaybookConfigError` at `warning` | 1 |
| `TestMark_EscalationSurvivesEveryReplace` | the field survives `fireEpisode`'s stale re-arm and the reclaim's `replace`; a revert of either threading reds it | 1 |
| `TestBookDispatch_RestartsOverAnEscalationOnlyDocument` | `{count:0, leg:"", escalatedAt}` + an attempt → `{count:1, reclaims:0, escalatedAt:""}`; an un-park document (`leg` set) keeps `reclaims`/`escalatedAt` — the ratified inheritance | 1 |
| `TestDispatchGap_PlannableAgainReleasesTheEscalation` | door 3 over no leg: live escalation mark + a row from which a plan now derives → mark deleted (conditioned), document kept, the fresh leg fires once, `Count == 1`, `reclaims == 0`, latch cleared | 2 |
| `TestDispatchGap_EntryAddedReleasesTheEscalation` | door 2: playbook re-authored with an entry (and separately with `surface`) → the mark is released and the real disposition runs | 2 |
| `TestDispatchGap_EscalationOverALeg_ReleasesOnlyAtItsBoundary` | door 3 with `count.Leg` = an open `assignTask` leg: a plannable row does **not** release; the leg's effects holding does (Rule 1); no second task | 2 |
| `TestDispatchGap_UnParkedExhaustedEscalationReleases` | an `exhausted`-class mark on a gap whose budget was re-armed → released on the general path, fresh episode booked; old-shape variant → `PlaybookConfigError`, mark left | 2 |
| `TestSweep_ReclaimOfUnplannableEscalationReResolvesFresh` | expired escalation mark over no leg: plannable row → mark deleted, **nothing dispatched by the reclaim**, arm (n) fires on the next pass; still unplannable → paced, no `mark reclaimed` Warn, no booking; `collapseOnlyReclaim` never called for it (a counter on the stub) | 2 |
| `TestSweep_ReclaimOfEscalationPreservesDisplacedLeg` (amended) | fixture gains the field and a paced-out `escalatedAt`; the re-fire preserves `escalatedFrom` | 2 |
| `TestSweep_OrphanArmSparesAStandingNoEntryEscalation` | door 2 mark + policy → not deleted, routed past the `entityKey` guard and the count read; policy removed → deleted with `orphanColumn` | 2 |
| `TestSweep_CountLegRefiresAMarklessEscalation` | `{count:0, escalatedAt}` + no mark + still-unplannable row → paced re-fire from the count leg; `{count:3, leg:legA, escalatedAt}` + no mark → routed above the zero test, Rule 1 tested, re-fire paced; a plannable leg-less document → arm (n) fires the plan | 2 |
| `TestSweep_CountLegDoorTwoRouteHonoursTheGates` | a door-2 document on a non-violating row, and on an `inflight_<g>` row → nothing fires | 2 |
| `TestSweep_CountLegNeverUnParksAnUnplannableZero` | the §4.4 table's first row, plus `ResetRetryBudget` refusing both doors | 2 |
| existing pins that must stay green | `TestHandleRow_LiveEscalationMarkNotTornDownAndRefired`, `TestEscalateExhaustedGap_*` (all), `TestFireEpisode_StaleReArm_TakesTheEscalationsLegFromItsCaller`, `TestDispatchGap_AugurPolicyRetiresGapWithoutPlaybook`, `TestResetRetryBudget_RefusesWhatItCannotHonour`, `TestSweep_CountLeg*` | 1, 2 |

Every fixture runs with and without a `maxretries_<g>` cap (the dossier's shared-fixture rule); every clear
assertion captures `since`; every §4.3 delete and every comment-claimed ordering (arm (j)'s route below the
gates; the escalation branch above the zero test) is revert- or move-proved line by line. No package touches
the `unplannable` door to "prove it live": which package opts in is product judgement, and the ephemeral-stack
e2e uses the synthetic goal target `TestGoalMode_UnplannableEscalatesToAugur` already builds.

---

## 11. Decomposition for the Steward — one fire, size S–M

- **Inc 1 — the seam and the class.** `escalateGap` extracted from `escalateExhaustedGap` (behaviour-neutral
  for door 1; every existing door-1 pin green before the doors move); `mark.Escalation` at all three write
  seams; `fireEpisode`'s trigger string; `bookEscalation`'s `unplannable` create; `bookDispatch`'s restart rule;
  the two doors routed (§4.1); `resolveGoalAction`'s config verdict (§4.2); the `escalated` threading removed and
  the reclaim's three consumers made unconditional with their comments rewritten. Tests: §10's Inc 1 rows.
- **Inc 2 — the release.** Rule 2 at the three sites of §4.3 (lane 1; reclaim + orphan-arm guard; count leg
  above the zero test, door 2 below the gates). Tests: §10's Inc 2 rows.
- **Docs.** `docs/components/weaver.md` §"Dispatch suppression" — the paragraph *"An escalation is booked
  nowhere and paced like a re-arm"* rewritten as *one episode, three doors* with the two release rules; the
  lane-1 decline table's `GapWithoutPlaybook` row gains its escalated disposition; `docs/components/augur.md`
  implementation-status rows for both triggers. Candidate dossier line for the close pass, if it lands twice:
  *a door built for one trigger is walked for every trigger sharing its consumer*.

Neither increment is posture-changing (no security plane, no capability, no Core-KV read). Review depth is the
Steward's sizing. Gates: `go test ./internal/weaver/ -count=1`, `go build ./...`, `make vet`, `golangci-lint
run ./internal/weaver/...`, every `scripts/lint-*.go` (`lint-weaver-classify-by-shape` in particular),
`make verify-kernel`, `make test-control-plane-authz`.

---

## 12. Risks + residuals

- **A stale pending proposal beside a released escalation.** A reviewer approving it dispatches through
  `augurDispatch`'s re-validation as today — the parent design's §12 disposition, unchanged and inherited.
- **The un-escalated `unplannable` goal gap alerts `TemplateDataError`** (`evaluator.go:~830`, the `errData`
  arm) — an accurate message under a sibling's code. Pre-existing, not this fire; a code `GapUnplannable` at the
  same key is a one-arm change the Steward may fold if the close pass agrees.
- **`staleMark`'s planned-mode limitation** stays parked (the parent's §12); an escalation mark never reaches it.
- **Skew** between two engine instances re-fires one backoff step early — door 1's existing exposure;
  single-instance today.
- **An un-parked static `directOp` gap's fresh attempt restarts its pacing pair** (§4.4) — priced there; no
  observable cadence change, one earlier escalation instant.
- **The reclaim's class release dispatches nothing**, leaving a quiet released gap to arm (n)'s next pass (≤ one
  sweep interval, 1 min) — chosen over re-implementing the collapse-only refusal in the reclaim.

---

## 13. Checklist walk (`agents/designer/SKILL.md` §2.3), items that bit

- **A.1 / A.5 (the demand is a hypothesis; a refusal's reason names a claim):** clause [a] was true of one door
  and false of the other (an orphan delete, not a re-arm); [d]'s "only bound" was no bound (§1.2). Both
  corrected in the fire.
- **A.4 (no live victim is a census nobody ran):** C1 — 0 targets escalate `unplannable`; both doors latent. The
  design is sized and sequenced on that, and §8 row 1(b) is the honest deletion alternative.
- **B.5 (the clear you are replacing):** door 3's re-pin (`reconciler.go:1176`) and door 2's orphan delete
  (`:915-927`) are the lines the release replaces; both named, both re-routed rather than left beside the new
  branch.
- **B.8 (a family's fourth member is wired where the first three are):** the third suppression site (the count
  leg) is a re-fire site for door 1 and had to become one for the `unplannable` doors (§4.3), or a quiet row's
  dead claim would never retry. The reviewer found the same rule unapplied to `replace`'s third caller and to
  the reclaim's three `escalated` consumers — folded.
- **D.4 (presence at zero now means two things):** §4.4's reader table, run against every declared reader of
  `Count == 0` and of presence (C5) — and the reviewer added two readers the first table missed.
- **D.2 (borrowed predicate / inert guard):** the shape-only classification (§8 row 3) would have borrowed the
  reclaim's resolution failure as a class test; the mark field is the structural fix.
- **D.7 (where a check sits is a constraint):** the first draft placed the door-2 count-leg route at an arm
  above the `violating` gate and the orphan-arm guard above the `entityKey` and count reads; both moved.
- **E.1 (lifetime at every boundary):** §5, including the never-written row for a lost CAS and the
  no-inheritance rule for the fresh chain's exponent — which moved from "the release deletes the document" to
  "`bookDispatch` restarts over it" once the conditioned delete was refuted.
- **F.4 ("mirrors X" — read above X):** `bookEscalation`'s refusal comment was read for its reason, and the
  reason's world (door 1) was shown not to be the `unplannable` doors' (§4.4). The first draft then mirrored
  `releaseCompletedLeg`'s *markless* branch for a release that holds a mark — the marked branch was the mirror.

---

## 14. Adversarial pass — run (2026-09-05, cold read-only reviewer against the code)

The reviewer traced every §1.2 cell to its line and could not falsify the per-door mechanism (the one
over-claim — "nothing can ever close" an `__effect` slot — is softened to "the gap's own close credits at most
one"). Hunts that found nothing: booking (no path books an escalation, no ordinary episode stops booking, once
the reclaim's guards are handled); pacing arithmetic (`reclaims:1` → second fire at the base, identical to door
1); the paced-mark TTL widening (unreachable for an escalation mark); the `unplannable` door's reach (goal gaps
only — both set sites are in `resolveGoalAction`); latch flap (`since` is never re-stamped). Findings, all
folded:

| Sev | Finding | Folded into |
|---|---|---|
| BLOCKING | the lane-1 release conditioned its count delete on `countRev`, which lane 1 holds as 0 for both door shapes (`needsCount` false for `Action == ""`); `LastRevision(0)` against a live key conflicts, so the release would Ack forever | §4.3: one write (the mark, the marked branch's mutex); §4.4: the document is never deleted by a release; §8 row 5 |
| BLOCKING | routing only the `unplannable` class while turning the pinned-not-in-catalog arm into `errConfig` strands every `exhausted`-class mark that reaches the general path (an un-park) and every old-shape mark, on a false `PlaybookConfigError`; the fixture behind a "must stay green" pin writes no field | §4.2: route on `Escalation != ""`; the old-shape paragraph; the fixture list; `TestDispatchGap_UnParkedExhaustedEscalationReleases` |
| MAJOR | "the displaced leg is empty by construction" was wrong — `escalationLeg` with no mark reads `count.Leg`, and a plannable-now release would dispatch beside an open human task | §4.3 Rule 1 (leg boundary only, over a leg) + Rule 2 (over no leg); `TestDispatchGap_EscalationOverALeg_ReleasesOnlyAtItsBoundary` |
| MAJOR | `replace` has a third caller (the reclaim, `:1226`) the field must survive — the parent fire's own shipped bug re-imported | §4.2 (three write seams); `TestMark_EscalationSurvivesEveryReplace` |
| MAJOR | removing `planGap`'s `escalated` return removes three reclaim behaviours (`:1218-1222`, `:1231`, `:1244`), not one, and `:796`'s comment's operative half | §4.1 door 3: the consumers named, made unconditional by construction, comments rewritten |
| MAJOR | the count leg's re-fire route below the `Count != 0` test never reaches a markless escalation over a leg (`Count > 0`) | §4.3 count leg: the branch sits above the zero test, Rule 1 first; §4.4 row 2; `TestSweep_CountLegRefiresAMarklessEscalation` |
| MAJOR | the latch enumeration missed `releaseCompletedLeg`'s clear (`:1580`) and the reason it exists; the release added no clear | §3 C6 (seven sites); §4.1 item 7; the class release clears |
| MAJOR | the door-2 count-leg route at arm (j) sat above the `violating` and suppression gates; arm (j)'s recorded non-termination reason was never retired | §4.3 count leg (gates restated inline; the reason quoted and retired by the orphan-arm guard); `TestSweep_CountLegDoorTwoRouteHonoursTheGates` |
| MAJOR | the orphan-arm guard was specified above the `entityKey` guard and the count read the seam needs | §4.3 reclaim (the mark falls through to both, then routes) |
| MINOR | arm (n)'s release inherited the escalation's `reclaims`/`escalatedAt` through `bookDispatch`'s no-restart for an empty leg, contradicting the lane-1 bullet | §4.4 restart rule; `TestBookDispatch_RestartsOverAnEscalationOnlyDocument` |
| MINOR | C5 missed `releaseCompletedLeg`'s presence test and `reArmDeclines`' `!planned` arm | §3 C5; §4.4 table |
| MINOR | "nothing can ever close it" over-claimed; §6 read a class §10.3 does not partition | §1.1/§1.2; §6 |
| MINOR | citation drift (nine lines) and the seam re-deriving `augurEscalation` | re-anchored; `escalateGap(esc, …)` |

Verdict as returned: *needs the two BLOCKING items re-shaped before build; the rest are same-fire folds*. Both
re-shapes are in the body above (the release is one conditioned mark delete; routing is by class presence), and
the design is build-ready at size S–M.
