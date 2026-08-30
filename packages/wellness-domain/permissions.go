package wellnessdomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions grants every wellness-domain op to the `operator` role (scope
// any) — the trusted-tool operator already holds standing permission, no new
// capability surface. CreateBooking, JoinWaitlist and CancelBooking ALSO
// grant `consumer`, scope=self (real-actor-write-auth-e2e idiom, clinic-
// domain's CreateAppointment/RescheduleAppointment/SetAppointmentStatus
// precedent): a real resident books, joins a waitlist for, or cancels their
// OWN class through the Gateway. `authContext.target == actor` is checked at
// step 3 (Contract #6); the Starlark script separately requires the
// booking's actual booker (CreateBooking's/JoinWaitlist's payload.booker;
// CancelBooking's bookedBy link) to BE that target identity — simpler than
// clinic's patient/identifiedBy indirection, since a wellness booking's
// booker IS an identity directly, not a business vertex a linked identity
// must resolve through.
//
// CreateStudio, CreateSession, CreateSessionSeries, CreateBooking,
// JoinWaitlist and CancelBooking additionally grant `frontOfHouse` at
// scope=any — the studio front-desk beat: opening a studio, scheduling a
// class (or a whole recurring series of them), booking a member in or
// waitlisting them, and releasing a member's seat. scope=any carries no
// platform-checked target, so each of those scripts
// binds the standing path itself with the workplace walk (`require_workplace`):
// a non-operator caller must hold a `worksAt` link covering a location the
// TARGET sits at — the studio's own `locatedAt` for a studio-scoped op, and
// `session -atStudio-> studio -locatedAt-> location` for the two booking ops.
// The location is never taken from the caller's word: CreateStudio guards on
// the location it is about to link, and an unlocated studio therefore stays
// operator-only. This is the same confinement CreateSession already carries,
// and it leaves the scope=self consumer path untouched — `require_workplace`
// returns early on `op.authTargetValidated`, so a member still books and
// cancels their own seat while holding no `worksAt` link at all.
//
// ReassignSession grants [operator, frontOfHouse, provider] — the union of
// CreateSession's staff-scheduling grant and TombstoneSession's bound-
// instructor grant, since editing a live session in place is the same
// administrative surface as either half of cancel-and-recreate. A
// front-of-house caller is workplace-confined exactly like CreateSession
// (worksAt a location the studio sits at); a provider-role caller is
// confined by the script to a session it is ledBy-bound to, exactly like
// TombstoneSession.
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
// SetBookingAttendance additionally grants `frontOfHouse` at scope=any,
// workplace-confined exactly like CancelBooking (`session -atStudio->
// studio -locatedAt-> location`, checked against the caller's `worksAt`):
// a provider-role caller is still confined by the script to bookings on a
// session it is ledBy-bound to (who actually showed is the judgement of
// whoever ran the class), while a front-of-house caller marks attendance for
// any booking at a studio they staff — the same staff/instructor split
// ReassignSession already draws.
//
// CreateInstructor / TombstoneInstructor are operator-only (mirrors
// clinic-domain's CreateProvider / TombstoneProvider — entity provisioning
// stays a trusted-tool ceremony). BindInstructorIdentity is operator-only
// too, mirroring clinic-domain's BindProviderIdentity: the bind mints the
// identity-domain `provider` role, so it matches its operator-only
// precondition CreateInstructor (front-of-house cannot create the instructor
// entity a bind would target) and keeps role-minting off the front-desk
// grant.
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
		{
			OperationType: "CreateStudio",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to submit CreateStudio (opens a studio at a location) — the script confines a non-operator caller to a location they worksAt, and a studio named with no location stays operator-only (an empty candidate list denies).",
			GrantsTo:      []string{"operator", "frontOfHouse"},
		},
		mk("TombstoneStudio"),
		{
			OperationType: "CreateSession",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to submit CreateSession (schedules a class on a studio's grid) — the studio front-desk beat.",
			GrantsTo:      []string{"operator", "frontOfHouse"},
		},
		{
			OperationType: "CreateSessionSeries",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to submit CreateSessionSeries (schedules occurrenceCount occurrences of a recurring class on a studio's grid in one atomic op) — the same studio front-desk beat as CreateSession, confined by the same in-script workplace walk.",
			GrantsTo:      []string{"operator", "frontOfHouse"},
		},
		{
			OperationType: "TombstoneSession",
			Scope:         "any",
			Note:          "Grants the operator the right to submit TombstoneSession operations, and a bound instructor the right to cancel a class THEY lead (the script's standing guard confines a non-operator caller to the session it is ledBy-bound to via its own instructor identifiedBy binding).",
			GrantsTo:      []string{"operator", "provider"},
		},
		{
			OperationType: "ReassignSession",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to submit ReassignSession (edits a live class's instructor and/or time without cancelling its bookings), and a bound instructor the right to reassign/reschedule a class THEY lead — the script confines a non-operator, non-front-of-house caller to the session it is ledBy-bound to via its own instructor identifiedBy binding, and a front-of-house caller to a studio at a location they worksAt. Moving a session to a DIFFERENT studio (newStudio) is operator-only regardless of hat, including the repair path that omits `studio` for a session whose own studio was already tombstoned — the script re-derives it off the still-live atStudio link.",
			GrantsTo:      []string{"operator", "frontOfHouse", "provider"},
		},
		{
			OperationType: "CreateBooking",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to book a class for a member — the script confines a non-operator caller to a session whose studio sits at a location they worksAt.",
			GrantsTo:      []string{"operator", "frontOfHouse"},
		},
		{
			OperationType: "CreateBooking",
			Scope:         "self",
			Note:          "Grants a consumer the right to book a class for THEMSELVES (the booking's booker must be the caller's own identity).",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "JoinWaitlist",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to join a member onto a full class's waitlist — the same session-workplace confinement CreateBooking's any-scope grant carries (prepare_booking_common, ddls.go).",
			GrantsTo:      []string{"operator", "frontOfHouse"},
		},
		{
			OperationType: "JoinWaitlist",
			Scope:         "self",
			Note:          "Grants a consumer the right to join a full class's waitlist for THEMSELVES (the waitlisted booking's booker must be the caller's own identity) — the mirror of CreateBooking's self-scope grant.",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "CancelBooking",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to cancel a member's seat OR waitlist slot — the script confines a non-operator caller to a session whose studio sits at a location they worksAt, after binding the supplied session to the booking. Cancelling a booked seat may promote the session's earliest waitlisted booking into it.",
			GrantsTo:      []string{"operator", "frontOfHouse"},
		},
		{
			OperationType: "CancelBooking",
			Scope:         "self",
			Note:          "Grants a consumer the right to cancel THEIR OWN booking or waitlist slot (the booking's bookedBy identity must be the caller's own identity).",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "SetBookingAttendance",
			Scope:         "any",
			Note:          "Grants the operator the right to submit SetBookingAttendance operations, a bound instructor the right to record attendance on a booking for a class THEY lead (the script's standing guard confines a non-operator, non-workplace caller to the session it is ledBy-bound to via its own instructor identifiedBy binding), and front-of-house staff the right to record attendance for any booking at a studio they worksAt (the script confines them to the session's studio location, mirroring CancelBooking).",
			GrantsTo:      []string{"operator", "provider", "frontOfHouse"},
		},
		mk("CreateInstructor"),
		mk("TombstoneInstructor"),
		{
			OperationType: "SetInstructorProfile",
			Scope:         "any",
			Note:          "Grants the operator the right to submit SetInstructorProfile operations, and a bound instructor the right to edit THEIR OWN profile (the script's standing guard confines a non-operator caller to the instructor it is identifiedBy-bound to). The `provider` role here is the generic one all three bind ops mint — a bound clinic provider and service provider hold it too, so the guard, not this grant, is what stops one archetype editing another's record.",
			GrantsTo:      []string{"operator", "provider"},
		},
		{
			OperationType: "BindInstructorIdentity",
			Scope:         "any",
			Note:          "Grants the operator alone the right to bind an existing instructor to its login identity. The bind mints the identity-domain `provider` role; it is operator-only to match its precondition CreateInstructor (also operator-only) and to keep the role-minting ceremony off the front-desk grant, mirroring clinic-domain BindProviderIdentity.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "ReleaseOrphanedBooking",
			Scope:         "any",
			Note:          "Grants the operator alone the right to release a booking orphaned by a called-off class. The Weaver service actor holds the operator role (bootstrap primordial grant) — this is the ONLY grant its wellnessOrphanedBookingSettlement directOp dispatch (targets.go) needs; no consumer/front-of-house path exists because the op carries no caller-supplied session to bind a self- or workplace-scope guard to.",
			GrantsTo:      []string{"operator"},
		},
	}
}
