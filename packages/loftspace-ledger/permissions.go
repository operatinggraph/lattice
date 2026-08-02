package loftspaceledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants.
//
// Grant matrix:
//
//	LoftspaceCreateAccount → operator, frontOfHouse (workplace-confined)
//	DebitAccount           → operator
//	CreditAccount          → operator
//
// DebitAccount/CreditAccount stay orchestrator-submitted (the same
// operator-grant idiom lease-signing uses): the trusted-tool app submits
// them when a charge or payment is recorded. LoftspaceCreateAccount also
// grants front-of-house staff: loftspace-app's billing view can only ever
// show "no account yet" until some caller opens the lease's ledger account,
// and that caller is meant to be the browser, the same as lease-signing's
// DecideLeaseApplication front-desk grant. Unlike clinic-ledger's /
// wellness-ledger's identical create op — unconfined because a patient/member
// carries no building — a leaseapp sits at a unit, so the frontOfHouse grant
// here is workplace-confined in scripts.go's execute() (require_workplace on
// the lease's appliesToUnit topology), mirroring DecideLeaseApplication /
// cafe-ledger's CreditCafeAccount.
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
// same idiom). Nothing else references "CreateAccount" in this package (no
// seed script, no Weaver target), so this is a straight rename, not an
// additive alias.
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
	}
}
