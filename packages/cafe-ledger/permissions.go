package cafeledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants. CreateAccount
// and DebitAccount are orchestrator-submitted (the operator-grant idiom every
// ledger package uses): CreateAccount when a resident opens a house tab for the
// first time, DebitAccount when the cafeTabSettlement playbook posts a settled
// tab. CreditCafeAccount and RefundCafeCharge are the two a human runs — a
// payment handed over at the counter, and a charge the café decides was wrong —
// so both also grant frontOfHouse, bound by the workplace confinement in
// transactionDDLScript to the buildings that staffer actually works at.
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
// ClinicCreditAccount consumer scope=self grants. What that grant needs beyond
// the platform's own target==actor match is an OWNERSHIP proof, and scripts.go's
// post_entry supplies it: the account's own heldFor->leaseapp->applicationFor
// topology must resolve to the caller, never the payload.
//
// The AMOUNT is a separate question and is not a property of this grant at all.
// Nothing on this platform verifies that a payment actually happened (no
// payment-rail integration — out of scope for a reference vertical), and that is
// as true of an amount a front-desk staffer keys under the scope=any grant as of
// one a resident types under scope=self. So post_entry caps a payment at the
// account's own maintained .balance on EVERY leg — an unbounded credit either
// way writes off debt the café is owed, and a mis-keyed one hides the resident
// from collections behind a balance that reads as paid ahead. DebitAccount gets
// no matching self-scope grant — a resident pays down a balance, never charges
// one.
//
// EvaluateCafeArrears and the arrears notification replyOp are the two ops no
// human path reaches. Both grant `operator` at scope=any — the operator-grant
// idiom every engine-submitted op in this package already uses — and neither is
// callable from a console: WEAVER's dispatch actor submits the first (and the
// script refuses every other actor outright, since the account it names ends up
// in a message a resident actually receives), the BRIDGE's service actor the
// second. Granting them to `operator` is what authorizes those two engines, and
// deliberately mints no consoleOperator counterpart — there is no operator
// workflow that runs either one by hand.
func Permissions() []pkgmgr.PermissionSpec {
	return append([]pkgmgr.PermissionSpec{
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
		{
			OperationType: "RefundCafeCharge",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to submit RefundCafeCharge (gives back a charge already posted, anchored on that charge by a reverses link). A staffer is confined to accounts whose lease sits at a location they worksAt; the operator is unconfined. There is deliberately NO consumer grant at any scope: deciding a charge was wrong is the café's call, not the person who owes it, and a resident who could refund their own charges would simply erase what they drank.",
			GrantsTo:      []string{"operator", "frontOfHouse"},
		},
		{
			OperationType: arrearsOp,
			Scope:         "any",
			Note:          "Grants the operator the right to submit EvaluateCafeArrears (ages a house tab and sends the one arrears reminder per episode). Dispatched by WEAVER's cafeArrearsReminders playbook — the script refuses every actor but Weaver's dispatch actor, because the account named on the payload is forwarded into a message a resident receives. Not a console operation: no consoleOperator grant is minted for it.",
			GrantsTo:      []string{"operator"},
		},
	}, notificationPermissions()...)
}
