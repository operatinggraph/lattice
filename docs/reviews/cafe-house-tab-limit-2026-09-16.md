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
3. **`Charge` self path refuses `TabLimitExceeded`** when `totalCents + amountCents > limit` (equal is allowed). The
   staff leg is unrestricted — the desk may ring past the limit, the way the wellness desk bills what it decides.
4. **`OpenTab` self path refuses `TabLimitExceeded`** when the limit is `0` (a tab that could hold nothing). Staff
   leg unrestricted.
5. **Lens `cafeHousePolicies`** → NATS-KV `cafe-house-policies`, one row per location carrying a policy:
   `{locationKey, tabLimitCents, name}` (`name` off `.presentation`).
6. **`cmd/cafe-app`:** `/api/residents` rows gain `tabLimitCents` (null = none) = min over the lease's
   `coveringLocations` ∩ policies; `/api/house-policies` (staff: the workplace-covered policies; operator: all).
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
7. **Non-goals:** a limit on the ledger balance (the credit hold's lane); a per-resident limit; a way to delete a
   recorded policy (mirrors the wellness profile — a policy is edited, never removed; a large value lifts it);
   Facet's descriptor form (courtesy `none` — the edge lenses project no tab limit).
