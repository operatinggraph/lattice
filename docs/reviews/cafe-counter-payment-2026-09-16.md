# Café — "Paying at the counter is refused until the Weaver posts" (2026-09-16)

**Filed (PO, 2026-09-16):** `Settle` posts the debit asynchronously; the desk's Record payment (Resident view) is
capped at the ledger balance, so `Settle` → `CreditCafeAccount` for the tab total is `PaymentExceedsBalance` for
~9 s live (indefinitely on a paused projection). No one-act settle-and-pay on the tab card. Winston-adjudicated
(implementation-level; no contract surface, no fork).

## Grounding

- **The cash is real before the debit is.** `Settle` closes the tab and writes nothing to the ledger
  ([ddls.go:1515-1610](../../packages/cafe-domain/ddls.go)); the `cafeTabSettlement` lens opens `missing_account`
  then `missing_charge` on the settled row and the Weaver posts `DebitAccount{…, tabRef}` ~9 s later
  ([lenses.go:348-385](../../packages/cafe-domain/lenses.go), [targets.go:61-83](../../packages/cafe-domain/targets.go)).
  `post_entry`'s payment cap reads the live `.balance` ([scripts.go:1510-1519](../../packages/cafe-ledger/scripts.go)),
  so a counter payment keyed before the posting is refused for money the resident has already handed over.
- **A cross-package posting is the playbook's job, never the op's (P2).** `Settle` cannot post the credit itself
  — cafe-domain writes no cafe-ledger vertex — and the shipped mechanism for "this settled tab owes the ledger an
  entry" is a lens gap the Weaver dispatches: `missing_charge → directOp(DebitAccount)`. A payment recorded at
  settle is the same shape one hop later: a fact on the tab, a gap that opens once the debit is posted, a
  `directOp(CreditCafeAccount)`. The ordering the cap needs falls out of the gap's own conjunct (the credit gap
  requires the `settles` link the debit writes).
- **`tabRef` is the chain of custody the lens reads.** `DebitAccount` alone accepts it and writes
  `lnk.cafetransaction.<tx>.settles.tab.<tab>` ([scripts.go:1553-1560, 1740-1748](../../packages/cafe-ledger/scripts.go));
  `CreditCafeAccount` refuses nothing but never reads it ([scripts.go:1892-1906](../../packages/cafe-ledger/scripts.go)).
  A payment that names the tab it pays for writes the **same `settles` link** (sentence test: *transaction settles
  tab* reads for cash as for a charge) and the lens tells the two apart by `.entry.type` with the shipped
  `count(CASE WHEN … THEN tx.key ELSE null END)` idiom ([edge-manifest lenses.go:901](../../packages/edge-manifest/lenses.go)
  is the `DISTINCT` form; here the count is deliberately plain — `cl`/`ol` bind at most one row each so `tx` never
  multiplies, and `DISTINCT` would move the lens into the branch-decomposing population with its own differential).
  A second relation name would push the lens's relation-narrowed Core KV filter over
  `MaxNarrowedFilterSubjects` (3 labels × (1 + 2×4) = 27 > 24, [subjects.go:226](../../internal/refractor/subjects/subjects.go))
  and demote it to label-narrowed delivery — every link event on three labels, skipped client-side (the
  2026-09-16 build's corpus census caught it; the discriminator keeps R = 3, 21 subjects).
- **Weaver `Params` take literals.** An unprefixed value is a plain string literal
  ([strategist.go:51-66](../../internal/weaver/strategist.go)), so `memo` and `reason` need no lens column; a
  `row.<col>` value must be non-null on every violating row (the `_packages.md` dossier's *a dispatch declaration
  must name what the runtime actually binds*) — the gap's own conjunct carries `paidAtSettleCents > 0`.
- **Who may say cash was taken.** `CreditCafeAccount` grants `operator` + `frontOfHouse` at `scope=any` and
  `consumer` at `scope=self` ([permissions.go:78-88](../../packages/cafe-ledger/permissions.go)); `Settle`'s self
  leg is the resident (`op.authContextTarget != ""`, [ddls.go:1548](../../packages/cafe-domain/ddls.go)). A resident
  settling their own tab from their phone hands over no cash — `paidCents` on the self leg is `AuthDenied`, the
  `waiver`-is-staff-only shape.
- **The desk's two Settle surfaces + the resident's one** — POS card `settle-btn`
  ([app.js:1079-1092, 1152](../../cmd/cafe-app/web/app.js)), Front Desk per-tab `settle-<key>`
  ([app.js:1442-1460, 1723](../../cmd/cafe-app/web/app.js)), resident `resident-settle-btn`
  ([app.js:2038, 2381](../../cmd/cafe-app/web/app.js)); Facet's descriptor ([opmetas.go:235-283](../../packages/cafe-domain/opmetas.go)).
  `Settle` is governed by `lint-refusal-courtesy` (4 sites), so a new state code declares at every one.
- **The arrears row's sibling.** `renderFrontDeskArrears` draws Write off / Pay out per row and
  `wireArrearsActions` delegates both ([app.js:1518-1580](../../cmd/cafe-app/web/app.js)); `handleWriteOffDebt` is a
  confirmed, detached-descriptor-mount `CreditCafeAccount{reason: waiver}` prefilled to the balance
  ([app.js:1581-1630](../../cmd/cafe-app/web/app.js)) — *Take payment* is the same function with `reason: payment`.
- **Gates this fire trips:** `lint-refusal-courtesy` (new `Settle` code at 3 JS sites + Facet); the read-drift
  baseline gains `Settle`'s four confinement-walk rows (the op always had the walk; the first non-operator
  `Settle` vector is what exercises it — the `Charge` 2026-09-13 shape); `lint-package-version` on both packages; no
  `verify-package-cafe-*.go` exists for either package, and no `PermittedCommands` list changes.

## Verdict — *the desk settles and takes the cash in one act; the ledger records both, debit first*

1. **`Settle{tabKey, paidCents?}`** — optional integer cents, staff legs only. Refused `AuthDenied` on the self leg
   (`op.authContextTarget != ""`); `InvalidArgument` unless a positive whole number.
   **`paidCents` must equal `totalCents`** — `PaidMismatchesTab` either way: the desk claims it took
   the total the card showed, and a card gone stale (a self-order or a void since render) refuses and re-renders
   rather than silently recording a partial. Not in Facet's self-voice descriptor (the `Charge`/`amountCents` precedent — a resident never sends
   it). Recorded on `.status` as `paidAtSettleCents` + `paidAtSettleBy` (= `op.actor`, the staffer who took
   the cash) — read as *cash the desk took at settle*, absent on every other settle and on `SettleStaleTab`.
2. **`missing_payment`** on `cafeTabSettlement`: settled ∧ `totalCents > 0` ∧ account exists ∧ a **debit** `settles`
   the tab ∧ `paidAtSettleCents > 0` ∧ no **credit** `settles` it yet (`txCount` / `payCount` split on `.entry.type`). Weaver dispatches
   `CreditCafeAccount{accountKey: row.accountKey, amountCents: row.paidAtSettleCents, memo: "Paid at the counter",
   reason: "payment", tabRef: row.tabKey}` (class `cafetransaction`, Reads `row.accountKey`, `row.tabKey`,
   OptionalReads `.balance` + `.arrears` as `missing_charge`). Never live with `missing_charge`: the debit conjunct
   orders them.
3. **`CreditCafeAccount` accepts `tabRef`** on its staff legs and writes the same `settles` link a charge does. A
   tab-tied credit is **bounded by the tab's recorded fact, not by the balance cap**: it must be a `payment` (a
   `waiver` with `tabRef` is `InvalidArgument`), the tab must be settled with a `paidAtSettleCents` the amount
   equals (`TabNotSettled` / `NoCounterPayment` / `CounterPaymentMismatch`), the tab's lease must be the account's
   own `heldFor` lease (`AuthDenied` — a staffer cannot close another lease's gap), and at most one counter payment
   posts per tab (`CounterPaymentAlreadyPosted`, off the tab's inbound `settles` links). With those holding, the
   `NoBalanceToPay` / `PaymentExceedsBalance` cap does not apply: the cash is real and already capped at `Settle`
   (`paidCents = totalCents`), so an account in credit legitimately goes further into credit, its `cashCents`
   rising with the payment and a later payout staying within cash paid in. (The cold review's F1: with the cap
   applied, a resident holding a refund credit made the desk's primary button park their cash forever,
   deterministically; F2/F3 were the waiver and cross-lease holes.) A self-scoped submit carrying `tabRef` is
   `AuthDenied`. `RefundCafeCharge` / `PayoutCafeCredit` still refuse the field. A `DebitAccount{tabRef}` keeps
   the precedent's alive-only check — its grant is operator-only, so no staff leg reaches it.
4. **The desk's one act.** Beside *Settle Tab* on the POS card and *Settle* on the Front Desk tab card: **Settle &
   pay $X** (X = the card's `totalCents`), submitting `Settle{paidCents: totalCents}`; hidden at `$0`. A settled tab
   carries the payment on every surface that shows it (the resident's *Pending posting* panel, the Today panel) as
   *paid $X at the counter*.
5. **Take payment on the arrears row** (the sibling row, its own commit): beside *Write off*, a confirmed
   `CreditCafeAccount{reason: payment, memo: "Paid at the desk"}` prefilled to the balance shown.
6. cafe-domain `0.16.0 → 0.17.0`; cafe-ledger `0.6.3 → 0.7.0`; READMEs.

### Fire brief (build note, 2026-09-16)

1. **Scope** (board row, verbatim): *Settle posts the debit asynchronously; the desk's Record payment (Resident
   view) is capped at the ledger balance, so `Settle` → `CreditCafeAccount` for the tab total is
   `PaymentExceedsBalance` for ~9 s live (indefinitely on a paused projection). No one-act settle-and-pay on the tab
   card.* Next: *a `paidAtSettle` on the tab the settlement playbook posts as a credit after the debit; Pay now
   beside Settle.* Green bar: a staff `Settle{paidCents}` at the tab's building records `paidAtSettleCents`/`By`;
   refused `AuthDenied` on the self leg, `PaidMismatchesTab` off the total, `InvalidArgument` on a non-integer; the
   lens row opens `missing_payment` only once a `settles` transaction exists and closes it on a credit one; the
   Weaver target dispatches `CreditCafeAccount` with `tabRef` and the credit writes `settles` with `.entry.type = credit`; `RefundCafeCharge` /
   `PayoutCafeCredit` still refuse `tabRef`; the desk's button submits `paidCents = totalCents`; live: a tab settled
   with `paidCents` lands a debit then a credit, balance unchanged, one `settles` link per entry.
2. **Touch-list (verified live):** cafe-domain `ddls.go:270-284` (`.status` schema + field docs), `:1515-1610`
   (`Settle`), `:200-240` (examples), `opmetas.go:235-283` (descriptor: InputSchema, `(facet)` line),
   `lenses.go:57` (BodyColumns), `:286-385` (`tabSettlementSpec` + comment), `targets.go:36-84`, `package.go`
   `Version`, `manifest.yaml:2`, `README.md`, `lens_cypher_test.go:220-261` (gap pins → mirror),
   `integration_test.go` (Settle vectors → mirror), `workplace_confinement_test.go` (a frontOfHouse `Settle{paidCents}`
   vector), `playbook_test.go` (target gap census) · cafe-ledger `scripts.go:1305-1330, 1553-1560, 1740-1748,
   1881-1906`, `ddls.go:330, 355-358, 392, 402, 415-420` (every "DebitAccount only" tabRef sentence), `opmetas.go`
   (CreditCafeAccount InputSchema gains `tabRef`? — NO: the field is Weaver-only, undocumented to Facet, like
   DebitAccount's), `package.go` `Version`, `manifest.yaml:2`, `README.md`, `ledger_test.go` (tabRef vectors →
   mirror the DebitAccount tabRef one; add the refund/payout refusal pins if absent) · cafe-app `tabs.go:50-134`
   (struct + normalization), `tabs_test.go`, `web/app.js:932, 1079-1092, 1152` (POS), `:1371, 1442-1460, 1723`
   (Front Desk card), `:1990, 2038, 2100-2112, 2381` (resident — courtesy `unreachable`), `:1179-1200` (Today panel),
   `:1518-1630` (arrears row + `handleWriteOffDebt` → mirror as `handleTakePayment`), a goja pin
   (`web_counter_payment_test.go`, mirror `web_receipt_test.go`).
3. **Precedents:** `missing_charge` (lens gap + target + `lens_cypher_test` pins); `DebitAccount`'s `tabRef` block
   + `settles` link (the credit writes the same link); `waiver` staff-only refusal (the self-leg `AuthDenied`);
   `orderedAt`/`servedBy` for the stamp idiom; `handleWriteOffDebt` for the arrears button; `renderOpenTabCard`
   `settle-btn` for the desk button.
4. **Increments:** (1) packages — cafe-ledger credit `tabRef` + docs + version + vectors; cafe-domain `Settle{paidCents}`
   + `.status` fields + descriptor + lens column/gap + target gap + version + README + vectors (self-leg refusal,
   frontOfHouse acceptance, mismatch, integer, lens pins: open→no gap, settled-unpaid→no payment gap, settled-paid-no-debit→
   `missing_charge` only, debit posted→`missing_payment`, credit `settles`→converged; target gap census). Green:
   `go test ./packages/cafe-domain/ ./packages/cafe-ledger/ -count=1`. (2) app — `paidAtSettleCents` through
   `/api/tabs`, *Settle & pay* on both desk cards, *paid at the counter* on the pending/Today surfaces, courtesy
   lines at all 3 JS sites; goja pin. Green: `go test ./cmd/cafe-app/ -count=1`, every `STRICT=1 scripts/lint-*.go`,
   `golangci-lint`, `make vet`. (3) *Take payment* — its own commit. (4) live: `make refresh-cafe`, `make
   reinstall-package PKG=cafe-ledger`, cycle `bin/cafe-app`; as Dana Whitfield settle a tab with `paidCents`, watch
   the debit then the credit post, balance unchanged.
5. **Gotchas:** both version bumps; `lint-refusal-courtesy` — `PaidMismatchesTab` at every Settle site (`cap` on the
   two desk sites, `unreachable` on the resident site and on Facet, whose self-voice schema omits the field); the dossier: *a dispatch declaration must name what the runtime actually binds* (the Params
   column is non-null under the gap's own conjunct) · *a mirror that drops one of the precedent's checks drops the
   invariant* (`tabRef` on the credit keeps the alive check; `RefundCafeCharge`/`PayoutCafeCredit` keep the refusal)
   · *the "leg" of a guard is every op that WRITES the guarded value* (`Settle` is the only writer of
   `paidAtSettleCents`; `SettleStaleTab` never writes it — pin it) · *a consumer's exclusion is grounded in the
   predicate that enforces it* (`missing_payment` ∧ `missing_charge` never both live: the `txCount > 0` conjunct
   is `missing_charge`'s own closing predicate, verbatim) · *a recorded value is read as the fact it records*
   (`paidAtSettleCents` = cash taken at settle; absent = none, never "unpaid") · standing #3 (revert-prove: drop the
   `txCount > 0` conjunct and watch the ordering pin fail; drop the self-leg refusal and watch its vector fail).
6. **Adjacent finds:** *The arrears row forgives a debt but cannot collect it* (★★ S, its own row) — absorbed as
   this batch's next unit, its own commit.
7. **Non-goals:** a partial counter payment (`paidCents` must equal the total; Record payment on the Resident
   view remains the partial path); a resident self-payment at settle; projecting the payment's `tabKey` on
   `cafeLedgerHistory` (no reader — `receiptLines` joins the debit); a tab limit (its own row).

### Build note (2026-09-16)

Shipped `1144aec5` (merge `0b3cc89a`); brief `62649fc5`. Both rows (the counter payment and the arrears *Take
payment*) in the one commit. Live on the shared stack (cafe-domain 0.17.0 + cafe-ledger 0.7.0 diff-applied,
`bin/cafe-app` cycled): Riley Chen opened a tab and self-ordered a Croissant through the Gateway; Dana
Whitfield's `Settle{paidCents: 1}` was refused `PaidMismatchesTab: the counter payment of $0.01 does not match
the tab total of $3.50`; `Settle{paidCents: 350}` landed and within 5 s the ledger held the debit ($3.50,
`Croissant`) and the credit ($3.50, `payment`, `Paid at the counter`), Riley's balance unchanged at $19.49,
the tab row `paidAtSettleCents: 350, posted: true`; the Front Desk rendered *of which $3.50 taken at the
counter* on the Today panel and *Take payment* beside *Write off* on all six arrears rows (the confirm dialogs
were not driven). CI: `unit-4` failed on `internal/substrate`'s `TestKVMarkerProvenance_ExpiryIsTheOnlyMaxAgeMarker`
(untouched by this fire, green locally in 12.6 s, 31 s on the runner; the same test reddened the previous café
fire's first run — a `/whetstone` chip is filed) and was rerun.

**Deviations from the brief.** (1) The credit writes `settles`, not a `paysFor` relation — the corpus census
caught the subject-budget demotion (Grounding). (2) `count(CASE …)` is deliberately non-`DISTINCT`
(Grounding). (3) `paidCents` is not in Facet's self-voice descriptor. (4) The read-drift baseline gained
`Settle`'s four confinement-walk rows — the first non-operator `Settle` vector.

**Close-pass classification.** Cold review round 1: F1 in-credit park (design-gap — the playbook-dispatched leg
inherited the human leg's cap), F2 waiver+`tabRef` and F3 cross-lease `tabRef` (implementation-bug /
design-gap — the `<x>Ref` mirror dropped the tie, as its debit precedent had), F4 `.entry` dependency (doc),
F5 `Posted` ignoring the new gap (implementation-bug — a derived FE boolean over the lens's gap columns), F6
stale-total silent partial (design-gap — the FE promise vs the op's predicate), F7/F9 convention, F8
brief-gap. Round 2: N1 discarded cursor and N2 dead `e.rejected` (both fixed inline). All fixed before merge;
dossier sightings appended to `_packages.md` (the "leg" class: a *dispatcher* is a leg; the mirror class: an
`<x>Ref` tie) and `vertical-apps.md` (the FE-promise class: a derived boolean over gap columns).
