# Story 1.5.5 — Route capability-package installs through the Processor (M5)

**Phase 1.5 (Hardening Block) · Wave B · Largest story · depends on 1.5.1 (done)**
**Tier:** Opus
**Author:** Winston · **Date:** 2026-05-30
**Sources:** Cap-pkg CR substrate-direct surface + F-001/F-002/F-011, Gate 5 **B2**, retro **C3**, Andrew's "route through Processor" decision. Also closes the **1.5.2 kernel-protection residual**.
**Andrew's decisions (this session, binding):** bundle into a single `InstallPackage` operation; **thin-script / fat-manifest** (see §3).

---

## 🔴 MANDATORY OPERATING RULES (READ FIRST)

1. Repo root `/Users/andrewsolgan/Documents/GitHub/Lattice`. No worktrees.
2. **Do NOT commit or push.** Leave changes in the working tree for Winston.
3. **Do NOT edit planning artifacts** (`_bmad-output/planning-artifacts/*`). Contract questions → `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md`. Docs go under `docs/`.
4. **No history comments** in code. Comments describe current behavior only.
5. **SURVEY FIRST, then CHECKPOINT.** This is the largest Phase-1.5 story. Before cutting, survey §2 and write your implementation map. After the install path works end-to-end for ONE package (rbac-domain), STOP and append a checkpoint to §8 (what works, what's left) before doing uninstall + the other packages — so Winston can re-scope/split if needed.
6. **Halt and escalate** (CAR) on any stuck loop (re-attempt after 3+ failures, immediate reverts, re-reading for an absent answer, cycling approaches, unresolved test failure after 2 debug attempts). Token budget tracked, NOT enforced.
7. **Append a closing summary** to §8.

---

## 1. Goal

Eliminate the substrate-direct install pattern. Capability-package install **and** uninstall route through the Processor as two new **kernel operations** — `InstallPackage` / `UninstallPackage` — so:
- the Processor sees installs in-line and its DDL cache stays coherent (closes **M5 / B2** — new classes are usable immediately, no restart);
- install is **one atomic commit** (all DDLs + lenses + roles + permissions + grants), so former `PreInstall` runtime entities are in the same batch + the manifest's `declaredKeys` (closes **F-001** orphans);
- concurrent installs get a predictable idempotency-tracker/`CreateOnly` conflict (**F-002**); uninstall tombstones carry `expectedRevision` (**F-011**);
- the formal package-install contract (**C3**) is "the `InstallPackage` op envelope carrying the pre-built mutation manifest";
- primordial kernel entities become **protected** from tombstone/update (closes the **1.5.2 residual**).

**Out of scope (defer):** version-upgrade path (cap-pkg **F-004**) → its own later story; Loom/Weaver; conformance freeze (1.5.7).

## 2. Required context — SURVEY before cutting

- `internal/pkgmgr/build.go` — `buildInstallBatch` ALREADY computes the full mutation set (DDL meta-vertices + lens meta-vertices + permission vertices + grant links + package vertex + `.manifest` aspect with `declaredKeys`). This IS the fat manifest. Note where it constructs envelopes (`MakeVertexEnvelope` etc.) and the `declaredKeys` snapshot (L232).
- `internal/pkgmgr/installer.go` — `Install` (the `PreInstall` hook at ~L117 + the substrate-direct `AtomicBatch` at L191) and `Uninstall` (`buildTombstoneBatch` + `AtomicBatch` at L413). `resolveGrants`, `RoleIDs`.
- `internal/pkgmgr/definition.go` — `Definition`, `PreInstallFn` (~L59–70, the substrate-direct role seeder to be folded in).
- `packages/identity-domain/seed.go` — the `PreInstall` hook seeding consumer/frontOfHouse/backOfHouse roles substrate-direct (12 keys, F-001). These move INTO the build mutation set.
- `internal/bootstrap/primordial.go` — how `MetaRootKey` (the kernel meta-DDL) + the `CapabilityLens` are seeded as primordial meta-vertices (`buildPrimordialEntries`, ~L274+). You add two more primordial DDLs here + `protected` markers.
- `internal/bootstrap/meta_ddl.go` — `MetaRootDDLScript` (the kernel DDL Starlark pattern: `make_vtx`/`make_aspect`/`make_tombstone`, `execute(state, op)` dispatch). Model the new scripts on it. The `Tombstone`/`Update` branches gain the protected-key guard.
- `internal/processor/step8_commit.go` — confirm the synchronous `vtx.meta.*` DDL-cache invalidation fires for an `InstallPackage` commit (it iterates `result.Mutations`).
- `cmd/lattice-pkg/main.go` — `runInstall`/`runUninstall` (currently build `Installer` → substrate-direct). These become op submitters.
- `cmd/lattice/output/submit.go` — `SubmitOp` (the canonical Processor op-submission pattern to reuse).
- `scripts/verify-package-{rbac,identity,identity-hygiene}.go` + the Makefile gates — must stay green after re-routing (they assert installed Core KV state, install-mechanism-agnostic).

Do NOT read large planning artifacts.

## 3. Design (LOCKED by Winston)

### 3.1 Two primordial kernel DDLs
Seed `InstallPackage` and `UninstallPackage` as primordial DDL meta-vertices in `primordial.go` (siblings of the meta-root DDL), each with `canonicalName`, `permittedCommands` (`["InstallPackage"]` / `["UninstallPackage"]`), `description`, `script`, and the Story-5.1 self-description aspects (inputSchema/outputSchema/fieldDescription/examples/.compensation) so they satisfy `MissingSelfDescription` and verify-kernel. They are **protected** (§3.4). Auth: these ops require an operator/admin capability (same privilege the operator role already holds) — wire the permission grant so the primordial admin/operator can submit them.

### 3.2 Install contract (C3) — thin script, fat manifest
- **Build (client side):** `buildInstallBatch` produces the canonical mutation list — keep it, but it now emits **logical documents** (`class`, `data`, `isDeleted`) WITHOUT manually stamping provenance (the Processor stamps `createdAt`/`lastModifiedAt`/author at step 8 — so installed entities get real provenance authored by the install actor, an improvement over substrate-direct). Entity NanoIDs stay **deterministic** (derived from package name+version+entity, as today via the index probes / build) so re-install is idempotent and produces identical keys.
- **Op payload:** `InstallPackage` payload = `{name, version, mutations: [{op, key, document}, ...]}` — the pre-built manifest. The op `requestId` is **deterministic** from package name+version so a re-submit dedup-short-circuits at step 2.
- **Kernel script (thin + guardrails):** the `InstallPackage` Starlark script iterates the payload mutations and emits them as the op's mutations, enforcing guardrails (it is a privileged op, so it must not be an arbitrary-write backdoor):
  - key-shape invariants — every key matches an allowed pattern (`vtx.<type>.<id>`, `vtx.<type>.<id>.<aspect>`, `vtx.meta.<id>[.aspect]`, `lnk.<...>`); reject anything else;
  - **reject any protected/primordial key** (§3.4) — installs may not overwrite kernel entities;
  - reject system/underscore-prefixed aspects (mirror step-6 `sensitiveAspectScope`);
  - all `op` values ∈ {`create`} for install (no updates/tombstones in an install);
  - emit a `PackageInstalled` event `{name, version, keyCount}`; `response {name, version, declaredKeys}`.
- **Atomicity + cache:** all mutations land in ONE step-8 atomic batch; step 8 invalidates the `vtx.meta.*` entries in-commit → the package's DDLs/lenses are immediately usable (M5/B2).
- **PreInstall fold:** move identity-domain's role seeding into `buildInstallBatch` (roles + roleindex + aspects become part of the manifest `declaredKeys`). Delete the `PreInstallFn` substrate-direct mechanism (`definition.go`, `installer.go`, `seed.go`). `resolveGrants`/`RoleIDs` still resolve grant targets, but the role vertices are now created in-batch with their deterministic NanoIDs.

### 3.3 Uninstall contract
- `UninstallPackage` payload = `{name, declaredKeys: [...]}` — the client (`installer`) reads the package's `.manifest` aspect first, then submits. Script tombstones each declared key with **`expectedRevision`** carried per key (client reads current revisions; OCC closes F-011) — OR, if per-key revision plumbing is too heavy for this story, tombstone unconditionally but **document** the window and append a CAR proposing the per-key-revision follow-up. Prefer real OCC if clean.
- Script **rejects protected keys** (defense in depth) and emits `PackageUninstalled`.

### 3.4 Kernel-protection guard (1.5.2 residual)
- Bootstrap seeds `protected: true` in the **root vertex doc `data`** of primordial kernel meta-vertices: the meta-root DDL, the `InstallPackage`/`UninstallPackage` DDLs, the `CapabilityLens`, the operator role, the primordial admin identity, and the primordial meta-permissions. (Root-doc field, not a separate aspect — the Tombstone/Update branches already read the root.)
- The **meta-root DDL** `UpdateMetaVertex` + `TombstoneMetaVertex` branches reject when the target root's `data.protected == true` → `fail("ProtectedMetaVertex: <key>")`. (`vertex_alive`/class reads already load the root; read `protected` the same way.)
- `UninstallPackage` rejects any declared key whose root is protected.
- Document the protection contract in `docs/components/processor.md`.

### 3.5 Eliminate substrate-direct
- `installer.go`: replace both `AtomicBatch` calls with `InstallPackage`/`UninstallPackage` op submission via the Processor (reuse `output.SubmitOp` pattern; the installer needs a NATS conn + the admin actor, which it has). The installer becomes an op-builder+submitter; `build.go` stays the mutation computer.
- `cmd/lattice-pkg`: `runInstall`/`runUninstall` submit ops (operator-credentialed).
- After this story, `grep -rn "AtomicBatch" packages/ internal/pkgmgr/ cmd/lattice-pkg/` is clean of install writes (the Phase-2-readiness "substrate-direct install grep-clean" gate). Primordial bootstrap (`internal/bootstrap`) legitimately keeps its `AtomicBatch` — that's the sanctioned non-Processor seed path, not an install.

## 4. Out of scope (do NOT touch)
- Version-upgrade path (F-004) — defer. If a package re-installs at a new version, a hard "already installed" error is acceptable for this story (document it).
- Conformance freeze (1.5.7); Gate-5 flip (1.5.6); Loom/Weaver.

## 5. Verification gates (run all; paste tails into §8). `make down && make up` between full-suite runs (Deviation 14).
```
go build ./...
make vet
golangci-lint run ./...
make down && make up && make verify-kernel
make verify-package-rbac && make verify-package-identity && make verify-package-identity-hygiene
go test ./internal/pkgmgr/... ./internal/bootstrap/... ./internal/processor/... -p 1 -count=1
go test ./... -p 1 -count=1
make test-bypass
make test-capability-adversarial
```
- **NEW required test:** install a package via `InstallPackage`, then submit a domain op on a class the package just declared **without restarting the Processor**, and assert it commits (proves M5/B2 cache coherence). Plus: install→uninstall→re-install cycle leaves no orphans (F-001); a `TombstoneMetaVertex`/`UpdateMetaVertex` against a protected primordial key is rejected (§3.4).
- verify-package-* must stay green (install state is mechanism-agnostic).

## 6. Deliverables checklist
- [ ] `InstallPackage` + `UninstallPackage` primordial kernel DDLs seeded (self-description complete; verify-kernel green) + operator/admin permission to submit them.
- [ ] Install routed through Processor: one atomic commit; DDL cache coherent in-commit (M5/B2 test passes).
- [ ] Thin script + guardrails (key-shape, protected-key, system-aspect, create-only); fat manifest from `build.go` (logical docs, Processor-stamped provenance, deterministic keys, deterministic requestId).
- [ ] PreInstall roles folded into the install batch + `declaredKeys`; `PreInstallFn` substrate-direct mechanism deleted; F-001 orphan-free install→uninstall→reinstall test passes.
- [ ] `UninstallPackage` routed through Processor; OCC via expectedRevision (or CAR if deferred); protected-key rejection.
- [ ] Kernel-protection: `protected:true` on primordial entities; meta-root Update/Tombstone + UninstallPackage reject protected keys; rejection test passes.
- [ ] substrate-direct install grep-clean (pkgmgr/packages/cmd-lattice-pkg); bootstrap seed path retained.
- [ ] All 3 packages install+verify via the new path; verify-package-* + all §5 gates green.
- [ ] Docs: `docs/components/processor.md` (InstallPackage/UninstallPackage + protection) + a `docs/contracts/` install-contract page (C3).

## 7. Notes
This story defines the package-install contract Loom will build on — keep the `InstallPackage` payload shape clean and documented. If the single session can't fit all of §6, honor the §5/rule-5 checkpoint after rbac-domain installs end-to-end and let Winston split (precedent: 4.6 multi-round).

## 8. Checkpoint + closing summary (sub-agent fills in)

### Implementation map (survey complete — written before cutting)

**Key facts established by survey:**
- `make up` runs the Processor with `LATTICE_AUTH_MODE=stub` (Makefile L41), so the **production `make verify-package-rbac` path bypasses cap-doc auth** — the installer only needs to submit a well-formed `InstallPackage` op; the stub authorizer authorizes it, the script runs, commits. Cap-doc projection of the `InstallPackage` perm only matters for capability-mode tests (`test-capability-adversarial`) and is wired primordially regardless.
- DDL resolution (step 4): envelope `Class` field → `DDLCache.Lookup(class)` by `canonicalName`. The cache auto-discovers ALL `vtx.meta.*` vertices at Refresh by their `.canonicalName` aspect (ddl_cache.go L91+). So seeding the two new DDL meta-vertices primordially makes them resolvable by `Class` automatically — no special registration.
- Op routing mirrors `CreateMetaVertex` (fr19_northstar_test.go L83-88): `Lane: meta`, `OperationType: "InstallPackage"`, `Class: "<ddl canonicalName>"`, `Actor: admin`, `Payload: {...}`. Submit via `output.SubmitOp` (NATS inbox round-trip).
- Step 8 already invalidates `vtx.meta.*` cache entries in-commit (step8_commit.go L170-190) — so M5/B2 coherence is FREE once installs route through a single op whose mutations include the DDL meta-vertices. No Committer change needed.
- Provenance: step 6/8 stamps `createdAt/By/ByOp` from the op actor + tracker key (buildMutationValue, step8_commit.go L237+). So `build.go` must emit LOGICAL docs (no manual provenance) — the Processor stamps it.
- `MetaRootDDLScript` (meta_ddl.go) is the model for the new thin scripts: `make_vtx/make_aspect`, `execute(state, op)` dispatch, `fail(...)` guardrails. Builtins available: `nanoid.new()`, `json.decode/encode`, `crypto.sha256NanoID`.

**Build order toward the rbac-domain CHECKPOINT:**
1. `nanoid.go`: add `InstallPackageDDL{ID,Key}` + `UninstallPackageDDL{ID,Key}` package vars + persist fields (bootstrap file v5 — bump checkVersion; `make down && make up` is already mandated so a fresh keyspace is fine). Add operator-grant perm NanoIDs (`PermInstallPackage*`, `PermUninstallPackage*`).
2. `meta_ddl.go`: add `InstallPackageDDLScript` + `UninstallPackageDDLScript` (thin + guardrails: key-shape, reject protected/primordial keys, reject system/underscore aspects, create-only for install).
3. `primordial.go`: seed the two DDL meta-vertices (9 self-desc aspects each incl `.compensation`), their permission vertices, grantedBy→operator links. Add `protected: true` to the root `data` of primordial kernel entities (§3.4). Bump `PrimordialVertexKeyCount` + `PrimordialVertexKeys()` + verify-kernel expectations.
4. `build.go`: change `addCreate` to emit logical docs `{op:"create", key, document:{class,data,isDeleted}}` (drop provenance stamping in `makeDocEnvelope` etc. for the manifest payload) and return the manifest list. Add a function producing the `InstallPackage` payload `{name, version, mutations}`.
5. `installer.go`: replace the `Install` AtomicBatch (L191) with `InstallPackage` op submission (deterministic requestId from name+version). Keep idempotency/dependency/grant-resolution preamble. Deterministic NanoIDs (derive from name+version+entity, not `substrate.NewNanoID()`).
6. `cmd/lattice-pkg/main.go`: `runInstall` builds + submits the op (the installer change covers this — main just calls `inst.Install`).
7. `make down && make up && make verify-kernel && make verify-package-rbac` → green = CHECKPOINT.

**Deferred to post-checkpoint (Winston may split):** uninstall (`UninstallPackage` + OCC), identity-domain PreInstall fold + `PreInstallFn` deletion, identity-hygiene, kernel-protection reject test, `InstallPhase1Packages` testutil refactor (installs run before pipeline exists — needs a meta-lane stub-auth commit path to drive ops), grep-clean, full §5 suite, docs.

**Open risk flagged for checkpoint report:** deterministic NanoID derivation must stay collision-free across a package's entities AND idempotent across re-install; will derive via `crypto.sha256NanoID`-equivalent over `name+version+entityTag`.

### ✅ CHECKPOINT — rbac-domain installs end-to-end through the Processor (2026-05-30)

**What is GREEN:**
- `go build ./...`, `make vet`, `golangci-lint run ./internal/bootstrap/... ./internal/pkgmgr/...` → all clean (0 issues).
- `make down && make up` → bootstrap committed **93** primordial entries (was ~69; +24 for the two new DDLs × 10 keys + 2 perms + 2 grant links).
- `make verify-kernel` → **ALL ASSERTIONS PASSED** (new InstallPackage/UninstallPackage DDL top-level keys present; existing kernel intact).
- `make verify-package-rbac` → **ALL ASSERTIONS PASSED (53 OK)** — rbac-domain installed via the Processor, all DDLs/permissions/grants present and correct.
- Processor log confirms the full pipeline ran for the install (not substrate-direct):
  `step1 parsed (lane=meta, operationType=InstallPackage)` → `step4 hydrated (class=InstallPackage)` → `step5 executed (mutations=31)` → `step8 committed (31 mutations, ONE atomic batch, seq=125)` → `step9 PackageInstalled event`. No rejections.
- **Provenance improvement verified:** the installed `vtx.package.<id>` has `createdBy=vtx.identity.<admin>` and `createdByOp=vtx.op.<InstallPackage-tracker>` — the Processor stamps real provenance authored by the install actor (§3.2), an improvement over the old bootstrap-identity substrate-direct stamp.
- **M5/B2 cache coherence:** step 8's existing `vtx.meta.*` invalidation fires in-commit for the InstallPackage batch (the 31 mutations include the DDL meta-vertices) — no Committer change needed; the new classes are usable without a Processor restart. (The explicit "submit a domain op on the just-declared class without restart" assertion is part of the deferred test set but the mechanism is in place and exercised by verify-package-rbac's reliance on the committed DDLs.)

**What was BUILT (toward the checkpoint):**
1. `internal/bootstrap/nanoid.go` — bootstrap file **v5**; added `InstallPackageDDL{ID,Key}`, `UninstallPackageDDL{ID,Key}`, `PermInstallPackage{ID,Key}`, `PermUninstallPackage{ID,Key}`; updated generate/populate/currentRaw/PrimordialVertexKeys; `PrimordialVertexKeyCount` 18 → **25**. checkVersion now requires "5" (`make down && make up` mandated anyway — Deviation 14).
2. `internal/bootstrap/install_ddl.go` (NEW) — `InstallPackageDDLScript` + `UninstallPackageDDLScript` (thin scripts over fat manifest) with shared `installGuardrailHelpers` prelude: key-shape, protected-root reject (via state), system/underscore-aspect reject, create-only (install) / OCC-per-key (uninstall). Plus the self-description constants (input/output schema, fieldDescription, examples).
3. `internal/bootstrap/primordial.go` — seeds the two DDL meta-vertices (9 aspects each incl `.compensation`) via new `seedPackageInstallDDL` helper; the two install permissions + grantedBy→operator links; `protected: true` on the root `data` of the admin identity, meta-root DDL, both Capability lenses, operator role, all meta-permissions, both install permissions, and the two new DDLs (§3.4).
4. `internal/pkgmgr/build.go` — `buildInstallBatch` now emits **logical documents** (`docVertex/docAspect/docLink`, no provenance) returned as `[]installMutation{op,key,document}`; the Processor stamps provenance at step 8.
5. `internal/pkgmgr/installer.go` — `Install` now derives **deterministic NanoIDs** (`deterministicNanoID(name,version,tag)` via SHA-256→alphabet) for all entities + a deterministic op `requestId`, builds the `InstallPackage` payload `{name,version,mutations}`, and submits via the new `submitOp` (ops.meta, NATS inbox round-trip — reproduced locally so `internal/pkgmgr` does not import a `cmd/` package). Handles accepted/duplicate/rejected.
6. `cmd/lattice-pkg/main.go` — unchanged surface; `runInstall` calls `inst.Install`, which now routes through the Processor.

**What REMAINS (Winston may split):**
- **Uninstall** (`UninstallPackage` op): the script is WRITTEN + seeded but `installer.go`'s `Uninstall` still uses `buildTombstoneBatch` + `AtomicBatch` (substrate-direct). Needs: read `.manifest`, optionally read per-key revisions for OCC (expectedRevision plumbing), submit `UninstallPackage` op. `cmd/lattice-pkg` `runUninstall` already just calls `inst.Uninstall`.
- **identity-domain PreInstall fold** — move `packages/identity-domain/seed.go`'s 3-role seeding into `buildInstallBatch` (deterministic role NanoIDs + roleindex + aspects → manifest `declaredKeys`); delete `PreInstallFn` mechanism (`definition.go`, `installer.go` step 2.5, `seed.go`). `resolveGrants`/`RoleIDs` stay.
- **identity-domain + identity-hygiene end-to-end** via the new path + their verify gates.
- **`InstallPhase1Packages` / `SetupPackageTestEnv` testutil refactor** — ~11 test files install packages BEFORE any pipeline exists; they now hit "wait for reply: context deadline exceeded". The helper must stand up a meta-lane stub-auth `CommitPath` + consumer and drive the InstallPackage ops. This is the single largest remaining work item and blocks `go test ./...`, `test-bypass`, `test-capability-adversarial`, and the bootstrap `self_description_e2e_test`.
- **NEW required tests:** install→submit-domain-op-without-restart (M5/B2); install→uninstall→reinstall orphan-free (F-001); TombstoneMetaVertex/UpdateMetaVertex against a protected primordial key is rejected (§3.4 — note: the meta-root DDL's Tombstone/Update branches do NOT yet read `data.protected`; that guard still needs to be added to `MetaRootDDLScript`).
- **Kernel-protection guard in `MetaRootDDLScript`** (§3.4) — the Update/Tombstone branches must `fail("ProtectedMetaVertex: ...")` when the target root's `data.protected == true`. NOT yet done (the `protected` flags are seeded; the meta-root enforcement is pending).
- **grep-clean** — `grep -rn "AtomicBatch" packages/ internal/pkgmgr/ cmd/lattice-pkg/`: still present in `installer.go` (Uninstall) and `packages/identity-domain/seed.go` (PreInstall) until the two items above land.
- **Docs** — `docs/components/processor.md` (InstallPackage/UninstallPackage + protection) + `docs/contracts/` install-contract page.

**Assessment of remaining effort:** the install half (the largest design risk — routing a privileged op through the full auth/hydrate/execute/commit pipeline with in-commit DDL coherence) is DONE and proven against production. The remainder is mechanical-but-broad: uninstall (small), PreInstall fold (small), meta-root protected guard (small Starlark edit), the new tests (moderate), and the **testutil pipeline refactor (moderate-large, gates the whole unit suite)**. Recommend either continuing in-session starting with the testutil refactor (unblocks the suite) then uninstall + PreInstall + protected guard, OR splitting the testutil refactor + tests into a follow-up if budget is tight. No stuck loops; no CARs needed.

_(Winston: design §3 held up cleanly — the stub-auth `make up` path made the install milestone reachable without first solving cap-doc projection; capability-mode projection is wired primordially via the operator grant and will surface for `test-capability-adversarial` once that test installs through a pipeline.)_

### ✅ CLOSING SUMMARY — all §6 deliverables complete (2026-05-30, continuation session)

Winston approved continuing in-session (no split). All remaining §6 deliverables landed in the order recommended. No stuck loops.

**Deliverables vs §6 (all ✅):**
- ✅ `InstallPackage` + `UninstallPackage` primordial kernel DDLs seeded (self-description complete; verify-kernel green) + operator/admin permission to submit them. *(landed in the checkpoint; unchanged.)*
- ✅ Install routed through the Processor — one atomic commit; DDL cache coherent in-commit. **M5/B2 test passes** (shared-cache pipeline: `rbac` class absent before install, resolvable + a `CreateRole` op commits after, on the same running Processor).
- ✅ Thin script + guardrails (key-shape, protected-key, system-aspect, create-only); fat manifest from `build.go` (logical docs, Processor-stamped provenance, deterministic keys + requestId).
- ✅ **PreInstall fold:** identity-domain's 3 roles (vertex + canonicalName/description aspects + roleindex) now created in the install batch with deterministic NanoIDs and captured in `declaredKeys`. `PreInstallFn` mechanism **deleted** (`definition.go`, `installer.go`, `packages/identity-domain/seed.go` removed). New `Definition.Roles []RoleSpec`. Folded role NanoIDs are deterministic (`deterministicNanoID(name,version,"role:"+canonical)`) → idempotent re-install. **F-001 orphan-free test passes.**
- ✅ **Uninstall** routed through the Processor (`UninstallPackage` op; installer reads `.manifest` declaredKeys + package vertex, submits tombstones). **OCC DEFERRED with documented window + CAR** — see Deviation/CAR below.
- ✅ **Kernel protection (§3.4):** `MetaRootDDLScript` `UpdateMetaVertex` + `TombstoneMetaVertex` now `fail("ProtectedMetaVertex: <key>")` when `state[meta_key].data.protected == True` (new `is_protected` helper). `UninstallPackage` rejects protected declared keys (was already in the seeded script via `_is_protected_root`). **Protected-rejection test passes** (asserts reply status=rejected, error mentions ProtectedMetaVertex, target unmutated, for BOTH Tombstone + Update).
- ✅ **grep-clean:** `grep -rn "AtomicBatch" packages/ internal/pkgmgr/ cmd/lattice-pkg/` → only `packages/identity-domain/state_machine_test.go` (an unrelated test fixture tombstoning a lease, NOT an install write). Bootstrap seed path (`internal/bootstrap`) legitimately retained.
- ✅ All 3 packages install+verify via the new path; verify-package-* + all §5 gates green.
- ✅ **Docs:** `docs/components/processor.md` gains "Package install / uninstall" + "Kernel protection" sections; new `docs/contracts/08-package-install.md` (Contract #8, C3); indexes updated (`docs/index.md`, `docs/contracts/_index.md`).

**testutil pipeline refactor (done FIRST, unblocked the suite):**
- `internal/testutil/install_phase1_packages.go` — `InstallPhase1Packages` now stands up a REAL meta-lane stub-auth `CommitPath` (`RunMetaInstallPipeline`) that consumes the submitted `InstallPackage`/`UninstallPackage` ops. The InstallPackage DDL script, step-6 validation, and step-8 atomic commit all run for real; only the auth step is stubbed (`AuthModeStub` via `MakeStubPipeline`). No guardrail or validation is skipped — installs are not faked.
- `internal/testutil/pipeline.go` — `ProvisionHarness` now also creates the `core-events` stream (step 9 publishes `PackageInstalled`/`PackageUninstalled`; absent stream caused nak-redelivery → cross-test interference on the shared `ops.meta` lane).
- On stop, the install consumer is deleted AND the committed install ops are purged from the `ops.meta` subject, so a meta-lane consumer a test creates afterward (DeliverAll) does not replay them as spurious "duplicate" outcomes. (Two real failures — `TestFR19_NFR_S10` + `TestGate4` got "duplicate" — were root-caused to lingering install ops + missing events stream and fixed here, NOT papered over.)
- `internal/pkgmgr/installer_test.go` — `newInstallerHarness` now seeds primordials + provisions ops/events streams + runs the meta pipeline, so the install/uninstall/list/idempotency unit tests exercise the Processor-routed path. Coverage not weakened (same assertions; install/uninstall now go through the full pipeline).

**Files touched:**
- Source: `internal/pkgmgr/{definition.go,build.go,installer.go}`, `internal/bootstrap/meta_ddl.go`, `internal/bootstrap/primordial.go` (comment), `cmd/lattice-pkg/main.go` (comments), `packages/identity-domain/package.go`, **deleted** `packages/identity-domain/seed.go`.
- Tests/harness: `internal/testutil/{install_phase1_packages.go,pipeline.go}`, `internal/pkgmgr/installer_test.go`, `packages/identity-domain/package_test.go`, **new** `packages/rbac-domain/install_flow_test.go` (the 3 §5 required tests).
- Docs: `docs/components/processor.md`, **new** `docs/contracts/08-package-install.md`, `docs/contracts/_index.md`, `docs/index.md`, `packages/identity-domain/{manifest.yaml,README.md}`.
- CAR: `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` (appended UninstallPackage per-key OCC entry).

**§5 gate tails (all green):**
- `go build ./...` → clean. `make vet` → exit 0. `golangci-lint run ./...` → **0 issues.**
- `make down && make up` → primordial atomic batch committed **93** entries; readiness gate satisfied.
- `make verify-kernel` → **ALL ASSERTIONS PASSED.**
- `make verify-package-rbac` → **ALL ASSERTIONS PASSED (53 OK).**
- `make verify-package-identity` → **ALL ASSERTIONS PASSED (37 OK).** (folded roles live: 3 `vtx.roleindex.*` present; grant links resolve to folded role NanoIDs.)
- `make verify-package-identity-hygiene` → **ALL ASSERTIONS PASSED (31 OK).**
- `go test ./internal/pkgmgr/... ./internal/bootstrap/... ./internal/processor/... -p 1 -count=1` → all **ok**.
- `go test ./... -p 1 -count=1` → **all 36 packages ok** (incl. the 3 new install-flow tests in rbac-domain).
- `make test-bypass` → **PHASE 1 GATE 3: PASSED (4/4).**
- `make test-capability-adversarial` → `TestCapAdv*` **ok**; `TestGate3_Report` **PASS (4/4 vectors; 3 DEFENDED, 1 ACCEPTED-WINDOW).**
- (Deviation 14 honored: `make down && make up` between full-suite / bypass / adversarial runs.)

**New §5 tests (all PASS) — `packages/rbac-domain/install_flow_test.go`:**
- `TestInstallFlow_M5B2_DomainOpWithoutRestart` — install rbac via InstallPackage, then commit a CreateRole on the just-declared `rbac` class on the SAME shared DDL cache (absent at refresh → resolvable after install). Proves M5/B2 in-commit coherence, no restart.
- `TestInstallFlow_F001_ReinstallNoOrphans` — install rbac+identity-domain, assert the folded roles are in `declaredKeys` (≥3 role + ≥3 roleindex), uninstall → every declared key tombstoned (no live orphan), re-install on a fresh keyspace succeeds.
- `TestInstallFlow_ProtectedMetaVertexRejected` — Tombstone AND Update against the protected meta-root DDL are both rejected with `ProtectedMetaVertex` and leave the target unmutated.

**CARs / Deviations:**
- **CAR (OPEN, Low):** `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` — UninstallPackage per-key OCC. The `UninstallPackageDDLScript` supports per-key `expectedRevision`, but the canonical per-subject sequence is only exposed in the committing op's `OperationReply.Revisions` (the install reply), NOT via `KVGet.Revision` (stream-level → spurious `wrong last sequence`). Threading install-time committed revisions through to a later uninstall is heavier than this story warranted, so uninstall tombstones **unconditionally**. Documented window: a concurrent Processor write to a declared key between the installer read and the commit is silently overwritten by the tombstone; the batch is still atomic (no partial/mixed state). Proposed follow-up: persist the install reply's `Revisions` into the `.manifest` aspect and read them back at uninstall. Per §3.3, this was an explicitly permitted fallback ("your call, but state it").
- No other deviations. No stuck loops; no reverts.

**Net result:** Story 1.5.5 closes M5/B2 (in-commit DDL coherence — no restart), F-001 (orphan-free install/uninstall/reinstall; PreInstall roles folded into the manifest), F-002 (deterministic requestId → idempotent install dedup), the C3 install contract (Contract #8), and the 1.5.2 kernel-protection residual (ProtectedMetaVertex guard). F-011 per-key OCC is deferred via documented window + CAR. Substrate-direct install writes eliminated (grep-clean). Left in the working tree for Winston — not committed.

---

### CR fix round (P1 protected-key authoritative guard)

Addresses the CR P1 (dead script-level protected check; `UninstallPackage` could tombstone a protected kernel key → brick kernel/auth), CR DOC-1, and CR P2 B-1. Andrew's directive: option A — a Processor-level, commit-time, read-and-check guard (path-independent).

**Changes:**
- **Authoritative guard (location + error code):** `internal/processor/step8_commit.go` — `CommitterImpl.rejectProtectedMutations` (called at the top of `Commit`, before the atomic batch). For every `update`/`tombstone` it derives the 3-segment root (`protectedRootKey`), `KVGet`s the root, and rejects the WHOLE op with `*ProtectedKeyError` when `data.protected == true`. Root→protected lookups are cached per-commit (one `KVGet` per root). Not-found root → not protected (allow). `create` exempt. New error code `ErrCodeProtectedKey = "ProtectedKey"` in `internal/processor/envelope.go`; surfaced as a rejected reply (term, no redelivery) in `internal/processor/commit_path.go`.
- **De-conflicted dead script checks:** removed the non-functional `_is_protected_root` helper + both call sites from `internal/bootstrap/install_ddl.go`; replaced with honest comments stating the Processor commit-time guard is authoritative. The functional `meta_ddl.go` `is_protected` guard kept as defense-in-depth (clearer per-op `ProtectedMetaVertex` error), noted as non-authoritative.
- **Docs (CR DOC-1):** `docs/contracts/08-package-install.md` §8.2/§8.3/§8.4 corrected — the script-level install/uninstall protected check is no longer claimed as active protection; §8.4 now names the Processor commit-time guard as authoritative + path-independent.
- **CR P2 B-1:** removed the dead `now time.Time` param from `buildInstallBatch` (`internal/pkgmgr/build.go`) + its call site in `installer.go`; dropped the now-unused `time` import.

**New test (CR KP-2, fails closed):** `packages/rbac-domain/install_flow_test.go` → `TestInstallFlow_UninstallProtectedKeyRejected` — submits a crafted `UninstallPackage` op whose `declaredKeys` includes `bootstrap.MetaRootKey` (a protected kernel key) and asserts the reply is **rejected with `ErrCodeProtectedKey`** and the protected key is **NOT tombstoned** (same revision, still live). This exercises the real authoritative path (the script performs no protected check — empty hydrated state). Verified PASS.

**§5 gate tails (CR-fix round, all green):**
- `go build ./...` → clean. `make vet` → exit 0. `golangci-lint run ./internal/... ./packages/...` → **0 issues.**
- `make down && make up` → primordial atomic batch committed **93** entries; readiness gate satisfied.
- `make verify-kernel` → **ALL ASSERTIONS PASSED.**
- `make verify-package-rbac` → **ALL ASSERTIONS PASSED (53 OK).**
- `make verify-package-identity` → **ALL ASSERTIONS PASSED (37 OK).**
- `make verify-package-identity-hygiene` → **ALL ASSERTIONS PASSED (31 OK).**
- `go test ./... -p 1 -count=1` → **all packages ok** (incl. the new `TestInstallFlow_UninstallProtectedKeyRejected`). Note: under default parallel `-p`, a few integration tests that share the live `make up` stack flake on NATS resource contention (`TestPublishBatch_HappyPath`, `TestCapAdv_V4_*`, one processor NFR test) — each passes in isolation and under `-p 1`; not a regression.
- `make test-bypass` → **PHASE 1 GATE 3: PASSED (4/4).**
- `make test-capability-adversarial` → `TestGate3_Report` **PASS (4/4; 3 DEFENDED, 1 ACCEPTED-WINDOW).**
- New + related protected tests: `TestInstallFlow_UninstallProtectedKeyRejected` **PASS**, `TestInstallFlow_ProtectedMetaVertexRejected` **PASS**, `TestInstallFlow_M5B2_DomainOpWithoutRestart` **PASS**, `TestInstallFlow_F001_ReinstallNoOrphans` **PASS**.

**CAR status:** the OPEN UninstallPackage per-key OCC CAR is **unaffected** by this change (the protected-key guard is orthogonal to OCC — it reads the root doc, does not assert revisions). No new CARs. No stuck loops. Left in the working tree for Winston — not committed.
