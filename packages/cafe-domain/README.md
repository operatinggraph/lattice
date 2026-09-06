# cafe-domain

The Café house-tab POS session domain (v0.11.3) — a short-lived `tab` per resident visit
(`OpenTab`/`Charge`/`VoidCharge`/`Settle`), settled onto `cafe-ledger`'s append-only house-tab account via a
Weaver playbook, never a direct cross-package write — plus the `menuitem` self-order catalog a resident's
own `Charge` binds against, and staff-workplace write confinement for both.

Depends: `lease-signing` (the `leaseapp` a tab is opened against) + `cafe-ledger` (the account/
transaction ops the playbook dispatches). Install: `lattice-pkg install packages/cafe-domain` (after
both).

## Status

Inc 2 (this package's domain + Weaver wiring + the thin FE, `cmd/cafe-app`) and Inc 3 (the one-bill
composition lens unioning `ledgerHistory` + `cafeLedgerHistory` by `leaseAppKey`) have both shipped — Inc 3
as its own lens-only package, [`packages/one-bill`](../one-bill), not inside this one. See
[`cafe-ledger-design.md`](../../_bmad-output/implementation-artifacts/cafe-ledger-design.md) for the
Inc 1–3 history. Since Inc 2, this package also grew the self-order menu catalog and staff-workplace write
confinement described below (facet-staff-worlds-design.md §3.5, §9).

## Inventory

| Kind | Canonical names |
|---|---|
| **Vertex types** (2) | `tab` (root `{}`, D5, `.status` aspect) · `menuitem` (root `{}`, D5, `.price` aspect) |
| **Aspect types** (3) | `tabStatus` — `vtx.tab.<id>.status`, `{value, totalCents, itemsMemo, openedAt, leaseAppKey, settledAt?}` · `cafeOpenTabGuard` — `vtx.leaseapp.<id>.cafeOpenTab`, `{tabKey}` (per-lease open-tab dedup guard) · `menuItemPrice` — `vtx.menuitem.<id>.price`, `{name, priceCents}` |
| **Links** (3) | `chargedTo` (tab → leaseapp, permanent) · `openFor` (tab → leaseapp, released by `Settle`) · `servedAt` (menuitem → location, permanent — what makes an item reachable) |
| **Operations** (8) | `OpenTab` · `Charge` · `VoidCharge` · `Settle` · `CreateMenuItem` · `RetireMenuItem` · `SetMenuItemLocation` · `UpdateMenuItem` |
| **Lenses** (3) | `cafeTabSettlement` (convergence, one row per tab, `missing_account`/`missing_charge`) → `weaver-targets` (`nats-kv`, `full` engine, actorAggregate) · `menuCatalog` (plain projection, one row per live menuitem) → `cafe-menu-catalog` (`nats-kv`) · `cafeLeaseWorkplaces` (one row per lease, `coveringLocations`) → `cafe-lease-workplaces` (`nats-kv`) — the read-side half of workplace confinement |
| **Weaver playbook** (1) | `cafeTabSettlement` — `missing_account` → `directOp(CreateAccount)` · `missing_charge` → `directOp(DebitAccount)` (both cafe-ledger) |

Grants (`permissions.go`): `OpenTab`/`Charge`/`Settle` grant `operator`+`frontOfHouse` at `scope: any` AND
`consumer` at `scope: self` (a resident may open/self-order/settle their OWN tab, verified via the lease's
`applicationFor→identity` link). `VoidCharge` grants only `operator`+`frontOfHouse` at `scope: any` — no
self-service grant, since a POS correction is a staff decision even to reverse a resident's own self-order
mis-tap. `CreateMenuItem`/`RetireMenuItem`/`SetMenuItemLocation`/`UpdateMenuItem` also grant
`operator`+`frontOfHouse` at `scope: any` (no `consumer` grant — running the catalog is a front-desk beat,
not a resident one); the workplace confinement (below) is what keeps one building's staff off another's
menu, not the grant itself.

## Key shapes (Contract #1)

```
vtx.tab.<id>                 class=tab             root {} (D5)
vtx.tab.<id>.status          class=tabStatus       {value ∈ open|settled, totalCents, itemsMemo, openedAt, leaseAppKey, settledAt?}
vtx.leaseapp.<id>.cafeOpenTab class=cafeOpenTabGuard {tabKey} (claimed by OpenTab, tombstoned by Settle)
vtx.menuitem.<id>            class=menuitem        root {} (D5)
vtx.menuitem.<id>.price      class=menuItemPrice   {name, priceCents}

lnk.tab.<id>.chargedTo.leaseapp.<id>          (tab → leaseapp; permanent — where the money lands; cafeTabSettlement anchors here)
lnk.tab.<id>.openFor.leaseapp.<id>            (tab → leaseapp; transient — that the tab is open; Settle tombstones it)
lnk.menuitem.<id>.servedAt.<locType>.<id>     (menuitem → location; permanent — what makes the item reachable to a resident who lives there)
lnk.cafetransaction.<id>.settles.tab.<id>     (cafetransaction → tab; written by cafe-ledger's DebitAccount tabRef)
```

## OCC-conditioned running total, not append-only line items

Unlike `cafe-ledger`'s append-only transaction history, a tab's `.status.totalCents` is a real
in-progress accumulator (`Charge` adds to it, `VoidCharge` subtracts — clamped at 0 rather than going
negative) — there is no per-item ledger during the POS session, so the aspect is upserted directly,
OCC-conditioned on its own current revision (the `providerSlotClaim` precedent): two concurrent
`Charge`/`VoidCharge` calls racing the same tab must not lose an update, so the loser gets
`RevisionConflict` and retries, rather than one call silently overwriting the other's total.
`VoidCharge` is operator/`frontOfHouse` only — no self-service grant, since a POS correction is a
staff decision even to reverse a resident's own self-order mis-tap. `Settle` freezes `totalCents`,
flips `value` to `settled`, and stamps `settledAt` — also OCC-conditioned. All three reject a tab that
is not currently `open` (`TabNotOpen`).

Alongside `totalCents`, every `Charge`/qualifying `VoidCharge` also appends a plain-text line to
`.status.itemsMemo` — a comma-joined running summary (a menu item's own `.price.name`, an off-menu
`Charge`'s caller-supplied `description` or the `"Off-menu charge"` default, or `"Void correction"`)
so a tab (open or settled) shows what was actually rung up, not just the sum — the `cafeTabSettlement`
lens projects it verbatim and the Weaver-dispatched `DebitAccount` posts the same string as the
settled ledger entry's `memo`. It is a summary line, not a structured per-item ledger — see Out of
scope.

## Self-order menu catalog

A self-service `Charge{tabKey, menuItemKey}` never trusts a caller-supplied `amountCents` — it derives the
amount from the referenced `menuitem`'s own `.price.priceCents` (`require_menu_item_price`, ddls.go),
the gap the original operator-only `Charge` grant existed to cover. `CreateMenuItem{name, priceCents,
locationKey}` (operator-only) mints the item + its `.price` aspect + the `servedAt` link, rejecting
`UnknownLocation`/`NotALocation` if `locationKey` is absent, tombstoned, or not a location; that link is
the item's only reachability — the `edgeEntityMenuItems` edge-manifest lens walks a resident's residence
chain down to items served where they live, so an unlinked item is one no client can offer. `UpdateMenuItem{menuItemKey,
name, priceCents}` rewrites the item's `.price` aspect in one OCC'd upsert — a rename and a reprice are the
same act on the same aspect, so one op covers both; both fields are required. `RetireMenuItem`
tombstones a live item, self-OCC'd. A self-order `Charge` is additionally confined to items served at the
tab's own building or an ancestor of it (`location_covers`, walking the item's `servedAt` place against the
tab's lease's `appliesToUnit`) — `servedAt` bounds what a browse walk OFFERS, this bounds what `Charge`
ACCEPTS. The `menuCatalog` lens lists every live item for the Resident view's self-order picker (P5).

## Staff-workplace write confinement

A non-operator staff actor (`frontOfHouse`) may `OpenTab`/`Charge` only against a lease whose unit sits
inside a location it `worksAt` — `require_workplace`/`enforce_workplace`/`worksAt_covers` (ddls.go), the
same bounded breadth-first `containedIn` walk `clinic-domain` and `wellness-domain` use, exempting an
actor holding the primordial `operator` role and no-op on the resident-self path (bound instead by the
`applicationFor` ownership probe). The `cafeLeaseWorkplaces` lens is the read-side mirror of that same
walk (`facet-staff-worlds-design.md` §9): it projects `coveringLocations` per lease so a staff read
boundary gets the identical answer from a set intersection, no Core-KV read needed (P5).

## Front-desk identity roster

`cafeIdentitiesRead` (protected Postgres Secure Lens, Contract #3 §3.10) resolves a signed-in identity's own
name for "Signed in as <name>". Each row anchors on its own identity NanoID PLUS every workplace building
that covers the identity's own lease (`applicationFor -> appliesToUnit -> containedIn*0..7`, the same depth
`cafeLeaseWorkplaces` and `worksAt_covers` reach) — so a `worksAt`-anchored front-desk actor
(service-location's `staffReadGrants`, `cap-read.staff`) resolves the name of any resident whose lease their
workplace covers, not only themselves. A WildcardAnchor holder still reads the whole roster.

## Weaver posts the settled total, never a direct cross-package write

`cafe-domain`'s own op scripts never write a `cafeaccount`/`cafetransaction` mutation directly — the
step-6 write gate keys `PermittedCommands` by `(operationType, class)`, and only `cafe-ledger`'s own
DDLs permit `CreateAccount`/`DebitAccount` for those classes. Instead, `Settle` closing a tab with
`totalCents > 0` surfaces on the `cafeTabSettlement` lens:

- **`missing_account`** — true while the resident's lease has no café-ledger account yet
  (`l.cafeLedgerAccount.data.accountKey` null). Weaver dispatches `CreateAccount{leaseAppKey}`
  (`cafe-ledger`) — "opening one via `CreateAccount` on first use."
- **`missing_charge`** — true once the account exists but no `cafetransaction` `settles` this tab yet.
  Weaver dispatches `DebitAccount{accountKey, amountCents, memo, tabRef}` (`cafe-ledger`) — the `tabRef`
  extension writes the `settles` audit link back to the tab, which is exactly what the lens's
  `OPTIONAL MATCH (t)<-[:settles]-(tx:cafetransaction)` reads to converge the gap.

Mirrors `semantic-contracts/targets.go`'s `missing_charge → directOp(DebitAccount)` shape verbatim —
every payload field the dispatched op requires goes directly in `Params` (the `objects-base`
precedent), never relies on `Target` (which only ever sets `AuthContext.Target` for auth-path scoping,
never a payload value).

## Out of scope

- **Structured per-item ledger** — `.status.itemsMemo` is a comma-joined text summary (name only, no
  per-line price/quantity), built by `Charge`/`VoidCharge` and frozen by `Settle`; it is not a
  structured array of `{menuItemKey, priceCents, chargedAt}` rows. A future structured itemization
  (e.g. for a printable receipt) is a distinct extension if the product needs one — the running text
  summary was the itemization gap verticals.md's Café row asked for.

One-open-tab-per-lease exclusivity IS built, not out of scope: the `cafeOpenTabGuard` aspect (Inventory
above) is a per-lease dedup guard `OpenTab` claims and `Settle` releases, rejecting a second concurrent
`OpenTab` on the same lease with `OpenTabAlreadyExists` (`ddls.go`).
