# Story 8.4 — Edge Case Hunter Review

Scope: the uncommitted change set extracting `substrate.ConsumerSupervisor` and
migrating Refractor's pipeline onto it. Method: exhaustive path/boundary walk.
Only UNHANDLED cases are listed; handled paths discarded silently.

Severity counts: **Critical 0 · Major 3 · Minor 6**

---

## Major

### M1 — Concurrent Resume dropped by `drainSignal`-before-wait (lost resume)
- **Path:** `consumer_supervisor_pump.go:208` (`waitWhilePaused`) and `:308` (`runProbeLoop`), both call `drainSignal(st.resumeCh)` / `drainSignal(st.forceResumeCh)` UNCONDITIONALLY before blocking.
- **Scenario:** An operator `Resume` (`operatorResume` at `:81`) sends the buffered `resumeCh`/`forceResumeCh` token in the window *between* a state transition that re-adds a pause reason and the pump's next `drainSignal`. The defensive drain then discards the legitimate token. Concrete trigger: probe-loop structural escalation (`:337-343`) does `addReason(PauseStructural)` and returns; the pump re-enters `waitWhilePaused`, which drains the resume token the operator just sent. The pump stays paused; the operator must Resume a second time.
- **Severity:** Major (operator control-surface command silently lost; FR31).
- **Test coverage:** None. `TestSupervisor_ManualPause_SurvivesRestart` and `_BlocksProbeResume` exercise steady-state pause/resume, not a Resume racing a reason re-add.

### M2 — Probe-success persists "active" over a "rebuilding" health entry mid-rebuild
- **Path:** `consumer_supervisor_pump.go:328-335` (`runProbeLoop` probe-success → `persistActive`) interacting with `pipeline.go:347-382` (`Rebuild` sets "rebuilding", does NOT pause the pump).
- **Scenario:** A `Rebuild` writes status "rebuilding" and `Reset`s the durable without pausing. If a message during the rescan triggers an infra pause that then recovers, the supervisor's `persistActive` overwrites "rebuilding" → "active" while consumer lag is still non-zero. `watchRebuildCompletion` later also sets active. The operator observes a premature "active" during an in-flight rebuild.
- **Severity:** Major (health-plane status correctness; the supervisor now owns the persist that the pipeline previously coupled to rebuild state).
- **Test coverage:** None covering infra-pause-during-rebuild.

### M3 — Partial-batch redelivery re-enqueues already-queued retry entries
- **Path:** `pipeline.go:594-638` (`writeResults`): a multi-result batch where an early result is enqueued to the retry queue (`enqueuedCount++`, `continue`) and a later result returns `CatInfra`/`CatStructural` → `return substrate.Nak, writeErr` leaves the message PENDING.
- **Scenario:** The supervisor's pause-leaves-pending semantics make redelivery the normal path on infra/structural. On redelivery the whole batch re-evaluates and the earlier transient result is enqueued into the retry queue a SECOND time. Duplicate retry-queue entries accumulate per pause/resume cycle.
- **Severity:** Major (resource/duplicate-work growth bounded only by idempotent writes; amplified by the new supervised redelivery contract).
- **Test coverage:** None for mixed transient-enqueue + infra in one batch.

---

## Minor

### m1 — Stale `reopenTrigger` token aborts the next fresh drain (one wasted iteration)
- **Path:** `consumer_supervisor.go:156` (`Reset` → `requestReopen`) vs `consumer_supervisor_pump.go:302-349` (`runProbeLoop` does NOT select on `reopenTrigger`) and `:281-300` (`handleDrainOutcome` `ClassInfra`/`ClassStructural` cases do NOT drain `reopenTrigger`).
- **Scenario:** A `Reset` during an infra pause (probe loop) leaves a buffered `reopenTrigger` token. When the pump later opens a fresh iterator and calls `drain`, the watcher (`:233`) consumes the stale token and immediately `mc.Stop()`s the new iterator, costing one wasted reopen cycle.
- **Severity:** Minor (self-heals next iteration). **No test.**

### m2 — Stale `pauseTrigger` token on the infra/structural drain-exit path
- **Path:** `consumer_supervisor_pump.go:281-300` (`handleDrainOutcome`): only the `default` case drains `pauseTrigger`/`reopenTrigger`; the `ClassInfra`/`ClassStructural` cases do not.
- **Scenario:** A manual `Pause` (`addReason` → `pauseTrigger`) arriving in the same drain that returns infra/structural leaves a buffered `pauseTrigger`, which prematurely stops the first iterator after resume.
- **Severity:** Minor (one wasted iteration). **No test.**

### m3 — `PendingForConsumer` returns not-managed error after `Remove`/`Stop`, swallowed by rebuild watcher as transient
- **Path:** `consumer_supervisor.go:237-243` returns `"not managed"` for an unknown name; `pipeline.go:395-401` treats every `PendingForConsumer` error as "retry" and loops.
- **Scenario:** If a rule is `Remove`d (or the supervisor `Stop`ped) while `watchRebuildCompletion` is polling, the not-managed error is indistinguishable from a transient consumer-info read failure; the watcher spins on the ticker until ctx cancels instead of exiting. No `pending==0` will ever arrive, so health never returns to "active".
- **Severity:** Minor (bounded by ctx lifetime; status stuck at "rebuilding"). **No test.**

### m4 — Durable deleted server-side out from under a running pump never re-creates
- **Path:** `consumer_supervisor_pump.go:165-179`: a not-found on `js.Consumer` only logs + backs off + retries `Consumer()`; it never calls `createConsumer`.
- **Scenario:** If an external actor deletes the durable (not via `Remove`/`Reset`), the pump loops forever on `Consumer()` not-found with no recreate path. Only `Reset` recreates. The supervisor self-heals a Reset's brief delete window but not an external/permanent deletion.
- **Severity:** Minor (out-of-band deletion is not a normal path). **No test.**

### m5 — `CreateOrUpdateConsumer` config-conflict on `Add` of a pre-existing durable with different config
- **Path:** `consumer_supervisor.go:257-276` (`createConsumer`) uses `CreateOrUpdateConsumer`; `Reset` (`:137-158`) deletes-then-creates, but `Add` does not.
- **Scenario:** `Add` for a `Name` whose server-side durable already exists with an incompatible immutable config (e.g. a different `DeliverPolicy` or `FilterSubject` from a prior run) surfaces the JetStream update-conflict error verbatim with no delete-recreate fallback, so the consumer never starts. Refractor relies on `Reset` for config changes, but a first-boot `Add` after a config change in the spec hits this.
- **Severity:** Minor (operators use Reset for config change). **No test.**

### m6 — Probe-loop `time.After(interval)` not cancelled on ctx done until the tick fires
- **Path:** `consumer_supervisor_pump.go:311-323`: the `select` does include `ctx.Done()`, so shutdown is prompt — BUT a fresh `time.After(interval)` timer is allocated on every loop iteration and never stopped, so during a long-running infra pause each non-recovering probe leaks one timer until it fires.
- **Scenario:** With the default 10s interval and a long outage, timers are short-lived enough to be harmless; only flagged as a boundary (unbounded probe duration × per-iteration timer alloc).
- **Severity:** Minor (GC reclaims fired timers; no leak across shutdown since `ctx.Done` exits). **No test.**

---

## Paths walked and confirmed HANDLED (not reported)

- Manual pause not cleared by passing probe (`clearReason(PauseInfra)` only) — handled, tested.
- Composite {Infra,Manual} Resume → infra-only probe loop re-entry — handled (`operatorResume` deletes Manual, leaves Infra; `runProbeLoop` drains its own `forceResumeCh`).
- `Add` after `Stop`, `Add` of existing name (double-checked under lock), `Remove`/`Reset` of absent name — handled.
- `Stop` racing `Add` (mc inserted under lock before Stop's snapshot; `done` always closed by the scheduled goroutine) — no leak.
- `drain` watcher goroutine on every exit path (`defer stopDone()` cancels `stopCtx`) — no leak.
- NakWithDelay zero-value floor → `DefaultRedeliveryDelay` (`consumer.go:230-237`); never-set-MaxDeliver invariant — handled, tested (`TestSupervisor_NoMaxDeliver_UnboundedRedelivery`).
- HealthSink malformed/missing entry → `StatusActive`; interrupted "rebuilding" → active (`supervisor_adapt.go:39-47`) — handled.
- `restoreState` unrecognised reason → assume active; Load error → assume active — handled.
- ctx cancel during probe sleep / pause block / reopen backoff — all selects include `ctx.Done()`.
