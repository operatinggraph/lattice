---
title: Story 4.7 Implementation Handoff Brief
story: 4.7 — Kernel Minimization + RBAC-Domain + Identity-Domain Packages
model_tier: Opus (locked)
token_budget: ~150K (estimate; for tracking only — not a halt threshold)
session: Fresh implementation session
architecture_lead: Winston
date: 2026-05-19
predecessor: Story 4.6 (Capability Package format + installer + identity-hygiene)
---

# Story 4.7 — Kernel Minimization + RBAC + Identity-Domain Packages: Handoff Brief

## Your Role

Strip the bootstrap kernel down to the minimum needed to authorize the first package install, then move what used to be bootstrap-seeded (RBAC operations + identity domain operations) into installable packages. After 4.7, the Lattice "core" is just enough machinery to bootstrap the platform; every domain — including identity itself — is a package.

Sequence:
1. Author `packages/rbac-domain/` — moves CreateRole / CreatePermission / AssignRole / GrantPermission / RevokeRole / RevokePermission + their inverse ops out of bootstrap into a package.
2. Author `packages/identity-domain/` — moves the identity DDL (CreateUnclaimedIdentity, ClaimIdentity, UpdateIdentityState) out of bootstrap into a package.
3. Shrink the bootstrap kernel to: streams + KV buckets + meta-meta-DDL (one DDL governing all `vtx.meta.*` mutations) + Capability Lens definition + operator role + 3 meta-permissions + 3 grants + 1 primordial admin actor + 1 holdsRole link.
4. Cutover: integration tests that previously relied on bootstrap-seeded identity/role/permission DDLs now install the packages in test setup before running.
5. Verify gates: `verify-kernel` (~25 OK), `verify-package-rbac` (~30 OK), `verify-package-identity` (~25 OK), `verify-package-identity-hygiene` (~20 OK).

This story does NOT touch the Capability Lens cypher shape (the cap-entry key prefix stays `cap.identity.<id>` per the 2026-05-19 default-2b decision — the primordial admin is still a `vtx.identity.<NanoID>` despite the identity DDL itself living in a package; this asymmetry is documented in this brief's Decision #4).

## 🔴 MANDATORY OPERATING RULES (READ FIRST)

- **No worktree.** Work directly in `/Users/andrewsolgan/Documents/GitHub/Lattice`. Verify `pwd` at startup.
- **No commits, no pushes.** Winston commits + pushes after review.
- **Planning artifacts are read-only.** Drift → CAR + continue.
- **Token budget tracking-only.** Estimate ~150K.
- **Halt and escalate** on stuck-loop patterns.
- **Checkpoint every 8-10 tool calls.**
- **Model tier:** Opus only.
- **Architecture binding:** Contract #1 + Contract #6 + `PHASE-1-COURSE-CORRECTION.md` §C-note4 + `docs/components/_packages.md` (Story 4.6 authored this); the kernel composition is fully specified below in §3 — your job is to implement what's specified, not redesign.
- **Lint watch:** `/Users/andrewsolgan/go/bin/golangci-lint run ./...` 0 issues required.
- **Andrew has authorized autonomous proceed.**

## What's Already in Place (do NOT redo)

- **`internal/pkgmgr/` installer** (Story 4.6) — submits packages via `substrate.AtomicBatch`. Same installer handles your two new packages.
- **`cmd/lattice-pkg/` CLI** (Story 4.6) — `install` / `uninstall` / `list` subcommands.
- **Package format** (Story 4.6) — manifest.yaml + ddls.go + lenses.go + permissions.go + README.md per `docs/components/_packages.md`.
- **identity-hygiene package** (Story 4.6) — first real package; depends on identity-domain (which is bootstrap-seeded today, will be a package after this story).
- **Capability Lens definition** at `internal/bootstrap/primordial.go:614` — seeded with `engine: "full"`. Stays in kernel.
- **Refractor full openCypher engine** — stays in kernel-served Refractor; not touched.

Tree is clean at session start (post-4.6 commit).

## Story Scope (4.7)

### 1. RBAC-domain package: `packages/rbac-domain/` (~30K tokens)

A package providing all role/permission/grant management operations.

**manifest.yaml:**
```yaml
name: rbac-domain
version: 0.1.0
description: Role, permission, and grant management operations.
depends: []  # rbac-domain has no package dependencies; it bootstraps role/perm ops
declares:
  ddls:
    - canonicalName: rbac
      class: meta.ddl.vertexType  # vertex DDL handling role + permission vertex ops
  permissions:
    - operationType: CreateRole
      scope: any
      grantsTo: [operator]
    - operationType: UpdateRole
      scope: any
      grantsTo: [operator]
    - operationType: TombstoneRole
      scope: any
      grantsTo: [operator]
    - operationType: CreatePermission
      scope: any
      grantsTo: [operator]
    - operationType: UpdatePermission
      scope: any
      grantsTo: [operator]
    - operationType: TombstonePermission
      scope: any
      grantsTo: [operator]
    - operationType: AssignRole
      scope: any
      grantsTo: [operator]
    - operationType: RevokeRole
      scope: any
      grantsTo: [operator]
    - operationType: GrantPermission
      scope: any
      grantsTo: [operator]
    - operationType: RevokePermission
      scope: any
      grantsTo: [operator]
```

**ddls.go:** the `rbac` DDL with `.script` aspect containing Starlark handling all 10 operations:
- `CreateRole { name, description }` → `vtx.role.<NanoID>` + `.canonicalName` + `.description` aspects
- `UpdateRole { roleKey, description }` → aspect-update on existing role vertex
- `TombstoneRole { roleKey }` → soft-delete + tombstone link cleanup is left for compensating ops (Phase 1 minimum: soft-delete vertex; orphaned grants tombstoned by operator separately)
- `CreatePermission { operationType, scope }` → `vtx.permission.<NanoID>` + aspects
- `UpdatePermission { permKey, scope }` → aspect-update
- `TombstonePermission { permKey }` → soft-delete (orphaned grants handled separately)
- `AssignRole { actorKey, roleKey }` → `lnk.<actorType>.<actorId>.holdsRole.role.<roleId>` (alphabetical-type ordering per Contract #1 §1.1)
- `RevokeRole { actorKey, roleKey }` → soft-delete the link
- `GrantPermission { permKey, roleKey }` → `lnk.permission.<permId>.grantsPermission.role.<roleId>`
- `RevokePermission { permKey, roleKey }` → soft-delete the link

Each script branch enforces basic input validation (required fields, valid NanoID format, target vertices exist if referenced).

Script LOC target: ~280 LOC total. If you exceed 320, refactor with helper functions before continuing.

**README.md:** describes the package contents + install instructions.

### 2. Identity-domain package: `packages/identity-domain/` (~25K tokens)

A package providing CreateUnclaimedIdentity + ClaimIdentity + UpdateIdentityState operations (the post-Story-4.6 trimmed identity DDL, repackaged).

**manifest.yaml:**
```yaml
name: identity-domain
version: 0.1.0
description: Identity vertex creation, claim, state-machine management.
depends:
  - rbac-domain  # identity-domain seeds roles (consumer, frontOfHouse, backOfHouse) via rbac ops
declares:
  ddls:
    - canonicalName: identity
      class: meta.ddl.vertexType
  permissions:
    - operationType: CreateUnclaimedIdentity
      scope: any
      grantsTo: [frontOfHouse, backOfHouse, operator]
    - operationType: UpdateIdentityState
      scope: any
      grantsTo: [operator]
    - operationType: ClaimIdentity
      scope: self
      grantsTo: [consumer]
```

**ddls.go:** the `identity` DDL — verbatim copy of the post-Story-4.6 trimmed `internal/bootstrap/identity_ddl.go` minus the bootstrap-seeding wrapper. Just the DDL definition + Starlark script handling CreateUnclaimedIdentity + UpdateIdentityState + ClaimIdentity.

**permissions.go:** the 3 permission vertices + grants (per manifest).

**README.md:** describes the package.

**A note on the consumer / frontOfHouse / backOfHouse roles:** these roles don't exist in the post-4.7 kernel (only `operator` does). The identity-domain package's install must FIRST create them via the rbac-domain package's CreateRole op. This means the installer needs to handle multi-step install: install order is rbac-domain first (creates the rbac DDL), then a follow-up step that uses the CreateRole op to seed the roles, then identity-domain (which depends on the roles existing).

**Decision (Winston):** for Phase 1, identity-domain's installer pre-step is a Go-side seed function in `packages/identity-domain/seed_roles.go` that the installer runs **before** the atomic batch — it directly seeds the 3 role vertices via `substrate.AtomicBatch` on `core-kv` (bypassing the rbac op path). This is acceptable because the package is still using substrate-direct writes, just in two stages. Phase 2 will replace with proper CreateRole op submission once Story 5.3 compensating-ops machinery is in place.

Alternative for cleanliness: the installer is extended (in 4.7's scope) to handle a `seed.go` per-package optional pre-step that runs Go code before the atomic batch. This is more general. Decision: implement the generalized hook because identity-domain isn't the last package to need this pattern.

### 3. Kernel shrinkage: `internal/bootstrap/` rewrite (~30K tokens)

The kernel after 4.7 contains ONLY:

**Streams + KV buckets** (unchanged): `core-operations`, `core-events`, `core-kv`, `health`, `capability-kv`, `refractor-adjacency`

**ONE meta-DDL** (the meta-meta-DDL):
- `vtx.meta.<NanoID-root>` with `class: "meta.ddl.vertexType"` and `.canonicalName: "root"`
- `.permittedCommands: ["CreateMetaVertex", "UpdateMetaVertex", "TombstoneMetaVertex"]`
- `.description`: governs all `vtx.meta.*` mutations
- `.script`: Starlark dispatching on `op.payload.targetClass`:
  - `meta.ddl.vertexType` / `meta.ddl.linkType` / `meta.ddl.aspectType` / `meta.ddl.eventType` → validate structural shape (canonicalName, permittedCommands, description, script aspects required), assign NanoID, write vertex + 4 aspects
  - `meta.lens` → validate structural shape (canonicalName, description, spec aspects required), assign NanoID, write vertex + 3 aspects
  - Any other targetClass → reject with `UnknownMetaClass`

Script LOC target: ~200 LOC.

**Two Lens definitions** (Capability Lens + Capability-Role-By-Op index Lens) — unchanged from current bootstrap, just keyed under the meta-DDL pattern. The cypher source is updated for the post-4.7 actor type (still `cap.identity.<id>` per Decision #4 below — the bootstrap admin is still an `identity` vertex).

**Operator role**: ONE primordial role vertex `vtx.role.<NanoID-operator>` with `.canonicalName: "operator"` + `.description`.

**Three meta-permissions**:
- `vtx.permission.<NanoID-pCMV>` with `.operationType: "CreateMetaVertex"`, `.scope: "any"`
- `vtx.permission.<NanoID-pUMV>` with `.operationType: "UpdateMetaVertex"`, `.scope: "any"`
- `vtx.permission.<NanoID-pTMV>` with `.operationType: "TombstoneMetaVertex"`, `.scope: "any"`

**Three grant links** (operator gets all three meta-permissions):
- `lnk.permission.<pCMV>.grantsPermission.role.<operator>`
- `lnk.permission.<pUMV>.grantsPermission.role.<operator>`
- `lnk.permission.<pTMV>.grantsPermission.role.<operator>`

**Primordial admin identity**:
- `vtx.identity.<NanoID-admin>` — type **identity** despite the identity DDL not being in the kernel (Decision #4 below)
- `lnk.identity.<admin>.holdsRole.role.<operator>` — holdsRole link from admin to operator
- The admin identity has NO `.state` aspect (no state machine in the kernel; the identity-domain package, when installed, doesn't retroactively assert state on pre-existing identity vertices)

**Filesystem credential**: `lattice.bootstrap.json` writes the admin NanoID + the operator role NanoID + the three meta-permission NanoIDs for installer + post-bootstrap tooling to read.

**Total kernel vertices + aspects + links**:
- 1 meta-DDL vertex + 4 aspects = 5
- 2 lens meta-vertices × ~3 aspects each = 8
- 1 role vertex + 2 aspects = 3
- 3 permission vertices × 3 aspects each = 12
- 3 grant links = 3
- 1 admin identity vertex = 1
- 1 admin→operator holdsRole link = 1
- **Total: ~33 entries → ~33 OK lines in verify-kernel**

(Compared to current 154 OK in verify-bootstrap. The kernel shrinks by ~120 OK lines worth of structure.)

The kernel writes via `substrate.AtomicBatch` on `core-kv` (single bucket — fits within atomic constraint).

`internal/bootstrap/primordial.go` is heavily rewritten. The current ~700 LOC drops to ~250 LOC (just the meta-DDL + 2 lenses + role + 3 perms + 3 grants + 1 admin + 1 link).

`scripts/verify-bootstrap.go` is renamed `scripts/verify-kernel.go` (or kept-as-is with shrunken expected count). New gates: `verify-package-rbac`, `verify-package-identity`, `verify-package-identity-hygiene` — each shells the installer to install + then runs assertion-style verification of the resulting Core KV state.

### 4. Capability Lens cypher generalization — DEFERRED to Phase 2 (~0K tokens)

Per Decision #4 below, the cap-entry key prefix stays `cap.identity.<id>` in Phase 1. No cypher changes in 4.7. Phase 2 generalizes to `cap.<actorType>.<id>` when other actor types appear (system actors, AI agents, etc.).

The cypher's `MATCH (identity:identity)` clause is **preserved** — the bootstrap admin is a `vtx.identity.<NanoID>` vertex even though identity-domain isn't in the kernel. This is the simplest path; the asymmetry costs nothing.

### 5. Cutover: integration tests (~25K tokens)

Many existing tests rely on bootstrap-seeded identity / role / permission DDLs. After kernel minimization, those DDLs only exist if rbac-domain + identity-domain packages have been installed.

**Test setup helper**: `internal/testutil/install_phase1_packages.go` (NEW) — function `InstallPhase1Packages(t *testing.T, conn *substrate.Conn)` that installs rbac-domain + identity-domain + identity-hygiene. Idempotent (uses the package vertex check). Test packages call this in their test-setup phase (TestMain or per-test helper).

**Migration**: tests in `internal/processor/` (identity_create_test.go, identity_claim_test.go, identity_state_machine_test.go, plus all of Story 3.x) currently assume the DDLs exist. After kernel shrinkage, they call `InstallPhase1Packages(t, conn)` in setup. Mechanical update: add the call after `bootstrap.SeedPrimordial(...)` in each test's setup.

**Bypass + Gate 3 tests**: same — install packages in test setup. Story 1.10's bypass suite and Story 3.7's adversarial suite both rely on real DDLs; same one-line setup migration.

**Hello Lattice + Gate 4/5**: Stories 5.x and 6.x will handle this themselves; not in 4.7's scope.

### 6. CI gate updates (~10K tokens)

- New Makefile target `verify-kernel` — was `verify-bootstrap`; rename. Expected count ~33 OK.
- New Makefile targets `verify-package-rbac`, `verify-package-identity`, `verify-package-identity-hygiene` — each: `make up` to bootstrap, then `lattice-pkg install packages/<name>`, then assertion-style verify.
- CI workflow `.github/workflows/ci.yml` updated to run all four gates in sequence.
- Old `verify-bootstrap` target deleted (the name moves to `verify-kernel`).

### 7. Verify pre-existing carries don't regress (~10K tokens)

The 4.5 stub-mode `MergeIdentity` test carry is resolved: post-4.6 + 4.7, MergeIdentity is in the identity-hygiene package with a real seeded grant. The 4.5 stub-mode tests get rewritten to use the package install path and capability-authorized merge calls.

## Architectural Decisions Already Made (Winston)

1. **Kernel composition is fully specified in §3.** Don't add to it without escalating. The kernel is intentionally minimal.

2. **Two packages installed by default Phase 1 deployment**: rbac-domain + identity-domain. identity-hygiene is optional (installed if duplicate detection is desired). All other domains (lease, work-order, payment, etc.) are out-of-scope examples for the future.

3. **The installer's atomic-batch pattern handles both Phase 1 ("seed via substrate") and Phase 2 ("submit ops") shapes.** 4.7 stays Phase 1 (substrate-direct). The installer code abstracts the write surface so Phase 2 only swaps the internals.

4. **Cap-entry key prefix stays `cap.identity.<id>`** (per the 2026-05-19 default-2b decision in PHASE-1-COURSE-CORRECTION.md). The primordial admin is a `vtx.identity.<NanoID>` even though identity-domain isn't in the kernel. The asymmetry ("primordial identity exists without being managed by the identity DDL") is one-time and small. The Capability Lens cypher does NOT change.

5. **Bootstrap-seeded admin identity has no `.state` aspect.** State is an identity-domain-package concept; the kernel admin pre-exists the package. After identity-domain installs, the admin remains stateless (no retroactive state assertion). This is fine — the state machine validator only fires on UpdateIdentityState ops; the admin doesn't submit those.

6. **Filesystem-bound admin credential** stays per Story 4.6 Decision #4. `lattice.bootstrap.json` is the source-of-truth for admin's NanoID + role NanoIDs + perm NanoIDs needed by the installer.

7. **identity-domain depends on rbac-domain.** Installer enforces dependency order at install-time. rbac-domain install seeds CreateRole + friends; identity-domain install's `seed.go` pre-step uses substrate-direct to seed the 3 user-facing roles (consumer, frontOfHouse, backOfHouse), THEN the atomic batch seeds the identity DDL + permission grants. Two-stage install for identity-domain.

8. **identity-hygiene depends on identity-domain** (Story 4.6 already declared this). After 4.7, that dependency is enforced via the installer's package-presence check.

9. **TombstoneRole / TombstonePermission semantics in rbac-domain**: soft-delete the vertex; do NOT auto-tombstone grant links. Operator must explicitly RevokePermission / RevokeRole before TombstoneRole. Otherwise grants point to a tombstoned vertex which is acceptable Phase 1 behavior (cap projection won't include them; orphans are harmless).

10. **identity-domain's CreateUnclaimedIdentity does NOT depend on the rbac-domain `holdsRole` link existing for the actor**: the actor's cap-entry is already authorized by the Capability Lens (which is in the kernel) based on the actor's pre-existing role assignment. CreateUnclaimedIdentity just writes the identity vertex; it doesn't seed any role for the new identity. The new identity is unclaimed → has no role assignment → its cap-entry is empty → it can do nothing until claimed. Standard flow.

11. **The kernel admin's `holdsRole` link to operator is seeded by the kernel, not by rbac-domain.** Even though `holdsRole` is an rbac-domain-package op type, the link itself is a primordial relationship that exists before rbac-domain installs. This is consistent with Decision #5 (the admin identity is primordial; its relationships are too).

12. **No new Starlark builtins.** 4.7 reuses what's there.

13. **No `data-contracts.md` edits.** All component-level documentation lives in `docs/components/` (Story 6.0) or `packages/<name>/README.md`.

14. **Cutover migration of integration tests is mechanical**, not architectural. Don't redesign tests; just add the `InstallPhase1Packages(t, conn)` call where DDLs are assumed.

15. **`verify-kernel`, not `verify-bootstrap`.** The name change reflects the conceptual shift: bootstrap now produces a kernel, packages add domain machinery on top.

## Required Context — Read These Only

| File | Why |
|---|---|
| `_bmad-output/implementation-artifacts/PHASE-1-COURSE-CORRECTION.md` | Big-picture; especially §C-note4 + the kernel composition discussion |
| `_bmad-output/implementation-artifacts/story-4.6-handoff-brief.md` | Predecessor — package format + installer |
| `_bmad-output/implementation-artifacts/story-4.1-handoff-brief.md` | Identity DDL state-machine + helper conventions (you copy these into the identity-domain package) |
| `_bmad-output/planning-artifacts/data-contracts.md` Contract #1 §1.1 + §1.5 | Key shapes + link ordering |
| `internal/bootstrap/primordial.go` | **Heavily rewrite** — shrink to the kernel composition |
| `internal/bootstrap/identity_ddl.go` | **Move to** `packages/identity-domain/ddls.go` (verbatim with installer-targeted wrapper) |
| `internal/bootstrap/nanoid.go` | **Trim** — kernel needs admin + operator + 3 meta-perm NanoIDs only; rbac/identity NanoIDs move to package install-time |
| `internal/pkgmgr/` | Read-only — Story 4.6 implementation; extend the installer to handle `seed.go` pre-step hook |
| `scripts/verify-bootstrap.go` | **Rename + rewrite** as `scripts/verify-kernel.go`; new expected count ~33 OK |
| `Makefile` | **Edit** — add verify-package-* targets |
| `.github/workflows/ci.yml` | **Edit** — sequence kernel + package gates |
| `internal/processor/identity_*_test.go` | **Edit** — add `InstallPhase1Packages(t, conn)` setup call |
| `internal/processor/identity_state_machine_test.go` | **Edit** — same |
| `internal/bypass/*` | **Edit** — same |
| `cmd/lattice-pkg/` | Read-only; extend if `seed.go` hook needs CLI surface (it doesn't — it runs at install time) |

**DO NOT read**: `lattice-architecture.md` (full), full `epics.md` beyond Epic 4 framing, Stories 1.x/2.x/3.x briefs.

## Suggested Sequence

**Phase A — Package format installer hook (target ~10K tokens):**
1. Extend `internal/pkgmgr/installer.go` to look for and invoke a `seed.go` per-package pre-step. The pre-step is a Go function `func PreInstall(conn *substrate.Conn, adminKey string) error` exported by the package's seed.go file. If present, it runs BEFORE the atomic batch.

**Phase B — rbac-domain package (target ~30K tokens):**
2. Author `packages/rbac-domain/{manifest.yaml,ddls.go,permissions.go,README.md}` per §1.

**Phase C — identity-domain package (target ~30K tokens):**
3. Author `packages/identity-domain/{manifest.yaml,ddls.go,permissions.go,seed.go,README.md}` per §2. The `seed.go` PreInstall seeds the 3 user-facing roles.

**Phase D — Kernel rewrite (target ~30K tokens):**
4. Rewrite `internal/bootstrap/primordial.go` per §3. Verify with a one-shot `go run scripts/verify-kernel.go` (after authoring it).
5. Rename + rewrite `scripts/verify-bootstrap.go` → `scripts/verify-kernel.go`.
6. Update `Makefile`.

**Phase E — Test cutover (target ~25K tokens):**
7. Author `internal/testutil/install_phase1_packages.go`.
8. Add the setup call to each integration-test package that uses identity/role/permission DDLs.

**Phase F — CI gate sequence (target ~10K tokens):**
9. Update `.github/workflows/ci.yml` to run all four gates.

**Phase G — Closing (target ~15K tokens):**
10. Run all gates locally.
11. Update token tracker Row 4.7.
12. Closing summary appended.

## Required Verification

```bash
go build ./...
make vet
/Users/andrewsolgan/go/bin/golangci-lint run ./...

# Old gate -> new gate
make verify-kernel                            # ~33 OK
make up                                       # boots Lattice with shrunken kernel
./bin/lattice-pkg install packages/rbac-domain
make verify-package-rbac                      # ~30 OK
./bin/lattice-pkg install packages/identity-domain
make verify-package-identity                  # ~25 OK
./bin/lattice-pkg install packages/identity-hygiene
make verify-package-identity-hygiene          # ~20 OK
make test-bypass                              # 4/4 BLOCKED
make test-capability-adversarial              # 4/4 DEFENDED
go test ./... -p 1 -count=1                   # all green
```

## Deliverables Checklist

1. ✅ `internal/pkgmgr/installer.go` — `seed.go` pre-step hook
2. ✅ `packages/rbac-domain/` — full package
3. ✅ `packages/identity-domain/` — full package incl. seed.go
4. ✅ `internal/bootstrap/primordial.go` — rewritten to ~33-OK kernel
5. ✅ `internal/bootstrap/nanoid.go` — trimmed
6. ✅ `scripts/verify-kernel.go` (renamed from verify-bootstrap)
7. ✅ `Makefile` — verify-kernel + verify-package-* targets
8. ✅ `.github/workflows/ci.yml` — gate sequence
9. ✅ `internal/testutil/install_phase1_packages.go` — test setup helper
10. ✅ Migration of integration tests to call the helper
11. ✅ All gates green
12. ✅ Token tracker Row 4.7 updated
13. ✅ Closing summary

## What 4.7 Is NOT

- Not Capability Lens cypher changes (cap-entry key shape stays `cap.identity.<id>`)
- Not the package-as-ops install path (still substrate-direct; Phase 2 + Story 5.3 follow-up does that)
- Not adding multiple actor types (only `identity` exists in Phase 1)
- Not new domain packages (lease, work-order, payment — Phase 2 examples)
- Not real NATS auth
- Not the Refractor token eviction (Story 2.4a)
- Not the Refractor Lattice-native source plane (Story 2.4b)

## Escalation

Append to `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` for:
- The kernel composition in §3 cannot be expressed in a single atomic batch (size or constraint issue)
- The `seed.go` pre-step pattern conflicts with substrate connection lifecycle
- Test cutover reveals tests that fundamentally depend on a DDL being bootstrap-seeded (not just installed-at-test-setup)

Halt for:
- Bypass / Gate 3 vector flips
- Stuck-loop pattern

## Closing

1. Verify all 13 deliverables
2. Run all required gates
3. Update token tracker Row 4.7
4. Closing summary

**DO NOT commit. DO NOT push.** Winston commits + pushes after review.
