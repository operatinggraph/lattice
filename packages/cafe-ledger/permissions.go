package cafeledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants. CreateAccount
// and DebitAccount are orchestrator-submitted (the operator-grant idiom every
// ledger package uses): CreateAccount when a resident opens a house tab for the
// first time, DebitAccount when the cafeTabSettlement playbook posts a settled
// tab. CreditCafeAccount is the one a human runs — a payment handed over at the
// counter — so it also grants frontOfHouse, bound by the workplace confinement
// in transactionDDLScript to the buildings that staffer actually works at.
//
// The vertical prefix on CreditCafeAccount is load-bearing, not decoration —
// do not "tidy" it back to CreditAccount. A standing grant is matched by
// operationType STRING EQUALITY alone (Contract #6 §240;
// processor.matchPlatformPermission), and the envelope's `class` — which picks
// the DDL — is a client-supplied hint that step 3 never reads. So an
// operationType is a GLOBAL namespace: loftspace-ledger and clinic-ledger each
// declare their own credit op, and every ledger's transaction DDL lists it in
// permittedCommands. A grant named CreditAccount here would therefore authorize
// the same caller against THEIR accounts too, where no workplace guard exists —
// turning this confined café grant into unconfined credit-any-account authority
// across three verticals. cafe-domain's Weaver target already pins `class` for
// the same collision on DebitAccount (targets.go), but pinning the class fixes
// dispatch, not authorization.
//
// CreateAccount and DebitAccount keep their bare names because they are granted
// to `operator` alone: the operator is unconfined everywhere by design, so for
// them the collision confers nothing. The rule the lint-package-standard S9 gate
// enforces is exactly that boundary — an operationType granted beyond `operator`
// must be declared by exactly one package.
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
			OperationType: "CreditCafeAccount",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to submit CreditCafeAccount (records a house-tab payment received). A staffer is confined to accounts whose lease sits at a location they worksAt; the operator is unconfined.",
			GrantsTo:      []string{"operator", "frontOfHouse"},
		},
	}
}
