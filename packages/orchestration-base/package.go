// Package orchestrationbase is the orchestration-base Capability Package —
// the foundational Phase 2 package that stands up the generic task
// substrate and re-sources FR56 ephemeral task grants out of the bootstrap
// god-cypher into a package-owned lens.
//
// It declares:
//
//   - A `loomLifecycle` DDL defining the three EVENT-ONLY Loom lifecycle ops
//     (Contract #10 §10.9): StartLoomPattern (→ loom.patternStarted),
//     CompletePattern (→ loom.patternCompleted), FailPattern
//     (→ loom.patternFailed). They produce NO business mutation; the Loom
//     instance is operational-only (loom-state, no Core-KV vertex).
//
//   - One DDL (`task`) defining the generic task vertex type + the
//     CreateTask/ClaimTask/ReAssignTask/CompleteTask/CancelTask operations.
//     Task root data is scalars only ({status, expiresAt}); relationships
//     are LINKS:
//
//     lnk.task.<id>.assignedTo.identity.<assigneeId>   # direct push assignment
//     lnk.task.<id>.queuedFor.role.<roleId>            # FR28 role-queue pull assignment
//     lnk.task.<id>.forOperation.meta.<opId>           # the op this task grants
//     lnk.task.<id>.scopedTo.<type>.<targetId>         # the grant's target
//
//     Exactly one of assignedTo/queuedFor is present on an open task.
//     ClaimTask atomically swaps queuedFor→assignedTo(claimant).
//
//   - Two Lenses (`capabilityEphemeral`, `myTasks`) projecting per-actor
//     ephemeral grants + the task inbox to the disjoint keys
//     `cap.ephemeral.<actor-suffix>` / `my-tasks.<actor-suffix>` (Contract
//     #6 §6.6 Phase-2 amendment, Contract #10 §10.7). Both are
//     LINK-SOURCED: they walk assignedTo/forOperation/scopedTo (+ reportsTo
//     2-hop for manager delegation, + holdsRole/queuedFor for FR28
//     role-queue fan-out) — NOT the old task.data.grantedOperationType/
//     targetKey fields (the corrected anti-pattern, Contract #10 §10.1).
//
//   - Permissions granting the task ops + the three Loom lifecycle ops to
//     `operator` (all scope: any).
//
// Install via the InstallPackage kernel op. See
// docs/components/_packages.md.
package orchestrationbase

import "github.com/asolgan/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:          "orchestration-base",
	Version:       "0.5.0",
	Description:   "Generic task substrate (task DDL + CreateTask/ClaimTask/SetAvailability, FR28 role-queue + fallback + Fire-2 availability routing) + package-owned capabilityEphemeral/myTasks lenses (FR56 grant re-sourcing + role-queue fan-out) + the FR29 unroutedTasks Weaver convergence target (surface-only).",
	Depends:       []string{"identity-domain"},
	DDLs:          DDLs(),
	Lenses:        Lenses(),
	Permissions:   Permissions(),
	WeaverTargets: WeaverTargets(),
}
