# loftspace-ledger

The LoftSpace tenant payment ledger (v0.3.0) — a per-lease financial account that records charges
(rent, fees, deposits) and payments as an **append-only** transaction history; a balance is always
derived by summing entries, never stored as a mutable running total.

Depends: `lease-signing` (the `leaseapp` vertex type an account is `heldFor`). Install:
`lattice-pkg install packages/loftspace-ledger` (after `lease-signing`; or `make install-loftspace`
onto a running stack).

## Inventory

| Kind | Canonical names |
|---|---|
| **Vertex types** (2) | `account` (root `{}`, D5) · `transaction` (root `{}`, D5, `.entry` aspect) |
| **Aspect types** (1) | `ledgerAccountGuard` — `vtx.leaseapp.<id>.ledgerAccount`, the per-lease create-only uniqueness guard |
| **Links** (2) | `heldFor` (account → leaseapp) · `postedTo` (transaction → account) |
| **Operations** (3) | `CreateAccount` · `DebitAccount` · `CreditAccount` |
| **Projection lenses** (2) | `ledgerHistory` (one row per transaction) → `loftspace-ledger-history` · `leaseAccounts` (lease → account key lookup) → `loftspace-lease-accounts` (both `nats-kv`, `full` engine) |

Every op is granted to the `operator` role at `scope: any` (`permissions.go`) — the trusted
single-identity model, no new capability surface, identical to `clinic-ledger`.

## Key shapes (Contract #1)

```
vtx.account.<id>                    class=account       root {} (D5 — balance is lens-derived)
vtx.transaction.<id>                class=transaction   root {} (D5)
vtx.transaction.<id>.entry          class=entry          {type ∈ debit|credit, amountCents, memo?, postedAt}
vtx.leaseapp.<id>.ledgerAccount     class=ledgerAccountGuard  {accountKey}  (the uniqueness guard)

lnk.account.<id>.heldFor.leaseapp.<id>        (account → leaseapp; account is the later-arriving vertex)
lnk.transaction.<id>.postedTo.account.<id>    (transaction → account; transaction is the later-arriving vertex)
```

## Independent account NanoID + guard aspect

`CreateAccount` mints the account under its **own independently-generated NanoID** — never reused
from the lease, since Core KV NanoIDs are unique platform-wide identifiers, not scoped per vertex
type. "At most one account per lease" is enforced by the deterministic create-only
`ledgerAccountGuard` aspect on the **leaseapp** (`leaseAppKey + ".ledgerAccount"`) instead of a
shared/derived key: a second `CreateAccount` for the same lease conflicts on that already-existing
aspect key. This mirrors `clinic-ledger`'s account/patient shape (the account held for a lease
instead of a patient); see
[`adjacency-shared-nanoid-collision-design.md`](../../_bmad-output/implementation-artifacts/adjacency-shared-nanoid-collision-design.md)
for why the account carries its own id rather than the lease's.

## Append-only ledger + the clause seam

`DebitAccount`/`CreditAccount` each mint a fresh `vtx.transaction.<id>` with a `.entry` aspect and
the `postedTo` link back to the account — no balance field is ever written or mutated; the
`ledgerHistory` lens derives a balance by summing `amountCents` (positive for debit, negative for
credit) client-side, so concurrent debits/credits never race a read-modify-write.

`DebitAccount`'s optional `clauseRef` additionally writes the `authorizedBy` audit link
(transaction → clause) and updates the clause's `.status` — `completed` for a one-time clause, or
`chargeValidUntil` re-armed (`recurringChargePeriod = 720h`, `scripts.go`) for a `period: monthly`
clause (Fire V3) — the `bespoke-contracts` Executable Paper package's canonical `directOp` consumer
of this ledger.

## Where the ledger is surfaced

`ledgerHistory` is the FE's payment-history read model (P5); `leaseAccounts` is the only way the FE
resolves a lease's account key, since it is no longer derivable from `leaseAppKey` (the independent
NanoID above) — a lease with no account yet still gets a row (`accountKey` null).

## Out of scope

- **A stored/cached balance** — deliberately never materialized; always summed from `ledgerHistory`.
- **Refunds / voids as a distinct operation** — model as an offsetting `CreditAccount`/`DebitAccount`
  entry with an explanatory `memo` today; a dedicated reversal op is not yet needed.
