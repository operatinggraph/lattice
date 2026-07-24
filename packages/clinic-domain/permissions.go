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
//
// BindProviderIdentity grants `operator` alone at scope=any — the bind
// ceremony that establishes a provider's login (mints the provider's
// identifiedBy link and idempotently grants the identity-domain `provider`
// role). Operator-only because the bind confers the role-minting power plus,
// through the provider guards' deliberate lack of a worksAt check,
// cross-building appointment access; delegating it to front-desk would let a
// front-desk actor bind an unbound provider at another building and escalate
// past workplace confinement. It matches CreateProvider (also operator-only):
// front-desk cannot create the provider entity a bind targets, so the grant
// buys only attack surface. Not further narrowed to scope=self: the op's own
// CreateOnly guards on BOTH sides make a bind mutually exclusive regardless of
// who submits it (persona-worlds-design.md Fire W0 §3.2).
//
// SetProviderHours, SetProviderTimeOff, SetAppointmentStatus, and
// RescheduleAppointment additionally grant `provider` at scope=any: a bound
// provider manages their OWN availability and their OWN appointments. Scope
// stays `any` (there is no scope=self equivalent for a non-identity target
// vertex like provider/appointment), so the Starlark script itself confines
// a provider-role, non-operator, non-workplace caller to the SPECIFIC
// provider it is identifiedBy-bound to — a THIRD standing binder alongside
// require_workplace's operator/worksAt pair (facet-staff-worlds-design.md
// §3.5 frames those two as complementary; this is the provider-hat leg).
// `provider` widens the EXISTING scope=any row on each of these four ops
// (never a second row): a permission's identity is its (operationType,
// scope) pair (Contract #8 §8.1 permTag — validatePermissionIdentityUniqueness
// rejects a duplicate before any KV write), so a second scope=any row for an
// op that already has one would collapse onto the SAME vtx.permission.<id>
// key as the first — silently dropping whichever grantsTo lost the race,
// never landing two grants. CreateAppointment's consumer widening (below)
// only looks like a second row because it sits at a DIFFERENT scope (self);
// BindProviderIdentity is a brand-new op, so its one row cannot collide with
// anything.
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
			OperationType: "CreatePatient",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to register a patient. Unconfined: a patient vertex is practice-wide (no building), so there is no location to workplace-confine to — front-desk registration mirrors the operator grant and identity-domain's own frontOfHouse CreateUnclaimedIdentity.",
			GrantsTo:      []string{"operator", "frontOfHouse"},
		},
		mk("TombstonePatient"),
		mk("CreateProvider"),
		mk("TombstoneProvider"),
		mk("SetProviderProfile"),
		{
			OperationType: "SetProviderHours",
			Scope:         "any",
			Note:          "Grants the operator the right to submit SetProviderHours operations, and a bound provider the right to set THEIR OWN working hours (the script's standing guard confines a non-operator caller to the provider it is identifiedBy-bound to).",
			GrantsTo:      []string{"operator", "provider"},
		},
		{
			OperationType: "SetProviderTimeOff",
			Scope:         "any",
			Note:          "Grants the operator the right to submit SetProviderTimeOff operations, and a bound provider the right to set THEIR OWN time off (the script's standing guard confines a non-operator caller to the provider it is identifiedBy-bound to).",
			GrantsTo:      []string{"operator", "provider"},
		},
		{
			OperationType: "CreateAppointment",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to book an appointment on behalf of a patient. The script's standing workplace guard confines a non-operator caller to providers practising at a building it worksAt (mirrors RescheduleAppointment / SetAppointmentStatus) — a front-desk actor cannot book with a provider at a building it does not staff.",
			GrantsTo:      []string{"operator", "frontOfHouse"},
		},
		{
			OperationType: "CreateAppointment",
			Scope:         "self",
			Note:          "Grants a consumer the right to book an appointment for THEMSELVES (payload.patient must be a patient linked, via identifiedBy, to the caller's own identity).",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "RescheduleAppointment",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to submit RescheduleAppointment operations, and a bound provider the right to reschedule THEIR OWN appointments (the script's standing guard confines a non-operator, non-workplace caller to appointments withProvider the provider it is identifiedBy-bound to).",
			GrantsTo:      []string{"operator", "frontOfHouse", "provider"},
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
			Note:          "Grants the operator and front-of-house staff the right to submit SetAppointmentStatus operations (the full transition surface, not the consumer cancel-slice), and a bound provider the right to set status on THEIR OWN appointments (the script's standing guard confines a non-operator, non-workplace caller to appointments withProvider the provider it is identifiedBy-bound to).",
			GrantsTo:      []string{"operator", "frontOfHouse", "provider"},
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
		{
			OperationType: "BindProviderIdentity",
			Scope:         "any",
			Note:          "Grants the operator alone the right to bind an existing provider to its login identity. The bind mints the identity-domain `provider` role and, via the provider guards' deliberate lack of a worksAt check, confers cross-building appointment access — a privilege escalation past workplace confinement if delegated to front-desk. It is operator-only to match its precondition CreateProvider (also operator-only): front-desk cannot create the provider entity a bind would target, so the grant would add only attack surface, never a standalone workflow.",
			GrantsTo:      []string{"operator"},
		},
	}
}
