package loftspacedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions grants every op to the `operator` role (scope any). The role
// canonical name `operator` is resolved by cmd/lattice-pkg to the seeded NanoID
// from lattice.bootstrap.json.
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
		mk("SetListing"),
		mk("SetUnitAddress"),
		mk("SetListingStatus"),
		mk("AssignUnitOwner"),
		mk("RemoveUnitOwner"),
	}
}
