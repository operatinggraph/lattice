package main

import (
	"encoding/json"
	"net/http"
	"testing"

	cafedomain "github.com/operatinggraph/lattice/packages/cafe-domain"
)

// seedHousePolicy seeds one cafeHousePolicies row for locationKey.
func seedHousePolicy(t *testing.T, s *server, locationKey string, tabLimitCents float64, name string) {
	t.Helper()
	putJSON(t, s.conn, cafedomain.HousePoliciesBucket, locationKey, map[string]any{
		"locationKey":   locationKey,
		"tabLimitCents": tabLimitCents,
		"name":          name,
	})
}

// TestEffectiveTabLimit_MinOverCoveringPolicies pins the composition rule the
// residents join applies — the same tightest-policy rule house_tab_limit
// applies on the op's resident-self leg (packages/cafe-domain/ddls.go): the
// minimum over the covering locations that record a policy, none when none
// does, and a $0 policy is a limit (found), never "no policy".
func TestEffectiveTabLimit_MinOverCoveringPolicies(t *testing.T) {
	policies := map[string]housePolicyRow{
		"vtx.building.b": {LocationKey: "vtx.building.b", TabLimitCents: 2000},
		"vtx.property.p": {LocationKey: "vtx.property.p", TabLimitCents: 900},
		"vtx.building.z": {LocationKey: "vtx.building.z", TabLimitCents: 0},
	}
	for _, tc := range []struct {
		name      string
		covering  []string
		wantLimit int64
		wantFound bool
	}{
		{"none on the chain", []string{"vtx.unit.u", "vtx.building.x"}, 0, false},
		{"building only", []string{"vtx.unit.u", "vtx.building.b"}, 2000, true},
		{"property tighter than building", []string{"vtx.unit.u", "vtx.building.b", "vtx.property.p"}, 900, true},
		{"order does not matter", []string{"vtx.property.p", "vtx.building.b", "vtx.unit.u"}, 900, true},
		{"a zero policy is a limit", []string{"vtx.unit.u", " vtx.building.z "}, 0, true},
		{"empty covering set", nil, 0, false},
	} {
		limit, found := effectiveTabLimit(tc.covering, policies)
		if limit != tc.wantLimit || found != tc.wantFound {
			t.Errorf("%s: effectiveTabLimit(%v) = (%d, %v), want (%d, %v)", tc.name, tc.covering, limit, found, tc.wantLimit, tc.wantFound)
		}
	}
}

// TestComputeHousePolicies_SkipsRowsWithoutALimit pins that a row projected
// with no tabLimitCents (never re-projected under the current spec) or a
// negative one is dropped rather than read as a limit.
func TestComputeHousePolicies_SkipsRowsWithoutALimit(t *testing.T) {
	rows := map[string]map[string]any{
		"vtx.building.ok":   {"locationKey": "vtx.building.ok", "tabLimitCents": 5000.0, "name": "Riverside"},
		"vtx.building.zero": {"locationKey": "vtx.building.zero", "tabLimitCents": 0.0},
		"vtx.building.none": {"locationKey": "vtx.building.none", "tabLimitCents": nil},
		"vtx.building.neg":  {"locationKey": "vtx.building.neg", "tabLimitCents": -5.0},
		"vtx.building.bare": {"name": "no key"},
	}
	keys := make([]string, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	got := computeHousePolicies(keys, func(key string) ([]byte, bool) {
		b, err := json.Marshal(rows[key])
		return b, err == nil
	})
	if len(got) != 2 {
		t.Fatalf("computeHousePolicies kept %v, want exactly the two rows carrying a non-negative limit", got)
	}
	if got["vtx.building.ok"].TabLimitCents != 5000 || got["vtx.building.ok"].Name != "Riverside" {
		t.Fatalf("ok row = %+v", got["vtx.building.ok"])
	}
	if p, ok := got["vtx.building.zero"]; !ok || p.TabLimitCents != 0 {
		t.Fatalf("a $0 policy must be kept as a limit, got %+v (present=%v)", p, ok)
	}
}

// TestHandleResidents_CarriesTabLimit pins the residents join: a lease whose
// covering locations record a policy carries tabLimitCents (the minimum);
// one whose chain records none serializes it as null, never omitted and
// never 0.
func TestHandleResidents_CarriesTabLimit(t *testing.T) {
	staff, resA, resB := "AAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBB", "CCCCCCCCCCCCCCCCCCCC"
	s, cookieFor := devSessionServer(t, fakeGatewayActor(t, map[string]bool{staff: true}))
	seedLeaseAt(t, s.conn, "vtx.leaseapp.aaa", resA, "vtx.unit.a", staffWorkplace, "vtx.property.p")
	seedLeaseAt(t, s.conn, "vtx.leaseapp.bbb", resB, "vtx.unit.b", staffWorkplace)
	seedHousePolicy(t, s, "vtx.property.p", 900, "The Estate")
	seedHousePolicy(t, s, "vtx.building.elsewhere", 100, "")

	rec := sessionGET(s, s.handleResidents, "/api/residents", cookieFor(staff))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Residents []map[string]json.RawMessage `json:"residents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := map[string]string{}
	for _, r := range body.Residents {
		var leaseAppKey string
		_ = json.Unmarshal(r["leaseAppKey"], &leaseAppKey)
		raw, present := r["tabLimitCents"]
		if !present {
			t.Fatalf("%s: tabLimitCents key omitted; it must serialize as null when no policy covers the lease", leaseAppKey)
		}
		got[leaseAppKey] = string(raw)
	}
	if got["vtx.leaseapp.aaa"] != "900" {
		t.Fatalf("lease aaa tabLimitCents = %s, want 900 (the property policy on its chain)", got["vtx.leaseapp.aaa"])
	}
	if got["vtx.leaseapp.bbb"] != "null" {
		t.Fatalf("lease bbb tabLimitCents = %s, want null (no policy on its chain; the elsewhere policy does not cover it)", got["vtx.leaseapp.bbb"])
	}
}

// TestHandleHousePolicies_StaffSeesOwnWorkplaceOnly pins the desk's read:
// a worksAt staffer sees their own workplace's policy AND every policy on
// the chains of the leases that workplace covers (the property cap above it
// binds those residents too — the op applies the tightest), never a
// neighbouring house's; a resident sees the policies covering their lease.
func TestHandleHousePolicies_StaffSeesOwnWorkplaceOnly(t *testing.T) {
	staff, resA := "AAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBB"
	s, cookieFor := devSessionServer(t, fakeGatewayActor(t, map[string]bool{staff: true}))
	seedLeaseAt(t, s.conn, "vtx.leaseapp.aaa", resA, "vtx.unit.a", staffWorkplace, "vtx.property.p")
	seedHousePolicy(t, s, staffWorkplace, 5000, "Riverside")
	seedHousePolicy(t, s, "vtx.property.p", 900, "The Estate")
	seedHousePolicy(t, s, "vtx.building.elsewhere", 100, "")

	for _, tc := range []struct {
		who  string
		want []string
	}{
		{staff, []string{staffWorkplace, "vtx.property.p"}},
		{resA, []string{"vtx.property.p", staffWorkplace}},
	} {
		rec := sessionGET(s, s.handleHousePolicies, "/api/house-policies", cookieFor(tc.who))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body=%s", tc.who, rec.Code, rec.Body.String())
		}
		var body struct {
			Policies []housePolicyRow `json:"policies"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		got := map[string]bool{}
		for _, p := range body.Policies {
			got[p.LocationKey] = true
		}
		if len(got) != len(tc.want) {
			t.Fatalf("%s sees %+v, want exactly %v", tc.who, body.Policies, tc.want)
		}
		for _, w := range tc.want {
			if !got[w] {
				t.Fatalf("%s sees %+v, want %s among them", tc.who, body.Policies, w)
			}
		}
	}
}
