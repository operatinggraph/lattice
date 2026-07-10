# Mixed-use composition surfaces — design + checkpoint

**Status:** 🏗️ building (Inc 1-4 shipped). Board row: [verticals.md](../planning-artifacts/backlog/verticals.md).

## Goal

The backlog item names two views that exist only because LoftSpace/Clinic/Café/Wellness share one
graph:

- **Front-desk unified resident context** — lease + visit + open tab + booked class, in one lookup,
  surfaced before asked.
- **Operations portfolio-pulse aggregate** — occupancy + service-attach-rate across packages.

## Grounding

- **Linking model is LINK-based, not shared-vertex-aspect.** All four verticals converge on one
  `vtx.identity.<NanoID>`, but each vertical's own vertex (leaseapp/patient/booking/tab) stays a
  separate vertex connected by an explicit link:
  - `packages/lease-signing/ddls.go` — leaseapp→identity via `applicationFor`.
  - `packages/clinic-domain/ddls.go` — patient→identity via `identifiedBy`.
  - `packages/wellness-domain/ddls.go` (`bookingVertexTypeDDL`) — booking→identity via `bookedBy`,
    plus an *emergent* `residentRate` link (booking→leaseapp) written ONLY when a supplied
    `leaseAppKey`'s `applicationFor` link resolves to the same identity as the booker, AND the
    leaseapp carries a `.tenancy` aspect (CreateOnly-stamped on the first `DecideLeaseApplication`
    approve) — a mismatch or unapproved lease falls through to `rate=standard`, never a hard failure.
  - `packages/cafe-domain/ddls.go` (`tabVertexTypeDDL`) — tab carries `leaseAppKey` denormalized onto
    its own `.status` aspect (not a fresh link) and the `openFor` link (tab→leaseapp).
- **Precedent: `packages/one-bill`** (Café Inc 3) — a lens-only package with no DDLs of its own,
  re-projecting two OTHER packages' data (loftspace-ledger + cafe-ledger transactions) into one shared
  bucket, tagged by `source`, because the cypher engine has no UNION. `front-desk` (below) mirrors this
  shape exactly.

## Increment 1 (shipped this fire)

**Front-desk: café open tab + resident's booked wellness class**, scoped down from the full 4-way +
operations aggregate (too large for one fire — see Deferred below):

- New lens-only package `packages/front-desk` (mirrors `one-bill`): one Lens, `frontDeskBookings`,
  re-projecting wellness-domain's `residentRate`-linked, `booked`-status bookings into
  `front-desk-bookings`, keyed by `leaseAppKey`. A booking with no `residentRate` link (standard rate,
  or an unclaimed/unapproved lease) never projects — front-desk shows only a resident's OWN booking,
  never every booking in the building.
- The café half (open tabs) needed **no new lens** — `cafe-domain`'s own `cafeTabSettlement`
  convergence lens already serves it keyed by `leaseAppKey`; the FE joins the two client-side by
  `leaseAppKey`, the same composition idiom `cmd/cafe-app`'s `computeTabs` and wellness-domain's own
  deliberately-uncounted `bookedCount` already use.
- `cmd/cafe-app`: new `GET /api/frontdesk-bookings` handler (`frontdesk.go`), wired into the existing
  Front Desk view (`web/app.js` `loadFrontDesk`/`frontDeskCard`) — each open-tab card now shows a
  "🧘 Booked: `<session>` · `<time>`" line when the resident has a live resident-rate booking.
  Best-effort: an unreachable/uninstalled `front-desk` bucket degrades to "no badge," not a page error.
- Registries: `cmd/lattice-pkg/main.go`, `cmd/loupe/pkg.go` (`packageRegistry`); `Makefile`
  `install-frontdesk` (mirrors `install-onebill`, depends on `wellness-domain` being installed first).
- Tests: `packages/front-desk/lens_cypher_test.go` (real rule-engine proof against the production
  spec — resident-rate row projects, standard-rate row doesn't, a cancelled/soft-deleted booking
  doesn't), `package_test.go` (manifest/definition parity), `cmd/cafe-app/frontdesk_test.go`
  (tombstoned-row skip).
- Live-verified: installed on the running dev stack (`front-desk` package, `writeCount=8`); cycled
  `cafe-app` to the rebuilt binary; the Front Desk view fires `GET /api/tabs` + `GET
  /api/frontdesk-bookings` (both 200), no console errors. **Not** live-verified: an actual populated
  booking badge — the one pre-existing lease on the shared stack has no `.tenancy` aspect (never
  signed/approved), and mutating that pre-existing, not-created-this-session vertex to force it
  through sign+approve was correctly blocked by the auto-mode safety classifier (modifying shared
  state without user authorization). The positive-projection case is instead proven by
  `lens_cypher_test.go` against the real rule engine using the exact production `bookingsSpec`
  constant — the strongest available proof short of a live click-through.

## Increment 2 (shipped this fire) — portfolio-pulse: occupancy

Scoped down from the full portfolio-pulse aggregate (occupancy + service-attach-rate) to the
**occupancy half only** — the same scoping-down move Inc 1 made for front-desk:

- **New protected Postgres lens `landlordUnitsRead`** (`packages/loftspace-domain`, v0.6.0 → 0.7.0):
  `MATCH (u:unit)<-[:manages]-(landlord:identity)`, no leaseapp required — a vacant, never-applied-to
  unit still projects a row, unlike lease-signing's `landlordLeaseApplicationsRead` (which requires a
  leaseapp to exist at all). Composite `unit_id`/`landlord_id` `IntoKey` (a co-managed unit fans out to
  one row per landlord, mirroring `landlordLeaseApplicationsRead`'s `app_id`/`landlord_id` shape).
  `authz_anchors = [nanoIdFromKey(landlord.key)]` — the same §6.14 set-membership RLS the
  primordial cap-read self-grant already licenses. `DiffRetraction: true` — the MATCH walks `manages`
  structurally (not an anchor-key equality), so a `RemoveUnitOwner` unassign needs Refractor's
  target-diff retraction path, same reasoning as the precedent lens. `unit_status` projects **null**
  for a managed-but-never-listed unit (its own bucket, not an excluded row) — proved by
  `landlord_units_lens_test.go`'s rule-engine tests (managed+listed, managed+unlisted-null-status,
  unmanaged-excluded, co-managed-fans-out-per-landlord).
- **`cmd/loftspace-app/portfolio.go`**: `GET /api/portfolio-pulse` — sibling of
  `handleLandlordApplications` (identical verified-JWT → per-request txn → `SET LOCAL
  lattice.actor_id` → RLS path). Folds the RLS-scoped rows into `{totalUnits, leased, available,
  pending, withdrawn, notListed, occupancyRate}` (`summarizePortfolioPulse`, occupancyRate = 0 when
  the landlord manages no units — never divides by zero).
- **FE**: a `#portfolio-pulse` banner in the landlord view (mirrors the `#landlord-rls` RLS-banner
  idiom) — `loadPortfolioPulse()` reads the endpoint on landlord sign-in/refresh, degrades to hidden
  (not a page error) when the boundary is unavailable, same best-effort posture as `loadLandlordRLS`.
- **Registries**: package version bump only (0.6.0 → 0.7.0, `package.go` + `manifest.yaml`); no new
  DDL/permission, so `make verify-package-loftspace-domain` (which asserts DDLs/permissions/package
  vertex, not lenses) needed no update — the lens shape is pinned by
  `TestPackage_ManifestMatchesDefinition` + `TestPackage_Permissions`' lens-count/shape assertions
  instead.
- Live-verify: `make refresh-loftspace` diff-applied the bumped package + cycled `bin/loftspace-app`
  on the running dev stack (F-004, no teardown). The new lens is `Protected`, so it started
  infra-paused (Contract #6 §6.14 verify-and-pause — Refractor issues no runtime DDL for a protected
  table); `make provision-readpath` (not yet part of the documented refresh flow — see
  `reference_protected_lens_provision_readpath` in Steward memory) created `read_landlord_units` +
  its RLS policy, the probe loop auto-cleared the pause (`"dependency recovered, resuming"`, no
  manual control-plane call), and `GET /api/portfolio-pulse` with a real dev-minted token returned 4
  live-projected units (`available` × 4, rents $2500/$2500/$2400/$2200) — full round-trip proven,
  not just the rule-engine tests.

## Increment 3 (shipped this fire) — portfolio-pulse: service-attach-rate

Of the landlord's currently-occupied (signed) leases, what fraction have a live wellness booking or
an open café tab — the other half of portfolio-pulse, deferred from Inc 2.

**Grounding (resolved before building, was the open question Inc 2 left):** where does the
cross-package join live? Confirmed **two existing precedents** for a vertical app reading a
*different* package's lens bucket — `cmd/cafe-app` already reads `packages/front-desk`'s
`front-desk-bookings` bucket (`cmd/cafe-app/frontdesk.go`), and `cmd/loftspace-app` already reads
`packages/privacy-base`'s PII-envelope bucket (`cmd/loftspace-app/objects_crypto.go`). So this is
applying an established pattern a second/third time, **not** inventing a new cross-package
mechanism — no primitive to file, no Designer/Andrew gate; built directly in `cmd/loftspace-app`
(the app that already owns occupancy).

- **`occupiedLeaseAppKeys`**: the landlord's signed applications (`queryLandlordApplications`,
  already read for the separate landlord-applications view) filtered to `SignedAt != nil` — the
  occupied-lease set attach-rate measures against (distinct from occupancy's unit-keyed rows, since
  `landlordUnitsRead` has no `leaseAppKey` at all — it's unit-centric via `manages`, not
  leaseapp-centric).
- **`computeServiceAttachRate`**: folds `front-desk-bookings` (keyed by `leaseAppKey`) and the
  shared `weaver-targets` bucket (`cafeTabSettlement.*` prefix, `leaseAppKey` in the body) down to
  the intersection with this landlord's occupied set — both buckets are global/cross-landlord, so
  the intersection is also the privacy boundary: never surfaces another landlord's or resident's
  raw row, only the count. A tab counts as attached while its status isn't `"settled"`; a booking
  counts by existing (`frontDeskBookings` already filters to `status='booked'`).
- **Best-effort**: unlike occupancy (502s if Postgres is down), a missing NATS connection or a
  failed KV read leaves `occupiedLeases`/`serviceAttached`/`serviceAttachRate` at zero rather than
  failing the whole `/api/portfolio-pulse` response — mirrors front-desk-bookings' own "no bucket =
  no rows, not an error" posture. FE (`loadPortfolioPulse`) omits the attach-rate clause entirely
  when `occupiedLeases` is 0, rather than showing a misleading "0% attached".
- Live-verify: landlord "Cap Default Verify" (`vtx.identity.8citcJ8PYhszmbMdPsuD`, 2 managed units,
  0 signed leases in this dev dataset) correctly rendered "0% occupied (0/2 leased, 2 available)"
  with the attach-rate clause correctly omitted — no dev-dataset lease is signed yet, so the
  positive-attach path is proven by `TestComputeServiceAttachRate` (unit test) against the real
  join logic, not live-clicked; no console errors.

## Increment 4 (shipped this fire) — front-desk: lease details

The other front-desk tail from the Deferred list below: term/rent on the card, not just the
`leaseAppKey` short-key.

**Grounding correction (verified before building, was the open question this Deferred note left):**
Inc 3's note claimed "loftspace-ledger's existing lens already carries it" — **false**, verified by
reading both `packages/loftspace-ledger/lenses.go` (ledgerHistory/leaseAccounts, neither carries unit
rent/term) and `packages/cafe-domain/lenses.go` (tabSettlement, same). No existing unprotected,
staff-readable lens keyed by leaseAppKey carries unit rent/term — `leaseApplicationsRead` /
`landlordLeaseApplicationsRead` (lease-signing) do, but both are §6.14 Protected/RLS-anchored to the
applicant or landlord, not café-staff-readable. Rather than copy the stale premise, added a **second
small unprotected nats-kv lens in `front-desk` itself** (`frontDeskLeaseDetails`, mirrors
`frontDeskBookings`' own shape) — package-level lens work, no primitive, no Designer/Andrew gate.

- **`frontDeskLeaseDetails`** (`packages/front-desk/lenses.go`, 0.1.0 → 0.2.0): one row per leaseapp
  (not per booking — every open tab needs a row, not just those with a booked class), keyed by
  `leaseAppKey`. `MATCH (l:leaseapp) OPTIONAL MATCH (l)-[:appliesToUnit]->(u:unit)` — OPTIONAL mirrors
  lease-signing's `leaseApplicationCompleteSpec` (unit is required at CreateLeaseApplication, so no
  `missing_unit` gap, but a tombstoned unit must not drop the anchor). Projects `unitAddress` /
  `unitRent` / `unitCurrency` / `unitLeaseTermMonths` off the unit's `.address`/`.listing` aspects.
- **`cmd/cafe-app`**: new `GET /api/frontdesk-lease-details` handler (`frontdesk.go`), same
  best-effort posture as the bookings handler (missing bucket → empty list, not an error). FE
  (`loadFrontDesk`/`frontDeskCard`) joins it client-side by `leaseAppKey`, same idiom as the bookings
  join — each open-tab card now shows a "🏠 $X USD/mo · Nmo term" line when the lease has an applied-to
  unit.
- Tests: `lens_cypher_test.go` (unit row projects rent/term; a leaseapp with no `appliesToUnit` link
  still projects a row, rent/term null), `frontdesk_test.go` (tombstoned-row skip, mirrors the
  bookings test).
- Live-verified: `make reinstall-package PKG=packages/front-desk` diff-applied 0.1.0→0.2.0 on the
  running dev stack (`upgrade committed ... created=6 updated=2`); cycled `bin/cafe-app`; the new
  bucket projected real data (`$2500 USD/mo`, 12mo term) via curl. In-browser: opened a real tab via a
  direct op submission (the browser sandbox's fetch to the gateway's `:8080` origin fails —
  `net::ERR_FAILED`, unrelated to this change — so the op that creates the fixture was submitted via
  curl instead of the UI's own "Open Tab" button), reloaded the Front Desk view, and the card rendered
  "🏠 $2500 USD/mo · 12mo term" with no console errors; settled the tab afterward to leave the dev
  dataset clean.

## Deferred (Inc 5+, not yet scoped in detail)

- **Clinic visit in the unified context** — deliberately excluded from Inc 1 per the PHI-sensitivity
  note on the *separate* "Clinical notes are write-only" backlog row (clinic patient data has its own
  Secure-Lens/Vault posture, `identifiedBy` claim semantics differ from `residentRate`'s optional/
  best-effort link) — needs its own grounding pass, not a copy-paste of this pattern.

**Next fire on this item:** the clinic-visit tail (its own PHI/Vault grounding pass) — the last item
in the Deferred list; re-read this doc's Deferred section first.
