# Weaver reclaim check-before-act probe — design

**Status:** 📐 **awaiting-Andrew (ratification)**
**Component:** Weaver (`internal/weaver`) · **Stream:** Lattice (Stream 2) · **Size:** S–M (one build fire)
**Designer fire:** Winston, 2026-06-27 · **Builds on:** Contract #10 §10.3 (in-flight marks / reclaim),
Contract #4 (idempotency tracker), the §E retry-budget machinery.

---

## For Andrew (the one-look decision)

**What it does, in two lines.** When the reconciler sweep reclaims an expired-lease mark for a
**collapse-only userTask action** (`assignTask` / `triggerLoom`), it first does **one Core-KV GET of the
prior episode's op-tracker** (`vtx.op.<requestId>`, re-derived from the mark's revision — storage-free). If
the prior dispatch already **committed**, the reclaim is **skipped** (the effect is durably in place; a
re-dispatch would only collapse on the existing task/instance anyway). If absent, the reclaim re-fires
**exactly as today**. `directOp` and external gaps — where reclaim re-dispatch is the *intended* bounded
retry — are **untouched**.

**The headline finding you should weigh first.** The Surveyor filed this as a *correctness* gap
("`assignTask` can mint a second task, `triggerLoom` a second pattern run"). **That correctness double is
already closed** by the ratified §10.3 claimId-seeded consumer idempotency (shipped). What remains is **not
correctness** — it is **wasteful phantom-reclaim churn + misleading operator noise**: an open human-task gap
whose package projects no `inflight_<g>` column is re-dispatched every sweep (~30–60 min) for the entire
**30-day** `assignTaskGrantTTL` window — up to ~1,440 redundant `CreateTask` ops + `vtx.op` trackers + Warn
"mark reclaimed" log lines for a *single* pending approval, every one a no-op the consumer collapses. This
design eliminates that at **platform altitude** without requiring every package author to add an
`inflight_<g>` lens column.

**ARCHITECTURAL FORK — probe vs. close (your call).** §10.3 *explicitly rejects* a producer-side existence
check ("a Weaver GET would race the publish→commit propagation lag"). My design threads that needle with a
distinction the contract didn't draw — see §6 — but it is a genuine judgment call:
- **Option A (recommended):** ship the best-effort op-tracker probe + the §10.3 clarification. Real
  churn/noise reduction; lens-independent; type-agnostic; one cheap GET that *net-reduces* load.
- **Option B (minimal):** declare the item resolved-by-§10.3; do only the **observability cleanup** (fix the
  stale `reconciler.go:229` comment + weaver.md:400, downgrade the phantom-reclaim log Warn→Debug). No
  contract change.
- **Option C:** do nothing — `inflight_<g>` is the sanctioned package knob; phantom churn is the package's
  job to quiet.

My recommendation is **A**: B/C leave a real operational wart (1,440× redundant ops per open human task) that
the *platform* can fix once, cheaply, for *all* packages. But A touches a frozen-contract line you ratified
deliberately, so it's yours to confirm. Trade-offs in §6.

**FROZEN-CONTRACT change (staged UNCOMMITTED in `main` for your review).** Contract #10 §10.3 — the
"Re-fire after lease expiry" bullet (line ~297) — is amended to **permit** the best-effort op-tracker probe
for the collapse-only userTask actions, **distinguished** from the rejected artifact-GET. The edit is staged
unstaged/uncommitted in `docs/contracts/10-orchestration-surfaces.md` as the proposal (the diff *is* the
request). Affected consumers: **Weaver only** (the reclaim path); no other component reads or writes this
surface. Only needed if you pick **Option A**.

---

## 1. Problem & intent

### 1.1 The backlog item (Surveyor-filed, 2026-06-27)

> On an expired-lease reclaim, the sweeper re-dispatches the gap's action as a fresh episode
> (`reconciler.go:218`) without probing whether the prior episode's effect already landed — the §10.3
> "documented rare-double." For external-I/O gaps this is harmless (the bridge de-dups on `idempotencyKey`);
> but `assignTask` can mint a second task and `triggerLoom` a second pattern run before the first completes.
> A bounded check-before-act probe … would collapse the double for the non-idempotent actions while leaving
> the lease/level-reconcile recovery intact.

### 1.2 What grounding revealed — the correctness double is *already closed*

Reading the contract before the code is what this stage is for, and it reframes the item. **Contract #10
§10.3 (lines 293–331) already ratified the disposition** the Surveyor proposes to add:

- The mark's `claimId` (minted at CAS-create, **preserved verbatim across every reclaim-`replace`**) seeds
  the dispatched artifact's id — `assignTask`'s `taskId` (`deriveStableTaskID`) and `triggerLoom`'s Loom
  `instanceId` (`deriveStableInstanceID`, `actuator.go:145`/`:157`). Every re-dispatch of the same open
  episode re-supplies the **same** id.
- The **consumer** is "the single idempotency authority": `CreateTask`'s Starlark reads the task key via
  `kv.Read()` and silently no-ops if present-and-alive (`CreateOnly` is the narrow concurrent-dispatch
  backstop); a re-emitted `loom.patternStarted` collapses on Loom's existing `instance.<id>`. §10.3:
  *"This **supersedes** the prior 'accepted rare double / check-before-act = Phase-3 hardening' disposition
  for the two human userTask actions."*
- §10.3 **explicitly rejects** the producer-side check the Surveyor describes: *"Weaver re-publishes the
  dispatch **without** a producer-side existence check (a Weaver GET would race the publish→commit
  propagation lag — inside that window it sees absent and re-publishes anyway, so it cannot **prevent** a
  double; only the consumer, committing against real state, can)."*
- For `directOp` and external gaps, §10.3 states the reclaim re-dispatch is **intended** ("re-call a dead
  vendor / mint a fresh service instance"), episode-scoped on `markRevision` and **bounded by `inflight_<g>`
  + `maxretries_<g>`."

So `assignTask`/`triggerLoom` **cannot mint a second task/pattern** — the Surveyor's correctness claim is
superseded by ratified contract behavior that has already shipped (the claimId machinery is live in
`strategist.go`/`state.go`/`reconciler.go`). The code/doc comments that still call it "the documented
rare-double … check-before-act deferred to Phase 3" (`reconciler.go:229`, `weaver.md:400`) are **stale
doc-drift** relative to the §10.3 supersession.

### 1.3 The genuine residual — phantom churn, not a double

What the consumer idempotency does **not** remove is the *cost* of the redundant dispatch. Walk the canonical
human-in-the-loop gap (`missing_approval` → `assignTask`):

1. Lane-1 dispatches once; `CreateTask` commits; the approval task now exists. `missing_approval` stays
   **true** (the human hasn't approved yet) → the gap stays **violating** → the mark stands with a 30-min
   lease.
2. The sweep (1-min cadence) reclaims at lease expiry (~30–60 min). It re-plans, re-`replace`s the mark
   (new revision → new episode requestId), re-publishes `CreateTask`, **bumps the dispatch-count**, and logs
   `Warn "weaver sweep: mark reclaimed"`. The duplicate `CreateTask` reaches the Processor, runs the DDL,
   and the `kv.Read()` no-op collapses it — **a committed but empty op, which still writes a fresh
   `vtx.op.<requestId>` tracker (24-h TTL)**.
3. This repeats **every reclaim window for up to `assignTaskGrantTTL` = 30 days** (`strategist.go:28`) — the
   grant deliberately outlives any human response. At a ~30-min effective cadence that is **~1,440 redundant
   committed ops, ~1,440 tracker vertices, and ~1,440 misleading Warn lines** for **one** pending approval.

The platform *does* offer a suppression knob — a package may project `inflight_<g>` (a lens column "a
remediation is in flight"), which `gapSuppressed` (`evaluator.go:396`) honors in both dispatch legs. But it
is **optional package work**, it is **eventually-consistent** (lens lag re-opens the window on every flip),
and **nothing forces a human-task package to author it**. The phantom storm is the default for any userTask
gap that omits it.

**Intent, then:** give the *platform* a default, lens-independent way to recognize "this episode's dispatch
already committed; re-firing only collapses" and **skip the redundant work + the false alarm** — while
leaving the ratified consumer-idempotency authority, the level-reconcile clearing, and the TTL backstop
exactly as they are.

---

## 2. The shape

### 2.1 Where it sits (mirroring the existing reclaim path)

The probe is a guard **inside `sweeper.reclaim` (`reconciler.go:231`)**, placed after the cheap gates that
already exist (`violating`, `gapSuppressed`, registry warm-up) and **before** `planGap` — so a suppressed
reclaim also skips the plan/registry-resolution cost:

```
reclaim(...):
    target, installed := source.target(targetID)         # unchanged
    ... orphan legs (warm-up gated) ...                  # unchanged
    if !boolColumn(violating): return                    # unchanged (L1 parity)
    if gapSuppressed(...): return                         # unchanged (inflight_/maxretries gate)

    # NEW — best-effort, userTask-scoped:
    if rec.Action in {assignTask, triggerLoom}:
        priorReqID := deriveEpisodeRequestID(targetID, entityID, gapColumn, markRev)
        if e.priorEpisodeCommitted(ctx, priorReqID):     # one core-kv GET of vtx.op.<priorReqID>
            e.logger.Debug("weaver sweep: reclaim suppressed; prior episode committed (consumer idempotency holds)", ...)
            s.bump(&s.reclaimsSuppressed)                # heartbeat counter (operator visibility)
            return                                       # leave the mark; level-reconcile / TTL bound it

    ... entityKey echo check, planGap, replace, fire ... # unchanged
```

The mechanism is **type-agnostic** (the op-tracker exists for *every* committed op regardless of action),
but the *application* is **action-gated** to the two collapse-only userTask actions — see §3 for why
`directOp` is excluded.

### 2.2 The evidence: the prior episode's op-tracker (Contract #4)

`deriveEpisodeRequestID(targetID, entityID, gapColumn, markRevision)` (`actuator.go:130`) is a **pure
function** of the mark key + its revision. Each time the mark is written (CAS-create, or reclaim-`replace`)
the engine `fire`s an op whose requestId derives from *that exact revision* (`evaluator.go:294`). So given
the mark at its **current** revision `markRev` (read this sweep pass), the requestId of the **last dispatch
actually fired for this mark** is exactly `deriveEpisodeRequestID(…, markRev)` — **no new mark field is
required**; the prior requestId is re-derived, not stored.

The probe reads `vtx.op.<priorReqID>` from `cfg.CoreKVBucket`:

| Tracker state | Meaning | Probe verdict |
|---|---|---|
| **Present, `isDeleted:false`** | The prior dispatch **committed** (Contract #4 §4.1 — tracker written atomically at commit step 8); the task/instance is durably in place. | **Skip** the reclaim (quiet). |
| **Absent** | Op never committed — publish failed (`fire`→Nak), Processor rejected it (auth/validation), it is still in the Processor backlog (tracker not yet written), or it committed >24 h ago and the tracker TTL-expired. | **Reclaim** as today. |
| **Present, `isDeleted:true`** | Operator-tombstone-then-resubmit signal (Contract #4 §4.3) — treat as not-committed. | **Reclaim** as today. |

### 2.3 Read path (P5) and write path (P2)

- **Read path.** Weaver **already** reads Core KV directly — `newTargetSource(conn, cfg.CoreKVBucket, …)`
  (`engine.go:284`) watches `vtx.meta.>` for the registry. Weaver is a **platform binary on the P5
  allowed-direct-read list** (CLAUDE.md). The probe adds **one `KVGet` of `vtx.op.<id>`** on the same
  bucket — *not* a new boundary crossing, and the op-tracker is **type-agnostic** (no concrete-type coupling,
  so it respects "don't hardwire generic components to concrete types", D5/architecture-data-placement).
- **Write path.** **Nothing new is written.** The probe is read-only; it *removes* writes (the redundant op
  submit + its tracker) it would otherwise cause. P2 is strengthened, not touched.

### 2.4 New heartbeat counter (Contract #5, author's-discretion metric)

Add `reclaimsSuppressed` alongside the existing sweep counters (`reclaims`, `orphansDeleted`, `corrupt` —
`reconciler.go:412`), surfaced under `metrics.sweep.*` in the Weaver heartbeat. It gives the operator (and
the Lamplighter) positive evidence the probe is working — a rising `reclaimsSuppressed` with a flat
`reclaims` is the healthy steady state for a deployment with open human tasks. **No contract change** —
§5.4 already makes extra metrics author's discretion.

---

## 3. Why the probe is action-gated to userTask actions

The discriminator is **what a reclaim re-dispatch actually does** per action — established by §10.3:

| Action | Reclaim re-dispatch effect | Probe-suppress? |
|---|---|---|
| `assignTask` | **Collapse-only** — same `claimId`-seeded `taskId`; `CreateTask` `kv.Read()` no-op. Re-dispatch never creates a second task; it is pure waste once committed. | **Yes** — safe + valuable. |
| `triggerLoom` | **Collapse-only** — same `claimId`-seeded `instanceId`; Loom's instance-presence + `CreateOnly` collapse a re-emitted `patternStarted`; a *terminal* instance drops it (no re-create). §10.3: a re-trigger **never** re-calls a vendor or mints a new instance. | **Yes** — safe + valuable. |
| `directOp` | **Intended retry** — §10.3: external gaps' reclaim re-dispatch is *intended* ("re-call a dead vendor / mint a fresh service instance"), bounded by `inflight_<g>` + `maxretries_<g>`. Re-firing **is** the next attempt. | **No** — suppressing it would break the bounded retry. |

For the two userTask actions, re-dispatch is *only ever* a collapse — so skipping a provably-committed one
**loses nothing** (no attempt is forgone) and **saves everything** (the op, the tracker, the count bump, the
Warn). For `directOp`, re-dispatch is the retry mechanism itself; the probe must not run, and the existing
`inflight_<g>` + `maxretries_<g>` bound stays the sole governor. Weaver cannot tell from the action alone
whether a *given* `directOp` is an idempotent CAS (collapse-only) or an external-retry, so it conservatively
excludes all `directOp` — a per-gap `reclaimMode` playbook hint could refine this later (§7), but that is
out of scope now.

**Dispatch-count interaction.** A probe-suppressed reclaim does **not** `bumpDispatchCount`. This is *more*
correct, not a regression: the §E retry budget counts *real* attempts toward `maxretries_<g>`, and a phantom
re-dispatch that the consumer collapses is not a real attempt. userTask gaps typically carry no
`maxretries_<g>` (a human task should not "give up"), so the budget term is inert for them regardless; even
where one is set, not consuming it on a collapse is the intended semantics (attempts are paced by real
outcomes, not by mark-lease expiries — §10.3 / `state.go:50`).

---

## 4. Why this is safe where the rejected check was not (the core insight)

§10.3 rejected the producer check because **GET-the-artifact races the lag**: the task/instance is created
*asynchronously after* the op commits and propagates, so a producer GET inside that window sees absent and
re-publishes anyway — it cannot *prevent* a double.

The op-tracker probe is **race-free in the only direction that matters**:

- The tracker is written **atomically at commit** (Contract #4 §4.1, step 8) — it *is* the commit fact the
  platform already uses for step-2 dedup. It is **durable and monotonic**: once present it never
  "un-commits."
- **Present ⟹ definitely committed ⟹ the artifact definitely exists** (the same atomic batch wrote both).
  So "present → skip" can never cause a *wrong* skip.
- **Absent → reclaim** is the safe default: if the op was actually mid-flight (pending pre-commit), the
  reclaim re-fires, and the **unchanged consumer idempotency collapses the result** (same `claimId` →
  same `taskId`/`instanceId`). Defense in depth — the probe is a *prompt*, the consumer is the *backstop*.

So the probe **never claims to prevent a double** (that remains the consumer's job, per §10.3); it only
**skips provably-redundant work** when it can cheaply read a settled commit fact. That is a different
operation from the rejected artifact-GET, and it is the distinction the §10.3 clarification (§6) draws.

**Tracker-TTL alignment.** The op-tracker lives **24 h** (Contract #4 §4.3); the mark lease is **30 min**,
TTL-backstop **60 min**. Reclaims occur ~30–60 min after the last dispatch — three orders of magnitude
inside the 24-h tracker horizon — so a committed prior episode's tracker is **reliably present**. After 24 h
the tracker TTL-expires, so one reclaim per 24 h re-fires (re-creating the tracker, collapsing on the
existing task) and goes quiet again: a 30-day open task drops from ~1,440 phantom dispatches to **~30** (one
per day) — a ~48× reduction — with the residual handful still individually harmless.

---

## 5. Contract surface

| Contract | Section | Change vs. build-to |
|---|---|---|
| **#10 Orchestration** | §10.3 "Re-fire after lease expiry" bullet (~line 297) | **Change** — permit the best-effort op-tracker probe for the collapse-only userTask actions, distinguished from the rejected artifact-GET. **Staged UNCOMMITTED in `main`** (Option A only). |
| **#4 Idempotency tracker** | §4.1 / §4.3 | **Build-to** — the tracker shape, the atomic-at-commit write, the 24-h TTL are used as-is; no change. |
| **#5 Health KV** | §5.4 | **Build-to** — `reclaimsSuppressed` is an author's-discretion metric; no change. |

The §10.3 edit is the **only** contract change, and **only if Andrew picks Option A**. Under Option B it is
withdrawn (the cleanup needs no contract change). It is staged unstaged in the working tree as the proposal;
the Designer does **not** commit it.

---

## 6. The fork in full — probe vs. close

**Option A — ship the best-effort probe (recommended).**
- *For:* removes ~1,440 redundant committed ops + trackers + Warn lines per open human task, for *all*
  packages, without per-package `inflight_<g>` work; lens-independent (works even when the package projects
  no companion column); one cheap GET on a path that already reads Core KV; **net-reduces** load (saves a
  publish + a Processor op + a tracker write per suppressed reclaim); strictly additive — absent-tracker
  behavior is byte-for-byte today's.
- *Against:* touches a §10.3 line you ratified deliberately; introduces a *second* idempotency-adjacent
  mechanism (a prompt above the consumer authority), which is conceptual surface area; the value is
  efficiency/observability, not correctness (the correctness case is already closed).

**Option B — close as resolved-by-§10.3, cleanup only.**
- *For:* no contract change; smallest surface; honors §10.3's "consumer is the single authority" literally;
  the stale comment/doc fixes are worth doing regardless.
- *Against:* leaves the phantom-churn wart in place; pushes the fix onto every package author (project
  `inflight_<g>`), which is exactly the kind of platform-vs-package boundary the architecture tries to get
  right — a *generic* inefficiency in a *generic* component is platform work.

**Option C — do nothing.**
- *For:* zero change; `inflight_<g>` is the sanctioned, already-shipped knob.
- *Against:* the knob is opt-in and lens-lagged; the default-path storm persists.

**Recommendation: A.** The churn is a real, generic, default-path cost in a generic platform component, and
the platform can fix it once, cheaply, race-free, for everyone — which is precisely the altitude argument.
B is a defensible fallback if you'd rather keep §10.3's producer-check prohibition absolute; in that case
take the **cleanup half of Fire 1** (below) and drop the probe.

---

## 7. Risks, alternatives, and decisions made

- **Risk: the probe masks a genuinely-lost dispatch.** It cannot — *absent* tracker ⇒ reclaim, unchanged.
  Only a *committed* prior op is skipped, and a committed op's effect is durable; there is nothing to
  recover. *(Decided: no.)*
- **Risk: a `directOp` retry is suppressed.** Excluded by the action gate (§3). *(Decided: exclude all
  `directOp`.)*
- **Risk: read cost per sweep.** The GET runs only on the *expired-lease reclaim* branch (already the rare,
  storm-prone path), and it *replaces* a heavier publish+op+tracker-write — net-negative load. *(Decided:
  acceptable.)*
- **Alternative considered — store the artifact id / a probe flag on the mark.** Rejected: the prior
  requestId is re-derivable from the mark revision (storage-free), and an extra mark field would add a write
  to the very path we're trying to make quieter.
- **Alternative considered — GET the artifact (task/instance) directly.** Rejected: it is the exact check
  §10.3 forbids (races the lag), couples Weaver to concrete artifact types, and crosses into `loom-state`
  for `triggerLoom`. The op-tracker is type-agnostic and race-free.
- **Alternative considered — make the probe action-agnostic (include `directOp`).** Rejected: §10.3 makes
  `directOp` reclaim the *intended* retry; a uniform probe would break it. A future per-gap `reclaimMode`
  playbook hint (`collapse` | `retry`) could let an *idempotent* `directOp` opt into suppression — noted as
  a deferred refinement, **not** designed here (avoids scope creep + a contract addition).
- **Open question resolved — count-bump on suppression.** Do **not** bump (see §3).
- **Open question resolved — log level.** Suppression logs at **Debug**; the existing reclaim-fired path
  keeps its Warn. As part of the cleanup, the reclaim-fired Warn message is reframed from "rare-double" to
  "mark reclaimed; re-dispatch (consumer collapses if already applied)".

---

## 8. Test strategy

**Unit (`reconciler_internal_test.go`, against the existing fake-Conn harness):**
- Probe **present** → reclaim skipped: no `replace`, no `fire`, no `bumpDispatchCount`; `reclaimsSuppressed`
  incremented; mark left intact at its current revision.
- Probe **absent** → reclaim proceeds: `replace` + `fire` + `bumpDispatchCount` exactly as today (the
  existing reclaim tests, unchanged, are the regression net).
- **Action gate**: a `directOp` expired-lease mark **never** probes and always reclaims (assert no `vtx.op`
  GET issued for `directOp`).
- **Tombstoned tracker** (`isDeleted:true`) → treated as absent → reclaim.
- `deriveEpisodeRequestID(…, markRev)` round-trip: the requestId the probe derives equals the requestId the
  prior `fire` used at that revision (pin the storage-free re-derivation invariant).

**E2e (`weaver_e2e_test.go`, ephemeral stack):** extend the open-userTask-gap convergence test to hold a
`missing_*`/`assignTask` gap open across **several** sweep intervals and assert (a) exactly **one**
`CreateTask` tracker is created (not one per sweep), (b) `reclaimsSuppressed` rises while `reclaims` stays
flat, (c) the gap still clears promptly on level-reconcile once the underlying condition is satisfied.
`TestWeaverE2E_MidFlightKill` (kills between CAS-create and publish → tracker absent) is the guard that the
absent-path recovery is unbroken.

**Gates:** `go build ./...`, `make vet`, `golangci-lint run ./...`, STRICT lint-conventions,
`go test ./internal/weaver/...`, and the convergence-job suite. No Gate-2/Gate-3 surface is touched
(read-only, no auth/security plane).

---

## 9. Decomposition for the Steward

This is **one build fire** (S–M); the contract edit is Andrew's commit, not a build step.

- **Fire 1 — the probe + cleanup (whole build).**
  1. `priorEpisodeCommitted(ctx, requestID) bool` helper (one `KVGet` of `cfg.CoreKVBucket` /
     `vtx.op.<requestID>`; absent/tombstoned → false; transient KV error → false = *do not suppress*, the
     safe/dispatch side, mirroring `gapSuppressed`'s error posture).
  2. The action-gated probe call in `sweeper.reclaim` (after `gapSuppressed`, before `planGap`).
  3. The `reclaimsSuppressed` counter + its heartbeat metric.
  4. **Doc-drift cleanup** (do regardless of A/B): rewrite the `reconciler.go:229` comment and
     `weaver.md:400` "documented rare-double … check-before-act deferred to Phase 3" to reflect the §10.3
     supersession + (Option A) the shipped probe; reframe the reclaim-fired Warn message.
  5. Tests (§8).

  Independently shippable + green. Under **Option B**, ship steps 4–5's cleanup half only and drop 1–3.

- **(Andrew) ratify + commit the §10.3 clarification** — gates the probe build under Option A; the cleanup
  half ships without it.

---

## 10. Grounding index (what was read)

Contract #10 §10.3 (marks/reclaim/claimId supersession, lines 270–331) · Contract #4 (idempotency tracker,
§4.1/§4.3) · Contract #5 §5.4 (author's-discretion metrics) · `internal/weaver/reconciler.go` (`sweeper`,
`reclaim`, `sweepMark`, counters) · `internal/weaver/actuator.go` (`deriveEpisodeRequestID`,
`deriveStableTaskID`/`InstanceID`) · `internal/weaver/strategist.go` (`assignTaskGrantTTL`, the action plans)
· `internal/weaver/state.go` (mark/`claimId`, `inflight_`/`maxretries_`, dispatch-count) ·
`internal/weaver/evaluator.go` (`gapSuppressed`, `fire`, `bumpDispatchCount`, `boolColumn`) ·
`internal/weaver/engine.go` (Config buckets, `targetSource` core-kv read) · `docs/components/weaver.md`
(Dispatch suppression, Reconciler sweep, Actuator) · `lattice-architecture.md` (P2/P5, data-placement D5) ·
CLAUDE.md (P5 allowed-direct-read platform binaries; no-history-comments).
