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
| **[Processor] The NFR-S6 tail: payload prices the release quantum; membership is a hardcoded list** | The caller prices work inside the release window (`env.Payload` decoded 2–4× there, unbounded below 1 MiB), so the quantum boundary is positionable — the ratified design says so; two shipped code comments say otherwise. Membership stays: the "dissolve" direction is falsified. | ★★ | M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/nfr-s6-release-quantum-payload-design.md) |
| **[Bootstrap] Reconcile creates + updates but never removes a retired kernel key** | A kernel entity the current binary does not build stays live and executable: a dispatchable DDL, a running lens pipeline, a held canonicalName. No wipe-free shrink path. | ★ | S–M | 🗄️ shelved (Inc 2 retire verb) · [design](../../implementation-artifacts/kernel-orphan-retirement-design.md) · Inc 1 detector shipped, census 0/0 both buckets; needs a binary-version floor |
| **[Loupe] A `newPackage` proposal is closed over a same-named package it never wrote** | Live (the Studio path is ungated) and shared by the apply 409 AND the mark-applied recovery. No primitive missing: the durable receipt is computed and discarded — `InstallPackage` drops the reply's `RequestID`/`OpTrackerKey`/`Revisions` after `.Status`. Shape: thread it through `ApplyResult`, stamp it as a no-TTL aspect on the proposal vertex; name+version stays the legacy fallback. | ★★ | M | 📋 ready · [triage §3](../../../docs/reviews/lattice-designer-triage-2026-08-27.md) |
| **[Pkgmgr] No additive (partial-Definition) package apply** | A capability proposal can create a package, never add to one: `Apply`'s in-place branch is whole-Definition convergence. Needs a per-key origin stamp + a removal verb to be sound. | ★ | L | 🗄️ shelved · trigger: the first shipped producer emitting `target.mode: upgradeExisting` for a NEW artifact · [why](../../implementation-artifacts/capability-apply-removal-refusal-design.md) §4 |
| **[Pkgmgr] An uninstalled package cannot be reinstalled — only refused** | Reinstall is a loud refusal naming the occupants, but the keys stay dead and the remedy it names restores grants only: uninstall is a one-way door, and the operator console offers it as a button. Reviving needs step 8's package-scope guard to resolve a tombstoned manifest. | ★★ | L | 🗄️ shelved (revive: a real recorded operator need to undo an uninstall) · false-green already closed (`00a4a73`) · [why](../../implementation-artifacts/package-restore-design.md) |
| **[identity-domain] A `credentialindex` with no `boundTo` link is residue nothing can walk** | Real (unenumerable by construction) — but the design already exists: the reachability increment of the ratified [credential-binding-plane-lifecycle design](../../implementation-artifacts/credential-binding-plane-lifecycle-design.md) §9, precondition (i) of the erasure-pattern direction. | ★ | S | 🚧 seq: that increment · [triage §4](../../../docs/reviews/lattice-designer-triage-2026-08-27.md) |
| **[Weaver] The sweep enumerates state keys, not declared×projected work** | One root, three symptoms: a never-dispatched gap leaves no key so the sweep never visits it (26/28 `clinicSiteBackfill` — blocks the verticals clinic row), a target-scoped config latch can't retire, and the contraction counts' restart-replay claim is false (~0 after every restart). | ★★★ | L | 📐 awaiting-Andrew · [design](../../implementation-artifacts/weaver-sweep-declared-work-enumeration-design.md) |
| **[Refractor] A varlength-hop lens re-evaluates every actor per CDC event, starving the auth plane** | Derivation statically refuses varlength (10 anchored + 6 plain lenses); a rebuild grinds for days while the sweep stays suppressed and co-tenant lenses lag (live: 15h suppressed, 19 lenses behind). Two blockers, not one: the two lenses in the live evidence are held by a second, masked conjunct. | ★★★ | L | 📐 awaiting-Andrew · [design](../../implementation-artifacts/varlength-anchor-derivation-design.md) |
| **[Loom] Terminal instance records are never pruned; the heartbeat 1,024-cap breakdown is the alarm** | `loom-state` accretes forever (9,260+ mostly-terminal cursors) — the Chronicler design's explicitly-deferred, never-filed follow-on. Shape: prune/TTL terminal cursors + count via the existing `instance.*.pattern` running-only pin index (also fixes `ListInstances`). | ★★ | S–M | 📋 ready · [triage §7](../../../docs/reviews/lattice-designer-triage-2026-08-27.md) |
| **[Tooling] `internal/` hardcodes package-owned operation names, undeclared** | Bare op-name literals across 12 non-test engine files. Four are deliberate core-owned policy maps but nothing says so, and two (`nfrS6Operations`, the Gateway's `rawCredentialCarveOut`) are byte-identical sets kept independently with no gate — a drift silently un-covers an enumeration oracle. Shape: default-deny the bare literal, author declares the category, mirroring `# read-posture:` (`lint-conventions.go:119`). | ★★ | S | 📋 ready · Andrew-asked 2026-08-25 |
| **[Processor] Nothing records what a DDL script actually read, so declared-Reads drift is unguardable** | Regex guards can't follow helpers (the norm in vertical packages); a static extractor is false-confidence-prone. Smallest honest guard: record actually-read keys on `ScriptContext` (the `LiveReads` plumbing shape), expose via testutil, each dispatch package's existing e2e asserts actual ⊆ declared ∪ sanctioned. Consolidates the verticals drift-guard row. | ★★ | S | 📋 ready · shape: [triage §6](../../../docs/reviews/verticals-designer-triage-2026-08-27.md) |
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
| **[natsperm] The app tier's read side is unrestricted — both planes** | `subscribe: [">"]` makes every app a live tap on `ops.>`, which carries sensitive mutations in PLAINTEXT (encryption is at commit step 6.5, after the lane). And a KV read is a *publish*, so the blanket `$JS.API.>` reads every bucket incl. all of Core KV. Subsumes the `capability-author-context` instance. | ★★★ | M | 🗄️ shelved (revive: app-tier NKey stops being trusted infra) · [why](../../implementation-artifacts/app-tier-transport-read-scope-design.md) |

### External-I/O maturity (bridge follow-ons)
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Loom] Sensitive-egress reads can't reach a link-discovered aspect** | `subject.<aspect>` egress declaration only covers the SUBJECT's own key (known at Loom dispatch time); a value discovered by walking a link inside the DDL script has no declaration path. A retention-class-custody workaround is also refused at the egress boundary. Unblocks the `verticals.md` executed-lease tenant-name gap. | ★★ | M | 📐 needs designer pass · no-pattern: link-discovered egress · [evidence](../../implementation-artifacts/lease-tenant-name-fire-brief.md) |

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
| **CI pipeline speed (continuous)** | Make CI faster without weakening any gate — owned continuously by the **Whetstone**. Eleven parallel jobs; unit sharded 4 ways by measured `go test` time, `internal/natsperm` now its own job (was a unit-4 step). | ★★ | M (ongoing) | 🏗️ continuous (Whetstone) · unit-1 pole cut 277s→170s (ad15f91); natsperm→own job (b558d16), unproven — CI never ran, see row below · next: measure once unblocked |
| **[Weaver] `GapActionSpec` has no `OptionalReads`, so a lazy-account-open gap can't declare its guard read** | A racing `missing_account` dispatch can't declare its guard-aspect read, so the clean `AccountAlreadyExists` path is unreachable — settles via a raw substrate conflict (retry cost, not correctness). Independent of the sweep cluster; downstream plumbing ships — five touch points. | ★ | S | 📋 ready · [triage §5](../../../docs/reviews/lattice-designer-triage-2026-08-27.md) |

### Parking lot — very low priority (far, far back)

Rolled to [archive/lattice-parked.md](archive/lattice-parked.md) — real but low-value; no design or build
effort without an Andrew greenlight. A row that acquires a real driver comes back here.

## Done log — lattice (newest first)

- 2026-08-27 · (triage, no code) · [Refractor] "wedged rebuild / event loss" row retired — refuted by `e63cff5` + a live Health-KV re-check; residue folded into the varlength-anchor row.
- 2026-08-27 · (triage, no code) · [Processor] Reads-template `:type` segment retired — DetachObject is served package-only by objects-base `derive_reads`; revive: a client-side type-extraction demand.
- 2026-08-27 · (live-stack op run) · [Bootstrap] stranded operator epoch `b153d120` tooled but never run — 42 edges revoked, 5 packages reinstalled (the tool's derived set, not the design's guess); unblocks 2 verticals.md rows.
- 2026-08-26 · `b558d163` · [CI] natsperm carved out of unit-4 into its own job — local build/vet/test green, CI-unproven (see flagged row)
- 2026-08-26 · `b153d120` · [Bootstrap] re-bootstrap-stranded-grants CLOSED — revocation CLI + reserved-name guards (both mint paths); lens residue detect-only; 4 cold reviews, one HIGH found+fixed
- 2026-08-26 · `8d039bdb` · [Contracts] six 🔭 contract-text flags adjudicated — #2 + #10 amendments ratified as public contracts; #9 timing + three #2 clauses rejected as implementation prose
One line per shipped item (`date · SHA · [tag] title`). Oldest roll to `archive/` past ~25.

- 2026-08-25 · `f12c428` · [rbac/Pkgmgr] grant-edge provenance CLOSED — the `grantedBy` edge stamps origin at both channels; five-class reconciler + CI gate on the edge plane; kernel-regrant rule not keyed on the stamp; 3 cold reviews
- 2026-08-25 · `ffd7769` · [Weaver] the `data:` latch's clears re-paired to facts CLOSED — template fact split off the gap column's read, retirement at all three close legs, clock-keyed log pacing that dates the issue; 6 MAJORs over 3 reviews
- 2026-08-25 · `ad15f91` · [CI] unit-1/unit-3 shard rebalance SHIPPED — refractor growth made unit-1 the pole (332s vs 154s summed); moved 84s of whole-package globs to unit-3; unit-1 277s→170s, run 282s→198s
- 2026-08-25 · `866f623` · [Weaver] a migrated `surface` gap's stranded state acted on by three legs CLOSED — `handleRow` skipped the column and silenced its Surface issue; `escalateExhaustedGap` guarded inside, `reclaim` leaves the mark to TTL
- 2026-08-25 · `057286f` · [Weaver] exhausted-gap durable stop + un-park CLOSED — alert re-derived from the budget that suppresses it, re-arm narrowed to a zeroed budget, operator verb + its capability verb; 6 review rounds
- 2026-08-25 · `d835bdf9` · [Bootstrap] prior-epoch operator-role detection CLOSED — the cross-epoch orphan class the `vtx.meta.>` census cannot reach; ranked by what it confers, hard gate only in verify-kernel; 3 cold reviews + close pass
- 2026-08-25 · `c69aa4a4` · [Processor/facet] NFR-S6 declared-read set CLOSED — descriptor-named keys only, refused at step-4 head; grammar-checked, union-attributed, over-deny made audible; facet's self-key padding removed; 3 cold reviews
- 2026-08-24 · `b9121e8` · [Weaver/Pkgmgr] inflight_<g>⇒maxretries_<g> companion pair GATED at install — directOp only; privacy-base's inert const-false markers replaced by reach-derived caps
- 2026-08-24 · `3a35bde` · [Weaver] inflight_<g> suppression-vs-reclaim contract CLOSED — the error fired on a contract-legal declaration; class+key+prefix retired, verdict and two-leg suppression unchanged; cold review clean
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

- *(older rolled to [archive/lattice-done.md](archive/lattice-done.md); newest `f28f832`)*
