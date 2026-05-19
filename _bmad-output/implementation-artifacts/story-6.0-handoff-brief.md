---
title: Story 6.0 Implementation Handoff Brief
story: 6.0 — Component Reference Pages (Processor, Refractor, Substrate)
model_tier: Sonnet (locked)
token_budget: ~30K (docs only; for tracking — not a halt threshold)
session: Fresh docs-authoring session
architecture_lead: Winston
date: 2026-05-19
predecessor: PHASE-1-COURSE-CORRECTION.md (2026-05-19 audit)
---

# Story 6.0 — Component Reference Pages: Handoff Brief

## Your Role

Author three per-component reference pages plus an index, living at `docs/components/` in the repo root. These pages give Winston (the architecture lead) and future implementers a single place to consult before authoring per-story handoff briefs — replacing the current pattern where Winston has to re-derive component framing inside every brief.

`lattice-architecture.md:23` promised these pages but they were never authored. This story authors the three components that have shipped Phase 1 code: Processor, Refractor, Substrate. Gateway, Loom, Weaver, Vault remain placeholder entries in the index.

## 🔴 MANDATORY OPERATING RULES

- **No worktree.** Work directly in `/Users/andrewsolgan/Documents/GitHub/Lattice` against branch `main`. Verify with `pwd` at startup.
- **No commits, no pushes.** Stage your changes; Winston commits + pushes after review.
- **Docs only.** No Go code edits, no test edits.
- **Token budget is for tracking only.** Estimate ~30K. Record outer-telemetry actual at session close.
- **Model tier:** Sonnet only.
- **Source-of-truth for content**: the actual code under `internal/<component>/` is authoritative. If a page contradicts the code, the code wins; raise a CAR for non-trivial discrepancies (append to `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md`).
- **Andrew has authorized autonomous proceed.**

## Deliverables

Create the following files. Each is markdown, ~200–400 lines, audience = implementers + architects.

### 1. `docs/components/_index.md` (~80 lines)

Index page. Contents:

```markdown
# Lattice Components

This directory contains per-component reference pages. Each page documents
what the component owns, what it reads/writes, its in/out contracts,
failure modes, and applicable architectural principles. Implementers and
Winston (architecture lead) should consult the relevant component page
**before** authoring per-story handoff briefs.

Cross-component interface contracts live in
`_bmad-output/planning-artifacts/data-contracts.md`. Per-component
implementation choices live HERE. Per-package capability definitions live
under `packages/<package-name>/` (post-Story 4.6).

## Phase 1 components (shipped code)

- [Processor](./processor.md) — operation write path, 10-step commit pipeline
- [Refractor](./refractor.md) — lens projection engine + openCypher full
  engine + control plane
- [Substrate](./substrate.md) — NATS / KV / NanoID / atomic-batch primitives

## Phase 2+ components (no Phase 1 code yet — placeholders)

- Gateway — TBD (Phase 2+; not in Phase 1 scope)
- Loom — TBD (Phase 2+)
- Weaver — TBD (Phase 2+)
- Vault — TBD (Phase 2+ crypto-shred / PII)

## How to use these pages

When authoring a story handoff brief that touches a component, read that
component's page first to understand: what's already there, what
contracts it honors, what principles apply, what's deferred to Phase 2.
This replaces the previous practice of inlining component framing inside
each brief.

When adding a new principle, new contract surface, or new failure mode
to a Phase 1 component, update the page in the same commit as the code.
Drift between page and code is treated as a documentation bug.
```

### 2. `docs/components/processor.md` (~300 lines)

Sections (use these exact headings):

#### Overview
One paragraph: Processor is the sole authorized write surface to Core KV. Operations flow through a 10-step pipeline that consumes from JetStream `ops.<lane>.>`, validates against DDL, executes Starlark scripts in a sandbox, commits atomic batches, and publishes events. **No read API** — read-side concerns belong to Refractor (lenses) or direct KV reads via CLI.

#### What this component owns
List package paths and what they do:
- `internal/processor/` — pipeline logic
- `cmd/processor/` — binary entry point
- Key files: `commit_path.go`, `step1_consume.go` … `step10_ack.go`, `script_context.go`, `starlark_runner.go`, `starlark_builtins.go`, `ddl_cache.go`, `step3_auth.go`, `step3_denial_response.go`, `step3_auth_trace.go`, `envelope.go`, `reply.go`, `nfr_r1_test.go`

#### In-contracts (what it consumes)
- **Operation envelopes** (`data-contracts.md` Contract #2) from JetStream subjects `ops.<lane>.>`
- **DDL meta-vertices** (Contract #1) from Core KV — read into in-memory cache at startup, invalidated on `vtx.meta.*` mutations
- **Capability KV** (Contract #6) read by step 3 for authorization decisions
- **Adjacency KV** — not yet read by Processor (Phase 2 candidate for Story 4.6 merge op inbound-link enumeration; currently Refractor-only)

#### Out-contracts (what it produces)
- **Core KV mutations** (Contract #1 + Contract #3) — atomic batch via substrate
- **Events** (Contract #3 EventList) — published to `events.<class>` on `core-events` stream
- **Idempotency tracker entries** (Contract #4) at `vtx.op.<requestId>` with 24h TTL
- **Operation replies** (Contract #2 §2.4) on the per-op reply-to inbox
- **Health KV signals** (Contract #5) — `health.processor.<instance>` + step3-latency + cap-staleness + auth-trace + alerts
- **`OperationReply.Detail`** field carries **commit-trace data only** (mutation count, event count, revision, traceId) per the 2026-05-19 course correction. **NOT** business data; the Starlark script's `ResponseDetail` should be a small audit payload, not a query response. (The pre-correction Epic 4 usage as a business-data channel is being walked back in Story 4.6.)

#### The 10-step write path
Brief description of each step:
1. Consume — pull op envelope from JetStream consumer
2. Dedup — write tracker; on conflict, treat as duplicate (retry-safe)
3. Auth — capability KV lookup; dispatch per Contract #6 §6.4-6.8 (Story 3.3)
4. Hydrate — load `contextHint.Reads` + (post-4.4) `ScanPrefixes` from Core KV. NOTE: `ScanPrefixes` is on the deprecation list (Story 4.6 removes it; narrow `LinkScans` replaces it)
5. Execute — run DDL's `.script` aspect in Starlark sandbox; produce MutationBatch + EventList + ResponseDetail
6. Validate — DDL `permittedCommands` + `sensitiveAspectScope` + key pattern checks (Story 1.7, 1.9)
7. Materialize events — assign per-event NanoIDs
8. Commit — substrate.AtomicBatch on core-kv bucket (mutations + tracker)
9. Publish — events to core-events stream via substrate.PublishBatch
10. Ack — JetStream ack the original op

#### Starlark sandbox
- Globals: `state` (key-list reads from hydrated map), `op` (envelope view), `ddl` (resolved DDL), `nanoid` (PCG-seeded deterministic generator), `crypto` (sha256, sha256NanoID, constant_time_equal), `strings` (levenshtein* — on deprecation list, moves to cypher executor per Story 4.6)
- No `load`, no `os`, no `time`, no `http` — bypass-tested in Gate 2
- Sandbox violations → `SandboxViolation` step-5 error
- Timeout via context + thread.Cancel + step fallback

#### Capability change operations (FR53 — Story 5.3)
Table of forward / compensating operation pairs (this is the table Story 5.3 used to direct into `data-contracts.md`; now lives here per the Documentation Layering Rule):

| Category | Forward | Compensating |
|---|---|---|
| Meta-vertex management | `CreateMetaVertex` | `TombstoneMetaVertex` |
| Meta-vertex update | `UpdateMetaVertex` | `UpdateMetaVertex` (with prior payload) |
| Role-permission grant | `GrantPermission` | `RevokePermission` |
| Identity-role assignment | `AssignRole` | `RevokeRole` |

Note: this table represents the post-Story-4.7 shape. Pre-4.7 (current) state has more granular DDL operations (CreateRole, CreatePermission, etc.) which collapse into `CreateMetaVertex` after 4.7.

#### Failure modes
- ConflictError (step 8 revision-condition fail) → bubble back to caller
- DDLViolation (step 6) → terminate, nack with term
- SandboxViolation / ScriptError (step 5) → terminate, nack with term
- AuthDenied (step 3) → terminate, ack with denial detail (no retry)
- PublicationError (step 9) → retryable, exponential backoff up to 3 attempts

#### Principles (binding)
- Sole authorized write surface (NFR-S2)
- No bypass: every write goes through all 10 steps (Phase 1 Gate 2 verifies)
- Idempotent under retry: tracker provides dedup
- ContextHint is **surgical** (per-key Reads), not **bulk** (ScanPrefixes is being walked back)
- ResponseDetail is **commit-trace**, not **business-data**
- Starlark cannot directly touch NATS (Contract #3 §3.7)

#### What's deferred
- Read-side capability authorization (Phase 2)
- Multi-cell routing (Phase 3)
- Real NATS auth (Phase 2 — replaces stub-mode-for-MergeIdentity Phase 1 carry from Story 4.5)

### 3. `docs/components/refractor.md` (~350 lines)

Same shape, but for Refractor:

#### Overview
Refractor projects Core KV state into derived KV buckets and Postgres tables via continuously-running Lens definitions. Lenses are openCypher queries (full engine, post-3.1b-ii) reading from Core KV + Adjacency KV, writing to per-lens target adapters. This is the **read-side** of Lattice — operations write to Core KV, lenses derive queryable projections.

#### What this component owns
- `internal/refractor/` (13 packages)
- `cmd/refractor/` — binary
- Key sub-packages: `pipeline/`, `lens/`, `adapter/`, `adjacency/`, `consumer/`, `control/`, `health/`, `ruleengine/` (with `simple/` + `full/` + `full/cypher/`), `failure/`, `subjects/`, `fixture/`, `config/`, `capabilityenv/`

#### In-contracts (what it consumes)
- **Core KV CDC events** — currently via `kv.Watch` on `vtx.meta.>` (lens defs) and durable JetStream consumer on Core KV backing stream (vertex/aspect/link mutations). Story 2.4b unifies both onto the durable-consumer pattern.
- **Lens meta-vertices** at `vtx.meta.<NanoID>` with `class: meta.lens` and a `.spec` aspect carrying cypher source + adapter + engine selection (`simple` or `full`)
- **Adjacency KV** — Refractor's own internal lookup index (built by consumer/bootstrap.go from link envelopes)

#### Out-contracts (what it produces)
- **Capability KV** (`cap.<actorType>.<id>` per Contract #6 §6.2) — produced by the bootstrap-seeded Capability Lens
- **Per-lens target KV buckets** (e.g., `duplicate-candidates` post-Story 4.6 — produced by identity-hygiene package's Duplicate Candidates Lens)
- **Postgres rows** for SQL-target lenses
- **Health KV signals** (`health.refractor.<instance>.lens.<canonicalName>` per Story 3.2b)
- **Audit + metrics subjects** — currently `materializer.audit.<lensId>` and `materializer.metrics.<lensId>` (Story 2.4a renames to `lattice.refractor.audit.*` etc.)
- **Control plane responses** — currently `materializer.control` via raw `nc.QueueSubscribe`; Story 2.4b migrates to NATS Services framework with `lattice.ctrl.refractor.<lensId>.<op>` subjects

#### Rule engine
- `simple` engine — v1 Materializer-derived parser; production-stable for legacy fixtures
- `full` engine — openCypher with `antlr4-go/antlr/v4 v4.13.1` runtime, grammar vendored from `jtejido/go-opencypher` (2026-05-15)
- Engine selection per Decision #3 in Story 3.1a: explicit-simple / explicit-full / absent-fallback
- Bootstrap-seeded Capability Lens uses `engine: "full"` (Story 3.2a) and is the canary for full-engine production wiring
- Pin: `cmd/refractor/main.go:191` constructs `full.New()`; pipeline routes via `engineKind` field
- Levenshtein UDFs (Story 4.6) extend the cypher executor with `levenshteinDist(a, b) -> int` and `levenshteinRatio(a, b) -> float`. Pure / deterministic / O(N²). NOT available in the simple engine.

#### Lens lifecycle
1. Lens def arrives via Core KV mutation on `vtx.meta.<NanoID>` (class `meta.lens`)
2. `corekv_source.go` translates the spec aspect into a `lens.Rule` (engine resolved via `ruleengine.Registry`)
3. Pipeline started per lens; durable JetStream consumer on Core KV backing stream filtered to the lens's source-key prefix
4. Each CDC event → engine evaluates → projection emitted → adapter writes
5. Latency tracked in `pipeline.LatencyRingBuffer` (128-sample window per pipeline instance)
6. Per-mutation health signals via `LatticeHeartbeater`
7. On lens-spec tombstone: pipeline drained, consumer removed, adjacency entries left in place (Phase 1 acceptable)

#### Refractor adjacency KV
- Bucket: `refractor-adjacency`
- Built by `consumer/bootstrap.go` (Story 2.1b + 3.2b) from every `lnk.*` CDC event; emits two directional entries per edge
- EdgeID == link key per Story 3.2b — adjacency is the inbound-link lookup index for the executor
- Story 4.6 `MergeIdentity` reads adjacency for inbound link enumeration via a new substrate helper `AdjacencyForNode(nodeKey) -> []EdgeID`

#### Capability KV envelope (Contract #6 §6.2)
- Built by `internal/refractor/capabilityenv/`
- Wraps lens output in the canonical envelope shape (key prefix `cap.identity.<actorId>` for Phase 1; `cap.<actorType>.<id>` after kernel generalization in Story 4.7 — though Phase 1 retains `cap.identity.` per the 2026-05-19 design decision)
- Phase-1 fields: `actor`, `version="1.0"`, `projectedAt`, `projectedFromRevisions`, `lanes`, `platformPermissions`, `serviceAccess`, `ephemeralGrants`, `roles`
- Post-Story-4.4 transient field: `pendingReview` (set when state == flagged-for-review); **deleted by Story 4.6** along with the flagged-for-review state itself

#### Principles (binding)
- Lenses are the read path; reads never go through the write path (this is what Story 4.6 corrects in Epic 4)
- Every Core KV mutation must be observable via at least one lens projection (NFR-P3 ≤500ms latency)
- Lens output is overwrite-by-reprojection — fabricated KV writes are corrected on next reprojection (Story 3.7 Vector #1 defense; Phase 2 adds substrate-level write restriction)
- Lens definitions live in Core KV vertices (not in source code); the platform discovers them via the meta-vertex CDC stream
- openCypher full engine is canonical for new lenses; simple engine is legacy-fixture support only

#### What's deferred
- Personal Lens / Secure Lens (Phase 2 — Story 2.2 gap analysis)
- Multi-cell lens routing (Phase 3)
- Cross-instance latency aggregation (current LatencyRingBuffer is per-instance)
- Link-envelope tombstone re-projection (currently re-projection fires via adj-watch only)
- Levenshtein in the simple engine (only added to full per Story 4.6)

### 4. `docs/components/substrate.md` (~200 lines)

#### Overview
Substrate is Lattice's NATS / KV / NanoID primitive layer. Every other component depends on it; it has no upstream dependencies in Lattice (only the NATS Go client). Substrate's job is to expose typed, contract-aware operations: key shape construction + parsing, atomic batch publish, KV CRUD, NanoID generation. It is **not** a general NATS wrapper — only the operations Lattice components share live here.

#### What this component owns
- `internal/substrate/` — single Go package
- Key files: `doc.go`, `nanoid.go`, `keys.go`, `envelope.go`, `conn.go`, `kv.go`, `batch.go`, `errors.go`

#### Exported surface
- **NanoID**: `NewNanoID()`, `Alphabet` (58-char), `IsValidNanoID(string) bool`
- **Key construction**: `VertexKey(type, id)`, `AspectKey(vtxKey, localName)`, `LinkKey(type1, id1, linkName, type2, id2)` (callers determine ordering; substrate does not enforce younger-first or alphabetical-first)
- **Key parsing**: `ClassifyKey(key) -> Kind` (KindVertex / KindAspect / KindLink / KindUnknown), `ParseVertexKey`, `ParseAspectKey`, `ParseLinkKey`
- **Document envelope**: `NewDocumentEnvelopeAt`, `AspectEnvelope`, `LinkEnvelope`
- **Connection**: `Connect(opts)` returns `*Conn` exposing `NATS()` and `JetStream()` for callers needing raw access
- **KV**: `KVGet`, `KVPut`, `KVPutWithTTL` (Story 3.5), `KVListKeys` (Story 1.7), `KVDelete` (soft-delete pattern)
- **Atomic batch**: `(*Conn).AtomicBatch(ops, timeout)` — single-bucket, all-or-nothing, raw-NATS protocol per Story 1.1 spike
- **Publish batch**: `(*Conn).PublishBatch(ops, timeout)` — non-conditional JetStream batch for events (Story 1.8)
- **Errors**: `ConflictError`, `MissingError`, sentinel types

#### Post-Story-4.7 additions (planned)
- `(*Conn).AdjacencyForNode(nodeKey string) -> []string` — reads Refractor's adjacency KV bucket, returns list of EdgeIDs (= link keys) for inbound + outbound edges of the given node. Required by Story 4.6 `MergeIdentity` for inbound-link enumeration without a global `lnk.*` scan.
- `(*Conn).SubscribeKVChanges(bucket, keyPrefix, durableName) -> <-chan KVEvent` — durable JetStream consumer over the `KV_<bucket>` backing stream, filtered to the given prefix. Replaces `kv.Watch` in Refractor's `corekv_source.go` per Story 2.4b.

#### Principles (binding)
- Substrate exposes only operations that are **architecturally common across components**. NATS Services framework helpers, JetStream-consumer-management helpers, watch helpers that are component-specific belong in the component, not in substrate.
- Key shape validation is centralized in `ClassifyKey` + `IsValidNanoID`. No other code should re-implement these checks.
- All Lattice components consume substrate; substrate consumes only `nats-io/nats.go`.

#### What's deferred
- Real NATS auth (currently relies on inherited connection; Phase 2 adds account-level enforcement — Story 3.7 Vector #1 carry)
- Inner-package migration of Refractor packages to use substrate uniformly (Deviation 5 carry; Story 2.4b absorbs some of this)

## Sequence

**Phase A — `_index.md` (target ~3K tokens):**
1. Author `docs/components/_index.md` from the spec above. Confirm `docs/components/` is created (it's a new directory).

**Phase B — `processor.md` (target ~12K tokens):**
2. Read `internal/processor/commit_path.go`, `step3_auth.go`, `step5_execute.go`, `starlark_runner.go`, `envelope.go`, `reply.go` to verify factual claims.
3. Author `docs/components/processor.md`.

**Phase C — `refractor.md` (target ~10K tokens):**
4. Read `cmd/refractor/main.go`, `internal/refractor/pipeline/pipeline.go` + `evaluate.go`, `internal/refractor/lens/corekv_source.go`, `internal/refractor/ruleengine/full/full.go`, `internal/refractor/capabilityenv/envelope.go` to verify factual claims.
5. Author `docs/components/refractor.md`.

**Phase D — `substrate.md` (target ~5K tokens):**
6. Read `internal/substrate/doc.go`, `keys.go`, `batch.go`, `kv.go`, `conn.go` to verify the exported-surface table.
7. Author `docs/components/substrate.md`.

**Phase E — Closing (target ~3K tokens):**
8. Update token tracker Row 6.0 with self-estimate.
9. Append a Deliverable #5 closing summary to this brief.

## Required Verification

```bash
# No code touched; just verify the three pages exist + render
ls -la docs/components/
wc -l docs/components/*.md
# Spot-check at least one factual claim per page against the code it cites
```

No CI gates apply (docs-only). Winston runs `make verify-bootstrap` after commit to confirm no regression (paranoia).

## Deliverables Checklist

1. ✅ `docs/components/_index.md` — ~80 lines, points to the three component pages + reserves Gateway/Loom/Weaver/Vault placeholders
2. ✅ `docs/components/processor.md` — ~300 lines per the §3 spec above
3. ✅ `docs/components/refractor.md` — ~350 lines per the §4 spec above
4. ✅ `docs/components/substrate.md` — ~200 lines per the §5 spec above
5. ✅ Token tracker Row 6.0 updated; closing summary appended to this brief

## What 6.0 Is NOT

- Not Gateway / Loom / Weaver / Vault pages (no Phase 1 code exists for them)
- Not a re-author of `lattice-architecture.md` (the foundation doc stays; these pages are the consult-first layer it promised at line 23)
- Not a refactor of `data-contracts.md` (separate work; C2 of the course correction already did the §6.13 eviction)
- Not a sharding of `epics.md` (separate consideration; not in Phase 1)

## Escalation

Append to `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` for:
- Code contradicts the spec in this brief in a non-trivial way (factual error)
- Architectural principle in this brief disagrees with what the code does

Halt for:
- Stuck-loop pattern

## Closing

1. Verify all 5 deliverables
2. Closing summary as Deliverable #5

**DO NOT commit. DO NOT push.** Winston commits + pushes after review.
