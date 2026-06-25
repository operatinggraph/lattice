# Bug — `my-tasks` lens does not project on task assignment (assignedTo not a reprojection trigger)

**Found:** 2026-06-25 (Steward, live-verifying the LoftSpace applicant task inbox / Increment C).
**Severity:** ★★★ — makes the per-identity task inbox (and any `my-tasks` consumer: FR28 role-queue,
the LoftSpace + future clinic task inboxes) **non-functional in the real flow**. Reproduced on a
**clean** `up-full` stack, so it is a genuine read-path bug, not environmental.
**Status:** 📋 Filed — needs design → **3-layer adversarial review** (core read-path engine) → fix.
Do NOT patch the Refractor reprojection engine unattended without that review runway.

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
