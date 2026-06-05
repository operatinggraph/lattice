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

### Story 8.1: Loom walking skeleton

As a platform developer,
I want the Loom engine to drive a fixture pattern of system-op steps to completion,
So that the core interpreter loop is proven before user-tasks and guards.

**Acceptance Criteria:**

**Given** a `meta.loomPattern` of system-op steps (no guards) installed as package/fixture data
**When** an op triggers a Loom instance (`internal/loom`, `identity:loom` actor)
**Then** the engine loads the pattern (CDC), reconciles a per-domain durable consumer from declared bindings (D2), and runs `event → advance cursor → submit next op → event` to completion
**And** the instance cursor is stored in `loom-state` (not Core KV)
**And** on engine restart the durable consumer resumes; the run completes exactly once (idempotent op submission)
**And** `loom` imports only `substrate/*` (no Go import of Processor/Weaver)

*FRs: FR26 · Depends on: 7.1, 7.6 (substrate durable-consumer), 1.5.10 (outbox) · Model: Opus (engine, multi-file) · Grounding: `docs/components/loom.md`, D2/D3*

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

**Given** a step with a guard predicate over current state
**When** Loom evaluates the step and the guard is false (data already present)
**Then** the step is skipped (no task created), cursor advances; guard true → step runs
**And** guards are **pure, deterministic** predicates (no side effects, no external reads) — verified by the engine contract
**And** with `loom-state` discarded, the engine **rebuilds the cursor by replaying guards** over Core KV tasks (first step whose guard is true and whose task is incomplete) — a crash-recovery test asserts identical resumption
**And** no branches/loops/fan-out are expressible (linear only)

*FRs: FR26 · Depends on: 8.2 · Model: Opus · Grounding: D3 guard purity; Contract #10 §10.5*

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

## Phase 2 Story Total

| Epic | Stories | Notes |
|---|---|---|
| Epic 7: Orchestration Foundations | 5 (7.5 won't-do) | task model + service actors + platform-wide schedule stream + substrate durable-consumer (FR29 creation-time already satisfied by 7.1/7.2 — 7.5 closed) |
| Epic 8: Loom — Deterministic Flow Engine | 3 | skeleton → user-tasks → guards |
| Epic 9: Weaver — Convergence Engine | 4 | target-as-Lens+lane1 → anti-storm → temporal → control-API (FR30) |
| Epic 10: External Convergence — Two-Phase Nudge | 2 | framework + idempotency proof |
| Epic 11: Loftspace Reference Vertical | 2 | package authoring + e2e convergence harness |
| **Total** | **17 stories** | Phase 2 FRs FR26, FR27, FR29, FR30, FR58 covered |

> **✅ Implementation gate (satisfied 2026-06-02):** the dedicated Loom/Weaver data-contracts session is **complete** — `docs/contracts/10-orchestration-surfaces.md` §10.1–§10.8 are **FROZEN** (guard grammar + subject/state-access, the `loomPattern` schema, target-Lens output, operational-KV shapes, scheduling subjects, Weaver target+playbook, and the cross-package cap-lens layering via the **(a1)** extraction; the narrow post-hoc-orphan referential case is deferred — no-orphan-**by-construction** in Story 7.5 stands). Per-story briefs are authored against the frozen contracts; **implementation may proceed — CS→DS→CR, Epic 7 first.**

**Deferred to Phase 3:** FR28 (role-queue + fallback), Weaver lane-2 (targeted-audit), L3 evaluator, full temporal scheduler / op-vertex pruner, Refractor negative-retraction projection, real external adapters, read-path auth (D1 rubric written).
