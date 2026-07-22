# Capability Packages

**Component reference** | Audience: package authors + architects

This page is the author-facing guide to building and installing a Capability
Package. The on-the-wire install/uninstall contract (op payload shape,
guardrails) lives in
[`/docs/contracts/08-package-install.md`](/docs/contracts/08-package-install.md);
the Processor-side commit behavior is in
[`processor.md`](./processor.md#package-install--uninstall).

## What a package is

A **Capability Package** is a versioned, atomic bundle of Core KV writes that
adds optional platform behavior *after* bootstrap. Packages are how Lattice
ships business-domain capability (operations, lenses, permissions) without
baking it into the primordial kernel — the kernel is deliberately minimal, and
everything else is a package.

Installed packages:

**Platform base** (identity, RBAC, generic substrate):

- `identity-domain` — the identity vertex type + create/claim/state-machine ops.
- `rbac-domain` — roles, permissions, and the assign/grant ops + their inverses.
- `identity-hygiene` — duplicate-identity detection (`duplicateCandidates` Lens)
  + operator-approved merge (`MergeIdentity` op).
- `orchestration-base` — the generic `task` DDL + `CreateTask` op (assignee
  required + validated, no-orphan by construction) + the package-owned
  `capabilityEphemeral` Lens. The lens re-sources FR56 ephemeral task grants out
  of the bootstrap god-cypher into the disjoint key `cap.ephemeral.<actor>`
  (Contract #6 §6.6 / Contract #10 §10.7) — a proof-of-pattern for the
  contract-contribution model (core owns the capability-kv bucket + step-3
  reader; a package projects the grant type it owns into a disjoint key space).
  Step-3's task-dispatch branch reads the new key as a single GET, no fallback.
- `objects-base` — the generic large-object vertex type (object DDL +
  attach / detach / tombstone ops), the `objectLiveness` GC convergence lens
  (driving the `object-store-manager`'s `TombstoneObject` reclaim), and the
  `objectAttachments` display lens. The **graph side of the off-graph blob
  plane**: the bytes live in the NATS Object Store, the graph holds only a
  content-addressed pointer-aspect (D5); the display lens is the apps' P5-clean
  byte-plane read model.

**LoftSpace vertical** (the lease-application reference slice):

- `service-domain` — the service template + instance vertex type and lifecycle
  ops; an instance records its external-call outcome as aspects (D5).
- `location-domain` — the spatial base domain: unit / building / property
  location vertices + the `containedIn` containment link.
- `service-location` — the residence-based service-access authZ scheme
  (`residesIn` / `availableAt` / `unavailableAt` / `permitsOperation` links) +
  the `capabilityServiceAccess` Lens projecting `cap.svc.<actor>`.
- `lease-signing` — the lease-application convergence vertical: the `leaseapp`
  vertex type + `CreateLeaseApplication` / `SignLease` ops, a real Weaver
  convergence target, the Loom `externalTask` patterns, and the bridge adapters,
  wired into one installable package.
- `loftspace-domain` — LoftSpace listing economics: the `.listing` + `.address`
  aspects on a `location-domain` unit (`SetListing` / `SetUnitAddress`) + the
  `availableListings` / `applicantRosterRead` projection Lenses
  (`applicantRosterRead` is a protected-Postgres Secure Lens — the identity
  name decrypts at projection time, Contract #3 §3.10). Introduces no new
  vertex type.

**Clinic vertical** (the 2nd reference vertical / forcing function for PHI +
recurring schedules):

- `clinic-domain` — the bookable domain: `patient` / `provider` / `appointment`
  vertex types + their aspects and links, with `Create*` /
  `SetAppointmentStatus` / `RescheduleAppointment` / `Tombstone*` ops and the
  `clinicAppointments` / `clinicProviders` / `clinicPatients` projection Lenses
  (the clinic FE's P5 read models).
- `clinic-reminders` — the clinic vertical's first orchestration: one-shot `@at`
  appointment reminders ~24h before the visit (the `appointmentReminders` Weaver
  convergence target re-arms a timer and dispatches
  `directOp(RecordAppointmentReminder)` at the deadline). Depends on
  `clinic-domain` + `orchestration-base`.

A package is **NOT** a runtime plugin. It is a *seed bundle*: at install time it
writes meta-vertices, permissions, lens definitions, and grant links to Core KV.
The Refractor and Processor pick those up via the same CDC watches they use for
the primordial DDLs. There is no in-process plugin loading at operation-handling
time; the package's behavior is realized entirely through Lattice's existing
data-plane mechanisms.

## Directory layout

```
packages/<package-name>/
  manifest.yaml          # name, version, dependencies, declared canonical names
  package.go             # exports `var Package = pkgmgr.Definition{...}`
  ddls.go                # Go literal definitions of DDL meta-vertices + Starlark scripts
  lenses.go              # Go literal definitions of Lens meta-vertices + cypher source (omit if none)
  permissions.go         # Permission vertices + grant link specs
  README.md              # human-facing description
  *_test.go              # package-scoped unit + end-to-end tests
```

Packages live at the repo root (`packages/`), not under `_bmad-output/` (those
are planning artifacts) and not in `internal/` (those are private to the Go
module). Repo-rooted because packages are first-class platform artifacts.

### Why YAML manifest + Go definitions?

The manifest is YAML for readability and tool ergonomics. The DDL / Lens /
Permission definitions are Go because they carry multi-line Starlark scripts and
cypher source — both painful to express as YAML strings. Each package exports a
single `Package` variable that the installer reads:

```go
// packages/identity-hygiene/package.go
package identityhygiene

import "github.com/operatinggraph/lattice/internal/pkgmgr"

var Package = pkgmgr.Definition{
    Name:        "identity-hygiene",
    Version:     "0.1.0",
    Description: "Duplicate-identity detection + operator-approved merge.",
    Depends:     []string{"identity-domain"},
    DDLs:        DDLs(),
    Lenses:      Lenses(),
    Permissions: Permissions(),
}
```

`internal/pkgmgr` builds the install op payload from this `Definition`;
`cmd/lattice-pkg` submits it. The YAML manifest is cross-checked against the Go
`Definition` (`pkgmgr.VerifyAgainstDefinition`) to catch drift.

## Manifest schema

```yaml
name: identity-hygiene
version: 0.1.0
description: Duplicate-identity detection + operator-approved merge.
depends:
  - identity-domain
declares:
  ddls:
    - canonicalName: identityHygiene
      class: meta.ddl.vertexType
  lenses:
    - canonicalName: duplicateCandidates
      adapter: nats-kv
      bucket: duplicate-candidates
      engine: full
  permissions:
    - operationType: MergeIdentity
      scope: any
      grantsTo: [operator]
  weaverTargets:
    - targetId: leaseSigning
      lensRef: leaseSigningCandidates
  loomPatterns:
    - patternId: leaseSigning
      subjectType: lease
  opMetas:
    - operationType: SignLease
```

Field semantics:

- **name**: unique identifier; matches the directory name.
- **version**: simple string equality for idempotency (semver is a future option).
- **depends**: declared dependencies on other packages. A missing dependency is
  logged as a warning and the install proceeds; strict enforcement is a future
  option.
- **declares.ddls[]**: each entry maps to one DDL meta-vertex + its canonical
  aspects (canonicalName, permittedCommands, description, script).
- **declares.lenses[]**: each entry maps to one Lens meta-vertex + its canonical
  aspects (canonicalName, spec, adapter, etc.). The Refractor auto-picks-up new
  lenses via its `vtx.meta.>` watch.
- **declares.permissions[]**: each entry maps to one permission vertex + N
  `grantedBy` links (one per role in `grantsTo`).
- **declares.weaverTargets[]**: each entry maps to one `meta.weaverTarget`
  meta-vertex + its `.spec` aspect (Contract #10 §10.8). `targetId` is the
  weaver-targets row prefix; `lensRef` (authored as a lens canonicalName, or a
  literal NanoID for an already-installed lens) resolves to that lens's id at
  install; the Go `Definition` carries the `gaps` remediation playbook. The
  Weaver registry auto-picks-up new targets via its `vtx.meta.>` watch.
- **declares.loomPatterns[]**: each entry maps to one `meta.loomPattern`
  meta-vertex + its `.spec` aspect (Contract #10 §10.5). `patternId` and
  `subjectType` identify the flow; the Go `Definition` carries the linear
  `steps` (each `{kind, operation, guard?}`). Loom CDC-loads new patterns.
- **declares.opMetas[]**: each entry maps to one op-meta vertex carrying
  `operationType` on its `data`, making that op discoverable by `forOperation`
  resolution. A package declaring an op as the target of a Weaver `assignTask`
  or a Loom `userTask` step must declare a matching `opMetas` entry.

## Installation semantics

Install and uninstall route **through the Processor** as the two primordial
kernel operations `InstallPackage` / `UninstallPackage` — packages do not write
to the substrate directly. The flow:

1. **Build the write set.** `internal/pkgmgr` reads the package `Definition` and
   pre-computes the complete mutation set — every DDL/lens/permission/grant key —
   as **logical documents** (`{class, data, isDeleted}`, no provenance).
2. **Idempotency check.** The op is keyed so a re-install of the same name +
   version is a no-op. A **different** version, or a same-version `--force`,
   takes the in-place **upgrade** path (F-004 — see
   [Upgrade / dev-loop refresh](#upgrade--in-place-dev-loop-refresh-f-004) below).
   The flip side: **a content edit under `packages/<x>/` must bump that
   manifest's `version`**, or plain install no-ops it and no running stack ever
   sees the change. CI enforces this per pushed range
   (`make lint-package-version`, `scripts/lint-package-version.go`; test files
   and `*.md` are exempt).
3. **Submit `InstallPackage`.** `cmd/lattice-pkg` publishes the op (operator
   credential = the admin identity from `lattice.bootstrap.json`). The kernel
   script iterates the mutation set, enforces the install guardrails (key-shape,
   protected-key, system-aspect, create-only — see the
   [package-install contract](/docs/contracts/08-package-install.md)), and emits
   it as the op's mutations.
4. **Atomic commit.** All writes land in ONE step-8 atomic batch on `core-kv`.
   The step-8 `vtx.meta.*` invalidation fires in-commit, so a class the package
   just declared is usable immediately on the running Processor — no restart.
5. **Auto-discovery.** The Refractor picks up new Lens meta-vertices via its
   `vtx.meta.>` watch and begins projecting.

Each install also writes a `vtx.package.<NanoID>` vertex with a `.manifest`
aspect carrying the full manifest JSON — the uninstall-time recovery handle that
enumerates every declared canonical name and its NanoID.

### Provenance

The Processor stamps `createdAt` / `createdBy` / `createdByOp` at step 8 from the
install actor, so installed entities carry real provenance authored by the
install operation — not a synthetic substitute.

## Uninstall semantics

`lattice-pkg uninstall <package-canonical-name>` reads the package's `.manifest`
aspect (`declaredKeys`) and submits `UninstallPackage`, which tombstones each
declared key (cascade-style) and **rejects any protected key** (defense in
depth). Uninstall is soft-delete only — tombstoned vertices remain queryable for
audit; physical removal is out of scope. The Refractor reprojects (lens output
disappears; permissions drop out of cap entries within NFR-P3 lag).

The script accepts an optional per-key `expectedRevision` for OCC; the client
currently submits tombstones unconditionally — see the
[package-install contract](/docs/contracts/08-package-install.md) for the
documented race window and the per-key-revision follow-up.

## Upgrade / in-place dev-loop refresh (F-004)

A package can be **upgraded in place** on a running stack — no `make down`, no
teardown. `lattice-pkg` is upgrade-aware:

```
lattice-pkg install <dir>                  # different version → auto-upgrade in place
lattice-pkg install --force <dir>          # same version → re-apply changed bodies (dev refresh)
lattice-pkg install --dry-run <dir>        # preview the create/update/tombstone delta, submit nothing
lattice-pkg upgrade <dir>                  # explicit upgrade; errors if not installed
```

**Mechanism.** `Installer.Upgrade` reads the installed package's `.manifest`
`declaredKeys`, rebuilds the new manifest, and **diffs by key**:

- a key only in the new manifest → **create**,
- a key only in the old → **tombstone** (sorted),
- a key in both whose logical body changed → **update** (creation provenance —
  `createdAt`/`createdBy`/`createdByOp` — is carried forward; only `lastModified*`
  is re-stamped with the upgrade actor); an unchanged body is **skipped**.

The whole delta is submitted as ONE `UpgradePackage` op and lands in a **single
step-8 atomic batch** (all-or-nothing, with the package `version` aspect bumped in
the same batch — version and entity-set are never inconsistent). The same step-8
**protected-key guard** that defends install rejects any `update`/`tombstone` of a
protected kernel/auth root, so an upgrade can never touch primordial state. After
commit, the Refractor re-projects the changed lenses and the Processor's
`vtx.meta.*` cache invalidates in-commit — converged with no restart.

**Version-independent entity keys** (Contract #8 §8.1) make this work: an entity's
`vtx.meta.<id>` / `vtx.<type>.<id>` derives from package **name + entity tag**, not
the version, so a surviving lens/DDL/role keeps its key across versions (an *update*
of a stable key, not a re-mint that would orphan vertices and break every NanoID
cross-ref — a `lensRef`, a `grantedBy` link).

> **One-time re-mint on a long-lived pre-F-004 stack.** A stack that installed
> packages *before* the version-independent-key change holds version-salted keys.
> The **first** upgrade/`--force` computes version-free keys, so old∩new is empty →
> the delta is **create-all-new + tombstone-all-old** (a blue-green re-mint inside
> the one atomic batch — Refractor sees the old lens deactivate and the new lens
> activate+rebuild with no window). This is expected and self-heals; thereafter keys
> are stable and upgrades are true in-place updates. A fresh `make up` (which
> re-seeds the kernel) never shows it.

**A brand-new entity hot-activates too — no restart.** Once the create mutation is
submitted (a fresh install, a version-bump upgrade, or a same-version `--force`
re-apply — a same-version `install` *without* `--force` is the idempotency no-op
above, not an activation gap), Refractor's `CoreKVSource` and the Processor's
`DDLCache` both react to it exactly like any other CDC event: `CoreKVSource` holds a
**durable** `vtx.meta.>` subscription for the life of the process (`internal/refractor/lens/corekv_source.go`),
and its `dispatchSpec` calls the **same** load callback whether the lens vertex is
brand new or already known — there is no install-time-only path. `DDLCache.Invalidate`
(`internal/processor/ddl_cache.go`, called synchronously from step 8 on every committed
`vtx.meta.*` mutation) is equally unconditional: it reloads whatever is now at the
key regardless of whether the cache previously held an entry there. Proven live at
the unit level by `TestCoreKVSource_LoadsLensFromAspect`, which starts the source
*before* writing the lens — modeling exactly this case. *(A `make down &&
up-<vertical>` fresh bootstrap is a different, narrower case: the **primordial**
kernel seed in `internal/bootstrap` — fixed NanoIDs no package write, new or
edited, can ever touch; see `docs/contracts/07-primordial-bootstrap.md`.)*

**Dev-loop Makefile targets** wrap this for the common edit-test loop on a running
stack:

- `make reinstall-package PKG=packages/<dir>` — diff-apply one edited package in
  place.
- `make refresh-clinic` / `make refresh-loftspace` — diff-apply the vertical's
  packages **and** rebuild+restart its FE binary (`bin/clinic-app` /
  `bin/loftspace-app`) in one command.

## Atomicity contract

**Install OR fail entire.** The single step-8 atomic batch on `core-kv` provides
this. If any write fails (revision conflict, guardrail rejection, etc.), no
writes commit — the package stays in its prior not-installed state. Cross-bucket
atomicity is not available (NATS limitation), so a package cannot atomically
write to buckets other than `core-kv`. Capability KV is reached *indirectly*: the
Refractor reprojects from the package's new Lens meta-vertex, so no cross-bucket
write is needed.

## What a package CANNOT do

- **Mutate other packages' or primordial DDLs.** Protected/primordial keys
  (identity DDL, rbac DDL, canonical roles, the Capability lens, the meta-root
  DDL) are rejected by the install guardrails.
- **Reach into substrate-level surfaces.** No JetStream stream/bucket config
  changes; no admin-auth changes; no event-stream subjects beyond what
  primordial provisioning provides.
- **Write system aspects.** No aspect `localName` may start with `_`.
- **Carry executable Go logic that runs at operation-handling time.** All
  business logic lives in Starlark (DDL `.script`) or cypher (Lens `.spec`). The
  Go code in the package directory exists only to build the install-time write
  set.

## Known limitations

- **No dependency-resolution graph** — a missing dependency warns rather than refuses.
- **No in-flight-instance version pinning** — an in-place upgrade re-projects lenses
  and swaps DDLs immediately; a Loom pattern instance mid-flight is not fenced to the
  DDL version it started on (F-004 follow-on G6, built behind a concrete need).
- **No NATS account-level auth** — the install actor is the filesystem-bound admin credential; substrate-level write enforcement is 🔭 Designed (the ratified NATS account write-restriction hardening — credential seam shipped, enforcement pending).

## CLI

```
lattice-pkg install [--force] [--dry-run] <path-to-package-dir>
lattice-pkg upgrade [--dry-run] <path-to-package-dir>
lattice-pkg uninstall <package-canonical-name>
lattice-pkg list
```

`install` reads the manifest + Go `Definition` and submits the `InstallPackage`
op on a fresh install; on an already-installed package it auto-upgrades on a
version change (`--force` re-applies same-version edits) via the `UpgradePackage`
op (see [Upgrade](#upgrade--in-place-dev-loop-refresh-f-004)). `--dry-run` previews
the create/update/tombstone delta without submitting. `upgrade` is the explicit
upgrade verb (errors if not installed). `uninstall` enumerates from the
`vtx.package.<NanoID>.manifest` aspect and submits `UninstallPackage`. `list`
reads all `vtx.package.>` keys and prints them.

## Authoring a new package — quick reference

1. `mkdir packages/my-package/`
2. Author `manifest.yaml`, `ddls.go`, `lenses.go` (if any), `permissions.go`, `README.md`.
3. Export a single `var Package = pkgmgr.Definition{...}` in `package.go`.
4. Register the package in `cmd/lattice-pkg/main.go`'s install dispatch.
5. Install with `lattice-pkg install packages/my-package`.

See `packages/identity-hygiene/` for the canonical example (DDL + Lens +
permission), or `packages/rbac-domain/` for paired forward/inverse ops.

## Related contracts

- **Contract #1** §1.3, §1.5 — vertex / aspect / link key shapes the install write set must conform to.
- **Contract #8** ([package-install](/docs/contracts/08-package-install.md)) — the `InstallPackage` / `UninstallPackage` op payload + guardrail contract.
- **Contract #6** §6.2 — Capability KV envelope shape (reached via Lens projection, never written directly).
- [`processor.md`](./processor.md#package-install--uninstall) — Processor-side commit + cache-coherence behavior.
- [`refractor.md`](./refractor.md) — the consumer of new Lens meta-vertices.
