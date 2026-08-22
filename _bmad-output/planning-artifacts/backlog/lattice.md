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
  **State** cell = a **token** + a **link to the design doc / commit** + (only if 🏗️) an **`owner:` stamp**
  and **one ≤10-word next step**. Nothing else. **The whole FILE is capped too** — compact rows when it bites.
- **A 🏗️ row names its owner** — `owner: <fire-branch>` (or the role, e.g. `owner: Whetstone`). Fires run in
  ephemeral containers where the fleet build lock is void (`agents/steward/REMOTE.md` §4), so an unowned 🏗️
  is indistinguishable from an abandoned one and two stewards claim it at once. Claim by pushing the row-flip
  before building; if a stamped owner's branch has been idle for hours, take it over and re-stamp.
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
- **Two filing gates** (2026-08-20 audit — [why](../../implementation-artifacts/lattice-backlog-audit-2026-08-20.md)):
  **(1) Consolidate at filing** — residuals that share a root cause file as **one** row naming the shared
  missing primitive, not N. **(2) Honest designer-gate** — a `📐 needs designer pass` row (design-WANTED,
  distinct from `📐 awaiting-Andrew`, a *finished* design) must declare the specific absent ratified pattern:
  `no-pattern: <named primitive>`. If a precedent exists in the touched file it is a steward `📋`, not a
  designer `📐` (§2.5's test). `lint-board` default-denies the bare label (the `# read-posture:` shape).

## Loupe → its own lane

Loupe (`cmd/loupe`) is Stream 3, on **[loupe.md](loupe.md)** (own build lock). Loupe rows do not live here;
a platform primitive Loupe needs still files HERE per the cross-lane rules.

## Component maintenance

Open items only (shipped ones are in the Done log). Grouped by component tag.

| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Bootstrap] Reconcile creates + updates but never removes a retired kernel key** | A kernel entity the current binary does not build stays live and executable: a dispatchable DDL, a running lens pipeline, a held canonicalName. No wipe-free shrink path. | ★ | S–M | 🗄️ shelved (Inc 2 retire verb) · [design](../../implementation-artifacts/kernel-orphan-retirement-design.md) · Inc 1 detector shipped, census 0/0 both buckets; needs a binary-version floor |
| **[Docs] Four sites still call the root actor set "kernel-seeded"; Fork A made it role-derived** | `capabilitykv/keys.go:47-56`, `cmd/processor/main.go:138-140` (+ refractor/weaver/loom), and Contract #6 §6.1's "bounded to the kernel-seeded root actors" all describe a population the predicate does not enforce. Stale post-Fork-A prose. | ★ | XS | 📋 ready · sent a designer fire down a wrong path 2026-08-11 |
| **[Pkgmgr] No live-vs-declared reconciliation for permission vertices** | `VerifyAgainstDefinition` compares manifest-to-Go, never Core KV, so a permission vertex no manifest declares is invisible to every gate. Branch A ratified keeps the runtime channel, so drift exists — but Inc 3's `origin` stamp (shipped) makes entries self-describing, leaving the reconciler an auditor convenience. | ★ | S | 📋 ready · unblocked (grant-provenance Inc 3 shipped) · [why](../../implementation-artifacts/grant-provenance-runtime-permission-minting-design.md) §1.1 |
| **[Loupe] A `newPackage` proposal is closed over a same-named package it never wrote** | The apply endpoint's recovery branch reads "installed at the target version" as "this proposal's install committed"; for `newPackage`, name+version cannot separate that from a pre-existing stranger (`review.go:862-878`). | ★★ | M | 📐 needs designer pass · no-pattern: a durable proposal→install binding readable before mark-applied · [why](../../implementation-artifacts/capability-apply-removal-refusal-design.md) §3.4 |
| **[Pkgmgr] No additive (partial-Definition) package apply** | A capability proposal can create a package, never add to one: `Apply`'s in-place branch is whole-Definition convergence. Needs a per-key origin stamp + a removal verb to be sound. | ★ | L | 🗄️ shelved · trigger: the first shipped producer emitting `target.mode: upgradeExisting` for a NEW artifact · [why](../../implementation-artifacts/capability-apply-removal-refusal-design.md) §4 |
| **[Pkgmgr] An uninstalled package cannot be reinstalled — only refused** | Reinstall is a loud refusal naming the occupants, but the keys stay dead and the remedy it names restores grants only: uninstall is a one-way door, and the operator console offers it as a button. Reviving needs step 8's package-scope guard to resolve a tombstoned manifest. | ★★ | L | 🗄️ shelved (revive: a real recorded operator need to undo an uninstall) · false-green already closed (`00a4a73`) · [why](../../implementation-artifacts/package-restore-design.md) |
| **[Tooling] `verify-claim-ceremony.go` asserts a 5s SLA the platform never promised** | `waitForRoleGrant`'s 5s deadline reads real unbounded latency as "never appears": 4/5 live demo-box runs failed it while every grant landed minutes later. Poll to convergence. | ★ | XS | 📋 ready |
| **[Processor] A derived `reads` can harden a floored key the envelope never declared** | The §2.5 floor applies to the envelope, not the merged set, so `derive_reads` output is outside it. Latent: every `derive_reads` in `packages/` returns only `{}`/`optionalReads`. | ★ | S | 📋 ready · consumer: the first `derive_reads` returning a `reads` key · [why](../../implementation-artifacts/auth-plane-projection-latency-design.md) §19.7 · scoped out of the shipped floor: [§8](../../implementation-artifacts/descriptor-floor-template-coverage-design.md) |
| **[Pkgmgr] `validateGrantSliceVarNames` cannot see a variable inside a node property map** | `patternVarNames` reports pattern variables only; the chain parser skips a `{...}` property map wholesale, so `(bk:booking {slice: grantSlice0})` is emitted verbatim. | ★ | XS–S | 📋 ready · record property-map vars at parse time |
| **[Tooling] The G2 derived-key gate does not cover `internal/` submitters** | `internal/gateway/whoami.go` re-implements identity-domain's email normalization and derives both index keys; `internal/objectmanager` derives too. The gate excludes `internal/` wholesale because that tree also OWNS the primitive. | ★ | S | 📋 ready · [why](../../implementation-artifacts/client-ceremony-op-descriptors-design.md) §12.7 |
| **[Processor] `derive_reads` binds `state`/`ddl` to empty dicts rather than failing closed** | `kv` and `nanoid` are fail-closed stubs; `state[k]` returns a silent `None` where `kv.Read` errors loudly. Within Contract #2 §2.5, but the purity argument's weakest link. | ★ | S | 📋 ready · [why](../../implementation-artifacts/client-ceremony-op-descriptors-design.md) §12.7 |
| **[Loom/Weaver] A dispatcher cannot declare its op's class-(e) enumerations** | A `kv.Links` walk is declared through `ContextHint.Enumerations`, expressible by neither Loom `systemOp` submit nor Weaver `directOp` (`GapActionSpec` has no field). | ★ | XS–S | 📋 ready · consumers: `identityErasure`; `identityErasureComplete` · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 3 res 1, inc 7 res 4 |
| **[identity-domain] A `credentialindex` with no `boundTo` link is residue nothing can walk** | It carries its identity in its body with no link to it, so no enumeration reaches one; the sweep covers only those with a `boundTo`. §9.2(i) names the class; the attestation does not. | ★ | S | 📋 ready · consumer: the erasure attestation's coverage claim · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 6 res 3 |
| **[identity-domain] `TombstoneOrphanedCredentialIndex`'s outbound arm leaves a phantom entry in the LIVE owner's `credentialBinding` array** | The op retires the index vertex only. In the outbound shape the owner is alive, and their sign-in-methods array still lists the erased credential. Rewrite precedent: `unbind_identity_credentials.go`'s `owner_binding_rewrite`. | ★ | XS–S | 📋 ready · NEW find, not a deferral — the index tombstone closes the leak in full; this is live-third-party hygiene · [why](../../implementation-artifacts/erasure-orchestration-design.md) close-out note |
| **[Weaver] A `surface` gap's Health issue carries no entity segment** | `issueKeyGap` keys per `(target, column)`, so with two erasures in flight the subject whose halves land first clears the issue raised for the stuck one. Wrong per-subject. | ★ | S | 📋 ready · consumer: `identityErasureComplete`'s async-half gaps · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 7 res 3 |
| **[Contract #1] The "default class from localName" clause is unimplemented and contradicts document-is-source-of-truth** | §1.5 promises the Processor defaults an omitted `class` from the key's local name; nothing implements it, and defaulting class FROM the key inverts the posture that the document's `class` field is authoritative for type + sensitivity. Delete the clause. | ★ | XS | 🔭 flag-for-Andrew (frozen-contract doc fix) · key-binding redesign REJECTED · [why](../../implementation-artifacts/sensitive-aspect-class-integrity-design.md) |
| **[Refractor] `dispositionEvalErr` has no `CatPrivacyCritical` arm** | The category is defined and classified (`classify.go:149`) but `pipeline/dispatch.go:364-386` routes it to the default `Nak`. Unreachable today — it is only ever constructed inside `keyshredded`, which handles its own pause and never returns it up the evaluation path. | ★ | XS–S | 📋 ready · consumer: the first caller wrapping an evaluation-path error as `PrivacyCritical` |

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
| **[capability-author] An authored Starlark artifact can launder sensitive plaintext into a non-sensitive aspect** | An undeclared `kv.Read` decrypts (`starlark_kv.go:424`), step 6's egress guard permits deriving into an ordinary domain event, and step 6.5 keys encryption on the DESTINATION DDL — so it stores as plaintext and an authored lens projects it. | ★★★ | L | 📐 needs designer pass · no-pattern: a declared-read floor for AI-authored scripts + a sensitivity gate on the Starlark kinds · [why](../../implementation-artifacts/authored-artifact-admission-model-design.md) §5.5 |
| **[natsperm] `AllowResponses` defeats every deny in the matrix** | A delivered message registers its reply subject as a dynamic response permission exactly when the client is denied on it, and a publisher's reply is never checked. Reproduced: `loom`, denied `$KV.core-kv.>`, takes a PubAck on it. Voids the denies for the 6 components carrying the flag. | ★★★ | M–L | 📐 needs designer pass · no-pattern: reply authority without `AllowResponses` · [why](../../implementation-artifacts/protected-consumer-ack-plane-denies-design.md) §3.3 |
| **[identity-domain] A claim rejection's LATENCY still separates already-claimed from wrong-key** | The wire shape is now identical, but a tombstoned `.claimKey` returns before `readPiiKeyEnvelope`'s KVGet and the AEAD decrypt, and the script exits earlier — so NFR-S6's generic refusal is measurable-through. The design holds itself to this standard at `ddls.go:1489`. | ★ | XS–S | 📋 ready · fixed floor on the rejection paths; NFR-S6 bars structure-leak, not timing · [why](../../implementation-artifacts/auth-plane-projection-latency-design.md) §19.5 |
| **[bootstrap] A package-plane actor can forge a package-origin permission and grant it to itself** | `origin:"package"` is client-supplied and buys the Contract #6 reserved-set exemption; nothing ties a created `vtx.permission.*` to authority the submitter may confer. Reachable from `UpgradePackage`'s create arm and from any package-authored DDL script. Owns the `grantedBy`-revival gap. | ★★★ | M | 🗄️ shelved (revive: consoleOperator delegated below root) · [design](../../implementation-artifacts/package-authority-minting-provenance-design.md) |
| **[natsperm] A consumer's `DeliverSubject` is unchecked — the app tier's no-`ops.>` guarantee is bypassable** | Nothing in `server/jetstream_api.go` checks `DeliverSubject`, so a `$JS.API.>` holder can write a forged envelope to a bucket it may write (`health-kv`) and mint a consumer republishing it onto `ops.system`. Falsifies #75 Fire 2b. Processor-side reachability UNVERIFIED. | ★★ | S–M | 📋 ready · standalone · ground the Processor arm first (contained ⇒ moot) · [why](../../implementation-artifacts/app-tier-transport-read-scope-design.md) §14/F2 |
| **[capability-author] Admission holes let an authored artifact reach the auth plane** | Each guard derives its governed set from a declaration source, not from the consumer: the bucket deny-list misses every package/app-owned bucket, `protectedDispatchSets` reads manifests so the 6 kernel-seeded ops stay dispatchable, and apply re-runs no kind-specific validation at all. | ★★★ | L | 📐 awaiting-Andrew · [design](../../implementation-artifacts/authored-artifact-admission-model-design.md) |
| **[natsperm] The app tier's read side is unrestricted — both planes** | `subscribe: [">"]` makes every app a live tap on `ops.>`, which carries sensitive mutations in PLAINTEXT (encryption is at commit step 6.5, after the lane). And a KV read is a *publish*, so the blanket `$JS.API.>` reads every bucket incl. all of Core KV. Subsumes the `capability-author-context` instance. | ★★★ | M | 🗄️ shelved (revive: app-tier NKey stops being trusted infra) · [why](../../implementation-artifacts/app-tier-transport-read-scope-design.md) |

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
| **[capability-author] One authoring request cannot co-propose a NEW lens + the target that binds it** | `RecordCapabilityProposal` records one `{kind,content}` per request and single-artifact apply resolves `lensRef` only by NanoID or same-Definition name — so a new-lens-plus-target intent can't produce both atomically. NL v1 binds an existing lens instead. | ★★ | L | 🗄️ shelved (revive: a real new-lens+target atomic intent, or a reported torn bundle) · two-step workaround shipped · [why](../../implementation-artifacts/capability-proposal-bundles-design.md) |

### Read-model / projection maturity
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **Typed relation signatures — `containedIn: location→location`** | Declare a relation's endpoint types against the taxonomy, enforced at step 6 fail-closed; a signed variable-length hop contributes its endpoint expansion rather than clearing exhaustiveness. Held 2026-08-13: the payoff shrank to 2 lenses, both convertible by a single-hop rewrite (replacement row on verticals). | ★★ | L | 🗄️ shelved (revive: an intermediate containment level, or rewrite-unreachable varlength census) · [design](../../implementation-artifacts/typed-relation-signatures-design.md) |
| **[Refractor] Cross-instance projection-latency rollup** | Aggregate per-lens projection latency across Refractor instances into one per-component view; single-instance today, so the two coincide. | ★ | S | 🚧 seq behind HA-NATS multi-instance · tombstone half subsumed by the [link-aspect design](../../implementation-artifacts/link-aspect-triggered-reprojection-plain-lenses-design.md) |

### Refinements & ops
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **`internal/refractor`'s claim-ceremony e2e family is non-deterministic at head** | Membership rotates over the three claim-ceremony e2es: `cap.roles.<target>` never gains the role-derived grant from the real `ClaimIdentity` op's `holdsRole` write within 25s. The test never waits for the adjacency edge the reprojection walks (the sweep e2e beside it does), so a lens pass can precede it. | ★★ | S–M | 📋 ready · owner: Whetstone · no repro on a quiet 4-core box · tighten, never loosen · [why](../../implementation-artifacts/retention-class-key-custody-design.md) §19.5 |
| **Suite reddens under parallel load, in packages the change never touched** | Rotating membership across unit-1, unit-2 and the convergence job. THREE mechanisms open: (a) `substrate.Connect`'s 2s no-retry handshake, ~45 sites remain; (b) a starvation signature (`found=map[]`); (c) a wall-clock DEADLINE read as correctness. (d) `privacy-pii-key-envelopes` race in `internal/leaseconvergence` FIXED. | ★★★ | M | 🏗️ owner: Whetstone · next: root-cause (b) |
| **`TestRefractor_E2E_P99` gates an absolute latency SLO on a shared runner** | NFR-P3's 500ms p99 is asserted by a unit test measuring wall-clock projection latency while three other jobs contend for the runner: CI run 31288862556 measured `10.03s`. A shared runner promises no latency floor, so the gate reads contention, not regression. | ★★ | S | 📋 ready · owner: Whetstone · reshape the measurement or move it off shared CI |
| **CI pipeline speed (continuous)** | Make CI faster without weakening any gate — owned continuously by the **Whetstone**. Ten parallel jobs; unit sharded 4 ways by measured `go test` time, `internal/natsperm` carved out with its own `-parallel`; lease-convergence's async trio parallelized as its own group. | ★★ | M (ongoing) | 🏗️ continuous (Whetstone) · `convergence` (164s) and `unit-4` (162s) now within 2s — no single pole · next: re-measure, both are candidates |

### Parking lot — very low priority (far, far back)

Rolled to [archive/lattice-parked.md](archive/lattice-parked.md) — real but low-value; no design or build
effort without an Andrew greenlight. A row that acquires a real driver comes back here.

## Done log — lattice (newest first)

One line per shipped item (`date · SHA · [tag] title`). Oldest roll to `archive/` past ~25.

- 2026-08-22 · `1509cd1` · [Core] capability-kv single read path CLOSED — 4 restatements of §6.1 routing folded in + a 2-check lint gate; 3 cold reviews + close pass
- 2026-08-22 · `9c24c918` · [natsperm] ack-plane read primitive CLOSED — registry covers both wire forms owner-scoped, app tier loses the grant, LEADER.STEPDOWN closed; 3 cold reviews, MAJOR was a false KNOWN LIMIT claim
- 2026-08-22 · `0e4db7d` · [Edge] first-paint gate identity CLOSED — a per-cycle level gate on the burst's end position; 3 cold reviews found 2 blocking defects + the blocker's own untested guard; 14 mutations pinned
- 2026-08-22 · `dea6550` · [Pkgmgr] capability-apply removal refusal CLOSED — coverage guard + `ApplyCapabilityPlan` as the only entry point; 3 cold reviews rewrote the ratified predicate, guard order and §3.4; 16 mutants pinned
- 2026-08-21 · `f28f832` · [Processor/Pkgmgr] §2.5 floor template-coverage CLOSED — `{me.*}` compiles to whole-segment patterns; install gate pins the vocabulary; review caught a submitter-steerable exclusion, fixed pre-ship
- 2026-08-21 · `ea28627` · [Pkgmgr] install-path double bucket-list CLOSED — checkCoreBucketExists → KVStatus probe; findInstalledPackage → `vtx.package.` prefix-list + batched KVGetMulti; cold review clean
- 2026-08-21 · `814e4c8` · [CI] substrate backfill ack-floor race CLOSED — `Ack()`→`DoubleAck(ctx)`; async-ack raced the before-snapshot, not a handler reattach
- 2026-08-21 · `dd22e08c` · [Pkgmgr] uninstall secure-lens attestation CLOSED — operator-attested `UninstallOptions`, CLI/Loupe/UI; 4 cold reviews found 2 bypasses; guard-vs-oracle agreement test mechanizes the class
- 2026-08-21 · `84d5aee` · [CI/hellolattice] main-red CLOSED — model-runner's 3 packages sorted into a unit shard (tests had never run in CI); the 2s NFR-P3 guard no longer spans lens cold start; 3 title-keyed Postgres polls key-scoped
- 2026-08-21 · `2df02bfd` · [model-runner/bridge/pkgmgr] NL-1 — model-runner service (sole vendor credential) + real capabilityAuthor adapter + authored-target dispatch-authority containment; 4 cold reviews, mutation-proven; env-opt-in. CI green
- 2026-08-21 · `a0b5238` · [Tooling] history-comment gate reads the whole comment — mid-sentence clauses + the Before-the-fix family now screened; 13 comments repaired, 2 noisy candidates rejected on measurement
- 2026-08-21 · `00a4a73` · [Pkgmgr] reinstall-over-uninstall false green CLOSED — install + dry-run refuse on occupied keys; 3 cold reviews fixed a remedy that restored nothing + 5 surviving mutations
- 2026-08-21 · `934352d9` · [privacy-base] verification tooling CLOSED — package gate (140 assertions, in CI) + live 4-step erasure ceremony (35); reinstall-grant gap filed
- 2026-08-21 · `ceb47fb` · [CI] whole-tree gofmt gate — every `scripts/*.go` is `//go:build ignore`, so golangci-lint's gofmt linter never loaded that tree; the step also fails on an unparseable file
- 2026-08-21 · `833a7427` · [Pkgmgr] byte-exact packageName CLOSED — near-miss refuses loudly; review refuted folding the resolver (deny-list vs destructive-resolver polarity) and the shared proposal_string strip

- 2026-08-21 · `f793bc55` · [Pkgmgr] manifest-oracle coverage CLOSED — secure-column history fail-closed on drop/rename + 2 review-executed bypasses; manifest retentionClasses; reserved RLS names on plain Columns/IntoKey

- 2026-08-21 · `8f49c13b` · [Pkgmgr/Weaver/Loupe] weaverTarget `.description` aspect end-to-end — installer emission, Studio field + intent, roster render, 25-target backfill + lint gate; live diff-apply verified. CI green
- 2026-08-21 · `a0bc24e` · [Refractor] two miscompiled clause shapes CLOSED — `*` projection bodies + required MATCH binding nothing new refused at parse; corpus census green, 2 pins moved not loosened
- 2026-08-21 · `ad18c5a5` · [privacy-base] pre-narrowing shred residue CLOSED — TombstoneOrphanedCredentialIndex + CLI sweep, both directions; 3-layer review found+fixed outbound gap + robustness bugs; owner-array hygiene follow-on filed
- 2026-08-21 · `f60565cf` · [Tooling] lint-doc-orphan gate — twice-seen orphaned-doc class mechanized; 9 genuine orphans found + repaired; lint-package-version narrowed to ignore comment-only pkgmgr edits
- 2026-08-21 · `603eae2c` · [Refractor] behavior-frozen consolidation CLOSED, whole scope — `pipeline.go` 3932→1326 + `executor.go` 2426→1407 over 6 increments, plus the test-fixture fold; no successor row
- 2026-08-21 · `a7c94ef` · [CI] lease-convergence's async trio parallelized as its own group — convergence job's pole; same 14 tests, no gate weakened
- 2026-08-21 · `fc49b7c` · [CI] `internal/natsperm` given its own `-parallel` budget — the unit-4 pole; same 1501 vectors, no gate weakened
- 2026-08-16 · (verification, no SHA) · [privacy-base] merge-concurrent-erasure-step-1 row REMOVED — already closed by `a0d762f3`'s dual-condition `write_path_closed` gate
- 2026-08-16 · `71cb8136` · [Pkgmgr] secureColumns holderTypes narrowing CLOSED — union-at-write-site both diffManifest branches; review found+fixed an unsound revive-branch exclusion; 2 adjacent hazards filed
- 2026-08-15 · `717312ca` · [Perf] Lattice-slice enumeration-corpus KVGetMulti sweep CLOSED — 9 pkgmgr/weaver/loom/refractor-health sites batched; 2 deferred as their own rows; Loupe/Verticals shares are those streams' pick
- 2026-08-15 · `c51746ec` · [Pkgmgr] AI-capability apply platform-package guard CLOSED — review found+fixed a real bypass: both Loupe apply/mark-applied handlers routed around the plan builder's deny-list; normalization gap filed separately

- *(older rolled to [archive/lattice-done.md](archive/lattice-done.md); newest `b4eb8fb2`)*
