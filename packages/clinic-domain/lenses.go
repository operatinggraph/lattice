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
  a.reminder.data.sentAt AS reminderSentAt`

// clinicProvidersSpec projects one row per NAMED provider — the human-readable
// roster the booking UI renders so a patient picks a provider by name + specialty
// instead of a raw vtx.provider.<id> key. The WHERE keeps only providers carrying
// a `.profile` aspect (the `<> null` aspect-presence idiom availableListings
// uses). The per-row key is the provider key (the IntoKey default); `providerKey`
// repeats it in the body.
const clinicProvidersSpec = `MATCH (pr:provider)
WHERE pr.profile.data.fullName <> null
RETURN
  pr.key AS key,
  pr.key AS providerKey,
  pr.profile.data.fullName AS name,
  pr.profile.data.specialty AS specialty,
  pr.profile.data.credentials AS credentials`

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
