package loftspaceledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants.
//
// Grant matrix:
//
//	LoftspaceCreateAccount → operator, frontOfHouse (workplace-confined), consumer (scope=self — landlord, see below)
//	DebitAccount           → operator
//	LoftspaceRecordCharge  → operator, consumer (scope=self — landlord only, see below)
//	CreditAccount          → operator, consumer (scope=self — resident or landlord, see below)
//	EvaluateLoftspaceArrears                   → operator (Weaver's dispatch actor; the script refuses every other)
//	RecordLoftspaceArrearsReminderNotification → operator (the bridge's service actor)
//
// DebitAccount is the ORCHESTRATED charge: the operator, and Weaver's
// clauseSatisfaction playbook (packages/semantic-contracts, Contract #10
// §10.8's canonical directOp) carrying a clauseRef that binds the amount to
// the clause's own terms. It stays operator-only. LoftspaceRecordCharge is a
// PERSON's manual charge on a lease account — the same append-only debit
// entry, never a clauseRef — submitted by the operator or by a landlord who
// manages the lease's unit (the scope=self grant). The two are distinct
// operationTypes rather than one op with two grants because operationType is
// a global namespace (the LoftspaceCreateAccount paragraph below): cafe-ledger
// admits its own DebitAccount, so a consumer grant on that name would reach
// the café script too (lint-package-standard S9).
//
// LoftspaceCreateAccount also grants front-of-house staff: loftspace-app's
// billing view can only ever show "no account yet" until some caller opens
// the lease's ledger account, and that caller is meant to be the browser,
// the same as lease-signing's DecideLeaseApplication front-desk grant.
// Unlike clinic-ledger's / wellness-ledger's identical create op —
// unconfined because a patient/member carries no building — a leaseapp sits
// at a unit, so the frontOfHouse grant here is workplace-confined in
// scripts.go's execute() (require_workplace on the lease's appliesToUnit
// topology), mirroring DecideLeaseApplication / cafe-ledger's
// CreditCafeAccount. Its scope=self grant is the landlord's: loftspace-app's
// landlord form opens the account itself on a lease's first-ever charge or
// payment, and a landlord holds no worksAt link, so the account script binds
// that path with the same manages probe as DecideLeaseApplication
// (require_manages on the lease's appliesToUnit unit).
//
// The transaction ops' two scope=self grants serve two populations that share the `consumer`
// role, and scripts.go's post_entry tells them apart from the account's OWN
// topology (heldFor→leaseapp), never from the payload: the RESIDENT holds
// the lease's applicationFor link; the LANDLORD holds a manages link to the
// unit the lease appliesToUnit. The resident proof is tried first — a
// landlord who tenants their own unit is a resident.
//
// The resident may credit, never debit, and the credit is capped.
// CreditAccount's resident path (a tenant paying down what they owe) is the
// one direction cafe-domain's Settle/Charge idiom does NOT already cover:
// café's own resident self-service deliberately excludes crediting the
// account (a payment is a front-desk act there — cash/card at the counter,
// so the amount is staff-witnessed). A rent portal is a different real-world
// shape (self-pay is the norm), so this package grants it, but the platform
// has no payment-rail integration to witness the money — the amount itself
// is the attack surface a resident's own submit fully controls, not just
// which account it targets. post_entry therefore does BOTH: the ownership
// proof café's idiom already has (the account's OWN
// heldFor→leaseapp→applicationFor topology resolves the lease and binds it
// to the caller's identity) AND an amount proof café's idiom does not need
// (self-Charge/Settle bind the amount to a trusted catalog/tab total
// instead) — a self-credit may never exceed the account's own recomputed
// outstanding balance, paginated + bounded, failing closed if the history is
// too large to verify. A resident holding a LoftspaceRecordCharge self grant
// is still refused by the script: a resident pays down a balance, never
// charges one.
//
// The landlord may debit AND credit, uncapped. The landlord is the lease's
// creditor — a repair charge or a month's rent is their own receivable, and
// a cheque received or rent forgiven is their own money to record — so
// neither direction has an amount to distrust; what the landlord path proves
// is only that the account's lease sits on a unit they manage
// (heldFor→appliesToUnit→manages), the same management link that confines
// lease-signing's DecideLeaseApplication and loftspace-domain's
// SetListingStatus self paths.
//
// Named LoftspaceCreateAccount rather than the bare CreateAccount this op
// used before: a standing grant matches on operationType STRING EQUALITY
// alone (Contract #6 §240; processor.matchPlatformPermission) — the
// envelope's `class` picks the DDL but step 3 never reads it — so
// operationType is a GLOBAL namespace. A frontOfHouse grant on the bare
// "CreateAccount" name would also authorize that role against
// clinic-ledger's/cafe-ledger's identically-named (operator-only) op, none
// of which intend it (lint-package-standard S9; cafe-ledger's
// CreditCafeAccount and wellness-ledger's WellnessCreateAccount are the
// same idiom). No Weaver target references it, but two external callers did
// and are renamed alongside this package: scripts/seed-showcase.go and
// packages/semantic-contracts (its own CreateClause/DebitAccount tests open
// a lease account first) — a straight rename, not an additive alias.
// LoftspaceRecordCharge is the same rule applied where a rename is NOT
// possible: DebitAccount's name is Contract #10 §10.8's literal and Weaver's
// clause-billing dispatch, so the person-facing charge gets its own
// vertical-unique name and DebitAccount keeps the orchestrated one.
//
// EvaluateLoftspaceArrears and the arrears notification replyOp are the two
// ops no human path reaches. Both grant `operator` at scope=any — the
// operator-grant idiom every engine-submitted op uses — and neither is
// callable from a console: WEAVER's dispatch actor submits the first (and the
// script refuses every other actor outright, since the account it names ends
// up in a message a tenant actually receives), the BRIDGE's service actor the
// second. Granting them to `operator` is what authorizes those two engines,
// and deliberately mints no consoleOperator or frontOfHouse counterpart —
// there is no desk workflow that runs either one by hand.
func Permissions() []pkgmgr.PermissionSpec {
	return append([]pkgmgr.PermissionSpec{
		{
			OperationType: "LoftspaceCreateAccount",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to submit LoftspaceCreateAccount (opens the ledger account for a signed lease). frontOfHouse is workplace-confined to the lease's unit — see package doc.",
			GrantsTo:      []string{"operator", "frontOfHouse"},
		},
		{
			OperationType: "LoftspaceCreateAccount",
			Scope:         "self",
			Note:          "Grants a consumer the right to open the ledger account of a lease on a unit they MANAGE — the landlord's first-ever charge or payment opens it. scripts.go's account script proves the manages link off the lease's own appliesToUnit unit (require_manages); no other consumer reaches the write.",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "DebitAccount",
			Scope:         "any",
			Note:          "Grants the operator the right to submit DebitAccount (records a charge — rent, a late fee, a deposit; the clause-authorized shape Weaver's clauseSatisfaction playbook dispatches; a person's manual charge is LoftspaceRecordCharge).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "CreditAccount",
			Scope:         "any",
			Note:          "Grants the operator the right to submit CreditAccount (records a payment received).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "CreditAccount",
			Scope:         "self",
			Note:          "Grants a consumer the right to record a payment on a lease's ledger account they stand behind: a resident paying down THEIR OWN lease (the account's heldFor lease's applicationFor link resolves to the caller — capped at the outstanding balance), or a landlord recording a payment received on a lease of a unit they MANAGE (heldFor→appliesToUnit→manages — uncapped, it is their own receivable). scripts.go.",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "LoftspaceRecordCharge",
			Scope:         "any",
			Note:          "Grants the operator the right to submit LoftspaceRecordCharge (a person's manual charge on a lease's ledger account — rent, a late fee, a repair; never clause-authorized).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "LoftspaceRecordCharge",
			Scope:         "self",
			Note:          "Grants a consumer the right to record a charge on the ledger account of a lease on a unit they MANAGE — a landlord's own receivable. scripts.go proves it off the account's own heldFor→appliesToUnit→manages topology; a resident (applicationFor) holding this grant is still refused.",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: arrearsOp,
			Scope:         "any",
			Note:          "Grants the operator the right to submit EvaluateLoftspaceArrears (ages a lease account and sends the one arrears reminder per episode). Dispatched by WEAVER's loftspaceArrearsReminders playbook — the script refuses every actor but Weaver's dispatch actor, because the account named on the payload is forwarded into a message a tenant receives. Not a console operation: no consoleOperator grant is minted for it.",
			GrantsTo:      []string{"operator"},
		},
	}, notificationPermissions()...)
}
