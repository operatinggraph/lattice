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
	"context"
	"encoding/json"
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
// with the full display-column surface (schedule, status, documentation signals).
func (f *lensFixture) seedAppointment(t *testing.T, apptName, patientName, providerName string) {
	t.Helper()
	f.vtx(t, apptName, "appointment")
	f.vtx(t, patientName, "patient")
	f.vtx(t, providerName, "provider")
	f.aspect(t, patientName, "demographics", "patientDemographics", map[string]any{"registeredAt": "2026-06-01T09:00:00Z", "fullName": "Alice Rivera"})
	f.aspect(t, providerName, "profile", "providerProfile", map[string]any{"fullName": "Dr. Sam Okafor", "specialty": "Cardiology"})
	f.aspect(t, apptName, "schedule", "appointmentSchedule", map[string]any{"startsAt": "2026-07-01T15:00:00Z", "endsAt": "2026-07-01T15:30:00Z", "reason": "Annual checkup"})
	f.aspect(t, apptName, "status", "appointmentStatus", map[string]any{"value": "scheduled"})
	f.aspect(t, apptName, "documentation", "appointmentDocumentation", map[string]any{"documentedAt": "2026-07-01T15:35:00Z", "followUpRequested": true, "followUpDate": "2026-08-01"})
	f.edge(t, "forPatient", apptName, patientName)
	f.edge(t, "withProvider", apptName, providerName)
}

// tombstoneVertex soft-deletes a seeded vertex by rewriting its body with
// isDeleted: true and leaving every link it carries live — the exact shape
// TombstoneProvider leaves behind, which cascades to no practicesAt or
// withProvider link (ddls.go's sites_for_provider documents that non-cascade as
// the reason it gates on the provider VERTEX). Contract #1 filters such a vertex
// out of every graph walk (internal/refractor/ruleengine/full/executor.go's
// fetchNode), so a lens that reached a building THROUGH it now reaches nothing —
// which is what makes the atSite arm of the anchor expression load-bearing.
func (f *lensFixture) tombstoneVertex(t *testing.T, name string) {
	t.Helper()
	id := f.ids[name]
	key := "vtx." + f.types[id] + "." + id
	raw, err := json.Marshal(map[string]any{
		"key": key, "class": f.types[id], "isDeleted": true, "data": map[string]any{},
	})
	require.NoError(t, err)
	_, err = f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
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
	// This fixture's patient carries no identity, so the erasable column is null
	// and their name sits in the plaintext fallback (TestClinic*_IdentifiedPatientNameIsSecure
	// covers the identified case).
	require.Nil(t, v["patient_name"])
	require.Equal(t, "Alice Rivera", v["unlinked_patient_name"])
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
	f.aspect(t, "alice", "demographics", "patientDemographics", map[string]any{"registeredAt": "2026-06-01T09:00:00Z", "fullName": "Alice Rivera"})
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

// seedSitedAppointment mints an appointment for a patient + provider, gives the
// provider its practicesAt sites, and books the appointment atSite the FIRST of
// them — the only shape the write path can produce, since CreateAppointment /
// SetAppointmentSite / BackfillAppointmentSite all validate the site is one the
// provider practicesAt before writing the atSite link.
func (f *lensFixture) seedSitedAppointment(t *testing.T, apptName, patientName, providerName string, siteNames ...string) {
	t.Helper()
	f.seedAppointment(t, apptName, patientName, providerName)
	for _, s := range siteNames {
		f.vtx(t, s, "building")
		f.edge(t, "practicesAt", providerName, s)
	}
	if len(siteNames) > 0 {
		f.edge(t, "atSite", apptName, siteNames[0])
	}
}

// TestClinicAppointmentsRead_LiveProviderAnchorsOnPracticesAtWithoutDuplicate —
// the healthy path is untouched by the atSite arm. A live provider practising at
// two buildings anchors the row on BOTH, and the atSite building — necessarily
// one of those two, since the write path only ever records a site the provider
// practicesAt — appears exactly ONCE, not twice: the CASE selects one arm or the
// other, never unions them. Without this, every appointment in a healthy corpus
// would carry a duplicate token that an RLS `unnest` pays for on every read.
func TestClinicAppointmentsRead_LiveProviderAnchorsOnPracticesAtWithoutDuplicate(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedSitedAppointment(t, "appt", "alice", "drsam", "riverside", "downtown")

	rows := f.project(t, clinicAppointmentsReadSpec)
	require.Len(t, rows, 1)
	anchors := anchorStrings(t, rows[0].Values["authz_anchors"])
	require.ElementsMatch(t, []string{f.ids["alice"], f.ids["riverside"], f.ids["downtown"]}, anchors,
		"a live provider anchors the row on every building it practises at")
	require.Len(t, anchors, 3, "the atSite building must not be counted a second time alongside the practicesAt walk")
}

// TestClinicAppointmentsRead_TombstonedProviderFallsBackToAtSiteAnchor — the
// headline of the atSite arm. TombstoneProvider cascades to neither the
// withProvider nor the practicesAt link, but Contract #1 filters the dead
// provider out of every walk, so the practicesAt comprehension yields [] and the
// row would keep only its patient self-anchor — dropping a still-open
// appointment out of every front-desk world, readable then only by the reserved
// WildcardAnchor holder. The appointment's own atSite link carries it instead,
// exactly as ddls.go's appointment_sites carries the matching WRITE.
//
// The provider practises at TWO buildings and the appointment is booked at one
// of them, so the assertion cannot pass by accident: were the practicesAt walk
// still reaching the tombstoned provider, downtown would appear too.
func TestClinicAppointmentsRead_TombstonedProviderFallsBackToAtSiteAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedSitedAppointment(t, "appt", "alice", "drsam", "riverside", "downtown")
	f.tombstoneVertex(t, "drsam")

	rows := f.project(t, clinicAppointmentsReadSpec)
	require.Len(t, rows, 1, "a tombstoned provider costs the row visibility, never existence")
	v := rows[0].Values
	require.Nil(t, v["provider_key"], "the tombstoned provider is filtered out of the walk, so its display columns are null")
	require.Equal(t, "vtx.building."+f.ids["riverside"], v["site_key"], "the appointment's own site is unaffected by the provider's status")

	anchors := anchorStrings(t, v["authz_anchors"])
	require.ElementsMatch(t, []string{f.ids["alice"], f.ids["riverside"]}, anchors,
		"the appointment's own atSite building must anchor the row once the provider's practicesAt walk yields nothing")
	require.NotContains(t, anchors, f.ids["downtown"],
		"only the appointment's OWN site falls back — a tombstoned provider must not keep conferring every building it practised at")
}

// TestClinicAppointmentsRead_UnassignedLiveProviderFallsBackToAtSiteAnchor —
// the tombstone is not the only way the practicesAt walk empties out. A LIVE
// provider whose sites were all withdrawn by RemoveProviderSite reaches no
// building either, and the CASE gates on the WALK rather than on the provider's
// liveness precisely so this shape falls back too — the same condition
// appointment_sites tests before falling back. The site the appointment was
// booked at was a legitimate one when CreateAppointment validated it, and
// withdrawing the provider from it afterwards does not move where the visit
// happens.
//
// This is a deliberate behaviour change beyond the tombstone case, pinned here
// so it stands as a decision rather than a side effect: before, such a row
// anchored on its patient alone.
func TestClinicAppointmentsRead_UnassignedLiveProviderFallsBackToAtSiteAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedAppointment(t, "appt", "alice", "drsam")
	f.vtx(t, "riverside", "building")
	// The provider is alive and its profile still projects; it simply practises
	// nowhere — no practicesAt link at all, which is what RemoveProviderSite
	// leaves behind once the last one is withdrawn.
	f.edge(t, "atSite", "appt", "riverside")

	rows := f.project(t, clinicAppointmentsReadSpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "vtx.provider."+f.ids["drsam"], v["provider_key"], "the provider is LIVE — this is not the tombstone vector")
	require.ElementsMatch(t, []string{f.ids["alice"], f.ids["riverside"]}, anchorStrings(t, v["authz_anchors"]),
		"a live provider that practises nowhere empties the walk exactly as a tombstoned one does, so the atSite arm carries the row")
}

// TestClinicAppointmentsRead_LiveProviderAtSiteOutsidePracticesAtIsNotUnioned —
// the security-relevant non-regression. The anchor is a FALLBACK, never a union:
// while the provider's practicesAt walk yields anything, the appointment's own
// atSite building must NOT be folded in on top of it. The shape is reachable —
// RemoveProviderSite can withdraw the provider from the site an appointment was
// booked at while leaving it assigned elsewhere — and a union would hand staff
// at that withdrawn building a read the write path (whose appointment_sites
// returns the provider's CURRENT sites, never the stale atSite) would refuse.
func TestClinicAppointmentsRead_LiveProviderAtSiteOutsidePracticesAtIsNotUnioned(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedAppointment(t, "appt", "alice", "drsam")
	f.vtx(t, "riverside", "building") // where the visit was booked
	f.vtx(t, "downtown", "building")  // where the provider practises now
	f.edge(t, "atSite", "appt", "riverside")
	f.edge(t, "practicesAt", "drsam", "downtown")

	rows := f.project(t, clinicAppointmentsReadSpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "vtx.building."+f.ids["riverside"], v["site_key"], "the display column still reports where the visit is booked")

	anchors := anchorStrings(t, v["authz_anchors"])
	require.ElementsMatch(t, []string{f.ids["alice"], f.ids["downtown"]}, anchors,
		"while the practicesAt walk yields a building, that arm alone anchors the row")
	require.NotContains(t, anchors, f.ids["riverside"],
		"the atSite building must NOT be unioned in alongside a live practicesAt walk — "+
			"appointment_sites returns the provider's CURRENT sites there, so a union would grant a read the write path denies")
}

// TestClinicAppointmentsRead_TombstonedProviderWithNoAtSiteKeepsSelfAnchorOnly —
// the still-open residual, pinned so it is not mistaken for a regression. An
// appointment behind a tombstoned provider AND carrying no atSite link has no
// surviving path to any building, so it anchors on its patient alone: readable
// by that patient and by the WildcardAnchor holder, by no front-desk world. It
// still PROJECTS — a missing building costs a row its staff visibility, never
// its existence, and never a null anchor element (both arms are comprehensions,
// so the empty walk yields [] rather than the [null] ProtectedAdapter rejects).
func TestClinicAppointmentsRead_TombstonedProviderWithNoAtSiteKeepsSelfAnchorOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedAppointment(t, "appt", "alice", "drsam")
	f.vtx(t, "riverside", "building")
	f.edge(t, "practicesAt", "drsam", "riverside")
	f.tombstoneVertex(t, "drsam")

	rows := f.project(t, clinicAppointmentsReadSpec)
	require.Len(t, rows, 1, "existence is never conditional on a reachable building")
	v := rows[0].Values
	require.Nil(t, v["site_key"], "no atSite link — the fallback arm has nothing to offer")
	require.Equal(t, []string{f.ids["alice"]}, anchorStrings(t, v["authz_anchors"]),
		"no live provider and no atSite leaves the patient self-anchor alone — the known residual, not a null element")
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
	// This fixture's patient carries no identity, so the erasable column is null
	// and their name sits in the plaintext fallback (TestClinic*_IdentifiedPatientNameIsSecure
	// covers the identified case).
	require.Nil(t, v["patient_name"])
	require.Equal(t, "Alice Rivera", v["unlinked_patient_name"])
	require.Equal(t, providerKey, v["provider_key"])
	require.Equal(t, "Dr. Sam Okafor", v["provider_name"])
	require.Equal(t, "2026-07-01T15:35:00Z", v["documented_at"])
	require.Equal(t, true, v["follow_up_requested"])
	require.Equal(t, "2026-08-01", v["follow_up_date"])

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
	f.aspect(t, "alice", "demographics", "patientDemographics", map[string]any{"registeredAt": "2026-06-01T09:00:00Z", "fullName": "Alice Rivera"})
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
// TestLandlordLeaseApplicationsRead_ContactlessApplicantStillProjects). A
// patient with no appointments also has an empty workplace fan-out, so
// authz_anchors carries only the patient's own self-anchor.
func TestClinicPatientsRead_NoIdentityLinkStillProjects(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "alice", "patient")
	f.aspect(t, "alice", "demographics", "patientDemographics", map[string]any{"registeredAt": "2026-06-01T09:00:00Z", "fullName": "Alice Rivera"})

	rows := f.project(t, clinicPatientsReadSpec)
	require.Len(t, rows, 1, "a patient with no linked identity still projects")
	v := rows[0].Values
	require.Nil(t, v["name"], "the secure column is fed by the identity, which this patient has none of")
	require.Equal(t, "Alice Rivera", v["unlinked_name"])
	require.Nil(t, v["identity_key"])
	require.Nil(t, v["email"])
	require.Nil(t, v["phone"])
	require.Equal(t, []string{f.ids["alice"]}, anchorStrings(t, v["authz_anchors"]),
		"no appointments -> empty workplace fan-out, self-anchor only")
}

// TestClinicPatientsRead_ProjectsWorkplaceAnchor — the fix for the front-desk
// empty-roster bug (verticals.md "Front desk can't read the patient roster"):
// authz_anchors carries the patient's own NanoID plus the practicesAt building
// of every provider the patient has an appointment with, mirroring
// clinicAppointmentsReadSpec's own anchor one hop further out. This is what
// lets service-location's staffReadGrants (cap-read.staff, anchored on the
// building a front-desk actor worksAt) match a roster row, not only the
// reserved WildcardAnchor holder.
func TestClinicPatientsRead_ProjectsWorkplaceAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "alice", "patient")
	f.aspect(t, "alice", "demographics", "patientDemographics", map[string]any{"registeredAt": "2026-06-01T09:00:00Z", "fullName": "Alice Rivera"})
	f.vtx(t, "appt", "appointment")
	f.vtx(t, "drsam", "provider")
	f.vtx(t, "riverside", "building")
	f.edge(t, "forPatient", "appt", "alice")
	f.edge(t, "withProvider", "appt", "drsam")
	f.edge(t, "practicesAt", "drsam", "riverside")

	rows := f.project(t, clinicPatientsReadSpec)
	require.Len(t, rows, 1, "exactly one roster row per patient regardless of appointment count")
	v := rows[0].Values

	require.ElementsMatch(t, []string{f.ids["alice"], f.ids["riverside"]}, anchorStrings(t, v["authz_anchors"]),
		"authz_anchors must carry the patient's own NanoID plus the workplace building of a provider it has an appointment with")
}

// TestClinicPatientsRead_WorkplaceAnchorDedupesAcrossAppointments — a patient
// with several appointments through providers who all practise at the SAME
// building must project that building's token ONCE, not once per appointment:
// an unbounded authz_anchors array costs every staff roster read an ever-
// growing RLS `unnest`, and never shrinks on its own. A DIFFERENT building a
// fourth appointment reaches must still survive DISTINCT alongside it — a fix
// that collapsed every building to one (under-grant) would pass a
// single-building fixture as easily as the correct one. The patient also
// carries a live identifiedBy link, so this is the one fixture proving the
// WITH doesn't drop the identity-derived columns it asserts
// (identity_key/email) when the workplace branch is the one doing the
// folding; name/phone aren't seeded here and rely on the pre-existing
// identity fixtures instead.
func TestClinicPatientsRead_WorkplaceAnchorDedupesAcrossAppointments(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "alice", "patient")
	f.aspect(t, "alice", "demographics", "patientDemographics", map[string]any{"registeredAt": "2026-06-01T09:00:00Z", "fullName": "Alice Rivera"})
	f.vtx(t, "aliceId", "identity")
	emailEnv := map[string]any{"ct": "b64-email-ct", "nonce": "b64-nonce-1", "keyId": "alice-key"}
	f.aspect(t, "aliceId", "email", "email", emailEnv)
	f.edge(t, "identifiedBy", "alice", "aliceId")

	f.vtx(t, "riverside", "building")
	f.vtx(t, "downtown", "building")

	f.vtx(t, "appt1", "appointment")
	f.vtx(t, "drsam", "provider")
	f.edge(t, "forPatient", "appt1", "alice")
	f.edge(t, "withProvider", "appt1", "drsam")
	f.edge(t, "practicesAt", "drsam", "riverside")

	f.vtx(t, "appt2", "appointment")
	f.vtx(t, "drpat", "provider")
	f.edge(t, "forPatient", "appt2", "alice")
	f.edge(t, "withProvider", "appt2", "drpat")
	f.edge(t, "practicesAt", "drpat", "riverside")

	f.vtx(t, "appt3", "appointment")
	f.edge(t, "forPatient", "appt3", "alice")
	f.edge(t, "withProvider", "appt3", "drsam")

	f.vtx(t, "appt4", "appointment")
	f.vtx(t, "drkim", "provider")
	f.edge(t, "forPatient", "appt4", "alice")
	f.edge(t, "withProvider", "appt4", "drkim")
	f.edge(t, "practicesAt", "drkim", "downtown")

	rows := f.project(t, clinicPatientsReadSpec)
	require.Len(t, rows, 1, "exactly one roster row per patient regardless of appointment count")
	v := rows[0].Values

	require.Equal(t, "vtx.identity."+f.ids["aliceId"], v["identity_key"], "the identity branch must survive the WITH alongside the workplace fan-out")
	require.Equal(t, emailEnv, v["email"], "email must survive the WITH whole, not dropped by the workplace branch's grouping")

	anchors := anchorStrings(t, v["authz_anchors"])
	require.ElementsMatch(t, []string{f.ids["alice"], f.ids["riverside"], f.ids["downtown"]}, anchors,
		"three appointments at riverside collapse to one anchor, but the distinct downtown building must still survive DISTINCT")
	require.Len(t, anchors, 3, "authz_anchors must not grow by one entry per appointment, and must not collapse a genuinely distinct building")
}

// TestClinicPatientsRead_TombstonedProviderFallsBackToAtSiteAnchor — the roster
// half of the atSite fallback, and the proof that it resolves PER APPOINTMENT
// rather than per patient. Alice has two appointments: one through a live
// provider at riverside, one through a since-tombstoned provider booked at
// downtown. The live appointment must not suppress the dead one's site (a
// patient-level "provider sites, else atSite" fallback would do exactly that,
// since the provider arm is non-empty), so BOTH buildings anchor the roster row
// and front desk at either one still sees Alice.
//
// The tombstoned provider also practises at a THIRD building it never saw Alice
// at, which must NOT appear: a retired provider stops conferring the buildings
// it practised at, and only the appointment's own recorded site survives it.
func TestClinicPatientsRead_TombstonedProviderFallsBackToAtSiteAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "alice", "patient")
	f.aspect(t, "alice", "demographics", "patientDemographics", map[string]any{"registeredAt": "2026-06-01T09:00:00Z", "fullName": "Alice Rivera"})
	f.vtx(t, "riverside", "building")
	f.vtx(t, "downtown", "building")
	f.vtx(t, "uptown", "building")

	// Live provider, seen at riverside.
	f.vtx(t, "appt1", "appointment")
	f.vtx(t, "drsam", "provider")
	f.edge(t, "forPatient", "appt1", "alice")
	f.edge(t, "withProvider", "appt1", "drsam")
	f.edge(t, "practicesAt", "drsam", "riverside")
	f.edge(t, "atSite", "appt1", "riverside")

	// Retired provider, seen at downtown; it also practised at uptown.
	f.vtx(t, "appt2", "appointment")
	f.vtx(t, "drgone", "provider")
	f.edge(t, "forPatient", "appt2", "alice")
	f.edge(t, "withProvider", "appt2", "drgone")
	f.edge(t, "practicesAt", "drgone", "downtown")
	f.edge(t, "practicesAt", "drgone", "uptown")
	f.edge(t, "atSite", "appt2", "downtown")
	f.tombstoneVertex(t, "drgone")

	rows := f.project(t, clinicPatientsReadSpec)
	require.Len(t, rows, 1, "exactly one roster row per patient regardless of appointment count")

	anchors := anchorStrings(t, rows[0].Values["authz_anchors"])
	require.ElementsMatch(t, []string{f.ids["alice"], f.ids["riverside"], f.ids["downtown"]}, anchors,
		"a live appointment's provider building and a tombstoned appointment's own atSite building must BOTH anchor the roster row")
	require.NotContains(t, anchors, f.ids["uptown"],
		"a tombstoned provider must not keep conferring a building this patient was never seen at")
	require.Len(t, anchors, 3,
		"riverside is named by both the live provider's practicesAt walk and its appointment's atSite link — the single collect(DISTINCT) must fold that to one token")
}

// TestClinicPatientsRead_TombstonedProviderWithNoAtSiteKeepsSelfAnchorOnly — the
// roster's half of the still-open residual, pinned so it is not mistaken for a
// regression. A patient whose ONLY appointment is behind a tombstoned provider
// with no atSite link has no surviving path to any building, so the roster row
// anchors on the patient alone. It still projects: collect() drops nulls, so
// buildingAnchors is [] and never the [null] element ProtectedAdapter rejects.
func TestClinicPatientsRead_TombstonedProviderWithNoAtSiteKeepsSelfAnchorOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "alice", "patient")
	f.aspect(t, "alice", "demographics", "patientDemographics", map[string]any{"registeredAt": "2026-06-01T09:00:00Z", "fullName": "Alice Rivera"})
	f.vtx(t, "riverside", "building")
	f.vtx(t, "appt", "appointment")
	f.vtx(t, "drgone", "provider")
	f.edge(t, "forPatient", "appt", "alice")
	f.edge(t, "withProvider", "appt", "drgone")
	f.edge(t, "practicesAt", "drgone", "riverside")
	f.tombstoneVertex(t, "drgone")

	rows := f.project(t, clinicPatientsReadSpec)
	require.Len(t, rows, 1, "existence is never conditional on a reachable building")
	require.Equal(t, []string{f.ids["alice"]}, anchorStrings(t, rows[0].Values["authz_anchors"]),
		"no live provider and no atSite leaves the patient self-anchor alone — the known residual, not a null element")
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
	f.aspect(t, "alice", "demographics", "patientDemographics", map[string]any{"registeredAt": "2026-06-01T09:00:00Z", "fullName": "Alice Rivera"})
	f.vtx(t, "bob", "patient")
	f.aspect(t, "bob", "demographics", "patientDemographics", map[string]any{"registeredAt": "2026-06-01T09:00:00Z", "fullName": "Bob Nakamura"})

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

// TestProviderIdentityReadGrants proves the providerIdentityReadGrants
// producer's cypher (persona-worlds-design.md Fire W0 §3.2/§3.3): a grant row
// exists only while a login identity BOTH holds the identity-domain
// `provider` role AND is identifiedBy-bound to a clinic provider entity — the
// bound login inherits the provider vertex's own anchor tokens
// (providerAppointmentsRead's authz_anchors, projected by
// TestProviderAppointmentsRead_ProjectsProviderSelfAnchor above). Mirrors
// staffReadGrants' both-links-required shape (service-location/lenses.go,
// TestStaffReadGrants_RequiresBothLinks) and
// TestClinicProviderReadGrants_SelfAnchorsEachProvider's assertion style.
func TestProviderIdentityReadGrants(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	t.Run("both links present grants one row", func(t *testing.T) {
		f := newLensFixture(t)
		f.vtx(t, "drOsei", "identity")
		f.vtx(t, "roleProvider", "role")
		f.aspect(t, "roleProvider", "canonicalName", "canonicalName", map[string]any{"value": "provider"})
		f.vtx(t, "provOsei", "provider")
		f.aspect(t, "provOsei", "profile", "providerProfile", map[string]any{"fullName": "Dr. Amara Osei", "specialty": "Cardiology"})
		f.edge(t, "holdsRole", "drOsei", "roleProvider")
		f.edge(t, "identifiedBy", "provOsei", "drOsei")

		rows := f.project(t, providerIdentityReadGrantsSpec)
		require.Len(t, rows, 1)
		v := rows[0].Values
		require.Equal(t, f.ids["drOsei"], v["actor_id"], "the BOUND LOGIN is the grant's actor, not the provider entity")
		require.Equal(t, f.ids["provOsei"], v["anchor_id"], "the grant anchors on the provider entity's own NanoID")
		require.Equal(t, "cap-read.provider.clinic", v["grant_source"])
	})

	t.Run("role without identifiedBy grants nothing", func(t *testing.T) {
		f := newLensFixture(t)
		f.vtx(t, "drOsei", "identity")
		f.vtx(t, "roleProvider", "role")
		f.aspect(t, "roleProvider", "canonicalName", "canonicalName", map[string]any{"value": "provider"})
		f.vtx(t, "provOsei", "provider")
		f.aspect(t, "provOsei", "profile", "providerProfile", map[string]any{"fullName": "Dr. Amara Osei", "specialty": "Cardiology"})
		f.edge(t, "holdsRole", "drOsei", "roleProvider")

		require.Empty(t, f.project(t, providerIdentityReadGrantsSpec))
	})

	t.Run("identifiedBy without the role grants nothing", func(t *testing.T) {
		f := newLensFixture(t)
		f.vtx(t, "drOsei", "identity")
		f.vtx(t, "provOsei", "provider")
		f.aspect(t, "provOsei", "profile", "providerProfile", map[string]any{"fullName": "Dr. Amara Osei", "specialty": "Cardiology"})
		f.edge(t, "identifiedBy", "provOsei", "drOsei")

		require.Empty(t, f.project(t, providerIdentityReadGrantsSpec))
	})

	t.Run("a different role with identifiedBy grants nothing", func(t *testing.T) {
		f := newLensFixture(t)
		f.vtx(t, "drOsei", "identity")
		f.vtx(t, "roleConsumer", "role")
		f.aspect(t, "roleConsumer", "canonicalName", "canonicalName", map[string]any{"value": "consumer"})
		f.vtx(t, "provOsei", "provider")
		f.aspect(t, "provOsei", "profile", "providerProfile", map[string]any{"fullName": "Dr. Amara Osei", "specialty": "Cardiology"})
		f.edge(t, "holdsRole", "drOsei", "roleConsumer")
		f.edge(t, "identifiedBy", "provOsei", "drOsei")

		require.Empty(t, f.project(t, providerIdentityReadGrantsSpec), "the canonicalName predicate excludes every other role")
	})
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

// TestPatientIdentityReadGrants is TestProviderIdentityReadGrants' patient
// sibling: the bridge that makes a person's LOGIN the actor of their own
// patient-anchored reads, instead of the patient vertex standing in as its own
// actor. The grant exists for exactly as long as the identifiedBy link does.
//
// Unlike the provider producer there is deliberately NO role predicate — being
// the person a record is about is asserted by identifiedBy, not by a role — so
// the role cases below assert the opposite of the provider ones: a bound
// patient is granted whether or not any role is held.
func TestPatientIdentityReadGrants(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	t.Run("a bound patient grants one row, actor-different-from-anchor", func(t *testing.T) {
		f := newLensFixture(t)
		f.vtx(t, "carol", "identity")
		f.vtx(t, "patCarol", "patient")
		f.aspect(t, "patCarol", "demographics", "patientDemographics", map[string]any{"registeredAt": "2026-06-01T09:00:00Z", "fullName": "Carol Okafor"})
		f.edge(t, "identifiedBy", "patCarol", "carol")

		rows := f.project(t, patientIdentityReadGrantsSpec)
		require.Len(t, rows, 1)
		v := rows[0].Values
		require.Equal(t, f.ids["carol"], v["actor_id"], "the BOUND LOGIN is the grant's actor, not the patient entity")
		require.Equal(t, f.ids["patCarol"], v["anchor_id"], "the grant anchors on the patient entity's own NanoID")
		require.Equal(t, "cap-read.patient.clinic", v["grant_source"])
	})

	t.Run("a patient with no identifiedBy grants nothing", func(t *testing.T) {
		f := newLensFixture(t)
		f.vtx(t, "patCarol", "patient")
		f.aspect(t, "patCarol", "demographics", "patientDemographics", map[string]any{"registeredAt": "2026-06-01T09:00:00Z", "fullName": "Carol Okafor"})

		require.Empty(t, f.project(t, patientIdentityReadGrantsSpec))
	})

	t.Run("an identity bound to no patient grants nothing", func(t *testing.T) {
		f := newLensFixture(t)
		f.vtx(t, "carol", "identity")

		require.Empty(t, f.project(t, patientIdentityReadGrantsSpec))
	})

	t.Run("the grant does not depend on holding any role", func(t *testing.T) {
		f := newLensFixture(t)
		f.vtx(t, "carol", "identity")
		f.vtx(t, "patCarol", "patient")
		f.aspect(t, "patCarol", "demographics", "patientDemographics", map[string]any{"registeredAt": "2026-06-01T09:00:00Z", "fullName": "Carol Okafor"})
		f.edge(t, "identifiedBy", "patCarol", "carol")

		rows := f.project(t, patientIdentityReadGrantsSpec)
		require.Len(t, rows, 1, "a patient who has not been granted `consumer` still owns their own record")
	})

	t.Run("one bound patient grants exactly one row per pair", func(t *testing.T) {
		f := newLensFixture(t)
		f.vtx(t, "carol", "identity")
		f.vtx(t, "dan", "identity")
		f.vtx(t, "patCarol", "patient")
		f.aspect(t, "patCarol", "demographics", "patientDemographics", map[string]any{"registeredAt": "2026-06-01T09:00:00Z", "fullName": "Carol Okafor"})
		f.vtx(t, "patDan", "patient")
		f.aspect(t, "patDan", "demographics", "patientDemographics", map[string]any{"registeredAt": "2026-06-01T09:00:00Z", "fullName": "Dan Ruiz"})
		f.edge(t, "identifiedBy", "patCarol", "carol")
		f.edge(t, "identifiedBy", "patDan", "dan")

		rows := f.project(t, patientIdentityReadGrantsSpec)
		require.Len(t, rows, 2)
		byActor := map[string]string{}
		for _, r := range rows {
			byActor[r.Values["actor_id"].(string)] = r.Values["anchor_id"].(string)
		}
		require.Equal(t, f.ids["patCarol"], byActor[f.ids["carol"]], "each login anchors on its OWN patient")
		require.Equal(t, f.ids["patDan"], byActor[f.ids["dan"]])
	})
}

// TestProtectedAppointmentReads_EncounterWithoutDocumentationProjectsNull proves
// the pre-split-corpus null-safety case on BOTH patient- and provider-anchored
// protected read models: an appointment carrying the SENSITIVE .encounter aspect
// but no .documentation aspect still projects a row (never fails), with null
// documented_at / follow_up_requested / follow_up_date — the same discipline
// clinicAppointments (the open lens) proves at the unprotected layer.
func TestProtectedAppointmentReads_EncounterWithoutDocumentationProjectsNull(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "appt", "appointment")
	f.vtx(t, "alice", "patient")
	f.vtx(t, "drsam", "provider")
	f.aspect(t, "alice", "demographics", "patientDemographics", map[string]any{"registeredAt": "2026-06-01T09:00:00Z", "fullName": "Alice Rivera"})
	f.aspect(t, "drsam", "profile", "providerProfile", map[string]any{"fullName": "Dr. Sam Okafor", "specialty": "Cardiology"})
	f.aspect(t, "appt", "schedule", "appointmentSchedule", map[string]any{"startsAt": "2026-07-01T15:00:00Z", "endsAt": "2026-07-01T15:30:00Z"})
	f.aspect(t, "appt", "status", "appointmentStatus", map[string]any{"value": "completed"})
	// .encounter present, .documentation absent.
	f.aspect(t, "appt", "encounter", "appointmentEncounter", map[string]any{"summary": "Patient seen for annual checkup."})
	f.edge(t, "forPatient", "appt", "alice")
	f.edge(t, "withProvider", "appt", "drsam")

	for name, spec := range map[string]string{
		"clinicAppointmentsReadSpec":   clinicAppointmentsReadSpec,
		"providerAppointmentsReadSpec": providerAppointmentsReadSpec,
	} {
		rows := f.project(t, spec)
		require.Len(t, rows, 1, "%s: an appointment with .encounter but no .documentation still projects exactly one row", name)
		v := rows[0].Values
		require.Nil(t, v["documented_at"], "%s: no .documentation aspect → null documented_at, even though .encounter exists", name)
		require.Nil(t, v["follow_up_requested"], "%s: no .documentation aspect → null follow_up_requested", name)
		require.Nil(t, v["follow_up_date"], "%s: no .documentation aspect → null follow_up_date", name)
	}
}

// seedEncounter writes the SENSITIVE .encounter aspect as the shape step 6.5
// actually commits — a ciphertext envelope {ct, nonce, keyId} whose keyId names
// the clinicalRecord retention-class holder, never the appointment or the
// patient. The engine copies that map through verbatim; turning it back into
// summary/assessment/plan is pipeline.SecureDecryptor's job, proven in
// internal/refractor. What is proven HERE is that the cypher hands the decryptor
// the whole envelope in each of the three columns, which is the part a lens spec
// can get wrong.
func (f *lensFixture) seedEncounter(t *testing.T, apptName, holderID string) map[string]any {
	t.Helper()
	envelope := map[string]any{
		"ct":    "3q2+7w==",
		"nonce": "3q2+7w==",
		"keyId": "vtx.retentionclass." + holderID,
	}
	f.aspect(t, apptName, "encounter", "appointmentEncounter", envelope)
	return envelope
}

// TestClinicEncountersRead_ProjectsEnvelopePerColumnUnderProviderAnchor — the
// clinical record's read model projects one row per DOCUMENTED appointment,
// hands the same ciphertext envelope to each of the three secure columns, and
// anchors on the treating provider alone.
func TestClinicEncountersRead_ProjectsEnvelopePerColumnUnderProviderAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedAppointment(t, "appt", "alice", "drsam")
	envelope := f.seedEncounter(t, "appt", "7hRMjLYwg6WSpaXj7hRM")

	rows := f.project(t, clinicEncountersReadSpec)
	require.Len(t, rows, 1, "exactly one row per documented appointment")
	v := rows[0].Values

	require.Equal(t, f.ids["appt"], v["appointment_id"], "appointment_id is the IntoKey")
	require.Equal(t, "vtx.appointment."+f.ids["appt"], v["entity_key"])
	require.Equal(t, "vtx.patient."+f.ids["alice"], v["patient_key"])
	require.Equal(t, "vtx.provider."+f.ids["drsam"], v["provider_key"])
	require.Equal(t, "2026-07-01T15:35:00Z", v["documented_at"],
		"documented_at comes off the NON-sensitive .documentation sibling")

	for _, col := range []string{"summary", "assessment", "plan"} {
		require.Equal(t, envelope, v[col],
			"secure column %q must carry the WHOLE ciphertext envelope — the decryptor's Field selects the plaintext key, and a projection reaching into the envelope would yield null", col)
	}

	// The patient's NAME is deliberately absent: the plaintext note must not sit
	// beside a direct identifier in this table.
	require.NotContains(t, v, "patient_name",
		"the clinical record's own table must not carry the patient name")

	require.Equal(t, []string{f.ids["drsam"]}, anchorStrings(t, v["authz_anchors"]),
		"authz_anchors must be exactly the treating provider's bare NanoID — no workplace token, so the note is not front-desk readable")
}

// TestClinicEncountersRead_UndocumentedAppointmentProducesNoRow — an appointment
// nobody has documented projects no row at all. The presence filter reads the
// non-sensitive .documentation sibling, written in the same batch as .encounter,
// so the row set is exactly the set of appointments carrying a record.
func TestClinicEncountersRead_UndocumentedAppointmentProducesNoRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "appt", "appointment")
	f.vtx(t, "alice", "patient")
	f.vtx(t, "drsam", "provider")
	f.aspect(t, "appt", "schedule", "appointmentSchedule", map[string]any{"startsAt": "2026-07-01T15:00:00Z", "endsAt": "2026-07-01T15:30:00Z"})
	f.edge(t, "forPatient", "appt", "alice")
	f.edge(t, "withProvider", "appt", "drsam")

	require.Empty(t, f.project(t, clinicEncountersReadSpec),
		"an undocumented appointment must produce no PHI-columned row")
}

// TestClinicEncountersRead_NoProviderLinkProducesNoRow — withProvider is the
// REQUIRED anchor walk, so a documented appointment with no provider projects no
// row rather than one with a null anchor. Fail-closed: an unanchored row in a
// protected table is readable by a WildcardAnchor holder alone, but the
// clinical-record plane should not rely on that.
func TestClinicEncountersRead_NoProviderLinkProducesNoRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "appt", "appointment")
	f.vtx(t, "alice", "patient")
	f.aspect(t, "appt", "documentation", "appointmentDocumentation", map[string]any{"documentedAt": "2026-07-01T15:35:00Z", "followUpRequested": false})
	f.seedEncounter(t, "appt", "7hRMjLYwg6WSpaXj7hRM")
	f.edge(t, "forPatient", "appt", "alice")

	require.Empty(t, f.project(t, clinicEncountersReadSpec),
		"a documented appointment with no treating provider must project no row")
}

// TestClinicEncountersRead_AnchorScopesPerProvider — two providers' records never
// carry each other's anchor. The projection-layer proof of "a provider reads only
// the notes they wrote".
func TestClinicEncountersRead_AnchorScopesPerProvider(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedAppointment(t, "apptA", "alice", "drsam")
	f.seedAppointment(t, "apptB", "bob", "drlee")
	f.seedEncounter(t, "apptA", "7hRMjLYwg6WSpaXj7hRM")
	f.seedEncounter(t, "apptB", "7hRMjLYwg6WSpaXj7hRM")

	rows := f.project(t, clinicEncountersReadSpec)
	require.Len(t, rows, 2)
	byAppt := map[string][]string{}
	for _, r := range rows {
		byAppt[r.Values["appointment_id"].(string)] = anchorStrings(t, r.Values["authz_anchors"])
	}
	require.Equal(t, []string{f.ids["drsam"]}, byAppt[f.ids["apptA"]])
	require.Equal(t, []string{f.ids["drlee"]}, byAppt[f.ids["apptB"]])
	require.NotContains(t, byAppt[f.ids["apptA"]], f.ids["drlee"], "one provider's note must not carry another's anchor")
}

// seedIdentifiedPatient links a patient to an identity carrying the sensitive
// .name aspect as step 6.5 commits it — a ciphertext envelope. The engine copies
// it through; pipeline.SecureDecryptor turns it back into the name at
// projection. What these tests prove is that the cypher reaches the envelope at
// all, which is the half a lens spec can get wrong.
func (f *lensFixture) seedIdentifiedPatient(t *testing.T, patientName, identityName string) map[string]any {
	t.Helper()
	f.vtx(t, identityName, "identity")
	envelope := map[string]any{
		"ct":    "3q2+7w==",
		"nonce": "3q2+7w==",
		"keyId": "vtx.identity." + f.ids[identityName],
	}
	f.aspect(t, identityName, "name", "name", envelope)
	f.edge(t, "identifiedBy", patientName, identityName)
	return envelope
}

// TestClinicPatientsRead_IdentifiedPatientNameIsSecure — an identified patient's
// name arrives as the identity's ciphertext envelope in the secure `name`
// column, and the plaintext fallback stays empty. The two columns are disjoint
// by construction: CreatePatient writes fullName onto .demographics only when
// there is no identity to carry it.
func TestClinicPatientsRead_IdentifiedPatientNameIsSecure(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "alice", "patient")
	f.aspect(t, "alice", "demographics", "patientDemographics", map[string]any{"registeredAt": "2026-06-01T09:00:00Z"})
	envelope := f.seedIdentifiedPatient(t, "alice", "aliceId")

	rows := f.project(t, clinicPatientsReadSpec)
	require.Len(t, rows, 1, "the ghost filter now reads registeredAt, which an identified patient still carries")
	v := rows[0].Values
	require.Equal(t, envelope, v["name"],
		"the secure column must carry the identity's whole .name envelope for the decryptor")
	require.Nil(t, v["unlinked_name"],
		"an identified patient carries no plaintext name — that is the point of the move")
	require.Equal(t, "vtx.identity."+f.ids["aliceId"], v["identity_key"])
}

// TestAppointmentReads_IdentifiedPatientNameIsSecure — both appointment read
// models take the patient's name off the linked identity too, so a shred nulls
// it wherever it is displayed rather than only on the roster.
func TestAppointmentReads_IdentifiedPatientNameIsSecure(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name string
		spec string
	}{
		{"clinicAppointmentsRead", clinicAppointmentsReadSpec},
		{"providerAppointmentsRead", providerAppointmentsReadSpec},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLensFixture(t)
			f.seedAppointment(t, "appt", "alice", "drsam")
			// seedAppointment seeds the walk-in shape (a plaintext demographics
			// name). Overwrite it with what CreatePatient actually writes for an
			// IDENTIFIED patient — registeredAt only — so the two name columns
			// stay disjoint, which is the invariant under test.
			f.aspect(t, "alice", "demographics", "patientDemographics", map[string]any{"registeredAt": "2026-06-01T09:00:00Z"})
			envelope := f.seedIdentifiedPatient(t, "alice", "aliceId")

			rows := f.project(t, tc.spec)
			require.Len(t, rows, 1)
			v := rows[0].Values
			require.Equal(t, envelope, v["patient_name"],
				"patient_name must be the identity's .name envelope, not a plaintext copy")
			require.Nil(t, v["unlinked_patient_name"])
		})
	}
}

// TestClinicEncountersRead_CarriesNoPatientIdentifier — the clinical record's own
// table names no patient at all, linked or not. Both name columns exist on the
// other read models; neither belongs beside the decrypted note.
func TestClinicEncountersRead_CarriesNoPatientIdentifier(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedAppointment(t, "appt", "alice", "drsam")
	f.seedIdentifiedPatient(t, "alice", "aliceId")
	f.seedEncounter(t, "appt", "7hRMjLYwg6WSpaXj7hRM")

	rows := f.project(t, clinicEncountersReadSpec)
	require.Len(t, rows, 1)
	for _, col := range []string{"patient_name", "unlinked_patient_name", "name"} {
		require.NotContains(t, rows[0].Values, col,
			"the clinical record's table must carry no patient identifier — a reader joins read_provider_appointments for one")
	}
}

// seedNamedIdentity mints a bare identity carrying the sensitive .name aspect
// in the at-rest shape step 6.5 commits — a ciphertext envelope, no plaintext
// field — and returns the envelope for assertion.
func (f *lensFixture) seedNamedIdentity(t *testing.T, name string) map[string]any {
	t.Helper()
	f.vtx(t, name, "identity")
	envelope := map[string]any{
		"ct":    "3q2+7w==",
		"nonce": "3q2+7w==",
		"keyId": "vtx.identity." + f.ids[name],
	}
	f.aspect(t, name, "name", "name", envelope)
	return envelope
}

// TestClinicIdentitiesRead_ProjectsEnvelopeWholeAndSelfAnchors — one row per
// named identity: the `name` column carries the ciphertext envelope MAP whole
// (what the Secure-Lens decryptor consumes; the engine never sees a name), and
// authz_anchors carries exactly the identity's OWN bare NanoID. That
// self-anchor is what the platform's base cap-read self-grant matches, which
// is what lets a front-desk staffer — in no patient or provider roster — read
// their own name with no clinic-specific grant producer at all.
func TestClinicIdentitiesRead_ProjectsEnvelopeWholeAndSelfAnchors(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	envelope := f.seedNamedIdentity(t, "frontDesk")

	rows := f.project(t, clinicIdentitiesReadSpec)
	require.Len(t, rows, 1, "exactly one roster row for the one named identity")
	v := rows[0].Values
	require.Equal(t, f.ids["frontDesk"], v["identity_id"], "identity_id is the bare NanoID (the IntoKey)")
	require.Equal(t, "vtx.identity."+f.ids["frontDesk"], v["identity_key"])
	require.Equal(t, envelope, v["name"], "the envelope must reach the decryptor whole, never a field of it")
	require.Equal(t, []string{f.ids["frontDesk"]}, anchorStrings(t, v["authz_anchors"]),
		"self-anchored on the identity's own NanoID alone — no workplace fan-out")
}

// TestClinicIdentitiesRead_ExcludesUnnamedAndPlaintextShapedIdentities — the
// ciphertext-presence WHERE: an identity with no .name aspect and one whose
// .name data is plaintext-shaped ({value}, a shape step 6.5 can never commit)
// both project NO row, so the lens can neither roster unnamed service actors
// nor carry a plaintext name by itself.
func TestClinicIdentitiesRead_ExcludesUnnamedAndPlaintextShapedIdentities(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "svc", "identity") // no .name at all
	f.vtx(t, "legacy", "identity")
	f.aspect(t, "legacy", "name", "name", map[string]any{"value": "Plain Text"})
	f.seedNamedIdentity(t, "frontDesk")

	rows := f.project(t, clinicIdentitiesReadSpec)
	require.Len(t, rows, 1, "only the ciphertext-named identity projects")
	require.Equal(t, "vtx.identity."+f.ids["frontDesk"], rows[0].Values["identity_key"])
}

// TestClinicIdentitiesRead_OneRowPerIdentityRegardlessOfClinicLinks — an
// identity bound to a patient AND a provider still projects exactly one row.
// The spec anchors on the identity alone and walks nothing, so no clinic-side
// link can fan a row out and collide on the single-valued identity_id IntoKey.
func TestClinicIdentitiesRead_OneRowPerIdentityRegardlessOfClinicLinks(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedNamedIdentity(t, "sam")
	f.vtx(t, "samPatient", "patient")
	f.vtx(t, "samProvider", "provider")
	f.edge(t, "identifiedBy", "samPatient", "sam")
	f.edge(t, "identifiedBy", "samProvider", "sam")

	rows := f.project(t, clinicIdentitiesReadSpec)
	require.Len(t, rows, 1, "one row per identity — the lens walks no clinic link")
	require.Equal(t, []string{f.ids["sam"]}, anchorStrings(t, rows[0].Values["authz_anchors"]))
}
