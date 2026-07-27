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
| **[Bootstrap] 5 aspect-type meta roots carry no `protected` flag** | `seedAspectTypeMeta` builds them with empty data while every other kernel root sets `protected: true`, so the step-8 guard does not cover them — a package upgrade/uninstall could update or tombstone a kernel DDL. | ★★ | XS | 📋 ready · [why](../../implementation-artifacts/kernel-seed-reconcile-design.md) §5 |
| **[Bootstrap] `bootstrap verify` reports a stale kernel as fresh** | It asserts presence + envelope shape, never content, so `make up`'s reuse short-circuit prints "kernel already up" over DDL scripts this binary no longer builds. `KernelDrift` now makes the assertion cheap; wiring it in makes `make up` self-healing. | ★★ | S | 📋 ready · [why](../../implementation-artifacts/kernel-seed-reconcile-design.md) §8 |
| **[Bootstrap] Reconcile creates + updates but never removes a retired kernel key** | A kernel entity the current binary no longer builds survives forever in an old bucket; needs an authoritative kernel-owned key enumeration separable from package-written `vtx.meta.*`. | ★ | S–M | 📋 ready · [why](../../implementation-artifacts/kernel-seed-reconcile-design.md) §8 |
| **[Refractor] Cold bring-up replay debt reads as hours of lens lag** | After a fresh-world bring-up every lens + capability reported `consumerLag` ≈ activation-to-head distance (~2000–2500) while every read model was verifiably complete (seeded rows present; manifests, worklists, authz all live). The measured consumer drains ~50 msg/min, so the hosted Loupe wears YELLOW "80 degraded" for hours after each nightly reset — a false staleness alarm. Ground which consumer the gauge reads vs the path that projected. | ★★ | M | 📋 ready · demo-box evidence in the filing commit |
| **[Refractor] A lens spec change re-compiles but never re-projects** | `ClassifyUpdate` returns `MatchChange` ("a full rebuild is required"), but `reload.go` only calls `UseFullEngine` — `Pipeline.Rebuild`'s one caller is the operator control RPC, which pkgmgr never invokes, and a plain nats-kv lens gets no convergence sweep. Rows projected before a lens gained a column keep the old shape until an unrelated CDC event re-derives them. Surfaced by wellness 0.13.0, whose read boundary reads that column (fails closed ⇒ presents as denial). | ★★ | M | 📋 ready |
| **[Refractor] Does a lens evaluation need a point-in-time snapshot?** | The evaluation read-memo makes reads repeatable **per key**, so an anchor can no longer split into two rows. It does not pin reads **across** keys: each key is first-read at its own instant, so one row can still blend two moments (stale `unitStatus` + fresh `outcome`). That is a single torn row — no guard rejects it and the next CDC event re-derives it. **Prove a torn row is reachable AND that some consumer acts on it** before designing revision-pinned reads. | ★★ | M | 📋 designer · prove necessity first |
| **[Refractor] A live claim's own consumer grant never projects into Capability KV** | `ClaimIdentity`'s R2 grant lands in Core KV (verified) but `cap.roles.<target>` never appears — no DLQ, absent past a completed sweep. First-ever-live unconditioned atomic-batch member (`step8_commit.go:191-215`); ground NATS 2.14 atomic-batch semantics before fixing. Repro `make test-claim-ceremony`. | ★★★ | M | 🚧 needs root-cause · [facet-staff-worlds §13.1](../../implementation-artifacts/facet-staff-worlds-design.md) |
| **[Loom] Guardless-step recovery check-before-act probe** | On total `loom-state` loss + a re-triggered `StartLoomPattern`, a fresh instance replays guards from cursor 0 (re-runs an already-applied guarded step). | ★ | S–M | 🗄️ shelved-backup (Andrew: no new engine Core-KV reads) |
| **[Processor] Tombstone-with-document warn→reject flip (Fire 2)** | Fire 1 (emitter sweep + parser warn) shipped `6b68fde4`; flip the warn to a reject once warn sightings are clean (stale stored scripts clear via world recreation). | ★★ | XS | 🚧 seq behind clean warn-window · [design](../../implementation-artifacts/tombstone-body-preservation-design.md) §6 · stale stored scripts now clear via `make reseed-kernel` |

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

> 🎯 **Showcase-period priority (Andrew, 2026-07-25): the Verticals stream has build priority** — the
> showcase (Facet rendering every archetype world correctly) is the standing goal, and this lane keeps
> winning the shared build lock over it. Until the showcase is done: prefer S-sized picks, start no M+
> build when Verticals has work queued, and yield the lock.
> **Build-ready now** (within that): the
> **script live-read budget** (★★ M, the bigger sibling of the shipped envelope ceiling), the
> **MergeIdentity dead collision check** (★★ S–M), then the two ★ Processor sensitive-predicate
> rows. Both 2026-07-25 ratifications are
> 🗄️ **shelved with named revives** (cap-read → showcase completion; appsession → first real-IdP
> deployment) — the Steward does not select them.
> Every `✅ ratified` row is done or driver-blocked; the rest are Whetstone's or parking-lot.
> A stale callout starves the lane — whoever ships next renames this.
>
> 📎 **Refractor is drained.** All seven buildable rows shipped 2026-07-25 against
> [refractor-open-rows-fire-briefs.md](../../implementation-artifacts/refractor-open-rows-fire-briefs.md);
> the two that remain are the shelved cap-read row and the HA-NATS-blocked rollup.

### Security & trust boundary
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Processor] Whole-set `state` exposure remains an existence oracle for sensitive classes** | A guard keyed on consumption still splits on a surplus sensitive declared read when the script takes a whole-set exposure (`items()`/`values()`/rendering `state`) — the flip is correct, so only read-scope validation of the declared set closes it. | ★ | S | 🚧 seq behind read-path auth (D1) · [design §2.2](../../implementation-artifacts/sensitive-read-tracker-consumption-design.md) · no live victim (no package script does it) |
| **[appsession] A co-hosted page can plant a session cookie (fixation)** | Cookies ignore port, so a sibling localhost app's page can `document.cookie` an ABSENT session cookie (HttpOnly blocks overwrite, not create) and the shared dev key makes it verify — the victim browses as an attacker-chosen identity. The origin gate cannot reach it (no request made); `__Host-` or a cookie-bound token closes it. | ★ | S–M | 📋 ready · dev/demo only (shared key) · [kit](../../../docs/components/appsession.md) |
| **[Processor] The sensitive predicate misses instanceOf-chained classes and links entirely** | Both the encrypt (step 6.5) and decrypt-on-read paths resolve `sensitive` by exact `DDLs.Lookup`, not step 6's `instanceOf` chain walk; and step 6 gates the sensitive write-scope on `KindAspect`, so a `Sensitive: true` **link** class is never rejected, never encrypted, and `kv.Links` never applies the read disposition to it. | ★ | S–M | 📋 ready · no live victim (every shipped sensitive DDL registers under its exact name; no sensitive link class) |
| **[Processor] A script's live reads have no budget** | Class-(e) `kv.Links` paging + one `kv.Read` per link is uncapped at the Processor: `identity_has_open_tasks` alone walks 64 pages × 256 links, so one MergeIdentity can issue ~16k sequential Core-KV GETs — ~16x the declared-read ceiling, which never sees a live read. `connKVReader.ReadVertex` has no budget. | ★★ | M | 📋 ready · consumer: any actor able to submit an op whose script enumerates |
| **[identity-hygiene] MergeIdentity's link-collision check can never fire** | `state[new_key]` tests the primary-rewritten link key, but the dispatcher declares only the secondary's edges — each carries `secondary_id` in the rewritten position, so `new_key` is never in `state` and a colliding `create` reaches step 8. Bites when both identities hold the same relation to one vertex (e.g. a shared role). Declaring the rewritten keys doubles the read set. | ★★ | S–M | 📋 ready · consumer: any merge of two identities sharing a relation |
| **[packages] ~20 read-posture comments assert hydration-time fatality** | `packages/*` DDL comments + two READMEs still say a declared-but-absent read faults "before the script runs" (identity-domain, service-domain, privacy-base, objects-base, orchestration-base, clinic/loftspace READMEs), as does `docs/contracts/10-orchestration-substrate.md:238`. Doc-only sweep. | ★ | S | 📋 ready |
| **Starlark 250ms wall budget fails installs under parallel test load** | `go test ./...` at default `-p` reds a different package-install test each run with `ScriptTimeout: script exceeded wall budget 250ms` — reproduced on unmodified `main`, so it predates any one fire. Costs every fire an investigation to rule out its own change. | ★★ | S–M | 📋 ready |
| **[Refractor] A `cap-read` document has no size bound** | Even deduped, an actor reaching enough distinct anchors renders `cap-read.<domain>.<actor>` past NATS's max payload; the write then fails permanently, freezing that actor's grant set so revocations stop landing (fail-OPEN). Design: per-anchor keys (the Postgres per-row twin). | ★★ | L | 🗄️ shelved (revive: showcase completion) · ✅ design Andrew-ratified 2026-07-25 (Option A) · [design](../../implementation-artifacts/cap-read-per-anchor-grant-keys-design.md) · §6.13/§6.14 contract edit committed |
| **[appsession] The production IdP posture cannot open a session** | `setCookie` runs only under a non-nil `Signer`, so with `_JWT_PUBLIC_KEY`/`_ISSUER` set nothing can issue the cookie — the verify-only posture is unreachable (401 everywhere), and `/api/session/refresh` 404s so every FE write path dies with it. Design: the kit becomes the OIDC code-flow RP. | ★★ | L | 🗄️ shelved (revive: first real-IdP deployment) · ✅ design Andrew-ratified 2026-07-25 · [design](../../implementation-artifacts/appsession-oidc-production-signin-design.md) |
| **Multi-hat `scope=any`+`scope=self` first-match over-confines** | `matchPlatformPermission` returns on the first operationType match regardless of scope, and `capabilityRoles` collects roles unordered — so a consumer+staff identity (e.g. seed-showcase `seedSamMultiHat`) can authorize their OWN cafe tab as scope=any, losing the self exemption. Fail-closed; bites a multi-hat who works and lives in different buildings. | ★ | S–M | 📋 ready · no live victim (showcase multi-hat has no leaseapp) |
| **NATS write restriction — Fire 4 (production mTLS)** | Fires 1–3 closed the fabricated-KV-write surface at the account level; the remaining fire binds subject permissions to client certificates instead of NKeys, which only matters off the dev stack. | ★ now / ★★ prod | M | 🗄️ shelved (revive: production deployment) · [design](../../implementation-artifacts/nats-account-write-restriction-design.md) §Fire-3-status |
| **Keyed identity-index hashes (HMAC)** | Unkeyed `sha256NanoID` contact hashes are dictionary-testable with substrate access and persist in JetStream history post-shred; a Vault-keyed HMAC bounds it but needs a MAC primitive + key custody at every hash computer, and must migrate ALL index consumers (identityindex, provision probe, dedup) in one stroke. | ★ now / ★★ prod | M | 🗄️ shelved (revive: production threat model) · [analysis](../../implementation-artifacts/dedup-over-encrypted-pii-design.md) §9.1/§10-C |

### Orchestration & edge — Loupe-routed (2026-07-25 PO pass)
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[orchestration-base] A re-dispatched flow's history row never clears its terminal** | The `eventStream` projection merges each event onto the stored row and cannot say "this event clears this column", so a re-dispatch under Weaver's STABLE instanceId sets status=running over a row keeping the previous run's `ended_at` (10 live rows read ended-BEFORE-started) and its `failure_reason`. | ★★★ | S–M | 📋 ready · consumer: Loupe Flows · [why](../../implementation-artifacts/loupe-flows-edge-depth-ux.md) §1.2 |
| **[Loom] No per-instance redrive** | `failed` is terminal, `RetryCount` only counts it, pause/resume are consumer-scoped (and the relay/deadline consumers are refused outright), and `StartLoomPattern` is idempotent on instanceId — so a failed flow cannot be resumed or safely re-run. Needs the double-execution question answered by design: resume at cursor, or restart under a new id with the old tombstoned. | ★★ | M | 📋 ready · consumer: Loupe Flows "act on it" · [why](../../implementation-artifacts/loupe-flows-edge-depth-ux.md) §2.2 |
| **[Personal Lens] No operator-initiated device hydration** | A gapped device is fixed by a warm resume, but edge nodes cannot self-report and no connection state is observable, so nothing can push one. A durable per-device hydration flag, consumed on the device's next SYNC attach, is the only shape the asymmetry allows. | ★★ | M | 📋 ready · consumer: Loupe Edge fleet · [why](../../implementation-artifacts/loupe-flows-edge-depth-ux.md) §3.2 |

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

- 2026-07-27 · `8981a8b0` · [CI] lease-convergence drain budget — the 3 15s outliers join the suite's 30s convention; the chain converged microseconds late on CI, and the window ceiling the old comment claimed was measured from the wrong instant
- 2026-07-27 · `e8d78278` · [Refractor] evaluation-scoped read memo — one cypher run observes one value per key, so a commit landing mid-evaluation can no longer split an anchor into two rows and drop the projection
- 2026-07-26 · — · [Packages] Conformance-sweep row closes as overtaken — Standard Inc 1–6 (verticals) drained it: `s1Debt`/`s6Debt` empty, 29 pkgs, no exemptions; `readTemplateDebt` (2) has its own verticals row
- 2026-07-26 · `6cacb337` · [CI] embedded-server ctor sweep — 97 `RunServer` fixtures routed through `natsfixture`, ctor gate now anchors both construction routes; net -480 lines
- 2026-07-26 · `f9a86e45` · [CI] bare-connect sweep — 84 embedded dials routed through `natsfixture.Connect`, the 6 that must fail fast declared via `// nats-connect:`; linter hook-mode path bug fixed
- 2026-07-26 · `7ac54ce1` · [CI] embedded-NATS handshake flake root-caused — 42 hand-rolled fixtures each inherited nats.go's single-shot 2s handshake deadline; `internal/natsfixture` owns it, lint blocks regressions, no assertion loosened
- 2026-07-26 · `0a409757` · [bootstrap] kernel-seed reconcile — a seeded Core KV picks up kernel-DDL fixes instead of freezing at the binary that seeded it; verify-kernel now asserts content
- 2026-07-25 · `1a1379f7` · [CI] Refractor sweep-count flake root-caused — the test read a per-pass aggregate mid-pass; 4-in-6 failing → 10/10 green, no assertion loosened
- 2026-07-25 · `a0a4bb34` · [pkgmgr,refractor] an upgrade that cannot take effect says so where the operator is — `reloadpin` predicts the refusal at apply time; `ReactivationRequired` + drift guard

- 2026-07-25 · `e5268c2f` · [refractor] a business lens heals its own hole and leaves its neighbours alone — real-substrate e2e: enrolled, scoped, healed, siblings pinned by revision

- 2026-07-25 · `7f183d69` · [refractor] a rebuild owns the signal the stall detector deferred to — outstanding + last-decreased published; a wedged rebuild escalates, a draining one stays exempt

- 2026-07-25 · `33a6cc61` · [refractor] the sweep lists roots at the substrate and pays for what it examines — `vtx.<type>.*` filter + budgeted anchorLive walk, cursor keeps the tail reachable

- 2026-07-25 · `4de52240` · [refractor] the un-truncatable rebuild is the grant table's repair, and now says so — premise disproven: absent rows re-derive through the ON CONFLICT arm; warning corrected

- 2026-07-25 · `90d79ff8` · [refractor] a rebuild truncates what the lens owns, not the bucket it borrows — prefix-scoped `Truncate` bound to the rule, closing the shared-bucket auth wipe

- 2026-07-25 · `043608a5` · [processor] a declared read set is bounded, and a repeated key is one read — summed `MaxDeclaredReads` ceiling at the envelope + `distinctKeys` in all three hydration loops; Contract #2 §2.5 edit staged uncommitted

- 2026-07-25 · `34b13ffd` · [refractor] every actor-aggregate lens gets the healer, gated on what it can prove it owns — ownership-scoped listing + three-part install gate; business verdicts warning-only

- 2026-07-25 · `298ef8ed` · [refractor] the hot-reload path decides in the open, and says so — refusal set extracted + testable, Output/grantTable/protected pinned (guard sources closed), refusals recorded on health

- 2026-07-25 · `f630efc3` · [refractor] the grant family's guard is real, so the pipeline is told about it — `SeqGuarded` on the read-grant adapter closes an unordered seq-0 grant INSERT on the adj-watch path

- 2026-07-25 · `8400efd7` · [refractor] the projection-write guard belongs to the lens, not to one adapter instance — rule-derived + re-applied on every build; guarded lens pinned to its target surface

- 2026-07-25 · `82f52fc4` · [refractor] a reconciliation write the guard drops is not a heal — absent-row seq-0 upsert refused where the guard binds; unguarded targets still create

- 2026-07-25 · `7e6030aa` · [refractor] the sweep's prefilter directions are hints that earn their share, not assumptions about the lens — both hints rotate + earn their budget; unstarves the only orphan detector

- 2026-07-25 · `a5210fb2` · [refractor] a business lens whose liveness cannot be read says so instead of vanishing — `LensProjectionUnreadable` + `projectionLag: null`, mirroring the auth-plane fix

- 2026-07-25 · `94ce0950` · [lint,pkgmgr] the Vertical Package Standard is enforced by a gate, not by prose — `lint-package-standard` (S1/S6/S7) blocking in CI + shrink-only debt baseline; corpus single-sourced in `internal/pkgregistry`

- 2026-07-25 · `831b0da9` · [refractor] a sweep that verifies nothing reports that, not the last pass's verdict — `CapabilitySweepStalled` (staleness clock + suppression cause) + `CapabilityLensUnreadable` (never dropped)

- 2026-07-25 · `3b0798c8` · [refractor] the sweep reports what it could not REPAIR, not only what it healed — `CapabilityRepairFailing` + `failingActors` gauge, slot-yielding per-actor backoff, departed-actor reap

- 2026-07-25 · `1b9852f2` · [refractor] DISTINCT binds on the aggregator that carries it, not the RETURN item — composed `collect(DISTINCT)+collect(DISTINCT)` deduped; unfroze a 1MB-over-payload `cap-read` doc; `normalizeForKey` made injective
- 2026-07-25 · `fbf46f9a` · [scripts] verify-package-loftspace-domain asserts each (op, scope) grant — one id per operationType let map iteration hide `SetListingStatus`'s second (landlord scope=self) vertex, red-or-green at random
- 2026-07-25 · `1a0d1849` · [appsession,loupe] the origin gate is `OriginGate`, usable without a session Manager — nested-navigation (iframe) clickjacking refused, Loupe's ~110-line fork deleted
- 2026-07-25 · `8e61174f` · [appsession,demo] a request must prove it came from the app's own origin — Fetch-Metadata + Origin gate at the RequireSession choke point, `_PUBLIC_ORIGIN` + demo wiring, kit component page
- 2026-07-25 · `2fff6e40` · [starlarksandbox] a script gets no host output channel — `print` discarded, so `print(state)` no longer renders decrypted plaintext to the Processor's stderr
- 2026-07-25 · `ae05f60a` · [processor] the external-egress guard fires on what the script CONSUMED, not on what hydration decrypted — closes the sensitive-class oracle; whole-set exposure records, `state` attrs default-deny
- 2026-07-25 · `3a78c109` · [processor] a declared read faults where the operation NAMES the key, not at hydration — closes the pre-script Core-KV existence oracle; enumeration deliberately does not fault
- 2026-07-24 · `10f01e71` · [lint,clinic-domain] a declaration binds to its own statement, not the next 8 lines (12 drifted sites fixed); clinic keys its exemption on `op.authTargetValidated`, `(legacy-self-exempt)` deleted

- *(older entries rolled to [archive/lattice-done.md](archive/lattice-done.md); includes `94c8224` hello-lattice NFR-P3 flake fix)*
