# Contract #10 (Substrate) — task vertex, operational KV, scheduling, task grants

> **A part of [Contract #10 — Orchestration Surfaces](10-orchestration-surfaces.md)** (the index +
> shared revision history). Section numbers **§10.1 / §10.3 / §10.4 / §10.7** are unchanged. These are
> the cross-engine surfaces both Loom and Weaver build on: the generic task vertex, the
> `loom-state` / `weaver-state` operational buckets, ADR-51 message scheduling, and ephemeral task grants.

## 10.1 Task vertex (D5 placement)

The generic `task` type DDL ships in the foundational package **`orchestration-base`**. Field
placement follows D5 — **Capability-Lens-read fields on root `data`**. In Phase 2 the generic `task`
carries **no aspects**: only root scalars + relationship links. (The UI renders from the **bound op's
self-describing DDL** via `forOperation`, not a task-local presentation aspect; instance specifics —
which target, who — come from the `scopedTo`/`assignedTo` links. A per-task presentation/params aspect
would duplicate the op schema and had no producer in the frozen §10.5 step shape, so both were dropped.)

**Relationships are LINKS, not fields** (decision #2; no exception exists in the brainstorming —
the Phase-1 `task.data.grantedOperationType`/`targetKey` *fields* in `lenses.go` are an undocumented
anti-pattern, corrected here). Only scalar attributes live on root `data`:

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
- **The ephemeral-grant *field shape* is UNCHANGED** (Contract #6 §6.6 still emits flattened
  `{source, taskKey, operationType, target, expiresAt}` — flattening is correct in a Lens read model).
  Two things change, per the (a1) extraction decision (2026-06-02):
  1. **The grant projection moves OUT of the bootstrap god-cypher into a package-owned lens.**
     `orchestration-base` ships a `capabilityEphemeral` lens that projects the grants to a **disjoint
     key** `cap.ephemeral.<actor-suffix>` (the same multi-Lens / disjoint-prefix pattern Contract #6
     §6.1 already endorses and `capabilityRoleIndex` already proves). The **bootstrap `capability`
     cypher drops its two `task` OPTIONAL MATCHes entirely** → core no longer references the `task`
     package type. Dependency direction is now correct: package(orchestration-base) → core(identity).
  2. **The grant is link-sourced, not field-sourced.** The new lens walks
     `(identity)<-[:assignedTo]-(task)-[:forOperation]->(op)` for `operationType`,
     `(task)-[:scopedTo]->(t)` for `target`, `task.data.expiresAt` (scalar) for expiry, plus the
     existing `reportsTo` 2-hop for manager delegation. *(Was: `task.data.grantedOperationType` /
     `targetKey` fields — the corrected anti-pattern.)*
- **Authorization mechanism (§10.7) is otherwise UNCHANGED** — op carries `authContext.{task,target}`;
  step-3 `matchEphemeralGrant` matches `taskKey + operationType + target + expiresAt`. The only step-3
  change: the **task-dispatch branch reads `cap.ephemeral.<actor>`** (it needs only grants) — a **single
  GET**, no fallback. A task-path no-match denies with `AuthContextMismatch`, which carries no
  `actorRoles` (the denial builder returns early for that code), so **no `cap.<actor>` second read is
  needed**. Subject-scoping intrinsic. Full shape + migration notes: Contract #6 §6.6 (Phase 2 amendment).
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
  **narrows** to the claimant through ordinary reprojection. **The §6.6 grant *field shape* and the
  step-3 matching logic are UNCHANGED** — a role-queued grant is just another per-actor `ephemeralGrants[]`
  entry, matched identically; the fan-out is a lens (package) detail, not a §6.6 change.
- **FR29 (unrouted tasks surface; never silently dropped).** A role-queue with **no eligible actor** is
  knowable only post-hoc (membership is a scan the write path may not run), so it is surfaced — not
  rejected — by an `orchestration-base` **`unroutedTasks` Weaver convergence target**: an open `queuedFor`
  task left unclaimed past a staleness threshold projects a `violating` row (visible in Loupe's
  convergence view) and rolls a `UnroutedTasks` entry into Weaver's Contract #5 §5.5 `issues[]` channel
  (severity warning ⇒ degraded). Surface-only (manual intervention); auto-escalation is a follow-on.

**`my-tasks` projection + tombstone obligation (Phase 2, Story 12.1a).** The `orchestration-base`
package ships a `my-tasks` actor-aggregate lens projecting, per identity, that identity's **open**
tasks to the package-owned bucket keyed `my-tasks.<actor-suffix>` (e.g. `my-tasks.identity.<id>`). It is
a **guarded** actor-aggregate key under the projection-write integrity guard (Contract #6 §6.2/§6.8):
the close-task delete is a **soft tombstone** `{ isDeleted: true, projectionSeq }`, not a physical key
removal, so a stale lower-seq replay cannot resurrect a completed task. **Forward obligation:** any
UI/query consumer of the `my-tasks` bucket **MUST treat an `isDeleted: true` document as absence** (skip
it) — otherwise a user sees ghost tasks they already completed. No production reader exists yet (only
the E2E); this records the obligation for the first one.

---


## 10.3 Operational KV namespaces — **FROZEN 2026-06-02** (amended 2026-06-18, 13.1)

All buckets here are **operational state (P1)** — single-component bookkeeping, never Core KV. **Bucket
names are dash-named** (NATS KV stream tokens, no dots — the earlier `loom.state.>` / `weaver.state.>`
notation was loose). `weaver-state` already exists as a primordial constant
(`primordial.go`); `loom-state` joins the primordial create list (Loom bootstrap story).
(`weaver-claims` is **retired** — see below; its bucket/constant teardown lands in the External I/O
Bridge nudge-retirement story.)

| Bucket | Owner | Key | Status |
|--------|-------|-----|--------|
| `loom-state` | Loom | `instance.<instanceId>` / `instance.<instanceId>.pattern` / `token.<pendingToken>` / `outbox.<token>` / `deadline.<instanceId>` | primordial (new), `AllowAtomicPublish: true` |
| `weaver-state` | Weaver | `<targetId>.<entityId>.<gapColumn>` | primordial (exists) |
| `weaver-work` | Weaver | — | **in-process only; no durable bucket in Phase 2** (see below) |

*(`weaver-claims` — **RETIRED** 2026-06-18, 13.1; the row is dropped here, the subsection below records why and what replaces it, and the bucket/primordial-constant/verify-enumeration teardown lands in the External I/O Bridge nudge-retirement story.)*

### `loom-state` — per-instance Loom cursor + co-located reverse index

`loom-state` holds **five key shapes** in the one bucket: four mutually disjoint prefixes (the same
one-bucket / disjoint-prefix pattern capability-kv §6.1 uses for `cap.ephemeral.*`), plus the
`instance.<instanceId>.pattern` definition pin — deliberately a **sub-key of its instance**
(instanceIds are NanoIDs, so the `.pattern` suffix is unambiguous and cannot collide with an
`instance.<instanceId>` key):

```
key:  instance.<instanceId>           value: { instanceId, patternRef, subjectKey, cursor, pendingToken, status, retryCount }
key:  instance.<instanceId>.pattern   value: <the full pattern definition, as loaded at trigger time>
key:  token.<pendingToken>            value: { instanceId }                                          # thin reverse pointer (committed-path)
key:  outbox.<token>                  value: { requestId, operation, payload, target, lane, actor }  # command-outbox record (the op to submit)
key:  deadline.<instanceId>           value: { setAt }   with a per-key TTL = the step deadline       # timeout backstop (one per instance; linear interpreter)
```
- `cursor` = current step index; `pendingToken` = the `taskId | requestId` of the step being awaited
  (§10.6); `status ∈ {running, complete, failed}`.
- **Definitions bind at instance start.** `instance.<instanceId>.pattern` is written **in the same
  `AtomicBatch`** that creates `instance.<instanceId>` (both `CreateOnly`) and is the pinned copy
  every step resolution (advance, completion, deadline recovery) reads — **never** the live pattern
  source. A pattern update mid-flight therefore affects **new instances only**: an in-flight
  instance's `cursor` always indexes into the definition it was created against, so reordering,
  inserting, or changing steps in the live definition cannot mis-index a running instance. The pin
  is deleted in the **same terminal batch** that flips `status` to `complete`/`failed` — the
  instance record itself persists, but listing `instance.*.pattern` keys yields exactly the **live**
  instance set, which is the second leg of the §10.9 per-domain consumer reconcile (current
  definitions ∪ pinned definitions of live instances), letting an in-flight instance survive its
  pattern being removed/updated-away. A missing pin for a `status=running` instance is an invariant
  break (the pin is written atomically with the instance), surfaced as an operator-visible failed
  terminal — never a silent fallback to the live source. Disaster recovery (total `loom-state`
  loss → fresh `StartLoomPattern`) re-binds to the **current** definition; this is re-convergence
  under today's truth, not a regression of the pin (see `docs/components/loom.md`).
- The `pendingToken → instance` correlation is a **durable co-located reverse index** (the `token.`
  pointer), resolved by a **direct GET** on completion — **not** an in-memory index. This is
  **multi-instance-safe**: any engine replica resolves any token via the bucket.
- Each step transition is a **single `substrate.AtomicBatch` on `loom-state`** (all ops target the one
  bucket — `internal/substrate/batch.go`): update `instance.<id>` (`cursor`, `pendingToken`), write the
  new `token.<newToken> → instanceId`, delete the prior `token.<oldToken>`, **write the
  `outbox.<newToken>` record**, and **re-arm `deadline.<instanceId>`** (a PUT with a fresh per-step TTL).
  All-or-nothing — the same construct the Processor uses for the mutation-batch + tracker at commit
  step 8. **The op submission is part of this atomic fact (the `outbox.` record), not a second write** —
  this is the **command-outbox** pattern, symmetric to the Processor's transactional *event* outbox.

#### Command outbox — `outbox.<token>` + the relay

A rejected/lost op submission must not be a dual write (state committed, op never sent). Loom writes the
**op to submit** as an `outbox.<token>` record **in the same batch** as the cursor/token transition; an
async **relay** — a durable consumer on the `loom-state` backing stream filtered to `outbox.>`
(mirroring `internal/processor/outbox/consumer.go`) — **fire-and-forget publishes** the op to
`core-operations` and **deletes `outbox.<token>` on publish-ack**. Re-publish is idempotent (Loom chose
the `requestId`; a duplicate collapses on the Contract #4 `vtx.op.<requestId>` tracker), so a crash
between batch and publish self-heals on resume. The relay needs only a publish — **no request-reply** —
so `internal/loom` carries no raw `nats.io`/`jetstream` handle. The §10.9 lifecycle ops route through the
same outbox.

#### Deadline — `deadline.<instanceId>` (per-instance, TTL-armed)

`deadline.<instanceId>` carries a **per-key TTL** = the current step's deadline; its **expiry** is the
off-stream failed/rejected backstop (§10.6). It is keyed on **`instanceId`** (not the token) because the
interpreter is linear (§10.5) — exactly one step is pending per instance — so one key always denotes the
current step's clock, and the TTL **expiry marker's subject carries the instanceId** (a delete-marker
carries no old value, so a `token.`-keyed TTL would lose the reverse mapping). Lifecycle: **created** in
the submit-step-0 batch; **re-armed** (PUT, fresh TTL) in each advance batch; **deleted** in the terminal
(`complete`/`fail`) batch; or **auto-expires** → the loom-state CDC observes the expiry marker
(`KeyValuePurge` / `Nats-Marker-Reason: MaxAge`, distinct from a normal DEL) → the step-deadline-exceeded
handler runs (§10.6). The value is thin (`setAt`, observability only) — the handler reconstructs from
`instance.<instanceId>`.
- **"No secondary KV index" is reinterpreted:** it forbids a **separate index bucket** (dual-write
  atomicity / drift); a co-located disjoint-prefix index in the *same* bucket, written in the same
  atomic batch, is sanctioned and stronger.
- **Provisioning (binding):** `loom-state` **must** be provisioned with **`AllowAtomicPublish: true`**
  on its underlying stream — the same flag `core-kv` gets. Today `enableAtomicPublish` is gated to
  `CoreKVBucket` only (`internal/bootstrap/primordial.go`); extend it to `loom-state` (alongside the
  existing bucket-create + the `verify-kernel` assertion). Without it, `Conn.AtomicBatch` on
  `loom-state` is rejected.
- **Rebuildability (D3)** no longer rests on a startup index rebuild: the durable `token.` pointer is a
  single atomic fact written write-ahead, so any replica correlates any completion by direct GET. The
  **write-ahead** and **guardless-step-token-only** invariants in **§10.6 "Crash-safety invariants"**
  still bind; the former watch-suspended-until-rebuild invariant is retired (no in-memory index).

### `weaver-state` — anti-storm in-flight mark (§10.8)

```
key:   <targetId>.<entityId>.<gapColumn>     # entity ID, not the dotted full key (§10.2)
value: { targetId, entityKey, gap, action, claimId?, claimedAt, leaseExpiresAt, heldBy? }
```
- Set via **KV create (CAS-on-absent) = the OCC** (§10.8).
- **Lease enforcement is BOTH passive and active:** the mark is written with a **NATS per-key TTL**
  (the bucket is provisioned TTL-capable) **and** an **active reconciler** sweeps for reclaim. The
  per-key TTL is the backstop — a missing/dead reconciler can therefore **never wedge a gap forever**
  (the key self-expires); the reconciler is for prompt reclaim. The lease is set **≫ expected
  remediation latency** so expiry means "presumed dead."
- **The per-key TTL is `2 × lease`, not a literal mirror of `leaseExpiresAt`** (`markTTLBackstopFactor`,
  a constant). `leaseExpiresAt` mirrors the **lease** (`claimedAt + lease`); the TTL is the lease's
  **dead-reconciler backstop**, strictly longer. The sweep can only *reclaim* a lease while the mark
  still exists, and the mark is the sweep's only evidence (it enumerates marks, not rows) — so the key
  must outlive `leaseExpiresAt` to give the interval-cadence sweep a full lease-width window to observe
  and re-attempt. With TTL == lease the key self-deletes the instant it becomes reclaimable and the
  reclaim clause is mechanically unreachable; `2 × lease` is the smallest factor that satisfies both
  the *never-wedge* (TTL) and *re-attempt* (sweep) clauses together. `SweepInterval` is clamped
  ≤ lease so at least one sweep pass lands inside the lease-to-TTL window.
- **Mark-clearing is level-reconciled, not edge-triggered** (§10.8): on each watch update **and** each
  reconciler sweep, Weaver compares the **current** row's `missing_<col>` against existing marks for
  that `<targetId>.<entityId>` and deletes any mark whose column is now `false` — it does **not** rely
  on catching the transitional flip (a coalescing watch can drop edges). `claimedAt` tags the
  episode so a stale mark from a prior closed episode can't shadow a fresh re-open.
- **Re-fire after lease expiry — consumer-enforced idempotency by deterministic open-episode identity.**
  A userTask reclaim is keyed by the **open-episode identity**: the mark's `claimId` (a fresh NanoID
  minted at the mark's CAS-create, **preserved verbatim** across every reclaim-`replace`) seeds the
  dispatched artifact's id — `assignTask`'s `taskId` and `triggerLoom`'s Loom `instanceId`. Weaver
  re-publishes the dispatch **without** a producer-side existence check (a Weaver GET would race the
  publish→commit propagation lag — inside that window it sees absent and re-publishes anyway, so it
  cannot *prevent* a double; only the consumer, committing against real state, can). The **consumer** is
  the single idempotency authority:
  - **`assignTask` → `CreateTask`:** the task vertex lives in **Core KV**, so the `CreateTask` Starlark
    script reads the task key via **`kv.Read()`** (§2.5 lazy on-demand read — *not* a `contextHint`
    read, which would fatal-`HydrationMiss` on the legitimately-absent key) and branches: present **and
    alive** (`task != None and not task.isDeleted`) → empty mutations **and** empty events (a coherent
    silent no-op); absent **or** logically-deleted → create as normal. The existing `CreateOnly` mutation
    is the narrow concurrent-dispatch backstop.
  - **`triggerLoom` → `StartLoomPattern`:** the Loom instance lives in **loom-state** (no Core-KV
    vertex), so the dedup is at **Loom**, not a Processor read — `StartLoomPattern` carries the stable
    `claimId`-seeded `instanceId` on `loom.patternStarted`, and Loom's instance presence check +
    `createInstance` `CreateOnly` collapse a re-emitted trigger onto the existing instance (no new
    instance, hence no new userTask). This dedups the whole pattern — the correct altitude for
    `triggerLoom`.

  A legitimate close→reopen — or a §10.8 planned-**leg** boundary (ratified Andrew 2026-07-05) —
  mints a new mark ⇒ new `claimId` ⇒ a fresh artifact; an out-of-band deletion
  self-heals (hard-tombstone ⇒ `kv.Read()` `None` ⇒ create; logical delete ⇒ present-but-`isDeleted`
  ⇒ create). This **supersedes** the prior "accepted rare double / check-before-act = Phase-3 hardening"
  disposition for the two human userTask actions. **External gaps are unchanged** — their reclaim
  re-dispatch is *intended* (re-call a dead vendor / mint a fresh service instance), episode-scoped on
  `markRevision` and bounded by `inflight_<g>` + `maxretries_<g>`; `directOp` likewise. *(The
  `nudge`-specific `claimId` clauses were retired 2026-06-18, 13.1; `claimId` now regains a producer —
  the mark CAS-create — and a consumer — the userTask id derivation. `claimId?` stays optional in the
  value shape only so reads tolerate a legacy pre-`claimId` mark mid-migration; new marks always carry
  it.)* **Migration bound:** a userTask gap that is **already in flight at deploy** carries a
  pre-`claimId` mark (`claimId==""`); its first post-deploy reclaim derives a stable empty-seed id that
  differs from the id the original (pre-deploy) dispatch used, so it may create **one** duplicate
  artifact — bounded, one-time, and self-healing (every later reclaim reuses the empty-seed id and
  collapses). A drain of open human-task gaps before deploy avoids even that one. **`triggerLoom`
  self-heal is bounded by Loom's instance lifecycle, not a tombstone read:** if the Loom instance has
  reached a terminal state, a re-emitted `patternStarted` is dropped (no re-create) — unlike the
  Core-KV task, whose hard/logical delete self-heals via `kv.Read`. A still-open gap whose instance
  terminated is resolved by level-reconciled mark-clearing, not by re-triggering the pattern.
- `entityKey` carries the full `vtx.<type>.<id>` (doc-is-truth); the key holds only the ID.

#### Reserved (non-mark) `weaver-state` key shapes

The mark shape above shares the bucket with reserved engine keys. All are structurally disjoint from
marks: `entityId`s are NanoIDs (`substrate.Alphabet` contains no underscore) and gap columns carry the
`missing_` prefix, so a reserved `__`-token can never collide with a mark segment. The reconciler sweep
skips all three (never enumerated as `CorruptMark`).

| Key shape | Role |
|---|---|
| `<targetId>.__control` | *(as-built since FR30 — documented here per the 2026-07-02 arch-review reconciliation)* durable dispatch-disable marker; authority for the control plane's `disable`/`enable`/`revoke` remediation-skip (`docs/components/weaver.md`). |
| `<targetId>.<entityId>.<gapColumn>.__count` | *(as-built — same reconciliation)* the retry-budget dispatch-count bounded by the lens's `maxretries_<g>` column; incremented on both dispatch legs, deleted on gap-close, long-TTL orphan backstop. |
| `<targetId>.__effect.<gapColumn>.<actionRef>` | **Ratified 2026-07-04 (planner extension — see §10.8); build-pending (Fire 2).** Per-(gap, action) effect bookkeeping: dispatch/close counters over a sliding window of the last K episodes (K config-tunable, default 20; event-keyed ring in the value, no clock sampling). Written on the two real dispatch legs and the level-reconciled gap-close path; GC'd by the sweep's orphan legs when the target/gap/action leaves the registry. |

### `weaver-claims` — RETIRED (Amended 2026-06-18 — 13.1, External I/O Bridge)

The Two-Phase Nudge claim record and the in-Weaver **Claim → Execute → Resolve** protocol are
**retired**. External idempotent I/O moves out of Weaver (convergence *detection*) into **Loom + the
bridge** (deterministic *execution*) — see `cmd/loom`'s §10.5/§10.6 `externalTask` step and
`docs/components/bridge.md`. The bucket, its primordial constant + provisioning, and the **two**
kernel-verify enumerations (`scripts/verify-kernel.go`, `internal/bootstrap/verify.go`) are removed in
the bridge epic's nudge-teardown story — **move-then-delete** (the `Fake*` adapters relocate to the
bridge first, so there is never a window where neither external path works; full teardown only after
the convergence e2e is green).

**Why it was retired:** the resolve op **could not address a candidate entity distinct from the nudge
`subject`** — the resolve-op payload was hard-coded `{ claimId, result, expectedRevision }` with
`authTarget = np.subject`, and a Starlark DDL op cannot read `authContext`
(`internal/processor/starlark_runner.go` binds only `{ requestId, lane, operationType, actor,
submittedAt, payload }`), so the DDL that should record the result had **no channel** to learn which
vertex (candidate ≠ subject) to write. The reference vertical surfaced this structural defect.

**What replaces it (FR58 / NFR-S11 preserved, more honestly):** the **claim vertex in Core KV** created
by the `externalTask`'s `instanceOp` **before** the `external.*` event is even publishable **is** the
visible claim (its **type is package-chosen** — the lease demo uses `service.<x>.instance`; the bridge
is **type-agnostic**). The claim, the resolve target, and the result holder unify into **one auditable
business vertex** with a natural idempotency key (one instance = one call). The external **outcome is
recorded as aspect(s)** on that vertex per **D5** (business data lives in aspects; the vertex root `data`
stays minimal — at most a justified lifecycle scalar), never fat root `data`. Idempotency on redelivery
rests on the **deterministic result-op `requestId`** (below) + the adapter's own `idempotencyKey` dedup
— **not** on the bridge reading any typed vertex.

**Hard invariant (FR58 determinism — pinned):** the bridge's result-op **`requestId` MUST be
`deterministic(idempotencyKey = instanceKey)`**, so a redelivered `external.*` event produces the
**same** result-op requestId, which collapses on the Contract #4 `vtx.op.<requestId>` tracker
(`internal/processor/step2_dedup.go`) → **exactly one** result mutation. This is the event-plane analog
of the §10.4 deterministic-`requestId` rule for the fired-timer→op path (and of the retired Weaver
resolve op's own `deriveResolveRequestID`).

### `weaver-work` — deferred (no durable bucket in Phase 2)

`weaver-work` was the **single normalized intake** for Weaver's 3 trigger lanes (violation /
event-targeted-audit / temporal) feeding one Evaluator→Strategist pipeline. In Phase 2 each **live**
lane's durability already lives in its **source**: lane-1 replays from `weaver-targets` (the violating
row persists), the temporal lane replays from the **`core-schedules` stream** (§10.4, durable
consumer), and dedup is in `weaver-state`. A separate durable queue would be redundant. The one lane
that genuinely needs `weaver-work` — **lane-2 (event-targeted-audit)**, whose trigger is a *transient*
core-event — is **Phase-3-deferred**. So Phase 2 treats `weaver-work` as an **in-process lane
multiplexer**; the durable bucket + work-item shape land when lane-2 does.

---


## 10.4 Message scheduling — platform-wide (ADR-51) — **FROZEN 2026-06-02**

> **Corrected 2026-06-05 (Story 7.4 implementation finding).** The fired-message **target subject
> must lie within `schedule.>`**: the NATS scheduler republishes the fired payload **back into the
> `core-schedules` stream** at the target subject and validates that target against the stream's own
> subjects, rejecting an out-of-stream target at publish time (`JSMessageSchedulesTargetInvalidError`).
> The earlier example target (`weaver.timer.fired.<…>`, outside `schedule.>`) was therefore wrong and
> is corrected below. Components consume their fired messages via a **JetStream consumer filtered on
> their target-subject prefix**. The shape is otherwise unchanged (`core-schedules` /
> `AllowMsgSchedules: true` / subject root `schedule.>`).

Message scheduling is a **platform-wide capability**, not Weaver-specific — same status as Health
KV. It is bootstrapped as core infra and usable by any component (Weaver's temporal lane is the
first consumer; the bridge's async-result lane — re-poll + give-up timeout for long-running external
calls (§10.6) — is the second; the recurring **platform sweep** (`@every`, below) is the third).
Op-vertex / tracker **retention** is **not** a schedule-lane consumer: idempotency trackers expire by
**NATS per-key TTL** (Contract #4 §4.3) and the events-outbox aspect is **tombstoned by the outbox
consumer on confirmed publish** — so the historically-anticipated "op-vertex pruner" (#49) has no
residual class to prune and is retired (see `implementation-artifacts/recurring-schedules-and-op-vertex-pruner-design.md`).

```
stream:            core-schedules             # platform-bootstrapped, AllowMsgSchedules: true
                                              #   (core-* family, like core-operations / core-events)
schedule subject:  schedule.<domain>.<kind>.<token...>    # publish here; one schedule per subject
                                              #   (bare-word subject root, like ops.> / events.>)
                                              #   <token...> = publisher-chosen dot-free token(s)
                                              #   e.g. Weaver uses  schedule.weaver.timer.<targetId>.<entityId>
header:            @at <RFC3339>   (absolute; or @every for recurring — Phase 2 uses @at one-shot)
                   Nats-Schedule-Target: <target subject>   # republish target (must be within schedule.>)
target subject:    schedule.<component>.fired.<token...>    # publisher-chosen, but MUST be within schedule.>
                                              #   e.g. Weaver uses  schedule.weaver.timer.fired.<targetId>.<entityId>
                                              #   (the scheduler republishes back into core-schedules here)
```

- **Naming:** stream `core-schedules` (dash-form, no project name — matches `core-operations` /
  `core-events`); subject root `schedule.>` (matches `ops.>` / `events.>`).
- **The segments after `schedule.<domain>.<kind>.` are publisher-chosen, dot-free tokens** within the
  `schedule.>` space — a publisher MAY key its schedules with more than one entity token. Weaver keys
  per **target AND entity** (`schedule.weaver.timer.<targetId>.<entityId>`, fired
  `schedule.weaver.timer.fired.<targetId>.<entityId>`), so two targets projecting a `freshUntil` for
  the same entity hold **independent timer slots** instead of colliding on the shared
  `MaxMsgsPerSubject: 1` rollup (without the `<targetId>` token the later projection would silently
  overwrite the earlier deadline). Each token is a **NanoID, not the dotted vertex key** (same
  discipline as §10.2/§10.3 — dots are subject-token separators); the full entity key, if needed,
  rides the **message payload**, not the subject.
- **The bridge's async-result lane (§10.6, Phase 3)** is the second consumer: it keys per claim
  handle — `schedule.bridge.poll.<handle>` (re-poll a still-pending external call) and
  `schedule.bridge.timeout.<handle>` (the per-claim give-up deadline), fired
  `schedule.bridge.poll.fired.<handle>` / `schedule.bridge.timeout.fired.<handle>` — and consumes its
  firings via a JetStream consumer filtered on `schedule.bridge.>`. Same frozen shape as Weaver's timer
  lane: a second publisher-chosen namespace within `schedule.>`, no change to the rules above.
- **`core-schedules` is NEW** — it **joins the primordial stream create list** (scheduling bootstrap
  story), alongside `core-operations`/`core-events`; `AllowMsgSchedules: true` is set at provisioning.
  (It does not exist yet — same "new, joins the create list" status as `loom-state` in §10.3.)
- The **stream** is shared/platform-wide; the **target (fired) subject** is chosen per publisher,
  so each component consumes only its own fired messages — but it **must lie within `schedule.>`**.
  When the timer fires, the NATS scheduler republishes the payload **back into `core-schedules`** at
  the target subject (an out-of-stream target is rejected at publish time). Each component consumes
  its fired messages via a **JetStream consumer filtered on its target-subject prefix** (e.g.
  `schedule.weaver.timer.fired.>`).
- Per-subject schedule → re-scheduling **replaces** the prior timer (one schedule per subject; for
  Weaver, per `<targetId>.<entityId>`).
- Durable across restart. The fired message hits the publisher's target subject; that component
  converts it to a normal **op** via the Processor — it is **never** published to `core-events`
  directly (the transactional outbox, Contract #3 / Story 1.5.10, remains the sole event producer).
- **Fired-timer → op is dedup'd.** JetStream delivery is at-least-once (a consumer crash before ack
  redelivers), so the converted op carries a **deterministic `requestId`** derived from the schedule
  subject (`schedule.<domain>.<kind>.<token...>` + fire instant) → Contract #4's `vtx.op.<requestId>`
  tracker collapses redeliveries. A redelivered timer does **not** double-act.

### Recurring schedules (`@every` / cron) — NATS 2.14 (the platform floor)

Recurring schedules need NATS 2.14, which **is the platform floor** (`go.mod` / `docker-compose.yml` pin
`nats:2.14`; Contract #4 §4.3) — so no version gate applies. `@at` (one-shot, available since NATS 2.12)
and `@every <duration>` / 6-field cron (recurring, NATS 2.14) share the
same lane, headers, subject discipline, and dedup rule. The recurring form differs only in lifecycle:

- **The schedule message persists and re-fires indefinitely.** For `@every`/cron the scheduler keeps the
  schedule message at its subject and republishes a fresh copy to the fired target on **every** interval
  (a one-shot `@at` auto-purges after its single delivery). Per-subject rollup still holds
  (`MaxMsgsPerSubject: 1`, `Nats-Rollup: sub` auto-applied) — **one active schedule per subject**.
- **Re-publishing the same subject REPLACES the prior schedule** (retune the cadence); **cancellation** =
  purge the schedule subject, delete the schedule message by sequence, or the atomic
  `Nats-Schedule-Next: purge` conditional stop. There is no implicit expiry — a recurring schedule runs
  until removed (a publisher that arms one owns stopping it; idempotent re-arm on restart is the norm).
- **Per-occurrence dedup extends the one-shot rule verbatim.** Each occurrence's converted op carries a
  **deterministic `requestId`** derived from the schedule subject **+ the occurrence instant** (the fired
  message's stored timestamp, or `Nats-Schedule-Next`), so an at-least-once redelivery of the *same*
  occurrence collapses on the Contract #4 tracker while a *new* occurrence is genuinely new work. A
  fire that drives a level-reconcile **handler** (not an op — e.g. a recurring sweep) is idempotent by
  the handler's own construction and needs no tracker.
- **The fired copy carries `Nats-Scheduler`** (the schedule subject that produced it) **and
  `Nats-Schedule-Next`** (the next-invocation instant). Past-due `@every` ticks after a restart fire
  immediately (catch-up); the scheduler coalesces overdue ticks rather than replaying each missed one.
- **First recurring consumer:** the platform recurring **sweep** — Weaver's reconciler sweep runs from a
  durable `@every` schedule (`schedule.weaver.sweep` → `schedule.weaver.sweep.fired`) rather than an
  in-process ticker, giving single-fire-across-replicas + an operator-visible, retunable cadence (the
  #47 "replaces cron" intent). Design:
  `implementation-artifacts/recurring-schedules-and-op-vertex-pruner-design.md`.

---


## 10.7 Ephemeral task grants — authorization (existing FR56 mechanism; cypher re-sourced)

A task assignment authorizes its assignee to perform the granted op **on the task's specific
target** via FR56 (Contract #6 §6.6, `internal/processor/step3_auth_capability.go`). Phase 2 **adds
no new auth surface and does not change the grant *matching logic*** — it relocates the grant
projection to a package-owned lens + disjoint key (a1 extraction), and link-sources the grant fields.

- The grant projection moves to a **`orchestration-base`-owned `capabilityEphemeral` lens** writing
  the disjoint key **`cap.ephemeral.<actor-suffix>`** (Contract #6 §6.6 Phase-2 amendment). It walks,
  per actor, `(identity)<-[:assignedTo]-(task)` (direct + `reportsTo` 2-hop for manager delegation),
  each grant = `{ source, taskKey, operationType, target, expiresAt }`. **Link-sourced:**
  `operationType` ← walk `task-[:forOperation]->(op)`; `target` ← walk `task-[:scopedTo]->(t)`;
  `expiresAt` ← `task.data.expiresAt` (scalar). *(Was: `task.data.grantedOperationType`/`targetKey`
  fields read by the bootstrap god-cypher — the corrected anti-pattern.)* **Bootstrap `capability`
  cypher drops its `task` OPTIONAL MATCHes** → core stops referencing the `task` package type.
- The op the assignee performs declares **`authContext.{task, target}`**. Step-3's `task` dispatch
  path (`matchEphemeralGrant`) authorizes iff a grant matches **`taskKey` ∧ `operationType` ∧
  `target` ∧ `expiresAt > now`** — **matching logic unchanged**; only the **source key** moves to
  `cap.ephemeral.<actor>` (read on the task branch — a **single GET, no fallback**: a no-match denies
  with `AuthContextMismatch`, which the denial builder emits without `actorRoles`, so no `cap.<actor>`
  second read is needed).
- **Subject-scoping is intrinsic** (`g.Target == ac.Target`): a leasing manager with many open
  `ApproveLeaseApplication` tasks is authorized for each *specific* lease application, never blanket.
- **No `fulfillsTask` field, no `taskGated` flag, no Contract #2 change.** Code touches: a new
  `capabilityEphemeral` lens in `orchestration-base`; bootstrap `lenses.go` loses its task matches;
  step-3 task branch reads the new key; migrate any field-shaped task fixtures + update the §6.6
  conformance test. The grant *field shape* (`EphemeralGrant`) is unchanged.

> Task **completion** (§10.6, resolved) rides on this auth: a successful op authorized via
> `authContext.task = T` **auto-completes T** in the same atomic batch (commit-path injected,
> emitting `TaskCompleted(T)`). Standalone `CompleteTask` is admin-only; `CancelTask` is the
> not-needed path.

---

