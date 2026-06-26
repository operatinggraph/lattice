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
		PermittedCommands: []string{"CreateProvider", "TombstoneProvider"},
		Description: "Clinic provider DDL. Vertex shape: vtx.provider.<NanoID>, class=provider, root data = {} " +
			"(minimal, D5 — the data lives in the .profile aspect). CreateProvider mints the provider + writes the " +
			".profile aspect {fullName (required), specialty (required), credentials?, bio?} atomically. " +
			"TombstoneProvider soft-deletes one.",
		Script: providerDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"fullName":{"type":"string","description":"The provider's full name (CreateProvider; required)."},` +
			`"specialty":{"type":"string","description":"The provider's clinical specialty, e.g. Cardiology (CreateProvider; required)."},` +
			`"credentials":{"type":"string","description":"Post-nominal credentials, e.g. MD (CreateProvider; optional)."},` +
			`"bio":{"type":"string","description":"Short provider bio (CreateProvider; optional)."},` +
			`"providerId":{"type":"string","description":"Optional bare NanoID for the new provider vertex (CreateProvider); absent → minted."},` +
			`"providerKey":{"type":"string","description":"vtx.provider.<NanoID> of an existing provider (TombstoneProvider; required, validated alive)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.provider.<NanoID> the operation wrote."}}}`,
		FieldDescription: map[string]string{
			"fullName":    "The provider's full name. Stored on the .profile aspect (CreateProvider; required).",
			"specialty":   "The provider's clinical specialty (e.g. Cardiology). Stored on the .profile aspect (CreateProvider; required).",
			"credentials": "Optional post-nominal credentials (e.g. MD, RN). Stored on the .profile aspect when present.",
			"bio":         "Optional short provider bio. Stored on the .profile aspect when present.",
			"providerId":  "Optional bare NanoID (no dots / key segments) for the new provider vertex. Absent → minted with nanoid.new().",
			"providerKey": "Full vtx.provider.<NanoID> key of an existing provider vertex to tombstone (TombstoneProvider).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "CreateProvider — register a provider",
				Payload: map[string]any{"fullName": "Dr. Sam Okafor", "specialty": "Cardiology", "credentials": "MD"},
				ExpectedOutcome: "Mints vtx.provider.<NanoID> (class=provider, root {}) + the .profile aspect " +
					"{fullName, specialty, credentials}. Returns primaryKey (the provider key).",
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
			"SetAppointmentStatus upserts the .status aspect to one of {scheduled, confirmed, completed, " +
			"cancelled, noShow}. TombstoneAppointment soft-deletes the appointment. NOTE (D6): this DDL records a " +
			"REQUESTED time — it does NOT enforce slot-uniqueness, double-book rejection, or provider hours (the " +
			"separate deferred temporal-availability item; Capability-KV §06 defers it).",
		Script: appointmentDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"patient":{"type":"string","description":"vtx.patient.<NanoID> the appointment is for (CreateAppointment; required, validated alive + class=patient)."},` +
			`"provider":{"type":"string","description":"vtx.provider.<NanoID> the appointment is with (CreateAppointment; required, validated alive + class=provider)."},` +
			`"startsAt":{"type":"string","description":"Appointment start, RFC3339 (CreateAppointment / RescheduleAppointment; required). Caller supplies canonical UTC."},` +
			`"endsAt":{"type":"string","description":"Appointment end, RFC3339 (CreateAppointment / RescheduleAppointment; required). Caller supplies canonical UTC."},` +
			`"reason":{"type":"string","description":"Visit reason / chief complaint (CreateAppointment / RescheduleAppointment; optional — on RescheduleAppointment an omitted reason clears it)."},` +
			`"appointmentId":{"type":"string","description":"Optional bare NanoID for the new appointment vertex (CreateAppointment); absent → minted."},` +
			`"appointmentKey":{"type":"string","description":"vtx.appointment.<NanoID> of an existing appointment (RescheduleAppointment / SetAppointmentStatus / TombstoneAppointment; required, validated alive)."},` +
			`"status":{"type":"string","enum":["scheduled","confirmed","completed","cancelled","noShow"],"description":"New status (SetAppointmentStatus; required)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.appointment.<NanoID> the operation wrote."}}}`,
		FieldDescription: map[string]string{
			"patient":        "Full vtx.patient.<NanoID> key the appointment is for. CreateAppointment validates it is alive + class=patient and writes the forPatient link (appointment→patient). The caller MUST list this key in ContextHint.Reads.",
			"provider":       "Full vtx.provider.<NanoID> key the appointment is with. CreateAppointment validates it is alive + class=provider and writes the withProvider link (appointment→provider). The caller MUST list this key in ContextHint.Reads.",
			"startsAt":       "Appointment start (RFC3339, canonical UTC). Stored on the .schedule aspect (CreateAppointment / RescheduleAppointment; required).",
			"endsAt":         "Appointment end (RFC3339, canonical UTC). Stored on the .schedule aspect (CreateAppointment / RescheduleAppointment; required).",
			"reason":         "Optional visit reason / chief complaint. Stored on the .schedule aspect when present (CreateAppointment / RescheduleAppointment; on RescheduleAppointment an omitted reason clears it).",
			"appointmentId":  "Optional bare NanoID (no dots / key segments) for the new appointment vertex. Absent → minted with nanoid.new().",
			"appointmentKey": "Full vtx.appointment.<NanoID> key of an existing appointment (RescheduleAppointment rewrites its .schedule; SetAppointmentStatus validates it alive + class=appointment; TombstoneAppointment validates it alive).",
			"status":         "New appointment status, one of {scheduled, confirmed, completed, cancelled, noShow} (SetAppointmentStatus; required).",
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
			"clinic-reminders package's convergence lens reads (it is not a caller input). The booking time is " +
			"REQUESTED, not conflict-checked (D6 — deferred).",
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
			"appointmentStatus) = {value ∈ scheduled|confirmed|completed|cancelled|noShow}. Non-sensitive. Written " +
			"by CreateAppointment (initial scheduled) and SetAppointmentStatus (transitions) — whose appointment " +
			"vertexType DDL owns the script; this aspect-type DDL is the step-6 write gate. Declaration-only: no op " +
			"handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"value":{"type":"string","enum":["scheduled","confirmed","completed","cancelled","noShow"]}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"value": "Appointment status: scheduled | confirmed | completed | cancelled | noShow.",
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
        mutations = [
            make_vtx(prkey, "provider", {}),
            make_aspect(prkey, "profile", "providerProfile", profile),
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
// .status aspect (no read-merge — status is its own aspect). The booking time is
// REQUESTED, not slot-conflict-checked (D6 — deferred).
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

APPOINTMENT_STATUSES = ["scheduled", "confirmed", "completed", "cancelled", "noShow"]

def required_status(p):
    s = required_string(p, "status")
    if s not in APPOINTMENT_STATUSES:
        fail("InvalidArgument: status: must be one of scheduled, confirmed, completed, cancelled, noShow; got " + s)
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

        appt_id = bare_nanoid_or_mint(p, "appointmentId")
        appt_key = "vtx.appointment." + appt_id

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
        ]
        events = [{"class": "clinic.appointmentCreated",
                   "data": {"appointmentKey": appt_key, "patient": patient, "provider": provider}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": appt_key}}

    if ot == "RescheduleAppointment":
        appt_key = required_string(p, "appointmentKey")
        parts_of(appt_key, "appointmentKey", "appointment")
        if not vertex_alive(state, appt_key):
            fail("UnknownAppointment: " + appt_key)
        cls = class_of(state, appt_key)
        if cls != "appointment":
            fail("WrongClass: appointmentKey: " + appt_key + " has class " + str(cls) + ", required appointment")

        # New times: normalize to canonical whole-second UTC (parse-validates the
        # instants AND makes the convergence lens's lexical RFC3339 compares sound
        # for any caller offset) — exactly the CreateAppointment idiom.
        starts_at = time.rfc3339_utc(required_string(p, "startsAt"))
        ends_at = time.rfc3339_utc(required_string(p, "endsAt"))
        reason = optional_string(p, "reason")

        # Re-derive remindAt = startsAt − 24h so the clinic-reminders convergence
        # lens re-projects a fresh freshUntil and the @at temporal lane re-arms for
        # the NEW time (for a not-yet-sent reminder). An already-sent reminder does
        # NOT re-fire — the reminders gate keys on reminderSentAt = null (a bounded
        # limitation; the remindedFor refinement is a tracked follow-up).
        sched = {"startsAt": starts_at, "endsAt": ends_at,
                 "remindAt": time.rfc3339_add(starts_at, "-24h")}
        if reason != None:
            sched["reason"] = reason

        # Unconditioned upsert of the WHOLE .schedule aspect: the caller carries the
        # existing reason (the FE round-trips it); an omitted reason clears it. The
        # forPatient / withProvider links + the .status aspect are untouched.
        mutations = [make_aspect_upsert(appt_key, "schedule", "appointmentSchedule", sched)]
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
        mutations = [make_aspect_upsert(appt_key, "status", "appointmentStatus", {"value": status})]
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
