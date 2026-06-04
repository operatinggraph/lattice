# Capability Packages

**Component reference** | Audience: package authors + architects | Last verified: 2026-06-03

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

- `identity-domain` — the identity vertex type + create/claim/state-machine ops.
- `rbac-domain` — roles, permissions, and the assign/grant ops + their inverses.
- `identity-hygiene` — duplicate-identity detection (`duplicateCandidates` Lens)
  + operator-approved merge (`MergeIdentity` op).
- `orchestration-base` (Phase 2) — the generic `task` DDL + `CreateTask` op
  (assignee required + validated, no-orphan by construction) + the package-owned
  `capabilityEphemeral` Lens. The lens re-sources FR56 ephemeral task grants out
  of the bootstrap god-cypher into the disjoint key `cap.ephemeral.<actor>` (the
  (a1) extraction, Contract #6 §6.6 / Contract #10 §10.7) — the first
  proof-of-pattern for the contract-contribution model (core owns the
  capability-kv bucket + step-3 reader; a package projects the grant type it
  owns into a disjoint key space). Step-3's task-dispatch branch reads the new
  key as a single GET, no fallback.

Phase 2 also adds `lease-signing` (the Loftspace reference vertical).

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

import "github.com/asolgan/lattice/internal/pkgmgr"

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

## Installation semantics

Install and uninstall route **through the Processor** as the two primordial
kernel operations `InstallPackage` / `UninstallPackage` — packages do not write
to the substrate directly. The flow:

1. **Build the write set.** `internal/pkgmgr` reads the package `Definition` and
   pre-computes the complete mutation set — every DDL/lens/permission/grant key —
   as **logical documents** (`{class, data, isDeleted}`, no provenance).
2. **Idempotency check.** The op is keyed so a re-install of the same name +
   version is a no-op; a different version fails closed (no in-place upgrade
   path yet).
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
- **No in-place upgrade** — a different version on an already-installed package fails closed.
- **No NATS account-level auth** — the install actor is the filesystem-bound admin credential; substrate-level write enforcement is a Phase 2 hardening.

## CLI

```
lattice-pkg install <path-to-package-dir>
lattice-pkg uninstall <package-canonical-name>
lattice-pkg list
```

`install` reads the manifest + Go `Definition` and submits the `InstallPackage`
op. `uninstall` enumerates from the `vtx.package.<NanoID>.manifest` aspect and
submits `UninstallPackage`. `list` reads all `vtx.package.>` keys and prints them.

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
