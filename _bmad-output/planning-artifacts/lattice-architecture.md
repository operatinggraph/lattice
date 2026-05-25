---
stepsCompleted: [1, 2, 3, 4, 5, 6, 7, 8, "prd-alignment"]
lastStep: "prd-alignment"
status: 'complete'
completedAt: '2026-04-11'
inputDocuments:
  - "brainstorming-session-2026-04-08.md"
  - "materializer-morph-plan.md"
  - "Lattice System Spec.md (Obsidian)"
  - "prd.md (2026-04-10)"
workflowType: 'architecture'
project_name: 'Lattice'
user_name: 'Andrew'
date: '2026-04-09'
---

# Architecture Decision Document

_This document builds collaboratively through step-by-step discovery. Sections are appended as we work through each architectural decision together._

## Project Context Analysis

> **Note:** This section is the *foundation* — it captures requirements, constraints, principles, and cross-cutting concerns discovered during architectural analysis. Later sections will provide **component-level reference pages** for each major component (Processor, Refractor, Gateway, Loom, Weaver, Vault) that collect: what it owns, what it reads/writes, its contracts in/out, failure modes, and which principles/constraints apply. Implementers should consult the component page first, then trace back to this section for rationale.

### Requirements Overview

**Functional Requirements:**
125 nameable work items spanning: storage/data plane primitives (7), ledger/operations (7), processor/logic engine (6), schema/DDL (5), identity/auth/ReBAC (6), refractor/lenses (9), orchestration (9), services/commands/tasks (6), crypto-shredding/PII (7), semantic contracts (4), cross-cutting/platform (10), sharding/cells (13), observability/control (11), adversarial mitigations (8), structural gap items (17).

7 active MVP streams (0–6) with clear ownership boundaries and boundary contracts.
4 deferred post-MVP streams (7–10: Closed-loop Ops, Cells/Sharding, Edge Lattice, Semantic Contracts).

Refractor inherits Materializer's full operational stack (4-tier failure classification, DLQ, retry queue, deferred re-evaluation, pause/resume/rebuild/replay control plane, in-memory NATS test harness) as-is — these are adopted, not re-designed. Stream 2 MVP runs independently via the inherited test harness; integration with Stream 1's Processor is a separate milestone.

**Non-Functional Requirements:**
- **Consistency:** Atomic batches via NATS 2.12; revision-condition optimistic concurrency; idempotency via Tracker vertices
- **Security:** ReBAC via Capability Lens; JWT actor authentication; mTLS between services; token revocation KV at Gateway. **Capability Lens is a security-critical projection** — auth correctness depends on projection correctness. A bug in the Capability Lens cypher rule = privilege escalation. Requires dedicated adversarial test suite. MVP: token revocation KV provides kill-switch (not consistency guarantee). v2: capability vector clock fence provides consistency. Auth debugging spans 3 state planes (Core KV permission paths → Capability Lens projection → Capability KV read) — observability must support this trace.
- **Privacy/Compliance:** Per-identity crypto-shredding; `sensitive` field markers; Vault with KMS; GDPR right-to-erasure
- **Observability:** Health-as-KV (operational state plane); consumer-lag metrics; operation-id = trace span; structured logging
- **Determinism:** Starlark sandbox (no I/O, no time, no random); scripts are pure functions over graph state
- **Availability:** 4-tier failure classification; DLQ; retry queue; deferred re-evaluation; pause/resume/rebuild primitives
- **Extensibility:** Multi-target Lens adapters (Postgres, ES, NATS KV, NATS streams); pluggable Vault; External Adapter framework for Weaver
- **Processor read amplification is the primary latency driver.** Each operation requires reads from Core KV (existing entity state), DDL cache (schema validation), Capability KV (auth check), and idempotency tracker (dedup). Context Hinting (declared read set on Command vertices) and JIT Hydration (per-op working set cache) are architectural mitigations, not optional optimizations — they are required for acceptable latency at scale.

**Scale & Complexity:**

- Primary domain: distributed backend / data infrastructure
- Complexity level: enterprise
- Estimated architectural components: 12+ (Substrate, Processor, Refractor, Gateway, Loom, Weaver, Vault, Client SDK, CLI tools, dev harness, test harness, observability libraries)

### Technical Constraints & Dependencies

- **NATS is the sole core plane dependency** — JetStream for ledger, KV for state, services for control plane. The core data plane (operations, KV writes, CDC, inter-service messaging) depends only on NATS. **Integration dependencies** (Postgres for Lens query surfaces, KMS/HSM for Vault, external IdP for actor signing) are separate — each needs its own availability and degradation posture. The system continues to accept and process operations if an integration dependency is down; only the dependent capability degrades.
- **Go is the implementation language** — maximizes Materializer codebase reuse (~80%)
- **Single-cell MVP** — all cross-cell concerns (sharding, Bridge Links, migration) deferred post-MVP
- **Materializer morph, not rewrite** — existing codebase becomes Refractor. Inherits operational stack as-is, but **failure tier classification must be re-audited** against Lattice's failure semantics. Crypto-shred failures are privacy-critical (cannot silently DLQ a shred event). Capability Lens failures are security-critical (stale auth = potential privilege escalation). These may require new tier definitions or escalation paths beyond Materializer's original 4-tier model.
- **Open-source Go openCypher parser** — avoids Java/ANTLR4 toolchain dependency
- **External KMS/HSM for Vault** — choice must be made in week 1 of Stream 6. Integration dependency with its own degradation posture.
- **External IdP for actor signing keys** — Stream 3 depends on this choice. Integration dependency.
- **Key structure is immutable by convention** — keys are addressing conventions (`vtx.<type>.<id>`, `asp.<vtxId>.<name>`, `lnk.<youngerId>.<name>.<olderId>`); only value schemas evolve via DDL. No "key migration" tool is needed or planned.
- **Every KV reader must independently enforce `isDeleted` filtering** — soft-deleted entities remain addressable by key. The Processor enforces this in the commit path; Refractor must enforce it independently when reading Core KV via CDC. There is no KV-level access control on tombstones.
- **DDL mutations are serialized via `ops.meta.>` lane** — Processor cache invalidation is synchronous with meta-lane commit. No concurrent DDL changes by design.

### KV Bucket Taxonomy

The system uses multiple NATS KV buckets with distinct ownership rules:

| Bucket | Owner (writes) | Readers | Purpose |
|--------|----------------|---------|---------|
| **Core KV** | Processor only (via `core-operations`) | Refractor (durable consumers on KV backing stream), Processor | Business state: vertices, aspects, links, DDL meta-vertices, op trackers, Loom instances |
| **Adjacency KV** | Refractor | Refractor | Refractor-private graph adjacency index for evaluator traversal. Inherited from Materializer. Ownership scope TBD — may remain Refractor-only or become shared. |
| **Health KV** | All components (direct write) | Any consumer, dashboards | Single shared bucket. Components write under distinct keys (`health.<component>.<instance>`). Operational self-reporting — NOT Core KV, NOT a Lens, NOT a vertex/aspect. This is the only sanctioned direct-KV-write pattern outside Refractor's own targets. |
| **Capability KV** | Refractor (Capability Lens target) | Processor (O(1) authz read), Gateway | Flattened permission path cache. A Lens projection, not a standalone auth store. |
| **Token Revocation KV** | Gateway / admin | Gateway | Kill-switch for compromised actors (MVP auth mitigation) |
| **Weaver Operational KV** | Weaver | Weaver | `weaver.state.>` — Weaver's internal dispatch state |
| **Weaver Claims KV** | Weaver | Weaver, audit consumers | `weaver.claims.>` — Two-Phase Nudge claim/resolve records for external operation idempotency (see PRD Alignment Addendum, Item 3) |
| **Lens target stores** | Refractor | Application queries | Postgres, Elasticsearch, per-user NATS streams — external to NATS KV |

*This table covers architecturally significant buckets with distinct ownership rules. Implementation may require additional operational stores (script caches, consumer checkpoints, compiled query plan caches). These are implementation details owned by the respective component — not architectural contracts — but should be documented in component-level design docs as they emerge.*

### Cross-Cutting Concerns Identified

- **CDC vs `core-events` delivery contracts differ fundamentally.** CDC is consumed via **durable JetStream consumers** on Core KV's backing stream — one durable consumer per Lens definition (inherited from Materializer's per-rule consumer model). These are NOT ephemeral KV watchers. Durable consumers provide: resume from last acked position after restart, consumer groups for horizontal scaling, ack-based delivery guarantees, and per-consumer offset tracking. `core-events` on JetStream are stream-ordered with ack semantics. Both patterns use durable consumers, but CDC consumers read the KV backing stream (all key mutations) while `core-events` consumers read explicitly published business events. **Developer mental model:** every KV write is visible to Refractor (implicit CDC via KV backing stream); only explicitly published events in the Starlark `EventList` reach Loom/Weaver.
- **Schema evolution** — DDL in Core KV with meta lane fencing; Lens schema migration requires zero-downtime swap
- **Idempotency** — 24h horizon with Tracker vertices; Loom workflows that sleep for weeks need extended dedupe patterns
- **Contract-first development** — weeks 1–4 are schema-locking sprints; almost every inter-stream blocker is a contract, not an implementation
- **Vault availability** — SPOF for sensitive aspects; cache strategy + degradation mode required
- **Read-Your-Own-Writes** — async projection gap bridged by client-side overlay protocol
- **Auth debugging complexity** — permission failures require tracing across Core KV (path exists?), Capability Lens (projection caught up?), and Capability KV (correct value?). Observability tooling must support this 3-plane trace.
- **Refractor is the system-wide liveness bottleneck.** Highest fan-in node: consumes Core KV CDC (via durable consumers on backing stream), DDL meta-vertex CDC, Lens definition CDC, `KeyShredded` events, Vault decrypt interface. Produces outputs consumed by Processor (Capability KV), Weaver (target Lenses), Gateway (indirectly), applications (query surfaces), and Edge Lattice (Personal Lens, post-MVP). If Refractor stalls, effects cascade: auth drifts, convergence stops, queries lie. Refractor health is a **system-level liveness indicator**, not just a component metric.
- **Lens activation is eventually consistent.** Writing `vtx.meta.lens.*` to Core KV does NOT mean the Lens is projecting — CDC propagation, Refractor loading, and initial projection are all async. Refractor Health KV should expose per-Lens activation state so consumers can distinguish "not yet active" from "active and empty."
- **Internal service actor model.** Loom, Weaver, and admin tools operate within the trust boundary with their own internal service actor identities at root-level access. They submit ops directly to the ledger (bypassing Gateway), using pre-provisioned signing keys. These are `Lattice-Actor: identity:<service_name>` with root-equivalent capabilities. Stream 3 must define the provisioning and identity semantics for internal service actors separately from user-facing identity.
- **Health KV schema is convention at MVP, not a hard contract.** No automated consumer reads Health KV at MVP (Closed-loop Weaver auditor is deferred to Stream 7). Format can evolve freely. Hardens into a contract when Stream 7 lands.
- **Adjacency KV is Refractor-private at MVP.** All graph queries that other components need must be expressed as Lenses and projected into target stores. Direct Adjacency KV access by non-Refractor components is explicitly prohibited at MVP. This forces all graph query needs through the Lens abstraction.

### Canonical Consumption Patterns

Two fundamentally different data consumption patterns exist in the system. All components must choose one:

| Pattern | Consumers | Source | Semantics | Failure Mode | Self-Healing |
|---------|-----------|--------|-----------|-------------|-------------|
| **CDC-reactive** | Refractor | Durable JetStream consumers on Core KV's backing stream (one per Lens definition) | Per-key, revision-ordered, tolerates duplicates and reorder | Stale projections (eventually catches up) | Resume from last acked position; replay from sequence 0 for rebuilds |
| **Event-driven** | Loom, Weaver | `core-events` JetStream | Stream-ordered, ack-based, at-least-once | Missed/delayed orchestration actions | DLQ, retry, explicit replay |

CDC-reactive components self-heal via revision convergence. Event-driven components need explicit DLQ and retry semantics. Future components must declare which pattern they follow.

A third pattern exists for **control plane operations** (pause, resume, rebuild, replay): synchronous **request-reply** via NATS services. This is neither CDC nor event-driven — it is imperative, synchronous, and operator-initiated. Not included in the table above because it is not a data consumption pattern, but it is a communication pattern that all components implement.

### Domain Semantics & PRD Dependency

This architecture document defines *technical* structure — data shapes, component boundaries, communication patterns. It does NOT define *domain semantics* — the business meaning of vertex types, event names, or workflow triggers. A **Product Requirements Document (PRD)** is a dependency for:
- Stream 4 (Orchestration) — Loom/Weaver patterns react to events whose business meaning must be precisely defined
- Stream 5 (Services/SDK) — Command/Query types reflect domain operations
- Quantitative Targets — order-of-magnitude scale numbers require a target user scenario (e.g., "mid-size property management company, ~10,000 active leases, ~100 concurrent users, ~50 ops/minute at peak")

A domain glossary or ubiquitous language document is recommended alongside the PRD to prevent event names from becoming meaningless strings across streams.

### Test Ownership for Cross-Stream Concerns

Cross-stream concerns require explicit test ownership:

| Concern | Test Owner | Why |
|---------|-----------|-----|
| Processor commit path crash recovery (idempotent retry) | Stream 1 | Processor owns the commit path |
| Duplicate event tolerance (Loom/Weaver) | Stream 4 | Consumer owns dedup |
| Capability Lens adversarial suite (privilege escalation) | Stream 3 (defines semantics) + Stream 2 (validates projection) | Joint ownership — Stream 3 writes test cases, Stream 2 ensures Refractor passes them |
| Crypto-shred propagation (row nullification) | Stream 6 (defines event) + Stream 2 (validates handler) | Joint — Stream 6 writes test scenario, Stream 2 validates Refractor behavior |
| Event schema validation (reject bad events before KV write) | Stream 1 | Processor owns validation |
| Fault injection harness (crash between atomic batch and event publish) | Stream 1 (test cases) + Stream 0 (harness utility) | Materializer's harness does NOT support fault injection today. Stream 1 needs a `FailAfterN` wrapper on JetStream publish to validate crash recovery. New work. |

### Execution Model

The stream decomposition defines **architectural boundaries**, not team boundaries. Execution is single-developer, which eliminates inter-team coordination risks but introduces different concerns: cognitive load of holding multiple streams in one head, implicit assumptions leaking across stream boundaries, and scope creep without external accountability. This architecture document is the primary mitigation — it externalizes decisions so they survive context switches between streams.

### MVP Vertical Slice Must-Haves

The MVP vertical slice (one op → ledger → Processor → Core KV → Refractor → Postgres → query) must include a minimal **Read-Your-Own-Writes mitigation** to avoid the double-submit problem during demos and early testing. Full RYOW overlay (Stream 5) can be deferred, but a simplified version is required at MVP: the Processor's response to an operation should include the `vtx.op` tracker ID, and the client can poll the tracker until the projection catches up. This is simpler than the full overlay protocol and sufficient for MVP.

### Recommended First Milestone Per Stream

The architecture defines inter-stream dependencies but not intra-stream sequencing (that's implementation planning). However, each stream should have a concrete **first deliverable milestone** to guide initial focus:

| Stream | First Milestone |
|--------|----------------|
| **0 — Substrate** | **Spike stories first:** validate NATS atomic batch size ceiling and durable consumer count limits against expected scale. Then: operation envelope schema v1 frozen + dev harness (`make up`) boots NATS with buckets + bootstrap root identity |
| **1 — Core** | Processor consumes a hardcoded operation, runs a trivial Starlark script, writes one vertex to Core KV via atomic batch |
| **2 — Refractor** | Materializer fork watches one Core KV backing stream via durable consumer and projects one Lens to Postgres (Go module rename is a prerequisite, not the milestone) |
| **3 — Identity/Auth** | Identity vertex type DDL + Gateway stamps hardcoded `Lattice-Actor` header + Capability Lens cypher feasibility spike |
| **4 — Orchestration** | Loom consumes one event from `core-events`, creates a Loom Instance Vertex via Processor, advances one step |
| **5 — Services/SDK** | Command vertex type DDL + CLI tool submits one operation and displays `vtx.op` tracker result |
| **6 — Privacy** | KMS choice made + Vault interface implementation + one sensitive field encrypted on write, decrypted on read |

### Quantitative Targets (Locked — PRD 2026-04-10)

| Target | Value | Rationale |
|--------|-------|-----------|
| **Write throughput** | 10–100 ops/sec sustained | Single-cell MVP; single-building Loftspace scale (~200 units) |
| **Core KV capacity** | Up to 100K keys | Vertices + aspects + links combined |
| **CDC-to-projection lag** | < 500ms p99 | Capability Lens and general Lenses; auth correctness depends on this ceiling |
| **Starlark execution** | < 100ms p99 per operation | Stream 0 spike validates before commit path design begins |
| **End-to-end latency** | < 2s p99 | Operation submission → projection visible in Lens target |
| **Demand sizing** | ~500 registered members, ~50 concurrent active sessions | Single-operator deployment at Phase 1 |
| **Onboarding** | < 60 min from `git clone` to working vertical slice | "Hello Lattice" developer experience target |

These values are design constraints, not SLOs. They inform batch sizes, cache TTLs, consumer parallelism, and Starlark spike go/no-go thresholds. Source: PRD §Non-Functional Requirements / Performance and §Success Criteria.

### Processor Commit Path

The Processor's commit sequence for a single operation:

1. **Consume** operation from `core-operations` JetStream (lane consumer)
2. **Dedup check** — read idempotency tracker (`vtx.op.<request-id>`)
3. **Auth check** — read Capability KV (O(1) lookup)
4. **Hydrate** — read existing entity state from Core KV (Context Hinting declares the read set; JIT Hydration caches the working set)
5. **Execute** — run Starlark script in sandbox; script returns `MutationBatch` (KV writes) + `EventList` (business events)
6. **Validate MutationBatch** — check against DDL JSON Schema
7. **Validate EventList** — check against event DDL meta-vertices (`vtx.meta.event.<name>`) — event schema validation happens BEFORE any KV writes
8. **Atomic batch** — write all KV mutations AND the idempotency tracker (`vtx.op.<request-id>`) in a single NATS 2.12 atomic batch with revision conditions. The tracker is part of the batch, not a separate write.
9. **Publish events** — publish validated events to `core-events` JetStream
10. **Ack** — acknowledge the `core-operations` JetStream message

If step 7 (event validation) fails, the entire operation is rejected — no KV writes occur. Events are schema-governed via DDL meta-vertices, same as vertices/aspects/links.

**Crash recovery and idempotency:** The `core-operations` message is only acked after the entire commit path completes (step 10). If the Processor crashes between step 8 (atomic batch) and step 9 (event publish), JetStream redelivers the operation. On retry, step 2 finds the idempotency tracker (written as part of the atomic batch in step 8). **The entire commit path must be idempotent:** on dedup detection, the Processor must still re-derive and publish events before acking, since the previous attempt may have crashed before event publication. KV writes are naturally idempotent (revision conditions cause a no-op on retry); event publication must also be safe to repeat (Loom/Weaver consumers must tolerate duplicate events via their own dedup or idempotent handling).

### Event Schema Governance

Events published to `core-events` are schema-governed, not fire-and-forget:

- **Event type DDL** lives in Core KV as `vtx.meta.event.<name>` meta-vertices, alongside vertex/aspect/link DDL
- **Starlark scripts do not publish events directly** — they return an `EventList` to the Processor
- **Processor validates event schemas** against registered DDL before committing any KV writes (fail-fast: invalid event = rejected operation)
- **Loom/Weaver consume typed, validated events** — the `core-events` stream is a schema-governed contract, not a schema-less firehose
- **Event DDL changes** follow the same `ops.meta.>` lane serialization as vertex/aspect/link DDL
- **Events are a three-way contract** between the producing Starlark script, the event DDL, and the consuming orchestration patterns (Loom/Weaver). Event schemas should be designed consumer-aware — Loom/Weaver pattern authors should have input into event schema design. **Convention:** events should carry references to the triggering operation's vertex IDs, allowing consumers to hydrate additional context from Lens projections rather than demanding all context in the event payload. This keeps events lean and decouples producers from consumers' evolving context needs.

### Architectural Principles

Foundational truths the architecture rests on, stated precisely:

**P1: All business and meta-domain state is vertices, aspects, and links in Core KV.** DDL, Lens definitions, Loom instances, idempotency trackers, event type definitions — all are vertices. Operational/internal state (Health KV, Weaver dispatch, Adjacency KV) lives outside Core KV in purpose-specific stores. **The boundary:** if it has business meaning or is consumed by other components' business logic, it's a vertex. If it's internal bookkeeping for a single component, it's operational state.

**P2: The Processor is the sole writer to Core KV — no exceptions.** All mutations flow through `core-operations` → Processor → atomic batch → Core KV. This is not just a write path — it is a **serialization point** that eliminates write-write conflicts, guarantees schema validation, enforces auth, and produces the total-ordered ledger. Loom and Weaver are clients that submit operations back through the ledger. DDL changes go through the Processor via `ops.meta.>`. The Replay tool replays operations through the Processor (it does not bypass it for direct KV writes).

**P3: NATS is sufficient for all core plane needs.** JetStream provides the durable ordered replayable ledger. KV provides state with revision tracking and watchers. Services provide the control plane. Two NATS capabilities require early validation against expected scale:
- **Atomic batch size ceiling** — NATS has byte/op limits per batch. The maximum mutation size a single operation can produce must be determined and documented. If a legitimate business operation exceeds it, the Processor needs a saga/compensation pattern.
- **Durable consumer count scalability** — Refractor creates one durable consumer per Lens definition on Core KV's backing stream. At MVP scale (tens of Lenses) this is fine. At scale (hundreds of Lenses), the number of concurrent durable consumers on a single stream needs validation. This is a well-understood NATS scaling dimension, unlike ephemeral KV watchers.

**P4: Starlark enforces single-operation invariants only.** Scripts are pure functions: `(current_state, operation) → (mutations, events)`. They validate within the scope of a single operation's hydrated working set. Cross-entity invariants (e.g., "background check must complete before lease") are enforced by Loom (sequential workflow). Declarative convergence invariants (e.g., "invoice total must match line items") are enforced by Weaver. Starlark performance benchmarking is a **Stream 1 concern** — if Starlark is too slow, the Processor is too slow, and discovering this in Stream 5 (SDK) is too late.

**P5: Lenses are the only application query surface.** Applications never read Core KV directly for queries. All query traffic goes through Lens projections in target stores (Postgres, ES, NATS KV, NATS streams). Core KV is optimized for writes (atomic batches, revision tracking, per-key granularity); Lenses transform write-optimized shapes into query-optimized shapes (CQRS by architecture). Developer/admin inspection of Core KV via NATS CLI is permitted for debugging but is not a supported application query path.

**P6: Single-cell MVP is safe because the data model is cell-agnostic.** Key naming (`vtx.<type>.<id>`, `lnk.<youngerId>.<name>.<olderId>`) does not embed cell identity. Multi-cell is purely a routing/replication concern layered underneath — no data model or business logic changes required. Safety depends on the expected MVP data volume fitting within NATS KV single-bucket scalability limits (validated against Quantitative Targets above: up to 100K keys at MVP).

> Deployment isolation model and Phase 3 scale-out path: see `docs/operations/deployment-isolation.md`.

## Starter / Foundation Evaluation

### Primary Technology Domain

Distributed backend / data infrastructure — Go services on NATS. No frontend framework at this stage.

### Foundation: Not a Starter Template, a Codebase Morph

This project does not use a starter template. The foundation is the existing **Materializer codebase** (`github.com/materializer`), morphed into the Refractor component. This decision was locked during brainstorming (Architectural Decision #9: "Materializer ≈ Refractor at MVP grade. Morph, don't rewrite.").

### Technology Decisions (Locked)

**Language & Runtime:**
- Go (version TBD — match Materializer's current Go version)
- No TypeScript, no Node.js in the core plane

**Infrastructure:**
- NATS Server (JetStream, KV) — sole core plane dependency
- Postgres — integration dependency (Lens target store)
- External KMS/HSM — integration dependency (Vault)

**Build Tooling:**
- Go modules (`go.mod`)
- `make` for build/test/run targets
- Dev harness: `make up` boots NATS with buckets + bootstrap data

**Testing Framework:**
- Go standard `testing` package
- In-memory NATS test harness (inherited from Materializer)
- Deterministic-replay golden tests for Starlark scripts
- Fault injection wrapper (new — needed for crash recovery testing)

**Code Organization:**
- Inherited from Materializer's project structure, adapted for Lattice component boundaries
- Module-per-component when components diverge (Processor, Refractor, Gateway, Loom, Weaver may become separate Go modules or separate binaries)

**Starlark Embedding:**
- `go.starlark.net` — Starlark interpreter for Go
- Determinism guard: no I/O, no time, no random in sandbox

**openCypher Parser:**
- ANTLR runtime: `github.com/antlr4-go/antlr/v4 v4.13.1`
- Grammar + generated Go parser vendored from `github.com/jtejido/go-opencypher` (as of 2026-05-15); see `internal/refractor/ruleengine/full/cypher/README.md` for the copied files. Vendoring avoids an upstream-module dependency and lets Refractor own the listener/visitor.

**Note:** The first implementation work is NOT "initialize a project" — it is the Stream 0 spike stories (NATS atomic batch ceiling, durable consumer limits) followed by the Materializer fork and module rename.

## Core Architectural Decisions

### Decision Priority Analysis

**Critical Decisions (Block Implementation) — All Made:**
- Single Core KV bucket for MVP (confirmed by NATS atomic batch constraint)
- NanoID for entity identification (20-char default, 8-char exception)
- Monolith deployment for MVP, split later
- Mono-repo

**Important Decisions (Shape Architecture) — Made:**
- Self-hosted NATS
- GitHub Actions CI/CD
- Retention policy enforcement via Weaver (not NATS-level retention)

**Deferred Decisions (Post-MVP or Decide in Stream):**
- External IdP choice (decide in Stream 3)
- Internal service actor provisioning mechanism (decide in Stream 3)
- External API surface — REST/gRPC/NATS direct (decide in Stream 5; MVP uses CLI → NATS direct)
- Multi-bucket strategy for sharding (decide in deferred Stream 8)

### Data Architecture

**Single Core KV Bucket (MVP)**

All vertices, aspects, links, DDL meta-vertices, idempotency trackers, Loom instances, and event type definitions live in a single NATS KV bucket backed by a single JetStream stream. This is required because **NATS atomic batches target a single stream** — multiple buckets would break cross-entity atomicity.

- Single bucket = one backing stream = atomic batches work across all entity types
- Refractor's durable consumers all watch the same backing stream
- Retention policy enforcement (e.g., pruning old idempotency trackers) is handled by Weaver, not NATS-level retention settings
- Multi-bucket sharding is deferred to Stream 8 (Cells)
- **Spike required:** validate atomic batch behavior within a single KV bucket — size ceiling, revision condition semantics, maximum keys per batch

**Entity ID Generation: NanoID**

All entity IDs use NanoID with a custom alphabet that removes ambiguous characters (I/l, O/0, etc.), yielding ~58 usable characters.

| Length | Use Case | Collision Space | Collision Risk |
|--------|----------|----------------|----------------|
| **20-char** (default) | All vertex/entity IDs — tenants, leases, identities, DDL meta-vertices, Lens definitions, Loom instances, etc. | 58^20 ≈ 10^35 | Effectively zero |
| **8-char** (exception) | Human-facing short codes where readability is prioritized and collision risk is acceptable — display codes, short references shared verbally. NOT primary keys. | 58^8 ≈ 128 billion | ~50% at 358K IDs of same type |

- ID generation is part of the **Starlark stdlib** — length is controlled at the API level, not by script authors
- IDs are NOT time-sortable (unlike ULID). Chronological ordering comes from the JetStream backing stream sequence, not from key ordering. This is acceptable because KV lookups are by exact key.
- The custom alphabet and predefined lengths are defined once in Stream 0 (substrate) and consumed by all components

### Authentication & Security

**Decided:**
- ReBAC via Capability Lens (from brainstorming)
- JWT actor authentication with `Lattice-Actor` header (from brainstorming)
- mTLS between services (from brainstorming)
- Token revocation KV at Gateway for MVP (from brainstorming)
- Internal service actors operate within trust boundary at root level (from context analysis)

**Deferred to Stream 3:**
- External IdP choice (Auth0, Keycloak, NATS-native, etc.)
- JWT signing algorithm
- Internal service actor key provisioning mechanism (config file, boot-time generation, Vault-stored)
- mTLS certificate management approach

### API & Communication Patterns

**Decided:**
- Internal communication: NATS subjects (operations, events, control plane)
- Core plane: `core-operations` (JetStream, 3 lanes), `core-events` (JetStream)
- Control plane: NATS request-reply services (`ctrl.<type>.<id>`)
- CDC: Durable JetStream consumers on Core KV backing stream (one per Lens)

**Deferred to Stream 5:**
- External API surface for clients (REST via Gateway, gRPC, or NATS direct)
- MVP uses CLI tool connecting to NATS directly — Gateway is a separate concern

### Infrastructure & Deployment

**Deployment Model: Monolith for MVP, Split Later**

MVP ships as a single Go binary with subcommands or a single process running all components (Processor, Refractor, Gateway stub). Components are architected as separate packages with clean interfaces so they can be extracted into separate binaries when operational needs require it (independent scaling, independent deployment, fault isolation).

Extraction triggers (when to split):
- A component needs independent horizontal scaling (e.g., Refractor under high Lens load)
- A component failure should not take down the entire system
- Different components need different deployment cadences

**Repository Structure: Mono-repo**

All components live in a single repository. Go workspace (`go.work`) may be used if components need separate Go modules, but for MVP a single `go.mod` is sufficient.

Benefits for single-developer execution model:
- Atomic cross-component changes (no version coordination)
- Single CI/CD pipeline
- Shared test harness and dev tooling
- Easier to enforce architectural boundaries via package visibility

**NATS: Self-hosted**

Self-hosted NATS server for development and initial deployment. Managed NATS (Synadia Cloud) is a future option for production if operational burden warrants it.

**CI/CD: GitHub Actions**

Standard GitHub Actions pipeline. Details deferred to implementation planning.

### Decision Impact Analysis

**Implementation Sequence:**
1. Stream 0 spike: validate NATS atomic batch within single KV bucket (go/no-go gate)
2. Stream 0 spike: validate durable consumer count limits
3. Stream 0: NanoID library selection/implementation with custom alphabet
4. Stream 0: dev harness with single Core KV bucket provisioning
5. Stream 1: Processor with monolith entry point
6. Stream 2: Materializer fork into mono-repo

**Cross-Component Dependencies:**
- Single bucket decision affects all components — Processor writes, Refractor reads, everyone uses the same backing stream
- NanoID is consumed by Starlark stdlib (Stream 1) and must be available before any vertex creation
- Monolith deployment means all components share a process for MVP — failure isolation is at the goroutine level, not the process level
- Mono-repo means Stream 2 (Materializer fork) imports into the same repo rather than remaining a separate dependency

## Implementation Patterns & Consistency Rules

These patterns prevent AI agents from making conflicting implementation choices. All agents implementing any Lattice component MUST follow these conventions.

### Go Code Naming

Follow standard Go conventions — no Lattice-specific deviations:

- **Packages:** short, lowercase, single-word when possible (`processor`, `refractor`, `gateway`, `evaluator`, `adapter`). No abbreviations (`proc`, `ref`) unless universally understood.
- **Interfaces:** `-er` suffix (`Writer`, `Evaluator`, `Projector`). No domain prefix unless disambiguation is needed.
- **Exported symbols:** export minimally. A package's public API should be the smallest surface that allows consumers to use it.
- **Receivers:** short, 1-2 letter, consistent within a type (`p` for Processor, `r` for Refractor, `e` for Evaluator).
- **Files:** lowercase, underscores for multi-word (`commit_path.go`, `lens_loader.go`). Match the primary type or concept in the file.

### JSON Field Naming in KV Values

**camelCase for all JSON fields in NATS KV values.** This matches Materializer's existing convention and applies to all vertex, aspect, link, DDL, event, and operational KV entries.

```json
// Vertex value
{"type": "tenant", "name": "Acme Corp", "isDeleted": false}

// Link value
{"nodeId": "abc123", "otherNodeId": "def456", "name": "memberOf", "direction": "outbound", "isDeleted": false}

// Aspect value
{"leaseStartDate": "2026-05-01", "monthlyRent": 2500, "status": "active"}

// Event payload
{"leaseId": "abc123", "tenantId": "def456", "signedAt": "2026-05-01T14:30:00Z"}
```

**No exceptions.** If a component reads or writes KV values with snake_case fields, it is a bug.

### NATS Subject Taxonomy

All NATS subjects follow a hierarchical dot-separated naming convention:

```
# Core operations (JetStream stream: core-operations)
ops.meta.>              # DDL changes, Lens definitions — sequential
ops.urgent.>            # Time-sensitive business operations
ops.bulk.>              # Batch imports, migrations, background work

# Core events (JetStream stream: core-events)
events.<domain>.<name>  # e.g., events.lease.signed, events.tenant.created

# Control plane (NATS request-reply services)
ctrl.<component>.<instance>.<action>
                        # e.g., ctrl.refractor.lens-001.pause
                        #       ctrl.refractor.lens-001.rebuild
                        #       ctrl.processor.main.status

# Health (NATS KV keys, not subjects — but follow same convention)
health.<component>.<instance>
                        # e.g., health.refractor.main
                        #       health.processor.main
```

**Naming rules:**
- All lowercase
- Dot-separated hierarchy
- Domain terms match DDL type names (e.g., `events.lease.*` corresponds to `vtx.meta.ddl.lease`)
- Component names match Go package names
- No `lattice.` prefix on any subject

### Error Handling

**Sentinel errors for simple cases, typed errors when context is needed.**

```go
// Sentinel errors (package-level)
var (
    ErrNotFound      = errors.New("not found")
    ErrAlreadyExists = errors.New("already exists")
    ErrRevisionConflict = errors.New("revision conflict")
)

// Typed errors (when callers need structured context)
type ValidationError struct {
    Field   string
    Message string
}

func (e *ValidationError) Error() string {
    return fmt.Sprintf("validation: %s: %s", e.Field, e.Message)
}
```

**Rules:**
- Use `fmt.Errorf("context: %w", err)` for wrapping — always preserve the chain
- Use `errors.Is()` for sentinel checks, `errors.As()` for typed error extraction
- The Processor commit path returns structured errors that include the failing step (e.g., "auth check failed", "event validation failed") so the operation submitter gets actionable feedback
- Never swallow errors silently — log and propagate, or log and handle

### Structured Logging

**`slog` (Go stdlib)** — matches Materializer. No third-party logging dependencies.

**Standard fields on every log line:**

| Field | Type | Present | Description |
|-------|------|---------|-------------|
| `component` | string | always | `processor`, `refractor`, `gateway`, `loom`, `weaver` |
| `operationId` | string | when processing an op | The `vtx.op.<request-id>` being processed |
| `lensId` | string | in Refractor | The Lens being projected |
| `consumerId` | string | when applicable | The durable consumer name |

**Log levels:**
- `debug` — internal state changes useful for development (KV reads, cache hits/misses)
- `info` — significant lifecycle events (component started, Lens activated, operation committed)
- `warn` — recoverable issues (retry triggered, DLQ'd message, degraded mode entered)
- `error` — unrecoverable issues requiring attention (commit path failure, Vault unavailable, atomic batch rejected)

**Example:**
```go
slog.Info("operation committed",
    "component", "processor",
    "operationId", reqID,
    "mutations", len(batch.Mutations),
    "events", len(batch.Events),
)
```

### Test Organization

**White-box tests in same package, `_test.go` suffix.** Matches Go convention and Materializer.

| Test Type | Location | Build Tag | Description |
|-----------|----------|-----------|-------------|
| **Unit tests** | Same package, `*_test.go` | none | Fast, no external dependencies |
| **Golden tests** | Same package, `*_test.go` + `testdata/` fixtures | none | Deterministic-replay tests for Starlark scripts (Processor) and Lens projections (Refractor) |
| **Integration tests** | Same package, `*_integration_test.go` | `//go:build integration` | Require in-memory NATS server; test cross-component flows |

**Golden test pattern (inherited from Materializer):**
- Fixture files in `testdata/` directory adjacent to test
- YAML-defined input state + operation → expected output state
- Deterministic replay: same input always produces same output
- Used for: Starlark script validation, Lens projection validation, commit path end-to-end

**Fault injection tests:**
- Use `FailAfterN` wrapper on JetStream publish (new capability)
- Located with integration tests, tagged `//go:build integration`
- Validate crash recovery and idempotent retry paths

### Configuration

**Single `config.yaml` at repo root**, with per-component sections. Environment variable overrides supported. Matches Materializer's pattern.

```yaml
# config.yaml
nats:
  url: "nats://localhost:4222"
  # ... connection settings

processor:
  lanes:
    meta:
      consumers: 1        # sequential by design
    urgent:
      consumers: 4
    bulk:
      consumers: 2
  starlark:
    scriptDir: "./scripts"
    maxExecutionMs: 500

refractor:
  healthIntervalSec: 10
  # ... per-lens overrides loaded from Core KV, not this file

gateway:
  port: 8080
  tokenRevocationBucketName: "token-revocation"

# ... other components
```

**Rules:**
- Config file is at repo root, next to `Dockerfile`
- Per-component sections match Go package names
- Environment variable override convention: `LATTICE_PROCESSOR_LANES_URGENT_CONSUMERS=8`
- Secrets (KMS keys, NATS credentials) NEVER in config.yaml — use env vars or mounted secret files
- Lens definitions are NOT in config — they live in Core KV as `vtx.meta.lens.*` meta-vertices
- Starlark scripts are NOT in config — they are loaded from a script directory

### Anti-Patterns (What NOT to Do)

| Anti-Pattern | Why It's Wrong | Correct Pattern |
|-------------|---------------|-----------------|
| snake_case JSON fields in KV values | Breaks cross-component compatibility | camelCase always |
| Direct KV writes outside Processor | Violates P2 (sole writer) | Submit operation via `core-operations` |
| Reading Core KV for application queries | Violates P5 (Lenses only) | Query Lens target stores |
| Swallowing errors in commit path | Hides failures, corrupts state | Log + propagate, or log + handle |
| Hardcoded NATS subjects | Breaks subject taxonomy | Use constants from shared `subjects` package |
| Per-component logging libraries | Inconsistent log formats | `slog` everywhere, standard fields |
| Config values for Lens definitions | Lens defs are meta-vertices in Core KV | Load from Core KV via bootstrap Lens |
| Ephemeral KV watchers for CDC | No resume, no ack, no offset tracking | Durable JetStream consumers on backing stream |
| Go import from Loom/Weaver to Processor | Creates extraction-blocking coupling | Communicate via NATS subjects; share only `substrate/*` types |

## Project Structure & Boundaries

### Gateway Architecture Decision

The internet-facing gateway is **not custom Go code**. Production uses a standard reverse proxy (NGINX or Envoy) for internet-facing concerns, with a thin Go translator service behind it:

| Layer | Responsibility | Implementation |
|-------|---------------|----------------|
| **Reverse proxy** (NGINX/Envoy) | TLS termination, rate limiting, IP allowlisting, request size limits, CORS, HTTP/2, connection pooling, DDoS mitigation, access logs | Infrastructure config (`deploy/`) — not Go code |
| **Translator** (`internal/gateway/`) | JWT validation, `Lattice-Actor` stamping, token revocation KV check, HTTP → NATS publish | Thin Go service — Lattice-specific logic only |

For MVP, the translator runs behind whatever reverse proxy the deployment provides (e.g., NGINX in `docker-compose.yml`). Internet-facing hardening is operational, not architectural.

### Complete Project Directory Structure

```
lattice/
├── .github/
│   └── workflows/
│       └── ci.yml                    # GitHub Actions CI pipeline
├── cmd/
│   └── lattice/
│       └── main.go                   # Single binary entry point (MVP monolith)
├── config.yaml                       # Shared config with per-component sections
├── config.example.yaml               # Template with documentation
├── Dockerfile
├── Makefile                          # build, test, test-integration, up, down
├── docker-compose.yml                # Dev harness: NATS + NGINX + bootstrap
├── go.mod
├── go.sum
├── CLAUDE.md
│
├── deploy/                           # Deployment/infrastructure config
│   ├── nginx.conf                   # Reverse proxy config (dev/staging)
│   └── nats-server.conf            # NATS server configuration
│
├── internal/
│   │
│   ├── substrate/                    # Stream 0 — shared platform primitives
│   │   ├── envelope/                # Operation envelope schema, parsing, validation
│   │   ├── subjects/                # NATS subject constants and builders
│   │   ├── nanoid/                  # NanoID generator with custom alphabet (20-char default, 8-char exception)
│   │   ├── nats/                    # Shared NATS helpers: consumer lifecycle (pause/resume/reset),
│   │   │                            #   header propagation, connection management, durable consumer factory
│   │   ├── actor/                   # Lattice-Actor header spec, JWT verification
│   │   ├── health/                  # Health KV format, heartbeat library
│   │   ├── control/                 # NATS Control Service standard (request-reply protocol)
│   │   ├── logging/                 # slog helpers, standard fields
│   │   └── harness/                 # In-memory NATS test harness (lifted from Materializer)
│   │
│   ├── processor/                    # Stream 1 — Core (Processor + KV write plane)
│   │   ├── processor.go             # Main service: consumer → dispatcher → commit path
│   │   ├── commit.go                # 10-step commit path implementation
│   │   ├── starlark/                # Starlark sandbox, stdlib (incl. NanoID), script loader/cache
│   │   ├── ddl/                     # DDL meta-vertex types, JSON Schema validator, event DDL
│   │   ├── kv/                      # Core KV read/write primitives, atomic batch
│   │   ├── idempotency/             # Tracker writer, dedup check, crash recovery re-derive
│   │   └── vault/                   # Vault middleware interface (encrypt/decrypt contract)
│   │
│   ├── refractor/                    # Stream 2 — Projection plane (morphed from Materializer)
│   │   ├── refractor.go             # Main service: durable consumers → evaluator → adapters
│   │   ├── engine/                  # openCypher parser, compiler, evaluator (from Materializer)
│   │   ├── adjacency/              # Adjacency KV builder (from Materializer)
│   │   ├── consumer/               # Durable consumer per Lens (from Materializer)
│   │   ├── adapter/                # Target store adapters: Postgres, NATS KV, ES
│   │   ├── pipeline/               # Projection pipeline orchestration (from Materializer)
│   │   ├── failure/                # 4-tier failure classification, DLQ, retry (from Materializer)
│   │   ├── lens/                   # Lens meta-vertex loader, bootstrap lens, lifecycle
│   │   ├── shred/                  # Crypto-shred row-nullification handler
│   │   └── control/               # Refractor-specific NATS control endpoints
│   │
│   ├── gateway/                     # Stream 3 (partial) — Thin translator behind reverse proxy
│   │   ├── gateway.go              # HTTP entry point, HTTP → NATS translation
│   │   ├── auth/                   # JWT validation, Lattice-Actor stamping
│   │   └── revocation/            # Token revocation KV check
│   │
│   ├── identity/                    # Stream 3 — Identity, Auth, Capability
│   │   ├── vertex.go               # Identity vertex type DDL definitions
│   │   ├── capability/             # Capability Lens definition, reader API
│   │   └── rebac/                  # Permission path semantics
│   │
│   ├── loom/                        # Stream 4 (partial) — Short utility workflows
│   │   ├── loom.go                 # Sensorium, Transition Engine, Actuator
│   │   ├── pattern/                # Pattern blueprint format, interpreter
│   │   └── instance/              # Loom Instance Vertex management
│   │
│   ├── weaver/                      # Stream 4 (partial) — Convergence engine
│   │   ├── weaver.go               # Sensorium, Evaluator, Strategist, Actuator
│   │   ├── target/                 # Declarative target definitions (as Lenses)
│   │   ├── nudge/                  # Two-Phase Nudge, external adapters
│   │   └── pruner/                # Operation Vertex pruner (idempotency-horizon GC)
│   │
│   ├── privacy/                     # Stream 6 — Crypto-shredding & PII
│   │   ├── vault/                  # Vault service implementation (KMS integration)
│   │   ├── middleware/             # Encrypt-on-write, decrypt-on-read implementation
│   │   └── shred/                 # Key-shred command, KeyShredded event emission
│   │
│   └── service/                     # Stream 5 — Services, Commands, Tasks, Client SDK
│       ├── command/                # Command/Query vertex types, DDL
│       ├── task/                   # Task vertex type, assignedTo semantics
│       ├── discovery/             # Command discovery API
│       └── form/                  # UI Form Schema aspect format
│
├── scripts/                         # Starlark business logic scripts
│   └── examples/                   # Example scripts for "Hello Lattice"
│
├── testdata/
│   ├── fixtures/                    # Golden test fixtures (YAML state + operation → expected)
│   └── scripts/                    # Test Starlark scripts
│
├── tools/                           # Build/dev tooling
│   ├── bootstrap/                  # Bootstrap data for dev harness (root identity, DDL)
│   └── cli/                       # CLI tool for submitting operations (Stream 5 MVP)
│
└── docs/                            # Project documentation
```

### Stream-to-Package Mapping

| Stream | Primary Packages | Shared Dependencies |
|--------|-----------------|---------------------|
| **0 — Substrate** | `internal/substrate/*` | None (bedrock) |
| **1 — Core** | `internal/processor/*` | `substrate/*` |
| **2 — Refractor** | `internal/refractor/*` | `substrate/*` |
| **3 — Identity/Auth** | `internal/identity/*`, `internal/gateway/*` | `substrate/*` |
| **4 — Orchestration** | `internal/loom/*`, `internal/weaver/*` | `substrate/*` |
| **5 — Services/SDK** | `internal/service/*`, `tools/cli/` | `substrate/*` |
| **6 — Privacy** | `internal/privacy/*` | `substrate/*`, `processor/vault` (interface only) |

### Architectural Boundaries — Package Import Rules

**Enforced by convention, verifiable by linter:**

| Package | May Import | Must NOT Import |
|---------|-----------|-----------------|
| `substrate/*` | Go stdlib only | Any `internal/` package |
| `processor` | `substrate/*`, `privacy/vault` (interface only) | `refractor`, `loom`, `weaver`, `gateway`, `service` |
| `refractor` | `substrate/*` | `processor`, `loom`, `weaver`, `gateway`, `service` |
| `gateway` | `substrate/*`, `identity` | `processor`, `refractor`, `loom`, `weaver` |
| `identity` | `substrate/*` | `processor`, `refractor`, `loom`, `weaver` |
| `loom` | `substrate/*` | `processor`, `refractor`, `weaver` |
| `weaver` | `substrate/*` | `processor`, `refractor`, `loom` |
| `privacy` | `substrate/*` | `refractor`, `loom`, `weaver`, `gateway`, `service` |
| `service` | `substrate/*` | All implementation packages |

**Key boundary principle:** Components that communicate at runtime via NATS (Loom → Processor, Weaver → Refractor) must NOT have Go import dependencies on each other. They share only `substrate/*` types (envelope, subjects, actor, nats helpers) and communicate via NATS subjects. This ensures they can be extracted into separate binaries later without untangling import cycles.

### Data Flow Through Structure

```
tools/cli/ or external client
    → NGINX/Envoy (TLS, rate limit, CORS)
        → gateway/ (JWT → Lattice-Actor, token revocation check, HTTP → NATS)
            → NATS publish to core-operations
                → processor/ consumes from JetStream (3 lane consumers)
                    → processor/commit.go runs 10-step commit path
                    → processor/starlark/ executes script from scripts/
                    → processor/kv/ atomic batch to Core KV
                    → NATS publish to core-events
                        → loom/ consumes events
                        → weaver/ consumes events
                → Core KV backing stream
                    → refractor/consumer/ durable consumer per Lens
                    → refractor/engine/ evaluates openCypher
                    → refractor/adapter/ writes to Postgres / NATS KV / ES
```

### Materializer Morph Mapping

| Materializer Package | Lattice Destination | Changes |
|---------------------|---------------------|---------|
| `cmd/materializer` | `cmd/lattice` | Renamed, multi-component entry point |
| `internal/adapter` | `internal/refractor/adapter` | Add ES adapter, Personal Lens adapter |
| `internal/adjacency` | `internal/refractor/adjacency` | Minimal changes |
| `internal/config` | `internal/substrate/` + root `config.yaml` | Split: shared substrate config + per-component |
| `internal/consumer` | `internal/refractor/consumer` | Subject rename, uses `substrate/nats/` helpers |
| `internal/control` | `internal/refractor/control` + `internal/substrate/control` | Split: Refractor-specific + shared standard |
| `internal/engine` | `internal/refractor/engine` | Parser swap (open-source), morph delta items |
| `internal/failure` | `internal/refractor/failure` | Re-audit tiers for crypto/auth severity |
| `internal/fixture` | `internal/substrate/harness` | Lifted as shared test utility |
| `internal/health` | `internal/substrate/health` | Lifted as shared library |
| `internal/pipeline` | `internal/refractor/pipeline` | Minimal changes |
| `internal/rule` | `internal/refractor/lens` | Rule → Lens, YAML → Core KV meta-vertex |
| `internal/subjects` | `internal/substrate/subjects` | Lifted + renamed subjects |
| `testdata/` | `testdata/` | Expanded with Processor fixtures |
| `grammar/` | `internal/refractor/engine/grammar/` or removed | Depends on parser strategy |

## Architecture Validation Results

### Coherence Validation ✅

**Decision Compatibility:**
- All technology choices are internally consistent: Go + NATS + Postgres, single binary monolith, mono-repo, `slog`, standard Go testing — no conflicts.
- NanoID in Starlark stdlib aligns with P2 (Processor is sole writer) — IDs are generated during operation processing, not externally.
- Single Core KV bucket decision is compatible with atomic batch requirement and Refractor's durable-consumer-per-Lens model (all consumers watch same backing stream).
- Monolith deployment is compatible with the import boundary rules — clean extraction later is architecturally supported.

**Pattern Consistency:**
- Naming conventions are consistent: camelCase JSON, lowercase dot-separated NATS subjects, Go standard naming, package names matching component names in config/subjects/logging.
- Anti-patterns table correctly mirrors the positive patterns — no contradictions.
- Error handling (sentinel + typed) aligns with the commit path's need for structured error reporting.

**Structure Alignment:**
- Directory tree matches stream-to-package mapping exactly.
- Import rules enforce the NATS-only communication boundary between runtime peers.
- Materializer morph mapping accounts for every existing package — no orphans.
- `substrate/nats/` correctly positioned as shared helper layer consumed by all components.

### Requirements Coverage Validation ✅

**Stream Coverage (125 work items across 7 active streams):**
- All 7 active streams (0-6) have architectural homes in the project structure, boundary contracts, and package import rules.
- All 4 deferred streams (7-10) are explicitly called out as post-MVP with no architectural debt introduced.
- Each stream has a defined first milestone, preventing ambiguous starts.

**Functional Requirements Coverage:**
- Storage/data plane: covered by single Core KV bucket, KV Bucket Taxonomy, key structure convention
- Ledger/operations: covered by `core-operations` 3-lane JetStream, Processor commit path
- Processor/logic: covered by 10-step commit path, Starlark sandbox, DDL validation
- Schema/DDL: covered by `ops.meta.>` lane, DDL meta-vertices, event DDL
- Identity/auth/ReBAC: covered by Capability Lens, Gateway architecture, internal service actor model
- Refractor/lenses: covered by Materializer morph, durable consumer model, adapter framework
- Orchestration: covered by Loom/Weaver separation, event-driven consumption pattern
- Services/commands/tasks: covered by service package, CLI tool, deferred API surface decision
- Crypto-shredding/PII: covered by privacy package, Vault interface, shred handler in Refractor

**Non-Functional Requirements Coverage:**
- Consistency: atomic batches + revision conditions + idempotency tracker ✅
- Security: Capability Lens + JWT + mTLS + token revocation + adversarial test ownership ✅
- Privacy: crypto-shredding + Vault + sensitive markers ✅
- Observability: Health KV + slog + operation-id tracing + auth debug 3-plane trace ✅
- Determinism: Starlark sandbox constraints ✅
- Availability: 4-tier failure classification + DLQ + retry + pause/resume ✅

### Implementation Readiness Validation ✅

**Decision Completeness:**
- All critical and important decisions are made with rationale.
- Deferred decisions are explicitly scoped to specific streams with clear triggers.
- Spike stories are identified as go/no-go gates before implementation proceeds.

**Structure Completeness:**
- Directory tree is complete down to package level with file-level detail for key components.
- Integration points (NATS subjects, KV buckets, config sections) are fully specified.
- Morph mapping provides a clear path from Materializer to Lattice for every package.

**Pattern Completeness:**
- Naming, error handling, logging, testing, and config patterns are all specified with examples.
- Anti-patterns table prevents common mistakes.
- Two canonical consumption patterns + control plane pattern cover all inter-component communication.

### Gap Analysis Results

**Critical Gaps:** None. All implementation-blocking decisions are made.

**Important Gaps:**

1. **Starlark stdlib API surface undefined** — The architecture mentions NanoID is in the stdlib, and scripts return `MutationBatch` + `EventList`, but the Starlark-side API (available functions, types, graph query helpers) isn't specified. This is a Stream 1 implementation concern but is architecturally significant since it defines what scripts can express.

2. **Lens target schema migration strategy** — mentioned ("zero-downtime swap") but not elaborated. When a Lens projection schema changes (e.g., Postgres table), how does Refractor handle the transition? Blue-green projection + cutover? This is a Stream 2 implementation concern but crosses into operational architecture.

3. **`core-events` subject partitioning** — Events use `events.<domain>.<name>` but it's unclear whether Loom and Weaver subscribe to specific subjects or use wildcards. Subscription strategy affects consumer count and ordering guarantees.

**Nice-to-Have Gaps:**

4. **Linter configuration for import boundary enforcement** — mentioned as "verifiable by linter" but no specific tool recommended (e.g., `depguard`, `go-cleanarch`).

5. **Makefile target specification** — mentioned targets (`build`, `test`, `test-integration`, `up`, `down`) but not defined.

### Architecture Completeness Checklist

**✅ Requirements Analysis**
- [x] Project context thoroughly analyzed (125 items, 7 streams, NFRs)
- [x] Scale and complexity assessed (enterprise, 12+ components)
- [x] Technical constraints identified (NATS-only core, Go, single-cell MVP)
- [x] Cross-cutting concerns mapped (CDC vs events, Refractor bottleneck, auth debugging)

**✅ Architectural Decisions**
- [x] Critical decisions documented (single bucket, NanoID, monolith, mono-repo)
- [x] Technology stack fully specified (Go, NATS, Postgres, slog, Starlark)
- [x] Integration patterns defined (CDC-reactive, event-driven, request-reply)
- [x] Performance considerations addressed (read amplification, Context Hinting, JIT Hydration)

**✅ Implementation Patterns**
- [x] Naming conventions established (Go, JSON, NATS subjects)
- [x] Structure patterns defined (directory tree, stream-to-package mapping)
- [x] Communication patterns specified (3 canonical patterns + anti-patterns)
- [x] Process patterns documented (error handling, logging, testing, config)

**✅ Project Structure**
- [x] Complete directory structure defined
- [x] Component boundaries established (import rules)
- [x] Integration points mapped (data flow diagram)
- [x] Materializer morph mapping complete

### Architecture Readiness Assessment

**Overall Status:** READY FOR IMPLEMENTATION

**Confidence Level:** HIGH — based on validation results. All critical decisions are made, patterns are consistent, structure is complete, and the Materializer morph provides a proven foundation.

**Key Strengths:**
- Principled separation of concerns (3 state planes, 2 consumption patterns, sole-writer rule)
- Materializer heritage provides battle-tested operational stack (failure tiers, DLQ, control plane)
- Spike-first approach for NATS unknowns prevents mid-implementation surprises
- Single-developer execution model is architecturally acknowledged with explicit mitigations
- Anti-patterns table is unusually specific — prevents real mistakes, not theoretical ones

**Areas for Future Enhancement:**
- ~~Quantitative targets~~ — **resolved**: locked values in PRD, now reflected in Quantitative Targets section above
- Component-level reference pages (promised in doc header, not yet created)
- Domain semantics / PRD dependency (acknowledged, separate workflow)
- Starlark stdlib API design (Stream 1)
- Lens migration strategy (Stream 2)
- AI agent model tier recommendations per story (during implementation story creation)

### Implementation Handoff

**AI Agent Guidelines:**
- Follow all architectural decisions exactly as documented
- Use implementation patterns consistently across all components
- Respect package import rules — violations indicate an architectural problem, not a code problem
- Refer to this document for all architectural questions
- When in doubt, check the anti-patterns table first

**First Implementation Priority:**
1. Stream 0 spike: NATS atomic batch within single KV bucket (go/no-go)
2. Stream 0 spike: durable consumer count limits
3. Stream 0: NanoID library, envelope schema, dev harness
4. Stream 2: Materializer fork + module rename into mono-repo
5. Stream 1: Processor with trivial Starlark → Core KV write

---

## PRD Alignment Addendum (2026-04-11)

> **Context:** The PRD (completed 2026-04-10) introduced functional requirements, non-functional requirements, and scoping decisions that extend or refine the architecture established above. This addendum documents 8 architectural decisions that align the two documents. Each decision references specific PRD requirements and states the architectural resolution.

### Item 1: Task-Based Authorization (FR56)

**PRD Requirement:** FR56 — "When a task is assigned to a specific user (e.g., 'approve this application'), that assignment itself creates temporary authorization for the assignee to execute the task's required operations, even if they lack standing permission. A manager can delegate tasks to direct reports via reporting-chain links."

**Architectural Decision:** The Capability Lens absorbs task assignments as ephemeral capability entries.

**How it works:**
- When Loom/Weaver assigns a task (creating a task vertex linked to an identity), the assignment link is a regular Core KV link
- The Capability Lens cypher rule already traverses `identity → role → permission` paths; it is extended to also traverse `identity → task-assignment → task → required-permissions` paths
- Task-derived capabilities appear in Capability KV alongside standing capabilities — Processor's auth check (commit path step 3) is unchanged
- When the task completes or is revoked, the link is removed; the next Capability Lens projection cycle removes the ephemeral entry (within CDC lag window)
- Manager delegation: `identity → reports-to → manager → task-assignment` traversal — a second-hop path in the same Lens rule

**Why not a separate auth mechanism:** One Lens, one Capability KV, one auth check. No branching logic in Processor. Task-based auth is a graph topology feature, not a code feature.

### Item 2: Write-Scope per DDL (FR57)

**PRD Requirement:** FR57 — "Each capability definition (DDL) specifies which operations it permits (e.g., a 'lease management' capability permits `CreateLease`, `TerminateLease`, `RenewLease` but not `ProcessPayment`). The commit path validates that the operation being executed is within the capability's permitted command set."

**Architectural Decision:** DDL meta-vertices carry a `permittedCommands` field, validated in Processor commit path step 6.

**How it works:**
- Each DDL meta-vertex (`vtx.meta.ddl.<type>`) includes a `permittedCommands` array listing the operation types that can produce mutations of that type
- Processor commit path step 6 (Validate MutationBatch): for each mutation in the batch, resolve the target entity's DDL and confirm the current operation type appears in `permittedCommands`
- If a Starlark script attempts to produce mutations outside its operation's permitted scope, the entire operation is rejected before any KV writes
- An empty or absent `permittedCommands` field means "unrestricted" — backward compatible with existing DDL

**Cost:** One additional DDL cache lookup per unique entity type in the MutationBatch. At MVP scale (< 100 DDL types), this is negligible. The DDL cache is already hot from step 6 schema validation.

### Item 3: Two-Phase Nudge — External Operation Idempotency (FR58)

**PRD Requirement:** FR58 — "External operations initiated by the platform's orchestration engine are idempotent; a failed or retried external call cannot result in a duplicate charge or duplicated action. The orchestration engine records a visible claim state before executing any external call and does not re-initiate a claimed operation."

**Architectural Decision:** Claims live in Weaver operational KV (`weaver.claims.>`), not Core KV. Resolve mutations go through Processor into Core KV as normal business state.

**Rationale:** Claims are operational bookkeeping — they record "I intend to call Stripe," not "a resident paid rent." This is analogous to Health KV and Weaver dispatch state: internal operational data that serves a single component's coordination needs. Routing claims through Processor's commit path would pollute Core KV with operational debris and add unnecessary round-trip latency to the external call hot path.

**Protocol:**
1. **Claim** — Weaver writes a claim record to `weaver.claims.<claim-id>` with operation details and timestamp. This is a direct KV write (same pattern as Health KV).
2. **Execute** — Weaver performs the external call (e.g., Stripe charge). The claim prevents any other Weaver instance from re-initiating the same operation.
3. **Resolve** — Weaver submits a normal operation through `core-operations` → Processor → Core KV recording the external result as business state (e.g., "payment succeeded, update lease balance"). The resolve mutation carries the `claim-id` as a reference field.

**Audit trail:** The resolve mutation in Core KV references the claim ID. The claim itself is retained in `weaver.claims.>` with a configurable retention window (default: 90 days). Audit queries can join the two: Core KV shows the business outcome; `weaver.claims.>` shows the operational intent. The PRD's "visible claim state" and "audit trail completeness" requirements are satisfied — claims are visible and retained, just not graph-visible.

**Update to KV Bucket Taxonomy:** `weaver.claims.>` added as a distinct bucket (see table above).

### Item 4: AI Actor Authority

**PRD Requirement (NFR — Security):** "AI agents are regular identity vertices subject to the same Capability Lens authorization as human actors; there is no privileged 'AI actor' class or bypass."

**Architectural Decision:** AI agents are identity vertices with naming convention `identity.ai.<purpose>.<id>`.

**How it works:**
- AI agents get identity vertices like any other actor: `vtx.identity.ai.onboarding-assistant.001`
- Their capabilities are defined by the same graph topology: `identity → role → permission` paths in Core KV, projected by the Capability Lens
- Processor commit path step 3 (auth check) treats AI actor operations identically to human actor operations — the `Lattice-Actor` header carries the AI identity, Capability KV is consulted, same code path
- AI agents discover their own capabilities via graph traversal (FR53: "AI agent can traverse the graph from its own identity vertex to discover available commands, required schemas, and system state")
- Scope limiting: AI agents' capability sets are intentionally narrower than human operators'. An onboarding assistant can submit `CreateIdentity` but not `TerminateLease`

**Distinction from internal service actors:** Internal service actors (Loom, Weaver, admin tools) operate at root-equivalent capability within the trust boundary. AI agents are NOT internal service actors — they operate within the Capability Lens like human users. The `ai.*` naming convention makes this unambiguous.

### Item 5: Encrypted Aspect Projection (Scale-Aware)

**PRD Requirement (NFR — Security):** PII aspects encrypted with per-identity keys; key material in external KMS/HSM, never in Core KV.

**Architectural Decision:** Key-caching strategy in Refractor's Secure Lens adapter, not shadow aspects. The caching approach scales with deployment needs.

**Why not shadow aspects:** Shadow aspects (writing decrypted copies alongside encrypted originals) create a second source of truth. If the shadow is stale, Refractor projects stale decrypted data. If the shadow write fails, the encrypted aspect exists without its shadow. This is a consistency hazard that adds complexity without architectural benefit.

**Scale-aware approach:**

At **Phase 1 / MVP scale** (~500 members, ~50 concurrent sessions, up to 100K keys), Refractor can call Vault for each sensitive aspect encountered during CDC processing. The volume is low enough that Vault latency is absorbed within the < 500ms p99 CDC lag budget. A simple in-memory LRU cache with short TTL (e.g., 60s) covers repeated accesses to the same identity's key within a projection cycle.

At **Phase 2+ scale** (higher throughput, more concurrent sessions, more sensitive aspects), the caching strategy graduates:
- **TTL-based key cache** in Refractor — keys cached per-identity with configurable TTL. Cache hit avoids Vault round-trip entirely. TTL controls the staleness window after a key rotation or shred event.
- **Cache invalidation via `KeyShredded` event** — Refractor already consumes shred events; on receipt, the corresponding key is evicted from cache immediately (TTL is a fallback, not the primary invalidation mechanism)
- **Vault-down degradation** — if Vault is unreachable, Secure Lens projections for sensitive aspects fall behind (stale). Non-sensitive projections continue normally. Health KV reports Secure Lens degraded state. This is a conscious tradeoff: stale sensitive projections are preferable to blocking all projection.

At **large scale** (if Vault round-trip latency becomes a measurable bottleneck even with caching), options include: Vault response batching (fetch N keys per request), pre-warming the cache on Refractor startup via bulk key listing, or a dedicated decryption sidecar that handles Vault communication asynchronously. These are implementation-time decisions informed by actual Vault latency measurements, not architectural commitments made now.

**The principle:** start simple (direct Vault calls), add caching when measurements justify it, and never create a second source of truth.

### Item 6: Aspect-Level Sensitivity Boundary

**PRD Requirement (NFR — Security / Privacy):** "Crypto-shredding operates at the aspect level"; sensitive aspects anchored to identity vertices.

**Architectural Decision:** Entire aspect marked sensitive via DDL `sensitive: true` flag (not property-level). Sensitive aspects must attach to identity vertices. Enforced by MutationBatch validator.

**How it works:**
- DDL meta-vertex for an aspect type includes `"sensitive": true` at the aspect-type level
- Processor commit path step 6 (Validate MutationBatch): if a mutation creates or updates a sensitive aspect, the validator checks that the target vertex is an identity vertex (or linked to one via a defined anchoring pattern)
- Sensitive aspects are encrypted by Refractor's Secure Lens adapter using the anchoring identity's key
- Crypto-shredding destroys the identity's key → all sensitive aspects for that identity become irrecoverable
- Non-sensitive aspects on the same vertex are unaffected

**Why aspect-level, not property-level:** A single aspect is the atomic unit of encryption and shredding. Property-level sensitivity would require partial encryption within a JSON value, which complicates every read/write path and makes crypto-shredding non-atomic. If some properties of an aspect are sensitive and others aren't, they should be separate aspects.

### Item 7: Quantitative Targets — Architecture Section Updated

The Quantitative Targets section above has been updated from TBD to locked values per the PRD. Key design implications:

- **Batch size ceiling spike (Stream 0):** must validate that NATS atomic batch supports the mutation sizes implied by 10–100 ops/sec at up to 100K keys
- **Starlark spike threshold:** < 100ms p99 is the go/no-go gate; if the spike exceeds this, Processor architecture needs revisiting before Stream 1 proceeds
- **Cache TTL guidance:** CDC lag budget of < 500ms p99 sets the upper bound for Capability KV staleness and DDL cache refresh intervals
- **Consumer parallelism:** ~50 concurrent sessions generating 10–100 ops/sec — single consumer per lane is likely sufficient at MVP; parallelism is a Phase 2 concern

### Item 8: P6 Update — Single-Cell Validation

Principle P6 referenced "Quantitative Targets TBD" — now resolved. The MVP data volume (up to 100K keys, 10–100 ops/sec, ~500 members) is well within NATS KV single-bucket scalability limits based on published NATS benchmarks. The Stream 0 spike will empirically validate this assertion before committing to the single-cell architecture for Phase 1.

---

### PRD Alignment Summary

| Item | PRD Source | Architectural Resolution | Affects |
|------|-----------|--------------------------|---------|
| 1 | FR56 | Capability Lens absorbs task assignments as ephemeral entries | Capability Lens cypher rules, Capability KV |
| 2 | FR57 | `permittedCommands` field in DDL, validated at commit path step 6 | DDL schema, Processor validation |
| 3 | FR58 | Claims in `weaver.claims.>` operational KV; resolves via Processor | Weaver, KV Bucket Taxonomy |
| 4 | NFR Security | AI agents as identity vertices, `identity:ai.*` naming | Identity model, Capability Lens |
| 5 | NFR Security | Scale-aware key-caching in Refractor; no shadow aspects | Refractor Secure Lens adapter, Vault integration |
| 6 | NFR Privacy | Aspect-level `sensitive: true` in DDL; identity-anchored; MutationBatch enforced | DDL schema, Processor validation, Refractor |
| 7 | NFR Performance | Quantitative Targets section updated from TBD to locked values | Stream 0 spikes, cache TTLs, consumer sizing |
| 8 | P6 | Single-cell validated against locked targets | Architecture principle P6 |
