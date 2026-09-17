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
- `loftspace-ledger` + `semantic-contracts` — the append-only lease ledger
  (`account` / `transaction`, `LoftspaceCreateAccount` / `DebitAccount` /
  `LoftspaceRecordCharge` / `CreditAccount`, the `ledgerHistory` / `leaseAccounts` Lenses) and the
  Executable Paper package that bills it: a `clause` vertex per provision
  (`CreateClause` / `SupersedeClause` / `InspectPremises` / `BackfillClauseTerm`),
  the `clauseSatisfaction` convergence target (one `DebitAccount` per period,
  gated on a recorded lapse reaching the clause's due date and the due date
  lying inside its `.terms.validFrom`/`validUntil` term; the due dates walk the
  calendar-month grid from `validFrom`, and each recurring charge records the
  period it bills + its due date on its own `.entry` — `periodStart` /
  `periodEnd` / `dueAt` — which `ledgerHistory` and `one-bill`'s rent source
  project so the statement names the month covered) and the `leaseRentSettlement` bootstrap
  (agreed rent → account → a rent clause per tenancy term: the original term
  from `.tenancy.leaseStart`, each signed renewal's from its recorded
  `termStart`/`rentAmount`; a legacy untermed clause is termed from the lease).

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

## Identity claim custody — the registrar holds the secret (accepted doctrine)

A front-desk actor who mints an identity (`CreateUnclaimedIdentity`) mints its claim secret client-side,
Lattice stores only the hash, and the ceremony shows the secret to the desk once to hand over — the
walk-in flow the Gateway claim design ratified (2026-07-06: "the app hands `s` to the prospect out of
band"). That registrar holds the secret by design, and the same custody carries into binding an
**existing** chart to a fresh unclaimed identity (clinic `BindPatientIdentity`): a staffer who kept the
secret could claim the login and read what the patient's own login reads — for the clinic, the encounter
note plaintext the desk's own grants deliberately exclude. **Andrew, 2026-09-12: the trust model is
accepted for a bound chart as it is for registration; the stakes change, the model does not.** What
contains it: a claim runs under the raw credential and refuses one already bound to another identity, so
the abuse needs a fresh credential and leaves three durable traces — the identity's `createdBy` (the
minter), the `identifiedBy` link's provenance (the binder), and the claiming credential in
`credentialindex`. The desk's reach is one chart, and never beyond what that patient's login reads. What
the platform owes is the **undo**, and it is identity-domain's operator-only `RevokeIdentityClaim`: it
reverses a secret-claim in one batch — every bound credential unlinked (its `boundTo` link and
`credentialindex` vertex tombstoned, one `identity.unbound` per credential so the Gateway's
credential-bindings row drops), the identity back to `unclaimed`, a fresh caller-minted claim secret
armed, an armed link secret and the consumer grant retired — and leaves the chart's `identifiedBy` in
place, so the real patient claims the same identity. It is provenance-checked before it writes: only an
identity that carries a *tombstoned* `.claimKey` (minted unclaimed, claimed by secret — never a
Gateway-provisioned credential identity) is admitted, the binding array and the live `boundTo` set must
agree exactly, and each credential's index must name this identity; the `identity.claimRevoked` event
records which credentials were cut and when they had bound. The clinic app offers it to the operator hat
as "Reset login" on a patient with a connected identity; `UnbindPatientIdentity` (the mis-connected-chart
repair) and `RotateClaimKey` (the lost-secret re-issue) keep their `unclaimed`-only guards.
Narrowing the desk (operator-only bind for charts with history) and out-of-band delivery of the secret
were priced and not taken: the first breaks the front-desk Connect-a-login ceremony for exactly the
returning patients it serves; the second replaces a doctrine that keeps Lattice out of the delivery.

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
   `vtx.meta.>` watch and begins projecting. **Expect a first-dispatch lag when a Weaver
   target, its op and the op's grant install together over rows that already exist:** the target's rows
   and the grant's capability-kv row are two lens projections of the same commit (and the target's
   registration is a direct Core-KV watch, faster than either), with no ordering between them, and
   Weaver's actuator is fire-and-forget (it never sees the Processor's `AuthDenied` reply). Rows that
   dispatch before the grant projects sit under their §10.3 mark until the sweep reclaims them at lease
   expiry (`MarkLease`, 30 min by default) and re-dispatches; nothing is lost. To clear it at once —
   safe right after such an install, when every live mark is a denied first attempt — `Revoke` the
   target then `Enable` it: `Revoke` deletes the target's in-flight marks and durable, `Enable` recreates
   the durable and every row re-delivers markless. `ReplayTarget` alone does not help (a live mark takes
   the anti-storm Ack). Triage record: `docs/reviews/verticals-designer-triage-2026-09-10.md` §5.

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
(`agents/steward/SKILL.md` §4). **Capped at 12 one-liners** (`lint-board` fails a dossier past the cap);
an entry RETIRES when a lint/test gate mechanizes it (name the gate, strike the entry — the gate's own
header and `lint-gates.md` carry the rule and its boundary, not this list). An entry is
**class · minting incident(s) · the check that catches it**; a second sighting is appended as a date and a
name, never as a second paragraph, and a class whose check is a gate-shaped rule is mechanized rather than
grown. The full narrative of every entry below is in the minting commit and the item's design doc.

Retired (the gate names the rule; the walk that stays is in the gate's header):
*a confinement guard tested only as the operator has never run* → `lint-workplace-staff-vector` (café `Charge` 2026-09-13; wellness `JoinWaitlist` 2026-09-15) ·
*a lens MATCH edit is a corpus edit* → `internal/refractor/*_corpus_census_test.go` (`go test ./internal/refractor/ -run 'Corpus|Census'`) ·
*a new refusal on an existing op is a claim about EVERY dispatcher* → `lint-seed-declared-reads` (wellness `SessionStarted`, café `TenancyEnded`, 2026-09-14; a `mustAccepted` seed caller still needs the op's new skip by hand) ·
*a `kv.Links` call with no page limit is charged at the 256-unit default* → `lint-links-page-limit` (33 sites, 2026-09-14; the LIMIT is still a cardinality claim — a repointed relation pages with the first-live cursor loop) ·
*a shared-vertex repoint needs a content-and-revision gate against every other writer* → `lint-live-read-pinned-mutation` (six sightings 2026-08-15 → 2026-09-13; a SUBJECT re-derived on the OCC re-execute, an enumeration-shaped cap, and a directOp rewriting a maintained aspect off the Weaver row alone stay walks) ·
*a guard's OCC rests on whoever writes its read declaration* → `lint-derive-reads-bare-vector` (lease-signing, 2026-09-13; which keys an op DELIBERATELY withholds from derivation is stated at the arm).

- **A sensitive aspect whose holder is shredded FAILS the op, never degrades** — a live `kv.Read` dies
  `ErrKeyShredded`/`ScriptFailed` though the envelope stays present with `data.shredded=true`; a DECLARED one faults
  at step-4 hydration before the script runs; deriving a binding whose PRESENCE is the hidden state leaks it on the
  timing axis. Minted: `TombstoneOrphanedCredentialIndex` (2026-08-23); `SignLease` tenant-name (2026-09-13);
  `ClaimIdentity` binding (2026-09-13, +0.08–0.12 ms, CIs excluding zero). Check: declare per-arm from the
  dispatcher's own classification; before an (e)-follow-up read of a sensitive aspect read the holder's `.piiKey`
  and skip on `shredded`, the test shredding BEFORE the op and asserting acceptance; the residual race fails closed
  and loudly; on a timing-uniform path write the binding blind (`claim_timing_probe_test.go` measures the shape).
- **A refusal that reads a key the op never WRITES is advisory unless the racing writer shares a mutated key** —
  the commit path conditions only mutated keys, so `SignRenewal`'s `NoticeGiven` (reads `.notice`, writes `.tenancy`)
  hydrated before a racing `GiveNotice` (writes `.notice`, nothing shared) commits still landed a signed term on a
  lease under notice; the sibling `TenancyEnded` never had the hole because both writers touch `.tenancy`. Minted:
  lease-signing (2026-09-15, caught cold). Check: for every new `fail(` that reads key K, name the writer of K and
  the key set each op mutates — if they share none, the guarded op re-stamps the shared aspect unchanged under OCC
  (`GiveNotice` on `.tenancy`) or the design records the window as accepted.
  Sighting (wellness `ReassignSession/CapacityBelowSeated`, 2026-09-16, caught cold): the shrink reads seat cells and
  writes only `.schedule`; a claim racing into the removed range lands one seat above capacity — recorded as accepted at
  the site, the DDL prose stating the window instead of a guarantee.

- **A dispatch declaration must name what the runtime actually binds** — a playbook `Params` on an OPTIONAL-hop
  column is a Weaver refusal (`strategist.go` "references row.<col>, which is null/absent") on every row where the
  hop misses, the gap open forever and every fixture seeding the hop; a descriptor hub `{actor}` for a walk the
  script runs from a payload key normalizes to the same `vtx.identity.<id>` on the self leg, so the read-drift
  guard measures nothing on the staff leg. Minted: café `cafeArrearsReminders` (2026-09-06, live — the one
  account with no `heldFor` lease was never evaluated); wellness `CreateBooking`'s `heldFor` hub (2026-09-15).
  Check (Params): a value names a column the anchor itself projects, or one the gap's OWN conjunct requires
  non-null (café tab `missing_charge` carries `accountKey <> null`); anything else reached by a walk is resolved by
  the op from state, and one lens pin seeds the anchor with the walk missing. Check (Hub): a declared hub names the
  identity the SCRIPT walks from, and the staff-leg vector resolves it with the payload, not the actor —
  `grep 'Hub: "{actor}"'` on every op whose script walks from a payload key. Sighting (clinic `pastDueAppointments`,
  2026-09-16, caught cold): a script read annotated `(a) declared in contextHint.reads` whose ONLY dispatcher is a
  playbook declaring `Reads: [row.entityKey]` ran on the live seam — the bare `.status` update landed unconditioned.
  Check (Reads): for an op whose sole dispatcher is a playbook, every `(a)`/`(d)` annotation resolves to a
  `Reads`/`OptionalReads` entry on that playbook, never to the test harness's hand declaration.
  Sighting (wellness `wellnessBookingChangeNotices`, 2026-09-16, builder-caught): a level-triggered gap templating
  `row.startsAt` off the OPTIONAL `forSession` walk reads null for the whole call-off window — the gap carries
  `se.schedule.data.startsAt <> null` as its own conjunct.
- **A gap's budget and cadence are derived from its WHOLE loop, not one arm or one window** — a cap summed over
  one arm of a multi-relation sweep under-sizes it (erasure residue caps, 2026-08-24, 2× and 3×); a gap that
  re-opens on a recorded clock lapse mints a successor per window while the prior instance stays live (lease-signing
  `bgcheckFreshnessWindow`, 2026-09-03: 3,637 instances on one identity); a level-triggered gap that stays OPEN
  across successful dispatches with CHANGED params wedges the anchor at the 4th cycle, `GapBudgetExhausted` (clinic
  `visitSeriesDue`, 2026-09-13); a gap whose closing conjunct is FALSE after the op's degenerate arm writes — `validUntil >
  moveOutAt` after the collapse arm set `validUntil = validFrom`, re-dispatched on every mark-lease reclaim and starved
  the sibling clause under `max()` (semantic-contracts `missing_termShortened`, 2026-09-15, two reviewers cold). Check: count the sweep's `collect_*` call sites and pin
  `Σ_arms(pages × pageLimit) / perCommit` against the script's own page constants; price a `validUntil`/`freshUntil` window as a vendor-validity policy over the stack's lifetime and name
  what retires the superseded artifact; for a gap over a growing set prove the FIRST dispatch closes it (aggregate to
  the run's extreme, pin a re-projection after the op's write) — Weaver has no episode boundary of its own; for every
  ARM of the op (no-op, collapse, complete) pin the gap FALSE over the state that arm leaves. Sighting: a lens-computed
  param the dispatched op can refuse on SHAPE (`whole_cents` on a fractional-dollar `depositAmount`) is a gap refused on every
  pass — pin the param's admissible domain at its source (loftspace deposit, 2026-09-15, caught cold). Sighting: a replay budget mirrored as a NUMBER (500) was sized against the live-read budget, not the 250 ms wall — a
  98-entry account aborted, and a ScriptTimeout is a rejection the gap re-dispatches forever (clinic arrears, 2026-09-16,
  live install); size by round trips (≈2.5 ms/entry; CI's 5000 ms wall hides it) and PAGE the walk across dispatches,
  each page closing the gap that dispatched it via alternating phase gaps (arrears resumable replay, 2026-09-16). Sighting (wellness rolling series, 2026-09-16, caught cold): a walk's page bound was sized on "the only writer mints ≤ 52" and a second writer of the walked relation (the rolling extension) made the set unbounded — a page bound is a claim about EVERY writer of the relation, re-derived when one is added, and the off switch for an unbounded set must not walk it (`StopSessionSeries`).
- **A standing `scope=any` write on an entity with no workplace confines by the target's STATE MACHINE, not by
  liveness** — a bind accepting any live identity lets a desk actor attach a stranger's record to their own login and
  inherit its grants (identity-domain confines to `unclaimed` for this reason). Minted: clinic `BindPatientIdentity`
  (2026-09-06, BLOCKING). Check: for every op granted to a staff role that names an actor-bearing vertex by key, list
  the `.state` values the script refuses and ask whether the caller could name themselves.
- **The "leg" of a guard is every op that WRITES the guarded value and every conjunct that READS it — a new
  kind, value or refused set walks all of them.** An amount cap on the self-service leg left the staff leg unbounded
  and a bounded replay ran before the ownership proof (café `CreditCafeAccount`, 2026-09-05); a clock on the
  first-transition op left the terminal→terminal repair op a side door (clinic `NotYetStarted` vs
  `CorrectAppointmentStatus`, 2026-09-13); a `settles` walk had its `reverses` probe on one of the two ops that reach
  it (wellness `ReleaseOrphanedBooking`, 2026-09-13); a `payout` debit passed `type != "debit"` as `reversesRef`
  (café `PayoutCafeCredit`, 2026-09-14); `.decision = lost` left `SignLease` keyed on the mutable premise it replaced
  (2026-09-14); a refusal naming a SET (`VisitNotHeld` = cancelled ∪ noShow) left the sibling lens at `<> 'cancelled'`
  (clinic `RecordEncounter`, 2026-09-14); a landlord self grant on the charge left the FE flow's prerequisite
  `LoftspaceCreateAccount` operator-only (loftspace, 2026-09-15); a notice ended the term at `termEnd` while
  `renewalComplete`'s `open` gate and `staleUserTasks` kept planning toward a signature `SignRenewal` refused
  (lease-signing, 2026-09-15, caught cold). Check: before closing a guard grep the aspect's writers; a guard that reads
  the premise a recorded fact replaces takes the fact as its own conjunct; for a new classification value grep every
  `data.get("<field>")` comparison in the package and every `status.data.value <>` conjunct in every lens gating on
  that status, in every package anchoring the type, deciding each site; for a link a refund/settlement mint walks,
  list every op that can reach the walk and the states each leaves the anchor in — a probe on one exists on all or
  the exclusion is stated per pair; nothing runs before the walk that proves the caller may name the target.
  Second minter: `SupersedeClause` re-mints `.terms` from its payload, so a `purpose` the deposit gaps read was dropped
  or changed on supersede (semantic-contracts, 2026-09-15, caught cold — now equal-or-omitted). Third minter: a self grant
  widened to `confirmed` walked the fee and the clock but not the FIELDS the shared write path stamps — a patient's
  re-confirm re-stamped `.status` and carried a payload `note` over the desk's (clinic, 2026-09-16, caught cold); the
  same fire's Weaver-dispatched terminal op (`MarkPastDueNoShow`) had no clock of its own and swept `noShow` onto a
  just-moved future date. A same-value re-set on a widened leg is the EMPTY batch; a widened leg lists every field the
  write stamps. Fourth minter (clinic `TombstoneAppointment`, 2026-09-16, caught cold): a new claim released through the
  cells' shared release seam inherited the seam's one unguarded caller — a tombstone of an already-terminal visit
  re-released keys a later booking held. Not gate-shaped (every minter is a different mechanism); the walk is the
  brief's part-5 copy: for a value released through a shared seam, list the seam's callers and the state each runs in.
  Fifth minter (café counter payment, 2026-09-16, caught cold): a PLAYBOOK-dispatched leg inherited the human
  leg's balance cap and parked real cash — a dispatcher is a leg, bounded by the fact it dispatches from.
  Sixth minter (arrears checkpoint, 2026-09-16): a writer that writes NOTHING is still a leg of a value it must clear.
- **Two individually-capped clearing verbs compose into a leak neither cap states — name the CONSERVED QUANTITY
  and refuse where the phantom is minted** — waiver (≤ owed) → refund (≤ charge) → payout (≤ credit) handed out cash
  for a charge nobody paid. Minted: cafe-ledger (2026-09-14); closed by `cashCents` on `.balance`. Check: for any
  ledger with more than one clearing verb, maintain the quantity conserved across ALL of them (cash in ≥ cash out) as
  a field and refuse at the verb that would mint the surplus, not the one that spends it.
- **A consumer's exclusion is grounded in the predicate that ENFORCES it — a gap conjunct is its neighbour's own
  claim predicate verbatim, a "by construction" row names the op refusal** — `appointmentReminders` excluded one
  terminal status where `pastDueAppointments` excluded three (2026-09-02, `GapBudgetExhausted` forever);
  `wellnessWaitlistPromotion` counted `status = booked` while its op read seat cells (2026-09-06); `tenancyEnd`'s
  `missing_relist` and `missing_listingLeased` disagreed on "live rival" (2026-09-14, a relist/lease ping-pong); the
  `tenancyEnd` consumer table left `renewalComplete` alone "by construction" while `EndTenancy` walks no renewals
  (2026-09-14). Check: for every `byTarget.<t>` a lens reads, both gates sit on ONE shared status fragment pinned on
  the shipped specs (`TestCapabilityEphemeral_ArmsShareTheirTargetsRelationAndStatusFragment` is the shape); for each
  gap conjunct name the op-side read that answers the same question, and a membership test applied to a neighbour is
  the neighbour's OWN claim predicate, verbatim; for every `freshUntil` a lens projects name the reader of its marker
  (none ⇒ delete the column); for every "cannot happen" exclusion name the refusal in the OP — if the op admits the
  state, the consumer takes the conjunct.
- **A recorded value is read as the FACT it records, never as a proxy for the event it was derived from — and a
  hydrated aspect's absence is two facts, not one.** `BackfillClauseTerm` re-gridded a recorded due by "the period
  containing it" (an immediate double charge, 2026-09-13); `DebitAccount` read `.status ∉ state` as "never charged"
  when `CreateClause` writes it unconditionally (2026-09-13); an untermed charge's `dueAt` was the posting instant
  with the clause's recorded lapse hydrated beside it (2026-09-14); café `CreditHold` read `.arrears.sentAt` — the
  SEND INTENT — as "a reminder went out" (2026-09-15); café read `.tenancy.leaseEnd` as "the tenancy ended" once
  `endedAt = leaseEnd` stopped being an invariant, and a date-only move-out was recorded as the caller's instant (both
  2026-09-15, caught cold). Check: invert the exact derivation of a recorded timestamp,
  never re-interpret it, with a vector whose derived value and source event fall in different periods; a stamp that
  names a date reads the recorded one when it exists; for every `key in state` test on an OptionalRead prove the key
  can be absent for a live vertex, else absence means undeclared → fail closed (the negative vector submits WITHOUT
  the declaration); state the recorded event's exact meaning at the reader; when a fire breaks an invariant two
  fields shared (`endedAt = leaseEnd`), grep every reader of the proxy field repo-wide; a date fact is stored as its
  UTC calendar day. Sightings (loftspace deposit, 2026-09-15, caught cold): `.status.state = completed` read as "charged"
  when a termed monthly clause also completes on its final period — the archetype is a conjunct; `depositHeldCents`
  (charged − returned, custody) read as "unpaid" on the balance line — the ledger attributes no payment to a clause.
  Sighting (clinic `RecordEncounter`, 2026-09-16, caught cold): `amendedAt` = "when the current TEXT was recorded" was
  stamped on a follow-up-only change — name the fact a stamp records and stamp it only when that fact changed.
- **A link key's type segment is what an OUTBOUND walk rebuilds the far endpoint from** — `mint_clause` wrote
  `lnk.clause.<c>.governs.lease.<l>` for a `vtx.leaseapp` target through four fires; the inbound walk bound fine, a
  clause-anchored `(c)-[:governs]->(l:leaseapp)` never could (`adjacency/store.go` `OtherType: dstType`). Check: a
  package test pins the literal link-key string each `make_link` writes against the target's `vtx.<type>`
  (`TestClauseSatisfaction_GovernsLinkKeyNamesTheLeaseappType`); a lens fixture derives a link's endpoint type from
  nothing but the key (`substrate.ParseLinkKey` + `adjacency.EventsForLink`).
- **A field a self-scoped op stores as informational becomes load-bearing the moment another op derives from it —
  validate at the mint, not the reader.** `.terms.leaseTermMonths` / `requestedRent` / `moveInDate` were free text until
  `DecideLeaseApplication` derived the tenancy from them (a zero term ended a lease at approval; `2026-9-15` died
  unnamed). Minted: lease-signing (2026-09-14). Check: when a script starts reading an aspect another op writes, open
  that writer's validation and refuse the malformed shapes THERE (`InvalidTerms`), storing the normalized value; the
  reader keeps a named refusal for rows that predate the check. Second sighting: `rentAmount` / `depositAmount` /
  `requestedRent` became whole-cent-load-bearing when `mint_clause` gained `whole_cents` (2026-09-15, caught cold) —
  the three sources now refuse more than two decimals. Third sighting (clinic `.encounter`, 2026-09-16, caught cold):
  a DDL `InputSchema` `maxLength` is declarative — nothing enforces it — and a history cap's byte arithmetic leaned on
  it; the script now refuses past 4,000 bytes. A bound a design counts on is enforced in the script, never read off
  the schema.
- **A mirror that drops one of the precedent's checks or branches drops the INVARIANT it enforced** (café `CreditCafeAccount{tabRef}`
  2026-09-16, caught cold: the `<x>Ref` mirror inherited the precedent's missing tie to the op's target) — clinic-ledger's
  `reversesRef` kept café's liveness check and dropped its ownership check (a desk waiver could disarm another
  patient's `missing_reversal`; the descriptor field then reached the self leg, 2026-09-14); wellness's arrears
  evaluator kept café's stale/carry branch and dropped `.balance → 0`, and the episode boundary went with it (two
  entries inside one Weaver window fused two episodes, 2026-09-15, BLOCKING); the loftspace mirror of that fix inherited
  its head-as-episode-start approximation (an exact payment of the reminded month re-reminded a never-square tenant)
  and its `accountKey:dueAt` notification key, whose per-head uniqueness was a property of café's `postedAt + term`
  that a RECORDED due date lacks (2026-09-15, both SHOULD-FIX); a checkpoint mirrored from a head that returns
  postedAt + balance was wrong for a head that also returns the order-sensitive `episodeStart` (wellness/loftspace,
  2026-09-16). Sighting: the wellness change-notice op mirrors the reminder op and drops its `ClassAlreadyStarted` guard —
  stated at the site with what refuses upstream (2026-09-16, caught cold). Check: for every precedent branch or
  conjunct the mirror omits, write what it enforced and name the new site that enforces it; for every key component,
  compare and boundary the mirror KEEPS, name what makes it unique / ordered in THIS package's data (mandated vector
  for an episode mechanism: the reminded item retired exactly while the episode continues); for every `<x>Ref`, name
  the relation tying it to the op's target and read that link — deterministic keys through `derive_reads`,
  walk-resolved ones as the `(e)` follow-up with a `read_drift_baseline` row — grepping the `authContextTarget`
  branch for every optional field the descriptor exposes; a sampled-evaluator invariant is proven with events interleaved BETWEEN
  the samples, never one entry per evaluation. Sighting (café `MarkLineServed`, 2026-09-16, caught cold): the
  confinement VECTOR mirrored `VoidCharge`'s and dropped its two forged-`authContext.target` legs, and the "writes
  nothing else" pin named the keys the op touches but not the three the OCC upsert carries — a mirrored test drops a
  precedent's leg the way a mirrored script drops a conjunct; a carry pin enumerates every key of the dict,
  revert-proven by dropping one.

## Related contracts

- **Contract #1** §1.3, §1.5 — vertex / aspect / link key shapes the install write set must conform to.
- **Contract #8** ([package-install](/docs/contracts/08-package-install.md)) — the `InstallPackage` / `UninstallPackage` op payload + guardrail contract.
- **Contract #6** §6.2 — Capability KV envelope shape (reached via Lens projection, never written directly).
- [`processor.md`](./processor.md#package-install--uninstall) — Processor-side commit + cache-coherence behavior.
- [`refractor.md`](./refractor.md) — the consumer of new Lens meta-vertices.
