# Story 8.5: Loom adopts ConsumerSupervisor — teardown, backoff, filter-reset, Health KV (F6 hardening + observability)

Status: done

## Story

As a platform operator,
I want Loom's durables (trigger, per-domain completion, relay, deadline-watcher) driven through the substrate `ConsumerSupervisor`,
So that a removed pattern leaks no consumer, a sustained failure backs off instead of hot-looping, a filter change recreates cleanly, and Loom's liveness is observable in Health KV.

## Acceptance Criteria

From `_bmad-output/planning-artifacts/epics/phase-2-epics.md` § Story 8.5 (source of truth; do not edit it).

1. **All four Loom durables run on `substrate.ConsumerSupervisor`** (8.4): the fixed trigger consumer
   (`loom-trigger`, `events.loom.patternStarted`), the per-domain completion consumers (`loom-<domain>`,
   dynamic/reconciled), the outbox relay (`loom-outbox-relay`, KV-CDC on `loom-state` `outbox.>`), and the
   deadline watcher (`loom-deadline`, KV-CDC on `loom-state` `deadline.>`). `loom` continues to import only
   `substrate/*` (8.1 AC#8) — **no `jetstream`/`nats.go` import appears anywhere in `internal/loom`**.
2. **`reconcileConsumers` becomes a desired-vs-running diff** over `Pattern.Domains()` aggregated across the
   pattern snapshot (`bindingRegistry(e.source.snapshot())`), driven through the supervisor's `Add`/`Remove`/
   `Reset`:
   - A domain newly referenced by any pattern → `Add` a `loom-<domain>` spec (additive case, unchanged
     behavior from today).
   - A domain referenced by NO pattern in the current snapshot (the **last** pattern referencing it was
     removed/updated-away) → `Remove` (supervisor stops the pump **and deletes the JetStream durable** —
     "no leaked consumer" is the F6 guarantee; an un-pumped server-side durable IS the leak). Correctness on
     a future re-add rests on `loom-state` + Contract #4 idempotency + `token.` pointer presence (a
     `DeliverAll` replay on re-add is safe and its cost is accepted) — NOT a preserved ack floor.
   - A domain whose **desired filter/config differs** from the running durable's (in practice: the filter
     subject `events.<domain>.>` never changes for a fixed domain name, so this branch is mechanically
     reachable only if a future spec field changes; implement the diff so it is generic — compare the
     desired `ConsumerSpec` against the last-applied one and call `Reset` on any difference) → `Reset`
     (delete-and-recreate), **never silently unchanged** (covers Story 8.2 review ECH Path #3).
   - Reconcile runs on every pattern load/update/remove callback (`source.setLoadCallback` /
     `setUpdateCallback`, including `removePattern`'s `updateCB(nil, nil)` signal) — same triggers as today,
     now resolving to a real diff instead of an additive-only map.
3. **The relay's publish-failure path returns `NakWithDelay`** (bounded cadence, unbounded count —
   at-least-once preserved; `RedeliveryDelay` left at its zero value so the supervisor applies
   `substrate.DefaultRedeliveryDelay` = 5s; **no `MaxDeliver`** on any Loom-created consumer) — subsumes the
   former F4 story on the Loom side. A handler returning `NakWithDelay` must not hot-loop at zero delay
   (tested).
4. **Loom publishes `health.loom.<instance>` to Health KV** (Contract #5; bucket `health-kv`,
   `bootstrap.HealthKVBucket`) via a Contract-#5-shaped heartbeater (mirroring
   `internal/refractor/health.LatticeHeartbeater` — Loom may NOT import `internal/refractor/health`; build a
   loom-local equivalent or a substrate-shared one if trivial, but do not cross the package boundary):
   - `status` / `heartbeatAt` / `metrics` (running instance count = 1 per process, per-domain consumer
     states, relay + deadline-watcher liveness) / `issues` per Contract #5 §5.2.
   - Loom additionally implements `substrate.HealthSink` (per the supervisor's caller-keyed sink contract,
     8.4 AC 1d) for **per-consumer pause-state** persistence — this is a SEPARATE, smaller KV entry/shape
     from the Contract #5 heartbeat document; the supervisor never invents keys, so Loom picks the key(s)
     (e.g. `health.loom.<instance>.consumer.<name>` or an equivalent caller-chosen scheme — document the
     choice). Consumer pause-state **persists and restores across a Loom restart** (supervisor `Add`-time
     restore semantics, 8.4 AC 1d / Q2 ruling: manual > structural > infra precedence).
5. **Tests assert** (new, `internal/loom/...`, embedded-NATS per existing patterns):
   - a removed-pattern domain's `loom-<domain>` consumer is torn down AND its JetStream durable deleted
     (assert via a test-side jetstream handle, mirroring 8.4's `consumer_supervisor_test.go` pattern);
   - a filter/config change on a domain spec triggers `Reset` (durable recreated);
   - the relay's `NakWithDelay` does not hot-loop at zero delay (redelivery does not arrive before the floor,
     generous CI tolerance);
   - a well-formed Contract-#5 `health.loom.<instance>` heartbeat is written (status/heartbeatAt/metrics/issues
     all present, schema matches §5.2);
   - consumer pause-state survives a Loom engine restart (pause via supervisor `Pause`, restart the engine
     against the same conn/buckets, assert the consumer restores paused without `Resume`).
6. **`go test ./internal/refractor/...` and `./internal/substrate/...` stay green, UNTOUCHED** (this story is
   the mirror image of 8.4 — it does not edit `internal/substrate` or `internal/refractor`; if a genuine
   substrate gap is found, STOP and write it up in Questions for Winston rather than editing substrate).

## Tasks / Subtasks

- [x] **Task 1: Loom's `ConsumerSpec` builders + a single `*substrate.ConsumerSupervisor` on `Engine`** (AC 1)
  - [x] Add a `*substrate.ConsumerSupervisor` field to `Engine`, constructed in `NewEngine` via
    `substrate.NewConsumerSupervisor(conn)`.
  - [x] Write four `ConsumerSpec` builders (one per durable), each returning a fully-populated
    `substrate.ConsumerSpec` — Stream, FilterSubject, DeliverPolicy, DeliverGroup (none needed — Loom has no
    queue-group fan-out today; leave empty unless a builder needs one), `RedeliveryDelay` (zero except the
    relay, see Task 3), Handler, Classify, Probe, Health, Logger:
    - `triggerSpec()` — Name `loom-trigger`, Stream `e.cfg.EventsStream`, FilterSubject `triggerSubject`
      (`events.loom.patternStarted`), `DeliverPolicy: substrate.DeliverAll`, Handler wraps
      `e.handleTrigger`.
    - `domainSpec(domain string)` — Name `"loom-" + domain`, Stream `e.cfg.EventsStream`, FilterSubject
      `"events." + domain + ".>"`, `DeliverPolicy: substrate.DeliverAll`, Handler wraps `e.handleCompletion`.
    - `relaySpec()` — Name `loom-outbox-relay`, Stream `"KV_" + e.cfg.LoomStateBucket`, FilterSubject
      `"$KV." + e.cfg.LoomStateBucket + "." + outboxPrefix + ">"`, `DeliverPolicy: substrate.DeliverAll`
      (KV-CDC streams keep history=1 per ADR-15 — confirm whether `DeliverLastPerSubject` is required here;
      the existing `RunDurableConsumer` call for the relay does not set a deliver policy override today, so
      match its current effective behavior — if uncertain, prefer `DeliverLastPerSubject` for KV-CDC
      consistency with Refractor's Core-KV consumer and note the choice in Dev Notes), Handler wraps
      `r.handle` (the relay's existing `handle` method — see Task 3 for the `NakWithDelay` change).
    - `deadlineSpec()` — Name `loom-deadline`, Stream `"KV_" + e.cfg.LoomStateBucket`, FilterSubject
      `"$KV." + e.cfg.LoomStateBucket + "." + deadlinePrefix + ">"`, `DeliverPolicy: substrate.DeliverAll`
      (same KV-CDC deliver-policy note as relaySpec), Handler wraps `e.handleDeadline`.
  - [x] **Handler signature adaptation**: today's handlers are `substrate.HandlerFunc` (`func(ctx, msg)
    Decision`); the supervisor's `SupervisedHandler` is `func(ctx, msg) (Decision, error)`. Adapt each
    existing handler with a thin wrapper returning `(decision, nil)` — the existing handlers already encode
    all their failure handling as `Decision` values (Ack/Nak/Term), so `Classify`/`Probe` are NOT exercised
    on the existing error paths; only the relay's NEW `NakWithDelay` path (Task 3) needs the richer surface,
    and it still returns `(NakWithDelay, nil)` — no error/Classify needed. **Conservative default**: set
    `Classify: func(err error) substrate.FailureClass { ... }` per Dev Notes "Classify hook" below, and
    `Probe` only where meaningful (Dev Notes).
  - [x] `Engine.Start` replaces the four `go e.runXxxConsumer(ctx)` / `go e.relay.run(ctx)` /
    `go e.runDeadlineWatcher(ctx)` goroutines with `supervisor.Add(ctx, spec)` calls for the three fixed
    specs (trigger, relay, deadline) at startup. The supervisor owns the pump goroutines — `Start` no longer
    spawns them directly. `reconcileConsumers` (Task 2) handles the dynamic per-domain set.
  - [x] On `Engine` shutdown (ctx cancellation), the supervisor's `Stop()` is called (stops pumps, preserves
    durables for the FIXED three — trigger/relay/deadline keep their ack-floor position, matching substrate
    doctrine: "a durable's persisted position is the point of its durability"). Per-domain consumers are
    NOT force-removed on shutdown — only on a live reconcile diff (Task 2). Wire `Stop()` into `Start`'s
    `<-ctx.Done()` return path.

- [x] **Task 2: `reconcileConsumers` becomes a desired-vs-running diff with teardown + Reset** (AC 2)
  - [x] Track the **last-applied desired domain set** (e.g. `map[string]substrate.ConsumerSpec` or just
    `map[string]struct{}` if specs never vary by domain beyond the name) alongside (or replacing)
    `domainConsumers map[string]context.CancelFunc` — the supervisor now owns the pump lifecycle, so the
    `context.CancelFunc` bookkeeping is no longer needed; replace with whatever minimal state the diff needs.
  - [x] On each reconcile: compute `desired := bindingRegistry(e.source.snapshot())` (existing helper,
    unchanged). For each `d` in `desired` not in the last-applied set → `supervisor.Add(ctx, domainSpec(d))`.
    For each `d` in the last-applied set not in `desired` → `supervisor.Remove(ctx, "loom-"+d)` (deletes the
    durable — F6). For each `d` in both whose spec differs from last-applied → `supervisor.Reset(ctx,
    "loom-"+d)` (or `UpdateSpec` + `Reset` if the spec itself changed, per 8.4's `UpdateSpec` API — see
    `internal/substrate/consumer_supervisor.go` `UpdateSpec`).
  - [x] **Caveat to restate (do not solve)**: removing a pattern while an in-flight instance of it exists
    orphans that instance's completion (its `loom-<domain>` consumer is torn down before the instance
    advances). This is UNCHANGED by delete-vs-preserve (the same orphaning would occur if the consumer were
    merely stopped, since no consumer means no completion delivery either way) — note this in
    `docs/components/loom.md` as a known limitation, do not build mitigation.
  - [x] `reconcileConsumers` needs a `context.Context` for the `Add`/`Remove`/`Reset` calls — today it's
    called from `loadCB`/`updateCB` closures with no ctx in scope; thread `e.ctx` (already stored on
    `Engine` from `Start`) through, with a nil-check / no-op guard if `e.ctx` is nil (defensive — shouldn't
    happen post-`Start`).
  - [x] Concurrency: `reconcileConsumers` already holds `e.mu`; supervisor calls (`Add`/`Remove`/`Reset`) may
    block briefly on JetStream RPCs — confirm holding `e.mu` across these calls doesn't deadlock against
    other `e.mu` users (`reconcileConsumers` is the only current locker; verify no supervisor callback
    re-enters `e.mu`).

- [x] **Task 3: Relay `NakWithDelay` on publish failure** (AC 3)
  - [x] In `relay.handle` (`internal/loom/actuator.go`), change the publish-failure return from
    `substrate.Nak` to `substrate.NakWithDelay`:
    ```go
    if err := r.conn.Publish(ctx, "ops."+rec.Lane, data, nil); err != nil {
        r.logger.Error("loom relay: publish failed; nak with delay", "requestId", rec.RequestID, "err", err)
        return substrate.NakWithDelay
    }
    ```
    (Also consider the KV-delete-failure Nak just below — Dev judgment: a delete failure after a successful
    publish is a different failure mode (the op already landed; only the outbox cleanup failed) — leaving it
    as plain `Nak` is defensible since redelivery just re-publishes (idempotent) and retries the delete; if
    changed to `NakWithDelay` too, document why in Dev Notes. Do not change the `Term` paths.)
  - [x] `relaySpec()`'s `RedeliveryDelay` is left at zero (→ `substrate.DefaultRedeliveryDelay` = 5s per 8.4
    Q4 ruling) unless a different floor is justified — document the choice.
  - [x] Adapter signature note: `relay.handle` is currently `func(ctx, msg substrate.Message)
    substrate.Decision` (matches `substrate.HandlerFunc`). Wrap it (or change its signature directly, dev's
    choice — `relay.handle` has no other callers) to satisfy `substrate.SupervisedHandler` (`(Decision,
    error)`); the `NakWithDelay` return needs no error.

- [x] **Task 4: Health KV — Contract #5 heartbeat (`health.loom.<instance>`)** (AC 4, part 1)
  - [x] New file `internal/loom/health.go` (or similar) implementing a Loom-local heartbeater mirroring
    `internal/refractor/health.LatticeHeartbeater` (`internal/refractor/health/lattice_heartbeater.go`) —
    SAME Contract #5 §5.2 document shape (`key`/`component`/`instance`/`version`/`status`/`heartbeatAt`/
    `startedAt`/`uptime`/`metrics`/`issues`), `component: "loom"`, `instance: e.cfg.Instance`, bucket
    `bootstrap.HealthKVBucket` (`"health-kv"`), 10s default interval (NFR-O1 floor).
  - [x] `metrics` for Loom (component-author's discretion per §5.4 "TBD in Phase 2; conventions will follow
    this pattern" — define a sensible baseline):
    - `runningInstances` — count of `loom-state instance.<id>` entries with `status=running` (a KV scan of
      the `instance.` prefix; bound the cost — a periodic heartbeat-interval scan is acceptable, do NOT scan
      per-message).
    - `consumers` — map of consumer name → state string (e.g. `"running"|"pausedInfra"|"pausedStructural"|
      "pausedManual"`) for `loom-trigger`, each `loom-<domain>`, `loom-outbox-relay`, `loom-deadline` — a
      simple liveness/pause snapshot the supervisor can answer (check whether `ConsumerSupervisor` exposes a
      state-introspection method post-8.4; if not, Loom may need to track pause-state itself via its
      `HealthSink` writes — see Task 5 — and read its own cache for the heartbeat. If genuinely no
      introspection path exists without a substrate change, note it in Questions for Winston rather than
      editing substrate).
  - [x] `issues` — empty `[]` at MVP unless an obvious always-on check exists (e.g. a consumer stuck in
    `pausedStructural` → one `issue` entry, `severity: "warning"`, `code: "ConsumerPaused"`). Keep minimal;
    do not over-build.
  - [x] Wire `go heartbeater.Run(ctx)` into `Engine.Start`, alongside the supervisor `Add` calls.
  - [x] `cmd/loom/main.go` needs no changes if the heartbeater is fully owned by `Engine.Start` (preferred —
    keeps `cmd/loom/main.go` minimal, matching its current shape). If `bootstrap.HealthKVBucket` needs
    threading into `loom.Config`, add a `Config.HealthKVBucket` field defaulting to `bootstrap.HealthKVBucket`
    in `withDefaults()` (Loom's `internal/loom` package may import `internal/bootstrap` — confirm this is
    already the case; `engine.go` does not currently import bootstrap, but `cmd/loom/main.go` does. If
    `internal/loom` importing `internal/bootstrap` is undesirable, hardcode `"health-kv"` as the default
    with a comment, or pass the bucket name as a `Config` field set by `cmd/loom/main.go` from
    `bootstrap.HealthKVBucket` — dev's choice, document it).

- [x] **Task 5: HealthSink — per-consumer pause-state persist + restore** (AC 4, part 2)
  - [x] Implement `substrate.HealthSink` for Loom — `SetActive(ctx)`, `SetPaused(ctx, reason, lastErr)`,
    `Load(ctx) (HealthStatus, PauseReason, error)` — per consumer. Each managed consumer
    (`loom-trigger`/`loom-<domain>`/`loom-outbox-relay`/`loom-deadline`) gets ITS OWN sink instance, keyed
    by a Loom-chosen key. Candidate key shape: `health.loom.<instance>.consumer.<name>` in the `health-kv`
    bucket (a SEPARATE key from the Contract #5 heartbeat `health.loom.<instance>` — do not conflate the
    two documents). Document the chosen shape in `docs/components/loom.md`.
  - [x] Sink implementation is a thin `substrate.Conn.KVGet`/`KVPut` wrapper (Loom already uses these
    primitives elsewhere, e.g. `state.go`) — a small JSON document `{status, pauseReason, lastError}`
    mirroring the minimal fields `HealthSink.Load` needs. This does NOT need to match
    `internal/refractor/health.Entry` byte-for-byte (that schema is Refractor's and stays put per 8.4) —
    Loom defines its own minimal shape.
  - [x] Wire each `ConsumerSpec.Health` field to its per-consumer sink instance in the Task 1 builders.
  - [x] **Restore-across-restart test** (AC 5): `Pause` a consumer via the supervisor's `Pause(ctx, name)`
    (operator control surface — Loom may not expose this externally yet, but the supervisor API is
    available for the test to call directly), confirm the sink persists `PausedManual`, construct a NEW
    `Engine`/supervisor against the same conn+buckets (simulating a restart), confirm the consumer restores
    into `PausedManual` (does not pump) without an explicit `Resume` — exactly the 8.4 `Add`-time restore
    semantics (`docs/components/substrate.md` "At startup, `Add` restores from the sink").

- [x] **Task 6: Classify/Probe hooks — minimal, conservative defaults**
  - [x] Define `loomClassify(err error) substrate.FailureClass` — used only where a handler now CAN return
    an error (today's handlers return bare `Decision`, not `(Decision, error)` with non-nil error in any
    live path; this hook exists for forward-compatibility and any genuinely-infra failure surfaced via the
    adapted handler signature). Conservative default: unrecognized errors → `substrate.ClassTransient`
    (the package zero value/default already does this — an explicit `Classify` may be omittable; dev's
    call). If a concrete Infra case is identified (e.g. `loom-state` bucket unavailable — a NATS-connection
    failure surfacing through `e.state.*` calls), map it to `substrate.ClassInfra`; a deleted/missing
    pattern-source bucket (structural misconfiguration) → `substrate.ClassStructural`. Keep this MINIMAL —
    a nil `Classify` (→ always `ClassTransient`) is an ACCEPTABLE deviation if no handler path realistically
    produces a non-nil error; note the deviation in Dev Notes rather than inventing speculative error paths.
  - [x] Define `loomProbe(ctx) error` only if a `ClassInfra` path is wired (Task 6 above) — e.g. probe
    `e.conn.KVGet` against the `loom-state` bucket's a sentinel key, or omit entirely (nil `Probe` makes an
    infra pause behave like structural — acceptable per 8.4 docs if Loom has no genuine infra-recovery
    signal). Document the choice.

- [x] **Task 7: Update `docs/components/loom.md`** (house rule: new docs → `/docs`)
  - [x] Document the supervisor adoption: all four durables now run on `substrate.ConsumerSupervisor`;
    `reconcileConsumers`'s desired-vs-running diff (Add/Remove/Reset semantics, F6 teardown-deletes-durable);
    relay `NakWithDelay` backoff (subsumes F4); the `health.loom.<instance>` Contract #5 heartbeat shape +
    metrics; the per-consumer HealthSink key shape + pause-restore semantics.
  - [x] Restate the in-flight-instance-orphaned-on-teardown caveat (Task 2) as a documented known limitation,
    not a defect to fix here.

- [x] **Task 8: Tests + verification gates** (AC 5, 6)
  - [x] New/updated tests in `internal/loom/...` (follow `internal/loom/loom_e2e_test.go` and
    `internal/substrate/consumer_supervisor_test.go` embedded-NATS patterns):
    - removed-pattern domain consumer torn down + durable deleted (test-side jetstream handle to assert
      `DeleteConsumer` / `ErrConsumerNotFound`).
    - filter/config change → `Reset` recreates the durable.
    - relay `NakWithDelay` does not hot-loop at zero delay.
    - `health.loom.<instance>` heartbeat well-formed per Contract #5 §5.2.
    - consumer pause-state survives engine restart.
  - [x] Existing `internal/loom` test suites (`loom_e2e_test.go`, unit tests for `engine.go`/`actuator.go`/
    `pattern.go`/`source.go`) — every behavioral assertion preserved; mechanical rewires to new
    supervisor-based wiring permitted, behavior changes are not (this is the regression net, mirroring 8.4's
    Task 8 for Refractor).
  - [x] Verification gates (all, before declaring done): `go build ./...`, `make vet`, `golangci-lint run
    ./...`, `make verify-kernel`, `make test-bypass` (Gate 2, all BLOCKED), `make test-capability-adversarial`
    (Gate 3, all DEFENDED/baseline), `go test ./internal/loom/... ./internal/substrate/...
    ./internal/refractor/...` (substrate + refractor must stay green UNTOUCHED — the mirror image of 8.4,
    which left `internal/loom` untouched).

## Dev Notes

### Adjudicated decisions (binding — encode, do not re-litigate)

1. **Teardown DELETES the durable** (`supervisor.Remove`). When the LAST pattern referencing a domain is
   removed (diff over the AGGREGATED domain set via `bindingRegistry(e.source.snapshot())`), tear down
   `loom-<domain>` and delete its JetStream durable. F6's guarantee is "no leaked consumer" — an un-pumped
   server-side durable IS the leak. Correctness on re-add rests on `loom-state` + Contract #4 idempotency +
   `token.` pointer presence (replayed completions for cleaned instances find no token pointer → no-op), NOT
   a preserved ack floor. A `DeliverAll` replay on re-add is safe; its cost is accepted.
2. **Caveat (do not solve)**: removing a pattern while an in-flight instance of it exists orphans that
   instance's completion — out of scope here (unchanged by delete-vs-preserve); document, don't fix.
3. **Relay backoff**: publish-failure path returns `substrate.NakWithDelay` — bounded cadence, UNBOUNDED
   count (at-least-once preserved; never set `MaxDeliver`).
4. **Health keys are Loom's to supply**: `health.loom.<instance>` per Contract #5 (`status`/`heartbeatAt`/
   `metrics` — running instance count, per-domain consumer states, relay + deadline-watcher liveness — +
   `issues`). The supervisor's `HealthSink` is caller-keyed; Loom implements/uses a sink writing THAT shape
   for pause-state (a separate, smaller document from the Contract #5 heartbeat). Pause-state persists and
   restores across a Loom restart (supervisor `Add`-time restore semantics).
5. **`internal/loom` imports only `substrate/*`** (8.1 AC#8) — the supervisor was built so this holds; no
   `jetstream`/`nats.go` imports may appear in `internal/loom`.
6. **All four Loom durables move to the supervisor**: trigger, per-domain completion consumers, relay,
   deadline watcher. The per-domain set is the dynamic/reconciled one; the other three are fixed specs added
   once at `Start`.
7. **Filter/config change → `Reset`** (delete-and-recreate), never silently unchanged (ECH Path #3 from the
   8.2 review). In practice the per-domain filter (`events.<domain>.>`) is name-derived and stable — encode
   the diff generically so a future spec-shape change is caught, even if today's domains never actually
   trigger it.

### `Resume` semantics carry forward

Per 8.4's shipped godoc on `(*ConsumerSupervisor).Resume` (`internal/substrate/consumer_supervisor.go`):
"Resume only clears pause reasons that were active at the moment it was called. A pause reason added AFTER a
Resume … is NOT retroactively cleared by that earlier Resume; the new failure re-enters its own pause state
and requires its own Resume." Loom has no operator control surface in THIS story (no CLI/API to call
`Pause`/`Resume`) — this matters mainly for the pause-restore test (Task 5/AC 5): if the test calls `Resume`
then immediately re-triggers a failure, expect a FRESH pause, not an already-cleared one.

### Classify hook — Infra vs Structural for Loom (minimal default)

Loom has no target-store adapter like Refractor's `adapter.Probe`. A conservative default is acceptable:
- **Infra** (probe loop, auto-recovers): `loom-state` bucket / NATS connectivity unavailable — a transient
  KV-read/write failure on `e.state.*` operations.
- **Structural** (blocks awaiting `Resume`): the pattern-source bucket (`core-kv`, `vtx.meta.>`) becomes
  unreadable/misconfigured — a permanent misconfiguration, not a transient blip.
- **Default** (no Classify, or unrecognized error): `ClassTransient`.

If, after implementation, NO handler path realistically produces a non-nil error (today's handlers fully
encode outcomes as `Decision`), a nil `Classify` (package default = always `ClassTransient`) is an ACCEPTABLE
deviation — note it in Completion Notes rather than inventing a speculative error path purely to exercise
`Classify`.

### Grounding map (read these before writing code)

- `internal/substrate/consumer_supervisor.go` — `ConsumerSupervisor`, `NewConsumerSupervisor`,
  `Add`/`Remove`/`Reset`/`Stop`/`UpdateSpec`/`Pause`/`Resume`/`PendingForConsumer`. `Remove` deletes the
  durable; `Stop` does not (substrate doctrine: "a durable's persisted position is the point of its
  durability" — callers wanting delete-on-shutdown call `Remove` per consumer, which Loom does NOT do for the
  three fixed durables on shutdown).
- `internal/substrate/consumer_supervisor_spec.go` — `ConsumerSpec`, `DeliverPolicy` (`DeliverAll` /
  `DeliverLastPerSubject`), `FailureClass`, `PauseReason`, `HealthStatus`, `HealthSink`, `SupervisedHandler`,
  `ClassifyFunc`, `ProbeFunc`, `DefaultProbeInterval`.
- `internal/substrate/consumer_supervisor_pump.go` — pump loop semantics (pause/probe/restore); composable
  pause-reason set; `dominantReason()` precedence manual > structural > infra for HealthSink persistence.
- `internal/substrate/consumer.go` — `NakWithDelay` (Decision value 3), `DefaultRedeliveryDelay` (5s),
  `Message{Subject, Body, Sequence, NumDelivered}`.
- `docs/components/substrate.md` (lines ~280–380) — "ConsumerSupervisor (supervised pump)" section: pause
  composability table, policy hooks, HealthSink persist+restore semantics, NakWithDelay.
- **The Refractor adoption pattern (worked example for 8.4)**:
  - `internal/refractor/pipeline/supervisor_adapt.go` — `healthSink` adapter (`SetActive`/`SetPaused`/
    `Load`), `classifyForSupervisor` (Refractor's `failure.Category` → `substrate.FailureClass`). Loom's
    sink/classify will be structurally similar but MUST NOT import `internal/refractor/*`.
  - `internal/refractor/pipeline/pipeline.go` — `RunOn`/`Run` wiring onto the supervisor.
  - `cmd/refractor/main.go` — top-level wiring: `health.NewLatticeHeartbeater(conn, healthKVBucket, instance,
    defaultHeartbeatEvery, logger)` + `go hb.Run(ctx)` (lines ~38, ~76–77) — Loom's Contract #5 heartbeater
    follows the SAME shape, `component: "loom"`.
  - `internal/refractor/health/lattice_heartbeater.go` — `LatticeHealthDoc` (Contract #5 §5.2 shape),
    `NewLatticeHeartbeater`, `Run`, `emit` — Loom's heartbeater mirrors this file's structure (cannot import
    it; re-implement the shape in `internal/loom`).
  - `internal/refractor/health/reporter.go` — `Entry` schema is Refractor's OWN and stays byte-identical
    (8.4 ruling); Loom does NOT reuse this type — Loom defines its own minimal pause-state document.
- `internal/loom/engine.go` — `runTriggerConsumer` (~187, fixed durable `loom-trigger` on
  `events.loom.patternStarted`), `reconcileConsumers` (~272, additive-only today; `domainConsumers
  map[string]context.CancelFunc`), `runDomainConsumer` (~296, durable `loom-<domain>`),
  `runDeadlineWatcher` (~575, KV-CDC durable on the loom-state bucket), `Start` (~161) wiring.
- `internal/loom/actuator.go` — the relay (`newRelay` ~52, `relay.run` ~62, `relay.handle` ~77).
- `internal/loom/pattern.go` — `Pattern.Domains()` (~50), `bindingRegistry` (in `source.go` ~325).
- `internal/loom/source.go` — `bindingRegistry` (~325, maps the set of referenced event domains across all
  registered patterns); `removePattern` (~221, calls `updateCB(nil, nil)` on a pattern's removal — the
  existing reconcile-trigger for teardown); `setLoadCallback`/`setUpdateCallback` (~78–79); `snapshot()`
  (~294).
- `docs/contracts/05-health-kv.md` (FROZEN) — the `health.<component>.<instance>` entry shape; §5.2 document
  shape, §5.3 status enum, §5.4 metrics baseline ("Loom / Weaver / Gateway: TBD in Phase 2; conventions will
  follow this pattern" — Loom is the FIRST to define its convention here), §5.6 heartbeat cadence/TTL (10s /
  100s).
- `internal/bootstrap/primordial.go` — `HealthKVBucket = "health-kv"` (~line 24).
- Contract #10 §10.6/§10.9 (`docs/contracts/10-orchestration-surfaces.md`, FROZEN) — correlation/trigger
  semantics; `cmd/loom/CONTRACT-AMENDMENT-REQUEST.md` documents F4 (trigger Nak-storm) and F6 (per-domain
  consumer teardown) as the two findings THIS STORY closes on the Loom side.

### Out of scope — do NOT pull in

- No edits to `internal/substrate` or `internal/refractor` (this story is the mirror image of 8.4, which left
  `internal/loom` untouched — if a genuine substrate gap blocks a task, STOP and write it up in Questions for
  Winston, do not patch substrate).
- No edits to `docs/contracts/*` or `_bmad-output/planning-artifacts/*`.
- No operator control-plane (CLI/API) for `Pause`/`Resume`/`list` — that is a future Loom control-plane story
  (per `cmd/loom/CONTRACT-AMENDMENT-REQUEST.md` §10.9 "Queryability … is its own (future) control-plane
  story"). This story only needs the supervisor's `Pause`/`Resume` to be CALLABLE (for the pause-restore
  test), not exposed externally.
- No exponential backoff (fixed configurable floor, `DefaultRedeliveryDelay` = 5s — adjudicated in 8.4). No
  `MaxDeliver` anywhere on Loom-created consumers.
- Solving the in-flight-instance-orphaned-on-teardown caveat (Task 2) — document only.

### House rules (binding, from CLAUDE.md)

- **NO history/changelog comments in code** — no `// Story 8.5`, `// Previously`, `// migrated from
  runDomainConsumer`, `// replaces the additive map`. Comments describe what the code does NOW. (Most-violated
  rule; this story rewires four existing consumers — resist narrating the rewire in comments.)
- `docs/contracts/*` are FROZEN; gaps via `cmd/loom/CONTRACT-AMENDMENT-REQUEST.md` (already exists — append a
  new section if a genuine gap surfaces, do not edit existing ratified sections).
- New documentation goes in `/docs` (this story updates `docs/components/loom.md`), not `_bmad-output/`.
- Sub-agents never commit, push, or branch — leave changes in the working tree; the lead (Winston) adjudicates
  and commits.
- Verification gates before declaring done: `go build ./...`, `make vet`, `golangci-lint run ./...`, `make
  verify-kernel`, `make test-bypass` (Gate 2, all BLOCKED), `make test-capability-adversarial` (Gate 3, all
  DEFENDED), plus `go test ./internal/loom/... ./internal/substrate/... ./internal/refractor/...` (substrate +
  refractor green UNTOUCHED; loom is the regression net).

### Project Structure Notes

- Modified: `internal/loom/engine.go` (supervisor field, spec builders, `reconcileConsumers` diff, `Start`/
  shutdown wiring, Classify/Probe hooks), `internal/loom/actuator.go` (`relay.handle` → `NakWithDelay` +
  `SupervisedHandler` signature).
- New: `internal/loom/health.go` (or similarly named — Contract #5 heartbeater, Loom-local, mirroring
  `internal/refractor/health/lattice_heartbeater.go`'s shape without importing it) and a HealthSink
  implementation file (may be the same file or split, e.g. `internal/loom/health_sink.go`) for per-consumer
  pause-state persist/restore.
- New tests: extend `internal/loom/loom_e2e_test.go` and/or new `internal/loom/*_test.go` files following its
  embedded-NATS harness (`startNATS`, `provision`, `newEngine` helpers already exist there).
- Modified: `docs/components/loom.md` (supervisor adoption + health surface).
- No new sub-packages; `internal/loom` stays flat per its existing one-concern-per-file style
  (`engine.go`, `actuator.go`, `pattern.go`, `source.go`, `state.go`).

### Previous story intelligence (8.4, done)

- 8.4 shipped `internal/substrate/consumer_supervisor.go` (+ `_spec.go`, `_pump.go`), migrated Refractor onto
  it via `internal/refractor/pipeline/supervisor_adapt.go` (the `healthSink`/`classifyForSupervisor` adapter
  pattern — Loom's equivalents live in `internal/loom`, not a shared package, since substrate must not import
  either component's adapters and the components must not import each other).
- 8.4's code review found the Refractor adapter seam (not the supervisor core) was where Majors clustered —
  expect the same here: the supervisor itself is solid (3-layer-reviewed, 0 Critical findings); Loom's
  adapter code (handler wrapping, HealthSink, Classify) is where care is needed.
- 8.4 left `internal/loom` and `internal/processor` green UNTOUCHED — confirm `go test ./internal/loom/...`
  passes on the CURRENT `main` (commit d125b79) before starting, as your baseline.
- `internal/substrate/consumer_test.go` and `internal/substrate/consumer_supervisor_test.go` show the
  established embedded-NATS test harness pattern — `internal/loom/loom_e2e_test.go` already follows an
  analogous pattern (`startNATS`, `provision`, `newEngine`); extend it rather than inventing a new harness.

### References

- [Source: _bmad-output/planning-artifacts/epics/phase-2-epics.md#Story 8.5]
- [Source: _bmad-output/implementation-artifacts/8-4-substrate-consumer-supervisor.md] (shipped 8.4 — API +
  rulings this story builds on)
- [Source: docs/contracts/05-health-kv.md (Contract #5)]
- [Source: docs/components/substrate.md#ConsumerSupervisor (supervised pump)]
- [Source: docs/components/loom.md]
- [Source: cmd/loom/CONTRACT-AMENDMENT-REQUEST.md] (F4, F6 findings this story closes)
- [Source: internal/substrate/consumer_supervisor.go; consumer_supervisor_spec.go; consumer_supervisor_pump.go;
  consumer.go]
- [Source: internal/loom/engine.go; actuator.go; pattern.go; source.go]
- [Source: internal/refractor/pipeline/supervisor_adapt.go; internal/refractor/health/lattice_heartbeater.go;
  cmd/refractor/main.go]
- [Source: internal/bootstrap/primordial.go (HealthKVBucket = "health-kv")]

## Dev Agent Record

### Code Review Triage (Winston)

Three-layer review complete: Blind Hunter (`8-5-cr-blind-hunter.md`, 0C/1M/2m/1n), Edge Case Hunter (`8-5-cr-edge-case-hunter.md`, 2 MEDIUM/4 LOW), Acceptance Auditor (`8-5-cr-acceptance-auditor.md`, 8 SATISFIED/1 PARTIAL/0 VIOLATED, all rulings honored).

**Fix-forward (this story):** BH-M1 `Engine.Start` early-return error paths call `supervisor.Stop()` (pumps run on a Background-derived ctx — partial startup must not leak goroutines/consumers); ECH-F1 `Remove` evicts the consumer-state cache entry (removed domain otherwise reported `running` in heartbeats forever); ECH-F2 `Remove` deletes the persisted per-consumer pause entry (stale entry otherwise restores a re-added domain into an old pause); ECH-F3/F4 empty `Config.Instance` gets a deterministic per-process default + uniqueness precondition documented; BH-n instance counter logs swallowed `KVGet` errors; ECH-F6 docs caveat re-attributed (pattern-definition removal orphans in-flight instances, not consumer teardown); AA-PARTIAL pure-Go `fingerprintOf` inequality unit test for the reconcile diff comparator.

**Deferred with rationale:** ECH-F5 (restore fails open on health-kv read error) is the 8.4-adjudicated `restoreHealthState` semantic the supervisor generalizes — by design, not a Loom defect. BH-m `formatISODuration` duplication with `internal/processor/health.go` — dedup would touch another package; noted for a cleanup pass. BH-m first-heartbeat-incomplete `metrics.consumers` map — documented async-seed behavior, fills within one cycle.

### Agent Model Used

claude-opus-4-8 (Amelia, bmad-dev-story)

### Debug Log References

- `go build ./...` — clean.
- `make vet` — clean.
- `golangci-lint run ./...` — 0 issues.
- `make verify-kernel` — ALL ASSERTIONS PASSED.
- `make test-bypass` — Gate 2 all BLOCKED; Gate 3 report PASSED (4/4).
- `make test-capability-adversarial` — all DEFENDED / ACCEPTED-WINDOW baseline; Gate 3 PASSED.
- `go test ./internal/loom/... ./internal/substrate/... ./internal/refractor/...` — all green;
  substrate + refractor UNTOUCHED.

### Completion Notes List

- **Q1/Ruling 1 — DeliverAll, explicit, all four specs.** Each `ConsumerSpec` sets
  `DeliverPolicy: substrate.DeliverAll` explicitly (matching `RunDurableConsumer`'s hard-coded
  `DeliverAllPolicy`). No `DeliverLastPerSubject` — that is Refractor's ADR-15 semantic, not Loom's.
- **Q2/Ruling 2 — Loom-side state cache.** The supervisor persists through the caller's `HealthSink`
  but exposes no read-back. Loom's `consumerStateCache` is fed by every per-consumer sink transition
  (`SetActive`/`SetPaused`/`Load`); the heartbeater reads it for `metrics.consumers`. Zero substrate
  edits. Note: the cache is seeded asynchronously by the pump's Add-time `Load`, so the very first
  ("starting") heartbeat may carry an incomplete `consumers` map; it fills in within one 10s cycle.
  This is acceptable for observability and is what the heartbeat test waits for.
- **Q3/Ruling 3 — `Config.HealthKVBucket`, literal `"health-kv"` default.** Added to `withDefaults()`
  alongside the other bucket literals; `cmd/loom/main.go` overrides from `bootstrap.HealthKVBucket`.
  `internal/loom` does NOT import `internal/bootstrap`.
- **Q4/Ruling 4 (OVERRIDE) — relay KV-delete-failure ALSO returns `NakWithDelay`.** Both relay
  failure-return paths (publish and the subsequent `KVDelete`) now return `NakWithDelay`. One
  consistent backoff posture; the relay never hot-loops against a failing ops stream or a failing
  `loom-state` KV. `Term` paths (unparseable record, marshal failure) unchanged.
- **Classify/Probe (Task 6) — nil, accepted deviation.** Every Loom handler fully encodes its outcome
  as a bare `Decision`; the adapter wrappers return `(decision, nil)`, so no live handler path
  produces a non-nil error. Per the Dev Notes "ACCEPTABLE deviation" clause, no `Classify`/`Probe`
  hooks are wired (the package default treats any error as `ClassTransient`). No speculative error
  path was invented purely to exercise `Classify`. Consequence: an infra pause would behave like a
  structural one, but with no error path there is no pause source today.
- **`cmd/loom/main.go` now passes `Instance: instance`** so the heartbeat key (`health.loom.<instance>`)
  matches the logged/connection instance id, rather than the engine auto-generating a separate id for
  the pattern-source durable suffix.
- **Shutdown wiring.** `Start` calls `supervisor.Stop()` on `<-ctx.Done()` — pumps stop, the three
  fixed durables keep their ack floor (substrate doctrine). Per-domain consumers are removed only by a
  live reconcile diff, never force-removed on shutdown.
- **Reset diff fingerprint.** The reconcile tracks a `specFingerprint` (stream/filter/deliverPolicy/
  deliverGroup) per applied domain spec and only calls `UpdateSpec`+`Reset` when it diverges — so a
  steady-state reconcile (same name-derived filter) is a no-op and never churns the ack floor. The
  Reset branch is exercised by a test-only `ResetDomainForTest` seam since production filters are
  name-stable.
- **Test seams.** `export_test.go` (package `loom`) exposes `PauseForTest` and `ResetDomainForTest`,
  delegating to the supervisor's `Pause`/`UpdateSpec`/`Reset`. Loom exposes no production operator
  control surface (deferred control-plane story); these are test-only.
- Known limitation (in-flight-instance-orphaned-on-teardown) documented in `docs/components/loom.md`,
  not mitigated (per Task 2 / Adjudicated decision 2).

### File List

- Modified: `internal/loom/engine.go` (supervisor field + state cache, Config.HealthKVBucket, four
  spec builders, supervisedHandler adapter, healthSinkFor, reconcileConsumers desired-vs-running diff
  with specFingerprint, Start/Stop wiring + heartbeater launch; removed runTriggerConsumer /
  runDomainConsumer / runDeadlineWatcher).
- Modified: `internal/loom/actuator.go` (`relay.handle` → `SupervisedHandler` signature; publish AND
  KVDelete failures → `NakWithDelay`; removed the now-unused `relay.run`).
- Modified: `cmd/loom/main.go` (pass `HealthKVBucket` + `Instance`).
- New: `internal/loom/health.go` (Contract #5 §5.2 heartbeater, consumerStateCache,
  runningInstanceCounter, ISO-8601 duration).
- New: `internal/loom/health_sink.go` (per-consumer `substrate.HealthSink` →
  `health.loom.<instance>.consumer.<name>`, feeds the state cache).
- Modified: `docs/components/loom.md` (supervisor adoption section: reconcile diff, F6 teardown, relay
  NakWithDelay, Contract #5 heartbeat + per-consumer sink, restore-across-restart, known limitation).
- New tests: `internal/loom/supervisor_test.go` (external: removed-pattern teardown+durable-delete,
  filter-change Reset, well-formed heartbeat, pause-state survives restart),
  `internal/loom/relay_internal_test.go` (internal: NakWithDelay no-hot-loop),
  `internal/loom/export_test.go` (test-only Pause/Reset seams).

## Questions for Winston

1. **KV-CDC `DeliverPolicy` for the relay/deadline-watcher specs (Task 1).** Today's
   `RunDurableConsumer(...DurableConsumerConfig{...})` calls for the relay (`actuator.go` `relay.run`) and
   deadline watcher (`engine.go` `runDeadlineWatcher`) do not explicitly set a deliver policy, so they get
   whatever `RunDurableConsumer`'s default is. The supervisor's `ConsumerSpec.DeliverPolicy` defaults to
   `DeliverAll` (zero value). For a KV-CDC stream (`KV_<bucket>`, history=1 per ADR-15), is `DeliverAll`
   equivalent in practice to `DeliverLastPerSubject` (since there's only ever one message per subject), or
   should these two specs explicitly set `DeliverLastPerSubject` for clarity/consistency with Refractor's
   Core-KV consumer? Recommend dev verify `RunDurableConsumer`'s current effective default against embedded
   NATS and match it, documenting the choice either way — low-risk either way given history=1, but flagging
   per the story's "never silently unchanged" spirit.

2. **Per-consumer health-state introspection for the Contract #5 `metrics.consumers` map (Task 4).** Does
   `substrate.ConsumerSupervisor` (post-8.4) expose any way to read a managed consumer's CURRENT pause state
   (e.g. an exported `Status(name) (HealthStatus, PauseReason)` or similar), or does the supervisor only
   PERSIST state via the caller's `HealthSink` (write-only from the supervisor's perspective)? If write-only,
   Loom's heartbeater must maintain its own in-memory cache of last-known consumer states (updated via the
   SAME `HealthSink.SetActive`/`SetPaused` calls Loom's sink already receives) to populate
   `metrics.consumers` without re-querying the supervisor. This is implementable either way but affects
   whether Task 4 needs a small substrate read-only accessor (which would be a substrate change — out of
   scope per this story's "no substrate edits" constraint) versus a purely Loom-side cache. Recommend: default
   to the Loom-side cache (no substrate change); only raise a CONTRACT-AMENDMENT-style note if the cache
   approach proves genuinely insufficient.

3. **`internal/loom` importing `internal/bootstrap` for `HealthKVBucket` (Task 4).** `cmd/loom/main.go`
   already imports `internal/bootstrap`; `internal/loom/engine.go` currently does not. Is it acceptable for
   `internal/loom` (the package, not just `cmd/loom`) to import `internal/bootstrap` for the
   `HealthKVBucket = "health-kv"` constant, or should `Config` gain a `HealthKVBucket` field (defaulted in
   `withDefaults()` to the literal `"health-kv"`, with `cmd/loom/main.go` free to override from
   `bootstrap.HealthKVBucket`) to avoid the cross-package dependency? Recommend the latter (`Config` field
   with a literal default + comment) — keeps `internal/loom`'s import graph minimal and consistent with how
   `CoreKVBucket`/`LoomStateBucket`/`EventsStream` are already handled (literal defaults in `withDefaults()`,
   not sourced from `internal/bootstrap`).

4. **Relay's KV-delete-failure path (Task 3).** The relay's `handle` has TWO failure-return points: publish
   failure (this story changes to `NakWithDelay`, per AC 3) and the subsequent `KVDelete` failure (currently
   plain `Nak`). Should the delete-failure path ALSO become `NakWithDelay`? Recommend leaving it as `Nak`
   (a delete failure after successful publish is a narrower, likely-transient KV blip; redelivery re-publishes
   idempotently and retries the delete with no backoff concern since the op already landed) — but flagging
   for an explicit ruling since "never silently unchanged" extends to backoff-path completeness too.

## Winston's rulings (adjudicated — binding; dev builds to these)

1. **Q1 → `DeliverAll`, explicitly, for all four specs.** Verified against code: `RunDurableConsumer` hard-codes `jetstream.DeliverAllPolicy` (`internal/substrate/consumer.go:122`), so every Loom durable runs DeliverAll today. An adoption story changes the *machinery*, never the *delivery semantics* — set `DeliverAll` explicitly on each spec (it is also the `ConsumerSpec` zero value, but explicit beats implicit here) and do NOT adopt `DeliverLastPerSubject`; that is Refractor's ADR-15 semantic, not Loom's.
2. **Q2 → Loom-side cache. CONFIRMED.** The supervisor persists through the caller's `HealthSink`; Loom's sink already sees every transition, so the heartbeater populates `metrics.consumers` from its own last-known-state cache. No substrate read-back accessor — this story makes zero substrate edits. If the cache genuinely cannot satisfy Contract #5, stop and write it up; do not edit substrate.
3. **Q3 → `Config.HealthKVBucket` with a literal `"health-kv"` default. CONFIRMED.** Matches how `CoreKVBucket`/`LoomStateBucket`/`EventsStream` are already configured (`withDefaults()` literals); `cmd/loom/main.go` may override from `bootstrap.HealthKVBucket`. `internal/loom` does not import `internal/bootstrap`.
4. **Q4 → OVERRIDE: the KV-delete-failure path ALSO returns `NakWithDelay`.** The recommendation's own scenario defeats it: with the ops stream healthy but loom-state KV failing, plain `Nak` redelivers immediately → re-publish (idempotent but not free — Processor churn) → re-fail delete, at full speed, for as long as the outage lasts. That IS the F4 hot-loop. Rule: every relay failure return gets bounded cadence, unbounded count. One consistent backoff posture; nothing in the relay hot-loops.
