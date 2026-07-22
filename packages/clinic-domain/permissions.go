package clinicdomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions grants every clinic-domain op to the `operator` role (scope any).
// The role canonical name `operator` is resolved by cmd/lattice-pkg to the seeded
// NanoID from lattice.bootstrap.json — identical to loftspace-domain. No new
// capability surface: the trusted-tool operator already holds standing permission.
//
// CreateAppointment, RescheduleAppointment, and SetAppointmentStatus ALSO grant
// `consumer`, scope=self (real-actor-write-auth-e2e idiom, lease-signing's
// CreateLeaseApplication precedent): a real patient books, reschedules, or
// cancels their OWN appointment through the Gateway. `authContext.target ==
// actor` is checked at step 3 (Contract #6); the Starlark script separately
// requires the target identity to be the appointment's actual patient's linked
// identity (via the patient's identifiedBy link), since step 3 never sees the
// payload and the patient vertex — not the identity — is the op's endpoint.
// SetAppointmentStatus's self grant is further restricted, in-script, to
// status=cancelled only — a self-service patient may cancel but never mark
// confirmed/checkedIn/completed/noShow (those stay staff-only).
//
// RescheduleAppointment and SetAppointmentStatus additionally grant
// `frontOfHouse` at scope=any — the front-desk schedule beat. This is the
// staff surface a real clinic front desk needs (move an appointment, mark a
// patient checked-in / no-show) and it is strictly NARROWER than the posture
// those staff hold today: the shipped clinic FE submits as `operator`, which
// is root-equivalent. `frontOfHouse` holds no kernel platform grants and no
// all-access read anchor, so a front-desk actor reaches exactly these ops and
// the rows their workplace grants open. Booking (CreateAppointment) and the
// clinical/roster surface (RecordEncounter, CreatePatient, provider
// administration) deliberately stay operator-only.
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
		mk("CreatePatient"),
		mk("TombstonePatient"),
		mk("CreateProvider"),
		mk("TombstoneProvider"),
		mk("SetProviderProfile"),
		mk("SetProviderHours"),
		mk("SetProviderTimeOff"),
		mk("CreateAppointment"),
		{
			OperationType: "CreateAppointment",
			Scope:         "self",
			Note:          "Grants a consumer the right to book an appointment for THEMSELVES (payload.patient must be a patient linked, via identifiedBy, to the caller's own identity).",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "RescheduleAppointment",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to submit RescheduleAppointment operations.",
			GrantsTo:      []string{"operator", "frontOfHouse"},
		},
		{
			OperationType: "RescheduleAppointment",
			Scope:         "self",
			Note:          "Grants a consumer the right to reschedule THEIR OWN appointment (the appointment's forPatient must be a patient linked, via identifiedBy, to the caller's own identity).",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "SetAppointmentStatus",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to submit SetAppointmentStatus operations (the full transition surface, not the consumer cancel-slice).",
			GrantsTo:      []string{"operator", "frontOfHouse"},
		},
		{
			OperationType: "SetAppointmentStatus",
			Scope:         "self",
			Note:          "Grants a consumer the right to cancel THEIR OWN appointment (status=cancelled only; the appointment's forPatient must be a patient linked, via identifiedBy, to the caller's own identity).",
			GrantsTo:      []string{"consumer"},
		},
		mk("RecordEncounter"),
		mk("TombstoneAppointment"),
		mk("SetSiteProfile"),
		mk("AssignProviderSite"),
		mk("RemoveProviderSite"),
	}
}
