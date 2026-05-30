# Lattice Reference Docs

Start here to navigate Lattice reference documentation. This tree contains durable reference material for implementers and AI agents. For BMad workflow artifacts (stories, epics, architecture decisions, retros), see `_bmad-output/`.

---

## Contracts — FROZEN

The seven Phase 1 data contracts are frozen. They define the exact wire shapes, key patterns, and behavioral rules the code implements. Do not paraphrase, alter field names, or reformat tables when consulting these.

| Doc | Description |
|-----|-------------|
| [contracts/_index.md](./contracts/_index.md) | Index page — preamble, authoring principle, links to all 7 contracts, document status |
| [contracts/01-addressing-and-envelope.md](./contracts/01-addressing-and-envelope.md) | Core KV key patterns (vtx/aspect/link), reserved types, document envelope, DDL lookup, permissive-by-default model, meta-DDL structure |
| [contracts/02-operation-envelope.md](./contracts/02-operation-envelope.md) | Operation envelope shape, lane semantics, reply contract, error codes, authContext dispatch |
| [contracts/03-mutation-batch-event-list.md](./contracts/03-mutation-batch-event-list.md) | Starlark return contract — mutation ops (create/update/tombstone), EventList, batch-internal consistency rules |
| [contracts/04-idempotency-tracker.md](./contracts/04-idempotency-tracker.md) | `vtx.op.<requestId>` shape, 24h TTL retention, dedup lifecycle |
| [contracts/05-health-kv.md](./contracts/05-health-kv.md) | Health KV convention — bucket/key pattern, document shape, status enum, heartbeat cadence |
| [contracts/06-capability-kv.md](./contracts/06-capability-kv.md) | Capability KV shape (SECURITY-CRITICAL) — per-actor projection, platformPermissions, serviceAccess, ephemeralGrants, scope enum |
| [contracts/07-primordial-bootstrap.md](./contracts/07-primordial-bootstrap.md) | `make up` seeding inventory, bootstrap config, idempotence, readiness gate |
| [contracts/08-package-install.md](./contracts/08-package-install.md) | Capability-package install/uninstall as Processor-routed kernel ops (`InstallPackage`/`UninstallPackage`) — thin-script/fat-manifest, guardrails, M5/B2 cache coherence, kernel protection |

---

## Components — Living reference

Per-component pages. Updated in the same commit as code changes. Drift between page and code is a documentation bug.

| Doc | Description |
|-----|-------------|
| [components/_index.md](./components/_index.md) | Component directory — how to use these pages, Phase 1 vs Phase 2+ components |
| [components/processor.md](./components/processor.md) | Processor — 10-step commit pipeline, lane consumers, DDL cache, auth stub, Starlark sandbox |
| [components/refractor.md](./components/refractor.md) | Refractor — Lens projection engine, openCypher full engine, control plane |
| [components/substrate.md](./components/substrate.md) | Substrate — NATS/KV primitives, NanoID generator, atomic-batch utilities, key builders |
| [components/_packages.md](./components/_packages.md) | Capability packages — package install path, DDL seeding, Phase 1 vs Phase 2 installer |
| [components/refractor-failure-tiers.md](./components/refractor-failure-tiers.md) | Refractor failure tier classification — Infrastructure/Structural/Terminal/Transient, plus privacy-critical and security-critical overrides |

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
