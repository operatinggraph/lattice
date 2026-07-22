---
title: Story 1.3 Implementation Handoff Brief
story: 1.3 — Dev Harness with Primordial Bootstrap
model_tier: Sonnet (locked)
token_budget: ~95K (input + output combined)
session: Fresh implementation session — first action is reading this brief
architecture_lead: Winston (any architectural question routes to Winston via Andrew)
date: 2026-05-13
---

# Story 1.3 — Dev Harness with Primordial Bootstrap: Implementation Handoff Brief

## Your Role

You are the implementing engineer for Story 1.3. Winston (architect) and Andrew (PO) have locked the story scope and AC. **You do not have authority to change story scope, AC, or architectural contracts during implementation.** If you discover something that contradicts the contracts or the story AC, stop and surface it — do not improvise.

This is the FIRST non-spike story. The two preceding stories (1.1, 1.2) were research spikes; their findings inform your work but their code is not on your critical path. Your output is the foundation that Stories 1.4–1.8 build on.

## Operating Rules (Lattice Project)

- **Model tier:** Sonnet only. Halt and report if you detect Opus or Haiku.
- **No PRs.** After implementation + Winston review, commit directly to `main`.
- **Architecture is binding:** `_bmad-output/planning-artifacts/lattice-architecture.md` + `_bmad-output/planning-artifacts/data-contracts.md` are sources of truth. Story AC is the immediate target.
- **No git commits by you.** Run `go build ./...` and `go vet ./...` and exercise `make up` / `make verify-bootstrap` / `make down`; confirm they all pass. Winston + Andrew commit.
- **Token tracker update:** at session close, update `_bmad-output/implementation-artifacts/token-usage-tracker.md` Row 1.3 with actual usage, model used, session date, brief notes.
- **Dependencies:** Bypass permissions enabled; `go get`, `go mod tidy`, `docker`, `docker compose` all permitted.

## Story Scope (Copy from `epics.md` — Authoritative)

> As a developer,
> I want a local development environment that starts from a single command and arrives at a verified bootstrap state, so that all subsequent stories have a stable, reproducible substrate to build against.

**Recommended model tier:** Sonnet
**Estimated token budget:** ~95K

### Acceptance Criteria (from `epics.md` — read in full to confirm no drift):

**Given** a developer has Docker and Go installed and has run `git clone`
**When** they run `make up`
**Then** a NATS 2.12+ server (2.14 recommended) starts with JetStream enabled, all required KV buckets created (Core KV, Health KV, Capability KV, Weaver Claims KV, Weaver State KV, Token Revocation KV — note: idempotency tracker entries live in Core KV at `vtx.op.<requestId>` per Contract #4, NOT in a separate bucket), and all required JetStream streams created (`core-operations`, `ops.meta.>`, `ops.*`).

**And** the bootstrap sequence writes the primordial identity vertex (`vtx.identity.<bootstrapId>`), the platform actor vertex (`vtx.identity.platform`), the root DDL meta-vertex (`vtx.meta.root`), the **primary Capability Lens definition** (`vtx.meta.lens.capability`), and the **secondary capability role-coverage index Lens** (`vtx.meta.lens.capabilityRoleIndex`) directly to Core KV with correct document envelopes per Contract #1. Both Lens definitions include their cypher rule body and target adapter declaration (`nats-kv` → `capability` bucket).

**And** `make up` blocks until a readiness gate confirms Refractor (stub) has observed the bootstrap writes — the gate is satisfied when Health KV shows all bootstrap keys present.

**And** `make down` tears down all containers cleanly.

**And** warm restart (`make down && make up`) completes in under 30 seconds (NFR-P7).

**And** a `make verify-bootstrap` target runs assertions against Core KV confirming all primordial keys exist with correct envelopes, exits 0 on success and non-zero with a diff on failure.

## Required Context — Read Before Implementing

Read these sections (do NOT read whole files):

| File | Section | Why |
|---|---|---|
| `_bmad-output/planning-artifacts/data-contracts.md` | Contract #1 — Addressing Model & Document Envelope | Key patterns + envelope shape for all primordial writes |
| `_bmad-output/planning-artifacts/data-contracts.md` | Contract #5 — Health KV Convention | Health KV bucket conventions; bootstrap emits readiness signals |
| `_bmad-output/planning-artifacts/data-contracts.md` | Contract #6 — Capability KV Shape | Two Lens definitions you must seed; key patterns `cap.<actorId>` + `cap.role-by-operation.<operationType>` |
| `_bmad-output/planning-artifacts/data-contracts.md` | Contract #7 — Primordial Bootstrap | Canonical bootstrap inventory; "no direct Capability KV seeding" principle (Lens projects bootstrap capabilities from graph topology) |
| `_bmad-output/planning-artifacts/lattice-architecture.md` | "Deployment Isolation" and "KV Bucket taxonomy" tables | Bucket ownership map; ports; topology overview |
| `_bmad-output/planning-artifacts/epics.md` | Story 1.3 (canonical AC — confirm no drift) | Source of truth |
| `internal/spike/nats-batch/README.md` | "API Discovery Note for Story 1.7" + Test 1 contract implication | Bootstrap must create Core KV with `LimitMarkerTTL` set (translates to stream's `AllowMsgTTL: true`) and stream `AllowAtomicPublish: true` |

## Architecture Decisions Already Made (Winston) — Apply Without Re-Litigating

These were settled in planning and during the spike phase. The brief includes them so you don't waste tokens re-deriving:

1. **Bootstrap bypasses Processor.** `make up` writes directly to Core KV — this is the sole sanctioned exception to the "Processor is sole writer to Core KV" rule. After bootstrap completes, all subsequent writes flow through Processor.

2. **NO direct Capability KV seeding.** Per Contract #7, the Capability Lens projects capabilities from graph topology (roles, role-permission grants, identity-role assignments). You seed the GRAPH; the Lens populates Capability KV. The readiness gate must therefore wait for the Capability Lens to project the bootstrap actors' entries — but since Refractor is a stub in this story, the gate uses a simpler proxy: presence of all bootstrap keys in Core KV AND a Health KV signal `health.bootstrap.complete: true`.

3. **Idempotency Tracker entries live in Core KV** at `vtx.op.<requestId>` per Contract #4. Do NOT provision a separate "Idempotency Tracker KV" bucket. The bucket list above is the complete list.

4. **NATS 2.14** is the target version (image: `nats:2.14-alpine` or equivalent stable). Server config must enable JetStream.

5. **Core KV bucket creation** must use:
   - `LimitMarkerTTL` set on `KeyValueConfig` (translates to stream's `AllowMsgTTL: true`) — for per-key TTL support per Contract #4
   - Stream's `AllowAtomicPublish: true` — for atomic batch support; set via `js.UpdateStream` after KV creation since `CreateKeyValue` does not set it automatically (per Story 1.1 spike finding)

6. **Postgres image** is `postgres:16` or latest stable 16.x. No specific schema seeding required in Story 1.3 — Postgres is provisioned empty; Lens target tables are created by Story 2.3's Postgres adapter when business Lenses activate.

7. **NanoID for primordial IDs:** You need NanoIDs for the bootstrap identity and the bootstrap Capability Lens cypher targets. Story 1.4 builds the production `internal/substrate.NewNanoID()`. For Story 1.3's bootstrap, use a tiny inline NanoID generator (≤ 20 lines) following Contract #1's spec (20-char from 58-char custom alphabet excluding I/l/O/0). Alternatively, use deterministic fixed IDs for primordial entities since reproducibility across `make down` / `make up` cycles is required anyway — fixed IDs are acceptable. Document the choice in your closing summary.

8. **Capability Lens cypher rule body:** Per Contract #6, the primary Lens's RETURN clause produces entries conforming to the three-section model. Write the cypher rule body that produces:
   - `platformPermissions`: from `identity → assignedRole → role → grantsPermission → permission`
   - `serviceAccess`: from `identity → containedIn* → location → availableAt → service` (empty for now — no service topology seeded in Story 1.3 bootstrap)
   - `ephemeralGrants`: from direct task assignment + manager-via-reporting-chain (also empty for now — no tasks seeded)

The secondary `capabilityRoleIndex` Lens's RETURN produces `cap.role-by-operation.<operationType>` entries listing role names that grant each operation type. See Contract #6 §6.1 for the exact key pattern.

These cypher rule bodies are TEXT in the Lens definition's `cypherRule` aspect — Refractor doesn't parse them in Story 1.3 (Refractor is a stub here; openCypher parsing arrives in Story 3.1). For Story 1.3, the rule bodies need only be structurally valid cypher strings; downstream openCypher integration will validate them. Use the patterns from the Capability Lens design discussion (data-contracts.md Contract #6 + the Story 3.2 AC examples).

9. **Refractor stub** for the readiness gate: write a small Go program (`cmd/refractor-stub/main.go`) that does ONE thing — watches Core KV via durable consumer, and when it sees ALL primordial keys have arrived, writes `health.bootstrap.complete: true` to Health KV. That's the readiness gate. Real Refractor arrives in Story 2.1.

10. **Roles to seed at bootstrap (per Story 3.6 AC):** the five canonical role vertices — `vtx.role.consumer`, `vtx.role.frontOfHouse`, `vtx.role.backOfHouse`, `vtx.role.operator`, `vtx.role.platformInternal`. Each gets a `description` aspect. Permission grants link them to permission vertices — but since the full permission inventory is defined by downstream stories (3.6, 4.1, 5.1, etc.), Story 1.3 seeds ONLY the platform-internal role's permissions (the service actor's root-equivalent grants per the architecture's "Additional Requirements"). The other four roles are seeded as vertices with no permission grants yet; they'll be populated when their domain stories land.

## Library / Environment

- **Go:** 1.26.1 (root `go.mod` already exists; module `github.com/operatinggraph/lattice`)
- **NATS client:** `github.com/nats-io/nats.go v1.52.0` (already in go.mod from Story 1.1)
- **NATS server:** Docker image `nats:2.14-alpine` (or latest 2.14.x). Configure with `-js` flag for JetStream.
- **Postgres:** Docker image `postgres:16-alpine` or `postgres:16`. Set up with default `lattice` database, `lattice` user, password from `.env` file (which is `.gitignore`d).
- **Docker Compose:** YAML at repo root `docker-compose.yml`. Both services (nats + postgres) start together.
- **Makefile:** at repo root, with targets `up`, `down`, `verify-bootstrap` (at minimum).

## Suggested Layout

```
/Lattice
├── docker-compose.yml          NEW (this story)
├── Makefile                    NEW (this story)
├── .env.example                NEW (this story — committed; .env is gitignored)
├── cmd/
│   ├── bootstrap/              NEW: bootstrap binary, runs at make up
│   │   └── main.go
│   └── refractor-stub/         NEW: minimal readiness watcher
│       └── main.go
├── internal/
│   ├── spike/...               (already exists from 1.1/1.2)
│   └── bootstrap/              NEW: bootstrap logic (envelope construction, key generation, seeding)
│       ├── primordial.go       Inventory of primordial entities + their aspects
│       ├── lenses.go           Both Capability Lens definitions
│       ├── roles.go            Five canonical role vertices
│       └── envelope.go         Document envelope construction per Contract #1
└── scripts/
    └── verify-bootstrap.go     OR a Go test file under cmd/bootstrap/ — your call
```

Layout suggestion — refine as you go. Whatever you choose, document it briefly in the closing summary.

## Deliverables Checklist

At session close, the following MUST exist:

1. ✅ `docker-compose.yml` at repo root — nats + postgres services; ports + healthchecks; volumes for data persistence (or explicit ephemeral choice — document either)
2. ✅ `Makefile` at repo root with `up`, `down`, `verify-bootstrap` targets at minimum
3. ✅ `cmd/bootstrap/main.go` — runs at `make up` after containers are healthy; provisions buckets/streams; writes primordial data; exits 0 on success
4. ✅ `cmd/refractor-stub/main.go` — minimal watcher that writes `health.bootstrap.complete: true` once all primordial keys land
5. ✅ `internal/bootstrap/` package — modular bootstrap logic (primordial inventory, envelope construction, Lens definitions, role definitions)
6. ✅ Verify command (`make verify-bootstrap`) that exits 0 on success, prints a diff on failure
7. ✅ `.env.example` committed; `.env` (with real creds for local dev) is `.gitignore`d
8. ✅ Updated `.gitignore` if needed (Docker volumes, local data dirs, etc.)
9. ✅ `make up` from a clean state completes in under 3 minutes; warm `make down && make up` completes in under 30 seconds (NFR-P7)
10. ✅ All code compiles: `go build ./...` exits 0; `go vet ./...` exits 0
11. ✅ Updated row in `_bmad-output/implementation-artifacts/token-usage-tracker.md`
12. ✅ Brief written description of any layout choices, alternative implementations considered, or open questions for Winston — in the closing summary

## What Story 1.3 Is NOT

- **Not** the full Refractor — that's Story 2.1. The Refractor stub here only watches for bootstrap key arrival and writes one Health KV entry.
- **Not** the full openCypher engine — that's Story 3.1. The Capability Lens cypher rule bodies are stored as strings; nobody parses them in Story 1.3.
- **Not** the Processor — that arrives in Stories 1.5–1.8. Story 1.3's bootstrap writes directly to Core KV (sanctioned exception).
- **Not** a production NATS deployment — single-node single-cell is the Phase 1 target per NFR-R6.
- **Not** a comprehensive DDL inventory — only the primordial entities listed above. Domain DDL (identity, role-permission, operation types) is seeded by domain stories that follow.

## Escalation Path

If during implementation:

- A NATS or Docker API doesn't support what the AC assumes → **STOP, write a finding, escalate to Winston via Andrew.** Do not improvise.
- An architecture contract appears wrong → document in `cmd/bootstrap/CONTRACT-AMENDMENT-REQUEST.md` and continue with the spike-style scope. Winston resolves the contract change after the story lands.
- Token usage trending past 115K (20% over budget) → flag it before continuing.
- Docker provisioning takes substantively longer than expected (e.g., images don't pull due to registry issues) → stop, report environmental issue, do not invent workarounds.

## Closing the Session

Before ending:

1. Verify all 12 deliverables
2. Run `make up` from clean state; confirm completes under 3 min and `make verify-bootstrap` exits 0
3. Run `make down && make up` and time it; confirm under 30 seconds
4. Run `make down` and verify clean teardown (no orphaned containers, no volume leftovers if ephemeral was chosen)
5. Update the token tracker row
6. Return a closing summary listing all deliverables, timing measurements, any layout choices made, and any open questions

Do NOT commit. Winston + Andrew will review and commit.
