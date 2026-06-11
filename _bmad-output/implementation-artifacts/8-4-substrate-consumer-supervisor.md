# Story 8.4: Substrate ConsumerSupervisor — extract Refractor's supervised pump, migrate Refractor (F4 hardening)

Status: done

> Replaces the withdrawn draft `8-4-substrate-consumer-lifecycle-manager.md` after an architecture
> review. The "thin-adapter over the bind" plan is REJECTED — do not resurrect it. The reusable
> asset is Refractor's **supervised pump** (the state machine deciding when/whether to pull, with
> Health-KV-persisted pause state), not the bind. Loom adoption moved to Story 8.5.

## Story

As a platform developer,
I want Refractor's supervised consumer runtime — the pause/probe/health state machine plus the bind lifecycle — extracted into a substrate `ConsumerSupervisor` that Refractor itself then runs on,
So that Loom (8.5) and Weaver (Epic 9: per-target lane-1 + per-domain lane-2 consumers; 9.4's disable/revoke control surface = the supervisor's `Pause`/`Remove`) reuse one supervised pump instead of each hand-rolling lifecycle, backoff, and health.

## Acceptance Criteria

From `_bmad-output/planning-artifacts/epics/phase-2-epics.md` § Story 8.4 (source of truth; do not edit it).

1. **`substrate.ConsumerSupervisor` exists in `internal/substrate/`** (exported type `ConsumerSupervisor`, e.g. `consumer_supervisor.go` + siblings; NO new sub-package) owning MECHANISM only:
   a. A **registry of desired consumer specs** + desired-vs-running reconcile: `Add` / `Remove` / `Reset` (delete-and-recreate) / `Stop`. Each spec carries its full config — stream, filter subject(s), durable name, deliver policy, queue group (`DeliverGroup`), redelivery floor — **caller-supplied, never hard-coded**. The supervisor is agnostic between event-stream durables (`events.<domain>.>`) and KV-CDC durables (`$KV.<bucket>.>`).
   b. A **per-consumer state machine**: Running → PausedInfra (probe loop) / PausedStructural (await `Resume`) / PausedManual (await `Resume`). Pause reasons are **distinct and composable** — resume-from-infra (a passing probe) must never clear an operator (manual) pause.
   c. **Backoff**: a `NakWithDelay` decision **appended to the END** of the iota `Decision` enum in `internal/substrate/consumer.go` (binary-additive: `Ack=0`/`Nak=1`/`Term=2` unchanged), mapped to `msg.NakWithDelay(delay)`. Delay = a fixed configurable redelivery floor per consumer spec — **NOT exponential** (adjudicated; if a test forces exponential, justify in Questions for Winston). Retry *cadence* bounded, retry *count* never — **the supervisor never sets `MaxDeliver`**. Every existing caller returning `Ack`/`Nak`/`Term` compiles and behaves identically (opt-in).
   d. A **HealthSink**: state transitions persisted to Health KV (Contract #5, `docs/contracts/05-health-kv.md` — FROZEN, build to it) under a **caller-supplied key** (Refractor's existing key today; `health.loom.<instance>` / `health.weaver.<target>` later), **restored at startup** — generalizing `restoreHealthState` including its exact semantics: infra pause → re-enter probe loop; structural/manual → block awaiting `Resume`; malformed or missing entry → treat as active; interrupted "rebuilding" → treat as active.
2. **Policy stays with the caller via hooks**: `Classify(err) → Transient|Terminal|Infra|Structural`, `Probe(ctx) error`, and the message handler. Refractor supplies `failure.Classify` and `adapter.Probe`.
3. **No `jetstream` (or `nats.go`) type escapes the supervisor's exported surface** — future callers (Loom, Weaver) import only `substrate/*`.
4. **Refractor is migrated onto the supervisor** (the proof): `pipeline.Run`'s supervision skeleton is hosted by the supervisor; Refractor KEEPS its processing policy — `drain`/`processMsg`, retry queue, DLQ, audit writer, lag poller, rebuild orchestration (`Rebuild`'s health-status + truncate + lag-watch choreography stays Refractor's; the consumer delete-recreate-swap part maps to the supervisor's `Reset`). `consumer.Manager`'s bind folds into supervisor specs.
5. **Regression net**: the pipeline + consumer-manager test suites — **every behavioral assertion preserved**. Mechanical rewires to new signatures are permitted; behavior changes are not. Queue-group fan-out (NFR12, `DeliverGroup`) and `DeliverLastPerSubjectPolicy` (ADR-15) preserved exactly.
6. **New tests assert**: (a) a handler returning `NakWithDelay` does not hot-loop at zero delay; (b) retry count remains unbounded (no `MaxDeliver` on supervisor-created durables); (c) a manual pause survives restart via Health KV; (d) an infra pause enters the probe loop and recovers on a passing probe; (e) `Reset` recreates a durable whose filter changed.

## Tasks / Subtasks

- [ ] **Task 1: `NakWithDelay` decision (AC 1c)** — `internal/substrate/consumer.go`
  - [ ] Append `NakWithDelay` to the END of the `Decision` iota block (value 3; `Ack=0`/`Nak=1`/`Term=2` untouched — binary-additive).
  - [ ] Map it in `applyDecision` to `msg.NakWithDelay(delay)` (exists in nats.go — repo pins v1.52.0). The delay is NOT carried on the Decision (it's a plain int): it comes from the consumer's spec/config — add an additive way for the decision-applier to know the per-consumer redelivery floor (e.g. an optional `RedeliveryDelay time.Duration` on `DurableConsumerConfig` / the supervisor spec; zero value falls back to plain `Nak` semantics or a package default — pick one and document it in the godoc).
  - [ ] Confirm by grep that every existing `Decision` caller (`internal/processor/outbox/consumer.go`, `internal/loom/engine.go` ×3, `internal/loom/actuator.go`) compiles unchanged and behaves identically. Do NOT change any of their return values.
  - [ ] Unit tests in `internal/substrate`: NakWithDelay applies a non-zero delay (no hot-loop — assert redelivery does not arrive before the floor elapses, with a generous tolerance for CI); enum values asserted (`Ack==0, Nak==1, Term==2, NakWithDelay==3`).
- [ ] **Task 2: `ConsumerSupervisor` registry + reconcile (AC 1a, 3)** — new `internal/substrate/consumer_supervisor.go` (+ siblings as needed)
  - [ ] Exported type `ConsumerSupervisor`, constructed from `*Conn` (the package-internal `c.js` handle is available; nothing jetstream-typed in any exported signature, parameter, return, or exported field).
  - [ ] Spec type carrying FULL config: stream, filter subject(s), durable name, deliver policy (at minimum `DeliverAll` and `DeliverLastPerSubject` — model as a substrate-owned enum, not `jetstream.DeliverPolicy`), queue group (`DeliverGroup`), redelivery floor, handler + hooks (Task 4), health key + sink (Task 5), logger.
  - [ ] `Add(spec)` — registers + creates (idempotent, `CreateOrUpdateConsumer`) + starts the supervised pump goroutine. `Remove(name)` — stops the pump AND deletes the server-side durable (generalizes `consumer.Manager.Remove`, manager.go:87). `Reset(name)` — delete-and-recreate preserving the spec's deliver policy, pump swaps to the new durable (generalizes `Manager.Reset`, manager.go:116, including its unconditional-delete + `ErrConsumerNotFound`-tolerant TOCTOU hardening). `Stop()` — stops all pumps (decide and document whether `Stop` deletes durables; Refractor's `Manager.Stop` deletes — preserve whatever the pipeline/manager tests assert).
  - [ ] The supervisor must NOT set `MaxDeliver` on any consumer it creates (AC 6b).
  - [ ] Layer 1 (`RunDurableConsumer`) stays dumb — do not graft supervision into it. The supervisor owns its own pump loop (it generalizes `pipeline.Run`'s `Messages()`/drain cycle, which needs consumer-handle swap on `Reset` and pause interleaving that the one-shot `RunDurableConsumer` cannot host). Reuse internal helpers where natural.
- [ ] **Task 3: per-consumer state machine (AC 1b)** — generalizes `pipeline.Run` (pipeline.go ~294–421), `runInfraProbeLoop` (~1075), and the manual-pause machinery (`Pause` ~991 / `Resume` ~1008, `initResumeCh`/`clearResumeCh`, `manualPauseTrigger`, `forceResumeCh`)
  - [ ] States: Running, PausedInfra, PausedStructural, PausedManual. Track active pause reasons as a composable SET, not a single value: probe-success clears ONLY the infra reason; operator `Resume(name)` clears manual + structural and force-exits an in-flight probe loop (preserving Refractor's `forceResumeCh` override behavior); the pump runs only when the set is empty.
  - [ ] `Pause(name)` (manual) interrupts a running drain promptly (generalize `manualPauseTrigger` + the stale-token drain at pipeline.go ~322–347); idempotent.
  - [ ] Infra probe loop: on an Infra-classified failure, pause + poll the caller's `Probe(ctx)` hook at a configurable interval (generalize `ProbeInterval`, exported-for-tests pattern at pipeline.go:28); a probe error classified Structural escalates to PausedStructural (pipeline.go ~1103); ctx-cancel exits cleanly.
  - [ ] Structural/manual pauses block awaiting `Resume` or ctx-done, exactly as the pipeline's `resumeCh` select does.
- [ ] **Task 4: policy hooks (AC 2)**
  - [ ] Hook surface per spec: `Classify(err) → Transient|Terminal|Infra|Structural` (a substrate-owned 4-tier enum mirroring `failure.Category` — substrate must NOT import `internal/refractor/failure`; Refractor adapts), `Probe(ctx) error`, and the message handler.
  - [ ] Handler boundary: the substrate `Message{Subject, Body, Sequence}` view is too thin for Refractor's `processMsg` (needs jetstream.Msg metadata — at minimum delivery count/timestamp — and Refractor's retry queue acks/naks the original message ASYNCHRONOUSLY after the handler returns). Design an **additive enrichment** of the handler boundary that supports both, WITHOUT bolting jetstream types onto the exported surface. Candidate shape (dev's latitude, constraints binding): enrich `Message` with metadata fields (e.g. `NumDelivered`), and let the supervisor handler return `(Decision, error)` — non-nil error routes through `Classify` (Infra/Structural → pause with the message left UN-acked/UN-naked for redelivery on resume, exactly as `processMsg`'s "do NOT ack/nak" contract, pipeline.go ~643–644 + ~754–757); plus a deferred-disposition mechanism (e.g. an opaque substrate-owned handle with `Complete(Decision)`) for the retry-queue case. If a genuine impossibility appears (the supervisor cannot host a pipeline behavior without breaking a test), STOP that subtask and write it up in Questions for Winston with the failing test named — do not force it.
- [ ] **Task 5: HealthSink — persist + restore (AC 1d)**
  - [ ] Substrate-owned small interface (e.g. `SetActive(ctx)`, `SetPaused(ctx, reason, lastErr)`, `Load(ctx) → (status, reason)`), keyed by the CALLER — the supervisor never invents health keys. Nil sink → skip all health I/O (mirrors `reporter != nil` guards).
  - [ ] Persist every supervisor state transition through the sink (generalize `setHealthPaused`/`setHealthActive`, pipeline.go ~1115–1136 — sink errors logged, never fatal).
  - [ ] Startup restore generalizing `restoreHealthState` (pipeline.go ~518–600) with its EXACT semantics: status≠"paused" (incl. interrupted "rebuilding", unknown) → active; paused+infra → enter probe loop; paused+structural / paused+manual → block awaiting `Resume`; malformed entry (nil reason) or read error → log + treat as active.
  - [ ] Composability vs Refractor's single-`PauseReason` Entry schema (`internal/refractor/health/reporter.go` Entry): when multiple reasons are active, persist the highest operator-relevance reason — precedence manual > structural > infra. (Today's pipeline never persists two at once; this only defines the new composable machine's tie-break. Flagged in Questions.)
- [ ] **Task 6: supervisor unit tests (AC 6)** — `internal/substrate/consumer_supervisor_test.go` (follow the embedded-NATS test patterns in `internal/substrate/consumer_test.go`)
  - [ ] (a) NakWithDelay no zero-delay hot-loop (may live in Task 1's tests); (b) supervisor-created durables have no `MaxDeliver` and a repeatedly-Nak'd message keeps redelivering past any small bound; (c) manual pause → sink persists → new supervisor instance with same sink restores into PausedManual and does not pump until `Resume`; (d) injected Infra classification → probe loop entered → probe flips to passing → pump resumes + sink shows active; composability: while PausedManual, a probe success does NOT resume the pump; (e) `Add` with filter A, then `Reset` after spec filter changes to B → durable recreated with filter B (assert via consumer info through a test-side jetstream handle — tests may use jetstream directly; only the exported surface is constrained).
- [ ] **Task 7: migrate Refractor onto the supervisor (AC 4, 5)** — `internal/refractor/pipeline/pipeline.go`, `internal/refractor/consumer/manager.go`, `cmd/refractor/main.go`
  - [ ] Host `pipeline.Run`'s supervision skeleton (restore → pump → classify → pause/probe/resume → reconnect) on the supervisor. Refractor KEEPS: `drain`/`processMsg` and all evaluate/write policy, retry queue + DLQ (`publishTerminalDLQ`), audit writer (`writeAudit`), lag poller, adjacency watch (`runAdjWatch`), hot-reload (`HotReloadInto`/`HotReloadPlan`), and `Rebuild`'s choreography (`SetRebuilding` → optional truncate → consumer reset → lag-poller retarget → `watchRebuildCompletion` lag-watch → `SetActive`) — only the delete-recreate-swap of the durable (today `consumerResetter.Reset` + `pendingCons` pickup, pipeline.go ~349–357/~458–477) maps to the supervisor's `Reset`.
  - [ ] Refractor supplies hooks: `failure.Classify` (adapted to the substrate enum), `adapter.Probe` (via `currentAdapter()` so hot-reload still retargets probes), and its handler. `Pause`/`Resume` (control plane, FR30/FR31) delegate to the supervisor; preserve `Resume`'s probe-loop override and its `context.WithoutCancel` health write.
  - [ ] HealthSink adapter over `health.Reporter` (`SetActive`/`SetPaused`/`GetStatus`) — Entry schema, KV bucket, and key (the bare ruleID — see `Reporter.put`, reporter.go:292–301) stay BYTE-IDENTICAL; `ErrorCount`/`ConsumerLag` preservation untouched. `health.Reporter` itself does not move.
  - [ ] Fold `consumer.Manager`'s bind into supervisor specs: durable name `refractor-<ruleID>`, `DeliverGroup` = same name (NFR12 queue-group fan-out), `DeliverLastPerSubjectPolicy` (ADR-15), Core-KV stream + filter from `subjects.CoreKVStream`/`CoreKVFilter`. Preserve the `"adjacency"` name-collision caveat (manager.go:166–172). What remains of `consumer.Manager` after the fold is the dev's call (thin shim over the supervisor or deletion with call-site rewires) — judged by the regression net, not by file count.
  - [ ] Lag poller + `watchRebuildCompletion` currently hold a `jetstream.Consumer` handle (`SetConsumer`, `cons.Info`); the supervisor returns no handles. Either query consumer info via Refractor's own jetstream handle by durable name (Refractor may hold jetstream directly — the import-only-substrate rule binds Loom/Weaver, not Refractor), or expose a substrate-typed pending/lag accessor on the supervisor. Preserve the existing rebuild-completion and lag-metric behavior either way.
  - [ ] Rewire `cmd/refractor/main.go` (today: `manager.Add` → `manager.Consumer` → `go p.Run(lensCtx, cons)`, ~lines 315–341, plus the bootstrap path ~148).
- [ ] **Task 8: regression net + gates + docs (AC 5)**
  - [ ] Full behavioral preservation: `go test ./internal/refractor/...` (pipeline_test.go, consumer/manager_test.go + bootstrap_test.go, control, health, all root-level e2e suites) — every behavioral assertion preserved; signature-level mechanical rewires only. `go test ./internal/substrate/...`. `go test ./internal/loom/...` and `./internal/processor/...` must stay green UNTOUCHED (this story does not edit them).
  - [ ] Verification gates (all, before declaring done): `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`, `make test-bypass` (Gate 2, all BLOCKED), `make test-capability-adversarial` (Gate 3, all DEFENDED).
  - [ ] Update `docs/components/substrate.md` with the ConsumerSupervisor surface (layering: dumb `RunDurableConsumer` vs supervised `ConsumerSupervisor`; hooks; HealthSink; NakWithDelay). New docs go in `/docs`, never `_bmad-output/`.

## Dev Notes

### Adjudicated architecture (binding — encode, do not re-litigate)

Mechanism vs policy, three layers:

| Layer | Owner | Content |
|---|---|---|
| 1 (exists, stays dumb) | `substrate.RunDurableConsumer` (`internal/substrate/consumer.go`) | one-shot bind+pump+ack; untouched except the additive `NakWithDelay` plumbing |
| 2 (NEW, this story) | `substrate.ConsumerSupervisor` | MECHANISM: spec registry + reconcile (`Add`/`Remove`/`Reset`/`Stop`), per-consumer pause state machine (infra/structural/manual, composable), `NakWithDelay` backoff floor, HealthSink persist/restore |
| 3 (callers) | POLICY via hooks | `Classify`, `Probe`, message handler. Refractor: `failure.Classify` + `adapter.Probe` + `processMsg` policy |

The supervised pump being extracted is `pipeline.Run`'s skeleton — the loop that decides
when/whether to pull, with Health-KV-persisted pause state. The bind (`consumer.Manager`) folds
into the supervisor's specs; it is not itself the reusable asset.

### Grounding map (read these before writing code)

- `internal/refractor/pipeline/pipeline.go` — `Run` (~294: restore → manual-pause check → pendingCons swap → `Messages()` → `drain` → classify-and-pause loop), `restoreHealthState` (~518), `drain` (~605, returns `(failure.Category, error)`; messages left pending on Infra/Structural), `processMsg` (~645, does its OWN ack/nak per message, incl. ack-and-skip and retry-queue deferred disposition), `Pause` (~991) / `Resume` (~1008), `initResumeCh`/`clearResumeCh` (~967–983), `runInfraProbeLoop` (~1075), `setHealthPaused`/`setHealthActive` (~1115), `Rebuild` (~437) / `watchRebuildCompletion` (~490)
- `internal/refractor/consumer/manager.go` — `Add` (47) / `Remove` (87) / `Reset` (116) / `Stop` (151); queue group = durable name `refractor-<ruleID>`; `DeliverLastPerSubjectPolicy` rationale (ADR-15) in `Add`'s godoc
- `internal/refractor/failure/classify.go` — 4-tier `Category` (`CatTransient`/`CatTerminal`/`CatInfra`/`CatStructural`) + `Classify` + explicit wrapper constructors
- `internal/refractor/health/reporter.go` — `Entry` schema (status active|paused|rebuilding; `PauseReason` *string null-when-active; `ErrorCount`/`ConsumerLag` preserved across writes), `SetActive`/`SetPaused`/`SetRebuilding`/`GetStatus`; KV key = bare ruleID (`put`, :297)
- `internal/substrate/consumer.go` — `Decision` (iota: Ack/Nak/Term), `HandlerFunc`, `Message{Subject, Body, Sequence}`, `DurableConsumerConfig`, `RunDurableConsumer`, `applyDecision`
- `cmd/refractor/main.go` — wiring: `manager.Add(ctx, r.ID)` (~315) → `cons := manager.Consumer(r.ID)` (~319) → `go p.Run(lensCtx, cons)` (~341)
- `docs/contracts/05-health-kv.md` — Contract #5, FROZEN. Build to it; a genuine gap goes through `cmd/<area>/CONTRACT-AMENDMENT-REQUEST.md`, never an edit.
- `docs/components/weaver.md` — future-caller grounding ONLY: 3 lanes (per-target lane-1 KV watch, per-domain lane-2 `events.<domain>.>`, temporal lane-3); Story 9.4 disable/revoke maps to the supervisor's `Pause`/`Remove`. Cite as a constraint on the exported surface; build NOTHING for it.
- `_bmad-output/planning-artifacts/lattice-architecture.md` #D2 (~line 1088) — per-domain durable consumers reconciled from a binding registry; the supervisor's reconcile generalizes this shape.

### Known migration costs (priced into Tasks 4 and 7 — do not discover them mid-flight)

1. **Thin `Message` view.** `processMsg` consumes `jetstream.Msg` directly (subject-prefix strip, `msg.Data()`, metadata, and self-managed ack/nak including async disposition by the retry queue). The handler boundary needs additive enrichment — design it; do NOT expose jetstream types.
2. **`drain` is a rewrite, not a wrapper.** Refractor's drain does its own ack choreography around the retry queue; unifying on Decision + Classify hooks rewrites `Run`'s skeleton. Escape hatch: if the supervisor genuinely cannot host a pipeline behavior without breaking a test, STOP that subtask and write it up in Questions for Winston, naming the failing test.
3. **Pause composability is NEW.** Today's pipeline holds one pause at a time; the supervisor tracks a reason set. Refractor's single-`PauseReason` health Entry persists the highest-precedence active reason (manual > structural > infra).
4. **No handles out** means the lag poller / rebuild lag-watch must get consumer info another way (Refractor-owned jetstream query by durable name, or a substrate-typed accessor).

### Out of scope — do NOT pull in

- **Loom adoption is Story 8.5** in its entirety: teardown of `loom-<domain>` consumers, relay `NakWithDelay` backoff, `health.loom.<instance>` Contract-#5 heartbeats, Loom pause-restore. **This story does not touch `internal/loom/`** (its tests must pass untouched).
- **Weaver (Epic 9)** — grounding context only; no `internal/weaver` code.
- No exponential backoff (fixed configurable floor — adjudicated). No `MaxDeliver` anywhere on supervisor consumers. No edits to `docs/contracts/*` or `_bmad-output/planning-artifacts/*`.

### House rules (binding, from CLAUDE.md)

- **NO history/changelog comments in code** — no `// Story 8.4`, `// Previously`, `// extracted from Refractor`, `// moved from pipeline.go`. Comments describe what the code does NOW. (Most-violated rule; extraction stories are the classic trigger — git blame is the record.)
- `docs/contracts/*` are FROZEN; gaps via `cmd/<area>/CONTRACT-AMENDMENT-REQUEST.md`.
- New documentation goes in `/docs`, not `_bmad-output/`.
- Sub-agents never commit, push, or branch — leave changes in the working tree; the lead adjudicates and commits.
- Verification gates before declaring done: `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`, `make test-bypass` (Gate 2, all BLOCKED), `make test-capability-adversarial` (Gate 3, all DEFENDED), plus `go test` for `internal/substrate`, `internal/refractor/...` (full), `internal/loom` (green untouched).

### Project Structure Notes

- New code: `internal/substrate/consumer_supervisor.go` (+ siblings, e.g. `consumer_supervisor_test.go`, optionally a health-sink file) — flat in `internal/substrate`, NO new sub-package, matching the package's existing one-concern-per-file style (`consumer.go`, `subscribe.go`, `publish.go`).
- Modified: `internal/substrate/consumer.go` (additive Decision), `internal/refractor/pipeline/pipeline.go`, `internal/refractor/consumer/manager.go`, `cmd/refractor/main.go`, their tests (mechanical rewires only), `docs/components/substrate.md`.
- nats.go is pinned at v1.52.0 (`go.mod`) — `msg.NakWithDelay` is available; no dependency changes.

### Previous story intelligence (8.1 / 8.2, both done)

- Loom consumers run on bare `RunDurableConsumer` (`engine.go:187` trigger, `:297` per-domain `loom-<domain>`, `:577` deadline watcher on the `$KV.<bucket>.` CDC stream; `actuator.go:63` relay) — this is the 8.5 adoption surface; keep the supervisor's spec expressive enough for all four (event-stream AND KV-CDC filters), but do not rewire them now.
- 8.2's review produced the filter-change hazard (a durable whose desired filter differs from the running one must be `Reset`, never silently unchanged) — AC 6e exists because JetStream rejects in-place filter updates on some config combinations; test it for real against embedded NATS.
- `internal/substrate/consumer_test.go` shows the established embedded-NATS test harness pattern for this package — follow it.

### References

- [Source: _bmad-output/planning-artifacts/epics/phase-2-epics.md#Story 8.4]
- [Source: docs/contracts/05-health-kv.md (Contract #5)]
- [Source: docs/components/weaver.md#The 3 lanes]
- [Source: docs/components/substrate.md]
- [Source: _bmad-output/planning-artifacts/lattice-architecture.md#D2]
- [Source: internal/refractor/pipeline/pipeline.go; internal/refractor/consumer/manager.go; internal/refractor/failure/classify.go; internal/refractor/health/reporter.go; internal/substrate/consumer.go; cmd/refractor/main.go]

## Questions for Winston

1. **Health-key wording vs code.** The adjudication describes the caller-supplied key as `health.refractor.<ruleId>` "today", but `health.Reporter.put` writes the bare `<ruleId>` as the KV key (the `health.<component>.<instance>` pattern is the Contract-#5 heartbeat key, written by the separate heartbeater). This story preserves Refractor's existing key byte-identically (regression net wins); the supervisor takes a fully caller-supplied key so Loom can use `health.loom.<instance>` in 8.5. Confirm that reading.
2. **Pause-reason precedence.** Composable reason set persisted through Refractor's single-`PauseReason` Entry as manual > structural > infra. Acceptable tie-break, or should the Entry schema grow (would touch the FROZEN-adjacent health schema doc)?
3. **`Stop` semantics.** Refractor's `Manager.Stop` deletes all durables; substrate's `RunDurableConsumer` doctrine is "never delete on shutdown — the persisted position is the point of durable". The supervisor exposes both intents (`Stop` = stop pumps; `Remove` = stop + delete); the Refractor migration keeps whatever its tests assert. Flag if you want a different default.
4. **Zero-value `RedeliveryDelay`.** When a spec omits the redelivery floor and a handler returns `NakWithDelay`: fall back to plain `Nak` or to a package-default floor? Story directs the dev to pick one and document it — pre-empt here if you have a preference.

## Winston's rulings (adjudicated — binding; dev builds to these)

1. **Q1 → CONFIRMED.** Refractor's existing KV key (bare `<ruleId>`) stays byte-identical — the regression net wins over the adjudication's shorthand wording. The supervisor's health key is fully caller-supplied (key AND bucket, via the sink); the `health.<component>.<instance>` Contract-#5 heartbeat shape is what Loom supplies in 8.5. The supervisor never invents or namespaces keys.
2. **Q2 → precedence CONFIRMED (manual > structural > infra); do NOT grow the Entry schema.** Today's pipeline never persists two reasons at once, so the tie-break only governs the new composable machine, and the health schema stays untouched. Accepted consequence (document in the godoc; no extra machinery): restore recovers only the highest-precedence reason — e.g. manual+infra persists `manual`, and after an operator `Resume` the lost infra pause simply re-presents on the next pump failure and re-enters the probe loop. Self-healing; correct.
3. **Q3 → split CONFIRMED, with the substrate default made explicit.** Supervisor `Stop` = stop pumps, **never** deletes durables (the persisted position is the point of a durable — substrate doctrine). `Remove` = stop + delete. Refractor's shutdown keeps its current delete-all behavior by invoking `Remove` per durable from its own adapter layer — whatever keeps the `Stop` assertions in `manager_test.go` green. Delete-on-stop is *Refractor's* policy, not the supervisor's.
4. **Q4 → package-default floor; never silent plain-`Nak`.** A handler returning `NakWithDelay` has expressed "do not hot-loop"; degrading to immediate redelivery on a missing config value silently reintroduces exactly the F4 failure this story exists to kill. Use a package-level default (5s — same order of magnitude as `durableReconnect`), document it in the godoc, allow the spec to override.

## Dev Agent Record

### Agent Model Used

Opus (dev sub-agent; ran out of tokens during final verification). Lead (Winston) completed the mechanical finish: removed six orphaned `manager := consumer.NewManager(...)` declarations in the root-level Refractor e2e tests, then ran all gates.

### Completion Notes List

- All verification gates pass: `go build`, `make vet`, `golangci-lint` (0 issues), `make verify-kernel` (live stack — the migrated refractor booted and projected the bootstrap capabilities), `make test-bypass` (all BLOCKED), `make test-capability-adversarial` (3 DEFENDED + 1 ACCEPTED-WINDOW baseline), `go test` substrate + full refractor (regression net green) + loom + processor (green untouched; one `TestOutbox_NoDoublePublish` failure was an embedded-NATS tempdir flake — passes repeatedly in isolation).
- **OPEN ITEM (Task 8, not done):** `docs/components/substrate.md` was never updated with the ConsumerSupervisor surface — the dev agent exhausted tokens first. Fold into the post-review fix pass.
- Task checkboxes were not ticked by the dev agent (token exhaustion); gate results above are the lead's verification, not per-subtask audit. The code-review layer adjudicates AC coverage.
- Notable shape choice for reviewers: `internal/refractor/consumer/manager.go` was NOT modified — the rewire lives in `internal/refractor/pipeline/supervisor_adapt.go` + `cmd/refractor/main.go` (e2e tests build specs via `e2e_supervisor_helper_test.go`). The story allowed "thin shim or deletion — judged by the regression net"; reviewers should check whether the retained Manager is still a live code path or dead weight.

### File List

- New: `internal/substrate/consumer_supervisor.go`, `consumer_supervisor_pump.go`, `consumer_supervisor_spec.go`, `consumer_supervisor_test.go`, `nak_with_delay_test.go`; `internal/refractor/pipeline/supervisor_adapt.go`; `internal/refractor/e2e_supervisor_helper_test.go`
- Modified: `internal/substrate/consumer.go`, `internal/substrate/keys.go`; `internal/refractor/pipeline/pipeline.go`, `pipeline_test.go`; `cmd/refractor/main.go`; 7 root-level Refractor e2e tests (supervisor wiring)

### Code Review Triage (Winston)

Three-layer review complete: Blind Hunter (`8-4-cr-blind-hunter.md`, 0C/2M/3m/2n), Edge Case Hunter (`8-4-cr-edge-case-hunter.md`, 0C/3M/6m), Acceptance Auditor (`8-4-cr-acceptance-auditor.md`, 20/20 SATISFIED, all rulings honored). Both hunters independently report the supervisor core clean; Majors cluster in the Refractor adapter seam.

**Fix-forward (this story):** BH-M1 `awaitStarted` timeout must log a warning (silent 2s no-op on early `Run` exit); BH-M2 `RunOn` guarded against double invocation (pump goroutine leak); BH-m fix the dead/mismatched test publish in `consumer_supervisor_test.go`; BH-m add DeliverGroup-survives-`Reset` test coverage; BH-n revert the cosmetic `keys.go` doc-comment reformat (diff noise); ECH-M1 documented in `Resume`'s godoc (a failure escalation discovered after a Resume was issued requires a fresh Resume — verified behavior-equivalent to the pre-migration probe-loop race); Task 8 `docs/components/substrate.md` ConsumerSupervisor section.

**Deferred as pre-existing (verified against `git show HEAD`, not introduced by this story; spun off):** ECH-M2 (probe recovery persists "active" over an in-flight "rebuilding" status — old `setHealthActive` had no rebuild guard either); ECH-M3 (partial-batch redelivery re-enqueues already-queued retry entries — identical control flow in the old `writeResults`); BH-n adjacency ruleID collision (documented pre-existing caveat). ECH minors m1–m6: benign wasted iterations / perf nits / operator-recoverable paths — deferred.

### Debug Log References

### Completion Notes List

### File List
