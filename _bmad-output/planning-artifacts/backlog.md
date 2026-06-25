# Lattice Backlog & Roadmap (Phase 3+)

**Owner:** Andrew (architect / planning lead). **Status:** living document.

This is the single consolidated backlog for everything deferred past the Phase 2 orchestration core,
plus the active next initiative. It supersedes the scattered deferral lists —
`lattice-architecture.md` "Open Items (Phase 3+)", `epics/index.md` "Deferred Architectural
Capabilities", and the per-component *Implementation status / Deferred* sections in
`docs/components/*` — which now point here. Frozen architecture *decisions* (e.g. the D1 read-path-auth
rubric) stay in `lattice-architecture.md`; this doc tracks *what to build next*, not *how it is designed*.

**Scales.** Importance: ★ low · ★★ medium · ★★★ high. Size: XS · S · M · L · XL (relative epic/story effort).

---

## Progress board

The single index of what is in flight and what has landed. Everything in the themed tables below
defaults to **📋 Backlog**; when an item is picked up it runs the normal loop (design → review → build →
review → commit) and surfaces here as **🏗️ Active → ✅ Done (commit)**. This board is the *index* —
per-item detail lives in design / story docs + git history, never in agent memory and never in a
`sprint-status` file (house rule).

| Item | Status | Ref |
|---|---|---|
| Loom control plane *(Loupe blocker #1)* | ✅ Done | `implementation-artifacts/loom-control-plane.md` |
| Loupe — view & control app | ✅ Done (v1 stab) | `implementation-artifacts/loupe-v1.md` |
| Large-file / binary handling — **v1a** (attach/read/detach) | ✅ Done (built + 3-layer-reviewed; merged to main) | `implementation-artifacts/large-file-binary-design.md` §1–§18 |
| Large-file / binary handling — **v1b** (GC) | ✅ Done (Option A: objectLiveness lens + Weaver directOp + epoch-CAS + object-store-manager; full Loop A+B convergence e2e green in CI; 3-layer-reviewed; merged to main; contracts #7 §7.2 + #10 §10.8 directOp.reads ratified) | `implementation-artifacts/large-file-binary-design.md` §20 |
| Refractor substrate inner-package migration | ✅ Done (Path B, d420ca4) | `implementation-artifacts/substrate-migration-plan.md` |
| Structured adapter result *(terminal-failure producer)* | ✅ Done (828f24d) | `implementation-artifacts/structured-adapter-result-design.md` |
| Async external-reply | ✅ Done (increments 1–3: 0860fb1, 0f85d45, 3504db6) | `implementation-artifacts/async-reply-design.md` |
| location-domain — spatial base package *(SL.1)* | ✅ Done (ae3a056) | `implementation-artifacts/service-location-design.md` |
| service-location — service-access authZ scheme *(SL.2)* | ✅ Done (e4af07c, 715b14b) | `implementation-artifacts/service-location-design.md` |
| _all other items_ | 📋 Backlog | see themed tables below |

---

## Active initiative — Loupe: the View & Control app *(first Edge Lattice prototype)*

> **Name:** *Loupe* (tentative). A jeweler's loupe is the tool you inspect a crystal through — apt for
> a window onto the lattice.

**What it is.** An internal **view-and-control client** for a running Lattice deployment: browse Core
KV (vertices / aspects / links), submit operations, install / uninstall capability packages, drive each
component's control plane (Refractor / Weaver / Loom), and observe Health KV. The first concrete UI on
top of the platform.

**Framing (the "a-ha").** Loupe is an **internal, trusted-operator tool**, but built *around the
Edge-node local-first machinery* — the same substrate + VAL mirror + reconcile-by-revision a real Edge
Lattice node would use — so it doubles as the **first prototype of Edge Lattice** without taking on the
Edge security layer. It is a stepping stone: prove the local-first view/control loop now; grow into the
per-user sovereign node later, once the deferred security pieces land.

**Non-goals (explicitly OUT — these stay Phase 3+).** Loupe runs as a **single trusted / privileged
identity** (like the CLI / admin), so this initiative does **not** build:

- per-user **authN / authZ**,
- the **Gateway**,
- **read-path authorization** (D1),
- **Personal Lens** / per-user filtering.

Loupe reads the **full** graph directly as a trusted client; per-user scoping is a later Edge evolution.

**Capabilities (v1).** Read Core KV + lens projections · submit ops (forms driven by DDL
self-description: `inputSchema` / `fieldDescription` / `examples`) · install / uninstall packages ·
Refractor / Weaver / Loom control ops · Health KV dashboard · view + upload large binaries (photos,
lease PDFs).

**Enabling work (the picked "Now" set) — ✅ all shipped (see the Progress board).**

| Enabling item | Why Loupe needs it | Imp | Size |
|---|---|---|---|
| **Loom control plane** | *Hard blocker.* Refractor + Weaver expose `lattice.ctrl.*` responders; Loom has none. Build `internal/loom/control` + `cmd/lattice/loom` + a `lattice.ctrl.loom.*` responder (list running instances, pause/resume consumers, inspect/fail an instance), mirroring `internal/weaver/control`. | ★★★ | M |
| **Large-file / binary handling** | Loupe shows + uploads profile photos and lease PDFs. NATS Object Store (chunked, content-addressed); the graph holds a pointer-aspect, the store holds the bytes; blobs never flow through the Refractor. *Authorization simplifies under the trusted-tool model* (binds to the trusted identity, not per-user). | ★★ | M–L |
| **Refractor substrate inner-package migration** | Hygiene + directly supports "around Edge machinery": ~30 `internal/refractor` files still hold raw `nats.go` / `jetstream` handles; a clean substrate boundary is what makes a local / embeddable node tractable. Needs substrate Watch / UpdatesOnly / NumPending / durable-consumer helpers first. | ★★ | M |

**Supporting / not blocking.** `UI Form Schema aspect` (brainstorming #52) would standardize form
rendering (DDL self-description already suffices for v1) · NATS **WebSocket** transport if Loupe is
browser-based (desktop / TUI / Electron use the native client) · Processor + Bridge have **no** control
plane — Loupe reads their Health instead (a minimal admin endpoint is optional, later).

**Open design questions for the epic.** Transport + host (desktop / TUI / Electron / browser-WS) ·
does Loupe embed a **local VAL mirror** via reconcile-by-revision (the Edge machinery) or read live
only · whether to add a thin read/query convenience surface (direct KV + lens reads work for v1).

---

## Now / near-term — ✅ shipped

The picked near-term set — **Loom control plane**, **Large-file / binary handling**, and **Refractor substrate
inner-package migration** — all shipped (see the Progress board). The Steward-triaged ride-along cleanups
turned out *not* to be clean small wins; they're parked at very low priority (see **Parking lot** at the
bottom).

---

## Deferred backlog (Phase 3+)

### Security & trust boundary
| Item | What it is | Imp | Size |
|---|---|---|---|
| Read-path authorization (D1) | Reads from lens targets (Postgres / KV) bypass the write-path Capability boundary. Rubric in `lattice-architecture.md` D1: Postgres RLS + a Capability-Read Lens; Gateway sets `lattice.actor_id`. Subsumes `cap.svc` serviceAccess read-auth. | ★★★ | L |
| Gateway | Edge trust boundary: JWT auth, `Lattice-Actor` stamping, read-path enforcement. Gates external actors + the real Edge node. | ★★★ | L |
| NATS account-level write restriction | Close the fabricated-KV-write surface at the substrate level (today defended only by overwrite-by-reprojection). | ★★ | M |

### Privacy / Vault
| Item | What it is | Imp | Size |
|---|---|---|---|
| Vault + crypto-shredding | Per-identity keys for sensitive aspects (SSN / DOB); right-to-be-forgotten = destroy the key; transient-session-key decryption for the Edge node; + the privacy failure tier (`KeyShredded` listener). | ★★★ | L |

### External-I/O maturity (bridge follow-ons)
| Item | What it is | Imp | Size |
|---|---|---|---|
| Real adapters + async result-return | Replace the `Fake*` adapters with real vendors and design the **asynchronous** result path. Today the bridge's `Adapter.Execute` is synchronous and must return a final `Result`; real checks / payments submit → pending ref → webhook/poll callback hours–days later. Needs (a) an inbound-result mechanism (webhook receiver, or poll via the `core-schedules` temporal lane), (b) an `Execute` contract that expresses "submitted, resolve later" (the bridge claim vertex stays open until the inbound result drives the replyOp), (c) a re-tuned wedged-claim horizon for legitimately-pending async claims. | ★★ | M–L |
| Structured adapter result | The bridge posts `{externalRef, result}` and the replyOp hard-codes `status="completed"` — there is no `failed` producer. Thread a structured pass/fail/detail status onto the reply; lens `missing_*` predicates key off the real status. | ★★ | S–M |
| Adapter read-seam / richer params | Adapters can only use what the target-lens row projects; a vendor needing extra subject fields (SSN / DOB) has no fetch path. Decide: richer projection columns vs. an adapter read seam. | ★★ | S–M |

### Scale-out
| Item | What it is | Imp | Size |
|---|---|---|---|
| Multi-cell / sharding | Graph scales by **cells** (root + subgraph co-located for atomic writes); global adjacency index + bridge links for cross-cell traversal; live migration as a dual-write shadow; multi-cell routing in Processor + Refractor. Keys already embed no cell identity (validated Phase 1). | ★ now / ★★★ at scale | XL |
| HA NATS clustering | Single-server today; clustering + multi-instance engine fan-out (several components note single-instance as a Phase-3 concern). | ★ now / ★★ prod | M–L |

### Edge & personal lenses *(the path Loupe grows into)*
| Item | What it is | Imp | Size |
|---|---|---|---|
| Personal / Secure Lens | Refractor projects a per-identity **security-filtered** subgraph stream; the "Interest Set" watchlist; RLS-style link filtering. Gates the real Edge node; intersects read-path auth. | ★★ | L |
| NATS-subject publish-events adapter | A Refractor target adapter that publishes projection deltas to `lattice.sync.user.<id>` subjects — required for Personal Lens fan-out (only NATS-KV + Postgres adapters ship today). | ★★ | S–M |
| Edge Lattice (full) | The sovereign per-user node: local VAL (SQLite / IndexedDB), local Starlark, offline-first, reconcile-by-revision, transient-key decryption of vaulted aspects. Loupe is its trusted-tool precursor. | ★★ | XL |

### AI-native
| Item | What it is | Imp | Size |
|---|---|---|---|
| AI-authored capabilities | A Lattice-aware agent proposes DDL / Starlark / lenses / workflows through human review + deterministic validation + rollback-friendly contracts. Marquee AI vision. | ★★–★★★ | L |
| L3 evaluator | Weaver's AI-assisted reasoning tier for ambiguous / novel convergence gaps (L1 / L2 ship today). | ★★ | M–L |
| Starlark guards (Loom) | The reserved `{reads, starlark}` guard escape hatch needs a verified-pure sandbox (the declarative grammar covers current flows). | ★ | M |

### Read-model / projection maturity
| Item | What it is | Imp | Size |
|---|---|---|---|
| Historical state query (FR51) | Operators query operational state across a configurable time range. | ★★ | M |
| Elasticsearch target adapter | A third lens target adapter (only NATS-KV + Postgres ship; no consumer yet). | ★ | M |
| Negative / filter-retraction projection | True "emit-only-when-violating" (Weaver targets currently project one row per candidate with a `violating` flag, avoiding retraction work). | ★ | M |
| Link-tombstone re-projection · cross-instance latency rollup | Two projection edge-cases / observability gaps (current approaches work). | ★ | S each |

### Refinements & ops
| Item | What it is | Imp | Size |
|---|---|---|---|
| Loom / Weaver control-API surfacing (beyond Loupe's needs) | Operator pause/resume + a durable `loom.*` read model beyond what the Loupe blocker covers. | ★ | M |
| Package version upgrade (F-004) | In-place re-install over an existing version + DDL migration semantics (install / uninstall exist; upgrade does not). | ★ | M |
| FR28 — role-queue + fallback | Assign tasks to a role queue with fallback (the demo uses direct identity assignment). | ★ | M |
| op-vertex pruner + `@every` schedules | GC of op-tracker vertices (#47 / #49) + recurring schedules (Phase 2 ships one-shot `@at` only). | ★ | M |
| Loupe live "system map" landing page *(Loupe)* | Landing view renders the running component + data-flow topology (the `architecture-overview` shape, deployed subset) as a **live** diagram — per-component/lens Health indicators, edge/link status, drill-in to vertices and control planes. Self-truthing: generated from Health KV + Core KV, not a static image. Base layer for the planned agent-activity operator console (`implementation-artifacts/agentic-ops-design.md`). | ★★ | M |
| Loupe agent-activity console *(Loupe)* | The ops layer atop the live system map: the Steward's queue + work in flight, the **L3 contract-review queue** (Andrew's touchpoint — structured what / why / affected-consumers, not raw uncommitted diffs), per-agent Health, and board state. The agents emit Health KV like components, so Loupe watching the platform watches the agents (dogfoods the dependency-watch). Operator surface for `implementation-artifacts/agentic-ops-design.md`. | ★★ | M |
| Conventions-linter — edit-time hook *(agentic-ops)* | ✅ Done. The 24 pre-existing `// Story N …` history-comments are swept and `STRICT=1 go run ./scripts/lint-conventions.go` is wired as a CI gate (`.github/workflows/ci.yml`); `go run ./scripts/lint-conventions.go --hook` now reads a `PostToolUse` stdin payload, scans the one edited `.go` file, and feeds advisory findings back via `hookSpecificOutput.additionalContext` (never blocks). Registration is a per-machine `.claude/settings.json` matcher (gitignored) — snippet in the script's doc comment. | ★ | XS |
| Version-control the agentic-ops role-skills *(agentic-ops)* | ✅ Done — canonical defs live in tracked **`agents/`** (`lamplighter`, `steward` + README); **`make install-skills`** copies them into the gitignored `.claude/skills/`. Edit in `agents/`, re-install. bmad tooling skills stay local. Owner skills land here as they're authored. | ★★ | S |

### Parking lot — very low priority (far, far back)

Real but low-value; the Steward should **not** spend design or build effort here unless Andrew explicitly
greenlights one (Steward triage 2026-06-24 — these were the "ride-along" cleanups that turned out not to be
clean small wins).

| Item | Why it's parked | Imp | Size |
|---|---|---|---|
| multi-aspect atomic OCC for `UpdateMetaVertex` | `meta_ddl.go` applies `expectedRevision` to the first changed aspect by design (each aspect has an independent NATS revision sequence); true atomic multi-key OCC needs a substrate per-key-revision primitive — M+, contract-adjacent — for marginal value. | ★ | M+ |
| freshnessExpiry marker tombstone-on-convergence | Per `packages/orchestration-base/mark_expired.go`, a converged marker is read by nothing and harmless; tombstoning buys cleanup not correctness + adds a convergence-edge write — near-zero value, Contract #10 §10.4-adjacent. | ★ | S |
| production freshness-window tuning | A staleness-tolerance vs. timer-churn value judgment — Andrew's call if/when it matters; not worth proactive effort. | ★ | XS |

---

## Done / moot — *not backlog*

- **Per-lens delete mode (Story 1.5.12)** — built; `deleteMode` (default hard) is in use across the
  task / ephemeral lenses.
- **§10.8 nudge-`operation` CAR** — moot: 13.5 retired the nudge GapAction; external remediation is now
  `triggerLoom` of an `externalTask` via the bridge.
- **Capability-Lens god-cypher → contract-contribution** — resolved (Epic 12).

---

*Consolidates and supersedes: `lattice-architecture.md` "Open Items (Phase 3+)" (OI-1 async adapters /
OI-2 large files carried here), `epics/index.md` "Phase 2+ Deferred Architectural Capabilities", and the
per-component "Deferred (Phase 3+)" sections.*
