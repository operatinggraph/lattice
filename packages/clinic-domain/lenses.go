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

// ClinicSitesBucket is the NATS-KV read model the clinicSites lens projects
// into — the **P5 query surface** for "what clinic sites exist": a site
// directory / (a later increment's) site-scoped booking picker reads THIS
// bucket (one row per named site, keyed by the location-domain building key),
// never Core KV.
const ClinicSitesBucket = "clinic-sites"

// ClinicProviderSitesBucket is the NATS-KV read model the providerSites lens
// projects into — the **P5 query surface** for "which providers practice at
// which sites": one row per (provider, site) pair, mirroring identity-
// hygiene's duplicateCandidates shape (composite IntoKey, DiffRetraction).
const ClinicProviderSitesBucket = "clinic-provider-sites"

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
			CanonicalName: "clinicSites",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        ClinicSitesBucket,
			Engine:        "full",
			Spec:          clinicSitesSpec,
		},
		{
			CanonicalName:  "providerSites",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         ClinicProviderSitesBucket,
			Engine:         "full",
			Spec:           providerSitesSpec,
			IntoKey:        []string{"provider_id", "site_id"},
			DiffRetraction: true,
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
			// rows. The PROVIDER audience moved to providerAppointmentsRead below
			// (a provider-self anchor); the clinic-wide STAFF views (follow-ups
			// worklist, the "All providers" schedule aggregate) moved to
			// cmd/clinic-app's handleStaffAppointments, reading THIS SAME table
			// (no per-row anchor needed for staff — the reserved WildcardAnchor
			// grant, D1 design §3.4 M5, matches every row regardless of its
			// authz_anchors; see internal/bootstrap.
			// CapabilityReadWildcardGrantsLensDefinition).
			//
			// authz_anchors = [nanoIdFromKey(p.key)] — the patient's OWN
			// NanoID (never a linked contact identity's — cmd/clinic-app mints
			// the JWT subject as the patient's own bare NanoID, app.js's
			// bareId(state.patient)). The platform's base cap-read self-anchor
			// (D1.1) only ever matches class=identity, so it does NOT grant a
			// patient (class=patient) its own anchor — clinicPatientReadGrants
			// below is clinic-domain's own cap-read.clinic.patient producer that
			// closes that gap; without it this table's rows are unreadable by
			// anyone but a WildcardAnchor holder.
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
			// landlordLeaseApplicationsRead's Increment 2 shape (a self-anchor
			// table), but — unlike the loftspace/lease-signing landlord case —
			// a provider is NOT an identity (class=provider), so it needs its
			// own grant producer, same as clinicAppointmentsRead's patient
			// anchor: clinicProviderReadGrants below.
			//
			// The clinic-wide staff views read clinicAppointmentsRead ABOVE (via
			// handleStaffAppointments' wildcard grant), not this provider-anchored
			// table — a staff actor's wildcard grant matches every protected
			// table, so no separate staff projection is needed here.
			//
			// authz_anchors = [nanoIdFromKey(pr.key)] — the provider's own NanoID.
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
		{
			// clinicPatientsRead — the protected Postgres read model for the
			// clinic-wide patient-context switcher (D1.5, mirroring the staff
			// wildcard increment: handleStaffAppointments / providerAppointmentsRead
			// above). cmd/clinic-app's handlePatients used to list the unprotected
			// clinicPatients NATS-KV bucket and serve every named patient's full
			// name to ANY caller with no authentication at all — a clinic-wide
			// membership-disclosure PHI dump (which patients exist at this clinic).
			// handleStaffPatients replaces that vector, reading THIS table as a
			// JWT-authenticated actor.
			//
			// Unlike clinicAppointmentsRead / providerAppointmentsRead there is no
			// per-patient self-anchor to carve out here — "the whole roster" has no
			// single-row owner, so every row projects an EMPTY authz_anchors set:
			// only an actor holding the reserved WildcardAnchor grant (D1 design
			// §3.4 M5, internal/refractor/adapter.WildcardAnchor) ever matches a
			// row, mirroring handleStaffAppointments' no-separate-staff-projection
			// note.
			//
			// NAME comes straight off .demographics (non-sensitive). email/phone
			// are SECURE columns (Contract #3 §3.10, Vault Fire 5 — the
			// Secure-Lens decrypt-at-projection primitive, mirroring
			// landlordLeaseApplicationsRead's applicant_email/applicant_phone
			// exactly): the OPTIONAL-matched identifiedBy identity's sensitive
			// .email/.phone aspects are RETURNed as ciphertext envelopes whole
			// (id.<aspect>.data) and decrypted at projection into this
			// staff-wildcard-anchored table — the only actors who can ever read
			// a row here already hold the WildcardAnchor grant, so decrypted
			// contact never reaches an unauthorized reader. A patient with no
			// identifiedBy link (identityKey null) or a shredded identity
			// projects null email/phone — never an error (right-to-erasure and
			// the pre-Vault/no-backfill posture both fall through the same
			// null path).
			CanonicalName: "clinicPatientsRead",
			Class:         "meta.lens",
			Adapter:       "postgres",
			Table:         "read_clinic_patients",
			Engine:        "full",
			Spec:          clinicPatientsReadSpec,
			Protected:     true,
			IntoKey:       []string{"patient_id"},
			Columns: []pkgmgr.PostgresColumn{
				{Name: "entity_key", Type: "text"},
				{Name: "patient_key", Type: "text"},
				{Name: "name", Type: "text"},
				{Name: "identity_key", Type: "text"},
				{Name: "email", Type: "text"},
				{Name: "phone", Type: "text"},
			},
			SecureColumns: []pkgmgr.SecureColumn{
				{Column: "email", IdentityKeyColumn: "identity_key", Field: "value"},
				{Column: "phone", IdentityKeyColumn: "identity_key", Field: "value"},
			},
		},
		{
			// clinicPatientReadGrants — the cap-read.clinic.patient GrantTable
			// producer that closes the gap flagged live (0-of-1 read):
			// clinicAppointmentsRead's authz_anchors anchors on the PATIENT
			// vertex's own bare NanoID (nanoIdFromKey(patient.key)), and
			// cmd/clinic-app mints the JWT subject as that SAME patient NanoID
			// (app.js's bareId(state.patient) — the patient is its own RLS
			// actor, never a linked contact identity). The platform's base
			// cap-read self-anchor producer (internal/bootstrap.
			// CapabilityReadGrantsLensDefinition) only MATCHes class=identity,
			// so a patient — a DIFFERENT vertex class — never receives a grant:
			// My Appointments was permanently empty for every patient.
			//
			// This is the package-level cap-read.<domain> producer the base
			// lens's doc comment anticipates (internal/bootstrap/lenses.go
			// "Each package ships its own cap-read.<domain> ... lens for the
			// relationships it owns") — clinic-domain is the first package to
			// ship one. Mirrors CapabilityReadGrantsLensDefinition's shape
			// exactly (a plain, non-actorAggregate GrantTable projection), just
			// self-anchored on class=patient instead of class=identity.
			//
			// grant_source = 'cap-read.clinic.patient', disjoint from the core
			// producer's 'cap-read.root' 'cap-read' and from
			// clinicProviderReadGrants' 'cap-read.clinic.provider' below — each
			// producer retracts only its own grant_source rows (§6.14).
			// RETRACTION is automatic: TombstonePatient's anchor-tombstone
			// resolves nanoIdFromKey(p.key) read-free, so the self-grant is
			// revoked the same way the base identity self-grant is.
			CanonicalName: "clinicPatientReadGrants",
			Class:         "meta.lens",
			Adapter:       "postgres",
			GrantTable:    true,
			Engine:        "full",
			Spec:          clinicPatientReadGrantsSpec,
		},
		{
			// clinicProviderReadGrants — providerAppointmentsRead's sibling
			// producer, self-anchoring class=provider the same way
			// clinicPatientReadGrants self-anchors class=patient (see its doc
			// comment for the full gap analysis: providerAppointmentsRead's
			// authz_anchors is the provider's own NanoID, and cmd/clinic-app
			// mints a provider's JWT subject the same way — bareId(providerKey)
			// — so "My Schedule" was equally permanently empty).
			//
			// grant_source = 'cap-read.clinic.provider'.
			CanonicalName: "clinicProviderReadGrants",
			Class:         "meta.lens",
			Adapter:       "postgres",
			GrantTable:    true,
			Engine:        "full",
			Spec:          clinicProviderReadGrantsSpec,
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

// clinicSitesSpec projects one row per NAMED clinic site — a location-domain
// building carrying a `.site` aspect (SetSiteProfile). Same flat no-WITH shape
// as clinicProviders/clinicPatients. The WHERE keeps only buildings carrying a
// name (the `<> null` aspect-presence idiom). The per-row key is the building
// key (the IntoKey default); `siteKey` repeats it in the body — the site
// directory / (a later increment's) site-scoped booking picker reads this.
const clinicSitesSpec = `MATCH (b:building)
WHERE b.site.data.name <> null
RETURN
  b.key AS key,
  b.key AS siteKey,
  b.site.data.name AS name`

// providerSitesSpec projects one row per (provider, site) practicesAt pair —
// a provider may practice at many sites, a site may host many providers, so
// this is a SEPARATE join lens rather than an array column folded into
// clinicProviders (mirrors identity-hygiene's duplicateCandidates shape:
// nats-kv full engine, no $actorKey, composite IntoKey [provider_id, site_id],
// DiffRetraction so an unassign — RemoveProviderSite tombstoning the
// practicesAt link — retracts the row instead of leaving it stale).
const providerSitesSpec = `MATCH (pr:provider)-[:practicesAt]->(b:building)
RETURN
  nanoIdFromKey(pr.key) AS provider_id,
  nanoIdFromKey(b.key)  AS site_id,
  pr.key                AS providerKey,
  b.key                 AS siteKey,
  pr.profile.data.fullName AS providerName,
  b.site.data.name         AS siteName`

// clinicPatientsReadSpec is the protected Postgres read model's cypher for the
// clinic-wide patient roster (D1.5, the staff-wildcard increment; Vault Fire 5
// added the identifiedBy contact columns). Same WHERE guard as
// clinicPatientsSpec (only NAMED patients project). authz_anchors is the
// empty list literal for every row — there is no per-patient self-anchor here
// (see clinicPatientsRead's doc comment), so only the reserved WildcardAnchor
// grant ever matches.
//
// identifiedBy is OPTIONAL — a patient created before its contact was minted,
// or one with no contact at all, has no linked identity, so identityKey /
// emailEnv / phoneEnv all project null together (the Secure-Lens decryptor's
// null-ciphertext-column path, never the null-identity-key error path — see
// internal/refractor/pipeline/secure.go decryptColumn). The shred's piiKey CDC
// event re-scans this UNANCHORED lens the same way it does
// landlordLeaseApplicationsRead, so a shredded patient's contact scrubs to
// null on the next projection touch.
const clinicPatientsReadSpec = `MATCH (p:patient)
WHERE p.demographics.data.fullName <> null
OPTIONAL MATCH (p)-[:identifiedBy]->(id:identity)
RETURN
  nanoIdFromKey(p.key)         AS patient_id,
  p.key                        AS entity_key,
  p.key                        AS patient_key,
  p.demographics.data.fullName AS name,
  id.key                       AS identity_key,
  id.email.data                AS email,
  id.phone.data                AS phone,
  []                           AS authz_anchors
`

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

// clinicPatientReadGrantsSpec is the cap-read.clinic.patient GrantTable
// producer's cypher — a plain, non-actorAggregate self-anchor projection
// mirroring internal/bootstrap.CapabilityReadGrantsLensDefinition exactly
// (MATCH the vertex, RETURN its own bare NanoID as both actor_id and
// anchor_id), just scoped to class=patient instead of class=identity. See
// clinicPatientReadGrants' doc comment (lenses.go) for why patient/provider
// need their own producer: the platform base self-anchor only ever matches
// class=identity.
const clinicPatientReadGrantsSpec = `MATCH (p:patient)
RETURN
  nanoIdFromKey(p.key)        AS actor_id,
  nanoIdFromKey(p.key)        AS anchor_id,
  'cap-read.clinic.patient'   AS grant_source
`

// clinicProviderReadGrantsSpec is clinicPatientReadGrantsSpec's provider
// sibling — self-anchors class=provider instead of class=patient.
const clinicProviderReadGrantsSpec = `MATCH (pr:provider)
RETURN
  nanoIdFromKey(pr.key)       AS actor_id,
  nanoIdFromKey(pr.key)       AS anchor_id,
  'cap-read.clinic.provider'  AS grant_source
`
