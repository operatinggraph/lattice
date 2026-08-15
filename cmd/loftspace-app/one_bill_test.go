package main

import "testing"

func TestComputeOneBillHistory_FiltersSumsOrdersAndTagsSource(t *testing.T) {
	entries := map[string]string{
		"vtx.transaction.1":     `{"transactionKey":"vtx.transaction.1","accountKey":"vtx.account.lll","leaseAppKey":"vtx.leaseapp.lll","type":"debit","amountCents":150000,"memo":"June rent","postedAt":"2026-06-01T00:00:00Z","source":"rent"}`,
		"vtx.cafetransaction.1": `{"transactionKey":"vtx.cafetransaction.1","accountKey":"vtx.cafeaccount.lll","leaseAppKey":"vtx.leaseapp.lll","type":"debit","amountCents":525,"memo":"Latte + Croissant","postedAt":"2026-06-03T00:00:00Z","source":"cafe"}`,
		"vtx.transaction.2":     `{"transactionKey":"vtx.transaction.2","accountKey":"vtx.account.lll","leaseAppKey":"vtx.leaseapp.lll","type":"credit","amountCents":100000,"memo":"Partial payment","postedAt":"2026-06-05T00:00:00Z","source":"rent"}`,
		// a different lease's transaction — must not leak into this lease's rows/balance
		"vtx.transaction.3": `{"transactionKey":"vtx.transaction.3","accountKey":"vtx.account.other","leaseAppKey":"vtx.leaseapp.other","type":"debit","amountCents":999999,"postedAt":"2026-06-01T00:00:00Z","source":"rent"}`,
		// a tombstoned / undecodable projection entry — skipped
		"vtx.transaction.4": `{}`,
	}
	get := fakeKV(entries)

	rows, balance := computeOneBillHistory(keysOf(entries), get, "vtx.leaseapp.lll")
	if len(rows) != 3 {
		t.Fatalf("want 3 rows for the lease, got %d (%+v)", len(rows), rows)
	}
	if rows[0].TransactionKey != "vtx.transaction.1" || rows[1].TransactionKey != "vtx.cafetransaction.1" || rows[2].TransactionKey != "vtx.transaction.2" {
		t.Errorf("want chronological order (1, cafe.1, 2), got (%s, %s, %s)", rows[0].TransactionKey, rows[1].TransactionKey, rows[2].TransactionKey)
	}
	if rows[0].Source != "rent" || rows[1].Source != "cafe" || rows[2].Source != "rent" {
		t.Errorf("want source tags (rent, cafe, rent), got (%s, %s, %s)", rows[0].Source, rows[1].Source, rows[2].Source)
	}
	// 150000 (rent debit) + 525 (café debit) - 100000 (rent credit) = 50525
	if balance != 50525 {
		t.Errorf("balance: want 150000+525-100000=50525, got %d", balance)
	}
}

func TestComputeOneBillHistory_NoTransactionsZeroBalance(t *testing.T) {
	rows, balance := computeOneBillHistory(nil, fakeKV(nil), "vtx.leaseapp.fresh")
	if len(rows) != 0 || balance != 0 {
		t.Errorf("want no rows / zero balance, got %d rows, balance=%d", len(rows), balance)
	}
}
