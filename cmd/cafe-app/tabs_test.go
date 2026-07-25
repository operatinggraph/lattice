package main

import "testing"

func TestComputeTabs_FiltersPrefixLeaseAndDerivesPosted(t *testing.T) {
	keys, get := fakeKV(map[string]any{
		"cafeTabSettlement.open1": map[string]any{
			"tabKey": "vtx.tab.open1", "leaseAppKey": "vtx.leaseapp.a", "totalCents": 850.0,
			"status": "open", "openedAt": "2026-07-07T10:00:00Z",
		},
		"cafeTabSettlement.posted1": map[string]any{
			"tabKey": "vtx.tab.posted1", "leaseAppKey": "vtx.leaseapp.a", "accountKey": "vtx.cafeaccount.1",
			"totalCents": 1200.0, "status": "settled", "openedAt": "2026-07-06T10:00:00Z", "settledAt": "2026-07-06T11:00:00Z",
			"missing_account": false, "missing_charge": false,
		},
		"cafeTabSettlement.pending1": map[string]any{
			"tabKey": "vtx.tab.pending1", "leaseAppKey": "vtx.leaseapp.b",
			"totalCents": 500.0, "status": "settled", "openedAt": "2026-07-06T09:00:00Z", "settledAt": "2026-07-06T09:30:00Z",
			"missing_account": true, "missing_charge": false,
		},
		// a different lens's row sharing the weaver-targets bucket — must not leak in
		"someOtherTarget.x": map[string]any{"tabKey": "vtx.tab.other", "leaseAppKey": "vtx.leaseapp.a"},
		// a tombstoned entry — skipped
		"cafeTabSettlement.bad": map[string]any{},
	})

	all := computeTabs(keys, get, "")
	if len(all) != 3 {
		t.Fatalf("want 3 rows (other-target row excluded), got %d (%+v)", len(all), all)
	}

	forA := computeTabs(keys, get, "vtx.leaseapp.a")
	if len(forA) != 2 {
		t.Fatalf("want 2 rows for lease a, got %d (%+v)", len(forA), forA)
	}
	byKey := map[string]tabRow{}
	for _, r := range forA {
		byKey[r.TabKey] = r
	}
	if got := byKey["vtx.tab.open1"].Posted; got {
		t.Errorf("open tab Posted = %v, want false", got)
	}
	if got := byKey["vtx.tab.posted1"].Posted; !got {
		t.Errorf("settled + converged tab Posted = %v, want true", got)
	}

	forB := computeTabs(keys, get, "vtx.leaseapp.b")
	if len(forB) != 1 || forB[0].Posted {
		t.Errorf("pending (missing_account) settled tab must report Posted=false, got %+v", forB)
	}
}

// A row naming a tab but no lease is skipped. The lease is what the
// resident's own-rows filter and the front-desk card's identity both key on,
// so a partially-converged row would otherwise render as a blank-identity tab
// in the staff house list.
func TestComputeTabs_SkipsRowWithNoLeaseAppKey(t *testing.T) {
	keys, get := fakeKV(map[string]any{
		"cafeTabSettlement.good": map[string]any{
			"tabKey": "vtx.tab.good", "leaseAppKey": "vtx.leaseapp.a",
			"totalCents": 100.0, "status": "open", "openedAt": "2026-07-07T10:00:00Z",
		},
		"cafeTabSettlement.noLease": map[string]any{
			"tabKey": "vtx.tab.noLease", "totalCents": 200.0,
			"status": "open", "openedAt": "2026-07-07T11:00:00Z",
		},
	})

	rows := computeTabs(keys, get, "")
	if len(rows) != 1 {
		t.Fatalf("want only the row carrying both keys, got %d (%+v)", len(rows), rows)
	}
	if rows[0].TabKey != "vtx.tab.good" {
		t.Errorf("kept the wrong row: %+v", rows[0])
	}
}
