package main

import (
	"errors"
	"testing"
	"time"
)

// TestReadAllOrFail_FailsLoudOnAnyFetchError proves a KVGet failure on a
// listed key aborts the whole read instead of silently vanishing the row —
// the bug that let a transient fetch failure produce a wrong balance.
func TestReadAllOrFail_FailsLoudOnAnyFetchError(t *testing.T) {
	boom := errors.New("boom")
	_, err := readAllOrFail([]string{"vtx.cafetransaction.1", "vtx.cafetransaction.2"}, func(key string) ([]byte, error) {
		if key == "vtx.cafetransaction.2" {
			return nil, boom
		}
		return []byte(`{}`), nil
	})
	if err == nil {
		t.Fatal("want an error when one of two listed keys fails to fetch, got nil")
	}
}

func TestReadAllOrFail_AllValuesOnSuccess(t *testing.T) {
	values, err := readAllOrFail([]string{"a", "b"}, func(key string) ([]byte, error) {
		return []byte(key), nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(values["a"]) != "a" || string(values["b"]) != "b" {
		t.Errorf("values = %+v, want each key's own bytes", values)
	}
}

func TestComputeLedgerHistory_FiltersSumsAndOrders(t *testing.T) {
	keys, get := fakeKV(map[string]any{
		"vtx.cafetransaction.1": map[string]any{"transactionKey": "vtx.cafetransaction.1", "accountKey": "vtx.cafeaccount.aaa", "leaseAppKey": "vtx.leaseapp.aaa", "type": "debit", "amountCents": 1200, "memo": "House tab", "postedAt": "2026-07-06T00:00:00Z"},
		"vtx.cafetransaction.2": map[string]any{"transactionKey": "vtx.cafetransaction.2", "accountKey": "vtx.cafeaccount.aaa", "leaseAppKey": "vtx.leaseapp.aaa", "type": "credit", "amountCents": 500, "memo": "Payment", "postedAt": "2026-07-07T00:00:00Z"},
		// a different lease's transaction — must not leak into this lease's rows/balance
		"vtx.cafetransaction.3": map[string]any{"transactionKey": "vtx.cafetransaction.3", "accountKey": "vtx.cafeaccount.other", "leaseAppKey": "vtx.leaseapp.other", "type": "debit", "amountCents": 99999, "postedAt": "2026-07-06T00:00:00Z"},
		// a tombstoned / undecodable projection entry — skipped
		"vtx.cafetransaction.4": map[string]any{},
	})

	rows, balance := computeLedgerHistory(keys, get, "vtx.leaseapp.aaa")
	if len(rows) != 2 {
		t.Fatalf("want 2 rows for the lease, got %d (%+v)", len(rows), rows)
	}
	if rows[0].TransactionKey != "vtx.cafetransaction.1" || rows[1].TransactionKey != "vtx.cafetransaction.2" {
		t.Errorf("want chronological order (1, 2), got (%s, %s)", rows[0].TransactionKey, rows[1].TransactionKey)
	}
	if balance != 700 {
		t.Errorf("balance: want 1200-500=700, got %d", balance)
	}
}

// TestComputeLedgerHistory_ProjectsReversesAndTabKeys pins the two columns the
// statement's refund affordances are built on. Both are optional in the lens, so
// both arrive absent on an ordinary payment — a handler that dropped them would
// leave every refund rendering identically to cash the resident handed over, and
// every charge un-refundable, with no test the wiser.
func TestComputeLedgerHistory_ProjectsReversesAndTabKeys(t *testing.T) {
	keys, get := fakeKV(map[string]any{
		"vtx.cafetransaction.1": map[string]any{"transactionKey": "vtx.cafetransaction.1", "accountKey": "vtx.cafeaccount.aaa", "leaseAppKey": "vtx.leaseapp.aaa", "type": "debit", "amountCents": 900, "memo": "Settled tab", "postedAt": "2026-07-06T00:00:00Z", "tabKey": "vtx.tab.t1"},
		"vtx.cafetransaction.2": map[string]any{"transactionKey": "vtx.cafetransaction.2", "accountKey": "vtx.cafeaccount.aaa", "leaseAppKey": "vtx.leaseapp.aaa", "type": "credit", "amountCents": 400, "memo": "Wrong item charged", "postedAt": "2026-07-07T00:00:00Z", "reversesKey": "vtx.cafetransaction.1"},
		"vtx.cafetransaction.3": map[string]any{"transactionKey": "vtx.cafetransaction.3", "accountKey": "vtx.cafeaccount.aaa", "leaseAppKey": "vtx.leaseapp.aaa", "type": "credit", "amountCents": 500, "memo": "House tab payment", "postedAt": "2026-07-08T00:00:00Z"},
	})

	rows, balance := computeLedgerHistory(keys, get, "vtx.leaseapp.aaa")
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d (%+v)", len(rows), rows)
	}
	if rows[0].TabKey != "vtx.tab.t1" {
		t.Errorf("charge tabKey = %q, want vtx.tab.t1 (what marks a debit refundable)", rows[0].TabKey)
	}
	if rows[0].ReversesKey != "" {
		t.Errorf("a charge reverses nothing, got %q", rows[0].ReversesKey)
	}
	if rows[1].ReversesKey != "vtx.cafetransaction.1" {
		t.Errorf("refund reversesKey = %q, want the charge it gives back", rows[1].ReversesKey)
	}
	if rows[1].TabKey != "" {
		t.Errorf("a refund settles no tab, got %q", rows[1].TabKey)
	}
	if rows[2].ReversesKey != "" || rows[2].TabKey != "" {
		t.Errorf("a plain payment carries neither column, got reversesKey=%q tabKey=%q", rows[2].ReversesKey, rows[2].TabKey)
	}
	// A refund is an ordinary credit in the arithmetic — the link carries the
	// correction's identity, so the balance sums it exactly like the payment.
	if balance != 0 {
		t.Errorf("balance: want 900-400-500=0, got %d", balance)
	}
}

func TestComputeLedgerHistory_NoTransactionsZeroBalance(t *testing.T) {
	rows, balance := computeLedgerHistory(nil, func(string) ([]byte, bool) { return nil, false }, "vtx.leaseapp.fresh")
	if len(rows) != 0 || balance != 0 {
		t.Errorf("want no rows / zero balance, got %d rows, balance=%d", len(rows), balance)
	}
}

func TestDeriveStatement_ZeroOrCreditBalanceHasNoDueDate(t *testing.T) {
	now := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	if due, overdue, days := deriveStatement(nil, 0, now); due != "" || overdue || days != 0 {
		t.Errorf("zero balance: want no due date, got due=%q overdue=%v days=%d", due, overdue, days)
	}
	if due, overdue, days := deriveStatement(nil, -500, now); due != "" || overdue || days != 0 {
		t.Errorf("credit balance: want no due date, got due=%q overdue=%v days=%d", due, overdue, days)
	}
}

func TestDeriveStatement_WithinGraceIsNotOverdue(t *testing.T) {
	rows := []ledgerEntryRow{{Type: "debit", AmountCents: 4750, PostedAt: "2026-08-20T00:00:00Z"}}
	now := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	due, overdue, days := deriveStatement(rows, 4750, now)
	if due != "2026-09-04T00:00:00Z" {
		t.Errorf("dueDate = %q, want 2026-09-04T00:00:00Z (15 days after the charge)", due)
	}
	if overdue || days != 0 {
		t.Errorf("want not overdue within grace, got overdue=%v days=%d", overdue, days)
	}
}

func TestDeriveStatement_PastGraceIsOverdue(t *testing.T) {
	rows := []ledgerEntryRow{{Type: "debit", AmountCents: 4750, PostedAt: "2026-08-01T00:00:00Z"}}
	now := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	due, overdue, days := deriveStatement(rows, 4750, now)
	if due != "2026-08-16T00:00:00Z" {
		t.Errorf("dueDate = %q, want 2026-08-16T00:00:00Z", due)
	}
	if !overdue || days != 14 {
		t.Errorf("want overdue=true days=14 (Aug 16 -> Aug 29 + 1), got overdue=%v days=%d", overdue, days)
	}
}

func TestDeriveStatement_CreditsAgeOffTheOldestDebitFirst(t *testing.T) {
	// Two debits; a credit big enough to fully clear the older one leaves the
	// NEWER debit's postedAt as the balance's true age — FIFO, not LIFO.
	rows := []ledgerEntryRow{
		{Type: "debit", AmountCents: 1000, PostedAt: "2026-08-01T00:00:00Z"},
		{Type: "debit", AmountCents: 500, PostedAt: "2026-08-20T00:00:00Z"},
		{Type: "credit", AmountCents: 1000, PostedAt: "2026-08-21T00:00:00Z"},
	}
	now := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	due, overdue, _ := deriveStatement(rows, 500, now)
	if due != "2026-09-04T00:00:00Z" {
		t.Errorf("dueDate = %q, want 2026-09-04T00:00:00Z (aged from the surviving Aug 20 debit)", due)
	}
	if overdue {
		t.Errorf("want not overdue (grace runs from the surviving debit, not the paid-off one)")
	}
}

// TestDeriveStatement_PrepaidCreditCarriesForward pins the live Riley Chen
// shape: a credit posted before any debit exists has nothing to offset yet,
// so it must carry forward and prepay the next debit rather than vanishing —
// the debit it prepays must not become an aged, overdue balance later.
func TestDeriveStatement_PrepaidCreditCarriesForward(t *testing.T) {
	rows := []ledgerEntryRow{
		{Type: "credit", AmountCents: 1000, PostedAt: "2026-08-01T00:00:00Z"},
		{Type: "debit", AmountCents: 1000, PostedAt: "2026-08-02T00:00:00Z"},
		{Type: "debit", AmountCents: 1425, PostedAt: "2026-08-28T23:50:00Z"},
	}
	now := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	due, overdue, days := deriveStatement(rows, 1425, now)
	if due != "2026-09-12T23:50:00Z" {
		t.Errorf("dueDate = %q, want 2026-09-12T23:50:00Z (aged from the Aug 28 debit, not the prepaid Aug 2 one)", due)
	}
	if overdue || days != 0 {
		t.Errorf("want not overdue days=0 (the Aug 1 credit prepaid the Aug 2 debit), got overdue=%v days=%d", overdue, days)
	}
}

// TestDeriveStatement_OverpaymentPrepaysLaterCharges proves a credit that
// outruns the open-debit queue carries its unapplied surplus forward to
// prepay whichever debits arrive next, in order — not just a credit that
// arrives with no debit open yet.
func TestDeriveStatement_OverpaymentPrepaysLaterCharges(t *testing.T) {
	rows := []ledgerEntryRow{
		{Type: "debit", AmountCents: 1425, PostedAt: "2026-08-01T00:00:00Z"},
		{Type: "credit", AmountCents: 5000, PostedAt: "2026-08-02T00:00:00Z"},
		{Type: "debit", AmountCents: 3000, PostedAt: "2026-08-03T00:00:00Z"},
		{Type: "debit", AmountCents: 2000, PostedAt: "2026-08-28T00:00:00Z"},
	}
	now := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	due, overdue, days := deriveStatement(rows, 1425, now)
	if due != "2026-09-12T00:00:00Z" {
		t.Errorf("dueDate = %q, want 2026-09-12T00:00:00Z (the Aug 3 debit fully prepaid, 575 of the Aug 28 debit prepaid, oldest open debit is Aug 28)", due)
	}
	if overdue || days != 0 {
		t.Errorf("want not overdue days=0, got overdue=%v days=%d", overdue, days)
	}
}

// TestDeriveStatement_PartialPrepayThenLaterCreditFIFO proves a surplus
// carried from an early credit and a later credit clearing an old debit's
// remainder compose correctly — the FIFO queue still ages off the true
// oldest open debit after both.
func TestDeriveStatement_PartialPrepayThenLaterCreditFIFO(t *testing.T) {
	rows := []ledgerEntryRow{
		{Type: "credit", AmountCents: 500, PostedAt: "2026-08-01T00:00:00Z"},
		{Type: "debit", AmountCents: 1000, PostedAt: "2026-08-02T00:00:00Z"},
		{Type: "debit", AmountCents: 700, PostedAt: "2026-08-20T00:00:00Z"},
		{Type: "credit", AmountCents: 500, PostedAt: "2026-08-21T00:00:00Z"},
	}
	now := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	due, overdue, days := deriveStatement(rows, 700, now)
	if due != "2026-09-04T00:00:00Z" {
		t.Errorf("dueDate = %q, want 2026-09-04T00:00:00Z (aged from Aug 20, the Aug 2 remainder cleared by the Aug 21 credit)", due)
	}
	if overdue || days != 0 {
		t.Errorf("want not overdue days=0, got overdue=%v days=%d", overdue, days)
	}
}

// TestDeriveStatement_PartialRetirementLeavesTheHead is the display half of the
// package-side vector of the same shape (packages/cafe-ledger's
// arrearsFIFOVectors): a credit SMALLER than the oldest open charge's remainder
// retires part of it and the head does NOT move, so the balance still ages from
// that older charge. It is the only vector where a credit neither clears the
// head nor overshoots it, and the wrong answer — popping the head anyway and
// aging from Aug 20 — is a date the other vectors produce legitimately, so
// nothing else here would catch it.
func TestDeriveStatement_PartialRetirementLeavesTheHead(t *testing.T) {
	rows := []ledgerEntryRow{
		{Type: "debit", AmountCents: 1000, PostedAt: "2026-08-01T00:00:00Z"},
		{Type: "debit", AmountCents: 700, PostedAt: "2026-08-20T00:00:00Z"},
		{Type: "credit", AmountCents: 400, PostedAt: "2026-08-21T00:00:00Z"},
	}
	now := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	due, overdue, days := deriveStatement(rows, 1300, now)
	if due != "2026-08-16T00:00:00Z" {
		t.Errorf("dueDate = %q, want 2026-08-16T00:00:00Z (the Aug 1 charge is only PART paid, so it is still the head)", due)
	}
	if !overdue || days != 14 {
		t.Errorf("want overdue=true days=14, got overdue=%v days=%d", overdue, days)
	}
}

func TestDeriveStatement_MalformedPostedAtFailsClosed(t *testing.T) {
	rows := []ledgerEntryRow{{Type: "debit", AmountCents: 4750, PostedAt: "not-a-date"}}
	due, overdue, days := deriveStatement(rows, 4750, time.Now())
	if due != "" || overdue || days != 0 {
		t.Errorf("malformed postedAt: want fail-closed (no due date), got due=%q overdue=%v days=%d", due, overdue, days)
	}
}

func TestResolveLeaseAccount_FindsMatchOrEmpty(t *testing.T) {
	keys, get := fakeKV(map[string]any{
		"vtx.leaseapp.aaa":   map[string]any{"leaseAppKey": "vtx.leaseapp.aaa", "accountKey": "vtx.cafeaccount.xyz", "arrearsDueAt": "2026-09-11T00:00:00Z", "arrearsReminderSentAt": "2026-09-12T00:00:00Z"},
		"vtx.leaseapp.other": map[string]any{"leaseAppKey": "vtx.leaseapp.other", "accountKey": ""},
		"vtx.leaseapp.bad":   map[string]any{},
	})

	if got := resolveLeaseAccount(keys, get, "vtx.leaseapp.aaa"); got.AccountKey != "vtx.cafeaccount.xyz" {
		t.Errorf("resolveLeaseAccount(aaa).AccountKey = %q, want vtx.cafeaccount.xyz", got.AccountKey)
	} else if got.ReminderSentAt != "2026-09-12T00:00:00Z" {
		t.Errorf("resolveLeaseAccount(aaa).ReminderSentAt = %q, want the lens row's arrearsReminderSentAt (2026-09-12T00:00:00Z), not merely non-empty", got.ReminderSentAt)
	} else if got.ArrearsDueAt != "2026-09-11T00:00:00Z" {
		t.Errorf("resolveLeaseAccount(aaa).ArrearsDueAt = %q, want 2026-09-11T00:00:00Z", got.ArrearsDueAt)
	}
	if got := resolveLeaseAccount(keys, get, "vtx.leaseapp.other"); got.AccountKey != "" || got.ReminderSentAt != "" {
		t.Errorf("resolveLeaseAccount(other) = %+v, want zero value (no account opened yet, nothing aged)", got)
	}
	if got := resolveLeaseAccount(keys, get, "vtx.leaseapp.unprojected"); got.AccountKey != "" || got.ReminderSentAt != "" {
		t.Errorf("resolveLeaseAccount(unprojected) = %+v, want zero value (no row at all)", got)
	}
}

// TestIndexLeaseAccounts_JoinsByLeaseAppKey proves the front-desk grid's
// batch join: every cafeLeaseAccounts row's reminderSentAt is keyed by its
// own leaseAppKey, and a lease with no row in the bucket at all (never
// looked up) reads back the zero value rather than another lease's data.
func TestIndexLeaseAccounts_JoinsByLeaseAppKey(t *testing.T) {
	keys, get := fakeKV(map[string]any{
		"vtx.leaseapp.aaa": map[string]any{"leaseAppKey": "vtx.leaseapp.aaa", "accountKey": "vtx.cafeaccount.xyz", "arrearsDueAt": "2026-09-11T00:00:00Z", "arrearsReminderSentAt": "2026-09-12T00:00:00Z"},
		"vtx.leaseapp.bbb": map[string]any{"leaseAppKey": "vtx.leaseapp.bbb", "accountKey": "vtx.cafeaccount.qrs"},
	})
	idx := indexLeaseAccounts(keys, get)

	if got := idx["vtx.leaseapp.aaa"].ReminderSentAt; got != "2026-09-12T00:00:00Z" {
		t.Errorf("idx[aaa].ReminderSentAt = %q, want the lens row's arrearsReminderSentAt (2026-09-12T00:00:00Z)", got)
	}
	if got, ok := idx["vtx.leaseapp.bbb"]; !ok || got.ReminderSentAt != "" {
		t.Errorf("idx[bbb] = %+v (ok=%v), want present with empty ReminderSentAt (no account row ever reminded)", got, ok)
	}
	if got, ok := idx["vtx.leaseapp.unlisted"]; ok || got.ReminderSentAt != "" {
		t.Errorf("idx[unlisted] = %+v (ok=%v), want absent/zero value for a lease with no row at all", got, ok)
	}
}
