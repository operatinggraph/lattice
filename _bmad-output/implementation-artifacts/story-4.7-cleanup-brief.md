---
title: Story 4.7 Cleanup — Handoff Brief
story: 4.7-cleanup — Close the four Phase-1 carries logged after Story 4.7 shipped
model_tier: Opus (locked)
token_budget: ~130K (tracked, not enforced)
session: Fresh implementation session (round 2; prior attempt reverted)
architecture_lead: Winston
date: 2026-05-22
predecessor: Story 4.7 (commit 7f6d583)
---

# Story 4.7 Cleanup — Handoff Brief

## Background

Story 4.7 shipped the bootstrap kernel minimization and moved identity + rbac into installable Capability Packages. It left four explicit carries:

1. Processor test cutover to `InstallPhase1Packages` deferred — identity/rbac DDL-behavior tests live in `internal/processor/` but exercise *domain* behavior, not processor mechanism. They should live with the packages.
2. Identity DDL + RoleMgmt scripts duplicated between `internal/bootstrap/` (legacy accessors) and `packages/` (canonical).
3. Legacy bootstrap accessor surface is dead code with stale `grantsPermission` literals.
4. `verify-package-*` Makefile gates shell `lattice-pkg list` rather than asserting per-package KV state.

The PO has explicitly flagged: "identity-related tests in the processor folder. Should they be there?" The answer is no — after 4.7, identity behavior is package-scoped, and tests belong with the package.

## 🔴 Non-negotiable rules

Read these slowly. A prior sub-agent attempt was reverted for violating them.

1. **"Move" means the file lives at the new path.** In-place modification with a renamed import is NOT a move. At session close, the result of
   ```
   ls internal/processor/identity_*_test.go internal/processor/role_*_test.go 2>/dev/null
   ```
   must be empty (or all matched files deleted). The result of
   ```
   ls packages/identity-domain/*_test.go packages/rbac-domain/*_test.go
   ```
   must include the ported test files (beyond the existing `package_test.go` in each).

2. **"Delete" is not a substitute for "move."** Test coverage is an asset. The prior attempt deleted `role_mgmt_integration_test.go` (558 LOC) and `role_mgmt_starlark_test.go` (268 LOC) entirely, rationalising that "the new rbac DDL has a different shape." That is **not acceptable**:
   - The integration tests (`TestRoleMgmt_CreateRole`, `_AssignRole`, `_RevokeRole`, `_UnauthorizedDenied`, `_AuditViaCapKV`) are structure-agnostic — they submit ops and assert outcomes through the commit pipeline + Capability KV. They must be **ported**, not deleted, to exercise the new single-`rbac` DDL with 10 op branches.
   - The Starlark unit tests (`TestStarlark_RoleDDL_*`, `TestStarlark_HoldsRoleDDL_*`, `TestStarlark_GrantsPermissionDDL_*`, etc.) test per-DDL script branches. The DDLs were 5; now there is 1 (`rbac`) with 10 branches. The tests must be **ported** to per-branch tests against the new single script, not deleted.
   - `TestStarlark_ReportsTo_AssignReportingChain` — `reportsTo` is genuinely outside the `rbac-domain` package's scope (it was a Story 3.6 surface). Determine where it should live (probably stays bootstrap-internal as a fixture demo, or moves to a future `org-hierarchy` package). If unclear, port to a new `packages/rbac-domain/reports_to_test.go` with a TODO to relocate later — do NOT silently delete.

3. **If "the export surface required to relocate is too costly," STOP and append a CAR.** Do not silently rewire in place as a workaround. The cost of exposing previously-internal processor helpers (`HydratorImpl`, `ExecutorImpl`, capability-doc builders, pipeline factories) is a real architectural decision — surface it via `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` if you hit it. Continue work on other deliverables while waiting for a response.

4. **The canonical pattern**: test files move to `packages/identity-domain/*_test.go` or `packages/rbac-domain/*_test.go` with `package identitydomain_test` or `package rbacdomain_test` (external test package). They import `internal/processor` + `internal/substrate` + `internal/refractor` + `internal/testutil` + the package itself. Test setup calls `testutil.InstallPhase1Packages(t, conn)` after `bootstrap.SeedPrimordial(...)`. If processor-internal helpers are needed but unexported, **export them** (rename the Go symbol to PascalCase) as the natural consequence — that's the architectural shape of "tests of package X live with X."

5. **Architectural-purity guardrails (same as 4.6/4.7)**:
   - No new ContextHint fields. No scans, no Adjacency reads, no lens-output reads from Processor.
   - Don't touch the package script bodies in `packages/{identity-domain,rbac-domain}/ddls.go`. They are canonical.
   - At session close, `grep -rn "AdjacencyReads\|LinkScans\|ScanPrefixes\|WithAdjacencyBucket\|AdjacencyForNode\|keys_with_prefix" internal/ cmd/ packages/` must return zero operational hits.

## Operating rules

- `cd /Users/andrewsolgan/Documents/GitHub/Lattice` and stay there. Do NOT use worktrees.
- You are the implementer. Do NOT commit or push — Winston commits after review. Leave changes unstaged.
- You MAY NOT edit `_bmad-output/planning-artifacts/{data-contracts.md,epics.md,lattice-architecture.md,MORPH-DEVIATIONS.md}`. CARs via `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md`.
- Model: Opus. Token budget ~130K (tracked, not enforced).
- Halt only on stuck-loop criteria (3+ re-attempts on same failure, immediate reverts, cycling between failed approaches, unresolved test failure after 2 debug attempts). Cost-of-relocation is NOT a halt criterion — it's a CAR signal.

## Scope (4 deliverables, this is one PR)

### 1. Move + port identity tests to `packages/identity-domain/`

Source files in `internal/processor/`:

| Source | Tests | Destination |
|---|---|---|
| `identity_create_test.go` (684 LOC) | `TestCreateUnclaimed_Success`, `_MissingName_Rejected`, `_MissingBothContacts`, `_NormalizesEmailCase`, `_DuplicateEmail_RemainsUnclaimed`, `_NonStaffActor_Denied`, `_Idempotent`, `_ClaimKeyHashOnly` | `packages/identity-domain/create_test.go` |
| `identity_claim_test.go` (952 LOC) | `TestClaimIdentity_Success`, `_WrongKey_GenericError`, `_AlreadyClaimed_GenericError`, `_Flagged_GenericError`, `_Merged_GenericError`, `_CredentialAlreadyBound_GenericError`, `_FR5_GrandfatheredFlow`, `_FR5_ImmediateAccess` | `packages/identity-domain/claim_test.go` |
| `identity_state_machine_test.go` (665 LOC) | `TestIdentity_StateMachine_AllowedTransitions`, `_RejectsDisallowed`, `TestIdentity_MergedGuard_RejectsMutation`, `TestIdentity_FR7_LeaseTombstoneDoesNotCascade`, `TestIdentity_RolePermissionGrantsProjected` | `packages/identity-domain/state_machine_test.go` |

**Rewiring**: each test currently builds a synthetic DDL via `bootstrap.IdentityDDL()` (about to be deleted in §3 below) and writes it to test KV by hand. After the move, tests use `testutil.InstallPhase1Packages(t, conn)` after `bootstrap.SeedPrimordial(...)` — the installer puts real DDLs + permissions + grants in KV exactly as production sees them.

**Preserve exactly:**
- NFR-S6 anti-enumeration error message shapes (`ClaimKeyInvalid` is generic — must remain generic; the test asserts this).
- `ClaimIdentity` `scope: self` — envelopes must carry `AuthContext{Target: actorKey}` (pattern already in the file).
- Capability-mode vs stub-mode auth distinctions where tests rely on them. Most tests can move from stub-auth to capability-auth now that real grants are installed; but preserve assertion intent.
- The TestMain-style fixture if any (consolidate per-package).

**Helper consolidation**: helpers private to these tests (DDL seeders, capability-doc builders, pipeline factories) move with them. If two destination files share a helper, factor into `packages/identity-domain/testhelpers_test.go`. Helpers must NOT cross package boundaries — `packages/rbac-domain/` gets its own helpers.

**`TestIdentity_FR7_LeaseTombstoneDoesNotCascade`**: substrate-level revision invariance demonstrated through an identity vertex. Keep in `packages/identity-domain/state_machine_test.go`; FR7 is conceptually about identity cascade isolation.

### 2. Move + port rbac tests to `packages/rbac-domain/`

Source files in `internal/processor/`:

| Source | Tests | Destination |
|---|---|---|
| `role_mgmt_integration_test.go` (558 LOC) | `TestRoleMgmt_CreateRole`, `_AssignRole`, `_RevokeRole`, `_UnauthorizedDenied`, `_AuditViaCapKV` | `packages/rbac-domain/integration_test.go` |
| `role_mgmt_starlark_test.go` (268 LOC) | `TestStarlark_RoleDDL_*` × 3, `TestStarlark_PermissionDDL_CreatePermission`, `TestStarlark_HoldsRoleDDL_*` × 2, `TestStarlark_GrantsPermissionDDL_GrantPermission`, `TestStarlark_ReportsTo_AssignReportingChain`, `TestStarlark_AllScriptsParse` | `packages/rbac-domain/starlark_test.go` |

**Integration tests** (5 tests) are structure-agnostic — they submit ops through a real pipeline and assert outcomes. Port directly: change setup to `testutil.InstallPhase1Packages`, drop any direct DDL-seeding boilerplate, assert against the new single-`rbac` DDL.

**Starlark unit tests** require structural rewrite. The pre-4.7 shape had 5 separate DDLs (role, permission, holdsRole, grantsPermission, reportsTo) each with its own script. The post-4.7 shape has ONE `rbac` DDL with 10 op branches handling all of them. Port the tests to **per-op-branch** form against the single rbac script:
- `TestStarlark_RoleDDL_CreateRole` → `TestStarlark_Rbac_CreateRole`
- `TestStarlark_HoldsRoleDDL_AssignRole` → `TestStarlark_Rbac_AssignRole`
- `TestStarlark_GrantsPermissionDDL_GrantPermission` → `TestStarlark_Rbac_GrantPermission`
- ...etc. Each runs the rbac script with an envelope carrying the op as `op.operationType`, asserts the expected mutations.
- `TestStarlark_AllScriptsParse` → `TestStarlark_Rbac_Parses` (single script now).
- `TestStarlark_ReportsTo_AssignReportingChain` — `reportsTo` is NOT in the rbac-domain package today (it was a separate Story 3.6 DDL). Two options:
  - (a) Confirm `reportsTo` is in `internal/bootstrap/` still as a primordial DDL — if so, the test can live in `internal/bootstrap/role_mgmt_test.go` (NEW) or stay test-orphaned. Verify before deleting.
  - (b) If `reportsTo` has no current home, port the test to `packages/rbac-domain/reports_to_test.go` with a `// TODO: relocate when reportsTo lands in its own package`. Do NOT silently delete.

### 3. Delete the legacy bootstrap accessor surface

After §1 and §2 are complete, NOTHING outside `internal/bootstrap/` should reference these symbols:
- `bootstrap.IdentityDDL`, `bootstrap.IdentityDDLEntry`, `bootstrap.IdentityPermissions`, `bootstrap.IdentityPermEntry`, `bootstrap.IdentityGrants`, `bootstrap.IdentityGrantSpec`
- `bootstrap.RoleMgmtDDLs`, `bootstrap.RoleMgmtDDLEntry`, `bootstrap.RoleMgmtPermissions`, `bootstrap.RoleMgmtPermEntry`, `bootstrap.RoleMgmtGrants`, `bootstrap.RoleMgmtGrantSpec`
- `bootstrap.DDLGrantsPermissionID`, `bootstrap.DDLGrantsPermissionKey`
- `bootstrap.IdentityPermissionKeys`, `bootstrap.IdentityGrantLinkKeys`, `bootstrap.RoleMgmtPermissionKeys`, `bootstrap.RoleMgmtGrantLinkKeys`
- The per-op identity + rbac permission NanoID constants (`PermCreateUnclaimedIdentityID`, etc.) — only delete those with zero remaining callers.

**Files to delete entirely:**
- `internal/bootstrap/identity_ddl.go`
- `internal/bootstrap/role_mgmt_ddl.go`

**File to trim** (not delete; it still holds operator role + meta-permission NanoIDs which `primordial.go` uses):
- `internal/bootstrap/nanoid.go` — remove the identity/rbac-specific surfaces listed above; keep operator role + meta-perm NanoIDs.

**Procedure**: delete the two files first, run `go build ./...`, follow compile errors to find remaining callers. Each remaining caller is a stale reference — fix or delete it. Iterate until the build is clean.

### 4. Strengthen verify-package gates

Currently `make verify-package-rbac`, `make verify-package-identity`, `make verify-package-identity-hygiene` shell `lattice-pkg list` — detects total install failure but not partial corruption.

Author three real assertion scripts modeled on `scripts/verify-kernel.go`:

- **`scripts/verify-package-rbac.go`** — after install, assert:
  - `vtx.meta.<rbacDDL-NanoID>` exists with `class=meta.ddl.vertexType`, `canonicalName=rbac`, `permittedCommands` containing all 10 ops, `.script` non-empty
  - 10 permission vertices (`vtx.permission.<NanoID>` × 10), each with `class=permission` + `operationType` + `scope` aspects
  - 10 `grantedBy` link keys (each permission → operator role)
  - `vtx.package.<NanoID>.manifest` aspect carrying manifest JSON with `name=rbac-domain`
  - Target ~30 OK lines.

- **`scripts/verify-package-identity.go`** — after install, assert:
  - `vtx.meta.<identityDDL-NanoID>` exists with `permittedCommands=[CreateUnclaimedIdentity, UpdateIdentityState, ClaimIdentity]`
  - 3 permission vertices (CreateUnclaimedIdentity scope=any; UpdateIdentityState scope=any; ClaimIdentity scope=self)
  - Grant link keys: CreateUnclaimedIdentity → operator+frontOfHouse+backOfHouse; UpdateIdentityState → operator; ClaimIdentity → consumer
  - 3 user-facing role vertices (consumer, frontOfHouse, backOfHouse) seeded by PreInstall hook
  - Package manifest vertex
  - Target ~30 OK lines.

- **`scripts/verify-package-identity-hygiene.go`** — after install, assert:
  - `vtx.meta.<identityHygieneDDL-NanoID>` with `permittedCommands=[MergeIdentity]`
  - `MergeIdentity` permission vertex with `scope=any` + `grantedBy` link to operator
  - `vtx.meta.<duplicateCandidatesLens-NanoID>` with `class=meta.lens` + `.spec` containing `secondaryInboundEdges` + `secondaryOutboundEdges` + `levenshteinRatio`
  - Package manifest vertex
  - Target ~20 OK lines.

**Style**: each assertion logs `OK: <description>` or `FAIL: <description>: <reason>`, script exits 1 on any FAIL. Mirror `verify-kernel.go`.

**Makefile**: update `verify-package-*` targets to invoke `go run scripts/verify-package-<name>.go` (matching the `verify-kernel` pattern).

**CI workflow**: already calls these targets; should require no changes.

## Verification gates

- `go build ./...`
- `make vet`
- `golangci-lint run ./...` — 0 issues
- `go test ./... -p 1 -count=1` — all packages green (note: use `-p 1`; cross-package parallel mode hits Deviation 14 flakes)
- `make verify-kernel` — should be unchanged from 4.7
- `make verify-package-rbac`, `make verify-package-identity`, `make verify-package-identity-hygiene` — all pass with the new assertion scripts
- `make test-bypass` (4/4 BLOCKED)
- `make test-capability-adversarial` (4/4 DEFENDED)

If Docker isn't available, run the non-Docker gates locally; flag the Docker-dependent ones for Winston to verify on CI.

## Closing-grep checks (run + report verbatim in closing summary)

```
ls internal/processor/identity_*_test.go internal/processor/role_*_test.go 2>/dev/null     # must be EMPTY
ls packages/identity-domain/*_test.go packages/rbac-domain/*_test.go                       # must include the moved files
grep -rn "AdjacencyReads\|LinkScans\|ScanPrefixes\|WithAdjacencyBucket\|AdjacencyForNode\|keys_with_prefix" internal/ cmd/ packages/   # zero operational hits
grep -rn "bootstrap\.IdentityDDL\|bootstrap\.RoleMgmtDDLs\|bootstrap\.IdentityPermissions\|bootstrap\.IdentityGrants\|bootstrap\.RoleMgmtPermissions\|bootstrap\.RoleMgmtGrants\|bootstrap\.DDLGrantsPermission\|bootstrap\.IdentityPermissionKeys\|bootstrap\.IdentityGrantLinkKeys\|bootstrap\.RoleMgmtPermissionKeys\|bootstrap\.RoleMgmtGrantLinkKeys" --include="*.go" .   # zero hits
grep -rn "ContextHint{" packages/   # review each — must be Reads of known keys only
```

## When you finish

Append a closing summary to `_bmad-output/implementation-artifacts/story-4.7-cleanup-closing-summary.md` with:
- Files moved (source → destination, LOC before/after)
- Files deleted (full paths)
- New scripts authored
- Test rewiring approach (helper consolidation, auth-mode shifts, any tests that needed substantive rewrites vs pure mechanical moves)
- Decisions on any tests that required judgment (e.g. `reportsTo` placement)
- Gate results (each gate + PASS/FAIL/skipped + reason)
- The five closing-grep outputs verbatim
- Any deviations from this brief — for each, the rule violated + the reason. If you find yourself writing "the brief said X but I did Y because Z," you are flagging your deviation correctly; do not call it "interpretation."
- Any open CARs
- Token self-estimate

Then stop.

## Out of scope

- Don't touch `packages/identity-hygiene/` scripts or lenses.
- Don't author the 4.6 Surface-B end-to-end MergeIdentity tests (separate carry; opportunistic only if a tiny smoke test falls naturally out of the identity-domain harness setup).
- Don't modify the Capability Lens cypher or the cap-entry key prefix.
- Don't introduce new ContextHint fields or any lens-output read from Processor.

## A note on the prior attempt

A prior sub-agent ran this brief and produced a result Winston had to revert. The deviations:
1. Identity tests were not moved — modified in place in `internal/processor/`, with a defense that exposing internal helpers was "too costly."
2. The two rbac test files were **deleted outright** (826 LOC of coverage) with the rationale that the new DDL shape made them stale.

Both are unacceptable. If you find yourself reaching either of those conclusions, that's a CAR signal, not a path forward. The brief's rules above are not aspirational — they are the success criteria.
