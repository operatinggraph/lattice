package leasesigning

// Rule-engine proof of the leaseApplicationsRead protected Postgres read model
// (D1.3 Fire 2, the applicant-self milestone).
//
// These drive leaseApplicationsReadSpec through the same `full` engine selected
// at activation (engine:"full"), against an embedded NATS Core/Adjacency KV, and
// assert the ENGINE PROJECTION ROW: the business scalars hop correctly and — the
// headline — authz_anchors carries exactly the applicant's bare NanoID, scoped
// per application. The Postgres RLS round-trip (the table provisioning + the
// set-membership policy + SET LOCAL lattice.actor_id) is the platform-side proof
// (internal/refractor adapter/rls tests, gated on POSTGRES_TEST_DSN) and the
// Fire-3 boundary e2e; the cypher's anchor derivation is proven here.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// projectRead runs the unparameterized leaseApplicationsRead lens over every
// leaseapp in the fixture and returns the projected rows.
func (f *lensFixture) projectRead(t *testing.T) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(leaseApplicationsReadSpec)
	require.NoError(t, err, "leaseApplicationsRead cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

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

// seedApplication mints one leaseapp with an applicant identity and a leased
// unit carrying address + listing aspects, plus the application's own .terms /
// .signature / .decision aspects — the full display-column surface.
func (f *lensFixture) seedApplication(t *testing.T, appName, applicantName, unitName string) {
	t.Helper()
	f.vtx(t, appName, "leaseapp")
	f.vtx(t, applicantName, "identity")
	f.vtx(t, unitName, "unit")
	f.aspect(t, unitName, "address", "address", map[string]any{"line1": "1 Market St", "city": "San Francisco", "region": "CA"})
	f.aspect(t, unitName, "listing", "listing", map[string]any{"rentAmount": 4200, "rentCurrency": "USD", "status": "available", "bedrooms": 2, "bathrooms": 1.5, "availableFrom": "2026-08-01T00:00:00Z"})
	f.aspect(t, appName, "terms", "terms", map[string]any{"moveInDate": "2026-08-01", "leaseTermMonths": 12, "requestedRent": 4100})
	f.aspect(t, appName, "signature", "signature", map[string]any{"signedAt": "2026-07-15T00:00:00Z"})
	f.aspect(t, appName, "decision", "decision", map[string]any{"value": "approved"})
	f.edge(t, "applicationFor", appName, applicantName)
	f.edge(t, "appliesToUnit", appName, unitName)
}

// TestLeaseApplicationsRead_ProjectsApplicantSelfAnchor — the protected read
// model projects one row per application carrying the display scalars and an
// authz_anchors set of exactly the applicant's bare NanoID (§6.14). This is the
// grant RLS matches: the base cap-read.<actor> self-anchor grants the applicant
// their own NanoID, so the row is readable by the applicant and nobody else.
func TestLeaseApplicationsRead_ProjectsApplicantSelfAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedApplication(t, "app", "alice", "unit1")
	appKey := "vtx.leaseapp." + f.ids["app"]
	aliceKey := "vtx.identity." + f.ids["alice"]
	unitKey := "vtx.unit." + f.ids["unit1"]

	rows := f.projectRead(t)
	require.Len(t, rows, 1, "exactly one read-model row per leaseapp")
	v := rows[0].Values

	require.Equal(t, f.ids["app"], v["app_id"], "app_id is the application's bare NanoID (the IntoKey)")
	require.Equal(t, appKey, v["entity_key"], "entity_key keeps the full leaseapp key")
	require.Equal(t, aliceKey, v["applicant"])
	require.Equal(t, unitKey, v["unit_key"])
	require.Equal(t, "1 Market St", v["unit_address"])
	require.Equal(t, "San Francisco", v["unit_city"])
	require.Equal(t, "CA", v["unit_region"])
	require.EqualValues(t, 4200, v["unit_rent"])
	require.Equal(t, "USD", v["unit_currency"])
	require.Equal(t, "available", v["unit_status"])
	require.EqualValues(t, 2, v["unit_bedrooms"], "D1.5: unit_bedrooms is projected for the lease-document builder")
	require.EqualValues(t, 1.5, v["unit_bathrooms"], "D1.5: unit_bathrooms is projected for the lease-document builder")
	require.Equal(t, "2026-08-01T00:00:00Z", v["unit_available_from"], "D1.5: unit_available_from is projected for the lease-document builder")
	require.Equal(t, "2026-07-15T00:00:00Z", v["signed_at"])
	require.Equal(t, "approved", v["landlord_decision"])
	require.Equal(t, "2026-08-01", v["terms_move_in_date"])
	require.EqualValues(t, 12, v["terms_lease_term_months"])
	require.EqualValues(t, 4100, v["terms_requested_rent"])

	// The headline: authz_anchors is exactly [alice's bare NanoID].
	require.Equal(t, []string{f.ids["alice"]}, anchorStrings(t, v["authz_anchors"]),
		"authz_anchors must carry exactly the applicant's bare NanoID (the §6.14 self-anchor RLS matches)")
}

// TestLeaseApplicationsRead_AnchorScopesPerApplicant — two applications by two
// applicants each anchor to ONLY their own applicant NanoID. This is the
// projection-layer proof of the "A sees only A's applications" headline: RLS,
// matching each row's authz_anchors against the reading actor's granted anchors,
// returns A's row to A and B's row to B with no overlap. (The DB enforcement is
// Fire 3's e2e; here we prove the lens never cross-anchors.)
func TestLeaseApplicationsRead_AnchorScopesPerApplicant(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedApplication(t, "appA", "alice", "unitA")
	f.seedApplication(t, "appB", "bob", "unitB")

	rows := f.projectRead(t)
	require.Len(t, rows, 2)
	byApp := map[string][]string{}
	for _, r := range rows {
		byApp[r.Values["app_id"].(string)] = anchorStrings(t, r.Values["authz_anchors"])
	}
	require.Equal(t, []string{f.ids["alice"]}, byApp[f.ids["appA"]], "A's application anchors only to A")
	require.Equal(t, []string{f.ids["bob"]}, byApp[f.ids["appB"]], "B's application anchors only to B")
	require.NotContains(t, byApp[f.ids["appA"]], f.ids["bob"], "A's row must NOT carry B's anchor")
	require.NotContains(t, byApp[f.ids["appB"]], f.ids["alice"], "B's row must NOT carry A's anchor")
}

// TestLeaseApplicationsRead_BareShellProducesNoRow — a malformed application with
// no applicationFor link projects NO row at all (applicationFor is a required
// MATCH). A shell that no applicant anchor would protect never enters the read
// model — the strongest fail-closed posture (and it avoids handing the array
// adapter a null anchor element). A well-formed application alongside it still
// projects normally, proving the required MATCH excludes only the shell.
func TestLeaseApplicationsRead_BareShellProducesNoRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "orphan", "leaseapp") // no applicationFor link
	f.seedApplication(t, "app", "alice", "unit1")

	rows := f.projectRead(t)
	require.Len(t, rows, 1, "only the well-formed application projects; the no-applicant shell is excluded")
	require.Equal(t, f.ids["app"], rows[0].Values["app_id"])
	require.Equal(t, []string{f.ids["alice"]}, anchorStrings(t, rows[0].Values["authz_anchors"]))
}

// TestLeaseApplicationsRead_DocPointersOnlyWhenAttached — the doc_store_name /
// doc_filename / doc_content_type columns project ONLY once the signedLease
// attachment exists: a completed docGen outcome with no anchored object still
// projects null (the GET answers "being generated"), and the attachment alone
// (without the outcome) carries no pointers to project. The extra fans stay
// aggregated — one row per application throughout.
func TestLeaseApplicationsRead_DocPointersOnlyWhenAttached(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedApplication(t, "app", "alice", "unit1")

	// No docGen claim at all: null pointers.
	rows := f.projectRead(t)
	require.Len(t, rows, 1)
	require.Nil(t, rows[0].Values["doc_store_name"])
	require.Nil(t, rows[0].Values["doc_filename"])
	require.Nil(t, rows[0].Values["doc_content_type"])

	// Completed docGen outcome, NOT yet anchored: still null (the app's GET
	// serves only anchored documents).
	f.vtxWithClass(t, "dg1", "service", "service.docGen.instance")
	f.edge(t, "providedTo", "dg1", "app")
	f.aspect(t, "dg1", "outcome", "leaseDocOutcome", map[string]any{
		"status": "completed", "completedAt": "2026-07-15T00:00:05Z",
		"digest": "SHA-256=abc123", "size": 1264, "contentType": "text/plain; charset=utf-8",
		"storeName": "dgStoreNanoXyz", "filename": "signed-lease-leaseapp.test.txt",
	})
	rows = f.projectRead(t)
	require.Len(t, rows, 1, "the docGen fan must not multiply the row")
	require.Nil(t, rows[0].Values["doc_store_name"], "an un-anchored document projects no pointers")

	// The signedLease attachment lands: the pointers project.
	f.vtx(t, "leaseDocObj", "object")
	f.edge(t, "signedLease", "leaseDocObj", "app")
	rows = f.projectRead(t)
	require.Len(t, rows, 1, "the attachment fan must not multiply the row")
	v := rows[0].Values
	require.Equal(t, "dgStoreNanoXyz", v["doc_store_name"])
	require.Equal(t, "signed-lease-leaseapp.test.txt", v["doc_filename"])
	require.Equal(t, "text/plain; charset=utf-8", v["doc_content_type"])
	require.Equal(t, []string{f.ids["alice"]}, anchorStrings(t, v["authz_anchors"]),
		"the applicant-self anchor is untouched by the doc fans")
}
