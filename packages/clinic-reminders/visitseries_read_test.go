package clinicreminders

// Rule-engine proof of the visitSeriesRead protected Postgres read model
// (D1.5, mirroring clinic-domain's TestClinicAppointmentsRead_* suite). These
// drive visitSeriesReadSpec through the same `full` engine selected at
// activation (engine:"full"), against an embedded NATS Core/Adjacency KV, and
// assert the ENGINE PROJECTION ROW: the display scalars hop correctly and —
// the headline — authz_anchors carries exactly the patient's bare NanoID. The
// Postgres RLS round-trip is the platform-side proof (cmd/clinic-app's
// visitseries_test.go, gated on POSTGRES_TEST_DSN); the cypher's anchor
// derivation is proven here.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// seedVisitSeries mints one visitseries linked to a named patient + provider,
// with the full display-column surface.
func (f *remFixture) seedVisitSeries(t *testing.T, seriesName, patientName, providerName string) {
	t.Helper()
	f.mkVisitSeries(t, seriesName, 30, "", "2026-08-01T09:00:00Z", 2, nil)
	f.vtx(t, patientName, "patient")
	f.vtx(t, providerName, "provider")
	f.aspect(t, patientName, "demographics", "patientDemographics", map[string]any{"fullName": "Alice Rivera"})
	f.aspect(t, providerName, "profile", "providerProfile", map[string]any{"fullName": "Dr. Sam Okafor", "specialty": "Cardiology"})
	f.edge(t, "forPatient", seriesName, patientName)
	f.edge(t, "withProvider", seriesName, providerName)
}

// TestVisitSeriesRead_ProjectsPatientSelfAnchor — the protected read model
// projects one row per series carrying the display scalars and an
// authz_anchors set of exactly the patient's bare NanoID (§6.14). This is the
// grant RLS matches: the base cap-read.<actor> self-anchor grants the patient
// their own NanoID, so the row is readable by the patient and nobody else.
func TestVisitSeriesRead_ProjectsPatientSelfAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.seedVisitSeries(t, "series", "alice", "drsam")
	seriesKey := "vtx.visitseries." + f.ids["series"]
	patientKey := "vtx.patient." + f.ids["alice"]
	providerKey := "vtx.provider." + f.ids["drsam"]

	rows := f.project(t, visitSeriesReadSpec)
	require.Len(t, rows, 1, "exactly one read-model row per series")
	v := rows[0].Values

	require.Equal(t, f.ids["series"], v["series_id"], "series_id is the series' bare NanoID (the IntoKey)")
	require.Equal(t, seriesKey, v["entity_key"])
	require.Equal(t, patientKey, v["patient_key"])
	require.Equal(t, "Alice Rivera", v["patient_name"])
	require.Equal(t, providerKey, v["provider_key"])
	require.Equal(t, "Dr. Sam Okafor", v["provider_name"])
	require.Equal(t, "Cardiology", v["provider_specialty"])
	require.Equal(t, float64(30), v["interval_days"])
	require.Equal(t, "2026-08-01T09:00:00Z", v["next_due_at"])
	require.Equal(t, float64(2), v["occurrence_count"])
	require.Equal(t, true, v["active"])

	anchors, ok := v["authz_anchors"].([]any)
	require.True(t, ok, "authz_anchors must project as a list")
	require.Equal(t, []any{f.ids["alice"]}, anchors,
		"authz_anchors must carry exactly the patient's bare NanoID (the §6.14 self-anchor RLS matches)")
}

// TestVisitSeriesRead_AnchorScopesPerPatient — two series for two different
// patients each anchor to ONLY their own patient NanoID.
func TestVisitSeriesRead_AnchorScopesPerPatient(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.seedVisitSeries(t, "seriesA", "alice", "drsam")
	f.seedVisitSeries(t, "seriesB", "bob", "drsam")

	rows := f.project(t, visitSeriesReadSpec)
	require.Len(t, rows, 2)
	byID := map[string][]any{}
	for _, r := range rows {
		byID[r.Values["series_id"].(string)] = r.Values["authz_anchors"].([]any)
	}
	require.Equal(t, []any{f.ids["alice"]}, byID[f.ids["seriesA"]], "seriesA anchors only to alice")
	require.Equal(t, []any{f.ids["bob"]}, byID[f.ids["seriesB"]], "seriesB anchors only to bob")
}

// TestVisitSeriesRead_NoPatientLinkProducesNoRow — a series with no forPatient
// link projects NO row at all (forPatient is a required MATCH, the anchor
// walk) — fail-closed, never a null anchor.
func TestVisitSeriesRead_NoPatientLinkProducesNoRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkVisitSeries(t, "orphan", 30, "", "2026-08-01T09:00:00Z", 0, nil) // no forPatient link
	f.seedVisitSeries(t, "series", "alice", "drsam")

	rows := f.project(t, visitSeriesReadSpec)
	require.Len(t, rows, 1, "only the well-formed series projects; the no-patient shell is excluded")
	require.Equal(t, f.ids["series"], rows[0].Values["series_id"])
}

// TestVisitSeriesRead_NoProviderLinkStillProjects — withProvider is OPTIONAL
// (a display-only neighbour, not the anchor): a series missing its provider
// link still projects a row anchored to the patient, with provider columns
// null.
func TestVisitSeriesRead_NoProviderLinkStillProjects(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkVisitSeries(t, "series", 30, "", "2026-08-01T09:00:00Z", 0, nil)
	f.vtx(t, "alice", "patient")
	f.aspect(t, "alice", "demographics", "patientDemographics", map[string]any{"fullName": "Alice Rivera"})
	f.edge(t, "forPatient", "series", "alice")

	rows := f.project(t, visitSeriesReadSpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Nil(t, v["provider_key"], "no withProvider link → null provider_key")
	require.Nil(t, v["provider_name"], "no withProvider link → null provider_name")
	anchors, ok := v["authz_anchors"].([]any)
	require.True(t, ok)
	require.Equal(t, []any{f.ids["alice"]}, anchors)
}

// TestVisitSeriesRead_PausedProjectsInactive — active reflects the same
// paused/activeUntil derivation as visitSeriesDueSpec.
func TestVisitSeriesRead_PausedProjectsInactive(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	yes := true
	f.mkVisitSeries(t, "series", 30, "", "2026-08-01T09:00:00Z", 0, &yes)
	f.vtx(t, "alice", "patient")
	f.edge(t, "forPatient", "series", "alice")

	rows := f.project(t, visitSeriesReadSpec)
	require.Len(t, rows, 1)
	require.Equal(t, false, rows[0].Values["active"], "paused series projects active=false")
}
