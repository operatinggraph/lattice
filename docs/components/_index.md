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
