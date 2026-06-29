# Design — Native `@every` recurring schedules (the temporal scheduler / cron-killer, #47) + the formal retirement of the op-vertex pruner (#49)

**Status: ✅ Andrew-ratified (2026-06-28)** · Designer fire 2026-06-28 (Winston, lattice-designer) · Backlog row: *Refinements & ops → "op-vertex pruner + `@every` schedules"* (`planning-artifacts/backlog/lattice.md`). **Ready for the Lattice Steward.**

---

## For Andrew (read this first)

**What it does, in two lines.** Promotes the temporal lane from one-shot `@at` to NATS-native **`@every` recurring schedules** — the durable, operator-visible, multi-instance-single-fire "cron" the platform's vision (brainstorm #47, "replaces cron") has always wanted — and migrates the platform's only real in-process cron (the Weaver reconciler sweep, today a `time.Ticker`) onto it. In the same breath it **formally retires the op-vertex pruner (#49)**: grounding shows NATS per-key TTL (Contract #4 §4.3) + the outbox-tombstone already GC every op-vertex class, so the pruner has no work to do — the long-standing `weaver/pruner/` slot in the architecture tree is vestigial.

**Ratification outcome (Andrew, 2026-06-28):**

1. **NATS version — NO fork: the platform is *already* on NATS 2.14.** The draft framed a "2.12 → 2.14 floor bump." Andrew corrected the premise: the implementation already pins **NATS 2.14** (`go.mod` `github.com/nats-io/nats-server/v2 v2.14.0`; `docker-compose.yml` `nats:2.14-alpine`). `@every`/cron are supported on the running platform **today** — there is nothing to bump. The "2.12 floor" was outdated documentation, now **fixed**: Contract #4 §4.3 corrected to state the **NATS 2.14 platform floor** (committed with this ratification); the §10.4 heading relaxed from "requires 2.14" (a gate) to "2.14, the platform floor — no version gate." (Two `lattice-architecture.md` "atomic batch via NATS 2.12" mentions are planning-owned and *not* stale-as-floor — flagged to the planning lead, §6.7.)

2. **Frozen-contract edit — Contract #10 §10.4 + the §4.3 fix — ✅ ratified + committed with this ratification.** §10.4 already *reserved* `@every` and *named* "op-vertex pruner / retention" as a future consumer. The edit (a) lifts `@every` from reserved to **specified** (recurrence persists + re-fires; cancellation = replace/purge the schedule subject or `Nats-Schedule-Next: purge`; the per-occurrence deterministic-`requestId` rule extends the existing `@at` dedup rule), and (b) replaces the stale "op-vertex pruner / retention are future consumers" clause with the NATS-TTL reconciliation. The §4.3 fix records the 2.14 floor.

**No architectural fork.** No Gateway / read-path-auth / Vault / multi-cell entanglement. The lane is operational infra (P1), not Core KV.

**✅ Andrew-ratified — ready for the Lattice Steward.**

---

## 1. Problem & intent

### 1.1 The capability gap (#47 — "replaces cron")

Brainstorm #47 ("Weaver temporal scheduler — time-based discrepancies, replaces cron") and the Phase-2 hand-off note (`lattice-architecture.md:1165`, "the full temporal scheduler / op-vertex pruner (#47/#49) remain Phase 2+") both anticipate a recurring scheduler. Phase 2 shipped only the **one-shot** half: Contract #10 §10.4 (FROZEN 2026-06-02) provisions `core-schedules` with `AllowMsgSchedules: true`, and every consumer publishes `@at <RFC3339>` one-shots:

- **Weaver freshness lane** (`internal/weaver/temporal.go`) — per-target-per-entity `@at` deadline timers (`schedule.weaver.timer.<targetId>.<entityId>`), re-armed level-driven on each row delivery.
- **Bridge async-result lane** (`internal/bridge/schedule.go`) — a **self-rescheduling `@at` chain** for re-poll + a give-up `@at` timeout (`schedule.bridge.poll.<handle>` / `schedule.bridge.timeout.<handle>`).

Where the platform wants a *fixed-cadence recurrence* today, it does not use the temporal lane at all — it falls back to an **in-process `time.Ticker`**. The one real instance is the **Weaver reconciler sweep** (`internal/weaver/reconciler.go:126`, `t := time.NewTicker(s.interval)`, default 1 min): the lease-reclaim / orphan-mark GC loop. This in-process ticker IS the platform's de-facto cron, and it has the classic in-process-cron weaknesses:

- **Invisible to operators** — there is no schedule to inspect, pause, or retune; the cadence is a redeploy-only constant.
- **No multi-instance coordination** — in the deferred HA / multi-instance-fan-out world (`lattice-architecture.md` Scale-out), N Weaver replicas would each tick independently → N concurrent sweeps. OCC + the §10.3 marks make that *safe* but *wasteful* (N× the list+reclaim load) and operationally confusing.
- **Cadence does not survive as state** — a restart resets the tick phase; there is no durable record that "a sweep is scheduled every minute."

`@every` closes this: one schedule message fires once per interval into `core-schedules`, a single durable consumer picks it up → **exactly one sweep per interval across all replicas**, the cadence is a durable + operator-visible + retunable schedule message, and it survives restart. This is literally "the temporal scheduler that replaces cron."

### 1.2 The op-vertex pruner (#49) is already discharged — retire it

Brainstorm #49 ("Operation Vertex pruner — Weaver's idempotency-horizon GC") and `lattice-architecture.md:298` ("Retention policy enforcement (e.g., pruning old idempotency trackers) is handled by Weaver, not NATS-level retention settings") + the `weaver/pruner/` slot in the component tree (`lattice-architecture.md:679`) all predate the **Contract #4 §4.3 freeze (NATS per-key TTL)**, which settled tracker retention a *different* way:

- **Idempotency trackers** (`vtx.op.<requestId>`) are written with a **24h NATS per-key TTL** (ADR-48) in the step-8 atomic batch — `internal/processor/tracker.go:19` (`TrackerTTL = 24h`), `step8_commit.go:168-174`. NATS publishes a `PURGE` marker at expiry; Refractor/CDC consumers observe it. **No Weaver pruner is involved.**
- **The events outbox aspect** (`vtx.op.<requestId>.events`) deliberately carries **no** TTL (it must outlive the 24h tracker so a long Processor/consumer outage never drops events — `outbox_aspect.go:34`, `step8_commit.go:180`), but the **outbox consumer tombstones it after a confirmed publish** (`internal/processor/outbox/consumer.go:108-111`, `c.conn.KVDelete(...)`). It is self-cleaning, not pruner-cleaned.

There is **no residual op-vertex class** that accumulates unbounded and lacks a retention mechanism. The pruner #49 has nothing to prune. The honest architect move — and the de-risking value of this Designer pass — is to **formally retire #49**, reconcile the stale `lattice-architecture.md:298`/`:679` against the frozen Contract #4 §4.3, and replace §10.4's "op-vertex pruner / retention are future consumers" line with the reconciled reality. (The arch-doc reconciliation is a *recommendation to the planning lead* — I do not edit planning artifacts; see §6.4.)

> **Why this matters and is not just bookkeeping:** keeping a phantom "future pruner" on the board and in the arch tree invites a future fire to *build dead scaffolding* — a Weaver sweep that GCs trackers NATS already GCs. Retiring it is the correct, grounded outcome.

### 1.3 Intent

One coherent platform increment: **(a)** specify + implement native `@every` recurring schedules as a first-class temporal-lane mode (build-to the reserved §10.4 seam), **(b)** prove + ship it by migrating the Weaver reconciler sweep onto it (the grounded cron-killer consumer — *not* dead scaffolding), and **(c)** retire #49 with the grounding above. The result: the platform gains durable recurrence, sheds an in-process cron, and closes a phantom backlog item — net code and concept both get *smaller*.

---

## 2. Grounding — the pattern I mirror (not a greenfield)

### 2.1 The existing one-shot temporal pattern (the precedent)

The `@every` consumer pattern is a **direct generalization of Weaver's `@at` temporal leg** (`internal/weaver/temporal.go`), which I mirror beat-for-beat:

| Concern | Weaver `@at` leg (precedent) | `@every` recurring (this design) |
|---|---|---|
| **Arm** | `Actuator.scheduleTimer` publishes one msg with `Nats-Schedule: @at <RFC3339>` + `Nats-Schedule-Target: schedule.weaver.timer.fired.<…>` (`weaver/actuator.go:104-113`) | a substrate helper publishes `Nats-Schedule: @every <duration>` + a fired-target within `schedule.>` |
| **Fire consumer** | a **fixed durable** `weaver-temporal` on `core-schedules`, filtered to the fired-subject prefix, on the `ConsumerSupervisor` (`temporalSpec`, `temporal.go:73`) | the same shape: a fixed durable, fired-prefix filter, on the owning component's `ConsumerSupervisor` |
| **Fired → op** | `handleFiredTimer` converts the firing into a `MarkExpired` op via the Processor (P2); never injected into `core-events` (§10.4) | same — a recurring fire drives whatever op/handler the consumer defines (the Weaver sweep is a *handler*, not an op — see §3.3) |
| **At-least-once dedup** | deterministic `requestId = derive(scheduleSubject + fireAt)` → Contract #4 tracker collapses redeliveries (`temporal.go:287`, §10.4 line 465) | per-occurrence `requestId = derive(scheduleSubject + occurrenceInstant)` — the *same* rule, occurrence-keyed (§3.2) |
| **Read-before-act** | `handleFiredTimer` re-reads the current `weaver-targets` row; a stale/superseded firing Acks without acting (`temporal.go:244-285`) | the sweep handler is already a level-reconcile over current `weaver-state` (`reconciler.go:142 pass()`) — inherently stale-safe; no per-fire staleness check needed |

The **bridge self-rescheduling `@at` chain** (`internal/bridge/schedule.go`) is the *anti-pattern* `@every` retires for fixed cadences: the bridge re-arms the next `@at` from inside each fire's handler. That is correct for the bridge's *dynamic, give-up-bounded* poll (each next-fire instant is computed from live state), so the bridge is **not** migrated here (§5.4) — but for a *fixed* cadence the self-re-arm is pure overhead the server-native `@every` removes.

### 2.2 Supervised-consumer + substrate boundary (the boundary I honor)

Every fired-schedule consumer runs on a `substrate.ConsumerSupervisor` (Loom runs all four durables on one; Weaver runs `weaver-temporal` on one). The `@every` fire consumer is one more `ConsumerSpec` on the owning component's existing supervisor — no new infrastructure. Publishing uses the existing `substrate.Conn.Publish(ctx, subject, data, header)` primitive (`internal/substrate/publish.go:34`) with the schedule headers; `internal/weaver` / `internal/loom` keep their `substrate/*`-only import boundary (no `nats.go`/`jetstream` leak).

### 2.3 NATS message-schedules — vendor facts (authoritative, NATS 2.12–2.14)

Grounded in the NATS docs, ADR-51, and the Synadia scheduling blog:

- **Modes:** `@at <RFC3339>` (one-shot, **2.12+**); `@every <duration>` and 6-field cron + subject sampling + timezones (**2.14+**). Lattice's floor is 2.12, "2.14 recommended" (Contract #4 §4.3). → **`@every` requires raising the effective floor to 2.14** (the decision in the For-Andrew block).
- **Recurrence:** for `@every`/cron the **schedule message persists and the server re-fires it indefinitely**; each firing generates a fresh message on the `Nats-Schedule-Target` subject. (`@at` auto-purges after its single delivery.)
- **Cancellation / retune:** publishing a new schedule to the **same subject replaces** the prior one (the server auto-applies `Nats-Rollup: sub`, consistent with our `MaxMsgsPerSubject: 1`). Hard stop = **purge the schedule subject** or **delete the schedule message by sequence**; atomic conditional stop = `Nats-Schedule-Next: purge`.
- **Fired-message headers:** the republished copy carries `Nats-Scheduler` (the schedule subject that produced it) and `Nats-Schedule-Next` (the next-invocation timestamp). These give a server-authoritative way to label each occurrence.
- **Target constraint (already in §10.4):** the fired target **must lie within `schedule.>`** (the stream's own subject space), else `JSMessageSchedulesTargetInvalidError` at publish time. Past-due `@every` ticks after a restart fire immediately (catch-up) — for a *level-reconcile* sweep this is harmless (one extra idempotent pass).

`Nats-Schedule-TTL` (→ `Nats-TTL` on the fired copy) lets a consumer discard stale fired deliveries; for the sweep we do **not** need it (the sweep is idempotent), but the helper exposes it for future consumers that want "skip if older than N."

Sources: [Synadia — Delayed Message Scheduling in NATS JetStream](https://www.synadia.com/blog/delayed-message-scheduling-nats-jetstream) · [NATS ADR-51](https://github.com/nats-io/nats-architecture-and-design/blob/main/adr/ADR-51.md) · [NATS 2.14 release notes](https://docs.nats.io/release-notes/whats_new/whats_new_214) · [NATS JetStream Headers](https://docs.nats.io/nats-concepts/jetstream/headers).

---

## 3. The shape

### 3.1 The `@every` substrate seam (Fire 1)

A thin, schedule-mode-agnostic helper on `internal/substrate` — the publish side already exists (`Conn.Publish` + the `ScheduleHeader`/`ScheduleTargetHeader` constants in `publish.go`); Fire 1 adds the recurring spelling + the constants the fired side reads:

```go
// internal/substrate/publish.go  (additive)
const (
    ScheduleHeader        = "Nats-Schedule"          // existing — value "@at <RFC3339>" OR "@every <dur>"
    ScheduleTargetHeader  = "Nats-Schedule-Target"   // existing
    ScheduleTTLHeader     = "Nats-Schedule-TTL"      // NEW (optional, opt-in stale-discard)
    SchedulerHeader       = "Nats-Scheduler"         // NEW — read off the fired copy (the schedule subject)
    ScheduleNextHeader    = "Nats-Schedule-Next"     // NEW — read off the fired copy (next-occurrence instant)
)

// ScheduleEvery arms a recurring schedule on the core-schedules stream: every
// `interval`, the server republishes `payload` to `target` (which MUST lie within
// schedule.>). Re-publishing the same `subject` REPLACES the prior schedule
// (per-subject rollup); CancelSchedule(subject) stops it. Requires NATS 2.14.
func (c *Conn) ScheduleEvery(ctx context.Context, subject, target string, interval time.Duration, payload []byte) error

// CancelSchedule purges the schedule subject (stops a recurring schedule).
func (c *Conn) CancelSchedule(ctx context.Context, subject string) error
```

`ScheduleEvery` formats `@every <Go-duration→NATS-duration>` (NATS accepts `1s`/`1m`/`1h`; `time.Duration.String()` is compatible for whole-unit values — the helper validates `interval > 0` and rejects sub-second to avoid hot-firing). It is a pure wrapper over `Publish` with the schedule headers — no new transport, no `jetstream` import in consumers.

> **Substrate boundary note (the one new internal seam):** `CancelSchedule` purges a subject on `core-schedules`. The substrate already exposes KV `Purge` (added in the Refractor migration) but **not** a stream-message purge. Fire 1 adds a minimal `Conn.purgeScheduleSubject` over the JetStream `Stream.Purge(StreamPurgeRequest{Subject})` API, confined to `internal/substrate` (Go API, **not** a contract). This mirrors how the substrate already wraps `EnsureStream`/`PublishCore`. No `substrate.EnsureKV`-style provisioning primitive is added (per [[no-substrate-ensurekv]] — provisioning stays in bootstrap; `core-schedules` is already primordial).

### 3.2 Per-occurrence determinism & dedup (the §10.4 rule, occurrence-keyed)

A recurring fire must be **idempotent under at-least-once redelivery** (a consumer crash before ack redelivers the *same* occurrence) while **distinct occurrences are distinct work**. The `@at` leg solves this with `requestId = derive(scheduleSubject + fireAt)`; the recurring leg uses the identical rule with the **occurrence instant**:

> **occurrenceInstant** = the fired message's JetStream store timestamp (`msg.Metadata().Timestamp`), truncated to the schedule granularity (whole seconds). It is fixed at store time → **stable across redeliveries** of that stored occurrence, and **distinct per occurrence** (each tick stores a new message). The server's `Nats-Schedule-Next` header is the alternative label; the design uses the store timestamp because it is the *current* occurrence (Next is the *following* one) and is available without parsing a header. Either is acceptable — §11 flags it as a settle-at-build detail.

For a fire that drives an **op** (the future generic case), `requestId = derive(scheduleSubject + occurrenceInstant)` → the Contract #4 `vtx.op.<requestId>` tracker collapses redeliveries; a new occurrence is a genuinely new op. For a fire that drives a **handler** with no op (the Weaver sweep — §3.3), idempotency is the handler's own property (the sweep is a level-reconcile; a redelivered occurrence is one extra harmless pass), so the requestId rule is moot there — but the helper computes it so any op-driving consumer inherits dedup for free.

### 3.3 The grounded consumer — the Weaver reconciler sweep, cron-killed (Fire 2)

The migration is deliberately minimal: **the sweep logic and its durable state are untouched; only the *trigger* moves** from the in-process `time.Ticker` to a fired `@every` schedule.

- **Arm (once, at engine start):** Weaver publishes a single recurring schedule
  `schedule.weaver.sweep` → target `schedule.weaver.sweep.fired`, `@every <SweepInterval>` (the existing `defaultSweepInterval`, 1 min). Re-publishing on every start is **idempotent** (per-subject rollup replaces) — so a restart, a config change to the interval, or a second replica all converge to one schedule.
- **Fire consumer:** a new fixed durable `weaver-sweep` on `core-schedules`, filtered to `schedule.weaver.sweep.fired`, on Weaver's existing `ConsumerSupervisor`. Its handler is **`sweeper.pass(ctx)`** — the exact body that runs today (`reconciler.go:142`), unchanged. One fired occurrence = one `pass`.
- **Why this is correct + safe (grounded):**
  - `pass()` is a **pure level-reconcile**: it lists current `weaver-state` marks and reconciles each against its live row + lease. It holds **no cross-pass in-memory state** that matters for correctness — the reclaim backoff is keyed on the mark's own `ClaimedAt` + `dispatchCount` in `weaver-state` KV (the ratified reclaim-backoff design), not in process memory. The only in-process state is metrics (`lastRunAt`, counters) and `corruptAlerted` (a per-process issue-dedup set) — both survive across fires within the engine's lifetime, unaffected by trigger source.
  - **Single-fire across replicas:** N Weaver replicas all arm `schedule.weaver.sweep` (idempotent rollup → one schedule); the fired occurrence is delivered to the `weaver-sweep` durable **once** → exactly one replica sweeps per interval. This is the multi-instance win the in-process ticker can't give.
  - **Overlap protection:** today the single goroutine + `time.Ticker` coalescing guarantees passes never overlap (`reconciler.go:121`). Under the durable consumer the same guarantee holds **per replica** (the supervised consumer processes one message at a time, max-ack-pending 1 for this durable). Across replicas, two passes can only overlap if delivery races a slow ack — and concurrent passes are already **safe by OCC** (the design's standing invariant; the in-process model already tolerated it in the multi-replica future). We set the `weaver-sweep` durable to `MaxAckPending: 1` so one replica never self-overlaps.
  - **Catch-up after downtime:** a `@every` schedule whose ticks elapsed while Weaver was down fires the missed occurrences immediately on reconnect. For a level-reconcile sweep that is **one (or a few coalesced) harmless extra passes** — exactly the semantics the `time.Ticker` "absorb dropped ticks" comment already documents.
- **Cutover:** Fire 2 removes the `time.NewTicker` loop (`reconciler.go:124-136`) and wires `pass` as the fire handler. The initial immediate `s.pass(ctx)` at startup is preserved as a one-shot warm sweep (so a cold start doesn't wait a full interval) **and** the recurring schedule is armed — belt-and-suspenders for the very first interval only.

> **Operator surface (free win):** because the sweep cadence is now a schedule message at `schedule.weaver.sweep`, Loupe / `lattice` CLI can later *show* and *retune* it (re-publish with a new `@every`) without a redeploy. Not in scope here, but the durable schedule is what makes it possible — exactly the #47 "operator-visible cron" intent.

### 3.4 Read path (P5) / write path (P2)

- **Write path (P2):** unchanged and honored. A recurring fire that drives state change does so **only** by submitting an op to the Processor (the `@at` precedent — `handleFiredTimer` → `MarkExpired`). The Weaver sweep already mutates state exclusively via ops (`reclaim`/`MarkExpired`/clears go through `core-operations`); the migration changes nothing about that. The schedule lane itself writes only to `core-schedules` (operational infra, P1) — never Core KV.
- **Read path (P5):** not applicable — `core-schedules` is operational infra, not a lens/read-model; no app reads it. The sweep reads `weaver-state` (Weaver-private operational KV, P1), as it does today. No new Core-KV read, no new lens.
- **P1:** the schedule lane is operational state (like Health KV / `loom-state` / `weaver-state`), bootstrapped as core infra (§10.4) — correctly outside Core KV.

### 3.5 Naming (Contract #1 discipline)

The schedule lane carries **subjects**, not vertices/aspects/links — Contract #1 key-shapes don't apply, but the §10.4 subject discipline does: `schedule.<domain>.<kind>.<token...>` with dot-free tokens. The new subjects:

- Schedule: `schedule.weaver.sweep` (domain `weaver`, kind `sweep`, no entity token — it is a singleton platform sweep, not per-entity).
- Fired target: `schedule.weaver.sweep.fired` (within `schedule.>`, per §10.4).

No new vertices/aspects/links are introduced anywhere in this design. (This is, deliberately, a design that *adds capability without adding graph shape*.)

---

## 4. Contract surface

### 4.1 Contract #10 §10.4 — amend (staged UNCOMMITTED in `main`)

§10.4 is FROZEN and already reserves `@every`. The amendment is a **specification of the reserved seam**, not a new shape — but because §10.4 is frozen, the edit goes to Andrew as the proposal. Exactly two changes (see §11 for line anchors):

1. **Recurring semantics block** — lift the parenthetical "or @every for recurring — Phase 2 uses @at one-shot" into a specified sub-section: the `@every`/cron schedule message **persists and re-fires**; cancellation = **replace** (re-publish same subject) / **purge subject** / `Nats-Schedule-Next: purge`; **requires NATS 2.14**; the fired copy carries `Nats-Scheduler` + `Nats-Schedule-Next`; the **per-occurrence deterministic-`requestId`** rule (subject + occurrence instant) extends the existing one-shot dedup rule verbatim.
2. **Future-consumers line (418)** — replace "op-vertex pruner / retention are future consumers" with the reconciled reality: tracker/op-vertex retention is discharged by **NATS per-key TTL (Contract #4 §4.3) + the outbox-tombstone**, not a schedule-lane pruner; the recurring lane's first consumer is the **platform recurring-sweep** (Weaver reconciler cron-kill).

**Affected consumers of §10.4:** Weaver temporal lane (gains the recurring spelling), the bridge async lane (unchanged — keeps its dynamic `@at` chain), bootstrap (`core-schedules` already provisioned; no stream-config change — `AllowMsgSchedules: true` already enables both modes, confirmed `primordial.go:198`). **No other contract is touched** — Contract #4 is *cited* (the retirement grounding), not changed.

### 4.2 NATS version floor (deployment, not a doc-contract)

Adopting `@every` requires NATS **2.14** at runtime. This is a deployment-config decision (the For-Andrew fork). If ratified as "bump the hard floor," the only artifact change is the dev/CI NATS image pin + a one-line note in Contract #4 §4.3 ("2.14 is now the floor for the recurring temporal lane") — staged with the §10.4 edit. If ratified as "opt-in / keep 2.12 floor," Fire 2 stays gated behind a 2.14 capability check and the `@at` chain remains the portable fallback (more code, recommended against).

### 4.3 What is explicitly **not** a contract change

The substrate helpers (`ScheduleEvery`/`CancelSchedule`), the new header constants, and the `weaver-sweep` consumer are all internal Go API / operational wiring — **not** contract surface. The `core-schedules` stream config is unchanged (already `AllowMsgSchedules: true`).

---

## 5. Migration, test & rollout strategy

### 5.1 Migration

- **`core-schedules`:** already primordial with `AllowMsgSchedules: true` — **no stream migration**. `@every` is enabled by the same flag as `@at`.
- **Weaver sweep cutover:** the `time.Ticker` → `@every` swap is a clean replacement within `internal/weaver`. On first start after the upgrade, Weaver arms `schedule.weaver.sweep` (idempotent); the old in-process ticker is deleted. **No data migration** (no schema, no graph shape). A rolling restart converges all replicas onto the one schedule.
- **Reversibility:** if the durable-sweep path ever misbehaves, reverting to the `time.Ticker` is a code-only rollback (the sweep *logic* is unchanged) plus a `CancelSchedule("schedule.weaver.sweep")` to clear the orphaned schedule. Low blast radius.

### 5.2 Test strategy

- **Fire 1 (substrate primitive):** an embedded-NATS (2.14) test that arms `@every 1s`, asserts ≥3 fired occurrences land at the target subject within a bounded window with **distinct** occurrence instants; `CancelSchedule` then stops further fires; re-publishing the same subject replaces (one active schedule, `MaxMsgsPerSubject:1` holds). A unit test on the per-occurrence `requestId` derivation (stable across a redelivered occurrence, distinct across occurrences). **Embedded-NATS fixtures MUST use `jsstore.Dir(t)`** for StoreDir to survive `-p 4` parallel teardown (per [[project_ci_test_parallelism]]).
- **Fire 2 (Weaver cron-kill):** an e2e on the ephemeral stack: arm an expired `weaver-state` mark, assert the durable `@every` sweep reclaims it within ~1 interval (no in-process ticker present); assert **single-fire** by running two Weaver engines against one `core-schedules` and asserting exactly one `pass` per interval (the durable delivers once). Re-use the existing reclaim e2e harness; the only new assertion is "the sweep ran from a fired schedule, not a ticker."
- **Regression:** the full reclaim/orphan-GC suite (`internal/weaver/...`) runs unchanged — `pass()` is byte-identical, so its existing tests are the safety net proving the migration is behavior-preserving.
- **Gates:** `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`, Gate-2 (`make test-bypass`, all BLOCKED), Gate-3 (`make test-capability-adversarial`, all DEFENDED), `go test ./internal/weaver/... ./internal/substrate/...`. No new Gate-2/3 vectors (the schedule lane introduces no auth surface — a fired schedule drives an op that passes through the *normal* step-3 auth on its own `operationType`, unchanged).

### 5.3 Rollout / CI

- CI's embedded NATS image must be **2.14** for the Fire-1 tests (the version-floor decision). If Andrew bumps the floor, this is the image pin; if opt-in, Fire 1's recurring tests gate on a 2.14 check.
- Doc-only commits skip CI (paths-ignore); the §10.4 edit is uncommitted (Andrew commits it), so no CI interaction there.

---

## 6. Risks & alternatives

### 6.1 Risk — the schedule message is the sole sweep trigger (single point of failure)

If `schedule.weaver.sweep` is ever purged/lost and not re-armed, sweeps stop silently. **Mitigations:** (a) Weaver **re-arms on every engine start** (idempotent), so any restart self-heals; (b) the Weaver heartbeat already surfaces `sweepLastRunAt` (`weaver/health.go:228`) — a stalled `sweepLastRunAt` is a Lamplighter-detectable anomaly (the existing §5 health channel covers it); (c) the startup warm `pass()` (§3.3 cutover) guarantees at least one sweep per process even if the schedule never fires. Residual risk is bounded and observable — strictly better than the in-process ticker, whose stall is *also* invisible today.

### 6.2 Risk — NATS 2.14 floor

Covered as the For-Andrew fork. 2.14 has been GA and is already "recommended." Recommendation: bump. If declined, the opt-in path keeps 2.12 working at the cost of carrying the `@at`-chain fallback for any recurrence — more code, the thing #47 set out to delete.

### 6.3 Risk — catch-up storm after long downtime

A `@every 1m` schedule down for an hour could fire ~60 coalesced occurrences on reconnect. For a level-reconcile sweep this is idempotent but wasteful. **Mitigation:** NATS coalesces missed `@every` ticks (it fires the overdue occurrence, not 60 copies, per ADR-51 catch-up semantics); and `MaxAckPending:1` serializes whatever does land. Acceptable; noted for the §10 adversarial check.

### 6.4 Alternative considered — keep the in-process ticker, do nothing

Rejected: leaves #47 unbuilt, the cron invisible, and the multi-instance future broken. But it is the *low-cost status quo*, so Fire 2 is structured to be independently revertible (§5.1).

### 6.5 Alternative considered — migrate the bridge poll chain too

Rejected (for now): the bridge poll is a **dynamic** schedule (next-fire computed from live poll state, give-up-bounded) — a poor fit for fixed-cadence `@every`. Its self-rescheduling `@at` chain is correct. Left as a *candidate future consumer* only if its cadence ever becomes fixed.

### 6.6 Alternative considered — build a real op-vertex pruner anyway (#49 as designed)

Rejected with grounding (§1.2): NATS per-key TTL + the outbox-tombstone already GC every op-vertex class; a pruner would be dead scaffolding sweeping keys NATS already removes. The correct outcome is retirement, not construction.

### 6.7 Reconciliation owed to the planning lead (not edited here)

`lattice-architecture.md:298` ("pruning trackers handled by Weaver"), the `weaver/pruner/` tree slot (`:679`), and the `weaver/nudge/` slot (`:678`, already retired by Epic 13.5) are **stale**. Planning artifacts are the planning lead's — I do **not** edit them. This design **recommends** the planning lead reconcile §298/§679 against Contract #4 §4.3 (tracker GC = NATS TTL) and drop the vestigial `pruner/`/`nudge/` tree entries. Flagged, not actioned.

---

## 7. Fire-by-fire decomposition (for the Lattice Steward)

Each fire is independently shippable + green, with a real consumer (no dead scaffolding).

### Fire 1 — the `@every` substrate primitive *(full review — substrate/temporal plane)*
- Add `ScheduleEvery` / `CancelSchedule` + the new header constants to `internal/substrate` (publish.go); add the confined `Stream.Purge(subject)` wrapper.
- Add the per-occurrence `requestId` derivation helper (mirrors `deriveTimerRequestID`).
- Tests per §5.2 Fire 1 (embedded NATS 2.14, `jsstore.Dir(t)`).
- **Ships green on its own** (a new substrate capability with test coverage; no consumer yet — but it is a *primitive*, the standard substrate-seam pattern, not feature scaffolding). Gate: build/vet/lint/`go test ./internal/substrate/...`.
- **Full 3-layer adversarial review** (new substrate/transport primitive; the §10 boundary checks below are the review's seed).

### Fire 2 — Weaver reconciler sweep → durable `@every` (the cron-kill; the grounded consumer)
- Arm `schedule.weaver.sweep` at engine start (idempotent); add the `weaver-sweep` fixed durable (`MaxAckPending:1`) on Weaver's `ConsumerSupervisor`, handler = `sweeper.pass`.
- Remove the `time.NewTicker` loop; keep the startup warm `pass`.
- Tests per §5.2 Fire 2 (reclaim e2e via the schedule; two-engine single-fire assertion).
- **Ships green; this is the today-consumer** — the in-process cron is gone, the durable one replaces it. Gate: full Weaver suite (proves behavior-preserving) + the new e2e.
- **Scaled review:** thorough lead review *plus* a focused adversarial pass on the single-fire / catch-up / overlap boundary (§6.1/§6.3) — the sweep logic is unchanged, but the trigger change touches a correctness-critical loop, so the lead must explicitly sign off the concurrency boundary (overridable note).

### Fire 3 — retire #49 + the §10.4 edit lands *(doc/contract; Andrew-gated commit)*
- Once Andrew commits the §10.4 amendment (§4.1), update `docs/components/weaver.md` (the sweep is now schedule-driven, cron-killed) and `docs/components/loom.md`/bridge as cross-refs if they cite the temporal lane.
- Surface the planning-lead reconciliation recommendation (§6.7) — a note, not an edit.
- Mark the backlog row ✅ done; close #49 as *retired-superseded* with the Contract #4 §4.3 grounding.
- **Optional follow-on (NOT in this design's scope, flagged):** a *new* second `@every` consumer — e.g. a recurring **wedged-Loom-instance audit** (a `loom-state` instance that never reaches terminal has no GC today; a periodic audit surfacing it to Health would be a genuinely-new recurring-sweep consumer). This is a *candidate*, filed for the Surveyor — not built here.

---

## 8. Open ratification items (decide-don't-defer — my calls, for Andrew to confirm)

1. **NATS 2.14 hard floor** *(the fork)* — **recommend: bump.** (§2.3, For-Andrew.)
2. **§10.4 amendment** — staged uncommitted; **the diff is the proposal.** (§4.1, §11.)
3. **Occurrence-instant source** — store timestamp (recommend) vs. `Nats-Schedule-Next` header. Settle-at-build; both correct. (§3.2.)
4. **Weaver-sweep review depth** — thorough-lead + focused concurrency-boundary adversarial pass (recommend) vs. full 3-layer. (§7 Fire 2.) Given the sweep *logic* is unchanged and only the *trigger* moves, thorough-lead + the targeted pass is proportionate — flagged so it can be overridden up.
5. **Pre-build party-mode pass** — this is a cross-cutting temporal-plane design touching a correctness-critical loop. **Recommend a `bmad-party-mode` / adversarial pass on §10 before Fire 1 builds** (the single-fire + catch-up + occurrence-dedup boundaries), folding findings in — consistent with the D1 / FR28 designs' pre-build gate.

---

## 9. What this design deliberately does NOT do

- Does **not** build a pruner (#49 retired, §1.2/§6.6).
- Does **not** migrate the bridge poll chain (§6.5) or any Health heartbeater (too-frequent liveness loops belong in-process).
- Does **not** add cron-expression or timezone scheduling (2.14 enables them; no consumer needs them yet — leave to a future fire).
- Does **not** edit planning artifacts (§6.7) or commit the §10.4 contract edit (Andrew's).
- Does **not** add an operator pause/retune UI for the sweep cadence (the durable schedule *enables* it; building it is later Loupe/CLI work).

---

## 10. Self-adversarial pass (folded in)

- **"A recurring fire double-acts on redelivery."** → Per-occurrence deterministic `requestId` collapses op-driving fires on the Contract #4 tracker; the Weaver sweep is a handler-side level-reconcile (idempotent by construction). Both legs covered. ✓
- **"Two replicas both sweep."** → The fired occurrence is delivered to the single `weaver-sweep` durable once → one replica per interval. Even if a delivery races a slow ack across replicas, concurrent `pass()` is OCC-safe (standing invariant). `MaxAckPending:1` prevents self-overlap. ✓
- **"The schedule message is lost → sweeps stop silently."** → Re-armed on every start (idempotent) + startup warm pass + `sweepLastRunAt` health stall is Lamplighter-detectable. Strictly better than the silently-stalling ticker today. ✓ (§6.1.)
- **"Catch-up storm after downtime."** → NATS coalesces overdue `@every` ticks; `MaxAckPending:1` serializes; a level-reconcile pass is idempotent. ✓ (§6.3.)
- **"`@every` raises the version floor under everyone's feet."** → Explicit fork, flagged, with an opt-in fallback if declined. ✓ (§2.3, §6.2.)
- **"Is the op-vertex pruner *really* dead, or did you miss a class?"** → Enumerated the op-vertex classes: tracker (`vtx.op.<id>` — NATS 24h TTL), events outbox aspect (`.events` — outbox-tombstoned on publish). No third class accumulates. The auth-trace Health KV (1h TTL), `weaver-state` marks (2×lease TTL), `loom-state` instances (deleted at terminal) are operational, not op-vertices, and each has its own lifecycle. The one *genuinely* un-GC'd operational residual is a **wedged Loom instance** — surfaced as a candidate future `@every` consumer (§7 Fire 3), not a pruner. ✓
- **"P2/P5 violation sneaking in?"** → The lane writes only `core-schedules` (P1 operational infra); state change is op-only (P2); no app reads the lane (P5 n/a). ✓

---

## 11. §10.4 contract edit — line anchors (for Andrew, to disentangle from co-pending edits)

`docs/contracts/10-orchestration-surfaces.md` already shows uncommitted edits from **other** fires (lane-authorization §2.x is in `02-*`; check `git diff` for what's mine in `10-*`). **My** §10.4 edit is confined to:

- **Line ~418** — replace the clause *"op-vertex pruner / retention are future consumers"* with the reconciled retention reality (NATS TTL + outbox-tombstone; first recurring consumer = the platform sweep).
- **Line ~427** — expand the `header:` note's *"or @every for recurring — Phase 2 uses @at one-shot"* parenthetical into a specified **Recurring schedules (`@every` / cron)** sub-block immediately after the §10.4 fenced block: recurrence persists + re-fires; cancellation (replace / purge / `Nats-Schedule-Next: purge`); requires NATS 2.14; fired copy carries `Nats-Scheduler` + `Nats-Schedule-Next`; per-occurrence deterministic-`requestId` rule (subject + occurrence instant) — a verbatim extension of the existing one-shot dedup rule at lines 465-468.

I have **not** staged this edit (`git add` excludes it). It sits uncommitted in `main` as the proposal; Andrew commits it on ratification (per [[feedback_ratified_contract_commit]]).

---

*Designer: Winston (lattice-designer) · 2026-06-28 · grounded in `lattice-architecture.md` (D2/D3/#47/#49, :298/:679/:1165), Contract #4 §4.3, Contract #10 §10.4, `internal/weaver/{temporal,reconciler,actuator}.go`, `internal/processor/{tracker,step8_commit,outbox/consumer}.go`, `internal/substrate/publish.go`, `internal/bootstrap/primordial.go`, brainstorm #47/#49/#119, and the NATS 2.14 message-schedules vendor docs (ADR-51 / Synadia).*
