# Kernel-orphan retirement — reconcile's missing third verb

**Status:** 📐 **awaiting-Andrew (ratification)**

**Component:** `internal/bootstrap` (+ `scripts/verify-kernel.go`, `scripts/lint-conventions.go`).

**Board row:** `[Bootstrap] Reconcile creates + updates but never removes a retired kernel key`
([backlog/lattice.md](../planning-artifacts/backlog/lattice.md), Component maintenance).

**Supersedes the residual** filed at [kernel-seed-reconcile-design.md](kernel-seed-reconcile-design.md) §8.

**Adversarial pass:** run this fire, three read-only reviewers — §12. It reshaped the design twice (scope
narrowed to whole-entity; the demand case rewritten). Read §12 before §9 if you want the short version of
why this doc is more cautious than its title suggests.

---

## For Andrew

**What it does, in two lines.** `ReconcilePrimordial` creates and updates kernel keys but never removes
one, so a `vtx.meta.*` **entity** this binary no longer builds stays live and executable in a long-lived
bucket. This adds the third verb: tombstone each meta-vertex that **positively proves it is kernel-owned**
(`createdByOp == BootstrapOpKey`) and whose **root** is absent from the set this binary builds.

**No architectural fork. No frozen-contract change** (§7 explains the one judgment call you may overrule).

**Three things worth your attention:**

1. **The board row's premise — "needs a provenance marker" — is wrong, and that is most of the design.**
   The marker already exists, is contract-mandated (Contract #7 §7.2 item 9), cannot be forged by any
   script (`step8_commit.go:457-460` *deletes* script-supplied values before stamping), and is
   **retroactive** — every kernel key ever written in this deployment already carries it. A *new* marker
   could not have reached the pre-existing orphan population, which is the only population this exists
   for. So: no new state, no migration, no backfill.

2. **The real gap is a tension nobody had written down.** Reconcile was built so a kernel *change* lands
   without `make down` (*"a shared dev stack and a demo box cannot casually be wiped"*). But a kernel
   *shrink* today has no wipe-free path: either the author bumps `lattice.bootstrap.json`'s version — and
   the deployment **refuses to boot** until someone wipes it, the exact remedy reconcile exists to
   eliminate — or the author doesn't, and the orphan lives forever. **Retirement is what makes a shrink
   wipe-free, exactly as reconcile made a change wipe-free.** That is a better reason to build this than
   the one on the board row, and it is the reason I still recommend it after the pass below.

3. **The adversarial pass cut the scope in half, and you should know what it cut.** I originally designed
   per-**aspect** retirement and called the partial shrink "the more common shape". It is — and it is
   also the one case where retirement is either **useless or harmful**: no Processor-side consumer honours
   an aspect-level `isDeleted` (`ddl_cache.go` checks the root only, then reads `.script` /
   `.permittedCommands` through structs with no `isDeleted` field — and the tombstone *preserves the
   body*, so a "retired" script keeps executing while reconcile reports converged), and a `.spec`-only
   tombstone latches a permanent red Refractor health card. **Retirement is now whole-entity only**;
   orphan aspects are *reported*, never written. §12.1 has the evidence.

**Demand is unmeasured and I say so.** This was a doc-only fire — no live bucket read. Inc 1 is
non-destructive and exists to produce the number; §9 makes Inc 2 **conditional** on it. If Inc 1 reports
zero on the demo box and the shared dev stack, shelve Inc 2 with the trigger named. I would rather hand
you a measurement than a mechanism.

---

## 1. Problem + intent

`ReconcilePrimordial` ([reconcile.go:183](../../internal/bootstrap/reconcile.go)) brings a long-lived Core
KV into agreement with the kernel the running binary builds. It has two verbs:

- **create** — a key in the built set the bucket lacks (`plan.missing`, `reconcile.go:121`).
- **update** — a key in the built set whose stored body differs (`plan.stale`, `reconcile.go:136`).

Both iterate `buildPrimordialEntries()` (`reconcile.go:107`). **Every key reconcile can reason about is a
key this binary builds.** A key the binary *no longer* builds is never visited, never compared, never
touched. The kernel can grow and it can change; it cannot shrink.

### 1.1 What a surviving orphan actually does

`vtx.meta.*` is the platform's **executable** self-description, so a retired kernel definition is not inert
residue — it is a live code path with no maintainer:

- **A retired kernel lens keeps being loaded.** `lens.CoreKVSource` subscribes to `vtx.meta.>` and routes
  every `meta.lens` envelope to the loader (`corekv_source.go:63-68`); nothing consults the built set. The
  lens gets a pipeline, a durable, an ack floor and a Health-KV card. The 2026-08-01 orphan-durable janitor
  (`73050cc5`) reaps a durable whose **lens is gone** and `714cefd6` tears a pipeline down on a lens
  tombstone — but for a retired *kernel* lens, nothing makes it *become* gone. **This design supplies the
  tombstone those two shipped reapers are already waiting for**, which is why it adds no teardown logic.
- **A retired kernel DDL stays dispatchable.** `DDLCache` drops a meta-root only on `rootDoc.IsDeleted`
  (`ddl_cache.go:191`); absent-from-source is not absent-from-cache. The *stored* script — the one the
  [kernel-seed-reconcile](kernel-seed-reconcile-design.md) fire proved can lag `main` by days — stays
  executable via `ops.meta.>`.
- **A retired kernel canonicalName stays reserved.** `checkCanonicalNameCollision`
  (`installer.go:640-693`) scans live `vtx.meta.*.canonicalName` and rejects any package declaring a
  colliding name, skipping only tombstoned ones (`installer.go:685`). So an orphan **blocks a package from
  claiming a name the kernel has given up** — and moving kernel entities *into packages* is precisely the
  committed direction (Story 4.7; Decision #10, `docs/components/bootstrap.md`).

That third consequence is what turns this from hygiene into a correctness gap: the kernel-minimization
migration is silently blocked on any bucket carrying an orphan.

### 1.2 Reachability — when an orphan can actually exist (rewritten after §12.2)

The first draft of this section asserted a concrete historical population: the five RoleMgmt DDLs removed
by Story 4.7 (`7f6d583c` / `5060fda6`) surviving in old buckets. **That claim is wrong and is struck.**
The mechanism that kills it:

- `lattice.bootstrap.json` carries a version, and `checkVersion` (`nanoid.go:461-471`) accepts **only
  `"16"`**, erroring otherwise with *"run `make down && make up`"*. The error **propagates**
  (`nanoid.go:342-343`) — it does not silently regenerate.
- So a deployment whose file predates a bump **cannot boot the current binary at all**; the operator must
  `make down`, which removes the containers *and* the file together (`Makefile:362-367`, volumes
  ephemeral). Bucket and file are never cleared independently.
- `5060fda6` was itself a version bump. **Any bucket old enough to hold RoleMgmt orphans was force-wiped
  long ago.**

What *is* reachable, precisely:

| Kernel change | Version bump? | Orphan survives? |
|---|---|---|
| **Add** an entity | **Forced** — `populate` (`nanoid.go:515+`) validates all 28 IDs and errors on an empty one, so an old file fails the new binary | n/a (no removal) |
| **Remove** an entity, author **bumps** | yes, by convention | **No** — deployment refuses to boot → forced wipe |
| **Remove** an entity, author **does not bump** | no — `encoding/json` ignores the now-unknown key (no `DisallowUnknownFields` anywhere in the package) and `populate` only validates fields the *current* struct declares | **Yes** |
| **Remove an aspect** from a live entity | never — no ID field is involved | **Yes**, but out of scope (§3.4) |

So whole-entity orphans are **contingent on an author not bumping a hand-maintained switch** — and nothing
enforces the bump: `checkVersion` is a literal `switch` with no test or lint tying it to the struct.

**This is the honest demand statement, and it is weaker than the board row implies — but it is also the
argument for building.** The bump is the wrong remedy: forcing `make down` on every kernel shrink
re-imposes exactly what reconcile was built to remove (`reconcile.go:145-149`). The coherent posture is
**"a removal must not need a version bump, because retirement handles it"** — which makes this design the
missing half of the reconcile fire rather than a nice-to-have. §6.7 rejects the alternative (enforce the
bump) on those grounds.

---

## 2. Grounding ledger (verified `file:line`, this fire)

Every row cites the code that **does** the thing, never a comment describing it. Independently
re-verified by a third reviewer (§12.3).

| Claim | Evidence |
|---|---|
| Reconcile only ever visits keys the binary builds | `reconcile.go:107,112` — `planReconcile` ranges `buildPrimordialEntries()` |
| `buildPrimordialEntries()` is the **total** kernel Core-KV write set | Sole source for both writers: `primordial.go:339` (`SeedPrimordial`), `reconcile.go:107`. `internal/bootstrap` has exactly three Core-KV write sites — `primordial.go:359` (`AtomicBatch`), `:385` (`seedPrimordialPerKey` fallback, fed the *same* slice), `reconcile.go:242`. `primordial.go:1264` writes Health KV, not Core KV. `lenses.go`/`meta_ddl.go`/`install_ddl.go` are builders it calls; `system_actors.go` is read-only. |
| Every kernel key carries `createdByOp = BootstrapOpKey` | `envelope.go:19,34,54` — all three `Make*Envelope` helpers pass it to `substrate.NewDocumentEnvelopeAt`, which sets `CreatedByOp` (`substrate/envelope.go:89`). Contract-mandated: **#7 §7.2 item 9**. |
| No **script** can forge `createdByOp` | `step8_commit.go:384` lists it immutable; `:458-460` **deletes** any script-supplied value *before* `:472` stamps the real tracker key. A structural erase, not a convention. (One non-script path exists — §4(b).) |
| A tombstone preserves `createdByOp` | `step8_commit.go:414-418` copies the prior document whole; only `isDeleted` + the `lastModified*` triplet change. A retired key stays identifiable as kernel-owned. |
| `BootstrapOpKey` is per-deployment, generated + persisted | `nanoid.go:1-28`, `generate()` at `:473-513` mints all 28 IDs; persisted to `lattice.bootstrap.json`, deleted by `make down`. Contract #7 §7.3. |
| Kernel metas are `protected: true` | `primordial.go:501,635,648,664,683,877,1028`. Per Contract #7 §7.2 item 7 / #6 §6.1 this is **anti-brick immutability against ops**, explicitly *not* a capability designator — it does not bind the seeder. It **does** block any op from updating a kernel meta (`rejectProtectedMutations`, `step8_commit.go:604-631`), which is what stops a package inheriting kernel provenance (§4(c)). |
| The write guard is per-key OCC on a NATS-KV atomic batch | `reconcile.go:233-239` — `substrate.BatchOp{HasRevision, Revision}`; `substrate/batch.go:148-151` sets `Nats-Expected-Last-Subject-Sequence` — **per-subject**, not whole-stream. |
| A bounded prefix listing exists, on the handle `planReconcile` already holds | `substrate/kv.go:229-246` (`KVListKeysPrefix`) discards a partial result on ctx expiry (`:245`). `planReconcile` takes a `jetstream.KeyValue` and can call `ListKeysFiltered(ctx, "vtx.meta.>")` on it directly — no `Conn` threading needed. `kv.go:181-182` names *"Core KV's meta-vertex sub-set"* as an explicitly bounded key set. |
| A lens **root** tombstone tears the pipeline down | `corekv_source.go:473-483` |
| A tombstoned canonicalName stops blocking a package | `installer.go:685-687` — `if env.IsDeleted { continue }` |
| **`ddl_cache` honours `isDeleted` on the ROOT ONLY** | `ddl_cache.go:191` is the only check; `:205/:233/:249/:268` read `.canonicalName`/`.permittedCommands`/`.sensitive`/`.script` into structs with **no `isDeleted` field**. This is why §3.4 refuses aspect-level retirement. |
| **`registry_probe` counts a lens declared on the ROOT ONLY** | `registry_probe.go:201` — `if vp.Class != "meta.lens" \|\| vp.IsDeleted { continue }`; the `.spec` fetch (`:204-214`) is deliberately fail-closed and never tests `isDeleted`. Second reason §3.4 refuses aspect-level retirement. |
| The primordial set is ~76 entries | `primordial.go:302` — *"Total ≈ 76 Core KV entries"*; `docs/components/bootstrap.md` says "~75". So a `vtx.meta.>` listing is a cheap boot-time read. |

---

## 3. The shape

### 3.1 The discriminator

A key is **retired kernel** if and only if **all** of the following hold, each established by a read of
that key (or its root) alone:

1. It is under `vtx.meta.` — mirroring `isKernelDefinition` (`reconcile.go:62`).
2. **Its meta-vertex ROOT (`vtx.meta.<id>`) is absent from `buildPrimordialEntries()`'s key set.** Whole
   entities only — see §3.4.
3. It is itself absent from the built set.
4. Its stored envelope parses and `createdByOp == BootstrapOpKey`.
5. Its stored envelope has `isDeleted == false`.
6. It is **not** an auth-plane lens (§3.6).

Anything else **keeps**, with no write and no error:

| Observation | Verdict | Why |
|---|---|---|
| `createdByOp` is some other op tracker | **keep** | A package's or operator's meta-vertex. The load-bearing negative case. |
| `createdByOp` absent / envelope unparseable | **keep** | Unknown provenance is not kernel provenance. |
| `KVGet` returns an error | **keep** | A failed read licenses nothing. |
| `isDeleted == true` | **keep** | Already retired, or operator-tombstoned. Idempotence. |
| Present in the built set | **keep** | Live kernel — the existing create/update path owns it. |
| **Root still in the built set** | **keep, and REPORT** | An orphan *aspect* of a live entity (§3.4). |

**The default is keep, and omission cannot flip it.** There is no field an author must remember to set,
no marker that can be forgotten, no "unknown ⇒ delete" branch. Fail-closed by *structure*, not by a lint
that notices later.

### 3.2 Why condition 2/3's set is safe when a package-manifest set would not be

Condition 3 tests membership in a set — and §6.3 rejects an alternative *for* using a set. The difference
is provenance of the set:

- `buildPrimordialEntries()` is **local, in-process, deterministic and total by construction**. It cannot
  come back short; on failure it returns an error and there is no plan (`reconcile.go:108-110`).
- A *remote* enumeration (listing package manifests) can come back short, and a short list turns live keys
  into apparent orphans. That is the janitor's stated reason for refusing one (`73050cc5`: *"a set is only
  as trustworthy as the enumeration that built it"*).

The **candidate** enumeration is remote — but its failure direction is safe: a short listing yields *fewer*
candidates, so it misses a retirement and never invents one. `KVListKeysPrefix` also refuses to return a
silently-partial result (`kv.go:245`).

### 3.3 Read path, write path, and where it hangs

**Read path.** `ListKeysFiltered(ctx, "vtx.meta.>")` on the KV handle `planReconcile` already holds → for
each key not in the built set, one `KVGet` → apply §3.1. Bootstrap reads Core KV directly; it is on
CLAUDE.md's P5 platform-binary exception list, and `kv.go:181-182` names the meta-vertex sub-set as an
explicitly bounded listing target. No lens is involved and none should be — the kernel is what lenses are
*defined by*.

**Write path.** A retirement is a **soft tombstone written directly to Core KV**, appended to
`ReconcilePrimordial`'s existing `substrate.AtomicBatch` (`reconcile.go:242`) as one more `BatchOp`:

```go
doc["isDeleted"] = true
doc["lastModifiedAt"] = substrate.FormatTimestamp(BootstrapTime)
doc["lastModifiedBy"] = BootstrapIdentityKey
doc["lastModifiedByOp"] = BootstrapOpKey
// createdAt / createdBy / createdByOp carried over untouched.
```

conditioned on the revision the plan read: `BatchOp{HasRevision: true, Revision: observed}`.

- **Body-preserving**, mirroring `step8_commit.go:414-418` — stays readable for audit, stays revivable
  (§3.5), and honours the shipped `tombstone-with-document` posture (`cbd0f244`).
- **`BootstrapTime`, never wall clock.** `ReconcilePrimordial`'s termination rests on the built envelope
  carrying a fixed timestamp (`reconcile.go:160-163`: *"Nothing here may introduce a time-varying field"*).
  A wall-clock stamp would still converge (a tombstoned key leaves the candidate set) but would break the
  component's determinism invariant and make two environments' kernels non-diffable.
- **Same batch, same atomicity** — a meta-vertex's root and aspects are retired together or not at all
  (`reconcile.go:165-169`).

**Not through the Processor.** A `TombstoneMetaVertex` op is the wrong instrument twice over: kernel metas
are `protected: true` and rejected at commit by design (which is *why* reconcile writes directly,
`reconcile.go:171-174`), and the Processor is not running at bootstrap time (Contract #7 §7.5: seed at
step 3, start Processor at step 5).

### 3.4 Granularity — whole entity only (reshaped by §12.1)

**A candidate is retired only if its meta-vertex root is also absent from the built set.** When the root is
still built, every unbuilt aspect under it is an **orphan aspect** — reported, never written.

The first draft did the opposite, treating partial shrink as the more common and equally desirable case. It
is more common, and it is the one case where retirement is useless or harmful:

- **Useless for the Processor.** `ddl_cache.go:191` is the *only* `isDeleted` check; `.canonicalName`,
  `.permittedCommands`, `.sensitive` and `.script` are read into structs with no `isDeleted` field
  (`:205,:233,:249,:268`). Because the tombstone preserves the body, a "retired" `.script` still yields
  `asp.Data.Source` — the Processor keeps executing it, forever, across every restart, while reconcile
  converges and `verify-kernel` reports clean. **A converged-but-wrong state with a success signal on it**
  is strictly worse than the orphan it replaces.
- **Harmful for Refractor.** `registry_probe.go:201` counts a lens as declared from its **root**, and its
  `.spec` fetch is deliberately fail-closed and never tests `isDeleted` (`:204-214`) — while
  `corekv_source.go:519-528` *does* tear the pipeline down on a `.spec` tombstone. Retiring `.spec` alone
  therefore removes the lens and leaves it counted as declared: a permanently latched
  `LensRegistryIncomplete` red card, the exact shape (`d040e00a`) spent a fire un-latching.
- **It fires on routine edits, not shrinks.** `addLensAspects` (`primordial.go:1094-1114`) branches the
  aspect set on `def.Adapter`: a kernel lens migrating `nats-kv → postgres` — the direction this repo is
  actively on (`lenses.go:309,355` already have `Adapter:"postgres"`) — keeps its root and ID but drops
  `.targetBucket` and `.outputSchema` from the built set. Per-aspect retirement would tombstone live
  aspects of a live lens on an ordinary definition edit, and (Inc 2) turn `make verify-kernel` red for it.

Reporting them is still worth doing: an orphan aspect is real drift an operator should see, and the report
costs nothing. **Writing** them requires aspect-level `isDeleted` support in `ddl_cache` and
`registry_probe` first — filed as §10 with that trigger.

Whole-entity retirement is safe because every consumer honours a **root** tombstone: `ddl_cache.go:191`,
`registry_probe.go:201`, `corekv_source.go:473-483`, `installer.go:685`. Root and aspects land in one
atomic batch; `CoreKVSource` fires `removeCB` on the root and again on `.spec`, and its `existed` guard
(`:480,:526`) makes the second a no-op in either delivery order.

### 3.5 The revive rule — the round-trip retirement forces

Retirement introduces a defect if shipped alone, and must ship with its fix.

`planReconcile` currently treats *any* tombstoned key as `retained` (`reconcile.go:132`). That rule protects
**revoked grants**: the primordial links sit outside the Processor's protected-key guard, and rbac-domain's
`RevokeRole`/`RevokePermission` tombstone exactly those shapes, so rewriting one would be *"an unlogged
privilege escalation"* (`reconcile.go:89-94`). Correct, and it stays.

But once retirement exists this becomes reachable:

> v1 builds K → v2 drops K → **retirement tombstones K** → v3 re-adds K at the same NanoID (the file pins
> it) → `planReconcile` sees K present, differing, tombstoned → **`retained`** → the kernel is silently and
> permanently incomplete.

A tombstoned key is **revived** (rewritten to the built body with `isDeleted: false`) only when all hold:

1. It **is** in the built set (a re-add, not a retirement), and
2. `isKernelDefinition(key)` — `vtx.meta.*` only, so no link/role/permission is ever revived, and
3. `createdByOp == BootstrapOpKey` **and** `lastModifiedByOp == BootstrapOpKey` — *bootstrap itself* wrote
   this tombstone.

Condition 3 preserves the revoked-grant protection intact: an operator's `TombstoneMetaVertex` stamps their
own tracker as `lastModifiedByOp` (`step8_commit.go:444`), so an operator-tombstoned kernel meta is **never**
revived — it stays `retained`, exactly as today. Condition 2 puts grants out of scope by key shape, twice.

Refractor handles the resurrection correctly: after a root tombstone purges `lensVertices`/`known`/
`pendingSpecs` (`corekv_source.go:473-486`), the revived root's non-deleted event re-registers the class and
the `.spec` buffering path (`:534-546`) covers the other CDC order (verified, §12.1).

### 3.6 The auth-plane refusal (added after §12.1)

**Retirement never retracts a lens's projected rows.** `pipelineDeleter.Delete` (`cmd/refractor/delete.go:40-63`)
removes the durable, cancels the pipeline and deletes the health card — it does **not** touch the target
bucket. So a retired lens stops projecting and its last-written rows stay live and served.

For most lenses that is inert. For the four kernel auth-plane lenses — `capability`, `capabilityRead`,
`capabilityReadGrants`, `capabilityReadWildcardGrants`, all `TargetBucket: "capability"`
(`lenses.go:102,215,308,354`) — it is **fail-open**: the grant set freezes, so every subsequent
`RevokeRole`/`RevokePermission` commits to Core KV and is never projected. Authorization keeps succeeding
off stale rows while revocations silently stop taking effect. That is the worst direction, and it is the
"a missed retraction on the security plane is an OVER-GRANT" reflex landing on my own design.

**So: retirement refuses, loudly, any candidate whose class is `meta.lens` and whose stored target bucket
is `capability`** — reported as `KERNEL AUTH-PLANE LENS UNBUILT` and never written. This is a small,
precise, fail-closed guard on the one plane where a mistake is a security defect; the general "frozen rows"
problem is filed in §10 with a lens-target-purge trigger. A deployment that genuinely wants to retire an
auth-plane lens is doing something that deserves a human, not a boot-time sweep.

---

## 4. Why `createdByOp` is a sound discriminator

The design licenses a write from one field, so the field deserves a matcher-level argument.

**(a) Can a non-bootstrap *code path* produce `createdByOp == BootstrapOpKey`?** No. The Processor is the
only other Core-KV writer (P2), and `buildMutationValue` sets `doc["createdByOp"] = trackerKey`
(`step8_commit.go:431`) from the operation's own tracker — never from the script. On update/tombstone,
`preserveImmutableFields` **deletes** the script's value before consulting the prior document (`:458-460`),
naming forgery as the threat it closes. `BootstrapOpKey` is referenced **nowhere outside
`internal/bootstrap/`** (grep verified twice, this fire and §12.3), so no in-tree caller can pass it.

**(b) One data-plane path defeats (a), and it needs a privileged actor.** `CheckDedup`
(`step2_dedup.go:60-63`) returns **`DedupNotFound`** for a tracker present with `isDeleted: true` — the
Contract #4 §4.5 operator-retry path. And the bootstrap tracker carries **no `protected` flag**
(`MakeBootstrapOpEnvelope`, `envelope.go:82-87`), while `docIsProtected` (`step8_commit.go:604-631`) rejects
only on `data.protected == true`. So an actor able to dispatch a DDL that tombstones `vtx.op.<BootstrapOpID>`
could then resubmit an op with that requestId and have every key it creates stamped `BootstrapOpKey`.

This falsifies "structurally unforgeable", and I am not going to leave the stronger claim standing. The
honest statement: **no code path forges it; one operator-level data-plane path can.** It is a hardening gap
that predates this design (the bootstrap tracker being unprotected is not new), and this design is what
makes it *matter*. Filed in §10 as a reserved-prefix guard on `vtx.op.*`; **not** a blocker, because the
actor it requires can already mutate the kernel directly.

**(c) Can a package inherit kernel provenance?** `preserveImmutableFields` *does* carry over a prior
`createdByOp` on update — so in principle a package updating a kernel meta would keep kernel provenance.
It cannot: every kernel `vtx.meta.*` root carries `protected: true` (`primordial.go:501,635,648,664,683,877,1028`)
and `rejectProtectedMutations` (`step8_commit.go:604-631`) blocks any op update/tombstone on that root *or*
its aspects (aspects resolve to the same root via `protectedRootKey`). The only residual is a kernel meta
whose root is absent or unparseable — a pre-existing corrupt state, not a normal path.

**(d) The `lattice.bootstrap.json`-regenerated case fails closed.** If the file is regenerated against a
bucket still holding the old kernel, `BootstrapOpKey` is a new NanoID, every old kernel key carries a
**foreign** `createdByOp` → **keep**. The pre-existing kernel is untouched. (It is a pre-existing broken
state — `docs/components/bootstrap.md`'s failure-mode table already names it as *"orphaning existing
references"*. Worth stressing: a **set-difference** discriminator would have deleted the entire old kernel
in this state. This one deletes nothing.)

**(e) The gate that keeps (a) true tomorrow.** (a) rests on "`BootstrapOpKey` has one writer" — exactly the
invariant the next agent breaks by reaching for the obvious thing. Per the lint doctrine the gate ships in
the same increment that makes the field load-bearing (Inc 2, §9): a `scripts/lint-conventions.go` rule
failing any reference to **`bootstrap.BootstrapOpKey`** from outside `internal/bootstrap/`.

**Scoped to `BootstrapOpKey` only.** The draft also covered `BootstrapIdentityKey`, which would have failed
CI on the commit introducing it — it is referenced today in `cmd/bootstrap/main.go:83,86`,
`cmd/loupe/main.go:102`, `internal/testutil/install_phase1_packages.go:78` and ~20 package tests (§12.1/§12.3).
`BootstrapOpKey` is the only symbol that licenses a delete, so the narrowing costs nothing and the gate
lands **blocking over a genuinely clean tree** — no warn-first debt.

---

## 5. Reconciliation with the existing mental model

**"Didn't we already handle this?"** Reconcile handles two of three verbs; its own design named the third
out of scope ([kernel-seed-reconcile-design.md](kernel-seed-reconcile-design.md) §8). This closes that
residual with *less* machinery than §8 anticipated — §8 assumed removal "needs an authoritative enumeration
… distinguishable from package-written `vtx.meta.*`" and concluded "that is a separate mechanism". The
enumeration it wanted already exists as a per-key field.

**"Doesn't the orphan-durable janitor already reap these?"** No, and the two compose. `73050cc5` reaps a
*durable* whose lens vertex positively says it is gone; `714cefd6` tears down a *pipeline* on a lens
tombstone. Both need a tombstone that, for a retired kernel lens, nothing writes. This supplies it.

**"Does this introduce new state?"** No. No new field, manifest, version register or marker.

**"Doesn't `protected: true` forbid this?"** `protected` is anti-brick immutability **against operations**
(Contract #7 §7.2 item 7, #6 §6.1); reconcile already rewrites protected kernel bodies today. Bootstrap is
the kernel's owner, not a caller it needs protection from.

**"Is a boot-time bucket listing a scan violation?"** No. The no-scans invariant governs the Processor's
serial commit lane. This is a boot-time read by the seeder, server-side prefix-filtered to `vtx.meta.>` over
a ~76-entry kernel, on a listing `substrate` itself names bounded (`kv.go:181-182`) and that `pkgmgr`
already performs unfiltered on every install (`installer.go:653`).

**Enforcement point, checked against the op-meta lesson.** The *authoring* decision — "this kernel entity
should go away" — is made in source, in `buildPrimordialEntries`, reviewed by git + CI. The *enumeration*
runs at apply time against whichever bucket is upgrading. That is the split `7381ace2` established: never
key the decision on the authoring environment's referent count. A fresh CI bucket has zero orphans; a
long-lived one may have several; both run the same code and reach the right answer.

---

## 6. Alternatives considered

### 6.1 A new per-key `kernelOwned: true` marker — **rejected**
The board row's assumed shape. **Not retroactive**, and the pre-existing population is the entire target: a
marker introduced now appears only on keys written after it, and a retired key is by definition never
written again. The keys it can reach are exactly the keys it is not needed for. (The container-default
reflex — *"everything, or everything from now on?"* — and here the answer is fatal.) It also duplicates
`createdByOp` and adds a field an author can forget.

### 6.2 A kernel key manifest stored in the bucket — **rejected**
Reconcile writes a manifest listing every key it built; the next pass diffs it. Authoritative in principle,
but it is a **set**, and a stale or truncated manifest mis-licenses deletion of live keys. Also not
retroactive, and it adds durable state to a kernel whose whole virtue is being derivable from the binary.
Strictly more machinery, strictly less safe.

### 6.3 Set difference against package `declaredKeys` — **rejected; this is the dangerous one**
Package ownership *is* recorded: `vtx.package.<id>.manifest` carries `declaredKeys` (`installer.go:809-843`).
So one could compute `orphans = live vtx.meta.* − built − ⋃ declaredKeys`. It is what the board row implies
and exactly what the janitor precedent refuses: the union comes from a **remote listing**, and a short
listing turns every package meta it omitted into an apparent orphan — with a **live package's DDL
tombstoned** as the consequence. The per-key `createdByOp` read makes the union unnecessary anyway.

### 6.4 Hard delete (NATS `DEL`) — **rejected**
Shelved by a standing Andrew decision (2026-07-02,
[hard-delete-mutation-verb-design.md](hard-delete-mutation-verb-design.md)). A tombstone is also better
here: it is what every consumer already reacts to, it preserves the audit record, and it is reversible
(§3.5). Keyspace is not reclaimed — the shelved row's problem, irrelevant at ~76 entries.

### 6.5 Refuse to retire an entity that still has referents — **rejected, filed §10**
Mirroring `7381ace2` (op-meta tombstone refuses an undeclared drop) does not transfer: there the author is a
*package* and the tool can preflight its declaration; here the author is a reviewed source change and a
boot-time referent scan is the whole-bucket reasoning §6.3 rejects.

### 6.6 Do nothing — **rejected, but it is the live alternative**
Taken seriously, because §1.2 shows demand is contingent and unmeasured. Rejected because the failure is not
passive (§1.1: an executable DDL, a running pipeline, a held canonicalName blocking the migration). But this
is *why* Inc 1 is non-destructive and Inc 2 is conditional (§9) — if the measured population is zero
everywhere and no shrink is pending, "do nothing" wins on the evidence and Inc 2 shelves.

### 6.7 Enforce the version bump instead (make every shrink force a wipe) — **rejected**
A gate tying `checkVersion` to the built kernel would make §1.2's third row impossible: every removal bumps,
every stale deployment refuses to boot, the operator wipes, no orphan ever survives. It is smaller than this
design and it is **the wrong direction**: it re-imposes `make down` as the remedy for a kernel change, which
is precisely what `ReconcilePrimordial` was built to eliminate (`reconcile.go:145-149` — *"a shared dev
stack and a demo box cannot casually be wiped, so 'wipe it' is not a remedy"*). Retirement makes a shrink
wipe-free the way reconcile made a change wipe-free; enforcing the bump gives up on that. **The two are
mutually exclusive and this is the fork worth naming** — if you prefer the cheap one, §9's Inc 1 still pays
for itself and Inc 2 becomes the bump gate instead.

---

## 7. Contract surface — no change, and the judgment call

**No `docs/contracts/*` edit is proposed; nothing is staged uncommitted.**

- **#7 §7.4 (idempotence)** preserved exactly, by the reconcile fire's own argument: a converged bucket
  produces zero writes. A retired key leaves the candidate set the moment it is tombstoned (§3.1 cond. 5).
- **#7 §7.2 (seeding inventory)** unchanged. It describes what bootstrap *seeds*; retirement removes what is
  **not** in it. Item 9's `createdByOp` guarantee is **relied on exactly as written**.
- **#1 §1.3 (immutable creation provenance)** likewise — already declared immutable, already enforced
  (`step8_commit.go:384`).

**The judgment call, stated so you can overrule it in one line.** This makes `createdByOp`
*security-relevant* — it now licenses a delete. A reasonable position is that a field carrying that weight
should say so in the contract. I chose **not** to edit, because the new obligation is a property of the
*codebase* ("`BootstrapOpKey` has one writer"), not the data model, and a codebase invariant belongs in a
lint gate — which §4(e) ships. If you'd rather it also be written into #7 §7.2 item 9, that is a
one-sentence addition and I will stage it; I did not want to manufacture contract churn to describe behavior
the contract already permits.

---

## 8. Test strategy

`natsfixture.Server(t)` and 20-char NanoIDs from `testutil` per CLAUDE.md — never a hand-rolled server or id.

**No production seam is needed.** A retired kernel entity is simulated by writing an extra
`vtx.meta.<NanoID>` root + aspects directly into the test bucket with `createdByOp = BootstrapOpKey` —
byte for byte what an older binary left behind.

| Test | Asserts |
|---|---|
| **Discriminator table** (Inc 1) | Every §3.1 row: foreign `createdByOp` → keep; absent field → keep; unparseable → keep; `KVGet` error → keep; already-tombstoned → keep; in-built-set → keep; **root still built → keep + reported as orphan aspect**; root unbuilt + kernel-provenanced → **retire**. |
| **The load-bearing negative** (Inc 1) | A realistic *package*-written `vtx.meta.*` (root + `.canonicalName` + `.script`, non-bootstrap tracker) is never planned for retirement, on a bucket where it is the only unbuilt meta. |
| **Aspect-shrink is never written** (Inc 1) | A live kernel lens with an unbuilt `.targetBucket` (the §3.4 adapter-migration shape) is reported, and `plan.steps()` is empty. Regression-guards the reshape. |
| **Auth-plane refusal** (Inc 2) | A `meta.lens` with `targetBucket: "capability"`, kernel-provenanced and unbuilt, is refused and reported — never tombstoned. |
| **Fixpoint** (Inc 2) | Reconcile with an orphan writes once; the next reconcile writes **zero** (`Changed() == false`). Guards Contract #7 §7.4. |
| **Determinism** (Inc 2) | The tombstone envelope is byte-identical across two runs (catches a wall-clock regression). |
| **Revive round-trip** (Inc 2) | Retire K, re-introduce K into the built set → rewritten with the built body, `isDeleted: false`. |
| **Operator tombstone never revived** (Inc 2) | A built `vtx.meta.*` tombstoned with `lastModifiedByOp = vtx.op.<other>` stays `retained`, no write. The revoked-grant protection intact. |
| **Atomicity** (Inc 2) | Root + all aspects in one batch; a conflicting concurrent write leaves the bucket wholly unchanged (mirrors `reconcile.go:247-251`). |
| **e2e, ephemeral stack** (Inc 2) | Plant an orphan `meta.lens` (non-capability target), confirm Refractor loads it, reconcile, assert pipeline + durable torn down — i.e. that the shipped reapers fire on the tombstone this design supplies. |
| **Lint gate** (Inc 2) | `lint-conventions` fails a fixture referencing `bootstrap.BootstrapOpKey` outside `internal/bootstrap/`; the real tree passes. |

**Gates:** `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`, every
`scripts/lint-*.go` gate, and full `go test ./...` for Inc 2 — it changes what a boot writes to Core KV,
which is a wide blast radius.

---

## 9. Decomposition for the Steward

Two increments. **Inc 2 is conditional on Inc 1's measurement.**

### Increment 1 — detect and report (non-destructive)

`KernelOrphans(ctx, kv) (entities, aspects []string, err error)` in `reconcile.go`, the read-only twin of
`KernelDrift`, sharing `planReconcile` so report and repair can never disagree — the single-comparison-path
discipline `reconcile.go:79-81` already establishes. `planReconcile` gains `orphanedEntities` /
`orphanedAspects`, populated but **not** appended to `steps()`. `ReconcilePrimordial` logs each at `Warn`.
`VerifyKernel` (`verify.go:259`) and `scripts/verify-kernel.go:325` report both classes as
**informational, non-failing** lines.

*Green criterion:* every existing `internal/bootstrap` test passes unchanged; `make verify-kernel` exit
status unchanged on every bucket. Nothing is written that was not written before.

**This increment is the measurement.** Its build note must record both counts on the shared dev stack and
the demo box.

> **Gate before Inc 2.** If Inc 1 reports **zero orphan entities** on both long-lived buckets, do **not**
> build Inc 2 — file it `🗄️ shelved` with the trigger *"a non-zero orphan-entity report, or the first
> kernel shrink that ships without a `lattice.bootstrap.json` version bump"*. Retirement machinery for an
> empty population is dead scaffolding, and Inc 1 already converts a silent condition into a visible one —
> which per §1.2 is most of the value, since the failure mode is *nobody knowing*. Note that a non-zero
> **aspect** count does **not** unblock Inc 2 (§3.4 refuses to write those); it unblocks the §10 row.

### Increment 2 — retire, revive, and bind it

Tombstone writes appended to the existing atomic batch (§3.3); the §3.6 auth-plane refusal; the revive rule
(§3.5); `ReconcileResult.Retired` / `.Revived` counters; the `verify-kernel` **orphan-entity** line
escalated from informational to failing (satisfiable now, because `make reseed-kernel` clears them on the
same pass — no warn-first debt); and the `lint-conventions` single-writer gate (§4(e)), blocking over a
clean tree. The orphan-**aspect** line stays informational (§3.4).

*Green criterion:* the full §8 table green, `go test ./...` green, and `make reseed-kernel` on a bucket
carrying a planted orphan entity leaves `make verify-kernel` passing.

---

## 10. Residuals — named, with their triggers

- **Aspect-level retirement is refused** (§3.4) because no Processor-side consumer honours an aspect
  `isDeleted` (`ddl_cache.go:205,233,249,268`) and `registry_probe.go:204-214` would latch a red card.
  *Trigger:* add aspect-level `isDeleted` checks to `ddl_cache.loadMetaVertex` and make
  `registryProbeSpecProbe` treat a tombstoned `.spec` as undeclared — **then** aspect retirement becomes
  meaningful. Worth filing as its own row regardless: today a hand-tombstoned kernel `.script` is silently
  ignored by the Processor, which is a latent trap independent of this design.
- **A retired lens's projected rows are never retracted** (`cmd/refractor/delete.go:40-63`). §3.6 refuses
  the auth-plane case; every other lens leaves frozen rows in its target. *Trigger:* a lens-target purge
  primitive, or a consumer harmed by stale rows in a retired lens's bucket.
- **The bootstrap op tracker is unprotected** (§4(b)) — `MakeBootstrapOpEnvelope` writes no
  `data.protected`, and `CheckDedup` treats a tombstoned tracker as not-found. *Trigger:* file a hardening
  row for a reserved-prefix guard on `vtx.op.*` mutations, or simply mark the bootstrap tracker
  `protected: true`. Pre-dates this design; this design raises its stakes.
- **Retired kernel *topology* (roles, permissions, links) is out of scope.** Reconcile declines to rewrite
  non-`vtx.meta.*` entries because operations own their lifecycle (`reconcile.go:94-99`), and retirement
  keeps that line: a tombstone there is a revoked grant, not a stale definition. Blast radius is bounded —
  the DDL a retired permission authorizes is retired by this design, so the operation becomes
  undispatchable. *Trigger:* a kernel shrink dropping a role/permission **without** dropping its DDL.
- **Loupe's op catalog has no `isDeleted` filter** (`cmd/loupe/ops.go:171-190`, `:39-70`, `:106-118`).
  Because the tombstone preserves the body, a fully-retired kernel DDL keeps appearing in the Submit-Op
  catalog with its schema; submitting yields `NoDDLForClass` (`step4_hydrate.go:91`). Confusing, not
  unsafe. *Trigger:* file as a Loupe-lane row.
- **`DDLCache` duplicate-`canonicalName` resolution is a coin flip** (`ddl_cache.go:134,144-150` — map
  range, "keeping first-seen"). Narrow, and unreachable at whole-entity granularity. *Trigger:* only if
  §10's aspect-level row ships.
- **No referent check before retirement** (§6.5). *Trigger:* an observed breakage where a live package
  depended on a retired kernel definition — at which point the fix is an authoring-time gate in the
  kernel's own build, not a boot-time scan.
- **Keyspace is not reclaimed** — a tombstone leaves the key present and enumerated. Owned by the shelved
  [hard-delete verb](hard-delete-mutation-verb-design.md) row; irrelevant at ~76 entries.
- **`make up`'s reuse short-circuit still bypasses all of this** (`reconcile.go:176-182`), so orphans
  surface via `make reseed-kernel` / `make verify-kernel`, not the inner loop. Pre-existing sibling row from
  [kernel-seed-reconcile-design.md](kernel-seed-reconcile-design.md) §8, unchanged.

---

## 11. Verified clean by the adversarial pass — do not re-litigate

- **`buildPrimordialEntries()` is the total kernel Core-KV write set** — three write sites, all fed from it;
  no env/flag gating of kernel *content*; no bootstrap-lens seeding in `cmd/refractor`; `testutil.EnsurePrimordials`
  writes nothing (`internal/testutil/primordials.go:21-34`).
- **Torn/duplicate removal** (root + `.spec` in one batch) — `corekv_source.go:480,526` both guard on
  `existed`; no-op in either delivery order.
- **Revive through Refractor** works in both CDC orders (`corekv_source.go:473-486,534-546`).
- **pkgmgr** skips tombstones for both canonicalName (`installer.go:685`) and weaver-target `.spec` (`:749`).
- **Weaver / Loom hold no dangling refs** — `registry.go:468,509,977-1031,1148`, `loom/source.go:173,203`
  purge and re-register; and moot today, since bootstrap builds no `meta.weaverTarget` / `meta.loomPattern`
  / op-meta vertices at all.
- **Chronicler** tears down on both root and `.spec` tombstone (`chronicler/source.go:156,197`).
- **`DurableJanitor`** reads the tombstone positively (`durable_janitor.go:163-175`).
- **Crash recovery reuses IDs, never regenerates** (`nanoid.go:345-351`); partial-field regeneration is
  impossible (`populate` validates all 28 and errors on any empty).
- **Listing failure direction is safe** — `KVListKeysPrefix` discards partials (`kv.go:242-247`); a short
  list yields fewer candidates, never more.

---

## 12. Adversarial pass (run this fire — three read-only reviewers)

Per the Designer skill, a design that licenses deletion of kernel state does not ship unreviewed, and the
pass is this lane's obligation, not a gate handed to the Steward. Three reviewers: discriminator soundness
(can it delete a live key?), consumer obligations (what breaks when a meta is tombstoned?), and citation
verification. **The pass reshaped the design; it was not ceremony.**

### 12.1 Blockers found, and how they were folded

| # | Finding | Fold |
|---|---|---|
| 1 | **Aspect-level retirement is a silent no-op for the Processor.** `ddl_cache.go:191` is the only `isDeleted` check; `.script`/`.permittedCommands`/`.sensitive`/`.canonicalName` are read into structs with no such field, and the tombstone preserves the body — so a "retired" script keeps executing while reconcile reports converged. | **§3.4 rewritten: whole-entity only.** Aspect orphans reported, never written. §10 files the consumer fix as the unblocking trigger. |
| 2 | **`.spec`-only retirement latches a permanent red health card** — `registry_probe.go:201` counts the lens declared from the root while `corekv_source.go:519-528` removes it. The design would *create* the `d040e00a` shape it cites as a reaper it composes with. | Same fold as #1. |
| 3 | **The adapter branch makes #1/#2 fire on routine edits** — `addLensAspects` (`primordial.go:1094-1114`) drops `.targetBucket`/`.outputSchema` from the built set on a `nats-kv → postgres` migration of a **live** lens. | Same fold as #1; called out explicitly in §3.4 as the likeliest trigger. |
| 4 | **§1.2's historical population was wrong** — `checkVersion` (`nanoid.go:461-471`) accepts only `"16"` and propagates the error, so a pre-4.7 deployment cannot boot the current binary; `make down` clears bucket and file together. The RoleMgmt orphans were force-wiped long ago. | **§1.2 rewritten** with the real reachability table, and the demand case re-grounded on the reconcile-vs-version-bump tension (§6.7) instead of a fabricated population. |
| 5 | **A retired lens's projected rows survive; on the auth plane that is fail-open** — `cmd/refractor/delete.go:40-63` never touches the target, so retiring `capability*` freezes the grant set and revocations silently stop applying. | **§3.6 added** — fail-closed refusal for any `meta.lens` targeting the `capability` bucket. General case filed §10. |
| 6 | **`createdByOp` is forgeable by a privileged actor** — `CheckDedup` (`step2_dedup.go:60-63`) returns NotFound for a tombstoned tracker, and the bootstrap tracker carries no `protected` flag (`envelope.go:82-87`). | **§4(b) rewritten**, the "structurally unforgeable" claim withdrawn; residual filed §10. Not a blocker — the actor required can already mutate the kernel. |
| 7 | **The lint gate could not land blocking as scoped** — `BootstrapIdentityKey` is referenced in `cmd/bootstrap`, `cmd/loupe`, `internal/testutil` and ~20 package tests. | **§4(e) narrowed to `BootstrapOpKey`** — the only symbol that licenses a delete, so the narrowing costs nothing and the tree is genuinely clean. |
| 8 | **Two commit citations in §1.1 were wrong** — `6c0a08c7` is a docs-only commit whose own message says the sweep was *not* performed (gated behind an approval), and `d040e00a` is a health-status race fix on 31 **live** lenses, not a reaping of gone ones. | §1.1 rewritten to cite only `73050cc5` and `714cefd6`, which are accurate. |
| 9 | **§3.3 mis-cited Contract #7 §7.1 for a read-path sanction** (§7.1 is about the write-path Capability-Lens exemption; "reconcile" appears nowhere in Contract #7). | Re-grounded on CLAUDE.md's P5 platform-binary exception list plus `kv.go:181-182`'s bounded-listing note. |
| 10 | **Implementation note:** `planReconcile` holds only a `jetstream.KeyValue`, not a `Conn`. | Correct that `substrate.KVListKeysPrefix` needs a `Conn`; the handle already exposes `ListKeysFiltered` directly, so no threading is needed. Noted in §2 and §3.3. |

### 12.2 What the pass could not refute

No reviewer found a path by which a **live** key is tombstoned once §3.4's whole-entity narrowing is
applied. The negative case (`createdByOp != BootstrapOpKey` ⇒ keep) survived a dedicated attempt to break
it across package installs, operator ops, Loom/Weaver/Refractor writers, crash recovery, and partial-file
regeneration. §11 lists what was checked and found clean.

### 12.3 Citation audit

A third reviewer independently verified ~60 `file:line` citations and all contract references. Every
citation inside `reconcile.go`, `envelope.go`, `primordial.go`, `step8_commit.go`, `substrate/{kv,batch,envelope}.go`,
the lens/DDL-cache/installer references and all contract sections checked out, several to the exact quoted
sentence; a handful were off by 1–3 lines with the substance unaffected and have been corrected. The errors
it found are folded as #7, #8 and #9 above. The `~75` figure is independently grounded at
`primordial.go:302` (*"Total ≈ 76 Core KV entries"*).
