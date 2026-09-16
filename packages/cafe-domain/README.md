# cafe-domain

The Café house-tab POS session domain (v0.18.0) — a short-lived `tab` per resident visit
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
| **Aspect types** (4) | `tabStatus` — `vtx.tab.<id>.status`, `{value, totalCents, itemsMemo, lines, openedAt, staleAt, leaseAppKey, settledAt?, paidAtSettleCents?, paidAtSettleBy?}` · `cafeOpenTabGuard` — `vtx.leaseapp.<id>.cafeOpenTab`, `{tabKey}` (per-lease open-tab dedup guard) · `menuItemPrice` — `vtx.menuitem.<id>.price`, `{name, priceCents, available}` · `cafeHousePolicy` — `vtx.<locType>.<id>.cafePolicy`, `{tabLimitCents}` (the house's self-service tab limit, on the location) |
| **Links** (3) | `chargedTo` (tab → leaseapp, permanent) · `openFor` (tab → leaseapp, released by `Settle`) · `servedAt` (menuitem → location, permanent — what makes an item reachable) |
| **Operations** (11) | `OpenTab` · `Charge` · `VoidCharge` · `MarkLineServed` · `Settle` · `CreateMenuItem` · `RetireMenuItem` · `SetMenuItemAvailability` · `SetMenuItemLocation` · `UpdateMenuItem` · `SetCafePolicy` |
| **Lenses** (4) | `cafeTabSettlement` (convergence, one row per tab, `missing_account`/`missing_charge`/`missing_payment`) → `weaver-targets` (`nats-kv`, `full` engine, actorAggregate) · `menuCatalog` (plain projection, one row per live menuitem) → `cafe-menu-catalog` (`nats-kv`) · `cafeLeaseWorkplaces` (one row per lease, `coveringLocations` + `leaseEnd`) → `cafe-lease-workplaces` (`nats-kv`) — the read-side half of workplace confinement, plus the resident-readable tenancy-end column · `cafeHousePolicies` (one row per location carrying a `.cafePolicy`, `{locationKey, tabLimitCents, name}`) → `cafe-house-policies` (`nats-kv`) |
| **Weaver playbook** (1) | `cafeTabSettlement` — `missing_account` → `directOp(CreateAccount)` · `missing_charge` → `directOp(DebitAccount)` · `missing_payment` → `directOp(CreditCafeAccount)` (all cafe-ledger) |

Grants (`permissions.go`): `OpenTab`/`Charge`/`Settle` grant `operator`+`frontOfHouse` at `scope: any` AND
`consumer` at `scope: self` (a resident may open/self-order/settle their OWN tab, verified via the lease's
`applicationFor→identity` link). `VoidCharge` grants only `operator`+`frontOfHouse` at `scope: any` — no
self-service grant, since a POS correction is a staff decision even to reverse a resident's own self-order
mis-tap. `CreateMenuItem`/`RetireMenuItem`/`SetMenuItemAvailability`/`SetMenuItemLocation`/`UpdateMenuItem` also grant
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
vtx.<locType>.<id>.cafePolicy class=cafeHousePolicy {tabLimitCents} (written by SetCafePolicy on location-domain's unit|building|property)

lnk.tab.<id>.chargedTo.leaseapp.<id>          (tab → leaseapp; permanent — where the money lands; cafeTabSettlement anchors here)
lnk.tab.<id>.openFor.leaseapp.<id>            (tab → leaseapp; transient — that the tab is open; Settle tombstones it)
lnk.menuitem.<id>.servedAt.<locType>.<id>     (menuitem → location; permanent — what makes the item reachable to a resident who lives there)
lnk.cafetransaction.<id>.settles.tab.<id>     (cafetransaction → tab; written by cafe-ledger's DebitAccount / CreditCafeAccount tabRef)
```

## OCC-conditioned running total, not append-only line items

Unlike `cafe-ledger`'s append-only transaction history, a tab's `.status.totalCents` is a real
in-progress accumulator (`Charge` adds to it, `VoidCharge` subtracts a named line's own amount —
clamped at 0 rather than going negative) — there is no per-item ledger during the POS session, so the
aspect is upserted directly, OCC-conditioned on its own current revision (the `providerSlotClaim`
precedent): two concurrent `Charge`/`VoidCharge` calls racing the same tab must not lose an update, so
the loser gets `RevisionConflict` and retries, rather than one call silently overwriting the other's
total. `VoidCharge{tabKey, lineId}` is operator/`frontOfHouse` only — no self-service grant, since a
POS correction is a staff decision even to reverse a resident's own self-order mis-tap; it derives the
void amount from the named `.status.lines` entry itself, never a caller-supplied `amountCents`, and
marks that entry `voided: true` in place. `Settle` freezes `totalCents`, flips `value` to `settled`,
and stamps `settledAt` — also OCC-conditioned. All three reject a tab that is not currently `open`
(`TabNotOpen`).

`Settle{tabKey, paidCents?}` — the desk settles and takes the cash in one act. `paidCents` is
optional, staff-only integer cents: the cash the desk took at the counter as the tab closed. When
present it is recorded on `.status` as `paidAtSettleCents` + `paidAtSettleBy` (`op.actor`, the staffer
who took it) and on the `tab.settled` event; absent, neither field is written — absent reads as *no
counter payment*, never as unpaid (`SettleStaleTab` never writes them either). Refused `AuthDenied` on
a resident's self-scoped `Settle` (a resident hands over no cash — the `waiver`-is-staff-only shape),
`PaidMismatchesTab` unless it equals `totalCents` (the desk pays the whole tab; a card that went stale
between render and click submits the old total and is refused, never recorded as a partial — the
resident's own Record payment is the partial path), `InvalidArgument` unless a positive whole number.
Nothing is posted to the ledger by the op itself (P2): the settlement playbook below posts the payment
after the charge, and `cafe-ledger` bounds that credit by the recorded value.

Alongside `totalCents`, every `Charge` also appends a matching entry to `.status.lines`; `.status.itemsMemo`
is re-derived from the live (non-voided) lines on every `Charge`/`VoidCharge` — a comma-joined summary (a
menu item's own `.price.name`, or an off-menu `Charge`'s caller-supplied `description`/`"Off-menu charge"`
default) so a tab (open or settled) shows what was actually rung up, not just the sum — the `cafeTabSettlement`
lens projects it verbatim and the Weaver-dispatched `DebitAccount` posts the same string as the
settled ledger entry's `memo`. The structured itemization lives beside it in `.status.lines` — one
`{id, description, amountCents, voided, orderedBy, orderedAt, servedAt?, servedBy?}` entry per `Charge`, the
receipt's own record — so on every tab that can still be voided, `totalCents` equals the sum of its non-voided
lines.

A line also records whether it was handed over. A staff ring-up is served at the counter, so `Charge` on the
staff leg stamps `servedAt = orderedAt` / `servedBy` at ring-up; a self-ordered line carries neither until the
desk submits `MarkLineServed{tabKey, lineId}` (operator/`frontOfHouse` only, confined to the tab's own building
the way `VoidCharge` is; refuses `LineVoided`, `LineAlreadyServed`, `UnknownChargeLine`, `TabNotOpen`). A line
with `orderedAt` and no `servedAt` is still to make — the desk's orders queue is exactly those lines, oldest
first, read off the `lines` column `cafeTabSettlement` already projects; a line with neither predates the
field and its state is unknown. `VoidCharge` and `MarkLineServed` both copy the line whole and overwrite only
the key they own, so neither drops what the other recorded.

## Self-order menu catalog

A self-service `Charge{tabKey, menuItemKey}` never trusts a caller-supplied `amountCents` — it derives the
amount from the referenced `menuitem`'s own `.price.priceCents` (`require_menu_item_price`, ddls.go),
the gap the original operator-only `Charge` grant existed to cover. `CreateMenuItem{name, priceCents,
locationKey}` (operator-only) mints the item + its `.price` aspect + the `servedAt` link, rejecting
`UnknownLocation`/`NotALocation` if `locationKey` is absent, tombstoned, or not a location; that link is
the item's only reachability — the `edgeEntityMenuItems` edge-manifest lens walks a resident's residence
chain down to items served where they live, so an unlinked item is one no client can offer. `UpdateMenuItem{menuItemKey,
name, priceCents}` rewrites the item's `.price` aspect in one OCC'd upsert — a rename and a reprice are the
same act on the same aspect, so one op covers both; both fields are required, and the aspect's `available`
is carried through unchanged. `SetMenuItemAvailability{menuItemKey, available}` rewrites the same aspect
with `name`/`priceCents` carried through — the desk's sold-out-for-the-day toggle; a missing `available`
field reads as `true` (never toggled). `menuCatalog` projects it (`coalesce(…, true)`), both app pickers
grey a sold-out item out, and a `Charge` naming one — self-order or POS pick — is refused
`ItemUnavailable`. `RetireMenuItem`
tombstones a live item, self-OCC'd. A self-order `Charge` is additionally confined to items served at the
tab's own building or an ancestor of it (`location_covers`, walking the item's `servedAt` place against the
tab's lease's `appliesToUnit`) — `servedAt` bounds what a browse walk OFFERS, this bounds what `Charge`
ACCEPTS. The `menuCatalog` lens lists every live item for the Resident view's self-order picker (P5).

## House tab limit

A house may record a self-service tab limit: `SetCafePolicy{locationKey, tabLimitCents}` (operator +
`frontOfHouse`, confined to a location the staffer `worksAt` or an ancestor of it — `CreateMenuItem`'s
confinement) writes `.cafePolicy {tabLimitCents}` on the location (a non-negative whole number of cents;
`0` = self-service tabs closed at this house; no aspect = no limit recorded). A class-(d) optionalReads
write: the caller declares `<locationKey>.cafePolicy` — absent mints it, present OCC-upserts it, tombstoned
OCC-revives it. The effective limit for a tab is the **tightest** `tabLimitCents` on the tab's lease's unit
and its `containedIn` ancestors (`house_tab_limit`, the `location_covers` walk reading one aspect per node),
so a property-wide cap stays a cap under a looser building policy. On the **resident-self leg only**,
`Charge` refuses `TabLimitExceeded` when `totalCents + amountCents` would pass the limit (equal is allowed)
and `OpenTab` refuses it when the limit is `0`; the staff leg is never limited — the desk rings past the
limit and is warned by its own read model. `cafeHousePolicies` projects one row per location carrying a
policy; `cmd/cafe-app` composes each lease's limit from `cafeLeaseWorkplaces.coveringLocations` ∩ those
rows with the same minimum rule (`/api/residents.tabLimitCents`, null = no limit).

## Staff-workplace write confinement

A non-operator staff actor (`frontOfHouse`) may `OpenTab`/`Charge` only against a lease whose unit sits
inside a location it `worksAt` — `require_workplace`/`enforce_workplace`/`worksAt_covers` (ddls.go), the
same bounded breadth-first `containedIn` walk `clinic-domain` and `wellness-domain` use, exempting an
actor holding the primordial `operator` role and no-op on the resident-self path (bound instead by the
`applicationFor` ownership probe). The `cafeLeaseWorkplaces` lens is the read-side mirror of that same
walk (`facet-staff-worlds-design.md` §9): it projects `coveringLocations` per lease so a staff read
boundary gets the identical answer from a set intersection, no Core-KV read needed (P5). It also carries
`leaseEnd` off the same `.tenancy` aspect `OpenTab`'s `TenancyEnded` guard reads (mirroring front-desk's
own `frontDeskLeaseDetails` projection, `packages/front-desk/lenses.go`) — `cmd/cafe-app`'s own
`/api/residents` (residents.go) joins this bucket onto its roster by `leaseAppKey`, so the resident's own
self-service Open Tab can give itself the courtesy the staff POS/front-desk picker already has
(`fillLeaseSelect`'s `tenancyEnded` check) without a second protected lens.

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
- **`missing_charge`** — true once the account exists but no debit `cafetransaction` `settles` this tab
  yet. Weaver dispatches `DebitAccount{accountKey, amountCents, memo, tabRef}` (`cafe-ledger`) — the
  `tabRef` extension writes the `settles` audit link back to the tab, which is exactly what the lens's
  `OPTIONAL MATCH (t)<-[:settles]-(tx:cafetransaction)` reads to converge the gap.
- **`missing_payment`** — true once the charge is posted (a debit `settles` the tab), the tab records
  `paidAtSettleCents > 0`, and no credit `cafetransaction` `settles` it yet. Weaver dispatches
  `CreditCafeAccount{accountKey, amountCents: paidAtSettleCents, memo: "Paid at the counter", reason:
  "payment", tabRef}` (`cafe-ledger`), and the credit writes the same `settles` audit link. One hop
  serves both gaps — the counts discriminate by `.entry.type` (`count(CASE WHEN … THEN tx.key ELSE
  null END)`), which keeps the lens inside the relation-narrowed consumer-filter budget a second
  relation name would have broken. The *charge is posted* conjunct is what orders the two postings: `cafe-ledger`'s payment cap reads the account's
  live `.balance`, so a credit dispatched before the debit would be refused `PaymentExceedsBalance` for
  cash the resident already handed over; opened only once the debit exists, the payment always lands
  inside the balance that debit opened. `missing_charge` and `missing_payment` are never both live.

Mirrors `semantic-contracts/targets.go`'s `missing_charge → directOp(DebitAccount)` shape verbatim —
every payload field the dispatched op requires goes directly in `Params` (the `objects-base`
precedent), never relies on `Target` (which only ever sets `AuthContext.Target` for auth-path scoping,
never a payload value).

## Out of scope

- **Per-line quantity / timestamps** — `.status.lines` records one `{id, description, amountCents, voided,
  orderedBy}` entry per `Charge`; a quantity column or a per-line `chargedAt` is a distinct extension if a
  printable receipt ever needs one.

One-open-tab-per-lease exclusivity IS built, not out of scope: the `cafeOpenTabGuard` aspect (Inventory
above) is a per-lease dedup guard `OpenTab` claims and `Settle` releases, rejecting a second concurrent
`OpenTab` on the same lease with `OpenTabAlreadyExists` (`ddls.go`).

## OpenTab refusals

`OpenTab{leaseAppKey}` refuses `UnknownLeaseApplication` (lease absent or tombstoned), `LeaseNotApproved`
(no lease-signing `.decision` reading `approved`), `TenancyEnded` (`submittedAt` at or past the lease's
`.tenancy` `leaseEnd`), `CreditHold`, `OpenTabAlreadyExists` (the guard above), and — on the resident-self
leg — `AuthDenied` when the target identity is not the lease's own applicant.

**Credit hold.** A lease whose café account carries an arrears episode a reminder has already gone out for
opens no new tab, on the staff and resident-self legs alike. The signal is `cafe-ledger`'s
`vtx.cafeaccount.<id>.arrears.sentAt`: `EvaluateCafeArrears` stamps it when it emits the reminder
notification — it records the send INTENT (the adapter's delivery outcome lands on the account's
`.arrearsNotification`, which the hold does not read) — carries it across every write of the same episode,
and drops it only when the balance returns to zero. So `sentAt` present means "reminded and still owes",
while `dueAt` alone (overdue, not yet reminded) or a bare `{evaluatedAt}` is not a hold. `OpenTab` reaches the account by a live class-(e) walk of the lease's
`heldFor` in-links filtered to `vtx.cafeaccount.` (the rent ledger's `vtx.account` link sits beside it),
then the per-candidate `.arrears` read — never a caller-declared read, so no submitter can decline to
declare it. An `.arrears` document of any other class is `InvalidState`. Clearing the hold is a payment or a
write-off on the account (the Front Desk arrears row); there is no override verb.
