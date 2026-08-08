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

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:             "edge-manifest",
	Version:          "0.15.7",
	Description:      "The Facet edge app's world manifest: fifteen Personal Lenses (edgeIdentity/edgeServices/edgeCatalog/edgeTasks/edgeInstances/edgeEntitySessions/edgeEntityProviders/edgeEntityBookings/edgeEntityTabs/edgeEntityStudios/edgeEntityMenuItems/edgeStaffPanes/edgeStaffWorkOrders/edgeProviderSchedule/edgeProviderQueue — edgeEntitySessions, edgeCatalog and edgeTasks each carry a second Walk for their role-derived reachability path, folding the former edgeInstructorSessions, edgeCatalogRoles and edgeTasksQueued siblings in) projecting identity (incl. the provider/instructor/serviceprovider self-anchors for `{me.<type>}` dispatch, and the same bindings relation-stamped and named in `anchors` so the renderer can group a person's hats by provenance — persona-worlds-design.md Fires W0/W5), reachable services, the op descriptor vocabulary, open and queued tasks, the maintenance work orders at a staff actor's workplace, the server-pane descriptors a held role is offeredTo (manifest.pane rows the host's generic pane executor and the renderer both consume), service instances, and browsable dispatch-target entities (manifest.ent rows a declared dispatch.targetType resolves against) — incl. the actor's own open café tabs off their lease, the café catalog items served where they live, the wellness studios at a staff actor's workplace, and the provider-hat rows: a bound provider's own appointments, a bound serviceprovider's own instance queue, a bound instructor's own led sessions — into the manifest.* namespace over the per-actor SYNC transport. Each non-self-anchored lens declares its actor\u2192anchor reachability once, as an AnchorWalk, and pkgmgr compiles both the lens's own cypher and the read-grant producer that grants its anchors: three generated producers (edgeManifestReadGrants, edgeManifestStaffReadGrants, edgeManifestProviderReadGrants), one per declared ReadGrantDomain, without which Refractor's D1 readableAnchors gate silently drops every row those lenses project.",
	Depends:          []string{"identity-domain", "orchestration-base", "service-domain", "service-location"},
	Lenses:           Lenses(),
	Panes:            Panes(),
	ReadGrantDomains: ReadGrantDomains(),
}
