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
| **[Loom] Redelivery/deadline-recovery edge branches uncovered** | `engine.go:resumeStepZero` (41.7% — redelivered trigger whose `createInstance` batch committed but step 0 never submitted, incl. the pattern-pin-missing→fail branch) + `state.go:disarmDeadline` (33.3% — KVGet/KVDelete error arms + the already-disarmed no-op that breaks the deadline-watcher re-entry loop) sit untested by any direct unit test. | ★ | XS–S | 📋 · `internal/loom/engine.go:460`, `internal/loom/state.go:451` |
| **[Refractor] NatsKVAdapter guardedWrite CAS-contention edge branches uncovered** | `guardedWrite`'s revision-conflict retry loop + CAS-exhaustion path (53.8%) — untested; not racelessly triggerable without a store-injection seam (residual, like `capabilityread-error-arm-tests`'s Get-failure arm). | ★ | XS–S | 📋 · `internal/refractor/adapter/natskv.go:192` |
| **[Weaver] `inflight_<g>`-as-external-gap-marker is unenforced** | The stale-mark reclaim relies on `inflight_<g>` only ever being lens-authored for a real outcome-driven external gap; true today but not install-time enforced. | ★ | S | 📋 · `internal/weaver/evaluator.go` (`staleMark`) |
| **[Processor] `stub-auth-active` health alert has no clear/TTL path** | `EmitAlert` writes via plain `KVPut` (no TTL, unlike `RecordCommitConflict`/`RecordClaimAttempt`'s `KVPutWithTTL`) — self-heals only by fresh re-emission. Mains now refuse a stub-mode start, so it can never re-fire — one historical event stays "warning" forever until the bucket is wiped. Needs a Designer call on reliably setting+clearing a "currently happening" signal. | ★ | S | 📋 needs-design (Designer) · `internal/processor/health_alerts.go:214` |

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
| **natsperm-matrix-hygiene** | Refractor's `$KV.>` write is broader than its lens-target set (covers dynamically-named package buckets — narrowing needs a real design, not a mechanical prune). | ★ | S | 📋 · bridge phantom-bucket half shipped `0377938`; remaining: Refractor narrowing needs design |

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
| **weaver-untested-arms** | Five untested failure arms (none security-critical): `seedDisabledTargets` list-keys error → Start abort; disable/enable fail-safe ordering + silent Pause/Resume bool discards; `releaseCompletedLeg` revision-conflict skip; `freezeOscillatingPair` Disable-failure leg. Add colocated tests. | ★ | S | 📋 |

## Lattice feature backlog — the Phase-3 build queue

The AI-driven flywheel draws from this list (Surveyor files → Designer designs → Steward builds the
ratified). Everything here needs design and is fair game **except** 🚧 Andrew-gated rows. Architectural
**forks** (Gateway, read-path auth, Vault, multi-cell, HA-NATS) and **frozen-contract** changes are
designed-through, but the *fork decision* + the *contract commit* are Andrew's.

> 🎯 **Build-ready now** (this section only — check the **Arch-review intake** section above too, it
> carries its own ✅ ratified / 📋 ready items): **Read-posture debt sweep → flip** (§13 worklist —
> Refinements & ops) — platform half (21) 🏗️ done, [verticals half](verticals.md) in-flight; the flip
> ships once both land, un-blocking **Edge Lattice** (EDGE.1 next).
> *Still gated*: **AI-caps Fire 4** (Andrew sign-off on AI-code-execution, not the sandbox).
> Whoever ships the named pick updates this callout to the next one — a stale callout starves the lane.

### Security & trust boundary
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| NATS account-level write restriction | Close the fabricated-KV-write surface at the substrate (account-level); today defended only by overwrite-by-reprojection. | ★★ | M | ✅ effectively done · [design](../../implementation-artifacts/nats-account-write-restriction-design.md) §Fire-3-status · only deferred Fire 4 (prod mTLS) remains |

### Privacy / Vault
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[identity-hygiene] Dedup over encrypted PII (duplicateCandidates)** | Post-Vault, the lens's WHERE matching (email/phone equality, name Levenshtein) runs on per-identity-DEK ciphertext → functionally inert; a secure lens can't fix in-engine matching. Needs a design: blind-index/HMAC companion aspect vs sanctioned engine mechanism. | ★★ | M | 📋 needs-design (Designer) · context in the [vault design](../../implementation-artifacts/vault-crypto-shredding-design.md) Fire 5b-i checkpoint |

### External-I/O maturity (bridge follow-ons)
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| Real adapters + async result-return | Replace the `Fake*` adapters with real vendors + design the async result path. | ★★ | M–L | ✅ async result-return done · real adapters deferred (prod) |
| Adapter read-seam / richer params | Adapters can only use what the target-lens row projects; add a subject-templated fetch seam for extra fields (SSN/DOB). | ★★ | S–M | 🚧 blocked-on: Designer (Starlark sensitivity-detection primitive) · [design](../../implementation-artifacts/adapter-read-seam-subject-templated-params-design.md) §grounding-finding · F1 shipped, F2 unsafe as speced (all identity PII is now Vault-sensitive) |

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
| Edge Lattice (full) | The sovereign per-user node: local VAL (SQLite/IndexedDB), local Starlark, offline-first, reconcile-by-revision. EDGE.1+2 (trusted-posture offline loop; PL.1/2 shipped) build first, EDGE.3–5 per the §7 gates. | ★★★ | XL | ✅ ratified · [design §7](../../implementation-artifacts/edge-lattice-full-design.md) · 🚧 seq: read-posture sweep+flip (Andrew 2026-07-09), then EDGE.1 |

### AI-native
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| AI-authored capabilities | A Lattice-aware agent proposes DDL/Starlark/lenses/workflows through human review + deterministic validation + rollback. | ★★–★★★ | L | 🏗️ building · [design](../../implementation-artifacts/ai-authored-capabilities-design.md) · Fire 3 CLOSED; next: Fire 4 (Starlark) 📐 awaiting-Andrew sign-off on AI-code-execution — sandbox builds WITH it, not before · Loupe UI is Stream 3's lane |
| **The Augur** (AI reasoning tier — L3 evaluator) | Weaver's AI-assisted reasoning tier for ambiguous/novel convergence gaps. The marquee AI-native feature. | ★★ | M–L | ✅ Fires 1+2a+2b shipped incl. §6 residual e2e (loop closes: escalate→review→dispatch) · [design](../../implementation-artifacts/augur-design.md) + [dispatch design](../../implementation-artifacts/augur-dispatch-pickup-design.md) · 🚧 Fire 3 autoApply Andrew-gated |
| Starlark guards (Loom) | The reserved `{reads, starlark}` guard escape hatch needs a verified-pure sandbox. | ★ | M | ✅ ratified (split) · [design](../../implementation-artifacts/loom-starlark-guards-design.md) · 🚧 Loom-side held (ships with first consumer) |
| **Weaver planner mandate (dispatcher → solver)** | Remediation stops being a static gap→action lookup: deterministic planner (per-gap candidate selection, then goal-regression synthesis over op-declared effects) with contraction/oscillation diagnostics and admission control; shadow mode + per-target cutover; the Augur stays the AI boundary. | ★★★ | XL | ✅ effectively done · [design](../../implementation-artifacts/weaver-planner-mandate-design.md) · Fires 1-9(Inc1)+R1-R3 shipped, consumed by LoftSpace renewals; Fire 9 AI tail deferred - needs a novel Augur gap, not renewals |

### Read-model / projection maturity
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Refractor] Convergence-lens filtering-WHERE activation guard** | Filter-retraction relies on convergence (`violating`) lenses never carrying a filtering WHERE (a retracted row reads to Weaver as entity deletion) — true for every live lens but unenforced at activation. | ★ | XS–S | 📋 review carry-out · [design](../../implementation-artifacts/negative-filter-retraction-projection-design.md) §Fires-1+2-checkpoint |
| Elasticsearch target adapter | A third lens target adapter (only NATS-KV + Postgres ship; no consumer yet). | ★ | M | ✅ ratified (2026-07-02, OpenSearch pin + FTS-first interim) · [design](../../implementation-artifacts/search-target-adapter-design.md) · shelf — first consumer (LoftSpace FTS unified search) filed on verticals; the OpenSearch adapter builds only on search-engine-scale demand |
| **[Refractor] Cross-instance projection-latency rollup** | Aggregate per-lens projection latency across Refractor instances into one per-component view (single-instance today, so per-instance == per-component). Link-tombstone re-projection half **subsumed** by the link-aspect reprojection design. | ★ | S | 🚧 seq behind HA-NATS multi-instance · [link-aspect design](../../implementation-artifacts/link-aspect-triggered-reprojection-plain-lenses-design.md) subsumes the tombstone half; no multi-instance consumer yet |

### Refinements & ops
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **CI pipeline speed (continuous)** | Make CI faster without weakening any gate — owned continuously by the **Whetstone**. Matrix split done (serial → 4 parallel jobs); convergence + unit parallelized. | ★★ | M (ongoing) | 🏗️ continuous (Whetstone) · `internal/bridge`'s 46 tests + a fixture race fixed (d2b6321, package 35s→7s) but `unit` job wall-clock unchanged (~137s) — local per-package sums don't predict the `-p4` critical path; next: capture real per-package timing FROM a CI run to find the actual pole |
| **Hard-delete mutation verb (true link/aspect keyspace reclaim)** | Mutation vocab is create/update/tombstone (soft PUTs); a tombstoned key persists + is still enumerated by `kv.Links`. A 4th `delete` verb (NATS `DEL`) lets dead links leave the keyspace, bounding `kv.Links` LIST cost. | ★ | M | 🗄️ shelved (Andrew 2026-07-02) · [design + hold banner](../../implementation-artifacts/hard-delete-mutation-verb-design.md) · demand dissolved by clinic write-path slot claims; §3 edits reverted; revive only on a real reclaim driver |
| **Script-read posture — declared+hydrated vs live `kv.get`/`kv.Links`** | Declared+hydrated reads as the write-path norm: `optionalReads` folds read-before-create in; `kv.Links` declared-as-metadata (Edge-gate + best-effort lint, not hydrated); guards become a generic Processor-side operation feature (supersedes Loom's engine read). | ★★ | L | ✅ Fires 1–2 shipped · [design §12](../../implementation-artifacts/script-read-posture-design.md) · Fire 3 (guards) deferred to its first consumer; debt sweep + warn→block flip = its own row below |
| **Read-posture debt sweep (platform packages) → flip lint to blocking** | 21 class-(b) lazy `kv.Read` sites in `packages/` — declare (a/d) or annotate (c/e) incl. dispatcher envelopes; flip advisory→blocking after the [verticals half](verticals.md) too — stops accrual, feeds Edge's declared-reads ⊆ mirror gate. | ★★★ | S–M | 🏗️ platform 21/21 done · [design §13](../../implementation-artifacts/script-read-posture-design.md) · next: flip once verticals lands |

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

- 2026-07-10 · `7372765` · [Weaver] augur-dispatch-§6-residual — mid-flight-kill + scope-escape-invalid e2e for Fire 2b's proposedOp dispatch; CI green
- 2026-07-10 · `eb7243c` · [Weaver] weaver-admission-pkgmgr-authoring — `WeaverTargetSpec.Admission` authoring path + install-time validation; lease-signing paces backgroundCheck/stripe; CI green
- 2026-07-10 · `710f1f0` · [Weaver/Bridge/Gateway/Loom/objmgr] health-issue-since-field — stamp+persist Contract #5 §5.5 `since` on every health issue, platform-wide; CI green
- 2026-07-09 · [Contract #6] §6.13 invalidation-amendment ratified — reconciled to retire-simple-engine's unconditional broad-BFS + auth-plane-guard reality (`6e0e205`); no narrow Invalidation member / construct gate
- 2026-07-09 · `e35cc38` · [Contracts] §6.14 protected-Delete+M5 wildcard, §7.3 bootstrap.json example, §10 task-revive text ratified — reconciliation to shipped code (94087bd/128111f); no code change
- 2026-07-09 · [F-004] Package version upgrade / DDL hot-reload CLOSED — Fires 1a-3 shipped; optional Fire-2 live-e2e deferred — [design](../../implementation-artifacts/package-version-upgrade-design.md)
- 2026-07-09 · `e97305f` · [scripts] verify-package-clinic-domain — fixed a nondeterministic map-overwrite hiding CreateAppointment's 2nd (self/consumer) permission vertex; CI green
- 2026-07-09 · `ba28bc7` · [Refractor] refractor-health-contract-minors — heartbeat version/status aligned to Contract #5; pendingSpecs ordering test added; CI green
- 2026-07-09 · `7f1e5d1` · [Processor] fr22-service-denial-structural-fields — deniedService/deniedServiceClass on single-service AuthContextMismatch; CI green
- 2026-07-09 · `513587d` · [Object Store] crypto-shred Fire 4 Increment 2 CLOSED — loftspace-app idDocument/proofOfIncome sensitive upload+decrypt; fixed a real pre-existing governingIdentity-persistence bug; 3-layer reviewed, CI green
- 2026-07-09 · `172fa98` · [Object Store] crypto-shred Fire 4 Increment 1 — `internal/objectcrypto` extracted from Loupe; privacy-base `piiKeyEnvelope` lens; `lattice.vault.wrapkey`/`unwrapkey` extended to loftspace-app; CI green
- 2026-07-09 · `659c635` · [Weaver] directOp-class-pin — `GapActionSpec.Class` threads to `opEnvelope.Class`; pinned on Café/bespoke-contracts ledger dispatches; unblocks [Café tab-settlement](verticals.md); CI green
- 2026-07-09 · `128111f` · [orchestration-base] CreateTask logical-delete create-wedge — present-but-isDeleted task revives via CAS-guarded update, not a blind create; §10 revive text ratified; CI green
- 2026-07-09 · `20abd1e` · [auth] scoped-privileged-lane-grants Fire 3 CLOSED — consoleOperator gains the allowlisted pkg-lifecycle trio at meta; requireRootAdmin retired
- 2026-07-09 · `f644399` · [Refractor] natskv-guard-edge-branches (storedProjectionSeq half) — fixed negative-seq uint64 wrap, removed dead json.Number branch, covered malformed/absent/negative/non-numeric watermarks; CI green
- 2026-07-09 · `0982345` · [auth] scoped-privileged-lane-grants Fire 2 — core `{op,lane}` allowlist + fail-closed strip-to-default + `PrivilegedLaneGrantRejected` alert; CI green
- 2026-07-09 · `16c3993` · [test] CI fix — sync a second Postgres fixture's `read_lease_applications` column list (objects_rls_test.go), missed in the same-day fire; CI green
- 2026-07-09 · `3a9d140` · [Security] #75 Fire 2b CLOSED — lease-doc gen is external I/O (docGen triad+bridge+Weaver auto-attach); loftspace-app `ops.>` stripped — [design](../../implementation-artifacts/lease-doc-external-io-design.md)
- 2026-07-08 · `ed90925` · [loftspace-app] #75 Fire 2b increment 2 — Attach/DetachObject browser-direct via the Gateway; dedup requestId ported to JS (verified vs Go); live-verified
- 2026-07-08 · `f8dec9c` · [Security] #75 Fire 2b increment 1 — clinic/cafe apps' core-operations publish stripped + dead /api/op deleted; natsperm-pinned (`TestVerticalAppOpsPublishDenied`), CI green
- 2026-07-08 · `9116052` · [Auth] capability is the only operational auth mode — stub retired as a deployable posture, Makefile default flipped
- 2026-07-08 · `050e5ac` · [Auth] unconditional class-aware platform routing — dropped the rbac boot-probe latch that stale-latched "rbac absent" for a component's whole life
- 2026-07-08 · `3e4930d` · [hello-lattice] grant CreateBook to operator before Milestone 3 submits it — capability-mode fix
- 2026-07-08 · `56784ac` · [docs] lens-hotreload-doc-fix CLOSED — "new lens needs Refractor restart" was false; corrected across 11 files incl. steward/fe-engineer SKILL.md + Loupe UI; CI green
- 2026-07-07 · `56911ac` · [Refractor/deploy] loupe-read-only-pg-role CLOSED — wildcard-grant posture per Andrew's standing M5 decision (not a bypass, row cited the wrong doc); verified live, CI green

- 2026-07-07 · `af86835` · [Weaver] weaver-ctrl-publish-grant-trim — dropped the redundant `lattice.ctrl.weaver.>` publish grant (subscribe + `allow_responses` already cover the control responder); natsperm-verified
- 2026-07-07 · `94087bd` · [Refractor] refractor-6-14-postgres-seam-truthup — seq-guarded protected-Delete tombstone, grant-table auth-plane severity, int64 wrap fix; lead-reviewed, CI green
- 2026-07-07 · `8b481a1` · [Refractor] refractor-failure-tier-backhalf — wired `SetRetryQueue`(deferred retry/DLQ) + `SetAuditWriter` (per-rule audit trail) in `cmd/refractor`, both previously dormant; lead-reviewed, CI green
- 2026-07-07 · `e189e74` · [Gateway] jwks health block — per-kid provenance (source/alg/addedAt) + poller lastPoll/swaps counters, mirroring the revocation block; lead-reviewed, CI green
- 2026-07-07 · `5bee182` · [pkgmgr] console-operator role package — mechanism B part 1 (scoped `consoleOperator` role + default-lane/ctrl.* grants, no privileged lane); lead-reviewed, CI green
- 2026-07-07 · `8846771` · [loftspace-app/clinic-app] real-actor-write-auth-e2e Phase 2 Inc 2 — credential-bindings read-boundary wiring, both vertical apps; lead-reviewed (mirrors already 3-layer-reviewed Gateway resolver), CI green
- 2026-07-07 · `0a73c3c` · [Weaver] arch-review fixes — control fail-closed default (3 planes), validator-mirror parity + heartbeat honesty, cross-package targetId install-check, materializer step-recursion, comment/natsperm hygiene; CI green
- *(older entries rolled to [archive/lattice-done.md](archive/lattice-done.md); includes `94c8224` hello-lattice NFR-P3 flake fix)*
