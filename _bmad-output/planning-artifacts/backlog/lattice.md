# Backlog — Lattice (Stream 2): features + component maintenance

Stream 2 = platform features + component maintenance. Pipeline: **Surveyor** files scored demand →
**Designer** turns items into design docs flagged for Andrew → **Lattice Steward** builds the ratified ones;
the **Whetstone** keeps CI fast cross-cutting. Written by the Lattice Steward + Surveyor (+ Whetstone CI rows,
+ PO-routed platform gaps) only. Index + cross-lane rules: [../backlog.md](../backlog.md).

## How this board works (read before editing — the row discipline)

**The board is an INDEX, not a journal.** One item = one row. The detail lives where the work lives.

- **A row is:** `Item · What it is (one line) · Imp · Size · State`. The **State** cell is a **state token**
  + a **link to the design doc / commit** + (only if 🏗️) a **one-line next step**. Nothing else.
- **Detail belongs in the linked design doc + git** — the design shape, the ratification record, adversarial
  findings, the fire-by-fire build journal, commit SHAs, coverage %, review-depth notes. **Never narrate that
  history in the row** (the CLAUDE.md no-changelog-comments rule, applied to the board). A multi-fire item's
  checkpoint (worktree path · what's done · next) lives in its design doc; the row carries a one-line pointer.
- **Shipped (✅ built) items leave the feature tables** and become a one-line **Done-log** entry
  (`date · SHA · [tag] title`). When the Done log exceeds ~25 lines, the oldest roll to `archive/`.
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

### Survey log (round-robin rotation)

Compact rotation memory only (survey *findings* become filed rows above + in the feature backlog).
Components: Core · Weaver · Loom · Refractor · Loupe (+ the cross-cutting feature backlog). Freshness via
`git log -1 --format=%ct -- <path>`; survey the stalest, note a dated line, rotate.

- **Steward fire 2026-06-30 (@every Fire 1 + board reconciliation):** built `@every` recurring-schedules
  **Fire 1** — the `substrate.ScheduleEvery`/`CancelSchedule` primitive + the 3 fired-copy header constants
  (pinned to the nats-server v2.14 `JSScheduler`/`JSScheduleNext`/`JSScheduleTTL`) + `DeriveScheduleOccurrenceRequestID`,
  all grounded in the pinned NATS source (rollup-replace, `@every` `time.ParseDuration`, >=1s floor all verified)
  and proven against embedded NATS 2.14. Picked over the ★★ Augur Fire 2b (the only genuinely-unbuilt ★★) because
  Fire 2b is a coupled autonomous-dispatch-plane loop-closer with **no green sub-increment** and an **under-resolved
  §3.4 dispatch-pickup mechanism** (flagged for a Designer pass — see the Augur row) — unfit to land green unattended;
  @every is a clean ratified primitive (kv.Links-Fire-1 precedent). **Reconciled TWO stale board rows** the SENSE pass
  caught: lane-authorization enforcement (Fire 2 shipped `d6530e9`, row said "next = enforcement") and the Weaver
  reclaim check-before-act probe (Option D backoff shipped `04c7689`, row said "📋 ready") — both moved to the Done log.
- **Steward fire 2026-06-30 (protected-lens OOB Fire 0+1 — built+3-layer-reviewed, checkpointed):** picked the
  highest-importance lattice-clean build-ready item (★★ protected-lens out-of-band, over the ★ @every-Fire-2 /
  FR28 continuations; the ★★★ D1.5's remaining increments are verticals-lane read-model migrations, not a
  lattice-clean increment). Built **Fire 0** (`substrate.ConsumerSpec.InitialPause` fail-closed activation seam +
  the runPump seed + 2 substrate tests) **+ Fire 1** (`VerifyProtectedTable`/`VerifyGrantTable` read-only catalog
  checks wired as the adapter `Probe`; `cmd/refractor` retires runtime DDL; protected/grant lenses register
  `InitialPause:PauseInfra`). Kept `Provision*`/`Build*DDL` functional as the out-of-band/test/CLI seam (only the
  runtime call sites removed) → zero verticals-lane churn (their RLS tests use `Build*DDL` directly). All gates
  green; Postgres-gated verify tests pass. **Full 3-layer review run** (security plane): Blind Hunter caught a
  **CRITICAL fail-open** — `relforcerowsecurity` alone is insufficient (FORCE-without-ENABLE = world-readable,
  verified empirically); now gates `relrowsecurity` AND `relforcerowsecurity` + `relkind='r'` + exact `text[]` +
  policy **posture** (named §6.14 membership policy, not mere presence); Acceptance Auditor ACCEPT; Edge Case
  Hunter BLOCK (loftspace-app consumes `read_lease_applications` → Fire 1 alone darks the live vertical → must
  co-ship the generic `make provision-readpath`). **Checkpointed** (not committed to main — committing alone
  regresses `up-loftspace`): worktree branch `steward-protected-lens-oob` head `59d2f98`. NEXT FIRE: build the
  generic provisioner (reuse `lens.CoreKVSource` + `Build*DDL`) + refractor.md + soft-delete guard, live-verify
  up-loftspace, ff-merge Fire 0+1+2. CI unaffected (no go-test/CI target activates a protected pg lens).
- **Steward fire 2026-06-30 (P7 lint gate):** shipped the instanceOf design's last DoD item — the
  `lint-conventions` P7 gate (discriminator-aspect shadowing the envelope class). Picked importance-first
  (★★ ratified-design residual) over the ★ build-ready FR28/@every; the other ★★ build-ready item
  (protected-lens out-of-band) is the larger L security-plane fire a prior fire deferred for a dedicated
  3-layer budget. Whole instanceOf item → Done log. Lead review (XS, build-ignored static lint, zero
  production-code impact, synthetic-probe-verified). `0cd2695`.
- **Steward fire 2026-06-30 (Refractor read-path):** reconciled a stale board row (full-engine
  anchor-tombstone retraction said 📋 ready but Fire 1 shipped `679fe25` — moved to Done log) and
  grounding-corrected the protected-lens out-of-band design before building: its §2.3 "no new pump
  logic / start infra-paused" premise is **ungrounded** (`ConsumerSpec` has no initial-pause field;
  a fresh protected lens drains+projects before posture verify → fail-OPEN). Folded the correction
  (new `ConsumerSpec.InitialPause` substrate seam = Fire 0; Fire 0+1 land together under full 3-layer)
  + flagged the board row. No code committed (security-plane build deferred to a dedicated fire with
  budget for the 3-layer review + the D1.4 Gate-3 fixture relocation).
- **Surveyor fire 2026-06-30 (feature backlog):** healthy + well-hydrated — ~25 scored items across 8
  sections, build-ready picks (FR28 role-queue, protected-lens OOB) already flagged. Grounded the one open
  PO-routed platform gap (Verticals "clinic provider/patient/appointment LINGER" → "full-engine tombstone
  retraction"): **already served**, no new lattice gap. `AnchorDeleteResult` (`679fe25`) retracts plain
  full-engine anchor root-tombstones — clinic list lenses anchor on the entity root, `Tombstone*` soft-deletes
  the root → `isDeleted` CDC → `evaluate.go:105` emits the Delete; a secondary-node tombstone falls through to
  re-execute (`evaluate.go:101-104`) so dependent rows refresh. The Verticals 🚧 `blocked-on-lattice` label is
  **stale** (verify + close verticals-side). Filed nothing (no filler).
- **Last surveyed:** 2026-06-30 Loom (`internal/loom` + control). Healthy — 81.5% / 76.6% cov, no 0%
  funcs, no TODO/FIXME; both deferred items (Starlark guards, durable `loom.*` read model) already filed.
  Filed one maintenance gap: HealthSink pause-restore coverage (mirrors the Weaver row).
  Prior rotation: Core → Weaver → Loupe → Loom → feature-backlog.
- **Next:** Refractor (`internal/refractor`), then Core.

## Lattice feature backlog — the Phase-3 build queue

The AI-driven flywheel draws from this list (Surveyor files → Designer designs → Steward builds the
ratified). Everything here needs design and is fair game **except** 🚧 Andrew-gated rows. Architectural
**forks** (Gateway, read-path auth, Vault, multi-cell, HA-NATS) and **frozen-contract** changes are
designed-through, but the *fork decision* + the *contract commit* are Andrew's.

> 🎯 **Build-ready now** (✅ ratified / 📋 ready, no upstream gate): **FR28 role-queue**.
> (**protected-lens out-of-band** now 🏗️ — Fire 0+1 built+3-layer-reviewed on a worktree; next = co-ship the
> `make provision-readpath` dev-provisioner + ff-merge. **`@every` schedules** 🏗️ — Fire 1 primitive shipped.)
> *Dependency-sequenced ratified items*: **Vault** + **Personal Lens** behind D1; **Gateway** behind
> NATS-write-restriction F2; **Object crypto-shred** behind Vault — build when their gate clears.
> (**Control-plane-authz** rides D1.2, now shipped → buildable, deprioritized behind D1 rollout.)
> **Augur** Fire 1 + 2a merged; Fire 2b+ is the next AI-native increment (§8).
> (**`kv.Links`** Fire 1 + Fire 2 (clinic consumer) shipped; only the optional Fire 3 e2e/lint remains.)

### Security & trust boundary
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| Read-path authorization (D1) | Reads from lens targets (Postgres/KV) bypass the write-path Capability boundary. Postgres RLS + a decomposed Capability-Read Lens; Gateway sets `lattice.actor_id`. Subsumes `cap.svc` read-auth. | ★★★ | L | 🏗️ building · [design](../../implementation-artifacts/read-path-authorization-d1-design.md) · D1.1–D1.4 shipped (base lens · JWT seam · protected-Postgres RLS · §5 Gate-3 read-bypass vectors + lint); next = D1.5 roll remaining read models onto the enforcement seam |
| **Protected-lens provisioning: out-of-band + verify-and-pause** | Refractor runs the protected/grant Postgres table DDL today; move provisioning out-of-band + verify-and-pause fail-closed (retire the RLS DDL-ownership exception). | ★★ | M→L | 🏗️ building · [design](../../implementation-artifacts/protected-lens-out-of-band-provisioning-verify-and-pause-design.md) · Fire 0+1 built+3-layer-reviewed+hardened on worktree `steward-protected-lens-oob` (head `59d2f98`), all gates green; review caught a CRITICAL fail-open (FORCE-without-ENABLE = world-readable → now gates relrowsecurity AND relforcerowsecurity + policy posture). **next = co-ship the generic `make provision-readpath` (loftspace-app consumes `read_lease_applications`, so Fire 1 alone darks the vertical) + refractor.md + soft-delete guard, live-verify up-loftspace, ff-merge Fire 0+1+2 to main** (§8 BUILD CHECKPOINT) |
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
| **The Augur** (AI reasoning tier — L3 evaluator) | Weaver's AI-assisted reasoning tier for ambiguous/novel convergence gaps. The marquee AI-native feature. | ★★ | M–L | 🏗️ building · [design](../../implementation-artifacts/augur-design.md) · Fire 1 (adaf7be) + Fire 2a (3dbd049) shipped; **Fire 2b (approved→dispatch loop-closer) DESIGNED** → 📐 awaiting-Andrew · [dispatch design](../../implementation-artifacts/augur-dispatch-pickup-design.md) · resolves §3.4: a primordial `augurDispatch` convergence target + an opt-in §10.8 `proposedOp` action (row-carried op gated by the dispatch-time §5 validator) + a `RecordProposalDispatch` flip; proposal-scoped deterministic requestId ⇒ re-dispatch is collapse-only; §10.8 action-table edit staged UNCOMMITTED; ONE fire, internal build order |
| Starlark guards (Loom) | The reserved `{reads, starlark}` guard escape hatch needs a verified-pure sandbox. | ★ | M | ✅ ratified (split) · [design](../../implementation-artifacts/loom-starlark-guards-design.md) · 🚧 Loom-side held (ships with first consumer) |

### Read-model / projection maturity
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Refractor] Link-triggered reprojection (plain/GrantTable lenses)** | Eager relationship-grant freshness. **Downgraded ★, de-blocked — NOT a D1.3 blocker.** | ★ | M | ✅ ratified · [design](../../implementation-artifacts/link-aspect-triggered-reprojection-plain-lenses-design.md) · ⚠️ consolidate-decision vs Negative/filter-retraction (Andrew) |
| Negative / filter-retraction projection | True "emit-only-when-violating" (targets currently project one row per candidate with a `violating` flag). | ★→★★ | M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/negative-filter-retraction-projection-design.md) · consolidation target for Link-triggered reprojection |
| Elasticsearch target adapter | A third lens target adapter (only NATS-KV + Postgres ship; no consumer yet). | ★ | M | 📋 (no consumer yet) |
| Link-tombstone re-projection · cross-instance latency rollup | Two projection edge-cases / observability gaps (current approaches work). | ★ | S each | 📋 |

### Refinements & ops
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **CI pipeline speed (continuous)** | Make CI faster without weakening any gate — owned continuously by the **Whetstone**. Matrix split done (serial → 4 parallel jobs); convergence + unit parallelized. | ★★ | M (ongoing) | 🏗️ continuous (Whetstone) · next: `loom`/`bridge` `t.Parallel()` |
| **[CI/Refractor] Hello-Lattice NFR-P3 latency flake** | The `≤500ms` capability-projection probe fails-then-passes on the shared CI runner (~590ms infra floor) — the dominant re-run flake (~50%). Not Whetstone-maskable (loosen/retry both weaken the gate). | ★★ | M | 🔭 owner/Andrew decision (infra-bound; shave CDC lag / bigger runner / re-scope CI conformance) |
| **Op-time bounded reverse-link / adjacency read (`kv.Links`)** | One sanctioned, bounded, fail-closed, paged op-time link-enumeration builtin (`kv.Links(hub, relation, direction, cursor, limit)`) — retires the key-list-in-aspect guard indexes. Relaxes the write-path no-scans invariant by exactly one primitive. | ★★★ | M–L | 🏗️ building · [design](../../implementation-artifacts/op-time-bounded-link-enumeration-design.md) · Fire 1 (primitive, cc2613f) + Fire 2 (clinic `hasBooking`+`bookingGuard` consumer) shipped; next = Fire 3 (optional ephemeral e2e + §8 hub-source lint); also unblocks the Loom effect-guard Fire 2 |
| **Hard-delete mutation verb (true link/aspect keyspace reclaim)** | The Starlark mutation vocab is `create`/`update`/`tombstone` (all soft PUTs); a soft-tombstoned key persists and is still enumerated by `kv.Links`. A 4th `delete` verb (NATS `DEL` marker via the existing `BatchOp.Delete` seam + step-8 + Contract #3) lets terminal `hasBooking` links (and other dead keys) leave the keyspace, bounding `kv.Links` **LIST** cost. Surfaced by kv.Links Fire 2. | ★ now / ★★ at scale | M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/hard-delete-mutation-verb-design.md) · §3 contract edit staged UNCOMMITTED; `DEL`-not-`PURGE` (op-ledger untouched, KV history retained); `ProtectedKey` backstop extended; 2 fires (clinic `hasBooking` reclaim = consumer) |
| **Script-read posture — declared+hydrated vs live `kv.get`/`kv.Links`** | Live Core-KV reads in scripts are the common root of the Loom-guard Processor-side redirect *and* the Edge A′-prediction partiality; declared+hydrated reads the norm, live reads classified (debt vs sanctioned config vs irreducible `kv.Links`), Loom guard read retired Processor-side. | ★★ | L | 📐 awaiting-Andrew · [design](../../implementation-artifacts/script-read-posture-design.md) · §2.5 `optionalReads` edit staged uncommitted; Fire 3 retires Loom `evalGuard` (G1 rec.) |
| **FR28 — role-queue + fallback** (+ FR29 unrouted surfacing) | A `queuedFor.role` link + `ClaimTask` op + `CreateTask` routing (named → role-queue → loud `RoutingFailed`); grant/inbox fan out to role-holders; an empty queue is surfaced post-hoc by a new `unroutedTasks` Weaver target. | ★ | M | ✅ ratified · [design](../../implementation-artifacts/fr28-role-queue-fallback-design.md) · 📋 ready (3 fires; §10.1 committed) |
| **`@every` recurring schedules** (op-vertex pruner #49 retired) | A `substrate.ScheduleEvery`/`CancelSchedule` seam + migrate the Weaver reconciler sweep (`time.Ticker` → durable `@every`). Op-vertex pruner retired (NATS per-key TTL + outbox tombstone cover it). | ★ | M | 🏗️ building · [design](../../implementation-artifacts/recurring-schedules-and-op-vertex-pruner-design.md) · Fire 1 (substrate `ScheduleEvery`/`CancelSchedule` primitive + occurrence-requestId helper) shipped; next = Fire 2 (Weaver sweep cron-kill) + Fire 3 (§10.4 doc/contract, Andrew-gated commit) |
| **Package version upgrade / DDL hot-reload (F-004)** | In-place re-install over an existing version + DDL-migration semantics (install/uninstall existed; upgrade did not). Diff-and-apply (create/update/tombstone) in one atomic Processor batch; version-independent entity keys. | ★★ | M | ✅ effectively done · [design](../../implementation-artifacts/package-version-upgrade-design.md) · Fires 1a–3 shipped; only an optional Fire-2 live e2e remains (§8.1 + §8.6 committed) |
| Loom / Weaver control-API surfacing | Operator pause/resume + a durable `loom.*` read model beyond what the Loupe blocker covers. | ★ | M | 📋 |

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

- 2026-06-30 · `44b385a` · [Core/substrate] `@every` Fire 1 — recurring-schedule primitive: `ScheduleEvery`/`CancelSchedule` (idempotent re-arm via the server's per-subject rollup; purge-by-subject cancel, no-op idempotent) + the 3 fired-copy header constants (pinned to nats-server v2.14 `JSScheduler`/`JSScheduleNext`/`JSScheduleTTL`) + `DeriveScheduleOccurrenceRequestID` (the @every analog of @at `deriveTimerRequestID`). Grounded in the pinned NATS 2.14 source, proven against embedded NATS 2.14. No contract change (§4.3); §10.4 spec edit is Fire 3. (Fire 2 = Weaver sweep cron-kill; in the feature backlog.)
- 2026-06-29 · `d6530e9` (+`cacfb2b` Fire 1 dark grants) · [Core/processor+rbac] Lane authorization enforcement (Contract #2 §2.3) — step-3 lane gate (service/task ⇒ default-only pre-read; platform ⇒ `env.Lane ∈ doc.Lanes`, fail-closed) emitting `LaneUnauthorized` "before any further processing" + Gate-3 Vector #8 (default-only actor on system/meta/urgent → BLOCKED; 8/8) + docs (service-actors.md deferral retired, processor.md note); full 3-layer review. Whole item done (optional Fire 3 role→lane blocks nothing — installs run as primordial admin). Build-to §2.3/§2.6, no contract change.
- 2026-06-30 · `0cd2695` · [lint/Core] instanceOf-template op-discovery — **DONE** (P7 lint gate, the design's last DoD item): `scripts/lint-conventions.go` P7 check flags a discriminator-shaped aspect (`.class`/`.family`/`.kind` localName) shadowing the envelope class; anchored on the Starlark aspect-emit helper (zero false positives vs CLI flag / string-slice / `cls` arg), skips tests. Green because the outliers were retired in e1d540f. Whole design complete (Fire 1 `cea0c3b` + Fire E `6eaabcc` + Verticals consumer `e1d540f`/`2a5087a` + Contracts #1 §1.5/§1.6 + #2 §2.1 + P7 in lattice-architecture.md).
- 2026-06-27 · `679fe25` (+`faa3aec` GrantTable composite variant) · [Refractor] Full-engine plain-projection anchor-tombstone retraction — `Engine.AnchorDeleteResult` (AST-only) + the non-actor pipeline twin emit a Delete on a root-tombstone of the lens anchor; fixes every full-engine plain lens (clinic/loftspace/lease/objects-base); doc note in refractor.md. (Reconciled stale board row 2026-06-30: design banner said 📋 ready but Fire 1 had shipped; clinic ephemeral-stack e2e is verticals-lane convergence-suite work.)
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
- 2026-06-28 · `cd20ce8` · [Core/pkgmgr] F-004 Fire 1a — version-independent entity keys
- 2026-06-28 · `75e9acc` · [Core/substrate] NATS write-restriction Fire 1 — credential seam (dark)
- 2026-06-28 · `04c7689` · [Weaver] Pace collapse-only userTask reclaims with a state machine
- 2026-06-27 · `d8bfa34` · [Loom] Pin the redelivery-dedup + op-meta-deregister paths
- 2026-06-27 · `4bd32f7` · [Core] Pin the Go↔Starlark value marshalling boundary
- 2026-06-27 · `8199c11` · [Weaver] Cover the control-plane authorize boundary + subject parsing
- 2026-06-27 · `a4f87ae` · [Core] Pin the substrate deterministic-id derivation invariant
- 2026-06-27 · `fd0cacd` · [Loupe] Test the object-serving anti-XSS disposition boundary
- 2026-06-27 · `f537f6b` · [Refractor] Postgres adapter `Truncater`
- 2026-06-27 · `c59a39f` · [Loom] Heartbeat status hard-coded to lifecycle string
- 2026-06-27 · `6998a39` · [Core] Bootstrap key construction → substrate key helpers
- 2026-06-27 · `f16e625` · [Core] Processor health-honesty — real `lane_lag` + status/issues
- 2026-06-26 · `4de7677` · [Weaver] Heartbeat status/issue-severity inconsistency
- 2026-06-26 · `2877a1c` · [Loupe] Surface component `status`/`issues` on health + system-map
