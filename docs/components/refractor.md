# Refractor

**Component reference** | Audience: implementers + architects | Last verified: 2026-06-03

---

## Overview

Refractor projects Core KV state into derived KV buckets and Postgres tables
via continuously-running Lens definitions. Lenses are openCypher queries (full
engine) that read from Core KV + Adjacency KV and write to per-lens target
adapters. This is the **read side** of Lattice: Processor writes to Core KV,
lenses derive queryable projections. Refractor does not write to Core KV — it
produces Capability KV, per-lens target buckets, Postgres rows, Health KV
signals, and audit/metrics subjects.

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
| `lens/` | `CoreKVSource` (durable consumer over `vtx.meta.>`, routes `meta.lens` class to loader); `Rule` type; `translateSpec` from `LensSpec` to `Rule`; engine selection via registry |
| `adapter/` | `Adapter` interface; `nats_kv` adapter; Postgres adapter; `PoolManager` for Postgres connection pooling |
| `adjacency/` | Adjacency KV read helpers |
| `consumer/` | `Bootstrapper` (builds adjacency index from link CDC events); `Manager` (manages per-lens durable JetStream consumers) |
| `control/` | `Service` — control plane on the NATS `micro.Service` framework; endpoints at `lattice.ctrl.refractor.<lensId>.<op>` |
| `health/` | `LatticeHeartbeater`; `Reporter`; `AuditWriter` (subjects `lattice.refractor.audit.<lensId>`); `LagPoller` (subjects `lattice.refractor.metrics.<lensId>`) |
| `ruleengine/` | Registry + engine interfaces; `simple/` (v1 legacy parser); `full/` (openCypher via ANTLR4) + `full/cypher/` (generated lexer/parser) |
| `failure/` | Failure-tier classification; retry / DLQ routing |
| `subjects/` | Centralizes all subject name construction (`lattice.refractor.*`, `lattice.ctrl.refractor.*`) |
| `fixture/` | Test fixtures and primordial bootstrap data |
| `config/` | Configuration types |
| `capabilityenv/` | Wraps executor RETURN rows into Contract #6 §6.2 Capability KV envelopes |

---

## In-contracts (what it consumes)

| Contract | Source | Notes |
|----------|--------|-------|
| **Core KV CDC events** | Durable JetStream consumer (`substrate.SubscribeKVChanges`) on the `KV_core-kv` backing stream | Both the all-mutations stream and the `vtx.meta.>` lens-def watch run on the same durable-consumer pattern; ack position persists across restarts so a restarted Refractor resumes rather than replaying from the start. |
| **Lens meta-vertices** | Core KV `vtx.meta.<NanoID>` with `class: meta.lens` and a `.spec` aspect | The `spec` aspect carries: `id`, `canonicalName`, `targetType`, `targetConfig`, `cypherRule`, `outputSchema`, `engine` (optional). Engine absent = `simple`-then-`full` fallback; `"full"` = full engine; `"simple"` = simple engine. |
| **Adjacency KV** | `refractor-adjacency` bucket | Refractor's internal inbound-link index, built by `consumer/bootstrap.go` from every `lnk.*` CDC event; two directional entries per edge. EdgeID == link key. The adjacency is the inbound-link lookup index for the cypher executor. |

---

## Out-contracts (what it produces)

| Artifact | Destination | Notes |
|----------|-------------|-------|
| **Capability KV** (Contract #6 §6.2) | `capability-kv` bucket | Produced by the bootstrap-seeded Capability Lens; key pattern `cap.<actorType>.<id>` (e.g. `cap.identity.<actorId>`), derived by stripping the `vtx.` prefix from the actor vertex key. Consumed by Processor's step-3 `CapabilityAuthorizer`. |
| **Per-lens target KV buckets** | Bucket name per `LensSpec.targetConfig.bucket` | e.g. `duplicate-candidates` (the identity-hygiene package's Duplicate Candidates Lens). Created on demand if not pre-provisioned. |
| **Postgres rows** | Target table per `LensSpec.targetConfig.table` | For SQL-target lenses. The adapter is **thin**: it upserts one column **per RETURN field** (`INSERT … (book_id, title) … ON CONFLICT DO UPDATE`) and issues no DDL. The target table is **provisioned out-of-band** (a migration), with columns matching the lens RETURN (key columns + projected fields). Delete projection is **mode-dependent** (`targetConfig.deleteMode`, default `hard`): the default hard delete issues `DELETE FROM` and needs only the key + projected columns; `deleteMode: soft` issues `UPDATE … SET is_deleted=true, deleted_at=NOW()` and **requires** the `is_deleted` / `deleted_at` columns. The Refractor does not create or alter the table. |
| **Health KV signals** (Contract #5) | Health KV `health.refractor.<instance>.lens.<canonicalName>` | Per-lens latency snapshots (p95, p99, mean, count from `LatencyRingBuffer`); consumer lag; per-instance heartbeat every 10s. |
| **Audit subjects** | `lattice.refractor.audit.<lensId>` | One `AuditEntry` per projection. |
| **Metrics subjects** | `lattice.refractor.metrics.<lensId>` | Consumer lag on `LagPoller` interval. |
| **Control plane** | `micro.Service` endpoints at `lattice.ctrl.refractor.<lensId>.<op>` | Handles JSON control requests (list lenses, force re-project, etc.) via the NATS Services framework. |

---

## Rule engine

Refractor has two engine implementations. The engine is selected per lens via
the `engine` field in the `LensSpec`. Selection logic lives in
`internal/refractor/ruleengine/` (`Registry.SelectForLens`).

### Simple engine (`ruleengine/simple/`)

- v1 Materializer-derived parser; custom recursive-descent implementation
- Production-stable for legacy fixtures that predate the full engine
- Use only for legacy fixtures; do not write new lenses targeting the simple engine
- Does not support Levenshtein UDFs (those exist in the full engine only)

### Full engine (`ruleengine/full/`)

- openCypher parser via `antlr4-go/antlr/v4 v4.13.1` runtime
- Grammar vendored from `jtejido/go-opencypher`
- `full.Engine.Parse` — lexes + parses via generated `cypher.CypherLexer` / `cypher.CypherParser`; walks AST with `newASTVisitor`; returns `*CompiledRule`
- `full.Engine.ExecuteWith` — evaluates the compiled query against Core KV + Adjacency KV; produces projection rows
- **Canonical engine for new lenses.** The bootstrap-seeded Capability Lens uses `engine: "full"`.
- **Wiring**: `cmd/refractor/main.go` constructs `full.New()` and registers it; `startPipeline` routes based on `r.ResolvedEngine == ruleengine.EngineFull`

#### `OPTIONAL MATCH … WHERE` null-restore semantics

When an `OPTIONAL MATCH` pattern matches real neighbors but a `WHERE` then excludes
**every** one of them, `applyMatch` preserves the anchor row with the optional
pattern variables bound null — the correct Cypher OPTIONAL MATCH semantics, for every
cypher. The null fallback is constructed from the source binding (`nullBindNewVars`,
shared with `matchPatterns`'s no-match branch), not recovered from the expansion set:
when the pattern matched only real neighbors, the expansion set holds no null row to
recover, so an anchor whose sole neighbor is WHERE-filtered must be null-restored
from the source. This is what makes a dedicated family-filtered `OPTIONAL MATCH …
WHERE` (e.g. the lease lens's `freshUntil` bgcheck match) safe: a no-fresh-match anchor
projects with the optional column null instead of dropping the row (a dropped
convergence row reads to Weaver as an entity deletion).

### Property model (how lens cypher reads a node)

A vertex's Core KV body carries the **envelope** (`key`, `class`, provenance,
`isDeleted`) and, by exception, a small `data` object for types that keep
business data on the vertex root (e.g. permissions: `perm.data.operationType`).
**Business data otherwise lives in aspects** — separate Core KV keys
`vtx.<type>.<id>.<localName>` whose body nests the value under `data`
(`canonicalName` → `data.value`, `description` → `data.text`). Vertices exist
mostly to walk links; aspects hold the data.

Lens cypher reads these **explicitly**, and the full engine's property resolver
(`executor.go` `resolveProperty`) disambiguates by presence in the root body:

| Cypher | Resolves to |
|---|---|
| `node.key`, `node.class` | vertex envelope (root body) |
| `node.data.<field>` | the vertex root `data` object (permissions only, by exception) |
| `node.<aspect>.data.<field>` | point-reads the aspect key `vtx.<type>.<id>.<aspect>` and navigates its body — e.g. `role.canonicalName.data.value` |

A name **present** in the root body returns that value; a name **absent** from
the root body is treated as an aspect reference and point-read (not a scan).
Only the first hop off a vertex resolves an aspect; the returned aspect body is
a plain map, so `.data.<field>` is ordinary map navigation. Authoring rule:
write the path the data actually lives at — `perm.data.operationType` (root),
`role.canonicalName.data.value` (aspect).

### Engine selection algorithm

1. `LensSpec.engine` field is inspected at spec load time (in `translateSpec`)
2. `ruleengine.Registry.SelectForLens` is called with the spec's `RuleEngine` string:
   - `"full"` → full engine
   - `"simple"` → simple engine
   - `""` (absent) → try simple first; if simple parse fails, try full; `AttemptedEngines` records the trial sequence
3. `Rule.ResolvedEngine` is set to the winning engine name; `Rule.CompiledRule` holds the compiled AST
4. At runtime, `startPipeline` checks `r.ResolvedEngine` to decide which evaluate path to use

### Levenshtein UDFs (full engine)

The full engine's cypher executor (`ruleengine/full/executor.go`) provides two
pure, deterministic, side-effect-free string UDFs:

- `levenshteinDist(a, b) -> int` — classical Wagner-Fischer edit distance, O(N²)
- `levenshteinRatio(a, b) -> float` — normalized similarity in [0, 1]

They are available only in the full engine; the simple engine does not support
UDFs. The identity-hygiene Duplicate Candidates Lens uses them to score
near-duplicate identities.

---

## Lens lifecycle

1. **Lens definition arrives** via Core KV mutation on `vtx.meta.<NanoID>` (vertex with `class: meta.lens`) + a `.spec` aspect
2. **`CoreKVSource`** consumes `vtx.meta.>` via the durable consumer; routes entries with class `meta.lens` to the spec parser. CDC ordering is not guaranteed — if the `.spec` aspect arrives before its parent vertex, it is buffered in `pendingSpecs` until the parent vertex's class is observed
3. **`translateSpec`** converts `LensSpec` → `Rule`; engine selection via `Registry.SelectForLens`; `CompiledRule` populated
4. **`startPipeline`** (in `cmd/refractor/main.go`) constructs the adapter (opens the target KV bucket / Postgres table), wires a `pipeline.Pipeline`, installs a `LatencyRingBuffer`, launches a `health.Reporter`
5. **`consumer.Manager`** creates a durable JetStream consumer on `KV_core-kv` backing stream filtered to the lens's source-key prefix
6. **Each CDC event** → `pipeline.Pipeline.HandleMessage` → engine evaluates → projection row(s) emitted → `EnvelopeFn` wraps row → adapter writes to target
7. **Latency** tracked in `pipeline.LatencyRingBuffer` (128-sample ring buffer, thread-safe). Per-mutation health signals via `LatticeHeartbeater.LensLatencyProvider`
8. **Lens spec update** → `CoreKVSource.updateCB` fires; `ClassifyUpdate` determines whether a hot-swap (query change only) or full pipeline restart is required
9. **Lens tombstone** (parent vertex deleted or `.spec` deleted) → pipeline drained, consumer removed, adjacency entries left in place (tombstone re-projection is a Phase 2 carry)

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
| EdgeID | == link key; consistent across adjacency + Core KV |
| Purpose | Inbound-link lookup index for the cypher executor (graph traversal without a global scan) |

Within Refractor the adjacency is consumed directly by the cypher executor for
inbound-link enumeration without a global `lnk.*` scan.

### Link fan-out on the capability pipeline

Most lenses only project on **vertex** CDC events; `pipeline.processMsg`
ack-and-skips link and aspect events. The capability pipeline is the exception:
it has an `ActorEnumerator` installed, and a pure link mutation (e.g.
`holdsRole`, `grantedBy`) changes an actor's authorization with **no
accompanying vertex change**. So on the actor-aware pipeline a `KindLink` CDC
event — create *and* tombstone (revocation) — drives a fan-out reprojection
(`evaluateLinkFanOut`): the link key is parsed into its two endpoint vertices,
affected actors are enumerated from **both** endpoints (union), and each is
reprojected through the same per-actor machinery as the vertex fan-out
(`reprojectActors`).

Because the dedicated adjacency `Bootstrapper` and the capability pipeline both
observe the same link event with no cross-consumer ordering guarantee, the
pipeline first **idempotently applies the link to the adjacency KV itself**
(via `adjacency.Build`, mirroring the bootstrapper's two directional events,
keyed by the link key as EdgeID) before enumerating. This guarantees the
reprojection cypher sees a consistent edge set and never races ahead of the
edge that triggered it; the bootstrapper's later `Build` for the same edge is a
no-op. A link whose endpoints reach no actor (e.g. a `book → author` link)
enumerates to the empty set and is a correct no-op.

---

## Capability KV envelope (Contract #6 §6.2)

Built by `internal/refractor/capabilityenv/`. The envelope wraps the cypher
RETURN row into the canonical Capability KV shape.

### Field set

| Field | Value |
|-------|-------|
| `key` | `cap.<actorType>.<id>` (constructed from the actor vertex key `vtx.<actorType>.<id>` via `capabilityKey`, which strips the `vtx.` prefix) |
| `actor` | Vertex key of the actor (`vtx.identity.<id>`) |
| `version` | `"1.0"` |
| `projectedAt` | RFC3339 **provenance** timestamp: the anchor actor vertex's `lastModifiedAt` (the committing op's timestamp per Contract #1 §1.3), bound into the cypher as `$projectedAt`. It is deterministic ("as-of input state") — replay/rebuild over the same vertex yields an identical value (no wall-clock churn). It is consumed only by monitoring + the Processor auth trace; the Processor does not compare it against any freshness ceiling. |
| `projectedFromRevisions` | Map of `{actorKey: revision, lensDefKey: revision}`; recorded as projection provenance and surfaced in the Processor auth trace (planes 2+3). Not a freshness gate. |
| `lanes` | `["default"]` (multi-lane projection is Phase 2) |
| `platformPermissions` | Array from cypher RETURN; `[]` if absent |
| `serviceAccess` | Array from cypher RETURN; `[]` if absent |
| `ephemeralGrants` | Array from cypher RETURN; `[]` if absent |
| `roles` | Array from cypher RETURN; `[]` if absent |

The `capabilityKey` derivation is actor-type-agnostic: any `vtx.<type>.<id>`
actor key projects to `cap.<type>.<id>`, so non-identity actor types (e.g.
service actors) are supported without code change.

---

## Rebuild & truncate semantics

`Pipeline.Rebuild(ctx, truncate)` resets a lens's durable consumer so the lens
re-projects from the start of its source stream. The optional truncate step
clears the target store first.

| Adapter / mode | `truncate` requested | Behavior |
|----------------|----------------------|----------|
| NATS-KV, unguarded | `false` | No truncate; the stream replay overwrites each key last-writer-wins. |
| NATS-KV, unguarded | `true` | `Truncate` purges every key in the bucket, then the stream replays into the empty bucket. (`Truncate` does what the flag promised — it is not a silent skip.) |
| NATS-KV, **guarded** | `false` or `true` | **Truncate is forced.** A guarded bucket's monotonic `projectionSeq` watermarks would reject the historical lower-seq replays against the live high-seq watermarks, leaving rejected-write holes. The pipeline detects guardedness via `Guarded()` (it never learns lens canonical names), purges the bucket — clearing the watermarks with the data — and logs at info that truncate was forced. The stream then replays from empty, the highest-seq write wins, and the steady state is identical to a from-scratch projection (Contract #6 §6.2). |
| Postgres (no `Truncater`) | `true` | Truncate is skipped with a warn (the adapter does not implement `Truncater`). |

`NatsKVAdapter.Truncate` purges each key (`Purge` per key) rather than deleting:
a purge drops prior revisions and leaves a delete marker, so a subsequent `Get`
returns `ErrKeyNotFound` and a guarded rebuild's first replay takes the
absent→`Create` path with no stale watermark in the way. The force keeps the
projection-write guard **on** across the rebuild — it is never bypassed, so the
monotonic ordering still holds: a stale retry-queue write carries its original
(lower) `projectionSeq` and is superseded by the higher-seq replay of the current
state. The post-rebuild **steady state therefore equals a from-scratch
projection** regardless of how a concurrent retry interleaves with the
truncate/replay — the guarantee is on the converged state, not on instantaneous
consistency mid-rebuild (while a guarded bucket is being rebuilt its keys are
transiently absent, which step-3 denies fail-closed). The retry queue is **not**
separately quiesced during the rebuild because it does not need to be: the guard
makes a racing stale write lose on its own.

---

## Capability-Lens health (operational backstop)

The Processor has no per-operation projection-freshness gate. The accepted
bounded-staleness window is backstopped *operationally* by the Refractor's
per-lens health, and (in future) by Gateway token revocation for hard
identity/session revocation. This subsection documents **what the
Capability-Lens pipeline emits today**.

The Capability Lens (`vtx.meta.lens.capability`, `engine: "full"`) is wired
through the same generic per-lens health path as every other lens — there is
**no Capability-Lens-specific liveness or lag signal, and no alerting**. The
signals it does emit:

| Signal | Source | Key / subject | Semantics |
|--------|--------|---------------|-----------|
| Per-lens status | `health.Reporter` | Health KV, keyed by the lens `ruleID` | `active` / `paused` / `rebuilding`, plus `errorCount`, `activeSequence`, `pauseReason`, `lastError`. Updated on lifecycle transitions and `RecordError`. |
| Consumer lag | `health.LagPoller` → `Reporter.SetConsumerLag` | `lattice.refractor.metrics.<lensId>` + the `consumerLag` field on the per-lens health entry | `NumPending` on the lens consumer, polled on an interval. |
| Per-lens latency | `pipeline.LatencyRingBuffer` → `LatticeHeartbeater.LensLatencyProvider` | `health.refractor.<instance>.lens.<canonicalName>` | p95 / p99 / mean / count of per-event projection latency (NFR-P3 instrument). |
| Instance heartbeat | `LatticeHeartbeater` | `health.refractor.<instance>` | 10s heartbeat with TTL purge (NFR-O1). |
| Audit | `health.AuditWriter` | `lattice.refractor.audit.<lensId>` | Per-projection audit append. |

**Known gap:** none of the above is Capability-Lens-aware, and there is **no
threshold/alert** that fires when the Capability Lens is `paused`, accumulating
`consumerLag`, or has stopped projecting (projector grossly behind). Detecting a
dead/lagging Capability projector today requires an operator to read these
generic signals and apply judgment. A dedicated Capability-Lens liveness alert
(and the Gateway token-revocation hard control) are the planned follow-ups;
until they land, the backstop for the absent freshness gate is operator-observed,
not automated.

---

## Principles (binding)

- **Lenses are the read path**: reads never go through the write path. The operation reply carries only commit-trace identifiers (`primaryKey`, `revisions`) — it is never a query channel (there is no arbitrary `detail` map, and the constraint is enforced in code).
- **Every Core KV mutation must be observable** via at least one lens projection (NFR-P3 ≤500ms end-to-end latency target). The `LatencyRingBuffer` p99 is the primary instrument.
- **Lens output is overwrite-by-reprojection**: fabricated or stale KV writes in a lens target are corrected on the next reprojection event. This is the fabricated-KV-write defense. Phase 2 adds substrate-level write restriction to the lens target buckets.
- **Lens definitions live in Core KV vertices**, not in source code. The platform discovers them via the `vtx.meta.>` CDC stream. Seeding a new lens requires a `CreateMetaVertex` operation through the Processor write path.
- **openCypher full engine is canonical for new lenses**; the simple engine is legacy-fixture support only. Do not write new lens definitions targeting the simple engine.

---

## What's deferred

| Feature | Phase | Notes |
|---------|-------|-------|
| Personal Lens / Secure Lens | Phase 2 | Requires per-identity lens scoping |
| Multi-cell lens routing | Phase 3 | Current pipeline is single-cell |
| Cross-instance latency aggregation | Phase 2 | Current `LatencyRingBuffer` is per-instance; no cluster-level rollup |
| Link-envelope tombstone re-projection | Phase 2 | Currently adjacency entries are left in place on tombstone; re-projection on tombstone is not triggered |
| Substrate-level write restriction on lens target buckets | Phase 2 | Today the defense against fabricated lens-target writes is overwrite-by-reprojection only |
