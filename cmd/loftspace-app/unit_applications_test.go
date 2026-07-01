package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func f64(v float64) *float64 { return &v }

// TestGroupByUnit_GroupsAndJoins exercises the landlord by-unit assembler: it
// groups live applications under their unit, joins each applicant's human name
// from the roster, derives the coarse disposition, surfaces a listed unit that
// has no applications yet, and surfaces a unit that has applications but no
// (longer a) listing.
func TestGroupByUnit_GroupsAndJoins(t *testing.T) {
	apps := []applicationRow{
		// u1 — alice: qualified, landlord-approved, and the unit leased → the terminal
		// "leased" disposition (signed; all applicant gaps closed).
		{EntityKey: "vtx.leaseapp.a2", Applicant: "vtx.identity.alice", ApplicantApproved: true,
			LandlordDecision: "approved", LandlordApproved: true,
			UnitKey: "vtx.unit.u1", UnitAddress: "1 Market St", UnitRent: f64(2400), UnitStatus: "leased"},
		// u1 — bob: a standing decline.
		{EntityKey: "vtx.leaseapp.a1", Applicant: "vtx.identity.bob", Declined: true, DeclinedBgcheck: true,
			MissingSignature: true, UnitKey: "vtx.unit.u1", UnitAddress: "1 Market St", UnitRent: f64(2400), UnitStatus: "leased"},
		// u2 — carol: still converging, not signed.
		{EntityKey: "vtx.leaseapp.a3", Applicant: "vtx.identity.carol", MissingBgcheck: true, MissingSignature: true,
			Violating: true, UnitKey: "vtx.unit.u2", UnitRent: f64(1800), UnitStatus: "available"},
		// u2 — dave: QUALIFIED but the landlord has NOT decided → the "qualified"
		// disposition (the Approve/Decline action state the landlord acts on).
		{EntityKey: "vtx.leaseapp.a5", Applicant: "vtx.identity.dave", ApplicantApproved: true,
			MissingDecision: true, Violating: true,
			UnitKey: "vtx.unit.u2", UnitRent: f64(1800), UnitStatus: "available"},
		// u3 — a unit with applications but NO listing (lost its listing): the
		// row's own unit facets must surface it.
		{EntityKey: "vtx.leaseapp.a4", Applicant: "vtx.identity.alice", MissingSignature: true,
			UnitKey: "vtx.unit.u3", UnitAddress: "9 Lost Ln", UnitRent: f64(999), UnitStatus: "pending"},
		// a malformed/bare row with no unitKey — must be skipped, never a "" unit.
		{EntityKey: "vtx.leaseapp.bad", Applicant: "vtx.identity.bob"},
	}
	identities := []identityView{
		{Key: "vtx.identity.alice", Name: "Alice Renter"},
		{Key: "vtx.identity.bob", Name: "Bob Tenant"},
		{Key: "vtx.identity.dave", Name: "Dave Applicant"},
		// carol is intentionally absent from the roster → her name resolves empty.
	}
	listings := []listingProjection{
		{UnitKey: "vtx.unit.u1", Status: "leased", RentAmount: f64(2400), AddrLine1: "1 Market St"},
		{UnitKey: "vtx.unit.u2", Status: "available", RentAmount: f64(1800), AddrLine1: "2 Mission St"},
		// u4 — a listed unit with ZERO applications: it must still appear.
		{UnitKey: "vtx.unit.u4", Status: "available", RentAmount: f64(3000), AddrLine1: "4 Folsom St"},
	}

	units := groupByUnit(apps, identities, listings)

	// units sort by unitKey: u1, u2, u3, u4 — no "" unit from the bad row.
	if len(units) != 4 {
		t.Fatalf("want 4 units (u1..u4, no empty), got %d: %+v", len(units), units)
	}
	got := map[string]unitApplicationsRow{}
	for _, u := range units {
		got[u.UnitKey] = u
	}
	if _, ok := got[""]; ok {
		t.Fatalf("a unitless application must not create an empty unit row")
	}
	if units[0].UnitKey != "vtx.unit.u1" || units[3].UnitKey != "vtx.unit.u4" {
		t.Errorf("units must sort by unitKey, got %q..%q", units[0].UnitKey, units[3].UnitKey)
	}

	// u1: two applications, sorted by leaseAppKey (a1 before a2).
	u1 := got["vtx.unit.u1"]
	if u1.ApplicationCount != 2 || len(u1.Applications) != 2 {
		t.Fatalf("u1 want 2 applications, got %d", u1.ApplicationCount)
	}
	if u1.Applications[0].LeaseAppKey != "vtx.leaseapp.a1" || u1.Applications[1].LeaseAppKey != "vtx.leaseapp.a2" {
		t.Errorf("u1 applications must sort by leaseAppKey, got %q, %q",
			u1.Applications[0].LeaseAppKey, u1.Applications[1].LeaseAppKey)
	}
	// bob (a1) declined, not signed; alice (a2) approved, signed (no missing_signature).
	bob := u1.Applications[0]
	if bob.ApplicantName != "Bob Tenant" || bob.Status != "declined" || !bob.Declined || bob.Signed {
		t.Errorf("u1 bob: want Bob Tenant/declined/!signed, got %+v", bob)
	}
	alice := u1.Applications[1]
	if alice.ApplicantName != "Alice Renter" || alice.Status != "leased" || !alice.Approved || !alice.Signed || !alice.LandlordApproved {
		t.Errorf("u1 alice: want Alice Renter/leased/signed/landlordApproved, got %+v", alice)
	}
	if u1.UnitRent == nil || *u1.UnitRent != 2400 || u1.UnitAddress != "1 Market St" || u1.UnitStatus != "leased" {
		t.Errorf("u1 facets: want 1 Market St/2400/leased, got addr=%q rent=%v status=%q",
			u1.UnitAddress, u1.UnitRent, u1.UnitStatus)
	}

	// u2: carol in review + dave qualified-awaiting-decision (sorted a3 before a5).
	u2 := got["vtx.unit.u2"]
	if u2.ApplicationCount != 2 || len(u2.Applications) != 2 {
		t.Fatalf("u2 want 2 applications (carol + dave), got %d: %+v", u2.ApplicationCount, u2.Applications)
	}
	carol := u2.Applications[0]
	if carol.LeaseAppKey != "vtx.leaseapp.a3" || carol.Status != "in_review" {
		t.Errorf("u2 carol: want a3/in_review, got %+v", carol)
	}
	if carol.ApplicantName != "" {
		t.Errorf("u2 carol (off-roster): name should resolve empty, got %q", carol.ApplicantName)
	}
	dave := u2.Applications[1]
	if dave.LeaseAppKey != "vtx.leaseapp.a5" || dave.Status != "qualified" || !dave.Qualified || dave.LandlordApproved || dave.LandlordDeclined {
		t.Errorf("u2 dave: want a5/qualified (awaiting landlord decision), got %+v", dave)
	}
	// the listing seeds u2's address even though the convergence rows carry none.
	if u2.UnitAddress != "2 Mission St" {
		t.Errorf("u2 address should seed from the listing, got %q", u2.UnitAddress)
	}

	// u3: appears purely from an application (no listing), facets from the row.
	u3 := got["vtx.unit.u3"]
	if u3.ApplicationCount != 1 || u3.UnitAddress != "9 Lost Ln" || u3.UnitStatus != "pending" {
		t.Errorf("u3 (listing-less) want 1 app + row facets, got %+v", u3)
	}

	// u4: a listed unit with zero applications still appears, count 0.
	u4 := got["vtx.unit.u4"]
	if u4.ApplicationCount != 0 || len(u4.Applications) != 0 {
		t.Errorf("u4 (no applicants) want 0 applications, got %d", u4.ApplicationCount)
	}
	if u4.Applications == nil {
		t.Errorf("u4 Applications must be a non-nil empty slice (renders as [])")
	}
	if u4.UnitRent == nil || *u4.UnitRent != 3000 || u4.UnitStatus != "available" {
		t.Errorf("u4 facets must seed from the listing, got rent=%v status=%q", u4.UnitRent, u4.UnitStatus)
	}
}

// TestGroupByUnit_Empty returns an empty (non-nil) slice when nothing is
// projected, so the handler renders {"units":[],"count":0}.
func TestGroupByUnit_Empty(t *testing.T) {
	units := groupByUnit(nil, nil, nil)
	if units == nil || len(units) != 0 {
		t.Fatalf("want empty non-nil slice, got %+v", units)
	}
}

func bptr(v bool) *bool { return &v }
func iptr(v int) *int   { return &v }

// TestGroupByUnit_CarriesListingForEdit proves the landlord row carries the full
// nested listing/address (the Edit-form pre-fill source) and that a withdrawn
// (off-market) unit still appears in the landlord view so the landlord can relist
// it — the applicant Browse hides withdrawn, the landlord surface does not.
func TestGroupByUnit_CarriesListingForEdit(t *testing.T) {
	listings := []listingProjection{
		{UnitKey: "vtx.unit.u1", Status: "withdrawn", RentAmount: f64(2200), RentCurrency: "USD",
			Bedrooms: f64(2), AvailableFrom: "2026-09-01T00:00:00Z", LeaseTermMonths: f64(12),
			AddrLine1: "5 Pine St", AddrCity: "Portland", AddrRegion: "OR", AddrPostal: "97201"},
	}
	units := groupByUnit(nil, nil, listings)
	if len(units) != 1 {
		t.Fatalf("a withdrawn listed unit must still appear in the landlord view; got %d units", len(units))
	}
	u := units[0]
	if u.UnitStatus != "withdrawn" {
		t.Errorf("want status withdrawn, got %q", u.UnitStatus)
	}
	if u.Listing == nil || u.Address == nil {
		t.Fatalf("Edit pre-fill needs the full listing+address; got listing=%s address=%s", u.Listing, u.Address)
	}
	var L, A map[string]any
	if err := json.Unmarshal(u.Listing, &L); err != nil {
		t.Fatalf("listing decode: %v", err)
	}
	if L["rentAmount"].(float64) != 2200 || L["leaseTermMonths"].(float64) != 12 || L["status"] != "withdrawn" {
		t.Errorf("listing pre-fill fields: got %v", L)
	}
	if err := json.Unmarshal(u.Address, &A); err != nil {
		t.Fatalf("address decode: %v", err)
	}
	if A["postal"] != "97201" || A["city"] != "Portland" {
		t.Errorf("address pre-fill fields: got %v", A)
	}
}

// TestGroupByUnit_CarriesQualificationProfile proves the derived qualification
// signals (never the raw financials) flow from the convergence row to the landlord
// applicantSummary, and that an application with no profile leaves them null /
// profileSubmitted=false (the FE renders "no profile yet" rather than a false 0).
func TestGroupByUnit_CarriesQualificationProfile(t *testing.T) {
	apps := []applicationRow{
		// alice: profile submitted, income meets 3x, employed, 2 refs, guarantor.
		{EntityKey: "vtx.leaseapp.a1", Applicant: "vtx.identity.alice", ApplicantApproved: true,
			MissingDecision: true, Violating: true, UnitKey: "vtx.unit.u1", UnitStatus: "available",
			ProfileSubmitted: true, IncomeToRentMet: bptr(true), EmploymentVerified: bptr(true),
			ReferenceCount: iptr(2), HasCoApplicant: bptr(false), HasGuarantor: bptr(true),
			GuarantorIncomeToRentMet: bptr(true)},
		// bob: no profile yet → null signals, profileSubmitted false.
		{EntityKey: "vtx.leaseapp.a2", Applicant: "vtx.identity.bob", MissingSignature: true,
			UnitKey: "vtx.unit.u1", UnitStatus: "available"},
	}
	listings := []listingProjection{{UnitKey: "vtx.unit.u1", Status: "available", RentAmount: f64(2000), AddrLine1: "1 Market St"}}
	units := groupByUnit(apps, nil, listings)
	if len(units) != 1 {
		t.Fatalf("want 1 unit, got %d", len(units))
	}
	byKey := map[string]applicantSummary{}
	for _, a := range units[0].Applications {
		byKey[a.LeaseAppKey] = a
	}
	alice := byKey["vtx.leaseapp.a1"]
	if !alice.ProfileSubmitted || alice.IncomeToRentMet == nil || !*alice.IncomeToRentMet {
		t.Errorf("alice income signal must carry through, got %+v", alice)
	}
	if alice.ReferenceCount == nil || *alice.ReferenceCount != 2 || alice.HasGuarantor == nil || !*alice.HasGuarantor {
		t.Errorf("alice refs/guarantor must carry through, got %+v", alice)
	}
	if alice.GuarantorIncomeToRentMet == nil || !*alice.GuarantorIncomeToRentMet {
		t.Errorf("alice guarantor income signal must carry through, got %+v", alice)
	}
	bob := byKey["vtx.leaseapp.a2"]
	if bob.ProfileSubmitted || bob.IncomeToRentMet != nil || bob.ReferenceCount != nil || bob.GuarantorIncomeToRentMet != nil {
		t.Errorf("bob has no profile → null signals, got %+v", bob)
	}
}

// TestDecodeListingProjections_SkipsBadRows decodes the availableListings bucket
// into flat projections, skipping unreadable keys and tombstoned (no-unitKey) rows.
func TestDecodeListingProjections_SkipsBadRows(t *testing.T) {
	entries := map[string]string{
		"vtx.unit.ok":   `{"unitKey":"vtx.unit.ok","status":"available","rentAmount":2200,"addrLine1":"5 Howard St"}`,
		"vtx.unit.gone": `{}`, // tombstoned projection — no unitKey, skipped
	}
	got := decodeListingProjections(keysOf(entries), fakeKV(entries))
	if len(got) != 1 || got[0].UnitKey != "vtx.unit.ok" {
		t.Fatalf("want only the decodable unitKey'd row, got %+v", got)
	}
	if got[0].RentAmount == nil || *got[0].RentAmount != 2200 || got[0].AddrLine1 != "5 Howard St" {
		t.Errorf("flat facets must decode, got %+v", got[0])
	}
}

// TestFilterUnitsToManaged proves the D1.5 scoping filter: only rows whose
// UnitKey is in the RLS-authoritative managed set survive, order preserved,
// and an empty/nil managed set drops everything rather than defaulting open.
func TestFilterUnitsToManaged(t *testing.T) {
	rows := []unitApplicationsRow{
		{UnitKey: "vtx.unit.u1", Applications: []applicantSummary{}},
		{UnitKey: "vtx.unit.u2", Applications: []applicantSummary{}},
		{UnitKey: "vtx.unit.u3", Applications: []applicantSummary{}},
	}
	got := filterUnitsToManaged(rows, map[string]bool{"vtx.unit.u1": true, "vtx.unit.u3": true})
	if len(got) != 2 || got[0].UnitKey != "vtx.unit.u1" || got[1].UnitKey != "vtx.unit.u3" {
		t.Fatalf("want [u1, u3] in order, got %+v", got)
	}

	if got := filterUnitsToManaged(rows, nil); len(got) != 0 {
		t.Fatalf("a nil managed set must drop everything (fail closed), got %+v", got)
	}
	if got := filterUnitsToManaged(nil, map[string]bool{"vtx.unit.u1": true}); got == nil || len(got) != 0 {
		t.Fatalf("want a non-nil empty slice for no input rows, got %+v", got)
	}
}

// D1.5: handleUnitApplications is now an AUTHENTICATED, RLS-scoped read (it used
// to serve every landlord's units + every applicant's PII with no auth at all).
// These mirror the handleApplications/handleLandlordApplications auth-gate
// proofs in readauth_test.go. The scoping itself reuses queryLandlordApplications
// verbatim, so its RLS enforcement is already proven by
// landlord_applications_rls_test.go; TestFilterUnitsToManaged above covers the
// new composition (managed-set → response filter) at the unit level.

func TestHandleUnitApplications_NoAuthConfigured_401(t *testing.T) {
	s := &server{logger: discardLogger(), natsTimeout: testTimeout} // authn nil
	rec := httptest.NewRecorder()
	s.handleUnitApplications(rec, httptest.NewRequest(http.MethodGet, "/api/unit-applications", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandleUnitApplications_NoToken_401(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	s.handleUnitApplications(rec, httptest.NewRequest(http.MethodGet, "/api/unit-applications", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no bearer)", rec.Code)
	}
}

func TestHandleUnitApplications_ForgedToken_401(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/unit-applications", nil)
	r.Header.Set("Authorization", "Bearer not.a.valid.jwt")
	s.handleUnitApplications(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (forged token)", rec.Code)
	}
}

// TestHandleUnitApplications_ValidToken_PoolUnconfigured_502: a verified actor
// with no read-model pool gets a clean 502 (the RLS-scoping source is
// unavailable), never a nil-pointer panic and never a default-open fall
// through to the unscoped read.
func TestHandleUnitApplications_ValidToken_PoolUnconfigured_502(t *testing.T) {
	s := devAuthServer(t) // authn set, pgPool nil
	tok, _, err := s.devSigner.mint("Hj4kPmRtw9nbCxz5vQ2y")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/unit-applications", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	s.handleUnitApplications(rec, r)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (pool unconfigured)", rec.Code)
	}
}
