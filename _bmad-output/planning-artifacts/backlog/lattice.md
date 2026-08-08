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
| **[Bootstrap] Reconcile creates + updates but never removes a retired kernel key** | A kernel entity the current binary no longer builds stays live and executable in a long-lived bucket — a dispatchable DDL, a running lens pipeline, and a held canonicalName that blocks the kernel→package migration. A shrink has no wipe-free path today. | ★ | S–M | ✅ ratified 2026-08-06 (Inc 1; fork → retire verb) · [design](../../implementation-artifacts/kernel-orphan-retirement-design.md) · Inc 2 gated on Inc 1's census |
| **[orchestration-base] A closed task's ephemeral grant stays exercisable until expiry** | `capabilityEphemeral`'s three branches filter only `expiresAt > $now` (lenses.go:284-308) and step-3 matches taskKey+opType+target+expiry — status never checked — so CancelTask/CompleteTask do not revoke the grant; a cancelled task's op stays submittable until `expiresAt`. Lens-side `status='open'` filter is the likely shape (myTasks already has it). | ★ | S | 🗄️ shelved (Andrew 2026-07-27: deprioritized; revive: a long-TTL task class or observed misuse) |
| **[Tooling] `verify-claim-ceremony.go` asserts a 5s SLA the platform never promised** | `waitForRoleGrant`'s 5s deadline (scripts/verify-claim-ceremony.go:437) reads a real, unbounded latency (see the row above) as "never appears" — 4/5 live runs on the fixed demo box failed this assertion while every grant was confirmed present in Capability KV minutes later. Poll to convergence and report the measured latency. | ★ | XS | 📋 ready |
| **[Processor] A re-claim dies in hydrate, so NFR-S6's generic rejection never renders** | The first claim tombstones `.claimKey`; the re-claim still declares it, so step 4 fails `decrypt …claimKey: read deleted sensitive aspect` → HTTP 500 before `ClaimIdentity` can return the generic 400 `ClaimKeyInvalid`, making "already claimed" distinguishable from "wrong key". | ★★ | S | 📋 ready · 3/3 live runs · [why](../../implementation-artifacts/auth-plane-projection-latency-design.md) §17.7 |
| **[Refractor] D2 Phase 2 — reverse anchor enumeration for neighbor events** | A referenced-non-anchor-type event still triggers a full per-lens recompute — plain-lens corpus only (D2's eligibility excludes actorAggregate). Measured: 1,325 neighbour-link events wedged `clinicProviders`, each a full recompute of 7 anchors. | ★★ | S | 📋 ready · unblocked by Inc 3b · [design](../../implementation-artifacts/auth-plane-projection-latency-design.md) §4.7 · wire the plain arm to the shared unit |
| **[Refractor] A staging `WITH`'s carried accumulators are stringified into the grouping key per row** | `projectItems` normalizes every non-aggregating item per binding row, so a generated producer's stage *k* pays `rows_k × Σ|slice_j|` full renderings of anchor maps that are functionally determined by what an earlier stage grouped on — redundant by construction. | ★★ | S–M | ✅ ratified 2026-08-06 (Winston, delegated) · [design](../../implementation-artifacts/full-engine-grouping-key-reduction-design.md) · unblocked |
| **[Pkgmgr] `validateGrantSliceVarNames` cannot see a variable inside a node property map** | `patternVarNames` reports pattern variables only; the chain parser skips a `{...}` property map wholesale, so `(bk:booking {slice: grantSlice0})` is accepted and emitted verbatim. Fail-closed today (the comparison never matches → under-grant), but the guard misses its stated invariant. | ★ | XS–S | 📋 ready · record property-map vars at parse time, or reject a non-literal value |
| **[Refractor] Sibling OPTIONAL branches multiply instead of folding** | `applyMatch` builds the full `[]binding` cross product; the 1M-row cap then permits ~730 MB. 14 hand-authored lenses have 2–8 independent branches in one stage, incl. `capabilityEphemeral` and `myTasks`; producer staging fixed only the generated ones. | ★★ | L | ✅ ratified 2026-08-06 (Winston, delegated; fork → engine) · [design](../../implementation-artifacts/full-engine-independent-branch-decomposition-design.md) · Inc 2 first |
| **[Refractor] The CDC write path audits a retraction the ordering guard declined** | `writeResults`' delete arm never consults an outcome (`pipeline.go:2043-2047`), so `writeAudit` publishes a `delete` fact for a revocation the §6.2 guard dropped. `DeleteWithOutcome` exists, but the upsert arm's mirror question comes first: `UpsertOutcome.Wrote` stays true on decline, so a naive swap makes the audit paths disagree. | ★ | S | 📋 ready · consumer: the audit log's account of revocations · fork: decide both arms together |
| **[Refractor] Lens health is liveness-only — a frozen row renders green** | 12 orphanedTaskGrants rows sat 12 days stale while the lens card showed green: status/lag/error health cannot see per-row wrongness, and the only live catcher was Weaver's LensEffectMismatch. A sampled projected-vs-recomputed row audit is the missing signal. | ★★ | M | 📋 ready · [design](../../implementation-artifacts/lens-projection-divergence-audit-design.md) · next: Fire 2 plain-lens Auditor |
| **[Refractor] Streaming the binding set (remove the ceiling, not lower it)** | `[]binding` → a lazy iterator, so peak rows stop being the whole materialized set. Branch decomposition takes every live lens to its largest single branch, so this only pays for a lens whose ONE branch's own fan-out nears the cap. | ★ | L | 🗄️ shelved · [design §9-C](../../implementation-artifacts/full-engine-independent-branch-decomposition-design.md) · revive: Inc 2 reports a single-group peak near the cap |
| **[Loom] Guardless-step recovery check-before-act probe** | On total `loom-state` loss + a re-triggered `StartLoomPattern`, a fresh instance replays guards from cursor 0 (re-runs an already-applied guarded step). | ★ | S–M | 🗄️ shelved-backup (Andrew: no new engine Core-KV reads) |
| **[Edge] An orphan a purge cannot reap has no server-side backstop** | A revoked credential fails the sign-out reap (the auth callout refuses its connection, correctly) and a crashed host never reaps at all — each strands a durable no client can name again. `InactiveThreshold` is the shape; the browser shell's 30 min cannot be copied to the Go host. | ★★ | S–M | ✅ ratified 2026-08-06 · [design](../../implementation-artifacts/edge-sync-orphan-expiry-design.md) |
| **[Facet] Two concurrent first-time `Acquire`s for one identity race the mirror** | `engineManager.Acquire` releases `m.mu` before `newEngine`, so both callers open the same bbolt mirror; the loser now fails with `ErrTimeout` after 2s (`store.Open`'s bounded lock) rather than hanging forever, but a request still 500s. | ★★ | S | 📋 ready · consumer: a first sign-in whose SSE attach and first write land together · serialize the build per identity |
| **[Edge] A cold sign-in replays the actor's retained history, not their world** | Measured live 2026-07-31: 2,049 frames to deliver a 14-key world (146×), `ready` at 33s. One subject `lattice.sync.user.<id>` carries every key's every revision, consumed `DeliverAll` (hardcoded `substrate/consumer.go:142`). | ★★ | M | ✅ ratified 2026-08-06 (fork → reposition) · [design](../../implementation-artifacts/edge-cold-signin-delivery-position-design.md) |
| **[Pkgmgr] A capability apply may `upgradeExisting` into a platform package** | `CapabilityApplyPlanForProposal` excludes no package, so an approved proposal naming `capability-author`/`orchestration-base` diff-applies a one-artifact Definition into it. Behind the human review gate. | ★ | S | 📋 ready · `internal/pkgmgr/capabilityapply.go:100-118` |
| **[Pkgmgr] Client-ceremony ops — Inc 4 (paired code + credential-scoped actor)** | The two-device half of the ceremony vocabulary: a secret armed on one device, returned on a different op as the raw credential actor. The rest of the item shipped; this is what the last 2 `[no-op-meta:]` exemptions wait on. | ★★ | M | 🗄️ shelved (revive: a two-device consumer) · [design §4.4](../../implementation-artifacts/client-ceremony-op-descriptors-design.md) |
| **[Refractor] A structural pause is terminal even where its own probe could settle it** | The tier means "pause until reconciled", but nothing re-checks: `waitWhilePaused` blocks on the operator across restarts, while a protected/grant lens's `VerifyProtectedTable` already adjudicates that exact condition on a loop for *infra* pauses. So a lens self-heals if its table is missing at activation and stays dark forever if it breaks after. | ★★ | M | ✅ ratified 2026-08-06 · [design](../../implementation-artifacts/structural-pause-recovery-design.md) §4.2 (Inc 2) |
| **[Processor] A declared read is never scope-checked against the operation** | `contextHint` is client-supplied and step 3 authorizes on `operationType + actor + authContext` without inspecting it, so any actor with any op grant can have any key hydrated and a sensitive aspect decrypted. Contained today unless a script renders the whole state. | ★★ | L–XL | 🗄️ shelved (revive: a whole-set-exposure script, or a 2nd unguarded payload read) · [design](../../implementation-artifacts/declared-read-scope-authorization-design.md) |
| **[Tooling] The G2 derived-key gate does not cover `internal/` submitters** | `internal/gateway/whoami.go` re-implements identity-domain's email normalization and derives both index keys; `internal/objectmanager` derives too. The gate excludes `internal/` wholesale because that tree also OWNS the primitive. Needs an allowlist of the three owning packages plus site annotations. | ★ | S | 📋 ready · [why](../../implementation-artifacts/client-ceremony-op-descriptors-design.md) §12.7 |
| **[Facet] A durably-queued ceremony write outlives the plaintext it minted** | The reveal is held in memory only, so a reload or sign-out while the intent sits in the offline queue drops the secret — and the write still lands on the next drain, arming an identity nobody can claim, with no signal on any surface. | ★★ | S–M | 📋 ready · consumer: staff creating an identity offline |
| **[identity-domain] A credential whose identity vertex was never provisioned projects no binding row** | `identityCredentialBindingsRead` anchors on `(c:identity)` and `readNode` returns nil for a missing key, so the row silently never appears while `identityCredentialsRead` still lists that credential from the decrypted array. Reachable via `lattice identity claim --actor`, which skips the Gateway's provisioning pre-flight. | ★★ | S–M | ✅ ratified 2026-08-06 · Inc 2 of [design](../../implementation-artifacts/credential-binding-plane-lifecycle-design.md) |
| **[Refractor] A lens cannot project a relationship's own `data` or name** | `RelPattern.Variable` is parsed (`full/visitor.go:274`) but `traverseRel` binds only the neighbour node, so `b.data.x` / `type(b)` are silent nulls — a link's data and name are unreadable by any lens. Consumer: `objectAttachments`, which cannot project the linkName `DetachObject` needs. | ★★ | M | ✅ ratified 2026-08-06 (Winston, delegated) · [design](../../implementation-artifacts/relationship-data-projection-design.md) · narrow bind only |
| **[privacy-base] Erasure is one op's atomic batch, so it can refuse to erase and leaves correlations live** | `ShredIdentityKey` is five jobs in one commit and fails above 999 mutations, so a well-connected person cannot be erased at all. Becomes a Loom pattern with a Weaver-driven convergent tail and a seal (Andrew 2026-08-06). | ★★★ | L–XL | 🏗️ Fire B step 2 done · [design](../../implementation-artifacts/erasure-orchestration-design.md) · 🔭 contract #10 §10.5 UNCOMMITTED · next: step 3, narrow ShredIdentityKey |
| **[identity-domain] A provisioned raw actor has no `credentialindex`, so its sign-in method is unlistable** | `ProvisionConsumerIdentity` writes the identity vertex and `.idpBinding` only — no index vertex, no `boundTo`, no `credentialBinding`. So a Scenario-B person's only credential is invisible to `identityCredentialBindingsRead`, and no reconcile reaches it. | ★★ | M | ✅ ratified 2026-08-06 · Inc 3a of [design](../../implementation-artifacts/credential-binding-plane-lifecycle-design.md) · 3b deferred |
| **[Processor] `derive_reads` binds `state`/`ddl` to empty dicts rather than failing closed** | `kv` and `nanoid` are fail-closed stubs; `state[k]` in a derivation returns a silent `None` instead of the loud error `kv.Read` gives. Within Contract #2 §2.5, which mandates stubs only for those two — but it is the weakest link in the purity argument, and `state` is what an author reaches for by habit. | ★ | S | 📋 ready · [why](../../implementation-artifacts/client-ceremony-op-descriptors-design.md) §12.7 |
| **[Refractor] An actor-aware lens narrows by label but never by relation** | `ConsumerFilter`'s relation dimension is gated to plain pipelines: `actorAwareFanOutRelevant` judges by endpoint type alone, so a relation-pinned subject would withhold a link its fan-out arm keeps. Needs a relation gate on that arm first. | ★ | S–M | 📋 ready · consumer: `capabilityRoles` — the remainder of Term A · [why](../../implementation-artifacts/auth-plane-projection-latency-design.md) §15.11 |
| **[Refractor] Narrowing's mandated healer is never exercised end to end** | §4.2 refuses to narrow a lens without a sweep plan, because narrowing removes the incidental reprojection that used to heal a lost row — yet no test deletes a row under a narrowed consumer and requires `RunSweep` to restore it. | ★★ | M | 📋 ready · consumer: the next fire editing a narrowed label set · [why](../../implementation-artifacts/auth-plane-projection-latency-design.md) §15.11 |
| **[Refractor] A pre-install `ConsumerFilter` call would narrow with no conjunct evaluated** | Order is correct today and verified, but the failure mode is inverted: an uninstalled enumerator reads as PLAIN, whose conditions activation has already met — so calling early yields the most aggressive filter with none of §4.2's conjuncts checked. | ★★ | S | 📋 ready · consumer: the next fire adding a `cmd/refractor` install stage · [why](../../implementation-artifacts/auth-plane-projection-latency-design.md) §15.11 |
| **[Refractor] The affected-anchor index refuses every lens carrying a `WITH`** | The conjunct is blanket where the hazard is narrow: only a variable a later clause re-references after the `WITH` dropped it re-seeds by bucket scan. It keeps 14 of 30 actorAggregate lenses on the BFS, and the generated `cap-read.<domain>` family's `WITH` is structural, so that family is reachable no other way. | ★★ | S–M | 📋 ready · consumer: the generated producer family · [why](../../implementation-artifacts/auth-plane-projection-latency-design.md) §17.6 |
| **[Refractor] The enumerator's anchor-type fast path returns a singleton** | `Enumerate` short-circuits to `[eventKey]` when the event vertex's type is the actor type (`actor_enumerator.go:107-109`), so an aspect event on one identity never reaches an anchor binding THAT identity at a non-anchor position — `capabilityEphemeral`'s `report:identity`. | ★★ | S | 📋 ready · consumer: the first lens reading a non-anchor identity position's data · [why](../../implementation-artifacts/auth-plane-projection-latency-design.md) §17.6 |
| **[Loom/Weaver] A dispatcher cannot declare its op's class-(e) enumerations** | A `kv.Links` walk is declared through `ContextHint.Enumerations`, expressible by neither the Loom `systemOp` submit path nor a Weaver `directOp` (`GapActionSpec` has no field). Metadata only — the walks execute correctly. | ★ | XS–S | 📋 ready · consumers: the `identityErasure` pattern; `identityErasureComplete` · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 3 res 1, inc 7 res 4 |
| **[identity-domain] A sealed identity's live index vertex denies the next person their own** | A registrant sharing an erasure-sealed identity's email hits that still-live `identityindex`, so they get no index vertex and no `duplicateOf` — and a later revive leaves the middle person permanently unindexed. | ★★ | S | 📋 ready · consumer: the §6 probe-hit gate · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 4 res 4 |
| **[privacy-base] The erasure walk's real ceiling is wall time, so its named stop never fires** | `kv.Links` costs one KVGet per key and re-lists the arm per page, so the 250ms wall binds far below the seal's 40,960-link budget: a wide subject dies as `ScriptTimeout`, not `ErasureVerificationUnreachable` — fail-closed but undiagnosable. | ★★ | S–M | 📋 ready · consumer: the operator surface (§12 step 4) · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 6 res 1–2 |
| **[identity-domain] A `credentialindex` with no `boundTo` link is residue nothing can walk** | It carries its identity in its body with no link to it, so no enumeration reaches one; the sweep covers only those that have a `boundTo`. §9.2(i) names the class; the attestation's coverage does not. | ★ | S | 📋 ready · consumer: the erasure attestation's coverage claim · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 6 res 3 |
| **[Weaver] A gap whose re-dispatch IS the progress advances once per mark lease** | A paged loop's own reprojection lands while its mark's 30-min lease is live, so `fireEpisode` takes the anti-storm drop and only the reconciler's reclaim re-dispatches — 64 links / 30 min. | ★★ | M–L | 📋 ready · needs design (engine seam) · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 7 res 1 |
| **[privacy-base] A stuck erasure sweep re-dispatches indefinitely with no escalation** | `maxretries_<g>` is ruled out (inert at the sweeps' scale, terminally parking at the seal's); a `surface` gap cannot fire either — a hard-failing sweep commits nothing, so "stuck" is history the row does not carry. | ★★ | S–M | 📋 ready · consumer: the operator surface (§12 step 4) · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 7 res 2 |
| **[Weaver] A `surface` gap's Health issue carries no entity segment** | `issueKeyGap` keys per `(target, column)`, so with two erasures in flight the subject whose halves land first clears the issue raised for the stuck one. Benign for aggregate gaps; wrong per-subject. | ★ | S | 📋 ready · consumer: `identityErasureComplete`'s async-half gaps · [why](../../implementation-artifacts/erasure-orchestration-design.md) inc 7 res 3 |
| **[Tooling] No gate enforces `gofmt`, and four files in one package have already drifted** | There is no gofmt step in `.github/workflows/*`, `gofmt` is not in golangci's enabled set, and the Makefile has no target — so formatting drift is invisible until someone runs `gofmt -l` by hand. `internal/refractor/pipeline` alone carries four unformatted files today. | ★ | XS | 📋 ready · consumer: any fire whose mechanical edit leaves a file unformatted and no gate says so |

### Survey log (round-robin rotation)

Rotation memory only — findings are the filed rows; fire narratives live in commits, never here.
Components: Core · Weaver · Loom · Refractor · Bootstrap · object-store-manager (+ the cross-cutting
feature backlog; Loupe moved to its own lane, [loupe.md](loupe.md)). Survey the stalest
(`git log -1 --format=%ct -- <path>`), note ONE dated line, rotate.

- 2026-07-13 Core (processor healthy, clean lint/vet, no TODOs; step 6.5 sensitive-encrypt path was 0% covered, filled 80.1%→82.0%).
- 2026-07-18 Weaver (healthy, 86.8%/78.6%/91.3% cov, clean lint, no TODOs; filed error-branch-coverage + a doc-drift fix).
- 2026-07-18 Loom (healthy, 82.3%/80.2% cov, clean lint, no TODOs; prior deadline/redelivery gaps already shipped `495476b`; filed starlark-guard-sandbox-value-iface-uncovered).
- 2026-07-18 Refractor (healthy, build/lint clean; confirmed all 8 07-06-review findings already resolved in code — no new rows).
- 2026-07-19 object-store-manager (67.5%/91.4% cov, clean lint, no TODOs; filed doc-drift fix + cascade error-branch coverage).
- 2026-07-19 Bootstrap (69.3% cov, clean lint, no TODOs; filed stale-bootstrap-json-no-freshness-probe (★★, the documented Known-gap) + seed-idempotency-branch-coverage).
- 2026-07-19 Core (processor 81.8%/substrate 76.2% cov, clean lint, no TODOs; filed consumer-supervisor-accessors-untested + outbox-consumer-undercovered + processor.md UninstallPackage doc-drift).
- 2026-07-25 Refractor (pre-scoped, out of rotation) — filed shared-bucket rebuild-truncate hazard from the cap-read design's adversarial review; next unchanged.
- **Next:** Weaver.

## Arch-review intake — CLOSED, no open rows

All three 2026-07 reviews' corrections shipped; verdicts live in the reports:
[platform](../../../docs/reviews/arch-review-2026-07-02.md) ·
[Refractor](../../../docs/reviews/arch-review-2026-07-06.md) ·
[Weaver](../../../docs/reviews/arch-review-2026-07-06-weaver.md).

## Lattice feature backlog — the Phase-3 build queue

The AI-driven flywheel draws from this list (Surveyor files → Designer designs → Steward builds the
ratified). Everything here needs design and is fair game **except** 🚧 Andrew-gated rows. Architectural
**forks** (Gateway, read-path auth, Vault, multi-cell, HA-NATS) and **frozen-contract** changes are
designed-through, but the *fork decision* + the *contract commit* are Andrew's.


### Security & trust boundary
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Processor] Whole-set `state` exposure remains an existence oracle for sensitive classes** | A guard keyed on consumption still splits on a surplus sensitive declared read when the script takes a whole-set exposure (`items()`/`values()`/rendering `state`) — the flip is correct, so only read-scope validation of the declared set closes it. | ★ | S | 🗄️ shelved with Inc 1 of [design](../../implementation-artifacts/declared-read-scope-authorization-design.md) (revive: a whole-set-exposure script) |
| **[lease-signing] A payload-named identity aspect is read with no ownership guard** | `CreateLeaseServiceInstance` takes `subjectKey` and (via `resolve_subject_params`) the aspect segment from the payload, checked for shape + liveness only (`scripts.go:1168`, `Scope:"any"`). Step 6 rejects the op before an `external.*` event can leak, but that guard is external-plane only — deriving PII into an ordinary domain event is unguarded. | ★★ | S | 📋 ready · guard the op + an authoring gate for the class, one fire |
| **[Loom] An externalTask can only declare its SUBJECT's own aspects for egress** | `inferExternalTaskReads` parses `subject.<aspect>` only (`internal/loom/externaltask_params.go:42`), so a LINKED vertex's field is undeclarable in `contextHint.egressReads` and the commit-path guard rejects it plaintext (`internal/processor/step6_validate.go:110`) — a vendor call needing a neighbour's sensitive field renders blank (LoftSpace: the executed lease names its tenant by raw NanoID). | ★★ | M | 📋 ready · needs a link-hop template form |
| **[appsession] The production IdP posture cannot open a session** | `setCookie` runs only under a non-nil `Signer`, so with `_JWT_PUBLIC_KEY`/`_ISSUER` set nothing can issue the cookie — the verify-only posture is unreachable (401 everywhere), and `/api/session/refresh` 404s so every FE write path dies with it. Design: the kit becomes the OIDC code-flow RP. | ★★ | L | 🗄️ shelved (revive: first real-IdP deployment) · ✅ design Andrew-ratified 2026-07-25 · [design](../../implementation-artifacts/appsession-oidc-production-signin-design.md) |
| **NATS write restriction — Fire 4 (production mTLS)** | Fires 1–3 closed the fabricated-KV-write surface at the account level; the remaining fire binds subject permissions to client certificates instead of NKeys, which only matters off the dev stack. | ★ now / ★★ prod | M | 🗄️ shelved (revive: production deployment) · [design](../../implementation-artifacts/nats-account-write-restriction-design.md) §Fire-3-status |
| **Keyed identity-index hashes (HMAC)** | Unkeyed `sha256NanoID` contact hashes are dictionary-testable with substrate access and persist in JetStream history post-shred; a Vault-keyed HMAC bounds it but needs a MAC primitive + key custody at every hash computer, and must migrate ALL index consumers (identityindex, provision probe, dedup) in one stroke. | ★ now / ★★ prod | M | 🗄️ shelved (revive: production threat model) · [analysis](../../implementation-artifacts/dedup-over-encrypted-pii-design.md) §9.1/§10-C |

### Privacy / Vault
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Vault] Sensitive aspects are identity-anchored, so retained records have no home** | Step 6 rejects a sensitive aspect on any non-identity parent, so two retained-class records sit plaintext in Core KV — clinic `.encounter` and lease-signing's income `.profile`. Custody belongs to a key holder carrying a retention policy; identity is the erase-on-request kind. | ★★★ | L–XL | ✅ ratified 2026-08-06 · [design](../../implementation-artifacts/retention-class-key-custody-design.md) · 2 fires |

### External-I/O maturity (bridge follow-ons)
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **Bridge — real vendor adapters** | The async result-return path ships; every adapter behind it is still a `Fake*`. Replacing them needs real vendor credentials + a production destination, so it waits on one. Augur's real adapter specifically: favor a NATS queue-group of model-runner processes over an embedded HTTP client — runners swap/scale independent of the bridge, no API key on it. | ★ now / ★★★ prod | M–L | 🗄️ shelved (revive: a real external destination) |

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
| **Dynamic type taxonomy — an abstract type a lens can label** | `subtypeOf` links between type meta vertices, resolved to a leaf-label set at activation, so a leaf declared by any package is picked up by lenses writing `:abstract*`. Recovers the polymorphism the label-binding fire removes; first consumer is `capabilityServiceAccess`, unnarrowable today. | ★★ | L | ✅ ratified 2026-08-06 · [design](../../implementation-artifacts/dynamic-type-taxonomy-design.md) · 2 fires · unblocked |
| **[Refractor] Cross-instance projection-latency rollup** | Aggregate per-lens projection latency across Refractor instances into one per-component view (single-instance today, so per-instance == per-component). Link-tombstone re-projection half **subsumed** by the link-aspect reprojection design. | ★ | S | 🚧 seq behind HA-NATS multi-instance · [link-aspect design](../../implementation-artifacts/link-aspect-triggered-reprojection-plain-lenses-design.md) subsumes the tombstone half; no multi-instance consumer yet |

### Refinements & ops
| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Refractor] The sweep-heal e2e polls the row, then asserts the counter** | `TestRefractor_ConvergenceSweep_DetectsAndHealsLostProjection_E2E` waits for the healed doc in KV then requires `Reconciled >= 1`; write and counter are separate steps, so under load the row lands first and the assert reads 0 — as it also would if CDC re-projection healed it, not the sweep. Tighten, never loosen. | ★★ | S | 📋 ready · owner: Whetstone · CI run 30182951177, green on re-run |
| **Package drive tests redden under full-suite load** | Four instances, all `packages/*`, each green alone: `TestSettle_ConsumerSelfScope_Allowed` (cafe-domain, 302s vs 21s), `TestRecordPII_OperatorRoleOnSecondPage` and `TestCompleteCredentialLink_GenericError_NoEnumeration` (identity-domain — the latter blows the 250ms Starlark wall budget during package INSTALL, i.e. setup, not an assert), `TestCreateLeaseApplication_DifferentApplicantsSameUnit_Allowed`. Not the handshake signature — root-cause, never loosen. | ★★ | S–M | 📋 ready · owner: Whetstone |
| **`TestSupervisor_AckWindowBoundToThePump` reds on a loaded runner** | CI run 31229578102 attempt 1: 27.6s (vs 2.6s local, 3/3) then `consumer did not drain to zero outstanding`. Untouched package; green on re-run, so attempt 1 is the only record. | ★★ | S | 📋 ready · owner: Whetstone · tighten, never loosen |
| **Embedded-NATS shard flakes under parallel load** | Two different embedded-NATS tests failed on CI runners on consecutive days (`TestLaneSpecs_PerLaneBacklogIsolation` unit-1; `TestPersonalLens_PL2_E2E_InterestSetFiltersThenAdmits` unit-2). Local repro: `go test ./...` with NO `-p` cap reddens 3 other embedded-NATS tests that pass 3x in isolation and under CI's `-p 4`. Root-cause per the flake rule: tighten, never loosen. | ★★ | M | 📋 ready · owner: Whetstone |
| **CI pipeline speed (continuous)** | Make CI faster without weakening any gate — owned continuously by the **Whetstone**. Matrix split done (serial → 4 parallel jobs); convergence + unit parallelized; unit itself now sharded across 2 runners. | ★★ | M (ongoing) | 🏗️ continuous (Whetstone) · aggregate-CPU ceiling confirmed 2x · next: propose paid larger runners to Andrew |
| **[Processor] A RevisionConflict on an UNDECLARED key names nothing** | NATS omits the failing subject, so `ConflictError.ConflictingKey` is always empty and `conflictKeyForSignal` rebuilds it only from *declared* defaulted/absent-create keys (`commit_path.go:520`) — a submitter who MISSES a `contextHint` declaration gets `conflictingKey:""` plus a raw `wrong last sequence`. Exactly the error the Contract #2 §2.5 sweep makes most likely. Found driving Café. | ★★ | M | 📋 ready |
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

- 2026-08-07 · `25ad55a9` · [privacy-base] erasure Fire B inc 8 — the identityErasure Loom pattern
- 2026-08-07 · `6ee839d9` · [privacy-base] erasure Fire B inc 7 — the identityErasureComplete weaverTarget
- *(older entries rolled to [archive/lattice-done.md](archive/lattice-done.md); newest rolled entry `010e383d`)*
