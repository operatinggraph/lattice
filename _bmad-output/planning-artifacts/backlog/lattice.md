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
| **[Weaver] Fresh-episode/reclaim error-branch coverage** | `fireEpisode`'s stale-mark reclaim path (NanoID-mint + `marks.replace` failures, 41.4% cov), `bumpDispatchCount`/`bumpEffectDispatch` failure-log branches (50%), `sweeper.deleteEffect` conflict/delete-failure (44.4%), and `reconcileConsumers` supervisor Add/UpdateSpec/Reset/Remove + health-sink-delete failure paths (62.7%) are the lowest-covered branches in an otherwise 86.8%-covered package (`internal/weaver/evaluator.go`, `reconciler.go`, `engine.go`). | ★ | S–M | 📋 ready |
| **[Weaver] Doc drift — stale op-vertex-pruner deferred bullet** | `docs/components/weaver.md` "Deferred (Phase 3+)" still lists "Full temporal scheduler / op-vertex pruner (#47/#49)" but the same doc's "Temporal lane" section (above it) already states #47 is realized by the two scheduling legs and #49 is retired — self-contradicting; drop the stale bullet. | ★ | XS | 📋 ready |

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
- 2026-07-18 Weaver (healthy, 86.8%/78.6%/91.3% cov, clean lint, no TODOs; filed error-branch-coverage + a doc-drift fix).
- **Next:** Loom.

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

> 🎯 **Build-ready now** (this section + the **Arch-review intake** above, which carries its own
> ✅ ratified / 📋 ready items). **Edge Lattice (EDGE.1–5) is mechanism-complete** — EDGE.1–4 plus
> EDGE.5 W1–W4 all shipped and tested; the fire-by-fire history lives in the
> [edge design](../../implementation-artifacts/edge-browser-node-design.md) + the Done log + git, not here.
> The attended `:9222` browser Gate-3 run is an optional live demo of proven mechanisms, **not a gate**.
> **The next build-ready picks are the 📋 ready / ✅ ratified rows in the feature tables below** — a stale
> callout starves the lane, so whoever ships the top pick renames this to the next one.

### Security & trust boundary
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| NATS account-level write restriction | Close the fabricated-KV-write surface at the substrate (account-level); today defended only by overwrite-by-reprojection. | ★★ | M | ✅ effectively done · [design](../../implementation-artifacts/nats-account-write-restriction-design.md) §Fire-3-status · only deferred Fire 4 (prod mTLS) remains |
| **Processor-MAC'd sensitive-refs (ref provenance)** | `$sensitiveRef` values are trusted at the package-DDL boundary; a fabricated ref names another identity's aspect and the bridge unwraps it. Processor MACs the refs it authors; a new ref-verified decrypt RPC + bridge grant swap — the ratified trigger gating AI-caps Fire 4. | ★★★ | M | ✅ CLOSED (2026-07-16) · [design](../../implementation-artifacts/sensitive-ref-mac-provenance-design.md) · Fires 1–2 shipped, CI green |
| **Keyed identity-index hashes (HMAC)** | Unkeyed `sha256NanoID` contact hashes are dictionary-testable with substrate access and persist in JetStream history post-shred; a Vault-keyed HMAC bounds it but needs a MAC primitive + key custody at every hash computer, and must migrate ALL index consumers (identityindex, provision probe, dedup) in one stroke. | ★ now / ★★ prod | M | 🗄️ shelved (revive: production threat model) · [analysis](../../implementation-artifacts/dedup-over-encrypted-pii-design.md) §9.1/§10-C |
| **Edge gap-detection needs STREAM.INFO, which the grant denies** | Warm-resume gap check moved off `$JS.API.STREAM.INFO.SYNC` (denied by the per-identity grant) onto the identity-bound `personal.syncgap` control RPC; the seam sheds `FirstSequence`. | ★★ | M | ✅ CLOSED (2026-07-17) · [design](../../implementation-artifacts/edge-syncgap-control-rpc-design.md) · Inc 1 `0acd68c` + Inc 2 `7fc7b42`; Gate-3 reconnect unblocked |

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
| Edge Lattice (full) | The sovereign per-user node: local VAL (SQLite/IndexedDB), local Starlark, offline-first, reconcile-by-revision. EDGE.1–3 (Go node, offline loop, untrusted security turn-on) shipped; EDGE.4–5 per the §7 gates. | ★★★ | XL | ✅ effectively done · [design §7](../../implementation-artifacts/edge-lattice-full-design.md) · EDGE.1–4 + EDGE.5 W1–W4 all shipped + tested · [EDGE.5 design](../../implementation-artifacts/edge-browser-node-design.md) · attended :9222 browser Gate-3 run = optional live demo, not a gate |
| Edge-manifest + personal-lens consumer (Facet platform half) | Five per-identity `nats_subject` manifest lenses (me/services/catalog/tasks/instances) + descriptor vocabulary (presentation/per-op schema/dispatch); `pkgmgr.LensSpec` `nats_subject` adapter; `RequestService` service-path op; seeded topology. Un-defers PL.6/EDGE.5. | ★★★ | L | ✅ CLOSED (Fires 0–1; +6th read-grant lens at Fire 2) · [design §3.2 amendment](../../implementation-artifacts/edge-showcase-app-design.md) · app half continues as Facet Fire 3 (verticals.md) |

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

- 2026-07-18 · `c26a7d6` · [objectcrypto] cover the Vault wrap/unwrap RPC seam (0%) + AEAD error guards — fake-Vault responder pins marshaling, round-trip, resp.Error propagation, transport + malformed-reply failure; 46.6%→91.4%
- 2026-07-18 · `029256d` · [loom] fix `TestLoomE2E_MidRunRestartExactlyOnce` flake — submit counter trails the write-ahead pending token, so poll `fp.submitted`≥1 (new `waitSubmitted`) instead of reading once; -race -count=8 clean
- 2026-07-17 · `9c830a4` · [weaver] test-cover the effect-mismatch loud surface (flagEffectMismatches set→recover→clear + metric, 53%→100%) + formatISODuration hours/clamp (50%→100%)
- 2026-07-17 · `7fc7b42` · [edge,facet] edge-syncgap Inc 2 — client swap: gapped() over personal.syncgap (bounded retry, strict nil-result), FirstSequence deleted from seam+transports+shell, cmd/facet sync-restart
- 2026-07-17 · `0acd68c` · [refractor,edge] edge-syncgap Inc 1 — the platform op: personal.syncgap control RPC (boolean, identity-bound, own verb) off the control host's own STREAM.INFO; six-place lockstep + STREAM.INFO deny vector
- 2026-07-17 · `ebb02ab` · [edge,refractor] EDGE.5 RR-1/3/4/5 boundary follow-ons CLOSED — edge SYNC adj-watch seq-0 skip + personal-lens requireReadGate fail-closed + producer→consumer round-trip test + REQUIRE_ACTOR_VERIFIER startup guard
- 2026-07-17 · `1573d11` · [facet,edge] EDGE.5 W4 inc 4b serving-wiring — `make up-facet-edge` browser-native stack target (build-edge-wasm + `FACET_BROWSER_ENGINE=1`); serving surface live-verified; live Gate-3 e2e = the tail (fresh :9222 stack)
- 2026-07-17 · `37617be` · [facet,edge] EDGE.5 W4 inc 4a — browser-native serving surface: `FACET_BROWSER_ENGINE` serves wasm+shell + injects `__EDGE_BOOT__` (token in-page/no-store, device id browser-local); nil = shipped Go host unchanged
- 2026-07-17 · `5bbff9d` · [edge] RR-2 sync/agent reconcile hardening — poison-key Term (store.ErrUnstorableKey), unrecognized-status keeps intent queued, overlay Discard matches RequestID; CI green
- 2026-07-17 · `b962871` · [CI] natsperm auth-callout PONG/PING flake fixed — connectEdge retries the pre-PONG RTT-PING race (nats-server 2s gate exceeded under CPU contention); deny vectors + prod conf untouched; stress 2/8→0/8
- 2026-07-17 · `b67612a` · [facet,edge] EDGE.5 W4 inc 3 — renderer feed-source swap: app.js pluggable source (SSE Go-host unchanged) + edge-source.mjs (engine onFrame) + config-gated boot.mjs; `make test-facet-web` CI gate
- 2026-07-17 · `e7a81c6` · [edge] EDGE.5 W4 inc 2 — host-side peer signal/consume: `OnChange`→`signalChange` (leader) + `onPeerChange`→re-read+republish (follower, off-loop goroutine); in-Chrome verified
- 2026-07-17 · `fa99b34` · [edge] EDGE.5 W4 inc 1 — shell multi-tab layer: fixed the latent `electLeader(...).catch` leader-path TypeError + built the BroadcastChannel follower change-signal + first `createShell` unit vectors; CI green
- 2026-07-17 · `86d29c9` · [edge,ci] EDGE.5 W3 inc 3b — JS transport shell over vendored nats.js 3.4.0 + consumer-create wire-form parity test (`edge-consumer-parity` CI job) + vendors.md row; CI green
- 2026-07-17 · `2127e27` · [edge,ci] EDGE.5 W3 inc 3a — wasm host entry (`internal/edge/browser` + `cmd/edge-wasm`) + `make build-edge-wasm`, driven over its JS API on real IndexedDB in Chrome; fetch submitter → 1.71 MB gz; CI green
- 2026-07-17 · `ee270f7` · [edge,ci] EDGE.5 W3 inc 2 — IndexedDB store (`syscall/js`) passing the storetest conformance suite on real IndexedDB in headless Chrome (wasmbrowsertest, pinned); vendors.md rows; CI job `edge-browser-store`; CI green
- 2026-07-17 · `ddd9e25` · [edge,processor,vault] EDGE.5 W3 inc 1 — 4 wire-leaf DTO pkgs (alias re-exports); engine 2.28→1.32 MB gz, zero nats-io under GOOS=js; js gate now asserts go list -deps; CI green
- 2026-07-17 · `af7f2cf` · [edge,substrate] EDGE.5 W2 — store/transport interfaces, storetest conformance harness, `!js` tags on the trusted NATS paths, substrate/keys leaf pkg, `GOOS=js` CI gate; measured the W3 size blocker; CI green
- 2026-07-17 · `e0de4bb` · [natsperm,edge] EDGE.5 W1 — native WS listener + the 6 Edge auth vectors twinned over ws://; origin fail-open killed (shape pin + real 403 handshake); fixed a vacuous ops deny; CI green
- 2026-07-16 · `2a5ee60` · [testutil,lint-conventions] bootstrap-globals-race Fire 2 CLOSED — migrated ~20 suite-local harnesses to EnsurePrimordials + added the lint gate; CI green
- 2026-07-16 · `0e8ecfd` · [testutil] bootstrap-globals-race Fire 1 — EnsurePrimordials (sync.Once) + t.Parallel() re-applied on lease-signing/clinic-domain/identity-domain; CI green
- 2026-07-16 · `219fa0c` · [pkgmgr,capability-author] AI-caps Fire 4 SHIPPED — vertexTypeDDL/opMeta kinds + condition-2 lint + live SensitiveAspectResolver; adversarially reviewed, fixed a real fail-open bug; CI green
- 2026-07-16 · `8a34fe4` · [bridge,vault,natsperm] sensitive-ref-mac-provenance Fire 2 CLOSED — bridge requires+verifies the MAC via lattice.vault.decryptref, natsperm grant swap, DEFENDED fabricated-ref e2e; adversarially reviewed; CI green
- 2026-07-16 · `b96f819` · [vault,processor] sensitive-ref-mac-provenance Fire 1 — Vault.MAC primitive + lattice.vault.decryptref RPC + both mint seams stamp the marker; full 3-layer adversarial review; CI green
- 2026-07-15 · `91a614f` · [CI] fixed natsperm auth-callout flake (unit-1 run 29383547635, Authorization Violation) — test-server auth_timeout 2s→10s under shard CPU contention; prod conf untouched
- 2026-07-14 · `59f4881` · [CI] tried isolating natsperm into its own step + raised `-parallel`; reverted — CI wall-clock 139s→140s, no net win (`-p 4` was already CPU-bin-packed, not natsperm-bound)
- *(older entries rolled to [archive/lattice-done.md](archive/lattice-done.md); includes `94c8224` hello-lattice NFR-P3 flake fix)*
