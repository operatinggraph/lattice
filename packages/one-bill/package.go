// Package onebill is the Café vertical's Increment 3 — the "one-bill"
// composition lens. It owns no vertex types, links, or permissions of its
// own: purely two Lens declarations (Lenses()) that re-project
// loftspace-ledger's and cafe-ledger's already-posted transactions, each
// tagged by source, into one shared read model (HistoryBucket) keyed by
// leaseAppKey — so a resident's café charges land on the same combined
// statement as their rent charges/payments.
//
// The cypher engine has no UNION (see lenses.go), so this is two Lenses
// sharing one bucket, mirroring the rbac-domain (cap.roles.*) /
// service-location (cap.svc.*) precedent of independent lenses composing
// into a shared bucket with disjoint keys.
//
// Depends on loftspace-ledger and cafe-ledger for the vertex/link classes its
// lenses match — declared for install-order/documentation honesty, though the
// cypher engine itself matches by class label at read time regardless (a
// stack running only one of the two ledgers simply sees that lens side project
// zero rows, not an error). Install via the InstallPackage kernel op, after
// both loftspace-ledger and cafe-ledger. See docs/components/_packages.md.
package onebill

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:        "one-bill",
	Version:     "0.2.0",
	Description: "Café vertical Inc 3 — combined-statement lens: loftspace-ledger + cafe-ledger transactions, tagged by source, into one leaseAppKey-keyed read model.",
	Depends:     []string{"loftspace-ledger", "cafe-ledger"},
	Lenses:      Lenses(),
}
