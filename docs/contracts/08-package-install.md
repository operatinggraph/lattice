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
- **Deterministic NanoIDs.** Every entity's NanoID is derived from
  `package name + version + entity tag` (`sha256` → Contract #1 alphabet), so a
  re-install produces identical keys and the create-only batch is idempotent.
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

> **Per-key OCC is deferred.** The script supports `expectedRevision`, but the
> canonical per-subject sequence is only exposed in the committing op's
> `OperationReply.Revisions` (the install reply), not via `KVGet`. Threading the
> install-time committed revisions through to a later uninstall is heavier than
> Story 1.5.5 warranted, so the client tombstones **unconditionally**. Window: a
> concurrent Processor write to a declared key between the client's read and the
> commit is silently overwritten by the tombstone; the batch is still atomic, so
> no partial/mixed state results. See
> `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` (UninstallPackage per-key OCC) for
> the proposed follow-up.

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

## 8.5 Out of scope

- **Version upgrade** (re-install at a new version): a hard "already installed"
  error is returned. The upgrade path is a later story.
- **Per-key uninstall OCC** (§8.3 window): deferred follow-up.
