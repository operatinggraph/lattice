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
| **[Processor] Tombstone-with-document warn→reject flip (Fire 2)** | Fire 1 (emitter sweep + parser warn) shipped `6b68fde4`; flip the warn to a reject once warn sightings are clean (stale stored scripts clear via world recreation). | ★★ | XS | 🚧 seq behind clean warn-window · [design](../../implementation-artifacts/tombstone-body-preservation-design.md) §6 |

### Survey log (round-robin rotation)

Rotation memory only — findings are the filed rows; fire narratives live in commits, never here.
Components: Core · Weaver · Loom · Refractor · Bootstrap · object-store-manager (+ the cross-cutting
feature backlog; Loupe moved to its own lane, [loupe.md](loupe.md)). Survey the stalest
(`git log -1 --format=%ct -- <path>`), note ONE dated line, rotate.

- 2026-07-02 Arch-review, all components — filed the intake section below; Refractor findings held for the post-update re-review; root-identity designation → Designer.
- 2026-07-02 Designer — object-plane-nats-permissions (★★★ arch #2; `$O.core-objects.>` grant fix + first natsperm object vectors; no contract change) (→ 📐).
- 2026-07-05 objmgr-and-bootstrap-component-pages CLOSED — bootstrap/vault/privacyworker pages written, README+architecture-overview updated, Bootstrap + object-store-manager added to this rotation.
- 2026-07-06 Arch-review — Refractor deferred re-review filed ([report](../../../docs/reviews/arch-review-2026-07-06.md)): verdict drifted; 9 rows filed (chronicler-host ★★★, publish-acl ★★★, protected-by-default ★★★); doc/marker truth-up done.
- 2026-07-13 Core (processor healthy, clean lint/vet, no TODOs; step 6.5 sensitive-encrypt path was 0% covered, filled 80.1%→82.0%).
- 2026-07-18 Weaver (healthy, 86.8%/78.6%/91.3% cov, clean lint, no TODOs; filed error-branch-coverage + a doc-drift fix).
- 2026-07-18 Loom (healthy, 82.3%/80.2% cov, clean lint, no TODOs; prior deadline/redelivery gaps already shipped `495476b`; filed starlark-guard-sandbox-value-iface-uncovered).
- 2026-07-18 Refractor (healthy, build/lint clean; confirmed all 8 07-06-review findings already resolved in code — no new rows).
- 2026-07-19 object-store-manager (67.5%/91.4% cov, clean lint, no TODOs; filed doc-drift fix + cascade error-branch coverage).
- 2026-07-19 Bootstrap (69.3% cov, clean lint, no TODOs; filed stale-bootstrap-json-no-freshness-probe (★★, the documented Known-gap) + seed-idempotency-branch-coverage).
- 2026-07-19 Core (processor 81.8%/substrate 76.2% cov, clean lint, no TODOs; filed consumer-supervisor-accessors-untested + outbox-consumer-undercovered + processor.md UninstallPackage doc-drift).
- **Next:** Weaver.

## Arch-review intake — platform hardening & doc/contract truth

Open corrections from the [2026-07-02 full-platform review](../../../docs/reviews/arch-review-2026-07-02.md)
— per-finding `file:line` evidence and per-component verdicts live there; the What-cells here are abridged.
Refractor's deferred re-review is now filed as its own subsection below (2026-07-06).
Severity-ordered; same row discipline as component maintenance (shipped rows collapse to the Done log).

| Item | What it is | Imp | Size | State |
|---|---|---|---|---|

### Refractor re-review (2026-07-06)

The deferred post-update re-review the 2026-07-02 pass held back — verdict **drifted** at the time; full
evidence in [arch-review-2026-07-06.md](../../../docs/reviews/arch-review-2026-07-06.md). **CLOSED** — the
2026-07-18 survey confirmed all 8 ranked corrections landed (`de4290b4`, `c5ed56b0`, `da8ee6cc` + the
Chronicler-host extraction and NKey-matrix grants), no open rows remain.

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

> 🎯 **Build-ready now.** No ratified-unbuilt design is left — the flywheel needs the Designer to
> restock. The best ready picks all need no new design: the two **appsession** rows (★★ M each — the
> production IdP posture is the one that blocks any non-loopback deploy, and CSRF bites as soon as two
> FEs are co-hosted), then `actor-aggregate backfill` (★★ S–M, named live consumer) and
> `lint-package-standard` (★★ M). Every other `✅ ratified` row is done or driver-blocked; the rest are
> Whetstone's or parking-lot. A stale callout starves the lane — whoever ships the next pick renames this.

### Security & trust boundary
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **`contextHint.reads` is a pre-script Core-KV existence oracle** | A declared-but-absent read faults `HydrationMiss` at step 4 and the reply returns `details.missingKey` (`step4_hydrate.go:170` → `commit_path.go:907` → `gateway.go:528`). The submitter writes its own contextHint and step-3 scope=self never inspects the resource, so any actor with any op grant probes ANY key's existence, one round trip each, without a script running. Fix precedent: `ClaimKeyInvalid` strips details (NFR-S6); check Contract #2 §2.4 first. | ★★★ | M | 📋 ready |
| **Starlark 250ms wall budget fails installs under parallel test load** | `go test ./...` at default `-p` reds a different package-install test each run with `ScriptTimeout: script exceeded wall budget 250ms` — reproduced on unmodified `main`, so it predates any one fire. Costs every fire an investigation to rule out its own change. | ★★ | S–M | 📋 ready |
| **Actor-aggregate lens rows do not backfill on a lens change** | Adding a walk to an actorAggregate lens reprojects nothing already stored — rows refresh only when a CDC event next touches that actor, and the obvious re-assert ops no-op when state already matches. `reproject`/`rebuild` need an asserted control-plane actor, so no operator-reachable backfill. Consumers: LoftSpace's landlord hat; 51 stale `cap-read.edgeManifest*` docs. | ★★ | S–M | 📋 ready · [live evidence](../../implementation-artifacts/read-grant-single-source-walk-design.md) §10 |
| **[appsession] The production IdP posture cannot open a session** | `setCookie` runs only under a non-nil `Signer`, so with `_JWT_PUBLIC_KEY`/`_ISSUER` configured nothing can issue the session cookie — the documented verify-only posture is unreachable. Fails closed (401 everywhere), and the per-request credential→identity resolve the old read boundary did now happens only at dev-login. Consumer: the first non-loopback deployment of any of the five FE binaries. | ★★ | M | 📋 ready |
| **[appsession] Cookie sessions carry no CSRF defense** | The kit's sessions rest on `SameSite=Strict` alone; cookies are domain- not port-scoped, so loupe :7777 / clinic :7799 / loftspace :7788 are same-site to one another and a `no-cors` POST from one carries another's cookie. No CSRF token, no Origin / `Sec-Fetch-Site` check on state-changing POSTs. Strictly better than the pre-kit no-auth posture; bites once any two of these are co-hosted or a real IdP lands. | ★★ | M | 📋 ready |
| **Multi-hat `scope=any`+`scope=self` first-match over-confines** | `matchPlatformPermission` returns on the first operationType match regardless of scope, and `capabilityRoles` collects roles unordered — so a consumer+staff identity (e.g. seed-showcase `seedSamMultiHat`) can authorize their OWN cafe tab as scope=any, losing the self exemption. Fail-closed; bites a multi-hat who works and lives in different buildings. | ★ | S–M | 📋 ready · no live victim (showcase multi-hat has no leaseapp) |
| NATS account-level write restriction | Close the fabricated-KV-write surface at the substrate (account-level); today defended only by overwrite-by-reprojection. | ★★ | M | ✅ effectively done · [design](../../implementation-artifacts/nats-account-write-restriction-design.md) §Fire-3-status · only deferred Fire 4 (prod mTLS) remains |
| **Keyed identity-index hashes (HMAC)** | Unkeyed `sha256NanoID` contact hashes are dictionary-testable with substrate access and persist in JetStream history post-shred; a Vault-keyed HMAC bounds it but needs a MAC primitive + key custody at every hash computer, and must migrate ALL index consumers (identityindex, provision probe, dedup) in one stroke. | ★ now / ★★ prod | M | 🗄️ shelved (revive: production threat model) · [analysis](../../implementation-artifacts/dedup-over-encrypted-pii-design.md) §9.1/§10-C |

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

### AI-native
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| AI-authored capabilities | A Lattice-aware agent proposes DDL/Starlark/lenses/workflows through human review + deterministic validation + rollback. | ★★–★★★ | L | ✅ effectively done · [design](../../implementation-artifacts/ai-authored-capabilities-design.md) · Fires 1–4 shipped (2026-07-16, `219fa0c`); Fire 5 (auto-apply) design-only per Andrew; Loupe UI is Stream 3's lane |
| **The Augur** (AI reasoning tier — L3 evaluator) | Weaver's AI-assisted reasoning tier for ambiguous/novel convergence gaps. The marquee AI-native feature. | ★★ | M–L | ✅ Fires 1+2a+2b shipped incl. §6 residual e2e (loop closes: escalate→review→dispatch) · [design](../../implementation-artifacts/augur-design.md) + [dispatch design](../../implementation-artifacts/augur-dispatch-pickup-design.md) · 🚧 Fire 3 autoApply Andrew-gated |
| **Weaver planner mandate (dispatcher → solver)** | Remediation stops being a static gap→action lookup: deterministic planner (per-gap candidate selection, then goal-regression synthesis over op-declared effects) with contraction/oscillation diagnostics and admission control; shadow mode + per-target cutover; the Augur stays the AI boundary. | ★★★ | XL | ✅ effectively done · [design](../../implementation-artifacts/weaver-planner-mandate-design.md) · Fires 1-9(Inc1)+R1-R3 shipped, consumed by LoftSpace renewals; Fire 9 AI tail deferred - needs a novel Augur gap, not renewals |

### Read-model / projection maturity
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| Elasticsearch target adapter | A third lens target adapter (only NATS-KV + Postgres ship; no consumer yet). | ★ | M | ✅ ratified (2026-07-02, OpenSearch pin + FTS-first interim) · [design](../../implementation-artifacts/search-target-adapter-design.md) · shelf — FTS interim consumer SHIPPED (`b105cf5`); OpenSearch adapter itself still has no consumer |
| **[Refractor] Cross-instance projection-latency rollup** | Aggregate per-lens projection latency across Refractor instances into one per-component view (single-instance today, so per-instance == per-component). Link-tombstone re-projection half **subsumed** by the link-aspect reprojection design. | ★ | S | 🚧 seq behind HA-NATS multi-instance · [link-aspect design](../../implementation-artifacts/link-aspect-triggered-reprojection-plain-lenses-design.md) subsumes the tombstone half; no multi-instance consumer yet |

### Refinements & ops
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **`lint-package-standard` gate (S1/S6/S7 subset)** | The ratified [Vertical Package Standard](../../implementation-artifacts/vertical-package-standard.md) is normative text with no enforcement — text alone doesn't bind the next author. Gate the mechanically-checkable rules: S1 full `OpMetaSpec` per user-facing op, S6 `lens_cypher_test.go` + structure pins, S7 `grantsTo` mirrored. Mirror `lint-lens-anchors`: packages/** scan, CI-wired, fail-closed + declared-exemption escape hatch. | ★★ | M | 📋 ready |
| **Embedded-NATS shard flakes under parallel load** | Two different embedded-NATS tests failed on CI runners on consecutive days (`TestLaneSpecs_PerLaneBacklogIsolation` unit-1; `TestPersonalLens_PL2_E2E_InterestSetFiltersThenAdmits` unit-2); both post-date the per-test-server parallelization. Local repro: `go test ./...` with NO `-p` cap reddens 3 other embedded-NATS tests that pass 3x in isolation and under CI's `-p 4`. Root-cause per the flake rule: tighten, never loosen. | ★★ | M | 📋 ready · owner: Whetstone |
| **CI pipeline speed (continuous)** | Make CI faster without weakening any gate — owned continuously by the **Whetstone**. Matrix split done (serial → 4 parallel jobs); convergence + unit parallelized; unit itself now sharded across 2 runners. | ★★ | M (ongoing) | 🏗️ continuous (Whetstone) · aggregate-CPU ceiling confirmed 2x, isolating natsperm into its own step reconfirmed it (Done log) · next: propose paid larger runners to Andrew |
| **Hard-delete mutation verb (true link/aspect keyspace reclaim)** | Mutation vocab is create/update/tombstone (soft PUTs); a tombstoned key persists + is still enumerated by `kv.Links`. A 4th `delete` verb (NATS `DEL`) lets dead links leave the keyspace, bounding `kv.Links` LIST cost. | ★ | M | 🗄️ shelved (Andrew 2026-07-02) · [design + hold banner](../../implementation-artifacts/hard-delete-mutation-verb-design.md) · demand dissolved by clinic write-path slot claims; §3 edits reverted; revive only on a real reclaim driver |
| **Script-read posture — declared+hydrated vs live `kv.get`/`kv.Links`** | Declared+hydrated reads as the write-path norm: `optionalReads` folds read-before-create in; `kv.Links` declared-as-metadata (Edge-gate + best-effort lint, not hydrated); guards become a generic Processor-side operation feature (supersedes Loom's engine read). | ★★ | L | ✅ Fires 1–2 shipped · [design §12](../../implementation-artifacts/script-read-posture-design.md) · Fire 3 (guards) deferred to its first consumer; debt sweep + warn→block flip SHIPPED `63aab49` |

### Parking lot — very low priority (far, far back)

Real but low-value; do **not** spend design or build effort here unless Andrew greenlights one.

| Item | Why it's parked | Imp | Size | State |
|---|---|---|---|---|
| **Expose the authorizer's resolved roles to op scripts (`op.actorRoles`)** | Step 3 resolves the actor's roles from the cap doc but scripts cannot see them, so an op asking "is my caller root" re-derives it by walking `holdsRole` — a re-derivation that can disagree with what step 3 authorized, plus a `kv.Links` round trip per op. | ★★ | S | 📋 ready · consumer: the staff workplace guards ([staff-worlds F4](../../implementation-artifacts/facet-staff-worlds-design.md)) |
| **Historical state query (FR51)** | Operators query historical state across a time range (audit/ledger + point-in-time reconstruction). Low near-term value + standing storage cost; builds to reserved contract seams. | ★ now / ★★ if real need | M→L | ✅ ratified (design) · [design](../../implementation-artifacts/historical-state-query-design.md) · build deferred (Andrew, revive on a concrete need); archive layers re-home to the Chronicler |
| multi-aspect atomic OCC for `UpdateMetaVertex` | `meta_ddl.go` applies `expectedRevision` to the first changed aspect by design; true multi-key OCC needs a substrate per-key-revision primitive — marginal value. | ★ | M+ | 🗄️ parked |
| freshnessExpiry marker tombstone-on-convergence | A converged marker is read by nothing and harmless; tombstoning buys cleanup not correctness. | ★ | S | 🗄️ parked |
| production freshness-window tuning | A staleness-tolerance vs. timer-churn value judgment — Andrew's call if/when it matters. | ★ | XS | 🗄️ parked |

## Done log — lattice (newest first)

One line per shipped item (`date · SHA · [tag] title`). Oldest roll to `archive/` past ~25.

- 2026-07-24 · `10f01e71` · [lint,clinic-domain] a declaration binds to its own statement, not the next 8 lines (12 drifted sites fixed); clinic keys its exemption on `op.authTargetValidated`, `(legacy-self-exempt)` deleted
- 2026-07-24 · `185a47ee` · [edge-manifest,test] the generated cap-read producer driven through the projection driver — an emptied slice retracts its key, a reachable actor keeps every real entry
- 2026-07-24 · `bd3b76ba` · [pkgmgr,edge-manifest,lint] one `AnchorWalk` declaration compiles both read-grant enumerations — closes the dual-enumeration footgun (S2); 13 lenses migrated, 3 producers generated, gate flipped
- 2026-07-24 · `390a6754` · [identity-domain,lint] validated-target exemptions are declared, not assumed — RecordIdentityPII rekeyed + §3.4.1 resource-bound; closes the forgeable-`authContext.target` item (Fire 2 of 2)
- 2026-07-24 · `390a6754` · [lint] `authcontext-target` + `workplace-exempt` blocking gates (derived helper set, fixture self-test) — closes the validated-target-exemption-without-a-resource-bind item
- 2026-07-24 · `fe8378c0` · [processor,packages] `op.authTargetValidated` closes the forgeable-`authContext.target` confinement bypass — primitive + 4-package guard migration (Fire 1 of 2)
- 2026-07-24 · `acdcfc7c` · [controlauth,test] flush the responder SUB before the echo helpers return — kills the `no responders available` unit-1 flake (client raced the responder's subscription); deterministic, 20x -p4 clean
- 2026-07-24 · `657861dd` · [gateway] `/v1/actor` answers its own CORS preflight — `handleWhoami` sets Vary/allow-listed headers + OPTIONS→204 before the GET-guard (mirrors `handleOperationStatus`); completes the whoami-hats browser surface
- 2026-07-24 · `6aa4959c` · [refractor,test] manifest.me self-anchored reprojection proven sound — e2e (existing personal row grows on a 2nd holdsRole link, D1 self-gate active); filed "ordering-token" freeze mechanism disproven, no server bug
- 2026-07-24 · `473929e3` · [rbac,clinic,wellness,service] revive a tombstoned grant instead of failing — grant_link/revive at 6 sites so a RevokeRole'd identity + re-bound provider can be re-granted; pkg versions bumped
- 2026-07-24 · `68ffc584` · [lint,edge-manifest] dual-enumeration S1 done — `lint-lens-anchors` CI gate (every non-self Personal-lens anchor kind needs a producer branch) + provider-world coverage; testkit now spans all 3 personas
- 2026-07-24 · `385c26a7` · [edge-manifest,test] read-grant/lens dual-enumeration coverage proof (Stage-1 testkit) — asserts every non-self anchor a Personal lens projects is granted, no vacuous pass; resident+staff personas
- 2026-07-24 · `56841e13` · [leaseconvergence,CI] fixed `TestRenewalConvergence_TwoTenantsDivergeThenDeclinePath`'s non-unique landlord-name RevisionConflict + widened `test-lease-convergence`'s `-run` filter so CI actually runs it
- 2026-07-24 · `283dd1a9` · [appsession] Kit gained its production verify-only (`_JWT_PUBLIC_KEY`/`_ISSUER`) branch + `revocationChecker` param — clinic W1 Inc 1 adoption; closes the production-verifier gap (Facet passes nil, unchanged)
- 2026-07-24 · `a2e71712` · [appsession,facet] Facet's session block extracted to a shared `internal/appsession` kit — the five-FE sign-in seam (persona-worlds P2); closes the platform-seams item
- 2026-07-23 · `a16b7589` · [gateway,identity-domain] whoami hats — `/v1/actor` reports roles[]+anchors[] via the new `identityAnchors` lens (persona-worlds P1; first Phase-0-brief fire)
- 2026-07-22 · `1ab88603` · [bootstrap] `VerifyKernel`/`InspectKernel` (the `make verify-kernel` gate logic) gain embedded-NATS defect-injection tests; package 71.2%→82.9%
- 2026-07-22 · `737e687e` · [bootstrap] `DecideReseed` extracted from `cmd/bootstrap`'s untested probe-then-reopen branch into `internal/bootstrap`, covered by 4 embedded-NATS tests
- 2026-07-22 · `907d0d34` · [weaver] fresh-episode/reclaim error-branch coverage — `fireEpisode` stale-mark reclaim + dispatch/effect-bump + `reconcileConsumers` Add/Remove faults; package 86.5%→87.9%
- 2026-07-22 · `6e1c7557` · [gateway] `GATEWAY_CORS_ORIGINS` dev default gains `127.0.0.1` twins for all four vertical apps (only :7810 had both) — live-verified via CORS preflight, closes the silent-write-block
- 2026-07-22 · `6b68fde4` · [processor,bootstrap,pkgmgr] tombstone body-preservation Fire 1 — emitter sweep drops the isDeleted/data husk, schema relaxed, parser warns (not silently drops) a tombstone-with-document; Fire 2 (warn→reject) next
- 2026-07-22 · `74883406` · [refractor,edge] Personal Lens retraction R2 — Edge-client keyset consumption (both engines) + hydrate dead-lens prune; unblocks the verticals staff-worlds claim beat

- 2026-07-22 · `c2abdfbe` · [refractor] `Pipeline.Run` seeds `lastAppliedSeq` from the durable's persisted ack floor at startup — closes the reconciliation-token residual, quiet-stream restarts no longer stay inert
- 2026-07-22 · `baf3cb30` · [refractor,rbac-domain] `capabilityRoles` emptyBehavior:delete now fires on last-role revocation — RealnessFiltered generalized for mixed map/scalar list columns; rbac-domain 0.3.0→0.3.1
- 2026-07-22 · `5c5cb236` · [refractor] `personal.hydrate` fans out to every registered Personal Lens, not just the last-registered one — fixes a role-queued task never reaching a rehydrating device
- 2026-07-22 · `77a9dea8` · [facet] host health emission — `health.facet.<instance>` via a second host-level NKey connection (natsperm `facet` row, publish health-kv-only + `_INBOX.>` subscribe); Lamplighter now sees a crash-looping sync engine
- 2026-07-22 · `ac4d46b8` · [refractor] auth-plane convergence sweep heals graph↔Capability-KV divergence via the reproject path — `CapabilityCoverageDivergence` + `reconciled` counter; closes the projection-reconciliation item
- 2026-07-22 · `222f66a5` · [CI] `edge-browser-store` retries once on the `websocket url timeout reached` signature alone — a cold-start miss no longer reds the gate, every other failure still fails unretried
- 2026-07-22 · `52fc791f` · [weaver] `resetConfidence` control verb + CLI drains a target's `__effect` confidence windows — the disable<reset<revoke ladder's middle rung; grants to control-authz + console-operator, never demo-operator
- 2026-07-22 · `9af5aed5` · [loom] poll-until-created in the e2e harness (new `waitTaskCreated`) — closes the `taskCreated`-after-`waitTaskKey` relay race across 8 sites; `-race -count=5` clean
- 2026-07-22 · confirm-only · [refractor] the two protected lenses (`landlordUnitsRead`/`landlordLeaseApplicationsRead`) confirmed clean live — 0 target rows vs a graph with 0 `manages` links: nothing to reconcile
- 2026-07-21 · `7b74ce70` · [packages] demo-operator inspect-only grant package (F20.3) — console-operator minus every write: `demoOperator` role + read lens + 3 `ctrl.*.read` grants; the platform boundary for public Loupe exposure (Andrew-gated)
- 2026-07-21 · `446b3549` · [weaver] revision-condition lane-1's mark delete so a lane-1/sweep race on one `__effect` close credits it once, not twice — a double-credit could mask a real LensEffectMismatch; -race regression test
- 2026-07-21 · `6b86e9e4` · [bootstrap] the `up` target keeps a stale bootstrap file on an empty Core KV (recreated stack — binary re-seeds at stable NanoIDs), discards only on a real mismatch — new `CoreKVEmpty` / `probe-empty` discriminator
- *(older entries rolled to [archive/lattice-done.md](archive/lattice-done.md); includes `94c8224` hello-lattice NFR-P3 flake fix)*
