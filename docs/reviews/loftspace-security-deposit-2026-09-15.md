# LoftSpace — a lease takes a security deposit (2026-09-15)

**Filed (PO, 2026-09-15, `fdc1cdde`):** "`DebitAccount`'s description names 'a deposit' and `leaseRentSettlement`
anticipates a one-time move-in clause, yet no listing carries one and no approval bills one; every live lease is rent
lines only. `depositAmount` on the listing, a `missing_deposit` gap minting a `oneTime` clause at approval, held apart
from rent on both statements (return rides move-out)." ★★ M. Winston-adjudicated (implementation-level; no contract
surface, no fork: every mechanism is package-owned and mirrors a shipped pattern — the `missing_clause` mint, the
`oneTime` archetype `clauseSatisfaction` already bills, `DebitAccount`'s clause-authorized entry, the recorded-fact
aspect at the approval event).

## Grounding

- **A one-time clause is already a billable archetype.** `CreateClause` defaults `period` to `oneTime` and refuses a
  term on it ([scripts.go:146-167](../../packages/semantic-contracts/scripts.go)); `clauseSatisfaction.missing_charge`
  opens for a `period <> 'monthly'` clause while no transaction is `authorizedBy` it (`chargeCount = 0`,
  [lenses.go:393-397](../../packages/semantic-contracts/lenses.go)) — no timer, it bills the moment it is minted — and
  dispatches `DebitAccount{accountKey, amountCents, clauseRef, period}` ([targets.go:100-111](../../packages/semantic-contracts/targets.go)).
  `DebitAccount` reads the amount from the clause's own `.terms`, stamps no period on a one-time entry, links the entry
  `authorizedBy` the clause and marks the clause `.status` `completed` ([loftspace-ledger/scripts.go:1438-1527](../../packages/loftspace-ledger/scripts.go)).
- **The rent clause is minted by a lease-anchored gap that tells its clause apart by SHAPE.** `leaseRentSettlement`
  fans over `(l)<-[:governs]-(c:clause)`, counts `period = 'monthly' AND conditioned <> true` clauses on the current
  term and opens `missing_clause` when none exists ([lenses.go:208-243](../../packages/semantic-contracts/lenses.go));
  its comment says the shape filter exists so "a one-time move-in fee" never suppresses rent ([lenses.go:107-112](../../packages/semantic-contracts/lenses.go)).
  A deposit clause needs the same kind of recognizable mark, and `period = 'oneTime'` alone is not one (any other
  one-time fee shares it) — `.terms` carries no purpose today.
- **The listing is the landlord's economics; the tenancy records what the approval read.** `.listing =
  {rentAmount, rentCurrency, bedrooms, bathrooms?, sqft?, availableFrom, leaseTermMonths, status}` is REPLACED by
  `SetListing` ([ddls.go:413-429](../../packages/loftspace-domain/ddls.go); `optional_number` at `:277`) and preserved
  verbatim by `SetListingStatus`'s `copy_data` ([ddls.go:301-307](../../packages/loftspace-domain/ddls.go)).
  `DecideLeaseApplication`'s first-approve branch reads the unit's `.listing` (a required read — `NoListing`) and
  stamps `.tenancy` create-only with `rentAmount` from `.terms.requestedRent` else `listing.rentAmount`
  ([scripts.go:954-1009](../../packages/lease-signing/scripts.go)). **`.tenancy` is rewritten wholesale by
  `SignRenewal` with an explicit five-field list** ([renewal_scripts.go:474-480](../../packages/lease-signing/renewal_scripts.go))
  — a field added to `.tenancy` is dropped by the next renewal (the "mirror drops the invariant" class), so the deposit
  is not a `.tenancy` field.
- **The ledger is one account per lease, statements read the `ledgerHistory` lens.** `ledgerHistory` walks
  `(t)-[:postedTo]->(a)-[:heldFor]->(l)` and `OPTIONAL (t)-[:authorizedBy]->(c)`, projecting `clauseKey` +
  `clauseProse` ([loftspace-ledger/lenses.go:222-239](../../packages/loftspace-ledger/lenses.go)); `cmd/loftspace-app`
  decodes it (`ledgerEntryProjection`, [ledger.go:16-48](../../cmd/loftspace-app/ledger.go)), sums the balance
  (`computeLedgerHistory`, `:57`) and renders one line: "Balance owed: $X · rent due <date>" (`rentBalanceLine`,
  [app.js:1556-1582](../../cmd/loftspace-app/web/app.js)) on the landlord ledger (`refreshLedgerBody`, `:3743`) and
  the tenant statement (`refreshTenantLedgerBody`, `:4032`; `renderStatementPanel`, `:3817`). The arrears
  evaluator ages the FIFO-oldest OPEN charge by its recorded `dueAt`, else its `postedAt`
  ([loftspace-ledger/lenses.go:86-103](../../packages/loftspace-ledger/lenses.go)) — an unpaid deposit is an open
  charge and ages like one.
- **Move-out is a recorded fact with consumers.** `EndTenancy` records `.tenancy.endedAt = termEnd` under OCC
  ([scripts.go:1606-1614](../../packages/lease-signing/scripts.go)); `leaseRentSettlement` already reads `.tenancy`
  and `.notice` on the same anchor. Nothing reads `endedAt` in the ledger; no lens dispatches a credit.
- **Live:** stack up, `bin/loftspace-app` :7788; four leases (Jordan / Riley / Sam / Priya), all rent-only. No
  `depositAmount` on any listing, no `.deposit` aspect anywhere (new fields, no migration).

## Decisions (Winston)

1. **`depositAmount` is a listing-economics field** (`unit.listing.depositAmount`, dollars like `rentAmount`,
   optional, `> 0` when present; absent = the unit takes no deposit). `SetListing` accepts it via `optional_number`
   and REPLACES it like every other field (a re-submit without it clears it — the edit form pre-fills it);
   `SetListingStatus` preserves it through `copy_data` unchanged. The DDL / OpMeta / package doc text names it; the
   Facet `SetListing` descriptor gains the field. `availableListings` projects `depositAmount` — the browse card, the landlord unit console and the edit
   form's pre-fill all read it (`landlordUnitsRead` projects nothing: no reader).
2. **The deposit is recorded on the lease at approval, as its own aspect: `.deposit = {amount, recordedAt}`**
   (class `leaseDeposit`), written create-only by `DecideLeaseApplication` on the same first-approve branch that
   stamps `.tenancy`, from `listing.depositAmount` when it is a positive number, else no aspect. The listing is a
   mutable relation — a landlord editing the deposit after approval must not change what this lease owes — so the
   figure is recorded at the event (`registeredAtSite` rule). Not a `.tenancy` field (grounding §3). The two
   protected application read lenses (`leaseApplicationsRead`, `landlordLeaseApplicationsRead`) project
   `deposit_amount`; `renewalsRead` does not (a renewal keeps the deposit; nothing there reads it).
3. **`CreateClause` gains an optional `purpose` token recorded on `.terms.purpose`** (`^[a-z][a-zA-Z0-9]{0,31}$`,
   `InvalidArgument` otherwise; absent = no purpose, every existing clause). `SupersedeClause` keeps the amended clause's recorded purpose — a payload `purpose` is admitted only when it equals
   it (both absent counts) — and refuses `ClauseNotActive` on a `completed` / `returned` clause (a charged clause
   re-minted as `active` would bill again; its `.status` is a required read, the `superseded` write pinned to the
   hydrated revision). A deposit clause's `purpose` is refused on any other shape: `deposit` is a `oneTime`
   `computational` clause. The purpose is what lets a lens tell a deposit from any other one-time fee — the same role
   `period = 'monthly' AND conditioned <> true` plays for rent.
4. **`leaseRentSettlement` mints and returns the deposit with two new gaps** (every column declared):
   - `missing_deposit` = `accountKey <> null AND depositAmount <> null AND endedAt = null AND depositClauseCount = 0`
     (an ended tenancy with no deposit clause is never minted, billed and refunded), where
     `depositAmount = l.deposit.data.amount`, `depositCents = CASE WHEN depositAmount = null THEN null ELSE
     depositAmount * 100 END` (the `termRentCents` guard), `depositClauseCount = count(DISTINCT CASE WHEN
     c.terms.data.purpose = 'deposit' THEN c.key ELSE null END)` — purpose-only: a mis-shaped `deposit` clause can
     only be seeded or legacy data, and the money-conservative reading of one is "the landlord installed a deposit"
     (the mint stays shut, the return never opens). Dispatch `CreateClause{leaseAppKey, accountKey,
     amountCents: row.depositCents, period: "oneTime", purpose: "deposit", prose: "Security deposit, held for the
     tenancy and returned when it ends."}`. It waits on the account like `missing_clause` does (the account opens
     once `requestedRent` is recorded) and opens at approval, before the signature, like the rent clause — the
     one-time archetype then bills it at once through `clauseSatisfaction` → `DebitAccount`, unchanged.
   - `missing_depositReturn` = `accountKey <> null AND endedAt <> null AND depositClauseKey <> null`, where `endedAt =
     l.tenancy.data.endedAt` and `depositClauseKey = max(CASE WHEN c.terms.data.purpose = 'deposit' AND
     c.terms.data.period = 'oneTime' AND c.terms.data.kind = 'computational' AND c.status.data.state = 'completed'
     THEN c.key ELSE null END)` (a monthly clause also reaches `completed`, on its final period) — `completed` is the state `DebitAccount` leaves a
     charged one-time clause in, so an uncharged deposit (still `active`) is not returned before it is charged, and
     a `returned` clause drops out, closing the gap. Dispatch `ReturnDeposit{leaseAppKey, clauseKey:
     row.depositClauseKey, accountKey}` (loftspace-ledger, Class `transaction`; Reads: the account, the lease's
     `.tenancy`, the clause, its `.terms`, `.status`; OptionalReads: the account's `.arrears`). The deterministic
     `chargesTo` / `governs` link keys span two row columns, which a `Params` template cannot express, so the op's own
     `derive_reads` hydrates them (with the account root, the lease root and `.arrears`) as optionalReads for every
     submitter — absent ⇒ the op's refusal, never a live read.
5. **`ReturnDeposit` (loftspace-ledger, operator `any`, Weaver's dispatch) posts the deposit back as a credit.**
   It proves the account, clause and lease roots live (`UnknownAccount` / `UnknownClause` / `UnknownLeaseApplication`),
   reads the clause's `.terms` and refuses `NotADeposit` unless `purpose = 'deposit'`, `period = 'oneTime'`, `kind =
   'computational'` and `amountCents > 0`, reads `.status` (absent ⇒ `InvalidState`, CreateClause writes it
   unconditionally), reads the lease's `.tenancy` and refuses `TenancyNotEnded` (`endedAt` absent), proves the clause
   `chargesTo` the payload account and `governs` the payload lease off the deterministic link keys
   (`ClauseAccountMismatch` / `ClauseLeaseMismatch`), and only then no-ops with empty mutations on `returned` (the
   `EndTenancy` idempotent shape — the gap is closed, a re-dispatch is a race; a mis-addressed submit refuses first)
   or refuses `DepositNotCharged` on any other state than `completed`. It writes one `transaction`
   (`.entry = {type: "credit", amountCents: the clause's amountCents, postedAt, memo: "Security deposit returned"}`,
   `postedTo` the account, `authorizedBy` the clause — the chain of custody `DebitAccount` records), updates the
   clause `.status` to `{state: "returned", returnedAt, …every recorded field kept}` with an explicit
   `expectedRevision` (the hydrated revision — an explicit pin is a terminal conflict, never a §3.2 re-hydrate retry,
   so a racing second return is rejected rather than replayed), emits `loftspace.depositReturned`, and marks
   `.arrears` stale through the `arrears_stale_mark` helper `post_entry` shares (a credit moves the FIFO). The return credit is an ordinary credit on the
   lease's account: it nets against whatever the tenant still owes (the final rent period, arrears) and the remainder
   reads "Credit balance: $X" — the refund owed to the tenant. ~~A payout verb, deductions and interest are product
   rules the PO did not file (alternatives).~~ *(2026-09-17: the PO filed deductions and a payout; the return nets the
   recorded deductions and a landlord verb pays a credit balance out — the section at the end. Interest stays unfiled.)*
6. **Both statements hold the deposit apart.** `ledgerHistory` and one-bill's `rentEntries` project `c.terms.data.purpose AS clausePurpose`;
   `ledger.go` / `one_bill.go` thread `ClausePurpose` and compute `depositHeldCents` (Σ debits − Σ credits over deposit
   rows — custody, not what is unpaid: a payment names no clause) + `depositChargedCents` / `depositChargedAt` /
   `depositReturnedAt` on `/api/ledger` and `/api/one-bill`. The FE adds one "Security deposit"
   line above the balance on the landlord ledger and the tenant statement: "Security deposit $D · held since
   <date>" / "Security deposit $D · returned <date>"; deposit rows carry a "Deposit" tag in place of the
   covers-period label; `rentBalanceLine` appends " · of which up to $min(balance, held) is the security deposit" while a balance is owed
   and a deposit is held, so the owed figure is never read as rent alone (the ledger cannot attribute a payment to the
   deposit, so it says "up to"). The tenant's "Pay rent" panel shows the bare balance; the strip is on the adjacent
   Statement panel (`refreshStatementBody`, the one-bill read). The listing card (browse), the landlord unit console and the edit /
   post forms show "Security deposit $D"; the terms panel and the landlord application card show it once recorded
   on the lease (`depositAmount` from the read lenses). Every amount source (`SetListing` rent + deposit, the
   application's `requestedRent`, the renewal's rent) refuses more than two decimals — `mint_clause` coerces
   `amountCents` to whole cents and refuses a fractional one, so a fractional-dollar source would otherwise leave the
   mint gap refused on every Weaver pass. Dates render by `fmtUTCDate`.
7. **Proof.** Package tests: `SetListing` accepts / validates / clears `depositAmount`, `SetListingStatus` keeps it;
   `DecideLeaseApplication` records `.deposit` from the listing and records none for an absent or non-positive
   figure; `CreateClause` `purpose` accepted / refused / absent; cypher pins of both gaps over one fixture set (no
   deposit · deposit unminted · minted-uncharged · charged-not-ended · ended-charged · returned); `ReturnDeposit`
   every refusal, the happy path, the idempotent no-op, the `.arrears` stale mark; goja pins for the FE lines.
   `internal/leaseconvergence` gains `TestLeaseConvergence_DepositChargedAndReturned` (leaseshortwindow): listing with
   a deposit → apply → approve → `.deposit` recorded → deposit clause minted → debited once → sign → notice for
   today → `EndTenancy` → one return credit, clause `returned`, no second return. Live: Nora Vance's vacant listing
   gains a deposit through the running app; browse and the unit console show it; an approval on it (if a vacant unit
   takes an application in the fire) mints + bills the deposit and the statement reads the strip.

## Alternatives rejected

| alternative | why not |
|---|---|
| Delete the thing — the landlord records the deposit by hand with `LoftspaceRecordCharge` | The PO's harm is that no listing CARRIES a deposit and no approval bills one; a hand charge is untagged, un-returned and invisible to an applicant browsing. |
| `depositAmount` on `.tenancy` | `SignRenewal` rewrites `.tenancy` from a five-field list; the deposit would vanish on renewal. Own aspect, one writer. |
| The gap reads the unit's live `listing.depositAmount` through `appliesToUnit` | A listing edit after approval would re-price a signed lease's deposit; the figure is recorded at the approval event. |
| Tell the deposit clause apart by its `prose` | Free text is not a key; a landlord-installed clause with the same prose would be returned as a deposit. A recorded `purpose` token is the shape filter. |
| Return by `CreditAccount` (existing op) | `CreditAccount` records a payment RECEIVED, links no clause, and cannot mark the deposit returned — the gap could never close, and the statement could not tell the return from a payment. |
| Return at the notice, or at `leaseEnd` | The notice is an intention; `endedAt` is the recorded end every consumer keys on, and it is what a return "rides". |
| A landlord verb with deductions / a payout verb | ~~Product rules the PO did not file~~ — filed 2026-09-16; built as the section at the end. |
| Mint the deposit at signature rather than approval | The rent clause is minted at approval; the deposit is due when the lease is granted. The filing says "at approval". |

## Fire brief (build note, 2026-09-15)

**1. Scope sentence** — verbatim above: `depositAmount` on the listing, a `missing_deposit` gap minting a `oneTime`
clause at approval, held apart from rent on both statements (return rides move-out). Green bar: the Decisions' proof (§7).

**2. Verified touch-list** (live at `ceadff67`):
- `packages/loftspace-domain/ddls.go:53-60,73-99,110,143,158-193,413-429` (DDL text + field docs + examples + `SetListing`
  data), `opmetas.go:73-119` (`SetListing` descriptor), `lenses.go:199-247` (`availableListings`), `:105-160,269-300`
  (`landlordUnitsRead` + its column DDL), `package.go:10`, `README.md`, `manifest.yaml` + `Version`; tests beside
  `integration_test.go:345` / `landlord_units_lens_test.go` / `lens_cypher_test.go`.
- `packages/lease-signing/scripts.go:954-1009` (first-approve branch), `ddls.go` (aspect DDL beside `tenancy` /
  `notice` at `:204-221`; PermittedCommands unchanged), `lenses.go:279,449,1471-1533,1645-1702` (both app read lenses
  + column DDLs), `manifest.yaml` + `Version`; tests beside `lease_signing_test.go:1127,1208` (the tenancy-rent pins),
  `seedUnitWithListing:2521`.
- `packages/semantic-contracts/scripts.go:126-272` (`mint_clause` purpose), `lenses.go:61-62,107-146,208-243`
  (BodyColumns + comment + spec), `targets.go:110-167` (two gaps), `ddls.go:41,60-97` (field text), `permissions.go`
  (OpMeta field), `manifest.yaml` + `Version`; tests beside `clause_term_test.go` / `lens_cypher_test.go`.
- `packages/loftspace-ledger/scripts.go:1207-1600` (`post_entry`, `derive_reads`, `execute` at `:1600-1626` →
  `ReturnDeposit`), `lenses.go:222-239` (`ledgerHistory` + its BodyColumns), `ddls.go:256` (PermittedCommands + op
  text), `permissions.go:121-125` (grant + OpMeta), `package.go:2,21`, `manifest.yaml` + `Version`; tests beside
  `ledger_test.go` / `lens_cypher_test.go`.
- `internal/leaseconvergence/tenancy_notice_convergence_test.go` (the vector to mirror; harness `seedApplicant:663`,
  `submitOp:624`).
- `cmd/loftspace-app/ledger.go:16-48,57-103,194-300,445` (projection + balance + arrears response), `listings.go:20-45,86`,
  `applications.go:58,118,202`, `landlord_applications.go:89,146,242` (row structs + column lists), `web/app.js:1433-1465`
  (browse card + `money`), `:1507-1520` (period label), `:1556-1582` (`rentBalanceLine`), `:2143-2200` (terms panel),
  `:3743-3812` (landlord ledger), `:4032-4060` (tenant ledger), `:5331-5356` / `:5410-5529` (edit / post forms),
  goja pins beside `ledger_ui_test.go` / `lease_term_ui_test.go`.

**3. Precedents.** `optional_number` bathrooms/sqft in `SetListing` (optional listing field); `copy_data` (status
rewrite keeps it); `.tenancy`'s create-only `make_aspect` on first approve + `rentAmount`'s positive-number test (the
`.deposit` write); `.notice` (own aspect, one writer, read lenses project it, DDL beside `tenancy`); `missing_clause`'s
`count(DISTINCT CASE …)` shape filter + `termRentCents` CASE guard (`missing_deposit`); `missing_term`'s
`max(CASE …)` one-per-pass dispatch (`missing_depositReturn`); `missing_account`'s cross-package dispatch of a
loftspace-ledger op; `post_entry`'s transaction + `postedTo` + `authorizedBy` mutations and `.arrears` stale mark
(`ReturnDeposit`); `EndTenancy`'s idempotent no-op + OCC pin on the hydrated revision; `DebitAccount`'s `.status`
`completed` write (the `returned` write keeps every field); `ledgerHistory`'s `clauseProse` optional-hop column
(`clausePurpose`); `rentBalanceLine` + `fmtUTCDate` + `moneyAmount` (the deposit line); `lease_term_ui_test.go` goja pins.

**4. Increments + green checks.**
- **Inc 1 (loftspace-domain + lease-signing, sonnet):** `depositAmount` on `SetListing` / DDL / OpMeta / both lenses;
  `.deposit` DDL + the first-approve write; `deposit_amount` on both app read lenses; version bumps.
  `go test ./packages/loftspace-domain/ ./packages/lease-signing/ -count=1`; `go test ./internal/refractor/ -run 'Corpus|Census'`;
  `lint-opmeta-required-fields`, `lint-package-version`, `lint-facet-*`.
- **Inc 2 (semantic-contracts + loftspace-ledger, opus — a new gap pair, a new op, a new clause state):** `purpose`
  on `mint_clause`; `missing_deposit` + `missing_depositReturn` + targets; `ReturnDeposit` + grant + OpMeta +
  `derive_reads`; `clausePurpose` on `ledgerHistory`; version bumps. `go test ./packages/semantic-contracts/
  ./packages/loftspace-ledger/ -count=1`; `lint-gap-column-declaration`, `lint-opmeta-required-fields`,
  `lint-derive-reads-bare-vector`, `lint-refusal-courtesy`, `lint-package-version`.
- **Inc 3 (e2e + FE, sonnet after 1+2):** the convergence vector (`make test-lease-convergence`); `ledger.go` +
  row structs + `app.js` + goja pins; `go test ./cmd/loftspace-app/ -count=1`; `lint-app-op-descriptors`,
  `lint-markup-escaping`, `lint-stale-render-guard`, `lint-web`, `lint-refusal-courtesy`.
- **Close:** `go build ./... && make vet && golangci-lint run ./... && gofmt -l scripts/`; every `scripts/lint-*.go`
  STRICT; `go test ./packages/... ./cmd/loftspace-app/... ./internal/refractor/... ./internal/leaseconvergence/...`;
  `make refresh-loftspace` + `provision-readpath` in the same breath (the latched-lens lesson) + `make
  verify-package-loftspace-domain` / `verify-package-lease-signing` on the live stack; rebuild + cycle
  `bin/loftspace-app`; live proof on Nora Vance's vacant listing.

**5. In-scope gotchas.** Package edits bump manifest + `Version` (four packages). New `missing_*` columns are declared
in the target's gaps (`lint-gap-column-declaration`) and every templated Param names a column the gap's own conjunct
requires non-null (`row.depositCents` ← `depositAmount <> null`; `row.depositClauseKey` ← `<> null`). `.deposit` is a
create-only write inside an op that also writes `.tenancy` create-only — one event, both or neither. The protected
read lenses gain a column: `provision-readpath` in the same breath as the install or they latch. No `$now` in any
cypher. `ReturnDeposit` is Weaver-dispatched only — no FE dispatch site, no Facet descriptor; its OpMeta still carries
the `(facet)`-free courtesy lines the gate asks of a described op. `.status` gains a THIRD writer: the `returned`
update pins the hydrated revision and keeps `completedAt` / `chargeValidUntil`. No `// Story …` comments. Dossier
entries walked (`_packages.md` + `vertical-apps.md`): *a recorded value is read as the FACT it records* (`state =
'completed'` is "charged", never "the clause exists"; `endedAt`, never `leaseEnd`); *the "leg" of a guard* (writers of
`.status`: CreateClause, DebitAccount, ReturnDeposit; readers of `purpose`: both gaps + `ledgerHistory`; writers of
`.listing`: SetListing REPLACES, SetListingStatus preserves); *a gap's budget and cadence from its WHOLE loop* (for
every arm of `ReturnDeposit` — no-op, refusal, credit — pin `missing_depositReturn` FALSE over the state it leaves;
`missing_deposit` closes on the first mint); *a dispatch declaration names what the runtime binds*; *a mirror that
drops a precedent's branch drops its invariant* (`post_entry`'s `.arrears` stale mark travels with the credit);
*a consumer's exclusion is grounded in the enforcing predicate* (the `completed` conjunct is `DebitAccount`'s own
write); *a new terminal state is a census of every status switch* (`returned` against every `state` comparison in
semantic-contracts + loftspace-ledger: `ShortenClauseTerm` / `BackfillClauseTerm` skip non-monthly; `clauseSatisfaction`
never reads `state`); *two courtesy surfaces name the same instant* (`fmtUTCDate` on both statements); *an op name in a
`cmd/<app>` comment is a UI reference*. Standing checklist: `.deposit` LIFETIME — written once at first approve, never
reset, tombstoned with the leaseapp; `.terms.purpose` written at mint, never changed; every count above re-run live; a
negative test's positive vector first, fixes proven by revert; one deterministic key one writer; precedent debt checked
(`CreditAccount`'s self path caps at the balance — `ReturnDeposit` is not that path and takes no cap).

**6. Adjacent finds.** None filed at scoping (a payout verb / deductions / deposit interest are product rules the PO did
not file — alternatives table, not rows).

**7. Non-goals.** Deductions, interest, a payout verb; a deposit on renewals (kept, not re-billed); café / wellness
ledgers; retro-fitting a deposit onto an already-approved lease (`.deposit` is recorded at the approval event only);
Facet's ledger surfaces.

### Build note (2026-09-15)

Shipped `ec274abe` (merge of `f561c439` · `e46dce40` · `aa6b8f88`) + `6b47d0c1`; brief `1f78958f`. CI reddened once on the
merge: the app's protected read now selects `deposit_amount` and the two Postgres-gated RLS fixtures (`rls_columns_test.go`,
skipped locally without `POSTGRES_TEST_DSN`) lacked the column (502 on every read-boundary vector — reproduced locally
against the stack's Postgres with CI's DSN, fixed, revert-proved), and the notice vector's steady-state window opened
before the tenancyEnd row had re-projected the relist (a KV wait followed by a read-model assertion — the vector now waits
for the row). Live on the shared stack (loftspace-domain 0.13.2, lease-signing 0.41.2, semantic-contracts 0.7.3,
loftspace-ledger 0.8.2, one-bill 0.5.1 diff-applied; `landlordLeaseApplicationsRead` latched on the new column between
the install and `provision-readpath`, resumed with `lattice lens resume <NanoID> --actor <Loupe operator>` — the CLI takes
the bare NanoID, a dotted `vtx.meta.…` never matches the responder's `*` token; `bin/loftspace-app` cycled): Nora Vance's
40 Riverside Walk took `SetListing` with `depositAmount: 2400` through the Gateway as the landlord; `availableListings`
and the unit console read `depositAmount 2400`; Riley Chen's existing lease on it carries no `.deposit` (recorded at
approval only) and its `/api/ledger` reads `depositHeldCents 0`. Every unit of hers is leased, so the approval → mint →
charge → return path is proven by `TestLeaseConvergence_DepositChargedAndReturned`, not live.

Deviations from the brief (folded into the body above): `landlordUnitsRead` projects no `unit_deposit` (no reader);
the custody links ride `derive_reads`, not the dispatcher's `Reads`; `missing_deposit` conjoins `endedAt = null`,
`missing_depositReturn` conjoins `accountKey <> null` and the `oneTime` `computational` archetype; `ReturnDeposit` refuses
before its no-op and proves the lease root; `SupersedeClause` keeps the recorded purpose, refuses `ClauseNotActive`, reads
`.status` / `.terms` as required and pins its `superseded` write; `mint_clause` coerces whole cents and every amount source
refuses more than two decimals; `ReturnDeposit` has no courtesy lines (no client site — `lint-refusal-courtesy` governs
sites, not ops); the balance line says "of which up to $min(balance, held)"; one-bill projects `clausePurpose`; the deposit
row's tag replaces its memo; the unit card is currency-aware.

Review classification (a cold pass over Inc 1+2, a cold cumulative pass over the whole diff, three fix rounds):
**design-gap** — the archetype pin on `completed` (a termed monthly clause completes too), purpose inheritance vs override
on supersede, `endedAt = null` on the mint, whole cents at the reader vs the three sources, the balance-line custody /
unpaid conflation (`_packages.md` sightings on the recorded-fact, "leg", cadence and load-bearing-field entries;
`vertical-apps.md` sighting on the count-predicate entry); **implementation-bug** — the no-op before custody, the
unproven lease root, `SupersedeClause` accepting a `completed` clause (pre-existing, closed here); **brief-gap** — the
grounding named `refreshTenantLedgerBody` as the tenant statement (it is `refreshStatementBody`), the RLS column fixture
(a Postgres-gated test the brief's green list never ran); **review-over-reach** — the explicit `expectedRevision` pin
(precedent-consistent, kept with the trade stated in the comment). Adjacent finds: none open.

## Deductions and the payout (PO row 2026-09-16, built 2026-09-17)

**Filed (PO, 2026-09-16, `16af6c9d`):** "`ReturnDeposit` credits the full clause at `endedAt`; no verb records a damage
deduction first, and what nets above the last rent reads 'Credit balance' forever. `RecordDepositDeduction{amount, reason}`
while held, a return that nets it, `PayOutBalance` on an ended tenancy; itemized on both statements."

### Decisions (Winston)

1. **A deduction is a ledger entry that moves custody, not what the tenant owes: `.entry.type = "deduction"`.** The
   deposit is money the tenant already paid; taking part of it for damage changes what comes back, never the account's
   balance. Every balance reader tests the type explicitly — the arrears FIFO (`scripts.go:568/578`), the resident
   self-credit walk (`:1545/1547`), `ledger.go:103/141/300`, `one_bill.go:96` — so a third type is ignored by all of
   them, and custody on the statement is charged − deducted − returned. It is `postedTo` the account and `authorizedBy`
   the deposit clause (the chain of custody the charge and the return record), `memo` = the reason. No `.arrears` stale
   mark: a deduction moves no FIFO (the evaluator's replay skips it at capture, so the checkpoint keeps carrying "every
   debit and credit" and nothing else).
2. **The clause's `.status` carries the running `deductedCents`** — the return already reads and pins `.status`, so no
   dispatcher declares a new key. `RecordDepositDeduction` writes it as a bare update on the hydrated key (Contract #3
   §3.2 re-hydrate retry: two concurrent deductions both land, the total exact); it refuses `DepositNotHeld` unless
   `state = completed` (active = not charged yet; returned = gone) and `DeductionExceedsDeposit` when `deductedCents +
   amountCents > terms.amountCents`. `clauseStatus`'s DDL (semantic-contracts) admits the op and names the field.
3. **`RecordDepositDeduction{accountKey, clauseKey, amountCents, reason}`** (loftspace-ledger, class `transaction`):
   `reason` required, 1–200 chars. Who: the landlord (the self-scope path `post_entry` proves — the account's own
   `heldFor` lease, its `appliesToUnit` unit, the caller's `manages` link; the resident branch answers first and is
   refused `AuthDenied` here) or the operator with no target. Custody off the deterministic `lnk.clause.<c>.chargesTo.
   account.<a>` (`ClauseAccountMismatch`); `.terms.purpose = deposit` on a `oneTime` `computational` clause
   (`NotADeposit`). `derive_reads` hydrates account root, clause root, `.terms`, `.status`, the chargesTo link — the
   `ReturnDeposit` branch's shape. Event `loftspace.depositDeducted`. The self-scope proof is EXTRACTED from `post_entry`
   into one helper the three self-scoped ops share (returns the lease key + which standing the caller proved); `post_entry`'s
   behaviour is unchanged and its tests pin it.
4. **`ReturnDeposit` credits `terms.amountCents − status.deductedCents`.** Net > 0: the credit as today, at the net.
   Net = 0 (fully deducted): no transaction and no links — a zero-amount entry is not a transaction — the clause still
   moves to `returned` with `returnedAt` (the pinned write as today), the event carries `amountCents: 0` and no
   `transactionKey`, the response no `primaryKey`. The gap and its dispatch are unchanged (semantic-contracts'
   `missing_depositReturn` reads state, not amounts).
5. **`PayOutBalance{accountKey, leaseAppKey}`** (loftspace-ledger, class `transaction`) pays the whole credit balance
   out, computed op-side from the account's own `postedTo` history exactly as the resident self-credit walk computes what
   is owed (the walk is EXTRACTED into `account_balance_cents(acct_key)` and both call it; its page budget exhausted
   refuses `HistoryTooLong`, never a partial sum). Who: landlord self path or operator, as (3). Guards: the lease root
   alive, `lnk.account.<a>.heldFor.leaseapp.<l>` alive (`AccountLeaseMismatch`), `.tenancy.endedAt` recorded
   (`TenancyNotEnded`), `NoCreditBalance` when owed ≥ 0. Writes one `{type: "debit", kind: "payout", amountCents:
   −owed, postedAt, memo: "Balance paid out to the tenant"}` `postedTo` the account, no `authorizedBy`, `.arrears`
   stale-marked like every debit. Event `loftspace.balancePaidOut`. Re-submit: owed is 0 → `NoCreditBalance`.
   `kind` is the recorded provenance of a debit no clause authorizes; `ledgerHistory` and one-bill's `rentEntries`
   project `t.entry.data.kind AS kind` so both statements label the row by its record, never its memo.
6. **Both statements itemize.** `ledgerEntryRow` gains `Kind`; `depositSummary` gains `DepositDeductedCents` and
   `DepositClauseKey` (the deposit charge row's clause — the form needs it); `computeDepositSummary` handles
   `deduction` (held −=, deducted +=). `/api/one-bill` mirrors. FE: `depositRowTag` tags a `deduction` row
   " · Deposit deduction" and KEEPS its memo (the reason is the line), a `kind = payout` row " · Balance paid out"
   (memo dropped, fixed text); the deposit strip reads "Security deposit $D · held since <date>", then "· $X deducted"
   when any, "· $R returned <date>" once returned, and "· fully deducted, nothing to return" when held is 0 with no
   return. The landlord ledger form gains "Deduct from deposit" (amount + a required reason, confirm) shown only while
   `depositHeldCents > 0`, and "Pay out $X to tenant" (confirm) shown only when `balanceCents < 0` and the row's
   `tenancyEndedAt` is set; every state refusal carries its `refusal-courtesy` line. The tenant statement renders the
   same rows and strip through the shared helpers.
7. **Proof.** Package tests: `RecordDepositDeduction` every refusal (resident `AuthDenied`, `DepositNotHeld` on active
   and returned, `DeductionExceedsDeposit` at the boundary, `ClauseAccountMismatch`, `NotADeposit`, missing reason),
   the happy path (entry shape, links, `deductedCents` running total across two deductions, no arrears mark); `ReturnDeposit`
   nets a deduction and the fully-deducted zero-net shape; `PayOutBalance` every refusal and the happy path (the debit
   zeroes the balance, arrears marked, a second call `NoCreditBalance`); the `Test*UndeclaredSubmitter*` vectors for both
   new `derive_reads` branches; `post_entry` self-credit unchanged after the extraction (the existing pins). Go:
   `computeDepositSummary` with a deduction and the zero-net case; goja pins for the tag, memo, strip and the two form
   gates. `internal/leaseconvergence` `TestLeaseConvergence_DepositChargedAndReturned` gains a deduction between the charge
   and the end (return credits the net). Live: a deduction on a held deposit on the running app if a lease carries one,
   else the convergence proof.

### Alternatives rejected

| alternative | why not |
|---|---|
| Delete the thing — the landlord records damage with `LoftspaceRecordCharge` and pays out with a hand debit | A charge is owed by the tenant: it ages into arrears and a reminder fires during the notice period for money the deposit already covers; a hand debit is a "charge" on the statement. The filing wants the deduction taken from custody and the payout named. |
| A deduction as `type: debit` with a marker | Counted by every balance reader; the same arrears false alarm. |
| Deductions as a list on the clause, the return reads it, statements unpack it | The statement is the ledger; a deduction as a row is itemized by the machinery that already sorts, tags and renders rows. The running total on `.status` is the op-readable figure; the rows are the itemization. |
| `PayOutBalance{amountCents}` trusted from the payload | The landlord already posts uncapped debits, so trust is not widened — but a figure the op can compute from the record it will not take from a client: the self-credit walk is the precedent and is reused. |
| Gate the return on a landlord "settle" verb so deductions can follow move-out | Changes the ratified auto-return; the PO did not ask. The notice period is the window. |
| Project the clause's `returnedAt` for the zero-net return | Derivable: held = 0 and deducted = charged with no credit row IS "nothing to return"; no new column in two packages. |

### Fire brief (build note, 2026-09-17)

**1. Scope** — verbatim the filing above. Green bar: decisions 1–7.

**2. Touch-list (verified live).** `packages/loftspace-ledger/scripts.go` — `post_entry` :1443 (self-scope proof :1467–1576,
balance walk :1526–1554), `arrears_stale_mark` :1339, `return_deposit` :1770–1909 (entry :1865, status pin :1886–1900),
`derive_reads` :1911–1987 (ReturnDeposit branch :1941–1977), dispatch table :1999–2017, arrears replay capture :493–497;
`ddls.go` — `transactionDDL` :291 (PermittedCommands :295, InputSchema :373–381), `accountArrearsAspectTypeDDL` :234;
`opmetas.go` — `LoftspaceRecordCharge` / `ReturnDeposit` entries (:172–182); `permissions.go` :140–167; `lenses.go` —
`ledgerHistorySpec` :281–299; `package.go:90` + `manifest.yaml` 0.9.1; `notifications.go` (events, if enumerated).
`packages/semantic-contracts/ddls.go:487` clauseStatus PermittedCommands + description; `package.go:114` + manifest 0.7.3.
`packages/one-bill/lenses.go:82` `rentEntriesSpec`; `package.go:29` + manifest 0.5.1. `cmd/loftspace-app/ledger.go`
(:83 row, :103 balance, :119–156 deposit summary), `one_bill.go` (:77, :96, :224–244), `web/app.js` (`entryPeriodLabel`
:1529, `depositRowTag` :1545, `entryMemoSuffix` :1556, `rentBalanceLine` :1610, deposit strip :1646, `refreshLedgerBody`
:3961, `renderLedgerRecordForm` :4182–4235, `refreshStatementBody`), `landlord_applications.go:90` (`tenancyEndedAt`);
goja harness `rent_arrears_ui_test.go:36`. `internal/leaseconvergence/deposit_convergence_test.go:166–279`.

**3. Precedents.** Ops: `return_deposit` (custody proofs, status pin), `post_entry` (self-scope proof, entry shape, stale
mark), the self-credit balance walk; grants: `LoftspaceRecordCharge`'s pair; OpMeta: `LoftspaceRecordCharge`; DDL admission:
`ReturnDeposit` in clauseStatus; lens column: `clausePurpose` in both specs; Go: `computeDepositSummary`; FE: the record form
+ `depositRowTag`; tests: `return_deposit_test.go`, `rent_arrears_ui_test.go`.

**4. Increments.** Inc 1 — loftspace-ledger ops + derive_reads + DDLs + grants + OpMetas + lens column + version;
semantic-contracts DDL + version; one-bill column + version. Green: `go test ./packages/loftspace-ledger/ ./packages/
semantic-contracts/ ./packages/one-bill/`, `STRICT=1` every `scripts/lint-*.go`. Inc 2 — `cmd/loftspace-app` Go + FE + goja
pins. Green: `go test ./cmd/loftspace-app/`, `node --check web/app.js`. Inc 3 — leaseconvergence test. Green: `make
test-lease-convergence` (leaseshortwindow tag; read the Makefile for the exact target). Then the full gate set.

**5. Gotchas.** Three package version bumps (`lint-package-version`); `Test*UndeclaredSubmitter*` per new `derive_reads`
op (`lint-derive-reads-bare-vector`); `refusal-courtesy` lines at every dispatch site for every reachable code (incl. the
helper's `AuthDenied`); `lint-opmeta-required-fields` / `lint-app-op-descriptors` on the new OpMetas; `op_catalog.go`
admits ops from the opCatalog lens — a missing OpMeta is a refused dispatch; the arrears replay checkpoint schema names
"every debit and credit" — skip a deduction at capture; a bare update on `.status` for the running total, an explicit pin
for the terminal return; MERGED ≠ RUNNING — `refresh-loftspace` for the three packages, cycle `bin/loftspace-app`.
Dossier (`_packages.md`): the "leg" of a guard is every writer of the value and every reader — `deductedCents`' writers
are one op, its readers the return; a mirror that drops a branch drops the invariant — the extraction keeps the
resident-first order; a recorded value is read as the fact it records. Dossier (`vertical-apps.md`): a count the FE
promises applies the op's own predicate — the "Pay out" gate reads the same balance the op computes; a new terminal
state is a census of every render gate — a `deduction` row through every row renderer and amount formatter. Standing
checklist: new state (`deductedCents`) has a lifetime — created by the first deduction, carried by the return's
field-preserving copy, never reset; every fix proven by revert; one deterministic key one writer (none new).

**6. Adjacent finds.** None at scoping.

**7. Non-goals.** Interest on the deposit; a settle verb gating the return; partial payouts; Facet descriptors for the
two landlord verbs (the landlord console is loftspace-app; `ReturnDeposit`'s precedent).
