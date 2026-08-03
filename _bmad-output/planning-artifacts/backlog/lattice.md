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
| **[Bootstrap] Reconcile creates + updates but never removes a retired kernel key** | A kernel entity the current binary no longer builds stays live and executable in a long-lived bucket — a dispatchable DDL, a running lens pipeline, and a held canonicalName that blocks the kernel→package migration. A shrink has no wipe-free path today. | ★ | S–M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/kernel-orphan-retirement-design.md) · fork: retire vs enforce the version bump |
| **[orchestration-base] A closed task's ephemeral grant stays exercisable until expiry** | `capabilityEphemeral`'s three branches filter only `expiresAt > $now` (lenses.go:284-308) and step-3 matches taskKey+opType+target+expiry — status never checked — so CancelTask/CompleteTask do not revoke the grant; a cancelled task's op stays submittable until `expiresAt`. Lens-side `status='open'` filter is the likely shape (myTasks already has it). | ★ | S | 🗄️ shelved (Andrew 2026-07-27: deprioritized; revive: a long-TTL task class or observed misuse) |
| **[Refractor] Post-claim auth-grant latency is unbounded by design** | Auth-plane actorAggregate lenses (`capabilityRoles` incl.) intake the full broad Core-KV stream serially — ~2 events/sec through a ceremony burst — so grant visibility is queue-position luck, 5–20s observed, no upper bound. Facet's `isTransientAuthLag` retry papers over it client-side. | ★★ | L | 🚧 blocked-on Andrew: §6.2 tie-rule edit staged UNCOMMITTED · [design](../../implementation-artifacts/auth-plane-projection-latency-design.md) · do not resume before ratification |
| **[Refractor] A labeled pattern node also binds by body `class`, so a class-only label narrows unsoundly** | `nodeMatches` (executor.go:555-573) binds a vertex whose key type ∉ the label set when its body `class` equals the label, and the narrowing gates decide on that set. location-domain gives `vtx.unit`/`building`/`property` the class `location`; nothing labels `:location` yet. Consumer: the Inc 1 gate, which narrows 4 auth lenses. | ★★★ | S–M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/lens-label-key-type-binding-design.md) · no fork, no contract change |
| **[Refractor] A plain lens narrows its filter with no standing healer, which the actor-aware path refuses to do** | `ActorAwareNarrowingLabels` will not narrow unless `sweeper != nil` — narrowing drops an incidental reprojection that happens to heal a lost row. `NarrowedFilterEligible` has no such conjunct, and `SetSweepPlan`'s only caller is `InstallActorAggregate`. | ★★ | M | 📋 needs design · consumer: 17 zero-relationship lenses incl. 3 GrantTable producers ([why](../../implementation-artifacts/lens-trigger-relation-narrowing-design.md) §4.1) |
| **[Refractor] Relationship-type alternation `[:a\|b]` is silently truncated to the first type** | `visitor.go`'s rel-type visit takes `names[0]` and discards the rest, so `-[:manages\|occupies]->` executes as `manages` alone — silently wrong, not an error. `anchorwalk.go` rejects alternation for exactly this reason; the Cypher path does not. Latent (zero corpus uses). | ★ | XS–S | 📋 ready · reject at parse, mirroring `anchorwalk.go` |
| **[Refractor] A MATCH hot-reload rewrites `plainReprojectLabels` unsynchronized against the consumer** | `useFullEngineBranches` reassigns the map + `plainReprojectAll` — and now `plainReprojectRelations` + `plainRelationsExhaustive` — while `handle` reads them, no lock. The label pair fails closed; the relation pair does not, since `exhaustive` against a nil map skips every link. | ★★ | S | 📋 ready · [why](../../implementation-artifacts/lens-trigger-relation-narrowing-design.md) §4.1 |
| **[Refractor] An OPTIONAL-MATCH-only or negated-WHERE label still excuses an earlier unlabeled sighting** | `ReferencedLabels`' pass 1 is forward-looking within a WITH segment, but neither position constrains the surviving binding, so the executor seeds that variable whole-bucket while the lens reports `exhaustive`. A required later MATCH does constrain it. Detector swept all 101 specs: zero flagged, latent. | ★★ | S | 📐 awaiting-Andrew · folded as Inc 2 of [design](../../implementation-artifacts/lens-label-key-type-binding-design.md) §4.3 |
| **[Tooling] `verify-claim-ceremony.go` asserts a 5s SLA the platform never promised** | `waitForRoleGrant`'s 5s deadline (scripts/verify-claim-ceremony.go:437) reads a real, unbounded latency (see the row above) as "never appears" — 4/5 live runs on the fixed demo box failed this assertion while every grant was confirmed present in Capability KV minutes later. Poll to convergence and report the measured latency instead of asserting a fixed window. | ★ | XS | 📋 ready |
| **[Gateway] Re-claiming an already-claimed identity returns 403, want 400** | `make test-claim-ceremony`'s second-device negative case wants `ClaimKeyInvalid` (400 Bad Request) but gets HTTP 403 — deterministic across 5/5 live runs on the demo box (2026-08-01). Auth now rejects the request before the business-rule check runs, so the client sees a permission error rather than the intended validation error. | ★ | XS–S | 📋 ready |
| **[Refractor] D2 Phase 2 — reverse anchor enumeration for neighbor events** | A referenced-non-anchor-type event still triggers a full per-lens recompute — plain-lens corpus only (D2's eligibility excludes actorAggregate). Demand trigger now measured: 1,325 neighbour-link events wedged `clinicProviders`, each a full recompute of 7 anchors. | ★★ | S | 🚧 seq behind [auth-plane-latency](../../implementation-artifacts/auth-plane-projection-latency-design.md) Inc 3 · re-measure after [relation narrowing](../../implementation-artifacts/lens-trigger-relation-narrowing-design.md) |
| **[Refractor] A staging `WITH`'s carried accumulators are stringified into the grouping key per row** | `projectItems` normalizes every non-aggregating item per binding row, so a generated producer's stage *k* pays `rows_k × Σ|slice_j|` full renderings of already-collected anchor maps — measured 3.3 s / 7.2 GB alloc at 5,009 anchors while peak rows sat at 0.3% of the cap. The accumulators are functionally determined by the actor an earlier stage grouped on. | ★★ | S–M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/full-engine-grouping-key-reduction-design.md) |
| **[Pkgmgr] `validateGrantSliceVarNames` cannot see a variable inside a node property map** | `patternVarNames` reports pattern variables only; the chain parser skips a `{...}` property map wholesale, so `(bk:booking {slice: grantSlice0})` is accepted and emitted verbatim. Fail-closed today (the comparison never matches → under-grant) and no shipped `Chain` carries a property map, but the guard misses its stated invariant. | ★ | XS–S | 📋 ready · record property-map vars at parse time, or reject a non-literal value |
| **[Refractor] Sibling OPTIONAL branches multiply instead of folding** | `applyMatch` builds the full `[]binding` cross product; the 1M-row cap then permits ~730 MB. 14 hand-authored lenses have 2–8 independent branches in one stage, incl. `capabilityEphemeral` (grants) and `myTasks`; producer staging fixed only the generated ones. Fold each branch into its own aggregator instead. | ★★ | L | 📐 awaiting-Andrew · [design](../../implementation-artifacts/full-engine-independent-branch-decomposition-design.md) · fork: engine vs authoring convention + gate |
| **[Refractor] Lens health is liveness-only — a frozen row renders green** | 12 orphanedTaskGrants rows sat 12 days stale while the lens card showed green: status/lag/error health cannot see per-row wrongness, and the only live catcher was Weaver's LensEffectMismatch. A sampled projected-vs-recomputed row audit (sweep-side divergence counter surfaced to Health KV) is the missing signal. | ★★ | M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/lens-projection-divergence-audit-design.md) · no fork, no contract change |
| **[Refractor] Streaming the binding set (remove the ceiling, not lower it)** | `[]binding` → a lazy iterator, so peak rows stop being the whole materialized set. Branch decomposition takes every live lens to its largest single branch, so this only pays for a lens whose ONE branch's own fan-out nears the cap. | ★ | L | 🗄️ shelved · [design §9-C](../../implementation-artifacts/full-engine-independent-branch-decomposition-design.md) · revive: Inc 2 reports a single-group peak near the cap |
| **[Loom] Guardless-step recovery check-before-act probe** | On total `loom-state` loss + a re-triggered `StartLoomPattern`, a fresh instance replays guards from cursor 0 (re-runs an already-applied guarded step). | ★ | S–M | 🗄️ shelved-backup (Andrew: no new engine Core-KV reads) |
| **[Edge] An orphan a purge cannot reap has no server-side backstop** | A revoked credential fails the sign-out reap (the auth callout refuses its connection, correctly) and a crashed host never reaps at all — each strands a durable no client can name again. `InactiveThreshold` is the shape; the browser shell's 30 min cannot be copied to the Go host. | ★★ | S–M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/edge-sync-orphan-expiry-design.md) · no fork, no contract change; unblocked (§7) |
| **[Facet] Two concurrent first-time `Acquire`s for one identity race the mirror** | `engineManager.Acquire` releases `m.mu` before `newEngine`, so both callers open the same bbolt mirror; the loser now fails with `ErrTimeout` after 2s (`store.Open`'s bounded lock) rather than hanging forever, but a request still 500s. The map-level "lost a race" guard runs only *after* the build. | ★★ | S | 📋 ready · consumer: a first sign-in whose SSE attach and first write land together · serialize the build per identity |
| **[Edge] A cold sign-in replays the actor's retained history, not their world** | Measured live 2026-07-31: 2,049 frames to deliver a 14-key world (146×), `ready` at 33s. One subject `lattice.sync.user.<id>` carries every key's every revision, consumed `DeliverAll` (hardcoded `substrate/consumer.go:142`; the edge transport seam declares no policy field). | ★★ | M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/edge-cold-signin-delivery-position-design.md) · fork: reposition vs compaction; size L→M |
| **[Pkgmgr] A capability apply may `upgradeExisting` into a platform package** | `CapabilityApplyPlanForProposal` excludes no package, so an approved proposal naming `capability-author`/`orchestration-base` diff-applies a one-artifact Definition into it. Behind the human review gate; pre-existing, now reachable from a second authoring lane. | ★ | S | 📋 ready · `internal/pkgmgr/capabilityapply.go:100-118` |
| **[Pkgmgr] Client-ceremony ops — Inc 4 (paired code + credential-scoped actor)** | The two-device half of the ceremony vocabulary: a secret armed on one device, returned on a different op as the raw credential actor. The rest of the item shipped; this is what the last 2 `[no-op-meta:]` exemptions wait on. | ★★ | M | 🗄️ shelved (revive: a two-device consumer) · [design §4.4](../../implementation-artifacts/client-ceremony-op-descriptors-design.md) |
| **[Refractor] `RETURN DISTINCT` dedupes rows by `json.Marshal`, which erases a node column** | `applyReturn` (executor.go:1231) keys DISTINCT on the marshalled row and drops the marshal error; `nodeRef`'s fields are all unexported, so a node-valued column renders `{}` and two rows differing only there collapse to one. No live victim — every corpus `RETURN` item is a `.key` or an aspect field. Use the injective `normalizeForKey` the grouping path uses. | ★ | XS–S | 📋 ready · [why](../../implementation-artifacts/full-engine-grouping-key-reduction-design.md) §12 |
| **[Refractor] A structural pause is terminal even where its own probe could settle it** | The tier means "pause until reconciled", but nothing re-checks: `waitWhilePaused` blocks on the operator across restarts, while a protected/grant lens's `VerifyProtectedTable` already adjudicates that exact condition on a loop for *infra* pauses. So a lens self-heals if its table is missing at activation and stays dark forever if it breaks after. | ★★ | M | 📐 Inc 2 only · [design](../../implementation-artifacts/structural-pause-recovery-design.md) §4.2 · Inc 1+3 shipped |
| **[Processor] A declared read is never scope-checked against the actor's grant** | `contextHint` is client-supplied and step 3 authorizes on `operationType + actor + authContext` without inspecting it, so any actor holding any op grant can have any key hydrated. Pre-existing and unchanged by class (g) — but `derive_reads` makes the PLATFORM the party exercising it, on every submitter rather than the ones that asked. | ★★ | L–XL | 📐 awaiting-Andrew · [design](../../implementation-artifacts/declared-read-scope-authorization-design.md) · no fork · #2 §2.5 staged UNCOMMITTED |
| **[Tooling] The G2 derived-key gate does not cover `internal/` submitters** | `internal/gateway/whoami.go` re-implements identity-domain's email normalization and derives both index keys; `internal/objectmanager` derives too. The gate excludes `internal/` wholesale because that tree also OWNS the primitive. Needs an allowlist of the three owning packages plus annotations on the legitimate sites. | ★ | S | 📋 ready · [why](../../implementation-artifacts/client-ceremony-op-descriptors-design.md) §12.7 |
| **[edge-manifest] No surface offers a STANDING staff op, so two shipped descriptors are unreachable** | Every `opButton` call site filters by `viaServices` or a `dispatchTargetType` match, so a role-granted op with no target — `CreateUnclaimedIdentity`, `RotateClaimKey` — projects a `manifest.op` row no view renders. Pre-dates Inc 3; Inc 3 made it binding. | ★★ | M | 📋 ready · consumer: the two ceremony descriptors ([why](../../implementation-artifacts/client-ceremony-op-descriptors-design.md) §13.6) |
| **[edge-manifest] Nothing projects an `identity` entity, so an identity-typed picker has no candidates** | `manifest.ent` stamps eight `entityType` literals and `edgeIdentitySpec` six `selfAnchors` types; `identity` is in neither. `RotateClaimKey` degrades honestly on this today and becomes completable with no code change once unclaimed identities are projected for staff. | ★★ | M | 📋 needs design · consumer: `RotateClaimKey`'s `x-entityRef` picker |
| **[Facet] A durably-queued ceremony write outlives the plaintext it minted** | The reveal is held in memory only, so a reload or sign-out while the intent sits in the offline queue drops the secret — and the write still lands on the next drain, arming an identity nobody can claim, with no signal on any surface. Persisting the plaintext is not the answer; warning before the queue drains is. | ★★ | S–M | 📋 ready · consumer: staff creating an identity offline |
| **[identity-domain] A credential whose identity vertex was never provisioned projects no binding row** | `identityCredentialBindingsRead` anchors on `(c:identity)` and `readNode` returns nil for a missing key, so the row silently never appears while `identityCredentialsRead` still lists that credential from the decrypted array. Reachable via `lattice identity claim --actor`, which skips the Gateway's provisioning pre-flight. | ★★ | S–M | 📋 needs design · fork: provision in the CLI vs re-anchor the lens · binding in Inc 2b-2 |
| **[Refractor] A lens cannot project a relationship's own `data`** | `RelPattern.Variable` is parsed (`full/visitor.go:274`) but `traverseRel` extends a binding with the neighbour NODE only (`full/executor.go`), so `MATCH (c)-[b:rel]->(u) RETURN b.data.x` has no meaning — a link's data is unreadable by any lens. Every relationship fact therefore has to be duplicated onto a vertex to be projectable. | ★★ | M | 📋 needs design · consumer: `identityCredentialBindingsRead`'s missing `bound_at` ([why](../../implementation-artifacts/client-ceremony-op-descriptors-design.md) §14.2) |
| **[privacy-base] A shred erases the credential→person link but leaves the index vertex naming both** | `ShredIdentityKey` tombstones every `boundTo` link because it names in plaintext which credential belonged to whom; `vtx.credentialindex.<hash>` carries the same `{actorKey, identityKey}` and is in no erasure set. The correlation survives, decrypt-free. | ★★ | S–M | 📋 needs design · consumer: the erasure guarantee ([why](../../implementation-artifacts/client-ceremony-op-descriptors-design.md) §15.6) |
| **[identity-domain] A provisioned raw actor has no `credentialindex`, so its sign-in method is unlistable** | `ProvisionConsumerIdentity` writes the identity vertex and `.idpBinding` only — no index vertex, no `boundTo`, no `credentialBinding`. So a Scenario-B person's only credential is invisible to `identityCredentialBindingsRead`, and no reconcile reaches it. An incomplete list is harder to notice than an empty one. | ★★ | M | 📋 needs design · consumer: Inc 2b-2's `signInMethods` pane ([why](../../implementation-artifacts/client-ceremony-op-descriptors-design.md) §15.6) |
| **[Processor] `derive_reads` binds `state`/`ddl` to empty dicts rather than failing closed** | `kv` and `nanoid` are fail-closed stubs; `state[k]` in a derivation returns a silent `None` instead of the loud error `kv.Read` gives. Within Contract #2 §2.5, which mandates stubs only for those two — but it is the weakest link in the purity argument, and `state` is what an author reaches for by habit. | ★ | S | 📋 ready · [why](../../implementation-artifacts/client-ceremony-op-descriptors-design.md) §12.7 |

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


### Security & trust boundary
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Processor] Whole-set `state` exposure remains an existence oracle for sensitive classes** | A guard keyed on consumption still splits on a surplus sensitive declared read when the script takes a whole-set exposure (`items()`/`values()`/rendering `state`) — the flip is correct, so only read-scope validation of the declared set closes it. | ★ | S | 📐 folded as Inc 1 of [design](../../implementation-artifacts/declared-read-scope-authorization-design.md) §4.2 · D1 sequencing falsified |
| **[appsession] The production IdP posture cannot open a session** | `setCookie` runs only under a non-nil `Signer`, so with `_JWT_PUBLIC_KEY`/`_ISSUER` set nothing can issue the cookie — the verify-only posture is unreachable (401 everywhere), and `/api/session/refresh` 404s so every FE write path dies with it. Design: the kit becomes the OIDC code-flow RP. | ★★ | L | 🗄️ shelved (revive: first real-IdP deployment) · ✅ design Andrew-ratified 2026-07-25 · [design](../../implementation-artifacts/appsession-oidc-production-signin-design.md) |
| **NATS write restriction — Fire 4 (production mTLS)** | Fires 1–3 closed the fabricated-KV-write surface at the account level; the remaining fire binds subject permissions to client certificates instead of NKeys, which only matters off the dev stack. | ★ now / ★★ prod | M | 🗄️ shelved (revive: production deployment) · [design](../../implementation-artifacts/nats-account-write-restriction-design.md) §Fire-3-status |
| **Keyed identity-index hashes (HMAC)** | Unkeyed `sha256NanoID` contact hashes are dictionary-testable with substrate access and persist in JetStream history post-shred; a Vault-keyed HMAC bounds it but needs a MAC primitive + key custody at every hash computer, and must migrate ALL index consumers (identityindex, provision probe, dedup) in one stroke. | ★ now / ★★ prod | M | 🗄️ shelved (revive: production threat model) · [analysis](../../implementation-artifacts/dedup-over-encrypted-pii-design.md) §9.1/§10-C |

### Orchestration & edge — Loupe-routed (2026-07-25 PO pass)
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|

### Privacy / Vault
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Vault] Sensitive aspects are identity-anchored, so event-scoped PHI has no home** | Step 6 rejects a sensitive aspect on any non-identity parent (`sensitiveAspectScope`), and one note per visit cannot be an identity aspect — so clinic `.encounter` PHI sits plaintext in Core KV, unprojectable, and outlives a crypto-shred. Needs anchor/custody separation + a retention posture. | ★★★ | L | 📐 awaiting-Andrew · [design](../../implementation-artifacts/subject-anchored-sensitive-aspects-design.md) · Contracts #1/#3 staged UNCOMMITTED |

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
| **`TestSettle_ConsumerSelfScope_Allowed` fails its outcome assert under full-suite load** | `packages/cafe-domain`'s self-service Settle drive returned a non-Accepted outcome once in a local `go test ./... -p 4` where the package took 302s vs 21s alone; passes in isolation on main and on the branch. Not the embedded-NATS handshake signature — it is an assertion, so it needs root-causing rather than the contention triage. | ★★ | S | 📋 ready · owner: Whetstone |
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

- 2026-08-03 · `4336978b` · [Refractor] a lens subscribes to the relations it traverses, not every link touching a referenced type — `clinicProviders`' frozen floor was 674 unbindable `providedTo` links; its rebuild now drains in <1 min
- 2026-08-03 · `80b30bdf` · [pkgmgr,edge-manifest,identity-domain,facet] client-ceremony Inc 2b-3 — the signInMethods pane confirmed live end-to-end, one own row per consumer under RLS; Inc 4 shelved
- 2026-08-03 · `80b30bdf` · [Refractor] an interrupted rebuild re-arms its watcher instead of latching "rebuilding" — 5 lenses un-latched live, the capability sweep un-suppressed, degraded → healthy
- 2026-08-03 · `bb027dc5` · [Refractor,lattice-CLI] structural pause Inc 1+3 — the pause keeps its cause, `lattice lens pause|resume|rebuild|health` ships; 3 frozen read models resumed live
- 2026-08-03 · `c2112bf1` · [Refractor] a Protected lens that gains a column converges its table instead of pausing structurally on it
- 2026-08-03 · `ec87b8f4` · [pkgmgr,edge-manifest,identity-domain,facet] client-ceremony Inc 2b-2 — PaneSpec.Surface, the signInMethods pane, UnlinkCredential's descriptor (exemption retired), two offer gates
- 2026-08-03 · `5d464007` · [identity-domain,lattice-CLI] ReconcileCredentialBinding — the boundTo plane converges on the index; a retraction is never overturned (0→6 edges live)
- *(older entries rolled to [archive/lattice-done.md](archive/lattice-done.md); newest rolled entry `9c00567f`)*
