---
stepsCompleted: [1, 2, 3, 4]
inputDocuments:
  - "_bmad-output/planning-artifacts/prd.md"
  - "_bmad-output/planning-artifacts/lattice-architecture.md"
workflowType: 'epics-and-stories'
project_name: 'Lattice'
user_name: 'Andrew'
date: '2026-04-11'
scope: 'Phase 1 (MVP / Platform Proof)'
---

# Lattice - Epic Breakdown

## Overview

This document provides the complete epic and story breakdown for Lattice, decomposing Phase 1 requirements from the PRD, and Architecture into implementable stories. Since all implementation will be executed by AI agents, each story includes a **recommended model tier** (Opus / Sonnet / Haiku) and an estimated **token budget** (input + output context).

**Scope:** Phase 1 — Platform Proof MVP (Streams 0–3 partial). Proves the core architectural claims under real conditions. Phase 2+ features (Gateway, Loftspace vertical, browser console, orchestration, privacy/Vault, full identity claim) are explicitly out of scope here.

---

## Documentation Layering Rule (2026-05-19)

When a story's AC needs to document something, choose the destination by **what kind of decision it is**:

| Decision kind | Lives in | Examples |
|---|---|---|
| Cross-component interface contract | `_bmad-output/planning-artifacts/data-contracts.md` | Envelope shapes, key shapes, MutationBatch return shape, Capability KV layout, Health KV convention |
| Per-component implementation choice | `docs/components/<name>.md` (per-component reference page) | DDL inventories, internal helper names, state-machine semantics, internal naming, choice-of-algorithm |
| Per-package capability definition | `packages/<package-name>/` directory | Package-specific DDLs, Lens defs, permissions, scripts (added by 2026-05-19 course correction; see PHASE-1-COURSE-CORRECTION.md) |
| Story-specific decision (rationale, deviations) | Handoff brief + commit message + MORPH-DEVIATIONS.md (where applicable) | Per-story choices, brief-imprecision corrections, scope splits |

**Story ACs MUST NOT direct edits to `data-contracts.md` for per-component or per-story details.** Instructions like "documented in `data-contracts.md` as an addendum to Contract #N" are a pattern this rule prohibits. Use one of the other three destinations.

This rule was added retroactively to address the 2026-05-19 audit finding (concern 2) that `data-contracts.md` accreted Story-specific dumps (notably the evicted §6.13). Existing AC text in Stories 1.x–4.x that violates this rule was left in place because those stories have shipped; the rule binds Stories 5.x and 6.x going forward.

---

## Requirements Inventory

### Functional Requirements

FR1: Staff can create an unclaimed identity record for a prospect or leaseholder without requiring the person to have an active account
FR2: A resident or member can self-register and bind their credentials to an existing unclaimed identity record using matching claim keys
FR3: The system detects potential duplicate identity records based on fuzzy matching of name, phone, and email; detection is presented for human review, never resolved automatically
FR4: Staff can review duplicate identity candidates and approve a merge; merges cannot occur without explicit staff confirmation
FR5: A leaseholder with a grandfathered account (no app registration) can claim their existing identity vertex and immediately access full account history — charges, lease terms, and communication history
FR6: An identity record exists in trackable states: unclaimed, claimed, flagged-for-review, merged
FR7: A member's identity and full interaction history persist after a lease or membership ends; the relationship is not erased by a transaction boundary
FR8: All state mutations are submitted through a single validated write path; no direct writes to the core data store are possible from any caller outside the platform
FR9: Business rules are expressed as deterministic scripts that execute against each submitted operation; scripts have no access to external I/O, network, secrets, or non-deterministic state
FR10: New entity types, business rules, and projection definitions can be authored and activated in a running system without redeployment
FR11: Every submitted operation produces an immutable, ordered ledger entry including author identity, timestamp, and full operation payload
FR12: Operations are idempotent; resubmitting the same operation produces the same outcome without creating duplicate or conflicting state
FR13: Multiple related state changes can be submitted as a single operation that commits entirely or fails entirely
FR14: An actor can confirm that a submitted operation has been durably committed before reading dependent projections
FR15: Business state can be projected into query-optimized external targets via configurable projection definitions authored as data operations
FR16: Projection definitions can be created, modified, and activated as platform operations without redeployment
FR17: Front-of-house staff can query pre-computed member context — identity, service history, open tickets, communication preferences — through a role-scoped projection surface
FR18: Back-of-house operators can query operational projections — occupancy, rent roll, payment status, maintenance SLA status — through a role-scoped projection surface
FR19: A Lattice-aware AI agent can traverse from any identity vertex to available commands, input schemas, and plain-language field descriptions without prior hardcoded knowledge of the deployment
FR20: Projection lag between a committed operation and its appearance in a query surface is bounded and observable
FR21: Permission relationships are derived from graph structure and used to authorize every operation in real time
FR22: A permission denial response specifies the exact permissions required, the actor's current role, and available escalation or routing paths
FR23: Auth failures are traceable across three planes: the graph permission path, the projection definition, and the cached permission check
FR24: Platform operators can define and assign role-scoped access for all actor types: consumer, front-of-house staff, back-of-house staff, operator, and internal system actors
FR25: Operators can audit which actors hold which permissions at any point in time
FR26: Multi-step business processes can be defined as workflows with conditional branching and human approval gates; workflows advance automatically when conditions are met
FR27: The platform can enforce convergence targets — desired operational states — and automatically assign remediation tasks when actual state diverges from target
FR28: Tasks can be assigned to a specific actor or to a role-based queue; when the primary assignee is unavailable, tasks fall back to the role queue
FR29: Unrouted tasks (no available assignee or queue) surface in operational health monitoring and are never silently dropped
FR30: Operators can view, modify, and revoke all active convergence targets from a management surface
FR31: An operator can describe a desired capability in natural language and receive a proposed entity type definition, business rule script, and projection definition for review
FR32: A proposed capability bundle is reviewed and approved via a task-based workflow before the capability is activated in the running system
FR33: An AI agent's pending intent is persisted in the graph between sessions so that the system retains context across interruptions
FR34: A Lattice-aware AI agent can submit validated intent through the standard write path with the same safety guarantees as human-submitted operations
FR35: Operators can view all AI-authored capability changes — including author, timestamp, and approver — and governance surfaces are accessible alongside operational health state
FR36: Capability authorship governance surfaces are accessible from the same surface as operational health state
FR37: Personal data fields can be individually encrypted; the encryption key for specific fields can be shredded to render those fields irrecoverable without affecting other fields on the same record
FR38: Non-personal fields — decision outcomes, denial reasons, business criteria applied — are retained after key shredding; the audit record remains intact and legally defensible
FR39: Erasure of an identity with active financial obligations (active lease, outstanding charges) requires explicit operator override
FR40: Payment records store transaction references, status codes, and amounts; raw payment credentials are never stored or processed by the platform
FR41: Data retention policy per data type (financial records, behavioral data, operational logs) is configurable by the operator
FR42: The complete, ordered, immutable operation ledger can be mirrored to an external long-term retention store for unlimited retention without platform changes
FR43: A developer can boot a complete local platform environment — including data substrate, projection target, and bootstrap data — from a single command
FR44: A developer can complete a minimal working vertical slice (one entity → one rule → one projection → one AI traversal query) in under 60 minutes from a fresh clone
FR45: A CLI tool allows developers and operators to submit operations, inspect graph state, and query projection surfaces without a browser client
FR46: Platform operational health — stream lag, unrouted tasks, projection errors, component status — is readable from a dedicated health data store separate from the business state store
FR47: A developer/operator console surfaces AI-suggested capability changes as human-review tasks alongside real-time operational health state
FR48: Platform deployments are isolated at the infrastructure level; each operator deployment maintains its own independent data and event streams
FR49: The platform can notify actors of state changes, assigned tasks, or time-sensitive events relevant to them
FR50: An actor can resume a previously interrupted AI interaction with their prior context and preferences available without re-stating them
FR51: Operators can query historical operational state across a configurable time range
FR52: The platform automatically emits health signals — projection lag, stream consumer status, unrouted task counts, component availability — to a dedicated observability store
FR53: An operator can revert any capability change by submitting a compensating operation through the write path without platform downtime or data surgery
FR54: A Lattice-aware AI agent can detect and flag data quality anomalies encountered during graph traversal
FR55: The platform includes a canonical reference implementation that serves as an integration test suite, developer onboarding starting point, and demonstrable vertical slice
FR56: An actor is authorized to complete an operation associated with a task assigned to them; authorization is established at task assignment time. A manager is authorized to complete tasks assigned to their direct reports, as determined by reporting-relationship links between identity vertices.
FR57: Each data type definition declares which operation types are permitted to mutate it; the platform enforces this write-scope constraint on every operation
FR58: External operations initiated by the platform's orchestration engine are idempotent; a failed or retried external call cannot result in a duplicate charge or duplicated action. The orchestration engine records a visible claim state before executing any external call and does not re-initiate a claimed operation.

### Non-Functional Requirements

**Performance:**
NFR-P1: Write throughput ≥ 10 ops/sec sustained, up to 100 ops/sec peak (single-cell MVP; Loftspace ~200 units scale)
NFR-P2: Core KV capacity supports up to 100K keys (vertices + aspects + links combined)
NFR-P3: CDC-to-projection lag < 500ms p99 (Capability Lens and general Lenses; auth correctness depends on this ceiling)
NFR-P4: Starlark execution < 100ms p99 per operation (Stream 0 spike validates; target revised if spike exceeds threshold)
NFR-P5: End-to-end latency < 2s p99 (operation submission → projection visible in Lens target)
NFR-P6: Operation commit confirmation within CDC lag window (actor can confirm durable commit before reading dependent projections)
NFR-P7: Local dev environment boot < 3 minutes first run (image pull + DB init); < 30 seconds warm restart
NFR-P8: "Hello Lattice" onboarding < 60 minutes (git clone → completed vertical slice, verified by external tester)

**Reliability:**
NFR-R1: The Processor commit path is crash-recoverable; fault injection at each of the 10 commit path steps produces the same outcome as a clean run
NFR-R2: Refractor (Lens) consumers resume from exactly their last committed offset after any restart; no events are skipped or double-processed
NFR-R3: Unrouted tasks are surfaced in Health KV observability and never silently dropped
NFR-R4: AI agent degradation produces explicit caveats and actionable guidance; no silent confidence on unverified state
NFR-R5: The `core-operations` JetStream stream is append-only; no compaction occurs by default (compaction is a deployment configuration set by the operator)
NFR-R6: Single-cell Phase 1 does not require HA clustering; a single NATS server is acceptable for development and portfolio demonstration

**Security:**
NFR-S1: All data encrypted at rest (NATS KV encryption) and in transit (TLS on all connections)
NFR-S2: The Capability Lens is the sole authorization boundary; architectural bypass is impossible by design — validated by the Phase 1 attempted-bypass test suite (4 bypass categories: direct KV write, stream publish outside `ops.*`, Starlark I/O escape, DDL schema violation)
NFR-S3: Starlark scripts execute in a sandbox with no access to external I/O, network, secrets, or non-deterministic state; validated by adversarial test suite (4 attack vectors: role escalation via direct KV write, projection lag window exposure, Lens definition mutation via AI-authored op, cross-vertex permission bleed)
NFR-S4: JWT/Lattice-Actor tokens are cryptographically signed; Gateway validates signatures before forwarding any operation
NFR-S5: PII aspects encrypted with per-identity encryption keys; key material held in external KMS/HSM, never stored in Core KV
NFR-S6: Auth denial responses are specific and actionable; do not expose internal permission graph structure beyond what the requesting actor requires
NFR-S7: Permission revocations propagate within the p99 CDC-to-projection lag ceiling (< 500ms) via event-driven Capability Lens reprojection. The Processor does NOT enforce a per-operation projection-staleness denial (the `projectedAt` hard-ceiling gate was removed in Story 1.5.4 — it false-denied unchanged-but-valid actors). The excessive-lag tail (Capability Lens projector grossly behind/dead) is an accepted bounded window, backstopped operationally (Refractor Capability-Lens health monitoring) and, for hard identity/session revocation, by Gateway JWT/token revocation (planned). `projectedAt` is deterministic input-derived provenance, not a liveness clock.
NFR-S8: GDPR / CCPA — crypto-shredding at the aspect level satisfies right-to-erasure without data deletion; sensitive aspects irrecoverable after key shredding, non-sensitive aspects retained
NFR-S9: PCI DSS — out of scope by design; raw payment credentials are never stored or processed by the platform
NFR-S10: AI agents are regular identity vertices subject to the same Capability Lens authorization as human actors; no privileged AI actor class or bypass (testable: AI agent with consumer role cannot access finance data)
NFR-S11: All external operations initiated by the orchestration engine are recorded in `weaver-claims` before the external call executes; resolve mutations carry claim reference into Core KV for audit trail completeness

**Data Integrity:**
NFR-D1: Multi-key operations commit entirely or fail entirely; no partial state observable by any reader during commit
NFR-D2: Every operation carries a unique client-generated ID; re-delivery of the same operation produces identical outcome with no side effects; Processor detects and short-circuits duplicates before applying any state change
NFR-D3: The `core-operations` JetStream stream is the immutable, ordered source of truth; no mutation, deletion, or reordering of committed entries permitted
NFR-D4: Concurrent conflicting writes to the same vertex detected via revision conditions; platform retries up to configured maximum before surfacing conflict error; last-write-wins is not the resolution strategy
NFR-D5: DDL schema violations are rejected at the write path boundary; malformed entity type definitions cannot reach Core KV

**Scalability:**
NFR-SC1: Phase 1 ceiling (single-cell): 100K keys in Core KV; 10–100 write ops/sec; single operator deployment sized for up to ~500 registered members and ~50 concurrent active sessions
NFR-SC2: Scale-out path: multi-cell architecture (Phase 3) adds horizontal scale without data model changes; cell-agnostic key design validated in Phase 1 — keys embed no cell identity
NFR-SC3: Lens target (Postgres): sized for single-building reporting at Phase 1; no sharding required
NFR-SC4: Each operator deployment runs in its own isolated NATS cluster; no cross-tenant data access possible at infrastructure level

**Evolvability:**
NFR-E1: New entity types, business rules, and projection definitions activate within the CDC-to-projection lag window (< 500ms p99) without restart, recompilation, or data migration
NFR-E2: Existing business rules and projection definitions propagate changes to all active consumers within the same lag window, without restart
NFR-E3: Any capability change is revertable via compensating operation through the write path; no out-of-band data surgery or deployment required
NFR-E4: Developers can run deterministic replay of any operation sequence against a Starlark rule or Lens definition in isolation, without a live NATS instance, for local business logic unit testing

**Operational Observability:**
NFR-O1: Health signals updated at minimum every 10 seconds; a Health KV reader never sees state older than 10 seconds plus read latency
NFR-O2: Health state is readable via direct KV reads without a Lens projection or a running Refractor instance
NFR-O3: Health signals include at minimum: projection lag per Lens consumer, stream depth, consumer offset lag, unrouted task count, and component availability status
NFR-O4: Authorization failures are traceable across three observable planes: the graph permission path in Core KV, the Capability Lens projection definition, and the Capability KV cached read

### Additional Requirements (Architecture)

- **Sole-writer rule (Core KV):** Processor is the only writer to Core KV. All mutations flow through `core-operations` → Processor → atomic batch → Core KV. Violations are an architectural defect, not a code issue.
- **Go implementation:** Maximizes Materializer codebase reuse (~80%). All components written in Go.
- **Materializer morph, not rewrite:** Existing Materializer codebase becomes Refractor. Failure tier classification must be re-audited — crypto-shred failures are privacy-critical; Capability Lens failures are security-critical. May require new tier definitions beyond original 4-tier model.
- **NATS-only core plane:** JetStream for ledger, KV for state, services for control plane. No other core plane dependencies.
- **Open-source Go openCypher parser:** Required for Capability Lens cypher evaluation. Avoids Java/ANTLR4 toolchain dependency.
- **Key structure is immutable by convention** (per Contract #1 §1.5): vertex keys are 3-segment `vtx.<type>.<id>`; aspect keys are 4-segment `vtx.<type>.<id>.<localName>` (an aspect is namespaced under its parent vertex); link keys are 6-segment `lnk.<typeA>.<idA>.<relation>.<typeB>.<idB>` where direction is a DDL authoring decision per Contract #1 §1.1 (source side = vertex that typically arrives *later* in graph growth; target side = vertex that typically *pre-exists*). No `asp.*` or `lnk.<id>.<name>.<id>` legacy shapes; those references in earlier draft text were superseded.
- **`isDeleted` filtering enforced independently by every KV reader:** Soft-deleted entities remain addressable by key. Processor enforces in commit path; Refractor enforces independently during CDC. No KV-level access control on tombstones.
- **DDL mutations serialized via `ops.meta.>` lane:** Processor cache invalidation is synchronous with meta-lane commit. No concurrent DDL changes by design.
- **Starlark spike is a Stream 0 gate:** Must complete before Stream 1 commit path design begins. If p99 > 100ms, commit path architecture changes materially.
- **NATS atomic batch spike is a Stream 0 gate:** Must validate NATS atomic batch size ceiling and durable consumer count limits against expected scale before committing to architecture.
- **Contract-first development:** Weeks 1–4 are schema-locking sprints. Nearly every inter-stream blocker is a contract, not an implementation.
- **`permittedCommands` field in DDL (FR57 implementation):** Each DDL meta-vertex includes `permittedCommands` array. Validated at Processor commit path step 6.
- **Task-based auth via Capability Lens (FR56 implementation):** Capability Lens cypher rule extended to traverse `identity → task-assignment → task → required-permissions`. No separate auth mechanism.
- **Two-Phase Nudge claims in `weaver-claims` (FR58 implementation):** Claims are direct KV writes by Weaver (not through Processor). Resolve mutations go through Processor as normal business state carrying `claim-id` reference. 90-day retention default.
- **AI agents as identity vertices (NFR-S10 implementation):** Naming convention `identity:ai.<purpose>.<id>`. Same Capability Lens, same commit path auth. Distinct from internal service actors (Loom, Weaver) which have root-equivalent capability.
- **Scale-aware Vault key-caching in Refractor (NFR-S5 implementation):** Phase 1 uses direct Vault calls + simple LRU cache. Phase 2+ graduates to TTL-based cache with `KeyShredded` event-driven invalidation. No shadow aspects.
- **Aspect-level sensitivity boundary:** DDL `"sensitive": true` at aspect-type level. Sensitive aspects must attach to identity vertices. Enforced at Processor commit path step 6 (MutationBatch validator).
- **Fault injection harness needed for crash recovery tests (NFR-R1):** Materializer's test harness does NOT support fault injection today. Stream 1 requires a `FailAfterN` wrapper on JetStream publish. New work.
- **Read-Your-Own-Writes (MVP tier):** Processor response includes `vtx.op` tracker ID; client polls tracker until projection catches up. Sufficient for MVP. Full RYOW overlay protocol deferred to Phase 2.
- **KV Bucket taxonomy (all buckets):**
  - Core KV: business state (Processor sole writer)
  - Adjacency KV: Refractor-private graph index (Materializer heritage)
  - Health KV: all components write directly under `health.<component>.<instance>`
  - Capability KV: Refractor writes (Capability Lens target); Processor reads for O(1) auth
  - Token Revocation KV: Gateway / admin writes; Gateway reads (MVP auth kill-switch)
  - Weaver Operational KV: `weaver-state` — Weaver-internal dispatch/in-flight state
  - Weaver Claims KV: `weaver-claims` — Two-Phase Nudge claim records (90-day retention)
  - Lens target stores: Postgres (MVP); ES, NATS streams (Phase 2+)

### UX Design Requirements

*No UX Design document exists for Phase 1 — the platform is NATS-native and CLI-only. No browser client or consumer-facing UI is in scope for Phase 1. Developer/operator experience is CLI + NATS CLI + Health KV direct reads.*

### FR Coverage Map

| FR | Epic | Description |
|----|------|-------------|
| FR1 | Epic 4 | Staff creates unclaimed identity record |
| FR2 | Epic 4 | Resident self-registers + binds to unclaimed identity via claim keys |
| FR3 | Epic 4 | Duplicate identity detection (fuzzy match, human review) |
| FR4 | Epic 4 | Staff-approved identity merge workflow |
| FR5 | Epic 4 | Grandfathered leaseholder claims identity + accesses history |
| FR6 | Epic 4 | Identity state machine (unclaimed → claimed → flagged → merged) |
| FR7 | Epic 4 | Identity and interaction history persist after lease/membership ends |
| FR8 | Epic 1 | Single validated write path; no direct Core KV writes from outside |
| FR9 | Epic 1 | Deterministic Starlark scripts; no I/O, network, secrets, non-determinism |
| FR10 | Epic 1 | New entity types/rules/projections activated without redeployment |
| FR11 | Epic 1 | Immutable ordered ledger entry per operation (author, timestamp, payload) |
| FR12 | Epic 1 | Operation idempotency; resubmit = same outcome, no duplicate state |
| FR13 | Epic 1 | Atomic multi-key operations (commit all or fail all) |
| FR14 | Epic 1 | Actor can confirm durable commit before reading dependent projections |
| FR15 | Epic 2 | Business state projected into query-optimized external targets via Lens definitions |
| FR16 | Epic 2 | Lens definitions created/modified/activated as platform operations without redeployment |
| FR17 | Phase 2+ | Front-of-house staff query surface (Loftspace vertical + browser client) |
| FR18 | Phase 2+ | Back-of-house operator query surface (Loftspace vertical + browser client) |
| FR19 | Epic 5 | AI agent traverses from identity vertex to commands/schemas/descriptions cold-start |
| FR20 | Epic 2 | Projection lag is bounded and observable |
| FR21 | Epic 3 | Permission relationships derived from graph structure; authorize every operation |
| FR22 | Epic 3 | Denial response specifies missing permission, actor's role, and roles that carry it (structural only — Phase 1) |
| FR23 | Epic 3 | Auth failures traceable across three planes (Core KV path, Lens projection, Capability KV cached read) |
| FR24 | Epic 3 | Operators define and assign role-scoped access for all actor types |
| FR25 | Epic 3 | Operators audit which actors hold which permissions |
| FR26 | Phase 2+ | Multi-step workflows with conditional branching and human approval gates (Loom) |
| FR27 | Phase 2+ | Convergence target enforcement + remediation task assignment (Weaver) |
| FR28 | Phase 2+ | Task assignment to actor or role-based queue with fallback |
| FR29 | Phase 2+ | Unrouted tasks surface in health monitoring; never silently dropped |
| FR30 | Phase 2+ | Operators view/modify/revoke convergence targets |
| FR31 | Phase 2+ | AI-authored capability proposal (natural language → DDL + Starlark + Lens) |
| FR32 | Phase 2+ | Capability bundle review + approval via task-based workflow |
| FR33 | Phase 2+ | AI agent pending intent persisted in graph across sessions |
| FR34 | Epic 5 | AI agent submits validated intent through standard write path |
| FR35 | Phase 2+ | Operators view AI-authored capability changes (author, timestamp, approver) |
| FR36 | Phase 2+ | Capability authorship governance accessible from same surface as health state |
| FR37 | Phase 2+ | Per-field PII encryption with key shredding (Stream 6 / Vault) |
| FR38 | Phase 2+ | Non-personal fields retained after key shredding; audit record intact |
| FR39 | Phase 2+ | Erasure guard — active financial obligations require operator override |
| FR40 | Phase 2+ | Payment record model (transaction ref, status, amount — no raw credentials) |
| FR41 | Phase 2+ | Data retention policy configurable per data type |
| FR42 | Phase 2+ | Immutable ledger mirroring to external long-term retention store |
| FR43 | Epic 6 | Developer boots complete local environment from single command (`make up`) |
| FR44 | Epic 6 | Developer completes minimal vertical slice in < 60 min from fresh clone |
| FR45 | Epic 6 | CLI tool for operations, graph inspection, projection queries |
| FR46 | Epic 6 | Platform health readable from dedicated Health KV store |
| FR47 | Phase 2+ | Developer/operator console with AI-suggested capability changes (browser UI) |
| FR48 | Epic 6 | Platform deployments isolated at infrastructure level (own NATS cluster) |
| FR49 | Phase 2+ | Actor notifications (state changes, task assignments, time-sensitive events) |
| FR50 | Phase 2+ | Actor resumes interrupted AI interaction with prior context preserved |
| FR51 | Phase 2+ | Operators query historical operational state across configurable time range (deferred from MVP) |
| FR52 | Epic 6 | Platform emits health signals to dedicated observability store (projection lag, stream depth, consumer offset, unrouted task count, component availability) |
| FR53 | Epic 5 | Operator reverts any capability change via compensating operation through write path — no platform downtime or data surgery |
| FR54 | Phase 2+ | AI agent detects and flags data quality anomalies during graph traversal |
| FR55 | Epic 6 | Canonical reference implementation ("Hello Lattice") — integration test + onboarding tutorial + live demo |
| FR56 | Epic 3 | Task assignment creates authorization; manager delegates via reporting-chain links; Capability Lens absorbs as ephemeral entries |
| FR57 | Epic 1 | DDL `permittedCommands` field; write-scope validated at Processor commit path step 6 |
| FR58 | Phase 2+ | Two-Phase Nudge external operation idempotency via `weaver-claims` (requires Weaver — Phase 2) |

---

## Epic List

### Epic 1: Substrate & Trustworthy Write Path
Developers can submit operations to a running Lattice instance with guaranteed atomic commit, idempotency, schema enforcement, and deterministic Starlark execution. This is the foundational contract everything else builds on. **First two stories must be the Starlark spike and NATS atomic batch spike — both are blocking gates before any implementation proceeds.**
**FRs covered:** FR8, FR9, FR10, FR11, FR12, FR13, FR14, FR57
**Phase 1 gates delivered:** Starlark spike (gate 1), Attempted bypass test suite (gate 2)

### Epic 2: Live Lens Projections (Materializer Morph)
Lift-and-shift the Materializer codebase into Lattice as Refractor — adjusting only what Lattice's data contracts require, preserving existing Materializer capabilities (target adapter interface, NATS KV target, Postgres target, control service, health/lag reporting, rule-definition sourcing). Close with a written functional gap analysis that grounds Epic 3 prerequisites and Phase 2+ backlog in measured reality. Historical state query (FR51) is deferred to Phase 2+.
**FRs covered:** FR15, FR16, FR20
**Phase 1 gates delivered:** Capability Lens adversarial test suite (gate 3, joint with Epic 3)

### Epic 3: Authorization & Security Perimeter
Every operation is authorized against graph-derived permissions with O(1) lookup. All four architectural bypass categories are impossible and test-proven. Auth failures produce structural, traceable, actionable responses (missing permission + role that carries it — no routing/escalation workflow). Enables Journey 5.
**FRs covered:** FR21, FR22, FR23, FR24, FR25, FR56
**Phase 1 gates delivered:** Capability Lens 4 attack vectors (gate 3), Bypass test suite (gate 2, joint with Epic 1)

### Epic 4: Identity & Member Lifecycle
Staff can register unclaimed member identities. Residents can self-register or claim grandfathered accounts via the two-phase claim model. The identity state machine (unclaimed → claimed → flagged → merged) is fully operational. Enables Journey 0 Paths A and C.
**FRs covered:** FR1, FR2, FR3, FR4, FR5, FR6, FR7

### Epic 5: AI-Native Platform Navigation
A Lattice-aware AI agent with zero prior knowledge of the deployment can traverse the graph from its own identity vertex — discovering available commands, input schemas, and plain-language descriptions — and submit a validated intent through the standard write path. Any capability change is revertable via compensating operation. This is Journey 6: the Phase 1 north star.
**FRs covered:** FR19, FR34, FR53
**Phase 1 gates delivered:** Compensating op / DDL rollback integration test (gate 4)

### Epic 6: Developer Experience & "Hello Lattice"
A developer can go from `git clone` to a working vertical slice — one entity type, one Starlark rule, one Lens projection, one AI traversal query — in under 60 minutes. The canonical reference implementation ("Hello Lattice") serves simultaneously as integration test suite, onboarding tutorial, and live demo. This epic validates that all five preceding epics wire together correctly.
**FRs covered:** FR43, FR44, FR45, FR46, FR48, FR52, FR55
**Phase 1 gates delivered:** "Hello Lattice" < 60 min from `git clone`, verified by external tester (gate 5)

### Phase 1.5: Hardening Block (SHIPPED)
Post-Phase-1 remediation (six-component CR sweep + stories 1.5.1–1.5.11), not new capability — closed the substrate-direct install pattern, hardened the write-path/kernel meta-DDL contracts, made capability-auth freshness deterministic, re-enabled Gate 5 to a full pass, and froze contract shapes behind a conformance suite. **Full roster + detail: [phase-1-epics.md → Phase 1.5 Hardening Block](./phase-1-epics.md#phase-15-hardening-block).**
**Phase 2 prerequisite (met):** Story 1.5.10 (transactional event outbox) + 1.5.11 (publisher relocation) shipped — `core-events` are intentional declared-schema events Loom/Weaver depend on, so event fidelity (persist the script-returned `EventList` in the step-8 atomic batch; a durable consumer republishes the *real* events on redelivery) is a hard gate for Phase 2 orchestration.

### Phase 2: Orchestration Core (epic list approved 2026-06-01)

Scope = Loom + Weaver + Two-Phase Nudge (FR26–27, FR29–30, FR58) on minimal core + the `lease-signing` reference package. Decisions of record: `lattice-architecture.md` → "Phase 2 Architecture — Orchestration Core" (D1–D5). Engine detail: `docs/components/{loom,weaver}.md`. Shapes: `docs/contracts/10-orchestration-surfaces.md`. **FR28 (role-queue + fallback) deferred to Phase 3** (the thin-slice demo uses direct identity assignment — building an unexercised role-queue was cut). Read-path auth / Gateway / Vault / AI-authoring / console / historical-query → Phase 3.

#### Epic 7: Orchestration Foundations
The platform can model, assign, and surface **tasks**. Ships the generic `task` DDL in a foundational `orchestration-base` package (D5 placement: cap-lens-read scalars on root `data`, relationships as links, no aspects), service-actor bootstrap provisioning (Loom/Weaver primordial identities, root-equivalent), the `AllowMsgSchedules` schedule-stream bootstrap config (ADR-51 prerequisite), and a "my-tasks" query Lens. **FR29 safety:** task assignment is fail-loud — an unresolvable assignee surfaces in Health KV, never silently dropped.
**FRs covered:** FR26 (task substrate), FR29

#### Epic 8: Loom — Deterministic Flow Engine
Deterministic multi-step procedures (NOT inherently user-facing) run to completion. Skeleton-first stories: walking skeleton (one pattern, system-op steps, no guards) → user-task steps → pure on/off guards (rebuildable cursor). Engine is a generic interpreter; patterns are package data. Demo flow: onboarding / verify-info.
**FRs covered:** FR26 (conditional steps = on/off guards + human-approval = user-tasks; *branching* is Weaver, per D3)

#### Epic 9: Weaver — Convergence Engine
A declared **target state converges**; gaps are detected and remediated. Skeleton-first stories: 9.1 target-as-Lens (row-per-candidate + `violating` flag) + lane-1 violation-driven + OCC actuator → 9.2 anti-storm `weaver-state` in-flight marks + TTL/lease reconciliation → 9.3 temporal lane (ADR-51 scheduled messages → internal subject → op) → 9.4 Weaver control-API/CLI (target list/disable/revoke = **FR30**, mirrors the Refractor control plane; no console dependency). Weaver consumes the Refractor (target = Lens); triggers Loom via op. *(Event-driven targeted-audit = lane-2, Phase-3-deferred — not an Epic 9 story.)*
**FRs covered:** FR27, FR30

#### Epic 10: External Convergence — Two-Phase Nudge
The platform calls external systems **exactly once**: claim (`weaver-claims`, before the call) → execute → resolve (op through Processor carrying `claim-id`). External Adapter framework proven by mocked reference adapters (`FakeStripe`, `FakeBackgroundCheck`). Real adapters are Phase 3.
**FRs covered:** FR58 (+ NFR-S11: claim recorded before external call executes)

#### Epic 11: Loftspace Reference Vertical
Author the installable `lease-signing` package — target Lens cypher ("Lease Application complete"), playbooks (gap → action), Loom pattern definitions, mocked-adapter config — and prove it end-to-end with a **convergence-harness e2e** (drain-then-assert: drive a lease-app from all-gaps-violating to steady state; assert the target row's `violating` flips false and *stays* false). Engines are already fixture-proven, so this epic is thin: package authoring + the e2e. Dogfoods that the package model carries orchestration content.
**FRs covered:** integration (FR26, FR27, FR29, FR30, FR58 end-to-end)

---

**Phase 2 (Orchestration Core) FRs:** FR26, FR27, FR29, FR30, FR58 — see "Phase 2: Orchestration Core" epic list above (Epics 7–11).

**Phase 3+ Deferred FRs:** FR17, FR18, FR28 *(role-queue + fallback — deferred from Phase 2; demo uses direct identity assignment)*, FR31–33, FR35–42, FR47, FR49, FR50, FR51, FR54
*(All mapped in FR Coverage Map above — none forgotten)*

**Phase 2+ Deferred Architectural Capabilities** *(silent gaps named explicitly so they are not assumed away):*
- **Historical state query support (FR51)** — replay any operation sequence into a temporary Lens target across a configurable time range. Phase 1 has the substrate (immutable `core-operations` stream, NFR-R5) but no operator-facing replay machinery.
- **Read-path authorization for Lens targets** — direct reads from Lens targets bypass the Capability Lens write-path boundary (NFR-S2); Phase 1/2 assume trusted readers. **Rubric written (Phase 2 sprint, Decision D1, 2026-06-01), build deferred to Phase 3:** leading approach is **Postgres RLS backed by a dedicated Capability-Read Lens** (RLS policies join against a projected grants table — DB-level filter, graph traversal stays once in the Refractor); JWT carries identity id, Gateway enforces + sets `lattice.actor_id`; read-proxy is the fallback. See `lattice-architecture.md` D1.
- **Refractor target adapter coverage** *(surfaced by Story 2.2 gap analysis §3.3, §3.4)* — Phase 1 ships only NATS KV and Postgres adapters. Elasticsearch adapter (§3.3) and NATS-subject publish-events adapter (§3.4 — required for Personal Lens fan-out per morph plan §2.1) are deferred. Adds new adapter implementations against the existing `adapter.Adapter` interface.
- **Refractor substrate inner-package migration** *(surfaced by Story 2.2 gap analysis §3.7, Deviation 5)* — 30 files inside `internal/refractor/` (15 production + 15 test) still consume raw `nats-io/nats.go` / `jetstream` handles. Substrate boundary is currently set at `cmd/refractor` + four new files only. Deferred deep refactor requires extending `internal/substrate` with Watch / UpdatesOnly / NumPending / durable-consumer helpers first.
- **NATS Services framework migration for Refractor control plane** *(surfaced by Story 2.2 gap analysis §3.8, Deviation 6)* — Refractor's control plane still uses `QueueSubscribe` on `refractor.control` rather than `micro.AddService`-based endpoints on `lattice.ctrl.refractor.<lensId>.<op>`. Operational-polish item per morph plan Phase 6.
- ~~**Refractor pipeline key-shape adaptation** *(surfaced by Story 2.2 gap analysis §2.5 / §3.13, Deviation 13 — Epic-3-blocking carry)* — `pipeline.parseCoreKVKey` still recognizes only legacy Materializer `node_<label>_<id>` keys; Contract-correct `vtx.<type>.<id>` and `lnk.*` keys are unrecognized and skipped.~~ **CLOSED (verified in code 2026-06-01 during Phase 2 planning).** The pipeline now routes all key handling through `substrate.ClassifyKey` / `ParseVertexKey` / `ParseLinkKey` / `ParseAspectKey` (`internal/refractor/pipeline/pipeline.go`, `evaluate.go`); Contract-correct `vtx.`/`lnk.`/aspect keys are recognized and projected. Closed as a side effect of Stories 1.5.8 (aspect CDC fan-out) + 1.5.9 (lens property model) rewriting the pipeline's key handling. Legacy `node_<label>_<id>` parsing survives **only** in the test fixture harness (`internal/refractor/fixture/`), which is a low-priority test-debt cleanup, not a Weaver/Phase-2 blocker.
- **Refractor adapter document-envelope reshape** *(surfaced by Story 2.2 gap analysis §3.9 — open carry from 2.1's Deliverable #12)* — Adapter outputs do not yet align with Contract #1's envelope shape (`{key, class, revision, ...payload}`). Requires adapter interface signature change and caller updates.
- **Refractor `Rule → Lens` Go-type cleanup pass** *(surfaced by Story 2.2 gap analysis §3.10, Deviations 4 + 11a)* — Package was renamed `rule → lens` but the Go type `Rule` and JSON field `ruleId` were not. Vestigial empty `team` field also produces double-dot subject patterns (e.g., `lattice.dlq..<lensId>`). Single cleanup pass.
- **Refractor shared-test-fixture helper to revert `-p 1`** *(surfaced by Story 2.2 gap analysis §3.11, Deviation 14)* — `go test ./...` currently requires `-p 1` to avoid embedded-NATS file-descriptor exhaustion. Path: one embedded NATS per test binary at TestMain reused across `t.Run` subtests.
- **Read-from-body-not-key — NATS read discipline** *(Andrew, binding principle; surfaced repeatedly)* — everywhere the platform reads from NATS, the information must come from the message **body**, not from re-parsing the key. Current violations: the Refractor adjacency consumer (`internal/refractor/consumer/bootstrap.go` `processLinkEnvelope` → `substrate.ParseLinkKey`) and the pipeline fan-out (`internal/refractor/pipeline/evaluate.go` `evaluateLinkFanOut`/`evaluateAspectFanOut` → `ParseLinkKey`/`ParseAspectKey`) reconstruct endpoints/direction from the key. The key is an addressing convenience, not the source of truth; the body carries the authoritative `sourceVertex`/`targetVertex`/`localName` (and `vertexKey` for aspects). Adopting this also gives the link envelope's `sourceVertex`/`targetVertex` fields their first reader (today they are write-only). Andrew owns the rollout; recorded here so it is not assumed away.
- ~~**Task/service business-data placement (root `data` vs aspect)**~~ **RESOLVED (Phase 2 architecture sprint, Decision D5, 2026-06-01).** Principle adopted: **Capability-Lens-read fields → vertex root `data`; all other business data → aspects** (subsumes the permissions exception; root remains available case-by-case for other justified reasons). Tasks: only scalars (`status`, `expiresAt`) on root `data`; **relationships (granted op, target) are LINKS, not fields**, and the ephemeral-grant projection **extracts to the `orchestration-base` `capabilityEphemeral` lens** (a1) so the bootstrap cap-lens drops `task` entirely — this **supersedes the earlier "`task.data.*` reads unchanged"** (see Story 7.1 + Contract #10 §10.1/§10.7). Generic `task` DDL ships in `orchestration-base` (Epic 7); service-actor vertices provisioned at bootstrap. See `lattice-architecture.md` D5.
- **Refractor structural-failure classification — undefined_column (42703)** — `failure.Classify` is confirmed to pause the rule on Postgres `undefined_table` (42P01); verify it also pauses on `undefined_column` (42703) so a schema/column mismatch is a structural pause (FR19a `pauseReason:structural`), not a silently-retried transient. Low-effort robustness check surfaced during Gate 5 (M4) work.
- **Package version upgrade (F-004)** — `InstallPackage`/`UninstallPackage` kernel ops exist (Story 1.5.5), but in-place package version upgrade (re-install over an existing version, DDL migration semantics) is deferred to its own story.
- **UninstallPackage per-key OCC (low CAR)** — `UninstallPackage` does not yet apply per-key optimistic-concurrency conditions across the keys it tombstones; a concurrent mutation during uninstall could race. Low-severity carry from Story 1.5.5.

---

## Phase 2: Orchestration Core — sprint reference & deferred carries (2026-06-01)

Epic list is in the "Phase 2: Orchestration Core" subsection of the Epic List above (Epics 7–11); detailed stories follow per epic. This block records the cross-cutting sprint outputs and the carries surfaced. **Charter:** `phase-2-charter.md`. **Decisions:** `lattice-architecture.md` → "Phase 2 Architecture — Orchestration Core" (D1–D5). **Engine detail:** `docs/components/{loom,weaver}.md`. **Shapes:** `docs/contracts/10-orchestration-surfaces.md`.

**Cross-cutting story guidance (from the sprint's party-mode reviews):**
- **Skeleton-first** for session-per-story: Loom = one-pattern/system-op-steps/no-guards → user-task steps → guards; Weaver = lane-1 → anti-storm+TTL → temporal (ADR-51) → targeted-audit → control-API.
- **FR29 (unrouted tasks surface in health; never silently dropped)** = fail-loud safety AC in Epic 7. **FR28 (role-queue + fallback) DEFERRED to Phase 3** — the thin-slice demo uses direct identity assignment; building an unexercised role-queue was cut.
- **Bootstrap prerequisite (Epic 7):** enable `AllowMsgSchedules` on a schedule stream (same shape as the existing atomic-publish flag) for the ADR-51 temporal lane.
- **No epic-zero Refractor work:** pull a just-in-time Refractor hardening story *inside Epic 9* only if the target Lens forces read-from-body / envelope-reshape (the key-shape §2.5 carry is already CLOSED).
- **Comment policy** (Anti-Patterns table, `lattice-architecture.md`) carried into every story brief: comments describe WHAT/WHY, never which story decided/changed it.

**Phase 2+ carries surfaced this sprint** (decomposed later, not stories now):
- **Refractor negative/filter-retraction projection** — true "emit-only-when-violating" target Lenses. Phase 2 sidesteps it via row-per-candidate + `violating` flag (D4). Needed at scale.
- **Weaver lane-2 on-demand evaluation** — built into the engine, *unexercised* by the demo (the lease target is fully expressible as a continuous Lens).
- **L3 (AI-assisted) evaluator tier** — Phase 3 (AI-authoring territory).
- **Full temporal scheduler / op-vertex pruner (#47/#49)** — Phase 2 ships only the thin ADR-51 one-shot path.
- **Shared Two-Phase Nudge actuator** — promote out of `internal/weaver/nudge/` only if a future Loom *saga* needs external steps.

---


## Epic Shards

Detailed stories (BDD acceptance criteria + source hints) are sharded by phase:

| Shard | Contents | Status |
|-------|----------|--------|
| [phase-1-epics.md](./phase-1-epics.md) | Epics 1–6 — Substrate, Lens projections, Auth perimeter, Identity lifecycle, AI-native navigation, DX/Hello-Lattice | SHIPPED |
| [phase-2-epics.md](./phase-2-epics.md) | Epics 7–11 — Orchestration Foundations, Loom, Weaver, Two-Phase Nudge, Loftspace reference vertical | Active |

This `index.md` holds the cross-phase material (Overview, Documentation Layering Rule, Requirements Inventory, Epic List, Phase 2 sprint reference). `bmad-create-story` reads the sharded dir (`*epic*/*.md`, SELECTIVE_LOAD); the top-level `epics.md` is a pointer here.
