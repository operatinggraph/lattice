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

// A settled tab's paidAtSettleCents passes through computeTabs unchanged
// when the lens row carries it (cash the desk took at settle), and stays
// absent (zero-valued, omitempty on the wire) when the row never recorded
// one — a settle with no counter payment, or a row predating the field.
func TestComputeTabs_PaidAtSettleCentsPresentAndAbsent(t *testing.T) {
	keys, get := fakeKV(map[string]any{
		"cafeTabSettlement.paid": map[string]any{
			"tabKey": "vtx.tab.paid", "leaseAppKey": "vtx.leaseapp.a", "accountKey": "vtx.cafeaccount.1",
			"totalCents": 1949.0, "status": "settled", "openedAt": "2026-09-16T10:00:00Z", "settledAt": "2026-09-16T11:00:00Z",
			"paidAtSettleCents": 1949.0, "missing_account": false, "missing_charge": false,
		},
		"cafeTabSettlement.unpaid": map[string]any{
			"tabKey": "vtx.tab.unpaid", "leaseAppKey": "vtx.leaseapp.a", "accountKey": "vtx.cafeaccount.1",
			"totalCents": 800.0, "status": "settled", "openedAt": "2026-09-16T09:00:00Z", "settledAt": "2026-09-16T09:30:00Z",
			"missing_account": false, "missing_charge": false,
		},
	})
	rows := computeTabs(keys, get, "")
	byKey := map[string]tabRow{}
	for _, r := range rows {
		byKey[r.TabKey] = r
	}
	if got, want := byKey["vtx.tab.paid"].PaidAtSettleCents, int64(1949); got != want {
		t.Errorf("paid tab PaidAtSettleCents = %d, want %d", got, want)
	}
	if got := byKey["vtx.tab.unpaid"].PaidAtSettleCents; got != 0 {
		t.Errorf("unpaid tab PaidAtSettleCents = %d, want 0 (absent)", got)
	}
}

// Posted waits on missing_payment the same way it already waits on
// missing_charge: a settled row whose charge landed but whose counter
// payment has not yet posted is not Posted, one where both have posted is,
// and a row carrying no missing_payment key at all (no counter payment was
// ever taken, or the row predates the field) falls back to missing_charge
// alone.
func TestComputeTabs_PostedWaitsOnMissingPayment(t *testing.T) {
	keys, get := fakeKV(map[string]any{
		"cafeTabSettlement.chargedNotPaid": map[string]any{
			"tabKey": "vtx.tab.chargedNotPaid", "leaseAppKey": "vtx.leaseapp.a", "accountKey": "vtx.cafeaccount.1",
			"totalCents": 1949.0, "status": "settled", "openedAt": "2026-09-16T10:00:00Z", "settledAt": "2026-09-16T11:00:00Z",
			"paidAtSettleCents": 1949.0, "missing_account": false, "missing_charge": false, "missing_payment": true,
		},
		"cafeTabSettlement.chargedAndPaid": map[string]any{
			"tabKey": "vtx.tab.chargedAndPaid", "leaseAppKey": "vtx.leaseapp.a", "accountKey": "vtx.cafeaccount.1",
			"totalCents": 1949.0, "status": "settled", "openedAt": "2026-09-16T09:00:00Z", "settledAt": "2026-09-16T09:30:00Z",
			"paidAtSettleCents": 1949.0, "missing_account": false, "missing_charge": false, "missing_payment": false,
		},
		"cafeTabSettlement.noCounterPayment": map[string]any{
			"tabKey": "vtx.tab.noCounterPayment", "leaseAppKey": "vtx.leaseapp.a", "accountKey": "vtx.cafeaccount.1",
			"totalCents": 800.0, "status": "settled", "openedAt": "2026-09-16T08:00:00Z", "settledAt": "2026-09-16T08:30:00Z",
			"missing_account": false, "missing_charge": false,
		},
	})
	rows := computeTabs(keys, get, "")
	byKey := map[string]tabRow{}
	for _, r := range rows {
		byKey[r.TabKey] = r
	}
	if got := byKey["vtx.tab.chargedNotPaid"].Posted; got {
		t.Errorf("charge posted but payment gap still open: Posted = %v, want false", got)
	}
	if got := byKey["vtx.tab.chargedAndPaid"].Posted; !got {
		t.Errorf("charge and payment both posted: Posted = %v, want true", got)
	}
	if got := byKey["vtx.tab.noCounterPayment"].Posted; !got {
		t.Errorf("no counter payment ever taken (no missing_payment key): Posted = %v, want true (follows missing_charge alone)", got)
	}
}

// A line's orderedAt/servedAt/servedBy/voidedReason pass through computeTabs
// unchanged — the desk's orders queue and the receipt's per-line state tag
// both read these straight off tabChargeLine, so a silently dropped or
// renamed field here would starve both without either failing to compile.
func TestComputeTabs_LineOrderedAndServedFieldsRoundTrip(t *testing.T) {
	keys, get := fakeKV(map[string]any{
		"cafeTabSettlement.open1": map[string]any{
			"tabKey": "vtx.tab.open1", "leaseAppKey": "vtx.leaseapp.a", "totalCents": 450.0,
			"status": "open", "openedAt": "2026-09-16T10:00:00Z",
			"lines": []map[string]any{
				{
					"id": "line-1", "description": "Latte", "amountCents": 450.0, "voided": false,
					"orderedBy": "vtx.identity.riley", "orderedAt": "2026-09-16T12:00:00Z",
					"servedAt": "2026-09-16T12:05:00Z", "servedBy": "vtx.identity.dana",
				},
				{
					"id": "line-2", "description": "Cortado", "amountCents": 400.0, "voided": true,
					"voidedReason": "unserved", "orderedBy": "vtx.identity.jamie", "orderedAt": "2026-09-16T11:00:00Z",
				},
			},
		},
	})
	rows := computeTabs(keys, get, "")
	if len(rows) != 1 || len(rows[0].Lines) != 2 {
		t.Fatalf("want 1 tab with 2 lines, got %+v", rows)
	}
	line := rows[0].Lines[0]
	if got, want := line.OrderedAt, "2026-09-16T12:00:00Z"; got != want {
		t.Errorf("OrderedAt = %q, want %q", got, want)
	}
	if got, want := line.ServedAt, "2026-09-16T12:05:00Z"; got != want {
		t.Errorf("ServedAt = %q, want %q", got, want)
	}
	if got, want := line.ServedBy, "vtx.identity.dana"; got != want {
		t.Errorf("ServedBy = %q, want %q", got, want)
	}
	swept := rows[0].Lines[1]
	if got, want := swept.VoidedReason, "unserved"; got != want {
		t.Errorf("VoidedReason = %q, want %q (the sweep's own void, threaded from the source row)", got, want)
	}
}
