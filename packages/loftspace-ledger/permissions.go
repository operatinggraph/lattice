package loftspaceledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants.
//
// Grant matrix:
//
//	LoftspaceCreateAccount → operator, frontOfHouse (workplace-confined)
//	DebitAccount           → operator
//	CreditAccount          → operator, consumer (scope=self — see below)
//
// DebitAccount stays orchestrator-submitted (the same operator-grant idiom
// lease-signing uses): charging rent is a landlord act, not a resident's to
// self-serve. LoftspaceCreateAccount also grants front-of-house staff:
// loftspace-app's billing view can only ever show "no account yet" until some
// caller opens the lease's ledger account, and that caller is meant to be the
// browser, the same as lease-signing's DecideLeaseApplication front-desk
// grant. Unlike clinic-ledger's / wellness-ledger's identical create op —
// unconfined because a patient/member carries no building — a leaseapp sits
// at a unit, so the frontOfHouse grant here is workplace-confined in
// scripts.go's execute() (require_workplace on the lease's appliesToUnit
// topology), mirroring DecideLeaseApplication / cafe-ledger's
// CreditCafeAccount.
//
// CreditAccount's scope=self grant (a tenant paying down what they owe) is
// the one direction cafe-domain's Settle/Charge idiom does NOT already cover:
// café's own resident self-service deliberately excludes crediting the
// account (a payment is a front-desk act there — cash/card at the counter,
// so the amount is staff-witnessed). A rent portal is a different real-world
// shape (self-pay is the norm), so this package grants it, but the platform
// has no payment-rail integration to witness the money — the amount itself
// is the attack surface a resident's own submit fully controls, not just
// which account it targets. scripts.go's post_entry therefore does BOTH: the
// ownership proof café's idiom already has (the account's OWN
// heldFor→leaseapp→applicationFor topology, never the payload, resolves the
// lease and binds it to the caller's identity) AND an amount proof café's
// idiom does not need (self-Charge/Settle bind the amount to a trusted
// catalog/tab total instead) — a self-credit may never exceed the account's
// own recomputed outstanding balance, paginated + bounded, failing closed if
// the history is too large to verify. DebitAccount gets no matching
// self-scope grant — a resident may pay down a balance, never charge one.
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
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "LoftspaceCreateAccount",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to submit LoftspaceCreateAccount (opens the ledger account for a signed lease). frontOfHouse is workplace-confined to the lease's unit — see package doc.",
			GrantsTo:      []string{"operator", "frontOfHouse"},
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
		{
			OperationType: "CreditAccount",
			Scope:         "self",
			Note:          "Grants a consumer the right to credit (pay down) THEIR OWN lease's ledger account — the account's heldFor lease's applicationFor link must resolve to the caller's identity (scripts.go). No matching DebitAccount grant: a resident pays down a balance, never charges one.",
			GrantsTo:      []string{"consumer"},
		},
	}
}
