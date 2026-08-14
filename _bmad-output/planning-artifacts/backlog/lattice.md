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
| **[Bootstrap] Reconcile creates + updates but never removes a retired kernel key** | A kernel entity the current binary does not build stays live and executable: a dispatchable DDL, a running lens pipeline, a held canonicalName. No wipe-free shrink path. | ★ | S–M | 🗄️ shelved (Inc 2 retire verb) · [design](../../implementation-artifacts/kernel-orphan-retirement-design.md) · Inc 1 detector shipped, census 0/0 both buckets; needs a binary-version floor |
| **[Pkgmgr] A package upgrade silently un-tombstones a deliberately revoked grant** | `RevokePermission` tombstones a grant link the package still declares, so the **surviving-key** arm (`upgrade.go:324-331`) sees `docLink`'s `isDeleted:false` (`build.go:1006`) differ from the tombstone and emits an `update`; step 8's update arm has no aliveness guard (`step8_commit.go:435-439`). | ★★ | S–M | 📋 ready · not the re-add arm `:298-315`, that revive is intended · blocks [grant-provenance](../../implementation-artifacts/grant-provenance-runtime-permission-minting-design.md) Inc 1 |
| **[Processor] The root actor set is a boot snapshot of a live topology** | Fork A made root = `holdsRole → operator` (live), but `ClassAwarePlatformKey` closes over a boot-time `SystemActorKeys` scan and four binaries snapshot independently: a newly-granted operator is unread until restart and the binaries can disagree. Revocation is fail-closed, so this is latency + consistency, not over-grant. | ★ | S–M | 📋 ready · [why](../../implementation-artifacts/grant-provenance-runtime-permission-minting-design.md) §3.3 |
| **[Docs] Four sites still call the root actor set "kernel-seeded"; Fork A made it role-derived** | `capabilitykv/keys.go:47-56`, `cmd/processor/main.go:138-140` (+ refractor/weaver/loom), and Contract #6 §6.1's "bounded to the kernel-seeded root actors" all describe a population the predicate does not enforce. Stale post-Fork-A prose. | ★ | XS | 📋 ready · sent a designer fire down a wrong path 2026-08-11 |
| **[Pkgmgr] No live-vs-declared reconciliation for permission vertices** | `VerifyAgainstDefinition` compares manifest-to-Go, never Core KV, so a permission vertex no manifest declares is invisible to every gate. Branch A ratified keeps the runtime channel, so drift exists — but Inc 3's `origin` stamp makes entries self-describing, leaving the reconciler an auditor convenience. | ★ | S | 📋 ready · seq: behind grant-provenance Inc 3 · [why](../../implementation-artifacts/grant-provenance-runtime-permission-minting-design.md) §1.1 |
| **[Tooling] `verify-claim-ceremony.go` asserts a 5s SLA the platform never promised** | `waitForRoleGrant`'s 5s deadline reads real unbounded latency as "never appears": 4/5 live demo-box runs failed it while every grant landed minutes later. Poll to convergence. | ★ | XS | 📋 ready |
| **[Processor] A derived `reads` can harden a floored key the envelope never declared** | The §2.5 floor applies to the envelope, not the merged set, so `derive_reads` output is outside it. Latent — both `derive_reads` in `packages/` return only `{}`/`optionalReads` on all seven paths — and outside the clause's literal scope, which binds the submitter. | ★ | S | 📋 ready · consumer: the first `derive_reads` returning a `reads` key · [why](../../implementation-artifacts/auth-plane-projection-latency-design.md) §19.7 |
| **[Processor] §2.5's floor silently skips the key templates it cannot resolve** | `resolveDescriptorFloor` applies no floor for `{me.*}`/`{entity.*}` and only logs Warn, so those keys stay hardenable by a hand-rolled `contextHint`. The clause reads unconditional; enforcement is not. Live: cafe-domain's `Charge`/`Settle` declare exactly that shape. | ★★ | M | 📋 ready · consumer LIVE: cafe-domain `Charge`,`Settle` · needs server-side me/entity resolution |
| **[Refractor] A personal row goes stale when its own D1 grant flips** | `personalEnvelopeFn` gates each row on `cap-read.*`, which lives in Capability KV, not Core KV — so no CDC event reaches the personal pipeline when a grant lands or retracts, and personal lenses get no sweep plan. A revoked grant leaving a row live is the over-grant direction. | ★★ | M | ✅ ratified (2026-08-13) · low priority · one fire, Inc 1→2 · [design](../../implementation-artifacts/personal-lens-grant-change-trigger-design.md) |
| **[Refractor] Two clause shapes the full engine accepts and silently miscompiles** | An **anonymous** required `MATCH` binding no NEW variable is dropped rather than filtered; the named-relationship form now filters correctly. `WITH *` reaches the AST as an empty projection list, so `projectItems` rebuilds each row empty and a later pattern re-seeds by whole-bucket scan. | ★★ | M | 📐 needs designer pass — refuse-at-parse vs implement is a clause-semantics fork, corpus-wide blast radius · 0 live lenses use either shape |
| **[wellness-ledger] `wellnessMemberAccounts` has no retraction transport** | Plain lens, no `Output` descriptor, no `EmptyBehavior`, no `DiffRetraction`; `AnchorProjectionKey` returns `ok=false` for any query carrying a `WITH`. A member whose last booking is deleted keeps their row forever. Its three settlement siblings do declare `DiffRetraction`. | ★★ | S | 📐 needs designer pass — turning retraction ON is the known mass-Delete hazard; `EmptyBehavior` must be decided with it · not auth-plane, stale-read direction |
| **[Pkgmgr] `validateGrantSliceVarNames` cannot see a variable inside a node property map** | `patternVarNames` reports pattern variables only; the chain parser skips a `{...}` property map wholesale, so `(bk:booking {slice: grantSlice0})` is emitted verbatim. | ★ | XS–S | 📋 ready · record property-map vars at parse time |
| **[Refractor] The CDC write path audits a retraction the ordering guard declined** | `writeResults`' delete arm never consults an outcome, so `writeAudit` publishes a `delete` fact for a revocation the §6.2 guard dropped. `UpsertOutcome.Wrote` stays true on decline, so `DeleteWithOutcome` alone makes the arms disagree. | ★ | S | 📋 ready · consumer: the audit log's account of revocations · fork: decide both arms together |
| **[Refractor] A key destruction's relevance oracle reads the lens NOW** | Both halves answer from the current `secureColumns[].holderTypes`, so narrowing a lens's declared holders puts its pre-upgrade ciphertext outside every later destruction — and the empty target set attests vacuously. | ★★ | M | 📋 ready · consumer: the first lens whose holder types narrow · [why](../../implementation-artifacts/retention-class-key-custody-design.md) §24.6 |
| **[Edge] A `delete` retracts one lens's attribution but tombstones the key for every lens** | `personal-lens-retraction-design.md` §3.1 defines `delete` as per-key **lens-attributed**; the envelope carries no `Lens` and `store.ApplyDelete(key, revision)` (`bolt.go:176`, `idb.go:280`) takes none, tombstoning the key outright. Same-key multi-lens overlap is established (`edgeTasks`/`edgeTasksQueued`), so one lens's retraction drops another's live row. `ApplyKeySet` has the attribution mechanism to extend. | ★★ | M | 📋 ready · consumer: any key two personal lenses project |
| **[Edge] The first-paint gate has no identity for the hydrate cycle it gates** | Two defects, one missing mechanism — a per-hydrate correlation id on the marker. `hydrationComplete` carries no `Lens`, so the gate releases on the first marker and can paint a partial world; and `personal.hydrate` is per-**identity**, so a second device's burst satisfies this device's gate. The client-side fix was built and refuted. | ★★ | M | 📐 needs designer pass · [why](../../implementation-artifacts/edge-cold-signin-delivery-position-design.md) §6 Fire 4 |
| **[Facet] Two concurrent first-time `Acquire`s for one identity race the mirror** | `engineManager.Acquire` releases `m.mu` before `newEngine`, so both callers open the same bbolt mirror; the loser fails `ErrTimeout` after 2s rather than hanging, but a request still 500s. `newEngine` starts `runSyncLoop` before returning, so the loser also attaches and deletes the winner's durable before `Close()`. | ★★ | S | 📋 ready · consumer: a first sign-in whose SSE attach and first write land together · serialize the build per identity |
| **[Pkgmgr] A capability apply may `upgradeExisting` into a platform package** | `CapabilityApplyPlanForProposal` excludes no package, so an approved proposal naming `capability-author`/`orchestration-base` diff-applies a one-artifact Definition into it. | ★ | S | 📋 ready · `internal/pkgmgr/capabilityapply.go:100-118` |
| **[Tooling] The G2 derived-key gate does not cover `internal/` submitters** | `internal/gateway/whoami.go` re-implements identity-domain's email normalization and derives both index keys; `internal/objectmanager` derives too. The gate excludes `internal/` wholesale because that tree also OWNS the primitive. | ★ | S | 📋 ready · [why](../../implementation-artifacts/client-ceremony-op-descriptors-design.md) §12.7 |
| **[Facet] A durably-queued ceremony write outlives the plaintext it minted** | The reveal is held in memory only, so a reload or sign-out while the intent sits in the offline queue drops the secret — the write still lands on the next drain, arming an identity nobody can claim, with no signal anywhere. | ★★ | S–M | 📋 ready · consumer: staff creating an identity offline |
| **[Processor] `derive_reads` binds `state`/`ddl` to empty dicts rather than failing closed** | `kv` and `nanoid` are fail-closed stubs; `state[k]` returns a silent `None` where `kv.Read` errors loudly. Within Contract #2 §2.5, but the purity argument's weakest link. | ★ | S | 📋 ready · [why](../../implementation-artifacts/client-ceremony-op-descriptors-design.md) §12.7 |
| **[Loom/Weaver] A dispatcher cannot declare its op's class-(e) enumerations** | A `kv.Links` walk is declared through `ContextHint.Enumerations`, expressible by neither Loom `systemOp` submit nor Weaver `directOp` (`GapActionSpec` has no field). | ★ | XS–S | 📋 ready · consumers: `identityErasure`; `identityErasureComplete` · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 3 res 1, inc 7 res 4 |
| **[identity-domain] An erased identity's live index vertex denies the next person their own** | A registrant sharing the email of a sealed — or merely key-shredded — identity hits that still-live `identityindex`, so they get no index vertex and no `duplicateOf`. Nothing sweeps it. | ★★ | S | 📋 ready · consumer: the second walk-in on a shredded contact · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 11 res 1 |
| **[identity-domain] A `credentialindex` with no `boundTo` link is residue nothing can walk** | It carries its identity in its body with no link to it, so no enumeration reaches one; the sweep covers only those with a `boundTo`. §9.2(i) names the class; the attestation does not. | ★ | S | 📋 ready · consumer: the erasure attestation's coverage claim · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 6 res 3 |
| **[Weaver] A gap whose re-dispatch IS the progress advances once per mark lease** | A paged loop's own reprojection lands while its mark's 30-min lease is live, so `fireEpisode` takes the anti-storm drop and only the reconciler's reclaim re-dispatches: 64 links/30 min. | ★★ | M–L | 📋 ready · needs design (engine seam) · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 7 res 1 |
| **[privacy-base] A stuck erasure sweep re-dispatches indefinitely with no escalation** | `maxretries_<g>` is ruled out (inert at the sweeps' scale, terminally parking at the seal's); a `surface` gap cannot fire either — a hard-failing sweep commits nothing, so "stuck" is history no row holds. | ★★ | S–M | 📋 ready · consumer: the operator surface (§12 step 4) · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 7 res 2 |
| **[Weaver] A `surface` gap's Health issue carries no entity segment** | `issueKeyGap` keys per `(target, column)`, so with two erasures in flight the subject whose halves land first clears the issue raised for the stuck one. Wrong per-subject. | ★ | S | 📋 ready · consumer: `identityErasureComplete`'s async-half gaps · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 7 res 3 |
| **[privacy-base] A pre-narrowing shredded subject earns a clean attestation over live `credentialindex` rows** | That shred tombstoned `boundTo` and left each `vtx.credentialindex.<hash>` standing, so the sweep finds no live link, all five residue arms read zero, and the seal attests `violating=false` over N live rows. | ★★ | S–M | 📋 ready · consumer: the attestation over any such shred · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 9 |
| **[privacy-base] `identityErasure` has never run end to end on a live stack** | Every arm is proven in package tests and the submit path by Weaver's dispatch, but no subject has gone through all four steps on a running stack — and a real run destroys a key. | ★★ | S | 📋 ready · consumer: the first real erasure · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 11 res 2 |
| **[privacy-base] A merge concurrent with erasure step 1 still wedges the subject** | The §6 piiKey gate refuses any merge submitted after step 1 commits, so only the race survives: a `MergeIdentity` hydrating its gate before step 1 and landing after holds no OCC on `piiKey`. Key destroyed, step 2 refuses `IdentityMerged` forever. | ★ | S | 📋 ready · consumer: `identityErasure` step 1 · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 9–10 |
| **[Tooling] privacy-base has no `verify-package-*` target** | It owns the erasure spine (pattern, weaver target, residue lens, seal, 6 DDLs) and asserts none of it after a diff-apply; a bad install surfaces only when an erasure stalls. | ★ | S | 📋 ready · consumer: the next privacy-base diff-apply · mirror `verify-package-identity` |
| **[Processor] Sensitive resolution trusts a mutation's self-reported `class`, never the key's localName** | Step 6/6.5 resolve an aspect's DDL off `Document["class"]` only; nothing checks it against the key's own localName. Omitted/wrong `class` on a sensitive key commits PHI plaintext, cache healthy or not. | ★★ | M | 📐 needs designer pass · [why](../../implementation-artifacts/ddl-cache-invalidation-fault-signal-design.md) §1 |
| **[Pkgmgr] A renamed or uninstalled retention class strands its DEK beyond any shred** | The holder id salts from the class canonicalName and `<holder>.piiKey` is not in `declaredKeys`: a rename mints a new holder while old ciphertexts name the old one; uninstall tombstones it, leaving the key undestroyable. | ★★ | S–M | 📋 ready · consumers LIVE: clinic `clinicalRecord`, lease-signing `underwritingRecord` · [why](../../implementation-artifacts/retention-class-key-custody-design.md) §14 |
| **[Pkgmgr] A plain `Columns` entry can still collide with a platform-reserved name** | Only `SecureColumns` is checked against the four reserved RLS columns (`authz_anchors`/`projection_seq`/`is_deleted`/`deleted_at`); an ordinary `Columns` entry with one of those names installs and fails only at Postgres activation (42701 duplicate column). | ★ | XS | 📋 ready · consumer: the next package declaring a plain column named one of the four |
| **[Refractor] `dispositionEvalErr` has no `CatPrivacyCritical` arm** | The category is defined and classified (`classify.go:149`) but `pipeline.go:2116-2138` routes it to the default `Nak`. Unreachable today — it is only ever constructed inside `keyshredded`, which handles its own pause and never returns it up the evaluation path. | ★ | XS–S | 📋 ready · consumer: the first caller wrapping an evaluation-path error as `PrivacyCritical` |
| **[Pkgmgr] The manifest verifier skips retention classes** | `ManifestBlock` (`manifest.go:133-152`) compares DDLs/lenses/permissions/weaverTargets/loomPatterns/opMetas/panes; `RetentionClasses` has no field and no comparison, so a package mints a `vtx.retentionclass` holder its manifest never declares. | ★ | XS–S | 📋 ready · consumers: clinic `clinicalRecord`, lease-signing `underwritingRecord` · mirror the opMetas block |
| **[Edge] The `SYNC` stream has no byte limit, only a 24h age** | `max_bytes=-1`, `max_age=24h`, `max_msgs_per_subject=10000` — re-measured live 2026-08-10: 993 MB / 1.07M msgs of a 1.66 GB store. Largest by BYTES only; `REFRACTOR_AUDIT` is larger by COUNT (1.93M msgs, 62% of all) and already capped at 512 MiB with `max_msgs_per_subject=-1`. Filestore RSS tracks count, so a byte cap alone may not stop the OOM. | ★★ | S–M | 📋 ready · consumer: the dev stack's survivability · precedent `EnsureAuditStream` · `natssubject.go:132-147` |
| **[Refractor] A behavior-frozen consolidation pass — 105K LOC at ×1.8 in 39 days** | test:prod ≈1:1, `pipeline.go` ≈3.2K, `executor.go` ≈2.1K; fold test scaffolding, split god-files — LOC + CI time down, suite green. | ★★ | M–L | 📋 ready · owner-driven · behavior-frozen · first target: pipeline.go + test-corpus overlap |
| **[Tooling] No gate enforces `gofmt`, and 34 files across all three lanes have drifted** | No gofmt step in CI, in golangci's enabled set, or in the Makefile — drift is invisible until someone runs `gofmt -l` by hand. Live census 2026-08-11: **34 unformatted files** spanning `internal/*`, `cmd/loupe`, and the vertical apps. Enabling the gate means formatting all of them, so it lands cleanest when the three lanes are quiet. | ★★ | S | 📋 ready · consumer: unformatted struct literals reach `main` today, in every lane |

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
| **[rbac] A second grant channel mints capability grants outside the package plane** | `CreatePermission`/`UpdatePermission`/`GrantPermission` are `scope:any` to operator with no allow-list, so a grant no manifest records and no uninstall retracts is indistinguishable at step 3 from a package one — reaching `ShredRetentionClassKey`. Fix is provenance, not a deny-list. | ★★ | M | ✅ ratified Branch A (2026-08-13) · low priority · seq: un-tombstone row · [design](../../implementation-artifacts/grant-provenance-runtime-permission-minting-design.md) |
| **[natsperm] `$JS.API.>` lets any component delete a durable or purge core-events** | `protectedStreamDenies` covers registered KV buckets only and `core-events` is a plain stream, so a vertical app (denied `ops.>`) can delete either shred worker's durable or purge the stream — pending destructions suppressed silently, shreds committed and never executed. | ★★ | M | 📋 ready · consumer: both crypto-shred workers · `internal/natsperm/matrix.go:56-70`,`:119-125` |
| **[identity-domain] A claim rejection's LATENCY still separates already-claimed from wrong-key** | The wire shape is now identical, but a tombstoned `.claimKey` returns before `readPiiKeyEnvelope`'s KVGet and the AEAD decrypt, and the script exits earlier — so NFR-S6's generic refusal is measurable-through. The design holds itself to this standard at `ddls.go:1489`. | ★★ | M | 📋 needs designer pass — constant-time rejection, no ratified pattern to extend · [why](../../implementation-artifacts/auth-plane-projection-latency-design.md) §19.5 |
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
| **[location-domain] Retire the `LEGACY_LOCATION_CLASS` widening now the data is clean** | `LEGACY_LOCATION_CLASS = "location"` widens class guards in 7 packages (cafe/clinic/location/loftspace/maintenance/wellness-domain, service-location) for pre-flip data. The 25 legacy roots were repaired 2026-08-10 and commit-time gates stop new ones, so the widening now admits only what cannot exist. | ★★ | S | 📋 ready · unblocks the Contract #1 transitional marker · [why](../../implementation-artifacts/dynamic-type-taxonomy-design.md) §17.22 |
| **Typed relation signatures — `containedIn: location→location`** | Declare a relation's endpoint types against the taxonomy, enforced at step 6 fail-closed; a signed variable-length hop contributes its endpoint expansion rather than clearing exhaustiveness. Held 2026-08-13: the payoff shrank to 2 lenses, both convertible by a single-hop rewrite (replacement row on verticals). | ★★ | L | 🗄️ shelved (revive: an intermediate containment level, or rewrite-unreachable varlength census) · [design](../../implementation-artifacts/typed-relation-signatures-design.md) |
| **[Refractor] Cross-instance projection-latency rollup** | Aggregate per-lens projection latency across Refractor instances into one per-component view; single-instance today, so the two coincide. | ★ | S | 🚧 seq behind HA-NATS multi-instance · tombstone half subsumed by the [link-aspect design](../../implementation-artifacts/link-aspect-triggered-reprojection-plain-lenses-design.md) |

### Refinements & ops
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **`internal/refractor`'s claim-ceremony e2e family is non-deterministic at head** | Membership rotates over the three claim-ceremony e2es: `cap.roles.<target>` never gains the role-derived grant from the real `ClaimIdentity` op's `holdsRole` write within 25s. The test never waits for the adjacency edge the reprojection walks (the sweep e2e beside it does), so a lens pass can precede it. | ★★ | S–M | 📋 ready · owner: Whetstone · no repro on a quiet 4-core box · tighten, never loosen · [why](../../implementation-artifacts/retention-class-key-custody-design.md) §19.5 |
| **Suite reddens under parallel load, in packages the change never touched** | Rotating membership across unit-1, unit-2 and the convergence job. THREE mechanisms: (a) a test dialling production `substrate.Connect` inherits nats.go's 2s no-retry handshake — ~45 sites remain; (b) a starvation signature (0 lenses activated in 25s, `found=map[]`); (c) a wall-clock DEADLINE read as correctness — `TestLeaseConvergence_DrainThenAssert_SteadyState` blew 30s in CI, 8.5s locally at the same SHA. | ★★★ | M | 🏗️ owner: Whetstone · next: root-cause (b) |
| **`TestRefractor_E2E_P99` gates an absolute latency SLO on a shared runner** | NFR-P3's 500ms p99 is asserted by a unit test measuring wall-clock projection latency while three other jobs contend for the runner: CI run 31288862556 measured `10.03s`. A shared runner promises no latency floor, so the gate reads contention, not regression. | ★★ | S | 📋 ready · owner: Whetstone · reshape the measurement or move it off shared CI |
| **[Perf] Convert the ~85-site `ListKeysPrefix`/list-then-get corpus to `KVGetMulti`** | Census (live, this fire): `cmd/loupe` (12 files), the 4 P5 vertical apps (~30 sites, Verticals-owned — wear-the-other-hat or that stream's pick), `pkgmgr` installer census (10 sites), weaver/loom state scans, the rule-engine's anchor scans. Precedent: `step4_hydrate.go` + `personalinterest.IsRelevant`. | ★★ | L | 📋 ready · [census](../../implementation-artifacts/adjacency-per-edge-index-design.md) §14.7 scope-diff |
| **CI pipeline speed (continuous)** | Make CI faster without weakening any gate — owned continuously by the **Whetstone**. Matrix split done (serial → 4 parallel jobs); convergence + unit parallelized; unit sharded across 4 runners (was 3), re-balanced by measured `go test` time not LOC. | ★★ | M (ongoing) | 🏗️ continuous (Whetstone) · unit pole 196s→170s, wall-clock 197s→171s (run 31513357030) · next: unit-2/convergence/lint-build now within 8s — no single pole; further gains likely need paid runners |
| **[Processor] A RevisionConflict on an UNDECLARED key names nothing** | NATS omits the failing subject, so `ConflictError.ConflictingKey` is always empty and `conflictKeyForSignal` rebuilds it only from *declared* defaulted/absent-create keys (`commit_path.go:520`) — a submitter MISSING a `contextHint` declaration gets `conflictingKey:""` plus a raw `wrong last sequence`. Found driving Café. | ★★ | M | 📋 ready |

### Parking lot — very low priority (far, far back)

Rolled to [archive/lattice-parked.md](archive/lattice-parked.md) — real but low-value; no design or build
effort without an Andrew greenlight. A row that acquires a real driver comes back here.

## Done log — lattice (newest first)

One line per shipped item (`date · SHA · [tag] title`). Oldest roll to `archive/` past ~25.

- 2026-08-14 · `afdbc5f4` · [Processor] a degraded DDL cache no longer trusts a stale-or-chain-walked answer; empty-class arm re-filed as its own gap

- 2026-08-14 · `0a9ff629` · [Refractor] plain-lens neighbour derivation Inc 4b CLOSED — the seeded branch's multi-position gap; Inc 5 stays deferred behind its own trigger
- 2026-08-14 · `8f421d80` · [Refractor] plain-lens neighbour derivation Inc 3 — the write licence (5 conjuncts) + auth-plane threading; still shadow-only, 3 cold reviews + 1 fix round + 1 verify pass found zero live defects
- 2026-08-14 · `cdc0b693` · [Refractor] plain-lens divergence Auditor (Fire 2) — a read-only per-row correctness verdict for 65 lenses the sweep never covered
- 2026-08-13 · `15c46beb` · [Processor] live-read round-trip collapse Fire 1 — KVGetMulti batching + a per-execution instanceOf memo; unblocks verticals.md self-pay; Fires 2–3 shelved
- 2026-08-11 · `1839f173` · [Bootstrap] a retired kernel entity is finally visible — orphan detector over the shared reconcile plan; review caught an advisory scan that could abort a boot, and a cost premise counting the wrong population
- 2026-08-11 · `1b3c4737` · [Edge/Refractor/Substrate] the SYNC stream expires what no client can name; review caught a probe transiently absent on every attach, and two streams that would wipe each other
- 2026-08-11 · `285e1fed` · [Refractor/Substrate] a structural pause probes its own way out on protected + grant lenses, latch-bounded and announced; review caught a silent auth-plane self-heal
- 2026-08-11 · `59441252` · [Refractor] sibling OPTIONAL branches fold instead of multiplying — capabilityEphemeral 75,000→50 peak rows; review caught two over-grant fail-opens in the reference walk, and a gauge reporting a sum
- 2026-08-11 · `2bb5a38b` · [Refractor/objects-base] a lens can project a relationship — `type(r)`, `r.key`, `r.data.<field>`; `objectAttachments` now supplies the `linkName` `DetachObject` requires
- 2026-08-11 · `bfbfe0c9` · [CI] unit sharded 3→4, re-balanced by measured `go test` time not LOC; unit pole 196s→170s, wall-clock 197s→171s; all gates still run, coverage assertion still exact
- 2026-08-11 · `029ef85b` · [Refractor] grouping-key reduction + `WITH DISTINCT` honoured — 9.7×/118× at 5k anchors; review caught a fail-open refusal path and an undercounted census, both mechanized
- 2026-08-11 · `942f78df` · [Edge/Substrate] cold sign-in delivery position — both hosts start where their knowledge ends; the discarded prefetch buffer and the boot-time position closed
- 2026-08-11 · `9ffc8ac8` · [Refractor/Processor/Pkgmgr] auth-plane grant latency CLOSED — Inc 4 close pass: descriptor-floor withdrawal, three install gates onto one entry point, boot meta reads fail closed
- 2026-08-11 · `d7126548` · [Refractor] 4a-4 — the actor-aware link arm gains a relation gate so the filter may narrow by relation; set-equality with the client arm, capabilityRoles 9 relation-blind subjects → 15 pinned
- 2026-08-11 · `fba7f172` · [Refractor] 4b — a half-installed pipeline refuses to narrow, reading the lens's declared kind from its own cypher rather than a new latch
- 2026-08-11 · `0ae2b7a1` · [CI] unit sharded 2→3 runners, re-balanced by test-file LOC (82.4k each); unit pole 210s→179s, CI wall-clock 227s→195s; all gates still run, coverage assertion still exact
- 2026-08-10 · `ed89444f` · [Processor] Contract #2 §2.5's descriptor floor — a submitter can no longer harden a descriptor-optional key into an existence oracle; claim ceremony 9 OK/0 FAIL live
- 2026-08-10 · `e72c86ed` · [Processor/identity-domain] 4d — a tombstoned sensitive read delivers scrubbed instead of failing the op, and both ceremonies' rejections collapse to one wire shape
- 2026-08-10 · `d4f7ebba` · [Refractor] 4a-2 — the enumerator's one-key answer kept only where a standing healer exists; tombstoned actors' peers reprojected
- 2026-08-10 · `20a45bb4` · [Contract] #1 abstract-type tombstone exemption + #3 soft-deleted sensitive-aspect read disposition — Andrew-ratified, both already built and live
- 2026-08-10 · `8013da3e` · [Refractor] 4c — the narrowing's mandated healer proven end to end; a row deleted under a narrowed consumer is restored only by RunSweep
- 2026-08-10 · `fef53ea7` · [Refractor] 4a-1 — the affected-anchor index narrows its WITH refusal, and walks the WITH clause its builder had been skipping
- 2026-08-10 · `33b7e49b` · [Refractor/Pkgmgr] dynamic type taxonomy — abstract types declared, resolved and gated; Fires A/B/C complete, C4/C5 adjudicated, narrowing re-homed to typed relation signatures
- 2026-08-10 · `98f83f5d` · [Refractor/Substrate] the taxonomy barrier reads the connection loss instead of waiting to be told — a verdict straddling a drop armed a `*` lens against a dead feed; 3/20 → 0/20
- 2026-08-10 · `e107083a` · [Refractor/Substrate] C2.6 — every rebuild starter shares one bound and the slot covers the pump's reopen; the non-atomic durable delete-recreate that paused a healthy lens is fixed
- 2026-08-10 · `208409f0` · [Refractor/Substrate] adjacency Shape B — the 1 MiB-jammed hub latches an overflow mark and its reads enumerate Core KV; live: 30,245 payload errors → 0, NATS 7.6 GiB OOM → 800 MiB steady, Refractor runs again
- 2026-08-10 · `d8cc803c` · [Substrate] `KVGetMulti` — batched, atomic multi-subject KV read (`multi_last`), adopted by step-4 hydration + `personalinterest.IsRelevant`; 3-layer adversarial review folded, incl. a wildcard-injection fix
- 2026-08-10 · `4b846e82` · [Weaver] a gap's external class is read from its pattern's step kinds, not its action name — the timed-out vendor call retries again, and its legs are gated (§10.3 ratified)
- 2026-08-10 · `a951b258` · [Substrate] `CredsFile` is pre-flighted like `NKeySeedFile` already was, closing the retry-loop asymmetry review found
- 2026-08-10 · `d592a2c8` · [Substrate] `Connect`'s initial NATS handshake gets its own timeout + bounded, ctx-aware retry budget that skips permanent auth failures
- 2026-08-09 · `59933666` · [Pkgmgr] secure-column guard reserves all four platform RLS columns, matching Refractor's activation-time check
- 2026-08-09 · `e2f01a16` · [CI] Whetstone — four flakes root-caused to tests depending on what they do not control: the Starlark wall budget, ack redelivery, Weaver's sweep cadence, and the NATS handshake
- *(older rolled to [archive/lattice-done.md](archive/lattice-done.md); newest `b71be85d`)*
