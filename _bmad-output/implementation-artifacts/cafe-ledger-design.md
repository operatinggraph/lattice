# Café vertical, Increment 1 — `cafe-ledger` (house-tab ledger)

**Status:** ✅ Winston-ratified — build-ready. Pure implementation decisions (package shape, naming),
no frozen-contract change, no architectural fork — decided per CLAUDE.md / steward §0 and built this fire.

## Scope of this increment

`verticals.md`'s Café row is ★★★/M and bundles `cafe-domain` (café location + OpenTab/Charge/Settle) +
`cafe-ledger` (the account/transaction mirror) + a thin FE + the one-bill composition lens. Mirroring the
precedent phasing (`loftspace-ledger` Inc 1 shipped the account/transaction vertex types alone —
`12736df` — before Inc 2's FE — `9947f75`), this increment ships **`cafe-ledger` only**: the
`cafeaccount`/`cafetransaction` vertex types, append-only, `cafeaccount heldFor` the resident's
`leaseapp` — exactly loftspace-ledger's own anchor, since a house tab belongs to the same resident
lease a rent ledger account does. `cafe-domain` (the tab lifecycle: OpenTab/Charge/Settle) and the FE
land as Inc 2 once this ledger primitive exists to post into; the one-bill composition lens
(`ledgerHistory` ∪ `cafeHistory` by `leaseAppKey`) lands once both ledgers are real.

## Ground: mirrors `loftspace-ledger` / `clinic-ledger` exactly, with one new wrinkle

Read `packages/loftspace-ledger/{ddls.go,scripts.go,manifest.yaml,package.go,permissions.go,ledger_test.go}`
and `packages/clinic-ledger/{ddls.go,lenses.go}` in full before writing this. Both packages:

- mint the account under its **own independently-generated NanoID** — never derived/shared from the
  anchor vertex (`adjacency-shared-nanoid-collision-design.md`: a shared NanoID corrupts
  `internal/refractor/adjacency`, which keys by bare NanoID with no type qualifier);
- enforce "one account per anchor" via a **deterministic create-only guard aspect on the anchor**
  (`<anchorKey>.ledgerAccount`), not the account's own id;
- keep root `data = {}` on both account and transaction (D5 — balance is lens-derived, never stored);
- post entries append-only via a shared `post_entry` Starlark helper (`DebitAccount`/`CreditAccount`);
- vertical-prefix every DDL/lens **canonicalName** (`clinicaccount` not `account`) because a
  canonicalName is global across every installed package
  (`internal/pkgmgr/installer.go` `checkCanonicalNameCollision`) — loftspace-ledger already owns the
  bare names.

**The new wrinkle café introduces:** loftspace-ledger and clinic-ledger anchor to *different* vertex
types (`leaseapp` vs `patient`), so their identically-named `.ledgerAccount` guard aspects never
collide — different parent vertex type, different key. **`cafe-ledger` anchors to the SAME `leaseapp`
vertex loftspace-ledger already anchors to** (a house tab belongs to the same resident lease as the
rent ledger). Reusing the local name `.ledgerAccount` for the café guard would collide key-for-key with
loftspace-ledger's own `vtx.leaseapp.<id>.ledgerAccount` aspect on that vertex — two different aspect
classes writing the identical key. **Fix: the guard aspect's local name is vertical-prefixed too, not
just its class** — `vtx.leaseapp.<id>.cafeLedgerAccount` (class `cafeLedgerAccountGuard`), distinct
from loftspace-ledger's `vtx.leaseapp.<id>.ledgerAccount` (class `ledgerAccountGuard`) on the same
vertex. The `heldFor` **link** needs no such fix — its key embeds the *source* vertex's own type
(`lnk.cafeaccount.<id>.heldFor.leaseapp.<id>` vs `lnk.account.<id>.heldFor.leaseapp.<id>`), so two
ledger packages anchoring the same leaseapp already produce distinct link keys.

## Shape

Package `cafe-ledger`, depends `lease-signing` (owns `leaseapp`) — same dependency as loftspace-ledger.

- **`cafeaccount`** vertex type (DDL `cafeaccount`) — `CreateAccount{leaseAppKey}` mints
  `vtx.cafeaccount.<NanoID>` (root `{}`), writes `vtx.leaseapp.<id>.cafeLedgerAccount` (class
  `cafeLedgerAccountGuard`) `= {accountKey}` on the leaseapp, and `lnk.cafeaccount.<id>.heldFor.leaseapp.<id>`.
- **`cafeLedgerAccountGuard`** aspect-type DDL (declaration-only), local name `cafeLedgerAccount`.
- **`cafetransaction`** vertex type (DDL `cafetransaction`) — `DebitAccount`/`CreditAccount` mirror
  loftspace-ledger's `transaction` DDL exactly (no `clauseRef`/`period` — that's the bespoke-contracts
  Executable Paper consumer, not needed here): mint `vtx.cafetransaction.<NanoID>` + `.entry` aspect
  `{type, amountCents, memo?, postedAt}` + `lnk.cafetransaction.<id>.postedTo.cafeaccount.<id>`.
- **`cafeLedgerHistory`** lens (one row per transaction, `nats-kv` bucket `cafe-ledger-history`).
- **`cafeLeaseAccounts`** lens (one row per leaseapp, `accountKey` null until opened — the FE's only
  way to resolve the account key, since it's independently minted), bucket `cafe-lease-accounts`.
- Permissions: `CreateAccount`/`DebitAccount`/`CreditAccount` all `grantsTo: [operator]`, `scope: any` —
  same trusted-single-identity idiom every ledger package uses (no new capability surface).

## Verify

`go build ./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run ./scripts/lint-conventions.go`,
`go test ./packages/cafe-ledger/...` (manifest/Definition lockstep + the 5-test ledger suite mirroring
loftspace-ledger's: mints-held-for, unknown-lease, debit/credit post, unknown-account,
non-positive-amount — plus a guard-collision regression seeding a loftspace-ledger `.ledgerAccount`
guard on the same leaseapp and asserting `.cafeLedgerAccount` still writes cleanly alongside it).

## Next

- **Inc 2** — `cafe-domain` (café location vertex + `tab` vertex: `OpenTab`/`Charge`/`Settle`; `Settle`
  posts a `DebitAccount` to the resident's `cafeaccount`, opening one via `CreateAccount` on first use)
  + thin FE (POS→tab · front-desk open-tabs · resident house-tab).
- **Inc 3** — one-bill composition lens unioning `ledgerHistory` + `cafeLedgerHistory` by `leaseAppKey`.
