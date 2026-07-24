---
name: owner
description: "Generic component-owner routine for the Agentic Operating Model — drive one platform component (Core / Weaver / Loom / Refractor / Loupe) forward by one unit of work via the hardened story loop. Invoked by the Steward (or directly) with the target component named. Prepares at L1 in a worktree; Winston admits at L2. Design: _bmad-output/implementation-artifacts/agentic-ops-design.md §2, §6.1.1, §6.3."
---

# Component Owner — advance one component by one unit of work

**This is a SKILL the Steward follows inline (or is invoked via the Skill tool) — NOT a spawnable sub-agent
type;** when the Steward runs it, *it* is Winston (build, then admit — no separate hand-up).

**Target component:** named by the caller (the Steward) — one of Core / Weaver / Loom / Refractor / Loupe.
Code map: **Core** → `internal/{processor,bootstrap,substrate}` + the core-operations/-events/-schedules
streams; **Weaver** → `internal/weaver`; **Loom** → `internal/loom`; **Refractor** → `internal/refractor`;
**Loupe** → `cmd/loupe`.

**Ladder:** prepare at **L1**. **Code** changes go in an **isolated git worktree** (Winston admits at L2).
**Docs — design docs, the board, and contracts — are edited directly in `main`** (not the worktree); a
**contract** change stays **uncommitted** in `main` for Andrew at L3 (no separate request doc — the uncommitted
diff is the proposal). **Scope:** this component only — file anything cross-cutting up to Winston.

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

**Then compile the FIRE BRIEF (Phase 0 — mandatory for M+ / multi-package / security-plane work; template +
gate: [`agents/fire-brief-template.md`](../fire-brief-template.md)):** verified touch-list + precedents to
mirror + increment order with runnable green checks + in-scope gotchas + adjacent finds (filed to the board
*before* the first edit) + non-goals, gated by the **scope-diff** against the item's ratified scope sentence
(narrow-only; dependencies re-verified both ways), appended to the owning design doc as its build note and
committed (docs, in `main`) before code. If the Steward handed you a brief, re-verify its citations still
hold and build from it; if activated without one, produce it first.

**Architecture invariants you build to (lattice-architecture.md — honor, don't relearn the hard way):**

- **P2 — the Processor is the sole writer to Core KV.** Mutate state by *submitting operations*
  (`core-operations` → Processor); DDL via `ops.meta.>`. Loom / Weaver are clients that submit ops — they
  **never** write Core KV directly.
- **P5 — lenses are the only application query surface.** Applications read lens projections (read-model
  targets: NATS-KV buckets, Postgres), never Core KV. **Loupe is the admin-inspector exception** (and the
  platform binaries). If a consumer needs a field no lens projects, **add the lens (DDL) to the owning
  package** — that's *package* work, not a Core-KV read; only a missing platform **primitive** (engine / op /
  substrate / orchestration) is component work.
- **P1 — business & meta state are vertices / aspects / links in Core KV;** operational / internal state lives
  outside it (Health KV, Weaver dispatch, Adjacency KV). **Health KV** (`health.<component>.<instance>`) is the
  *only* sanctioned direct-KV write outside Refractor's own lens targets — not Core KV, not a lens, not a vertex.
- **Every KV reader independently filters `isDeleted` / tombstones** — soft-deleted keys remain addressable
  (Refractor must filter on read; the Processor enforces it on commit).
- **Relationships are LINKS — never keys in `data`, root OR aspect** (Contract #1; `data` = scalars only,
  everywhere). A lens may *project* a flattened `{ref}` but must **source it by walking the link** (documented
  exception: `permission` vertices). **A key-list / ref index stored in an aspect — a `.bookings`
  `{appts:[vtx.…]}`, a `.leaseApplications` `{applications:[…]}` — is a VIOLATION**, even when it backs an op's
  own guard logic. If a guard needs the *set* of a vertex's neighbors (a reverse-link enumeration the
  known-key-reads op path lacks), that is a **missing platform primitive → file it to `lattice.md` and WAIT
  (block the item)**; do **not** denormalize keys into an aspect to dodge the wait. Cap-KV §06's "the operation's
  own Starlark logic" licenses the *check*, **not** storing relationships in aspects. (Pure existence-uniqueness
  — ≤1 of X per (a,b) — needs no set: use a deterministic guard LINK + `CreateOnly`, revive-on-reuse.)
- **Capability KV is a lens projection** (the Capability Lens), not a standalone auth store — and it is
  **security-critical**: projection correctness *is* auth correctness.
- **Events carry references; consumers hydrate context from lens projections, not fat payloads.**

## 2. Scope the work

Either the Steward handed a specific board item, or run **Inquiry** ("how do I improve this component"):
generate scored candidates from Health emissions, flake / CI history, the Deferred section, TODO/FIXME,
coverage / lint gaps, and inbound demand → file them to the board (centrally, via Winston), then pick the
top ready one.

## 3. Design (if non-trivial)

A short design doc in `_bmad-output/implementation-artifacts/` — **written directly in `main`** (a doc, not
worktree code); team-review if substantial. **If it needs a contract change** → flag Andrew, edit the contract
**in `main`, uncommitted** (L3, no separate request doc), and build against the proposed shape.

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
cross-component interface / needs another component). **Out-of-scope finds go in this report for Winston to
triage into a board row — do NOT `spawn_task` a user-facing chip yourself; the board row is the canonical
demand, and a chip (if any) is Winston's routed convenience that must name the skill to run.** Winston merges
to `main` (L2) or routes to Andrew (L3).
Owners **file and prepare**; they never self-prioritize above Winston or commit directly. **Any board update
you hand up is a ONE-LINE status + SHA** (the board is an index, not a journal — §5 of the swimlanes design):
the detail — what you built, the findings, coverage — lives in the **commit message + the design doc**, never
narrated into the board cell.
