package clinicledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants.
// DebitAccount/CreditAccount stay orchestrator-submitted (the operator-grant
// idiom clinic-domain uses) — both are charge/payment entries a human posts
// via the front desk or billing flow. ClinicCreateAccount also grants
// front-of-house staff: clinic-app's billing view can only ever show "no
// account yet" until some caller opens the patient's ledger account, and
// that caller is meant to be the browser, the same as clinic-domain's
// CreatePatient/BindProviderIdentity front-desk grants. Unconfined — a
// clinicaccount is anchored on a patient, and clinic-domain's own patient
// registration is itself unconfined front-desk work, so there is no
// location to workplace-confine this to either.
//
// Named ClinicCreateAccount rather than the bare CreateAccount every other
// ledger package's create op used: a standing grant matches on
// operationType STRING EQUALITY alone (Contract #6 §240;
// processor.matchPlatformPermission) — the envelope's `class` picks the DDL
// but step 3 never reads it — so operationType is a GLOBAL namespace. A
// frontOfHouse grant on the bare "CreateAccount" name would also authorize
// that role against loftspace-ledger's/cafe-ledger's identically-named
// (operator-only) op, none of which intend it (lint-package-standard S9;
// cafe-ledger's CreditCafeAccount and wellness-ledger's
// WellnessCreateAccount are the same idiom). Nothing else references
// "CreateAccount" in this package (no seed script, no Weaver target), so
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
