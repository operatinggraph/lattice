package loftspacedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions grants every op to the `operator` role (scope any). The role
// canonical name `operator` is resolved by cmd/lattice-pkg to the seeded NanoID
// from lattice.bootstrap.json.
//
// SetListingStatus additionally carries a scope=self grant to `consumer` — the
// landlord path. The ownership ops (AssignUnitOwner / RemoveUnitOwner) carry no
// such grant: they are what CONFERS management, and the FIRST assignment onto a
// freshly minted unit is by construction something no already-managing identity
// can authorize, so opening them to `consumer` would buy delegation rather than
// the self-service path the landlord console needs. The script's enforce_manages
// probe is what would make such a grant SAFE to consider — it default-denies
// every non-operator actor that does not already hold a manages link to the
// payload unit — but the grant itself is a product call nobody has made.
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
