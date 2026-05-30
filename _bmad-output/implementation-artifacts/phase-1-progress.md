# Phase 1 Progress

**Updated:** 2026-05-20, after Story 4.6 (Capability Package format + identity-hygiene). **Epic 4 CLOSED (5/5)** + 2 of 5 course-correction stories shipped (6.0, 4.6); 3 remain (4.7, 2.4a, 2.4b) before Epic 5 — see [PHASE-1-COURSE-CORRECTION.md](./PHASE-1-COURSE-CORRECTION.md).

This file tracks **what's shipped, what's next, what's still open**. Operating rules and workflow live in [`WINSTON-RESUME.md`](./WINSTON-RESUME.md). Token-by-token accounting lives in [`token-usage-tracker.md`](./token-usage-tracker.md).

## Current State

**Stories shipped: 34** (recomputed from per-row tracker; original plan was 31 stories, splits added 4 — 2.1+2.1b, 3.1→3.1a+3.1b-i+3.1b-ii, 3.2→3.2a+3.2b — Story 2.3 was added between 2.2 and 3.1, and 5 course-correction stories added 2026-05-19; of those, 6.0 + 4.6 + 4.7 are now shipped).

**Latest commit on main:** `dfc9762` (Story 4.6 — Capability Package format + identity-hygiene); Story 4.7 commit pending push.

**Epic 3 closed; Epic 4 CLOSED** (5/5 original stories) + Stories 4.6 + 4.7 (course corrections) shipped: introduced the Capability Package mechanism, the identity-hygiene package, then minimized the bootstrap kernel and migrated rbac + identity domains into installable packages. After 4.7, the kernel is ~33 entries (vs 154 pre-correction); domain DDLs ship via `lattice-pkg install`. The Processor's write-path read surface is unchanged from its contract-defined set (Core KV + DDL cache + Capability KV + idempotency tracker). The 2026-05-19 architectural lesson is fully realized: graph topology flows Lens → Client → Command parameter → Processor-validates-against-Core-KV.

**Token totals so far:** **~4,972K actual / 3,847K plan-budget (129%) for 31 / 32+ stories (97%).** Re-computed from per-row tracker actuals. Quality bar maintained across all gates.

## Phase 1.5 — Hardening Block (status)

Plan: `_bmad-output/planning-artifacts/sprint-change-proposal-2026-05-28.md`. Process: Winston runs CS→DS→CR via no-commit sub-agents; Winston adjudicates/commits/watches CI. 7 stories.

| Story | Title | Status | Commit |
|---|---|---|---|
| 1.5.1 | Substrate write-path contracts (ctx + per-key revisions) | ✅ SHIPPED (CI green) | `09edf5c` |
| 1.5.3 | UpdateMetaVertex expansion | ✅ SHIPPED (CI green) | `d58311c` |
| 1.5.2 | DDL tombstone coherence (M6) | ✅ SHIPPED (CI green) | `pending` |
| 1.5.4 | Capability auth freshness coherence (B4) | ✅ SHIPPED (CI green) | `pending` |
| 1.5.5 | Route cap-pkg installs through Processor (M5) | ✅ SHIPPED (CI green) | `pending` |
| 1.5.7 | Contract conformance suite + freeze | queued (Wave C; needs 1.5.1+1.5.3) — see directive below | — |
| 1.5.6 | Re-enable Hello Lattice M4–M6 + flip Gate 5 | queued (Wave D) | — |

**1.5.5 note (largest story; Andrew's architectural decisions):** Capability-package install/uninstall now route through the Processor as two primordial kernel ops — `InstallPackage` / `UninstallPackage` (Andrew's calls: **bundle into a single op**, **thin-script / fat-manifest** — client pre-builds the mutation set, the kernel script validates+emits in ONE atomic commit). Closes M5/B2 (DDL cache coherent in-commit, no restart), F-001 (identity-domain PreInstall roles folded into the atomic batch + declaredKeys; `seed.go` deleted), F-002 (idempotency-tracker/CreateOnly conflict), F-011 (uninstall OCC — partially: per-key OCC deferred via CAR since `KVGet.Revision` ≠ atomic-batch per-subject sequence). Defines **Contract #8** (package-install). substrate-direct install pattern grep-clean. CR: 1 P1 + 3 P2. **The P1 (kernel-protection) closed the 1.5.2 residual properly:** the script-level protected check was dead code for Install/Uninstall (installer sets no ContextHint.Reads), so UninstallPackage could tombstone protected kernel keys. Andrew's decision: a **Processor-level, commit-time, read-and-check guard** (`CommitterImpl.rejectProtectedMutations` in step8 — rejects any update/tombstone whose root doc has `data.protected==true`; new `ErrCodeProtectedKey`), path-independent so it covers all ops at once. Defense-in-depth meta-root script guard kept. Open low CAR: UninstallPackage per-key OCC follow-up.

**1.5.7 DIRECTIVE (Andrew, 2026-05-30, binding):** The Processor write path must NOT be a read channel. `OperationReply` must NOT surface an arbitrary `map[string]any` from a Starlark script (`OperationReply.Detail`, from the script's `response` key). The "MUST NOT be logged — may carry sensitive tokens" (NFR-S6/S7) comments on `Detail` in `internal/processor/envelope.go` are the red flag — a field needing a do-not-log warning carries data it shouldn't. Epic-4/4.6 claimed to walk this back but the field + comments remain ("reviewers maintain compliance, processor does not enforce" = not enforced). **1.5.7 must REMOVE the arbitrary-data escape hatch + ENFORCE in code, then freeze the constrained reply shape.** Open design tension: `CreateUnclaimedIdentity` delivers a genuine one-time plaintext `claimKey` (no Core KV counterpart) via the reply — needs a typed single-purpose one-time-secret channel, not a general map. Resolve when authoring the 1.5.7 brief.

**1.5.4 note (architectural decision):** Per Andrew's decision this session, the Processor's per-operation capability projection-**freshness gate was REMOVED entirely** (it false-denied unchanged-but-valid actors — the Hello-Lattice M4 failure). Freshness is a property of the *projector*, not each doc; per-doc reprojection of quiet actors would be O(actors) write-amplification measuring the wrong thing. Grounded in brainstorming #111(a)/#91/architecture L90. The bounded staleness window is now **accepted**, backstopped operationally (Refractor Capability-Lens health — surveyed, currently no dedicated signal/alert; documented as a known gap) and by future Gateway JWT/token revocation. `projectedAt` is now **deterministic provenance** from the anchor vertex's `lastModifiedAt` (F-009 churn closed; fail-loud, no wall-clock fallback). Winston updated NFR-S7 + the Gate-3 Vector-#2 AC (epics.md); Gate-3 now reports 3 DEFENDED + 1 ACCEPTED-WINDOW. CR: 1 P1 + 1 P2, both doc/comment-only (stale `docs/observability/health-kv-schema.md` cap-staleness refs), fixed by Winston. **Wave A complete (1.5.1–1.5.4).**

**1.5.2 note:** CR found 1 P1 (primordial-lens cascade mismatch) + 1 P2. Winston adjudicated (CAR resolution in `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md`): (a) ratified tombstoning `.compensation` — `ReadCompensation` already maps `isDeleted:true`→`ErrCompensationAspectMissing`; (b) FIXED F-1 by making the lens cascade the UNION of DDL-created + primordial lens aspects (no live orphan for either lens kind; absent-aspect tombstones are harmless). **RESIDUAL → Story 1.5.5:** no guard prevents `TombstoneMetaVertex`/`UpdateMetaVertex` from targeting primordial kernel entities (kernel root DDL, CapabilityLens) — a pre-existing catastrophic foot-gun; needs a protected-key mechanism, deferred to the install-routing/kernel-protection story.

**1.5.3 note:** CR clean (0 P0/P1, 2 P2 + 1 nit), all adjudicated and fixed inline by Winston: permittedCommands per-element string check (parity with Create); fail-the-forward-op when a changed field's prior value is unavailable (a null prior would bake an un-submittable rollback) instead of capturing nil; doc wording. Fixed a latent description-blanking bug as part of "mutate only fields present".

**1.5.1 note:** CR found one Major (M-1) — bootstrap seeding lost its timeout bound because the locked design's premise (`readyCtx` governs seeding) was wrong (`main.go` seeds with `context.Background()` before `readyCtx` exists). Winston fix: bound `SeedPrimordial` with a `BOOTSTRAP_READY_TIMEOUT_SEC`-derived context in `cmd/bootstrap/main.go`. All other CR observations confirmed the impl solid. Empirical revision==stream-sequence assertion passed on live NATS.

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
| 4.7 | Kernel Minimization + rbac-domain + identity-domain | 7f6d583 | Bootstrap shrunk from 154 OK → ~33 entries (meta-meta-DDL + 2 lenses + operator role + 3 meta-perms + 3 grants + 1 admin + 1 holdsRole link). `grantsPermission` link DDL renamed `grantedBy` (permission→role). packages/rbac-domain/ (rbac DDL handles 10 ops in ~310 LOC). packages/identity-domain/ (verbatim Story-4.6 trimmed identity DDL + PreInstall hook seeding consumer/frontOfHouse/backOfHouse roles via substrate-direct writes). `internal/pkgmgr` extended with `PreInstallFn` hook + `Installer.RoleIDs` grant resolution. New verify gates: verify-kernel (replaces verify-bootstrap), verify-package-{rbac,identity,identity-hygiene}. `internal/testutil/install_phase1_packages.go` helper. Architectural-purity checks all clean: no new ContextHint fields, no Adjacency reads, no scans. 241K Opus (+91K OVERRUN, ~1.6× budget). |
| 4.7-cleanup | Test relocation + legacy bootstrap accessor deletion | 5060fda | 5 identity/rbac DDL-behavior tests relocated from `internal/processor/` to `packages/{identity-domain,rbac-domain}/_test` (external test packages calling `testutil.InstallPhase1Packages` after `bootstrap.SeedPrimordial`). Legacy bootstrap surface deleted: `identity_ddl.go`, `role_mgmt_ddl.go`, `roles.go`, `nanoid.go` trimmed (-1,887 LOC bootstrap dead code; -4,291 LOC total including old test locations). 3 narrow processor exports (`SeedFromRequestID`, `DeterministicNanoID`, `NewStubEventPublisher`) + 2 new testutil fixtures (`pipeline.go`, `embedded_nats.go`). Round 1 reverted for in-place-rewire-and-delete shortcuts; round 2 with hardened brief landed cleanly. |
| 2.4a | Refractor Token Eviction (mechanical) | a34a388 | Subjects: `materializer.{rules,health}.<lensId>` deleted entirely (Health subject was already deprecated by Story 3.2b's per-instance Health KV; Rules subject went away with Core-KV-watched lens defs); `materializer.{audit,metrics,dlq}.<lensId>` → `lattice.refractor.{audit,metrics,dlq}.<lensId>`. Durables/streams: `MATERIALIZER_DLQ_*` → `REFRACTOR_DLQ_*`; adjacency consumer `materializer-adjacency` → `refractor-adjacency`; rule consumer prefix `materializer-<ruleID>` → `refractor-<ruleID>`; control-plane queue group `materializer-control` → `refractor-control`. KV bucket `materializer-health` default + field deleted entirely from config. `team` field eviction (Deviation 4 closure): validation removed; DLQ subject signature trimmed `(team, lensID) → (lensID)`; `TestParse_MissingTeam` rewritten to `TestParse_NoTeam_Accepted`. Comments rewritten across ~8 files. **Intentionally NOT touched**: `materializer.control` subject (2.4b territory — durable JetStream consumer for lens defs + NATS Services control plane). `lens/loader.go` + `loader_test.go` (-841 LOC) deleted entirely (lens defs come from Core KV via `corekv_source.go` now; loader was dead). 133K Sonnet (+43K, 1.5× budget — within mechanical-rename reasonableness). |
| 2.4b | Refractor Lattice-Native Source Plane | pending | New `substrate.SubscribeKVChanges` helper: durable JetStream consumer on the `KV_core-kv` backing stream filtered to `$KV.core-kv.vtx.meta.>`. Replaces the ephemeral `kv.Watch(ctx, "vtx.meta.>", IncludeHistory())` in `lens/corekv_source.go` — cross-restart sequence position preserved (no more replay-all-history-on-resume waste). Durable retained on ctx.Done (would-be-deletion would wipe the ack floor and defeat the durable-resume promise; sub-agent caught the brief's self-contradiction here). Control plane: `nc.QueueSubscribe(subjects.Control(), "refractor-control", ...)` → `micro.AddService` with endpoints at `lattice.ctrl.refractor.<lensId>.<op>` for the 6 real ops (health, validate, rebuild, pause, resume, delete) — sub-agent verified op list against the actual handler rather than the brief's drifted example list. `subjects.Control()` deleted. **Behavioral shift worth flagging to clients**: unknown ops no longer return `{"error":"unknown operation: ..."}` — they time out with `nats: no responders available` because NATS Services routes by subject. Documented in the brief's migration mapping. 146K Opus (+46K, 1.5× budget). Architectural-purity grep clean; no Processor touched; final `materializer` operational reference (the control-plane subject literal) is now evicted. |

## Upcoming Sequence

**Epic 4 — Identity & Member Lifecycle: CLOSED (5/5)** — realignment pending in 4.6 + 4.7.

**Course-correction stories (2026-05-19; ahead of Epic 5):**
- ✅ **6.0** Component reference pages — **SHIPPED** (97K Sonnet; 3× docs-budget OVERRUN)
- ✅ **4.6** Capability Package format + installer + identity-hygiene package — **SHIPPED** (483K Opus across 3 rounds; 2.7× OVERRUN; architectural-boundary learnings codified)
- ✅ **4.7** Kernel minimization + rbac-domain + identity-domain packages — **SHIPPED** (241K Opus; 1.6× OVERRUN; Processor purity preserved)
- ✅ **4.7-cleanup** Test relocation + legacy bootstrap accessor deletion — **SHIPPED** (365K Opus across 2 rounds; 3 of 4 carries closed; verify-package-* script-hardening remains)
- ✅ **2.4a** Refractor token eviction — mechanical rename sweep — **SHIPPED** (133K Sonnet; 1.5× budget)
- ✅ **2.4b** Refractor Lattice-native source plane — **SHIPPED** (146K Opus; 1.5× budget; course correction complete)

**Course correction sequence complete.** Epic 5 unblocked.

**Epic 5 — DDL Self-Description & AI Agent Cold-Start:**
- ✅ **5.1** DDL Self-Description Aspects FR19 — **IN REVIEW** (Sonnet; 5 primordial aspect-type meta-vertices; MetaRootDDLScript enforces MissingSelfDescription; pkgmgr fail-fast; all 3 Phase-1 packages carry all 4 new fields; 2 e2e integration tests)
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
- ~~**4.5 carry — `TombstoneIdentity` stub**~~: **CLOSED implicitly by Story 4.6** — `TombstoneIdentity` was removed from the identity DDL's `permittedCommands` when 4.6 trimmed the surface from 8 ops to 3 (`[CreateUnclaimedIdentity, UpdateIdentityState, ClaimIdentity]`). The `NotYetImplemented` script branch is gone. `grep -rn "TombstoneIdentity\|NotYetImplemented"` over `internal/` + `packages/` returns zero hits. If Phase 2 wants an identity tombstone op, it'll come back as a package-supplied operation (most likely in `identity-hygiene` alongside `MergeIdentity`).
- **4.6 carry — identity-hygiene Surface-B integration tests**: 8 end-to-end tests (lens projection variants, MergeIdentity happy-path, fabricated/non-touching/tombstoned edge rejections, post-merge redirect) require a full Refractor + Processor + installed-package harness that doesn't exist yet. Unit/structural tests landed; e2e deferred until a package-aware test fixture exists. Recommended pre-Story-4.7 (4.7 will install rbac-domain + identity-domain packages and benefit from the same fixture).
- **4.6 carry — `lattice candidates list` CLI verb**: out of scope for 4.6 (CLI tooling is Story 6.1). Until then, operators read the `duplicate-candidates` bucket directly and construct `MergeIdentity{primary, secondary, edges: secondaryInboundEdges + secondaryOutboundEdges}` by hand.
- ~~**4.7 carry — legacy bootstrap accessor cleanup**~~: **CLOSED by 4.7 cleanup** (commit pending). `internal/bootstrap/{identity_ddl.go,role_mgmt_ddl.go,roles.go}` deleted entirely; `nanoid.go` trimmed of all identity/rbac-specific surface; `DDLGrantsPermissionID`, `IdentityPermissionKeys`, `IdentityGrantLinkKeys`, `RoleMgmtPermissionKeys`, `RoleMgmtGrantLinkKeys`, the per-op identity/rbac permission NanoID constants, and the `CanonicalRoles()` / `PlatformInternalPermission()` helpers are all gone. Operator role + meta-permission NanoIDs retained (still primordial).
- ~~**4.7 carry — processor test cutover deferred**~~: **CLOSED by 4.7 cleanup** (commit pending). All 5 identity/rbac DDL-behavior test files moved from `internal/processor/` to `packages/{identity-domain,rbac-domain}/_test`: `identity_create_test.go` → `packages/identity-domain/create_test.go` (369 LOC), `identity_claim_test.go` → `packages/identity-domain/claim_test.go` (540 LOC), `identity_state_machine_test.go` → `packages/identity-domain/state_machine_test.go` (338 LOC), `role_mgmt_integration_test.go` → `packages/rbac-domain/integration_test.go` (323 LOC), `role_mgmt_starlark_test.go` → `packages/rbac-domain/starlark_test.go` (276 LOC) + `reports_to_test.go` (39 LOC, orphan case per brief §2 option b). Tests run as `package identitydomain_test` / `package rbacdomain_test` (external) and call `testutil.InstallPhase1Packages` after `bootstrap.SeedPrimordial`. Three narrow processor exports (`SeedFromRequestID`, `DeterministicNanoID`, `NewStubEventPublisher`) enabled the move without leaking processor internals.
- ~~**4.7 carry — `IdentityDDL()` + `RoleMgmtDDLs()` script-body duplication**~~: **MOOT after 4.7 cleanup** — the legacy script bodies no longer exist in `internal/bootstrap/`; the package files carry the only copies.
- ~~**4.7 carry — `verify-package-*` gates are weak**~~: **CLOSED by verify-package-hardening** (commit pending). Three new scripts (`scripts/verify-package-{rbac,identity,identity-hygiene}.go`, 1,380 LOC total) assert per-package Core KV state: DDL meta-vertex + aspects (canonicalName/permittedCommands/description/script), per-op permission vertices with `operationType`+`scope` aspects, `grantedBy` link keys to expected roles, lens meta-vertices with `.spec` content checks (identity-hygiene), package manifest aspect. Each script targets ~20-40 OK lines. Makefile wired to invoke `go run scripts/verify-package-<name>.go` after install (replaces the `lattice-pkg list` stub).
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
