package loftspaceledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants. All three
// ops are orchestrator-submitted (the same operator-grant idiom lease-signing
// uses): the trusted-tool app submits CreateAccount when a lease is signed and
// DebitAccount/CreditAccount when a charge or payment is recorded.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "CreateAccount",
			Scope:         "any",
			Note:          "Grants the operator the right to submit CreateAccount (opens the ledger account for a signed lease).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "DebitAccount",
			Scope:         "any",
			Note:          "Grants the operator the right to submit DebitAccount (records a charge — rent, a late fee, a deposit).",
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
