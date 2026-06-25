# Bug — `my-tasks` lens does not project on task assignment (assignedTo not a reprojection trigger)

**Found:** 2026-06-25 (Steward, live-verifying the LoftSpace applicant task inbox / Increment C).
**Severity (as filed):** ★★★. **Status: ✅ RESOLVED — NOT an engine bug (environmental).** The
`assignedTo` reprojection trigger is **sound**; the original symptom was the stale-accumulated dev-stack
artifact already cleared elsewhere on the board (the Refractor's CDC consumer position drifts on a
many-times-restarted `up-full`). A clean stack projects correctly. A realistic regression test now guards
the path. While confirming this, a **real but lower-severity (★★) gap** was found and fixed in the same
read path (operationName projected null in the real flow — see "Second finding" below).

## Investigation verdict (Steward, 2026-06-25) — four independent lines of evidence

1. **Realistic-ordering e2e PASSES.** A new `internal/refractor/refractor_mytasks_assignedto_e2e_test.go`
   reproduces the real lifecycle — the assignee **identity is written FIRST**, then (seconds later) the
   task vertex + the three links are created as **genuine Contract #1 link envelopes written to Core KV**
   (real CDC, NOT pre-seeded adjacency). The inbound `lnk.task.<id>.assignedTo.identity.<id>` mutation
   reprojects the identity's row and the freshly-assigned task projects. This is exactly the scenario the
   original report said "will fail today" — it passes.
2. **The engine path is correct by construction.** `myTasks` is an `actorAggregate` lens, so it routes
   through `evalLinkFanOut` (not the "no handler registered" ack-skip — that log line is emitted by every
   *non-actor* lens consumer on every link mutation and is normal noise). `evalLinkFanOut` idempotently
   builds adjacency from BOTH endpoints and seeds the actor enumeration from both, so an `assignedTo` link
   reprojects the identity endpoint. The dedicated adjacency Bootstrapper *also* builds the edge from the
   same link CDC. The sibling `capabilityEphemeral` lens uses the identical `(identity)<-[:assignedTo]-(task)`
   pattern and ships working.
3. **`cmd/refractor` wiring is byte-identical to the test** (`IsActorAggregate` → `InstallActorAggregate`
   → `SetActorEnumerator`; same `DeliverLastPerSubject`, same `CoreKVFilter`). No myTasks-specific gap.
4. **LIVE clean-stack confirmation.** `make down` → fresh `make up-full` → `make install-loftspace` →
   created an applicant identity → minted a unit → `CreateLeaseApplication`. Weaver/Loom dispatched two
   userTasks `assignedTo` the **pre-existing** applicant (RecordIdentityPII + SignLease). The `my-tasks`
   bucket (`my-tasks.identity.<applicant>`) projected **both** open tasks within ~2s. The original "2 open
   tasks, 0 lens rows" symptom did not reproduce.

**Root cause of the original symptom:** environmental — same as the board's already-cleared "Refractor
stale-stack non-projection" flag. Operational note: live-verify on a fresh `make down` + `up-full`, never a
long-lived accumulated stack.

## Second finding (★★, FIXED in the same fire) — operationName projected null in the real flow

The self-describing task-content feature projected `operationName: null` / `operationDescription: null`
live, because the lens hopped `op.canonicalName.data.value` — but a dispatched userTask's `forOperation`
points at the operation's **DDL meta-vertex** (`meta.ddl.vertexType`), whose name lives on the **root** as
`data.operationType`; package op DDLs carry **no** `.canonicalName` aspect (only a handful of primordial
metas do). The original e2e masked this by *manufacturing* a `.canonicalName` aspect on the fixture op.

**Fix:** the `myTasks` lens now projects `operationName ← op.data.operationType` (the field every op DDL
carries; the same source `capabilityEphemeral` already reads). `operationDescription` stays a best-effort
`.description`-aspect hop (null when the package authored none — the known authoring nicety). The Loupe
operator inbox (`cmd/loupe/tasks.go`, which reads Core KV directly as the inspector) gets the matching
fallback: prefer the `.canonicalName` aspect, fall back to the root `operationType`. Both the realistic
refractor e2e and a new Loupe `computeTasks` sub-test assert the fallback.

---

## Original report (preserved)

**Severity (as filed):** ★★★ — claimed the per-identity task inbox is non-functional in the real flow.
**Status (as filed):** 📋 Filed — needs design → 3-layer adversarial review → fix. *(Superseded by the
verdict above: the trigger is sound; the real defect was the smaller operationName-source gap, now fixed.)*

## Symptom

On a clean stack with the LoftSpace vertical, create an applicant + a lease application and let Weaver/Loom
dispatch the gaps. Two **open** tasks are created, both correctly `assignedTo` the applicant identity
(`SignLease` scoped to the leaseapp, `RecordIdentityPII` scoped to the identity — confirmed via Loupe
`/api/tasks?status=open`). Yet the `my-tasks` lens (`MyTasksBucket = "my-tasks"`) projects **zero rows**
for that identity — so `loftspace-app`'s `GET /api/tasks?applicant=` (and Loupe's own consumers) show an
empty inbox.

## Root cause

The `my-tasks` lens is **identity-anchored** (`MATCH (identity:identity {key:$actorKey}) OPTIONAL MATCH
(identity)<-[:assignedTo]-(task:task) …`, `packages/orchestration-base/lenses.go`). It reprojects an
identity row **only when CDC touches that identity anchor** — its vertex or aspects, or an *outbound/role*
link the engine already registers (observed live: it reprojects on `holdsRole`, `grantedBy`,
`applicationFor`, `appliesToUnit`, and identity aspect writes like `.ssn`/`.dob`).

It does **not** reproject on the inbound `lnk.task.<id>.assignedTo.identity.<id>` mutation — the Refractor
pipeline logs exactly that:

```
pipeline: link mutation observed but no handler registered   key=lnk.task.<id>.assignedTo.identity.<applicant>
```

In the real flow the identity is created **long before** any task is assigned to it
(`CreateUnclaimedIdentity` → … minutes later … Weaver/Loom `CreateTask`). So when the `assignedTo` link
finally lands, there is no fresh identity-anchor CDC, the `assignedTo` mutation has no registered handler,
and the lens never re-runs to pick up the task. Forcing a later identity-aspect CDC (writing `.ssn`/`.dob`
via `RecordIdentityPII`) *did* re-run the lens (`ruleId=<myTasks>` processed `…ssn`/`…dob`) but it **still
emitted no row** — i.e. even on reprojection the identity's adjacency view did not contain the inbound
`assignedTo` edge. So both the trigger registration **and** the inbound-edge adjacency for `assignedTo`
need to be in scope of the fix.

## Why the e2e test masks it

`internal/refractor/refractor_mytasks_e2e_test.go` builds the `assignedTo` edge **first**, then writes the
identity vertex **last**:

```go
buildEdge("assignedTo", "task", taskID, "identity", identityID)   // edge first
…
// Finally write the identity vertex — the CDC event the lens projects on.
writeVertex(identityKey, "identity", map[string]any{"name": "assignee"})   // anchor CDC AFTER the edge
```

The trailing identity-vertex write is an identity-anchor CDC that fires *after* the edge already exists in
adjacency, so the row projects and the test is green. That write-ordering is **unrealistic** — it inverts
the real lifecycle (identity exists, then gets assigned a task). The test gives false confidence; it should
write the identity **before** the task + `assignedTo` edge and still assert the row projects (that variant
will fail today and is the regression guard for the fix).

## Fix direction (for the owning fire — Refractor owner)

1. Register **inbound** link relations consumed by identity-anchored lenses (`assignedTo`, and audit
   `forOperation`/`scopedTo` too) as **reprojection triggers**, so a `task <assignedTo> identity` link
   mutation reprojects the *target identity's* `my-tasks` row (not only the source task). Today only the
   relations seen live (`holdsRole`/`applicationFor`/…) are wired; `assignedTo` falls through to
   "no handler registered" (`internal/refractor/pipeline/pipeline.go:504`).
2. Ensure the inbound `assignedTo` edge is materialized in the identity's adjacency view at link-creation
   time (the `.ssn`/`.dob`-triggered reprojection finding zero tasks shows the edge isn't there yet).
3. Make the e2e realistic: write the identity **before** the task/`assignedTo`, assert the row projects,
   and add a close-era assertion (already present) — this is the regression guard.

## Not affected / already correct

- **Increment C code is correct** (`cmd/loftspace-app/tasks.go` + FE): the reader is comprehensively
  unit-tested, the live endpoint reads the right `my-tasks` bucket cleanly (P5), and **both completion op
  shapes are verified end-to-end through the real Processor** (`RecordIdentityPII` → `.ssn`/`.dob`,
  `SignLease` → `.signature` aspects landed). The inbox renders the moment the lens projects; this bug is
  the upstream data-source, not the FE.
- `leaseApplicationComplete` (the My Applications tracker source) **does** project correctly on a clean
  stack (it is anchored on the leaseapp and triggers on its own link mutations).
