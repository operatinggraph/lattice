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
| **[Pkgmgr] No additive (partial-Definition) package apply** | A capability proposal can create a package, never add to one: `Apply`'s in-place branch is whole-Definition convergence. Needs a per-key origin stamp + a removal verb to be sound. | ★ | L | 🗄️ shelved · trigger: the first shipped producer emitting `target.mode: upgradeExisting` for a NEW artifact · [why](../../implementation-artifacts/capability-apply-removal-refusal-design.md) §4 |
| **[Pkgmgr] An uninstalled package cannot be reinstalled — only refused** | Reinstall is a loud refusal naming the occupants, but the keys stay dead and the remedy it names restores grants only: uninstall is a one-way door, and the operator console offers it as a button. Reviving needs step 8's package-scope guard to resolve a tombstoned manifest. | ★★ | L | 🗄️ shelved (revive: a real recorded operator need to undo an uninstall) · false-green already closed (`00a4a73`) · [why](../../implementation-artifacts/package-restore-design.md) |
| **[identity-domain] A `credentialindex` with no `boundTo` link is residue nothing can walk** | Real (unenumerable by construction) — but the design already exists: the reachability increment of the ratified [credential-binding-plane-lifecycle design](../../implementation-artifacts/credential-binding-plane-lifecycle-design.md) §9, precondition (i) of the erasure-pattern direction. | ★ | S | 🚧 seq: that increment · [triage §4](../../../docs/reviews/lattice-designer-triage-2026-08-27.md) |
| **[Refractor] The `WITH`-scope refusal masks the two `cap-read.edgeManifest*` producers** | The generated producer re-binds a name a `WITH` dropped, so derivation refuses it — the second blocker behind the varlength one, and the lenses the live symptom actually names. Inc 2 narrows the refusal to a structural-identity whitelist. | ★★★ | S–M | 📋 ready · revived 2026-09-01: Inc 1 observed live, both producers still backlogged · [design](../../implementation-artifacts/varlength-anchor-derivation-design.md) §13 Inc 2 |
| **[Loom] `loom-state` accretes a terminal `instance.<id>` forever, and three paths enumerate the whole bucket** | Pruning stays out — cursor presence is the frozen-contract collapse point for Weaver's unbounded userTask re-dispatch. The live harm is enumeration: `ListInstances` is past `KVGetMulti`'s fast-path gate against a 5s handler timeout. | ★★ | M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/loom-instance-enumeration-bounding-design.md) |
| **[Weaver] One per-target issue budget is shared by the WORKLOAD and FAULT populations** | `rowIssueCapPerTarget`=500 spans `gap:`/`data:`/`template:`/`sweep:`, released only at zero. A `surface` gap's population is open work, so 500 unclaimed tasks fill it and the target's later faults are refused — `data:`/`sweep:` lost outright, and a refusal inverts two log seams (Error every sweep pass; Debug forever). | ★★ | M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/weaver-surface-workload-vs-fault-issues-design.md) |
| **[Pkgmgr] An AI-authored `weaverTarget` artifact has no static holder of `missing_*` ⊆ gaps** | `validateWeaverTargetArtifact` defers LensRef resolution by design and the artifact Definition carries no lens, so the new `lint-gap-column-declaration` gate cannot reach this path. Enforcing it needs validation to resolve an already-installed lens's columns — live state, where the artifact checks are pure today. | ★★ | M | 📐 needs designer pass · no-pattern: installed-lens column resolution at capability-artifact validation |
| **[Weaver] Build the ratified `{actor}` enumeration-hub token** | §10.8 admits `{actor}` under a transitional note; until built, a `{actor}` hub dispatches as a plain literal. Fire: engine substitutes it at plan time; refused outside a hub at install + load; declare `ReleaseOrphanedBooking`'s confinement walk + retire its baseline row; strike the note. | ★★ | S | 📋 ready · [why](../../implementation-artifacts/descriptor-declared-enumerations-design.md) §8.8 |
| **[Processor/Loom] A key discovered by a traversal cannot be declared on any surface** | Consolidates the link-discovered walk hub and the link-discovered sensitive egress: every declaration surface resolves its key at dispatch time, so a key reached by following a link is undeclarable by construction. Census: 47 of 102 baselined walk rows; blocks LoftSpace's executed-lease tenant name. | ★★ | L | 📐 awaiting-Andrew · [design](../../implementation-artifacts/declared-path-reads-design.md) |
| **[Weaver/Loom/Refractor] Retire the holder-less `control-operator` role** | Decided: its intended holder went to `consoleOperator` (a strict superset) one day after it shipped, two later ratified designs declined to use it, nothing references it. Recipe: `control-authz` version bump; `diffManifest` tombstones the orphans (two-way door); drop the lint allowlist entry. | ★ | XS | 📋 ready · [triage §9](../../../docs/reviews/lattice-designer-triage-2026-08-27.md) |

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
| **[capability-author] Authored-artifact admission holes — auth-plane reach + sensitive-plaintext laundering** | Merged pair, one doc, one dormancy: each admission guard derives its governed set from a declaration source rather than the consumer, and an authored script can launder sensitive plaintext into a non-sensitive aspect. | ★★★ | L | 🗄️ shelved · no revive trigger (Andrew) · behind dormant AI-authoring (`BRIDGE_CAPABILITY_AUTHOR=real`) · [why](../../implementation-artifacts/authored-artifact-admission-model-design.md) |
| **[natsperm] Server-originated publishes defeat the deny plane — server-published bytes + unscoped `STREAM.CREATE`** | Merged pair, one doc, one root: a message the SERVER publishes for a client carries no permissions (reply subjects, PubAcks, `RePublish` dests), and an unregistered-name stream's mirror + `RePublish` lands chosen bytes on any subject. | ★★★ | L | 🗄️ shelved · revive: active exploitation by a trusted binary, or the STREAM.CREATE runtime census · [why](../../implementation-artifacts/protected-consumer-ack-plane-denies-design.md) §8/§8.4 |
| **[bootstrap] A package-plane actor can forge a package-origin permission and grant it to itself** | `origin:"package"` is client-supplied and buys the Contract #6 reserved-set exemption; nothing ties a created `vtx.permission.*` to authority the submitter may confer. Reachable from `UpgradePackage`'s create arm and from any package-authored DDL script. Owns the `grantedBy`-revival gap. | ★★★ | M | 🗄️ shelved (revive: consoleOperator delegated below root) · [design](../../implementation-artifacts/package-authority-minting-provenance-design.md) |
| **[Processor] `contextHint.egressReads` is submitter wire no admission step inspects** | Class (f) is the only read class the platform carries OUT of the platform, and nothing refuses a declaration naming any identity's sensitive aspect. The emission end is lint-enforced across all 7 external-emitting ops; the API end is contained only by four client wire structs lacking the field, which no gate protects. | ★★ | S | 📐 awaiting-Andrew · [design](../../implementation-artifacts/egress-read-declaration-authority-design.md) |
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
| **[Refractor] Derivation licence for personal lenses** | The pattern-directed derivation is refused for every personal lens, so `edgeCatalog`/`edgeInstances` still expand a descriptor hub per event. Three refusals, not one: the two out-of-pattern inputs (only D1 has an edge — the Interest Set has none), a healer conjunct reading `sweeper` alone where `standingHealerInstalled` already exists, and a multi-walk lens handed no hop index at all. | ★★★ | L | ✅ ratified · [design](../../implementation-artifacts/personal-lens-derivation-licence-design.md) |
| **[Refractor] Three lenses are underivable — an untyped `-[r]->` matches any relation** | `objectLiveness`/`objectAttachments` and loftspace's Protected `objectIdentityAttachmentsRead` bind an untyped hop, so the anchor index refuses them and every neighbour event runs the relation-blind walk. The refusal's reason is false the way the varlength one was — the walk filters fetched edges, it never indexes by relation name. | ★★ | L | ✅ Andrew-ratified · [design](../../implementation-artifacts/untyped-hop-anchor-derivation-design.md) |

### AI-native
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **AI-authored capabilities — Fire 5 (auto-apply)** | Fires 1–4 ship the propose→validate→human-review→apply loop; Fire 5 would apply a high-confidence proposal with no human verdict. Design-only by Andrew. | ★★ | M | 🚧 Andrew-gated (design-only) · [design](../../implementation-artifacts/ai-authored-capabilities-design.md) |
| **The Augur — Fire 3 (autoApply)** | Fires 1+2a+2b close the escalate→review→dispatch loop with a human verdict in it; Fire 3 removes it for high-confidence remediations. | ★★ | M | 🚧 Andrew-gated · [design](../../implementation-artifacts/augur-design.md) + [dispatch](../../implementation-artifacts/augur-dispatch-pickup-design.md) |
| **[capability-author] One authoring request cannot co-propose a NEW lens + the target that binds it** | `RecordCapabilityProposal` records one `{kind,content}` per request and single-artifact apply resolves `lensRef` only by NanoID or same-Definition name — so a new-lens-plus-target intent can't produce both atomically. NL v1 binds an existing lens instead. | ★★ | L | 🗄️ shelved (revive: a real new-lens+target atomic intent, or a reported torn bundle) · two-step workaround shipped · [why](../../implementation-artifacts/capability-proposal-bundles-design.md) |

### Read-model / projection maturity
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Refractor/Weaver] Expiry is a fact on the CHECK, not a clock in the lens — sweep the `$now` lenses** | 16 lenses read `$now`, so their stored rows are claims about a past instant and the convergence sweep — their only verifier — cannot tell a broken projection from a moving clock. Fix: read the lapse `MarkExpired` already records, keyed per target, `freshUntil` included. | ★★★ | L | 📐 awaiting-Andrew (scope fork) · [design](../../implementation-artifacts/expiry-as-a-recorded-fact-design.md) |
| **Typed relation signatures — `containedIn: location→location`** | Declare a relation's endpoint types against the taxonomy, enforced at step 6 fail-closed; a signed variable-length hop contributes its endpoint expansion rather than clearing exhaustiveness. Held 2026-08-13: the payoff shrank to 2 lenses, both convertible by a single-hop rewrite (replacement row on verticals). | ★★ | L | 🗄️ shelved (revive: an intermediate containment level, or rewrite-unreachable varlength census) · [design](../../implementation-artifacts/typed-relation-signatures-design.md) |
| **[Refractor] Cross-instance projection-latency rollup** | Aggregate per-lens projection latency across Refractor instances into one per-component view; single-instance today, so the two coincide. | ★ | S | 🚧 seq behind HA-NATS multi-instance · tombstone half subsumed by the [link-aspect design](../../implementation-artifacts/link-aspect-triggered-reprojection-plain-lenses-design.md) |

### Refinements & ops
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Substrate] `drainDirectGetFallback` may report a short marked-node read as success** | Observed once, not reproduced in 20 runs: `TestNeighbors_MarkedNodeIsNeverQuietlyShort` failed its completeness assertion. Hypothesis: the drain's only success exit is `NumPending == 0` on the FIRST consumer info after creation. Falsify under `-p 4` load with `-count=50`. | ★★ | S | 📋 ready · owner: Whetstone · [design](../../implementation-artifacts/refractor-hub-walk-and-periodic-load-design.md) §8 |
| **`internal/refractor`'s claim-ceremony e2e family is non-deterministic at head** | Membership rotates over the three claim-ceremony e2es: `cap.roles.<target>` never gains the role-derived grant from the real `ClaimIdentity` op's `holdsRole` write within 25s. The test never waits for the adjacency edge the reprojection walks (the sweep e2e beside it does), so a lens pass can precede it. | ★★ | S–M | 📋 ready · owner: Whetstone · no repro on a quiet 4-core box · tighten, never loosen · [why](../../implementation-artifacts/retention-class-key-custody-design.md) §19.5 |
| **Suite reddens under parallel load, in packages the change never touched** | Rotating across unit-1/2 + convergence. Three open: (a) `substrate.Connect`'s 2s no-retry handshake, ~45 sites; (b) a `found=map[...]` starvation in leaseconvergence's 25s lens wait — seen EMPTY and PARTIAL (latest `EagerReopen`), so slow watch, not never-started; (c) a wall-clock DEADLINE read as correctness — three shapes now (20s + 25s lens waits, and a 5s Resume-drain in `TestSupervisor_PendingForConsumer`), so not lens-specific. | ★★★ | M | 🏗️ owner: Whetstone · next: root-cause (b) |
| **CI pipeline speed (continuous)** | Make CI faster without weakening any gate — owned continuously by the **Whetstone**. Eleven parallel jobs; unit sharded 4 ways by measured `go test` time, `internal/natsperm` now its own job (was a unit-4 step). | ★★ | M (ongoing) | 🏗️ continuous (Whetstone) · unit-1 pole cut 277s→170s (ad15f91); natsperm→own job (b558d16), unproven — CI never ran, see row below · next: measure once unblocked |
| **No gate can observe the 250ms production script wall** | `internal/testutil`'s `init()` assigns `processor.DefaultScriptWallBudget = 5s`, so all 24 packages linking it — cafe-domain's own `workplace_confinement_test.go` included — exercise their DDLs at 20× the budget the running Processor enforces. A walk that blows the wall live is green in CI; twice now it shipped and only live PO discovery found it (`0bda5e71`, `b997ff2a`). | ★★ | S | 📋 ready · owner: Whetstone |
| **A correctly-bounded confinement walk still blows the wall — no batched-read primitive exists** | Re-derived: staff Charge is 20–23 live RTs (doubled `leaseapp_unit` walk); all three batched substrate transports refuted against the pinned server on load axes quiet benches can't see — the primitive is refuted, not unbuilt. Build: café memo + paced live probe. | ★★ | S | ✅ ratified (Winston-adjudicated) · [design](../../implementation-artifacts/kv-links-listing-leg-collapse-design.md) |

### Parking lot — very low priority (far, far back)

Rolled to [archive/lattice-parked.md](archive/lattice-parked.md) — real but low-value; no design or build
effort without an Andrew greenlight. A row that acquires a real driver comes back here.

## Done log — lattice (newest first)

- 2026-09-01 · `8a2cee97` · [Refractor] executor reads a marked hub at the hop's relation, validator re-reads scoped; composed whole reads pin both ways; torn multi-walk footprints rejected — hub drain gone; pair's cost is the `$now` rescan
- 2026-09-01 · `1fca25cf` · [Refractor] pattern-scoped actor walk + Postgres `GetRow` + idle-aware periodic loops + rebuild registration race — 8 stuck personal lenses drain at 10–25 msg/s
- 2026-09-01 · (triage, no code) · [orchestration-base] duplicate-human-task row retired — the prescribed work-scoped dedup is REFUTED (3 shapes, code-cited); cause is an anchor-vs-work granularity mismatch, package fix filed on verticals
- 2026-08-31 · `044ac715` · [Contracts] full-corpus public-posture sweep DONE — all 15 files at promise altitude; 6 factual drifts fixed, ~40 dangling §refs retargeted, rule codified in contracts README
- 2026-08-31 · `571e45e6` · [Contracts] #10 §10.8 {actor} hub token ratified ahead of build — transitional note in text; build fire filed 📋 ready
- 2026-08-31 · `935492df` · [Contracts] #10 §10.8 ratified with posture trims — three-arm param grammar + directOp optionalReads land; gate narration and internal names stay out
- 2026-08-31 · `0da6c431` · [Contracts] #10-substrate redrive clause adjudicated — branch rejected as mechanism-for-mechanism; clause rewritten to the observable promise; full-file posture sweep queued
- 2026-08-30 · `9ab532a` · [packages] actor-role walk declared on 31 of 32 ops — baseline walks 130→99, holdsRole 32→1; fixtures resolve the hint from the spec; cold review, 1 MAJOR + 4 MINOR closed
- 2026-08-30 · `9d0bec7` · [CI] main un-reddened — cafe-app's Relocate action recorded in the op-literal ceiling
- 2026-08-29 · `bcc2681` · [Pkgmgr] descriptor-declared kv.Links walks SHIPPED — the fourth declaration surface end to end, cafe's 8 ops declaring through it, 7 baseline rows retired; 3 cold reviews, 1 BLOCKING severed hop + 4 MAJOR closed
- 2026-08-29 · `5699325` · [Processor/testutil] read-drift ratchet SHIPPED — scripts record what they actually read; guard armed on every CapabilityPipeline blocks new undeclared reads/walks; cold review: 1 BLOCKING + 4 MAJOR closed
- 2026-08-29 · `3c2e21c` · [Weaver] GapWithoutPlaybook orphan columns CLOSED — the two deliberate orphans declared `surface` + a projected-`missing_*` ⊆ gaps CI gate; 2 cold reviews, 1 BLOCKING + 3 MAJOR closed
- 2026-08-29 · `83eb57b` · [Loupe/Pkgmgr] capability-proposal install receipt — create-only `.install` binding; unproven newPackage close refused; 4 cold reviews, 5 BLOCKING/MAJOR closed
- 2026-08-29 · `343260a` · [Loom] redrive repaired (could never re-pin over the terminal DEL marker) + heartbeat count moved to the pin index; 3 cold reviews, 2 BLOCKING
- 2026-08-29 · `7d3e31b` · [Weaver] gap-action declaration surface CLOSED — optionalReads + the `json:<literal>` param token, refused at dispatch/load/install; 3 cold reviews, 1 BLOCKING privilege escape closed; ReplayTarget fix `0fd3e8f`
- 2026-08-28 · `7b68919f` · [Tooling] op-name declaration gate — internal/ + cmd/ declare package-owned verbs, carve-out ⊆ NFR-S6 pinned; 3 cold reviews, 1 BLOCKING; found+fixed 2 live claim-attempts accounting defects
- 2026-08-28 · `77650a8` · [Refractor] varlength anchor derivation Inc 1 — the derivation steps the ranged hop its executor already walks; 3 cold reviews, 3 BLOCKING found + closed (Min>1 seeding, empty expansion, work budget)
- 2026-08-28 · `81a1c94` · [Weaver] decline-retry CLOSED — declines Nak on a 5m floor by fix path + `ReplayTarget` verb; live `ReplayTarget clinicSiteBackfill` still owed (needs a live stack)

- 2026-08-28 · `2d1b7ef` · [Processor] NFR-S6 release quantum DELETED — timing equalized where it is made (script fails once, tombstoned sensitive read pays the live decrypt); 3 cold reviews, 2 MAJOR + T2 unmet found and closed
- 2026-08-27 · (triage, no code) · [Refractor] "wedged rebuild / event loss" row retired — refuted by `e63cff5` + a live Health-KV re-check; residue folded into the varlength-anchor row.
- 2026-08-27 · (triage, no code) · [Processor] Reads-template `:type` segment retired — DetachObject is served package-only by objects-base `derive_reads`; revive: a client-side type-extraction demand.
- 2026-08-27 · (live-stack op run) · [Bootstrap] stranded operator epoch `b153d120` tooled but never run — 42 edges revoked, 5 packages reinstalled (the tool's derived set, not the design's guess); unblocks 2 verticals.md rows.
- 2026-08-26 · `b558d163` · [CI] natsperm carved out of unit-4 into its own job — local build/vet/test green, CI-unproven (see flagged row)
- 2026-08-26 · `b153d120` · [Bootstrap] re-bootstrap-stranded-grants CLOSED — revocation CLI + reserved-name guards (both mint paths); lens residue detect-only; 4 cold reviews, one HIGH found+fixed
- 2026-08-26 · `8d039bdb` · [Contracts] six 🔭 contract-text flags adjudicated — #2 + #10 amendments ratified as public contracts; #9 timing + three #2 clauses rejected as implementation prose
One line per shipped item (`date · SHA · [tag] title`). Oldest roll to `archive/` past ~25.


- *(older rolled to [archive/lattice-done.md](archive/lattice-done.md); newest `f12c428`)*
