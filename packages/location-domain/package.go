// Package locationdomain is the location-domain Capability Package. It owns
// the spatial base domain — the place graph — mirroring how identity-domain
// owns the identity base domain.
//
// Four DDLs: the ABSTRACT `location` type and its three CONCRETE leaves
// (`unit`, `building`, `property`), each joined to it by a `subtypeOf` link.
// The three leaves share one script and each admits all five operations:
//
//	CreateLocation, TombstoneLocation
//	WireContainedIn, UnwireContainedIn
//	SetLocationPresentation
//
// A location is one of the three concrete vertex types, chosen by the
// `locationType` op parameter (Contract #6 §6.9); the class equals the key
// type:
//
//	vtx.unit.<id>      class=unit
//	vtx.building.<id>  class=building
//	vtx.property.<id>  class=property
//
// Root data is minimal `{}` (D5 — business data lives in aspects). The abstract
// `location` type names no instance: it exists so one lens label —
// `(l:location*)` — expands against the subtypeOf links into the concrete leaf
// set, and so a write-path guard can say "any location" by asking the key's
// type segment rather than hardcoding the three levels in every package.
//
// Because all three leaves admit the same operations, the Processor cannot
// infer a class from the operationType, so every submitter names the concrete
// leaf matching the key it acts on. The abstract `location` is never a legal
// envelope class.
//
// Containment is the `containedIn` link (location → location, transitive —
// unit → building → property). Direction follows Contract #1 §1.1: the
// later-arriving vertex is the SOURCE, so the sentence reads "unit containedIn
// building" (source = the child/contained vertex, target = the parent/container):
//
//	lnk.<childType>.<childId>.containedIn.<parentType>.<parentId>
//
// WireContainedIn validates BOTH endpoints are alive AND keyed with an admitted
// location type segment before it writes the link — a non-location vertex can
// never be wired into the place graph.
//
// A location may carry an optional `.presentation` display aspect
// ({name, description?, icon?, category?}) — set at creation or via
// SetLocationPresentation. Locations are class-2 nameable business vertices
// (display-name-convention-design.md): the aspect is a mutable non-identity
// label a renderer projects instead of a bare NanoID, never PII.
//
// Install via `lattice-pkg install packages/location-domain`. See
// docs/components/_packages.md.
package locationdomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:        "location-domain",
	Version:     "0.4.0",
	Description: "Spatial base domain: the abstract `location` type with its unit/building/property concrete leaves, and the containedIn containment link.",
	Depends:     []string{},
	DDLs:        DDLs(),
	Permissions: Permissions(),
	OpMetas:     OpMetas(),
}
