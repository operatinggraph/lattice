// Package loftspacedomain is the loftspace-domain Capability Package. It adds
// the LoftSpace listing economics — the leasable facets of a unit — on top of
// location-domain's place graph, WITHOUT introducing a vertex type.
//
// location-domain owns vtx.unit.<id> (class=location): the physical place, with
// containment via containedIn, but root data {} and no economics. A unit is
// minted by location-domain's CreateLocation(locationType=unit). This package
// depends on location-domain and contributes two aspects on that same unit:
//
//	vtx.unit.<id>.listing  = {rentAmount, rentCurrency, bedrooms, bathrooms?,
//	                          sqft?, availableFrom, leaseTermMonths, status}
//	vtx.unit.<id>.address  = {line1, line2?, city, region, postal}
//
// Three ops write them:
//
//	SetListing       — publishes / updates the .listing aspect
//	SetUnitAddress   — records / updates the .address aspect
//	SetListingStatus — status-only transition of the .listing aspect (preserves
//	                   the economics); the directOp a lease application's
//	                   convergence target dispatches to mark a unit leased on approval
//
// The op scripts live on a single vertexType DDL (loftspaceListing); the listing
// and address aspect-type DDLs are step-6 write gates (the Processor keys
// permittedCommands on the mutation document's class), mirroring
// orchestration-base's freshnessMarker/freshnessExpiry split. Both aspects are
// non-sensitive: they attach to a unit, not an identity, so step-6's
// sensitiveAspectScope does not fire. Applicant income / employment (the
// sensitive data) lives on the identity, not here.
//
// This is the foundation an applicant FE renders: "what am I applying to lease"
// becomes answerable. lease-signing's CreateLeaseApplication walks an
// appliesToUnit link to this unit (a later increment).
//
// Install via `lattice-pkg install packages/loftspace-domain` AFTER
// location-domain. See docs/components/_packages.md.
package loftspacedomain

import "github.com/asolgan/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:        "loftspace-domain",
	Version:     "0.3.0",
	Description: "LoftSpace listing economics: the .listing + .address aspects on a location unit, written by SetListing / SetUnitAddress / SetListingStatus. Depends on location-domain; introduces no vertex type.",
	Depends:     []string{"location-domain"},
	DDLs:        DDLs(),
	Lenses:      Lenses(),
	Permissions: Permissions(),
}
