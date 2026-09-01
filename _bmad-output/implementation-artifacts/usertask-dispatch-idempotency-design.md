# Design — userTask dispatch idempotency (stop duplicate human tasks)

**Status:** ✅ CLOSED — code built (2026-06-26) and the Contract #10 §10.3 amendment ratified + committed in the 2026-08-25 batch (`8d039bdb`, 2026-08-26); nothing pending. ALL code is implemented + 3-layer
reviewed + gates green: the §2.5 lazy `kv.Read()` keystone (§4.4 / §8 step 3, keystone commit `3a35941`
on main), the Weaver `claimId` mint/preserve (§3, §8.1), the `deriveStable*` derivations (§8.2), the
`CreateTask` `kv.Read` branch + `StartLoomPattern` optional `instanceId` (§8.3–8.4), and the interim-cap
revert (§8.5). Enforcement is **consumer-side** (Processor/Loom), not a producer-side GET — see §4.3.
The Contract #10 §10.3 amendment (§7) is **ratified and committed** (`8d039bdb`; the clause now
lives in `10-orchestration-substrate.md`, restated at promise altitude by the 2026-08-31 posture sweep).
**Refinement found at build (vs §4.4):** `StartLoomPattern`'s dedup is **at Loom**, not a Processor
`kv.Read` — the Loom instance lives in **loom-state**, not Core KV, so a Processor read can't reach it.
`triggerLoom` instead carries the stable `claimId`-seeded `instanceId`, and Loom's `getInstance`
presence + `createInstance` `CreateOnly` collapse the re-emitted `patternStarted`. Same idempotency
guarantee, correct tier. Only `assignTask`→`CreateTask` (a Core-KV task) uses the `kv.Read` branch.
**Author:** Winston (Steward, 2026-06-25; built 2026-06-26). **Frozen-contract touches:** Contract #10
§10.3 (§7) only — ratified + committed (`8d039bdb`). Contract #2 §2.5 was already specified —
implementing it was a conformance fill (landed). The interim `maxretries=1` cap is now **reverted** (the
general fix supersedes it).

---

## 1. Problem

Weaver re-creates **human userTasks** indefinitely. For a lease application a person has not yet
completed, the Tasks inbox accumulates a fresh `RecordIdentityPII` and `SignLease` every ~30 minutes
(found live by Andrew; the API showed 3× each at 30-min intervals).

Root cause is the §10.3 reconciler **mark lease**. Weaver records a per-gap suppression mark with a
30-minute lease (`defaultMarkLease`, [reconciler.go:17](../../internal/weaver/reconciler.go)); when the
lease expires the sweep presumes the dispatched work dead and **re-dispatches** the gap action
([reconciler.go:231](../../internal/weaver/reconciler.go) `reclaim`), gated only by `gapSuppressed`
(skip if `inflight_<g>` set **or** the dispatch-count reached `maxretries_<g>`,
[evaluator.go:381](../../internal/weaver/evaluator.go)). The two external gaps (bgcheck/payment) are
protected by their `inflight_<g>` companion; the two **human** userTask gaps (`missing_onboarding`,
`missing_signature`) have **no suppression companion**, so each lease expiry re-creates their task.

The re-fire itself is **by design**: the dispatched identity is **episode-scoped on `markRevision`**,
which changes every reclaim —
`deriveEpisodeTaskID(targetID, entityID, gapColumn, markRevision)`
([actuator.go:140](../../internal/weaver/actuator.go), [strategist.go:152](../../internal/weaver/strategist.go)) —
and `triggerLoom`/onboarding passes **no** instance id at all
([strategist.go:109](../../internal/weaver/strategist.go)), so Loom mints a fresh instance (hence a fresh
task) per `StartLoomPattern`. Each reclaim is intentionally a **new episode**.

§10.3 already names this: a re-fired `triggerLoom`/`assignTask` is an *"accepted rare double (lease ≫
remediation latency makes it rare … a duplicate task is operator-visible) — documented bound … the robust
**check-before-act** variant is a **Phase-3 hardening**."*

**The defect is the assumption, not the code.** *"Lease ≫ remediation latency"* holds for **machine**
remediation (an external call resolves in seconds, far inside the 30-min lease — re-fire is genuinely
rare). It is **false for a human**: a person legitimately takes hours-to-days, *far longer* than the
lease, so for human-paced gaps the "rare double" is a **guaranteed ~30-min duplication**. The model
under-specified human-paced gaps. It is **general** — every userTask gap in every package (clinic
included) inherits it.

This is the deferred Phase-3 "check-before-act" hardening, now forced concrete.

---

## 2. Principle

The duplicate is the one place orchestration mints a **non-deterministic** (episode-scoped) identity
where a userTask wants a **stable** one. Everywhere else, at-least-once delivery is made safe by
deterministic identity collapsing on an existing record:

- the Contract #4 tracker `vtx.op.<requestId>` (re-submit collapses),
- Loom's userTask `taskId` from `(instanceId, cursor)` (§10.6 crash-retry collapses),
- the bridge's external dedup on the deterministic service-instance `instanceKey`.

So the fix is not a new mechanism — it makes the reconciler obey the **same idempotency philosophy** as
the rest of the platform: a userTask dispatch carries a **deterministic, per-open-episode identity**, and
re-dispatch is **idempotent** against it.

---

## 3. The key: revive the dormant `claimId` as the per-episode token

A purely `(targetId, entityId, gapColumn)` key has a correctness hole: it cannot distinguish a *fresh*
open-episode from a *completed-then-reopened* one (a freshness-reopening gap would collapse on the old
completed task and never re-create). We need an identity **stable across reclaims** but **fresh across a
legitimate close→reopen**.

That token already exists, dormant and purpose-built. The reconciler `mark` carries `ClaimID`
([state.go:78](../../internal/weaver/state.go)) — minted atomically with the CAS-create as a per-episode
identity for the old nudge's idempotency, orphaned when the nudge was retired (13.1, 2026-06-18); §10.3
records it as *"left optional … no remaining producer."* It is the exact hook, because of **when** it is
minted:

- **Mint `claimId` at the mark's CAS-create** — the start of an open-gap episode (a fresh NanoID).
- **Preserve `claimId` across reclaims** — the reconciler `replace` keeps it; only the lease/`claimedAt`
  refresh.
- **Derive the userTask identity from it** — `deriveID("task:", targetID+entityID+gapColumn+claimId, 0)`.

→ **stable across reclaims** (no duplicate while the human takes their time), **fresh across reopen** (a
new mark ⇒ new `claimId` ⇒ a fresh task). `markRevision` is too fresh (changes per reclaim); a blind
triple is too stable (misses reopen). `claimId` is exactly right.

---

## 4. Mechanism — both userTask paths

Two gap actions produce human tasks; both switch from `markRevision`- to `claimId`-derived identity.
**Nothing else changes** (see §5 scope).

### 4.1 `assignTask` (the SignLease task)

- Strategist derives the task id from the episode token:
  `taskId = deriveStableTaskID(targetID, entityID, gapColumn, claimId)` (a new helper alongside
  `deriveEpisodeTaskID`; folds `claimId` into the `deriveID` seed, [actuator.go](../../internal/weaver/actuator.go)).
  The `payload` closure already receives the mark context at plan time
  ([strategist.go:146](../../internal/weaver/strategist.go)); thread `claimId` through instead of the bare
  `markRevision`.

### 4.2 `triggerLoom` (the onboarding pattern → RecordIdentityPII task)

- Strategist passes a **deterministic instance id** to `StartLoomPattern`:
  `instanceId = deriveStableInstanceID(targetID, entityID, gapColumn, claimId)`
  ([strategist.go:109](../../internal/weaver/strategist.go) — today the payload is
  `{patternRef, subjectKey, expectedRevision}` with no instance id).
- `StartLoomPattern` honours a caller-supplied `instanceId` (verbatim if present, minted if absent —
  mirrors `CreateTask`'s optional `taskId`, §10.6). A re-trigger for the same open-episode collapses on
  the existing `instance.<instanceId>` (CreateOnly) → no new Loom instance → no new task. This dedups the
  whole pattern, not just its task — the correct altitude for `triggerLoom`.

### 4.3 The act — enforce idempotency on the CONSUMER (Processor), not a producer-side GET

A producer-side "GET the task; skip if present" probe (in Weaver's reconciler) is **race-prone, and the
race is structural**: a dispatch is *published to `core-operations` and committed to Core KV
asynchronously*, so between Weaver publishing `CreateTask` and the task appearing in Core KV there is a
propagation window. A second reclaim (or the lane-1 path) that GETs Core KV inside that window sees
**absent** and re-publishes — two `CreateTask`s now race in the queue. The producer GET does not *prevent*
the double-publish; only the **consumer** (the Processor, committing against actual Core-KV state) can
decide create-vs-collapse atomically. So enforcement belongs at the Processor.

**This is exactly how Loom already survives the same lag** (§10.6): Loom never relies on a producer GET
to gate re-publishing. It (a) keys the task on a **deterministic** `(instanceId, cursor)` id, (b) writes
the op **write-ahead to its outbox** and relays it once, (c) guards on the **pendingToken** (a redelivered
trigger *"finds the cursor present"* and drops, [engine.go:398](../../internal/loom/engine.go)), and (d)
its creation-deadline probe only ever **disarms-on-present** — *"once onDeadline's probe confirms the task
vertex exists, it disarms"* ([engine.go:903](../../internal/loom/engine.go)); it **never blindly
re-publishes on absent**. The net effect: any re-publish **collapses** on the deterministic id at the
Processor (the Contract #4 tracker / `CreateOnly`), never duplicates. Loom doesn't duplicate because it
**never re-episodes** — Weaver's reconciler intentionally does (a fresh `markRevision` per reclaim), which
is right for *external* retries but wrong for human tasks.

So the fix moves Weaver's userTask dispatch onto the same footing: **deterministic `claimId`-derived
identity (§3) + the Processor as the single idempotency authority.** With a deterministic `taskId`, a
re-published `CreateTask` is decided at commit against real Core-KV state. The remaining question is only
*how the Processor expresses "already there → fine"* without noise:

- **`create` today is `CreateOnly`** ([step8_commit.go:154](../../internal/processor/step8_commit.go)) —
  a re-publish **rejects** (no duplicate, but a rejected op per lease = operator-noise).
- A `contextHint.reads` declaration of the maybe-absent task key causes a fatal `HydrationMiss`
  ([step4_hydrate.go:152](../../internal/processor/step4_hydrate.go)) if the key is absent — the hint
  path cannot express "read this key, tolerate absence."

Contract §2.5 resolves this cleanly. It specifies that `contextHint.reads` is a **pre-fetch
optimisation, not a gate**: *"When absent: Processor uses lazy on-demand reads during Starlark
execution; Each `kv.Read()` call from Starlark performs a Core KV fetch."* Absent returns `None`
gracefully, not a fatal error. This lets the **script itself decide coherently**: present → return
`{"mutations": [], "events": []}` (silent no-op, mutations and events suppressed together); absent →
emit `create` (CreateOnly) mutations + events as normal. Events are under script control so the outbox
is always consistent — this is the problem a `createIfAbsent` mutation cannot solve (mutations may
no-op at commit but events are already in the outbox, firing a phantom `taskCreated` for a task that
was not created). The `CreateOnly` backstop remains the concurrent-race guard for the narrow window of
two dispatch ops arriving in the same commit frame. **No new mutation type needed.** The "one new
Processor affordance" is **implementing the §2.5 lazy `kv.Read()` builtin** (§4.4) — specified but
absent from the current code.

### 4.4 The affordance — implement §2.5 lazy `kv.Read()` (contract/code gap)

Contract §2.5 (*"Context Hint Semantics"*) already specifies this mechanism:

> **When absent [from `contextHint.reads`]:** Processor uses lazy on-demand reads during Starlark
> execution; Each `kv.Read()` call from Starlark performs a Core KV fetch; Per-operation latency
> increases proportional to read count.

The current implementation does **not** wire this up. `starlark_runner.go` exposes only the
pre-hydrated `state` map, `op`, `ddl`, `nanoid`, `crypto`, `time`, and `json` — no `kv` module.
`step5_execute.go`'s `Execute` receives `HydratedState` but no `Conn`. This is a **contract/code gap**:
`kv.Read()` is specified but absent from the implementation.

**The fix:** thread a live Core-KV connection into `ScriptContext` (or `StarlarkRunner`) and expose a
`kv` module: `kv.Read(key)` performs a single Core-KV GET and returns the value dict, or `None` if
absent / hard-tombstoned. With this, the `CreateTask` Starlark script branches before emitting anything:

    task = kv.Read("vtx.task." + taskId)
    if task != None and not task.isDeleted:
        return {"mutations": [], "events": []}   # already exists and alive — silent no-op
    # ... normal create path: vertex + links + grant + taskCreated event

> **Branch on `isDeleted`, not just `None`.** `kv.Read()` returns `None` only for an absent or
> *hard*-tombstoned key (NATS delete/purge/TTL-expiry). A *logically*-deleted vertex (`isDeleted=true`)
> is still a live KV envelope, so `kv.Read()` returns it as a **present doc carrying the flag** — exactly
> as `state[...]` surfaces logical deletes. The script must therefore treat *present-and-deleted* as
> "needs (re)creating" (`task == None or task.isDeleted`); a bare `if task != None` would wrongly suppress
> re-creation of a cancelled-but-still-open task and break the self-heal property below. The keystone
> primitive (now built) surfaces `isDeleted` precisely so the script can make this call.

This is a **conformance fill**, not a new surface: §2.5 already authorises `kv.Read()`; the Processor
needs to implement it. No `createIfAbsent` mutation type. No Contract #3 §3.2 amendment.

**Why kv.Read() beats createIfAbsent here:**

- **Event coherence.** Mutations and events are committed in the same atomic batch
  ([step8_commit.go:177](../../internal/processor/step8_commit.go)). `createIfAbsent` lets the Processor
  skip mutations at commit — but the script has already emitted `taskCreated` events, which land in the
  outbox unconditionally (there is no post-hoc event filter). With `kv.Read()` the script decides both
  mutations AND events in one branch: absent → create + event; present → empty return, no event.
- **No new mutation vocabulary.** `create` / `update` / `tombstone` suffice; no Contract #3 §3.2
  amendment needed.
- **Spec alignment.** §2.5 already blesses this pattern; implementing it closes an existing gap rather
  than opening new contract surface.

**Deleted tasks self-heal — for BOTH delete kinds, via the `isDeleted` branch.** A *hard*-tombstoned key
(NATS delete/purge/TTL-expiry) reads as `None`; a *logically*-deleted key reads as a present doc with
`isDeleted=true`. Because the script branches on `task == None or task.isDeleted`, **either** kind routes
to the create path. If a task was deleted (either way) but its gap is still open, Weaver's re-dispatch
re-creates it — the correct outcome (the work still needs doing; a cancellation that resolves the gap
closes it and stops re-dispatch). Branching on `None` alone would self-heal only the hard-delete case and
silently strand a logically-cancelled-but-open gap — hence the `isDeleted` clause is load-bearing, not
defensive.

**Op name stays `CreateTask`.** The caller's intent and DDL surface are unchanged. Idempotency is a
safety property, not a different operation.

> **The deterministic `requestId` alone is not enough.** The Contract #4 tracker has a **24h TTL**
> ([step8_commit.go:173](../../internal/processor/step8_commit.go)); a human routinely exceeds 24h, after
> which the tracker is gone and the re-dispatch runs. The durable guard must live at the **`taskId`**
> level (the `kv.Read()` script branch), not only the requestId/tracker.

---

## 5. Scope — surgical, userTask-only

- Only `assignTask` and `triggerLoom` switch to `claimId`-derived identity. **External gaps keep
  `markRevision` episode-scoping** — their reclaim-retry is *intended* (re-call a dead vendor / mint a
  fresh service instance) and already bounded by `inflight_<g>` + `maxretries_<g>`. `directOp` likewise
  unchanged.
- **No package code** — the fix is entirely in Weaver core + the two generic orchestration ops, so it
  fixes every userTask gap in every package (clinic included) with zero per-package work.
- `directOp`'s `reads`-injected `expectedRevision` and the external `inflight`/budget machinery are
  untouched.

---

## 6. Correctness properties

| Property | `maxretries=1` (interim cap) | producer-side GET probe | **`claimId` id + §2.5 `kv.Read()` script branch (this design)** |
|---|---|---|---|
| No duplicate while task open | ✅ | ⚠️ races the publish→commit lag | ✅ script sees present → empty return |
| Race-free under propagation lag | ✅ | ❌ GET sees absent mid-lag → double-publish | ✅ script reads at execution, CreateOnly backstop at commit |
| Re-create after legit close→reopen | ✅ (count resets) | ⚠️ | ✅ fresh `claimId` ⇒ fresh task |
| Self-heal a task lost out-of-band | ❌ never re-creates | ⚠️ | ✅ absent/tombstoned ⇒ create |
| No rejected-op noise per lease | ✅ (suppressed) | ⚠️ | ✅ script returns empty mutations+events |
| Events coherent with mutations | ✅ (never fires) | ⚠️ | ✅ script branches both together |
| General (all packages) | ❌ per-package columns | ✅ | ✅ |

The interim cap is create-once-*forever* (never recovers a lost task) and is per-package; this design is
reopen-correct, self-healing, and general. **The cap becomes redundant and is reverted as part of this
fix** (the `maxretries_onboarding`/`maxretries_signature` columns + the `retry_budget.go` constants).

---

## 7. Contract impact — §10.3 amendment (Andrew ratification)

Proposed replacement for the §10.3 *"Re-fire after lease expiry — idempotency by action"* bullet (do
**not** edit the frozen file until ratified):

> **Re-fire after lease expiry — consumer-enforced idempotency by deterministic episode identity.** A
> userTask reclaim is keyed by the **open-episode identity**: the mark's `claimId` (minted at the mark's
> CAS-create, **preserved** across reclaims) seeds the dispatched artifact's id — `assignTask`'s `taskId`
> and `triggerLoom`'s Loom `instanceId`. Weaver re-publishes the dispatch **without** a producer-side
> existence check (which would race the publish→commit propagation lag); the **Processor** is the single
> idempotency authority — the `CreateTask` / `StartLoomPattern` Starlark script reads the task key via
> `kv.Read()` (§2.5 lazy on-demand read, conformance fill) and branches on present-and-alive → empty
> mutations + empty events (silent no-op); absent/tombstoned → create as normal; the existing `CreateOnly`
> backstop handles the narrow concurrent-dispatch race. A legitimate close→reopen mints a new mark ⇒ new
> `claimId` ⇒ a fresh artifact; an out-of-band deletion self-heals (tombstoned ⇒ `kv.Read()` returns
> `None` ⇒ create). This **supersedes** the "accepted rare double / check-before-act = Phase-3 hardening"
> disposition for the two human userTask actions; external gaps retain episode-scoped (per-reclaim)
> dispatch, bounded by `inflight_<g>` + `maxretries_<g>`.

The §10.3 mark value-shape note — `claimId` regains a producer (CAS-create) + consumer (id derivation);
strike *"left optional … no remaining producer."*

The only frozen-contract touch is **Contract #10 §10.3** (above). Contract #2 §2.5 already specifies
`kv.Read()` — implementing it is a code-gap fill, not a new amendment. The mechanism, scope, and identity
design (§3–§6) are Winston-ratified; the §10.3 amendment is Andrew's to ratify on the fresh run.

---

## 8. Implementation plan (post-ratification)

1. **Mint + preserve `claimId`** — set `ClaimID` (fresh NanoID) on the mark CAS-create in `fireEpisode`
   ([evaluator.go:222](../../internal/weaver/evaluator.go)); preserve it in the reconciler `replace`
   ([reconciler.go:310](../../internal/weaver/reconciler.go) → the `marks.replace` seam) — only
   lease/`claimedAt`/`heldBy` refresh.
2. **Stable id derivation** — `deriveStableTaskID` / `deriveStableInstanceID` (claimId-seeded) next to
   `deriveEpisodeTaskID` ([actuator.go:140](../../internal/weaver/actuator.go)); use them in the
   `assignTask` and `triggerLoom` plan branches ([strategist.go:104](../../internal/weaver/strategist.go),
   [strategist.go:152](../../internal/weaver/strategist.go)). Thread `claimId` into the plan `payload`
   closure (it already takes the mark context).
3. **Implement §2.5 lazy `kv.Read()` (§4.4)** — add a `kv` module to `StarlarkRunner`'s globals backed
   by a live Core-KV connection threaded into `ScriptContext` (or `StarlarkRunner`). `kv.Read(key)` →
   value dict or `None` if absent/tombstoned. Update `CreateTask`'s Starlark script to call
   `kv.Read("vtx.task." + taskId)`: if non-`None` return `{"mutations":[],"events":[]}` (present →
   silent no-op); if `None` proceed with the normal create path. `StartLoomPattern` likewise checks its
   `instance.<instanceId>` key. `CreateOnly` remains the concurrent-race backstop. Keep
   `TestCreateTaskReads_MatchDDLScript` / DDL drift-guards in lock-step.
4. **`StartLoomPattern` gains an optional caller-supplied `instanceId`** (verbatim if present, minted if
   absent — mirrors `CreateTask`'s `taskId` seam). The strategist threads the `claimId`-derived
   `instanceId` through the `triggerLoom` plan payload. **No producer-side GET in Weaver** — it
   re-publishes the deterministic-id dispatch; the script's `kv.Read()` at the Processor decides
   create-vs-no-op. (Lane-1's first dispatch is unchanged beyond minting `claimId` + the deterministic
   id; the mark CAS-create already throttles lane-1 re-dispatch, §10.8.)
5. **Revert the interim cap** — drop `maxretries_onboarding`/`maxretries_signature` from
   `packages/lease-signing/{lenses.go,retry_budget.go}` and the `UserTaskDispatchCaps` test.

## 9. Test plan

- **Weaver unit:** a reclaim of a userTask gap with a preserved `claimId` derives the **same** `taskId`
  (vs `markRevision` deriving a different one today); the reclaim op is idempotent (present → no-op).
- **`claimId` lifecycle:** minted on CAS-create, preserved across `replace`, gone on gap-close
  (`clearClosedMarks`); a fresh mark after reopen mints a **new** `claimId` (fresh task id).
- **Consumer-side idempotency:** `CreateTask` / `StartLoomPattern` given a deterministic key whose vertex
  already exists-and-is-alive: script's `kv.Read()` returns the value → script returns empty mutations +
  empty events (no second vertex, no event, no rejection); absent/tombstoned → `kv.Read()` returns `None`
  → creates normally. Drive at the Processor (script-execution) level via the `kv.Read()` branch.
- **Lag race (the load-bearing case):** two `CreateTask`s with the **same** deterministic `taskId` arriving
  back-to-back (the publish→commit window) commit to **one** task — the second no-ops/collapses. This is the
  scenario a producer GET cannot cover.
- **Integration / heavy e2e:** drive a lease application, leave the userTasks open across ≥1 mark lease
  (a shortened `MarkLease` test config — see §10), assert **exactly one** `RecordIdentityPII` + one
  `SignLease` persist; then complete one and confirm reopen behaviour where applicable.
- **No-regression:** external gaps still re-dispatch on reclaim (episode-scoped), bounded by
  `inflight`/`maxretries`.

## 10. Risks / open items (Winston-resolved unless noted)

- **`MarkLease` not test-configurable.** The heavy e2e needs a short lease to observe a reclaim within
  wall-clock; `MarkLease`/`SweepInterval` are config fields ([engine.go:57](../../internal/weaver/engine.go))
  but `cmd/weaver` exposes no env for them. *Resolution:* the **engine config already carries them** — the
  test harness sets them directly; no `cmd/weaver` env needed for the test (a separate, optional
  operability nicety, not part of this fix).
- **Enforcement altitude (consumer, not producer) — Andrew's call, 2026-06-25.** A producer-side GET is
  rejected because it races the publish→commit propagation lag (§4.3): inside the window the GET sees
  absent and re-publishes, so it never *prevents* a double-publish — only the Processor, committing against
  real Core-KV state, can. Cost: one new Processor affordance (§4.4); benefit: it generalizes (every
  idempotent-create op stops hand-rolling the pre-read dance). The reads-miss-is-fatal rule
  ([step4_hydrate.go:152](../../internal/processor/step4_hydrate.go)) is *why* an op cannot just
  read-before-create today — the affordance is what removes that wall.
- **§2.5 `kv.Read()` implementation scope.** Threading a live Core-KV connection into `ScriptContext` /
  `Execute` is modest but non-trivial: the `Execute` signature currently takes only `HydratedState`, no
  `Conn` (see [step5_execute.go:29](../../internal/processor/step5_execute.go)). `kv.Read()` in a script
  is a per-call NATS round-trip; document it as an intentional opt-in for the idempotency-read pattern,
  not a general KV scan hook (prevent script authors from treating it as a read model). The empty-mutations
  return path already succeeds (step8_commit.go:183 writes no outbox for zero events; an empty mutation
  batch is not a special case). `CreateOnly` remains the concurrent-race backstop.
- **`StartLoomPattern` idempotency interaction with Loom's own deterministic `taskId`.** Once the Loom
  *instance* id is stable, Loom's `(instanceId, cursor)` task id is automatically stable too — the two
  compose; no separate Loom-task change needed.
- **Adversarial review** (pre-mortem): the one true hazard is a stable id colliding across the
  close→reopen boundary — closed by minting `claimId` at CAS-create (a reopen is a *new* mark). The
  second is an idempotent op masking a real second-task need — there is none for these gaps (assignee ==
  scopedTo == subject, §10.5; one open task per gap is the invariant). Recommend a full party + 3-layer
  adversarial pass on the **built** change before admit (Weaver core + a frozen-contract amendment clears
  the L+ bar). **Done** — 3-layer adversarial review ran on the built change (Blind / Edge / Acceptance):
  verdict CONFORMS / ship-ready, no Critical/High correctness defects. Findings actioned: the
  `StartLoomPattern` `oneOf` schema made unambiguous (`additionalProperties:false`); the assignTask
  taskId reclaim-stability proof added (matching the triggerLoom one); the migration + self-heal-asymmetry
  notes below + in the §10.3 amendment.
- **Migration bound (build-time finding).** A userTask gap **already in flight at deploy** has a
  pre-`claimId` mark (`claimId==""`). Its first post-deploy reclaim derives a stable *empty-seed* id that
  differs from the id the pre-deploy dispatch used, so it may create **one** duplicate — bounded,
  one-time, self-healing (later reclaims reuse the empty-seed id and collapse). Strictly better than the
  every-30-min duplication this fixes; a pre-deploy drain of open human-task gaps avoids even the one.
  Documented in the §10.3 amendment.
- **Deferred (nice-to-have, not an admit blocker).** A heavy end-to-end "drive a real lease application,
  leave the userTasks open across ≥1 mark-lease reclaim through the REAL Processor commit, assert exactly
  one `RecordIdentityPII` + one `SignLease` persist" test (§9 integration item). The mechanism is covered
  by: the Weaver-level two-reclaim stability proofs (assignTask `taskId` + triggerLoom `instanceId`), the
  claimId lifecycle test, the CreateTask present→no-op / deleted→self-heal script tests, and the existing
  `CreateOnly` commit coverage. The full real-commit-across-a-reclaim e2e remains the gold standard to add.

---

## 11. Sequencing — a fresh Steward run (Andrew, 2026-06-25)

1. Andrew ratifies the §7 §10.3 amendment — or redirects. The §2.5 lazy `kv.Read()` mechanism is a
   conformance fill (§2.5 already authorises it); no amendment needed beyond §10.3.
2. Apply the §10.3 contract edit to `docs/contracts/10-orchestration-surfaces.md` (Andrew's edit / on his nod).
3. Build §8: implement §2.5 `kv.Read()` first (the keystone — threads the Conn into ScriptContext and
   wires the kv module), then the Weaver `claimId` mint/derive, then `CreateTask`/`StartLoomPattern`
   using it. Party + 3-layer adversarial review (Processor execution path + Weaver core + one frozen
   contract = clearly L+); gates green incl. `make verify-package-*` for any touched op DDL.
4. Revert the interim cap (§8.5) in the same change.
5. Commit direct to main, CI green.

Until then: the interim `maxretries=1` cap stays (duplication is stopped for lease-signing); other
packages' userTask gaps remain exposed to the 30-min duplication until this general fix lands.
