# Lattice Components

This directory contains per-component reference pages. Each page documents what
the component owns, what it reads and writes, its in/out contracts, failure
modes, and the architectural principles it honors. Each page describes the
component **as designed**; a short *Implementation status* section at the end of
each page records what is built today versus deferred to a later phase.

Cross-component interface contracts live in
[`/docs/contracts/`](/docs/contracts/README.md). Per-component implementation
choices live HERE. Per-package capability definitions live under
`packages/<package-name>/`.

## The components

**Core write / read spine**

- [Bootstrap](./bootstrap.md) — the one-shot provisioning binary: seeds every KV bucket / stream /
  object store plus the ~75-entry primordial Core-KV batch (meta-meta DDL, Capability Lens anchor,
  service-actor identities), then exits; the sole sanctioned non-Processor Core-KV writer.
- [Processor](./processor.md) — the sole authorized writer: the 9-step commit
  pipeline, the Starlark sandbox, the DDL cache, capability authorization, and the
  transactional event outbox.
- [Refractor](./refractor.md) — the read side: continuous openCypher lens
  projections, the security-critical Capability Lens, CDC consumers, and the
  control plane.
- [Substrate](./substrate.md) — the NATS / KV / NanoID primitive layer: key
  shapes, atomic batch, and durable + supervised consumers.

**Orchestration**

- [Loom](./loom.md) — the deterministic procedure engine: a linear-sequence
  interpreter with userTask / systemOp / externalTask steps and a rebuildable
  cursor.
- [Weaver](./weaver.md) — the convergence engine: targets-as-Lenses, the 3-lane
  work stream, and triggerLoom / assignTask / directOp remediation.
- [Bridge](./bridge.md) — the external-I/O egress: a durable `events.external.>`
  consumer, the adapter registry, and idempotent result-op submission.
- [The Chronicler](./chronicler.md) — the event-ledger materializer: tails platform event / ledger
  streams (`events.loom.>`, later `events.weaver.>`, the intent ledger) into append-only history read
  models (`orchestration-history`) + verbatim archives — the history counterpart to Refractor's
  present-state projection. A separate binary by charter (event streams stay out of Refractor).

**Cross-cutting**

- [Capability Packages](./_packages.md) — the install / uninstall model and the
  package-authoring guide (the kernel stays minimal; everything else is a package).
- [Service actors](./service-actors.md) — the internal Loom / Weaver / Bridge /
  object-store-manager identities seeded at bootstrap and how they hold root-equivalent capability.
- [Platform message scheduling](./scheduling.md) — the `core-schedules` stream and
  the `@at` scheduled-message convention any component uses to turn time into an op.
- [Platform control plane](./control-plane.md) — the `lattice.ctrl.*` operator
  request/reply surface (pause / resume / inspect / retire) the three engines expose and
  Loupe + the CLI drive.
- [Refractor failure tiers](./refractor-failure-tiers.md) — the four-tier failure
  model plus the privacy / security supersession tiers (Capability-Lens alert + the
  privacy-critical crypto-shred tier).
- [Object-store-manager](./object-store-manager.md) — the always-on byte-janitor of the
  off-graph blob plane: Loop B tombstone reclaim, the never-attached crash-orphan reconcile,
  and the owner-tombstone-cascade.

**Edge & security**

- [Gateway](./gateway.md) — the edge trust boundary: verifies an external actor's IdP-signed JWT,
  stamps the verified identity onto every operation, and bounds each actor's read view to the
  sub-graph its ReBAC links permit — closing actor impersonation at the edge.
- [Vault](./vault.md) — per-identity key custody + crypto-shredding: encrypt-on-write / decrypt-on-read
  for sensitive aspects, and the irreversible `ShredKey` right-to-be-forgotten primitive. A library
  embedded in the Processor + Refractor, not a standalone binary.
- [Privacy-worker](./privacyworker.md) — the asynchronous half of crypto-shredding: consumes a
  recorded shred intent and calls `Vault.ShredKey`, then records the finalization. Co-located in the
  Processor process.

**Experience layer**

- [Loupe](./loupe.md) — the internal view-and-control console: browse Core KV, drive
  the component control planes, submit DDL-driven ops, install packages, upload blobs;
  a trusted single-identity, loopback-bound, auth-less inspector (the one application
  allowed to read Core KV directly — the P5 exception).

## How to use these pages

When authoring a story handoff brief that touches a component, read that
component's page first to understand what it owns, what contracts it honors, what
principles apply, and what is deferred. These pages are the consult-first layer,
so a brief can cite a component page rather than re-explaining the component
inline.

Update a component page in the same commit as the code it describes. Drift
between page and code is treated as a documentation bug.

## Implementation status

| Component | Status |
|-----------|--------|
| Bootstrap — one-shot primordial provisioning | ✅ Built (Phase 1) |
| Processor, Refractor, Substrate, Capability Lens, Capability Packages | ✅ Built (Phase 1 / 1.5) |
| Loom, Weaver, Bridge, object-store-manager, service actors, platform scheduling | ✅ Built (Phase 2) |
| Loupe — operator view-and-control console (trusted single-identity, loopback, no auth) | ✅ Built (Phase 3) |
| Gateway — JWT auth, `Lattice-Actor` stamping | ✅ Built (Phase 3) — write-path (Fires 1+2: JWT verify + actor stamping + live JWKS); read-path enforcement in progress |
| Vault, Privacy-worker — per-identity keys, crypto-shredding | ✅ Built (Phase 3) — encrypt-on-write/decrypt-on-read + `ShredIdentityKey`; per-vertical fires ongoing |
| The Chronicler — event-ledger materializer (durable orchestration history) | ✅ Ratified (Fork C) — PROJECT mode + `loomFlowHistory` shipped, host extraction to `cmd/chronicler` pending |

Each page's own *Implementation status* / *What's deferred* section is the
authoritative, fine-grained record for that component.
