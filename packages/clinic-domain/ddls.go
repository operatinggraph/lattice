package clinicdomain

import "github.com/asolgan/lattice/internal/pkgmgr"

// Canonical names. Three vertexType DDLs own the op scripts (each op is admitted
// by EXACTLY ONE vertexType DDL — the operationType→script index drops an op
// claimed by two, so no overlap is allowed there). Four aspectType DDLs are
// step-6 write gates only (the Processor keys permittedCommands on the MUTATION
// document's class; aspectType DDLs are excluded from script selection), mirroring
// loftspace-domain's listing/address split.
//
// Aspect classes are clinic-namespaced (patientDemographics, providerProfile,
// appointmentSchedule, appointmentStatus) so the globally-unique canonicalName
// namespace (Contract #1 §1.5) is not polluted by generic words like "status" /
// "profile". The aspect's LOCAL NAME (the key segment a lens hops, e.g.
// vtx.patient.<id>.demographics) stays clean — the executor resolves
// node.<localName>.data.<field> by the key segment, independent of the class — so
// the lenses read p.demographics / a.status while the gate keys on the namespaced
// class.
const (
	patientVertexDDL     = "patient"
	providerVertexDDL    = "provider"
	appointmentVertexDDL = "appointment"

	demographicsAspectDDL = "patientDemographics"
	profileAspectDDL      = "providerProfile"
	scheduleAspectDDL     = "appointmentSchedule"
	statusAspectDDL       = "appointmentStatus"
	bookingsAspectDDL     = "providerBookings"
	hoursAspectDDL        = "providerHours"
)

// DDLs returns the package's seven DDL meta-vertex declarations:
//
//   - patient (vertexType) — owns CreatePatient + TombstonePatient.
//   - provider (vertexType) — owns CreateProvider + TombstoneProvider.
//   - appointment (vertexType) — owns CreateAppointment + SetAppointmentStatus +
//     TombstoneAppointment.
//   - patientDemographics / providerProfile / appointmentSchedule /
//     appointmentStatus (aspectType) — step-6 write gates for the four aspects.
//
// Architectural rules (binding — the known-key discipline of location-domain /
// loftspace-domain):
//
//   - The scripts read ONLY by known key. CreateAppointment validates BOTH link
//     endpoints (patient + provider) by the keys the caller lists in
//     ContextHint.Reads; the Tombstone/SetAppointmentStatus ops validate their
//     target by its key. No prefix scans, no adjacency lookups, no lens reads.
//   - CreateAppointment's endpoints MUST be alive AND the right class (patient /
//     provider): a dead or wrong-class endpoint is never wired (structured
//     ScriptError) — endpoint-class validation is at the op, not a downstream
//     untyped cypher match.
//
// Every aspect is NON-sensitive: patient demographics (incl. DOB) attach to a
// vtx.patient — NOT an identity — so step-6's sensitiveAspectScope (which forbids
// a sensitive aspect on a non-identity vertex) would REJECT a sensitive aspect
// here anyway. Real PHI handling + right-to-be-forgotten is the deferred Vault
// plane (clinic is its forcing function); these plain aspects are correct under
// the trusted-tool posture (no read-path auth yet).
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		patientVertexTypeDDL(),
		providerVertexTypeDDL(),
		appointmentVertexTypeDDL(),
		demographicsAspectTypeDDL(),
		profileAspectTypeDDL(),
		scheduleAspectTypeDDL(),
		statusAspectTypeDDL(),
		bookingsAspectTypeDDL(),
		hoursAspectTypeDDL(),
	}
}

func patientVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     patientVertexDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreatePatient", "TombstonePatient"},
		Description: "Clinic patient DDL. Vertex shape: vtx.patient.<NanoID>, class=patient, root data = {} " +
			"(minimal, D5 — the data lives in the .demographics aspect). CreatePatient mints the patient + writes " +
			"the .demographics aspect {fullName (required), dob?, email?, phone?} atomically. TombstonePatient " +
			"soft-deletes one. The aspect is NON-sensitive (it attaches to a patient, not an identity); real PHI " +
			"handling is the deferred Vault plane.",
		Script: patientDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"fullName":{"type":"string","description":"The patient's full name (CreatePatient; required)."},` +
			`"dob":{"type":"string","description":"Date of birth, RFC3339 date (CreatePatient; optional)."},` +
			`"email":{"type":"string","description":"Contact email (CreatePatient; optional)."},` +
			`"phone":{"type":"string","description":"Contact phone (CreatePatient; optional)."},` +
			`"patientId":{"type":"string","description":"Optional bare NanoID for the new patient vertex (CreatePatient); absent → minted."},` +
			`"patientKey":{"type":"string","description":"vtx.patient.<NanoID> of an existing patient (TombstonePatient; required, validated alive)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.patient.<NanoID> the operation wrote."}}}`,
		FieldDescription: map[string]string{
			"fullName":   "The patient's full name. Stored on the .demographics aspect (CreatePatient; required).",
			"dob":        "Optional date of birth (RFC3339 date). Stored on the .demographics aspect when present.",
			"email":      "Optional contact email. Stored on the .demographics aspect when present.",
			"phone":      "Optional contact phone. Stored on the .demographics aspect when present.",
			"patientId":  "Optional bare NanoID (no dots / key segments) for the new patient vertex (vtx.patient.<patientId>). Absent → minted with nanoid.new().",
			"patientKey": "Full vtx.patient.<NanoID> key of an existing patient vertex to tombstone (TombstonePatient).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "CreatePatient — register a patient",
				Payload: map[string]any{"fullName": "Alice Rivera", "dob": "1990-04-12T00:00:00Z", "email": "alice@example.com"},
				ExpectedOutcome: "Mints vtx.patient.<NanoID> (class=patient, root {}) + the .demographics aspect " +
					"{fullName, dob, email}. Accepts an optional bare-NanoID patientId. Returns primaryKey (the patient key).",
			},
			{
				Name:            "TombstonePatient — remove a patient",
				Payload:         map[string]any{"patientKey": "vtx.patient.<NanoID>"},
				ExpectedOutcome: "Soft-deletes the patient vertex. Returns primaryKey. Rejects an absent / already-dead patient.",
			},
		},
	}
}

func providerVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     providerVertexDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateProvider", "TombstoneProvider", "SetProviderHours"},
		Description: "Clinic provider DDL. Vertex shape: vtx.provider.<NanoID>, class=provider, root data = {} " +
			"(minimal, D5 — the data lives in the .profile aspect). CreateProvider mints the provider + writes the " +
			".profile aspect {fullName (required), specialty (required), credentials?, bio?} atomically. " +
			"TombstoneProvider soft-deletes one. SetProviderHours upserts the .hours availability aspect " +
			"{windows: [{day (0=Sun..6=Sat), openSec, closeSec}]} (UTC seconds-of-day) — the opt-in business-hours " +
			"windows CreateAppointment / RescheduleAppointment enforce (an out-of-hours booking is rejected " +
			"OutsideHours); an absent .hours aspect or windows=[] means the provider is unconstrained.",
		Script: providerDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"fullName":{"type":"string","description":"The provider's full name (CreateProvider; required)."},` +
			`"specialty":{"type":"string","description":"The provider's clinical specialty, e.g. Cardiology (CreateProvider; required)."},` +
			`"credentials":{"type":"string","description":"Post-nominal credentials, e.g. MD (CreateProvider; optional)."},` +
			`"bio":{"type":"string","description":"Short provider bio (CreateProvider; optional)."},` +
			`"providerId":{"type":"string","description":"Optional bare NanoID for the new provider vertex (CreateProvider); absent → minted."},` +
			`"providerKey":{"type":"string","description":"vtx.provider.<NanoID> of an existing provider (TombstoneProvider / SetProviderHours; required, validated alive)."},` +
			`"windows":{"type":"array","description":"Availability windows (SetProviderHours; required). Each {day:0-6 (Sun=0), openSec:0-86400, closeSec:0-86400} with openSec<closeSec; UTC seconds-of-day. An empty array clears the constraint.","items":{"type":"object","properties":{"day":{"type":"integer"},"openSec":{"type":"integer"},"closeSec":{"type":"integer"}}}}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.provider.<NanoID> the operation wrote."}}}`,
		FieldDescription: map[string]string{
			"fullName":    "The provider's full name. Stored on the .profile aspect (CreateProvider; required).",
			"specialty":   "The provider's clinical specialty (e.g. Cardiology). Stored on the .profile aspect (CreateProvider; required).",
			"credentials": "Optional post-nominal credentials (e.g. MD, RN). Stored on the .profile aspect when present.",
			"bio":         "Optional short provider bio. Stored on the .profile aspect when present.",
			"providerId":  "Optional bare NanoID (no dots / key segments) for the new provider vertex. Absent → minted with nanoid.new().",
			"providerKey": "Full vtx.provider.<NanoID> key of an existing provider vertex (TombstoneProvider tombstones it; SetProviderHours sets its availability).",
			"windows":     "Availability windows (SetProviderHours). A list of {day:0-6 (Sun=0), openSec, closeSec} where openSec/closeSec are UTC seconds-of-day (0..86400) and openSec<closeSec. An empty list clears the constraint (provider becomes unconstrained).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "CreateProvider — register a provider",
				Payload: map[string]any{"fullName": "Dr. Sam Okafor", "specialty": "Cardiology", "credentials": "MD"},
				ExpectedOutcome: "Mints vtx.provider.<NanoID> (class=provider, root {}) + the .profile aspect " +
					"{fullName, specialty, credentials}. Returns primaryKey (the provider key).",
			},
			{
				Name: "SetProviderHours — Mon/Wed 09:00–17:00 UTC",
				Payload: map[string]any{
					"providerKey": "vtx.provider.<NanoID>",
					"windows": []any{
						map[string]any{"day": 1, "openSec": 32400, "closeSec": 61200},
						map[string]any{"day": 3, "openSec": 32400, "closeSec": 61200},
					},
				},
				ExpectedOutcome: "Validates the provider is alive + class=provider and each window (day 0-6, " +
					"0<=openSec<closeSec<=86400), then upserts vtx.provider.<NanoID>.hours {windows}. Subsequent " +
					"CreateAppointment / RescheduleAppointment reject a booking outside these windows (OutsideHours). " +
					"windows=[] clears the constraint.",
			},
		},
	}
}

func appointmentVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     appointmentVertexDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateAppointment", "RescheduleAppointment", "SetAppointmentStatus", "TombstoneAppointment"},
		Description: "Clinic appointment DDL. Vertex shape: vtx.appointment.<NanoID>, class=appointment, root data = " +
			"{} (minimal, D5). CreateAppointment validates the patient (class=patient) + provider (class=provider) " +
			"are alive, then atomically mints the appointment + the .schedule aspect {startsAt, endsAt, remindAt, reason?} + " +
			"the .status aspect {value: scheduled} + the forPatient link (appointment→patient) + the withProvider " +
			"link (appointment→provider). Both links follow Contract #1 §1.1 (the later-arriving appointment is the " +
			"source). RescheduleAppointment rewrites the .schedule aspect with new startsAt/endsAt (re-deriving " +
			"remindAt = startsAt − 24h so the clinic-reminders @at re-arms for a not-yet-sent reminder), leaving the " +
			"links + status untouched; an omitted reason clears it (the caller carries the existing reason). " +
			"SetAppointmentStatus upserts the .status aspect to one of {scheduled, confirmed, checkedIn, completed, " +
			"cancelled, noShow}, with an optional audit note (a cancel / no-show reason, stored on .status distinct " +
			"from the .schedule visit reason). TombstoneAppointment soft-deletes the appointment. CreateAppointment AND " +
			"RescheduleAppointment REJECT a double-book (SlotConflict): each reads the provider's .bookings index (a " +
			"declared, OCC-snapshotted contextHint.reads key) and kv.Reads every live candidate's schedule + status, " +
			"failing on an overlap with a still scheduled / confirmed appointment (reschedule skips the appointment being " +
			"moved) (Capability-KV §06 — enforced via the op's own Starlark logic). RescheduleAppointment therefore " +
			"requires the provider key. Both also enforce the provider's opt-in availability windows (the .hours aspect, " +
			"set by SetProviderHours): a booking outside a provider's business hours is rejected (OutsideHours); a provider " +
			"with no .hours is unconstrained.",
		Script: appointmentDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"patient":{"type":"string","description":"vtx.patient.<NanoID> the appointment is for (CreateAppointment; required, validated alive + class=patient)."},` +
			`"provider":{"type":"string","description":"vtx.provider.<NanoID> the appointment is with (CreateAppointment / RescheduleAppointment; required, validated alive + class=provider; on reschedule it must be the appointment's actual provider — list provider+'.bookings' in contextHint.reads for the conflict check)."},` +
			`"startsAt":{"type":"string","description":"Appointment start, RFC3339 (CreateAppointment / RescheduleAppointment; required). Caller supplies canonical UTC."},` +
			`"endsAt":{"type":"string","description":"Appointment end, RFC3339 (CreateAppointment / RescheduleAppointment; required). Caller supplies canonical UTC."},` +
			`"reason":{"type":"string","description":"Visit reason / chief complaint (CreateAppointment / RescheduleAppointment; optional — on RescheduleAppointment an omitted reason clears it)."},` +
			`"appointmentId":{"type":"string","description":"Optional bare NanoID for the new appointment vertex (CreateAppointment); absent → minted."},` +
			`"appointmentKey":{"type":"string","description":"vtx.appointment.<NanoID> of an existing appointment (RescheduleAppointment / SetAppointmentStatus / TombstoneAppointment; required, validated alive)."},` +
			`"status":{"type":"string","enum":["scheduled","confirmed","checkedIn","completed","cancelled","noShow"],"description":"New status (SetAppointmentStatus; required)."},` +
			`"note":{"type":"string","description":"Optional audit note for the transition, e.g. a cancel / no-show reason (SetAppointmentStatus; optional). Stored on .status, distinct from the .schedule visit reason; an omitted note carries none."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.appointment.<NanoID> the operation wrote."}}}`,
		FieldDescription: map[string]string{
			"patient":        "Full vtx.patient.<NanoID> key the appointment is for. CreateAppointment validates it is alive + class=patient and writes the forPatient link (appointment→patient). The caller MUST list this key in ContextHint.Reads.",
			"provider":       "Full vtx.provider.<NanoID> key the appointment is with. CreateAppointment validates it is alive + class=provider and writes the withProvider link (appointment→provider). RescheduleAppointment also requires it (the appointment's actual provider, validated via the withProvider link) to conflict-check the new time. The caller MUST list this key — and, for reschedule, provider+'.bookings' — in ContextHint.Reads.",
			"startsAt":       "Appointment start (RFC3339, canonical UTC). Stored on the .schedule aspect (CreateAppointment / RescheduleAppointment; required).",
			"endsAt":         "Appointment end (RFC3339, canonical UTC). Stored on the .schedule aspect (CreateAppointment / RescheduleAppointment; required).",
			"reason":         "Optional visit reason / chief complaint. Stored on the .schedule aspect when present (CreateAppointment / RescheduleAppointment; on RescheduleAppointment an omitted reason clears it).",
			"appointmentId":  "Optional bare NanoID (no dots / key segments) for the new appointment vertex. Absent → minted with nanoid.new().",
			"appointmentKey": "Full vtx.appointment.<NanoID> key of an existing appointment (RescheduleAppointment rewrites its .schedule; SetAppointmentStatus validates it alive + class=appointment; TombstoneAppointment validates it alive).",
			"status":         "New appointment status, one of {scheduled, confirmed, checkedIn, completed, cancelled, noShow} (SetAppointmentStatus; required).",
			"note":           "Optional audit note recorded with a SetAppointmentStatus transition (e.g. a cancel / no-show reason). Stored on the .status aspect, distinct from the .schedule visit reason; omitted → no note.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "CreateAppointment — book a patient with a provider",
				Payload: map[string]any{
					"patient":  "vtx.patient.<patientNanoID>",
					"provider": "vtx.provider.<providerNanoID>",
					"startsAt": "2026-07-01T15:00:00Z",
					"endsAt":   "2026-07-01T15:30:00Z",
					"reason":   "Annual checkup",
				},
				ExpectedOutcome: "Validates the patient (class=patient) + provider (class=provider) are alive. Atomically " +
					"commits vtx.appointment.<NanoID> (root {}) + .schedule {startsAt, endsAt, remindAt, reason} (remindAt = " +
					"startsAt − 24h, derived) + .status {value: " +
					"scheduled} + the forPatient + withProvider links. Returns primaryKey (the appointment key). Rejects with " +
					"ScriptError if the patient or provider is absent / dead / the wrong class. Does NOT check for slot " +
					"conflicts (D6 — deferred).",
			},
			{
				Name: "RescheduleAppointment — move an appointment to a new time",
				Payload: map[string]any{
					"appointmentKey": "vtx.appointment.<NanoID>",
					"startsAt":       "2026-07-02T16:00:00Z",
					"endsAt":         "2026-07-02T16:30:00Z",
					"reason":         "Annual checkup",
				},
				ExpectedOutcome: "Validates the appointment is alive + class=appointment, then rewrites the .schedule " +
					"aspect {startsAt, endsAt, remindAt, reason?} with the new times — re-deriving remindAt = startsAt − 24h " +
					"(canonical UTC) so the clinic-reminders @at re-arms for a not-yet-sent reminder. The forPatient / " +
					"withProvider links + the .status aspect are untouched. An omitted reason clears it (the caller carries " +
					"the existing reason). Returns primaryKey. Does NOT check slot conflicts (D6 — deferred).",
			},
			{
				Name:    "SetAppointmentStatus — confirm an appointment",
				Payload: map[string]any{"appointmentKey": "vtx.appointment.<NanoID>", "status": "confirmed"},
				ExpectedOutcome: "Validates the appointment is alive + class=appointment, then upserts the .status aspect " +
					"{value: confirmed} (unconditioned — re-runnable). Returns primaryKey. Rejects a status outside the enum.",
			},
		},
	}
}

// demographicsAspectTypeDDL declares the .demographics aspect (class
// patientDemographics) — the step-6 write gate for CreatePatient. Declaration-only
// (the script lives on the patient vertexType DDL). NON-sensitive: it attaches to
// a vtx.patient, not an identity.
func demographicsAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     demographicsAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreatePatient"},
		Description: "Patient demographics aspect (clinic). Stored as vtx.patient.<NanoID>.demographics (class " +
			"patientDemographics) = {fullName, dob?, email?, phone?}. NON-sensitive (it attaches to a patient, not " +
			"an identity — real PHI handling is the deferred Vault plane). Written ONLY by CreatePatient (whose " +
			"patient vertexType DDL owns the script); this aspect-type DDL is the step-6 write gate. " +
			"Declaration-only: no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"fullName":{"type":"string"},"dob":{"type":"string"},"email":{"type":"string"},"phone":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"fullName": "The patient's full name.",
			"dob":      "Date of birth (RFC3339 date).",
			"email":    "Contact email.",
			"phone":    "Contact phone.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "patient demographics aspect",
				Payload:         map[string]any{"fullName": "Alice Rivera", "dob": "1990-04-12T00:00:00Z"},
				ExpectedOutcome: "Stored as vtx.patient.<NanoID>.demographics; written by CreatePatient.",
			},
		},
	}
}

// profileAspectTypeDDL declares the .profile aspect (class providerProfile) — the
// step-6 write gate for CreateProvider. Declaration-only; NON-sensitive.
func profileAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     profileAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateProvider"},
		Description: "Provider profile aspect (clinic). Stored as vtx.provider.<NanoID>.profile (class " +
			"providerProfile) = {fullName, specialty, credentials?, bio?}. Non-sensitive. Written ONLY by " +
			"CreateProvider (whose provider vertexType DDL owns the script); this aspect-type DDL is the step-6 " +
			"write gate. Declaration-only: no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"fullName":{"type":"string"},"specialty":{"type":"string"},"credentials":{"type":"string"},"bio":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"fullName":    "The provider's full name.",
			"specialty":   "The provider's clinical specialty.",
			"credentials": "Post-nominal credentials.",
			"bio":         "Short provider bio.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "provider profile aspect",
				Payload:         map[string]any{"fullName": "Dr. Sam Okafor", "specialty": "Cardiology", "credentials": "MD"},
				ExpectedOutcome: "Stored as vtx.provider.<NanoID>.profile; written by CreateProvider.",
			},
		},
	}
}

// scheduleAspectTypeDDL declares the .schedule aspect (class appointmentSchedule)
// — the step-6 write gate for CreateAppointment. Declaration-only; NON-sensitive.
func scheduleAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     scheduleAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateAppointment", "RescheduleAppointment"},
		Description: "Appointment schedule aspect (clinic). Stored as vtx.appointment.<NanoID>.schedule (class " +
			"appointmentSchedule) = {startsAt, endsAt, remindAt, reason?}. Non-sensitive. Written by CreateAppointment " +
			"(initial) and RescheduleAppointment (new times) — whose appointment vertexType DDL owns the script; this " +
			"aspect-type DDL is the step-6 write gate. Declaration-only: no op handler. remindAt = startsAt − 24h is a " +
			"precomputed reminder deadline the " +
			"clinic-reminders package's convergence lens reads (it is not a caller input). CreateAppointment " +
			"conflict-checks the booking against the provider's .bookings index (double-book rejection) and the " +
			"provider's opt-in .hours availability windows (OutsideHours rejection).",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"startsAt":{"type":"string"},"endsAt":{"type":"string"},"remindAt":{"type":"string"},"reason":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"startsAt": "Appointment start (RFC3339).",
			"endsAt":   "Appointment end (RFC3339).",
			"remindAt": "Precomputed reminder deadline (RFC3339, canonical UTC) = startsAt − 24h. Derived by CreateAppointment, not a caller input; the clinic-reminders convergence lens projects it as freshUntil to arm the @at reminder timer.",
			"reason":   "Visit reason / chief complaint.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "appointment schedule aspect",
				Payload:         map[string]any{"startsAt": "2026-07-01T15:00:00Z", "endsAt": "2026-07-01T15:30:00Z", "remindAt": "2026-06-30T15:00:00Z", "reason": "Annual checkup"},
				ExpectedOutcome: "Stored as vtx.appointment.<NanoID>.schedule; written by CreateAppointment (which derives remindAt = startsAt − 24h).",
			},
		},
	}
}

// statusAspectTypeDDL declares the .status aspect (class appointmentStatus) — the
// step-6 write gate for CreateAppointment (initial) AND SetAppointmentStatus
// (transitions). Declaration-only; NON-sensitive.
func statusAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     statusAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateAppointment", "SetAppointmentStatus"},
		Description: "Appointment status aspect (clinic). Stored as vtx.appointment.<NanoID>.status (class " +
			"appointmentStatus) = {value ∈ scheduled|confirmed|checkedIn|completed|cancelled|noShow, note?}. " +
			"Non-sensitive. Written by CreateAppointment (initial scheduled) and SetAppointmentStatus (transitions, with " +
			"an optional audit note — a cancel / no-show reason, distinct from the .schedule visit reason) — whose " +
			"appointment vertexType DDL owns the script; this aspect-type DDL is the step-6 write gate. Declaration-only: " +
			"no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"value":{"type":"string","enum":["scheduled","confirmed","checkedIn","completed","cancelled","noShow"]},"note":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"value": "Appointment status: scheduled | confirmed | checkedIn | completed | cancelled | noShow.",
			"note":  "Optional audit note recorded with a status transition (e.g. a cancel / no-show reason).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "appointment status aspect",
				Payload:         map[string]any{"value": "confirmed"},
				ExpectedOutcome: "Stored as vtx.appointment.<NanoID>.status; written by CreateAppointment / SetAppointmentStatus.",
			},
		},
	}
}

// bookingsAspectTypeDDL declares the .bookings aspect (class providerBookings) —
// the per-provider appointment-index that op-time double-book detection reads. The
// step-6 write gate for CreateProvider (inits it empty) AND CreateAppointment
// (appends the new appointment, prunes terminal/tombstoned entries). Declaration-
// only; NON-sensitive. The index is a plain list of appointment keys, not inline
// intervals — CreateAppointment validates each candidate's LIVE schedule + status
// via kv.Read (§2.5), so a reschedule / cancel that does not maintain the index can
// never make it block a freed slot.
func bookingsAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     bookingsAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateProvider", "CreateAppointment", "RescheduleAppointment"},
		Description: "Provider bookings index aspect (clinic). Stored as vtx.provider.<NanoID>.bookings (class " +
			"providerBookings) = {appts: [vtx.appointment.<id>, ...]}. Non-sensitive. The per-provider appointment " +
			"adjacency CreateAppointment + RescheduleAppointment read (as a declared, OCC-snapshotted contextHint.reads " +
			"key) to detect a double-book before committing: each kv.Reads every candidate's live .schedule + .status, " +
			"rejects on an overlap with a still-scheduled/confirmed appointment (SlotConflict; reschedule skips the " +
			"appointment being moved), and rewrites the pruned index (tombstoned / terminal entries dropped to bound it). " +
			"Initialized empty by CreateProvider so the key is always present (a declared read of an absent key is a fatal " +
			"HydrationMiss). The OCC check on this aspect is the concurrency serialization point (two simultaneous bookings " +
			"or moves for one provider fail closed: the second commit RevisionConflicts, never a silent double-book). " +
			"Declaration-only: no op handler.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"appts":{"type":"array","items":{"type":"string"}}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"appts": "List of vtx.appointment.<NanoID> keys booked with this provider (the live, non-terminal book; terminal / tombstoned entries are pruned on the next CreateAppointment / RescheduleAppointment).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "provider bookings index aspect",
				Payload:         map[string]any{"appts": []any{"vtx.appointment.abc123", "vtx.appointment.def456"}},
				ExpectedOutcome: "Stored as vtx.provider.<NanoID>.bookings; initialized empty by CreateProvider, appended by CreateAppointment.",
			},
		},
	}
}

// hoursAspectTypeDDL declares the .hours availability aspect (class providerHours)
// — the step-6 write gate for SetProviderHours. Declaration-only (the script lives
// on the provider vertexType DDL). NON-sensitive. The aspect is OPT-IN: a provider
// with no .hours (or windows=[]) is unconstrained, so this is backward-compatible
// with providers created before the capability shipped. CreateAppointment /
// RescheduleAppointment read it on demand (kv.Read, §2.5 — NOT a declared/OCC read:
// hours are config, not a concurrency serialization point) to reject a booking
// outside a provider's windows (OutsideHours). Windows are UTC seconds-of-day so
// the membership test is exact integer arithmetic over time.weekday /
// time.seconds_of_day (no mixed-width "HH:MM" lexical hazard).
func hoursAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     hoursAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"SetProviderHours"},
		Description: "Provider availability-hours aspect (clinic). Stored as vtx.provider.<NanoID>.hours (class " +
			"providerHours) = {windows: [{day (0=Sun..6=Sat), openSec, closeSec}]} where openSec/closeSec are UTC " +
			"seconds-of-day (0..86400) with openSec<closeSec. Non-sensitive. OPT-IN: an absent aspect or windows=[] " +
			"means the provider is unconstrained. Written ONLY by SetProviderHours (whose provider vertexType DDL " +
			"owns the script); this aspect-type DDL is the step-6 write gate. Read on demand by CreateAppointment / " +
			"RescheduleAppointment to enforce the windows (OutsideHours rejection). Declaration-only: no op handler.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"windows":{"type":"array","items":{"type":"object","properties":{"day":{"type":"integer"},"openSec":{"type":"integer"},"closeSec":{"type":"integer"}}}}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"windows": "Availability windows: a list of {day:0-6 (Sun=0), openSec, closeSec} (UTC seconds-of-day). An appointment is admitted only if its [start,end] falls inside one window on its weekday.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "provider hours aspect",
				Payload:         map[string]any{"windows": []any{map[string]any{"day": 1, "openSec": 32400, "closeSec": 61200}}},
				ExpectedOutcome: "Stored as vtx.provider.<NanoID>.hours; written by SetProviderHours; enforced by CreateAppointment / RescheduleAppointment.",
			},
		},
	}
}

// patientDDLScript handles CreatePatient + TombstonePatient. Known-key reads only.
// CreatePatient mints the patient vertex + the .demographics aspect atomically
// (CreateOnly, so a crash-retry with the same patientId collapses on the Contract
// #4 tracker). Root data stays {} (D5).
const patientDDLScript = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_tombstone(key):
    return {"op": "tombstone", "key": key,
            "document": {"isDeleted": True, "data": {}}}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def optional_string(p, name):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None or type(v) != type(""):
        return None
    v = v.strip()
    if len(v) == 0:
        return None
    return v

def bare_nanoid_or_mint(p, name):
    if not hasattr(p, name):
        return nanoid.new()
    v = getattr(p, name)
    if v == None:
        return nanoid.new()
    if type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": must be a non-empty id string")
    v = v.strip()
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

def parts_of(key, name, want_type):
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
    if want_type != "" and parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[1], parts[2]

def vertex_alive(state, key):
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreatePatient":
        full_name = required_string(p, "fullName")
        pid = bare_nanoid_or_mint(p, "patientId")
        pkey = "vtx.patient." + pid
        demo = {"fullName": full_name}
        dob = optional_string(p, "dob")
        if dob != None:
            demo["dob"] = dob
        email = optional_string(p, "email")
        if email != None:
            demo["email"] = email
        phone = optional_string(p, "phone")
        if phone != None:
            demo["phone"] = phone
        mutations = [
            make_vtx(pkey, "patient", {}),
            make_aspect(pkey, "demographics", "patientDemographics", demo),
        ]
        events = [{"class": "clinic.patientCreated", "data": {"patientKey": pkey}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": pkey}}

    if ot == "TombstonePatient":
        pkey = required_string(p, "patientKey")
        parts_of(pkey, "patientKey", "patient")
        if not vertex_alive(state, pkey):
            fail("UnknownPatient: " + pkey)
        mutations = [make_tombstone(pkey)]
        events = [{"class": "clinic.patientTombstoned", "data": {"patientKey": pkey}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": pkey}}

    fail("patient DDL: unknown operationType: " + ot)
`

// providerDDLScript handles CreateProvider + TombstoneProvider. Same idioms as the
// patient script.
const providerDDLScript = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_aspect_upsert(vtx_key, local_name, cls, data):
    # Unconditioned update: create-if-absent / overwrite-if-present (the .hours
    # aspect is opt-in, so SetProviderHours may be the first writer).
    return {"op": "update", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_tombstone(key):
    return {"op": "tombstone", "key": key,
            "document": {"isDeleted": True, "data": {}}}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def optional_string(p, name):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None or type(v) != type(""):
        return None
    v = v.strip()
    if len(v) == 0:
        return None
    return v

def bare_nanoid_or_mint(p, name):
    if not hasattr(p, name):
        return nanoid.new()
    v = getattr(p, name)
    if v == None:
        return nanoid.new()
    if type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": must be a non-empty id string")
    v = v.strip()
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

def parts_of(key, name, want_type):
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
    if want_type != "" and parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[1], parts[2]

def vertex_alive(state, key):
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

def class_of(state, key):
    if key not in state:
        return None
    doc = state[key]
    if doc == None or not hasattr(doc, "class"):
        return None
    return getattr(doc, "class")

def require_int_in(w, name, lo, hi):
    # Each window arrives as a dict (a nested JSON object). day / openSec / closeSec
    # must be integers in range. Whole-number JSON decodes to a Starlark int.
    if type(w) != type({}):
        fail("InvalidArgument: windows: each window must be an object; got " + type(w))
    v = w.get(name)
    if v == None:
        fail("InvalidArgument: windows: " + name + ": required")
    if type(v) != type(0):
        fail("InvalidArgument: windows: " + name + ": must be an integer; got " + type(v))
    if v < lo or v > hi:
        fail("InvalidArgument: windows: " + name + ": must be in [" + str(lo) + ", " + str(hi) + "]; got " + str(v))
    return v

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateProvider":
        full_name = required_string(p, "fullName")
        specialty = required_string(p, "specialty")
        prid = bare_nanoid_or_mint(p, "providerId")
        prkey = "vtx.provider." + prid
        profile = {"fullName": full_name, "specialty": specialty}
        credentials = optional_string(p, "credentials")
        if credentials != None:
            profile["credentials"] = credentials
        bio = optional_string(p, "bio")
        if bio != None:
            profile["bio"] = bio
        # .bookings: the per-provider appointment index, initialized EMPTY so the
        # key is always present — CreateAppointment declares it in contextHint.reads
        # for an OCC-snapshotted double-book check, and a declared read of an absent
        # key is a fatal HydrationMiss.
        mutations = [
            make_vtx(prkey, "provider", {}),
            make_aspect(prkey, "profile", "providerProfile", profile),
            make_aspect(prkey, "bookings", "providerBookings", {"appts": []}),
        ]
        events = [{"class": "clinic.providerCreated", "data": {"providerKey": prkey}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": prkey}}

    if ot == "TombstoneProvider":
        prkey = required_string(p, "providerKey")
        parts_of(prkey, "providerKey", "provider")
        if not vertex_alive(state, prkey):
            fail("UnknownProvider: " + prkey)
        mutations = [make_tombstone(prkey)]
        events = [{"class": "clinic.providerTombstoned", "data": {"providerKey": prkey}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": prkey}}

    if ot == "SetProviderHours":
        prkey = required_string(p, "providerKey")
        parts_of(prkey, "providerKey", "provider")
        if not vertex_alive(state, prkey):
            fail("UnknownProvider: " + prkey)
        cls = class_of(state, prkey)
        if cls != "provider":
            fail("WrongClass: providerKey: " + prkey + " has class " + str(cls) + ", required provider")
        # windows is required (pass [] to clear the constraint). Each window is
        # {day:0-6 (Sun=0), openSec, closeSec} in UTC seconds-of-day, openSec<closeSec.
        if not hasattr(p, "windows"):
            fail("InvalidArgument: windows: required (use [] to clear)")
        windows = getattr(p, "windows")
        if type(windows) != type([]):
            fail("InvalidArgument: windows: must be a list")
        clean = []
        for w in windows:
            day = require_int_in(w, "day", 0, 6)
            open_sec = require_int_in(w, "openSec", 0, 86400)
            close_sec = require_int_in(w, "closeSec", 0, 86400)
            if not (open_sec < close_sec):
                fail("InvalidArgument: windows: openSec must be < closeSec; got openSec=" + str(open_sec) + " closeSec=" + str(close_sec))
            clean.append({"day": day, "openSec": open_sec, "closeSec": close_sec})
        # Unconditioned upsert of the WHOLE .hours aspect (create-if-absent — it is
        # opt-in, CreateProvider does not init it). No OCC: hours are config, not a
        # concurrency serialization point (the .bookings index is the only one).
        mutations = [make_aspect_upsert(prkey, "hours", "providerHours", {"windows": clean})]
        events = [{"class": "clinic.providerHoursSet",
                   "data": {"providerKey": prkey, "windowCount": len(clean)}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": prkey}}

    fail("provider DDL: unknown operationType: " + ot)
`

// appointmentDDLScript handles CreateAppointment + RescheduleAppointment +
// SetAppointmentStatus + TombstoneAppointment. CreateAppointment validates BOTH
// endpoints (patient + provider) alive + class, then atomically mints the
// appointment vertex + the .schedule + .status{scheduled} aspects + the forPatient
// + withProvider links (Contract #1 §1.1 — the later-arriving appointment is the
// source). RescheduleAppointment rewrites the .schedule aspect with new times
// (re-deriving remindAt = startsAt − 24h), an unconditioned upsert that leaves the
// links + status untouched. SetAppointmentStatus is an unconditioned upsert of the
// .status aspect (no read-merge — status is its own aspect). CreateAppointment
// REJECTS a double-book via the provider's OCC-snapshotted .bookings index +
// per-candidate kv.Read liveness (Capability-KV §06 — the op's own Starlark logic).
const appointmentDDLScript = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_aspect_upsert(vtx_key, local_name, cls, data):
    return {"op": "update", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_aspect_upsert_occ(vtx_key, local_name, cls, data, expected_revision):
    # Like make_aspect_upsert but carries an explicit expectedRevision so the
    # commit applies an OCC condition (an update with no expectedRevision commits
    # UNCONDITIONED — step8_commit.go). The bookings-index serialization point.
    m = make_aspect_upsert(vtx_key, local_name, cls, data)
    m["expectedRevision"] = expected_revision
    return m

def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": data}}

def make_tombstone(key):
    return {"op": "tombstone", "key": key,
            "document": {"isDeleted": True, "data": {}}}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def optional_string(p, name):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None or type(v) != type(""):
        return None
    v = v.strip()
    if len(v) == 0:
        return None
    return v

def bare_nanoid_or_mint(p, name):
    if not hasattr(p, name):
        return nanoid.new()
    v = getattr(p, name)
    if v == None:
        return nanoid.new()
    if type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": must be a non-empty id string")
    v = v.strip()
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

def parts_of(key, name, want_type):
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
    if want_type != "" and parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[1], parts[2]

def vertex_alive(state, key):
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

def class_of(state, key):
    if key not in state:
        return None
    doc = state[key]
    if doc == None:
        return None
    if not hasattr(doc, "class"):
        return None
    return getattr(doc, "class")

def require_live_typed(state, key, name, want_class):
    # Endpoint validation: the endpoint MUST be alive AND the expected class. A
    # dead or wrong-class endpoint is never wired into an appointment.
    if not vertex_alive(state, key):
        fail("UnknownEndpoint: " + name + ": " + key + " is absent or tombstoned")
    cls = class_of(state, key)
    if cls != want_class:
        fail("WrongClass: " + name + ": " + key + " has class " + str(cls) + ", required " + want_class)

def enforce_hours(provider, starts_at, ends_at):
    # Opt-in provider availability windows (Capability-KV §06 — the op's own Starlark
    # logic). Read the provider's .hours aspect on demand (kv.Read, §2.5 — NOT a
    # declared/OCC read: hours are config, not the booking serialization point). An
    # absent / deleted aspect or windows=[] means UNCONSTRAINED (backward-compatible
    # with providers created before this capability). Otherwise the appointment's
    # [start, end] must sit inside ONE window on its UTC weekday. Times are exact
    # integers (time.weekday 0=Sun..6=Sat, time.seconds_of_day 0..86399) so the
    # membership test is integer arithmetic — no mixed-width string-compare hazard.
    hours = kv.Read(provider + ".hours")
    if hours == None or hours.isDeleted:
        return
    windows = hours.data.get("windows")
    if windows == None or type(windows) != type([]) or len(windows) == 0:
        return
    sw = time.weekday(starts_at)
    ew = time.weekday(ends_at)
    ss = time.seconds_of_day(starts_at)
    es = time.seconds_of_day(ends_at)
    if sw != ew:
        fail("OutsideHours: appointment spans more than one UTC day (start weekday " + str(sw) + ", end weekday " + str(ew) + "); book within a single availability window")
    for w in windows:
        if type(w) != type({}):
            continue
        d = w.get("day")
        o = w.get("openSec")
        c = w.get("closeSec")
        if d == None or o == None or c == None:
            continue
        if d == sw and o <= ss and es <= c:
            return
    fail("OutsideHours: provider " + provider + " is not available at the requested time (UTC weekday " + str(sw) + ", " + str(ss) + "s-" + str(es) + "s of day); no matching availability window")

APPOINTMENT_STATUSES = ["scheduled", "confirmed", "checkedIn", "completed", "cancelled", "noShow"]

def required_status(p):
    s = required_string(p, "status")
    if s not in APPOINTMENT_STATUSES:
        fail("InvalidArgument: status: must be one of scheduled, confirmed, checkedIn, completed, cancelled, noShow; got " + s)
    return s

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateAppointment":
        patient = required_string(p, "patient")
        _, patient_id = parts_of(patient, "patient", "patient")
        provider = required_string(p, "provider")
        _, provider_id = parts_of(provider, "provider", "provider")
        # Both endpoints alive + the right class (endpoint validation at the op).
        require_live_typed(state, patient, "patient", "patient")
        require_live_typed(state, provider, "provider", "provider")

        # Normalize startsAt / endsAt to canonical whole-second UTC (time.rfc3339_utc
        # — a pure builtin, no clock read). This parse-validates the instants AND
        # makes the lexical RFC3339 compares the convergence lens relies on
        # (startsAt > $now, remindAt <= $now) sound for ANY caller offset / fractional
        # form, not only Z-suffixed input — the lease-signing normalization idiom.
        starts_at = time.rfc3339_utc(required_string(p, "startsAt"))
        ends_at = time.rfc3339_utc(required_string(p, "endsAt"))
        reason = optional_string(p, "reason")

        # A zero / negative-length booking is invalid (and would make every
        # half-open overlap test below vacuously false). Canonical-UTC strings
        # compare lexically == chronologically.
        if not (starts_at < ends_at):
            fail("InvalidArgument: endsAt: must be strictly after startsAt; got startsAt=" + starts_at + " endsAt=" + ends_at)

        # Provider availability windows (opt-in; OutsideHours if the booking falls
        # outside the provider's .hours). Checked before the double-book fan-out.
        enforce_hours(provider, starts_at, ends_at)

        appt_id = bare_nanoid_or_mint(p, "appointmentId")
        appt_key = "vtx.appointment." + appt_id

        # Double-book detection (Capability-KV §06 — "the operation's own Starlark
        # logic"). The provider's .bookings index is the list of appointment keys
        # booked with this provider. It MUST be a declared contextHint.reads key so
        # this update is OCC-snapshotted — the concurrency serialization point: two
        # simultaneous bookings for one provider both snapshot the index at the same
        # revision, both append, and the second commit RevisionConflicts (fail
        # closed, never a silent double-book). CreateProvider initializes it empty so
        # the key is always present (a declared read of an absent key is a fatal
        # HydrationMiss). For each candidate, read its LIVE vertex + status + schedule
        # via kv.Read (§2.5): a tombstoned vertex or a terminal (cancelled / completed
        # / noShow) appointment is pruned and does not block; a still scheduled /
        # confirmed appointment whose half-open interval overlaps the request is a
        # SlotConflict. NOTE TombstoneAppointment tombstones only the vertex (the
        # aspects linger), so liveness MUST gate on the vertex isDeleted, not the
        # aspects. Pruning bounds both the index and the per-call kv.Read fan-out to a
        # provider's live book.
        bookings_key = provider + ".bookings"
        if bookings_key not in state:
            fail("HydrationMiss: " + bookings_key + ": the provider .bookings index must be declared in contextHint.reads (it is created empty by CreateProvider; a provider created before this capability was installed must be re-created on a fresh stack)")
        booked = state[bookings_key]
        existing = []
        appts_val = booked.data.get("appts")
        if appts_val != None and type(appts_val) == type([]):
            existing = appts_val
        terminal_statuses = ["cancelled", "completed", "noShow"]
        kept = []
        for cand_key in existing:
            if cand_key == appt_key:
                continue
            cand = kv.Read(cand_key)
            if cand == None or cand.isDeleted:
                continue
            cstatus = kv.Read(cand_key + ".status")
            status_val = None
            if cstatus != None and not cstatus.isDeleted:
                status_val = cstatus.data.get("value")
            if status_val in terminal_statuses:
                continue
            # Still live + non-terminal: keep it in the rebuilt index, and test overlap.
            kept.append(cand_key)
            csched = kv.Read(cand_key + ".schedule")
            if csched == None or csched.isDeleted:
                continue
            c_starts = csched.data.get("startsAt")
            c_ends = csched.data.get("endsAt")
            if c_starts == None or c_ends == None:
                continue
            # Half-open [start, end) overlap on canonical-UTC RFC3339 (lexical ==
            # chronological): two intervals overlap iff a.start < b.end AND b.start <
            # a.end. Back-to-back (a.end == b.start) does NOT overlap.
            if starts_at < c_ends and c_starts < ends_at:
                fail("SlotConflict: provider " + provider + " is already booked " + c_starts + "/" + c_ends + " (appointment " + cand_key + "); requested " + starts_at + "/" + ends_at)
        kept.append(appt_key)

        # forPatient / withProvider: the appointment (later-arriving) is the
        # source, the pre-existing patient / provider is the target (Contract #1
        # §1.1). Sentences: "appointment forPatient patient", "appointment
        # withProvider provider".
        for_patient_lnk = "lnk.appointment." + appt_id + ".forPatient.patient." + patient_id
        with_provider_lnk = "lnk.appointment." + appt_id + ".withProvider.provider." + provider_id

        # remindAt = startsAt − 24h: the reminder deadline the clinic-reminders
        # convergence lens projects as freshUntil so the @at temporal lane fires a
        # reminder ~24h ahead. Precomputed at write time (time.rfc3339_add — a pure
        # builtin, no clock read) and emitted canonical UTC, so the lens needs no
        # date arithmetic — only the RFC3339 lexical compare. A booking < 24h out
        # yields a past remindAt → reminded immediately. rfc3339_add also parse-
        # validates startsAt as RFC3339 (fails closed on a malformed instant).
        sched = {"startsAt": starts_at, "endsAt": ends_at,
                 "remindAt": time.rfc3339_add(starts_at, "-24h")}
        if reason != None:
            sched["reason"] = reason

        # Root data minimal (D5): {} on root. The patient / provider are links; the
        # schedule + status are aspects.
        mutations = [
            make_vtx(appt_key, "appointment", {}),
            make_aspect(appt_key, "schedule", "appointmentSchedule", sched),
            make_aspect(appt_key, "status", "appointmentStatus", {"value": "scheduled"}),
            make_link(for_patient_lnk, appt_key, patient, "forPatient", "forPatient", {}),
            make_link(with_provider_lnk, appt_key, provider, "withProvider", "withProvider", {}),
            # The rebuilt (pruned + appended) provider bookings index, OCC-guarded on
            # the snapshot revision — see the double-book block above.
            make_aspect_upsert_occ(provider, "bookings", "providerBookings", {"appts": kept}, booked.revision),
        ]
        events = [{"class": "clinic.appointmentCreated",
                   "data": {"appointmentKey": appt_key, "patient": patient, "provider": provider}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": appt_key}}

    if ot == "RescheduleAppointment":
        appt_key = required_string(p, "appointmentKey")
        _, appt_id = parts_of(appt_key, "appointmentKey", "appointment")
        if not vertex_alive(state, appt_key):
            fail("UnknownAppointment: " + appt_key)
        cls = class_of(state, appt_key)
        if cls != "appointment":
            fail("WrongClass: appointmentKey: " + appt_key + " has class " + str(cls) + ", required appointment")

        # The appointment's provider — required so the move is conflict-checked
        # against that provider's book exactly as CreateAppointment is (without it a
        # reschedule could silently move an appointment INTO an occupied slot,
        # bypassing the double-book defense). The caller MUST also list
        # provider + ".bookings" in contextHint.reads — the OCC-snapshotted
        # serialization point. The passed provider is validated to be THIS
        # appointment's actual provider by reading the deterministic withProvider link
        # key (kv.Read, §2.5): a live link proves the relationship AND that the
        # provider is a real, once-validated provider — a wrong / fabricated provider
        # would otherwise check the wrong (e.g. empty) book and bypass the test. The
        # provider vertex root itself need not be a declared read (the .bookings index
        # is the serialization point; the link is the identity proof).
        provider = required_string(p, "provider")
        _, provider_id = parts_of(provider, "provider", "provider")
        with_provider_lnk = "lnk.appointment." + appt_id + ".withProvider.provider." + provider_id
        wp = kv.Read(with_provider_lnk)
        if wp == None or wp.isDeleted:
            fail("WrongProvider: provider " + provider + " is not the provider of appointment " + appt_key)

        # New times: normalize to canonical whole-second UTC (parse-validates the
        # instants AND makes the convergence lens's lexical RFC3339 compares sound
        # for any caller offset) — exactly the CreateAppointment idiom.
        starts_at = time.rfc3339_utc(required_string(p, "startsAt"))
        ends_at = time.rfc3339_utc(required_string(p, "endsAt"))
        reason = optional_string(p, "reason")

        # A zero / negative-length booking is invalid (mirrors CreateAppointment; the
        # original reschedule lacked this guard).
        if not (starts_at < ends_at):
            fail("InvalidArgument: endsAt: must be strictly after startsAt; got startsAt=" + starts_at + " endsAt=" + ends_at)

        # Provider availability windows (opt-in; OutsideHours if the new time falls
        # outside the provider's .hours) — the move must land inside business hours too.
        enforce_hours(provider, starts_at, ends_at)

        # Double-book detection for the MOVE: the new interval must not overlap any
        # OTHER live, non-terminal appointment of this provider — the appointment
        # being moved is skipped (you never conflict with your own current slot). The
        # provider's .bookings index is the OCC-snapshotted serialization point:
        # reschedule rewrites it (pruned to the live, non-terminal book) under
        # expectedRevision, so a concurrent create / reschedule for the same provider
        # fails closed (RevisionConflict), never a silent double-book. Mirrors the
        # CreateAppointment block; kv.Read (§2.5) gives each candidate's live
        # vertex + status + schedule.
        bookings_key = provider + ".bookings"
        if bookings_key not in state:
            fail("HydrationMiss: " + bookings_key + ": the provider .bookings index must be declared in contextHint.reads (it is created empty by CreateProvider)")
        booked = state[bookings_key]
        existing = []
        appts_val = booked.data.get("appts")
        if appts_val != None and type(appts_val) == type([]):
            existing = appts_val
        terminal_statuses = ["cancelled", "completed", "noShow"]
        kept = []
        for cand_key in existing:
            cand = kv.Read(cand_key)
            if cand == None or cand.isDeleted:
                continue
            cstatus = kv.Read(cand_key + ".status")
            status_val = None
            if cstatus != None and not cstatus.isDeleted:
                status_val = cstatus.data.get("value")
            if status_val in terminal_statuses:
                continue
            # Live + non-terminal: keep it in the rebuilt index (this also re-asserts
            # the moved appointment's own membership). Never overlap-test against self.
            kept.append(cand_key)
            if cand_key == appt_key:
                continue
            csched = kv.Read(cand_key + ".schedule")
            if csched == None or csched.isDeleted:
                continue
            c_starts = csched.data.get("startsAt")
            c_ends = csched.data.get("endsAt")
            if c_starts == None or c_ends == None:
                continue
            if starts_at < c_ends and c_starts < ends_at:
                fail("SlotConflict: provider " + provider + " is already booked " + c_starts + "/" + c_ends + " (appointment " + cand_key + "); requested " + starts_at + "/" + ends_at)

        # Re-derive remindAt = startsAt − 24h so the clinic-reminders convergence
        # lens re-projects a fresh freshUntil and the @at temporal lane re-arms for
        # the NEW time (for a not-yet-sent reminder; the remindedFor term re-arms an
        # already-sent one).
        sched = {"startsAt": starts_at, "endsAt": ends_at,
                 "remindAt": time.rfc3339_add(starts_at, "-24h")}
        if reason != None:
            sched["reason"] = reason

        # Unconditioned upsert of the WHOLE .schedule aspect (the caller round-trips
        # the reason; an omitted reason clears it; forPatient / withProvider links +
        # .status untouched), PLUS the OCC-guarded rewrite of the provider's pruned
        # bookings index — the serialization point, see the double-book block above.
        mutations = [
            make_aspect_upsert(appt_key, "schedule", "appointmentSchedule", sched),
            make_aspect_upsert_occ(provider, "bookings", "providerBookings", {"appts": kept}, booked.revision),
        ]
        events = [{"class": "clinic.appointmentRescheduled",
                   "data": {"appointmentKey": appt_key, "startsAt": starts_at, "endsAt": ends_at}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": appt_key}}

    if ot == "SetAppointmentStatus":
        appt_key = required_string(p, "appointmentKey")
        parts_of(appt_key, "appointmentKey", "appointment")
        if not vertex_alive(state, appt_key):
            fail("UnknownAppointment: " + appt_key)
        cls = class_of(state, appt_key)
        if cls != "appointment":
            fail("WrongClass: appointmentKey: " + appt_key + " has class " + str(cls) + ", required appointment")
        status = required_status(p)
        # Optional audit note (cancel / no-show reason for billing + records).
        # Stored on the .status aspect, distinct from the .schedule visit reason.
        # Omitted → the .status carries only {value} (an unconditioned upsert, so a
        # later transition without a note clears any prior note — intended: the note
        # belongs to the terminal cancel/no-show it was recorded with).
        status_data = {"value": status}
        note = optional_string(p, "note")
        if note != None:
            status_data["note"] = note
        mutations = [make_aspect_upsert(appt_key, "status", "appointmentStatus", status_data)]
        events = [{"class": "clinic.appointmentStatusSet",
                   "data": {"appointmentKey": appt_key, "status": status}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": appt_key}}

    if ot == "TombstoneAppointment":
        appt_key = required_string(p, "appointmentKey")
        parts_of(appt_key, "appointmentKey", "appointment")
        if not vertex_alive(state, appt_key):
            fail("UnknownAppointment: " + appt_key)
        mutations = [make_tombstone(appt_key)]
        events = [{"class": "clinic.appointmentTombstoned", "data": {"appointmentKey": appt_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": appt_key}}

    fail("appointment DDL: unknown operationType: " + ot)
`

// aspectDeclarationOnlyScript is the declaration-only Starlark for the four
// aspect-type DDLs. The aspects are written by the vertexType DDLs' ops; these
// aspect-type DDLs are step-6 write gates only, never op handlers — they fail
// closed if dispatched.
const aspectDeclarationOnlyScript = `
def execute(state, op):
    fail("aspect-type DDL: not an operation handler: " + op.operationType)
`
