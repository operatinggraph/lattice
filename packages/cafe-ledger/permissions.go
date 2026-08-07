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
//
// CreditCafeAccount's scope=self grant (a resident paying down their own house
// tab) mirrors loftspace-ledger's CreditAccount / clinic-ledger's
// ClinicCreditAccount consumer scope=self grants: nothing on this platform
// verifies a self-submitted payment actually happened (no payment-rail
// integration — out of scope for a reference vertical), so the amount itself
// is the attack surface, not just which account it targets. scripts.go's
// post_entry therefore proves BOTH ownership (the account's own
// heldFor->leaseapp->applicationFor topology, never the payload) and amount
// (a self-credit may never exceed the account's own recomputed outstanding
// balance, paginated + bounded, failing closed if the history is too large to
// verify). DebitAccount gets no matching self-scope grant — a resident pays
// down a balance, never charges one.
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
		{
			OperationType: "CreditCafeAccount",
			Scope:         "self",
			Note:          "Grants a resident the right to credit (pay down) THEIR OWN house-tab account — the account's heldFor lease's applicationFor link must resolve to the caller's identity (scripts.go). No matching DebitAccount grant: a resident pays down a balance, never charges one.",
			GrantsTo:      []string{"consumer"},
		},
	}
}
