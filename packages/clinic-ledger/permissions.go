package clinicledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants.
// ClinicCreateAccount, ClinicDebitAccount, and ClinicCreditAccount all grant
// front-of-house staff alongside the operator: clinic-app's billing view
// ships a single Record charge/payment form to the front desk, and every
// step of that flow — opening the account, then charging or crediting it —
// is meant to be reachable from the browser, the same as clinic-domain's
// CreatePatient/BindProviderIdentity front-desk grants. Unconfined — a
// clinicaccount is anchored on a patient, and clinic-domain's own patient
// registration is itself unconfined front-desk work, so there is no
// location to workplace-confine any of the three to.
//
// Named Clinic{CreateAccount,DebitAccount,CreditAccount} rather than the
// bare names every other ledger package's create/debit/credit ops use: a
// standing grant matches on operationType STRING EQUALITY alone (Contract
// #6 §240; processor.matchPlatformPermission) — the envelope's `class`
// picks the DDL but step 3 never reads it — so operationType is a GLOBAL
// namespace. A frontOfHouse grant on a bare "DebitAccount"/"CreditAccount"
// name would also authorize that role against loftspace-ledger's/
// wellness-ledger's identically-named (operator-only) ops, none of which
// intend it (lint-package-standard S9; cafe-ledger's CreditCafeAccount and
// wellness-ledger's WellnessCreateAccount are the same idiom). Nothing else
// references the bare "DebitAccount"/"CreditAccount" names in this package
// (no seed script, only this package's own targets.go Weaver dispatch), so
// this is a straight rename, not an additive alias.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "ClinicCreateAccount",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to submit ClinicCreateAccount (opens the ledger account for a registered patient). Unconfined: see package doc.",
			GrantsTo:      []string{"operator", "frontOfHouse"},
		},
		{
			OperationType: "ClinicDebitAccount",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to submit ClinicDebitAccount (records a charge — a copay, an invoice line). Unconfined: see package doc.",
			GrantsTo:      []string{"operator", "frontOfHouse"},
		},
		{
			OperationType: "ClinicCreditAccount",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to submit ClinicCreditAccount (records a payment received). Unconfined: see package doc.",
			GrantsTo:      []string{"operator", "frontOfHouse"},
		},
	}
}
