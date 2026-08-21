# Reinstall over an uninstall — the occupancy gate

**Status: ✅ Winston-ratified — build-ready** (the occupancy gate, §3).
The *restoration* half — reviving an uninstalled package's keys — is **not** in this scope and is
**not** a steward decision; see §5, filed as its own row.

Owns board row *[Pkgmgr] An uninstalled package's permission grant cannot be restored by any reinstall*
(`backlog/lattice.md`), minted by the privacy-base verification-tooling fire
([erasure-orchestration-design.md](erasure-orchestration-design.md) build note).

---

## 1. What is actually true (ground-truthed, not assumed)

Uninstall → reinstall of the same package leaves the package dead, and says the opposite.

1. `Uninstall` tombstones every declared key — the permission vertex and its `grantedBy` link included
   (`internal/pkgmgr/installer.go:1385-1430`). A tombstone is a document carrying `isDeleted=true`
   (Contract #1), **not** a KV delete: the key stays occupied.
2. `Install` step 2 asks `findInstalledPackage`, which skips a tombstoned manifest
   (`installer.go:779-781`) and so reports the package **absent**. The install proceeds as greenfield.
3. `buildManifestBatch` mints **version-independent** keys (Contract #8 §8.1), so the reinstall lands on
   the exact keys the uninstall tombstoned — every one of them.
4. The batch is **create-only** (Contract #8 §8.2 guardrail table; `InstallPackageDDLScript` refuses any
   other verb, `internal/bootstrap/install_ddl.go:94-95`), and step 8 turns each `create` into a
   `CreateOnly` write (`internal/processor/step8_commit.go:294-295`). A tombstoned key defeats that
   assertion — the same fact `diffManifest` already records for the upgrade path
   (`internal/pkgmgr/upgrade.go:429-437`).

So the reinstall can only end two ways, and both are wrong:

- **Within 24h of the original install** — `contentRequestID(name, version, "install-op", ops)`
  (`installer.go:189`) is deterministic in exactly the inputs that did not change, so the reinstall
  reuses the first install's requestId. The `vtx.op.<requestId>` tracker is still live (TTL 24h,
  `step8_commit.go:346`), the Processor replies `Duplicate`, and `Install` treats `Duplicate` as success
  (`installer.go:198`). **Reported: `install committed`. Committed: nothing.** This is the shape the
  board row observed.
- **After 24h** — the tracker has expired, the batch runs, and every `CreateOnly` conflicts. The install
  fails with a bare `RevisionConflict` that names neither the cause nor a remedy — and re-running is just
  as deterministic, so it fails identically forever.

A second occupant class reaches the same wall from the other side: `Uninstall` deliberately leaves
retention-class holders **live** and undeclared (`installer.go:1311-1325`), so a package declaring a
retention class collides on a *live* key, not a tombstoned one.

## 2. Why "just revive it" is not this fire's to build

The revive mechanism exists and is ratified — `diffManifest`'s update arm (`upgrade.go:464-484`) — but it
is reachable only from `UpgradePackage`, and two ratified walls stand between a reinstall and it:

- **Contract #8 §8.2** states the create-only guardrail normatively, and **§8.4 rule (3)** exempts
  `InstallPackage` from the package-ownership rule *because* it is create-only. Teaching install to emit
  updates changes both — a frozen-contract commit, which is Andrew's.
- **The step-8 package-scope guard refuses it anyway.** `resolvePackageScope` treats a tombstoned
  manifest as an uninstalled package and returns an **unresolved** scope that owns nothing
  (`step8_commit.go:1185-1191`), so every non-`create` mutation in an upgrade-shaped reinstall is refused
  `unscoped` (`step8_commit.go:1085-1092`). The guard's own comment argues *against* honouring it:
  "honouring it would hand a dead package's stale set to a live batch."

Widening that guard is a security-plane decision with no ratified pattern to extend, and the pkgmgr
dossier's newest entry is a warning against exactly this shape of widening ("a security-plane skip guard
keyed on tombstone-state alone, with no anchor-type check, silently widens past its ratified scope").
It goes to a designer pass (§5), not to a steward fire.

## 3. Winston's adjudication — what ships

**The false green is a defect in its own right, and it is entirely ours.** An operator who reinstalls a
package and is told `install committed` has been given a fact that is not true, about a security grant,
with no signal anywhere that the verb is still missing. Fixing that needs no contract and no guard
change: `Install` simply has to look before it leaps.

**Ships: the occupancy gate.** Between building the manifest batch and submitting it, `Install` reads
the committed state of its own declared key set and refuses, loudly and specifically, when any of those
keys is already occupied. Two buckets, never conflated:

- **tombstoned** — the uninstalled-package case. The message says the package was uninstalled, that its
  keys cannot be re-created, and names the supported remedy.
- **live** — a retention-class holder uninstall preserved on purpose, or a foreign occupant. The message
  says so separately, because the operator's next move differs.

This converts a permanent, silent, invisible failure into a loud one at the only moment an operator is
watching, and it covers **both** paths of §1 — the ≤24h duplicate *and* the >24h bare conflict — because
it runs before the op is submitted at all.

**The remedy the message names must be true in every state the message can be printed in — and the
first draft of this clause was not.** ~~`ShredIdentityKey` is not in the core-reserved set, and rbac-domain
grants `CreatePermission` to `operator`, so an operator can mint the grant back as a `runtime`-origin
permission today.~~ **Struck 2026-08-21**, falsified by the cold security review of the first build:
`CreatePermission` mints the permission **vertex** only, and authority flows exclusively through the
`grantedBy` edge that `cap.roles.<actor>` projects (`packages/rbac-domain/lenses.go:93`). Restoring a grant
takes a second verb, `GrantPermission { permKey, roleKey }` (`ddls.go:126`) — also granted to `operator`
(`permissions.go:41`). A remedy naming only the first is a success reply with no grant behind it: the very
defect this gate exists to kill, reproduced in its own refusal text.

Three further states make the generic advice false, so the message qualifies itself rather than printing
one sentence for every package:

- a declared permission carrying `Lanes` (live today: `packages/console-operator/permissions.go:79`) — the
  runtime mint writes no `lanes` (`ddls.go:323`) and the lane cannot be added afterwards, `UpdatePermission`
  being both core-reserved and deliberately ungranted;
- a declared permission whose operationType is core-reserved — a `runtime`-origin entry can never confer it
  (Contract #6 §6.1 rule 3), and the attempt raises `AlertCodeReservedOperationGrantRejected`, which the
  platform's own alerting reads as a self-mint;
- the package that declares the remedy verbs themselves — uninstall `rbac-domain` and the grantor is among
  what was revoked.

The message also states that a restored grant is `runtime`-origin: no `declaredBy`, in no manifest, and
never retracted by a future uninstall. An operator is entitled to that before being told to mint one.

**Fail-closed, not fail-quiet.** A batched read that *fails* is not evidence of a clean bucket, so a read
error refuses the install — the same posture `readMetaDocs` already states for the install gates
(`installer.go:867-871`).

## 4. Fire brief

**Scope sentence.** `Install` refuses, before submitting `InstallPackage`, when any key of its own
freshly-built declared set is already committed in Core KV — reporting the tombstoned and live occupants
separately and naming the operator's remedy — so that a reinstall over an uninstall can no longer report
`install committed` while committing nothing.

**Verified touch-list** (checked live at the SHAs above):

| site | what changes |
|---|---|
| `internal/pkgmgr/installer.go:170-196` | new step between `buildManifestBatch` and the submit |
| `internal/pkgmgr/installer.go` (new helper + sentinel) | the occupancy probe and its typed error |
| `internal/pkgmgr/installer_test.go` | the reinstall-after-uninstall regression + the live-occupant case |
| `internal/pkgmgr/apply.go:227-247` | the dry-run preview runs the same probe |
| `internal/pkgmgr/apply_test.go` | the dry-run and `--force` cases over an uninstalled package |

`applyFreshInstall`'s dry-run branch is in scope because it bypasses `Install` entirely: without the probe
it previews `install, N keys created` for an install that cannot commit one of them — the same false green,
one layer up. The non-dry-run `--force` path needs nothing: `Apply` dispatches on `existing == nil`
(`apply.go:136-142`), so a forced install over an uninstalled package reaches `Install` and the gate.

**Precedents to mirror — copy these, do not invent:**

- `readMetaDocs` (`installer.go:859-881`) — one batched `KVGetMulti`; absent key = absent from the map;
  a failed batch is a loud error, never a clean read.
- `abstractGuardReadChunk = 1024` (`taxonomy.go:641-647`) — chunk a batched read at the primitive's
  fast-path cap so a large set never falls to the expensive drain.
- `getCommitted` (`upgrade.go:979-992`) — how a committed document is decoded and its `isDeleted` read.
- `findInstalledPackage`'s near-miss refusal (`installer.go:733-798`) — the house shape for "an exact
  miss with a suspicious neighbour is not the same fact as absence; refuse loudly".
- `ErrVersionMismatch` / `ErrUninstallConflict` / `ErrUpgradeConflict` — the sentinel-error shape.

**Increment order + green checks.**

1. The probe helper + sentinel, wired into `Install`. Green: `go build ./...`, `make vet`.
2. Tests: reinstall-after-uninstall refuses; live-occupant refuses with the other bucket; a clean install
   is unaffected. Green: `go test ./internal/pkgmgr/...`.
3. Gates: `golangci-lint run ./...`, `STRICT=1 go run ./scripts/lint-conventions.go`, `go test ./...`.

**In-scope gotchas.**

- The probe must run **after** `buildManifestBatch` (the declared set is its output) and **before** the
  submit — not before step 2, whose `Skipped` arm is the legitimate same-version re-run and must keep
  short-circuiting first.
- Do not conflate a live occupant with a tombstoned one; the retention-class holder is live *by design*.
- Corpus-scan hazard (pkgmgr dossier): do **not** reach for `KVListKeys` here. The probe reads exactly
  the package's own declared keys — bounded by the package, never by the bucket.
- `Apply`'s fresh-install path (`apply.go:227-248`) calls `Install`, so it inherits the gate. That is
  correct: an apply over an uninstalled package hits the identical wall.

**Non-goals.** Reviving anything. Touching `InstallPackageDDLScript`, Contract #8, or the step-8
package-scope guard. Changing `Uninstall`.

**Dossier entries carried in** (`docs/components/pkgmgr.md`):
*A corpus-wide guard read must exclude the churn namespaces* — honoured by reading only the declared set.
*A field validated after normalization must be MATCHED after the same normalization* — the near-miss half
applies: where a match selects something destructive, keep the comparison exact and make the near-miss a
loud refusal.
*A local gate run and CI's gate run do not see the same tree* — run the `scripts/lint-*.go` gates **after**
committing.

## 5. The blocked half — restoration, and the options for it

Filed as its own board row (`📐 needs designer pass · no-pattern: package-revive ownership scope for a
tombstoned manifest`). Sketched here so the decision is a one-look one:

- **(a) Reinstall-as-upgrade.** Let `resolvePackageScope` resolve a *tombstoned* manifest when the batch
  is itself reviving that manifest in the same commit, and route `Install` through `UpgradePackage` when
  it finds tombstoned occupants. Reuses the ratified revive arm whole; the entire question is whether a
  dead package's recorded `declaredKeys` may define a live batch's ownership surface. Note the guard's
  stated objection is concrete, not abstract: those keys still list the retention-class holders uninstall
  deliberately left live and undeclared.
- **(b) A restore verb.** A distinct `RestorePackage` op with its own kernel DDL and its own scope rule,
  leaving install create-only and §8.4 rule (3)'s exemption untouched. More surface, no widening of an
  existing guard.
- **(c) Leave restoration out of the package plane.** Uninstall is destructive by design; the runtime
  `CreatePermission` path (§3) already restores a *grant*, and a package is reinstalled under a new name.
  Cheapest, and the honest reading of what uninstall means — but it leaves package identity
  single-use-per-lifetime, which is a real cost for any package with lenses or DDLs.

## 6. Build note

**Shipped** — `aca2120` (the gate) + `00a4a73` (the review fix round). Scope as §3/§4, plus the dry-run
preview added mid-build (§4 touch-list).

**What the three cold reviews changed.** The enforcement point was attacked on all three lenses and held:
the false-positive census proved `declared` and `ops` share one append site so no key goes unprobed,
`vtx.roleindex.<sha(canonicalName)>` is the only non-package-scoped declared key and the shipped corpus has
no collision on it, and every Makefile/CI install path re-installs a LIVE package and short-circuits at
step 2 ahead of the gate. What did not hold was the refusal TEXT — see the struck claim in §3 and the two
dossier entries this item minted/extended.

**Known narrowing, stated rather than implied.** `KVGetMulti` returns documents and drops NATS delete/purge
markers, while the commit's `CreateOnly` (`Nats-Expected-Last-Subject-Sequence: 0`) fails against a marker
as against a document. The gate therefore under-reports and never over-reports: a marker-only key reads as
free here and is refused loudly one step later. It is deliberately not marker-aware — that needs a batched
primitive the substrate does not offer — and no production path puts a marker on a declared key (the only
Core-KV `KVDelete` is the outbox consumer's, on `vtx.op.<requestId>.events`, which no package declares; no
production `KVPurge` on the bucket exists). The one route in is an operator clearing a key by hand, which
the refusal text now names as a trap instead of inviting.

**Found and NOT fixed — filed, with their out.** Two pre-existing full-bucket `KVListKeys` reads on the
install path: `checkCoreBucketExists` lists the entire bucket to learn whether the stream exists, and
`findInstalledPackage` lists it again and then issues one serial `KVGet` per `vtx.package.*.manifest`. Both
are the dossier's churn-namespace hazard, both are paid by every Install/Upgrade/Apply/Uninstall, and
neither is this item's mechanism. One row, naming the shared missing primitive.

**Residual with no row, deliberately:** the probe→submit window is unguarded, so a concurrent
install/uninstall landing inside it still yields a bare `RevisionConflict`. The gate narrows that window
and never claimed to close it; the tracker op remains the only true mutual-exclusion point.
