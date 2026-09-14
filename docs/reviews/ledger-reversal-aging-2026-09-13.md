# Wellness + Café — "A reversal is aged against the wrong charge" (2026-09-13)

**Filed (PO, 2026-09-13):** `deriveStatement` FIFOs every credit
([wellness ledger.go:211](../../cmd/wellness-app/ledger.go), [café ledger.go:208](../../cmd/cafe-app/ledger.go)),
so a refund that `reverses` a NAMED debit pays off the OLDEST open one instead; the wellness lens drops the
`reverses` walk. Live: Alex Kim's $15 "Forfeit Proof Flow" charge (Sep 13) reads due Sep 29 because the Sep 14
refund of a *different* class (Evening Flow, cancelled) paid it — the real head is the Sep 13 charge.
Winston-adjudicated (implementation-level; no contract surface, no fork).

## Grounding

- **Only two verticals age a balance.** `deriveStatement` exists in `cmd/wellness-app` and `cmd/cafe-app`
  alone; `cmd/clinic-app` and `cmd/loftspace-app` carry no due-date / overdue logic (`grep -rn overdue
  cmd/clinic-app cmd/loftspace-app` → none). The row's "same shape in clinic/loftspace" is false — nothing to
  fix there.
- **Café already projects the target and ignores it.** `cafeLedgerHistory` walks `(t)-[:reverses]->(rt)` and
  projects `reversesKey` ([lenses.go:110-121](../../packages/cafe-ledger/lenses.go)); the app carries it to the FE
  (`ReversesKey`, [ledger.go:23,39](../../cmd/cafe-app/ledger.go)), which renders "reverses the charge of …"
  ([app.js:1515-1536](../../cmd/cafe-app/web/app.js)) — but `deriveStatement` reads only `Type`/`PostedAt`.
- **Café's due fact is written by an op that reproduces the same FIFO — lockstep.** `arrears_head`
  ([scripts.go:187](../../packages/cafe-ledger/scripts.go)) is documented as *"the FIFO the resident's own statement
  runs, reproduced exactly"*; `EvaluateCafeArrears` recomputes `.arrears.dueAt` from it whenever a credit marks
  the episode `stale` ([scripts.go:1458-1467](../../packages/cafe-ledger/scripts.go)). A refund is exactly such a
  credit. Changing the app's aging without the op's re-opens the seam the lockstep exists to close: the desk
  would show one due date and the reminder would fire on another. `arrears_entries` reads each entry's `.entry`
  under an `(e)` follow-up ([scripts.go:154-185](../../packages/cafe-ledger/scripts.go)); the refund's `.entry` records
  no target (the link *is* the refund's identity, [scripts.go:1484-1494](../../packages/cafe-ledger/scripts.go)),
  while the reversed charge's `.entry.refundedCents` records the aggregate.
- **Wellness's target is one hop further.** A refund credit links `settlesRefund` → `wellnessrefund` marker; the
  marker links `reverses` → the charge, written unconditionally at every mint site (`CancelBooking` ×2,
  `ReleaseOrphanedBooking` ×2 — [ddls.go:4841, 5093, 5231, 5283](../../packages/wellness-domain/ddls.go)). A wellness
  **waiver** names no charge (the desk's Waive is an amount, [app.js:3541](../../cmd/wellness-app/web/app.js); live: 16
  waivers, all untargeted) — it stays an ordinary FIFO credit.
- **Wellness's FIFO is the older, narrower copy.** Café's `deriveStatement` carries a credit posted ahead of any
  open debit forward as `surplus` (the Riley Chen fix, `TestDeriveStatement_PrepaidCreditCarriesForward`);
  wellness's drops it, so a prepaying member's next charge ages as if unpaid. Same file, same fix — absorbed.
- **A cancelled class's charge loses its name.** `CancelBooking` tombstones the booking in the batch that mints
  the marker, so the charge row's `settlesClassPrice` walk stops matching and `className` nulls (live: the Sep 14
  "Evening Flow with Sam" debit projects `className: null` while its refund names the class). The marker that
  reverses the charge carries the snapshot. Same lens edit — absorbed.

## Verdict — *a credit that names its charge retires that charge; everything else queues*

One netting rule, written three times in lockstep (two Go, one Starlark), and one lens column.

1. **Netting.** Rows in `(postedAt, transactionKey)` order (unchanged). Pre-pass, in that order: for each credit
   carrying `reversesKey`, `absorbed = min(credit, debitAmount[reversesKey] − absorbedSoFar[reversesKey])`,
   accumulate both. Walk: a debit opens with `amount − absorbedTotal[key]` (zero → it never opens; the surplus
   prepay applies to what remains, as today); a credit joins the FIFO with `amount − absorbed[creditKey]`
   (its unabsorbed excess — a target already retired by an earlier payment, or a target that is not one of this
   account's debits — flows through the ordinary path, so no money is ever dropped). Balance is untouched (still
   `Σdebit − Σcredit`); only which charge the balance is *aged from* changes.
2. **Wellness lens.** `ledgerHistorySpec` gains `OPTIONAL MATCH (rf)-[:reverses]->(rtx:wellnesstransaction)` →
   `rtx.key AS reversesKey`, and `OPTIONAL MATCH (t)<-[:reverses]-(rrf:wellnessrefund)` so the charge row's
   `className`/`classStartsAt` coalesce last onto `rrf.detail` (the marker's snapshot survives the tombstone —
   the same reason `rf.detail` is read on the credit row). Bump 0.2.21 → 0.2.22.
3. **Café op-side.** `arrears_entries` follows `kv.Links(tx, "reverses", "out")` for credit entries only (a refund
   carries exactly one; a payment none) and stashes `reversesKey`; `arrears_head` nets by rule 1. Bump the
   package. `.entry` shape unchanged — no `reason` field this fire (that is the write-off row's).
4. **Apps.** Both `deriveStatement`s implement rule 1; wellness gains `ReversesKey` on projection + row and
   café's surplus carry. Wellness FE: a credit with `reversesKey` mirrors café's "reverses the charge of <date>"
   line at both render sites.

**Alternatives.** *Delete the FIFO and age from the newest charge* — wrong the other way (a weeks-old unpaid charge
would read as fresh). *Record `reversesKey` in the refund's `.entry`* — a relationship stored as a key in an aspect
(Contract #1; the CLAUDE.md paper-over), and the four live café refunds would still lack it; the link is already
the record. *Net by `refundedCents` on the charge alone* — identifies the retired amount but not which credits are
refunds, so they would double-count in the FIFO. *Let `EvaluateCafeArrears` read the app* — P5 backwards.

**Contract surface:** none. **Test strategy:** a `deriveStatement` table in each app (the Alex Kim shape: refund
of the NEWER charge leaves the OLDER one as the head; excess over a retired target falls through; untargeted
credits FIFO as before; wellness gains café's prepay cases); a cafe-ledger pipeline test
(`TestArrears_EvaluateNetsARefundAgainstItsCharge`: two charges, refund the newer, `dueAt` stays the older's);
wellness `lens_cypher_test` pins for `reversesKey` on the credit row and `className` on a charge whose booking is
tombstoned but whose marker reverses it. Live: Alex Kim's due date moves from Sep 29 to Sep 28 (Sep 13 + 15).

**Size S (pkg ×2 + lens + app ×2) · Winston-adjudicated.**

### Fire brief (build note, 2026-09-13)

1. **Scope** — the row's next step, verbatim: *"project the reversed key on the credit row; net reversals by
   target, FIFO only payments"*, built as rules 1–4 above. Green bar: the test strategy's pins + the live proof.
2. **Touch-list (verified live at selection):** `cmd/wellness-app/ledger.go` — `ledgerEntryProjection` `:30-42`,
   `ledgerEntryRow` `:49-58`, `computeLedgerHistory` `:75-106`, `computeLedgerBalances` `:156-198`,
   `deriveStatement` `:211-249`; `cmd/wellness-app/ledger_test.go` (no `deriveStatement` table today; fixture =
   `putJSON` into `LedgerHistoryBucket`, `:369-395`); `cmd/wellness-app/web/app.js` `:1136-1143`, `:2861-2866`.
   `cmd/cafe-app/ledger.go` — `deriveStatement` `:208-257`; `cmd/cafe-app/ledger_test.go` `:104-248`.
   `packages/wellness-ledger/lenses.go` — `ledgerHistorySpec` `:383-401` + its comment `:340-382`;
   `lens_cypher_test.go` `:572-707`; `manifest.yaml:2` + `package.go:103` (0.2.21 → 0.2.22).
   `packages/cafe-ledger/scripts.go` — `arrears_entries` `:154-185`, `arrears_head` `:187-238`;
   `ledger_test.go` `:2385-2428` (the rearm precedent + `debitAt`/`creditAt`/`refundAs` helpers `:566-620`);
   `manifest.yaml:2` + `package.go:114` (0.5.1 → 0.5.2).
3. **Precedents:** café `deriveStatement`'s surplus walk (`:217-241`) — the wellness copy converges on it;
   `cafeLedgerHistory`'s `reverses` walk + `reversesKey` column ([lenses.go:110-121](../../packages/cafe-ledger/lenses.go));
   `rf.detail` coalesce for a snapshot that survives a tombstone ([wellness lenses.go:396-401](../../packages/wellness-ledger/lenses.go));
   the `(e)` follow-up `kv.Links` idiom in `reversed_charge` ([scripts.go:1118-1127](../../packages/cafe-ledger/scripts.go));
   café FE's `rowByKey` + "reverses the charge of" ([app.js:1515-1536](../../cmd/cafe-app/web/app.js)).
4. **Increments:** (1) both apps' `deriveStatement` + tables — `go test ./cmd/wellness-app/ ./cmd/cafe-app/ -count=1`;
   (2) wellness lens + pins + bump — `go test ./packages/wellness-ledger/ -count=1`; (3) café op-side + pipeline
   test + bump — `go test ./packages/cafe-ledger/ -count=1`; (4) wellness FE + `node --check` + `make lint-web`;
   then `go build ./... && make vet && golangci-lint run ./... && STRICT=1 go run ./scripts/lint-conventions.go &&
   DIFF_BASE=<base> go run ./scripts/lint-package-version.go`; live: `make refresh-wellness refresh-cafe` from the
   main checkout, cycle `bin/wellness-app` + `bin/cafe-app`, read Alex Kim's due date.
5. **Gotchas:** three copies of one rule — the lockstep is the deliverable (a table case ported verbatim to all
   three); both package versions bump; `# read-posture: (e)` on the new `kv.Links`; the op's page budget already
   degrades on `history_too_long` — the extra Links per credit stays inside it; revert-proofs run in the worktree.
   `_packages.md` dossier: *a lens that reads a RECORDED fact depends on whoever arms the timer that records it*
   (the recorded `dueAt` is what this fire re-derives — the stale→Evaluate path is the re-arm); *a cap derived
   from a paged sweep must be summed over every ARM* (n/a — no cap). `vertical-apps.md` dossier: *a count the FE
   promises must apply the op's own predicate* (the desk's due date IS the op's — lockstep); *a value reaches markup
   unescaped because the escaper's callers were never censused* (the new FE line renders a date, through the
   existing `textContent` path). Standing checklist: #1 no new state; #2 censuses re-run live (2 aging apps,
   4 mint sites, 16 untargeted waivers, 1 live wellness refund, 4 live café refunds); #3 each table case
   reverted-proven; #4 nothing removed; #5 no new key; #6 café's FIFO verified against its own op copy.
6. **Adjacent finds:** wellness drops a prepaid credit (absorbed, inc 1); a cancelled class's charge loses its
   name (absorbed, inc 2); the row's clinic/loftspace claim (recorded above, nothing to build).
7. **Non-goals:** no `reason` on café entries; no targeted waiver op; no clinic/loftspace edit; no change to
   balance arithmetic or the episode/notification rules; no new bucket.
