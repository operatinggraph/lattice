# Backlog — Lattice (Stream 2): features + component maintenance

The **Lattice** lane: platform **features** + **component maintenance**. Advanced by the **Lattice Steward**
(`steward-autonomous`) **round-robin across components** (Core / Weaver / Loom / Refractor / Loupe +
cross-cutting features); hydrated by the **Surveyor** (`platform-surveyor`) **+ PO-routed platform gaps**
(demand the verticals surface). Scales, status vocabulary, cross-lane rules: see [../backlog.md](../backlog.md).
Design: `implementation-artifacts/agentic-ops-swimlanes-design.md`.

Status: 📋 ready · 🏗️ in-flight (worktree) · ✅ done (commit) · 📐 design-proposed · 🚧 blocked.

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


## Component maintenance

Grounded, definition-of-ready maintenance/observability items surfaced by the **Surveyor** from the
component code + docs + Health/CI signals. Tagged by component; the Steward round-robins these alongside
features. None below needs a frozen-contract change or is an architectural fork.

| Item | What + why (grounding refs) | Imp | Size | Status |
|---|---|---|---|---|
| **[Refractor] Capability-Lens liveness/lag alert** | ✅ **Done** — the Refractor instance heartbeat now implements the Contract #5 §5.5 `issues[]` + §5.4 `status` anomaly channel (previously hard-coded healthy/empty) driven by auth-plane (`projection.IsAuthPlane`, `capability-kv`) lens liveness: a **paused** capability lens → `CapabilityLensPaused` (error ⇒ `status:unhealthy`), an **active** lens with `consumerLag` over threshold (default 100, overridable) → `CapabilityLensLagging` (warning ⇒ `status:degraded`); `metrics.capabilityLens.<name>` `{status,consumerLag,alert}` emitted every cycle; `since` persists across heartbeats, drops on resolve. Read-only (no authz path / Core KV / projection touched). **No contract change** — the §5.5 schema already existed. The **Lamplighter** classifies it now. Design + thorough-lead-review: `implementation-artifacts/refractor-capability-lens-liveness-alert.md`. | ★★ | S–M | ✅ done |
| **[Weaver] Heartbeat status/issue-severity inconsistency** | ✅ **Done** (4de7677) — the heartbeater hard-coded `status` to the lifecycle string, so a heartbeat carrying issues still reported `status:"healthy"` (self-inconsistent with Contract #5 §5.2/§5.3: issues empty iff healthy; warning⇒degraded, error⇒unhealthy). `aggregateStatus(lifecycle, issues)` now escalates the steady-state status to the worst open issue severity (error wins over warning); `"starting"`/`"shutdown"` are reported verbatim (lifecycle phase, not a health grade). Both halves of the original decision now closed: per-target data gaps were already downgraded to `severity:"warning"` (1bcdce4), and the source `status` emission is now consistent. Read-only Health-KV self-report; no Core KV / contract change. Tests: `TestAggregateStatus` table + e2e now asserts `status:"unhealthy"` alongside the error issues it checks. Mirrors Refractor's `aggregateStatus` (with `"starting"` also protected — §5.3-defensible, transitions are author's discretion). | ★★ | S | ✅ done |
| **[Refractor] Postgres adapter `Truncater`** | The Postgres adapter doesn't implement `adapter.Truncater`, so a `Pipeline.Rebuild(truncate=true)` against a SQL-target lens **silently skips** the truncate (warn at `internal/refractor/pipeline/pipeline.go:400`) — stale rows survive a rebuild. NATS-KV is the only adapter with truncate today (`adapter/natskv.go:17`). Add `TRUNCATE`/`DELETE FROM` on the target table so SQL-target rebuilds clear cleanly. **No live impact yet** (no Postgres-target lens ships — verified), but a prerequisite for the deferred **Postgres read-model**. Refs: `adapter/adapter.go:23` (the `Truncater` iface), `adapter/postgres*.go`, `pipeline/pipeline.go:394-400`. | ★ (★★ once a Postgres-target lens ships) | S | 📋 ready |
| **[Refractor] Doc drift — `consumer.Manager` retired** | ✅ **Done** — `docs/components/refractor.md` sub-package table + lens-lifecycle step 5 now describe the live design: per-lens durable consumers are owned by each `pipeline.Pipeline` via `substrate.ConsumerSupervisor` (durable `refractor-<ruleID>`); the `consumer/` package holds only the adjacency `Bootstrapper`. Pure doc fix. | ★ | XS | ✅ done |
| **[Core] Processor health: fabricated `lane_lag` + no status/issues anomaly channel** | The Processor heartbeat (`internal/processor/health.go:156`) emits `metrics.lane_lag` as a **hardcoded** `{default:0, meta:0, urgent:0, system:0}` map and always sets `Status` to the lifecycle string passed to `emit()` with `Issues: []any{}` — so a backed-up write lane (the #1 Processor failure mode, on the **sole Core-KV writer / P2 critical path**) is **invisible** to the Lamplighter, and the reported zeros are worse than omission because the watcher trusts them. **Completes the health-honesty sweep** already shipped on Refractor (capability-lens liveness) + Weaver (`aggregateStatus`); the Processor is the third, highest-stakes self-reporter. Fix: surface the real consumer backlog from the durable consumer's `NumPending` (the heartbeater holds `conn`/`bucket`; the consumer handle from `EnsureConsumer` must be threaded in — `commit_path.go:648`), and derive `status`/`issues[]` via a `aggregateStatus`-style helper (lag > threshold ⇒ `ProcessorLaneLagging`/degraded). **Design note for the Steward/Designer:** the Processor runs **one** durable consumer (`processor-main`) over `ops.>` (`step1_consume.go`), so a per-lane breakdown is aspirational — report the honest aggregate `pending` (or drop the per-lane keys to `null`) rather than fabricating four zeros; true per-lane lag would need per-subject `NumPending` introspection or per-lane consumers (out of scope). **No contract change** — Contract #5 §5.4 already lists `lane_lag` as a recommended Processor metric; §5.3/§5.5 already define the `status`/`issues[]` channel. Refs: `internal/processor/health.go:142-167`, `internal/processor/step1_consume.go`, `commit_path.go:648`; mirror `internal/refractor`/`internal/weaver` `aggregateStatus`. | ★★★ | S–M | 📋 ready |
| **[Core] Bootstrap key construction → substrate key helpers** | Bootstrap builds aspect keys by raw string concatenation — `MetaRootKey + ".canonicalName"`, `+ ".permittedCommands"`, etc. in `internal/bootstrap/primordial.go` (and `install_ddl.go`/`meta_ddl.go`/`system_actors.go`) — instead of the canonical `substrate.AspectKey(vtxKey, localName)` / `VertexKey` / `LinkKey` helpers (`internal/substrate/keys.go`). Hygiene only: keys are correct today, but the concat pattern is exactly the Contract #1 key-shape duplication the helpers exist to centralize, and an off-by-a-dot here would mis-seed the primordial graph silently. **Explicitly flagged as a clean follow-up** by the (resolved) `internal/substrate/CONTRACT-AMENDMENT-REQUEST.md` ("bootstrap could now adopt `substrate.VertexKey`/`AspectKey`/`LinkKey`… recommended as a future bootstrap-substrate alignment cleanup") — never done. No contract change, no behavior change (helper output is byte-identical). Refs: `internal/bootstrap/primordial.go:415-424`, `internal/substrate/keys.go:33-60`. | ★ | XS–S | 📋 ready |

### Survey log (round-robin rotation)

_The Surveyor notes each run here so the next run rotates to the least-recently-surveyed component._

- **2026-06-26 — Refractor** (`internal/refractor` + `cmd/refractor` + `docs/components/refractor.md`).
  Substrate migration **verified clean** — raw `nats.go`/`jetstream` confined to `control/service.go` (the
  accepted `micro.Service` exception); the residual `cmd/refractor` `js.CreateKeyValue` is Andrew-adjudicated
  (provisioning belongs to bootstrap, no `substrate.EnsureKV`). Codebase debt near-zero (no real TODO/FIXME;
  active de-flaking — 489c64a, 715b14b). Filed 3 grounded items above; remaining Refractor deferreds
  (link-tombstone re-projection, cross-instance latency rollup, Elasticsearch adapter, negative/retraction
  projection, lens-target write restriction) already tracked in the Deferred backlog. **Next rotation:** Core
  (`internal/processor`/`bootstrap`/`substrate`) — the next-stalest un-surveyed component (Loupe's lane is
  already saturated).
- **2026-06-26 — Core** (`internal/processor` + `internal/bootstrap` + `internal/substrate`; stalest by
  `git log` — processor last touched ~5d before Weaver). **No real TODO/FIXME** in any of the three packages
  (clean). The `substrate/CONTRACT-AMENDMENT-REQUEST.md` is RESOLVED (2026-05-13) but flagged one un-done clean
  follow-up (bootstrap→substrate key helpers — filed). The `processor.md` "What's deferred" items (read-path
  auth, multi-cell, NATS account auth, multi-aspect OCC) all already live in the feature backlog / parking lot
  — not re-filed. Filed **2 grounded items**: the high-value Processor health-honesty completion (fabricated
  `lane_lag` / no anomaly channel — finishes the Refractor+Weaver sweep, ★★★) and the low bootstrap key-helper
  hygiene (★). Neither needs a frozen-contract change. **Next rotation:** Loom (`internal/loom`) — the
  next-stalest non-Core component (Refractor/Loupe/Weaver are all fresher and recently surveyed/built).

---


## Lattice feature backlog — the Phase-3 build queue

> Formerly "Deferred (Phase 3+)". These are **no longer deferred** — the AI-driven flywheel **actively draws
> from this list**: the **Surveyor** files + scores demand here, the **Designer** (`lattice-designer`) turns the
> items into design docs **flagged for Andrew to ratify**, and the **Lattice Steward** builds the ratified ones.
> **Almost everything below needs design and is fair game** (importance-ordered, ambition encouraged) — the
> **only** exclusion is a row marked **🚧 Andrew-gated** (a standing block/shelve — currently just the one
> shelved Loupe agent-activity console). Architectural **forks** (Gateway, read-path auth, Vault, multi-cell,
> HA-NATS) and **frozen-contract** changes are still **designed through** — the Designer preps the design + the
> uncommitted contract edit and explains the fork — but the *fork decision* and the *contract commit* are
> Andrew's (see the cross-lane rules in [../backlog.md](../backlog.md)).

### Security & trust boundary
| Item | What it is | Imp | Size | Status |
|---|---|---|---|---|
| Read-path authorization (D1) | Reads from lens targets (Postgres / KV) bypass the write-path Capability boundary. Rubric in `lattice-architecture.md` D1: Postgres RLS + a Capability-Read Lens; Gateway sets `lattice.actor_id`. Subsumes `cap.svc` serviceAccess read-auth. | ★★★ | L | 📐 **awaiting-Andrew** — design: [`read-path-authorization-d1-design.md`](../../implementation-artifacts/read-path-authorization-d1-design.md). Designs through the enforcement-boundary fork (Postgres-RLS vs read-gateway-over-NATS-KV) + the authN/Gateway seam; uncommitted Contract #6 §6.14 edit staged for ratification. |
| Gateway | Edge trust boundary: JWT auth, `Lattice-Actor` stamping, read-path enforcement. Gates external actors + the real Edge node. | ★★★ | L | The **read-actor auth seam** (JWT→`lattice.actor_id`, token-revocation KV) is designed as D1 increment 1 in the row above; the full internet-facing Gateway (NGINX/Envoy hardening, IdP) is the deferred sibling. |
| NATS account-level write restriction | Close the fabricated-KV-write surface at the substrate level (today defended only by overwrite-by-reprojection). | ★★ | M | 📋 |

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
| **CI pipeline speed (continuous)** *(agentic-ops / CI)* | Make CI **faster without weakening any gate** — owned continuously by the **Whetstone** (`ci-whetstone`): the single serial `ci.yml` job (~20 min: build→vet→lint→`make up`→verify-kernel→8×verify-package→Gate2/3→hello-lattice→lease-convergence→object-gc→full `go test -p 4`) → a **parallel job matrix**; Go build/module **caching**; safe test-parallelism raises; redundant stack spin-ups pruned; slow/flaky tests fixed. **Every gate still runs; flakiness never rises; proof = measured CI wall-clock ↓.** | ★★ | M (ongoing) |
| Loom / Weaver control-API surfacing (beyond Loupe's needs) | Operator pause/resume + a durable `loom.*` read model beyond what the Loupe blocker covers. | ★ | M |
| Package version upgrade (F-004) | In-place re-install over an existing version + DDL migration semantics (install / uninstall exist; upgrade does not). | ★ | M |
| FR28 — role-queue + fallback | Assign tasks to a role queue with fallback (the demo uses direct identity assignment). | ★ | M |
| op-vertex pruner + `@every` schedules | GC of op-tracker vertices (#47 / #49) + recurring schedules (Phase 2 ships one-shot `@at` only). | ★ | M |
| Loupe agent-activity console *(Loupe)* | 🚧 **Andrew-gated (shelved 2026-06-25)** — read-seam options rejected; revisit once the dependency map + ops-state data-home mature. The design is retained (`implementation-artifacts/loupe-agent-activity-console-design.md`); **do not design or build now.** *(What it would be: the ops layer atop the live system map — the Steward's queue + work in flight, the L3 contract-review queue, per-agent Health, board state.)* | ★★★ | M |

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


---

## Done log — lattice (newest first)

_Append shipped Stream-2 items here (this lane only — keeps the board collision-safe)._

- **2026-06-26 — Backlog retitle + sweep + two new roles** — the feature backlog section "Deferred (Phase 3+)" → **"Lattice feature backlog — the Phase-3 build queue"** (the flywheel now actively draws from it; "deferred" framing retired). Swept the feature queue clean of shipped ✅ rows already recorded in git / the index (Loupe live system-map landing page, the conventions-linter edit-time hook, version-control of the role-skills) and marked the Loupe agent-activity console **🚧 Andrew-shelved**. Added two concurrent roles to Stream 2: the **Designer** (`lattice-designer`, `agents/designer/`) — architect persona, turns backlog items into design docs flagged for Andrew to ratify, ahead of the Steward — and the **Whetstone** (`ci-whetstone`, `agents/whetstone/`) — make CI faster + kill flaky tests, without weakening any gate. Pipeline is now Surveyor → Designer → (Andrew ratifies) → Lattice Steward, with the Whetstone as a cross-cutting CI-speed/flake loop.
- **2026-06-26 — [Weaver] Heartbeat status/issue-severity inconsistency** (4de7677) — closes the source-side half of the Loupe-surfaced inconsistency: Weaver's heartbeater hard-coded `status` to the lifecycle string, emitting `status:"healthy"` while carrying issues (violates Contract #5 §5.2/§5.3 — issues empty iff healthy). New `aggregateStatus` escalates steady-state status to the worst open issue severity (error⇒unhealthy, warning⇒degraded); `"starting"`/`"shutdown"` reported verbatim. Read-only Health-KV self-report, no contract change. `TestAggregateStatus` table + e2e asserts `status:"unhealthy"` with the error issues. Earlier 1bcdce4 had already downgraded per-target data gaps to `warning` (the other half), so the channel is now fully consistent.
- **2026-06-26 — [Loupe] Surface component `status`/`issues` on health + system map** (2877a1c) — closes the value chain of the Refractor liveness alert: Loupe's health cards + system-map nodes now honor the Contract #5 §5.4 `status` / §5.5 `issues[]` anomaly channel (previously freshness-only). `componentLiveness` (shared by `computeHealth`/`computeSystemMap`) fuses freshness + reported status + worst issue severity (error→red, warning→yellow), so a component is surfaced honestly even when its status field lags its issues. **In-browser-verified on the live stack** — caught Weaver self-reporting `healthy` with two `error` TemplateDataError issues, now rendered unhealthy/red. FE: degraded/unhealthy card+node styling, status tags, error-issue red text, red-overall summary counts unhealthy. Read-only inspector path, no contract change. 11 new tests. Filed Weaver self-report-inconsistency follow-up.
- **2026-06-26 — [Refractor] Capability-Lens liveness/lag alert** — instance heartbeat now implements the Contract #5 §5.5 `issues[]` / §5.4 `status` anomaly channel for auth-plane (`capability-kv`) lenses: paused → `CapabilityLensPaused` (unhealthy), over-threshold lag → `CapabilityLensLagging` (degraded); `metrics.capabilityLens` always emitted; `since` persistence + resolve-drop. Read-only, no contract change. 13 tests (9 pure + 4 e2e shape). Filed a Loupe follow-up (surface component status/issues). Design: `implementation-artifacts/refractor-capability-lens-liveness-alert.md`.
- **2026-06-26 — [Refractor] Doc drift `consumer.Manager` retired** — `docs/components/refractor.md` corrected (sub-package table + lens-lifecycle step 5): per-lens durable consumers are owned by `pipeline.Pipeline` via `substrate.ConsumerSupervisor`, not a `consumer.Manager`. Pure doc, direct to main.
