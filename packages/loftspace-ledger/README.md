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
| **Aspect types** (3) | `ledgerAccountGuard` — `vtx.leaseapp.<id>.ledgerAccount`, the per-lease create-only uniqueness guard · `loftspaceAccountArrears` — `vtx.account.<id>.arrears`, the arrears-episode state · `loftspaceAccountArrearsNotification` — `vtx.account.<id>.arrearsNotification`, the reminder's delivery outcome |
| **Links** (2) | `heldFor` (account → leaseapp) · `postedTo` (transaction → account) |
| **Operations** (6) | `LoftspaceCreateAccount` · `DebitAccount` · `LoftspaceRecordCharge` · `CreditAccount` · `EvaluateLoftspaceArrears` (Weaver-dispatched) · `RecordLoftspaceArrearsReminderNotification` (bridge replyOp) |
| **Projection lenses** (2) | `ledgerHistory` (one row per transaction) → `loftspace-ledger-history` · `leaseAccounts` (lease → account key lookup + the account's `arrearsDueAt` / `arrearsRemindedFor` / `arrearsReminderSentAt`) → `loftspace-lease-accounts` (both `nats-kv`, `full` engine) |
| **Weaver targets** (1) | `loftspaceArrearsReminders` (one row per account) → `weaver-targets`; playbook `missing_evaluation → directOp EvaluateLoftspaceArrears` |

`DebitAccount` — the clause-authorized charge Weaver's `clauseSatisfaction` playbook dispatches — is
granted to `operator` only at `scope: any`; `LoftspaceRecordCharge` (a person's manual charge, never
clause-authorized) and `CreditAccount` additionally grant `consumer` at `scope: self`, proven in
`scripts.go` off the account's own `heldFor` topology: the lease's `applicationFor` holder (the resident)
may credit only, capped at the outstanding balance; the holder of a `manages` link to the lease's
`appliesToUnit` unit (the landlord) may charge and credit, uncapped — the operationType is a global
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
vtx.transaction.<id>.entry          class=entry          {type ∈ debit|credit, amountCents, memo?, postedAt}
vtx.leaseapp.<id>.ledgerAccount     class=ledgerAccountGuard  {accountKey}  (the uniqueness guard)
vtx.account.<id>.arrears            class=loftspaceAccountArrears  {evaluatedAt, dueAt?, remindAt?, remindedFor?, sentAt?, stale?, historyTooLong?}
vtx.account.<id>.arrearsNotification class=loftspaceAccountArrearsNotification  {status, remindedFor, sentAt}

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

`DebitAccount`/`LoftspaceRecordCharge`/`CreditAccount` each mint a fresh `vtx.transaction.<id>` with a `.entry` aspect and
the `postedTo` link back to the account — no balance field is ever written or mutated; the
`ledgerHistory` lens derives a balance by summing `amountCents` (positive for debit, negative for
credit) client-side, so concurrent debits/credits never race a read-modify-write.

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
`postedTo` history under a bounded budget (50 × 10 entries; past it the evaluation records
`historyTooLong` and goes quiet rather than refusing), runs the plain FIFO the tenant's statement runs
(no netting pre-pass: no entry in this ledger names a charge it reverses), and writes
`vtx.account.<id>.arrears`:

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

Every posted entry (`DebitAccount` / `LoftspaceRecordCharge` / `CreditAccount`) carries the existing
`.arrears` forward and marks it `stale` (dropping `historyTooLong`), minting nothing when absent; both
scripts' `derive_reads` hydrate `[account, account.arrears]` so the upsert stays OCC for a submitter
that declared nothing. `leaseAccounts` projects `arrearsDueAt` / `arrearsRemindedFor` /
`arrearsReminderSentAt` for the landlord ledger, the tenant statement and the portfolio list.

## Where the ledger is surfaced

`ledgerHistory` is the FE's payment-history read model (P5); `leaseAccounts` is the only way the FE
resolves a lease's account key, since it is no longer derivable from `leaseAppKey` (the independent
NanoID above) — a lease with no account yet still gets a row (`accountKey` null).

## Out of scope

- **A stored/cached balance** — deliberately never materialized; always summed from `ledgerHistory`.
- **Refunds / voids as a distinct operation** — model as an offsetting `CreditAccount`/`DebitAccount`
  entry with an explanatory `memo` today; a dedicated reversal op is not yet needed.
