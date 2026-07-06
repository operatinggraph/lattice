---
status: 'complete'
contractsCompleted: [1, 2, 3, 4, 5, 6, 7]
contractsTotal: 7
completedDate: '2026-05-12'
project_name: 'Lattice'
user_name: 'Andrew'
date: '2026-04-11'
purpose: 'Frozen data contracts for Phase 1 implementation. AI agents implement from this document.'
relatedDocs:
  - "_bmad-output/planning-artifacts/lattice-architecture.md"
  - "_bmad-output/planning-artifacts/prd.md"
---

# Lattice — Data Contracts

This directory defines the frozen data shapes that Phase 1 implementation depends on. It is the **single source of truth** for keys, document envelopes, DDL structure, and operation contracts. Every AI agent implementing a Phase 1 story consults these contracts before touching any data shape.

**Companion documents:**
- `lattice-architecture.md` — explains *why* the platform is shaped this way
- `prd.md` — explains *what* the platform must do

These contracts specify **what exactly** the data looks like. Architecture explains rationale; PRD explains requirements; these contracts bind them to concrete shapes.

**Authoring principle (locked):**
> The platform's meta-model is expressed using the same primitives the platform offers to its users — vertices, aspects, and links. The key is an opaque address; meaning lives in the document. Validation is permissive by default; declarations enable enforcement.

---

## Contracts

| File | Contract | Status |
|------|----------|--------|
| [01-addressing-and-envelope.md](./01-addressing-and-envelope.md) | Contract #1 — Addressing Model & Document Envelope | FROZEN |
| [02-operation-envelope.md](./02-operation-envelope.md) | Contract #2 — Operation Envelope | FROZEN |
| [03-mutation-batch-event-list.md](./03-mutation-batch-event-list.md) | Contract #3 — MutationBatch and EventList (Starlark Return Contract) | FROZEN |
| [04-idempotency-tracker.md](./04-idempotency-tracker.md) | Contract #4 — Idempotency Tracker (`vtx.op.<requestId>`) | FROZEN |
| [05-health-kv.md](./05-health-kv.md) | Contract #5 — Health KV Convention | FROZEN |
| [06-capability-kv.md](./06-capability-kv.md) | Contract #6 — Capability KV Shape | FROZEN |
| [07-primordial-bootstrap.md](./07-primordial-bootstrap.md) | Contract #7 — Primordial Bootstrap | FROZEN |
| [08-package-install.md](./08-package-install.md) | Contract #8 — Capability-Package Install (`InstallPackage` / `UninstallPackage`) | Phase 1.5 (Story 1.5.5) |
| [09-identity-claim-flow.md](./09-identity-claim-flow.md) | Contract #9 — Identity Claim Flow (client-minted claim secret) | Phase 1.5 (Story 1.5.7) |
| [10-orchestration-surfaces.md](./10-orchestration-surfaces.md) *(index)* | Contract #10 — Orchestration Surfaces (Loom/Weaver: task placement, target-Lens output, operational KV, ADR-51 subjects). **Sharded — §10.x numbers unchanged:** [Loom](./10-orchestration-loom.md) (§10.5/6/9) · [Weaver](./10-orchestration-weaver.md) (§10.2/8) · [Augur](./10-orchestration-augur.md) (§10.8 AI tier) · [Substrate](./10-orchestration-substrate.md) (§10.1/3/4/7) | Phase 2 — FROZEN |

The original seven Phase-1 contracts are locked. Contract #8 was added in the Phase-1.5 hardening block (Story 1.5.5) when package install/uninstall moved from substrate-direct writes to Processor-routed kernel operations. Contract #9 was added in Story 1.5.7, which froze the reply shape (closed `response` schema, `primaryKey` replacing the arbitrary `detail` map) and the identity claim secret flow (Option C — client mints, Lattice never holds plaintext). Subsequent revisions follow the standard process: changes go through architectural review with Andrew, are recorded in the document's revision history, and propagate to downstream stories.

---

## Document Status

**Version 1.0 complete — 2026-05-12.**

All seven contracts locked. Subsequent revisions follow the standard process: changes go through architectural review with Andrew, are recorded in the document's revision history, and propagate to downstream stories (Epic 1 substrate, Processor, Refractor, Capability Lens implementations).

**Followups tracked for Phase 2 design sessions:**
- **Service Availability Windows** — shape, recurring schedules, integration with operations. Out of Capability KV; needs a dedicated architecture session.
- **NATS Counters (ADR-49) integration** — Health KV metrics, rate limiting, audit log ordering. Mostly quality-of-life; not transformational.
- **NATS Message Scheduling (ADR-51) integration** — significantly simplifies Loom (orchestration) and Weaver (convergence) design by replacing polling loops with native scheduled dispatch. To be revisited before Stream 4 begins.
