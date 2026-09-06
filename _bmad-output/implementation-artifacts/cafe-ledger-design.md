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

**Refractor restart, live-verified (2026-07-07):** cycled the running `bin/refractor` process (kill +
relaunch, same binary/env — no rebuild needed since only the lens *definition* was newly installed, not
Refractor's code) to pick up the `one-bill` package's DDL, which — per this repo's documented cache-at-install-time
caveat — a live Refractor won't see until restart. Confirmed via `nats kv ls` (refractor's own nkey): the
`one-bill-history` bucket now exists (created `2026-07-07 06:05:57`) and Refractor came back up with zero
errors. It shows 0 rows, which is *correct*, not a gap — `core-kv` has no `:transaction` or `:cafetransaction`
vertices yet (no rent or café charge has ever been posted on this dev stack), so both source ledgers'
own history buckets (`loftspace-ledger-history`, `cafe-ledger-history`) are equally empty; the lens has
nothing to union yet. This closes Inc 3's live-reprojection gap at the Refractor level — the cypher logic
was already proven by the embedded-NATS regression suite, and now the live install path is proven too.
**Inc 2b's separate gap is still open and untouched by this**: `cafe-app`'s own NATS nkey
(`deploy/nkeys/cafe-app.nk`, already generated) needs the shared dev NATS container's *authorization
config* reloaded before `cafe-app` can authenticate — that requires touching the shared NATS container
itself (not a single component process), a materially different risk tier from a Refractor-only restart,
so it stays deferred to an explicitly authorized full-stack cycle as before.

**Correction (2026-07-07, Steward, per Andrew's request):** the "documented F-004 caveat" cited above to
justify this restart does not hold — it was a misreading of `_packages.md`'s own Upgrade section, since
corrected there. Refractor's `CoreKVSource` holds a **durable** `vtx.meta.>` CDC subscription for the life
of the process and dispatches a newly-observed lens vertex to the **same** load callback as any other
lens, whether it arrived via a fresh install, an upgrade, or a same-version `--force`; the Processor's
`DDLCache.Invalidate` is equally unconditional. `TestCoreKVSource_LoadsLensFromAspect` proves this live at
the unit level (starts the source, *then* writes the lens). The `one-bill` lens would have started
projecting into `one-bill-history` without the restart performed here — the restart was unnecessary, not
wrong (it's a real, safe, standing capability: cycling Refractor is always available and did no harm), but
future fires should live-verify a newly-installed lens directly rather than defaulting to a restart. See
`docs/components/_packages.md`'s "brand-new entity" section for the corrected mechanism.

## Next

- **Mixed-use composition surfaces** (verticals.md) — front-desk/operations aggregate views consuming
  `one-bill-history` (and others) once a full bootstrap cycle activates it live.

## Inc 4 — café arrears reminders (`cafeArrearsReminders`)

### Fire brief (build note, 2026-09-06)

**1. Scope sentence (verticals.md row, verbatim).** *Nothing ever tells a resident they owe the café money —
Café's only Weaver targets are `cafeTabSettlement` + `cafeStaleTabSettlement`, while clinic carries
`appointmentReminders` + `followUpReminders`, wellness `wellnessBookingReminders` and LoftSpace `leaseExpiry`.
3 of 7 café debtors sit 12–19 days past the 15-day net term with nothing sent to either the resident or the
desk. Ready: an arrears convergence target off `cafeLedgerHistory`'s aged balance, mirroring
`wellnessBookingReminders`.* Green bar: a resident whose FIFO-oldest open charge passes the 15-day term gets
ONE notification per arrears episode through the bridge's `notification` adapter, the desk's arrears grid and
the resident's statement both show when it was sent, and no account is ever reminded for a balance it does
not owe (the FE's `deriveStatement` FIFO and the op's FIFO agree).

**Live premise, re-run 2026-09-06T16:00Z** (`cafe-ledger-history` read model, 36 rows / 56 lease rows): 6 debtor
leases, not 7 (one settled since filing); 4 past the term (oldest open debit 07-22, 08-01, 08-02, 08-07), 2
inside it (08-27 → due 09-11, 08-28 → due 09-12). The 4 are the first sends; the 2 are the first armed timers.

**Alternatives.** (a) *Build nothing* — the desk already sees the arrears grid and the resident sees the red
banner when they open the app; refused: the row's ask is the push (nothing is *sent*), which is exactly what
the three sibling verticals have and café lacks. (b) *Anchor the target per DEBIT transaction* (`t.entry.dueAt`
recorded at post) — refused: it fans out one reminder per aged charge (three croissants → three nags on three
days) and needs a marker write on every debit; account-level is one op per episode. (c) *Have `post_entry`
maintain the FIFO head exactly* — refused: a partial payment moves the head to a debit the op cannot name
without replaying the account's history on every payment; the head is recomputed once, by the Weaver-dispatched
op, only when it matters.

**2. Verified touch-list.**
- `packages/cafe-ledger/ddls.go:24` `accountDDL` — `PermittedCommands` += `EvaluateCafeArrears`; new
  `accountArrearsAspectTypeDDL` (class `cafeAccountArrears`, writers: DebitAccount, CreditCafeAccount,
  RefundCafeCharge, EvaluateCafeArrears); new notification-outcome DDL pair (`cafeArrearsNotificationOp` /
  `cafeAccountArrearsNotification`) in a new `notifications.go` mirroring
  `packages/wellness-reminders/notifications.go:1-140`.
- `packages/cafe-ledger/scripts.go:691-960` `post_entry` — the `.arrears` state table below, beside the `.balance`
  cache write at `:913-923`; `derive_reads` (`:978-1032`) returns `optionalReads: [<acct>.balance, <acct>.arrears]`.
  New `EvaluateCafeArrears` branch in `accountDDLScript` (`:66-127`), with the bounded `postedTo` enumeration
  mirroring `backfill_balance` (`:548-591`, `BALANCE_BACKFILL_PAGE_LIMIT`/`_MAX_PAGES` at `:545-546`).
- `packages/cafe-ledger/lenses.go:36-56` — new `cafeArrearsReminders` convergence lens (nats-kv, `full`);
  `leaseAccountsSpec` (`:97-106`) gains `arrearsDueAt` / `arrearsRemindedFor` / `arrearsReminderSentAt` columns.
- `packages/cafe-ledger/targets.go` (new) — `WeaverTargets()` with the `cafeArrearsReminders` playbook;
  `package.go:87-106` wires `WeaverTargets`, bumps `Version` 0.4.0 → **0.5.0** with `manifest.yaml` in lockstep.
- `packages/cafe-ledger/permissions.go:54-84` — `EvaluateCafeArrears` + the replyOp to `operator`/any;
  `opmetas.go:87-93,139-150` — `.arrears` added to both descriptor ops' `OptionalReads`.
- `packages/cafe-domain/targets.go:64-74` — `missing_charge`'s `OptionalReads` += `row.accountKey.arrears`
  (`DebitAccount` is dispatched from here too); `cafe-domain` 0.12.1 → **0.12.2** (`package.go:94` + manifest).
- `packages/cafe-ledger/package_test.go:36-51` structure pins (DDLs 4→7, Permissions 5→7, Lenses 2→3,
  WeaverTargets 0→1, OpMetas 2→4); `lens_cypher_test.go` new pins; `ledger_test.go` op pipeline tests.
- `cmd/cafe-app/ledger.go:16` `statementGraceDays` → `cafeledger.ArrearsGraceDays` (the package owns the term;
  one constant); `:110-122` `balanceRow` + `handleFrontDeskBalances` (`frontdesk.go:331-386`) gain
  `reminderSentAt` joined from `cafeLeaseAccounts`; the resident ledger response likewise;
  `cmd/cafe-app/web/app.js:393-405` (`statementLine`) and `:1116-1130` (`frontDeskArrearsLine`) render it.

**3. Precedents to mirror.** Lens: `packages/wellness-reminders/lenses.go:132-149` (freshUntil / gap /
`freshnessExpiry.byTarget` conjuncts, `fmt.Sprintf` on the target constant). Target:
`wellness-reminders/targets.go:31-46` (directOp, `Params` from row columns, `Reads`/`OptionalReads`). Op script:
`wellness-reminders/ddls.go:158-300` (Weaver-actor guard first statement, liveness guard, unconditioned marker,
`external.notification` keyed `<entity>:<for>`). ReplyOp: `wellness-reminders/notifications.go`. `.arrears`
write verb + `derive_reads`: `cafe-ledger/scripts.go:772-813, 913-923, 978-1032` (the `.balance` cache).
Enumeration: `scripts.go:548-591`. FIFO: `cmd/cafe-app/ledger.go:211-259` `deriveStatement` — the op's
FIFO must be the same algorithm (surplus carry-forward, sort by `(postedAt, transactionKey)` per
`sortLedgerRows` `ledger.go:28-35`); pin both against one shared vector table.

**`.arrears` state table** (class `cafeAccountArrears`; fields `evaluatedAt` always, `dueAt?`, `remindedFor?`,
`sentAt?`, `stale?`, `historyTooLong?`). B = hydrated `.balance.balanceCents` before the entry, B′ after;
"legacy" = no cache. An **episode** is the stretch from the debit that takes B from ≤ 0 to > 0 until the
balance returns to ≤ 0; `sentAt` is per-EPISODE, `remindedFor` per-HEAD (a partial payment moves the head
within one episode).
| event | write |
|---|---|
| CreateAccount | none (the account owes nothing; `evaluatedAt = null` opens `missing_evaluation` once, the op writes `{evaluatedAt}`) |
| debit, B ≤ 0, B′ > 0 | `{dueAt: postedAt + ArrearsGraceDays, evaluatedAt: postedAt}` — a new episode; create if absent, update if present (drops the old `remindedFor`/`sentAt`/`stale`/`historyTooLong`) |
| debit, B > 0 | none — the FIFO head is unchanged |
| debit, B ≤ 0, B′ ≤ 0 | none — the account is in credit and stays there; the surplus prepays the charge, so there is no open debit to age |
| credit/refund, B′ ≤ 0 | `{evaluatedAt: postedAt}` — nothing owed, no timer; create or update |
| credit/refund, B′ > 0 | present → carry every field except `historyTooLong` + `stale: true`; absent → none |
| legacy account, any entry | present → carry (minus `historyTooLong`) + `stale: true`; absent → none |
| `EvaluateCafeArrears` | recompute the FIFO head over the bounded enumeration: no open head → `{evaluatedAt}` (episode over — `sentAt` goes with it); head due D\* in the future → `{dueAt: D\*, evaluatedAt}` + carry `remindedFor`/`sentAt`; D\* passed → `{dueAt: D\*, remindedFor: D\*, evaluatedAt}` ALWAYS (this is what closes the gap), and **`sentAt` ABSENT is the send condition** — absent → `sentAt: now` + `external.notification` keyed `<accountKey>:<D\*>`; present → carried unchanged, nothing sent (one notification per EPISODE, not per head). `stale`/`historyTooLong` are never carried |
| `EvaluateCafeArrears`, history past the replay budget | DEGRADE, never refuse: `{evaluatedAt: now, historyTooLong: true}` + carry `dueAt`/`remindedFor`/`sentAt` as they stood, `stale` dropped, no notification, op ACCEPTED. A refusal would be a permanent silent stop — the row's own gap is the only thing that re-drives the op, so a rejected op leaves it open and Weaver re-dispatches a doomed evaluation every window. The lens suppresses **both** `freshUntil` and `missing_evaluation` on `historyTooLong`, so the row goes quiet but stays visible in `weaver-targets`; the next posted entry drops the flag and buys exactly one more attempt |
Crash/replay/redelivery: every `.arrears` write is create-if-absent (absence-conditioned via the declared
optional read) or a bare update auto-conditioned on the hydrated revision — a race retries, never last-write-
wins; a redelivered `Evaluate` recomputes to the same answer and the notification dedups at the adapter on the
episode key. Tombstone: a tombstoned account is refused by the liveness guard. Upgrade: 56 live accounts carry
no `.arrears` → 56 `missing_evaluation` gaps at install, one op each, then quiet.

Lens `cafeArrearsReminders` (anchor `MATCH (a:cafeaccount {key: $actorKey})`, `OPTIONAL MATCH
(a)-[:heldFor]->(l:leaseapp)`): columns `actorKey`, `entityKey`, `leaseAppKey`, `dueAt`, `remindedFor`,
`reminderSentAt`, `stale`, `historyTooLong`, `evaluatedAt`; `freshUntil = dueAt` when `dueAt` set ∧
`remindedFor <> dueAt` ∧ ¬stale ∧ ¬historyTooLong ∧ ¬(`freshnessExpiry.byTarget.cafeArrearsReminders >= dueAt`);
one gap `missing_evaluation` (=`violating`) = ¬historyTooLong ∧ (`evaluatedAt = null` ∨ `stale = true` ∨
(`remindedFor <> dueAt` ∧ `byTarget >= dueAt`)). The gap's third arm carries no `dueAt` set test — `byTarget >= dueAt`
is already false on a null `dueAt`, so the conjunct is dead there; `freshUntil` keeps its own, because there the
comparison it guards is NEGATED. `(x = null)` is the engine's null test; a mis-compiled lens FALLS BACK silently —
every conjunct gets a pin in `lens_cypher_test.go` (pending / due / sent / stale / historyTooLong / cleared /
never-evaluated / no-lease), with a `recordLapse`-style helper copied from
`wellness-reminders/lens_cypher_test.go:103-125`.

**4. Increment order.**
- **Inc 1 (package, opus — state-machine class):** everything under `packages/`. Green: `go test
  ./packages/cafe-ledger/... ./packages/cafe-domain/... -count=1`; `go test ./internal/refractor/ -run
  'TestCorpus|Census' -count=1` re-pinned deliberately; `STRICT=1 go run ./scripts/lint-conventions.go`;
  `go run ./scripts/lint-lens-anchors.go`; `go run ./scripts/lint-gap-column-declaration.go`;
  `go run ./scripts/lint-package-standard.go`; `DIFF_BASE=main go run ./scripts/lint-package-version.go`.
  Revert-proofs: delete the `stale` write and watch the partial-payment pin fail; delete the FIFO surplus carry
  and watch the shared-vector pin fail; swap the actor guard off and watch the forged-send pin fail.
- **Inc 2 (FE, sonnet):** `cmd/cafe-app` per the touch-list. Green: `go test ./cmd/cafe-app/... -count=1`,
  `go run ./scripts/lint-app-op-descriptors.go`, `go run ./scripts/lint-web.go`, `node --check` on app.js.
- **Close:** `go build ./...`, `make vet`, `golangci-lint run ./...`, cold opus review over the whole diff,
  merge, CI, then live: `make refresh-cafe` from the main checkout (diff-applies both packages + cycles
  `bin/cafe-app`), watch Weaver dispatch 56 evaluations, assert the 4 overdue accounts carry `.arrears.sentAt`
  + a `.arrearsNotification` outcome, the 2 pending carry armed `freshUntil`, and the grid/statement show it.

**5. In-scope gotchas.** Package edits bump manifest + `Version` in lockstep (both packages). `# read-posture:`
annotations on every `kv.Links`/`kv.Read` in the new branch (class (e) enumeration + follow-ups; `.arrears` is
class (d) via `derive_reads`). Time facts are recorded on the entity — `dueAt` is written by the op, the lens
never reads `$now`. The op-side FIFO sorts on `(postedAt, transactionKey)` because `rfc3339_utc` is whole
seconds (two charges in one second need the total key). Dossier entries that bind here: *a lens MATCH edit is
a corpus edit* (run the refractor census pins); *a guard's OCC rests on whoever writes its read declaration*
(`.arrears` in `derive_reads` + one test with an empty `contextHint`); *a convergence gap that re-opens on a
recorded clock lapse mints a new instance every window* (this design opens once per episode, never per
window — assert no re-dispatch after a send); *an engine-recognized companion column whose name does not match
its gap is dead* (build column names from the gap key); *a new op granted to `operator` by its own package is not
callable from the console* (Weaver dispatches, not Loupe — no console grant needed, state that in the perm
note); FE: *a server-side refusal added to one form leaves its sibling a dead end* (n/a — no new form), *an op
name in any `cmd/<app>` Go comment is a UI reference to `lint-app-op-descriptors`* (name the op by role in FE
comments). Standing checklist: (1) the `.arrears` LIFETIME is the table above; (2) the 6-debtor census is
pinned above; (3) every fix is revert-proven (listed in Inc 1); (4) n/a — nothing removed; (5) `.arrears` has
five writers, all conditioned on one hydrated revision; (6) the wellness replyOp's create-only outcome aspect
conflicts on a rescheduled class's second reply — NOT copied: café episodes recur, so the outcome aspect is an
idempotent overwrite (identical content on redelivery).

**6. Adjacent finds.** `wellness-reminders` / `clinic-reminders` notification-outcome aspects are create-only
on a single key, so a rescheduled class's second outcome reply is rejected (audit-only aspect; the marker that
gates the lens is unaffected) — absorbed as a batch unit only if the close pass has budget; otherwise it is the
same precedent-debt shape the brief already refuses to copy, recorded here. Nothing else out of scope surfaced.

**7. Non-goals.** No change to `deriveStatement`'s display math or the arrears grid's ordering; no resident-
facing notification *content* beyond the adapter's params; no reminder cadence (one per episode, no
escalation); no `.balance` backfill from the new op (a legacy account stays legacy for the payment cap).
