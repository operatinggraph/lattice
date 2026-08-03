# A supervised consumer's ack window is sized by the client's prefetch, not by its pump

**Status:** ✅ Winston-ratified — build-ready (no frozen-contract change, no architectural fork).
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

Three facts pin the shape:

- **`Unprocessed Messages: 0`.** The server has nothing left to send. Every message is either acked or
  outstanding. So this is not a backlog — it is a consumer that has been handed everything and cannot
  finish it.
- **Consumer sequence 27,202 against a stream that ends at 46,308, with only 678 distinct messages
  outstanding.** Consumer sequence counts *deliveries*, including redeliveries. The gap is redelivery
  churn, not progress.
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

## 2. The mechanism

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

## 6. Verification

- Unit, `internal/substrate`: a consumer whose handler outlives AckWait still advances its ack floor,
  and the in-flight message is not redelivered under the handler.
- Unit, `internal/substrate`: `messagesOpts` yields a prefetch of 1 for both the single-worker and
  fan-out paths.
- Full `go test ./...` — a shared default on `ConsumerSupervisor` reaches the Processor's lanes and
  every engine that supervises a consumer, so the blast radius is wider than the package.
- **Live:** cycle `bin/refractor` from `main`, confirm `refractor-ynnaZCqFhB3t22dLynna`'s ack floor
  advances and `Outstanding Acks` falls to 0, and time a `lattice lens rebuild` to size the prefetch
  trade honestly rather than by assertion.

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
