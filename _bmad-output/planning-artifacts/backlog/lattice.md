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
| **[Loom] `loom-state` accretes a terminal `instance.<id>` forever, and three paths enumerate the whole bucket** | The cursor is permanent by design; the live harm is enumeration: `ListInstances` is 12× past `KVGetMulti`'s 1,024-subject fast path and hits the 1 MB create-consumer wall near 22k instances. Fix: subject-filtered listings, a `failed` index, completed history from the read model. | ★★ | M | ✅ ratified 2026-09-02 (Andrew: split; seq: after the Loupe Flows-tab row) · [design](../../implementation-artifacts/loom-instance-enumeration-bounding-design.md) |
| **[Pkgmgr] An AI-authored `weaverTarget` artifact has no static holder of `missing_*` ⊆ gaps** | `validateWeaverTargetArtifact` defers LensRef resolution by design and the artifact Definition carries no lens, so the new `lint-gap-column-declaration` gate cannot reach this path. Enforcing it needs validation to resolve an already-installed lens's columns — live state, where the artifact checks are pure today. | ★★ | M | 📐 needs designer pass · no-pattern: installed-lens column resolution at capability-artifact validation |
| **[Weaver] Build the ratified `{actor}` enumeration-hub token** | §10.8 admits `{actor}` under a transitional note; until built, a `{actor}` hub dispatches as a plain literal. Fire: engine substitutes it at plan time; refused outside a hub at install + load; declare `ReleaseOrphanedBooking`'s confinement walk + retire its baseline row; strike the note. | ★★ | S | 📋 ready · [why](../../implementation-artifacts/descriptor-declared-enumerations-design.md) §8.8 |
| **[Processor/Loom] A key discovered by a traversal cannot be declared on any surface** | Every write-path declaration surface resolves its key at dispatch, so a link-reached key is undeclarable there, while the lenses already express such traversals. The one live consumer (the lease's tenant name) closes by a snapshot on the application instead. | ★★ | L | 🗄️ shelved (revive: a second traversal-discovered egress consumer no snapshot or lens can serve, with a root the envelope cannot steer) · [design](../../implementation-artifacts/declared-path-reads-design.md) |
| **[lease-signing/Loom] Supersede a background check automatically when its successor completes** | The primitive exists (`TombstoneSupersededLeaseServiceInstance`, operator-driven, ownership-checked); the durable rule needs the reply op to find the prior instance without a live enumeration, and a product answer on check history. | ★★ | M | 📐 needs designer pass · no-pattern: prior-instance discovery at reply time without a live enumeration · [design](../../implementation-artifacts/bgcheck-runaway-and-broad-filter-design.md) §6 |
| **[Bootstrap/Loom] The 1 s marker TTL is the delivery window of every TTL-expiry signal** | `LimitMarkerTTL=1s` on all six `PerKeyTTL` buckets means a `deadline.*` expiry (Loom's only rejected/lost-step signal) and a tracker expiry exist for one second; a consumer down across that second never sees it, no startup scan notices, and the deadline handler's Nak on a probe error asks for a redelivery of that same second-lived marker. Decide the value per bucket. | ★★★ | S | 📋 ready · [why](../../implementation-artifacts/loom-state-tombstone-sweep-design.md) §11 |
| **[Weaver/Loom/Refractor] Retire the holder-less `control-operator` role** | Decided: its intended holder went to `consoleOperator` (a strict superset) one day after it shipped, two later ratified designs declined to use it, nothing references it. Recipe: `control-authz` version bump; `diffManifest` tombstones the orphans (two-way door); drop the lint allowlist entry. | ★ | XS | 📋 ready · [triage §9](../../../docs/reviews/lattice-designer-triage-2026-08-27.md) |
| **[Weaver] The `unplannable` / no-playbook escalation doors have no pacing or booking model** | Only the `exhausted` door has an episode model (booked nowhere, paced, latched); the no-entry door books the count + an `__effect` slot and is orphan-deleted per lease, the no-plan door re-pins unpaced and never re-plans. Fix: one `escalateGap` seam, the mark declares its class, a release when the gap can act again. Zero `unplannable` consumers today. | ★★ | S–M | ✅ ratified (Winston-adjudicated) · [design](../../implementation-artifacts/weaver-escalation-episode-three-doors-design.md) |
| **[edge-manifest] `edgeCatalog` carries the whole descriptor vocabulary per row** | Every catalog row repeats the descriptor vocabulary (~2 KB × 26–97 rows per actor), so one actor's content pass is 50–200 KB of near-identical text; a vocabulary reference per row, or a per-actor vocabulary row, would cut it by an order of magnitude. Measured at the personal-lens delta T7. | ★ | S | 📋 ready · [why](../../implementation-artifacts/personal-lens-delta-publication-design.md) §13 |
| **[Refractor] `adjacency.upsertEdge` removes an edge by `EdgeID` with no ordering guard** | Deterministic `EdgeID`s: a revoke→re-grant reuses one, the removal has no sequence compare, a Nak'd message redelivers — an older tombstone after the newer create deletes a live edge, absent to every walk, the enumerator, the cap-read diff and the derived-anchor retraction walk (19 lenses, 5 PII tables). Fix: seq-guard removal + upsert per edge. | ★★★ | S–M | 📋 ready · [why](../../implementation-artifacts/perentry-unchanged-entry-withholding-design.md) §4.4 |

| **[Weaver] A goal-mode gap's dispatch shape never reaches `externalDispatchGap`** | Switches on `ga.Action` ([evaluator.go:616](../../../internal/weaver/evaluator.go:616)) — `""` for every goal gap — so `staleMark` never returns true for one, and three legs read that: `renewalComplete`'s external `refreshBgcheck` leg always reclaims collapse-only, and `reset-budget` refuses it forever. `resolvedLegAction` resolves it a line above two of the three sites. | ★★ | S–M | 📐 needs designer pass · no-pattern: dispatch-shape resolution for a goal gap at lane-1's pre-plan stale gate |

### Survey log (round-robin rotation)

Rotation memory only — findings are the filed rows; fire narratives live in commits, never here.
Components: Core · Weaver · Loom · Refractor · Bootstrap · object-store-manager (+ the cross-cutting feature
backlog). Survey the stalest (`git log -1 --format=%ct -- <path>`), note ONE dated line, rotate.

- 2026-07-18 Refractor (healthy; all 8 07-06-review findings already resolved — no new rows).
- 2026-07-19 object-store-manager (67.5/91.4% cov; filed doc-drift + cascade error-branch cov).
- 2026-07-19 Bootstrap (69.3% cov; filed stale-bootstrap-json-no-freshness-probe ★★ + seed-idempotency-branch-cov).
- 2026-07-19 Core (processor 81.8/substrate 76.2% cov; filed 3: supervisor-accessors, outbox-consumer cov, processor.md drift).
- 2026-07-25 Refractor (out of rotation) — filed shared-bucket rebuild-truncate hazard; next unchanged.
- 2026-09-05 Weaver (91.0% cov, zero TODOs; filed goal-mode dispatch-shape classifier + the planner mandate's Fire 9).
- **Next:** Loom.

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
| **[Processor] `contextHint.egressReads` is submitter wire no admission step inspects** | Class (f) is the only read class the platform carries OUT of the platform, and nothing refuses a declaration naming any identity's sensitive aspect. The emission end is lint-enforced across all 7 external-emitting ops; the API end is contained only by ten wire structs lacking the field, which no gate protects. | ★★ | S | ✅ ratified 2026-09-01 (Andrew: build; the contract lines land with the fire) · [design](../../implementation-artifacts/egress-read-declaration-authority-design.md) |
| **[natsperm] The app tier's read side is unrestricted — both planes** | `subscribe: [">"]` makes every app a live tap on `ops.>`, which carries sensitive mutations in PLAINTEXT (encryption is at commit step 6.5, after the lane). And a KV read is a *publish*, so the blanket `$JS.API.>` reads every bucket incl. all of Core KV. Subsumes the `capability-author-context` instance. | ★★★ | M | 🗄️ shelved (revive: app-tier NKey stops being trusted infra) · [why](../../implementation-artifacts/app-tier-transport-read-scope-design.md) |
| **[bridge] Egress-unwrap serves identity-custodied sensitive aspects only — a retention-class holder's `$sensitiveRef` permanently fails** | `resolveSensitiveRef` (`egress.go:190`) hard-rejects any non-identity holder; `piiKeyEnvelope` is `MATCH (i:identity)` only. A retention-class-custodied snapshot declares fine Processor-side but never reaches a vendor. Blocks verticals.md's lease-tenant-name row. | ★★ | S–M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/retention-class-egress-envelope-design.md) |

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

| **[Weaver] The planner mandate's Fire 9 — the Augur floor — is its last unbuilt fire, on no board** | Fires 1–8 shipped; §8's Fire 9 (`unplannable`/`exhausted` → Augur, plan-shaped proposals via the Fire-6 per-leg path, promotion) is tracked nowhere. The Augur materialises one `proposedAction`/`proposedParams` today, so §7(b)'s named case — a novel gap with no pre-authored decomposition — has no remediation path. | ★★ | M | ✅ ratified · seq: after the three-doors escalation row · [design §8](../../implementation-artifacts/weaver-planner-mandate-design.md) |

### Read-model / projection maturity
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Refractor/Processor] `capabilityEphemeral` is the last `$now` in the lens corpus** | Its three arms admit a task's grants only while `expiresAt > $now`; the two task-anchored targets already record the lapse on the task, so the lens reads that marker instead, the café stale-tab lens converts alike, and a corpus census pin refuses `$now` blocking. Safe today: the Processor re-checks `expiresAt`. | ★★ | S | ✅ ratified (Winston-adjudicated) · [design](../../implementation-artifacts/capability-ephemeral-recorded-expiry-design.md) |
| **[Refractor] A perEntry read-grant producer rewrites every unchanged entry of every reached actor** | The guarded adapter never skips an identical row, so every entry of every reached actor is rewritten per event: one `providedTo` create committed 4,993 of 5,938 staff entries; ~400 audited entries/s, 74 % of `REFRACTOR_AUDIT`, 213 M deletes on `capability-kv`. Fix: withhold an equal fresh entry + two refusals in `Reproject`'s delete arm. | ★★★ | S–M | 🏗️ building · owner: claude/bold-tesla-ras9ml · [design](../../implementation-artifacts/perentry-unchanged-entry-withholding-design.md) §15 · next: Inc 1 refusals |
| **[Refractor] A neighbour-keyed plain lens that genuinely partitions by anchor is still refused** | `landlordLeaseApplicationsRead`'s composite `(app_id, landlord_id)` key binds the non-anchor half and falls back to whole-corpus rescan even though its rows partition cleanly (bucket G, `with-alias-anchor-closure-design.md` §2.1). Blocks verticals.md's landlord-visibility row. | ★★★ | S–M | ✅ ratified (Winston-adjudicated) · [design](../../implementation-artifacts/anchor-partitioned-plain-lens-retraction-design.md) |
| **[Refractor] The three partition-shaped grant tables keep a whole source-scoped diff per event** | `providerIdentityReadGrants`, `staffReadGrants`, `patientIdentityReadGrants` partition by anchor but stay whole-diff: that per-event diff is an un-truncatable grant table's only shrink path on a rebuild. Needs a grant-writer rebuild diff (or truncate) + `ListKeysWhere`. | ★ | S | 🗄️ shelved (revive: a measured grant-table event cost, or a grant-writer rebuild diff) · [why](../../implementation-artifacts/anchor-partitioned-plain-lens-retraction-design.md) §3.7 |
| **Typed relation signatures — `containedIn: location→location`** | Declare a relation's endpoint types against the taxonomy, enforced at step 6 fail-closed; a signed variable-length hop contributes its endpoint expansion rather than clearing exhaustiveness. Held 2026-08-13: the payoff shrank to 2 lenses, both convertible by a single-hop rewrite (replacement row on verticals). | ★★ | L | 🗄️ shelved (revive: an intermediate containment level, or rewrite-unreachable varlength census) · [design](../../implementation-artifacts/typed-relation-signatures-design.md) |
| **[Refractor] Cross-instance projection-latency rollup** | Aggregate per-lens projection latency across Refractor instances into one per-component view; single-instance today, so the two coincide. | ★ | S | 🚧 seq behind HA-NATS multi-instance · tombstone half subsumed by the [link-aspect design](../../implementation-artifacts/link-aspect-triggered-reprojection-plain-lenses-design.md) |

### Refinements & ops
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Substrate] `drainDirectGetFallback` may report a short marked-node read as success** | Observed once, not reproduced in 20 runs: `TestNeighbors_MarkedNodeIsNeverQuietlyShort` failed its completeness assertion. Hypothesis: the drain's only success exit is `NumPending == 0` on the FIRST consumer info after creation. Falsify under `-p 4` load with `-count=50`. | ★★ | S | 📋 ready · owner: Whetstone · [design](../../implementation-artifacts/refractor-hub-walk-and-periodic-load-design.md) §8 |
| **`internal/refractor`'s claim-ceremony e2e family is non-deterministic at head** | Membership rotates over the three claim-ceremony e2es: `cap.roles.<target>` never gains the role-derived grant from the real `ClaimIdentity` op's `holdsRole` write within 25s. The test never waits for the adjacency edge the reprojection walks (the sweep e2e beside it does), so a lens pass can precede it. | ★★ | S–M | 📋 ready · owner: Whetstone · no repro on a quiet 4-core box · tighten, never loosen · [why](../../implementation-artifacts/retention-class-key-custody-design.md) §19.5 |
| **Suite reddens under parallel load, in packages the change never touched** | Rotating across unit-1/2 + convergence. Two open: (a) `substrate.Connect`'s 2s no-retry handshake, ~45 sites; (c) a wall-clock DEADLINE read as correctness — two shapes remain (a 20s lens wait, a 5s Resume-drain in `TestSupervisor_PendingForConsumer`). (b) `found=map[...]` leaseconvergence lens-wait starvation FIXED — root cause is CoreKVSource's serial MaxPrefetch:1 replay (~20 events, each a network round trip); 25s→90s. | ★★★ | M | 🏗️ owner: Whetstone · next: root-cause (a) or (c) |
| **CI pipeline speed (continuous)** | Make CI faster without weakening any gate — owned continuously by the **Whetstone**. Thirteen parallel jobs; unit sharded 4 ways by measured `go test` time, `internal/natsperm` + `internal/refractor` each their own job. | ★★ | M (ongoing) | 🏗️ continuous (Whetstone) · `internal/refractor` split off unit-1 into unit-refractor (1225c84c): 235s avg → 188s, full green · next: re-measure the pole once this split settles |
| **No gate can observe the 250ms production script wall** | `internal/testutil`'s `init()` assigns `processor.DefaultScriptWallBudget = 5s`, so all 24 packages linking it — cafe-domain's own `workplace_confinement_test.go` included — exercise their DDLs at 20× the budget the running Processor enforces. A walk that blows the wall live is green in CI; twice now it shipped and only live PO discovery found it (`0bda5e71`, `b997ff2a`). | ★★ | S | 📋 ready · owner: Whetstone |
| **A correctly-bounded confinement walk still blows the wall — no batched-read primitive exists** | Re-derived: staff Charge is 20–23 live RTs (doubled `leaseapp_unit` walk); all three batched substrate transports refuted against the pinned server on load axes quiet benches can't see — the primitive is refuted, not unbuilt. Build: café memo + paced live probe. | ★★ | S | ✅ ratified (Winston-adjudicated) · [design](../../implementation-artifacts/kv-links-listing-leg-collapse-design.md) |
| **[Processor] The confinement/authority-walk wall fix (row above) is café-only** | Its ratified Inc A memoizes only café's `leaseapp_unit` double-call. Clinic's `workplace_exempt` walk and LoftSpace's renewal→leaseapp→unit→`manages` chain are structurally different, absent from Inc A's census, and still blow the wall once it ships. | ★★★ | M | 📐 needs designer pass · no-pattern: a generalized authority-walk primitive beyond café's shape |

### Parking lot — very low priority (far, far back)

Rolled to [archive/lattice-parked.md](archive/lattice-parked.md) — real but low-value; no design or build
effort without an Andrew greenlight. A row that acquires a real driver comes back here.

## Done log — lattice (newest first)

- 2026-09-05 · `8a43aa9d` · [Refractor] personal-lens delta publication SHIPPED — rows by provenance, frames per pass, silent rebuild (`edgeCatalog` −94.6 %) ([design](../../implementation-artifacts/personal-lens-delta-publication-design.md))
- 2026-09-05 · `1225c84c` · [CI] split `internal/refractor` off unit-1 into its own job (unit-refractor) — CI wall-clock 235s avg → 188s, full green
- 2026-09-05 · `424e2740` · [Refractor] Secure plain lenses audit under a mask + retract by derivation; the neighbour-retraction transport gate ([design](../../implementation-artifacts/secure-plain-lens-retraction-and-audit-design.md))
- 2026-09-05 · `89b61556` · [Weaver] an exhausted goal gap re-plans at its leg boundary; the budget books attempts, the escalation books nothing and is paced ([design](../../implementation-artifacts/weaver-exhausted-gap-leg-boundary-design.md))
- 2026-09-04 · `3c54ddb3` · [Weaver] a surface gap is ONE counted entry per (target, gap column); refused raises paced, overflow windowed ([design](../../implementation-artifacts/weaver-surface-workload-vs-fault-issues-design.md))
- 2026-09-04 · `ade79cee` · [Refractor/objects-base] an untyped hop is a wildcard: objectLiveness on the `vtx.object.>` filter, objectAttachments derives live ([design](../../implementation-artifacts/untyped-hop-anchor-derivation-design.md))
- 2026-09-04 · `d9db9deb` · [Processor/Bootstrap] the write gate reads the STORED class; the kernel's 12 topology links protected ([design](../../implementation-artifacts/stored-class-write-gate-and-kernel-topology-protection-design.md))
- 2026-09-04 · `c38af84` · [CI] lint-build split into lint-build + lint-static, parallel not serial — 131s/163s vs old combined 171–206s, full CI green (run 33893636046)
- 2026-09-04 · `1982952e` · [Loom] deadline probe keys on the `MaxAge` marker + key presence + a conditioned fail, never an empty body; `disarmDeadline` deleted ([design](../../implementation-artifacts/loom-deadline-marker-provenance-design.md))
- 2026-09-03 · `a5f4ef2e` · [Loom/Substrate] `loom-state` removals are TTL'd purges, 61,731 legacy tombstones swept at start, `redrive` over a removed token fixed, gate `checkLoomStateDelete`
- 2026-09-03 · `595ea540` · [Refractor] a `WITH` no longer refuses per-anchor closure: 3 lenses gain the anchor Delete, leaseApplicationsRead licensed, 1 to 22 msg/s ([design](../../implementation-artifacts/with-alias-anchor-closure-design.md))
- 2026-09-03 · `e5aa6ca2` · [Refractor] `edgeInstances` ~15 s/event → 0.24 s live: gate scope, batched reads, pipelined writes, resolve-then-get ([design](../../implementation-artifacts/personal-lens-whole-actor-cost-design.md))
- 2026-09-03 · `c76522e` · [CI] leaseconvergence lens-activation wait root-caused (CoreKVSource's serial MaxPrefetch:1 replay) and fixed, 25s→90s — board row 137(b) resolved, full CI green (run 33816206576)
- 2026-09-03 · `689eb0c0` · [lease-signing] TombstoneSupersededLeaseServiceInstance (ownership-checked, operator-only) + purge of 12,245 superseded background checks on the dev stack, 0 rejected
- 2026-09-03 · `7e2ef6b2` · [lease-signing] a background check stays valid 30 days (not 5 min) and the op-meta targets are labeled — the runaway re-check loop stops; leaseApplicationComplete narrows to 7 labels
- 2026-09-03 · `3017eac3` · [Refractor] a lens Output edit re-activates the lens in place — ownership-tested purge, refusal by construction, scoped health clear; live round trip on cafeStaleTabSettlement
- 2026-09-03 · `cf897d71` · [Refractor] personal-lens derivation licence SHIPPED — edges, cap-read closure, licence + single-instance gate, multi-walk union; edgeCatalog 3/min→25 msg/s
- 2026-09-02 · `d6960bda` · [Refractor/Weaver/packages] expiry is a recorded fact — MarkExpired records the lapse per target, 14 lenses read the marker and no `$now`; 4 cold reviews, 1 BLOCKING + 7 MAJOR closed; per-anchor payoff 📐 filed
- 2026-09-02 · `27250d12` · [CI] main un-reddened — the client-only OptionalReads census pin moved 3→2 after cafe's staff Settle stopped templating its lease
- 2026-09-01 · `8a2cee97` · [Refractor] executor reads a marked hub at the hop's relation, validator re-reads scoped; composed whole reads pin both ways; torn multi-walk footprints rejected — hub drain gone; pair's cost is the `$now` rescan
- 2026-09-01 · `1fca25cf` · [Refractor] pattern-scoped actor walk + Postgres `GetRow` + idle-aware periodic loops + rebuild registration race — 8 stuck personal lenses drain at 10–25 msg/s
- 2026-09-01 · (triage, no code) · [orchestration-base] duplicate-human-task row retired — the prescribed work-scoped dedup is REFUTED (3 shapes, code-cited); cause is an anchor-vs-work granularity mismatch, package fix filed on verticals
- 2026-08-31 · `044ac715` · [Contracts] full-corpus public-posture sweep DONE — all 15 files at promise altitude; 6 factual drifts fixed, ~40 dangling §refs retargeted, rule codified in contracts README
- 2026-08-31 · `571e45e6` · [Contracts] #10 §10.8 {actor} hub token ratified ahead of build — transitional note in text; build fire filed 📋 ready
- 2026-08-31 · `935492df` · [Contracts] #10 §10.8 ratified with posture trims — three-arm param grammar + directOp optionalReads land; gate narration and internal names stay out

One line per shipped item (`date · SHA · [tag] title`). Oldest roll to `archive/` past ~25.


- *(older rolled to [archive/lattice-done.md](archive/lattice-done.md); newest `0da6c431`)*
