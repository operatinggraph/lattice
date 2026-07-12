// Package edgemanifest is the edge-manifest Capability Package
// (edge-showcase-app-design.md §3, Fire 1) — the world manifest the Facet
// edge app renders from. It declares no DDLs and no permissions; it is
// purely five Personal Lenses (Lenses()) that re-project data other
// packages already own (identity, orchestration-base, service-domain,
// service-location) into the reserved `manifest.` key namespace, delivered
// per-actor over the shared SYNC nats-subject transport
// (edge-manifest Fire 0).
//
// Install via the InstallPackage kernel op. See docs/components/_packages.md
// and docs/components/edge-manifest.md (the vocabulary spec).
package edgemanifest

import "github.com/asolgan/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:        "edge-manifest",
	Version:     "0.1.0",
	Description: "The Facet edge app's world manifest: five Personal Lenses (edgeIdentity/edgeServices/edgeCatalog/edgeTasks/edgeInstances) projecting identity, reachable services, the op descriptor vocabulary, open tasks, and service instances into the manifest.* namespace over the per-actor SYNC transport.",
	Depends:     []string{"identity-domain", "orchestration-base", "service-domain", "service-location"},
	Lenses:      Lenses(),
}
