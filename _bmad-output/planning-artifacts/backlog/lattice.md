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
- 2026-07-06 Arch-review — Refractor deferred re-review filed ([report](../../../docs/reviews/arch-review-2026-07-06.md)): verdict drifted; 9 rows filed (chronicler-host ★★★, publish-acl ★★★, protected-by-default ★★★); doc/marker truth-up done.
- **Next:** Core.

## Arch-review intake — platform hardening & doc/contract truth

Open corrections from the [2026-07-02 full-platform review](../../../docs/reviews/arch-review-2026-07-02.md)
— per-finding `file:line` evidence and per-component verdicts live there; the What-cells here are abridged.
Refractor's deferred re-review is now filed as its own subsection below (2026-07-06).
Severity-ordered; same row discipline as component maintenance (shipped rows collapse to the Done log).

| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Gateway] JWKS heartbeat block (Loupe F11 enabler)** | Add a `jwks` block `{keys:[{kid,source,alg,addedAt}],lastPoll,swaps}` to `health.gateway.<instance>`, mirroring the shipped `revocation` block — needs per-kid provenance (source/alg/addedAt) threaded through the auth core (the Verifier stores a bare kid→key map; ParseJWKS drops alg) + swap/lastPoll counters on the poller. | ★★ | S | 📋 · unblocks loupe F11 (JWKS panel); up-full-deploy half shipped (F10 node no longer a ghost) |
| **gateway-claim-flow-authz-contradiction** | Claim ops must be reachable pre-auth, but identity-domain role-gates both (`CreateUnclaimedIdentity` → staff, `ClaimIdentity` → `consumer` self) and a fresh actor holds no role → chicken-and-egg. Fix: `ProvisionConsumerIdentity` (Gateway auto-provisions a consumer on first touch, narrow `identityProvisioner` role). | ★★ | M | ✅ mechanism shipped (`7326774`) · walk-in binding (Phase 2) remains under real-actor-write-auth-e2e |
| **real-actor-write-auth-e2e** | Prove scoped capability write-auth end-to-end: apps submit as real role-scoped users through the Gateway (not `bootstrap` root) via a shared dev Fake IdP, under `up-full-capability`, with a genuine allow + deny. Retires the stub as a load-bearing crutch (app tier; system-actor Fire 2 did the engine tier). Browser-direct topology. | ★★★ | L | ✅ Phase 1 (Lattice) done · [design](../../implementation-artifacts/real-actor-write-auth-e2e-design.md) · items 1-4+6 shipped (`921fda4`); item 5 (browser-direct FE) unblocked on Verticals |
| **[auth] scoped privileged-lane grants (retire all-or-nothing operator-root)** | `holdsRole→operator` is class-blind full root — no middle tier; a Loupe operator can't run pkg-install without being kernel root; boot-snapshot staleness. Fix (C1): per-op lanes in `cap.roles` + a core allowlist → a `consoleOperator` runs meta-lane pkg-lifecycle without root, no snapshot. | ★★ | M | ✅ ratified (Andrew 2026-07-06, C1) · [design](../../implementation-artifacts/scoped-privileged-lane-grants-design.md) · build after B; §6.4 edit specified |
| **contract-10-weaver-text-reconciliation** | Contract #10 Weaver drift — 2 of 5 spots remain (reserved-key, anti-storm cross-ref, revision-history reconciled by the planner-mandate ratify + the shard): the augur block still specs `pattern`+triggerLoom while the engine takes op/adapter/replyOp+directOp, so a package author's field is silently dropped; and §10.2 still calls weaver-targets "read only by Weaver, never on the read-path" vs its P5 app-read reality. Stage one uncommitted edit for Andrew. | ★★ | S | 📋 |
| **natsperm-matrix-hygiene** | Refractor's `$KV.>` write is broader than its lens-target set (covers dynamically-named package buckets — narrowing needs a real design, not a mechanical prune). | ★ | S | 📋 · bridge phantom-bucket half shipped `0377938`; remaining: Refractor narrowing needs design |
| **contract7-7.3-config-example-refresh** | §7.3's bootstrap.json example still lists `processorIdentityKey` + a 5-key `metaMetaDDLKeys` block (same drift §7.2 items 1/7 fixed) — reconcile to the as-built config struct (no processor identity; one self-describing root DDL). Needs a read of the bootstrap config struct first. | ★ | XS | 📋 |
| **fr22-service-denial-structural-fields** | FR22's `DenialDetails` has no service branch — a service-op denial names nothing structural. Fork B: emit `deniedService` (from authContext) + `deniedServiceClass` (one `.class` aspect read at denial time); `availableServiceClasses` is out of scope — what's available is the app's read-model question (P5). Contract #6 §6.12 is the spec. | ★ | S | 📋 · Fork B ratified 2026-07-03 (§6.12 amended) · low-priority |

### Refractor re-review (2026-07-06)

The deferred post-update re-review the 2026-07-02 pass held back — verdict **drifted**; full evidence in
[arch-review-2026-07-06.md](../../../docs/reviews/arch-review-2026-07-06.md). The docs-refresh, vendors-row,
and stale-marker corrections were applied in the filing commit (Done log); these are the open builds.

| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **refractor-6-14-postgres-seam-truthup** | Close the remaining §6.14 seams: seq-guard the protected `Delete` (stale-replay resurrection window); stage the M5 wildcard-anchor contract edit the shipped RLS policy already enforces (reconcile the `rls.go`/`capabilityread.go` §6.14 citations with it); decide auth-plane vs warning severity for a paused grant/protected lens; fix the `int64(MaxUint64)` wrap in the shred→grant-table seq stamp. Supersedes the protected-Postgres-LWW row. | ★★ | S | 📋 |
| **refractor-failure-tier-backhalf** | `cmd/refractor` never wires `SetRetryQueue`/`SetAuditWriter`: no deferred retry, no DLQ routing, no audit emission. Wire the shipped libraries, or ratify the Nak-only posture and rewrite the failure-tier Route column. | ★★ | S | 📋 |
| **lens-target-reserved-bucket-guard** | `pkgmgr` denies only the `"capability"` alias; Refractor auto-creates any bucket a lens names and rebuild `Truncate` purges it — a mis-authored lens can wipe `health-kv`/`refractor-adjacency`; ACL-less dev runs have no backstop. Add a reserved-bucket denylist in `pkgmgr` + a fail-closed mirror at Refractor activation. | ★★ | S | 📋 |
| **section-6-13-invalidation-amendment** | §6.13's frozen text specifies an `Invalidation` plan member + fails-activation rule that retire-simple-engine deliberately deleted (code: broad-BFS enumerator, warn-and-proceed). Stage the in-place contract edit reconciling §6.13 to the as-ratified reality, uncommitted for Andrew. | ★★ | S | 🔭 flag-for-Andrew |
| **capabilityread-error-arm-tests** | Pin the D1 gate's fail-closed *error* posture: `(false, error)` on KV Get failure, malformed slice JSON, and list-keys failure is unpinned and free to rot. | ★★ | S | 📋 |
| **refractor-health-contract-minors** | Align the heartbeat `version` (`"0.1.0"`→`"1.0"`) and status (`"shutdown"`→`shuttingDown`) to Contract #5 (Processor already conforms; update the observability schema doc); add a `pendingSpecs` spec-before-parent ordering test. | ★ | S | 📋 |

## Lattice feature backlog — the Phase-3 build queue

The AI-driven flywheel draws from this list (Surveyor files → Designer designs → Steward builds the
ratified). Everything here needs design and is fair game **except** 🚧 Andrew-gated rows. Architectural
**forks** (Gateway, read-path auth, Vault, multi-cell, HA-NATS) and **frozen-contract** changes are
designed-through, but the *fork decision* + the *contract commit* are Andrew's.

> 🎯 **Build-ready now** (this section only — check the **Arch-review intake** section above too, it
> carries its own ✅ ratified / 📋 ready items, e.g. `chronicler-host-reconciliation` ★★★): nothing in
> *this* section is fully unblocked. *Genuinely gated*: **Object crypto-shred Fire 4** (Fires 1+2+3
> shipped `93d6f88`/`6169671`/`5e83939`) — grounding surfaced a real trust-boundary fork, flagged for
> Andrew (🔭 below); **AI-caps Fire 4** (Andrew sign-off on AI-code-execution, not the sandbox).

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
| **[Object Store] Crypto-shred for object-store blobs** | Vault covers sensitive **aspects** (Core KV) but not PII-bearing **blobs** (lease PDFs, ID scans, signatures) — extend crypto-shred to the Object Store. | ★★ | M | 🔭 flag-for-Andrew · [design](../../implementation-artifacts/object-store-crypto-shred-design.md) §8 finding · Fire 4 needs loftspace-app granted `lattice.vault.wrapkey/unwrapkey` (trust-boundary widen, Andrew's call) |

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
| NATS-subject publish-events adapter | A Refractor target adapter publishing projection deltas to `lattice.sync.user.<id>` — required for Personal Lens. | ★★ | S–M | 📐 subsumed → Personal Lens Fire 1 |
| Edge Lattice (full) | The sovereign per-user node: local VAL (SQLite/IndexedDB), local Starlark, offline-first, reconcile-by-revision. | ★★ | XL | ✅ ratified · [design](../../implementation-artifacts/edge-lattice-full-design.md) · 🚧 seq (far) |

### AI-native
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| AI-authored capabilities | A Lattice-aware agent proposes DDL/Starlark/lenses/workflows through human review + deterministic validation + rollback. | ★★–★★★ | L | 🏗️ building · [design](../../implementation-artifacts/ai-authored-capabilities-design.md) · Fire 3 CLOSED; next: Fire 4 (Starlark) 📐 awaiting-Andrew sign-off on AI-code-execution — sandbox builds WITH it, not before · Loupe UI is Stream 3's lane |
| **The Augur** (AI reasoning tier — L3 evaluator) | Weaver's AI-assisted reasoning tier for ambiguous/novel convergence gaps. The marquee AI-native feature. | ★★ | M–L | ✅ Fires 1+2a+2b shipped (loop closes: escalate→review→dispatch) · [design](../../implementation-artifacts/augur-design.md) + [dispatch design](../../implementation-artifacts/augur-dispatch-pickup-design.md) · 🚧 Fire 3 autoApply Andrew-gated; follow-up: mid-flight-kill + drift-invalid e2e (§6 residual) |
| Starlark guards (Loom) | The reserved `{reads, starlark}` guard escape hatch needs a verified-pure sandbox. | ★ | M | ✅ ratified (split) · [design](../../implementation-artifacts/loom-starlark-guards-design.md) · 🚧 Loom-side held (ships with first consumer) |
| **Weaver planner mandate (dispatcher → solver)** | Remediation stops being a static gap→action lookup: deterministic planner (per-gap candidate selection, then goal-regression synthesis over op-declared effects) with contraction/oscillation diagnostics and admission control; shadow mode + per-target cutover; the Augur stays the AI boundary. | ★★★ | XL | ✅ effectively done · [design](../../implementation-artifacts/weaver-planner-mandate-design.md) · Fires 1-9(Inc1)+R1-R3 shipped, consumed by LoftSpace renewals; Fire 9 AI tail deferred - needs a novel Augur gap, not renewals |

### Read-model / projection maturity
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Refractor/deploy] Loupe read-only PG role (`provision-loupe-role`)** | Loupe's shipped F9 seam reads postgres lens targets via `LOUPE_PG_DSN` — needs a SELECT-only role (mirror `provision-loftspace-role`) + an inspector posture over FORCE-RLS tables: BYPASSRLS (recommended) vs wildcard `actor_read_grants` grant. Until then, postgres lens contents render pg-pending. | ★★ | S | 📋 · unblocks loupe F9 full value |
| **[Refractor] Convergence-lens filtering-WHERE activation guard** | Filter-retraction relies on convergence (`violating`) lenses never carrying a filtering WHERE (a retracted row reads to Weaver as entity deletion) — true for every live lens but unenforced at activation. | ★ | XS–S | 📋 review carry-out · [design](../../implementation-artifacts/negative-filter-retraction-projection-design.md) §Fires-1+2-checkpoint |
| Elasticsearch target adapter | A third lens target adapter (only NATS-KV + Postgres ship; no consumer yet). | ★ | M | ✅ ratified (2026-07-02, OpenSearch pin + FTS-first interim) · [design](../../implementation-artifacts/search-target-adapter-design.md) · shelf — first consumer (LoftSpace FTS unified search) filed on verticals; the OpenSearch adapter builds only on search-engine-scale demand |
| **[Refractor] Cross-instance projection-latency rollup** | Aggregate per-lens projection latency across Refractor instances into one per-component view (single-instance today, so per-instance == per-component). Link-tombstone re-projection half **subsumed** by the link-aspect reprojection design. | ★ | S | 🚧 seq behind HA-NATS multi-instance · [link-aspect design](../../implementation-artifacts/link-aspect-triggered-reprojection-plain-lenses-design.md) subsumes the tombstone half; no multi-instance consumer yet |

### Refinements & ops
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **CI pipeline speed (continuous)** | Make CI faster without weakening any gate — owned continuously by the **Whetstone**. Matrix split done (serial → 4 parallel jobs); convergence + unit parallelized. | ★★ | M (ongoing) | 🏗️ continuous (Whetstone) · `internal/bridge`'s 46 tests + a fixture race fixed (d2b6321, package 35s→7s) but `unit` job wall-clock unchanged (~137s) — local per-package sums don't predict the `-p4` critical path; next: capture real per-package timing FROM a CI run to find the actual pole |
| **[CI/Refractor] Hello-Lattice NFR-P3 latency flake** | The capability-projection probe fails-then-passes on the shared CI runner — re-scoped to a 1000ms regression guard (Andrew-ratified; reported SLA unchanged), but the runner floor has drifted to ~1.1s. | ★★ | M | ✅ fixed 2026-07-03 (`94c8224`, deadline 1000ms→2000ms) — re-examine if it recurs |
| **Hard-delete mutation verb (true link/aspect keyspace reclaim)** | Mutation vocab is create/update/tombstone (soft PUTs); a tombstoned key persists + is still enumerated by `kv.Links`. A 4th `delete` verb (NATS `DEL`) lets dead links leave the keyspace, bounding `kv.Links` LIST cost. | ★ | M | 🗄️ shelved (Andrew 2026-07-02) · [design + hold banner](../../implementation-artifacts/hard-delete-mutation-verb-design.md) · demand dissolved by clinic write-path slot claims; §3 edits reverted; revive only on a real reclaim driver |
| **Script-read posture — declared+hydrated vs live `kv.get`/`kv.Links`** | Declared+hydrated reads as the write-path norm: `optionalReads` folds read-before-create in; `kv.Links` declared-as-metadata (Edge-gate + best-effort lint, not hydrated); guards become a generic Processor-side operation feature (supersedes Loom's engine read). | ★★ | L | ✅ Fires 1–2 shipped · [design §12](../../implementation-artifacts/script-read-posture-design.md) · 3-layer reviewed, CI green; Fire 3 (guards) deferred to its first consumer; 55 class-(b) sites are the debt worklist |
| **CreateTask logical-delete create-wedge** | A logically-deleted task can never self-heal: CreateTask's create always conflicts against the still-present doc (Contract #10 §10.3's "logical delete ⇒ create" claim holds only for hard tombstones). Pre-existing; found by script-read-posture's self-review. | ★★ | S–M | 📋 · decide resurrect-vs-suppress + reconcile §10.3 |
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

- 2026-07-07 · `da8ee6c` · [Refractor/pkgmgr] refractor-protected-by-default-gate — declare-one gate (translateSpec + pkgmgr + lint-conventions); 3-layer reviewed, fixed forward (lint scanner rewrite, BootstrapLens gap, Public+GrantTable guard)
- 2026-07-07 · `921fda4` · [lease-signing/processor/lattice-pkg] real-actor-write-auth-e2e Phase 1 items 4+6 — consumer scope=self grant + live e2e proof; 2 platform bugs fixed along the way; 3-layer reviewed, fixed forward

- 2026-07-06 · `265d5d8` · [Processor/Loom/Weaver] script-read-posture Fires 1–2 — optionalReads + enumerations metadata + read-posture lint; 3-layer reviewed, CI green
- 2026-07-06 · `5ad5d6e` · [Makefile] real-actor-write-auth-e2e Phase 1 item 3 — `up-full-capability` + `dev-seed-staff` (staff identity holds operator); lead-reviewed, live-verified except the AssignRole leg (permission-gated); item 4 (e2e) next
- 2026-07-06 · `cf102b4` · [Gateway/apps] real-actor-write-auth-e2e Phase 1 item 1 — shared dev-IdP trust (loftspace/clinic dev-auth now signs+verifies with the Gateway's checked-in dev key); lead-reviewed; item 3 (up-full-capability) next
- 2026-07-06 · `88815a8` · [Vault/Refractor] Personal Lens Fire PL.5 CLOSED — IssueSessionKey transient-key RPC + ciphertext passthrough marking; Gate-3 vector 5 e2e; 3-layer reviewed, fixed forward (rowHasCiphertext false-positive)
- 2026-07-06 · `512ce42` · [Chronicler] chronicler-host-reconciliation CLOSED — live cutover done (attended): refractor/chronicler cycled, health green, no cypherRule errors; NATS container needed a restart (torn bind-mount)
- 2026-07-06 · `0ae926a` · [Refractor/Processor] refractor-publish-acl-gap — ops.system + lattice.sync.> NATS grants (refractor + processor, co-located privacyworker); 2 natsperm proof vectors; 3-layer reviewed, clean
- 2026-07-06 · `b0530b8` · [Weaver] Registry cleanup edge branches — targetId-rename + pattern-alias-reassignment coverage (33%→89%, 50%→100%); test-only, lead-reviewed
- 2026-07-06 · `5e83939` · [Privacy/Object Store] Crypto-shred Fire 3 — erasure-coverage + §4.2 multi-party-independence tests over the real Loupe GET/decrypt handlers; test-only, lead-reviewed; Fire 4 (vertical consumer) next
- 2026-07-06 · `6169671` · [Privacy/Object Store] Crypto-shred Fire 2 — Loupe trusted-client encrypt/decrypt path (AES-256-GCM, oid-bound AAD, Vault WrapKey/UnwrapKey RPCs); 3-layer reviewed, fixed forward (AAD binding); Fire 3 next
- 2026-07-06 · `98ac889` · [Refractor] Personal Lens Fire PL.4 — Hydration Hook (`personal.hydrate` control RPC, cold bulk projection + terminal marker); 3-layer reviewed, fixed forward (SetRevisionCursor CAS race)
- 2026-07-06 · `6cfda76` · [Weaver] weaver-exhausted-escalation-and-model CLOSED (Fire 9 Inc1) — exhausted budget escalates to Augur or raises `GapBudgetExhausted`; `augur.model` threaded; 3-layer reviewed, fixed forward (mark-storm bug)
- 2026-07-06 · `7f34136` · [LoftSpace/Weaver planner] Lease-renewal R3 CLOSED — `renewalsRead` dual-anchor lens + tenant/landlord Renewal cards + task CTAs; 3-layer reviewed, fixed forward (co-manager read-access gap, numeric coercion)
- 2026-07-06 · `286fd98` · [Chronicler/docs] component doc page + Fork-C re-ratification (own `health.chronicler.<instance>` heartbeat, Loupe node already expects it); eventlens→`cmd/chronicler` extraction is the ratified pending build
- 2026-07-06 · `a865692` · [Refractor/docs] arch-review 2026-07-06 re-review filed + doc/marker truth-up (failure-tiers now-built sections, refractor.md 17-pkgs/step8-9/health-key, vendors ANTLR row, classify/rls stale markers)
- 2026-07-06 · `8fa743c` · [Contract #3] §3.5/§3.4/§3.8 amended to as-built — referential integrity is script + Weaver's job (no step-6 dangling-ref pass); event schemas package-owned (no step-7 event-DDL check); arch item 5
- 2026-07-06 · `3884f01` · [Contract #10] loom async-deadline paragraph reconciled to §10.6 — deadline bounds instanceOp submission (disarms at commit); bridge give-up timeout is the dead-call backstop (arch item 12)
- 2026-07-06 · `6d2b4c5` · [Weaver] External-gap stale-mark reclaim — prompt fresh-instance retry after a failed call, per Contract #10 §10.3; 3-layer reviewed, fixed forward (vacuous confirmedConcluded signal)
- 2026-07-06 · `945f605` · [Contract #7] §7.2 reconciled to as-built kernel — holdsRole→operator topology (not data.protected), 5→1 meta-meta DDL, no processor identity; §7.7 untouched (arch item 7; +922a294, dfbad3d)
- 2026-07-06 · `9711814` · [Contract #2] §2.6 error-code table reconciled to the wire + §2.9 lenient-parse fix + TestConformance_ErrorCodeTable_MatchesWire pin (arch item 4)
- 2026-07-06 · `81c0c6b` · [Weaver] Planner mandate R2 — LoftSpace lease-renewal package (5 ops, 2 goal-authored targets, e2e); 3-layer reviewed, fixed forward (oscillation-path collision, double-extension guard); R3 FE next
- 2026-07-05 · `11cc15f` · [loom] dispatch authContext.target — carry the real vtx.meta.<NanoID> as Pattern.MetaKey (not the human PatternID), both dispatch sites + pinning test (arch-review item 10)
- 2026-07-05 · `11cc15f` · [repo] debris — 5 CONTRACT-AMENDMENT-REQUEST.md removed, objects-base reclaim comments fixed, objmgr up-full BOOTSTRAP_JSON_PATH, internal/spike README (arch item 11)
- 2026-07-05 · `11cc15f` · [Gateway] up-full deploy — Gateway now started in make up-full (dev-mode :8080), Loupe map node no longer a ghost (arch item 1a; F10)
- *(older entries rolled to [archive/lattice-done.md](archive/lattice-done.md))*
