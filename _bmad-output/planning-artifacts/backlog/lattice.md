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
| **[Weaver] `inflight_<g>`-as-external-gap-marker is unenforced** | The stale-mark reclaim relies on `inflight_<g>` only ever being lens-authored for a real outcome-driven external gap; true today but not install-time enforced. | ★ | S | 📋 needs-design (Designer) · install-time lens-schema check impossible as scoped (2026-07-10); runtime candidate: `staleMark` consults the gap's action class from the target spec — Weaver holds both at runtime |
| **[Bridge/Processor] Op-status read surface — `lattice.op.status` responder** | Processor-hosted op-status RPC (vault.decrypt pattern); all 4 named submitters migrated. | ★★★ | S | ✅ all 4 fires shipped · [design](../../implementation-artifacts/op-status-read-surface-design.md) · 🔭 §10.6 contract edit push-held for Andrew (see design doc Status) · unblocks matrix-hygiene read-tightening follow-on |
| **[Refractor/rbac-domain] service-location grants missing from `cap.roles` projection** | 8 operator permissions are `grantedBy`-linked in Core KV but absent from the live `cap.roles.<actor>` doc — ops wrongly denied; comparable grants from other packages DID propagate. | ★★★ | ? | 🔭 flag-for-Andrew · filed as spawn_task chip 2026-07-12 (task_cbc1d94b) |

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
- **Next:** Core.

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
> carries its own ✅ ratified / 📋 ready items): **Edge Lattice EDGE.1 + EDGE.2 + EDGE.3 CLOSED**
> (2026-07-12) — the offline-first read loop, the optimistic write path, and the untrusted
> multi-identity security turn-on (Gateway-submit, Personal Lens PL.3 fan-out, per-identity
> subscribe-ACL) are all done — see [edge design §7](../../implementation-artifacts/edge-lattice-full-design.md).
> **Next Edge increment: EDGE.4** (Vault Proxy, gated on Vault Phase A + PL.5, both since shipped) —
> **now ALSO gated** on the newly-filed `personal.hydrate`/register/deregister capability-auth gap
> (Security & trust boundary table above): under real `LATTICE_AUTH_MODE=capability` no ordinary
> identity's own Edge node can reach these RPCs at all today (control-operator-only grant, hydrate
> ungranted even there, no self-scope concept) — EDGE.4's session-key RPC would land in the exact same
> unreachable family, so build that self-scope primitive first. **EDGE.5** (browser/mobile node, gated
> on the Gateway WS/push bridge) remains a separate, larger, Designer-scoped item. A full multi-persona
> adversarial re-review of the EDGE.3 security boundary is still flagged open in the design doc §8.
> **sensitive-param-egress CLOSED** (2026-07-11) — Fire 1 (disposition + emission guard) + Fire 2 (bridge
> unwrap + lease-signing live consumer) both shipped, CI green.
> **edge-manifest Fire 0 SHIPPED** (2026-07-12, `78955d0`) — `pkgmgr.LensSpec` can now declare a
> `nats-subject` Personal Lens; SYNC stream carries the designed 24h MaxAge; `internal/edge/sync`
> exports an `OnChange` hook + `UpdateInterest` passthrough.
> **edge-manifest Fire 1 CLOSED** (2026-07-12, `f6be3b0`, final increment) — `install-edge-manifest` +
> `make seed-edge-demo` + a genuine live e2e (`internal/refractor/edge_manifest_fire1_e2e_test.go`) close
> Fire 1's own green bar; also fixed a real bug where the 5 shipped lenses lacked the `anchor` column
> `projection.personalEnvelopeFn` requires, so they'd never have published a delta in production.
> Live-stack `make seed-edge-demo` is currently blocked by a separately-filed capability-projection bug
> (see the Security & trust boundary table) — not a fault of edge-manifest's own code. **Next: Fire 2**
> `[verticals]` — Facet v0 dev host + renderer (own lane, not Lattice's).
> **AI-caps Fire 4 materializer NOT yet build-ready** (verified 2026-07-11): sign-off condition 1
> (Processor-MAC'd sensitive-refs, sensitive-param-egress-design.md §3.6) has no code anywhere
> (`git log --all` shows only the ratification doc commit) — it needs its own design pass
> (lattice-designer), not an inline build. Fire 4's vertexTypeDDL/opMeta materializer stays blocked
> until that lands; do not pick it up as "build-ready" without re-verifying.
> Whoever ships the named pick updates this callout to the next one — a stale callout starves the lane.

### Security & trust boundary
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| NATS account-level write restriction | Close the fabricated-KV-write surface at the substrate (account-level); today defended only by overwrite-by-reprojection. | ★★ | M | ✅ effectively done · [design](../../implementation-artifacts/nats-account-write-restriction-design.md) §Fire-3-status · only deferred Fire 4 (prod mTLS) remains |
| **Keyed identity-index hashes (HMAC)** | Unkeyed `sha256NanoID` contact hashes are dictionary-testable with substrate access and persist in JetStream history post-shred; a Vault-keyed HMAC bounds it but needs a MAC primitive + key custody at every hash computer, and must migrate ALL index consumers (identityindex, provision probe, dedup) in one stroke. | ★ now / ★★ prod | M | 🗄️ shelved (revive: production threat model) · [analysis](../../implementation-artifacts/dedup-over-encrypted-pii-design.md) §9.1/§10-C |
| **`personal.hydrate`/`register`/`deregister` unreachable by real identities under capability auth** | Transport grant still empty post-Fire-2 (`natsauth.go` `controlRPCs`) AND the capability gate needs `ctrl.refractor.<verb> scope=any`, granted only to `control-operator` (hydrate ungranted even there) — no self-scope exists. | ★★★ | M | 🔭 flag-for-Andrew · not new design, it's §3.3's own ratified-but-unbuilt grant; fully diffed as spawn_task chip 2026-07-12 (grant-widening needs attended sign-off) · blocks EDGE.4 |

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
| **Global identity for a hyperscale tenant** | A hyperscale tenant (WeWork) spans cells/regions — cross-cell shadows + cross-region residency on top of multi-cell. | ★ now / ★★★ at hyperscale | L–XL | 📐 awaiting-Andrew · [design](../../implementation-artifacts/global-identity-hyperscale-tenant-design.md) · 🚧 build behind multi-cell Fire 2 + a real hyperscale driver; NO contract change (one scoped multi-homed-`identity` exception flagged); 5 fires |
| **HA NATS clustering** | Single-server today; clustering + multi-instance engine fan-out. | ★ now / ★★ prod | M–L | ✅ ratified · [design](../../implementation-artifacts/ha-nats-clustering-design.md) · 🚧 shelved (prod-HA driver) |

### Edge & personal lenses (the path Loupe grows into)
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| Personal / Secure Lens | Refractor projects a per-identity security-filtered subgraph stream; the Interest-Set watchlist; RLS-style link filtering. | ★★ | L | ✅ effectively done · [design](../../implementation-artifacts/personal-secure-lens-design.md) · Fires 1–5 shipped (D1 + Vault gates closed); PL.6 (multicast dedup, WebSocket bridge) deferred, no Edge consumer yet |
| Edge Lattice (full) | The sovereign per-user node: local VAL (SQLite/IndexedDB), local Starlark, offline-first, reconcile-by-revision. EDGE.1–3 (Go node, offline loop, untrusted security turn-on) shipped; EDGE.4–5 per the §7 gates. | ★★★ | XL | 🏗️ building · [design §7](../../implementation-artifacts/edge-lattice-full-design.md) · EDGE.1–3 done · next: EDGE.4 (Vault Proxy) or EDGE.5 (browser node), both build-ready |
| Edge-manifest + personal-lens consumer (Facet platform half) | Five per-identity `nats_subject` manifest lenses (me/services/catalog/tasks/instances) + descriptor vocabulary (presentation/per-op schema/dispatch); `pkgmgr.LensSpec` `nats_subject` adapter; `RequestService` service-path op; seeded topology. Un-defers PL.6/EDGE.5. | ★★★ | L | ✅ CLOSED (Fire 1) · [design §7](../../implementation-artifacts/edge-showcase-app-design.md) · next: Fire 2 `[verticals]` Facet v0 app |

### AI-native
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| AI-authored capabilities | A Lattice-aware agent proposes DDL/Starlark/lenses/workflows through human review + deterministic validation + rollback. | ★★–★★★ | L | 🏗️ building · [design](../../implementation-artifacts/ai-authored-capabilities-design.md) · Fire 4 inc 1 (sandbox leaf) SHIPPED `474745b` · 🚧 blocked-on: MAC-hardening design (see callout above) before the materializer kinds · Loupe UI is Stream 3's lane |
| **The Augur** (AI reasoning tier — L3 evaluator) | Weaver's AI-assisted reasoning tier for ambiguous/novel convergence gaps. The marquee AI-native feature. | ★★ | M–L | ✅ Fires 1+2a+2b shipped incl. §6 residual e2e (loop closes: escalate→review→dispatch) · [design](../../implementation-artifacts/augur-design.md) + [dispatch design](../../implementation-artifacts/augur-dispatch-pickup-design.md) · 🚧 Fire 3 autoApply Andrew-gated |
| Starlark guards (Loom) | The reserved `{reads, starlark}` guard escape hatch needs a verified-pure sandbox. | ★ | M | ✅ ratified (split) · [design](../../implementation-artifacts/loom-starlark-guards-design.md) · Fire 1 (shared sandbox) SHIPPED `474745b` via AI-caps · 🚧 Fire 2 (Loom-side) held on guard-eval-location decision |
| **Weaver planner mandate (dispatcher → solver)** | Remediation stops being a static gap→action lookup: deterministic planner (per-gap candidate selection, then goal-regression synthesis over op-declared effects) with contraction/oscillation diagnostics and admission control; shadow mode + per-target cutover; the Augur stays the AI boundary. | ★★★ | XL | ✅ effectively done · [design](../../implementation-artifacts/weaver-planner-mandate-design.md) · Fires 1-9(Inc1)+R1-R3 shipped, consumed by LoftSpace renewals; Fire 9 AI tail deferred - needs a novel Augur gap, not renewals |

### Read-model / projection maturity
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| Elasticsearch target adapter | A third lens target adapter (only NATS-KV + Postgres ship; no consumer yet). | ★ | M | ✅ ratified (2026-07-02, OpenSearch pin + FTS-first interim) · [design](../../implementation-artifacts/search-target-adapter-design.md) · shelf — first consumer (LoftSpace FTS unified search) filed on verticals; the OpenSearch adapter builds only on search-engine-scale demand |
| **[Refractor] Cross-instance projection-latency rollup** | Aggregate per-lens projection latency across Refractor instances into one per-component view (single-instance today, so per-instance == per-component). Link-tombstone re-projection half **subsumed** by the link-aspect reprojection design. | ★ | S | 🚧 seq behind HA-NATS multi-instance · [link-aspect design](../../implementation-artifacts/link-aspect-triggered-reprojection-plain-lenses-design.md) subsumes the tombstone half; no multi-instance consumer yet |

### Refinements & ops
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **CI pipeline speed (continuous)** | Make CI faster without weakening any gate — owned continuously by the **Whetstone**. Matrix split done (serial → 4 parallel jobs); convergence + unit parallelized; unit itself now sharded across 2 runners. | ★★ | M (ongoing) | 🏗️ continuous (Whetstone) · unit-1/unit-2 shard: ~145s total wall-clock vs ~237s single-runner unit (Done log) · next: caching / a slow-test trim, or a 3rd unit shard if either side grows past ~150s |
| **Hard-delete mutation verb (true link/aspect keyspace reclaim)** | Mutation vocab is create/update/tombstone (soft PUTs); a tombstoned key persists + is still enumerated by `kv.Links`. A 4th `delete` verb (NATS `DEL`) lets dead links leave the keyspace, bounding `kv.Links` LIST cost. | ★ | M | 🗄️ shelved (Andrew 2026-07-02) · [design + hold banner](../../implementation-artifacts/hard-delete-mutation-verb-design.md) · demand dissolved by clinic write-path slot claims; §3 edits reverted; revive only on a real reclaim driver |
| **Script-read posture — declared+hydrated vs live `kv.get`/`kv.Links`** | Declared+hydrated reads as the write-path norm: `optionalReads` folds read-before-create in; `kv.Links` declared-as-metadata (Edge-gate + best-effort lint, not hydrated); guards become a generic Processor-side operation feature (supersedes Loom's engine read). | ★★ | L | ✅ Fires 1–2 shipped · [design §12](../../implementation-artifacts/script-read-posture-design.md) · Fire 3 (guards) deferred to its first consumer; debt sweep + warn→block flip = its own row below |

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

- 2026-07-13 · `9a86a01` · [Refractor] projection-package coverage sweep — Install{ActorAggregate,PersonalLens} wiring + personalEnvelopeFn D1/Interest-Set branches; 59.2%→93.0%; CI green
- 2026-07-13 · `a6c3802` · [Core/bootstrap] test-coverage sweep — Persist, PrivacyActorKey (incl. pre-v15 absent case), seedPrimordialPerKey concurrent-bootstrap fallback; 65.7%→69.3%; CI green
- 2026-07-12 · `4b8e815` · [Weaver] registry-cleanup-edge-branches-uncovered SHIPPED — CDC malformed-input paths covered, 84.8%→86.2%; CI green
- 2026-07-12 · `d24446e` · [docs] doc sweep — README/architecture-overview/loupe.md corrected to reflect shipped D1 + Personal Lens + Edge Lattice EDGE.1-3 (were still marked designed/deferred)
- 2026-07-12 · `f6be3b0` · [edge-manifest,refractor] edge-manifest Fire 1 CLOSED — install-edge-manifest chain, seed-edge-demo, live e2e; fixed a lens anchor bug blocking all 5 lenses from ever publishing; CI green
- 2026-07-12 · `1b778f9` · [pkgmgr,edge,edge-manifest] edge-manifest Fire 1 inc 2 — 5-lens `packages/edge-manifest` (first nats-subject Personal Lens package), edge/store.go manifest.* key exemption, verify-package-edge-manifest; CI green
- 2026-07-12 · `17d6fbe` · [CI] unit job split into weight-balanced unit-1/unit-2 shards + a coverage-guard job; overall wall-clock 237s→145s (~39% faster), CI green
- 2026-07-12 · `cd5a077` · [pkgmgr,Processor,service-domain] edge-manifest Fire 1 inc 1 — OpMetaSpec descriptor-vocabulary fields + RequestService service-path consumer op + template .presentation aspect; CI green
- 2026-07-12 · `78955d0` · [pkgmgr,Refractor,Edge] edge-manifest Fire 0 — nats-subject LensSpec adapter, SYNC stream 24h MaxAge, edge/sync OnChange + UpdateInterest; CI green
- 2026-07-12 · `8d4ebd9` · [CLI] op-status-read-surface Fire 4 CLOSED — `lattice op status` migrates off raw Core-KV KVGet onto the lattice.op.status RPC; live-stack smoke-verified; CI green
- 2026-07-12 · `3bd743c` · [Loom] op-status-read-surface Fire 3 — trackerExists migrates to the lattice.op.status RPC; taskVertexExists retired; §10.6 contract edit staged uncommitted; CI green
- 2026-07-12 · `a4446d5` · [Gateway] op-status-read-surface Fire 2 — GET /v1/operations/{requestId} backs the 202-fallback poll onto the lattice.op.status RPC; CI green
- 2026-07-12 · `f12f4ce` · [Processor,Bridge] op-status-read-surface Fire 1 — lattice.op.status responder replaces the bridge's direct Core-KV skip-probe read; CI green
- 2026-07-12 · `bd3f4b7` · [Edge,Gateway] Edge Lattice EDGE.3 CLOSED — agent.Submitter + GatewaySubmitter replace direct core-operations submit; Gate-3 e2e proves valid-submits/revoked-denies vs a real gateway.Server; CI green
- 2026-07-12 · `eec08a6` · [identity-domain,Gateway] multi-credential-identity-linking Fire 4 CLOSED — UnlinkCredential + credentialindex revive-safety + materializer bucket-delete fold; Fires 1-4 all shipped; CI green
- 2026-07-12 · `3e345d1` · [Edge,scripts] per-identity-nats-subscribe-acl Fire 3 CLOSED — live-stack revocation e2e proves vector 4 against real prod wiring; EDGE.3 gate flipped build-ready; CI green
- 2026-07-12 · `2f07d93` · [Edge,Refractor] per-identity-nats-subscribe-acl Fire 2 — cmd/edge EDGE_TOKEN connect + inbox scoping; Refractor personal.{register,deregister,hydrate} bind to the verified actor; CI green
- 2026-07-11 · `a3ec8d5` · [Gateway,natsperm] per-identity-nats-subscribe-acl Fire 1 CLOSED — xkey day-one condition wired (UnsealRequest/SealResponse sealed-box round trip); CI green

- 2026-07-11 · `ce47946` · [CI] natsperm `t.Parallel()` experiment — local win (34.4s→25.9s) didn't hold in CI (unit job CPU-oversubscribed under `-p 4`); reverted (85b77a9→ce47946); CI green
- 2026-07-11 · `232f9ea` · [Loupe] systemmap/lens-detail Core-KV listing scoped to vtx.package. subtree — false "RED — all absent" landing banner fixed, verified live on the 13K-key bucket
- 2026-07-11 · `a6aeb04` · [Processor] deterministic-requestId resubmits unbrick on tracker/outbox subject residue (Contract #4 §4.3/§4.5 realized; F-004 same-version force-refresh works past 24h again); regression tests
- 2026-07-11 · `616c3fa` · [natsperm] $JS.FC.> granted matrix-wide — the lattice-pkg install hang root-caused (flow-control ack denied → permanent KV-listing stall) + lattice-pkg 60s bound + substrate partial-listing guards
- 2026-07-11 · `4bae4d5` · [CI] two unit-job flakes root-caused at source — processor ack/term consumer-state reads + substrate AckResume dead-iterator race; poll/quiesce test barriers, vendor-source-grounded (detail in commit msgs); CI green
- 2026-07-11 · `5ce906b` · [identity-domain,gateway] multi-credential-linking Fire 3 — provision-time identityindex probe (whoami `?probe=1`), built as a P5-clean lens read (§3.4 build-note); no contract change; CI green
- 2026-07-11 · `bfe91b4` · [scripts] verify-package-identity — expect Fire 2's 2 new permittedCommands (CI stack-gates caught the stale count live); CI green
- 2026-07-11 · `625f411` · [identity-hygiene] multi-credential-linking Fire 2 — link flow (InitiateCredentialLink/CompleteCredentialLink) + Gateway whoami; NFR-S6 leak found+fixed in review; CI green
- 2026-07-11 · `259e4a1` · [identity-hygiene] multi-credential-linking Fire 1 — MergeIdentity repoints every credential resolving to a merged-away secondary onto primary; Gateway materializer folds identity.rebound; CI green
- 2026-07-11 · `72919d9` · [identity-hygiene] dedup-over-encrypted-pii Fire 3 CLOSED — ShredIdentityKey in-commit indexes/duplicateOf erasure + CreateUnclaimedIdentity revive fix + batch-size preflight; Fires 1-3 all shipped; CI green
- 2026-07-11 · `f4eb556` · [CI] natsperm deniedTimeout 2s→500ms (unit job's new long pole after this morning's matrix-hygiene Fire 1 added 2 registry-driven tests) — unit job 5m20→3m25, CI green, no new flakiness (stress-ran ×10)
- *(older entries rolled to [archive/lattice-done.md](archive/lattice-done.md); includes `94c8224` hello-lattice NFR-P3 flake fix)*
