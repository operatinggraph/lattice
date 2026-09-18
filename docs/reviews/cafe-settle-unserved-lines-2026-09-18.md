# Café — "A settled tab bills a self-order nobody made" (2026-09-18)

**Filed (PO, 2026-09-17):** `Settle` (resident, desk, the 24 h sweep) posts lines carrying `orderedAt` and no
`servedAt`; the desk's queue drops a non-open tab ([app.js:1408](../../cmd/cafe-app/web/app.js)) and
`MarkLineServed`/`VoidCharge` refuse `TabNotOpen` after, so the kitchen loses the ticket and the resident pays;
the only remedy is a refund. Refuse or void unserved lines at settle. Live: 4 settled tabs, 5 unserved lines,
$17.50. Winston-adjudicated (implementation-level; no contract surface, no fork). The self-order fulfilment
design ([2026-09-16](cafe-self-order-fulfilment-2026-09-16.md) §Non-goals) left "refusing `Settle` with
unserved lines" as the resident's call; the PO's live count is the call.

## Grounding

- **Three legs close a tab.** `Settle` ([ddls.go:1739-1868](../../packages/cafe-domain/ddls.go)) serves the
  resident's self leg (`authContextTarget` set, the `applicationFor` ownership proof) and the desk's staff leg
  (`require_workplace`, optional `paidCents` that must equal the total); `SettleStaleTab`
  ([ddls.go:1870-1938](../../packages/cafe-domain/ddls.go)) is the orchestration-internal twin
  `cafeStaleTabSettlement`'s `missing_settle` gap dispatches once `staleAt` passes
  ([targets.go:140-165](../../packages/cafe-domain/targets.go)); it no-ops on a tab a staff Settle already closed.
  All three freeze `lines` whole and carry `totalCents` forward unchanged.
- **A line's state is three-valued and already read that way everywhere.** `orderedAt` set + `servedAt` absent =
  to make; `servedAt` set = served; neither = predates the fields, unknown ([ddls.go:301](../../packages/cafe-domain/ddls.go)
  prose; `ordersQueue` [app.js:1405-1431](../../cmd/cafe-app/web/app.js); `chargeLinesBlock`
  [app.js:515-540](../../cmd/cafe-app/web/app.js)). A staff ring-up is served at ring-up; only a self-order can be
  unserved at settle.
- **`void_line_by_id` is the void precedent** ([ddls.go:1303-1324](../../packages/cafe-domain/ddls.go)): copies the
  line whole, rewrites `voided` only, returns the amount; `VoidCharge` ([ddls.go:1641-1700](../../packages/cafe-domain/ddls.go))
  subtracts it from `totalCents` clamped at 0 and re-derives `itemsMemo` from the updated lines.
- **A resident cannot void.** `VoidCharge` and `MarkLineServed` grant `operator` + `frontOfHouse` only
  ([permissions.go](../../packages/cafe-domain/permissions.go)); a resident's unmade order is the desk's to make or
  cancel. The desk's Orders panel ([app.js:1451-1512](../../cmd/cafe-app/web/app.js)) is where it marks; the POS card
  ([app.js:1262](../../cmd/cafe-app/web/app.js)) is where it voids.
- **Nothing downstream reads a line.** `cafeTabSettlement`'s `missing_charge` posts `row.totalCents` with
  `row.itemsMemo`, `missing_payment` posts `paidAtSettleCents` ([targets.go:99-137](../../packages/cafe-domain/targets.go));
  a `totalCents` of 0 never opens the charge gap. So a void at settle needs only the total and memo recomputed.
- **Sites of `Settle`.** JS: `settlePayEnvelope` ([app.js:194](../../cmd/cafe-app/web/app.js)), `renderPos`
  ([app.js:1024](../../cmd/cafe-app/web/app.js)), `loadFrontDesk` ([app.js:1514](../../cmd/cafe-app/web/app.js)),
  `renderResident` ([app.js:2299](../../cmd/cafe-app/web/app.js)); Facet via the descriptor
  ([opmetas.go:238](../../packages/cafe-domain/opmetas.go)). `lint-refusal-courtesy` arms on a new state code.
- **Gates.** No new op: `lint-app-op-descriptors`' ceiling (11) and the read-drift baseline are untouched; the
  refusal reads `.status` only, which every dispatcher already declares (`lint-seed-declared-reads` has nothing
  new to bind). `lint-refusal-courtesy` binds the four JS sites + Facet for the new code. Version bump both sites.

## Verdict — *a tab closes only over made orders: a person settling is refused until each unmade line is served or voided; the sweep, with nobody there, voids them*

1. **`unserved_line_ids(lines)`** — the ids of every line not voided, with `orderedAt`, without `servedAt` (the
   three-state read; a legacy line with neither is never unserved).
2. **`Settle` refuses `UnservedLines`** on both human legs, after the confinement / ownership proof (a foreign
   staffer learns nothing about a tab's lines from the refusal) and before the `paidCents` checks: `UnservedLines:
   N order(s) still to make — mark served or void first: <id (description), …>`. The desk marks or voids and
   settles again; the resident asks the desk. A refusal, not a void, on a human leg: the person at the counter
   knows whether the latte was handed over and the desk has both verbs; a silent void would hand a served-but-
   unmarked line out free.
3. **`SettleStaleTab` voids every unserved line** — copied whole, `voided: true`, `voidedReason: "unserved"` (the
   only void that records a reason: a desk void is the desk's act, this one is the sweep's, and the receipt says
   "never made"); `totalCents` decremented by each line's own amount, clamped at 0; `itemsMemo` re-derived from
   the updated lines; the `tab.settled` event carries `voidedUnservedLineIds`. A tab nobody closed in 24 h whose
   order nobody marked made is a tab whose order was not made; the house's remedy for a served-but-unmarked line
   is the Orders panel, before the sweep. The no-op arm (already settled) is unchanged.
4. **FE courtesy.** A shared pure `unservedLineCount(tab)` (goja-pinned): the POS card's *Settle* / *Settle & pay*,
   the Front Desk card's *Settle* and the resident's *Settle* are `disabled` while it is > 0, the desk's with
   "N to make — mark served or void first", the resident's with "Your order is still being made — the desk can
   settle or cancel it". The POS card's `chargeLinesBlock` gains a *Mark served* button beside *Void* on a to-make
   line (the desk's in-place way through the refusal — `renderPos` becomes a second `MarkLineServed` site and
   declares its courtesies), and a voided line with `voidedReason: "unserved"` reads *(voided — never made)*. An
   `UnservedLines` refusal on a stale card re-renders it, the `TabNotOpen` / `PaidMismatchesTab` idiom. Facet:
   `none` — its tab browse projects no line column; the toast names the lines.
5. cafe-domain `0.18.0 → 0.19.0`; DDL prose for `Settle` / `SettleStaleTab` / `lines`; README.

### Fire brief (build note, 2026-09-18)

1. **Scope** (board row, verbatim): *`Settle` (resident, desk, the 24 h sweep) posts lines carrying `orderedAt`
   and no `servedAt`; the desk's queue drops a non-open tab and `MarkLineServed`/`VoidCharge` refuse `TabNotOpen`
   after, so the kitchen loses the ticket and the resident pays; the only remedy is a refund. Refuse or void
   unserved lines at settle.* Green bar: a desk `Settle` and a resident self `Settle` over a tab with one
   unserved self-order line are refused `UnservedLines` naming the line, and accepted once the line is marked
   served (and, separately, once voided); a desk `Settle{paidCents}` over an unserved line is refused
   `UnservedLines` before `PaidMismatchesTab`; a foreign staffer is refused on confinement, not `UnservedLines`;
   a tab whose only unserved line predates `orderedAt` settles; `SettleStaleTab` over two self-order lines (one
   served, one not) and one staff line settles with the unserved line `voided: true, voidedReason: "unserved"`,
   the total reduced by exactly its amount, the memo without it, the other lines byte-identical, and the event
   naming the id; `unservedLineCount` counts three tabs' lines correctly (voided / served / legacy excluded); the
   settle buttons are disabled at the count.
2. **Touch-list (verified live):** cafe-domain `ddls.go:1303-1324` (`void_line_by_id` → sibling helper),
   `:1739-1868` (`Settle` — refusal after the ownership proof at `:1776`, before `optional_cents` at `:1794`),
   `:1870-1938` (`SettleStaleTab` — the void at `existing_lines`, `:1909`), `:112-137` + `:276-305` (DDL
   prose: `Settle`, `SettleStaleTab`, `lines`, the `voidedReason` key in the schema at `:293`), `package.go:106`,
   `manifest.yaml:2`, `README.md`, `integration_test.go:1381-1630` (Settle vectors → mirror), `:1636-1705`
   (SettleStaleTab vector → mirror), `:2064-2300` (self-scope + paidCents vectors → mirror), `opmetas.go:238-239`
   (Facet courtesy line) · cafe-app `web/app.js:194-203` (`settlePayEnvelope`), `:515-540` (`chargeLinesBlock`),
   `:1024-1026` (renderPos courtesies), `:1099` (void wiring → mirror for serve), `:1203-1212` + `:1620-1624`
   (stale-card refusal idiom), `:1262` (POS card), `:1396-1431` (`ordersQueue` → sibling helper),
   `:1449-1500` (Orders panel `MarkLineServed` submit → mirror), `:1514-1515`, `:1953` (Front Desk card),
   `:2299-2300` + `:2353` (resident card), `web_orders_queue_test.go` (goja pin → mirror).
3. **Precedents:** `void_line_by_id` (copy-whole void); `VoidCharge`'s clamp + memo re-derivation; `fail("Code:
   …")` with `require_open_status`'s message shape; `PaidMismatchesTab`'s placement after the ownership proof; the
   Orders panel's `MarkLineServed` envelope + in-flight guard; `fillLeaseSelect`'s disabled-option-with-reason shape (`renderPos`, `:1024`);
   `web_orders_queue_test.go` for the goja pin.
4. **Increments:** (1) package — helper + refusal + sweep void + DDL prose/schema + version + README + Facet
   courtesy line; vectors per the green bar; green: `go test ./packages/cafe-domain/ -count=1`. (2) app —
   `unservedLineCount` + disabled buttons + Mark served on the POS card + never-made tag + stale-card handling +
   courtesy declarations; goja pin; green: `go test ./cmd/cafe-app/ -count=1`, `STRICT=1 go run
   ./scripts/lint-refusal-courtesy.go`, every `STRICT=1 scripts/lint-*.go`, `golangci-lint run ./...`, `make vet`.
   (3) live: `make refresh-cafe`, cycle `bin/cafe-app`, a self-order then a Settle refused through the Gateway,
   marked served, settled.
5. **Gotchas:** version bump both sites (`lint-package-version`); `lint-refusal-courtesy` — every `Settle` site
   declares `UnservedLines` (`disable — unservedLineCount(tab) > 0 …`), `renderPos` declares
   `MarkLineServed/TabNotOpen, LineVoided, LineAlreadyServed` as a new site, Facet `(facet)` line; the refusal
   reads only `.status` (already declared by every dispatcher — no `lint-seed-declared-reads` change); the
   `_packages.md` dossier: *the "leg" of a guard is every op that WRITES the guarded value* (three closers of a
   tab — `Settle` ×2 legs and `SettleStaleTab` — each decides the unserved set; `void_line_by_id` and the new void
   are the two writers of `voided`, both copy-whole) · *a recorded value is read as the fact it records*
   (`voidedReason: "unserved"` = the sweep voided it unmade; absent = a desk void; three line states, the
   fourth — voided-unserved — reads *never made*) · *a mirror that drops one of the precedent's checks drops the
   invariant* (the refusal sits after confinement + ownership exactly where `PaidMismatchesTab` sits; the sweep's
   void keeps the clamp) · `vertical-apps.md`: *a count the FE promises must apply the op's own predicate*
   (`unservedLineCount` is `unserved_line_ids` in JS — pin both over one fixture with a legacy, a voided, a served
   and a to-make line) · standing checklist #3 (revert-prove: drop the refusal and watch the desk vector pass;
   drop the sweep's void and watch the stale vector fail; drop the `disabled` and watch the goja pin fail).
6. **Adjacent finds:** none at scoping.
7. **Non-goals:** a resident cancelling their own order (VoidCharge's self scope — a different row); refusing
   `SettleStaleTab` (nobody is there to act); a made/served split (one served mark); re-opening a settled tab.

### Build note (2026-09-18)

Shipped `a97c193c` (merge `89491f71`); brief `92ea61a2`. Live on the shared stack (cafe-domain 0.19.0 diff-applied,
`bin/cafe-app` cycled): Riley Chen opened a tab and self-ordered a Latte through the Gateway (`orderedAt
2026-09-18T18:05:41Z`, no `servedAt`); their own Settle was refused `UnservedLines: 1 order(s) still to make — mark
served or void first: line-1 (Latte)`, Dana Whitfield's desk Settle the same; Dana's `MarkLineServed` landed and
Riley's Settle was then accepted. The sweep's void is proven by `TestSettleStaleTab_VoidsUnservedLines` (the 24 h
deadline is not driven live).

Deviations from the brief: `tabs.go` (the app's read boundary) joined the touch-list at review — the brief omitted
it; `unservedLineCount` counts a `pending` line (an accepted self-order the lens has not projected yet) and the
resident card counts over its rendered overlay; `settleButtonAttrs` + `residentSettlePanelMarkup` were extracted so
the disabled markup is pinned, not the ternary; the null guard on a line's amount. Review classification (one cold
pass, three lenses, over the whole diff): **implementation-bug + brief-gap** — `/api/tabs` dropped `voidedReason`
(BLOCKING; the vertical-apps census class, sixth sighting); **implementation-bug** — the pending overlay left Settle
enabled (seventh sighting on the count-predicate class), the stale-card re-render at two of five sites;
**test-gap** — the two ordering clauses (confinement / ownership before `UnservedLines`) and the disabled markup had
no vector; **nit** — a present-null amount would have raised in the sweep. Adjacent finds: none.
