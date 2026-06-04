// Package orchestrationbase is the orchestration-base Capability Package —
// the foundational Phase 2 package that stands up the generic task
// substrate and re-sources FR56 ephemeral task grants out of the bootstrap
// god-cypher into a package-owned lens.
//
// It declares:
//
//   - One DDL (`task`) defining the generic task vertex type + the
//     CreateTask operation. Task root data is scalars only
//     ({status, expiresAt}); relationships are LINKS:
//
//	lnk.task.<id>.assignedTo.identity.<assigneeId>   # who performs it
//	lnk.task.<id>.forOperation.meta.<opId>           # the op this task grants
//	lnk.task.<id>.scopedTo.<type>.<targetId>         # the grant's target
//
//   - One Lens (`capabilityEphemeral`) projecting per-actor ephemeral
//     grants to the disjoint key `cap.ephemeral.<actor-suffix>` in the
//     primordial capability-kv bucket (Contract #6 §6.6 Phase-2 amendment,
//     Contract #10 §10.7). The lens is LINK-SOURCED: it walks
//     assignedTo/forOperation/scopedTo (+ reportsTo 2-hop for manager
//     delegation) — NOT the old task.data.grantedOperationType/targetKey
//     fields (the corrected anti-pattern, Contract #10 §10.1).
//
//   - One permission (`CreateTask`, scope any) granted to `operator`.
//
// Install via the InstallPackage kernel op. See
// docs/components/_packages.md.
package orchestrationbase

import "github.com/asolgan/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:        "orchestration-base",
	Version:     "0.1.0",
	Description: "Generic task substrate (task DDL + CreateTask) + package-owned capabilityEphemeral lens (FR56 grant re-sourcing).",
	Depends:     []string{"identity-domain"},
	DDLs:        DDLs(),
	Lenses:      Lenses(),
	Permissions: Permissions(),
}
