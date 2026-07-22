package cafeledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants. All three
// ops are orchestrator-submitted (the same operator-grant idiom every ledger
// package uses): the trusted-tool app submits CreateAccount when a resident
// opens a house tab for the first time and DebitAccount/CreditAccount when a
// tab is settled or a payment is recorded.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "CreateAccount",
			Scope:         "any",
			Note:          "Grants the operator the right to submit CreateAccount (opens the café house-tab account for a resident lease).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "DebitAccount",
			Scope:         "any",
			Note:          "Grants the operator the right to submit DebitAccount (records a café charge — a settled tab).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "CreditAccount",
			Scope:         "any",
			Note:          "Grants the operator the right to submit CreditAccount (records a house-tab payment received).",
			GrantsTo:      []string{"operator"},
		},
	}
}
