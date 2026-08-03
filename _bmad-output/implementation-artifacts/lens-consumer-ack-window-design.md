# A supervised consumer's ack window is sized by the client's prefetch, not by its pump

**Status:** Inc 1 + Inc 2 SHIPPED. **§2 is SUPERSEDED by §5b**, and **§5b's open question is ANSWERED
in [`lens-trigger-relation-narrowing-design.md`](lens-trigger-relation-narrowing-design.md) §1** — the
674 messages are neighbour-link events on a relation the lens never traverses, admitted by a trigger
derivation that keys on endpoint type and discards the relation segment. Nothing in §1–§2's mechanism
wedged this consumer; read §5b, then that design.
**Board row:** `[Refractor] A lens consumer's ack floor freezes, so its rebuild can never drain`
(`backlog/lattice.md`, component maintenance).
**Owning code:** `internal/substrate` (the defect), `internal/refractor/health` (the silence).

## 1. What was measured

Live on the dev stack, 2026-08-03, consumer `refractor-ynnaZCqFhB3t22dLynna` (lens `clinicProviders`,
an actorAggregate on `vtx.identity.>` / `vtx.provider.>`):

```
                Ack Wait: 5m0s
         Max Ack Pending: 1,000
  Last Delivered Message: Consumer sequence: 27,202   Stream sequence: 46,308
    Acknowledgment Floor: Consumer sequence: 3,930    Stream sequence: 3,641
        Outstanding Acks: 678 out of maximum 1,000
     Redelivered Messages: 323
    Unprocessed Messages: 0
```

Three facts were read off this — the third of which turned out to be an inference, not a fact; see §5b:

- **`Unprocessed Messages: 0`.** The server has nothing left to send. Every message is either acked or
  outstanding. So this is not a backlog — it is a consumer that has been handed everything and cannot
  finish it.
- **Consumer sequence 27,202 against a stream that ends at 46,308, with only 678 distinct messages
  outstanding.** Consumer sequence counts *deliveries*, including redeliveries. ~~The gap is redelivery
  churn, not progress.~~ **Wrong** — the gap is accumulated history, not an ongoing loop; the *rate* was
  never checked, and `num_redelivered` was constant in the same sample (§5b).
- **Nothing errored and nothing paused.** The pump never returned a failure class, so no
  `PauseInfra` / `PauseStructural` was entered, nothing was logged, and the lens rendered green.

A goroutine dump taken while wedged (`REFRACTOR_PPROF_ADDR`, 697 goroutines) shows 101 of the 102 lens
pumps parked in `pullSubscription.Next` and exactly one inside its handler:

```
pipeline.(*Pipeline).handle            pipeline.go:1374
 → evalAspectFanOut → evaluateAspectFanOut → reprojectActors
 → executeFullForActor → ... → full.(*executor).readNode   executor.go:801
 → substrate.(*Conn).KVGet → jetstream GetLastMsgForSubject
```

No deadlock. The handler is simply *slow* — an actorAggregate reprojection re-executes the full engine
once per actor, with a KV round trip per node — and its latency is unbounded by design (that is the
separate `auth-plane-projection-latency` row, which this design does not fix and does not need to).

## 2. The mechanism ~~(as first read)~~ — SUPERSEDED by §5b

> **Superseded.** Everything below is a correct description of a real defect, and Inc 1 fixes it. It is
> **not** the cause of the live wedge in §1: the rate measurement in §5b shows 11 deliveries in 20
> minutes and a constant `num_redelivered`, which is starvation, not the redelivery storm this section
> infers. Kept because the defect and its fix are real, and because the inference error is worth seeing.

`ConsumerSupervisor`'s pump is strictly serial. `drain` (`consumer_supervisor_pump.go:321`) calls
`mc.Next()`, runs the handler to completion in `processMsg`, applies the ack, and only then calls
`Next()` again. One message is *worked* at a time, always.

The pull iterator underneath it is not serial. `messagesOpts` (`consumer_supervisor_pump.go:41`) sets a
prefetch bound **only** when `Workers > 1`; a single-worker consumer inherits nats.go's default. In our
pinned nats.go v1.52.0 that default is `DefaultMaxMessages = 500` (`jetstream/pull.go:191`), with
`ThresholdMessages` defaulting to half of it (`pull.go:1183`), and the iterator's buffer is literally
`make(chan *nats.Msg, MaxMessages)` (`pull.go:525`). So the client asks the server for up to 500
messages and parks them in a channel.

**JetStream's AckWait clock starts at delivery, not at `Next()`.** A message sitting in that channel is,
to the server, delivered and awaiting acknowledgement. Its 5-minute window is burning while the pump is
several hundred messages away from looking at it.

That gives the livelock directly. Let `L` be handler latency and `N` the prefetch depth. The message at
the back of the buffer waits `N·L` before the pump starts it. Once `N·L > AckWait`, the server redelivers
it — and the redelivered copy enters the same buffer behind the same queue. The pump now owes strictly
more work than arrived. `AckFloor` stops moving, `NumRedelivered` climbs, `NumAckPending` sits at the
buffer depth, and none of it is an error.

With `N = 500` and an actorAggregate handler, `N·L` is hours against a 5-minute window. The consumer can
never drain, which is why a rebuild — whose whole definition is "reset the cursor and re-drain" — can
never complete.

**The invariant that was violated:** a pump must not hold more messages than it can *start* within
AckWait. Because `L` is unbounded, no static `N > 1` satisfies that. The single-worker path was left on
the JetStream default deliberately — the comment at `consumer_supervisor_pump.go:34` says so, to keep
existing Loom/Weaver/Refractor consumers byte-identical — and that byte-identity is the defect.

## 3. The silence

Separately, and this is the half the board row named: a wedged consumer publishes nothing.

`LagPoller` reports `p.Pending` → `PendingForConsumer` → `info.NumPending` (`consumer_supervisor.go:346`).
The wedged consumer's `NumPending` is **0** — the server has delivered everything. So `consumerLag` and
`projectionLag` both read zero and the lens is indistinguishable from one that is fully caught up.
`OutstandingForConsumer` (`NumPending + NumAckPending`) already exists and already carries the truth; the
health entry just never looks at it.

`LagProgressAt` does not rescue this either: it is stamped when lag *decreases*, and a lag pinned at 0
never decreases, so the clock ages — but no reader branches on an aging clock at zero lag.

The rebuild-wedge detection that does exist is auth-plane-only (`CapabilityLensProvider`'s convergence
sweep), so a plain lens's stuck rebuild is invisible.

## 4. The fix

### Inc 1 — bound the pump's ack window to what the pump can honor (`internal/substrate`)

**1a. Prefetch one message per worker, always.** `messagesOpts` sets `PullMaxMessages(1)` for every
supervised consumer, not just the fan-out lanes. `fanOutPullMaxMessages` retires: a fan-out worker's
drain loop is just as serial as a single-worker one, so 8 is the same bug at a smaller multiple.

This makes forward progress a property of the design rather than of handler speed: at most one message
is outstanding per worker, so the ack floor advances one message at a time no matter how slow the
handler is.

*Cost, stated honestly:* nats.go issues its next pull from inside `Next()` (`checkPending` at
`pull.go:642`), so a prefetch of 1 serializes one pull round-trip per message instead of overlapping it.
Every supervised consumer — the Refractor lens pumps, the Processor's op lanes — pays it. It is a
loopback request/reply against work that is a Starlark execution or a full-engine projection; the trade
is a sub-millisecond round trip for a failure mode that costs the lens its entire life. Verified live
against a real rebuild in §6.

**1b. Heartbeat the in-flight message.** Even with a prefetch of 1, a handler that legitimately runs
longer than AckWait loses its own window and gets redelivered under itself — bounded duplicate work
forever. `processMsg` starts a ticker alongside the handler that calls `jetstream.Msg.InProgress()` at
half the effective AckWait and stops when the handler returns.

This is the upstream-sanctioned mechanism: `InProgress` sends `+WPI` and, uniquely among the ack types,
does **not** mark the message acked (`jetstream/message.go:438-441`), so it may be sent repeatedly and a
heartbeat that races the final `Ack` fails harmlessly with `ErrMsgAlreadyAckd`. With it, AckWait stops
bounding handler latency at all — which is the correct posture for a projection engine whose per-message
work is unbounded by design.

Together: 1a bounds *how much* un-started work can age out (to nothing), 1b bounds *how long* started
work may take (to unbounded). Neither alone closes it.

### Inc 2 — make a frozen ack floor a signal (`internal/refractor/health`)

The health entry gains the two fields that distinguish "caught up" from "wedged", and the poller stamps
them:

- **`ackPending`** — `NumAckPending`, read via the existing `OutstandingForConsumer` shape. Nonzero and
  steady is the wedge; zero is genuinely drained.
- **`ackFloorProgressAt`** — stamped at the first poll and re-stamped whenever `AckFloorForConsumer`
  (already exported, `consumer_supervisor.go:379`) reports a higher floor. An ack floor that has not
  moved while `ackPending > 0` is the wedge, and the age of the clock is how long it has been wedged.

A signal no consumer branches on is not a signal, so Inc 2 also renders both in `lattice lens health`,
which is the operator-facing reader that exists today.

## 5. Non-goals

- **Fixing actorAggregate reprojection latency.** That is the `auth-plane-projection-latency` row and is
  blocked on Andrew. This design makes the pump survive an unbounded handler; it does not make the
  handler bounded.
- **A new health `status` value.** The status vocabulary is closed and read by Loupe, the CLI, and Edge
  control responses; widening it is a bigger blast radius than this warrants. `ackPending` +
  `ackFloorProgressAt` are additive fields on an existing entry.
- **Draining the already-churned redelivery debt.** Cycling Refractor after the fix re-establishes the
  window from the persisted floor; no migration is needed.

## 5b. Correction — Inc 1 is a real defect, but it is NOT this wedge's cause

Recorded after building and cycling Inc 1 live, because the fix did not move the wedge and the
original §2 reading does not survive the measurement.

**What the fix did do.** The over-prefetch is real and independently provable: a serial pump holding
500 delivered messages it has not started is a live hazard, and
`TestSupervisor_AckWindowBoundToThePump` fails without either half of Inc 1. That stands.

**What it did not do.** Cycled `bin/refractor` from `main` at 02:56 and watched
`refractor-ynnaZCqFhB3t22dLynna` for 20 minutes. Unchanged: ack floor pinned at consumer seq 3,930,
`num_ack_pending` 678, `num_redelivered` 323. `delivered.consumer_seq` moved 27,202 → 27,213 — **11
deliveries in 20 minutes**, not a storm.

**The measurement that reframes it.** `refractor.log` shows this lens processing exactly one
*distinct* message every ~5 minutes — 02:12, 02:17, 02:22, 02:27, 02:32, 02:38, 02:43, 02:48, 02:54,
then 02:59 and 03:05 after the restart. Same cadence before and after the fix, and each `entityId`
differs, so the pump is making real forward progress, one message per AckWait period. Each handler
call takes ~3 minutes, well under the 5-minute window.

So the pump is not thrashing on redeliveries and is not falling behind a prefetch — it is **starved**.
`num_pending` is 0, so nothing is queued to hand it; the only source of work is a message whose
AckWait has expired, and it receives one per 5-minute expiry cycle. The ack floor stays at 3,641
because the floor is the contiguous acked prefix and the 678 un-acked messages sit above it, each
waiting its own expiry turn. At one per cycle that is ~56 hours to drain.

**§2's error, named:** it inferred a redelivery storm from a large `delivered.consumer_seq` and a
frozen floor, and never checked the *rate*. The 23k consumer-sequence gap is accumulated history from
whatever produced the 678, not evidence of an ongoing loop. `num_redelivered` sitting constant at 323
was visible in the very first sample and contradicts a storm outright.

**What is still unexplained, and is the open question:** why the 678 became un-acked in the first
place, and why the expiry cycle yields one message rather than the whole expired set. Note the pump
asks for exactly one message now (Inc 1), so a single-message-per-cycle *delivery* is expected
post-fix — but the same cadence predates the fix, when the prefetch was 500. That rules the prefetch
out as the throttle and points at the server-side redelivery path or at something upstream leaving
this lens's messages un-acked in bulk. Inc 2 is what makes the state visible while that is chased;
it does not close it.

## 6. Verification

- Unit, `internal/substrate`: a consumer whose handler outlives AckWait still advances its ack floor,
  and the in-flight message is not redelivered under the handler.
- Unit, `internal/substrate`: `messagesOpts` yields a prefetch of 1 for both the single-worker and
  fan-out paths.
- Full `go test ./...` — a shared default on `ConsumerSupervisor` reaches the Processor's lanes and
  every engine that supervises a consumer, so the blast radius is wider than the package.
- **Live:** cycled `bin/refractor` from `main` and watched the consumer for 20 minutes. The ack floor
  did **not** advance — see §5b. Inc 1 is proven by its unit test and by CI, not by this consumer.

## 7. Build note — fire 2026-08-03

**Scope sentence (verbatim, from §4):** bound the supervised pump's prefetch to one message per worker,
heartbeat the in-flight message so a handler may outlive AckWait, and surface a frozen ack floor on the
health entry.

**Touch list (verified live):**

- `internal/substrate/consumer_supervisor_pump.go:27-47` — `fanOutPullMaxMessages` / `messagesOpts`.
- `internal/substrate/consumer_supervisor_pump.go:349-360` — `processMsg`.
- `internal/substrate/consumer_supervisor_spec.go:151-161` — `AckWait` / `MaxAckPending` doc.
- `internal/refractor/health/healthwire/healthwire.go:38` — `Entry`.
- `internal/refractor/health/lag_poller.go:104-175` — `Start` / `poll` / `recordLagProgress`.
- `internal/refractor/health/reporter.go:401` — `SetProjectionProgress`.
- `cmd/refractor/main.go:1034` — `NewLagPoller` wiring.

**Precedents to mirror:** `recordLagProgress`'s "stamp on improvement" clock is the exact shape
`ackFloorProgressAt` wants; `OutstandingForConsumer`'s doc comment already states the NumPending-alone
trap that Inc 2 is closing.

**Increment order:** Inc 1 (substrate, its own green commit) → Inc 2 (refractor health).

**In-scope gotchas:** `InProgress` must not run after the handler returns (it would race the ack and log
`ErrMsgAlreadyAckd` noise) — stop the ticker before applying the decision. `spec.AckWait == 0` means the
JetStream default of 30s, not "no wait", so the heartbeat interval derives from an effective value.

**Non-goals:** §5.
