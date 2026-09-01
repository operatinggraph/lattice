# Contract #8 — Capability-Package Install

Capability-package install and uninstall are **kernel operations** routed through
the Processor, not substrate-direct writes. The formal install contract (C3) is
"the `InstallPackage` op envelope carrying the pre-built mutation manifest." This
contract defines the op payloads, the guardrails, the atomicity and
cache-coherence guarantees, and the kernel-protection rules.

It builds on [Contract #2 — Operation Envelope](02-operation-envelope.md) (the op
is a normal envelope on the `meta` lane) and
[Contract #3 — Mutation Batch](03-mutation-batch-event-list.md) (the op commits
a mutation batch atomically).

---

## 8.1 Payload model — pre-computed logical documents, deterministic keys

The submitter pre-computes the **complete mutation set** for a package — every
DDL, lens, permission, grant link, declared role, role index, the package
vertex, and the `.manifest` aspect — and ships them as **logical documents** in
the op payload.

- **Logical documents** carry `class`, `data`, `isDeleted` (and for aspects
  `vertexKey`, `localName`; for links `sourceVertex`, `targetVertex`,
  `localName`) — but **no provenance**. The platform stamps `createdAt`,
  `createdBy`, `createdByOp` from the install actor at commit, so installed
  entities carry real provenance authored by the actor that ran the install; a
  submitted provenance value is never honored.
- **Deterministic, version-independent NanoIDs.** Every entity's NanoID is derived
  from `package name + entity tag` (`sha256` → Contract #1 alphabet) — **not** the
  version. The same logical entity keeps the **same** key across versions, so a
  version upgrade is an **in-place update** of stable keys (§8.6) and every NanoID
  cross-reference stays valid; a same-version re-install produces identical keys,
  keeping the create-only batch idempotent. The permission tag keys on
  `operationType + scope` (logical identity), not the list index, so reordering a
  package's permissions does not churn keys.
- **Deterministic `requestId`.** The op `requestId` is derived from
  `name + version` (install), so a re-submit dedups as the same operation.

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

### Guardrails

`InstallPackage` is privileged, so it must not be an arbitrary-write backdoor:

| Guardrail | Rule |
|---|---|
| key-shape | every key matches `vtx.<type>.<id>[.<aspect…>]` or `lnk.<…>`; anything else → `InvalidArgument` |
| system-aspect | no aspect `localName` may start with `_` → `InvalidArgument` |
| create-only | every mutation `op` must be `create` (no updates/tombstones in an install) |

Kernel protected-key enforcement is the commit-time guard (§8.4) — authoritative
and path-independent. `InstallPackage` is additionally safe by construction:
installs are create-only, so an attempt to overwrite any existing root (protected
or not) conflicts rather than committing.

### Atomicity + cache coherence

All mutations commit as **one atomic operation**, and a class the package just
declared is usable immediately on the same running platform — **no restart, no
manual refresh**.

---

## 8.3 `UninstallPackage` op

**Envelope:** `lane: "meta"`, `operationType: "UninstallPackage"`,
`class: "UninstallPackage"`, `actor: <admin/operator identity>`.

**Payload:** the submitter reads the package's `.manifest` aspect first, then
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

Each declared key is tombstoned; a key carrying an integer `expectedRevision`
asserts it for OCC. A tombstone of a protected kernel key is rejected by the
commit-time guard (§8.4).

**Exception: a retention-class holder is never submitted for tombstone.** A
`vtx.retentionclass.<NanoID>` key holder (root + its `.retentionPolicy` aspect)
is excluded from the submitted `declaredKeys`. Its DEK may be destroyed only by
`ShredRetentionClassKey`, whose target-existence guard refuses an
already-tombstoned holder — tombstoning the holder here would strand the class
key it custodies beyond any reach, permanently. The excluded keys are **reported
to the caller, never silently dropped**, in two distinct buckets: **preserved**
(left live in Core KV — undeclared, but still shreddable on the controller's own
retention schedule) and **already-stranded** (found already tombstoned: the
class key it custodies is past every destruction path, and an operator carrying
that retention obligation needs to see it named rather than counted as intact).
Every other declared key (DDL/lens/permission/grant/role/aspect) tombstones
normally.

> **Per-key OCC (read-time revision).** Before submitting, the submitter reads
> each declared key and passes its current revision as `expectedRevision`. If any
> declared key is concurrently modified between that read and the commit, the
> whole atomic batch is rejected (`RevisionConflict`) — the batch is atomic, so
> the package is left **fully installed** (never half-uninstalled); re-run the
> uninstall. Conditioning on the *read-time* revision (not the install-time one)
> is what makes a legitimately-upgraded key not spuriously conflict.

---

## 8.4 Kernel protection

The two install DDLs are themselves **protected** primordial meta-vertices, as
are the meta-root DDL, both Capability lenses, the operator role, the primordial
admin identity, and the primordial meta-permissions. Protection is a
`protected: true` field in the **root vertex document `data`** (not a separate
aspect).

**Authoritative guard (commit-time, path-independent).** Every `update` or
`tombstone` mutation whose root vertex document carries `data.protected == true`
**rejects the whole operation** with error code `ProtectedKey`. `create`
mutations are exempt (create-only already conflicts on overwrite). The guard is
path-independent: it covers `InstallPackage`, `UninstallPackage`,
`UpdateMetaVertex` / `TombstoneMetaVertex`, and any future op at once,
regardless of what the originating script checked. A root that does not exist is
not protected (allow).

Defense-in-depth (clearer per-op error, **not** authoritative):
`UpdateMetaVertex` / `TombstoneMetaVertex` reject a protected target with the
distinct code `ProtectedMetaVertex: <key>`; the commit-time guard above remains
the authoritative backstop.

Net invariant: an operation cannot disable auth (the Capability lenses) or the
kernel (the meta-root DDL) by rewriting or tombstoning them.

**Permission/role provenance protection (commit-time, path-independent).** In
addition to the protected-root guard, every `update` mutation (and any
`tombstone` mutation that carries a document) targeting an existing
`vtx.permission.<id>` root is rejected — error code `PermissionProvenance` — if
it would change `data.operationType`, `data.scope`, `data.origin`,
`data.declaredBy`, or `data.lanes` from the value already committed: those
fields are **write-once** outside the RBAC op surface (which itself never
rewrites them — `UpdatePermission` is deliberately ungranted to every role).
`data.origin` / `data.declaredBy` may be set for the first time on a permission
stored without them (a pre-existing installation predating their introduction);
every other guarded field must already be present. `data.note` may still change
freely. The identical rule makes a role's **entire root document** write-once —
not only its `.canonicalName` aspect (a top-level root field shadows a
same-named aspect in cypher reads) — and protects the `.canonicalName` aspect
itself the same way. A `vtx.roleindex.<id>` root (the canonical-name→role
lookup) is write-once in full by the same rule: a rewritten `data.roleId` would
redirect a canonical role name to a different role's grants with no new grant
step. No legitimate path needs the guarded fields to change on a surviving key
(§8.1 keys are content-addressed on them): a real change produces a **different
key** — a create paired with a tombstone — never an update.

**Package-manifest ownership scoping (commit-time, path-independent).** Four
further guards, all rejecting with error code **`PackageScope`**, running
unconditionally for `InstallPackage`/`UpgradePackage`/`UninstallPackage` — and
an envelope cannot dodge them by making its `class` and `operationType`
diverge:

1. none of the three may ever mutate a `holdsRole` link — no package
   `Definition` field produces one;
2. a **created** `.manifest` aspect may only declare keys the same batch itself
   creates, plus its own package root;
3. an `UpgradePackage`/`UninstallPackage` `update`/`tombstone` must target a key
   already in the **named package's own** prior `.manifest.declaredKeys` (the
   package's own root and manifest aspect are exempt as self-referential), or a
   key the same batch's own manifest update legitimately adds — itself limited
   to a batch-created key, an already-declared key, or a key whose stored
   document is already tombstoned **and** is also an update/tombstone target
   elsewhere in the same batch;
4. a **created** link's source vertex (never the target — deliberately
   asymmetric, so a package may grant to a role it does not own) and a
   **created** aspect's parent vertex must each be in what the batch creates or
   already owns.

`InstallPackage` is create-only and so is naturally exempt from rule (3) only —
rules (1), (2), and (4) all still apply to it.

**Not closed by this guard:** a `create` of a fresh permission/role vertex whose
key is self-consistent with the named package's own claimed content,
`grantedBy`-linked to a role the actor already legitimately holds — closing that
requires server-side verification of a package's real compiled Definition, which
the platform does not perform. **Narrowed, not closed:** reviving an
already-**dead**, non-protected key some other package once declared — no live
key is reachable by any path above, but per-key mint provenance is not tracked
durably, so a dual-declaration of an orphaned dead key remains possible.

---

## 8.6 `UpgradePackage` op

In-place version upgrade (and dev-mode same-version re-apply). The submitter
reads the installed package's `.manifest.declaredKeys` (the **old** key set),
rebuilds the **new** manifest with the same logical-document rules as install
(§8.1, on the version-independent keys), **diffs by key**, and ships the delta
as a single mixed-mutation op:

- a key in **new \ old** → `create`
- a key in **new ∩ old** whose body **changed** → `update` (a byte-equal body is
  omitted — no needless re-stamp). **Exception:** for a non-definition key
  (anything outside `vtx.meta.*` — permission/role vertices, the `grantedBy`
  grant link) whose committed body is already tombstoned, the diff omits the
  update even though the body differs: a surviving grant/role key is tombstoned
  only by an explicit operator action
  (`RevokePermission`/`TombstonePermission`/`TombstoneRole`), and **a
  deliberate revocation is never silently undone by a later upgrade**.
  `vtx.meta.*` definitions keep the plain body-diff rule.
- a key in **old \ new** → `tombstone`. **Exception:** a
  `vtx.retentionclass.<NanoID>` key holder (root or its `.retentionPolicy`
  aspect) is never tombstoned by this diff, whether the removal is a class
  rename or an outright drop — same rule and reasons as §8.3's uninstall
  exception. The excluded keys are left live but undeclared, and are **reported,
  never silently dropped**: the upgrade reports preserved and already-stranded
  **counts**; an uninstall reports the **key list itself** (§8.3), since it has
  no other delta to size the operator's attention against.

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
    { "op": "tombstone", "key": "vtx.permission.MnPq…" }
  ]
}
```

`fromVersion == toVersion` is a legal **dev-mode re-apply** (force same-version),
producing only `update` mutations for changed bodies. The op `requestId` is derived
from `name + fromVersion + toVersion`, so distinct upgrades dedup independently while
a re-submit of the same upgrade short-circuits.

**Response detail:** `{ name, fromVersion, toVersion, created: [<key>…], updated: [<key>…], tombstoned: [<key>…] }`.
**Event:** `PackageUpgraded { name, fromVersion, toVersion, createdCount, updatedCount, tombstonedCount }`.

### Guardrails

Same key-shape + underscore-aspect rejection as install. `op` must be one of
`create` / `update` / `tombstone`. **Unlike install, `UpgradePackage` is not
create-only**, so it is not safe by construction; the §8.4 commit-time guards
cover every `update`/`tombstone` path-independently — an upgrade cannot rewrite
or tombstone a protected kernel / auth root.

### Atomicity + cache coherence

All create/update/tombstone mutations commit as **one atomic operation**
(all-or-nothing — no half-migrated package), and the new definitions are usable
immediately (no restart). Downstream read models and engine registries converge
via the ordinary CDC machinery: `docs/components/refractor.md` (Lens lifecycle)
+ `docs/components/_packages.md`.

### OCC

`update` / `tombstone` are conditioned on the **read-time revision**, the same as
uninstall (§8.3): each mutation asserts the revision its key was read at as its
`expectedRevision`. A concurrent write to a declared key between the diff read
and the commit fails the whole atomic batch (`RevisionConflict`); the batch is
atomic, so the package is left at its **pre-upgrade** version (never
half-migrated) — re-run the upgrade to resolve.

---

## 8.7 Out of scope

- **In-flight-instance DDL-version pinning:** the upgrade is atomic but does not
  fence in-flight orchestration — a breaking DDL change can land while a Loom
  instance is mid-pattern or a Weaver gap is open.
