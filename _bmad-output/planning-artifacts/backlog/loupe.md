# Backlog — Loupe (Stream 3): the operator console

Stream 3 = the Loupe console (`cmd/loupe`: Go handlers + `web/` UI). Pipeline: **PO review** files the
program → **Sally** (bmad-agent-ux-designer) produces the UX design → **Winston adjudicates**
(Andrew delegated design ratification for this program, 2026-07-02 — no 📐-awaiting-Andrew gate here) →
the **Loupe Steward** builds fires UX-then-FE. Index + cross-lane rules: [../backlog.md](../backlog.md);
row discipline: [lattice.md → "How this board works"](lattice.md) (lint-board covers this file).

**Lane boundaries.** Code scope is `cmd/loupe/**` (+ its tests). A needed platform primitive
(engine/op/substrate) or deploy/contract change routes per the cross-lane rules — file to
[lattice.md](lattice.md) and `🚧 blocked-on:` it (trivial established-pattern mirrors excepted).
**Concurrency:** this lane runs in PARALLEL with both other streams (Andrew, 2026-07-02) — it does NOT
take the shared build lock; Loupe fires serialize among themselves on `/tmp/lattice-loupe-build.lock`.

Open items only — shipped demand is in the Done log. 

## Loupe 2.0 — "the map is the console" (the program)

PO review 2026-07-01 (Andrew session); UX design **adjudicated 2026-07-02** (Winston, Andrew-delegated):
[loupe-2-ux-design.md](../../implementation-artifacts/loupe-2-ux-design.md) — build fires per its §14;
one FE fire at a time; each fire retires a tab only in the same fire as its replacement.
**Extended 2026-07-02** with the platform-edges fires F10–F13 (Gateway/Vault/Chronicler onto the curated map +
the Chronicler Time Machine) — brief:
[loupe-platform-edges.md](../../implementation-artifacts/loupe-platform-edges.md); UX **adjudicated 2026-07-02**
(Winston): [loupe-platform-edges-ux.md](../../implementation-artifacts/loupe-platform-edges-ux.md) — F10
buildable-first; F11–F13 gated on lattice cross-lane asks (§6 there).

| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **F13 — Chronicler Time Machine (L2-full + L3)** | L1 is satisfied by the shipped Flows tab and L2 v1 by the flow-liveness scrubber; scrubbing past the live window and browsing the ledger both need history Loupe cannot read yet. | ★★★ | L | 🚧 blocked-on: Chronicler archive mode (lattice, Andrew-deferred) · [UX §4](../../implementation-artifacts/loupe-platform-edges-ux.md) |
| **Weaver Studio NL assist — Describe + Load-into-Author (NL-2)** | The Author stage gains a Describe panel (plain-language intent → `RequestCapabilityAuthoring` relay under the operator's token) and the review console a "Load into Author" hydration for weaverTarget proposals — the AI-draft→human-edit loop the Studio lacks. | ★★★ | M | 🚧 blocked-on: NL-1 real capability-author adapter (lattice, 📐 awaiting-Andrew) · [design](../../implementation-artifacts/natural-language-weaver-targets-design.md) §3.4 |

## Component maintenance

| Item | What it is | Imp | Size | State |
|---|---|---|---|---|

## Parked

| Item | Why it's parked | Imp | Size | State |
|---|---|---|---|---|
| Loupe agent-activity console | The ops layer atop the live system map (Steward queue, L3 review queue, per-agent Health). Read-seam options rejected. The L1 map keeps its `#sysmap-console` mount reserved. | ★★★ | M | 🚧 Andrew-gated (shelved 2026-06-25; design retained, do not build) |

## PO notes (rotation memory — capped, dated one-liners)

- 2026-07-18 — **F17 UX drafted + adjudicated inline (Winston, Andrew-delegated) → F17.1 SHIPPED → F17 CLOSED**: the task inbox was blind to the FR28/FR29 queue plane. `computeTasks` now surfaces `queuedFor` (role-queue pull assignment), a derived `assignment` kind, `available` (the assignee's `.availability` routing gate, absent==available; nil for a role queue), and `stuck` (open + role-queued + past expiry — the Loupe-local mirror of the `unroutedTasks` target's `missing_claim` gap; `now` injected for determinism, stuck sorts first). FE: assignment badge, availability chip, red `stuck·unrouted` badge + top-sort + "stuck/unrouted only" filter. Chose to NOT duplicate the Weaver `UnroutedTasks` Health-KV issue into `/api/tasks` (it renders authoritatively on the Weaver component page; the per-row flag is the drill-down — UX §4). Verified live: `/api/tasks` returns the new fields backward-compatibly; assigned+available cards render + the filter's empty state works (no live role-queued/stuck data on the stack → those branches rode Go unit tests). A follow-up committed the card-meta wrap so the chip+expiry don't overflow the card. Lead self-review. **Next:** F18/F19/F20 still need a Sally UX pass (Winston adjudicates in-fire when a fire reaches them).
- 2026-07-18 — **F16.2 SHIPPED → F16 CLOSED**: capability approve+apply (`#/review/capability/<id>/{approve,apply}`). Both spikes landed in-Loupe, no cross-lane ask: approve re-validates the artifact server-side against the live catalog (Option A — the CLI's three `ValidateCapabilityArtifact` deps all constructible in `cmd/loupe`; a fresh-invalid verdict blocks client-side, no op sent), apply drives the two-commit F-004 install (`CapabilityApplyPlanForProposal`→`Installer.Apply`→`MarkCapabilityProposalApplied`) reusing `pkg.go`'s Installer wiring. FE: approve button live, approved-state "Apply now". Known tail: a partial failure (install committed, mark op failed) isn't retryable via the button for a newPackage — recovery is the CLI mark step (error names it). Verified headless (routing/auth/method-gating/handler-reach; rebuilt asset served) + embedded-NATS Go tests for the guards + `freshCapabilityVerdict` (shared stack has no `capability-author` installed, same posture as F16.1/F16.3). Lead self-review.

- 2026-07-19 — **F18 UX drafted + adjudicated inline (Winston) → F18.1 SHIPPED → F18 CLOSED**: view-only fire, the diagnostics were already on the heartbeat ([forks + honesty rule in the UX doc](../../implementation-artifacts/loupe-f18-planner-diagnostics-ux.md)). Live-verified on the real degraded Weaver; `plannerShadow` absent ⇒ section hides, never a fake 0%. Noted NOT filed: the Weaver Control column's "lacks the control grant" is stack state (console-operator package not installed here), not a gap. **Next:** F19, F20 need a UX/design pass.

- 2026-07-19 — **F19 UX drafted + adjudicated inline (Winston) → F19.1 SHIPPED → F19 CLOSED**: zero cross-lane ask. Two reusable findings: `personal.syncgap` is unusable as an operator source (identity-bound + bare bool by design — derive gap from JetStream instead), and `revisionCursor` is NOT a SYNC sequence (it is the pipeline's `LastAppliedSeq`) — details + the deliberate divergence from the platform's gap predicate in the [UX doc](../../implementation-artifacts/loupe-f19-edge-fleet-ux.md) §4. **Next:** F20 needs a design pass, gated on the demo's public-launch phase.

- 2026-07-19 — **F20 UX drafted + adjudicated inline (Winston) → F20.1 SHIPPED**: Loupe-side half only; exposure stays Andrew-gated. Two reusable findings in the [design](../../implementation-artifacts/loupe-f20-demo-operator-ux.md) §2.2/§2.3: a read-only posture needs a **reveal axis** separate from the write axis (a decrypt is a GET, and its vault RPC carries no actor), and Loupe's "loopback ⇒ safe" checks read the **bind host, not the peer** — behind a proxy login would 403 (F20.5, blocks exposure). **Next:** F20.5, then F20.2.

- 2026-07-19 — **Designer pass (Winston): F20.5 + F20.2 flipped from problem statements to build designs** — [§6/§7 + second-pass adjudication §4.1](../../implementation-artifacts/loupe-f20-demo-operator-ux.md). Exposure-checklist #4/#5 resolved in-lane (limiter is Loupe-side — stock Caddy ships no rate-limit handler, Caddy now a `docs/vendors.md` row; SSE cap posture-derived).

- 2026-07-19 — **F20.5 SHIPPED**: proxied login unblocked (checklist #2). 3-layer review found four real defects (limiter amplification, XFF trust, int32 SSE truncation, parse/match asymmetry) — all fixed forward; departures recorded in [design §6.8](../../implementation-artifacts/loupe-f20-demo-operator-ux.md). New checklist item: single-hop only, no CDN. **Next:** F20.2; F20.3 is the remaining cross-lane exposure dep.

- 2026-07-25 — **F24.1 + F24.2 SHIPPED as one fire** (they were the same surface: the roster's Interest Set expander is what the device panel became). Two reusable findings in the [design](../../implementation-artifacts/loupe-flows-edge-depth-ux.md) §3.1: JetStream's `ack_floor.last_active` is an ACTIVITY clock (`o.lat = time.Now()` on each ack, nats-server 2.14) held in process-local consumer state that a restart zeroes — so it is the only liveness-adjacent signal this roster can carry, and its ABSENCE never means "never acked"; and there is no exact per-device time-to-gap without reading a SYNC payload into the console, so the time headroom is an interpolation marked "~" while the floor's own clock (firstTime + MaxAge) is exact. 3-layer review caught a headroom of 0 on an EMPTY stream rendering every caught-up device red — the same fleet-wide false red the gap predicate exists to avoid, one field over. **Next:** the lane is drained again; the two act-on-it rows stay blocked on their lattice seams.

- 2026-07-25 — **Lane drained of ready work** (F1–F22 closed; F13's tail blocked on Andrew-deferred Chronicler archive mode; agent-console Andrew-gated; the cross-origin row was found already discharged by `1a0d1849`). Two reusable findings from the recovery fire: a two-commit flow's recovery check must match the package **version**, not just the name (an `upgradeExisting` target is installed by definition *before* the apply); and `submitOpViaGateway` returns `(reply, nil)` on a Processor **rejection**, so any call site branching on `err` alone reports a refusal as success — Loupe had two. **Next:** a PO/UX survey pass to refill the lane.

- 2026-07-19 — **F20.2 SHIPPED → F20's Loupe half CLOSED** (F20.3 + Andrew's go-ahead are all that remain). Two reusable findings in [design §7.5](../../implementation-artifacts/loupe-f20-demo-operator-ux.md): a demo honesty surface must not promise the *platform's* grants while F20.3 is unshipped (and never for reveals — no actor on the vault RPC); and a suppression path deciding at render time needs the posture awaited before routing, since only the CSS-class path self-heals.

- 2026-08-02 — **F25.3b SHIPPED → F25 CLOSED** (`28dd2c55`): propose (`POST /api/weaver/author/propose`, mints one NanoID per artifact, relays `SubmitCapabilityProposal`) + a review-queue `source` badge (operator/ai) + the Trial panel, a guided wrapper around primitives that already existed (control-relay revoke/enable, the op console, F25.1's own target map) — zero new platform surface beyond the one op. Live-verified against the real dev stack end to end (login → draft → check → propose → real Processor reply → revoke). One reusable finding, not specific to this fire: **every `packages/capability-author` op currently rejects "no matching platformPermission" on this stack** — `cap.role-by-operation.{SubmitCapabilityProposal,ReviewCapabilityProposal}` are both empty in Core KV, so the package's `Permissions()` grants were never provisioned here (a pre-existing environment gap, not a regression — confirmed identical on `ReviewCapabilityProposal`, which F16.3 also could only relay-verify, never commit-verify, live). Fixing it is a stack-provisioning action, not a Loupe-lane code change. Go handler tests cover propose's per-artifact independence; goja tests cover the source badge. Lead self-review (M-size, no frozen contract, reuses the existing generic op/control relays).

## Done log — loupe (newest first)

One line per shipped item (`date · SHA · [tag] title`). Oldest roll to `archive/` past ~25.

- 2026-08-21 · `3c185e9b` · [Loupe/maint] `retentionKeyStatus` operator surface — `/api/vault/retention-keys` + Vault page Retention classes panel. Tests, live-verified, CI green
- 2026-08-21 · `6d34b1d3` · [Loupe/maint] `protectedFlag` — a malformed (non-bool) `targetConfig.protected` now reads as protected, not silently unprotected. Tests, CI green
- 2026-08-21 · `fb6a637d` · [Loupe/maint] Shred button now starts the identityErasure pattern, not a bare key shred; panel gains a five-step erasure progress list. Tests, live-verified, CI green
- 2026-08-02 · `28dd2c55` · [Loupe/F25.3b] Weaver Target Studio — Propose + Trial: `SubmitCapabilityProposal` submission, review-queue source badge, born-disabled Trial panel; F25 CLOSED. Lead self-review, live-verified end to end
- 2026-08-02 · `c1bdcfae` · [Loupe/F25.3a] Weaver Target Studio — Author: draft+check+export at `#/weaver/author`. Lead self-review, live-verified, fixed forward two defects found live
- 2026-08-02 · `e9408470` · [Loupe/F25.2] Weaver Target Studio — Verify: Checks panel + `#/weaver/verify` (V1/V2/V3). Lead self-review, live-verified on the real dev stack

- 2026-08-02 · `d0b879d8` · [Loupe/F25.1] Weaver Target Studio — Observe: roster + definition map + entity drill (`#/weaver`) joining playbook · rows · marks · control · heartbeat. Lead self-review, live-verified
- 2026-08-01 · `f73d8928` · [Loupe/Lens] Contents panel scopes to the lens's own key prefix (shared buckets stop showing sibling rows) and badges guarded soft-tombstones as retracted
- 2026-07-30 · `7984e32c` · [Loupe/Flows] Retry button wired to Loom's redrive — the second (and last) of the two 2026-07-25 act-on-it rows; `#/flows` is no longer read-only
- 2026-07-30 · `2edba1f3` · [Loupe/Edge] gapped device panel gets its one write action — "Request hydration on next attach", the lattice cross-lane primitive's console consumer. Lead self-review
- 2026-07-25 · `6ac1523e` · [Loupe/F24.1+F24.2] Edge fleet triage — worst-first order, retention headroom in messages + time, in-place Interest Set, `#/edge/<key>` device panel. 3-layer review fixed forward, live-verified
- 2026-07-25 · `1551f31b` · [Loupe/F23.1] Flow detail — `#/flows/<id>` step sequence off the pinned pattern + the instance cursor; history/engine/pattern kept separable, disagreement stated. Lead self-review, live-verified
- 2026-07-25 · `f5eb461c` · [Loupe/F23.0+F23.2] Flows badge reads Loom's status not its memory — terminal-aware liveness + `stale-history`; pattern names resolved, grouped, exception-first. Lead self-review, live-verified
- 2026-07-25 · `44aa4a22` · [Loupe/maint] Capability apply recovers from a half-commit in-console — payload-free `mark-applied`, version-verified; rejected op replies no longer read as success. 3-layer review, CI green
- 2026-07-25 · `1a0d1849` · [Loupe/maint] Cross-origin fork retired — `appsession.OriginGate` extracted off `*Manager`; Loupe's copy (parser + matcher + loopback test) deleted, kit tests cover it
- 2026-07-25 · `256229a4` · [Loupe/maint] Cross-origin gate is one choke point (`requireOperator`) + the Fetch-Metadata branch; nested-navigation framing refused. 3-layer review fixed forward, live-verified, CI green
- 2026-07-22 · `9359fce2` · [Loupe/F21] F21 CLOSED — Fire 3: retry timer self-heals demo Loupe after a slow reset drain, no more human-noticed 502s. 3-layer review fixed forward, live-verified, CI green
- 2026-07-22 · `d48541a3` · [Loupe/F22] F22 CLOSED — Contents panel handles `nats_subject` targets honestly, points Personal targets to Edge Fleet. Lead self-review, live-verified, CI green
- 2026-07-22 · `0690381e` · [Loupe/F20.4] F20 CLOSED — hosted read-only Loupe exposed on its own subdomain; per-reset operator provisioning; exposure checklist #1–#7 discharged live (Andrew's go)
- 2026-07-19 · `c645c772` · [Loupe/F20.2] Demo polish — inspect-only control reads (omission-denies classification), write-affordance suppression, `/login` disclaimer. 3-layer review fixed forward, live-verified, CI green
- 2026-07-19 · `ca941e58` · [Loupe/F20.5] Public-origin posture — `LOUPE_PUBLIC_ORIGIN` (origin gate + Secure cookie), dev-auth⇒demo boot coupling, credential-exchange limiter, SSE cap knob. 3-layer review fixed forward, live-verified, CI green
- 2026-07-19 · `018dd913` · [Loupe/F20.1] Hosted-demo read-only posture — `LOUPE_DEMO_MODE` (default off): method default-deny, boot guard, reveal denial, visitor banner. 3-layer review fixed forward, live-verified, CI green
- 2026-07-19 · `14a1b490` · [Loupe/F19] Edge fleet — Personal Lens subscriber roster + per-device sync-gap triage (`#/edge`). 3-layer review fixed forward, live-verified on a real 7-device fleet, CI green
- 2026-07-19 · `a9fa69ae` · [Loupe/F18] Weaver planner diagnostics — exception-first Planner panel (oscillation · mismatch · contraction · admission · shadow); view-only, no server change. Goja coverage, live-verified, CI green
- 2026-07-18 · `5b623837` · [Loupe/F17] Queue-plane-aware task inbox — `queuedFor` + assignment kind + assignee availability + FR29 stuck/unrouted flag (top-sort + filter); UX drafted+adjudicated inline. Go unit coverage, live-verified, CI green
- 2026-07-18 · `569f06af` · [Loupe/maint] designAhead trio flip — Gateway/Vault/Chronicler `designAhead`→`optional`; down-state "offline", everLive crash→absent-red preserved. Tests, live-verified, CI green
- 2026-07-18 · `0f292d43` · [Loupe/F16.2] Capability approve+apply — server-side re-validation (Option A) + two-commit F-004 install, closing F16. Embedded-NATS tests; headless-verified. Lead self-review, CI green
- 2026-07-18 · `d010fe60` · [Loupe/F16.3] AI review console — Augur escalation tab, queue + detail + approve + reject (`#/review/augur`), shares F16.1's card renderer. Goja + embedded-NATS test coverage; live-verified. Lead self-review, CI green
- 2026-07-18 · `d37e86b` · [Loupe/F16.1] AI review console — capability queue + detail + reject (`#/review`). Goja + embedded-NATS test coverage; live-verified. Lead self-review, CI green
Older entries (F1–F15, F13, F12 inc.3, deploy) rolled to [`archive/loupe-done.md`](archive/loupe-done.md).
