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
| **[Weaver] HealthSink pause-restore round-trip uncovered** | `consumerHealthSink.Load` paused-restore branch + `pauseReasonFromString` sit at 0% coverage. | ★★ | XS–S | 📋 |
| **[Loom] HealthSink pause-restore round-trip uncovered** | `consumerHealthSink.Load` paused branch (`internal/loom/health_sink.go:75-81`) + `pauseReasonFromString` switch arms partly uncovered (pkg 81.5%); restart-pause-restore unexercised end-to-end. Mirror of the Weaver gap above. | ★★ | XS–S | 📋 |
| **[Loom] Guardless-step recovery check-before-act probe** | On total `loom-state` loss + a re-triggered `StartLoomPattern`, a fresh instance replays guards from cursor 0 (re-runs an already-applied guarded step). | ★ | S–M | 🗄️ shelved-backup (Andrew: no new engine Core-KV reads) |
| **[Loupe] Static-UI serving (`go:embed web`) untested** | The embedded operator-UI mount has no coverage. | ★ | XS | 📋 |
| **[Loupe] Operator UI (`app.js`, 1142 LOC) has no automated coverage** | No JS test harness in the repo — standing up one is an architectural call. | ★★ | L | 🔭 flag-for-Andrew |
| **[Refractor] Retire the legacy `simple` engine (full-engine is universal)** | All 20 lenses are `engine:"full"`; the ~2.8k-LOC `simple` parser + its registry fallback are dead in prod but own the shared `EvalResult`/`QueryPlan` types → decouple-then-delete. | ★★ | M–L | 📋 needs design (type-decouple; no contract change) · `ruleengine/simple/` |
| **[Refractor/pipeline] Fan-out eval-error disposition + adjacency-watch edge branches uncovered** | `dispositionEvalErr` (0% — link/aspect fan-out eval-error → terminal-DLQ / infra-pause / transient-nak mapping) and `handleAdjUpdate` (13.5% — adjacency-watch reprojection: the not-found / tombstone / bad-key / unmarshal arms). Happy-path fan-out is e2e-covered; the error/edge arms are not. Mirror of the HealthSink coverage rows. | ★★ | XS–S | 📋 · refs `pipeline/pipeline.go:625,921`, `pipeline/evaluate.go` |

### Survey log (round-robin rotation)

Rotation memory only — findings are the filed rows; fire narratives live in commits, never here.
Components: Core · Weaver · Loom · Refractor · Loupe (+ the cross-cutting feature backlog). Survey the
stalest (`git log -1 --format=%ct -- <path>`), note ONE dated line, rotate.

- **Last surveyed:** 2026-06-30 Refractor (healthy; filed simple-engine-retire + fan-out-coverage) ·
  feature-backlog (healthy, ~25 scored items).
- **Next:** Core (`internal/processor` + `bootstrap` + `substrate`), then Weaver.

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
> **Augur** Fire 1 + 2a merged; Fire 2b (dispatch loop-closer) ✅ ratified → build-ready.
> (**`kv.Links`** Fire 1 + Fire 2 (clinic consumer) shipped; only the optional Fire 3 e2e/lint remains.)

### Security & trust boundary
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| Read-path authorization (D1) | Reads from lens targets (Postgres/KV) bypass the write-path Capability boundary. Postgres RLS + a decomposed Capability-Read Lens; Gateway sets `lattice.actor_id`. Subsumes `cap.svc` read-auth. | ★★★ | L | 🏗️ building · [design](../../implementation-artifacts/read-path-authorization-d1-design.md) · D1.1–D1.4 shipped; D1.5 rolling — lease-document GET done (10bd188); next: clinic/loftspace remaining read models |
| Gateway | Edge trust boundary: JWT auth, `Lattice-Actor` stamping, read-path enforcement. Gates external actors + the real Edge node. | ★★★ | L | ✅ ratified · [design](../../implementation-artifacts/gateway-external-trust-boundary-design.md) · 🚧 seq behind NATS-write-restriction F2b |
| NATS account-level write restriction | Close the fabricated-KV-write surface at the substrate (account-level); today defended only by overwrite-by-reprojection. | ★★ | M | 🏗️ building · [design](../../implementation-artifacts/nats-account-write-restriction-design.md) · F1 (credential seam) shipped; F2 = live enforcement |
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
| **The Augur** (AI reasoning tier — L3 evaluator) | Weaver's AI-assisted reasoning tier for ambiguous/novel convergence gaps. The marquee AI-native feature. | ★★ | M–L | 🏗️ building · [design](../../implementation-artifacts/augur-design.md) · Fire 1 (adaf7be) + 2a (3dbd049) shipped; Fire 2b ✅ ratified · [dispatch design](../../implementation-artifacts/augur-dispatch-pickup-design.md) — build-ready (role-queue-only assignTask) |
| Starlark guards (Loom) | The reserved `{reads, starlark}` guard escape hatch needs a verified-pure sandbox. | ★ | M | ✅ ratified (split) · [design](../../implementation-artifacts/loom-starlark-guards-design.md) · 🚧 Loom-side held (ships with first consumer) |

### Read-model / projection maturity
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Refractor] Link-triggered reprojection (plain/GrantTable lenses)** | Eager relationship-grant freshness. **Downgraded ★, de-blocked — NOT a D1.3 blocker.** | ★ | M | ✅ ratified · [design](../../implementation-artifacts/link-aspect-triggered-reprojection-plain-lenses-design.md) · ⚠️ consolidate-decision vs Negative/filter-retraction (Andrew) |
| Negative / filter-retraction projection | True "emit-only-when-violating" (targets currently project one row per candidate with a `violating` flag). | ★→★★ | M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/negative-filter-retraction-projection-design.md) · consolidation target for Link-triggered reprojection |
| Elasticsearch target adapter | A third lens target adapter (only NATS-KV + Postgres ship; no consumer yet). | ★ | M | 📋 (no consumer yet) |
| Link-tombstone re-projection · cross-instance latency rollup | Two projection edge-cases / observability gaps (current approaches work). | ★ | S each | 📋 |
| **[Refractor/Loupe] Silent lens-projection stall is undetectable** | A stalled projection is invisible: Clinic-PO saw committed ops stop reaching every clinic read model while Refractor self-reported `green`/`active`. Emit per-lens projection lag → Health KV; populate Loupe's `freshness` column (today always `-`). | ★★ | M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/lens-projection-liveness-design.md) · per-lens `lastProjectedAt` + lag issues; no contract change; 3 fires |

### Refinements & ops
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **CI pipeline speed (continuous)** | Make CI faster without weakening any gate — owned continuously by the **Whetstone**. Matrix split done (serial → 4 parallel jobs); convergence + unit parallelized. | ★★ | M (ongoing) | 🏗️ continuous (Whetstone) · next: `loom`/`bridge` `t.Parallel()` |
| **[CI/Refractor] Hello-Lattice NFR-P3 latency flake** | The `≤500ms` capability-projection probe fails-then-passes on the shared CI runner (~590ms infra floor) — the dominant re-run flake (~50%). | ★★ | M | ✅ resolved — NFR-P3 CI projection deadlines re-scoped to a 1000ms regression guard; reported SLA unchanged (Andrew-ratified) |
| **Op-time bounded reverse-link / adjacency read (`kv.Links`)** | One sanctioned, bounded, fail-closed, paged op-time link-enumeration builtin (`kv.Links(hub, relation, direction, cursor, limit)`) — retires the key-list-in-aspect guard indexes. Relaxes the write-path no-scans invariant by exactly one primitive. | ★★★ | M–L | 🏗️ building · [design](../../implementation-artifacts/op-time-bounded-link-enumeration-design.md) · Fire 1 (cc2613f) + Fire 2 (clinic consumer) shipped; next = optional Fire 3 (e2e + hub-source lint) |
| **Hard-delete mutation verb (true link/aspect keyspace reclaim)** | Mutation vocab is create/update/tombstone (soft PUTs); a tombstoned key persists + is still enumerated by `kv.Links`. A 4th `delete` verb (NATS `DEL`) lets dead links leave the keyspace, bounding `kv.Links` LIST cost. | ★ now / ★★ at scale | M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/hard-delete-mutation-verb-design.md) · §3 edit staged uncommitted; `DEL`-not-`PURGE`; 2 fires (clinic reclaim = consumer) |
| **Script-read posture — declared+hydrated vs live `kv.get`/`kv.Links`** | Live Core-KV reads in scripts are the common root of the Loom-guard Processor-side redirect *and* the Edge A′-prediction partiality; declared+hydrated reads the norm, live reads classified (debt vs sanctioned config vs irreducible `kv.Links`), Loom guard read retired Processor-side. | ★★ | L | 📐 awaiting-Andrew · [design](../../implementation-artifacts/script-read-posture-design.md) · §2.5 `optionalReads` edit staged uncommitted; Fire 3 retires Loom `evalGuard` (G1 rec.) |
| **FR28 — role-queue + fallback** (+ FR29 unrouted surfacing) | A `queuedFor.role` link + `ClaimTask` op + `CreateTask` routing (named → role-queue → loud `RoutingFailed`); grant/inbox fan out to role-holders; an empty queue is surfaced post-hoc by a new `unroutedTasks` Weaver target. | ★ | M | 🏗️ building · [design](../../implementation-artifacts/fr28-role-queue-fallback-design.md) (`9495081`,`12fc79b`) · next: Fire 3 unrouted surfacing |
| **`@every` recurring schedules** (op-vertex pruner #49 retired) | A `substrate.ScheduleEvery`/`CancelSchedule` seam + migrate the Weaver reconciler sweep (`time.Ticker` → durable `@every`). Op-vertex pruner retired (NATS per-key TTL + outbox tombstone cover it). | ★ | M | 🏗️ building · [design](../../implementation-artifacts/recurring-schedules-and-op-vertex-pruner-design.md) · Fire 1 (primitive) + Fire 2 (Weaver sweep cron-kill, `e04498e`) shipped; only Fire 3 (§10.4 doc/contract + #49 retirement, Andrew-gated commit) remains |
| **Package version upgrade / DDL hot-reload (F-004)** | In-place re-install over an existing version + DDL-migration semantics (install/uninstall existed; upgrade did not). Diff-and-apply (create/update/tombstone) in one atomic Processor batch; version-independent entity keys. | ★★ | M | ✅ effectively done · [design](../../implementation-artifacts/package-version-upgrade-design.md) · Fires 1a–3 shipped; only an optional Fire-2 live e2e remains (§8.1 + §8.6 committed) |
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
- 2026-06-29 · `d85450d` · [Refractor/Core] `nanoIdFromKey` auth-plane cypher fn (D1 prereq)
- 2026-06-29 · `97afcd2` · [Core] Processor commit OCC §3.2 update-conditioning + bounded retry + Health signal
- 2026-06-29 · `6eaabcc` · [Core] instanceOf Fire E — expose the op's own type-DDL meta key to Starlark
- 2026-06-29 · `cea0c3b` · [Core] instanceOf Fire 1 — step-6 governing-DDL chain resolver
- 2026-06-29 · `1443109` · [CI] Grounding fire: re-measured the pipeline
- 2026-06-28 · `ce2086f` · [CI] Parallelize the lease-convergence e2e gate (t.Parallel)
- 2026-06-28 · `07f3824` · [CI] Parallelize the weaver test package (t.Parallel)
- 2026-06-27 · `1443109` · [CI] Serial pipeline → 4-job parallel matrix
- 2026-06-28 · `7f98d83` · [Core/pkgmgr] F-004 Fire 3 — dev-loop refresh targets + upgrade docs
- *(older entries rolled to [archive/lattice-done.md](archive/lattice-done.md))*
