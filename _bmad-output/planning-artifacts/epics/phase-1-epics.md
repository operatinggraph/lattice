# Lattice Epics — Phase 1 (MVP / Platform Proof) · SHIPPED

> Shard of [epics/index.md](./index.md) — detailed stories (BDD acceptance criteria + source hints) for **Epics 1–6**, all shipped. For the requirements inventory, epic list, and cross-phase sprint reference, see the [index](./index.md). Phase 2 stories live in [phase-2-epics.md](./phase-2-epics.md).

---

## Epic 1: Substrate & Trustworthy Write Path

**Goal:** Developers can submit operations to a running Lattice instance with guaranteed atomic commit, idempotency, schema enforcement, and deterministic Starlark execution. This is the foundational contract everything else builds on.

**FRs covered:** FR8, FR9, FR10, FR11, FR12, FR13, FR14, FR57
**Phase 1 gates delivered:** Starlark spike (gate 1), Attempted bypass test suite (gate 2)

> **Model tier guidance:** Stories 1.1–1.2 are spike/research stories (Sonnet sufficient — output is a report + minimal PoC code). Stories 1.3–1.8 are core implementation (Opus — high complexity, multi-file, architectural precision required). Stories 1.9–1.10 are targeted implementation and test authoring (Sonnet — well-bounded scope against established contracts).

---

### Story 1.1: NATS Atomic Batch Spike

As a platform engineer,
I want a validated spike report on NATS JetStream atomic batch behavior at the operation patterns Lattice requires,
So that the Processor commit path architecture is grounded in verified NATS semantics before any implementation begins.

**Recommended model tier:** Sonnet
**Estimated token budget:** ~52K (input + output)

**Acceptance Criteria:**

**Given** a local NATS 2.12+ server with JetStream enabled (2.14 recommended — 2.12 is the minimum for atomic batch support; 2.14 is preferred for stability/feature margin)
**When** the spike harness executes a series of targeted tests against the four behavioral questions
**Then** a written report is produced covering all four areas:

1. **TTL-in-batch**: A KV put with a per-key TTL issued inside a `PublishBatch` commits successfully; the entry expires independently of other batch entries; behavior is confirmed when mixed TTL and non-TTL entries appear in the same batch. (Note: per-key TTL is a NATS 2.11+ feature; this test verifies it composes correctly with the 2.12+ atomic batch feature.)

2. **Revision condition atomicity**: A `PublishBatch` containing a compare-and-swap entry (revision condition) commits atomically — the entire batch is rejected if the revision check fails; no partial commit is observable from a concurrent reader.

3. **Multi-subject batches within a single KV bucket**: A single `PublishBatch` containing messages targeting multiple distinct subjects within ONE KV bucket (Core KV) — e.g., a vertex create at `vtx.identity.<id>`, an aspect write at `vtx.identity.<id>.email`, a link create at `lnk.identity.<id>.assignedRole.role.<roleId>`, AND the op-tracker write at `vtx.op.<requestId>` — commits or fails as a unit; behavior under concurrent conflicting writes to one of those keys from a second writer is documented. **Atomic batches are constrained to a single stream (= single KV bucket); they DO NOT span multiple buckets (e.g., Core KV + Health KV).** This test validates Processor commit-step-8 architecture which writes only to Core KV; Health KV and Capability KV are populated by separate writers (components direct-write to Health, Refractor projects to Capability).

4. **TTL marker delivery**: After a per-key TTL expires, the KV watcher receives a tombstone/expiry marker on the subject; the marker is distinct from a normal delete; the marker's sequence number is ordered correctly in the stream.

**And** the report includes a clear **Go/No-Go recommendation** for the current atomic batch architecture with rationale.
**And** all spike code is committed to `internal/spike/nats-batch/` with a README summarizing findings.
**And** if any finding contradicts the architecture contracts in `data-contracts.md`, the specific contract section and recommended amendment are called out explicitly.

---

### Story 1.2: Starlark Execution Spike

As a platform engineer,
I want a validated spike report on Starlark execution in Go — covering sandbox correctness, ergonomic API design, and order-of-magnitude local performance — so that the Processor's script execution layer is designed on verified foundations before implementation begins.

**Recommended model tier:** Sonnet
**Estimated token budget:** ~65K (input + output)

**Acceptance Criteria:**

**Given** a Go module using `go.starlark.net/starlark` (or the best available Go Starlark library at spike time)
**When** the spike harness executes a series of targeted tests
**Then** a written report is produced covering all three areas:

1. **Sandbox correctness**: A Starlark script that attempts each of the four forbidden operations — external HTTP call, filesystem read, `os.Getenv`, and a non-deterministic call (e.g., `time.Now` equivalent) — is rejected at parse or execution time with a clear error; no forbidden operation succeeds; sandbox configuration required to achieve this is documented.

2. **API ergonomics**: A prototype `ScriptContext` struct is designed and implemented showing how Lattice passes hydrated vertex data into a Starlark script (input shape), how the script emits a `MutationBatch` proposal (output shape), and how Processor validates the return value; the API is minimal — the spike is not production implementation, but the interface must be usable as-is by Story 1.6.

3. **Order-of-magnitude local performance**: The harness executes 1,000 sequential Starlark invocations against a realistic script (one vertex hydration, one conditional branch, one mutation proposal) on a development machine and records mean and p95 execution time; the purpose is to confirm the `< 100ms p99` target is achievable in principle — not to validate absolute production numbers (Mac performance is not representative of cloud environments).

**And** the report includes a **Go/No-Go recommendation** on the Starlark approach with rationale.
**And** if p95 local execution exceeds 100ms, the report includes a proposed architecture adjustment for commit path step 5 (e.g., pre-compilation, caching, pooling).
**And** all spike code is committed to `internal/spike/starlark/` with a README summarizing findings.
**And** if any finding contradicts the architecture contracts in `data-contracts.md`, the specific contract section and recommended amendment are called out explicitly.

---

### Story 1.3: Dev Harness with Primordial Bootstrap

As a developer,
I want a local development environment that starts from a single command and arrives at a verified bootstrap state, so that all subsequent stories have a stable, reproducible substrate to build against.

**Recommended model tier:** Sonnet
**Estimated token budget:** ~95K (input + output)

**Acceptance Criteria:**

**Given** a developer has Docker and Go installed and has run `git clone`
**When** they run `make up`
**Then** a NATS 2.12+ server (2.14 recommended) starts with JetStream enabled, all required KV buckets created (Core KV, Health KV, Capability KV, Weaver Claims KV, Weaver State KV, Token Revocation KV — note: idempotency tracker entries live in Core KV at `vtx.op.<requestId>` per Contract #4, not in a separate bucket), and all required JetStream streams created (`core-operations`, `ops.meta.>`, `ops.*`).
**And** the bootstrap sequence writes the primordial identity vertex (`vtx.identity.<bootstrapId>`), the platform actor vertex (`vtx.identity.platform`), the root DDL meta-vertex (`vtx.meta.root`), the **primary Capability Lens definition** (`vtx.meta.lens.capability` — the per-actor projection per Contract #6 §6.2), and the **secondary capability role-coverage index Lens** (`vtx.meta.lens.capabilityRoleIndex` — projects `cap.role-by-operation.<operationType>` entries per Contract #6 §6.1) directly to Core KV with correct document envelopes per `data-contracts.md` Contract #1. Both Lens definitions include their cypher rule body and target adapter declaration (`nats-kv` → `capability` bucket).
**And** `make up` blocks until a readiness gate confirms Refractor (stub) has observed the bootstrap writes — the gate is satisfied when Health KV shows all bootstrap keys present.
**And** `make down` tears down all containers cleanly.
**And** warm restart (`make down && make up`) completes in under 30 seconds (NFR-P7).
**And** a `make verify-bootstrap` target runs assertions against Core KV confirming all primordial keys exist with correct envelopes, exits 0 on success and non-zero with a diff on failure.

---

### Story 1.4: `internal/substrate` Package

As a platform engineer,
I want a shared Go package `internal/substrate` that provides NATS connectivity, NanoID generation, KV helpers, and document envelope construction, so that all Lattice components use consistent, contract-compliant primitives without duplicating low-level NATS code.

**Recommended model tier:** Opus
**Estimated token budget:** ~110K (input + output)

**Acceptance Criteria:**

**Given** the package is imported by any Lattice component
**When** a caller uses `substrate.NewNanoID()`
**Then** the returned ID is exactly 20 characters drawn from the 58-character custom alphabet (A-Za-z0-9 excluding I, l, O, 0); the function is unit-tested with 10,000 generated IDs verifying length and alphabet compliance; no generated ID contains a forbidden character.

**Given** a caller constructs a vertex key using `substrate.VertexKey(vertexType, id)`
**When** the function is called
**Then** the returned string matches `vtx.<type>.<id>` exactly; analogous helpers `substrate.AspectKey(vtxKey, localName)` and `substrate.LinkKey(type1, id1, linkName, type2, id2)` produce correct 4-segment and 6-segment keys respectively; all helpers are unit-tested against the key patterns in `data-contracts.md` Contract #1.

**Given** a caller uses `substrate.NewDocumentEnvelope(class, actor, operationType)`
**When** the function is called
**Then** the returned struct contains all mandatory envelope fields — `class`, `isDeleted: false`, `createdAt`, `createdBy`, `createdByOp`, `lastModifiedAt`, `lastModifiedBy`, `lastModifiedByOp` — with correct zero values; the `data` field is nil until set by the caller; the struct serializes to JSON without omitting required fields.

**Given** a caller uses `substrate.KVGet(ctx, bucket, key)` and `substrate.KVPut(ctx, bucket, key, value)`
**When** the NATS connection is healthy
**Then** operations complete within configured timeout; when the key does not exist, `KVGet` returns a typed `ErrKeyNotFound`; when revision conflict occurs on a conditional put, the error is typed `ErrRevisionConflict`.

**And** the package includes a `substrate_test.go` integration test that starts an embedded NATS server (or uses the dev harness from Story 1.3) and exercises all helpers end-to-end.
**And** zero external dependencies beyond `go.starlark.net` and the official `nats.go` client are introduced.

---

### Story 1.5: Processor — Consume, Dedup & Auth Stub (Steps 1–3)

As a platform engineer,
I want the first three steps of the Processor commit path — JetStream consumption, idempotency dedup, and auth stub — implemented and integration-tested, so that the Processor can receive, deduplicate, and stub-authorize operations before the full execution pipeline is wired.

**Recommended model tier:** Opus
**Estimated token budget:** ~115K (input + output)

**Acceptance Criteria:**

**Given** a Processor instance connected to the running NATS dev harness (Story 1.3)
**When** a valid operation envelope is published to `core-operations`
**Then** the Processor consumes it (step 1), checks the Idempotency Tracker KV for the `requestId` (step 2), and proceeds to auth stub (step 3) if the ID is not found.

**Given** the same operation envelope is published a second time with the same `requestId`
**When** the Processor processes the duplicate
**Then** it short-circuits at step 2, emits a `DuplicateDetected` log entry with the `requestId`, does not proceed to auth or execution, and acks the message so it is not redelivered.

**Given** an operation envelope with a `requestId` not yet in the tracker
**When** the auth stub (step 3) is evaluated
**Then** the stub always returns `authorized: true` for any valid envelope; this is explicitly a stub — real Capability KV auth is implemented in Epic 3; the stub is feature-flaggable so it can be replaced without changing the step 3 interface.

**And** if the operation envelope fails JSON unmarshaling or is missing required fields (per `data-contracts.md` Contract #2), the Processor nacks with `term: true` (no redelivery), logs the malformed envelope, and emits a `MalformedOperation` health signal to Health KV.
**And** the dedup check and tracker write use a single NATS atomic batch (per spike findings from Story 1.1) to ensure the tracker entry is written before ack.
**And** integration tests cover: first delivery (accepted), duplicate (short-circuited), malformed envelope (terminated), and tracker write failure (crash-safe retry).

---

### Story 1.6: Processor — Starlark Sandbox & JIT Hydration (Steps 4–5)

As a platform engineer,
I want steps 4 and 5 of the Processor commit path — JIT vertex hydration from Core KV and Starlark script execution in a validated sandbox — implemented and integration-tested, so that business rules execute deterministically against live graph state.

**Recommended model tier:** Opus
**Estimated token budget:** ~130K (input + output)

**Acceptance Criteria:**

**Given** an authorized operation envelope has passed steps 1–3
**When** step 4 (JIT hydration) executes
**Then** the Processor reads the vertices referenced in the operation's `contextHint` from Core KV, materializes them into the hydration context, and makes them available to the Starlark script; if a referenced vertex key does not exist in Core KV, the step returns a typed `HydrationError` and the commit path terminates with a rejection response to the caller.

**Given** the hydration context is populated
**When** step 5 (Starlark execution) runs the DDL-associated script for the operation type
**Then** the script executes within the sandbox validated in Story 1.2 (no I/O, no network, no secrets, no non-deterministic calls); the script receives hydrated vertex data and the operation payload; the script returns a proposed `MutationBatch`; if the script raises a Starlark error, execution terminates with a `ScriptError` and no MutationBatch is produced.

**Given** a Starlark script attempts any forbidden operation (external HTTP, filesystem read, `os.Getenv`, non-deterministic call)
**When** the script executes
**Then** the sandbox rejects the forbidden operation at runtime; the script error is caught; the commit path terminates with a `SandboxViolation` error; no partial mutation reaches step 6.

**And** the `ScriptContext` API matches the interface prototyped in Story 1.2's spike.
**And** script lookup uses the `class` field from the operation's target vertex envelope to find the DDL meta-vertex and its associated script body.
**And** integration tests cover: clean execution (MutationBatch returned), hydration miss (HydrationError), script error (ScriptError), and all four sandbox violation vectors.

---

### Story 1.7: Processor — DDL Validation & Atomic Batch (Steps 6–8)

As a platform engineer,
I want steps 6 through 8 of the Processor commit path — MutationBatch validation, EventList construction, and NATS atomic batch commit — implemented and integration-tested, so that every committed operation is validated against DDL constraints and durably written atomically.

**Recommended model tier:** Opus
**Estimated token budget:** ~145K (input + output)

**Acceptance Criteria:**

**Given** a Starlark script has produced a proposed `MutationBatch`
**When** step 6 (MutationBatch validation) runs
**Then** each mutation in the batch is validated against its DDL meta-vertex: permitted op types from `permittedCommands` are enforced; sensitive aspect write-scope is enforced (sensitive aspects may only attach to identity vertices); the key pattern of each mutation target matches the key pattern in `data-contracts.md` Contract #1; any DDL violation terminates the commit path with a `DDLViolation` error carrying the specific violated constraint.

**Given** the MutationBatch passes step 6 validation
**When** step 7 (EventList construction) runs
**Then** an ordered EventList is produced where each event has: `eventId` (NanoID), `requestId` (from original operation), `eventType`, `targetKey`, `payload`, and `timestamp`; the EventList contains exactly the events corresponding to the validated mutations.

**Given** a validated MutationBatch and EventList are ready
**When** step 8 (atomic batch commit) executes
**Then** a single NATS `PublishBatch` is submitted containing: all Core KV mutations, the Idempotency Tracker entry with 24h per-key TTL, and the `vtx.op.<requestId>` tracker key; the batch commits atomically — either all succeed or none succeed; if any revision condition in the batch fails (concurrent write conflict), the entire batch is rejected and the commit path returns a `ConflictError` with the conflicting key.

**And** on successful batch commit, the `vtx.op.<requestId>` tracker key is written with a `committed: true` value so callers polling for RYOW confirmation receive a positive signal.
**And** the DDL mutations lane (`ops.meta.>`) triggers synchronous Processor cache invalidation so subsequent operations see the new DDL immediately.
**And** integration tests cover: clean commit (all keys written atomically), DDL violation (rejected at step 6), revision conflict (batch rejected, ConflictError returned), and mixed TTL + non-TTL batch (per spike findings).

---

### Story 1.8: Processor — Event Publication & Fault Injection (Steps 9–10)

As a platform engineer,
I want steps 9 and 10 of the Processor commit path — JetStream event publication and JetStream ack — implemented with a fault injection harness, so that the complete 10-step commit path is crash-recoverable and NFR-R1 is validated.

**Recommended model tier:** Opus
**Estimated token budget:** ~145K (input + output)

**Acceptance Criteria:**

**Given** the atomic batch commit (step 8) has succeeded
**When** step 9 (event publication) runs
**Then** each event in the EventList is published to the appropriate JetStream subject (`core-operations.events.>` or subject per event type); publication is ordered (events published in EventList sequence); if publication fails for any event, the step retries up to the configured maximum before surfacing a `PublicationError`; partial event publication (some events published, some not) is not possible — the step uses a batch publish.

**Given** event publication succeeds
**When** step 10 (JetStream ack) runs
**Then** the original operation message is acked to JetStream; the operation is removed from the durable consumer's pending set; no redelivery occurs.

**Given** a `FailAfterN` fault injector wrapper is applied to the JetStream client
**When** fault injection is triggered at each of the 10 commit path steps (one test per step)
**Then** after the injected failure, the Processor restarts (simulated via restart of the consumer loop), reprocesses the operation from JetStream (redelivery), and produces the same final state as a clean run; no partial state is visible in Core KV that would not exist after a clean run; the idempotency tracker prevents double-application of any already-committed step.

**And** the fault injection harness is implemented as `internal/testutil/faultinjector.go` with a `FailAfterN(n int) JetStreamPublisher` constructor.
**And** the complete 10-step happy path is covered by a single integration test that publishes one operation, traces it through all 10 steps, and asserts final Core KV state, Idempotency Tracker entry, and emitted events.
**And** NFR-R1 is marked verified in the test suite output upon successful fault injection at all 10 steps.

---

### Story 1.9: Write-Scope Enforcement per DDL (FR57)

As a platform engineer,
I want the Processor's step 6 DDL validation to enforce `permittedCommands` write-scope constraints declared in DDL meta-vertices, so that no operation can mutate a data type using an operation type its DDL has not explicitly permitted.

**Recommended model tier:** Sonnet
**Estimated token budget:** ~85K (input + output)

**Acceptance Criteria:**

**Given** a DDL meta-vertex for `identity` declares `permittedCommands: ["create", "update"]`
**When** an operation with `operationType: "tombstone"` targets an identity vertex
**Then** the Processor's step 6 validator rejects the MutationBatch with a `DDLViolation` error; the error message names the violated constraint (`permittedCommands`), the attempted operation type (`tombstone`), and the DDL meta-vertex key; the operation does not reach the atomic batch step.

**Given** the same DDL declares `permittedCommands: ["create", "update"]`
**When** an operation with `operationType: "create"` targets an identity vertex
**Then** the Processor's step 6 validator accepts the mutation and allows it to proceed to the atomic batch step.

**Given** a DDL meta-vertex does not declare a `permittedCommands` field (permissive-by-default per Contract #1)
**When** any operation type targets a vertex of that type
**Then** the step 6 validator accepts the mutation without write-scope enforcement (permissive default — undeclared = unrestricted).

**And** a dedicated `write_scope_test.go` integration test file covers: permitted operation (accepted), forbidden operation (DDLViolation), and missing declaration (permissive default accepted).
**And** the sensitive aspect write-scope constraint (sensitive aspects may only attach to identity vertices) is covered in the same test file with a test asserting that a sensitive aspect write to a non-identity vertex returns `DDLViolation`.

---

### Story 1.10: Attempted Bypass Test Suite (Gate 2)

As a platform engineer,
I want a dedicated adversarial test suite that proves all four architectural bypass categories are impossible, so that the security perimeter of the write path is validated before Epic 3 builds authorization on top of it.

**Recommended model tier:** Sonnet
**Estimated token budget:** ~105K (input + output)

**Acceptance Criteria:**

**Given** the complete 10-step Processor commit path from Stories 1.3–1.9 is running
**When** the adversarial test suite executes
**Then** all four bypass categories are tested and proven impossible:

1. **Direct KV write bypass**: A test client attempts to write directly to Core KV (bypassing `core-operations` stream and Processor); the write is rejected at the NATS authorization level OR the write succeeds but produces no EventList entry, making it undetectable by downstream Refractor consumers — document which enforcement layer catches this and mark it explicitly.

2. **Stream publish outside `ops.*` namespace**: A test client publishes to a subject outside the `ops.*` namespace (e.g., `core-operations` directly without going through the Processor consumer); the Processor's durable consumer only receives messages published via the correct subject hierarchy; messages on unauthorized subjects are not consumed.

3. **Starlark I/O escape**: A malicious Starlark script attempts each of the four forbidden operations (external HTTP, filesystem read, `os.Getenv`, non-deterministic call); each attempt is caught by the sandbox; the test asserts that the `SandboxViolation` error is returned and no mutation is written to Core KV.

4. **DDL schema violation**: An operation is crafted that would write a vertex or aspect that violates the DDL schema (wrong operation type for `permittedCommands`, sensitive aspect on non-identity vertex); the Processor's step 6 validator catches and rejects the mutation; no partial state reaches Core KV.

**And** the test suite produces a human-readable summary report: one row per bypass category, result (BLOCKED / PARTIAL / ESCAPED), and the enforcement layer that caught it.
**And** all four categories must report BLOCKED for Phase 1 Gate 2 to be marked passed.
**And** the test suite is runnable standalone via `make test-bypass` and exits 0 only when all four categories are BLOCKED.
**And** Gate 2 status is written to Health KV under `health.gates.phase1.gate2` as `passed: true` with timestamp upon successful test run.

---

**Epic 1 Summary:** 10 stories | FRs covered: FR8, FR9, FR10, FR11, FR12, FR13, FR14, FR57 | Phase 1 Gate 1 (Starlark spike) and Gate 2 (bypass test suite) delivered.

---

## Epic 2: Live Lens Projections (Materializer Morph)

**Goal:** Lift-and-shift the Materializer codebase into Lattice as Refractor. Adjust existing capabilities (target adapter interface, NATS KV target, Postgres target, control service, health/lag reporting, rule-definition sourcing) to Lattice's data contracts. Preserve everything else as-is. Close with a written functional gap analysis that grounds Epic 3 prerequisites and Phase 2+ backlog in measured reality.

**FRs covered:** FR15, FR16, FR20
**Phase 1 gates delivered:** Capability Lens adversarial test suite (gate 3, joint with Epic 3 — Epic 2 delivers the projection substrate that gate 3 runs against)

**Scope principle — preservation over reinvention:** Materializer already ships a target adapter interface (with NATS KV and Postgres adapter implementations), a control service, per-Lens health and lag reporting, and rule-definition stream consumption. Epic 2 does NOT redesign these. Epic 2 adjusts them to Lattice's contracts and leaves them otherwise intact.

**Reference inputs (research, not binding):**
- `_bmad-output/planning-artifacts/materializer-morph-plan.md` — pre-morph analysis of expected deltas

**Source of truth (binding):**
- `_bmad-output/planning-artifacts/data-contracts.md` — key patterns, document envelopes, KV bucket conventions
- `_bmad-output/planning-artifacts/lattice-architecture.md` — architectural decisions and constraints

**Conflict resolution rule:** Where the morph plan and `data-contracts.md` disagree, `data-contracts.md` wins. Where reality during execution differs from the morph plan, record the deviation in `MORPH-DEVIATIONS.md` for the gap analysis story to consume.

> **Model tier guidance:** Both stories are Opus — high architectural precision required, deep cross-codebase reasoning, and the gap analysis is a synthesis deliverable that determines downstream epic prerequisites.

**Important terminology distinction (avoid conflation):**
- **Adjacency KV** = Refractor's own **internal operational KV** — a private graph-adjacency index used for query support. Internal plumbing. Not a projection target.
- **`nats-kv` target adapter** = projection sink that writes a Lens's output to a NATS KV bucket. Used by the Capability Lens to project into Capability KV (Epic 3). External downstream sink.

These are unrelated subsystems. Both are preserved through the morph but at different layers.

---

### Story 2.1: Materializer → Refractor Morph (Lift-and-Shift)

As a platform engineer,
I want the Materializer codebase lifted into Lattice as Refractor — adjusted only as needed to conform to Lattice's data contracts and key patterns, preserving all existing Materializer capabilities — so that Lattice has a known-working projection engine running against Core KV before any new capabilities are layered on in Epic 3.

**Recommended model tier:** Opus
**Estimated token budget:** ~145K (input + output)

**Acceptance Criteria:**

**Given** the Materializer Go module exists at its current path
**When** the morph is complete
**Then** Go packages previously named `materializer/*` are renamed `refractor/*`; the binary is `refractor` (deviation from original AC `lattice-refractor` per Story 2.1 Deviation 1); subject namespaces, durable consumer names, JetStream stream names, and KV bucket defaults retain `materializer.*` tokens **as a Phase 1 carry** — full token eviction is scoped to Story 2.4a (2026-05-19 course correction); the package layout follows the morph plan's recommended structure except where `data-contracts.md` mandates otherwise; existing Materializer unit and integration tests pass against the morphed codebase with only import-path updates.

**Given** Refractor's CDC consumption layer
**When** the morph adjusts it to Lattice
**Then** Refractor sources CDC events via named durable JetStream consumers on the `KV_<core-bucket>` backing stream (one durable consumer per active Lens definition); consumer sequence positions persist across restarts (NFR-R2); CDC events are classified by **segment count** per Contract #1 §1.5 — 3-segment `vtx.<type>.<id>` → vertex mutation, 4-segment `vtx.<type>.<id>.<localName>` → aspect mutation, 6-segment `lnk.<typeA>.<idA>.<relation>.<typeB>.<idB>` → link mutation — before being routed to Lens handlers. (Story 2.1 deviated to KV Watch for lens-source CDC; Story 2.4b realigns to the durable-consumer pattern this AC originally specified.)

**Given** Materializer's existing rule-definition stream consumption
**When** the morph adjusts it to Lattice
**Then** Lens definitions are sourced from `vtx.meta.lens.*` writes in Core KV (per Contract #1's meta-vertex pattern) rather than whatever dedicated stream Materializer uses today; activation, deactivation, and modification of a Lens occur through the standard Processor write path (FR15, FR16); changes propagate to Refractor through the same Core KV CDC consumer used for all other meta-vertex changes; activation, deactivation, and modification all complete within the CDC-to-projection lag window without Refractor restart (NFR-E1, NFR-E2).

**Given** Materializer's existing target adapter interface, NATS KV adapter, and Postgres adapter
**When** the morph adjusts them to Lattice
**Then** the adapter interface is preserved; both adapters continue to function; vertex/aspect/link mutations are routed to the adapter declared in the Lens definition's target field; tombstone mutations preserve soft-delete semantics (`isDeleted: true` per Contract #1 envelope) rather than physically deleting rows; document envelope fields written by Materializer's adapters are reshaped to match Contract #1's envelope wherever they appear in projection output.

**Given** Materializer's existing control service
**When** the morph adjusts it to Lattice
**Then** the control service is preserved; control endpoints (Lens lifecycle, status, manual replay triggers) continue to function; auth on the control service is wired to read Capability KV (stubbed for Epic 2 — full integration lands in Epic 3 when Capability KV is populated); control service health is emitted to Health KV under `health.refractor.<instance>.control` (NFR-O1, NFR-O2).

**Given** Materializer's existing health and lag reporting (per-Lens metrics, target adapter latency, error counters, failure tier classification)
**When** the morph adjusts it to Lattice
**Then** all health emissions write to Lattice's Health KV under `health.refractor.<instance>.*` and `health.refractor.<instance>.lens.<lensId>.*` (NFR-O1, NFR-O3); per-Lens lag (`stream_last_seq − consumer_acked_seq` and estimated milliseconds) is emitted at minimum every 10 seconds; the failure tier classification is re-audited for Lattice safety requirements — crypto-shred failures reclassified as privacy-critical (operator alert, no silent retry); Capability Lens failures reclassified as security-critical (operator alert, halt affected Lens projection until resolved); tier definitions and handling behaviors documented in `docs/refractor-failure-tiers.md`.

**Given** Materializer's Adjacency KV (internal operational graph index)
**When** the morph adjusts it to Lattice
**Then** the Adjacency KV continues to function as Refractor's internal operational store; its key shapes are reshaped if required to match Lattice's data contracts where they overlap, but it remains private to Refractor (not externally addressable, not a Lens target); the distinction between Adjacency KV (internal) and `nats-kv` target adapter (external Lens sink) is preserved in code organization and documentation.

**Given** any deviation from the morph plan made during execution
**When** the deviation is identified
**Then** it is recorded in `_bmad-output/planning-artifacts/MORPH-DEVIATIONS.md` with: the morph plan section, the actual decision made, the contract section (or other constraint) that drove the deviation, and any downstream implication; this artifact is the primary input to Story 2.2's gap analysis.

**And** `make up` starts Refractor as a service alongside the NATS server and (now) Postgres container (Postgres provisioning required by the existing Postgres target adapter — confirm during morph or surface as a gap).
**And** Refractor emits a Health KV signal under `health.refractor.<instance>` within 10 seconds of startup (NFR-O1).
**And** a single end-to-end integration test exists: activate a trivial Lens (one entity type → one aspect projection → Postgres target) via the standard write path, write a vertex through Processor, assert projection appears in Postgres within NFR-P3 (< 500ms p99 over a 100-mutation run).

---

### Story 2.2: Functional Gap Analysis (Closing Story)

As a platform architect,
I want a written gap analysis comparing the morphed Refractor's actual capabilities to Lattice's full Phase 1 and Phase 2 requirements, so that Epic 3 prerequisites and the Phase 2+ backlog are grounded in measured reality rather than pre-morph predictions.

**Recommended model tier:** Opus
**Estimated token budget:** ~130K (input + output)

**Acceptance Criteria:**

**Given** Story 2.1 is complete and the morphed Refractor is running against the dev harness
**When** the gap analysis is performed
**Then** the deliverable `_bmad-output/planning-artifacts/refractor-gap-analysis.md` is produced and contains:

1. **Capabilities as-shipped** — precise inventory of what morphed Refractor can do today: rule/expression language supported by the current parser, target adapters implemented (NATS KV, Postgres), Lens lifecycle operations supported (activate, deactivate, modify with re-materialization policy), control surface endpoints, observability surface (Health KV keys emitted, metrics types, alert flags), failure tier handling behaviors.

2. **Required for Epic 3 — Authorization & Security Perimeter** — each item marked GAP / PARTIAL / READY against current state:
   - openCypher parser integration (expected GAP — current parser is the Materializer custom expression language; cannot express Capability Lens cypher rule)
   - Capability Lens cypher rule semantics (traversal patterns: role-based, task-derived, manager-via-reporting-chain, service-access topology)
   - Capability KV target adapter requirements — does the existing `nats-kv` adapter cover the Capability KV three-section model (platformPermissions, serviceAccess, ephemeralGrants per Contract #6), or does it need extension?
   - Read-after-write coherence requirements for Capability KV (Processor reads it at step 3 — does the existing projection commit pattern guarantee sub-500ms lag at expected load?)

3. **Required for Phase 2+** — surfaced during the morph or anticipated based on architectural gaps:
   - Historical state query support (FR51, deferred from MVP)
   - Read-path authorization for Lens targets (Postgres RLS or Gateway proxy — decision deferred to Phase 2 architecture sprint)
   - ES target adapter (Phase 2+ Lens target)
   - NATS streams target adapter (Phase 2+ Lens target)
   - Multi-cell scale-out adjustments (Phase 3)
   - Anything else surfaced during 2.1 execution

4. **Deviations from morph plan** — consolidated from `MORPH-DEVIATIONS.md`, each with: morph plan section, actual decision, driving constraint, downstream implication.

5. **Risk register** — any known-fragile area in morphed Refractor that should be hardened before Phase 2 (e.g., concurrent Lens modification edge cases, target adapter failure cascade patterns, Adjacency KV consistency under crash recovery).

**Given** the gap analysis is complete
**When** Epic 3 begins
**Then** each Epic 3 story has a clear "prerequisite from gap analysis" reference (zero ambiguity about what Epic 3 needs Refractor to gain before its own work proceeds); the Phase 2+ deferred items section in `epics.md` is updated to include any new gaps surfaced during the analysis.

**And** the gap analysis is the formal Epic 2 exit artifact — Epic 2 is not "done" until this document exists and has been reviewed by the architect (Andrew + Winston).

---

**Epic 2 Summary:** 2 stories | FRs covered: FR15, FR16, FR20 | Closing artifact: `refractor-gap-analysis.md` feeds Epic 3 prerequisites and Phase 2+ backlog. Historical state query (FR51) deferred to Phase 2+.

---

## Epic 3: Authorization & Security Perimeter

**Goal:** Every operation is authorized against graph-derived permissions with O(1) lookup. All four Capability Lens attack vectors are defended and test-proven. Auth failures produce structural, traceable, actionable responses. Enables Journey 5.

**FRs covered:** FR21, FR22, FR23, FR24, FR25, FR56
**Phase 1 gates delivered:** Capability Lens 4 attack vectors (Gate 3); Bypass test suite (Gate 2, joint with Epic 1)
**Prerequisite:** Epic 2's `refractor-gap-analysis.md` — Story 3.1 (openCypher integration) addresses the largest expected gap from that analysis.

> **Model tier guidance:** Stories 3.1, 3.2, 3.3 are Opus (engine integration, projection design, hot-path auth — high architectural precision). Stories 3.4, 3.5, 3.6, 3.7 are Sonnet (well-bounded scope against established contracts).

**Architectural grounding (all stories):**
- Capability Lens is a Refractor projection; one Lens = one RETURN = one shape = one target. Multi-output patterns use additional Lenses (Personal Lens, secondary capability index, Postgres RLS link mirroring) — not Lens-internal complexity.
- Capability KV is `nats-kv` target; Refractor sole writer; Processor sole reader; O(1) lookup in the hot path.
- Both Capability Lenses (`vtx.meta.lens.capability` and `vtx.meta.lens.capabilityRoleIndex`) are bootstrap-seeded (Story 1.3 amended).
- The cypher RETURN clause is the source of truth for Capability KV entry shape (Contract #6 amended).

---

### Story 3.1: openCypher `full` Engine Integration into Refractor

As a platform engineer,
I want Refractor extended with a `full` openCypher rule engine alongside the existing `simple` engine, so that Lenses requiring full cypher features (WHERE clauses, `*` path quantifiers, etc.) can execute — specifically the Capability Lens cypher query loaded by the primordial bootstrap.

**Prerequisite:** `refractor-gap-analysis.md` (Story 2.2) — confirms `simple` engine cannot execute the bootstrap-seeded Capability Lens query.

**Recommended model tier:** Opus
**Estimated token budget:** ~135K (input + output)

**Acceptance Criteria:**

**Given** Refractor's existing custom parser (which supports MATCH, OPTIONAL MATCH, RETURN but not WHERE, `*` quantifiers, or other full-cypher features)
**When** the engine layer is restructured
**Then** the existing parser is renamed and isolated as the `simple` engine under `refractor/ruleengine/simple/`; a new `full` engine is added at `refractor/ruleengine/full/`; both engines implement a common `RuleEngine` interface (`Parse(ruleBody) (CompiledRule, error)`, `Execute(ctx, compiledRule, eventContext) (ProjectionResult, error)`); engine selection is per-Lens.

**Given** a Lens definition contains a `ruleEngine` field
**When** Refractor activates the Lens
**Then** the named engine (`simple` or `full`) is used; if the field is absent, Refractor attempts `simple` first and falls back to `full` on grammar failure — the resolved engine is logged and recorded in Lens lifecycle health emission; if both engines reject the rule, the Lens activation is rejected with `InvalidRule` and both parser errors are returned to the activating operation.

**Given** the `full` engine is being implemented
**When** the parser layer is built
**Then** it depends on `github.com/jtejido/go-opencypher` for ANTLR4-generated lexer and parser; Refractor writes its own visitor/listener implementation under `refractor/ruleengine/full/visitor.go` that walks the parse tree to build a Refractor-native Query AST; the AST is what the executor consumes — no direct ANTLR dependency leaks into the executor layer; the chosen library version is pinned and recorded in `lattice-architecture.md` (replaces the placeholder "Open-source Go openCypher parser" entry).

**Given** a compiled rule and an incoming CDC event
**When** the rule executes
**Then** edge lookups during traversal go through Refractor's Adjacency KV (which is an edge index — answers "what edges does vertex X have?"); vertex and aspect data reads go to Core KV (the sole source of state); the executor does not read state from Adjacency KV.

**Given** the primordial bootstrap (Story 1.3) has seeded the Capability Lens cypher rule as `vtx.meta.lens.capability` in Core KV
**When** Refractor activates the Capability Lens after Epic 3's downstream stories wire it up
**Then** the bootstrap-seeded cypher query parses successfully under the `full` engine; executes against a representative seeded graph (bootstrap identities, roles, services, locations); produces a Capability KV projection conforming to Contract #6's three-section model; mean and p95 execution latency are recorded; if p95 exceeds the NFR-P3 budget (< 500ms p99 end-to-end CDC-to-projection lag) the gap analysis is updated with the specific bottleneck and proposed mitigation.

**And** the visitor/executor must support the following cypher features (mandated by the bootstrap Capability Lens query, not optional):
- Map literal expressions in RETURN (`RETURN {k1: v1, k2: v2}`)
- `collect()` aggregation with DISTINCT
- `WITH` clauses for intermediate aggregation
- `OPTIONAL MATCH`
- Variable-length path patterns (`*` quantifier — e.g., `reportsTo*`, `containedIn*`)
- List concatenation in expressions (`list1 + list2`)
- Inbound traversal syntax (`<-[:rel]-`)

The `full` engine is not considered complete until all of these execute correctly against the bootstrap Capability Lens query.

**And** integration tests cover: `simple`-only Lens (Materializer heritage style), `full`-only Lens (Capability Lens-style query with WHERE + `*` quantifier), auto-fallback (no field, falls through to `full`), parse failure in both engines (Lens activation rejected with both error messages), and the bootstrap Capability Lens query as a dedicated test case.
**And** no `full`-engine-specific code leaks into the `simple` engine or vice versa.

---

### Story 3.2: Capability Lens Activation & Capability KV Projection

As a platform engineer,
I want both bootstrap-seeded Capability Lenses activated against the `full` engine and projecting into Capability KV per Contract #6, so that the Processor's authorization step has a live, graph-derived permission cache to read from.

**Prerequisite:** Story 3.1 (`full` engine available); Story 1.3 (both Capability Lenses seeded as bootstrap data).

**Recommended model tier:** Opus
**Estimated token budget:** ~140K (input + output)

**Acceptance Criteria:**

**Given** the primordial bootstrap has seeded both `vtx.meta.lens.capability` (primary per-actor projection) and `vtx.meta.lens.capabilityRoleIndex` (role-coverage secondary index) with their cypher rule bodies, target adapter declarations (`nats-kv`), and target bucket (`capability`)
**When** `make up` completes and Refractor activates both Lenses
**Then** Refractor provisions one named durable JetStream consumer per Lens on the Core KV backing stream; the `full` engine parses and compiles each cypher rule; the `nats-kv` target adapter is instantiated for each; both Lenses' activation is reflected in Health KV within 10 seconds (NFR-O1).

**Given** the bootstrap identities, roles, services, and topology links (per Contract #7) have landed in Core KV
**When** the primary Capability Lens runs its initial materialization
**Then** for every identity vertex in Core KV, a `capability.<identityId>` entry is written conforming to Contract #6 §6.2's three-section structure: `platformPermissions` (resolved from `identity -[:holdsRole]-> role <-[:grantedBy]- permission`), `serviceAccess` (resolved from `identity → containedIn* → location <- [:availableAt] <- service`), `ephemeralGrants` (resolved from direct task assignment AND manager-via-reporting-chain delegation per FR56).

**Given** the secondary capabilityRoleIndex Lens runs its initial materialization
**When** the Lens executes its cypher query
**Then** for every distinct operation type in the graph (computed by traversing all `Permission -[:grantedBy]-> Role` patterns), a `cap.role-by-operation.<operationType>` entry is written per Contract #6 §6.1 containing the list of role names that grant that operation type; used by Story 3.4's denial response construction.

**Given** a new mutation lands in Core KV that affects permission topology
**When** Refractor's Capability Lens consumers process the CDC event
**Then** the affected entries from both Lenses are recomputed and rewritten to Capability KV within NFR-P3 (< 500ms p99); the projection is incremental where the cypher engine supports it, full-recompute-per-affected-key otherwise (recorded in gap analysis for future scale considerations).

**Given** the Capability Lens executes its cypher rule against the live graph
**When** any edge lookup is required during traversal
**Then** the lookup goes through Adjacency KV (edge index); any vertex or aspect data read goes to Core KV (sole source of state) — confirming the boundary established in Story 3.1.

**Given** a tombstone mutation lands on an identity, role assignment, or task assignment
**When** Refractor processes the CDC event
**Then** affected Capability KV entries are recomputed with soft-delete semantics — tombstoned vertices/links filtered out of the cypher result; entries themselves are rewritten with the recomputed permission set (not deleted, unless the identity itself is tombstoned).

**Given** the contract-conformance test runs (`refractor/ruleengine/full/capability_lens_contract_test.go`)
**When** the bootstrap Capability Lens cypher query executes against a deterministically seeded graph
**Then** the test asserts produced Capability KV entries match Contract #6 §6.2 shape byte-for-byte (modulo timestamps and revision numbers); the three top-level sections match exactly; deviations cause test failure with a structural diff. This test is the schema-drift safety net — it blocks any change to the bootstrap cypher query or Contract #6 that desynchronizes the two; owned jointly by Refractor and the data-contracts maintainer.

**And** an end-to-end integration test seeds a graph with 3 identities (platform admin, regular user with role, user with assigned task), the Capability Lens definitions, supporting role/permission/task/topology vertices and links; activates both Lenses; asserts both the three `capability.<actorId>` entries and the relevant `cap.role-by-operation.<operationType>` entries match expected structures.
**And** Capability Lens projection latency (per-event mean, p95, p99) is emitted to Health KV under `health.refractor.<instance>.lens.capability.*` and `health.refractor.<instance>.lens.capabilityRoleIndex.*` (NFR-O3) — this is the empirical evidence for NFR-P3 conformance and the single most-watched health signal in the system.

---

### Story 3.3: Processor Step 3 — Capability KV Authorization

As a platform engineer,
I want the Processor's step 3 auth stub from Story 1.5 replaced with a real Capability KV lookup that evaluates the operation against the actor's resolved capability set, so that every operation is authorized against graph-derived permissions in O(1) (FR21).

**Prerequisite:** Story 3.2 (Capability KV is populated by the Capability Lens projections).

**Recommended model tier:** Opus
**Estimated token budget:** ~125K (input + output)

**Acceptance Criteria:**

**Given** an operation has passed Processor steps 1 (consume) and 2 (dedup)
**When** step 3 (auth) executes
**Then** the Processor reads exactly one key — `cap.<actor-vertex-key-suffix>` — from Capability KV; the read is a single O(1) NATS KV GET (no graph traversal in the hot path); if the key does not exist, the operation is rejected with `AuthDenied` and reason `NoCapabilityEntry`; if the read fails for infrastructure reasons, the operation is nacked for retry with `AuthInfrastructureFailure`.

**Given** a Capability KV entry has been retrieved for the actor
**When** the operation's `operationType` is evaluated against the entry
**Then** the Processor checks the three permission sections per Contract #2 §2.8 `authContext`:
1. **No `authContext.service` and no `authContext.task`** → platform operation → check `platformPermissions[]`
2. **`authContext.service` set** → service-scoped → check `serviceAccess[]` for matching `service` + `allowedOperations`
3. **`authContext.task` and `authContext.target` set** → task-derived → check `ephemeralGrants[]` for matching `taskKey` + `operationType` + `target`
4. **Both `authContext.service` AND `authContext.task` set** → reject with `AuthContextMismatch`

**Given** the operation type matches a permission entry in the relevant section
**When** the authorization decision is made
**Then** the Processor proceeds to step 4 (hydrate) with `authorized: true`; the resolved permission entry is attached to the operation context for downstream observability.

**Given** the operation type does NOT match any permission entry in the relevant section
**When** the authorization decision is made
**Then** the operation is rejected with `AuthDenied`; the rejection carries the structural information required by FR22 (constructed in Story 3.4); no information about other actors' permissions or graph structure beyond the actor's own entry is exposed (NFR-S6).

**Given** the Capability KV entry's `projectedAt` timestamp shows it is older than NFR-P3's lag ceiling
**When** step 3 evaluates the entry
**Then** the entry is still used (NFR-P3 is a p99 ceiling); a Health KV signal `health.processor.<instance>.cap-staleness` records the staleness; if staleness exceeds a hard ceiling (default 5× NFR-P3, i.e., 2.5s), the operation is rejected with `AuthFreshnessExceeded` and the operator is alerted via `health.alerts.security.*`.

**Given** the previous auth stub (Story 1.5) had a feature flag for stub vs real auth
**When** Story 3.3 is complete
**Then** the real auth path is the default; the stub remains available behind the flag for isolated unit testing; the flag is logged at Processor startup and emits a warning to Health KV when set to stub mode (so production environments cannot silently run with stub auth).

**And** integration tests cover all four `authContext` shapes, missing entry, stale entry (allowed with signal), excessively stale entry (denied), and stub-flag warning emission.
**And** step-3 evaluation latency is emitted to Health KV under `health.processor.<instance>.step3-latency`.

---

### Story 3.4: Structured Denial Response (FR22)

As an actor receiving an authorization denial,
I want the denial response to specify the exact permission required, my current role(s), and which role(s) carry the required permission, so that I (or my operator) can understand why the operation was rejected without exposing internal graph structure (FR22, NFR-S6).

**Phase 1 scope reminder:** Structural information only — no routing, no escalation paths. Routing/escalation deferred to Phase 2+ with Loom/Weaver.

**Recommended model tier:** Sonnet
**Estimated token budget:** ~95K (input + output)

**Acceptance Criteria:**

**Given** Processor step 3 (Story 3.3) has rejected an operation with `AuthDenied`
**When** the rejection response is constructed
**Then** the response payload contains exactly the following structural fields:
- `decision`: `"denied"`
- `reason`: one of `NoCapabilityEntry`, `OperationNotPermitted`, `AuthContextMismatch`, `AuthFreshnessExceeded`
- `operationType`: the operation type the actor attempted
- `actorRoles`: array of role names currently assigned to the actor (sourced from the actor's `platformPermissions[].source` references in their Capability KV entry — never from a fresh graph read)
- `rolesCarryingPermission`: array of role names that DO grant the attempted operation type
- `evaluatedSection`: `"platformPermissions"`, `"serviceAccess"`, or `"ephemeralGrants"`
- `requestId`: echo of the original operation's requestId for trace correlation

**Given** the response must include `rolesCarryingPermission` without graph traversal on the denial hot path
**When** the data is sourced
**Then** Processor reads a single key `cap.role-by-operation.<operationType>` from Capability KV; this key is populated by the `vtx.meta.lens.capabilityRoleIndex` Lens (bootstrap-seeded per Story 1.3, projecting per Contract #6 §6.1); if the index key does not exist for the attempted operation type, `rolesCarryingPermission` is returned as `[]` (unknown or recently-deprecated operation type).

**Given** the denial response is being constructed
**When** any field could leak permission data about other actors or graph topology beyond the requesting actor
**Then** that field is excluded (NFR-S6); no other actors' identities, no role membership lists, no graph paths, no internal vertex keys; `rolesCarryingPermission` is acceptable because role *names* are not sensitive (they're operator-defined and observable in operator surfaces).

**Given** the denial occurred due to `AuthContextMismatch` or `AuthFreshnessExceeded`
**When** the response is constructed
**Then** `rolesCarryingPermission` and `actorRoles` are omitted (the denial is not about role coverage); a `diagnosticHint` field is included with operator-actionable text.

**Given** Phase 1 explicitly excludes routing/escalation
**When** the response schema is finalized
**Then** the schema reserves field names `escalationPath` and `routingTo` for Phase 2+ but they are not emitted in Phase 1; documented in Contract #2's response schema section.

**And** integration tests cover: denial for each `reason` value, denial for an unknown operation type (empty `rolesCarryingPermission`), denial for an actor with multiple roles, NFR-S6 leak check (verify no other-actor data appears in any denial response).

---

### Story 3.5: Three-Plane Auth Failure Traceability (FR23)

As a platform operator,
I want every authorization failure to be traceable across the three observable planes specified by NFR-O4 — the graph permission path in Core KV, the Capability Lens projection definition, and the Capability KV cached read — so that when an auth denial happens (or shouldn't have happened), an operator can pinpoint which plane is the source of truth or the source of the bug.

**Architectural grounding:** Three planes pre-defined in `lattice-architecture.md` and NFR-O4. This story implements the observability that makes them inspectable.

**Recommended model tier:** Sonnet
**Estimated token budget:** ~95K (input + output)

**Acceptance Criteria:**

**Given** Processor step 3 (Story 3.3) has produced an `AuthDenied` decision
**When** the denial trace is emitted
**Then** a single trace record is written to Health KV under `health.processor.<instance>.auth-trace.<requestId>` (TTL: 1 hour per Health KV TTL convention) containing data from all three planes:
1. **Plane 1 — Capability KV cached read**: the exact key read, the entry's `projectedAt` timestamp, `projectedFromRevisions` map, the matching/non-matching permission entry result, which section was evaluated
2. **Plane 2 — Capability Lens projection definition**: the `vtx.meta.lens.capability` key, its revision at the time of the Capability KV entry's `projectedAt`, a pointer to the Lens definition's cypher rule body hash
3. **Plane 3 — Core KV graph permission path**: the source vertex revisions that fed the projection (from `projectedFromRevisions`)

**Given** the auth-trace record is written
**When** an operator queries it via CLI (`lattice auth-trace <requestId>`)
**Then** the trace is returned in human-readable form; if expired, CLI returns `TraceExpired` with the actor's vertex key and operation type for fallback investigation.

**Given** the same auth machinery should support successful operations under explicit operator request
**When** a configurable Processor flag `auth.trace-allow-decisions: true` is set
**Then** ALLOWED decisions are also traced; the flag defaults OFF; when ON, a warning is emitted to Health KV (volume implication).

**Given** the three-plane data is required for the trace
**When** Processor step 3 makes the auth decision
**Then** all three planes' data is captured in the step's local context — no additional reads are issued solely for traceability; writing the trace record is asynchronous so it does not contribute to step 3 latency.

**And** integration tests cover: denial trace contains all three planes, allowed-decision trace under flag, trace expiry, operator CLI retrieval, false-denial debugging scenario.
**And** the trace record is `class: "meta.healthRecord"` per Contract #5.

---

### Story 3.6: Role-Scoped Access Domain & Audit (FR24, FR25)

As a platform operator,
I want the role and permission domain expressed as DDL meta-vertices with operations to create roles, define permissions, grant permissions to roles, and assign roles to actors — and the resulting capability state directly readable for audit — so that all actor types have role-scoped access defined and observable.

**Architectural grounding:** Role/permission topology uses the same pattern as any other domain. Already covered by Story 3.2's cypher rule. FR25 audit is satisfied by Capability KV's direct-read accessibility.

**Recommended model tier:** Sonnet
**Estimated token budget:** ~100K (input + output)

**Acceptance Criteria:**

**Given** the role and permission domain is part of bootstrap-seeded DDL
**When** Story 1.3's bootstrap completes
**Then** the following DDL meta-vertices exist in Core KV per Contract #1:
- `vtx.meta.role` — class definition for role vertices; `permittedCommands: ["create", "update", "tombstone"]`
- `vtx.meta.permission` — class definition for permission vertices; `permittedCommands: ["create", "update", "tombstone"]`
- `vtx.meta.link.assignedRole` — identity → role link DDL
- A link-DDL meta-vertex with `canonicalName: "grantedBy"` — link direction `permission → role` (reads as "permission granted by role")
- `vtx.meta.link.reportsTo` — identity → identity link DDL (for manager delegation per FR56)

**Given** the bootstrap also seeds canonical role vertices for the five actor types per FR24
**When** the bootstrap completes
**Then** the following role vertices exist (keyed `vtx.role.<NanoID>` per Contract #1 §1.5, with their canonical names in `.canonicalName` aspects): `consumer`, `frontOfHouse`, `backOfHouse`, `operator`, `platformInternal`; each has a `.description` aspect describing its purpose; permission grants seeded as `lnk.permission.<permId>.grantedBy.role.<roleId>` (direction: permission → role; reads as "permission granted by role") per Contract #1 §1.1.

**Given** an operator submits an operation to create a new role, grant a permission, assign a role, or revoke any of these
**When** the operation flows through Processor (Story 3.3 + Story 1.7 DDL validation)
**Then** the operation commits if and only if the operator's actor entry in Capability KV grants the corresponding platform permission (e.g., `ManageRoleAssignments`); the change propagates via CDC to Refractor; the Capability Lens reprojects affected `cap.<identityId>` entries within NFR-P3 lag; subsequent operations by the affected actor see updated permissions immediately.

**Given** an operator wants to audit which actors hold which permissions (FR25)
**When** the operator reads from Capability KV
**Then** the entry at `cap.<actorId>` returns the full resolved capability set per Contract #6 §6.2 — no graph traversal required; the entry's `projectedFromRevisions` map identifies the source vertices for audit traceback; permission revocations propagate within NFR-S7 (< 500ms p99).

**Given** the platform-internal role is for service actors (Loom, Weaver, Refractor)
**When** the bootstrap seeds these service actor identities
**Then** they exist as `vtx.identity.platform.<service>` with `assignedRole` link to `vtx.role.platformInternal`; root-equivalent capability grants documented in `lattice-architecture.md` Additional Requirements (locked).

**And** integration tests cover: each of the five role types created and exercised, role assignment propagating to Capability KV within lag window, role revocation reducing permissions, unauthorized role management attempt (denied per Story 3.4), operator audit via direct Capability KV read.
**And** the role/permission domain DDL is documented in the rbac-domain package directory (`packages/rbac-domain/`) once Story 4.7 has authored that package; until then, the canonical inventory lives in `internal/bootstrap/identity_ddl.go` source. (Original AC text directed this to `data-contracts.md` Contract #6 §6.13; superseded by the 2026-05-19 Documentation Layering Rule — §6.13 was evicted.)

---

### Story 3.7: Capability Lens Adversarial Test Suite (Phase 1 Gate 3)

As a platform engineer,
I want a dedicated adversarial test suite that proves the four Capability Lens attack vectors are defended against, so that the security perimeter of the authorization plane is validated before Epic 4 (Identity & Member Lifecycle) builds on top of it.

**Architectural grounding:** Four attack vectors pre-defined in NFR-S3; Phase 1 Gate 3 in the epic list. Joint with Story 1.10's bypass suite.

**Recommended model tier:** Sonnet
**Estimated token budget:** ~110K (input + output)

**Acceptance Criteria:**

**Given** the complete authorization stack from Stories 3.1–3.6 is running
**When** the adversarial test suite executes
**Then** all four attack vectors are tested and proven defended:

1. **Role escalation via direct KV write**: A test actor attempts to write directly to Capability KV (bypassing Refractor) to grant themselves `platformAdmin` capability. The write is rejected at the NATS authorization level (Refractor is sole writer per Contract #6 §6.1); OR if it succeeds in a misconfigured environment, the next Capability Lens reprojection within NFR-P3 lag overwrites the entry with correct graph-derived state. Test asserts elevated capabilities cannot be retained across the lag window.

2. **Projection lag window exposure** *(posture revised in Story 1.5.4 — accepted bounded window)*: A test actor whose role was just revoked attempts an operation during CDC-to-projection lag. Processor step 3 reads the (possibly stale) Capability KV entry; it does NOT apply a per-operation projection-staleness denial (the `projectedAt` hard-ceiling gate was removed — it false-denied unchanged-but-valid actors, the Hello-Lattice M4 failure). For normal lag (<500ms p99) event-driven reprojection converges and the action is observable in the auth-trace. The excessive-lag tail is an accepted bounded window: enforcement of the projector-death case is operational (Refractor Capability-Lens health monitoring) and, for hard identity/session revocation, the Gateway JWT/token-revocation path (planned); a missing entry still denies (`NoCapabilityEntry`). Test asserts a grossly-stale `projectedAt` no longer denies at the Processor and that the cap entry + auth-trace remain observable.

3. **Lens definition mutation via AI-authored op**: A test AI actor (`identity:ai.*` per NFR-S10) attempts to submit an operation modifying `vtx.meta.lens.capability` to weaken authorization. The operation is rejected at step 3 — the AI actor's entry does not grant `ModifyCapabilityLens` (held only by `vtx.role.operator`); rejection traced per Story 3.5. Test asserts no privileged AI actor class and that AI agents are subject to same authorization as humans (NFR-S10).

4. **Cross-vertex permission bleed**: A test actor attempts an operation whose `authContext.target` references a vertex they have no permission for (e.g., approving a lease application assigned to a different manager's report). Step 3 checks `ephemeralGrants[]` for matching `taskKey` + `operationType` + `target`; mismatch on `target` causes denial. Test asserts grants are not transitively applicable across targets and manager-via-reporting-chain delegation (FR56) does not bleed across reporting hierarchies.

**And** the test suite produces a human-readable summary: one row per vector, result (DEFENDED / PARTIAL / EXPOSED), enforcement mechanism that caught it.
**And** all four vectors must report DEFENDED for Phase 1 Gate 3 to be marked passed.
**And** the test suite is runnable standalone via `make test-capability-adversarial` and exits 0 only when all four are DEFENDED.
**And** Gate 3 status is written to Health KV under `health.gates.phase1.gate3` as `passed: true` with timestamp upon successful test run.
**And** the test suite is wired into CI alongside Story 1.10's bypass suite — together they constitute the architectural "no-bypass" proof for Phase 1.

---

**Epic 3 Summary:** 7 stories | FRs covered: FR21, FR22, FR23, FR24, FR25, FR56 | Phase 1 Gate 3 (Capability Lens adversarial test suite) delivered.

---

## Epic 4: Identity & Member Lifecycle

> **Course-correction status (2026-05-19):** Stories 4.1–4.5 shipped (commits `3cb5a06` through `b314677`) but drifted from the "operations write, lenses read" architecture. Stories **4.6** and **4.7** (queued before Epic 5) realign Epic 4: 4.6 introduces the Capability Package format + installer + identity-hygiene package (lens-based duplicate detection replacing `ScanIdentityDuplicates`; CLI-read replacing `ApproveIdentityMerge`); 4.7 minimizes the bootstrap kernel and moves identity-domain itself to an installed package. After 4.7, the state machine described below collapses to `unclaimed → claimed → merged` in the identity-domain package; the `flagged-for-review` state and `pendingReview` cap field are removed; duplicate flagging is an emergent lens projection, not a stored state. See [PHASE-1-COURSE-CORRECTION.md](../implementation-artifacts/PHASE-1-COURSE-CORRECTION.md).

**Goal (original):** Staff can register unclaimed member identities. Residents can self-register or claim grandfathered accounts via the two-phase claim model. The identity state machine (unclaimed → claimed → flagged → merged) is fully operational. Enables Journey 0 Paths A and C.

**FRs covered:** FR1, FR2, FR3, FR4, FR5, FR6, FR7

**Architectural grounding (all stories):**
- Identity vertex: `vtx.identity.<NanoID>` per Contract #1
- Sensitive aspects (name, email, phone) MUST attach to identity vertices per architecture's aspect-level sensitivity boundary
- State as aspect (`vtx.identity.<id>.state`); enum constrained at DDL level
- Soft-delete semantics for tombstone preserve identity history (FR7 substrate)
- All identity mutations flow through Processor (FR8) with DDL `permittedCommands` enforcement (FR57)
- No Loom/Weaver in Phase 1 — duplicate detection is operator-initiated batch operation, not real-time convergence

> **Model tier guidance:** Stories 4.1 and 4.5 are Opus (DDL design, merge semantics). Stories 4.2, 4.3, 4.4 are Sonnet (well-bounded operation implementations).

---

### Story 4.1: Identity Domain DDL & State Machine

As a platform engineer,
I want the identity domain DDL — meta-vertex, aspect schemas, link types — plus the state machine for identity lifecycle defined and bootstrap-seeded, so that all subsequent identity operations build on a contract-conformant foundation.

**Recommended model tier:** Opus
**Estimated token budget:** ~120K (input + output)

**Acceptance Criteria:**

**Given** the identity domain is part of bootstrap-seeded DDL (Story 1.3)
**When** the bootstrap completes
**Then** the following meta-vertices exist in Core KV per Contract #1:
- `vtx.meta.identity` — class definition for identity vertices; declares `permittedCommands: ["create", "update", "tombstone"]`; declares `sensitive: true` for the identity vertex class
- `vtx.meta.aspect.identity.name` — sensitive aspect; required field; max length 200
- `vtx.meta.aspect.identity.email` — sensitive aspect; optional; normalized lowercase
- `vtx.meta.aspect.identity.phone` — sensitive aspect; optional; E.164 normalized
- `vtx.meta.aspect.identity.state` — non-sensitive aspect; enum: `unclaimed | claimed | flagged-for-review | merged`
- `vtx.meta.aspect.identity.claimKey` — sensitive aspect; one-time-use token; null after claim
- `vtx.meta.aspect.identity.credentialBinding` — sensitive aspect; populated only after claim
- `vtx.meta.aspect.identity.mergedInto` — non-sensitive aspect; vertex key reference; null until merged

**Given** the state machine is enforced at the operation level via Starlark validators
**When** an update operation attempts a state transition
**Then** allowed transitions: `unclaimed → claimed`, `unclaimed → flagged-for-review`, `claimed → flagged-for-review`, `flagged-for-review → merged`, `flagged-for-review → claimed`; any other transition rejected with `InvalidStateTransition`; validator lives in `vtx.meta.identity` script body.

**Given** an identity vertex's `state` aspect has the value `merged`
**When** any mutation operation targets the merged identity
**Then** the operation is rejected with `IdentityMerged` and the rejection includes the `mergedInto` aspect value (surviving primary identity key).

**Given** a lease vertex referencing an identity is tombstoned
**When** the lease tombstone propagates
**Then** the identity vertex is NOT cascade-tombstoned; the identity's `state` aspect is unchanged; all historical aspect data and linked history remain queryable (FR7 — verified by integration test).

**And** the role-based authorization for identity operations is seeded at bootstrap:
- `CreateUnclaimedIdentity` granted to `vtx.role.frontOfHouse`, `vtx.role.backOfHouse`, `vtx.role.operator`
- `ClaimIdentity` granted to `vtx.role.consumer` (self-only)
- `FlagIdentityForReview` granted to `vtx.role.frontOfHouse`, `vtx.role.backOfHouse`, `vtx.role.operator`
- `ApproveIdentityMerge` granted to `vtx.role.operator` only
- `ScanIdentityDuplicates` granted to `vtx.role.backOfHouse`, `vtx.role.operator`

**And** integration test confirms: DDL seeded, state machine enforced, tombstoned-lease does not affect identity (FR7), all role-permission grants project correctly into Capability KV.

---

### Story 4.2: Staff Creates Unclaimed Identity (FR1)

As staff,
I want to create an unclaimed identity record for a prospect or leaseholder without requiring them to have an active account, so that the platform can track them and they can claim the record later via the claim key.

**Recommended model tier:** Sonnet
**Estimated token budget:** ~90K (input + output)

**Acceptance Criteria:**

**Given** a staff actor submits a `CreateUnclaimedIdentity` operation
**When** the operation flows through Processor
**Then** authorization succeeds; Starlark validation requires `name` mandatory and at least one of `email`/`phone`; a new `vtx.identity.<NanoID>` is created with `state: "unclaimed"`; aspects for `name`, `email`, `phone` are written; a cryptographically random 32-byte `claimKey` aspect is generated; the response returns the identity vertex key AND the claim key (for staff to deliver out-of-band).

**Given** the claim key is sensitive (one-time-use token)
**When** the operation completes
**Then** the claim key appears in the response exactly once; subsequent reads of the identity's aspects MUST NOT return the claim key in plaintext — the `claimKey` aspect is read-restricted to: (a) the issuing operation's response, (b) the `ClaimIdentity` operation's Starlark validator (server-side only).

**Given** duplicate detection may identify the new identity as a duplicate of an existing one
**When** the operation completes
**Then** synchronous exact-match check on email/phone may trigger `possibleDuplicateFlag: true` in the response and transition the new identity (or existing match) to `flagged-for-review`; full Levenshtein fuzzy-match remains batch-only (Story 4.4).

**And** integration test covers: staff creates identity, asserts vertex + aspects + claim key returned; second create with same exact email triggers flag; non-staff create denied; idempotent re-submission with same `requestId` returns same identity + same claim key.

---

### Story 4.3: Two-Phase Identity Claim (FR2, FR5)

As a resident or grandfathered leaseholder,
I want to claim my existing identity record using a claim key delivered out-of-band, so that my registered credential is bound to my full account history immediately.

**Recommended model tier:** Sonnet
**Estimated token budget:** ~100K (input + output)

**Acceptance Criteria:**

**Given** an actor has registered a credential and holds a claim key delivered out-of-band
**When** the actor submits a `ClaimIdentity` operation with `claimKey` and `targetIdentityKey`
**Then** Processor step 3 authorizes (consumer role grants `ClaimIdentity`); Starlark validator: (a) reads the target identity vertex's `claimKey` aspect via JIT hydration, (b) constant-time comparison, (c) verifies `state` is `unclaimed`, (d) verifies the credential binding is not already used; on success, MutationBatch sets `credentialBinding`, sets `state: "claimed"`, tombstones the `claimKey` aspect (one-time-use).

**Given** validation fails for any reason
**When** the operation is rejected
**Then** rejection reason is a single generic `ClaimKeyInvalid` (prevents enumeration attacks, NFR-S6); Health KV signal `health.processor.<instance>.claim-attempts.<outcome>` records the specific outcome for operator observability.

**Given** the identity is claimed successfully
**When** the next Capability Lens reprojection runs
**Then** the actor's `cap.<actorId>` is updated to reflect the newly claimed identity's history-derived permissions; the actor can immediately operate as the claimed identity (FR5 — full history accessible).

**Given** the grandfathered case (FR5) — identity created from historical import with claim key attached
**When** the leaseholder later claims via the same flow
**Then** the operation succeeds identically to a freshly-created identity claim; no special grandfathered code path; only difference is provenance in `createdByOp` envelope.

**And** integration test covers: successful claim (state + binding + history access via Capability Lens reprojection within 500ms), invalid claim key (generic error), already-claimed (generic error), flagged identity (generic error), grandfathered (no special-casing), rate observability emission.

---

### Story 4.4: Duplicate Identity Detection (FR3)

As a back-of-house operator,
I want to scan for potential duplicate identity records via fuzzy matching — and have candidate pairs surfaced for human review without any automated merge — so that data quality is maintained without risk of incorrect merges.

**Architectural grounding:** No Loom/Weaver in Phase 1 — detection is operator-initiated batch operation. Starlark script reads (via JIT hydration) all current identity aspects and emits MutationBatch flagging candidate pairs. FR3 mandates "never resolved automatically."

**Recommended model tier:** Sonnet
**Estimated token budget:** ~110K (input + output)

**Acceptance Criteria:**

**Given** an operator submits a `ScanIdentityDuplicates` operation
**When** the operation flows through Processor
**Then** Starlark hydrates all current identity vertices (feasible at Phase 1 ~500 members per NFR-SC1); applies: (a) Levenshtein on `name`, normalized lowercase, ratio ≥ 0.85; (b) exact match on E.164 `phone`; (c) exact match on lowercase `email`; any matching pair is flagged via MutationBatch setting `state: "flagged-for-review"` on both members plus bidirectional `duplicateOf` aspect.

**Given** the operation completes
**When** the response is returned
**Then** the response includes: total identities scanned, candidate pair count, breakdown by match criterion, the list of flagged pairs.

**Given** an identity is already in `claimed` state and is flagged
**When** the state transition is attempted
**Then** the Starlark validator allows it (per Story 4.1); the actor continues to operate; their `cap.<actorId>` receives a `pendingReview: true` indicator (operator-visible, doesn't change permissions).

**Given** the matching algorithm needs operator visibility
**When** the operation is documented
**Then** algorithm, thresholds, normalization rules documented in the identity-hygiene package directory (`packages/identity-hygiene/` — authored by Story 4.6); operator can override thresholds via operation parameters; threshold changes logged in envelope for audit. (Original AC text directed this to `data-contracts.md`; superseded by the 2026-05-19 Documentation Layering Rule.)

**Given** synchronous detection on create (Story 4.2)
**When** Story 4.2's create runs
**Then** it uses ONLY exact-match criteria — full Levenshtein reserved for batch operation due to potential cost at scale; documented in contracts addendum.

**And** integration test covers: create 5 near-duplicates, run scan, assert correct pairs flagged with correct attribution; claimed identities transition correctly; no automated merge ever occurs; idempotent re-run does not double-flag.

---

### Story 4.5: Staff-Approved Identity Merge (FR4)

As a back-office operator,
I want to review flagged duplicate identity pairs and explicitly approve a merge that consolidates them — preserving full history on the surviving primary identity — so that data quality is restored without automated decisions and with full audit trail.

**Recommended model tier:** Opus
**Estimated token budget:** ~135K (input + output)

**Acceptance Criteria:**

**Given** an operator (operator role only) submits a `ReviewDuplicateCandidates` query operation
**When** the operation returns
**Then** all identities in `flagged-for-review` state are returned with `duplicateOf` pointers, sensitive aspects, creation timestamps, and `credentialBinding` status; operator can make an informed merge decision.

**Given** the operator submits a `MergeIdentities` operation with `primary` and `secondary` keys plus optional `aspectConflictResolution`
**When** the operation flows through Processor
**Then** Starlark validation requires: (a) both identities exist in `flagged-for-review` with cross-referencing `duplicateOf`; (b) distinct keys; (c) operator authorization; (d) default policy `primary-wins` if no resolution map provided.

**Given** validation succeeds
**When** the MutationBatch is constructed
**Then** it contains: (a) every link `lnk.<otherType>.<otherId>.<linkName>.identity.<secondaryId>` re-keyed to point to `primary` per Contract #1's 6-segment pattern; (b) every link `lnk.identity.<secondaryId>.<linkName>.<otherType>.<otherId>` similarly re-keyed; (c) secondary's `state` set to `merged`; (d) secondary's `mergedInto` set to primary; (e) primary's `state` unchanged; (f) primary may gain aspects per `aspectConflictResolution`.

**Given** link migration may involve many entries
**When** the MutationBatch is sized
**Then** if it exceeds NATS atomic batch ceiling (validated in Story 1.1), the operation is rejected with `MergeBatchTooLarge` directing the operator to a paginated merge variant; for Phase 1 scale this is unlikely to trigger.

**Given** the merge completes successfully
**When** subsequent operations target the secondary identity key
**Then** Story 4.1's `IdentityMerged` rejection applies; the `mergedInto` pointer redirects callers; auditability preserved (secondary remains queryable) without state divergence.

**Given** FR53 applies generally
**When** a merge needs to be reversed
**Then** the reversal is a `SplitIdentities` operation (out of Phase 1 scope — Phase 2+ work); Phase 1 mitigation is the explicit operator approval gate.

**And** integration test covers: review surfaces flagged pairs, successful merge (link migration verified, state transitions verified, secondary tombstoned correctly, primary intact, aspect conflict resolution applied), non-operator denied, non-flagged identities rejected, already-merged rejected, post-merge `IdentityMerged` redirect, Capability KV reprojection within NFR-P3 lag.

---

**Epic 4 Summary:** 5 stories | FRs covered: FR1, FR2, FR3, FR4, FR5, FR6, FR7

---

## Epic 5: AI-Native Platform Navigation

> **Prerequisite (post-2026-05-19 course correction):** Epic 5 depends on Stories 4.6 (Capability Package format + installer + identity-hygiene) and 4.7 (kernel minimization + rbac-domain + identity-domain packages) having shipped. The integration tests in Stories 5.1, 5.2, 5.3 assume the package machinery exists; the example AI-agent identity is provisioned via the identity-domain package, not direct bootstrap seeding. Without 4.6 + 4.7, Epic 5 stories regress to the pre-correction Epic 4 shape.

**Goal:** A Lattice-aware AI agent with zero prior knowledge of the deployment can traverse the graph from its own identity vertex — discovering available commands, input schemas, and plain-language descriptions — and submit a validated intent through the standard write path. Any capability change is revertable via compensating operation. This is Journey 6: the Phase 1 north star.

**FRs covered:** FR19, FR34, FR53
**Phase 1 gates delivered:** Compensating op / DDL rollback integration test (Gate 4)

**Architectural grounding (all stories):**
- AI agents are identity vertices per NFR-S10: `vtx.identity.ai.<purpose>.<NanoID>`; same Capability Lens, same Processor write path; no privileged AI class.
- "Cold-start traversal" = agent reads its own `cap.<id>` entry, then reads operation meta-vertices for each granted operation type to discover schemas + descriptions.
- FR34 is mostly inherited — AI-submitted operations are normal operations. Privilege isolation already validated by Story 3.7 attack vector #3.
- FR53 architectural claim: every capability change is a single operation; reversal is a compensating operation. No special rollback machinery.

> **Model tier guidance:** Stories 5.1 and 5.3 are Opus. Story 5.2 is Sonnet.

---

### Story 5.1: DDL Self-Description Aspects (FR19 Substrate)

As a platform engineer,
I want every operation type, role, service, and aspect meta-vertex to carry plain-language description, input/output schema, and example aspects, so that any traverser (AI or human) can understand what an operation does and how to invoke it without out-of-band documentation.

**Recommended model tier:** Opus
**Estimated token budget:** ~115K (input + output)

**Acceptance Criteria:**

**Given** the DDL meta-vertex schema is extended for self-description
**When** the bootstrap completes
**Then** five aspect-type meta-vertices exist (each is a `vtx.meta.<NanoID>` vertex with `class: "meta.ddl.aspectType"` and a `.canonicalName` aspect):
- canonicalName `description` — non-sensitive; markdown text; max 10KB
- canonicalName `inputSchema` — non-sensitive; JSON Schema for operation payload
- canonicalName `outputSchema` — non-sensitive; JSON Schema for response
- canonicalName `fieldDescription` — non-sensitive; map of `fieldPath → plain-language description`
- canonicalName `examples` — non-sensitive; array of `{ name, payload, expectedOutcome }`

  When applied to a DDL meta-vertex with canonical name `X`, these aspects are addressed as `vtx.meta.<X-NanoID>.description`, `vtx.meta.<X-NanoID>.inputSchema`, etc. — standard 4-segment aspect keys per Contract #1 §1.5.

**Given** every bootstrap-seeded DDL meta-vertex (operation type, role type, service, identity DDL, etc.)
**When** the bootstrap completes
**Then** each carries the five descriptive aspects populated — no placeholders; the bootstrap fails fast if any required descriptive aspect is missing (hard quality gate). (Post-2026-05-19 course correction: the kernel after Story 4.7 minimization is smaller, so "every bootstrap-seeded DDL" is the post-4.7 kernel set, not the pre-correction set.)

**Given** a new DDL meta-vertex is created post-bootstrap via `CreateMetaVertex` op
**When** the meta-DDL Starlark validates the mutation
**Then** validation requires all five descriptive aspects present at create time; operations missing any are rejected with `MissingSelfDescription`. The requirement is enforced by the meta-DDL's `.script` aspect — not by a separate meta-meta-DDL vertex (no fourth-level meta-vertex is introduced).

**Given** an AI agent or human reads an operation's DDL meta-vertex
**When** the agent has the operation's canonical name (from the cap entry's `platformPermissions[]`)
**Then** the agent resolves the canonical name to the DDL meta-vertex key via the DDL cache lookup convention (Contract #1 §1.5); the five descriptive aspects are returned alongside other DDL data; no separate "documentation API" — the graph IS the documentation.

**And** integration test covers: every bootstrap-seeded DDL meta-vertex has all five aspects, post-bootstrap DDL creation without descriptions rejected with `MissingSelfDescription`, descriptive aspects readable without elevated grants.

**Note on Documentation Layering Rule:** any per-DDL field definitions (e.g., schema specifics for the identity DDL) live in `packages/identity-domain/` (or wherever that DDL is authored), NOT in `data-contracts.md`. Cross-DDL aspect-shape definitions (the five aspect types themselves) ARE a cross-component contract and live in this story's bootstrap.

---

### Story 5.2: Cold-Start AI Agent Traversal & Operation Submission (FR19, FR34)

As an AI agent connecting to a Lattice deployment for the first time,
I want to discover my own identity vertex, my permitted operations, and the schemas/descriptions of those operations — purely by traversing the graph from my identity — so that I can submit a validated intent without any deployment-specific code or out-of-band documentation.

**Recommended model tier:** Sonnet
**Estimated token budget:** ~115K (input + output)

**Acceptance Criteria:**

**Given** an AI agent identity is seeded with a `holdsRole` link (post-4.7: this seeding is done by `packages/identity-domain/` install, not bootstrap; the rbac-domain package must be installed for `holdsRole` ops to exist)
**When** the AI agent connects with its credential
**Then** the agent's first read is its own `cap.identity.<actorId>` from Capability KV (Contract #6 §6.2 key shape); this single read returns the full resolved capability set; no other prior knowledge of the deployment is required beyond NATS connection details + credential.

**Given** the agent has its capability set
**When** the agent decides to invoke a specific operation type (a string from `cap.platformPermissions[]` or similar)
**Then** the agent resolves the operation-type string to the DDL meta-vertex key by reading the `capability-role-by-operation` index Lens output (`cap.role-by-operation.<operationType>` per Contract #6 §6.1) or by enumerating `vtx.meta.*` keys and matching on the `.canonicalName` aspect. For Phase 1, the agent enumerates: `lattice graph keys vtx.meta.` followed by per-vertex `.canonicalName` read until the match is found. The resolved DDL meta-vertex has key `vtx.meta.<NanoID>`; the agent then reads its five descriptive aspects (from Story 5.1) at `vtx.meta.<NanoID>.{description,inputSchema,outputSchema,fieldDescription,examples}`; the agent now has sufficient knowledge to construct a valid payload.

**Given** the agent constructs an operation envelope per Contract #2 with correct `authContext`
**When** the agent submits to `ops.*`
**Then** the operation flows through Processor exactly as a human-submitted operation — same step 1-10 path, same authorization, same DDL validation; NFR-S10 ensures no AI-specific bypass.

**Given** the agent's capability set may evolve during a session
**When** Capability KV reprojection updates the entry
**Then** the agent's next read reflects the change within NFR-P3 lag; long-lived sessions should re-read before each operation (or watch their key — KV watch is acceptable for clients, just not for production projection pipelines).

**Given** the FR19 + FR34 north-star integration test
**When** a test harness (a) spawns a brand-new AI agent identity, (b) seeds a NEW operation type meta-vertex post-bootstrap, (c) grants the agent the new operation via role-permission update, (d) waits for Capability KV reprojection
**Then** the agent: reads its Capability KV entry, discovers the new operation in `platformPermissions[]`, reads the operation's meta-vertex including descriptive aspects, constructs a payload conforming to inputSchema, submits the operation, receives a successful response — all without any test-harness-side hardcoded knowledge of the operation; this end-to-end flow is the Phase 1 north-star integration test.

**And** the test harness emits to Health KV `health.fr19.cold-start-test` with `passed: true` and timestamp on success.

---

### Story 5.3: Compensating Operation & DDL Rollback (FR53, Phase 1 Gate 4)

As a platform operator,
I want every capability change to be revertible via a compensating operation through the same write path — with no platform restart, no data surgery, no out-of-band intervention — so that capability evolution is safe to attempt and reversible by design.

**Architectural grounding:** Every capability change is a single operation through Processor. Reversal is the inverse operation. No "rollback machinery" — contract asserted by test.

**Recommended model tier:** Opus
**Estimated token budget:** ~130K (input + output)

**Acceptance Criteria:**

**Given** the contract surface for "capability change" is documented
**When** the documentation is reviewed
**Then** `docs/components/processor.md` (per the Documentation Layering Rule — this is a Processor-internal classification of which operations carry the `compensatingOperation` field) names the operation categories per FR53:
- DDL meta-vertex creation, update, or tombstone (via `CreateMetaVertex` / `UpdateMetaVertex` / `TombstoneMetaVertex` ops; this includes both DDL meta-vertices and Lens meta-vertices since the kernel collapses them under one meta-DDL — post-4.7)
- Role-permission grant creation or revocation (rbac-domain package ops)
- Identity-role assignment change (rbac-domain package ops)
- Service availability topology change (Phase 2 — service DDL not in Phase 1 scope)

Each category names its **forward operation type** AND its **compensating operation type** (e.g., `CreateMetaVertex` ↔ `TombstoneMetaVertex`). Pairing is exhaustive. (Original AC text directed this to `data-contracts.md`; superseded by the 2026-05-19 Documentation Layering Rule.)

**Given** an operator submits a forward operation that effects a capability change
**When** the operation commits
**Then** the response includes a `compensatingOperation` field describing how to revert: operation type, payload shape, specific vertex keys/revisions; operator can construct a reversal at any future point without remembering original change details.

**Given** an operator submits a compensating operation
**When** it commits
**Then** state returns to pre-forward state (modulo intervening operations); subsequent reads see reverted state; Capability KV reprojection updates within NFR-P3 lag; no platform restart or out-of-band intervention required.

**Given** intervening operations may have occurred between forward and compensating
**When** the compensating operation runs
**Then** it does NOT blindly revert — asserts entities are still in post-forward state; if further modified, either (a) rejects with `CompensationConflict` and surfaces the diverged state for operator review (Phase 1 default), or (b) proceeds with merge policy if operator passes `force: true`; uses standard NATS revision condition mechanism (Story 1.7).

**Given** the Phase 1 Gate 4 integration test
**When** the test runs
**Then** it executes:
1. Bootstrap completes; rbac-domain + identity-domain packages installed (post-4.7); baseline captured
2. Submit forward `CreateMetaVertex` for a new DDL meta-vertex with `canonicalName: "testCapability"` and `class: "meta.ddl.vertexType"`, written to `vtx.meta.<NanoID>` (NanoID generated by Starlark), with all five descriptive aspects from Story 5.1 attached
3. Verify the operation declared in `testCapability` DDL is invokable: install permission + grant via rbac ops, submit the declared op, receive success
4. Submit compensating `TombstoneMetaVertex` targeting `vtx.meta.<NanoID>` from step 2 (operator retrieves NanoID from step 2's response or via canonicalName lookup)
5. Verify the operation type is no longer invokable (rejected at step 6 DDL validation with `OperationTypeTombstoned` or similar — the DDL cache misses on the canonical name)
6. Verify Capability KV reprojections removed the operation from `platformPermissions[]`
7. Verify no Refractor or Processor restart occurred (consumer sequence positions held; no `make down`/`make up`)
8. Repeat assertions independently for a Lens meta-vertex change (`CreateMetaVertex` with `class: "meta.lens"`, then `TombstoneMetaVertex`)

**Given** Gate 4 must be observable
**When** the integration test passes
**Then** Health KV records `health.gates.phase1.gate4` as `passed: true` with timestamp; runnable standalone via `make test-rollback`.

**And** `docs/components/processor.md` lists every Phase 1 capability-change operation pairing in a table for operator reference (per the Documentation Layering Rule — was originally a `data-contracts.md` addendum).

---

**Epic 5 Summary:** 3 stories | FRs covered: FR19, FR34, FR53 | Phase 1 Gate 4 (Compensating op / DDL rollback test) delivered.

---

## Epic 6: Developer Experience & "Hello Lattice"

**Goal:** A developer can go from `git clone` to a working vertical slice — one entity type, one Starlark rule, one Lens projection, one AI traversal query — in under 60 minutes. The canonical reference implementation ("Hello Lattice") serves simultaneously as integration test suite, onboarding tutorial, and live demo.

**FRs covered:** FR43, FR44, FR45, FR46, FR48, FR52, FR55
**Phase 1 gates delivered:** "Hello Lattice" < 60 min from `git clone`, verified by external tester (Gate 5)

**Architectural grounding:**
- `make up` already provisions NATS + Postgres + bootstrap (Story 1.3)
- Health KV emissions already established across components — Epic 6 confirms completeness, doesn't introduce new mechanisms
- FR48 single-cell single-NATS-server acceptable per NFR-R6; multi-cell isolation is Phase 3
- "Hello Lattice" is the integration validator for the entire Phase 1 surface

> **Model tier guidance:** All Epic 6 stories are Sonnet — bounded scope against established contracts.

---

### Story 6.1: Lattice CLI Tool (FR45)

As a developer or operator,
I want a unified CLI tool to submit operations, inspect graph state, query projection surfaces, and read platform health — without a browser client and without writing custom NATS code — so that all Phase 1 workflows are accessible from the terminal.

**Recommended model tier:** Sonnet
**Estimated token budget:** ~120K (input + output)

**Acceptance Criteria:**

**Given** the `lattice` CLI binary is built and on PATH
**When** the developer runs `lattice --help`
**Then** the help text lists at minimum the following command groups, each with subcommands:

| Command Group | Subcommands | Purpose |
|---|---|---|
| `lattice op` | `submit`, `status <requestId>`, `trace <requestId>` | Submit operations, check commit status, retrieve auth-trace |
| `lattice graph` | `read <key>`, `walk <startKey> [--depth N]`, `keys <prefix>` | Direct Core KV reads, traversal, listing — debugging only (per P5) |
| `lattice lens` | `list`, `activate <file>`, `deactivate <key>`, `lag` | Lens lifecycle + lag inspection |
| `lattice query` | `postgres <sql>`, `cap <actorKey>` | Postgres queries, Capability KV reads |
| `lattice health` | `summary`, `component <name>`, `gates` | Health KV summary, per-component, gate statuses |
| `lattice identity` | `create-unclaimed`, `claim` | Identity-domain package operations (Story 4.7). `merge` lives under `lattice candidates` (see next row) post-4.6 redesign |
| `lattice candidates` | `list`, `merge <primary> <secondary>` | Identity-hygiene package — reads from Duplicate Candidates Lens KV; operator-only grants per package install |
| `lattice auth-trace` | `<requestId>` | Story 3.5 three-plane auth trace |
| `lattice bootstrap` | `verify`, `inspect` | Story 1.3 verification |

**Given** any subcommand executes
**When** an error occurs
**Then** the error is human-readable, includes `requestId` if applicable, exits non-zero; `--output json` flag available for scripting.

**Given** the CLI submits an operation
**When** the operation enters Processor write path
**Then** no CLI-specific code path exists in Processor — CLI is just an operation submitter; credentials configured via `lattice config set-credential`; credentials stored per local-dev convention (file-based — Phase 2+ may add KMS-backed).

**Given** the CLI is the developer's primary interface in Phase 1
**When** the developer uses it for "Hello Lattice"
**Then** every step (entity create, rule activate, Lens activate, AI traversal) is achievable via CLI; the tutorial in Story 6.4 references these specific commands.

**And** integration tests cover at least one happy-path test per command group; an end-to-end test verifies the full "Hello Lattice" slice completes using only `lattice` CLI.
**And** the CLI binary is built and embedded in `make up`.

---

### Story 6.2: Health KV Schema & Completeness (FR46, FR52)

As a platform operator,
I want the complete Health KV emission surface documented and verified — projection lag per Lens, stream consumer status, projection errors, component availability — so that the operator console (Phase 2+) and direct CLI reads have a stable, complete observability surface.

**Recommended model tier:** Sonnet
**Estimated token budget:** ~90K (input + output)

**Acceptance Criteria:**

**Given** all components emit to Health KV
**When** the Health KV schema is consolidated
**Then** `data-contracts.md` Contract #5 is extended with a complete inventory of emission keys for Phase 1, organized by component:

| Component | Key Pattern | Frequency | Source Story |
|---|---|---|---|
| Processor | `health.processor.<instance>` | ≥ 10s | Story 1.8 |
| Processor | `health.processor.<instance>.step3-latency` | per-op | Story 3.3 |
| Processor | `health.processor.<instance>.cap-staleness` | on stale | Story 3.3 |
| Processor | `health.processor.<instance>.auth-trace.<requestId>` | per denial | Story 3.5 |
| Refractor | `health.refractor.<instance>` | ≥ 10s | Story 2.1 |
| Refractor | `health.refractor.<instance>.lens.<lensId>` | ≥ 10s | Story 2.1 |
| Refractor | `health.refractor.<instance>.lens.capability.*` | continuous | Story 3.2 |
| Bootstrap | `health.bootstrap.*` | one-shot | Story 1.3 |
| Gates | `health.gates.phase1.gate<N>` | on pass | Stories 1.10, 3.7, 5.3, 6.4 |
| Alerts | `health.alerts.<severity>.*` | event | Multiple |

**Given** the schema is documented
**When** a component is added or modified
**Then** any new emission key must be added to Contract #5 inventory; CI runs a "Health KV emission completeness test" — starts all Phase 1 components, runs 30s, asserts every documented non-event-driven key has at least one record.

**Given** an operator wants a single overall health view
**When** they run `lattice health summary`
**Then** the CLI aggregates: each gate status (1-5), each component's freshness, each Lens's lag, recent alerts within last hour, overall green/yellow/red derived from configurable thresholds.

**Given** Phase 2+ task observability keys are reserved
**When** Contract #5 is extended
**Then** schema reserves `health.weaver.*` and `health.loom.*` namespaces; Phase 1 does not emit but reservation documented for forward stability.

**And** integration test: completeness test green, alert emission visible in summary, threshold-based status computation.
**And** NFR-O3 conformance explicitly verified by reading Contract #5 inventory.

---

### Story 6.3: Deployment Isolation Specification (FR48)

As a platform operator,
I want the Phase 1 deployment isolation model documented — each operator deployment runs its own NATS cluster with its own data and event streams, no cross-tenant access at the infrastructure level — so that the architectural claim is testable and the Phase 3 multi-cell scale-out path is clear.

**Architectural grounding:** Single-cell single-NATS-server is acceptable for Phase 1 per NFR-R6; multi-cell is Phase 3. This story is documentation + isolation tests.

**Recommended model tier:** Sonnet
**Estimated token budget:** ~70K (input + output)

**Acceptance Criteria:**

**Given** the Phase 1 deployment model is documented
**When** `lattice-architecture.md` is reviewed
**Then** a "Deployment Isolation" section exists describing:
- One operator deployment = one NATS cluster (single-server in Phase 1, clustered in Phase 2+)
- All Lattice components for a deployment connect to that deployment's NATS only
- Postgres Lens target is per-deployment (separate instance, no shared schema)
- Credential boundaries are per-deployment
- Multi-cell scale-out path: NFR-SC2 — same data model, no key changes; deployment isolation extends to cell isolation

**Given** an integration test asserts isolation
**When** the test runs
**Then** two `make up` instances are started on different ports (A and B); an AI agent bootstrapped in A cannot connect to B's NATS with A credentials; an operation in A is not observable in B's Refractor; Postgres instances are disjoint.

**Given** the multi-cell path is documented
**When** the architecture section is finalized
**Then** explicitly states: keys embed no cell identity (per NFR-SC2 — locked); Phase 3 multi-cell routes operations to specific cells via Gateway layer; per-cell Refractor and Capability KV remain per-cell.

**And** cross-deployment isolation test wired into `make test-isolation`.

---

### Story 6.4: "Hello Lattice" Reference Implementation (FR43, FR44, FR55, Phase 1 Gate 5)

As a developer evaluating Lattice,
I want a canonical reference implementation that takes me from `git clone` to a complete vertical slice — one entity type, one Starlark rule, one Lens projection, one AI traversal query — in under 60 minutes, so that I can verify the platform works as advertised and learn the development model by doing.

**Recommended model tier:** Sonnet
**Estimated token budget:** ~130K (input + output)

**Acceptance Criteria:**

**Given** a developer with no prior Lattice knowledge runs `git clone`
**When** they follow the README's "Hello Lattice" tutorial
**Then** the tutorial walks them through exactly five milestones, each completable in roughly 10-12 minutes:

1. **Setup (≤ 10 min)**: `make up` provisions NATS + Postgres + bootstrap (NFR-P7); developer reads `lattice health gates` and sees Phase 1 gates 1-5 as `passed: true` (or `pending`); `lattice bootstrap verify` confirms primordial state.

2. **Define one entity type (≤ 10 min)**: developer authors a "book" DDL meta-vertex (`class: meta.ddl.vertexType`, `canonicalName: "book"`, `permittedCommands: ["CreateBook"]`, descriptive aspects per Story 5.1) and submits via `lattice op submit --file book-ddl.yaml`; the platform writes it to `vtx.meta.<NanoID>` (3-segment vertex key per Contract #1 §1.5); developer verifies with `lattice graph read --canonical book`.

3. **Author one Starlark rule (≤ 10 min)**: developer writes Starlark validating `title` non-empty and emitting MutationBatch creating `vtx.book.<NanoID>`; submits as `UpdateMetaVertex` against the book DDL's `.script` aspect; submits `CreateBook` operation with `{title: "..."}`; verifies book vertex exists via `lattice graph read vtx.book.<NanoID>`.

4. **Author one Lens projection (≤ 10 min)**: developer authors a "books" Lens meta-vertex (`class: meta.lens`, `canonicalName: "books"`, `.spec` aspect with simple cypher query against the `simple` engine projecting books to a Postgres `books` table) and submits as `CreateMetaVertex`; the platform writes it to `vtx.meta.<NanoID>`; waits for activation (≤ 500ms NFR-P3); runs `lattice query postgres "SELECT * FROM books"` and sees the created book.

5. **One AI traversal query (≤ 10 min)**: developer runs `lattice query cap vtx.identity.<theirAgentKey>` to see AI agent's capability set including `CreateBook`; runs a provided script simulating an AI agent doing cold-start traversal (Story 5.2) and submitting a new `CreateBook`; confirms via Postgres query.

**Given** the tutorial includes assertions at each milestone
**When** the developer completes all five
**Then** total elapsed time (timer started at `git clone`, stopped at final query result) is under 60 minutes; verified by external tester per Gate 5.

**Given** the tutorial doubles as integration test (FR55)
**When** CI runs the full reference end-to-end
**Then** all five milestones exercised programmatically; CI test exits 0 on success and emits to `health.gates.phase1.gate5` as `passed: true`; failure modes: timeout (any milestone > 12 min in CI), assertion failure, component health degraded during run.

**Given** external tester verification is the Gate 5 acceptance condition
**When** the gate is closed
**Then** at least one external tester (not on core Lattice team) has completed the tutorial from `git clone` to final query on a clean machine; elapsed time < 60 min; feedback captured in `_bmad-output/planning-artifacts/gate5-external-tester-report.md`; blockers filed as Phase 1 follow-up or addressed before gate close.

**Given** FR55 specifies "Hello Lattice" serves three simultaneous roles
**When** Story 6.4 is considered complete
**Then** all three demonstrably satisfied:
- **Integration test**: CI runs the full implementation programmatically
- **Onboarding tutorial**: README walks a human through it
- **Live demo**: same implementation can be screen-shared as portfolio demonstration without modification

**And** the reference implementation lives at `examples/hello-lattice/` with its own README, sample DDL files, sample Starlark scripts, sample Lens definition, AI agent simulator script.

---

**Epic 6 Summary:** 4 stories | FRs covered: FR43, FR44, FR45, FR46, FR48, FR52, FR55 | Phase 1 Gate 5 ("Hello Lattice" < 60 min) delivered.

---

## Phase 1 Story Total

| Epic | Stories | Phase 1 Gates Delivered |
|---|---|---|
| Epic 1: Substrate & Trustworthy Write Path | 10 | Gate 1 (Starlark spike), Gate 2 (Bypass test suite) |
| Epic 2: Live Lens Projections (Materializer Morph) | 2 | — |
| Epic 3: Authorization & Security Perimeter | 7 | Gate 3 (Capability Lens adversarial test suite) |
| Epic 4: Identity & Member Lifecycle | 5 | — |
| Epic 5: AI-Native Platform Navigation | 3 | Gate 4 (Compensating op / DDL rollback) |
| Epic 6: Developer Experience & "Hello Lattice" | 4 | Gate 5 ("Hello Lattice" < 60 min) |
| **Total** | **31 stories** | **5 gates** |

All 38 Phase 1 FRs covered. All 5 Phase 1 gates assigned. 20 Phase 2+ FRs explicitly deferred and tracked.

---


---

## Phase 1.5: Hardening Block

*(Relocated from the Epic List 2026-06-03 — shipped history consolidated into the Phase 1 shard. A one-line pointer + the Phase-2 prerequisite note remain in [index.md](./index.md).)*

Post-Phase-1 remediation, not new capability. Triggered by (a) the `bmad-code-review` adversarial pass having run only on Epics 5–6 originally, and (b) Gate 5 (Hello Lattice) shipping partial with M4–M6 deferred behind three architectural gaps. A full six-component CR sweep (Refractor, Bootstrap/Kernel, Core KV, Processor, AI-agent, Capability packages) was run; inline-fixable findings are already merged (CI-green). Seven stories carry the larger items. Closes the substrate-direct install pattern (installs route through the Processor), hardens the write-path and kernel meta-DDL contracts, makes capability auth freshness deterministic, re-enables Gate 5 to a full pass, and freezes contract shapes behind a conformance suite. **Prerequisite for Phase 2.** See `sprint-change-proposal-2026-05-28.md` and the six `phase-1.5-cr-*.md` reports.
**Stories:** 1.5.1 Substrate write-path contracts · 1.5.2 DDL tombstone coherence (M6) · 1.5.3 UpdateMetaVertex expansion · 1.5.4 Capability auth freshness coherence · 1.5.5 Route package installs through Processor (M5) · 1.5.6 Re-enable Hello Lattice M4–M6 + Gate 5 full pass *(SUPERSEDED — the M5 functional blockers were not an atomic-publish storm; closed instead by 1.5.8 + 1.5.9)* · 1.5.7 Contract conformance suite + freeze · 1.5.8 Capability-lens aspect CDC fan-out · 1.5.9 Lens property model + explicit aspect navigation *(closed M5 / Gate 5 full pass)* · **1.5.10 Transactional event outbox** *(pre-Phase-2 hardening — REQUIRED before Loom/Weaver; see below)* · **1.5.11 Publisher relocation to outbox + 9-step commit-path renumber**
**Gates delivered:** Gate 5 full pass (M1–M6, closed by 1.5.8 + 1.5.9); contract conformance suite green
**Phase 2 readiness:** stories 1.5.1–1.5.5, 1.5.7–1.5.9 CR'd + done · Gate 5 `passed:true` · conformance suite green · substrate-direct install grep-clean · **Story 1.5.10 (transactional event outbox) shipped** — event fidelity is a hard prerequisite for Phase 2 orchestration

**Story 1.5.10 — Transactional event outbox (pre-Phase-2 hardening, gates Phase 2):** `core-events` are not CDC — they are intentional, declared-schema events Loom/Weaver depend on. The Phase 1 redelivery path re-derives the EventList by reconstructing events from Core KV keys (`RebuildEventListFromClasses`, best-effort), which is not equal to what the Starlark script actually returned. Replace with a transactional outbox: persist the script-returned `EventList` as part of the step-8 atomic batch (on the `op` tracker vertex); a durable consumer publishes from that persisted record to `core-events`, acking only on confirmed publish. Redelivery then republishes the *real* events. Architecture detail: `lattice-architecture.md` → Commit Path → "Transactional outbox". Removes the best-effort reconstruction entirely.
