package main

import (
	"net/http"
	"testing"
	"time"

	cafedomain "github.com/operatinggraph/lattice/packages/cafe-domain"
)

// No-Postgres unit coverage for the portfolio-pulse reader: the fail-closed
// auth/pool paths and the pure aggregation logic, mirroring
// landlord_applications_test.go.

func TestHandlePortfolioPulse_NoAuthPosture_401(t *testing.T) {
	s := noPostureServer(t)
	rec := sessionGET(s, s.handlePortfolioPulse, "/api/portfolio-pulse", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandlePortfolioPulse_NoCookie_401(t *testing.T) {
	s, _ := devSessionServer(t, nil)
	rec := sessionGET(s, s.handlePortfolioPulse, "/api/portfolio-pulse", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no session cookie)", rec.Code)
	}
}

func TestHandlePortfolioPulse_ForgedCookie_401(t *testing.T) {
	s, _ := devSessionServer(t, nil)
	forged := &http.Cookie{Name: s.session.CookieName(), Value: "not.a.valid.jwt"}
	rec := sessionGET(s, s.handlePortfolioPulse, "/api/portfolio-pulse", forged)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (forged cookie)", rec.Code)
	}
}

// A signed-in actor with no read-model pool gets a clean 502, never a
// nil-pointer panic (mirrors the landlord-applications reader).
func TestHandlePortfolioPulse_ValidSession_PoolUnconfigured_502(t *testing.T) {
	s, cookieFor := devSessionServer(t, nil) // session set, pgPool nil
	rec := sessionGET(s, s.handlePortfolioPulse, "/api/portfolio-pulse", cookieFor("Hj4kPmRtw9nbCxz5vQ2y"))
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

// A co-managed unit fans out to one row per landlord (landlordUnitsReadSpec,
// packages/loftspace-domain/lenses.go) — a landlord co-managing a building's
// units with 3 others must still see distinct-unit counts, not 4x-inflated
// ones, and a status other than "leased" (available/pending) among the
// fanned-out duplicates must not be lost.
func TestSummarizePortfolioPulse_CoManagedUnit_DedupedByUnitKey(t *testing.T) {
	units := []portfolioPulseUnit{
		{UnitKey: "vtx.unit.a", UnitStatus: "leased"},
		{UnitKey: "vtx.unit.a", UnitStatus: "leased"},
		{UnitKey: "vtx.unit.a", UnitStatus: "leased"},
		{UnitKey: "vtx.unit.a", UnitStatus: "leased"},
		{UnitKey: "vtx.unit.b", UnitStatus: "available"},
		{UnitKey: "vtx.unit.b", UnitStatus: "available"},
	}
	got := summarizePortfolioPulse(units)
	if got.TotalUnits != 2 || got.Leased != 1 || got.Available != 1 {
		t.Fatalf("unexpected breakdown: %+v", got)
	}
	if len(got.Units) != 2 {
		t.Fatalf("Units = %d rows, want 2 deduped", len(got.Units))
	}
	if want := 0.5; got.OccupancyRate != want {
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
		{EntityKey: "vtx.leaseapp.b", SignedAt: nil},      // never signed
		{EntityKey: "vtx.leaseapp.c", SignedAt: strp("")}, // signed_at present but empty
		{EntityKey: "vtx.leaseapp.d", SignedAt: strp("2026-07-05T00:00:00Z")},
	}
	got := occupiedLeaseAppKeys(rows)
	want := []string{"vtx.leaseapp.a", "vtx.leaseapp.d"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("occupiedLeaseAppKeys = %v, want %v", got, want)
	}
}

// serviceAttachTestNow anchors every "this period" test at a fixed instant
// (Tests & Determinism: no wall-clock reliance) — 2026-08-30T12:00:00Z, with
// a serviceAttachLookbackDays=30 cutoff of 2026-07-31T12:00:00Z.
var serviceAttachTestNow = time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

func TestComputeServiceAttachRate(t *testing.T) {
	occupied := []string{
		"vtx.leaseapp.a", "vtx.leaseapp.b", "vtx.leaseapp.c", "vtx.leaseapp.d",
		"vtx.leaseapp.e", "vtx.leaseapp.f", "vtx.leaseapp.g", "vtx.leaseapp.h",
		"vtx.leaseapp.i",
	}

	bookings := map[string][]byte{
		// a: booked, startsAt in the future -> attached (a currently-booked
		// class always qualifies, its startsAt is always past cutoff).
		"b1": mustMarshal(serviceBookingRow{LeaseAppKey: "vtx.leaseapp.a", Status: "booked", StartsAt: "2026-09-15T09:00:00Z"}),
		// e: noShow, startsAt inside the lookback window -> attached — a
		// class that already happened is real usage, not noise.
		"b2": mustMarshal(serviceBookingRow{LeaseAppKey: "vtx.leaseapp.e", Status: "noShow", StartsAt: "2026-08-10T09:00:00Z"}),
		// f: noShow, startsAt before the lookback window -> not attached.
		"b3": mustMarshal(serviceBookingRow{LeaseAppKey: "vtx.leaseapp.f", Status: "noShow", StartsAt: "2026-05-01T09:00:00Z"}),
		// g: waitlisted (never got a seat), startsAt otherwise in-window ->
		// not attached regardless of timing.
		"b4": mustMarshal(serviceBookingRow{LeaseAppKey: "vtx.leaseapp.g", Status: "waitlisted", StartsAt: "2026-08-29T09:00:00Z"}),
		// h: booked with no parseable startsAt (tombstoned session) ->
		// falls back to existence-only -> attached.
		"b5": mustMarshal(serviceBookingRow{LeaseAppKey: "vtx.leaseapp.h", Status: "booked"}),
		// i: noShow with no parseable startsAt -> the existence-only
		// fallback only covers "booked" -> not attached.
		"b6": mustMarshal(serviceBookingRow{LeaseAppKey: "vtx.leaseapp.i", Status: "noShow"}),
		// belongs to a landlord/lease NOT in this landlord's occupied set —
		// must not leak into the count.
		"b7": mustMarshal(serviceBookingRow{LeaseAppKey: "vtx.leaseapp.other-landlord", Status: "booked", StartsAt: "2026-09-01T00:00:00Z"}),
	}
	tabs := map[string][]byte{
		// b: still open -> attached, no window check needed.
		cafedomainPrefixKey("t1"): mustMarshal(serviceTabRow{LeaseAppKey: "vtx.leaseapp.b", Status: "open"}),
		// c: settled inside the lookback window -> attached.
		cafedomainPrefixKey("t2"): mustMarshal(serviceTabRow{LeaseAppKey: "vtx.leaseapp.c", Status: "settled", SettledAt: "2026-08-20T00:00:00Z"}),
		// d: settled before the lookback window -> not attached.
		cafedomainPrefixKey("t3"): mustMarshal(serviceTabRow{LeaseAppKey: "vtx.leaseapp.d", Status: "settled", SettledAt: "2026-06-01T00:00:00Z"}),
		"not-a-tab-key":           mustMarshal(serviceTabRow{LeaseAppKey: "vtx.leaseapp.c", Status: "open"}), // wrong prefix, ignored
	}
	getBookings := func(k string) ([]byte, bool) { v, ok := bookings[k]; return v, ok }
	getTabs := func(k string) ([]byte, bool) { v, ok := tabs[k]; return v, ok }

	bookingKeys := []string{"b1", "b2", "b3", "b4", "b5", "b6", "b7"}
	tabKeys := []string{cafedomainPrefixKey("t1"), cafedomainPrefixKey("t2"), cafedomainPrefixKey("t3"), "not-a-tab-key"}

	attached, total := computeServiceAttachRate(occupied, serviceAttachTestNow, bookingKeys, getBookings, tabKeys, getTabs)
	// attached: a (future booked), b (open tab), c (settled in-window),
	// e (noShow in-window), h (booked, no timestamp, fallback) = 5 of 9.
	if attached != 5 || total != 9 {
		t.Fatalf("computeServiceAttachRate = (%d, %d), want (5, 9)", attached, total)
	}
}

// A missing bucket (e.g. a vertical whose package has never written a row)
// surfaces as an empty key list, not an error, at this layer — the handler
// is what turns a KVListKeys error into "pass empty keys for that source".
// This locks in that computeServiceAttachRate itself already does the right
// thing with a wholly-empty source: the other source's leases still count,
// instead of the whole rate collapsing to 0 (portfolio-pulse's
// service-attach-rate KPI going dark whenever just one of its two sources
// was absent).
func TestComputeServiceAttachRate_OneSourceEmpty_OtherSourceStillCounts(t *testing.T) {
	occupied := []string{"vtx.leaseapp.a", "vtx.leaseapp.b"}
	tabs := map[string][]byte{
		cafedomainPrefixKey("t1"): mustMarshal(serviceTabRow{LeaseAppKey: "vtx.leaseapp.a", Status: "open"}),
	}
	getTabs := func(k string) ([]byte, bool) { v, ok := tabs[k]; return v, ok }
	getBookings := func(string) ([]byte, bool) { t.Fatal("getBookings called with no booking keys"); return nil, false }

	attached, total := computeServiceAttachRate(occupied, serviceAttachTestNow, nil, getBookings, []string{cafedomainPrefixKey("t1")}, getTabs)
	if attached != 1 || total != 2 {
		t.Fatalf("computeServiceAttachRate with bookings source empty = (%d, %d), want (1, 2)", attached, total)
	}
}

func TestComputeServiceAttachRate_NoOccupiedLeases_ZeroNoDivideByZero(t *testing.T) {
	attached, total := computeServiceAttachRate(nil, serviceAttachTestNow, []string{"x"}, func(string) ([]byte, bool) { return nil, false }, []string{"y"}, func(string) ([]byte, bool) { return nil, false })
	if attached != 0 || total != 0 {
		t.Fatalf("computeServiceAttachRate with no occupied leases = (%d, %d), want (0, 0)", attached, total)
	}
}

func cafedomainPrefixKey(suffix string) string {
	return cafedomain.TabSettlementTarget + "." + suffix
}

// TestComputeLandlordLeaseBalances_WorstFirstOwedOnly locks in the three
// things portfolio-pulse's arrears column depends on: unsigned leases are
// excluded (occupiedLeaseAppKeys' own rule, applied here too), a credit
// (negative) balance is not arrears so it's dropped rather than shown as
// "owed", and the remaining rows sort highest-balance-first.
func TestComputeLandlordLeaseBalances_WorstFirstOwedOnly(t *testing.T) {
	rows := []protectedLandlordRow{
		{EntityKey: "vtx.leaseapp.small", SignedAt: strp("2026-07-01T00:00:00Z"), UnitAddress: strp("12 Small St"), ApplicantName: strp("A. Small")},
		{EntityKey: "vtx.leaseapp.big", SignedAt: strp("2026-07-01T00:00:00Z"), UnitAddress: strp("99 Big Ave")},
		{EntityKey: "vtx.leaseapp.credit", SignedAt: strp("2026-07-01T00:00:00Z")},
		{EntityKey: "vtx.leaseapp.unsigned", SignedAt: nil},
	}
	entries := map[string]string{
		"t1": `{"transactionKey":"t1","leaseAppKey":"vtx.leaseapp.small","type":"debit","amountCents":5000,"postedAt":"2026-08-01T00:00:00Z"}`,
		"t2": `{"transactionKey":"t2","leaseAppKey":"vtx.leaseapp.big","type":"debit","amountCents":480000,"postedAt":"2026-08-01T00:00:00Z"}`,
		"t3": `{"transactionKey":"t3","leaseAppKey":"vtx.leaseapp.credit","type":"credit","amountCents":10000,"postedAt":"2026-08-01T00:00:00Z"}`,
		// unsigned lease has an outstanding debit too — must still be excluded.
		"t4": `{"transactionKey":"t4","leaseAppKey":"vtx.leaseapp.unsigned","type":"debit","amountCents":999999,"postedAt":"2026-08-01T00:00:00Z"}`,
	}
	get := fakeKV(entries)

	got := computeLandlordLeaseBalances(rows, keysOf(entries), get)
	if len(got) != 2 {
		t.Fatalf("want 2 arrears rows (unsigned + credit excluded), got %d: %+v", len(got), got)
	}
	if got[0].LeaseAppKey != "vtx.leaseapp.big" || got[0].BalanceCents != 480000 || got[0].UnitAddress != "99 Big Ave" {
		t.Errorf("row 0 = %+v, want the bigger balance first", got[0])
	}
	if got[1].LeaseAppKey != "vtx.leaseapp.small" || got[1].BalanceCents != 5000 || got[1].ApplicantName != "A. Small" {
		t.Errorf("row 1 = %+v, want the smaller balance second", got[1])
	}
}
