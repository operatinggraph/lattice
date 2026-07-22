// Package servicelocation is the service-location Capability Package — the
// residence-based service-access authorization scheme (the cap.svc grant
// source). It composes location-domain (the place graph) and service-domain
// (the service catalog) into a thin access scheme; it owns no vertex types of
// its own, only the links that wire the topology and the lens that projects it.
//
// One DDL (`serviceLocation`) handles the ten link ops:
//
//	WireResidesIn / UnwireResidesIn               # identity → location
//	WireWorksAt / UnwireWorksAt                   # identity → location
//	WireAvailableAt / UnwireAvailableAt           # service-template → location
//	WireUnavailableAt / UnwireUnavailableAt       # service-template → location
//	WirePermitsOperation / UnwirePermitsOperation # service → op-meta
//
// Direction follows Contract #1 §1.1 (later-arriving vertex is the SOURCE) and
// reads as a sentence ("identity residesIn location", "identity worksAt
// location", "service availableAt location"). Each Wire op validates its
// endpoint classes at the op — residesIn / worksAt target is a location;
// availableAt / unavailableAt source is a service TEMPLATE and target is a
// location; permitsOperation source is a service and target is an op-meta
// vertex.
//
// residesIn and worksAt are the two identity spines, and they are read very
// differently. residesIn is authorization-bearing: it is the left edge of the
// capabilityServiceAccess join, so wiring it grants service access. worksAt is
// pure topology — it says where a staff actor's world composes from and where
// their workplace-anchored read grants derive, and it is deliberately absent
// from that join. Staff authority is role grants (cap.roles), never a
// consequence of where someone works.
//
// It ships one lens — `capabilityServiceAccess` (actorAggregate) — projecting
// the disjoint key cap.svc.<actor-suffix> in the shared capability-kv bucket:
// the services reachable from the actor's residence→containment chain that are
// availableAt a reachable location (with unavailableAt exclusions), and the
// operations each permits (Contract #6 §6.5 / §6.10). The core service auth
// path reads cap.svc.<actor> via the re-pointed serviceKeyFromActor derivation
// (internal/processor), so the location grant unions into authorization
// alongside cap.roles.* (rbac-domain) and cap.ephemeral.* (orchestration-base).
//
// Depends on location-domain (the location vertices + containedIn) and
// service-domain (the service templates + instanceOf). Install via the
// InstallPackage kernel op. See docs/components/_packages.md.
package servicelocation

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:        "service-location",
	Version:     "0.5.0",
	Description: "Residence-based service-access scheme: residesIn/worksAt/availableAt/unavailableAt/permitsOperation links + the capabilityServiceAccess lens projecting cap.svc.<actor>.",
	Depends:     []string{"location-domain", "service-domain"},
	DDLs:        DDLs(),
	Lenses:      Lenses(),
	Permissions: Permissions(),
}
