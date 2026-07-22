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
		mk("TombstoneSession"),
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
	}
}
