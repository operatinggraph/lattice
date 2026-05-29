// Package rbacdomain is the rbac-domain Capability Package. It provides
// role, permission, and grant management operations.
//
// One DDL (`rbac`) handles all 10 operations:
//
//	CreateRole, UpdateRole, TombstoneRole
//	CreatePermission, UpdatePermission, TombstonePermission
//	AssignRole, RevokeRole
//	GrantPermission, RevokePermission
//
// Link conventions:
//   - holdsRole link key: lnk.<actorType>.<actorId>.holdsRole.role.<roleId>
//     (actor source, role target — actor added later in graph growth)
//   - grantedBy link key: lnk.permission.<permId>.grantedBy.role.<roleId>
//     ("permission granted by role"; permission source, role target —
//     permission added later in graph growth)
//
// The GrantPermission / RevokePermission operations write/tombstone the
// grantedBy link. The operation verbs follow operator-action semantics
// and are orthogonal to the link's canonical name.
//
// Install via `lattice-pkg install packages/rbac-domain`. See
// docs/components/_packages.md.
package rbacdomain

import "github.com/asolgan/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:        "rbac-domain",
	Version:     "0.1.0",
	Description: "Role, permission, and grant management operations.",
	Depends:     []string{},
	DDLs:        DDLs(),
	Permissions: Permissions(),
}
