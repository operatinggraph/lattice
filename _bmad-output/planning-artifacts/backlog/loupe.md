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
| **F13 — Chronicler Time Machine** | Flow-history browser + map scrubber + ledger browser (platform-edges brief §4 L1–L3); overrides the Chronicler design's "rides F6" display note (Loupe scope). | ★★★ | L | 🚧 L1 reconciled (shipped Flows tab satisfies it, no rebuild) + L2 v1 SHIPPED (flow-liveness scrubber); L2-full/L3 blocked-on: Chronicler archive mode (lattice, unscheduled) · [UX §4](../../implementation-artifacts/loupe-platform-edges-ux.md) |
| **F21 — demo-operator cold re-mint survives a world reset** | Hosted Loupe died on every world rotation: a reset rescans ~13k events, so the re-minted operator's grant sat behind the backlog and the 4m poll expired, degrading `demo-up.sh` to no-Loupe. Root cause = the auth-plane class in [the reconciliation design](../../implementation-artifacts/capability-projection-reconciliation-design.md). | ★★★ | M | 🔁 recurred 2026-07-22 — deadline ~4x short; needs Fire 3 (async retry) — [design §10](../../implementation-artifacts/capability-projection-reconciliation-design.md) |
| **F22 — lens Contents panel handles `nats_subject` targets** | `lensRowsTarget` (cmd/loupe/lens.go:393) knows only `nats_kv` and `postgres`, so a valid `nats_subject` lens falls to `default` and renders as a red "unknown targetType" malformed-spec error — visible in the public demo. The platform accepts three types; a `nats_subject` target is a per-actor delta stream with no stored rows by design. Add a third rows-target kind, keeping `default` for real malformations. | ★★ | S | ✅ SHIPPED |

## New capability surfaces — 2026-07-18 PO survey

Gaps found wearing the PO hat: Lattice platform capabilities shipped since Loupe's last feature work
(~F15, 2026-07-07) that have **no operator surface** in the console today (all CLI-only). Pipeline: each
needs a Sally UX pass → Winston adjudicates (Andrew-delegated for this program) → Loupe Steward builds.

| Item | What it is | Imp | Size | State |
|---|---|---|---|---|

## Component maintenance

| Item | What it is | Imp | Size | State |
|---|---|---|---|---|

*(nothing open.)*

## Parked

| Item | Why it's parked | Imp | Size | State |
|---|---|---|---|---|
| Loupe agent-activity console | The ops layer atop the live system map (Steward queue, L3 review queue, per-agent Health). Read-seam options rejected. The L1 map keeps its `#sysmap-console` mount reserved. | ★★★ | M | 🚧 Andrew-gated (shelved 2026-06-25; design retained, do not build) |

## PO notes (rotation memory — capped, dated one-liners)

- 2026-07-07 — Follow-up (`6b1ab6e`): `56911ac` proved the mechanism but left the live default operator as root — actually re-scoped it (console-operator's own read-grant lens + persisted `loupe-operator.json`, `up-full` wires it automatically); verified live against real non-empty protected-table data.
- 2026-07-18 — Café + Wellness curated onto the door-band Apps group (all four verticals render together, client-shelf empty) — `3470f7d`, verified live (all four green). Same session: **PO survey** (Loupe untouched since F15/2026-07-07) filed F16–F19 + the `designAhead`-trio maintenance row; the old "flip Gateway designAhead" Next-line is now that row (Gateway up-full trigger shipped `11cc15f`).
- 2026-07-18 — F16 UX design drafted (Sally) + **adjudicated (Winston, Andrew-delegated)**, grounded live against the shipped read-models/op DDLs. Key finding: **both** loops are human-gated (Augur `augurDispatchPending` fires on `review.state="approved"`, not `pending`) — Augur is an action tab, not observe-only; its approve is *lighter* than capability's (server-side re-validation, no apply step). §8 forks resolved: approve=Loupe-in-process (Option A), apply=apply-in-Loupe (CLI fallback), reject=simple-confirm, Augur pending sorts by confidence. F16 → 📋 ready.
- 2026-07-18 — **F16.1 SHIPPED**: capability queue+detail+reject, verified live (routing/nav/error rendering; the shared stack has no `capability-author` package installed, so functional correctness rode an embedded-NATS Go test instead). **Next:** F16.3 (Augur tab, zero dep, first prod `ReviewProposal` use), then F16.2 (approve+apply, two contingent spikes). `designAhead`-trio maintenance row is 📋 ready.
- 2026-07-18 — **F16.3 SHIPPED**: Augur escalation tab — queue+detail+approve+reject, shares F16.1's card renderer, pending-by-confidence sort (§8.4), badge now sums both loops. Augur's approve re-validates entirely server-side (no client validation payload, no apply step) so both verdicts shipped in one fire — this is `ReviewProposal`'s first production submitter; verified live (routing/auth/error rendering — the shared stack has no `packages/augur` installed either, so the approve→dispatch write path rode the embedded-NATS Go test, same posture as F16.1). Lead self-review. **Next:** F16.2 (capability approve+apply, two contingent spikes).
- 2026-07-18 — **designAhead trio flip SHIPPED** (`569f06af`): Winston-adjudicated posture — Gateway/Vault/Chronicler are optional (up-full only), not design-ahead; everLive-gated down-state (never-seen→offline keeps kernel-only green; crash→absent-red). `mapEdge.DesignAhead` (app→gateway route) untouched. F11-revoke live-click NOT exercised (destructive submit, declined unattended per the risky-action guardrail); the revoke surface is unaffected + Gateway confirmed heartbeating.
- 2026-07-18 — **F17 UX drafted + adjudicated inline (Winston, Andrew-delegated) → F17.1 SHIPPED → F17 CLOSED**: the task inbox was blind to the FR28/FR29 queue plane. `computeTasks` now surfaces `queuedFor` (role-queue pull assignment), a derived `assignment` kind, `available` (the assignee's `.availability` routing gate, absent==available; nil for a role queue), and `stuck` (open + role-queued + past expiry — the Loupe-local mirror of the `unroutedTasks` target's `missing_claim` gap; `now` injected for determinism, stuck sorts first). FE: assignment badge, availability chip, red `stuck·unrouted` badge + top-sort + "stuck/unrouted only" filter. Chose to NOT duplicate the Weaver `UnroutedTasks` Health-KV issue into `/api/tasks` (it renders authoritatively on the Weaver component page; the per-row flag is the drill-down — UX §4). Verified live: `/api/tasks` returns the new fields backward-compatibly; assigned+available cards render + the filter's empty state works (no live role-queued/stuck data on the stack → those branches rode Go unit tests). A follow-up committed the card-meta wrap so the chip+expiry don't overflow the card. Lead self-review. **Next:** F18/F19/F20 still need a Sally UX pass (Winston adjudicates in-fire when a fire reaches them).
- 2026-07-18 — **F16.2 SHIPPED → F16 CLOSED**: capability approve+apply (`#/review/capability/<id>/{approve,apply}`). Both spikes landed in-Loupe, no cross-lane ask: approve re-validates the artifact server-side against the live catalog (Option A — the CLI's three `ValidateCapabilityArtifact` deps all constructible in `cmd/loupe`; a fresh-invalid verdict blocks client-side, no op sent), apply drives the two-commit F-004 install (`CapabilityApplyPlanForProposal`→`Installer.Apply`→`MarkCapabilityProposalApplied`) reusing `pkg.go`'s Installer wiring. FE: approve button live, approved-state "Apply now". Known tail: a partial failure (install committed, mark op failed) isn't retryable via the button for a newPackage — recovery is the CLI mark step (error names it). Verified headless (routing/auth/method-gating/handler-reach; rebuilt asset served) + embedded-NATS Go tests for the guards + `freshCapabilityVerdict` (shared stack has no `capability-author` installed, same posture as F16.1/F16.3). Lead self-review.

- 2026-07-19 — **F18 UX drafted + adjudicated inline (Winston) → F18.1 SHIPPED → F18 CLOSED**: view-only fire, the diagnostics were already on the heartbeat ([forks + honesty rule in the UX doc](../../implementation-artifacts/loupe-f18-planner-diagnostics-ux.md)). Live-verified on the real degraded Weaver; `plannerShadow` absent ⇒ section hides, never a fake 0%. Noted NOT filed: the Weaver Control column's "lacks the control grant" is stack state (console-operator package not installed here), not a gap. **Next:** F19, F20 need a UX/design pass.

- 2026-07-19 — **F19 UX drafted + adjudicated inline (Winston) → F19.1 SHIPPED → F19 CLOSED**: zero cross-lane ask. Two reusable findings: `personal.syncgap` is unusable as an operator source (identity-bound + bare bool by design — derive gap from JetStream instead), and `revisionCursor` is NOT a SYNC sequence (it is the pipeline's `LastAppliedSeq`) — details + the deliberate divergence from the platform's gap predicate in the [UX doc](../../implementation-artifacts/loupe-f19-edge-fleet-ux.md) §4. **Next:** F20 needs a design pass, gated on the demo's public-launch phase.

- 2026-07-19 — **F20 UX drafted + adjudicated inline (Winston) → F20.1 SHIPPED**: Loupe-side half only; exposure stays Andrew-gated. Two reusable findings in the [design](../../implementation-artifacts/loupe-f20-demo-operator-ux.md) §2.2/§2.3: a read-only posture needs a **reveal axis** separate from the write axis (a decrypt is a GET, and its vault RPC carries no actor), and Loupe's "loopback ⇒ safe" checks read the **bind host, not the peer** — behind a proxy login would 403 (F20.5, blocks exposure). **Next:** F20.5, then F20.2.

- 2026-07-19 — **Designer pass (Winston): F20.5 + F20.2 flipped from problem statements to build designs** — [§6/§7 + second-pass adjudication §4.1](../../implementation-artifacts/loupe-f20-demo-operator-ux.md). Exposure-checklist #4/#5 resolved in-lane (limiter is Loupe-side — stock Caddy ships no rate-limit handler, Caddy now a `docs/vendors.md` row; SSE cap posture-derived).

- 2026-07-19 — **F20.5 SHIPPED**: proxied login unblocked (checklist #2). 3-layer review found four real defects (limiter amplification, XFF trust, int32 SSE truncation, parse/match asymmetry) — all fixed forward; departures recorded in [design §6.8](../../implementation-artifacts/loupe-f20-demo-operator-ux.md). New checklist item: single-hop only, no CDN. **Next:** F20.2; F20.3 is the remaining cross-lane exposure dep.

- 2026-07-19 — **F20.2 SHIPPED → F20's Loupe half CLOSED** (F20.3 + Andrew's go-ahead are all that remain). Two reusable findings in [design §7.5](../../implementation-artifacts/loupe-f20-demo-operator-ux.md): a demo honesty surface must not promise the *platform's* grants while F20.3 is unshipped (and never for reveals — no actor on the vault RPC); and a suppression path deciding at render time needs the posture awaited before routing, since only the CSS-class path self-heals.

## Done log — loupe (newest first)

One line per shipped item (`date · SHA · [tag] title`). Oldest roll to `archive/` past ~25.

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
- 2026-07-18 · `3470f7d` · [Loupe/maint] System Map cleanup — Café + Wellness curated onto the door-band Apps group (all four verticals together; client-shelf empty). Verified live (all four green), lead self-review, CI green
- 2026-07-07 · `6b1ab6e` · [Loupe/F15] Actually re-scoped the standing operator to consoleOperator (56911ac only proved the mechanism); console-operator's own read-grant lens + persisted identity. Verified live vs. real data, CI green
- 2026-07-07 · `56911ac` · [Loupe/F15 inc.3] Items 5-6 CLOSED — pkg-lifecycle root-admin gate + live e2e (consoleOperator allow/deny); Postgres F9 seam wired to M5's wildcard-grant posture. Verified live + unit test, CI green
- 2026-07-07 · `635db70` · [Loupe/F15 inc.2] Op-submissions relay through the Gateway, replacing `adminActor` direct-stamp. 3-layer reviewed, fixed forward; verified live + CI green
- 2026-07-06 · `af43dab` · [Loupe/F15 inc.1] Browser-usable login session — cookie + `/login` page + unauth-nav redirect; pins gate to the configured operator. 3-layer reviewed, fixed forward; verified live + CI green
- 2026-07-06 · `19c1dd0` · [Loupe/F15 inc.1] Operator login gate — requireOperator wraps the whole mux; 3-layer reviewed, fixed forward; verified live + CI green

- 2026-07-06 · `c5e1c80` · [Loupe/F13] L1 reconciled + L2 v1 map scrubber (flow-liveness replay); 3-layer review fixed forward; verified live + CI green
- 2026-07-06 · `f7c7e36` · [Loupe/maint] Ad-hoc (Andrew) — human-scale `freshness` "ago" past a minute (`32914s ago` → `9h ago`); single-point fix; verified live + CI green
- 2026-07-06 · `78ca047` · [Loupe/F12 inc.3] Crypto-shred proof view — `#/graph/<identity>?view=shred`, typed-confirm `ShredIdentityKey` via `/api/op`; F12 CLOSED; 3-layer review fixed forward; verified live + CI green
- 2026-07-06 · `fa78cde` · [Loupe/F12 inc.2] Reveal — audited decrypt in the Graph explorer (`POST /api/vault/decrypt`, sealed/revealed aspect rows); 3-layer review fixed forward; verified live + CI green
- 2026-07-06 · `8742f49` · [Loupe/F12 inc.1] Vault component page — metrics line + `GET /api/vault/shreds` read-only shred-status fleet view (in-flight identities linked into the Graph explorer); verified live, lead self-review
- 2026-07-04 · `cc0df14` · [Loupe/F14] Map scale — package-grouped lens cluster cards (exception-first density, filter) + verticals as curated door-band `app` nodes (offline≠red); verified live, lead self-review
- 2026-07-04 · `1b19838` · [Loupe/F11] Gateway security console — auth-failure headline + JWKS panel (empty until the heartbeat `jwks` block) + typed-confirm revoke surface over the op model; 3-layer review fixed forward
- 2026-07-03 · `1c77a6c` · [Loupe/F10] Curated topology — Gateway/Vault/Chronicler on the map (design-ahead state, ingress band, lateral Vault, object-store plane); verify + 3-layer review fixes through `6e6d0f4`
- 2026-07-03 · `d5617db` · [Loupe/F9] Postgres read seam — `LOUPE_PG_DSN` connector + `/api/lens/<id>/rows` pg path; also ships the console-wide same-origin gate (rebinding-hardened)
- 2026-07-03 · `f8b09c6` · [Loupe/F7] Submit-Op follow-through — structured accepted panel + ~12s pulse follow-through + session op log + `#/op?type=` prefill; Files/vertex attach polish
- 2026-07-03 · `73a3146` · [Loupe/F8] Packages first-class — `#/package/<key>` graph-resolved contents + install/upgrade/uninstall wrapping pkgmgr (dry-run delta as the confirm, typed uninstall, same-origin gate); keyTarget owns package vertices
- 2026-07-03 · `0821a36` · [Loupe/F6] Live pulse — SSE tail of core-events + map rail feed w/ poll-diff derived rows + edge pulse animation + topbar LED; §8.2 activeSequence premise corrected
- 2026-07-02 · `23a994e` · [Loupe] Phase-gates panel removed from the System Map — gate chips retired ahead of the security-proof-watchdog component (lattice); server computeGates left dormant
- 2026-07-02 · `7f724c5` · [Loupe/F5] Lens page — `#/lens/<id>` four panels + `/api/lens` detail/rows (pg-pending state); typed-confirm delete; map/roster/graph lens links re-pointed
- 2026-07-02 · `24768e8` · [Loupe/F4] Health absorption + status vocabulary — renderedState + pending-readpath rollup exclusion, shell pill+alert strip, map rail gates panel; Health tab retired
- 2026-07-02 · `5865e0e` · [Loupe/F3] Component pages + Control dissolution — `#/component/<id>` plural instances + row-level control + lens roster; Control tab retired
- 2026-07-02 · `976a18f` · [Loupe/F2] Graph explorer — faceted/paged list + linkifying renderer + ego-graph hood mode; Core KV tab retired
- 2026-07-02 · `e6a8a46` · [Loupe/F1] Console shell — hash router + ES-module split + goja logic tier (also closes: static-UI serving test, operator-UI coverage Fire 1)
- 2026-07-02 · `4b8743f` · [Loupe/deploy] Control planes restored for operator surfaces — `lattice.ctrl.>` grant (write-restriction lockout) + natsperm positive round-trip pin
