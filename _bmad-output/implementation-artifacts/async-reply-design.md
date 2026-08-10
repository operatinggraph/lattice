# Asynchronous external-reply — design proposal

**Status:** 📋 Design proposal for team review (NOT yet a build brief). Implements backlog row
**"Real adapters + async result-return"** (External-I/O maturity, ★★ M–L) and subsumes the
**"re-tuned wedged-claim horizon"** sub-item.

**Prerequisite / continuity:** the in-flight **terminal-failed adapter outcome** item
(`structured-adapter-result-design.md`) is a clean prerequisite — async defers *when* the
`status ∈ {completed, failed}` verdict is determined, not its shape. Same `.outcome` aspect, same
replyOp; async just posts the replyOp *later*, from a resolver, instead of inline.

---

## The thesis — only one link is synchronous

The externalTask chain is **already asynchronous everywhere except the adapter call**:

- **Loom already dispatches-and-parks.** `internal/loom/doc.go:39` — the externalTask is *"never a
  synchronous submit-reply (§10.6)."* Loom writes a write-ahead `token.<handle>`, parks on it, and
  correlates `orchestration.externalTaskCompleted{externalRef}` to close it (`token.go`). The token
  doesn't care whether the reply lands in 50 ms or 50 hours.
- **Loom already has a deadline backstop.** The "deadline watcher / stuck-instance backstop"
  (`control.go:58,221`) fails an instance that never completes.
- **The bridge's `adapter.Execute(ctx, req) (Result, error)` is the lone synchronous step** — it MUST
  return a final `Result` inline, and the bridge posts the replyOp immediately.

Real vendors (background checks, payments, KYC, doc-verification) are *submit → pending ref → result
hours/days later via webhook or status-poll*. So the entire job is: **make the adapter call async, and
drive the eventual resolution** — without re-architecting Loom (it already waits) or inventing a new
wait primitive (core-schedules already is one).

---

## Component spine — each for what it is good for

| Component | Role in async | Why it fits | Change |
|---|---|---|---|
| **Loom** | The **async waiter**. Parks `token.<handle>`, waits for `externalTaskCompleted`. | Already dispatch-and-park; already deadline-backstopped. | **None** to the wait. Only its deadline horizon is re-tuned (see §timeout). |
| **core-schedules** | The **temporal driver** — fires the *poll* and the *give-up timeout* at wall-clock instants. | Exactly Weaver's proven lane-3 pattern: `freshUntil` → `@at` schedule → `fired.>` consumer → idempotent op, recovery via a fixed durable's ack-floor (`weaver/temporal.go`). One-shot `@at` is what Phase-2 ships — a self-rescheduling `@at` chain gives recurrence with **no `@every` dependency**. | The bridge becomes a **second core-schedules client** alongside Weaver (new subject namespace — contract flag below). |
| **Weaver** | The **convergence/gap policy**. Learns the *pending* state so it does **not** re-dispatch an in-flight call; escalates when a call resolves *failed* or times out. | Weaver already is the "ensure-eventually / re-trigger on gap" engine (triggerLoom on a violating gap). Pending-awareness is one new clause in the gap predicate. | Gap predicate gains a pending-suppression clause; the lens projects the pending dimension. |
| **the bridge** | The **external boundary** — split into *egress (submit)* and *ingress (resolve)*. Owns the adapter, the poll loop, and the timeout. | It is the only thing that talks to vendors; it already stays **stateless** (no outbox/cursor — `actuator.go`), which *forces* the pending state into the graph — a feature: observable, crash-safe, Weaver-visible. | New: async `Execute`/`Poll` contract, a pending-marker op, a temporal consumer (mirrors `weaver/temporal.go`). |

---

## Claim-vertex lifecycle (the state lives in the graph)

`vtx.service.<handle>` carries **two distinct aspects** — they must be separate because `.outcome` is
**create-only** (the FR58 once-only guard), so "pending" cannot be a transient `.outcome.status`:

```
              CreateLeaseServiceInstance (exists)
 (none) ─────────────────────────────────────────▶ instance, no aspects
                                                          │  external.<adapter> event
                                                          ▼
                                    bridge: adapter.Execute(req)
                   ┌──────────────────────────┬───────────────────────────┐
            Resolved (sync/fast)        Pending (async)                error (transient)
                   │                          │                            │
   post RecordLeaseServiceOutcome   post RecordServiceDispatch        NakWithDelay
        {status}  (TODAY's path)    {vendorRef, deadline, nextPollAt}  (redeliver event)
                   │                  → .dispatch aspect (NEW)
                   ▼                  + arm schedule.bridge.poll@nextPollAt
              .outcome written          + arm schedule.bridge.timeout@deadline
              externalTaskCompleted     (NO .outcome yet — token stays parked)
                                                   │
                          ┌────────────────────────┼─────────────────────────┐
                  poll fires (core-sched)   timeout fires (core-sched)   webhook (Phase B)
                          │                         │                         │
                 adapter.Poll(vendorRef)   no .outcome yet?            resolve(ref, Result)
                   ├ Resolved → post outcome   → post outcome{failed}  → post outcome
                   └ stillPending → re-arm        (terminal give-up)
                       poll@backoff
                                                   ▼
                                       .outcome written → externalTaskCompleted
                                            → Loom closes token.<handle>
```

`.dispatch` (the pending marker) and `.outcome` (the terminal result) are written by **two package ops**;
the free-form `result` / PII handling and `externalTaskCompleted` emit are unchanged from today.

---

## Adapter contract change

```go
// Disposition is the adapter's verdict on a dispatch or a poll.
type Disposition int
const (
    Resolved Disposition = iota // terminal: Result is final (sync adapters always return this)
    Pending                     // submitted; resolve later via Poll or webhook
)

type Outcome struct {
    Disposition Disposition
    Result      Result    // valid when Resolved (carries the {completed,failed} status from the failed-producer item)
    Ref         string    // vendor reference, valid when Pending — the opaque poll/webhook key
    NextPollAt  time.Time // optional adapter hint; bridge applies a default backoff if zero
    Deadline    time.Time // optional vendor SLA; bridge applies a default horizon if zero
}

type Adapter interface {
    Execute(ctx, req) (Outcome, error) // error stays transient-retry (redeliver the event)
    Poll(ctx, ref)    (Outcome, error) // Resolved → post reply; Pending → not yet; error transient
}
```

A terminal failure is `Outcome{Resolved, Result{Status: failed}}` with `err == nil` (errors remain
*transient retry*, never a business verdict). **Sync adapters are trivial**: `Execute` returns
`Resolved` and `Poll` is never reached (or returns `Resolved` defensively) — today's fakes barely
change. A new `fakeAsyncCheck` returns `Pending` once, then `Resolved` after N polls, exercising the
whole path with no infrastructure.

---

## Temporal machinery — mirror Weaver lane-3, do not reinvent

The bridge gains its own lane-3, structurally identical to `weaver/temporal.go`:

- **Arm (egress, on Pending):** the bridge actuator publishes `@at` schedules
  `schedule.bridge.poll.<handle>` @`nextPollAt` and `schedule.bridge.timeout.<handle>` @`deadline`.
  One-schedule-per-subject **replace** ⇒ a redelivered event re-arms idempotently (exactly Weaver's
  `scheduleFreshness` posture). A past instant fires immediately (correct level semantics).
- **Fire (ingress):** a supervised durable consumer filtered to `schedule.bridge.*.fired.>` (a fixed
  durable, like `weaver-temporal` — its ack-floor is the missed-while-down recovery). On a poll firing
  → `adapter.Poll`; on a timeout firing → give-up. Deterministic requestId (subject + fire instant) ⇒
  at-least-once redelivery collapses on the Contract #4 tracker.
- **Read-before-act:** before polling/timing-out, re-read the claim's `.outcome` — if already resolved
  (a racing webhook, a prior poll), Ack without acting. Mirrors Weaver's `handleFiredTimer` staleness
  guard and the bridge's existing `resultAlreadyLanded` skip-probe.

This is the strongest reason to be confident: it is a **proven, in-production-in-this-repo pattern**,
re-applied — not a new mechanism.

---

## The two timeout layers (the "re-tuned wedged-claim horizon", item c)

There are **two** give-up horizons and they must be ordered:

1. **Bridge poll-timeout** (`schedule.bridge.timeout`, per-claim, from the adapter's `Deadline`/vendor
   SLA): on expiry with no `.outcome`, the bridge posts `RecordLeaseServiceOutcome{status: failed}` (or
   a distinct `timedOut` — open decision) — a **graceful** terminal resolution that closes the token
   cleanly and lets the lease-app converge to a definite negative state.
2. **Loom deadline-watcher** (`control.go`, per-instance, the stuck-instance backstop): the **longstop**.

**The re-tune:** today Loom's deadline horizon assumes a fast synchronous reply; a legitimately-pending
48 h check would trip it as "stuck." So Loom's externalTask deadline must be **per-adapter / longer than
the vendor SLA**, and the bridge poll-timeout must fire **strictly before** it — so the normal path is a
clean bridge-posted `failed`, and Loom's watcher only fires if the *bridge itself* is dead (the genuine
"wedged" case). i.e. **bridge-timeout = the SLA give-up; Loom-deadline = the backstop for a dead bridge.**
This ordering is the heart of item (c).

---

## Idempotency & crash-safety

- **No double-submit:** the `.dispatch` marker is write-ahead; on event redelivery the bridge runs an
  `alreadyDispatched` skip-probe (read `.dispatch`) and resumes polling instead of re-calling the vendor.
  Belt-and-suspenders: the adapter's `idempotencyKey` (instanceKey) makes a re-submit a vendor-side no-op.
- **No double-resolve:** posting the replyOp is already idempotent (deterministic requestId + create-only
  `.outcome`). A webhook racing a poll, or a poll racing the timeout, both post the same op → collapses.
- **Timeout vs late success race:** create-only `.outcome` ⇒ **first writer wins.** Once timed-out =
  failed, even if a late success arrives (a "re-open" flow is explicitly out of scope; note it).
- **Recovery:** bridge restart → the fixed temporal durable resumes from its ack-floor (missed firings
  replay); the `.dispatch` markers are the authoritative pending set.

---

## Weaver gap-state machine (the one Weaver change)

The gap predicate gains a pending dimension (the lens projects `.dispatch`):

| `.dispatch` | `.outcome` | deadline | Weaver verdict |
|---|---|---|---|
| absent | absent | — | **missing** → trigger (dispatch the externalTask) |
| present | absent | not passed | **pending / in-flight** → **WAIT** (do NOT re-trigger) |
| present | absent | passed | **wedged** → the bridge-timeout should already be resolving it; Weaver escalates if not |
| any | present, `completed` | — | **satisfied** |
| any | present, `failed`/`timedOut` | — | **unsatisfied** → normal escalation policy (human nudge / policy re-dispatch — never a silent auto-resubmit of the same vendorRef) |

The single critical new behaviour: **pending-suppression** — never dispatch a second external call while
one is legitimately in flight.

---

## Inbound mechanism — poll first, webhook as a drop-in later

`resolve(handle, Result)` is the **one seam** both drivers converge on (it posts the replyOp). Design it
once; the driver is pluggable:

- **Poll via core-schedules (recommended Phase-3 primary).** No inbound HTTP surface, works behind NAT,
  exercises core-schedules (the lane Andrew named). Latency = poll cadence — fine for hours-scale checks.
- **Webhook (Phase B, additive).** An inbound HTTP receiver verifies the vendor signature, maps
  `vendorRef → handle`, and calls the *same* `resolve()`. Lower latency, but needs a reachable endpoint +
  signature trust + the ref→handle index. Nothing in Loom/Weaver/the replyOp changes to add it.

---

## Phasing

- **Phase A — core machinery, fakes only (the bulk of the value, zero infra).** Async `Execute`/`Poll`
  contract; `RecordServiceDispatch` op + `.dispatch` aspect; the bridge temporal lane (arm + fired
  consumer, mirroring `weaver/temporal.go`); poll-timeout; Loom deadline re-tune; the pending-claims lens
  + Weaver pending-suppression; `fakeAsyncCheck` (Pending → Poll×N → Resolved) + a timeout test. Fully
  deterministic, fully tested without a vendor.
- **Phase B — real inbound.** Webhook receiver (HTTP ingress, signature verify, ref→handle map → resolve)
  and/or a real adapter `Poll`. Vendor-specific, infra-heavy.
- **Phase C — scale & ops.** Poll backoff tuning; batch reconcile (one `@every` heartbeat + pending lens
  instead of per-claim `@at` chains) when volume warrants; pending-age observability; the
  late-result-after-timeout policy.

---

## Contract surface to confirm BEFORE building (flag for Andrew)

Per the autonomous mandate, contract touches are flagged, not silently taken:

1. **Contract #10 §10.4 (temporal lane subject space).** The bridge becomes a second core-schedules
   producer/consumer (`schedule.bridge.poll.*` / `schedule.bridge.timeout.*` + their `.fired.` mirror).
   Needs a subject-namespace allocation alongside `schedule.weaver.timer.*`. **Likely a CAR.**
2. **Contract #10 §10.5/§10.6 (externalTask instanceOp/replyOp).** §10.6 already says the externalTask is
   "never a synchronous submit-reply" — so the **Pending disposition may already be within §10.6's
   spirit**; confirm, and decide whether the `RecordServiceDispatch` pending-marker op needs a §10.x
   clause or is purely package-local (lease-signing), like the reply op.
3. **`.outcome` status enum.** Optional third value `timedOut` (vs folding into `failed` with a reason).
   Package-local (lease-signing / service-domain) — not a frozen contract, but a vocabulary decision.
4. **Loom externalTask deadline horizon** — per-adapter / configurable, and its relationship to the
   bridge poll-timeout (the §timeout ordering). Confirm this lives in Loom's instanceOp params vs config.

---

## Open decisions for review

1. **Inbound primary:** poll-via-core-schedules (recommended) vs webhook-first. (`resolve()` keeps both.)
2. **Poll triggering:** per-claim `@at` chain (recommended Phase-3 — precise, needs no `@every`) vs one
   `@every` heartbeat + pending-lens batch (scale variant, Phase C).
3. **Timeout status:** reuse `failed` vs a distinct `timedOut` (recommended — lets the lens/operator
   distinguish "vendor said no" from "vendor never answered"; one enum value).
4. **`RecordServiceDispatch` shape** + whether `nextPollAt` advances are full ops or a lighter touch
   (and whether re-arm goes through an op at all, or is a pure schedule replace with the `.dispatch`
   carrying only the immutable vendorRef + deadline).
5. **Poll backoff** policy + cap; and the late-result-after-timeout stance (first-writer-wins, no re-open).
6. **Who owns the timeout** — the bridge's own temporal lane (recommended, symmetry with Weaver) vs
   extending Weaver's lane-3 to also arm bridge timers (rejected: conflates the vendor-call owner with
   the convergence engine).

---

## Build sequencing (increments — each its own worktree + review)

The epic lands in increments, not one sub-agent. The boundaries fall at the correctness/dependency
seams:

- **Increment 1 — the async Adapter SPI + the pending-dispatch marker (no poller yet).** `bridge.Adapter`
  gains the async shape: `Execute(ctx, req) (Outcome, error)` with `Outcome{Disposition: Resolved |
  Pending, Result, Ref}` + `Poll(ctx, ref) (Outcome, error)`. On a Pending Execute, `dispatch.go` posts a
  new package op **`RecordServiceDispatch`** that writes a create-only **`.dispatch`** marker
  `{vendorRef, submittedAt}` on the claim vertex and posts **no** `.outcome` (the token stays parked); an
  `alreadyDispatched` skip-probe makes a redelivery not re-call the vendor. A new **`fakeAsyncCheck`**
  returns Pending then Resolved-on-Poll; the sync fakes gain the new signature + a trivial `Poll`.
  Nothing calls `Poll` yet — the SPI is complete so it never changes again, but the *driver* is
  Increment 2. Touches `internal/bridge` + `packages/lease-signing` only; does **not** touch the schedule
  lane (independent of the §10.4 edit). The sync path (today's fakes, the lease-convergence e2e) is
  unchanged and MUST stay green.
- **Increment 2 — the bridge temporal poll/timeout lane** (uses `schedule.bridge.*`, per the §10.4
  edit). Arm `schedule.bridge.poll/timeout.<handle>` on Pending; a fired consumer (mirror
  `weaver/temporal.go`) calls `adapter.Poll` → resolve (post `replyOp`) or re-arm, and on timeout posts a
  terminal `failed`/`timedOut` `replyOp`. The `.dispatch` marker grows `{deadline, nextPollAt}`.
- **Increment 3 — Weaver pending-suppression + Loom deadline re-tune** — **required before any REAL async
  adapter** (without it Weaver re-triggers a still-pending call → double-dispatch). The lens projects the
  pending dimension; the gap predicate gains the pending-suppression clause; Loom's externalTask step
  deadline (§10.6 `deadline.<instanceId>` TTL) is sized per-adapter to outlast the vendor SLA.

Increment 1 is launched first; it is safe to build/test in isolation (only `fakeAsyncCheck` ever returns
Pending, so real flows are unaffected until Increment 3 wires a real async adapter).

---

## Fire brief — the external-gap classifier reads the action, not the gap (build note, 2026-08-09)

**1. Scope sentence.** The three `TestAsyncConvergence_*` legs run in no CI gate and two fail at clean
`main`: after the bridge timeout posts its terminal `failed` outcome, Weaver never dispatches a fresh
retry. Restore the ratified timeout→failed→fresh-retry leg, then put all three legs under a gate.
**Green bar:** `go test -tags leaseshortwindow ./internal/leaseconvergence/ -run TestAsyncConvergence`
all three PASS, and `make test-lease-convergence` selects them.

**2. Root cause (proven live, not inferred).** `staleMark` classifies a gap as EXTERNAL by its *dispatch
action* — `ga.Action != actionDirectOp && ga.Action != actionProposedOp` → not external
(`internal/weaver/evaluator.go:339`). But this package's two vendor-backed gaps are `triggerLoom` of an
**externalTask** pattern (`packages/lease-signing/targets.go:90-91`; `patterns.go:38-65`, both
`Kind: "externalTask"`, no userTask step). So a genuine external gap is judged a package authoring bug.
Observed at the instant of failure, 7ms after the failed outcome lands:

> `ERROR weaver: target leaseApplicationComplete: row column inflight_bgcheck is declared but gap
> missing_bgcheck's playbook action "triggerLoom" is not external-dispatch (directOp/proposedOp);
> ignoring the marker`

Consequences, both live and not test-only:
- Lane-1 (`evaluator.go:300`) computes `stale=false` with `found=true` → the **anti-storm drop**; the
  retry never dispatches on the CDC touch the outcome produces.
- The sweep (`reconciler.go:530`) sets `confirmedConcluded=false` → `collapseOnlyReclaim` is true → the
  userTask backoff paces the reclaim **and** preserves the claimId, so any eventual re-dispatch collapses
  onto the already-terminal Loom instance — exactly the no-op that `reconciler.go:597-611` warns of.
- `maxretries_bgcheck` / the Augur `exhausted` escalation are unreachable: the dispatch count never climbs.
- A standing **`error`-severity** `InflightActionMismatch` Health issue for a correctly-authored package.

**3. The contract is on the fix's side — no contract change.** Contract #10 §10.3
(`docs/contracts/10-orchestration-substrate.md:233`) defines an external gap by **companion-column
presence**, never by action: *"External gaps are unchanged — their reclaim re-dispatch is intended,
episode-scoped on `markRevision` and bounded by `inflight_<g>` + `maxretries_<g>`; `directOp` likewise."*
The engine's action test is a narrowing the contract never asked for. This design's own §Component spine
frames the external gap as a **triggerLoom** gap ("Weaver already is the ensure-eventually / re-trigger
on gap engine (triggerLoom on a violating gap)"), so the fix restores ratified behavior.

**4. Chosen mechanism — ask the real question, at zero read cost.** The contract's actual distinction is
"triggerLoom of a **userTask-containing** pattern" (the reconciler's own comment, `reconciler.go:516-529`).
Weaver cannot answer that today: `patternMeta` stores only the meta-vertex key
(`registry.go:348`, `:1067-1089`). But `indexPattern` **already holds the whole spec body** —
`unwrapSpecBody(body, "steps")` — and probes only `patternId`. Extend `patternSpecProbe` to record whether
any step is `Kind: "userTask"`, carried on the existing `patternMeta`/`patternOwner` lifetime (created at
index, dropped by `removePatternLocked`). No new KV read, no new cache lifetime, no `GapActionSpec` schema
change, no package edits or version bumps.

*Rejected:* a `GapActionSpec.External` flag (schema change + every package re-declares what the engine can
already derive); a dispatch-time spec read (new read path on the hot dispatch leg).

**Fail-safe direction:** an unknown / not-yet-indexed pattern counts as **userTask-containing**. A
duplicated human task is worse than a delayed retry, and the misclassification self-corrects the moment
the pattern indexes. This also keeps `TestSweep_InflightActionMismatchIgnoredForUserTaskGap` green
unchanged — it seeds `patternMeta` directly with no spec.

**5. Verified touch-list** (checked live at `ddba78d2`):
- `internal/weaver/registry.go:1057-1060` — `patternSpecProbe`: add the steps probe.
- `internal/weaver/registry.go:1067-1090` — `indexPattern`: record userTask-containment.
- `internal/weaver/registry.go:348` / `:1092-1101` — the map + `removePatternLocked`: same lifetime.
- `internal/weaver/evaluator.go:330-347` — `staleMark`: replace the action test with the containment test;
  keep the `InflightActionMismatch` alert for the case it was written for.
- `internal/weaver/reconciler.go:53-56` — `collapseOnlyReclaim`: follows `confirmedConcluded`, unchanged.
- `Makefile:1746` — the `-run` filter selects `TestLeaseConvergence|TestRenewalConvergence` only; the three
  `TestAsyncConvergence_*` match neither (verified with `go test -list`). Widen it.

**6. Precedents to mirror.** The probe-at-index pattern is `indexPattern` itself. The alert + self-clear
pair is `issueKeyInflightMismatch` (`evaluator.go:338-345`). The gate widening mirrors the existing
`test-lease-convergence` recipe.

**7. Increment order.**
1. Registry: probe step kinds + carry containment. Green: `go test ./internal/weaver/ -run TestSweep -count=1`.
2. `staleMark`: containment-based classifier. Green: `go test ./internal/weaver/... -count=1` (incl.
   `TestSweep_InflightActionMismatchIgnoredForUserTaskGap`, which must pass **unchanged**).
3. Prove the fix end to end: all three `TestAsyncConvergence_*` pass; revert increment 2 and watch
   `_Timeout_FailedThenOneRetry` fail again (the mechanism-disabled check).
4. Gate: widen the `Makefile` `-run` filter; confirm with `go test -list`.

**8. In-scope gotchas.**
- *New state needs a LIFETIME:* the containment flag rides `patternMeta`'s existing lifetime — state table
  is created at index / replaced by `removePatternLocked` / rebuilt on replay. No new boundary.
- *A negative test needs its positive vector proven first:* increment 3's revert-and-watch-it-fail is that
  proof; a green suite alone does not pin this fix.
- *Precedent may carry debt:* `gapSuppressed` (`evaluator.go:896`) carries the **same** action-based
  narrowing for its cap-fallback term. It is not load-bearing for this green bar (the lease gaps declare
  `maxretries_<g>`, so the fallback never fires) — noted, not fixed here, and filed as a row.
- Weaver's logger in `internal/leaseconvergence/harness_test.go:192` is `io.Discard`, which is why this
  `error` alert has been invisible to every run of this suite. Leave it discarded; note it.

**9. Adjacent finds — filed before the first edit.**
- `[Weaver] gapSuppressed's cap-fallback shares the same action-based external-gap test` — the sibling of
  this defect, inert today. **steward-owned · next run.**
- `[Tooling] The lease-convergence harness discards every engine log` — an `error`-severity Health alert
  fired on every run of this suite and no run could show it. **steward-owned · next run.**

**10. Non-goals.** No change to `gapSuppressed`'s cap-fallback, to the bridge's timeout semantics, to the
lens, to any package, or to `docs/contracts/*`.
