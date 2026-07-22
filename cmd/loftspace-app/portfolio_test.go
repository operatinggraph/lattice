package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	cafedomain "github.com/operatinggraph/lattice/packages/cafe-domain"
)

// No-Postgres unit coverage for the portfolio-pulse reader: the fail-closed
// auth/pool paths and the pure aggregation logic, mirroring
// landlord_applications_test.go.

func TestHandlePortfolioPulse_NoAuthConfigured_401(t *testing.T) {
	s := &server{logger: discardLogger(), natsTimeout: testTimeout} // authn nil
	rec := httptest.NewRecorder()
	s.handlePortfolioPulse(rec, httptest.NewRequest(http.MethodGet, "/api/portfolio-pulse", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandlePortfolioPulse_NoToken_401(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	s.handlePortfolioPulse(rec, httptest.NewRequest(http.MethodGet, "/api/portfolio-pulse", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no bearer)", rec.Code)
	}
}

func TestHandlePortfolioPulse_ForgedToken_401(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/portfolio-pulse", nil)
	r.Header.Set("Authorization", "Bearer not.a.valid.jwt")
	s.handlePortfolioPulse(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (forged token)", rec.Code)
	}
}

// A verified actor with no read-model pool gets a clean 502, never a
// nil-pointer panic (mirrors the landlord-applications reader).
func TestHandlePortfolioPulse_ValidToken_PoolUnconfigured_502(t *testing.T) {
	s := devAuthServer(t) // authn set, pgPool nil
	tok, _, err := s.devSigner.mint("Hj4kPmRtw9nbCxz5vQ2y")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/portfolio-pulse", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	s.handlePortfolioPulse(rec, r)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (pool unconfigured)", rec.Code)
	}
}

func TestSummarizePortfolioPulse(t *testing.T) {
	rent1500 := 1500.0
	units := []portfolioPulseUnit{
		{UnitKey: "vtx.unit.a", UnitStatus: "leased", UnitRent: &rent1500},
		{UnitKey: "vtx.unit.b", UnitStatus: "leased"},
		{UnitKey: "vtx.unit.c", UnitStatus: "available"},
		{UnitKey: "vtx.unit.d", UnitStatus: "pending"},
		{UnitKey: "vtx.unit.e", UnitStatus: "withdrawn"},
		{UnitKey: "vtx.unit.f", UnitStatus: ""}, // never listed
	}
	got := summarizePortfolioPulse(units)
	if got.TotalUnits != 6 || got.Leased != 2 || got.Available != 1 || got.Pending != 1 || got.Withdrawn != 1 || got.NotListed != 1 {
		t.Fatalf("unexpected breakdown: %+v", got)
	}
	if want := 2.0 / 6.0; got.OccupancyRate != want {
		t.Fatalf("occupancyRate = %v, want %v", got.OccupancyRate, want)
	}
}

func TestSummarizePortfolioPulse_NoUnits_ZeroRateNoDivideByZero(t *testing.T) {
	got := summarizePortfolioPulse(nil)
	if got.TotalUnits != 0 || got.OccupancyRate != 0 {
		t.Fatalf("empty portfolio should be all-zero, got %+v", got)
	}
}

func strp(s string) *string { return &s }

func TestOccupiedLeaseAppKeys(t *testing.T) {
	rows := []protectedLandlordRow{
		{EntityKey: "vtx.leaseapp.a", SignedAt: strp("2026-07-01T00:00:00Z")},
		{EntityKey: "vtx.leaseapp.b", SignedAt: nil},        // never signed
		{EntityKey: "vtx.leaseapp.c", SignedAt: strp("")},   // signed_at present but empty
		{EntityKey: "vtx.leaseapp.d", SignedAt: strp("2026-07-05T00:00:00Z")},
	}
	got := occupiedLeaseAppKeys(rows)
	want := []string{"vtx.leaseapp.a", "vtx.leaseapp.d"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("occupiedLeaseAppKeys = %v, want %v", got, want)
	}
}

func TestComputeServiceAttachRate(t *testing.T) {
	occupied := []string{"vtx.leaseapp.a", "vtx.leaseapp.b", "vtx.leaseapp.c"}

	bookings := map[string][]byte{
		"b1": mustMarshal(serviceBookingRow{LeaseAppKey: "vtx.leaseapp.a"}),
		// belongs to a landlord/lease NOT in this landlord's occupied set —
		// must not leak into the count.
		"b2": mustMarshal(serviceBookingRow{LeaseAppKey: "vtx.leaseapp.other-landlord"}),
	}
	tabs := map[string][]byte{
		cafedomainPrefixKey("t1"): mustMarshal(serviceTabRow{LeaseAppKey: "vtx.leaseapp.b", Status: "open"}),
		cafedomainPrefixKey("t2"): mustMarshal(serviceTabRow{LeaseAppKey: "vtx.leaseapp.c", Status: "settled"}),
		"not-a-tab-key":           mustMarshal(serviceTabRow{LeaseAppKey: "vtx.leaseapp.c", Status: "open"}), // wrong prefix, ignored
	}
	getBookings := func(k string) ([]byte, bool) { v, ok := bookings[k]; return v, ok }
	getTabs := func(k string) ([]byte, bool) { v, ok := tabs[k]; return v, ok }

	bookingKeys := []string{"b1", "b2"}
	tabKeys := []string{cafedomainPrefixKey("t1"), cafedomainPrefixKey("t2"), "not-a-tab-key"}

	attached, total := computeServiceAttachRate(occupied, bookingKeys, getBookings, tabKeys, getTabs)
	// a: booked -> attached. b: open tab -> attached. c: settled tab + a
	// wrong-prefix "open" row that must be ignored -> not attached.
	if attached != 2 || total != 3 {
		t.Fatalf("computeServiceAttachRate = (%d, %d), want (2, 3)", attached, total)
	}
}

func TestComputeServiceAttachRate_NoOccupiedLeases_ZeroNoDivideByZero(t *testing.T) {
	attached, total := computeServiceAttachRate(nil, []string{"x"}, func(string) ([]byte, bool) { return nil, false }, []string{"y"}, func(string) ([]byte, bool) { return nil, false })
	if attached != 0 || total != 0 {
		t.Fatalf("computeServiceAttachRate with no occupied leases = (%d, %d), want (0, 0)", attached, total)
	}
}

func cafedomainPrefixKey(suffix string) string {
	return cafedomain.TabSettlementTarget + "." + suffix
}
