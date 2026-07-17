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

## Loupe → its own lane

Loupe (`cmd/loupe`) is advanced by **Stream 3** on its own board — **[loupe.md](loupe.md)** (the Loupe 2.0
console program + Loupe component maintenance; runs parallel to this stream, own build lock). Loupe rows no
longer live here; a platform primitive Loupe needs still files HERE per the cross-lane rules.

## Component maintenance

Open items only (shipped ones are in the Done log). Grouped by component tag.

| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Loom] Guardless-step recovery check-before-act probe** | On total `loom-state` loss + a re-triggered `StartLoomPattern`, a fresh instance replays guards from cursor 0 (re-runs an already-applied guarded step). | ★ | S–M | 🗄️ shelved-backup (Andrew: no new engine Core-KV reads) |

### Survey log (round-robin rotation)

Rotation memory only — findings are the filed rows; fire narratives live in commits, never here.
Components: Core · Weaver · Loom · Refractor · Bootstrap · object-store-manager (+ the cross-cutting
feature backlog; Loupe moved to its own lane, [loupe.md](loupe.md)). Survey the stalest
(`git log -1 --format=%ct -- <path>`), note ONE dated line, rotate.

- 2026-07-01 Core (healthy; filed atomic-batch-size-ceiling + uninstall-per-key-OCC).
- 2026-07-01 Weaver (healthy, 83%/77% cov, no TODOs; filed registry-cleanup-edge-branches-uncovered).
- 2026-07-01 Designer — Refractor pipeline fan-out eval-error disposition + adj-watch edge arms (→ 📐).
- 2026-07-01 Loom (healthy, 81%/77% cov, clean lint, no TODOs; filed redelivery/deadline-recovery-edge-branches-uncovered).
- 2026-07-01 Designer — search/ES target adapter (3rd Refractor adapter; OpenSearch rec., FTS interim) (→ 📐).
- 2026-07-01 Designer — feature queue designed-out (all ~30 rows carry a design); resolved stale L309 (link-tombstone subsumed by link-aspect design, latency-rollup seq behind HA). Remaining 📋 = owner test-coverage.
- 2026-07-02 Refractor (healthy, clean lint; retraction/rollup already tracked; filed capability-pipeline-link-aspect-fanout-untested + natskv-guard-edge-branches).
- 2026-07-02 Arch-review, all components — filed the intake section below; Refractor findings held for the post-update re-review; root-identity designation → Designer.
- 2026-07-02 Designer — object-plane-nats-permissions (★★★ arch #2; `$O.core-objects.>` grant fix + first natsperm object vectors; no contract change) (→ 📐).
- 2026-07-05 objmgr-and-bootstrap-component-pages CLOSED — bootstrap/vault/privacyworker pages written, README+architecture-overview updated, Bootstrap + object-store-manager added to this rotation.
- 2026-07-06 Arch-review — Refractor deferred re-review filed ([report](../../../docs/reviews/arch-review-2026-07-06.md)): verdict drifted; 9 rows filed (chronicler-host ★★★, publish-acl ★★★, protected-by-default ★★★); doc/marker truth-up done.
- 2026-07-13 Core (processor healthy, clean lint/vet, no TODOs; step 6.5 sensitive-encrypt path was 0% covered, filled 80.1%→82.0%).
- **Next:** Weaver.

## Arch-review intake — platform hardening & doc/contract truth

Open corrections from the [2026-07-02 full-platform review](../../../docs/reviews/arch-review-2026-07-02.md)
— per-finding `file:line` evidence and per-component verdicts live there; the What-cells here are abridged.
Refractor's deferred re-review is now filed as its own subsection below (2026-07-06).
Severity-ordered; same row discipline as component maintenance (shipped rows collapse to the Done log).

| Item | What it is | Imp | Size | State |
|---|---|---|---|---|

### Refractor re-review (2026-07-06)

The deferred post-update re-review the 2026-07-02 pass held back — verdict **drifted**; full evidence in
[arch-review-2026-07-06.md](../../../docs/reviews/arch-review-2026-07-06.md). The docs-refresh, vendors-row,
and stale-marker corrections were applied in the filing commit (Done log); these are the open builds.

| Item | What it is | Imp | Size | State |
|---|---|---|---|---|

### Weaver re-review (2026-07-06)

Scoped Weaver re-review — verdict **healthy** (best-conformed engine); full evidence in
[arch-review-2026-07-06-weaver.md](../../../docs/reviews/arch-review-2026-07-06-weaver.md). The W2 control
fail-closed fix, W3 validator-parity + heartbeat honesty, W4 targetId install-check, W1/W6 comment +
natsperm hygiene, and the W5 contract reconciliation shipped this session (Done log); these are the
deferred follow-ons.

| Item | What it is | Imp | Size | State |
|---|---|---|---|---|

## Lattice feature backlog — the Phase-3 build queue

The AI-driven flywheel draws from this list (Surveyor files → Designer designs → Steward builds the
ratified). Everything here needs design and is fair game **except** 🚧 Andrew-gated rows. Architectural
**forks** (Gateway, read-path auth, Vault, multi-cell, HA-NATS) and **frozen-contract** changes are
designed-through, but the *fork decision* + the *contract commit* are Andrew's.

> 🎯 **Build-ready now** (this section only — check the **Arch-review intake** section above too, it
> carries its own ✅ ratified / 📋 ready items): **lens-registry-restart-integrity CLOSED** (2026-07-13,
> Fire A `6503f22` + Fire B `8ccdfff`) — live-stack verified (cycled the running Refractor process onto
> the build; 59 lenses reactivated on restart, `lensesRegistered: 59`, no `LensRegistryIncomplete`).
> Next: **Edge Lattice EDGE.1 + EDGE.2 + EDGE.3 CLOSED**
> (2026-07-12) — the offline-first read loop, the optimistic write path, and the untrusted
> multi-identity security turn-on (Gateway-submit, Personal Lens PL.3 fan-out, per-identity
> subscribe-ACL) are all done — see [edge design §7](../../implementation-artifacts/edge-lattice-full-design.md).
> **EDGE.4 SHIPPED** (2026-07-13, `fb557cb` inc 1 + `3c61feb` inc 2 — identity-bound `sessionkey`
> control RPC + `internal/edge/vault` client, local AEAD decrypt via `vault.OpenWithSessionKey`).
> EDGE.1–4 are now all done (see the Edge Lattice row below). **EDGE.5** (browser/mobile node) is
> ✅ ratified (2026-07-16, FORK-W A′) — [edge-browser-node-design.md](../../implementation-artifacts/edge-browser-node-design.md);
> fires W1–W4 ALL run in THIS lane (Andrew: single-lane, incl. W4's Facet renderer swap — do not
> park W4 as "verticals"). **W1 SHIPPED** (2026-07-17, `e0de4bb`) — the WS listener is on and the
> callout is proven transport-invariant. **W2 SHIPPED** (2026-07-17, `af7f2cf`) — store/transport
> interfaces + conformance harness + `GOOS=js` CI gate. **Next named build-ready pick: EDGE.5 W3**
> (browser host) — start with the **DTO extraction** its §3.2 names: the engine is 2.28 MB gz vs a
> ~2.6 MB tripwire because `sync`/`agent`/`vault` reach `nats.go` through wire-type packages, so W3
> would otherwise trip FORK-W B on dead code. The §8 full multi-persona
> adversarial re-review of the EDGE.3 security boundary is ✅ COMPLETE (2026-07-16, Designer, 5 lenses) —
> boundary holds, no CRITICAL/HIGH; 5 hardening follow-ons filed (RR-1…RR-5 below), none an EDGE.5 gate.
> See [edge design §8.1](../../implementation-artifacts/edge-lattice-full-design.md).
> **sensitive-param-egress CLOSED** (2026-07-11) — Fire 1 (disposition + emission guard) + Fire 2 (bridge
> unwrap + lease-signing live consumer) both shipped, CI green.
> **edge-manifest Fire 0 SHIPPED** (2026-07-12, `78955d0`) — `pkgmgr.LensSpec` can now declare a
> `nats-subject` Personal Lens; SYNC stream carries the designed 24h MaxAge; `internal/edge/sync`
> exports an `OnChange` hook + `UpdateInterest` passthrough.
> **edge-manifest Fire 1 CLOSED** (2026-07-12, `f6be3b0`, final increment) — `install-edge-manifest` +
> `make seed-edge-demo` + a genuine live e2e (`internal/refractor/edge_manifest_fire1_e2e_test.go`) close
> Fire 1's own green bar; also fixed a real bug where the 5 shipped lenses lacked the `anchor` column
> `projection.personalEnvelopeFn` requires, so they'd never have published a delta in production.
> The `[Refractor/rbac-domain]` capability-projection bug that had been blocking live-stack
> `make seed-edge-demo` is CLOSED (`0b72492`, 2026-07-13) — no longer a caveat.
> **`[verticals]` Facet Fire 2 SHIPPED** (`f5b3031`, 2026-07-13, dev host + PWA renderer, live-verified).
> Facet's own next is Fire 3 (auth turn-on), now unblocked — see verticals.md.
> **Processor-MAC'd sensitive-refs CLOSED** (2026-07-16) — Fire 1 (mint+verify) + Fire 2
> (bridge swap + natsperm grant swap) both shipped, CI green — see the Security & trust boundary row.
> **AI-caps Fire 4 SHIPPED** (2026-07-16, `219fa0c`) — vertexTypeDDL/opMeta materializer kinds,
> the condition-2 lint, and a live-catalog `SensitiveAspectResolver`; AI-authored-capabilities is now
> effectively done (Fire 5 stays design-only per Andrew's recommendation; a Loupe UI affordance is
> Stream 3's lane) — see the AI-native table row below.
> **`[bootstrap] internal/bootstrap primordial-ID globals race` CLOSED** (2026-07-16) — Fire 1 (`0e8ecfd`,
> `testutil.EnsurePrimordials` + `t.Parallel()` on lease-signing/clinic-domain/identity-domain) + Fire 2
> (`2a5ee60`, migrated the ~20 suite-local harnesses + the lint-conventions gate) both shipped, CI green.
> Whoever ships the named pick updates this callout to the next one — a stale callout starves the lane.

### Security & trust boundary
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| NATS account-level write restriction | Close the fabricated-KV-write surface at the substrate (account-level); today defended only by overwrite-by-reprojection. | ★★ | M | ✅ effectively done · [design](../../implementation-artifacts/nats-account-write-restriction-design.md) §Fire-3-status · only deferred Fire 4 (prod mTLS) remains |
| **Processor-MAC'd sensitive-refs (ref provenance)** | `$sensitiveRef` values are trusted at the package-DDL boundary; a fabricated ref names another identity's aspect and the bridge unwraps it. Processor MACs the refs it authors; a new ref-verified decrypt RPC + bridge grant swap — the ratified trigger gating AI-caps Fire 4. | ★★★ | M | ✅ CLOSED (2026-07-16) · [design](../../implementation-artifacts/sensitive-ref-mac-provenance-design.md) · Fires 1–2 shipped, CI green |
| **Keyed identity-index hashes (HMAC)** | Unkeyed `sha256NanoID` contact hashes are dictionary-testable with substrate access and persist in JetStream history post-shred; a Vault-keyed HMAC bounds it but needs a MAC primitive + key custody at every hash computer, and must migrate ALL index consumers (identityindex, provision probe, dedup) in one stroke. | ★ now / ★★ prod | M | 🗄️ shelved (revive: production threat model) · [analysis](../../implementation-artifacts/dedup-over-encrypted-pii-design.md) §9.1/§10-C |
| **RR-3 — PL.3 fail-closed on `capKV == nil`** | The personal-lens read-grant gate is skipped (lens runs OPEN) when `capKV` is nil, logging only a WARN. Prod-safe today (Refractor exits on KV-open failure), but a future personal lens wired without `capKV` runs fully open. Refuse to register a `personal:true` lens without `capKV`. | ★ | S | 📋 ready · [design §8.1 RR-3](../../implementation-artifacts/edge-lattice-full-design.md) |
| **RR-5 — Assert `ActorVerifier` on the Edge-facing control plane** | The §3.4 control-op identity binding is gated on `verifier != nil`; with no verifier, `body.IdentityID` is self-asserted (dev posture). An untrusted-Edge control service should refuse to start without a verifier rather than silently degrade. | ★★ | S | 📋 ready · [design §8.1 RR-5](../../implementation-artifacts/edge-lattice-full-design.md) |

### Privacy / Vault
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|

### External-I/O maturity (bridge follow-ons)
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| Real adapters + async result-return | Replace the `Fake*` adapters with real vendors + design the async result path. | ★★ | M–L | ✅ async result-return done · real adapters deferred (prod) |

### Scale-out
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| Multi-cell / sharding | Graph scales by **cells** (root + subgraph co-located for atomic writes); global adjacency index + bridge links for cross-cell. | ★ now / ★★★ at scale | XL | ✅ ratified · [design](../../implementation-artifacts/multi-cell-sharding-design.md) · 🚧 seq (prod-scale driver) |
| **Global identity for a hyperscale tenant** | A hyperscale tenant (WeWork) spans cells/regions — cross-cell shadows + cross-region residency on top of multi-cell. | ★ now / ★★★ at hyperscale | L–XL | ✅ ratified (2026-07-16) · 🚧 Andrew-gated: DO NOT BUILD until further notice (does NOT auto-clear on multi-cell Fire 2 / a driver) · [design](../../implementation-artifacts/global-identity-hyperscale-tenant-design.md) |
| **HA NATS clustering** | Single-server today; clustering + multi-instance engine fan-out. | ★ now / ★★ prod | M–L | ✅ ratified · [design](../../implementation-artifacts/ha-nats-clustering-design.md) · 🚧 shelved (prod-HA driver) |

### Edge & personal lenses
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| Personal / Secure Lens | Refractor projects a per-identity security-filtered subgraph stream; the Interest-Set watchlist; RLS-style link filtering. | ★★ | L | ✅ effectively done · [design](../../implementation-artifacts/personal-secure-lens-design.md) · Fires 1–5 shipped (D1 + Vault gates closed); PL.6 WS half subsumed by the ratified [EDGE.5 design](../../implementation-artifacts/edge-browser-node-design.md); multicast dedup stays deferred (bandwidth trigger) |
| Edge Lattice (full) | The sovereign per-user node: local VAL (SQLite/IndexedDB), local Starlark, offline-first, reconcile-by-revision. EDGE.1–3 (Go node, offline loop, untrusted security turn-on) shipped; EDGE.4–5 per the §7 gates. | ★★★ | XL | 🏗️ building · [design §7](../../implementation-artifacts/edge-lattice-full-design.md) · EDGE.1–4 done · [EDGE.5 design](../../implementation-artifacts/edge-browser-node-design.md) W1 ✅ W2 ✅ · next: W3 browser host — DTO extraction first ([§3.2](../../implementation-artifacts/edge-browser-node-design.md)) |
| Edge-manifest + personal-lens consumer (Facet platform half) | Five per-identity `nats_subject` manifest lenses (me/services/catalog/tasks/instances) + descriptor vocabulary (presentation/per-op schema/dispatch); `pkgmgr.LensSpec` `nats_subject` adapter; `RequestService` service-path op; seeded topology. Un-defers PL.6/EDGE.5. | ★★★ | L | ✅ CLOSED (Fires 0–1; +6th read-grant lens at Fire 2) · [design §3.2 amendment](../../implementation-artifacts/edge-showcase-app-design.md) · app half continues as Facet Fire 3 (verticals.md) |
| **RR-1 — Edge `Revision==0` delta ordering hazard** | Personal-lens adjacency-watch reprojection publishes sentinel seq-0 deltas to the Edge; the Edge LWW gate applies-on-equal so a reordered rev-0 upsert/tombstone transiently resurrects/drops a key. Guarded server adapters already skip seq-0; the Edge SYNC adapter doesn't. | ★★ | S–M | 📋 ready · [design §8.1 RR-1](../../implementation-artifacts/edge-lattice-full-design.md) · fix: skip seq-0 adj-watch write for the natssubject adapter |
| **RR-2 — Edge Sync/agent reconcile hardening** | Three coupled defects: poison-key `Nak` hot-loop (should `Term` like a malformed envelope); unrecognized terminal `ReplyStatus` dequeues + loses a durable edit (must stay queued); overlay `Discard` ignores `RequestID` (drops a newer intent's overlay). | ★★ | M | 📋 ready · [design §8.1 RR-2](../../implementation-artifacts/edge-lattice-full-design.md) |
| **RR-4 — Edge producer→consumer envelope round-trip test** | The re-declared `deltaEnvelope` (sync.go) has no test decoding a real `NatsSubjectAdapter` envelope through the consumer struct + `edge/store`; a producer-side field rename passes CI. | ★ | S | 📋 ready · [design §8.1 RR-4](../../implementation-artifacts/edge-lattice-full-design.md) |

### AI-native
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| AI-authored capabilities | A Lattice-aware agent proposes DDL/Starlark/lenses/workflows through human review + deterministic validation + rollback. | ★★–★★★ | L | ✅ effectively done · [design](../../implementation-artifacts/ai-authored-capabilities-design.md) · Fires 1–4 shipped (2026-07-16, `219fa0c`); Fire 5 (auto-apply) design-only per Andrew; Loupe UI is Stream 3's lane |
| **The Augur** (AI reasoning tier — L3 evaluator) | Weaver's AI-assisted reasoning tier for ambiguous/novel convergence gaps. The marquee AI-native feature. | ★★ | M–L | ✅ Fires 1+2a+2b shipped incl. §6 residual e2e (loop closes: escalate→review→dispatch) · [design](../../implementation-artifacts/augur-design.md) + [dispatch design](../../implementation-artifacts/augur-dispatch-pickup-design.md) · 🚧 Fire 3 autoApply Andrew-gated |
| Starlark guards (Loom) | The `{reads, starlark}` guard escape hatch needs a verified-pure sandbox. | ★ | M | ✅ SHIPPED (both fires) · [design](../../implementation-artifacts/loom-starlark-guards-design.md) · Fire 1 `474745b` (shared sandbox) + Fire 2 (Loom guard eval) — see Done log |
| **Weaver planner mandate (dispatcher → solver)** | Remediation stops being a static gap→action lookup: deterministic planner (per-gap candidate selection, then goal-regression synthesis over op-declared effects) with contraction/oscillation diagnostics and admission control; shadow mode + per-target cutover; the Augur stays the AI boundary. | ★★★ | XL | ✅ effectively done · [design](../../implementation-artifacts/weaver-planner-mandate-design.md) · Fires 1-9(Inc1)+R1-R3 shipped, consumed by LoftSpace renewals; Fire 9 AI tail deferred - needs a novel Augur gap, not renewals |

### Read-model / projection maturity
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| Elasticsearch target adapter | A third lens target adapter (only NATS-KV + Postgres ship; no consumer yet). | ★ | M | ✅ ratified (2026-07-02, OpenSearch pin + FTS-first interim) · [design](../../implementation-artifacts/search-target-adapter-design.md) · shelf — FTS interim consumer SHIPPED (`b105cf5`); OpenSearch adapter itself still has no consumer |
| **[Refractor] Cross-instance projection-latency rollup** | Aggregate per-lens projection latency across Refractor instances into one per-component view (single-instance today, so per-instance == per-component). Link-tombstone re-projection half **subsumed** by the link-aspect reprojection design. | ★ | S | 🚧 seq behind HA-NATS multi-instance · [link-aspect design](../../implementation-artifacts/link-aspect-triggered-reprojection-plain-lenses-design.md) subsumes the tombstone half; no multi-instance consumer yet |

### Refinements & ops
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **CI pipeline speed (continuous)** | Make CI faster without weakening any gate — owned continuously by the **Whetstone**. Matrix split done (serial → 4 parallel jobs); convergence + unit parallelized; unit itself now sharded across 2 runners. | ★★ | M (ongoing) | 🏗️ continuous (Whetstone) · aggregate-CPU ceiling confirmed 2x, isolating natsperm into its own step reconfirmed it (Done log) · next: propose paid larger runners to Andrew |
| **`internal/bootstrap` primordial-ID globals race** | `populate()` (nanoid.go) writes ~64 package-level globals per call; `SetupPackageTestEnv` calls it per-test, so `t.Parallel()` races (confirmed `-race`). Blocked parallelizing lease-signing/clinic-domain/identity-domain tests. | ★★ | M | ✅ CLOSED · [design](../../implementation-artifacts/bootstrap-primordial-globals-race-design.md) · Fires 1–2 shipped, CI green |
| **Hard-delete mutation verb (true link/aspect keyspace reclaim)** | Mutation vocab is create/update/tombstone (soft PUTs); a tombstoned key persists + is still enumerated by `kv.Links`. A 4th `delete` verb (NATS `DEL`) lets dead links leave the keyspace, bounding `kv.Links` LIST cost. | ★ | M | 🗄️ shelved (Andrew 2026-07-02) · [design + hold banner](../../implementation-artifacts/hard-delete-mutation-verb-design.md) · demand dissolved by clinic write-path slot claims; §3 edits reverted; revive only on a real reclaim driver |
| **Script-read posture — declared+hydrated vs live `kv.get`/`kv.Links`** | Declared+hydrated reads as the write-path norm: `optionalReads` folds read-before-create in; `kv.Links` declared-as-metadata (Edge-gate + best-effort lint, not hydrated); guards become a generic Processor-side operation feature (supersedes Loom's engine read). | ★★ | L | ✅ Fires 1–2 shipped · [design §12](../../implementation-artifacts/script-read-posture-design.md) · Fire 3 (guards) deferred to its first consumer; debt sweep + warn→block flip SHIPPED `63aab49` |

### Parking lot — very low priority (far, far back)

Real but low-value; do **not** spend design or build effort here unless Andrew greenlights one.

| Item | Why it's parked | Imp | Size | State |
|---|---|---|---|---|
| **Historical state query (FR51)** | Operators query historical state across a time range (audit/ledger + point-in-time reconstruction). Low near-term value + standing storage cost; builds to reserved contract seams. | ★ now / ★★ if real need | M→L | ✅ ratified (design) · [design](../../implementation-artifacts/historical-state-query-design.md) · build deferred (Andrew, revive on a concrete need); archive layers re-home to the Chronicler |
| multi-aspect atomic OCC for `UpdateMetaVertex` | `meta_ddl.go` applies `expectedRevision` to the first changed aspect by design; true multi-key OCC needs a substrate per-key-revision primitive — marginal value. | ★ | M+ | 🗄️ parked |
| freshnessExpiry marker tombstone-on-convergence | A converged marker is read by nothing and harmless; tombstoning buys cleanup not correctness. | ★ | S | 🗄️ parked |
| production freshness-window tuning | A staleness-tolerance vs. timer-churn value judgment — Andrew's call if/when it matters. | ★ | XS | 🗄️ parked |

## Done log — lattice (newest first)

One line per shipped item (`date · SHA · [tag] title`). Oldest roll to `archive/` past ~25.

- 2026-07-17 · `af7f2cf` · [edge,substrate] EDGE.5 W2 — store/transport interfaces, storetest conformance harness, `!js` tags on the trusted NATS paths, substrate/keys leaf pkg, `GOOS=js` CI gate; measured the W3 size blocker; CI green
- 2026-07-17 · `e0de4bb` · [natsperm,edge] EDGE.5 W1 — native WS listener + the 6 Edge auth vectors twinned over ws://; origin fail-open killed (shape pin + real 403 handshake); fixed a vacuous ops deny; CI green
- 2026-07-16 · `2a5ee60` · [testutil,lint-conventions] bootstrap-globals-race Fire 2 CLOSED — migrated ~20 suite-local harnesses to EnsurePrimordials + added the lint gate; CI green
- 2026-07-16 · `0e8ecfd` · [testutil] bootstrap-globals-race Fire 1 — EnsurePrimordials (sync.Once) + t.Parallel() re-applied on lease-signing/clinic-domain/identity-domain; CI green
- 2026-07-16 · `219fa0c` · [pkgmgr,capability-author] AI-caps Fire 4 SHIPPED — vertexTypeDDL/opMeta kinds + condition-2 lint + live SensitiveAspectResolver; adversarially reviewed, fixed a real fail-open bug; CI green
- 2026-07-16 · `8a34fe4` · [bridge,vault,natsperm] sensitive-ref-mac-provenance Fire 2 CLOSED — bridge requires+verifies the MAC via lattice.vault.decryptref, natsperm grant swap, DEFENDED fabricated-ref e2e; adversarially reviewed; CI green
- 2026-07-16 · `b96f819` · [vault,processor] sensitive-ref-mac-provenance Fire 1 — Vault.MAC primitive + lattice.vault.decryptref RPC + both mint seams stamp the marker; full 3-layer adversarial review; CI green
- 2026-07-15 · `91a614f` · [CI] fixed natsperm auth-callout flake (unit-1 run 29383547635, Authorization Violation) — test-server auth_timeout 2s→10s under shard CPU contention; prod conf untouched
- 2026-07-14 · `59f4881` · [CI] tried isolating natsperm into its own step + raised `-parallel`; reverted — CI wall-clock 139s→140s, no net win (`-p 4` was already CPU-bin-packed, not natsperm-bound)
- 2026-07-14 · `ea2b48b` · [CI] internal/substrate's 63 tests now `t.Parallel()` (20.4s→9s local); CI shard flat — ceiling confirmed 2x
- 2026-07-14 · `c22b3a6` · [CI] processor+outbox `t.Parallel()` (29s→9s, 17s→10s); found real `internal/bootstrap.populate()` global-state race blocking the same fix elsewhere
- 2026-07-13 · `e0c64df` · [loom,starlarksandbox] Starlark guards Fire 2 CLOSED — `{reads, starlark}` guard eval lit up, budget-bounded parse-time compile-check, deterministic dict key ordering fix; CI green
- 2026-07-13 · `b56f155` · [CI] internal/natsperm's 32 per-test embedded-NATS conformance tests now `t.Parallel()` (69s→53s in CI, zero races); shard wall-clock unchanged, real bottleneck named
- 2026-07-13 · `0b72492` · [rbac-domain] service-location cap.roles gap CLOSED — ground-truthed healthy live; added a regression test for recurrence
- 2026-07-13 · `f1ce5bb` · [Weaver] inflight_<g>-as-external-gap-marker SHIPPED — staleMark cross-checks ga.Action vs directOp/proposedOp, InflightActionMismatch Health issue on mismatch; CI green
- 2026-07-13 · `3c61feb` · [vault,edge] EDGE.4 increment 2 — `internal/edge/vault` client: session-key request+TTL-cache + local AEAD decrypt via new `vault.OpenWithSessionKey`; `Reader` composes over `overlay.Read`; CI green
- 2026-07-13 · `fb557cb` · [refractor,gateway,control-authz] EDGE.4 increment 1 — identity-bound `sessionkey` control RPC (Vault Proxy trust boundary), grants in lockstep across 3 places; CI green
- 2026-07-13 · `182d751` · [weaver] fixed CI-caught TestTargetSource_StableInstanceGetsFreshDurableEachBoot flake from the age-guarded prune (Loom's sibling test was fixed in Fire A, Weaver's copy was missed); CI green
- 2026-07-13 · `8ccdfff` · [refractor,cmd/lattice] lens-registry-restart-integrity Fire B CLOSED — lensesRegistered metric + RegistryProbe reconciliation + health-summary lens staleness; live-stack verified; CI green
- 2026-07-13 · `6503f22` · [refractor,substrate,loom] lens-registry-restart-integrity Fire A — CoreKVSource per-boot durable (fixes the live P0 cold-registry incident) + age-guarded PruneStaleDurables (all 4 meta-sources inherit it); CI green
- 2026-07-13 · `ca9affe` · [controlauth,natsauth,control-authz] per-identity-nats-subscribe-acl Fire 2 tail — opened personal.hydrate/register/deregister (op table + consumer grant + transport); EDGE.4 unblocked; CI green
- 2026-07-13 · `9a86a01` · [Refractor] projection-package coverage sweep — Install{ActorAggregate,PersonalLens} wiring + personalEnvelopeFn D1/Interest-Set branches; 59.2%→93.0%; CI green
- 2026-07-13 · `a6c3802` · [Core/bootstrap] test-coverage sweep — Persist, PrivacyActorKey (incl. pre-v15 absent case), seedPrimordialPerKey concurrent-bootstrap fallback; 65.7%→69.3%; CI green
- 2026-07-12 · `4b8e815` · [Weaver] registry-cleanup-edge-branches-uncovered SHIPPED — CDC malformed-input paths covered, 84.8%→86.2%; CI green
- 2026-07-12 · `d24446e` · [docs] doc sweep — README/architecture-overview/loupe.md corrected to reflect shipped D1 + Personal Lens + Edge Lattice EDGE.1-3 (were still marked designed/deferred)
- 2026-07-12 · `f6be3b0` · [edge-manifest,refractor] edge-manifest Fire 1 CLOSED — install-edge-manifest chain, seed-edge-demo, live e2e; fixed a lens anchor bug blocking all 5 lenses from ever publishing; CI green
- 2026-07-12 · `1b778f9` · [pkgmgr,edge,edge-manifest] edge-manifest Fire 1 inc 2 — 5-lens `packages/edge-manifest` (first nats-subject Personal Lens package), edge/store.go manifest.* key exemption, verify-package-edge-manifest; CI green
- 2026-07-12 · `17d6fbe` · [CI] unit job split into weight-balanced unit-1/unit-2 shards + a coverage-guard job; overall wall-clock 237s→145s (~39% faster), CI green
- *(older entries rolled to [archive/lattice-done.md](archive/lattice-done.md); includes `94c8224` hello-lattice NFR-P3 flake fix)*
