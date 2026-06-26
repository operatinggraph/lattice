# Design — close an assignTask task when its gap is satisfied

**Status:** 📐 Proposal — mechanism Winston-ratified (implementation decisions resolved below);
the ONE flagged piece is a Contract #10 §10.8 question (gap-*close*-driven dispatch) → `awaiting Andrew`.
Build is its own fire (Weaver core-orchestration change → 3-layer review + `make test-lease-convergence`
heavy e2e).

**Problem (verified live, backlog row "Close assignTask tasks when their gap is satisfied", ★★, S–M).**
An `assignTask` GapAction dispatches a `CreateTask` (a `vtx.task.<id>` userTask, `status:"open"`,
`scopedTo` the subject, `forOperation` the bound op). When the underlying gap later closes via *another
path* — e.g. `SignLease` writes `.signature` directly, closing `missing_signature` — nothing transitions
the task to `complete`. The task vertex stays `status:"open"` forever. An applicant inbox (the shipped
LoftSpace Tasks tab, which reads the `my-tasks` lens filtered to `status='open'`) then shows a
permanently-stale "Sign your lease" item *after the applicant has already signed*. The gap is the source
of truth; the task that actuates it has no closure path on gap-satisfaction.

## Grounding (the code as it is today)

- **Weaver already knows the task id it dispatched.** `internal/weaver/actuator.go:140` —
  `deriveEpisodeTaskID(targetID, entityID, gapColumn, markRevision)` is the deterministic NanoID the
  `assignTask` episode supplies to `CreateTask` (the Contract #10 §10.6 verbatim-`taskId` seam). It is a
  pure function of `(targetId, entityId, gapColumn, markRevision)`.
- **`markRevision` is recoverable at close time.** The §10.3 anti-storm `mark`
  (`internal/weaver/state.go`) is keyed `<targetId>.<entityId>.<gapColumn>`, carries an `Action` field
  (`assignTask` / `directOp` / `triggerLoom`), and is **CAS-created once then deleted** — never updated
  in place (the dispatch *count* is a separate `…state` key). So the mark's **current** KV revision ==
  its **create** revision == the `markRevision` that seeded `deriveEpisodeTaskID`. `markStore.read`
  (`state.go:157`) already returns `(*mark, entry.Revision, …)`.
- **The closure hook point exists.** `internal/weaver/evaluator.go:299` `clearClosedMarks` runs on
  EVERY row update (level-driven, §10.3), iterating the gap columns whose `missing_<g>` is no longer
  true and calling `e.marks.delete(...)` for each. Today it deletes blind. This is exactly where a gap
  transitions open→closed.
- **The transition op exists.** `packages/orchestration-base/ddls.go` — the `task` DDL already exposes
  `CompleteTask` (open→complete) and `CancelTask` (open→cancelled); both assert the task is `open`,
  known-key reads only.
- **The dispatch primitive exists.** `directOp` (Contract #10 §10.8) with `reads=[targetKey]` is the
  ratified path for Weaver to submit a Processor op against a named vertex (used today by
  `TombstoneObject`). `CompleteTask` is the same shape: a directOp against `vtx.task.<taskId>` with
  `reads=[taskKey]`.

## Mechanism (recommended)

Extend `clearClosedMarks`: for each closing gap, **read the mark before deleting it**; if
`mark.Action == "assignTask"`, derive the task id from the mark's revision and **dispatch a
`CompleteTask` directOp** for `vtx.task.<taskId>` (reads=`[taskKey]`), then delete the mark.

```
for each gap col whose missing_<col> is no longer true:
    m, rev, ok := marks.read(targetID, entityID, col)        // NEW: read before delete
    if ok && m.Action == "assignTask":
        taskID  := deriveEpisodeTaskID(targetID, entityID, col, rev)
        taskKey := "vtx.task." + taskID
        reqID   := deriveCloseRequestID(targetID, entityID, col, rev)   // deterministic, redelivery-safe
        dispatch directOp CompleteTask{taskKey} reads=[taskKey] requestId=reqID   // idempotent
    marks.delete(...)                                         // existing
    marks.deleteDispatchCount(...)                            // existing
```

### Resolved implementation decisions (Winston-ratified)

1. **CompleteTask, not CancelTask.** The gap's *fact landed* — the task's purpose is fulfilled, not
   abandoned. `complete` is the honest terminal state; `cancelled` would mis-report a satisfied
   obligation as dropped. (A future "the subject was deleted / the application was withdrawn" path is
   the CancelTask case — out of scope here.)
2. **Idempotent + redelivery-safe.** The `CompleteTask` directOp gets a *deterministic* requestId
   (`deriveCloseRequestID`, namespaced disjoint from the episode's dispatch/task ids, seeded from the
   same `(target,entity,gap,markRevision)`) so an at-least-once `clearClosedMarks` redelivery collapses
   on the Contract #4 `vtx.op.<requestId>` tracker — exactly the §10.6 collapse `CreateTask` already
   relies on. No duplicate transition.
3. **Tolerate "already not open".** If the applicant completed the task *normally* (via the Tasks tab →
   the bound op → the gap closed → this hook also fires), the task is already `complete` and
   `CompleteTask` fails `TaskAlreadyInState`. That failure is **benign** and must be treated as success
   (the desired end-state already holds). Mirror it for an already-`cancelled` task. The directOp
   failure-classification must not alert/retry on these — they are convergence, not error.
4. **assignTask gaps only.** `directOp` / `triggerLoom` gaps spawn no task — the `Action == "assignTask"`
   guard is the discriminator. Gaps that were never dispatched have no mark → `read` returns `!ok` → skip.
5. **Ordering & failure.** Dispatch the close BEFORE `marks.delete` (the revision is needed). If the
   close *publish* fails, do NOT delete the mark this pass (return the deferred-retry decision, as
   `clearClosedMarks` already does on a delete failure) — redelivery re-derives the same requestId and
   re-attempts; the deterministic id makes the retry safe.
6. **The mark read is one extra KV GET per closing gap.** Acceptable — `clearClosedMarks` already does a
   delete + a dispatch-count delete per closing gap; the read is on the same cold path (a gap closes
   once per episode), not the hot violating-row path.

### The ONE flagged item → Andrew (Contract #10 §10.8)

Today the §10.8 action table is **gap-OPEN driven**: a *violating* gap dispatches its action. This adds a
**gap-CLOSE-driven dispatch** — a *satisfied* `assignTask` gap dispatches a `CompleteTask` to retire its
task. That is a new dispatch trigger in the orchestration contract. It is plausibly an in-spirit
extension of assignTask's lifecycle (the grant's natural end), but whether §10.8 should describe a
close-side action is a **frozen-contract call** → `docs/contracts/*`, Andrew's. Build the mechanism
behind this flag; ratify the contract wording with Andrew before committing the contract edit (the code
itself touches no frozen contract — it reuses the directOp/CompleteTask primitives).

## Alternatives considered (and why not)

- **Convergence/lens-driven ("an open task whose gap is satisfied" as its own gap).** The natural
  Lattice pattern, but it needs a cross-entity join — the task's row must see its subject's convergence
  state (task → subject → subject's `weaver-targets` row), across two lens buckets. The full cypher
  engine can't join across read-model buckets; this would need a new engine capability. Heavier, and the
  Weaver-mark path above is exact and cheap because Weaver *already* holds the (task ↔ gap) linkage via
  the deterministic id. Revisit only if a second consumer needs the "stale open task" projection.
- **A CloseTasksForGap op that scans for matching tasks.** Rejected: the `task` DDL is known-key-reads
  only (no lens-output reads, FR-discipline) — an op cannot scan for "open tasks assignedTo X
  forOperation Y". The deterministic-id derive avoids any scan.
- **TTL-only expiry.** The grant already carries `expiresAt` (30d, `assignTaskGrantTTL`), but that
  reaps the grant a month late, not when the fact lands — useless for the stale-inbox symptom.

## Test plan (for the build fire)

- Weaver unit: `clearClosedMarks` with an `assignTask` mark → asserts a `CompleteTask` directOp is
  dispatched for the derived `taskKey` with `reads=[taskKey]` and the deterministic requestId; a
  `directOp`/`triggerLoom` mark → no dispatch; redelivery → same requestId (collapse).
- Idempotency: an already-`complete` / already-`cancelled` task → the directOp's benign-failure path is
  classified as success (no alert, no retry).
- `make test-lease-convergence` extension: drive `SignLease` to close `missing_signature`, then assert
  the §10.8-spawned `SignLease` task transitions `open→complete` (the `my-tasks` lens row drops), and
  the applicant Tasks tab no longer shows it.

## Blast radius / risk

Weaver core-orchestration (the convergence-clearing path every target shares) → full 3-layer adversarial
review + the heavy convergence e2e. The change is additive (a new branch on the existing close path) and
behind the `Action == "assignTask"` guard, so non-assignTask flows are untouched.
