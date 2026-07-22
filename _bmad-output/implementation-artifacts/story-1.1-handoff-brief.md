---
title: Story 1.1 Implementation Handoff Brief
story: 1.1 — NATS Atomic Batch Spike
model_tier: Sonnet (locked — do NOT use Opus or Haiku)
token_budget: ~52K (input + output combined)
session: Fresh implementation session — first action is reading this brief
architecture_lead: Winston (any architectural question routes to Winston via Andrew)
date: 2026-05-13
---

# Story 1.1 — NATS Atomic Batch Spike: Implementation Handoff Brief

## Your Role

You are the implementing engineer for Story 1.1. Winston (the architect) and Andrew (the product owner) have completed the planning workflow and locked the story's scope and AC. Your job is to deliver the spike code and the written report defined by the AC. **You do not have authority to change story scope, AC, or architectural contracts during implementation.** If you discover something that contradicts the contracts or the story AC, stop and surface it — do not improvise.

## Operating Rules (Lattice Project)

- **Model tier is locked:** Sonnet only. If the session is on a different tier, halt and inform Andrew.
- **No PRs.** After implementation + Winston review, commit directly to `main`.
- **Architecture is binding:** `_bmad-output/planning-artifacts/lattice-architecture.md` + `_bmad-output/planning-artifacts/data-contracts.md` are the sources of truth. Story AC is the immediate target.
- **Embedded NATS is approved for spike code** — no Docker dependency for Story 1.1 or 1.2.
- **Token tracking:** at session close, record actual token usage in `_bmad-output/implementation-artifacts/token-usage-tracker.md` per the procedure documented there.

## Story Scope (Copy from `epics.md` — Authoritative)

> As a platform engineer,
> I want a validated spike report on NATS JetStream atomic batch behavior at the operation patterns Lattice requires,
> So that the Processor commit path architecture is grounded in verified NATS semantics before any implementation begins.

**Recommended model tier:** Sonnet
**Estimated token budget:** ~52K

**Acceptance Criteria (from `epics.md`):**

Given a local NATS 2.12+ server (2.14 recommended) with JetStream enabled, when the spike harness executes a series of targeted tests against the four behavioral questions, then a written report is produced covering all four areas:

1. **TTL-in-batch**: A KV put with a per-key TTL issued inside a `PublishBatch` commits successfully; the entry expires independently of other batch entries; behavior is confirmed when mixed TTL and non-TTL entries appear in the same batch. (Per-key TTL is a NATS 2.11+ feature; this test verifies it composes correctly with the 2.12+ atomic batch feature.)

2. **Revision condition atomicity**: A `PublishBatch` containing a compare-and-swap entry (revision condition) commits atomically — the entire batch is rejected if the revision check fails; no partial commit is observable from a concurrent reader.

3. **Multi-subject batches within a single KV bucket**: A single `PublishBatch` containing messages targeting multiple distinct subjects within ONE KV bucket (Core KV) — e.g., a vertex create at `vtx.identity.<id>`, an aspect write at `vtx.identity.<id>.email`, a link create at `lnk.identity.<id>.assignedRole.role.<roleId>`, AND the op-tracker write at `vtx.op.<requestId>` — commits or fails as a unit; behavior under concurrent conflicting writes to one of those keys from a second writer is documented. **Atomic batches are constrained to a single stream (= single KV bucket); they DO NOT span multiple buckets** — Health KV and Capability KV are populated by separate writers (components direct-write to Health; Refractor projects to Capability) and are NEVER part of the Processor's atomic batch.

4. **TTL marker delivery**: After a per-key TTL expires, the KV watcher receives a tombstone/expiry marker on the subject; the marker is distinct from a normal delete; the marker's sequence number is ordered correctly in the stream.

**And** the report includes a clear **Go/No-Go recommendation** for the current atomic batch architecture with rationale.
**And** all spike code is committed to `internal/spike/nats-batch/` with a README summarizing findings.
**And** if any finding contradicts the architecture contracts in `data-contracts.md`, the specific contract section and recommended amendment are called out explicitly.

## Required Context — Read Before Implementing

The spike's four behavioral questions all map to specific architectural assumptions made during planning. To know whether a finding *contradicts* a contract, you need to know what the contracts assume.

**Read these sections (do NOT read whole files — token budget is tight):**

| File | Section | Why |
|---|---|---|
| `_bmad-output/planning-artifacts/data-contracts.md` | Contract #1 — Addressing Model & Document Envelope | Key patterns the batch will write |
| `_bmad-output/planning-artifacts/data-contracts.md` | Contract #3 — MutationBatch and EventList | The atomic batch is what Processor step 8 emits |
| `_bmad-output/planning-artifacts/data-contracts.md` | Contract #4 — Idempotency Tracker (`vtx.op.<requestId>`) | TTL-in-batch behavior we're testing (24h TTL on tracker entry inside the same batch) |
| `_bmad-output/planning-artifacts/data-contracts.md` | Contract #5 — Health KV Convention | Multi-subject batch test reaches Health KV |
| `_bmad-output/planning-artifacts/lattice-architecture.md` | Sections referencing ADR-48 (per-key TTL), ADR-49 (Counters), ADR-51 (Message Scheduling) | Architectural decisions this spike validates |
| `_bmad-output/planning-artifacts/epics.md` | Story 1.1 (the canonical AC — confirm no drift between brief and master) | Source of truth |

## Library / Environment

- **Go version:** 1.26.1 (confirmed via `go version` at scaffolding time)
- **Go module:** `github.com/operatinggraph/lattice` (root `go.mod` exists; this spike is `internal/spike/nats-batch/` within it)
- **NATS server:** embedded — import `github.com/nats-io/nats-server/v2/server` and start an in-process server with JetStream enabled. Do NOT require Docker.
- **NATS client:** `github.com/nats-io/nats.go` — the official client; check it exposes the `PublishBatch` API (atomic batch is NATS 2.12+; per-key TTL is 2.11+).
- **NATS server version:** **minimum 2.12, recommended 2.14.** 2.12 introduces atomic batch (`PublishBatch`); 2.14 is the preferred target for stability and feature margin. Pin the embedded-server dependency to a 2.12.x or 2.14.x release accordingly.

## Deliverables Checklist

At session close, the following MUST exist:

1. ✅ `internal/spike/nats-batch/main.go` (or organized into multiple files) — runnable test harness for the four behavioral tests
2. ✅ `internal/spike/nats-batch/README.md` — the **written report** required by AC; structured as four sections (one per behavioral question), each with: test description, observed behavior, raw output snippet, interpretation, contract implication (if any)
3. ✅ `internal/spike/nats-batch/go.sum` and updated root `go.mod` reflecting added NATS dependencies
4. ✅ Go/No-Go recommendation at the top of the README with one-paragraph rationale
5. ✅ If any finding contradicts `data-contracts.md`: a `CONTRACT-AMENDMENT-REQUEST.md` alongside the report naming the specific contract section, the contradiction, and the recommended amendment text
6. ✅ Updated row in `_bmad-output/implementation-artifacts/token-usage-tracker.md` with actual token usage, model used, session date, and any notes
7. ✅ All code must compile clean (`go build ./...` exits 0) and `go vet ./...` must pass

## What This Spike Is NOT

- **Not** a Processor implementation — that comes in Stories 1.5-1.8
- **Not** a benchmark — no NFR-P targets are validated here (those land in production env per architectural decisions)
- **Not** a production NATS deployment — embedded server is fine
- **Not** a multi-subject Core KV implementation — only enough to verify the batch behavior; the actual Core KV structure is established in Story 1.3

## Escalation Path

If during implementation you find:

- A NATS API doesn't support what the AC assumes → **STOP, write a brief finding, escalate to Winston via Andrew.** Do not improvise an alternative architecture.
- An architecture contract appears wrong → **document the contradiction in `CONTRACT-AMENDMENT-REQUEST.md`** and continue with the spike as scoped. Winston resolves the contract change after the spike, not during.
- Token usage trending past 60K (15% over budget) → **flag it in your next message before continuing.** Andrew may decide to scope down or accept overrun.

## Closing the Session

Before ending the session:

1. Verify all 7 deliverables above
2. Run `go build ./...` and `go vet ./...` from repo root — must pass
3. Update the token tracker
4. Present the work to Winston/Andrew via summary message listing all deliverables and the Go/No-Go recommendation headline
5. Wait for review approval before committing to main (Andrew or Winston signs off)
