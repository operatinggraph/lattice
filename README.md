# Lattice

**A graph-native, agent-native application platform built on NATS.**

> ⚠️ **Work in progress.** Lattice is an active, in-development project.
> See **[Project status](#project-status)** for what's implemented today.

**Website: [operatinggraph.com](https://operatinggraph.com)** — what Lattice is, why it exists,
and the story of the AI agent organization that builds it (the About page carries the long-form
reading). This README is the technical front door: the architecture, the status, and how to run it.

---

## What it is

Lattice runs an entire business domain as **one addressable graph** — the **VAL model**:
**vertices** (entities), **aspects** (their data, each independently addressable), and **links**
(typed relationships that double as the ReBAC authorization graph). Types, operations, schemas,
and permissions are themselves in the graph, so the system can describe itself to any actor —
including AI agents, which discover what they may do by walking from their own identity instead
of depending on a hand-written SDK.

A few invariants carry the whole design:

- **One write path.** Every mutation is an operation on the `core-operations` stream; the
  **Processor** is the sole writer to Core KV — a 9-step commit pipeline with schema-aware
  Starlark validation, capability authorization, atomic batch commit, and a transactional event
  outbox. There is no side door for state mutation.
- **Reads are lens projections.** The **Refractor** consumes Core-KV change-data-capture and
  continuously projects **lenses** — openCypher-derived materialized views in Postgres (with
  row-level security) or NATS KV. Applications read lenses, never Core KV, and every read model
  can be rebuilt from the ledger.
- **Everything is a package.** The kernel stays minimal; identity, RBAC, and all domain behavior
  install as **capability packages** through the same write path — and activate live on a running
  stack, no restart.
- **AI authorship has guardrails.** Agents submit operations like any other actor; agent-authored
  changes (DDL, Starlark rules, lenses, workflows) pass human review and the same deterministic
  validation and write path as business data.

## How it works

Lattice is a small set of cooperating components, each with a living reference page:

| Component | Role |
|-----------|------|
| [Processor](docs/components/processor.md) | The sole authorized writer — a 9-step commit pipeline that runs Starlark over Core KV, with atomic batch commit and a transactional event outbox |
| [Refractor](docs/components/refractor.md) | The read side — continuous openCypher lens projections (Postgres / NATS KV), the security-critical Capability Lens, and CDC consumers |
| [Substrate](docs/components/substrate.md) | NATS / KV / NanoID primitives — key shapes, atomic batch, durable CDC consumers |
| [Capability Packages](docs/components/_packages.md) | Installable bundles (identity, RBAC, domain logic) added through the `InstallPackage` kernel op — the kernel stays minimal |
| [Loom](docs/components/loom.md) | The procedure engine — deterministic, idempotent, linear flows (the "executive") |
| [Weaver](docs/components/weaver.md) | The convergence engine — drives a declared target state toward convergence (the "visionary") |
| [Augur](docs/components/augur.md) | The Weaver's AI-assisted reasoning tier — escalates a gap the deterministic planner can't solve to a model, records the answer as a validated, human-reviewed proposal, then dispatches it back through the Weaver (the Processor stays the sole writer) |
| [Bridge](docs/components/bridge.md) | The external-I/O egress — the one component that makes outbound calls to external systems, idempotently, via a durable `events.external.>` consumer and a pluggable adapter registry |
| [The Chronicler](docs/components/chronicler.md) | The event-ledger materializer — tails the platform's event and intent-ledger streams into append-only history read models: the durable audit counterpart to Refractor's present-state projections (a separate binary by charter) |
| [Object-store-manager](docs/components/object-store-manager.md) | The byte-janitor of the off-graph blob plane — reclaims object-store bytes when their vertex is tombstoned or crash-orphaned, and cascades owner-tombstones to detach dangling object links |
| [Gateway](docs/components/gateway.md) | The edge trust boundary — verifies an external actor's IdP-signed JWT, stamps the verified identity onto every operation, and bounds each actor's read view to the sub-graph its ReBAC links permit, closing actor impersonation at the edge |
| [Vault](docs/components/vault.md) | Per-identity key custody and crypto-shredding — encrypt-on-write / decrypt-on-read for sensitive aspects, and the irreversible `ShredKey` "right to be forgotten" primitive; a library embedded in the Processor and Refractor, with an async privacy-worker |
| [Loupe](docs/components/loupe.md) | The internal operator console — browse Core KV, drive the engines' control planes, submit DDL-driven ops, install packages, upload blobs; a trusted single-identity, loopback-bound inspector (the one app allowed to read Core KV directly) |

The exact wire shapes, key patterns, and behavioral rules are pinned in the data contracts under
[`docs/contracts/`](docs/contracts/README.md).

## Built by an AI agent organization

Lattice is deliberately developed by AI agents — the code, the tests, the contracts, and the
documentation are agent-written, with one human principal as architect and supervisor: setting the
vision, freezing the data contracts, and ratifying every design and frozen-contract change. Since
Phase 3 the loop runs autonomously on a schedule — agents survey demand, design, build behind the
full gate suite, and exercise the shipped apps to file the next round of demand. The paper trail
is in-repo: the live boards under
[`_bmad-output/planning-artifacts/backlog.md`](_bmad-output/planning-artifacts/backlog.md),
the design docs under `_bmad-output/implementation-artifacts/`, and the role charters in
[`agents/README.md`](agents/README.md). The longer story is on the
[website's About page](https://operatinggraph.com/about).

## Project status

This is the one place that distinguishes what's built from what's designed.

| Phase | Scope | State |
|-------|-------|-------|
| **Phase 1** | Trustworthy core: substrate, Processor write path, Refractor lens projections, identity/RBAC packages, Capability-Lens authorization, the Hello Lattice reference slice | ✅ Implemented + tested (CI-gated) |
| **Phase 1.5** | Hardening: kernel minimization, package installs routed through the Processor, contract conformance suite, transactional event outbox | ✅ Complete |
| **Phase 2** | Orchestration: Loom (procedures) + Weaver (convergence) + the external-I/O bridge + the Loftspace lease-application reference vertical | ✅ Complete (CI-gated) |
| **Phase 3** | Driven by the autonomous agentic flywheel (see above). **Shipped:** the Loupe operator console and four vertical front-ends (LoftSpace, Clinic, Café, Wellness) end-to-end on the real stack; the Gateway trust boundary with real-actor write authorization and operator login (capability authorization is the only mode); Vault crypto-shredding with audited reveal; the Chronicler history materializer; the Augur propose → review → dispatch loop; Semantic Contracts as a sanctioned pattern + reference package; the first production Personal Lenses and the Edge Lattice reference node (offline-first read/write, per-identity connection confinement). **In build:** the Edge in-browser node (wasm) and the Facet discovery-driven personal client. **Designed + deferred:** multi-cell sharding, HA clustering. | 🏗️ Continuous |

## Documentation

- **[`docs/architecture-overview.md`](docs/architecture-overview.md)** — full platform architecture diagram (as designed, all phases)
- **[`docs/`](docs/README.md)** — the documentation map (contracts, components, observability, operations)
- **[`docs/contracts/`](docs/contracts/README.md)** — the data contracts (source of truth for wire shapes)
- **[`docs/components/`](docs/components/README.md)** — living per-component reference pages
- **[`docs/hello-lattice.md`](docs/hello-lattice.md)** — the 60-minute end-to-end tutorial

The longer-form vision (the Lattice Manifest and System Spec) lives in a separate design vault;
the architecture of record is in `docs/`.

## Quick start

Prerequisites: Go 1.26+, Docker + Docker Compose, `make`.

```console
# Minimal kernel — NATS + Postgres, primordial bootstrap, Processor + Refractor
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

```console
# Full platform — adds the Loom/Weaver/Bridge engines, the core capability
# packages (identity, RBAC, privacy, objects), the Gateway (:8080), and the
# Loupe operator console → http://127.0.0.1:7777
make up-full
```

### Demo verticals

Each target brings the full platform up (reusing a running stack), installs the vertical's
capability packages onto the live graph, and starts its front-end:

```console
make up-loftspace    # LoftSpace residential leasing   → http://127.0.0.1:7788
make up-clinic       # Clinic appointment booking      → http://127.0.0.1:7799
make up-cafe         # Café house-tab ledger           → http://127.0.0.1:7801
make up-wellness     # Wellness classes & rosters      → http://127.0.0.1:7802
```

The verticals compose: install more than one and they share the graph — a completed Clinic visit
can book a Wellness class with no integration code written.

Then walk the full vertical slice by hand — define a type, create entities, project to Postgres,
drive it with an AI agent, and roll a schema change back — in the
**[Hello Lattice tutorial](docs/hello-lattice.md)**.

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
make test-rollback
make lint-conventions
```

The security proofs live in each mechanism's own colocated tests — `internal/bypass` (the
outcome-level residual), `internal/natsperm`, the Processor's Starlark gates, the Refractor's RLS
tests, the Gateway auth tests — and all run under `make test`.

### Dev-loop: apply a package edit without a teardown

Packages upgrade **in place** on a running stack — no `make down`. After editing a
package's DDL, lens, or permissions:

```console
# Diff-apply one edited package (create/update/tombstone in one atomic op)
make reinstall-package PKG=packages/clinic-domain

# Refresh a whole vertical: diff-apply its packages + rebuild/restart its FE binary
make refresh-clinic        # or: refresh-loftspace / refresh-cafe / refresh-wellness

# Preview the delta without submitting
lattice-pkg install --dry-run packages/clinic-domain
```

A *newly-added* lens/role/op hot-activates live too, same as an edit — the Refractor's CDC watch
and the Processor's DDL cache both react to any commit, not just updates. Only a change to the
*primordial* kernel seed needs a fresh bootstrap. See
[Capability Packages → Upgrade](docs/components/_packages.md#upgrade--in-place-dev-loop-refresh-f-004).

## License

Lattice is **source-available, not open source**: the code is public to read, run locally, and
evaluate; all other rights are reserved — see [LICENSE](LICENSE). For commercial licensing or
partnership inquiries: [hello@operatinggraph.com](mailto:hello@operatinggraph.com).
