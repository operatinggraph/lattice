# loftspace-ledger

The LoftSpace tenant payment ledger — a per-lease financial account that records charges
(rent, fees, deposits) and payments as an **append-only** transaction history; a balance is always
derived by summing entries, never stored as a mutable running total.

Depends: `lease-signing` (the `leaseapp` vertex type an account is `heldFor`) and
`orchestration-base` (the `freshnessExpiry` marker the arrears timer's firing writes onto the
account). Install: `lattice-pkg install packages/loftspace-ledger` (after both; or
`make install-loftspace` onto a running stack).

## Inventory

| Kind | Canonical names |
|---|---|
| **Vertex types** (3) | `account` (root `{}`, D5) · `transaction` (root `{}`, D5, `.entry` aspect) · `loftspaceArrearsNotificationOp` (the bridge's replyOp handler, no vertex of its own) |
| **Aspect types** (4) | `ledgerAccountGuard` — `vtx.leaseapp.<id>.ledgerAccount`, the per-lease create-only uniqueness guard · `loftspaceAccountArrears` — `vtx.account.<id>.arrears`, the arrears-episode state · `loftspaceAccountArrearsNotification` — `vtx.account.<id>.arrearsNotification`, the reminder's delivery outcome · `depositDeductions` — `vtx.clause.<id>.deductions`, this package's own running-deduction-total aspect on a `semantic-contracts` clause |
| **Links** (3) | `heldFor` (account → leaseapp) · `postedTo` (transaction → account) · `authorizedBy` (transaction → clause, written by `DebitAccount` with a `clauseRef`, by `ReturnDeposit` and by `RecordDepositDeduction`) |
| **Operations** (9) | `LoftspaceCreateAccount` · `DebitAccount` · `LoftspaceRecordCharge` · `CreditAccount` · `ReturnDeposit` (Weaver-dispatched) · `RecordDepositDeduction` · `PayOutBalance` · `EvaluateLoftspaceArrears` (Weaver-dispatched) · `RecordLoftspaceArrearsReminderNotification` (bridge replyOp) |
| **Projection lenses** (2) | `ledgerHistory` (one row per transaction, incl. `kind`) → `loftspace-ledger-history` · `leaseAccounts` (lease → account key lookup + the account's `arrearsDueAt` / `arrearsRemindedFor` / `arrearsReminderSentAt`) → `loftspace-lease-accounts` (both `nats-kv`, `full` engine) |
| **Weaver targets** (1) | `loftspaceArrearsReminders` (one row per account) → `weaver-targets`; playbook `missing_evaluation → directOp EvaluateLoftspaceArrears` |

`DebitAccount` — the clause-authorized charge Weaver's `clauseSatisfaction` playbook dispatches — is
granted to `operator` only at `scope: any`; `LoftspaceRecordCharge` (a person's manual charge, never
clause-authorized), `CreditAccount`, `RecordDepositDeduction` and `PayOutBalance` additionally grant
`consumer` at `scope: self`, proven in `scripts.go`'s shared `self_scope_standing` helper off the
account's own `heldFor` topology: the lease's `applicationFor` holder (the resident) may credit only,
capped at the outstanding balance (`CreditAccount`), and is refused outright on the other three; the
holder of a `manages` link to the lease's `appliesToUnit` unit (the landlord) may charge and credit
uncapped, deduct from a held deposit, and pay a credit balance out — the operationType is a global
namespace, so the landlord's charge carries a vertical-unique name rather than a self grant on the
`DebitAccount` name `cafe-ledger` also admits (`permissions.go`). `LoftspaceCreateAccount` also grants `frontOfHouse`,
**workplace-confined** to the lease's own building (`scripts.go`'s `require_workplace` on the
lease's `appliesToUnit` topology — unlike `clinic-ledger`'s identical create op, a leaseapp sits at
a unit, so this one cannot be granted unconfined) — the front desk opens a lease's ledger account
directly from the browser.

## Key shapes (Contract #1)

```
vtx.account.<id>                    class=account       root {} (D5 — balance is lens-derived)
vtx.transaction.<id>                class=transaction   root {} (D5)
vtx.transaction.<id>.entry          class=entry          {type ∈ debit|credit|deduction, kind?, amountCents, memo?, postedAt, periodStart?, periodEnd?, dueAt?}
vtx.leaseapp.<id>.ledgerAccount     class=ledgerAccountGuard  {accountKey}  (the uniqueness guard)
vtx.account.<id>.arrears            class=loftspaceAccountArrears  {evaluatedAt, dueAt?, remindAt?, remindedFor?, sentAt?, stale?, historyTooLong?, historyBudget?, replay?}
vtx.account.<id>.arrearsNotification class=loftspaceAccountArrearsNotification  {status, remindedFor, sentAt}
vtx.clause.<id>.deductions          class=depositDeductions  {totalCents, count, lastRecordedAt}  (this package's own aspect on a semantic-contracts clause)

lnk.account.<id>.heldFor.leaseapp.<id>        (account → leaseapp; account is the later-arriving vertex)
lnk.transaction.<id>.postedTo.account.<id>    (transaction → account; transaction is the later-arriving vertex)
```

## Independent account NanoID + guard aspect

`LoftspaceCreateAccount` mints the account under its **own independently-generated NanoID** — never reused
from the lease, since Core KV NanoIDs are unique platform-wide identifiers, not scoped per vertex
type. "At most one account per lease" is enforced by the deterministic create-only
`ledgerAccountGuard` aspect on the **leaseapp** (`leaseAppKey + ".ledgerAccount"`) instead of a
shared/derived key: a second `LoftspaceCreateAccount` for the same lease conflicts on that already-existing
aspect key. This mirrors `clinic-ledger`'s account/patient shape (the account held for a lease
instead of a patient); see
[`adjacency-shared-nanoid-collision-design.md`](../../_bmad-output/implementation-artifacts/adjacency-shared-nanoid-collision-design.md)
for why the account carries its own id rather than the lease's.

## Append-only ledger + the clause seam

`DebitAccount`/`LoftspaceRecordCharge`/`CreditAccount`/`ReturnDeposit`/`RecordDepositDeduction`/`PayOutBalance`
each mint a fresh `vtx.transaction.<id>` with a `.entry` aspect and the `postedTo` link back to the
account — no balance field is ever written or mutated; the `ledgerHistory` lens derives a balance by
summing `amountCents` (positive for debit, negative for credit; a `deduction` entry contributes to
neither direction) client-side, so concurrent debits/credits never race a read-modify-write.

`CreditAccount`'s resident branch and `PayOutBalance` additionally carry a bare, content-unchanged
update of the account **root** vertex alongside their transaction mint: the balance/payout amount
each computes is derived from the account's `postedTo` history, not stored anywhere, so two concurrent
submits against the same account otherwise share no written key and both commit independently. The
root update gives them one — a lost race re-hydrates and re-executes against the winner's now-committed
history (Contract #3 §3.2) rather than a second self-credit or payout landing on stale numbers.
`LoftspaceRecordCharge` and the landlord's uncapped `CreditAccount` branch skip it: the landlord is the
sole creditor on that path, so there is no concurrent write it needs to serialize against.

`DebitAccount`'s optional `clauseRef` additionally writes the `authorizedBy` audit link
(transaction → clause) and updates the clause's `.status` — `completed` for a one-time clause, or
`chargeValidUntil` re-armed for a `period: monthly` clause — the `semantic-contracts` Executable
Paper package's canonical `directOp` consumer of this ledger. `chargeValidUntil` is the clause's next
due date: for a clause whose `.terms` carry `validFrom`/`validUntil` (a rent clause) the due dates walk
the calendar-month anniversary grid from `validFrom` — each charge bills the period starting at the
recorded due (read as an OptionalRead on `.status`) and records the next anniversary, computed from
`validFrom` every time so the day-of-month never drifts; the charge whose next due reaches `validUntil`
marks the clause `completed`, and a due already at `validUntil` is refused (`TermExhausted`). An
untermed monthly clause keeps the `postedAt + RecurringChargePeriod` (720h) cadence.

## Rent arrears: "how late", and one reminder per episode

The ledger stores no balance and no arrears state on the account root, so the wellness-ledger arrears
mechanism (a ledger that likewise stores no balance) is applied here, with one difference in where the
dates come from. `EvaluateLoftspaceArrears{accountKey}` — dispatched by Weaver's
`loftspaceArrearsReminders` playbook, and refused for any other actor — replays the account's own
`postedTo` history ONE PAGE PER DISPATCH (30 entries a page, `ArrearsPageLimit`; up to 20 pages,
`ArrearsMaxPages` — 600 entries total; past it the evaluation records `historyTooLong` with the
`historyBudget` it exhausted and goes quiet rather than refusing, and a later, larger budget re-arms an
account a smaller one parked). A history longer than one page leaves a checkpoint on
`.arrears.replay` (`{phase, cursor, pages, entries}` — every debit and credit read so far keyed by its
bare transaction ID (never its full vtx key) and its own recorded postedAt, no netting or collapsed
total, since the episode-start computation below needs each entry's real timing) that the next
dispatch — chained by
the lens's `missing_replay_a` / `missing_replay_b` gaps — resumes from; the finalize page runs the plain
FIFO the tenant's statement runs (no netting pre-pass: no entry in this ledger names a charge it
reverses) over the whole set, and writes `vtx.account.<id>.arrears`:

- **`dueAt`** is the FIFO-oldest open charge's **own recorded `.entry.dueAt`** — the date a
  clause-authorized rent charge carries from its anniversary grid — or its `postedAt` when it recorded
  none (a landlord one-off is due on receipt). Never a term added to the posting; "N days overdue"
  counts from it.
- **`remindAt`** = `dueAt + ArrearsGraceDays` (5 days, `scripts.go`): where the reminder timer arms, so a
  charge that posts on its due date does not nag the same morning.
- **`remindedFor` = `dueAt`** once `remindAt` has passed (closes the convergence gap); **`sentAt`** once per
  episode, on the commit that emits `external.notification` keyed `<accountKey>:<dueAt>:<headTransactionKey>`
  (the head's own key makes the episode key unique — recorded due dates repeat across charges on one
  lease; `replyOp RecordLoftspaceArrearsReminderNotification`, params `{accountKey, reminderType:
  "loftspaceRentArrears", dueAt, balanceCents, leaseAppKey?, identityKey?}` — the lease and tenant
  resolved live off `heldFor` → live lease → `applicationFor`, never the payload, and omitted when a
  withdrawn lease dangles).
- `{evaluatedAt}` alone when nothing is owed — the episode ends only here; a `sentAt` before the
  `postedAt` of the charge that opened the current episode (the debit that took the account from square
  to owing — not necessarily the head, which a partial payment can move past it) is dropped as a
  finished episode's.

## The deposit: deduction and return

`RecordDepositDeduction{accountKey, clauseKey, amountCents, reason}` (operator, or the landlord's
`consumer scope: self`, the same `self_scope_standing` proof `CreditAccount`/`PayOutBalance` run — a
resident submit is refused `AuthDenied`) takes a deduction off a charged, still-held deposit clause
before it is returned. It proves the clause's own custody (`chargesTo` / `purpose: deposit` / one-time
computational — `ClauseAccountMismatch` / `NotADeposit`) and `.status: completed` (`DepositNotHeld`
otherwise — `active` means not yet charged, `returned` means already gone), refuses
`DeductionExceedsDeposit` once the running total on the clause's own `.deductions` aspect plus this
amount would exceed the clause's own `amountCents`, and mints a transaction with `.entry {type:
deduction, amountCents, postedAt, memo: reason}` plus the `postedTo` and `authorizedBy` links. It then
creates (first deduction) or bare-updates (later ones) the clause's own `.deductions` aspect
(`{totalCents, count, lastRecordedAt}`) — never `expectedRevision`-pinned, so two concurrent deductions
both land under the §3.2 re-hydrate retry. A deduction moves no FIFO and marks no `.arrears` stale; a
deduction is neither a debit nor a credit, so every balance reader that tests the type explicitly (the
resident self-credit walk, the arrears FIFO, `ledgerHistory`'s summed balance) ignores it.

`ReturnDeposit{leaseAppKey, clauseKey, accountKey}` is the deposit's way back — `semantic-contracts`'
`leaseRentSettlement` playbook dispatches it (`missing_depositReturn`) once the lease's `.tenancy`
records `endedAt` and its `purpose: deposit` clause is `completed` (charged). It reads everything from
the graph's own record: the clause's `.terms` (`NotADeposit` without the purpose token; the amount is
the clause's own), its `.status` (`DepositNotCharged` while still `active`; a `returned` clause is an
idempotent no-op), the lease's `.tenancy` (`TenancyNotEnded` without `endedAt`) and the clause's own
`chargesTo` / `governs` links (`ClauseAccountMismatch` / `ClauseLeaseMismatch`). It credits the NET of
the clause's full amount less whatever `RecordDepositDeduction` has already taken off it, read from the
clause's own `.deductions` aspect (absent = never deducted) — a net below zero (deductions exceeding the
clause's own amount) is refused `InvalidState`, never a negative posting. A positive net posts one
credit `authorizedBy` the clause; a net of exactly zero mints no transaction at all (a zero-amount entry
is not a transaction) but still emits the event, with `amountCents: 0` and no `transactionKey`. Either
way it also writes the clause's own `.deductions` aspect — a bare update carrying the hydrated total
unchanged where one exists, or a `{totalCents: 0, count: 0, lastRecordedAt: postedAt}` create where none
does — purely as a serialization anchor: since a deduction reads `.status` but writes `.deductions`,
and a return reads `.deductions` but writes `.status`, the two ops would otherwise share no written key
and a boundary-time deduction could land on an already-returned clause. Writing `.deductions` from both
sides means a lost race re-hydrates and re-executes against the other's now-committed state instead of
committing blind. `ledgerHistory` projects `clausePurpose` so a statement holds the deposit apart from
rent. Operator-only, no self grant, no screen; the DDL's `derive_reads` hydrates its whole read set,
including `.deductions`, from the payload keys.

`PayOutBalance{accountKey, leaseAppKey}` (operator, or the landlord's `consumer scope: self`, same
proof) pays an ended tenancy's whole credit balance out to the tenant: the amount is computed op-side
from the account's own `postedTo` history (the same recomputation the resident self-credit cap uses),
never trusted from the payload. It proves the account's own `heldFor` link names the payload lease
(`AccountLeaseMismatch` otherwise), the lease's `.tenancy` records `endedAt` (`TenancyNotEnded`
otherwise), and refuses `NoCreditBalance` once the recomputed balance is not negative. It mints a
transaction with `.entry {type: debit, kind: payout, amountCents: the credit balance's magnitude,
postedAt}` and the `postedTo` link (no `authorizedBy` — no clause authorizes it). `kind` is the
recorded provenance of a debit no clause authorizes; `ledgerHistory` and the one-bill `rentEntries`
lens project it so a statement labels the row by this recorded fact, never by its memo.

Every posted entry (`DebitAccount` / `LoftspaceRecordCharge` / `CreditAccount` / `ReturnDeposit` /
`PayOutBalance`) carries the existing `.arrears` forward and marks it `stale` (dropping
`historyTooLong`, `historyBudget` and `replay` — a posted entry changes the set a live checkpoint's
cursor pages over), minting nothing when absent; every script's `derive_reads` hydrates `[account,
account.arrears]` so the upsert stays OCC for a submitter that declared nothing. `RecordDepositDeduction`
is the one op that posts an entry without touching `.arrears` at all — a deduction moves no FIFO, so
there is nothing for an arrears episode to react to. `leaseAccounts` projects `arrearsDueAt` /
`arrearsRemindedFor` / `arrearsReminderSentAt` for the landlord ledger, the tenant statement and the
portfolio list.

## Where the ledger is surfaced

`ledgerHistory` is the FE's payment-history read model (P5); `leaseAccounts` is the only way the FE
resolves a lease's account key, since it is no longer derivable from `leaseAppKey` (the independent
NanoID above) — a lease with no account yet still gets a row (`accountKey` null).

## Out of scope

- **A stored/cached balance** — deliberately never materialized; always summed from `ledgerHistory`.
- **Refunds / voids as a distinct operation** — model as an offsetting `CreditAccount`/`DebitAccount`
  entry with an explanatory `memo` today; a dedicated reversal op is not yet needed.
