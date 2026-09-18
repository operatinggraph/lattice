# LoftSpace ledger — a reversal retires the charge it names, and lateness costs what the lease says (2026-09-18)

**Filed (PO, 2026-09-18, `bf4d4bef`), two rows built as one fire:**

- *"A reversal is aged like a payment"* ★★ M, pkg — "The ledger names no `reverses` relation (`scripts.go:437`, README
  'not yet needed'), so `arrears_head` retires the OLDEST charge with a correction credit. Live: Jordan's 09-05 charge
  reversed 09-13 leaves his 08-06 rent unpaid, yet the statement and the landlord's Rent-owed read 13 days overdue, not
  43. clinic-ledger's `reverses` walk is the precedent."
- *"A late fee is a memo on a manual charge"* ★★ M, pkg + FE — "`LoftspaceRecordCharge{memo: "Late fee"}` is the whole
  mechanism (`permissions.go:156`); the clause `purpose` token admits `lateFee` and nothing reads it
  (`semantic-contracts/scripts.go:87`). Live: Riley 48 days overdue, reminded once, charged $0. A `purpose=lateFee` clause
  billed once per arrears episode."

One fire: both rows are the arrears mechanism of `loftspace-ledger` — the first makes the head honest, the second bills
against that head, and a fee billed on a wrong head is a wrong fee. Winston-adjudicated: every mechanism mirrors a
shipped pattern (clinic-ledger's `reversesRef` + netting pre-pass; the `.deposit` → `missing_deposit` → purpose-marked
clause chain in lease-signing + semantic-contracts; the arrears op's own once-per-episode `sentAt`; lease-signing's
`require_manages` self leg). No contract surface. Co-applicant (★★ M) is the next LoftSpace row, not a lesser one — a
separate spine (lease-signing identity minting), picked after this pair by cohesion.

## Grounding

- **Live (2026-09-18, `loftspace-ledger-history` + `weaver-targets`):** Jordan Ellis's account `vtx.account.mcZN2ghvtcB5yhafYiwi`
  — 08-06 debit $2,050 (`kKy4mJvX`) · 09-04 credit $500 · 09-05 debit $2,050 (`f3herm2Z`) · 09-13 credit $2,050 memo
  *"Reversal: rent billed 2026-09-05 for a period after the 2026-09-06 lease end"* (`sQ9sTvaP`, no link) · 09-13 debit
  $2,125 (renewed rent). `.arrears` reads `dueAt 2026-09-05T17:01:51Z`, `remindedFor` = it, `sentAt 09-16`. FIFO with no
  netting retires the 08-06 remainder ($1,550) with the reversal; netted, the 09-05 charge is what it retires and the head is
  08-06. Riley Chen `vtx.account.F6yNkE6jZiPooy6nbxrQ`: 08-01 debit $1,900 open, `sentAt 09-16`, no fee — her lease carries
  no fee term, and under this design none bills until one is set. Seven accounts carry `.arrears`; none carries `replay`.
- **The netting precedent** — `packages/clinic-ledger/scripts.go:1375-1394` (`reversesRef` validated: a live debit posted to
  THIS account, refused on a debit and on the consumer self leg), `:1574-1575` (the `reverses` link written in `post_entry`'s
  batch), `:1674-1679` (`derive_reads` rebuilds the target's `postedTo` link key — class (g)), `:253-266` (`arrears_entries`'
  per-credit `kv.Links(credit, "reverses", "out", None, 1)` — `(e)`, limit 1), `:360-377` (`arrears_head`'s pre-pass: absorbed
  per credit = min(credit, target face − already absorbed); the FIFO then retires the named debit by the absorbed amount and
  the remainder is a plain credit). Lens: `clinic-ledger/lenses.go:227` projects `reversesKey`; app: `cmd/clinic-app/ledger.go:177-250`
  is the Go mirror of the pre-pass; `web/app.js:4053` renders *reverses the charge of …*. Tests to mirror:
  `TestCreditAccount_ReversesRefWritesReversesLink`, `_UnknownReversesRefRejected`, `TestDebitAccount_ReversesRefRejected`,
  `_ConsumerSelfScope_RejectedReversesRef`, `_ReversesRefOtherAccountRejected`, `TestArrears_EvaluateNetsAReversalAgainstItsCharge`,
  `TestArrears_ReversalOnAnEarlierPageNetsItsCharge`, `TestClinicLedgerHistory_Reverses_ProjectsReversesKey`.
- **This ledger today** — `packages/loftspace-ledger/scripts.go:425-458` (the fold's comment asserting "no reverses relation
  to walk"), `:460-537` `arrears_entries` (checkpoint `entries[txId] = {postedAt, type, amountCents, dueAt}`), `:552-701`
  `arrears_head` (the FIFO the statement runs, `episodeStart` tracked), `:861` the `historyTooLong` carry list, `:925-1057`
  the send branch (`sentAt` stamped once per episode, dropped when `sent_at < episode_start`), `:1580` `post_entry`
  (`allow_clause_ref` — the clause-authorized debit's `authorizedBy` link), `:2386-2395` `derive_reads`. `CreditAccount`:
  `opmetas.go:120-151` (no `reversesRef`), `permissions.go:142-152` (operator `any` + consumer `self`), `ddls.go:303-415`.
  `ledgerHistorySpec` `lenses.go:281-300` walks no `reverses`; `leaseAccountsSpec` `:316-324` projects the three arrears
  columns. README `:195-199` "a dedicated reversal op is not yet needed". App: `cmd/loftspace-app/ledger.go:69`
  `computeLedgerHistory`, `:310` `deriveRentArrears` (the Go FIFO, no netting), `:432` `handleLedger`; `web/app.js:4350-4402`
  `renderLedgerRecordForm` (the landlord's charge form; the payment form beside it).
- **The clause vocabulary** — `semantic-contracts/scripts.go:173-340` `mint_clause` (`period ∈ {oneTime, monthly}` `:195-199`;
  `purpose` token `:87-109`; the deposit guard `:251-252` — purpose=deposit ⇒ oneTime computational; `.status {state:
  active}` `:314`), `:352-425` `SupersedeClause` (inherits purpose; tombstones the amended clause, links `amends`).
  `lenses.go:436-485` `clauseSatisfactionSpec` — `missing_charge` bills `(period <> 'monthly') AND chargeCount = 0` once, and
  monthly on the term grid: **any new period token is billed once at mint by that arm as written.** `leaseRentSettlement`
  `lenses.go:280-320` (`depositClauseCount`/`depositClauseKey` over the inbound `governs` fan; `missing_deposit` `targets.go:214-226`
  mints `CreateClause{purpose: deposit, period: oneTime, amountCents: row.depositCents}` from the lease's `.deposit`).
  `CreateClause`/`SupersedeClause` are operator-only (`permissions.go:84-99`); `cmd/loftspace-app` submits neither.
- **The fee-term precedent** — `SetListing{depositAmount}` (loftspace-domain, the landlord's self leg) → `DecideLeaseApplication`
  stamps `.deposit {amount, recordedAt}` create-only at approval (`lease-signing/ddls.go:896-932`) → the gap mints the clause →
  `ReturnDeposit` at tenancy end. `require_manages` (`lease-signing/scripts.go:573-613`) is the landlord self-leg probe;
  `DecideLeaseApplication`'s `{Scope: self, GrantsTo: [consumer]}` row `permissions.go:143-147`. Read models project
  `depositAmount` (`lenses.go:1532, 1579, 1706, 1750`); the app's `applications.go:64` / `landlord_applications.go:94` wire it,
  `app.js:2338, 5469` render it. `scripts/verify-package-lease-signing.go:131` pins the DDLs' `PermittedCommands`.
- **Op-name census:** `CreditAccount` is claimed by loftspace-ledger alone (clinic/wellness/café prefix theirs);
  `LinkReversal`, `SetLateFee` claimed by nobody. No `scripts/verify-package-{loftspace-ledger,semantic-contracts}.go`.

## Decisions (Winston)

1. **`CreditAccount` names the charge it reverses.** Optional `reversesRef` (a `vtx.transaction.<id>` key): the target must be
   a live debit posted to the payload account (its `postedTo` link key rebuilt by `derive_reads`, class (g), read as `(d)`),
   `amountCents` ≤ its face (`ReversalExceedsCharge`), refused on the consumer self leg (`AuthDenied` — a tenant records a
   payment, never a reversal) and on `DebitAccount`/`LoftspaceRecordCharge` (`InvalidArgument`). The batch writes
   `lnk.transaction.<creditId>.reverses.transaction.<debitId>` (*credit reverses debit*; the later-arriving vertex is the
   source). `memo` stays optional. Op-meta gains the field; the DDL description names the link.
2. **`LinkReversal{creditKey, reversesRef}`** — operator only (the Weaver/console actor; the FE offers it to nobody): links a
   credit that was posted as a reversal before the relation existed to the charge it corrects. Guards: both live and posted to
   one account, the credit a credit, the target a debit, amount ≤ face, and the credit names nothing yet (`(e)`
   `kv.Links(credit, "reverses", "out", None, 1)` → `AlreadyLinked`). Two writers of one deterministic key, arbitrated by
   population: `CreditAccount` writes it atomically with the credit; `LinkReversal` writes it CreateOnly on a credit that
   has none. It marks `.arrears.stale` the way `post_entry` does (the enumerated set's meaning changed under the head), so
   the evaluation recomputes. Jordan's `sQ9sTvaP → f3herm2Z` is the live instance; the README's "not yet needed" line is
   replaced by the relation.
3. **The fold carries the relation and the head nets it.** `arrears_entries` gains clinic's per-credit `(e)` walk and records
   `reversesId` on the credit's checkpoint entry (identity, not a key-list — the same argument the checkpoint already makes
   for `txId`); `arrears_rows` rebuilds `reversesKey`; `arrears_head` gains the pre-pass verbatim (`absorbed_by_credit`, capped
   at the target's face less what earlier reversals absorbed, in `(postedAt, key)` order), retires the named debit by the
   absorbed amount and hands the remainder to the FIFO as a plain credit; `episodeStart` is unchanged in meaning (a debit
   fully reversed before it was ever open never opens an episode — it is retired at its own position, which is after it;
   so it DOES open one and the reversal closes it, exactly as a same-day payment would). The fold comment at `:425-458` is
   rewritten to describe the walk. `derive_reads` for `CreditAccount` rebuilds the target's `postedTo` key (clinic `:1674`).
4. **The statement and the console net the same way.** `ledgerHistorySpec` gains `OPTIONAL MATCH (t)-[:reverses]->(rt:transaction)`
   and projects `reversesKey`; `computeLedgerHistory` carries it; `deriveRentArrears` gains the Go pre-pass (clinic-app
   `ledger.go:177-250` verbatim, incl. its "stays in `(postedAt, key)` order" comment). The landlord's ledger: each unreversed
   debit row gets **Reverse** → a confirmed `CreditAccount{accountKey, amountCents: <face − already reversed>, memo,
   reversesRef}` through `landlordSubmit()` (`sent`/`confirmed` staging; refusal courtesy for `ReversalExceedsCharge`,
   `UnknownTransaction`, `AuthDenied`); a credit row that names a charge reads *reverses the charge of <local date>* on both
   hats; the tenant's statement and the landlord's Rent-owed read the netted head. goja pins over the wire struct.
5. **The late fee is a term of the lease, recorded by its landlord.** `SetLateFee{leaseAppKey, amountCents}` in lease-signing:
   landlord self leg via `require_manages` on the application's `appliesToUnit` unit (the `DecideLeaseApplication` shape) +
   operator `any`; `amountCents` a positive integer; writes `.lateFee = {amountCents, recordedAt}` on the leaseapp,
   create-or-update OCC on the declared optional read (`{payload.leaseAppKey}.lateFee`); refused on an ended tenancy
   (`TenancyEnded`) and on an undecided application (`NotApproved` — a fee is a term of a lease, not of an offer). No
   removal verb (non-goal); an amendment supersedes the clause (decision 6). `leaseApplicationsRead` /
   `landlordLeaseApplicationsRead` project `late_fee_cents`; the app wires it and both cards read *Late fee $X after 5 days*;
   the landlord's approved-application card gains **Set late fee** (prompt → `SetLateFee` via `landlordSubmit()`).
6. **The settlement lens mints and amends the clause, as it does the deposit.** `leaseRentSettlement` gains
   `lateFeeCents` (= `l.lateFee.data.amountCents`), `lateFeeClauseCount` / `lateFeeClauseKey` / `lateFeeClauseCents` over
   the inbound `governs` fan (`purpose = 'lateFee' AND status.state = 'active'`, the `depositClauseKey` shape). Gaps:
   `missing_lateFeeClause = accountKey <> null AND lateFeeCents <> null AND endedAt = null AND lateFeeClauseCount = 0` →
   `directOp CreateClause{leaseAppKey, accountKey, amountCents: row.lateFeeCents, period: "perArrearsEpisode", purpose:
   "lateFee", prose: <literal>}` (`Reads: [row.leaseAppKey, row.accountKey]`, `missing_deposit` verbatim);
   `missing_lateFeeAmendment = lateFeeClauseKey <> null AND lateFeeCents <> null AND lateFeeClauseCents <> lateFeeCents` →
   `directOp SupersedeClause{clauseKey: row.lateFeeClauseKey, leaseAppKey, accountKey, amountCents: row.lateFeeCents,
   period: "perArrearsEpisode", prose}` (`Reads` the clause, its `.terms`, `.status`, the lease and the account — the
   `missing_termShortened` declaration shape; `Target: row.lateFeeClauseKey`). Both join `violating`.
7. **`perArrearsEpisode` is a clause period, and `clauseSatisfaction` bills only the periods it knows.** `mint_clause` admits
   `period ∈ {oneTime, monthly, perArrearsEpisode}`; `perArrearsEpisode` ⇔ `purpose = lateFee` (both directions, the deposit
   guard's mirror), computational only, no proration, no term. `clauseSatisfactionSpec`'s one-time arm becomes
   `(period = 'oneTime')` in `missing_charge` and `violating` (a lens that bills what it does not understand bills a fee at
   mint), pinned by a `lens_cypher_test` vector for a `perArrearsEpisode` clause with `chargeCount = 0` → `missing_charge`
   FALSE. `SupersedeClause` carries the period through as it does the purpose.
8. **The arrears evaluation bills the fee on the commit that sends the reminder — once per episode.** In the send branch
   (`scripts.go:996`, `data["sentAt"] = evaluated_at`): the op walks the account's inbound `chargesTo` links (`(e)`, bounded —
   the clauses charging this account) and reads each `.terms` + `.status` (follow-ups) for the live `purpose = lateFee` one;
   with one found it posts a debit in the same batch — a fresh `vtx.transaction.<id>` root + `.entry {type: debit,
   amountCents: <clause>, postedAt: evaluated_at, dueAt: evaluated_at, memo: "Late fee — rent due <dueAt date>"}` +
   `postedTo` + `authorizedBy` the clause (the `DebitAccount` clause-authorized posting shape, via `post_entry`'s
   helpers, never a second implementation) — and records `.arrears.lateFeeAt = evaluated_at`. `lateFeeAt` is carried and
   dropped exactly with `sentAt` (the live-episode carry `:1028-1030`, the `historyTooLong` carry `:861`, `post_entry`'s
   carry list, the boundary drop `:964-966`, the no-head drop). The fee is billed only on the send commit: a fee term set
   after the reminder went out applies from the next episode (stated on the landlord's card). The fee debit is not in the
   FIFO this evaluation computed; the head it names is the rent's, and the next lapse or posted entry re-evaluates with the
   fee in the queue — the episode continues (the queue never emptied), so neither a second reminder nor a second fee follows
   when the rent alone is paid and the fee becomes the head. `leaseAccountsSpec` projects `arrearsLateFeeAt`; the landlord's
   Rent-owed row reads *Late fee billed <local date>*; the tenant's statement row shows the clause prose the fee carries.
9. **Non-goals.** No fee on the tenant's self leg or any hat but the evaluation; no listing-level fee (the term is the
   lease's — a legacy lease gets one by `SetLateFee`, which is what Riley's needs); no removal of a fee term; no
   retro-billing of an episode already reminded; no reversal from the tenant's hat; no `CreditAccount` cap on the SUM of
   several reversals (netting caps at face, as clinic); no change to the reminder's grace (5 days serves the fee too); no
   Facet descriptor (`SetLateFee`'s self leg is `standing`-dispatched from loftspace-app, as `SetUnitAddress` is).

## Build

**Scope sentence.** A landlord's reversal names the charge it corrects and the arrears head, the statement and the
Rent-owed read all retire that charge, not the oldest; a landlord records a late-fee term on a lease, the settlement lens
mints its `purpose=lateFee` clause, and the reminder that goes out after the grace bills that fee once per arrears episode.
Green: Jordan's legacy reversal linked → his head reads 08-06 (43 days) on `/api/ledger` and Nora's Rent-owed; a fresh
reversal from Nora's console lands with its link; Riley's lease given a $50 fee → the clause minted by convergence; a
fresh episode past its grace bills the fee on the reminder's commit and a re-evaluation bills nothing more.

**Touch-list (verified 2026-09-18).**
- `packages/loftspace-ledger/`: `scripts.go` (`:425-458` fold comment; `:460-537` `arrears_entries` + `reversesId`;
  `:539-550` `arrears_rows`; `:552-701` `arrears_head` pre-pass; `:861`, `:996-1030`, `:964` the `lateFeeAt` carry/drop +
  the fee posting; `:1580-…` `post_entry` `reversesRef` + carry list; `:2386-2395` `derive_reads`; a `LinkReversal` branch)
  · `ddls.go` (`:38`, `:246`, `:307` `PermittedCommands` + descriptions; `.arrears` aspect DDL gains `lateFeeAt`) ·
  `opmetas.go:120-151` (+ a bare `LinkReversal` op-meta) · `permissions.go:142-152` (+ `LinkReversal` operator grant) ·
  `lenses.go:281-300, 316-324` · `README.md:100-125, 195-199` · `manifest.yaml` + `package.go:90` → 0.11.0 · tests:
  `ledger_test.go` (the five clinic vectors + `LinkReversal` vectors), `arrears_test.go` (netting, earlier-page netting,
  fee billed once, fee not billed without a clause, `lateFeeAt` dropped at the boundary, carried on partial payment),
  `lens_cypher_test.go` (`reversesKey`, `arrearsLateFeeAt`), a `derive_reads_test.go` with the `Test*UndeclaredSubmitter*`
  vector for `CreditAccount`'s new derived key (`lint-derive-reads-bare-vector`).
- `packages/semantic-contracts/`: `scripts.go:195-199, 251-252` · `lenses.go:280-320` (columns + two gaps in
  `leaseRentSettlement`), `:436-485` (`period = 'oneTime'`) · `targets.go:214-226` (two gap specs) · `package.go:114` +
  `manifest.yaml` → 0.8.0 · tests: `clause_purpose_test.go` (perArrearsEpisode ⇔ lateFee), `lens_cypher_test.go` (the
  one-time arm FALSE on `perArrearsEpisode`; the two new gaps TRUE/FALSE per arm), `TestSemanticContracts_LeaseRentSettlementColumnsMatchLens`.
- `packages/lease-signing/`: `ddls.go` (`.lateFee` aspect DDL beside `:896-932`; `SetLateFee` script branch; the leaseapp
  DDL's `PermittedCommands`) · `permissions.go` (self + any grants, op-meta with a form) · `lenses.go:1532-1579, 1706-1750`
  (`late_fee_cents`) · `package.go:117` + `manifest.yaml` → 0.44.0 · `scripts/verify-package-lease-signing.go:131`
  (`SetLateFee` in the leaseapp `ddlCheck`) · tests: a `set_late_fee_test.go` (managing landlord accepted; tenant, other
  landlord, undeclared link, ended tenancy, undecided application refused; amendment OCC).
- `cmd/loftspace-app/`: `ledger.go:69, 177-250 (new), 310, 432` · `applications.go:64`, `landlord_applications.go:94` ·
  `web/app.js` (`:4350-4402` the Reverse action beside the forms; the credit-row render; `:2338`, `:5469` the fee term +
  **Set late fee**; Rent-owed `arrearsLateFeeAt`) · goja pins beside the existing ledger pins.
- `Makefile`: nothing new (`refresh-loftspace` chains all three packages; check it does — `grep -n loftspace-ledger Makefile`).

**Precedents.** Decisions 1–4: clinic-ledger `scripts.go:1375-1394, 1574-1575, 1674-1679, 253-266, 360-377`, `lenses.go:227`,
`cmd/clinic-app/ledger.go:177-250`, `web/app.js:4053`, the eight tests above. Decision 5: `lease-signing/scripts.go:573-613`,
`permissions.go:143-147`, `ddls.go:896-932`, loftspace-domain's `SetUnitAddress` (op + `app.js` prompt-and-submit).
Decisions 6–7: `semantic-contracts/lenses.go:280-320`, `targets.go:200-260`, `scripts.go:173-340`. Decision 8: the send
branch `:925-1057` + `post_entry` `:1580` (the clause-authorized debit) + `ReturnDeposit` (`:2309`, a ledger op that reads a
clause's `.terms` it found by walk).

**Increments.** (1) **Reversal, whole** — loftspace-ledger decisions 1–3 + app decision 4 (`go test ./packages/loftspace-ledger/
./cmd/loftspace-app/`). (2) **Late fee, whole** — semantic-contracts 6–7, lease-signing 5, loftspace-ledger 8, app 5/8
(`go test ./packages/semantic-contracts/ ./packages/lease-signing/ ./packages/loftspace-ledger/ ./cmd/loftspace-app/`).
Sequential in one worktree (both reach `scripts.go`'s arrears op and `app.js`'s ledger panel). Then the gate loop
(`go build ./...`, `make vet`, `golangci-lint run ./...`, every `scripts/lint-*.go` under `STRICT=1`, `gofmt -l scripts/`,
`DIFF_BASE=<base> lint-package-version`, `make verify-package-lease-signing` against the stack), the live rollout
(`refresh-loftspace` = diff-apply + `provision-readpath` + app cycle; `reinstall-package` for semantic-contracts if the
chain omits it; weaver targets hot-reload), the `LinkReversal` repair of Jordan's `sQ9sTvaP`, and the live proof above.

**Gotchas (dossier, part 5 — the entries this fire trips).** *The leg of a guard is every op that writes the guarded value*
— `lateFeeAt` is carried on EVERY `.arrears` writer: the send branch, the not-yet branch, `historyTooLong`, mid-replay,
`post_entry`'s carry, `LinkReversal`'s stale mark; dropped on exactly the two paths `sentAt` drops. *For every ARM of the op
pin the gap FALSE over the state it leaves* — `missing_lateFeeClause` FALSE after the mint; `missing_lateFeeAmendment` FALSE
after the supersede and FALSE when the amounts agree; `missing_charge` FALSE on a `perArrearsEpisode` clause at every
`chargeCount`. *A dispatch declaration must name what the runtime binds* — `row.lateFeeClauseKey` is bound by the gap's own
`<> null`; never a Param off an optional hop the gap does not bind. *A recorded value is read as the FACT it records* —
`lateFeeAt` is the instant the fee was billed, judged against `episodeStart` like `sentAt`, never a proxy for "a fee
exists". *A mirror that drops one of the precedent's checks drops the invariant* — the pre-pass keeps clinic's cap-at-face
and its `(postedAt, key)` order; `reversesRef` keeps clinic's same-account proof (the derived `postedTo` key) and its
self-leg refusal; `LinkReversal` re-states every one of them. *A link key's type segment is what an outbound walk rebuilds
the far endpoint from* — pin the literal `lnk.transaction.<c>.reverses.transaction.<d>`. *Two individually-capped clearing
verbs compose into a leak* — a reversal is a CREDIT: `PayOutBalance` pays out a credit balance; a reversal larger than the
open balance is refused at face, and `SelfClearing`'s own-account rule (`5e86a342`) already covers the actor. FE: *a transport
throw after a submit* — `sent`/`confirmed` on Reverse and Set late fee; *op names in Go comments* — `lint-app-op-descriptors`;
*a new read-model column walks the app's struct pair* — `reversesKey`, `late_fee_cents`, `arrearsLateFeeAt` each reach the
wire struct and the goja pin builds from it; *two courtesy surfaces name the same instant in different zones* — `lateFeeAt`,
`postedAt` are instants, `localDateTime` everywhere; *a count the FE promises must apply the op's own predicate* — Reverse is
offered on a debit whose face exceeds what earlier reversals absorbed, the op's own cap. Standing checklist 1–6 walked: (1)
`lateFeeAt`'s lifetime table is decision 8; `reversesId` rides the checkpoint's existing lifetime; (2) the live census above
re-run at build; (3) revert-proof per increment — delete the pre-pass and watch the netting test fail, delete the fee posting
and watch the once-per-episode test fail; (4) nothing removed — the README's "not yet needed" is REPLACED by the relation
and the memo-only path remains valid for a credit that names nothing; (5) decision 2 states the arbitration; (6) clinic's
`reversesRef` verified against its own rule (`lint-derive-reads-bare-vector` clean — the vector exists at
`clinic-ledger/derive_reads_test.go`).

**Scope-diff.** Every touch traces to the scope sentence. Additions the ask does not name and the mechanism requires:
`LinkReversal` (the ask's live instance predates the relation; without it the fix cannot reach Jordan); `SetLateFee` + the
`.lateFee` term (a `purpose=lateFee` clause must be minted from a recorded landlord intent — `CreateClause` is operator-only
and a self grant on a shared op is not this fire's to add); the `perArrearsEpisode` period + the one-time arm's narrowing
(the ask's clause must not be billed at mint by `clauseSatisfaction`); the amendment gap (a fee term with no correction path
is a trap). Declared dependency re-verified both ways: `bd722499`'s paged replay is load-bearing (the checkpoint entry
gains a field) and shipped; `5e86a342`'s `SelfClearing` is not touched. Nothing here is load-bearing for anything unbuilt.
