# Package authority-minting provenance — closing the create-forgery gap

**Status: ✅ RATIFIED-AND-PARKED (Andrew, 2026-08-21).** Design signed off; **not scheduled**. Board row
is `🗄️ shelved`, not build-ready `✅ ratified` — the Steward must not pull it until the revive trigger
below fires. The security analysis stands; parking is a capacity/priority call, not a rejection.

**The decisions (Andrew, this session):**
- **Increment 1 only.** Inc 1 (the commit-time admission guard + server-derived `origin` stamp) is the
  actual hole-closure. **Increment 3** (the `pkgstd` authoring gate + 54-pair sanction migration) is a
  lint ratchet, **deferred** — it rides the revive fire or a later hardening pass, never blocks Inc 1.
  **Increment 2**'s `bucketguard` reserved-key-pattern refusal overlaps the standing ★★★ *"three
  admission holes let an authored artifact reach the auth plane"* row (`lattice.md`) — **folded there**,
  not carried by this design.
- **R3 is DROPPED.** DD found R3 as drafted refused a shipped flow (it keyed the *submitting* actor at
  `scope: any`, while the invariant it claimed to mirror checks the *proposer* at the *requested* scope).
  R0 + R1 close the named escalation for the shapes that matter; R3 is not revived without a consumer
  that needs it and a corrected scope predicate. Every "R0, R1 and R3" below reads **R0 + R1**.
- **Fork §13 resolved to Branch A** — "my own ops, yes; other people's, no": a non-root console operator
  may install a package that confers only operationTypes the package itself implements (R1), never a
  core-owned authority-minting op. Branch B (withdraw the lifecycle trio from `consoleOperator`) and C
  (signed manifests) are not taken.

**Why parked, and the revive trigger.** The escalation is real and premise-inverting — `consoleOperator`
is *deliberately sub-root* ([console-operator/permissions.go:19-21](../../packages/console-operator/permissions.go);
the whole point of `loupe-operator-auth-lift-design.md` branch B was to move the routine Loupe login off
root onto it), it holds `InstallPackage`/`UpgradePackage`, and the install path is a plain operator
manifest upload — so a `consoleOperator` can upload a crafted package, forge a permission (or a
`holdsRole→operator` edge) the commit guards skip, and become root, erasing the sub-root boundary four
shipped designs were built to establish. **But** the escalation has a *distinct victim* only once
`consoleOperator` (or any sub-root holder of the package-lifecycle trio) is delegated to a principal not
already trusted as root. Today the sole live holder is trusted as root, so the boundary is not yet
load-bearing in practice.

> **Revive trigger:** `consoleOperator` (or any sub-root package-lifecycle holder) is delegated to a
> principal you do not already trust as root — a real multi-operator deployment, or a runtime/AI actor
> granted the lifecycle trio (e.g. AI-authored-capabilities Fire 4+ shipping a non-human lifecycle
> actor). At that point the sub-root boundary becomes load-bearing and Inc 1 ships before the delegation.

**Contract edits — reverted, ride the revive fire.** The paired edits (Contract #6 §6.1 rule 4;
Contract #8 §8.4 authority-minting admission block) are **reverted from the working tree**, not
committed: a parked design has no build fire, and committing "the Processor stamps `origin`, never trusts
the body" as present-tense contract text while the Processor still trusts the body would be a
committed-but-unimplemented **fail-open security clause** — the exact trap ratification exists to prevent.
Contract #8 §8.4 is shared with `package-restore-design.md`; its detangle happens with that item's
decision. The still-open gap stays honestly documented by §8.4's pre-existing *"Not closed by this guard:
a create of a fresh permission/role vertex…"* residual, which this design's edit had removed and which is
restored on revert.

**Pre-build gate: DISCHARGED.** The adversarial pass this design owes itself was run cold this fire and
returned four blockers, all confirmed at the source and folded — two of them fatal to the draft as
written. §16 records what they were and where each landed. No gate is left dangling for the revive fire.

Board row: *"[bootstrap] A package-plane actor can forge a package-origin permission and grant it to
itself"* (`lattice.md`, Security & trust boundary, ★★★). Filed as
`permission-role-provenance-write-once-design.md` §8(a); the row also carries the §15
`grantedBy`-revival gap, which this design closes as a consequence rather than as a second mechanism.

---

## For Andrew (one-look ratification block)

**What it does.** Today any actor holding `UpgradePackage`/`InstallPackage` — which in a live stack
means the **non-root `consoleOperator`**, and it is reached through Loupe's ordinary package endpoints —
can mint a brand-new `vtx.permission.*` vertex with a **self-declared `origin: "package"`** and any
`operationType` at all, `grantedBy`-link it to a role they already hold, and be authorized for that
operationType on their next submission. `origin: "package"` is precisely the stamp that exempts a grant
from Contract #6 §6.1's reserved-operation refusal, so the forged vertex is *more* privileged than one
minted through the sanctioned RBAC channel. This design replaces "trust the submitted stamp" with a
**server-derived** one, and admits an authority-minting mutation only under a rule the Processor can
evaluate from state it already holds.

**The rule, in one sentence.** A mutation that can change *who holds what* — derived as the projection
inputs of the two capability lenses, not hand-listed — is admitted only when the actor **holds the
corresponding minting op** (R0, leaving the RBAC surface untouched), the acting package **implements the
operationType it confers** (R1), the operation came through the **core root-grant plane** (R2), or the
actor **already holds the conferred op** (R3, no amplification) — and `origin`/`declaredBy` are stamped
by the Processor from whichever admitted it, never copied from the mutation.

**Three things grounding and the adversarial pass changed, all worth your eye.**

1. **The threat is wider than the row.** The same forgery is reachable from **any package-authored DDL
   script**, not only the three package-lifecycle ops, because `rejectPackageScopeViolations` returns
   immediately for every other operationType (`step8_commit.go:1061-1063`).
2. **There is a cheaper total escalation, and my first draft missed it.** The Processor's only
   `holdsRole` refusal lives *inside* that same lifecycle-scoped guard (`:1068-1070`), so **one forged
   `lnk.identity.<self>.holdsRole.role.<RoleOperatorID>` edge — from any script — makes the attacker an
   operator outright.** It also manufactures both branches this design relies on: it grants
   `CreatePermission`/`GrantPermission` (satisfying R0) and it puts the attacker into
   `bootstrap.SystemActorKeys`, which is **graph-discovered from exactly that link**
   (`internal/bootstrap/system_actors.go:35-65`), satisfying R2 after the next restart. This is why the
   governed set is now *derived* from the two lenses rather than enumerated by hand.
3. **A second, larger pre-existing defect fell out, and this design fixes it rather than filing it.** R2
   needs the `cap.{actorSuffix}` anchor keyspace to be core-owned. **It is not guarded at all**:
   `capability-kv` is an explicitly allowed package bucket, nothing inspects a lens's `OutputKeyPattern`,
   and a Refractor test proves a lens declaring the literal colliding pattern *installs*
   (`internal/refractor/projection/grant_change_install_test.go:62-67, 116-118`). A package lens claiming
   that pattern overwrites the kernel's own root-grant projection for every system actor. It ships in
   Increment 2 because R2 cannot be sound without it (§5.3).

**The fork (§13).** R1 admits 25 of the 31 shipped packages for a console operator. The other six-op
minority — `console-operator`, `control-authz`, `demo-operator`, `privacy-operator-grant`, all of them
*grants-only* packages that implement nothing and exist to confer other components' ops on an
operator-tier role — become **root-plane-only installs**. They already are (`make install-packages`
drives them through `lattice-pkg` as the bootstrap admin, Makefile:1102-1115), so **nothing regresses
today**. The fork is whether that is the *permanent* trust boundary: **should a non-root console operator
ever be able to install a package that confers authority it does not itself hold?** My recommendation:
**no** — and the design is built that way. The alternative branch (§13.2) keeps the door open with a
signed-manifest root of trust, which is real work and Vault-adjacent key custody.

**Contract touches (staged uncommitted in `main`, §12).** Contract #8 §8.4 gains the admission rule and
the server-derived stamp; Contract #6 §6.1's origin invariant gains the sentence that makes the stamp
authoritative rather than advisory. Both edits are the proposal — no separate amendment doc.

**Deliberately not built here** (§14): the already-accepted dead-key revival residual, and the
`grantedBy` link's own tombstone provenance (which this design makes unnecessary rather than solving).

---

## 1. Problem + intent

`grant-provenance-runtime-permission-minting-design.md` (✅ RATIFIED, Branch A, Andrew 2026-08-13) built
the **origin invariant**: a `runtime`-origin permission may never confer a core-reserved operationType, a
`package`-origin one may confer anything its package declares. The reasoning is stated at
`internal/processor/step3_auth_capability.go:472-477` — a package declaration is *"an explicit,
manifest-recorded, uninstallable deployment decision rather than authority an actor conferred on itself
in two ops."*

That sentence is a **claim about who can produce a `package` stamp**, and it is false. The stamp is a
literal in a client-built mutation body (`internal/pkgmgr/build.go:358-372`), and every server-side check
between the submitter and Core KV was built to check something else:

- the three package-lifecycle scripts validate name/version/op-vocabulary/key-shape only
  (`internal/bootstrap/install_ddl.go:197-261`);
- step 6 never resolves a DDL for `class:"permission"` — no `CanonicalName:"permission"` is registered
  anywhere, so the schema/permittedCommands/sensitive checks are skipped
  (`internal/processor/step6_resolve_ddl.go:278`; the skip is documented in `step6_validate.go`'s own
  package comment);
- `rejectPermissionRoleRewrites` (`step8_commit.go:762`) is write-**once**, so it governs `update`, not
  `create`;
- `rejectPackageScopeViolations` (`step8_commit.go:1050`) binds a batch to the keys its package owns, and
  its own doc comment names this residual verbatim: *"It does not reach a package minting a fresh
  permission of its own and granting that (the create-forgery gap, which needs server-side Definition
  content and stays open)"* (`step8_commit.go:1039-1041`).

So the last line of defence is the reserved-operation refusal — and the forged stamp is exactly what
turns that refusal off.

**Intent:** make the origin invariant true rather than aspirational, by deriving `origin` from what the
Processor can verify instead of from what the submitter asserts, and by admitting an
authority-minting mutation only on a rule that survives a hostile submitter.

## 2. The live escalation, end to end

Threat actor: an identity holding the `consoleOperator` role. It is not hypothetical — `make dev-seed`
provisions a standing one in every dev stack (`Makefile:750-795`, *"holds consoleOperator (NOT root)"*),
and Loupe's package endpoints run under it.

| # | Step | Why nothing stops it |
|---|---|---|
| 1 | Actor submits `UpgradePackage` (meta lane) naming a package it may legitimately name | `privilegedLaneAllowlist` sanctions the meta lane for the trio (`step3_auth_capability.go:426-430`); `consoleOperator` holds all three (`packages/console-operator/permissions.go:72-85`) |
| 2 | Batch `create`s `vtx.permission.<fresh>` with `{operationType:"<anything>", scope:"any", origin:"package", declaredBy:"<the named package>"}` | The upgrade script validates shape only; step 6 resolves no DDL for the class; the write-once guard governs updates |
| 3 | Same batch `create`s `lnk.permission.<fresh>.grantedBy.role.<a role the actor holds>` | Rule #5 constrains the created link's **source** (the fresh permission — created this batch, so it passes) and deliberately leaves the **target** unconstrained, because 20+ packages rely on `grantsTo:[operator]` (`step8_commit.go:1030-1036`) |
| 4 | Same batch's manifest update declares both keys | They are created in this same batch, which is exactly what `validatedManifestClaims` admits |
| 5 | `capabilityRoles` projects it (`packages/rbac-domain/lenses.go:91-103`), carrying `origin` through verbatim | The lens is a pass-through by design — enforcement is at consumption |
| 6 | Next submission: `matchPlatformPermission` matches the entry; `WouldRefuseReservedGrant` returns **false** because `runtimeOrigin("package")` is false (`step3_auth_capability.go:501-503, 520-522, 630`) | The forged stamp bought the exemption |

**Reachability, confirmed rather than assumed.** My first reading was that this had no live consumer,
because `cmd/loupe/pkg.go:438` builds the installer with `s.adminActor`, which `cmd/loupe/main.go:102`
sets to `bootstrap.BootstrapIdentityKey`. That reading is **wrong**, and the correction is load-bearing:
Loupe overrides the installer's submit hook (`inst.Submit = s.pkgmgrSubmit`, `pkg.go:440`, `:494`,
`review.go:731`), and `pkgmgrSubmit` relays through the Gateway *"under whatever operator token ctx
carries, in place of the installer's default direct-NATS submission stamping AdminActor"*
(`cmd/loupe/gatewayrelay.go:121-138`). The mechanism is decisive rather than suggestive:
`Installer.submitOp` returns at the `Submit` hook (`internal/pkgmgr/installer.go:501-506`) and never
reaches `submitDirectOp`, the only line that stamps `Actor: i.AdminActor` (`:525`) — so `AdminActor`
survives solely as `createdBy` provenance (`:41`). The Gateway then stamps the verified token subject at
`internal/gateway/gateway.go:817` (`Actor: actorID` inside `buildEnvelope`), and its request struct
carries no actor field at all. **Every Loupe package install, upgrade, uninstall, and AI-capability apply
therefore submits as the logged-in console operator.** The severity on the row is right.

*Recording the wrong first reading is deliberate: a reassuring negative — "the installer is constructed
with the admin actor, so this has no live consumer" — is exactly the claim that gets the least scrutiny,
and it would have shelved a live ★★★ escalation.*

## 3. Grounding ledger (verified live, this fire, `HEAD` = `6218550a`)

Each row cites the code that *does* the thing.

### 3.1 The two authorization planes are already distinct, and the platform already draws the line

| Fact | Citation |
|---|---|
| The **root grant** is a core-owned cypher **literal** of exactly six operationTypes — `CreateMetaVertex`, `UpdateMetaVertex`, `TombstoneMetaVertex`, `InstallPackage`, `UninstallPackage`, `UpgradePackage` — projected to `cap.{actorSuffix}` for identities holding the primordial `operator` role, and to nobody else | `internal/bootstrap/lenses.go:135-148`; anchor key pattern `:112`; `EmptyBehavior:"delete"` `:114` |
| The primordial seed grants the same six and only those to the `operator` role | `internal/bootstrap/primordial.go:729-731, 739-741, 769-790`; reseed key list `internal/bootstrap/nanoid.go:686-691` |
| Ordinary actors read a **different key** — `cap.roles.<actor>`, projected by rbac-domain's package lens | `packages/rbac-domain/lenses.go:36-62, 91-103` |
| **Which keys step 3 even reads is decided by an actor set the graph itself defines.** `ClassAwarePlatformKey` routes an actor in `systemActorKeys` to a UNION read of `[anchorKey, rolesKey]` and **every other actor to `[rolesKey]` alone** | `internal/capabilitykv/keys.go:57-79` |
| **`systemActorKeys` is graph-discovered, not a static list** — it scans Core KV for live `holdsRole → RoleOperatorID` link keys at Processor startup. So "is this actor root" reduces to "does this edge exist", which is why §5.1 shape 3 must be governed for R2 to mean anything | `internal/bootstrap/system_actors.go:35-65`; snapshot at `cmd/processor/main.go:142` |
| **The only `holdsRole` refusal in the platform is scoped to package-lifecycle ops** — it sits after `rejectPackageScopeViolations`' lifecycle bail, so every other operationType is ungoverned | `internal/processor/step8_commit.go:1061-1063` (bail), `:1068-1070` (the ban) |
| A link's relation is parsed from the **key**, never from the document's `class`, so a forged key is a real edge whatever the body claims | `internal/refractor/adjacency/store.go:124-172` |
| Step 3 already returns **which of those keys were present**, `+`-joined, and already threads it onto the decision | `internal/capabilitykv/read.go:48, 58` (`present`); `internal/processor/step3_auth_capability.go:240, 273` (`ResolvedPermission.CapKey`), `:279` (`dec.Resolved`) |
| A step-3-derived, **non-wire** trust bit already rides the envelope and is stamped once in `commit_path.go` before the OCC retry loop | `internal/processor/opwire/opwire.go:141-151` (`AuthTargetValidated bool \`json:"-"\``), `internal/processor/commit_path.go:266-273`, derivation `internal/processor/operation_context.go:46-58` |
| The Gateway stamps the **verified token subject** and structurally cannot take an actor from the request | `internal/gateway/gateway.go:476, 526, 817` (`Actor: actorID` inside `buildEnvelope`); `operationRequest` carries no actor field |
| `Installer.AdminActor` never reaches the wire when `Submit` is overridden — `submitOp` returns at the hook and never reaches the only line that stamps it | `internal/pkgmgr/installer.go:501-506` vs `:516-530` (`Actor: i.AdminActor`, `:525`); `AdminActor` is `createdBy` provenance (`:41`) |

The last row is the pattern this design extends. It is a shipped answer to exactly the question R2 asks —
*"how does step 8 learn something step 3 decided, without re-reading Capability KV and without trusting
the wire?"* — and its `json:"-"` tag is what makes the answer forgery-proof.

### 3.2 `origin` cannot carry the plane signal

`runtimeOrigin(origin) = origin != "package"` (`step3_auth_capability.go:501-503`), and its doc comment
is explicit that **absence must read as runtime** so that an unstamped or partially-migrated vertex is
*governed by* the reservation rather than exempt from it.

The root-grant entries carry **no** `origin` at all (`internal/bootstrap/lenses.go:141-147` projects a
literal list of `{operationType, scope}` pairs). So today "unstamped" happens to mean both *"root"* and
*"most restricted"*. Those are opposite roundings on one field: for the reserved-set rule, absence must
mean **more** restriction; for a plane test, absence would have to mean **less**. They agree only because
the kernel seed is unstamped, which is a fact about the corpus, not about the mechanism — stamp the
kernel seed (a plausible future hygiene fire) and the plane test silently fails **open**. *A token whose
two directions want opposite roundings is telling you they are two mechanisms.* R2 therefore gets its
own bit; it does not read `origin`.

### 3.3 The reserved set is two members, and the exemption is origin-conditioned

`reservedOperationTypes = {ShredRetentionClassKey, UpdatePermission}`
(`step3_auth_capability.go:478-481`); `WouldRefuseReservedGrant` is
`reservedOperationTypes[op] && runtimeOrigin(origin)` (`:520-522`), called from
`matchPlatformPermission` (`:630`) and reused verbatim by pkgmgr's no-escalation check
(`internal/pkgmgr/capabilitymaterializer.go:251-262`). A forged `package` stamp turns the second conjunct
off for both consumers at once.

### 3.4 There is no no-amplification rule anywhere, and root holds almost nothing

Searched and **not found**: any code comparing *"does the submitting actor already hold operationType T"*
before admitting a `permission{operationType:T}` create or a `grantedBy` link.
`packages/rbac-domain/ddls.go`'s `CreatePermission` branch takes `operationType` as a free string with no
allow-list (noted at `step3_auth_capability.go:456-457`); its `GrantPermission` branch checks only that
the permission and role vertices are alive.

The one adjacent precedent is `requesterHolds` (`internal/pkgmgr/capabilitymaterializer.go:264-267`,
consumed at `:831`), the AI-authored-capability grant artifact's no-escalation invariant. **It does not
transfer.** It gates a *proposal* that a human then reviews, and the actor whose holdings it reads is the
proposer, not an installer. Applied here it would be worse than useless: the bootstrap admin holds
**exactly six operationTypes** (§3.1), so a no-amplification rule keyed on the submitter would refuse
every legitimate install of every domain package — the admin holds none of `clinic-domain`'s eighteen
ops. This is the borrowed-predicate trap: a read-only, human-reviewed precondition is not a write
licence.

### 3.5 What a package legitimately declares — the census that decides R1

Executable, re-runnable (§10.1). Over all 31 registry packages, with
`P = {Permissions[].OperationType}`, `I = ⋃ DDLs[].PermittedCommands`, `M = {OpMetas[].OperationType}`,
`FOREIGN = P \ (I ∪ M)`:

- **25 of 31 packages have `FOREIGN = ∅`** — they declare permissions only for operationTypes they
  themselves implement.
- **4 packages carry all 54 foreign pairs**, and all four are the same structural class — *grants-only*
  packages that implement nothing (`|I| = |M| = 0` for three of them) and exist purely to confer another
  component's ops on an operator-tier role:

  | Package | Foreign ops | `GrantsTo` |
  |---|---|---|
  | `console-operator` | 29 — the package-lifecycle trio, `ShredIdentityKey`, `RevokeActor`, `UnrevokeActor`, `AttachObject`, `DetachObject`, 20 × `ctrl.*` | `consoleOperator` |
  | `control-authz` | 21 × `ctrl.*` | `control-operator` (+ 4 domain roles on five refractor verbs) |
  | `demo-operator` | 3 × `ctrl.*.read` | `demoOperator` |
  | `privacy-operator-grant` | `ShredIdentityKey` | `operator` |

- Every foreign op traces to a real implementer elsewhere — `objects-base`, `identity-domain`,
  `privacy-base`, or the kernel (`InstallPackage`/`UninstallPackage`/`UpgradePackage` are seeded by
  `internal/bootstrap/primordial.go:602-630`). The 20 `ctrl.*` verbs are implemented by no DDL at all
  (control-plane ops dispatched outside the DDL mechanism) — see §9.3, which is why R1's implementer test
  reads the **live DDL command index** rather than the package's own `PermittedCommands`.

- **All four grants-only packages are installed by the root CLI**, in dependency order, from the
  Makefile: `Makefile:1102-1115` drives `./bin/lattice-pkg install packages/control-authz`,
  `packages/privacy-operator-grant`, `console-operator`, each with `BOOTSTRAP_JSON_PATH` — i.e. as the
  bootstrap admin over direct NATS, never through Loupe. **The design's admission rule therefore
  regresses nothing that ships today.**

### 3.6 The threat is wider than the filed row — the guard must be path-independent

The row, and `permission-role-provenance-write-once-design.md` §8(a), scope the forgery to
`UpgradePackage`/`InstallPackage`'s create arm. Grounding says otherwise:
`rejectPackageScopeViolations` runs only for the three package-lifecycle classes
(`step8_commit.go:1050`, resolved by `packageLifecycleType`, `:966`), and **no other guard gates a
`create` of a `vtx.permission.*` root**. A console operator who can install a package can also ship a
DDL in it whose Starlark emits that create directly, under an operationType of their own — reaching the
same forged vertex without ever touching a package-lifecycle op.

And the widening is not only about permissions: the platform's single `holdsRole` refusal is inside that
same lifecycle-scoped guard (`step8_commit.go:1068-1070`), so a forged role binding — the cheapest total
escalation there is — is ungoverned on every other path. §5.1 shape 3.

So the admission rule is written where `rejectProtectedMutations` and `rejectPermissionRoleRewrites`
live: **path-independent, in `Commit`, evaluated for every operation**, with the package-lifecycle arm as
one admission branch rather than as the guard's scope. This is the same lesson §19's own review learned
one layer down (a guard keyed on `operationType` alone stands down for exactly the envelope that most
needs it) applied to the guard's *scope* rather than to its class resolution. The exhaustive producer
census that pins this is §10.2.

### 3.7 There is no server-side Definition registry to extend, and the obvious one is incomplete

The board row's `no-pattern:` names a *server-side package-Definition registry*. One exists —
`internal/pkgregistry` (`registry.go:1-9`, 31 packages) — but it is **not reachable from the Processor**:
`internal/pkgmgr` imports `internal/processor` in four production files (`installer.go:17`,
`upgrade.go:12`, `capabilitymaterializer.go:10`, `opmetaretirement.go:10`), so the reverse import is a
cycle, and `pkgregistry` sits above `pkgmgr`.

The cycle is breakable (`definition.go` itself imports only `encoding/json` and `fmt`), but breaking it
does not buy the property. **A registry check would refuse a live, shipped consumer:** the AI-authored
capability apply path materializes a *synthetic* Definition at apply time
(`internal/pkgmgr/capabilitymaterializer.go`, `DefinitionForCapabilityArtifact`; the plan builder at
`capabilityapply.go:162`), which by construction is not in the compiled registry. Verifying a batch
against `pkgregistry` would break every approved AI capability apply — Fires 1–4, shipped. §9.1 prices it
as a rejected alternative rather than as the design.

*This is the reflex that a design sentence of the form "just verify it against the compiled Definition"
is an unopened mechanism. Opening it is what moved the design off the row's own suggested shape.*

## 4. Reconciliation with the existing mental model

**"Didn't we already close this?"** Three guards ship on this seam and none reaches it. The protected-root
guard (Contract #8 §8.4) governs `update`/`tombstone` of `protected:true` roots — a forged permission is a
fresh, unprotected root. The permission/role write-once guard governs `update` — this is a `create`. The
package-manifest scope guard binds a batch to keys its package owns — a freshly created key is owned by
construction, and the guard's own comment names this residual (`step8_commit.go:1039-1041`). What is
missing is not *scope* but *content authority*, and the platform has never had one.

**"Does this duplicate or contradict an established pattern?"** It extends three, and contradicts none.
R2's envelope bit mirrors `AuthTargetValidated` field-for-field. The server-derived stamp mirrors what
rbac-domain's own script already does — it writes `origin:"runtime"` as a hardcoded literal
(`packages/rbac-domain/ddls.go`, `CreatePermission` branch), never from the payload; this design gives
the *package* arm the same treatment the *runtime* arm has had since day one. The authoring lint mirrors
`scripts/pkgstd/grantauthoring.go`'s default-deny-with-declared-sanction shape verbatim — including its
own comment naming this gap as out of its reach (`grantauthoring.go:34-38`).

**"Does this introduce new state?"** One per-request boolean on the envelope (`json:"-"`, never
persisted, never on the wire) and one per-entry source attribution inside the already-transient merged
capability doc. Nothing at rest, no new KV key, no new bucket, no migration of stored documents — §5.4
states the lifetime of both at every boundary the neighbouring state already honours.

**"Is a parallel design touching this seam?"** Checked, this fire: of the eleven design docs currently
`📐 awaiting-Andrew`, only `package-version-upgrade-design.md` touches the same files — and it is
`✅ ratified + SHIPPED`, and is in fact the source of the content-addressed permission-key derivation this
design leans on for its §14 audit (Contract #8 §8.1, `perm:<operationType>:<scope>`). No collision, no
consolidation needed. `permission-role-provenance-write-once-design.md` is this design's parent, and §12
below states which of its sentences this one supersedes.

**"Is the roadmap end-state different from what we're documenting?"** Yes, and §13 is that fork.
Signed package manifests (a real root of trust for arbitrary Definitions) would let a non-root operator
install an authority-conferring package safely. This design is the shape that is correct *without* key
custody; it is a narrowing, not a placeholder, and nothing in it has to be undone if signing later lands.

## 5. The shape

### 5.1 One admission rule, path-independent — and the set it governs, derived not listed

> **Banner supersedes this section on R3:** R3 is dropped (see the ratification banner). The revive fire
> builds R0 + R1 only. R3's text below is retained for the record; do not build it without a named
> consumer and a corrected scope predicate (submitter at a *covering* scope, not `scope: any`).

Grounding (§3.6, census §10.2) narrows the diagnosis past the filed row. The runtime plane is **not**
broken: minting a permission, granting it, and assigning a role are themselves operations —
`CreatePermission`, `GrantPermission`, `AssignRole` — granted to the primordial `operator` role alone
(`packages/rbac-domain/permissions.go:33-43`). That gate is correct and sufficient *for the op surface*.

What is broken is that **the mutations those ops produce can be emitted without going through them.**
`rejectPackageScopeViolations` returns immediately for any operationType outside the three
package-lifecycle names (`step8_commit.go:1061-1063`), step 6 resolves the governing DDL from the
document's own client-supplied `class` and is permissive when none resolves
(`step6_validate.go:33-37, 247-253`), the protected-root guard exempts `create`
(`step8_commit.go:728-732`), and the write-once guard skips `create` and has no link arm at all
(`:823-831`). So any package-authored Starlark DDL — one the console operator installed for its own
business op — can emit those mutations directly and be admitted.

#### The set, derived from the two capability lenses

An earlier draft listed three mutation shapes by hand. The adversarial pass (§16, finding 1) broke that
list in one move, and the lesson is that a **hand-enumerated** set is the wrong shape for this guard. The
right set is *derivable*: it is exactly the **projection inputs of the two lenses that decide
authorization**, because a mutation that cannot change what those lenses emit cannot change what anyone
is authorized to do.

- rbac-domain's `capabilityRoles` walks `(identity)-[:holdsRole]->(role)<-[:grantedBy]-(permission)` and
  reads `permission.data.{operationType,scope,lanes,origin}` (`packages/rbac-domain/lenses.go:91-103`).
- the core anchor lens matches `(identity)-[:holdsRole]->(role)` where
  `role.canonicalName.data.value = 'operator'` and grants the six kernel ops at all four lanes
  (`internal/bootstrap/lenses.go:120, 136-148`).

Union of their inputs — **the authority-minting set**, and every member is governed:

| # | Mutation shape | Why it is in the set |
|---|---|---|
| 1 | `create` of a `vtx.permission.<id>` root | the grant's content |
| 2 | `create`, or tombstoned→live `update`, of `lnk.permission.<pid>.grantedBy.role.<rid>` | binds a grant to a role |
| 3 | `create`, or tombstoned→live `update`, of `lnk.identity.<aid>.holdsRole.role.<rid>` | **binds a role to an actor — the cheapest total escalation, and the one the earlier draft missed** |
| 4 | `create` of a `vtx.role.<id>` root, or `create`/`update` of its `.canonicalName` aspect | the anchor lens keys on the canonical name; a fresh role named `operator` is root |
| 5 | `create`/`update` of a `vtx.roleindex.<id>` root | resolves a canonical name to a role for every consumer that looks one up |

Shape 3 is why the earlier draft was fatally incomplete. The Processor's only `holdsRole` refusal lives
*inside* `rejectPackageScopeViolations`, after its lifecycle bail (`step8_commit.go:1068-1070`), so for
every non-lifecycle operation there is no holdsRole guard at all — and the relation is parsed from the
**link key**, never from the document's `class` (`internal/refractor/adjacency/store.go:124-172`), so a
forged key is a real edge whatever the body claims. One create of
`lnk.identity.<self>.holdsRole.role.<RoleOperatorID>` — and `RoleOperatorID` is a compile-time constant
in this repo (`internal/bootstrap/nanoid.go:618-621`) — projects the operator's entire grant set into
`cap.roles.<self>`. Shape 4 is the same escalation without needing to know any primordial id: mint a role,
name it `operator`, link to it.

#### The admission rule

For each mutation in the set, the operation is admitted iff **one** of:

- **R0 — the actor is entitled to mint.** The submitting actor holds the corresponding minting
  operationType at `scope:"any"`: `CreatePermission` for shape 1, `GrantPermission` for 2, `AssignRole`
  for 3, `CreateRole` for 4 and 5 (or whichever op rbac-domain registers for role creation — Increment 1
  confirms the exact names against `packages/rbac-domain/ddls.go` rather than assuming them). This leaves
  the RBAC op surface exactly as it is.
- **R1 — the package implements what it confers.** Shapes 1 and 2 only, on a package-lifecycle op that
  passed `rejectPackageScopeViolations`, per §5.2. **R1 never admits shapes 3, 4 or 5** — no package
  `Definition` field produces a `holdsRole` link, and a package minting a role named `operator` is never
  legitimate.
- **R2 — the root plane.** The operation was authorized through the core anchor grant (§5.3).
- **R3 — no amplification.** The submitting actor **already holds the conferred operationType** at
  `scope:"any"`, judged by the same predicate step 3 uses. Conferring authority you already hold moves no
  privilege boundary, so this is safe by construction; it is also the invariant the AI-capability grant
  path already enforces on its proposer (`requesterHolds`,
  `internal/pkgmgr/capabilitymaterializer.go:264-267, 831`). Shapes 1 and 2 only — a `holdsRole` edge
  confers a whole role, not one op, so R3 has nothing to compare.

Otherwise: reject the whole operation with `AuthorityMintError{Key, Op, Reason, OperationType}`,
`Reason ∈ {"notEntitled", "foreignOp", "notRootPlane", "roleBinding", "reservedRoleName"}`, wire code
`ErrCodeAuthorityMint` in `internal/processor/envelope.go` + `internal/opwire`, reply mapping in
`commit_path.go` mirroring `*PackageScopeError`'s branch.

**R3 is what keeps the AI-capability grant path alive** (§16, finding 2). A `grant` artifact materializes
a Definition holding exactly one `PermissionSpec` and no DDLs
(`internal/pkgmgr/capabilitymaterializer.go:846-857`), so it confers another package's op by
construction and R1 can never admit it. Under R3 it is admitted precisely when the applying operator
already holds that op — which is the only case in which approving it was not an escalation in the first
place. Without R3 this design would have done exactly what it rejects §9.1 for doing.

**A core-owned reserved role name.** Shape 4 also needs a structural rule, not just an admission branch:
a package-lifecycle batch may never mint or rewrite a `.canonicalName` whose value is on a core-owned
reserved list, whose first member is `operator` (`Reason:"reservedRoleName"`). This mirrors the
reserved-key-pattern refusal of §5.3 and the `reservedOperationTypes` device — core owns the policy,
packages assign within it.

**Why R0 is not the trivially-satisfied branch an earlier draft made it.** My first formulation admitted
R0 whenever the mutation's own `data.origin` was absent or `"runtime"`. That is unsound twice over: a
**link** carries no `origin` at all, so every forged `grantedBy` would have passed; and the kernel-seeded
permissions carry no `origin` either — `internal/bootstrap/primordial.go:742-752` and `:753-765` stamp
`data.protected:true` on both the meta and install sets, not `origin` — so "reads as runtime" would have
licensed linking the primordial `InstallPackage` permission to a role the attacker holds. **This is
§3.2's collision biting a second time in the same design:** absence-means-runtime is a rounding chosen
for the reserved-set consumer, and it is wrong for every other consumer that borrows it. R0 therefore
asks about the **actor's entitlement**, never about a field on the mutation.

### 5.1.1 The state table for a `grantedBy` link, written before the predicate

The permission a link points at has six distinguishable states, and they do not want the same answer.
Evaluating one clause over the set is how the previous attempt broke.

| Target permission P | Admitted by | Why |
|---|---|---|
| `data.protected == true` (kernel-seeded) | **R2 only** | A kernel permission is conferred by the kernel or by root. R0/R3 must not reach it: P carries no `origin`, so any origin-shaped test reads it as runtime and hands out `InstallPackage` |
| stored `data.origin == "package"` | R1, R2 or R3 | Conferring another component's declared authority is a deployment decision, or a non-amplifying redistribution |
| stored origin runtime/absent, **not** protected | R0, R1, R2 or R3 | The ordinary runtime plane, unchanged — `GrantPermission`'s revive path (`rbac-domain/ddls.go:411-413`) lands here |
| created in this same batch | evaluate this table against the origin §5.4 **stamped**, not the one submitted | A batch that mints and grants in one step gets one consistent answer |
| stored document is **tombstoned** | the row its surviving `data.origin`/`data.protected` select | A Lattice tombstone **preserves the body** (`step8_commit.go:414-418`), so these fields are still readable and still authoritative — a retired package permission does not become a runtime one by being retired |
| **no stored document and not created in this batch** (dangling target) | **refuse** (`notEntitled`) | The default arm. A link to a permission that does not exist cannot be evaluated, and the vertex it names may be created later by anyone |

The fourth row is why §5.4's stamp is ordered **before** the link rule inside the guard. The last row is
the table's fail-closed default: the guard enumerates the states it recognizes and refuses everything
else rather than falling through to admit.

### 5.1.2 The prior document this table needs — and the read it costs

The table reads the target permission's `data.protected`, `data.origin` and `data.operationType`, and
**none of it is in the batch's `prior` cache today**: `readPriorDocuments` adds only `m.Key` for
`update`/`tombstone` mutations plus `protectedRootKey(m.Key)`, and `protectedRootKey` returns `""` for
anything not prefixed `vtx.` (`step8_commit.go:641-660`, `:1379-1385`) — so a **link** key contributes
nothing, and a `create` contributes nothing at all. For the very attack §5.1 highlights, `prior` is empty
and the guard would be blind (§16, finding 4).

The fix is small but must be stated, because the earlier draft claimed zero new reads and was wrong:
**`readPriorDocuments` gains one rule** — for every authority-minting link mutation, add the endpoint
roots the guard must judge (the permission root for shape 2; the role root and its `.canonicalName`
aspect for shapes 3 and 4). These join the **existing batched multi-get**, not a new per-link `KVGet`, so
the cost is *keys added to a read the commit path already performs*, bounded by the number of
authority-minting mutations in the batch — zero for the overwhelming majority of operations, which
contain none.

**Consistency posture, named rather than left open:** those keys enter the batch's OCC condition set with
the revision they were read at, so a concurrent tombstone or rewrite of the permission or role between
the read and the commit loses the batch rather than racing the admission decision. This is the same
posture `readPriorDocuments`' existing entries carry, and it is what makes a *read-then-admit* safe here.

### 5.2 R1 — "implements what it confers", read from the live DDL corpus, not from the manifest

The tempting form of R1 is *"the operationType appears in the acting package's own `PermittedCommands`"*.
That is a claim about a client-supplied Definition and is forgeable in one line: declare a DDL whose
`permittedCommands` names `GrantPermission`, and R1 admits a permission for rbac-domain's op.

R1 therefore reads the Processor's own in-memory DDL cache. **But it cannot read the existing
`byCommand` index**, and this is the sharpest correction grounding produced:

- `byCommand` is `map[string]string` — operationType → **canonicalName**, not the DDL's meta-vertex key
  (`internal/processor/ddl_cache.go:274`); the key needs a second hop through `Lookup(name)` →
  `MetaVertexRef.MetaVertexKey` (`:877-882`, `:28-29`).
- **An operationType claimed by more than one DDL is DROPPED from the index entirely**
  (`buildByCommand`, `:1213-1220` — `if c.count > 1 { continue }`, a deliberate fail-closed for *class
  inference*). So in `byCommand`, "ambiguous" and "unknown" are the **same observation**. An R1 written
  over it would read a shadowing attack — the attacker registers a second DDL for `GrantPermission` — as
  "unknown op, the package is introducing it", and **admit**. Fail-open, in the one state that matters.
- Only `vertexType`-kind DDLs are eligible (`commandIndexEligible`, `:1176`); an `aspectType` DDL naming
  a command is excluded, so `byCommand` also *under*-reports implementers and would refuse a legitimate
  op implemented that way.

R1 therefore needs its own derivation, built in the same pass and invalidated by the same signal: a
**claimant index** `byCommandClaimants map[string][]string` — operationType → the **meta-vertex keys** of
every DDL declaring it in `permittedCommands`, `vertexType` **and** `aspectType`, ambiguity preserved
rather than collapsed. It is one extra map over the same `byName` walk `buildByCommand` already runs
(`:1167-1210`), rebuilt by the same `Refresh` (`:489`) and `Invalidate` (`:1047`) — no new read, no new
lifetime boundary, no behaviour change to `ClassForCommand`, whose fail-closed collapse stays exactly as
it is.

With that, R1 admits the conferred `operationType` **T** iff **both**:

**(a) the acting package is one of T's implementers** — at least one claimant of T is in
`created ∪ owned`. Note *at least one*, not *all*: an earlier draft required every claimant to be owned,
and the adversarial pass falsified it against the shipped corpus (§16, finding 3). `DebitAccount` is
claimed by DDLs in **three** packages — `packages/cafe-ledger/ddls.go:114`,
`packages/loftspace-ledger/ddls.go:96`, `packages/semantic-contracts/ddls.go:314` — a fact the codebase
already documents (`packages/semantic-contracts/targets.go:65-68`: *"DebitAccount is claimed by 4
installed ledger DDLs"*). The all-claimants rule would have refused a console operator's `cafe-ledger`
install whenever `loftspace-ledger` was already installed, order-dependently, while §10.1's census —
which measures a *different* predicate — passed.

**(b) T is not a core-owned authority-minting op.** A small, explicitly enumerated, core-owned set —
`CreatePermission`, `UpdatePermission`, `GrantPermission`, `RevokePermission`, `AssignRole`,
`RevokeRole`, the package-lifecycle trio, and the meta-vertex trio — may be conferred only under R2 or
R3, never under R1. This is what closes the shadowing attack that (a) alone would admit: an attacker
shipping their own DDL declaring `permittedCommands: ["GrantPermission"]` becomes a claimant, satisfies
(a), and would otherwise mint themselves a grant for it. The set lives beside `reservedOperationTypes`
and `privilegedLaneAllowlist` and is maintained the same way — core owns the policy; a package assigns
within it.

| Claimants of T in the live corpus | R1 |
|---|---|
| none, and this batch creates a DDL declaring T | **admit** — the package is introducing T with its implementation (and T cannot be in (b)'s set, which is entirely kernel/rbac-owned and always has a live claimant) |
| ≥1 claimant ∈ `created ∪ owned`, T ∉ (b)'s set | **admit** — the package is one of T's implementers; the ledger corpus lives here |
| ≥1 claimant ∈ `created ∪ owned`, T ∈ (b)'s set | **refuse** (`foreignOp`) — the shadowing case |
| no claimant ∈ `created ∪ owned` | **refuse** (`foreignOp`) — conferring someone else's op needs R2 or R3 |
| none, and this batch creates no DDL declaring T | **refuse** (`foreignOp`) — a permission for an op nothing implements confers nothing today and everything the day someone registers it |

`created ∪ owned` is already computed by `rejectPackageScopeViolations`'s `packageScope`
(`step8_commit.go:975-995`) — R1 consumes it rather than recomputing, which is also why R1 is available
only on the package-lifecycle arm: outside it there is no manifest to establish ownership. **That guard
currently discards it**: it returns `error` only (`:1051`) and `scope` is a local (`:1075`), so
Increment 1 changes the signature to return the resolved scope alongside the error and threads it into
the new guard (§16, finding 6). A trivial change, named here so the builder does not discover it.

**Ordering note the builder must honour:** the DDL cache is invalidated *after* a successful step-8 batch
that touched `vtx.meta.>` (`ddl_cache.go:915-917`), so during the very commit that installs a package the
claimant index does **not** yet contain that batch's own DDLs. R1's first row is written against
`created` — the batch's own mutations — precisely for that reason, and must not be "simplified" into an
index lookup that would be empty at exactly the moment it is asked.

### 5.3 R2 — the root-plane bit, from a key step 3 already returns

An earlier draft proposed adding per-entry source attribution to `capabilitykv.PlatformPermission` and
threading it through `MergeDocs`. Grounding made that unnecessary: **step 3 already computes and already
threads the answer.** `ReadAndMerge` accumulates the keys that were actually present and returns them
`+`-joined (`internal/capabilitykv/read.go:48, 58`); step 3 stores that string on the resolved permission
as `CapKey` (`internal/processor/step3_auth_capability.go:240, 273`) and hangs it on the decision
(`:279`). Nothing new is needed in `internal/capabilitykv` at all.

And the question R2 asks is *"is this actor root?"*, not *"which entry matched?"* — because
`ClassAwarePlatformKey` reads the anchor key **only** for actors in `systemActorKeys`
(`internal/capabilitykv/keys.go:57-79`).

**That set is not a static allow-list, and an earlier draft claimed it was.** `bootstrap.SystemActorKeys`
is **graph-discovered at runtime** (`internal/bootstrap/system_actors.go:35-65`) by scanning Core KV for
live `lnk.identity.<id>.holdsRole.role.<RoleOperatorID>` link keys, snapshotted at Processor startup
(`cmd/processor/main.go:142`). So membership is exactly "holds the primordial operator role" — which
means R2's soundness **depends entirely on shape 3 of §5.1 being governed**. Forge one `holdsRole` edge
and the attacker joins `systemActorKeys` on the next restart, and R2 admits everything. The adversarial
pass found this (§16, finding 1) and it is the reason the mutation set is derived from the lenses rather
than hand-listed: R2 is not an independent branch sitting beside shape 3, it is *downstream* of it.

With shape 3 closed, the property holds and is structural: an ordinary actor cannot mint the edge, so it
cannot enter the set, so it cannot produce an anchor key in `CapKey`, whatever its grants say. Stated as
a dependency rather than as an independent fact, because that is what it is.

So R2 is one derived bit, mirroring `AuthTargetValidated` field-for-field: it rides the envelope as
`AuthorityMint.RootPlane` (§5.3.1), stamped in `commit_path.go` beside `AuthTargetValidated`
(`:273`) by a new `rootGrantAuthorized(resolvedPermission)` in `operation_context.go` returning true iff
`rp != nil` and `rp.CapKey` contains this actor's anchor key shape
(`capabilitykv.CapabilityKeyFromActor`). The `json:"-"` tag is what makes it unforgeable, and it is why
the shipped precedent is the right one to copy rather than a fresh mechanism.

Fail-closed in every direction: a nil `ResolvedPermission` (the `StubAuthorizer` path) is false; an
anchor doc missing through lens lag is false (the key is absent from `present`); an unrecognized key
shape is false. §10.4 pins each.

**R2's soundness rests on the anchor keyspace, and that keyspace is currently unguarded — so this design
ships the guard.** This is a confirmed finding, not a check to run: `capability-kv` is deliberately an
allowed package bucket (`internal/pkgmgr/bucketguard.go:33-42` lists it among "the shared platform-projection
buckets packages legitimately target"), **none** of `validateLensBuckets`/`validateLensAdapters`/
`validateLensReadPath` inspects `OutputKeyPattern` at all, and no lint or Processor-side check does
either. rbac-domain's `capabilityRoles` stays off the anchor key by *naming discipline alone* — its own
comment says the `cap.roles.` prefix is what "keeps the package's grant projection off the core
`cap.<actor>` key" (`packages/rbac-domain/lenses.go:22-26`) — and a Refractor test proves a lens declaring
the literal colliding pattern `cap.{actorSuffix}` **installs successfully**
(`internal/refractor/projection/grant_change_install_test.go:62-67`).

That is independently a live authorization-plane defect, larger in blast radius than the one this design
was filed for: a package lens claiming `cap.{actorSuffix}` overwrites the kernel's own root-grant
projection for every system actor. It is not filed as a follow-on row because R2 cannot be sound without
it — Increment 2 ships a **structural** refusal (a package `LensSpec` may not declare an output key
pattern that collides with a core-reserved pattern; the reserved list is core-owned, and
`cap.{actorSuffix}` is its first member), plus the fixture test that proves it fires.

### 5.3.1 R0/R3's transport — named, and corrected

R0 and R3 ask what the actor holds, and step 8 has **no** view of that: `Commit`'s envelope carries
`Actor` and `OperationType` and nothing else about entitlement
(`internal/processor/opwire/opwire.go:127-152`). Naming the channel is mandatory, and the draft's first
answer was **wrong**.

The draft read `decision.Doc`. That field is **nil on the allow path**:
`internal/processor/step3_auth_capability.go:277-285` populates it only in the `else if
entry.threadsDocOnDenial` branch, and both `step3_auth.go:43-46` and `step3_auth_trace.go:216` say so in
terms. Worse, "just populate it on allow too" is not free — `step3_auth_trace.go:220-223` keys FR23
auth-trace content on it, so allowed decisions would start emitting Plane 2/3 records they do not emit
today. *This is the assumed-transport failure in the very section written to avoid it; recording it
because the reflex clearly needs the checklist, not the memory.*

The correct channel is **inside step 3**, where `doc` is in scope and already read: `Authorize` derives
the bits at the point it builds `ResolvedPermission` (`:270-279`) and hangs them there, beside `CapKey`.
`commit_path.go` then stamps the envelope from `resolvedPermission` at the existing site (`:273`),
exactly as `authTargetValidated` does — no new field on `Decision`, no FR23 side effect, no second read.

```go
// internal/processor/opwire — mirrors AuthTargetValidated's json:"-" posture
type AuthorityMintContext struct {
    RootPlane bool            // R2: CapKey names this actor's anchor key
    HeldAny   map[string]bool // R0 + R3: operationTypes held at scope "any"
}
// OperationEnvelope gains:  AuthorityMint AuthorityMintContext `json:"-"`
```

`HeldAny` is built with the **same** predicate step 3 itself applies — entries filtered by
`WouldRefuseReservedGrant` (`step3_auth_capability.go:520-522`) and required to carry `scope:"any"`, so a
`scope:"self"` grant of a minting op cannot satisfy R0 and a runtime-origin self-mint of
`CreatePermission` cannot bootstrap it. Reusing the exported predicate is the discipline that keeps
pkgmgr's `covers` from drifting from step 3 (`capabilitymaterializer.go:255`); a second hand-written
matcher would be a second place for the reserved-set rule to rot.

**Scope of the reuse, stated honestly:** `WouldRefuseReservedGrant` is applied on the **platform** path
only — neither `matchEphemeralGrant` (`:325-367`) nor `matchServiceAccess` (`:369-410`) consults it
today. That is pre-existing and not this design's to fix (§14), but it means "the same matcher semantics
step 3 uses" is true of one of three matchers. R0/R3 read the platform path's entries and evaluate false
on the other two, which is the fail-closed direction.

### 5.4 Server-derived `origin` and `declaredBy` — the stamp becomes a verdict

Admission alone leaves the forgeable field in place for every future path. So on every admitted mutation
in the set, the Processor **overwrites** `data.origin` and `data.declaredBy` in the mutation document
before the batch is assembled, and before §5.1.1's link table is evaluated:

| Admitted by | `data.origin` | `data.declaredBy` |
|---|---|---|
| R1 | `"package"` | the package name resolved by `packageScope` — **not** the payload's `declaredBy` |
| R2 on a package-lifecycle op | `"package"` | same |
| R2 on any other op | `"runtime"` | cleared |
| R0 | `"runtime"` | cleared |

This is what makes the invariant *hold* rather than *be checked*: after it, a `package` stamp in Core KV
is a statement the Processor made, and Contract #6 §6.1's exemption rests on something real. It subsumes
the `declaredBy`-forgery half of the row without a separate rule, and it matches what rbac-domain's own
script has always done for the runtime arm (`ddls.go:323`).

Stamping applies to `create` only, so it cannot fight `rejectPermissionRoleRewrites`, and the contract's
existing carve-out — *"`data.origin`/`data.declaredBy` may be set for the first time on a permission
stored without them"* — is an `update` path and stays exactly as written.

### 5.5 What this does to the §15 `grantedBy`-revival ambiguity — closed, without a tombstone marker

`permission-role-provenance-write-once-design.md` §15 abandoned a shipped guard because
`env.OperationType` cannot separate an attacker's revival of a tombstoned `grantedBy` link from
`diffManifest`'s legitimate `!survives` re-add (`internal/pkgmgr/upgrade.go:361-384`), and concluded the
fix needed *"a provenance marker at the tombstone write site"* — new mechanism, new edge cases, no
ratified pattern.

Under this design the question dissolves, because the discriminant is no longer *what kind of tombstone
was this* but *may this actor confer this authority at all*:

- **Legitimate `!survives` re-add:** the package is re-adding a permission for an op **it implements** →
  R1 admits. The pinning case is `TestUpgrade_ReAddsRemovedEntity`
  (`internal/pkgmgr/upgrade_test.go:95-138`) — the exact test §14's attempt broke — and it is **verified
  compatible**: it re-adds a permission for `operationType "SampleOp"` (asserted at `:132`), and the
  fixture `sampleDef` declares a DDL `sampleClass` whose `PermittedCommands` is `["SampleOp"]`
  (`internal/pkgmgr/installer_test.go:301-333`). R1's second row admits it.
- **`GrantPermission` re-granting a revoked runtime grant** (`rbac-domain/ddls.go:411-413`): R0 admits —
  the actor holds `GrantPermission`, and §5.1.1's third row covers the target.
- **Root re-adding anything:** R2 admits.
- **Attacker reviving a foreign, package-origin, or kernel-protected permission's `grantedBy`:** R0
  refuses (they hold no `GrantPermission`), R1 refuses (`foreignOp`), R2 refuses (`notRootPlane`).

That is why the board row's *"owns the `grantedBy`-revival gap (§15)"* is discharged here as a
consequence rather than as a second mechanism — and it is the honest reading of §15's own closing note,
which suspected one mechanism covering creates, scoping and revival was *"the more coherent shape"*.

### 5.6 The authoring gate — the grants-only class becomes declared

A migration clears today's debt; only a gate binds the next author (Andrew, ratifying `authTargetValidated`:
*"Lint is how agents are **actually** forced to do the right thing"*). The runtime guard refuses a forged
mint; nothing yet stops a future package from quietly joining the grants-only class and being installed
by root out of habit rather than by decision.

So this design also ships a **`lint-package-standard` rule**, body in `scripts/pkgstd/`, mirroring
`grantauthoring.go` in every structural respect — a compiled-`Definition` walk via `pkgregistry` (so a
spec built by a helper closure or a loop resolves to its real `OperationType`), a
`[…-sanctioned: <code> — <prose>]` marker with a closed code vocabulary, a prose class excluding `[` so
an unterminated marker fails closed rather than borrowing a later bracket, and a fixture test that proves
the rule *fires* (`grantauthoring.go:1-12` explains why a security rule's body lives in `pkgstd` and not
in the `//go:build ignore` script):

- **Default-deny:** any `PermissionSpec` whose `OperationType` is not in the package's own
  `⋃ DDLs[].PermittedCommands ∪ {OpMetas[].OperationType}`.
- **Declared exception:** `[foreign-grant-sanctioned: <code> — <prose>]` in the spec's `Note`.
- **Code vocabulary**, each naming the reason so the sanction expires when its reason does:
  `control-plane-verb` (the 20 `ctrl.*` ops, implemented outside the DDL mechanism — §9.3),
  `kernel-lifecycle` (the package-install trio), `cross-package-operator-grant` (`ShredIdentityKey`,
  `RevokeActor`, `AttachObject` and their kin).

The migration is bounded and known exactly: **54 pairs across 4 packages** (§3.5), every one a real
decision that deserves the annotation. The gate ships **blocking**, not warn-first — the migration leaves
zero debt, and a warn-first gate over a clean tree is the fingers-crossed state the fire exists to end.
`grantauthoring.go:34-38` names this very gap as beyond its own reach; this rule is its sibling, not its
replacement.

## 6. Data model, read path, write path

Nothing is added to the graph. No new vertex type, aspect, link, lens, DDL, or operation. The design is
entirely: one commit-time admission rule, one derived stamp, three per-request envelope bits, one extra
in-memory index over the DDL cache's existing walk, one structural key-pattern refusal in the package
authoring path, and one lint gate.

- **P2 (Processor is the sole Core-KV writer):** honoured — every change is inside the Processor's own
  commit step, on mutations already flowing through it.
- **P5 (applications read lens projections):** untouched — no application read path changes; the
  capability read-model's *shape on the wire* is unchanged (the new field is `json:"-"`).
- **Write-path no-scans:** honoured — R1 reads an in-memory index and the scope set
  `rejectPackageScopeViolations` already computed; R0/R3 read bits derived inside step 3 from the
  capability document it already fetched; R2 reads a per-request bool. **No scans, and no new *round
  trip*** — but **not** "zero new KV reads", which an earlier draft claimed and which §5.1.2 falsifies:
  an authority-minting *link* mutation adds its endpoint roots to the batch's existing multi-get, because
  `readPriorDocuments` contributes nothing for a link key or a create today
  (`step8_commit.go:641-660`, `:1379-1385`). The addition is bounded by the number of authority-minting
  mutations in the batch, which is **zero for the overwhelming majority of operations**: the guard
  short-circuits on the key-shape test before any input is consulted, so an operation that mints no
  authority pays a string check and nothing more.
- **P1 (business/meta state in Core KV, operational state outside):** honoured — the three envelope bits
  are per-request operational state and are never persisted.

## 7. State-lifetime table for every new stateful mechanism

Two pieces of state are added. Neither persists; each is specified at every boundary its neighbour
already honours.

| | `OperationEnvelope.AuthorityMint` | `ddlCache.byCommandClaimants` |
|---|---|---|
| **What it is** | one per-request struct — `RootPlane` (§5.3) plus `HeldAny`, the actor's scope-`any` op set (§5.3.1) | one in-memory map, operationType → DDL meta-vertex keys (§5.2) |
| **Created** | derived inside step 3's `Authorize` where `doc` is in scope (`step3_auth_capability.go:270-279`), carried on `ResolvedPermission`, stamped onto the envelope in `commit_path.go` once, beside `AuthTargetValidated` (`:273`) | in `buildByCommand`'s existing `byName` walk (`ddl_cache.go:1167-1210`), in the same pass, from the same input |
| **Reset** | never re-derived; deliberately stamped **before** the OCC retry loop is entered, for the same reason `AuthTargetValidated` is — steps 4–8 re-execute on a retry without re-running auth | wholly rebuilt, never mutated in place: `Refresh` (`:489`) and `Invalidate` (`:1047`) each assign a fresh map, exactly as `byCommand` does |
| **Carried** | on the envelope, through every step 4–8 and every OCC retry | on the cache, read-only, for the life of that build |
| **Ordered** | set once, read once | **rebuilt AFTER the committing batch, not during it** (`:915-917`) — which is why R1's first row reads the batch's own `created` set rather than the index (§5.2's ordering note) |
| **Crash / replay** | nothing persisted; a redelivered message re-authorizes and re-stamps | nothing persisted; reconstructed from Core KV by `Refresh` at construction (`:365-367`) |
| **Upgrade / tombstone** | n/a | a tombstoned meta-vertex leaves the corpus on the next rebuild, same as for `byCommand` — no separate retraction path to get wrong |
| **Wire / forgery** | `json:"-"` — cannot arrive from a client (the shipped `AuthTargetValidated` precedent) | never crosses a boundary |
| **Absence** | false — deny | an operationType with no entry means "no live claimant", which R1's table treats as admissible **only** when the batch itself creates a claimant |
| **Stub-authorizer path** | false — R2 unavailable; §14 states the obligation | unaffected |

No change is made to `internal/capabilitykv`'s document shape. R2 reads `ResolvedPermission.CapKey`,
which already exists; R0/R3 read the capability document **inside step 3**, where it is already in scope
— never `Decision.Doc`, which is nil on the allow path (§5.3.1). The merged document itself is **not**
carried onto the envelope — only the derived struct — so it keeps the lifetime it already has
(per-request, discarded with the decision) and gains no new boundary to specify. The extra keys §5.1.2
adds to `readPriorDocuments` are not a third piece of state: they enter the existing `prior` cache, whose
lifetime is the batch, and leave with it.

## 8. Contract surface

Two frozen contracts, both **edited in `main` and left UNCOMMITTED** — the diff is the proposal.

- **Contract #8 §8.4** (`docs/contracts/08-package-install.md`) gains a fourth paragraph after the
  package-manifest scoping one, stating the admission rule (R0/R1/R2), the server-derived
  `origin`/`declaredBy` stamp, the `AuthorityMint` error code and its reasons, and replacing the current
  closing sentence — *"**Not closed by this guard:** a `create` of a fresh permission/role vertex …
  closing that needs server-side access to a package's real compiled Definition, which does not exist
  today (§19.6)"* — which this design supersedes and which must be **rewritten, not banner-annotated**.
- **Contract #6 §6.1** gains a **fourth** rule making the origin stamp authoritative: `data.origin` is
  written by the Processor at commit and never accepted from a mutation body, so the reserved-operation
  exemption rests on a server-side verdict rather than on a submitted field. **Appended as rule 4, not
  inserted as rule 3** — five live call sites cite *"Contract #6 §6.1 rule 3"* for the
  reserved-operationType refusal (`internal/processor/step3_auth_capability.go:440,611`;
  `internal/processor/step3_grant_provenance_test.go:9`; `internal/pkgmgr/capabilitymaterializer.go:246`;
  `internal/pkgmgr/grantlaundering_test.go:8`), and an inserted rule would silently invalidate every one.
  The list intro changes "Three rules" → "Four rules". *An earlier draft did insert it; a citation grep
  is what caught it, and that grep is owed by any edit to a numbered contract list.*

Neither edit changes an existing consumer's behaviour; both describe guarantees that only become true
when this design ships, which is why they are staged rather than committed ahead of the build
(*a committed-but-unimplemented clause is fail-open* — the lesson from the contract-landed-ahead-of-build
row).

**A convention worth questioning while we are here** (§3 obligation, flagged not assumed): Contract #6's
`origin` vocabulary is two-valued (`package` | `runtime`) with absence folded into `runtime`. Once the
Processor derives the value, a third value — `kernel` for the primordial seed — would let the reserved
set be reasoned about without the "absence means runtime *and* means root" collision §3.2 describes. I
have **not** built it: it is a contract-vocabulary change with a migration of stored documents, and this
design does not need it. It belongs in the fork discussion (§13.3) rather than in the build.

## 9. Alternatives considered

### 9.1 Verify the batch against the compiled `Definition` (the row's own `no-pattern:`) — rejected

Link `internal/pkgregistry` into the Processor and check each mutation against
`registry.Lookup(name)`. **Rejected on a live consumer, not on cost:** the AI-authored capability apply
path materializes a synthetic Definition that is by construction absent from the registry (§3.7), so
this refuses every approved capability apply — Fires 1–4, shipped. (An earlier draft of *this* design
then did the same thing by a different route — R1 alone cannot admit a `grant` artifact, which carries a
`PermissionSpec` and no DDLs. R3 exists because the adversarial pass caught it: §16, finding 2. Rejecting
an alternative for a flaw and then reproducing it is a failure mode worth naming.) The import cycle is a secondary
obstacle and a breakable one; the consumer is not. It is also strictly *narrower* than R1+R2 in the
direction that matters: it would admit `console-operator`'s 29 foreign grants for **any** actor holding
`UpgradePackage`, because the compiled Definition really does declare them — the check proves the batch
matches the repo, not that the submitter may deploy it.

### 9.2 Remove the package-lifecycle trio from `consoleOperator` (the demand-side fix) — rejected as a *substitute*, adopted as a *question*

The single-digit-consumer rule says a demand-side fix is mandatory to price, and here it is genuinely
cheap: delete `pkgLifecyclePermissions` from `packages/console-operator/permissions.go:72-85` and the
escalation has no actor. **It is not sufficient**, for two reasons. It is pure grant curation with
nothing binding the next author — precisely the fingers-crossed state the lint doctrine forbids — and
§3.6 shows the same forgery is reachable from any package-authored DDL script, which no grant edit
touches. It also has a real cost this time: it removes Loupe's package install/upgrade/uninstall and the
AI-capability apply button for every non-root operator, since all four run as the logged-in operator
(§2). What it *is* is the fork's other half — see §13.1, where I recommend a narrowed version of it
alongside the guard.

### 9.3 Derive R1 from the package's own `PermittedCommands` instead of the live index — rejected

One line to forge (§5.2). Reading the live command index costs nothing extra and is the only form that
survives a hostile Definition. The cost of the strict form is the 20 `ctrl.*` verbs, which are
implemented by **no DDL at all** — so under R1 they can never be admitted, and `control-authz` /
`demo-operator` / `console-operator` are root-plane installs permanently rather than incidentally. That
is the right answer (a control-plane admin verb is exactly what a non-root operator must not be able to
grant itself), and §5.6's `control-plane-verb` sanction code is where it is written down.

### 9.4 A no-amplification rule keyed on the conferred op — rejected (and why R0 is not it)

*"A package may confer T only if the submitter already holds T."* Refuses every legitimate domain-package
install *and* the whole point of RBAC: the bootstrap admin holds exactly six operationTypes (§3.1, §3.4),
none of them a domain op, and an administrator granting `CreateBooking` to a clinician has never needed
to hold `CreateBooking`. The precedent that suggests it (`requesterHolds`,
`internal/pkgmgr/capabilitymaterializer.go:264-267`) governs a human-reviewed **proposal**, and its
consumer's tolerance for being wrong is "the proposal is not offered"; ours is "a wrong grant commits" —
§3.4 spells out why the predicate does not transfer.

R0 looks superficially similar and is a different question. It asks whether the actor holds the
**authority-minting op itself** (`CreatePermission`/`GrantPermission`) — a fixed pair the platform already
reserves to `operator` — not whether it holds the op being conferred. That is the entitlement the op
surface has always encoded; R0 only makes it bind the *mutation* as well as the *verb*.

### 9.5 A provenance marker on the tombstone, per §15's option (a) — rejected as unnecessary

§15 proposed distinguishing an explicit `RevokePermission` tombstone from `UpgradePackage`'s own
`old \ new` diff tombstone, and flagged its own edge cases (documents predating the marker; whether it
survives a round-trip). §5.5 shows the discriminant was never needed: asking *may this actor confer this
op* answers both branches correctly without asking *why was this tombstoned*. Rejecting it also removes
a stored-state migration this design otherwise would have carried.

### 9.6 Signed package manifests — deferred to the fork, not rejected

The general answer: a signing key over the Definition digest, verified in the Processor, admitting any
Definition a trusted authority vouched for — including a synthetic one, if the reviewer's approval signs
it. It is the only shape that would let a non-root operator install an authority-conferring package
safely. It needs key custody (Vault-adjacent, an existing architectural fork), and it is strictly
additive to this design: R2 would gain a third admission branch and nothing else changes. **This is why
the design is a narrowing rather than a placeholder** — §13.2.

### 9.7 A stronger stance considered and taken

The interim-versus-committed check: an earlier draft admitted a package-lifecycle batch by *plane alone*
(root mints, package-plane never does), which is simpler but refuses 25 of 31 packages for a console
operator and would have made Loupe's package endpoints near-useless. R1 exists because the corpus said
the useful line is *"your own ops, yes; other people's, no"* — a committed rule, not a hedge. Conversely
the design does **not** offer a per-install override flag or an allow-list file: on the security plane a
forgeable interim that gets reworked is worse than doing it once.

## 10. Executable censuses and the checks each increment must run

Every count this design leans on ships as the command that derives it, so Phase-0 re-runs it
mechanically rather than trusting this prose.

**10.1 The census, in TWO forms, because they measure different predicates (§16, finding 3).**
*(i) The authoring-gate corpus:* `FOREIGN = P \ (I ∪ M)` per package over `pkgregistry.All()`. Expected:
31 packages, 25 with `FOREIGN = ∅`, 54 pairs across exactly
`{console-operator, control-authz, demo-operator, privacy-operator-grant}`. This pins §5.6's migration
set. *(ii) The guard's own predicate:* for each package, evaluate **R1 as specified in §5.2** against a
corpus in which every *other* package is already installed — i.e. does at least one claimant of each
declared `OperationType` land in that package's own key set, and is the op outside the (b) set. The two
answers differ for the ledger packages (`DebitAccount`, three claimants) and that difference is the
finding that broke the earlier draft. **Both ship as tests**; (ii) is the one that must gate, because a
census that passes while the guard refuses is worse than no census. *Owned by Increment 3 (i) and
Increment 1 (ii).*

**10.2 The authority-mint producer census (§3.6).** `grep -rn --include='*.go' 'vtx.permission'` and
`'grantedBy'` across the whole tree **including `_test.go`, `*.md`, and the examples**, plus a scan of
the Starlark bodies inside `packages/*/ddls.go`, classified per producer (kernel direct write / pkgmgr
batch / Starlark script / Go submitter / fixture / doc). It must answer three questions before Increment 1
hardens: does any script besides rbac-domain's `CreatePermission` branch mint a permission or a
`grantedBy` link; does any script set `origin` to the literal `"package"`; and is such a create admitted
today. *Owned by Increment 1, run at Phase 0.* A non-empty answer to the first widens R0's definition and
must correct §5.1 before code is written.

**10.3 The claimant-index build (§5.2).** *Answered this fire, and it changed the design:* `byCommand` is
`map[string]string` → canonicalName (`ddl_cache.go:274`), needs a second hop for the key (`:877-882`),
**drops** an operationType claimed by more than one DDL (`:1213-1220`) and excludes `aspectType` DDLs
(`commandIndexEligible`, `:1176`). The increment therefore *builds* `byCommandClaimants` rather than
reading `byCommand`. The pinning test is a cache built from two DDLs claiming one command, asserting the
claimant map keeps both while `byCommand` still keeps neither. *Owned by Increment 1.*

**10.4 Fail-closed vectors for the three derived bits (§5.3, §5.3.1).** Two tables.
*R0 (Increment 1):* nil `decision.Doc`; a doc granting neither minting op; a doc granting
`CreatePermission` at `scope:"self"` (must NOT satisfy R0); at `scope:"any"` (must); and a
*runtime*-origin self-minted `CreatePermission` entry, which `WouldRefuseReservedGrant` must strip before
R0 sees it — the anti-bootstrap row.
*R2 (Increment 2):* nil `ResolvedPermission`; empty `CapKey`; `CapKey` = `cap.roles.<actor>` only;
`CapKey` = `cap.<actorSuffix>+cap.roles.<actor>`; and a `CapKey` naming a **different** actor's anchor
key. Only the fourth returns true — the fifth is the one a naive `strings.Contains` gets wrong, so the
check is against this actor's own derived anchor key, never a substring match.

**10.5 The anchor-keyspace guard (§5.3).** *Answered this fire: there is no guard.* `capability-kv` is an
explicitly allowed package bucket (`bucketguard.go:33-42`), no validator inspects `OutputKeyPattern`, and
`internal/refractor/projection/grant_change_install_test.go:62-67` proves a lens declaring the literal
`cap.{actorSuffix}` pattern installs. Increment 2 therefore **ships** the structural refusal plus a
fixture test proving it fires, and adds a census test asserting no shipped `LensSpec` collides. *Owned by
Increment 2 — this is build scope, not a check.*

**10.6 The legitimate-revival pin (§5.5).** *Answered this fire:* the test re-adds a permission for
`SampleOp` (`upgrade_test.go:95-138`, asserted at `:132`) and its fixture declares a DDL whose
`PermittedCommands` is `["SampleOp"]` (`installer_test.go:301-333`), so R1's second row admits it. The
obligation that remains is mechanical: `go test ./internal/pkgmgr/... -run TestUpgrade_ReAddsRemovedEntity`
green at every increment — it is the test that broke §14's attempt, and it is the design's canary.
*Owned by Increment 1 and re-run by Increment 2.*

## 11. Test strategy

- **Unit, `internal/processor/step8_commit_test.go`:** a case table over the admission rule, mirroring
  §19.5's 36-case table shape. Rows: the six escalation steps of §2 as one end-to-end refusal; each R0/R1/R2
  admission; each R1 claimant state from §5.2's table (four rows, including the shadowing case `byCommand` collapses); the
  stamp-overwrite (a mutation asserting `origin:"package"` admitted under R2-on-a-non-lifecycle-op is
  stored as `runtime`); a mutation asserting a `declaredBy` that is not the resolved package name. Each
  refusal row asserts the `Reason` discriminant, not merely that it refused.
- **A positive vector first**, per the negative-test-false-pass rule: a legitimate `clinic-domain` install
  under a package-plane actor must be admitted, and a mutation-test must show the guard fires when R1's
  index lookup is inverted.
- **Unit, `internal/processor/step8_commit_test.go` (second table):** §5.1.1's link state
  table, one row per target-permission state — protected, package-origin, runtime, created-in-batch,
  tombstoned-body-preserved, and dangling — crossed with each admission branch. This is the table whose absence broke
  the earlier draft, so it is written first.
- **Unit, `internal/processor/operation_context_test.go`:** §10.4's fail-closed table, including
  the different-actor anchor key that defeats a prefix match.
- **Unit, `internal/pkgmgr/bucketguard_test.go`:** the anchor-keyspace refusal fires on a `LensSpec`
  declaring `cap.{actorSuffix}`, and does **not** fire on rbac-domain's real
  `cap.roles.{actorSuffix}` — plus a corpus census asserting no shipped spec collides.
- **Lint fixture test, `scripts/pkgstd/`:** the gate fires on an unsanctioned foreign grant, passes on a
  well-formed sanction, and refuses an unterminated marker and an unknown code — the three cases
  `grantauthoring_test.go` already establishes as the shape.
- **E2E, ephemeral stack:** the §2 escalation as a live refusal — submit the forged `UpgradePackage`
  batch as the dev console operator (`make dev-seed-console-operator` provisions exactly this
  identity, `Makefile:750-795`) and assert `AuthorityMint`; the §3.6 variant too (the same forgery
  from a package-authored DDL under an ordinary operationType), since that is the arm the filed row
  did not name; then assert the same actor can still install a domain package, that
  `make install-packages` (root CLI, `Makefile:1102-1115`) still installs all four grants-only
  packages, and that an `operator`-submitted `CreatePermission`/`GrantPermission` pair still works
  end to end (R0's positive vector on the live plane).
- **Unit, the shape-3/4/5 arms:** a `holdsRole` create under an ordinary operationType is refused
  (`roleBinding`) with no `AssignRole` held and admitted with it; a `vtx.role` create whose
  `.canonicalName` is `operator` is refused (`reservedRoleName`) on every branch including R1; a
  `roleindex` rewrite repointing a canonical name is refused. **The `holdsRole` case gets a mutation
  test** — invert the guard and prove the escalation lands — because it is the finding that fell out of
  a hand-enumerated set, and a regression here is total.
- **Unit, R3:** an actor holding `AttachObject` at `scope:"any"` may confer it (the AI grant-artifact
  case); at `scope:"self"` may not; a runtime self-mint of the same op does not bootstrap it.
- **E2E, the AI path:** an approved `grant` capability proposal applies through Loupe as a console
  operator who holds the conferred op, and is refused for one who does not — the §16 finding-2 pin.
- **Regression:** `TestUpgrade_ReAddsRemovedEntity` (§10.6), a `cafe-ledger` install with
  `loftspace-ledger` already present (the finding-3 pin), the full `internal/processor`,
  `internal/pkgmgr`, `internal/capabilitykv` and `internal/bypass` packages, and `make verify-kernel`.

## 12. Staging note, and what this design supersedes

> **Superseded by the ratification banner (2026-08-21): parked ⇒ contract edits REVERTED, not committed.**
> Both edits below (Contract #6 §6.1 rule 4; Contract #8 §8.4 authority-minting admission block) are
> reverted from the working tree — a parked design has no build fire, and a committed-but-unbuilt
> present-tense security clause is fail-open. They ride the revive fire. On revert, §8.4's original *"Not
> closed by this guard: a create of a fresh permission/role vertex…"* residual (which this edit had
> removed) is restored, so the still-open gap stays documented. Contract #8 §8.4 is shared with
> `package-restore-design.md`; the file's detangle happens with that item's decision.

The originally-proposed edits were — `docs/contracts/06-capability-kv.md`
(§6.1 gains rule **4**, the Processor-derived stamp, appended so rules 1–3 keep their numbers — §8) and
`docs/contracts/08-package-install.md` (§8.4 gains the authority-minting admission paragraph). The tree
was otherwise clean at fire start (`6218550a`).

**Superseded text, rewritten rather than banner-annotated.** The §8.4 edit **deletes** the sentence
*"**Not closed by this guard:** a `create` of a fresh permission/role vertex … closing that needs
server-side access to a package's real compiled Definition, which does not exist today (§19.6)"* rather
than leaving it standing under a newer paragraph — a later reader grounding in the contract must not find
the withdrawn claim still asserted. `permission-role-provenance-write-once-design.md` §8(a), §15 and
§19.6 remain as the historical record of how the gap was found; §15's two proposed fixes (a tombstone
provenance marker; folding into manifest scoping) are both superseded by §5.5 here, and its framing of the
gap as confined to `UpgradePackage`'s create arm is corrected by §3.6. The board row is likewise renamed
to the corrected scope.

## 13. The fork for Andrew — RESOLVED: Branch A (Andrew, 2026-08-21)

**Resolution:** Branch A. A non-root console operator may install a package that confers only
operationTypes the package itself implements (R1), never a core-owned authority-minting op. "My own ops,
yes; other people's, no" is the permanent line. Branch B (withdraw the trio from `consoleOperator`) and
Branch C (signed manifests) are not taken. The original fork text follows for the record.

### 13.1 The question

**Should a non-root console operator be able to install a package that confers authority it does not
itself hold?**

This is not a mechanism question — R0, R1 and R2 are settled and I am not asking about them. It is the trust
boundary: today the answer is *yes, silently*, and it is what makes the escalation reachable. Three
branches:

| | Branch | What a console operator can install | Cost |
|---|---|---|---|
| **A** *(recommended)* | Ship R1+R2 as designed | The 25 packages that implement what they confer; **not** the 4 grants-only ones | Nothing regresses today (§3.5); a future grants-only package needs a root install |
| **B** | Ship R1+R2 **and** withdraw the trio from `consoleOperator` | Nothing — package lifecycle becomes root-only | Loupe loses package install/upgrade/uninstall and AI-capability apply for non-root operators (§2) |
| **C** | Ship R1+R2 now, and later add signed manifests as a third admission branch | Eventually anything a trusted authority signed | Key custody — a separate architectural fork (§9.6) |

**My recommendation is A**, with C as the roadmap and **B explicitly not taken**. A closes the escalation
completely while leaving the console operator the capability it actually uses. B is strictly safer but
pays a real product cost for a threat A already closes, and it would make Loupe's shipped package
endpoints dead surface. If your instinct is that package lifecycle should simply be root's, B is one line
and this design still ships underneath it — the guard is what stops the grant from being quietly
re-added later.

### 13.2 What ratifying A commits us to

That **"my own ops, yes; other people's, no"** is the permanent line for non-root package installs, and
that widening it later goes through signing (C) rather than through a new exemption. Concretely: a future
package that wants to be installable by a console operator must implement what it grants.

### 13.3 A smaller contract question, if you want it decided now

Whether Contract #6's `origin` vocabulary should gain a third value, `kernel`, for the primordial seed
(§8). Not needed by this design and not built; it would remove a real collision, at the price of a stored-
document migration. Happy to leave it filed.

## 14. Residuals — named, not built

- **The dead-key revival residual** (`permission-role-provenance-write-once-design.md` §19.6) is
  unchanged: a package can still declare-and-revive an already-dead, non-protected key some other package
  once owned. It needs per-key ownership provenance, which does not exist. This design neither widens nor
  narrows it.
- **A root-plane actor can still forge.** R2 admits anything the bootstrap `operator` submits. That is
  not an escalation — the operator holds `CreateMetaVertex` and can already author anything — but it
  means the guard is a *containment* boundary for non-root actors, not an integrity check on root. Say so
  in the contract wording rather than implying more.
- **Stub-authorizer mode** yields R2 = false (nil `ResolvedPermission`), so a stack running
  `StubAuthorizer` could not install the grants-only packages. Increment 2's Phase 0 must establish
  whether any supported path still runs stub mode and, if so, what the honest answer is. Recorded as an
  **obligation, not a prediction** — my expectation is that capability mode is the shipped default and
  stub is dev-only, and that expectation is exactly the kind of reassuring negative §2 warns about, so it
  gets grounded rather than assumed.
- **The unguarded anchor keyspace is fixed here, not filed** (§5.3) — but note what it means for
  everything *already* shipped: `capability-kv` has been an open package bucket with no key-pattern
  guard for the whole life of the platform, and the only thing keeping rbac-domain off the anchor key is
  its own choice of prefix. Increment 2's corpus census is what turns "no package has done it" from a
  hope into a pinned fact.
- **The stamp is not retroactive, and the pre-existing population is the one it cannot reach.** §5.4
  stamps `origin` at **create**, so every `vtx.permission.*` already in Core KV keeps whatever stamp it
  was committed with — and `rejectPermissionRoleRewrites` makes those fields write-once, so nothing can
  correct one afterwards. "Set it once and everything inherits" is *everything from now on*. The
  pre-existing set is believed clean — census §10.2 found `internal/pkgmgr/build.go:371` to be the only
  producer of a `package` stamp anywhere, and no Starlark script sets one — but *believed clean* is a
  claim, so **Increment 1 ships a one-shot audit** (a read-only sweep of every live `vtx.permission.*`,
  asserting each `package`-origin vertex's key equals `PackageEntityNanoID(declaredBy, perm:<op>:<scope>)`
  — the content-addressed derivation of Contract #8 §8.1, which a forged vertex minted under a different
  package name cannot satisfy). If it comes back non-empty on a live stack, that is a finding for Andrew,
  not something the guard silently inherits.
- **The reserved-set rule is platform-path only.** Neither `matchEphemeralGrant`
  (`step3_auth_capability.go:325-367`) nor `matchServiceAccess` (`:369-410`) applies
  `WouldRefuseReservedGrant`. Pre-existing, not created here, and R0/R3 evaluate false on both paths
  (the fail-closed direction) — but it means §5.3.1's "the same matcher semantics step 3 uses" holds for
  one of three matchers. Worth its own row if it is not already carried by the auth-plane work.
- **The 20 `ctrl.*` verbs have no DDL**, so §9.3's consequence is permanent under R1. If control-plane
  ops later gain op-metas, `control-plane-verb` sanctions expire and the gate reds — which is the
  designed behaviour of the sanction vocabulary.

## 15. Decomposition for the Steward

> **Superseded by the ratification banner (2026-08-21): Increment 1 only; R3 dropped; Inc 2 folds into
> the ★★★ three-admission-holes row; Inc 3 deferred.** The revive fire builds **Inc 1 with R0 + R1**
> (not R3). Increments 2 and 3 as written below are retained for reference, not for scheduling.

**Increment 1 — the admission rule, the claimant index, and the derived stamp (path-independent).**
Phase 0 re-runs §10.2 (the producer census, tests and docs included) and §10.1(ii), and confirms the
exact rbac-domain op names R0 keys on (`CreatePermission`/`GrantPermission`/`AssignRole`/role-creation)
against `packages/rbac-domain/ddls.go` rather than assuming them. Builds `byCommandClaimants` (§5.2,
§10.3) and the `readPriorDocuments` endpoint rule (§5.1.2); changes `rejectPackageScopeViolations` to
return its resolved scope (§5.2); derives `AuthorityMintContext` inside step 3 (§5.3.1) with `RootPlane`
left false. Ships `rejectUnauthorizedAuthorityMint` over **all five mutation shapes** with **R0 and R1**
(R3 dropped — banner), the `AuthorityMintError` type, the wire code, the reply mapping, §5.4's stamp,
§5.1.1's state table, and the reserved-role-name refusal.

*Note the earlier draft predicted rbac-domain's own first install would break under R0-only. It does
not — R1's first row admits it, since every rbac-domain permission's only claimant is a DDL created in
that same batch, and every later root install passes R0 because the bootstrap admin reads
`CreatePermission`/`GrantPermission` out of `cap.roles.<admin>` through the union read
(`internal/capabilitykv/keys.go:71-78`). The adversarial pass falsified the hazard (§16, finding 7); the
increments do not need to merge.*

Owns §10.1(ii), §10.2, §10.3, §10.6, the §14 pre-existing-population audit, and the §11 step-8 tables.

**Increment 2 — R2 and the anchor keyspace.** Ships `AuthorityMintContext.RootPlane` and its derivation, wires
R2 into the rule, and ships the **structural core-reserved-key-pattern refusal** in
`internal/pkgmgr/bucketguard.go` with its fixture test and corpus census (§5.3, §10.5) — build scope, not
a check, because §10.5 came back negative. Phase 0 settles the stub-authorizer question (§14). Owns
§10.4, §10.5, the `bucketguard` tests, and the E2E.

**Increment 3 — the authoring gate.** Ships the `pkgstd` rule body, its fixture test (fires / passes on a
well-formed sanction / refuses an unterminated marker / refuses an unknown code), the
`lint-package-standard` wiring, and the 54-pair sanction migration across the four grants-only packages.
Owns §10.1 as a committed test. Blocking from its first commit.

## 16. Adversarial pass — run, and what it changed

Run this fire, cold, against the finished draft, briefed to break it rather than to check it (the
Designer-lane obligation: a design that flags its own gate must discharge it before it is build-ready).
It returned **four blockers, all real, all confirmed at the source before folding**. Two were fatal to
the design as written. Recorded here because the findings are the design's reasoning, not a changelog.

| # | Finding | Where it landed |
|---|---|---|
| 1 | **The mutation set was incomplete: a `holdsRole` create is a cheaper total escalation.** The platform's only `holdsRole` refusal sits inside `rejectPackageScopeViolations` after its lifecycle bail (`step8_commit.go:1061-1063, 1068-1070`), so every non-lifecycle op is ungoverned. One forged edge to `RoleOperatorID` (a repo constant) projects the whole operator grant set — **and manufactures both R0 and R2**, since `SystemActorKeys` is graph-discovered from exactly that link (`internal/bootstrap/system_actors.go:35-65`) | §5.1 — the set is now **derived** from the two capability lenses' projection inputs (5 shapes) instead of hand-listed; §5.3 restates R2 as *downstream of* shape 3, not independent of it; §3.1 gains the corrected rows |
| 2 | **R1 killed the shipped AI-authored `grant` apply.** A grant artifact materializes a Definition with one `PermissionSpec` and no DDLs (`capabilitymaterializer.go:846-857`), so R1 can never admit it — the design reproduced the exact flaw it rejects §9.1 for | **R3 (no amplification)** added: the actor already holds the conferred op at `scope:"any"`, the same invariant `requesterHolds` enforces on the proposer. Admits the AI path precisely when it was not an escalation |
| 3 | **§3.5's census measured a different predicate than R1.** `DebitAccount` has **three** claimant packages (`cafe-ledger`, `loftspace-ledger`, `semantic-contracts` — a fact `packages/semantic-contracts/targets.go:65-68` already documents), so the all-claimants-owned rule refused a console operator's ledger install order-dependently, while §10.1's census passed | §5.2 R1 relaxed to **≥1 claimant owned**, with a core-owned **authority-minting op set** as the conjunct that closes the shadowing attack the relaxation would otherwise open; §10.1 now ships **two** censuses, and the one that gates measures the guard's own predicate |
| 4 | **§5.1.1 read a document the commit path never fetches.** `readPriorDocuments` contributes nothing for a link key or a create (`step8_commit.go:641-660`, `:1379-1385`), so the row protecting kernel permissions was unimplementable | §5.1.2 added: the endpoint roots join the **existing batched multi-get** with a stated OCC posture; §6's "zero new KV reads" claim corrected — it was false |
| 5 | `decision.Doc` is **nil on the allow path** (`step3_auth_capability.go:277-285`), and populating it would flip FR23 auth-trace content for allowed decisions | §5.3.1 rewritten: the bits are derived **inside step 3**, where `doc` is already in scope, and ride `ResolvedPermission` |
| 6 | `rejectPackageScopeViolations` discards `packageScope` (returns `error` only) | §5.2 names the signature change; Increment 1 owns it |
| 7 | Increment 1's "known hazard" (rbac-domain's first install) was a **false alarm** | §15 corrected; the increments no longer need to merge |
| 8 | The reserved-set rule is platform-path only — the ephemeral and service matchers never apply it | §14, as an adjacent pre-existing note |

**What the pass says about how this design was built.** Findings 2, 4 and 5 are all the *same* reflex
failing — asserting that a mechanism can be reused, bent, or read without opening it — and finding 5
landed in the section written specifically to avoid it. Finding 1 is the checkable form of the same
thing: a hand-enumerated set is an assertion that the enumeration is complete. The structural repair, not
just the local one, is that the governed set is now derived from the lenses that decide authorization, so
a future shape that reaches those lenses is in the set by construction rather than by someone
remembering it.

Two premises the pass tried to break and could not: kernel permissions carry `data.protected:true` on
**both** the meta and install sets (`internal/bootstrap/primordial.go:742-752, 753-765`), so §5.1.1's
first row is sound; and §5.4's stamp cannot fight `rejectPermissionRoleRewrites`, which runs first
(`step8_commit.go:265`) and skips `create` outright (`:823-831`).

- **(2026-09-04, from the stored-class write gate)** The denial direction on `holdsRole`/`grantedBy` stays open by DDL on purpose (that design §2.4): eight ops across seven packages create `holdsRole` links, the create side is this design's R0/R1, and closing one direction alone would be dead scaffolding — both directions close together on this design's revive trigger (`consoleOperator` delegated below root). The twelve kernel-seeded links are protected by exact key regardless.
