// Package maintenancedomain is the maintenance-domain Capability Package —
// the cross-vertical operations domain: a work order raised against a place,
// queued to a maintenance role, and resolved by whoever claims it.
//
//	vtx.workorder.<id>  class=workorder  root {}   .report     {summary, priority, reportedAt, reportedBy}
//	                                               .resolution {notes, resolvedAt, resolvedBy}
//	lnk.workorder.<id>.locatedAt.<locType>.<locId>   (workorder → location, later-arriving source)
//
// Two ops, and they are deliberately the two HALVES of the FR28 role-queue
// beat rather than a lifecycle the package drives itself:
//
//   - ReportIssue mints the work order at a location. It does NOT mint the
//     task — tasks are orchestration-base's, and Contract #10 §10.1 owns the
//     exactly-one-of assignedTo/queuedFor invariant. A work order becomes
//     WORK when CreateTask(queue: <role>, forOperation: <ResolveWorkOrder's
//     op-meta>, scopedTo: <the work order>) is submitted — and the package
//     carries WHO submits it as a declarative convergence target rather than
//     a step in the op: the workOrderQueue lens (lenses.go) projects
//     missing_task on every unresolved work order with no open task, and its
//     weaverTarget (targets.go) queues ResolveWorkOrder to backOfHouse through
//     Weaver's assignTask queue arm. That separation is what lets the same
//     work order be queued to a different role, or reassigned, without the
//     op knowing anything about it.
//   - ResolveWorkOrder writes the .resolution aspect. It is the op the queued
//     task GRANTS: the claimant performs it under authContext.task and the
//     Processor's §10.6 auto-complete closes the task on the same commit, so
//     no separate "complete" op exists or should.
//
// Terminality mirrors lease-signing's `.decision`: `.resolution` is the
// read-before-write terminal marker, and a RE-submit carrying identical notes
// is an idempotent no-op rather than a rejection. That is not politeness — it
// is the offline consumer's requirement (facet-staff-worlds-design.md §6 F5):
// a disconnected device queues the resolve, drains on reconnect, and a drain
// that retries under a fresh requestId must not fail the tech's work. Notes
// that DIFFER from the recorded ones are rejected, so a resolution can never
// silently flip.
//
// Write confinement is F4's canonical workplace guard, byte-identical to the
// four packages that already carry it (facet-staff-worlds-design.md §6 F4),
// with one documented difference at ReportIssue — see the guard's call site in
// ddls.go: a create op has no target topology to resolve, so the reported
// location IS the subject, and naming a location the caller does not
// worksAt-cover DENIES rather than escalates. ReportIssue also carries a
// consumer scope=self leg the staff guard cannot see: a resident reports an
// issue at the unit they reside in. An authContext.target naming the caller
// selects that bind — whichever grant row authorized the call, so a
// staff-resident dual hat lands on the bind the tenant card asked for — and
// the caller's own residesIn link to the unit is what admits the write
// (require_residence), the GiveNotice tenant-hat posture.
//
// The queued task carries the role and the work order, not a location: any
// backOfHouse holder anywhere may claim and resolve it (ClaimTask checks
// holdsRole only; ResolveWorkOrder's task path skips the worksAt walk). A
// location-scoped queue is a filed platform primitive.
//
// Depends location-domain (the location vertices ReportIssue validates its
// `location` against, read by known key) and orchestration-base (documentation
// only — no Starlark-level dependency; it is the package whose CreateTask
// queues a work order and whose ClaimTask hands it to a claimant).
//
// Install via `lattice-pkg install packages/maintenance-domain`.
// See _bmad-output/implementation-artifacts/facet-staff-worlds-design.md §6 F5.
package maintenancedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:          "maintenance-domain",
	Version:       "0.3.0",
	Description:   "Cross-vertical maintenance work orders: vtx.workorder.<NanoID> raised at a location by ReportIssue and closed by ResolveWorkOrder, the op an FR28 role-queued task grants its claimant (the §10.6 auto-complete closes the task, so no separate completion op exists). `.resolution` is the read-before-write terminal marker — an identical re-submit is an idempotent no-op so an offline device's drain retry cannot fail the work, a differing one is rejected so a resolution never silently flips. Both ops carry F4's canonical workplace write-confinement guard on the standing path; ReportIssue additionally admits a consumer on a scope=self grant bound to the unit they residesIn. The workOrderQueue lens + weaverTarget queue every unresolved, untasked work order as a ResolveWorkOrder task to backOfHouse, so a reported issue becomes work without the reporter minting the task. ResolveWorkOrder carries an op-meta with the full edge-manifest descriptor vocabulary (presentation/inputSchema/dispatch authContext=task) so a Facet client can render and submit it from the task row alone.",
	Depends:       []string{"location-domain"},
	DDLs:          DDLs(),
	Lenses:        Lenses(),
	Permissions:   Permissions(),
	OpMetas:       OpMetas(),
	WeaverTargets: WeaverTargets(),
}
