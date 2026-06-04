# Lattice

**A graph-native, AI-native application platform built on NATS.**

> ⚠️ **Work in progress.** Lattice is an active, in-development project. 
> The sections below describe the platform as designed. 
> See **[Project status](#project-status)** for what's implemented today.

---

## What is Lattice?

Most software is a "dumb warehouse" of data, and we bolt AI onto it after the fact — agents
that guess at API shapes, hallucinate schemas, and fight inconsistent state. Lattice inverts
that: it's an operating system for application state where **the data model is the integration
surface**, designed from the ground up for both human logic and machine intelligence.

Lattice stores everything as a graph and treats every change as a deterministic, auditable
operation:

- **State is a graph.** Entities are **vertices**, their data lives in **aspects**, and every
  relationship — including authorization — is a **link**. Keys are opaque addresses; meaning
  lives in the documents. This is the **VAL** (Vertex · Aspect · Link) model.
- **Writes are deterministic operations.** A change is an *intent* submitted to an immutable
  ledger. A single authorized writer — the **Processor** — runs a sandboxed **Starlark** script
  that validates the intent against the schema and returns the mutations + business events to
  commit atomically. No code path writes state except through this pipeline.
- **Reads are derived views.** The **Refractor** continuously projects the graph into queryable
  **lenses** (openCypher → Postgres, NATS KV) via change-data-capture. The read side is always
  derived from the source of truth, never written directly — and authorization itself is just a
  lens (ReBAC projected into an O(1) capability check).
- **The platform is self-describing.** Every operation and type is a DDL meta-vertex carrying
  its own schema, description, and examples. An AI agent starts at an identity, follows links to
  discover what it can do, reads the schema, and submits a valid operation — **cold-start, with
  no SDK and no integration code.** The graph *is* the prompt context.
- **The kernel is minimal.** Identity, RBAC, and all business capability ship as installable
  **Capability Packages** (DDLs + lenses + permissions), not baked into the core.

On top of this core, two engines drive *action*: the **Loom** runs deterministic, imperative
procedures ("do A, then B, then C"); the **Weaver** drives declarative convergence ("this target
state must hold — make it so"), nudging external systems and AI agents to close the gap.

The longer-form vision (the Lattice Manifest and System Spec) lives in a separate design vault;
the architecture of record is in [`docs/`](docs/README.md).

---

## Built by AI agents

Lattice is **deliberately developed by AI agents** — as much an experiment in AI-driven software
development as it is a platform. **The agents write everything**: the code, the tests, the
contracts, and the documentation. The work is organized with the
[BMAD method](https://github.com/bmad-code-org/BMAD-METHOD) (a structured agentic workflow with
analyst, architect, scrum master, developer, and reviewer roles) and a session-per-story model
where each story is implemented by a fresh agent against a self-contained brief, then reviewed
by another.

My role (Andrew) is **architect and supervisor**, not implementer: I set the vision and the
binding architectural decisions, freeze the data contracts, pressure-test proposals, review and
adjudicate the agents' output, and steer course — but I don't write the implementation. The
goal is to see how far a rigorously-supervised, contract-first agentic process can carry a
genuinely complex distributed system.

---

## How it works

Lattice is a small set of cooperating components, each with a living reference page:

| Component | Role |
|-----------|------|
| [Processor](docs/components/processor.md) | The sole authorized writer — a 9-step commit pipeline that runs Starlark over Core KV, with atomic batch commit and a transactional event outbox |
| [Refractor](docs/components/refractor.md) | The read side — continuous openCypher lens projections (Postgres / NATS KV), the security-critical Capability Lens, and CDC consumers |
| [Substrate](docs/components/substrate.md) | NATS / KV / NanoID primitives — key shapes, atomic batch, durable CDC consumers |
| [Capability Packages](docs/components/_packages.md) | Installable bundles (identity, RBAC, domain logic) added through the `InstallPackage` kernel op — the kernel stays minimal |
| [Loom](docs/components/loom.md) | The procedure engine — deterministic, idempotent, linear flows (the "executive") |
| [Weaver](docs/components/weaver.md) | The convergence engine — drives a declared target state, with Two-Phase Nudge for safe external side effects (the "visionary") |

The exact wire shapes, key patterns, and behavioral rules are pinned in the data contracts under
[`docs/contracts/`](docs/contracts/README.md).

### The wider platform

The same primitives extend outward into the rest of the Lattice vision:

- **Gateway** — the trust boundary at the edge: it authenticates actors (JWT), stamps identity
  onto every operation, and enforces read-path authorization so an agent's view of the world is
  bounded by the same ReBAC links as a human's.
- **Vault & crypto-shredding** — sensitive aspects are encrypted with per-identity keys, so the
  "right to be forgotten" is *physical*: destroy the key and that identity's data — even in the
  immutable ledger — becomes permanent, unrecoverable gibberish.
- **Semantic Contracts ("Executable Paper")** — legal prose modeled as atomic **clause vertices**
  linked directly to the state they govern. The Weaver enforces each clause continuously, turning
  a contract into a live billing-and-compliance engine with a perfect chain of custody from the
  signed paragraph to every action it authorized.
- **Edge Lattice & Personal Lenses** — a sovereign client-side node (mobile / web / IoT) running
  the same VAL model and Starlark locally for offline-first, zero-latency, privacy-first
  interaction. The cloud Refractor pushes each device a **Personal Lens** — a security-filtered
  stream of just the sub-graph that identity may see (a filter, not a clone) — and the Edge node
  reconciles by revision when it reconnects.
- **Cells & sharding** — the graph scales by **cells**: a root vertex and its sub-graph are
  co-located in one bucket so writes stay atomic, while a global adjacency index and bridge links
  carry cross-cell traversal, and live data migration runs as a dual-write "shadow" dance with no
  downtime.

---

## Project status

This is the one place that distinguishes what's built from what's designed.

| Phase | Scope | State |
|-------|-------|-------|
| **Phase 1** | Trustworthy core: substrate, Processor write path, Refractor lens projections, identity/RBAC packages, Capability-Lens authorization, the Hello Lattice reference slice | ✅ Implemented + tested (CI-gated) |
| **Phase 1.5** | Hardening: kernel minimization, package installs routed through the Processor, contract conformance suite, transactional event outbox | ✅ Complete |
| **Phase 2** | Orchestration: Loom + Weaver + Two-Phase Nudge + a Loftspace lease-application reference vertical | 🔨 Contracts frozen; implementation starting |
| **Phase 3+** | Gateway (read-path auth, JWT), Vault (crypto-shredding / PII), AI-authored capabilities, Semantic Contracts, Edge Lattice + Personal Lenses, multi-cell sharding | 🔭 Designed, future work |

---

## Documentation

- **[`docs/architecture-overview.md`](docs/architecture-overview.md)** — full platform architecture diagram (as designed, all phases)
- **[`docs/`](docs/README.md)** — the documentation map (contracts, components, observability, operations)
- **[`docs/contracts/`](docs/contracts/README.md)** — the data contracts (source of truth for wire shapes)
- **[`docs/components/`](docs/components/README.md)** — living per-component reference pages
- **[`docs/hello-lattice.md`](docs/hello-lattice.md)** — the 60-minute end-to-end tutorial

---

## Quick start

```console
# Start NATS + Postgres, bootstrap primordial state, start the Refractor
make up

# Confirm everything is healthy
lattice health gates
```

Expected output:

```console
health.gates.phase1.gate1  passed: true
health.gates.phase1.gate2  passed: true
health.gates.phase1.gate3  passed: true
health.gates.phase1.gate4  passed: true
```

`make up` seeds only the primordial kernel. Identity and RBAC ship as **Capability Packages**,
so install them before using identity/role operations:

```console
lattice-pkg install packages/identity-domain
lattice-pkg install packages/rbac-domain
```

Then walk the full vertical slice — define a type, create entities, project to Postgres, drive
it with an AI agent, and roll a schema change back — in the
**[Hello Lattice tutorial](docs/hello-lattice.md)**.

### Prerequisites

- Go 1.26+
- Docker + Docker Compose
- `make`

---

## Development

```console
# Build all binaries
make build

# Run all unit + integration tests (serialised for embedded NATS stability)
make test

# Lint
golangci-lint run ./...

# Go vet (ANTLR-generated files excluded)
make vet

# Gate tests
make verify-kernel
make test-bypass
make test-capability-adversarial
make test-rollback
```
