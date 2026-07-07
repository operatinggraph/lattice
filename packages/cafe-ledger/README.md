# cafe-ledger

The Café house-tab payment ledger (v0.1.0) — a per-lease financial account that records café charges
and payments as an **append-only** transaction history; a balance is always derived by summing
entries, never stored as a mutable running total.

Depends: `lease-signing` (the `leaseapp` vertex type an account is `heldFor` — the same resident lease
`loftspace-ledger`'s own rent account anchors to; a house tab belongs to the same lease). Install:
`lattice-pkg install packages/cafe-ledger` (after `lease-signing`).

## Increment 1 of 3 (Café vertical, `verticals.md`)

This package ships the ledger primitive alone. `cafe-domain` (café location + the `OpenTab`/`Charge`/
`Settle` tab lifecycle, which posts into this ledger) and the thin café FE are Increment 2; the
one-bill composition lens unioning `ledgerHistory` + `cafeLedgerHistory` by `leaseAppKey` is
Increment 3. See
[`cafe-ledger-design.md`](../../_bmad-output/implementation-artifacts/cafe-ledger-design.md).

## Inventory

| Kind | Canonical names |
|---|---|
| **Vertex types** (2) | `cafeaccount` (root `{}`, D5) · `cafetransaction` (root `{}`, D5, `.entry` aspect) |
| **Aspect types** (1) | `cafeLedgerAccountGuard` — `vtx.leaseapp.<id>.cafeLedgerAccount`, the per-lease create-only uniqueness guard |
| **Links** (2) | `heldFor` (cafeaccount → leaseapp) · `postedTo` (cafetransaction → cafeaccount) |
| **Operations** (3) | `CreateAccount` · `DebitAccount` · `CreditAccount` |
| **Projection lenses** (2) | `cafeLedgerHistory` (one row per transaction) → `cafe-ledger-history` · `cafeLeaseAccounts` (lease → account key lookup) → `cafe-lease-accounts` (both `nats-kv`, `full` engine) |

Every op is granted to the `operator` role at `scope: any` (`permissions.go`) — the trusted
single-identity model, no new capability surface, identical to `loftspace-ledger` / `clinic-ledger`.

## Key shapes (Contract #1)

```
vtx.cafeaccount.<id>                     class=cafeaccount       root {} (D5 — balance is lens-derived)
vtx.cafetransaction.<id>                 class=cafetransaction   root {} (D5)
vtx.cafetransaction.<id>.entry           class=entry              {type ∈ debit|credit, amountCents, memo?, postedAt}
vtx.leaseapp.<id>.cafeLedgerAccount      class=cafeLedgerAccountGuard  {accountKey}  (the uniqueness guard)

lnk.cafeaccount.<id>.heldFor.leaseapp.<id>          (cafeaccount → leaseapp; cafeaccount is the later-arriving vertex)
lnk.cafetransaction.<id>.postedTo.cafeaccount.<id>  (cafetransaction → cafeaccount; cafetransaction is the later-arriving vertex)
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

## Append-only ledger

`DebitAccount`/`CreditAccount` each mint a fresh `vtx.cafetransaction.<id>` with a `.entry` aspect and
the `postedTo` link back to the account — no balance field is ever written or mutated; the
`cafeLedgerHistory` lens derives a balance by summing `amountCents` (positive for debit, negative for
credit) client-side, so concurrent debits/credits never race a read-modify-write.

## Where the ledger is surfaced

`cafeLedgerHistory` is the FE's house-tab payment-history read model (P5); `cafeLeaseAccounts` is the
only way the FE resolves a lease's café account key, since it is no longer derivable from
`leaseAppKey` (the independent NanoID above) — a lease with no café account yet still gets a row
(`accountKey` null).

## Out of scope (this increment)

- **The tab lifecycle** (`OpenTab`/`Charge`/`Settle`) — `cafe-domain`, Increment 2.
- **A stored/cached balance** — deliberately never materialized; always summed from `cafeLedgerHistory`.
- **Refunds / voids as a distinct operation** — model as an offsetting `CreditAccount`/`DebitAccount`
  entry with an explanatory `memo`, mirroring the other ledger packages.
