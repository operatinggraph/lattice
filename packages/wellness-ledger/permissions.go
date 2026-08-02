package wellnessledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants.
// DebitAccount/CreditAccount stay orchestrator-submitted (the operator-grant
// idiom clinic-ledger uses) — every wellness charge today is a Weaver-target
// auto-charge (no-show fee, class price), never a front-desk-initiated entry.
// WellnessCreateAccount also grants front-of-house staff: wellness-app's My
// Classes balance panel can only ever show "no charges yet" until some
// caller opens the member's account, and unlike Debit/Credit that caller IS
// meant to be the browser (a member's first charge should not have to
// precede their account existing). Unconfined, mirroring clinic-ledger's
// CreateAccount / clinic-domain's CreatePatient — a wellnessaccount is
// anchored on a member identity, which carries no building, so there is no
// location to workplace-confine front-desk staff to.
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
// payload.booker.
//
// Named WellnessCreateAccount rather than the bare CreateAccount every other
// ledger package's CreateAccount uses: a standing grant matches on
// operationType STRING EQUALITY alone (Contract #6 §240;
// processor.matchPlatformPermission) — the envelope's `class` picks the DDL
// but step 3 never reads it — so operationType is a GLOBAL namespace. A
// frontOfHouse grant on the bare "CreateAccount" name would also authorize
// that role against clinic-ledger's / loftspace-ledger's / cafe-ledger's
// identically-named (operator-only) op, none of which intend it
// (lint-package-standard S9; cafe-ledger's CreditCafeAccount is the same
// idiom). Nothing else references "CreateAccount" in this package (no seed
// script, no Weaver target, no FE call), so this is a straight rename, not
// an additive alias.
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
			OperationType: "DebitAccount",
			Scope:         "any",
			Note:          "Grants the operator the right to submit DebitAccount (records a charge — a no-show fee today).",
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
