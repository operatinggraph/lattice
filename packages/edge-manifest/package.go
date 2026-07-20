// Package edgemanifest is the edge-manifest Capability Package
// (edge-showcase-app-design.md §3, Fire 1) — the world manifest the Facet
// edge app renders from. It declares no DDLs and no permissions; it is
// purely Personal Lenses (Lenses()) that re-project data other
// packages already own (identity, orchestration-base, service-domain,
// service-location, wellness-domain, clinic-domain) into the reserved
// `manifest.` key namespace, delivered
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
	Version:     "0.7.0",
	Description: "The Facet edge app's world manifest: ten Personal Lenses (edgeIdentity/edgeServices/edgeCatalog/edgeCatalogRoles/edgeTasks/edgeTasksQueued/edgeInstances/edgeEntitySessions/edgeEntityProviders/edgeEntityBookings) projecting identity, reachable services, the op descriptor vocabulary, open and queued tasks, service instances, and browsable dispatch-target entities (manifest.ent rows a declared dispatch.targetType resolves against) into the manifest.* namespace over the per-actor SYNC transport. Plus edgeManifestReadGrants and edgeManifestStaffReadGrants, the cap-read.edgeManifest read-grant producers the non-self-anchored lenses need to actually publish (Fire 2).",
	Depends:     []string{"identity-domain", "orchestration-base", "service-domain", "service-location"},
	Lenses:      Lenses(),
}
