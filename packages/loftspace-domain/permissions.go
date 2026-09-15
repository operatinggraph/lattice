package loftspacedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions grants every op to the `operator` role (scope any), and the
// three listing ops additionally to `consumer` at scope=self — the landlord
// path. The role canonical name `operator` is resolved by cmd/lattice-pkg to
// the seeded NanoID from lattice.bootstrap.json.
//
// Grant matrix:
//
//	SetListing               → operator
//	SetUnitAddress           → operator
//	SetListingStatus         → operator
//	AssignUnitOwner          → operator
//	RemoveUnitOwner          → operator
//	SetListing (self)        → consumer
//	SetUnitAddress (self)    → consumer
//	SetListingStatus (self)  → consumer
//
// The three listing ops carry a scope=self grant to `consumer` — the landlord
// path: a signed-in landlord holds no operator role, and what confines them is
// the script's require_manages probe, which requires the acting identity's own
// `manages` link to the payload unit before any of the three writes. Editing
// the rent or the address of a unit one manages is the same act as taking it
// off-market, so the three share one grant shape. The ownership ops
// (AssignUnitOwner / RemoveUnitOwner) carry no such grant: they are what
// CONFERS management, and the FIRST assignment onto a freshly minted unit is by
// construction something no already-managing identity can authorize, so
// opening them to `consumer` would buy co-manager delegation rather than the
// self-service path the landlord console needs. The script's enforce_manages
// probe is what would make such a grant SAFE to consider, but a delegated
// `manages` link is revocable only by an operator (never by the delegator who
// granted it) and confers DecideLeaseApplication, the renewal ops, and a Secure
// Lens that decrypts applicant identity names — a durable,
// only-operator-revocable PII-decrypt grant with no landlord demand behind it.
// Decided: not granted. Unit management stays operator-conferred, so posting a
// NEW listing (which mints the unit and assigns its manager) remains an
// operator act while editing an already-managed one is the landlord's.
func Permissions() []pkgmgr.PermissionSpec {
	mk := func(op string) pkgmgr.PermissionSpec {
		return pkgmgr.PermissionSpec{
			OperationType: op,
			Scope:         "any",
			Note:          "Grants the operator the right to submit " + op + " operations.",
			GrantsTo:      []string{"operator"},
		}
	}
	return []pkgmgr.PermissionSpec{
		mk("SetListing"),
		mk("SetUnitAddress"),
		mk("SetListingStatus"),
		mk("AssignUnitOwner"),
		mk("RemoveUnitOwner"),
		{
			OperationType: "SetListing",
			Scope:         "self",
			Note:          "Grants a landlord the right to set the listing economics of a unit they MANAGE (the script requires the acting identity's manages link to the payload unit).",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "SetUnitAddress",
			Scope:         "self",
			Note:          "Grants a landlord the right to set the address of a unit they MANAGE (the script requires the acting identity's manages link to the payload unit).",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "SetListingStatus",
			Scope:         "self",
			Note:          "Grants a landlord the right to transition the listing status of a unit they MANAGE (the script requires the acting identity's manages link to the payload unit).",
			GrantsTo:      []string{"consumer"},
		},
	}
}
