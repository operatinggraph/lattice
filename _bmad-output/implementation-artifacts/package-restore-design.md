# `RestorePackage` — undoing an uninstall

**Status: 📐 awaiting-Andrew (ratification).** Carries a **frozen-contract change** — Contract #8 (a new
**§8.5**, plus one pointer in §8.2 and one scope paragraph in §8.4) and Contract #3 §3.2/§3.3 (a fourth
mutation verb, **and two corrections to text that already contradicts shipped behaviour**) — staged
**uncommitted** in `main`. No architectural fork.

Owns board row *[Pkgmgr] An uninstalled package cannot be reinstalled — only refused*
(`backlog/lattice.md`), filed by [reinstall-over-uninstall-occupancy-design.md](reinstall-over-uninstall-occupancy-design.md)
§5 as `📐 needs designer pass · no-pattern: package-revive ownership scope for a tombstoned manifest`.

---

## For Andrew

**What it does, in two lines.** `UninstallPackage` is today a one-way door: every declared key is
tombstoned, and no path — install, upgrade, apply — can bring the package back. `RestorePackage` is its
exact inverse: the same payload shape, the same per-key OCC, the same kernel-script structure, and one new
mutation verb (`revive`) whose semantics mirror `tombstone`'s — carry the stored body forward, flip the
liveness flag. It restores the package to the state the uninstall left it in, at the version it was
uninstalled at; a subsequent `upgrade` moves it forward.

**No architectural fork.** The one shape question — reinstall-as-upgrade vs. a distinct verb — is resolved
in §6 on grounded mechanics, not forwarded: routing a reinstall through `UpgradePackage` requires bending
two guards whose current behaviour is correct for their own consumers, and would make *every* upgrade of an
uninstalled package a silent resurrection. A distinct verb leaves `InstallPackage`, `UpgradePackage` and
`UninstallPackage` byte-identical in behaviour.

**Frozen-contract changes, staged uncommitted in `main`** (§8):

| Contract | § | Change | Affected consumers |
|---|---|---|---|
| #8 Package install | **8.2** | one sentence pointing at §8.5's mirror property — install is create-only, restore is revive-only | doc only; no code enforces the guardrail table itself |
| #8 | **8.4** | one paragraph, *Restore scope*: a restore's owned surface is its own tombstoned manifest's `declaredKeys` ∩ revived-by-this-batch ∩ currently-tombstoned ∩ tombstoned-by-that-uninstall | `resolvePackageScope` (`internal/processor/step8_commit.go:1141`) |
| #8 | **§8.5 (new)** | the `RestorePackage` op — payload, guardrails, scope, authority, what it does and does not restore. **§8.5 was unused; nothing is renumbered** | new |
| #3 Mutations | **3.2 / 3.3** | a fourth verb, `revive` — admitted **only** for `RestorePackage` | every verb switch in `internal/processor` (§11 C1) |
| #3 | **3.3** (two corrections) | *"There is no separate `restore` op"* and *"Tombstones are permanent; keys are not reused"* both **already contradict shipped, ratified behaviour** — `UpgradePackage`'s revive arm and §8.4 rule (3). Corrected in the same diff | anyone grounding a design in §3.3, which is how this was found |

**The adversarial gate this design set for itself has been run** (§16): two cold reviewers, disjoint lenses,
**4 BLOCKING and 6 SERIOUS findings, all folded**. Two of them would have shipped a verb that cannot work
(the restore could never own its own manifest aspect, so the batch refused itself) and a containment claim
that was bypassable (keyed on `operationType`, which the executing script is not bound to). The design you are
reading is the corrected one; §16 records what changed and why, because the *pattern* in those findings —
precedent cited but not re-read — is worth more than the individual fixes.

> **The `docs/contracts/08-package-install.md` diff currently carries TWO independent proposals.** The
> *Authority-minting admission* subsection is
> [package-authority-minting-provenance-design.md](package-authority-minting-provenance-design.md)'s, staged
> before this fire. **Mine are the §8.2 table row, the §8.4 restore-scope paragraph, and the new §8.7** —
> each begins with a pointer to *this* doc by name, so the two are separable by hunk.

**One ordering constraint you should know about, and it is a real dependency, not a preference (§9).**
The authority-minting design's governed set includes "a `create`, or **tombstoned→live `update`**, of a
`lnk.permission.<pid>.grantedBy.role.<rid>` link". A restore emits exactly that shape for every permission
the package declares. Whichever of the two ships second must account for the first. My recommendation, with
the reasoning in §9: **authority-minting ratifies and ships first**, and its governed set and its
core-owned authority-minting op list both name `RestorePackage` — otherwise a new authority-reviving verb
exists un-governed for the length of that window.

---

## 1. What is true today (ground-truthed)

[reinstall-over-uninstall-occupancy-design.md](reinstall-over-uninstall-occupancy-design.md) §1 established
the mechanics of the failure and shipped the occupancy gate that made it loud (`aca2120` + `00a4a73`). This
section does not re-derive that; it establishes what stands between a dead package and a live one.

**1.1 An uninstall preserves everything and destroys only liveness.** `buildMutationValue`'s tombstone
branch seeds the written value from the whole stored document and changes only `isDeleted` and the
`lastModified*` triplet (`internal/processor/step8_commit.go:523-553`); its own doc comment says the
tombstone "keeps the provenance a later revive needs" (`:513-515`). Nothing hard-deletes a Core-KV entity
key: the only `KVDelete` against the core bucket is the outbox consumer's, on `vtx.op.<requestId>.events`
(`internal/processor/outbox/consumer.go:111`), which no package declares. **Every byte a restore needs is
still sitting on the key.**

**1.2 Reviving a key is a first-class, already-ratified concept — three times over.**

- `diffManifest`'s update arm revives a package entity key a prior version dropped
  (`internal/pkgmgr/upgrade.go:458-484`).
- rbac-domain's `grant_link` helper is explicitly three-state: alive → no-op, **tombstoned → revive**,
  absent → create (`packages/rbac-domain/ddls.go:164-178`), used by both `AssignRole` (`:370-376`) and
  `GrantPermission` (`:409-417`).
- Contract #8 §8.4 rule (3) already carves out "a key whose stored document is already tombstoned **and** is
  also an update/tombstone target elsewhere in the same batch", implemented as `validatedManifestClaims`
  (`step8_commit.go:1226-1253`) on the stated ground that **"a dead key has no live owner to displace"**
  (`:1215-1218`).

**1.3 Two walls stand between an uninstalled package and any of that.**

- **W1 — the package-scope guard refuses a tombstoned manifest.** `resolvePackageScope` returns an
  unresolved scope (owning nothing) the moment `docIsTombstoned(manifest.Doc)` is true
  (`step8_commit.go:1189-1191`), and the ownership loop then rejects every non-`create` mutation as
  `unscoped` (`:1082-1089`). The guard's stated objection is concrete: the dead manifest's `declaredKeys`
  "still list the retention-class holder keys uninstall deliberately leaves live and undeclared — so
  honouring it would hand a dead package's stale set to a live batch" (`:1185-1188`).
- **W2 — `diffManifest` refuses to revive the authority topology.** A key present in both the old and new
  key sets whose committed body is already tombstoned is counted `revocationsRespected` and left dead —
  scoped to `vtx.permission.*`, `vtx.role.*`, `lnk.*.grantedBy.role.*`, `lnk.*.holdsRole.role.*`, excluding
  `vtx.meta.*` (`internal/pkgmgr/upgrade.go:487-517`). For a reinstall-over-uninstall **every** key is in
  both sets, so this arm decides the whole authority half of the package.

**1.4 Three guards that turn out NOT to be walls, and one of them is the design's safety floor.**

- `rejectPermissionRoleRewrites` makes a permission's provenance fields, a role root's whole body, its
  `.canonicalName`, and a roleindex root write-once (`step8_commit.go:824-...`). It is **not** a wall:
  `isDeleted` is excluded from the comparison by name and with a stated reason — "it is the entity's
  liveness flag, which the tombstone path owns" (`:869-871`) — and a mutation carrying **no document** is
  skipped outright (`:828-830`, the bare-tombstone carve-out). A revive that supplies no body is therefore
  admitted, and *any* revive that also rewrote the body would be refused. That asymmetry is exactly the
  guarantee this design wants, and §5.2 is built to sit inside it rather than beside it.
- The blanket `holdsRole` refusal (`:1067-1071`) rejects any `holdsRole` mutation in a package-lifecycle
  batch. No package `Definition` field produces a `holdsRole` link, so a restore never emits one and the
  refusal stays untouched and unweakened.
- `rejectProtectedMutations` KVGets each update/tombstone's 3-segment root and refuses on
  `data.protected == true`. Package-declared roots are not protected, so a restore passes it — **but that it
  runs at all is an increment obligation, not an inherited property, and the first draft stated it as the
  latter** (§16, S3). Its test is `if m.Op != "update" && m.Op != "tombstone" { continue }`
  (`step8_commit.go:730`) — an `!=` predicate that silently skips an unlisted verb. §14 carries the structural
  answer: one shared predicate, not four hand-edited conjuncts.

**1.5 Every entity key is package-namespaced by construction — with exactly one exception.**
`entityNanoID(def.Name, tag)` folds the package name into the NanoID
(`internal/pkgmgr/installer.go:430-445`; pinned by `version_independent_keys_test.go`), so no package can
compute another's entity key. The one exception is
`vtx.roleindex.<sha256NanoID("rolecanonical:"+CanonicalName)>` (`internal/pkgmgr/build.go:85-87`), whose
derivation omits the package name; `sha256NanoID` is called exactly once in the whole package. Four packages
declare roles — `console-operator`, `control-authz`, `demo-operator`, `identity-domain` — with five distinct
canonical names, so the shipped corpus has no collision on it (§11 ships the census as a command).

**1.6 The shipped refusal names a remedy that cannot restore a package.** The occupancy gate's tombstoned
bucket advises `CreatePermission` + `GrantPermission`, qualified by three states in which even that is
partial (`installer.go:1080-1138`). Those two verbs restore **grants**. They restore no DDL, no lens, no
op-meta, no weaver target, no pane, no role, and no roleindex — and `CreatePermission` mints a *fresh
random* NanoID (`packages/rbac-domain/ddls.go:306-328`), never the deterministic key the package's own
permission occupied. So today the platform's own refusal text is the closest thing to a restore path, and it
does not restore the package.

---

## 2. The demand, priced honestly

**The census is small and I am not going to inflate it.** No Makefile target and no CI workflow ever invokes
`lattice-pkg uninstall`; every install-family target re-installs a *live* package and short-circuits at step
2. The live callers of `Uninstall` are exactly two: `cmd/lattice-pkg/main.go:372` (`lattice-pkg uninstall
<name>`) and `cmd/loupe/pkg.go:520` (`POST /api/packages/uninstall`, wired to a button in the operator
console).

That is the whole demand, and it is still enough, for one reason: **the platform ships an uninstall button
in its operator console and has no undo for it.** The recovery paths available today are

1. `make down` — force-wipes the whole stack, i.e. every other package and all business data; catastrophic
   on a demo or production box, and the very command whose destructiveness `internal/bootstrap/nanoid.go`'s
   version gate already sends operators to reluctantly;
2. re-declare the package under a **different name** — which re-keys every entity (§1.5), orphaning the
   original keys forever and disconnecting the new entities from all existing business data;
3. nothing.

An operator console that offers a destructive verb with no inverse is not a complete operator console. That
is the demand: not a count of callers, but the fact that **one of the platform's four package-lifecycle
verbs is irreversible by construction**, and the one it is irreversible *for* is the one an operator can
trigger from a web page.

**The cheap alternative is priced in §6.3 and rejected there**, not here.

---

## 3. Reconciliation with the existing mental model

*Didn't we already handle this?* Partly. The occupancy gate (shipped) made the failure **loud** — an install
over an uninstalled package now refuses by name instead of reporting `install committed` and committing
nothing. It deliberately scoped restoration out (§2 of that doc) and filed it. This is that item.

*Doesn't the platform already refuse to un-tombstone things, on purpose?* **Yes, and this is the most
important thing to reconcile.** `internal/bootstrap/reconcile.go:105-112` states it flatly: a stored document
carrying a soft tombstone is never rewritten, because "Rewriting one would silently restore a revoked grant —
turning a boot into an unlogged privilege escalation." That is the platform's answer to the identical
question in the kernel plane, and this design does not weaken it. It is preserved by *three* properties
reconcile's own path lacks, all of which this design supplies:

| reconcile's un-tombstone | `RestorePackage` |
|---|---|
| would run **automatically**, on every boot | runs only when an operator submits the op |
| has **no operator intent** behind it | is the operator's intent, and nothing else triggers it |
| would be **unlogged** — no op, no event, no actor | is an op with an actor, a tracker vertex, a `package.restored` event, and a preflight that enumerates every authority it is about to revive **before** it is submitted |
| cannot tell a revoked grant from a stale body | refuses any key whose tombstone was not written by the uninstall itself (§5.3) |

The invariant reconcile protects is *"a revoked grant is never **silently** resurrected."* A restore's
resurrection is neither silent nor automatic, and §5.3 narrows it further to what the uninstall itself
killed.

*Does this duplicate an established pattern?* No — it completes one. `UninstallPackage` and `RestorePackage`
are a symmetric verb pair with the identical payload shape, the identical per-key OCC discipline, and kernel
scripts that differ in one string. The nearest precedent for the *shape* of a symmetric operator-verb pair
over a reversible state change is `RevokeActor` / `UnrevokeActor`
(`packages/identity-domain/revocation.go:20-113`).

*Does this introduce new state?* **No.** No registry, no cache, no marker, no TTL, no new field on any
document. Everything §5 decides is read from data Contract #1 §1.3 already stamps on every write. §10 is the
state-lifetime table, and it is empty by construction — which is the point.

---

## 4. The shape, in one paragraph

`RestorePackage { name, declaredKeys: [{key, expectedRevision}, …] }` — byte-identical in shape to
`UninstallPackage`'s payload. Its kernel DDL is `UninstallPackageDDLScript` with `"tombstone"` replaced by
`"revive"`. The `revive` verb is the mirror of `tombstone` in `buildMutationValue`: seed the written value
from the whole stored document, set `isDeleted = false`, stamp the `lastModified*` triplet, preserve the
creation triplet. **The client supplies no document at all** — which is what makes `RestorePackage` provably
a *restore* rather than a rewrite (§5.2). `resolvePackageScope` gains one branch: for `lifecycle ==
"RestorePackage"` a tombstoned manifest resolves, and the owned surface is exactly the keys this batch
revives that the manifest declared, that are currently tombstoned, and whose tombstone the uninstall itself
wrote (§5.3).

---

## 5. The mechanism

### 5.1 The op, its script, its permission

`RestorePackage` joins the package-lifecycle family:

- **Kernel DDL** `RestorePackageDDLScript` in `internal/bootstrap/install_ddl.go`, structurally identical to
  `UninstallPackageDDLScript` (`:127-177`): the shared `installGuardrailHelpers` prelude, the same
  `declaredKeys` entry parsing (string or `{key, expectedRevision}`), the same key-shape guardrail, the same
  integer check on `expectedRevision` — emitting `{"op": "revive", "key": …}` instead of `tombstone`, and a
  `package.restored` event carrying `{name, keyCount}`. Self-description constants mirror the uninstall
  trio's (`:275-290`).
- **Primordial permission** `PermRestorePackageKey` + its `grantedBy` link to `RoleOperatorID`. Kernel-seeded,
  so it survives the uninstall of *any* package — including rbac-domain, whose uninstall is precisely the case
  the occupancy gate's current remedy cannot help with (§1.6).
- **`isPackageLifecycleOp`** (`step8_commit.go:932-938`) gains the fourth name, so every path-independent
  package guard — `holdsRole` refusal, forged-manifest-create refusal, scope resolution — covers it from the
  first commit.

**The kernel-seeding checklist — every site the existing three touch (§16, F2).** The first draft named only
the script file and the key-constant enumeration and would have shipped an op that is never seeded and never
dispatchable. `internal/bootstrap/nanoid.go:259-310`'s own version history records this exact list from the
last time it was walked — entry "12", *"UpgradePackage primordial DDL + its operator permission added"*:

| site | what a fourth op needs |
|---|---|
| `bootstrap/primordial.go:614-628` | a fourth `seedPackageInstallDDL(add, RestorePackageDDLKey, …)` — the call that actually writes the meta-vertex + its 9 aspects (`:875-915`). **This is the file the first draft never named.** |
| `bootstrap/primordial.go:742-746` | a fourth `installPerms` entry — feeds two loops, one seeding the permission vertex (`:762-773`), one the `grantedBy` link to operator (`:786-796`) |
| `bootstrap/install_ddl.go` | the script + `restorePackageInputSchema` / `outputSchema` / `fieldDescription` / `examples` constants (the uninstall trio's live at `:275-290`) |
| `bootstrap/nanoid.go` | `RestorePackageDDLID/Key`, `PermRestorePackageID/Key`; **two new `PrimordialIDsRaw` JSON fields**, the `targets` pointers (`:495-498`), the required-field check (`:540-543`), the `PrimordialVertexKeys()` entries (`:673`, `:684`, `:691`) |
| `bootstrap/nanoid.go:718` | `PrimordialVertexKeyCount` **37 → 40** (DDL meta-vertex + permission + grant link) |
| `bootstrap/nanoid.go:461-470` | `checkVersion` **"16" → "17"** — see the operational consequence below |
| `bootstrap/service_actor_test.go:238` | the magic number in `TestPrimordialVertexKeyCount_AgreesWithEnumeration` |
| `scripts/verify-kernel.go:105-111` | the KERNEL KEY COUNT DRIFT gate — red until the constant and the enumeration move together |
| `bootstrap/primordial.go:618` | **`UninstallPackage`'s compensation-inverse hint currently names itself**, which is false for a destructive op. With a restore verb it becomes truthful; fix it in the same increment. |

**A `make down && make up` is required to adopt this, on every existing deployment — and that irony belongs
in the open.** `PermUpgradePackage` and `UpgradePackageDDL` are **randomly generated NanoIDs persisted in the
bootstrap file** (`PrimordialIDsRaw`, `nanoid.go:220-223`, `:495-498`), not derived values, so a fourth op
adds two new fields; `checkVersion` refuses any file whose version string is not the current one and sends the
operator to `make down && make up` (`nanoid.go:461-470`), which force-wipes the stack. **A design whose whole
purpose is sparing an operator a `make down` cannot be installed without one.** That is a pre-existing
platform property — twelve prior primordial additions paid it — and this design pays it rather than papering
over it. §17 records the cheaper shape it suggests, as an observation for Andrew rather than scope this fire
absorbs.

`InstallPackage`, `UpgradePackage` and `UninstallPackage` are **unchanged in behaviour**. No existing script
gains a verb; no existing scope branch changes.

### 5.2 The `revive` verb, and why it carries no document

`buildMutationValue` gains a branch that is the tombstone branch with one constant flipped:

```
if m.Op == "tombstone" || m.Op == "revive" {   // seed from the stored document
    for k, v := range prior { doc[k] = v }
}
…
case "update", "tombstone", "revive":
    if m.Op == "tombstone" { doc["isDeleted"] = true }
    if m.Op == "revive"    { doc["isDeleted"] = false }
    preserveImmutableFields(doc, prior, env, stamp, trackerKey)
```

**The mutation parser must never read `document` for a `revive`, exactly as it never does for a tombstone**
(`internal/bootstrap/install_ddl.go:160` emits key-only; `step8_commit.go:510-511` states the property). That
single restriction is what carries the design's central safety claim:

> A `RestorePackage` batch cannot write any **body** byte a prior commit did not already write to that key.

The qualifier is load-bearing and the first draft omitted it (§16, S8). The committer always writes
`doc["key"]` and the `lastModified*` triplet, and `preserveImmutableFields` (`step8_commit.go:566-586`)
*stamps* the creation triplet from the current operation when the stored document lacks one — so restoring a
document that predates provenance preservation attributes its creation to the restoring operator. That is the
same carve-out §12's own test vector already states, extended to the creation triplet. The claim is about
`class`, `data`, `vertexKey`, `localName`, `sourceVertex`, `targetVertex` — everything a script could
otherwise choose.

**The vocabulary this completes was already half-symmetric.** Contract #3 §3.3 describes `tombstone` as the
document-less liveness verb — "a tombstone mutation carries no `document`; one supplied is not honored" — and
`update` as its document-carrying counterpart, which restores an entity when its body does not mark it
deleted. `revive` is the fourth cell of that table: two document-carrying verbs (`create`, `update`) and two
document-less liveness verbs (`tombstone`, `revive`). The capability is not new — §3.3 already sanctions
restoring via `update` — what is new is a form of it that **cannot** carry a body.

Consider the alternative that capability suggests — restore as an `update` carrying client-supplied bodies. `rejectPermissionRoleRewrites`
would hold the permission/role/roleindex bodies to write-once, but it covers **only** those three shapes. A
`vtx.meta.<id>.script` aspect is covered by nothing, so a hand-rolled `RestorePackage` payload could revive a
package's DDL with attacker-chosen Starlark under the package's own key. That turns a restore verb into the
arbitrary-write backdoor `InstallPackageDDLScript`'s own doc block (`install_ddl.go:13-14`) says a privileged
package op must not be. Carrying no document removes the channel rather than guarding it.

The verb is **admitted for `RestorePackage` and nothing else** — default-deny at `step6_validate`'s verb
switch (`:186`) and `starlark_runner`'s (`:338`). A package-authored DDL emitting `revive` fails validation.
Without it every package author would gain "un-tombstone any key I can address", and because a revive carries
no document it would slip `rejectPermissionRoleRewrites`' `m.Document == nil` skip (`:828-830`), resurrecting
a revoked grant with no guard in the path.

**But it must be keyed the way `packageLifecycleType` is keyed, and the first draft got this wrong (§16, S2).**
The draft said "keyed on the resolved operation type". The repo's own comment refutes that, in the very file
this design edits — `step8_commit.go:950-956`:

> *"The executing script is selected by CLASS, not by operationType: `resolveClass` prefers `env.Class`, then
> `payload.class`, and only then falls back to the operationType's registered class. Nothing binds the three
> together … a guard keyed on operationType alone would stand down for exactly the envelope that most needs
> it."*

Verified independently at `step4_hydrate.go:474-495` (that precedence) and `step3_auth_capability.go:343`
(authorization matches `env.OperationType`, never the class). Both naive keyings fail: keyed on
`operationType`, an envelope declaring `RestorePackage` runs whatever script its `class` names; keyed on the
resolved class, an envelope declaring a cheap operationType runs the restore script while step 3 never sees
`RestorePackage`. The admission conjunct is therefore
**`packageLifecycleType(env, payloadClass) == "RestorePackage"`** — the same three-arm read every other
path-independent package guard already uses (`:966-973`) — plumbed into the mutation parser from
`sc.Operation` (`starlark_runner.go:78`), which `parseMutations` (`:316`) does not carry today. That plumbing
is an Increment 2 line item, not an assumption.

**With that correction, the honest statement of where containment lives changes, and it is worth stating
plainly.** `PermRestorePackageKey` gates a *string in the envelope*, not the script — a **pre-existing**
property of every package primitive, `InstallPackage` included, which this verb inherits rather than
introduces. The authoritative containment is `resolvePackageScope` (§5.3), which bounds what the batch may
touch **regardless of who submitted it or which permission they held**; the verb restriction is a second,
independent layer. This is also the sharpest reason for §9's sequencing recommendation: the in-flight
authority-minting guard is the design that closes the class/operationType decoupling itself, and says so —
*"this one runs for every operationType, not only the three package-lifecycle primitives."*

**One half of the claim came back stronger than written.** The "no client document" property does not rest on
the parser being careful: `parseMutations` **hard-fails** a document supplied on any non-create/update verb
(`starlark_runner.go:358-363`, `"tombstone must not carry a document"`), so a `revive` added to the whitelist
at `:338` inherits that refusal automatically rather than needing a new rule.

Every other verb switch treats `revive` as **update-class** — prior-document read (`readPriorDocuments:653-659`,
which is what makes §5.3 cost zero extra reads), `conditionRevision`, `rejectProtectedMutations`,
`rejectPermissionRoleRewrites`, the scope guard's non-`create` branch, `commit_path.go:630`,
`step65_encrypt.go:56`. §11 ships the census of every site as a command.

### 5.3 The owned surface, and the tombstone-provenance discriminator

`resolvePackageScope` gains one branch at `step8_commit.go:1189`:

```
if !manifest.Found { return scope, nil }
if docIsTombstoned(manifest.Doc) {
    if lifecycle != "RestorePackage" { return scope, nil }   // Upgrade/Uninstall unchanged
    scope.resolved = true
    uninstallOp := manifest.Doc["lastModifiedByOp"]
    claimable := func(k string) bool {
        if !isReviveTarget(k, mutations)          { return false } // this batch is reviving it
        p := prior.doc(k)
        if !docIsTombstoned(p)                    { return false } // live → never owned
        return p["lastModifiedByOp"] == uninstallOp                // killed by THIS uninstall
    }
    for k := range declaredKeySet(manifest.Doc) {
        if claimable(k) { scope.owned[k] = struct{}{} }
    }
    // The manifest aspect is NOT in its own declaredKeys — the snapshot is taken
    // before its addCreate (installer.go:1702-1704) — so the loop above can never
    // reach it, and a restore MUST revive it or the package stays uninstalled.
    // The live-manifest path adds both explicitly (step8_commit.go:1198-1199);
    // the restore path does the same, held to the same four conditions.
    if claimable(scope.manifestKey) { scope.owned[scope.manifestKey] = struct{}{} }
    if claimable(scope.pkgKey)      { scope.owned[scope.pkgKey] = struct{}{} }
    return scope, nil
}
```

> **This block is the corrected form.** The first draft populated `owned` from `declaredKeySet` alone and
> was **broken on the mainline path**: `rejectPackageScopeViolations` refuses every non-`create` mutation
> whose key is not in `owned` (`step8_commit.go:1082-1088`), so the manifest's own revive would have failed
> `unscoped` and taken the whole atomic batch with it — every round-trip in §12 would have died on that line.
> Found by the adversarial pass (§16, F1). Note also that the manifest is its **own** provenance reference,
> so condition (4) holds for it by construction; it is written through `claimable` anyway rather than
> special-cased, so a future edit to the conditions cannot leave the manifest behind.

Three conjuncts, and each answers a specific objection:

- **`isReviveTarget` + `docIsTombstoned(prior)`** is `validatedManifestClaims`' own ratified predicate
  (`:1246`) — *a dead key this batch is reviving has no live owner to displace* — applied to the manifest
  resolution instead of to a manifest's growth. It answers W1's stated objection **directly and completely**:
  a retention-class holder that the uninstall deliberately left **live** is live, therefore never enters
  `owned`, therefore cannot be updated or tombstoned by the restoring batch. The guard no longer has to
  refuse the whole manifest to protect one live key.
- **`lastModifiedByOp == the manifest's`** is the discriminator. `Uninstall` tombstones the manifest aspect
  in the same atomic batch as every other declared key (`installer.go:1702-1708`), and `buildMutationValue`
  stamps one `trackerKey` across the batch (`:553`). So a declared key whose tombstone carries the manifest's
  own `lastModifiedByOp` was killed by that uninstall; one carrying anything else was killed by something
  else, and a restore must not undo it. This is dossier entry #12's requirement met — the guard is **not**
  keyed on tombstone-state alone.
- **Cost: zero extra reads, and — because of the manifest fix above — the source document is the
  *conditioned* one.** `readPriorDocuments` loads a prior for every update/tombstone key (`:653-659`), and a
  restore revives every key it inspects, the manifest included. That matters beyond cost:
  `resolvePackageScope` prefers the batch's own prior read precisely because it is the revision the batch
  conditions on, and warns that a batch not mutating its manifest "falls back to an out-of-band read, which no
  batch condition covers" (`:1170-1186`). A restore that omitted the manifest from its batch would compute its
  entire ownership set from an unconditioned read — a race the design would not have seen (§16, S7). Reviving
  the manifest closes it: `uninstallOp` is read from a document the atomic batch asserts a revision on.

**The state table, written before the predicate.**

| # | declared key's state at restore | tombstone's `lastModifiedByOp` | outcome | why |
|---|---|---|---|---|
| 1 | tombstoned | == the manifest's | **revive** | the uninstall killed it |
| 2 | tombstoned by a revoke **before** the uninstall on a **pre-Inc-1** stack, then re-tombstoned by it | == the manifest's | **revive**, named in the preflight | the uninstall overwrote the revoke's stamp; unrecoverable from state (see below) |
| 3 | tombstoned by anything other than that uninstall — a revoke **before** it (post-Inc-1), a meta-vertex tombstone **after** it, or a retention holder already stranded | ≠ the manifest's | **left dead, named in the result — the rest of the batch proceeds** | a deliberate act this restore must not undo; see the paragraph below on why this is a per-key skip and not a batch refusal |
| 4 | live — a retention-class holder the uninstall preserved | n/a | **no mutation**; not owned | already correct; body is unchanged, so no mutation is emitted at all |
| 5 | live — a foreign or unexplained occupant | n/a | **refuse**, named | never owned; the preflight names it before the op is submitted |
| 6 | absent | n/a | **refuse**, named | a revive has no body to write; the operator reinstalls from the manifest instead |
| 7 | the package's manifest is **live** | n/a | **refuse** — "not uninstalled; use `install`/`upgrade`" | |
| 8 | the package's manifest is **absent** | n/a | **refuse** — nothing to restore | |

**Row 3 is a per-key skip, and the first draft made it a whole-batch refusal — which would have made an
ordinary class of packages permanently unrestorable (§16, S4).** The draft argued row 3 was barely reachable
because `RevokePermission`/`RevokeRole` fail on an already-tombstoned link
(`packages/rbac-domain/ddls.go:391-392`, `:428-429`). True, and aimed at the wrong window: the reachable one
is **before** the uninstall. `RevokePermission` tombstones
`lnk.permission.<pid>.grantedBy.role.<rid>` (`ddls.go:427-429`), a key that **is** in the declaring package's
`declaredKeys` (`build.go:396`). Today the uninstall overwrites that stamp, making it row 2; **after Increment
1 skips it, it keeps the revoke's stamp and becomes row 3** — so a whole-batch refusal would mean *every
package an operator has ever revoked a grant on becomes permanently unrestorable*, and Increment 1 would have
converted an over-grant into a total availability loss. A second class is live today with no Increment 1 at
all: a retention-class holder tombstoned before the uninstall is prefix-excluded from the tombstone set
(`installer.go:1680-1690`) and reported `RetentionHoldersAlreadyStranded` (`:1735`), so it keeps a foreign
stamp and is row 3 on the very next restore.

Skipping per key is also the **semantically correct** answer, not merely the available one: a restore undoes
an uninstall and nothing else, so a grant revoked before the uninstall stays revoked. It is not a partial
restore with a success signal, because the result names every skipped key, its stamp's operation, and the
verb that would re-grant it — the operator is told exactly what did not come back and how to bring it back
deliberately. Row 5 and row 6 stay **batch** refusals: a live foreign occupant or an absent declared key mean
the restore cannot produce a coherent package at all, which is a different fact from "one grant stays
revoked".

**Row 2 is the residue, and it needs Increment 1 to close.** `Uninstall` today re-tombstones a key
that is already tombstoned — its loop includes every declared key that resolves, regardless of `isDeleted`
(`installer.go:1755-1799`) — which overwrites the revoking op's stamp with the uninstall's. Increment 1
makes `Uninstall` **skip** an already-tombstoned key and report it in an `AlreadyTombstoned` bucket, mirroring
the two buckets it already keeps for exactly this distinction (`RetentionHoldersAlreadyStranded`,
`SecureColumnsAlreadyErased`). After Inc 1 the discriminator is exact.

**It is not retroactive, and this must be said rather than implied.** A package uninstalled *before* Inc 1
ships carries the uninstall's stamp on every key including any previously-revoked grant, and the revocation
is not recoverable from state — `rbac.permissionRevoked` is an event in a retention-bounded stream, not
queryable state. Restoring such a package reinstates it exactly as declared, which is precisely what a fresh
install would do. **The mechanism that covers row 2 in every era is therefore not the discriminator but the
preflight** (§5.5), which enumerates every grant the restore will revive so the operator can re-revoke.

**How reachable is row 3, honestly?** For grant links, not very: `RevokePermission` and `RevokeRole` both
`fail("UnknownLink: …")` on an already-tombstoned link (`packages/rbac-domain/ddls.go:391-392`, `:428-429`),
so after an uninstall the grant cannot be revoked again. Row 3 is live for `vtx.meta.*` keys, which
`TombstoneMetaVertex` (operator-granted) can tombstone at any time. I am not claiming a broad live
population; I am claiming the conjunct costs one map lookup, is the difference between a guard keyed on
provenance and one keyed on tombstone-state alone, and has at least one reachable path today.

### 5.4 The restore batch (pkgmgr)

`Installer.Restore(ctx, packageName) (*RestoreResult, error)` — the mirror of `Uninstall`
(`installer.go:1649`), and deliberately taking a **name, not a Definition**:

1. Resolve the package vertex key from the name (`substrate.PackageEntityNanoID(name, "package")`) — *not*
   via `findInstalledPackage`, which skips a tombstoned manifest by design (`installer.go:779-781`). Read
   `<pkgKey>.manifest` directly.
2. Refuse rows 7 and 8 of the state table.
3. Read every `declaredKeys` entry plus the manifest aspect and the package root — one `KVGetMulti`, chunked
   at `abstractGuardReadChunk`, mirroring `declaredKeyOccupants` (`installer.go:998-1006`). A failed batch
   refuses; a probe that cannot read the kernel has not found it restorable.
4. Classify each key by the state table. Rows **5 and 6** → refuse the whole restore with
   `ErrRestoreBlocked`, naming every blocking key in its own bucket. Row **3** → omit the key from the batch
   and report it under `LeftRevoked`, with the stamp's operation and the verb that would re-grant it. Row 4 →
   no mutation. Rows 1/2 → `{key, expectedRevision}`.
5. Submit `RestorePackage` with `requestID = contentRequestID(name, manifestVersion, "restore-op", revives)`
   — **not** the uninstall's `deterministicNanoID(name, version, …)` shape, which the first draft mirrored
   (§16, S5). Restore never advances the version, so a name+version derivation is constant across every
   restore of a package: `restore → uninstall → restore` inside the 24h `TrackerTTL`
   (`internal/processor/tracker.go:19`) reuses the first restore's requestId, the Processor replies
   `Duplicate`, and the mirrored reply handling treats `Duplicate` as success (`installer.go:1829`) — **the
   second restore reports success, commits nothing, and the package stays dead.** That is precisely the bug
   `contentRequestID` exists to fix, in its own words (`installer.go:396-409`), reproduced by copying the
   wrong sibling. Folding the revive set's digest in restores uniqueness, because each cycle's
   `expectedRevision`s differ.

**No `Definition` is read, parsed, or diffed.** Restore is `undo uninstall`, not `install at a new version` —
it restores the package at the version the manifest records, and a subsequent `upgrade` moves it forward
through the ratified §8.6 path. That composition is what lets this design sidestep every drift question
(secure-column widening, canonicalName changes, retention-policy edits) rather than answering each one twice.

**Two existing gates keep working, and one of them is not obvious.** Because the restore emits no
canonicalNames of its own, it does not re-run `checkCanonicalNameCollision` — and it does not need to for the
keys it revives, because those keys are the package's own. But a *name* collision is still reachable:
`canonicalNamesFromKeys` skips tombstoned meta-vertices (`installer.go:1335-1337`), so while package X is
uninstalled, package Y may legitimately install a DDL carrying a canonicalName X once held. Restoring X then
produces two **live** meta-vertices with one canonicalName, and the DDL cache serves one per name — arbitrating
to the lowest-keyed root and dropping the loser from both its indexes, so the loser's `permittedCommands`,
`sensitive`, custody and script silently stop applying (`installer.go:1351-1357`). **The restore preflight
must therefore run the live-canonicalName check over the names its revived meta-vertices carry**, using
`scanMeta` + `canonicalNamesFromKeys` and excluding the package's own ids exactly as
`checkCanonicalNameCollision` does (`:1391-1420`). This is an increment obligation, not an inherited
property; §12 owns its test. The same reasoning covers `checkOpMetaOperationTypeCollision` and
`checkWeaverTargetIDCollision`.

*(`scanMeta` performs a full-bucket `KVListKeys`. That cost is the already-filed row "[Pkgmgr] The install
path lists the whole Core KV bucket twice, per operation" — restore inherits it and does not worsen it; it is
not this design's to fix.)*

### 5.5 The preflight is the operator-facing safety mechanism

**The shape, which the first draft asserted rather than designed (§16, F3).** `Uninstall` has no options
struct and no preview mode — `runUninstall` (`cmd/lattice-pkg/main.go:352-424`) submits immediately — so
"mirrors `Uninstall`" does not supply one. The only working precedent is `ApplyOptions.DryRun`
(`internal/pkgmgr/apply.go:11-22`), and its real content is not the boolean but an explicit **guard-ordering
rule**: a guard whose refusal the real run would raise must run **before** the dry-run return
(`apply.go:189-196`, the Secure-Lens custody guard, "so a preview can honestly report that the real apply
would refuse"), while a guard with a **side effect** must run after (`apply.go:211-217`, the op-meta
retirement guard, "a preview must never cancel a live task as a side effect").

So: `Installer.Restore(ctx, packageName string, opts RestoreOptions)` with `RestoreOptions{DryRun bool}`,
and the same rule applied per guard:

| runs **before** the dry-run return (a preview must predict the refusal) | runs **after** (real runs only) |
|---|---|
| the state-table classification of every declared key (rows 3/5/6/7/8) | the op submission itself |
| the live-canonicalName re-collision check (§5.4) — a restore that would be refused for a name someone else took while it was dead must say so in the preview | — |
| the batched read failing (a probe that cannot read the kernel has not found the package restorable) | — |

Nothing on the restore path has a side effect to defer, which is why the right-hand column is empty and worth
saying so: the preflight is a pure read, so `DryRun` costs only the submit.

`Restore` in dry-run form (and the CLI's default before a confirmation) prints, before anything is submitted:

- the package name, the version the manifest records, and the date of the uninstall (`lastModifiedAt` on the
  manifest tombstone);
- **every authority the restore will revive**, per permission: the operationType, the scope, the role the
  `grantedBy` link binds it to, and whether the operationType is core-reserved. The first draft called this
  `restoreAdvice` "reused rather than rebuilt" — it cannot be (§16, S9): `classifyRestorePermissions`
  (`installer.go:1087`) takes `[]PermissionSpec`, a `Definition` field, and a name-only restore has no
  `Definition`. The **partition logic** is reusable; its **input** must be re-derived from the stored
  `vtx.permission.*` bodies the restore already reads (`data.operationType`, `data.scope`, `data.lanes`), with
  the role taken from the `grantedBy` link key's own last two segments. Refactoring
  `classifyRestorePermissions` onto that projection so both callers share one partition is an Increment 2 line
  item — and it matters, because §2's safety argument rests on this output;
- **that identities still holding the package's roles regain those capabilities on commit.** A `holdsRole`
  link is not package-declared, so an uninstall never tombstoned it: it has been pointing at a dead role all
  along and becomes live again the moment the role does. This is correct — the assignment was never revoked —
  and it is the single fact an operator most needs to see before confirming;
- the counts per state-table row, so rows 3/5/6 are visible as refusals rather than surprises.

§2's honest framing depends on this: the reason a restore verb is safe is not that resurrection is harmless,
but that the operator sees the exact authority surface before they authorise it.

---

## 6. Alternatives

### 6.1 (a) Reinstall-as-upgrade — resolve the tombstoned manifest for `UpgradePackage`, and route `Install` through it

**Rejected on three grounded mechanics, any one of which is sufficient.**

1. **It makes every upgrade of an uninstalled package a silent resurrection.** The branch would be keyed on
   `docIsTombstoned(manifest)`, not on intent, so `lattice-pkg install --force` over an uninstalled package
   would resurrect its full authority surface with no operator statement of intent and no distinguishable
   audit record — a `package.upgraded` event. That is `reconcile.go:105-112`'s "unlogged privilege
   escalation" with an operator's fingerprints on a *different* verb.
2. **It requires bending W2, whose current behaviour is correct for its own consumer.** `diffManifest`'s
   revocation guard exists so that an upgrade of a **live** package does not silently revive a grant someone
   deliberately revoked (`upgrade.go:487-517`). For a reinstall every key is in both sets, so the guard
   decides the entire authority half — leave it and the reinstall restores definitions but no grants (a
   partial restore with an `upgrade committed` success signal, which is the occupancy gate's own defect
   reproduced one layer up); bend it and the live-package case loses its protection. **A predicate borrowed
   from another consumer carries that consumer's tolerance**: W2's consumer tolerates a missed revive, mine
   would tolerate a resurrected revocation. Different job.
3. **Routing `Install` through it needs a frozen-contract *weakening*.** `InstallPackageDDLScript` fails on
   any non-`create` verb (`install_ddl.go:94-95`), and Contract #8 §8.2's guardrail table states create-only
   normatively while §8.4 rule (3) exempts `InstallPackage` from the ownership rule **because** it is
   create-only. Teaching install to emit updates changes both, and turns an op that is safe by construction
   into one that is safe by guard. A new verb's contract change is **additive**: nothing that is create-only
   today stops being create-only.

**Could a variant of (a) beat the recommendation?** The variant worth testing is *"keep the verb but gate it
on an explicit `--restore` flag"* — i.e. intent supplied at the client rather than in the op. It fails
objection 1 for the reason the enforcement-point reflex predicts: the intent would live in the client, while
the resurrection happens in the Processor, so a hand-rolled `UpgradePackage` payload gets the resurrection
without ever passing a flag. Intent that the commit-time guard cannot see is not intent.

### 6.2 (b) A distinct `RestorePackage` verb — **recommended**

Everything in §5. The costs, stated plainly: one new kernel DDL script (structurally a copy of the
uninstall's), one new primordial permission, one new mutation verb touching ~20 verb switches (§11), a new
contract §, and a new CLI verb + Loupe endpoint. The benefits are objections 1–3 above, inverted.

### 6.3 (c) Leave restoration out of the package plane — "uninstall is destructive by design"

This is the honest reading of what uninstall means, it is free, and per the "price the demand-side fix"
discipline it is a mandatory alternative given §2's two-caller census. Its concrete form is **(c′)**: make
irreversibility explicit — a typed-confirmation on Loupe's uninstall button, a `--yes-i-understand` on the
CLI, and a rewrite of the occupancy gate's remedy text to say plainly that a package name is single-use for
the life of the deployment.

**Rejected, and it is worth being precise about why**, because (c′) is genuinely better than nothing:

- It **prevents** the next mistake and **recovers** none. Every package already uninstalled stays dead.
- It ratifies a property no operator would choose: that installing a package permanently consumes its name
  in that deployment. Renaming to recover (§2, option 2) re-keys every entity, so the recovered package is a
  *different* package sharing a schema — the business data written under the old keys is orphaned.
- It leaves §1.6's defect standing. The refusal text would still be the platform's closest thing to a restore
  path, and would still not restore a package.
- The prevention half is cheap and correct **regardless**, so it is not really an alternative: **the typed
  confirmation ships in Increment 3 alongside the restore affordance**, where it can honestly say "this is
  reversible with `restore`" instead of "this is forever".

### 6.4 (d) Make `Uninstall` hard-delete instead of tombstoning

Then a reinstall is a plain greenfield create and nothing else is needed. Rejected: Contract #1's soft-delete
addressing is the platform's audit posture ("vertices remain queryable for audit",
`installer.go:1646-1647`), no hard-delete path for entity keys exists (§1.1), and a NATS delete marker still
occupies the subject — so a `CreateOnly` would conflict against the marker exactly as against a document
(`internal/substrate/batch.go:149-150`; the occupancy design's build note records the same fact from the
probe side). It trades an audit invariant for a bug it would not even fix.

---

## 7. What a restore does NOT restore

The substitution reflex, pointed at a *revival*: what did the uninstall silently carry away that comes back,
and what does not?

| | after `RestorePackage` |
|---|---|
| DDL/lens/op-meta/weaverTarget/loomPattern/pane meta-vertices + aspects | **restored**, live, and re-registered without a restart (§7.1) |
| permission vertices, `grantedBy` links, role vertices, `.canonicalName`, roleindex | **restored**, with the body byte-identical to what the package declared (§5.2) |
| `holdsRole` assignments to the package's roles | **never removed** by the uninstall; become effective again the moment the role does — the preflight says so out loud (§5.5) |
| retention-class holders | **never removed**; still live, and re-declared by the restored manifest (state-table row 4) |
| business data the package's DDLs wrote | never touched by uninstall; untouched by restore |
| lens read-model rows in the target bucket/Postgres | not restored by the commit; **backfilled** by Refractor once the lens re-activates — its durable consumer is created `DeliverPolicy: DeliverLastPerSubject` (`cmd/refractor/main.go:2226-2236`), so a freshly re-registered lens replays the latest revision of every matching subject rather than only projecting from now on. This was the claim most likely to be an empty-read-model trap; it is not one |
| ciphertext in a secure-lens target store | never destroyed by uninstall; the restored lens resumes attesting its coverage — which is the damage the filed row *"[Pkgmgr] Uninstall erases the same secure-lens history with no attestation"* describes, recovered. This design does **not** close that row: that row is about the missing attestation *at uninstall time*. |
| the version the package was at | the manifest's recorded version — restore never advances it (§5.4) |
| a grant revoked **before** an uninstall that pre-dates Increment 1 | comes back (state-table row 2); the preflight enumerates it |

### 7.1 Every runtime consumer picks the revive up — verified, not assumed

A revive's KV write is an ordinary versioned PUT with `isDeleted: false`, so every CDC consumer sees a normal
update event. The question is whether each consumer's handler **re-registers** what it dropped on the
tombstone. Traced, per consumer:

1. **Processor DDL cache** — invalidation is keyed on the `vtx.meta.` key prefix and is **verb-agnostic**
   (`step8_commit.go:429-448`), running synchronously in-commit. `loadMetaVertex` treats `IsDeleted` as the
   sole absent-test (`ddl_cache.go:623-625`), and `Invalidate` re-derives `byName`/`byMetaPK`/`byCommand`
   from the full root set (`:1017-1048`). Op-metas take the identical path (`:534-575`, `:991-1000`), so the
   read-disposition floor restores itself.
2. **Refractor lens registry** — `dispatchSpec` looks up `s.known[lensID]`, and because the tombstone deleted
   the entry (`:912` root, `:1001` spec) the revive takes the `!exists` arm and calls `loadCB` — a **fresh
   load**, not an update (`internal/refractor/lens/corekv_source.go:1364-1373`; the first draft cited this
   file at the wrong path), wired to `activateIfNotRegistered` → `startPipeline`
   (`cmd/refractor/main.go:1926-1932`). The watch filter is the static
   `[]string{"vtx.meta.", "lnk.meta.*.subtypeOf.>"}` (`:725`) — no per-lens narrowing, no revision high-water,
   no negative cache — and both root-first and spec-first orderings drain (`:975-982`, `:1013-1025`), so the
   atomic batch's ordering is safe either way. `reloadpin.RefusedChange` is not on this path at all; its only
   caller is `upgrade.go:644`, an advisory log-only diff.
   **The one exception, which §12's e2e must pin rather than assume:** `startPipeline` re-runs every
   precondition on a revive, and a lens carrying `*` labels under a taxonomy that has not resolved is recorded
   refused and returns (`cmd/refractor/main.go:1449-1451`) — dark until the next taxonomy event. A "no restart"
   assertion written without accounting for it is flaky, not false.
3. **Weaver targets and Loom patterns** — one shared CDC source; `removeVertex`/`removeSpec` drop the target
   on tombstone and `dispatchTarget` finds no owner and calls `loadCB` on revive
   (`internal/weaver/registry.go:552-595`, `:987-1041`); `indexPattern` repopulates unconditionally
   (`:1123-1148`).
4. **The auth plane** — the relevance gate that decides whether a link event reaches a lens judges on
   **relation name and endpoint types only**, never on `isDeleted` (`pipeline/dispatch.go:92-96`, `:258-265`),
   so a tombstoned→live `grantedBy` link is exactly as relevant as its tombstone was, on both the actor-aware
   and plain paths. `capabilityRoles` re-projects the actor's capability set.
5. **`bootstrap.reconcile`** — writes only keys in `built`, this binary's own primordial kernel set
   (`reconcile.go:125-133`), so a restored *package* entity is invisible to its write path. It can neither
   fight a restore nor re-tombstone it.
6. **`registry_probe`** — a stateless repeating diff; it counts a lens declared from its root
   (`registry_probe.go:332`) and stops reporting it missing once the runtime registry converges. (The
   pre-existing root-vs-`.spec` asymmetry it carries does not manifest here: a restore revives root and spec
   in one batch.)
7. **`opmetaretirement`** — upgrade-time only; it does not run on the restore path.

---

## 8. Contract surface (the edits are staged uncommitted in `main`)

- **Contract #8 §8.2** — one sentence after the create-only note: `RestorePackage` is **revive-only**, the
  mirror of install's create-only, and its script emits no document.
- **Contract #8 §8.4** — a *Restore scope* paragraph at the end of the section, stating the four per-key
  conditions of §5.3 normatively. It answers the guard's own recorded objection in the contract text: a key
  that is **live** is never owned by a restoring batch, which is what keeps the retention-class holders the
  guard's comment names outside a restore's reach.
- **Contract #8 §8.5 (new)** — the op: payload, guardrails, per-key OCC, scope, authority, the event, and an
  explicit statement of what it does and does not restore (§7). **§8.5 was previously unused** (the document
  runs 8.1–8.4, 8.6, 8.7), so the new section renumbers nothing — which matters here, because the pending
  Contract #6 proposal beside it records that five call sites cite "§6.1 rule 3" by number.
- **Contract #3 §3.2/§3.3** — the mutation vocabulary gains `revive`, **admitted only for `RestorePackage`**,
  with §5.2's containment reasoning stated normatively.
- **Contract #3 §3.3 — two corrections, and they are the interesting part.** Grounding this design in the
  contract turned up two normative sentences that shipped, ratified behaviour already contradicts:
  *"There is no separate `restore` op"* (under `update`) and *"Tombstones are permanent; keys are not reused —
  a new entity requires a new NanoID"* (under `tombstone`). Both pre-date `UpgradePackage`'s revive arm
  (`internal/pkgmgr/upgrade.go:458-484`), rbac-domain's three-state `grant_link`
  (`packages/rbac-domain/ddls.go:164-178`) and Contract #8 §8.4 rule (3)'s own revive carve-out. The second is
  the more dangerous: read literally it says a returning entity must be a *different* entity, which is the
  opposite of what version-independent keys (§8.1) exist to guarantee. The corrected text distinguishes the
  two facts that sentence conflated — a tombstoned key is **not** freed for a *new* entity (`create` still
  conflicts against it), and the **same** entity may come back on the same key. **These corrections are
  proposed regardless of whether the rest of this design is ratified.**

**Is the constraint being changed one that deserves to exist?** Yes, in both directions. §8.2's create-only
guardrail for `InstallPackage` is load-bearing and is left alone — this design's whole verb choice exists to
avoid weakening it. §8.4's tombstoned-manifest refusal is also correct as written; what it lacked was a
lifecycle op for which resolving a dead manifest is the *point*, and a narrower ownership rule than "the whole
stale set". Both are supplied rather than relaxed.

---

## 9. Interaction with `package-authority-minting-provenance-design.md` (📐 awaiting-Andrew, carries a fork)

That design stages an uncommitted §8.4 subsection, *Authority-minting admission*, whose governed set includes
**"a `create`, or tombstoned→live `update`, of a `lnk.permission.<pid>.grantedBy.role.<rid>` link"** — the
shape a restore emits for every permission the package declares. Three concrete obligations follow, and none
of them is optional:

1. **The governed set must name the `revive` verb, not just `update`.** A shape defined as "tombstoned→live
   *update*" does not, on its face, cover a tombstoned→live *revive*. Under this design a revive carries no
   document, so it also slips `rejectPermissionRoleRewrites`' `m.Document == nil` skip. If the two land in
   either order without this, the platform's newest authority-reviving verb is the one shape the authority
   guard does not see.
2. **`RestorePackage` must join the core-owned authority-minting op list** (which today reads
   "…the package-lifecycle trio…"). Otherwise a package may declare a permission conferring `RestorePackage`
   and grant it to a role it controls — the escalation that design exists to close, through the door this
   one opens.
3. **R1's DDL-ownership test reads "already in its `.manifest.declaredKeys`".** For a restore that manifest
   is tombstoned, so R1 must read the same tombstoned manifest §5.3 resolves, or it will refuse every
   restore. The natural resolution is that a revive whose key is in the restoring package's own tombstoned-manifest
   `declaredKeys`, whose prior document is tombstoned, and whose tombstone that package's own uninstall wrote
   confers nothing **that package's manifest did not already record** — the link key encodes both endpoints,
   so the (permission, role) pair is fixed by the key.

   **It is *not* the "strictly stronger than R1/R3" statement the first draft claimed (§16, S6.)** Manifest
   membership records *adoption*, not original declaration: `validatedManifestClaims` admits any **tombstoned**
   key a batch updates into that batch's own `declaredKeys` (`step8_commit.go:1244-1249`), because
   "a dead key has no live owner to displace" is a claim about displacement, not provenance. So a package's
   manifest can name a `grantedBy` link it never declared — one minted at runtime by `GrantPermission` and
   later revoked — and that key then passes all three restore conjuncts. The rule is therefore **weaker** than
   R1 and R3 and must be written into §8.4 as an additional admission with its own bound, not as a
   strengthening. §17 records the underlying adoption path, which is pre-existing and belongs to the
   authority-minting design rather than to this one.

**Ordering — grounded, not a guess.** Either order *works*; the asymmetry is what each order costs. If
authority-minting ships first, restore is built against a live governed set and obligation 3 is a paragraph
in that design's own text. If restore ships first, a new verb capable of reviving grant links exists for the
length of the window with no authority-minting guard in the path — and that design's build then has to
retro-admit a shipped shape. **Recommendation: authority-minting first.** It is already blocked on your fork
decision, so this costs nothing but sequence.

---

## 10. State-lifetime table

**This design introduces no new stateful mechanism** — no registry, cache, latch, watch, accumulated set,
marker, or TTL. Everything §5.3 decides is read at commit time from the prior-document map the commit already
builds, off fields Contract #1 §1.3 stamps on every write.

| new state | created | reset | carried | ordered |
|---|---|---|---|---|
| *(none)* | — | — | — | — |

The one *derived* quantity — "was this key's tombstone written by the uninstall that killed the manifest?" —
is a comparison of two values already on disk, evaluated fresh on every commit. It is level-triggered by
construction: it needs no arming, cannot race, and is unaffected by crash, replay, reconnect or restart. The
one boundary it does depend on is that both sides come from the **same** document generation; a manifest and a
key restored from different backups would compare stamps across a discontinuity. The refusal is the safe
direction (a mismatch refuses), so a restore-across-a-restore is loud, not silently wrong.

---

## 11. Executable censuses

Each count this design relies on ships as the command that derives it, so the build's Phase-0 re-runs it
mechanically instead of trusting this prose.

**C1 — every mutation-verb switch `revive` must be added to.** Sizes Increment 2's Processor work.

```
grep -rn '"tombstone"' --include="*.go" internal cmd | grep -v "_test.go" | grep -v "^internal/spike/"
```

Expected at time of writing: **28 lines** (the first draft said 26 and did not reproduce — §16, F4; a census
whose whole claim is that it is command-derived has to reproduce, so the number is corrected rather than the
command). Of those, the *decision* sites are `step6_resolve_ddl.go:484,554`,
`step6_validate.go:186,221,241,273`, `starlark_runner.go:338`, `commit_path.go:630`,
`step8_commit.go:296,523,544,545,655,730,828,1229`, `step65_encrypt.go:56`, `script_context.go:197` (doc),
plus `internal/bootstrap/install_ddl.go:221,233,322` (the upgrade script's own vocabulary + input schema) and
`internal/pkgmgr/apply.go:301` / `cmd/lattice-pkg/main.go:319` (reporting). **Unit: matching lines, not
distinct switches** — several lines belong to one `switch`. `internal/spike/` is excluded deliberately: it is
a spike, not a commit path; if the build finds it wired in anywhere, that is a finding, not a chore.
The remaining lines are **producers or prose**, and each is named rather than left to be rediscovered:
`internal/bootstrap/meta_ddl.go:70` and `install_ddl.go:160` (Starlark emissions — the second is the sibling
being cloned, not modified), `install_ddl.go:352` (an examples literal), `upgrade.go:581` (`diffManifest`'s
tombstone arm — out of scope because Upgrade's behaviour is unchanged), `step6_validate.go:552` (a comment).
The ~88 `"op": "tombstone"` emissions under `packages/` are producers too and keep emitting `tombstone`.

**The method has a blind spot, and it is the reason §13 mandates the verb×site table rather than this grep.**
A decision site need not contain the string `"tombstone"`: `step8_commit.go:1083` (`if m.Op == "create" {
continue }`) and `:1092` (`if m.Op != "create" { continue }`) decide `revive`'s fate through a binary
create/not-create test that this census cannot see. Both happen to be **correct** for `revive` by
construction — it lands in the same bucket as `update`/`tombstone`, which is what the design wants — but that
is luck, not coverage. A literal-string census sizes the work; only the exhaustive table proves it.

**C2 — the authoritative declared-key count per package** (sizes the restore batch, the prior-read fan-out,
and the op payload). Source literals are a *poor* proxy — several packages build specs in loops — so the
authoritative derivation is the live manifest:

```
lattice-pkg list --json | jq -r '.[].name' | while read -r p; do
  echo "$p $(nats kv get CORE "vtx.package.$(…entityNanoID p package…).manifest" --raw | jq '.data.declaredKeys | length')"
done
```

The static lower bound from source literals, for orientation only (`DDLSpec{`/`LensSpec{`/`PermissionSpec{`/
`OpMetaSpec{`/`RoleSpec{`/`WeaverTargetSpec{` per `packages/*/`): the largest declarers are `clinic-domain`
(23 DDLs), `wellness-domain` (21), `clinic-reminders` (18 DDLs, 6 lenses, 5 permissions, 4 targets) and
`lease-signing` (18). Each DDL and lens mints a root plus several aspects, so the expected key count is
**order 100–250 for the largest package** — the same order the uninstall's batch already carries in
production, which is the cost model this design inherits rather than introduces.

**C3 — cross-package-shared declared keys** (proves §1.5's "exactly one exception"):

```
grep -rn 'sha256NanoID(\|entityNanoID(' internal/pkgmgr/*.go | grep -v "_test.go"
```

Expected: `sha256NanoID` appears **once**, at `build.go:85-87`, minting `vtx.roleindex.*` without the package
name. Every other key derivation folds `def.Name` in. If a second package-name-free derivation ever appears,
§5.3's ownership argument needs re-deriving.

**C4 — role canonical-name collisions across the shipped corpus** (the one namespace C3 exposes):

```
grep -rhn 'RoleSpec{' -A 2 --include="*.go" packages | grep CanonicalName | sort | uniq -c | sort -rn
```

Expected: five distinct names across four packages, no duplicates.

---

## 12. Test strategy

Every test below is owned by a named increment (§13); none is left unowned.

**Increment 1**
- `Uninstall` over a package with one already-tombstoned declared key: the key is **not** re-tombstoned, its
  `lastModifiedByOp` is unchanged, and it is reported in `AlreadyTombstoned`. (`internal/pkgmgr`)
- The existing uninstall tests keep passing unchanged — the skip must not alter the count of keys the batch
  tombstones in the ordinary case. **The closest existing test is
  `installer_test.go:872-907`** (`TestInstaller_Uninstall_ReportsAlreadyErasedSecureColumnsSeparately`), which
  already uninstalls over a pre-tombstoned declared key and survives Inc 1 because it never asserts on
  `res.Tombstoned`. It is **not** a substitute for the new vector: its fixture tombstones by a raw `KVPut`
  (`:888`) rather than through an op, so it stamps no distinct `lastModifiedByOp` and cannot exercise §5.3's
  row-2-vs-row-3 distinction at all. The new fixture must tombstone **through a real op** or the discriminator
  test passes vacuously.

**Increment 2**
- **Verb containment (the design's central claim).** A package-authored DDL emitting `{"op": "revive"}` is
  refused at validation; only `RestorePackage` may emit it. Include the *positive* vector — a restore whose
  revive is admitted — so the negative cannot pass vacuously. (`internal/processor`)
- **A revive writes no client bytes.** A `RestorePackage` payload carrying a `document` on an entry: the
  document is ignored, and the committed value is byte-equal to the pre-uninstall value except `isDeleted`
  and the `lastModified*` triplet. Mutation-test it by perturbing the supplied document.
  (`internal/processor`)
- **Scope, per state-table row.** One vector each for rows 1–8 of §5.3, asserting the specific refusal, not
  merely that an error occurred. Row 4 asserts **no mutation is emitted** for the live retention holder — the
  direct answer to W1's stated objection. (`internal/processor`)
- **The discriminator is not vacuous.** A key tombstoned by a *different* op after the uninstall is refused
  while its siblings revive (row 3) — the mutation test is to make `lastModifiedByOp` equal and watch the
  refusal disappear. (`internal/processor`)
- **`rejectPermissionRoleRewrites` still fires.** A `RestorePackage` batch that reaches the guard with a
  document on a `vtx.permission.*` key is refused — proving the guard is on the revive path, not bypassed by
  it. (`internal/processor`)
- **The canonicalName re-collision.** Uninstall X; install Y declaring a DDL with a canonicalName X held;
  restore X → refused by name, naming both meta-vertex ids. This is §5.4's non-obvious obligation and it
  fails silently if the increment forgets it. (`internal/pkgmgr`)
- **Round-trip e2e on the ephemeral stack.** Install a package with a DDL, a lens, a permission and a role →
  submit an op it authorizes (succeeds) → uninstall → submit again (refused) → restore → submit again
  (succeeds), **with no restart**. The no-restart assertion is what pins §7.1 items 1–4. Assert the lens
  re-projects a row and the actor's `cap.roles.<actor>` regains the entry.
- **Restoring `rbac-domain` itself** — the case §1.6 says the shipped remedy cannot help with. Proves the
  primordial permission's independence from any package.
- **The verb-fallthrough vector (§14).** Assert the batch-op switch's `default` errors, and mutation-test the
  four shared-predicate sites: with `mutationNeedsPrior` returning false for `revive`, the round-trip e2e must
  go **red** — if it still passes, the test is not observing the body the revive is supposed to preserve.
- **Double restore inside the tracker TTL** (§5.4 step 5): `restore → uninstall → restore` in one test with no
  version change; the second restore must actually commit. With `deterministicNanoID` substituted for
  `contentRequestID` the test must go red, or it does not cover S5 at all.
- **Row 3 is a skip, not a refusal**: revoke one declared grant, uninstall, restore — the package comes back,
  the revoked grant does **not**, and the result names it with the verb that would re-grant it.
- **The taxonomy-refused lens** (§7.1 item 2): the e2e's no-restart assertion must wait on the lens actually
  registering rather than on the commit, or it races `startPipeline`'s precondition re-run.

**Increment 2 — the gate that binds the NEXT author.** §14's first risk is that a future verb, or a future
guard, silently mis-handles `revive` by falling through a switch. A lint rule cannot classify that, but it
does not need to: the mechanized form is a **table-driven test enumerating every mutation verb × every
commit-path decision site**, asserting each pair has an explicit decision. Adding a fifth verb (the `delete`
verb Contract #3 §3.3 already names as separately-designed) then fails the table until every site decides it,
instead of compiling quietly. This ships **in Increment 2**, not as defense-in-depth afterwards — per the
standing lint doctrine, the convention and the thing that enforces it land together.

**Increment 3** — the Loupe endpoint returns 409 (not 502) for every `ErrRestoreBlocked` bucket; the detail
page renders the preflight's authority list before the confirmation.

**Review depth is the Steward's sizing** (`agents/steward/SKILL.md` §4). For its input: Increment 2 is
**posture-changing** — it adds a mutation verb and widens a commit-time security guard. Increments 1 and 3
are not.

---

## 13. Decomposition for the Steward

**Size: L** (the board row said M; §11's C1 and the security-plane test surface in §12 do not fit an M).

**Increment 1 — `Uninstall` preserves prior-tombstone provenance.** Skip an already-tombstoned declared key
rather than re-tombstoning it; report it in a new `AlreadyTombstoned` bucket, mirroring
`RetentionHoldersAlreadyStranded` and `SecureColumnsAlreadyErased`. Independently shippable, independently
valuable (`UninstallResult.Tombstoned` becomes honest, and the batch shrinks), and it is what makes §5.3's
discriminator exact rather than vacuous. **Not posture-changing.**

**Increment 2 — `RestorePackage`, end to end.** The `revive` verb + its containment + the verb×site table;
**the whole kernel-seeding checklist in §5.1** (the `primordial.go` seed calls, the two new bootstrap NanoID
fields, `PrimordialVertexKeyCount` 37 → 40, the `checkVersion` bump, the test magic number, `verify-kernel`,
and the untruthful uninstall compensation hint) — that table is the increment's scope list, not a footnote,
because the first draft omitted it entirely and would have handed the Steward an op that never seeds;
`isPackageLifecycleOp`; the `packageLifecycleType`-keyed verb admission **plus the `sc.Operation` plumbing
into `parseMutations`** (§5.2); the shared `mutationNeedsPrior`/`mutationIsConditioned` predicates + the
batch-op `default` (§14); the `resolvePackageScope` restore branch + the discriminator; `pkgmgr.Restore` +
`RestoreOptions{DryRun}` + the preflight + `classifyRestorePermissions` refactored onto a stored-body
projection (§5.5) + `ErrRestoreBlocked`/`LeftRevoked`; `contentRequestID`; `lattice-pkg restore <name>`. **This is deliberately one fire and not three.** The verb, the guard branch and the op are each
dead scaffolding without the other two — a `revive` verb with no `RestorePackage` realizes no value and can
only be exercised by a test, which is the shape this repo's dead-scaffolding test exists to refuse.
**Posture-changing.**

**Increment 3 — the operator console (Loupe lane, `cmd/loupe/**`).** `POST /api/packages/restore`; the new
sentinel added to `packageApplyStatus` (`cmd/loupe/pkg.go:369-378`) so it renders 409 rather than 502 — the
pkgmgr dossier's *"a new failure mode is not shipped until every surface that renders it says the right
thing"* entry, which was minted by exactly this mistake on `ErrDeclaredKeysOccupied`; the package detail
page's restore affordance; and §6.3's typed confirmation on the **uninstall** button, which can now honestly
say the action is reversible. **Its board row belongs on `backlog/loupe.md`, not here** — Increment 2 must
not land leaving Loupe rendering a raw 502 for the new sentinel, so Increment 2's own close should confirm
the row is filed. **Not posture-changing.**

---

## 14. Risks

- **The `revive` verb's blast radius is the vocabulary, and the first draft's mitigation was wrong** (§16,
  S3). "Let the compiler find the rest" does not work: the load-bearing sites are **not switches**. Four are
  `!=` predicates — `readPriorDocuments` (`step8_commit.go:655`), `rejectProtectedMutations` (`:730`),
  `applyHydratedRevisions` (`commit_path.go:630`), and `buildMutationValue`'s prior seed (`:523`) — and the
  batch-op switch (`:293-296`) has **no `default`**. An unhandled `revive` therefore commits as a plain PUT
  with neither `CreateOnly` nor `HasRevision`, and with `prior == nil` from the skipped read
  `buildMutationValue` writes only the provenance fields — **erasing `class`, `data`, `sourceVertex` and
  `targetVertex` on every declared key, unconditioned.** The verb whose purpose is to restore a body would
  destroy it, silently, on the mainline path.
  **Mitigation, structural rather than a checklist:** introduce one shared predicate pair
  (`mutationNeedsPrior(op)` / `mutationIsConditioned(op)`) and rewrite all four `!=` sites onto it, so a new
  verb is decided in **one** place; give the batch-op switch an explicit erroring `default`; and ship the
  verb×site table test (§13) that fails when a fifth verb — the `delete` verb Contract #3 §3.3 already names
  as separately-designed — arrives without a decision at every site.
- **Restore is bounded by the manifest it revives.** If a manifest's `declaredKeys` were ever incomplete, the
  restore is incomplete in the same way and reports success. This is not a new exposure — `Uninstall` has the
  identical dependency and has shipped on it — but it is the one place where "restore committed" could be
  narrower than "the package is back". The round-trip e2e (§12) is what keeps it honest.
- **Row 2 of the state table is not retroactively closable** (§5.3). Stated in the design, stated in §8.7,
  and covered operationally by the preflight rather than by a mechanism.
- **§9's ordering.** If the two designs are built in the other order, obligation 1 becomes a live gap rather
  than a paragraph.
- **Adoption costs a full stack wipe** (§5.1). Every existing deployment must `make down && make up` to take
  the new primordial keys. Sequence Increment 2 accordingly — it is not a hot-deployable change, and the
  demo box's own state is lost adopting the feature that exists to stop state being lost.

---

## 15. Open questions — resolved

| question | resolution |
|---|---|
| Does restore take a name or a Definition? | **A name.** Restore is `undo uninstall`; it restores at the manifest's recorded version and an ordinary `upgrade` moves it forward (§5.4). This is what makes every drift question — secure columns, canonicalNames, retention policy — someone else's already-ratified problem. |
| Reuse `update`, or add a verb? | **Add `revive`.** An `update` needs client-supplied bodies, and nothing guards a `vtx.meta.*.script` body — which would make `RestorePackage` an arbitrary-write backdoor rather than a restore (§5.2). |
| Should `apply`/`install` auto-route to a restore when it finds tombstoned occupants? | **No.** Intent the commit-time guard cannot see is not intent (§6.1). The occupancy gate keeps refusing and now names a remedy that works. |
| Where is the tombstone-provenance rule enforced — client or Processor? | **Both, authoritatively in the Processor.** It bounds what a hand-rolled payload may revive, so it is a security invariant, not authorship hygiene; the client mirrors it only so the operator gets a readable refusal instead of `unscoped`. |
| Does a restore need to re-run the install-time collision gates? | **The canonicalName/opMeta/weaverTarget-id checks, yes** — a name it once held may have been claimed while it was dead (§5.4). The permission-identity and key-shape gates, no — it emits no new keys. |
| Contract #3 §3.3 says "there is no separate `restore` op" and "keys are not reused" — does that block this? | **No — those sentences are already wrong.** Both are contradicted by shipped, ratified revive paths (§8). They are corrected in the same contract diff, and the correction stands on its own merits. |
| Is `RestorePackage` an architectural fork? | **No.** It is a mechanism-level decision resolved in §6 on grounded mechanics. The frozen-contract change is what routes this to Andrew. |

---

## 16. Adversarial pass — run, and what it changed

Run as a deferred gate on this design before it was flagged, per the Designer lane's obligation to discharge
its own gates. Two cold reviewers with disjoint lenses, both read-only against the working tree.

**Completeness / mechanism-reality lens — 2 BLOCKING, 2 SERIOUS, 1 MINOR, all folded above.**

| # | finding | where it landed |
|---|---|---|
| F1 | **BLOCKING.** §5.3's owned-set loop populated `owned` from `declaredKeySet` alone — but the manifest aspect is never in its own `declaredKeys` (`installer.go:1702-1704`), so the manifest's own revive would have been refused `unscoped` (`step8_commit.go:1082-1088`) and taken the whole atomic batch with it. The mainline path, not an edge case. | §5.3 pseudocode rewritten; both explicit keys added, routed through the same four conditions |
| F2 | **BLOCKING.** The design named `install_ddl.go` and `nanoid.go`'s key *enumeration* but never `primordial.go`, the file whose `add(...)` calls actually seed a kernel DDL and permission — so the op would never have been created on any bootstrap. Nine further sites (key count, version gate, the test's magic number, `verify-kernel`) went with it. | §5.1 gains the full checklist as a table; §13 makes it Increment 2's scope list |
| F3 | **SERIOUS.** §5.5's "dry-run form" was asserted; `Uninstall` has no options struct, and `Apply`'s `DryRun` is a guard-**ordering** rule, not a boolean. | §5.5 gains `RestoreOptions{DryRun}` and the per-guard before/after table |
| F4 | **SERIOUS.** C1's stated result (26) did not reproduce — the command returns 28. A census whose claim is that it is command-derived must reproduce. | §11 C1 corrected; the five non-decision lines named; the literal-string method's blind spot documented |
| F5 | **MINOR.** `installer_test.go:872-907` already covers the Inc 1 scenario, but tombstones via raw `KVPut`, so it stamps no distinct `lastModifiedByOp` and cannot exercise the discriminator. | §12 Increment 1 requires the new fixture to tombstone through a real op |

Claims the pass **verified rather than broke**: the single Core-KV `KVDelete` (§1.1); censuses C3 and C4
exactly as stated; the package-namespacing of every key class including the `pane offeredTo role`,
`permission forOperation meta` and `subtypeOf` link classes; and §7's read-model claim, which turned out to be
stronger than written — a re-registered lens **backfills** (`DeliverLastPerSubject`), it does not merely
project forward.

**Security / soundness lens — 2 BLOCKING, 4 SERIOUS, 3 MINOR, all folded above.** It found F1 independently,
by a different route (`build.go:430`'s snapshot comment rather than `installer.go`'s), which is the strongest
evidence the finding is real and not a reviewer's misreading.

| # | finding | where it landed |
|---|---|---|
| S1 | **BLOCKING.** Same defect as F1, found independently. | §5.3 |
| S2 | **BLOCKING.** "Keyed on the resolved operation type" is unsound — the executing script is selected by **class**, not operationType, and `step8_commit.go:950-956` says so in the file this design edits. Both naive keyings are bypassable. It also forced an honest restatement: `PermRestorePackageKey` gates a string in the envelope, not the script, so the authoritative containment is the scope guard, not the permission. | §5.2 rewritten onto `packageLifecycleType`'s three-arm read; §9's sequencing argument sharpened |
| S3 | **SERIOUS.** The guards §1.4 called "still in force" are `!=` predicates, and the batch-op switch has no `default` — an unhandled `revive` commits **unconditioned** with `prior == nil` and erases the body it exists to restore. §14's "let the compiler find it" was wrong. | §1.4 restated as an obligation; §14 replaced with a shared-predicate fix + a mutation test |
| S4 | **SERIOUS.** Row 3's reachable window is **before** the uninstall, not after — so a whole-batch refusal would make every package an operator has revoked a grant on permanently unrestorable, and Increment 1 would have *caused* it. | Row 3 is now a per-key skip with a named report; §5.4 step 4 and §12 updated |
| S5 | **SERIOUS.** `deterministicNanoID(name, version, …)` reproduces the exact dedup bug `contentRequestID` was written to fix — a second restore in 24h reports success and commits nothing. | §5.4 step 5; a red-on-substitution test in §12 |
| S6 | **SERIOUS.** §9's "strictly stronger than R1/R3" is unsound: `validatedManifestClaims` admits any tombstoned key into a batch's own `declaredKeys`, so manifest membership records adoption, not declaration. | §9 obligation 3 corrected to "weaker, needs its own bound"; the adoption path recorded in §17 |
| S7 | **SERIOUS→resolved by F1's fix.** With the manifest omitted from the batch, `uninstallOp` came from an *unconditioned* out-of-band read (`:1170-1186`), so §10's "cannot race" was false. Reviving the manifest closes it. | §5.3's cost bullet |
| S8 | **MINOR.** "Cannot write any byte" is literally false — the committer stamps `key`, `lastModified*`, and heals an absent creation triplet. | §5.2 qualified to *body* bytes |
| S9 | **MINOR.** §5.5 said `restoreAdvice` is "reused rather than rebuilt" while §5.4 said no `Definition` is read; `classifyRestorePermissions` takes `[]PermissionSpec`. | §5.5: partition logic reused, input re-derived from stored bodies |
| S10 | **MINOR.** A cited path did not exist, and `startPipeline` re-runs preconditions on a revive — a taxonomy-refused lens stays dark. | §7.1 item 2 corrected and extended; §12 pins it |

Claims this pass **verified rather than broke**: §7.1's DDL-cache, auth-plane and reconcile items (with a
stronger citation than the design had — the auth-plane target write is guarded only by a monotonic watermark
with no resurrection refusal, `adapter/natskv.go:378-384`, so the revive's higher sequence overwrites the
retraction); §1.1; §1.5's roleindex exception being unreachable as a cross-package surface; §5.3's fail-closed
posture on an undecodable prior (`docIsTombstoned(nil) == false` ⇒ not owned ⇒ refused); and — checked
specifically because the design might have understated it — §5.5's `holdsRole` blast radius, which is
**exactly right**: the authority genuinely was withdrawn while the role was dead (vertex liveness is enforced
in the walk, `ruleengine/full/executor.go:811-813`) and genuinely returns.

**What these passes say about the draft's habits, beyond the fixes.** F1 and F2 are the same failure in two
places: *"mirrors X"* stated about a mechanism not opened. §5.3 mirrored the live-manifest branch without
reading its last two lines; §5.1 mirrored "the existing three" without opening the file that seeds them. Both
were sentences of the form the grounding reflexes name explicitly as unopened mechanisms, and both were in the
draft anyway — which is the argument for running the checklist as a checklist rather than recalling it.

S2, S5 and S9 are one further habit and a sharper one: **precedent-transfer without re-reading the
precedent.** "Mirrors `Uninstall`" carried across a requestId derivation whose own doc comment documents the
bug it causes (S5), an options struct that does not exist (F3), and a permissions partition whose input type
the mirror cannot supply (S9); "keyed on the operation type" carried across a keying the target file's own
comment refutes (S2). Every one was cheap to check and none was checked, because the sentence that named the
precedent read as a citation.

---

## 17. One observation for Andrew, deliberately not scoped into this fire

**Every new primordial op forces a full stack wipe on every deployment, and it need not.** The bootstrap
file's `checkVersion` (`internal/bootstrap/nanoid.go:461-470`) refuses any version string but the current one
and sends the operator to `make down && make up`. That is right for a **breaking** change to the kernel
topology. It is heavier than necessary for a purely **additive** one: this design's delta is two new generated
NanoID fields, and `planReconcile` already creates absent primordial keys on boot without rewriting anything
tombstoned (`reconcile.go:118-121`). A version gate that accepted an older file when the only delta is
additive — generating the missing IDs and rewriting the file — would let a primordial addition land without
destroying deployment state.

I am flagging rather than designing it: it is a different subsystem, it affects every future primordial
addition equally rather than this one specially, and folding it in would be scope creep on a fire that is
already L. It is worth your view because the cost compounds — thirteen primordial additions so far, each
having wiped every stack that adopted it, on a platform now running demo boxes with state worth keeping.

**A second observation, from the adversarial pass (§16, S6), which I am deliberately NOT filing as its own
board row.** `validatedManifestClaims` admits any **tombstoned** key a batch updates into that batch's own
`.manifest.declaredKeys` (`step8_commit.go:1244-1249`) on the ground that "a dead key has no live owner to
displace" — which is true about *displacement* and says nothing about *provenance*. So a package can adopt
into its manifest a key it never declared, provided that key is dead: a `grantedBy` link minted at runtime by
`GrantPermission` and later revoked is the concrete shape, and `rejectPermissionRoleRewrites` does not cover
link keys at all (`:850-905`). This is exactly the residual Contract #8 §8.4 already records in its own words
— *"per-key ownership provenance … does not exist today, so a dual-declaration of an orphaned dead key remains
possible"* — so it is **known, contract-recorded, and pre-existing**, not a new exposure this design opens.
It belongs inside `package-authority-minting-provenance-design.md`'s scope, which is already rewriting exactly
these admission rules; filing a separate row would violate the board's own consolidate-at-filing gate. What
this design owes it is honesty about the bound (§9 obligation 3), which is now stated.
