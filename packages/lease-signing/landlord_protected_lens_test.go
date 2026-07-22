package leasesigning

// Rule-engine proof of the landlordLeaseApplicationsRead protected Postgres read
// model (D1.3 Increment 2, the landlord/residence audience).
//
// These drive landlordLeaseApplicationsReadSpec through the same `full` engine
// selected at activation (engine:"full"), against an embedded NATS Core/Adjacency
// KV, and assert the ENGINE PROJECTION ROW: the row anchors to the MANAGING
// landlord's bare NanoID, resolved by an INBOUND walk of loftspace-domain's
// `manages` link. The headline — a landlord row's authz_anchors carries exactly
// the managing landlord's NanoID — is the grant the §6.14 RLS policy matches: the
// primordial cap-read self-grant grants the landlord their own NanoID, so the
// landlord (and nobody else) reads applications to units they manage, with NO
// `cap-read.residence` grant lens and NO link-triggered reprojection primitive.
// The Postgres RLS round-trip (table provisioning + the set-membership policy +
// SET LOCAL lattice.actor_id) is the platform-side proof (internal/refractor
// adapter/rls tests, POSTGRES_TEST_DSN) and the Increment-3 boundary e2e; the
// cypher's anchor derivation is proven here.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/vault"
)

// projectLandlordRead runs the landlordLeaseApplicationsRead lens over every
// leaseapp in the fixture with the real wall-clock $now (the D1.5 Rec-C
// `qualified` readiness clone's bgcheck-freshness term needs it — the same
// param the live pipeline supplies, executeFullForActor's
// params["now"] = time.Now().UTC().Format(time.RFC3339)) and returns the
// projected rows.
func (f *lensFixture) projectLandlordRead(t *testing.T) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(landlordLeaseApplicationsReadSpec)
	require.NoError(t, err, "landlordLeaseApplicationsRead cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"now": time.Now().UTC().Format(time.RFC3339),
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// seedManagedApplication mints one leaseapp with an applicant identity and a unit
// (address + listing aspects), plus a landlord identity that MANAGES the unit (the
// loftspace-domain `manages` link). The full well-formed shape the landlord read
// model projects.
func (f *lensFixture) seedManagedApplication(t *testing.T, appName, applicantName, unitName, landlordName string) {
	t.Helper()
	f.vtx(t, appName, "leaseapp")
	f.vtx(t, applicantName, "identity")
	f.vtx(t, unitName, "unit")
	f.vtx(t, landlordName, "identity")
	f.aspect(t, unitName, "address", "address", map[string]any{"line1": "1 Market St", "city": "San Francisco", "region": "CA"})
	f.aspect(t, unitName, "listing", "listing", map[string]any{"rentAmount": 4200, "rentCurrency": "USD", "status": "available"})
	f.aspect(t, appName, "terms", "terms", map[string]any{"moveInDate": "2026-08-01", "leaseTermMonths": 12, "requestedRent": 4100})
	f.aspect(t, appName, "signature", "signature", map[string]any{"signedAt": "2026-07-15T00:00:00Z"})
	f.aspect(t, appName, "decision", "decision", map[string]any{"value": "approved"})
	f.edge(t, "applicationFor", appName, applicantName)
	f.edge(t, "appliesToUnit", appName, unitName)
	// manages: landlord (source) -> unit (target), class "manages"
	// (lnk.identity.<landlordID>.manages.unit.<unitID>).
	f.edge(t, "manages", landlordName, unitName)
}

// TestLandlordLeaseApplicationsRead_ProjectsManagingLandlordAnchor — the protected
// landlord read model projects one row per managed application, carrying the
// display scalars and an authz_anchors set of exactly the MANAGING landlord's bare
// NanoID (§6.14). This is the grant RLS matches: the base cap-read self-anchor
// grants the landlord their own NanoID, so the row is readable by the managing
// landlord and nobody else.
func TestLandlordLeaseApplicationsRead_ProjectsManagingLandlordAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedManagedApplication(t, "app", "alice", "unit1", "larry")
	appKey := "vtx.leaseapp." + f.ids["app"]
	aliceKey := "vtx.identity." + f.ids["alice"]
	larryKey := "vtx.identity." + f.ids["larry"]
	unitKey := "vtx.unit." + f.ids["unit1"]

	rows := f.projectLandlordRead(t)
	require.Len(t, rows, 1, "exactly one landlord row per (application, managing landlord)")
	v := rows[0].Values

	require.Equal(t, f.ids["app"], v["app_id"], "app_id is the application's bare NanoID")
	require.Equal(t, f.ids["larry"], v["landlord_id"], "landlord_id is the managing landlord's bare NanoID")
	require.Equal(t, appKey, v["entity_key"])
	require.Equal(t, aliceKey, v["applicant"])
	require.Equal(t, larryKey, v["landlord_key"], "landlord_key keeps the full managing-landlord vertex key")
	require.Equal(t, unitKey, v["unit_key"])
	require.Equal(t, "1 Market St", v["unit_address"])
	require.Equal(t, "San Francisco", v["unit_city"])
	require.Equal(t, "CA", v["unit_region"])
	require.EqualValues(t, 4200, v["unit_rent"])
	require.Equal(t, "USD", v["unit_currency"])
	require.Equal(t, "available", v["unit_status"])
	require.Equal(t, "2026-07-15T00:00:00Z", v["signed_at"])
	require.Equal(t, "approved", v["landlord_decision"])
	require.Equal(t, "2026-08-01", v["terms_move_in_date"])
	require.EqualValues(t, 12, v["terms_lease_term_months"])
	require.EqualValues(t, 4100, v["terms_requested_rent"])

	// The headline: authz_anchors is exactly [larry's bare NanoID].
	require.Equal(t, []string{f.ids["larry"]}, anchorStrings(t, v["authz_anchors"]),
		"authz_anchors must carry exactly the managing landlord's bare NanoID (the §6.14 grant the base self-grant matches)")
}

// TestLandlordLeaseApplicationsRead_AnchorScopesPerLandlord — two applications on
// two units managed by two different landlords each anchor to ONLY their own
// managing landlord. The projection-layer proof that a landlord reads only their
// units' applications: RLS, matching each row's authz_anchors against the reading
// actor's granted anchors, returns Larry's row to Larry and Linda's to Linda.
func TestLandlordLeaseApplicationsRead_AnchorScopesPerLandlord(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedManagedApplication(t, "appA", "alice", "unitA", "larry")
	f.seedManagedApplication(t, "appB", "bob", "unitB", "linda")

	rows := f.projectLandlordRead(t)
	require.Len(t, rows, 2)
	byApp := map[string][]string{}
	for _, r := range rows {
		byApp[r.Values["app_id"].(string)] = anchorStrings(t, r.Values["authz_anchors"])
	}
	require.Equal(t, []string{f.ids["larry"]}, byApp[f.ids["appA"]], "unitA's application anchors only to its manager Larry")
	require.Equal(t, []string{f.ids["linda"]}, byApp[f.ids["appB"]], "unitB's application anchors only to its manager Linda")
	require.NotContains(t, byApp[f.ids["appA"]], f.ids["linda"], "Larry's row must NOT carry Linda's anchor")
	require.NotContains(t, byApp[f.ids["appB"]], f.ids["larry"], "Linda's row must NOT carry Larry's anchor")
}

// TestLandlordLeaseApplicationsRead_UnmanagedUnitProducesNoRow — a well-formed
// application whose unit has NO managing landlord projects NO landlord-row (the
// `manages` MATCH is REQUIRED). No landlord can read it, and the array adapter is
// never handed a null anchor — the strongest fail-closed posture. A managed
// application alongside it still projects normally, proving the required MATCH
// excludes only the unmanaged case.
func TestLandlordLeaseApplicationsRead_UnmanagedUnitProducesNoRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	// An application to an unmanaged unit: applicant + unit + links, but NO manages link.
	f.vtx(t, "orphanApp", "leaseapp")
	f.vtx(t, "carol", "identity")
	f.vtx(t, "unitOrphan", "unit")
	f.aspect(t, "unitOrphan", "listing", "listing", map[string]any{"rentAmount": 3000, "rentCurrency": "USD", "status": "available"})
	f.edge(t, "applicationFor", "orphanApp", "carol")
	f.edge(t, "appliesToUnit", "orphanApp", "unitOrphan")
	// A fully-managed application alongside it.
	f.seedManagedApplication(t, "app", "alice", "unit1", "larry")

	rows := f.projectLandlordRead(t)
	require.Len(t, rows, 1, "only the managed application projects; the unmanaged unit's application is excluded")
	require.Equal(t, f.ids["app"], rows[0].Values["app_id"])
	require.Equal(t, []string{f.ids["larry"]}, anchorStrings(t, rows[0].Values["authz_anchors"]))
}

// TestLandlordLeaseApplicationsRead_ProjectsProfileSignals — D1.5 Rec C: the
// applicant qualification-profile signals (income/employment/references/
// co-applicant/guarantor) project as informational scalars on the landlord row,
// scalar hops off app.profile.data.* with no aggregation. An application whose
// applicant never submitted a profile projects profile_submitted=false and every
// signal null (unknown, not false) — asserted on the sibling app in
// seedManagedApplication, which carries no .profile aspect.
func TestLandlordLeaseApplicationsRead_ProjectsProfileSignals(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedManagedApplication(t, "app", "alice", "unit1", "larry")
	f.aspect(t, "app", "profile", "profile", map[string]any{
		"annualIncome":             90000,
		"employmentStatus":         "employed",
		"references":               []any{"ref1", "ref2"},
		"hasCoApplicant":           false,
		"hasGuarantor":             true,
		"employmentVerified":       true,
		"referenceCount":           2,
		"submittedAt":              "2026-07-01T00:00:00Z",
		"incomeToRentMet":          true,
		"guarantorIncomeToRentMet": false,
	})
	f.seedManagedApplication(t, "appNoProfile", "bob", "unit2", "larry")

	rows := f.projectLandlordRead(t)
	byApp := map[string]map[string]any{}
	for _, r := range rows {
		byApp[r.Values["app_id"].(string)] = r.Values
	}

	withProfile := byApp[f.ids["app"]]
	require.Equal(t, true, withProfile["profile_submitted"])
	require.Equal(t, true, withProfile["income_to_rent_met"])
	require.Equal(t, true, withProfile["employment_verified"])
	require.EqualValues(t, 2, withProfile["reference_count"])
	require.Equal(t, false, withProfile["has_co_applicant"])
	require.Equal(t, true, withProfile["has_guarantor"])
	require.Equal(t, false, withProfile["guarantor_income_to_rent_met"])

	noProfile := byApp[f.ids["appNoProfile"]]
	require.Equal(t, false, noProfile["profile_submitted"], "no .profile aspect -> profile_submitted false, not null")
	require.Nil(t, noProfile["income_to_rent_met"], "no profile -> unknown, not false")
	require.Nil(t, noProfile["employment_verified"])
	require.Nil(t, noProfile["reference_count"])
	require.Nil(t, noProfile["has_co_applicant"])
	require.Nil(t, noProfile["has_guarantor"])
	require.Nil(t, noProfile["guarantor_income_to_rent_met"])
}

// TestLandlordLeaseApplicationsRead_CoManagedUnitFansToOneRowPerLandlord — a unit
// managed by TWO landlords fans the application out to ONE row PER landlord, each
// anchored to that one landlord, with a distinct composite (app_id, landlord_id)
// key (no collision). This is the WATCH on the multi-owner assumption: each
// co-manager reads the application via their own anchored row.
func TestLandlordLeaseApplicationsRead_CoManagedUnitFansToOneRowPerLandlord(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedManagedApplication(t, "app", "alice", "unit1", "larry")
	// A SECOND landlord co-manages the same unit.
	f.vtx(t, "linda", "identity")
	f.edge(t, "manages", "linda", "unit1")

	rows := f.projectLandlordRead(t)
	require.Len(t, rows, 2, "a co-managed unit fans the application out to one row per managing landlord")

	byLandlord := map[string]map[string]any{}
	for _, r := range rows {
		byLandlord[r.Values["landlord_id"].(string)] = r.Values
	}
	require.Contains(t, byLandlord, f.ids["larry"])
	require.Contains(t, byLandlord, f.ids["linda"])
	// Same application, each row anchored to exactly its one landlord.
	require.Equal(t, f.ids["app"], byLandlord[f.ids["larry"]]["app_id"])
	require.Equal(t, f.ids["app"], byLandlord[f.ids["linda"]]["app_id"])
	require.Equal(t, []string{f.ids["larry"]}, anchorStrings(t, byLandlord[f.ids["larry"]]["authz_anchors"]))
	require.Equal(t, []string{f.ids["linda"]}, anchorStrings(t, byLandlord[f.ids["linda"]]["authz_anchors"]))
}

// TestLandlordLeaseApplicationsRead_ProjectsContactEnvelopesWhole — the Secure-Lens
// contract at the engine layer (Contract #3 §3.10): applicant_name /
// applicant_email / applicant_phone RETURN the applicant's sensitive aspect
// envelope WHOLE (id.<aspect>.data — the {ct, nonce, keyId} map the Processor
// commits), never a plaintext hop, so the pipeline's SecureDecryptor is the only
// place plaintext appears. An applicant missing an aspect projects that column
// null while the ROW still projects — the contact columns are display enrichment,
// never a row gate (no WHERE on ciphertext presence, unlike applicantRosterRead).
func TestLandlordLeaseApplicationsRead_ProjectsContactEnvelopesWhole(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedManagedApplication(t, "app", "alice", "unit1", "larry")
	nameEnv := map[string]any{"ct": "b64-name-ct", "nonce": "b64-nonce-1", "keyId": "alice-key"}
	emailEnv := map[string]any{"ct": "b64-email-ct", "nonce": "b64-nonce-2", "keyId": "alice-key"}
	f.aspect(t, "alice", "name", "name", nameEnv)
	f.aspect(t, "alice", "email", "email", emailEnv)
	// No phone aspect: the column must project null, the row must survive.

	rows := f.projectLandlordRead(t)
	require.Len(t, rows, 1)
	v := rows[0].Values

	require.Equal(t, nameEnv, v["applicant_name"], "applicant_name carries the ciphertext envelope whole")
	require.Equal(t, emailEnv, v["applicant_email"], "applicant_email carries the ciphertext envelope whole")
	require.Nil(t, v["applicant_phone"], "a missing sensitive aspect projects null, not a dropped row")
}

// TestLandlordLeaseApplicationsRead_ContactlessApplicantStillProjects — an
// application whose applicant has NO sensitive contact aspects at all still
// projects its landlord row (all three contact columns null). Guards against a
// future WHERE that would silently drop applications from the landlord's
// decision surface.
func TestLandlordLeaseApplicationsRead_ContactlessApplicantStillProjects(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedManagedApplication(t, "app", "alice", "unit1", "larry")

	rows := f.projectLandlordRead(t)
	require.Len(t, rows, 1, "a contactless applicant's application still reaches the landlord surface")
	v := rows[0].Values
	require.Nil(t, v["applicant_name"])
	require.Nil(t, v["applicant_email"])
	require.Nil(t, v["applicant_phone"])
	require.Equal(t, []string{f.ids["larry"]}, anchorStrings(t, v["authz_anchors"]))
}

// TestLandlordLeaseApplicationsRead_ProjectsQualified — D1.5 Rec-C remainder
// (decision-surface-design.md §4 Option A): `qualified` is the SAME readiness
// formula leaseApplicationCompleteSpec derives as `applicantApproved` — ssn
// on file, a fresh completed background check, a completed payment, and a
// signed lease — now cloned onto the RLS-protected landlord lens via the
// readinessOptionalMatch/readinessWithItems shared cypher fragment. This also
// pins the WITH-clause refactor: the three map-valued Secure columns
// (applicant_name/email/phone) still project their ciphertext envelope whole
// after being carried through the new WITH as passthrough aliases.
func TestLandlordLeaseApplicationsRead_ProjectsQualified(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedManagedApplication(t, "app", "alice", "unit1", "larry")
	f.aspect(t, "alice", "ssn", "ssn", map[string]any{"value": "123456789"})
	nameEnv := map[string]any{"ct": "b64-name-ct", "nonce": "b64-nonce-1", "keyId": "alice-key"}
	f.aspect(t, "alice", "name", "name", nameEnv)
	f.vtxWithClass(t, "bg1", "service", "service.backgroundCheck.instance")
	f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-01T00:00:00Z", "validUntil": farFutureValidUntil})
	f.vtxWithClass(t, "pay1", "service", "service.payment.instance")
	f.aspect(t, "pay1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-02T00:00:00Z"})
	f.edge(t, "providedTo", "bg1", "alice")
	f.edge(t, "providedTo", "pay1", "alice")
	// A second, unqualified application on the same landlord's portfolio: no
	// ssn, no service instances at all — every readiness signal absent.
	f.seedManagedApplication(t, "appUnqualified", "bob", "unit2", "larry")

	rows := f.projectLandlordRead(t)
	byApp := map[string]map[string]any{}
	for _, r := range rows {
		byApp[r.Values["app_id"].(string)] = r.Values
	}

	qualified := byApp[f.ids["app"]]
	require.Equal(t, true, qualified["qualified"], "ssn + fresh bgcheck + payment + signature -> qualified")
	require.Equal(t, nameEnv, qualified["applicant_name"], "the WITH refactor must not disturb the Secure-Lens envelope passthrough")

	unqualified := byApp[f.ids["appUnqualified"]]
	require.Equal(t, false, unqualified["qualified"], "no ssn, no service instances -> not qualified")
}

// TestLandlordLeaseApplicationsRead_QualifiedRequiresFreshBgcheck — a STALE
// completed background check (validUntil in the past) does not count toward
// qualified, mirroring leaseApplicationCompleteSpec's missing_bgcheck freshness
// predicate (the SAME `inst.outcome.data.validUntil > $now` term, shared via
// readinessWithItems).
func TestLandlordLeaseApplicationsRead_QualifiedRequiresFreshBgcheck(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedManagedApplication(t, "app", "alice", "unit1", "larry")
	f.aspect(t, "alice", "ssn", "ssn", map[string]any{"value": "123456789"})
	f.vtxWithClass(t, "bg1", "service", "service.backgroundCheck.instance")
	f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2020-01-01T00:00:00Z", "validUntil": "2020-06-01T00:00:00Z"})
	f.vtxWithClass(t, "pay1", "service", "service.payment.instance")
	f.aspect(t, "pay1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-02T00:00:00Z"})
	f.edge(t, "providedTo", "bg1", "alice")
	f.edge(t, "providedTo", "pay1", "alice")

	rows := f.projectLandlordRead(t)
	require.Len(t, rows, 1)
	require.Equal(t, false, rows[0].Values["qualified"], "a stale bgcheck must not count toward qualified")
}

// TestLandlordLeaseApplicationsRead_QualifiedWithRealVaultCiphertext seeds
// .ssn with the ACTUAL shape the Processor's step 6.5 commits for a
// sensitive aspect — a vault.Ciphertext envelope ({ct, nonce, keyId}), not
// every other test's fixture-only plaintext {value: "..."}. readinessWithItems
// used to read id.ssn.data.value, which is always null on a real ciphertext
// envelope (it carries no "value" key) — so a real, correctly-encrypted ssn
// could never satisfy `ssnVal <> null`, and qualified/applicantApproved
// could never turn true for any real (non-test) applicant. This pins the
// fix (id.ssn.data — the whole aspect body — is the presence test) against
// the shape that actually ships.
func TestLandlordLeaseApplicationsRead_QualifiedWithRealVaultCiphertext(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedManagedApplication(t, "app", "alice", "unit1", "larry")

	v, err := vault.NewLocalBackend([]byte("0123456789abcdef0123456789abcdef"), "test-v1")
	require.NoError(t, err)
	aliceKey := "vtx.identity." + f.ids["alice"]
	env, err := v.CreateIdentityKey(context.Background(), aliceKey)
	require.NoError(t, err)
	plaintext, err := json.Marshal(map[string]any{"value": "123456789"})
	require.NoError(t, err)
	ct, err := v.Encrypt(context.Background(), aliceKey, env, plaintext)
	require.NoError(t, err)
	ctBytes, err := json.Marshal(ct)
	require.NoError(t, err)
	var ciphertextEnvelope map[string]any
	require.NoError(t, json.Unmarshal(ctBytes, &ciphertextEnvelope))
	require.NotContains(t, ciphertextEnvelope, "value", "sanity: the real committed shape carries no plaintext value key")

	f.aspect(t, "alice", "ssn", "ssn", ciphertextEnvelope)
	f.vtxWithClass(t, "bg1", "service", "service.backgroundCheck.instance")
	f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-01T00:00:00Z", "validUntil": farFutureValidUntil})
	f.vtxWithClass(t, "pay1", "service", "service.payment.instance")
	f.aspect(t, "pay1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-02T00:00:00Z"})
	f.edge(t, "providedTo", "bg1", "alice")
	f.edge(t, "providedTo", "pay1", "alice")

	rows := f.projectLandlordRead(t)
	require.Len(t, rows, 1)
	require.Equal(t, true, rows[0].Values["qualified"], "a real Vault-ciphertext ssn + fresh bgcheck + payment must satisfy qualified")
}

// TestLandlordLeaseApplicationsRead_ShredMakesContactUndecryptable closes the
// vault-crypto-shredding-design.md Fire 5b close gate: a ShredIdentityKey run
// against the applicant's Vault key must make Vault.Decrypt fail on the EXACT
// ciphertext envelope this lens projects as applicant_name — proving the
// right-to-erasure guarantee for landlordLeaseApplicationsRead's own secure
// columns specifically, not just the readiness-formula presence check
// 5b-ii-d fixed (which only proved id.ssn.data resolves non-null, never
// touched decrypt). The lens itself never decrypts (SecureColumns carry the
// envelope whole to the Postgres adapter, §6.14) — the row survives the
// shred with its envelope intact; the guarantee is that the envelope is now
// permanently useless, proven by attempting the real Vault.Decrypt call a
// downstream Reveal RPC would make.
func TestLandlordLeaseApplicationsRead_ShredMakesContactUndecryptable(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedManagedApplication(t, "app", "alice", "unit1", "larry")

	v, err := vault.NewLocalBackend([]byte("0123456789abcdef0123456789abcdef"), "test-v1")
	require.NoError(t, err)
	aliceKey := "vtx.identity." + f.ids["alice"]
	env, err := v.CreateIdentityKey(context.Background(), aliceKey)
	require.NoError(t, err)

	nameCT, err := v.Encrypt(context.Background(), aliceKey, env, []byte(`"Alice Applicant"`))
	require.NoError(t, err)
	nameCTBytes, err := json.Marshal(nameCT)
	require.NoError(t, err)
	var nameEnvelope map[string]any
	require.NoError(t, json.Unmarshal(nameCTBytes, &nameEnvelope))
	f.aspect(t, "alice", "name", "name", nameEnvelope)

	rows := f.projectLandlordRead(t)
	require.Len(t, rows, 1)
	projected, ok := rows[0].Values["applicant_name"].(map[string]any)
	require.True(t, ok, "applicant_name must project the ciphertext envelope whole")
	require.Equal(t, nameEnvelope, projected, "the lens must project the SAME envelope committed to Vault")

	projectedBytes, err := json.Marshal(projected)
	require.NoError(t, err)
	var roundTripCT vault.Ciphertext
	require.NoError(t, json.Unmarshal(projectedBytes, &roundTripCT))

	// Sanity: decrypts fine pre-shred, using the exact envelope this lens projects.
	plaintext, err := v.Decrypt(context.Background(), aliceKey, env, roundTripCT)
	require.NoError(t, err)
	require.Equal(t, `"Alice Applicant"`, string(plaintext))

	require.NoError(t, v.ShredKey(context.Background(), aliceKey))
	_, err = v.Decrypt(context.Background(), aliceKey, env, roundTripCT)
	require.Error(t, err, "Vault.Decrypt must fail post-shred for landlordLeaseApplicationsRead's own committed ciphertext")

	// The row survives (this lens has no keyshredded nullification listener —
	// that's the nats_kv-target mechanism internal/cryptoshred's e2e proves;
	// a Postgres protected lens's Phase-A guarantee is key destruction, not
	// row removal): applicant_name still projects the same now-undecryptable
	// envelope.
	rowsAfterShred := f.projectLandlordRead(t)
	require.Len(t, rowsAfterShred, 1)
	require.Equal(t, nameEnvelope, rowsAfterShred[0].Values["applicant_name"], "the row survives with its envelope intact; only decrypt is destroyed")
}
