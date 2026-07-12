// Package servicedomain is the service-domain Capability Package — the
// instance/template foundation the Loftspace lease-application reference
// vertical converges over.
//
// It declares:
//
//   - One DDL (`service`) defining the generic service vertex type + four
//     lifecycle ops (the fourth, RequestService, is the edge-manifest Fire 1
//     consumer-invocable service-path counterpart to CreateServiceInstance —
//     see below). A service vertex is either a TEMPLATE (an offering) or
//     an INSTANCE (a run of an offering), discriminated by the vertex ENVELOPE
//     class (`service.<x>.template` / `service.<x>.instance` — P7, no `.class`
//     shadow aspect); the service family `<x>` ∈ {backgroundCheck, payment}.
//     Root data is minimal ({}); relationships are LINKS:
//
//     lnk.service.<tplId>.providedBy.<provType>.<provId>     # offering's provider
//     lnk.service.<tplId>.instanceOf.meta.<serviceDDLId>     # template → the service DDL meta (write-gate type authority)
//     lnk.service.<instId>.instanceOf.service.<tplId>        # run → its template (chains to the DDL meta)
//     lnk.service.<instId>.providedTo.identity.<applicantId> # run → the applicant
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
//   - Permissions granting three of the four lifecycle ops to `operator`
//     (scope: any) — the vertical's installer/orchestrator submits them.
//     RequestService carries NO permission grant: it authorizes structurally
//     via authContext.service against the cap.svc availability grant
//     (service-location's lens), never a standing role.
//
//   - Op-metas making CreateServiceInstance, RecordServiceOutcome, and
//     RequestService `forOperation`-resolvable. RequestService's op-meta also
//     carries the descriptor-vocabulary aspects (presentation/inputSchema/
//     dispatch) an edge client renders + submits it from with no hardcoded
//     knowledge of this package (edge-showcase-app-design.md §3.3).
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

import "github.com/asolgan/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:        "service-domain",
	Version:     "0.3.0",
	Description: "Service template + instance vertex type (service DDL + lifecycle ops incl. RequestService); the instance records its external-call outcome as aspects (D5). No read-path lens (Phase-3 deferred).",
	Depends:     []string{"identity-domain", "orchestration-base"},
	DDLs:        DDLs(),
	Permissions: Permissions(),
	OpMetas:     OpMetas(),
}
