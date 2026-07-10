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
	hoursAspectDDL        = "providerHours"
	timeOffAspectDDL      = "providerTimeOff"
	encounterAspectDDL    = "appointmentEncounter"

	providerSlotClaimAspectDDL = "providerSlotClaim"
	patientSlotClaimAspectDDL  = "patientSlotClaim"

	identityPatientClaimAspectDDL = "identityPatientClaim"
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
// Every aspect is NON-sensitive: .demographics carries only the patient's
// fullName — the display label the roster lenses need — never contact PII.
// Real contact (email/phone) is Vault-plane PII that belongs on a vtx.identity,
// never a bare vtx.patient aspect (step-6's sensitiveAspectScope forbids a
// sensitive aspect on a non-identity vertex anyway). CreatePatient accepts an
// optional pre-minted identityKey and wires an identifiedBy link to it — the
// caller (the FE) mints the identity carrying the sensitive contact first via
// identity-domain's CreateUnclaimedIdentity, mirroring loftspace-app's
// applicant flow (clinic-domain-design.md: "vtx.identity + an identifiedBy
// link — not a rework"). A CreateOnly identityPatientClaim aspect on the
// identity globally guards it: the identifiedBy link key alone is
// (patient, identity)-composite, so it cannot stop two DIFFERENT patients
// both wiring the same identityKey; the claim aspect, keyed solely on the
// identity, is what makes the second claim collide. No backfill for
// pre-existing patients (Vault's full-stack-reset delivery boundary covers
// the migration). Display of the linked contact rides a later Secure-Lens
// protected model (Fire 5 of the Vault crypto-shredding design); this fire
// only wires the link.
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		patientVertexTypeDDL(),
		providerVertexTypeDDL(),
		appointmentVertexTypeDDL(),
		demographicsAspectTypeDDL(),
		profileAspectTypeDDL(),
		scheduleAspectTypeDDL(),
		statusAspectTypeDDL(),
		hoursAspectTypeDDL(),
		timeOffAspectTypeDDL(),
		providerSlotClaimAspectTypeDDL(),
		patientSlotClaimAspectTypeDDL(),
		encounterAspectTypeDDL(),
		identityPatientClaimAspectTypeDDL(),
	}
}

func patientVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     patientVertexDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreatePatient", "TombstonePatient"},
		Description: "Clinic patient DDL. Vertex shape: vtx.patient.<NanoID>, class=patient, root data = {} " +
			"(minimal, D5 — the data lives in the .demographics aspect + the optional identifiedBy link). " +
			"CreatePatient mints the patient + writes the .demographics aspect {fullName (required)} atomically; " +
			"an optional identityKey wires lnk.patient.<id>.identifiedBy.identity.<identityId> to a pre-minted " +
			"vtx.identity (validated alive + class=identity) carrying the patient's sensitive contact, and claims " +
			"a CreateOnly vtx.identity.<identityId>.patientClaim guard aspect — a second, DIFFERENT patient passing " +
			"the same identityKey is rejected (IdentityAlreadyClaimed). TombstonePatient soft-deletes one. The " +
			".demographics aspect is NON-sensitive (it attaches to a patient, not an identity); real contact PII " +
			"lives on the linked identity, the Vault plane's unit.",
		Script: patientDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"fullName":{"type":"string","description":"The patient's full name (CreatePatient; required)."},` +
			`"identityKey":{"type":"string","description":"vtx.identity.<NanoID> of a pre-minted identity carrying the patient's sensitive contact (CreatePatient; optional, validated alive + class=identity; wires the identifiedBy link)."},` +
			`"patientId":{"type":"string","description":"Optional bare NanoID for the new patient vertex (CreatePatient); absent → minted."},` +
			`"patientKey":{"type":"string","description":"vtx.patient.<NanoID> of an existing patient (TombstonePatient; required, validated alive)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.patient.<NanoID> the operation wrote."}}}`,
		FieldDescription: map[string]string{
			"fullName":    "The patient's full name. Stored on the .demographics aspect (CreatePatient; required).",
			"identityKey": "Full vtx.identity.<NanoID> key of a pre-minted identity to link (CreatePatient; optional). Must be alive + class=identity; wires the identifiedBy link and claims a CreateOnly patientClaim guard aspect on the identity (rejected if another patient already claimed it). Absent → the patient has no linked identity.",
			"patientId":   "Optional bare NanoID (no dots / key segments) for the new patient vertex (vtx.patient.<patientId>). Absent → minted with nanoid.new().",
			"patientKey":  "Full vtx.patient.<NanoID> key of an existing patient vertex to tombstone (TombstonePatient).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "CreatePatient — register a patient",
				Payload: map[string]any{"fullName": "Alice Rivera"},
				ExpectedOutcome: "Mints vtx.patient.<NanoID> (class=patient, root {}) + the .demographics aspect " +
					"{fullName}. Accepts an optional bare-NanoID patientId. Returns primaryKey (the patient key).",
			},
			{
				Name:    "CreatePatient — register a patient with linked contact identity",
				Payload: map[string]any{"fullName": "Alice Rivera", "identityKey": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Mints the patient as above, plus lnk.patient.<id>.identifiedBy.identity.<identityId> " +
					"to the supplied identity (rejected if that identity is absent, tombstoned, or the wrong class) " +
					"and a CreateOnly patientClaim guard aspect on the identity (rejected if a DIFFERENT patient " +
					"already claimed it).",
			},
			{
				Name:            "TombstonePatient — remove a patient",
				Payload:         map[string]any{"patientKey": "vtx.patient.<NanoID>"},
				ExpectedOutcome: "Soft-deletes the patient vertex. Returns primaryKey. Rejects an absent / already-dead patient.",
			},
		},
	}
}

// identityPatientClaimAspectTypeDDL declares the .patientClaim aspect ATTACHED
// onto an identity-domain vtx.identity (the clinic-reminders idiom of a package
// adding an aspect onto another package's vertex type — clinic-reminders/ddls.go's
// .reminder marker on clinic-domain's own appointment vertex). It is the global
// exclusivity guard for CreatePatient's optional identityKey: the .demographics
// aspect + identifiedBy link alone let two DIFFERENT patients both pass the same
// identityKey (the identifiedBy link key is (patient, identity)-composite, never
// identity-only), so two roster rows would decrypt and display the same person's
// contact. A CreateOnly aspect keyed SOLELY on the identity closes that gap —
// mirroring providerSlotClaim/patientSlotClaim's per-cell existence-marker lock,
// just with one claimant instead of one cell. Declaration-only; NON-sensitive (no
// data, so step-6's sensitiveAspectScope never fires on it either way).
func identityPatientClaimAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     identityPatientClaimAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreatePatient"},
		Description: "Identity patient-claim guard aspect (clinic-domain, attached onto an identity-domain vertex). " +
			"Stored as vtx.identity.<NanoID>.patientClaim (class identityPatientClaim) = {} — a pure existence marker, " +
			"no relationship field. CreatePatient writes ONE per claimed identityKey, CreateOnly: the key ITSELF " +
			"(identical regardless of WHICH patient is claiming) is the lock — a second, different patient passing the " +
			"same identityKey collides at commit (RevisionConflict), never a silent double-claim. Declaration-only: no " +
			"op handler (CreatePatient's script, owned by the patient vertexType DDL, writes it).",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"data": "Always {} — a pure existence marker. Exclusivity is enforced by the KEY (the identity), never by a field in data.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "identity patient-claim guard aspect",
				Payload:         map[string]any{},
				ExpectedOutcome: "Stored as vtx.identity.<NanoID>.patientClaim; claimed once by CreatePatient's identityKey wiring. A second, different patient claiming the same identity is rejected.",
			},
		},
	}
}

func providerVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     providerVertexDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateProvider", "TombstoneProvider", "SetProviderProfile", "SetProviderHours", "SetProviderTimeOff"},
		Description: "Clinic provider DDL. Vertex shape: vtx.provider.<NanoID>, class=provider, root data = {} " +
			"(minimal, D5 — the data lives in the .profile aspect). CreateProvider mints the provider + writes the " +
			".profile aspect {fullName (required), specialty (required), credentials?, bio?} atomically. " +
			"SetProviderProfile edits an existing provider's .profile — it REPLACES the aspect with the supplied " +
			"{fullName (required), specialty (required), credentials?, bio?} (the editor seeds the form from the " +
			"projected profile so a replace edits the live set; fullName + specialty stay required so the roster " +
			"lens never loses the provider). TombstoneProvider soft-deletes one. SetProviderHours upserts the .hours availability aspect " +
			"{windows: [{day (0=Sun..6=Sat), openSec, closeSec}]} (UTC seconds-of-day) — the opt-in recurring-weekly " +
			"business-hours windows CreateAppointment / RescheduleAppointment enforce (an out-of-hours booking is " +
			"rejected OutsideHours); an absent .hours aspect or windows=[] means the provider is unconstrained. " +
			"SetProviderTimeOff upserts the .timeOff exceptions aspect {ranges: [{from, to, reason?}]} (RFC3339 UTC " +
			"instants) — the opt-in date-specific blackout layer on top of the recurring hours (vacation / holiday / " +
			"out-sick): a booking overlapping any blocked range is rejected ProviderUnavailable, even if it falls " +
			"inside the weekly .hours; an absent .timeOff aspect or ranges=[] means no blackouts.",
		Script: providerDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"fullName":{"type":"string","description":"The provider's full name (CreateProvider / SetProviderProfile; required)."},` +
			`"specialty":{"type":"string","description":"The provider's clinical specialty, e.g. Cardiology (CreateProvider / SetProviderProfile; required)."},` +
			`"credentials":{"type":"string","description":"Post-nominal credentials, e.g. MD (CreateProvider / SetProviderProfile; optional)."},` +
			`"bio":{"type":"string","description":"Short provider bio (CreateProvider / SetProviderProfile; optional)."},` +
			`"providerId":{"type":"string","description":"Optional bare NanoID for the new provider vertex (CreateProvider); absent → minted."},` +
			`"providerKey":{"type":"string","description":"vtx.provider.<NanoID> of an existing provider (TombstoneProvider / SetProviderProfile / SetProviderHours / SetProviderTimeOff; required, validated alive)."},` +
			`"windows":{"type":"array","description":"Availability windows (SetProviderHours; required). Each {day:0-6 (Sun=0), openSec:0-86400, closeSec:0-86400} with openSec<closeSec; UTC seconds-of-day. An empty array clears the constraint.","items":{"type":"object","properties":{"day":{"type":"integer"},"openSec":{"type":"integer"},"closeSec":{"type":"integer"}}}},` +
			`"ranges":{"type":"array","description":"Time-off blackout ranges (SetProviderTimeOff; required). Each {from, to, reason?} with from/to RFC3339 UTC instants and from<to. A booking overlapping any range is rejected (ProviderUnavailable). An empty array clears all blackouts.","items":{"type":"object","properties":{"from":{"type":"string"},"to":{"type":"string"},"reason":{"type":"string"}}}}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.provider.<NanoID> the operation wrote."}}}`,
		FieldDescription: map[string]string{
			"fullName":    "The provider's full name. Stored on the .profile aspect (CreateProvider / SetProviderProfile; required).",
			"specialty":   "The provider's clinical specialty (e.g. Cardiology). Stored on the .profile aspect (CreateProvider / SetProviderProfile; required).",
			"credentials": "Optional post-nominal credentials (e.g. MD, RN). Stored on the .profile aspect when present (CreateProvider / SetProviderProfile).",
			"bio":         "Optional short provider bio. Stored on the .profile aspect when present (CreateProvider / SetProviderProfile).",
			"providerId":  "Optional bare NanoID (no dots / key segments) for the new provider vertex. Absent → minted with nanoid.new().",
			"providerKey": "Full vtx.provider.<NanoID> key of an existing provider vertex (TombstoneProvider tombstones it; SetProviderProfile edits its profile; SetProviderHours sets its recurring availability; SetProviderTimeOff sets its date-specific blackouts).",
			"windows":     "Availability windows (SetProviderHours). A list of {day:0-6 (Sun=0), openSec, closeSec} where openSec/closeSec are UTC seconds-of-day (0..86400) and openSec<closeSec. An empty list clears the constraint (provider becomes unconstrained).",
			"ranges":      "Time-off blackout ranges (SetProviderTimeOff). A list of {from, to, reason?} where from/to are RFC3339 UTC instants and from<to. A booking whose [start,end) overlaps any range is rejected (ProviderUnavailable) even when it falls inside the weekly .hours. An empty list clears all blackouts.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "CreateProvider — register a provider",
				Payload: map[string]any{"fullName": "Dr. Sam Okafor", "specialty": "Cardiology", "credentials": "MD"},
				ExpectedOutcome: "Mints vtx.provider.<NanoID> (class=provider, root {}) + the .profile aspect " +
					"{fullName, specialty, credentials}. Returns primaryKey (the provider key).",
			},
			{
				Name: "SetProviderProfile — edit an existing provider's profile",
				Payload: map[string]any{
					"providerKey": "vtx.provider.<NanoID>",
					"fullName":    "Dr. Samira Okafor",
					"specialty":   "Cardiology",
					"credentials": "MD, FACC",
				},
				ExpectedOutcome: "Validates the provider is alive + class=provider, then REPLACES " +
					"vtx.provider.<NanoID>.profile with {fullName, specialty, credentials?, bio?}. fullName + " +
					"specialty are required (the roster lens keys on fullName); an omitted credentials/bio clears " +
					"that field. Returns primaryKey (the provider key).",
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
			{
				Name: "SetProviderTimeOff — block a vacation week",
				Payload: map[string]any{
					"providerKey": "vtx.provider.<NanoID>",
					"ranges": []any{
						map[string]any{"from": "2026-07-06T00:00:00Z", "to": "2026-07-13T00:00:00Z", "reason": "Vacation"},
					},
				},
				ExpectedOutcome: "Validates the provider is alive + class=provider and each range (from/to RFC3339 " +
					"UTC, from<to), then upserts vtx.provider.<NanoID>.timeOff {ranges}. Subsequent CreateAppointment / " +
					"RescheduleAppointment reject a booking overlapping any range (ProviderUnavailable), even inside the " +
					"weekly .hours. ranges=[] clears all blackouts.",
			},
		},
	}
}

func appointmentVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     appointmentVertexDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateAppointment", "RescheduleAppointment", "SetAppointmentStatus", "RecordEncounter", "TombstoneAppointment"},
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
			"from the .schedule visit reason). The terminal statuses {cancelled, completed, noShow} are FINAL: " +
			"re-setting the same terminal value is idempotent, but changing a terminal status to a different one is " +
			"rejected (TerminalStatus) so a finished / cancelled visit cannot silently revert; non-terminal statuses " +
			"move freely. RecordEncounter upserts the .encounter aspect — the post-visit clinical " +
			"record {summary, assessment?, plan?} (raw clinical content, captured plaintext-for-now under the trusted-tool " +
			"posture, the .demographics discipline — the deferred Vault plane owns its at-rest encryption + display; NEVER " +
			"projected into a read model) plus the OPERATIONAL, non-PHI signals {documentedAt (derived from op.submittedAt), " +
			"followUpRequested, followUpDate?} that the clinicAppointments lens DOES project (presence-of-documentation + " +
			"follow-up scheduling, never the clinical content). TombstoneAppointment soft-deletes the appointment. The " +
			"clinic's booking grid is a mandatory 15-minute cadence (:00/:15/:30/:45; SlotGridViolation if startsAt/endsAt " +
			"misalign, AppointmentTooLong past 24h/96 cells): CreateAppointment AND RescheduleAppointment discretize " +
			"[startsAt,endsAt) into its covered 15-minute cells and CLAIM a deterministic slot-claim aspect per cell on " +
			"BOTH the provider and patient hub vertices (vtx.provider.<p>.slot<cellcode> / vtx.patient.<pt>.slot<cellcode>) " +
			"— the write-path CreateOnly/expectedRevision conditioning on each cell key IS the double-book lock (no read-time " +
			"enumeration, no serialization epoch): a live claim on any covered cell rejects with SlotConflict (provider) or " +
			"PatientDoubleBook (patient) (Capability-KV §06 — the op's own Starlark logic). RescheduleAppointment releases " +
			"the cells the appointment no longer needs and claims the new ones in the same atomic batch (a collision leaves " +
			"the original booking fully intact); SetAppointmentStatus releases all held cells on a terminal transition " +
			"(cancelled/completed/noShow). Both also enforce the provider's opt-in availability windows (the .hours aspect, " +
			"set by SetProviderHours): a booking outside a provider's business hours is rejected (OutsideHours); a provider " +
			"with no .hours is unconstrained. Both also enforce the provider's opt-in date-specific time-off (the .timeOff " +
			"aspect, set by SetProviderTimeOff): a booking overlapping any blackout range is rejected (ProviderUnavailable), " +
			"even when it falls inside the weekly .hours; a provider with no .timeOff is unrestricted. Both also reject a " +
			"startsAt at or before op.submittedAt " +
			"(ScheduleInPast) — a soft past-time guard (submittedAt is caller-supplied; the host clock is " +
			"not exposed to Starlark).",
		Script: appointmentDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"patient":{"type":"string","description":"vtx.patient.<NanoID> the appointment is for (CreateAppointment / RescheduleAppointment; required; on create validated alive + class=patient, on reschedule/terminal-SetAppointmentStatus/TombstoneAppointment it must be the appointment's actual patient, validated via the forPatient link)."},` +
			`"provider":{"type":"string","description":"vtx.provider.<NanoID> the appointment is with (CreateAppointment / RescheduleAppointment; required, validated alive + class=provider; on reschedule/terminal-SetAppointmentStatus/TombstoneAppointment it must be the appointment's actual provider, validated via the withProvider link)."},` +
			`"startsAt":{"type":"string","description":"Appointment start, RFC3339 (CreateAppointment / RescheduleAppointment; required). Caller supplies canonical UTC, aligned to the clinic's 15-minute booking grid (:00/:15/:30/:45; SlotGridViolation otherwise)."},` +
			`"endsAt":{"type":"string","description":"Appointment end, RFC3339 (CreateAppointment / RescheduleAppointment; required). Caller supplies canonical UTC, aligned to the 15-minute grid; span capped at 96 cells / 24h (AppointmentTooLong)."},` +
			`"reason":{"type":"string","description":"Visit reason / chief complaint (CreateAppointment / RescheduleAppointment; optional — on RescheduleAppointment an omitted reason clears it)."},` +
			`"appointmentId":{"type":"string","description":"Optional bare NanoID for the new appointment vertex (CreateAppointment); absent → minted."},` +
			`"appointmentKey":{"type":"string","description":"vtx.appointment.<NanoID> of an existing appointment (RescheduleAppointment / SetAppointmentStatus / TombstoneAppointment; required, validated alive)."},` +
			`"status":{"type":"string","enum":["scheduled","confirmed","checkedIn","completed","cancelled","noShow"],"description":"New status (SetAppointmentStatus; required). Transitioning TO a terminal value (completed/cancelled/noShow) for the first time also requires provider + patient (to release the held slot-claim cells; omitted on a non-terminal transition or an idempotent same-value re-set)."},` +
			`"note":{"type":"string","description":"Optional audit note for the transition, e.g. a cancel / no-show reason (SetAppointmentStatus; optional). Stored on .status, distinct from the .schedule visit reason; an omitted note carries none."},` +
			`"summary":{"type":"string","description":"Visit summary / clinical note (RecordEncounter; required). RAW clinical content — captured plaintext-for-now (the .demographics PHI discipline), NEVER projected into a read model (the deferred Vault plane owns display)."},` +
			`"assessment":{"type":"string","description":"Clinical assessment / diagnosis (RecordEncounter; optional). RAW PHI — captured, never projected."},` +
			`"plan":{"type":"string","description":"Treatment plan / orders (RecordEncounter; optional). RAW PHI — captured, never projected. The clinical reason for any follow-up belongs here, not in the operational followUp fields."},` +
			`"followUpRequested":{"type":"boolean","description":"Whether the visit calls for a follow-up (RecordEncounter; optional, default false). OPERATIONAL, non-PHI — projected (the existence of a follow-up, like an appointment time)."},` +
			`"followUpDate":{"type":"string","description":"Suggested follow-up date, RFC3339 / date (RecordEncounter; optional). OPERATIONAL, non-PHI — projected only when followUpRequested is true."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.appointment.<NanoID> the operation wrote."}}}`,
		FieldDescription: map[string]string{
			"patient":           "Full vtx.patient.<NanoID> key the appointment is for. CreateAppointment validates it is alive + class=patient, writes the forPatient link, and claims a patientSlotClaim aspect per covered 15-minute cell. RescheduleAppointment / a terminal SetAppointmentStatus / TombstoneAppointment also require it (the appointment's actual patient, validated via the forPatient link) to release/claim cells against the patient's other bookings (PatientDoubleBook).",
			"provider":          "Full vtx.provider.<NanoID> key the appointment is with. CreateAppointment validates it is alive + class=provider, writes the withProvider link, and claims a providerSlotClaim aspect per covered 15-minute cell. RescheduleAppointment / a terminal SetAppointmentStatus / TombstoneAppointment also require it (the appointment's actual provider, validated via the withProvider link) to release/claim cells against the provider's other bookings (SlotConflict).",
			"startsAt":          "Appointment start (RFC3339, canonical UTC). Stored on the .schedule aspect (CreateAppointment / RescheduleAppointment; required). Must be in the future relative to op.submittedAt — a past / now startsAt is rejected (ScheduleInPast). Must align to the 15-minute grid (SlotGridViolation).",
			"endsAt":            "Appointment end (RFC3339, canonical UTC). Stored on the .schedule aspect (CreateAppointment / RescheduleAppointment; required). Must align to the 15-minute grid; span capped at 96 cells / 24h (AppointmentTooLong).",
			"reason":            "Optional visit reason / chief complaint. Stored on the .schedule aspect when present (CreateAppointment / RescheduleAppointment; on RescheduleAppointment an omitted reason clears it).",
			"appointmentId":     "Optional bare NanoID (no dots / key segments) for the new appointment vertex. Absent → minted with nanoid.new().",
			"appointmentKey":    "Full vtx.appointment.<NanoID> key of an existing appointment (RescheduleAppointment rewrites its .schedule; SetAppointmentStatus validates it alive + class=appointment; TombstoneAppointment validates it alive).",
			"status":            "New appointment status, one of {scheduled, confirmed, checkedIn, completed, cancelled, noShow} (SetAppointmentStatus; required). The first transition to a terminal value also requires provider + patient.",
			"note":              "Optional audit note recorded with a SetAppointmentStatus transition (e.g. a cancel / no-show reason). Stored on the .status aspect, distinct from the .schedule visit reason; omitted → no note.",
			"summary":           "Required visit summary / clinical note (RecordEncounter). RAW clinical content — captured plaintext-for-now under the trusted-tool posture (the .demographics PHI discipline) and NEVER projected into a read model; the deferred Vault plane owns its at-rest encryption + display.",
			"assessment":        "Optional clinical assessment / diagnosis (RecordEncounter). RAW PHI — captured, never projected.",
			"plan":              "Optional treatment plan / orders (RecordEncounter). RAW PHI — captured, never projected. The clinical reason for a follow-up lives here, not in the operational followUp fields.",
			"followUpRequested": "Optional boolean (default false): does this visit need a follow-up (RecordEncounter)? OPERATIONAL, non-PHI — projected into clinicAppointments (the existence of a follow-up, like an appointment time, is not clinical content).",
			"followUpDate":      "Optional suggested follow-up date (RFC3339 / date) (RecordEncounter). OPERATIONAL, non-PHI — projected only when followUpRequested is true.",
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
				ExpectedOutcome: "Validates the patient (class=patient) + provider (class=provider) are alive and " +
					"startsAt/endsAt align to the 15-minute grid. Atomically commits vtx.appointment.<NanoID> (root {}) + " +
					".schedule {startsAt, endsAt, remindAt, reason} (remindAt = startsAt − 24h, derived) + .status " +
					"{value: scheduled} + the forPatient + withProvider links + one providerSlotClaim/patientSlotClaim " +
					"aspect per covered 15-minute cell. Returns primaryKey (the appointment key). Rejects with ScriptError " +
					"if the patient or provider is absent / dead / the wrong class, a misaligned start/end " +
					"(SlotGridViolation), a provider double-book (SlotConflict), or a patient double-book across " +
					"providers (PatientDoubleBook).",
			},
			{
				Name: "RescheduleAppointment — move an appointment to a new time",
				Payload: map[string]any{
					"appointmentKey": "vtx.appointment.<NanoID>",
					"provider":       "vtx.provider.<providerNanoID>",
					"patient":        "vtx.patient.<patientNanoID>",
					"startsAt":       "2026-07-02T16:00:00Z",
					"endsAt":         "2026-07-02T16:30:00Z",
					"reason":         "Annual checkup",
				},
				ExpectedOutcome: "Validates the appointment is alive + class=appointment, that the passed provider / " +
					"patient are its actual provider / patient (via the withProvider / forPatient links), and that the new " +
					"startsAt/endsAt align to the 15-minute grid, then rewrites the .schedule aspect {startsAt, endsAt, " +
					"remindAt, reason?} with the new times — re-deriving remindAt = startsAt − 24h (canonical UTC) so the " +
					"clinic-reminders @at re-arms for a not-yet-sent reminder. In the same atomic batch it releases the " +
					"provider/patient slot-claim cells the appointment no longer needs and claims the newly-covered ones, " +
					"conflict-checked against both the provider's book (SlotConflict) and the patient's book " +
					"(PatientDoubleBook) — a collision leaves the original booking's claims fully intact. The forPatient / " +
					"withProvider links + the .status aspect are untouched. An omitted reason clears it (the caller carries " +
					"the existing reason). Returns primaryKey.",
			},
			{
				Name:    "SetAppointmentStatus — confirm an appointment",
				Payload: map[string]any{"appointmentKey": "vtx.appointment.<NanoID>", "status": "confirmed"},
				ExpectedOutcome: "Validates the appointment is alive + class=appointment, then upserts the .status aspect " +
					"{value: confirmed} (unconditioned — re-runnable). Returns primaryKey. Rejects a status outside the enum.",
			},
			{
				Name: "RecordEncounter — document a completed visit",
				Payload: map[string]any{
					"appointmentKey":    "vtx.appointment.<NanoID>",
					"summary":           "Patient seen for annual checkup; vitals normal.",
					"assessment":        "Essential hypertension, well-controlled.",
					"plan":              "Continue current medication; recheck in 6 months.",
					"followUpRequested": true,
					"followUpDate":      "2027-01-15T15:00:00Z",
				},
				ExpectedOutcome: "Validates the appointment is alive + class=appointment, then upserts the .encounter aspect — the " +
					"RAW clinical record {summary, assessment?, plan?} captured plaintext-for-now (NEVER projected, the .demographics " +
					"PHI discipline) plus the OPERATIONAL signals {documentedAt (= op.submittedAt, canonical UTC), followUpRequested, " +
					"followUpDate?} that the clinicAppointments lens DOES project. Unconditioned upsert (re-runnable — a provider can " +
					"correct the note). Returns primaryKey.",
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
			"patientDemographics) = {fullName}. NON-sensitive (it attaches to a patient, not an identity) and " +
			"carries no contact PII — that lives on the identity CreatePatient's optional identityKey links via " +
			"identifiedBy, the deferred Vault plane's unit. Written ONLY by CreatePatient (whose " +
			"patient vertexType DDL owns the script); this aspect-type DDL is the step-6 write gate. " +
			"Declaration-only: no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"fullName":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"fullName": "The patient's full name.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "patient demographics aspect",
				Payload:         map[string]any{"fullName": "Alice Rivera"},
				ExpectedOutcome: "Stored as vtx.patient.<NanoID>.demographics; written by CreatePatient.",
			},
		},
	}
}

// profileAspectTypeDDL declares the .profile aspect (class providerProfile) — the
// step-6 write gate for CreateProvider + SetProviderProfile. Declaration-only;
// NON-sensitive.
func profileAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     profileAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateProvider", "SetProviderProfile"},
		Description: "Provider profile aspect (clinic). Stored as vtx.provider.<NanoID>.profile (class " +
			"providerProfile) = {fullName, specialty, credentials?, bio?}. Non-sensitive. Written by " +
			"CreateProvider (mints it) and SetProviderProfile (replaces it) — both owned by the provider " +
			"vertexType DDL script; this aspect-type DDL is the step-6 write gate. Declaration-only: no op handler.",
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
			"conflict-checks the booking by claiming a slot-claim aspect per covered 15-minute cell on the provider and " +
			"patient hubs (double-book rejection) and the provider's opt-in .hours availability windows (OutsideHours " +
			"rejection).",
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

// providerSlotClaimAspectTypeDDL declares the .slot<cellcode> aspect (class
// providerSlotClaim) — a deterministic per-15-minute-cell existence marker on the
// provider hub. The step-6 write gate for CreateAppointment / RescheduleAppointment /
// SetAppointmentStatus / TombstoneAppointment (create / release / re-claim).
// Declaration-only; NON-sensitive.
// One aspect per occupied grid cell, created ON DEMAND — never pre-seeded by
// CreateProvider, so there is no "must exist before declared read" constraint (a
// claim aspect need not pre-exist for any provider). Its data is {} — a pure
// existence marker, no
// relationship field: the key ITSELF (identical across two competing bookings for
// the same cell) is the lock — CreateOnly/expectedRevision at commit is the safety
// property, not the lazy kv.Read that picks the mutation verb (design
// clinic-booking-write-path-slot-claims-design.md §2.1/§2.4).
func providerSlotClaimAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     providerSlotClaimAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateAppointment", "RescheduleAppointment", "SetAppointmentStatus", "TombstoneAppointment"},
		Description: "Provider 15-minute slot-claim aspect (clinic). Stored as vtx.provider.<NanoID>.slot<cellcode> " +
			"(class providerSlotClaim) = {} — a pure existence marker, no relationship field. <cellcode> is the cell's " +
			"canonical whole-second UTC start with '-'/':' stripped and lowercased (e.g. 2026-07-03T09:00:00Z → " +
			"slot20260703t090000z). CreateAppointment claims one per covered cell (CreateOnly — the key collision across " +
			"two concurrent bookings for the same cell IS the double-book lock: SlotConflict on commit-time rejection); " +
			"RescheduleAppointment releases cells the move no longer needs and claims the newly-covered ones in the same " +
			"atomic batch; SetAppointmentStatus tombstones all held cells on a terminal transition (cancelled/completed/" +
			"noShow), freeing them; TombstoneAppointment tombstones all held cells on a hard delete, regardless of " +
			"status. Non-sensitive; created on demand, no CreateProvider init needed. Declaration-only: no op handler.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"data": "Always {} — a pure existence marker. The claim's job is done by the KEY (hub + deterministic cellcode), never by a field in data.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "provider slot-claim aspect",
				Payload:         map[string]any{},
				ExpectedOutcome: "Stored as vtx.provider.<NanoID>.slot<cellcode>; claimed by CreateAppointment/RescheduleAppointment, released by RescheduleAppointment (vacated cells) / SetAppointmentStatus (terminal transition).",
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

// timeOffAspectTypeDDL declares the .timeOff exceptions aspect (class
// providerTimeOff) — the step-6 write gate for SetProviderTimeOff. Declaration-only
// (the script lives on the provider vertexType DDL). NON-sensitive (operational, not
// PHI). The aspect is OPT-IN: a provider with no .timeOff (or ranges=[]) has no
// blackouts, so this is backward-compatible with providers created before the
// capability shipped. It is the date-specific blackout LAYER on top of the recurring
// weekly .hours: a booking must satisfy BOTH (inside an .hours window AND outside
// every .timeOff range). CreateAppointment / RescheduleAppointment read it on demand
// (kv.Read, §2.5 — NOT a declared/OCC read: time-off is config, not a concurrency
// serialization point) to reject a booking overlapping any blocked range
// (ProviderUnavailable). Ranges are canonical-UTC RFC3339 instants so the half-open
// overlap test is the same lexical-==-chronological compare CreateAppointment uses
// for double-book detection.
func timeOffAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     timeOffAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"SetProviderTimeOff"},
		Description: "Provider time-off exceptions aspect (clinic). Stored as vtx.provider.<NanoID>.timeOff (class " +
			"providerTimeOff) = {ranges: [{from, to, reason?}]} where from/to are canonical-UTC RFC3339 instants with " +
			"from<to. Non-sensitive (operational, not PHI). OPT-IN: an absent aspect or ranges=[] means no blackouts. " +
			"The date-specific blackout LAYER on top of the recurring weekly .hours — a booking must be inside an .hours " +
			"window AND outside every .timeOff range. Written ONLY by SetProviderTimeOff (whose provider vertexType DDL " +
			"owns the script); this aspect-type DDL is the step-6 write gate. Read on demand by CreateAppointment / " +
			"RescheduleAppointment to reject a booking overlapping any range (ProviderUnavailable). Declaration-only: no op handler.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"ranges":{"type":"array","items":{"type":"object","properties":{"from":{"type":"string"},"to":{"type":"string"},"reason":{"type":"string"}}}}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"ranges": "Time-off blackout ranges: a list of {from, to, reason?} (RFC3339 UTC instants, from<to). A booking whose [start,end) overlaps any range is rejected (ProviderUnavailable).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "provider time-off aspect",
				Payload:         map[string]any{"ranges": []any{map[string]any{"from": "2026-07-06T00:00:00Z", "to": "2026-07-13T00:00:00Z", "reason": "Vacation"}}},
				ExpectedOutcome: "Stored as vtx.provider.<NanoID>.timeOff; written by SetProviderTimeOff; enforced by CreateAppointment / RescheduleAppointment.",
			},
		},
	}
}

// patientSlotClaimAspectTypeDDL declares the .slot<cellcode> aspect on a PATIENT
// (class patientSlotClaim) — the symmetric analog of providerSlotClaimAspectTypeDDL.
// Catches a patient booked with TWO DIFFERENT providers at the same instant, which
// the provider-side claim set alone cannot see (each provider's cells are disjoint
// keys). Declaration-only; NON-sensitive; carries no relationship field.
func patientSlotClaimAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     patientSlotClaimAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateAppointment", "RescheduleAppointment", "SetAppointmentStatus", "TombstoneAppointment"},
		Description: "Patient 15-minute slot-claim aspect (clinic). Stored as vtx.patient.<NanoID>.slot<cellcode> " +
			"(class patientSlotClaim) = {} — a pure existence marker, no relationship field. Same cellcode derivation, " +
			"claim/release lifecycle, and CreateOnly-is-the-lock property as providerSlotClaim, contended on the patient " +
			"hub — it catches a patient double-booked across TWO DIFFERENT providers at the same instant, which the " +
			"provider-side claim set alone cannot see (each provider's cells are disjoint keys). Non-sensitive; created on " +
			"demand. Declaration-only: no op handler.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"data": "Always {} — a pure existence marker. The claim's job is done by the KEY (hub + deterministic cellcode), never by a field in data.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "patient slot-claim aspect",
				Payload:         map[string]any{},
				ExpectedOutcome: "Stored as vtx.patient.<NanoID>.slot<cellcode>; claimed by CreateAppointment/RescheduleAppointment, released by RescheduleAppointment (vacated cells) / SetAppointmentStatus (terminal transition).",
			},
		},
	}
}

// encounterAspectTypeDDL declares the .encounter aspect (class appointmentEncounter)
// — the post-visit clinical record, the step-6 write gate for RecordEncounter
// (whose appointment vertexType DDL owns the script). Declaration-only.
//
// NON-sensitive in the step-6 sense (it attaches to a vtx.appointment, not an
// identity — sensitiveAspectScope forbids a sensitive aspect on a non-identity
// vertex anyway), BUT it carries RAW CLINICAL PHI (summary / assessment / plan) and
// is therefore stored plaintext-for-now ONLY under the trusted-tool posture, the
// SAME discipline as patient .demographics: the deferred Vault plane owns its
// at-rest encryption + right-to-be-forgotten + display. The clinic vertical is the
// Vault forcing function — clinical-record display is the validating flow that
// pulls Vault into existence. The clinicAppointments lens DELIBERATELY projects
// ONLY the operational, non-PHI fields (documentedAt / followUpRequested /
// followUpDate) and NEVER the clinical content — the .demographics name-only
// precedent applied to the encounter.
func encounterAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     encounterAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"RecordEncounter"},
		Description: "Appointment encounter aspect (clinic). Stored as vtx.appointment.<NanoID>.encounter (class " +
			"appointmentEncounter) = {summary, assessment?, plan?, documentedAt, followUpRequested, followUpDate?}. The " +
			"post-visit clinical record, written by RecordEncounter (whose appointment vertexType DDL owns the script). " +
			"summary / assessment / plan are RAW clinical PHI — captured plaintext-for-now under the trusted-tool posture " +
			"(the .demographics discipline) and NEVER projected into a read model (the deferred Vault plane owns display). " +
			"documentedAt / followUpRequested / followUpDate are OPERATIONAL, non-PHI signals the clinicAppointments lens " +
			"DOES project (documentation-presence + follow-up scheduling). Declaration-only: no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"summary":{"type":"string"},"assessment":{"type":"string"},"plan":{"type":"string"},` +
			`"documentedAt":{"type":"string"},"followUpRequested":{"type":"boolean"},"followUpDate":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"summary":           "Visit summary / clinical note (RAW PHI — never projected).",
			"assessment":        "Clinical assessment / diagnosis (RAW PHI — never projected).",
			"plan":              "Treatment plan / orders (RAW PHI — never projected).",
			"documentedAt":      "When the visit was documented (RFC3339, = op.submittedAt). Operational — projected.",
			"followUpRequested": "Whether a follow-up is needed. Operational — projected.",
			"followUpDate":      "Suggested follow-up date (RFC3339 / date). Operational — projected when followUpRequested.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "appointment encounter aspect",
				Payload:         map[string]any{"summary": "Annual checkup, vitals normal.", "documentedAt": "2026-07-01T15:30:00Z", "followUpRequested": false},
				ExpectedOutcome: "Stored as vtx.appointment.<NanoID>.encounter; written by RecordEncounter. Clinical fields never projected.",
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
    # Endpoint validation: the linked vertex MUST be alive AND the expected
    # class. A dead or wrong-class identityKey is never wired.
    if not vertex_alive(state, key):
        fail("UnknownEndpoint: " + name + ": " + key + " is absent or tombstoned")
    cls = class_of(state, key)
    if cls != want_class:
        fail("WrongClass: " + name + ": " + key + " has class " + str(cls) + ", required " + want_class)

def claim_identity(identity_key):
    # Global exclusivity guard (Capability-KV §06 — pure existence-uniqueness, no
    # list needed): at most one patient may ever claim a given identity (nothing
    # releases the claim, so it is never tombstoned). kv.Read here is LAZY (§2.5
    # idiom, same as the appointment DDL's claim_cell) — it only picks the error
    # message; the safety property is the atomic batch's CreateOnly conditioning at
    # commit: two DIFFERENT patients passing the same identityKey both read it
    # absent and both emit op:create for the IDENTICAL key, but CreateOnly on a key
    # at revision 0 commits exactly once — the loser's whole batch RevisionConflicts
    # (fail closed, never a silent double-claim).
    # read-posture: (d) declared in contextHint.optionalReads by CreatePatient's
    # dispatcher (cmd/clinic-app/web/app.js)
    existing = kv.Read(identity_key + ".patientClaim")
    if existing != None:
        fail("IdentityAlreadyClaimed: " + identity_key + " is already linked to another patient")
    return make_aspect(identity_key, "patientClaim", "identityPatientClaim", {})

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreatePatient":
        full_name = required_string(p, "fullName")
        pid = bare_nanoid_or_mint(p, "patientId")
        pkey = "vtx.patient." + pid
        demo = {"fullName": full_name}
        mutations = [
            make_vtx(pkey, "patient", {}),
            make_aspect(pkey, "demographics", "patientDemographics", demo),
        ]
        # Optional identifiedBy link to a pre-minted identity carrying the
        # patient's sensitive contact (Vault plane). The patient (later-
        # arriving) is the source, the pre-existing identity is the target
        # (Contract #1 §1.1). Sentence: "patient identifiedBy identity".
        identity_key = optional_string(p, "identityKey")
        if identity_key != None:
            _, identity_id = parts_of(identity_key, "identityKey", "identity")
            require_live_typed(state, identity_key, "identityKey", "identity")
            identified_by_lnk = "lnk.patient." + pid + ".identifiedBy.identity." + identity_id
            mutations.append(make_link(identified_by_lnk, pkey, identity_key, "identifiedBy", "identifiedBy", {}))
            # Global claim guard: at most one patient may ever wire the SAME
            # identity (else two roster rows would decrypt/display the same
            # person's contact). See claim_identity.
            mutations.append(claim_identity(identity_key))
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
        # write-path claim key.
        mutations = [make_aspect_upsert(prkey, "hours", "providerHours", {"windows": clean})]
        events = [{"class": "clinic.providerHoursSet",
                   "data": {"providerKey": prkey, "windowCount": len(clean)}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": prkey}}

    if ot == "SetProviderTimeOff":
        prkey = required_string(p, "providerKey")
        parts_of(prkey, "providerKey", "provider")
        if not vertex_alive(state, prkey):
            fail("UnknownProvider: " + prkey)
        cls = class_of(state, prkey)
        if cls != "provider":
            fail("WrongClass: providerKey: " + prkey + " has class " + str(cls) + ", required provider")
        # ranges is required (pass [] to clear all blackouts). Each range is
        # {from, to, reason?} — from/to RFC3339 UTC instants with from<to. Normalize
        # both to canonical whole-second UTC (time.rfc3339_utc — pure, no clock read)
        # so the stored ranges compare lexically == chronologically (the overlap test
        # CreateAppointment runs against them is sound for any caller offset).
        if not hasattr(p, "ranges"):
            fail("InvalidArgument: ranges: required (use [] to clear)")
        ranges = getattr(p, "ranges")
        if type(ranges) != type([]):
            fail("InvalidArgument: ranges: must be a list")
        clean = []
        for r in ranges:
            if type(r) != type({}):
                fail("InvalidArgument: ranges: each range must be an object; got " + type(r))
            rf = r.get("from")
            rt = r.get("to")
            if rf == None or type(rf) != type("") or len(rf.strip()) == 0:
                fail("InvalidArgument: ranges: from: required non-empty RFC3339 string")
            if rt == None or type(rt) != type("") or len(rt.strip()) == 0:
                fail("InvalidArgument: ranges: to: required non-empty RFC3339 string")
            cf = time.rfc3339_utc(rf.strip())
            ct = time.rfc3339_utc(rt.strip())
            if not (cf < ct):
                fail("InvalidArgument: ranges: from must be < to; got from=" + cf + " to=" + ct)
            cr = {"from": cf, "to": ct}
            reason = r.get("reason")
            if reason != None and type(reason) == type("") and len(reason.strip()) > 0:
                cr["reason"] = reason.strip()
            clean.append(cr)
        # Unconditioned upsert of the WHOLE .timeOff aspect (create-if-absent — it is
        # opt-in, CreateProvider does not init it). No OCC: time-off is config, not a
        # write-path claim key.
        mutations = [make_aspect_upsert(prkey, "timeOff", "providerTimeOff", {"ranges": clean})]
        events = [{"class": "clinic.providerTimeOffSet",
                   "data": {"providerKey": prkey, "rangeCount": len(clean)}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": prkey}}

    if ot == "SetProviderProfile":
        prkey = required_string(p, "providerKey")
        parts_of(prkey, "providerKey", "provider")
        if not vertex_alive(state, prkey):
            fail("UnknownProvider: " + prkey)
        cls = class_of(state, prkey)
        if cls != "provider":
            fail("WrongClass: providerKey: " + prkey + " has class " + str(cls) + ", required provider")
        full_name = required_string(p, "fullName")
        specialty = required_string(p, "specialty")
        profile = {"fullName": full_name, "specialty": specialty}
        credentials = optional_string(p, "credentials")
        if credentials != None:
            profile["credentials"] = credentials
        bio = optional_string(p, "bio")
        if bio != None:
            profile["bio"] = bio
        # Unconditioned upsert REPLACING the whole .profile aspect (it always exists —
        # CreateProvider mints it). The editor seeds the form from the projected
        # profile (fullName/specialty/credentials/bio all carried by clinicProviders),
        # so a replace edits the live set; fullName + specialty stay required so the
        # provider never loses the fields its roster lens (WHERE fullName <> null) and
        # booking picker depend on. Mirrors SetProviderHours / SetProviderTimeOff.
        mutations = [make_aspect_upsert(prkey, "profile", "providerProfile", profile)]
        events = [{"class": "clinic.providerProfileSet", "data": {"providerKey": prkey}}]
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
// .status aspect (no read-merge — status is its own aspect).
//
// Double-book detection is WRITE-PATH deterministic-key claims, not read-time
// enumeration (design clinic-booking-write-path-slot-claims-design.md): the
// clinic's booking grid is a mandatory 15-minute cadence, so [startsAt,endsAt)
// discretizes losslessly into a finite set of grid cells. CreateAppointment claims
// one providerSlotClaim / patientSlotClaim aspect per covered cell on the provider
// and patient hubs (vtx.<hub>.<slotcellcode>, deterministic — the SAME key across
// two competing bookings for the same cell); the key collision at commit
// (CreateOnly / expectedRevision) IS the lock, not a read+epoch pair. Reschedule
// releases the cells the move no longer needs and claims the newly-covered ones in
// the SAME atomic batch (a collision leaves the original booking's claims intact);
// SetAppointmentStatus and TombstoneAppointment release all held cells on a
// terminal transition / hard delete (recomputed from .schedule + the caller-
// supplied, link-validated provider/patient — never a stored back-reference).
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
    # read-posture: (c) config — deliberately unsnapshotted (out of OCC so an
    # hours edit never conflicts a concurrent booking commit)
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

def enforce_time_off(provider, starts_at, ends_at):
    # Opt-in provider date-specific time-off (Capability-KV §06 — the op's own
    # Starlark logic). The blackout LAYER on top of enforce_hours: read the provider's
    # .timeOff aspect on demand (kv.Read, §2.5 — config, not the booking serialization
    # point). An absent / deleted aspect or ranges=[] means NO blackouts (backward-
    # compatible with providers created before this capability). Otherwise the
    # appointment's [start, end) must not overlap ANY blocked [from, to) range. Ranges
    # are canonical-UTC RFC3339 (lexical == chronological); half-open overlap test
    # (a.start < b.end AND b.start < a.end) — back-to-back (appt ending exactly at a
    # range's from, or starting exactly at its to) does NOT overlap, so a booking up
    # to the start of a blackout (or from its end) is allowed.
    # read-posture: (c) config — deliberately unsnapshotted (out of OCC so a
    # time-off edit never conflicts a concurrent booking commit)
    off = kv.Read(provider + ".timeOff")
    if off == None or off.isDeleted:
        return
    ranges = off.data.get("ranges")
    if ranges == None or type(ranges) != type([]) or len(ranges) == 0:
        return
    for r in ranges:
        if type(r) != type({}):
            continue
        rf = r.get("from")
        rt = r.get("to")
        if rf == None or rt == None:
            continue
        if starts_at < rt and rf < ends_at:
            fail("ProviderUnavailable: provider " + provider + " is on time-off " + rf + "/" + rt + "; requested " + starts_at + "/" + ends_at)

def enforce_future(starts_at, submitted_at):
    # Soft past-time guard (Capability-KV §06 — the op's own Starlark logic). The
    # booking MUST start strictly after op.submittedAt: a past / now booking is
    # almost always a mistake AND its remindAt = startsAt − 24h would also be past,
    # so the clinic-reminders @at lane would silently never fire a useful reminder.
    # submittedAt is caller-supplied (the host clock is intentionally NOT exposed to
    # Starlark, starlark_runner.go), so this is a SOFT guard appropriate to the
    # trusted single-identity model — not a hard temporal authority. Normalize it to
    # canonical whole-second UTC (time.rfc3339_utc — pure, no clock read) so the
    # compare is sound for any offset; canonical-UTC RFC3339 compares lexically ==
    # chronologically.
    submitted = time.rfc3339_utc(submitted_at)
    if not (submitted < starts_at):
        fail("ScheduleInPast: startsAt " + starts_at + " is not in the future (submitted " + submitted + ")")

def optional_bool(p, name):
    # Default False (absent / null / non-bool → False).
    if not hasattr(p, name):
        return False
    v = getattr(p, name)
    if v == None or type(v) != type(True):
        return False
    return v

def normalize_follow_up_date(s):
    # The clinic FE captures followUpDate as an HTML <input type=date> → a
    # date-only "YYYY-MM-DD". A downstream follow-up reminder's @at timer needs a
    # full RFC3339 instant (Weaver's temporal lane parses the lens freshUntil as
    # RFC3339, and time.rfc3339_utc itself rejects a bare date), so a date-only
    # value is anchored to 09:00:00Z — "the morning of" the follow-up date. A caller
    # that already supplies a full RFC3339 instant is normalized to canonical UTC so
    # the stored followUpDate is byte-stable for the remindedFor <> followUpDate
    # convergence compare. Storing a full instant is backward-compatible: the FE
    # slices followUpDate to its first 10 chars (the date) everywhere it renders it.
    if "T" not in s and len(s) == 10:
        s = s + "T09:00:00Z"
    return time.rfc3339_utc(s)

APPOINTMENT_STATUSES = ["scheduled", "confirmed", "checkedIn", "completed", "cancelled", "noShow"]
TERMINAL_STATUSES = ["cancelled", "completed", "noShow"]

GRID_MINUTES_STR = ["00", "15", "30", "45"]
GRID_STEP = "15m"
MAX_SLOT_CELLS = 96  # 24h of 15-minute cells -- a generous backstop, not an expected ceiling

def required_status(p):
    s = required_string(p, "status")
    if s not in APPOINTMENT_STATUSES:
        fail("InvalidArgument: status: must be one of scheduled, confirmed, checkedIn, completed, cancelled, noShow; got " + s)
    return s

def enforce_grid(starts_at, ends_at):
    # The clinic's booking grid is a mandatory 15-minute cadence (product decision,
    # Andrew 2026-07-02): every legal appointment boundary must sit on a cell edge, so
    # slot_cells' discretization below is lossless. Canonical whole-second UTC is
    # fixed-width (YYYY-MM-DDTHH:MM:SSZ, 20 chars), so the minute/second fields are
    # sliced directly rather than adding a new time builtin.
    for label, t in [("startsAt", starts_at), ("endsAt", ends_at)]:
        if len(t) != 20:
            fail("SlotGridViolation: " + label + ": must be a canonical whole-second UTC instant; got " + t)
        if t[17:19] != "00" or t[14:16] not in GRID_MINUTES_STR:
            fail("SlotGridViolation: " + label + " must align to the clinic's 15-minute booking grid (:00/:15/:30/:45); got " + t)

def slot_cells(starts_at, ends_at):
    # Enumerate the half-open [starts_at, ends_at) interval's covered 15-minute grid
    # cells (Capability-KV §06 — the op's own Starlark logic). Both endpoints are
    # grid-aligned (enforce_grid), so every cell boundary is exact. Starlark has no
    # while-loop, so a bounded for-range + a fail-closed "still more" check enumerates
    # the whole set without ever silently truncating (MAX_SLOT_CELLS is a generous
    # 24h backstop, not an expected ceiling).
    cells = []
    cur = starts_at
    for _i in range(MAX_SLOT_CELLS + 1):
        if not (cur < ends_at):
            return cells
        cells.append(cur)
        cur = time.rfc3339_add(cur, GRID_STEP)
    fail("AppointmentTooLong: appointment spans more than " + str(MAX_SLOT_CELLS) + " 15-minute slots (24h); shorten the interval")

def slot_cellcode(cell_start):
    # A localName-legal ([a-z][a-zA-Z0-9]*) encoding of a cell's canonical UTC start —
    # deterministic and identical across every writer competing for the same cell.
    return cell_start.replace("-", "").replace(":", "").lower()

def claim_cell(hub, cellcode, cls, conflict_code, who):
    # kv.Read here is LAZY (§2.5 idiom, same as enforce_hours/enforce_time_off) — it
    # only decides which mutation verb to emit (create / CAS-revive / reject); it is
    # NOT itself the safety property. The safety property is the atomic batch's
    # CreateOnly / expectedRevision conditioning at commit: two concurrent claims for
    # the same cell both read it absent and both emit op:create, but CreateOnly on a
    # key with revision 0 commits exactly once — the loser's whole batch rejects
    # (RevisionConflict), the Processor retries, and the retry's kv.Read now sees the
    # winner's live cell and fails closed.
    # read-posture: (d) declared in contextHint.optionalReads by CreateAppointment /
    # RescheduleAppointment's dispatcher (cmd/clinic-app/web/app.js slotClaimKeys)
    key = hub + ".slot" + cellcode
    existing = kv.Read(key)
    if existing != None and not existing.isDeleted:
        fail(conflict_code + ": " + who + " " + hub + " slot " + cellcode + " is already booked")
    if existing != None and existing.isDeleted:
        return make_aspect_upsert_occ(hub, "slot" + cellcode, cls, {}, existing.revision)
    return make_aspect(hub, "slot" + cellcode, cls, {})

def require_matching_provider(appt_id, provider):
    # Validates the caller-supplied provider is THIS appointment's actual provider by
    # reading the deterministic withProvider link (kv.Read, §2.5) — a live link proves
    # the relationship. Used wherever an op must recompute the appointment's cell set
    # (Reschedule / terminal SetAppointmentStatus / TombstoneAppointment) without a
    # stored back-reference (Contract #1: no relationship-as-key-list in aspect data).
    _, provider_id = parts_of(provider, "provider", "provider")
    with_provider_lnk = "lnk.appointment." + appt_id + ".withProvider.provider." + provider_id
    # read-posture: (a) declared in contextHint.reads by every caller's dispatcher —
    # RescheduleAppointment (cmd/clinic-app/web/app.js submitReschedule),
    # SetAppointmentStatus's terminal branch (setStatus), TombstoneAppointment
    # (packages/clinic-domain/integration_test.go clSubmit calls, its only caller)
    wp = kv.Read(with_provider_lnk)
    if wp == None or wp.isDeleted:
        fail("WrongProvider: provider " + provider + " is not the provider of appointment vtx.appointment." + appt_id)
    return provider_id

def require_matching_patient(appt_id, patient):
    # Symmetric analog of require_matching_provider over the forPatient link.
    _, patient_id = parts_of(patient, "patient", "patient")
    for_patient_lnk = "lnk.appointment." + appt_id + ".forPatient.patient." + patient_id
    # read-posture: (a) declared in contextHint.reads by every caller's dispatcher —
    # RescheduleAppointment (cmd/clinic-app/web/app.js submitReschedule),
    # SetAppointmentStatus's terminal branch (setStatus), TombstoneAppointment
    # (packages/clinic-domain/integration_test.go clSubmit calls, its only caller)
    fp = kv.Read(for_patient_lnk)
    if fp == None or fp.isDeleted:
        fail("WrongPatient: patient " + patient + " is not the patient of appointment vtx.appointment." + appt_id)
    return patient_id

def release_cells_mutations(provider, patient, sched):
    # Recompute the appointment's held cells from its OWN .schedule aspect (never a
    # stored back-reference) and tombstone both hubs' claim aspects for each — an
    # UNCONDITIONED tombstone (no expectedRevision): a stale-tombstone race here can
    # only ever free a cell a step early, never silently keep two live claims open,
    # so it is not a correctness hole (design §2.6). Returns [] if the schedule is
    # missing/malformed (defensive — should not happen for a live appointment).
    if sched == None or sched.isDeleted:
        return []
    s_starts = sched.data.get("startsAt")
    s_ends = sched.data.get("endsAt")
    if s_starts == None or s_ends == None:
        return []
    out = []
    for c in slot_cells(s_starts, s_ends):
        cc = slot_cellcode(c)
        out.append(make_tombstone(provider + ".slot" + cc))
        out.append(make_tombstone(patient + ".slot" + cc))
    return out

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

        # Patient-self (consumer's scope=self grant only): step 3 authorizes
        # scope=self by checking authContext.target == actor (Contract #6), but
        # the op's endpoint is the PATIENT vertex, not an identity — step 3 never
        # sees the payload and has no notion of "this patient's identity" anyway.
        # The script closes the gap by requiring the target identity to be THIS
        # patient's linked identity (lnk.patient.<id>.identifiedBy.identity.<id>).
        # A consumer whose own identity is not the patient's identifiedBy link is
        # rejected, even if they satisfy step 3 by naming themselves as
        # authContext.target. Empty for the standing operator grant (scope=any
        # never sets authContext), so this check is a no-op there — operator
        # keeps booking on behalf of any patient, exactly as its own grant
        # (unconstrained by scope) allows.
        if op.authContextTarget != "":
            _, target_identity_id = parts_of(op.authContextTarget, "authContextTarget", "identity")
            identified_by_lnk = "lnk.patient." + patient_id + ".identifiedBy.identity." + target_identity_id
            # The self-service caller (the only one that ever sets
            # authContextTarget) already knows both payload.patient and its own
            # authContext.target before submitting, so it computes this key
            # client-side and declares it — same as orchestration-base's
            # engine-dispatcher availability gate.
            # read-posture: (d) declared in contextHint.optionalReads by the
            # self-service caller
            if kv.Read(identified_by_lnk) == None:
                fail("AuthDenied: a patient may only book an appointment for themselves")

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

        # The booking must start in the future (relative to op.submittedAt) — a soft
        # past-time guard; see enforce_future.
        enforce_future(starts_at, op.submittedAt)

        # The clinic's mandatory 15-minute grid (SlotGridViolation if misaligned) —
        # checked before the availability/claim checks below so a malformed request
        # fails on the cheapest guard first.
        enforce_grid(starts_at, ends_at)

        # Provider availability windows (opt-in; OutsideHours if the booking falls
        # outside the provider's .hours). Checked before the slot-claim fan-out.
        enforce_hours(provider, starts_at, ends_at)

        # Provider date-specific time-off (opt-in; ProviderUnavailable if the booking
        # overlaps a blackout range) — the exception layer on top of the weekly hours.
        enforce_time_off(provider, starts_at, ends_at)

        # Discretize [startsAt, endsAt) into its covered 15-minute cells — lossless
        # under the grid constraint (AppointmentTooLong past 24h/96 cells).
        cells = slot_cells(starts_at, ends_at)

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
        # schedule + status are aspects. One providerSlotClaim + one patientSlotClaim
        # per covered cell IS the double-book lock (write-path CreateOnly/
        # expectedRevision, not a read-time enumeration + serialization epoch).
        mutations = [
            make_vtx(appt_key, "appointment", {}),
            make_aspect(appt_key, "schedule", "appointmentSchedule", sched),
            make_aspect(appt_key, "status", "appointmentStatus", {"value": "scheduled"}),
            make_link(for_patient_lnk, appt_key, patient, "forPatient", "forPatient", {}),
            make_link(with_provider_lnk, appt_key, provider, "withProvider", "withProvider", {}),
        ]
        for c in cells:
            cc = slot_cellcode(c)
            mutations.append(claim_cell(provider, cc, "providerSlotClaim", "SlotConflict", "provider"))
        for c in cells:
            cc = slot_cellcode(c)
            mutations.append(claim_cell(patient, cc, "patientSlotClaim", "PatientDoubleBook", "patient"))
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

        # The appointment's provider / patient — required so the move is conflict-
        # checked against each book exactly as CreateAppointment is, and so the OLD
        # cells can be released (without them a reschedule could silently move an
        # appointment INTO an occupied slot, or leave the vacated cells claimed
        # forever). Each is validated to be THIS appointment's actual provider /
        # patient via its withProvider / forPatient link (require_matching_provider /
        # require_matching_patient) — a live link proves the relationship AND that
        # the endpoint is real, once-validated (a wrong / fabricated endpoint would
        # otherwise recompute the wrong, e.g. empty, cell set and bypass the test).
        provider = required_string(p, "provider")
        require_matching_provider(appt_id, provider)

        patient = required_string(p, "patient")
        require_matching_patient(appt_id, patient)

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

        # The new time must start in the future (relative to op.submittedAt) — a soft
        # past-time guard; see enforce_future. A reschedule into the past is rejected
        # exactly as a create is.
        enforce_future(starts_at, op.submittedAt)

        # The clinic's mandatory 15-minute grid (SlotGridViolation if misaligned).
        enforce_grid(starts_at, ends_at)

        # Provider availability windows (opt-in; OutsideHours if the new time falls
        # outside the provider's .hours) — the move must land inside business hours too.
        enforce_hours(provider, starts_at, ends_at)

        # Provider date-specific time-off (opt-in; ProviderUnavailable if the new time
        # overlaps a blackout range) — the move must also avoid the provider's time-off.
        enforce_time_off(provider, starts_at, ends_at)

        # Release-old / claim-new, in the SAME atomic batch: read the appointment's
        # CURRENT .schedule to know which cells it holds today, discretize both the
        # old and new intervals, and diff. Cells held by BOTH sets need no mutation —
        # they stay claimed straight through the move (no read/re-claim gap).
        # read-posture: (a) declared in contextHint.reads by RescheduleAppointment's
        # dispatcher (cmd/clinic-app/web/app.js submitReschedule)
        old_sched = kv.Read(appt_key + ".schedule")
        if old_sched == None or old_sched.isDeleted:
            fail("InvalidState: " + appt_key + ".schedule is missing; cannot reschedule")
        old_starts = old_sched.data.get("startsAt")
        old_ends = old_sched.data.get("endsAt")
        if old_starts == None or old_ends == None:
            fail("InvalidState: " + appt_key + ".schedule is missing startsAt/endsAt; cannot reschedule")
        old_cells = slot_cells(old_starts, old_ends)
        new_cells = slot_cells(starts_at, ends_at)  # already grid-validated above

        to_release = [c for c in old_cells if c not in new_cells]
        to_claim = [c for c in new_cells if c not in old_cells]

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
        # .status untouched — the move keeps the same provider / patient). Vacated
        # cells release via an UNCONDITIONED tombstone (known-live from old_cells, so
        # no read needed); newly-covered cells run through claim_cell exactly as
        # Create does (reject / CAS-revive / create). If ANY to_claim cell collides,
        # the WHOLE batch — including the to_release tombstones — is atomically
        # rejected, so a failed reschedule leaves the original booking's claims fully
        # intact (design §2.5).
        mutations = [make_aspect_upsert(appt_key, "schedule", "appointmentSchedule", sched)]
        for c in to_release:
            cc = slot_cellcode(c)
            mutations.append(make_tombstone(provider + ".slot" + cc))
            mutations.append(make_tombstone(patient + ".slot" + cc))
        for c in to_claim:
            cc = slot_cellcode(c)
            mutations.append(claim_cell(provider, cc, "providerSlotClaim", "SlotConflict", "provider"))
        for c in to_claim:
            cc = slot_cellcode(c)
            mutations.append(claim_cell(patient, cc, "patientSlotClaim", "PatientDoubleBook", "patient"))
        events = [{"class": "clinic.appointmentRescheduled",
                   "data": {"appointmentKey": appt_key, "startsAt": starts_at, "endsAt": ends_at}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": appt_key}}

    if ot == "SetAppointmentStatus":
        appt_key = required_string(p, "appointmentKey")
        _, appt_id = parts_of(appt_key, "appointmentKey", "appointment")
        if not vertex_alive(state, appt_key):
            fail("UnknownAppointment: " + appt_key)
        cls = class_of(state, appt_key)
        if cls != "appointment":
            fail("WrongClass: appointmentKey: " + appt_key + " has class " + str(cls) + ", required appointment")
        status = required_status(p)
        # Terminal-status lifecycle guard: cancelled / completed / noShow are FINAL.
        # Re-setting the SAME terminal value is idempotent (re-run-safe under
        # at-least-once, and lets a noteless re-set clear a prior note); changing a
        # terminal status to a DIFFERENT one is rejected — a finished / cancelled visit
        # must not silently revert (e.g. completed→scheduled, cancelled→completed). A
        # non-terminal current status (scheduled / confirmed / checkedIn) still moves
        # freely (including corrections). The current status is read lazily (kv.Read,
        # §2.5 idiom), NOT a declared / OCC read: this op is already an unconditioned
        # upsert with no cross-op serialization, so the guard matches its existing
        # single-op semantics (it closes the single-op invalid transition; concurrent
        # transitions race exactly as the upsert already did). Re-opening a terminal
        # appointment is a future explicit op, not a status flip.
        # read-posture: (d) declared in contextHint.optionalReads by
        # SetAppointmentStatus's dispatcher (cmd/clinic-app/web/app.js) — absence is
        # the legit first-set case (no status yet)
        cur_val = None
        cur_status = kv.Read(appt_key + ".status")
        if cur_status != None and not cur_status.isDeleted:
            cur_val = cur_status.data.get("value")
            if cur_val in TERMINAL_STATUSES and status != cur_val:
                fail("TerminalStatus: appointment " + appt_key + " is " + str(cur_val) + " (terminal); cannot transition to " + status + " — cancelled/completed/noShow are final")
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
        # A FIRST terminal transition (non-terminal → terminal; a same-value re-set is
        # skipped — its cells were already released by the first transition) frees the
        # slot: recompute the held cells from .schedule and tombstone both hubs' claim
        # aspects (release_cells_mutations, design §2.6). provider/patient are
        # required here (not on non-terminal transitions) — there is no relationship
        # stored on the appointment to look them up by any other means (Contract #1: no
        # back-reference in aspect data), so the caller supplies them and they are
        # validated against the withProvider/forPatient links before use.
        if status in TERMINAL_STATUSES and status != cur_val:
            provider = required_string(p, "provider")
            require_matching_provider(appt_id, provider)
            patient = required_string(p, "patient")
            require_matching_patient(appt_id, patient)
            # read-posture: (a) declared in contextHint.reads by SetAppointmentStatus's
            # dispatcher (cmd/clinic-app/web/app.js setStatus), only on the terminal branch
            mutations = mutations + release_cells_mutations(provider, patient, kv.Read(appt_key + ".schedule"))
        events = [{"class": "clinic.appointmentStatusSet",
                   "data": {"appointmentKey": appt_key, "status": status}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": appt_key}}

    if ot == "RecordEncounter":
        appt_key = required_string(p, "appointmentKey")
        parts_of(appt_key, "appointmentKey", "appointment")
        if not vertex_alive(state, appt_key):
            fail("UnknownAppointment: " + appt_key)
        cls = class_of(state, appt_key)
        if cls != "appointment":
            fail("WrongClass: appointmentKey: " + appt_key + " has class " + str(cls) + ", required appointment")
        # RAW clinical record — PHI, captured plaintext-for-now (the .demographics
        # discipline), NEVER projected (the lens projects only the operational fields
        # below; the deferred Vault plane owns clinical-content display). summary is
        # the required visit note; assessment (diagnosis) + plan (orders) optional.
        summary = required_string(p, "summary")
        enc = {"summary": summary,
               # documentedAt is OPERATIONAL (when the visit was documented) — derived
               # from op.submittedAt, normalized to canonical whole-second UTC
               # (time.rfc3339_utc — pure, no clock read). A non-null documentedAt IS
               # the "visit documented" presence signal the lens surfaces.
               "documentedAt": time.rfc3339_utc(op.submittedAt),
               # followUpRequested is OPERATIONAL, non-PHI (the existence of a
               # follow-up, like an appointment time — projected). The clinical REASON
               # for a follow-up lives in the plan field and is never projected.
               "followUpRequested": optional_bool(p, "followUpRequested")}
        assessment = optional_string(p, "assessment")
        if assessment != None:
            enc["assessment"] = assessment
        plan = optional_string(p, "plan")
        if plan != None:
            enc["plan"] = plan
        follow_date = optional_string(p, "followUpDate")
        if enc["followUpRequested"] and follow_date != None:
            # Normalized to a full canonical-UTC RFC3339 instant so the optional
            # clinic-reminders follow-up reminder can arm an @at timer at it.
            enc["followUpDate"] = normalize_follow_up_date(follow_date)
        # Unconditioned upsert of the whole .encounter aspect (re-runnable — a
        # provider can correct the note). The .schedule / .status aspects + the
        # forPatient / withProvider links are untouched.
        mutations = [make_aspect_upsert(appt_key, "encounter", "appointmentEncounter", enc)]
        events = [{"class": "clinic.appointmentEncounterRecorded",
                   "data": {"appointmentKey": appt_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": appt_key}}

    if ot == "TombstoneAppointment":
        appt_key = required_string(p, "appointmentKey")
        _, appt_id = parts_of(appt_key, "appointmentKey", "appointment")
        if not vertex_alive(state, appt_key):
            fail("UnknownAppointment: " + appt_key)
        # A hard tombstone must free the appointment's held cells too — regardless of
        # its current status — else they stay claimed forever (a leak, not merely a
        # bound-maintenance nicety). provider/patient are required for the same reason
        # as SetAppointmentStatus's terminal path: no back-reference is stored on the
        # appointment, so the caller supplies + we validate them.
        provider = required_string(p, "provider")
        require_matching_provider(appt_id, provider)
        patient = required_string(p, "patient")
        require_matching_patient(appt_id, patient)
        # read-posture: (a) declared in contextHint.reads by TombstoneAppointment's
        # dispatcher (packages/clinic-domain/integration_test.go clSubmit calls, its
        # only caller — operator-only op, no FE dispatcher)
        mutations = [make_tombstone(appt_key)] + release_cells_mutations(provider, patient, kv.Read(appt_key + ".schedule"))
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
