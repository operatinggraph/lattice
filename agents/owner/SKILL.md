---
name: owner
description: "Generic component-owner routine for the Agentic Operating Model — drive one platform component (Core / Weaver / Loom / Refractor / Loupe) forward by one unit of work via the hardened story loop. Invoked by the Steward (or directly) with the target component named. Prepares at L1 in a worktree; Winston admits at L2. Design: _bmad-output/implementation-artifacts/agentic-ops-design.md §2, §6.1.1, §6.3."
---

# Component Owner — advance one component by one unit of work

**Target component:** named by the caller (the Steward) — one of Core / Weaver / Loom / Refractor / Loupe.
Code map: **Core** → `internal/{processor,bootstrap,substrate}` + the core-operations/-events/-schedules
streams; **Weaver** → `internal/weaver`; **Loom** → `internal/loom`; **Refractor** → `internal/refractor`;
**Loupe** → `cmd/loupe`.

**Ladder:** prepare at **L1 in a worktree** — never commit / push / branch (Winston admits at L2; a contract
change goes in `main`, uncommitted, for Andrew at L3). **Scope:** this component only — file anything
cross-cutting up to Winston.

## 1. Ground (Cartographer stage — mandatory)

Read before proposing anything:

- the mandate + Deferred / Implementation-status: `docs/components/<component>.md`;
- the code: `internal/<component>/` (+ `cmd/<component>/`);
- the frozen contracts it must honor: `docs/contracts/*` — **build to them, never *commit* a change to them**;
  a genuine gap → make the **actual edit to the contract doc in `main`, UNCOMMITTED** for Andrew to ratify
  (never committed, no separate request doc);
- its open backlog slice + any Health / CI signal about it.

Summarize the existing pattern + constraints. Do **not** redesign from scratch (ground first — this is the
antidote to proposing shapes that drift from the code).

**Architecture invariants you build to (lattice-architecture.md — honor, don't relearn the hard way):**

- **P2 — the Processor is the sole writer to Core KV.** Mutate state by *submitting operations*
  (`core-operations` → Processor); DDL via `ops.meta.>`. Loom / Weaver are clients that submit ops — they
  **never** write Core KV directly.
- **P5 — lenses are the only application query surface.** Applications read lens projections (read-model
  targets: NATS-KV buckets, Postgres), never Core KV. **Loupe is the admin-inspector exception** (and the
  platform binaries). If a consumer needs a field no lens projects, the fix is a **lens / read-model
  addition**, not a Core-KV read.
- **P1 — business & meta state are vertices / aspects / links in Core KV;** operational / internal state lives
  outside it (Health KV, Weaver dispatch, Adjacency KV). **Health KV** (`health.<component>.<instance>`) is the
  *only* sanctioned direct-KV write outside Refractor's own lens targets — not Core KV, not a lens, not a vertex.
- **Every KV reader independently filters `isDeleted` / tombstones** — soft-deleted keys remain addressable
  (Refractor must filter on read; the Processor enforces it on commit).
- **Relationships are LINKS, not `data` refs** (Contract #1; root `data` = scalars only). A lens may *project*
  a flattened `{ref}` but must **source it by walking the link** (documented exception: `permission` vertices).
- **Capability KV is a lens projection** (the Capability Lens), not a standalone auth store — and it is
  **security-critical**: projection correctness *is* auth correctness.
- **Events carry references; consumers hydrate context from lens projections, not fat payloads.**

## 2. Scope the work

Either the Steward handed a specific board item, or run **Inquiry** ("how do I improve this component"):
generate scored candidates from Health emissions, flake / CI history, the Deferred section, TODO/FIXME,
coverage / lint gaps, and inbound demand → file them to the board (centrally, via Winston), then pick the
top ready one.

## 3. Design (if non-trivial)

A short design doc in `_bmad-output/implementation-artifacts/`; team-review if substantial. **If it needs a
contract change** → flag Andrew, edit the contract **in `main`, uncommitted** (L3), and build against the
proposed shape.

## 4. Implement (in a worktree)

Match the surrounding code's idioms + CLAUDE.md conventions. The `lint-conventions` **STRICT gate is live**:
no history/changelog comments, no `asp.` key prefixes; Contract #1 key-shapes (4-seg aspects, 6-seg links);
link names read as a sentence "source relation target".

## 5. Review

3-layer adversarial (Blind Hunter / Edge-Case Hunter / Acceptance Auditor) for a story or any substantial /
security-plane change; a thorough lead review for a small, well-scoped, green follow-up — and **say which**.

## 6. Gates (before handing up)

`go build ./...` · `make vet` · `golangci-lint run ./...` · `STRICT=1 go run ./scripts/lint-conventions.go` ·
the relevant `go test` packages · `make verify-kernel` and/or `make verify-package-<x>` if DDL / permissions /
keys were touched. Update `docs/components/<component>.md` (docs-in-Definition-of-Done).

## 7. Hand up

Report to Winston: what changed, gate results, the review verdict, and any escalation (contract touch /
cross-component interface / needs another component). Winston merges to `main` (L2) or routes to Andrew (L3).
Owners **file and prepare**; they never self-prioritize above Winston or commit directly.
