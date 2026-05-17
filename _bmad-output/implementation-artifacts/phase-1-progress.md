# Phase 1 Progress

**Updated:** 2026-05-17, after Story 3.7 (commit ecb2e68 + 008b420 lint cleanup). **Epic 3 closed.**

This file tracks **what's shipped, what's next, what's still open**. Operating rules and workflow live in [`WINSTON-RESUME.md`](./WINSTON-RESUME.md). Token-by-token accounting lives in [`token-usage-tracker.md`](./token-usage-tracker.md).

## Current State

**Stories shipped: 22 / 32+** (the `+` denotes stories added outside the original 31-story plan: Story 2.3 hardening; Story 3.1 split into 3.1a + 3.1b-i + 3.1b-ii; Story 3.2 split into 3.2a + 3.2b).

**Latest commit on main:** `008b420` (lint cleanup after Story 3.7 ecb2e68).

**Epic 3 closed.** All Phase 1 Gate 3 attack vectors DEFENDED. Next work is Epic 4 (Identity & Member Lifecycle).

**Token totals so far:** ~3,093K / 3,517K (88%) for 22/32+ stories (69%). Token efficiency tracks ~19 points behind story-progress; quality bar maintained across all gates.

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

## Upcoming Sequence

**Epic 4 — Identity & Member Lifecycle (next epic):**
- **4.1** Identity Domain DDL & State Machine (Opus, ~120K)
- **4.2** Staff Creates Unclaimed Identity FR1 (Sonnet, ~90K)
- **4.3** Two-Phase Identity Claim FR2/FR5 (Sonnet, ~100K)
- **4.4** Duplicate Identity Detection FR3 (Sonnet, ~110K)
- **4.5** Staff-Approved Identity Merge FR4 (Opus, ~135K)

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
