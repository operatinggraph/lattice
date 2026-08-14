# Grant provenance — closing the runtime permission-minting channel

**Status: ✅ RATIFIED — Branch A (grantable, with provenance)** · Andrew, 2026-08-13 ·
**low priority** — build after higher-priority ratified work · Designer: Winston (architect) · 2026-08-11

**Ratification record.** §5.4 resolved: **Branch A** — an op authored outside the package plane stays
grantable via `CreatePermission`+`GrantPermission`, stamped `data.origin: "runtime"`; the core reserved
set refuses runtime-origin grants of reserved verbs while a package manifest may still grant them.
The Contract #6 §6.1 provenance clause **committed at ratification** with a transitional note (the note
dies with Inc 3). Fire order unchanged and binding: un-tombstone prerequisite (§9 row 1) → Inc 1+2 →
Inc 3. DD corrections folded 2026-08-13 pre-ratification: the prerequisite re-pinned to the
surviving-key arm (`upgrade.go:324-331` via `build.go:1006`, `step8_commit.go:435-439`; the re-add arm
revives by design), and census C4 corrected seven→nine specs.

Backlog row: `planning-artifacts/backlog/lattice.md` → *Security & trust boundary* →
"[rbac] A second grant channel mints capability grants outside the package plane".
Named demand: `packages/privacy-base/permissions.go:59-67` — the maintainers' own filed obligation.

---

## For Andrew

**What it does.** `operator` holds `CreatePermission` / `UpdatePermission` / `GrantPermission` at
`scope:any` — a second grant channel, parallel to package install, that mints capability grants with
**no manifest entry, no `vtx.package` record, no `UninstallPackage` retraction, and no verifier that can
see them**. This design makes runtime-minted grants **distinguishable from package-installed ones**, and
uses that distinction to do what the row asked for and could not otherwise express.

**The recommendation, in three parts:**

1. **Withdraw `UpdatePermission`.** It has **no invoking caller anywhere** (§8 C1), and it is the
   *retarget* primitive: it rewrites a permission vertex's `data` wholesale, so it can silently point a
   **package-installed** permission at a different operationType and strip its `note`/`lanes` (§1.2).
   Removing it makes a permission vertex's body **write-once**, which is the precondition for everything
   below.
2. **Keep `CreatePermission` / `GrantPermission`, and give them provenance.** A runtime-minted vertex is
   marked as such; the capability lens projects the mark. These two are **not** dead surface and must not
   be withdrawn — see the fork below.
3. **Then the row's ask becomes expressible**: a core-owned reserved set that refuses a
   **runtime-provenanced** grant of a reserved verb while leaving a **package** free to grant it. That
   distinction is the whole point, and it is exactly what privacy-base wants preserved.

**I recommended plain withdrawal first and the adversarial pass falsified it — the correction is the
design.** Two blocking findings: (a) `CreatePermission`+`GrantPermission` are the load-bearing mechanism
of `internal/hellolattice` (Phase 1 Gate 5, a **required CI job**, plus the shipped tutorial at
`docs/hello-lattice.md:235`/`:250`); and (b) structurally, `CreateMetaVertex` lets root author an
operationType **outside** the package plane, and no package can declare a `PermissionSpec` for such an
op — so this channel is the *only* route to grant an ad-hoc op to anyone. Withdrawing all three would
have broken a required gate and removed a capability with no replacement. My own census caused this: it
filtered `_test.go`, and that exclusion was doing the load-bearing work (§8 C1, corrected).

**The one architectural fork — §5.4, RESOLVED: Branch A (Andrew, 2026-08-13).** Should an operationType
authored outside the package plane (via `CreateMetaVertex`) be grantable at all? **Yes, with
provenance** — it preserves the tutorial and the exploratory path, and provenance turns the ungoverned
channel into a governed one. The alternative (ad-hoc ops become permanently ungrantable, tutorial
rewritten onto packages) was cleaner in principle and cheaper to build, but it deletes a working
capability to close an *audit* gap that provenance closes without loss.

**One frozen-contract edit — committed at ratification** (Contract #6 §6.1, with a transitional note
until Inc 3 lands the enforcement) — see §9.

**No architectural fork.** The one fork I expected — "is `operator`-can-make-`operator` an escalation?" —
was already settled: **`root-identity-designation-design.md` Fork A (ratified 2026-07-02)** established
root = `holdsRole → operator` topology and that *"you must already be root to grant root."* I am not
reopening it, and this design does **not** touch `AssignRole`, `CreateRole`, or the role plane (§5.2).

**Frozen-contract edit — committed at ratification** — `docs/contracts/06-capability-kv.md` §6.1: a short
normative clause requiring every permission vertex to carry its **origin**, and reserving to core the
policy of which operationTypes a *runtime-provenanced* grant may never confer. §6.1 previously specified
who *projects* grants but was silent on who may *author* them, which is the gap this whole item lives in.
The clause carries a transitional note (build lands with Inc 3; the note dies with it).

**Also worth your attention (§5.1, found by the adversarial pass):** a deliberate `RevokePermission`
against a **package-installed** grant is **not durable** — the next routine `lattice-pkg` upgrade or
`--force` re-apply of that package silently un-tombstones it (`upgrade.go:292-332` emits an `update`, and
step 8's update arm has no aliveness guard). That is a pre-existing defect this design did not introduce,
but Inc 1 ships a mandatory version bump, so it would *trigger* it. Filed and sequenced (§9).

**Three corrections to the board row, folded in below** (§2): the row says "two ops" — it is **three**
(`UpdatePermission` retargets an *existing, package-installed* permission vertex); the row's destruction
claim names the wrong verb
(`ShredIdentityKey` **is** granted, by `privacy-operator-grant`, your 2026-07-04 decision — only
`ShredRetentionClassKey` ships no grant); and the row's implied severity is lower than it reads, because
root can already reach any verb via `InstallPackage` (§3.1). What the channel actually costs is
**attributability and revertibility**, not reachability. I have kept the item because that is exactly
what privacy-base's posture is built on.

**Size: M** under Branch A (Inc 1 S, Inc 2 XS, Inc 3 M — plus the sequenced prerequisite, S–M).
**S–M** under Branch B, plus the hello-lattice rework. The row said M; that was right, though not for the
reason the row gave.

---

## 1. The mechanism, end to end

The grant chain, every hop cited. Nothing here is inferred.

| # | Hop | Code |
|---|---|---|
| 1 | `CreatePermission` reads `operationType` as a free string, mints `vtx.permission.<random NanoID>` with `data.{operationType,scope}` | `packages/rbac-domain/ddls.go:306-322` |
| 2 | `GrantPermission` links `lnk.permission.<id>.grantedBy.role.<id>` — no check that the caller holds the verb, no check on which role | `packages/rbac-domain/ddls.go:392-411` |
| 3 | All three ops are granted to `operator` at `scope:"any"` | `packages/rbac-domain/permissions.go:9-30` (`:22`, `:23`, `:27`) |
| 4 | `capabilityRoles` walks `identity -[:holdsRole]-> role <-[:grantedBy]- perm` and re-emits `perm.data.operationType` **verbatim** | `packages/rbac-domain/lenses.go:80-91` |
| 5 | Written to `cap.roles.<actorSuffix>` | `packages/rbac-domain/lenses.go:39-58` |
| 6 | Step 3 matches by **exact string equality**, `scope:"any"` ⇒ allow | `internal/processor/step3_auth_capability.go:512`, `:516-523` |

Nothing between hops 1 and 6 consults a registry of legal operationTypes. `ClassForCommand` /
`buildByCommand` (`internal/processor/ddl_cache.go`) and step 6's `permittedCommands`
(`step6_validate.go:221-237`) both key on **`env.OperationType` — the outer submitted op** (is
`CreatePermission` allowed to write the `permission` class), never on the payload string being minted.

### 1.1 The sanctioned channel, for contrast

A package-installed permission is a different object in three respects that all matter:

| Property | Package install | Runtime mint |
|---|---|---|
| Vertex id | **Deterministic** — `entityNanoID(pkgName, permTag(op, scope))`, `installer.go:418-420`, `:479-481` | Random `nanoid.new()`, `ddls.go:313` |
| Manifest record | Declared + count/name-checked at install | None — no manifest can name it |
| Revocation | `UninstallPackage` tombstones **each declared key** | Nothing retracts it; no declaredKeys entry exists |
| Attribution | `vtx.package` record + version | The op log only |

Citations: `internal/pkgmgr/build.go:357-378` (vertex + `grantedBy` link),
`internal/bootstrap/install_ddl.go:123-127` (uninstall tombstones `declaredKeys`).

**The verifier cannot close this.** `Manifest.VerifyAgainstDefinition`
(`internal/pkgmgr/manifest.go:130-152`) compares the **manifest YAML against the Go `Definition`** — a
source-to-source check that never reads Core KV. There is no live-vs-declared reconciliation for
permission vertices anywhere, so a runtime-minted grant is invisible to every gate. (The one partial
exception, `scripts/verify-package-rbac.go:456-490`, asserts *declared → live* for rbac-domain's own ten
ops; it cannot see extras.)

### 1.2 The third op the row misses

`UpdatePermission` (`ddls.go:325-341`) rewrites the vertex's `data` **wholesale** to
`{operationType, scope}`. Consequences, none of which the row names:

- It **retargets an existing, package-installed permission vertex** to any other operationType. The
  deterministic key still says `permTag("CreateRole","any")`; the body now says something else. Live
  grants desynchronize from every manifest, and §1.1's verifier is structurally blind to it.
- It **drops `note`** and **drops `lanes`** (both are conditional fields the installer writes —
  `build.go:361-367`). A package's allowlisted privileged-lane grant silently downgrades to
  default-lane on any `UpdatePermission` touching it.
- It is the op that **defeats the obvious fix**: any design that marks provenance in the vertex body
  (`data.declaredBy`) is undone by the one op that rewrites the body. I priced that alternative in §5.3
  before finding this, and it is why the alternative loses.

---

## 2. Correcting the row (the census that falsified it)

Run concurrently with drafting, briefed to falsify rather than confirm.

| Row claim | Verdict | Correction |
|---|---|---|
| `operationType` is a free string with no allow-list | **CONFIRMED** | — |
| Both ops are `scope:any` to `operator` | **CONFIRMED** | Seeded `operator` holders: the primordial admin + 5 kernel service actors (`internal/bootstrap/primordial.go:792-854`). Gateway is deliberately excluded (`:483-497`). |
| "the shred verbs ship no grant" | **PARTLY WRONG** | `ShredIdentityKey` **is** granted — `packages/privacy-operator-grant/permissions.go:15-26` (→`operator`, your 2026-07-04 decision) and `packages/console-operator/permissions.go:27-32` (→`consoleOperator`). Only **`ShredRetentionClassKey`** ships no grant, deliberately (`privacy-base/permissions.go:51-57`). |
| "two ops reach an irreversible destruction" | **WRONG COUNT** | **Three** ops form the channel (§1.2). And the reachable verb is `ShredRetentionClassKey`, not "the shred verbs". |
| No never-self-grantable concept exists | **CONFIRMED** | The only repo occurrence of the phrase is privacy-base's own comment declaring it *"filed, not assumed here"*. |

**Corpus size** (this decides why a deny-list loses): **187 distinct permissioned operationTypes** across
**28 packages** and **9 distinct grant-target roles** (`backOfHouse, consoleOperator, consumer,
control-operator, demoOperator, frontOfHouse, identityProvisioner, operator, provider`). Note three
separately-named operator-ish roles exist — `operator` (kernel, root-equivalent) is **not**
`consoleOperator` or `control-operator`; the row's bare "operator" means the kernel one.

The board row is corrected as part of this fire (§9).

---

## 3. What grounding CLEARED — read this before weighing severity

Three things that look like part of the defect and are not. Each was checked because assuming it would
have inflated the design.

### 3.1 Root can already reach any verb — so this is not a reachability escalation

Every `operator`-holder that step 3 routes to the anchor reads `cap.<actor>`, whose cypher is a
**hard-coded literal** granting `CreateMetaVertex / UpdateMetaVertex / TombstoneMetaVertex /
InstallPackage / UninstallPackage / UpgradePackage` at `scope:'any'`, with lanes
`[default, meta, urgent, system]` (`internal/bootstrap/lenses.go:135-148`, `:124`). `InstallPackage` is
a total grant-authoring capability — the install DDL is a *"thin script over a fat manifest"* that
**trusts the client-supplied document bodies** (`internal/bootstrap/install_ddl.go:34`). So root can mint
any grant by installing a one-permission package.

**Therefore the honest harm is not "root gains a verb it could not otherwise reach." It is that one of
the two routes is unattributable and unrevertible.** That is precisely the property privacy-base's
posture rests on, quoted verbatim: *"What the missing grant actually buys is that doing so is an
explicit, separately committed act with its own audit trail, rather than authority that arrives silently
with the role."* Withdrawing the runtime channel makes that sentence true. I state the reduced severity
plainly rather than let the row's framing carry the ratification.

### 3.2 `operator`-can-make-`operator` is ratified, not a hole

`bootstrap.SystemActorKeys` (`internal/bootstrap/system_actors.go:35-66`) selects **any** live
`lnk.identity.<id>.holdsRole.role.<RoleOperatorID>` link — no kernel-seeded filter, despite four
code comments and Contract #6 §6.1 prose describing the set as *"kernel-seeded"* / *"primordial (fixed at
bootstrap)"*. I was drafting a fix for this when I re-read the rejection it belongs to:
**`root-identity-designation-design.md`, Fork A, ratified by Andrew 2026-07-02**, deliberately moved
these exact three sites off `data.protected` **onto** the `holdsRole → operator` topology, on the
explicit reasoning that the topology *"is self-protecting: you must already be root to grant root — it
cannot be bootstrapped from nothing."*

So role-cloning is root exercising root, lateral not vertical. **This design does not touch it.** The
stale "kernel-seeded" prose is a docs-truth residual, not a defect (§9 files it).

### 3.3 A real staleness defect lives here, but it is a different item

Within the ratified model there *is* a genuine flaw, and it is not security: `ClassAwarePlatformKey`
(`internal/capabilitykv/keys.go:58-79`) closes over a **boot snapshot** of the actor set
(`cmd/processor/main.go:138-150`, whose comment asserts *"SystemActorKeys are primordial (fixed at
bootstrap), so discovering them once here is stable for the process lifetime"* — an assumption about the
*population* that the *predicate* does not enforce). Four binaries snapshot independently
(`cmd/{processor,refractor,weaver,loom}/main.go`) and can disagree about who is root; a newly-granted
operator's anchor doc is written live by the lens but unread until a restart.

The **revocation** direction is fail-closed and immediate (the `holdsRole` tombstone triggers the
anchor's `emptyBehavior: delete`; `ReadAndMerge` skips the absent key and falls back to `cap.roles`), so
this is a grant-latency and cross-binary-consistency defect, not an over-grant. **Filed as its own row
(§9), not folded in** — it is a different mechanism with a different fix, and bundling it would hide a
docs-truth correction inside a security fire.

---

## 4. The defect, stated exactly

> The capability plane has **two** grant-authoring channels. One (package install) is declarative,
> manifest-recorded, attributable to a package + version, and revertible by `UninstallPackage`. The other
> (`CreatePermission` / `UpdatePermission` / `GrantPermission`, `scope:any` to `operator`) produces an
> **indistinguishable** result at the consumption point with **none** of those properties.
>
> Indistinguishability is the defect. Not the channel's existence — the platform has a legitimate need for
> it (below) — but the fact that step 3, the auditor, and the operator cannot tell the two apart.

Two consequences worth separating:

- **The audit consequence** (the filed row): a grant deliberately withheld as a deployment decision —
  `ShredRetentionClassKey`, the widest-blast-radius verb in the platform — can be conferred without the
  explicit, separately-committed act that withholding it was supposed to force.
- **The integrity consequence** (§1.2, unfiled until now): `UpdatePermission` can retarget a
  package-installed permission vertex, desynchronizing live grants from the manifest that declared them,
  with no gate able to observe it.

**Who actually calls these — and how my first census got it wrong.** There is no *production* caller: no
seed script, no verify script, no Loupe handler or web asset, no vertical app, no `cmd/**` or `internal/**`
caller. I concluded "dead surface, withdraw all three" from that.

But census C1 filtered `_test.go`, and **that exclusion was doing load-bearing work I never justified** —
the claim was a negative, and the glob was its premise. Re-run without it: **8 files**, and one matters a
great deal — `internal/hellolattice/hellolattice_test.go` (14
references) is the **Phase 1 Gate 5 reference implementation**, a CI gate
(`.github/workflows/ci.yml:243`, `make test-hello-lattice`), and `docs/hello-lattice.md:235`/`:250`
narrates the same two ops as the tutorial's *taught* way to grant a capability.

**And the tutorial is not using the channel gratuitously — it is using the only route there is.** The
hello-lattice comment says so directly (`hellolattice_test.go:333-337`): *`CreateBook` is an ad hoc
tutorial DDL, so unlike a package-installed DDL it "ships no permissions.go, so nothing grants it to any
role."* That generalizes into the fact my first draft missed: **`CreateMetaVertex` — a literal, root-only
kernel grant (§3.1) — authors an operationType outside the package plane, and no `PermissionSpec` can
exist for it, because no package declares it.** `CreatePermission`+`GrantPermission` is structurally the
*only* way to grant such an op to anyone. Withdrawing them would have left `CreateMetaVertex` able to
author ops that **nothing could ever invoke** — root included, since the anchor's literal six do not
include them.

So the trio splits cleanly: **`UpdatePermission` is dead and dangerous** (no caller, mutates
package-installed state); **`CreatePermission`/`GrantPermission` are live and load-bearing**. §5 treats
them differently for that reason. The NFR-P3 latency probe (`:649-676`) leans on the same property
deliberately — it mints a fresh uncached operationType per iteration *because* the channel needs no
package — so it survives unchanged under §5's shape and would have had to be redesigned under withdrawal.

---

## 5. The shape: make provenance real, then reserve on it

### 5.1 The change, in three moves

**Move 1 — withdraw `UpdatePermission` (and only it).** Remove `mk("UpdatePermission")` from
`packages/rbac-domain/permissions.go`; nine grants remain. It has no invoking caller (§8 C1) and it is
the sole op that can **mutate an existing** permission vertex's body — the integrity hole of §1.2 and the
one thing that would make any provenance marker forgeable. With it gone, a permission vertex's `data` is
**write-once**: authored either by `CreatePermission` or by the installer, never rewritten. Everything
below depends on that.

**Move 2 — mark the origin.** `CreatePermission` stamps `data.origin: "runtime"` on the vertex it mints;
`internal/pkgmgr/build.go:357-368` stamps `data.origin: "package"` plus `data.declaredBy: <packageName>`.
`capabilityRolesSpec` (`packages/rbac-domain/lenses.go:80-91`) projects `perm.data.origin` into each
`platformPermissions` entry alongside `operationType`/`scope`/`lanes`.

Why this is sound *here* and was not in my earlier draft (§5.3): the marker's integrity rests on the body
being write-once, which Move 1 establishes. Absence of the field is treated as **`runtime`** — the
fail-closed direction, so a pre-existing vertex minted before this lands is governed, not exempted, and a
forgotten stamp denies rather than grants.

**Move 3 — reserve on provenance, not on the verb alone.** A core-owned set of operationTypes that a
**runtime-provenanced** entry may never confer, enforced in `matchPlatformPermission`
(`internal/processor/step3_auth_capability.go:503-586`) at the point the entry is consumed, mirroring
`privilegedLaneAllowlist`'s shape (`:419-430`, `:468-489`): refuse the entry, continue the scan, and raise
a Health alert (`AlertCodeReservedOperationGrantRejected`, per Contract #5 §5.5) so an operator sees that
a reserved grant was attempted rather than having it silently work or silently vanish. v1 of the set is
the destruction verbs privacy-base withholds — `ShredRetentionClassKey` first.

**Enforcing at consumption, not at mint, is deliberate** and follows the ratified precedent: it does not
care *how* the entry got into `cap.roles`, so a future second producer inherits the refusal for free. A
mint-time guard in the Starlark would cover one op in one package.

**What each party can do afterwards** — the distinction the row's plain deny-list could not express:

| Actor | `ShredRetentionClassKey` | Ordinary verb |
|---|---|---|
| A **package** declaring a `PermissionSpec` | **Allowed** — the sanctioned, manifest-recorded, uninstallable deployment decision privacy-base is protecting | Allowed |
| A **runtime mint** (`CreatePermission`+`GrantPermission`) | **Refused** at step 3 + Health alert | Allowed, and now *attributable* — the entry carries `origin: runtime` |

### 5.2 What this design explicitly does NOT change

`AssignRole`, `CreateRole`, `RevokeRole`, the `holdsRole → operator` root topology, the anchor cypher's
literal grant set, and `SystemActorKeys`' predicate — all Fork A's ratified model (§3.2). Nor
`TombstonePermission` / `RevokePermission`: both only narrow, and the operator must keep a way to retract
a grant without an uninstall.

**The retraction they provide is not durable, and that is a real defect — just not this design's.** The
adversarial pass traced it, and a later scout re-pinned the arm: for a key present in **both** the old and
new declared sets, the **surviving-key** arm (`internal/pkgmgr/upgrade.go:324-331`) emits an **`update`**
when the committed doc differs from the rebuilt one — and it differs *because* `docLink` stamps
`isDeleted:false` explicitly (`build.go:1006`), which `logicalDocEqual` (comparing only the fields the NEW
document carries) sees diverge from the tombstone. Step 8's update arm
(`internal/processor/step8_commit.go:435-439`) has **no aliveness guard**, so the update silently
un-tombstones. (The nearby re-add arm, `upgrade.go:298-315` — a package dropping an entity and adding it
back — revives **by design**, with a test asserting it; the bug is only the surviving-key arm.) Permission
keys are version-independent (`installer.go:411-420`, `:473-481`), so the condition holds forever. **Net: a deliberately revoked
package grant is silently restored by the next `lattice-pkg` upgrade or `make reinstall-package`
(`Makefile:1496-1501`)** — no `GrantPermission` call, no restoration event.

This predates the design, but **Inc 1 ships a mandatory version bump, which is exactly such an upgrade** —
so shipping it would *fire* the latent bug on any deployment that had revoked one of the retained grants.
It is therefore sequenced as a **prerequisite** (§9 row 1, `seq:` before Inc 1), not a footnote. Note the
asymmetry the same trace establishes, which works in the design's favour: the *withdrawn* op falls into
`old \ new` and is correctly **tombstoned** (`upgrade.go:334`, `:359-367`), never revived.

**The DDL branch for `UpdatePermission` stays.** Only the grant is withdrawn, not the Starlark: the
`permittedCommands` conformance assertion (`scripts/verify-package-rbac.go:180-192`) stays green, and
removing an op branch from a shipped package is a migration cost for a behaviourally identical result. An
ungranted op is denied at step 3 by absence (§6.8) — the same fail-closed the withheld shred verbs rely
on.

### 5.3 Alternatives considered

| Alternative | Why it loses |
|---|---|
| **Withdraw all three grants** (my own first recommendation) | **Falsified by the adversarial pass.** It breaks a required CI job (`stack-gates` → `make test-hello-lattice`) and the shipped tutorial, and — the structural objection — it leaves `CreateMetaVertex`-authored ops permanently ungrantable by anyone (§4). It rested on a "zero live callers" census whose `_test.go` exclusion was the premise. Recorded here rather than deleted: the reasoning that produced it (dead surface ⇒ delete) is right, and it was the *census* that was wrong. |
| **A bare never-self-grantable set** (the row's + privacy-base's literal ask) | Keys on `operationType` alone, and at step 3 a package-installed entry and a hand-minted one are **identical** — so a deny-list on `ShredRetentionClassKey` also refuses the future *package* that legitimately grants it, breaking the exact "deployment says so on purpose" path privacy-base is protecting. Not wrong, **incomplete**: it becomes correct once provenance makes the distinction expressible, which is why §5.1 Move 3 is this alternative *plus* Moves 1–2. |
| **`requesterHolds` — a grant may not exceed the caller's own held scope** (mirroring `internal/pkgmgr/capabilitymaterializer.go:804-808`) | The right invariant for *escalation*, and it does not bite the harm this item is actually about: root holds `InstallPackage`, so under `requesterHolds` root may still confer anything (§3.1). The harm is attributability, not reachability. **Retained as normative for any future runtime grant channel** (§6, contract clause), not as this fix. |
| **Delete the `UpdatePermission` DDL branch too** | Behaviourally identical to withdrawing its grant, but costs `permittedCommands` churn + the verify-gate rewrite. Rejected on cost, not principle. |
| **Live-vs-declared permission reconciler** (flag any `vtx.permission.*` no manifest declares) | Genuinely missing (§1.1) — and under *this* shape it has a real consumer, since `CreatePermission` survives. But provenance (Move 2) gives the same answer far more cheaply: an entry is self-describing, so nothing needs to be reconciled against a manifest to know its origin. Filed (§9) as the auditor's follow-on, not a precondition. |

**Re-asking the discipline question — could a variant of a rejected option beat the recommendation?**
Yes, and it is the fork in §5.4: *withdraw `CreatePermission`/`GrantPermission` too and rewrite the
tutorial onto packages.* It is cheaper (no lens change, no step-3 change, no contract clause about
origin) and it yields a stronger one-sentence invariant. It loses on capability, not on cost — see §5.4.

### 5.4 The fork — may an op authored outside the package plane be granted at all? RESOLVED: Branch A

**Resolved by Andrew at ratification, 2026-08-13: Branch A.** The two branches were coherent end-states,
and the choice was a product judgement about what `CreateMetaVertex` is *for*. Branch B is recorded below
as the rejected alternative.

**Branch A — grantable, with provenance (ratified; this design).** Ad-hoc DDL authoring stays a real
capability: author with `CreateMetaVertex`, grant with `CreatePermission`+`GrantPermission`, and the grant
is now marked `origin: runtime`, visible to the auditor and refusable for reserved verbs. Tutorial and the
NFR-P3 probe survive untouched. Cost: the lens projects one more field, step 3 gains one check, and the
contract gains a clause. **Why I prefer it:** the audit gap is closed *without* deleting a working
capability, and the exploratory path — author a type, grant it, try it — is a genuine part of what makes
the platform pleasant to learn.

**Branch B — not grantable; packages are the only grant surface (rejected).** Withdraw all three, rewrite
hello-lattice Milestones 3 and 5 to ship `CreateBook` as a small package, re-home the NFR-P3 probe onto an
op that already exists, and accept that `CreateMetaVertex`-authored ops are authorable-but-uninvokable.
The invariant becomes a single sentence with no exceptions, which is worth real money on a security plane.
Cost: a working capability is deleted, and the tutorial's "here is how you grant a capability" lesson
becomes "install a package," which is correct but a heavier first experience.

**What did not depend on the fork:** Move 1 (withdraw `UpdatePermission`) was right under both branches —
it is uncalled under both. With Branch A ratified, Moves 2–3 proceed as specified; Inc 1 was written
branch-independent (§7) and stays as written.

---

## 6. The invariant, and the gate that binds the next author

Withdrawing the grants clears today's debt. Nothing stops tomorrow's agent from adding
`mk("CreatePermission")` back, or a new package from granting it — it is exactly what someone would
reach for when wiring an admin UI. **Per the lint doctrine, the gate ships in this design, as a required
increment, blocking from day one** (the migration leaves zero debt, so a warn-first gate would be the
fingers-crossed state the fire exists to end).

**The invariant.** *Every permission vertex declares its origin, and origin decides what it may confer.
A `package`-origin vertex may grant any operationType its package declares; a `runtime`-origin vertex may
never confer a core-reserved operationType. Origin is write-once — no operation may rewrite a permission
vertex's body — and an absent origin reads as `runtime`. The kernel anchor's literal grant set
(`internal/bootstrap/lenses.go:135-148`) is the one core-owned exception, being seeded, not authored.*

The gate below binds the first clause's precondition — that no package re-grants a body-rewriting op.
(Under Branch B, §5.4, the invariant simplifies to *"a capability grant is authored by a package
install"* and the gate's set widens to all three ops; the gate's shape is identical either way.)

**The gate must be structural, not a source scan — and this is the one place I had it wrong first.** The
obvious shape is a `lint-conventions.go` text scan for `PermissionSpec{OperationType: "CreatePermission"}`
in `packages/**`. It cannot work: rbac-domain declares its grants as **`mk("CreatePermission")`**
(`permissions.go:10-29`), a helper call, and roughly a fifth of the corpus is built the same way —
`componentReadPermission(component)` loops in `console-operator`/`control-authz`, named-constant ops in
`clinic-reminders`/`wellness-reminders`. A literal scan would miss the exact idiom the gate exists to
catch, and a filename glob would miss the four files `clinic-reminders` splits its specs across.

**So the gate goes in `scripts/lint-package-standard.go`, over the package registry** — the single
enumeration of shipped packages as **compiled `pkgmgr.Definition`s** (`internal/pkgregistry/registry.go`,
`Names()`/`Lookup()`/`All()` at `:93`/`:86`/`:104`). That file already runs the exact loop this gate needs:
`scripts/lint-package-standard.go:211-212` iterates `pkgregistry.Names()` and `Lookup`s each `Definition`,
so the new rule is a few lines inside an existing walk, not new wiring. Reading `Definition.Permissions`
evaluates every helper, loop, and constant to its real `OperationType` value — and registration is itself
enforced (`lint-package-standard.go:8-9`: the rules run *"over every package in internal/pkgregistry, so a
package cannot"* escape by going unregistered).

- **Fail** any `PermissionSpec` whose `OperationType` is in the **body-rewriting set** — `UpdatePermission`
  today (all three under Branch B). `CreatePermission`/`GrantPermission` stay grantable under the
  recommended branch, so the gate deliberately does **not** list them: its job is to protect Move 1's
  write-once precondition, not to relitigate the fork.
- **The escape hatch is a declaration, not a flag.** Following the shipped default-deny-plus-declare
  pattern (`scripts/lint-conventions.go`'s `# read-posture:` convention), a package that must re-grant one
  declares it — an exported `GrantAuthoringSanctioned() []string` on the package, or a registry-side
  allowlist keyed by package name — so re-granting is a deliberate, reviewable act rather than an
  accident. Declaring is cheap; forgetting fails closed.

This is the §8 C2 lesson applied to the gate itself: **enumerate by the declaration, never by the
literal** — the same reason C2 resolves helper-built specs by hand rather than trusting a grep.

---

## 7. Decomposition for the Steward

Three increments plus a **prerequisite**. Inc 1 and Inc 2 are branch-independent; **Inc 3 is the
Branch-A enforcement, unblocked by the 2026-08-13 ratification**. Inc 1 and Inc 3 change an
authorization surface and take the **full review pass**; Inc 2 is a lint gate at the Steward's normal
sizing (`agents/steward/SKILL.md` §4).

> **Prerequisite (§9 row 1, `seq:` before Inc 1) — fix the tombstone-revive on package upgrade.** Inc 1
> ships a mandatory version bump, and an upgrade currently un-tombstones any surviving key an operator
> deliberately revoked (§5.2). Land the aliveness guard first, or Inc 1 silently restores revoked grants
> on every deployment that ever ran `RevokePermission`. This is the one ordering constraint in the design.

### Increment 1 — withdraw `UpdatePermission` *(posture-changing: full review pass; both branches)*

1. Delete `mk("UpdatePermission")` from `packages/rbac-domain/permissions.go`; update the doc comment's
   "10 permission vertices" to 9 and say **why** it is absent — a body-rewriting op on the security plane
   with no caller — so the reader does not re-add it as an oversight.
2. Bump `rbac-domain`'s version and update `manifest.yaml`'s permission count — an edit without a version
   bump no-ops on apply, and `VerifyAgainstDefinition` (`manifest.go:150-152`) fails on a count mismatch.
3. **Split `rbacExpectedOps` in `scripts/verify-package-rbac.go`** (`:52-58`). It drives two assertions
   that must now diverge: the `permittedCommands` check (`:180-192`) still expects **all ten** (the DDL
   branch stays, §5.2), while the permission-vertex + `grantedBy`-link loop (`:456-490`) must expect the
   **nine granted**. Split the constant rather than editing one list, or the gate asserts a grant this
   increment removed.
4. **Already-minted vertices are out of scope and the build note must say so.** Withdrawing a
   `PermissionSpec` tombstones the package's own declared keys on upgrade (§5.2); it cannot retract a
   vertex the installer never declared. Inc 1's e2e asserts the *fresh-install* posture; a sweep of
   pre-existing runtime-minted vertices is filed (§9), not assumed.

**Tests (owned by Inc 1).** A package test asserting exactly nine specs, none of them `UpdatePermission`.
An e2e on the ephemeral stack: install rbac-domain, submit `UpdatePermission` as the primordial admin,
assert `AuthDenied` "no matching platformPermission" — with a **positive vector first** (`CreateRole` as
the same actor, asserted *allowed*) so the denial cannot pass vacuously on a broken fixture.
**`make test-hello-lattice` must be run**: it is `-tags integration`, so the default `go test ./...` does
not compile it and cannot catch a break there.

### Increment 2 — the lint gate *(blocking from day one; both branches)*

The §6 check in `scripts/lint-package-standard.go`, inside the existing `pkgregistry` walk (`:211-212`),
plus its unit test. The test needs a **positive vector first**: a fixture `Definition` carrying
`mk("UpdatePermission")` — the *helper-built* form, not a literal — must FAIL, proving the gate sees
through the indirection that defeats a source scan; the same spec with the sanction declaration must PASS.
A literal-only fixture would let a text-scanning implementation pass the test vacuously. No new CI wiring.

### Increment 3 — provenance + the reserved set *(posture-changing: full review pass)*

§5.4 resolved (Branch A, 2026-08-13) — this increment is unblocked once the prerequisite and Inc 1 land.
Three sub-steps, in order, because each is the next one's precondition:

1. **Stamp origin.** `CreatePermission` writes `data.origin: "runtime"`
   (`packages/rbac-domain/ddls.go:306-322`); `internal/pkgmgr/build.go:357-368` writes
   `data.origin: "package"` + `data.declaredBy`. Sound only because Inc 1 made the body write-once.
2. **Project it.** Add `origin: perm.data.origin` to `capabilityRolesSpec`
   (`packages/rbac-domain/lenses.go:80-91`) and to the Contract #6 §6.4 `platformPermissions` field table.
   The projection is a **new field on an auth-plane lens**, so it inherits the §6.2 seq guard unchanged —
   no new state, no new lifetime.
3. **Reserve on it.** The core set + the refusal in `matchPlatformPermission`
   (`internal/processor/step3_auth_capability.go:503-586`), mirroring `disallowedPrivilegedLanes`
   (`:439-451`) in shape: refuse the entry, **continue the scan** (an actor may hold the same op from a
   package entry too, and that one must still win), raise `AlertCodeReservedOperationGrantRejected`.

**Tests (owned by Inc 3).** Table-driven step-3 tests over the §5.1 matrix — the four cells, each
asserted, plus the mixed case where an actor holds **both** a runtime-origin and a package-origin entry
for a reserved verb and the package entry must still authorize (this is the case a first-match
implementation gets wrong, and it is why the scan continues). **Absent-origin must be tested as
`runtime`**, not as a skip — the fail-closed default is the whole safety of the migration. Plus a
capadv vector in `internal/bypass`: mint `ShredRetentionClassKey` at runtime, assert refusal **and** the
Health alert.

**Also owned by Inc 3 (Branch A) or Inc 1 (Branch B) — rewrite privacy-base's obligation comment**
(`packages/privacy-base/permissions.go:59-67`). It currently tells the reader that closing the gap
*"needs a core-owned never-self-grantable operationType set, which is filed, not assumed here."* Whichever
branch ships, that paragraph must be rewritten to describe what actually landed, or the next reader
re-files the bare deny-list §5.3 rejected.

---

## 8. Executable censuses

Re-runnable at Phase 0 rather than trusted as prose.

**C1 — per-op caller census. The first version of this was wrong and the correction is the design
(§4).** It excluded `_test.go`, which is precisely where the live callers are; a negative claim is only as
wide as its sweep, and that `grep -v` was the whole premise. It must cover tests, docs, and examples, and
must be run **per op**, because the three differ — that is what §5 turns on.

```bash
for op in CreatePermission UpdatePermission GrantPermission; do echo "### $op"; grep -rln "$op" --include="*.go" --include="*.md" --include="*.js" --include="*.html" --include="*.ts" . | grep -v node_modules | grep -v "^./_bmad-output"; done
```

Expect — and classify every hit, since a *declaration* is not a *caller*:

| Op | Invoking callers | Verdict |
|---|---|---|
| `UpdatePermission` | **none** — every hit is a DDL dispatch branch, a `permittedCommands` list, a spec fixture, a README, or a conformance list | withdraw (Inc 1) |
| `CreatePermission` | `internal/hellolattice/hellolattice_test.go` (Milestones 3 + 5 + the NFR-P3 probe), `docs/hello-lattice.md:235` | keep (Branch A) |
| `GrantPermission` | same three sites, `docs/hello-lattice.md:250` | keep (Branch A) |

**C1b — the CI gate that the caller census implies.** `make test-hello-lattice` is `-tags integration`
(`Makefile:1725-1728`) and runs in the `stack-gates` job (`.github/workflows/ci.yml:243`). A build-tagged
suite is **not** compiled by `go test ./...`, so no default gate would catch a break here. Any increment
touching the trio must run it explicitly.

```bash
grep -rn "test-hello-lattice" Makefile .github/workflows/ci.yml
```

**C2 — distinct permissioned operationTypes (the number that sinks the deny-list).** Expect **187**.
Counts distinct *values*, not matching lines, and resolves the helper-built `ctrl.<component>.<verb>`
and named-constant specs that a literal-only grep truncates (naively, `"ctrl." + component` greps as the
bogus partial `"ctrl."`). Files declaring specs:

```bash
find packages internal -name '*.go' ! -name '*_test.go' | xargs grep -l 'PermissionSpec{' | wc -l   # expect 35 files / 28 packages
```

**C3 — no live-vs-declared reconciliation exists for permission vertices.** Expect: no hit that reads
Core KV `vtx.permission.*` and compares against a manifest, other than
`scripts/verify-package-rbac.go`'s declared→live loop.

```bash
grep -rn "vtx.permission" --include="*.go" internal/ scripts/ | grep -v "_test.go"
```

**C4 (pins Inc 1) — after the change, exactly nine specs.** A package test, not a grep:
`len(Permissions()) == 9` and the set does not contain `UpdatePermission`. (An earlier draft of this
census expected seven — a residue of the falsified withdraw-all-three shape; nine is Inc 1's number.)

---

## 9. Contract surface, board, and spawned rows

**Frozen-contract edit — `docs/contracts/06-capability-kv.md` §6.1, committed at ratification
(2026-08-13), carrying a transitional note until Inc 3 lands.** §6.1 previously specified who *projects*
grants ("core owns the bucket + the step-3 reader; packages project the grant types they own") but was
silent on who may *author* the permission vertices those projections walk — the exact gap this item
occupies. The edit adds one short normative paragraph carrying the §6 invariant: origin is declared and
write-once, origin decides what may be conferred, and any future runtime grant channel must also enforce
`requesterHolds`. Affected consumers: `rbac-domain` (Inc 1 + Inc 3 step 1), the `capabilityRoles` lens
(Inc 3 step 2), step 3 (Inc 3 step 3), and the `lint-package-standard` gate. Inc 3 removes the
transitional note and adds the `origin` row to the §6.4 field table.

**Board row** — ✅ ratified Branch A (2026-08-13), low priority per Andrew, `seq:` behind the
un-tombstone row; row text carries §2's corrections (three ops, not two; `ShredRetentionClassKey`, not
"the shred verbs").

**Spawned rows** (filed, not folded — each is a different mechanism):

1. **[Pkgmgr] A package upgrade silently un-tombstones a deliberately revoked grant** (§5.2) — the
   surviving-key branch emits an `update` (`upgrade.go:324-331`, via `docLink`'s explicit `isDeleted:false`,
   `build.go:1006`) and step 8's update arm has no aliveness guard (`step8_commit.go:435-439`), so
   `make reinstall-package` or any version bump revives what `RevokePermission` retracted. The re-add arm
   (`:298-315`) revives by design and is not the bug. **`seq:` before Inc 1**, which ships exactly such a
   bump. ★★ · S–M.
2. **[Processor] The root actor set is a boot snapshot of a live topology** (§3.3) — four binaries
   snapshot `SystemActorKeys` independently at boot; a newly-granted operator is unread until restart and
   the binaries can disagree. Revocation is fail-closed, so this is grant-latency + cross-binary
   consistency, not over-grant. ★ · S–M.
3. **[Docs] Four sites describe the root actor set as "kernel-seeded"; Fork A made it role-derived**
   (`capabilitykv/keys.go:47-56`, `cmd/processor/main.go:138-140`, `cmd/{refractor,weaver,loom}/main.go`,
   Contract #6 §6.1's "bounded to the kernel-seeded root actors"). Stale prose post-Fork-A — and it is
   what sent this fire down a wrong path before the ratified design corrected it. ★ · XS.
4. **[Pkgmgr] No live-vs-declared reconciliation for permission vertices** (§1.1) — under Branch A the
   auditor's natural follow-on once `origin` exists (it makes each entry self-describing, so the reconciler
   is a convenience rather than the mechanism). Not a precondition for any increment. ★ · S.

Rows 2–4 are **not** blockers for this design. Row 1 is.

---

## 10. Reconciliation with the existing mental model

**"Didn't we already handle this?"** Partly, twice, and neither reaches it.
`privilegedLaneAllowlist` (`step3_auth_capability.go:419-430`) is core-owned policy over what a
package-projected grant may do **at a privileged lane** — it constrains a grant's *lane*, never its
existence, so it is silent on a default-lane self-mint. `validateGrantArtifact`'s `requesterHolds`
(`capabilitymaterializer.go:804-808`) *is* the no-escalation invariant, but it guards the AI-authored
capability path only; rbac-domain's ops never adopted it. This design's Move 3 is the **first** of the two
generalized: `privilegedLaneAllowlist`'s exact shape (core-owned constant, refuse-and-continue, Health
alert) applied to *which verb* rather than *which lane*. The contract clause makes the second normative
for any future channel.

**Does this contradict an established pattern?** No — it *completes* one. Contract #6 §6.1's
contract-contribution model already says packages own the grant types they project; it never said who may
*author* the vertices those projections walk. This makes authoring legible to projection.

**Does it introduce new state — and do we already keep that state somewhere?** One field,
`data.origin`, on a vertex the installer already stamps with `operationType`/`scope`/`note`/`lanes`
(`build.go:357-368`). It needs **no lifetime rules**, which is the point of Move 1 ordering before Move 2:
the field is written exactly once, at authoring, by one of two writers, and no operation can rewrite it —
so there is no reset/carry/ordering boundary to specify. The projected copy inherits the auth-plane lens's
existing §6.2 seq guard; no new guard, no new tombstone semantics.

**Why is a self-mint containable at all today?** One accident worth naming so nobody relies on it:
neither `CreatePermission` nor `UpdatePermission` accepts a `lanes` parameter (`ddls.go:306-341`), so a
runtime-minted grant carries no per-entry lanes and falls back to `capabilityRoles`' static
`Lanes: ["default"]` (`lenses.go:51-57`) — which is why a self-mint cannot reach `InstallPackage` at the
`meta` lane through *this* channel. That containment is a **property of the DDL's parameter list, not a
mechanism**: adding a `lanes` param to `CreatePermission` — an obvious future convenience — would remove
it silently, with no gate.

Under the recommended branch `CreatePermission` survives, so this accident is **not** cleaned up by
withdrawal and must be made deliberate instead. Inc 3 therefore carries a small explicit obligation: the
privileged-lane allowlist must be evaluated against a `runtime`-origin entry the same way the reserved set
is, so that a future `lanes` parameter cannot silently confer a privileged lane on a self-minted grant.
Named here rather than left implicit, because the containment currently reads as intentional and is not.

---

## 11. Risks

- **The migration population.** Permission vertices minted before Inc 3 carry no `origin`. Rule 2 reads
  absence as `runtime` — fail-closed, and the right direction — but it means a *package*-installed vertex
  from before the change is treated as runtime until its package is re-applied. For a reserved verb that
  would refuse a legitimate package grant (a visible over-deny, never a silent over-grant). Inc 3 must
  therefore land the installer's stamp **and** re-apply the packages in the same fire, and the build note
  must say which populations were re-stamped. This is the one place the design touches live state.
- **The reserved set is still a curated list**, just a much smaller and better-typed one — it now bounds
  only *runtime* grants, so forgetting an entry loses attribution on one verb rather than opening the
  package plane. v1 is `ShredRetentionClassKey`; adding to it is a Processor constant edit and a test.
- **A future admin UI wants runtime granting.** That is now *supported* rather than forbidden — it stamps
  `origin: runtime`, inherits the reserved-set refusal, and per the contract clause must also enforce
  `requesterHolds`. This is a strictly better position than Branch B leaves us in.
- **Branch B's risk (moot — B rejected at ratification):** hello-lattice Milestones 3 and 5 plus the
  NFR-P3 probe would have needed re-authoring, and the probe in particular depends on minting a *fresh
  uncached* operationType per iteration — re-homing it onto a package-declared op changes what it
  measures. That rework was the real cost of B, and part of why A won.

---

## 12. Un-tombstone prerequisite (§9 row 1) — fire brief (build note, 2026-08-14)

**Scope sentence.** Fix `diffManifest`'s surviving-key branch so an out-of-band tombstone
(`RevokePermission`/`TombstonePermission`) on a key the package continues to declare is respected across
the next upgrade, instead of being silently revived by the body-diff update path (§5.2).

**Verified touch-list** (checked live, this fire):
- `internal/pkgmgr/upgrade.go:317-332` — `diffManifest`'s surviving-key branch (`survives == true`). After
  the `committed == nil` re-create check and before `logicalDocEqual`, add: if `committed["isDeleted"] ==
  true`, `continue` (skip — do not emit an update). This key can only be tombstoned while surviving by an
  out-of-band op: the package's own diff tombstones exclusively `old \ new` (line 358), never a key present
  in both sets, so `committed.isDeleted==true` here is unambiguous evidence of a deliberate external
  revocation, not a partial upgrade state.
- `internal/pkgmgr/upgrade.go:204-225` (`diffSummary`) — add a `revocationsRespected int` counter,
  incremented on the new skip branch, mirroring the existing `revived`/`reactivation` fields' purpose:
  making a silent-by-default outcome visible to the operator.
- `internal/pkgmgr/upgrade.go:95-105` (`UpgradeResult`) — surface as
  `RevocationsRespected int`, same pattern as `ReactivationRequired`.
- `internal/pkgmgr/apply.go:28-` (`ApplyResult`) and `:117-129` (`Apply`'s in-place branch) — same field,
  since `Apply` shares `computeDeltaAgainst` → `diffManifest` and hits the identical path on
  `make reinstall-package` / `refresh-<vertical>`.
- `internal/pkgmgr/upgrade_test.go` — new test asserting the fix; mirror `TestUpgrade_ReAddsRemovedEntity`'s
  harness shape (`newInstallerHarness`, `sampleDef`, `kvDoc`) but tombstone the surviving permission
  out-of-band via `conn.KVUpdate` (pattern: `TestUpgrade_RaceOnTombstonedKeyRejected:358-404` for the
  direct-KV-write technique) *before* running an upgrade that still declares the same permission
  unchanged, then assert it is still tombstoned after and `RevocationsRespected == 1`. Also re-run
  `TestUpgrade_ReAddsRemovedEntity` and `TestUpgrade_DiffCreateUpdateTombstone` (unchanged expectations —
  the fix must not touch the `!survives` re-add arm or the tombstone-emission arm) and
  `TestUpgrade_DeltaCarriesExpectedRevision` to confirm OCC conditioning is untouched by the new skip.

**Precedents mirrored:** the skip-without-counter shape already exists one branch below
(`logicalDocEqual` body-equality skip, `upgrade.go:324-326`) — the new check is the same shape, checked
first. The `RevocationsRespected` field mirrors `ReactivationRequired`'s "success needs a visible caveat"
convention (`upgrade.go:39-43`).

**Increment order (one increment, XS–S in practice):**
1. `diffManifest` skip + `diffSummary`/`UpgradeResult`/`ApplyResult` field — `go build ./internal/pkgmgr/...`.
2. New test + the three regression re-runs — `go test ./internal/pkgmgr/... -run 'TestUpgrade|TestApply' -v`.
3. Full gates (below).

**In-scope gotchas (standing checklist + component dossier):**
- Capability/security-plane change (permission durability) → full 3-layer adversarial review at admit,
  regardless of size (steward SKILL.md §4), never lead-review-only.
- **One deterministic key, one writer** (checklist #5): this fix does not add a second writer — it removes
  a spurious one (the surviving-key arm should never have written this key while it is tombstoned).
- **Every census is a premise** (checklist #2): "the package's own diff tombstones exclusively `old \ new`"
  is re-verified live above at `upgrade.go:334-367` — confirmed: the only other `Op: "tombstone"` emission
  site in `diffManifest`.
- `docs/components/pkgmgr.md`'s dossier (6 active entries) carries forward: "Two writers of one
  deterministic key" (checklist #5, already cited above) is the closest match; none of the others bind
  this fix directly.

**Adjacent finds:** none surfaced beyond the three already-spawned sibling rows (§9 rows 2–4), which are
explicitly not blockers for this row and stay filed as-is.

**Non-goals:** the re-add arm (`!survives`, `:298-315`) is untouched — its revive is intended and tested.
Provenance (`origin` field, Inc 2–3 of this design) is not part of this prerequisite; it lands after,
per the ratified sequencing (§9).

**Shipped (build note, 2026-08-14).** Built as briefed, then narrowed by a cold opus adversarial review
before merge — the review's core finding: the first-cut guard skipped revival for **every** surviving
tombstoned key, including package-owned `vtx.meta.*` definitions (lenses/DDLs/panes/opMetas), which is
wider than this design's ratified scope (grant durability only) and removes an unanalyzed,
never-ratified definition-repair behavior (`ReactivationRequired`/opMeta-retirement-guard interactions
were never evaluated against it). **Fix landed:** the skip is scoped to `!strings.HasPrefix(key,
metaVertexPrefix)` — grant/role topology (`vtx.permission.*`, `vtx.role.*`,
`lnk.permission.*.grantedBy.role.*`) only; `vtx.meta.*` definitions keep the pre-existing body-diff revive
path, unchanged. (Note for the next reader: `internal/bootstrap/reconcile.go`'s tombstone rule is
*unconditional* — it never revives anything tombstoned, definitions included — so it grounds only the
*shape* of a definition-vs-topology split, not a claim that reconcile itself revives tombstoned
definitions; the actual justification for the narrowing is ratified-scope discipline, not that precedent.)

Also landed, all review-driven: a `noChangesReason` helper so a Force/no-op run that respected a
revocation no longer reports the false "already matches (no changes)"; `apply.go`'s pre-existing
`Created: sum.created` (omitting `sum.revived`) fixed to match `upgrade.go`'s construction; the counter
surfaced to both operator surfaces (`cmd/lattice-pkg`'s `logApplyResult` Warn, `cmd/loupe/pkg.go`'s
`applyReply` JSON) since an unread counter is not visibility; and test coverage extended to the literal
`RevokePermission` shape (the grant **link**, not just the permission vertex), an `Apply`+`Force`
same-version case (the actual `make reinstall-package`/`refresh-<vertical>` trigger), and a negative test
proving the narrowing (`TestUpgrade_RevocationGuardExcludesDefinitions`).

**Contract.** `docs/contracts/08-package-install.md` §8.6 carries a normative addition (the second
omission condition) — staged **UNCOMMITTED** in `main` per CLAUDE.md, flagged for Andrew; not part of the
2026-08-13 ratification, which only touched contract 06 §6.1.

**Dossier.** New entry filed to `docs/components/pkgmgr.md`: a security-plane skip guard keyed on mere
key survival, with no anchor-type check, silently widens past its ratified scope to cover schema/routing
definitions the design never analyzed — check: key the guard on the anchor-type prefix
(`metaVertexPrefix`/`vtx.meta.`), never on tombstone-state alone.

## 13. Increment 1 + Increment 2 fire brief (build note, 2026-08-14)

**Scope sentence.** Ship §7 Increment 1 (withdraw the `UpdatePermission` grant from `rbac-domain`, making
a permission vertex's body write-once — nine specs remain) and Increment 2 (a `scripts/lint-package-standard.go`
gate that fails any package's `PermissionSpec` for `UpdatePermission`, blocking from day one), the two
branch-independent, non-Inc-3 increments now unblocked by the un-tombstone prerequisite (CLEARED
2026-08-14, `0bb6daea`).

**Verified touch-list** (re-checked live this fire, zero divergence from the design's citations — all
five files last touched 2026-08-11, inside the design's own authoring window):
- `packages/rbac-domain/permissions.go:19-29` — the ten `mk(...)` calls; remove `mk("UpdatePermission")`
  (line 23). Doc comment at line 5 ("Permissions returns the 10 permission vertices…") → "9".
- `packages/rbac-domain/manifest.yaml` — version `0.3.4` → bump; remove the `operationType:
  UpdatePermission` entry (line 35).
- `scripts/verify-package-rbac.go:52-57` (`rbacExpectedOps`) — split into two constants: one used by the
  `permittedCommands` check (`:180-191`, stays all ten — the DDL branch is untouched, §5.2) and one used by
  the permission-vertex + `grantedBy`-link loop (`:456-507`, drops to nine).
- `packages/rbac-domain/ddls.go` — ` UpdatePermission` DDL dispatch branch (`:325`), `permittedCommands`
  list entry (`:56`), and doc comments (`:10,35,74-77,122`) **stay** (§5.2: only the grant is withdrawn,
  not the Starlark).
- `packages/rbac-domain/package_test.go:32`, `testhelpers_test.go:42`, `integration_test.go:368` — spec
  fixtures asserting the full ten-op shape; each needs the Inc-1 e2e's positive/negative pair folded in or
  a sibling test added (builder's call, mirroring existing fixture shape).
- `scripts/lint-package-standard.go:211-212` — existing `pkgregistry.Names()`/`Lookup()` walk; add the new
  rule inside it, reading `Definition.Permissions()` (not a source scan).
- `internal/pkgregistry/registry.go:86,93,104` — `Lookup`/`Names`/`All`, confirmed unchanged, no edit
  needed here.

**Precedents to mirror:**
- The lint gate mirrors `lint-package-standard.go:91-128`'s existing `[no-op-meta: <code> — <prose>]`
  declared-exception convention (closed vocabulary in a `Note` field) for its escape hatch, per §6's
  "declaration, not a flag" instruction — **not** a separate allowlist file. A `PermissionSpec` that must
  re-grant `UpdatePermission` declares it in its own `Note`.
- Inc 1's e2e mirrors the existing `AuthDenied "no matching platformPermission"` assertion shape already
  used elsewhere in the step-3 test corpus (builder locates the nearest sibling at build time — no single
  cited precedent file for this exact denial shape).
- `rbacExpectedOps` split mirrors the general "split the constant rather than editing one list" instruction
  in §7 Inc 1 step 3 verbatim.

**Increment order + runnable green checks:**
1. Inc 1 code: remove the grant, bump version + manifest count, split `rbacExpectedOps` →
   `go build ./packages/rbac-domain/... ./scripts/...`.
2. Inc 1 tests: package test (exactly nine specs, no `UpdatePermission`) + step-3 e2e (positive `CreateRole`
   allowed, then `UpdatePermission` denied) →
   `go test ./packages/rbac-domain/... -run 'TestPermissions|TestRBAC' -v` and
   `make test-hello-lattice` (build-tagged `integration` — **not** covered by default `go test ./...`,
   must run explicitly per C1b).
3. Inc 2 code: the lint rule + its unit test (positive vector: helper-built `mk("UpdatePermission")`
   fixture FAILS; same spec + `[no-op-meta:…]`-style sanction PASSES) →
   `go run ./scripts/lint-package-standard.go` and
   `go test ./scripts/... -run TestLintPackageStandard -v`.
4. Full gates: `go build ./...`, `make vet`, `golangci-lint run ./...`,
   `STRICT=1 go run ./scripts/lint-conventions.go`, `make verify-package-rbac`,
   `go test ./packages/rbac-domain/... ./scripts/... ./internal/processor/...`.

**In-scope gotchas (standing checklist + component dossiers, copied in):**
- **Capability/security-plane change** → full 3-layer adversarial review at admit, regardless of size
  (steward SKILL.md §4) — Inc 1 changes an authorization surface, Inc 2 is its guarding gate; both reviewed
  together as one posture-changing unit.
- Standing checklist #3 (negative test needs its positive vector first) — binds the Inc-1 e2e (`CreateRole`
  allowed before `UpdatePermission` denied) and the Inc-2 lint test (sanctioned spec PASSES before the
  unsanctioned one is asserted to FAIL).
- Standing checklist #6 (precedent may carry debt) — the `[no-op-meta:…]` convention is verified live
  above (item 7 of the scout report), not assumed from the design doc's description of it.
- `docs/components/pkgmgr.md` dossier: "Two writers of one deterministic key" (not directly triggered —
  this fire removes a writer, adds no new one) and "canonicalName vs instance-key segment" (not triggered —
  no canonicalName logic touched). `docs/components/processor.md` dossier: "a silently-rejected op logs at
  Info" — relevant if the Inc-1 denial e2e needs to inspect *why* `UpdatePermission` was refused; raise the
  test logger to WARN/capture step-3's reason rather than trusting silence.
- `make test-hello-lattice` is `-tags integration` (C1b) — must run explicitly; a green default `go test
  ./...` does not prove Milestones 3/5 or the NFR-P3 probe are unaffected (they use `CreatePermission`/
  `GrantPermission`, not the withdrawn op, so expected to pass untouched — verify, don't assume).

**Adjacent finds:** none beyond the three already-spawned sibling rows (§9 rows 2–4, not blockers) and the
already-filed reconciler follow-on (§5.3, seq'd behind Inc 3). Nothing new surfaced by the scout.

**Non-goals:** Increment 3 (provenance stamp + reserved set) is explicitly out of scope for this fire — it
depends on Inc 1's write-once precondition but is its own posture-changing unit, sized M, and follows as a
separate fire. The `UpdatePermission` DDL dispatch branch, `permittedCommands` list entry, and doc comments
in `ddls.go` are untouched per §5.2 (Starlark stays; only the grant is withdrawn).
