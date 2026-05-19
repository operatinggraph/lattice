# Phase 1 Progress

**Updated:** 2026-05-19, after Story 4.5 (commit `b314677`) + 2026-05-19 course correction. **Epic 4 CLOSED (5/5)** but with architectural-realignment carry — see [PHASE-1-COURSE-CORRECTION.md](./PHASE-1-COURSE-CORRECTION.md).

This file tracks **what's shipped, what's next, what's still open**. Operating rules and workflow live in [`WINSTON-RESUME.md`](./WINSTON-RESUME.md). Token-by-token accounting lives in [`token-usage-tracker.md`](./token-usage-tracker.md).

## Current State

**Stories shipped: 29** (recomputed from per-row tracker; original plan was 31 stories, splits added 4 — 2.1+2.1b, 3.1→3.1a+3.1b-i+3.1b-ii, 3.2→3.2a+3.2b — and Story 2.3 was added between 2.2 and 3.1).

**Latest commit on main:** `b314677` (Story 4.5 — Staff-Approved Identity Merge / FR4).

**Epic 3 closed; Epic 4 CLOSED** (5/5 stories complete) — with realignment carry tracked in [PHASE-1-COURSE-CORRECTION.md](./PHASE-1-COURSE-CORRECTION.md) (the 2026-05-19 audit found Epic 4 drifted from the "operations write, lenses read" architecture; corrective stories 4.6 + 4.7 are queued before Epic 5).

**Token totals so far:** **~4,151K actual / 3,517K original-plan-budget (118%) for 28 / 32+ stories (88%).** Re-computed from per-row tracker actuals (28 rows × Actual column). Five new stories added 2026-05-19 from course correction (6.0, 4.6, 4.7, 2.4a, 2.4b — see Upcoming Sequence below) are not yet reflected in budget total. Quality bar maintained across all shipped gates.

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
| 6.0 | Component Reference Pages | pending | docs/components/{_index,processor,refractor,substrate}.md (636 lines total); closes lattice-architecture.md:23 gap; 97K OVERRUN (3× budget; docs-only) |

## Upcoming Sequence

**Epic 4 — Identity & Member Lifecycle: CLOSED (5/5)** — realignment pending in 4.6 + 4.7.

**Course-correction stories (2026-05-19; ahead of Epic 5):**
- ✅ **6.0** Component reference pages — `docs/components/{processor,refractor,substrate}.md` + `_index.md` — **SHIPPED** (97K Sonnet; 3× docs-budget OVERRUN)
- **4.6** Capability Package format + installer + identity-hygiene package (Opus, ~180K) — brief ready; next
- **4.7** Kernel minimization + rbac-domain + identity-domain packages (Opus, ~150K) — brief ready
- **2.4a** Refractor token eviction — mechanical rename sweep (Sonnet, ~90K) — brief ready
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
- **4.5 carry — `MergeIdentity` grant unseeded**: `permittedCommands` includes `MergeIdentity` but no `PermMergeIdentity` vertex / no role grant was seeded by 4.1. Phase 1 worked around this with stub-mode integration tests for `MergeIdentity`. A small follow-up story (Story 5.x candidate) must seed `PermMergeIdentity → operator` + the grant link, and rebaseline verify-bootstrap from 154 OK to ~156 OK.
- **4.5 carry — `TombstoneIdentity` stub**: still returns `NotYetImplemented`. AC for 4.5 did not require it; Phase 1 closure may need it; Phase 2 candidate.
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
