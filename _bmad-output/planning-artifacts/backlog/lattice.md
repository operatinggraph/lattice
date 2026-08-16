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
| **[Processor] The root actor set is a boot snapshot of a live topology** | Fork A made root = `holdsRole → operator` (live), but `ClassAwarePlatformKey` closes over a boot-time `SystemActorKeys` scan and four binaries snapshot independently: a newly-granted operator is unread until restart and the binaries can disagree. Revocation is fail-closed, so this is latency + consistency, not over-grant. | ★ | S–M | 📋 ready · [why](../../implementation-artifacts/grant-provenance-runtime-permission-minting-design.md) §3.3 |
| **[Docs] Four sites still call the root actor set "kernel-seeded"; Fork A made it role-derived** | `capabilitykv/keys.go:47-56`, `cmd/processor/main.go:138-140` (+ refractor/weaver/loom), and Contract #6 §6.1's "bounded to the kernel-seeded root actors" all describe a population the predicate does not enforce. Stale post-Fork-A prose. | ★ | XS | 📋 ready · sent a designer fire down a wrong path 2026-08-11 |
| **[Pkgmgr] No live-vs-declared reconciliation for permission vertices** | `VerifyAgainstDefinition` compares manifest-to-Go, never Core KV, so a permission vertex no manifest declares is invisible to every gate. Branch A ratified keeps the runtime channel, so drift exists — but Inc 3's `origin` stamp (shipped) makes entries self-describing, leaving the reconciler an auditor convenience. | ★ | S | 📋 ready · unblocked (grant-provenance Inc 3 shipped) · [why](../../implementation-artifacts/grant-provenance-runtime-permission-minting-design.md) §1.1 |
| **[Tooling] `verify-claim-ceremony.go` asserts a 5s SLA the platform never promised** | `waitForRoleGrant`'s 5s deadline reads real unbounded latency as "never appears": 4/5 live demo-box runs failed it while every grant landed minutes later. Poll to convergence. | ★ | XS | 📋 ready |
| **[Processor] A derived `reads` can harden a floored key the envelope never declared** | The §2.5 floor applies to the envelope, not the merged set, so `derive_reads` output is outside it. Latent — both `derive_reads` in `packages/` return only `{}`/`optionalReads` on all seven paths — and outside the clause's literal scope, which binds the submitter. | ★ | S | 📋 ready · consumer: the first `derive_reads` returning a `reads` key · [why](../../implementation-artifacts/auth-plane-projection-latency-design.md) §19.7 |
| **[Processor] §2.5's floor silently skips the key templates it cannot resolve** | `resolveDescriptorFloor` applies no floor for `{me.*}`/`{entity.*}` and only logs Warn, so those keys stay hardenable by a hand-rolled `contextHint`. The clause reads unconditional; enforcement is not. Live: cafe-domain's `Charge`/`Settle` declare exactly that shape. | ★★ | M | 📐 needs designer pass (2026-08-15) · consumer LIVE: cafe `Charge`/`Settle` · missing/multi-anchor `{me.*}` semantics is a design call |
| **[Refractor] Two clause shapes the full engine accepts and silently miscompiles** | An **anonymous** required `MATCH` binding no NEW variable is dropped rather than filtered; the named-relationship form now filters correctly. `WITH *` reaches the AST as an empty projection list, so `projectItems` rebuilds each row empty and a later pattern re-seeds by whole-bucket scan. | ★★ | M | 📐 needs designer pass — refuse-at-parse vs implement is a clause-semantics fork, corpus-wide blast radius · 0 live lenses use either shape |
| **[wellness-ledger] `wellnessMemberAccounts` has no retraction transport** | Plain lens, no `Output` descriptor, no `EmptyBehavior`, no `DiffRetraction`; `AnchorProjectionKey` returns `ok=false` for any query carrying a `WITH`. A member whose last booking is deleted keeps their row forever. Its three settlement siblings do declare `DiffRetraction`. | ★★ | S | 📐 needs designer pass — turning retraction ON is the known mass-Delete hazard; `EmptyBehavior` must be decided with it · not auth-plane, stale-read direction |
| **[Pkgmgr] `validateGrantSliceVarNames` cannot see a variable inside a node property map** | `patternVarNames` reports pattern variables only; the chain parser skips a `{...}` property map wholesale, so `(bk:booking {slice: grantSlice0})` is emitted verbatim. | ★ | XS–S | 📋 ready · record property-map vars at parse time |
| **[Refractor] A key destruction's relevance oracle reads the lens NOW** | Both halves answer from the current `secureColumns[].holderTypes`, so narrowing a lens's declared holders puts its pre-upgrade ciphertext outside every later destruction — and the empty target set attests vacuously. | ★★ | M | 📋 ready · consumer: the first lens whose holder types narrow · [why](../../implementation-artifacts/retention-class-key-custody-design.md) §24.6 |
| **[Edge] The first-paint gate has no identity for the hydrate cycle it gates** | Two defects, one missing mechanism — a per-hydrate correlation id on the marker. `hydrationComplete` carries no `Lens`, so the gate releases on the first marker and can paint a partial world; and `personal.hydrate` is per-**identity**, so a second device's burst satisfies this device's gate. The client-side fix was built and refuted. | ★★ | M | 📐 needs designer pass · [why](../../implementation-artifacts/edge-cold-signin-delivery-position-design.md) §6 Fire 4 |
| **[Pkgmgr] `packageName` is compared byte-exact across the installer** | `IsPackageInstalled` + the Starlark `proposal_string` helper (`packages/capability-author/ddls.go`, unlike its `.strip()`-ing `required_string` sibling) never normalize case/whitespace — pre-existing, every human package install too, not AI-capability-specific. | ★ | XS–S | 📋 ready · found 2026-08-15 reviewing the platform-package guard · [why](../../implementation-artifacts/ai-authored-capabilities-design.md) §8 Fire 2 close-out |
| **[Tooling] The G2 derived-key gate does not cover `internal/` submitters** | `internal/gateway/whoami.go` re-implements identity-domain's email normalization and derives both index keys; `internal/objectmanager` derives too. The gate excludes `internal/` wholesale because that tree also OWNS the primitive. | ★ | S | 📋 ready · [why](../../implementation-artifacts/client-ceremony-op-descriptors-design.md) §12.7 |
| **[Tooling] The identityindex ownership-gate's `expectedRevision` pin has no red test** | `index_owned_by`/`idx_names_owner`'s content check is mutation-tested, but no in-process fixture can lose the actual commit-order race, so the `expectedRevision` pin (the half that closes the window, not just narrows it) is unverified by any test — reverting it stays green today. | ★ | XS–S | 📋 ready · [why](../../implementation-artifacts/erasure-orchestration-design.md) fire brief close-out |
| **[Processor] `derive_reads` binds `state`/`ddl` to empty dicts rather than failing closed** | `kv` and `nanoid` are fail-closed stubs; `state[k]` returns a silent `None` where `kv.Read` errors loudly. Within Contract #2 §2.5, but the purity argument's weakest link. | ★ | S | 📋 ready · [why](../../implementation-artifacts/client-ceremony-op-descriptors-design.md) §12.7 |
| **[Loom/Weaver] A dispatcher cannot declare its op's class-(e) enumerations** | A `kv.Links` walk is declared through `ContextHint.Enumerations`, expressible by neither Loom `systemOp` submit nor Weaver `directOp` (`GapActionSpec` has no field). | ★ | XS–S | 📋 ready · consumers: `identityErasure`; `identityErasureComplete` · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 3 res 1, inc 7 res 4 |
| **[identity-domain] A `credentialindex` with no `boundTo` link is residue nothing can walk** | It carries its identity in its body with no link to it, so no enumeration reaches one; the sweep covers only those with a `boundTo`. §9.2(i) names the class; the attestation does not. | ★ | S | 📋 ready · consumer: the erasure attestation's coverage claim · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 6 res 3 |
| **[Weaver] A gap whose re-dispatch IS the progress advances once per mark lease** | A paged loop's own reprojection lands while its mark's 30-min lease is live, so `fireEpisode` takes the anti-storm drop and only the reconciler's reclaim re-dispatches: 64 links/30 min. | ★★ | M–L | 📋 ready · needs design (engine seam) · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 7 res 1 |
| **[privacy-base] A stuck erasure sweep re-dispatches indefinitely with no escalation** | `maxretries_<g>` is ruled out (inert at the sweeps' scale, terminally parking at the seal's); a `surface` gap cannot fire either — a hard-failing sweep commits nothing, so "stuck" is history no row holds. | ★★ | S–M | 📋 ready · consumer: the operator surface (§12 step 4) · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 7 res 2 |
| **[Weaver] A `surface` gap's Health issue carries no entity segment** | `issueKeyGap` keys per `(target, column)`, so with two erasures in flight the subject whose halves land first clears the issue raised for the stuck one. Wrong per-subject. | ★ | S | 📋 ready · consumer: `identityErasureComplete`'s async-half gaps · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 7 res 3 |
| **[privacy-base] A pre-narrowing shredded subject earns a clean attestation over live `credentialindex` rows** | That shred tombstoned `boundTo` and left each `vtx.credentialindex.<hash>` standing, so the sweep finds no live link, all five residue arms read zero, and the seal attests `violating=false` over N live rows. | ★★ | S–M | 📐 needs designer pass (2026-08-15) · needs an undesigned `credentialindex` enumeration primitive, same gap as row 63 · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 9, §9.2(i) |
| **[privacy-base] `identityErasure` has never run end to end on a live stack** | Every arm is proven in package tests and the submit path by Weaver's dispatch, but no subject has gone through all four steps on a running stack — and a real run destroys a key. | ★★ | S | 📋 ready · consumer: the first real erasure · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 11 res 2 |
| **[privacy-base] A merge concurrent with erasure step 1 still wedges the subject** | The §6 piiKey gate refuses any merge submitted after step 1 commits, so only the race survives: a `MergeIdentity` hydrating its gate before step 1 and landing after holds no OCC on `piiKey`. Key destroyed, step 2 refuses `IdentityMerged` forever. | ★ | S | 📋 ready · consumer: `identityErasure` step 1 · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 9–10 |
| **[Tooling] privacy-base has no `verify-package-*` target** | It owns the erasure spine (pattern, weaver target, residue lens, seal, 6 DDLs) and asserts none of it after a diff-apply; a bad install surfaces only when an erasure stalls. | ★ | S | 📋 ready · consumer: the next privacy-base diff-apply · mirror `verify-package-identity` |
| **[Processor] Sensitive resolution trusts a mutation's self-reported `class`, never the key's localName** | Step 6/6.5 resolve an aspect's DDL off `Document["class"]` only; nothing checks it against the key's own localName. Omitted/wrong `class` on a sensitive key commits PHI plaintext, cache healthy or not. | ★★ | M | 📐 needs designer pass · [why](../../implementation-artifacts/ddl-cache-invalidation-fault-signal-design.md) §1 |
| **[Pkgmgr] A plain `Columns` entry can still collide with a platform-reserved name** | Only `SecureColumns` is checked against the four reserved RLS columns (`authz_anchors`/`projection_seq`/`is_deleted`/`deleted_at`); an ordinary `Columns` entry with one of those names installs and fails only at Postgres activation (42701 duplicate column). | ★ | XS | 📋 ready · consumer: the next package declaring a plain column named one of the four |
| **[Refractor] `dispositionEvalErr` has no `CatPrivacyCritical` arm** | The category is defined and classified (`classify.go:149`) but `pipeline.go:2116-2138` routes it to the default `Nak`. Unreachable today — it is only ever constructed inside `keyshredded`, which handles its own pause and never returns it up the evaluation path. | ★ | XS–S | 📋 ready · consumer: the first caller wrapping an evaluation-path error as `PrivacyCritical` |
| **[Pkgmgr] The manifest verifier skips retention classes** | `ManifestBlock` (`manifest.go:133-152`) compares DDLs/lenses/permissions/weaverTargets/loomPatterns/opMetas/panes; `RetentionClasses` has no field and no comparison, so a package mints a `vtx.retentionclass` holder its manifest never declares. | ★ | XS–S | 📋 ready · consumers: clinic `clinicalRecord`, lease-signing `underwritingRecord` · mirror the opMetas block |
| **[Refractor] A behavior-frozen consolidation pass — 105K LOC at ×1.8 in 39 days** | test:prod ≈1:1, `pipeline.go` ≈3.2K, `executor.go` ≈2.1K; fold test scaffolding, split god-files — LOC + CI time down, suite green. | ★★ | M–L | 📋 ready · owner-driven · behavior-frozen · first target: pipeline.go + test-corpus overlap |

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
| **[natsperm] `$JS.ACK.>` lets any component forge an ack for a core-events consumer it doesn't own** | `processAckMsgLocked` never checks the publisher, so any `$JS.ACK.>` holder can ack-forge a guessed `sseq`/`dseq` for a protected core-events consumer — a narrower sibling of the closed MSG.NEXT steal, no per-consumer scoping exists. | ★ | M | 📐 needs designer pass · found 2026-08-14 · `internal/natsperm/matrix.go` |
| **[identity-domain] A claim rejection's LATENCY still separates already-claimed from wrong-key** | The wire shape is now identical, but a tombstoned `.claimKey` returns before `readPiiKeyEnvelope`'s KVGet and the AEAD decrypt, and the script exits earlier — so NFR-S6's generic refusal is measurable-through. The design holds itself to this standard at `ddls.go:1489`. | ★★ | M | 📋 needs designer pass — constant-time rejection, no ratified pattern to extend · [why](../../implementation-artifacts/auth-plane-projection-latency-design.md) §19.5 |
| **[Loom] An externalTask can only declare its SUBJECT's own aspects for egress** | `inferExternalTaskReads` parses `subject.<aspect>` only (`externaltask_params.go:42`), so a LINKED vertex's field is undeclarable in `egressReads` and the commit guard rejects it plaintext (`step6_validate.go:170`, live: `leasedoc_scripts.go:21-29`). | ★★ | M | 📐 needs designer pass (2026-08-15) · verified NOT a parser extension — genuine fork (see this row's commit message), no ratified pattern to extend |
| **[bootstrap] `UpgradePackage`'s create arm can forge a package-origin permission/role vertex** | No server-side check ties a create mutation's content to the named package's real, compiled Definition — an attacker can mint a brand-new `vtx.permission.*` with a self-declared `origin:"package"` and any operationType. `roleindex` shares the gap since 2026-08-15. | ★★★ | M | 📐 needs designer pass · found 2026-08-14 · [why](../../implementation-artifacts/permission-role-provenance-write-once-design.md) §8(a) |
| **[rbac] A tombstoned `grantedBy` link can be revived by a direct `update` with `isDeleted:false`** | `diffManifest`'s already-tombstoned-skip is client-side only; an `UpgradePackage` holder crafting the envelope directly restores a specifically-revoked grant with no server-side backstop. | ★★ | S–M | 📐 needs designer pass · build attempt 2026-08-15 falsified `OperationType` as a sufficient signal · [findings](../../implementation-artifacts/permission-role-provenance-write-once-design.md) §15 |
| **[Loupe] A `weaverTarget`/lens config's `protected` field reads a malformed value as "not protected"** | `cmd/loupe/lens.go:83`/`lenses.go:45` read `cfg["protected"].(bool)` ok-discarded — same fail-open shape, display-only badge not a gate; today's writers always emit Go `true`. | ★ | XS | 📋 ready · found 2026-08-15 · [why](../../implementation-artifacts/permission-role-provenance-write-once-design.md) §17 |

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
| **Typed relation signatures — `containedIn: location→location`** | Declare a relation's endpoint types against the taxonomy, enforced at step 6 fail-closed; a signed variable-length hop contributes its endpoint expansion rather than clearing exhaustiveness. Held 2026-08-13: the payoff shrank to 2 lenses, both convertible by a single-hop rewrite (replacement row on verticals). | ★★ | L | 🗄️ shelved (revive: an intermediate containment level, or rewrite-unreachable varlength census) · [design](../../implementation-artifacts/typed-relation-signatures-design.md) |
| **[Refractor] Cross-instance projection-latency rollup** | Aggregate per-lens projection latency across Refractor instances into one per-component view; single-instance today, so the two coincide. | ★ | S | 🚧 seq behind HA-NATS multi-instance · tombstone half subsumed by the [link-aspect design](../../implementation-artifacts/link-aspect-triggered-reprojection-plain-lenses-design.md) |

### Refinements & ops
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **`internal/refractor`'s claim-ceremony e2e family is non-deterministic at head** | Membership rotates over the three claim-ceremony e2es: `cap.roles.<target>` never gains the role-derived grant from the real `ClaimIdentity` op's `holdsRole` write within 25s. The test never waits for the adjacency edge the reprojection walks (the sweep e2e beside it does), so a lens pass can precede it. | ★★ | S–M | 📋 ready · owner: Whetstone · no repro on a quiet 4-core box · tighten, never loosen · [why](../../implementation-artifacts/retention-class-key-custody-design.md) §19.5 |
| **Suite reddens under parallel load, in packages the change never touched** | Rotating membership across unit-1, unit-2 and the convergence job. THREE mechanisms: (a) a test dialling production `substrate.Connect` inherits nats.go's 2s no-retry handshake — ~45 sites remain; (b) a starvation signature (0 lenses activated in 25s, `found=map[]`); (c) a wall-clock DEADLINE read as correctness — `TestLeaseConvergence_DrainThenAssert_SteadyState` blew 30s in CI, 8.5s locally at the same SHA. | ★★★ | M | 🏗️ owner: Whetstone · next: root-cause (b) |
| **`TestRefractor_E2E_P99` gates an absolute latency SLO on a shared runner** | NFR-P3's 500ms p99 is asserted by a unit test measuring wall-clock projection latency while three other jobs contend for the runner: CI run 31288862556 measured `10.03s`. A shared runner promises no latency floor, so the gate reads contention, not regression. | ★★ | S | 📋 ready · owner: Whetstone · reshape the measurement or move it off shared CI |
| **[Weaver] `sweeper.pass`'s mark+row read pair needs a joint-snapshot judgment call before batching** | Sequential per-entity mark-then-row reads; whether the reconciliation invariant tolerates a joint `KVGetMulti` snapshot vs. today's two straight-line `KVGet`s is undecided. Runs every minute, highest-traffic site in the enumeration corpus. | ★ | S | 📋 ready · consumer: the enumeration-corpus sweep · [why](../../implementation-artifacts/adjacency-per-edge-index-design.md) §17.2 |
| **[Refractor] Rule-engine anchor derivation's memoized reads make bulk `KVGetMulti` a possible net loss** | `executor.go:833-890` already memoizes fetched nodes (`ex.nodes`, `:994`) and exits early on many paths; batching every listed key up front could read more than the short-circuiting per-key path it would replace. Needs a read-count comparison, not a mirror. | ★ | S | 📋 ready · consumer: the enumeration-corpus sweep · [why](../../implementation-artifacts/adjacency-per-edge-index-design.md) §17.2 |
| **CI pipeline speed (continuous)** | Make CI faster without weakening any gate — owned continuously by the **Whetstone**. Matrix split done (serial → 4 parallel jobs); convergence + unit parallelized; unit sharded across 4 runners (was 3), re-balanced by measured `go test` time not LOC. | ★★ | M (ongoing) | 🏗️ continuous (Whetstone) · unit pole 196s→170s, wall-clock 197s→171s (run 31513357030) · next: unit-2/convergence/lint-build now within 8s — no single pole; further gains likely need paid runners |

### Parking lot — very low priority (far, far back)

Rolled to [archive/lattice-parked.md](archive/lattice-parked.md) — real but low-value; no design or build
effort without an Andrew greenlight. A row that acquires a real driver comes back here.

## Done log — lattice (newest first)

One line per shipped item (`date · SHA · [tag] title`). Oldest roll to `archive/` past ~25.

- 2026-08-15 · `717312ca` · [Perf] Lattice-slice enumeration-corpus KVGetMulti sweep CLOSED — 9 pkgmgr/weaver/loom/refractor-health sites batched; 2 deferred as their own rows; Loupe/Verticals shares are those streams' pick
- 2026-08-15 · `c51746ec` · [Pkgmgr] AI-capability apply platform-package guard CLOSED — review found+fixed a real bypass: both Loupe apply/mark-applied handlers routed around the plan builder's deny-list; normalization gap filed separately
- 2026-08-15 · `b4eb8fb2` · [Tooling] gofmt CI gate CLOSED — 37 files formatted; dangling ratified §19 contract edit also committed this fire (`0e84769e`)
- 2026-08-15 · `70bec5e6` · [bootstrap] package-manifest ownership scoping CLOSED — review found+fixed a self-forged-manifest bypass; §8(a)/§15 stay open, narrowed not closed
- 2026-08-15 · `6ecf3d77` · [Facet] queued ceremony reveal durability CLOSED — reload recovery + a pinned loss signal; review found+fixed 2 real gaps in the sign-out/auth-death purge paths
- 2026-08-15 · `8ca834a1` · [Processor] undeclared-key RevisionConflict attribution CLOSED — kv.Links-discovered keys now retry-eligible + correctly named; review found+fixed a false-pass test and an unbounded probe
- 2026-08-15 · `07b1615b` · [location-domain] `LEGACY_LOCATION_CLASS` widening CLOSED — 7 packages; census found the Contract #1 tombstone exemption is a permanent invariant, not the removable marker the row assumed
- 2026-08-15 · `ada53b37` · [Edge] `SYNC` stream MaxBytes CLOSED — 512 MiB cap mirrors `EnsureAuditStream`; 2 dangling ratified contract edits also committed this fire (`004e079c`)
- 2026-08-15 · `aa41f292` · [rbac] `vtx.roleindex.<id>` provenance write-once CLOSED — mirrors the role-root guard; 3 cold reviews + 1 fix round, 0 blocking; create-forgery and no-repoint-heal limitations filed, not built
- 2026-08-15 · (verification, no SHA) · [Edge] cross-lens delete-drop row REMOVED — already closed, proven by the pinned `TestPersonalTarget_ProducesNoDeleteShapedResult`
- 2026-08-15 · `30d87457` · [identity-domain] erased-incumbent identityindex repoint CLOSED — review found+fixed a cross-op steal/revive race, closed with content+revision gates in privacy-base and identity-hygiene too; pin-coverage gap filed
- 2026-08-15 · `db65e4da` · [Facet] concurrent first-`Acquire` mirror race CLOSED — per-identity singleflight, not a widened mutex; adversarial review found+fixed a Purge-race corpse-supersedes-live-build bug; both regressions mutation-verified
- 2026-08-15 · `965a8415` · [Processor] `data.protected`/`data.sensitive` fail-opens CLOSED — mutation gate + a review-found live gap at the `.sensitive` aspect read site, fixed same fire; Loupe sibling filed

- 2026-08-15 · `79b090cc` · [Processor] mutation `isDeleted` type gate CLOSED — batch-wide pre-pass fails a malformed value closed; review found no bypass, caught a comment overclaim; 2 adjacent fail-opens filed

- 2026-08-15 · `034ab1f1` · [bootstrap] permission/role provenance write-once CLOSED — review closed a role-root shadow bypass beyond the design's sketch; contract §8.4 staged uncommitted for Andrew; 2 adjacent gaps filed
- 2026-08-14 · `8796e6e9` · [Pkgmgr] renamed/uninstalled retention class no longer strands its DEK CLOSED — diffManifest + Uninstall exclude the holder; contract §8.3/§8.6 staged uncommitted for Andrew
- 2026-08-14 · `acecf6f9` · [platform] primordialActor ownership guard CLOSED — 7 `Scope:"any"` ops restricted to their dispatching engine; lint gate default-denies the class
- 2026-08-14 · `83891a8c` · [natsperm] core-events JS.API side channel CLOSED — 6-consumer registry + matrix-wide SNAPSHOT/RESTORE; ack-forgery residual filed
- 2026-08-14 · `f464c7a5` · [rbac] grant-provenance Inc 3 CLOSED — origin stamp + reserved-set refusal; review found + fixed a real grant-laundering bypass; 28-pkg migration verified live
- 2026-08-14 · `1208e638` · [rbac] grant-provenance Inc 1+2 — UpdatePermission's grant withdrawn, structural lint gate blocks re-granting it; review found a live UpgradePackage escalation, filed separately; Inc 3 remains
- 2026-08-14 · `0bb6daea` · [Pkgmgr] un-tombstone prerequisite CLOSED — a revoked grant/role key no longer silently revives on the next upgrade; Contract #8 §8.6 edit staged uncommitted for Andrew
- 2026-08-14 · `63f53d67` · [Refractor] personal-lens D1 grant-change trigger CLOSED — Inc 1 `b69487ef` (notification edge) + Inc 2 `63f53d67` (convergence sweep); adversarial pass caught a Health-entry resurrection, fixed same commit
- 2026-08-14 · `afdbc5f4` · [Processor] a degraded DDL cache no longer trusts a stale-or-chain-walked answer; empty-class arm re-filed as its own gap

- 2026-08-14 · `0a9ff629` · [Refractor] plain-lens neighbour derivation Inc 4b CLOSED — the seeded branch's multi-position gap; Inc 5 stays deferred behind its own trigger
- *(older rolled to [archive/lattice-done.md](archive/lattice-done.md); newest `8f421d80`)*
