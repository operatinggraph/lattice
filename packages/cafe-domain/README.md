# cafe-domain

The Café house-tab POS session domain (v0.1.0) — a short-lived `tab` per resident visit
(`OpenTab`/`Charge`/`Settle`), settled onto `cafe-ledger`'s append-only house-tab account via a
Weaver playbook, never a direct cross-package write.

Depends: `lease-signing` (the `leaseapp` a tab is opened against) + `cafe-ledger` (the account/
transaction ops the playbook dispatches). Install: `lattice-pkg install packages/cafe-domain` (after
both).

## Increment 2 of 3 (Café vertical, `verticals.md`)

This fire ships the domain + Weaver wiring only: `tab` (`OpenTab`/`Charge`/`Settle`) + the
`cafeTabSettlement` convergence lens + playbook. The thin café FE (POS→tab · front-desk open-tabs ·
resident house-tab) is a follow-up checkpoint of this same increment. Increment 3 (one-bill
composition lens unioning `ledgerHistory` + `cafeLedgerHistory` by `leaseAppKey`) is next. See
[`cafe-ledger-design.md`](../../_bmad-output/implementation-artifacts/cafe-ledger-design.md).

## Inventory

| Kind | Canonical names |
|---|---|
| **Vertex types** (1) | `tab` (root `{}`, D5, `.status` aspect) |
| **Aspect types** (1) | `tabStatus` — `vtx.tab.<id>.status`, `{value, totalCents, openedAt, leaseAppKey, settledAt?}` |
| **Links** (1) | `openFor` (tab → leaseapp) |
| **Operations** (3) | `OpenTab` · `Charge` · `Settle` |
| **Convergence lens** (1) | `cafeTabSettlement` (one row per tab, `missing_account`/`missing_charge`) → `weaver-targets` (`nats-kv`, `full` engine, actorAggregate) |
| **Weaver playbook** (1) | `cafeTabSettlement` — `missing_account` → `directOp(CreateAccount)` · `missing_charge` → `directOp(DebitAccount)` (both cafe-ledger) |

Every op is granted to the `operator` role at `scope: any` (`permissions.go`) — the trusted
single-identity model, no new capability surface, identical to `cafe-ledger`.

## Key shapes (Contract #1)

```
vtx.tab.<id>            class=tab        root {} (D5)
vtx.tab.<id>.status     class=tabStatus  {value ∈ open|settled, totalCents, openedAt, leaseAppKey, settledAt?}

lnk.tab.<id>.openFor.leaseapp.<id>            (tab → leaseapp; tab is the later-arriving vertex)
lnk.cafetransaction.<id>.settles.tab.<id>     (cafetransaction → tab; written by cafe-ledger's DebitAccount tabRef)
```

## OCC-conditioned running total, not append-only line items

Unlike `cafe-ledger`'s append-only transaction history, a tab's `.status.totalCents` is a real
in-progress accumulator (`Charge` adds to it) — there is no per-item ledger during the POS session, so
the aspect is upserted directly, OCC-conditioned on its own current revision (the `providerSlotClaim`
precedent): two concurrent `Charge` calls racing the same tab must not lose an update, so the loser
gets `RevisionConflict` and retries, rather than one charge silently overwriting the other's total.
`Settle` freezes `totalCents`, flips `value` to `settled`, and stamps `settledAt` — also
OCC-conditioned. Both reject a tab that is not currently `open` (`TabNotOpen`).

## Weaver posts the settled total, never a direct cross-package write

`cafe-domain`'s own op scripts never write a `cafeaccount`/`cafetransaction` mutation directly — the
step-6 write gate keys `PermittedCommands` by `(operationType, class)`, and only `cafe-ledger`'s own
DDLs permit `CreateAccount`/`DebitAccount` for those classes. Instead, `Settle` closing a tab with
`totalCents > 0` surfaces on the `cafeTabSettlement` lens:

- **`missing_account`** — true while the resident's lease has no café-ledger account yet
  (`l.cafeLedgerAccount.data.accountKey` null). Weaver dispatches `CreateAccount{leaseAppKey}`
  (`cafe-ledger`) — "opening one via `CreateAccount` on first use."
- **`missing_charge`** — true once the account exists but no `cafetransaction` `settles` this tab yet.
  Weaver dispatches `DebitAccount{accountKey, amountCents, tabRef}` (`cafe-ledger`) — the `tabRef`
  extension (this fire) writes the `settles` audit link back to the tab, which is exactly what the
  lens's `OPTIONAL MATCH (t)<-[:settles]-(tx:cafetransaction)` reads to converge the gap.

Mirrors `bespoke-contracts/targets.go`'s `missing_charge → directOp(DebitAccount)` shape verbatim —
every payload field the dispatched op requires goes directly in `Params` (the `objects-base`
precedent), never relies on `Target` (which only ever sets `AuthContext.Target` for auth-path scoping,
never a payload value).

## Out of scope (this increment)

- **The thin FE** (POS→tab · front-desk open-tabs · resident house-tab) — a follow-up checkpoint.
- **The one-bill composition lens** — `cafe-domain`/`cafe-ledger` Increment 3.
- **Per-item charge audit trail** — `Charge` accumulates a running total only; itemized line receipts
  are a future extension if the product needs one (YAGNI — no demand row asks for it yet).
- **One-open-tab-per-lease exclusivity** — a resident could in principle have two concurrently open
  tabs; no demand row requires the guard, so it is not built.
