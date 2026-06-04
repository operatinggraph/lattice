# Lattice Reference Docs

Start here to navigate Lattice reference documentation. This tree contains durable reference material for implementers and AI agents. For BMad workflow artifacts (stories, epics, architecture decisions, retros), see `_bmad-output/`.

New to Lattice? Read the [project README](../README.md), then check the [architecture overview](./architecture-overview.md) for a full-platform diagram, and walk the [Hello Lattice tutorial](./hello-lattice.md).

---

## Contracts — FROZEN

The data contracts define the exact wire shapes, key patterns, and behavioral rules the code implements. Do not paraphrase, alter field names, or reformat tables when consulting these. Contracts #1–#7 are the original Phase 1 set; #8 and #9 were added in the Phase 1.5 hardening block; #10 is the Phase 2 orchestration surface.

| Doc | Description |
|-----|-------------|
| [contracts/README.md](./contracts/README.md) | Index page — preamble, authoring principle, links to all contracts, document status |
| [contracts/01-addressing-and-envelope.md](./contracts/01-addressing-and-envelope.md) | Core KV key patterns (vtx/aspect/link), reserved types, document envelope, DDL lookup, permissive-by-default model, meta-DDL structure |
| [contracts/02-operation-envelope.md](./contracts/02-operation-envelope.md) | Operation envelope shape, lane semantics, reply contract, error codes, authContext dispatch |
| [contracts/03-mutation-batch-event-list.md](./contracts/03-mutation-batch-event-list.md) | Starlark return contract — mutation ops (create/update/tombstone), EventList, batch-internal consistency rules |
| [contracts/04-idempotency-tracker.md](./contracts/04-idempotency-tracker.md) | `vtx.op.<requestId>` shape, 24h TTL retention, dedup lifecycle |
| [contracts/05-health-kv.md](./contracts/05-health-kv.md) | Health KV convention — bucket/key pattern, document shape, status enum, heartbeat cadence |
| [contracts/06-capability-kv.md](./contracts/06-capability-kv.md) | Capability KV shape (SECURITY-CRITICAL) — per-actor projection, platformPermissions, serviceAccess, ephemeralGrants, scope enum |
| [contracts/07-primordial-bootstrap.md](./contracts/07-primordial-bootstrap.md) | `make up` seeding inventory, bootstrap config, idempotence, readiness gate |
| [contracts/08-package-install.md](./contracts/08-package-install.md) | Capability-package install/uninstall as Processor-routed kernel ops (`InstallPackage`/`UninstallPackage`) — thin-script/fat-manifest, guardrails, cache coherence, kernel protection |
| [contracts/09-identity-claim-flow.md](./contracts/09-identity-claim-flow.md) | Identity claim flow (client-minted claim secret; Lattice never holds plaintext) + the frozen reply shape (closed `response` schema, `primaryKey`) |
| [contracts/10-orchestration-surfaces.md](./contracts/10-orchestration-surfaces.md) | Phase 2 orchestration surfaces (Loom/Weaver: task placement, target-Lens output, operational KV namespaces, ADR-51 scheduling subjects) |

---

## Components — Living reference

Per-component pages. Updated in the same commit as code changes. Drift between page and code is a documentation bug.

| Doc | Description |
|-----|-------------|
| [components/README.md](./components/README.md) | Component directory — how to use these pages, Phase 1 vs Phase 2 components |
| [components/processor.md](./components/processor.md) | Processor — 9-step commit pipeline, lane consumers, DDL cache, capability authorization, Starlark sandbox, transactional outbox |
| [components/refractor.md](./components/refractor.md) | Refractor — lens projection engine, openCypher full engine, Capability Lens, control plane |
| [components/substrate.md](./components/substrate.md) | Substrate — NATS/KV primitives, NanoID generator, atomic-batch + durable-consumer utilities, key builders |
| [components/_packages.md](./components/_packages.md) | Capability packages — Processor-routed install path, DDL/lens/permission seeding, authoring guide |
| [components/refractor-failure-tiers.md](./components/refractor-failure-tiers.md) | Refractor failure-tier classification — Infrastructure/Structural/Terminal/Transient + designed privacy/security supersession tiers |
| [components/loom.md](./components/loom.md) | Loom (Phase 2, design) — deterministic procedure engine: linear-sequence interpreter, rebuildable cursor |
| [components/weaver.md](./components/weaver.md) | Weaver (Phase 2, design) — convergence engine: target-as-Lens, 3-lane work stream, Two-Phase Nudge |

---

## Observability

| Doc | Description |
|-----|-------------|
| [observability/health-kv-schema.md](./observability/health-kv-schema.md) | Health KV key inventory for Phase 1 — authoritative key-level details per component, `lattice health summary` rollup semantics |

---

## Operations

| Doc | Description |
|-----|-------------|
| [operations/deployment-isolation.md](./operations/deployment-isolation.md) | Deployment isolation model — per-deployment NATS, Postgres, and bootstrap identity; no cross-deployment sharing |
