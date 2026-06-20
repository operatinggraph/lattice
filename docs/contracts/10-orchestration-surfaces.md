# Contract #10 — Orchestration Surfaces (Loom / Weaver)

**Status: Phase 2 — FROZEN (2026-06-02).** Authored in the Phase 2 architecture sprint
(2026-06-01); hardened DESIGN→frozen across the 2026-06-02 data-contracts session (Loom side +
all four Weaver sections). Rationale: `lattice-architecture.md` → "Phase 2 Architecture —
Orchestration Core". Component detail: `docs/components/{loom,weaver}.md`.

This contract defines the data shapes the orchestration engines introduce. All sections (§10.1–§10.9)
are frozen — implementation stories build to these shapes; changes require a contract revision, not an
in-flight redefinition. **Known deferred carries** (do NOT reopen the frozen shapes — they extend them
later): shared pure-Starlark guard evaluator (until the first Starlark guard is authored, §10.5);
platform `scope: specific` in step-3 (§10.8 `triggerLoom` external callers, Phase 3); `weaver-work`
durable bucket (lane-2 / Phase 3, §10.3).

---

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
lnk.task.<id>.assignedTo.identity.<assigneeId>   # who performs it (existing §6.9 convention)
lnk.task.<id>.forOperation.meta.<opId>           # the operation this task grants (relationship → link)
lnk.task.<id>.scopedTo.<type>.<targetId>         # the target the grant is scoped to (often ≠ assignee)
```
(All three links: task = later-arriving **source**, the other vertex pre-exists = **target**, per Contract #1 §1.1.)

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
- **No-orphan invariant (FR29 by construction):** `CreateTask` **requires** an `assignee` and
  commits the `task --assignedTo--> identity` link atomically with the vertex; `CreateTask` and
  `ReAssignTask` Starlark **validate the assignee identity and reject** (structured `ScriptError`)
  if invalid — a task pointing at a non-existent identity is never committed. (Link direction per
  Contract #1 §1.1: the task is the later-arriving source side, the pre-existing identity is the
  target — the `assignedTo` name reads from the source side.) Tombstoning/merging
  an identity that holds open tasks is rejected (operator reassigns/cancels first). So a task
  always has a valid assignee; there is no unassigned/orphaned state to monitor.
- **Phase 3:** FR28 (role-queue + fallback) is deferred; when it lands, a role-queue with no
  eligible actor *is* unroutable and the FR29 Health-KV monitor returns for that case.

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

## 10.2 Weaver target Lens output (D4) — **FROZEN 2026-06-02** (amended 2026-06-18, 13.1)

One row **per candidate entity**, carrying a `violating` flag — **not** row-only-when-violating
(avoids Refractor retraction). Projected by the existing `nats_kv` adapter.

**Bucket — one shared, primordial, dash-named bucket** (NATS KV bucket names are stream tokens:
`[A-Za-z0-9_-]+`, **no dots**; cf. `core-kv` / `weaver-state` in `primordial.go`). All convergence
targets project into the single `weaver-targets` bucket under a disjoint `<targetId>.` key prefix —
the **same contract-contribution pattern as capability-kv** (§6.1): the bucket is core-owned/primordial,
packages project their target rows into it, no per-install bucket provisioning. (`weaver-targets` is
**NEW — joins the primordial bucket create list**, like `loom-state` §10.3.) Unlike capability-kv's
core-fixed prefixes, `<targetId>` is package-authored, so **`targetId` uniqueness across installed
targets is install-validated** (§10.8) — two packages must not collide in the shared bucket.

**Key on the entity *ID*, not the full vertex key.** A candidate entity is **always a vertex** (never
an aspect — aspects surface only as gap predicates / param columns *within* a vertex-candidate row), so
its key is always `vtx.<type>.<id>`. The dotted full key must **not** be embedded in the NATS KV key
(its dots are subject-token separators → brittle). Within a `<targetId>.` partition every candidate is
the same type, so the type segment is redundant: the entity segment is just the **NanoID**. The full
key lives in the document (`entityKey`) — document, not key, is the source of truth (standing principle).

```
bucket:  weaver-targets                              # shared, primordial
key:     <targetId>.<entityId>                       # e.g. leaseApplicationComplete.Lk2Pn6mQrtwzKbcXvP3T
value:   {
           "entityKey":   "vtx.leaseApp.<id>",       # echo of the candidate vertex key
           "violating":   true,                      # lens-projected; Weaver lane-1 watch filter
           "missing_onboarding": true,               # gap columns: missing_<gap> (snake_case bool)
           "missing_bgcheck":    false,
           "missing_payment":    true,
           "missing_signature":  true,
           "applicant":   "vtx.identity.<id>",       # param column(s) — §10.8 templates row.<field>
           "projectedAt": "2026-05-12T14:32:18.142Z" # deterministic as-of (Contract #6 semantics)
         }
```

**Convergence lens as an `actorAggregate` (Amended 2026-06-18 — 13.1, External I/O Bridge).** A
convergence target whose row must reproject on a change to a *linked* constituent — e.g. a leaseApp
that reads identity aspects **and** a service-instance vertex **across links**
(`MATCH (app)-[:applicationFor]->(id), (id)<-[:providedTo]-(inst:service)`) — MAY be projected by an
**`actorAggregate`** lens (Refractor Output descriptor, `projectionKind: "actorAggregate"`) instead of
the plain `nats_kv` projection (which reprojects only its own anchor vertex and would miss a linked
constituent flipping). **The §10.2 key shape is unchanged** (Option (b) at ratification): such a lens
declares an explicit **key column** (the bare-NanoID `<entityId>`) that the actorAggregate `BuildKey`
emits **instead of** its default `{actorSuffix}` (= `<type>.<id>`), so the row key stays
`<targetId>.<entityId>` (bare NanoID) and Weaver's `splitRowKey` accepts it unchanged. The frozen
§10.2 key + `splitRowKey` stay frozen; the change is localized to the Refractor Output-descriptor
machinery Epic 12 introduced.

**Watch.** Weaver does a **filtered watch `<targetId>.>`** per target it manages (discovering each
target's id from the `meta.weaverTarget` registry, §10.8). Row-per-candidate (incl. non-violating)
means Weaver watches all rows under its prefix and **acts only on `violating == true`** (lane 1).

**Column conventions (the §10.2↔§10.8 contract seam):**
- `entityKey` — echo of the candidate vertex key (the value mirrors the key, as the cap-doc echoes
  `key`/`actor`).
- `violating` — **lens-projected** bool; the Lens decides what counts as needing convergence (it is
  *not* an implicit OR of the gaps). This is Weaver's lane-1 dispatch filter.
- gap columns **`missing_<gap>`** — snake_case bools. **The §10.8 `gaps` map keys bind *exactly* to
  these column names.** The Strategist's gap-detection = scan keys with the `missing_` prefix whose
  value is `true`.
- **param columns** (free-form, e.g. `applicant`) — whatever the §10.8 playbook templates reference
  (`row.<field>`); the Lens **must project every column the playbook templates name**.
- **`freshUntil`** (optional, engine-recognized convention) — an RFC3339 instant the target cypher
  computes as `resolve + window`. The engine converts it into an `@at` schedule (the time→op temporal
  lane, §10.4) and **never computes the window itself** — the freshness rule lives in the cypher, the
  engine only turns the projected deadline into a timer. A free-form param column by storage, named by
  convention so the engine/Lens seam is explicit.
- `projectedAt` — deterministic as-of provenance, **same semantics as Contract #6 §6.3** (the
  candidate's `lastModifiedAt`, not a wall-clock read). The NATS KV entry's own revision arrives free
  on each watch update, so it is **not** projected into the value.

**No read-path authz anchor here.** The `weaver-targets` bucket is internal operational state read
only by Weaver (a bootstrap-provisioned service actor); it is never on the read-path, and read-path
auth is Phase-3-deferred (D1). The scoping the remediation needs is carried by the **param columns**
above, and each remediation op the Actuator submits carries its own `authContext`. *If* a target Lens
is **also** projected to the Phase-3 Postgres read-path, it carries the D1 authz anchor **there** like
any protected Lens — orthogonal to this bucket.

**Retraction (per D4, settled).** Gap closes → `violating` / `missing_*` flip via **upsert**. True
entity deletion → row deleted (`IsDeleted` path). **Deferred:** true emit-only-when-violating requires
Refractor negative/filter-retraction projection. Freshness rules live in the **target cypher**
(`missing_bgcheck = NOT EXISTS(check WHERE date > now − window)`).

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
- **Re-fire after lease expiry — idempotency by action:** `triggerLoom` / `assignTask` re-fire is
  **accepted as a rare double** (lease ≫ remediation latency makes it rare; Loom guard-idempotency
  limits damage, and a duplicate task is operator-visible) — **documented bound, not a silent risk**;
  the robust check-before-act variant is a Phase-3 hardening. *(The `nudge`-specific `claimId`
  clauses — `claimId` minted atomically with the CAS-create, and "`nudge` is safe via `claimId`" —
  retired 2026-06-18, 13.1: `nudge` is gone (§10.8) and external idempotency is now the
  service-instance key on the bridge path. `claimId?` in the value shape above is left optional but
  has no remaining producer.)*
- `entityKey` carries the full `vtx.<type>.<id>` (doc-is-truth); the key holds only the ID.

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
calls (§10.6) — is the second; op-vertex pruner / retention are future consumers).

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

---

## 10.5 Loom pattern definition (package data)

A `meta.loomPattern` meta-vertex (loaded via CDC like a Lens def). A pattern declares a single
**`subjectType`** — the vertex the instance runs for; the trigger op supplies the subject id.
Guards and step operations are relative to the subject.

**Starting an instance** is the op **`StartLoomPattern{ patternRef, subjectKey }`** (`subjectKey`
must be a vertex of `subjectType`). Authorization is per-pattern via **`authContext.target =
vtx.meta.loomPattern.<patternId>`** + capability scope (Weaver = `scope: any`; external/per-pattern =
`scope: specific` / task grant) — full contract in **§10.8 "`triggerLoom` authorization"**. This is
the pattern-*start* auth (distinct from the per-step auth of §10.6/§10.7).

```
{
  "patternId":   "onboarding",
  "subjectType": "identity",
  "completionDomains": ["orchestration"],
  "steps": [
    { "kind": "userTask", "operation": "SetName",
      "guard": { "absent": "subject.profile.data.name" } },
    { "kind": "userTask", "operation": "SetPhone",
      "guard": { "absent": "subject.profile.data.phone" } },
    { "kind": "userTask", "operation": "SetAddress" }
  ]
}
```

**`completionDomains?: ["<domain>", …]`** (optional) — the set of `events.<domain>.>` the engine
reconciles a **durable per-domain consumer** for (D2). A **domain** is the **first segment of an event
class** — the `<domain>` in `events.<domain>.>`. Every event class is `<domain>.<eventName>`
(Contract #3 §3.4, validated at commit step 7), so this model is **true codebase-wide**, not
illustrative: e.g. class `identity.created` → domain `identity`, class `orchestration.taskCompleted` →
domain `orchestration`; `loom-<domain>` is always a valid durable name. **Defaults to `[subjectType]`**
when omitted (covers the common same-domain flow). A flow whose steps complete in a domain other than
the subject's **must list it explicitly**; the §10.6 per-step completion **deadline** is the not-silent
backstop for an omitted/mis-declared domain (FR29 never-silently-drop). The engine reads
`completionDomains` — it does not *know* domains; per-step granularity is unnecessary because
correlation is domain-independent (§10.6), so the **set** of domains is sufficient.

**A userTask completes on the `orchestration` domain.** A userTask step completes via the
`orchestration.taskCompleted` event (the §10.6 commit-path auto-complete), regardless of the subject's
type — so an all-userTask onboarding pattern over `identity` subjects declares
`completionDomains: ["orchestration"]` (NOT `["identity"]`, which would reconcile a `loom-identity`
consumer that never sees the completion). A pattern mixing userTask + systemOp steps lists every domain
it completes on.

**Step shape:** `{ kind, operation, guard? }` for `userTask`/`systemOp` — completion is implicit
(§10.6), no per-step event. The `externalTask` kind (below) is **two-op-shaped** and carries its own
fields `{ kind, adapter, params, replyOp, instanceOp }`.
- `kind` ∈ `userTask` (engine creates a task with links `assignedTo` → the subject,
  `forOperation` → the step's op, `scopedTo` → **the subject** — a Loom `userTask` scopes its grant
  to the instance subject; the frozen step shape carries no separate target field; UI renders from
  the op's self-describing DDL via the `forOperation` link) | `systemOp` (engine submits the op
  directly) | **`externalTask`** (engine submits the `instanceOp`, then parks awaiting the external
  result — see below).
- **Linear only** — no branches/loops/fan-out. A compound *path* is a Weaver signal. The
  `externalTask`'s two ops (submit-instanceOp → park) are **one logical step**, not a branch/fan-out.

**`externalTask` (Amended 2026-06-18 — 13.1, External I/O Bridge).** A step that dispatches an idempotent
external call and **waits for its result** — symmetric to a `userTask` (dispatch to an async completer,
then park; the completer is a human for userTask, the bridge for externalTask). Shape:

```
{ "kind": "externalTask", "adapter": "<name>", "params": { ... }, "replyOp": "<ResolveOp>", "instanceOp": "<CreateInstanceOp>" }
```

- The engine submits the **`instanceOp`**, whose DDL (a) creates the **claim vertex** (Core KV business
  state — the FR58 "visible claim before the call", §10.3; its **type is package-chosen** — the lease
  demo uses `service.<x>.instance`, but the bridge is **type-agnostic**) and (b) emits the
  `external.<adapter>` event via **that op's transactional outbox**. The `external` domain is
  **ordinary** — no Contract #3 change (the open `<domain>.<eventName>` model, no Processor allowlist);
  the `instanceOp` DDL declares + emits the event-type as package data. The bridge
  (`docs/components/bridge.md`) consumes `events.external.>`, calls the adapter idempotently, and posts
  `replyOp` back.
- The engine **then PARKS** on `token.<instanceKey>` (§10.6) — the instance key it **mints write-ahead**
  and passes to `instanceOp` as a caller-supplied id (exactly as it supplies `CreateTask`'s
  deterministic `taskId` write-ahead, §10.6 invariant 1).
- `adapter` is the external adapter name; `params` are row/subject templates resolved per the
  §10.5/§10.8 templating rule; `replyOp` is the result-op type the bridge posts back (carrying
  `payload.externalRef = instanceKey`, §10.6) — **its DDL records the external outcome as aspect(s) on
  the claim vertex** (**D5**: business data lives in aspects; the vertex root `data` stays minimal — at
  most a justified lifecycle scalar such as `status`), **not** as fat root `data`; `instanceOp` is the
  DDL op that mints the claim vertex + emits the event.
- **Loom stays pure:** the event rides the **`instanceOp`'s transactional outbox** (the op Loom submits
  through the command-outbox relay), **not** a Loom-held NATS handle — the `internal/loom`
  substrate-only boundary is unchanged.
- **Completion is symmetric to a userTask** (amended 2026-06-18). Besides recording the outcome
  aspect(s), the `replyOp` DDL **emits `orchestration.externalTaskCompleted` carrying
  `payload.externalRef = instanceKey`** — the uniform orchestration-domain completion signal Loom
  correlates on, the analog of `orchestration.taskCompleted{taskKey}` for a userTask (§10.6). An
  externalTask pattern therefore declares **`completionDomains: ["orchestration"]`** (exactly like an
  all-userTask pattern), and Loom's existing `loom-orchestration` consumer advances it. The event is
  **emitted by the purpose-built `replyOp`** rather than platform-injected the way `taskCompleted` is for
  a userTask's *oblivious* bound op — the emission mechanism differs to match the completer (a deliberate
  result op vs an ordinary business op), the outcome (an orchestration-domain completion correlated by a
  token) is identical. The wait for that event is **unbounded** once the `instanceOp` commits (§10.6),
  exactly as a userTask's human wait is unbounded once its task vertex exists.

**Async resolution (Amended 2026-06-19 — Phase 3, async external-reply).** The bridge's adapter call
MAY resolve **asynchronously**: a real vendor returns a *pending reference* on submit and the true
result lands later (webhook or status-poll, minutes–days). This rides the **already-unbounded wait**
above with no change to the completion model — the bridge posts `replyOp` (hence
`externalTaskCompleted`) whenever resolution arrives. While pending, the bridge records an interim
**pending marker** — a package-chosen aspect on the claim vertex written via a package `dispatch` op,
analogous to the `.outcome` aspect (D5) — and drives **re-poll + a give-up timeout** through its §10.4
schedule lane (`schedule.bridge.>`). A timeout posts a terminal `replyOp` with a `failed` status, so a
never-answered call **converges** rather than parking forever. The §10.6 step `deadline.<instanceId>`
TTL — the off-stream backstop — is sized to the external SLA (per-adapter) and the bridge's give-up
timeout fires **before** it: the normal path is a clean bridge-posted outcome, and the Loom deadline
catches only a genuinely dead bridge. A synchronous adapter (today's fakes) is unchanged — it resolves
inline and posts `replyOp` immediately, writing no pending marker.

**Guards — pure predicate over the subject's current state.** Absent guard = step always runs.

- **Paths are explicit** (consistent with the 1.5.9 explicit-aspect-navigation principle):
  `subject.<aspect>.data.<field>` (aspect) or `subject.data.<field>` (root). Guards read **only
  the subject + its aspects** — no link-walking (a guard that needs related state is a Weaver
  signal). At step-entry the engine JIT-hydrates the subject (root + referenced aspects) and
  resolves the path with the same `resolveProperty`/aspect-navigation the Refractor executor uses.
- **Declarative grammar (default):** atoms `{absent: <path>}`, `{present: <path>}`,
  `{equals: {path, value}}`, composable with `{allOf|anyOf|not: [...]}` (still one boolean — NOT
  branching). **Pinned semantics (binding, removes ambiguity):** `absent` = the path resolves to
  **null, missing, a soft-deleted aspect, OR (for strings) empty-after-trim**; `present` = not
  absent. An empty-string-after-trim is **absent**; `"0"`/`false`/`0` are **present**.
- **Starlark escape hatch (reserved):** for a predicate the grammar can't express, a guard may be
  `{ "reads": ["<aspect>", ...], "starlark": "def guard(subject): return ..." }` — evaluated by
  the **same verified-pure sandbox** the Processor uses (`Load` nil; no I/O / env / NATS;
  deterministic — confirmed in `starlark_runner.go`). `reads` is the read-hint (which subject
  aspects to hydrate), answering the input-parameter question; the function gets `subject` exactly
  as a script gets `state`, returns a bool. The shared pure-evaluator extraction lands **only when
  the first Starlark guard is authored** (deferred until needed; declarative-only ships without it).
- Either way a guard is **pure declarative data or a pure function** → the instance cursor is
  rebuildable by replaying guards (no side effects, deterministic).

Patterns + step→operation bindings + guards are package data; the engine is a generic interpreter.
**How a step's completion is detected and correlated to its instance → §10.6.**

---

## 10.6 Step completion & instance correlation

A step is correlated to its instance by a **unique token Loom already knows or the completion
event already carries** — concurrent-safe (multiple instances per subject, or many tasks of one
op-type per actor, are unambiguous), with **no topological guessing**.

**Correlation is a durable `token.<token>` GET** (§10.3), **domain-independent**: a consumed
`core-events` message on *any* subscribed domain whose body `requestId`/`taskId` matches a live
`token.` pointer is the **committed** terminal → advance via the atomic batch. The per-domain consumer
only decides *which events Loom sees* (the partition, §10.5 `completionDomains`), never *which instance*
— that is the pointer. **Idempotency** (at-least-once redelivery): the `token.` pointer's **presence is
the guard** — pointer gone (step already advanced, pointer deleted in the batch) → drop/ack, no
re-advance.

| Step kind | Pending token (in `loom-state`) | Completion signal Loom consumes |
|-----------|----------------------------------|----------------------------------|
| **userTask** | the **`taskKey`** (`vtx.task.<id>`) of the task it created | `orchestration.taskCompleted` core-event → **`payload.taskKey`** → live `token.<taskKey>` GET → instance |
| **systemOp** | the **`requestId`** of the op it submitted | a committed business event on a subscribed domain whose top-level `requestId` matches a live `token.<requestId>` → advance via the atomic batch. **failed/rejected** is **off-stream** (a rejected op writes no tracker/event) — learned via the **per-step deadline + a read-before-act probe** (below), never the submit reply → `status=failed` / `retryCount` per policy; the deadline also backstops a mis-declared `completionDomains` (§10.5) → alert, never a silent wedge |
| **externalTask** *(13.1, 2026-06-18; deadline+completion amended 2026-06-18)* | the **`instanceKey`** — an **opaque bare-NanoID handle** Loom mints write-ahead and passes to `instanceOp` as a caller-supplied id (the `instanceOp` DDL prepends its package-chosen type to form the `vtx.<type>.<handle>` claim-vertex key; the engine stays type-agnostic) | **`orchestration.externalTaskCompleted`** core-event → **`payload.externalRef`** → live `token.<instanceKey>` GET → instance — **symmetric to userTask's `orchestration.taskCompleted` → `payload.taskKey`** (emitted by the `replyOp` DDL; see §10.5). The deadline backstops the **`instanceOp` submission only**: once the `instanceOp` commits it **disarms**, and the wait for the bridge's `replyOp` is **unbounded** (like the post-creation human wait) — it **never advances the cursor**. A rejected/lost `instanceOp` → `FailPattern` (the **creation-deadline + instanceOp-tracker probe**, below) |

All event business fields ride the Event envelope's **`payload`** object (Contract #3 §3.4), so Loom's
**three** structural correlation keys are **top-level `requestId`** (systemOp), **`payload.taskKey`**
(userTask), and **`payload.externalRef`** (externalTask — *Amended 2026-06-18, 13.1*). Loom stays
domain-ignorant — it tries each field against the durable token store (`token.<requestId>`,
`token.<taskKey>`, `token.<externalRef>`) and **at most one live pointer resolves** — the one for the
current pending step. Disjointness is **by field + by the single live pointer**, not by the key's type
segment: `externalRef` is the **opaque bare-NanoID handle** Loom minted (the `instanceOp` DDL forms the
`vtx.<type>.<handle>` claim-vertex key *from* it; the handle itself carries no type), so it needs no
fixed shape. The `externalTask`'s write-ahead handle is that **instance key Loom mints** — it does not
own the bridge's later result-op `requestId`, so it parks on a handle it controls and the bridge echoes
it back as `payload.externalRef`.

### systemOp terminals — committed on-stream, failed/rejected off-stream (deadline + probe)

A submitted systemOp has three orthogonal outcomes; separating them is what removes the wedge:

- **committed** — a `core-events` body `requestId` matches a live `token.` pointer → advance. (on-stream)
- **crash / transient** — **not a terminal**: the command-outbox relay re-publishes and the durable
  consumers resume from their ack floor. The outbox owns crash-recovery; the deadline does not.
- **rejected / failed / unseen** — **off-stream** (a rejected op writes no tracker and emits no event,
  Processor denies before commit step 8), learned via the **per-step `deadline.<instanceId>` TTL**
  (§10.3). The synchronous `ops.<lane>` submit-reply is **not** used — it blocks the consumer and forces
  a raw NATS handle into the engine.

**Step-deadline-exceeded handler.** When `deadline.<instanceId>` expires (the loom-state CDC observes
the `KeyValuePurge`/MaxAge marker; or the reconciler fallback detects an overdue instance), the handler
for instance `I`:

1. **GET `instance.<I>`.** Absent or `status != running` → **ack/no-op** (already terminal, or a stale
   marker). Re-reading current state — never acting on the marker alone — is the idempotency +
   multi-replica guard.
2. Let `T = instance.pendingToken`. **Read-before-act probe: GET the Contract #4 op tracker `vtx.op.<T>`**
   (a Core-KV *read* — Loom reads, never writes Core KV; symmetric to the bridge's read-before-act
   recovery on the service-instance vertex, §10.3):
   - **tracker present** → the op committed; its completion event was missed (mis-declared
     `completionDomains` / lost) → **advance** exactly as the committed terminal would, **and alert**
     ("completion recovered via deadline probe — check `completionDomains`"). Flow stays live.
   - **tracker absent, `outbox.<T>` present** → the relay has not delivered yet → **re-arm**
     `deadline.<I>` (fresh TTL); do **not** fail.
   - **tracker absent, `outbox.<T>` absent** → published but did not commit → **rejected** → per
     `retryCount` policy re-submit (fresh `outbox.<T>` + re-arm) or `status=failed` (atomic batch also
     deletes `token.<T>` + `deadline.<I>`) → submit `FailPattern` (§10.9). **Alert.**
3. Every branch re-reads `instance` and is CAS-on-`running`, so a redelivered marker / second replica
   finds the work done → no-op.

The deadline is set **≫ expected op latency** (the `weaver-state` lease precedent); a late commit after a
false-fail finds the pointer gone → dropped (a bounded, alerted divergence, not a silent one).

### userTask creation path — bounded creation-deadline + task-vertex probe

A userTask step is **two waits in sequence**: a **bounded** wait for the task to be *created* (a machine
action — `CreateTask` commits in milliseconds), then an **unbounded** wait for the human to act on it.
The deadline+probe above covers the *systemOp* step; the userTask **creation** wait gets the analogous
backstop so a rejected/lost `CreateTask` (e.g. the subject identity is dead → `CreateTask`'s no-orphan
validation rejects it, or a taskId collision) fails the instance instead of parking `token.<taskKey>`
forever (the silent wedge §10.6 forbids).

- A userTask step arms a **bounded creation-deadline** (`CreateTaskTimeout`, sized ≫ any `CreateTask`
  commit latency — **not** a human-response window).
- When it fires, a read-before-act probe GETs the task vertex **`vtx.task.<taskId>`** from Core KV (a
  Loom *read*, like the systemOp tracker GET):
  - **present** → the task was created; the flow is now in the legitimate **unbounded human wait** →
    **disarm** the deadline (cursor/token untouched) and stop. The human may take days — there is no
    further runtime timeout.
  - **absent** → probe the `CreateTask` op's Contract #4 tracker / `outbox` record exactly like the
    systemOp path (tracker present → committed-but-raced → re-arm; outbox present → relay not yet
    delivered → re-arm; neither → `CreateTask` **rejected/lost** → `FailPattern` + alert).
- Every branch is CAS-on-`running`, mirroring the systemOp handler. Loom only **reads** Core KV here;
  the module boundary (substrate-only) is unchanged.

**Honest nuance:** after the creation-deadline disarms (the task vertex exists), there is **no runtime
timeout** on the human wait — so a *mis-declared userTask `completionDomains`* (one that omits the
`orchestration` domain) is caught by a **load-time warn** when the pattern is loaded, not by a runtime
backstop. The warn is loud; the pattern is not rejected (a future userTask completion domain could
differ).

### externalTask creation path — bounded creation-deadline + instanceOp-tracker probe (amended 2026-06-18)

An externalTask step is **two waits in sequence** — exactly like a userTask, with the bridge in the
human's role: a **bounded** wait for the `instanceOp` to *commit* (the machine action that creates the
claim vertex + emits the `external.<adapter>` event), then an **unbounded** wait for the bridge to post
the `replyOp`. It is therefore handled like the **userTask** creation path, **not** like a systemOp: the
deadline backstops the *submission*, never the external round-trip, and it **must not advance the
cursor**. (This corrects the original "exactly like a systemOp" framing, under which a committed-but-not-
yet-replied `instanceOp` would wrongly advance the flow before the external result landed.)

- An externalTask step arms a **bounded creation-deadline** (the `CreateTaskTimeout` machine-action
  bound, sized ≫ `instanceOp` commit latency — **not** an external-call window).
- When it fires, a read-before-act probe GETs the **`instanceOp`'s Contract #4 tracker
  `vtx.op.<opRequestId>`** (the `opRequestId` Loom derived for the `instanceOp` — a Loom *read*, like the
  systemOp/userTask probes; Loom cannot read the claim vertex itself, whose type is package-chosen, so it
  probes the op tracker it owns):
  - **tracker present** → the `instanceOp` committed: the claim vertex exists and the `external.<adapter>`
    event was emitted, so the bridge will (eventually, at-least-once + idempotent) reply → **disarm** the
    deadline (cursor/token untouched) and stop. The bridge wait is now **unbounded** — no further runtime
    timeout (the bridge's durability is the guarantee; a dead bridge surfaces on the **bridge's own**
    Contract #5 Health, not a per-instance Loom timeout). **The cursor advances only on
    `orchestration.externalTaskCompleted` — never on this probe.**
  - **tracker absent, `outbox.<opRequestId>` present** → the relay has not delivered the `instanceOp` yet
    → **re-arm**; do not fail.
  - **tracker absent, `outbox.<opRequestId>` absent** → the `instanceOp` was **rejected/lost** (the
    external call will never happen) → `status=failed` (the atomic batch also deletes `token.<instanceKey>`
    + `deadline.<I>`) → submit `FailPattern` (§10.9). **Alert.** (FR29 — the submission is never a silent
    wedge.)
- Every branch is CAS-on-`running`, mirroring the systemOp/userTask handlers. Loom only **reads** Core KV
  here; the substrate-only boundary is unchanged.

**Honest nuance (same as userTask):** after the creation-deadline disarms, there is **no runtime timeout**
on the bridge wait — a *mis-declared externalTask `completionDomains`* (one that omits `orchestration`) is
caught by a **load-time warn** (the analog of the userTask warn), not a runtime backstop; a permanently-
dead bridge is closed out-of-band via `FailPattern` (the `CancelTask` analog). This deliberate, observable
async-wait is what both async-completer step kinds (userTask, externalTask) accept — distinct from the
**systemOp** deadline, which *does* advance on a tracker-present probe because for a systemOp the op's own
commit **is** the completion.

### Completing a userTask — by `taskKey`, via `orchestration.taskCompleted` (RESOLVED)

A task is closed by **`taskKey`** (`vtx.task.<id>`; never by inferring actor+op-type — a manager may
hold many open tasks of one op-type for different targets). Completion emits
`orchestration.taskCompleted` carrying **`payload.taskKey`**; Loom correlates `payload.taskKey →
instance` via a live `token.<taskKey>` GET. No new envelope field, no Contract #2 change — the op
already carries `authContext.task` for §10.7 auth.

- **Primary path — auto-complete on the authorizing op's commit.** A task exists to authorize +
  track exactly one op (`forOperation`) on one target (`scopedTo`); performing that op **is**
  fulfilling the task. So when an op authorized via `authContext.task = T` commits successfully, the
  **commit path injects T's completion** (`status → complete` + `orchestration.taskCompleted{taskKey:
  T}`) into the **same atomic batch** — platform-injected, like provenance, in the same code path that
  already matched the grant at step-3. Atomic, no "did-the-op-but-task-still-open" wedge, no per-op
  script coupling.
  - **The injection is conditional on `status == open` (read-and-CAS within the same batch).** This
    closes the race with admin `CompleteTask`/`CancelTask`: if T was already completed, the second
    flip is a **no-op** (no double `orchestration.taskCompleted`); if T was **cancelled**, auto-complete
    must **not** resurrect it (the CAS-on-`open` fails → the op still commits, but T stays `cancelled`
    and emits no completion event). This also bounds the stale-grant window (the cap-lens projection
    lags the status flip, so a just-closed task can still authorize via the stale projection — the CAS
    makes that commit's auto-complete a harmless no-op rather than a double-act).
  - **`orchestration.taskCompleted` consumption at Loom is idempotent** (JetStream is at-least-once): a
    redelivered completion for an already-advanced instance is dropped, not re-advanced.
- **`CompleteTask(taskKey)`** — retained only as an explicit admin / out-of-band completion path.
- **`CancelTask(taskKey)`** — for a task that is no longer needed (e.g. its target was withdrawn);
  distinct from completion.

Loom watches `orchestration.taskCompleted` regardless of which path emitted it.

**The engine supplies the task id (write-ahead requires it).** Crash-safety invariant 1 requires the
`token.<taskKey>` pointer be written **before** the side effect (`CreateTask`), so Loom must know the
`taskKey` ahead of commit. `CreateTask` therefore accepts an **optional caller-supplied `taskId`**
(present → used verbatim; absent → minted internally, so admin/manual callers are unaffected). The
engine derives a deterministic `taskId` from `(instanceId, cursor)` and passes it, making the `taskKey`
(`vtx.task.<taskId>`) known write-ahead. A crash-retry re-submits the **same** `CreateTask` and
collapses on the Contract #4 `vtx.op.<requestId>` tracker — no duplicate task. The `task` DDL is package
data (not a frozen contract); the grant/auth path (§10.7) is unchanged.

### Why this needs NO frozen-contract change

- **systemOp** correlation watches the tracker keyed by the `requestId` Loom itself chose.
- **userTask** correlation watches the generic `orchestration.taskCompleted` event, which carries the
  `taskKey` under `payload` intrinsically.
- Authorization reuses the existing `authContext.{task,target}` + `ephemeralGrants` (§10.7).

### Constraint

`loom-state` maps `{taskKey | requestId} → instance` via the durable co-located `token.<token>`
pointer (§10.3), resolved by direct GET; the instance records its single pending token. Because tokens
are unique per pending step, no one-active-instance-per-subject restriction is needed — concurrent
instances for the same subject are fully distinguishable, and any engine replica resolves any token.

### Crash-safety invariants (binding — "rebuildable" depends on these)

D3 calls the cursor "rebuildable," but rebuildability only holds if these orderings are mandated
(they are contract invariants, **not** Loom-story latitude):

1. **Write-ahead = the atomic batch (retained, now outbox-inclusive).** The `token.<token>` pointer, the
   `instance.<id>` update, **the `outbox.<token>` op record**, and the `deadline.<instanceId>` TTL are
   persisted to `loom-state` in **one `substrate.AtomicBatch`**. For a systemOp the side effect (the op
   reaching `core-operations`) is the **relay's** decoupled, idempotent publish *from* that batch — so
   the batch and the side effect are **no longer a dual write**: invariant 1 holds by construction, not
   by ordering discipline. (For a userTask the side effect is still `CreateTask`, keyed/idempotent — the
   engine supplies the deterministic `taskId` so the `token.<taskKey>` is known write-ahead.) On
   restart, a persisted `pendingToken` whose `outbox.` record still exists is simply re-published by the
   relay; one whose op already committed collapses on the Contract #4 `vtx.op.<requestId>` tracker. A
   crash can no longer orphan a token between side effect and persist.
2. **Guardless steps complete only via their token (retained).** A step with no guard has **no
   guard-replay signal** (guard-replay can't tell a guardless step ran). So a guardless step's
   completion comes **solely** from its `pendingToken` (taskId/requestId); re-drive must **not** re-run
   a step whose token is still pending, or it double-submits. (The §10.5 example ends on a guardless
   `SetAddress`.)
3. **(REMOVED) Completion watch suspended until rebuild.** There is no in-memory index to rebuild — a
   redelivered completion resolves against the durable `token.<token>` pointer (§10.3) regardless of
   engine age or replica. The durable per-domain consumer redelivers from its ack floor, and the
   pointer's presence is the idempotency guard, so no suspend-until-warm gate is needed.

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

## 10.8 Weaver target + playbook (package data) — **FROZEN 2026-06-02** (amended 2026-06-18, 13.1)

A `meta.weaverTarget` meta-vertex bundles the **detection** (violation Lens, §10.2) and the
**remediation** (gap → action playbook). CDC-loaded like `meta.lens` / `meta.loomPattern`; Weaver
reconciles **one filtered watch (`weaver-targets` `<targetId>.>`) per target**.

```
meta.weaverTarget {
  "targetId": "leaseApplicationComplete",
  "lensRef":  "<meta.lens id of the violation Lens (§10.2 output)>",
  "gaps": {
    "missing_onboarding": { "action": "triggerLoom",  "pattern": "onboarding",
                            "subject": "row.applicant" },
    "missing_bgcheck":    { "action": "triggerLoom",  "pattern": "backgroundCheck",
                            "subject": "row.applicant" },
    "missing_payment":    { "action": "triggerLoom",  "pattern": "collectPayment",
                            "subject": "row.applicant" },
    "missing_signature":  { "action": "assignTask",   "operation": "SignLease",
                            "assignee": "row.applicant", "target": "row.entityKey" }
  }
}
```

### The §10.2 ↔ §10.8 binding (the detection↔remediation seam)

- **`targetId` is the single binding token:** it is *both* this vertex's id *and* the `weaver-targets`
  key prefix the `lensRef`'d Lens projects rows under (`<targetId>.<entityId>`). They must match, and
  **`targetId` is install-validated unique** across installed targets (the bucket is shared — a
  collision would interleave two targets' rows; same install-time check class as the `gaps`-key rule below).
- **Every `gaps` key MUST be a `missing_<gap>` column** produced by the §10.2 Lens. Install-time
  validation: each `gaps` key matches the `missing_` convention. The Strategist detects gaps by
  scanning the row's keys with the `missing_` prefix whose value is `true`.
- **A row column `missing_*: true` with no `gaps[col]` entry is a config error → alert**, never
  silently skipped (FR29 "never silently drop" discipline). Weaver surfaces it to Health KV.

### Action contracts

Every action's params are resolved per row (templating below). The Actuator submits ops under
**Weaver's bootstrap-provisioned service-actor authority**.

| `action` | params | effect |
|----------|--------|--------|
| `triggerLoom` | `{ pattern, subject }` | submit `StartLoomPattern{ patternRef: pattern, subjectKey: subject }` → Loom (§10.5). `subject` must resolve to a vertex of the pattern's `subjectType`. **Auth: see below.** Also the path for **external remediation** (since 2026-06-18, 13.1): `triggerLoom` a pattern whose body is an `externalTask` (§10.5) — this **replaces the retired `nudge` action**. |
| `assignTask` | `{ operation, assignee, target }` | `CreateTask` (§10.1): `assignedTo`→`assignee`, `forOperation`→`operation`, `scopedTo`→`target`. |
| `directOp` | `{ operation, target?, params? }` | submit `operation` directly as a remediation op. |

> **`nudge` — RETIRED (Amended 2026-06-18 — 13.1, External I/O Bridge).** The `nudge` GapAction (and the
> `operation` field added to it in Story 10.2) is removed: external I/O moves out of Weaver (convergence
> *detection*) into **Loom + the bridge** (deterministic *execution*). Weaver's job collapses to **detect
> → `triggerLoom`**; it no longer dispatches or resolves external calls. External remediation is now
> `triggerLoom` of a pattern containing an `externalTask` (§10.5/§10.6), and the FR58 claim/idempotency
> guarantee is carried by the service-instance vertex on the bridge path (§10.3 `weaver-claims` retirement
> note). Weaver retains `triggerLoom` / `assignTask` / `directOp`.

### Templating

A param value is **either a literal** (`pattern: "onboarding"`) **or the token `row.<column>`**
(`subject: "row.applicant"`) — no expressions. The Strategist substitutes `row.<column>` with that
column's value from the violation row. A `row.<column>` that resolves null/absent is a **data error**
— surface, do not fire a malformed remediation. (This is why §10.2 requires the Lens to **project
every column the playbook templates name**.)

### `triggerLoom` authorization — `StartLoomPattern` + pattern-as-target

Starting a Loom instance is the op `StartLoomPattern` carrying **`authContext.target =
vtx.meta.loomPattern.<patternId>`** (the pattern definition vertex). Per-pattern authorization then
falls out of the existing capability scope model (Contract #6 §6.7), with **no per-pattern op type**:

- **Weaver** holds `StartLoomPattern @ scope: any` (seeded in `orchestration-base`) → may start any
  pattern. This is the only caller Phase 2 needs.
- **External / per-pattern callers** would use `scope: specific` (allowed-pattern-target list) or a
  task-scoped ephemeral grant (§10.7). **Phase-3 carry:** step-3's `matchPlatformPermission` currently
  **actively DENIES** platform `scope: specific` (returns `AuthContextMismatch`, "not implemented" —
  it is not a silent pass; Contract #6 §6.7). So **do not seed an external `scope: specific`
  `StartLoomPattern` grant in Phase 2** expecting it to authorize — it won't. The *mechanism* is specced
  now; only `scope: any` (Weaver) is **implemented and exercised** in Phase 2.

This also fills a Loom gap: §10.5/§10.6/§10.7 settled auth for the *steps within* a pattern
(userTask→ephemeral grant; systemOp→engine authority) but not the pattern *start* — `StartLoomPattern`
+ pattern-as-target is that contract.

### Flow & anti-storm

Lane-1 sees a `violating` row → for **every** currently-true `missing_*` gap **not already
in-flight**, the Strategist looks up `gaps[col]` and the Actuator executes:

- **In-flight mark** in `weaver-state`, keyed **`<targetId>.<entityId>.<gapColumn>`** (entity *ID*,
  not the dotted full key — §10.2). Set via **KV create (CAS-on-absent)** — *that* create **is** the
  anti-storm OCC: concurrent evaluations of the same gap race the create, the loser drops, the winner
  dispatches. Value shape (incl. TTL/lease, full `entityKey`) freezes in §10.3.
- **Mark clears** on **gap-close** or **lease expiry** — both **level-reconciled, not edge-triggered**
  (§10.3 weaver-state): on each watch update and reconciler sweep, Weaver compares the **current** row's
  `missing_<col>` against existing marks and deletes any whose column is now `false` (a coalescing watch
  can drop the transitional flip, so Weaver must not depend on *seeing* it). Lease expiry is enforced by
  a **NATS per-key TTL + active reconciler** (§10.3) — a dead reconciler can't wedge a gap forever.
  Async remediations (Loom — incl. an `externalTask`'s external call via the bridge) close their gap
  when their downstream work lands and the Lens re-projects `false`; `claimedAt` tags the episode so a
  stale prior-episode mark can't shadow a re-open. **Re-fire idempotency by action** is pinned in §10.3
  (`triggerLoom` / `assignTask` = documented rare-double; an `externalTask` external call dedups on the
  **deterministic** bridge result-op `requestId`, §10.3 `weaver-claims` retirement note).
- **Gaps fire in parallel** — independent remediations run concurrently.
- **Gap *dependencies* are encoded in the target Lens predicates, not in Weaver.** If bgcheck needs
  onboarding first, the Lens makes `missing_bgcheck` true only once onboarding is done
  (`missing_bgcheck = onboarded AND NOT EXISTS(recent check)`). A dependent gap simply isn't `true`
  until its prerequisite closes, so parallel firing is always safe. Weaver stays a generic parallel
  dispatcher; ordering is declarative.

Target + playbook are **package data**; the Weaver engine is a generic dispatcher.

---

## 10.9 Pattern trigger & lifecycle — `loom`-domain ops

§10.5/§10.8 settle the *auth* to start a pattern (`StartLoomPattern` + pattern-as-target) but not how a
**committed** trigger reaches the engine, nor how a pattern's terminal is announced. This section closes
both on the **event plane**, with no Core-KV instance state.

**Instance is operational-only (binding).** A Loom instance is **operational state** — it lives **only
in `loom-state`** (P1, the `instance.<instanceId>` cursor, §10.3) and gets **no Core-KV business
vertex**. Its lifecycle is announced on the **event plane** (`core-events`), **not** projected as
Core-KV business state. These ops emit their `loom.*` events the ordinary way: at commit the faithful
`EventList` is persisted as the **outbox aspect `vtx.op.<requestId>.events`** — alongside the universal
`vtx.op.<requestId>` tracker, in the same step-8 atomic batch — and the outbox CDC consumer publishes
from that aspect (`internal/processor/outbox/consumer.go`, filter `$KV.<bucket>.vtx.op.*.events`). So
each writes the **standard tracker + outbox-events aspect**; the distinguishing property is only that it
creates **no business-domain vertex** — the instance's sole durable home is the `loom-state` cursor.

**Three lifecycle ops** (shipped by `orchestration-base`; the engine stays generic), each → outbox →
`events.loom.*` (**P2: never a direct publish**):

| Op | Posted by | Business vertex | Emits (body: `instanceId, patternRef, subjectKey, requestId`) |
|----|-----------|-----------------|------|
| `StartLoomPattern{patternRef, subjectKey}` | **caller** (Weaver `scope:any` / client / fixture) | none | `loom.patternStarted` |
| `CompletePattern{instanceId}` | **Loom** (`identity:loom`) | none | `loom.patternCompleted` |
| `FailPattern{instanceId, reason?}` | **Loom** (`identity:loom`) | none | `loom.patternFailed` |

(Each also writes the universal `vtx.op.<requestId>` tracker + the `…events` outbox aspect — that is how
the event is emitted; none writes a business vertex.)

- **`instanceId` = the `StartLoomPattern` `requestId`** (already a NanoID) — no minting, and redelivery
  dedup is automatic (Loom's `loom-state instance.<instanceId>` cursor keyed on it → already present →
  skip). The instance's sole durable home is that cursor (§10.3).
- Loom runs a **fixed durable consumer on `events.loom.patternStarted`** (always-on, **independent of
  `completionDomains`**). On the event: validate `patternRef` against the loaded pattern registry, create
  the `loom-state instance.<instanceId>` cursor, submit step 0.
- The engine's **internal** completion/failure is a **`loom-state` status transition** (operational,
  `status ∈ {running, complete, failed}`); the `CompletePattern`/`FailPattern` op is the *outward
  announcement* (loop closure + nesting), the terminal Actuator op of an exhausted/failed pattern.
- **Idempotency needs no new machinery:** `StartLoomPattern`'s Contract #4 tracker dedups a duplicate
  trigger op at the Processor; Loom dedups at-least-once event redelivery on the `instanceId` (the
  `loom-state` cursor presence).
- **`loom` is a first-class domain:** Loom *consumes* `patternStarted` (trigger) and *emits*
  `patternCompleted`/`patternFailed`. A Loom completion is therefore itself a consumable completion
  event — so a Phase-3 **nested** pattern (a step that runs a sub-flow and waits) simply lists `loom`
  in its `completionDomains` (§10.5) and correlates on the sub-instance's token, with **no new
  machinery**.
- **Queryability** ("which flows are running") is served by **Loom's control plane** — analogous to
  Refractor's (`internal/refractor/control/service.go`), reading `loom-state` — **not** Core KV. It is
  its own (future) control-plane story; Weaver gets the analogous one (Story 9.4 control-API). A
  Refractor lens over the `loom.*` event stream remains an option for a durable read model if one is
  later wanted.

**No special Processor capability needed.** Event emission already rides the outbox aspect
(`vtx.op.<requestId>.events`) written in the commit batch, so a lifecycle op is an ordinary op that
emits events and writes no business vertex — nothing in the pipeline special-cases it. (An op whose
`result.Mutations` is empty but whose `result.Events` is non-empty still commits the tracker + the
`…events` aspect and publishes; confirm no upstream guard rejects an empty *business*-mutation set.)

---

## Revision history

| Date | Change |
|------|--------|
| 2026-06-01 | Created (Phase 2 design) — task placement, target-Lens output, operational KV namespaces, ADR-51 subjects, Loom pattern shape. |
| 2026-06-02 | Data-contracts session (Loom). Guard grammar: explicit subject-paths, pinned `absent`=null/missing/soft-deleted/empty-after-trim, declarative atoms+combinators + reserved verified-pure-Starlark escape hatch. Step shape `{kind, operation, guard?}` (dropped `completionEvent`). §10.6 completion/correlation (taskId / requestId tokens). §10.8 Weaver target+playbook drafted. |
| 2026-06-02 | **Auth realignment (Andrew).** Verified FR56 task-auth already exists, subject-scoped (`matchEphemeralGrant`: taskKey+operationType+target+expiresAt). Dropped the invented `fulfillsTask`/`taskGated`; op uses existing `authContext.{task,target}`; **Capability KV doc-shape + step-3 unchanged**. Task completion (CompleteTask vs auto-complete) left to implementation session. |
| 2026-06-02 | **Task completion RESOLVED** (was left "finalize later" — shouldn't have been). Auto-complete is primary: an op authorized via `authContext.task=T` auto-completes T in the same atomic batch (commit-path injected, `TaskCompleted(T)`); `CompleteTask` admin-only, `CancelTask` not-needed. Loom/tasks now fully settled (§10.1/§10.5/§10.6/§10.7). |
| 2026-06-02 | **Links-not-fields correction (Andrew).** No brainstorming exception exists for storing relationships as task fields; the Phase-1 `task.data.grantedOperationType`/`targetKey` reads in `lenses.go` are an anti-pattern. Task root `data` = scalars only `{status, expiresAt}`; **operation + target are LINKS** (`forOperation` → op meta, `scopedTo` → target; plus existing `assignedTo`). Capability KV *projected* `ephemeralGrants` shape is unchanged (flattening is correct in a read model); only the cap-lens **cypher** is re-sourced to walk the links → a small **Phase-1 hardening** (`lenses.go` + field-shaped task fixtures migration). |
| 2026-06-03 | **§10.1 — dropped speculative `presentation` + `params` task aspects (Andrew).** `presentation` duplicated the bound op's self-describing DDL (the canonical render source, §10.5); `params` had no producer in the frozen §10.5 step shape `{kind,operation,guard?}`. Generic `task` is now **scalars + links only, no aspects** (UI renders from `forOperation`→op DDL; instance specifics from `scopedTo`/`assignedTo`). No migration cost (introduced in the Phase-2 draft; nothing depended on them). Epics 7 overview + Story 7.1 AC updated. |
| 2026-06-03 | **Scoped pre-implementation review applied (Winston coherence + Quinn crash-safety).** Clarifications within the frozen shapes (no shape changed — all use existing fields); FROZEN status holds. **Coherence:** dropped the dead "roles-fallback-on-denial" claim (task-path denial = `AuthContextMismatch`, carries no `actorRoles`) → §10.1/§10.7/§6.6 task path is a single GET; noted `core-schedules`/`weaver-targets` are NEW (join the primordial create lists, like `loom-state`); `targetId` install-validated unique (shared bucket); Loom `userTask` `scopedTo` = the subject; `scope:specific` reconciled across §6.4/§6.7/§10.8 (platform-path is a deny-stub, Phase-3). **Crash-safety:** auto-complete is **CAS-on-`status==open`** (no double `TaskCompleted`, never resurrects a cancelled task; `TaskCompleted` consumption idempotent) §10.1/§10.6; Loom **crash-safety invariants** pinned (write-ahead `pendingToken`, guardless-step token-only completion, watch-suspended-until-rebuild) §10.6; systemOp correlation watches **both** terminals §10.6; `claimId` minted **atomically with the CAS-create** + reconciler reuses it & reads-`resolveRef`-before-re-execute (no double charge) §10.3; lease enforced by **NATS per-key TTL + active reconciler** (no wedge); mark-clearing **level-reconciled** not edge-triggered §10.3/§10.8; non-nudge re-fire = documented rare-double (D-i(a)); temporal fired-timer→op carries a **deterministic `requestId`** (dedup at-least-once) §10.4. |
| 2026-06-02 | **§10.4 FROZEN + §10 flipped DESIGN→FROZEN (Andrew).** Scheduling confirmed. **Renamed** off the project-name prefix (no resource is named after the project): stream `lattice-schedules`→**`core-schedules`** (matches `core-operations`/`core-events`), subject root `lattice.schedule.*`→**`schedule.<domain>.<kind>.<entityId>`** (matches `ops.>`/`events.>`); `<entityId>` = NanoID, full key in payload (entity-ID discipline, §10.2/§10.3). With §10.4 done, all §10.1–§10.8 are frozen; doc header flipped. Deferred carries noted (don't reopen frozen shapes): shared Starlark evaluator, platform `scope:specific`, `weaver-work` durable bucket. |
| 2026-06-02 | **§10.3 FROZEN (Andrew).** Operational KV namespaces. Bucket names fixed to dash-form (`loom-state`/`weaver-state`/`weaver-claims`; latter two exist primordially, `loom-state` joins the create list). `loom-state` key `<instanceId>`, value `{instanceId,patternRef,subjectKey,cursor,pendingToken,status,retryCount}`; token→instance correlation = in-memory index rebuilt from persisted `pendingToken` (no secondary KV index). `weaver-state` key `<targetId>.<entityId>.<gapColumn>`, value `{targetId,entityKey,gap,action,claimId?,claimedAt,leaseExpiresAt,heldBy?}` (CAS-create=OCC; clears on gap-close/lease-expiry; `claimId` only for nudge). `weaver-claims` key `<claimId>`, value `{claimId,adapter,operation,subject,params,idempotencyKey,state,claimedAt,resolvedAt?,resolveRef?}` — **external idempotency = `idempotencyKey`(=claimId) the adapter dedups on**, no CAS on claim (weaver-state already serialized dispatch); Claim→Execute→Resolve per arch Item 3; 90d retention. **`weaver-work` DEFERRED** (Andrew): its purpose = normalized intake for the 3 trigger lanes + durability; but Phase-2 live lanes already replay from their sources (lane-1 from `weaver-targets`, temporal from `core-schedules`), dedup is in `weaver-state` → durable queue redundant. Only lane-2 (transient event-targeted-audit, Phase-3) needs it. Phase 2 = in-process lane mux, no bucket. |
| 2026-06-02 | **§10.8 FROZEN + entity-ID key fix (Andrew).** Weaver target+playbook settled. §10.2↔§10.8 seam made binding: `targetId` = both the vertex id and the `weaver-targets` key prefix; every `gaps` key must be a `missing_<gap>` column; a true gap with no playbook entry = config error → Health alert (FR29 discipline). Action contracts pinned: `triggerLoom{pattern,subject}`, `nudge{adapter,subject,params?}`, `assignTask{operation,assignee,target}` (→ §10.1 task links), `directOp{operation,target?,params?}`. Templating: literal vs `row.<column>`, null reference = data error. **`triggerLoom` auth resolved** (Andrew's security catch — the unresolved Loom pattern-*start* auth): generic **`StartLoomPattern`** op with **pattern vertex as `authContext.target`** → per-pattern granularity via existing capability scope (Weaver `scope:any`, seeded in orchestration-base; external `scope:specific`/task-grant = Phase-3 carry since platform `specific` is stubbed). Added a §10.5 pointer. Anti-storm: in-flight mark `weaver-state` key `<targetId>.<entityId>.<gapColumn>` set via CAS-create (=OCC), clears on gap-close or lease expiry. **Entity-ID key fix (both §10.2 + §10.8):** candidate is **always a vertex** (never an aspect — aspects are gap predicates/param columns *within* a row), so key on the **NanoID** not the dotted full key (`vtx.X.<id>` dots are subject separators → brittle); full `entityKey` stays in the document (doc-is-truth principle). §10.2 key `<targetId>.<entityKey>`→`<targetId>.<entityId>`. |
| 2026-06-02 | **§10.2 FROZEN (Andrew).** Target Lens output settled. Bucket fixed: NATS KV bucket names take no dots — one shared primordial **`weaver-targets`** bucket, key `<targetId>.<entityKey>`, filtered watch `<targetId>.>` (same contract-contribution pattern as capability-kv §6.1; no per-install bucket). Authz-anchor field **removed** — the bucket is internal Weaver-only operational state, off the read-path (D1 read-path auth is Phase-3); scoping rides the **param columns** + each remediation op's own `authContext`. Frozen column conventions: `entityKey` echo, lens-projected `violating` (lane-1 filter), `missing_<gap>` snake_case bools (**keys bind exactly to §10.8 `gaps`**), free-form param columns, `projectedAt` (Contract #6 as-of semantics); dropped value-`revision` (NATS entry revision is free on watch). **Carry:** §10.3's `weaver.state.>`/`weaver.claims.>` notation is loose — real buckets are `weaver-state`/`weaver-claims` (primordial); fix when §10.3 freezes. |
| 2026-06-06 | **Loom amendment ratified (Andrew) — §10.3/§10.5/§10.6 reshaped + new §10.9** (`cmd/loom/CONTRACT-AMENDMENT-REQUEST.md`, Story 8.1 structural session). **§10.3:** `loom-state` now holds two disjoint-prefixed keys `instance.<instanceId>` (cursor) + `token.<pendingToken>` (thin `{instanceId}` reverse pointer); the `pendingToken → instance` correlation is a **durable co-located index resolved by direct GET** (multi-instance-safe), each step transition a single `substrate.AtomicBatch` on the one bucket; "no secondary KV index" reinterpreted (forbids a *separate* bucket; co-located disjoint-prefix in the same batch is sanctioned); `loom-state` **provisioned `AllowAtomicPublish: true`** (extend the `CoreKVBucket`-only `enableAtomicPublish` gate). **§10.5:** optional **`completionDomains`** added (default `[subjectType]`; cross-domain flows list explicitly; §10.6 timeout backstops). **§10.6:** correlation rewritten to the durable `token.<token>` GET (domain-independent; pointer-presence idempotency; off-stream failed/rejected via submit reply / timeout); **crash-safety invariant 3 (watch-suspended-until-rebuild) REMOVED** (no in-memory index), invariants 1–2 retained. **§10.9 (NEW):** pattern trigger & lifecycle via three `loom`-domain ops `StartLoomPattern`/`CompletePattern`/`FailPattern` (no business vertex; events ride the standard `vtx.op.<requestId>.events` outbox aspect) emitting `loom.patternStarted`/`Completed`/`Failed` on a first-class **`loom`** domain; `instanceId` = `StartLoomPattern` `requestId`; fixed `events.loom.patternStarted` trigger consumer (independent of `completionDomains`); instance stays **operational-only** (`loom-state`, NO Core-KV vertex); "which flows are running" served by Loom's **control plane** (like `internal/refractor/control`, reading `loom-state`), not Core KV. |
| 2026-06-06 | **Loom command-outbox ratified (Andrew) — §10.3 + §10.6** (CAR Request 5, Story 8.1 review finding F2). **§10.3:** `loom-state` gains two disjoint prefixes — `outbox.<token>` (the op-to-submit record) and `deadline.<instanceId>` (per-key TTL = the step deadline). The per-step transition writes/re-arms both in the **same `substrate.AtomicBatch`** as the cursor/token update, so op submission is no longer a dual write (the **command-outbox** pattern, symmetric to the Processor's *event* outbox). An async **relay** (durable consumer on the `loom-state` backing stream `outbox.>`) fire-and-forget publishes the op to `core-operations` and deletes the record on publish-ack (re-publish idempotent via Loom's chosen `requestId` + the Contract #4 tracker) — **no request-reply, no raw NATS handle in `internal/loom`**. `deadline.<instanceId>` is per-instance (linear interpreter ⇒ one pending step), re-armed on advance, deleted on terminal, or auto-expires (`KeyValuePurge`/MaxAge marker, distinct from DEL). **§10.6:** the failed/rejected terminal is **off-stream via the deadline + a read-before-act probe** (`GET vtx.op.<token>`: present→advance+alert; absent+outbox-present→re-arm; absent+no-outbox→fail) — the **synchronous `ops.<lane>` submit-reply terminal is REMOVED**. Crash-safety invariant 1 restated as outbox-inclusive (write-ahead holds by construction). Retires findings F1/F2/F5 + the C2 blocking-callback. Mechanism verified against the repo (`BatchOp.TTL`; `internal/spike/nats-batch/test_ttl_marker_delivery.go`); reconciler-sweep is the sanctioned fallback. |
| 2026-06-02 | **(a1) cap-lens extraction (Andrew).** Reading `step3_auth_capability.go` + `lenses.go` revealed the Capability Lens is a **god-cypher in core/bootstrap** that hard-codes the grant vocabulary of *multiple packages* (rbac-domain `role`/`permission`/`holdsRole`/`grantedBy`; service/location; Phase-2 `task`/`assignedTo`) into one per-actor doc — `task` is the newest tenant of a pre-existing inverted dependency. Fix (Story 7.1 scope): ephemeral grants leave the bootstrap god-cypher for an **`orchestration-base`-owned `capabilityEphemeral` lens** → disjoint key **`cap.ephemeral.<actor>`** (reuses the `capabilityRoleIndex` disjoint-prefix pattern, Contract #6 §6.1; no Refractor lens-merge needed). Bootstrap cypher **drops all `task` refs** (dependency direction flips package→core). Step-3 task branch reads the new key; `cap.<actor>` still read for `roles` on task-path denials. Grant *field shape* unchanged. Broader god-lens decomposition (role/permission/service projections + a generic step-3 **auth-hooks** consumer side) recorded as a future-ADR open item in `lattice-architecture.md`. |
| 2026-06-07 | **Event-domain model ratified (Andrew) — §10.5/§10.6** (CAR Requests 6–9, folded into Story 8.2; superseded by the broader Contract #3 event-domain model). Every event class is now `<domain>.<eventName>` (Contract #3 §3.4, enforced at commit step 7), so the §10.5 "domain = first segment" routing model is **true**, not illustrative. **§10.5:** the onboarding example becomes `completionDomains: ["orchestration"]` — a userTask completes on the **`orchestration`** domain (the `orchestration.taskCompleted` event), regardless of subject type. **§10.6:** the userTask correlation row reads `taskKey` (`vtx.task.<id>`) → completion `orchestration.taskCompleted` → **`payload.taskKey`** → `token.<taskKey>` GET (all event business fields ride the envelope `payload`, Contract #3 §3.4; Loom's two correlation keys are top-level `requestId` (systemOp) and `payload.taskKey` (userTask)); the userTask completion subsection retitled "by `taskKey`"; crash-safety invariant 1 notes the engine supplies the deterministic `taskId` via `CreateTask`'s optional `taskId` so the `taskKey` is known write-ahead (no Contract #2 change; §10.7 auth unchanged). Added the **userTask creation-deadline + task-vertex probe** (R9): a userTask arms a bounded `CreateTaskTimeout` that disarms once the task vertex exists (then the human wait is unbounded), failing a rejected/lost `CreateTask` rather than wedging — with the honest nuance that after disarm a mis-declared `completionDomains` is caught by a load-time warn, not a runtime backstop. |
| 2026-06-12 | **Weaver amendments ratified (Andrew) — §10.2/§10.3/§10.4** (`cmd/weaver/CONTRACT-AMENDMENT-REQUEST.md`, Stories 9.2/9.3). **§10.3 (R1):** the `weaver-state` mark's per-key TTL is **`2 × lease`** (`markTTLBackstopFactor`), not a literal mirror of `leaseExpiresAt` — `leaseExpiresAt` mirrors the *lease* (`claimedAt + lease`) and the TTL is the strictly-longer dead-reconciler backstop. The interval-cadence sweep reclaims a lease only while the mark still exists (the mark is its only evidence — it enumerates marks, not rows), so the key must outlive `leaseExpiresAt` for the *sweep-reclaims-expired-leases* clause to be reachable; `2 ×` is the smallest factor satisfying both the never-wedge (TTL) and re-attempt (sweep) clauses, with `SweepInterval` clamped ≤ lease. **§10.4 (R2):** the schedule-subject template's segments after `schedule.<domain>.<kind>.` are **publisher-chosen dot-free tokens** within `schedule.>` — Weaver keys per **target AND entity** (`schedule.weaver.timer.<targetId>.<entityId>`, fired `…fired.<targetId>.<entityId>`) so two targets projecting a `freshUntil` for the same entity hold independent timer slots instead of colliding on the `MaxMsgsPerSubject: 1` rollup (pins the same "publisher-chosen" reading the fired-target line already had). **§10.2 (R3):** **`freshUntil`** named as an optional engine-recognized convention column (RFC3339, a free-form param column by storage) — the target cypher computes `resolve + window` and projects the deadline; the engine converts it to an `@at` schedule (§10.4) and never computes the window. |
| 2026-06-12 | **Pattern-definition pinning ratified (Andrew) — §10.3** (CAR Request 10, post-8.3 fix-forward, finding F2). `loom-state` gains a **fifth key shape**, `instance.<instanceId>.pattern` — the full pattern definition as loaded at trigger time, written in the **same `AtomicBatch`** that creates `instance.<instanceId>` (both `CreateOnly`) and deleted in the **same terminal batch** that flips `status` to `complete`/`failed`. It is deliberately a **sub-key of its instance**, not a fifth disjoint prefix (instanceIds are NanoIDs, so `.pattern` is unambiguous); the other four prefixes remain disjoint. **Definitions bind at instance start**: all step resolution (advance, completion, deadline recovery) reads this pin, never the live pattern source, so a pattern update mid-flight (reordered/inserted/changed steps) cannot mis-index a running instance's `cursor` — pattern updates affect **new instances only**. Listing `instance.*.pattern` yields exactly the live-instance set, which is the second leg of the §10.9 per-domain consumer reconcile (current definitions ∪ pinned definitions of live instances): an in-flight instance survives its pattern being removed/updated-away, and the domain consumer drains once its last live instance completes — superseding the prior documented in-flight-orphan-on-pattern-removal caveat. A missing pin for a `status=running` instance is an invariant break, surfaced as an operator-visible failed terminal (never a silent wedge or a Nak loop). Disaster recovery (total `loom-state` loss → fresh `StartLoomPattern`) re-binds to the current definition, unchanged from the Story 8.3 narrow recovery semantics. Event-embedded pins were analyzed and rejected (`core-events` `MaxAge=7d` vs unbounded userTask waits). |
| 2026-06-13 | **§10.8 `nudge` action `operation` field ratified (Andrew) — §10.8** (`cmd/weaver/CONTRACT-AMENDMENT-REQUEST.md` Request 4, Story 10.2). The `nudge` action params become **`{ adapter, operation, subject, params? }`** (`operation` **required**), and the `missing_bgcheck` example gains `"operation": "ResolveBackgroundCheck"`. `operation` is the **resolve-op type** — the op the Two-Phase Nudge submits in its Resolve leg to record the external outcome back into Core KV (Claim→Execute→Resolve, arch Item 3). Reconciles an internal inconsistency: the §10.3 `weaver-claims` record already carries `operation`, but that value could only come from the playbook's nudge action, which had no field for it; without it the Resolve leg has no op to submit and a claim could never reach `state=resolved`. A blank or `row.`-templated `operation` routes to the same `errConfig`-surfaced-to-Health posture as a blank/templated `adapter`. |
| 2026-06-18 | **External I/O Bridge amendments ratified (Andrew) — §10.2 / §10.3 / §10.5 / §10.6 / §10.8** (one coherent package; CARs in `cmd/{loom,weaver,refractor}/CONTRACT-AMENDMENT-REQUEST.md`; umbrella `_bmad-output/planning-artifacts/sprint-change-proposal-2026-06-18.md`). The reference vertical surfaced that external I/O sat in the wrong engine; it moves out of Weaver (convergence *detection*) into **Loom + a new generic `bridge` component** (deterministic *execution*), event-driven and symmetric to userTasks. **§10.5/§10.6 (loom):** new **`externalTask`** step kind — two-op-shaped `{kind, adapter, params, replyOp, instanceOp}` (Loom submits the `instanceOp`, which creates the `service.<x>.instance` claim vertex + emits an `external.<adapter>` event via that op's transactional outbox, then **parks**); a **third** completion-correlation key **`payload.externalRef`** (= the `instanceKey` Loom mints write-ahead and parks on as `token.<instanceKey>`; the bridge's `replyOp` echoes it back). The "no new envelope field" userTask assumption is **struck** — this is a real engine extension (a 3rd `correlationKeys` key). Loom stays pure (event rides the op's outbox, no NATS handle). **§10.3 (weaver A):** **`weaver-claims` RETIRED** — the Two-Phase Nudge claim record + Claim→Execute→Resolve protocol leave Weaver; the visible-claim guarantee (FR58/NFR-S11) is now the **service-instance vertex in Core KV** (created before the `external.*` event is publishable). Reason: the resolve op could not address a candidate ≠ the nudge `subject` (hard-coded payload + Starlark can't read `authContext`). **Hard invariant pinned:** the bridge result-op `requestId` MUST be `deterministic(idempotencyKey = instanceKey)` (redelivery collapses on the Contract #4 tracker → exactly one result mutation). `weaver-state` + the reconciler/sweeper are **KEPT** (they serve `triggerLoom`/`assignTask`/`directOp`); only the nudge-specific `claimId` clauses retired. **§10.8 (weaver B):** **`nudge` GapAction RETIRED** (supersedes the Story-10.2 `operation`-on-nudge addition above) — external remediation is now `triggerLoom` of a pattern containing an `externalTask`; the `missing_bgcheck` example becomes a `triggerLoom`. **§10.2 (refractor, Option (b)):** a convergence target lens MAY be an **`actorAggregate`** (needed to reproject on linked-constituent change — identity aspects + service-instance across links); the **frozen §10.2 key + `splitRowKey` stay UNCHANGED** — such a lens declares an explicit key column emitting the bare-NanoID `<entityId>` instead of the default `{actorSuffix}` (= `<type>.<id>`), landing in the Epic-12 Output-descriptor machinery. **Contract #3 — NO amendment** (dropped at ratification): `external` is an ordinary domain under the open `<domain>.<eventName>` model (no Processor allowlist), realized via a package event-type DDL + the bridge's `events.external.>` consumer (envelope spec → `docs/components/bridge.md`). Bucket/constant/verify-enumeration teardown + the engine work land in the External I/O Bridge epic — Epic 13, stories 13.2–13.5 — under full 3-layer review + the FR58 crash/retry proof; the `Fake*` adapters move-then-delete to the bridge (never a window where neither path works). **Pre-commit coherence refinement (Andrew, 2026-06-18):** the claim vertex's **type is package-chosen** (the bridge is **type-agnostic** — `service.<x>.instance` is the lease demo's choice, not a contract constraint), the external **outcome is recorded as aspect(s) per D5** (minimum data in the vertex root, never fat root `data`), and bridge idempotency rests on the deterministic result-op `requestId` + the adapter's `idempotencyKey` dedup — **not** a typed-vertex read. |
| 2026-06-18 | **externalTask deadline + completion symmetry (UNCOMMITTED — pending Andrew's review) — §10.5/§10.6.** Corrects the externalTask deadline, which (as first ratified above) said "exactly like a systemOp": on deadline-fire with a committed-but-not-yet-replied `instanceOp` the engine would **advance the cursor before the external result landed** — broken wait-for-result (a later outcome-branching step reads stale/absent data) on any slow/dead bridge or short timeout. **Fix: externalTask is handled like a `userTask`, not a systemOp.** The deadline is a **bounded creation-deadline on the `instanceOp` submission**; on `instanceOp` commit it **disarms** → the wait for the bridge `replyOp` is **unbounded** (never advances on the deadline). A rejected/lost `instanceOp` still → `FailPattern` (FR29; the submission is never a silent wedge). **Completion** becomes a dedicated **`orchestration.externalTaskCompleted{externalRef}`** event **emitted by the `replyOp` DDL** — the uniform orchestration-domain signal symmetric to `orchestration.taskCompleted{taskKey}` (Loom's existing `loom-orchestration` consumer advances it; externalTask patterns declare `completionDomains: ["orchestration"]`; a load-time warn mirrors `userTaskCompletionUnobservable`). Emitted-by-the-replyOp (not platform-injected as `taskCompleted` is) because the `replyOp` is a purpose-built completion op, not an oblivious business op. Also corrected the stale "full `vtx.<type>.<id>` key" §10.6 wording to the **opaque bare-NanoID handle** Loom actually mints (the Story 13.2 §0 resolution; the `instanceOp` DDL forms the typed key from the handle). Engine + 13.2-fixture/test updates land in the follow-up story; `docs/components/{loom,bridge}.md` updated in lockstep. |
