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
| **[Contract #2] §2.5 does not state the `derive_reads` contradiction refusal** | The clause enumerates derive_reads' failure modes and a descriptor contradicting its own derivation is not among them; both neighbouring rules resolve toward demote where the Processor now refuses. Behaviour already ships (`44d42a7`); the text does not. | ★ | XS | 🔭 flag-for-Andrew · proposal branch `claude/contract-2-5-derive-reads-refusal` (branch-vs-main diff IS the proposal) · ratify = merge |
| **[Contract #9] NFR-S6 states a rejection's SHAPE but not its TIMING** | §9.3 calls the generic-reply-code collapse anti-enumeration, but constrains only the wire shape; the causes were measured to separate by 0.27–0.70 ms, so the oracle is reachable without defeating the shape. Implementation shipped `624d445`. | ★★ | XS | 🔭 flag-for-Andrew · proposal branch `claude/contract-9-nfr-s6-timing-invariant` (branch-vs-main diff IS the proposal) · ratify = merge |
| **[Processor] A submitter prices the work inside an NFR-S6 op's release quantum** | Declared reads (cap 1000) resolve inside the quantized window and the Gateway copies `contextHint` verbatim, so padding can shift a rejection across a boundary. Quantization bounds this to a boundary-crossing probability; full closure needs a CLOSED declared-read set. | ★★ | M | 🔭 flag-for-Andrew · Contract #2 §2.5 must refuse (not demote) an out-of-set declared read · detector shipped: `claim_floor_late_total` |
| **[Bootstrap] Reconcile creates + updates but never removes a retired kernel key** | A kernel entity the current binary does not build stays live and executable: a dispatchable DDL, a running lens pipeline, a held canonicalName. No wipe-free shrink path. | ★ | S–M | 🗄️ shelved (Inc 2 retire verb) · [design](../../implementation-artifacts/kernel-orphan-retirement-design.md) · Inc 1 detector shipped, census 0/0 both buckets; needs a binary-version floor |
| **[Bootstrap] A re-bootstrap strands the prior epoch's operator grants** | Primordial ids are minted per generation, so regenerating `lattice.bootstrap.json` mints a NEW `operator` role while the prior one and its `grantedBy` permissions stay live with no holder. Live: 21 grants reachable only from the dead role (`AttachObject`, ledger creates, the erasure set); `bootstrap verify` passes green. LoftSpace-requested. | ★★ | S–M | 📐 needs designer pass · no-pattern: cross-epoch primordial reconciliation · [adjacent](../../implementation-artifacts/kernel-orphan-retirement-design.md) |
| **[Loupe] A `newPackage` proposal is closed over a same-named package it never wrote** | The apply endpoint's recovery branch reads "installed at the target version" as "this proposal's install committed"; for `newPackage`, name+version cannot separate that from a pre-existing stranger (`review.go:862-878`). | ★★ | M | 📐 needs designer pass · no-pattern: a durable proposal→install binding readable before mark-applied · [why](../../implementation-artifacts/capability-apply-removal-refusal-design.md) §3.4 |
| **[Pkgmgr] No additive (partial-Definition) package apply** | A capability proposal can create a package, never add to one: `Apply`'s in-place branch is whole-Definition convergence. Needs a per-key origin stamp + a removal verb to be sound. | ★ | L | 🗄️ shelved · trigger: the first shipped producer emitting `target.mode: upgradeExisting` for a NEW artifact · [why](../../implementation-artifacts/capability-apply-removal-refusal-design.md) §4 |
| **[Pkgmgr] An uninstalled package cannot be reinstalled — only refused** | Reinstall is a loud refusal naming the occupants, but the keys stay dead and the remedy it names restores grants only: uninstall is a one-way door, and the operator console offers it as a button. Reviving needs step 8's package-scope guard to resolve a tombstoned manifest. | ★★ | L | 🗄️ shelved (revive: a real recorded operator need to undo an uninstall) · false-green already closed (`00a4a73`) · [why](../../implementation-artifacts/package-restore-design.md) |
| **[identity-domain] A `credentialindex` with no `boundTo` link is residue nothing can walk** | It carries its identity in its body with no link to it, so no enumeration reaches one; the sweep covers only those with a `boundTo`. §9.2(i) names the class; the attestation does not. | ★ | S | 📐 needs designer pass · no-pattern: a link making `credentialindex` owner-reachable · operator CLI already prefix-lists it; the lens + attestation cannot · [why](../../implementation-artifacts/erasure-orchestration-design.md) §9.2(i) |
| **[Weaver] A data-issue column that stops being READ strands its entry** | A per-row `data:` issue is retired by its reader, so a column no longer read at all — drop a target's `Admission` block and `admitGap` returns before `intColumn` — leaves a standing `RowDataError` until the row is deleted. | ★ | M | 📐 needs designer pass · no-pattern: per-delivery re-derivation of a row's data-issue family · from the `data:` split (`173de5cc`) |
| **[Weaver] `gapSuppressed`/`staleMark` disagree on a human-task gap declaring `inflight_<g>`** | `gapSuppressed` (evaluator.go:1008) trusts any declared `inflight_<g>`; `staleMark` (evaluator.go:326-362) calls the same declaration on a human gap a lens-authoring bug, alerting `InflightActionMismatch` — standing since 2026-08-21 on lease-signing's onboarding/signature gaps. | ★★ | S–M | 📐 needs designer pass · no-pattern: one suppression-vs-reclaim contract for `inflight_<g>` on a non-external gap · consolidates 2 verticals.md rows |
| **[Refractor] An actor-aggregate lens with a variable-length hop enumerates every actor per CDC event** | Derivation statically refuses (`pattern carries a variable-length relationship`), so each event re-evaluates the whole actor set — the same root that forces the broad consumer filter. | ★★ | M–L | 📐 needs designer pass · no-pattern: anchor derivation across a variable-length relationship · [why](../../implementation-artifacts/capability-plane-rebuild-throughput-design.md) §5 |

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
| **[capability-author] An authored Starlark artifact can launder sensitive plaintext into a non-sensitive aspect** | An undeclared `kv.Read` decrypts (`starlark_kv.go:424`), step 6's egress guard permits deriving into an ordinary domain event, and step 6.5 keys encryption on the DESTINATION DDL — so it stores as plaintext and an authored lens projects it. | ★★★ | L | 🗄️ shelved · [why](../../implementation-artifacts/authored-artifact-admission-model-design.md) §5.5 |
| **[rbac] A `grantedBy` link carries no provenance, so a forged grant edge reconciles clean** | Authorization travels the `lnk.permission.*.grantedBy.role.*` edge, not the vertex. `GrantPermission` accepts any live permission key and any live role key with no manifest check, and links carry no `origin` stamp — so the cheapest escalation forges no vertex at all and the permission reconciler (`9470ab24`) reports clean by design. | ★★ | M | 📐 needs designer pass · no-pattern: provenance on a `grantedBy` link · surfaced by the reconciler's cold review |
| **[natsperm] Server-published bytes defeat every deny, for every component** | A message the server publishes for a client carries no permissions — a reply subject, a PubAck, a `RePublish` dest. Probed: `clinic-app` (no flag) writes `core-kv`, `capability-kv`, `ops.>`; `facet` (no `$JS.API.>`) writes too; `RePublish` forges an ack. | ★★★ | L | 🗄️ shelved · accepted-and-pinned; needs active exploitation by a trusted component binary · [why](../../implementation-artifacts/protected-consumer-ack-plane-denies-design.md) §8 |
| **[bootstrap] A package-plane actor can forge a package-origin permission and grant it to itself** | `origin:"package"` is client-supplied and buys the Contract #6 reserved-set exemption; nothing ties a created `vtx.permission.*` to authority the submitter may confer. Reachable from `UpgradePackage`'s create arm and from any package-authored DDL script. Owns the `grantedBy`-revival gap. | ★★★ | M | 🗄️ shelved (revive: consoleOperator delegated below root) · [design](../../implementation-artifacts/package-authority-minting-provenance-design.md) |
| **[capability-author] Admission holes let an authored artifact reach the auth plane** | Each guard derives its governed set from a declaration source, not from the consumer: the bucket deny-list misses every package/app-owned bucket, `protectedDispatchSets` reads manifests so the 6 kernel-seeded ops stay dispatchable, and apply re-runs no kind-specific validation at all. | ★★★ | L | 🗄️ shelved · no revive trigger (Andrew) · holes behind dormant AI-authoring (`BRIDGE_CAPABILITY_AUTHOR=real`) · [why](../../implementation-artifacts/authored-artifact-admission-model-design.md) |
| **[natsperm] `STREAM.CREATE` is unscoped, so a mirror + `RePublish` reaches any subject** | `protectedStreamDenies` binds registered stream names only; a stream under any other name carries its mirror source in the request BODY, and its `RePublish` dest lands chosen bytes anywhere — `$JS.ACK.` included. Also defeats bridge's core-KV read denies. | ★★★ | M | 🗄️ shelved · precedent: `protectedStreamDenies` · needs the runtime stream-creation census first · [why](../../implementation-artifacts/protected-consumer-ack-plane-denies-design.md) §8.4 |
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
| **Suite reddens under parallel load, in packages the change never touched** | Rotating membership across unit-1, unit-2 and the convergence job. THREE mechanisms open: (a) `substrate.Connect`'s 2s no-retry handshake, ~45 sites remain; (b) a starvation signature (`found=map[]`); (c) a wall-clock DEADLINE read as correctness — specimen: `TestRefractor_LeaseSigningConvergence_ProjectsScalarColumns` (20s lens activation, unit-1, green on re-run). (d) `privacy-pii-key-envelopes` race in `internal/leaseconvergence` FIXED. | ★★★ | M | 🏗️ owner: Whetstone · next: root-cause (b) |
| **CI pipeline speed (continuous)** | Make CI faster without weakening any gate — owned continuously by the **Whetstone**. Ten parallel jobs; unit sharded 4 ways by measured `go test` time, `internal/natsperm` carved out with its own `-parallel`; lease-convergence's async trio parallelized as its own group. | ★★ | M (ongoing) | 🏗️ continuous (Whetstone) · `convergence` (164s) and `unit-4` (162s) now within 2s — no single pole · next: re-measure, both are candidates |

### Parking lot — very low priority (far, far back)

Rolled to [archive/lattice-parked.md](archive/lattice-parked.md) — real but low-value; no design or build
effort without an Andrew greenlight. A row that acquires a real driver comes back here.

## Done log — lattice (newest first)

One line per shipped item (`date · SHA · [tag] title`). Oldest roll to `archive/` past ~25.

- 2026-08-24 · `624d445` · [Processor/identity-domain] claim-rejection timing oracle CLOSED — n=3000 overturned the same-day n=40 "no gap"; quantized release from receipt, NFR-S6 op set, reply key-echo closed; 3 cold reviews
- 2026-08-24 · `c44216c` · [Refractor] CapabilityRepairBlocked names its class — BlockedClass across fold/sweep/health; content+retraction error-on-sight, provenance stays warning, business-lens ceiling held; guard untouched; 3 cold reviews
- 2026-08-24 · `87cb2bb` · [Processor/identity-domain] NFR-S6 descriptor-floor drift gated — hostile probe derived from the descriptor, floored set pinned independently; 3 revert-proofs
- 2026-08-23 · `5a85ad7` · [Tooling] this-fire narration gate CLOSED — 5 phrases admitted on measurement, matched across a wrapped comment block; 160 sites swept, 113 AST-proven comment-only
- 2026-08-23 · `e3fc6b2` · [CI] `TestRefractor_E2E_P99` shared-runner contention CLOSED — moved off unit-1's `-p 4` batch into its own sequential step; isolated p99=50ms vs the 500ms budget
- 2026-08-23 · `69b48ba` · [Bridge] augur adapter registration CLOSED — escalation tier dead in every deployment; composition-root census gate, 3 families mutation-pinned; cold review caught forged model provenance
- 2026-08-23 · `62432f2` · [Refractor] dispositionEvalErr privacy-critical arm CLOSED — the tier fell through to Nak+nil (no pause, no alert, no backoff); mutation-proven
- 2026-08-23 · `9718dac7` · [Pkgmgr] live-vs-declared permission reconciliation CLOSED — key-based against declaredKeys, registry-anchored, five drift classes + a CI gate; 3 cold reviews found a fail-open and 3 false remedies
- 2026-08-23 · `a12fef1` · [Pkgmgr] grant-slice property-map gate CLOSED — parse-time propVars, one quote-aware scanner; cold review found a BLOCKING false refusal (sibling walks sharing `{k: false}`) + a smuggled accumulator
- 2026-08-23 · `a6e8cec` · [Weaver] malformed-anchor RowDataError CLOSED — per-entity key, level-driven raise/clear, registry-removal teardown; 4 review findings fixed, placement move-mutation pinned
- 2026-08-23 · `20264df` · [Tooling] ceremony 5s SLA CLOSED — 5 waits across 2 harnesses poll to a 3-min convergence ceiling, mirroring verify-erasure-ceremony
- 2026-08-23 · `960bb01` · [Docs] root actor set named by its predicate — 21 comments said "kernel-seeded" for a `holdsRole → operator` population; contracts untouched
- 2026-08-23 · `94daa9f` · [Process] batch review lessons routed — builder prompts ban the tree-wide git verbs; weaver teardown-route + pkgmgr one-fact-twice dossier entries
- 2026-08-23 · `e63cff5` · [Refractor] capability-plane rebuild throughput CLOSED — "wedged" refuted (throughput-bound); the phantom audit firehose gated on a positive `Committed` signal at every write site; varlength-anchor successor filed
- 2026-08-23 · `44d42a7` · [Processor/Tooling] derived-reads plane tail CLOSED — floor reaches derived reads (refuse, not demote), `state`/`ddl` fail closed, G2 covers `internal/`; 3 cold reviews, 47 mutations
- 2026-08-23 · `ac7cd921` · [Weaver] per-subject gap-issue key CLOSED — issueKeyGap split entity/config, every clear re-paired, heartbeat listing bounded; 13 per-line revert proofs, 3 needed isolation vectors
- 2026-08-23 · `fac3e381` · [identity-domain] the sweep's owner-array rewrite pins its read — precedent debt the mirror exposed, proven lossy then fixed
- 2026-08-23 · `6ebf0e83` · [identity-domain] erased credential leaves a live owner's sign-in list CLOSED — outbound-arm rewrite, per-arm sensitive declare, expectedRevision pin
- 2026-08-23 · `0d3c841c` · [Loom/Weaver/pkgmgr/bridge] enumeration delivering-lines proven — cold review reverted the guard + JSON tag green; bridge mirror, engine-load validation, candidate/catalog surfaces
- 2026-08-23 · `48957677` · [Loom/Weaver] dispatcher class-(e) enumerations CLOSED — Contract #2 already defined the field, nothing had populated it; five erasure walks declared at both dispatchers
- 2026-08-22 · `de193db` · [natsperm] `DeliverSubject` row GROUNDED + REFUTED — the real primitive is server-published bytes; folded into the ★★★ row
- 2026-08-22 · `a70fb07a` · [Contract #1] §1.5 "default class from localName" DELETED — clause was implemented nowhere and asserted key-decides-class against document-is-source-of-truth
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

- *(older rolled to [archive/lattice-done.md](archive/lattice-done.md); newest `f793bc55`)*
