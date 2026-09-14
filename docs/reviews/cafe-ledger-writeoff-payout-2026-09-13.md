# Café — "A debt it can never collect" + "A refund handed back in cash AND spent as credit" (2026-09-13)

**Filed (PO, 2026-09-13), two rows on one mechanism.** (1) The only clearing verb is `CreditCafeAccount`,
whose `.entry` carries no reason ([scripts.go:1240](../../packages/cafe-ledger/scripts.go)) — a write-off is
booked as cash collected; live: 3 of 5 overdue debtors are unapproved seed leases still drawing reminders.
(2) `RefundCafeCharge` only posts a credit ([scripts.go:1593](../../packages/cafe-ledger/scripts.go)); nothing
records money leaving, `CreditCafeAccount` refuses on a credit balance, and the desk grid drops credit leases
([ledger.go:157](../../cmd/cafe-app/ledger.go)), so the house owes invisibly; live: Riley Chen −$35.75 after
two refunds, prepaying every later tab. Winston-adjudicated (implementation-level; no contract surface, no
fork — the precedent for (1) is shipped in clinic-ledger + wellness-ledger, and (2) is one more entry kind on
the same `.entry` aspect).

## Grounding

- **The precedent for a reason on a credit is shipped twice.** clinic-ledger's `ClinicCreditAccount` carries
  `reason ∈ {payment, waiver}` (default `payment`), refused as `waiver` on a self-scoped submit
  ([opmetas.go:110](../../packages/clinic-ledger/opmetas.go)); wellness-ledger adds `refund`
  ([scripts.go:282-289](../../packages/wellness-ledger/scripts.go)) and refuses both non-payment reasons on the
  member leg ([:305-310](../../packages/wellness-ledger/scripts.go)). Both lenses project `t.entry.data.reason`
  and both FEs render "(waived)" / "(refunded)" off it ([wellness app.js:1146-1152](../../cmd/wellness-app/web/app.js)).
- **No payout precedent exists anywhere** (`grep -rin "payout|paid out|settledAs|disburse" packages/ cmd/*/web`
  → 0). The row's two options: a staff-only payout debit, or `settledAs: cash` on the refund. **Decided: the
  payout debit.** Whether cash moved is not a property of the refund — a refund of an UNPAID charge moves no
  cash and correctly nets the balance to zero, while a refund of a paid charge leaves a credit the house
  either hands back or lets the resident spend. Under the FIFO a partially-paid history makes "was this charge
  paid" ambiguous at refund time; the cash fact is known only when the desk hands it over. So the cash
  leaving is its own entry: `PayoutCafeCredit` posts a **debit** (`reason: payout`) capped at the account's
  credit, so Σdebit−Σcredit returns to zero — the credit was settled in cash, not spent. Every balance
  consumer sums it unchanged (a debit), and the arrears branch never opens because the new balance is ≤ 0.
  **Amended at build (2026-09-14):** "moves no cash" is not enough once a waiver exists — write off $18,
  refund the same charge, and the credit reads as payable. The conserved quantity is **cash**: an account's
  credit balance may never exceed the net cash it has paid in, `cashCents = Σ payment − Σ payout`, a second
  maintained field on `.balance`, enforced where the phantom credit would be minted (`RefundExceedsPaid`) and
  again at the payout (`PayoutExceedsCash`). And a payout, being a debit, must never itself be refundable —
  `reversed_charge` refuses a debit carrying any `reason`.
- **`.entry.reason` becomes a complete classification, not a payment-only flag.** Credits: `payment` (default)
  · `waiver` (CreditCafeAccount, staff only) · `refund` (written by `RefundCafeCharge` itself). Debits:
  absent (a charge) · `payout` (written by `PayoutCafeCredit` itself). A caller-supplied `reason` is accepted
  on `CreditCafeAccount` alone and refused elsewhere (the `reversesRef` / `tabRef` cross-refusal idiom,
  [scripts.go:1395-1397](../../packages/cafe-ledger/scripts.go), [:1697-1699](../../packages/cafe-ledger/scripts.go)).
- **The amount cap already binds the waiver leg.** `is_payment` selects the outstanding-balance cap + the
  legacy backfill ([scripts.go:1293-1349](../../packages/cafe-ledger/scripts.go)); a waiver is a credit that
  is not a refund, so it is capped at what is owed with no new conjunct — only the refusal's wording changes.
  The payout leg needs the SAME two things mirrored on the debit side: the balance read (backfilling a legacy
  account, as a payment does) and a cap — `balance ≥ 0` refuses (`NoCreditToPayOut`), `amount > −balance`
  refuses (`PayoutExceedsCredit`). `_packages.md` dossier: *an amount cap written for the self-service leg
  leaves the staff leg unbounded* — here the cap is on the op, both legs, as `CreditCafeAccount`'s is.
- **Confinement + self-scope.** `PayoutCafeCredit` grants `operator` + `frontOfHouse` at `scope: any` and NO
  consumer grant (a resident handing themselves cash); the script refuses a target / validated bit exactly as
  `RefundCafeCharge` does ([scripts.go:1651-1691](../../packages/cafe-ledger/scripts.go)) and confines through
  `post_entry`'s `require_workplace`. `waiver` on a self-scoped `CreditCafeAccount` is `AuthDenied` on the
  ownership branch, the wellness shape. Dossier: *a confinement guard tested only as the operator has never
  run* — the payout op gets a frontOfHouse home/away pair in `workplace_confinement_test.go`.
- **OCC rests on `derive_reads`.** The op's `.balance` / `.arrears` optional reads are derived server-side for
  the three existing entry ops ([scripts.go:1582-1623](../../packages/cafe-ledger/scripts.go)); `PayoutCafeCredit`
  joins the same list, and one test submits it with an empty `contextHint` (dossier: *a guard's OCC rests on
  whoever writes its read declaration*).
- **The desk grid drops `balance <= 0`** ([ledger.go:157](../../cmd/cafe-app/ledger.go)) and `balanceRow` carries
  no `accountKey`; the arrears list renders inert text ([app.js:1149-1172](../../cmd/cafe-app/web/app.js)). Both
  forms that dispatch `CreditCafeAccount` are hand-built around the descriptor via `renderOpForm` with `me`
  set ([app.js:1620-1697](../../cmd/cafe-app/web/app.js)) — the write-off and payout buttons mirror that exact
  shape. `frontDeskBalanceBadge` already renders nothing at `≤ 0` ([app.js:1112-1120](../../cmd/cafe-app/web/app.js)).
- **Live census (stack up, 2026-09-13):** 5 leases owe (3 seed leases $18 / 23–39 days); 1 lease in credit
  (Riley Chen −$35.75, two refunds). Re-run at build: `curl -s :7801/api/frontdesk-balances` as staff.

## Verdict — *a credit says why it was posted; cash handed back is its own debit*

1. `CreditCafeAccount{…, reason?: payment|waiver}` — stored on `.entry`, default `payment`; `waiver` refused on
   the self-scoped leg; capped at the outstanding balance either way (wording: "a write-off of $X exceeds…").
2. `RefundCafeCharge` writes `reason: refund` itself and refuses a payload `reason`; `DebitAccount` refuses one.
3. `PayoutCafeCredit{accountKey, amountCents, memo?}` — a debit with `reason: payout`; staff-only, never
   self-scoped, workplace-confined; reads `.balance` (backfilling a legacy account); refuses `NoCreditToPayOut`
   when `balance ≥ 0`, `PayoutExceedsCredit` when `amount > −balance` and `PayoutExceedsCash` when
   `amount > cashCents`; moves `.balance` by `+amount` and `cashCents` by `−amount`; never writes `.arrears`
   (the debit branch opens an episode only when the new balance is > 0, which the cap forbids); emits
   `account.paidOut`. A payout is never refundable (`reversesRef` naming a debit with a `reason` is refused).
3b. **The cash invariant** — `.balance` carries `cashCents = Σ payment credits − Σ payout debits` (minted 0 by
   `CreateAccount`, carried on every write; on a pre-0.6.0 document or a legacy account it is computed once by
   the bounded `postedTo` replay from the payment / refund / payout legs — a reason-less credit is a payment
   iff it carries no live `reverses` link — and left absent by a charge). `RefundCafeCharge` refuses
   `RefundExceedsPaid` when the refund would leave the credit balance above `cashCents`.
4. `cafeLedgerHistory` projects `t.entry.data.reason AS reason`.
5. `cmd/cafe-app`: `Reason` threads through the statement rows; `computeLedgerBalances` keeps credit leases
   (`balanceCents < 0`, no due date) and every row carries `accountKey`; the desk list shows debtors with a
   **Write off** button (confirm → `CreditCafeAccount` `reason: waiver`, amount = balance) and credit leases as
   "in credit $X" with a **Pay out** button (confirm → `PayoutCafeCredit`, amount = credit); ledger lines badge
   "Waived" / "Paid out" alongside the existing "Refund"; the tab card badge says "In credit $X".

**Test strategy.** Package: waiver accepted + `.entry.reason` pinned, self-scoped waiver `AuthDenied`, waiver
over the balance refused with the write-off wording, refund row carries `reason: refund`, payload `reason` on
DebitAccount / RefundCafeCharge / PayoutCafeCredit refused; payout accepted against a credit (balance → 0,
`.arrears` untouched), `NoCreditToPayOut` at zero and positive, `PayoutExceedsCredit`, self-scoped payout
refused, payout backfills a legacy account, empty-`contextHint` payout hydrates via `derive_reads`, frontOfHouse
home/away pair. Lens: `reason` column pinned on a waiver row and a payout row. App: credit lease appears with
negative `balanceCents` and no due date; `accountKey` populated from the projection; `reason` threaded (asserted
equal to the projection's value). FE: `node --check`, `make lint-web`, catalog-coverage test for the new op.
Every refusal reverted-proven in the worktree.

### Fire brief (build note, 2026-09-13)

1. **Scope** — the two rows' next steps verbatim: *"`waiver` reason, refused self-scoped; desk Write-off on the
   arrears row"* and *"a staff-only payout debit (or `settledAs: cash` on the refund); desk shows credit"* —
   built as verdict 1–5. Green bar: the test strategy + the live proof (one seed debtor written off, Riley
   Chen's $35.75 paid out, both statements reading right).
2. **Touch-list (verified live at selection):** `packages/cafe-ledger/scripts.go` — `post_entry` `:1217-1553`
   (reason parse beside `memo` `:1236`; ownership branch `:1241-1281`; `is_payment` `:1293-1296` → the
   cap/backfill selector; cap `:1341-1359`; cross-refusals `:1395-1397`), `execute` `:1625-1708` (RefundCafeCharge
   refusal `:1651-1691` is the payout dispatch's mirror), `derive_reads` `:1582-1623`; `opmetas.go` `:72-120`
   (CreditCafeAccount descriptor — `reason` joins the schema + FieldDescriptions; the new descriptor mirrors
   RefundCafeCharge's `:122-181` with CreditCafeAccount's enumerations); `permissions.go` `:64-100`; `ddls.go`
   `PermittedCommands` `:177`, `:310`, the cafetransaction narrative `:308-408`; `lenses.go` `:107-122`;
   `lens_cypher_test.go` `:118-204`; `ledger_test.go` helpers `:144-166`, `:561-593`; `workplace_confinement_test.go`
   `:59-195`; `manifest.yaml:2` + `package.go:114` (0.5.2 → 0.6.0); `README.md` `:30`, `:36-40`, `:89`, `:104`;
   `internal/testutil/read_drift_baseline.txt` (`:157-163`, `:319-328` — the payout op's confinement rows).
   `cmd/cafe-app/ledger.go` — `ledgerEntryProjection` `:15-25`, `ledgerEntryRow` `:33-41`, `balanceRow` `:112-119`,
   `computeLedgerBalances` `:129-170`; `ledger_test.go`; `op_catalog.go:208`; `web/app.js` `:1053-1057`,
   `:1112-1120`, `:1149-1172`, `:1514-1552`, `:1620-1654`; `web/index.html:41-42`. `docs/components/vertical-apps.md:38`.
3. **Precedents:** wellness `reason` parse + self-leg refusal ([scripts.go:282-310](../../packages/wellness-ledger/scripts.go));
   clinic descriptor `reason` enum ([opmetas.go:110-116](../../packages/clinic-ledger/opmetas.go)); café's own
   `RefundCafeCharge` dispatch refusal + `confine=True` ([scripts.go:1651-1707](../../packages/cafe-ledger/scripts.go))
   for the payout dispatch; the `is_payment` cap for the payout cap ([:1341-1359](../../packages/cafe-ledger/scripts.go));
   `staffCreditHint` / `creditAs` / `seedChargedAccount` for the tests; the front-desk payment form for both
   buttons ([app.js:1620-1654](../../cmd/cafe-app/web/app.js)); wellness's `isWaiver` badge
   ([app.js:1146-1152](../../cmd/wellness-app/web/app.js)).
4. **Increments:** (1) package — scripts + descriptors + grants + DDL lists + lens + tests + bump + baseline —
   `go test ./packages/cafe-ledger/ -count=1 && go test ./internal/refractor/ -run 'Corpus|Census' -count=1`;
   (2) app Go — `go test ./cmd/cafe-app/ -count=1`; (3) FE — `node --check cmd/cafe-app/web/app.js && make lint-web
   && go test ./cmd/cafe-app/ -count=1 && go run ./scripts/lint-app-op-descriptors.go`; then every gate:
   `go build ./... && make vet && golangci-lint run ./... && STRICT=1 go run ./scripts/lint-conventions.go &&
   DIFF_BASE=<base> go run ./scripts/lint-package-version.go` + `lint-markup-escaping`, `lint-stale-render-guard`,
   `lint-ceremony-throw-path`, `lint-opmeta-required-fields`, `lint-package-standard`; live: `make refresh-cafe`
   from the main checkout, cycle `bin/cafe-app`, write off one seed debtor, pay out Riley Chen.
5. **Gotchas:** package bump (new op = minor); `derive_reads` covers the new op; `# read-posture` annotations
   stay as-is (no new live reads); the refusal strings are toasted verbatim (money, never a key); the new
   descriptor carries `{actor} holdsRole out` AND `{payload.accountKey} postedTo in` (the backfill replay) and
   both FE buttons pass `me`; `KNOWN_CATALOG_OPS` gains the op; the FE builds markup by string → every value
   through `escapeHtml`; the two irreversible submits narrate a throw as "may have landed" in the `withheld`
   vocabulary. `_packages.md` dossier: *a confinement guard tested only as the operator has never run*
   (frontOfHouse vector); *a guard's OCC rests on whoever writes its read declaration* (empty-contextHint test);
   *an amount cap written for the self-service leg leaves the staff leg unbounded* (cap on the op); *a recorded
   value is read as the FACT it records… a hydrated aspect's absence is two facts* (`.balance` absence stays
   the legacy signal, backfilled — never "no credit"); *a standing `scope=any` write… confine by state machine*
   (n/a — the target is an account, ownership proven by topology). `vertical-apps.md` dossier: *a staff-leg
   descriptor context that passes no `me`*; *a server-side refusal added to one form leaves its sibling a dead
   end* (Write off renders only on a debtor row, Pay out only on a credit row, both amounts prefilled from the
   projected balance); *a transport throw after a destructive submit*; *a value reaches markup unescaped*.
   Standing checklist: #1 no new state (one more entry kind on an existing aspect); #2 censuses re-run live
   (0 payout precedents, 2 reason precedents, 2 dispatch sites, 5 debtors + 1 credit); #3 every refusal
   reverted-proven, `Reason` asserted equal to the projection at the producer; #4 nothing removed; #5 no new
   deterministic key; #6 the clinic/wellness precedents verified against the self-leg rule before mirroring.
6. **Adjacent finds:** none at scoping — the Today panel, the moved-out-resident tab and the wellness rows are
   separate board rows, untouched.
7. **Non-goals:** no `settledAs` on the refund; no consumer grant for the payout; no change to the FIFO,
   the arrears episode rules or the reminder; no Today panel; no clinic/wellness/loftspace edit.

### Build note (2026-09-14)

Shipped at `50987c67` (merge `83b78ff3`, CI green first run); cafe-ledger 0.6.0 refreshed live, `bin/cafe-app`
cycled. Live: the $10 / 39-day seed debtor written off (`.entry.reason = waiver`, balance 0, episode closed);
Riley Chen's $35.75 paid out (`reason: payout`, balance 0, `cashCents` computed from her 15-entry history, the
credit lease left the desk grid); a second payout refused `NoCreditToPayOut`; a refund of the payout refused.
The desk renders Write off on every debtor row (front-of-house sign-in); the button's own dispatch was not
driven in-browser — the native `confirm()` blocks the automation — so the FE path is proven by the descriptor
tests + the identical `renderOpForm` shape of the payment form, not by a click. Brief deviations: the design's
empty-`contextHint` test is an empty-`optionalReads` submit (dropping `Reads` fails earlier at `vertex_alive`);
a refund against a legacy account now replays and mints `.balance` so the cash floor binds there too (a >500-
entry legacy history is refused fail-closed on the refund leg where it posted before). Cold review (opus) found
one BLOCKING and one composition hole, both closed in the fire and both **design-gap** class: (B1) a payout is a
debit, so the refund cap written for charges admitted it — an unbounded cash loop; (S1) waiver → refund → payout
handed out cash for a charge nobody paid — the design's grounding premise ("a refund of an unpaid charge moves
no cash") stopped holding the moment a second clearing verb existed. Both appended to the `_packages.md`
dossier. The FE review found one wording defect (a definitive rejection toasted as "may have landed"), fixed
by marking rejected replies on the error.
