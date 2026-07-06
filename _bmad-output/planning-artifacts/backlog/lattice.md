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
| **[Weaver] Registry cleanup edge branches uncovered** | `targetSource.removeOwnedTargetLocked` (targetId-rename removal, 33%), `removePatternLocked` + `removeOpMetaLocked` (pattern/op-meta vertex deletion index cleanup, 50%) — untested paths that keep the in-memory dispatch-resolution indices (`patternMeta`, `opMetaByType`) from leaking stale entries when a referenced `meta.loomPattern`/op meta-vertex is deleted or a target's `targetId` is renamed. | ★ | XS–S | 📋 · `internal/weaver/registry.go:372,586,640` |
| **[Loom] Guardless-step recovery check-before-act probe** | On total `loom-state` loss + a re-triggered `StartLoomPattern`, a fresh instance replays guards from cursor 0 (re-runs an already-applied guarded step). | ★ | S–M | 🗄️ shelved-backup (Andrew: no new engine Core-KV reads) |
| **[Loom] Redelivery/deadline-recovery edge branches uncovered** | `engine.go:resumeStepZero` (41.7% — redelivered trigger whose `createInstance` batch committed but step 0 never submitted, incl. the pattern-pin-missing→fail branch) + `state.go:disarmDeadline` (33.3% — KVGet/KVDelete error arms + the already-disarmed no-op that breaks the deadline-watcher re-entry loop) sit untested by any direct unit test. | ★ | XS–S | 📋 · `internal/loom/engine.go:460`, `internal/loom/state.go:451` |
| **[Refractor] Capability-pipeline link/aspect fan-out dispatch untested** | `evalLinkFanOut`/`evalAspectFanOut` (0%) — the actor-aware pipeline's CDC dispatch for `holdsRole`/`grantedBy` link + aspect events that recompute authz on role grant/revoke — has no test at any level; no test references `evaluateLinkFanOut`/`evaluateAspectFanOut` either. | ★★ | S–M | 📋 · `internal/refractor/pipeline/pipeline.go:577,609`, `evaluate.go:319,348,411` |
| **[Refractor] NatsKVAdapter guarded-write CAS-contention + malformed-watermark edge branches uncovered** | `guardedWrite`'s revision-conflict retry loop + CAS-exhaustion path (53.8%) and `storedProjectionSeq`'s `json.Number`/malformed-doc branches (46.7%) — the H4 no-resurrect guard's contention/legacy-doc handling — untested. | ★ | XS–S | 📋 · `internal/refractor/adapter/natskv.go:190,250` |
| **[Weaver] `inflight_<g>`-as-external-gap-marker is unenforced** | The stale-mark reclaim relies on `inflight_<g>` only ever being lens-authored for a real outcome-driven external gap; true today but not install-time enforced. | ★ | S | 📋 · `internal/weaver/evaluator.go` (`staleMark`) |

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
- **Next:** Core.

## Arch-review intake — platform hardening & doc/contract truth

Open corrections from the [2026-07-02 full-platform review](../../../docs/reviews/arch-review-2026-07-02.md)
— per-finding `file:line` evidence and per-component verdicts live there; the What-cells here are abridged.
**Refractor findings are deliberately absent**: that component is mid-update and Andrew re-reviews it after.
Severity-ordered; same row discipline as component maintenance (shipped rows collapse to the Done log).

| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Gateway] JWKS heartbeat block (Loupe F11 enabler)** | Add a `jwks` block `{keys:[{kid,source,alg,addedAt}],lastPoll,swaps}` to `health.gateway.<instance>`, mirroring the shipped `revocation` block — needs per-kid provenance (source/alg/addedAt) threaded through the auth core (the Verifier stores a bare kid→key map; ParseJWKS drops alg) + swap/lastPoll counters on the poller. | ★★ | S | 📋 · unblocks loupe F11 (JWKS panel); up-full-deploy half shipped (F10 node no longer a ghost) |
| **gateway-claim-flow-authz-contradiction** | Claim ops must be reachable pre-auth, but identity-domain role-gates both (`CreateUnclaimedIdentity` → staff, `ClaimIdentity` → `consumer` self) and a fresh actor holds no role → chicken-and-egg. Grounded: `ClaimIdentity` is unreachable by anyone today; an unauthenticated door wouldn't fix it. | ★★ | M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/gateway-claim-flow-identity-provisioning-design.md) · ratify-but-shelve |
| **contract-10-weaver-text-reconciliation** | Contract #10 Weaver drift — 2 of 5 spots remain (reserved-key, anti-storm cross-ref, revision-history reconciled by the planner-mandate ratify + the shard): the augur block still specs `pattern`+triggerLoom while the engine takes op/adapter/replyOp+directOp, so a package author's field is silently dropped; and §10.2 still calls weaver-targets "read only by Weaver, never on the read-path" vs its P5 app-read reality. Stage one uncommitted edit for Andrew. | ★★ | S | 📋 |
| **step6-batch-internal-consistency-decision** | Contract #3 §3.5 + spine steps 6–7 assert validations the Processor doesn't perform (link-endpoint/aspect-host dangling-reference resolution; §3.4/§3.8 event-type DDL check) — unbuilt and untracked. Decide build-vs-amend per layer (both checks are cheap and fail-closed-aligned); build the chosen ones or stage a narrowing amendment. | ★★ | M | 📋 |
| **natsperm-matrix-hygiene** | Refractor's `$KV.>` write is broader than its lens-target set (covers dynamically-named package buckets — narrowing needs a real design, not a mechanical prune). | ★ | S | 📋 · bridge phantom-bucket half shipped `0377938`; remaining: Refractor narrowing needs design |
| **contract7-7.3-config-example-refresh** | §7.3's bootstrap.json example still lists `processorIdentityKey` + a 5-key `metaMetaDDLKeys` block (same drift §7.2 items 1/7 fixed) — reconcile to the as-built config struct (no processor identity; one self-describing root DDL). Needs a read of the bootstrap config struct first. | ★ | XS | 📋 |
| **fr22-service-denial-structural-fields** | FR22's `DenialDetails` has no service branch — a service-op denial names nothing structural. Fork B: emit `deniedService` (from authContext) + `deniedServiceClass` (one `.class` aspect read at denial time); `availableServiceClasses` is out of scope — what's available is the app's read-model question (P5). Contract #6 §6.12 is the spec. | ★ | S | 📋 · Fork B ratified 2026-07-03 (§6.12 amended) · low-priority |
| **weaver-exhausted-escalation-and-model** | The augur block validates `exhausted` + parses `augur.model` but no engine path fires either — a spent-budget gap is silently skipped (evaluator `continue`, no escalation); this now contradicts §10.8's promised standing Health issue at the suppression site (code raises none) — a live code-vs-frozen-contract divergence — and `model` is consumed by nothing. Wire `exhausted` through augurEscalation + raise the Health issue (threading model), or strike both from the block and the §10.8 sentence. | ★★ | S | 📋 |
| **contract10-async-deadline-reconcile** | Contract #10's async paragraph says the Loom step deadline is per-adapter-sized and backstops a dead bridge, but its own §10.6 + the code disarm that deadline at instanceOp commit (FailPattern is the out-of-band close; the bridge waits unbounded). Stage a reconciling edit; note the single global CallDeadline as deferred-with-real-adapters. | ★ | XS | 📋 |

## Lattice feature backlog — the Phase-3 build queue

The AI-driven flywheel draws from this list (Surveyor files → Designer designs → Steward builds the
ratified). Everything here needs design and is fair game **except** 🚧 Andrew-gated rows. Architectural
**forks** (Gateway, read-path auth, Vault, multi-cell, HA-NATS) and **frozen-contract** changes are
designed-through, but the *fork decision* + the *contract commit* are Andrew's.

> 🎯 **Build-ready now**: **Object crypto-shred Fire 2** (Fire 1 platform seam shipped `93d6f88`;
> Loupe client-side encrypt/decrypt path next) — unblocked, not yet picked up. *Still genuinely
> gated*: **AI-caps Fire 4** (Andrew sign-off on AI-code-execution, not the sandbox).

### Security & trust boundary
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| Gateway | Edge trust boundary: JWT auth, `Lattice-Actor` stamping, read-path enforcement. Gates external actors + the real Edge node. | ★★★ | L | ✅ effectively done · [design](../../implementation-artifacts/gateway-external-trust-boundary-design.md) · Fire 1+2+3 shipped (write path, JWKS, RLS read-front); Fire 4 retired ([re-grounded](../../implementation-artifacts/gateway-claim-flow-identity-provisioning-design.md)); Fire 5 is ops (reverse-proxy), not a Steward fire |
| NATS account-level write restriction | Close the fabricated-KV-write surface at the substrate (account-level); today defended only by overwrite-by-reprojection. | ★★ | M | ✅ effectively done · [design](../../implementation-artifacts/nats-account-write-restriction-design.md) §Fire-3-status · only deferred Fire 4 (prod mTLS) remains |
| Control-plane Capability authorization (FR30) | Both control planes (Weaver/Refractor `…/control`) should be capability-gated, not open responders. | ★★ | M | ✅ CLOSED · [design](../../implementation-artifacts/control-plane-capability-authz-design.md) · Fire 1a+1b+1c+2 all shipped (verified-actor JWT, 3-layer reviewed) |

### Privacy / Vault
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[identity-hygiene] Dedup over encrypted PII (duplicateCandidates)** | Post-Vault, the lens's WHERE matching (email/phone equality, name Levenshtein) runs on per-identity-DEK ciphertext → functionally inert; a secure lens can't fix in-engine matching. Needs a design: blind-index/HMAC companion aspect vs sanctioned engine mechanism. | ★★ | M | 📋 needs-design (Designer) · context in the [vault design](../../implementation-artifacts/vault-crypto-shredding-design.md) Fire 5b-i checkpoint |
| **[Object Store] Crypto-shred for object-store blobs** | Vault covers sensitive **aspects** (Core KV) but not PII-bearing **blobs** (lease PDFs, ID scans, signatures) — extend crypto-shred to the Object Store. | ★★ | M | 🏗️ building · [design](../../implementation-artifacts/object-store-crypto-shred-design.md) §8 checkpoint · Fire 1 (platform seam) shipped `93d6f88`; next: Fire 2 (Loupe client-side encrypt/decrypt) |

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
| Personal / Secure Lens | Refractor projects a per-identity security-filtered subgraph stream; the Interest-Set watchlist; RLS-style link filtering. | ★★ | L | 🏗️ building · [design](../../implementation-artifacts/personal-secure-lens-design.md) · PL.3 (D1 readableAnchors gate) shipped — next: PL.4 Hydration Hook |
| NATS-subject publish-events adapter | A Refractor target adapter publishing projection deltas to `lattice.sync.user.<id>` — required for Personal Lens. | ★★ | S–M | 📐 subsumed → Personal Lens Fire 1 |
| Edge Lattice (full) | The sovereign per-user node: local VAL (SQLite/IndexedDB), local Starlark, offline-first, reconcile-by-revision. | ★★ | XL | ✅ ratified · [design](../../implementation-artifacts/edge-lattice-full-design.md) · 🚧 seq (far) |

### AI-native
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| AI-authored capabilities | A Lattice-aware agent proposes DDL/Starlark/lenses/workflows through human review + deterministic validation + rollback. | ★★–★★★ | L | 🏗️ building · [design](../../implementation-artifacts/ai-authored-capabilities-design.md) · Fire 3 CLOSED; next: Fire 4 (Starlark) 📐 awaiting-Andrew sign-off on AI-code-execution — sandbox builds WITH it, not before · Loupe UI is Stream 3's lane |
| **The Augur** (AI reasoning tier — L3 evaluator) | Weaver's AI-assisted reasoning tier for ambiguous/novel convergence gaps. The marquee AI-native feature. | ★★ | M–L | ✅ Fires 1+2a+2b shipped (loop closes: escalate→review→dispatch) · [design](../../implementation-artifacts/augur-design.md) + [dispatch design](../../implementation-artifacts/augur-dispatch-pickup-design.md) · 🚧 Fire 3 autoApply Andrew-gated; follow-up: mid-flight-kill + drift-invalid e2e (§6 residual) |
| Starlark guards (Loom) | The reserved `{reads, starlark}` guard escape hatch needs a verified-pure sandbox. | ★ | M | ✅ ratified (split) · [design](../../implementation-artifacts/loom-starlark-guards-design.md) · 🚧 Loom-side held (ships with first consumer) |
| **Weaver planner mandate (dispatcher → solver)** | Remediation stops being a static gap→action lookup: deterministic planner (per-gap candidate selection, then goal-regression synthesis over op-declared effects) with contraction/oscillation diagnostics and admission control; shadow mode + per-target cutover; the Augur stays the AI boundary. | ★★★ | XL | 🏗️ building · [design](../../implementation-artifacts/weaver-planner-mandate-design.md) · next: [renewal](../../implementation-artifacts/loftspace-lease-renewal-goal-authored-target-design.md) R3 FE |

### Read-model / projection maturity
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Refractor/deploy] Loupe read-only PG role (`provision-loupe-role`)** | Loupe's shipped F9 seam reads postgres lens targets via `LOUPE_PG_DSN` — needs a SELECT-only role (mirror `provision-loftspace-role`) + an inspector posture over FORCE-RLS tables: BYPASSRLS (recommended) vs wildcard `actor_read_grants` grant. Until then, postgres lens contents render pg-pending. | ★★ | S | 📋 · unblocks loupe F9 full value |
| **[Refractor] Convergence-lens filtering-WHERE activation guard** | Filter-retraction relies on convergence (`violating`) lenses never carrying a filtering WHERE (a retracted row reads to Weaver as entity deletion) — true for every live lens but unenforced at activation. | ★ | XS–S | 📋 review carry-out · [design](../../implementation-artifacts/negative-filter-retraction-projection-design.md) §Fires-1+2-checkpoint |
| **[Refractor] Protected/plain Postgres adapter is unguarded last-writer-wins** | The plain/protected `PostgresAdapter` ignores `projectionSeq` (unconditional LWW) — a stale replay can transiently reorder a security-relevant row. Posture accepted 2026-07-02 (the D1 M3 CDC-lag analog); this row is the follow-up hardening: extend the seq-guard to protected targets. | ★ | S–M | 📋 |
| Elasticsearch target adapter | A third lens target adapter (only NATS-KV + Postgres ship; no consumer yet). | ★ | M | ✅ ratified (2026-07-02, OpenSearch pin + FTS-first interim) · [design](../../implementation-artifacts/search-target-adapter-design.md) · shelf — first consumer (LoftSpace FTS unified search) filed on verticals; the OpenSearch adapter builds only on search-engine-scale demand |
| **[Refractor] Cross-instance projection-latency rollup** | Aggregate per-lens projection latency across Refractor instances into one per-component view (single-instance today, so per-instance == per-component). Link-tombstone re-projection half **subsumed** by the link-aspect reprojection design. | ★ | S | 🚧 seq behind HA-NATS multi-instance · [link-aspect design](../../implementation-artifacts/link-aspect-triggered-reprojection-plain-lenses-design.md) subsumes the tombstone half; no multi-instance consumer yet |

### Refinements & ops
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **CI pipeline speed (continuous)** | Make CI faster without weakening any gate — owned continuously by the **Whetstone**. Matrix split done (serial → 4 parallel jobs); convergence + unit parallelized. | ★★ | M (ongoing) | 🏗️ continuous (Whetstone) · `internal/bridge`'s 46 tests + a fixture race fixed (d2b6321, package 35s→7s) but `unit` job wall-clock unchanged (~137s) — local per-package sums don't predict the `-p4` critical path; next: capture real per-package timing FROM a CI run to find the actual pole |
| **[CI/Refractor] Hello-Lattice NFR-P3 latency flake** | The capability-projection probe fails-then-passes on the shared CI runner — re-scoped to a 1000ms regression guard (Andrew-ratified; reported SLA unchanged), but the runner floor has drifted to ~1.1s. | ★★ | M | ✅ fixed 2026-07-03 (`94c8224`, deadline 1000ms→2000ms) — re-examine if it recurs |
| **Hard-delete mutation verb (true link/aspect keyspace reclaim)** | Mutation vocab is create/update/tombstone (soft PUTs); a tombstoned key persists + is still enumerated by `kv.Links`. A 4th `delete` verb (NATS `DEL`) lets dead links leave the keyspace, bounding `kv.Links` LIST cost. | ★ | M | 🗄️ shelved (Andrew 2026-07-02) · [design + hold banner](../../implementation-artifacts/hard-delete-mutation-verb-design.md) · demand dissolved by clinic write-path slot claims; §3 edits reverted; revive only on a real reclaim driver |
| **Script-read posture — declared+hydrated vs live `kv.get`/`kv.Links`** | Declared+hydrated reads as the write-path norm: `optionalReads` folds read-before-create in; `kv.Links` declared-as-metadata (Edge-gate + best-effort lint, not hydrated); guards become a generic Processor-side operation feature (supersedes Loom's engine read). | ★★ | L | ✅ ratified · [design](../../implementation-artifacts/script-read-posture-design.md) · Fires 1–2 shippable (Contract #2 committed); guard (Fire 3) build + contracts deferred |
| **Package version upgrade / DDL hot-reload (F-004)** | In-place re-install over an existing version + DDL-migration semantics (install/uninstall existed; upgrade did not). Diff-and-apply (create/update/tombstone) in one atomic Processor batch; version-independent entity keys. | ★★ | M | ✅ effectively done · [design](../../implementation-artifacts/package-version-upgrade-design.md) · Fires 1a–3 shipped; only an optional Fire-2 live e2e remains (§8.1 + §8.6 committed) |

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

- 2026-07-06 · `6d2b4c5` · [Weaver] External-gap stale-mark reclaim — prompt fresh-instance retry after a failed call, per Contract #10 §10.3; 3-layer reviewed, fixed forward (vacuous confirmedConcluded signal)
- 2026-07-06 · `945f605` · [Contract #7] §7.2 reconciled to as-built kernel — holdsRole→operator topology (not data.protected), 5→1 meta-meta DDL, no processor identity; §7.7 untouched (arch item 7; +922a294, dfbad3d)
- 2026-07-06 · `9711814` · [Contract #2] §2.6 error-code table reconciled to the wire + §2.9 lenient-parse fix + TestConformance_ErrorCodeTable_MatchesWire pin (arch item 4)
- 2026-07-06 · `81c0c6b` · [Weaver] Planner mandate R2 — LoftSpace lease-renewal package (5 ops, 2 goal-authored targets, e2e); 3-layer reviewed, fixed forward (oscillation-path collision, double-extension guard); R3 FE next
- 2026-07-05 · `11cc15f` · [loom] dispatch authContext.target — carry the real vtx.meta.<NanoID> as Pattern.MetaKey (not the human PatternID), both dispatch sites + pinning test (arch-review item 10)
- 2026-07-05 · `11cc15f` · [repo] debris — 5 CONTRACT-AMENDMENT-REQUEST.md removed, objects-base reclaim comments fixed, objmgr up-full BOOTSTRAP_JSON_PATH, internal/spike README (arch item 11)
- 2026-07-05 · `11cc15f` · [Gateway] up-full deploy — Gateway now started in make up-full (dev-mode :8080), Loupe map node no longer a ghost (arch item 1a; F10)
- 2026-07-05 · `e0737db` · [Weaver] Planner mandate R1 pkgmgr authoring — mode/goal/goalColumns/actions on WeaverTargetSpec/GapActionSpec + install validation; R2 package next
- 2026-07-05 · `c7f3bb5` · [Weaver] Planner mandate R1 — goal-regression dispatch wiring (Synthesize, per-leg pin/release, unplannable→augur); pkgmgr authoring next
- 2026-07-05 · `b99d51c` · [Weaver] Planner mandate Fire 6 Increment 3 — per-gap `actions` catalog parse + install-validation (row-reachable pre/effects, concrete-effects check); R1 dispatch wiring next
- 2026-07-05 · `ade75bd`+`bd90c71` · [Weaver] Planner mandate Fire 8 — admission control (priority-fair token-bucket dispatch pacing, §10.2 `priority` col + `admission` block, opt-in); Fire 9 next
- 2026-07-05 · `33b75e8` · [Refractor] Personal Lens Fire PL.3 — D1 readableAnchors security gate (capabilityread pkg), security-wins-over-relevance; Gate-3 vectors 1-3 e2e, 3-layer reviewed; PL.4 next
- 2026-07-05 · `93d6f88` · [Privacy/Object Store] Crypto-shred Fire 1 — Vault WrapKey/UnwrapKey + AttachObject sensitive/governingIdentity/encryption, identity-salted oid; dormant, 3-layer reviewed; Fire 2 next
- 2026-07-05 · `a2208a6` · [Security] Control-plane capability authz Fire 2 — verified-actor JWT (reuses D1's gateway/auth), nil-verifier = Fire 1 unchanged; item CLOSED, 3-layer reviewed
- 2026-07-05 · `0307450` · [Weaver] Planner mandate Fire 7 — contraction monitor + oscillation detector (heartbeat-surfaced diagnostics, freeze via `__control`); zero dispatch-decision change; Fire 8 next

- 2026-07-05 · `c99770f` · [Security/Gateway] Fire 3 — RLS-enforced read-path front (`GET /v1/<name>`, config-only read-model registry); Gateway CLOSED (Fire 5 is ops), 3-layer reviewed
- 2026-07-05 · `—` · [Privacy/Vault] Vault + crypto-shredding CLOSED — live delivery-boundary reset + e2e (attended session): real ciphertext at rest, real decrypt for the authorized landlord, real shred nulls contact cols row-intact
- 2026-07-05 · `cfc65fe` · [Loupe] Chronicler Fire 3 CLOSED — Flows tab (durable Loom-flow history, P5 read + live/orphaned badge), lead-reviewed
- 2026-07-05 · `1e35cc9` · [Refractor/orchestration-base] Chronicler Fire 2 — `loomFlowHistory` event-sourced lens, first eventStream consumer, 3-layer reviewed
- 2026-07-05 · `a55a9db` · [Refractor] Chronicler Fire 1 — `eventStream` lens-source primitive (dark; no production lens yet), 3-layer reviewed
- 2026-07-05 · `6aade75` · [Loom/Weaver] loom-pattern-source-cold-registry CLOSED — per-boot nonce decouples durable uniqueness from `Instance` (both pattern/target-source)
- 2026-07-05 · `a3f6a23` · [Security] Control-plane capability authz (FR30) Fire 1b — CapabilityKVChecker + control-authz grants, enforcement now default-on behind the shipped NATS trust floor; 3-layer reviewed
- 2026-07-05 · `03976c2` · [Security] Control-plane capability authz (FR30) Fire 1a — Lattice-Actor header threaded through Weaver/Loom/Refractor control + CLI/Loupe, zero enforcement change; 3-layer reviewed
- 2026-07-05 · `f30b80e` · [Refractor/adapter] Fixed pool.Close/DROP TABLE cleanup order in rls_test.go + rls_verify_test.go — `t.Cleanup(pool.Close)` so LIFO runs the drop first, no more leaked tables on rerun
- 2026-07-05 · `109f59a` · [bootstrap] `make up` reuse-branch freshness check — gates reuse on `lattice bootstrap verify`; stale/mismatched JSON forces re-bootstrap instead of silently reading empty
- 2026-07-05 · `fc41c3b` · [Core] UninstallPackage/UpgradePackage per-key OCC (F-011) CLOSED — Fire 2 (upgrade) per-key `expectedRevision` on update/tombstone, mirrors Fire 1
- 2026-07-05 · `a2c86b4` · [Core] Atomic-batch size ceiling CLOSED — typed `BatchTooLarge` pre-flight guard (substrate) + step-8 rejection (Processor), no redelivery
- 2026-07-05 · `744f75d` · [Docs] objmgr-and-bootstrap-component-pages CLOSED — bootstrap/vault/privacyworker component pages + README/architecture-overview updates + survey-rotation slots
- 2026-07-05 · `e67e073` · [FR28/29] Role-queue + fallback Fire 3 CLOSED — `unroutedTasks` Weaver target (new `surface` GapAction, no dispatch, Health-KV only); §10.8 amendment uncommitted for Andrew
- 2026-07-05 · `df742d2` · [AI-native] AI-authored capabilities Fire 3 CLOSED — weaverTarget/loomPattern kinds + Starlark-guard record-time gate, 3-layer reviewed
- 2026-07-05 · `c58ba1c` · [AI-native] AI-authored capabilities Fire 2 CLOSED — `lattice capability list/review` + `lattice-pkg apply-proposal`, 3-layer reviewed

- 2026-07-05 · `99f7255` · [AI-native] AI-authored capabilities Fire 2 fast-follow — grant kind in the materializer (scope check + e2e); Loupe/CLI affordance remains
- 2026-07-05 · `99d3aa9` · [AI-native] AI-authored capabilities Fire 2 — F-004 apply loop + applied flip; lens-kind loop CLOSED; grant kind + Loupe/CLI affordance remain
- 2026-07-05 · `0e8a3b9` · [AI-native] AI-authored capabilities Fire 2 Increment 1 — ReviewCapabilityProposal human-verdict op (approve/reject, fresh-verdict re-validation); F-004 apply + applied flip remains
- 2026-07-05 · `95a743a` · [Refractor/pipeline] Fan-out eval-error disposition + adj-watch edge arms pinned (`dispositionEvalErr` 100%, `handleAdjNode` extracted + covered); item CLOSED
- 2026-07-05 · `fc4094e` · [AI-native] AI-authored capabilities — capabilityProposals + capabilityAuthorContext P5 read models; manifest drift fix + drift test; ReviewCapabilityProposal/apply remains
- *(older entries rolled to [archive/lattice-done.md](archive/lattice-done.md))*
