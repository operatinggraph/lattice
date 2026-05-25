# Story 6.3: Deployment Isolation Specification (FR48)

Status: review

## Story

As a platform operator,
I want the Phase 1 deployment isolation model documented — each operator deployment runs its own NATS cluster with its own data and event streams, no cross-tenant access at the infrastructure level — so that the architectural claim is verifiable and the Phase 3 multi-cell scale-out path is clear.

---

## Spec Deviations (read first)

### SD-1 — Documentation destination: `docs/operations/` not `_bmad-output/` (OVERRIDES SPEC)

The epics.md §Story 6.3 AC1 says "a Deployment Isolation section exists" in `lattice-architecture.md`. **Reject this destination.** Per PO directive (2026-05-24), new living documentation goes to `docs/`, not to `_bmad-output/planning-artifacts/`.

**SD-1 Resolution:**
- Primary deliverable: `docs/operations/deployment-isolation.md` — the canonical deployment isolation reference. This file becomes the authoritative source for Phase 1 (and Phase 3 forward) isolation model, replacing any proposed inline extension to `lattice-architecture.md`.
- `_bmad-output/planning-artifacts/lattice-architecture.md` gets ONE pointer line added in a relevant section (e.g., after the "Multi-cell" or "Deployment" discussion): `> Deployment isolation model: see docs/operations/deployment-isolation.md (canonical).`
- This pointer is applied by Winston post-commit. Sub-agents MUST NOT edit `_bmad-output/planning-artifacts/` files. The brief flags it as a Winston-action item (see Winston Actions section below).

This mirrors the pattern established by Story 6.2 (`docs/observability/health-kv-schema.md`).

**No history comments in code.** The implementer MUST NOT add inline comments or prose that record when or why something was added — e.g., `// Story 6.3: added this`, `<!-- Added in Story 6.3 -->`, `> Previously X, now Y`, or `> Replaced old section Z`. Git history is the record of change; the document describes what the system IS now. Reviewers (Winston) will reject history comments at code-review time. This rule applies to ALL files touched by this story, including the new `docs/` document itself.

---

### SD-2 — Integration test deferred (OVERRIDES SPEC, explicit PO directive from Epic 6 kickoff)

The epics.md §Story 6.3 AC2 specifies an integration test that starts two `make up` instances on different ports and asserts cross-deployment isolation. **This is explicitly deferred** to Phase 1.5 / Phase 2 work.

**SD-2 Resolution:**
- AC2 (two-instance isolation test) is dropped from this story's acceptance criteria.
- AC4 (`make test-isolation` Makefile target) is dropped — it depends on AC2.
- Story 6.3 is a **documentation-only story**. Zero code changes. Zero new test files. Zero Makefile changes.
- The deferred test specification is preserved as a "Verification Path" appendix inside `docs/operations/deployment-isolation.md` itself. This makes the future test a documentation artifact even though the code is deferred. (See AC3 and the doc outline below.)

**Deferred AC text (verbatim from epics.md — kept here for future reference):**

> **Given** an integration test asserts isolation
> **When** the test runs
> **Then** two `make up` instances are started on different ports (A and B); an AI agent bootstrapped in A cannot connect to B's NATS with A credentials; an operation in A is not observable in B's Refractor; Postgres instances are disjoint.
>
> **And** cross-deployment isolation test wired into `make test-isolation`.

When Phase 1.5 / Phase 2 implements this test, it should reference this appendix as the specification and the deferred AC text above as the acceptance criteria.

---

## Acceptance Criteria

### AC1 — `docs/operations/deployment-isolation.md` documents the complete Phase 1 isolation model

The file must cover:
- One operator deployment = one NATS server (Phase 1, single-server per NFR-R6) or one NATS cluster (Phase 2+, HA mode)
- All Lattice components (Bootstrap, Processor, Refractor, CLI) for a deployment connect to **that deployment's NATS URL only**; there is no NATS subject or KV bucket shared across deployments
- Postgres Lens target is per-deployment (separate Postgres instance, no shared schema, no shared tables)
- Credential boundaries are per-deployment: `NATS_URL` and `REFRACTOR_PG_DSN` are the sole connection credentials for Phase 1; a component configured for deployment A cannot reach deployment B
- Explicit statement: **keys embed no cell identity** (NFR-SC2 — locked). Key naming (`vtx.<type>.<id>`, `lnk.<youngerId>.<name>.<olderId>`) is cell-agnostic by design (lattice-architecture.md P6). This is what makes Phase 3 multi-cell scale-out possible without data-model changes.
- Multi-cell scale-out path (NFR-SC2): same data model, no key changes; deployment isolation naturally extends to cell isolation when Phase 3 adds horizontal routing. Per-cell Refractor and Capability KV remain per-cell in Phase 3.
- Phase 3 multi-cell routes operations to specific cells via the Gateway layer (not yet shipped); per-cell Refractor instance and Capability KV bucket remain isolated per cell.

### AC2 — DEFERRED (see SD-2)

AC2 (integration test: two `make up` instances + cross-isolation assertions) is deferred to Phase 1.5 / Phase 2.

### AC3 — "Verification Path" appendix documents the future test

`docs/operations/deployment-isolation.md` includes an appendix section titled "Verification Path (Phase 2)" that describes:
- What the deferred integration test would assert (two instances on different ports, A-credentials-can't-reach-B-NATS, op-in-A-not-observable-in-B-Refractor, disjoint Postgres)
- The `NATS_PORT` and `POSTGRES_PORT` override mechanism already present in `docker-compose.yml` (the `${NATS_PORT:-4222}:4222` and `${POSTGRES_PORT:-5432}:5432` env overrides are the exact mechanism that enables running two stacks side-by-side)
- The expected `make test-isolation` target shape (deferred)

This appendix makes the future test specification a documentation artifact so the Phase 2 implementer has a complete spec to work from.

### AC4 — DEFERRED (see SD-2)

`make test-isolation` Makefile target is deferred to Phase 1.5 / Phase 2.

---

## Architectural Guardrails (non-negotiable)

**Guardrail 1 — Documentation goes to `docs/`, not `_bmad-output/`.**
The canonical isolation model lives in `docs/operations/deployment-isolation.md`. The only permitted change to `_bmad-output/` is the one-line pointer in `lattice-architecture.md`, and that is applied by Winston post-commit. Sub-agents NEVER edit planning artifacts.

**Guardrail 2 — No code changes.**
Story 6.3 is documentation only. The implementer MUST NOT modify any file under `cmd/`, `internal/`, `Makefile`, `docker-compose.yml`, or any Go source. `git diff --stat` at story close must show exactly ONE new file: `docs/operations/deployment-isolation.md`. If `git status` shows any file outside `docs/operations/` modified or created, stop and raise a CONTRACT-AMENDMENT-REQUEST.md.

**Guardrail 3 — Integration test is deferred.**
Do NOT implement `make test-isolation`. Do NOT create any `*_test.go` file. Do NOT add `test-isolation` to the Makefile. The Verification Path appendix in the doc is the full deliverable for the test specification.

**Guardrail 4 — Single source of truth.**
Content that lives in `docs/operations/deployment-isolation.md` must NOT be duplicated in `lattice-architecture.md` or `data-contracts.md`. Those files get one-line pointers only (applied by Winston). If the doc's content conflicts with or extends `lattice-architecture.md`, the doc wins for isolation-specific detail; `lattice-architecture.md` retains its current architectural principles text untouched.

**Guardrail 5 — No history comments.**
Standard rule (see SD-1 header). The new doc must read as current architectural truth, not a change record.

---

## `docs/operations/deployment-isolation.md` — Required Structure

The implementer MUST produce a document with this section structure (headings verbatim):

```
# Deployment Isolation

## Overview

## Phase 1 Topology: Single-Deployment, Single-Server

### NATS Isolation
### Postgres Isolation
### Credential Boundary

## Cell-Agnostic Key Design (NFR-SC2)

## Phase 3 Scale-Out Path: Multi-Cell

### Cell Topology
### Gateway Routing Layer
### Per-Cell Components

## Verification Path (Phase 2)
```

The sections must cover the substance described in each AC above. The precise prose is left to the implementer — but the section structure, terminology, and content scope are fixed by this brief. See the "Dev Notes" section for required content detail per section.

---

## Tasks / Subtasks

- [x] Task 1 — Author `docs/operations/deployment-isolation.md`
  - [x] 1.1 Create `docs/operations/` directory (first file in this sub-directory).
  - [x] 1.2 Write the document following the required section structure above.
  - [x] 1.3 Verify all AC1 content bullets are covered in the document (NATS isolation, Postgres isolation, credential boundary, NFR-SC2 key design, multi-cell path, Phase 3 Gateway routing, per-cell components).
  - [x] 1.4 Write the "Verification Path (Phase 2)" appendix section covering the deferred AC2 test spec.
  - [x] 1.5 Check that no prose in the document refers to when it was written or what story added it — no history language.
  - [x] 1.6 Confirm all terminology is consistent with `lattice-architecture.md` vocabulary (do not coin new terms — see "Terminology Consistency" in Dev Notes).

- [x] Task 2 — Architecture purity verification
  - [x] 2.1 `git diff --stat` shows exactly ONE new file: `docs/operations/deployment-isolation.md`. Zero other changes.
  - [x] 2.2 No file under `cmd/`, `internal/`, or root-level `Makefile` is modified.
  - [x] 2.3 No `_bmad-output/planning-artifacts/` file was touched. (Winston applies the pointer post-commit.)
  - [x] 2.4 Scan the new doc for history-comment language: no occurrences of "added in", "previously", "Story 6.3", "replaced", "as of".

---

## Dev Notes

### Overview

Story 6.3 is pure documentation. It expresses the deployment isolation model that already exists implicitly in the codebase — NATS URL and Postgres DSN are the only per-deployment connection credentials; components are wired at startup via environment variables; there is no cross-deployment NATS subject.

The story's value is making this model **explicit and testable-in-principle** via the Verification Path appendix, and establishing the NFR-SC2 key-agnostic guarantee as a first-class documented invariant.

No new infrastructure needed. No new interfaces. No new tests. One new Markdown file.

---

### Topology Facts (from codebase — write these accurately in the doc)

**NATS connection per component (confirmed from source):**
- Bootstrap (`cmd/bootstrap/main.go`): connects to `NATS_URL` (env, default `nats://localhost:4222`)
- Refractor (`cmd/refractor/main.go`): `flag.String("nats-url", envOr("NATS_URL", nats.DefaultURL), ...)` — single NATS URL at startup
- Processor (`cmd/processor/main.go`): same env-var pattern
- CLI (`cmd/lattice/`): `--nats-url` flag / `NATS_URL` env (Story 6.1)

All components use `substrate.Connect(ctx, substrate.ConnectOpts{URL: natsURL})` — a single connection to a single NATS server URL. There is no multi-NATS routing in Phase 1 code.

**Postgres connection (Refractor only):**
- `REFRACTOR_PG_DSN` env var (set in `make up` target: `REFRACTOR_PG_DSN="postgres://lattice:lattice_dev@localhost:5432/lattice?sslmode=disable"`)
- No other component connects to Postgres directly

**`docker-compose.yml` port isolation (the future test mechanism):**
- `${NATS_PORT:-4222}:4222` and `${POSTGRES_PORT:-5432}:5432` — these env overrides already allow running two independent stacks on separate ports. Document this as the mechanism the Phase 2 isolation test will use.

**Bootstrap JSON (`lattice.bootstrap.json`):**
- Written at `make up` time to `./lattice.bootstrap.json` (default path, `BOOTSTRAP_JSON_PATH` env override)
- Contains per-deployment NanoIDs for primordial entities — bootstrap is inherently per-deployment

---

### Required Content Per Section

**§ "Phase 1 Topology: Single-Deployment, Single-Server"**

Describe the Phase 1 deployment model in concrete terms:
- One NATS server process (single container, `nats:2.14-alpine`)
- One Postgres instance (`postgres:16-alpine`) for Lens target tables
- One bootstrap JSON (`lattice.bootstrap.json`) with per-deployment NanoIDs for primordial vertices
- All Lattice binaries connect to this single NATS URL at startup; changing `NATS_URL` at startup is the only configuration needed to point a component at a different deployment
- NFR-R6: single-server is acceptable for Phase 1 (development and portfolio demonstration); HA clustering is Phase 2+

**§ "NATS Isolation"**

- NATS KV buckets (`core-kv`, `health-kv`, `capability-kv`, `refractor-adjacency`) are scoped to their NATS server; they are not shared or replicated across deployments
- NATS JetStream streams (`core-operations`, `core-events`) are scoped to their NATS server
- No NATS "super-cluster" or "leaf node" configuration exists at Phase 1; deployments are fully network-isolated at the NATS level
- NFR-SC4: each operator deployment runs in its own isolated NATS cluster; no cross-tenant data access possible at infrastructure level

**§ "Postgres Isolation"**

- The Refractor's Postgres Lens target is per-deployment: a separate Postgres instance, separate `lattice` database, separate tables
- No shared schema, no shared Postgres user across deployments
- The Capability Lens projection table (if Postgres-backed) is per-deployment; a Capability KV read for deployment A has no path to deployment B's Postgres

**§ "Credential Boundary"**

- Phase 1 credential surface is minimal: `NATS_URL` (all components) and `REFRACTOR_PG_DSN` (Refractor only)
- A credential set configured for deployment A contains deployment A's NATS URL; a component started with A's credentials cannot reach B's NATS server (they are on different network endpoints)
- Phase 2+ will introduce mTLS with per-deployment certificates and NATS NKey/Creds authentication; the isolation guarantee becomes cryptographically enforced rather than network-topology enforced
- Phase 1 relies on network isolation (separate container networks / separate ports in dev); the Verification Path appendix describes how this is tested

**§ "Cell-Agnostic Key Design (NFR-SC2)"**

This is the most architecturally important section. Write clearly:
- Lattice key naming does NOT embed cell identity. Keys are:
  - Vertex: `vtx.<type>.<id>` — type is a domain type name, id is a NanoID
  - Aspect: `asp.<vtxId>.<name>` — owned by a vertex
  - Link: `lnk.<youngerId>.<name>.<olderId>` — two-vertex edge, IDs are NanoIDs
  - Meta: `vtx.meta.<NanoID>` with `.canonicalName` aspect
  - Op tracker: `vtx.op.<NanoID>`
- No key contains a cell prefix, a deployment prefix, or any topology-aware segment
- NFR-SC2 is LOCKED: this key design is an immutable contract. No future story may introduce cell-prefixed keys in Core KV.
- The consequence: a key written in one cell is valid as-is if that data were migrated to another cell — the routing layer (Gateway, Phase 3) handles topology, not the keys themselves
- This is Architectural Principle P6 from `lattice-architecture.md`: "Multi-cell is purely a routing/replication concern layered underneath — no data model or business logic changes required."

**§ "Phase 3 Scale-Out Path: Multi-Cell"**

- Multi-cell (Phase 3) adds horizontal scale without altering the Core KV data model
- Cell topology: each cell has its own NATS server (or cluster), its own Core KV, its own Refractor, its own Capability KV — the deployment isolation model of Phase 1 extends naturally to inter-cell isolation
- Gateway routing layer (Phase 3): a Gateway component (not yet shipped) routes incoming operations to the correct cell based on operator-defined routing policy; the operation payload and key shapes do not change
- Per-cell Refractor: each cell's Refractor projects only its cell's Core KV CDC; per-lens Capability KV and adjacency KV remain per-cell
- Cross-cell operations (Bridge Links, Phase 3): cross-cell references use the same key shapes; the Gateway resolves cross-cell link traversal. No key schema changes.

**§ "Verification Path (Phase 2)"**

- Overview: the Phase 1 isolation claim is currently validated by topology (separate NATS URLs) rather than by an automated test. This appendix specifies the future integration test.
- Mechanism: `docker-compose.yml` already exposes `NATS_PORT` and `POSTGRES_PORT` env overrides (`${NATS_PORT:-4222}:4222`, `${POSTGRES_PORT:-5432}:5432`). Two deployments can be started on non-overlapping ports:
  - Deployment A: default ports (4222 / 5432)
  - Deployment B: `NATS_PORT=4232 POSTGRES_PORT=5442 make up`
- Future test assertions (deferred to Phase 2, not implemented in this story):
  1. Bootstrap deployment A; bootstrap deployment B independently.
  2. Assert: a Refractor started with `NATS_URL=nats://localhost:4222` cannot connect to `nats://localhost:4232` (wrong NATS URL = connection refused or auth failure).
  3. Assert: an operation submitted in deployment A is visible in A's Refractor projection but NOT in B's Refractor projection (independent Core KV, independent CDC consumers).
  4. Assert: A's Postgres tables are populated; B's Postgres tables contain only B's data.
  5. Assert: A's `lattice.bootstrap.json` contains different NanoIDs than B's (deployments are identity-distinct).
- Proposed `make test-isolation` target (deferred):
  ```
  test-isolation:
      NATS_PORT=4232 POSTGRES_PORT=5442 make up   # start deployment B on alt ports
      go test -tags integration ./internal/isolation/... -v -timeout 120s
      NATS_PORT=4232 POSTGRES_PORT=5442 make down  # tear down deployment B
  ```
- This target and its test file (`internal/isolation/isolation_test.go`) are NOT created by Story 6.3. They are Phase 2 work.

---

### Terminology Consistency

Use only vocabulary already established in `lattice-architecture.md` and `data-contracts.md`. Do NOT coin new terms. In particular:

| Use this | Not this |
|---|---|
| "operator deployment" | "tenant", "instance", "environment" |
| "NATS server" (Phase 1) or "NATS cluster" (Phase 2+) | "NATS node", "NATS broker" |
| "Lens projection" | "view", "materialized view", "query surface" |
| "Core KV" | "core key-value store", "core state store" |
| "Capability KV" | "auth cache", "permission store" |
| "cell" | "shard", "partition", "region" (cells are the Lattice-specific term) |
| "Gateway" | "router", "proxy", "load balancer" |
| "Refractor" | "projector", "materializer" (Refractor is the component name) |

The terms "cell-agnostic", "deployment isolation", and "Verification Path" are introduced by this document and are acceptable.

---

### Files to Create

```
docs/operations/
  deployment-isolation.md    — primary deliverable (AC1, AC3)
```

That is the only file this story creates.

---

### Files NOT to Touch

- `_bmad-output/planning-artifacts/lattice-architecture.md` — sub-agents NEVER edit planning artifacts. Winston applies the one-line pointer at commit review.
- `_bmad-output/planning-artifacts/data-contracts.md` — same rule. If a pointer is needed, Winston adds it.
- `_bmad-output/planning-artifacts/epics.md` — read-only reference.
- `Makefile` — no `test-isolation` target in this story.
- `docker-compose.yml` — no changes; the existing port-override mechanism is already there.
- `cmd/` — no binary changes.
- `internal/` — no Go source changes.
- `docs/components/` — no changes to existing component pages.
- `docs/observability/health-kv-schema.md` — no changes.

---

### Winston Action Items (post-commit, Winston applies)

These are NOT sub-agent deliverables. They are reminders for Winston after committing Story 6.3:

1. **`_bmad-output/planning-artifacts/lattice-architecture.md`** — add one pointer line in the section discussing multi-cell / P6 or deployment topology. Suggested location: after Principle P6 ("Single-cell MVP is safe because the data model is cell-agnostic"): `> Deployment isolation model and Phase 3 scale-out path: see docs/operations/deployment-isolation.md.`
2. **`_bmad-output/planning-artifacts/data-contracts.md`** — optionally, add one pointer in §NFR or §Cross-cutting section if a deployment isolation callout exists there. Low priority; the `lattice-architecture.md` pointer is sufficient.

---

### Architecture Compliance Checklist

- [x] `docs/operations/deployment-isolation.md` exists with all 7 required sections
- [x] All AC1 content bullets are addressed in the document
- [x] "Verification Path (Phase 2)" appendix section covers the deferred AC2 test spec
- [x] No file under `cmd/`, `internal/`, `Makefile`, `docker-compose.yml` modified
- [x] No `_bmad-output/planning-artifacts/` file touched
- [x] No history-comment language in the document (`git grep -i "story 6.3\|added in\|previously\|replaced" docs/operations/deployment-isolation.md` → zero hits)
- [x] Terminology is consistent with established vocabulary (see table above)

---

### References

- [Source: `_bmad-output/planning-artifacts/epics.md` §Story 6.3, lines 1489–1519] — original AC (AC1, AC2 deferred, AC3, AC4 deferred)
- [Source: `_bmad-output/planning-artifacts/epics.md` lines 121, 144–147] — NFR-R6, NFR-SC1, NFR-SC2, NFR-SC3, NFR-SC4 definitions
- [Source: `_bmad-output/planning-artifacts/lattice-architecture.md` §Architectural Principles P6] — "Single-cell MVP is safe because the data model is cell-agnostic"
- [Source: `_bmad-output/planning-artifacts/lattice-architecture.md` §KV Bucket Taxonomy] — bucket ownership table; Health KV pattern
- [Source: `cmd/refractor/main.go`] — NATS URL flag pattern; single-connection topology
- [Source: `cmd/bootstrap/main.go`] — `NATS_URL` env; `BOOTSTRAP_JSON_PATH`; per-deployment NanoID seeding
- [Source: `docker-compose.yml`] — `${NATS_PORT:-4222}`, `${POSTGRES_PORT:-5432}` port-override mechanism
- [Source: `Makefile` `up` target] — `REFRACTOR_PG_DSN` env; component startup sequence
- [Source: `docs/components/_index.md`] — `docs/` as living documentation destination
- [Source: `_bmad-output/implementation-artifacts/6-2-health-kv-schema-completeness.md`] — SD-1 docs-relocation pattern; brief structure template
- [Source: `_bmad-output/implementation-artifacts/WINSTON-RESUME.md`] — operating conventions, no-history-comment rule, sub-agents never edit planning artifacts

---

## Implementation Tier & Budget

**Model tier: Sonnet** (documentation-only; no code; bounded scope).
**Estimated token budget: ~50K** (tracking only, NOT enforced per Rule 8 in WINSTON-RESUME.md). This is a documentation-only story — input context dominates; output is one Markdown file. Sub-agent self-reports are typically 20-30% low vs outer telemetry; trust the task-notification `total_tokens`.

---

## Stuck-Loop Halt Criteria

Halt and surface for Winston review if any of the following occur:
- Implementing AC1 requires understanding a component's NATS connection behavior that is NOT visible from `cmd/*/main.go` (e.g., multi-NATS routing found in `internal/substrate/`). Stop and file a CONTRACT-AMENDMENT-REQUEST.md describing the discrepancy.
- Any implementation path leads toward modifying a Go source file, a Makefile target, or a `_bmad-output/` planning artifact. Stop immediately — this is a Guardrail 2 violation.
- The "Verification Path" appendix requires resolving ambiguity about the `docker-compose.yml` multi-stack mechanism that cannot be resolved by reading the file directly. Stop and ask Winston.

Do NOT halt for token budget alone.

---

## Dev Agent Record

### Agent Model Used

claude-sonnet-4-6

### Debug Log References

None — implementation was straightforward: documentation only, no code changes, no ambiguities that required halting.

### Completion Notes List

- Created `docs/operations/` directory (new sub-directory).
- Authored `docs/operations/deployment-isolation.md` (1,414 words) with all 7 required sections in exact heading order.
- AC1 coverage confirmed: NATS isolation (scoped KV buckets and streams), Postgres isolation (per-deployment instance + DSN), credential boundary (NATS_URL + REFRACTOR_PG_DSN), NFR-SC2 cell-agnostic key design with locked invariant statement, multi-cell Phase 3 path, Gateway routing layer, per-cell components.
- AC3 coverage confirmed: "Verification Path (Phase 2)" appendix documents the port-override mechanism in docker-compose.yml (`${NATS_PORT:-4222}` / `${POSTGRES_PORT:-5432}`), the five future test assertions, and the proposed deferred `make test-isolation` target.
- `go build ./...` — clean, no regressions.
- History-comment scan (`git grep -i "story 6.3|added in|previously|replaced|as of"`) — zero hits.
- `git status` shows exactly two new untracked items: the story brief itself and `docs/operations/` — zero modifications to existing files.
- No `_bmad-output/planning-artifacts/` files touched; Winston pointer actions remain pending.
- All terminology consistent with `lattice-architecture.md` vocabulary (operator deployment, NATS server, Lens projection, Core KV, Capability KV, cell, Gateway, Refractor).

### File List

- `docs/operations/deployment-isolation.md` (new)
