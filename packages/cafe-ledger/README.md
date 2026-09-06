# cafe-ledger

The Café house-tab payment ledger — a per-lease financial account that records café charges,
payments and refunds as a transaction history no posted entry's money fields are ever rewritten in.
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
`DebitAccount`'s `tabRef` (below) — never a direct cross-package write. Both halves have shipped,
including the café FE (`cmd/cafe-app`). The one-bill composition lens unioning `ledgerHistory` +
`cafeLedgerHistory` by `leaseAppKey` is Increment 3. See
[`cafe-ledger-design.md`](../../_bmad-output/implementation-artifacts/cafe-ledger-design.md).

## Inventory

| Kind | Canonical names |
|---|---|
| **Vertex types** (2) | `cafeaccount` (root `{}`, D5, `.balance` aspect) · `cafetransaction` (root `{}`, D5, `.entry` aspect) |
| **Aspect types** (4) | `cafeLedgerAccountGuard` — `vtx.leaseapp.<id>.cafeLedgerAccount`, the per-lease create-only uniqueness guard · `cafeAccountBalance` — `vtx.cafeaccount.<id>.balance`, the maintained running total · `cafeAccountArrears` — `vtx.cafeaccount.<id>.arrears`, the arrears-episode state · `cafeAccountArrearsNotification` — `vtx.cafeaccount.<id>.arrearsNotification`, the reminder's audit-only outcome |
| **Links** (4) | `heldFor` (cafeaccount → leaseapp) · `postedTo` (cafetransaction → cafeaccount) · `settles` (cafetransaction → tab, the charge that settled a `cafe-domain` tab) · `reverses` (cafetransaction → cafetransaction, a refund to the charge it gives back) |
| **Operations** (6) | `CreateAccount` · `DebitAccount` (optional `tabRef` — `cafe-domain`'s Settle consumer) · `CreditCafeAccount` · `RefundCafeCharge` · `EvaluateCafeArrears` (Weaver-dispatched) · `RecordCafeArrearsReminderNotification` (bridge replyOp) |
| **Projection lenses** (2) | `cafeLedgerHistory` (one row per transaction) → `cafe-ledger-history` · `cafeLeaseAccounts` (lease → account key lookup, plus the account's arrears due date / reminder timestamp) → `cafe-lease-accounts` (both `nats-kv`, `full` engine) |
| **Weaver targets** (1) | `cafeArrearsReminders` — its own convergence lens → `weaver-targets`; one gap, `missing_evaluation` → `directOp(EvaluateCafeArrears)` |

`CreateAccount` and `DebitAccount` are granted to `operator` alone at `scope: any`
(`permissions.go`) — both are orchestrator-submitted, neither is something a person decides to do.
`CreditCafeAccount` also grants `frontOfHouse`, because recording a payment is a front-desk act with a
human on the other side of the counter; a staffer holding it is confined by `transactionDDLScript`
to accounts whose lease sits at a location they `worksAt`, while `operator` stays unconfined. It and
`RefundCafeCharge` are the two ops carrying an `OpMetaSpec` descriptor, for the same reason — both are
person-triggered; `CreateAccount` and `DebitAccount` are orchestrator-submitted and carry none.

## Key shapes (Contract #1)

```
vtx.cafeaccount.<id>                     class=cafeaccount       root {} (D5 — the DISPLAYED balance is lens-derived)
vtx.cafeaccount.<id>.balance             class=cafeAccountBalance {balanceCents}  (the maintained authorization cache)
vtx.cafetransaction.<id>                 class=cafetransaction   root {} (D5)
vtx.cafetransaction.<id>.entry           class=transactionEntry   {type ∈ debit|credit, amountCents, memo?, postedAt, refundedCents?}
vtx.leaseapp.<id>.cafeLedgerAccount      class=cafeLedgerAccountGuard  {accountKey}  (the uniqueness guard)

lnk.cafeaccount.<id>.heldFor.leaseapp.<id>          (cafeaccount → leaseapp; cafeaccount is the later-arriving vertex)
lnk.cafetransaction.<id>.postedTo.cafeaccount.<id>  (cafetransaction → cafeaccount; cafetransaction is the later-arriving vertex)
lnk.cafetransaction.<id>.settles.tab.<id>           (cafetransaction → tab; DebitAccount's optional tabRef audit link)
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

`DebitAccount`/`CreditCafeAccount`/`RefundCafeCharge` each mint a fresh `vtx.cafetransaction.<id>` with a
`.entry` aspect and the `postedTo` link back to the account. A posted entry's own money fields are
never rewritten, and the `cafeLedgerHistory` lens derives the **displayed** balance by summing
`amountCents` (positive for debit, negative for credit) client-side — that independent sum stays the
display source of truth and never reads the cache below.

Each of those three ops **also** moves the account's own `.balance` aspect by the signed amount, via a
bare `update` carrying **no** `expectedRevision` of its own. The Processor auto-conditions such an
update on the revision the key was hydrated at (Contract #3 §3.2) and marks it retry-eligible, so two
concurrent entries against one account serialize and retry rather than silently dropping an update —
but only for a key the operation DECLARED. `contextHint` is submitter-supplied and nothing enforces
it, so the declaration cannot be left to the caller: the transaction DDL's own `derive_reads(op)`
(Contract #2 §2.5 class (g)) returns `<accountKey>.balance` under `optionalReads` on every dispatch of
all three ops, and the dispatchers declare the same key besides. That O(1) cache is what lets
`CreditCafeAccount` cap a payment at the outstanding balance without replaying a long house tab.

`.balance` is declared **optional**, not required, because an account minted under `cafe-ledger`
< 0.4.0 does not carry one — a closed set, since `CreateAccount` mints the aspect, so no account
opened today joins it.
Only a **payment** ever pays for the one-time bounded replay of that account's `postedTo` history,
because a payment is the only leg whose cap needs the number: a charge or a refund against such an
account posts and writes no `.balance` at all, leaving it legacy until a payment first touches it —
and that payment's replay sums the whole history, those later entries included, so the cache is never
seeded from a partial sum. That asymmetry is deliberate: the tab-settlement playbook's `DebitAccount`
dispatch runs unattended and must never be wedged by an account whose history outgrew the replay
budget. Every touch after the aspect exists is O(1). Nothing outside this package reads it.

## The payment cap

`CreditCafeAccount` refuses an `amountCents` larger than the account's outstanding balance, and
refuses any payment against an account that owes nothing (`AuthDenied`). The cap binds the
**operation**, not the caller: no payment rail on this platform witnesses the amount, and that is as
true of a number a front-desk staffer keys under the `scope: any` grant as of one a resident types
under `scope: self`. An uncapped payment writes off debt the café is owed, and a mis-keyed one hides
the resident behind a balance that reads as paid ahead.

A payment may never exceed what is owed on **any** leg: there is no pay-ahead deposit on a house tab.
A credit surplus arises only from a refund, and the resident statement's FIFO carry
(`cmd/cafe-app/ledger.go`, `deriveStatement`) exists for that case.

`RefundCafeCharge` is deliberately **not** balance-capped — its ceiling is the reversed charge's own
un-refunded remainder (below), so giving back a charge the resident already paid legitimately takes
`.balance` negative. Capping a refund at the balance would make the one case a refund exists for the
one case it could not handle.

## Where the ledger is surfaced

`cafeLedgerHistory` is the FE's house-tab payment-history read model (P5); `cafeLeaseAccounts` is the
only way the FE resolves a lease's café account key, since it is no longer derivable from
`leaseAppKey` (the independent NanoID above) — a lease with no café account yet still gets a row
(`accountKey` null).

## `tabRef` — the `cafe-domain` settlement back-link

`DebitAccount` accepts an optional `tabRef` (`vtx.tab.<NanoID>`, validated alive when supplied): when
present it writes `lnk.cafetransaction.<id>.settles.tab.<id>` (mirroring `loftspace-ledger`'s
`clauseRef`/`authorizedBy` precedent) — the audit link `cafe-domain`'s `cafeTabSettlement` lens reads
to detect a settled tab's charge has posted. A plain human-submitted `DebitAccount` omitting `tabRef`
is byte-for-byte unaffected.

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
historyTooLong?}`) whose whole lifetime is coarse on purpose. A charge that takes the balance from
zero-or-below to **owing** IS the FIFO-oldest open charge, so `post_entry` can record the due date its own
`postedAt` implies (`postedAt` + `ArrearsGraceDays`) without replaying anything; a payment that clears the
balance rewrites the aspect to `{evaluatedAt}` alone and ends the episode. Everything in between it refuses to
guess: a **partial** payment can move the head to a later charge with a later due date, a question only the
whole history answers, so the entry marks the state `stale` instead. A charge against an account already in
credit (a refund took it below zero) writes nothing at all — the surplus prepays it outright, so there is no
open debit to age. An account carrying no `.balance` at all (the legacy set) can only ever mark stale — it has
no before/after balance to reason from, so it never mints arrears state off a number it does not have.

The lens arms Weaver's `@at` at the recorded `dueAt` and opens its one gap when the timer's lapse is recorded
on the account, when the state is `stale`, or when the account has never been evaluated. All three dispatch
the same remediation, `EvaluateCafeArrears`, which recomputes the head with **the same FIFO the resident's own
statement runs** (`cmd/cafe-app/ledger.go`, `deriveStatement` — credits offset the oldest still-open charge
first, an unapplied credit carries forward as surplus) and rewrites `.arrears`. A recomputed date that has
passed is recorded as `remindedFor` — that is what closes the gap — and where **no reminder has yet gone out in
this episode** (`sentAt` absent) the same commit stamps `sentAt` and fires `external.notification` to the
bridge's `notification` adapter, keyed `<accountKey>:<dueAt>`.

`sentAt`, not `remindedFor`, is the send condition, and the difference is what makes it **one reminder per
arrears episode** rather than one per head. An episode runs from the charge that took the account from square
to owing until the balance returns to zero; within one episode a partial payment retires the oldest charge and
the head moves to the next, whose own term has usually passed too. Keying the send off the head would hand a
resident who is visibly paying their tab down a second nag for the same continuous debt. `sentAt` is carried
across every write of a live episode and dropped only where the episode ends, so a re-dispatch or redelivery
finds it already recorded and emits nothing, and the adapter dedups a genuine redelivery on the episode key.
A resident who pays off and runs a new tab up gets a fresh episode with a clean `sentAt`, and its due date is
necessarily later than any instant the permanent `freshnessExpiry` marker already holds.

An account whose transaction history outruns the evaluation's bounded replay budget is not refused — it is
recorded. A refusal would be a permanent silent stop: the row's own gap is the only thing that re-drives the
op, and a rejected op never closes it, so Weaver would re-dispatch a doomed evaluation on every window with
nothing sent and nothing said. The evaluation instead writes `historyTooLong`, carrying `dueAt`/`remindedFor`/
`sentAt` untouched and sending nothing, and the lens suppresses **both** the gap and the `@at` while the flag
stands. The row stays in `weaver-targets` for an operator to find; the next posted entry drops the flag and
re-marks the state stale, buying exactly one more attempt.

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
