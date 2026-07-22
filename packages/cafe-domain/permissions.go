package cafedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants. Every op
// keeps its orchestrator-submitted grant (the trusted-tool app — POS /
// front-desk FE — submits OpenTab when a resident's visit starts, Charge per
// rung-up item, and Settle at checkout). OpenTab, Charge, and Settle ALL
// ALSO grant `consumer`, scope=self (real-actor-write-auth-e2e idiom,
// clinic-domain's CreateAppointment/wellness-domain's CreateBooking
// precedent): a resident may open, self-order on, or close their OWN house
// tab. `authContext.target == actor` is checked at step 3 (Contract #6); the
// Starlark script separately requires the tab's lease to be identified-by
// that target identity (via the lease's applicationFor link, lease-signing's
// own convergence-link shape) — the same patient/identifiedBy indirection
// clinic-domain uses, since a café tab is anchored to a lease, not an
// identity, directly. A self-service Charge binds against the menuItem
// catalog (CreateMenuItem/RetireMenuItem below, operator-only): the
// referenced item's own .price.priceCents is what a resident's charge
// amounts to, never a caller-supplied number — the gap the original
// operator-only Charge grant existed to cover.
//
// OpenTab, Charge, and Settle additionally grant `frontOfHouse` at scope=any —
// the POS beat the package doc above already describes as the trusted-tool
// app's job. Naming the role makes that posture honest: the shipped café FE
// submits as `operator` (root-equivalent) today, and `frontOfHouse` reaches
// exactly the three tab ops and nothing else. The menu catalog
// (CreateMenuItem / RetireMenuItem) stays operator-only — pricing is not a
// front-desk decision, and the self-service Charge derivation trusts it.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "OpenTab",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to submit OpenTab (starts a café house-tab session for a resident lease).",
			GrantsTo:      []string{"operator", "frontOfHouse"},
		},
		{
			OperationType: "OpenTab",
			Scope:         "self",
			Note:          "Grants a consumer the right to open a house tab for THEMSELVES (the tab's lease must be identified-by the caller's own identity).",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "Charge",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to submit Charge (rings up an item on an open tab, raw amountCents).",
			GrantsTo:      []string{"operator", "frontOfHouse"},
		},
		{
			OperationType: "Charge",
			Scope:         "self",
			Note:          "Grants a consumer the right to self-order on THEIR OWN house tab (the tab's lease must be identified-by the caller's own identity); amountCents is derived from a menuItem catalog entry, never trusted from the caller.",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "Settle",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to submit Settle (closes a tab for house-account posting).",
			GrantsTo:      []string{"operator", "frontOfHouse"},
		},
		{
			OperationType: "Settle",
			Scope:         "self",
			Note:          "Grants a consumer the right to settle THEIR OWN house tab (the tab's lease must be identified-by the caller's own identity).",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "CreateMenuItem",
			Scope:         "any",
			Note:          "Grants the operator the right to add an item to the self-order menu catalog.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "RetireMenuItem",
			Scope:         "any",
			Note:          "Grants the operator the right to remove an item from the self-order menu catalog.",
			GrantsTo:      []string{"operator"},
		},
	}
}
