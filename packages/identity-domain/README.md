# identity-domain Capability Package

Identity vertex creation, claim, and state-machine management.

## Contents

- DDL `identity` (class `meta.ddl.vertexType`) handling 3 operations:
  - `CreateUnclaimedIdentity` (grants → frontOfHouse, backOfHouse, operator)
  - `UpdateIdentityState`     (grants → operator)
  - `ClaimIdentity` (scope=self, grants → consumer)
- 3 permission vertices + 5 `grantedBy` link grants
- 3 user-facing roles (consumer, frontOfHouse, backOfHouse) — role vertex
  + canonicalName/description aspects + a `vtx.roleindex.*` entry each —
  declared in the package definition and created in the install batch.

## State machine

`unclaimed → claimed` via UpdateIdentityState. The `merged` state is
set only by the identity-hygiene package's MergeIdentity script.

## Install

    lattice-pkg install packages/identity-domain

Depends on `rbac-domain` (dependency warning logged; install order is the
operator's responsibility).

The install is ONE atomic commit routed through the Processor as an
`InstallPackage` op (Story 1.5.5): the 3 role vertices + their aspects +
`vtx.roleindex.*` entries, the DDL meta-vertex + aspects, the package
manifest, 3 permission vertices, and 5 grantedBy link grants. Everything
(roles included) lands in the manifest's `declaredKeys`, so uninstall
reclaims it all (closes F-001). Deterministic NanoIDs make re-install
idempotent. See `docs/contracts/08-package-install.md`.

## Architectural notes

- All script reads are known-key only. Duplicate-detection index
  lookups use `crypto.sha256NanoID` to produce stable index keys; the
  caller declares them in `ContextHint.Reads`.
- The DDL script handles CreateUnclaimedIdentity, UpdateIdentityState,
  and ClaimIdentity with known-key reads only.
