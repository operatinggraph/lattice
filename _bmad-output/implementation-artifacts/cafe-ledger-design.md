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

## Inc 2a — `cafe-domain` (domain + Weaver wiring), shipped

`cafe-domain` ships the `tab` vertex (`OpenTab`/`Charge`/`Settle`, OCC-conditioned running total —
`Charge` is a real accumulator, unlike an idempotent status flip, so it needs the `providerSlotClaim`
precedent's OCC conditioning, not an unconditioned upsert) + the `cafeTabSettlement` actorAggregate
convergence lens + a §10.8 playbook. No separate "café location" vertex: YAGNI — no demand row asks
for multi-location cafés, and the tab's only essential relationship is to the resident lease
(`openFor`), mirroring `cafe-ledger`'s own anchor.

`Settle` never posts to `cafe-ledger` directly — the step-6 write gate keys `PermittedCommands` by
`(operationType, class)`, so cafe-domain's own script cannot write a `cafeaccount`/`cafetransaction`
mutation it doesn't own. Instead a settled, positive-total tab surfaces on `cafeTabSettlement`:
`missing_account` (no café-ledger account yet) → Weaver `directOp(CreateAccount)` — "opening one via
`CreateAccount` on first use"; `missing_charge` (account exists, not yet posted) → Weaver
`directOp(DebitAccount)` with a `tabRef` back-link. `cafe-ledger`'s `DebitAccount` is extended
(additive, byte-for-byte unaffected without `tabRef`) with that optional `tabRef`, writing the
`settles` audit link (`cafetransaction`→`tab`) the lens's `missing_charge` gate reads — mirrors
`loftspace-ledger`'s `clauseRef`/`bespoke-contracts` precedent exactly (`packages/bespoke-contracts/targets.go`).

## Inc 2b — `cmd/cafe-app` thin FE, shipped

Three vanilla-JS views (POS → tab, front-desk open-tabs, resident house-tab) mirroring
`cmd/loftspace-app`/`cmd/clinic-app`'s idioms exactly: `server.go` route + `embed web`, a `devSigner`
staff-token minter (every café op is `grantsTo:[operator] scope:any`, so one fixed staff identity
covers every write — no per-resident login exists in this thin FE), and browser-direct
`submitOp()` → the Gateway's `POST /v1/operations` (the current, non-deprecated write path both
sibling apps use — NOT their own legacy `/api/op` proxy). Reads are three lens projections, all P5:
`cafeLeaseAccounts` (the lease picker), `cafeTabSettlement` (open/settled tabs — `weaver-targets`,
filtered by the `cafeTabSettlement.` key prefix since that bucket is shared across every package's
convergence lens), and `cafeLedgerHistory` + `cafeLeaseAccounts` together (the resident's posted
charge history + running balance, mirroring `cmd/loftspace-app/ledger.go`'s two-bucket join).

**Lens gap closed first:** `cafeTabSettlement`'s `RETURN`/`BodyColumns` (§ above) projected only
`missing_account`/`missing_charge`/`violating` — an **open** tab and a **settled-and-fully-posted**
tab produced an identical body (both gaps false), so the FE had no way to tell "still open" from
"posted." Fixed additively: `status`/`openedAt`/`settledAt` (already read internally off
`t.status.data.*` for the gap booleans) now also flow to `RETURN` + `BodyColumns` — same lens, same
bucket, no consumer of the two gap columns changes. `cmd/cafe-app/tabs.go`'s `Posted` field derives
from `status == "settled" && !missing_account && !missing_charge`.

**Verify:** `go build ./...`, `make vet`, `golangci-lint run ./...` all clean; `STRICT=1 go run
./scripts/lint-conventions.go` 0 issues (unchanged advisory-warning count); full `go test ./...`
green, including two new `cafeTabSettlement` cypher-lens regression cases (open tab's
status/openedAt/settledAt shape, settled-and-converged tab's) and `cmd/cafe-app`'s own unit suite
(`computeLeases`/`computeTabs`/`computeLedgerHistory`/`resolveLeaseAccount`/`healthProbe`, all
table-tested over a fake KV seam, mirroring `cmd/loftspace-app`/`cmd/clinic-app`'s own test shape).
Wired into the dev harness: `make up-cafe`/`install-cafe`/`refresh-cafe`/`run-cafe-app` (Makefile),
a `cafe-app` NATS nkey + permission block (`deploy/gen-dev-nkeys/main.go`, regenerated
`deploy/nats-server.conf` — additive, every existing component's seed/permissions untouched,
confirmed by `internal/natsperm`'s `TestConfigParses` + the full write-isolation suite staying
green) and a `:7801` origin added to `GATEWAY_CORS_ORIGINS`.
**Live-stack / in-browser verification is DEFERRED**, not done this fire: the shared dev NATS
container's loaded config predates this nkey addition, and reloading it (or swapping in another
component's credential as a stand-in) are both live-shared-infrastructure actions outside this
fire's authorization — the new nkey activates cleanly on the next `make down && make up-full`/
`up-cafe` bootstrap cycle (or an explicitly authorized live reload), at which point the in-browser
POS/front-desk/resident flows should be exercised end-to-end before this is treated as fully proven.

## Inc 3 — `packages/one-bill`, the composition lens, shipped

The cypher engine has no UNION (`internal/refractor/ruleengine/full/visitor.go` rejects any query
carrying a `oC_Union` at parse time, and `docs/contracts/06-capability-kv.md` states the platform rule —
one Lens, one RETURN shape; multi-output patterns are additional Lenses, not Lens-internal complexity), so
"unioning `ledgerHistory` + `cafeLedgerHistory` by `leaseAppKey`" is not a single unioned cypher query. It
ships as a new lens-only package, `packages/one-bill` (no DDLs/permissions/roles — mirrors
`control-authz`'s grant-only shape, just for lenses), declaring **two** Lenses —
`oneBillRentEntries` (matches `:transaction`/`:account`, loftspace-ledger's classes) and
`oneBillCafeEntries` (matches `:cafetransaction`/`:cafeaccount`, cafe-ledger's) — both projecting into
the SAME shared bucket (`one-bill-history`), each row additionally tagged `source: "rent"` / `"cafe"`.
This mirrors the existing rbac-domain (`cap.roles.*`) / service-location (`cap.svc.*`) precedent of two
independently-declared Lenses composing into one bucket with disjoint keys — here the per-row key is the
transaction's own `t.key`, and `vtx.transaction.<id>` vs `vtx.cafetransaction.<id>` are already disjoint
by vertex-type prefix, so no extra key-namespacing is needed. `Depends: [loftspace-ledger, cafe-ledger]`
for install-order/documentation honesty; the cypher engine itself matches by class label at read time
regardless (same as loftspace-ledger's own OPTIONAL MATCH into bespoke-contracts' `:clause` with no
declared dependency) — a stack running only one of the two ledgers just sees that lens side project zero
rows, not an error (the installer logs, not fails, an unverified declared dependency).

New Makefile target `install-onebill` (requires `install-loftspace` + `install-cafe` to have already run).

**Verify:** `go build ./...`, `make vet`, `golangci-lint run ./...` (0 issues), `STRICT=1 go run
./scripts/lint-conventions.go` (0 issues, unchanged 55 advisory-warning baseline — the package reads no
KV directly), full `go test ./...` green including `packages/one-bill`'s own embedded-NATS cypher-lens
regression suite (`lens_cypher_test.go`, mirroring `cafe-domain`'s `cafeTabSettlement` harness): each
lens projects its own tagged row correctly, and — seeding BOTH a rent and a café transaction against the
SAME lease in one graph — a `TestOneBill_KeysDoNotCollide` case proves the two lenses' output keys stay
disjoint when run over a real mixed graph, not just by inspection. Installed live onto the running dev
stack (`make install-onebill`): Core KV commit succeeded (`packageKey=vtx.package.8tSH7g2FgERmeMTX8tSH`,
`writeCount=14`), confirming the manifest/Definition parse + install path end-to-end.
**Live reprojection into `one-bill-history` is DEFERRED**, not done this fire, for the same reason Inc
2b's in-browser check was: this is a lens newly ADDED to an already-running Refractor, and per this
repo's own documented F-004 caveat ("an ADDED lens/role/op won't activate under a live stack — the
Refractor + DDL cache load lenses at install time"), the running Refractor process won't start
projecting rows into the new bucket until it restarts — a `make down && make up-full` cycle (or an
explicitly authorized live Refractor restart), both live-shared-infrastructure actions outside this
fire's authorization (the same boundary Inc 2b's nkey activation hit). The cypher logic itself is proven
correct by the embedded-NATS regression suite above; only the live end-to-end reprojection await the
next full bootstrap cycle.

## Next

- **Mixed-use composition surfaces** (verticals.md) — front-desk/operations aggregate views consuming
  `one-bill-history` (and others) once a full bootstrap cycle activates it live.
