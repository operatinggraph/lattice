# An exhausted gap re-plans at the leg boundary the contract promises, and a retry budget books attempts

**Status: ✅ RATIFIED (Winston-adjudicated, per the 2026-08-20 delegation) — build-ready (2026-09-04).**
Board row: `[Weaver] An exhausted gap never re-plans, and re-escalates every 30 minutes forever`
(`backlog/lattice.md`, ★★★, S–M; filed by the LoftSpace PO 2026-09-04, commit `0797e540`). Blocks the
verticals row *The tenant cannot sign their own renewal, and the term ends in two days*.

## For Andrew

**What it does, in two lines.** (1) A gap whose retry budget is spent keeps the plan leg it was on, so the
moment that leg's declared effects hold in the row the pin releases and the next leg dispatches — the
§10.8 promise the escalation was silently destroying. (2) The budget counts *attempts*: a sweep re-claim that
collapses onto a still-open human task, and an Augur escalation's own re-fire, no longer spend it — the same
predicate the `__effect` window already uses — and the escalation re-fire is paced like every other
collapse-only episode instead of every 30 minutes.

**No architectural fork.** Every decision is mechanism inside `internal/weaver`, on a seam the Planner
extension already defines (pin-release at effects-hold). The one product-shaped question — *should a human
task that sits unactioned for 15 hours count as six failed retries and be handed to the AI tier?* — is
answered by the ratified mechanism it replaces: the budget was ruled to track "real attempts, one per
anti-storm window" (§2.1 G4), and by the contract's own liveness invariant, under which an assigned task
already means "a human now owns it".

**No contract change.** Every behaviour here is the runtime keeping a promise the contract already makes:
§10.8's *"the pin releases once the leg's declared `effects` all hold in the current row"* and *"Pin-release
… resets the gap's dispatch count (per-leg budget semantics)"*, §10.8's *"Mark clears on … planned-leg
completion"*, and §10.2's *"the retry budget"*. The §10.3 reserved-key list calls `__count` "the per-gap
dispatch counter" — a mechanism sentence in a list of private key shapes, which a pure refactor would falsify;
it is not amended here. §6 quotes each sentence.

**Immediate operator stopgap (not this design's build):** the live renewal
`vtx.renewal.QomdjY7hAGS6mHvN9d2j` ends 2026-09-06. Its old-shape state carries no leg pin, so Inc 2 cannot
release it retroactively (§8). Two `weaver-state` deletes plus `lattice weaver replay-target renewalComplete`
re-plan it today — §8 gives the exact keys. The Steward should run that in the fire's Phase 0.

---

## 1. Problem + intent

### 1.1 What the filing said, verbatim, split at every clause

From the filing commit `0797e540` (LoftSpace PO, 2026-09-04):

> The platform half: the missing_renewalComplete gap exhausted its budget on 2026-08-29 17:35, roughly a
> day BEFORE the landlord set the terms that made signRenewal reachable, **[a]** and the planner has never
> re-planned since. **[b]** It has instead re-submitted CreateAugurReasoningClaim against the target meta
> every ~30 minutes (weaver.log, unbroken through 21:03 yesterday) — 265 dispatches against a budget of 6,
> **[c]** with LensEffectMismatch reporting zero observed closes over the last 20 — **[d]** while the single
> proposal it produced still reads review.state "pending" six days on, proposing assignTask
> ApproveLeaseApplication scoped to a renewal ("no playbook entry").

| Clause | Section | Disposition |
|---|---|---|
| [a] exhausted a day before the terms landed | §1.2, §3 Inc 1 | The budget was spent by re-claims that mounted no attempt. Fixed: the budget books attempts. |
| [b] never re-planned since | §1.3, §3 Inc 2 | The escalation overwrote the leg pin, so the contract's effects-hold release had nothing to test. Fixed: the pin survives escalation. |
| [c] re-submits every ~30 min, 265 vs 6; zero closes over 20 | §1.4, §3 Inc 3 | Each re-fire is a *rejected* op (create-only conflict), booked into the gap's count and into a pseudo-action `__effect` window. Fixed: an escalation books neither, and re-fires on the collapse-only backoff. |
| [d] the proposal is pending six days, and proposes an out-of-playbook action | **declined** | Human review latency and the model's proposal quality are the Augur review surface's concern (Loupe `review.go`), not Weaver's. Under Inc 2 the gap re-plans regardless of the proposal's state; the stale proposal stays reviewable and rejectable. |

### 1.2 [a] — how six was spent with zero attempts

The retry budget is one `weaver-state` document per (target, entity, gap): `…__count` `{count}`
(`internal/weaver/state.go:291-294`), incremented at two seams — the lane-1 CAS-create-and-fire
(`evaluator.go:860`, `bumpDispatchCount`) and the sweep's reclaim after its in-place replace
(`reconciler.go:1094`). The gate is `count >= maxretries_<g>` (`evaluator.go:1464`).

The sweep's reclaim of a **collapse-only** episode — an `assignTask` or userTask-parking `triggerLoom` whose
`claimId` is preserved verbatim so the re-dispatch collapses onto the task already open
(`reconciler.go:63`, `collapseOnlyReclaim`) — is paced by an exponential backoff whose exponent is that same
count (`reconciler.go:147`, `backoffInterval`: base = the 30-minute mark lease, `reconciler.go:17`; cap 24 h,
`engine.go:117-120`). And it **advances the count on every re-arm**, by explicit decision:

> `reconciler.go:1105-1112`: *"The retry-budget count above is deliberately NOT gated: it bounds reclaim
> effort per §10.8, which is exactly what a repeat reclaim spends."*

That sentence was written in `5b58f667` (2026-07-19), the fix that stopped the **`__effect` window** from
booking the same collapse-only re-claims — because *"a human userTask held open across enough reclaims filled
its whole window with pending slots nothing could close"*. The count was exempted from the same fix on the
"reclaim effort" reading, but the count has a second reader the exemption never priced: the exhaustion gate.
So for a human task left unactioned, the series is

| reclaim # | fires at (after dispatch) | count after |
|---|---|---|
| 1 | 30 min | 2 |
| 2 | +1 h | 3 |
| 3 | +2 h | 4 |
| 4 | +4 h | 5 |
| 5 | +8 h | 6 → **exhausted** |

**≈ 15.5 hours of a landlord not opening a task spends a budget of six with zero retries mounted**, and the
gap is handed to the Augur tier. That is the day between 08-29 17:35 and the terms landing on 08-30 13:04:52Z
(`termsSetAt` in the live row, §2.3).

### 1.3 [b] — why the terms landing changed nothing

`renewalComplete` is the corpus's only goal-mode target (§3 C2): one gap, a four-leg catalog
(`refreshBgcheck` → `setTerms` → `verifyGuarantor`? → `signRenewal`; `packages/lease-signing/renewal_targets.go:64-153`),
`maxretries_renewalComplete = 6` projected **per leg** — its own design says *"per-leg retry bound (the count
resets at pin-release)"* (`loftspace-lease-renewal-goal-authored-target-design.md:185`).

The contract's per-leg semantics are real and shipped: `releaseCompletedLeg` (`evaluator.go:1217`) clears the
mark, deletes the count and credits the leg's `__effect` close when the **pinned** leg's effects hold. But it
resolves the leg from the mark's `action`, and the escalation **replaces that mark** with its own — action
`"directOp"`, the reasoning op's dispatch class (`evaluator.go:1608-1636` clears the stale leg mark, then
`fireEpisode` CAS-creates the escalation's). `"directOp"` is not a catalog ref, so `releaseCompletedLeg`
returns false at its catalog lookup (`evaluator.go:1225-1233`), and `resolveGoalAction` treats the pin as
"vanished" (`strategist.go:520-537`). Three consequences:

1. **Lane 1** (`evaluator.go:177-205`): every delivery of the changed row reads the gate first — exhausted —
   and routes to `escalateExhaustedGap`, which finds the escalation's live mark and Acks. `dispatchGap`, the
   only lane-1 caller of `releaseCompletedLeg`, is never reached.
2. **The sweep's reclaim** (`reconciler.go:891-912`) runs `releaseCompletedLeg` first — but on the
   escalation's mark, whose action is not a leg. False. Then the gate: exhausted → re-escalate.
3. **The count leg** (`reconciler.go:629-640`) has no mark at all to read a leg from.

So the fact the contract promises to act on — *the leg's effects hold* — is true in the row and tested by
nobody. `signRenewal`'s `pre` (the goal's full remainder) is satisfied in the live row (§2.3), the fresh plan
is the single leg `[signRenewal]`, and nothing will ever synthesize it.

### 1.4 [c] — what the 30-minute loop actually does

`escalateExhaustedGap`'s re-fire arm (`evaluator.go:1587-1636`): a **stale** escalation mark (lease
expired) is cleared and the escalation fired fresh — *"the ONLY recovery a dead escalation episode has: the
reasoning claim's own Loom instance or bridge call can be lost, and nothing else re-derives it."* Its re-fire
rate is one per mark lease (30 min), and each re-fire is a **fresh** episode — a fresh `requestId`, so the
Contract #4 tracker does not collapse it.

But the op it re-submits is create-only on the claim vertex:

> `packages/augur/ddls.go:511-513`: *"A redelivered instanceOp conflicts create-only on the root vertex and
> is rejected."*

So past the first commit, **every re-fire is a rejected op**. The recovery the arm exists for — a claim whose
vertex was never minted — is real only until the first commit lands; after that the arm is a 30-minute
rejected-op generator that Weaver never observes (it reads no op outcome — no `reject`/tracker read anywhere
in `engine.go`/`actuator.go`). And each re-fire is booked twice against the gap it is not part of: the gap's
own `count` (`fireEpisode` → `bumpDispatchCount`, unconditional — the live count is **309**) and an
`__effect` window keyed on the pseudo-ref `"directOp"` (`bumpEffectDispatch` with `actionRef == esc.Action`),
which fills with 20 pending slots no close can answer and raises the `LensEffectMismatch` the PO quoted
(`health.go:887-888`, warning only — a false alarm, not a gate).

---

## 2. Grounding ledger

### 2.1 Claims, and what settled them

| # | Claim | Verdict | Where |
|---|---|---|---|
| G1 | The budget is per (target, entity, gap) and never resets while the gap stays open, except at pin-release / gap-close / operator reset | **Confirmed** | `state.go:71-89` (chain-scoped, 256×lease TTL); `evaluator.go:1093`, `:1249`; `reconciler.go:586`; `control.go:342` |
| G2 | A collapse-only reclaim advances the count | **Confirmed, deliberate** | `reconciler.go:1094` + the comment `:1105-1112`; commit `5b58f667` |
| G3 | The same reclaim is *not* booked to `__effect` | **Confirmed** | `reconciler.go:1113-1115` `if !collapseOnly` |
| G4 | The ratified budget semantic is attempts | **Confirmed** | `async-reply-increment3-design.md:152-175` (Andrew's option B): *"so the count tracks real attempts, one per anti-storm window"*; `docs/components/weaver.md:376-380`: *"tracks one-per-anti-storm-window real attempts"*; `augur-design.md:123-126`: *"tried its one fixed action `maxretries` times and gave up"* |
| G5 | Pin-release at effects-hold is a contract promise, and it resets the count | **Confirmed** | `docs/contracts/10-orchestration-weaver.md:316-333`; `:249-251` |
| G6 | The escalation overwrites the leg pin | **Confirmed** | `evaluator.go:1608-1636`; live mark body §2.3 (`"action":"directOp"`) |
| G7 | The re-fire is a rejected op after the first commit | **Confirmed** | `packages/augur/ddls.go:511-513`; the live cadence §2.3 (6 fresh requestIds in 2.5 h) |
| G8 | The escalation is booked into the gap's count and an `__effect` window | **Confirmed** | `evaluator.go:1641` `fireEpisode(… actionRef …)` with `actionRef = esc.Action`; `:860-871`; live `renewalComplete.__effect.missing_renewalComplete.directOp` = 20 × false |
| G9 | `LensEffectMismatch` gates nothing | **Confirmed** | `health.go:887-888` sets a warning; `planner_shadow`'s close-rate reader serves no installed target (`control.go:289-291`) |
| G10 | The operator un-park verb cannot reach a goal-mode gap | **Confirmed** | `control.go:580-583` refuses `ga.Action == ""`; the count leg's re-arm arm declines the same (`reconciler.go:701-707`); Loupe omits the verb entirely (`cmd/loupe/control.go:58-64`) — CLI only (`cmd/lattice/weaver/weaver.go:322`) |
| G11 | The escalation's re-fire is unpaced by construction | **Confirmed** | the mark's action `"directOp"` reads not-collapse-only in `collapseOnlyReclaim`; but the reclaim path never reaches its pacing block for an exhausted gap — the gate routes to `escalateExhaustedGap` first (`reconciler.go:926-949`), which paces on the lease alone |
| G12 | Weaver observes no op rejection | **Confirmed** | `grep -n -i "reject\|declin" internal/weaver/engine.go internal/weaver/actuator.go` — comments only |
| G13 | The filed "265 dispatches vs budget 6" | **Confirmed and restated**: 309 on 2026-09-04 18:54 PT, all rejected past the first; the 6 were spent with zero mounted attempts (§1.2) | §2.3 |
| G14 | An in-flight Weaver fire overlaps this seam | **Partly** | `weaver-surface-workload-vs-fault-issues-design.md` (🏗️ `fire/weaver-surface-workload`) edits the surface arm (`evaluator.go:316-334`) and issue-latch loudness at `issueKeyGapEntity`; it does not touch the count record, `gapSuppressed`, the reclaim's booking or the escalation's re-fire arm. **Sequence this build after it lands** and re-run the touch list at Phase 0 (§9). |

### 2.2 The two populations the one count served

| Reader of `__count` | What it needs to grow with | Which re-arms should advance it |
|---|---|---|
| The exhaustion gate (`verdict`, `evaluator.go:1464`) | attempts mounted against the vendor / the task / the op | fresh episodes; external and `directOp` reclaims (each a real re-submit) |
| The backoff exponent (`backoffInterval`, `reconciler.go:147`) | how many times the sweep has re-armed this same open episode | every reclaim, collapse-only included |

One integer cannot serve both (`feedback_shared_budget_asserts_one_cardinality_law`). The exemption in
`5b58f667` reasoned about the second reader and shipped the count into the first.

### 2.3 Live evidence (shared dev stack, 2026-09-04 18:54 PT; read via `nats … kv get`, `--raw`)

**Mark** `renewalComplete.QomdjY7hAGS6mHvN9d2j.missing_renewalComplete`:
```
{"targetId":"renewalComplete","entityKey":"vtx.renewal.QomdjY7hAGS6mHvN9d2j","gap":"missing_renewalComplete",
 "action":"directOp","claimId":"FMZVEu6Au4gm5KTnimSr","claimedAt":"2026-09-05T01:54:27Z","leaseExpiresAt":"2026-09-05T02:24:27Z"}
```
**Count** `…__count`: `{"count":309}` (was 308 one read earlier — advancing once per re-fire).

**Row** `weaver-targets renewalComplete.QomdjY7hAGS6mHvN9d2j` (the goal remainder holds; the fresh plan is
`[signRenewal]`):
```
"bgcheckValidUntil":"2026-10-03T19:46:50Z","hasGuarantor":false,"guarantorVerifiedAt":null,
"termsSetAt":"2026-08-30T13:04:52Z","signedAt":null,"maxretries_renewalComplete":6,
"missing_renewalComplete":true,"violating":true
```
**Effect windows** (`renewalComplete.__effect.missing_renewalComplete.*`): `directOp` 20/20 false (the
mismatch); `setTerms` 12 attempts, 0 closes; `refreshBgcheck` 8 attempts, 2 closes.

**Cadence** (`weaver.log`, rotated at the 16:09 restart): `CreateAugurReasoningClaim` submitted 16:21, 16:51,
17:22, 17:52, 18:23, 18:54 — six fresh requestIds in 2.5 h, one per lease.

**Population:** `weaver-state` holds **1** `__count`, **1** mark, 39 `__effect` windows. The exhausted-gap
population is exactly this row.

---

## 3. Censuses (run in this fire; the build's Phase 0 re-runs them mechanically)

**C1 — capped gaps and their dispatch class** (which gaps Inc 1 can change the behaviour of):
```sh
for g in bgcheck charge claim completion credentialResidue dedupResidue erasureSeal onboarding operation \
         payment price refund release reminder renewalComplete settle; do
  f=$(grep -rln "\"missing_$g\"" packages/*/*targets*.go | head -1)
  echo "$g | ${f#packages/} | $(grep -A4 "\"missing_$g\"" $f | grep -o 'Action:\s*"[a-zA-Z]*"\|Goal:' | head -1)"
done
```
Result: 16 capped gaps — 10 `directOp`, 2 external `triggerLoom` (`bgcheck`, `payment`), 3 `surface`, **1
goal-mode (`renewalComplete`)**. `directOp` and external reclaims are real re-submits and stay attempts;
`surface` never dispatches. **The only gap whose legs are collapse-only is `renewalComplete`** — Inc 1's
live effect is one target; its class (any planned target with a human leg) is the reason it is a platform
rule and not a lens edit.

**C2 — planned-mode targets:** `grep -rln 'Mode:.*"planned"' packages/*/*.go | grep -v _test` → 1
(`lease-signing/renewal_targets.go`).

**C3 — targets escalating `exhausted`:** `grep -rn 'Escalate:.*"exhausted"' packages/*/*.go | grep -v _test`
→ 3 (`lease-signing` ×2, `wellness-ledger`). The two non-goal ones are `directOp`/external gaps: Inc 2's
release test is a no-op for them (no leg), Inc 3's pacing applies to all three.

**C4 — every reader of the count document:** `grep -n "getDispatchCount\|dispatchCountEntry\|incrementDispatchCount\|resetDispatchCount\|deleteDispatchCount\|backoffInterval(" internal/weaver/*.go | grep -v _test`
→ gate reads: `evaluator.go:1414`, `reconciler.go:629` (via `gapSuppressedWithCount`), `control.go:378`;
backoff reads: `reconciler.go:1015`, `:1035`; writers: `evaluator.go:871`, `reconciler.go:1094`
(increment); `evaluator.go:1093`, `:1249`, `reconciler.go:586` (delete); `control.go:389` (reset to 0).
Loupe decodes the body tolerantly (`cmd/loupe/weaver.go:224-228` keys only; the mark struct `:208-218` has no
`DisallowUnknownFields` anywhere in `internal/weaver` or `cmd/loupe`).

**C5 — every reader of the mark's `action`:** `grep -n "rec.Action\|\.Action ==\|pinnedAction" internal/weaver/evaluator.go internal/weaver/reconciler.go internal/weaver/strategist.go | grep -v _test`
→ `releaseCompletedLeg` (`evaluator.go:1217`), `resolvePlannedAction`/`resolveGoalAction`
(`strategist.go`), the reclaim's `collapseOnlyReclaim(rec.Action, …)` and `planGap(…, rec.Action)`
(`reconciler.go:986`, `:1053`), `deleteMark`'s close credit (`reconciler.go:1194`). None reads a field the
escalation mark does not already carry; Inc 2 adds one and changes what `releaseCompletedLeg` resolves from.

**C6 — the rejection is create-only:** `grep -n "conflicts create-only" packages/augur/ddls.go` → `:512`.

---

## 4. The shape

Three increments on one seam, plus a small verb increment. All `internal/weaver`; no package edit, no new
KV bucket, no new key shape — two fields on an existing private document and one on the mark.

### 4.1 Increment 1 — the budget books attempts; the backoff keeps its own exponent

**The count document grows two fields** (`state.go:291-294`):
```go
type dispatchCount struct {
    Count       int    `json:"count"`                 // attempts mounted in this chain — the exhaustion gate's input
    Reclaims    int    `json:"reclaims,omitempty"`    // sweep re-arms of this open episode — the backoff exponent
    Leg         string `json:"leg,omitempty"`         // the actionRef the attempts are charged to (Inc 2)
    EscalatedAt string `json:"escalatedAt,omitempty"` // last escalation fire (Inc 3)
}
```
- `incrementDispatchCount(ctx, target, entity, gap, actionRef, attempt bool)`: `attempt` ⇒ `Count++`;
  always `Reclaims++` when called from the sweep's reclaim; `Leg = actionRef`, and a **different** `actionRef`
  than the stored one restarts `Count` at 1 (a leg change *is* a new chain — belt-and-braces beside the
  release path, which deletes the document; it also makes the never-released regression case honest).
- **The seam and the predicate are the ones that already exist.** The reclaim's booking (`reconciler.go:1094`)
  passes `attempt = !collapseOnly` — the same `collapseOnly` the `__effect` booking two lines below already
  branches on, so the count and the window book the same attempts, by construction. Lane-1's CAS-create-and-fire
  (`evaluator.go:871`) stays `attempt = true` (a fresh episode is an attempt).
- `backoffInterval(count)` (`reconciler.go:147`) reads `Reclaims` — `reconciler.go:1015`, `:1035` — so a human
  task pending a week still climbs 30 min → 24 h exactly as today, while its `Count` stays at 1.
- **Lane 1's own stale-external re-arm is a reclaim too.** `fireEpisode`'s `found && stale` branch
  (`evaluator.go:779-826`) re-arms an expired external episode in place and bumps the count at `:823`; it
  books `attempt = true` *and* `Reclaims++`, so the two seams that re-arm an episode agree.
- **`resetDispatchCount` becomes a read-modify-write.** Today it marshals a fresh `{count:0}` literal
  (`state.go:447-460`) and its interface (`retryBudgetStore`, `state.go:400-403`) hands the caller only an
  `int`. Widen `dispatchCountEntry` to return the document, and have the reset write `{count:0, reclaims,
  leg, escalatedAt}` at the read revision — an operator's un-park does not erase the pacing history of an
  episode that may still be open. The one test double, `budgetStoreStub` (`control_internal_test.go:1116-1135`),
  follows; no build-tagged harness implements this interface (adversarial pass, §14). `deleteDispatchCount`
  stays a blind delete: a stale "gap closed" read racing a pacing write loses `Reclaims`/`EscalatedAt`, and the
  failure direction is *fire sooner* (a base-interval backoff), never a park — named in §5.

**What changes for each capped class (C1):** `directOp` and external gaps — nothing (their reclaims are
attempts and are not paced today, so `Reclaims` is written and never read). `surface` — nothing. Goal/candidate
gaps with a collapse-only leg — the leg's budget now bounds task *creations*, not time-on-inbox. The engine
default budget (`defaultDirectOpRetryBudget`, `evaluator.go:1379`) is `directOp`-only — unchanged.

### 4.2 Increment 2 — the escalation preserves the leg pin, and every suppression site honours the boundary

**The mark gains `escalatedFrom`** (`state.go:106-115`): the leg actionRef the gap was on when its budget
spent. `escalateExhaustedGap` sets it from the stale mark it clears (`rec.Action`, `evaluator.go:1608-1636`),
or — with no mark (the count leg's path) — from the count document's `Leg`. An ordinary leg mark leaves it
empty. **`legOf(mark, count)`** = `mark.EscalatedFrom` if set, else `mark.Action` if the mark is a leg mark,
else `count.Leg`.

**`releaseCompletedLeg` takes the leg from `legOf`, not from `pinnedAction` alone** (`evaluator.go:1217`): a
goal gap whose resolved leg's `effectGuards` all hold in the current row releases — deletes the mark
(revision-conditioned on the caller's read), deletes the count, credits the leg's `__effect` close, and clears
the gap's `issueKeyGapEntity` latch (`GapBudgetExhausted` / `GapEscalatedToAugur` both state a fact about a
budget that no longer exists). Non-goal gaps: unchanged (`ga.Goal == nil` ⇒ false).

**The release runs at the top of `escalateExhaustedGap`**, after the `surfaceOnlyGap` bail and *before* the
augur-policy check (`evaluator.go:1562`): a leg boundary is a fact whether or not the target escalates. The
function takes the count document as a parameter: the count leg already holds it from the read that decided
the gap was exhausted (the same one-read rule `gapSuppressedWithCount` states, `evaluator.go:1439`), and the
two mark-holding sites read it once — no second round trip per parked gap per pass.

**Every raise and clear at the shared latch `issueKeyGapEntity(target, entity, gap)`, enumerated** (the
dossier's rule before adding a clear): raises — the `surface` arm (`evaluator.go:331`; a `surface` column is
never a goal gap, disjoint by playbook), `GapBudgetExhausted` (`:1572`), `GapEscalatedToAugur` (`:1687`);
clears — `planGap` on a built plan (`:650`), `escalateExhaustedGap`'s pre-escalation clear (`:1584`),
`retireClosedGapIssues` at gap-close, the tombstone prefix clear (`:1009`), and the count leg's corrupt-body
arm (`reconciler.go:606`, which returns before any release or escalation in the same pass). The set was
enumerated by `grep -n "issueKeyGapEntity(" internal/weaver/*.go | grep -v _test` — the build's Phase 0
re-runs it and treats a new hit as scope. The release's own clear is needed
because the fresh plan can fail to build (a config error alerts at `issueKeyGapConfig`, a different key) and
would otherwise leave "escalated to Augur" standing over a gap that has left the reasoning tier. Nothing
re-raises at the key in the same pass after a release, so the test asserts membership *and* that a fresh
raise, if any, carries a new `since`. On a
true return the function re-plans fresh and fires the next leg exactly as the reclaim's leg-advance does
(`reconciler.go:891-912`: `planGap(…, "")` → `fireEpisode(…, found=false)`), returning its decision.
Because all three suppression sites route through this one function — lane 1 (`evaluator.go:195`), the
reclaim (`reconciler.go:943`), the count leg (`reconciler.go:634`) — the boundary is honoured on a delivery,
on a lease expiry and on a quiet row alike, and the dossier's "every RETIRE above every cannot-act GUARD"
rule is kept: the release is a retire, the policy check is a guard.

**Mid-chain regression** stays as ratified: if the escalated leg's effects do *not* hold, nothing releases and
the escalation stands. A regression that later re-satisfies the leg (a bgcheck that lapses and is refreshed by
a human) releases then. There is no "re-plan on any row change" — a row change without leg progress is not
evidence (§7 row 4).

### 4.3 Increment 3 — the escalation is a collapse-only episode: booked nowhere, paced like one

- **`fireEpisode` learns the episode's kind** (`evaluator.go:766`): `escalation bool`. An escalation fire
  books neither `bumpDispatchCount` nor `bumpEffectDispatch` (`:871-872`) — its lineage is the reasoning
  claim's, keyed by `deriveAugurHandle` (`actuator.go:189`), and its "actionRef" `"directOp"` is a dispatch
  class, not a catalog ref. `bumpOscillation` stays (the reasoning op's declared effects are still effects).
  The live `…__effect.missing_renewalComplete.directOp` window is retired by the existing
  `resetConfidence` verb in the build note; no new janitor.
- **Re-fire pacing is a level test on the count document, not the mark.** In the stale-or-absent arm
  (`evaluator.go:1608-1636`): if `now − count.EscalatedAt < backoffInterval(count.Reclaims)` → Ack, leave
  the stale mark (its TTL, 2 × lease, is the backstop — an absent mark and a stale one take the same arm, so
  TTL loss changes nothing). Otherwise clear, fire, and write `EscalatedAt = now`, `Reclaims++` on the
  document (one CAS write, the same retry loop `incrementDispatchCount` uses). First fire: `EscalatedAt`
  absent ⇒ immediate. Series: 30 min, 1 h, 2 h, 4 h … 24 h cap — the never-minted-claim recovery survives at
  the cost every other collapse-only episode already pays, and a pending proposal costs one rejected op a day
  instead of 48.
- **The first fire keeps its shape**: a fresh episode, fresh `requestId`, the escalation's own mark. The live
  arm (`found && leaseLive` → Ack) is unchanged.

### 4.4 Increment 4 — the un-park verb and the re-arm arm resolve a goal leg the same way

`reArmDeclines` (`control.go:569-600`) refuses every planned/goal gap because *"the sweep's re-arm never runs a
plan to find out what it would fire"*. That is a claim about the arm, and the arm's reason
(`reconciler.go:701-707`: the decision must precede `planGap`, which consumes an admission token) is met by
resolving the leg without planning: `resolvePlannedAction(…, pinnedAction: "")` is a pure function of (row,
catalog) for a goal gap (`strategist.go:511`, `Synthesize`) and reads only `__effect` windows for a candidates
gap (`rankCandidates`) — no admission token, no plan build. One helper, `resolvedLegAction(ctx, target, …)`,
used by both the verb and the arm: the leg's dispatch action then takes the **same** `collapseOnlyReclaim`
refusal the verb already applies to static gaps — an `assignTask` leg is still refused ("its artifact may still
be open"), a `directOp`/external leg is accepted. The dossier's rule holds: the verb refuses exactly what the arm
permanently declines, because they share the resolver. Cost: `Synthesize` runs once inside the verb's
control-plane handler (5 s deadline, `control.go:481-486`) and twice per re-armed gap per sweep pass (the
resolver, then `planGap`) — a bounded search over a catalog of at most `len(actions)+2` depth (four legs
today), microseconds against a handler measured in seconds; the arm runs only for the rare `count == 0` key.

---

## 5. State-lifetime table

| Field | Created | Advanced | Reset / carried at each boundary | Read by |
|---|---|---|---|---|
| `count.Count` (attempts) | first attempt (`{count:1}`) | fresh episode; external/`directOp` reclaim | **gap-close**: deleted (`clearClosedMarks`, `deleteCount`); **pin-release**: deleted; **leg change at increment**: restarts at 1; **operator reset**: 0; **TTL 256×lease**: GC only; **crash/restart**: durable, carried | the gate (3 sites), `resetBudget` |
| `count.Reclaims` (re-arms) | first re-arm (sweep reclaim, or lane 1's stale-external re-arm) | every reclaim re-arm; every escalation re-fire | deleted with the document at gap-close / pin-release (a blind delete racing a pacing write loses it — direction: the next backoff starts at base, i.e. *fire sooner*); **kept across operator reset**; carried across restart | `backoffInterval` (reclaim pacing, mark-TTL widening, escalation pacing) |
| `count.Leg` | first attempt | rewritten on every attempt increment | deleted with the document | `legOf` when no mark exists (count leg) |
| `count.EscalatedAt` | first escalation fire | every escalation fire | deleted with the document; absent ⇒ "fire now" | the re-fire pacing test |
| `mark.EscalatedFrom` | escalation CAS-create | never (a mark is replaced whole) | dies with the mark: pin-release, gap-close, TTL 2×lease, orphan/corrupt deletes | `legOf` |

**Never-written row:** an old-shape document `{count:N}` reads `Reclaims = 0` (backoff = base, as a first
reclaim today), `Leg = ""`, `EscalatedAt = ""` (fire now). An old-shape escalation mark reads
`EscalatedFrom = ""` and `Action = "directOp"` ⇒ `legOf` falls to `count.Leg` = `""` ⇒ **no release** — the
one live instance is re-armed by hand (§8); after the build every new escalation carries its leg.

**Two clocks, both named:** the re-fire test compares wall-clock to `EscalatedAt` written by this engine — the
same posture `ClaimedAt` already has for reclaim pacing (`reconciler.go:1019`). Skew between engine instances
is bounded by the lease and only delays a re-fire.

---

## 6. Contract surface — builds to, no change

| Sentence (verbatim) | Where | This design |
|---|---|---|
| *"the pin releases once the leg's declared `effects` all hold in the current row … a reclaim re-dispatches the pinned leg while incomplete and re-plans only past a completed leg — level-triggered advance"* | `10-orchestration-weaver.md:316-322` | Inc 2 makes it true after an escalation — builds to |
| *"Pin-release is the pinned leg's `__effect` close-credit and resets the gap's dispatch count (per-leg budget semantics)"* | `:322-324` | Inc 2 — builds to |
| *"Mark clears on gap-close, planned-leg completion (the pinned leg's declared `effects` all hold in the current row), or lease expiry"* | `:249-251` | Inc 2 — builds to |
| *"an integer `maxretries_<g>` cap (the retry budget …)"* | `:63-66` | Inc 1 — a re-claim that mounts nothing is not a retry; builds to |
| *"every `violating` row is eventually discharged … excluded … or escalated (budget exhaustion → `surface`/Augur: a human now owns it)"* | `:258-266` | Unchanged. An open `assignTask` is a human owning it; the stale-task targets (`staleAssignedTasks`, `staleUserTasks`) are the platform's answer to an ignored one, not the retry budget |
| *"`…__count` — the per-gap dispatch counter bounded by `maxretries_<g>`"* | `10-orchestration-substrate.md:184` | A mechanism sentence naming a private key; a pure refactor would falsify it. Not amended. If Andrew's next contract pass wants the word, *"attempt counter"* is the text of record |
| The Augur shard | `10-orchestration-augur.md` | Names no re-fire cadence; Inc 3 is mechanism |

---

## 7. Reconciliation with the existing mental model

**Didn't we already handle this?** Twice, each half. Pin-release at effects-hold shipped with the goal-mode
fire (`releaseCompletedLeg`, 2026-09-02) — it resolves the leg from the mark, and the escalation replaces the
mark. Attempt-vs-reclaim booking shipped for the `__effect` window (`5b58f667`) — and exempted the count on a
reading that priced one of its two readers. Neither is a new mechanism; both are the existing rule applied to
the case it missed.

**Does this add state we already keep?** No new keys. `Reclaims` is the count the backoff was already reading
under the wrong name; `Leg` is the mark's `Action` made durable past the mark; `EscalatedAt` is `ClaimedAt` for
an episode whose mark can TTL away; `EscalatedFrom` is the pin the escalation was erasing.

**Does it contradict the design of record?** It contradicts the `5b58f667` comment and restores the
mechanism-B ruling above it. The comment goes; git blame keeps the history (CLAUDE.md).

**Why not have the lens reset the budget?** Ruled out when the budget was placed: a reset-on-success is not a
lens predicate (`async-reply-increment3-design.md` §"Why B").

---

## 8. Migration, the live instance, and compatibility

- **Documents:** additive JSON fields on two private `weaver-state` shapes; every reader tolerates absence
  (§5 never-written row). Loupe's decoders are tolerant (C4). No `verify-kernel` surface, no package version
  bump, no bootstrap change. A rolling binary cycle is enough.
- **The one live instance** (§2.3) cannot self-release: its mark carries no leg and its count no `Leg`.
  Stopgap, runnable today by an operator, recorded here for the fire's Phase 0 (`nats` with
  `deploy/nkeys/lattice.nk`, per `reference_read_a_readmodel_row_live`):
  ```sh
  nats --server=localhost:4222 --nkey=deploy/nkeys/lattice.nk kv del weaver-state \
    renewalComplete.QomdjY7hAGS6mHvN9d2j.missing_renewalComplete -f
  nats --server=localhost:4222 --nkey=deploy/nkeys/lattice.nk kv del weaver-state \
    renewalComplete.QomdjY7hAGS6mHvN9d2j.missing_renewalComplete.__count -f
  ./bin/lattice weaver replay-target renewalComplete     # re-delivers the row set; lane 1 plans [signRenewal]
  ./bin/lattice weaver reset-confidence renewalComplete   # retires the "directOp" pseudo-window + its mismatch
  ```
  The replay re-delivers a markless, countless, violating row; `dispatchGap` synthesizes `[signRenewal]` and
  assigns the tenant's task. Expected: one `SignRenewal` task on the renewal within a minute of the replay.
  `weaver-state` is Weaver-private operational state (not Core KV; P2 is untouched) and the two keys are
  exactly what pin-release would have deleted.
- **Rollback:** the old binary ignores the new fields; a document written with `Count` semantics under the new
  rule reads as a smaller count to the old gate (safe side: dispatchable).

---

## 9. Alternatives

| # | Option | Verdict |
|---|---|---|
| 1 | **Delete the re-fire arm** — fire the escalation once; a lost claim is the bridge's / Contract #4's problem | Rejected, narrowly. The never-minted case (the op itself failed to publish or was rejected on a transient) has no other re-derivation, and the arm's cost under Inc 3 is one rejected op per day. Kept, paced. |
| 2 | **Delete the retry budget for collapse-only legs** (never count them at all; rely on the backoff) | This *is* Inc 1 for the count, but a human-leg budget still bounds task *creations* (a leg whose task is cancelled six times is genuinely stuck). Keep the gate; charge attempts. |
| 3 | **Demand-side: raise `maxretries_renewalComplete`** (6 → 100) | Rejected. The count keeps climbing on re-claims; the budget becomes a slower clock, not a bound on anything; and the goal target is the first of its class, not the last. |
| 4 | **Reset the budget on any row change** (event-triggered) | Rejected. A bgcheck lapsing changes the row without leg progress; the budget would be unbounded for exactly the runaway the bgcheck design just closed. The level test is the leg's effects, as ratified. |
| 5 | **Do not overwrite the leg mark; give the escalation its own key** (`…<gap>.__augur`) | Rejected. A second mark shape needs its own reclaim/TTL/orphan legs and a Loupe decoder; one field on the existing mark (`EscalatedFrom`) carries the same fact through every existing leg. |
| 6 | **Lengthen the escalation mark's lease to 24 h** | Rejected. `MarkLease` is one engine constant read by every reclaim; a per-mark lease is a new field on the same mark *plus* a TTL widening — strictly more than Inc 3's level test on the count document, and it still books the re-fire. |
| 7 | **Operator verb only** (no automatic release; teach `resetBudget` goal-mode) | Rejected as the whole; kept as Inc 4. The contract promises the release automatically, and the verb refuses the live case's `assignTask` leg for a correct reason (a fresh episode duplicates an open task). |
| 8 | **Have the lens project a pending-proposal column so the row shows the escalation** | Rejected. Couples every business lens to the augur package for a fact Weaver already holds in its own state; the re-fire is bounded by Inc 3 without a new column. |

Rejections 1 and 6 in combination: neither depends on the other's absence — 1 removes a recovery, 6 books
what Inc 3 stops booking.

**Dead-scaffolding test:** every increment has a live consumer today — the renewal (Inc 1–3) and the three
`exhausted` escalators (Inc 3); Inc 4 serves a goal gap exhausted on a non-human leg, which no shipped target
has, but it is a ~40-line hygiene change on a verb whose refusal is wrong today and it retires a standing
board-worthy residual rather than filing one (`feedback_resolve_residuals_dont_file_them`).

---

## 10. Test strategy

**Fixture rule (dossier):** every new vector runs once with `maxretries_<g>` projected and once *without* it
(the engine default is `directOp`-only, so an uncapped goal gap must never exhaust); every vector that
asserts a clear captures the latch's `since` and asserts it unchanged where the same pass re-raises. Target ids
in fixtures stay under 20 characters (the `…ID` NanoID lint).

| Increment | Test (owned) | Pins |
|---|---|---|
| 1 | `TestSweep_CollapseOnlyReclaim_AdvancesReclaimsNotCount` | five paced re-claims of an `assignTask` leg: `Reclaims`=5, `Count`=1, gate not exhausted |
| 1 | `TestSweep_ExternalReclaim_IsAnAttempt` | a `triggerLoom` external leg's reclaim advances both |
| 1 | `TestBackoffInterval_ReadsReclaims` | mutation: swap the exponent back to `Count` → reds (the widening `:1035` too) |
| 1 | `TestIncrementDispatchCount_LegChangeRestartsCount` | `{count:5,leg:A}` + attempt on `B` → `{count:1,leg:B}` |
| 1 | `TestResetRetryBudget_KeepsReclaims` | operator reset → `{count:0,reclaims:5}` |
| 2 | `TestHandleRow_ExhaustedLegWhoseEffectsHoldReleasesAndAdvances` (lane 1) | escalation mark with `escalatedFrom=setTerms`, row now carries `termsSetAt` → mark and count gone, `setTerms` close credited, `[signRenewal]` fired, `GapEscalatedToAugur` cleared |
| 2 | `TestSweep_CountLegReleasesEscalatedLegFromCountLeg` | no mark, `{count:6,leg:setTerms}`, effects hold → same outcome |
| 2 | `TestSweep_ReclaimReleasesEscalatedLeg` | stale escalation mark → release, not re-escalate |
| 2 | `TestEscalateExhaustedGap_EffectsNotHolding_StillEscalates` | the regression guard: `termsSetAt` null → escalation stands, count untouched |
| 2 | `TestEscalateExhaustedGap_OldShapeMark_DoesNotRelease` | `escalatedFrom` absent and `count.Leg` absent → no release (the §8 migration row) |
| 2 | `TestEscalateExhaustedGap_NoAugurPolicy_StillReleases` | the retire-above-guard ordering: a target with no augur block releases at the boundary |
| 3 | `TestEscalateExhaustedGap_BooksNeitherCountNorEffect` | after the fire: `Count` unchanged, no `__effect…directOp` key. Revert the `escalation` flag → reds |
| 3 | `TestEscalateExhaustedGap_RefireIsPacedByReclaims` | stale mark, `EscalatedAt` 10 min ago, `Reclaims`=2 (1 h) → Ack, no fire; at 61 min → fires, `Reclaims`=3 |
| 3 | `TestEscalateExhaustedGap_LiveEscalationIsNotRePaidAndADeadOneIsRetried` (existing, `replay_internal_test.go`) | kept: the live-mark Ack and the first re-fire; its "dead one is retried" leg now sets `EscalatedAt` old enough |
| 4 | `TestResetRetryBudget_GoalGap_DirectOpLegAccepted` / `…_AssignTaskLegRefused` | the verb and the arm agree, one vector each way, plus the existing static-gap control |
| e2e | `weaver_e2e_test.go`: a goal target with a human leg, the task left open past five reclaims, then the leg's effect written by hand → the next leg dispatches with no operator verb | the whole story, on embedded NATS (`natsfixture`) |

Existing pins that must stay green and are read for intent, not just run: `TestSweep_ReclaimBackoff_*`
(pacing unchanged), `TestGoalMode_FreshDispatchThenLegReleaseAdvances`,
`TestReclaim_ReleasesCompletedLegInsteadOfReclaiming`, `TestSweep_CountLeg*`, the four
`TestEscalateExhaustedGap_*`. Build-tagged harnesses reached: none add an interface method; run
`make test-control-plane-authz` regardless (the verb's signature is unchanged, the service interface is not).

---

## 11. Decomposition for the Steward

One fire, sequenced **after `fire/weaver-surface-workload` lands** (G14); Phase 0 re-runs C1–C6 and the touch
list against merged `main`, and runs the §8 stopgap first if the renewal is still unsigned.

| Inc | Scope | Posture-changing? | Size |
|---|---|---|---|
| 1 | `state.go` count document + `incrementDispatchCount`; `dispatchCountEntry` returns the document and `resetDispatchCount` read-modify-writes (the `retryBudgetStore` interface widens; `budgetStoreStub` follows); `reconciler.go` booking flag and backoff reads; `fireEpisode`'s stale-external branch books both; delete the `:1105-1112` comment | No (fewer exhaustions; nothing dispatches that did not) | S |
| 2 | `mark.EscalatedFrom`; `legOf`; `releaseCompletedLeg` resolves via it; the release + advance at the top of `escalateExhaustedGap` | **Yes** — a parked gap dispatches its next leg | S–M |
| 3 | `fireEpisode(…, escalation)`; the pacing test + `EscalatedAt` write in the re-fire arm | No | S |
| 4 | `resolvedLegAction`; `reArmDeclines` + the count leg's arm (n) use it | No | S |

Increments 1 and 3 share the booking seam and land together; 2 is the payoff and the review-depth driver; 4
last. `docs/components/weaver.md` §"Dispatch suppression" (`:365-437`) and §"`resetBudget`" (`:963`) are
rewritten in the same fire to describe the count as attempts + reclaims and the verb's goal-mode acceptance.
Dossier candidates for the close pass: *a deliberate exemption ("deliberately NOT gated") is a claim about
every reader of the value, not the one the author had in mind — enumerate the readers (C4) before inheriting
it*; and *an escalation that replaces a pinned mark must carry the pin forward, or every level test on the pin
goes dark*.

---

## 12. Risks + residuals

- **The `refreshBgcheck` leg is external but is reclaimed as collapse-only.** `staleMark` receives the *gap's*
  `GapAction` (`Action == ""` for a goal gap), so `confirmedConcluded` is false and the `triggerLoom` leg's
  reclaim preserves its `claimId` — collapsing onto the terminal Loom instance as a no-op (`reconciler.go:966`,
  `:1053-1065`). Today that leg advances via the bridge reply writing `bgcheckValidUntil`, so the reclaim's
  no-op is harmless; it becomes a stuck leg only if a check is lost. **Out of scope** — the dossier's first
  entry, on the goal-mode seam; filed as a one-line residual on the board only if the Steward's Phase 0 finds a
  live instance (the census C1 shows none: the bgcheck runaway is closed).
- **Skew:** two engine instances with clocks apart by more than a lease could re-fire an escalation one
  backoff step early. Single-instance today (HA-NATS is shelved); the same exposure `ClaimedAt` pacing already
  carries.
- **The pending proposal is left pending.** Inc 2 re-plans the gap around it; the proposal's staleness is
  visible in the review surface (it names the gap and the candidate). A reviewer approving a stale proposal
  dispatches through `augurDispatch`'s re-validation as today. Declined in §1.1 [d].

## 13. Checklist walk (designer/SKILL.md §2.3), items that bit

- **A.5 / A.6 (a shipped refusal's reason; the function actually called):** the `5b58f667` comment's
  "bounds reclaim effort" named one reader of the count; C4 enumerated three. `reArmDeclines`' "the re-arm
  never runs a plan" is true of the arm and irrelevant to lane 1, the second reader of a zero.
- **B.5 (the clear you are replacing):** the escalation's stale-mark clear (`:1608-1636`) is the line that
  destroys the pin; Inc 2 keeps the clear and carries the pin through it rather than adding a second mark.
- **C.4 (run every census):** C1's answer — one collapse-only capped gap — was smaller than the row implied;
  the platform rule is justified by the class (C2 = the first goal target), and the doc says so.
- **D.6 (an exclusion is a coverage claim):** "a human now owns it" was checked against the stale-task
  targets, which exist and are loaded (`weaver.log`: `staleAssignedTasks`, `staleUserTasks`).
- **E.1 (lifetime at every boundary):** §5, including the never-written row and the operator-reset asymmetry
  (`Count` zeroed, `Reclaims` kept).
- **F.1 (a workaround that bends an invariant):** the "delete two keys" stopgap is priced as an operator
  action on private state, not a design mechanism, and the design removes the need for it.

## 14. Adversarial pass — run (2026-09-04, cold read-only reviewer against the code)

The reviewer traced every §1 claim to the deciding code and could not falsify the mechanism: lane 1 never
reaches `releaseCompletedLeg` for an exhausted goal gap and the sweep reaches it only on the escalation's
`"directOp"` mark (false); the backoff series and the unconditional count bump are as quoted; the re-fire is a
fresh episode booked twice; `resolveGoalAction` is pure and `rankCandidates` reads only `__effect`; the §8
stopgap traces through `ReplayTarget` → `handleRow` → `dispatchGap` to `[signRenewal]` with no blocker; no
import cycle, no build-tagged double on any touched interface. Five findings, all folded above:

| Sev | Finding | Folded into |
|---|---|---|
| MAJOR | `resetDispatchCount` marshals a fresh `{count:0}` and its interface returns an `int`, so "keeps `Reclaims`" needs the read to return the document (`state.go:400-403`, `:447-460`; `budgetStoreStub`) | §4.1, §11 Inc 1 |
| MAJOR | the `issueKeyGapEntity` clear enumeration missed the count leg's corrupt-body arm (`reconciler.go:606`) | §4.2, with the grep that closes the set |
| MINOR | `deleteDispatchCount` is a blind delete; a race drops the pacing fields — direction "fire sooner" | §4.1, §5 |
| MINOR | lane 1's own stale-external re-arm (`fireEpisode` `found && stale`) is a reclaim seam the `Reclaims` rule did not name | §4.1 |
| MINOR | Inc 4 runs `Synthesize` inside the verb's 5 s handler and twice per re-armed gap per pass | §4.4, priced |

Verdict as returned: *build-ready for the mechanism; the two MAJORs are same-fire scope, not re-design.*
