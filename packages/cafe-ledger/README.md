# cafe-ledger

The Café house-tab payment ledger — a per-lease financial account that records café charges,
payments, write-offs, refunds and cash payouts as a transaction history no posted entry's money fields
are ever rewritten in.
The **displayed** balance is derived by summing entries (`cafeLedgerHistory`); the account
additionally carries a maintained `.balance` aspect, an O(1) authorization cache that exists so a
payment can be capped at what is actually owed without replaying a long house tab.

Depends: `lease-signing` (the `leaseapp` vertex type an account is `heldFor` — the same resident lease
`loftspace-ledger`'s own rent account anchors to; a house tab belongs to the same lease) and
`orchestration-base` (`MarkExpired` and the `freshnessExpiry` marker the arrears `@at` firing writes onto
the account). Install: `lattice-pkg install packages/cafe-ledger` (after both).

## Increment 1 of 3 (Café vertical, `verticals.md`)

This package shipped the ledger primitive alone. `cafe-domain` (the `OpenTab`/`Charge`/`Settle` tab
lifecycle) is Increment 2's domain half, posting into this ledger through a Weaver playbook via
`DebitAccount`'s and `CreditCafeAccount`'s `tabRef` (below) — never a direct cross-package write. Both halves have shipped,
including the café FE (`cmd/cafe-app`). The one-bill composition lens unioning `ledgerHistory` +
`cafeLedgerHistory` by `leaseAppKey` is Increment 3. See
[`cafe-ledger-design.md`](../../_bmad-output/implementation-artifacts/cafe-ledger-design.md).

## Inventory

| Kind | Canonical names |
|---|---|
| **Vertex types** (2) | `cafeaccount` (root `{}`, D5, `.balance` aspect) · `cafetransaction` (root `{}`, D5, `.entry` aspect) |
| **Aspect types** (4) | `cafeLedgerAccountGuard` — `vtx.leaseapp.<id>.cafeLedgerAccount`, the per-lease create-only uniqueness guard · `cafeAccountBalance` — `vtx.cafeaccount.<id>.balance`, the maintained running total · `cafeAccountArrears` — `vtx.cafeaccount.<id>.arrears`, the arrears-episode state · `cafeAccountArrearsNotification` — `vtx.cafeaccount.<id>.arrearsNotification`, the reminder's audit-only outcome |
| **Links** (4) | `heldFor` (cafeaccount → leaseapp) · `postedTo` (cafetransaction → cafeaccount) · `settles` (cafetransaction → tab: the charge that settled a `cafe-domain` tab, and the counter payment for the cash the desk took as it was settled) · `reverses` (cafetransaction → cafetransaction, a refund to the charge it gives back) |
| **Operations** (7) | `CreateAccount` · `DebitAccount` (optional `tabRef` — `cafe-domain`'s settlement consumer) · `CreditCafeAccount` (optional `reason` — `payment` or the staff-only `waiver`; optional staff-only `tabRef` — the settlement playbook's counter-payment consumer) · `RefundCafeCharge` · `PayoutCafeCredit` · `EvaluateCafeArrears` (Weaver-dispatched) · `RecordCafeArrearsReminderNotification` (bridge replyOp) |
| **Projection lenses** (2) | `cafeLedgerHistory` (one row per transaction) → `cafe-ledger-history` · `cafeLeaseAccounts` (lease → account key lookup, plus the account's arrears due date / reminder timestamp) → `cafe-lease-accounts` (both `nats-kv`, `full` engine) |
| **Weaver targets** (1) | `cafeArrearsReminders` — its own convergence lens → `weaver-targets`; three gaps, `missing_evaluation` and the two replay-continuation gaps `missing_replay_a` / `missing_replay_b`, all → `directOp(EvaluateCafeArrears)` |

`CreateAccount` and `DebitAccount` are granted to `operator` alone at `scope: any`
(`permissions.go`) — both are orchestrator-submitted, neither is something a person decides to do.
`CreditCafeAccount` also grants `frontOfHouse`, because recording a payment (or writing a balance off)
is a front-desk act with a human on the other side of the counter; a staffer holding it is confined by
`transactionDDLScript` to accounts whose lease sits at a location they `worksAt`, while `operator` stays
unconfined. `RefundCafeCharge` and `PayoutCafeCredit` grant the same pair on the same terms, and neither
grants a consumer at any scope. Those three are the ops carrying an `OpMetaSpec` descriptor, for the
same reason — all are person-triggered; `CreateAccount` and `DebitAccount` are orchestrator-submitted
and carry none.

## Key shapes (Contract #1)

```
vtx.cafeaccount.<id>                     class=cafeaccount       root {} (D5 — the DISPLAYED balance is lens-derived)
vtx.cafeaccount.<id>.balance             class=cafeAccountBalance {balanceCents, cashCents}  (the maintained authorization cache: what is owed, and the net cash paid in)
vtx.cafetransaction.<id>                 class=cafetransaction   root {} (D5)
vtx.cafetransaction.<id>.entry           class=transactionEntry   {type ∈ debit|credit, amountCents, memo?, postedAt, reason?, refundedCents?}
vtx.leaseapp.<id>.cafeLedgerAccount      class=cafeLedgerAccountGuard  {accountKey}  (the uniqueness guard)

lnk.cafeaccount.<id>.heldFor.leaseapp.<id>          (cafeaccount → leaseapp; cafeaccount is the later-arriving vertex)
lnk.cafetransaction.<id>.postedTo.cafeaccount.<id>  (cafetransaction → cafeaccount; cafetransaction is the later-arriving vertex)
lnk.cafetransaction.<id>.settles.tab.<id>           (cafetransaction → tab; DebitAccount's / CreditCafeAccount's optional tabRef audit link)
lnk.cafetransaction.<id>.reverses.cafetransaction.<id>  (refund → the charge it gives back; the refund is the later-arriving vertex)
```

## Independent account NanoID + guard aspect (and why the guard's LOCAL NAME is prefixed too)

`CreateAccount` mints the account under its **own independently-generated NanoID** — never reused
from the lease, since Core KV NanoIDs are unique platform-wide identifiers, not scoped per vertex
type ([`adjacency-shared-nanoid-collision-design.md`](../../_bmad-output/implementation-artifacts/adjacency-shared-nanoid-collision-design.md)).
"At most one café account per lease" is enforced by a deterministic create-only guard aspect on the
**leaseapp** — but unlike `loftspace-ledger` / `clinic-ledger`, whose guard aspects anchor to
*different* vertex types (`leaseapp` vs `patient`) and so never collide, `cafe-ledger` anchors to the
**same `leaseapp`** `loftspace-ledger` already anchors to. Reusing the local name `ledgerAccount`
would silently collide key-for-key with `loftspace-ledger`'s own
`vtx.leaseapp.<id>.ledgerAccount` aspect on that same vertex. The fix: the guard's **local name**,
not just its class, is vertical-prefixed — `vtx.leaseapp.<id>.cafeLedgerAccount` (class
`cafeLedgerAccountGuard`) — distinct from `loftspace-ledger`'s `vtx.leaseapp.<id>.ledgerAccount`
(class `ledgerAccountGuard`) on the identical vertex. The `heldFor` **link** needs no such fix — its
key embeds the *source* vertex's own type (`cafeaccount` vs `account`), so it is already distinct.

## Two balances, and why

`DebitAccount`/`CreditCafeAccount`/`RefundCafeCharge`/`PayoutCafeCredit` each mint a fresh
`vtx.cafetransaction.<id>` with a `.entry` aspect and the `postedTo` link back to the account. A posted
entry's own money fields are never rewritten, and the `cafeLedgerHistory` lens derives the **displayed**
balance by summing `amountCents` (positive for debit, negative for credit) client-side — that
independent sum stays the display source of truth and never reads the cache below.

`.entry.reason` says *why* a line was posted and never changes that arithmetic. A credit is `payment`
(cash collected — `CreditCafeAccount`'s default), `waiver` (debt forgiven — `CreditCafeAccount` with
`reason: waiver`, staff only) or `refund` (written by `RefundCafeCharge` itself); a debit carries no
reason (a charge) or `payout` (written by `PayoutCafeCredit` itself). Only `CreditCafeAccount` accepts
a payload `reason`; every other op refuses one (`InvalidArgument`) rather than ignore it, the same
cross-refusal `tabRef` and `reversesRef` get. The lens projects `reason` so a statement can badge a
line "Waived", "Refund" or "Paid out" without ever mistaking forgiven debt for money received.

Each of those four ops **also** moves the account's own `.balance` aspect by the signed amount, via a
bare `update` carrying **no** `expectedRevision` of its own. The Processor auto-conditions such an
update on the revision the key was hydrated at (Contract #3 §3.2) and marks it retry-eligible, so two
concurrent entries against one account serialize and retry rather than silently dropping an update —
but only for a key the operation DECLARED. `contextHint` is submitter-supplied and nothing enforces
it, so the declaration cannot be left to the caller: the transaction DDL's own `derive_reads(op)`
(Contract #2 §2.5 class (g)) returns `<accountKey>.balance` under `optionalReads` on every dispatch of
all four ops, and the dispatchers declare the same key besides. That O(1) cache is what lets
`CreditCafeAccount` cap a payment at the outstanding balance, and `PayoutCafeCredit` cap a payout at the
credit held, without replaying a long house tab.

`.balance` is declared **optional**, not required, because an account minted under `cafe-ledger`
< 0.4.0 does not carry one — a closed set, since `CreateAccount` mints the aspect, so no account
opened today joins it.
Only the legs whose guard needs a number — a **payment** (or write-off), a **refund** and a
**payout** — ever pay for the one-time bounded replay of that account's `postedTo` history: a charge
against such an account posts and writes no `.balance` at all, leaving it legacy until one of those
first touches it — and that replay sums the whole history, those later entries included, so the cache
is never seeded from a partial sum.

`.balance` carries a second maintained field beside `balanceCents`: **`cashCents`, the net cash the
account has paid in** — payments add, payouts subtract, and a charge, a write-off or a refund leaves it
alone. Both fields ride every `.balance` write. A live document *without* `cashCents` predates the
field; its absence means "not yet computed", never zero, and the next payment, refund or payout
computes it from the same replay (a reason-less credit from before `reason` existed counts as a
payment exactly when no `reverses` link leaves it) and writes it, while a charge leaves it absent. That asymmetry is deliberate: the tab-settlement playbook's `DebitAccount`
dispatch runs unattended and must never be wedged by an account whose history outgrew the replay
budget. Every touch after the aspect exists is O(1). Nothing outside this package reads it.

## The payment cap

`CreditCafeAccount` refuses an `amountCents` larger than the account's outstanding balance
(`PaymentExceedsBalance`), and refuses any payment against an account that owes nothing
(`NoBalanceToPay`). The cap binds the **operation**, not the caller: no payment rail on this platform
witnesses the amount, and that is as true of a number a front-desk staffer keys under the `scope: any`
grant as of one a resident types under `scope: self`. An uncapped payment writes off debt the café is
owed, and a mis-keyed one hides the resident behind a balance that reads as paid ahead.

A **write-off** (`reason: waiver`) is the same credit under the same cap — forgiving more than is owed
would put the resident in credit the café then owes — and only its refusal code and message read
differently (`WriteOffExceedsBalance`, "a write-off of $X exceeds the outstanding balance of $Y"). It
is staff-only: a resident's self-scoped submit carrying `reason: waiver` is refused `AuthDenied` before
the ownership walk, because forgiving a debt is the café's call and never the debtor's.

A payment may never exceed what is owed on **any** leg: there is no pay-ahead deposit on a house tab.
A credit surplus arises only from a refund, and the resident statement's FIFO carry
(`cmd/cafe-app/ledger.go`, `deriveStatement`) exists for that case — or the desk settles it in cash
with `PayoutCafeCredit` (below).

`RefundCafeCharge` is deliberately **not** balance-capped — its ceiling is the reversed charge's own
un-refunded remainder (below), so giving back a charge the resident already paid legitimately takes
`.balance` negative. Capping a refund at the balance would make the one case a refund exists for the
one case it could not handle.

## The cash invariant

**An account's credit may never exceed the net cash it has paid in.** Without it, a written-off charge
could be refunded: write off $18 (balance 0), refund that charge (its `refundedCents` is untouched by a
waiver) to −$18, and pay $18 out — cash handed over for a charge nobody paid. The floor is `cashCents`,
enforced where the credit is minted: `RefundCafeCharge` refuses `RefundExceedsPaid` when the balance
it would leave is further below zero than `cashCents` ("a refund of $X would leave this account $Y in
credit, more than the $Z it has paid in"). A refund of an *unpaid* charge lands at zero and passes; a
refund of a paid charge lands at a credit the cash covers and passes; a refund of a partially-paid
charge is bounded by what was actually paid; only forgiven debt is turned away. `PayoutCafeCredit`
carries the same floor as defence in depth (`PayoutExceedsCash`) — it never fires while the invariant
holds, and stands so the floor is enforced where cash actually leaves as well.

The other half of the loop is closed at `reversesRef`: only a posted **charge** — a debit with no
`reason` — can be refunded. A payout is a debit too, and one with no `refundedCents` tally, so a
refund that could name it would hand the credit straight back: charge, pay, refund, pay out, refund the
payout, pay out again, without end.

## `PayoutCafeCredit` — settling a credit in cash

A refund of a charge the resident had **already paid** leaves the account in credit: the café owes
money out, and that credit is either spent — the resident statement's FIFO carry prepays their later
tabs with it — or handed back in cash by the desk. Whether cash changes hands is not a property of the
refund — a refund of an *unpaid* charge moves no cash and correctly nets the balance to zero — and
under the FIFO a partially-paid history makes "was this charge paid" ambiguous at refund time. The cash
fact is known only when the desk hands it over, so it is its own entry: `PayoutCafeCredit{accountKey,
amountCents, memo?}` posts an ordinary **debit** with `reason: payout`, capped at the credit the account
holds, so Σdebit−Σcredit returns to zero — the credit was settled in cash, not spent. Every balance
consumer sums it unchanged, and `cashCents` moves down by the amount handed over.

The cap is the payment cap's mirror: `NoCreditToPayOut` when the account owes or is square
(`.balance >= 0`), `PayoutExceedsCredit` past the credit, `PayoutExceedsCash` past the cash paid in (the
invariant above, re-enforced at the till). Every refusal spells money, never a key. Because the balance
a payout leaves is at most zero, the arrears episode branch never opens — a payout **never writes
`.arrears`**, and is absent from that DDL's `PermittedCommands` while present on `.balance`'s. It
backfills a legacy account exactly as a payment does (its cap needs the number just the same), it is
granted to `operator` and `frontOfHouse` at `scope: any` with **no** consumer grant, it refuses a
self-scoped submit outright, it can never itself be refunded (`reversesRef` refuses a debit carrying a
reason), and it is workplace-confined like a payment.

## Where the ledger is surfaced

`cafeLedgerHistory` is the FE's house-tab payment-history read model (P5); `cafeLeaseAccounts` is the
only way the FE resolves a lease's café account key, since it is no longer derivable from
`leaseAppKey` (the independent NanoID above) — a lease with no café account yet still gets a row
(`accountKey` null).

## `tabRef` — the `cafe-domain` settlement back-links

`DebitAccount` and `CreditCafeAccount` accept an optional `tabRef` (`vtx.tab.<NanoID>`, validated
alive when supplied; refused `InvalidArgument` on `RefundCafeCharge` and `PayoutCafeCredit`). When
present the entry writes `lnk.cafetransaction.<id>.settles.tab.<id>` (mirroring `loftspace-ledger`'s
`clauseRef`/`authorizedBy` precedent), one relation for both entry types — *transaction settles tab*
reads for a payment as well as a charge — and `cafe-domain`'s `cafeTabSettlement` lens discriminates
by `.entry.type`:

- a **charge** (debit) is what `missing_charge` counts to detect a settled tab's charge has posted;
- a **payment** (credit) is what `missing_payment` counts to detect the cash the desk took at settle
  (`Settle{paidCents}`, recorded on the tab as `paidAtSettleCents`) has posted. The playbook opens that
  gap only once a settling debit exists, so the payment always lands inside the balance the charge
  opened and the payment cap (below) never refuses it for cash the resident has already handed over.

`tabRef` on a credit is staff-only: a resident's self-scoped `CreditCafeAccount` carrying one is refused
`AuthDenied` — a settles link on their own payment would read to the lens as the desk's counter payment
already posted, closing the gap before the cash the desk recorded ever reached the ledger.

A tab-tied credit is **bounded by the tab's recorded fact, not by the balance cap**
(`require_counter_payment`, `scripts.go`): `reason` must be `payment` (a write-off pays for no tab —
`InvalidArgument`); the tab's `.status` — a declared read, `row.tabKey.status` on the playbook dispatch and
derived by this DDL's own `derive_reads` for any submitter — must be `settled` (`TabNotSettled`) and record
`paidAtSettleCents` (`NoCounterPayment`) equal to `amountCents` (`CounterPaymentMismatch`); the tab's
`leaseAppKey` must be the account's own `heldFor` lease, recovered from the account's topology exactly as
the resident-self proof does (`AuthDenied`); and no credit may already `settles` the tab
(`CounterPaymentAlreadyPosted` — the replay dedup, read off the tab's inbound `settles` entries). With those
proven the credit is exempt from `NoBalanceToPay` / `PaymentExceedsBalance`: the cash is real and was
bounded at `Settle` to the tab's total, a resident's own self-payment landing between the charge and the
counter payment can no longer make it refusable, and an account legitimately goes further into credit —
`cashCents` rises by the same amount, so the cash floor still bounds any later `PayoutCafeCredit`. Every
credit without `tabRef` keeps the cap unchanged. A plain human-submitted entry omitting `tabRef` is
byte-for-byte unaffected.

## `reversesRef` — refunding a posted charge

`RefundCafeCharge` posts an ordinary credit entry (so every balance consumer sums it unchanged) plus a
`reverses` link back to the charge it corrects — the link is the refund's whole identity, and what lets
a statement say "this line is a correction" rather than "the resident handed money over". The
reference must name a live `cafetransaction` whose `.entry` is a **debit** posted to the **same**
account, and `amountCents` may not exceed that charge's own amount minus its `refundedCents` tally.
That tally is maintained on the charge's own `.entry` under a compare-and-set pinned to the revision it
was hydrated at, so two refunds racing one charge serialize: the loser is refused, never admitted
alongside the winner. A refund is a front-desk act and is never self-scoped.

## The arrears reminder

Nothing used to tell a resident they owed the café money. `cafeArrearsReminders` is the target that does,
and it is this package's first orchestration.

The account carries a `.arrears` aspect (`{evaluatedAt, dueAt?, remindedFor?, sentAt?, stale?,
historyTooLong?, historyBudget?, replay?}`) whose whole lifetime is coarse on purpose. A charge that takes the balance from
zero-or-below to **owing** IS the FIFO-oldest open charge, so `post_entry` can record the due date its own
`postedAt` implies (`postedAt` + `ArrearsGraceDays`) without replaying anything; a payment that clears the
balance rewrites the aspect to `{evaluatedAt}` alone and ends the episode. Everything in between it refuses to
guess: a **partial** payment can move the head to a later charge with a later due date, a question only the
whole history answers, so the entry marks the state `stale` instead. A charge against an account already in
credit (a refund took it below zero) writes nothing at all — the surplus prepays it outright, so there is no
open debit to age. An account carrying no `.balance` at all (the legacy set) can only ever mark stale — it has
no before/after balance to reason from, so it never mints arrears state off a number it does not have.

The lens arms Weaver's `@at` at the recorded `dueAt` and opens its evaluation gap when the timer's lapse is
recorded on the account, when the state is `stale`, or when the account has never been evaluated. All three
dispatch the same remediation, `EvaluateCafeArrears`, which recomputes the head with **the same FIFO the
resident's own statement runs** (`cmd/cafe-app/ledger.go`, `deriveStatement` — credits offset the oldest
still-open charge first, an unapplied credit carries forward as surplus) and rewrites `.arrears`. A recomputed
date that has passed is recorded as `remindedFor` — that is what closes the gap — and where **no reminder has
yet gone out in this episode** (`sentAt` absent) the same commit stamps `sentAt` and fires
`external.notification` to the bridge's `notification` adapter, keyed `<accountKey>:<dueAt>`.

`sentAt`, not `remindedFor`, is the send condition, and the difference is what makes it **one reminder per
arrears episode** rather than one per head. An episode runs from the charge that took the account from square
to owing until the balance returns to zero; within one episode a partial payment retires the oldest charge and
the head moves to the next, whose own term has usually passed too. Keying the send off the head would hand a
resident who is visibly paying their tab down a second nag for the same continuous debt. `sentAt` is carried
across every write of a live episode and dropped only where the episode ends, so a re-dispatch or redelivery
finds it already recorded and emits nothing, and the adapter dedups a genuine redelivery on the episode key.
A resident who pays off and runs a new tab up gets a fresh episode with a clean `sentAt`, and its due date is
necessarily later than any instant the permanent `freshnessExpiry` marker already holds.

The replay is **resumable**: one page of `ArrearsPageLimit` (30) entries per dispatch — sized by round trips
against the Processor's 250 ms script wall, see `scripts.go` — with the running aggregate and cursor recorded
on `.arrears.replay` between pages and Weaver chaining the dispatches through the lens's two phase gaps
(`missing_replay_a` / `missing_replay_b`), so a history of up to `ArrearsPageLimit × ArrearsMaxPages` = 600
entries is evaluated exactly across up to 20 dispatches. A history past that records `historyTooLong` with the
budget it exhausted (`historyBudget`) and goes quiet (no gap, no timer, row still visible) until the next entry
that rewrites the aspect — a payment, or an episode-opening charge — buys one more attempt — or until a raised
budget re-arms it once. A posted entry mid-replay drops the checkpoint and the next evaluation restarts at
page 1.

The op is restricted to **Weaver's dispatch actor**. Its `operator`/`scope: any` grant admits every
operator-role holder, and the account named on the payload is forwarded into a message a resident actually
receives — a wider submitter set is a forged send. `RecordCafeArrearsReminderNotification` records the
adapter's verdict as an audit-only `.arrearsNotification` aspect. Unlike the wellness/clinic precedents it
mirrors, that write is an idempotent **overwrite** rather than create-only: café arrears recur on one account
and every episode replies onto the same key, so a create-only write would reject every outcome after the
first.

The net term lives in one place — the exported `ArrearsGraceDays` constant (`scripts.go`), interpolated into
the Starlark as a duration string and read directly by the FE's statement math.

## Out of scope

- **Reversing a posted payment** — a credit that should not have been recorded has no op that undoes
  it. `RefundCafeCharge` reverses a *charge*, not a payment.
