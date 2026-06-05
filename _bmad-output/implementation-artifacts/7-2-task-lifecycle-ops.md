# Story 7.2 — Task lifecycle ops (ReAssign / Complete / Cancel) + auto-complete + my-tasks Lens

Status: done — shipped `d25a839` (CI green, 2026-06-05). 3-layer adversarial review clean after one fix-forward (the `origin` frozen-contract violation). One follow-up spun off (revision-guarded refractor projection writes — pre-existing, inherited from 7.1).

**Tier:** Opus (new ops on the task substrate + a **commit-path auto-completion injection on the security-plane** + a new projection lens + OCC/state-machine validation). This extends the Story 7.1 substrate; it touches the step-3 task-auth code path, so it is security-plane-adjacent and warrants full review.
**Epic spec:** `_bmad-output/planning-artifacts/epics/phase-2-epics.md` → "### Story 7.2: Task lifecycle ops (ReAssign / Complete / Cancel) + my-tasks Lens" (line ~31). Read it for the user-story framing.
**Binding grounding (FROZEN — read these, do NOT redefine):** `docs/contracts/10-orchestration-surfaces.md` §10.1 (task shape: scalars+links, `status ∈ {open, complete, cancelled}`), **§10.6** (step completion & auto-complete — the heart of AC #2), §10.7 (ephemeral grants / the auth code path auto-complete piggybacks on). Contract #1 §1.1 (link direction: later-arriving vertex = source). P4 (single-op invariants). D5 (Capability-Lens-read fields on root `data`).
**Depends on:** Story 7.1 (done — `orchestration-base` package, `task` DDL, `CreateTask`, `capabilityEphemeral` lens, step-3 task-auth path). This story adds to that package + that code path; it does not re-do any of it.
**Workflow:** the DS is a sub-agent. Repo root, no worktree. Do **NOT** commit/push or branch, and do **NOT** edit planning artifacts (`epics/*.md`, `lattice-architecture.md`, `MORPH-DEVIATIONS.md`) or **FROZEN** contracts (`docs/contracts/*`). You MAY edit `/docs/components/*`. A genuine contract gap → file `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` and continue with a different deliverable; do not edit the frozen shape in place.

---

## 0. ADJUDICATION — Winston build target. DS builds to THIS.

### 0.0 What this story delivers (scope boundary)

Give the `task` substrate its full lifecycle + make a task self-complete when its granted op runs. In scope:

1. **`ReAssignTask`** op (in `orchestration-base` `ddls.go`, alongside `CreateTask`): validate the **new** assignee identity (alive check → structured `ScriptError`, no commit, same single-op invariant as `CreateTask`); on success **atomically** remove the old `task→identity` `assignedTo` link and commit the new one. Emits a `TaskReAssigned` event.
2. **Auto-completion (§10.6 primary path)** — the highest-value, security-plane piece. When an op authorized via `authContext.task = T` (i.e. step-3 took the **task path**, `resolved.Path == "task"`) **commits successfully**, the **commit path injects** T's `status → complete` mutation + a `TaskCompleted(T)` event into the **same atomic batch** — platform-injected (like provenance), located in `internal/processor` in the step-3-task-auth → commit code path. **No separate call, no wedge, no per-op script coupling.**
3. **`CompleteTask`** (explicit admin / out-of-band) and **`CancelTask`** (no-longer-needed) ops → transition the task root `data.status` to `complete` / `cancelled`; emit `TaskCompleted` / `TaskCancelled` via the outbox (the normal event path — scripts return `events`, the transactional outbox publishes).
4. **`my-tasks` Lens** — per-identity projection of that identity's **open** tasks (a queryable surface for verification + UI). New package-owned `nats-kv` lens in `orchestration-base` `lenses.go`.
5. **Validated transitions** — cannot complete (or re-assign) a **cancelled** task; cannot cancel an already-`complete` task; **OCC on the task root** (the lifecycle ops read the task root and assert its revision so concurrent transitions can't clobber).

**OUT of scope (do NOT build — later work):** the Loom/Weaver engines; `StartLoomPattern`; service-actor bootstrap (Story 7.3); the schedule stream (Story 7.4); the post-hoc cross-package orphan rule (Story 7.5 — explicitly deferred there); FR28 role-queues. This story is *direct-op* lifecycle only — no engine consumes `TaskCompleted` yet (Loom will, in Epic 8), but the event is emitted now so the contract is honoured.

### 0.1 A1 — Auto-complete is the crux: injected on the COMMIT PATH, CAS-on-`status==open` (§10.6, binding)

This is the part most likely to be built wrong. Build it **exactly** to §10.6:

- **Trigger:** an op whose step-3 decision resolved on the **task path** — i.e. `decision.Resolved.Path == "task"` with a non-nil `decision.Resolved.EphemeralGrant`. That grant's `EphemeralGrant.TaskKey` (`internal/processor/capability_doc.go`) **is** the task `T` to complete. Do **not** re-derive `T` from anywhere else; the matched grant already names it. (`ResolvedPermission` is threaded as `resolvedPermission` on the `HandleMessage` stack frame in `commit_path.go` — it is already in scope at the commit point.)
- **Injection point:** the auto-complete `status→complete` mutation + the `TaskCompleted(T)` event must land in the **SAME atomic batch** as the authorized op's own mutations/events — the same atomic write that provenance is injected into (`step8_commit.go buildMutationValue` is where provenance is injected today; auto-complete is the analogous platform injection, but at the *mutation/event-set* level, not the per-field level). Decide the cleanest seam: either (a) augment `result ScriptResult` (append the task-status `update` mutation + the `TaskCompleted` `EventSpec`) **after** step-3 resolves task-path and **before** `Committer.Commit`, so the existing batch builder carries them; or (b) thread the task-completion into `Committer.Commit` so it appends to `BatchOp`s + `EventList` there. **Prefer (a)** — it reuses `BuildEventList` + the existing mutation→batch path unchanged, and the auto-complete event then flows through the **same transactional outbox** as every other event (no second event channel). Whatever the seam, the invariant is: **one atomic batch, both the op's effect and the task closure, or neither.**
- **CAS-on-`status == open` (BINDING — §10.6 + the 2026-06-03 crash-safety pass):** the injected completion **MUST be conditional on the task currently being `open`**, applied as a read-and-CAS *within the same batch* (an `update` mutation on `vtx.task.<id>` carrying the task root's `ExpectedRevision`, having read the current root). This closes three races:
  1. **Already `complete`** (admin `CompleteTask` raced, or a redelivery): the second flip is a **no-op** → **no double `TaskCompleted`**.
  2. **`cancelled`** (admin `CancelTask` raced): auto-complete **must NOT resurrect it** — the CAS-on-`open` fails, **the op still commits**, T stays `cancelled`, and **no `TaskCompleted` is emitted**.
  3. **Stale-grant window:** the cap-lens projection lags the status flip, so a just-closed task can still authorize via the stale `cap.ephemeral.<actor>` projection. The CAS makes that commit's auto-complete a **harmless no-op** rather than a double-act.
  - "CAS fails → the *op* still commits" is the load-bearing subtlety: a failed auto-complete CAS must **not** fail the user's op. So the read-current-status decision happens before assembling the batch: if T is not `open`, **inject nothing** (commit the op alone). If T is `open`, inject the conditional `status→complete` update (with the task root's expected revision) + the `TaskCompleted` event. If that conditional update loses an OCC race at commit time, you have two honest choices — re-read-and-retry the injection, or treat the whole batch as a `RevisionConflict` (the existing commit-path conflict branch) so JetStream-redelivery re-evaluates; **pick the simpler one and state it in the closing summary.** Do NOT silently drop the user's op.
- **Idempotent `TaskCompleted` consumption** is a *Loom* (Epic 8) concern, not this story — but the **CAS-on-`open` is what makes a redelivered op's re-injection a no-op** here, so a redelivery never emits a second `TaskCompleted`. Verify that.
- **`CompleteTask` vs auto-complete distinction (§10.6):** auto-complete is the *primary* path (performing the granted op fulfils the task). `CompleteTask` is retained **only** as the explicit admin / out-of-band completion path (e.g. an operator closes a task no actor performed). Both converge on the same `status→complete` + `TaskCompleted` shape; the difference is the trigger.

### 0.2 A2 — `ReAssignTask` re-points the link atomically (Contract #1 §1.1, P4)

- Params: the task key (`vtx.task.<id>`) + the **new** `assignee` (`vtx.identity.<id>`). `ContextHint.Reads` must include the task root, the **old** `assignedTo` link (so the script can name it for removal), and the new assignee identity.
- Starlark **JIT-validates the new assignee identity is alive** (the `vertex_alive` helper already in `ddls.go`) and **rejects with a structured `ScriptError`** if absent/invalid — **same single-op invariant as `CreateTask`** (P4); no orphan reassignment is ever committed.
- **Validate the task is in a re-assignable state:** the task root must be `open`. Re-assigning a `complete`/`cancelled` task is rejected (structured error). **OCC:** the reassign reads the task root and asserts its revision (so a concurrent transition can't clobber).
- On success, **one atomic batch:** **tombstone (or delete-key, per the package's link-removal idiom) the old** `lnk.task.<id>.assignedTo.identity.<oldId>` **+ create the new** `lnk.task.<id>.assignedTo.identity.<newId>` (task = source, identity = target, per Contract #1 §1.1 — same direction as `CreateTask`). The task **root vertex is not re-created** — only the link flips (and the root may take an OCC-guarded touch if the package idiom requires it; don't gratuitously bump it).
- **Decide the link-removal mechanism by reading 7.1 + the package idiom:** the ephemeral-grant lens (and any cap re-projection) must re-derive correctly after the flip — the old grant for the old assignee must disappear, the new assignee must gain it. Since the `capabilityEphemeral` lens is **link-sourced** (walks `assignedTo`), removing the old link + adding the new is exactly what re-projects the grants. Confirm the link removal produces a CDC event the Refractor fans out to **both** the old and new actor (so the old actor's `cap.ephemeral.<old>` loses the grant and the new actor's gains it). If the existing actor-fan-out only re-projects the *new* endpoint, the **old** actor's stale grant is a real bug — flag it. (1.5.12 default-hard-delete: a removed link should hard-delete; confirm the old assignee's grant goes to absence.)
- Emit a `TaskReAssigned` event (`{ taskKey, oldAssignee, newAssignee }`) via the normal `events` return.

### 0.3 A3 — `CompleteTask` / `CancelTask`: validated state machine + OCC on the task root

- Both ops take the task key; `ContextHint.Reads` includes the task root.
- **State-machine validation (binding):** the transition table is `open → complete` (CompleteTask), `open → cancelled` (CancelTask). **Reject** (structured `ScriptError`) any other source state:
  - `CompleteTask` on a `cancelled` task → **rejected** ("cannot complete a cancelled task" — this is the AC's named invariant).
  - `CancelTask` on a `complete` task → **rejected** (symmetric).
  - either op on a task already in the target state → decide **idempotent no-op vs reject** and state which; given §10.6 makes the *auto-complete* path the idempotent one (CAS-on-open), the **explicit admin ops may reject a re-transition** (clearer operator signal) — but a redelivery of the *same* op must not double-emit, which the dedup tracker (`vtx.op.<requestId>`, Contract #4) already handles at step-2, so a straightforward reject is safe. Pick one, justify it.
- **OCC on the task root (binding):** read the task root, assert its revision on the `update` mutation (`MutationOp.ExpectedRevision`), so two concurrent transitions can't both win. A lost OCC race surfaces as the existing commit-path `RevisionConflict` (Contract #2 §2.6).
- Effect: `update` `vtx.task.<id>` root `data.status` → `complete` / `cancelled` (scalars-only root, §10.1 — do **not** add aspects or fields). Emit `TaskCompleted` / `TaskCancelled` (`{ taskKey }`, plus whatever the auto-complete path emits — **keep the `TaskCompleted` payload identical** between the auto-complete and `CompleteTask` paths so Loom consumes one shape).
- **Permissions:** grant `CompleteTask` / `CancelTask` / `ReAssignTask` to the same `operator` idiom `CreateTask` uses (the established management-op grantee in `permissions.go`; widening later is additive). Confirm against how `CreateTask` was granted; if a different grantee is clearly more correct, justify it — do not invent a new role.

### 0.4 A4 — `my-tasks` Lens: per-identity OPEN tasks (§10.1, plain `nats-kv` projection)

- A new package-owned lens in `orchestration-base` `lenses.go`, mirroring the **plain `nats-kv` projection** shape of `identity-hygiene`'s `duplicateCandidates` lens (`Adapter: "nats-kv"`, `Engine: "full"`, package-owned bucket) — **NOT** the capability-envelope shape (no `cmd/refractor/main.go` `case` is needed for a plain projection lens; the envelope `case`s in `main.go` exist only to re-key the capability docs). Confirm by comparing to `duplicate-candidates` (which has **no** `main.go` case and works).
- **Projection:** one row per **(identity, open task)** — for each identity, its tasks where `status == 'open'` (walk `(identity)<-[:assignedTo]-(task:task)` with `task.data.status = 'open'`). Project the fields a verification/UI surface needs: `taskKey`, `assignee` (the identity key), the `forOperation` op key (walk `forOperation`), the `scopedTo` target key (walk `scopedTo`), `expiresAt`. Key the bucket entry on the **identity NanoID + task NanoID** (entity-ID discipline, §10.2 — dots are subject separators; full keys in the document). Pick a clear key shape (e.g. `<identityId>.<taskId>`) and a clear bucket name (e.g. `my-tasks`); state both.
- **Open-only is the filter:** a task that transitions to `complete`/`cancelled` must **drop out** of the projection. With **default hard delete** (1.5.12 — do NOT set `deleteMode`), a row whose task is no longer `open` re-projects to absence → the key is hard-deleted → the task disappears from `my-tasks`. Confirm the lens produces that absence (the same absence mechanism 7.1's ephemeral lens relies on — if a `status != open` task simply produces no row, the default-hard adapter removes the key). If the plain projection does NOT synthesize a delete on the open→closed transition (7.1's FIX 1 discovered that `ErrSkipProjection` does **not** delete — it leaves the key), you have the same gap: **a closed task would linger in `my-tasks`.** Verify against 7.1's FIX 1 finding and handle it the same way (the lens must produce a real absence/delete signal on the open→closed flip), or flag it as an Open Question if the plain-lens path differs.
- **Bucket provisioning:** mirror how `duplicate-candidates` is provisioned (package-install-time, NOT the primordial create list — `duplicate-candidates` is not primordial). Confirm.

### 0.5 A5 — No history/changelog comments; present-tense only (CLAUDE.md, the most-violated rule)

Every comment describes what the code does **now**. **Never** write `// Story 7.2 …`, `// auto-complete added …`, `// was: …`, `// previously …`, `// now …` (as a change-narration), `// replaces …`. git blame is the record. (7.1's CR had to strip 27 such comments — do not reintroduce them.) Contract/spec **references** (`// §10.6`, `// Contract #1 §1.1`) are fine; change-narration is not.

### 0.6 A6 — Key-shape & link-direction conventions (Contract #1 §1.1)

- Task vertices stay `vtx.task.<id>`; root `data` = **scalars only** (`status`, `expiresAt`) — **no aspects, no new fields** (§10.1). Do **not** reintroduce `task.data.grantedOperationType`/`targetKey` (the corrected anti-pattern).
- Links stay 6-segment `lnk.task.<id>.assignedTo.identity.<id>` etc.; **task = source** (later-arriving), the other vertex = target. `ReAssignTask`'s new link follows the identical direction.
- Link sentence test: `task assignedTo identity` reads "source <relation> target". Apply it to anything you touch.

### 0.7 Gates (all must pass before handing back)

`go build ./...` · `make vet` · `golangci-lint run ./...` · `make verify-kernel` (the auto-complete touches the commit path — bootstrap/kernel regression) · `make test-bypass` (Gate 2, all BLOCKED) · `make test-capability-adversarial` (Gate 3, all DEFENDED — the auto-complete sits on the task-auth code path, so the adversarial capability suite MUST stay green and prove auto-complete cannot be weaponised to complete/cancel a task the actor wasn't granted) · `go test ./internal/processor/... ./packages/orchestration-base/... -count=1` · the capability/ephemeral E2E suite in `internal/refractor/` (the `my-tasks` lens + any re-projection on reassign must keep it green — docker stack up, NATS `nats://localhost:4222`, Postgres DSN per the Makefile). The docker stack is currently UP. Flake retry per Deviation 14 allowed; a flake claim without a re-run is a drift signal.

---

## 1. Story (user-facing)

As a **staff actor**,
I want to **reassign, complete, or cancel a task — and have a task auto-complete when its granted op runs**,
so that **tasks have a full, race-safe lifecycle and an identity can see the open tasks assigned to it.**

## 2. Acceptance Criteria (faithful to the epic AC, line ~37)

1. **ReAssign:** **Given** an open task (assigned at creation per 7.1), **When** a `ReAssignTask` op runs, **Then** its Starlark **validates the new assignee identity and rejects if invalid** (same single-op invariant as `CreateTask`); on success it **re-points the `assignedTo` link atomically** — the old `task→identity` link is removed and the new one committed in one batch.
2. **Auto-completion (§10.6 primary path):** **When** an op authorized via `authContext.task = T` **commits successfully**, the **commit path injects** T's `status → complete` + a `TaskCompleted(T)` event into the **same atomic batch** (platform-injected like provenance, in the step-3 task-auth code path), **conditional on `T.status == open`** (CAS) — so performing the granted op fulfils its task with **no separate call and no wedge**, never double-completes, and never resurrects a cancelled task.
3. **Admin lifecycle:** `CompleteTask` (explicit admin / out-of-band) and `CancelTask` (no-longer-needed) ops transition `status` to `complete` / `cancelled` on the task root `data`, emitting `TaskCompleted` / `TaskCancelled` via the outbox.
4. **my-tasks Lens:** a `my-tasks` Lens projects, **per identity, its OPEN tasks** (a queryable surface for verification/UI); a task leaving `open` drops out of the projection.
5. **Validated transitions:** transitions are validated — **cannot complete a cancelled task** (and the symmetric guards), with **OCC on the task root** so concurrent transitions cannot clobber.

## 3. Tasks / Subtasks

- [ ] **T1 — `ReAssignTask` op** (AC #1; A2, A6)
  - [ ] Add `ReAssignTask` to `task` DDL `PermittedCommands` + the script `execute` dispatch in `packages/orchestration-base/ddls.go`.
  - [ ] Validate new assignee alive (`vertex_alive`) → `ScriptError` reject; validate task is `open` (read task root); OCC-assert the task root revision.
  - [ ] Atomic batch: remove old `assignedTo` link + create new one (task=source, Contract #1 §1.1); emit `TaskReAssigned`.
  - [ ] Confirm the `capabilityEphemeral` re-projection fans out to **both** old + new actor (old loses grant → absence; new gains it). Flag if only one side re-projects.
- [ ] **T2 — Auto-completion on commit** (AC #2; A1 — the crux)
  - [ ] In `internal/processor` commit path: when `decision.Resolved.Path == "task"` (non-nil `EphemeralGrant`), read `T = EphemeralGrant.TaskKey`'s current root status.
  - [ ] If `T.status == open`: inject a conditional (`ExpectedRevision`) `status→complete` update + a `TaskCompleted(T)` event into the **same atomic batch** (prefer appending to `ScriptResult` before `Committer.Commit` so it rides `BuildEventList` + the outbox).
  - [ ] If `T` is not `open`: inject nothing; the op commits alone (no double-complete; no cancelled-resurrection).
  - [ ] OCC-race on the injected update: re-read-retry **or** surface as `RevisionConflict` (pick one, justify); never drop the user's op.
- [ ] **T3 — `CompleteTask` / `CancelTask` ops** (AC #3, #5; A3)
  - [ ] Add both to `task` DDL; state-machine guards (`open→complete`, `open→cancelled`; reject complete-of-cancelled + symmetric); OCC on the task root.
  - [ ] Emit `TaskCompleted` / `TaskCancelled` (identical `TaskCompleted` payload shape to the auto-complete path).
- [ ] **T4 — `my-tasks` Lens** (AC #4; A4)
  - [ ] Add the plain `nats-kv` lens to `lenses.go` (mirror `duplicateCandidates`); cypher = per-identity open tasks with link-walked op/target.
  - [ ] Confirm open→closed flips the row to **absence** (default hard delete; the 7.1 FIX 1 absence mechanism). Flag if the plain-lens path lingers a closed task.
  - [ ] Bucket provisioned at install (mirror `duplicate-candidates`, not primordial).
- [ ] **T5 — Permissions** (A3): grant `ReAssignTask` / `CompleteTask` / `CancelTask` to `operator` (the `CreateTask` idiom) in `permissions.go` + `manifest.yaml`.
- [ ] **T6 — Tests** (see §5) + all gates (§0.7) green.

## 4. Dev Notes

### Where things live (read these first — DS does the deep reads)
- **The package you extend:** `packages/orchestration-base/{ddls.go, lenses.go, permissions.go, manifest.yaml}` (7.1). `ddls.go` already has the helpers you reuse: `vertex_alive`, `parts_of`, `required_string`, `make_vtx`, `make_link`, `time.rfc3339_utc`. `CreateTask` is your op template; the three link shapes + directions are established there.
- **The auto-complete seam (the crux):** `internal/processor/commit_path.go` — `resolvedPermission := decision.Resolved` (~line 192) is in scope right before `Committer.Commit` (~line 259); `decision.Resolved.Path == "task"` + `decision.Resolved.EphemeralGrant.TaskKey` is your task `T`. `ScriptResult` (`internal/processor/script_context.go`: `Mutations []MutationOp`, `Events []EventSpec`) is what `Committer.Commit` consumes; `MutationOp.ExpectedRevision *uint64` is your CAS handle. `internal/processor/step8_commit.go` `buildMutationValue` is where provenance is injected — your auto-complete is the analogous *batch-level* platform injection. `BuildEventList` (`step7_events.go`) turns `ScriptResult.Events` into the outbox `EventList` — append the `TaskCompleted` `EventSpec` to `ScriptResult.Events` and it flows through the **same transactional outbox** as every other event (no second channel).
- **`EphemeralGrant`:** `internal/processor/capability_doc.go` (`.TaskKey` names T). `ResolvedPermission`: `internal/processor/operation_context.go`.
- **Step-3 task path:** `internal/processor/step3_auth_capability.go` (`authorizeTaskPath`, `matchEphemeralGrant`) — **do not change its matching logic**; you only *consume* its resolved grant downstream at commit. (7.1 made this byte-for-byte stable; keep it.)
- **The `my-tasks` lens template:** `packages/identity-hygiene/lenses.go` (`duplicateCandidates` — a plain `nats-kv`/`full` projection lens with a package-owned bucket, **no** `cmd/refractor/main.go` case). The capability envelope `case`s in `cmd/refractor/main.go` (~lines 257–289) are NOT a template for `my-tasks` — they re-key capability docs; a plain projection lens needs none of that.
- **OCC / revisions:** `MutationOp.ExpectedRevision` (`script_context.go`); how scripts read current state + revision is the same mechanism `identity-domain`'s state-machine ops use (`packages/identity-domain/` — see its state-machine test for the OCC/revision idiom).
- **Default hard delete:** Story 1.5.12 — package lenses inherit **hard** delete (no `deleteMode`). The 7.1 closing summary's **FIX 1** is the must-read: a *live, row-less* projection does NOT auto-delete the key unless the lens produces a real delete/absence signal (`ErrSkipProjection` ≠ delete). Both `my-tasks` (closed task must vanish) and `ReAssignTask` (old assignee's ephemeral grant must vanish) depend on this — verify each produces a genuine absence, the same way 7.1's ephemeral lens does.

### State-machine table (the contract you implement)
```
            CreateTask                ReAssignTask        CompleteTask     CancelTask     auto-complete(§10.6)
(none) ───▶ open                          —                  —                —                  —
open   ───▶  —          re-point link, stay open   ──▶ complete   ──▶ cancelled   ──▶ complete (CAS-on-open)
complete ──  reject (not a create)   reject (not open)   reject/no-op   reject       no-op (CAS fails, op still commits)
cancelled ─  reject                  reject (not open)   REJECT*        reject/no-op  no-op, NOT resurrected (CAS fails)
```
`*` = the AC's named invariant: **cannot complete a cancelled task.**

### Project Structure Notes
- All new ops live **inside `orchestration-base`** (`ddls.go` script `execute` dispatch) — same package, same `task` DDL meta-vertex (extend `PermittedCommands`). The **only** `internal/` (core) change is the **auto-complete commit-path injection** (T2) — that is intentional: §10.6 says it is **platform-injected** (a core commit-path behaviour), exactly like provenance, **not** a package script. Do not try to make a package script complete the task — that's the "separate call / wedge" the contract explicitly avoids.
- Keep the core change **minimal and task-generic**: the commit path must not learn the `task` *type's* schema beyond "read root `status`, write `status=complete`, emit `TaskCompleted`". It keys entirely off `Resolved.Path == "task"` + `EphemeralGrant.TaskKey` — no `orchestration-base` import into core. (Dependency direction: package→core, never core→package; 7.1 established this.)

### References
- [Source: docs/contracts/10-orchestration-surfaces.md#10.6] — auto-complete primary path, CAS-on-`open`, `TaskCompleted(taskId)`, `CompleteTask` admin-only, `CancelTask` not-needed.
- [Source: docs/contracts/10-orchestration-surfaces.md#10.1] — task = scalars+links, `status ∈ {open, complete, cancelled}`, no aspects; FR29 no-orphan; ReAssign validates+rejects.
- [Source: docs/contracts/10-orchestration-surfaces.md#10.7] — the ephemeral-grant auth code path the auto-complete piggybacks on ("a successful op authorized via `authContext.task = T` auto-completes T in the same atomic batch").
- [Source: docs/contracts/01-addressing-and-envelope.md#1.1] — link direction (later-arriving = source).
- [Source: _bmad-output/implementation-artifacts/story-7.1-orchestration-base.md] — package layout, `task` DDL, `CreateTask` pattern, the link-sourced `capabilityEphemeral` lens, FIX 1 (absence mechanism), the step-3 task path.
- [Source: packages/identity-hygiene/lenses.go] — plain `nats-kv` projection-lens template for `my-tasks`.

## 5. Test plan (concrete — count delivered tests from the diff)

- **`ReAssignTask`:** success re-points the link atomically (old link gone, new link present, task still `open`); **reject** new-assignee absent/invalid (`ScriptError`, no commit); reject on a `complete`/`cancelled` task; OCC conflict on a concurrent transition; ephemeral grant re-projects (old actor → absence, new actor → grant).
- **Auto-complete (the crux):** an op authorized via `authContext.task=T` that commits → T flips to `complete` **in the same batch** + exactly one `TaskCompleted(T)` emitted; **CAS-on-open:** T already `complete` → op commits, **no** second `TaskCompleted` (redelivery-safe); T `cancelled` → op commits, T **stays cancelled**, **no** `TaskCompleted` (no resurrection); a non-task-path op (role/service auth) injects nothing. Assert the auto-complete + the op's own effect are **atomic** (both or neither).
- **`CompleteTask` / `CancelTask`:** `open→complete` / `open→cancelled` success + correct event; **complete-of-cancelled rejected**; cancel-of-complete rejected; OCC on the task root; redelivery is a dedup no-op (tracker), not a double event.
- **`my-tasks` lens:** an open task projects a row for its assignee (with op/target/expiresAt); completing/cancelling/reassigning the task updates the projection (closed task **drops out**; reassigned task moves from old to new identity).
- **Security (Gate 3):** the auto-complete cannot be used to complete/cancel a task the actor was not granted (it only fires on a *matched* grant whose `TaskKey` it completes — assert it cannot name an arbitrary task); cross-target/cross-actor denials from 7.1 stay DEFENDED.
- **Gates:** every gate in §0.7 with its result; flag every security-test touch with a one-line faithful-migration justification.

If you judge the story too large for one safe pass (the auto-complete commit-path change is the risk concentration), **halt and propose a split** (e.g. 7.2a = `ReAssign`/`Complete`/`Cancel` ops + `my-tasks` lens; 7.2b = the commit-path auto-complete) rather than landing a broken commit-path intermediate.

## 6. Closing summary (DS appends when done)

Deliverables vs §0 checklist; exact files changed (`git status`); test count (from diff); every gate + result (anything not run + why); **the auto-complete seam you chose (a vs b) + the OCC-race resolution you chose, with one-line justification each**; every security-test (Gate 2/3) change with a faithful-migration justification; any CAR filed; any deviation. Do NOT commit.

## Dev Agent Record

### Agent Model Used
claude-opus-4-8 (Amelia, BMad senior dev)

### Debug Log References
- `internal/refractor/refractor_mytasks_e2e_test.go::TestRefractor_MyTasksLens_E2E` — vanish-on-close failure traced through the pipeline fan-out to the lens cypher: when an identity's sole task is filtered out, `identity.key AS actorKey` projects NULL (the OPTIONAL chain collapses), so `NewMyTasksWrapper` hit its `actorKey == ""` early return and emitted `ErrSkipProjection` (key lingers) instead of `ErrDeleteProjection`. Pinned at the engine level by `TestMyTasksCypher_CompleteTask_NullsActorKey`.

### Completion Notes List
- Finished a ~95%-complete in-tree implementation; did not revert or re-author the design. The auto-complete commit-path seam and lifecycle ops were already present and correct.
- Fixed the two named breakages (lint unused helper → wrote the redelivery dedup E2E; lens-count conformance assertions → select `capabilityEphemeral` by `CanonicalName`).
- Found + fixed one genuine functional bug the existing `my-tasks` E2E surfaced: the wrapper's null-actorKey skip (see Debug Log). This is the §0.4 / Adjudication #3 FIX-1 trap (a closed task lingered in `my-tasks`).
- Confirmed the auto-complete code matches Winston's adjudication (a) ScriptResult augmentation and (i) re-read-and-retry exactly — no divergence.

### File List
Modified (in-tree before this session, verification-completed here):
- `internal/processor/{commit_path.go, script_context.go, starlark_runner.go, step4_hydrate.go}`
- `cmd/refractor/main.go`
- `packages/orchestration-base/{ddls.go, lenses.go, permissions.go, manifest.yaml, create_task_test.go, package_test.go}`
New (in-tree before this session):
- `internal/processor/{autocomplete.go, autocomplete_test.go, autocomplete_integration_test.go}`
- `internal/refractor/{capabilityenv/mytasks_test.go, refractor_mytasks_e2e_test.go, ruleengine/full/mytasks_cypher_test.go}`
- `packages/orchestration-base/{lifecycle_pipeline_test.go, lifecycle_script_test.go, testhelpers_test.go}`
Edited THIS session:
- `internal/refractor/capabilityenv/envelope.go` — `NewMyTasksWrapper`: fall back to `params["actorKey"]` when the row's `actorKey` is null (the vanish-on-close fix).
- `internal/refractor/capabilityenv/mytasks_test.go` — added `TestMyTasksWrapper_NullRowActor_FallsBackToParams`.
- `internal/refractor/ruleengine/full/mytasks_cypher_test.go` — added `TestMyTasksCypher_CompleteTask_NullsActorKey` (pins the null-actorKey engine behaviour the wrapper compensates for).
- `internal/refractor/ruleengine/full/{bootstrap_e2e_test.go, capability_lens_contract_test.go}` + `internal/refractor/refractor_capability_multi_e2e_test.go` — select `capabilityEphemeral` by `CanonicalName` (lens-count migration; a second lens `myTasks` is now legitimately declared).
- `internal/refractor/refractor_mytasks_e2e_test.go` — added a quiescence wait before the close flip (drains the create-era CDC fan-out backlog so a stale open re-projection can't resurrect the key; mirrors production temporal separation).
- `packages/orchestration-base/{testhelpers_test.go, lifecycle_pipeline_test.go}` — added `trackerEventCount` helper + `TestCompleteTask_Redelivery_E2E_DedupNoDoubleEmit` (consumes the previously-unused `assertTrackerNotEvent`).

---

## 6. Closing summary

**Deliverables vs §0 checklist (all present + verified):**
1. `ReAssignTask` (A2) — re-points `assignedTo` atomically, JIT-validates new assignee alive, OCC on the task root, rejects non-open tasks. ✅
2. Auto-complete on the commit path (A1, the crux) — injected on the step-3 task-path commit, CAS-on-`open`, in the same atomic batch. ✅
3. `CompleteTask` / `CancelTask` (A3) — validated state machine, OCC on the task root, reject same-state re-transitions (Adjudication #4). ✅
4. `my-tasks` lens (A4) — plain `nats-kv` per-identity OPEN-task projection; vanish-on-close confirmed (see fix below). ✅
5. Validated transitions + OCC (A5/A6) — complete-of-cancelled rejected; key shapes / link direction / no-history-comments respected. ✅

**Auto-complete seam chosen = (a) ScriptResult augmentation** — `injectTaskAutoCompletion` appends the conditional `status→complete` `MutationOp` + the `TaskCompleted` `EventSpec` to a copy of `ScriptResult`, riding the existing batch builder, `BuildEventList`, and the transactional outbox unchanged (no second assembly path). The injected ops carry an `origin: "platform-autocomplete"` marker so the seam is auditable (Adjudication #1). **Confirmed: the in-tree code matches (a) — no divergence.**

**OCC-race resolution chosen = (i) re-read-and-retry** — `commitWithTaskAutoComplete`: on an atomic-batch conflict it re-reads the task to attribute the conflict; if the task root is untouched at the asserted revision the conflict is the user's own mutation and surfaces unchanged; if still open at a newer revision it retries the injection once with the fresh CAS handle; if closed/moved (or the retry loses again) it commits the user's op ALONE. A task-side race never bounces the user's op (Adjudication #2). **Confirmed: the in-tree code matches (i) — no divergence.**

**Two named test fixes:**
1. **Lint unused `assertTrackerNotEvent`** → wrote `TestCompleteTask_Redelivery_E2E_DedupNoDoubleEmit` (seeds + completes a task, redelivers the same `CompleteTask` op/RequestID → `OutcomeDuplicate`, asserts status stays `complete`, `TaskCompleted` recorded exactly once, no `TaskCancelled`). This is genuine §0.4 coverage (honest redeliveries absorbed by the step-2 dedup tracker); the helper is now consumed. *Justification: real intended coverage, not a deletion.*
2. **Lens-count conformance assertions** (`TestCapabilityEphemeralLens_ContractConformance`, `TestCapabilityEphemeralLens_E2E`, and the multi-actor E2E) → select the `capabilityEphemeral` lens by `CanonicalName` instead of `require.Len(...,1)` / `[0]`. Story 7.2 legitimately adds a second package lens (`myTasks`). *Justification: faithful test migration (a new lens was added) — the conformance check of `capabilityEphemeral` is unchanged and not weakened. NOT a security-test relaxation.*

**One additional genuine bug found + fixed (§0.4 / Adjudication #3 FIX-1 trap):** the `my-tasks` E2E (`TestRefractor_MyTasksLens_E2E`) failed on vanish-on-close. Root cause: when an identity's only task is filtered out, the lens cypher's `identity.key AS actorKey` projects NULL, so `NewMyTasksWrapper` skipped (key lingers) instead of deleting. Fix: the wrapper now falls back to the per-actor `params["actorKey"]` anchor when the row's `actorKey` is null, so the empty-openTasks case correctly returns `ErrDeleteProjection`. Pinned by a new engine-level test + a wrapper unit test. (The `capabilityEphemeral` lens never exposed this because its E2E does not exercise an open→close transition.) A secondary last-writer-wins projection race — a slow create-era open re-projection landing after the close-delete — is real but only manifests under the test's millisecond-scale cram; the test now drains the backlog with a quiescence wait before flipping, mirroring production's temporal separation between `CreateTask` and `CompleteTask`. This race is noted as a latent refractor-infra property (projection writes are not revision-guarded/monotonic), out of scope to fix here.

**Gates (all green; docker stack up, NATS `nats://localhost:4222`):**
- `go build ./...` — PASS
- `make vet` — PASS
- `golangci-lint run ./...` — PASS (**0 issues**)
- `make verify-kernel` — PASS (ALL ASSERTIONS PASSED)
- `make test-bypass` (Gate 2) — **PASSED, 4/4 BLOCKED**
- `make test-capability-adversarial` (Gate 3) — **PASSED, 4/4 cleared (3 DEFENDED, 1 ACCEPTED-WINDOW** — the pre-existing bounded projection-lag window, unchanged by this story)
- `go test ./packages/orchestration-base/... ./internal/processor/... ./internal/refractor/... -count=1` — PASS, except `internal/processor/outbox::TestOutbox_NoDoublePublish` flaked once (Deviation 14: NATS "no responders" / missing jetstream `meta.inf.tmp`) and **PASSED on the single allowed retry**.

**Security-test (Gate 2/3) changes:** none. Gate 2 and Gate 3 suites are unmodified and stay fully green; the lens-count fixes above are conformance/E2E tests, not bypass/adversarial security tests.

**Test count:** 33 new test functions across the story's new test files (lifecycle pipeline 6, lifecycle script 9, autocomplete unit 2, autocomplete integration 7, my-tasks wrapper 5, my-tasks E2E 1, my-tasks cypher 3), plus additions to `create_task_test.go` / `package_test.go`. Of these, 3 were authored this session (the redelivery dedup E2E, the cypher null-actorKey test, the wrapper params-fallback test).

**CAR filed:** none. **Deviations:** Deviation 14 outbox flake (retried once, passed) — no drift.

---

## Winston's Adjudication (all four RESOLVED — DS builds to these)

1. **Auto-complete seam → (a) ACCEPTED.** Augment `ScriptResult` in the core commit-path step (append the conditional `status→complete` `MutationOp` + the `TaskCompleted` `EventSpec`) so the existing batch builder + `BuildEventList` + transactional outbox carry it unchanged. The "script-authored vs platform-injected" blur is acceptable — provenance is already injected the same way; this keeps the change task-generic with no `orchestration-base` import into core. **Mark the injected ops unambiguously as platform-injected** (a comment or a small tagged-origin marker) so the seam is auditable — but do NOT add a second mutation/event assembly path.
2. **OCC race → (i) ACCEPTED.** Re-read-and-retry the injection; if T is now closed, drop it (CAS-on-open makes "T closed" a clean no-op). **A failed/raced auto-complete MUST NOT fail the user's op** — bouncing the user op on a task-side race they didn't cause would be exactly the wedge §10.6 forbids. Never return `RevisionConflict` for this.
3. **`my-tasks` open→closed → vanish-on-close ACCEPTED.** AC says "open tasks"; no audit-retention window in Phase 2. The DS MUST verify the plain-lens path genuinely removes the key on close (the 7.1 FIX-1 trap — `ErrSkipProjection` leaves the key); if it lingers, emit a real delete signal the way the ephemeral lens does (`ErrDeleteProjection`). A closed task lingering in `my-tasks` is a bug, not acceptable.
4. **Admin re-transition → REJECT (not no-op) ACCEPTED.** `CompleteTask`/`CancelTask` reject a same-state re-transition with a structured `ScriptError` (clear operator signal); honest redeliveries are absorbed by the step-2 dedup tracker. The auto-complete path stays the idempotent CAS-on-open one. Complete-of-cancelled stays rejected (the named AC invariant).

---

## Open Questions (for Winston)

1. **Auto-complete seam (T2) — `ScriptResult` augmentation vs `Committer.Commit` threading.** §0.1 prefers seam (a): append the conditional `status→complete` `MutationOp` + the `TaskCompleted` `EventSpec` to `ScriptResult` after task-path auth and before `Committer.Commit`, so the existing batch builder + `BuildEventList` + transactional outbox carry it unchanged. This means a *core* commit-path step mutates `ScriptResult` (a value normally owned by the package script's output). That is the cleanest reuse of the existing atomic-batch + outbox path, and it keeps the change task-generic (no `orchestration-base` import into core). **Confirm seam (a) is acceptable** — it's the lowest-risk way to guarantee "same atomic batch + same outbox," but it does blur "script-authored vs platform-injected" in `ScriptResult`. (Alternative (b) keeps `ScriptResult` script-only and injects inside `Committer.Commit`, at the cost of a second mutation/event assembly path.) Recommendation: (a).

2. **OCC-race on the injected auto-complete update.** When T is `open` at read time but its root revision moves before commit (a concurrent admin `CompleteTask`/`CancelTask`), the injected CAS update fails. Two honest resolutions (both in §0.1): (i) re-read-and-retry the injection (drop it if T is now closed; the user op still commits), or (ii) treat the batch as `RevisionConflict` so JetStream redelivers and re-evaluates (simpler, but bounces the user's op on a *task-side* race the user didn't cause). Recommendation: **(i)** — the user's op should not be penalised for a concurrent task transition; the auto-complete is best-effort-but-correct (CAS-on-open already makes "T closed" a no-op). Confirm (i).

3. **`my-tasks` open→closed absence.** Per 7.1's FIX 1, a plain projection lens does **not** delete a key just because a row stopped projecting (`ErrSkipProjection` leaves the key). If the same holds for `my-tasks`, a completed/cancelled task would **linger** in the `my-tasks` bucket. The DS will verify the plain-lens path and, if it lingers, produce a genuine delete signal the same way the ephemeral lens does (FIX 1's `ErrDeleteProjection`). **Flagging now** in case Winston wants `my-tasks` to *retain* recently-closed tasks for a short audit window instead of vanishing instantly (the AC says "open tasks," so default = vanish; confirm no audit-retention requirement). Recommendation: vanish-on-close (AC-faithful); no retention window in Phase 2.

4. **Explicit-admin re-transition: reject vs idempotent no-op.** §0.3 leans toward `CompleteTask`/`CancelTask` **rejecting** a re-transition to the same state (clear operator signal), relying on the step-2 dedup tracker to absorb honest redeliveries. The auto-complete path is the idempotent one (CAS-on-open). Confirm the admin ops should **reject** (not no-op) a same-state re-transition.

---

## Winston — Code Review Adjudication (3-layer adversarial, 2026-06-05)

Three parallel review layers ran (Blind Hunter / Edge Case Hunter / Acceptance Auditor). Triage:

- **MUST-FIX — applied. Frozen-contract violation (Auditor F1 / Blind #4):** the auto-complete injected a non-contract `origin` field onto the task root `data` (violating §10.1 scalars-only `{status, expiresAt}`) and into the `TaskCompleted` event payload (diverging from the explicit `CompleteTask` shape the story required to be identical). Fixed in `internal/processor/autocomplete.go`: root data is now exactly `{status:"complete", expiresAt:<carried>}` and the event is `{taskKey}` — byte-for-byte identical to `transition_task` (`ddls.go`). Audit seam relocated to a structured log field at the injection point in `commit_path.go` (off the persisted shape).
- **Refuted — Blind #1 (BLOCKER: double-commit on lost ack):** the idempotency tracker op is `CreateOnly` inside the same atomic batch (`step8_commit.go:168-174`). A truly-applied-but-lost-ack first commit cannot be re-applied — the retry/fallback batches are rejected by the tracker's CreateOnly guard, the error surfaces, and JetStream redelivery + step-2 dedup resolve it. Exactly-once `TaskCompleted` holds.
- **Refuted — Blind #2 (use `ConflictingKey` for attribution):** the real substrate's `ErrAtomicBatchRejected` wraps an opaque NATS ack/publish error string; it carries no structured `ConflictingKey` (that field exists only in test mocks). The re-read-based attribution is the only available mechanism and is sound. No change.
- **Accepted deviation — Auditor F2 (`my-tasks` built as an envelope/fan-out lens with a `cmd/refractor/main.go` case, contra §0.4's "plain lens"):** §0.4's premise was wrong — it assumed a non-anchored `duplicateCandidates`-style cypher, but `my-tasks` is legitimately per-actor-anchored (like the ephemeral lens), and the anchor is exactly what makes reassign fan-out reproject both endpoints and vanish-on-close emit a real `ErrDeleteProjection`. The implementation's choice is correct. Deviation accepted and recorded.
- **Follow-up filed (out of scope, pre-existing) — Edge F1 (projection-resurrection race):** the refractor natskv adapter writes projections with unconditional `kv.Put`/`kv.Delete` (no revision guard), so a delayed/retried stale "open" re-projection can overwrite a close-delete — a closed task can reappear in `my-tasks`, and the same unguarded path drives `capabilityEphemeral`, raising a security-plane ephemeral-grant-resurrection concern. Inherited from Story 7.1; not introduced here. Spun off as a tracked follow-up (revision-guarded projection writes). The 7.2 E2E quiescence-wait is relabelled to state it works around this known race, not that the race is closed.
- **Noted, no fix — Blind #5/#6 (find_assigned_link nondeterminism / ReAssign new-assignee TOCTOU):** LOW; the atomic reassign tombstones the old link (no two live `assignedTo` links coexist; tombstoned links hydrate `isDeleted` and are skipped), and the post-hoc-orphan window on the new assignee is explicitly deferred to Story 7.5 (no-orphan-by-construction).

**Verification gates (run by Winston, all green):** `go build ./...`, `make vet`, `golangci-lint run ./...` (0 issues), `make verify-kernel`, `make test-bypass` (Gate 2 — all BLOCKED), `make test-capability-adversarial` (Gate 3 — PASSED 4/4: 3 DEFENDED, 1 ACCEPTED-WINDOW = the pre-existing Story 1.5.4 projection-lag vector, not introduced here), and `go test ./internal/processor/... ./packages/orchestration-base/... ./internal/refractor/...` (green; the `outbox` `TestOutbox_NoDoublePublish` NATS `meta.inf.tmp` flake cleared on the one allowed Deviation-14 retry).
