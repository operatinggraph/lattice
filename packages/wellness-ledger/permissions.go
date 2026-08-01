package wellnessledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants. All three
// ops are orchestrator-submitted (the same operator-grant idiom clinic-ledger
// uses): the trusted-tool app submits CreateAccount when a member's first
// charge/payment needs an account, and DebitAccount/CreditAccount when a
// charge or payment is recorded.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "CreateAccount",
			Scope:         "any",
			Note:          "Grants the operator the right to submit CreateAccount (opens the ledger account for a member).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "DebitAccount",
			Scope:         "any",
			Note:          "Grants the operator the right to submit DebitAccount (records a charge — a no-show fee today).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "CreditAccount",
			Scope:         "any",
			Note:          "Grants the operator the right to submit CreditAccount (records a payment received).",
			GrantsTo:      []string{"operator"},
		},
	}
}
