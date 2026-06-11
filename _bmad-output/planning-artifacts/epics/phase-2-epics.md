# Phase 2 Epics — Detailed Stories (Orchestration Core)

> Shard of [epics/index.md](./index.md) — **Epics 7–11** (active work). Phase 1 stories live in [phase-1-epics.md](./phase-1-epics.md).

> Decisions of record: `lattice-architecture.md` → "Phase 2 Architecture — Orchestration Core" (D1–D5). Engine detail: `docs/components/{loom,weaver}.md`. Shapes: `docs/contracts/10-orchestration-surfaces.md`. **Every story brief carries the binding comment policy** (Anti-Patterns table): comments describe WHAT/WHY, never which story decided/changed it. Cadence: session-per-story; Winston runs CS→DS→CR via sub-agents that don't commit, adjudicates/commits/watches CI.

## Epic 7: Orchestration Foundations

**Goal:** The platform can model, assign, complete, and surface **tasks** — and is provisioned to run orchestration (service actors, schedule stream). Self-demonstrable via direct ops before any engine exists.
**FRs covered:** FR26 (task substrate), FR29

### Story 7.1: `orchestration-base` package + `task` DDL + CreateTask (assignee required)

As a platform developer,
I want a foundational `orchestration-base` package defining the generic `task` vertex type and a `CreateTask` operation that **requires and validates an assignee identity**,
So that any package (and the engines) can create tasks through the standard write path, and a task can never exist unassigned.

**Acceptance Criteria:**

**Given** the `orchestration-base` package (`packages/orchestration-base/{manifest.yaml,ddls.go,...}`)
**When** it is installed via the `InstallPackage` kernel op
**Then** a `task` DDL meta-vertex exists per Contract #10 §10.1 — root `data` = scalars only `{ status, expiresAt }`; relationships as **links** (`assignedTo` → identity, `forOperation` → op meta, `scopedTo` → target); **no aspects** (UI renders from the bound op's self-describing DDL via `forOperation`; the speculative `presentation`/`params` aspects were dropped — Contract #10 §10.1)
**And** `CreateTask` **requires an `assignee` identity**; its Starlark JIT-hydrates and validates the identity, **rejecting** with a structured `ScriptError` if it is absent/invalid (single-op invariant, P4) — no orphan task is ever committed
**And** a successful `CreateTask` commits atomically `vtx.task.<id>` (`status: open`) + the `assignedTo`/`forOperation`/`scopedTo` links (each: task = source, other vertex = target, per Contract #1 §1.1)
**And** per the **(a1) extraction** (Contract #10 §10.1/§10.7, Contract #6 §6.6 amendment) the FR56 ephemeral-grant projection is **owned by this package**: `orchestration-base` ships a **`capabilityEphemeral` lens** that walks `assignedTo`/`forOperation`/`scopedTo` (+ `reportsTo` 2-hop) and projects each grant `{source, taskKey, operationType, target, expiresAt}` to the disjoint key **`cap.ephemeral.<actor>`** (link-sourced — *not* the old `task.data.grantedOperationType`/`targetKey` fields)
**And** the **bootstrap `capability` cypher drops its `task` OPTIONAL MATCHes** (core no longer references the `task` package type); **step-3's task-branch reads `cap.ephemeral.<actor>`** (matching logic unchanged) and falls back to `cap.<actor>` for `roles` on a task-path denial; field-shaped task fixtures **and the Phase-1 cap-lens conformance/bypass tests that read `ephemeralGrants`** are migrated
**And** install is atomic and idempotent (re-install is a no-op or fails closed, per Contract #8)

*FRs: FR26, FR29 (no-orphan by construction) · Depends on: 1.5.5 (InstallPackage) · Model: Opus (DDL + package + contract) · Grounding: Contract #10 §10.1, D5, P4*

### Story 7.2: Task lifecycle ops (ReAssign / Complete / Cancel) + my-tasks Lens

As a staff actor,
I want to reassign, complete, or cancel a task,
So that tasks have a full lifecycle and an identity can see the tasks assigned to it.

**Acceptance Criteria:**

**Given** an open task (already assigned at creation, per 7.1)
**When** a `ReAssignTask` op runs
**Then** its Starlark **validates the new assignee identity and rejects if invalid** (same single-op invariant as CreateTask); on success it re-points the `assignedTo` link atomically (old `task→identity` link removed, new one committed)
**And** **auto-completion (§10.6, primary path):** when an op authorized via `authContext.task = T` commits successfully, the **commit path injects** T's `status → complete` + `TaskCompleted(T)` event into the **same atomic batch** (platform-injected, like provenance; in the step-3 task-auth code path) — so performing the granted op fulfills its task with no separate call and no wedge
**And** `CompleteTask` (explicit admin/out-of-band) / `CancelTask` (no-longer-needed) ops transition `status` to `complete` / `cancelled` (root `data`), emitting `TaskCompleted` / `TaskCancelled` via the outbox
**And** a "my-tasks" Lens projects, per identity, its open tasks (queryable surface for verification/UI)
**And** transitions are validated (cannot complete a cancelled task; OCC on the task root)

*FRs: FR26, FR29 · Depends on: 7.1 · Model: Opus · Grounding: Contract #10 §10.1/§10.6/§10.7, P4*

### Story 7.3: Service-actor bootstrap provisioning

As the platform operator,
I want Loom and Weaver provisioned as internal service-actor identities at bootstrap,
So that the engines can submit operations as authenticated root-equivalent actors.

**Acceptance Criteria:**

**Given** primordial bootstrap (Contract #7)
**When** the platform starts
**Then** `identity:loom` and `identity:weaver` service-actor vertices exist with pre-provisioned signing keys and root-equivalent capability (arch §92)
**And** their provisioning **extends** the existing primordial bootstrap path (does not introduce a parallel actor-provisioning mechanism)
**And** an op submitted with `Lattice-Actor: identity:weaver` passes commit-path step-3 auth identically to a human actor

*FRs: (enabler) · Depends on: Contract #7 · Model: Opus (bootstrap + security) · Grounding: arch §92 internal service actor model*

### Story 7.4: Platform-wide message-scheduling stream (ADR-51)

As the platform operator,
I want a platform-wide message-scheduling stream provisioned at bootstrap,
So that any component (Weaver's temporal lane first) can use NATS native message scheduling.

**Acceptance Criteria:**

**Given** primordial bootstrap
**When** the platform starts
**Then** a **platform-wide** `core-schedules` stream exists with `AllowMsgSchedules: true` (a platform capability like Health KV — *not* Weaver-owned; same config shape as the existing atomic-publish flag; `core-*` family name, no project-name prefix)
**And** publishing on `schedule.<domain>.<kind>.<entityId>` (`<entityId>` = NanoID, full key in payload) is the shared ingress; the publisher chooses the republish target subject (so each component consumes only its own fired messages)
**And** a smoke test publishes an `@at` scheduled message and observes republish to the chosen target subject at the scheduled time
**And** re-publishing to the same schedule subject replaces the prior schedule (one schedule per subject)

*FRs: (enabler) · Depends on: Contract #7 · Model: Sonnet (config, well-bounded) · Grounding: Contract #10 §10.4, ADR-51, D4*

### Story 7.5: No-orphan task invariant (FR29 by construction)

> **Status: WON'T DO — closed 2026-06-05.** Already satisfied by construction; no new mechanism is
> warranted. The only `orchestration-base` ops that create or re-point an `assignedTo` link are
> `CreateTask` (7.1) and `ReAssignTask` (7.2), and **both already validate the target identity is
> alive and reject with a structured `UnknownAssignee` error** (CreateTask also validates
> `forOperation` + `scopedTo`). `CompleteTask`/`CancelTask` only flip status and cannot orphan. So
> no-orphan-at-creation is **total today**. Loom/Weaver introduce no new vector — they submit the
> same *known* ops through the same validated write path (no privileged task-creation backdoor), so a
> general structural guard would be redundant. The two cases this story nominally reserved for are
> out of scope by its own AC: **post-hoc cross-package orphaning** (identity tombstone vs. open
> tasks) is deferred to the data-contracts session, and the **FR28 role-queue unroutable** case is
> Phase 3 (the FR29 Health-KV monitor). No story file was created.

As an operator,
I want it to be impossible for a task to reference a non-existent assignee,
So that no task is ever silently orphaned — enforced as an invariant, not monitored after the fact.

**Acceptance Criteria:**

**Given** the create-time / reassign-time assignee validation (7.1, 7.2)
**When** any `orchestration-base` op would leave a task pointing at an invalid identity
**Then** the op is **rejected** with a structured error — a task with an invalid assignee is never committed at write time (no-orphan-at-creation is total, and lives entirely inside `orchestration-base`)
**And** because Phase 2 assigns to concrete identities (FR28 role-queues deferred), no "unrouted-queue" case exists at creation

> **Open design item — post-hoc orphaning (cross-package).** Identity lifecycle (tombstone/merge) lives in the `identity-domain`/`identity-hygiene` **packages**, not core; tasks live in `orchestration-base`. So "refuse to tombstone an identity that holds open tasks" is a **cross-package referential-integrity** question, not a core invariant we can assert here — it would require either the identity package to depend on the task type, or a general platform referential rule (which doesn't exist today; deletes are soft via `isDeleted`). **Deferred to the Loom/Weaver data-contracts session** (see below) to decide: generic referential rule vs. a package-interaction convention vs. accept-and-reconcile. Do NOT bake a cross-package mechanism into this AC.
>
> **Phase 3 note:** when FR28 (role-based queues + fallback) lands, a task assigned to a role-queue with no eligible actor *is* genuinely unroutable and cannot be prevented at creation — the FR29 **Health-KV monitor** returns then. Phase 2 = FR29-by-construction *at creation*; the post-hoc and queue cases are later work.

*FRs: FR29 (safety, creation-time) · Depends on: 7.1, 7.2 · Model: Sonnet · Grounding: P4 single-op invariants; FR29 never-silently-dropped*

### Story 7.6: Substrate durable-consumer primitive

As a platform developer,
I want a minimal durable-consumer primitive in `internal/substrate`,
So that the outbox, Loom, and Weaver share one ack-disciplined consumer rather than each wiring raw `nats.io`.

**Acceptance Criteria:**

**Given** the existing minimal consumer pattern in `internal/processor/outbox` (durable bind, pull, ack-on-confirmed, resume-from-last-ack)
**When** it is generalized into `internal/substrate` (e.g. a `DurableConsumer`: bind to a stream + filter subject, consume-with-ack, resume)
**Then** the **outbox** is refactored onto the substrate primitive (behavior-preserving; tests green) — proving the surface is sufficient
**And** the surface is the **minimal** common need — it does NOT include Refractor's pause / lag-poll / reset / `DeliverLastPerSubject` machinery (that stays in Refractor; its migration onto this base remains the existing deferred substrate-inner-package carry)
**And** Loom (8.1) and Weaver (9.1) consume this primitive; no new `nats.io`/`jetstream` handles appear in `internal/loom` or `internal/weaver`

*FRs: (enabler) · Depends on: 1.5.10/1.5.11 (outbox), 1.5.1 (substrate write-path) · Model: Opus (substrate primitive, multiple consumers) · Grounding: deferred carry "extend internal/substrate with durable-consumer helpers"; read-from-body discipline*

## Epic 8: Loom — Deterministic Flow Engine

**Goal:** Deterministic multi-step procedures (user-task or system-op steps) run to completion via a generic interpreter; patterns are package data.
**FRs covered:** FR26 (conditional = on/off guards + human-approval = user-tasks; *branching* is Weaver, per D3)

### Story 8.1: Loom engine machinery

As a platform developer,
I want the Loom engine machinery stood up — durable pattern source, event-driven trigger, per-domain completion consumers, and a durable correlation index — driving one simple system-op pattern to completion,
So that the core interpreter loop is proven before user-tasks and guards.

**Acceptance Criteria:**

**Given** a `meta.loomPattern` of system-op steps (no guards) with `completionDomains` (default `[subjectType]`), installed as package/fixture data
**When** a caller submits `StartLoomPattern{patternRef, subjectKey}` and its committed `loom.patternStarted` event reaches the engine (`internal/loom`, `identity:loom` actor)
**Then** the engine (patterns loaded via a durable Core-KV subscription) instantiates, reconciles one durable per-domain consumer per `completionDomains` entry (D2), and runs `event → advance cursor → submit next op → event` to `CompletePattern` (`loom.patternCompleted`)
**And** the instance lives only in `loom-state` (operational; never a Core-KV vertex), keyed `instance.<id>` + a durable `token.<token>` reverse pointer, each transition one `AtomicBatch` (`loom-state` provisioned `AllowAtomicPublish`)
**And** completions correlate by `requestId` in the event body via the durable `token.` pointer (domain-independent; no in-memory index); a rejected step fails off-stream via the submit reply
**And** on engine restart the durable consumers resume and the run completes exactly once (idempotent via the Contract #4 tracker + token-pointer presence)
**And** `loom` imports only `substrate/*` (no Go import of Processor/Weaver)

*FRs: FR26 · Depends on: 7.1, 7.6 (substrate durable-consumer), 1.5.10 (outbox) · Model: Opus (engine, multi-file) · Grounding: `docs/components/loom.md`, Contract #10 §10.3/§10.5/§10.6/§10.9, D2*

### Story 8.2: User-task steps

As a flow author,
I want a Loom step that assigns a task and advances when the user completes the bound operation,
So that human-in-the-loop flows (onboarding) run deterministically.

**Acceptance Criteria:**

**Given** an onboarding pattern `[collectName, collectPhone, collectAddress]` of user-task steps
**When** Loom reaches a step
**Then** the Actuator submits an op creating a task assigned to the identity, with the step's bound operation set (`task.operation`)
**And** the flow waits; when the user submits the bound op (e.g. `SetName`) the completion event advances the cursor to the next step
**And** completing the final step emits `OnboardingComplete`
**And** a long wait does not break correctness (cursor durable in `loom-state`)

*FRs: FR26 · Depends on: 8.1, 7.2 · Model: Opus · Grounding: `docs/components/loom.md` core model*

### Story 8.3: Pure on/off guards + cursor rebuild

As a flow author,
I want steps to carry an optional on/off guard that skips already-satisfied steps,
So that one pattern serves both "collect" and "verify-info," and the cursor is crash-recoverable.

**Acceptance Criteria:**

**Given** a step carrying an optional `guard` — a pure predicate expressed as **pattern data** (the `Step.guard` field already exists in `internal/loom/pattern.go`, rejected today by `validate()`), evaluated by the engine over current Core-KV state
**When** Loom evaluates the step and the guard is false (data already present)
**Then** the step is skipped (no task created), cursor advances; guard true → step runs
**And** guards are **pure and deterministic by construction**: the guard vocabulary is a restricted predicate over instance/task aspects (no side effects, no external reads), so purity is a property of the predicate language — not a runtime check; the engine rejects any guard outside the vocabulary
**And** with `loom-state` **lost entirely** (disaster recovery — normal restart resumes from the durable `loom-state` cursor + `token.` index per §10.6), the engine can **re-derive the cursor by replaying guards** over Core KV tasks (first step whose guard is true and whose task is incomplete) — sound by construction because the cursor is a function of guarded state; a recovery test asserts identical resumption
**And** no branches/loops/fan-out are expressible (linear only)

*FRs: FR26 · Depends on: 8.2 · Sequenced after 8.4, 8.5 · Model: Opus · Grounding: `internal/loom/pattern.go` (Step.guard), D3 guard purity, Contract #10 §10.5*

### Story 8.4: Substrate ConsumerSupervisor — extract Refractor's supervised pump, migrate Refractor (F4 hardening)

As a platform developer,
I want Refractor's supervised consumer runtime — the pause/probe/health state machine plus the bind lifecycle — extracted into a substrate `ConsumerSupervisor` that Refractor itself then runs on,
So that Loom (8.5) and Weaver (Epic 9: per-target lane-1 + per-domain lane-2 consumers; 9.4's disable/revoke control surface = the supervisor's `Pause`/`Remove`) reuse one supervised pump instead of each hand-rolling lifecycle, backoff, and health.

**Acceptance Criteria:**

**Given** Refractor interleaves a supervision state machine inside `pipeline.Run` (failure classification → infra **probe loop** / structural pause / manual pause; rebuild consumer hot-swap; `restoreHealthState` reading pause-state back from Health KV at startup) with projection processing, while `consumer.Manager` owns only the bind (`Add`/`Remove`/`Reset`/`Stop`)
**When** the **mechanism** is extracted into `substrate.ConsumerSupervisor`:
- a registry of desired consumer specs + desired-vs-running **reconcile** (`Add`/`Remove`/`Reset` = delete-and-recreate/`Stop`), each spec carrying its full config — deliver policy, queue group, filter — supplied by the caller, never hard-coded
- a per-consumer **state machine**: Running → PausedInfra (probe loop) / PausedStructural / PausedManual (await `Resume`) — pause reasons stay **distinct and composable** (resume-from-infra never clears an operator pause)
- **backoff**: a `NakWithDelay` decision appended to substrate's `Decision` enum (binary-additive; `Ack`/`Nak`/`Term` values unchanged) with a fixed configurable redelivery floor — retry *cadence* bounded, retry *count* never (the supervisor never sets `MaxDeliver`)
- a **HealthSink**: state transitions persisted to Health KV (Contract #5) under a **caller-supplied key prefix** (`health.refractor.<ruleId>` / `health.loom.<instance>` / `health.weaver.<target>`) and restored on startup (generalized `restoreHealthState`)
**Then** policy stays with the caller via hooks — `Classify(err) → Transient|Terminal|Infra|Structural`, `Probe(ctx) error`, and the message handler — the supervisor owns mechanism only, stays agnostic between event-stream and KV-CDC durables, and **no `jetstream` handle escapes its exported surface** (Loom/Weaver import only `substrate/*`)
**And** **Refractor is migrated onto the supervisor**: `pipeline.Run`'s supervision skeleton is hosted by the supervisor; Refractor keeps its processing policy (`drain`/`processMsg`, retry queue, DLQ, audit, lag poller, `failure.Classify`, `adapter.Probe`); the pipeline + consumer-manager test suites are the **regression net** — every behavioral assertion preserved (mechanical rewires to new signatures permitted; behavior changes are not), queue-group fan-out (NFR12) + `DeliverLastPerSubjectPolicy` (ADR-15) intact
**And** tests assert: a handler returning `NakWithDelay` does not hot-loop at zero delay; retry count remains unbounded; a manual pause survives restart via Health KV; an infra pause enters the probe loop and recovers on a passing probe; `Reset` recreates a durable whose filter changed

*Replaces the earlier thin-adapter plan after an architecture review: the reusable asset is Refractor's supervised pump (when/whether to pull + health-persisted pause state), not the bind. Validated against Weaver's Epic 9 requirements. Loom adoption moved to 8.5. Depends on: 8.1, 8.2, 7.6 · Model: Opus · Grounding: internal/refractor/pipeline/pipeline.go (`Run`, `restoreHealthState`, `runInfraProbeLoop`, `Pause`/`Resume`, `Rebuild`/`pendingCons`), internal/refractor/consumer/manager.go, internal/refractor/failure/classify.go, internal/refractor/health/reporter.go, internal/substrate/consumer.go, docs/contracts/05-health-kv.md, docs/components/weaver.md (3 lanes + FR30)*

### Story 8.5: Loom adopts ConsumerSupervisor — teardown, backoff, filter-reset, Health KV (F6 hardening + observability)

As a platform operator,
I want Loom's durables (trigger, per-domain completion, relay, deadline-watcher) driven through the substrate `ConsumerSupervisor`,
So that a removed pattern leaks no consumer, a sustained failure backs off instead of hot-looping, a filter change recreates cleanly, and Loom's liveness is observable in Health KV.

**Acceptance Criteria:**

**Given** Loom hand-rolls its consumers on the bare `RunDurableConsumer` and `reconcileConsumers` (`internal/loom/engine.go`) is additive-only (the `domainConsumers` map never shrinks)
**When** Loom drives all its durables through the supervisor (8.4)
**Then** `reconcileConsumers` becomes a desired-vs-running diff over `Pattern.Domains()` aggregated across the pattern snapshot; when the **last** pattern referencing a domain is removed, the `loom-<domain>` consumer is torn down **and its JetStream durable deleted** — *adjudicated: F6's guarantee is "no leaked consumer," and an un-pumped server-side durable IS the leak; correctness on a future re-add rests on `loom-state` + Contract #4 idempotency + `token.` pointer presence, not a preserved ack floor, so a `DeliverAll` replay on re-add is safe and its cost accepted* — *subsumes the former F6 story*
**And** a domain whose desired filter/config differs from the running durable is `Reset` (delete-and-recreate), never silently unchanged — *covers Story 8.2 review ECH Path #3*
**And** the relay's publish-failure path returns `NakWithDelay` (bounded cadence, unbounded count — at-least-once preserved; no `MaxDeliver`) — *subsumes the former F4 story on the Loom side*
**And** Loom publishes `health.loom.<instance>` to Health KV via the supervisor's HealthSink (Contract #5 names `loom` as a Phase-2 publisher: `status`/`heartbeatAt`/`metrics` — running instance count, per-domain consumer states, relay + deadline-watcher liveness — + `issues`), and consumer pause-state persists and restores across a Loom restart
**And** `loom` still imports only `substrate/*` (8.1 AC#8 holds)
**And** tests assert: a removed-pattern domain consumer is torn down and its durable deleted; a filter change recreates; no zero-delay spin; a well-formed Contract-#5 heartbeat; pause-state survives a Loom restart

*Health-KV publishing arrives with supervisor adoption (no separate observability story). Depends on: 8.4 · Model: Opus · Grounding: internal/loom/engine.go (`reconcileConsumers`, `runTriggerConsumer`, `runDeadlineWatcher`, relay), internal/loom/pattern.go (`Domains()`), docs/contracts/05-health-kv.md, Contract #10 §10.6/§10.9*

## Epic 9: Weaver — Convergence Engine

**Goal:** A declared target state converges; gaps detected + remediated; operators manage targets. Weaver consumes the Refractor (target = Lens); never a cypher runtime.
**FRs covered:** FR27, FR30

### Story 9.1: Target-as-Lens + violation-driven lane + OCC actuator

As a platform developer,
I want Weaver to watch a target Lens's violation output and remediate gaps,
So that a declared target state converges.

**Acceptance Criteria:**

**Given** a fixture target Lens projecting **one row per candidate with a `violating` flag** (+ gap columns) to the shared `weaver-targets` bucket, key `<targetId>.<entityId>` (NATS-KV; entity = vertex, key on the NanoID, full `entityKey` in the value)
**When** a row with `violating: true` appears
**Then** the Sensorium enqueues lane-1 work; Evaluator L1 confirms still-violating + not in-flight; L2 classifies the gap; Strategist selects a playbook; the Actuator submits a remediation op via the Processor with an **OCC revision-condition**
**And** triggering a Loom utility is done **via an op** (not a Go call)
**And** when the gap closes the row's flag flips `false` via upsert (no retraction needed); Weaver stops acting
**And** `weaver` imports only `substrate/*`

*FRs: FR27 · Depends on: 7.x, 7.6 (substrate durable-consumer), 8.1, 1.5.1 (OCC revisions) · Model: Opus (engine) · Grounding: `docs/components/weaver.md`, D4*

### Story 9.2: Anti-storm in-flight marks + TTL reconciliation

As a platform developer,
I want in-flight convergence marks with a TTL and reconciliation,
So that Weaver neither re-triggers on a persisting violation nor wedges forever after a crash.

**Acceptance Criteria:**

**Given** a violation Weaver is already remediating
**When** the same row is re-observed before it re-projects (CDC lag)
**Then** a `weaver-state` in-flight mark (key `<targetId>.<entityId>.<gapColumn>`, set via CAS-create) suppresses re-triggering
**And** the mark carries a **TTL/lease**; a reconciliation reclaims expired leases
**And** a **mid-flight-kill test** (Actuator crashes after marking in-flight, before completing) asserts the lease expires and the target is re-attempted — never permanently wedged

*FRs: FR27 · Depends on: 9.1 · Model: Opus · Grounding: D3/D4 anti-storm; `docs/components/weaver.md` failure modes*

### Story 9.3: Temporal lane (ADR-51 scheduled messages)

As a platform developer,
I want time-derived violations to surface without polling,
So that freshness rules (e.g. "background check older than N") converge.

**Acceptance Criteria:**

**Given** a resolved entity with a freshness window
**When** the Actuator schedules an `@at(resolve+window)` message on the `AllowMsgSchedules` stream, subject keyed per-entity
**Then** at expiry NATS republishes to an internal `weaver.timer.fired.>` subject; the temporal lane submits a `MarkExpired` **op** via the Processor (never injected into `core-events`)
**And** CDC re-projects the target; the freshness gap flips violating
**And** re-doing the entity before expiry re-publishes to the same subject, **replacing** the prior timer
**And** the schedule is durable across a Weaver restart

*FRs: FR27 · Depends on: 9.1, 7.4 · Model: Opus · Grounding: Contract #10 §10.4, ADR-51, D4*

### Story 9.4: Weaver control-API / CLI (FR30)

As an operator,
I want to list, disable, and revoke convergence targets,
So that I can manage convergence without a console (Phase 2 has no UI).

**Acceptance Criteria:**

**Given** installed targets
**When** I invoke the Weaver control API / CLI (mirroring the Refractor control plane — NATS Services or equivalent)
**Then** I can `list` active targets, `disable` (pause) a target so Weaver stops acting on it, and `revoke` it
**And** disabling a target halts its convergence without deleting its Lens definition
**And** the control surface is operator-facing only (no console dependency)

*FRs: FR30 · Depends on: 9.1 · Model: Sonnet (well-bounded, mirrors existing control plane) · Grounding: arch Refractor control plane; FR30*

## Epic 10: External Convergence — Two-Phase Nudge

**Goal:** The platform calls external systems exactly once (idempotent), proven against mocked reference adapters.
**FRs covered:** FR58 (+ NFR-S11)

### Story 10.1: External Adapter framework + Two-Phase Nudge protocol

As a platform developer,
I want a claim→execute→resolve protocol around external calls,
So that a failed or retried external call cannot duplicate an action.

**Acceptance Criteria:**

**Given** the External Adapter framework (`internal/weaver/nudge/`) and a `FakeBackgroundCheck` reference adapter
**When** Weaver performs an external action
**Then** it writes a claim to `weaver.claims.<claim-id>` **before** the call (NFR-S11), executes the (mocked) call, then submits a **resolve op** through the Processor carrying `claim-id`
**And** the claim prevents any other instance re-initiating the same operation
**And** claims are retained (default 90d); audit can join the resolve op (Core KV) to the claim (intent)

*FRs: FR58, NFR-S11 · Depends on: 9.1 · Model: Opus · Grounding: arch Item 3; `docs/components/weaver.md` Two-Phase Nudge*

### Story 10.2: Nudge wired into the Actuator + idempotency proof

As a platform developer,
I want the Strategist's external playbooks to drive nudges, with a retry-safety test,
So that the idempotency guarantee is proven end-to-end.

**Acceptance Criteria:**

**Given** a playbook whose action is an external nudge and a `FakeStripe` reference adapter
**When** the Actuator executes it
**Then** the nudge follows the claim→execute→resolve path
**And** an **idempotency test** drives a failed/retried external call and asserts **no duplicate** action/charge (claim is the idempotency boundary)
**And** a crash between claim and resolve leaves the claim visible and the operation not re-initiated until reconciled

*FRs: FR58 · Depends on: 10.1 · Model: Opus · Grounding: arch Item 3; FR58*

## Epic 11: Loftspace Reference Vertical

> **Sequencing: runs AFTER Epic 12.** The reference vertical is authored on the post-12 projection plane (sound under retry/reorder; packages own their own grant projections). See the execution-order note in the Phase 2 Story Total section.

**Goal:** An installable `lease-signing` package converges a Lease Application end-to-end — the dogfood test that the package model carries orchestration content. Thin: engines are fixture-proven.
**FRs covered:** integration (FR26, FR27, FR29, FR30, FR58)

### Story 11.1: `lease-signing` package authoring

As a vertical author,
I want the Loftspace lease-application shipped as an installable package,
So that the orchestration engines run a real convergence scenario from package data alone.

**Acceptance Criteria:**

**Given** the `lease-signing` package (`packages/lease-signing/{manifest,ddls,lenses,permissions}.go` + adapter config)
**When** installed via `InstallPackage`
**Then** it provides: a lease-application DDL; a **target Lens cypher** projecting "Lease Application complete" (row-per-candidate + `violating` + gap columns incl. a recent-background-check freshness predicate); **playbooks** mapping each gap → action (Loom onboarding / Two-Phase Nudge bgcheck / Two-Phase Nudge or Loom payment / sign task); **Loom pattern(s)** (onboarding/verify-info); and `FakeStripe`/`FakeBackgroundCheck` config
**And** no engine code changes are required to run it (engines stay generic)

*FRs: integration · Depends on: 8.x, 9.x, 10.x · Model: Opus (package authoring across DDL/Lens/Starlark) · Grounding: charter demo decomposition; `docs/components/_packages.md`*

### Story 11.2: Convergence-harness end-to-end

As the platform team,
I want an end-to-end test that drives a lease application to steady state,
So that Loom + Weaver + Two-Phase Nudge + temporal are proven to converge.

**Acceptance Criteria:**

**Given** a freshly created lease-application with all gaps violating
**When** the orchestration runs (Weaver detects gaps → triggers Loom onboarding + nudges bgcheck/payment → temporal handles freshness → sign task)
**Then** a **drain-then-assert** harness observes the target row's `violating` flip `false` and **remain** false (steady state), within a bounded window
**And** the background-check freshness predicate is exercised via a short ADR-51 window
**And** a retried external call does not double-act (idempotency holds end-to-end)
**And** the demo runs from `InstallPackage` of `lease-signing` on an otherwise minimal core

*FRs: integration (FR26, FR27, FR29, FR30, FR58) · Depends on: 11.1 · Model: Opus (e2e harness) · Grounding: charter; Quinn's drain-then-assert pattern (cf. M5)*

## Epic 12: Projection-Plane Integrity & Capability Decomposition

> **Sequencing: runs BEFORE Epic 11** (Andrew, 2026-06-07) even though it is numbered later — see the execution-order note in the Phase 2 Story Total section. Epic numbers ≠ execution order.

> **Source:** Winston architecture session against `_bmad-output/planning-artifacts/refractor-lens-decomposition-brief.md` (2026-06-07). The brief's four decisions are adjudicated here: **D-INTEGRITY** (12.1), **D-PIPELINE** (12.2–12.4), **D-CONSUMER** (12.5), **D-PROJECTION** (12.6–12.7). Decision record + rationale: [docs/decisions/projection-plane-decomposition.md](../../../docs/decisions/projection-plane-decomposition.md). Proposed contract amendments: `cmd/refractor/CONTRACT-AMENDMENT-REQUEST.md` (Requests 4–7) and `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` (D-CONSUMER).

**Goal:** Make the per-actor projection plane (1) **sound** under retry/reorder so a revoked grant can never be resurrected on the security plane, (2) **authorable by packages without core edits**, then (3) **decompose** the bootstrap `capability` god-cypher into package-owned grant projections with a generic consumer-side dispatcher — so "packages own the grant types they own" is true on **both** the write (projection) and read (auth) sides, while preserving the single-GET auth hot path.

**FRs covered:** (architecture hardening — no new FR; protects NFR-S2 single-authorization-surface, the Contract #6 O(1) auth promise, and the minimal-core/everything-is-a-package principle)

**Why this epic exists.** Story 7.2's review surfaced that a *package* lens cannot be added without editing **core** (a new `case` in `cmd/refractor/main.go` + a wrapper in `internal/refractor/capabilityenv/`) — the inverse of the package-layering rule. The same review surfaced a confirmed security-plane resurrection window (below). The brief's existing god-cypher open item assumed "each package projects the grant types it owns," but packages currently *can't* — so the projection plumbing must be made data-driven first.

**Adjudicated positions (deviations from the brief flagged):**
- **D-INTEGRITY is a confirmed-reachable security bug, not theoretical.** The pipeline retry queue captures a *row* (`enqueueRetry` → `capturedResult`), not a re-evaluation, and replays it via `a.Upsert(capturedResult.Keys, capturedResult.Row)` (`internal/refractor/pipeline/pipeline.go:944-949`). So: an "open-era" ephemeral-grant Upsert that fails transiently is captured; a later close-event Delete succeeds; the retried capture lands **after** the delete and re-writes the revoked grant — and no further CDC event re-deletes it (the task is already closed). On `capabilityEphemeral` this is a revoked-grant resurrection on the **security plane**.
- **Ordering-guard mechanism — Winston overrides the brief's lean.** The brief proposed using `projectedFromRevisions` (the auth-coherence vector) as the ordering guard. Two problems make it unfit *as the guard*: (a) `projectedAt` is derived from the **anchor (actor) vertex** provenance, and the actor vertex is unchanged when a *task* closes, so open-era and close-era projections of the same actor are indistinguishable by `projectedAt`; (b) the current `projectedFromRevisions` only stamps the actor + lens-def revisions — it doesn't capture the task/link sources at all, and even a fixed version faces the brief's own open question (multi-source vector dominance when the source *set* shrinks on close). **Winston's mechanism: a monotonic `projectionSeq` = the JetStream stream sequence of the triggering CDC message.** It is globally totally-ordered by the substrate, plan-independent, deterministic-replay-safe (rebuild replays in stream order → highest-seq write wins → identical steady state), and sidesteps the multi-source dominance question entirely. `projectedFromRevisions` stays as the coherence/debug datum and is *separately* widened (by D-PIPELINE's compiled plan) to cover the full source set — two concerns, two data.
- **D-PIPELINE is tractable because the machinery already exists on both sides.** The **simple** engine already compiles reverse-traversal invalidation (`simple.reverseTraverse`/`walkBackToAnchor` over `QueryPlan.Steps` — the Materializer pattern). The **full** engine has a clean, ANTLR-free AST (`full/ast.go`: MATCH patterns, directions, hop bounds). The invalidation compiler walks the full AST into a `simple.TraversalStep`-shaped plan and reuses the existing reverse-traversal to find affected anchors — replacing the broad `ActorEnumerator` BFS for full-engine actor-aggregate lenses.
- **The per-name switch fully reduces to declarative aspects.** All four wrappers (`capability`, `capabilityRoleIndex`, `capabilityEphemeral`, `myTasks`) differ only in: output-key pattern, which RETURN columns form the body, freshness stamping, and empty→delete-vs-skip. Even the `realEphemeralGrants`/`realOpenTasks` "drop degenerate `{taskKey:null}` collect artifacts" logic generalizes to a declarative `realnessFilter`. So `projectionKind: actorAggregate` + a small descriptor + the compiled plan = the whole behavior, no Go.
- **D-PROJECTION multiplies the step-3 read fan-out — and D-CONSUMER is what keeps it O(1).** Moving role/permission and service-access to disjoint keys means step-3 no longer finds them in one `cap.<actor>` doc. The brief under-states this. The resolution: step-3 **already** path-dispatches (task/service/platform) *before* the read (the 7.1 ephemeral pattern). So each path reads exactly **one** path-specific disjoint key — the single-GET hot path is *preserved*, not lost. D-CONSUMER's generic dispatcher is therefore not symmetric-nicety; it's the mechanism that makes decomposition free on the read side. D-PROJECTION and D-CONSUMER land **together per grant-type** so the read and write sides never drift.

> **Party-review applied (2026-06-07).** The multi-agent review (Bob/Amelia/Quinn/Sally/John) split the original 12.1 into **12.1a** (the guard) + **12.1b** (rebuild reconciliation), and threaded 11 other corrections through the epic. The pre-review version had 7 stories; this is the post-review 8-story shape. Findings ledger: `docs/decisions/projection-plane-decomposition.md` § "Party review".

### Story 12.1a: Monotonic projection-write guard — security/correctness plane (D-INTEGRITY)

> **Independently shippable and sequenced first** — no dependency on the rest of the epic; supersedes background task `task_3d57a524`. Scope is **NATS-KV, the two at-risk lenses only** (`capabilityEphemeral`, `my-tasks`); the primary `capability` lens + rebuild interaction are 12.1b.

As the platform security owner,
I want every guarded per-actor projection write to be rejected if a newer projection for that key already landed,
So that a retried or reordered stale projection can never resurrect a revoked ephemeral grant (security plane) or a closed task (correctness plane).

**Acceptance Criteria:**

**Given** the confirmed resurrection window — the retry queue runs on its **own goroutine** (`failure/retry.go:102`) and replays a **captured `EvalResult`** (`pipeline.go:929-950`, `a.Upsert(capturedResult.Keys, capturedResult.Row)`), so a captured "open-era" `Upsert` can land after a successful close-`Delete` and re-write a revoked grant
**When** a guarded projection is evaluated
**Then** **`projectionSeq` is plumbed end-to-end**: `processMsg` captures `msg.Metadata().Sequence.Stream`, threads it into `simple.EvalResult` (a new field, so the retry-queue capture carries it), and the pipeline passes it to the adapter — for **both** the fan-out path and the inline path
**And** the **`adapter.Adapter` interface is extended** to carry the ordering token on writes (e.g. `Upsert`/`Delete` gain a `projectionSeq uint64` param or an `EvalResult`-shaped arg) — `Delete`'s nil-row case is handled because the token is **not** carried in the row body; the **Postgres adapter is explicitly OUT of scope** (it implements the new signature as a pass-through/no-guard — documented), only `NatsKVAdapter` enforces the guard
**And** the NATS-KV adapter writes **conditionally via CAS, not read-then-write**: `Get` (current rev + stored `projectionSeq`) → drop as idempotent no-op when `incoming ≤ stored` → else `Update` with `ExpectedRevision`; on a revision-conflict error **re-read and re-compare in a bounded loop** (the concurrent retry-goroutine writer makes this load-bearing, not theoretical)
**And** a **`Delete` on a guarded key becomes a soft tombstone** `{isDeleted:true, projectionSeq:<seq>}` so the high-water mark survives physical absence; step-3 already denies on both absent key and tombstone (no grants → no match), so auth semantics are unchanged (Contract #6 §6.8)
**And** the **`my-tasks` E2E assertion is flipped**: `refractor_mytasks_e2e_test.go:226` ("closed task must vanish from my-tasks") currently asserts the key is **absent** after close — with soft-tombstone it asserts the key is a **tombstone** (`isDeleted:true`); the `requireQuiescentRevision` settle-wait (`:253`) is **removed** (the masked race is now structurally closed)
**And** a **fail-without/pass-with adversarial regression test** reproduces the exact captured-retry resurrection (enqueue an open-era `Upsert`, commit a close-`Delete`, fire the retry) and asserts the stale replay is **rejected** — the test must FAIL against `main` (no guard) and PASS with the guard; it lands in the **Gate 3 (DEFENDED)** adversarial suite, not a lone unit test
**And** the change is raised as a Contract #6 §6.2/§6.3/§6.8 amendment (`cmd/refractor/CONTRACT-AMENDMENT-REQUEST.md` Request 4) — `projectionSeq` field, interface change, CAS, soft-tombstone-carries-watermark
**And** the **my-tasks shape amendment** (Contract #10 §10.1) records that consumers MUST skip `isDeleted` tombstones — a forward obligation for any UI/query reader of the `my-tasks` bucket (no production reader exists yet; only the E2E)
**And** Gate 2 (BLOCKED) + Gate 3 (DEFENDED) pass

*FRs: (security/correctness hardening; protects NFR-S2) · Depends on: nothing (ships first) · Model: Opus (security plane, concurrency) · Grounding: brief D-INTEGRITY; `adapter/{adapter,natskv}.go`, `pipeline/pipeline.go:929-950`, `failure/retry.go:102`, `refractor_mytasks_e2e_test.go:226`; Contract #6 §6.2/§6.3/§6.8*

### Story 12.1b: Guard ↔ rebuild reconciliation + primary capability lens (D-INTEGRITY pt.2)

> Follows 12.1a. Closes the operational footgun the guard introduces for `Rebuild`, and extends the guard to the primary `capability` lens.

As the platform operator,
I want a rebuild to correctly restore guarded projections despite the monotonic write-guard,
So that the integrity guard never silently prevents a rebuild from rewriting live state.

**Acceptance Criteria:**

**Given** the guard from 12.1a and the existing `Pipeline.Rebuild(ctx, truncate)` (`pipeline.go:437`) — a `Rebuild(truncate=false)` replays **historical** events carrying their **original, lower** stream seqs, which the guard would reject against a bucket still holding live high-seq watermarks (rebuild silently restores nothing)
**When** a guarded-bucket rebuild runs
**Then** the conflict is resolved by an explicit, tested rule — **either** guarded buckets force `truncate=true` (watermarks cleared with the data, first replay write wins) **or** rebuild bypasses the guard for the duration of the replay (documented bypass flag) — the story picks one and ACs it; a `Rebuild(truncate=false)` on a guarded bucket must **either** be rejected with a clear error **or** correctly restore every key
**And** a test drives a rebuild of `capabilityEphemeral`/`my-tasks` after live traffic and asserts the post-rebuild projection equals a from-scratch projection (no rejected-write holes)
**And** the guard is **extended to the primary `capability` lens** (the fan-out aggregate over identity/roles/services) with the same CAS+tombstone mechanism
**And** **`capabilityRoleIndex` is explicitly excluded** from this guard family — it is keyed by `operationType`, not actor (`envelope.go:344`); it is an operation-aggregate, not an actor-aggregate, and its resurrection profile differs (decided here, not hand-waved as "for consistency")
**And** Gate 2 + Gate 3 pass

*FRs: (security/correctness hardening) · Depends on: 12.1a · Model: Opus (security plane + rebuild semantics) · Grounding: `pipeline/pipeline.go:437`, `adapter/adapter.go` (Truncater); Contract #6 §6.2*

### Story 12.2: Invalidation-compiler spike (D-PIPELINE spike)

> **Spike — non-shipping.** Output is a decision report + a passing equivalence test, not production wiring. De-risks 12.3.

As the architect,
I want to prove a narrow invalidation compiler can derive the affected-anchor set from the full openCypher AST,
So that 12.3 is built on a validated approach rather than a speculative one.

**Acceptance Criteria:**

**Given** the full-engine ASTs for `myTasks` and `capabilityEphemeral` and the existing `simple.reverseTraverse`/`walkBackToAnchor` machinery
**When** the spike compiles each AST's MATCH/OPTIONAL-MATCH patterns into a `simple.TraversalStep`-shaped step list (anchor → leaf, with direction + hop bounds) and runs reverse-traversal from a changed non-anchor vertex / link / aspect
**Then** the **correctness oracle** holds on a fixture graph, for vertex, link, and aspect CDC events: the compiled affected-anchor set is a **subset of** the broad `ActorEnumerator` BFS set (the compiled plan is *precise*, the BFS *over-reprojects* — they are deliberately **not** equal) **and** it **contains every actor whose projection output actually changes** (verified by reprojecting the BFS superset and diffing — no missed anchor). The win is recorded as the BFS-minus-compiled count (the over-reprojection the plan eliminates)
**And** the spike report enumerates exactly which openCypher constructs the narrow compiler covers (`MATCH`/`OPTIONAL MATCH`, labels, rel names/directions, variable-length hops within the existing cap, conservative `WHERE`, simple path-preserving `WITH`) and which it does **not**, with the fallback policy: **non-security** projections may fall back to broad BFS; **auth-plane actor-aggregate** lenses must compile or **fail activation closed**
**And** a go/no-go recommendation for 12.3 is recorded

*FRs: (enabler) · Depends on: nothing (informs 12.3) · Model: Opus (compiler design) · Grounding: brief D-PIPELINE "Spike"; `ruleengine/full/ast.go`, `ruleengine/simple/{plan,evaluator}.go`*

### Story 12.3: Projection plan compiler + `projectionKind: actorAggregate` marker (D-PIPELINE core)

As a platform developer,
I want Refractor to compile an actor-aggregate lens into a projection + invalidation plan from declarative contract data,
So that the per-actor projection behavior is data, not core Go keyed on a lens name.

**Acceptance Criteria:**

**Given** the spike's validated approach (12.2)
**When** a lens definition declares `projectionKind: "actorAggregate"` (a new `meta.lens` aspect, Contract #6 §6.13)
**Then** Refractor compiles a `ProjectionPlan{Execution, Invalidation, Output}`: **Execution** = evaluate the lens for a bound `$actorKey` (already present); **Invalidation** = the compiled reverse-traversal plan (12.2) that derives affected anchors from a changed vertex/link/aspect, replacing the broad BFS for these lenses; **Output** = the declarative descriptor below
**And** the **Output descriptor** is read from the lens definition aspects, covering every behavior the four Go wrappers encode: `anchorType` (or inferred from `MATCH (x:identity {key:$actorKey})`), `outputKeyPattern` (constrained, e.g. `cap.ephemeral.{actorSuffix}`), `bodyColumns` (which RETURN aliases form the doc body), `emptyBehavior` (`delete` | `softDelete` | `emptyDoc` | `skip`), `realnessFilter` (drop degenerate collect artifacts by a non-empty key field — generalizes `realEphemeralGrants`/`realOpenTasks`), and `freshness: auto` (stamp `projectionSeq` per 12.1 + the widened `projectedFromRevisions`)
**And** `projectedFromRevisions` is widened to cover the **contributing source set the plan read** (actor + tasks/roles/links). **Scope decision (party review):** v1 covers sources that *contributed* a binding; covering sources that were *read-then-excluded* (e.g. a now-closed task) requires the full executor to report every Core-KV key it touched-then-dropped (executor instrumentation) — that is called out as **either in-scope for this story or deferred to a 12.3-follow-up**, and the AC must state which. Either way this is the coherence/debug datum, **not** the ordering guard (which is `projectionSeq`, 12.1a) (Contract #6 §6.3 amendment, Request 6)
**And** an actor-aggregate lens whose MATCH uses an unsupported construct **fails activation** when it is an auth-plane lens (fail closed) and logs a fallback-to-BFS warning when it is not
**And** the descriptor's `emptyBehavior: softDelete` reuses the **same tombstone mechanism** as 12.1a's guard (one mechanism, not two)
**And** no behavior changes yet for the built-in lenses (this story adds the machinery; 12.4 migrates onto it) — the existing switch still drives them

*FRs: (enabler) · Depends on: 12.2 · Model: Opus (compiler + contract) · Grounding: brief D-PIPELINE option 1; Contract #6 §6.13/§6.3; `cmd/refractor/main.go:256-313`*

### Story 12.4: Migrate built-in lenses off the `CanonicalName` switch (D-PIPELINE landing)

As a platform developer,
I want the four built-in capability lenses re-expressed as declarative `actorAggregate` lenses and the per-name switch deleted,
So that a package can ship a per-actor aggregating lens with **zero** core edits — proving the layering inversion is gone.

**Acceptance Criteria:**

**Given** the plan compiler (12.3) and the integrity guard (12.1a/12.1b)
**When** the **three actor-aggregate** lenses — `capability`, `capabilityEphemeral`, `myTasks` — are re-declared with `projectionKind: actorAggregate` + the Output descriptor (12.3); **`capabilityRoleIndex` is handled separately** — it is an operation-aggregate (keyed by `operationType`), so it either gets its own `projectionKind` (e.g. `operationAggregate`) or remains a small bespoke path, **stated explicitly in the AC** (no silent "fourth actor-aggregate")
**Then** the per-`CanonicalName` `switch` in `cmd/refractor/main.go` and the bespoke wrappers in `internal/refractor/capabilityenv/` are **deleted** (envelope/fan-out/delete-key now flow from the compiled plan)
**And** the change is **behavior-preserving in outcome**: the Contract #6 §6.2/§6.6 conformance tests, the Capability-Lens 4-attack-vector bypass suite, and the `my-tasks` E2E all pass — **test fixtures/oracles may change** where the declarative descriptor replaces wrapper internals, but the asserted outcomes hold
**And** a **proof test installs a brand-new actor-aggregate package lens** (a throwaway fixture lens) via `InstallPackage` and observes it project + invalidate correctly **with no change to any file under `cmd/` or `internal/refractor/capabilityenv/`** — the acceptance gate for "packages can do this now"
**And** Gate 2 + Gate 3 pass (the `capability`/`capabilityEphemeral` paths are security-critical)

*FRs: (enabler; minimal-core principle) · Depends on: 12.3, 12.1a, 12.1b · Model: Opus (security-critical migration) · Grounding: brief §1, §2 origin note; `cmd/refractor/main.go`, `capabilityenv/envelope.go`*

### Story 12.5: Generic step-3 auth-hook dispatcher (D-CONSUMER)

> **Security-critical** — the auth hot path. Full 3-layer + Gate 2/3.

As the platform security owner,
I want step-3 to dispatch over a registry of grant-matchers configured by data instead of a hardcoded `switch`,
So that the *consumer* side stops naming each grant type, symmetric to the data-driven projection side.

> **Party-review pin (the gap that would have cost a whole story).** "Packages register a matcher" is **NOT** package-shipped Go/plugin code — Lattice packages are **data** (cypher, Starlark, manifest). The model is a **fixed set of core-provided matcher *kinds*** (the existing ephemeral / service-scoped / platform-scope logics live in core), and a package **declares, as install-time data, which matcher kind authorizes its grant type and which disjoint Capability-KV key holds it** (+ the field mapping). Core owns the matcher *implementations*; data owns the *wiring*. This keeps Lattice data-only and bounds the effort.

**Acceptance Criteria:**

**Given** step-3's current hardcoded dispatch (`taskSet → matchEphemeralGrant`, `serviceSet → matchServiceAccess`, default → `matchPlatformPermission` — `step3_auth_capability.go:142-282`)
**When** step-3 is refactored into a **generic dispatcher** over a registry whose entries are **data**: each entry binds an authContext path → a **core matcher kind** → the **disjoint Capability-KV key** that path reads (+ field mapping)
**Then** the dispatch table is **data**, not a `switch` naming `task`/`service`; the three existing logics become the seed **core matcher kinds**, re-expressed with **identical** behavior
**And** the **single-GET hot path is preserved by the one-key-per-path invariant**: path selection happens **before** the read (as today), each path maps to **exactly one** disjoint key, so exactly one GET per Authorize call. **Two packages contributing the same path is a config error** (or requires upstream merge) — the dispatcher never fans a single path into N reads. The denial-path `actorRoles` second read stays off the hot path
**And** the bypass suite and §6.4–§6.8 dispatch tests pass — **fixtures migrate** where the registry replaces the `switch`, asserted outcomes hold
**And** the change is raised as a Contract #6 / Contract #2 §2.8 amendment (`cmd/processor/CONTRACT-AMENDMENT-REQUEST.md`) describing the fixed-matcher-kind registry + one-key-per-path read model
**And** Gate 2 (BLOCKED) + Gate 3 (DEFENDED) pass

*FRs: (security architecture) · Depends on: 7.1 (existing dispatch); **hard prerequisite of 12.6** · Model: Opus (auth hot path) · Grounding: brief D-CONSUMER; lattice-architecture.md god-cypher open item "Consumer side"; `step3_auth_capability.go:142-282`*

### Story 12.6: Decompose god-cypher — rbac-domain role/permission projection (D-PROJECTION pt.1)

> **Security-critical.** First god-cypher decomposition after the keystone is in place.

As a platform developer,
I want `rbac-domain` to own its role/permission grant projection as a package lens,
So that core stops referencing the rbac grant vocabulary and the bootstrap cypher shrinks.

**Acceptance Criteria:**

**Given** the plan compiler (12.4) and the generic dispatcher (12.5)
**When** `rbac-domain` ships a `capabilityRoles` actor-aggregate lens projecting role-derived `platformPermissions` (+ `roles`) to a **disjoint key** (e.g. `cap.roles.<actor>`) and **registers its auth-hook** (12.5) in the same story
**Then** the bootstrap `capability` cypher **drops** its `holdsRole`/`grantedBy`/`role`/`permission` MATCHes (core no longer references the rbac package types — dependency direction flips package→core, mirroring the 7.1 `task` extraction)
**And** the platform-path step-3 read targets `cap.roles.<actor>` via the registered hook; the FR22 denial `actorRoles` source moves with it
**And** **primordial-vs-package composition is defined (party review):** the primordial/bootstrap identity has root-equivalent platform grants that core projects even when `rbac-domain` is absent. The story ACs how a core-projected primordial grant and an `rbac-domain`-projected `cap.roles.<actor>` grant **compose on the platform read path without collision** — the lean: the dispatcher reads exactly one key by actor class (primordial → core-owned key; ordinary → `cap.roles.<actor>`), preserving one-key-per-path (12.5)
**And** **`capabilityRoleIndex` ownership is resolved**: it moves to / is owned by `rbac-domain` consistently — and the AC states the **degradation** when rbac-domain is absent (FR22 `rolesCarryingPermission` is empty), so it's a chosen behavior, not a surprise
**And** behavior is preserved: the bypass suite (role-manipulation attack vector), §6.10 role-specialization behavior, and the §6.2 conformance test pass — **fixtures migrate** to the disjoint key (asserted outcomes hold)
**And** Gate 2 + Gate 3 pass

*FRs: (security architecture) · Depends on: 12.4, 12.5 · Model: Opus (security-critical) · Grounding: brief D-PROJECTION; Contract #6 §6.1 contract-contribution; `internal/bootstrap/lenses.go:41`*

### Story 12.7: Retire the god-cypher's service/location remnants (D-PROJECTION pt.2)

> **Security-critical. Two-path story (Andrew, 2026-06-07).** The `service-location` package **does
> not exist today** and may not exist when 12.7 runs (no `packages/service-location/` — only a
> concept write-up: `packages/service-location/CONCEPT.md`). So 12.7 is not "make a package ship a
> lens"; it is **"get the service/location grant vocabulary out of core,"** which has two acceptable
> endings depending on what's landed:
> - **Path A — package exists:** implement more-or-less as originally stated — `service-location`
>   ships `capabilityServiceAccess` (actor-aggregate, §6.10 behaviors intact) to a disjoint key and
>   registers its auth-hook; the god-cypher drops the service/location MATCHes.
> - **Path B — package absent (default):** **just delete the god-cypher's service/location remnants**
>   and let the future service package own its projection **when it lands** (the 12.3/12.4/12.5
>   machinery makes that a pure package addition — no core edit). Core ends owning only the bucket +
>   key conventions + the dispatcher. Do **not** build a placeholder package to satisfy symmetry.

As a platform developer,
I want the bootstrap `capability` god-cypher's service/location grant vocabulary removed from core,
So that core stops referencing service/location types regardless of whether a service package exists yet.

**Acceptance Criteria:**

**Given** the rbac decomposition (12.6) — and a check of whether `packages/service-location/` exists at story time
**When** Path A applies (package exists): it ships a `capabilityServiceAccess` actor-aggregate lens projecting `serviceAccess` (with the §6.10 multi-level containment-exclusion + transitive-availability + operation-override behaviors intact) to a disjoint key and registers its auth-hook **per `packages/service-location/CONCEPT.md`**
**Or** Path B applies (package absent): the bootstrap cypher's service/location MATCHes are simply **deleted**, with no replacement projection authored — the service-path step-3 matcher kind + disjoint key remain registered-but-unpopulated (absence = denial, Contract #6 §6.8) until a real service package projects into it
**Then** in **both** paths the bootstrap `capability` cypher **drops** its `containedIn`/`availableAt`/`unavailableAt`/`permitsOperation` MATCHes and shrinks to only the **primordial-identity anchor** (or retires entirely, leaving core owning just the bucket + key conventions + the step-3 dispatcher) — **core no longer references service/location types**
**And** the bypass suite's service-access oracle is reconciled to the chosen path: Path A migrates fixtures to the disjoint key (outcomes hold); Path B asserts a service op now denies with the no-entry path until a service package lands (the `Hello Lattice` / §6.10 service fixtures move with whatever owns service projection — documented, not silently dropped)
**And** the brief's god-cypher open item in `lattice-architecture.md` is marked resolved (proposed to the planning lead) and Contract #6 §6.1 records the completed contract-contribution decomposition (amendment)
**And** Gate 2 + Gate 3 pass

*FRs: (security architecture) · Depends on: 12.6 · Model: Opus (security-critical) · Grounding: brief D-PROJECTION; Contract #6 §6.10; `internal/bootstrap/lenses.go:46-48`; `packages/service-location/CONCEPT.md`*

## Phase 2 Story Total

| Epic | Stories | Notes |
|---|---|---|
| Epic 7: Orchestration Foundations | 5 (7.5 won't-do) | task model + service actors + platform-wide schedule stream + substrate durable-consumer (FR29 creation-time already satisfied by 7.1/7.2 — 7.5 closed) |
| Epic 8: Loom — Deterministic Flow Engine | 5 | skeleton → user-tasks → guards → hardening (backoff, consumer teardown) |
| Epic 9: Weaver — Convergence Engine | 4 | target-as-Lens+lane1 → anti-storm → temporal → control-API (FR30) |
| Epic 10: External Convergence — Two-Phase Nudge | 2 | framework + idempotency proof |
| Epic 11: Loftspace Reference Vertical | 2 | package authoring + e2e convergence harness |
| Epic 12: Projection-Plane Integrity & Capability Decomposition | 8 | D-INTEGRITY guard 12.1a + rebuild reconciliation 12.1b (ship first) → invalidation compiler spike+build+migration (12.2–12.4) → generic auth-hook consumer (12.5) → god-cypher decomposition rbac (12.6) → retire service/location remnants (12.7, two-path: implement if `service-location` exists, else just delete the remnants); architecture hardening from the refractor-lens-decomposition brief, party-reviewed 2026-06-07. **Lands BEFORE Epic 11** (see ordering note). |
| **Total** | **25 stories** | Phase 2 FRs FR26, FR27, FR29, FR30, FR58 covered; Epic 12 = NFR-S2 / minimal-core hardening; **execution order: Epics 7–10 → 12 → 11** |

> **⚠️ Execution order (Andrew, 2026-06-07): Epics 7–10 → 12 → 11.** Epic 12 (projection-plane
> integrity + capability decomposition) **lands before Epic 11** (Loftspace reference vertical). Two
> reasons: (1) Epic 11's convergence e2e leans on tasks / ephemeral grants / `my-tasks`
> vanish-on-close as *correctness guarantees* — exactly what the 12.1a/b D-INTEGRITY guard makes sound
> (Epic 11 built first would inherit the masked resurrection race + the settle-wait crutch); (2)
> authoring `lease-signing` on the *decomposed* projection model (12.3–12.5) lets the reference package
> own its own grant projections cleanly via the contract-contribution path, instead of being written
> against the god-cypher and migrated later. Epic numbers are not execution order here.

> **✅ Implementation gate (satisfied 2026-06-02):** the dedicated Loom/Weaver data-contracts session is **complete** — `docs/contracts/10-orchestration-surfaces.md` §10.1–§10.8 are **FROZEN** (guard grammar + subject/state-access, the `loomPattern` schema, target-Lens output, operational-KV shapes, scheduling subjects, Weaver target+playbook, and the cross-package cap-lens layering via the **(a1)** extraction; the narrow post-hoc-orphan referential case is deferred — no-orphan-**by-construction** in Story 7.5 stands). Per-story briefs are authored against the frozen contracts; **implementation may proceed — CS→DS→CR, Epic 7 first.**

**Deferred to Phase 3:** FR28 (role-queue + fallback), Weaver lane-2 (targeted-audit), L3 evaluator, full temporal scheduler / op-vertex pruner, Refractor negative-retraction projection, real external adapters, read-path auth (D1 rubric written).
