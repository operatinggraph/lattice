# Weaver

**Component reference** | Audience: implementers + architects

> Decisions of record live in `_bmad-output/planning-artifacts/lattice-architecture.md` →
> "Phase 2 Architecture — Orchestration Core" (D3, D4). Data shapes are frozen in Contract #10
> (§10.2 target rows, §10.3 weaver-state, §10.4 scheduling, §10.8 target+playbook) — where this
> page and the contract diverge, the contract governs. Update this page in the same commit as the
> code; drift is a documentation bug.
>
> This page describes **what Weaver is**. A per-surface ledger of what is built vs. deferred lives
> in [Implementation status](#implementation-status) at the end.

External idempotent I/O (FR58 / NFR-S11) is **not** a Weaver concern: a target that needs an
external call remediates with **`triggerLoom`** of a Loom **`externalTask`** pattern, and the
**bridge** component (`docs/components/bridge.md`) executes the call idempotently. Weaver detects
and dispatches; it never reaches an external system itself.

The engine ships **zero domain knowledge** — targets and playbooks are package data; domain
literals appear only in tests and fixtures.

---

## Overview

Weaver is the **convergence engine** — it drives the graph toward declared **target states** by
detecting discrepancies and remediating them, optionally by triggering Loom utilities. It is
the declarative counterpart to Loom (brainstorming #122): **Weaver decides *what* is missing;
Loom executes a *fixed procedure* to fill a gap.** A "Lease Application complete" is a *target
state*, not a workflow.

Crucially, **a Weaver target is a Lens** — Weaver is a **consumer of the Refractor**, never its
own cypher runtime. The Refractor projects "currently-violating" rows; Weaver reacts.

Weaver is an **internal service actor** at root-equivalent capability. It **submits operations
through the Processor** (never writes Core KV directly). Its only direct write is to the
`weaver-state` bucket (dispatch/bookkeeping marks, Contract #10 §10.3).

---

## Pipeline

```
Sensorium (3-lane intake — an in-process multiplexer; no durable weaver-work bucket in Phase 2, §10.3)
   → Evaluator (L1 re-confirm + in-flight dedup; L2 hydrate + classify gap + select playbook)
   → Strategist (playbook registry: gap-type → action)
   → Actuator (OCC ops via Processor; trigger Loom via op; external I/O via triggerLoom of an externalTask)
```

Each Phase-2 lane replays from its own durable **source** (lane 1 from `weaver-targets`, lane 3
from `core-schedules`) with dedup in `weaver-state`, so a separate normalized `weaver-work` queue
would be redundant — it is deferred with lane 2 (§10.3).

### The 3 lanes

Each lane exists because the others structurally cannot see its violations:

| Lane | Trigger | Rationale |
|------|---------|-----------|
| **1. Violation-driven** | a row in a target Lens's output (CDC over the target KV) | the main path; Refractor keeps the target live |
| **2. Event-driven (targeted-audit)** | a `core-events` event → re-evaluate only the touched subgraph | for targets too costly to keep continuously projected |
| **3. Temporal** | a fired `@at` schedule on `core-schedules` (`schedule.weaver.timer.fired.>`, ADR-51 / §10.4) | time-derived violations emit no CDC (e.g. "bgcheck older than 90d") |

### Evaluator tiers

| Tier | Job | Status |
|------|-----|--------|
| **L1** | re-confirm row is still violating; drop if already in-flight (`weaver-state` mark) | ✅ |
| **L2** | hydrate context, classify the specific gap, select playbook input | ✅ |
| **L3** | AI-assisted reasoning for ambiguous/novel discrepancies | **deferred → Phase 3** |

### Strategist — playbook registry (package data)

A **playbook** maps gap-type → action. Engine is a generic dispatcher; playbooks ship in the
package (`lease-signing`):

| Gap (example) | Action |
|---------------|--------|
| `missing_onboarding` | `triggerLoom` of the onboarding pattern → **triggers Loom** (op-based, D3) |
| `missing_bgcheck` | `triggerLoom` of a background-check `externalTask` pattern (the bridge runs the call) |
| `missing_payment` | `triggerLoom` of a collect-payment `externalTask` pattern (the bridge runs the call) |
| `missing_signature` | `assignTask` — assign a sign **task** to the applicant |

### Actuator

- **OCC** — every op carries a revision-condition (substrate per-key revisions) so
  two ticks can't double-apply.
- **Triggers Loom via an op** — auditable, idempotent ledger entry (not a Go call; engines share
  only `substrate/*`).
- **External actions** — `triggerLoom` of a Loom `externalTask` pattern: the call is dispatched and
  executed idempotently by the **bridge** (`docs/components/bridge.md`), never by Weaver itself.

---

## Targets as Lenses (D4)

Targets project **one row per *candidate* entity with a `violating` boolean** (+ gap columns),
**not** row-only-when-violating — a gap closing flips the flag via a normal **upsert** (already
supported); only true entity deletion deletes a row (`IsDeleted`, already handled). This **avoids
forcing Refractor retraction work** in Phase 2.

The rows land in one shared, primordial **`weaver-targets`** NATS-KV bucket (the existing `nats_kv`
adapter), keyed `<targetId>.<entityId>` — the entity **NanoID**, never the dotted vertex key (the
full `vtx.<type>.<id>` rides the value's `entityKey`). Per the frozen §10.2 shape:

```
bucket: weaver-targets
key:    <targetId>.<entityId>
value:  { entityKey, violating, missing_onboarding, missing_bgcheck, missing_payment,
          missing_signature, <param columns, e.g. applicant>, freshUntil?, projectedAt }
```

Weaver does a **filtered `<targetId>.>` watch** per target it manages (lane 1) and **acts only on
`violating == true`**. A satisfied entity has `violating=false`; the row vanishes only on true
deletion. (True "emit-only-when-violating" + Refractor negative/filter-retraction projection is a
**deferred** scale-time capability.)

The freshness rule lives **in the target cypher**, not the engine: `missing_bgcheck = NOT
EXISTS(check WHERE date > now − window)`, and the cypher projects the next deadline as the optional
`freshUntil` column the temporal lane arms a timer from (below).

---

## Temporal lane — NATS scheduled messages (ADR-51, Contract #10 §10.4)

Time-derived violations emit no CDC, so Weaver converts **time into an op** using NATS native
message scheduling on the platform-wide **`core-schedules`** stream (`AllowMsgSchedules: true`,
subject root `schedule.>`, provisioned at bootstrap — not Weaver-owned):

```
Lens projects the deadline: row column freshUntil = resolve + window (RFC3339)
  → on each row delivery the Actuator publishes @at(freshUntil) on core-schedules,
       subject  schedule.weaver.timer.<targetId>.<entityId>
       header   Nats-Schedule-Target: schedule.weaver.timer.fired.<targetId>.<entityId>
  ... NATS holds it (durable across restart; re-publish to the same subject replaces) ...
  → at expiry the scheduler republishes the payload BACK INTO core-schedules at the fired subject
  → the weaver-temporal durable (filtered schedule.weaver.timer.fired.>) converts it to a
       MarkExpired op via the Processor (deterministic requestId: schedule subject + fire instant)
  → CDC + outbox event → target Lens re-projects → the freshness gap flips violating → lane-1 remediates
```

- **The freshness window lives in the target cypher, never the engine**: the Lens computes
  `resolve + window` and projects it as the engine-recognized **optional row column `freshUntil`**
  (RFC3339 string; a §10.2 free-form param column by carriage). The engine converts time→op only —
  a non-string/unparseable `freshUntil`, or one without an `entityKey`, surfaces a `RowDataError`
  Health issue and skips scheduling; a **past** `freshUntil` never schedules (any previously-armed
  firing is already durable in the stream; a Lens that only projects past deadlines is a package
  bug that surfaces as "violation never flips").
- **Level-driven scheduling, no edge detection**: every row delivery carrying a future
  `freshUntil` re-publishes the schedule — idempotent under one-schedule-per-subject replace —
  so re-doing the entity before expiry **replaces** the prior timer, and restart replay re-arms
  for free. A schedule-publish failure Naks the row on the bounded delay cadence.
- **Per-target-per-entity timer slot**: the subject carries both dot-free tokens
  (`schedule.weaver.timer.<targetId>.<entityId>`), so two targets watching the same entity hold
  independent timers. The `fired` token is reserved in this subject space — a targetId literally
  named `fired` is refused at scheduling time with a loud Health issue.
- **No weaver-state mark, no lease, for the fired→op conversion** — the §10.4 deterministic
  `requestId` is the dedup: a redelivered firing collapses on the Contract #4
  `vtx.op.<requestId>` tracker; a re-armed timer's new fire instant is a genuinely new op.
  Marks/OCC remain lane-1 remediation-dispatch machinery.
- **Never injected into `core-events` directly** — the transactional outbox stays the sole event
  producer; the fired message becomes a normal **op** (`MarkExpired`, payload
  `{entityKey, targetId, expiredAt}`, submitted under Weaver's service-actor authority with no
  `authContext`; the op's DDL/grants are package data).
- **Accepted Phase 2 bounds** (operator-visible, self-healing):
  - A `MarkExpired` **rejected at the Processor** is not re-attempted by Weaver (fire-and-forget,
    nothing leases it) — the freshness flip then waits for the next CDC touch of the entity. An
    op-*publish* failure IS retried (Nak → the same requestId).
  - With `MaxMsgsPerSubject: 1` on `core-schedules`, a **newer firing at the same fired subject
    rolls up an older one** the consumer has not yet processed — only the latest conversion is
    delivered, which is level-correct (the newest `expiredAt` supersedes; both would poke the
    same entity).
  - **No cancel/purge**: a deleted entity or removed target leaves a pending timer armed; the
    stray fire produces one `MarkExpired` for an absent entity — rejected/no-op at the
    Processor, harmless (no mark, no retry).
- The **fixed `weaver-temporal` durable**'s ack floor is the missed-while-down recovery: fired
  messages persist under limits retention and the durable resumes from its floor on restart.
  Phase 2 is single-instance; multi-instance fan-out is a Phase 3 concern. Heartbeat metrics
  `timersScheduled` / `timersFired` count the lane's two legs since start. No custom scheduler
  subsystem; the op-vertex pruner (#47/#49) remains Phase 2+ maturity.

---

## Dispatch suppression — `inflight_<g>` (Lens) + the `maxretries_<g>`-bounded dispatch-count (Weaver-state)

A gap is a `missing_<g>` §10.2 bool. Weaver suppresses re-dispatch of a gap on **two** grounds — a
remediation already in flight, and a spent retry budget — sourced differently:

- **`inflight_<g>` — a Lens column.** A Lens may project, **per gap**, the engine-recognized
  **dispatch-suppression companion** `inflight_<g>` (the **prefix-swap** convention). It is a §10.2
  `BodyColumn` Weaver reads to alter behaviour, the **same mechanism class as `freshUntil`** — **not**
  a `gaps` key, so the gap-column scans (`openGapColumns`, `markCandidateColumns`, which match the
  `missing_` prefix only) never treat it as a gap or write a mark at it. An **absent or non-bool**
  `inflight_<g>` reads `false` (via `boolColumn`, which surfaces a non-bool as a `RowDataError`).
- **The retry budget — a Weaver-state dispatch-count bounded by `maxretries_<g>`.** Weaver keeps a
  per-`(targetId, entityId, gapColumn)` **dispatch-count** in `weaver-state`
  (`<targetId>.<entityId>.<gapColumn>.__count`, a reserved key shape disjoint from marks and the
  `__control` marker). It is **incremented on each actual dispatch** — the lane-1 CAS-create-and-fire
  and the sweep's reclaim, so it tracks one-per-anti-storm-window real attempts from **both** dispatch
  legs — and **reset (deleted) on gap-close** by `clearClosedMarks` (the same level-reconciled path
  that deletes the mark). The Lens supplies only the **cap**: an integer `maxretries_<g>` column
  (package policy baked into the cypher, like the freshness window). The budget term suppresses when
  `dispatchCount(target, entity, gap) >= maxretries_<g>`.

The budget lives in Weaver-state, not the Lens, because a true reset-on-success ("failures since the
last success") is **not expressible as a lens predicate**: a lifetime `count(failed) >= cap` never
resets, so a check that fails to the cap, then completes, then goes stale would wedge the renewal
forever. Gap-close **is** the reset, and it lives where Weaver owns the close path. The count is
**chain-scoped**: it persists across mark-lease/TTL expiries (a reclaim must accumulate, not reset),
with a long TTL backstop (`dispatchCountTTLBackstopFactor × MarkLease`, far larger than the mark's)
**only** to GC an orphaned count whose gap-close was never observed — never to expire mid-chain.

The gate (`gapSuppressed` = `inflight_<g>` **OR** dispatch-count `>= maxretries_<g>`) is read in
**both** dispatch legs:

1. the **lane-1 dispatch loop** (`evaluator.go`, before `dispatchGap`), and
2. the **sweep `reclaim`** (`reconciler.go`, beside the `violating` gate) — the **load-bearing**
   one: a mark-lease expiry → reclaim is the actual re-dispatch path for a long-pending remediation,
   so the lane-1 skip alone would not stop the sweeper.

While either ground holds, Weaver **does not (re-)dispatch** the gap's remediation — but the gap
**stays violating** (the entity is genuinely unsatisfied); only re-dispatch is suppressed. Every
default is the **safe (dispatch) side**: an absent/non-bool `inflight_<g>`, an absent/non-positive
`maxretries_<g>`, or a transient count-read failure all leave the gap dispatchable, so a missing or
garbled input never silently wedges a real gap. Once the budget is spent the gap is the
operator-/Loupe-visible **"needs human escalation"** terminal — a human-submitted remediation that
*completes* closes the gap, which deletes the count, so a later reopen starts a fresh budget.

The lease-signing convergence lens is the reference user — for the bgcheck/payment external-call gaps
it projects `inflight_<g>` (a service instance with a `.dispatch` marker **present** and no
`.outcome` — presence-based, not deadline-bounded, so a stuck-pending call is never double-dispatched
against the vendor) and the constant `maxretries_<g>` caps; Weaver's dispatch-count enforces the
bounded retry against those caps.

---

## Control plane (FR30)

Operators manage Weaver's currently-registered convergence targets via a `nats-io/nats.go/micro`
Services responder (`internal/weaver/control`), mirroring Refractor's control plane
(`internal/refractor/control`), plus a `lattice weaver` CLI command group
(`cmd/lattice/weaver/`). `cmd/weaver` starts the listener alongside the engine.

### Subjects

| Subject | Operation |
|---------|-----------|
| `lattice.ctrl.weaver.list` (exact) | `list` — every registered target: `targetId`, `lensRef`, sorted playbook `gaps` columns, and `state` (`active` \| `disabled`) |
| `lattice.ctrl.weaver.<targetId>.disable` | `disable` — pause dispatch for `<targetId>` |
| `lattice.ctrl.weaver.<targetId>.enable` | `enable` — resume dispatch for `<targetId>` |
| `lattice.ctrl.weaver.<targetId>.revoke` | `revoke` — immediate cleanup + disable for `<targetId>` |

`TargetSummary.state` is a 2-value enum — there is no durable "revoked" state; `revoke` is a
strict superset of `disable` (see below) and reports `"disabled"`.

### Dispatch-skip marker and in-memory cache

Durable truth for the disabled state is the `<targetId>.__control` key in `weaver-state`
(Contract #10 §10.3 bucket; reserved-leading-underscore shape — `__control` can never collide
with a `<targetId>.<entityId>.<gapColumn>` mark, because `entityId`s are NanoIDs and
`substrate.Alphabet` contains no underscore). The reconciler sweep (`internal/weaver/reconciler.go`)
explicitly skips `__control`-suffixed keys — it is not a §10.3 mark and is never enumerated as
`CorruptMark` or deleted by a sweep pass.

The engine maintains an in-memory `disabledTargetSet`, seeded at `Start` by scanning
`weaver-state` for `*.__control` markers (`seedDisabledTargets`) and updated synchronously by
`Disable`/`Enable`/`Revoke` — the same "in-memory cache rebuilt from durable backing" pattern as
the target registry (`targetSource`) and `consumerStateCache`. The hot-path remediation guard
(`handleRow`'s dispatch leg) reads this in-memory set — no per-message KV read.

The disabled state suppresses **only remediation**, not violation-detection bookkeeping. A
disabled target still:

- clears resolved marks (`clearClosedMarks`, run unconditionally before the disabled-skip);
- arms/re-arms freshness timers (`scheduleFreshness`, lane-3 — keeps lane-3 state current so an
  instant re-enable loses no deadline);
- records freshness expiries (`handleFiredTimer` still submits `MarkExpired` for an already-armed
  timer — state-recording, already gated by the read-before-act row-presence/renewed guards).

What it does NOT do while disabled: create a new in-flight mark or run any Strategist/Actuator
remediation (`triggerLoom`/`assignTask`/`directOp`). On `enable`, lane-3/clearing state is
already current and remediation resumes for whatever is still violating — nothing is lost across a
disable→enable window, and no row re-touch is required.

### `disable` / `enable`

`disable <targetId>` writes the `<targetId>.__control` marker (and updates the in-memory set)
**first**, **then** calls `substrate.ConsumerSupervisor.Pause` on the target's lane-1 KV-CDC
durable (`PauseManual` — survives restart via the existing `HealthSink` pause-restore, the same
mechanism the supervised consumers use). `enable <targetId>` calls `Resume` **first**, **then** deletes the
marker and clears the in-memory set, and re-runs `reconcileConsumers` so a consumer removed by a
prior `revoke` is restored immediately rather than waiting for the next registry event. Both
return an error if `targetId` is not currently registered.

**Fail-safe-to-inert ordering.** The `__control` marker is the authority for the remediation-skip;
the `HealthSink` pause-restore is independent and governs only lane-1 pumping. The write order
makes every partial failure / restart window land on "still disabled (inert)", never "acting when
the operator said stop":

- `disable` writes the marker before the pause — a partial failure (marker set, pause failed or the
  process died) is remediation-inert (`handleRow` already skips), which is safe.
- `enable` resumes before deleting the marker — a partial failure (resumed, marker still present) is
  still remediation-inert; the operator re-issues `enable` to heal.

On restart the `__control` marker is authoritative for the remediation-skip (re-seeded into the
in-memory set by `seedDisabledTargets`).

### `revoke`

`revoke <targetId>` is a **strict superset of `disable`**: it (a) removes the target's lane-1
durable entirely (`ConsumerSupervisor.Remove` — durable deleted, mirroring
`reconcileConsumers`'s removal path), drops the engine's last-applied fingerprint
(`e.targets[targetId]`), and deletes the consumer's health-sink entry, (b) deletes every
`weaver-state` key with prefix `<targetId>.` — every in-flight `<targetId>.<entityId>.<gapColumn>`
mark **and** the `<targetId>.__control` marker — and clears the target's standing Health issues,
then (c) **re-writes** the `<targetId>.__control` disabled marker. Step (a)'s `e.targets` drop is
what lets the next `reconcileConsumers` pass re-Add the consumer (it now sees `running==false`);
step (c) means that re-added consumer comes up inert — because the target is still
`meta.weaverTarget`-registered (`revoke` does not unregister it or touch its Lens definition),
dispatch stays inert until an explicit `enable` (which clears the marker and re-runs reconcile).
Unlike `disable`/`enable`, `revoke` on an unregistered/unknown `targetId` is **not** an error —
idempotent, mirroring `ConsumerSupervisor.Remove`'s no-op-if-unmanaged posture, and still writes
the disabled marker so a future registration of that `targetId` starts disabled.

**Uninstall vs. revoke.** A `revoke` keeps the target registered and disabled. A genuine uninstall
(the target leaving `targetSource` — e.g. its Lens is retired) is the `reconcileConsumers` removal
branch, which deletes the consumer, its health-sink entry, **and** the `<targetId>.__control`
marker, and prunes the in-memory set — so a re-install of the same `targetId` does not silently
come up disabled and no orphan marker leaks in `weaver-state`.

**`revoke` is immediate-cleanup, not standing suppression of re-registration** — it does not
prevent the target from being re-installed via a fresh `meta.weaverTarget` vertex, and it does
not retire the target's underlying Lens. Fully decommissioning a target requires also retiring
its Lens (out of this story's scope — an op-path/Refractor concern).

### Capability authorization

`internal/weaver/control` ships a `StubCapabilityChecker` (allow-all, logs every call) — mirroring
`internal/refractor/control`'s stub posture. Full Capability-KV integration of the control plane is
deferred (Phase 3).

---

## What this component owns

| Path | Role |
|------|------|
| `internal/weaver/` | Engine: Sensorium, 3-lane work stream, Evaluator tiers, Strategist dispatcher, Actuator |
| `internal/weaver/control/` | Operator control plane (FR30): `list`/`disable`/`enable`/`revoke` NATS Services responder |
| `cmd/weaver/` | Binary entry point (extractable; shares only `substrate/*`) — starts the control-plane listener alongside the engine |
| `cmd/lattice/weaver/` | `lattice weaver list\|disable\|enable\|revoke` CLI command group |

**Package data:** target Lens cypher, playbook definitions, gap→action mappings (`lease-signing`).

---

## In / Out contracts

| Direction | Contract | Notes |
|-----------|----------|-------|
| In | `weaver-targets` `<targetId>.>` KV-CDC durable | lane 1 (primary input — **not** the core-events consumer) |
| In | `events.<domain>.>` per-domain consumer | lane 2 targeted-audit only (Phase 3) |
| In | `schedule.weaver.timer.fired.>` on `core-schedules` | lane 3 (ADR-51 scheduled messages; fixed durable `weaver-temporal`) |
| Out | ops via `core-operations` (`ops.<lane>`) | fire-and-forget; OCC `expectedRevision` payload; trigger-Loom |
| Out | `@at` schedules via `core-schedules` (`schedule.weaver.timer.<targetId>.<entityId>`) | lane 3 scheduling leg; replace-on-reschedule (one schedule per subject) |
| Out (own) | `weaver-state` bucket | in-flight convergence marks (anti-storm); per-key TTL backstop (2× lease) + reconciler sweep |
| In/Out | `micro.Service` endpoints at `lattice.ctrl.weaver.<targetId>.<op>` and `lattice.ctrl.weaver.list` | Control plane (FR30): operator `list`/`disable`/`enable`/`revoke` — see "Control plane" below |

---

## Failure modes

| Mode | Behavior |
|------|----------|
| Re-trigger storm | violation persists until gap closes *and* re-projects → the `weaver-state` in-flight mark suppresses re-trigger |
| **Actuator crash mid-flight** | in-flight marks carry a **TTL/lease**; the reconciler sweep reclaims expired leases so a target is never wedged. *(Tested: `TestWeaverE2E_MidFlightKill` kills the episode between CAS-create and publish and proves the re-attempt.)* |
| External call retried/failed | not a Weaver concern — `triggerLoom` of an `externalTask` hands the call to the bridge, which de-dups on the `idempotencyKey` so a retry produces at most one external effect (FR58; see `docs/components/bridge.md`) |
| Target too costly to keep live | lane-2 on-demand evaluation (deferred-exercise) |

---

## Principles that apply

- **P2** — Processor is the sole Core KV writer; Weaver is a client. Claims/state are
  operational KV, not Core KV (P1).
- **P4** — Weaver enforces declarative convergence invariants; Starlark enforces single-op
  invariants only.
- **Weaver targets ARE Lenses** — Weaver consumes the Refractor; it is not a cypher runtime.
- **Module boundary** — `weaver` imports only `substrate/*`; triggers Loom via NATS/op.

## Implementation status

What ships today in `internal/weaver` + `cmd/weaver`, and what is deliberately deferred:

| Surface | Status |
|---------|--------|
| **Lane 1 (violation-driven)** | ✅ Shipped. One **supervised KV-CDC durable per target** on the `KV_weaver-targets` backing stream (`$KV.weaver-targets.<targetId>.>`, `DeliverLastPerSubject`) via `substrate.ConsumerSupervisor` — never a raw `kv.Watch`. Desired-vs-running reconcile over the `meta.weaverTarget` registry: removal deletes the JetStream durable; a spec change Resets it. |
| **Target registry** | ✅ Shipped. `meta.weaverTarget` CDC source (Core KV `vtx.meta.>`), §10.8 install-time validations (`missing_*` gaps keys, `targetId` uniqueness + dot-free), reject-and-alert (Health KV issue), never silent. |
| **Dispatch OCC (§10.3)** | ✅ Shipped in the full frozen shape: the `weaver-state` mark (`<targetId>.<entityId>.<gapColumn>`) is a **CAS-create** carrying `claimedAt`/`leaseExpiresAt`/`heldBy` and a **NATS per-key TTL at 2× the lease** (the backstop — a dead reconciler can never wedge a gap forever); mark-clearing is **level-reconciled on each watch update AND each reconciler sweep**. The mark carries the frozen `claimId` field (`omitempty`) but it is always empty: every action dispatches through a single fire-and-forget `ops.<lane>` submit, so no id is ever minted onto the mark. |
| **Reconciler sweep** | ✅ Shipped (`internal/weaver/reconciler.go`): an interval-cadence pass (default 1m, clamped ≤ the lease so expiry is always observed before the TTL backstop; lease default 30m, both `Config`-tunable) over every mark — prompt level-clearing of closed gaps, orphan reclaim (target removed, column dropped from row + playbook), corrupt-mark delete+alert (the issue retires once the key stays gone), and **expired-lease reclaim as a fresh episode**: a revision-conditioned **in-place replace** of the mark (fresh lease, re-armed TTL → new revision → new `requestId`), so the key is never absent across a reclaim and only **violating** rows re-dispatch (mirroring lane-1's L1 gate). A re-fired `triggerLoom`/`assignTask`/`directOp` is the **documented rare-double** — operator-visible via the sweep's Warn logs and heartbeat counters, with the check-before-act probe deferred to Phase 3. All sweep deletes are revision-conditioned; both orphan legs are gated for a warm-up window after start (`SweepOrphanWarmup`, default 5m — a registry-replay-readiness proxy). |
| **Actuator** | ✅ Shipped as **fire-and-forget publish** to `ops.<lane>` with a deterministic per-dispatch-episode `requestId` (derived from the mark's current revision — its CAS-create, or the sweep's reclaim replace; Contract #4 collapses re-fires). **No request-reply, no command outbox** — Weaver has no cursor advance to dual-write; recovery is the mark + level-reconcile + lease: a rejected/lost op leaves the mark in place and the sweep re-attempts it at lease expiry. The op payload carries the row's `expectedRevision` (the OCC revision-condition); `triggerLoom` resolves the live `meta.loomPattern` vertex for `authContext.target` (pattern-as-target). |
| **Actions** | `triggerLoom` (→ `StartLoomPattern` op, never a Go call), `assignTask` (→ `CreateTask` with episode-deterministic `taskId`), `directOp` ✅. External idempotent I/O is `triggerLoom` of a Loom `externalTask` pattern — the **bridge** executes the call (`docs/components/bridge.md`); Weaver never holds an adapter. An action the playbook names outside this set fails closed (a loud `PlaybookConfigError` Health issue), never a silent skip. |
| **Health** | ✅ Contract #5 heartbeat at `health.weaver.<instance>` (metrics: `consumers`, `targets`, `marksInFlight`, `sweepReclaims`, `sweepOrphansDeleted`, `sweepCorrupt`, `sweepLastRunAt`, `timersScheduled`, `timersFired`) + per-consumer pause-state docs at `health.weaver.<instance>.consumer.<name>`; config/data errors surface as issues. |
| **Lane 3 (temporal)** | ✅ Shipped (Contract #10 §10.4). One **fixed supervised durable** `weaver-temporal` on `core-schedules` filtered `schedule.weaver.timer.fired.>`; the lane-1 row handler's **scheduling leg** re-arms `@at(freshUntil)` per delivery (level-driven, replace-on-reschedule); the fired→op conversion submits `MarkExpired` under the **deterministic timer `requestId`** (schedule subject + fire instant) with **no weaver-state mark**. See "Temporal lane" above for the convention column and the accepted Phase 2 bounds. |
| **Control API/CLI (Pause/Resume surface)** | ✅ Shipped (FR30). `internal/weaver/control` exposes `list`/`disable`/`enable`/`revoke` over a `nats-io/nats.go/micro` Services responder; `lattice weaver` CLI group. See "Control plane" above. |
| **Lane 2 (event-targeted-audit) + `weaver-work`** | ⏳ Phase 3 (§10.3: no durable bucket today). |
| **Real target Lens via Refractor + playbook package data** | ✅ Shipped — the `lease-signing` reference vertical provides a real convergence target + §10.8 playbook; the engine also runs against test-written §10.2 fixture rows. |

---

## Deferred (Phase 3+)

- Refractor negative/filter-retraction projection (true emit-only-when-violating).
- Lane-2 on-demand evaluation (built, unexercised in demo).
- L3 evaluator (AI-assisted).
- Full temporal scheduler / op-vertex pruner (#47/#49).
- Real external adapters (Stripe/background-check) — Phase 3 integration.
