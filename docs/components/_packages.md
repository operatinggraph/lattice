# Capability Packages

**Status:** Phase 1 (Story 4.6) — initial spec. Phase 2 (Story 5.3+) replaces the substrate-direct installer with an operation-envelope path.

## What a package is

A **Capability Package** is a versioned, atomic bundle of Core KV writes that
adds optional platform behavior *after* bootstrap. Packages are how Lattice
ships business-domain capability (operations, lenses, permissions) without
baking it into the primordial kernel.

Examples:

- `identity-hygiene` (this story) — duplicate-identity detection + operator
  merge.
- Future: `lease-signing`, `work-order`, `payment-reconciliation`, etc.

A package is **NOT** a runtime plugin. It is a *seed bundle*: install-time it
writes meta-vertices, permissions, lens definitions, and grant links to Core
KV. The Refractor and Processor pick those up via the same CDC watches they
use for the primordial DDLs. There is no in-process plugin loading at
operation-handling time; the package's behavior is realized entirely through
Lattice's existing data-plane mechanisms.

## Directory layout

```
packages/<package-name>/
  manifest.yaml          # name, version, dependencies, declared canonical names
  ddls.go                # Go literal definitions of DDL meta-vertices + Starlark scripts
  lenses.go              # Go literal definitions of Lens meta-vertices + cypher source
  permissions.go         # Permission vertices + grant link specs
  README.md              # human-facing description
  integration_test.go    # optional — package-scoped end-to-end tests
```

Packages live at the repo root (`packages/`), not under `_bmad-output/`
(those are planning artifacts) and not in `internal/` (those are private to
the Go module). Repo-rooted because packages are first-class platform
artifacts.

### Why YAML manifest + Go definitions?

The manifest is YAML for readability and tool ergonomics. The DDL / Lens /
Permission definitions are Go because they carry multi-line Starlark scripts
and cypher source — both painful to express as YAML strings. The installer
imports each package's Go files directly via a single exported variable:

```go
// packages/identity-hygiene/identity_hygiene.go
package identityhygiene

import "github.com/asolgan/lattice/internal/pkgmgr"

var Package = pkgmgr.Definition{
    Name:    "identity-hygiene",
    Version: "0.1.0",
    DDLs:    DDLs(),
    Lenses:  Lenses(),
    Permissions: Permissions(),
}
```

The installer (`cmd/lattice-pkg`) imports `packages/identity-hygiene` and
reads `Package`. Future package registries will replace this static import
with a dynamic registry; Phase 1 keeps it mechanical.

## Manifest schema

```yaml
name: identity-hygiene
version: 0.1.0
description: Duplicate-identity detection + operator-approved merge.
depends:
  - identity-domain    # if installed; for Phase 1 this is the in-bootstrap identity DDL
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
- **version**: Phase 1 uses simple string equality for idempotency. Phase 2
  may introduce semver.
- **depends**: declared dependencies on other packages or the in-bootstrap
  domain DDLs. Phase 1 warns-and-proceeds when a dependency is missing;
  Phase 2 will enforce strictly.
- **declares.ddls[]**: each entry maps to one DDL meta-vertex + its four
  canonical aspects (canonicalName, permittedCommands, description,
  script).
- **declares.lenses[]**: each entry maps to one Lens meta-vertex + its
  canonical aspects (canonicalName, spec, adapter, etc.). The Refractor
  auto-picks-up new lenses via its `vtx.meta.>` watch.
- **declares.permissions[]**: each entry maps to one permission vertex +
  N `grantsPermission` links (one per role in `grantsTo`).

## Installation semantics

The installer constructs *all* Core KV writes for the package, then submits
them in a single `substrate.AtomicBatch` call against the `core-kv` bucket.
Cross-bucket atomicity is not supported by NATS atomic batch (Story 1.1) —
packages that would need writes to other buckets are not supported in
Phase 1.

### Steps the installer performs

1. **Read manifest + dependency check.** Phase 1 logs a warning and
   proceeds if a declared dependency is missing.

2. **Idempotency check.** The installer reads
   `vtx.package.<canonical-name-NanoID>`. If a vertex with the same name and
   version is already present, the install is a no-op. If the version
   differs, Phase 1 refuses (Phase 2 will introduce an upgrade path).

3. **Construct the write set:**
   - 1 DDL meta-vertex + 4 aspects per declared DDL (canonicalName,
     permittedCommands, description, script).
   - 1 Lens meta-vertex + ≥3 aspects per declared Lens (canonicalName,
     spec, adapter, plus engine + bucket + class aspects per the lens shape).
   - 1 permission vertex per declared permission.
   - 1 `grantsPermission` link per `grantsTo` entry.
   - 1 `vtx.package.<NanoID>` vertex with `.manifest` aspect carrying the
     full manifest JSON. The manifest aspect is the package's
     uninstall-time recovery handle: it lists every declared canonical name
     and its NanoID so uninstall can enumerate.

4. **Submit one atomic batch.** All writes succeed or all fail.

5. **Auto-discovery.** The Refractor + Processor pick up the new
   meta-vertices via their existing CDC / KV watches. No restart required.

### Provenance

Every primordial-bootstrap-style aspect carries:

- `createdBy: <admin-actor-key>` — the operator credential's identity key,
  read from `lattice.bootstrap.json`.
- `createdByOp: "pkg-install:<package-name>"` — Phase-1 substitute for the
  real operation envelope's traceId.

Phase 2 / Story 5.3 will replace the installer internals with
`CreateMetaVertex` operations submitted through the Processor (capability-
authorized, rollback-able via compensating ops). The directory format and
manifest schema are stable across Phase 1 → Phase 2.

## Uninstall semantics

`lattice-pkg uninstall <package-canonical-name>` performs:

1. Read `vtx.package.<NanoID>` for the package, parse the `.manifest`
   aspect to enumerate every Core KV key the install wrote.

2. Soft-delete each enumerated key via `substrate.AtomicBatch` with
   `isDeleted: true` envelopes. The Refractor reprojects (lens output
   disappears; permissions removed from cap entries within NFR-P3 lag).

3. Soft-delete `vtx.package.<NanoID>` itself last.

Uninstall is **soft-delete only**. Tombstone vertices remain queryable for
audit; physical removal is out of scope.

## Atomicity contract

**Install OR fail entire.** `substrate.AtomicBatch` provides this for free
on the single `core-kv` bucket. If any write fails (revision conflict,
limit, etc.), no writes commit — the package is in the same not-installed
state it was before the call.

## Phase 1 limitations

Document these explicitly because they shape what a package author can do
in 4.6:

- **Substrate-direct.** The installer uses substrate directly, not the
  operation envelope path. This is the "skeleton install" — operator
  credential is just the admin NanoID read from `lattice.bootstrap.json`.
- **No dependency-resolution graph.** Missing dependencies trigger a
  warning, not a refusal.
- **No upgrade path.** Different version on already-installed package
  triggers a refusal.
- **No real NATS auth.** Substituted by filesystem-bound admin credential.
- **Single bucket.** Cross-bucket atomicity isn't available, so packages
  that need writes to other buckets (e.g., seed entries into
  `capability-kv`) are unsupported. (Capability Lens output projection
  reaches `capability-kv` indirectly via the Refractor reprojecting from
  the new Lens meta-vertex — that path works without cross-bucket writes.)

## What a package CANNOT do (Phase 1)

- **Mutate other packages' DDLs.** No `UpdateMetaVertex` of another
  package's vertex.
- **Reach into substrate-level surfaces.** No JetStream stream/bucket
  config changes; no admin auth changes; no event-stream subjects beyond
  what the existing primordial provisioning provides.
- **Override bootstrap-seeded primordial data.** Identity DDL,
  role-management DDL, canonical roles, capability lens are all
  off-limits.
- **Carry executable Go logic** that runs at operation-handling time. All
  business logic must live in Starlark (DDL `.script`) or cypher
  (Lens `.spec`). Go code in the package directory only exists to build
  the install-time write set.

## CLI

```
lattice-pkg install <path-to-package-dir>
lattice-pkg uninstall <package-canonical-name>
lattice-pkg list
```

`install` reads the manifest + Go definitions and submits the atomic
batch. `uninstall` enumerates from the `vtx.package.<NanoID>.manifest`
aspect. `list` reads all `vtx.package.>` keys and prints them.

## Authoring a new package — quick reference

1. `mkdir packages/my-package/`
2. Author `manifest.yaml`, `ddls.go`, `lenses.go`, `permissions.go`,
   `README.md`.
3. Add a single exported `Package = pkgmgr.Definition{...}` in a top-level
   `.go` file in the package.
4. Import the package in `cmd/lattice-pkg/main.go`'s install dispatch.
5. Test with `lattice-pkg install packages/my-package`.

See `packages/identity-hygiene/` for the canonical first example.

## Related contracts

- **Contract #1** §1.3, §1.5 — vertex / aspect key shapes that the
  installer's write set must conform to.
- **Contract #2** §2.1 — operation envelope (Phase 2 path).
- **Contract #6** §6.2 — Capability KV envelope shape (off-limits to
  packages directly; reached via Lens projection).
- `docs/components/refractor.md` — the consumer of new Lens meta-vertices.
- `docs/components/processor.md` — the consumer of new DDL meta-vertices.
