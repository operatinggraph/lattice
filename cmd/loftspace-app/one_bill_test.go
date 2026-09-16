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

// TestComputeOneBillHistory_PeriodRidesThrough — the rent source's recorded
// billing period + due date pass through to the statement row; a café entry
// (which projects none) stays empty.
func TestComputeOneBillHistory_PeriodRidesThrough(t *testing.T) {
	entries := map[string]string{
		"vtx.transaction.1":     `{"transactionKey":"vtx.transaction.1","accountKey":"vtx.account.lll","leaseAppKey":"vtx.leaseapp.lll","type":"debit","amountCents":212500,"postedAt":"2026-09-13T23:49:30Z","source":"rent","periodStart":"2026-09-06T00:00:00Z","periodEnd":"2026-10-06T00:00:00Z","dueAt":"2026-09-06T00:00:00Z"}`,
		"vtx.cafetransaction.1": `{"transactionKey":"vtx.cafetransaction.1","accountKey":"vtx.cafeaccount.lll","leaseAppKey":"vtx.leaseapp.lll","type":"debit","amountCents":1200,"postedAt":"2026-09-14T00:00:00Z","source":"cafe"}`,
	}
	rows, _ := computeOneBillHistory(keysOf(entries), fakeKV(entries), "vtx.leaseapp.lll")
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if rows[0].PeriodStart != "2026-09-06T00:00:00Z" || rows[0].PeriodEnd != "2026-10-06T00:00:00Z" || rows[0].DueAt != "2026-09-06T00:00:00Z" {
		t.Errorf("rent row period = (%q, %q, due %q)", rows[0].PeriodStart, rows[0].PeriodEnd, rows[0].DueAt)
	}
	if rows[1].PeriodStart != "" || rows[1].DueAt != "" {
		t.Errorf("café row must carry no period, got (%q, %q)", rows[1].PeriodStart, rows[1].DueAt)
	}
}

// TestComputeOneBillHistory_ClausePurposeRidesThrough — a rent entry
// authorized by a purpose=deposit clause carries clausePurpose through to
// the combined statement row (the entry-row "Deposit" tag reads it); a
// café entry (which never authorizes off a semantic-contracts clause) and
// a plain rent charge both stay empty.
func TestComputeOneBillHistory_ClausePurposeRidesThrough(t *testing.T) {
	entries := map[string]string{
		"vtx.transaction.1":     `{"transactionKey":"vtx.transaction.1","accountKey":"vtx.account.lll","leaseAppKey":"vtx.leaseapp.lll","type":"debit","amountCents":150000,"postedAt":"2026-06-01T00:00:00Z","source":"rent","clausePurpose":"deposit"}`,
		"vtx.transaction.2":     `{"transactionKey":"vtx.transaction.2","accountKey":"vtx.account.lll","leaseAppKey":"vtx.leaseapp.lll","type":"debit","amountCents":230000,"postedAt":"2026-06-02T00:00:00Z","source":"rent"}`,
		"vtx.cafetransaction.1": `{"transactionKey":"vtx.cafetransaction.1","accountKey":"vtx.cafeaccount.lll","leaseAppKey":"vtx.leaseapp.lll","type":"debit","amountCents":525,"postedAt":"2026-06-03T00:00:00Z","source":"cafe"}`,
	}
	rows, _ := computeOneBillHistory(keysOf(entries), fakeKV(entries), "vtx.leaseapp.lll")
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
	if rows[0].ClausePurpose != "deposit" {
		t.Errorf("deposit row clausePurpose = %q, want deposit", rows[0].ClausePurpose)
	}
	if rows[1].ClausePurpose != "" {
		t.Errorf("plain rent row clausePurpose = %q, want empty", rows[1].ClausePurpose)
	}
	if rows[2].ClausePurpose != "" {
		t.Errorf("café row clausePurpose = %q, want empty", rows[2].ClausePurpose)
	}
}
