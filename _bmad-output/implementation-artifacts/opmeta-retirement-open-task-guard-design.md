# Design — Op-meta retirement with open task referents (the zombie-task guard)

**Status: 📐 awaiting-Andrew (ratification)**

> **For Andrew:** a package upgrade/uninstall that deletes an op-meta while open tasks still
> link it `forOperation` currently strands those tasks in both planes (grant projects null
> operationType → undispatchable; inbox row loses its descriptor) — the live Sam-Okafor repro,
> healed only by CancelTask. This design makes the deletion **refuse, loudly, naming the
> referents** (a step-8 guard beside the §8.4 protected guard), adds the key-stable repair
> (`RebindTaskOperation`, same-operationType only), and makes the package author declare a
> move's destination (`MovedOps`) so pkgmgr rebinds survivors automatically — filling the task
> slice of the seam Contract #8 §8.7 explicitly reserved (brainstorm G6).
> **No architectural fork** — every mechanism mirrors an established pattern (§8.4 commit-time
> guard, §2.5.1 bounded enumeration, §8.1 deterministic keys, ratified tombstone-body posture).
> **Frozen-contract edits staged UNCOMMITTED in `main`** (§7): Contract #8 new §8.5 + §8.7
> trim; Contract #2 §2.6 one error-code row (`OpMetaInUse`); Contract #10 §10.1 one line
> (`RebindTaskOperation`).
> **Adversarial pass: run 2026-07-27** (read-only agent, findings folded — the material ones:
> deletion-via-update bypass now in the guard predicate; §2.6 table row added to the contract
> set; the auth plane's status-blindness corrected in §4 and its pre-existing gap filed as its
> own board row: a cancelled/completed task's ephemeral grant stays exercisable until expiry).

**Backlog row:** `[Pkgmgr] An op-meta tombstone orphans the open tasks that reference it`
(lattice.md, Component maintenance · ★★ M). Filed 2026-07-27 from a live repro
([facet-discovery-restoration §6](facet-discovery-restoration-design.md)): a package move
tombstoned `RecordIdentityPII`'s op-meta while an open task still linked it `forOperation` —
the task became an undispatchable zombie, healed only by operator `CancelTask`.

---

## 1. Problem + intent

A task binds the operation it grants by **link**: `lnk.task.<id>.forOperation.meta.<opId>`
(`packages/orchestration-base/ddls.go:361`). `CreateTask` validates the op-meta is **alive at
creation** (`vertex_alive`, ddls.go:277) — and nothing ever re-validates it. When a package
upgrade/uninstall tombstones that op-meta (`internal/pkgmgr/upgrade.go` `diffManifest`: a key in
old\new → tombstone, no referent check), every open task bound to it breaks in **both** planes at
once:

- **Auth plane:** `capabilityEphemeral` link-sources the grant's `operationType` from the op-meta
  (`packages/orchestration-base/lenses.go:315`). The walk drops the tombstoned vertex → the grant
  projects null `operationType` → step-3 `matchEphemeralGrant` (taskKey+operationType+target+
  expiresAt, Contract #10 surfaces changelog 2026-06-02) can never match → the task's op cannot be
  submitted. Auto-complete (`authContext.task=T`) is therefore unreachable too — the task can
  never leave `open` except by `CancelTask`.
- **Display plane:** `myTasks` projects `forOperation: op.key`, `operationName:
  op.data.operationType` (lenses.go:240-241) → null columns; the inbox renders a task with no
  operation, the descriptor path (edge-manifest) is dead.

This is the **task slice of a gap Contract #8 already reserves**: §8.7 *Out of scope* — "a
breaking DDL change during an upgrade while a Loom instance is mid-pattern or a Weaver gap is
open: warned/blocked by a future migration guard (**brainstorm G6**). The upgrade is atomic but
does not today fence in-flight orchestration." G6's proposed shape (brainstorming-session
2026-04-08, #G6) is exactly "migration tool warns on in-flight referents before allowing breaking
changes." This design fills the task-referent slice of that seam. It deliberately does **not**
build G6's full Loom-instance DDL-version pinning (§8 boundary below).

**Intent:** a package author retiring or moving an operation must state what happens to the open
tasks bound to it, and an undeclared retirement must **fail closed, loudly, naming the
referents** — never silently strand work or strand an operator with a zombie.

## 2. Grounded state (what exists that this extends)

- **The commit-time guard pattern (mirror this):** `rejectProtectedMutations`
  (`internal/processor/step8_commit.go:597-`) — authoritative, path-independent, iterates the
  batch's update/tombstone mutations, reads root docs from the commit's prior-doc cache, rejects
  the whole op. Contract #8 §8.4 codifies the split: script-side checks are courtesy; the
  commit-time guard is the authority "regardless of whether the originating script inspected"
  anything.
- **The op-meta recognizer:** op-metas share `class: meta.ddl.vertexType` with ordinary DDL roots
  (`internal/pkgmgr/build.go:23`), but are the only meta roots carrying `data.operationType`
  (build.go:222-232) — the same predicate both engine registries index
  (`internal/weaver/registry.go:1122-1143` "operationType → vtx.meta.<id>").
- **Bounded inbound enumeration:** the link key embeds the target
  (`lnk.task.<taskId>.forOperation.meta.<opId>`), so `lnk.task.*.forOperation.meta.<opId>` is a
  **server-side subject filter** — `substrate.KVListKeysFilter` (kv.go:260-276), paged
  (cursor+limit), bounded by the op's own referent degree. The same primitive class the sanctioned
  §2.5.1 op-time enumeration uses (e.g. ClaimTask's queuedFor resolution, ddls.go:411-425).
- **Task lifecycle:** root `data = {status ∈ open|complete|cancelled, expiresAt}`; lifecycle ops
  flip status; task **roots** are never tombstoned and neither are `forOperation`/`scopedTo` links
  (ddls.go:353, :364 — the *assignment* links do tombstone: ClaimTask drops `queuedFor`
  (ddls.go:460), ReAssignTask the old `assignedTo` (ddls.go:515)) — so a `forOperation` link
  enumeration sees the op's *all-time* tasks and must filter tombstoned links + non-open roots.
  `expiresAt` is normalized to canonical UTC whole-second RFC3339 at CreateTask (ddls.go:265-270);
  `capabilityEphemeral` gates grants on `expiresAt > $now` and step 3 re-checks expiry at auth
  time via `time.Parse` (step3_auth_capability.go:349-360).
- **Tombstone body preservation is ratified permanent posture**
  ([design](tombstone-body-preservation-design.md)): a tombstoned op-meta keeps
  `data.operationType` readable via `kv.Read` — load-bearing here for post-tombstone recovery
  (§5.3).
- **Deterministic entity keys (Contract #8 §8.1):** an op-meta's NanoID derives from
  `package name + entity tag` — a client can **compute** the key an operationType has (or will
  have) in any named package without reading anything.
- **Live-repro heal:** operator `CancelTask` (open→cancelled, ddls.go:527/584-607) — work
  destroyed, but the world unwedged. `RebindTaskOperation` (§4) is the work-preserving sibling.

## 3. The shape — declare the disposition, guard the commit, rebind the survivors

Three mechanisms, one per fire, each independently shippable:

### 3.1 Fire 1 — the commit-time referent guard (authoritative, fail-closed)

A step-8 guard beside `rejectProtectedMutations`, same contract stance (§8.4's split). Trigger:
any **deletion mutation of an op-meta root** — a `tombstone`, **or an `update` whose document
sets `isDeleted: true`** (deletion-by-update is otherwise an open bypass: step 8 preserves a
script's `isDeleted` on updates, and op-meta roots have no governing DDL and no `protected`
flag to stop one) — where the mutation's key is a 3-segment `vtx.meta.<id>` and the mutation's
**own prior doc** carries a non-empty `data.operationType` (the recognizer; aspect mutations
never match — `operationType` lives only on the root, build.go:231-232). A prior doc already
`isDeleted` is skipped (re-tombstoning a dead meta strands nothing new); a prior doc that
exists but cannot be parsed **rejects** (fail closed — the one place the recognizer must not
shrug).

1. Enumerate `lnk.task.*.forOperation.meta.<id>` via `KVListKeysFilter` (the filter is applied
   server-side; the call materializes the matching key list, and the cursor+limit bound the
   per-key follow-up reads, not the listing itself — kv.go:286-331).
2. For each non-tombstoned link, read the task root; count it a **blocking referent** iff the
   root is alive ∧ `status == "open"` ∧ `expiresAt` is in the future — compared by
   `time.Parse(RFC3339)`, mirroring step 3's own expiry check, so legacy non-normalized offsets
   compare correctly; an unparseable `expiresAt` blocks (fail closed).
3. Any blocking referent → reject the whole operation: error code **`OpMetaInUse`**, detail
   naming the op-meta key, its `operationType`, the first N task keys + the total count.
   Short-circuit on the first blocker; the pass path pages to exhaustion.

Path-independent by construction: covers `UpgradePackage`, `UninstallPackage`,
`TombstoneMetaVertex`, and any future script. Expired-but-open tasks do **not** block (their
grant is dead at step 3's own expiry re-check regardless of projection state; blocking
retirement on them would let stale inbox debris wedge upgrades forever) — their null-op inbox
rows are the pre-existing cosmetic state for every expired task, unchanged by this design.

Scope boundary, stated: the filter is `lnk.task.*` because tasks are today's only **runtime**
producer of `forOperation` links (verified — no other constructor in packages/ or the engines).
The `lnk.permission.*.forOperation.meta.*` install-plane links are package-owned and travel in
the same diffs, so they are deliberately not referents. A future runtime source of
`forOperation` links must register with this guard — that is the extension point, and the
guard's test suite pins the task filter so the registration is a conscious act.

Cost note: enumeration is bounded by the op's all-time task-link degree (one root read per live
link) and runs only when an op-meta deletion is in a batch (upgrades/uninstalls — rare,
operator-paced). The eventual keyspace answer for unbounded historical-link growth is the
shelved hard-delete/pruner track, not this guard.

### 3.2 Fire 2 — `RebindTaskOperation` (the work-preserving move)

A new task-lifecycle op in orchestration-base (task DDL `PermittedCommands`, operator/admin
grant — same posture as admin-only `CompleteTask`):

- **Payload:** `{taskKey, toOperation}` (`toOperation` = full `vtx.meta.<NanoID>` key).
- **Declared reads:** task root + `toOperation`; the current `forOperation` link is resolved by
  the sanctioned §2.5.1 bounded enumeration (`kv.Links(task, "forOperation", "out")` — at most
  one live, mirroring ClaimTask's queuedFor resolution), and the **old** op-meta is then read for
  the equality check (read-posture (e) follow-up read).
- **Guards:** task alive ∧ `status == open`; `toOperation` alive, is an op-meta, and
  `toOperation.data.operationType == old.data.operationType` — a rebind relocates the *same*
  operation, never substitutes a different one (a different op is a different task: cancel +
  recreate). The old meta may already be tombstoned — its preserved body (§2) still serves the
  equality check, so recovery works **after** a breakage as well as before one.
- **Mutations:** tombstone the old `forOperation` link conditioned on the revision the
  enumeration read (**the old link is the rebind's OCC epoch** — every rebind of the same task
  contends on that one key, so two concurrent rebinds serialize and the loser gets
  `RevisionConflict`; the vocabulary supports a conditioned link tombstone even though no script
  uses one yet — `expectedRevision` parses op-agnostically, starlark_runner.go:360-368) + create
  `lnk.task.<id>.forOperation.meta.<newId>`; task root untouched (assignee, scopedTo, expiresAt
  all preserved). Event `orchestration.taskRebound {taskKey, fromOperation, toOperation}`.
- **Dispatch surface:** envelope-driven like `CancelTask`/`CompleteTask` (no OpMetaSpec —
  pkgmgr's flow and the operator console submit the op directly; a descriptor entry is added
  only if a UI surface ever wants to offer rebind, which none does today). An old target that
  turns out not to be an op-meta (CreateTask validates only "live `vtx.meta.*`",
  ddls.go:275-278) yields `operationType == None` and the equality check refuses cleanly — such
  a task was born broken and stays a CancelTask case.
- **Projection heal:** both consumers are actor-aggregate lenses with ActorEnumerators installed
  (projection/driver.go:218), and a link CDC event routes to `evalLinkFanOut`, which seeds an
  undirected BFS from **both** endpoints after self-applying the link to adjacency
  (pipeline/evaluate.go:371-421) — task→assignedTo→identity is depth 1, the queuedFor/reportsTo
  variants depth 2, all inside the cap. The mechanism is pinned by
  `refractor_capability_linkfanout_e2e_test.go` (capabilityRoles) and
  `refractor_mytasks_assignedto_e2e_test.go` (myTasks' link trigger); the forOperation-specific
  vector is this fire's e2e. Both lenses project **per-actor whole-doc** rows
  (`cap.ephemeral.<actor>`, myTasks per identity), so the rebind is a single-row overwrite — no
  row-set-shrink retraction hazard.

### 3.3 Fire 3 — pkgmgr: the declared disposition + orchestrated flow

The package Definition gains one optional field: **`MovedOps map[string]string`** —
`operationType → destination package name` — stated on the version that **drops** the op. The
author declares which safe shape the drop is; the bare drop stays default-deny (the lint-gate
philosophy applied to lifecycle):

- **Move (declared):** pkgmgr derives the successor key deterministically from
  `(destination package, operationType)` (§8.1 — no scan, no registry dependency), `KVGet`s it,
  and requires it alive with equal `operationType` — absent → refuse with "install/upgrade
  `<destination>` first" (the flow self-orders: destination before source). Then, for each open
  referent of the dropped op (same filter as the guard), it submits one `RebindTaskOperation`,
  and finally the upgrade. Tasks follow the op; nobody loses their worklist.
- **Retirement (no successor):** default = **refuse at preflight** with the named referents
  (same enumeration, friendlier moment than the commit rejection). An explicit
  `RetireCancelsOpenTasks: []string{opType,…}` opt-in makes pkgmgr submit `CancelTask` per open
  referent first — the destructive path exists, but only spelled out.
- **TOCTOU:** a task created between preflight and commit is caught by Fire 1's guard → the
  upgrade rejects → re-run converges (the same read-time-OCC re-run UX `ErrUpgradeConflict`
  already established; rebinds/cancels are per-task idempotent).
- **Uninstall:** no Definition, so no `MovedOps` — an uninstall with open referents refuses
  (guard + a preflight courtesy naming them); the operator cancels or rebinds explicitly.
  Uninstalling a package with work in flight *should* be loud.

## 4. Reconciliation with the existing mental model

- *Didn't CreateTask already validate this?* At **creation** only (`vertex_alive` on the declared
  read). The gap is temporal: valid-at-birth, never re-checked. This design adds the re-check at
  the only place the binding can break — the op-meta's own tombstone.
- *Didn't we already fence upgrades?* §8.4 fences **kernel** roots (`protected: true`), and the
  per-key OCC fences **concurrent writers of the same keys**. Neither sees a *referent* of a
  tombstoned key. §8.7 explicitly reserved that as future work (G6); this builds the task slice
  of it.
- *Is refuse-while-referenced even our posture?* It already is — Contract #10 §10.1 states it
  for the task's **other** endpoints: "Tombstoning/merging an identity (or role) that holds
  open tasks is rejected (operator reassigns/cancels first)." The op-meta endpoint was the
  missing leg of a committed stance, not a new philosophy.
- *Does this duplicate the Loom/Weaver op-resolution machinery?* No — both engines resolve
  `operationType → current meta` at **task-creation** time (registry index at
  weaver/registry.go:1122-1143, loom/engine.go:930), and neither can strand a task after this
  design: Loom hard-errors on an unresolvable op (`forOperation unresolved`, engine.go:936-939)
  while Weaver classifies the same condition transient and **retries** (strategist.go:198-201 —
  a retired op surfaces as a stuck gap, not a terminal failure). The gap this design closes is
  only the already-created task; the not-yet-reached-step residual (full G6 instance pinning,
  plus Weaver's stuck-gap visibility) stays out of scope, unchanged.
- *New state?* None. No new vertex/aspect/lens; one new op, one optional Definition field, one
  commit-time guard. The disposition state lives where task state always lived (root `status`,
  the links).
- *Why is the guard expiry-aware when the board row said "open"?* Because "open" alone would let
  expired inbox debris (nothing ever transitions an expired task; MarkExpired is a reprojection
  touch, not a status flip) block every retirement forever, and an expired task can never
  authorize anything — step 3 re-checks expiry at auth time regardless of projection state. The
  blocking predicate is therefore **open ∧ unexpired: the intersection of strandable work and
  live grants** — not an echo of the auth plane's own predicate, which is *status-blind*
  (capabilityEphemeral filters only `expiresAt > $now`, lenses.go:284-308, and step 3 matches
  taskKey+operationType+target+expiry only). That status-blindness is itself a pre-existing
  gap this review surfaced — **a cancelled/completed task's grant stays projected and
  exercisable until `expiresAt`** — filed as its own board row; it is orthogonal to this
  design (a closed task has no work to strand, so the guard rightly ignores it).

## 5. Alternatives considered

- **Auto-cancel by default (no guard).** Preserves upgrade fluidity; silently destroys in-flight
  work — a receptionist's task vanishes because a package rolled. Rejected as *default*; kept as
  the explicit `RetireCancelsOpenTasks` opt-in (the variant that beats blanket auto-cancel:
  consent per operationType, named in the package diff).
- **Cancel + recreate instead of `RebindTaskOperation`.** Fails on key stability, not just
  sentiment: a cancelled task is still *alive*, so a CreateTask re-supplying the same `taskId`
  is a defined **no-op** (ddls.go:357-359) — recreation forces a new `taskKey`, which breaks
  Loom's write-ahead `token.<taskKey>` correlation pointer (Contract #10 §10.6) and every
  key-bound reference to the task (inbox rows, manifests). Rebind-in-place is the only
  key-stable repair. Rejected.
- **Bind tasks by operationType name, resolve at read time.** Dissolves the dangling-link
  problem entirely — but it is literally the corrected anti-pattern (`capabilityEphemeral`
  replaced field-sourced `data.grantedOperationType` with the link walk, lenses.go:270-272;
  Contract #10 §10.1 "task relationships are links, not fields"), and it would make the auth
  plane resolve grants through a mutable name index instead of an explicit edge. Rejected.
- **B-side heal sweep** (on op-meta create, re-point open tasks whose target is a tombstoned
  same-operationType meta). Heals *after* a breakage window instead of preventing it — on the
  auth plane a broken window is denial-of-work; and once the guard exists the window never
  opens, so the sweep has no consumer (dead scaffolding). Rejected; `RebindTaskOperation` covers
  post-hoc repair of worlds broken before this ships.
- **A maintained open-referent counter/index per op-meta.** O(1) guard reads, but new state with
  its own staleness + writers (every task transition) — versus an enumeration that is exact by
  construction and runs only at retirement time. Rejected (simplest-extension rule; "do we
  already keep that state?" — no, and we don't need to start).
- **Script-side guards in the three meta scripts instead of step 8.** Not path-independent — a
  fourth script forgets and the hole reopens; §8.4 already litigated this exact split and chose
  commit-time. Rejected.

## 6. Failure modes + races (named, bounded)

- **CreateTask vs concurrent tombstone (cross-lane).** CreateTask's declared `forOperation` read
  is hydrated at its own step-4; a meta-lane tombstone landing between that hydration and
  CreateTask's step-8 commit yields a task the guard never saw (the guard ran before the task
  existed; the create validated a then-live meta). The window is one op's hydrate-to-commit span;
  the batch cannot precondition on a *new* key matching a filter, and the colliding key is new by
  definition. **Accepted + named**: the airtight closure is a read-validity assertion primitive
  (condition a batch on an untouched key's revision) — deferred with this race as its named
  consumer; the zombie it can produce is exactly what Fire 2 repairs.
- **Wedge-by-open-tasks.** Whoever holds CreateTask permission can keep an op-meta blocked.
  Bounded: task creation is permissioned (orchestration-base permissions), every task carries a
  required `expiresAt`, and the guard ignores expired tasks — a wedge decays on its own clock,
  and the refusal names the tasks to cancel.
- **Guard cost on hot ops.** All-time link degree paging at retirement time (§3.1 cost note);
  short-circuit on first blocker. Acceptable at operator cadence; pruner track owns the keyspace
  growth.
- **Malformed `expiresAt`.** Blocks (fail closed) — pre-normalization debris surfaces at
  retirement instead of silently passing.

## 7. Contract surface

- **Contract #8** — new **§8.5 Referential lifecycle guard (op-metas)**: the commit-time guard
  (§3.1) beside §8.4, and `MovedOps` as §8.1/§8.6 Definition surface; **§8.7** shrinks (the task
  slice moves in-scope; Loom-instance DDL-version pinning remains out). Edit staged
  **UNCOMMITTED** in `main` for Andrew.
- **Contract #2 (§2.6 error-code table)** — one row: `OpMetaInUse`. The table is CI-asserted
  bidirectionally (`conformance_errorcode_table_test.go:15-44`), so the code cannot ship without
  the row; Fire 1's plumbing is the established triple — opwire enum (opwire.go:175), envelope
  re-export (envelope.go:57), the `errors.As` branch beside ProtectedKey in commit_path.go
  (:445-459, terminal — `substrate.Term`). Edit staged **UNCOMMITTED**.
- **Contract #10 (substrate §10.1)** — one line adding `RebindTaskOperation` to the task
  lifecycle ops with its same-operationType invariant. Edit staged **UNCOMMITTED**.
- Contract #6 (Capability KV) — untouched: grant field shape `{source, taskKey, operationType,
  target, expiresAt}` unchanged; rebind heals through ordinary reprojection.

## 8. Boundaries (what this deliberately does not do)

- **No Loom-instance DDL-version pinning** (full G6): a pattern step not yet reached that names
  a retired op fails at task-creation — loud in Loom (engine.go:936-939), transient-retry in
  Weaver (strategist.go:198-201, a stuck gap) — the standing posture; pinning (and stuck-gap
  visibility) is a separate design when a real migration demands it.
- **No business-vertex referent guard** (`scopedTo` target tombstoned under an open task):
  business lifecycle is domain semantics owned by packages (P1); the meta plane is
  platform-owned lifecycle, which is why the platform guards it. A domain wanting cascade rules
  writes them in its own scripts.
- **No task janitor** for expired-open debris — pre-existing cosmetic state, unchanged here;
  worth a separate S row only if a PO files demand.

## 9. Test strategy

- **Fire 1:** step-8 unit vectors (op-meta with open task → `OpMetaInUse`; **deletion-via-update
  → same rejection**; complete/cancelled/expired referents → pass; re-tombstone of a dead meta →
  pass; unparseable prior doc → reject; non-op-meta tombstones unaffected; malformed expiresAt
  blocks) + the error-code plumbing (opwire enum, envelope re-export, commit-path `errors.As`
  branch, §2.6 conformance row) + an e2e: install → CreateTask → upgrade dropping the op →
  rejected with named referents → CancelTask → re-run upgrade → clean. Positive vector first
  (the negative-test rule).
- **Fire 2:** script vectors (open-task rebind; equality violation; tombstoned-old-meta rebind
  succeeds; closed task refused) + e2e proving both projections heal (cap.ephemeral grant
  carries the new op key's operationType; myTasks row re-fills) via the link fan-out.
- **Fire 3:** pkgmgr unit (MovedOps key derivation; successor-absent refusal; retire refusal
  names tasks; opt-in cancel flow) + the move e2e: B-then-A upgrade with an open task, task
  follows, zero zombies; plus `verify-package` runs for the touched packages (CI gap rule).

## 10. Decomposition for the Steward

| Fire | Contents | Size | Ships alone? |
|---|---|---|---|
| 1 | `OpMetaInUse` commit-time guard (tombstone + deletion-via-update) + error-code plumbing + tests | S–M | Yes — ends silent zombie creation (refusal + named referents; remediation = existing CancelTask) |
| 2 | `RebindTaskOperation` + projection-heal e2e | S–M | Yes — repairs existing zombies, enables work-preserving moves for operators |
| 3 | `MovedOps` + pkgmgr preflight/flows + move e2e | M | Yes — automates the move; depends on 1+2 |

Dead-scaffolding check: each fire has a live consumer on day one (1: the next package move —
the class already occurred live; 2: the demo world's healed-by-cancel case + operator repair;
3: the standing package-move workflow).
