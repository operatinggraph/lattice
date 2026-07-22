package rbacdomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the 10 permission vertices + their grants. Every
// operation is granted to the `operator` role. The role canonical name
// `operator` is resolved by cmd/lattice-pkg to the seeded NanoID from
// lattice.bootstrap.json.
func Permissions() []pkgmgr.PermissionSpec {
	mk := func(op string) pkgmgr.PermissionSpec {
		return pkgmgr.PermissionSpec{
			OperationType: op,
			Scope:         "any",
			Note:          "Grants the operator the right to submit " + op + " operations.",
			GrantsTo:      []string{"operator"},
		}
	}
	return []pkgmgr.PermissionSpec{
		mk("CreateRole"),
		mk("UpdateRole"),
		mk("TombstoneRole"),
		mk("CreatePermission"),
		mk("UpdatePermission"),
		mk("TombstonePermission"),
		mk("AssignRole"),
		mk("RevokeRole"),
		mk("GrantPermission"),
		mk("RevokePermission"),
	}
}
