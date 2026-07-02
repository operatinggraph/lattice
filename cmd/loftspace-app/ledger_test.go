package main

import "testing"

func TestComputeLedgerHistory_FiltersSumsAndOrders(t *testing.T) {
	entries := map[string]string{
		"vtx.transaction.1": `{"transactionKey":"vtx.transaction.1","accountKey":"vtx.account.lll","leaseAppKey":"vtx.leaseapp.lll","type":"debit","amountCents":150000,"memo":"June rent","postedAt":"2026-06-01T00:00:00Z"}`,
		"vtx.transaction.2": `{"transactionKey":"vtx.transaction.2","accountKey":"vtx.account.lll","leaseAppKey":"vtx.leaseapp.lll","type":"credit","amountCents":100000,"memo":"Partial payment","postedAt":"2026-06-05T00:00:00Z"}`,
		// a different lease's transaction — must not leak into this lease's rows/balance
		"vtx.transaction.3": `{"transactionKey":"vtx.transaction.3","accountKey":"vtx.account.other","leaseAppKey":"vtx.leaseapp.other","type":"debit","amountCents":999999,"postedAt":"2026-06-01T00:00:00Z"}`,
		// a tombstoned / undecodable projection entry — skipped
		"vtx.transaction.4": `{}`,
	}
	get := fakeKV(entries)

	rows, balance := computeLedgerHistory(keysOf(entries), get, "vtx.leaseapp.lll")
	if len(rows) != 2 {
		t.Fatalf("want 2 rows for the lease, got %d (%+v)", len(rows), rows)
	}
	if rows[0].TransactionKey != "vtx.transaction.1" || rows[1].TransactionKey != "vtx.transaction.2" {
		t.Errorf("want chronological order (1, 2), got (%s, %s)", rows[0].TransactionKey, rows[1].TransactionKey)
	}
	if balance != 50000 {
		t.Errorf("balance: want 150000-100000=50000, got %d", balance)
	}
}

func TestComputeLedgerHistory_NoTransactionsZeroBalance(t *testing.T) {
	rows, balance := computeLedgerHistory(nil, fakeKV(nil), "vtx.leaseapp.fresh")
	if len(rows) != 0 || balance != 0 {
		t.Errorf("want no rows / zero balance, got %d rows, balance=%d", len(rows), balance)
	}
}

func TestResolveLeaseAccount_FindsMatchOrEmpty(t *testing.T) {
	entries := map[string]string{
		"vtx.leaseapp.lll":   `{"leaseAppKey":"vtx.leaseapp.lll","accountKey":"vtx.account.xyz"}`,
		"vtx.leaseapp.other": `{"leaseAppKey":"vtx.leaseapp.other","accountKey":""}`,
		// a tombstoned / undecodable projection entry — skipped
		"vtx.leaseapp.bad": `{}`,
	}
	get := fakeKV(entries)

	if got := resolveLeaseAccount(keysOf(entries), get, "vtx.leaseapp.lll"); got != "vtx.account.xyz" {
		t.Errorf("resolveLeaseAccount(lll) = %q, want vtx.account.xyz", got)
	}
	if got := resolveLeaseAccount(keysOf(entries), get, "vtx.leaseapp.other"); got != "" {
		t.Errorf("resolveLeaseAccount(other) = %q, want empty (no account opened yet)", got)
	}
	if got := resolveLeaseAccount(keysOf(entries), get, "vtx.leaseapp.unprojected"); got != "" {
		t.Errorf("resolveLeaseAccount(unprojected) = %q, want empty (no row at all)", got)
	}
}
