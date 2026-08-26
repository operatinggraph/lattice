package clinicreminders

// Rule-engine proof of the visitSeriesRead protected Postgres read model
// (D1.5, mirroring clinic-domain's TestClinicAppointmentsRead_* suite). These
// drive visitSeriesReadSpec through the same `full` engine selected at
// activation (engine:"full"), against an embedded NATS Core/Adjacency KV, and
// assert the ENGINE PROJECTION ROW: the display scalars hop correctly and —
// the headline — authz_anchors carries the patient's bare NanoID plus the
// series' provider's workplace token, when one is wired (workplace_anchor_test.go
// pins the comprehension; TestVisitSeriesRead_AnchorsProviderWorkplace below
// proves the token appears). The Postgres RLS round-trip is the platform-side
// proof (cmd/clinic-app's visitseries_test.go, gated on POSTGRES_TEST_DSN);
// the cypher's anchor derivation is proven here.

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
	f.aspect(t, patientName, "demographics", "patientDemographics", map[string]any{"registeredAt": "2026-06-01T09:00:00Z", "fullName": "Alice Rivera"})
	f.aspect(t, providerName, "profile", "providerProfile", map[string]any{"fullName": "Dr. Sam Okafor", "specialty": "Cardiology"})
	f.edge(t, "forPatient", seriesName, patientName)
	f.edge(t, "withProvider", seriesName, providerName)
}

// TestVisitSeriesRead_ProjectsPatientSelfAnchor — the protected read model
// projects one row per series carrying the display scalars and an
// authz_anchors set of exactly the patient's bare NanoID (§6.14) WHEN the
// series' provider carries no practicesAt link, as this fixture's does not
// (TestVisitSeriesRead_AnchorsProviderWorkplace covers the wired case). This
// is the grant the base cap-read.<actor> self-anchor matches: the patient's
// own NanoID grants them the row.
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
	// No identity on this fixture's patient, so the erasable column is null and
	// the name sits in the plaintext fallback.
	require.Nil(t, v["patient_name"])
	require.Equal(t, "Alice Rivera", v["unlinked_patient_name"])
	require.Equal(t, providerKey, v["provider_key"])
	require.Equal(t, "Dr. Sam Okafor", v["provider_name"])
	require.Equal(t, "Cardiology", v["provider_specialty"])
	require.Equal(t, float64(30), v["interval_days"])
	require.Equal(t, "2026-08-01T09:00:00Z", v["next_due_at"])
	require.Equal(t, float64(2), v["occurrence_count"])
	require.Equal(t, "active", v["series_status"])

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
	f.aspect(t, "alice", "demographics", "patientDemographics", map[string]any{"registeredAt": "2026-06-01T09:00:00Z", "fullName": "Alice Rivera"})
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

// TestVisitSeriesRead_PausedProjectsPaused — series_status distinguishes an
// explicitly paused series from a naturally-ended one (verticals.md "A
// naturally-ended visit series still shows a working Resume button"): both
// used to collapse into one fused `active=false`, indistinguishable at the
// client.
func TestVisitSeriesRead_PausedProjectsPaused(t *testing.T) {
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
	require.Equal(t, "paused", rows[0].Values["series_status"])
}

// TestVisitSeriesRead_NaturallyEndedProjectsEnded — a series never paused
// whose next occurrence would fall past its own activeUntil reads "ended",
// not "paused": the exact distinction the fused boolean lost, which let a
// finished series show a Resume button that submitted and changed nothing
// observable (verticals.md).
func TestVisitSeriesRead_NaturallyEndedProjectsEnded(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkVisitSeries(t, "series", 30, "2026-07-01T09:00:00Z", "2026-08-01T09:00:00Z", 0, nil)
	f.vtx(t, "alice", "patient")
	f.edge(t, "forPatient", "series", "alice")

	rows := f.project(t, visitSeriesReadSpec)
	require.Len(t, rows, 1)
	require.Equal(t, "ended", rows[0].Values["series_status"],
		"never paused, but nextDueAt (2026-08-01) is past activeUntil (2026-07-01)")
}

// TestVisitSeriesRead_PausedPastItsEndStaysPaused — a series paused BEFORE it
// would have ended stays "paused", not "ended": pausing is what the human
// did, and reclassifying it out from under them just because wall-clock
// caught up would make Resume (which still works — pausing freezes
// nextDueAt) look like a dead end it is not.
func TestVisitSeriesRead_PausedPastItsEndStaysPaused(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	yes := true
	f.mkVisitSeries(t, "series", 30, "2026-07-01T09:00:00Z", "2026-08-01T09:00:00Z", 0, &yes)
	f.vtx(t, "alice", "patient")
	f.edge(t, "forPatient", "series", "alice")

	rows := f.project(t, visitSeriesReadSpec)
	require.Len(t, rows, 1)
	require.Equal(t, "paused", rows[0].Values["series_status"])
}

// TestVisitSeriesRead_NoActiveUntilNeverEnds — an open-ended series (no
// activeUntil at all) can never read "ended", regardless of how far
// nextDueAt has rolled.
func TestVisitSeriesRead_NoActiveUntilNeverEnds(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkVisitSeries(t, "series", 30, "", "2099-01-01T09:00:00Z", 40, nil)
	f.vtx(t, "alice", "patient")
	f.edge(t, "forPatient", "series", "alice")

	rows := f.project(t, visitSeriesReadSpec)
	require.Len(t, rows, 1)
	require.Equal(t, "active", rows[0].Values["series_status"])
}

// TestVisitSeriesRead_SeriesEndableTracksNotEnded — series_endable is the
// EndVisitSeries op-meta's VisibleWhen gate (OpVisibleWhenSpec is single-
// condition equality, so a positive "endable" flag stands in for "series_status
// <> ended"): true for both "active" and "paused" (only "ended" ever disables
// it), and it flips false the moment the same clean-termination condition that
// derives "ended" is met — never a second, independently-drifting predicate.
func TestVisitSeriesRead_SeriesEndableTracksNotEnded(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	yes := true
	cases := []struct {
		name        string
		paused      *bool
		activeUntil string
		nextDueAt   string
		wantStatus  string
		wantEndable bool
	}{
		{"active", nil, "", "2026-08-01T09:00:00Z", "active", true},
		{"paused", &yes, "", "2026-08-01T09:00:00Z", "paused", true},
		{"ended", nil, "2026-07-01T09:00:00Z", "2026-08-01T09:00:00Z", "ended", false},
		{"pausedPastItsEnd", &yes, "2026-07-01T09:00:00Z", "2026-08-01T09:00:00Z", "paused", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newRemFixture(t)
			f.mkVisitSeries(t, "series", 30, c.activeUntil, c.nextDueAt, 0, c.paused)
			f.vtx(t, "alice", "patient")
			f.edge(t, "forPatient", "series", "alice")

			rows := f.project(t, visitSeriesReadSpec)
			require.Len(t, rows, 1)
			require.Equal(t, c.wantStatus, rows[0].Values["series_status"])
			require.Equal(t, c.wantEndable, rows[0].Values["series_endable"])
		})
	}
}

// TestVisitSeriesRead_AnchorsProviderWorkplace — authz_anchors carries the
// patient's own NanoID PLUS the workplace token (the building the series'
// provider practises at), mirroring clinicAppointmentsReadSpec exactly. This
// is what lets cmd/facet's frontOfHouse worklist pane read a workplace's
// series through service-location's staffReadGrants, without a WildcardAnchor.
func TestVisitSeriesRead_AnchorsProviderWorkplace(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.seedVisitSeries(t, "series", "alice", "drsam")
	f.vtx(t, "riverside", "building")
	f.edge(t, "practicesAt", "drsam", "riverside")

	rows := f.project(t, visitSeriesReadSpec)
	require.Len(t, rows, 1)
	anchors, ok := rows[0].Values["authz_anchors"].([]any)
	require.True(t, ok, "authz_anchors must project as a list")
	require.Equal(t, []any{f.ids["alice"], f.ids["riverside"]}, anchors,
		"authz_anchors must carry the patient anchor first, then the provider's workplace token")
}

// TestVisitSeriesRead_ProviderWithNoWorkplaceStillProjects — a provider who
// practises nowhere costs the row its staff visibility, never its existence
// (§ the null-element hazard the pattern comprehension exists to avoid): the
// series still projects, anchored to the patient alone.
func TestVisitSeriesRead_ProviderWithNoWorkplaceStillProjects(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.seedVisitSeries(t, "series", "alice", "drsam") // drsam has no practicesAt link

	rows := f.project(t, visitSeriesReadSpec)
	require.Len(t, rows, 1)
	anchors, ok := rows[0].Values["authz_anchors"].([]any)
	require.True(t, ok)
	require.Equal(t, []any{f.ids["alice"]}, anchors,
		"a workplace-less provider must cost the row its staff visibility, never the row itself")
}

// TestVisitSeriesRead_NoWithProviderLinkDoesNotLeakAnUnrelatedBuilding — a
// series with NO withProvider link at all (pr unbound, not merely
// workplace-less) must anchor to the patient alone even while an UNRELATED
// series' provider practises somewhere: the comprehension starts from pr
// itself, never a keyspace scan, so a null pr can never pick up a building
// that belongs to a different provider entirely.
func TestVisitSeriesRead_NoWithProviderLinkDoesNotLeakAnUnrelatedBuilding(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkVisitSeries(t, "orphan", 30, "", "2026-08-01T09:00:00Z", 0, nil) // no withProvider link
	f.vtx(t, "alice", "patient")
	f.edge(t, "forPatient", "orphan", "alice")

	f.seedVisitSeries(t, "series", "bob", "drsam")
	f.vtx(t, "riverside", "building")
	f.edge(t, "practicesAt", "drsam", "riverside")

	rows := f.project(t, visitSeriesReadSpec)
	require.Len(t, rows, 2)
	byID := map[string][]any{}
	for _, r := range rows {
		byID[r.Values["series_id"].(string)] = r.Values["authz_anchors"].([]any)
	}
	require.Equal(t, []any{f.ids["alice"]}, byID[f.ids["orphan"]],
		"a provider-less series must anchor to its patient alone, never an unrelated series' building")
	require.Equal(t, []any{f.ids["bob"], f.ids["riverside"]}, byID[f.ids["series"]])
}
