package cafedomain

import "github.com/asolgan/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants. All three
// ops are orchestrator-submitted (the same operator-grant idiom every café
// package uses): the trusted-tool app (POS / front-desk FE) submits OpenTab
// when a resident's visit starts, Charge per rung-up item, and Settle at
// checkout.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "OpenTab",
			Scope:         "any",
			Note:          "Grants the operator the right to submit OpenTab (starts a café house-tab session for a resident lease).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "Charge",
			Scope:         "any",
			Note:          "Grants the operator the right to submit Charge (rings up an item on an open tab).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "Settle",
			Scope:         "any",
			Note:          "Grants the operator the right to submit Settle (closes a tab for house-account posting).",
			GrantsTo:      []string{"operator"},
		},
	}
}
