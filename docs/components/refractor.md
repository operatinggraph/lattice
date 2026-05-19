# Refractor

**Component reference** | Audience: implementers + architects | Last verified: 2026-05-19

---

## Overview

Refractor projects Core KV state into derived KV buckets and Postgres tables
via continuously-running Lens definitions. Lenses are openCypher queries
(full engine in production since Story 3.1b-ii) that read from Core KV +
Adjacency KV and write to per-lens target adapters. This is the **read side**
of Lattice: Processor writes to Core KV, lenses derive queryable projections.
Refractor does not write to Core KV — it produces Capability KV, per-lens
target buckets, Postgres rows, Health KV signals, and audit/metrics subjects.

> **Phase 1 carry**: the `materializer` token is present throughout the
> codebase — subjects (`materializer.audit.*`, `materializer.metrics.*`,
> `materializer.control`), package-level constants, and legacy JetStream
> consumer machinery. Story 2.4a will evict these tokens and rename to
> `lattice.refractor.*`. Do not clean up the `materializer` token in Phase 1
> stories; it is tracked as a named carry.

---

## What this component owns

| Path | Role |
|------|------|
| `internal/refractor/` | All projection engine sub-packages (13 packages) |
| `cmd/refractor/` | Binary entry point; wires engine, consumer, pipeline, adapter, control, health |

Key sub-packages:

| Sub-package | Role |
|-------------|------|
| `pipeline/` | `Pipeline` struct; drives per-lens CDC-event → evaluate → adapt loop; `LatencyRingBuffer` (128-sample window) |
| `lens/` | `CoreKVSource` (watches `vtx.meta.>`, routes `meta.lens` class to loader); `Rule` type; `translateSpec` from `LensSpec` to `Rule`; engine selection via registry |
| `adapter/` | `Adapter` interface; `nats_kv` adapter; Postgres adapter; `PoolManager` for Postgres connection pooling |
| `adjacency/` | Adjacency KV read helpers |
| `consumer/` | `Bootstrapper` (builds adjacency index from link CDC events); `Manager` (manages per-lens durable JetStream consumers) |
| `control/` | `Service` — control plane; currently on raw `nc.QueueSubscribe` at subject `materializer.control` (Story 2.4b migrates to NATS Services) |
| `health/` | `LatticeHeartbeater`; `Reporter`; `AuditWriter` (subjects `materializer.audit.<ruleId>`); `LagPoller` (subjects `materializer.metrics.<ruleId>`) |
| `ruleengine/` | Registry + engine interfaces; `simple/` (v1 legacy parser); `full/` (openCypher via ANTLR4) + `full/cypher/` (generated lexer/parser) |
| `failure/` | Failure-tier classification; retry / DLQ routing |
| `subjects/` | Centralizes all subject name construction (includes current `materializer.*` tokens — Story 2.4a carry) |
| `fixture/` | Test fixtures and primordial bootstrap data |
| `config/` | Configuration types |
| `capabilityenv/` | Wraps executor RETURN rows into Contract #6 §6.2 Capability KV envelopes |

---

## In-contracts (what it consumes)

| Contract | Source | Notes |
|----------|--------|-------|
| **Core KV CDC events** | `kv.Watch` on `vtx.meta.>` (lens defs) + durable JetStream consumer on `KV_core-kv` backing stream (all mutations) | Story 2.4b will unify both onto the durable-consumer pattern; for now both watch patterns coexist |
| **Lens meta-vertices** | Core KV `vtx.meta.<NanoID>` with `class: meta.lens` and a `.spec` aspect | The `spec` aspect carries: `id`, `canonicalName`, `targetType`, `targetConfig`, `cypherRule`, `outputSchema`, `engine` (optional). Engine absent = `simple`-then-`full` fallback; `"full"` = full engine; `"simple"` = simple engine. |
| **Adjacency KV** | `refractor-adjacency` bucket | Refractor's internal inbound-link index, built by `consumer/bootstrap.go` from every `lnk.*` CDC event; two directional entries per edge. EdgeID == link key (Story 3.2b). The adjacency is the inbound-link lookup index for the cypher executor. |

---

## Out-contracts (what it produces)

| Artifact | Destination | Notes |
|----------|-------------|-------|
| **Capability KV** (Contract #6 §6.2) | `capability-kv` bucket | Produced by the bootstrap-seeded Capability Lens; key pattern `cap.identity.<actorId>` (Phase 1 shape; generalized to `cap.<actorType>.<id>` post-Story 4.7). Consumed by Processor's step-3 `CapabilityAuthorizer`. |
| **Per-lens target KV buckets** | Bucket name per `LensSpec.targetConfig.bucket` | e.g. `duplicate-candidates` post-Story 4.6 (identity-hygiene package's Duplicate Candidates Lens). Created on demand if not pre-provisioned. |
| **Postgres rows** | Target table per `LensSpec.targetConfig.table` | For SQL-target lenses; schema managed by `ensurePostgresTable` idempotent DDL. |
| **Health KV signals** (Contract #5) | Health KV `health.refractor.<instance>.lens.<canonicalName>` | Per-lens latency snapshots (p95, p99, mean, count from `LatencyRingBuffer`); consumer lag; per-instance heartbeat every 10s (Story 3.2b). |
| **Audit subjects** | `materializer.audit.<lensId>` | One `AuditEntry` per projection (Story 2.4a carry — will rename to `lattice.refractor.audit.*`). |
| **Metrics subjects** | `materializer.metrics.<lensId>` | Consumer lag on `LagPoller` interval (Story 2.4a carry — will rename to `lattice.refractor.metrics.*`). |
| **Control plane responses** | `materializer.control` via `nc.QueueSubscribe` | Handles JSON control requests (list lenses, force re-project, etc.). Story 2.4b migrates to NATS Services framework at `lattice.ctrl.refractor.<lensId>.<op>`. |

---

## Rule engine

Refractor has two engine implementations. The engine is selected per lens via
the `engine` field in the `LensSpec`. Selection logic lives in
`internal/refractor/ruleengine/` (`Registry.SelectForLens`).

### Simple engine (`ruleengine/simple/`)

- v1 Materializer-derived parser; custom recursive-descent implementation
- Production-stable for legacy fixtures that predate the full engine
- Use only for legacy fixtures; do not write new lenses targeting the simple engine
- Does not support Levenshtein UDFs (Story 4.6 adds UDFs to the full engine only)

### Full engine (`ruleengine/full/`)

- openCypher parser via `antlr4-go/antlr/v4 v4.13.1` runtime
- Grammar vendored from `jtejido/go-opencypher` (vendored 2026-05-15)
- `full.Engine.Parse` — lexes + parses via generated `cypher.CypherLexer` / `cypher.CypherParser`; walks AST with `newASTVisitor`; returns `*CompiledRule`
- `full.Engine.ExecuteWith` — evaluates the compiled query against Core KV + Adjacency KV; produces projection rows
- **Production status**: in production since Story 3.1b-ii; the bootstrap-seeded Capability Lens uses `engine: "full"` and serves as the canary for full-engine production wiring
- **Dependency pin**: `cmd/refractor/main.go:191` constructs `full.New()` and registers it; `startPipeline` routes based on `r.ResolvedEngine == ruleengine.EngineFull`

### Engine selection algorithm

1. `LensSpec.engine` field is inspected at spec load time (in `translateSpec`)
2. `ruleengine.Registry.SelectForLens` is called with the spec's `RuleEngine` string:
   - `"full"` → full engine (Story 3.2a explicit selection)
   - `"simple"` → simple engine
   - `""` (absent) → try simple first; if simple parse fails, try full; `AttemptedEngines` records the trial sequence
3. `Rule.ResolvedEngine` is set to the winning engine name; `Rule.CompiledRule` holds the compiled AST
4. At runtime, `startPipeline` checks `r.ResolvedEngine` to decide which evaluate path to use

### Levenshtein UDFs (planned — Story 4.6)

Story 4.6 will extend the full engine's cypher executor with:
- `levenshteinDist(a, b) -> int` — pure edit-distance computation, O(N²)
- `levenshteinRatio(a, b) -> float` — normalized similarity in [0, 1]

These are pure, deterministic, side-effect-free and available only in the
full engine. The simple engine does NOT receive UDF support. They are NOT
present in the current Phase 1 codebase — this is a future extension point.

---

## Lens lifecycle

1. **Lens definition arrives** via Core KV mutation on `vtx.meta.<NanoID>` (vertex with `class: meta.lens`) + a `.spec` aspect
2. **`CoreKVSource`** watches `vtx.meta.>` via `kv.Watch`; routes entries with class `meta.lens` to the spec parser. CDC ordering is not guaranteed — if the `.spec` aspect arrives before its parent vertex, it is buffered in `pendingSpecs` until the parent vertex's class is observed
3. **`translateSpec`** converts `LensSpec` → `Rule`; engine selection via `Registry.SelectForLens`; `CompiledRule` populated
4. **`startPipeline`** (in `cmd/refractor/main.go`) constructs the adapter (opens or creates the target KV bucket / Postgres table), wires a `pipeline.Pipeline`, installs a `LatencyRingBuffer`, launches a `health.Reporter`
5. **`consumer.Manager`** creates a durable JetStream consumer on `KV_core-kv` backing stream filtered to the lens's source-key prefix
6. **Each CDC event** → `pipeline.Pipeline.HandleMessage` → engine evaluates → projection row(s) emitted → `EnvelopeFn` wraps row → adapter writes to target
7. **Latency** tracked in `pipeline.LatencyRingBuffer` (128-sample ring buffer, thread-safe). Per-mutation health signals via `LatticeHeartbeater.LensLatencyProvider`
8. **Lens spec update** → `CoreKVSource.updateCB` fires; `ClassifyUpdate` determines whether a hot-swap (query change only) or full pipeline restart is required
9. **Lens tombstone** (parent vertex deleted or `.spec` deleted) → pipeline drained, consumer removed, adjacency entries left in place (Phase 1 acceptable — tombstone re-projection is a Phase 2 carry)

---

## Refractor adjacency KV

The `refractor-adjacency` bucket is Refractor's internal secondary index for
graph traversal. It is built and maintained exclusively by Refractor; no other
component writes to it.

| Property | Value |
|----------|-------|
| Bucket name | `refractor-adjacency` |
| Builder | `consumer/bootstrap.go` (`Bootstrapper.Run`) — processes every `lnk.*` CDC event |
| Entry shape | Two directional entries per link: `adj.<type1>.<id1>.<linkName>` → list of `<type2>.<id2>` EdgeIDs |
| EdgeID | == link key (per Story 3.2b); consistent across adjacency + Core KV |
| Purpose | Inbound-link lookup index for the cypher executor (graph traversal without a global scan) |

Story 4.6 `MergeIdentity` reads adjacency for inbound link enumeration via a
new substrate helper `AdjacencyForNode(nodeKey) -> []EdgeID`. Until that
helper ships, the adjacency is consumed directly by the cypher executor only.

---

## Capability KV envelope (Contract #6 §6.2)

Built by `internal/refractor/capabilityenv/`. The envelope wraps the cypher
RETURN row into the canonical Capability KV shape.

### Phase 1 field set

| Field | Value |
|-------|-------|
| `key` | `cap.identity.<actorId>` (constructed from `vtx.identity.<id>` via `capabilityKey`) |
| `actor` | Vertex key of the actor (`vtx.identity.<id>`) |
| `version` | `"1.0"` (pinned for Phase 1) |
| `projectedAt` | RFC3339 timestamp of the projection |
| `projectedFromRevisions` | Map of `{actorKey: revision, lensDefKey: revision}`; used by Processor step 3 for freshness checks |
| `lanes` | `["default"]` (Phase 1; multi-lane projection is Phase 2) |
| `platformPermissions` | Array from cypher RETURN; `[]` if absent |
| `serviceAccess` | Array from cypher RETURN; `[]` if absent |
| `ephemeralGrants` | Array from cypher RETURN; `[]` if absent |
| `roles` | Array from cypher RETURN; `[]` if absent |
| `pendingReview` | `true` when identity state == `"flagged-for-review"` (Story 4.4); field omitted when `false` |

> **Post-Story 4.4 transient field**: `pendingReview` is deleted from the
> envelope by Story 4.6, which also removes the `flagged-for-review` state.
> The `stateReader` argument to `capabilityenv.NewWrapper` becomes a no-op
> after Story 4.6.

### Key prefix evolution

Phase 1 retains `cap.identity.` as the key prefix (per the 2026-05-19 design
decision). Post-Story 4.7 the prefix generalizes to `cap.<actorType>.` to
support non-identity actor types. The Processor's `CapabilityAuthorizer`
reads the Phase 1 prefix in current code.

---

## Principles (binding)

- **Lenses are the read path**: reads never go through the write path. Story 4.6 corrects the pre-correction Epic 4 pattern where `OperationReply.Detail` was being used as a query channel.
- **Every Core KV mutation must be observable** via at least one lens projection (NFR-P3 ≤500ms end-to-end latency target). The `LatencyRingBuffer` p99 is the primary instrument.
- **Lens output is overwrite-by-reprojection**: fabricated or stale KV writes in a lens target are corrected on the next reprojection event. This is the Story 3.7 Vector #1 defense. Phase 2 adds substrate-level write restriction to the lens target buckets.
- **Lens definitions live in Core KV vertices**, not in source code. The platform discovers them via the `vtx.meta.>` CDC stream. Seeding a new lens requires a `CreateMetaVertex` operation through the Processor write path.
- **openCypher full engine is canonical for new lenses**; the simple engine is legacy-fixture support only. Do not write new lens definitions targeting the simple engine.
- **`materializer.*` subject tokens are a named carry**: do not rename in Phase 1 stories; Story 2.4a owns the rename.

---

## What's deferred

| Feature | Phase | Notes |
|---------|-------|-------|
| Personal Lens / Secure Lens | Phase 2 | Story 2.2 gap analysis; requires per-identity lens scoping |
| Multi-cell lens routing | Phase 3 | Current pipeline is single-cell |
| Cross-instance latency aggregation | Phase 2 | Current `LatencyRingBuffer` is per-instance; no cluster-level rollup |
| Link-envelope tombstone re-projection | Phase 2 | Currently adjacency entries are left in place on tombstone; re-projection on tombstone is not triggered |
| Levenshtein UDFs in full engine | Story 4.6 | `levenshteinDist` + `levenshteinRatio` added to cypher executor by Story 4.6 |
| Levenshtein in simple engine | Never | Simple engine is legacy-only; no UDF investment planned |
| `materializer.*` token rename | Story 2.4a | Rename to `lattice.refractor.*`; tracked as a named Phase 1 carry |
| Control plane NATS Services migration | Story 2.4b | Currently raw `nc.QueueSubscribe` on `materializer.control` |
| Durable-consumer unification for `vtx.meta.>` watch | Story 2.4b | Currently `kv.Watch`; Story 2.4b migrates to durable JetStream consumer pattern |
