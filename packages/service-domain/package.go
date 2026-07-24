// Package servicedomain is the service-domain Capability Package — the
// instance/template foundation the Loftspace lease-application reference
// vertical converges over.
//
// It declares:
//
//   - The `service` DDL defining the generic service vertex type + its
//     lifecycle + wiring ops (RequestService is the edge-manifest Fire 1
//     consumer-invocable service-path counterpart to CreateServiceInstance;
//     WireProvidedBy wires an EXISTING live template to a provider entity —
//     see below). A service vertex is either a TEMPLATE (an offering) or
//     an INSTANCE (a run of an offering), discriminated by the vertex ENVELOPE
//     class (`service.<x>.template` / `service.<x>.instance` — P7, no `.class`
//     shadow aspect); the service family `<x>` ∈ {backgroundCheck, payment,
//     laundry, fitness} (edge-showcase-app-design.md §7.3 — widened honestly
//     for the showcase dataset). RetireServiceTemplate (§7.3) is admin-only
//     cleanup that soft-deletes a template that no longer belongs.
//     Root data is minimal ({}); relationships are LINKS:
//
//     lnk.service.<tplId>.providedBy.<provType>.<provId>     # offering's provider
//     lnk.service.<tplId>.instanceOf.meta.<serviceDDLId>     # template → the service DDL meta (write-gate type authority)
//     lnk.service.<instId>.instanceOf.service.<tplId>        # run → its template (chains to the DDL meta)
//     lnk.service.<instId>.providedTo.identity.<applicantId> # run → the applicant
//     lnk.serviceprovider.<id>.identifiedBy.identity.<id>    # provider-archetype login binding
//
//     The fine-grained envelope class misses the exact class→DDL lookup, so the
//     step-6 write-gate resolver walks the instanceOf chain
//     (instance → template → service DDL meta) to the type authority
//     (Contract #1 §1.5; exactly one instanceOf per vertex keeps it unambiguous).
//
//     The availableAt availability assertion (template → location) is owned by
//     the service-location package, not this DDL.
//
//     A run records its external-call OUTCOME (status + completedAt) as an
//     `.outcome` aspect on the instance vertex (D5 — descriptive business
//     data in aspects, root minimal); no outcome aspect exists until
//     RecordServiceOutcome writes it (absence = not-yet-complete).
//
//   - The `serviceprovider` DDL + its two guard aspect-type DDLs — a lean,
//     generic provider-archetype entity template-attached vendors (e.g. a
//     laundry operator) bind their login to (persona-worlds-design.md Fire
//     W0 F3: per-domain provider entities, not one shared type).
//     BindServiceProviderIdentity mirrors clinic-domain's
//     BindProviderIdentity verbatim: an identifiedBy link + CreateOnly
//     mutual-exclusivity guards on both sides + an idempotent holdsRole
//     grant of the identity-domain `provider` role.
//
//   - Permissions granting most ops to `operator` (scope: any) — the
//     vertical's installer/orchestrator submits them. RequestService
//     carries NO permission grant: it authorizes structurally via
//     authContext.service against the cap.svc availability grant
//     (service-location's lens), never a standing role. RecordServiceOutcome
//     additionally grants `provider`, confined in-script to a bound
//     serviceprovider's OWN provided templates.
//
//   - Op-metas making CreateServiceInstance, RecordServiceOutcome, and
//     RequestService `forOperation`-resolvable. RequestService's and
//     RecordServiceOutcome's op-metas also carry the descriptor-vocabulary
//     aspects (presentation/inputSchema/dispatch) an edge client renders +
//     submits them from with no hardcoded knowledge of this package
//     (edge-showcase-app-design.md §3.3).
//
// It declares NO lens: the serviceAccess / cap.svc read-path auth plane and the
// availableAt availability assertion it walks are owned by the service-location
// package. The convergence lens that reads an instance's outcome across the
// providedTo link is a separate downstream concern.
//
// Depends on identity-domain (the providedTo link points at an identity) and
// orchestration-base (the demo's task/loom substrate). Install via the
// InstallPackage kernel op. See docs/components/_packages.md.
package servicedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:        "service-domain",
	Version:     "0.9.0",
	Description: "Service template + instance vertex type (service DDL + lifecycle ops incl. RequestService, RetireServiceTemplate, WireProvidedBy) plus the lean serviceprovider DDL (the provider-archetype binding, persona-worlds-design.md Fire W0); the instance records its external-call outcome as aspects (D5). RecordServiceOutcome grants operator + a bound serviceprovider (confined in-script to templates they provide). No read-path lens (Phase-3 deferred).",
	Depends:     []string{"identity-domain", "orchestration-base"},
	DDLs:        DDLs(),
	Permissions: Permissions(),
	OpMetas:     OpMetas(),
}
