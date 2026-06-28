# Weaver reclaim phantom-churn suppression — design

**Status:** ✅ **Andrew-ratified (2026-06-27) — Option D (Weaver-state backoff).**
**Component:** Weaver (`internal/weaver`) · **Stream:** Lattice (Stream 2) · **Size:** S–M (one build fire)
**Designer fire:** Winston, 2026-06-27 · **Builds on:** Contract #10 §10.3 (in-flight marks / reclaim),
the existing `mark` state (`state.go`) + `dispatchCount`, the §E retry-budget machinery.
**Contract change:** **NONE.** (The earlier Option-A draft staged a §10.3 clarification; that edit is
**withdrawn** — Option D reads/writes only Weaver-private `weaver-state` and touches no frozen contract.)

---

> **As-built note (2026-06-28, commit `04c7689`).** One grounded correction was made
> during the build, flagged for Andrew. This design's mark-survival reasoning (§2.3, §7
> — "the per-key TTL backstop is sized far larger than the 24-h backoff cap, so the mark
> never TTL-expires inside a backoff window") is **false at the default constants**:
> `markTTLBackstopFactor = 2`, so the mark TTL is `2 × lease` ≈ **60 m**, far *shorter*
> than the 24-h cap. A backed-off mark would therefore TTL-expire mid-backoff into a
> **markless open gap**, and a later CDC redelivery of the still-violating row would mint
> a fresh `claimId` → a new `taskId` → a **duplicate task** (`fireEpisode` CAS-creates on
> mark absence). The build closes this **within Option D's mechanism** (no extra writes,
> no new field): a userTask reclaim **sizes the re-armed mark's per-key TTL to outlast the
> next backoff window** (`backoffInterval(count+1)` + a sweep-cadence margin, floored at
> the default backstop), so the mark is always reclaimed — TTL re-armed — before it can
> die. `directOp` marks keep the byte-identical default TTL. With survival guaranteed the
> backoff reaches the full 24-h cap as intended (≈ 36 reclaims / 30 days, not ~1,440).
> The §7 "no extra write" decision still holds — the fix only *sizes* the existing reclaim
> write's TTL, it adds no write. Not a contract change.

## Decision (Andrew-ratified)

**What it does, in two lines.** When the reconciler sweep would reclaim an expired-lease mark for a
**collapse-only userTask action** (`assignTask` / `triggerLoom`), it applies an **exponential backoff keyed
on the mark's own state** (`ClaimedAt` + the existing `dispatchCount`): the first reclaim still fires at
lease-expiry (lost-dispatch recovery is unchanged), but subsequent reclaims of the *same still-open episode*
back off (≈ lease → 1h → 2h → … capped at 24h) instead of re-firing every sweep. `directOp` and external
gaps — where reclaim re-dispatch is the *intended* bounded retry — are **untouched**.

**Why Option D over the original Option-A probe (the ratified choice).** Grounding produced three facts that
re-pointed the design:
1. **The op-tracker TTL is 24 h** (Contract #4 §4.3) — so the originally-proposed Core-KV-tracker probe
   would only suppress within a 24 h window and reset to ~1/day anyway (~1,440 → ~30). The probe's *precision*
   ("did it commit?") buys little once you accept a daily floor.
2. **The dispatch requestId is already stateless-derivable** (`deriveEpisodeRequestID`, `actuator.go:130`) —
   so the probe needed no new state, *but neither does backoff*.
3. **Weaver already keeps the per-episode state backoff needs.** The `mark` (`state.go:77`) carries
   **`ClaimedAt`** — refreshed on every `create`/`replace` (lines 133/196), i.e. "when this episode was last
   dispatched" — and there is already a **`dispatchCount`** key in `weaver-state` (`state.go:236`). Backoff is
   therefore a **pure read of state Weaver already writes**, with **no Core-KV read** and **no frozen-contract
   change** — which also disentangles this item from contract #10 (the §10.3 edit is withdrawn, leaving only
   the unrelated L3 §10.8 edit there to settle independently).

Option D solves the same operational wart Option A targeted — the **phantom-reclaim churn** (see §1) — at
platform altitude, for all packages, more cheaply (no cross-component read, no contract surface), with richer
operator observability (suppression counter + the existing dispatch count). The one trade-off vs. A
(§7): backoff suppresses by "I dispatched recently," not "it provably committed," so a genuinely-lost
*subsequent* re-dispatch waits a backoff interval rather than the next sweep — immaterial for a human-task
gap open for up to 30 days, and the consumer's `claimId` idempotency makes any re-dispatch safe regardless.
The **first** reclaim is unchanged (fires at lease-expiry), so first-dispatch-loss recovery does not regress.

---

## 1. Problem & intent

### 1.1 The backlog item (Surveyor-filed, 2026-06-27)

> On an expired-lease reclaim, the sweeper re-dispatches the gap's action as a fresh episode
> (`reconciler.go:218`) without probing whether the prior episode's effect already landed — the §10.3
> "documented rare-double." For external-I/O gaps this is harmless (the bridge de-dups on `idempotencyKey`);
> but `assignTask` can mint a second task and `triggerLoom` a second pattern run before the first completes.

### 1.2 What grounding revealed — the *correctness* double is already closed

Reading the contract before the code reframes the item. **Contract #10 §10.3 already ratified** the
disposition the Surveyor proposes to add:

- The mark's `claimId` (minted at CAS-create, **preserved verbatim across every reclaim-`replace`**,
  `state.go:182`) seeds the dispatched artifact's id — `assignTask`'s `taskId` (`deriveStableTaskID`) and
  `triggerLoom`'s `instanceId` (`deriveStableInstanceID`, `actuator.go:145/157`). Every re-dispatch of the
  same open episode re-supplies the **same** id.
- The **consumer** is the single idempotency authority: `CreateTask`'s Starlark `kv.Read()`-then-no-ops if
  the task exists; a re-emitted `loom.patternStarted` collapses on Loom's existing `instance.<id>`. §10.3:
  *"This supersedes the prior 'accepted rare double / check-before-act = Phase-3 hardening' disposition for
  the two human userTask actions."*

So `assignTask`/`triggerLoom` **cannot mint a second task/pattern** — the Surveyor's correctness claim is
superseded by shipped behavior. The code/doc comments still calling it "the documented rare-double …
check-before-act deferred to Phase 3" (`reconciler.go:229`, `weaver.md:400`) are **stale doc-drift**.

### 1.3 The genuine residual — phantom churn, not a double

What consumer idempotency does **not** remove is the *cost* of the redundant dispatch. The canonical
human-in-the-loop gap (`missing_approval` → `assignTask`):

1. Lane-1 dispatches once; `CreateTask` commits; the approval task exists. `missing_approval` stays **true**
   (the human hasn't approved) → the gap stays **violating** → the mark stands with a ~30-min lease.
2. The sweep (1-min cadence) reclaims at lease-expiry. It re-`replace`s the mark (new revision → new episode
   requestId), re-publishes `CreateTask`, bumps `dispatchCount`, and logs `Warn "mark reclaimed"`. The
   duplicate `CreateTask` reaches the Processor, runs the DDL, the `kv.Read()` no-op collapses it — **a
   committed-but-empty op that still writes a fresh `vtx.op.<requestId>` tracker (24-h TTL)**.
3. This repeats **every reclaim window for up to `assignTaskGrantTTL` = 30 days** (`strategist.go:28`). At a
   ~30–60-min effective cadence that is **hundreds–~1,440 redundant committed ops, tracker vertices, and
   misleading Warn lines** for **one** pending approval.

The platform offers a suppression knob (`inflight_<g>`, honored by `gapSuppressed`, `evaluator.go:396`) — but
it is **optional package work**, **lens-lagged**, and **nothing forces a human-task package to author it**.
The phantom storm is the default for any userTask gap that omits it.

**Intent:** give the *platform* a default, lens-independent, state-cheap way to stop re-firing a
provably-recently-dispatched collapse-only episode every sweep — while leaving consumer idempotency, the
level-reconcile clearing, and the TTL backstop exactly as they are.

---

## 2. The shape (Option D — Weaver-state backoff)

### 2.1 Where it sits (a guard inside the existing reclaim path)

The backoff is a guard **inside `sweeper.reclaim` (`reconciler.go:231`)**, after the cheap gates that already
exist (`violating`, `gapSuppressed`, registry warm-up) and **before** `planGap`/`replace`/`fire` — so a
suppressed reclaim skips the plan-resolution, the mark write, the op publish, *and* the tracker it would
spawn:

```
reclaim(...):
    ... unchanged gates: target, orphan legs, violating, gapSuppressed ...

    # NEW — best-effort, action-gated to the two collapse-only userTask actions:
    if rec.Action in {assignTask, triggerLoom}:
        count   := e.dispatchCount(targetID, entityID, gapColumn)   # existing weaver-state key
        elapsed := now - parse(rec.ClaimedAt)                       # mark field, already written every dispatch
        if elapsed < backoffInterval(count):
            e.logger.Debug("weaver sweep: reclaim backed off; episode dispatched recently", ...)
            s.bump(&s.reclaimsSuppressed)                           # heartbeat counter (operator visibility)
            return                                                   # leave the mark; do NOT replace/fire/bump

    ... entityKey echo check, planGap, replace, fire, bumpDispatchCount ...   # unchanged
```

No new mark field, no new KV key, no Core-KV read. The guard is a pure comparison over state Weaver already
maintains.

### 2.2 The backoff function

```
backoffInterval(count):
    # count == 0 or 1  → base (≈ the mark lease) → first reclaim fires at lease-expiry as today
    # then exponential, capped at the 24-h idempotency horizon
    return min(base * 2^max(0, count-1), 24h)
```

- **`base ≈ lease`** (≈30 min): the *first* reclaim (`count` 0→1) fires at the normal lease-expiry, so a
  genuinely-lost first dispatch still recovers promptly — **no recovery regression vs. today**.
- **Exponential ramp, 24 h cap:** a 30-day open task sees ~30min, 1h, 2h, 4h, 8h, 16h, 24h, then ~daily —
  roughly **a few dozen** real re-dispatches instead of ~1,440. The cap aligns with the op-tracker's 24-h
  horizon (no point backing off past the window in which a duplicate would even be deduped at step 2).
- **`base` / cap are config** (mirroring `MarkLease`); the defaults above ship.

### 2.3 Why `ClaimedAt` is the right clock (no new state)

`ClaimedAt` is set to `now` on **every** `create` and `replace` (`state.go:133/196`) — i.e. it stamps the
**last actual (re-)dispatch** of the open episode. A backed-off sweep does **not** `replace`, so `ClaimedAt`
**keeps aging**; once `elapsed ≥ backoffInterval(count)` the next sweep proceeds to the real reclaim (which
`replace`s, resetting `ClaimedAt = now`, and bumps `dispatchCount`, lengthening the next interval). The clock
lives in shared state (the mark), so it is **instance-agnostic** — every Weaver instance computes the same
backoff verdict, no coordination needed.

**Mark survival across a backoff gap.** A backed-off sweep returns without re-arming the lease, so `reclaim`
is re-entered each sweep (a cheap compare + return — no I/O). The mark itself is kept alive by its existing
per-key TTL backstop (`markTTLBackstopFactor × lease`, `state.go`), which is sized far larger than the 24-h
backoff cap, so the mark never TTL-expires inside a backoff window; the periodic real reclaim re-arms it.

### 2.4 New heartbeat counter (Contract #5, author's-discretion metric)

Add `reclaimsSuppressed` alongside the existing sweep counters (`reclaims`, `orphansDeleted`, `corrupt`,
`reconciler.go:412`), surfaced under `metrics.sweep.*`. A rising `reclaimsSuppressed` with a flat `reclaims`
is the healthy steady state for a deployment with open human tasks — positive evidence for the operator + the
Lamplighter. **No contract change** — §5.4 already makes extra metrics author's discretion.

---

## 3. Why the backoff is action-gated to userTask actions

The discriminator is **what a reclaim re-dispatch actually does** per action (established by §10.3):

| Action | Reclaim re-dispatch effect | Back off? |
|---|---|---|
| `assignTask` | **Collapse-only** — same `claimId`-seeded `taskId`; `CreateTask` `kv.Read()` no-op. Re-dispatch never creates a second task; once committed it is pure waste. | **Yes** — safe + valuable. |
| `triggerLoom` | **Collapse-only** — same `claimId`-seeded `instanceId`; Loom's instance-presence + `CreateOnly` collapse a re-emitted `patternStarted`; a terminal instance drops it. | **Yes** — safe + valuable. |
| `directOp` | **Intended retry** — §10.3: external gaps' reclaim re-dispatch is *intended* ("re-call a dead vendor / mint a fresh service instance"), bounded by `inflight_<g>` + `maxretries_<g>`. Re-firing **is** the next attempt. | **No** — backing it off would slow the intended retry. |

For the two userTask actions, re-dispatch is *only ever* a collapse — so pacing repeats with backoff **loses
nothing** (the first reclaim still fires on time; the consumer collapses any later one) and **saves
everything** (the op, the tracker, the count bump, the Warn). For `directOp`, re-dispatch is the retry
mechanism; the backoff must not run, and `inflight_<g>` + `maxretries_<g>` stay the sole governor. Weaver
cannot tell from the action alone whether a given `directOp` is an idempotent CAS or an external retry, so it
conservatively excludes **all** `directOp` (a per-gap `reclaimMode` playbook hint could refine this later —
§7, out of scope now).

---

## 4. Why this is safe (and needs no contract change)

§10.3 rejected a *producer-side existence check* because **GET-the-artifact races the commit-propagation lag**
— a producer GET inside that window sees absent and re-publishes anyway, so it cannot *prevent* a double.
**Option D does not probe anything** — it neither GETs the artifact nor the op-tracker. It only **paces its
own re-dispatch cadence** using its own mark state. So the §10.3 prohibition is simply not engaged: there is
no producer-side check, no race to lose, no claim to "prevent a double" (that remains the consumer's job,
unchanged). This is why Option D needs **no §10.3 edit** — it operates entirely within Weaver's existing
reclaim discretion (the *interval* between reclaims was never contract-fixed; only the consumer-idempotency
disposition was).

Safety properties:
- **Never suppresses the first reclaim** → a lost first dispatch recovers at lease-expiry as today.
- **Absent/unknown commit state is irrelevant** → backoff doesn't ask "did it commit"; it re-fires every
  interval regardless, and the consumer collapses a redundant one. Defense in depth is unchanged.
- **`directOp` excluded** → the intended external retry is untouched.
- **Level-reconcile + TTL backstop unchanged** → once the underlying condition clears, the gap stops being
  violating and the mark is removed on the normal path; nothing about backoff delays that.

---

## 5. Contract surface

| Contract | Section | Change vs. build-to |
|---|---|---|
| **#10 Orchestration** | §10.3 | **NONE.** The Option-A §10.3 clarification is **withdrawn**; backoff engages no producer-check prohibition. (The unrelated L3 §10.8 edit staged in the same file is a *different* item — leave it.) |
| **#4 Idempotency tracker** | — | **Build-to** — not read by Option D at all. |
| **#5 Health KV** | §5.4 | **Build-to** — `reclaimsSuppressed` is an author's-discretion metric; no change. |

**No frozen-contract change.** Nothing under `docs/contracts/` is edited by this item.

---

## 6. The options weighed (D chosen; A/B/C recorded)

- **Option D — Weaver-state backoff (CHOSEN).** *For:* removes the phantom storm for all packages, no
  per-package `inflight_<g>` work; **no Core-KV read, no contract change**; richer observability; first-retry
  recovery unchanged. *Against:* paces by "dispatched recently," not "provably committed" (a lost *subsequent*
  re-dispatch waits one backoff interval — immaterial for 30-day human tasks; claimId idempotency makes any
  re-dispatch safe).
- **Option A — best-effort Core-KV op-tracker probe.** *For:* more precise (skips only a provably-committed
  episode). *Against:* TTL-bounded to a ~daily floor anyway (Contract #4 §4.3), a cross-component read, and it
  edits a §10.3 line set deliberately — entangling this item with contract #10's pending L3 edit. *Rejected
  in favor of D* (same outcome, cleaner surface).
- **Option B — cleanup only (declare resolved-by-§10.3).** *For:* smallest surface. *Against:* leaves the
  phantom-churn wart on every package author — a generic inefficiency in a generic component is platform work.
- **Option C — do nothing.** *Against:* the `inflight_<g>` knob is opt-in + lens-lagged; the default storm
  persists.

The doc-drift cleanup (§1.2 — the stale `reconciler.go:229` / `weaver.md:400` "rare-double … Phase-3"
comments, and the reclaim-fired Warn message) is folded into Option D regardless.

---

## 7. Risks, alternatives, decisions made

- **Risk: backoff delays recovery of a genuinely-lost dispatch.** Only *subsequent* re-dispatches are paced;
  the **first** reclaim fires at lease-expiry unchanged, so a lost first dispatch recovers as today. A lost
  later re-dispatch recovers after one backoff interval (hours) — acceptable for a human-task gap open for
  days, and claimId idempotency makes the eventual re-dispatch safe. *(Decided: acceptable.)*
- **Risk: a `directOp` retry is slowed.** Excluded by the action gate (§3). *(Decided: exclude all
  `directOp`.)*
- **Risk: mark TTL-expires during a backoff gap.** The per-key TTL backstop (`markTTLBackstopFactor × lease`)
  exceeds the 24-h cap by a wide margin; the periodic real reclaim re-arms it. *(Decided: safe; assert the
  factor ≥ cap/lease in the build.)*
- **Alternative — Option A probe.** Rejected (§6): same outcome, more surface, contract entanglement.
- **Alternative — add a new `lastDispatchedAt` mark field.** Unnecessary: `ClaimedAt` already is exactly that
  (refreshed on every dispatch). *(Decided: reuse `ClaimedAt`.)*
- **Alternative — re-arm the lease on a backed-off sweep (an `extendLease` write).** Rejected: a backed-off
  `reclaim` returning cheaply each sweep (compare + return, no I/O) is simpler than a new write path, and the
  mark TTL backstop already covers survival. *(Decided: no extra write.)*
- **Open question resolved — count semantics.** A backed-off sweep does **not** bump `dispatchCount` (it is
  not a real attempt); only a real reclaim bumps it, lengthening the next interval. *(Decided.)*
- **Open question resolved — log level.** Backed-off sweep logs **Debug**; the real reclaim-fired path keeps
  its log, reframed from "rare-double" to "mark reclaimed; re-dispatch (consumer collapses if already
  applied)".
- **Future refinement (noted, not built):** a per-gap `reclaimMode` (`collapse` | `retry`) playbook hint
  could let an idempotent `directOp` opt into backoff. Out of scope (avoids scope creep + a contract add).

---

## 8. Test strategy

**Unit (`reconciler_internal_test.go`, fake-Conn harness):**
- `ClaimedAt` recent (elapsed < backoff) → reclaim backed off: no `replace`, no `fire`, no `bumpDispatchCount`;
  `reclaimsSuppressed` incremented; mark left intact.
- `ClaimedAt` aged past `backoffInterval(count)` → reclaim proceeds exactly as today (existing reclaim tests,
  unchanged, are the regression net).
- **First reclaim (count 0→1)** fires at lease-expiry regardless of backoff (no recovery regression).
- **Backoff growth**: assert `backoffInterval` is monotonic in `count` and capped at 24 h.
- **Action gate**: a `directOp` expired-lease mark **never** backs off (always reclaims).

**E2e (`weaver_e2e_test.go`, ephemeral stack):** hold a `missing_*`/`assignTask` gap open across **several**
sweep intervals; assert (a) far fewer `CreateTask` trackers than sweep intervals (backoff pacing), (b)
`reclaimsSuppressed` rises while `reclaims` stays low, (c) the gap still clears promptly on level-reconcile
once satisfied. `TestWeaverE2E_MidFlightKill` (kills between CAS-create and publish) guards that
first-dispatch-loss recovery is unbroken.

**Gates:** `go build ./...`, `make vet`, `golangci-lint run ./...`, STRICT lint-conventions,
`go test ./internal/weaver/...`, the convergence-job suite. No Gate-2/Gate-3 surface touched (read-only of
own state; no auth/security plane).

---

## 9. Decomposition for the Steward

**One build fire (S–M), no contract step.**

- **Fire 1 — the backoff guard + cleanup (whole build).**
  1. `backoffInterval(count) time.Duration` (config base ≈ lease, exponential, 24-h cap).
  2. The action-gated backoff guard in `sweeper.reclaim` (after `gapSuppressed`, before `planGap`), reading
     `dispatchCount` + the mark's `ClaimedAt`.
  3. The `reclaimsSuppressed` counter + its heartbeat metric.
  4. **Doc-drift cleanup:** rewrite the `reconciler.go:229` comment and `weaver.md:400` "documented
     rare-double … check-before-act deferred to Phase 3" to reflect the §10.3 supersession + the shipped
     backoff; reframe the reclaim-fired Warn message.
  5. Tests (§8).

  Independently shippable + green. **No Andrew contract commit gates this** (Option D ships standalone).

---

## 10. Grounding index (what was read)

Contract #10 §10.3 (marks/reclaim/claimId supersession) · Contract #4 §4.1/§4.3 (idempotency tracker, 24-h
TTL) · Contract #5 §5.4 (author's-discretion metrics) · `internal/weaver/state.go` (`mark`{`ClaimedAt`,
`ClaimID`, `LeaseExpiresAt`}, `dispatchCount`, `markTTLBackstopFactor`, `create`/`replace`) ·
`internal/weaver/reconciler.go` (`sweeper`, `reclaim`, counters) · `internal/weaver/actuator.go`
(`deriveEpisodeRequestID`, `deriveStable{Task,Instance}ID`) · `internal/weaver/strategist.go`
(`assignTaskGrantTTL`) · `internal/weaver/evaluator.go` (`gapSuppressed`, `fire`, `bumpDispatchCount`) ·
`internal/weaver/engine.go` (Config buckets) · `docs/components/weaver.md` (Dispatch suppression, Reconciler
sweep) · `lattice-architecture.md` (P2/P5, data-placement D5) · CLAUDE.md (no-history-comments).
