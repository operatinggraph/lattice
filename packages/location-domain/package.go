// Package locationdomain is the location-domain Capability Package. It owns
// the spatial base domain — the place graph — mirroring how identity-domain
// owns the identity base domain.
//
// One DDL (`location`) handles all four operations:
//
//	CreateLocation, TombstoneLocation
//	WireContainedIn, UnwireContainedIn
//
// A location is one of three vertex types — unit, building, or property —
// discriminated by the `locationType` op parameter (Contract #6 §6.9):
//
//	vtx.unit.<id>      class=location
//	vtx.building.<id>  class=location
//	vtx.property.<id>  class=location
//
// Root data is minimal `{}` (D5 — business data lives in aspects). The shared
// `location` class is what a downstream cypher rule guards on when it walks the
// place graph; the type segment names the level.
//
// Containment is the `containedIn` link (location → location, transitive —
// unit → building → property). Direction follows Contract #1 §1.1: the
// later-arriving vertex is the SOURCE, so the sentence reads "unit containedIn
// building" (source = the child/contained vertex, target = the parent/container):
//
//	lnk.<childType>.<childId>.containedIn.<parentType>.<parentId>
//
// WireContainedIn validates BOTH endpoints are alive AND location-class before
// it writes the link — a non-location vertex can never be wired into the place
// graph.
//
// Install via `lattice-pkg install packages/location-domain`. See
// docs/components/_packages.md.
package locationdomain

import "github.com/asolgan/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:        "location-domain",
	Version:     "0.1.0",
	Description: "Spatial base domain: unit/building/property location vertices and the containedIn containment link.",
	Depends:     []string{},
	DDLs:        DDLs(),
	Permissions: Permissions(),
}
