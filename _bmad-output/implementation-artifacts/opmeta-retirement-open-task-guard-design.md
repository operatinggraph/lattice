# Design — Op-meta retirement with open task referents (authorship-time disposition)

**Status: 🗄️ shelved (Andrew, 2026-07-27 ratification) — low priority; revive on a zombie-task
recurrence or the first package move that must preserve in-flight work.**

> **Ratification outcome (Andrew, 2026-07-27).** The original draft proposed a three-fire build
> centered on an authoritative step-8 commit-time guard. Andrew rejected that shape on three
> grounds, all correct: (1) **priority** — one observed occurrence, operator-visible and healed
> in minutes by `CancelTask`, does not justify M-scale machinery (recency bias); (2) **the
> disposition belongs to upgrade authorship** — cancel-vs-migrate is the author's decision,
> made when the package version is written, not discovered through a Processor rejection;
> (3) **the guard would have put a link enumeration in the Processor's write path** — bounded
> only by the op's *all-time* task-link degree (completed tasks never shed links), running on
> the **serial** meta lane. The write-path no-scans invariant exists precisely to forbid this;
> Contract #10 §10.1 already refuses role-member enumeration at CreateTask on the same ground.
> The §8.4 protected-guard precedent did not transfer: commit-time guards are for **security
> invariants** that must hold against any author; lifecycle hygiene belongs to the **authoring
> tool**. This doc is rewritten to the surviving shape (below); the staged contract edits
> (#2 §2.6 `OpMetaInUse`, #8 §8.5, #10 §10.1 rebind) were reverted un-committed.
> **Caveat (Andrew, same session):** the **decision** is authorship-time; the **enumeration**
> is apply-time and environment-relative (dev and prod see different open-task sets). So the
> declaration is unconditional policy required on every op drop, and the refusal never keys on
> the authoring environment's referent count — otherwise a referent-free dev apply goes green
> and hides the prod refusal.

**Backlog row:** `[Pkgmgr] An op-meta tombstone orphans the open tasks that reference it`
(lattice.md, Component maintenance · ★★ → shelved). Live repro 2026-07-27
([facet-discovery-restoration §6](facet-discovery-restoration-design.md)): a package move
tombstoned `RecordIdentityPII`'s op-meta while an open task linked it `forOperation`; the task
became undispatchable; operator `CancelTask` healed it.

---

## 1. Problem (grounded, unchanged by the rework)

A task binds the operation it grants by link (`lnk.task.<id>.forOperation.meta.<opId>`,
ddls.go:361). `CreateTask` validates the op-meta is alive **at creation** (`vertex_alive`,
ddls.go:277); nothing re-validates later. When a package upgrade/uninstall deletes that op-meta
(`upgrade.go` `diffManifest`: old\new → tombstone, no referent awareness), an open task breaks
in both planes at once:

- **Auth:** `capabilityEphemeral` link-sources the grant's `operationType` from the op-meta
  (lenses.go:315); the walk drops the tombstoned vertex → null `operationType` → step-3
  `matchEphemeralGrant` can never match → the op cannot be submitted, so auto-complete is
  unreachable — the task can never leave `open` except by `CancelTask`.
- **Display:** `myTasks` projects `op.key` / `op.data.operationType` (lenses.go:240-241) →
  null columns; the inbox row loses its operation, the descriptor path is dead.

Severity in practice: **visible, rare, recoverable.** The row renders (with null op), the
operator cancels, the world moves on. This is hygiene, not a security hole — which is why the
disposition belongs to authorship (below) and no commit-path machinery is warranted.

## 2. The surviving shape — one fire, pkgmgr preflight only, Processor untouched

**The package author declares the disposition; pkgmgr enforces it at authoring time.** All
enumeration happens client-side in pkgmgr (a platform binary, Core-KV read-sanctioned,
off-lane — no serialization cost anywhere), using the diff it already computes:

**The decision and the enumeration are split across time and place (Andrew's caveat):** the
*decision* is versioned policy in the package — made once, at authorship; the *enumeration* is
an apply-time act against whichever environment is being upgraded, and its result varies (dev:
usually zero referents; prod: real in-flight work). The declaration is therefore
**unconditional — required on every op drop** — and the refusal never keys on the current
environment's referent count: gating on "referents exist here" would let a referent-free dev
apply go green and surface the missing declaration first in prod.

1. **Declaration check (environment-independent):** a delta that tombstones an op-meta
   (recognizer: 3-segment `vtx.meta.<id>` whose committed doc carries `data.operationType` —
   build.go:222-232) without a declared disposition for that operationType → **refuse to
   submit**, in every environment, with the error naming the operation and the two
   declarations. The bare drop is default-deny at authorship, where the author is present to
   decide.
2. **Enumeration (apply-time, environment-relative — sizes the remediation, never the
   decision):** enumerate `lnk.task.*.forOperation.meta.<id>` via `KVListKeysFilter`; a
   referent counts iff its link is live and its task root is alive ∧ `status == "open"` ∧
   unexpired (`time.Parse`, mirroring step 3's expiry check; unparseable counts).
3. **Declared dispositions** on the Definition of the version dropping the op:
   - `RetireCancelsOpenTasks: [<operationType>, …]` — pkgmgr submits the existing `CancelTask`
     per enumerated referent (ddls.go:527 — status flip, OCC on the task root) — 0..N per
     environment — then the upgrade.
   - `MovedOps: {<operationType>: <destinationPackage>}` — **deferred** (see §3); declaring it
     today fails preflight with "work-preserving moves are not yet supported — cancel or wait".
     The vocabulary is reserved so the declaration surface doesn't churn when the move path
     lands.

**Residual windows, accepted by priority call (Andrew 2026-07-27), both visible + healable:**
a task created between preflight and commit (cross-lane, the hydrate-to-commit span) can still
strand; a manual `TombstoneMetaVertex` bypasses pkgmgr entirely (operator-driven — the operator
owns the consequence). Both produce the known null-op row, healed by `CancelTask`. If either
recurs enough to matter, that recurrence is this row's revive trigger — and the answer will
still be authorship-side (a preflight verb for the console), not a Processor scan.

## 3. Deferred with named consumers (not built, on purpose)

- **`RebindTaskOperation`** (key-stable re-point to a same-operationType successor; the old
  link's read revision as OCC epoch). Named consumer: the first package move that must
  **preserve** in-flight tasks — none exists; the one live case was correctly served by cancel.
  Worth knowing when it revives: cancel+recreate is *not* a substitute (a cancelled task is
  still alive, so a same-id CreateTask no-ops, ddls.go:357-359, and a new key breaks Loom's
  write-ahead `token.<taskKey>` pointer, Contract #10 §10.6) — rebind-in-place is the only
  key-stable repair, and both consumers heal via ordinary link fan-out (actor-aggregate
  ActorEnumerators, `evalLinkFanOut` BFS from both endpoints, pipeline/evaluate.go:371-421).
- **`MovedOps` execution** — successor key derivable client-side from §8.1 deterministic ids
  (`entityNanoID(destPkg, "opMeta:"+opType)`, installer.go:261): no scan, no registry needed,
  and the flow self-orders (destination absent → "install it first"). Rides with rebind.
- Contract touch-ups ride the revive too: a `#8` client-preflight note and a `#10 §10.1` rebind
  line — **no contract edit exists today** (the 2026-07-27 staged edits were reverted).

## 4. Alternatives considered (including the one Andrew rejected)

- **Step-8 commit-time referent guard (`OpMetaInUse`) — the original recommendation, REJECTED
  at ratification.** Wrong on two axes, kept here so it is not re-proposed: (a) it bends the
  write-path no-scans invariant — the enumeration is bounded by *all-time* task-link degree
  (links are never pruned; the pruner is the shelved hard-delete track) and would run on the
  serial meta lane, stalling every meta op behind it; (b) it moves an authorship decision to
  the wrong actor at the wrong time — the §8.4 mirror transfers only for security invariants
  (protected roots must hold against a hostile author); lifecycle hygiene is decided *by* the
  author, so the authoring tool is the enforcement point. Same lesson class as the RLS-anchor
  precedent mis-transfer: same word ("guard"), different job.
- **Auto-cancel by default.** Silently destroys in-flight work on every package roll. Rejected;
  survives only as the explicit per-operationType `RetireCancelsOpenTasks` declaration.
- **Bind tasks by operationType name, resolve at read time.** Dissolves dangling links but is
  the corrected anti-pattern (lenses.go:270-272; Contract #10 §10.1 "task relationships are
  links, not fields") and would route the auth plane through a mutable name index. Rejected.
- **B-side heal sweep** (re-point on successor install). Machinery with no consumer while moves
  are rare; heals a window the preflight mostly prevents. Rejected (dead-scaffolding test).
- **Open-referent counter/index per op-meta.** New state + new writers on every task transition
  to make a rare authoring-time check O(1). Rejected.

## 5. Test strategy + decomposition (the one fire, when revived or picked up cheap)

Single fire, S–M, entirely in `internal/pkgmgr` + `packages/orchestration-base` verify:
preflight unit vectors (undeclared drop → refusal **even with zero referents** — the dev-green
trap pinned; declared cancel + zero referents → clean apply; declared cancel + N referents →
CancelTasks submitted then upgrade; complete/cancelled/expired referents don't count;
unparseable expiresAt counts; MovedOps declared → explicit not-yet-supported refusal) + one
e2e: install → CreateTask → upgrade dropping the op → preflight refusal → re-run with
`RetireCancelsOpenTasks` → task cancelled, upgrade lands, zero zombies. Processor: zero
changes; no new error codes; no contract edits.

## 6. Review record

- Adversarial pass (read-only agent, 2026-07-27, pre-ratification): confirmed the fan-out heal
  mechanics, the MovedOps key derivation, the race characterization, and tombstone-body
  readability; found the deletion-via-update bypass and the §2.6 table obligation (both moot
  for the shelved shape — no Processor guard exists to bypass); surfaced the independent
  finding filed as its own board row: **a closed task's ephemeral grant stays exercisable
  until `expiresAt`** (`capabilityEphemeral` filters only expiry; step 3 never checks status).
- Ratification (Andrew, 2026-07-27): shape redirected to authorship-time, priority set low,
  row shelved — folded above and into the Designer skill (§2 reflex: enforcement-point choice
  + right-sizing to observed demand).
