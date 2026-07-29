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
| **[Pkgmgr] An op-meta tombstone orphans the open tasks that reference it** | A package upgrade/uninstall that drops an op-meta strands open `forOperation` referents (grant projects null → undispatchable; inbox row loses its op). Designed shape: pkgmgr preflight + author-declared disposition (cancel now, MovedOps/rebind reserved) — no Processor scan. | ★★ | S–M | 🔭 flag-for-Andrew · revive trigger MET: recurrence observed live 2026-07-28, 12 orphaned open tasks · [design](../../implementation-artifacts/opmeta-retirement-open-task-guard-design.md) |
| **[orchestration-base] A closed task's ephemeral grant stays exercisable until expiry** | `capabilityEphemeral`'s three branches filter only `expiresAt > $now` (lenses.go:284-308) and step-3 matches taskKey+opType+target+expiry — status never checked — so CancelTask/CompleteTask do not revoke the grant; a cancelled task's op stays submittable until `expiresAt`. Lens-side `status='open'` filter is the likely shape (myTasks already has it). | ★ | S | 🗄️ shelved (Andrew 2026-07-27: deprioritized; revive: a long-TTL task class or observed misuse) |
| **[Refractor] Two lenses sharing one IntoKey race per column** | `edgeCatalog` and `edgeCatalogRoles` both project `manifest.op.<id>`; last writer wins per re-derivation, so a multi-hat actor's op rows flap between column shapes. Ratified shape eliminates the N-writers cause: multi-walk lenses (pkgmgr `Walks`) + engine UNION (grammar already parses it) + disjoint-key guard with no escape hatch; per-source-merge draft withdrawn at ratification. | ★★ | M–L | 🚧 blocked-on composition fix · [design](../../implementation-artifacts/refractor-shared-keyspace-arbitration-design.md) §12 |
| **[Refractor] A lens spec change re-compiles but never re-projects** | `ClassifyUpdate` returns `MatchChange` ("a full rebuild is required"), but `reload.go` only calls `UseFullEngine` — `Pipeline.Rebuild`'s one caller is the operator control RPC, which pkgmgr never invokes, and a plain nats-kv lens gets no convergence sweep. Rows projected before a lens gained a column keep the old shape until an unrelated CDC event re-derives them. Surfaced by wellness 0.13.0, whose read boundary reads that column (fails closed ⇒ presents as denial). | ★★ | M | 📋 ready |
| **[Refractor] Does a lens evaluation need a point-in-time snapshot?** | A torn cross-key read is a **combination-grant** on the auth plane's three conjunctive shapes. Designed: no snapshots; auth-plane footprint validation (verify → re-execute → requeue). | ★★ | M | 🔭 flag-for-designer · Inc 2 built + reverted (CI stack-gates regression, scope predicate too coarse) · [why](../../implementation-artifacts/refractor-evaluation-consistency-design.md) §10.1 checkpoint |
| **[Refractor] A live claim's own consumer grant never projects into Capability KV** | `ClaimIdentity`'s R2 grant lands in Core KV (verified) but `cap.roles.<target>` never appears — no DLQ, absent past a completed sweep. First-ever-live unconditioned atomic-batch member (`step8_commit.go:191-215`); ground NATS 2.14 atomic-batch semantics before fixing. Repro `make test-claim-ceremony`. | ★★★ | M | 🚧 needs root-cause · [facet-staff-worlds §13.1](../../implementation-artifacts/facet-staff-worlds-design.md) |
| **[Refractor] Actor enumeration never traverses through an actor — `reportsTo`-inherited grants never refresh** | `capabilityEphemeral` embeds a report's tasks in the manager's doc, but the fan-out BFS stops at the first actor (actor_enumerator.go:136-151) — a task/op/target mutation enumerates only the report, so the manager's `cap.ephemeral.*` staleness (incl. over-grants) persists until an unrelated event or the sweep. | ★★ | S–M | 📋 ready · found by the [consistency design](../../implementation-artifacts/refractor-evaluation-consistency-design.md)'s adversarial pass |
| **[Loom] Guardless-step recovery check-before-act probe** | On total `loom-state` loss + a re-triggered `StartLoomPattern`, a fresh instance replays guards from cursor 0 (re-runs an already-applied guarded step). | ★ | S–M | 🗄️ shelved-backup (Andrew: no new engine Core-KV reads) |
| **[Processor] Tombstone-with-document warn→reject flip (Fire 2)** | Fire 1 (emitter sweep + parser warn) shipped `6b68fde4`; flip the warn to a reject once warn sightings are clean (stale stored scripts clear via world recreation). | ★★ | XS | 🚧 seq behind clean warn-window · [design](../../implementation-artifacts/tombstone-body-preservation-design.md) §6 · stale stored scripts now clear via `make reseed-kernel` |
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
> **Build-ready now:** the sensitive-predicate instanceOf-chain gap **shipped** 2026-07-28 (Done
> log); its sibling whole-set-exposure row stays seq-blocked behind read-path auth (D1). The
> next ready security/trust-boundary item is **[appsession] co-hosted-page session fixation**
> (★ S–M, dev/demo only). The **script live-read
> budget** (★★ M) is now **✅ shipped** 2026-07-28 (Done log) — kv.Read/kv.Links share a
> per-execution round-trip ceiling. The **cap-read per-anchor grant keys** fix (★★ L) is also
> **✅ shipped** — Fires 1-3 all landed 2026-07-28 (Done log); the shred-nullify follow-on for
> package-generated producers is its own filed row below. `appsession`'s OIDC design stays
> 🗄️ **shelved** (revive: first real-IdP deployment) — unrelated to the showcase.
> Every `✅ ratified` row is done or driver-blocked; the rest are Whetstone's or parking-lot.
> A stale callout starves the lane — whoever ships next renames this.
>
> 📎 **Refractor is drained.** All seven buildable rows shipped 2026-07-25 against
> [refractor-open-rows-fire-briefs.md](../../implementation-artifacts/refractor-open-rows-fire-briefs.md);
> the cap-read row above (now build-ready) and the HA-NATS-blocked rollup are what remain.

### Security & trust boundary
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Processor] Whole-set `state` exposure remains an existence oracle for sensitive classes** | A guard keyed on consumption still splits on a surplus sensitive declared read when the script takes a whole-set exposure (`items()`/`values()`/rendering `state`) — the flip is correct, so only read-scope validation of the declared set closes it. | ★ | S | 🚧 seq behind read-path auth (D1) · [design §2.2](../../implementation-artifacts/sensitive-read-tracker-consumption-design.md) · no live victim (no package script does it) |
| **[appsession] A co-hosted page can plant a session cookie (fixation)** | Cookies ignore port, so a sibling localhost app's page can `document.cookie` an ABSENT session cookie (HttpOnly blocks overwrite, not create) and the shared dev key makes it verify — the victim browses as an attacker-chosen identity. The origin gate cannot reach it (no request made); `__Host-` or a cookie-bound token closes it. | ★ | S–M | 📋 ready · dev/demo only (shared key) · [kit](../../../docs/components/appsession.md) |
| **[Processor] step6's own instanceOf-chain DDL resolution reads live Core KV with no shared budget** | `connInstanceOfReader.LiveInstanceOfTargets` (step6_resolve_ddl.go) issues its own prefix-list + per-key GETs, up to `maxInstanceOfHops`=4 chain hops per mutation — a separate live-read surface from the kv.Read/kv.Links budget (script-live-read-budget fire), not covered by it. Already soft-bounded by the hop cap + the atomic-batch mutation ceiling, not unbounded, but worth its own accounting pass. | ★ | S | 📋 ready · found reviewing the script-live-read-budget fix |
| **[packages] ~20 read-posture comments assert hydration-time fatality** | `packages/*` DDL comments + two READMEs still say a declared-but-absent read faults "before the script runs" (identity-domain, service-domain, privacy-base, objects-base, orchestration-base, clinic/loftspace READMEs), as does `docs/contracts/10-orchestration-substrate.md:238`. Doc-only sweep. | ★ | S | 📋 ready |
| **Starlark 250ms wall budget fails installs under parallel test load** | `go test ./...` at default `-p` reds a different package-install test each run with `ScriptTimeout: script exceeded wall budget 250ms` — reproduced on unmodified `main`, so it predates any one fire. Costs every fire an investigation to rule out its own change. | ★★ | S–M | 📋 ready |
| **[Refractor] A package-generated `cap-read.*` producer's grants are not shred-nullified** | Only the base lens is wired into `keyshredded`'s `NullifyTarget` list — a shredded identity's package-domain grants (e.g. edge-manifest's) survive. Needs per-producer `NullifyTarget` wiring (lens IDs are install-time NanoIDs, not a static list). | ★★ | S–M | 📋 ready · [design](../../implementation-artifacts/cap-read-per-anchor-grant-keys-design.md) |
| **[appsession] The production IdP posture cannot open a session** | `setCookie` runs only under a non-nil `Signer`, so with `_JWT_PUBLIC_KEY`/`_ISSUER` set nothing can issue the cookie — the verify-only posture is unreachable (401 everywhere), and `/api/session/refresh` 404s so every FE write path dies with it. Design: the kit becomes the OIDC code-flow RP. | ★★ | L | 🗄️ shelved (revive: first real-IdP deployment) · ✅ design Andrew-ratified 2026-07-25 · [design](../../implementation-artifacts/appsession-oidc-production-signin-design.md) |
| **Multi-hat `scope=any`+`scope=self` first-match over-confines** | `matchPlatformPermission` returns on the first operationType match regardless of scope, and `capabilityRoles` collects roles unordered — so a consumer+staff identity (e.g. seed-showcase `seedSamMultiHat`) can authorize their OWN cafe tab as scope=any, losing the self exemption. Fail-closed; bites a multi-hat who works and lives in different buildings. | ★ | S–M | 📋 ready · no live victim (showcase multi-hat has no leaseapp) |
| **NATS write restriction — Fire 4 (production mTLS)** | Fires 1–3 closed the fabricated-KV-write surface at the account level; the remaining fire binds subject permissions to client certificates instead of NKeys, which only matters off the dev stack. | ★ now / ★★ prod | M | 🗄️ shelved (revive: production deployment) · [design](../../implementation-artifacts/nats-account-write-restriction-design.md) §Fire-3-status |
| **Keyed identity-index hashes (HMAC)** | Unkeyed `sha256NanoID` contact hashes are dictionary-testable with substrate access and persist in JetStream history post-shred; a Vault-keyed HMAC bounds it but needs a MAC primitive + key custody at every hash computer, and must migrate ALL index consumers (identityindex, provision probe, dedup) in one stroke. | ★ now / ★★ prod | M | 🗄️ shelved (revive: production threat model) · [analysis](../../implementation-artifacts/dedup-over-encrypted-pii-design.md) §9.1/§10-C |

### Orchestration & edge — Loupe-routed (2026-07-25 PO pass)
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
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
| **[Edge] The per-actor SYNC subject retains delta history unboundedly** | The cold-reconnect boot-gate race was a *symptom* of unbounded per-actor subject growth (now fixed, Done log) — every delta + every prior hydrate burst a device ever received stays replayable, and the growth itself is still unbounded; a retention/compaction posture on the subject is the underlying fix. | ★★ | S–M | 📋 ready |
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

- 2026-07-28 · `533a0b71` · [Edge] hydrationComplete boot-gate now matches the hydrate RPC's own target revision, not the first (possibly stale-replayed) marker seen
- 2026-07-28 · `6c720482` · [chronicler,orchestration-base] eventStream ColumnMapping gains ClearOn — a Loom re-dispatch's patternStarted no longer carries the prior run's ended_at/failure_reason onto the new running row
- 2026-07-28 · `c08c28be` · [Processor] sensitive predicate now covers instanceOf-chained classes; pkgmgr rejects Sensitive on a non-aspectType DDL, closing the link/event gap by construction
- 2026-07-28 · `ea3f3852` · [Refractor] evaluation-consistency Fire 1 Inc 1 — edge memo + node/edge revisions, the footprint-validation primitives; item continues 🏗️
- 2026-07-28 · `e8fee3b0` · [Processor] script live-read budget — kv.Read/kv.Links share a per-execution round-trip ceiling (charged at the clamped page limit, race-safe), sized + pinned against MergeIdentity's own worst case
- 2026-07-28 · `76c9629e` · [cap-read] Fire 3 legacy-shape purge — one-shot tool tombstones any surviving legacy doc, IsReadable drops the dual-read union; item closes (Fires 1-3 all shipped)
- 2026-07-28 · `3d950442` · [weaver,loftspace-ledger] DebitAccount derives amountCents from the clause's own .terms — never a Weaver-copied row value — closing the census row 5 money-provenance gap
- 2026-07-28 · `101b01fd` · [cap-read] Fire 2 producer flips — every generated cap-read producer (edge-manifest's three) now emits per-anchor grant keys; validateGrantDomainName hardened
- 2026-07-27 · `d8bdf7fe` · [identity-hygiene] MergeIdentity's dead link-collision check now fires — rewritten-key optionalReads declared, so a real collision migrates as a duplicate instead of rejecting the whole merge
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
- *(older entries rolled to [archive/lattice-done.md](archive/lattice-done.md); includes `94c8224` hello-lattice NFR-P3 flake fix)*
