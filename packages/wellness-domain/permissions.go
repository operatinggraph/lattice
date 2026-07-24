package wellnessdomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions grants every wellness-domain op to the `operator` role (scope
// any) — the trusted-tool operator already holds standing permission, no new
// capability surface. CreateBooking and CancelBooking ALSO grant `consumer`,
// scope=self (real-actor-write-auth-e2e idiom, clinic-domain's
// CreateAppointment/RescheduleAppointment/SetAppointmentStatus precedent): a
// real resident books or cancels their OWN class through the Gateway.
// `authContext.target == actor` is checked at step 3 (Contract #6); the
// Starlark script separately requires the booking's actual booker (CreateBooking's
// payload.booker; CancelBooking's bookedBy link) to BE that target identity —
// simpler than clinic's patient/identifiedBy indirection, since a wellness
// booking's booker IS an identity directly, not a business vertex a linked
// identity must resolve through.
//
// TombstoneSession additionally grants `provider` at scope=any (widening the
// EXISTING scope=any row's GrantsTo, never a second row — a permission's
// identity is its (operationType, scope) pair, Contract #8 §8.1, mirroring
// clinic-domain's SetProviderHours widening in wave 1): a bound instructor
// cancels only a class THEY lead. Scope stays `any` (there is no scope=self
// equivalent for a non-identity target vertex like session), so the Starlark
// script itself confines a provider-role, non-operator caller to the session
// it is ledBy-bound to via the caller's own instructor identifiedBy binding —
// the same third-standing-binder shape clinic-domain's provider hat uses.
// front-of-house is deliberately NOT granted TombstoneSession — cancelling a
// class is the operator/instructor surface, not the front-desk one (unlike
// CreateSession, the studio front-desk beat below).
//
// CreateInstructor / TombstoneInstructor are operator-only (mirrors
// clinic-domain's CreateProvider / TombstoneProvider — entity provisioning
// stays a trusted-tool ceremony). BindInstructorIdentity additionally grants
// `frontOfHouse` at scope=any — the staff-run bind ceremony that establishes
// an instructor's login, mirroring clinic-domain's BindProviderIdentity
// grant verbatim (the op's own CreateOnly guards on both sides already make
// a bind mutually exclusive regardless of who submits it).
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
		mk("CreateStudio"),
		mk("TombstoneStudio"),
		{
			OperationType: "CreateSession",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to submit CreateSession (schedules a class on a studio's grid) — the studio front-desk beat.",
			GrantsTo:      []string{"operator", "frontOfHouse"},
		},
		{
			OperationType: "TombstoneSession",
			Scope:         "any",
			Note:          "Grants the operator the right to submit TombstoneSession operations, and a bound instructor the right to cancel a class THEY lead (the script's standing guard confines a non-operator caller to the session it is ledBy-bound to via its own instructor identifiedBy binding).",
			GrantsTo:      []string{"operator", "provider"},
		},
		mk("CreateBooking"),
		{
			OperationType: "CreateBooking",
			Scope:         "self",
			Note:          "Grants a consumer the right to book a class for THEMSELVES (the booking's booker must be the caller's own identity).",
			GrantsTo:      []string{"consumer"},
		},
		mk("CancelBooking"),
		{
			OperationType: "CancelBooking",
			Scope:         "self",
			Note:          "Grants a consumer the right to cancel THEIR OWN booking (the booking's bookedBy identity must be the caller's own identity).",
			GrantsTo:      []string{"consumer"},
		},
		mk("CreateInstructor"),
		mk("TombstoneInstructor"),
		{
			OperationType: "BindInstructorIdentity",
			Scope:         "any",
			Note:          "Grants the operator alone the right to bind an existing instructor to its login identity. The bind mints the identity-domain `provider` role; it is operator-only to match its precondition CreateInstructor (also operator-only) and to keep the role-minting ceremony off the front-desk grant, mirroring clinic-domain BindProviderIdentity.",
			GrantsTo:      []string{"operator"},
		},
	}
}
