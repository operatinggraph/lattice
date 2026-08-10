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
  step**. Nothing else. **The whole FILE is capped too** — compact rows when it bites.
- **The fire's narrative goes in the COMMIT MESSAGE + the design doc — NEVER the board** (the CLAUDE.md
  no-changelog rule). Never in a cell: design rationale / fork-resolution / "why I chose this", adversarial
  findings, the fire-by-fire journal, SHAs-with-prose, coverage %, review depth, "Was: …". A multi-fire
  checkpoint (worktree · done · next) lives in the **design doc**; the row carries a one-line pointer.
  **The four ways this regressed after the 2026-06-29 reform — refuse each by name:** design summary in
  State (✓ `🏗️ building · [design](…) · next: Inc 1 series-state lens`); blocked-reasoning essay
  (✓ `🚧 blocked-on Vault (PII projection) · [why](design)`); survey-log fire-journal (✓ `2026-06-30
  Refractor — healthy; filed 2`); multi-sentence Done-log entry (✓ `date · SHA · [tag] title`).
- **Capped sections** (the lint enforces): **Survey-log / PO-notes ≤ 12 dated one-liners** — rotation memory
  only (what was surveyed/exercised, what's next), never a per-fire log; **Done-log ≤ 25 one-liners**, older
  roll to `archive/`. **Shipped (✅ built) items leave the feature tables** → a one-line Done-log entry.
- **Scales.** Imp: ★ low · ★★ medium · ★★★ high. Size: XS · S · M · L · XL.
- **State tokens.** 📋 ready · 🏗️ building (worktree) · 📐 awaiting-Andrew (design ratification) ·
  ✅ ratified (design signed off, not yet built) · 🚧 blocked (Andrew-gated, or `seq:`/`blocked-on:` another
  item) · 🎯 top-priority pick · 🗄️ shelved-backup · 🔭 flag-for-Andrew.

## Loupe → its own lane

Loupe (`cmd/loupe`) is Stream 3, on **[loupe.md](loupe.md)** (own build lock). Loupe rows do not live here;
a platform primitive Loupe needs still files HERE per the cross-lane rules.

## Component maintenance

Open items only (shipped ones are in the Done log). Grouped by component tag.

| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Bootstrap] Reconcile creates + updates but never removes a retired kernel key** | A kernel entity the current binary does not build stays live and executable: a dispatchable DDL, a running lens pipeline, a held canonicalName. No wipe-free shrink path. | ★ | S–M | ✅ ratified 2026-08-06 (Inc 1; fork → retire verb) · [design](../../implementation-artifacts/kernel-orphan-retirement-design.md) · Inc 2 gated on Inc 1's census |
| **[Tooling] `verify-claim-ceremony.go` asserts a 5s SLA the platform never promised** | `waitForRoleGrant`'s 5s deadline reads real unbounded latency as "never appears": 4/5 live demo-box runs failed it while every grant landed minutes later. Poll to convergence. | ★ | XS | 📋 ready |
| **[Refractor] Post-claim auth-grant latency is unbounded by design** | Auth-plane actorAggregate lenses intake the broad Core-KV stream, so grant visibility is queue-position luck. Inc 0–3 bounded it (`5d60fb2b`); Inc 4 finishes the narrowing — coverage, the pre-install guard, the healer's proof, and the re-claim 500. | ★★ | L | 🏗️ building · [design](../../implementation-artifacts/auth-plane-projection-latency-design.md) §18 · next: Inc 4a coverage |
| **[Refractor] A staging `WITH`'s carried accumulators are stringified into the grouping key per row** | `projectItems` renormalizes every non-aggregating item per binding row, so a generated producer's stage *k* re-renders anchor maps an earlier stage's grouping already fixed. | ★★ | S–M | ✅ ratified 2026-08-06 (Winston, delegated) · [design](../../implementation-artifacts/full-engine-grouping-key-reduction-design.md) · unblocked |
| **[Pkgmgr] `validateGrantSliceVarNames` cannot see a variable inside a node property map** | `patternVarNames` reports pattern variables only; the chain parser skips a `{...}` property map wholesale, so `(bk:booking {slice: grantSlice0})` is emitted verbatim. | ★ | XS–S | 📋 ready · record property-map vars at parse time |
| **[Refractor] Sibling OPTIONAL branches multiply instead of folding** | `applyMatch` builds the full `[]binding` cross product; the 1M-row cap then permits ~730 MB. 14 hand-authored lenses have 2–8 independent branches in one stage; producer staging covered only generated ones. | ★★ | L | ✅ ratified 2026-08-06 (Winston, delegated; fork → engine) · [design](../../implementation-artifacts/full-engine-independent-branch-decomposition-design.md) · Inc 2 first |
| **[Refractor] The CDC write path audits a retraction the ordering guard declined** | `writeResults`' delete arm never consults an outcome, so `writeAudit` publishes a `delete` fact for a revocation the §6.2 guard dropped. `UpsertOutcome.Wrote` stays true on decline, so `DeleteWithOutcome` alone makes the arms disagree. | ★ | S | 📋 ready · consumer: the audit log's account of revocations · fork: decide both arms together |
| **[Refractor] A key destruction's relevance oracle reads the lens NOW** | Both halves answer from the current `secureColumns[].holderTypes`, so narrowing a lens's declared holders puts its pre-upgrade ciphertext outside every later destruction — and the empty target set attests vacuously. | ★★ | M | 📋 ready · consumer: the first lens whose holder types narrow · [why](../../implementation-artifacts/retention-class-key-custody-design.md) §24.6 |
| **[Refractor] Lens health is liveness-only — a frozen row renders green** | 12 orphanedTaskGrants rows sat 12 days stale behind a green lens card: status/lag/error health cannot see per-row wrongness. | ★★ | M | 📋 ready · [design](../../implementation-artifacts/lens-projection-divergence-audit-design.md) · next: Fire 2 plain-lens Auditor |
| **[Edge] An orphan a purge cannot reap has no server-side backstop** | A revoked credential fails the sign-out reap (the auth callout refuses its connection) and a crashed host never reaps — each strands a durable no client can name again. `InactiveThreshold` is the shape; the shell's 30 min will not port. | ★★ | S–M | ✅ ratified 2026-08-06 · [design](../../implementation-artifacts/edge-sync-orphan-expiry-design.md) |
| **[Facet] Two concurrent first-time `Acquire`s for one identity race the mirror** | `engineManager.Acquire` releases `m.mu` before `newEngine`, so both callers open the same bbolt mirror; the loser fails `ErrTimeout` after 2s (`store.Open`'s bounded lock) rather than hanging, but a request still 500s. | ★★ | S | 📋 ready · consumer: a first sign-in whose SSE attach and first write land together · serialize the build per identity |
| **[Edge] A cold sign-in replays the actor's retained history, not their world** | Measured live 2026-07-31: 2,049 frames for a 14-key world (146×), `ready` at 33s. One subject `lattice.sync.user.<id>` carries every key's every revision, consumed `DeliverAll` (`substrate/consumer.go:142`). | ★★ | M | ✅ ratified 2026-08-06 (fork → reposition) · [design](../../implementation-artifacts/edge-cold-signin-delivery-position-design.md) |
| **[Pkgmgr] A capability apply may `upgradeExisting` into a platform package** | `CapabilityApplyPlanForProposal` excludes no package, so an approved proposal naming `capability-author`/`orchestration-base` diff-applies a one-artifact Definition into it. | ★ | S | 📋 ready · `internal/pkgmgr/capabilityapply.go:100-118` |
| **[Refractor] A structural pause is terminal even where its own probe could settle it** | The tier means "pause until reconciled" but nothing re-checks: `waitWhilePaused` blocks on the operator across restarts, while `VerifyProtectedTable` already adjudicates that condition on a loop for *infra* pauses. | ★★ | M | ✅ ratified 2026-08-06 · [design](../../implementation-artifacts/structural-pause-recovery-design.md) §4.2 (Inc 2) |
| **[Tooling] The G2 derived-key gate does not cover `internal/` submitters** | `internal/gateway/whoami.go` re-implements identity-domain's email normalization and derives both index keys; `internal/objectmanager` derives too. The gate excludes `internal/` wholesale because that tree also OWNS the primitive. | ★ | S | 📋 ready · [why](../../implementation-artifacts/client-ceremony-op-descriptors-design.md) §12.7 |
| **[Facet] A durably-queued ceremony write outlives the plaintext it minted** | The reveal is held in memory only, so a reload or sign-out while the intent sits in the offline queue drops the secret — the write still lands on the next drain, arming an identity nobody can claim, with no signal anywhere. | ★★ | S–M | 📋 ready · consumer: staff creating an identity offline |
| **[Refractor] A lens cannot project a relationship's own `data` or name** | `RelPattern.Variable` is parsed (`full/visitor.go:274`) but `traverseRel` binds only the neighbour node, so `b.data.x` / `type(b)` are silent nulls. Consumer: `objectAttachments`, which cannot project the linkName `DetachObject` needs. | ★★ | M | ✅ ratified 2026-08-06 (Winston, delegated) · [design](../../implementation-artifacts/relationship-data-projection-design.md) · narrow bind only |
| **[Processor] `derive_reads` binds `state`/`ddl` to empty dicts rather than failing closed** | `kv` and `nanoid` are fail-closed stubs; `state[k]` returns a silent `None` where `kv.Read` errors loudly. Within Contract #2 §2.5, but the purity argument's weakest link. | ★ | S | 📋 ready · [why](../../implementation-artifacts/client-ceremony-op-descriptors-design.md) §12.7 |
| **[Loom/Weaver] A dispatcher cannot declare its op's class-(e) enumerations** | A `kv.Links` walk is declared through `ContextHint.Enumerations`, expressible by neither Loom `systemOp` submit nor Weaver `directOp` (`GapActionSpec` has no field). | ★ | XS–S | 📋 ready · consumers: `identityErasure`; `identityErasureComplete` · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 3 res 1, inc 7 res 4 |
| **[identity-domain] An erased identity's live index vertex denies the next person their own** | A registrant sharing the email of a sealed — or merely key-shredded — identity hits that still-live `identityindex`, so they get no index vertex and no `duplicateOf`. Nothing sweeps it. | ★★ | S | 📋 ready · consumer: the second walk-in on a shredded contact · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 11 res 1 |
| **[Processor] A class-(e) enumeration has no budget of its own — the 250ms wall binds first** | `kv.Links` costs one KVGet per key and re-lists per page, so the wall binds far below any declared link budget: erasure dies `ScriptTimeout`, and ~19 hops sink self-pay live. | ★★★ | M | 📋 ready · consumers: self-pay ([verticals.md](verticals.md)) · erasure [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 6 res 1–2 |
| **[identity-domain] A `credentialindex` with no `boundTo` link is residue nothing can walk** | It carries its identity in its body with no link to it, so no enumeration reaches one; the sweep covers only those with a `boundTo`. §9.2(i) names the class; the attestation does not. | ★ | S | 📋 ready · consumer: the erasure attestation's coverage claim · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 6 res 3 |
| **[Weaver] A gap whose re-dispatch IS the progress advances once per mark lease** | A paged loop's own reprojection lands while its mark's 30-min lease is live, so `fireEpisode` takes the anti-storm drop and only the reconciler's reclaim re-dispatches: 64 links/30 min. | ★★ | M–L | 📋 ready · needs design (engine seam) · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 7 res 1 |
| **[privacy-base] A stuck erasure sweep re-dispatches indefinitely with no escalation** | `maxretries_<g>` is ruled out (inert at the sweeps' scale, terminally parking at the seal's); a `surface` gap cannot fire either — a hard-failing sweep commits nothing, so "stuck" is history no row holds. | ★★ | S–M | 📋 ready · consumer: the operator surface (§12 step 4) · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 7 res 2 |
| **[Weaver] A `surface` gap's Health issue carries no entity segment** | `issueKeyGap` keys per `(target, column)`, so with two erasures in flight the subject whose halves land first clears the issue raised for the stuck one. Wrong per-subject. | ★ | S | 📋 ready · consumer: `identityErasureComplete`'s async-half gaps · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 7 res 3 |
| **[privacy-base] A pre-narrowing shredded subject earns a clean attestation over live `credentialindex` rows** | That shred tombstoned `boundTo` and left each `vtx.credentialindex.<hash>` standing, so the sweep finds no live link, all five residue arms read zero, and the seal attests `violating=false` over N live rows. | ★★ | S–M | 📋 ready · consumer: the attestation over any such shred · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 9 |
| **[privacy-base] `identityErasure` has never run end to end on a live stack** | Every arm is proven in package tests and the submit path by Weaver's dispatch, but no subject has gone through all four steps on a running stack — and a real run destroys a key. | ★★ | S | 📋 ready · consumer: the first real erasure · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 11 res 2 |
| **[privacy-base] A merge concurrent with erasure step 1 still wedges the subject** | The §6 piiKey gate refuses any merge submitted after step 1 commits, so only the race survives: a `MergeIdentity` hydrating its gate before step 1 and landing after holds no OCC on `piiKey`. Key destroyed, step 2 refuses `IdentityMerged` forever. | ★ | S | 📋 ready · consumer: `identityErasure` step 1 · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 9–10 |
| **[Tooling] privacy-base has no `verify-package-*` target** | It owns the erasure spine (pattern, weaver target, residue lens, seal, 6 DDLs) and asserts none of it after a diff-apply; a bad install surfaces only when an erasure stalls. | ★ | S | 📋 ready · consumer: the next privacy-base diff-apply · mirror `verify-package-identity` |
| **[Processor] Step 6.5 `continue`s past what it cannot adjudicate** | Two plaintext-commit arms: an empty `class` (`step65_encrypt.go:60`; step 6's gate at `step6_validate.go:156`), and a cache-missing DDL with no live-read fault — `if !ok \|\| !ref.Sensitive` (`:83`), reachable via `Refresh` warn-and-skip. | ★★ | S–M | 📋 ready · consumer SHIPPED: clinic `.encounter` PHI · [why](../../implementation-artifacts/retention-class-key-custody-design.md) §26.3 |
| **[Pkgmgr] A renamed or uninstalled retention class strands its DEK beyond any shred** | The holder id salts from the class canonicalName and `<holder>.piiKey` is not in `declaredKeys`: a rename mints a new holder while old ciphertexts name the old one; uninstall tombstones it, leaving the key undestroyable. | ★★ | S–M | 📋 ready · consumers LIVE: clinic `clinicalRecord`, lease-signing `underwritingRecord` · [why](../../implementation-artifacts/retention-class-key-custody-design.md) §14 |
| **[Pkgmgr] A plain `Columns` entry can still collide with a platform-reserved name** | Only `SecureColumns` is checked against the four reserved RLS columns (`authz_anchors`/`projection_seq`/`is_deleted`/`deleted_at`); an ordinary `Columns` entry with one of those names installs and fails only at Postgres activation (42701 duplicate column). | ★ | XS | 📋 ready · consumer: the next package declaring a plain column named one of the four |
| **[Refractor] `dispositionEvalErr` has no `CatPrivacyCritical` arm** | The category is defined and classified (`classify.go:149`) but `pipeline.go:2116-2138` routes it to the default `Nak`. Unreachable today — it is only ever constructed inside `keyshredded`, which handles its own pause and never returns it up the evaluation path. | ★ | XS–S | 📋 ready · consumer: the first caller wrapping an evaluation-path error as `PrivacyCritical` |
| **[Pkgmgr] The manifest verifier skips retention classes** | `ManifestBlock` (`manifest.go:133-152`) compares DDLs/lenses/permissions/weaverTargets/loomPatterns/opMetas/panes; `RetentionClasses` has no field and no comparison, so a package mints a `vtx.retentionclass` holder its manifest never declares. | ★ | XS–S | 📋 ready · consumers: clinic `clinicalRecord`, lease-signing `underwritingRecord` · mirror the opMetas block |
| **[Pkgmgr] A lens's node labels are never extracted from its spec, and a walk parser refuses `*`** | The installer checks a lens spec parses (`capabilitymaterializer.go:791`) but never reads its labels, so `K` is unavailable and §10.2's cap refusal is uncomputable. The walk parser also refuses a trailing `*` in any node position (`anchorwalk.go:735`) — fails closed. | ★★ | M | 📋 ready · consumers: §10.2's cap refusal · an authoring label lint (needs a key-type vocabulary) · [why](../../implementation-artifacts/dynamic-type-taxonomy-design.md) §17.10 |
| **[Refractor] A node's whole adjacency list is one KV value, so a high-in-degree node cannot be indexed** | `AdjValue.Edges` holds every edge at `adj.<nodeId>`, and a type meta is the target of an `instanceOf` link from every instance — so the write exceeds NATS max payload and link fan-out fails for every lens traversing it. | ★★★ | S–M | ✅ ratified 2026-08-10 (fork → B overflow-mark; C shelved) · [design](../../implementation-artifacts/adjacency-per-edge-index-design.md) §15 · seq: after multi_last primitive |
| **[Edge] The `SYNC` stream has no byte limit, only a 24h age** | `max_bytes=-1`, `max_age=24h` — measured 997 MB / 1.07M msgs in that window, the largest contributor to a 1.6 GB store on a host where `lattice-nats` is being OOM-killed. The cold-sign-in design repositions delivery; it does not bound the stream. | ★★ | S–M | 📋 ready · consumer: the dev stack's survivability · set a `max_bytes` |
| **[Refractor] The taxonomy resolver's `armed` flag asserts more than its consumer can back** | Three shapes: `SetArmed(true)` fires on the first replayed event, so a `*` lens activating mid-replay narrows against a partial taxonomy; a NATS `RECONNECTING` leaves it answering `StatusArmed` blind; and a restart inside an unresolved window makes activation refuse the lens, so its rows stay AND revocations stop applying. | ★★ | M | 📋 ready · consumer: Fire B's first `:location*` lens · [why](../../implementation-artifacts/dynamic-type-taxonomy-design.md) §17.8 |
| **[Processor] The meta/op reserved-name gate is pkgmgr-only, not Processor-enforced** | Contract #1 §1.2's commit-time gate isn't implemented there; only pkgmgr's install-time check runs today. | ★★ | S–M | 📋 ready · consumer: Contract #1 §1.2's stated gate · [why](../../implementation-artifacts/dynamic-type-taxonomy-design.md) §17.2 |
| **[Pkgmgr] The abstract-flip guard is vacuous when canonicalName ≠ key segment** | `checkAbstractNoLiveInstances` scans `vtx.<canonicalName>.`; a differently-keyed type (`workOrder`→`vtx.wo.<id>`) scans empty and the flip proceeds unguarded. | ★★ | S | 📋 ready · consumer: §3.4's no-live-instances guarantee · [why](../../implementation-artifacts/dynamic-type-taxonomy-design.md) §17.9 |
| **[Refractor] A behavior-frozen consolidation pass — 105K LOC at ×1.8 in 39 days** | test:prod ≈1:1, `pipeline.go` ≈3.2K, `executor.go` ≈2.1K; fold test scaffolding, split god-files — LOC + CI time down, suite green. | ★★ | M–L | 📋 ready · owner-driven · behavior-frozen · first target: pipeline.go + test-corpus overlap |
| **[Tooling] No gate enforces `gofmt`, and four files in one package have already drifted** | No gofmt step in CI, in golangci's enabled set, or in the Makefile — drift is invisible until someone runs `gofmt -l` by hand. `internal/refractor/pipeline` alone carries four unformatted files today. | ★ | XS | 📋 ready · consumer: any fire whose mechanical edit leaves a file unformatted and no gate says so |

### Survey log (round-robin rotation)

Rotation memory only — findings are the filed rows; fire narratives live in commits, never here.
Components: Core · Weaver · Loom · Refractor · Bootstrap · object-store-manager (+ the cross-cutting feature
backlog). Survey the stalest (`git log -1 --format=%ct -- <path>`), note ONE dated line, rotate.

- 2026-07-18 Refractor (healthy; all 8 07-06-review findings already resolved — no new rows).
- 2026-07-19 object-store-manager (67.5/91.4% cov; filed doc-drift + cascade error-branch cov).
- 2026-07-19 Bootstrap (69.3% cov; filed stale-bootstrap-json-no-freshness-probe ★★ + seed-idempotency-branch-cov).
- 2026-07-19 Core (processor 81.8/substrate 76.2% cov; filed 3: supervisor-accessors, outbox-consumer cov, processor.md drift).
- 2026-07-25 Refractor (out of rotation) — filed shared-bucket rebuild-truncate hazard; next unchanged.
- **Next:** Weaver.

## Lattice feature backlog — the Phase-3 build queue

The flywheel draws from this list (Surveyor files → Designer designs → Steward builds the ratified).
Everything here needs design and is fair game **except** 🚧 Andrew-gated rows. Architectural **forks**
(Gateway, read-path auth, Vault, multi-cell, HA-NATS) and **frozen-contract** changes are designed-through,
but the *fork decision* + the *contract commit* are Andrew's.


### Security & trust boundary
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[rbac] Any `operator` can self-grant the verb no package grants them** | `CreatePermission` takes `operationType` as a free string with no allow-list, and it and `GrantPermission` are `scope:any` to operator — so "ships no grant" (the shred verbs) is a default, not a boundary: two ops reach an irreversible destruction. Needs a never-self-grantable set. | ★★ | M | 📋 ready · consumer: the destruction verbs' claimed posture · `packages/rbac-domain/ddls.go:306`,`:392` |
| **[natsperm] `$JS.API.>` lets any component delete a durable or purge core-events** | `protectedStreamDenies` covers registered KV buckets only and `core-events` is a plain stream, so a vertical app (denied `ops.>`) can delete either shred worker's durable or purge the stream — pending destructions suppressed silently, shreds committed and never executed. | ★★ | M | 📋 ready · consumer: both crypto-shred workers · `internal/natsperm/matrix.go:56-70`,`:119-125` |
| **[lease-signing] A payload-named identity aspect is read with no ownership guard** | `CreateLeaseServiceInstance` takes `subjectKey` and (via `resolve_subject_params`) the aspect segment from the payload, checked for shape + liveness only (`scripts.go:1168`, `Scope:"any"`). Step 6's guard is external-plane only — deriving PII into an ordinary domain event is unguarded. | ★★ | S | 📋 ready · guard the op + an authoring gate for the class, one fire |
| **[Loom] An externalTask can only declare its SUBJECT's own aspects for egress** | `inferExternalTaskReads` parses `subject.<aspect>` only (`externaltask_params.go:42`), so a LINKED vertex's field is undeclarable in `egressReads` and the commit guard rejects it plaintext (`step6_validate.go:110`) — a vendor call needing a neighbour's sensitive field renders blank. | ★★ | M | 📋 ready · needs a link-hop template form |

### External-I/O maturity (bridge follow-ons)
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|

### Scale-out
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| Multi-cell / sharding | Graph scales by **cells** (root + subgraph co-located for atomic writes); global adjacency index + bridge links. | ★ now / ★★★ at scale | XL | ✅ ratified · [design](../../implementation-artifacts/multi-cell-sharding-design.md) · 🚧 seq (prod-scale driver) |
| **Global identity for a hyperscale tenant** | A hyperscale tenant spans cells/regions — cross-cell shadows + cross-region residency on top of multi-cell. | ★ now / ★★★ at hyperscale | L–XL | ✅ ratified (2026-07-16) · 🚧 Andrew-gated: DO NOT BUILD until further notice (does NOT auto-clear on multi-cell Fire 2 / a driver) · [design](../../implementation-artifacts/global-identity-hyperscale-tenant-design.md) |
| **HA NATS clustering** | Single-server today; clustering + multi-instance engine fan-out. | ★ now / ★★ prod | M–L | ✅ ratified · [design](../../implementation-artifacts/ha-nats-clustering-design.md) · 🚧 shelved (prod-HA driver) |

### Edge & personal lenses
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|

### AI-native
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **AI-authored capabilities — Fire 5 (auto-apply)** | Fires 1–4 ship the propose→validate→human-review→apply loop; Fire 5 would apply a high-confidence proposal with no human verdict. Design-only by Andrew. | ★★ | M | 🚧 Andrew-gated (design-only) · [design](../../implementation-artifacts/ai-authored-capabilities-design.md) |
| **The Augur — Fire 3 (autoApply)** | Fires 1+2a+2b close the escalate→review→dispatch loop with a human verdict in it; Fire 3 removes it for high-confidence remediations. | ★★ | M | 🚧 Andrew-gated · [design](../../implementation-artifacts/augur-design.md) + [dispatch](../../implementation-artifacts/augur-dispatch-pickup-design.md) |

### Read-model / projection maturity
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Substrate] Multi-subject direct get (`multi_last`) as a read primitive** | One locked, consumer-less round trip returns last-per-subject for a key list or filters (≤1,024 subjects; 413 above). Measured ~31µs/key vs 153µs sequential gets; 4× the ephemeral lister on the same set. Consumers: step-4 hydration (atomic read-set), the ~12-file `ListKeysPrefix`-class corpus. | ★★★ | S–M | ✅ ratified 2026-08-10 (Andrew-directed) · [spike + protocol](../../implementation-artifacts/adjacency-per-edge-index-design.md) §14 · builds first |
| **Dynamic type taxonomy — an abstract type a lens can label** | `subtypeOf` links between type metas, resolved to a leaf-label set at activation, so a leaf any package declares is picked up by lenses writing `:abstract*`. Recovers the polymorphism the label-binding fire removes; first consumer `capabilityServiceAccess`. | ★★★ | L | 🏗️ building (Fire B) · [design](../../implementation-artifacts/dynamic-type-taxonomy-design.md) §14 · next: B0 rebuild scheduler |
| **[Refractor] Cross-instance projection-latency rollup** | Aggregate per-lens projection latency across Refractor instances into one per-component view; single-instance today, so the two coincide. | ★ | S | 🚧 seq behind HA-NATS multi-instance · tombstone half subsumed by the [link-aspect design](../../implementation-artifacts/link-aspect-triggered-reprojection-plain-lenses-design.md) |

### Refinements & ops
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **`internal/refractor`'s claim-ceremony e2e family is non-deterministic at head** | Membership rotates over the three claim-ceremony e2es: `cap.roles.<target>` never gains the role-derived grant from the real `ClaimIdentity` op's `holdsRole` write within 25s. The test never waits for the adjacency edge the reprojection walks (the sweep e2e beside it does), so a lens pass can precede it. | ★★ | S–M | 📋 ready · owner: Whetstone · no repro on a quiet 4-core box · tighten, never loosen · [why](../../implementation-artifacts/retention-class-key-custody-design.md) §19.5 |
| **Suite reddens under parallel load, in packages the change never touched** | Rotating membership — `packages/*` together, then `TestLaneSpecs_PerLaneBacklogIsolation` (unit-1) and `TestPersonalLens_PL2_E2E_InterestSetFiltersThenAdmits` (unit-2) on consecutive days. One mechanism found: a test dialling production `substrate.Connect` inherits nats.go's 2s no-retry handshake, bypassing the `natsfixture` hardening. Processor tree converted; ~45 sites remain. | ★★★ | M | 🏗️ processor tree done · next: convert `cmd/loupe`+`cmd/facet`+`cmd/lattice/*` |
| **`TestRefractor_E2E_P99` gates an absolute latency SLO on a shared runner** | NFR-P3's 500ms p99 is asserted by a unit test measuring wall-clock projection latency while three other jobs contend for the runner: CI run 31288862556 measured `10.03s`. A shared runner promises no latency floor, so the gate reads contention, not regression. | ★★ | S | 📋 ready · owner: Whetstone · reshape the measurement or move it off shared CI |
| **CI pipeline speed (continuous)** | Make CI faster without weakening any gate — owned continuously by the **Whetstone**. Matrix split done (serial → 4 parallel jobs); convergence + unit parallelized; unit itself now sharded across 2 runners. | ★★ | M (ongoing) | 🏗️ continuous (Whetstone) · aggregate-CPU ceiling confirmed 2x · next: propose paid larger runners to Andrew |
| **[Processor] A RevisionConflict on an UNDECLARED key names nothing** | NATS omits the failing subject, so `ConflictError.ConflictingKey` is always empty and `conflictKeyForSignal` rebuilds it only from *declared* defaulted/absent-create keys (`commit_path.go:520`) — a submitter MISSING a `contextHint` declaration gets `conflictingKey:""` plus a raw `wrong last sequence`. Found driving Café. | ★★ | M | 📋 ready |

### Parking lot — very low priority (far, far back)

Rolled to [archive/lattice-parked.md](archive/lattice-parked.md) — real but low-value; no design or build
effort without an Andrew greenlight. A row that acquires a real driver comes back here.

## Done log — lattice (newest first)

One line per shipped item (`date · SHA · [tag] title`). Oldest roll to `archive/` past ~25.

- 2026-08-10 · `4b846e82` · [Weaver] a gap's external class is read from its pattern's step kinds, not its action name — the timed-out vendor call retries again, and its legs are gated (§10.3 ratified)
- 2026-08-10 · `a951b258` · [Substrate] `CredsFile` is pre-flighted like `NKeySeedFile` already was, closing the retry-loop asymmetry review found
- 2026-08-10 · `d592a2c8` · [Substrate] `Connect`'s initial NATS handshake gets its own timeout + bounded, ctx-aware retry budget that skips permanent auth failures
- 2026-08-09 · `59933666` · [Pkgmgr] secure-column guard reserves all four platform RLS columns, matching Refractor's activation-time check
- 2026-08-09 · `e2f01a16` · [CI] Whetstone — four flakes root-caused to tests depending on what they do not control: the Starlark wall budget, ack redelivery, Weaver's sweep cadence, and the NATS handshake
- *(older rolled to [archive/lattice-done.md](archive/lattice-done.md); newest `b71be85d`)*
