// Package frontdesk is the Café/Wellness "mixed-use composition surfaces"
// Increment 1 — the front-desk unified resident context. It owns no vertex
// types, links, or permissions of its own: a single Lens declaration
// (Lenses()) that re-projects wellness-domain's residentRate-linked
// bookings, keyed by leaseAppKey, into front-desk-bookings — the "beat that
// exists only because the packages share one graph" the backlog item names
// (a resident's booked class surfaced right next to their café tab, without
// asking).
//
// The café half of the unified context (open tabs) needs no re-projection:
// cafe-domain's own cafeTabSettlement convergence lens already serves it
// keyed by leaseAppKey, so the FE joins the two client-side, mirroring
// wellness-domain's own deliberately-uncounted bookedCount idiom.
//
// Depends on wellness-domain for the vertex/link classes its lens matches —
// declared for install-order/documentation honesty, though the cypher
// engine itself matches by class label at read time regardless (installing
// before wellness-domain just means the lens projects zero rows, not an
// error, the same one-bill precedent). Install via the InstallPackage
// kernel op. See docs/components/_packages.md.
package frontdesk

import "github.com/asolgan/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:        "front-desk",
	Version:     "0.1.0",
	Description: "Café/Wellness mixed-use composition Inc 1 — front-desk unified resident context: wellness-domain's resident-rate bookings re-projected keyed by leaseAppKey, joined client-side with cafe-domain's open tabs.",
	Depends:     []string{"wellness-domain"},
	Lenses:      Lenses(),
}
