# The Chronicler

**Component reference** | Audience: implementers + architects

**Status:** ✅ ratified (Fork C, 2026-07-02; re-ratified 2026-07-06). The event→row projection logic ships
today **hosted in Refractor** (`internal/refractor/eventlens` + `internal/refractor/lens/eventsource.go`,
wired in `cmd/refractor/main.go`). Per the ratified decision it is being **extracted** into the standalone
`cmd/chronicler` binary + `internal/chronicler` package; until that lands, the `internal/refractor`
locations are the current host. Design: `orchestration-history-read-model-design.md`. Board:
`chronicler-host-reconciliation`.

---

## Overview

The Chronicler is Lattice's **event-ledger materializer** — it materializes **append-only history** from
platform streams. It is the counterpart to Refractor: Refractor projects **convergent present state** from
the Core-KV change feed by evaluating openCypher lenses over Core KV + Adjacency; the Chronicler tails an
**event or ledger stream** as a raw sequential feed and records its **history**, with **no cypher, no
adjacency, and none of Refractor's evaluate machinery**.

That separation is *why it is a distinct component*, not a Refractor sub-package. Refractor's founding
charter **excludes event streams** — it consumes the Core-KV change feed via the NATS-KV watcher, **not**
`lattice-events` (brainstorm Stream-2 boundary). Hosting event-stream consumption inside the
auth-plane-critical Refractor binary would cross that charter line, so the Chronicler runs as its own small
binary (bridge / object-store-manager weight class), assembled from the shared `substrate.ConsumerSupervisor`,
the Refractor-style adapter SPI, and the `healthkv.Reporter` — all importable libraries needing no
co-residence.

Two modes:

1. **PROJECT** *(shipped, F1–F2)* — an event stream → a convergent history read model. The first definition
   is `loomFlowHistory`: `events.loom.>` → the `orchestration-history` NATS-KV bucket (one row per Loom
   flow instance).
2. **ARCHIVE** *(deferred, F3)* — a ledger stream verbatim → unlimited object-store storage (sequenced
   segments + a manifest carrying first/last-seq + a prev-segment chain, ack-only-after-durable-write). No
   auto-trim of the live stream in v1 — trimming behind the archived watermark is operator policy, separate
   from archiver correctness.

---

## What this component owns

| Path | Role |
|------|------|
| `cmd/chronicler/` *(target)* | Binary entry point; assembles the event→row pipeline from `ConsumerSupervisor` + the adapter SPI + `healthkv.Reporter`. Emits the `health.chronicler.<instance>` heartbeat. |
| `internal/chronicler/` *(target)* | The event→row projection engine — the extraction home of today's `internal/refractor/eventlens` (`manager.go`, `project.go`) + the `eventStream` lens-source primitive (`internal/refractor/lens/eventsource.go`). |

**Current host (pre-extraction):** the same logic lives in `internal/refractor/eventlens/` +
`internal/refractor/lens/eventsource.go`, activated by `startEventStreamPipeline` in
`cmd/refractor/main.go`. The extraction moves the host; the event→row model, the package-owned
definitions, the read-model targets, and the P5 read path all carry over verbatim, and Refractor keeps no
`LensSpec.Source`.

---

## In-contracts (what it consumes)

| Contract | Source | Notes |
|----------|--------|-------|
| **Event streams** | Durable JetStream consumer on `core-events` (`bootstrap.CoreEventsStreamName`), subject-filtered per definition (F1: `events.loom.>`; F2: `events.weaver.>`) | The `eventStream` lens-source primitive (`pkgmgr.SourceConfig`) declares the source stream + subject. History is materialized from already-published business events — the Chronicler is a pure downstream reader. |
| **Ledger streams** *(archive mode, deferred)* | `core-operations` (intent ledger), committed-delta | Tailed as a raw sequential feed for verbatim archival (F3). |
| **Definitions** | Package-owned, delivered like lenses | `orchestration-base` ships the `loomFlowHistory` definition (`packages/orchestration-base/lenses.go`, `Source: loomFlowHistorySource`) — same package-delivery model as Refractor lenses; definitions are not in source code. |

---

## Out-contracts (what it produces)

| Artifact | Destination | Notes |
|----------|-------------|-------|
| **History read models** | Per-definition NATS-KV bucket (F1: `orchestration-history`, `bootstrap.OrchestrationHistoryBucket`) | P5-read by apps + Loupe's Flows tab (`cmd/loupe/flows.go`) — consumers read the read-model bucket, **never** the source stream or `loom-state`. |
| **Archive segments** *(deferred)* | Object-store plane | Sequenced segments + manifest (F3). |
| **Health KV** (Contract #5) | `health.chronicler.<instance>` | Standard `LatticeHeartbeater` heartbeat — the signal Loupe's system-map `chronicler` node + `componentLiveness` already expect (see below). |

---

## Read / write-path posture

- **P5** — consumers read the **history read model** (the lens target bucket), never the source stream and
  never `loom-state`. Loupe's Flows tab is the reference consumer.
- **P2** — the Chronicler writes only its own read-model targets + its Health KV. It **never writes Core
  KV** and **submits no operations**: it materializes already-committed history, so it has no write path to
  guard.
- **Charter boundary** — it consumes event / ledger **streams** (the boundary Refractor deliberately
  excludes). It does **not** evaluate cypher, read Adjacency KV, or project convergent present state — those
  stay Refractor's. A definition that needs graph evaluation is a Refractor lens, not a Chronicler
  definition.

---

## Health & liveness

The Chronicler emits a standard Contract #5 heartbeat at **`health.chronicler.<instance>`** via a
`LatticeHeartbeater` (interval heartbeat with TTL purge, §5.4 `status`, §5.5 `issues[]`) — identical in
shape to Refractor's `health.refractor.<instance>`.

Loupe already carries the consumer side: its system map defines the `chronicler` node
(`cmd/loupe/systemmap.go`, currently `designAhead: true`) and its edges (`core-operations → chronicler`
archive, `core-events → chronicler` history, `core-kv → chronicler` CDC, `chronicler → object-store`
archive segments). Loupe reads component health at `health.<node-id>.<instance>`, so once the binary emits
`health.chronicler.<instance>` the node **flips live** (the `designAhead` absent-red suppression drops on
first-seen-alive) and `componentLiveness` fuses heartbeat freshness with the §5.4 status + worst §5.5 issue
severity — no Loupe change required.

---

## Fires

| Fire | Scope | State |
|------|-------|-------|
| **F1** | The component + PROJECT mode + `loomFlowHistory` (`orchestration-base` ships the definition) | Projection logic shipped; **host extraction to `cmd/chronicler` pending** (`chronicler-host-reconciliation`) |
| **F2** | Weaver history (`events.weaver.>`) | Follows F1 |
| **F3** | ARCHIVE mode + the `core-operations` intent-ledger archive (the PRD unlimited-retention path; FR51's substrate) | Deferred |

---

## What's deferred

| Feature | Notes |
|---------|-------|
| ARCHIVE mode | F3 — verbatim ledger archival to the object-store plane. |
| FR51 re-home | The intent-ledger + committed-delta archives re-home to the Chronicler on FR51 revive (retiring the "dedicated lens adapter" contortion). |
| #68 op-trace | A future Chronicler definition. |
| Live-stream auto-trim | Not in v1 — trimming behind the archived watermark is operator policy. |

---

## Principles (binding)

- **History is append-only.** The Chronicler never mutates or converges past state — it materializes the
  sequence as it arrives.
- **No cypher, no adjacency, no evaluate.** It tails a stream as a raw sequential feed. Graph evaluation is
  Refractor's job, not a Chronicler definition's.
- **Definitions live in packages**, discovered like lenses — not in source code.
- **P5 is the read path.** Consumers read the history read model; the source stream and `loom-state` are
  never a query surface.
- **Separate binary, by charter.** Event-stream consumption stays out of the auth-plane-critical Refractor
  binary.
