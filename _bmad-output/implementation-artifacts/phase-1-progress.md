# Phase 1 Progress

**Updated:** 2026-05-20, after Story 4.6 (Capability Package format + identity-hygiene). **Epic 4 CLOSED (5/5)** + 2 of 5 course-correction stories shipped (6.0, 4.6); 3 remain (4.7, 2.4a, 2.4b) before Epic 5 — see [PHASE-1-COURSE-CORRECTION.md](./PHASE-1-COURSE-CORRECTION.md).

This file tracks **what's shipped, what's next, what's still open**. Operating rules and workflow live in [`WINSTON-RESUME.md`](./WINSTON-RESUME.md). Token-by-token accounting lives in [`token-usage-tracker.md`](./token-usage-tracker.md).

## Current State

**Stories shipped: 31** (recomputed from per-row tracker; original plan was 31 stories, splits added 4 — 2.1+2.1b, 3.1→3.1a+3.1b-i+3.1b-ii, 3.2→3.2a+3.2b — Story 2.3 was added between 2.2 and 3.1, and 5 course-correction stories added 2026-05-19; of those, 6.0 + 4.6 + 4.7 are now shipped).

**Latest commit on main:** `dfc9762` (Story 4.6 — Capability Package format + identity-hygiene); Story 4.7 commit pending push.

**Epic 3 closed; Epic 4 CLOSED** (5/5 original stories) + Stories 4.6 + 4.7 (course corrections) shipped: introduced the Capability Package mechanism, the identity-hygiene package, then minimized the bootstrap kernel and migrated rbac + identity domains into installable packages. After 4.7, the kernel is ~33 entries (vs 154 pre-correction); domain DDLs ship via `lattice-pkg install`. The Processor's write-path read surface is unchanged from its contract-defined set (Core KV + DDL cache + Capability KV + idempotency tracker). The 2026-05-19 architectural lesson is fully realized: graph topology flows Lens → Client → Command parameter → Processor-validates-against-Core-KV.

**Token totals so far:** **~4,972K actual / 3,847K plan-budget (129%) for 31 / 32+ stories (97%).** Re-computed from per-row tracker actuals. Quality bar maintained across all gates.

## Shipped Story Index

Quick reference; full details in token-usage-tracker.md.

| Story | Title | Commit | Notes |
|---|---|---|---|
| 1.1 | NATS atomic batch spike | early | Gate 1 contribution |
| 1.2 | Starlark spike | early | Sandbox + perf verified |
| 1.3 | Dev harness + bootstrap | early | docker-compose + Makefile |
| 1.4 | `internal/substrate` | early | NATS/KV/NanoID primitives |
| 1.5 | Processor steps 1-3 | early | StubAuthorizer |
| 1.6 | Processor steps 4-5 | early | Starlark sandbox + JIT hydration |
| 1.7 | Processor steps 6-8 | early | DDL cache + ConflictError |
| 1.8 | Processor steps 9-10 | early | Events + NFR-R1 10/10 |
| 1.9 | FR57 write-scope | early | VERIFIED |
| 1.10 | Phase 1 Gate 2 bypass suite | early | 4/4 BLOCKED |
| 2.1 (+2.1b) | Materializer → Refractor morph | early | AC #10 e2e p99=10.3ms |
| 2.2 | Refractor gap analysis | early | 15 deviations + Appendix A |
| 2.3 | Pipeline key-shape adaptation | early | Deviation 13 fix |
| 3.1a | Engine boundary + selection | early | RuleEngine interface |
| 3.1b-i | Cypher visitor + AST | early | Bootstrap CapabilityLens parses |
| 3.1b-ii | Cypher executor + bootstrap e2e | early | p99=11.7ms |
| 3.2a | Capability Lens live activation | early | Single-id p99=9.6ms |
| 3.2b | Capability Lens AC closure | 99017ca | Multi-id p99=5.7ms |
| 3.3 | Processor step 3 Capability KV auth | ee293bb | Real authorizer + freshness + alerts |
| 3.4 | Structured denial response (FR22) | 09e218e | DenialResponseBuilder + role coverage |
| 3.5 | Three-plane auth trace (FR23) | 19bd508 | AuthTraceEmitter + KVPutWithTTL |
| 3.6 | Role-scoped access domain (FR24/25) | 22a132f | 5 DDLs + 12 operator perms + §6.13 |
| 3.7 | Capability Lens adversarial suite (Gate 3) | ecb2e68 | 4/4 DEFENDED; Epic 3 closed |
| 4.1 | Identity Domain DDL & State Machine | 3cb5a06 | 1 DDL + 5 perms + 10 grants + state machine; verify-bootstrap 154 OK |
| 4.2 | Staff Creates Unclaimed Identity (FR1) | 7462fc7 | crypto.sha256 + sha256NanoID + duplicate index vertices; 65K Sonnet UNDERRUN |
| 4.3 | Two-Phase Identity Claim (FR2, FR5) | 677747c | crypto.constant_time_equal + RecordClaimAttempt + generic ClaimKeyInvalid + credentialindex; two-session OVERRUN |
| 4.4 | Duplicate Identity Detection (FR3) | e89c4f7 | strings.levenshtein + ScanPrefixes hydrator + canonical duplicateOf link + capabilityenv pendingReview; two-session OVERRUN |
| 4.5 | Staff-Approved Identity Merge (FR4) | b314677 | ApproveIdentityMerge (review) + MergeIdentity (link rekey + state→merged + mergedInto); lnk. global-scan hydrator; 12 integration tests across capability+stub auth; 215K single-session OVERRUN; Epic 4 CLOSED |
| 6.0 | Component Reference Pages | 12b7574 | docs/components/{_index,processor,refractor,substrate}.md (636 lines total); closes lattice-architecture.md:23 gap; 97K OVERRUN (3× budget; docs-only) |
| 4.6 | Capability Package + Identity-Hygiene | dfc9762 | docs/components/_packages.md + cmd/lattice-pkg/ + internal/pkgmgr/ (5 installer tests) + packages/identity-hygiene/ (single lens enriched with edge enumeration; MergeIdentity takes `edges` as command parameter, validates against Core KV). Walked back Epic 4 accommodations: ScanPrefixes, keys_with_prefix, strings.* module, pendingReview, flagged-for-review state all deleted. Identity DDL trimmed 8→3 ops, 1131→448 LOC. Levenshtein moved to cypher executor UDFs. 3 sub-agent rounds; 483K OVERRUN (2.7× budget). |
| 4.7 | Kernel Minimization + rbac-domain + identity-domain | pending | Bootstrap shrunk from 154 OK → ~33 entries (meta-meta-DDL + 2 lenses + operator role + 3 meta-perms + 3 grants + 1 admin + 1 holdsRole link). `grantsPermission` link DDL renamed `grantedBy` (permission→role). packages/rbac-domain/ (rbac DDL handles 10 ops in ~310 LOC). packages/identity-domain/ (verbatim Story-4.6 trimmed identity DDL + PreInstall hook seeding consumer/frontOfHouse/backOfHouse roles via substrate-direct writes). `internal/pkgmgr` extended with `PreInstallFn` hook + `Installer.RoleIDs` grant resolution. New verify gates: verify-kernel (replaces verify-bootstrap), verify-package-{rbac,identity,identity-hygiene}. `internal/testutil/install_phase1_packages.go` helper. Architectural-purity checks all clean: no new ContextHint fields, no Adjacency reads, no scans. 241K Opus (+91K OVERRUN, ~1.6× budget). |

## Upcoming Sequence

**Epic 4 — Identity & Member Lifecycle: CLOSED (5/5)** — realignment pending in 4.6 + 4.7.

**Course-correction stories (2026-05-19; ahead of Epic 5):**
- ✅ **6.0** Component reference pages — **SHIPPED** (97K Sonnet; 3× docs-budget OVERRUN)
- ✅ **4.6** Capability Package format + installer + identity-hygiene package — **SHIPPED** (483K Opus across 3 rounds; 2.7× OVERRUN; architectural-boundary learnings codified)
- ✅ **4.7** Kernel minimization + rbac-domain + identity-domain packages — **SHIPPED** (241K Opus; 1.6× OVERRUN; Processor purity preserved)
- **2.4a** Refractor token eviction — mechanical rename sweep (Sonnet, ~90K) — brief ready; next
- **2.4b** Refractor Lattice-native source plane — durable JetStream consumer for lens defs + NATS Services control plane (Opus, ~100K) — brief ready

**Epic 5 — DDL Self-Description & AI Agent Cold-Start:**
- **5.1** DDL Self-Description Aspects FR19 (Opus, ~115K)
- **5.2** Cold-Start AI Agent Traversal (Sonnet, ~115K)
- **5.3** Compensating Op + DDL Rollback (Opus, ~130K) — Gate 4

**Epic 6 — CLI, Health, Deployment, Reference Implementation:**
- **6.1** Lattice CLI tool FR45 (Sonnet, ~120K) — picks up `lattice auth-trace` from 3.5 carry
- **6.2** Health KV schema + completeness (Sonnet, ~90K)
- **6.3** Deployment isolation FR48 (Sonnet, ~70K)
- **6.4** "Hello Lattice" reference impl (Sonnet, ~130K) — Gate 5; external tester required

## Open Items & Residual Carries

### Phase 2+ deferrals (from completed stories)

- **3.4**: `actorRoles` returns full vertex keys; `rolesCarryingPermission` returns bare names — asymmetry flagged for Phase 2 normalization.
- **3.5**: Cypher rule body hash deferred to Phase 2 (Phase 1 uses `sha256(lensKey+"@"+projectedAt)` fingerprint).
- **3.5**: `lattice auth-trace` CLI deferred to Story 6.1; `LookupAuthTrace` helper available for the wrap.
- **3.6**: AC text `assignedRole` resolved to `holdsRole` per cypher consistency; documented in data-contracts.md §6.13. Phase 2 may re-canonicalize.
- **3.7 Vector #1**: Phase 1 defense is Refractor reprojection only. NATS-account-level write restriction on Capability KV (Contract #6 §6.1) is the Phase 2 hardening — adds substrate-level enforcement on top of the current overwrite-by-reprojection guarantee.
- **4.5 carry — `MergeIdentity` grant unseeded**: ~~`permittedCommands` includes `MergeIdentity` but no `PermMergeIdentity` vertex / no role grant was seeded by 4.1.~~ **CLOSED by Story 4.6:** the identity-hygiene Capability Package declares both the `MergeIdentity` permission vertex and the `MergeIdentity → operator` grant link, installed via `lattice-pkg install packages/identity-hygiene`.
- **4.5 carry — `TombstoneIdentity` stub**: still returns `NotYetImplemented`. AC for 4.5 did not require it; Phase 1 closure may need it; Phase 2 candidate.
- **4.6 carry — identity-hygiene Surface-B integration tests**: 8 end-to-end tests (lens projection variants, MergeIdentity happy-path, fabricated/non-touching/tombstoned edge rejections, post-merge redirect) require a full Refractor + Processor + installed-package harness that doesn't exist yet. Unit/structural tests landed; e2e deferred until a package-aware test fixture exists. Recommended pre-Story-4.7 (4.7 will install rbac-domain + identity-domain packages and benefit from the same fixture).
- **4.6 carry — `lattice candidates list` CLI verb**: out of scope for 4.6 (CLI tooling is Story 6.1). Until then, operators read the `duplicate-candidates` bucket directly and construct `MergeIdentity{primary, secondary, edges: secondaryInboundEdges + secondaryOutboundEdges}` by hand.
- ~~**4.7 carry — legacy bootstrap accessor cleanup**~~: **CLOSED by 4.7 cleanup** (commit pending). `internal/bootstrap/{identity_ddl.go,role_mgmt_ddl.go,roles.go}` deleted entirely; `nanoid.go` trimmed of all identity/rbac-specific surface; `DDLGrantsPermissionID`, `IdentityPermissionKeys`, `IdentityGrantLinkKeys`, `RoleMgmtPermissionKeys`, `RoleMgmtGrantLinkKeys`, the per-op identity/rbac permission NanoID constants, and the `CanonicalRoles()` / `PlatformInternalPermission()` helpers are all gone. Operator role + meta-permission NanoIDs retained (still primordial).
- ~~**4.7 carry — processor test cutover deferred**~~: **CLOSED by 4.7 cleanup** (commit pending). All 5 identity/rbac DDL-behavior test files moved from `internal/processor/` to `packages/{identity-domain,rbac-domain}/_test`: `identity_create_test.go` → `packages/identity-domain/create_test.go` (369 LOC), `identity_claim_test.go` → `packages/identity-domain/claim_test.go` (540 LOC), `identity_state_machine_test.go` → `packages/identity-domain/state_machine_test.go` (338 LOC), `role_mgmt_integration_test.go` → `packages/rbac-domain/integration_test.go` (323 LOC), `role_mgmt_starlark_test.go` → `packages/rbac-domain/starlark_test.go` (276 LOC) + `reports_to_test.go` (39 LOC, orphan case per brief §2 option b). Tests run as `package identitydomain_test` / `package rbacdomain_test` (external) and call `testutil.InstallPhase1Packages` after `bootstrap.SeedPrimordial`. Three narrow processor exports (`SeedFromRequestID`, `DeterministicNanoID`, `NewStubEventPublisher`) enabled the move without leaking processor internals.
- ~~**4.7 carry — `IdentityDDL()` + `RoleMgmtDDLs()` script-body duplication**~~: **MOOT after 4.7 cleanup** — the legacy script bodies no longer exist in `internal/bootstrap/`; the package files carry the only copies.
- **4.7 carry — `verify-package-*` gates are weak**: targets shell `lattice-pkg list` rather than asserting on per-package Core KV state (DDL aspects, permission vertices, grant link keys, lens specs). Sufficient to detect total-failure-to-install, not sufficient to detect partial-install corruption. Strengthen with per-package assertion scripts (mirror `scripts/verify-kernel.go` style) before Phase 1 closure. **Status: still open** — the 4.7 cleanup round 2 ran out of budget before reaching this deliverable. Mechanical script-writing; Sonnet ~30K should close it.
- **4.5 carry — `SplitIdentities` (un-merge)**: out of Phase 1 per AC. Phase 2+.
- **4.5 carry — Refractor reprojection NFR-P3 test for merge**: brief §5 listed `TestMergeIdentity_CapKVReprojection_NFR_P3`; sub-agent deferred citing duplication with existing `refractor_capability_multi_e2e_test.go` + 3.2b adjacency-bridge coverage. Accepted as soft trade; revisit if a real reprojection-on-merge regression slips later.
- **4.5 carry — capabilityenv `merged: true, mergedInto: <primary>` field**: not added. Existing reprojection on `state` aspect mutation suffices for AC. Add if a Phase 2 consumer needs it.
- **4.4 Winston note (post-Epic-4 cleanup)**: `data-contracts.md` has accreted internal-to-component / story-specific decisions (especially §6.13 from 3.6) that conceptually belong with the component code or domain docs, not next to cross-component interface contracts. Plan a documentation refactor pass after Epic 4 closes: split inter-component contracts from intra-component algorithm specs / DDL behavior notes. 4.4 explicitly skipped adding §6.14 per this concern.

### Residual carries from 3.2b (still open for 3.7+ / Phase 2)

1. Actor enumerator over-fans on dense graphs (undirected BFS, no relation-type whitelist). Relation-type-aware enumeration is a Phase 2 optimization.
2. Link-envelope tombstone re-projection is indirect (acks-and-drops; re-projection fires via adj-watch). Production benefit: link-envelope-triggered fan-out invocation. Deferred.
3. `projectedFromRevisions` partial coverage (anchor + lens-def only). Full source-vertex tracking is opportunistic.
4. Latency ring buffer per-pipeline-instance. Multi-instance aggregation is Phase 2 (multi-cell).
5. Hot-reload routing tested by inspection only.

### MORPH-DEVIATIONS open carries

- **Deviation 5** — substrate inner-package migration (~30 files). Deferred.
- **Deviation 11** — single-aspect lens spec assumption (operational choice; revisit Phase 2).
- **Deviation 11a** — Rule→Lens Go-type cleanup. Deferred.
- **Deviation 14** — `go test ./... -p 1` required; produces occasional inter-package flakes under combined-suite load. Phase 2 candidate: shared-fixture helper.

None block current work.

## CI Flake Patterns

- **JetStream redelivery + tracker dedup** is slow on GitHub Actions runners. NFR-R1 fault tests have 30s timeouts (`driveOne`/`driveOneAny`).
- **Embedded-NATS resource pressure** under `-p 1` full-suite mode produces inter-package flakes (Deviation 14). Re-run usually clears. Story 3.4, 3.5, 3.6 all saw this on first run; second run green.

## Update Cadence

Update this file at the end of each story:
1. Add row to "Shipped Story Index" with commit SHA + one-line summary.
2. Move the shipped row off "Upcoming Sequence".
3. Append any new residuals to "Open Items & Residual Carries".
4. Refresh "Current State" totals (story count, token totals, latest commit).
