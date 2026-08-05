package wellnessledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants.
// WellnessCreateAccount, WellnessDebitAccount, and WellnessCreditAccount all
// grant front-of-house staff alongside the operator: wellness-app's Roster
// view ships a Record charge/payment form to the front desk, and every step
// of that flow — opening a member's account, then charging or crediting it —
// is meant to be reachable from the browser, mirroring clinic-ledger's
// identical ClinicCreateAccount/ClinicDebitAccount/ClinicCreditAccount fix.
// Unconfined — a wellnessaccount is anchored on a member identity, which
// carries no building, so there is no location to workplace-confine any of
// the three to.
//
// WellnessCreateAccount ALSO grants `consumer` at scope=self (real-actor-
// write-auth-e2e idiom, wellness-domain's CreateBooking self-scope
// precedent): most bookings are self-service (Schedule's own Book button,
// not a front-desk walk-in), so the account has to open at the SAME moment
// a self-service booker's booking is created — the any-scope front-desk
// grant alone would leave the majority of bookers without one.
// `authContext.target == actor` is checked at step 3 (Contract #6); the
// script separately requires payload.identityKey to BE that target
// (scripts.go), the same gap CreateBooking's script closes for
// payload.booker. WellnessDebitAccount/WellnessCreditAccount carry no
// self-scope grant: a payment credit needs a human witness (cash/card handed
// over the counter), the same reason clinic-ledger and cafe-ledger keep
// their own credit ops off the consumer role — a member self-crediting their
// own balance would erase debt with no actual payment changing hands.
//
// Named Wellness{CreateAccount,DebitAccount,CreditAccount} rather than the
// bare names every other ledger package's create/debit/credit ops use: a
// standing grant matches on operationType STRING EQUALITY alone (Contract
// #6 §240; processor.matchPlatformPermission) — the envelope's `class`
// picks the DDL but step 3 never reads it — so operationType is a GLOBAL
// namespace. A frontOfHouse grant on a bare "DebitAccount"/"CreditAccount"
// name would also authorize that role against clinic-ledger's/
// loftspace-ledger's identically-named (operator-only, or in clinic's case
// now Clinic-prefixed) ops, none of which intend it (lint-package-standard
// S9; cafe-ledger's CreditCafeAccount is the same idiom). Nothing else
// references the bare "DebitAccount"/"CreditAccount" names in this package
// (no seed script, only this package's own targets.go Weaver dispatch), so
// this is a straight rename, not an additive alias.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "WellnessCreateAccount",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to submit WellnessCreateAccount (opens the ledger account for a member). Unconfined: see package doc.",
			GrantsTo:      []string{"operator", "frontOfHouse"},
		},
		{
			OperationType: "WellnessCreateAccount",
			Scope:         "self",
			Note:          "Grants a consumer the right to open their OWN ledger account (identityKey must be the caller's own identity) — mirrors wellness-domain's CreateBooking self-scope grant.",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "WellnessDebitAccount",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to submit WellnessDebitAccount (records a charge — a no-show fee, a class price, or a front-desk-recorded fee). Unconfined: see package doc.",
			GrantsTo:      []string{"operator", "frontOfHouse"},
		},
		{
			OperationType: "WellnessCreditAccount",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to submit WellnessCreditAccount (records a payment received). Unconfined: see package doc.",
			GrantsTo:      []string{"operator", "frontOfHouse"},
		},
	}
}
