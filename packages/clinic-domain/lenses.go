package clinicdomain

import "github.com/asolgan/lattice/internal/pkgmgr"

// ClinicAppointmentsBucket is the NATS-KV read model the clinicAppointments lens
// projects into. It is the **P5 query surface** for "what appointments exist" — a
// clinic FE reads THIS projected bucket (one row per appointment, keyed by the
// appointment key, each carrying patientKey / providerKey for client-side scoping
// of "my appointments" / "provider schedule"), never Core KV
// (lattice-architecture.md P5 — lenses are the only application query surface).
// The Refractor auto-creates the bucket on lens load.
const ClinicAppointmentsBucket = "clinic-appointments"

// ClinicProvidersBucket is the NATS-KV read model the clinicProviders lens
// projects into — the **P5 query surface** for "who can I book with": the booking
// UI reads THIS bucket (one row per named provider, keyed by the provider key) to
// render a human-readable provider picker, never Core KV.
const ClinicProvidersBucket = "clinic-providers"

// ClinicPatientsBucket is the NATS-KV read model the clinicPatients lens projects
// into — the **P5 query surface** for "who are the patients": the clinic FE reads
// THIS bucket (one row per named patient, keyed by the patient key) to render the
// patient-context switcher and to scope a patient's appointments, never Core KV.
// It projects the patient NAME only — DOB / contact are the PHI the deferred Vault
// plane owns and are deliberately NOT fanned into a read model here.
const ClinicPatientsBucket = "clinic-patients"

// Lenses returns the package's two projection lenses. Both are flat projections
// (no aggregation / WITH), so OPTIONAL-matched neighbour bindings are live
// directly in RETURN and the §4-B1 "WITH-drop" hazard does not apply. Aspect
// fields are read by the documented node.<aspect>.data.<field> form (the same
// access loftspace-domain / lease-signing use), including neighbour aspect-hops
// (lease-signing reads id.ssn.data.value off an OPTIONAL-matched identity the
// same way).
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName: "clinicAppointments",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        ClinicAppointmentsBucket,
			Engine:        "full",
			Spec:          clinicAppointmentsSpec,
		},
		{
			CanonicalName: "clinicProviders",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        ClinicProvidersBucket,
			Engine:        "full",
			Spec:          clinicProvidersSpec,
		},
		{
			CanonicalName: "clinicPatients",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        ClinicPatientsBucket,
			Engine:        "full",
			Spec:          clinicPatientsSpec,
		},
		{
			// clinicAppointmentsRead — the protected Postgres read model for the
			// patient-facing "My Appointments" view (D1.5, mirroring D1.3 Fire 2's
			// applicant-self milestone). Contract #6 §6.14: protected-by-default,
			// one authz_anchors set of bare-NanoID match tokens per row, RLS
			// returning only rows the reading actor is granted.
			//
			// This is the PATIENT-SELF audience only. cmd/clinic-app's
			// handleAppointments today lists the unprotected clinicAppointments
			// NATS-KV bucket and lets ANY caller pass `?patient=<any patient>` to
			// read that patient's full appointment history — including the
			// operational post-visit signals (documentedAt/followUpRequested) —
			// with no authentication at all. handleMyAppointments (D1.5) replaces
			// that vector for the patient's own view: RLS scopes the read to the
			// verified JWT subject, so a caller cannot request another patient's
			// rows. The PROVIDER audience (a provider's own schedule) and the
			// clinic-wide staff views (follow-ups worklist, the "every provider"
			// schedule aggregate) are NOT yet migrated — they stay on the
			// existing unprotected handleAppointments path. Closing them needs
			// either a provider-self anchor (a straightforward follow-up,
			// mirroring landlordLeaseApplicationsRead's Increment 2) or a
			// staff/admin wildcard grant (an Andrew posture call, per the D1
			// design's M5 Loupe-all-access decision) — flagged on the board, not
			// freelanced here.
			//
			// authz_anchors = [nanoIdFromKey(patient identity key)] — the
			// patient-self anchor. The shipped base cap-read.<actor> self-anchor
			// (D1.1) grants each patient their own NanoID, so RLS matches
			// patient=P's rows for P's session and nobody else's.
			//
			// Adapter postgres + Protected: Refractor provisions the RLS table
			// (FORCE ROW LEVEL SECURITY + the policy) from Columns at activation,
			// mirroring lease-signing's leaseApplicationsRead exactly. DSN is
			// left empty: Refractor resolves it from REFRACTOR_PG_DSN.
			//
			// forPatient is a REQUIRED match (the anchor walk) so an appointment
			// with no patient link projects NO row — fail-closed, never a null
			// authz_anchor. withProvider stays OPTIONAL (a display-only neighbour,
			// like leaseApplicationsRead's unit walk), matching the existing
			// clinicAppointments lens's null-safety for an incomplete appointment.
			CanonicalName: "clinicAppointmentsRead",
			Class:         "meta.lens",
			Adapter:       "postgres",
			Table:         "read_clinic_appointments",
			Engine:        "full",
			Spec:          clinicAppointmentsReadSpec,
			Protected:     true,
			IntoKey:       []string{"appointment_id"},
			Columns: []pkgmgr.PostgresColumn{
				{Name: "entity_key", Type: "text"},
				{Name: "starts_at", Type: "text"},
				{Name: "ends_at", Type: "text"},
				{Name: "reason", Type: "text"},
				{Name: "status", Type: "text"},
				{Name: "status_note", Type: "text"},
				{Name: "patient_key", Type: "text"},
				{Name: "patient_name", Type: "text"},
				{Name: "provider_key", Type: "text"},
				{Name: "provider_name", Type: "text"},
				{Name: "provider_specialty", Type: "text"},
				{Name: "reminder_sent_at", Type: "text"},
				{Name: "follow_up_reminder_sent_at", Type: "text"},
				{Name: "documented_at", Type: "text"},
				{Name: "follow_up_requested", Type: "boolean"},
				{Name: "follow_up_date", Type: "text"},
			},
		},
		{
			// providerAppointmentsRead — the protected Postgres read model for the
			// provider-facing "My Schedule" view (D1.5, closing the provider vector
			// cmd/clinic-app's handleAppointments doc-comment flagged: `?provider=`
			// let ANY caller read a named provider's full schedule — including
			// every patient's name and the post-visit documentedAt/followUpRequested
			// signals — with no authentication at all). Mirrors
			// landlordLeaseApplicationsRead's Increment 2 exactly: the same
			// self-anchor trick (no extra cap-read grant lens needed — a provider is
			// an identity, and the shipped base cap-read.<actor> self-anchor (D1.1)
			// already grants every identity its own NanoID), just a different
			// anchor-walk relation (withProvider instead of manages).
			//
			// The clinic-wide staff views (follow-ups worklist, the "All providers"
			// schedule aggregate) still have no per-row anchor to scope by and stay
			// on the existing unprotected handleAppointments path pending a
			// staff/admin wildcard-grant posture call (the D1 design's M5
			// Loupe-all-access decision) — flagged on the board, not freelanced here.
			//
			// authz_anchors = [nanoIdFromKey(provider identity key)].
			//
			// withProvider is a REQUIRED match (the anchor walk) so an appointment
			// with no provider link projects NO row — fail-closed, mirroring
			// clinicAppointmentsRead's REQUIRED forPatient walk. forPatient stays
			// OPTIONAL: a display-only neighbour, not the anchor.
			CanonicalName: "providerAppointmentsRead",
			Class:         "meta.lens",
			Adapter:       "postgres",
			Table:         "read_provider_appointments",
			Engine:        "full",
			Spec:          providerAppointmentsReadSpec,
			Protected:     true,
			IntoKey:       []string{"appointment_id"},
			Columns: []pkgmgr.PostgresColumn{
				{Name: "entity_key", Type: "text"},
				{Name: "starts_at", Type: "text"},
				{Name: "ends_at", Type: "text"},
				{Name: "reason", Type: "text"},
				{Name: "status", Type: "text"},
				{Name: "status_note", Type: "text"},
				{Name: "patient_key", Type: "text"},
				{Name: "patient_name", Type: "text"},
				{Name: "provider_key", Type: "text"},
				{Name: "provider_name", Type: "text"},
				{Name: "provider_specialty", Type: "text"},
				{Name: "reminder_sent_at", Type: "text"},
				{Name: "follow_up_reminder_sent_at", Type: "text"},
				{Name: "documented_at", Type: "text"},
				{Name: "follow_up_requested", Type: "boolean"},
				{Name: "follow_up_date", Type: "text"},
			},
		},
	}
}

// clinicAppointmentsSpec projects one row per appointment, walking forPatient and
// withProvider (each 0..1, so the row stays one-per-anchor — 0..1 × 0..1 = 1, the
// §10.2 shape). The 0..1 cardinality is enforced by the OP, not the cypher:
// CreateAppointment writes exactly one forPatient + one withProvider link
// (deterministic CreateOnly keys), and no op adds a second of either — so this
// stays a clean flat (no-WITH) projection. A future op that could attach a second
// link of the same relation would own re-introducing a fan-out guard. The per-row
// key column is `key` (the appointment key, the IntoKey
// default), so the read model is keyed by vtx.appointment.<id>; patientKey /
// providerKey repeat the joined endpoints in the body so a reader can scope to
// "my appointments" (by patient) or a "provider schedule" (by provider).
// Neighbour columns (patientName / providerName / providerSpecialty) are null when
// a link is absent (the reader treats them as absent). reminderSentAt is a null-safe
// read of the appointment's .reminder aspect (written by the clinic-reminders package
// when the @at reminder fires): it is null until a reminder is sent, and null whenever
// clinic-reminders is not installed — a soft read-model surfacing, never a build
// dependency (the engine reads the aspect by key-shape; clinic-domain installs alone).
// followUpReminderSentAt is the same null-safe soft read of the appointment's
// .followUpReminder aspect (written by clinic-reminders when the at-the-date follow-up
// @at reminder fires) — null until a follow-up reminder fires and null whenever
// clinic-reminders is not installed.
//
// documentedAt / followUpRequested / followUpDate are the OPERATIONAL, non-PHI
// signals of the appointment's .encounter aspect (the post-visit clinical record
// written by RecordEncounter). The RAW clinical content (summary / assessment /
// plan) is DELIBERATELY NOT projected — it is PHI the deferred Vault plane owns, the
// same name-only discipline clinicPatients applies to .demographics. A non-null
// documentedAt IS the "visit documented" presence signal (mirrors reminderSentAt);
// followUpDate is null unless a follow-up was requested. All null until a visit is
// documented (and whenever no .encounter aspect exists), null-safe by key-shape.
const clinicAppointmentsSpec = `MATCH (a:appointment)
OPTIONAL MATCH (a)-[:forPatient]->(p:patient)
OPTIONAL MATCH (a)-[:withProvider]->(pr:provider)
RETURN
  a.key AS key,
  a.key AS appointmentKey,
  a.schedule.data.startsAt AS startsAt,
  a.schedule.data.endsAt AS endsAt,
  a.schedule.data.reason AS reason,
  a.status.data.value AS status,
  a.status.data.note AS statusNote,
  p.key AS patientKey,
  p.demographics.data.fullName AS patientName,
  pr.key AS providerKey,
  pr.profile.data.fullName AS providerName,
  pr.profile.data.specialty AS providerSpecialty,
  a.reminder.data.sentAt AS reminderSentAt,
  a.followUpReminder.data.sentAt AS followUpReminderSentAt,
  a.encounter.data.documentedAt AS documentedAt,
  a.encounter.data.followUpRequested AS followUpRequested,
  a.encounter.data.followUpDate AS followUpDate`

// clinicProvidersSpec projects one row per NAMED provider — the human-readable
// roster the booking UI renders so a patient picks a provider by name + specialty
// instead of a raw vtx.provider.<id> key. The WHERE keeps only providers carrying
// a `.profile` aspect (the `<> null` aspect-presence idiom availableListings
// uses). The per-row key is the provider key (the IntoKey default); `providerKey`
// repeats it in the body. specialty / credentials / bio are projected so the
// provider EDITOR UI can read-modify-write the full profile (SetProviderProfile
// replaces the whole .profile, so the form seeds every editable field from here).
//
// timeOff projects the provider's opt-in .timeOff aspect's `ranges` array verbatim
// (a list of {from, to, reason?} canonical-UTC RFC3339 ranges written by
// SetProviderTimeOff), null when the provider has declared no blackouts. It is a
// non-scalar projection — the engine returns the array value, which the read model
// stores as JSON — so the time-off MANAGER UI can read-modify-write the current
// ranges (SetProviderTimeOff replaces the whole list) and the booking picker can
// warn about a blocked date. The op (CreateAppointment / RescheduleAppointment,
// enforce_time_off) stays the authority; this is the display surface only.
//
// hours projects the provider's opt-in .hours aspect's `windows` array verbatim
// (a list of {day 0-6, openSec, closeSec} UTC seconds-of-day written by
// SetProviderHours), null when the provider has set no availability windows. Like
// timeOff it is a non-scalar projection — the booking picker reads it (together
// with timeOff and the provider's existing appointments) to compute and suggest
// the open slots for a chosen date. The op (enforce_hours) stays the authority;
// this is the display surface only.
const clinicProvidersSpec = `MATCH (pr:provider)
WHERE pr.profile.data.fullName <> null
RETURN
  pr.key AS key,
  pr.key AS providerKey,
  pr.profile.data.fullName AS name,
  pr.profile.data.specialty AS specialty,
  pr.profile.data.credentials AS credentials,
  pr.profile.data.bio AS bio,
  pr.timeOff.data.ranges AS timeOff,
  pr.hours.data.windows AS hours`

// clinicPatientsSpec projects one row per NAMED patient — the roster the clinic FE
// renders so a person picks who they are (the patient-context switcher) and scopes
// "my appointments" by patientKey, instead of a raw vtx.patient.<id> key. Same flat
// no-WITH shape as clinicProviders. The WHERE keeps only patients carrying a
// `.demographics` aspect (the `<> null` aspect-presence idiom). NAME ONLY: DOB /
// email / phone are the PHI the deferred Vault plane owns and are intentionally NOT
// projected into this read model — the switcher needs only a human label.
const clinicPatientsSpec = `MATCH (p:patient)
WHERE p.demographics.data.fullName <> null
RETURN
  p.key AS key,
  p.key AS patientKey,
  p.demographics.data.fullName AS name`

// clinicAppointmentsReadSpec is the PATIENT-anchored protected Postgres read
// model's cypher (D1.5). forPatient is REQUIRED (the anchor walk) — an
// appointment with no patient link projects no row, fail-closed, never a null
// authz_anchor (mirrors leaseApplicationsReadSpec's REQUIRED applicant walk).
// withProvider stays OPTIONAL: it is a display-only neighbour, not the anchor.
// Same column surface as clinicAppointmentsSpec (the unprotected lens) so the
// migrated "My Appointments" view keeps full display parity.
const clinicAppointmentsReadSpec = `MATCH (a:appointment)
MATCH (a)-[:forPatient]->(p:patient)
OPTIONAL MATCH (a)-[:withProvider]->(pr:provider)
RETURN
  nanoIdFromKey(a.key)               AS appointment_id,
  a.key                              AS entity_key,
  a.schedule.data.startsAt           AS starts_at,
  a.schedule.data.endsAt             AS ends_at,
  a.schedule.data.reason             AS reason,
  a.status.data.value                AS status,
  a.status.data.note                 AS status_note,
  p.key                              AS patient_key,
  p.demographics.data.fullName       AS patient_name,
  pr.key                             AS provider_key,
  pr.profile.data.fullName           AS provider_name,
  pr.profile.data.specialty          AS provider_specialty,
  a.reminder.data.sentAt             AS reminder_sent_at,
  a.followUpReminder.data.sentAt     AS follow_up_reminder_sent_at,
  a.encounter.data.documentedAt      AS documented_at,
  a.encounter.data.followUpRequested AS follow_up_requested,
  a.encounter.data.followUpDate      AS follow_up_date,
  [nanoIdFromKey(p.key)]             AS authz_anchors
`

// providerAppointmentsReadSpec is the PROVIDER-anchored protected Postgres read
// model's cypher (D1.5). withProvider is REQUIRED (the anchor walk) — an
// appointment with no provider link projects no row, fail-closed, never a null
// authz_anchor (mirrors clinicAppointmentsReadSpec's REQUIRED forPatient walk).
// forPatient stays OPTIONAL: a display-only neighbour, not the anchor. Same
// column surface as clinicAppointmentsReadSpec so the provider's "My Schedule"
// view keeps full display parity with "My Appointments".
const providerAppointmentsReadSpec = `MATCH (a:appointment)
MATCH (a)-[:withProvider]->(pr:provider)
OPTIONAL MATCH (a)-[:forPatient]->(p:patient)
RETURN
  nanoIdFromKey(a.key)               AS appointment_id,
  a.key                              AS entity_key,
  a.schedule.data.startsAt           AS starts_at,
  a.schedule.data.endsAt             AS ends_at,
  a.schedule.data.reason             AS reason,
  a.status.data.value                AS status,
  a.status.data.note                 AS status_note,
  p.key                              AS patient_key,
  p.demographics.data.fullName       AS patient_name,
  pr.key                             AS provider_key,
  pr.profile.data.fullName           AS provider_name,
  pr.profile.data.specialty          AS provider_specialty,
  a.reminder.data.sentAt             AS reminder_sent_at,
  a.followUpReminder.data.sentAt     AS follow_up_reminder_sent_at,
  a.encounter.data.documentedAt      AS documented_at,
  a.encounter.data.followUpRequested AS follow_up_requested,
  a.encounter.data.followUpDate      AS follow_up_date,
  [nanoIdFromKey(pr.key)]            AS authz_anchors
`
