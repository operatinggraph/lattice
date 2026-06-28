# FR28 — Role-queue task assignment + routing fallback (+ FR29 unrouted-task surfacing)

**Status: 📐 awaiting-Andrew (ratification)**
Designer fire (Winston / `lattice-designer`), 2026-06-28. One Lattice-lane backlog item → ratify-ready design.
The **Lattice Steward** builds this **only after** Andrew ratifies.

---

## For Andrew (the one-look decision)

**What it does (two lines).** Lets a `task` be assigned to a **role-queue** (`task --queuedFor--> role`)
instead of only a concrete identity, with `CreateTask` **routing** a primary assignee → role-queue
fallback → loud reject; any role-holder can **`ClaimTask`** a queued task (it converts to a direct
`assignedTo`). The existing `capabilityEphemeral` / `myTasks` actor-aggregate lenses **fan the grant
and the inbox out to every role-holder** until claimed. A queued task that no eligible actor ever
picks up is surfaced by a new **`unroutedTasks` Weaver convergence target** (FR29 — "never silently
dropped").

**No architectural fork.** This is application/package-plane work on machinery that already ships (the
`task` vertex, the `assignedTo` link, the two actor-aggregate lenses with their `reportsTo` fan-out, the
rbac `holdsRole` graph, Weaver convergence targets). It honors P2 (writes via ops), P5 (reads via
lenses), and the write-path no-scans invariant (the crux below).

**One frozen-contract change — staged UNCOMMITTED in `main` for your ratification:**
- **`docs/contracts/10-orchestration-surfaces.md` §10.1 (Task vertex).** Adds the
  `lnk.task.<id>.queuedFor.role.<roleId>` link, the `ClaimTask` lifecycle op, the "assigned to **either**
  an identity **or** a role-queue (exactly one)" invariant, the role-queue **grant fan-out** note, and
  replaces the *"Phase 3: FR28 deferred … FR29 monitor returns"* paragraph with the landed shape + the
  `unroutedTasks` monitor. **Affected consumers:** `orchestration-base` (the `task` DDL + the two lenses),
  the Processor step-3 ephemeral-grant path (unchanged logic — see §6 below), Loupe's task view. **§6.6
  (Capability KV ephemeralGrants) is NOT touched** — the grant *field shape* and the step-3 *matching
  logic* are unchanged; the role fan-out is a package-owned **lens** detail, not a contract change (the
  reconciliation in §6 walks through why).

**The one decision I resolved that's worth your eye (within §10.1 latitude, not a separate fork):**
*capacity vs. availability.* The PRD vision (§294) routes on "Sam's queue is **at capacity** OR Sam is
**marked unavailable**." I ship **availability (a boolean) as the Fire-2 MVP** and sequence **numeric
capacity as a flagged Fire-2b follow-on**, because capacity requires an `openCount` counter maintained
across *every* task-lifecycle op (the write path cannot scan an identity's task set — §5). The boolean
covers the primary "marked unavailable" case cleanly with a single known-key read; capacity is additive.
My recommendation is to ratify the phasing as written. (Full capacity design is in §7 so it's on the shelf.)

---

## 1. Problem + intent

**FR28** (PRD §740, readiness-report §78, epics/index §71): *"Tasks can be assigned to a specific actor
or to a role-based queue; when the primary assignee is unavailable, tasks fall back to the role queue."*
Paired with **FR29**: *unrouted tasks surface in the health observability view; never silently dropped.*

The PRD's concrete scenario (`prd.md:294`):

> Carlos creates a `PaymentDisputeRequest` task assigned to **Sam**. If **Sam's queue is at capacity or
> Sam is marked unavailable**, the Starlark routing script **falls back to the leasing team queue
> (role-based, not person-based)**. If the leasing queue is **also** unavailable, the operation **fails
> with a specific error and surfaces in the health observability view as an unrouted task requiring
> manual intervention. No silent drop.**

**What ships today (the gap).** Phase 2 deliberately deferred FR28 (`epics/phase-2-epics.md:785`). The
`task` DDL (`packages/orchestration-base/ddls.go`) `CreateTask` **requires** a concrete `assignee`
identity, validates it is alive, and commits `task --assignedTo--> identity` atomically. There is **no
role-queue target, no routing/fallback, no availability model, and no unrouted-task monitor.** Contract
#10 §10.1 records the deferral precisely and — crucially — **pre-designs the landing**:

> **Phase 3:** FR28 (role-queue + fallback) is deferred; when it lands, a role-queue with no eligible
> actor *is* unroutable and the FR29 Health-KV monitor returns for that case.

This design lands FR28 against that anticipated shape. Brainstorm grounding: the task/Command model
(Lattice System Spec "A Task is a specific instance of a Command assigned to an actor … linked to an
Identity via an `assignedTo` link") and FR22's role-scoped capability vocabulary (the role graph this
reuses).

---

## 2. The shape (data model, read path, write path, orchestration)

### 2.1 Data model — one new link, no new vertex type, no new status

The `task` vertex is unchanged in type and root shape (`{status, expiresAt}`, `status ∈ {open,
complete, cancelled}` — **no new status value**). One new **link** (Contract #1: relationships are links,
not fields; sentence test "task queuedFor role" ✓; the later-arriving task is the **source**, the
pre-existing role is the **target**):

```
vtx.task.<id>            root data = { status, expiresAt }     # UNCHANGED
lnk.task.<id>.forOperation.meta.<opId>                          # UNCHANGED (the granted op)
lnk.task.<id>.scopedTo.<type>.<targetId>                        # UNCHANGED (the grant target)

# EXACTLY ONE of the two assignment links is present at any time:
lnk.task.<id>.assignedTo.identity.<assigneeId>                  # direct push assignment (today's shape)
lnk.task.<id>.queuedFor.role.<roleId>                           # NEW — pull/claim role-queue assignment
```

**Invariant (added to §10.1):** an open task carries **exactly one** assignment link — `assignedTo` (a
named identity will perform it) **or** `queuedFor` (any holder of the role may claim it). `ClaimTask`
atomically converts `queuedFor → assignedTo`. A task is never both, never neither (the no-orphan
invariant, FR29-by-construction, extends to "either a valid identity or a valid role").

**Why a `queued` status value is NOT added (alternative rejected, §8-A1):** the assignment **link
discriminates** the queued-vs-assigned state already (`queuedFor` present ⇒ queued). Adding a `queued`
status would force a status-enum contract change and a second source of truth for the same fact. Reuse
`status: open` + the link. This mirrors how the platform already models state via links (P-of-record).

### 2.2 Write path (P2 — ops only, known-key reads only)

All mutations are Processor ops on the `task` DDL (`orchestration-base`), extending the existing
known-key-reads-only Starlark. **No op reads a lens or scans Core KV** (`TestPackage_NoScans`).

- **`CreateTask` (extended, backward-compatible).** Accepts the existing `assignee` (identity) **and/or**
  a new `queue` (a `vtx.role.<id>` key) plus the existing `forOperation` / `scopedTo` / `expiresAt`. The
  **routing decision** (Fire 2; §2.5) reads at most **one known key** — the named `assignee`'s
  `availability` aspect — and commits **one** assignment link:
  - assignee given, alive, and `available` → `assignedTo identity` (byte-identical to today's path).
  - else a `queue` (role) given and the role vertex is alive → `queuedFor role`.
  - else (no valid endpoint resolved) → **reject `RoutingFailed`** (a structured `ScriptError`; no task
    committed — "no silent drop" at creation).
  An `assignee`-only call with no `availability` aspect present behaves **exactly as today** (absent
  aspect = available; pure additive compatibility).
- **`ClaimTask(taskKey)` (new).** A role-holder claims a queued task. Validates: the task is **open** and
  carries a `queuedFor role` link; the **claimant holds that role** (a single known-key read of
  `lnk.identity.<claimant>.holdsRole.role.<roleId>` — the claimant + role are both known, so the exact
  link key is known: **not a scan**). On success, atomically **tombstone** `queuedFor role` + **create**
  `assignedTo claimant` (the same atomic link-swap `ReAssignTask` already does). Idempotent re-claim by
  the same actor is a no-op; a claim by a non-role-holder rejects `NotAuthorizedToClaim`; a claim of an
  already-claimed (now `assignedTo`) task rejects `TaskAlreadyClaimed`.
- **`ReAssignTask` (extended)** may now re-point to a `queue` (role) as well as an identity (re-queue),
  same atomic link-swap.
- **`SetAvailability(identity, available)` (new, Fire 2).** Writes the `availability` aspect (§2.4).
- `CompleteTask` / `CancelTask` — unchanged (operate on root `status`); the autocomplete path
  (`internal/processor/autocomplete.go`) is unchanged (it acts on the §10.7 task grant, which a claimed
  task carries identically once it is `assignedTo` the claimant).

### 2.3 Read path (P5 — lenses fan the grant + inbox out to role-holders)

This is the **mirror-the-existing-pattern** core. The two `orchestration-base` actor-aggregate lenses
already fan a task out across actors via link-walking (`capabilityEphemeral` walks `assignedTo` **plus a
`reportsTo` 2-hop** for manager delegation; `myTasks` walks `assignedTo`). FR28 adds **one more
OPTIONAL-MATCH branch to each**, walking the role-queue:

```cypher
// added to BOTH capabilityEphemeral and myTasks, after the existing assignedTo / reportsTo branches:
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)<-[:queuedFor]-(qtask:task)
  WHERE qtask.data.status = 'open'                          // myTasks
  // (capabilityEphemeral uses qtask.data.expiresAt > $now, mirroring its existing branch)
OPTIONAL MATCH (qtask)-[:forOperation]->(qop)
OPTIONAL MATCH (qtask)-[:scopedTo]->(qtgt)
// → collect the same grant/inbox entry shape, unioned into ephemeralGrants / openTasks
```

Effect: **while queued, the grant (and the task-inbox row) projects to every identity holding the
role** — the role-queue's "anyone in the team can pick it up" semantics, expressed through the *same*
Capability-KV `cap.ephemeral.<actor>` projection the direct path uses. The actor-aggregate
`ActorEnumerator` already re-projects on `holdsRole` / `queuedFor` CDC events (it walks adjacency from
the changed vertex to the affected actors), so **no new fan-out machinery** — this is the identical
decomposition the auth plane already runs. On **`ClaimTask`**, the `queuedFor` link tombstones → the
role-fan-out branch yields nothing for the non-claimants → their `cap.ephemeral` / `my-tasks` entries
drop that grant on reprojection (the existing `emptyBehavior:delete` + soft-tombstone path), and the
claimant picks it up via the `assignedTo` branch. **The grant narrows from the role to the claimant
atomically through ordinary reprojection** — no bespoke revocation.

### 2.4 The `availability` aspect (Fire 2)

A new 4-segment aspect on the identity (Contract #1 shape; D5 — business state lives in an aspect, root
stays minimal):

```
vtx.identity.<id>.availability    data = { available: bool }          # Fire 2 (boolean MVP)
                                  data = { available: bool, openCount: int }   # Fire 2b (capacity, §7)
```

Written only by `SetAvailability` (and, for `openCount`, maintained by the lifecycle ops — §7). Read by
the `CreateTask` routing Starlark as a **single known key** (the assignee is named, so its aspect key is
known). Absent aspect ⇒ treated as available (compatibility default).

### 2.5 Orchestration — FR29 unrouted-task surfacing (a Weaver convergence target)

"Unrouted" is, precisely, **an open `queuedFor` task that no eligible actor claims** — a state that
*should* converge (someone claims it) but hasn't. That is exactly a **Weaver convergence gap**, so FR29
reuses Weaver rather than inventing a monitor. `orchestration-base` ships a new **`unroutedTasks` Weaver
target lens** (`weaver-targets` bucket, §10.2 row shape) projecting one row per open queued task:

```
{ entity: <taskKey>, queuedRole: <roleKey>, openSince: <expiresAt-or-createdAt>,
  violating: (now - openSince) > $staleThreshold }     // unclaimed past the threshold ⇒ violating
```

A `violating: true` row is **visible in Loupe's convergence view** and Weaver's heartbeat rolls a
count into its Contract #5 §5.5 `issues[]` channel (`UnroutedTasks`, severity warning → the component is
*degraded*, not unhealthy — it is a business backlog, not a component fault), which the **Lamplighter**
and Loupe's health/observability view already surface. **Remediation is surface-only first** (FR29 says
"requiring manual intervention"); auto-escalation (Weaver `assignTask`-ing the stale task to an
operator role) is a flagged follow-on (§8-A4), not the MVP — escalating automatically before an operator
has even seen it would defeat the "manual intervention" intent.

**Why the empty-queue case is a monitor, not a creation-time reject — the load-bearing reconciliation.**
The `CreateTask` write path **cannot ask "does this role have any members?"**: enumerating a role's
holders is a reverse walk (`role <-[:holdsRole]- identity`) — an unbounded scan the write path forbids
(`TestPackage_NoScans`; the same invariant the op-time-bounded-link-enumeration design is separately
relaxing, and which a *target*-keyed walk like this can't satisfy anyway). So routability of the
**fallback queue's membership** is structurally a **post-hoc** question — which is exactly why Contract
#10 §10.1 already says the FR29 monitor "returns for that case." The PRD's *"fails with a specific
error"* and §10.1's *"FR29 monitor"* are therefore **two distinct cases, cleanly split by what the write
path can know**:

| Case | What the write path can check (known-key) | Outcome |
|---|---|---|
| No valid endpoint at all (assignee invalid **and** no/invalid `queue` role) | the named identity / role vertex is alive | **`CreateTask` rejects `RoutingFailed`** (loud, no task) |
| Valid role-queue, but zero members / none available | — (membership is a scan; unknowable at write time) | **task created `queuedFor`; FR29 `unroutedTasks` monitor** surfaces it if it ages |

Both are "no silent drop." This split is not a compromise — it is what the no-scans invariant *forces*,
and §10.1 anticipated it.

---

## 3. Contract surface

| Contract / § | Change vs. build-to | Why |
|---|---|---|
| **#10 §10.1 Task vertex** | **CHANGE** (staged uncommitted) | New `queuedFor` link; `ClaimTask` op; "exactly one assignment link" invariant; role-queue grant fan-out note; replace the "Phase 3 FR28 deferred" para with the landed shape + `unroutedTasks` monitor. |
| #10 §10.2 Weaver target row | build-to | `unroutedTasks` is a standard §10.2 target lens row — no shape change. |
| #6 §6.6 ephemeralGrants[] | **build-to (NOT changed)** | Field shape `{source, taskKey, operationType, target, expiresAt}` and the step-3 matching scan are unchanged; the role fan-out is a package-owned **lens cypher** detail. A role-queued grant is just another array entry, matched per-actor identically. |
| #5 §5.4/§5.5 Health-KV | build-to | `UnroutedTasks` is an author-discretion `issues[]` entry; the schema already permits it. |
| #1 key-shapes | build-to | `queuedFor` is a canonical 6-segment link; `availability` a canonical 4-segment aspect. |

Only **one** frozen-contract doc is touched (#10 §10.1). It is staged **uncommitted** in `main` per the
designer protocol — the diff is the proposal.

---

## 4. Reconciliation with the existing mental model ("but didn't we already…?")

- **Didn't we already build task assignment + the grant projection?** Yes — for the **direct** path
  (`assignedTo identity`). FR28 adds the **role-queue** path. The grant/inbox machinery is *reused*
  (one OPTIONAL-MATCH branch per lens), not rebuilt.
- **Doesn't this duplicate the `reportsTo` manager-delegation fan-out?** No — it is the **same kind** of
  fan-out (an actor inherits a task they don't directly hold), via the **same** actor-aggregate
  `ActorEnumerator`. `reportsTo` fans *down a management hierarchy*; `queuedFor`+`holdsRole` fans *across
  a role's members*. Mirroring `reportsTo`'s proven shape is deliberate (the skill's "mirror the
  established internal pattern" mandate) — not a new mechanism.
- **Does this contradict §10.1's design-of-record?** No — §10.1 explicitly *reserved* FR28 + the FR29
  monitor for "when it lands." This is the landing, in the anticipated shape (queued grant fan-out +
  monitor for the no-eligible-actor case), not a deviation.
- **New state introduced?** Two: the `queuedFor` link (Core KV, P2 — an ordinary canonical link) and the
  `availability` aspect (Core KV, P1 business state). Both live where the architecture already keeps
  their kind (links + aspects in Core KV); no new operational-state home. The `openCount` capacity
  counter (Fire 2b) is the only *maintained* state and is explicitly flagged (§7).
- **Auth widening?** A role-queued task grants its bound op to *every* role-holder until claimed. This is
  **intended** (the definition of a role-queue) and **bounded** by the existing grant scope
  (`taskKey + operationType + target + expiresAt`); it mirrors how a role already confers a *standing*
  permission to all its holders — the ephemeral grant is the task-scoped analog. Claiming collapses it to
  one actor. Documented in §10.1 so it is a *visible* property, not an accident.

---

## 5. Migration / compatibility + test strategy

**Migration.** Purely additive: a new link relation, a new aspect, two new ops (`ClaimTask`,
`SetAvailability`), two additive lens branches, one new lens (`unroutedTasks`). Existing `assignee`-only
`CreateTask` calls are **byte-identical**. Lands via the F-004 package upgrade path (or a fresh install);
the new lens reprojects on activation. No data backfill (existing direct-assigned tasks are untouched).

**Tests** (extend the existing `orchestration-base` harnesses — `task_script_test.go`,
`create_task_test.go`, `lifecycle_*_test.go`, `lens_cypher_test.go`):
- **Unit (Starlark, meta-pipeline harness):** `CreateTask` queue-target commits `queuedFor`; routing
  picks assignee-when-available / queue-when-unavailable / `RoutingFailed` when neither resolves;
  `ClaimTask` swaps `queuedFor→assignedTo`, rejects non-role-holders + already-claimed; `ReAssignTask`
  to a queue; `SetAvailability` aspect write; idempotency on re-dispatch (§10.3).
- **Lens (cypher-contract):** a queued task projects its grant + inbox row to **all** role-holders
  (assert ≥2 holders each see it); on `ClaimTask` the grant **narrows** to the claimant (others drop);
  the `unroutedTasks` target marks an aged unclaimed queued task `violating:true` and a freshly-queued
  one `violating:false`.
- **Adversarial (the auth-correctness vector, full 3-layer in Fire 1):** a non-role-holder is **never**
  granted a queued task's op (their `cap.ephemeral.<actor>` carries no entry → step-3 denies
  `AuthContextMismatch`); a claimed task grants **only** the claimant.
- **Ephemeral-stack e2e (Fire 3):** queue a task to a role with two holders → both see it in `my-tasks`
  → one `ClaimTask`s → the other's inbox drops it → complete; and: queue to a role, let it age → the
  `unroutedTasks` row goes `violating` and Weaver's heartbeat raises `UnroutedTasks`.

**Gates:** the project battery (`go build ./...`, `make vet`, `golangci-lint`, `verify-kernel`, Gate
2/Gate 3, `verify-package-orchestration-base`, the relevant `go test`), plus the new Gate-3 adversarial
vector above.

---

## 6. Risks + alternatives

**Risks.**
- *Reprojection storm on a large role.* Adding/removing a `queuedFor` link, or a `holdsRole` change on a
  big role, re-projects every role-holder's `cap.ephemeral` / `my-tasks` — the same cost the auth plane
  already pays for `holdsRole` changes (`capabilityRoles` fans identically). Bounded by role size;
  acceptable at Phase-3 scale, and no worse than the standing-permission fan-out the platform already
  runs. Flag if a role ever grows pathologically large (a sharding-era concern, not now).
- *Grant-narrowing race on claim.* Two actors `ClaimTask` the same queued task concurrently — the
  atomic link-swap is CAS-on-the-`queuedFor`-link's presence (the second claim sees `assignedTo`, not
  `queuedFor`, and rejects `TaskAlreadyClaimed`). First-claimant-wins, no double-grant. Standard OCC.
- *Stale `availability`.* The routing reads the assignee's `availability` aspect, which can lag a
  `SetAvailability`. Worst case: a task routes to a just-marked-unavailable assignee (or queues past a
  just-returned one). Self-healing — the assignee can `ReAssignTask`/the operator re-routes, and the
  FR29 monitor catches a stalled result. Acceptable; availability is advisory, not a security boundary.

**Alternatives considered (earn the recommendation).**
- **A1 — `queued` status value vs. the `queuedFor` link as the discriminator.** *Rejected the status
  value.* The link already encodes the fact; a status value duplicates it and costs a status-enum
  contract change. (Re-asked: could a status value *beat* the link? Only if a consumer needed to filter
  queued tasks without the link — but every consumer walks the assignment link anyway. Link wins.)
- **A2 — pull/claim vs. push-with-rotation.** *Chose: support both, via the assignee-vs-queue target.*
  The PRD has **both** — primary `assignee` is a **push** (to a named identity), the fallback role-queue
  is a **pull** (anyone claims). A pure round-robin *push* (the routing script picks one available member)
  was rejected for the fallback because (a) picking a member requires enumerating the role — a write-path
  scan, forbidden; and (b) the PRD explicitly wants "the leasing team **queue** (role-based, not
  person-based)." Pull is both correct and the only write-path-legal option for the fallback.
- **A3 — `availability` aspect vs. an operational-KV presence store.** *Chose the aspect.* Availability
  is queryable **business** state (who can take work), part of the identity's data (D5 — business data in
  aspects), and must be a known-key read from the write path; an operational-KV presence store (like a
  Weaver mark) is for ephemeral orchestration state and isn't a Core-KV known-key the routing op may
  read. Aspect wins. (Re-asked: could presence-KV beat it? Only if availability were high-churn
  sub-second presence — it isn't; it's operator/shift-grained.)
- **A4 — FR29 as a Weaver convergence target vs. a bespoke Health-KV monitor vs. auto-escalation.**
  *Chose the Weaver target, surface-only first.* An unclaimed queued task **is** an unconverged state —
  Weaver is its natural owner, and a `weaver-targets` `violating` row + a Health-KV `issues[]` count is
  exactly the "surfaces in the observability view" FR29 asks for, with **zero new monitor
  infrastructure**. A bespoke monitor would reinvent convergence detection. **Auto-escalation** (Weaver
  `assignTask`-ing the stale task onward) is deferred — FR29 says "manual intervention," and
  auto-escalating before a human sees it pre-empts that intent; it's a clean follow-on once the
  surface-only loop is proven.
- **A5 — route in the `CreateTask` write path vs. in a Weaver reconciler.** *Chose the write path* (the
  PRD's "Starlark routing script"). The routing decision needs only a **single known-key read** (the
  named assignee's `availability`), which the write path can do; pushing routing into Weaver would add a
  convergence round-trip + a reconciler action for a decision that's synchronous-at-creation by nature.
  Weaver owns the *post-hoc* unrouted-tail (A4); creation-time routing stays in the op.

---

## 7. Capacity (Fire 2b — designed, sequenced as a follow-on)

Numeric capacity ("Sam's queue is **at capacity**") needs an **`openCount`** the routing Starlark can
read as a single known key, because the write path **cannot scan** an identity's task set. Design:
- Extend the `availability` aspect to `{ available, capacity, openCount }`.
- **Maintain `openCount` in the task-lifecycle ops** (each a bounded, known-key write of the affected
  identity's aspect): `CreateTask`/`ClaimTask` → `+1` on the assignee/claimant; `CompleteTask`/
  `CancelTask`/the autocomplete path → `−1`; `ReAssignTask` → `−1` old, `+1` new. All atomic within the
  op's commit, so the counter is consistent if every assignment-changing path updates it.
- Routing check becomes `available AND openCount < capacity`.

**Why a counter, not a derived count:** deriving the count means scanning the identity's tasks
(forbidden) or reading the lagging `my-tasks` lens (P5 — the write path must not read a lens read-model).
A maintained counter is the only write-path-legal option; its cost is touching every lifecycle op, which
is why it's a **separate, flagged increment** rather than folded into the boolean MVP. **Dead-scaffolding
check:** capacity is *not* built before its consumer — it ships only when a vertical actually routes on
capacity; the boolean MVP is independently valuable meanwhile.

---

## 8. Decomposition for the Steward (fire-by-fire, each independently shippable + green)

- **Fire 1 — Role-queue + claim (the core).** The `queuedFor` link; `CreateTask` accepts a `queue`
  (role) target; `ClaimTask` op; the two additive lens branches (grant + my-tasks fan-out); the §10.1
  contract edit committed by Andrew on ratification. **Value now:** a task can be queued to a role and
  claimed by any member; the grant + inbox fan out correctly. **Review: full 3-layer** (auth-plane: the
  grant fan-out + the claim authorization are security-relevant).
- **Fire 2 — Availability routing.** The `availability` aspect (boolean); `SetAvailability` op; the
  `CreateTask` routing Starlark (assignee-when-available → role-queue fallback → `RoutingFailed`).
  **Value now:** primary→fallback routing per the PRD. **Review: thorough lead** (no new auth surface;
  write-path routing logic) — overridable to 3-layer if the routing touches the grant path.
  - **Fire 2b (follow-on) — numeric capacity** (§7): the `openCount` counter + capacity check. Sequenced
    behind a vertical that routes on capacity.
- **Fire 3 — FR29 unrouted surfacing.** The `unroutedTasks` Weaver target lens + the Weaver heartbeat
  `UnroutedTasks` issue + the ephemeral-stack e2e. **Value now:** an unclaimed queued task surfaces in
  Loupe/health, never silently dropped. **Review: thorough lead** (read-model + heartbeat, no Core-KV
  write).

A **vertical consumer exists for every fire** (the LoftSpace/Clinic task workflow assigns tasks today),
so none is dead scaffolding.

---

## 9. Open questions — resolved (decide-don't-defer)

1. **New status value for queued?** No — the `queuedFor` link discriminates (§2.1, A1).
2. **Who holds the grant for a queued task?** Every role-holder, until claimed; via the lens fan-out
   (§2.3). §6.6 unchanged.
3. **Capacity now or later?** Boolean availability now (Fire 2); numeric capacity a flagged follow-on
   (Fire 2b, §7) — forced by the write-path no-scans constraint.
4. **Empty-queue: reject or monitor?** Invalid-endpoint → reject `RoutingFailed`; valid-but-unstaffed
   queue → FR29 monitor (§2.5) — the split the no-scans invariant forces and §10.1 anticipated.
5. **FR29's home?** A Weaver convergence target + Health-KV `issues[]`, surface-only first (A4).
6. **Routing location?** The `CreateTask` op (single known-key read), per the PRD (A5).

---

## 10. Adversarial pass (self-review, folded in)

- *Could a non-role-holder gain a queued grant?* Only if the fan-out branch matched them — it walks
  `(identity)-[:holdsRole]->(role)<-[:queuedFor]-(task)`, so a non-holder yields no edge → no grant. The
  Gate-3 vector (§5) pins this. ✓
- *Could `ClaimTask` be forged by a non-member?* The op validates the claimant↔role `holdsRole` link
  (known-key) before the swap; absent ⇒ `NotAuthorizedToClaim`. ✓
- *Double-claim?* CAS-on-`queuedFor`-presence; second claim rejects (§6). ✓
- *Grant leak after claim?* The `queuedFor` tombstone drops the fan-out for non-claimants on the next
  reprojection (existing soft-tombstone/`emptyBehavior:delete`); the claimant's `assignedTo` carries it.
  ✓ — but **flag for the Steward:** assert the **reprojection ordering** (a non-claimant's grant must
  drop *before* or atomically-with the claim's visibility) in the Fire-1 lens test, since a window where
  both the old role-fan-out and the new `assignedTo` project the grant would transiently widen it. The
  existing `projectionSeq` guard on the guarded `cap.ephemeral.<actor>` key bounds this to a stale-replay
  no-op, but the test should make the ordering explicit.
- *Unbounded fan-out on a huge role?* Bounded by role size; same cost as the standing-permission plane
  (§6 risk). ✓

> **Recommended pre-build step:** a `bmad-party-mode` / adversarial pass on the **grant-narrowing
> reprojection window** (the §10 flag) before Fire 1 builds — the one spot where a fan-out lens could
> transiently over-grant. The rest of the design is conservative reuse of shipped machinery.

---

## 11. Summary for the board

FR28 role-queue + routing fallback, landed against Contract #10 §10.1's anticipated shape: a `queuedFor`
role-queue link + `ClaimTask`, the grant/inbox fanned to role-holders via the existing actor-aggregate
lenses, availability-based routing in `CreateTask`, and an `unroutedTasks` Weaver target for FR29 — three
shippable fires, one uncommitted §10.1 edit, no architectural fork. **📐 awaiting-Andrew (ratification).**
