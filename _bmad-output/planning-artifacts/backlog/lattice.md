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
| **[Health-KV] Orphaned dead-instance heartbeat keys never expire** | Each `health.<component>.<instanceID>` is written with no TTL, so a dead instance's key persists forever → permanent stale entries the Lamplighter must distinguish from live. | ★★ | S–M | ✅ ratified (2026-07-02, Fire-3 re-key) · [design](../../implementation-artifacts/health-kv-ttl-orphan-expiry-design.md) · 2 fires (A+B merged; re-key after sink consolidation) |
| **[Core] Processor per-lane consumers (ConsumerSupervisor adoption)** | Replace the single `processor-main` durable over all `ops.*` lanes (Phase-1 simplification) with per-lane consumers, per the architecture's design-of-record. | ★★ | M | 🏗️ building (per-lane fires shipped; see git) |
| **[Weaver] HealthSink pause-restore round-trip uncovered** | `consumerHealthSink.Load` paused-restore branch + `pauseReasonFromString` sit at 0% coverage. | ★★ | XS–S | ✅ ratified (2026-07-02, Bridge included) · [design](../../implementation-artifacts/health-sink-consolidation-design.md) · one fire (consolidate + rewire all three) |
| **[Weaver] Registry cleanup edge branches uncovered** | `targetSource.removeOwnedTargetLocked` (targetId-rename removal, 33%), `removePatternLocked` + `removeOpMetaLocked` (pattern/op-meta vertex deletion index cleanup, 50%) — untested paths that keep the in-memory dispatch-resolution indices (`patternMeta`, `opMetaByType`) from leaking stale entries when a referenced `meta.loomPattern`/op meta-vertex is deleted or a target's `targetId` is renamed. | ★ | XS–S | 📋 · `internal/weaver/registry.go:372,586,640` |
| **[Loom] HealthSink pause-restore round-trip uncovered** | `consumerHealthSink.Load` paused branch (`internal/loom/health_sink.go:75-81`) + `pauseReasonFromString` switch arms partly uncovered (pkg 81.5%); restart-pause-restore unexercised end-to-end. Mirror of the Weaver gap above. | ★★ | XS–S | ✅ ratified (2026-07-02) · [design](../../implementation-artifacts/health-sink-consolidation-design.md) · same consolidation as the Weaver row |
| **[Loom] Guardless-step recovery check-before-act probe** | On total `loom-state` loss + a re-triggered `StartLoomPattern`, a fresh instance replays guards from cursor 0 (re-runs an already-applied guarded step). | ★ | S–M | 🗄️ shelved-backup (Andrew: no new engine Core-KV reads) |
| **[Refractor] Retire the legacy `simple` engine (full-engine is universal)** | All 20 lenses are `engine:"full"`; the ~2.8k-LOC `simple` parser + its registry fallback are dead in prod but own the shared `EvalResult`/`QueryPlan` types → decouple-then-delete. | ★★ | M–L | 🏗️ building · [design](../../implementation-artifacts/retire-simple-engine-design.md) · Fire 1+2 shipped (carrier move, dead invalidation-forest deleted); next: Fire 3 delete the simple engine |
| **[Refractor/pipeline] Fan-out eval-error disposition + adjacency-watch edge branches uncovered** | `dispositionEvalErr` (0% — fan-out eval-error → terminal-DLQ/infra-pause/transient-nak) + `handleAdjUpdate` (13.5% — the not-found/tombstone/bad-key/unmarshal/guarded/write arms). Happy-path fan-out is e2e-covered; the error/edge arms are not. | ★★ | XS–S | ✅ ratified (2026-07-02, eval-transient asymmetry pinned as intended) · [design](../../implementation-artifacts/refractor-pipeline-failure-disposition-coverage-design.md) · 1 fire |
| **[Core] Atomic-batch size ceiling undocumented + unenforced** | A Starlark script's mutation set has no documented/enforced max size; a legitimate op that exceeds NATS's per-batch byte limit surfaces as a raw substrate/NATS error at step 8, not a typed Processor rejection — no bound, no clean failure mode. | ★ | S | ✅ ratified (low-priority maintenance) · [design](../../implementation-artifacts/atomic-batch-size-ceiling-design.md) · contracts committed; 1 fire |
| **[Core] UninstallPackage tombstones unconditionally (F-011 per-key OCC follow-up)** | `Installer.Uninstall`/`Upgrade` submit without per-key `expectedRevision` — a concurrent write to a declared key is silently overwritten. Fix: condition on the read-time `KVGet` revision (already read). | ★ | S–M | ✅ ratified · [design](../../implementation-artifacts/package-install-per-key-occ-design.md) · read-time revision (not install-time); §8.3/§8.6/§8.7 committed; 2 fires (uninstall, upgrade) |
| **[Loom] Redelivery/deadline-recovery edge branches uncovered** | `engine.go:resumeStepZero` (41.7% — redelivered trigger whose `createInstance` batch committed but step 0 never submitted, incl. the pattern-pin-missing→fail branch) + `state.go:disarmDeadline` (33.3% — KVGet/KVDelete error arms + the already-disarmed no-op that breaks the deadline-watcher re-entry loop) sit untested by any direct unit test. | ★ | XS–S | 📋 · `internal/loom/engine.go:460`, `internal/loom/state.go:451` |
| **[Refractor] Capability-pipeline link/aspect fan-out dispatch untested** | `evalLinkFanOut`/`evalAspectFanOut` (0%) — the actor-aware pipeline's CDC dispatch for `holdsRole`/`grantedBy` link + aspect events that recompute authz on role grant/revoke — has no test at any level; no test references `evaluateLinkFanOut`/`evaluateAspectFanOut` either. | ★★ | S–M | 📋 · `internal/refractor/pipeline/pipeline.go:577,609`, `evaluate.go:319,348,411` |
| **[Refractor] NatsKVAdapter guarded-write CAS-contention + malformed-watermark edge branches uncovered** | `guardedWrite`'s revision-conflict retry loop + CAS-exhaustion path (53.8%) and `storedProjectionSeq`'s `json.Number`/malformed-doc branches (46.7%) — the H4 no-resurrect guard's contention/legacy-doc handling — untested. | ★ | XS–S | 📋 · `internal/refractor/adapter/natskv.go:190,250` |

### Survey log (round-robin rotation)

Rotation memory only — findings are the filed rows; fire narratives live in commits, never here.
Components: Core · Weaver · Loom · Refractor (+ the cross-cutting feature backlog; Loupe moved to its own
lane, [loupe.md](loupe.md)). Survey the stalest (`git log -1 --format=%ct -- <path>`), note ONE dated line, rotate.

- 2026-07-01 Core (healthy; filed atomic-batch-size-ceiling + uninstall-per-key-OCC).
- 2026-07-01 Weaver (healthy, 83%/77% cov, no TODOs; filed registry-cleanup-edge-branches-uncovered).
- 2026-07-01 Designer — Refractor pipeline fan-out eval-error disposition + adj-watch edge arms (→ 📐).
- 2026-07-01 Loom (healthy, 81%/77% cov, clean lint, no TODOs; filed redelivery/deadline-recovery-edge-branches-uncovered).
- 2026-07-01 Designer — search/ES target adapter (3rd Refractor adapter; OpenSearch rec., FTS interim) (→ 📐).
- 2026-07-01 Designer — feature queue designed-out (all ~30 rows carry a design); resolved stale L309 (link-tombstone subsumed by link-aspect design, latency-rollup seq behind HA). Remaining 📋 = owner test-coverage.
- 2026-07-02 Refractor (healthy, clean lint; retraction/rollup already tracked; filed capability-pipeline-link-aspect-fanout-untested + natskv-guard-edge-branches).
- 2026-07-02 Arch-review, all components — filed the intake section below; Refractor findings held for the post-update re-review; root-identity designation → Designer.
- 2026-07-02 Designer — object-plane-nats-permissions (★★★ arch #2; `$O.core-objects.>` grant fix + first natsperm object vectors; no contract change) (→ 📐).
- **Next:** Core.

## Arch-review intake — platform hardening & doc/contract truth

Open corrections from the [2026-07-02 full-platform review](../../../docs/reviews/arch-review-2026-07-02.md)
— per-finding `file:line` evidence and per-component verdicts live there; the What-cells here are abridged.
**Refractor findings are deliberately absent**: that component is mid-update and Andrew re-reviews it after.
Severity-ordered; same row discipline as component maintenance (shipped rows collapse to the Done log).

| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **root-designation-topology-reconverge** | Root is designated two contradictory ways — Contract #7 §7.7 says `holdsRole→operator` topology (self-protecting), but shipped core + Contract #6 §6.1 drifted to the forgeable `data.protected` bit (Epic-12; `*` read wildcard forgeable). Re-converge core on the topology; retire the bit as designator (anti-brick kept). | ★★★ | S–M | ✅ ratified (2026-07-02, Fork A) · [design](../../implementation-artifacts/root-identity-designation-design.md) · one fire; §6.1 committed; create-guard deferred |
| **object-plane-nats-permissions** | Object-plane NKey grants mismatch the pinned nats.go: writes publish on `$O.core-objects.C/M.>` but objmgr is granted `$OBJ.objects-base.>` (wrong prefix + bucket) and Loupe/loftspace-app have no `$O.` grant at all — blob upload + GC delete should be transport-denied on the live stack. Fix the grants; add natsperm object vectors (zero today); verify a live upload. | ★★★ | S | 📐 awaiting-Andrew · [design](../../implementation-artifacts/object-plane-nats-permissions-design.md) · one fire; no contract change |
| **gateway-revocation-kill-switch-activation** | Kill-switch dormant — nothing populates a revocation set, no admin surface revokes, a failed bucket-open silently disables checking. **Mechanism (Andrew, 2026-07-02):** `RevokeActor` op outboxes a revocation event the Gateway consumes into its own internal-state KV (not a Loupe-written bucket; lands in the Chronicler ledger). Fail-closed per request; add the Loupe revoke UI. | ★★ | M | 📋 needs-design (Designer) · blocks loupe F11 |
| **[Gateway] up-full deploy + JWKS heartbeat block (Loupe enablers)** | Gateway isn't started by `make up-full`'s `orchestration` target (`run-gateway` exists) → its map node is a ghost; and its heartbeat carries no trusted-key state. Start it in up-full (dev-mode + dev key); add a `jwks` block `{keys:[{kid,source,alg,addedAt}],lastPoll,swaps}` to `health.gateway.<instance>`. | ★★ | S | 📋 · blocks loupe F10 (truthful node) / F11 (JWKS panel) |
| **heartbeat-false-green-aggregation** | Bridge, Gateway, and object-store-manager emit status "healthy" unconditionally while carrying (or ignoring) issues — an outage rides a green heartbeat; Loom/Weaver already aggregate issue severity. Port the aggregateStatus rule into all three heartbeaters. | ★★ | S | 📋 |
| **contract-10-weaver-text-reconciliation** | Contract #10's Weaver text drifted from as-built in five spots — worst: the §10.8 augur block says `pattern`+triggerLoom while the engine takes op/adapter/replyOp + a directOp, so a package author's field is silently dropped; also the anti-storm cross-ref, two reserved weaver-state key shapes, the §10.2 weaver-targets read-path, revision history. Stage one uncommitted edit for Andrew. | ★★ | S | 📋 |
| **contract-wire-error-code-reconciliation** | Contract #2 §2.6's error-code table diverged from the wire both ways (7 listed codes never emitted; 6 emitted codes unlisted), plus the §4.1 tracker class and §2.9's unknown-field claim. Reconcile the frozen text to the real closed enum (uncommitted edit for Andrew); pin it with a conformance test that reads the contract's table. | ★★ | S | 📋 |
| **step6-batch-internal-consistency-decision** | Contract #3 §3.5 + spine steps 6–7 assert validations the Processor doesn't perform (link-endpoint/aspect-host dangling-reference resolution; §3.4/§3.8 event-type DDL check) — unbuilt and untracked. Decide build-vs-amend per layer (both checks are cheap and fail-closed-aligned); build the chosen ones or stage a narrowing amendment. | ★★ | M | 📋 |
| **chronicler-prebuild-regrounding** | Pre-F1/F2 corrections to the ratified Chronicler design: F2 consumes `events.weaver.>` but Weaver emits no events — fold the lifecycle-event producer into F2; archive segments carry no object vertices, so objmgr's sweep would GC them — needs a GC-fenced bucket; F1's projection example maps data/committedAt vs the published Event doc's payload/timestamp; loom-state terminal cursors persist — re-ground the deletion premise. | ★★ | M | ✅ ratified · [design](../../implementation-artifacts/orchestration-history-read-model-design.md) |
| **loom-pattern-source-cold-registry** | loom.md advises a stable `Instance`, but the pattern-source durable derives from it and resumes from its ack floor — a crashed stable-Instance Loom reattaches empty, boots with no pattern registry, and new triggers Nak-loop until a meta vertex is rewritten. Un-armed only while nothing sets LOOM_INSTANCE. Per-boot durable nonce; fix source.go's contradictory comments. | ★★ | S | 📋 |
| **natsperm-matrix-hygiene** | Stale/over-broad transport grants: bridge is allowed `$KV.bridge-external.>`/`$KV.bridge-schedule.>` (consumer names, not buckets — bridge writes only health-kv), and Refractor's `$KV.>` write is broader than its lens-target set. Prune the stale grants, narrow or explicitly-deny, extend the natsperm vectors to pin the tightened matrix. | ★★ | S | 📋 |
| **objmgr-and-bootstrap-component-pages** | object-store-manager + bootstrap are always-on platform binaries with no docs/components page, no README row, no architecture-overview mention, and no Surveyor-rotation slot; vault + privacyworker are built but page-less too. Write the four pages; add the index/README/overview rows; put objmgr + bootstrap in the survey rotation. | ★★ | M | 🏗️ building · objmgr page shipped `2430489` · next: bootstrap/vault/privacyworker pages + overview + survey rotation |
| **contract7-and-processor-mandate-refresh** | Contract #7 §7.2/§7.7 describe a superseded kernel (5 meta-meta DDLs, processor identity, topology-walk cypher) — stage the alignment edit for Andrew. processor.md/doc.go omit step-6.5 encryption, the §3.2 OCC retry, task auto-completion, and kv.Links; commit_path.go keeps "stubbed 4-10"/"auth (stub)" comments; a bootstrap comment asserts a capability graph-walk that isn't. | ★ | S | 📋 |
| **contract6-serviceclass-and-example-drift** | (residual flag from docs-truth-sweep, doc fixes shipped 3ef4830) Frozen Contract #6 still drifts: §6.5 carries `serviceClass` in the `serviceAccess` shape, but the shipped residence lens dropped it (`lenses.go` + `package_test.go` guard); §6.12's worked example promises service-denial fields the denial builder never emits. Andrew: reconcile or amend. | ★ | S | 🔭 flag-for-Andrew |
| **weaver-exhausted-escalation-and-model** | The ratified augur block accepts `exhausted` as an escalation trigger and parses `augur.model`, but no engine path fires either — a budget-exhausted gap is silently skipped (no escalation, no Health issue) and model is consumed by nothing. Wire the trigger through augurEscalation (threading model), or strike both from the block. | ★ | S | 📋 |
| **loom-dispatch-authcontext-target** | Loom sets each step op's `authContext.target` to "vtx.meta."+PatternID — a human-readable name, while the real vertex is `vtx.meta.<NanoID>` — so live externalTask ops carry a dangling target in the forbidden canonicalName shape. Inert under scope-any; breaks when scope-specific auth lands. Carry the real meta key through source+pin; fix pattern.go's false comment. | ★ | S | 📋 |
| **gate3-vector14-in-gate** | Gate 3's gateway-impersonation vector #14 is backed by a test in internal/gateway that the gate target never runs (it only runs internal/bypass) — the gate can report DEFENDED while that test fails. Add the package to the gate's scope or add an in-package bypass test; refresh the stale vector-count comments. | ★ | S | 📋 |
| **repo-debris-and-stale-narration** | Remove the five resolved CONTRACT-AMENDMENT-REQUEST.md journals (cmd/{loom,processor,refractor,weaver}, internal/substrate — git is the record) and the pre-cascade comment clusters (objmgr package doc; objects-base OpMetas naming a nonexistent reclaim pattern; loom doc.go); decide internal/spike disposition; fix the objmgr Makefile launch missing BOOTSTRAP_JSON_PATH. | ★ | S | 📋 |
| **contract10-async-deadline-reconcile** | Contract #10's async paragraph says the Loom step deadline is per-adapter-sized and backstops a dead bridge, but its own §10.6 + the code disarm that deadline at instanceOp commit (FailPattern is the out-of-band close; the bridge waits unbounded). Stage a reconciling edit; note the single global CallDeadline as deferred-with-real-adapters. | ★ | XS | 📋 |

## Lattice feature backlog — the Phase-3 build queue

The AI-driven flywheel draws from this list (Surveyor files → Designer designs → Steward builds the
ratified). Everything here needs design and is fair game **except** 🚧 Andrew-gated rows. Architectural
**forks** (Gateway, read-path auth, Vault, multi-cell, HA-NATS) and **frozen-contract** changes are
designed-through, but the *fork decision* + the *contract commit* are Andrew's.

> 🎯 **Build-ready now**: **Vault Fire 5b** (★★★ — readiness clone shipped `13ffb75`; next 5b-ii-c
> FE wiring + console retirement, then 5b-iii clinic contact + FE tails; unblocks 3 Verticals
> rows). *Dependency-sequenced ratified items*: **Personal Lens** (buildable,
> deprioritized behind Vault) · **Object crypto-shred** (behind Vault). Current fire/park state for
> Gateway · FR28 · Augur · Control-plane-authz · `kv.Links` lives on their rows below.

### Security & trust boundary
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| Gateway | Edge trust boundary: JWT auth, `Lattice-Actor` stamping, read-path enforcement. Gates external actors + the real Edge node. | ★★★ | L | 🏗️ building · [design](../../implementation-artifacts/gateway-external-trust-boundary-design.md) · Fire 1+2 (JWKS live poll/rotation) shipped; Fire 4 (claim-front) needs re-grounding — see [doc](../../../docs/components/gateway.md); next: read-front (behind D1.3) |
| NATS account-level write restriction | Close the fabricated-KV-write surface at the substrate (account-level); today defended only by overwrite-by-reprojection. | ★★ | M | 🏗️ building · [design](../../implementation-artifacts/nats-account-write-restriction-design.md) · F1+F2 shipped (live enforcement ON, `1f2f999`+`083b0ad`); next: optional Fire 3 (flip Gate 2/3 bypass tests hard + verify-nats-permissions CI job) |
| Control-plane Capability authorization (FR30) | Both control planes (Weaver/Refractor `…/control`) should be capability-gated, not open responders. | ★★ | M | ✅ ratified · [design](../../implementation-artifacts/control-plane-capability-authz-design.md) · rides D1.2 (shipped) → buildable; deprioritized behind D1 rollout |
| **System-actor package-op grants absent under capability auth** | A kernel system actor's platform read is the fixed 6-op `cap.<actor>` anchor, so every engine-submitted package op (MarkExpired, CreateTask, DetachObject, RecordShredFinalization) authorizes only under the dev stub (`make up` runs `LATTICE_AUTH_MODE=stub`). | ★★ | M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/system-actor-package-op-grants-design.md) · Fork: union read (anchor ∪ cap.roles); §6.1/§2.8 edits staged |

### Privacy / Vault
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| Vault + crypto-shredding | Per-identity keys for sensitive aspects (SSN/DOB); right-to-be-forgotten = destroy the key; transient-session-key decrypt. | ★★★ | L | 🏗️ building · [design](../../implementation-artifacts/vault-crypto-shredding-design.md) · 5b-ii-d shipped (`04bcbf0`, sev-1 ssn-presence-vs-ciphertext fix); next: ShredIdentityKey proof on this lens, then 5b-iii clinic contact |
| **[identity-hygiene] Dedup over encrypted PII (duplicateCandidates)** | Post-Vault, the lens's WHERE matching (email/phone equality, name Levenshtein) runs on per-identity-DEK ciphertext → functionally inert; a secure lens can't fix in-engine matching. Needs a design: blind-index/HMAC companion aspect vs sanctioned engine mechanism. | ★★ | M | 📋 needs-design (Designer) · context in the [vault design](../../implementation-artifacts/vault-crypto-shredding-design.md) Fire 5b-i checkpoint |
| **[Object Store] Crypto-shred for object-store blobs** | Vault covers sensitive **aspects** (Core KV) but not PII-bearing **blobs** (lease PDFs, ID scans, signatures) — extend crypto-shred to the Object Store. | ★★ | M | ✅ ratified · [design](../../implementation-artifacts/object-store-crypto-shred-design.md) · 🚧 behind Vault |
| **[Vault→Loupe] surface enablers** | For loupe F12 (Vault map node/page + Reveal + crypto-shred proof): a dedicated `health.vault.<instance>` heartbeat group (metrics ride privacy-worker today); `lattice.vault.decrypt` reachable from Loupe's actor; **grant Loupe's operator actor `ShredIdentityKey`** (op shipped `604342b`; Andrew-approved 2026-07-02, scoped). | ★★ | S | 📋 · blocks loupe F12 · [UX §3](../../implementation-artifacts/loupe-platform-edges-ux.md) |

### External-I/O maturity (bridge follow-ons)
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| Real adapters + async result-return | Replace the `Fake*` adapters with real vendors + design the async result path. | ★★ | M–L | ✅ async result-return done · real adapters deferred (prod) |
| Adapter read-seam / richer params | Adapters can only use what the target-lens row projects; add a subject-templated fetch seam for extra fields (SSN/DOB). | ★★ | S–M | 🏗️ building · [design](../../implementation-artifacts/adapter-read-seam-subject-templated-params-design.md) · F1 (sub-templated params) shipped |

### Scale-out
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| Multi-cell / sharding | Graph scales by **cells** (root + subgraph co-located for atomic writes); global adjacency index + bridge links for cross-cell. | ★ now / ★★★ at scale | XL | ✅ ratified · [design](../../implementation-artifacts/multi-cell-sharding-design.md) · 🚧 seq (prod-scale driver) |
| **Global identity for a hyperscale tenant** | A hyperscale tenant (WeWork) spans cells/regions — cross-cell shadows + cross-region residency on top of multi-cell. | ★ now / ★★★ at hyperscale | L–XL | 📐 awaiting-Andrew · [design](../../implementation-artifacts/global-identity-hyperscale-tenant-design.md) · 🚧 build behind multi-cell Fire 2 + a real hyperscale driver; NO contract change (one scoped multi-homed-`identity` exception flagged); 5 fires |
| **HA NATS clustering** | Single-server today; clustering + multi-instance engine fan-out. | ★ now / ★★ prod | M–L | ✅ ratified · [design](../../implementation-artifacts/ha-nats-clustering-design.md) · 🚧 shelved (prod-HA driver) |

### Edge & personal lenses (the path Loupe grows into)
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| Personal / Secure Lens | Refractor projects a per-identity security-filtered subgraph stream; the Interest-Set watchlist; RLS-style link filtering. | ★★ | L | ✅ ratified (design) · [design](../../implementation-artifacts/personal-secure-lens-design.md) · D1 gate cleared — buildable, deprioritized behind Vault |
| NATS-subject publish-events adapter | A Refractor target adapter publishing projection deltas to `lattice.sync.user.<id>` — required for Personal Lens. | ★★ | S–M | 📐 subsumed → Personal Lens Fire 1 |
| Edge Lattice (full) | The sovereign per-user node: local VAL (SQLite/IndexedDB), local Starlark, offline-first, reconcile-by-revision. | ★★ | XL | ✅ ratified · [design](../../implementation-artifacts/edge-lattice-full-design.md) · 🚧 seq (far) |

### AI-native
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| AI-authored capabilities | A Lattice-aware agent proposes DDL/Starlark/lenses/workflows through human review + deterministic validation + rollback. | ★★–★★★ | L | ✅ ratified · [design](../../implementation-artifacts/ai-authored-capabilities-design.md) |
| **The Augur** (AI reasoning tier — L3 evaluator) | Weaver's AI-assisted reasoning tier for ambiguous/novel convergence gaps. The marquee AI-native feature. | ★★ | M–L | ✅ Fires 1+2a+2b shipped (loop closes: escalate→review→dispatch) · [design](../../implementation-artifacts/augur-design.md) + [dispatch design](../../implementation-artifacts/augur-dispatch-pickup-design.md) · 🚧 Fire 3 autoApply Andrew-gated; follow-up: mid-flight-kill + drift-invalid e2e (§6 residual) |
| Starlark guards (Loom) | The reserved `{reads, starlark}` guard escape hatch needs a verified-pure sandbox. | ★ | M | ✅ ratified (split) · [design](../../implementation-artifacts/loom-starlark-guards-design.md) · 🚧 Loom-side held (ships with first consumer) |
| **Bespoke contracts / "Executable Paper" — Starlark-backed semantic clauses** | `vtx.clause` vertices (prose + Starlark predicate + formula) linked to the state they govern; Weaver audits satisfaction against a resident/patient ledger, auto-debiting computational clauses + opening a Task for judgment ones. Vault: `Contract as Executable paper/*`. | ★★★ | XL | ✅ ratified (2026-07-02, re-scoped: pattern+package) · [design](../../implementation-artifacts/bespoke-contracts-executable-paper-design.md) · residue: weaver.md note + UDF on demand |

### Read-model / projection maturity
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Refractor] Convergence-lens filtering-WHERE activation guard** | Filter-retraction relies on convergence (`violating`) lenses never carrying a filtering WHERE (a retracted row reads to Weaver as entity deletion) — true for every live lens but unenforced at activation. | ★ | XS–S | 📋 review carry-out · [design](../../implementation-artifacts/negative-filter-retraction-projection-design.md) §Fires-1+2-checkpoint |
| **[Refractor] Protected/plain Postgres adapter is unguarded last-writer-wins** | The plain/protected `PostgresAdapter` ignores `projectionSeq` (unconditional LWW) — a stale replay can transiently reorder a security-relevant row. Posture accepted 2026-07-02 (the D1 M3 CDC-lag analog); this row is the follow-up hardening: extend the seq-guard to protected targets. | ★ | S–M | 📋 |
| Elasticsearch target adapter | A third lens target adapter (only NATS-KV + Postgres ship; no consumer yet). | ★ | M | ✅ ratified (2026-07-02, OpenSearch pin + FTS-first interim) · [design](../../implementation-artifacts/search-target-adapter-design.md) · shelf — first consumer (LoftSpace FTS unified search) filed on verticals; the OpenSearch adapter builds only on search-engine-scale demand |
| **[Refractor] Cross-instance projection-latency rollup** | Aggregate per-lens projection latency across Refractor instances into one per-component view (single-instance today, so per-instance == per-component). Link-tombstone re-projection half **subsumed** by the link-aspect reprojection design. | ★ | S | 🚧 seq behind HA-NATS multi-instance · [link-aspect design](../../implementation-artifacts/link-aspect-triggered-reprojection-plain-lenses-design.md) subsumes the tombstone half; no multi-instance consumer yet |
| **[Refractor/Loupe] Silent lens-projection stall is undetectable** | A stalled projection is invisible: Clinic-PO saw committed ops stop reaching every clinic read model while Refractor self-reported `green`/`active`. Emit per-lens projection lag → Health KV; populate Loupe's `freshness` column (today always `-`). | ★★ | M | ✅ ratified (2026-07-02, StallDetect off) · [design](../../implementation-artifacts/lens-projection-liveness-design.md) · one fire (emit+backstop); freshness UI rides Loupe F5 |

### Refinements & ops
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **CI pipeline speed (continuous)** | Make CI faster without weakening any gate — owned continuously by the **Whetstone**. Matrix split done (serial → 4 parallel jobs); convergence + unit parallelized. | ★★ | M (ongoing) | 🏗️ continuous (Whetstone) · `internal/bridge` require.Never windows trimmed to actual margin (f8e017d, 44.5s→27.6s); next: `internal/loom` (41.6s) now the `unit` job's long pole |
| **[CI/Refractor] Hello-Lattice NFR-P3 latency flake** | The capability-projection probe fails-then-passes on the shared CI runner — re-scoped to a 1000ms regression guard (Andrew-ratified; reported SLA unchanged), but the runner floor has drifted to ~1.1s. | ★★ | M | ⚠️ recurred 2026-07-02 (probes 1.07–1.27s vs 1000ms; 2 reruns to green) · Whetstone re-examine |
| **Op-time bounded reverse-link / adjacency read (`kv.Links`)** | One sanctioned, bounded, fail-closed, paged op-time link-enumeration builtin (`kv.Links(hub, relation, direction, cursor, limit)`) — retires the key-list-in-aspect guard indexes. Relaxes the write-path no-scans invariant by exactly one primitive. | ★★★ | M–L | 🏗️ building · [design](../../implementation-artifacts/op-time-bounded-link-enumeration-design.md) · ⚠️ build diverged from the ratification banner (inverted `hasBooking`, §1.1) — fix rides the verticals slot-claims redesign · Fire 3 parked |
| **Hard-delete mutation verb (true link/aspect keyspace reclaim)** | Mutation vocab is create/update/tombstone (soft PUTs); a tombstoned key persists + is still enumerated by `kv.Links`. A 4th `delete` verb (NATS `DEL`) lets dead links leave the keyspace, bounding `kv.Links` LIST cost. | ★ | M | 🗄️ shelved (Andrew 2026-07-02) · [design + hold banner](../../implementation-artifacts/hard-delete-mutation-verb-design.md) · demand dissolved by clinic write-path slot claims; §3 edits reverted; revive only on a real reclaim driver |
| **Script-read posture — declared+hydrated vs live `kv.get`/`kv.Links`** | Declared+hydrated reads as the write-path norm: `optionalReads` folds read-before-create in; `kv.Links` declared-as-metadata (Edge-gate + best-effort lint, not hydrated); guards become a generic Processor-side operation feature (supersedes Loom's engine read). | ★★ | L | ✅ ratified · [design](../../implementation-artifacts/script-read-posture-design.md) · Fires 1–2 shippable (Contract #2 committed); guard (Fire 3) build + contracts deferred |
| **FR28 — role-queue + fallback** (+ FR29 unrouted surfacing) | A `queuedFor.role` link + `ClaimTask` op + `CreateTask` routing (named → role-queue → loud `RoutingFailed`); grant/inbox fan out to role-holders; an empty queue is surfaced post-hoc by a new `unroutedTasks` Weaver target. | ★ | M | 🏗️ building · [design](../../implementation-artifacts/fr28-role-queue-fallback-design.md) (`9495081`,`12fc79b`) · next: Fire 3 unrouted surfacing |
| **Package version upgrade / DDL hot-reload (F-004)** | In-place re-install over an existing version + DDL-migration semantics (install/uninstall existed; upgrade did not). Diff-and-apply (create/update/tombstone) in one atomic Processor batch; version-independent entity keys. | ★★ | M | ✅ effectively done · [design](../../implementation-artifacts/package-version-upgrade-design.md) · Fires 1a–3 shipped; only an optional Fire-2 live e2e remains (§8.1 + §8.6 committed) |
| **[Verticals] loftspace-app / clinic-app have no Health-KV self-report** | Neither app writes health status at all — an admin-actor load failure (hit live 2026-07-01: on-disk `lattice.bootstrap.json` `version:"13"` vs `checkVersion`'s required `"14"`, committed `40f4d25`) or a NATS outage is invisible to Loupe; only surfaces when a user's `/api/op` write 400s. | ★★ | S | ✅ ratified (2026-07-02, TTL on) · [design](../../implementation-artifacts/vertical-app-health-self-report-design.md) · one fire (+opt objmgr tail) |
| Loom / Weaver control-API surfacing | Operator pause/resume + a durable `loom.*` read model beyond what the Loupe blocker covers. | ★ | M | ✅ ratified (2026-07-02, Fork C: the Chronicler — new event-ledger materializer component) · [design](../../implementation-artifacts/orchestration-history-read-model-design.md) · fires (chronicler-prebuild-regrounding first): component+loom history → weaver history → core-ops archive; display = Loupe **F13** (the Time Machine) |

### Parking lot — very low priority (far, far back)

Real but low-value; do **not** spend design or build effort here unless Andrew greenlights one.

| Item | Why it's parked | Imp | Size | State |
|---|---|---|---|---|
| **Historical state query (FR51)** | Operators query historical state across a time range (audit/ledger + point-in-time reconstruction). Low near-term value + standing storage cost; builds to reserved contract seams. | ★ now / ★★ if real need | M→L | ✅ ratified (design) · [design](../../implementation-artifacts/historical-state-query-design.md) · build deferred (Andrew, revive on a concrete need); archive layers re-home to the Chronicler |
| multi-aspect atomic OCC for `UpdateMetaVertex` | `meta_ddl.go` applies `expectedRevision` to the first changed aspect by design; true multi-key OCC needs a substrate per-key-revision primitive — marginal value. | ★ | M+ | 🗄️ parked |
| freshnessExpiry marker tombstone-on-convergence | A converged marker is read by nothing and harmless; tombstoning buys cleanup not correctness. | ★ | S | 🗄️ parked |
| production freshness-window tuning | A staleness-tolerance vs. timer-churn value judgment — Andrew's call if/when it matters. | ★ | XS | 🗄️ parked |

## Done log — lattice (newest first)

One line per shipped item (`date · SHA · [tag] title`). Oldest roll to `archive/` past ~25.

- 2026-07-02 · `3ef4830` · [docs] Arch-review: docs-truth-sweep — Gateway/Vault built, gateway.md phantom §refs + ops-publish fix, service-actors no-Gateway/enforcement-live, CONCEPT serviceClass dropped; §6.5/§6.12 flagged
- 2026-07-02 · `04bcbf0` · [lease-signing] Vault Fire 5b-ii-d — sev-1 fix: ssn presence check always null on real Vault ciphertext, blocking every real applicant from qualifying; regression test added
- 2026-07-02 · `98cbfe8`+`f69b3e9` · [docs] Arch-review: bridge-and-substrate-doc-refresh — bridge async SPI/poll-timeout/Augur, scheduling @every+bridge-lane, substrate ctx-sigs/6-files/object+publish+stream surfaces/godoc
- 2026-07-02 · `6ddb1fb` · [docs/control-plane] Arch-review: control-plane-surface-contract — new `docs/components/control-plane.md` (subject grammar, per-plane op vocab, reply envelope, transport+stub auth posture, drift guard) + index row
- 2026-07-02 · `7eb3330` · [lease-signing/loftspace-app] Vault Fire 5b-ii-c — landlord decisioning moved onto the RLS-enforced read; trusted console's Approve/Decline retired (lead review, FE-only)
- 2026-07-02 · `13ffb75` · [lease-signing/loftspace-app] Vault Fire 5b-ii-b — landlord readiness clone (`qualified`) via a shared cypher fragment with the convergence lens; 3-layer review (security-plane)
- 2026-07-02 · `5901bc4` · [Refractor] Negative/filter-retraction Fire 3 — target-diff retraction (DiffRetraction opt-in + activation-time unanchored-query guard; 3-layer review; CLOSES the epic, unblocks Vault 5b close)
- 2026-07-02 · `5624392` · [Refractor] Negative/filter-retraction F1+F2 — plain-lens aspect/link reprojection + anchor-self retraction (3-layer review; Fire 3 target-diff next, gates Vault 5b)
- 2026-07-02 · `a710c7a` · [lease-signing/loftspace-app] Vault Fire 5b-ii — landlord applicant contact Secure-Lens columns (name/email/phone; retraction fire now gates 5b close)
- 2026-07-02 · `603fd1f` · [loftspace/vault] Fire 5b-i — applicant roster onto the Secure Lens (applicantRoster retired, applicantRosterRead secure, app reads rewired; duplicateCandidates → Designer)
- 2026-07-02 · `da8279f` · [Core/apps] Read-path authorization (D1) CLOSED — D1.1–D1.5 all shipped; Gateway read-front + Personal Lens are beyond-D1 rows (design §7)
- 2026-07-02 · `51ba38e` + `00b098d` · [Gateway] Fires 1+2 — external write-path translator + live JWKS polling
- *(older entries rolled to [archive/lattice-done.md](archive/lattice-done.md))*
