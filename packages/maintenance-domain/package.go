// Package maintenancedomain is the maintenance-domain Capability Package —
// the cross-vertical operations domain: a work order raised against a place,
// queued to a maintenance role, resolved by whoever claims it or by the
// landlord of the unit it is at, and reported back to whoever raised it.
//
//	vtx.workorder.<id>  class=workorder  root {}   .report               {summary, priority, reportedAt, reportedBy}
//	                                               .resolution           {notes, resolvedAt, resolvedBy}
//	                                               .resolvedNotice       {resolvedFor, sentAt}
//	                                               .resolvedNotification {status, sentAt}
//	lnk.workorder.<id>.locatedAt.<locType>.<locId>   (workorder → location, later-arriving source)
//	lnk.workorder.<id>.reportedBy.identity.<id>      (workorder → reporter, later-arriving source)
//
// Two human ops, and they are deliberately the two HALVES of the FR28
// role-queue beat rather than a lifecycle the package drives itself:
//
//   - ReportIssue mints the work order at a location, stamps and LINKS its
//     reporter. It does NOT mint the task — tasks are orchestration-base's,
//     and Contract #10 §10.1 owns the exactly-one-of assignedTo/queuedFor
//     invariant. A work order becomes WORK when CreateTask(queue: <role>,
//     forOperation: <ResolveWorkOrder's op-meta>, scopedTo: <the work order>)
//     is submitted — and the package carries WHO submits it as a declarative
//     convergence target rather than a step in the op: the workOrderQueue
//     lens (lenses.go) projects missing_task on every unresolved work order
//     with no open task, and its weaverTarget (targets.go) queues
//     ResolveWorkOrder to backOfHouse through Weaver's assignTask queue arm.
//     That separation is what lets the same work order be queued to a
//     different role, or reassigned, without the op knowing anything about
//     it. The same lens backfills the reportedBy link on an order that
//     predates it (missing_reporter → LinkWorkOrderReporter).
//   - ResolveWorkOrder writes the .resolution aspect. It is the op the queued
//     task GRANTS: the claimant performs it under authContext.task and the
//     Processor's §10.6 auto-complete closes the task on the same commit, so
//     no separate "complete" op exists or should. It is also the op a
//     landlord reaches on a scope=self grant, bound to the unit they manage;
//     a task left open by that route (or by an operator's standing resolve)
//     is cancelled by the staleWorkOrderTasks target rather than by the op.
//
// Three orchestration-internal ops close the loop toward the reporter:
// LinkWorkOrderReporter (the backfill above), RecordWorkOrderResolvedNotice
// (the workOrderResolvedNotices target tells the reporter once, keyed on the
// resolution's own stamp, that someone else resolved their order — nobody is
// told what they resolved themselves) and RecordWorkOrderResolvedNotification
// (the bridge's replyOp recording the send's outcome). reporterWorkOrdersRead
// is the protected Postgres read model a reporter's own card lists —
// vertical-neutral, anchored on the reporter alone.
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
// worksAt-cover DENIES rather than escalates. Both human ops also carry a
// consumer scope=self leg the staff guard cannot see: a resident reports an
// issue at the unit they reside in (require_residence, the GiveNotice
// tenant-hat posture), and a landlord resolves an order at the unit they
// manage (require_manages_unit, lease-signing's require_manages posture). An
// authContext.target naming the caller selects that bind — whichever grant
// row authorized the call, so a staff-resident dual hat lands on the bind
// the card asked for — and the caller's own residesIn / manages link to the
// unit, declared by the dispatcher, is what admits the write.
//
// The queued task carries the role and the work order, not a location: any
// backOfHouse holder anywhere may claim and resolve it (ClaimTask checks
// holdsRole only; ResolveWorkOrder's task path skips the worksAt walk). A
// location-scoped queue is a filed platform primitive.
//
// Depends location-domain (the location vertices ReportIssue validates its
// `location` against, read by known key) and orchestration-base (the package
// whose CreateTask queues a work order, whose ClaimTask hands it to a
// claimant, and whose CancelTask the staleWorkOrderTasks target dispatches by
// its "task" DDL class — a declared dependency, as lease-signing and
// clinic-reminders declare it for the same dispatch).
//
// Install via `lattice-pkg install packages/maintenance-domain`.
// See _bmad-output/implementation-artifacts/facet-staff-worlds-design.md §6 F5.
package maintenancedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:          "maintenance-domain",
	Version:       "0.4.0",
	Description:   "Cross-vertical maintenance work orders: vtx.workorder.<NanoID> raised at a location by ReportIssue (which also links the order to its reporter) and closed by ResolveWorkOrder — the op an FR28 role-queued task grants its claimant (the §10.6 auto-complete closes the task, so no separate completion op exists), and the op a landlord reaches on a scope=self grant bound to the unit they manage. `.resolution` is the read-before-write terminal marker — an identical re-submit is an idempotent no-op so an offline device's drain retry cannot fail the work, a differing one is rejected so a resolution never silently flips. Both ops carry F4's canonical workplace write-confinement guard on the standing path; ReportIssue additionally admits a consumer on a scope=self grant bound to the unit they residesIn. Three convergence targets close the loop: the workOrderQueue lens + weaverTarget queue every unresolved, untasked work order as a ResolveWorkOrder task to backOfHouse and backfill a missing reporter link (LinkWorkOrderReporter); staleWorkOrderTasks cancels an open task whose order was resolved by a non-task route; workOrderResolvedNotices tells the reporter once that someone else resolved their order (RecordWorkOrderResolvedNotice, its outcome recorded by the bridge's RecordWorkOrderResolvedNotification replyOp). reporterWorkOrdersRead is the protected Postgres read model of a reporter's own orders. ResolveWorkOrder carries an op-meta with the full edge-manifest descriptor vocabulary (presentation/inputSchema/dispatch authContext=task) so a Facet client can render and submit it from the task row alone.",
	Depends:       []string{"location-domain", "orchestration-base"},
	DDLs:          DDLs(),
	Lenses:        Lenses(),
	Permissions:   Permissions(),
	OpMetas:       OpMetas(),
	WeaverTargets: WeaverTargets(),
}
