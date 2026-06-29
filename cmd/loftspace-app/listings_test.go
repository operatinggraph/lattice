package main

import (
	"encoding/json"
	"testing"
)

// fakeKV is an in-memory kvGetter for the assembler tests: a map of key → raw
// projection-row bytes. A missing key reports false, as a real read-model read does.
func fakeKV(entries map[string]string) kvGetter {
	return func(key string) ([]byte, bool) {
		v, ok := entries[key]
		if !ok {
			return nil, false
		}
		return []byte(v), true
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestComputeListings_FiltersStatusAndReshapes(t *testing.T) {
	entries := map[string]string{
		"vtx.unit.aaa": `{"unitKey":"vtx.unit.aaa","status":"available","rentAmount":2400,"rentCurrency":"USD","bedrooms":2,"bathrooms":1.5,"availableFrom":"2026-08-01T00:00:00Z","leaseTermMonths":12,"addrLine1":"1 Market St","addrCity":"SF","addrRegion":"CA","addrPostal":"94103"}`,
		"vtx.unit.bbb": `{"unitKey":"vtx.unit.bbb","status":"pending","rentAmount":3000,"rentCurrency":"USD","bedrooms":3}`,
		// a tombstoned / empty projection entry — skipped (no unitKey)
		"vtx.unit.ccc": `{}`,
	}
	get := fakeKV(entries)

	got := computeListings(keysOf(entries), get, "available")
	if len(got) != 1 {
		t.Fatalf("available filter: want 1 row, got %d (%+v)", len(got), got)
	}
	if got[0].UnitKey != "vtx.unit.aaa" || got[0].Status != "available" {
		t.Errorf("row: want vtx.unit.aaa/available, got %q/%q", got[0].UnitKey, got[0].Status)
	}
	// the listing facets reshape into the nested object the FE renders
	var L map[string]any
	if err := json.Unmarshal(got[0].Listing, &L); err != nil {
		t.Fatalf("listing decode: %v", err)
	}
	if L["rentAmount"].(float64) != 2400 || L["bathrooms"].(float64) != 1.5 {
		t.Errorf("listing facets: want rent 2400 / bath 1.5, got %v", L)
	}
	if got[0].Address == nil {
		t.Errorf("address: want the reshaped address, got nil")
	} else {
		var A map[string]any
		_ = json.Unmarshal(got[0].Address, &A)
		if A["city"] != "SF" {
			t.Errorf("address reshape: want city=SF, got %v", A)
		}
	}

	// all statuses → available + pending (the empty entry stays excluded)
	all := computeListings(keysOf(entries), get, "all")
	if len(all) != 2 {
		t.Fatalf("all filter: want 2 rows, got %d", len(all))
	}
	// the pending unit has no address columns → no address object
	if all[1].UnitKey == "vtx.unit.bbb" && all[1].Address != nil {
		t.Errorf("pending unit has no address; want nil, got %s", string(all[1].Address))
	}

	// a status nothing matches
	if none := computeListings(keysOf(entries), get, "leased"); len(none) != 0 {
		t.Errorf("leased filter: want 0 rows, got %d", len(none))
	}
}

// TestComputeListings_HidesWithdrawn proves an off-market (withdrawn) unit never
// reaches the applicant Browse — not under the default filter, not under "all" —
// but is still returned when explicitly filtered for (the landlord path is not
// this endpoint, but the explicit filter is the deliberate opt-in escape hatch).
func TestComputeListings_HidesWithdrawn(t *testing.T) {
	entries := map[string]string{
		"vtx.unit.aaa": `{"unitKey":"vtx.unit.aaa","status":"available","rentAmount":2400}`,
		"vtx.unit.www": `{"unitKey":"vtx.unit.www","status":"withdrawn","rentAmount":1800}`,
	}
	get := fakeKV(entries)

	if all := computeListings(keysOf(entries), get, "all"); len(all) != 1 || all[0].UnitKey != "vtx.unit.aaa" {
		t.Fatalf(`"all" must hide the withdrawn unit; got %+v`, all)
	}
	if def := computeListings(keysOf(entries), get, ""); len(def) != 1 || def[0].UnitKey != "vtx.unit.aaa" {
		t.Fatalf(`default must hide the withdrawn unit; got %+v`, def)
	}
	if avail := computeListings(keysOf(entries), get, "available"); len(avail) != 1 {
		t.Fatalf(`"available" should return only the available unit; got %+v`, avail)
	}
	if wd := computeListings(keysOf(entries), get, "withdrawn"); len(wd) != 1 || wd[0].UnitKey != "vtx.unit.www" {
		t.Fatalf(`explicit "withdrawn" filter should return the withdrawn unit; got %+v`, wd)
	}
}

func TestComputeListings_SkipsUndecodable(t *testing.T) {
	entries := map[string]string{
		"vtx.unit.aaa": `not json`,
		"vtx.unit.bbb": `{"unitKey":"vtx.unit.bbb","status":"available","rentAmount":1000}`,
	}
	got := computeListings(keysOf(entries), fakeKV(entries), "all")
	if len(got) != 1 || got[0].UnitKey != "vtx.unit.bbb" {
		t.Fatalf("want only the decodable row, got %+v", got)
	}
}
