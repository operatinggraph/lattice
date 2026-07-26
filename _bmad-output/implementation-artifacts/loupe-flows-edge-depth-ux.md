# Loupe — Flows and Edge, beyond reading tiles

**Status:** 📐 design, adjudicated inline (Winston, Andrew-delegated for the Loupe program) · 2026-07-25
**Lane:** Loupe (Stream 3) · rows on [backlog/loupe.md](../planning-artifacts/backlog/loupe.md)
**Grounded live** against the running stack (26 Loom flows, 31 edge devices) on 2026-07-25.

Both tabs render a wall of tiles and stop. This is the PO pass on what they are for, what
they are missing, and what the platform can actually support today — the last part matters,
because the first thing the grounding turned up is that one of these tabs is lying.

---

## 1. The finding that comes first: the Flows tab reports finished flows as running

On the live stack, 10 of 26 flow rows render **`RUNNING · LIVE`**. Loom's own control plane
reports every one of those instances as **`status: "complete"`**, cursor 1, terminal.

The same rows carry an `endedAt` **earlier than their `startedAt`** — the console renders
both timestamps side by side without noticing that one precedes the other.

Two independent defects stack to produce this, and they are in different lanes.

### 1.1 The live badge asks the wrong question (Loupe, in-lane)

`liveLoomInstances` ([cmd/loupe/flows.go](../../cmd/loupe/flows.go)) collects instance ids
from the `lattice.ctrl.loom.list` read and ignores the `status` field sitting beside each id
in the same payload. `computeFlows` then badges a row `live` on set membership alone.

But Loom's list includes **terminal** instances — their `loom-state` records survive
completion. So "live" currently means *"Loom still has a state record for this id"*, not
*"this flow is running"*.

The tab's own explanatory copy says an orphaned row is **"a flow whose terminal event never
landed, not a leak"**. These 10 rows are precisely that case. The badge whose entire purpose
is to catch a stale row instead **confirms** it, because the authoritative answer was in the
payload and went unread. A cross-reference that cannot disagree with the thing it checks is
not a cross-reference.

**Fix (in-lane, XS–S):** treat only non-terminal instances as live, and reconcile the row's
status against Loom's — where the read model says running and Loom says complete, the row is
`stale-history`, which is a third badge, not a synonym for either.

### 1.2 A re-dispatched flow's row never clears its terminal (platform, cross-lane)

`loomFlowHistorySource` ([packages/orchestration-base/lenses.go](../../packages/orchestration-base/lenses.go))
projects `events.loom.>` into one row per `payload.instanceId`, merging each partial onto the
stored row (carry-forward). `ended_at` and `failure_reason` are written only by
`patternCompleted`/`patternFailed`.

Weaver deliberately derives a **stable** instanceId per (target, entity, gap, claim) —
`deriveStableInstanceID` ([internal/weaver/actuator.go](../../internal/weaver/actuator.go)) —
so a re-dispatch collapses onto the existing instance. When that re-dispatch commits, the
`loomLifecycle` DDL emits `patternStarted` regardless of whether the engine will act on it
(Loom, being idempotent on instanceId, ignores a trigger for an instance it has already
finished). The projection therefore sets `status=running` and a fresh `started_at` onto a row
whose `ended_at` is left over from the previous run — hence ended-before-started.

The carry-forward has no way to say "this column is cleared by this event". That is the
platform gap, and it is not Flows-specific: any event-sourced projection with a lifecycle that
can restart hits it.

**Consequence worth naming:** `failure_reason` carries forward the same way, so a flow that
failed and later re-ran renders a red failure reason on a healthy running row.

**Routing:** `lattice.md` — the `eventStream` projection needs column clearing (or the
lifecycle DDL must stop emitting `patternStarted` for an instance that will not start).
Loupe's 1.1 fix stands on its own and does not wait for it.

---

## 2. Flows — what it is for

The tab is the **Chronicler's durable Loom-flow history**: what orchestration ran, on what
subject, and how it ended. Today each card carries pattern ref, subject, two timestamps, and a
status chip, and the header says plainly *"Read-only — no control-plane op from here."*

Wearing the PO hat against 26 live cards, four things are wrong before any feature is added.

**The pattern is unreadable.** Every card is titled `vtx.meta.b9zHvyhCTmMsTZ6Rb9zH`. An
operator cannot tell a lease-renewal flow from an onboarding flow. The map and the lens page
both already resolve `vtx.meta.<id>` → `canonicalName`; Flows is the one surface that does not.
This alone is most of the tab's unreadability, and it costs one resolver already written.

**26 cards, one wall.** No grouping, no worst-first ordering. A failed flow sorts wherever its
timestamp puts it. Exception-first — failed, then stale-history, then running, then complete —
is the vocabulary every other Loupe surface already uses.

**No detail.** A flow is a *sequence of steps*; the card shows none of it. Loom's control plane
already answers this: `inspect` returns `InstanceDetail{Instance, CurrentStep, Terminal}`
with cursor, pendingToken and retryCount, and Loupe's control proxy already classifies
`inspect` as read-only, so it passes even the demo posture's method gate. **Verified live
against a running instance** — this needs no cross-lane ask.

**No dead end escape for the pattern.** The subject is a keyLink; the pattern ref is plain
text. Both should reach the Graph, and the pattern should also reach its package.

### 2.1 What can be built now

**F23.1 — Flow detail (S).** Click a flow → a detail panel: resolved pattern name (linked to
the meta-vertex and its owning package), subject keyLink, the step sequence with the cursor
marked, `pendingToken`, `retryCount`, `terminal`, and the failure reason when there is one.
The step list comes from the pinned pattern via `inspect`. Where the read model and Loom
disagree, the panel says so in both voices rather than picking one — that disagreement is
diagnostic, and 1.1 turns it into a first-class badge.

**F23.2 — Make the wall readable (XS–S).** Resolve pattern names, group by pattern, sort
exception-first, linkify the pattern ref. Mechanically small; it is the difference between a
tab an operator scans and one they close.

### 2.2 What "act on it" actually means — Loom supports no retry

The ask was to retry a stale or failed step *if Loom supports it*. **It does not**, and the
answer is worth recording so nobody re-derives it:

- Instance statuses are `running` / `complete` / `failed`, and `failed` is **terminal**
  ([internal/loom/state.go](../../internal/loom/state.go)). `RetryCount` is incremented once
  on the terminal transition; it counts, it does not drive a retry.
- The control plane's op set is `inspect` (read), `pause`, `resume`
  ([internal/loom/control/service.go](../../internal/loom/control/service.go)) — and
  pause/resume act on **named consumers**, not on an instance. Pausing the outbox relay or the
  deadline watcher is refused outright as an engine-wide hazard.
- `StartLoomPattern` is idempotent on instanceId, so re-submitting a failed instance's id is a
  no-op. Only a *new* instance for the same subject can re-run the work, and nothing in the
  console can mint one safely today.

So the honest position: **Flows becomes an excellent read-and-diagnose surface now, and stays
read-only until the platform grows a per-instance redrive.** A "Retry" button that quietly
starts a second instance under a fresh id would double-execute every side effect the first run
already committed — the reason a redrive is a platform design, not a console button.

**Cross-lane ask (lattice.md):** a Loom per-instance **redrive** op — resume a failed instance
at its cursor, or restart it under a new id with the old one tombstoned — with the
double-execution question answered by design. Flows is the named consumer.

---

## 3. Edge — what it is for

The tab is the **Personal Lens subscriber fleet**: who is registered, their Interest Set, and
their position in the SYNC stream. Live it reads *31 devices across 9 identities · 26 gapped*.

It is honest and dense, and F19 already resolved the hard questions (why `personal.syncgap` is
unusable as an operator source; why `revisionCursor` is not a SYNC sequence). The gap is not
truthfulness — it is that the tab states a fleet-wide problem and offers nothing to do about it.

**The headline is a diagnosis with no next step.** 26 of 31 gapped is the tab's own finding.
The copy names the remedy — a warm resume consumes `personal.hydrate` — and the operator
cannot trigger one.

**No triage order.** Devices sort by identity, so the worst device on the fleet is wherever its
identity happens to fall. Worst-first by messages-aged-out is the same exception-first rule
Flows needs.

**No per-device depth.** Each device is a flat text block: no Interest Set contents, no recent
deltas, no registration history. The one drill-in is the identity keyLink.

### 3.1 What can be built now

**F24.1 — Fleet triage (XS–S).** Sort worst-first by messages-aged-out, summarise the fleet by
retention headroom rather than by identity order, and make each device expandable to its
Interest Set contents. Read-only, in-lane, no platform dependency.

**F24.2 — Device detail (S).** A per-device panel: Interest Set terms, ack position against the
retention floor with the headroom in messages *and* in time, registration provenance, and the
identity's other devices. Still read-only.

### 3.2 The action needs a platform seam

Triggering a warm resume means asking a device to consume `personal.hydrate` — but the design's
own premise is that **edge nodes cannot self-report and no connection state is observable**.
The console cannot push to a device it cannot see. What it *could* do is mark a device for
hydration on its next appearance, which is a durable per-device flag nothing owns today.

**Cross-lane ask (lattice.md):** an operator-initiated **hydration request** for a registered
device — durable, consumed on the device's next SYNC attach, idempotent. Loupe's Edge tab is
the named consumer. Until it exists, F24.1/F24.2 make the tab a triage surface, and the tab
should say plainly that remediation is the device's own next attach.

---

## 4. Build order

| Fire | What | Size | Depends on |
|---|---|---|---|
| **F23.0** | Live-badge honesty: terminal instances are not live; read-model-vs-Loom disagreement becomes `stale-history` | XS–S | — |
| **F23.2** | Flows readability: resolved pattern names, grouping, exception-first sort, linkified pattern | XS–S | — |
| **F23.1** | Flow detail panel: steps, cursor, pendingToken, retryCount, failure reason | S | F23.0 (badge vocabulary) |
| **F24.1** | Edge fleet triage: worst-first, retention-headroom headline, expandable Interest Set | XS–S | — |
| **F24.2** | Edge device detail panel | S | F24.1 |

F23.0 leads deliberately: every other Flows fire renders state on top of a badge that is
currently wrong, and building depth over a lying status ships a more convincing lie.

The two cross-lane asks (Loom per-instance redrive; operator-initiated device hydration) are
filed to `lattice.md` and gate only the *act-on-it* half. Everything above is buildable in-lane
today.
