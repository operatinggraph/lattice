# Phase 1.5 CR — Capability Packages Fix Summary

## Skipped (Story #15 rewrite territory)

- **F-001** PreInstall orphans — skipped
- **F-002** Concurrent install TOCTOU — skipped
- **F-004** Version upgrade path logic — skipped
- **F-011 logic** — only doc caveat applied (see F-011 below)

---

## Applied Fixes

### P1

| Finding | File:Line | Description |
|---------|-----------|-------------|
| F-003 | `internal/pkgmgr/installer.go:229-240` | Added `checkCoreBucketExists()` pre-flight that returns `ErrBootstrapRequired` when `core-kv` bucket is absent |
| F-003 | `internal/pkgmgr/installer.go:224-227` | Declared `ErrBootstrapRequired` sentinel |
| F-003 | `cmd/lattice-pkg/main.go:65-71` | CLI layer catches `ErrBootstrapRequired` and prints "core-kv bucket not found — run `lattice-pkg bootstrap` (or `make up`) before installing packages." |
| F-005 | `packages/rbac-domain/manifest.yaml:12` | Added explicit `lenses: []` section for consistency with lens-bearing packages |

### P2

| Finding | File:Line | Description |
|---------|-----------|-------------|
| F-006 | `packages/identity-domain/manifest.yaml:3-7` | Stripped story-history narrative from description; replaced with functional description only |
| F-007 | `packages/identity-hygiene/manifest.yaml:8` | Removed `# in-bootstrap identity DDL; Phase 1 warns-and-proceeds` comment |
| F-007 | `packages/identity-hygiene/README.md:55-60` | Stripped "Story 5.3 / Phase 2 will route through CreateMetaVertex ops" from "Phase 1 carries" section; renamed section "Install notes" with current-state description |
| F-008 | `scripts/verify-package-rbac.go` | Added `pkgverify.CheckAspectEnvelope()` calls for all 8 DDL aspects + manifest aspect |
| F-008 | `scripts/verify-package-identity.go` | Added `pkgverify.CheckAspectEnvelope()` calls for all 8 DDL aspects + manifest aspect |
| F-008 | `scripts/verify-package-identity-hygiene.go` | Added `pkgverify.CheckAspectEnvelope()` calls for all DDL + Lens aspects + manifest aspect |
| F-009 | `scripts/pkgverify/verify.go` (new file) | Extracted shared infrastructure: `ListAllKeys`, `GetEnvelope`, `CheckAspectEnvelope`, `FindMetaByCanonical`, `FindPackageManifest`, `ToStringSlice`, `ToSet`, `EnvOrDefault` |
| F-009 | `scripts/verify-package-rbac.go` | Rewrote to use `pkgverify` library; `//go:build ignore` + `package main` contract preserved; Makefile `go run ./scripts/verify-package-rbac.go` still works |
| F-009 | `scripts/verify-package-identity.go` | Rewrote to use `pkgverify` library |
| F-009 | `scripts/verify-package-identity-hygiene.go` | Rewrote to use `pkgverify` library |
| F-010 | `internal/pkgmgr/installer.go:143-153` | Added post-`resolveGrants` validation: any `GrantsTo` entry that is not a valid NanoID (via `substrate.IsValidNanoID`) returns an error before batch submission |
| F-011 (doc) | `internal/pkgmgr/build.go:272-277` | Added doc caveat: "concurrent Processor writes to these keys between the read phase and batch submission will be silently overwritten; per-key sequence numbers are a future improvement" |

**F-009 approach chosen:** `scripts/pkgverify/` as an importable normal Go package under the module. The three per-package scripts remain `//go:build ignore` + `package main` and are still invoked via `go run ./scripts/verify-package-*.go`. The shared library is importable at `github.com/operatinggraph/lattice/scripts/pkgverify`. This is the most direct approach and avoids a combined single-script workaround.

### Nits

| Finding | File:Line | Description |
|---------|-----------|-------------|
| N-002 | `internal/pkgmgr/manifest.go:115-141` | Added `crossCheckGrantsTo()` to `VerifyAgainstDefinition`: compares manifest `grantsTo` list against Go `Definition.GrantsTo` (set equality) for each permission |
| N-004 | `packages/rbac-domain/reports_to_test.go` | **Deleted** permanently-skipped dead test file (reportsTo retired; git blame is the record) |

N-001, N-003, N-005 — addressed within F-009 refactor and history comment sweep.

---

## History Comments (37 instances, 16 files)

All in-scope Story X.Y references stripped from production code, package YAML, and test files. Error message strings in `build.go` cleaned (e.g., `"InputSchema required (Story 5.1)"` → `"InputSchema required"`). Full list:

| File | Change |
|------|--------|
| `internal/pkgmgr/definition.go` | Removed Story 5.3, 5.1, 4.7 refs; stripped Phase 2 forward-looking text |
| `internal/pkgmgr/build.go` | Removed Story 5.1 (×4 in error strings), 4.7, 1.1; stripped Phase 2 carry |
| `internal/pkgmgr/installer.go` | Removed Story 1.1 ref; cleaned Phase 1 warn-and-proceed message |
| `cmd/lattice-pkg/main.go` | Removed Story 5.3, 4.7 refs; stripped Phase 2 carry from package doc |
| `packages/identity-domain/manifest.yaml` | Stripped "Replaces ... Story 4.1/4.6/4.7" from description |
| `packages/identity-domain/package.go` | Removed "post-Story-4.6", "Story 4.7" |
| `packages/identity-domain/ddls.go` | Removed "Verbatim copy of post-Story-4.6", "Story 4.7" in description, script comment |
| `packages/identity-domain/permissions.go` | Removed "per Story 4.7 brief §2", Phase 1 carry |
| `packages/identity-domain/seed.go` | Removed "Phase 2 will route … Story 5.3", Phase 2 cross-package lookup comment |
| `packages/identity-domain/create_test.go` | Removed Story 4.2, 4.6 references; cleaned walk-back comments |
| `packages/identity-domain/claim_test.go` | Removed Story 4.3 from header |
| `packages/identity-domain/state_machine_test.go` | Removed Story 4.1 from header |
| `packages/identity-domain/testhelpers_test.go` | Removed Story 4.7 from header |
| `packages/identity-hygiene/README.md` | Removed Story 5.3, Phase 2 narrative |
| `packages/identity-hygiene/lenses.go` | Removed "Phase 1 … Phase 2 via a Lens `parameters` aspect" |
| `packages/identity-hygiene/merge_test.go` | Removed Story 4.6, 4.7 from header |
| `packages/identity-hygiene/testhelpers_test.go` | Removed Story 4.6 from header |
| `packages/rbac-domain/package.go` | Removed "before Story 4.7", "Link conventions (Story 4.7)" |
| `packages/rbac-domain/ddls.go` | Removed "Link key shapes (Story 4.7)", "Phase 2" uniqueness comment |
| `packages/rbac-domain/permissions.go` | No changes needed (already clean) |
| `packages/rbac-domain/package_test.go` | Removed "Story 4.7 rename" from comment |
| `packages/rbac-domain/testhelpers_test.go` | Removed Story 4.7 from header |
| `packages/rbac-domain/starlark_test.go` | Removed Story 3.6, 4.7 references from header |
| `packages/rbac-domain/integration_test.go` | Removed Story 3.6, 4.7 references from header |
| `packages/rbac-domain/README.md` | Removed "Phase 1: substrate-direct", "Phase 2 concern" |
| `packages/identity-domain/README.md` | Removed Story 4.7 from header, "Story 4.6-trimmed" from architectural note |
| `scripts/verify-package-rbac.go` | Removed Story 5.1 (×2) |
| `scripts/verify-package-identity.go` | Removed Story 5.1 (×2) |
| `scripts/verify-package-identity-hygiene.go` | Removed Story 5.1 (×2) |

---

## Test Harness Fix (N-003)

| File:Line | Description |
|-----------|-------------|
| `internal/pkgmgr/installer_test.go:69-73` | Added `inst.RoleIDs = map[string]string{"operator": "Hj4kPmRtw9nbCxz5vQ2y"}` to `newInstallerHarness` so the F-010 NanoID validation guard passes in unit tests without a real bootstrap; the mock value is a valid Contract #1 NanoID |

---

## Verification Note

`go build ./...`, `go vet`, and `go test ./internal/pkgmgr/... ./packages/... -p 1 -count=1` all pass.

`make verify-package-rbac`, `verify-package-identity`, `verify-package-identity-hygiene` (F-008/F-009 live-stack validation) require a running Docker stack and could not be run. The shared library and per-script envelope assertions are in place and will exercise on next `make up` run.
