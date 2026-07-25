package loftspacedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions grants every op to the `operator` role (scope any). The role
// canonical name `operator` is resolved by cmd/lattice-pkg to the seeded NanoID
// from lattice.bootstrap.json.
//
// SetListingStatus additionally carries a scope=self grant to `consumer` — the
// landlord path. The ownership ops (AssignUnitOwner / RemoveUnitOwner) get NO
// such grant on purpose: they are what CONFERS management, so a self-scoped
// grant on them would let any signed-in identity make itself the landlord of
// any unit.
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
		{
			OperationType: "SetListingStatus",
			Scope:         "self",
			Note:          "Grants a landlord the right to transition the listing status of a unit they MANAGE (the script requires the acting identity's manages link to the payload unit).",
			GrantsTo:      []string{"consumer"},
		},
	}
}
