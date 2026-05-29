# rbac-domain Capability Package

Role / permission / grant management operations.

## Contents

- DDL `rbac` (class `meta.ddl.vertexType`) handling 10 operations:
  - `CreateRole`, `UpdateRole`, `TombstoneRole`
  - `CreatePermission`, `UpdatePermission`, `TombstonePermission`
  - `AssignRole`, `RevokeRole`
  - `GrantPermission`, `RevokePermission`
- 10 permission vertices (one per op), each granted to the `operator` role
  via a `grantedBy` link.

## Link conventions

- `lnk.<actorType>.<actorId>.holdsRole.role.<roleId>` — actor source,
  role target. Actors are added later in graph growth.
- `lnk.permission.<permId>.grantedBy.role.<roleId>` — permission source,
  role target. Reads as "permission granted by role". The
  `GrantPermission` / `RevokePermission` operation verbs follow
  operator-action semantics and are distinct from the link's canonical
  name.

## Install

    lattice-pkg install packages/rbac-domain

The operator role must already exist in the kernel (it is the sole
primordial role).

## Architectural notes

- All script reads are by known key; no scans, no adjacency reads, no
  lens-output reads.
- For Assign / Grant ops the link key is deterministic from inputs; the
  script reads it by known key to make the op idempotent.
- Canonical-name uniqueness is NOT enforced in the write path. Operators
  who need uniqueness gate it upstream.
