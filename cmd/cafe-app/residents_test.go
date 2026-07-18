package main

import "testing"

func TestComputeResidents_SortsFiltersAndSkipsUndecodable(t *testing.T) {
	keys, get := fakeKV(map[string]any{
		"leaseApplicationComplete.b": map[string]any{"entityKey": "vtx.leaseapp.b", "applicant": "vtx.identity.z", "landlordApproved": true},
		"leaseApplicationComplete.a": map[string]any{"entityKey": "vtx.leaseapp.a", "applicant": "vtx.identity.y", "landlordApproved": false},
		"leaseApplicationComplete.x": map[string]any{"entityKey": "vtx.leaseapp.x"}, // no applicant yet — skipped
		"leaseApplicationComplete.n": map[string]any{},                              // undecodable — skipped
		"otherLensRow.q":             map[string]any{"applicant": "vtx.identity.q"}, // wrong lens prefix — skipped
	})
	rows := computeResidents(keys, get)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d (%+v)", len(rows), rows)
	}
	if rows[0].BookerKey != "vtx.identity.y" || rows[1].BookerKey != "vtx.identity.z" {
		t.Errorf("want sorted by bookerKey (y, z), got (%s, %s)", rows[0].BookerKey, rows[1].BookerKey)
	}
	if rows[0].LeaseAppKey != "vtx.leaseapp.a" || !rows[1].Approved {
		t.Errorf("unexpected row content: %+v", rows)
	}
}
