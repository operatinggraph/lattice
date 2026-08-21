# Done log archive — loupe (older shipped items, newest first)

Rolled from `loupe.md` when its live Done log passed ~25 entries. Full detail is in git.

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
