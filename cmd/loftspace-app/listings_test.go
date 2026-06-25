package main

import (
	"encoding/json"
	"testing"
)

// fakeKV is an in-memory kvGetter for the assembler tests: a map of key → raw
// envelope bytes. A missing key reports false, as a real Core KV read does.
func fakeKV(entries map[string]string) kvGetter {
	return func(key string) ([]byte, bool) {
		v, ok := entries[key]
		if !ok {
			return nil, false
		}
		return []byte(v), true
	}
}

func aspect(isDeleted bool, data string) string {
	if data == "" {
		data = "{}"
	}
	d := "false"
	if isDeleted {
		d = "true"
	}
	return `{"isDeleted":` + d + `,"data":` + data + `}`
}

func TestComputeListings_FiltersAndJoinsAddress(t *testing.T) {
	entries := map[string]string{
		// available, with address
		"vtx.unit.aaa.listing": aspect(false, `{"rentAmount":2400,"rentCurrency":"USD","bedrooms":2,"status":"available"}`),
		"vtx.unit.aaa.address": aspect(false, `{"line1":"1 Market St","city":"SF","region":"CA","postal":"94103"}`),
		// pending, no address
		"vtx.unit.bbb.listing": aspect(false, `{"rentAmount":3000,"rentCurrency":"USD","bedrooms":3,"status":"pending"}`),
		// tombstoned listing — never surfaces
		"vtx.unit.ccc.listing": aspect(true, `{"status":"available"}`),
		// noise that must be ignored
		"vtx.unit.aaa":             aspect(false, `{}`),
		"vtx.leaseapp.zzz":         aspect(false, `{}`),
		"lnk.leaseapp.z.x.unit.aaa": aspect(false, `{}`),
	}
	get := fakeKV(entries)

	// default available-only
	got := computeListings(keysOf(entries), get, "available")
	if len(got) != 1 {
		t.Fatalf("available filter: want 1 row, got %d (%+v)", len(got), got)
	}
	if got[0].UnitKey != "vtx.unit.aaa" {
		t.Errorf("unitKey: want vtx.unit.aaa, got %q", got[0].UnitKey)
	}
	if got[0].Status != "available" {
		t.Errorf("status: want available, got %q", got[0].Status)
	}
	if got[0].Address == nil {
		t.Errorf("address: want the joined .address data, got nil")
	} else {
		var a map[string]any
		if err := json.Unmarshal(got[0].Address, &a); err != nil || a["city"] != "SF" {
			t.Errorf("address join: want city=SF, got %v (err %v)", a, err)
		}
	}

	// all statuses → the available + the pending (tombstoned still excluded)
	all := computeListings(keysOf(entries), get, "all")
	if len(all) != 2 {
		t.Fatalf("all filter: want 2 rows, got %d (%+v)", len(all), all)
	}
	if all[1].Address != nil {
		t.Errorf("pending unit has no address; want nil, got %s", string(all[1].Address))
	}

	// a status that matches nothing
	none := computeListings(keysOf(entries), get, "leased")
	if len(none) != 0 {
		t.Errorf("leased filter: want 0 rows, got %d", len(none))
	}
}

func TestComputeIdentities_LiveRootsWithLabel(t *testing.T) {
	entries := map[string]string{
		"vtx.identity.aaa":         aspect(false, `{}`),
		"vtx.identity.aaa.profile": aspect(false, `{"displayName":"Ada Lovelace"}`),
		"vtx.identity.bbb":         aspect(false, `{}`),
		"vtx.identity.ccc":         aspect(true, `{}`), // tombstoned → excluded
		"vtx.unit.aaa":             aspect(false, `{}`), // not an identity
	}
	got := computeIdentities(keysOf(entries), fakeKV(entries))
	if len(got) != 2 {
		t.Fatalf("want 2 live identities, got %d (%+v)", len(got), got)
	}
	if got[0].Key != "vtx.identity.aaa" || got[0].Label != "Ada Lovelace" {
		t.Errorf("first identity: want aaa/Ada Lovelace, got %q/%q", got[0].Key, got[0].Label)
	}
	if got[1].Key != "vtx.identity.bbb" || got[1].Label != "" {
		t.Errorf("second identity: want bbb/<no label>, got %q/%q", got[1].Key, got[1].Label)
	}
}

func TestUnitOfListingKey(t *testing.T) {
	cases := []struct {
		key  string
		unit string
		ok   bool
	}{
		{"vtx.unit.aaa.listing", "vtx.unit.aaa", true},
		{"vtx.unit.aaa.address", "", false},
		{"vtx.unit.aaa", "", false},
		{"vtx.unit..listing", "", false},
		{"vtx.leaseapp.aaa.listing", "", false},
		{"lnk.unit.aaa.x.y.z", "", false},
	}
	for _, c := range cases {
		unit, ok := unitOfListingKey(c.key)
		if ok != c.ok || unit != c.unit {
			t.Errorf("unitOfListingKey(%q) = (%q,%v), want (%q,%v)", c.key, unit, ok, c.unit, c.ok)
		}
	}
}

// keysOf returns the map keys (order-independent; computeListings sorts output).
func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
