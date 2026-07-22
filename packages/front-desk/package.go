// Package frontdesk is the Café/Wellness/Clinic "mixed-use composition
// surfaces" Increment 1 (+ the Inc 4 lease-details tail, + the Inc 5 clinic
// tail) — the front-desk unified resident context. It owns no vertex types,
// links, or permissions of its own: three Lens declarations (Lenses()) —
// frontDeskBookings re-projects wellness-domain's residentRate-linked
// bookings, keyed by leaseAppKey, into front-desk-bookings (a resident's
// booked class surfaced right next to their café tab, without asking);
// frontDeskLeaseDetails re-projects every leaseapp's applied-to unit
// rent/term, keyed by leaseAppKey, into front-desk-lease-details (the lease
// details — term/rent — on every open-tab card, not just those with a booked
// class); frontDeskVisits re-projects clinic-domain's residentVisit-linked
// appointments, keyed by leaseAppKey, into front-desk-visits (existence +
// time of a resident's own upcoming clinic visit — never the visit reason or
// clinical content).
//
// The café half of the unified context (open tabs) needs no re-projection:
// cafe-domain's own cafeTabSettlement convergence lens already serves it
// keyed by leaseAppKey, so the FE joins all three client-side, mirroring
// wellness-domain's own deliberately-uncounted bookedCount idiom.
//
// Depends on wellness-domain + clinic-domain for the vertex/link classes its
// lenses match — declared for install-order/documentation honesty, though
// the cypher engine itself matches by class label at read time regardless
// (installing before either just means that lens projects zero rows, not an
// error, the same one-bill precedent). frontDeskLeaseDetails matches
// leaseapp and unit the same way, without adding lease-signing/
// loftspace-domain to Depends — consistent with frontDeskBookings' own
// leaseapp match above.
// Install via the InstallPackage kernel op. See docs/components/_packages.md.
package frontdesk

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:        "front-desk",
	Version:     "0.3.0",
	Description: "Café/Wellness/Clinic mixed-use composition Inc 1 + Inc 4 + Inc 5 — front-desk unified resident context: wellness-domain's resident-rate bookings, every leaseapp's unit rent/term, and clinic-domain's resident-confined visits (existence + time only), all re-projected keyed by leaseAppKey, joined client-side with cafe-domain's open tabs.",
	Depends:     []string{"wellness-domain", "clinic-domain"},
	Lenses:      Lenses(),
}
