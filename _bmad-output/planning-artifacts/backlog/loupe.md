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

## New capability surfaces — 2026-07-18 PO survey

Gaps found wearing the PO hat: Lattice platform capabilities shipped since Loupe's last feature work
(~F15, 2026-07-07) that have **no operator surface** in the console today (all CLI-only). Pipeline: each
needs a Sally UX pass → Winston adjudicates (Andrew-delegated for this program) → Loupe Steward builds.

| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **F16 — AI review console** | Operator control point for the AI-native loop, zero surface today (CLI-only): (a) capability-proposal queue — DDL/lens diff, approve→apply; (b) Augur L3 escalation queue — proposed ops, approve/reject→auto-dispatch. Both human-gated; one console, two tabs, one shared card. Fires F16.1 (see+reject) → F16.3 (Augur) → F16.2 (cap approve+apply). | ★★★ | M | ✅ CLOSED — F16.1+F16.3+F16.2 shipped; full human-in-the-loop surface (both loops) live · [UX design](../../implementation-artifacts/loupe-f16-ai-review-console-ux.md) |
| **F17 — Orchestration queue observability** | Loupe's task inbox (`tasks.go`) shows only status/assignee/scopedTo/operation — blind to the FR28 queue plane shipped since: `queuedFor` + `ClaimTask`, `unroutedTasks` (Weaver-surfaced via Health-KV), availability-gated routing + `SetAvailability`. Add queued/unrouted/available columns + a "stuck/unrouted work" view. High value (live orchestration visibility), small. | ★★★ | S–M | 🚧 needs UX design (Sally, light) |
| **F18 — Weaver planner-mandate diagnostics** | Planner mandate shipped end-to-end (consumed by LoftSpace renewals) with rich heartbeat-surfaced diagnostics — contraction monitor + oscillation detector, shadow-mode agree/diverge metrics, admission-control token-bucket pacing, `LensEffectMismatch`/effect-confidence — but only the generic component-page health rollup shows it. Add a planner solver/remediation view (oscillation, divergence, admission state). | ★★ | M | 🚧 needs UX design (Sally) |
| **F19 — Edge / Personal-Lens fleet** | A whole plane is invisible: Personal Lens (PL.1–6) + Edge Lattice (EDGE.1–5) — per-identity subscribe ACL, `personal.{hydrate,register,deregister,syncgap}` control RPCs, Interest Sets, native WS listener. Add a connected-edge-nodes / personal-lens-subscribers / sync-gap-health view. Sovereign per-user ⇒ medium operator value, but the console can't see the edge fleet at all. | ★★ | M–L | 🚧 needs UX design (Sally) |

## Component maintenance

| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **designAhead trio flip** | `systemmap.go` still hardcodes gateway/vault/chronicler as `designAhead:true`, but all three now heartbeat live (Gateway in `up-full`; Vault `health.vault.*`; Chronicler `health.chronicler.*`) — the flag is moot when live and only mis-renders the DOWN state as "design-ahead" not a real absent/offline signal. Decide the down-state posture (core-expected red vs vertical-style offline; kernel-only `make up` skips them) and flip. Also verify the F11 revoke loop live now Gateway runs in up-full. | ★★ | XS–S | 📋 ready |

## Parked

| Item | Why it's parked | Imp | Size | State |
|---|---|---|---|---|
| Loupe agent-activity console | The ops layer atop the live system map (Steward queue, L3 review queue, per-agent Health). Read-seam options rejected. The L1 map keeps its `#sysmap-console` mount reserved. | ★★★ | M | 🚧 Andrew-gated (shelved 2026-06-25; design retained, do not build) |

## PO notes (rotation memory — capped, dated one-liners)

- 2026-07-01 PO review (Andrew session) — filed the program; found+fixed the control-plane lockout.
- 2026-07-02 UX design adjudicated (2 premises corrected against live stack — see design §15).
- 2026-07-02 PO review (Andrew session) — **extended 2.0** with platform-edges fires F10–F13 (Gateway/Vault/Chronicler onto the curated map + the Time Machine); map stays curated, agent-console stays shelved, design-ahead all three.
- 2026-07-02 — F10–F13 UX **adjudicated** (Winston): [platform-edges-ux](../../implementation-artifacts/loupe-platform-edges-ux.md); Andrew grants `ShredIdentityKey`+`RevokeActor`, map shows design-ahead, revoke = op→event→Gateway-internal-KV (refined lattice revocation row → Designer). Cross-lane asks filed to lattice (Gateway up-full+jwks, Vault→Loupe enablers).
- 2026-07-02 — removed the phase-gates chips from the map (Andrew): the security proofs (bypass g2 / capability g3) become a new Lattice component (human-named, periodic + "check now", isolated runner) — [security-proof-watchdog](../../implementation-artifacts/security-proof-watchdog-brief.md), filed Designer on lattice.
- 2026-07-03 — **Loupe 2.0 core COMPLETE** (F1–F9 all shipped). F9's full value (protected-table rows) needs the read role — filed to lattice ("[Refractor/deploy] Loupe read-only PG role").
- 2026-07-04 — F11 built against the shipped op model (revocation kill-switch Fires 1+2, lattice); review found the materializer poison-pill (invalid actor key → forever-redelivery) — filed to lattice.md, fixed same-day (`37b54b2`).
- 2026-07-03 — PO+Sally session (Andrew, screenshot-driven): filed **F14** — lens shelf crowding at ~24 lenses (label spam, truncation, hidden below-fold chips) + the verticals' map home. Andrew corrected the first ruling: gateway design F5 routes the verticals' USER writes through the Gateway in end-state (§3.4 bypass = service actors only) — door band shows solid direct (today) + dashed via-Gateway (end-state); UX amended + adjudicated same session (delegated).
- 2026-07-05 — Vault CLOSED + Chronicler F1–F3 shipped (lattice, both same-day): F12 is ready-to-build (UX+FE only, no lattice blocker left); F13's L1 overlaps the Flows tab Chronicler's own Fire 3 shipped — reconcile before extending; L2-full/L3 stay blocked on the unscheduled Chronicler archive-mode fire.
- 2026-07-06 — F12 increment 1 shipped (component page + shred fleet view); verified live against a real shredded identity already on the stack. All §3.1 ⚠️ ASSUMES resolved: `health.vault.*` heartbeats live, `lattice.vault.decrypt` already granted to Loupe's nkey, `ShredIdentityKey` already grant-packaged to the operator role (`packages/privacy-operator-grant`) — no lattice-lane blocker for the remaining increments.
- 2026-07-06 — F12 increment 2 shipped (Reveal — audited decrypt in the Graph explorer); 3-layer review fixed forward (identity-anchor validation, complete-envelope check, malformed-reply guard, stale-DOM guard); verified live against a real shredded identity's sealed row (never against live plaintext PII — the auto-mode PII-handling gate correctly declined that, and it isn't needed: the Go round-trip test already proves decrypt-to-plaintext through the real vault RPC). Noted, not filed as a blocker: `internal/vault/service.go`'s decrypt RPC responder logs only failed decrypts today, not successful ones — the design's "this reveal is audited" claim is a property of that responder, not of Loupe's proxy; a Lattice-lane follow-up to add success-path audit logging is fair game whenever that lane picks it up.
- 2026-07-06 — **F12 CLOSED**: increment 3 shipped (the crypto-shred proof view, frontend-only, reused every existing endpoint); 3-layer review fixed forward (a failed status read no longer silently reads as a false negative, the finalization poll is now capped, DOM writes scoped). Verified live on both a temp preview port and the real running instance; declined (per the risky-action guardrail) to actually click-confirm a real ShredIdentityKey submission against the shared dev stack — the typed-confirm gating was verified via a wrong-token/right-token/cancel sequence instead.
- 2026-07-06 — F13 L1 reconciled (shipped Flows tab satisfies it, no `#/history` rebuild) + L2 v1 shipped (flow-liveness map scrubber, rides the same bucket); L2-full/L3 still wait on Chronicler archive mode.
- 2026-07-07 — **F15 CLOSED**: items 5-6 shipped (`56911ac`) — pkg-lifecycle root-admin gate (confused-deputy close) + live e2e proof under `up-full-capability` (consoleOperator RevokeActor allowed, InstallPackage denied). Also closed the cross-filed "Loupe read-only PG role" lattice item in the same commit (M5 wildcard-grant, not bypass).
- 2026-07-07 — Follow-up (`6b1ab6e`): `56911ac` proved the mechanism but left the live default operator as root — actually re-scoped it (console-operator's own read-grant lens + persisted `loupe-operator.json`, `up-full` wires it automatically); verified live against real non-empty protected-table data.
- 2026-07-18 — Café + Wellness curated onto the door-band Apps group (all four verticals render together, client-shelf empty) — `3470f7d`, verified live (all four green). Same session: **PO survey** (Loupe untouched since F15/2026-07-07) filed F16–F19 + the `designAhead`-trio maintenance row; the old "flip Gateway designAhead" Next-line is now that row (Gateway up-full trigger shipped `11cc15f`).
- 2026-07-18 — F16 UX design drafted (Sally) + **adjudicated (Winston, Andrew-delegated)**, grounded live against the shipped read-models/op DDLs. Key finding: **both** loops are human-gated (Augur `augurDispatchPending` fires on `review.state="approved"`, not `pending`) — Augur is an action tab, not observe-only; its approve is *lighter* than capability's (server-side re-validation, no apply step). §8 forks resolved: approve=Loupe-in-process (Option A), apply=apply-in-Loupe (CLI fallback), reject=simple-confirm, Augur pending sorts by confidence. F16 → 📋 ready.
- 2026-07-18 — **F16.1 SHIPPED**: capability queue+detail+reject, verified live (routing/nav/error rendering; the shared stack has no `capability-author` package installed, so functional correctness rode an embedded-NATS Go test instead). **Next:** F16.3 (Augur tab, zero dep, first prod `ReviewProposal` use), then F16.2 (approve+apply, two contingent spikes). `designAhead`-trio maintenance row is 📋 ready.
- 2026-07-18 — **F16.3 SHIPPED**: Augur escalation tab — queue+detail+approve+reject, shares F16.1's card renderer, pending-by-confidence sort (§8.4), badge now sums both loops. Augur's approve re-validates entirely server-side (no client validation payload, no apply step) so both verdicts shipped in one fire — this is `ReviewProposal`'s first production submitter; verified live (routing/auth/error rendering — the shared stack has no `packages/augur` installed either, so the approve→dispatch write path rode the embedded-NATS Go test, same posture as F16.1). Lead self-review. **Next:** F16.2 (capability approve+apply, two contingent spikes).
- 2026-07-18 — **F16.2 SHIPPED → F16 CLOSED**: capability approve+apply (`#/review/capability/<id>/{approve,apply}`). Both spikes landed in-Loupe, no cross-lane ask: approve re-validates the artifact server-side against the live catalog (Option A — the CLI's three `ValidateCapabilityArtifact` deps all constructible in `cmd/loupe`; a fresh-invalid verdict blocks client-side, no op sent), apply drives the two-commit F-004 install (`CapabilityApplyPlanForProposal`→`Installer.Apply`→`MarkCapabilityProposalApplied`) reusing `pkg.go`'s Installer wiring. FE: approve button live, approved-state "Apply now". Known tail: a partial failure (install committed, mark op failed) isn't retryable via the button for a newPackage — recovery is the CLI mark step (error names it). Verified headless (routing/auth/method-gating/handler-reach; rebuilt asset served) + embedded-NATS Go tests for the guards + `freshCapabilityVerdict` (shared stack has no `capability-author` installed, same posture as F16.1/F16.3). Lead self-review.

## Done log — loupe (newest first)

One line per shipped item (`date · SHA · [tag] title`). Oldest roll to `archive/` past ~25.

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
