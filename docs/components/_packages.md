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
  retentionClasses:
    - canonicalName: clinicalRecord
      policy: eraseOnExpiry
      retentionPeriod: P7Y
```

Field semantics:

- **name**: unique identifier; matches the directory name.
- **version**: simple string equality for idempotency (semver is a future option).
- **depends**: declared dependencies on other packages. A missing dependency is
  logged as a warning and the install proceeds; strict enforcement is a future
  option.
- **declares.ddls[]**: each entry maps to one DDL meta-vertex + its canonical
  aspects (canonicalName, description, and — for an ordinary, non-abstract
  DDL — permittedCommands, script). A DDL may instead declare:
  - **abstract**: marks the type as naming no instance (dynamic-type-taxonomy-
    design.md §3.2) — usable as a lens pattern label or a `subtypeOf`
    ancestor, never as the class of a written document or a key's type
    segment. Legal only alongside `class: meta.ddl.vertexType`; mutually
    exclusive with `permittedCommands`/`script`, which an abstract DDL emits
    neither of. Declaring `abstract: true` over a type that still has a live
    instance is refused at install/upgrade time.
  - **subtypeOf**: the canonicalName of the type this DDL is a subtype of.
    The target may be concrete or abstract (a concrete type may have
    subtypes too). The installer resolves it — batch-local first (another
    DDL in the SAME package), then against the already-installed kernel —
    and fails the install closed when the name does not resolve to a live,
    non-tombstoned `meta.ddl.vertexType` meta-vertex. This needs no
    cooperation from the target's owning package: a package may declare
    `subtypeOf` against a type it does not itself own. The resulting
    `subtypeOf` link lands in the declaring package's own `declaredKeys`, so
    uninstalling that package cleans the link up (uninstalling the TARGET's
    owning package instead leaves the link pointing at a tombstoned
    meta-vertex, which subsequent reads treat as non-contributing). The
    installer also refuses a `subtypeOf` graph that is cyclic or requires an
    upward walk deeper than 4 hops.
  - **leafBudget**: (abstract types only) the abstract type's own promise about
    how large its transitive concrete-leaf set may grow — the bound a dependent
    lens prices its narrowed-filter label cap against. It has two consumers,
    landing on deliberately different actors:
    - A **leaf installer** that pushes the transitive count past the budget is
      **WARNED, never rejected** — one package's lens narrowing must never veto
      another package's type declaration.
    - A **lens author** whose own lens cannot fit `K + Σ leafBudget ≤ 8` is
      **REFUSED at their own install**, where they can act on it
      (`ErrLensLabelCap`). `K` is the lens's referenced labels minus its whole
      expansion set, and the gate engages only for an *exhaustive* lens carrying
      a `*` sigil. The remedy is to rewrite a redundant concrete label as the
      sigil, or to ask the abstract type's owner for a smaller budget — **not**
      to delete the label, which clears exhaustiveness and makes the lens broad.
    An abstract type that declares no budget takes the whole cap (8), which
    forces `K = 0` for every consuming lens; the installer warns when a
    declaration omits it.
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
- **declares.retentionClasses[]**: each entry maps to one retention-class key
  holder the package's own Go `Definition.RetentionClasses` declares
  (retention-class-key-custody-design.md §3.1) — a `vtx.retentionclass.<NanoID>`
  root + `.retentionPolicy` aspect a DDL's aspect types name via
  `Custody.RetentionClass`. **Mandatory** whenever the package declares any
  `RetentionClasses`: `VerifyAgainstDefinition` fails the count when the
  manifest's list and the Definition's disagree in length, so an author who
  adds a Go-side retention class without adding the matching manifest entry
  fails verification, not silently ships undocumented. `canonicalName` is the
  entry's identity; `policy` and `retentionPeriod` are the actual data-
  controller obligation (currently `eraseOnExpiry` is the only implemented
  `policy`, and `retentionPeriod` is an ISO-8601 duration) and are compared
  too — a manifest that agrees on `canonicalName` alone but not on what the
  class's obligation actually is would let that obligation drift with a
  zero-line manifest diff, defeating the one construct whose whole purpose is
  being the reviewable statement of it.

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

`lattice-pkg uninstall [--retire-secure-lens '<lens>=<note>']... <package-canonical-name>`
reads the package's `.manifest`
aspect (`declaredKeys`) and submits `UninstallPackage`, which tombstones each
declared key (cascade-style) except a `vtx.retentionclass.*` holder — those the
client excludes from the payload and reports back instead, since tombstoning one
would put the class DEK it custodies beyond `ShredRetentionClassKey` forever
([Contract #8 §8.3](/docs/contracts/08-package-install.md)) — and **rejects any
protected key** (defense in depth). Uninstall is soft-delete only — tombstoned vertices remain queryable for
audit; physical removal is out of scope. The Refractor reprojects (lens output
disappears; permissions drop out of cap entries within NFR-P3 lag).

Each tombstone carries the `expectedRevision` the client's own read observed, so
the batch is per-key OCC ([Contract #8 §8.3](/docs/contracts/08-package-install.md)):
a concurrent write to any declared key between that read and the commit fails the
whole batch loudly (`ErrUninstallConflict`) rather than being silently overwritten,
and the package is left fully installed — never a partial state.

### Secure-Lens key custody: the operator attests, or the uninstall is refused

A Secure Lens's `targetConfig.secureColumns` is the only record of which key
holders a target store's ciphertext was written under, and Refractor's
destruction-readiness oracle (`internal/refractor/health/registry_probe.go`)
answers "which lenses hold ciphertext for this holder type?" from the live
registry alone. Tombstoning such a lens erases that record while every row it
encrypted stays in the target store, so the oracle would go on attesting
destruction coverage over rows it can no longer see.

Uninstall therefore **refuses** to erase a lens the oracle can still see unless
the operator attests the retirement, per lens, with a note
(`--retire-secure-lens '<lens canonicalName>=<why this history is safe to stop
carrying>'`, repeatable; `retiredSecureLenses` on Loupe's endpoint). The
attestation is the operator's, supplied at the call site — never
`Definition.RetiredSecureColumns`, which is the package author's declaration for
the [Upgrade](#upgrade--in-place-dev-loop-refresh-f-004) path: a package able to
pre-declare its own uninstall retirement would ship the excuse for its own
erasure. The platform verifies nothing about the ciphertext; the note is the
only record of who decided it was safe.

A lens the oracle **already** could not see needs no attestation — its vertex
root absent, not `meta.lens`, or soft-deleted, or its spec an `eventStream`
source. Those are reported as `secureColumnsAlreadyErased`: pre-existing damage
this uninstall neither caused nor can undo, and gating on it would make that
damage un-uninstallable.

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
- a key only in the old → **tombstone** (sorted) — except a `vtx.retentionclass.*`
  holder, which a dropped or renamed class leaves live-but-undeclared for the same
  reason uninstall does ([Contract #8 §8.6](/docs/contracts/08-package-install.md)),
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
**durable** subscription over `vtx.meta.>` and `lnk.meta.*.subtypeOf.>` (one
consumer, dynamic-type-taxonomy-design.md §6.1) for the life of the process
(`internal/refractor/lens/corekv_source.go`),
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
lattice-pkg uninstall [--retire-secure-lens '<lens>=<note>']... <package-canonical-name>
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
2. Author `manifest.yaml`, `ddls.go`, `lenses.go` (if any), `permissions.go`,
   `opmetas.go` (a full `OpMetaSpec` — Presentation + InputSchema +
   FieldDescriptions + Dispatch — for every op a person may trigger), `README.md`.
3. Export a single `var Package = pkgmgr.Definition{...}` in `package.go`.
4. Register the package in `cmd/lattice-pkg/main.go`'s install dispatch.
5. Install with `lattice-pkg install packages/my-package`.

See `packages/identity-hygiene/` for the canonical example (DDL + Lens +
permission), or `packages/rbac-domain/` for paired forward/inverse ops.

The normative bar is the Vertical Package Standard
(`_bmad-output/implementation-artifacts/vertical-package-standard.md`, S1–S10) —
its mechanical subset is CI-blocking via `scripts/lint-package-standard.go`
(descriptor completeness, structure pins, manifest hygiene, pinned guard
helpers) and `scripts/lint-app-op-descriptors.go` (the app seam: an op a
`cmd/*-app` wires UI to is user-facing by demonstration and must be described
here, in its owning package — `[no-op-meta: <code> — <reason>]` in the
permission Note is the only exemption, from the gate's closed vocabulary).
The descriptor idiom to copy is `packages/clinic-domain/opmetas.go`.

## Review keeps catching (dossier)

The recurring review-finding classes for package authoring — fire briefs copy the applicable entries into
part 5 (`agents/fire-brief-template.md`), the item-close review appends new ones
(`agents/steward/SKILL.md` §4). **Capped at 12 one-liners**; an entry RETIRES when a lint/test gate
mechanizes it (name the gate, strike the entry).

- **A cross-package type guard must survive the migration window in BOTH directions** — when a type's class
  or key shape changes, the old and new populations are live simultaneously and nothing rewrites the old
  documents, so a guard admitting only one is a silent outage on the other. Minted: dynamic-type-taxonomy B1
  (`cls == "location"` would have rejected all 69 live locations; the accepted-widening arm was then unpinned
  in 4 of 7 packages, so narrowing it back left the whole suite green). Check: every guard needs a *positive*
  vector per live shape, not just a negative — and mutation-test each by narrowing the set.
- **Census the CHECK, not the wrapper** — a generic helper (`require_live_typed(…, "location")`) reaches the
  same guard as the named wrapper, so a grep for wrapper names undercounts. Minted: dynamic-type-taxonomy §9.2
  (8 sites / 5 packages claimed; 19 / 7 real, plus two JavaScript submit sites no Go census would find).
  Check: grep the predicate and the error string, then re-derive at Phase 0.
- **A shared-vertex repoint needs a content-and-revision gate against EVERY other writer of that vertex, not
  just atomicity within its own batch.** A script's own mutation batch being atomic (CAS-guarded) proves
  nothing about a DIFFERENT op racing the same key between that op's own live read and its commit — the
  gap is closed only by (a) checking the read's *content* still matches what justified the mutation and
  (b) pinning the write to *that same read's revision*, not a later step-8 fallback re-read. Minted:
  identity-domain's `CreateUnclaimedIdentity` identityindex repoint (2026-08-15) — two independent cold
  reviews found `PurgeIdentityDedupFootprint`'s sweep and `MergeIdentity`'s repoint could each destroy or
  steal a vertex the repoint had just legitimately claimed. Check: for any script that reads a vertex live
  (`kv.Read`, read-posture (e)) then conditionally mutates it, grep for `expectedRevision` on that exact
  mutation — its absence, or a bare content check without the revision pin, is the defect.
  **Second sighting (2026-08-23), and the precedent was the carrier:** `unbind_identity_credentials.go`'s
  `owner_binding_rewrite` was the shape a new op was told to mirror, and it had no pin — so the mirror
  would have inherited the defect had it copied instead of checking. `applyHydratedRevisions` supplies a
  revision only for keys a DISPATCHER declared, and step 8's own prior-document read happens *after* the
  script filtered the array, so it closes the window it measures rather than the one that matters. Both
  ops now pin, each proven by dropping the pin and watching the racing write be accepted instead of
  conflicting. **Mechanize on the next sighting.** Third sighting (café `RefundCafeCharge`, 2026-09-05):
  a refund ceiling computed from a paged `reverses` enumeration let two concurrent refunds jointly exceed the
  charge — closed by keeping the tally as a field on the reversed charge's own declared `.entry` read and
  pinning the upsert to that read's revision; the enumeration-shaped cap is the tell. Fourth sighting (clinic `BindPatientIdentity`,
  2026-09-06): the `.demographics` rewrite used the unconditioned upsert while its own comment claimed a pin —
  caught in the fix round; `make_aspect_update_occ` now carries `demo.revision`.
- **A declared sensitive read is decrypted BEFORE the script runs, so declaring it unconditionally can
  break the very population the op exists for** — step 4 hydrates every declared aspect, and a sensitive
  one decrypts under its owner's DEK. An op whose whole purpose is cleaning up after an erased owner
  faults at hydration (`vault: identity key shredded`) if it declares that owner's aspect on the arm where
  the owner is dead. Declare per-arm, from the dispatcher's own classification, and make the residual race
  fail closed and loudly. Minted: `TombstoneOrphanedCredentialIndex`'s owner-array rewrite (2026-08-23) —
  nine tests fell to the unconditional declaration. Check: for any `optionalReads` naming a `Sensitive:
  true` aspect, ask which arm reaches it with the holder's key already destroyed.
- **An engine-recognized companion column whose name does not match its gap is silently dead** — the engine
  derives the name from the gap key (`missing_<g>` → `maxretries_<g>`/`inflight_<g>`), finds nothing, and
  falls back to its default; no gate, test or projection notices, and the package's own doc keeps claiming
  the column works. Minted: `wellness-ledger` projected `maxretries_price` for gap `missing_price_charge`
  (2026-08-24) — dead since it shipped, harmless only because the default budget happened to equal the
  intended cap. Check: build the column name from the gap key in code, never spell it by hand; a column
  with no gap of the same name is dead unless a named non-Weaver reader consumes it.
- **A cap derived from a paged sweep must be summed over every ARM the gap covers, not one arm's reach** —
  a sweep that drains one relation per commit, in fixed order, needs `Σ_arms(pages × pageLimit) / perCommit`
  dispatches; a drained arm returns empty and yields to the next rather than failing, so dispatches past
  one arm's ceiling are still progress. Minted: the erasure residue caps (2026-08-24) were derived from a
  single arm and under-sized 2× and 3×. Check: count the sweep's `collect_*` call sites, and pin the
  constant against the script's own page constants rather than asserting it equals itself.
- **A standing `scope=any` write on an entity with no workplace must confine by the target's STATE MACHINE, not by
  liveness** — a bind that accepts any live identity lets a front-desk actor attach a stranger's record to their own
  claimed login and inherit its read grants; identity-domain already confines its front/back-of-house writes to
  `unclaimed` for exactly this reason. Minted: clinic `BindPatientIdentity` (2026-09-06), caught cold as BLOCKING.
  Check: for every op granted to a staff role that names an identity (or any actor-bearing vertex) by key, ask
  which `.state` values the script refuses and whether the caller could name themselves.
- **A new op granted to `operator` by its OWN package is not thereby callable from the console** —
  `cmd/loupe` runs as the scoped `consoleOperator` (mechanism B, "never root"), whose grants live in a
  DIFFERENT package (`console-operator`), and there is no wildcard: an op absent from that list is denied
  at the CapabilityAuthorizer, so the feature no-ops while the endpoint still answers 200. Nothing fails —
  every package-local pin, the corpus census and CI all stay green, because the grant gap is cross-package.
  Minted: `RecordCapabilityInstallReceipt` (2026-08-29) shipped granted only to `operator`, so the receipt
  would never have landed from the console. Check: for every new op a `cmd/loupe` handler submits, assert
  it appears in `console-operator`'s grant list — and have the submitting handler surface its own failure
  on the SUCCESS path, so a denial is visible instead of swallowed.
- **A convergence gap that re-opens on a recorded clock lapse mints a new instance every window — the retry budget counts failures, not successful cycles, so a demo-cadence constant in a long-lived stack is a runaway.** Minted: lease-signing 2026-09-03 — a five-minute production `bgcheckFreshnessWindow` produced 3,637 background-check instances on one identity in a month (12,281 on seven), each lapse re-opening `missing_bgcheck` and `triggerLoom` minting a successor while the prior instance stayed live; the lens aggregating over them then scanned all N per event and its rebuild could not drain. Check: for every gap whose closing artifact carries a `validUntil`/`freshUntil`, state the window as a vendor-validity policy and price the loop at that cadence over the stack's lifetime; and ask what retires the superseded artifact — an instance nothing tombstones is unbounded growth (`Tombstone*` commands exist for patient/provider/appointment/location, none for a service instance).

- **A lens MATCH edit is a corpus edit — the refractor census pins move even when every package test is green.**
  `internal/refractor`'s corpus tests pin, per lens, the branch decomposition, the sibling-group population and the
  label set / filter mode; an added OPTIONAL MATCH hop changes all three and nothing in the package's own suite,
  `lint-lens-anchors` or the app tests notices. Minted: café `cafeLedgerHistory` refund columns (2026-09-05) — CI
  reddened on three pins after a green local run of every package gate. Check: any edit to a lens `Spec` runs
  `go test ./internal/refractor/ -run 'TestCorpus|Census' -count=1` before merge and re-pins deliberately, stating
  in the commit why each verdict moved.

- **A guard's OCC rests on whoever writes its read declaration.** `contextHint` is submitter-supplied and never
  enforced, so a cap that reads a maintained aspect declared only in a descriptor's `optionalReads` is hydrated —
  and its bare update revision-conditioned — only for callers who repeat the declaration; a caller that omits it
  gets a live read and a last-write-wins update under concurrency. Minted: café `CreditCafeAccount`'s `.balance`
  cap (2026-09-05), caught cold; clinic-ledger carried the identical shape from the precedent it mirrored. Check:
  any key a script's correctness depends on being HYDRATED (an OCC-conditioned update, a read-before-create) is
  returned by the script's own `derive_reads`, and one test submits with an empty `contextHint`.
- **An amount cap written for the self-service leg leaves the staff leg unbounded, and a bounded replay hoisted
  above the ownership proof is an amplification primitive.** The trust argument ("no rail verifies the payment")
  is a property of the op, not of who submitted it; and a history replay that runs before the walk proving the
  caller may name the account lets any grant-holder bill ~500 live reads to a stranger's key. Minted: café
  `CreditCafeAccount` (2026-09-05). Check: ask which leg the guard is NOT on, and what runs before the proof that
  names the target.
## Related contracts

- **Contract #1** §1.3, §1.5 — vertex / aspect / link key shapes the install write set must conform to.
- **Contract #8** ([package-install](/docs/contracts/08-package-install.md)) — the `InstallPackage` / `UninstallPackage` op payload + guardrail contract.
- **Contract #6** §6.2 — Capability KV envelope shape (reached via Lens projection, never written directly).
- [`processor.md`](./processor.md#package-install--uninstall) — Processor-side commit + cache-coherence behavior.
- [`refractor.md`](./refractor.md) — the consumer of new Lens meta-vertices.
- **A lens that reads a RECORDED fact depends on whoever arms the timer that records it — couple the two
  populations in one fragment, and never host a neighbour's window on an anchor nothing reads.** Two shapes
  in one item (expiry-as-a-recorded-fact, 2026-09-02). (a) `appointmentReminders` closed its gap on
  `pastDueAppointments`' recorded end but excluded one terminal status where the past-due lens excluded
  three, so a `completed`/`noShow` never-reminded appointment held a gap no timer could ever close —
  `GapBudgetExhausted` per row, forever (found by the close pass; the lens tests exercised `scheduled` ×12).
  (b) `leaseApplicationComplete` and `renewalComplete` each projected a `freshUntil` computed from the
  background-check INSTANCE's window, so their own timers marked the wrong vertex and, once the instance
  recorded its own lapse, fired into a marker no cypher read. Check: for every `byTarget.<t>` a lens reads,
  name the lens that projects `freshUntil` for target `t`, assert both gate on ONE shared status fragment
  (pin it on the shipped specs, not on the constant), and for every `freshUntil` a lens projects, name the
  reader of the marker it will produce — none ⇒ delete the column. The same coupling binds a lens to the OP
  its gap dispatches: every conjunct of `missing_<g>` must count the population the op's own test reads
  (`wellnessWaitlistPromotion` counted `status = booked` while `PromoteWaitlistedBookings` read seat cells —
  a class that ran and was rescheduled opened a gap the op could only decline, `GapBudgetExhausted` forever;
  2026-09-06 close pass). Check: for each gap conjunct, name the op-side read that answers the same question.
