// Package onebill is the Café vertical's Increment 3 — the "one-bill"
// composition lens, since extended to clinic + wellness. It owns no vertex
// types, links, or permissions of its own: purely four Lens declarations
// (Lenses()) that re-project loftspace-ledger's, cafe-ledger's,
// clinic-ledger's and wellness-ledger's already-posted transactions, each
// tagged by source, into one shared read model (HistoryBucket) keyed by
// leaseAppKey — so a resident's café, clinic and wellness charges all land on
// the same combined statement as their rent charges/payments.
//
// The cypher engine has no UNION (see lenses.go), so this is four Lenses
// sharing one bucket, mirroring the rbac-domain (cap.roles.*) /
// service-location (cap.svc.*) precedent of independent lenses composing
// into a shared bucket with disjoint keys.
//
// Depends on loftspace-ledger, cafe-ledger, clinic-ledger and wellness-ledger
// for the vertex/link classes its lenses match — declared for
// install-order/documentation honesty, though the cypher engine itself
// matches by class label at read time regardless (a stack running only some
// of the four ledgers simply sees the other lenses' side project zero rows,
// not an error). Install via the InstallPackage kernel op, after all four
// ledger packages. See docs/components/_packages.md.
package onebill

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:        "one-bill",
	Version:     "0.4.0",
	Description: "Combined-statement lens: loftspace-ledger + cafe-ledger + clinic-ledger + wellness-ledger transactions, tagged by source, into one leaseAppKey-keyed read model.",
	Depends:     []string{"loftspace-ledger", "cafe-ledger", "clinic-ledger", "wellness-ledger"},
	Lenses:      Lenses(),
}
