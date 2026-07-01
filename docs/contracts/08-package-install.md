# Contract #8 — Capability-Package Install

Capability-package install and uninstall are **kernel operations** routed through
the Processor, not substrate-direct writes. The formal install contract (C3) is
"the `InstallPackage` op envelope carrying the pre-built mutation manifest." This
contract defines the op payloads, the kernel-script guardrails, the atomicity and
cache-coherence guarantees, and the kernel-protection rules.

It builds on [Contract #2 — Operation Envelope](02-operation-envelope.md) (the op
is a normal envelope on the `meta` lane) and
[Contract #3 — Mutation Batch](03-mutation-batch-event-list.md) (the script emits
a mutation batch the Committer applies atomically).

---

## 8.1 Design — thin script, fat manifest

The client (`internal/pkgmgr`) pre-computes the complete mutation set for a
package — every DDL, lens, permission, grant link, declared role, role index,
the package vertex, and the `.manifest` aspect. It ships them as **logical
documents** in the op payload. The kernel script is thin: it iterates the
supplied mutations, enforces guardrails, and emits them. This keeps the
privileged kernel script small and auditable while the (untrusted-but-validated)
mutation computation stays client-side.

- **Logical documents** carry `class`, `data`, `isDeleted` (and for aspects
  `vertexKey`, `localName`; for links `sourceVertex`, `targetVertex`,
  `localName`) — but **no provenance**. The Processor stamps `createdAt`,
  `createdBy`, `createdByOp` at step 8 from the install actor, so installed
  entities carry real provenance authored by the actor that ran the install.
- **Deterministic, version-independent NanoIDs.** Every entity's NanoID is derived
  from `package name + entity tag` (`sha256` → Contract #1 alphabet) — **not** the
  version. The same logical entity (a lens, DDL, role, op-meta — identified by its
  canonicalName / identity tag) therefore keeps the **same** `vtx.meta.<id>` /
  `vtx.<type>.<id>` key across versions, so a version upgrade is an **in-place
  update** of stable keys (§8.6) rather than a re-mint that would orphan the old
  vertices and break every NanoID cross-reference (a WeaverTarget's `lensRef`, a
  permission's `grantedBy` link). A same-version re-install still produces identical
  keys, so the create-only batch stays idempotent. The permission tag keys on
  `operationType + scope` (logical identity), not the list index, so reordering a
  package's permissions does not churn keys.
- **Deterministic `requestId`.** The op `requestId` is derived from
  `name + version` (install) so a re-submit dedup-short-circuits at step 2.

---

## 8.2 `InstallPackage` op

**Envelope:** `lane: "meta"`, `operationType: "InstallPackage"`,
`class: "InstallPackage"`, `actor: <admin/operator identity>`.

**Payload:**

```json
{
  "name": "rbac-domain",
  "version": "0.1.0",
  "mutations": [
    {
      "op": "create",
      "key": "vtx.meta.AbCdEfGhJkLmNpQrStUv",
      "document": { "class": "meta.ddl.vertexType", "isDeleted": false, "data": {} }
    }
  ]
}
```

**Response detail:** `{ name, version, declaredKeys: [<key>, …] }`.
**Event:** `PackageInstalled { name, version, keyCount }`.

### Guardrails (enforced by the kernel script)

`InstallPackage` is privileged, so the script must not be an arbitrary-write
backdoor:

| Guardrail | Rule |
|---|---|
| key-shape | every key matches `vtx.<type>.<id>[.<aspect…>]` or `lnk.<…>`; anything else → `InvalidArgument` |
| system-aspect | no aspect `localName` may start with `_` → `InvalidArgument` |
| create-only | every mutation `op` must be `create` (no updates/tombstones in an install) |

Note: the install/uninstall scripts declare no `ContextHint.Reads`, so they do
**not** perform a protected-key check (their hydrated `state` is empty). Kernel
protected-key enforcement is the Processor commit-time guard (§8.4), which is
authoritative and path-independent. `InstallPackage` is additionally safe by
construction — installs are create-only, so the atomic batch's CreateOnly
condition conflicts on any attempt to overwrite an existing protected root.

### Atomicity + cache coherence (M5/B2)

All mutations land in **one** step-8 atomic batch. The existing step-8
`vtx.meta.*` invalidation fires **in-commit** for the DDL/lens meta-vertices in
that batch, so a class the package just declared is usable immediately on the
same running Processor — **no restart, no manual refresh**.

---

## 8.3 `UninstallPackage` op

**Envelope:** `lane: "meta"`, `operationType: "UninstallPackage"`,
`class: "UninstallPackage"`, `actor: <admin/operator identity>`.

**Payload:** the client reads the package's `.manifest` aspect first, then
submits its `declaredKeys`. Each entry may be a bare key string or a
`{ key, expectedRevision }` object:

```json
{
  "name": "rbac-domain",
  "declaredKeys": [
    { "key": "vtx.meta.AbCdEfGhJkLmNpQrStUv" },
    { "key": "vtx.permission.MnPqRsTuVwXyZ123abcd" }
  ]
}
```

**Response detail:** `{ name, tombstonedKeys: [<key>, …] }`.
**Event:** `PackageUninstalled { name, keyCount }`.

The script tombstones each declared key and (when a key carries an integer
`expectedRevision`) asserts it for OCC. The script itself performs **no**
protected-key check — a tombstone of a protected kernel key is rejected
authoritatively by the Processor commit-time guard (§8.4), not by this script.

> **Per-key OCC (read-time revision).** Before submitting, the client `KVGet`s each
> declared key and passes the entry's revision as its `expectedRevision`. A Core-KV
> key is its own JetStream subject, so `KVGet`'s revision **is** the per-subject
> last-sequence the `Nats-Expected-Last-Subject-Sequence` precondition compares
> against (the same read-time revision the Processor conditions its own §3.2
> updates/tombstones on). If any declared key is concurrently modified between the
> client's read and the commit, the whole atomic batch is rejected
> (`RevisionConflict`) — because the batch is atomic, the package is left **fully
> installed** (never half-uninstalled); re-run the uninstall (the re-read picks up
> the new revision). Conditioning on the *read-time* revision (not the install-time
> one) is what makes a legitimately-upgraded key not spuriously conflict.

---

## 8.4 Kernel protection

The two install DDLs are themselves **protected** primordial meta-vertices, as
are the meta-root DDL, both Capability lenses, the operator role, the primordial
admin identity, and the primordial meta-permissions. Protection is a
`protected: true` field in the **root vertex document `data`** (not a separate
aspect).

**Authoritative guard (Processor commit-time, path-independent).** The
authoritative kernel-protection backstop is the Processor commit step (step 8,
`rejectProtectedMutations` in `internal/processor/step8_commit.go`). For every
`update` or `tombstone` mutation it derives the 3-segment root
(`vtx.<type>.<id>`), `KVGet`s the root document, and **rejects the whole
operation** with error code `ProtectedKey` when `data.protected == true`.
`create` mutations are exempt (create-only already conflicts on overwrite). This
guard is path-independent: it covers `InstallPackage`, `UninstallPackage`,
`UpdateMetaVertex` / `TombstoneMetaVertex`, and any future DDL at once,
regardless of whether the originating script inspected `data.protected`. A root
that does not exist is not protected (allow).

Defense-in-depth (clearer per-op error, **not** authoritative):

- `UpdateMetaVertex` / `TombstoneMetaVertex` (meta-root DDL) reject a target whose
  root `data.protected == true` → `ProtectedMetaVertex: <key>`. This is functional
  and tested (the meta-root DDL declares the target in `ContextHint.Reads`), but
  the Processor commit-time guard above is the authoritative backstop.

This closes the 1.5.2 kernel-protection residual: an operation cannot disable auth
(the Capability lens) or the kernel (the meta-root DDL) by rewriting or tombstoning
it.

---

## 8.6 `UpgradePackage` op

In-place version upgrade (and dev-mode same-version re-apply). The client reads the
installed package's `.manifest.declaredKeys` (the **old** key set), rebuilds the
**new** manifest with the same logical-document machinery as install (§8.1, on the
now version-independent keys), **diffs by key**, and ships the delta as a single
mixed-mutation op:

- a key in **new \ old** → `create`
- a key in **new ∩ old** whose body **changed** → `update` (a byte-equal body is
  omitted — no needless re-stamp / re-rebuild)
- a key in **old \ new** → `tombstone`

Because keys are version-independent (§8.1), a surviving entity keeps its key, so the
upgrade is a true in-place update; every NanoID cross-reference stays valid.

**Envelope:** `lane: "meta"`, `operationType: "UpgradePackage"`,
`class: "UpgradePackage"`, `actor: <admin/operator identity>`.

**Payload:**

```json
{
  "name": "clinic-domain",
  "fromVersion": "0.1.0",
  "toVersion": "0.2.0",
  "mutations": [
    { "op": "update",    "key": "vtx.meta.AbCd…", "document": { "class": "meta.lens", "data": {} } },
    { "op": "create",    "key": "vtx.meta.WxYz…", "document": { "class": "meta.ddl.vertexType", "data": {} } },
    { "op": "tombstone", "key": "vtx.permission.MnPq…", "document": { "isDeleted": true, "data": {} } }
  ]
}
```

`fromVersion == toVersion` is a legal **dev-mode re-apply** (force same-version),
producing only `update` mutations for changed bodies. The op `requestId` is derived
from `name + fromVersion + toVersion`, so distinct upgrades dedup independently while
a re-submit of the same upgrade short-circuits.

**Response detail:** `{ name, fromVersion, toVersion, created: [<key>…], updated: [<key>…], tombstoned: [<key>…] }`.
**Event:** `PackageUpgraded { name, fromVersion, toVersion, createdCount, updatedCount, tombstonedCount }`.

### Guardrails (enforced by the kernel script)

Same key-shape + underscore-aspect rejection as install (shared `installGuardrailHelpers`).
`op` must be one of `create` / `update` / `tombstone`. **Unlike install, `UpgradePackage`
is not create-only**, so it is not safe-by-construction; it relies on the **authoritative
Processor commit-time protected-key guard** (§8.4, `rejectProtectedMutations`), which already
covers every `update` / `tombstone` "regardless of … the originating script" — an upgrade
therefore cannot rewrite or tombstone a protected kernel / auth root.

### Atomicity + cache coherence

All create/update/tombstone mutations land in **one** step-8 atomic batch (all-or-nothing —
no half-migrated package), and the step-8 `vtx.meta.*` invalidation fires in-commit for every
touched DDL/lens meta-vertex, so the new definitions are usable immediately (no restart).
Downstream reaction is the **existing** CDC machinery: Refractor's `CoreKVSource` observes a
changed lens `.spec` and `ClassifyUpdate` selects a hot-swap (INTO-only) or a **full rebuild**
(MATCH change — which **evicts** rows the new cypher no longer matches, discharging the Epic-12
"anchor no longer matches WHERE → tombstone on in-place upgrade" obligation); the Weaver /
Loom registries reload changed targets / patterns from their CDC sources.

### OCC

`update` / `tombstone` are conditioned on the **read-time revision**, the same as uninstall
(§8.3): the diff already `KVGet`s each surviving key for the body comparison, and a removed key
is read to capture its revision, so each mutation asserts the revision it was read at as its
`expectedRevision`. A concurrent Processor write to a declared key between the diff read and the
commit fails the whole atomic batch (`RevisionConflict`); the batch is atomic, so the package is
left at its **pre-upgrade** version (never half-migrated) — re-run the upgrade to resolve.

---

## 8.7 Out of scope

- **In-flight-instance DDL-version pinning** (a breaking DDL change during an upgrade while a
  Loom instance is mid-pattern or a Weaver gap is open): warned/blocked by a future migration
  guard (brainstorm G6). The upgrade is atomic but does not today fence in-flight orchestration.
