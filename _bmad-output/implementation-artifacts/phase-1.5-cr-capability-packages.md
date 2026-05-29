# Capability Packages CR Report — Phase 1.5

## Summary

- Files reviewed: 36 across `internal/pkgmgr/` (manifest.go, definition.go, installer.go, build.go, manifest_test.go, installer_test.go), `cmd/lattice-pkg/main.go`, `packages/rbac-domain/` (7 files), `packages/identity-domain/` (9 files), `packages/identity-hygiene/` (8 files), `scripts/verify-package-rbac.go`, `scripts/verify-package-identity.go`, `scripts/verify-package-identity-hygiene.go`
- P0 findings: 1
- P1 findings: 4
- P2 findings: 6
- Nit findings: 5
- History comments: 37 across in-scope files (see section below)

---

## Substrate-direct Install Surface

This section catalogs every Core KV write the installer makes, for the architectural decision pending in Phase 1.5.

### Bucket

All writes target the single `core-kv` bucket. Cross-bucket batches are not supported by NATS atomic-batch (documented in `internal/substrate/batch.go`).

### Per-install write set (one atomic batch, `CreateOnly: true` on every op)

For each DDL declared in the package Definition, the installer writes **9 keys**:
- `vtx.meta.<NanoID>` — DDL meta-vertex (DocumentEnvelope, class = `meta.ddl.vertexType`)
- `vtx.meta.<NanoID>.canonicalName` — AspectEnvelope, `data.value = <canonicalName>`
- `vtx.meta.<NanoID>.permittedCommands` — AspectEnvelope, `data.commands = [...]`
- `vtx.meta.<NanoID>.description` — AspectEnvelope, `data.text = ...`
- `vtx.meta.<NanoID>.script` — AspectEnvelope, `data.source = ...`
- `vtx.meta.<NanoID>.inputSchema` — AspectEnvelope, `data.schema = ...`
- `vtx.meta.<NanoID>.outputSchema` — AspectEnvelope, `data.schema = ...`
- `vtx.meta.<NanoID>.fieldDescription` — AspectEnvelope, `data.fieldDescriptions = {...}`
- `vtx.meta.<NanoID>.examples` — AspectEnvelope, `data.examples = [...]`

For each Lens declared in the package Definition, the installer writes **6 keys**:
- `vtx.meta.<NanoID>` — Lens meta-vertex (DocumentEnvelope, class = `meta.lens`)
- `vtx.meta.<NanoID>.adapter` — AspectEnvelope
- `vtx.meta.<NanoID>.bucket` — AspectEnvelope
- `vtx.meta.<NanoID>.canonicalName` — AspectEnvelope
- `vtx.meta.<NanoID>.engine` — AspectEnvelope
- `vtx.meta.<NanoID>.spec` — AspectEnvelope

For each Permission declared in the package Definition, the installer writes **1 key + N grant link keys**:
- `vtx.permission.<NanoID>` — DocumentEnvelope, class = `permission`, data includes `operationType`, `scope`, optional `note`
- `lnk.permission.<permID>.grantedBy.role.<roleID>` — one per entry in `GrantsTo`

Package bookkeeping — always written:
- `vtx.package.<NanoID>` — DocumentEnvelope, class = `package`, data includes `name`, `version`
- `vtx.package.<NanoID>.manifest` — AspectEnvelope, class = `manifest`, data includes `name`, `version`, `description`, `depends`, `declaredKeys` (snapshot of all keys above, excluding the manifest aspect itself)

### Writes NOT tracked in `declaredKeys` (outside the atomic batch)

The `identity-domain` package's `PreInstall` hook (`packages/identity-domain/seed.go`) runs **before** the main atomic batch and writes to `core-kv` in a **separate** atomic batch. These writes are invisible to the uninstall path:
- `vtx.role.<NanoID>` — role vertex for each of: `consumer`, `frontOfHouse`, `backOfHouse`
- `vtx.role.<NanoID>.canonicalName` — aspect
- `vtx.role.<NanoID>.description` — aspect
- `vtx.roleindex.<sha256NanoID("rolecanonical:<name>")>` — index vertex (DocumentEnvelope, class = `roleindex`)

Total: 12 keys seeded by `PreInstall`, none appearing in `declaredKeys`.

### No Processor invalidation signal

After `lattice-pkg install` completes, there is **no mechanism** to notify the Processor's in-memory DDL cache that new meta-vertices exist. The Processor's cache is populated at startup by scanning `vtx.meta.*` keys. A package installed while the Processor is running will not be seen until the Processor restarts. This is the root cause of the M5/M6 DDL cache gap. There is no watch, event, or invalidation call emitted anywhere in the install path.

---

## P0 Findings

### [F-001] PreInstall writes are orphaned on uninstall — 12 Core KV keys leak permanently

**File:** `internal/pkgmgr/installer.go:108-124` / `packages/identity-domain/seed.go:38-107`

**What:** The `identity-domain` PreInstall hook writes 12 keys to `core-kv` (3 role vertices × 4 keys: vertex + `canonicalName` + `description` + roleindex) in a separate atomic batch that runs **before** the main install batch. These keys are never added to `declaredKeys`. The `Uninstall` path (`installer.go:329-386`) reads `declaredKeys` from the manifest aspect and tombstones those keys. Because the 12 PreInstall-written keys are not in `declaredKeys`, `lattice-pkg uninstall identity-domain` leaves all 3 role vertices and all 3 index entries fully alive in `core-kv` with no tombstone.

**Proof:** `build.go:232` captures `declaredKeys` as a snapshot of `declared[]` at the point the manifest aspect is written. `declared[]` is populated only by `addCreate()` inside `buildInstallBatch()`. `PreInstall` runs at `installer.go:111` — before `buildInstallBatch` is called — and writes directly via `conn.AtomicBatch()` with no feedback to the `declared[]` slice.

**Why it matters:**
1. A `lattice-pkg uninstall identity-domain` + reinstall cycle will fail on `rbac` DDL's `CreateRole` because the leaked `consumer`/`frontOfHouse`/`backOfHouse` role vertices still exist with `CreateOnly: true` keys. The PreInstall idempotency check (`vtx.roleindex.*` probe) finds the live index and reuses the NanoIDs, so install succeeds — but the orphaned role vertices from the prior install remain and accumulate across reinstall cycles.
2. If operator wants to clean up role state (e.g., after a botched install), there is no supported path. The `lattice-pkg uninstall` command provides no option for PreInstall-seeded state.
3. Any audit query on `vtx.role.*` will see phantom roles after uninstall.

**Suggested fix:** The installer should collect the keys written by `PreInstall` (e.g., return them as an additional `[]string` from the hook) and include them in the `declaredKeys` snapshot written to the manifest aspect. Alternatively, the `Uninstall` path should tombstone all keys matching `vtx.roleindex.*` for any role whose canonical name appears in the package's permission grant targets.

---

## P1 Findings

### [F-002] Concurrent install of the same package has a TOCTOU window between idempotency check and atomic batch

**File:** `internal/pkgmgr/installer.go:92-174`

**What:** `Install()` checks for an existing package via `findInstalledPackage()` (a KV scan), then proceeds to build and submit a new atomic batch. There is no distributed lock or optimistic-concurrency marker. Two concurrent `lattice-pkg install rbac-domain` invocations will both see `existing == nil` in step 2, both generate fresh NanoIDs, and both submit `CreateOnly: true` batches. The second batch will fail with a NATS conflict error on every key (since `CreateOnly` means "fail if key exists"). The second process gets a raw NATS error, not a meaningful `ErrVersionMismatch` or "already installed" signal.

**Why it matters:** CI pipelines that install packages in parallel (a common pattern when multiple gate tests provision a fresh cell) will produce a confusing opaque NATS error from the second runner rather than a clean idempotency result. Operator tooling may not retry or recognize the failure as benign.

**Suggested fix:** Two options: (a) add a well-known install-lock key (`meta.install.lock.<pkgName>`) written with `CreateOnly: true` at the start of install and tombstoned at the end — the second concurrent caller gets a predictable conflict on the lock key and can retry or skip; (b) catch the NATS `ErrKeyWrongLastSequence`/conflict error from the atomic batch and return a wrapped error indicating that a concurrent install won, then re-probe `findInstalledPackage` to determine if the result is safe to accept as a no-op.

---

### [F-003] Cold-cell install (before bootstrap) produces an opaque error, not a clear precondition failure

**File:** `internal/pkgmgr/installer.go:92-94` / `cmd/lattice-pkg/main.go:124-127`

**What:** `findInstalledPackage` calls `KVListKeys(ctx, CoreBucket)`. The substrate `Conn.bucket()` method opens (does not create) the KV bucket. If `lattice-pkg install` is run before `bootstrap` has created `core-kv`, the `KVGet`/`KVListKeys` call will fail with a NATS `nats: key not found` or `nats: no keys found` or a stream-not-found error, depending on the substrate version. This error is wrapped as `"pkgmgr: list keys: ..."` and propagated to `cmd/lattice-pkg/main.go`, which prints `install failed: pkgmgr: list keys: ...` — a message that does not mention "bootstrap has not been run" or what the operator should do.

**Why it matters:** Operators running packages against a freshly provisioned NATS instance (e.g., a new Docker environment where bootstrap hasn't run yet) will receive a message that points to an internal key-listing failure with no recovery hint.

**Suggested fix:** Add a pre-flight check in `runInstall` (or `Install`) that probes for the `core-kv` bucket existence and returns a clear `ErrBootstrapRequired` error if the bucket is absent. The `cmd/lattice-pkg` layer should print "core-kv bucket not found — run `lattice-pkg bootstrap` (or `make up`) before installing packages."

---

### [F-004] Version upgrade path is a hard error with no escape hatch

**File:** `internal/pkgmgr/installer.go:102-103`

**What:** `ErrVersionMismatch` is returned when an installed package version differs from the requested version (in either direction — downgrade or upgrade). There is no `--force` flag, no `upgrade` subcommand, and no documented migration path. Once `rbac-domain@0.1.0` is installed, the only way to install `rbac-domain@0.2.0` is `uninstall` + `install`, which tombstones all 10 permission vertices + their grant links and recreates them. If any runtime RBAC state references those permission keys (e.g., a user permission checked by the Processor), the tombstone breaks the live system.

**Why it matters:** Phase 1.5 will ship `rbac-domain@0.2.0` (or similar) the moment any package gains a new operation. The installer, as written, provides no in-place upgrade path. The only docs reference is the `ErrVersionMismatch` error string; no story, README, or doc explicitly documents the uninstall+reinstall procedure as the canonical upgrade path.

**Suggested fix:** (a) Document the `uninstall + install` procedure as the explicit Phase 1 upgrade path in the package README and in the `ErrVersionMismatch` error message (add "use `lattice-pkg uninstall <name>` followed by `lattice-pkg install` to upgrade"); or (b) add a `lattice-pkg upgrade` subcommand as a Phase 2 milestone. The absence of any documented path is the blocker, not the technical limitation itself.

---

### [F-005] `rbac-domain` manifest YAML omits `lenses:` section — silent zero-length slice in cross-validation

**File:** `packages/rbac-domain/manifest.yaml` / `internal/pkgmgr/manifest.go:96-99`

**What:** The `rbac-domain` manifest YAML has no `lenses:` key at all (confirmed by grep). When YAML unmarshalling populates `ManifestBlock`, the `Lenses` field becomes `nil` (length 0). `VerifyAgainstDefinition` checks `len(m.Declares.Lenses) == len(d.Lenses)`. `Package.Lenses` in `rbacdomain/package.go` is also nil (unset). So the check passes: `0 == 0`. This is correct for `rbac-domain` today.

The risk: if a future author adds a Lens to the Go Definition without adding it to the manifest YAML, the `len()` check catches it. **However**, `VerifyAgainstDefinition` does not distinguish between `lenses: []` (explicit empty) and an absent `lenses:` key. An author who accidentally deletes the `lenses:` section from a package that declares lenses would still get a count check — which is fine. This is not a bug today, but it does mean the manifest YAML for `rbac-domain` is inconsistently shaped compared to `identity-hygiene`, which explicitly declares all three sections. The inconsistency will trip up anyone who uses `rbac-domain/manifest.yaml` as a copy-paste template for a lens-bearing package.

**Suggested fix:** Add an explicit `lenses: []` or comment `# no lenses` to `rbac-domain/manifest.yaml` for consistency and to prevent future authors from using it as a template that silently omits lens declarations.

---

## P2 Findings

### [F-006] `identity-domain/manifest.yaml` description block contains no-history narrative text

**File:** `packages/identity-domain/manifest.yaml:4-7`

**What:** The `description:` block reads:

```
Identity vertex creation, claim, and state-machine management. Replaces
the bootstrap-seeded identity DDL (Story 4.1 / 4.6) after kernel
minimization (Story 4.7). The install seed.go PreInstall hook seeds
the 3 user-facing roles (consumer, frontOfHouse, backOfHouse) via
substrate-direct writes; the atomic batch then seeds the identity DDL
+ permission grants.
```

This embeds story-history narrative ("Replaces the bootstrap-seeded identity DDL (Story 4.1 / 4.6)") directly in an install-time artifact. The manifest description is stored verbatim in Core KV as the package vertex's manifest aspect. Any tool that reads package descriptions (e.g., a future `lattice-pkg describe` command, an AI-agent self-description traversal) will surface this history noise to runtime consumers.

**Suggested fix:** Strip to the functional description only. "Identity vertex creation, claim, and state-machine management. Provides CreateUnclaimedIdentity, UpdateIdentityState, ClaimIdentity. Depends on rbac-domain."

---

### [F-007] `identity-hygiene` manifest comment references substrate-direct install as a Phase 2 concern but provides no tracking

**File:** `packages/identity-hygiene/manifest.yaml:8` / `packages/identity-hygiene/README.md:57`

**What:** The manifest comment `# in-bootstrap identity DDL; Phase 1 warns-and-proceeds` and the README line "Install is substrate-direct (Story 5.3 / Phase 2 will route through Processor)" acknowledge the architectural gap but embed the remediation pointer in comments inside versioned artifacts rather than in a tracked work item. The `depends:` enforcement is documented as "Phase 1 logs a warning" in `installer.go:81-88` — but `installer.go` only logs warnings, never errors. An operator who installs `identity-hygiene` without `identity-domain` installed will get a warning that is easy to miss in CI output.

**Suggested fix:** (a) Make dependency validation a configurable strictness level (warn vs error), with `--allow-unverified-deps` for Phase 1 CI; (b) document the Phase 2 enforcement in the architecture decision log rather than scattered across YAML comments and README lines.

---

### [F-008] Verify scripts check key presence and content but do not validate envelope `vertexKey` / `localName` fields in aspects

**File:** `scripts/verify-package-rbac.go:135-170` / `scripts/verify-package-identity.go:154-228` / `scripts/verify-package-identity-hygiene.go:148-185`

**What:** The verify scripts call `getEnvelope()` and assert `data.*` fields (canonicalName value, permittedCommands list, description text, script source, inputSchema/outputSchema/fieldDescription/examples presence). They do **not** verify that aspect envelopes carry correct `vertexKey` and `localName` fields. A Contract #1 violation where `vtx.meta.<NanoID>.canonicalName` is written as a DocumentEnvelope (missing `vertexKey`) rather than an AspectEnvelope would pass all three verify scripts.

This gap was flagged in the Bootstrap CR for `verify-kernel.go`; the same gap exists here. The `build.go` helpers call `makeAspectEnvelope()` which correctly populates both fields, so the actual install path is correct. The verify gap means regressions in `makeAspectEnvelope` won't be caught by `make verify-package-*`.

**Suggested fix:** Add envelope shape assertions to `getEnvelope` callers for aspect keys: check that `env["vertexKey"]` matches the expected vertex key prefix and that `env["localName"]` matches the aspect name suffix.

---

### [F-009] Three verify scripts duplicate ~120 LOC of identical infrastructure per file

**File:** `scripts/verify-package-rbac.go` / `scripts/verify-package-identity.go` / `scripts/verify-package-identity-hygiene.go`

**What:** Each script defines its own `listAllKeys`, `getEnvelope`, `findMetaByCanonical`, `findPackageManifest`, `toStringSlice`, `toSet`, and `envOrDefault` functions (prefixed `rbac-`, `identity-`, `hygiene-` to avoid symbol collision since all three are `package main`). The three scripts are not composable into a single `go run` invocation. Adding a fourth package (e.g., `org-hierarchy`) requires copying another ~120 LOC of boilerplate.

**Suggested fix:** Extract shared verify infrastructure into `scripts/pkgverify/verify.go` as a library with functions `ListAllKeys`, `GetEnvelope`, `FindMetaByCanonical`, `FindPackageManifest`. Each verify script becomes a ~50-LOC caller. This is not urgent but will accumulate tech debt with each new package.

---

### [F-010] `resolveGrants` silently passes unresolved canonical names through to the batch

**File:** `internal/pkgmgr/installer.go:176-207`

**What:** If `resolveGrants` encounters a canonical name in `GrantsTo` that is neither a `vtx.role.*`-prefixed string nor present in `i.RoleIDs`, it appends the raw string unchanged (line 202: `newGrants = append(newGrants, g)`). This raw canonical name is then used as a role NanoID in the grant link key: `lnk.permission.<permID>.grantedBy.role.<rawCanonicalName>`. The resulting key is syntactically valid but semantically broken — it points to a non-existent role vertex. The batch commits successfully (CreateOnly does not validate target vertex existence). The grant link is silently written with a dangling target.

This path is reachable if `lattice.bootstrap.json` does not contain the `roleOperator` key (bootstrap JSON was written by an older version) or if a package's `GrantsTo` references a role that was not seeded by a prior PreInstall.

**Why it matters:** The grant link appears to exist in Core KV; verify scripts that check link key presence will report OK. The Processor's permission check, however, will fail to find the linked role vertex and will deny the operation. The operator sees `PermissionDenied` with no indicator that the grant link target is malformed.

**Suggested fix:** After `resolveGrants`, validate that every remaining entry in `GrantsTo` looks like a valid NanoID (e.g., matches the NanoID alphabet pattern and length). Return an error before the batch is submitted if any entry is unresolved.

---

### [F-011] `buildTombstoneBatch` uses unconditional puts — a concurrent re-projection can overwrite a tombstone

**File:** `internal/pkgmgr/build.go:271-281`

**What:** The comment at line 271 documents the decision: "Tombstone batch uses unconditional puts (no OCC)." The rationale given is that "the entire batch is still atomic." This is correct for atomicity within the batch itself. However, it does not protect against a Processor write that happens to update one of the to-be-tombstoned keys (e.g., a meta-vertex's `description` aspect updated via `UpdateMetaVertex`) **between** the `buildTombstoneBatch` read phase and the atomic batch submission. The tombstone would unconditionally overwrite the Processor's update, silently discarding it.

In Phase 1 this is low-risk because meta-vertices are not runtime-mutable via the Processor (the kernel's `UpdateMetaVertex` op is out of scope for packages). For runtime vertices (role vertices, permission vertices) it is a real window if the operator issues an `UpdateRole` concurrently with an uninstall.

**Suggested fix:** Document this window explicitly in `buildTombstoneBatch`'s comment: "Note: concurrent Processor writes to these keys between the read phase and batch submission will be silently overwritten. Phase 2 should use per-key sequence numbers in the tombstone batch."

---

## Nit Findings

### [N-001] `envOrDefault` is defined in both `scripts/verify-package-rbac.go` (line 472) and `scripts/verify-package-identity.go` (line 497) with identical bodies

Both scripts are `package main` with `//go:build ignore`. No compile-time conflict exists since they're built separately. But if they're ever moved to a shared test binary the identical unexported symbol will conflict. Minor; see F-009 for the broader fix.

---

### [N-002] `identity-domain/manifest.yaml` uses `grantsTo` (camelCase) but the Go `ManifestPermission` struct tags it as `yaml:"grantsTo"` — no issue, but the YAML key is load-bearing

**File:** `packages/identity-domain/manifest.yaml:18-27` / `internal/pkgmgr/manifest.go:49`

The YAML tag is `grantsTo` (lowercase `g`), which matches the file. The struct tag is `yaml:"grantsTo,omitempty"`. This is consistent. Note: the manifest YAML field `grantsTo` is parsed but **never used to drive install behavior** — install behavior comes entirely from the Go `Definition`, not the manifest. `VerifyAgainstDefinition` only checks `OperationType` across Permissions, not `GrantsTo` lists. A drift between manifest `grantsTo` and Go `GrantsTo` would pass cross-validation silently.

---

### [N-003] `installer_test.go` `sampleDef` hard-codes `GrantsTo: []string{"operator"}` but `inst.RoleIDs` is nil in all test cases — `resolveGrants` leaves "operator" as a raw string in every test

**File:** `internal/pkgmgr/installer_test.go:106-114`

The test installer (`newInstallerHarness`) does not set `RoleIDs`. `resolveGrants` leaves "operator" as the literal role NanoID in the grant link key: `lnk.permission.<id>.grantedBy.role.operator`. This is not a valid NanoID but the test still passes because the `TestInstaller_HappyPath` spot-check only verifies that `DeclaredKeys` entries exist in core-kv — it does not validate grant link shape or that the target role vertex exists. The dangling grant link is silently acceptable in test context but means unit tests do not cover the grant resolution path at all.

---

### [N-004] `reports_to_test.go` in `packages/rbac-domain/` skips unconditionally with a `t.Skip` — dead test file

**File:** `packages/rbac-domain/reports_to_test.go:38`

```go
t.Skip("reportsTo retired from kernel in Story 4.7; awaiting org-hierarchy Capability Package — TODO: relocate this test.")
```

This is a permanently skipped test that adds noise to `go test ./packages/rbac-domain/...` output. The TODO is untracked. The test file should either be deleted (if the functionality will live in a future `org-hierarchy` package with different test fixtures) or moved to a dedicated story artifact.

---

### [N-005] `packages/identity-hygiene/merge_test.go` header comment references "Story 4.6 Surface-B (Phase-1 hygiene carry)" — no-history violation in test file

**File:** `packages/identity-hygiene/merge_test.go:1`, `packages/identity-hygiene/testhelpers_test.go:1`, `packages/identity-domain/testhelpers_test.go:1`, `packages/identity-domain/create_test.go:1`, etc.

Multiple test file headers encode story-history attribution. These are test files so they don't appear at runtime, but they accumulate reviewer confusion and violate the no-history-in-code convention. The production code files (`definition.go`, `build.go`, `installer.go`, `cmd/lattice-pkg/main.go`) similarly reference Story 1.1, 4.7, 5.1, 5.3 in comments. See the full count below.

---

## No-History Comments

`grep -rn -E "Story [0-9]+\.|Replaces|Previously|Was [A-Z]" internal/pkgmgr/ cmd/lattice-pkg/ packages/` — 37 matches across 16 files.

| File | Count | Worst offenders |
|------|-------|-----------------|
| `internal/pkgmgr/definition.go` | 3 | Story 5.3, 5.1, 4.7 |
| `internal/pkgmgr/build.go` | 6 | Story 5.1 (×4), 4.7, 1.1 |
| `internal/pkgmgr/installer.go` | 1 | Story 1.1 |
| `cmd/lattice-pkg/main.go` | 2 | Story 5.3, 4.7 |
| `packages/identity-domain/manifest.yaml` | 3 | "Replaces", Story 4.1, 4.6, 4.7 |
| `packages/identity-domain/package.go` | 2 | Story 4.7 |
| `packages/identity-domain/ddls.go` | 1 | Story 4.7 |
| `packages/identity-domain/permissions.go` | 1 | Story 4.7 |
| `packages/identity-domain/seed.go` | 1 | Story 5.3 |
| `packages/identity-domain/create_test.go` | 3 | Story 4.2, 4.6 |
| `packages/identity-hygiene/README.md` | 1 | Story 5.3 |
| `packages/identity-hygiene/merge_test.go` | 2 | Story 4.6, 4.7 |
| `packages/rbac-domain/package.go` | 2 | Story 4.7 |
| `packages/rbac-domain/ddls.go` | 1 | Story 4.7 |
| `packages/rbac-domain/package_test.go` | 1 | Story 4.7 |
| `packages/rbac-domain/reports_to_test.go` | 5 | Story 3.6, 4.7 (multiple) |

The production-code comments in `build.go` are the most disruptive: Story numbers appear in error message strings returned to callers (`"InputSchema required (Story 5.1)"`). These strings will surface in CLI output and operator logs.

---

## Architectural Note: Substrate-direct Install Gap (M5/M6)

The substrate-direct install pattern means the Processor's in-memory DDL cache is populated only at startup (by scanning `vtx.meta.*` keys). When `lattice-pkg install` writes new meta-vertices to `core-kv`, the Processor has no notification. Operations on classes declared by the newly-installed package will be rejected with `UnknownClass` until the Processor restarts.

No finding is filed for this because it is a documented, acknowledged architectural debt item. It is cataloged here for Phase 1.5 architectural decision context:

**Signal needed:** After the install atomic batch commits, something must tell the Processor to invalidate and reload its DDL cache. Options:
1. A dedicated NATS subject (`lattice.meta.invalidate`) that `lattice-pkg` publishes to after install; the Processor subscribes and rescans.
2. A NATS KV watch on `vtx.meta.*` that the Processor already has running (eliminates the install-side change, but requires KV watch infrastructure in the Processor).
3. Route installs through the Processor's operation-submission lane (Story 5.3 / Phase 2) — the Processor sees the CreateMetaVertex ops and updates its own cache in-line.

Option 3 is the architecturally correct solution; Options 1 and 2 are Phase 1.5 mitigations.
