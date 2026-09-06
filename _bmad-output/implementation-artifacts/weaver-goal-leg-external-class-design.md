# Weaver — a goal gap's external leg is classified by the leg it resolves to, not the playbook entry

**State: `✅ RATIFIED (Winston-adjudicated, per the 2026-08-20 delegation)` — 2026-09-05, after the §13 adversarial pass.**
**Board row:** `[Weaver] A goal-mode gap's dispatch shape never reaches externalDispatchGap` (lattice.md, Component
maintenance). **Filed:** Surveyor, 2026-09-05 (`9b1c532c`). **Designer:** Winston, 2026-09-05, grounded at `259aa629`.
**Size:** S–M · **Imp:** ★★ · **Owner after ratification:** the Lattice Steward, one fire, three increments (§11).

## 0. For Andrew — one look

**What it does.** `staleMark` — the predicate that decides whether a gap's expired episode is a *concluded external
call* (reclaim mints a fresh `claimId`, a fresh vendor call / Loom instance, bounded by `maxretries_<g>`) or a
*possibly-open human artefact* (reclaim preserves the `claimId` and collapses) — reads the playbook entry's
`Action`, which is `""` for every goal-mode gap. So a goal leg that dispatches a `triggerLoom` over an
externalTask-only pattern (lease-signing's `refreshBgcheck`) is classified "never makes an external call" at all
four consumers. The design makes `staleMark` classify the **resolved leg** (the same pure resolution the reclaim
already performs one line later for `collapseOnlyReclaim`), extends the shipped lint gate to cover it, and has the
one planned-mode lens in the corpus declare the `inflight_<g>` companion its external leg needs — **scoped to the
leg** (true only while the check is what the chain is waiting on, §3.5), because the suppression gate reads the
column before any leg is chosen — without which the engine fix is dead scaffolding (§8, row 3).

**Fork check — none.** No Gateway / read-path auth / Vault / multi-cell / HA-NATS surface; no new state; no new
key family; no new contract vocabulary. The one decision a prior design parked as "a claimId decision the design
did not take" (`weaver-exhausted-gap-leg-boundary-design.md` build note) is already taken by the frozen contract:
§10.3 says an External gap's reclaim "is intended — a genuinely new attempt (a new vendor call)", and names
"`triggerLoom` of an externalTask-only pattern" as External with the class "read from the pattern's own step
kinds — never from the playbook action name". A goal leg dispatching exactly that shape is that gap. §5.

**Contract check — builds to §10.3 and §10.8, no edit.** The runtime starts keeping a promise the contract already
makes for a shape (a goal leg) it did not keep it for. §5 quotes both sentences, including the §10.8 "unchanged
within a leg" clause the reviewer asked to see adjudicated: a stale reclaim keeps the pin (no re-plan) and applies
§10.3's class-dependent rule, which is what "unchanged" refers to.

**Payoff, honestly sized (§1.2):** a check that *concluded without success*, or one the adapter never accepted,
is retried on the renewal leg (bounded by the leg's budget of 6) instead of collapsing forever onto the failed
instance. A call lost *after* acceptance stays in flight for good — the platform's deliberate presence-based
posture for the static bgcheck gap too (`weaver.md:470-474`; `patterns.go:16-17` "the bridge wait is unbounded") —
and this design neither fixes nor worsens that.

**Therefore:** `✅ RATIFIED (Winston-adjudicated, per the 2026-08-20 delegation)`, §13.

---

## 1. Problem + intent

### 1.1 The row, verbatim (Surveyor, `9b1c532c`)

> A goal-mode gap's dispatch shape never reaches externalDispatchGap. The classifier switches on ga.Action, which
> is "" for every goal/planned-mode gap, so it falls to the default arm and staleMark can never return true for one.
> Three legs read that verdict, not the one the exhausted-gap-leg design S12 priced: the stale-reconcile itself, the
> sweep's reclaim pacing (reconciler.go:1071), and — the one no design enumerated — reset-budget's refusal
> (control.go:630), which therefore refuses every goal gap resolving to triggerLoom/assignTask/proposedOp
> permanently, with a message asserting the artifact "may still be open". renewalComplete is a shipped target whose
> refreshBgcheck leg is external, so this is live, not latent; the design's own latency argument (no inflight_<g>
> declared) covers only the staleMark half. […] Precedent for two of the three call sites is resolvedLegAction,
> already called a line above each; lane-1's site (evaluator.go:481) precedes plan resolution, which is the part with
> no ratified pattern.

Split at each claim, with the grounding verdict (§2 carries the code):

| Claim | Verdict |
|---|---|
| the classifier switches on `ga.Action`, `""` for a goal gap, default arm | **True.** `evaluator.go:616-647`; the goal gap's playbook entry has `Action == ""` by construction (`resolvePlannedAction`'s guard, `strategist.go:430`). |
| three legs read the verdict | **Four.** C1 finds four call sites: lane-1's stale gate (`evaluator.go:481`), the sweep's markless re-arm (`reconciler.go:785`), the sweep's reclaim (`reconciler.go:1071`), and `reset-budget`'s refusal (`control.go:630`). The row missed the re-arm arm, which is the sibling of the verb it did name. |
| `reset-budget` refuses every goal gap resolving to `triggerLoom`/`assignTask`/`proposedOp` permanently | **True as stated, and half of it is correct behaviour.** For a leg that resolves to `assignTask` (three of `renewalComplete`'s four legs) the refusal is exactly right — a human task may be open. The wrong verdict is confined to a leg that resolves to an *external* shape, and only while `inflight_<g>` reads false. |
| "this is live, not latent" | **The misclassification is live; the harm is latent, and the fix is inert without a package change.** `renewalComplete` declares `maxretries_renewalComplete` and **no `inflight_renewalComplete`** (C5 = 0). `staleMark` returns false at its *first* guard (`row[inflight_<g>]` undeclared, `evaluator.go:578`) before the classifier runs, so for the one consumer the classifier's verdict is never consulted today. Resolving the leg changes nothing for that consumer until the lens declares the column. That is why §11 makes the package increment mandatory, not a follow-on. |
| lane-1 "precedes plan resolution, the part with no ratified pattern" | **Half true.** Lane-1's stale gate is only consulted when a mark is `found` (`:481`), and a found mark carries `rec.Action` — the pinned leg's ref. `resolvePlannedAction(…, pinnedAction)`'s pinned branch is a pure catalog lookup (`strategist.go:561-566`), the exact resolution the reclaim performs at `reconciler.go:1094` for the same mark. The ratified pattern exists; it is the reclaim's own. What lane-1 lacks is the *call*, not the pattern. |

### 1.2 Why it matters — the payoff, in the state the column can separate

`staleMark` is the only path by which an expired episode of an external call gets a **fresh** `claimId`. Every
Loom instance id is claimId-seeded (`deriveStableInstanceID`, `actuator.go:174`), so a reclaim that preserves the
`claimId` re-dispatches onto the terminal instance as a no-op.

What `inflight_<g>` can and cannot tell apart decides what the fresh mint buys. The corpus's in-flight fact is
presence-based — `.dispatch.data.vendorRef <> null AND .outcome.data.status = null` (`lenses.go:909`), chosen so
"a stuck-pending call is never double-dispatched against the vendor" (`weaver.md:470-474`), and the
`backgroundCheck` pattern's deadline disarms at the instance-op commit, "the bridge wait is unbounded"
(`patterns.go:16-17`). So the states are:

| Check state | `inflight_<g>` | `bgcheckValidUntil` | goal leg today | goal leg after |
|---|---|---|---|---|
| concluded, **not** usable (`outcome.status` ≠ `completed`, or completed but lapsed) | false | null | mark reclaims with the **same** `claimId` ⇒ collapses onto the failed instance forever; budget never spends | **fresh instance**, an attempt booked, six then `GapBudgetExhausted` |
| adapter never accepted (`.dispatch` never landed) | false | null | same collapse | same fresh retry |
| accepted, vendor never replied (**lost after accept**) | **true, permanently** | null | stuck on the leg (collapse) | stuck on the leg (suppressed) — **identical harm**, by the platform's posture, not this design's |
| in flight, healthy | true | null | suppressed / collapse | identical |

The first two rows are the payoff and they are real: the static `missing_bgcheck` gap on `leaseApplicationComplete`
retries exactly those (the dossier's first entry was minted when it did not). The third row is a limit of the
presence-based column that a Loom step deadline or a bridge-written failure outcome would lift for every consumer
at once; it is named here so nobody reads this design as a lost-call fix. `reset-budget`'s wrong refusal (row 7 of
§3.3) is the operator-facing half of the same first two rows.

## 2. Grounding — the mechanism, in code

| Piece | Where | What it does today |
|---|---|---|
| `staleMark(targetID, entityID, row, col, ga)` | `evaluator.go:573-589` | guard 1: `inflight_<g>` undeclared ⇒ false; guard 2: `externalDispatchGap(ga, row)` not external ⇒ false (Debug log); else `!inflight_<g>` |
| `externalDispatchGap(ga, row)` | `evaluator.go:616-647` | `directOp`/`proposedOp` ⇒ external; `triggerLoom` ⇒ resolve `ga.Pattern`, ask the registry's step-kind probe (`patternIsExternalEligible`, `registry.go:1380`); default (incl. `""`) ⇒ "never makes an external call" |
| lane-1 stale gate | `evaluator.go:481` | `stale := found && !leaseLive && staleMark(…, ga)`; `stale` reaches `fireEpisode` (`:903`), which mints a fresh `claimId` and CAS-replaces the mark |
| sweep reclaim | `reconciler.go:1071-1097` | `confirmedConcluded := staleMark(…, ga)`; **then** `dispatchAction` is resolved via `resolvePlannedAction(…, rec.Action)` and fed to `collapseOnlyReclaim` — the resolution `staleMark` needs is already computed, one call later |
| sweep markless re-arm | `reconciler.go:779-785` | `resolvedAction, resolvedRef, perr := resolvedLegAction(…)`; `collapseOnlyReclaim(resolvedAction, staleMark(…, ga))` |
| `reset-budget` (`reArmDeclines`) | `control.go:625-638` | same pair; the `ga.Action == ""` wording branch at `:631` already knows it is a goal gap |
| `resolvedLegAction` | `strategist.go:492-500` | `resolvePlannedAction(…, "")` and returns `(resolved.Action, actionRef, perr)` — the resolved `GapAction` is computed and **dropped** |
| `resolvePlannedAction` | `strategist.go:430-468` | non-planned or `Action != ""` ⇒ `ga` unchanged; candidates ⇒ pinned lookup or rank; goal ⇒ `resolveGoalAction` |
| `resolveGoalAction` | `strategist.go:560-620` | pinned ⇒ catalog lookup (pure); pinned-not-in-catalog ⇒ `planError{unplannable}` (an escalation's `"directOp"` mark, or a re-authored catalog); fresh ⇒ `Synthesize` |
| the resolved leg's shape | `catalogEntryGapAction`, `strategist.go:526-540` | carries `Action` **and** `Pattern` — everything `externalDispatchGap` reads |
| the lint gate | `scripts/lint-weaver-classify-by-shape.go` (CI STRICT, `ci.yml:373`) | gates `collapseOnlyReclaim`'s first argument only; its header parks `externalDispatchGap` "with a designer row" — this is that row |
| the one planned-mode target | `packages/lease-signing/renewal_targets.go:138` | `renewalComplete`, goal mode, catalog: `refreshBgcheck` (`triggerLoom` · `backgroundCheck`), `verifyGuarantor` / `setTerms` / `signRenewal` (`assignTask`) |
| its lens | `renewal_lenses.go:68-73, 269-303` | body columns include `maxretries_renewalComplete` (= 6), **no** `inflight_renewalComplete`; the walk already reaches `(id)<-[:providedTo]-(inst:service)` and reads `inst.outcome` — the same fan `leaseApplicationComplete` computes `bgInflight` over (`lenses.go:909`) |

**A comment this design corrects in the same fire.** `externalDispatchGap`'s doc (`evaluator.go:606-611`) says the
transient unindexed-pattern case is reachable from the sweep but "Lane-1 does not: planGap runs first there and
defers the gap on an unresolvable pattern before dispatchGap ever consults staleMark." C9: in `dispatchGap`,
`staleMark` is called at `:481` and `planGap` at `:517` — lane-1 consults the classifier *before* planning. The
consequence is benign (an unknown pattern reads not-external ⇒ the mark is treated as live ⇒ the delivery is Ack'd
under the anti-storm drop, and the sweep reclaims later) but the sentence is false and a reviewer would inherit it.
Inc 1 rewrites it.

**Permission envelope.** No new NATS verb, subject or bucket; the only KV traffic is what the four sites already
perform. Nothing for `natsperm` to see.

## 3. The shape

### 3.1 Rule

**A gap's external class is decided by the dispatch it resolves to — the leg — never by the playbook entry.**
`staleMark` takes the resolved leg's `GapAction`; each of the four consumers hands it the leg it has already
resolved (or resolves it, once, by the pure pinned lookup); a leg that cannot be resolved for this row confers no
stale-reconcile authority. For every gap that names its own `Action`, `resolvePlannedAction` returns the entry
unchanged, so static targets are byte-identical — the suite invariant the planner-mandate design established
("mode-absent targets byte-identical").

### 3.2 The engine edit (Inc 1)

1. **`staleMark(targetID, entityID string, row map[string]any, col string, leg GapAction) bool`** — same body; the
   parameter is renamed to say what it must be. `externalDispatchGap(leg, row)` unchanged in body except one new
   arm: `case "": return false, false, "its plan resolves to no dispatch for this row, so nothing here can have
   concluded"` — the zero `GapAction` is what an unresolvable pin passes (§3.3), and the default arm's wording
   ("its playbook action \"\" never makes an external call") would misdescribe it.

2. **`resolvedLegAction` returns the leg:** `(leg GapAction, ref string, perr *planError)`. Its two callers read
   `resolvedAction := leg.Action` into the local the existing gate requires for `collapseOnlyReclaim`, and pass `leg`
   to `staleMark`. No third return value: the action is a field of the leg.

3. **The four sites** (the shape is prescribed so the extended gate, §3.4, reads it):

   - `evaluator.go:481` (lane-1, mark found): before the stale gate,
     ```go
     leg := GapAction{}
     if pinnedAction != "" {
         if resolved, _, perr := e.resolvePlannedAction(ctx, target, targetID, entityID, col, ga, row, pinnedAction); perr == nil {
             leg = resolved
         }
     }
     stale := found && !leaseLive(rec.LeaseExpiresAt, time.Now()) && e.staleMark(targetID, entityID, row, col, leg)
     ```
     `pinnedAction` is `rec.Action` (`:426`), `""` when no mark was found *or* the mark records no action; the
     guard is on the pin, not on `found`, because `resolvePlannedAction(…, "")` routes a goal gap to
     `resolveGoalAction`'s fresh branch and `Synthesize` (`strategist.go:561`, `:596`) — the reviewer's catch. A
     mark with an empty action gets the zero leg ⇒ no stale-reconcile authority (row 8's rule). A fresh goal gap
     never runs `Synthesize` here. `planGap` (`:517`) keeps taking
     `pinnedAction` and resolving again; that second call is the same pure lookup, the precedent being the reclaim
     (`reconciler.go:1094` then `planGap` at `:1177` resolve the same pin twice today).
   - `reconciler.go:1071` (reclaim): hoist the resolution that already sits at `:1094` above the `staleMark` call:
     ```go
     leg := GapAction{}
     dispatchAction := rec.Action
     if resolved, _, perr := e.resolvePlannedAction(ctx, target, targetID, entityID, gapColumn, ga, row, rec.Action); perr == nil {
         leg = resolved
         dispatchAction = resolved.Action
     }
     confirmedConcluded := e.staleMark(targetID, entityID, row, gapColumn, leg)
     …
     collapseOnly := collapseOnlyReclaim(dispatchAction, confirmedConcluded)
     ```
     The `perr != nil` fallback for `dispatchAction` (the mark's recorded string) is unchanged; `leg` stays zero in
     that case (§3.3 row "unresolvable pin").
   - `reconciler.go:779` and `control.go:625`: `leg, resolvedRef, perr := e.resolvedLegAction(…)`; on `perr` the
     existing declines stand; else `resolvedAction := leg.Action` and `collapseOnlyReclaim(resolvedAction,
     e.staleMark(…, leg))`. `reArmDeclines`' goal-gap wording branch (`control.go:631`) stays; it now describes a
     verdict taken over the leg.

4. **Comment corrections** — `externalDispatchGap`'s lane-1 sentence (§2); `staleMark`'s doc gains one paragraph:
   the argument is the resolved leg, and why (the goal-mode seam re-imported the dossier's first entry by reading
   the entry). No history narration in either.

### 3.3 State table — every shape at every site, before → after

`E` = external-eligible pattern / `directOp` / `proposedOp`; `H` = `assignTask` or a parking pattern. "fresh" =
fresh `claimId` minted (a new attempt, booked against the budget); "collapse" = `claimId` preserved, paced.

| # | Gap shape | Pinned / resolved leg | `inflight_<g>` | Site | Before | After |
|---|---|---|---|---|---|---|
| 1 | static, `Action` named | = the entry | declared, false | all four | classified on the entry | **identical** — `resolvePlannedAction` returns `ga` unchanged |
| 2 | static | = the entry | undeclared | all four | false at guard 1 | identical |
| 3 | goal, leg E (`refreshBgcheck`) | pinned, in catalog | declared, false | lane-1 / reclaim | collapse (default arm) | **fresh** — §10.3's External reclaim, booked on the leg's budget |
| 4a | goal, leg E | pinned, in catalog | declared, true | lane-1 / reclaim | never reaches `staleMark`: the suppression gate precedes it (`evaluator.go:222-246` `continue` before `dispatchGap`; `reconciler.go:1027` returns, mark untouched) | identical — the gate is unchanged |
| 4b | goal, leg E | fresh or pinned | declared, true | `reset-budget` (no suppression gate, `control.go:625-630`) | refused (collapse-only wording) | refused — `!inflight` false ⇒ `staleMark` false ⇒ collapse-only; correct, a call is in flight |
| 4c | goal, leg E, **lost after accept** | pinned | true, permanently | all four | stuck (collapse / suppressed) | stuck (suppressed) — §1.2 row 3; the harm is the column's posture, identical before and after |
| 5 | goal, leg E | pinned, in catalog | **undeclared** (the corpus today) | all four | false at guard 1 | **identical** — guard 1 fires before the classifier; this is why Inc 3 exists |
| 6 | goal, leg H (`setTerms`, `signRenewal`, `verifyGuarantor`) | pinned, in catalog | declared, false | all four | collapse | collapse — `assignTask` is never external; the human task may be open |
| 7 | goal, fresh (no mark) | `resolvedLegAction` ⇒ `Synthesize` | declared, false | re-arm / `reset-budget` | leg E: refused / declined as collapse-only; leg H: refused | leg E: **re-arm fires, verb accepts** (a fresh instance is exactly what the operator asked for); leg H: unchanged refusal, same message |
| 8 | goal, pinned ref not in catalog (a re-authored catalog) | `planError{unplannable}` | any | lane-1 / reclaim | collapse (default arm) | collapse — `leg` stays zero ⇒ the new `""` arm ⇒ false. **Fail-closed: no resolution, no fresh claimId.** |
| 9 | goal, escalation mark (`rec.Action == "directOp"`, `EscalatedFrom` set) | not in catalog ⇒ `planError` | any | reclaim | `staleMark` false; `dispatchAction = "directOp"` ⇒ not collapse-only ⇒ unpaced bounded retry, `claimId` preserved | **identical** — the same fallback, the same verdicts (`weaver-escalation-episode-three-doors-design.md` §4.5 keeps this seam as is; the row proves it stays so) |
| 10 | static gap's escalation mark | `resolvePlannedAction` ⇒ `ga` (Action named) | as the gap declares | reclaim | classified on `ga` | identical (row 1) |
| 11 | candidates gap (planned mode, `Candidates` set) | pinned candidate / ranked | declared, false | all four | collapse (default arm) | classified on the candidate's shape — a `directOp` candidate reads external. **Zero consumers today** (C4: no `Candidates:` in `packages/`); the shape is in the table because the resolver hands it to the same predicate, and the pin test (§9) carries one vector for it. |
| 12 | goal, leg `triggerLoom` over an **unindexed** pattern (registry replaying after a restart) | pinned, in catalog | declared, false | reclaim (and now provably lane-1, §2) | false (transient, Debug) | identical — the classifier's own fail-safe, reached with the leg's `Pattern` instead of the entry's empty one |
| 13 | `surface` gap | — | — | — | never reaches `staleMark` (`surfaceOnlyGap` guards precede it at every site) | identical |
| 14 | `proposedOp` (static; `augurDispatch`) | = the entry | declared, false | re-arm | external per classifier, collapse-only per `collapseOnlyReclaim` — the recorded disagreement (`weaver-gap-companion-pair-validation-design.md` §3.2) | identical; **not this design's seam** |

Row 3 is the payoff; rows 1, 2, 4a–4c, 5, 6, 8–14 are the "nothing else moves" proof; row 7 is the operator-verb
payoff the Surveyor named. Row 7's `Synthesize` at the re-arm and at `reset-budget` is what happens **today**
(`resolvedLegAction` already runs there, `reconciler.go:779`, `control.go:625`); nothing new is paid.

**Two clocks, unchanged.** Lane-1's fresh mint is gated on `!leaseLive` (production mark lease 30 min); the sweep's
on lease expiry plus the collapse-only/uncapped backoff, which an External leg with a usable cap does not enter
(`uncappedExternal` reads `hasUsableRetryCap`, and `renewalComplete` declares 6). The give-up bound is the leg's
budget: a goal gap's count restarts per leg (`weaver.md` § planner mandate, "per-leg budget semantics"), so six
lost checks on `refreshBgcheck` exhaust it and `escalateExhaustedGap` takes over. No new clock.

### 3.4 The lint gate extension (Inc 2) — ships blocking

`scripts/lint-weaver-classify-by-shape.go` gains a second rule, in the same file, same self-test harness, same
`STRICT=1` wiring (`ci.yml:373` — no CI edit):

**Rule 2.** Every call to `staleMark` passes, as its `GapAction` argument, a plain identifier whose **every
assignment in the enclosing function** is one of: (a) a call to `resolvePlannedAction` or `resolvedLegAction`
(the identifier on the LHS of that call's result list); (b) the zero literal `GapAction{}`; (c) an identifier
that itself satisfies (a) — one hop, so `leg = resolved` inside an `if resolved, _, perr := e.resolvePlannedAction(…)`
passes. A selector (`target.Gaps[col]`), a function parameter, a map index, or an identifier bound any other way
(`leg := ga`) is a finding. **Rule 2b.** `externalDispatchGap` is called from exactly one site, inside
`staleMark`, with `staleMark`'s own parameter — any other call is a finding (the classifier is not a public
predicate; a second caller would have to re-derive the resolution discipline).

Self-test vectors (the harness at `:170-200` takes `{name, src, want}`):

| vector | want |
|---|---|
| `leg, _, perr := e.resolvePlannedAction(…); _ = e.staleMark(t, e2, row, col, leg)` | 0 |
| `leg := GapAction{}; if r, _, perr := e.resolvePlannedAction(…); perr == nil { leg = r }; _ = e.staleMark(…, leg)` | 0 |
| `leg, ref, perr := e.resolvedLegAction(…); _ = e.staleMark(…, leg)` | 0 |
| `_ = e.staleMark(t, e2, row, col, ga)` (`ga` a parameter) | 1 |
| `_ = e.staleMark(t, e2, row, col, target.Gaps[col])` | 1 |
| `leg := ga; _ = e.staleMark(…, leg)` | 1 |
| `ext, _, _ := e.externalDispatchGap(ga, row)` outside `staleMark` | 1 (rule 2b) |

The corpus run keeps its "found zero calls ⇒ refuse the all-clear" posture for both predicates. **Run during the
fire against the Inc 1 tree, expected: clean, 4 `staleMark` calls, 1 `externalDispatchGap` call** — and against
head before Inc 1, expected: 4 findings (every site passes `ga`), which is the proof the rule sees what it is
for. Blocking from the commit that lands it: Inc 1 leaves zero debt.

**Not a one-line addition** (reviewer): `checkFile` matches `call.Fun.(*ast.Ident)` only (`:133-135`), and
`e.staleMark` / `e.externalDispatchGap` are `*ast.SelectorExpr` on the receiver — Rule 2 needs a second matcher
(selector whose `Sel.Name` is the predicate) and per-`FuncDecl` scoping to read the argument's bindings, which the
current file-wide `ast.Inspect` does not carry. Same file, same self-test harness and `STRICT` wiring; a second
walk, not the same one.

Why not fold Rule 2 into the existing identifier check: the existing rule is "not a selector"; `ga` is an
identifier and would pass. The hazard here is specifically *which* identifier, so the rule must read the binding.

### 3.5 The package increment (Inc 3) — `packages/lease-signing`, a leg-scoped in-flight column

`renewalComplete` declares **`inflight_renewalComplete`**, computed from the same fan `leaseApplicationComplete`
computes `bgInflight` over (`lenses.go:909`) **and conjoined with the external leg's unmet effect**:

```cypher
count(DISTINCT CASE WHEN inst.class = 'service.backgroundCheck.instance'
                     AND inst.dispatch.data.vendorRef <> null
                     AND inst.outcome.data.status = null
                    THEN inst.key ELSE null END) AS bgInflight,
…
((bgInflight > 0) AND (bgcheckValidUntil = null)) AS inflight_renewalComplete,
```

added to the `WITH` aggregate (beside the existing `max(CASE …)` for `bgcheckValidUntil`, so the aggregate/non-
aggregate mix is the shape the rule engine already accepts here) and the `RETURN`, and to `BodyColumns`
(`renewal_lenses.go:70-73`) — the declaration surface `staleMark`'s guard 1 and `gapSuppressed` read
(`Output.BodyColumns` ∪ `StaticEmptyColumns`, per the gap-companion design §3.2). `(x = null)` is the corpus's
is-null idiom (`lenses.go:953`, `missing_onboarding`); `bgcheckValidUntil` is already a `WITH` alias so the
conjunct is expressible in the same `RETURN`. `maxretries_renewalComplete` = 6 already stands, so the §10.3 pair is
complete and `hasUsableRetryCap` is true. **Version bump:** `manifest.yaml` `0.31.27 → 0.31.28` and `package.go`
`Version` mirroring it (`DIFF_BASE=<base> go run ./scripts/lint-package-version.go`).

**Why scoped, not the bare `bgInflight > 0` (the reviewer's BLOCKING).** The suppression gate reads the column
**before any leg is chosen** — `gapSuppressionTerms` returns `suppressed` on the column alone (`evaluator.go:1864`),
at lane-1 before `dispatchGap` (`:222-246`) and at the sweep before its dispatch (`reconciler.go:1027`). Over a
mixed catalog a bare column therefore suppresses the **human** legs too: `setTerms` has no `pre` and cost 1
(`renewal_targets.go:99-105`), so whenever `bgcheckValidUntil` already holds the fresh leg is a human one, and a
check in flight for the same tenant — the static target's, dispatched on the same lapse (`lenses.go:954`: a leased
**and** approved application's `missing_bgcheck` re-opens), or a lost one beside a later completed one — would
park the landlord's and the tenant's tasks behind it. Scoping the column to `bgcheckValidUntil = null` makes it
read true only while the check is the thing the chain is waiting on, which is the contract's meaning of the column
("a remediation *for this gap* is already in flight") applied to the leg the gap is on.

**The authoring rule this fixes in place (goes into `weaver.md` §3.6):** *a `goal` gap's `inflight_<g>` is the
in-flight fact of its external leg conjoined with that leg's unmet `effects` — never the bare in-flight fact —
because the suppression gate runs before the leg is bound.* Per leg, after scoping:

- leg `refreshBgcheck` (E), `bgcheckValidUntil` null: the column is the §10.3 in-flight fact; `staleMark` reads
  `!inflight` ⇒ fresh reclaim only once the prior call concluded without success (rows 3 / 4b), suppressed while
  healthy (4a), stuck if lost-after-accept (4c — unchanged).
- legs H, `bgcheckValidUntil` present: the column reads false whatever stray instance is in flight ⇒ the human
  legs dispatch and reclaim exactly as today (row 6). The lane-1 suppression gate also skips `releaseCompletedLeg`
  (it lives inside `dispatchGap`, `evaluator.go:443`); the sweep's release runs **before** its suppression gate
  (`reconciler.go:996` precedes `:1027`), so even in the one state where the column is true a completed
  `refreshBgcheck` leg is released by the sweep and the chain advances — the backstop the reviewer asked to see
  named.
- **The static-target overlap** (`leaseApplicationComplete.missing_bgcheck` re-opening on the same lapse): with the
  scoped column, while `bgcheckValidUntil` is null the renewal suppresses on the static target's in-flight check
  and vice versa (`inflight_bgcheck` reads the renewal's instance too — same fan), so the lapse produces one check
  rather than two, modulo the `.dispatch`-landing window that already exists. Observed from the two cyphers; the
  Inc 3 e2e (§9) is where it is proven. The double-dispatch itself is pre-existing and not this design's row.

**Lens test (owned by Inc 3):** `renewal_lenses`' fixture family (`bgcheck_freshness_lens_test.go`,
`renewals_read_lens_test.go`) gains `TestRenewalComplete_InflightIsLegScoped`: (i) a seeded instance with
`.dispatch.vendorRef`, no `.outcome`, no completed check ⇒ `inflight_renewalComplete = true`; (ii) the same beside
a completed, unlapsed check (`bgcheckValidUntil` present) ⇒ **false** — the vector that pins the scoping;
(iii) `.outcome.status = 'failed'` alone ⇒ false (the payoff state, §1.2 row 1); (iv) no instance ⇒ false. Every
false is a REAL false, never null — the `hasGuarantor` null-folding lesson in that file's comment applies to any
boolean the engine's `boolColumn` reads.

### 3.6 Docs

`docs/components/weaver.md`: the dossier's first entry drops its "parked" clause (`:1307`) and names the gate's
second rule; the §10.3 companion paragraph (`:470-484`) gains two sentences: a planned-mode gap's class is read
from its resolved leg, and a goal gap's `inflight_<g>` is leg-scoped (the §3.5 authoring rule, with the reason). `packages/lease-signing/README.md`: the renewal target's column list. No `_bmad-output`
edits beyond this doc and the board row.

## 4. State-lifetime table

No new stateful mechanism. `leg` is a per-call local; `inflight_renewalComplete` is a lens projection whose
lifetime is the row's (created by projection, retracted with the row under `EmptyBehavior: "delete"`, reprojected
on every neighbour event across `providedTo` — the same neighbour path that already drives `bgcheckValidUntil`).
The engine's existing state (marks, counts, `__effect`) is read and written on exactly the paths it is today; the
only value that changes is which branch a reclaim takes (row 3 → fresh).

## 5. Contract surface — builds to §10.3 and §10.8, no edit

`docs/contracts/10-orchestration-substrate.md` §10.3, verbatim:

> Re-fire after lease expiry is class-dependent, and the class is read from the pattern's own step kinds — never
> from the playbook action name. […] **External gaps** — `directOp`, or `triggerLoom` of an **externalTask-only**
> pattern (the §13.1 external-remediation path): a reclaim re-dispatch is **intended** — a genuinely new attempt
> (a new vendor call) — gated on `inflight_<g>` reading false and hard-bounded by `maxretries_<g>`.

A goal leg that dispatches `triggerLoom` over `backgroundCheck` is "`triggerLoom` of an externalTask-only pattern".
The new behaviour is that sentence coming true for a goal gap.

`docs/contracts/10-orchestration-weaver.md` §10.8 (`:328-334`), the clause the reviewer asked to see adjudicated,
verbatim:

> **The mark pins the choice per leg:** the §10.3 mark's `action` carries the chosen actionRef at CAS-create, and
> a sweep reclaim re-dispatches the **pinned** leg verbatim — no re-rank, no re-plan — until the leg's declared
> `effects` hold […]. Replanning happens only at **leg boundaries** (effects-hold) and **gap boundaries**
> (close→reopen), both minting a fresh mark ⇒ fresh `claimId`; the deterministic-requestId / reclaim-collapse
> machinery is unchanged within a leg.

Row 3's stale reclaim is **not a re-plan**: the mark is CAS-replaced with the same pinned `action` (`marks.replace`
with `resolvedAction`, `reconciler.go:1226`; `fireEpisode`'s stale branch with `action`, `evaluator.go:948`), and
the leg advances only on effects-hold as before. What changes within the leg is the `claimId`, and the sentence
that governs that is the one it defers to: "the deterministic-requestId / reclaim-collapse machinery is unchanged
within a leg" — *unchanged* meaning §10.3's machinery applies as §10.3 defines it, which for an External gap is
"a genuinely new attempt" and for a userTask gap is collapse. The shard's own preamble fixes that reading: the
§10.3 constructs' "meaning is fixed here; their layouts, tuning, and mechanics are the owning component's"
(`10-orchestration-substrate.md`, "Named constructs the sibling shards reference"). Today's engine, which
collapses an external leg within a leg, is the behaviour that contradicts §10.3; the design removes the
contradiction. Nothing a consumer could observe against the two texts read together changes. **No
`docs/contracts/*` edit; nothing staged.**

One honest residue: read alone, "reclaim-collapse machinery … unchanged within a leg" *can* be misread as
"reclaims collapse within a leg". The design does not depend on a wording change, so none is staged; if Andrew
prefers the clause to say "the §10.3 class-dependent reclaim rule is unchanged within a leg", that is a
one-clause touch-up for a ratification session, not a condition of this build.

## 6. Reconciliation with the existing mental model

- *Didn't we already handle this?* Twice-adjacent, never here. The dossier's first entry fixed `staleMark`'s
  action-name split for **static** gaps (the pattern-step probe); `lint-weaver-classify-by-shape` (2026-09-04)
  fixed the same class for `collapseOnlyReclaim`'s **first** argument and explicitly parked its second
  (`staleMark`) "with a designer row". Two designs recorded the residue (`weaver-exhausted-gap-leg-boundary-design.md`
  §12 + build note; `weaver-escalation-episode-three-doors-design.md` §4.5 / §12). This is the row they parked
  against.
- *The "claimId decision the design did not take."* It was framed as open because the prior fire was scoped to the
  leg boundary. It is not open: §10.3 (quoted in §5) decides it for every gap of that shape, and the static
  `missing_bgcheck` gap on the same package already takes it. Declining it for a goal leg would make the goal
  catalog the one place in the platform where an external call cannot be retried after loss.
- *Does this duplicate a pattern?* It removes one: today the reclaim resolves the pin twice for two predicates
  that should read one resolution; after, one resolution feeds both (the same "pinned to the ref the
  classification was taken over" discipline the re-arm arm already documents at `reconciler.go:790-796`).
- *State we already keep?* None added. The in-flight fact is derived from the service instance's own aspects,
  the source of truth the static lens already reads.
- *The gap-companion validator* (`orchestrationguard.go`) stays `directOp`-only: a goal gap's class is not
  statically decidable from the playbook (its legs are mixed, and a `triggerLoom` leg's pattern may be
  row-templated) — the same reason `triggerLoom` was excluded. The runtime `uncappedExternal` backstop is what
  covers a goal gap that declares `inflight_<g>` without a cap; `renewalComplete` declares one.

## 7. Executable censuses (run 2026-09-05 at `259aa629`; the build's Phase 0 re-runs them)

```
C1 staleMark call sites (non-test):                       grep -n 'e\.staleMark(' internal/weaver/*.go | grep -v _test
  control.go:630 · evaluator.go:481 · reconciler.go:785 · reconciler.go:1071          → 4
C2 externalDispatchGap call sites (non-test):             grep -n 'externalDispatchGap(' … | grep -v _test
  evaluator.go:581 (inside staleMark) + the declaration at :616                         → 1 caller
C3 resolvedLegAction callers (non-test):                  grep -n 'e\.resolvedLegAction(' …
  control.go:625 · reconciler.go:779                                                    → 2
C4 planned-mode targets in packages/:                     grep -rn 'Mode: *"planned"' packages/ --include='*.go' | grep -v _test
  packages/lease-signing/renewal_targets.go:138                                         → 1 (renewalComplete)
    candidates gaps:                                      grep -rn 'Candidates:' packages/ … | wc -l → 0
    goal catalog legs by Action (renewal_targets.go):     triggerLoom ×1 (refreshBgcheck) · assignTask ×3
C5 inflight_renewalComplete declarations:                 grep -rn 'inflight_renewalComplete' packages/ internal/ | wc -l → 0
C6 tests naming staleMark/externalDispatchGap:            action_seam_matrix ×1 · exhausted_gap_leg ×1 · evaluator_internal ×14 · reconciler_internal ×1
    + OUTSIDE internal/weaver (reviewer): internal/pkgmgr/gapcompanionpin_test.go:220,:252 AST-parses
      externalDispatchGap and fails if the count of case clauses whose whole body returns external=true moves.
      The new `case "":` arm returns false ⇒ the pin is unaffected; Inc 1 runs `go test ./internal/pkgmgr/` to prove it.
C7 existing gate at head:                                 go run ./scripts/lint-weaver-classify-by-shape.go
  clean — 18 file(s), 3 collapseOnlyReclaim call(s)
C8 backgroundCheck pattern steps (patterns.go:38-42):     Kind: "externalTask" only                            → external-eligible
C9 lane-1 order in dispatchGap:                           e.staleMark( at evaluator.go:481 · e.planGap( at :517  → staleMark precedes planGap
C10 build-tagged tests in internal/weaver:                grep -rl '^//go:build ' internal/weaver/ | wc -l    → 0
```

Every count is a premise of a section above: C1/C2/C3 size Inc 1 and Rule 2's expected corpus; C4/C5 are why Inc 3
is mandatory and why row 11 has no live consumer; C6 is the test census Inc 1 amends; C7 is the gate's base; C8 is
row 3's eligibility; C9 is the comment correction; C10 says no tagged harness reaches a touched signature
(`staleMark` / `resolvedLegAction` are unexported engine methods, not interface members).

## 8. Alternatives

| # | Alternative | Verdict |
|---|---|---|
| 1 | **Do not have this thing** — keep classifying on the entry; a goal gap's external leg stays collapse-only. | The world with it removed is the world today, priced against the payoff §1.2 actually sizes: a renewal whose check concluded **without success** (or was never accepted) collapses onto the failed instance on every reclaim, paced, budget unspent, `GapBudgetExhausted` unreachable, and `reset-budget` refuses the operator with a wrong reason — the defect §10.3 was written against and the static gap on the same package no longer has. (A lost-after-accept call is NOT in this row's price: it is stuck with or without the design.) Two designs and a lint header recorded the debt. Rejected. |
| 2 | Per-leg companions — `inflight_<g>_<leg>` — so a mixed catalog can declare in-flight per leg. | The reviewer refuted this row's first objection ("nothing per-leg is expressible that per-gap is not"): a **bare** per-gap column IS read before the leg is bound and would suppress the human legs (§3.5). The objection that survives is different: per-leg columns need the engine to know the leg at the suppression gate, which for a fresh episode means resolving (and for a goal gap, synthesizing) *before* suppression — new contract vocabulary plus a reordering of lane-1's gate for every target. The **leg-scoped per-gap column** (§3.5) gives the same separation with no vocabulary and no engine change: the lens conjoins in-flight with the external leg's unmet effect, so the column is per-leg *in value* while per-gap *in shape*. Rejected in favour of that. |
| 2b | The bare per-gap column (`bgInflight > 0`), the first draft of Inc 3. | **Withdrawn — the BLOCKING finding.** Suppresses `setTerms` / `signRenewal` behind any in-flight check for the tenant once `bgcheckValidUntil` holds. Replaced by the scoped column; the lens test's vector (ii) pins the difference. |
| 3 | **Engine only** (Inc 1 + 2, no package change). | Inert for the only consumer: guard 1 fires before the classifier (row 5, C5 = 0). Dead scaffolding by the §2.3 test — the consumer exists but the value is unrealised until the lens declares. Rejected; the package increment is in the fire. |
| 4 | **Package only** (declare `inflight_renewalComplete`, no engine change). | The column is honoured for suppression only; `staleMark` still classifies the entry ⇒ the leg stays collapse-only (row 5 → row "declared, classifier false"). Priced in combination with 3: each half is the other's objection; both are required, and together they are the design. Rejected alone. |
| 5 | Record the class on the **mark** at dispatch (`rec.External bool`) and read it back, no resolution at reclaim. | Adds a field to a persisted key family (a lifetime at every boundary: create, replace, reclaim, the markless re-arm and `reset-budget` which have no mark to read); and it freezes a verdict the registry may correct on replay (row 12, the transient case the classifier deliberately re-evaluates live — `TestStaleMark_ClassifierFollowsRegistryReplay` pins that). The resolution is pure and already performed at three of four sites. Rejected. |
| 6 | Restructure the consumer: pull `refreshBgcheck` out into a static `triggerLoom` gap on the renewal lens; the goal keeps the three human legs. | Rewrites the design-of-record (`loftspace-lease-renewal-goal-authored-target-design.md` §4.3: conditional and vendor legs live in the goal so the planner sequences them; `signRenewal`'s `pre` entails the bgcheck atom). With the leg removed the goal is unplannable whenever the check lapses (`ErrNoPlan` ⇒ `unplannable` escalation) until a *different* target closes it — the chain gets a hole its own planner cannot see. And it leaves the platform seam wrong for the next mixed catalog. Rejected; the single-digit-consumer "rewrite the consumer" test was run and lost here because the consumer's shape is the ratified point of goal mode. |
| 7 | Extend the install-time validator to goal gaps (refuse `inflight` without `maxretries` on a catalog with an external leg). | Same objection as `triggerLoom`'s exclusion in the companion design: the leg's pattern may be row-templated and the registry may not hold it at install; a false refusal at install is worse than the runtime `uncappedExternal` backstop. Out of scope; unchanged. |

Each rejected row's objection run back against the recommendation: (2) — the recommendation adds no vocabulary;
(5) — it adds no state and keeps the live re-evaluation; (6) — it leaves the design-of-record intact; (3)/(4) —
it ships both halves in one fire, so neither objection applies to the whole.

## 9. Test strategy

**Inc 1 (engine).**
- `TestStaleMark_ExternalDispatchClassifier` (`evaluator_internal_test.go:561`): the table is reinterpreted as
  *legs* — every existing vector passes unchanged (a static entry is its own leg). Add: a goal-catalog entry
  materialised through `catalogEntryGapAction` with `Action: triggerLoom, Pattern: "bgcheckFlow"` ⇒ stale; the
  zero `GapAction` ⇒ false with no Health issue; a candidates gap's `directOp` candidate ⇒ stale (row 11's one
  vector).
- New `TestSweep_GoalLegExternalReclaimMintsFreshClaimID` (`reconciler_internal_test.go`, beside
  `TestSweep_InflightMarkerIgnoredForUnindexedPattern`): a planned-mode target with a 2-leg catalog
  (`triggerLoom`·externalTask-only, `assignTask`), row declares `inflight_x: false` + `maxretries_x`; a mark pinned
  to the external leg with an expired lease ⇒ the reclaim replaces the mark with a **different** `claimId`, bumps
  `Count` (an attempt), is not paced; the same with `inflight_x: true` ⇒ untouched (row 4); pinned to the
  `assignTask` leg ⇒ `claimId` preserved (row 6). This is the mutation-test pair the fixture rule demands: one
  vector per direction, and the fixture omits `inflight_x` in a third vector to pin row 5.
- `TestHandleRow_*` sibling for lane-1: found mark, expired lease, external leg, `inflight` false ⇒ `fireEpisode`'s
  `found && stale` branch (fresh `claimId`, `bumpDispatchCount(…, true, true, legScoped=true)`).
- `reArmDeclines`: the existing decline vectors (`control_internal_test`) gain two — a goal gap whose fresh leg is
  the external one with `inflight` false ⇒ **accepted**; the same with `inflight` true ⇒ refused with the
  goal-gap wording. The action-seam matrix (`action_seam_matrix_internal_test.go`) gains no new row: the
  plan-time-resolved row already asserts every seam refuses the empty action, which still holds.
- The `escalation` rows (9/10) are pinned by the existing exhausted-gap-leg corpus; run it, do not extend it.

**Inc 2 (gate).** The seven self-test vectors in §3.4; the corpus run against the Inc 1 tree; and the negative proof
(the gate against pre-Inc-1 `main` reports 4) recorded in the build note.

**Inc 3 (package).** The lens test in §3.5; `go test ./packages/lease-signing/ -run 'Renewal'` green; the
ephemeral-stack e2e — `internal/leaseconvergence/renewal_convergence_test.go`, **build-tagged `leaseshortwindow`**
(never compiled by `go test ./...`; run via `make test-lease-convergence`, `Makefile:1909`) —
drives one renewal through a lapsed check with the `backgroundCheck`
adapter replying **`failed`** (the state the column separates, §1.2 row 1 — a withheld reply is the lost-after-accept
state and must NOT be the payoff vector), asserts the reclaim after lease expiry mints a **second** Loom instance id for the tenant
(two `service.backgroundCheck.instance` vertices `providedTo` the identity) and that `Count` on the
`refreshBgcheck` leg reads 2. The static-target overlap in §3.5 is checked by the same run: while the withheld
check is in flight and no completed check exists, `inflight_renewalComplete` reads true and no second dispatch
occurs; then the fixture seeds a completed, unlapsed check beside the withheld one and asserts the column flips
to **false** and `setTerms` dispatches — the leg-scoping proof at the e2e level.

## 10. Migration, compatibility, risks

- **Running deployment.** Inc 1/2 are engine + script: a Weaver restart. Inc 3 is a package upgrade: the lens
  reprojects every `renewalComplete` row with the new column on its next event; until then guard 1 keeps the old
  verdict (row 5) — a monotone, safe interim. No weaver-state migration: marks, counts and `__effect` keys keep
  their shapes.
- **Byte-identity for static targets** — `resolvePlannedAction` is the identity for `Action != ""`; the suite's
  mode-absent invariant is the pin.
- **Risk — a fresh instance beside a live one.** The fresh mint is gated on `inflight_<g>` reading false, and the
  column is presence-based on `.dispatch` — written by the bridge once the adapter accepts the call. Between the
  dispatch op committing and `.dispatch` landing, `inflight` reads false while a call is committed-and-queued; the
  exposure is bounded by `!leaseLive` (30 min) at lane-1 and by lease expiry at the sweep — the same window
  `staleMark`'s call-site comment already prices for the static gap (`evaluator.go:467-480`). Unchanged in size;
  now applies to one more gap.
- **Risk — cost at lane-1.** One extra `resolvePlannedAction` per delivery *with a found mark* on a planned-mode
  target: a pinned catalog lookup, O(len(catalog)) = 4, no KV, no `Synthesize`. Fresh episodes skip it.
- **Risk — the goal-mode overlap with the static bgcheck target** (§3.5): an improvement on the cypher's face; the
  Inc 3 e2e is where it is proven rather than asserted.
- **Limit — lost after accept** (§1.2 row 3): the presence-based column cannot separate a lost call from a live one,
  so a vendor that never replies parks the leg, before and after. Lifting that is a platform decision about the
  `externalTask` bridge wait ("unbounded", `patterns.go:17`) for every consumer at once — a Surveyor row if a live
  instance appears, not a residual of this design.
- **Open questions:** none left open. The claimId question is answered by §5; the per-gap-vs-per-leg column by
  §8 row 2; the escalation-mark seam by row 9.

## 11. Decomposition for the Steward — one fire, three increments, in order

| Inc | Scope | Owns | Posture |
|---|---|---|---|
| **1 · engine** | `staleMark(…, leg)`; `resolvedLegAction` returns the leg; the four sites (§3.2); the `""` arm; the two comment corrections; `weaver.md` §3.6 | the §9 Inc 1 tests, all green under `go test ./internal/weaver/` | **posture-changing** (a goal gap's external leg reclaims fresh once its lens declares) |
| **2 · gate** | Rule 2 + 2b in `lint-weaver-classify-by-shape.go`, seven self-test vectors, blocking under `STRICT=1` | the negative proof against pre-Inc-1 main | hygiene |
| **3 · package** | `inflight_renewalComplete` (§3.5), `BodyColumns`, version `0.31.28` + `Version`, README, lens test, the e2e | the §9 Inc 3 tests; `lint-package-version` | **posture-changing** (the consumer goes live) |

Gates before done: `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`, every
`scripts/lint-*.go` (`lint-weaver-classify-by-shape` STRICT, `lint-package-version` with `DIFF_BASE`,
`lint-conventions`, `lint-board` STRICT for the row), `go test ./internal/weaver/ ./internal/pkgmgr/
./packages/lease-signing/`, and the `leaseshortwindow` tag — every file in `internal/leaseconvergence` carries it (C10's sibling census:
`grep -rl '^//go:build ' internal/leaseconvergence/` → 7 of 7), so the renewal e2e is invisible to `go test ./...`
and must be run by name. Review depth is the Steward's sizing (SKILL §4); the
dossier entries to copy into the brief's part 5: the first (shape-vs-name), the fourth (fixture supplies an
optional input — the `inflight_x` omission vector), and the fifth (an operator verb refuses what the arm declines).

## 12. Checklist walk (designer/SKILL.md §2.3) — items that bit

- **A.1 / A.4 (the row's premise is a hypothesis; count the instances the harm needs):** "live, not latent" split
  into a live misclassification and a latent harm; the fix was inert for the only consumer (C5 = 0) until the
  package increment was made mandatory. The row's "three legs" was four (C1).
- **A.7 (a shipped refusal's reason is a claim):** the parked "claimId decision the design did not take" was a
  scope note, not an open decision — §10.3 had already taken it; and the lane-1 ordering claim in
  `externalDispatchGap`'s doc was false at head (C9).
- **B.6 (read whether machinery can be bent):** `resolvePlannedAction`'s pinned branch is pure and already called
  twice per reclaim; `catalogEntryGapAction` carries `Pattern`, so the classifier needs nothing new.
- **C.4 (run every census):** C1–C10 pasted; C4 corrected row 11 to zero live consumers before it could justify
  anything.
- **D.1 / D.2 (omission fails closed; the state table with an outcome column):** the unresolvable pin passes the
  zero leg ⇒ the new `""` arm ⇒ no fresh claimId (row 8); rows 9/10 prove the escalation seam does not move.
- **D.9 (a convention's gate ships in the same design, blocking):** Rule 2 reads the binding, because "not a
  selector" would have admitted `ga`.
- **F.5 (mirrors X — read above X):** the reclaim's own doc at `reconciler.go:1077-1092` names the leg-ref hazard
  this design closes for the sibling predicate; the mirror imports the fix, not the bug.
- **§8 row 1 (delete the thing) and "rewrite the N consumers" (row 6)** both run; row 6 lost on the
  design-of-record, not on size.
- **Caught by the cold pass, not the walk (§13) — two classes worth a §2.3 line each:** (1) a declared column is
  read by a gate that runs **before** the variable the design reasons about (the leg) is bound — locate every
  reader of the column relative to that binding, not just the predicate you are fixing; the bare column would have
  parked two human legs. (2) a payoff must be evaluated in the state the *sensor* reports for the harm — a lost
  call reads "in flight" forever, so the mechanism only separates the states the column separates; §1.2 was
  re-derived as a table of check states.

## 13. Adversarial pass — run (2026-09-05, cold read-only reviewer against the code) — and stamp

The reviewer traced every claim to the deciding code and verified: C1/C7/C8/C9 line numbers and counts;
`resolvePlannedAction`'s pinned branches are pure (`strategist.go:434, :437, :561`); `catalogEntryGapAction`
carries `Pattern` (`:531`); rows 8/9/10 genuinely identical; no build-tagged or exported-test caller of
`staleMark` / `resolvedLegAction`; version `0.31.27 → 0.31.28`; `leaseshortwindow` on 7/7 leaseconvergence
files. Seven findings, all folded:

| Sev | Finding | Folded into |
|---|---|---|
| BLOCKING | the bare per-gap `inflight` column suppresses the human legs: `gapSuppressionTerms` short-circuits on the column before any leg is consulted (`evaluator.go:1864`, `:222-246`; `reconciler.go:1027`); `setTerms` has no `pre` and cost 1, so with `bgcheckValidUntil` held the fresh leg is human | §3.5 rewritten: the column is **leg-scoped** (`AND bgcheckValidUntil = null`); §8 rows 2/2b; lens vector (ii); the e2e's scoping assertion |
| MAJOR | §1.2's motivating harm (a call lost after accept) reads `inflight` **true forever** under the presence-based column and the unbounded bridge wait (`lenses.go:909`; `patterns.go:16-17`), so the design does not remove it | §1.2 re-derived as a table of check states; §0 sizes the payoff honestly; §8 row 1 re-priced; §10 names the limit and its owner |
| MAJOR | §5 asserted §10.8 compliance without quoting `10-orchestration-weaver.md:333-334` ("Replanning happens only at leg boundaries … fresh `claimId`; the reclaim-collapse machinery is unchanged within a leg") | §5 quotes and adjudicates it: the stale reclaim is not a re-plan, the pin survives, and "unchanged" defers to §10.3's class rule; the misreading is recorded as an optional touch-up, not a staged edit |
| MINOR | row 4 described a path 3 of 4 sites never take (suppression precedes `staleMark`) | rows 4a/4b/4c |
| MINOR | `internal/pkgmgr/gapcompanionpin_test.go:220,:252` AST-parses `externalDispatchGap`'s clauses | C6 extended; the `""` arm returns false, pin unaffected, `go test ./internal/pkgmgr/` in Inc 1's gates |
| MINOR | Rule 2 is not the existing walk: `checkFile` matches `*ast.Ident` callees only; `e.staleMark` is a selector, and binding analysis needs `FuncDecl` scope | §3.4 says so |
| MINOR | lane-1 guard `if found` would run `Synthesize` for a found mark with an empty action | §3.2 guards on `pinnedAction != ""` |
| NOTE | the sweep's `releaseCompletedLeg` precedes its suppression gate; lane-1's does not | §3.5 names the backstop |

Verdict as returned: *needs re-design — Inc 3 as specified regresses two human legs; §1.2's payoff and §5's
contract clearance both need re-derivation.* All three re-derived above; the mechanism (Inc 1/2) was not
disputed. The re-derived §3.5 keeps the increment package-only (no engine, no vocabulary), so the split and the
sizing stand.

**Stamp.** No fork (§0), no contract edit (§5), gates discharged (§2.3 walk in §12; the adversarial pass above):
`✅ RATIFIED (Winston-adjudicated, per the 2026-08-20 delegation)` — build-ready for the Lattice Steward, §11.
