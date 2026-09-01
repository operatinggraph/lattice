# Contract #10 (Substrate) — task vertex, operational KV, scheduling, task grants

> **A shard of [Contract #10 — Orchestration Surfaces](10-orchestration-surfaces.md)** — §10.1 /
> §10.3 / §10.4 / §10.7 keep their canonical numbers: the cross-engine surfaces both Loom and Weaver
> build on.

## 10.1 Task vertex (D5 placement)

The generic `task` type DDL ships in the foundational package **`orchestration-base`**. Field
placement follows D5 — **Capability-Lens-read fields on root `data`**. The generic `task` carries
**no aspects**: only root scalars + relationship links (the UI renders from the **bound op's
self-describing DDL** via `forOperation`; instance specifics come from the `scopedTo`/`assignedTo`
links).

**Relationships are LINKS, not fields.** Only scalar attributes live on root `data`:

```
vtx.task.<id>            (root data — scalar attributes only)
  { status, expiresAt }
lnk.task.<id>.forOperation.meta.<opId>           # the operation this task grants (relationship → link)
lnk.task.<id>.scopedTo.<type>.<targetId>         # the target the grant is scoped to (often ≠ assignee)

# EXACTLY ONE assignment link is present on an open task (FR28):
lnk.task.<id>.assignedTo.identity.<assigneeId>   # direct/push: a named identity performs it (§6.9 convention)
lnk.task.<id>.queuedFor.role.<roleId>            # role-queue/pull (FR28): any holder of the role may ClaimTask it
```
(All links: task = later-arriving **source**, the other vertex pre-exists = **target**, per Contract #1 §1.1.)

- `status ∈ {open, complete, cancelled}` — root, scalar; an expired/closed task must not grant.
- **The ephemeral-grant *field shape* is** flattened `{source, taskKey, operationType, target,
  expiresAt}` (Contract #6 §6.6 — flattening is correct in a Lens read model), projected by the
  package-owned `capabilityEphemeral` lens to the disjoint key `cap.ephemeral.<actor-suffix>` and
  **link-sourced** (§10.7). Authorization: §10.7 / Contract #6 §6.6.
- UI finds the bound op's schema by walking `forOperation` to the operation meta-vertex.
- **No-orphan invariant (FR29 by construction):** an open task carries **exactly one** assignment link.
  `CreateTask` **requires a routable endpoint** — either an `assignee` identity (committing
  `task --assignedTo--> identity`) **or** a `queue` role (committing `task --queuedFor--> role`) — and
  **rejects** (structured `ScriptError`, `RoutingFailed`/`UnknownAssignee`) if neither resolves to a
  live vertex: a task pointing at a non-existent endpoint is never committed. `CreateTask` /
  `ReAssignTask` validate the endpoint by a **known-key read** (the named identity/role); they do **not**
  enumerate a role's members (the write-path no-scans invariant), so an empty/unstaffed role-queue is
  *not* a creation-time error — see the FR28 paragraph below. (Link direction per Contract #1 §1.1: the
  task is the later-arriving source; the assignment-link name reads from the source side.)
  Tombstoning/merging an identity (or role) that holds open tasks is rejected (operator
  reassigns/cancels first).
- **FR28 (role-queue + routing fallback) — landed.** A task may be assigned to a **role-queue**
  (`queuedFor role`) instead of a named identity. `CreateTask` **routes**: a named `assignee` that is
  alive and available → `assignedTo` (the direct/push path, unchanged); else a `queue` role that is alive
  → `queuedFor` (the pull path); else → reject `RoutingFailed`. **`ClaimTask(taskKey)`** lets any holder
  of the queued role claim the task — it validates the claimant↔role `holdsRole` link (known-key) and
  atomically swaps `queuedFor → assignedTo claimant`. **Grant fan-out:** while queued, the package-owned
  `capabilityEphemeral` lens projects the task's ephemeral grant (and `my-tasks` its inbox row) to
  **every identity holding the role** — the role-queue's "anyone in the team may perform it" semantics,
  via the same actor-aggregate fan-out the `reportsTo` delegation already uses; on `ClaimTask` the grant
  **narrows** to the claimant through ordinary reprojection. **The §6.6 grant *field shape* and its
  matching logic are UNCHANGED** — a role-queued grant is just another per-actor `ephemeralGrants[]`
  entry, matched identically; the fan-out is a lens (package) detail, not a §6.6 change.
- **FR29 (unrouted tasks surface; never silently dropped).** A role-queue with **no eligible actor** is
  knowable only post-hoc (membership is a scan the write path may not run), so it is surfaced — not
  rejected — by an `orchestration-base` **`unroutedTasks` Weaver convergence target**: an open `queuedFor`
  task left unclaimed past a staleness threshold projects a `violating` row (visible in Loupe's
  convergence view) and rolls a `UnroutedTasks` entry into Weaver's Contract #5 §5.5 `issues[]` channel
  (severity warning ⇒ degraded). Surface-only (manual intervention); auto-escalation is a follow-on.

**`my-tasks` projection + tombstone obligation.** The `orchestration-base` package ships a `my-tasks`
actor-aggregate lens projecting, per identity, that identity's **open** tasks to the package-owned
bucket keyed `my-tasks.<actor-suffix>` (e.g. `my-tasks.identity.<id>`). It is a **guarded**
actor-aggregate key under the projection-write integrity guard (Contract #6 §6.2/§6.8): the close-task
delete is a **soft tombstone** `{ isDeleted: true, projectionSeq }`, not a physical key removal, so a
stale lower-seq replay cannot resurrect a completed task. **Obligation:** any UI/query consumer of the
`my-tasks` bucket **MUST treat an `isDeleted: true` document as absence** (skip it) — otherwise a user
sees ghost tasks they already completed.

---


## 10.3 Operational KV namespaces — **FROZEN 2026-06-02** (amended 2026-06-18, 13.1)

All buckets here are **operational state (P1)** — single-component bookkeeping, never Core KV. Each
bucket is its owner's **private** state: no other component, application, or package reads or writes
it, and nothing in a bucket's internal key layout is a platform surface. **Bucket names are
dash-named** (NATS KV stream tokens, no dots). Both live buckets are primordial.

| Bucket | Owner | Purpose |
|--------|-------|---------|
| `loom-state` | Loom | per-instance pattern cursor + the engine's transactional bookkeeping |
| `weaver-state` | Weaver | anti-storm in-flight marks + dispatch bookkeeping |
| `weaver-work` | Weaver | **in-process lane multiplexer only**; a durable bucket lands with Phase-3 lane-2 (event-targeted audit), whose trigger is a transient core-event — every live lane already replays from its source |

*(`weaver-claims` is **retired** — the subsection below keeps what replaced it.)*

Key layouts, value shapes, index/provisioning posture, and recovery mechanics are the owning
component's own: `docs/components/loom.md` / `docs/components/weaver.md`.

### `loom-state` — Loom's instance promises

- **Definitions bind at instance start.** An instance runs against the pattern definition that was
  live when it started; a pattern update mid-flight affects **new instances only** — reordering,
  inserting, or changing steps in the live definition cannot mis-index a running instance, and an
  in-flight instance survives its pattern being removed or updated away. A running instance whose
  pinned definition is lost is an invariant break, surfaced as an operator-visible failed terminal —
  never a silent re-bind to the live source. Disaster recovery (total `loom-state` loss → fresh
  `StartLoomPattern`) re-binds to the **current** definition; this is re-convergence under today's
  truth, not a regression of the binding.
- **`failed` is terminal for every automatic path, but not unconditionally one-way**: the
  `lattice.ctrl.loom.<instanceId>.redrive` operator command is the one sanctioned `failed → running`
  transition. It resumes **at the instance's recorded `cursor`** — never restarts at a fresh
  id/cursor 0, which would re-execute every step the failed run already committed — and re-binds the
  instance to the **current** live definition. If that definition no longer covers the recorded
  `cursor` (the pattern was edited since the failure), redrive **refuses** rather than resume against
  a misindexed step. Concurrent redrives of the same instance are safe: at most one takes effect; the
  other is refused.
- **A step transition and the op it emits are one atomic fact — never a dual write.** Loom cannot
  commit a cursor advance without the op it implies, nor submit the op without the advance. A crash
  between commit and publish self-heals on resume: re-publish is idempotent (Loom chooses the
  `requestId`, so a duplicate collapses on the Contract #4 `vtx.op.<requestId>` tracker), and a
  redelivered submission never double-acts.
- **Every pending step carries a deadline.** A step whose reply never arrives — off-stream rejection,
  lost completion, dead downstream — is detected by deadline expiry and drives §10.6's
  step-deadline-exceeded recovery; an instance never hangs silently on a step that will not complete.
- **Engine replicas are interchangeable.** Completion correlation needs no in-memory state: any
  replica resolves any pending completion from the bucket alone, and engine state is rebuildable
  (D3) with no startup scan.

### `weaver-state` — anti-storm + re-fire promises (§10.8)

- **In-flight suppression, with a self-expiring backstop.** While a gap's remediation is in flight,
  Weaver does not re-dispatch it. The suppression is leased, and the lease's expiry backstop is
  substrate-enforced: a dead or missing reconciler can **never wedge a gap forever** — the
  suppression self-expires and the gap becomes reclaimable. The lease is sized ≫ expected remediation
  latency, so expiry means "presumed dead."
- **Suppression clears by level, not edge.** Weaver compares the current row against its standing
  marks on every watch update **and** every reconciler sweep, clearing any suppression whose gap
  column is now false — it never depends on catching a transitional flip (a coalescing watch can
  drop edges), and a stale mark from a prior closed episode cannot shadow a fresh re-open.
- **Re-fire after lease expiry is class-dependent, and the class is read from the pattern's own step
  kinds — never from the playbook action name.**
  - **userTask gaps** — `assignTask`, and `triggerLoom` of a **userTask-containing** pattern: a
    reclaim re-dispatch **collapses onto the existing open artifact** (the same task; the same Loom
    instance) — a lease expiry never produces a duplicate human task. A legitimate close→reopen of
    the gap yields a genuinely fresh task/instance; an out-of-band deletion self-heals (the task is
    re-created, or a logically-deleted one revived) rather than wedging.
  - **External gaps** — `directOp`, or `triggerLoom` of an **externalTask-only** pattern (the §13.1
    external-remediation path): a reclaim re-dispatch is **intended** — a genuinely new attempt
    (a new vendor call) — gated on `inflight_<g>` reading false and hard-bounded by `maxretries_<g>`.
    Exhaustion raises §10.8's `GapBudgetExhausted` — **a loud stop, never a silent park** — and the
    exhaustion alert is durable: it survives suppression expiry and engine restarts for as long as
    the suppression lasts.
  - **A gap that declares `inflight_<g>` MUST declare `maxretries_<g>` when it is an External gap;
    the requirement never binds a userTask gap.** Only an External reclaim starts a genuinely new
    attempt with nothing downstream to collapse a duplicate onto, so `maxretries_<g>` is that
    reclaim's *only* bound — declaring `inflight_<g>` without it makes `GapBudgetExhausted`
    unreachable and the gap re-dispatches indefinitely. A userTask gap may declare `inflight_<g>`
    alone, for suppression only (§10.2): the duplicate-bound it would protect is one the consumer
    side already holds.

### Named constructs the sibling shards reference

The Loom/Weaver shards (§10.5–§10.6, §10.8) reference these §10.3 constructs by name. Their meaning
is fixed here; their layouts, tuning, and mechanics are the owning component's
(`docs/components/loom.md` / `docs/components/weaver.md`):

- **`instance.<instanceId>`** — a Loom instance's sole durable record (the cursor); the instance has
  **no Core-KV vertex**. The record persists after terminal: its presence is the dedup evidence that
  collapses a re-emitted trigger for the same instance.
- **`token.<pendingToken>`** — the durable pending-completion pointer; any replica correlates any
  completion by a direct GET on it, and pointer absence means the step already advanced.
- **The command-outbox relay** — the op a step transition emits is recorded in the same atomic batch
  and published asynchronously; after a crash it re-publishes, and duplicates collapse on the
  Contract #4 tracker.
- **`deadline.<instanceId>`** — the pending step's deadline; its expiry drives §10.6's
  step-deadline-exceeded recovery.
- **The mark** (`weaver-state`) — a gap's in-flight suppression record. It pins the **chosen action**
  for the episode's duration, and carries the episode's **`claimId`**, preserved across every
  reclaim of a userTask gap (the identity that lets a re-dispatch collapse onto the existing
  task/instance); an External reclaim mints a fresh one.
- **`<targetId>.__control`** — the durable dispatch-freeze marker behind the control plane's
  `disable`/`enable`/`revoke` remediation-skip.
- **`…__count`** — the per-gap dispatch counter bounded by `maxretries_<g>`; the durable anchor from
  which an exhausted gap's standing `GapBudgetExhausted` issue is re-derived for as long as the
  suppression lasts.
- **`…__effect`** — per-(gap, action) dispatch/close bookkeeping over a sliding episode window; the
  close-rate input to §10.8's planned-mode action selection.

### `weaver-claims` — retired

The Two-Phase Nudge claim record and the in-Weaver **Claim → Execute → Resolve** protocol are
**retired**; external idempotent I/O lives in **Loom + the bridge** (§10.5/§10.6 `externalTask`).
The FR58 / NFR-S11 **visible claim** is the **claim vertex in Core KV** created by the
`externalTask`'s `instanceOp` **before** the `external.*` event is publishable — one auditable
business vertex (type package-chosen; the bridge is type-agnostic) whose external **outcome is
recorded as aspect(s)** per D5, never fat root `data`.

**Hard invariant (FR58 determinism):** the bridge's result-op **`requestId` MUST be
`deterministic(idempotencyKey = instanceKey)`**, so a redelivered `external.*` event produces the
**same** result-op requestId and collapses on the Contract #4 tracker → **exactly one** result
mutation. The event-plane analog of §10.4's deterministic-`requestId` rule for the fired-timer→op
path.

---


## 10.4 Message scheduling — platform-wide (ADR-51) — **FROZEN 2026-06-02**

Message scheduling is a **platform-wide capability**, not Weaver-specific — same status as Health KV:
bootstrapped as core infra, usable by any component. Op-vertex / tracker **retention** is **not** a
schedule-lane consumer (trackers expire by NATS per-key TTL, Contract #4 §4.3; the events-outbox
aspect is tombstoned by the outbox consumer on confirmed publish).

```
stream:            core-schedules             # platform-bootstrapped
schedule subject:  schedule.<domain>.<kind>.<token...>    # publish here; one schedule per subject
                                              #   (bare-word subject root, like ops.> / events.>)
                                              #   <token...> = publisher-chosen dot-free token(s)
                                              #   e.g. Weaver uses  schedule.weaver.timer.<targetId>.<entityId>
header:            @at <RFC3339>   (absolute; or @every for recurring)
                   Nats-Schedule-Target: <target subject>   # republish target (must be within schedule.>)
target subject:    schedule.<component>.fired.<token...>    # publisher-chosen, but MUST be within schedule.>
                                              #   e.g. Weaver uses  schedule.weaver.timer.fired.<targetId>.<entityId>
                                              #   (the scheduler republishes back into core-schedules here)
```

- **Naming:** stream `core-schedules` (dash-form, no project name — matches `core-operations` /
  `core-events`); subject root `schedule.>` (matches `ops.>` / `events.>`).
- **The segments after `schedule.<domain>.<kind>.` are publisher-chosen, dot-free tokens** within the
  `schedule.>` space — a publisher MAY key with more than one entity token, and keys them so
  independent schedules never collide on the one-schedule-per-subject rollup (Weaver keys per target
  AND entity: `schedule.weaver.timer.<targetId>.<entityId>`). Each token is a **NanoID, not the
  dotted vertex key** (dots are subject-token separators); the full entity key, if needed, rides the
  **message payload**, not the subject.
- The **stream** is shared/platform-wide (primordial); the **target (fired) subject** is chosen per
  publisher and **must lie within `schedule.>`** (an out-of-stream target is rejected at publish
  time). When the timer fires, the scheduler republishes the payload back into `core-schedules` at
  the target subject, and each component consumes **only its own** fired subjects.
- Per-subject schedule → re-scheduling **replaces** the prior timer (one schedule per subject; for
  Weaver, per `<targetId>.<entityId>`).
- Durable across restart. The fired message hits the publisher's target subject; that component
  converts it to a normal **op** via the Processor — it is **never** published to `core-events`
  directly (the transactional outbox, Contract #3, remains the sole event producer).
- **Fired-timer → op is dedup'd.** Delivery is at-least-once (a consumer crash before ack
  redelivers), so the converted op carries a **deterministic `requestId`** derived from the schedule
  subject (`schedule.<domain>.<kind>.<token...>` + fire instant) → Contract #4's `vtx.op.<requestId>`
  tracker collapses redeliveries. A redelivered timer does **not** double-act.

### Recurring schedules (`@every` / cron)

`@at` (one-shot) and `@every <duration>` / 6-field cron (recurring) share the same lane, headers,
subject discipline, and dedup rule (version gates: `docs/vendors.md`). The recurring form differs only
in lifecycle:

- **The schedule persists and re-fires indefinitely.** For `@every`/cron the scheduler keeps the
  schedule at its subject and delivers a fresh fired copy on **every** interval (a one-shot `@at`
  auto-purges after its single delivery). One active schedule per subject still holds.
- **Re-publishing the same subject REPLACES the prior schedule** (retune the cadence); **cancellation**
  removes the schedule at its subject (purge, delete, or the atomic conditional stop). There is no
  implicit expiry — a recurring schedule runs until removed (a publisher that arms one owns stopping
  it; idempotent re-arm on restart is the norm).
- **Per-occurrence dedup extends the one-shot rule verbatim.** Each occurrence's converted op carries
  a **deterministic `requestId`** derived from the schedule subject **+ the occurrence instant**, so
  an at-least-once redelivery of the *same* occurrence collapses on the Contract #4 tracker while a
  *new* occurrence is genuinely new work. A fire that drives a level-reconcile **handler** (not an op
  — e.g. a recurring sweep) is idempotent by the handler's own construction and needs no tracker.
- **Past-due ticks after a restart fire immediately** (catch-up), and the scheduler **coalesces**
  overdue ticks rather than replaying each missed one.
- A recurring consumer gains single-fire-across-replicas + an operator-visible, retunable cadence
  (e.g. Weaver's reconciler sweep — `docs/components/scheduling.md`).

---


## 10.7 Ephemeral task grants — authorization (FR56)

A task assignment authorizes its assignee to perform the granted op **on the task's specific
target** via FR56 (Contract #6 §6.6). The grant *matching logic* and *field shape* are Contract #6
§6.6's; this section binds the task vertex to them.

- The grant projection is the **`orchestration-base`-owned `capabilityEphemeral` lens** writing the
  disjoint key **`cap.ephemeral.<actor-suffix>`** (Contract #6 §6.6), per actor covering direct
  assignment plus 2-hop `reportsTo` manager delegation; each grant =
  `{ source, taskKey, operationType, target, expiresAt }`. **Link-sourced:** `operationType` and
  `target` come from the task's own `forOperation` / `scopedTo` links; `expiresAt` from the task
  scalar. The core capability projection carries no task knowledge — the task grant surface is
  package-owned end to end.
- The op the assignee performs declares **`authContext.{task, target}`**. The task dispatch path
  authorizes iff a grant matches **`taskKey` ∧ `operationType` ∧ `target` ∧ `expiresAt > now`**;
  a no-match **denies** with `AuthContextMismatch`.
- **Subject-scoping is intrinsic** (the grant's target must equal the op's declared target): a
  leasing manager with many open `ApproveLeaseApplication` tasks is authorized for each *specific*
  lease application, never blanket.
- **No `fulfillsTask` field, no `taskGated` flag, no Contract #2 change.** The grant *field shape*
  (`EphemeralGrant`) is Contract #6 §6.6's.

> Task **completion** rides on this auth: a successful op authorized via `authContext.task = T`
> auto-completes T in the same atomic batch (§10.6).

---

