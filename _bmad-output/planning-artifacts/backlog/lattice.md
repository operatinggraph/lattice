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
| **[Weaver] `inflight_<g>`-as-external-gap-marker is unenforced** | The stale-mark reclaim relies on `inflight_<g>` only ever being lens-authored for a real outcome-driven external gap; true today but not install-time enforced. | ★ | S | 📋 needs-design (Designer) · Weaver has no lens-schema visibility at install time to validate against (checked 2026-07-10) — a mechanical validator isn't possible as scoped, needs a real cross-component mechanism |

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

## Lattice feature backlog — the Phase-3 build queue

The AI-driven flywheel draws from this list (Surveyor files → Designer designs → Steward builds the
ratified). Everything here needs design and is fair game **except** 🚧 Andrew-gated rows. Architectural
**forks** (Gateway, read-path auth, Vault, multi-cell, HA-NATS) and **frozen-contract** changes are
designed-through, but the *fork decision* + the *contract commit* are Andrew's.

> 🎯 **Build-ready now** (this section only — check the **Arch-review intake** section above too, it
> carries its own ✅ ratified / 📋 ready items): **Edge Lattice EDGE.1 + EDGE.2 CLOSED** (2026-07-10) —
> the offline-first read loop AND the optimistic write path (`internal/edge/{store,sync,overlay,agent}` +
> `cmd/edge`) are done. **Edge's own queue is now gated**: EDGE.3 (untrusted multi-identity) needs D1
> (Personal Lens PL.3) + the Gateway write-path translator + NATS-account subscribe-ACL — not build-ready
> yet (edge design §7 checkpoint). No other ratified-design item is queued behind it — the next fire
> should re-derive the top importance×readiness pick from this board (component maintenance table +
> Arch-review intake) rather than assume a named Edge increment.
> *Still gated*: **AI-caps Fire 4** (Andrew sign-off on AI-code-execution, not the sandbox).
> Whoever ships the named pick updates this callout to the next one — a stale callout starves the lane.

### Security & trust boundary
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| NATS account-level write restriction | Close the fabricated-KV-write surface at the substrate (account-level); today defended only by overwrite-by-reprojection. | ★★ | M | ✅ effectively done · [design](../../implementation-artifacts/nats-account-write-restriction-design.md) §Fire-3-status · only deferred Fire 4 (prod mTLS) remains |
| **Multi-credential identity linking + merge credential-awareness** | One human, N IdPs: no path binds a 2nd credential to a claimed U (claim is one-shot); MergeIdentity never repoints credentialindex/bindings and the materializers fold `identity.claimed` only → a merge strands A on the merged-loser. Link flow + merge rebind + provision-time probe. | ★★ | M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/multi-credential-identity-linking-design.md) · #11 §11.4 edit staged uncommitted |
| **Per-identity NATS subscribe-ACL (Edge sync plane)** | Untrusted Edge connections may subscribe ONLY their own per-identity SYNC subject (`subjects.PersonalSync`), and revocation must cut subscribe. #75 v1 explicitly declined subscribe lockdown (§3.2); PL Fork 3 assumed #75 delivers it — un-owned gap, now filed. Needs dynamic per-identity NATS authN (auth-callout vs operator-JWT fork), consuming Contract #11 tokens. | ★★ | M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/per-identity-nats-subscribe-acl-design.md) |

### Privacy / Vault
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[identity-hygiene] Dedup over encrypted PII (duplicateCandidates)** | Post-Vault, the lens's WHERE matching (email/phone equality, name Levenshtein) runs on per-identity-DEK ciphertext → functionally inert; a secure lens can't fix in-engine matching. Needs a design: blind-index/HMAC companion aspect vs sanctioned engine mechanism. | ★★ | M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/dedup-over-encrypted-pii-design.md) |

### External-I/O maturity (bridge follow-ons)
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| Real adapters + async result-return | Replace the `Fake*` adapters with real vendors + design the async result path. | ★★ | M–L | ✅ async result-return done · real adapters deferred (prod) |
| Adapter read-seam / richer params | Adapters can only use what the target-lens row projects; add a subject-templated fetch seam for extra fields (SSN/DOB). | ★★ | S–M | 📐 awaiting-Andrew · [sensitive-param-egress design](../../implementation-artifacts/sensitive-param-egress-design.md) (unblocks F2–3; #2/#3/#10-loom edits staged uncommitted) · F1 shipped |

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
| Edge Lattice (full) | The sovereign per-user node: local VAL (SQLite/IndexedDB), local Starlark, offline-first, reconcile-by-revision. EDGE.1+2 (trusted-posture offline loop; PL.1/2 shipped) build first, EDGE.3–5 per the §7 gates. | ★★★ | XL | 🏗️ building · [design §7 checkpoint](../../implementation-artifacts/edge-lattice-full-design.md) · EDGE.1+2 done · 🚧 next EDGE.3 blocked-on: per-identity subscribe-ACL (its D1/PL.3 + Gateway legs closed; gate re-verified 2026-07-10) |

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
| Elasticsearch target adapter | A third lens target adapter (only NATS-KV + Postgres ship; no consumer yet). | ★ | M | ✅ ratified (2026-07-02, OpenSearch pin + FTS-first interim) · [design](../../implementation-artifacts/search-target-adapter-design.md) · shelf — first consumer (LoftSpace FTS unified search) filed on verticals; the OpenSearch adapter builds only on search-engine-scale demand |
| **[Refractor] Cross-instance projection-latency rollup** | Aggregate per-lens projection latency across Refractor instances into one per-component view (single-instance today, so per-instance == per-component). Link-tombstone re-projection half **subsumed** by the link-aspect reprojection design. | ★ | S | 🚧 seq behind HA-NATS multi-instance · [link-aspect design](../../implementation-artifacts/link-aspect-triggered-reprojection-plain-lenses-design.md) subsumes the tombstone half; no multi-instance consumer yet |

### Refinements & ops
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **CI pipeline speed (continuous)** | Make CI faster without weakening any gate — owned continuously by the **Whetstone**. Matrix split done (serial → 4 parallel jobs); convergence + unit parallelized. | ★★ | M (ongoing) | 🏗️ continuous (Whetstone) · next: reorder `unit`'s go test package args within one `-p 4` pool (worker-split tried, no win, see Done log) |
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

- 2026-07-10 · `e2a6a27` · [Processor] stub-auth-active-alert-ttl — `EmitAlert` joins Category B's `diagnosticTTL` (re-armed each write); self-clears instead of staying "warning" forever; CI green
- 2026-07-10 · `c0875e6` · [Core] substrate-untested-arms — Wrap adapter (incl. closed-conn), IsConnectionError/IsInvalidKeyError classifiers, DocumentEnvelope.Update, KVPutWithTTL, KVDeleteRevision; cov 75.6%→77.5%; CI green
- 2026-07-10 · `20b8707` · [Bootstrap] bootstrap-untested-arms — two-phase-commit recovery/error paths (nanoid.go) + toMap map/struct/marshal-error/non-object-error branches (envelope.go, no prior test file); cov 61.9%→65.5%; CI green
- 2026-07-10 · `d6161aa` · [Refractor] natskv-guard-edge-branches (guardedWrite half) — kvStore seam + scripted-fake tests cover CAS retry (Create+Update) and exhaustion; CI green
- 2026-07-10 · `9812231` · [Security] Contract #11 external actor authN — per-kid opaque/nanoid subject binding + IdP-provenance `.idpBinding` aspect; CI green
- 2026-07-10 · `61859e0` · [Refractor] convergence-lens-where-guard — `ValidateNoFilteringWhereForConvergence` activation-time guard; exempts actorAggregate lenses (unroutedTasks precedent); CI green
- 2026-07-10 · `0fd7f3f` · [CI] unit-job worker-packing experiment — natsperm/lease-signing isolated onto a dedicated worker, measured no win vs. noisy baseline, reverted (c2f25bb → 0fd7f3f); CI green
- 2026-07-10 · `63aab49` · [scripts] read-posture-debt-sweep-flip — §13 sequencing item 3, advisory→blocking (STRICT CI fails, 0 issues repo-wide); unblocks Edge Lattice EDGE.1
- 2026-07-10 · `495476b` · [Loom] loom-untested-arms — resumeStepZero pattern-pin-missing branch + disarmDeadline re-entry/error arms covered; CI green
- 2026-07-10 · `0103725` · [Weaver] weaver-untested-arms — 4/5 untested failure arms colocated-tested (control.go + evaluator.go); CI green
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
- *(older entries rolled to [archive/lattice-done.md](archive/lattice-done.md); includes `94c8224` hello-lattice NFR-P3 flake fix)*
