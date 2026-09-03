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
//
// ClinicCreditAccount's scope=self grant (a patient paying down what they
// owe) mirrors loftspace-ledger's CreditAccount consumer scope=self: the one
// direction café's Settle/Charge idiom doesn't cover (a payment there is a
// staff-witnessed cash/card act), but a patient portal's self-pay is the
// norm and the platform has no payment-rail integration to witness the
// money — the amount itself is the attack surface a patient's own submit
// fully controls, not just which account it targets. scripts.go's
// post_entry therefore proves BOTH: ownership (the account's own
// heldFor→patient→identifiedBy topology, never the payload, resolves the
// patient and binds it to the caller's identity) and amount (a self-credit
// may never exceed the account's own maintained .balance aspect — an O(1)
// cache post_entry keeps in lockstep with every posted entry, not a live
// full-history replay). ClinicDebitAccount gets no matching self-scope grant — a patient
// may pay down a balance, never charge one.
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
		{
			OperationType: "ClinicCreditAccount",
			Scope:         "self",
			Note:          "Grants a patient the right to credit (pay down) THEIR OWN account — the account's heldFor patient's identifiedBy link must resolve to the caller's identity (scripts.go). No matching ClinicDebitAccount grant: a patient pays down a balance, never charges one.",
			GrantsTo:      []string{"consumer"},
		},
	}
}
