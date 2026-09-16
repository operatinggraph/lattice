# Café — "A self-order is half a loop — nobody is told to make it" (2026-09-16)

**Filed (PO, 2026-09-16):** a resident's phone order lands as a bare line on one of N open-tab cards: no
`orderedAt`, no made/served state, no new-order signal, the desk polls Refresh. Live: `Latte · by Riley Chen`,
indistinguishable from one served an hour ago. Winston-adjudicated (implementation-level; no contract surface,
no fork).

## Grounding

- **A line records who, never when or whether.** `Charge` appends `{id, description, amountCents, voided,
  orderedBy}` to `.status.lines` ([ddls.go:1341-1345](../../packages/cafe-domain/ddls.go)); the aspect DDL
  ([ddls.go:229-252](../../packages/cafe-domain/ddls.go)) declares exactly those keys. The only stamps on a tab
  are `openedAt` / `staleAt` (OpenTab) and `settledAt` (Settle), all `time.rfc3339_utc(op.submittedAt)`
  ([ddls.go:1205](../../packages/cafe-domain/ddls.go)). `is_self` already splits the self-order path from the
  POS ring-up inside the one `Charge` branch ([ddls.go:1304](../../packages/cafe-domain/ddls.go)).
- **`void_line_by_id` rebuilds the line by hand** ([ddls.go:1082-1100](../../packages/cafe-domain/ddls.go)) —
  it names the five keys it keeps, so any key a later fire adds is silently dropped by the first void. The
  dossier's *a mirror that drops one of the precedent's checks drops the invariant* applies inward here.
- **`VoidCharge` is the per-line staff op precedent** ([ddls.go:1354-1414](../../packages/cafe-domain/ddls.go)):
  `required_string(p, "lineId")`, `require_open_status`, workplace confinement BEFORE the line lookup (a
  foreign staffer learns no line ids from the refusal), OCC upsert of `.status`. Granted `operator` +
  `frontOfHouse` at `scope=any` ([permissions.go:71-74](../../packages/cafe-domain/permissions.go)); descriptor
  with a `(facet)` courtesy line ([opmetas.go:172-200](../../packages/cafe-domain/opmetas.go)); its confinement
  walk is baselined as three `read` rows in `internal/testutil/read_drift_baseline.txt:292-294`; vectors in
  `integration_test.go:894-1377` and `workplace_confinement_test.go:189-291`.
- **The desk already reads every line.** `cafeTabSettlement` passes `t.status.data.lines` through whole
  ([targets.go:355](../../packages/cafe-domain/targets.go)); `/api/tabs` re-normalizes it into `tabChargeLine`
  ([tabs.go:50-56](../../cmd/cafe-app/tabs.go)); `loadFrontDesk` filters to open tabs and draws a card per tab
  ([app.js:1209-1300](../../cmd/cafe-app/web/app.js)), `chargeLinesBlock` renders the lines
  ([app.js:454-476](../../cmd/cafe-app/web/app.js)). No lens is missing — the queue is a projection of a column
  the desk already holds, so P5 is satisfied by `/api/tabs` and no new read model is needed.
- **Nothing polls.** The Front Desk view loads on tab click and on `#frontdesk-refresh` only
  ([app.js:664-675, 2425](../../cmd/cafe-app/web/app.js)); no `setInterval` / SSE anywhere in the app.
- **Gates a new staff op trips:** `lint-workplace-staff-vector` (a non-operator staff vector in
  `workplace_confinement_test.go`), `lint-refusal-courtesy` (JS site + Facet descriptor declarations per state
  code), `lint-app-op-descriptors` (`cmd/cafe-app` ceiling `9`, [lint-app-op-descriptors.go:236](../../scripts/lint-app-op-descriptors.go)),
  `lint-opmeta-required-fields`, the read-drift guard (baseline rows). No `verify-package-cafe-domain.go` exists.

## Verdict — *a line says when it was ordered and whether it was served; the desk sees the unserved ones oldest-first, and the page tells it*

1. **`orderedAt` on every line** — `Charge` stamps `time.rfc3339_utc(op.submittedAt)` on the entry it appends.
2. **A served mark, recorded at the event.** A line carries `servedAt` (RFC3339) + `servedBy` (identity key)
   once handed over. **A POS ring-up is handed over at the counter**, so the staff path of `Charge` stamps both
   at ring-up (`servedAt = orderedAt`, `servedBy = op.actor`); the self path leaves them absent — that line is
   the desk's work. A line with no `orderedAt` predates this fire and is read as unknown, never as unserved.
3. **`MarkLineServed{tabKey, lineId}`** — staff-only (`operator` + `frontOfHouse`, `scope=any`), the `VoidCharge`
   shape: confinement before the line lookup; refuses `TabNotOpen`, `UnknownChargeLine`, `LineVoided`,
   `LineAlreadyServed`; stamps `servedAt` / `servedBy` on the one line, OCC upsert of `.status` with total, memo
   and every other line untouched; event `tab.lineServed`. `void_line_by_id` and the new helper copy the line
   whole and overwrite the one key they own, so no later key is dropped by either.
4. **The desk's orders queue.** `ordersQueue(tabs)` (pure, goja-pinned): over open tabs, lines not voided with
   `orderedAt` and no `servedAt`, oldest `orderedAt` first (tie: tabKey, id). A Front Desk **Orders** panel
   above the tab grid lists each as *item · who · ordered N min ago · Mark served*; the panel header counts
   them. `chargeLinesBlock` tags an unserved self-order *to make* and a served one *served* on every surface,
   so the resident sees their order's state on their own card.
5. **The page tells the desk.** While the Front Desk view is showing, `/api/tabs` is re-read every 15 s and
   the Orders panel + count re-rendered (only the panel — the card grid's joins stay on Refresh); the interval
   is cleared when the view changes. Polling is the established transport here (no watch/SSE in any vertical
   app); a push transport is a platform primitive this row does not need.
6. cafe-domain `0.15.1 → 0.16.0`; `PermittedCommands` on the tab vertex + `tabStatus` aspect DDLs; descriptor +
   grant; baseline rows; ceiling `9 → 10`; README.

### Fire brief (build note, 2026-09-16)

1. **Scope** (board row, verbatim): *A resident's phone order lands as a bare line on one of N open-tab cards:
   no `orderedAt`, no made/served state, no new-order signal, the desk polls Refresh. Live: `Latte · by Riley
   Chen`, indistinguishable from one served an hour ago.* Next: *line `orderedAt` + a served mark on
   `.status.lines`, a Front Desk orders queue oldest-first.* Green bar: a self-order `Charge` lands a line with
   `orderedAt` and no `servedAt`; a staff `Charge` lands one served at ring-up; `MarkLineServed` as a
   front-of-house staffer at the tab's building stamps `servedAt`/`servedBy` and nothing else, is refused at
   another building, on a voided line, on a served line, on a settled tab, and to a consumer; a void keeps
   `orderedAt`; `ordersQueue` orders three lines across two tabs oldest-first and omits served/voided/legacy
   lines; the Orders panel re-reads without a click.
2. **Touch-list (verified live):** cafe-domain `ddls.go:43` + `:233` (`PermittedCommands`), `:229-252` (aspect
   DDL text + schema), `:1082-1100` (`void_line_by_id`), `:1341-1345` (`Charge` line), `:1354-1414`
   (`VoidCharge` → mirror), `opmetas.go:172-200` (descriptor → mirror), `permissions.go:71-74`, `package.go:99`,
   `manifest.yaml:2`, `README.md:55-78`, `integration_test.go:894-1377` (vectors → mirror),
   `workplace_confinement_test.go:189-291` (`wcSubmitVoidCharge` → mirror) · `internal/testutil/read_drift_baseline.txt:292-294`
   · `scripts/lint-app-op-descriptors.go:236` · cafe-app `tabs.go:50-56, 79-134`, `tabs_test.go`, `web/app.js:454-476`
   (`chargeLinesBlock`), `:664-675` (`showView`), `:961-980` (VoidCharge wiring → mirror), `:1208-1300`
   (`loadFrontDesk`), `:881-888` (courtesy block), `web/index.html:36-52`, `web/style.css`, a new
   `web_orders_queue_test.go` (mirror `web_receipt_test.go`).
3. **Precedents:** `VoidCharge` (script, descriptor, grant, vectors, confinement vector, baseline rows, Facet
   courtesy); `openedAt` for the stamp idiom; `renderFrontDeskArrears` for a Front Desk list with row buttons;
   `web_receipt_test.go` for the goja pin.
4. **Increments:** (1) package — fields + helper + op + DDL text + descriptor + grant + version + README + baseline
   rows; vectors: stamp on self/staff `Charge`, serve path, the five refusals, void carries `orderedAt`, the
   confinement vector. Green: `go test ./packages/cafe-domain/ -count=1`, `go test ./internal/testutil/ -count=1`.
   (2) app — `tabChargeLine` fields, `ordersQueue` + Orders panel + `Mark served` + 15 s re-read + line tags +
   courtesy lines + ceiling; goja pin + `tabs_test.go`. Green: `go test ./cmd/cafe-app/ -count=1`, every
   `STRICT=1 scripts/lint-*.go`, `golangci-lint`, `make vet`. (3) live: `make refresh-cafe`, cycle
   `bin/cafe-app`, a self-order as Riley Chen, the queue as Dana Whitfield, `MarkLineServed` via the Gateway.
5. **Gotchas:** version bump both sites; `lint-workplace-staff-vector` (frontOfHouse vector, not operator);
   `lint-refusal-courtesy` — JS site `MarkLineServed/TabNotOpen, LineVoided, LineAlreadyServed: hide —
   ordersQueue …` and Facet `(facet)` lines; `lint-app-op-descriptors` ceiling with the reason beside it; the
   read-drift baseline rows under a `#` comment; the `_packages.md` dossier: *a mirror that drops one of the
   precedent's checks drops the invariant* (every refusal `VoidCharge` runs before the line lookup runs here
   too, in the same order) · *a recorded value is read as the fact it records* (`servedAt` = handed over;
   absent + `orderedAt` present = to make; absent + no `orderedAt` = unknown — three states, the queue and the
   tags each decide all three) · *the "leg" of a guard is every op that WRITES the guarded value* (`Charge`'s two
   paths and `MarkLineServed` are the three writers of `servedAt`; `void_line_by_id` is the fourth touch and
   copies it) · standing checklist #3 (revert-prove: drop the `servedAt` stamp on the staff path and watch the
   staff vector fail; drop the copy in `void_line_by_id` and watch the void vector fail) · #6 (`void_line_by_id`'s
   hand-rebuilt dict is the debt, fixed here, not copied).
6. **Adjacent finds:** *Tab and visit times render as raw UTC ISO* (★ XS, its own row) — absorbed as this
   batch's next unit, its own commit.
7. **Non-goals:** a push transport (SSE/watch) for the desk; refusing `Settle` with unserved lines (the
   resident's call); a kitchen/bar station split; Facet showing per-line state (its tab browse projects no
   line column; the descriptor form takes a `lineId`).
