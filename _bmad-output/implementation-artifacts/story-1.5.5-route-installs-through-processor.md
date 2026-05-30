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

_(checkpoint findings appended below once rbac-domain is green)_
