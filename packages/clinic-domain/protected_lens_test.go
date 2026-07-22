package clinicdomain

// Rule-engine proof of the clinicAppointmentsRead protected Postgres read model
// (D1.5, the patient-self milestone, mirroring lease-signing's
// TestLeaseApplicationsRead_* suite).
//
// These drive clinicAppointmentsReadSpec through the same `full` engine selected
// at activation (engine:"full"), against an embedded NATS Core/Adjacency KV, and
// assert the ENGINE PROJECTION ROW: the display scalars hop correctly and — the
// headline — authz_anchors carries exactly the patient's bare NanoID, scoped per
// appointment. The Postgres RLS round-trip is the platform-side proof
// (internal/refractor adapter/rls tests, gated on POSTGRES_TEST_DSN); the
// cypher's anchor derivation is proven here.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
)

// anchorStrings normalizes a projected authz_anchors value (a list literal) into
// a []string for assertion. A nil element (nanoIdFromKey of an absent key) is
// surfaced as "" so a deny-all bare-shell row is observable.
func anchorStrings(t *testing.T, v any) []string {
	t.Helper()
	require.NotNil(t, v, "authz_anchors must project as a list, never null")
	list, ok := v.([]any)
	require.Truef(t, ok, "authz_anchors must be a list, got %T", v)
	out := make([]string, len(list))
	for i, e := range list {
		if e == nil {
			out[i] = ""
			continue
		}
		s, ok := e.(string)
		require.Truef(t, ok, "authz_anchors element must be a string, got %T", e)
		out[i] = s
	}
	return out
}

// seedAppointment mints one appointment linked to a named patient + provider,
// with the full display-column surface (schedule, status, encounter signals).
func (f *lensFixture) seedAppointment(t *testing.T, apptName, patientName, providerName string) {
	t.Helper()
	f.vtx(t, apptName, "appointment")
	f.vtx(t, patientName, "patient")
	f.vtx(t, providerName, "provider")
	f.aspect(t, patientName, "demographics", "patientDemographics", map[string]any{"fullName": "Alice Rivera"})
	f.aspect(t, providerName, "profile", "providerProfile", map[string]any{"fullName": "Dr. Sam Okafor", "specialty": "Cardiology"})
	f.aspect(t, apptName, "schedule", "appointmentSchedule", map[string]any{"startsAt": "2026-07-01T15:00:00Z", "endsAt": "2026-07-01T15:30:00Z", "reason": "Annual checkup"})
	f.aspect(t, apptName, "status", "appointmentStatus", map[string]any{"value": "scheduled"})
	f.aspect(t, apptName, "encounter", "appointmentEncounter", map[string]any{"documentedAt": "2026-07-01T15:35:00Z", "followUpRequested": true, "followUpDate": "2026-08-01"})
	f.edge(t, "forPatient", apptName, patientName)
	f.edge(t, "withProvider", apptName, providerName)
}

// TestClinicAppointmentsRead_ProjectsPatientSelfAnchor — the protected read model
// projects one row per appointment carrying the display scalars and an
// authz_anchors set of exactly the patient's bare NanoID (§6.14). This is the
// grant clinicPatientReadGrants (this package's own cap-read.clinic.patient
// producer, lenses.go) matches: it self-grants the patient's own NanoID, so
// the row is readable by the patient and nobody else. (The platform's base
// cap-read self-anchor does NOT cover this — it only ever matches
// class=identity, and a patient is class=patient.)
func TestClinicAppointmentsRead_ProjectsPatientSelfAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedAppointment(t, "appt", "alice", "drsam")
	apptKey := "vtx.appointment." + f.ids["appt"]
	patientKey := "vtx.patient." + f.ids["alice"]
	providerKey := "vtx.provider." + f.ids["drsam"]

	rows := f.project(t, clinicAppointmentsReadSpec)
	require.Len(t, rows, 1, "exactly one read-model row per appointment")
	v := rows[0].Values

	require.Equal(t, f.ids["appt"], v["appointment_id"], "appointment_id is the appointment's bare NanoID (the IntoKey)")
	require.Equal(t, apptKey, v["entity_key"])
	require.Equal(t, "2026-07-01T15:00:00Z", v["starts_at"])
	require.Equal(t, "2026-07-01T15:30:00Z", v["ends_at"])
	require.Equal(t, "Annual checkup", v["reason"])
	require.Equal(t, "scheduled", v["status"])
	require.Equal(t, patientKey, v["patient_key"])
	require.Equal(t, "Alice Rivera", v["patient_name"])
	require.Equal(t, providerKey, v["provider_key"])
	require.Equal(t, "Dr. Sam Okafor", v["provider_name"])
	require.Equal(t, "Cardiology", v["provider_specialty"])
	require.Equal(t, "2026-07-01T15:35:00Z", v["documented_at"])
	require.Equal(t, true, v["follow_up_requested"])
	require.Equal(t, "2026-08-01", v["follow_up_date"])

	// The headline: authz_anchors is exactly [alice's bare NanoID].
	require.Equal(t, []string{f.ids["alice"]}, anchorStrings(t, v["authz_anchors"]),
		"authz_anchors must carry exactly the patient's bare NanoID (the §6.14 self-anchor RLS matches)")
}

// TestClinicAppointmentsRead_AnchorScopesPerPatient — two appointments for two
// different patients each anchor to ONLY their own patient NanoID. This is the
// projection-layer proof of "A sees only A's appointments": RLS, matching each
// row's authz_anchors against the reading actor's granted anchors, returns A's
// row to A and B's row to B with no overlap.
func TestClinicAppointmentsRead_AnchorScopesPerPatient(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedAppointment(t, "apptA", "alice", "drsam")
	f.seedAppointment(t, "apptB", "bob", "drsam")

	rows := f.project(t, clinicAppointmentsReadSpec)
	require.Len(t, rows, 2)
	byAppt := map[string][]string{}
	for _, r := range rows {
		byAppt[r.Values["appointment_id"].(string)] = anchorStrings(t, r.Values["authz_anchors"])
	}
	require.Equal(t, []string{f.ids["alice"]}, byAppt[f.ids["apptA"]], "A's appointment anchors only to A")
	require.Equal(t, []string{f.ids["bob"]}, byAppt[f.ids["apptB"]], "B's appointment anchors only to B")
	require.NotContains(t, byAppt[f.ids["apptA"]], f.ids["bob"], "A's row must NOT carry B's anchor")
	require.NotContains(t, byAppt[f.ids["apptB"]], f.ids["alice"], "B's row must NOT carry A's anchor")
}

// TestClinicAppointmentsRead_NoPatientLinkProducesNoRow — an appointment with no
// forPatient link projects NO row at all (forPatient is a required MATCH, the
// anchor walk). A shell no patient anchor would protect never enters the read
// model — the strongest fail-closed posture (and it avoids handing the array
// adapter a null anchor element). A well-formed appointment alongside it still
// projects normally, proving the required MATCH excludes only the shell.
func TestClinicAppointmentsRead_NoPatientLinkProducesNoRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "orphan", "appointment") // no forPatient link
	f.seedAppointment(t, "appt", "alice", "drsam")

	rows := f.project(t, clinicAppointmentsReadSpec)
	require.Len(t, rows, 1, "only the well-formed appointment projects; the no-patient shell is excluded")
	require.Equal(t, f.ids["appt"], rows[0].Values["appointment_id"])
	require.Equal(t, []string{f.ids["alice"]}, anchorStrings(t, rows[0].Values["authz_anchors"]))
}

// TestClinicAppointmentsRead_NoProviderLinkStillProjects — withProvider is
// OPTIONAL (a display-only neighbour, not the anchor): an appointment missing
// its provider link still projects a row anchored to the patient, with the
// provider columns null.
func TestClinicAppointmentsRead_NoProviderLinkStillProjects(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "appt", "appointment")
	f.vtx(t, "alice", "patient")
	f.aspect(t, "alice", "demographics", "patientDemographics", map[string]any{"fullName": "Alice Rivera"})
	f.aspect(t, "appt", "schedule", "appointmentSchedule", map[string]any{"startsAt": "2026-07-01T15:00:00Z"})
	f.aspect(t, "appt", "status", "appointmentStatus", map[string]any{"value": "scheduled"})
	f.edge(t, "forPatient", "appt", "alice")

	rows := ruleengineFilterByKey(f.project(t, clinicAppointmentsReadSpec), "appointment_id", f.ids["appt"])
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Nil(t, v["provider_key"], "no withProvider link → null provider_key")
	require.Nil(t, v["provider_name"], "no withProvider link → null provider_name")
	require.Equal(t, []string{f.ids["alice"]}, anchorStrings(t, v["authz_anchors"]))
}

// TestProviderAppointmentsRead_ProjectsProviderSelfAnchor mirrors
// TestClinicAppointmentsRead_ProjectsPatientSelfAnchor for the
// providerAppointmentsReadSpec (D1.5 Increment 2): same display scalars, but
// authz_anchors carries exactly the PROVIDER's bare NanoID.
func TestProviderAppointmentsRead_ProjectsProviderSelfAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedAppointment(t, "appt", "alice", "drsam")
	apptKey := "vtx.appointment." + f.ids["appt"]
	patientKey := "vtx.patient." + f.ids["alice"]
	providerKey := "vtx.provider." + f.ids["drsam"]

	rows := f.project(t, providerAppointmentsReadSpec)
	require.Len(t, rows, 1, "exactly one read-model row per appointment")
	v := rows[0].Values

	require.Equal(t, f.ids["appt"], v["appointment_id"])
	require.Equal(t, apptKey, v["entity_key"])
	require.Equal(t, patientKey, v["patient_key"])
	require.Equal(t, "Alice Rivera", v["patient_name"])
	require.Equal(t, providerKey, v["provider_key"])
	require.Equal(t, "Dr. Sam Okafor", v["provider_name"])

	// The headline: authz_anchors is exactly [the provider's bare NanoID], NOT
	// the patient's — the anchor axis flips relative to clinicAppointmentsRead.
	require.Equal(t, []string{f.ids["drsam"]}, anchorStrings(t, v["authz_anchors"]),
		"authz_anchors must carry exactly the provider's bare NanoID (the §6.14 self-anchor RLS matches)")
}

// TestProviderAppointmentsRead_AnchorScopesPerProvider mirrors
// TestClinicAppointmentsRead_AnchorScopesPerPatient: two appointments with two
// different providers each anchor to ONLY their own provider NanoID.
func TestProviderAppointmentsRead_AnchorScopesPerProvider(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedAppointment(t, "apptA", "alice", "drsam")
	f.seedAppointment(t, "apptB", "alice", "drpat")

	rows := f.project(t, providerAppointmentsReadSpec)
	require.Len(t, rows, 2)
	byAppt := map[string][]string{}
	for _, r := range rows {
		byAppt[r.Values["appointment_id"].(string)] = anchorStrings(t, r.Values["authz_anchors"])
	}
	require.Equal(t, []string{f.ids["drsam"]}, byAppt[f.ids["apptA"]], "A's appointment anchors only to drsam")
	require.Equal(t, []string{f.ids["drpat"]}, byAppt[f.ids["apptB"]], "B's appointment anchors only to drpat")
	require.NotContains(t, byAppt[f.ids["apptA"]], f.ids["drpat"], "drsam's row must NOT carry drpat's anchor")
	require.NotContains(t, byAppt[f.ids["apptB"]], f.ids["drsam"], "drpat's row must NOT carry drsam's anchor")
}

// TestProviderAppointmentsRead_NoProviderLinkProducesNoRow mirrors
// TestClinicAppointmentsRead_NoPatientLinkProducesNoRow: withProvider is now
// the REQUIRED anchor walk, so an appointment with no provider link projects
// NO row — fail-closed, never a null anchor.
func TestProviderAppointmentsRead_NoProviderLinkProducesNoRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "orphan", "appointment") // no withProvider link
	f.seedAppointment(t, "appt", "alice", "drsam")

	rows := f.project(t, providerAppointmentsReadSpec)
	require.Len(t, rows, 1, "only the well-formed appointment projects; the no-provider shell is excluded")
	require.Equal(t, f.ids["appt"], rows[0].Values["appointment_id"])
	require.Equal(t, []string{f.ids["drsam"]}, anchorStrings(t, rows[0].Values["authz_anchors"]))
}

// TestProviderAppointmentsRead_NoPatientLinkStillProjects mirrors
// TestClinicAppointmentsRead_NoProviderLinkStillProjects: forPatient is
// OPTIONAL here (a display-only neighbour, not the anchor), so an appointment
// missing its patient link still projects a row anchored to the provider, with
// the patient columns null.
func TestProviderAppointmentsRead_NoPatientLinkStillProjects(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "appt", "appointment")
	f.vtx(t, "drsam", "provider")
	f.aspect(t, "drsam", "profile", "providerProfile", map[string]any{"fullName": "Dr. Sam Okafor", "specialty": "Cardiology"})
	f.aspect(t, "appt", "schedule", "appointmentSchedule", map[string]any{"startsAt": "2026-07-01T15:00:00Z"})
	f.aspect(t, "appt", "status", "appointmentStatus", map[string]any{"value": "scheduled"})
	f.edge(t, "withProvider", "appt", "drsam")

	rows := ruleengineFilterByKey(f.project(t, providerAppointmentsReadSpec), "appointment_id", f.ids["appt"])
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Nil(t, v["patient_key"], "no forPatient link → null patient_key")
	require.Nil(t, v["patient_name"], "no forPatient link → null patient_name")
	require.Equal(t, []string{f.ids["drsam"]}, anchorStrings(t, v["authz_anchors"]))
}

// TestClinicPatientsRead_ProjectsContactEnvelopesWhole — the Secure-Lens
// contract at the engine layer (Contract #3 §3.10, Vault Fire 5, mirroring
// TestLandlordLeaseApplicationsRead_ProjectsContactEnvelopesWhole): email /
// phone RETURN the identifiedBy identity's sensitive aspect envelope WHOLE
// (id.<aspect>.data — the {ct, nonce, keyId} map the Processor commits),
// never a plaintext hop, so the pipeline's SecureDecryptor is the only place
// plaintext appears. A linked identity missing one aspect projects that
// column null while the row still projects.
func TestClinicPatientsRead_ProjectsContactEnvelopesWhole(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "alice", "patient")
	f.vtx(t, "aliceId", "identity")
	f.aspect(t, "alice", "demographics", "patientDemographics", map[string]any{"fullName": "Alice Rivera"})
	emailEnv := map[string]any{"ct": "b64-email-ct", "nonce": "b64-nonce-1", "keyId": "alice-key"}
	f.aspect(t, "aliceId", "email", "email", emailEnv)
	// No phone aspect: the column must project null, the row must survive.
	f.edge(t, "identifiedBy", "alice", "aliceId")

	rows := f.project(t, clinicPatientsReadSpec)
	require.Len(t, rows, 1)
	v := rows[0].Values

	require.Equal(t, "vtx.identity."+f.ids["aliceId"], v["identity_key"])
	require.Equal(t, emailEnv, v["email"], "email carries the ciphertext envelope whole")
	require.Nil(t, v["phone"], "a missing sensitive aspect projects null, not a dropped row")
}

// TestClinicPatientsRead_NoIdentityLinkStillProjects — a patient with no
// identifiedBy link (never given contact, or created before Fire 5b-iii's
// re-model) still projects its roster row: identity_key/email/phone all null,
// never a dropped row or an engine error (mirrors
// TestLandlordLeaseApplicationsRead_ContactlessApplicantStillProjects).
func TestClinicPatientsRead_NoIdentityLinkStillProjects(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "alice", "patient")
	f.aspect(t, "alice", "demographics", "patientDemographics", map[string]any{"fullName": "Alice Rivera"})

	rows := f.project(t, clinicPatientsReadSpec)
	require.Len(t, rows, 1, "a patient with no linked identity still projects")
	v := rows[0].Values
	require.Equal(t, "Alice Rivera", v["name"])
	require.Nil(t, v["identity_key"])
	require.Nil(t, v["email"])
	require.Nil(t, v["phone"])
}

// TestClinicPatientReadGrants_SelfAnchorsEachPatient — the cap-read.clinic.patient
// GrantTable producer's cypher proof: one grant row per patient, actor_id ==
// anchor_id == the patient's own bare NanoID, grant_source ==
// 'cap-read.clinic.patient'. This is the grant clinicAppointmentsRead's
// authz_anchors matches (see TestClinicAppointmentsRead_ProjectsPatientSelfAnchor)
// — without it, RLS has nothing granting a patient its own row.
func TestClinicPatientReadGrants_SelfAnchorsEachPatient(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "alice", "patient")
	f.aspect(t, "alice", "demographics", "patientDemographics", map[string]any{"fullName": "Alice Rivera"})
	f.vtx(t, "bob", "patient")
	f.aspect(t, "bob", "demographics", "patientDemographics", map[string]any{"fullName": "Bob Nakamura"})

	rows := f.project(t, clinicPatientReadGrantsSpec)
	require.Len(t, rows, 2)
	byActor := map[string]ruleengine.ProjectionResult{}
	for _, r := range rows {
		byActor[r.Values["actor_id"].(string)] = r
	}
	for _, id := range []string{f.ids["alice"], f.ids["bob"]} {
		r, ok := byActor[id]
		require.Truef(t, ok, "expected a self-grant row for patient %s", id)
		require.Equal(t, id, r.Values["anchor_id"], "a patient's grant anchors on ITS OWN NanoID")
		require.Equal(t, "cap-read.clinic.patient", r.Values["grant_source"])
	}
}

// TestClinicProviderReadGrants_SelfAnchorsEachProvider is
// TestClinicPatientReadGrants_SelfAnchorsEachPatient's provider sibling.
func TestClinicProviderReadGrants_SelfAnchorsEachProvider(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "drsam", "provider")
	f.aspect(t, "drsam", "profile", "providerProfile", map[string]any{"fullName": "Dr. Sam Okafor", "specialty": "Cardiology"})

	rows := f.project(t, clinicProviderReadGrantsSpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, f.ids["drsam"], v["actor_id"])
	require.Equal(t, f.ids["drsam"], v["anchor_id"], "a provider's grant anchors on ITS OWN NanoID")
	require.Equal(t, "cap-read.clinic.provider", v["grant_source"])
}

func ruleengineFilterByKey(rows []ruleengine.ProjectionResult, col, id string) []ruleengine.ProjectionResult {
	out := make([]ruleengine.ProjectionResult, 0, 1)
	for _, r := range rows {
		if r.Values[col] == id {
			out = append(out, r)
		}
	}
	return out
}
