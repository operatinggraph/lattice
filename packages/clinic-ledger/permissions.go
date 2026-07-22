package clinicledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants. All three
// ops are orchestrator-submitted (the same operator-grant idiom clinic-domain
// uses): the trusted-tool app submits CreateAccount when a patient is
// registered and DebitAccount/CreditAccount when a charge or payment is
// recorded.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "CreateAccount",
			Scope:         "any",
			Note:          "Grants the operator the right to submit CreateAccount (opens the ledger account for a registered patient).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "DebitAccount",
			Scope:         "any",
			Note:          "Grants the operator the right to submit DebitAccount (records a charge — a copay, an invoice line).",
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
