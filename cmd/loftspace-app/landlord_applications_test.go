package main

import (
	"net/http"
	"testing"
)

// No-Postgres unit coverage for the landlord protected reader: the fail-closed
// auth/pool paths and the pure grouping + flag-derivation logic. The RLS
// enforcement itself is the gated POSTGRES_TEST_DSN proof in
// landlord_applications_rls_test.go.

func TestHandleLandlordApplications_NoAuthPosture_401(t *testing.T) {
	s := noPostureServer(t)
	rec := sessionGET(s, s.handleLandlordApplications, "/api/landlord/applications", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandleLandlordApplications_NoCookie_401(t *testing.T) {
	s, _ := devSessionServer(t, nil)
	rec := sessionGET(s, s.handleLandlordApplications, "/api/landlord/applications", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no session cookie)", rec.Code)
	}
}

func TestHandleLandlordApplications_ForgedCookie_401(t *testing.T) {
	s, _ := devSessionServer(t, nil)
	forged := &http.Cookie{Name: s.session.CookieName(), Value: "not.a.valid.jwt"}
	rec := sessionGET(s, s.handleLandlordApplications, "/api/landlord/applications", forged)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (forged cookie)", rec.Code)
	}
}

// A signed-in actor with no read-model pool gets a clean 502, never a
// nil-pointer panic (mirrors the applicant reader).
func TestHandleLandlordApplications_ValidSession_PoolUnconfigured_502(t *testing.T) {
	s, cookieFor := devSessionServer(t, nil) // session set, pgPool nil
	rec := sessionGET(s, s.handleLandlordApplications, "/api/landlord/applications", cookieFor("Hj4kPmRtw9nbCxz5vQ2y"))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (pool unconfigured)", rec.Code)
	}
}

func TestDeriveLandlordRowFlags(t *testing.T) {
	approved := "approved"
	declined := "declined"
	cases := []struct {
		name         string
		decision     *string
		wantApproved bool
		wantDeclined bool
		wantAlias    bool
	}{
		{"nil", nil, false, false, false},
		{"approved", &approved, true, false, false},
		{"declined", &declined, false, true, true},
		// A non-nil value that matches no case must stay fully neutral (a future
		// fall-through default that set a flag would be caught here).
		{"pending", strPtr("pending"), false, false, false},
		{"empty", strPtr(""), false, false, false},
		{"garbage", strPtr("APPROVED"), false, false, false}, // case-sensitive; no match
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			row := protectedLandlordRow{LandlordDecision: c.decision}
			deriveLandlordRowFlags(&row)
			if row.LandlordApproved != c.wantApproved || row.LandlordDeclined != c.wantDeclined || row.Declined != c.wantAlias {
				t.Errorf("flags = approved:%v declined:%v alias:%v", row.LandlordApproved, row.LandlordDeclined, row.Declined)
			}
		})
	}
}

func TestGroupLandlordRowsByUnit(t *testing.T) {
	uL := "vtx.unit.unit-L"
	uCO := "vtx.unit.unit-CO"
	addrL := "1 Market St"
	leased := "leased"
	available := "available"
	var rent float64 = 4200

	emptyUK := ""

	// unit-L: its FIRST row carries NO facets (all nil); a LATER row has them — the
	// defensive fill must still populate the card. unit-CO: the co-managed row. Plus
	// a nil-unit_key orphan AND an empty-string-unit_key row, both of which skip.
	rows := []protectedLandlordRow{
		{EntityKey: "vtx.leaseapp.app-L1", UnitKey: &uL}, // first row, facets nil
		{EntityKey: "vtx.leaseapp.app-L2", UnitKey: &uL, UnitAddress: &addrL, UnitStatus: &available, UnitRent: &rent},
		{EntityKey: "vtx.leaseapp.app-CO", UnitKey: &uCO, UnitStatus: &leased},
		{EntityKey: "vtx.leaseapp.orphan"},                  // nil unit_key → skipped
		{EntityKey: "vtx.leaseapp.blank", UnitKey: &emptyUK}, // empty-string unit_key → skipped
	}

	groups := groupLandlordRowsByUnit(rows)
	if len(groups) != 2 {
		t.Fatalf("want 2 unit groups (nil + empty-string unit_key skipped), got %d: %+v", len(groups), groups)
	}
	// Stable sort by unitKey: unit-CO < unit-L.
	if groups[0].UnitKey != uCO || groups[1].UnitKey != uL {
		t.Fatalf("groups not sorted by unitKey: %s, %s", groups[0].UnitKey, groups[1].UnitKey)
	}
	if len(groups[1].Applications) != 2 {
		t.Errorf("unit-L should carry 2 applications, got %d", len(groups[1].Applications))
	}
	// The defensive fill: unit-L's facets came from its SECOND row (the first was nil).
	if groups[1].UnitAddress != addrL || groups[1].UnitRent == nil || *groups[1].UnitRent != rent || groups[1].UnitStatus != available {
		t.Errorf("unit-L facets not filled from a later row (first-row-nil case): %+v", groups[1])
	}
	if groups[0].UnitStatus != leased {
		t.Errorf("unit-CO status = %q, want leased", groups[0].UnitStatus)
	}
}

// strPtr returns a pointer to a string literal for the nullable-column fixtures.
func strPtr(s string) *string { return &s }

func TestGroupLandlordRowsByUnit_Empty(t *testing.T) {
	if got := groupLandlordRowsByUnit(nil); len(got) != 0 {
		t.Fatalf("nil rows must produce no groups, got %+v", got)
	}
}
