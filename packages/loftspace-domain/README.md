# loftspace-domain

The LoftSpace listing-economics package (v0.4.0) — adds the leasable facets of a unit **on top of**
`location-domain`'s place graph, introducing **no vertex type of its own**. `location-domain` owns
`vtx.unit.<id>` (`class=location`): the physical place, with containment via `containedIn`, root data
`{}` (D5), and no economics. This package contributes two aspects on that same unit plus a
landlord→unit management link — the cross-package **aspect/link-contribution pattern** other verticals
also use (attach data to a vertex you do not own, gated by your own DDL).

Depends: `location-domain` (install `lattice-pkg install packages/loftspace-domain` **after** it, or
`make install-loftspace` onto a running stack).

## Inventory

| Kind | Canonical names |
|---|---|
| **Vertex types** (0) | none — contributes to `location-domain`'s `vtx.unit.<id>` |
| **Aspect types** (2) | `listing`, `address` (both step-6 write gates, declaration-only — no op handler) |
| **Links** (1) | `manages` (identity → unit, landlord management) |
| **Operations** (5) | `SetListing` · `SetUnitAddress` · `SetListingStatus` · `AssignUnitOwner` · `RemoveUnitOwner` |
| **Projection lenses** (2) | `availableListings` → `loftspace-listings` (`nats-kv`) · `applicantRosterRead` → `read_loftspace_identities` (protected `postgres`, secure lens; both `full` engine) |

Every op is granted to the `operator` role at `scope: any` (`permissions.go`) — the trusted single-identity
model, no new capability surface, identical to `clinic-domain`.

## Key shapes (Contract #1)

```
vtx.unit.<id>              class=location (owned by location-domain; root {})
vtx.unit.<id>.listing      class=listing   {rentAmount, rentCurrency, bedrooms, bathrooms?, sqft?,
                                            availableFrom (RFC3339 date), leaseTermMonths,
                                            status ∈ available|pending|leased|withdrawn}
vtx.unit.<id>.address      class=address   {line1, line2?, city, region, postal}

lnk.identity.<landlordID>.manages.unit.<unitID>   (class "manages" — landlord → unit;
                                                    the actor identity is the source, the unit the
                                                    target, mirroring lease-signing's appliedToUnit
                                                    guard-link shape where both endpoints pre-exist)
```

Sentence: "landlord manages unit." At most **one** live `manages` link per (landlord, unit) — the
deterministic per-pair key is the uniqueness constraint, so `AssignUnitOwner` needs no list: it reads
the one key on demand and creates / revives / no-ops; `RemoveUnitOwner` tombstones it (the reversible
complement, so an ownership transfer never needs a tombstone-and-recreate).

Neither `.listing` nor `.address` is sensitive — they attach to a unit (`class=location`), not an
identity, so step-6's `sensitiveAspectScope` does not fire. Applicant income / employment (the sensitive
data) lives on the identity side, owned by `lease-signing` / `identity-domain`, not here.

## Operations

- **`SetListing`** — `{unit, rentAmount, rentCurrency, bedrooms, bathrooms?, sqft?, availableFrom,
  leaseTermMonths, status}`. Validates `unit` is an alive `vtx.unit.<id>` of `class=location` (listed in
  `ContextHint.Reads`), then an **unconditioned upsert** of the whole `.listing` aspect (create-if-absent
  / overwrite-if-present) — republishing (e.g. flipping `status`) overwrites in place. Returns
  `primaryKey`.
- **`SetUnitAddress`** — `{unit, line1, line2?, city, region, postal}`. Same unit validation; unconditioned
  upsert of `.address`.
- **`SetListingStatus`** — `{unit, status}`. Status-only transition: `kv.Read`s the existing `.listing`
  (rejects `NoListing` if absent) and rewrites **only** `status`, preserving the economics verbatim.
  Idempotent no-op if the status already matches (no mutation, no event — `primaryKey` omitted, since an
  empty response is required to signal "no commit"). This is the `directOp` the
  `leaseApplicationComplete` convergence target dispatches to mark a unit `leased` on approval, and the
  op a landlord calls by hand to take a unit off-market (`withdrawn`) or relist it (`available`).
- **`AssignUnitOwner`** — `{landlord, unit}`. Validates the landlord is an alive `vtx.identity` and the
  unit an alive `class=location` unit (both in `ContextHint.Reads`), reads the deterministic
  `manages` link key **on demand** (`kv.Read` — it may not exist, so a declared read would
  `HydrationMiss` on first touch, deferred past hydration), and creates it (absent), revives it via
  CAS (tombstoned by a prior
  `RemoveUnitOwner`), or no-ops (already live). Returns `primaryKey` (the link), omitted on no-op.
- **`RemoveUnitOwner`** — `{landlord, unit}`. Reconstructs the link key, reads it on demand, and
  tombstones it — idempotent (absent / already-tombstoned → clean no-op). Does not require the unit to
  still be alive (revoking ownership of a retired unit is valid).

## Projection lenses (P5 — the only application query surface)

A LoftSpace FE reads these projected read models, **never Core KV** (lattice-architecture.md P5).
Both are flat (no `WITH`/aggregation) `full`-engine projections.

- **`availableListings`** → `loftspace-listings`. One row per **listed** unit (`WHERE
  u.listing.data.status <> null` — a unit with no `.listing` is not leasable and is excluded). Flattens
  the listing economics + street address (address columns null when the unit has no `.address`) into a
  query-optimized row keyed by the unit key — the `CreateLeaseApplication` target the applicant FE
  submits. Does **not** filter on `status` itself, so a reader can default to showing `available` units
  while still surfacing `pending` / `leased` / `withdrawn` on request.
- **`applicantRosterRead`** → `read_loftspace_identities` (protected Postgres, Contract #6 §6.14 RLS).
  One row per **named** identity (`WHERE i.name.data.ct <> null` — ciphertext presence, since the
  sensitive `name` aspect holds only an envelope at rest) — the human-readable picker so a person selects
  themselves by name instead of a raw `vtx.identity.<id>` key. A **secure lens** (Contract #3 §3.10):
  Refractor decrypts the name envelope under the owning identity's DEK at projection time, so plaintext
  exists only in the RLS-protected table; a shredded identity's name projects NULL. Each row's
  `authz_anchors` carries the identity's own bare NanoID plus the managing landlord and covering
  buildings of any unit it has a live lease application against (`applicationFor -> appliesToUnit ->
  manages` / `containedIn*1..`, mirroring `cafe-domain`'s `cafeIdentitiesRead` and `lease-signing`'s
  `landlordLeaseApplicationsRead` anchor shape), plus the reserved WildcardAnchor grant — `cmd/loftspace-app`
  reads the table as the SIGNED-IN caller, for the picker and for server-side name resolution alike.

## Out of scope (owned elsewhere / deferred)

- **No vertex type.** The unit is minted by `location-domain`'s `CreateLocation(locationType=unit)`; this
  package only ever contributes aspects/links onto vertices it does not own.
- **Applicant qualification, PHI-adjacent sensitive data, and the lease lifecycle** (apply → sign →
  landlord decide) are `lease-signing` + `identity-domain`'s domain, not this package's.
- **Cascade-on-tombstone.** Neither `SetListing`/`SetUnitAddress` nor the `manages` link has a
  tombstone-cascade trigger — this matches `clinic-domain` / `lease-signing`: there is no platform
  owner-tombstone-cascade primitive (a deferred GC item), so no package builds a bespoke one.
