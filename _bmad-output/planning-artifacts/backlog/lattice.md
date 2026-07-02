# Backlog — Lattice (Stream 2): features + component maintenance

Stream 2 = platform features + component maintenance. Pipeline: **Surveyor** files scored demand →
**Designer** turns items into design docs flagged for Andrew → **Lattice Steward** builds the ratified ones;
the **Whetstone** keeps CI fast cross-cutting. Written by the Lattice Steward + Surveyor (+ Whetstone CI rows,
+ PO-routed platform gaps) only. Index + cross-lane rules: [../backlog.md](../backlog.md).

## How this board works (read before editing — the row discipline)

**The board is an INDEX, not a journal.** One item = one row; the detail lives where the work lives.
A lint gate (`scripts/lint-board.go`, run in CI + before any board commit) enforces the budgets below —
**a fire that bloats a row or section fails the gate.**

- **A row is** `Item · What it is (one line) · Imp · Size · State` — **aim ≤ 300 chars, hard cap 600.** The
  **State** cell = a **token** + a **link to the design doc / commit** + (only if 🏗️) **one ≤10-word next
  step**. Nothing else.
- **The fire's narrative goes in the COMMIT MESSAGE + the design doc — NEVER the board** (the CLAUDE.md
  no-changelog rule). Do **not** put in a cell: design rationale / fork-resolution / "why I chose this",
  adversarial findings, the fire-by-fire journal, commit SHAs-with-prose, coverage %, review depth, "Was: …".
  A multi-fire checkpoint (worktree · done · next) lives in the **design doc**; the row carries a one-line
  pointer. **The four ways this regressed after the 2026-06-29 reform — refuse each by name:**
  - ✗ **Design summary in State** (*"steward impl-ratified the fork → package rolling-@at … @every stays
    reserved … Build: Inc 1 → Inc 2"*). ✓ `🏗️ building · [design](…) · next: Inc 1 series-state lens`.
  - ✗ **Blocked-reasoning essay** (*"blocked-on Vault because .demographics are PHI, test-enforced, clinic is
    the Vault forcing function, NOT ready as filed"*). ✓ `🚧 blocked-on Vault (PII projection) · [why](design)`.
  - ✗ **Survey-log / PO-notes fire-journal** (a multi-line narrative of what the fire did). ✓ one dated line:
    `2026-06-30 Refractor — healthy; filed 2 (simple-engine retire, fan-out cov)`. Narrative → the commit.
  - ✗ **Multi-sentence Done-log entry.** ✓ exactly one line: `date · SHA · [tag] title`.
- **Capped sections** (the lint enforces): **Survey-log / PO-notes ≤ 12 dated one-liners** — rotation memory
  only (what was surveyed/exercised, what's next), never a per-fire log; **Done-log ≤ 25 one-liners**, older
  roll to `archive/`. **Shipped (✅ built) items leave the feature tables** → a one-line Done-log entry.
- **Scales.** Imp: ★ low · ★★ medium · ★★★ high. Size: XS · S · M · L · XL.
- **State tokens.** 📋 ready · 🏗️ building (worktree) · 📐 awaiting-Andrew (design ratification) ·
  ✅ ratified (design signed off, not yet built) · 🚧 blocked (Andrew-gated, or `seq:`/`blocked-on:` another
  item) · 🎯 top-priority pick · 🗄️ shelved-backup · 🔭 flag-for-Andrew.

## Active initiative — Loupe (first Edge Lattice prototype)

The view-&-control app built *around* Edge machinery (no authN/Z, Gateway, read-path auth, or Personal Lens —
trusted single-identity tool). Its three enabling items — **Loom control plane**, **Large-file/binary
handling**, **Refractor substrate migration** — all ✅ shipped (see Done log + the Progress board in
[../backlog.md](../backlog.md)). Loupe is now advanced like any Lattice component (owner for backend,
UX-then-FE for `cmd/loupe/web`).

## Component maintenance

Open items only (shipped ones are in the Done log). Grouped by component tag.

| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Health-KV] Orphaned dead-instance heartbeat keys never expire** | Each `health.<component>.<instanceID>` is written with no TTL, so a dead instance's key persists forever → permanent stale entries the Lamplighter must distinguish from live. | ★★ | S–M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/health-kv-ttl-orphan-expiry-design.md) · restores Contract #5 §5.6 TTL conformance (no contract change); 3 fires |
| **[Core] Processor per-lane consumers (ConsumerSupervisor adoption)** | Replace the single `processor-main` durable over all `ops.*` lanes (Phase-1 simplification) with per-lane consumers, per the architecture's design-of-record. | ★★ | M | 🏗️ building (per-lane fires shipped; see git) |
| **[Weaver] HealthSink pause-restore round-trip uncovered** | `consumerHealthSink.Load` paused-restore branch + `pauseReasonFromString` sit at 0% coverage. | ★★ | XS–S | 📐 awaiting-Andrew · [design](../../implementation-artifacts/health-sink-consolidation-design.md) · consolidate the identical Loom/Weaver/Bridge sink into shared `internal/healthkv`, test round-trip once; no contract change; 3 fires |
| **[Weaver] Registry cleanup edge branches uncovered** | `targetSource.removeOwnedTargetLocked` (targetId-rename removal, 33%), `removePatternLocked` + `removeOpMetaLocked` (pattern/op-meta vertex deletion index cleanup, 50%) — untested paths that keep the in-memory dispatch-resolution indices (`patternMeta`, `opMetaByType`) from leaking stale entries when a referenced `meta.loomPattern`/op meta-vertex is deleted or a target's `targetId` is renamed. | ★ | XS–S | 📋 · `internal/weaver/registry.go:372,586,640` |
| **[Loom] HealthSink pause-restore round-trip uncovered** | `consumerHealthSink.Load` paused branch (`internal/loom/health_sink.go:75-81`) + `pauseReasonFromString` switch arms partly uncovered (pkg 81.5%); restart-pause-restore unexercised end-to-end. Mirror of the Weaver gap above. | ★★ | XS–S | 📐 awaiting-Andrew · [design](../../implementation-artifacts/health-sink-consolidation-design.md) · same consolidation as the Weaver row (one shared tested sink) |
| **[Loom] Guardless-step recovery check-before-act probe** | On total `loom-state` loss + a re-triggered `StartLoomPattern`, a fresh instance replays guards from cursor 0 (re-runs an already-applied guarded step). | ★ | S–M | 🗄️ shelved-backup (Andrew: no new engine Core-KV reads) |
| **[Loupe] Static-UI serving (`go:embed web`) untested** | The embedded operator-UI mount has no coverage. | ★ | XS | 📋 |
| **[Loupe] Operator UI (`app.js`, 1142 LOC) has no automated coverage** | No JS test harness in the repo — standing up one is an architectural call. | ★★ | L | 📐 awaiting-Andrew · [design](../../implementation-artifacts/loupe-fe-test-strategy-design.md) · Go-native (goja logic tier, no Node); dep-fork = adopt goja; browser e2e deferred; 2 fires |
| **[Refractor] Retire the legacy `simple` engine (full-engine is universal)** | All 20 lenses are `engine:"full"`; the ~2.8k-LOC `simple` parser + its registry fallback are dead in prod but own the shared `EvalResult`/`QueryPlan` types → decouple-then-delete. | ★★ | M–L | 🏗️ building · [design](../../implementation-artifacts/retire-simple-engine-design.md) · Fire 1+2 shipped (carrier move, dead invalidation-forest deleted); next: Fire 3 delete the simple engine |
| **[Refractor/pipeline] Fan-out eval-error disposition + adjacency-watch edge branches uncovered** | `dispositionEvalErr` (0% — fan-out eval-error → terminal-DLQ/infra-pause/transient-nak) + `handleAdjUpdate` (13.5% — the not-found/tombstone/bad-key/unmarshal/guarded/write arms). Happy-path fan-out is e2e-covered; the error/edge arms are not. | ★★ | XS–S | 📐 awaiting-Andrew · [design](../../implementation-artifacts/refractor-pipeline-failure-disposition-coverage-design.md) · pins FR16–19a disposition contract; no contract change; 1 fire |
| **[Core] Atomic-batch size ceiling undocumented + unenforced** | A Starlark script's mutation set has no documented/enforced max size; a legitimate op that exceeds NATS's per-batch byte limit surfaces as a raw substrate/NATS error at step 8, not a typed Processor rejection — no bound, no clean failure mode. | ★★ | S | 📐 awaiting-Andrew · [design](../../implementation-artifacts/atomic-batch-size-ceiling-design.md) · §2.6+§3.9.1 edits staged uncommitted; 1000-msg/`max_payload` bound → typed `BatchTooLarge`; 1 fire |
| **[Core] UninstallPackage tombstones unconditionally (F-011 per-key OCC follow-up)** | `Installer.Uninstall`/`Upgrade` submit without per-key `expectedRevision` — a concurrent write to a declared key is silently overwritten. Fix: condition on the read-time `KVGet` revision (already read). | ★ | S–M | ✅ ratified · [design](../../implementation-artifacts/package-install-per-key-occ-design.md) · read-time revision (not install-time); §8.3/§8.6/§8.7 committed; 2 fires (uninstall, upgrade) |
| **[Loom] Redelivery/deadline-recovery edge branches uncovered** | `engine.go:resumeStepZero` (41.7% — redelivered trigger whose `createInstance` batch committed but step 0 never submitted, incl. the pattern-pin-missing→fail branch) + `state.go:disarmDeadline` (33.3% — KVGet/KVDelete error arms + the already-disarmed no-op that breaks the deadline-watcher re-entry loop) sit untested by any direct unit test. | ★ | XS–S | 📋 · `internal/loom/engine.go:460`, `internal/loom/state.go:451` |

### Survey log (round-robin rotation)

Rotation memory only — findings are the filed rows; fire narratives live in commits, never here.
Components: Core · Weaver · Loom · Refractor · Loupe (+ the cross-cutting feature backlog). Survey the
stalest (`git log -1 --format=%ct -- <path>`), note ONE dated line, rotate.

- 2026-07-01 Core (healthy; filed atomic-batch-size-ceiling + uninstall-per-key-OCC).
- 2026-07-01 Weaver (healthy, 83%/77% cov, no TODOs; filed registry-cleanup-edge-branches-uncovered).
- 2026-07-01 Designer — Refractor pipeline fan-out eval-error disposition + adj-watch edge arms (→ 📐).
- 2026-07-01 Loom (healthy, 81%/77% cov, clean lint, no TODOs; filed redelivery/deadline-recovery-edge-branches-uncovered).
- 2026-07-01 Designer — search/ES target adapter (3rd Refractor adapter; OpenSearch rec., FTS interim) (→ 📐).
- 2026-07-01 Designer — feature queue designed-out (all ~30 rows carry a design); resolved stale L309 (link-tombstone subsumed by link-aspect design, latency-rollup seq behind HA). Remaining 📋 = owner test-coverage.
- **Next:** Refractor, then Loupe.

## Lattice feature backlog — the Phase-3 build queue

The AI-driven flywheel draws from this list (Surveyor files → Designer designs → Steward builds the
ratified). Everything here needs design and is fair game **except** 🚧 Andrew-gated rows. Architectural
**forks** (Gateway, read-path auth, Vault, multi-cell, HA-NATS) and **frozen-contract** changes are
designed-through, but the *fork decision* + the *contract commit* are Andrew's.

> 🎯 **Build-ready now**: **op-time bounded link enumeration Fire 3** (optional e2e/lint) or the next
> ★★★-importance ratified item off dependency-gate (D1.5 / write-restriction F2).
> (**FR28 role-queue** Fire 1 + Fire 2 done — see Done log; Fire 3 unrouted surfacing next.
> **protected-lens out-of-band** ✅ SHIPPED — see Done log. **`@every` schedules** Fire 1 + Fire 2 shipped
> (`e04498e`); only the Andrew-gated Fire 3 §10.4 doc/contract remains.)
> *Dependency-sequenced ratified items*: **Vault** + **Personal Lens** behind D1; **Gateway** behind
> NATS-write-restriction F2; **Object crypto-shred** behind Vault — build when their gate clears.
> (**Control-plane-authz** rides D1.2, now shipped → buildable, deprioritized behind D1 rollout.)
> **Augur** Fires 1+2a+2b all shipped — the full escalate→review→dispatch loop closes; Fire 3 (autoApply) stays Andrew-gated.
> (**`kv.Links`** Fire 1 + Fire 2 (clinic consumer) shipped; only the optional Fire 3 e2e/lint remains.)

### Security & trust boundary
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| Read-path authorization (D1) | Reads from lens targets (Postgres/KV) bypass the write-path Capability boundary. Postgres RLS + a decomposed Capability-Read Lens; Gateway sets `lattice.actor_id`. Subsumes `cap.svc` read-auth. | ★★★ | L | 🏗️ building · [design](../../implementation-artifacts/read-path-authorization-d1-design.md) · D1.1–D1.5 shipped (every clinic-app/loftspace-app read model classified protected-or-public and closed); remaining D1 scope = the deferred Gateway/Personal-Lens fork (Andrew) |
| Gateway | Edge trust boundary: JWT auth, `Lattice-Actor` stamping, read-path enforcement. Gates external actors + the real Edge node. | ★★★ | L | ✅ ratified · [design](../../implementation-artifacts/gateway-external-trust-boundary-design.md) · 🚧 seq behind NATS-write-restriction F2b |
| NATS account-level write restriction | Close the fabricated-KV-write surface at the substrate (account-level); today defended only by overwrite-by-reprojection. | ★★ | M | 🏗️ building · [design](../../implementation-artifacts/nats-account-write-restriction-design.md) · F2b code+wiring done (worktree, gates green); live-stack turn-on needs Andrew present (shared-stack teardown, unattended-blocked) |
| Control-plane Capability authorization (FR30) | Both control planes (Weaver/Refractor `…/control`) should be capability-gated, not open responders. | ★★ | M | ✅ ratified · [design](../../implementation-artifacts/control-plane-capability-authz-design.md) · rides D1.2 (shipped) → buildable; deprioritized behind D1 rollout |

### Privacy / Vault
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| Vault + crypto-shredding | Per-identity keys for sensitive aspects (SSN/DOB); right-to-be-forgotten = destroy the key; transient-session-key decrypt. | ★★★ | L | ✅ ratified · [design](../../implementation-artifacts/vault-crypto-shredding-design.md) · 🚧 seq behind D1 |
| **[Object Store] Crypto-shred for object-store blobs** | Vault covers sensitive **aspects** (Core KV) but not PII-bearing **blobs** (lease PDFs, ID scans, signatures) — extend crypto-shred to the Object Store. | ★★ | M | ✅ ratified · [design](../../implementation-artifacts/object-store-crypto-shred-design.md) · 🚧 behind Vault |

### External-I/O maturity (bridge follow-ons)
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| Real adapters + async result-return | Replace the `Fake*` adapters with real vendors + design the async result path. | ★★ | M–L | ✅ async result-return done · real adapters deferred (prod) |
| Adapter read-seam / richer params | Adapters can only use what the target-lens row projects; add a subject-templated fetch seam for extra fields (SSN/DOB). | ★★ | S–M | 🏗️ building · [design](../../implementation-artifacts/adapter-read-seam-subject-templated-params-design.md) · F1 (sub-templated params) shipped |

### Scale-out
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| Multi-cell / sharding | Graph scales by **cells** (root + subgraph co-located for atomic writes); global adjacency index + bridge links for cross-cell. | ★ now / ★★★ at scale | XL | ✅ ratified · [design](../../implementation-artifacts/multi-cell-sharding-design.md) · 🚧 seq (prod-scale driver) |
| **Global identity for a hyperscale tenant** | A hyperscale tenant (WeWork) spans cells/regions — cross-cell shadows + cross-region residency on top of multi-cell. | ★ now / ★★★ at hyperscale | L–XL | 📐 awaiting-Andrew · [design](../../implementation-artifacts/global-identity-hyperscale-tenant-design.md) · 🚧 build behind multi-cell Fire 2 + a real hyperscale driver; NO contract change (one scoped multi-homed-`identity` exception flagged); 5 fires |
| **HA NATS clustering** | Single-server today; clustering + multi-instance engine fan-out. | ★ now / ★★ prod | M–L | ✅ ratified · [design](../../implementation-artifacts/ha-nats-clustering-design.md) · 🚧 shelved (prod-HA driver) |

### Edge & personal lenses (the path Loupe grows into)
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| Personal / Secure Lens | Refractor projects a per-identity security-filtered subgraph stream; the Interest-Set watchlist; RLS-style link filtering. | ★★ | L | ✅ ratified (design) · [design](../../implementation-artifacts/personal-secure-lens-design.md) · 🚧 build behind D1 |
| NATS-subject publish-events adapter | A Refractor target adapter publishing projection deltas to `lattice.sync.user.<id>` — required for Personal Lens. | ★★ | S–M | 📐 subsumed → Personal Lens Fire 1 |
| Edge Lattice (full) | The sovereign per-user node: local VAL (SQLite/IndexedDB), local Starlark, offline-first, reconcile-by-revision. | ★★ | XL | ✅ ratified · [design](../../implementation-artifacts/edge-lattice-full-design.md) · 🚧 seq (far) |

### AI-native
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| AI-authored capabilities | A Lattice-aware agent proposes DDL/Starlark/lenses/workflows through human review + deterministic validation + rollback. | ★★–★★★ | L | ✅ ratified · [design](../../implementation-artifacts/ai-authored-capabilities-design.md) |
| **The Augur** (AI reasoning tier — L3 evaluator) | Weaver's AI-assisted reasoning tier for ambiguous/novel convergence gaps. The marquee AI-native feature. | ★★ | M–L | ✅ Fires 1+2a+2b shipped (loop closes: escalate→review→dispatch) · [design](../../implementation-artifacts/augur-design.md) + [dispatch design](../../implementation-artifacts/augur-dispatch-pickup-design.md) · 🚧 Fire 3 autoApply Andrew-gated; follow-up: mid-flight-kill + drift-invalid e2e (§6 residual) |
| Starlark guards (Loom) | The reserved `{reads, starlark}` guard escape hatch needs a verified-pure sandbox. | ★ | M | ✅ ratified (split) · [design](../../implementation-artifacts/loom-starlark-guards-design.md) · 🚧 Loom-side held (ships with first consumer) |
| **Bespoke contracts / "Executable Paper" — Starlark-backed semantic clauses** | `vtx.clause` vertices (prose + Starlark predicate + formula) linked to the state they govern; Weaver audits satisfaction against a resident/patient ledger, auto-debiting computational clauses + opening a Task for judgment ones. Vault: `Contract as Executable paper/*`. | ★★★ | XL | 📐 awaiting-Andrew · [design](../../implementation-artifacts/bespoke-contracts-executable-paper-design.md) · rides existing convergence machinery (no new engine); scoping fork = pattern+package vs platform-engine (Andrew) |

### Read-model / projection maturity
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Refractor] Link-triggered reprojection (plain/GrantTable lenses)** | Eager relationship-grant freshness. **Downgraded ★, de-blocked — NOT a D1.3 blocker.** | ★ | M | ✅ ratified · [design](../../implementation-artifacts/link-aspect-triggered-reprojection-plain-lenses-design.md) · ⚠️ consolidate-decision vs Negative/filter-retraction (Andrew) |
| Negative / filter-retraction projection | True "emit-only-when-violating" (targets currently project one row per candidate with a `violating` flag). | ★→★★ | M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/negative-filter-retraction-projection-design.md) · consolidation target for Link-triggered reprojection |
| Elasticsearch target adapter | A third lens target adapter (only NATS-KV + Postgres ship; no consumer yet). | ★ | M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/search-target-adapter-design.md) · vendor fork (OpenSearch rec.) + FTS interim; no contract change; build behind a search consumer |
| **[Refractor] Cross-instance projection-latency rollup** | Aggregate per-lens projection latency across Refractor instances into one per-component view (single-instance today, so per-instance == per-component). Link-tombstone re-projection half **subsumed** by the link-aspect reprojection design. | ★ | S | 🚧 seq behind HA-NATS multi-instance · [link-aspect design](../../implementation-artifacts/link-aspect-triggered-reprojection-plain-lenses-design.md) subsumes the tombstone half; no multi-instance consumer yet |
| **[Refractor/Loupe] Silent lens-projection stall is undetectable** | A stalled projection is invisible: Clinic-PO saw committed ops stop reaching every clinic read model while Refractor self-reported `green`/`active`. Emit per-lens projection lag → Health KV; populate Loupe's `freshness` column (today always `-`). | ★★ | M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/lens-projection-liveness-design.md) · per-lens `lastProjectedAt` + lag issues; no contract change; 3 fires |

### Refinements & ops
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **CI pipeline speed (continuous)** | Make CI faster without weakening any gate — owned continuously by the **Whetstone**. Matrix split done (serial → 4 parallel jobs); convergence + unit parallelized. | ★★ | M (ongoing) | 🏗️ continuous (Whetstone) · `internal/loom` de-flaked+sped (9129005, 55s→41s); next: `internal/bridge` (44s) now the `unit` job's long pole |
| **[CI/Refractor] Hello-Lattice NFR-P3 latency flake** | The `≤500ms` capability-projection probe fails-then-passes on the shared CI runner (~590ms infra floor) — the dominant re-run flake (~50%). | ★★ | M | ✅ resolved — NFR-P3 CI projection deadlines re-scoped to a 1000ms regression guard; reported SLA unchanged (Andrew-ratified) |
| **Op-time bounded reverse-link / adjacency read (`kv.Links`)** | One sanctioned, bounded, fail-closed, paged op-time link-enumeration builtin (`kv.Links(hub, relation, direction, cursor, limit)`) — retires the key-list-in-aspect guard indexes. Relaxes the write-path no-scans invariant by exactly one primitive. | ★★★ | M–L | 🏗️ building · [design](../../implementation-artifacts/op-time-bounded-link-enumeration-design.md) · Fire 1 (cc2613f) + Fire 2 (clinic consumer) shipped; next = optional Fire 3 (e2e + hub-source lint) |
| **Hard-delete mutation verb (true link/aspect keyspace reclaim)** | Mutation vocab is create/update/tombstone (soft PUTs); a tombstoned key persists + is still enumerated by `kv.Links`. A 4th `delete` verb (NATS `DEL`) lets dead links leave the keyspace, bounding `kv.Links` LIST cost. | ★ now / ★★ at scale | M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/hard-delete-mutation-verb-design.md) · §3 edit staged uncommitted; `DEL`-not-`PURGE`; 2 fires (clinic reclaim = consumer) |
| **Script-read posture — declared+hydrated vs live `kv.get`/`kv.Links`** | Declared+hydrated reads as the write-path norm: `optionalReads` folds read-before-create in; `kv.Links` declared-as-metadata (Edge-gate + best-effort lint, not hydrated); guards become a generic Processor-side operation feature (supersedes Loom's engine read). | ★★ | L | ✅ ratified · [design](../../implementation-artifacts/script-read-posture-design.md) · Fires 1–2 shippable (Contract #2 committed); guard (Fire 3) build + contracts deferred |
| **FR28 — role-queue + fallback** (+ FR29 unrouted surfacing) | A `queuedFor.role` link + `ClaimTask` op + `CreateTask` routing (named → role-queue → loud `RoutingFailed`); grant/inbox fan out to role-holders; an empty queue is surfaced post-hoc by a new `unroutedTasks` Weaver target. | ★ | M | 🏗️ building · [design](../../implementation-artifacts/fr28-role-queue-fallback-design.md) (`9495081`,`12fc79b`) · next: Fire 3 unrouted surfacing |
| **`@every` recurring schedules** (op-vertex pruner #49 retired) | A `substrate.ScheduleEvery`/`CancelSchedule` seam + migrate the Weaver reconciler sweep (`time.Ticker` → durable `@every`). Op-vertex pruner retired (NATS per-key TTL + outbox tombstone cover it). | ★ | M | 🏗️ building · [design](../../implementation-artifacts/recurring-schedules-and-op-vertex-pruner-design.md) · Fire 1 (primitive) + Fire 2 (Weaver sweep cron-kill, `e04498e`) shipped; only Fire 3 (§10.4 doc/contract + #49 retirement, Andrew-gated commit) remains |
| **Package version upgrade / DDL hot-reload (F-004)** | In-place re-install over an existing version + DDL-migration semantics (install/uninstall existed; upgrade did not). Diff-and-apply (create/update/tombstone) in one atomic Processor batch; version-independent entity keys. | ★★ | M | ✅ effectively done · [design](../../implementation-artifacts/package-version-upgrade-design.md) · Fires 1a–3 shipped; only an optional Fire-2 live e2e remains (§8.1 + §8.6 committed) |
| **[Verticals] loftspace-app / clinic-app have no Health-KV self-report** | Neither app writes health status at all — an admin-actor load failure (hit live 2026-07-01: on-disk `lattice.bootstrap.json` `version:"13"` vs `checkVersion`'s required `"14"`, committed `40f4d25`) or a NATS outage is invisible to Loupe; only surfaces when a user's `/api/op` write 400s. | ★★ | S | 📐 awaiting-Andrew · [design](../../implementation-artifacts/vertical-app-health-self-report-design.md) · shared `internal/healthkv` dependency-probing reporter; no contract change; 2 fires (+opt objmgr) |
| Loom / Weaver control-API surfacing | Operator pause/resume + a durable `loom.*` read model beyond what the Loupe blocker covers. | ★ | M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/orchestration-history-read-model-design.md) · pause/resume shipped; durable history via new `eventStream` lens-source (FORK A, §10.9-blessed, no contract change); 3 fires |

### Parking lot — very low priority (far, far back)

Real but low-value; do **not** spend design or build effort here unless Andrew greenlights one.

| Item | Why it's parked | Imp | Size | State |
|---|---|---|---|---|
| **Historical state query (FR51)** | Operators query historical state across a time range (audit/ledger + point-in-time reconstruction). Low near-term value + standing storage cost; builds to reserved contract seams. | ★ now / ★★ if real need | M→L | ✅ ratified (design) · [design](../../implementation-artifacts/historical-state-query-design.md) · build deferred (Andrew, revive on a concrete need) |
| multi-aspect atomic OCC for `UpdateMetaVertex` | `meta_ddl.go` applies `expectedRevision` to the first changed aspect by design; true multi-key OCC needs a substrate per-key-revision primitive — marginal value. | ★ | M+ | 🗄️ parked |
| freshnessExpiry marker tombstone-on-convergence | A converged marker is read by nothing and harmless; tombstoning buys cleanup not correctness. | ★ | S | 🗄️ parked |
| production freshness-window tuning | A staleness-tolerance vs. timer-churn value judgment — Andrew's call if/when it matters. | ★ | XS | 🗄️ parked |
| Loupe agent-activity console | The ops layer atop the live system map (Steward queue, L3 review queue, per-agent Health). Read-seam options rejected. | ★★★ | M | 🚧 Andrew-gated (shelved 2026-06-25; design retained, do not build) |

## Done log — lattice (newest first)

One line per shipped item (`date · SHA · [tag] title`). Oldest roll to `archive/` past ~25.

- 2026-07-01 · `970585f` · [Refractor] Retire-simple-engine Fire 1 — lift `EvalResult`/`NodeEntry` into `ruleengine` (pure relocation, simple keeps a type alias)
- 2026-07-01 · `4920bc6` · [Augur] Fire 2b — `augurDispatch` closes the loop (approve→dispatch); 3-layer review folded (reconciler backoff pacing + dispatch-time anchor-field pinning)
- 2026-07-01 · `da8279f` · [loftspace-app] D1.5 — `handleUnitApplications` landlord operator-console unauth read fix (RLS-scoped to `queryLandlordApplications`'s managed-unit set; D1.5 read-model rollout now complete)
- 2026-07-01 · `6c98748` · [loftspace-app] D1.5 — `handleIdentities` system-wide unauth roster dump fix (new `applicantRosterRead` wildcard-only protected lens; `handleStaffIdentities` replaces it)
- 2026-07-01 · `40240dd` · [clinic-app] D1.5 — `handlePatients` clinic-wide unauth roster dump fix (new `clinicPatientsRead` wildcard-only protected lens; `handleStaffPatients` replaces it)
- 2026-07-01 · `9129005` · [CI] Whetstone — `internal/loom` e2e sleeps → deterministic readiness polls (5 files, ~20 sites; package 55s→41s in CI, 3 restart tests de-flake-hardened via `joinEngine`)
- 2026-07-01 · `b1c2eeb` · [clinic-app] D1.5 — `handleAppointments` provider-availability PHI over-exposure fix (minimal availabilityRow strips patient/visit fields from the unauthenticated slot-picker endpoint)
- 2026-07-01 · `f509b84` · [loftspace-app/clinic-app] D1.5 — loftspace tasks (JWT-scoped) + clinic visit-series (new `visitSeriesRead` protected lens) read boundaries
- 2026-07-01 · `9191eed` · [loftspace-app] D1.5 — objects/documents read boundary (unit photos stay public; identity/leaseapp document bytes now authenticateRead+entitled-scoped; closed the unauthenticated document/PII-byte dump)
- 2026-07-01 · `40f4d25` · [Core/clinic-app] D1.5 — staff wildcard read grant (WildcardAnchor RLS clause + capabilityReadWildcardGrants kernel lens; closed the unauthenticated clinic-wide appointments dump)
- 2026-07-01 · `17ccd42` · [clinic-app/clinic-domain] D1.5 Increment 2 — provider-self protected schedule read model (RLS-closed the unauthenticated `?provider=` full-schedule leak; staff wildcard audiences flagged follow-up)
- 2026-07-01 · `c46fbe2` · [clinic-app/clinic-domain] D1.5 — patient-self protected read model (RLS-closed the unauthenticated `?patient=` appointment-history leak; provider/staff audiences flagged follow-up)
- 2026-07-01 · `ac43891` · [CI/hellolattice] NFR-P3 flake resolved — CI projection deadlines re-scoped to a 1000ms regression guard (runner-floor headroom); reported SLA unchanged (Andrew-ratified)
- 2026-07-01 · `10bd188` · [loftspace-app/lease-signing] D1.5 — RLS-protect the lease-document GET (closed an unauthenticated PII read of weaver-targets)
- 2026-07-01 · `12fc79b` · [Core/orchestration-base] FR28 Fire 2 — availability-gated routing (`SetAvailability` op + `availability` aspect; `CreateTask` falls back to queue when the assignee is unavailable)
- 2026-07-01 · `4712c46` · [Core/rbac-domain+identity-hygiene] Contract #10 §10.1 no-orphan tombstone guard — `TombstoneRole`/`MergeIdentity` reject a live queuedFor/assignedTo open task (found in FR28 Fire 1 adversarial review)
- 2026-06-30 · `9495081` · [Core/orchestration-base] FR28 Fire 1 — role-queue + claim (`queuedFor` link, `CreateTask` assignee-or-queue routing, `ClaimTask`, capabilityEphemeral/myTasks role fan-out)
- 2026-07-01 · `ef108b4` · [Refractor] Protected-lens out-of-band provisioning + verify-and-pause — Fire 0+1+2 (fail-closed activation gate, `Verify{Protected,Grant}Table`, `emit-ddl`/`provision-readpath`, seq-guard)
- 2026-06-30 · `e04498e` · [Weaver] `@every` Fire 2 — reconciler sweep cron-kill (durable `@every` replaces the in-process ticker)
- 2026-06-30 · `44b385a` · [Core/substrate] `@every` Fire 1 — `ScheduleEvery`/`CancelSchedule` recurring-schedule primitive
- 2026-06-29 · `d6530e9` · [Core/processor+rbac] Lane authorization enforcement (§2.3) — step-3 lane gate + `LaneUnauthorized` + Gate-3 vector #8
- 2026-06-30 · `0cd2695` · [lint/Core] instanceOf P7 lint gate (whole instanceOf design done)
- 2026-06-27 · `679fe25` · [Refractor] Full-engine plain-projection anchor-tombstone retraction (`AnchorDeleteResult`)
- 2026-06-30 · `44049ed` · [Core/bypass] D1.4 — Gate-3 read-path authorization adversarial vectors (§5.1–5.5: no-JWT · cross-actor · revoked · cross-anchor bleed · no-RLS-policy); Gate 3 now 13/13, gate sets `POSTGRES_TEST_DSN`
- 2026-06-30 · `<pending>` · [clinic-domain] kv.Links Fire 2 — re-author the appointment double-book guard onto `hasBooking` links + scalar `bookingGuard` epoch (drop the `.bookings` key-list aspects + DDLs); pkg 0.8.0
- 2026-06-30 · `cc2613f` · [Core/substrate] kv.Links Fire 1 — bounded op-time link enumeration primitive (+ `KVListKeysFilter` paged subject-filter seam)
- 2026-06-30 · `3dbd049` · [Augur] Fire 2a — `ReviewProposal` human-verdict op
- 2026-06-30 · [CI] Flake-hunt: mined the re-run history (attempt-aware) — found the Hello-Lattice NFR-P3 flake
- 2026-06-29 · `faa3aec` · [Refractor] GrantTable composite-keyed anchor-tombstone retraction
- 2026-06-29 · `89a9842` · [CI] Halve the leaseshortwindow freshness window (40s→25s) — convergence −33s
- 2026-06-29 · `65f4f4d` · [Loom/orchestration-base] Adapter-read-seam Fire 1 — subject-templated params
- 2026-06-29 · `f04f331` · [Core/bootstrap] D1.3 Increment 1 — base `capabilityRead` self-anchor lens
- 2026-06-29 · `c1a8901` · [Core/pkgmgr + Refractor] Package-declared protected/grant Postgres lenses
- 2026-06-29 · `d772195` · [Refractor] Full-engine multi-column projection key (GrantTable producer)
- *(older entries rolled to [archive/lattice-done.md](archive/lattice-done.md))*
