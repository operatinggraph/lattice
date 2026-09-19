# Café — "A house tab has no limit" (2026-09-16)

**Filed (PO, 2026-09-16):** self-order is capped only by the credit hold, which arms 15 days + a reminder after the
first unpaid debit; a new resident can run any total in the meantime. No recorded tab limit anywhere. Ask: a recorded
limit (precedent: the wellness studio no-show policy), `Charge`/`OpenTab` self path refuse `TabLimitExceeded`, desk
warned. Winston-adjudicated (implementation-level; no contract surface, no fork).

## Grounding

- **The tab's running total is a stored accumulator, so the check is one comparison, not a walk.**
  `Charge` OCC-upserts `.status.totalCents` (`new_total = existing.data.get("totalCents") + amount_cents`,
  [ddls.go:1448](../../packages/cafe-domain/ddls.go)); a limit refusal reads the same aspect the op already declares
  (`{payload.tabKey}.status`, [opmetas.go:158](../../packages/cafe-domain/opmetas.go)) — no new declared read.
- **The self path is already distinguished, and already walks the tab's place.** `is_self = op.authContextTarget != ""`
  ([ddls.go:1375](../../packages/cafe-domain/ddls.go)); a self-order always names a `menuItemKey`, so the locality bound
  resolves `tab_location = leaseapp_unit(...)` ([ddls.go:1436](../../packages/cafe-domain/ddls.go)) — the unit the
  policy walk starts from is already in hand. `OpenTab`'s self leg resolves no location today (its staff leg does,
  via `require_workplace([leaseapp_unit(lease_key)])`, [ddls.go:1225](../../packages/cafe-domain/ddls.go)).
- **The café has no venue vertex; its "house" is the location the desk works at.** Menu items are `servedAt` a
  location, staff are confined by `worksAt`, and `location_covers` walks the tab's unit up its `containedIn` chain
  ([ddls.go:819-880](../../packages/cafe-domain/ddls.go)). A house policy therefore lives on a **location** vertex —
  `unit` / `building` / `property` ([location-domain ddls.go:110-160](../../packages/location-domain/ddls.go)) — as a
  café-owned aspect, the way `OpenTab` writes `.cafeOpenTab` on lease-signing's `leaseapp`
  ([ddls.go:332-354](../../packages/cafe-domain/ddls.go)): the step-6 gate keys on `(operationType, class)`, so a
  café aspect-type DDL permits the write without touching location-domain.
- **Precedent for the policy shape.** wellness `studioProfile.noShowFeeCents` — optional non-negative integer, set by
  `SetStudioProfile` (OCC merge-upsert on a declared read, [wellness ddls.go:2211-2270](../../packages/wellness-domain/ddls.go)),
  read by `SetBookingAttendance` as an `(e)` follow-up off the studio walk, absent = no policy recorded
  ([design](wellness-noshow-fee-2026-09-15.md)). Precedent for location validation + confinement: `CreateMenuItem`'s
  `require_live_location` + `require_workplace([locationKey])` ([ddls.go:1972-1995, 2105-2145](../../packages/cafe-domain/ddls.go)).
- **The read side.** `cafeLeaseWorkplaces` projects `coveringLocations` per lease (unit + `containedIn*0..7`,
  [lenses.go:229-237](../../packages/cafe-domain/lenses.go)); `cmd/cafe-app` already joins that bucket onto
  `/api/residents` ([residents.go:95-170](../../cmd/cafe-app/residents.go)) and reads the staffer's own workplace off
  `state.anchors` ([app.js:1901](../../cmd/cafe-app/web/app.js)). `MATCH (l:location*)` is a tested head pattern
  ([taxonomy_expansion_internal_test.go:138](../../internal/refractor/pipeline/taxonomy_expansion_internal_test.go)).
- **No `scripts/verify-package-cafe-domain.go` exists** — nothing pins the café `PermittedCommands` counts.

## Verdict — *the house records a tab limit; a resident's own order stops at it; the desk sees it and may exceed it*

1. **`vtx.<locType>.<id>.cafePolicy`** (class `cafeHousePolicy`) = `{tabLimitCents}` — a non-negative integer, whole
   cents; `0` = self-service tabs closed at this house; **absent = no limit recorded**. Written only by
   **`SetCafePolicy{locationKey, tabLimitCents}`** (new op, café's `menuitem` script — the desk's house-configuration
   script; `operator` + `frontOfHouse` at `scope: any`, confined by `require_workplace([locationKey])` exactly as
   `CreateMenuItem`; `UnknownLocation` / `NotALocation` / `InvalidArgument`). Reads `[locationKey]`; optionalReads
   `[locationKey + ".cafePolicy"]` — absent → create, present → OCC upsert on its revision, tombstoned → OCC revive.
2. **The effective limit for a tab is the tightest policy on its chain.** `house_tab_limit(unit_key)` walks the
   tab's lease's unit and its `containedIn` ancestors (the `location_covers` walk, same bounds), reads `.cafePolicy`
   at each node as an `(e)` follow-up, and returns the **minimum** `tabLimitCents` found, or `None` when no node on
   the chain records one. Min, not nearest: the app composes the same rule from `coveringLocations` ∩ policies with
   no order dependence, and a property-wide cap stays a cap under a looser building policy.
3. **`Charge` self path refuses `TabLimitExceeded`** when `balanceCents + totalCents + amountCents > limit` (equal is
   allowed), where `balanceCents` is the lease's café account `.balance.balanceCents` — signed (a credit widens the
   room), `0` when the lease has no account yet or the account carries no `.balance` (a legacy account, cafe-ledger
   ddls.go). *(Amended 2026-09-18 — the limit bounds the resident's open EXPOSURE at the house, the recorded ledger balance
   plus the open tab, not one tab's total: a resident who settled without paying and reopened was at $0 again.)* The
   staff leg is unrestricted — the desk may ring past the limit, the way the wellness desk bills what it decides.
4. **`OpenTab` self path refuses `TabLimitExceeded`** when the limit is `0` (a tab that could hold nothing) **or when
   `balanceCents >= limit`** (a tab no line could join — amended 2026-09-18). Staff leg unrestricted.
5. **Lens `cafeHousePolicies`** → NATS-KV `cafe-house-policies`, one row per location carrying a policy:
   `{locationKey, tabLimitCents, name}` (`name` off `.presentation`).
6. **`cmd/cafe-app`:** `/api/residents` rows gain `tabLimitCents` (null = none) = min over the lease's
   `coveringLocations` ∩ policies; `/api/house-policies` (staff: every policy on the chains of the leases the workplace
   covers — the op binds the tightest, so the panel names a tighter one above; operator: all; resident: their chain).
   Resident view: the tab card says "House limit $X · $Y left"; the self-order picker disables an item that would
   push past it; Open Tab hides at limit 0 with a "self-service tabs are closed" panel. POS: the open-tab card says
   the limit and warns "at / over the house limit" once `totalCents >= limit`. Manage Menu: a "House tab limit"
   panel shows the staffer's workplace policy and sets it (`SetCafePolicy`).
7. cafe-domain `0.17.0 → 0.18.0`; README inventory.

## Fire brief (build note, 2026-09-16)

1. **Scope** (board row, verbatim): *Self-order is capped only by the credit hold, which arms 15 days + a reminder
   after the first unpaid debit; a new resident can run any total in the meantime. No recorded tab limit anywhere.* —
   *a recorded limit (precedent: wellness no-show policy), `Charge`/`OpenTab` self path refuse `TabLimitExceeded`,
   desk warned.* Green bar: with `tabLimitCents = 1000` on the building, a resident's `Charge` that would take the
   tab to $10.01 is refused and one to exactly $10.00 accepted; the desk's `Charge` past $10 is accepted; with
   `0` the resident's `OpenTab` is refused and the desk's accepted; with policies at $20 (property) and $10
   (building) the $10 binds; a staffer at another building cannot `SetCafePolicy` this one; the resident card
   shows the limit and disables the item that would exceed it; the POS card warns at the limit.
2. **Touch-list (verified live at fire start):** cafe-domain `ddls.go:39-43` (tab DDL — untouched),
   `:372-376` (menuitem `PermittedCommands` += `SetCafePolicy`), `:340-370` (guard aspect DDL → mirror for
   `cafeHousePolicyAspectTypeDDL`), `:819-880` (`location_covers` → sibling `house_tab_limit`), `:1214-1300`
   (`OpenTab` self leg), `:1359-1470` (`Charge` self leg, after the locality bound), `:1958-1995`
   (`require_live_location`), `:2101-2145` (`CreateMenuItem` arm → mirror for `SetCafePolicy`);
   `opmetas.go:89-91, 128-131` (facet courtesy lines), `:290-321` (`CreateMenuItem` descriptor → mirror);
   `permissions.go:119-123` (`SetMenuItemAvailability` grant → mirror); `lenses.go:82-96` (lens list),
   `:276-286` (`menuCatalogSpec` → sibling); `package.go:106` + `manifest.yaml:2`; README inventory;
   `integration_test.go`, `workplace_confinement_test.go:660-676` (staff vector shape), `lens_cypher_test.go`,
   `opmetas_test.go`. cafe-app `residents.go:38-46, 126-170`, `menu.go:149-183`, new `policies.go`, `server.go:60`
   (routes), `web/app.js:955-963, 964-1060, 1183-1220, 2113, 2164-2200, 1964-2000, 2762-2800`, `web/index.html:63-88`;
   `web_hold_test.go` (goja pin shape); `residents_test.go`.
3. **Precedents:** `SetStudioProfile` (merge-upsert on a declared optional read); `CreateMenuItem` (location
   validation + `require_workplace([locationKey])`, descriptor, grant, staff vector); `location_covers` (the walk);
   `require_no_credit_hold` (a self/staff refusal helper); `menuCatalogSpec` (plain projection + bucket const);
   `leaseTenancyEnds` (the residents join); `renderCreditHoldPanel` (a no-button panel); `menuOptions`'s disabled
   optgroup (the picker courtesy).
4. **Increments:** (1) package — aspect DDL + `SetCafePolicy` + `house_tab_limit` + the two refusals + descriptor +
   grant + lens + version + README; green: `go test ./packages/cafe-domain/ -count=1`. (2) app — the join, the
   endpoint, the three views, goja pins; green: `go test ./cmd/cafe-app/ -count=1`. Then `go build ./...`, `make vet`,
   `golangci-lint run ./...`, every `scripts/lint-*.go` under `STRICT=1`, `gofmt -l scripts/`, `DIFF_BASE=<base> go run
   ./scripts/lint-package-version.go`. Live: `make refresh-cafe`, cycle `bin/cafe-app`, drive the green bar via the
   Gateway as Riley Chen (resident) + Dana Whitfield (desk).
5. **In-scope gotchas:** the new refusal codes must be declared at every dispatch site (`lint-refusal-courtesy`:
   both app.js sites for `OpenTab` and `Charge` + the two `(facet)` lines); `SetCafePolicy` reaches
   `require_workplace` → `lint-workplace-staff-vector` needs a non-operator staff vector; the walk's `kv.Links`
   carries a page limit (`lint-links-page-limit`); the `.cafePolicy` upsert is OCC'd on a **declared** optional
   read, never a live one (`lint-live-read-pinned-mutation`); every `kv.Read`/`kv.Links` in `house_tab_limit` is
   `# read-posture: (e)` annotated; version + `Version` constant bump together. Dossier (`_packages.md`): *a recorded
   value is read as the FACT it records* — the limit bounds the tab's `totalCents`, nothing else; *a standing
   scope=any write on an entity with no workplace confines by the target's state machine* — `SetCafePolicy` is
   confined by the location itself; *a mirror that drops one of the precedent's checks drops the invariant* —
   `require_live_location` checks key type AND class. Dossier (`vertical-apps.md`): *a count/label the FE promises
   applies the op's own predicate* — the picker's "over the limit" test is `totalCents + priceCents > limit`, the
   script's exact conjunct, pinned in goja; *the staff-leg context drops `{actor}` enumerations* — the
   `SetCafePolicy` submit declares `holdsRole`; *unescaped markup* — the location name renders through `escapeHtml`.
   Standing checklist: (1) the policy aspect's lifetime — created by `SetCafePolicy`, carried across every other op,
   never reset; a tombstoned location's policy dies with it (the walk skips a tombstoned node); (3) revert-proof:
   disable the self-leg check and watch `TestCharge_SelfRefusedOverHouseLimit` fail; (5) one writer
   (`SetCafePolicy`) of one deterministic key; (6) `CreateMenuItem`'s confinement shape is verified against
   `lint-workplace-staff-vector`, not merely copied.
6. **Adjacent finds:** none filed at scoping.
7. **Non-goals:** ~~a limit on the ledger balance (the credit hold's lane)~~ *(struck 2026-09-18 — the balance is now
   inside the bound; the credit hold remains the reminded-arrears refusal)*; a per-resident limit; a way to delete a
   recorded policy (mirrors the wellness profile — a policy is edited, never removed; a large value lifts it);
   Facet's descriptor form (courtesy `none` — the edge lenses project no tab limit).

### Build note (2026-09-16)

Shipped `71b71bc5` (merge `c6136985`); brief `9d1f05e5`. One commit, both increments. Live on the shared stack
(cafe-domain 0.17.0 → 0.18.0 diff-applied, `bin/cafe-app` cycled): Dana Whitfield recorded `SetCafePolicy{Riverside,
900}` through the Gateway and `cafeHousePolicies` projected the row within 6 s; Riley Chen's `/api/residents` row read
`tabLimitCents: 900`; Riley opened a tab and self-ordered two croissants ($7.00), the third was refused
`TabLimitExceeded: this house limits a self-service tab to $9.00; the tab stands at $7.00 and this item is $3.50; ask
the desk` (step 5 wall 45 ms, 8 live reads, 5 listings; the accepted self-orders 61–65 ms), Dana rang a fourth past the
limit ($10.50); after Riley settled, `SetCafePolicy{…, 0}` closed the house and Riley's `OpenTab` was refused
`TabLimitExceeded: self-service tabs are closed at this house`; the limit was then set to $50.00 and left there. Rendered:
the Manage Menu panel reads *House tab limit at Riverside Building: $50.00 per tab* with the input prefilled, the POS
and resident cards read *House limit $50.00 · $46.50 left* over Riley's $3.50 tab.

**Deviations from the brief.** (1) The lens head is `MATCH (loc:location*)` — the corpus's first EXHAUSTIVE `*` sigil
lens: the label-cap gate prices it (0 concrete labels + location's LeafBudget 5) and the pkgmgr sigil-corpus pins were
re-stated; anchor derivation refuses the sigil head (no single key prefix), so the lens runs on the enumerator, and its
seven refractor census verdicts are pinned as derived (no neighbour, so no retraction-transport debt). (2) `SetCafePolicy`
lives in the menuitem script (the desk's house-configuration script), dispatched `class: menuitem`. (3) The staff read
of `/api/house-policies` returns the covered chains, not only the workplace's own row (review F1). (4) Starlark `class`
is a keyword — the policy's class check is `getattr(policy, "class", None)`. (5) `lint-app-op-descriptors`' café ceiling
10 → 11 (the panel is a single prefilled input beside the hand-built Add-item form; the wellness `SetStudioProfile`
shape). (6) `parseDollarsOrZero` refuses a fraction of a cent rather than rounding it.

**Close-pass classification.** Cold review (opus): nothing blocking. F1 panel promised the workplace's own row as the
rule (design-gap — the FE-promise class, sixth sighting, `vertical-apps.md`); F2 second chain walk on the self Charge
(perf — measured live at 45–65 ms under the 250 ms wall, no fold; the walk stays a sibling of `location_covers`); F3
class filter parity op ↔ lens (implementation-bug, fixed); F4 missing vectors (test-gap — tombstoned node on the chain,
foreign-class and malformed values, `SetCafePolicy` on a unit, OpenTab under a positive limit / on a property chain, all
added); NITs: fail-open bounds documented, whole-cent parse, README op count, one `cafe-lease-workplaces` list per
residents request (all fixed); the operator has no `worksAt` anchor so cannot set a policy from the UI (left: the
operator sets it by CLI, the desk from the panel). CI: `unit-4` failed on `internal/substrate`'s
`TestKVMarkerProvenance_ExpiryIsTheOnlyMaxAgeMarker` (untouched, the same flake the previous two café fires met; a
whetstone chip already stands) and was rerun.

## Amendment — the house limit bounds open exposure, not one tab (2026-09-18)

**Filed (PO, 2026-09-17):** *`house_tab_limit` bounds one tab's `totalCents`; a resident settles (no payment changes hands)
and reopens in two taps, and the credit hold arms only after a reminded 30-day due date. Bound the open balance + tab, or a
per-day tally. Live: settle 20:35:54 → new tab 20:44.* Winston-adjudicated (implementation-level; the 2026-09-16 non-goal
was a scoping choice, not a ratified posture).

**Decision — the balance joins the bound; no per-day tally.** A per-day tally is new state with a lifetime (a day boundary
in some zone — the clinic hours row shows what UTC days cost) and bounds the wrong thing: the house cares what a resident
*owes*, and that is already recorded O(1) at `vtx.cafeaccount.<id>.balance.balanceCents` (cafe-ledger `cafeAccountBalance`,
kept in lockstep by every posted entry). The self-leg check becomes `balanceCents + totalCents + amountCents > limit`; the
account is reached exactly as `require_no_credit_hold` reaches `.arrears` — `cafe_account_for_lease(lease_key)` (the
`heldFor` in-link walk) then one `(e)` follow-up read of `.balance`. Read posture: no account → `0`; `.balance` absent or
tombstoned → `0` (a legacy account, the ledger's own documented absence); wrong class → `InvalidState` (the credit-hold
precedent's class check). Signed: a credit balance widens the room. **Accepted window:** a settled tab's debit posts via
`cafeTabSettlement.missing_charge` seconds after `Settle`; between the two the recorded balance excludes it, so a
settle-and-reopen inside that window sees the old room once. The refusal reads the recorded fact, never re-derives it.
`OpenTab` self leg additionally refuses when `balanceCents >= limit` (no line could join). Field name `tabLimitCents`,
class `cafeHousePolicy` and code `TabLimitExceeded` stay — an installed value is not renamed; the descriptions say what the
limit now bounds. `Charge` cannot declare the `heldFor` walk (its hub is the tab's own `leaseAppKey`, data-derived —
`OpDispatchSpec.Enumerations` admits `{actor}` / `{payload.<field>}` only), the same class as the `leaseapp_unit` walk it
already runs; `OpenTab` already declares it.

### Fire brief (build note, 2026-09-18) — three café rows in one fire

1. **Scope** (board rows, verbatim): **(A)** *`house_tab_limit` bounds one tab's `totalCents` (ddls.go:1612); a resident
   settles (no payment changes hands) and reopens in two taps, and the credit hold arms only after a reminded 30-day due
   date. Bound the open balance + tab, or a per-day tally.* **(B)** *`computeLeases` sorts by lease key (leases.go:46) and
   `fillLeaseSelect` renders one flat `<select>` (app.js:873): 49 disabled seed rows interleaved with 6 usable, no
   open-tabs-first, no type-to-find. Live: the one open tab sat 19th.* **(C)** *`postedAt` renders verbatim (app.js:2515)
   beside a localized "Due 9/30/2026" and "Opened Sep 17, 2026, 1:40 PM"; the `localDateTime` formatter covers the cards
   only.* Green bar: (A) with a $10 limit and a $6 recorded balance, a resident's `Charge` taking the tab to $4.01 is
   refused and one to exactly $4.00 accepted; with a $10 balance the resident's `OpenTab` is refused `TabLimitExceeded`
   and the desk's accepted; a −$2 credit balance admits a $12 tab; a lease with no account behaves as today; the resident
   card reads the balance in its room-left line and the picker disables what would not fit; the POS card warns on the
   same figure. (B) the POS picker lists leases with an open tab first, then billable leases by resident name, then the
   disabled rows in a trailing "Not billable" group, and a type-to-find input narrows the options by name / unit. (C)
   every `postedAt` on the statement renders through `localDateTime`.
2. **Touch-list (verified live 2026-09-18):** cafe-domain `ddls.go:54,77` (tab/Charge DDL descriptions), `:450,542`
   (`tabLimitCents` descriptions), `:1169-1204` (`cafe_account_for_lease`), `:1206-1234` (`require_no_credit_hold` → mirror
   for a sibling `house_exposure_balance(lease_key)`), `:966-1012` (`house_tab_limit`), `:1455-1462` (OpenTab self leg),
   `:1662-1675` (Charge self leg); `opmetas.go:92,133` (facet courtesy lines); `README.md:26,146-160`; `package.go:106` +
   `manifest.yaml:2` (0.19.0 → 0.20.0); `house_tab_limit_test.go:180-303,381-419` (+ vectors seeding a `heldFor` account
   with `.balance`, the shape of `integration_test.go:646-731`). cafe-app `web/app.js:404-427` (`tabLimitOf`,
   `tabLimitRemaining`, `houseLimitLine` → take the balance), `:578` (`menuOptions`), `:838-875`
   (`loadLeasePickerContext` / `loadBalances`), `:895-932` (`fillLeaseSelect`), `:944` (`tenancyEnded`), `:1000`
   (`localDateTime`), `:1034,1044-1045,1052-1123` (POS load / courtesy lines / `renderPos`), `:1312` (`renderOpenTabCard`),
   `:2442,2464-2465,2490,2508-2535` (resident load / courtesy lines / ledger read / card), `:2696,2699` (statement
   `postedAt`); `web/index.html:28-29,108-109` (the two pickers); `residents.go:118-125` (comment); `web_house_limit_test.go`
   (the three pins → exposure), new `web_lease_picker_test.go` + a statement-stamp pin (`web_escape_test.go:98-188` shape).
3. **Precedents:** `require_no_credit_hold` (the account walk + `(e)` follow-up + class check); cafe-ledger's `.balance`
   absence semantics (ddls.go:154-215); `tabLimitRemaining` / `houseLimitLine` goja pins; clinic `#patient-search`
   (`cmd/clinic-app/web/index.html:14`, `app.js:856-870`) for the type-to-find input; `renderFrontDeskArrears`' sort
   (`app.js:1809-1835`) for a ranked list; `localDateTime` call sites (`app.js:1323,2109,2527,2619`).
4. **Increments:** (1) package — `house_exposure_balance`, the two refusals, descriptions, README, version; green:
   `go test ./packages/cafe-domain/ -count=1`. (2) app — the exposure in the three helpers + card lines + picker, the POS
   picker ranking + search, the statement stamps, goja pins; green: `go test ./cmd/cafe-app/ -count=1`. Then `go build
   ./...`, `make vet`, `golangci-lint run ./...`, every `scripts/lint-*.go` under `STRICT=1`, `gofmt -l scripts/`,
   `DIFF_BASE=<base> go run ./scripts/lint-package-version.go`. Live: `make refresh-cafe`, drive the green bar via the
   Gateway as Riley Chen (resident) + Dana Whitfield (desk); screenshot the POS picker.
5. **In-scope gotchas:** the changed refusal predicate must be restated at every `TabLimitExceeded` courtesy site
   (`lint-refusal-courtesy`: app.js 1044-1045 · 2464-2465 · opmetas 92/133); the new walk in `Charge` is `# read-posture:
   (e)` annotated at both `kv.Links` and the follow-up `kv.Read` (`lint-conventions`), paged with `LIVE_LINK_PAGE_LIMIT`
   (`lint-links-page-limit`); `.balance` is never a declared read of `Charge` (data-derived hub) — no `derive_reads`
   entry; `Charge` mutates `.status` only, the balance's writers (`DebitAccount` et al.) share no key → the race is
   accepted at the site (dossier: *a refusal that reads a key the op never writes is advisory* — state the window);
   version + `Version` constant bump together; `lint-app-op-descriptors` café ceiling 11 (no new op). Dossier
   (`_packages.md`): *a recorded value is read as the FACT it records* — `balanceCents` is what the ledger recorded, the
   in-flight debit is not re-derived; *a mirror that drops one of the precedent's checks drops the invariant* — the
   balance reader keeps the credit-hold's class check and its no-account branch. Dossier (`vertical-apps.md`): *a count
   the FE promises applies the op's own predicate* — the picker's over-limit test becomes `balance + total + price >
   limit`, the script's exact conjunct, pinned in goja; *two courtesy surfaces name the same instant in different
   zones* — `postedAt` is an instant, rendered locale everywhere on the statement; *a new key's census walks the app's
   wire structs* — the resident card takes `ledger.balanceCents` (already on the wire), the POS card
   `balances[lease].balanceCents`; *a released uniqueness guard invalidates every FE set* — the picker's open-tab rank
   reads `/api/tabs` `status === "open"`, one per lease by the `.cafeOpenTab` guard. Standing checklist: (1) no new
   state — the picker's order and filter are derived per render; (3) revert-proof: drop the balance term and watch
   `TestCharge_SelfRefusedOverExposure` fail, drop the rank and watch the picker pin fail; (5) no new writer; (6)
   `require_no_credit_hold`'s walk is verified against `lint-links-page-limit`, not merely copied.
6. **Adjacent finds:** none at scoping.
7. **Non-goals:** counting a settled-unposted tab (the accepted window above); a per-day tally; a limit on the desk;
   renaming `tabLimitCents`; server-side ranking in `computeLeases` (names live only in the FE's identity join).
