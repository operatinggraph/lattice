// Package pkgregistry is the single enumeration of the shipped packages:
// manifest name -> compiled Go Definition.
//
// Phase 1 has no package discovery — a package is a Go Definition compiled into
// the binary that installs it — so the corpus has to be listed somewhere. It is
// listed HERE, once. Every consumer (cmd/lattice-pkg's installer CLI, Loupe's
// package endpoints, the `lint-package-standard` gate) reads this map, so a new
// package becomes visible to all of them the moment it is added, and the gate
// cannot be escaped by a package that simply never got registered.
package pkgregistry

import (
	"sort"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	augur "github.com/operatinggraph/lattice/packages/augur"
	cafedomain "github.com/operatinggraph/lattice/packages/cafe-domain"
	cafeledger "github.com/operatinggraph/lattice/packages/cafe-ledger"
	capabilityauthor "github.com/operatinggraph/lattice/packages/capability-author"
	clinicdomain "github.com/operatinggraph/lattice/packages/clinic-domain"
	clinicledger "github.com/operatinggraph/lattice/packages/clinic-ledger"
	clinicreminders "github.com/operatinggraph/lattice/packages/clinic-reminders"
	consoleoperator "github.com/operatinggraph/lattice/packages/console-operator"
	controlauthz "github.com/operatinggraph/lattice/packages/control-authz"
	demooperator "github.com/operatinggraph/lattice/packages/demo-operator"
	edgemanifest "github.com/operatinggraph/lattice/packages/edge-manifest"
	frontdesk "github.com/operatinggraph/lattice/packages/front-desk"
	identitydomain "github.com/operatinggraph/lattice/packages/identity-domain"
	identityhygiene "github.com/operatinggraph/lattice/packages/identity-hygiene"
	leasesigning "github.com/operatinggraph/lattice/packages/lease-signing"
	locationdomain "github.com/operatinggraph/lattice/packages/location-domain"
	loftspacedomain "github.com/operatinggraph/lattice/packages/loftspace-domain"
	loftspaceledger "github.com/operatinggraph/lattice/packages/loftspace-ledger"
	maintenancedomain "github.com/operatinggraph/lattice/packages/maintenance-domain"
	objectsbase "github.com/operatinggraph/lattice/packages/objects-base"
	onebill "github.com/operatinggraph/lattice/packages/one-bill"
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
	privacybase "github.com/operatinggraph/lattice/packages/privacy-base"
	privacyoperatorgrant "github.com/operatinggraph/lattice/packages/privacy-operator-grant"
	rbacdomain "github.com/operatinggraph/lattice/packages/rbac-domain"
	semanticcontracts "github.com/operatinggraph/lattice/packages/semantic-contracts"
	servicedomain "github.com/operatinggraph/lattice/packages/service-domain"
	servicelocation "github.com/operatinggraph/lattice/packages/service-location"
	wellnessdomain "github.com/operatinggraph/lattice/packages/wellness-domain"
)

// registry maps a package's manifest name — which is also its directory name
// under packages/ — to its Definition.
var registry = map[string]pkgmgr.Definition{
	"rbac-domain":            rbacdomain.Package,
	"identity-domain":        identitydomain.Package,
	"identity-hygiene":       identityhygiene.Package,
	"orchestration-base":     orchestrationbase.Package,
	"service-domain":         servicedomain.Package,
	"location-domain":        locationdomain.Package,
	"loftspace-domain":       loftspacedomain.Package,
	"clinic-domain":          clinicdomain.Package,
	"clinic-ledger":          clinicledger.Package,
	"clinic-reminders":       clinicreminders.Package,
	"service-location":       servicelocation.Package,
	"edge-manifest":          edgemanifest.Package,
	"lease-signing":          leasesigning.Package,
	"loftspace-ledger":       loftspaceledger.Package,
	"cafe-ledger":            cafeledger.Package,
	"cafe-domain":            cafedomain.Package,
	"one-bill":               onebill.Package,
	"front-desk":             frontdesk.Package,
	"objects-base":           objectsbase.Package,
	"augur":                  augur.Package,
	"capability-author":      capabilityauthor.Package,
	"privacy-base":           privacybase.Package,
	"privacy-operator-grant": privacyoperatorgrant.Package,
	"semantic-contracts":     semanticcontracts.Package,
	"control-authz":          controlauthz.Package,
	"console-operator":       consoleoperator.Package,
	"demo-operator":          demooperator.Package,
	"wellness-domain":        wellnessdomain.Package,
	"maintenance-domain":     maintenancedomain.Package,
}

// Lookup returns the Definition registered under a manifest name.
func Lookup(name string) (pkgmgr.Definition, bool) {
	d, ok := registry[name]
	return d, ok
}

// Names returns every registered package name, sorted, so a caller iterating
// the corpus reports in a stable order.
func Names() []string {
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// All returns a copy of the registry, for a caller that needs to range over
// name/Definition pairs.
func All() map[string]pkgmgr.Definition {
	out := make(map[string]pkgmgr.Definition, len(registry))
	for name, def := range registry {
		out[name] = def
	}
	return out
}
