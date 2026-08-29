// Package edgemanifest is the edge-manifest Capability Package
// (edge-showcase-app-design.md §3, Fire 1) — the world manifest the Facet
// edge app renders from. It declares no DDLs and no permissions; it is
// almost entirely Personal Lenses (Lenses()) that re-project data other
// packages already own (identity, orchestration-base, service-domain,
// service-location, wellness-domain, clinic-domain) into the reserved
// `manifest.` key namespace, delivered
// per-actor over the shared SYNC nats-subject transport
// (edge-manifest Fire 0).
//
// The one exception is `opCatalog`, a plain nats-kv lens: the same op
// descriptor vocabulary, projected globally into an open read-model bucket so
// a STAFF application (which is not an edge node and holds no per-actor SYNC
// transport) can render an operation's form from the package's own
// declaration — staff-descriptor-rendering-design.md §2.1.
//
// Install via the InstallPackage kernel op. See docs/components/_packages.md
// and docs/components/edge-manifest.md (the vocabulary spec).
package edgemanifest

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:             "edge-manifest",
	Version:          "0.17.8",
	Description:      "The Facet edge app's world manifest: fifteen Personal Lenses (edgeIdentity/edgeServices/edgeCatalog/edgeTasks/edgeInstances/edgeEntitySessions/edgeEntityProviders/edgeEntityBookings/edgeEntityTabs/edgeEntityStudios/edgeEntityMenuItems/edgeStaffPanes/edgeStaffWorkOrders/edgeProviderSchedule/edgeProviderQueue — edgeEntitySessions and edgeTasks each carry a second Walk for their role-derived reachability path, folding the former edgeInstructorSessions and edgeTasksQueued siblings in; edgeCatalog carries a second Walk the same way, folding the former edgeCatalogRoles sibling in, plus a third reaching op-metas through the actor's own assigned tasks) projecting identity (incl. the provider/instructor/serviceprovider self-anchors for `{me.<type>}` dispatch, and the same bindings relation-stamped and named in `anchors` so the renderer can group a person's hats by provenance — persona-worlds-design.md Fires W0/W5), reachable services, the op descriptor vocabulary, open and queued tasks, the maintenance work orders at a staff actor's workplace, the server-pane descriptors a held role is offeredTo (manifest.pane rows the host's generic pane executor and the renderer both consume), service instances, and browsable dispatch-target entities (manifest.ent rows a declared dispatch.targetType resolves against) — incl. the actor's own open café tabs off their lease, the café catalog items served where they live, the wellness studios at a staff actor's workplace, and the provider-hat rows: a bound provider's own appointments, a bound serviceprovider's own instance queue, a bound instructor's own led sessions — into the manifest.* namespace over the per-actor SYNC transport. Each non-self-anchored lens declares its actor\u2192anchor reachability once, as an AnchorWalk, and pkgmgr compiles both the lens's own cypher and the read-grant producer that grants its anchors: three generated producers (edgeManifestReadGrants, edgeManifestStaffReadGrants, edgeManifestProviderReadGrants), one per declared ReadGrantDomain, without which Refractor's D1 readableAnchors gate silently drops every row those lenses project. Alongside them one PLAIN (nats-kv) lens, opCatalog: the op-catalog read model a staff application renders operation forms from — one open row per op meta, keyed by operationType, carrying the whole descriptor vocabulary plus the roles that grant it (staff-descriptor-rendering-design.md §2.1).",
	Depends:          []string{"identity-domain", "orchestration-base", "service-domain", "service-location"},
	Lenses:           Lenses(),
	Panes:            Panes(),
	ReadGrantDomains: ReadGrantDomains(),
}
