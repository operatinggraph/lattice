# Clinic multi-site — design note

**Status:** Increment 1 (backend) shipped. Increment 2 (FE) queued.
**Board row:** [verticals.md](../planning-artifacts/backlog/verticals.md) — "Clinic is a single-location, single-specialty silo".

## Problem

`location-domain` was unused by `clinic-domain` (unlike `loftspace-domain`, which
already integrates it): a provider had exactly one `specialty` and no site. A real
multi-location practice group needs provider↔location association and (eventually)
per-location scheduling.

## Pattern mirrored

`loftspace-domain`'s two-DDL cross-package-contribution shape onto `location-domain`'s
place graph (`loftspaceListing` aspect-contribution + `loftspaceOwnership`
link-contribution), applied 1:1 to clinic:

- **`clinicSite`** (vertexType DDL, owns `SetSiteProfile`) writes a `.site` aspect
  `{name}` onto a `vtx.building` (validated alive + `class=location`). `clinicSiteProfile`
  is the paired aspectType (step-6 write gate) DDL — mirrors `loftspaceListing`/`listing`.
- **`clinicSiteAssignment`** (vertexType DDL, owns `AssignProviderSite` /
  `RemoveProviderSite`) writes/tombstones the `practicesAt` LINK
  `lnk.provider.<id>.practicesAt.building.<id>` — source = provider (the later-arriving
  fact — a provider is *assigned* to a site), target = building. Reads as "provider
  practicesAt building." Create/revive-CAS/no-op idempotency, deterministic per-pair
  key read on demand (`kv.Read`, declared `optionalReads`) — mirrors
  `loftspaceOwnership`'s `AssignUnitOwner`/`RemoveUnitOwner` exactly, including the
  many-to-many shape (a provider may practice at many sites; a site may host many
  providers — no list needed, the pair key alone is the uniqueness constraint).

`clinic-domain` now `depends: [location-domain]` (previously self-contained).

## Lenses

- **`clinicSites`** (nats-kv, one row per named building) — mirrors `availableListings`'s
  flat single-MATCH shape. Site directory read model.
- **`providerSites`** (nats-kv, one row per `(provider, site)` pair, composite
  `IntoKey: [provider_id, site_id]`, `DiffRetraction: true`) — mirrors
  identity-hygiene's `duplicateCandidates` exactly. A provider×site join was
  deliberately NOT folded as an array column into `clinicProviders` (a `collect()`
  aggregation's grouping semantics inside a non-`$actorKey`-anchored "full" multi-row
  lens is unproven in this codebase — every existing `collect()` use anchors on a
  single actor key. A separate one-row-per-pair lens with a composite key sidesteps
  the question entirely and has a proven precedent.)

## What's NOT in this increment (Increment 2, next fire)

- **FE**: a site directory admin page (`SetSiteProfile`/`AssignProviderSite` forms) and
  a `#book-site` filter in the booking picker (mirrors `8315a88`'s specialty filter).
- **Per-location scheduling hours**: today `.hours`/`.timeOff` are still keyed only on
  the provider — a provider practicing at two sites has ONE availability set shared
  across both. Making hours vary per-(provider, site) is a real design fork (does the
  15-minute slot-claim key become `vtx.provider.<id>.location.<siteId>.slot<cellcode>`,
  or does a provider's per-site hours simply gate booking while the claim stays
  provider-global — which already correctly prevents a provider being double-booked
  across two sites at once, a desirable side effect of keeping the claim provider-
  scoped). Recommend keeping the slot-claim provider-scoped (no change) and adding an
  optional per-site `.hours` override read at booking time — smallest change, preserves
  the cross-site double-book guard for free. Left as a follow-up design decision, not
  blocking Increment 1.
- **Appointment→site association**: `CreateAppointment` does not yet record which site
  a booking is at. Needed before per-location hours can gate anything. Increment 2.

## Verification

`packages/clinic-domain/site_integration_test.go` — full lifecycle through the real
Processor (create/idempotent-reassign/revive/reject-dead-provider/multi-site). Makefile
install-order (`install-clinic`, `verify-package-clinic-domain`,
`verify-package-clinic-reminders`, `refresh-clinic`) and
`scripts/verify-package-clinic-domain.go` updated for the new dependency + DDLs + ops.
