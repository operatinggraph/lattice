# Design — A sweep `Reproject` is not supersession-guarded, so its write can outlive the rule it ran under

**Status: 📐 DRAFT awaiting Andrew — authored 2026-08-06 (critical-necessity session, filed-by-steward row re-examined)**
**Author: Winston (Designer fire, 2026-08-06)**
**Backlog row:** `planning-artifacts/backlog/lattice.md` → *Component maintenance* → "[Refractor] A sweep pass spans a rule swap, so actors in one pass reproject under two rules".
**Verdict on the row as filed: KILL the framing, keep the coordinates.** The row was filed by the adversarial pass of `82c7972b` two minutes after that commit landed (`4a1abecb`, 2026-08-03 05:17:23 vs 05:15:21 — `git merge-base --is-ancestor 82c7972b 4a1abecb` confirms the order). Its stated defect — "earlier actors converged under the old rule and later ones under the new" — is **not a defect**: §2 shows the reload's own rebuild force-truncates every swept lens's rows *and their watermarks*, so a mixed-rule pass is destroyed rather than merely superseded. But re-grounding the row's own two line cites turned up a **different, real, security-relevant end state** at the same coordinates, which `82c7972b` closed on the CDC path and left open on the sweep path (§3). This design is that defect, not the row's.

---

## For Andrew (one-look ratification)

**What it does (two lines).** Extends the supersession guard `82c7972b` installed on the CDC write path (`writeResults` Naks when the rule generation moved under it) to the **`Reproject` write path**, which never got it — so a convergence-sweep write derived from a replaced rule is abandoned instead of landed. Second, makes the guarded adapter's *silent* stale-watermark drop visible to the reconciler, so a sweep that provably cannot repair a row stops reporting it as healed every pass.

**The one design decision (no fork).** *Where the check goes.* `Reproject` already takes a coherent rule snapshot (`reproject.go:133`) and threads it into evaluation — but then writes through its own loop, bypassing `writeResults` and therefore bypassing `supersededRule`. I put the check at the **top of `Reproject`'s write loop** (after evaluation, before any write), the exact placement and reasoning `writeResults` uses. That placement is what makes it sound: `publishRuleState`'s `ruleGen++` happens **synchronously** in `UseFullEngineBranches`, strictly before the `go func(){ Rebuild }` that truncates (`cmd/refractor/reload.go:346` then `:360`), so any Reproject write that *could* land after the purge necessarily observes a changed generation. One check closes the whole window; no per-actor rebuild polling, no new lifetime, no new state.

**Contract surface: NONE.** Contract #6 §6.2 / §6.14 guard semantics are untouched — this changes *who is allowed to write*, not what the guard does with a write. No auth-surface change: the fix strictly *withholds* writes (fail-closed), grants nothing, reveals nothing.

**Why it is worth a fire.** The frozen row is the **pre-edit permission set on the auth plane**, and it is self-concealing: the sweep logs `healed a divergent projection` and increments `healed` on every pass while the row never changes (§3.4). That is the same outcome `82c7972b`'s commit message calls "a MATCH edit made to revoke something, silently defeated" — the door it shut on the handler, standing open on the sweep. Size **S**; two increments, both small; every test fails without its fix.

---

## 1. Grounding ledger

Every claim below is a line on current `main` (`ff9f2cc4`), read this session. No claim rests on a comment alone where behavior was checkable.

| # | Fact | Cite |
|---|---|---|
| G1 | The sweep pass calls `Reproject` per actor in a serial loop over `sel.actors`. | `internal/refractor/pipeline/sweep.go:391` — `res, rerr := s.p.Reproject(ctx, actor)`. Row's cite is **exact, no drift.** |
| G2 | Each `Reproject` takes its **own** rule snapshot, so a pass genuinely can span two rules. | `internal/refractor/pipeline/reproject.go:133` — `rs := p.ruleState()`, doc: "Sweep, control-plane RPC and the retry queue all reach here off the consumer goroutine, so this entry point takes its own rule snapshot." Row's premise **verified.** |
| G3 | `Reproject` stamps the **consumer head**, not a message sequence, as its ordering token. | `reproject.go:129` — `seq := p.Progress().LastAppliedSeq`. |
| G4 | `Reproject` writes through its own loop (`adpt.Upsert` / `adpt.Delete`), **not** `writeResults`. | `reproject.go:~150-205`. |
| G5 | `Reproject` contains **zero** rule-generation references — it never consults `supersededRule`. | `grep -n "gen\|superseded" internal/refractor/pipeline/reproject.go` → no matches. |
| G6 | The CDC path **does** consult it, and Naks. | `pipeline.go:2028` in `writeResults`; `supersededRule` at `pipeline.go:2021`; `ruleGen` at `:173`, incremented under `ruleMu` at `:631`. |
| G7 | A MATCH hot-reload swaps the rule **synchronously**, then triggers `Rebuild` on a **new goroutine**. | `cmd/refractor/reload.go:346` (`UseFullEngineBranches` → `publishRuleState` → `ruleGen++`) then `:360` (`go func(){ entry.pipeline.Rebuild(rl.ctx, false) }`). |
| G8 | `Rebuild` **force-truncates** a guarded *and* truncatable target even when called with `truncate=false`. | `pipeline.go:1362-1390` — "guarded bucket forces truncate (avoids rejected-write holes)". |
| G9 | The truncate is scoped to the lens's own rows, so it is a safe unconditional force. | `projection/driver.go:~446` `ApplyTruncateScope(adpt, r)`; `natskv.go:506` `Truncate` lists under `a.keyPrefix`. |
| G10 | **The sweep is only ever installed on a `*NatsKVAdapter` target.** `sweepEnrolment` requires `adapter.PrefixKeyLister`, and `ListKeysPrefix` is implemented by `NatsKVAdapter` **and nothing else** — not `PostgresAdapter`, not `ProtectedAdapter` (wraps `inner` as a named field, does not promote it), not `GrantWriterAdapter`. | `projection/driver.go:309-324`; sole implementation `natskv.go:393`. |
| G11 | `NatsKVAdapter` **is** a `Truncater`. With G10 this means *every swept lens is truncatable*, so G8's force always fires on a guarded swept lens. | `natskv.go:481`, `var _ Truncater = (*NatsKVAdapter)(nil)` at `:18`. |
| G12 | The guard drops a write whose token is **equal to or lower than** the stored watermark, returning `nil`. | `natskv.go:293` — `if storedSeq, ok := storedProjectionSeq(entry.Value); ok && storedSeq >= incomingSeq { return nil }`. Postgres mirror: `ON CONFLICT … WHERE EXCLUDED.projection_seq > "<table>".projection_seq` (`postgres.go:120`). |
| G13 | An **absent** key takes the `Create` branch and lands **regardless** of token. This is why a truncate "clears the way" for a replay — and equally why a post-truncate stray write lands. | `natskv.go:283-291`. |
| G14 | The guarded upsert path **always** reports `Wrote: true`, "even through `guardedWrite`'s own internal no-op branches (a stale-or-equal stored seq, …)". | `natskv.go:158-171`; `UpsertOutcome.Wrote` doc, `adapter.go:117-124`. |
| G15 | `lastAppliedSeq` is set **unconditionally** per acked message, and is re-seeded from the durable's ack floor on registration (raise-only there). | `pipeline.go:372-374` (`recordAppliedSeq`), called at `:1569`; `seedAppliedSeqFromAckFloor` `:392-404`. |
| G16 | The sweep checks suppression (incl. `RebuildInFlight`) **once, at pass entry** — never per actor. So a pass already in flight keeps writing straight through a rebuild. | `sweep.go:362` in `pass()`; `RebuildInFlight` at `pipeline.go:1169`. |
| G17 | The sweep already has an "abandon the whole pass" disposition for a per-pipeline condition, with precedent prose. | `sweep.go:392-402` (`ErrNoOrderingToken` → warn, `s.record(ctx, healed, rerr)`, `return`). |
| G18 | `82c7972b`'s own reasoning names the sweep as the *healer* of this hazard class — it did not consider that the sweep shares the hazard. | `pipeline.go:1997-2020` `supersededRule` doc: "The convergence sweep heals it eventually, but only for a lens that got a sweep plan … so it is not a bound worth relying on." |

---

## 2. Why the row as filed is dead

The row's worry is that one pass mixes two rules, leaving "earlier actors converged under the old rule and later ones under the new". Walk what actually happens to that mixed output.

A MATCH edit reaches `reload.go`'s `MatchChange` arm, which swaps the rule and then rebuilds (G7). By G10+G11, **every** sweep-enrolled lens writes through `NatsKVAdapter`, which is truncatable; so by G8 the rebuild **force-truncates** that lens's rows — data *and* `projectionSeq` watermarks — scoped to its own key prefix (G9), then resets the durable to replay the whole Core-KV corpus under the new rule. Post-truncate every key is absent, so by G13 the replay's re-derivations all land unconditionally.

The mixed-rule rows are therefore not "eventually superseded" — they are **deleted and re-derived**, watermark included. The row's own hedge ("the next pass re-runs") understates its own dismissal: it is not the next sweep pass that erases the mixed output, it is the rebuild the same reload already kicked off, and it erases it by truncation, so not even a watermark survives to obstruct the correction. On an unguarded swept lens there are no watermarks at all and the replay is last-writer-wins — same result.

And the pass boundary is irrelevant in the other direction too. A rule swap landing *between* two passes produces exactly the same population of old-rule rows as one landing *mid*-pass. Nothing in the outcome is a function of "two rules in one pass", so **granularity is the wrong unit of analysis** and there is no design to do on it. Killed.

*(For completeness: the one target class where a rebuild leaves live rows alone is a guarded **un**-truncatable one — the grant family, which "deliberately does NOT implement `Truncater`: `actor_read_grants` is shared" (`read_path_adapters.go:14`). By G10 that family is **never swept** — it is not a `PrefixKeyLister` — so no sweep pass can straddle a rule swap there. The two halves are disjoint, which is what makes §2's conclusion airtight rather than merely typical.)*

---

## 3. The defect that is actually there

### 3.1 The gap

`82c7972b` made the CDC path refuse to write results derived from a replaced rule (G6), because — in its own words — "the truncate runs on the reload's own goroutine while this handler is still mid-flight … so an in-flight evaluation can land its stale row AFTER the purge and then swallow its own correction."

`Reproject` is the *other* write path into the same target. It takes the same coherent snapshot (G2) and threads it into evaluation, but then writes through its own loop (G4) and **never consults `supersededRule`** (G5). Three callers reach it — the sweep, the control-plane `reproject` RPC, and the pipeline's own actor-retry queue — and none of them is guarded.

### 3.2 The sequence that freezes a row

1. A sweep pass is mid-loop over a large actor set (G1, G16 — no per-actor re-check, so the pass runs to completion regardless of what the reload does).
2. A MATCH edit arrives. `ruleGen++` publishes the new rule synchronously; the rebuild goroutine starts (G7).
3. The rebuild force-truncates the lens's rows and watermarks (G8, G9).
4. The still-running pass calls `Reproject` for actor **A**. Its snapshot is the **old** rule. Nothing stops it (G5). It evaluates A under the old rule and writes — into a now-empty target, so the `Create` branch lands unconditionally (G13) — at token `LastAppliedSeq` = **H**, the consumer head (G3).
5. The replay reaches A's events. Each carries its own stream sequence, all **≤ H**. The guard sees `storedSeq(H) >= incomingSeq` and drops every one as an idempotent no-op (G12).

A now holds an **old-rule row at watermark H**, and the rebuild — the mechanism whose entire purpose was to re-derive the corpus under the new rule — has been locked out of that key by a write it was racing.

This is strictly worse than the handler-path hazard `82c7972b` fixed. There, the stale write carried its own `msg.Sequence`, so it blocked only the replay of *that one message*. Here the token is the **head**, which is ≥ every replayed sequence — so one stray sweep write blocks the replay of **all** of that actor's history.

### 3.3 Why nothing erases it

- **The rebuild**: locked out, per §3.2 step 5.
- **The next sweep pass**: the sweep is suppressed for the rebuild's duration (G16), then resumes. By G15 `lastAppliedSeq` is back at H once the replay drains (the replay re-applies the same sequences; the ack-floor seed is raise-only). So the corrective write is *also* at H, and `>=` drops it too (G12). Every subsequent pass repeats this forever.
- **`82c7972b`**: covers `writeResults` only (G5, G6).

The freeze lifts only when a **new** Core-KV event with sequence > H, *matching this lens's filter*, reprojects A through the CDC path. On a narrowed auth lens — and narrowing the filter is precisely what the recent Refractor work achieved — matching events are sparse by construction, so the window is unbounded in the same sense the row's neighbours are: not "forever", but "until unrelated traffic happens to arrive".

If the MATCH edit was a narrowing or a revocation, the frozen row **is the pre-edit permission set**. That is the exact over-grant `82c7972b` set out to prevent.

### 3.4 Why it is invisible

`Reproject` sets `out.Wrote = true` after `adpt.Upsert` returns `nil`. By G14 the guarded path returns `nil` *and* reports `Wrote: true` even when `guardedWrite` dropped the write as stale-or-equal. So the sweep increments `healed` and logs `pipeline: sweep: healed a divergent projection` (`sweep.go:~418`) on **every pass**, for a row it provably cannot touch.

This blindness is not novel — it is already named in the codebase, and only half-closed. `SeqGuarded`'s doc (`adapter.go:64-70`) says it "exists for the one caller that must decide BEFORE writing — reconciliation, which reports back whether it healed anything … without this a reconciler cannot tell that silence apart from a write that landed." `Reproject` uses that interface to fail closed on the `seq == 0` case (`ErrNoOrderingToken`). The `storedSeq >= incomingSeq` case is the *same* silence, and no caller can see it.

A permanently-unrepairable auth row that reports itself healed once a minute is worse than one that reports nothing: it consumes the liveness signal that would otherwise expose it.

---

## 4. Necessity

Held against the standard tests:

- **Is there a converged-but-wrong end state?** Yes — §3.2. A row whose stored content is derived from a retired rule, at a watermark that rejects every corrective write.
- **Does an existing mechanism erase it?** No — §3.3 walks the rebuild, the next sweep pass, and `82c7972b` individually. All three are defeated by the same `>=`.
- **Is the trigger plausible?** Yes, and it is *the row's own named consumer*: "a MATCH edit landing during a sweep of a large actor set." A large actor set is exactly what keeps the pass in flight long enough to still be writing after the truncate. Package-driven MATCH edits are the normal way a lens changes (`reload.go`'s `MatchChange` arm exists for them), and the sweep runs on a 60s default interval across every actor-aggregate lens, auth-plane and business alike (`driver.go:415-441`).
- **Is the consumer a product surface, not a scenario?** Yes — the auth plane's grant projections, and the revocation semantics the platform promises.
- **Is it already someone else's design?** No. `lens-trigger-relation-narrowing-design.md` §8/§8.1 records `82c7972b` and scopes itself to the CDC path. Nothing in the corpus covers `Reproject`'s write disposition. It is not a fold.

Necessity **met** — on §3's mechanism, not on the row's.

---

## 5. Alternatives considered

**A. Do nothing.** Defensible only if the frozen row self-heals. It does not (§3.3), and the sweep actively misreports it as healed (§3.4). Do-nothing also leaves the asymmetry as a trap: the next contributor reading `supersededRule`'s doc will reasonably conclude the hazard is closed, because it says so for the path it guards. **Rejected.**

**B. Re-check `RebuildInFlight` per actor in the sweep loop.** Cheap and local, but wrong on both edges. It over-fires (a rebuild from an operator RPC involves no rule swap, so a pass that suspends for it abandons useful work for no reason) and under-fires (it closes the truncate window only, not the general "wrote under a retired rule" case, and it leaves the control-plane RPC and the retry queue — the other two `Reproject` callers — unguarded). It also treats a symptom of the reload sequence rather than the invariant. **Rejected**, though it would have been the tempting one-liner.

**C. Have the rebuild wait for the in-flight sweep pass to drain.** Introduces a new cross-goroutine lifetime (who resets it, who carries it, what happens if the pass errors mid-way) and makes the reload path block on an unbounded loop over a large actor set. A new lifetime is exactly the cost the snapshot design avoided. **Rejected.**

**D. Give `Reproject` a fresh snapshot per actor instead of per call.** This is what the row's framing would suggest — make the *pass* rule-coherent. It does not touch the defect: a snapshot taken any time before the swap still writes after the truncate. It would also *undo* `82c7972b`'s coherence guarantee by reintroducing multiple snapshots inside one operation, the precise incoherence `ruleState`'s doc forbids ("two snapshots in one operation can straddle a hot-reload"). **Rejected — and this is the concrete reason the row's framing had to be replaced rather than narrowed.**

**E. Chosen — mirror the precedent.** Check `supersededRule(rs)` once, at the top of `Reproject`'s write loop, and refuse. Soundness rests on G7's ordering: `ruleGen++` completes synchronously before the truncating goroutine exists, so `gen != rs.gen` is *guaranteed* true for any write that could reach the target after the purge. No new state, no new lifetime, no polling, one comparison under an existing lock. Plus Inc 2 to make the guard's silence audible, so a recurrence of this class cannot hide behind a green counter again.

---

## 6. Increments

### Inc 1 — `Reproject` refuses to write under a superseded rule *(the correctness fix)*

- Add `ErrRuleSuperseded` beside `ErrNotActorAggregate` / `ErrNoOrderingToken` in `internal/refractor/pipeline/reproject.go`.
- In `Reproject`, after `reprojectActors` returns and **before** the write loop, `if p.supersededRule(rs) { return Reprojection{}, ErrRuleSuperseded }`. Same placement and rationale as `writeResults` (`pipeline.go:2028`), cross-referenced in the comment so the two paths read as one policy.
- Per-caller disposition:
  - **Sweep** (`sweep.go:391`): abandon the whole pass, mirroring the `ErrNoOrderingToken` arm (G17) — the condition is per-pipeline, not per-actor, so continuing would repeat the refusal for every remaining actor. Log at Info (this is an expected consequence of a reload, not a fault), `s.record(ctx, healed, nil)`, return. **Not** `noteActorFailure`: no actor is at fault, and charging one a consecutive-failure strike would push it into `backoffPasses` and delay the genuine post-rebuild heal.
  - **Control-plane RPC** (`control/service.go`): return the error. An operator's reproject that a concurrent rule edit invalidated must say so; the rebuild the edit triggered is about to re-derive the row anyway.
  - **Retry queue** (`enqueueActorReprojectRetry`): treat as transient and re-enqueue — the next attempt runs under the settled rule. Must not be terminal: the actor's original repair is still owed.
- Order-of-operations note for the builder: the check must sit **after** evaluation, not before. Checking early would leave the window open for a swap landing *during* evaluation, which is the longer interval.

### Inc 2 — the guarded drop becomes visible to the reconciler *(the detector)*

- Extend the guarded write to report whether the CAS **committed** or was **dropped against an equal-or-fresher watermark** — the branch at `natskv.go:293`. Shape: give `guardedWrite` an outcome return and surface it through `upsert`, so `UpsertOutcome` can distinguish *committed* from *declined by watermark*. `natskv.go:158-171`'s reasoning stays intact: the guarded path must still **attempt** the write on every call regardless of row content; this changes only what it *reports*, never whether it tries.
- `Reprojection` gains a field distinguishing "wrote" from "**blocked** — a stored watermark ≥ my token". `Reproject` stops setting `out.Wrote = true` unconditionally.
- The sweep counts a blocked actor separately from a healed one and reports it in its status. A row the sweep cannot repair is the definition of unrepairable, and the sweep's own doc holds that "a sweep the lens cannot support is not a degraded sweep — it is one that faults every tick, reporting a lens that is unrepairable rather than one that is simply not swept" (`driver.go:427-433`). The same posture applies per row.
- Keep this an **observability** change, not a new remediation. What to *do* about a blocked row (bump the token? refuse? escalate?) is a separate question with a real fork in it, and Inc 1 removes the mechanism that manufactures blocked rows in the first place. Deliberately out of scope; if Inc 2 shows blocked rows arising from some *other* cause, that is a genuine new finding and gets its own row with evidence attached.

**Build order matters.** Inc 1 alone fixes the defect. Inc 2 alone would only make it visible. Inc 1 first; Inc 2 in the same fire (it is small, and shipping the fix without the detector is what let this class hide for three days).

---

## 7. Verification

Colocated in `internal/refractor/pipeline`, mirroring `rule_swap_race_internal_test.go` — the file `82c7972b` added for the CDC half. These are its sweep-path siblings and belong beside it. **Every test must be proven to fail without its fix** (state the observed failure in the build note, per house practice).

1. **`TestReproject_RefusesWriteUnderSupersededRule`** — install an actor-aggregate lens; enter `Reproject` with a snapshot; publish a new rule via `UseFullEngineBranches` before the write loop runs; assert `ErrRuleSuperseded` and that the target received **no** write. Without Inc 1: the old-rule row lands.
2. **`TestSweepPass_AbandonsOnRuleSwap`** — a pass over several actors with a rule swap injected mid-loop. Assert the pass returns early, records no actor failure (no `backoffPasses` strike), and that no actor was written under the retired rule. Deterministic sync only — a channel the fake adapter signals on write, never `time.Sleep`.
3. **`TestGuardedWrite_PostTruncateStrayWriteDoesNotFreezeReplay`** — the end-to-end residual, and the one that proves the *outcome* rather than the mechanism. Truncate a guarded target, land a stray head-token write, then replay a lower-sequence event and assert the row **ends up matching the new rule**. Without Inc 1 the row stays at the stray write's content, frozen at watermark H — which is the defect, stated as an outcome.
4. **`TestReprojection_ReportsBlockedNotHealed`** (Inc 2) — a stored watermark ≥ the reconciler's token; assert `Reprojection` reports *blocked*, not `Wrote`, and that the sweep counts it as unrepaired. Without Inc 2 it reports a heal that never happened.
5. **Negative-control discipline.** Test 3 can pass for the wrong reason if the fake adapter's guard is not actually enforcing `>=`. Assert the positive vector first — that the stray write *does* freeze the replay with Inc 1 reverted — so a green result cannot come from an inert guard.

Gates: `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`, all `scripts/lint-*.go` gates, and `go test ./internal/refractor/...`. Wide-blast-radius note: Inc 2 touches `UpsertOutcome`, which `writeResults` consumes for its write-audit skip — run the **full** `go test ./...`, not just the Refractor packages, and confirm the audit behavior for an unchanged row is unaltered.

---

## 8. Non-goals

- **The row's granularity question.** Closed as a non-defect in §2, not carried forward. If this design is ratified, the board row should be **replaced** by it, not linked from it — the row's text describes a mechanism that does not misbehave, and leaving it standing would re-file the same dead question at the next survey.
- **Rule coherence *across* a pass.** Alternative D — explicitly rejected; it would regress `82c7972b`'s per-operation coherence.
- **What to do about a blocked row** beyond reporting it (Inc 2's boundary).
- **The un-truncatable guarded target** (the grant family): a real and already-documented property of `Rebuild` (`pipeline.go:1370-1386` states it at length), untouched here, and by G10 never swept — so out of this design's blast radius entirely.
- **Contract #6 §6.2 / §6.14 guard semantics.** Unchanged. `>=`-drops is correct; the bug is that a retired rule's write was allowed to establish the watermark being honored.
