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
| **[Bootstrap] Reconcile creates + updates but never removes a retired kernel key** | A kernel entity the current binary no longer builds survives forever in an old bucket; needs an authoritative kernel-owned key enumeration separable from package-written `vtx.meta.*`. | ★ | S–M | 📋 designer · confirmed `internal/pkgmgr` also owns `vtx.meta.*` (installer.go:657,720) — prefix scan can't separate them, needs a provenance marker · [why](../../implementation-artifacts/kernel-seed-reconcile-design.md) §8 |
| **[orchestration-base] A closed task's ephemeral grant stays exercisable until expiry** | `capabilityEphemeral`'s three branches filter only `expiresAt > $now` (lenses.go:284-308) and step-3 matches taskKey+opType+target+expiry — status never checked — so CancelTask/CompleteTask do not revoke the grant; a cancelled task's op stays submittable until `expiresAt`. Lens-side `status='open'` filter is the likely shape (myTasks already has it). | ★ | S | 🗄️ shelved (Andrew 2026-07-27: deprioritized; revive: a long-TTL task class or observed misuse) |
| **[Refractor] A live claim's own consumer grant never projects into Capability KV** | `ClaimIdentity`'s R2 grant lands in Core KV (verified) but `cap.roles.<target>` never appears — no DLQ, absent past a completed sweep. Repro `make test-claim-ceremony` (2/3 live runs, demo box). | ★★★ | M | 🚧 Andrew driving directly (dedicated session) · [design §13.7](../../implementation-artifacts/facet-staff-worlds-design.md) |
| **[Refractor] Footprint reduction — kill the five amplifiers** | An 11 MB graph produces 6 GB substrate exhaust + 540 MB RSS + restart storms: unanchored full-graph rescans, ungated vertex events, whole-stream per-lens durables, unconditional audited writes, and a structurally-dead per-pipeline adjacency watch. 7 fires (quick wins + D1 filter-subjects + D2 delta evaluation). | ★★★ | L | 🏗️ building (Winston session, Andrew-directed) · [design](../../implementation-artifacts/refractor-footprint-reduction-design.md) |
| **[Loom] Guardless-step recovery check-before-act probe** | On total `loom-state` loss + a re-triggered `StartLoomPattern`, a fresh instance replays guards from cursor 0 (re-runs an already-applied guarded step). | ★ | S–M | 🗄️ shelved-backup (Andrew: no new engine Core-KV reads) |
| **[lattice CLI] `lens list` names no lens** | CANONICAL_NAME is empty for every installed lens: `canonicalNameFromDoc` (`cmd/lattice/lens/lens.go:158-164`) reads the meta-vertex ROOT's `data.canonicalName`, but Contract #1 puts the name on a separate `.canonicalName` aspect (the root's `data` is `{protected:true}`). `lens_test.go:27`'s fixture invents the root shape, so the test passes over a shape that never exists. Routed by the Café PO fire. | ★★ | XS | 📋 ready |
| **[Edge] Every engine build leaks a durable SYNC consumer** | `engineManager.Acquire` mints a fresh deviceID per engine (`cmd/facet/enginemanager.go:117`) so each build creates a new `edge-sync-<identity>-<device>` durable, and nothing ever deletes one (`RunDurableConsumer` documents that it does not). Live: 9 durables for 5 identities, one identity holding 4, each pinned at the 10k per-subject cap. Grows per sign-in; each new durable also re-reads the whole retained subject. | ★★ | S–M | 📋 ready · reuse the device id (store is already per-identity) or reap on Purge |
| **[Edge] A cold sign-in replays the actor's retained history, not their world** | Measured live 2026-07-31: 2,049 frames to deliver a 14-key world (146×), `ready` at 33s. One subject `lattice.sync.user.<id>` carries every key's every revision, consumed `DeliverAll` (hardcoded `substrate/consumer.go:142`; the edge transport seam declares no policy field). Compaction means per-key subjects — which needs tombstones, a keyset-frame home, and new cursor/gap semantics. | ★★ | L | 📋 designer · retention cap + boot-gate already shipped; this is the amplification |
| **[Pkgmgr] `OpMetaSpec` has no vocabulary for a client-mint-and-reveal-secret ceremony** | identity-domain's 5 `[no-op-meta:]`-exempt ops ([standard §8](../../implementation-artifacts/vertical-package-standard.md)) need 3 new primitives (computed-hash directive, mint-then-reveal UX, raw `AuthContext`) no existing field configures — every past extension shipped one declarative idea, never this. Consumer: [verticals.md](verticals.md) 5-ops row. | ★★ | M | 📋 designer · ground `internal/pkgmgr/definition.go:437-530` + `cmd/facet/credentials.go:242-252` |

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
- 2026-07-25 Refractor (pre-scoped, out of rotation) — filed shared-bucket rebuild-truncate hazard from the cap-read design's adversarial review; next unchanged.
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

> **Showcase-period priority LIFTED (2026-07-27).** The 2026-07-25 Andrew directive that gave the
> Verticals stream build priority "until the showcase is done" no longer applies: Facet's
> archetype-world rendering is shipped end-to-end except the iOS-native increment, which is blocked on
> host Xcode tooling, not on lane priority ([verticals.md](verticals.md)'s Edge showcase app row —
> "every non-iOS increment shipped"). The Lattice lane no longer caps picks at S-sized or yields the
> shared build lock on that basis — select by importance × readiness as normal (§2 above).
> **Build-ready now (2026-07-31):** the sensitive-predicate instanceOf-chain gap, the script
> live-read budget, and the **cap-read per-anchor grant keys** initiative (Fires 1-5 + both
> residuals it surfaced) are all **✅ shipped** (Done log). `[appsession] co-hosted-page session
> fixation` also shipped 2026-07-31. The whole-set-exposure row stays seq-blocked behind read-path
> auth (D1); `appsession`'s OIDC design stays 🗄️ **shelved** (revive: first real-IdP deployment).
> The read-posture-comments doc sweep shipped 2026-07-31 (Done log); its one `docs/contracts/*`
> site (`10-orchestration-substrate.md:249`) is fixed **uncommitted in `main`**, flagged for Andrew
> (non-substantive — aligns a stray cross-reference with Contract #2 §2.5's already-ratified text).
> The step6 instanceOf-chain live-read accounting pass also shipped 2026-07-31 (Done log). No other
> ready security/trust-boundary item remains — the rest are seq-blocked or shelved (above).
> Every `✅ ratified` row is done or driver-blocked; the rest are Whetstone's or parking-lot.
> A stale callout starves the lane — whoever ships next renames this.
>
> 📎 **Refractor is drained.** All seven buildable rows shipped 2026-07-25 against
> [refractor-open-rows-fire-briefs.md](../../implementation-artifacts/refractor-open-rows-fire-briefs.md);
> the two freshly-filed cap-read residuals above and the HA-NATS-blocked rollup are what remain.

### Security & trust boundary
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Processor] Whole-set `state` exposure remains an existence oracle for sensitive classes** | A guard keyed on consumption still splits on a surplus sensitive declared read when the script takes a whole-set exposure (`items()`/`values()`/rendering `state`) — the flip is correct, so only read-scope validation of the declared set closes it. | ★ | S | 🚧 seq behind read-path auth (D1) · [design §2.2](../../implementation-artifacts/sensitive-read-tracker-consumption-design.md) · no live victim (no package script does it) |
| **[appsession] The production IdP posture cannot open a session** | `setCookie` runs only under a non-nil `Signer`, so with `_JWT_PUBLIC_KEY`/`_ISSUER` set nothing can issue the cookie — the verify-only posture is unreachable (401 everywhere), and `/api/session/refresh` 404s so every FE write path dies with it. Design: the kit becomes the OIDC code-flow RP. | ★★ | L | 🗄️ shelved (revive: first real-IdP deployment) · ✅ design Andrew-ratified 2026-07-25 · [design](../../implementation-artifacts/appsession-oidc-production-signin-design.md) |
| **NATS write restriction — Fire 4 (production mTLS)** | Fires 1–3 closed the fabricated-KV-write surface at the account level; the remaining fire binds subject permissions to client certificates instead of NKeys, which only matters off the dev stack. | ★ now / ★★ prod | M | 🗄️ shelved (revive: production deployment) · [design](../../implementation-artifacts/nats-account-write-restriction-design.md) §Fire-3-status |
| **Keyed identity-index hashes (HMAC)** | Unkeyed `sha256NanoID` contact hashes are dictionary-testable with substrate access and persist in JetStream history post-shred; a Vault-keyed HMAC bounds it but needs a MAC primitive + key custody at every hash computer, and must migrate ALL index consumers (identityindex, provision probe, dedup) in one stroke. | ★ now / ★★ prod | M | 🗄️ shelved (revive: production threat model) · [analysis](../../implementation-artifacts/dedup-over-encrypted-pii-design.md) §9.1/§10-C |

### Orchestration & edge — Loupe-routed (2026-07-25 PO pass)
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|

### Privacy / Vault
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|

### External-I/O maturity (bridge follow-ons)
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **Bridge — real vendor adapters** | The async result-return path ships; every adapter behind it is still a `Fake*`. Replacing them needs real vendor credentials + a production destination, so it waits on one. | ★ now / ★★★ prod | M–L | 🗄️ shelved (revive: a real external destination) |

### Scale-out
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| Multi-cell / sharding | Graph scales by **cells** (root + subgraph co-located for atomic writes); global adjacency index + bridge links for cross-cell. | ★ now / ★★★ at scale | XL | ✅ ratified · [design](../../implementation-artifacts/multi-cell-sharding-design.md) · 🚧 seq (prod-scale driver) |
| **Global identity for a hyperscale tenant** | A hyperscale tenant (WeWork) spans cells/regions — cross-cell shadows + cross-region residency on top of multi-cell. | ★ now / ★★★ at hyperscale | L–XL | ✅ ratified (2026-07-16) · 🚧 Andrew-gated: DO NOT BUILD until further notice (does NOT auto-clear on multi-cell Fire 2 / a driver) · [design](../../implementation-artifacts/global-identity-hyperscale-tenant-design.md) |
| **HA NATS clustering** | Single-server today; clustering + multi-instance engine fan-out. | ★ now / ★★ prod | M–L | ✅ ratified · [design](../../implementation-artifacts/ha-nats-clustering-design.md) · 🚧 shelved (prod-HA driver) |

### Edge & personal lenses
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **Personal Lens — multicast fan-out dedup** | Fires 1–5 shipped and PL.6's WS half is subsumed by EDGE.5; what remains is deduping identical per-identity deltas across subscribers, which only pays back at subscriber counts no cell has yet. | ★ | M | 🗄️ shelved (revive: a bandwidth trigger) · [design](../../implementation-artifacts/personal-secure-lens-design.md) |

### AI-native
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **AI-authored capabilities — Fire 5 (auto-apply)** | Fires 1–4 ship the propose→validate→human-review→apply loop; Fire 5 would let a high-confidence proposal apply without a human verdict. Design-only by Andrew's decision. | ★★ | M | 🚧 Andrew-gated (design-only) · [design](../../implementation-artifacts/ai-authored-capabilities-design.md) |
| **The Augur — Fire 3 (autoApply)** | Fires 1+2a+2b close the escalate→review→dispatch loop with a human verdict in it; Fire 3 removes that verdict for high-confidence remediations. | ★★ | M | 🚧 Andrew-gated · [design](../../implementation-artifacts/augur-design.md) + [dispatch](../../implementation-artifacts/augur-dispatch-pickup-design.md) |
| **Weaver planner — Fire 9 AI tail** | The deterministic planner ships and drives LoftSpace renewals; the tail hands a gap the planner cannot solve to the Augur. Renewals never produce one, so it needs a genuinely novel gap to build against. | ★★ | M | 🗄️ shelved (revive: a gap the planner cannot solve) · [design](../../implementation-artifacts/weaver-planner-mandate-design.md) |

### Read-model / projection maturity
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **OpenSearch target adapter** | A third lens target adapter beside NATS-KV and Postgres. The Postgres FTS interim already serves the one search consumer, so the adapter itself still has none. | ★ | M | 🗄️ shelved (ratified, no consumer) · [design](../../implementation-artifacts/search-target-adapter-design.md) |
| **[Refractor] Cross-instance projection-latency rollup** | Aggregate per-lens projection latency across Refractor instances into one per-component view (single-instance today, so per-instance == per-component). Link-tombstone re-projection half **subsumed** by the link-aspect reprojection design. | ★ | S | 🚧 seq behind HA-NATS multi-instance · [link-aspect design](../../implementation-artifacts/link-aspect-triggered-reprojection-plain-lenses-design.md) subsumes the tombstone half; no multi-instance consumer yet |

### Refinements & ops
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Refractor] The sweep-heal e2e polls the row, then asserts the counter** | `TestRefractor_ConvergenceSweep_DetectsAndHealsLostProjection_E2E` waits for the healed doc in KV then requires `Reconciled >= 1`; write and counter are separate steps, so under load the row lands first and the assert reads 0 — as it also would if CDC re-projection healed it, not the sweep. Tighten, never loosen. | ★★ | S | 📋 ready · owner: Whetstone · CI run 30182951177, green on re-run |
| **Embedded-NATS shard flakes under parallel load** | Two different embedded-NATS tests failed on CI runners on consecutive days (`TestLaneSpecs_PerLaneBacklogIsolation` unit-1; `TestPersonalLens_PL2_E2E_InterestSetFiltersThenAdmits` unit-2); both post-date the per-test-server parallelization. Local repro: `go test ./...` with NO `-p` cap reddens 3 other embedded-NATS tests that pass 3x in isolation and under CI's `-p 4`. Root-cause per the flake rule: tighten, never loosen. | ★★ | M | 📋 ready · owner: Whetstone |
| **CI pipeline speed (continuous)** | Make CI faster without weakening any gate — owned continuously by the **Whetstone**. Matrix split done (serial → 4 parallel jobs); convergence + unit parallelized; unit itself now sharded across 2 runners. | ★★ | M (ongoing) | 🏗️ continuous (Whetstone) · aggregate-CPU ceiling confirmed 2x, isolating natsperm into its own step reconfirmed it (Done log) · next: propose paid larger runners to Andrew |
| **Hard-delete mutation verb (true link/aspect keyspace reclaim)** | Mutation vocab is create/update/tombstone (soft PUTs); a tombstoned key persists + is still enumerated by `kv.Links`. A 4th `delete` verb (NATS `DEL`) lets dead links leave the keyspace, bounding `kv.Links` LIST cost. | ★ | M | 🗄️ shelved (Andrew 2026-07-02) · [design + hold banner](../../implementation-artifacts/hard-delete-mutation-verb-design.md) · demand dissolved by clinic write-path slot claims; §3 edits reverted; revive only on a real reclaim driver |
| **Script-read posture — Fire 3 (Processor-side guards)** | Fires 1–2 + the debt sweep + the warn→block flip ship. Fire 3 makes a guarded step a generic Processor-side operation feature, superseding Loom's engine read; no op needs one yet. | ★★ | M | 🗄️ shelved (revive: the first guarded-step consumer) · [design §12](../../implementation-artifacts/script-read-posture-design.md) |

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

- 2026-07-31 · `cc1bddc8` · [Processor] step6 instanceOf on-demand reads (connInstanceOfReader, connVertexClassReader) now charge the shared live-read budget, closing the accounting gap kv.Read/kv.Links already had
- 2026-07-31 · `fbc783ca` · [packages] ~19 read-posture comments no longer claim hydration-time fatality — HydrationMiss is deferred to first touch (Contract #2 §2.5), doc-only
- 2026-07-31 · `b4aae06f` · [Refractor] a PerEntry cap-read lens on a non-prefix-listing adapter is now refused at activation, not at first shred [design](../../implementation-artifacts/cap-read-per-anchor-grant-keys-design.md)
- 2026-07-31 · `4e3fd70c` · [appsession] dev-minted session tokens now carry an app-scoped `aud` claim — closes the co-hosted-page cookie-fixation gap across all 5 adopters
- 2026-07-31 · stale row closed — multi-hat scope=any/self ordering was already fixed `4790992b` (2026-07-25, LoftSpace landlord-hat fire); `TestCapabilityAuthorizer_DualScope_*` proves it, no build needed
- 2026-07-31 · `cbd0f244` · [Processor] tombstone-with-document now rejected, not warned — Fire 2 flip, swept 14 packages/* sites Fire 1 missed [design §6](../../implementation-artifacts/tombstone-body-preservation-design.md)
- 2026-07-30 · `098423e0` · [Substrate] consumer reopen retry now backs off exponentially with jitter (100ms→5s cap) instead of a fixed 100ms interval, so a sustained outage doesn't hammer the server every consumer on a connection retries against
- 2026-07-30 · `646d1ac1` · [Refractor] lens activation + hot-reload now retry a transient NATS blip (adapter build, audit-stream ensure) instead of permanently stranding the lens
- 2026-07-30 · `7381ace2` · [Pkgmgr] op-meta tombstone now refuses an undeclared drop and cancels open referents when RetireCancelsOpenTasks is declared [design](../../implementation-artifacts/opmeta-retirement-open-task-guard-design.md)
- 2026-07-30 · `4e85358c` · [Lattice-CLI] `health summary`'s refractor/processor rows now escalate on issues[] — a live LensRegistryIncomplete sat "green" for 2.5h
- 2026-07-30 · `e384f34a` · [Refractor] deactivated lens's frozen Health KV entry no longer pins rollup yellow — isLensDeleted probe drops the row
- 2026-07-30 · `e8349678` · [Loom] per-instance redrive — resumes a FAILED instance at its recorded cursor, never restarts; unblocks Loupe Flows "act on it"
- 2026-07-30 · `b9b9cad3` · [Refractor,Substrate] SYNC stream now caps per-subject retention (`MaxMsgsPerSubject: 10,000`) — finishes the retention posture [design §3.2](../../implementation-artifacts/personal-secure-lens-design.md)
- 2026-07-30 · `2edba1f3` · [Refractor,Edge,Loupe] operator-initiated device hydration request — durable per-device flag consumed on next SYNC attach [§3.2](../../implementation-artifacts/loupe-flows-edge-depth-ux.md)
- 2026-07-30 · `b097f1a4` · [Refractor,Loupe] cold bring-up replay debt no longer reads as staleness — a draining `consumerLag` renders green until its `lagProgressAt` clock stalls 2min, closing the demo box's hours-long false YELLOW
- 2026-07-30 · `a7284b8a` · [Processor] wall-budget test flake — `PROCESSOR_SCRIPT_WALL_MS` now wired + widened for `-p 4` test runs, production 250ms NFR-P4 default untouched
- 2026-07-30 · `3deda37c` · [Refractor] Postgres GrantTable cap-read producers now shred-nullified — closes Fire 4's first residual [design §Fire 5](../../implementation-artifacts/cap-read-per-anchor-grant-keys-design.md)
- 2026-07-30 · `da1eb1b6` · [Refractor] actor enumeration now hops `reportsTo` once (directional, non-recursive) so a manager's `cap.ephemeral.*` refreshes on a report's task mutation, matching capabilityEphemeral's fixed 2-hop cypher
- 2026-07-30 · `7628070e` · [Bootstrap] VerifyKernel now compares kernel content, not just presence/shape — closes the gap where `bootstrap verify` (and the `up` target's reuse short-circuit) passed a stale kernel clean
- 2026-07-30 · `60778733` · [Refractor] a Personal lens's KeyColumns now threads onto every compiled branch, not just branch 0 — fixes the live `key field "ns" absent from keys map` write failures (edgeCatalog/edgeTasks/edgeEntitySessions)
- 2026-07-30 · `a43f9fcf` · [Refractor] a walk-owned column's own multi-row fan-out (multi-hat actor, 2+ roles reaching one op) now resolves deterministically per owner branch instead of refusing the merge
- 2026-07-30 · `de806ee4` · [Refractor] an accepted MATCH hot-reload now triggers Pipeline.Rebuild, reprojecting the existing corpus instead of only future events
- 2026-07-29 · `9d5e2348` · [Refractor] shared-keyspace composition (d) — tasks merged into one multi-walk lens, verified live [design §17](../../implementation-artifacts/refractor-shared-keyspace-arbitration-design.md)
- 2026-07-29 · `4186479a` · [Refractor] shared-keyspace composition (c) — catalog merged into one multi-walk lens, classifier gap fixed, verified live [design §16](../../implementation-artifacts/refractor-shared-keyspace-arbitration-design.md)
- 2026-07-29 · `bcf52101` · [Refractor] shared-keyspace composition (b) — sessions merged into one multi-walk lens, verified live [design §15](../../implementation-artifacts/refractor-shared-keyspace-arbitration-design.md)
- 2026-07-29 · `69119818` · [Refractor] shared-keyspace composition (a) — N branches per multi-walk lens, merged by key [design §14](../../implementation-artifacts/refractor-shared-keyspace-arbitration-design.md)
- 2026-07-29 · `c80bfa00` · [Refractor] evaluation-consistency Fire 1 Inc 2 — conjunct-unit classifier + selector-scoped footprint, fan-in stress test [design §14](../../implementation-artifacts/refractor-evaluation-consistency-design.md)
- 2026-07-29 · `2177c60d` · [Bootstrap] the 5 aspect-type meta roots now carry `data.protected: true`, closing the commit-time guard gap [kernel-seed-reconcile-design](../../implementation-artifacts/kernel-seed-reconcile-design.md) §5 named
- 2026-07-28 · `3aa45a5a` · [Refractor] every package-generated cap-read.* NATS-KV producer is now shred-nullified — dynamic TargetLister discovers by declared descriptor field, static base-lens floor keeps a boot-window regression closed
- 2026-07-28 · `533a0b71` · [Edge] hydrationComplete boot-gate now matches the hydrate RPC's own target revision, not the first (possibly stale-replayed) marker seen
- 2026-07-28 · `6c720482` · [chronicler,orchestration-base] eventStream ColumnMapping gains ClearOn — a Loom re-dispatch's patternStarted no longer carries the prior run's ended_at/failure_reason onto the new running row
- 2026-07-28 · `c08c28be` · [Processor] sensitive predicate now covers instanceOf-chained classes; pkgmgr rejects Sensitive on a non-aspectType DDL, closing the link/event gap by construction
- *(older entries rolled to [archive/lattice-done.md](archive/lattice-done.md); newest rolled entry `ea3f3852`)*
