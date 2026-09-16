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
	_, err := readAllOrFail([]string{"vtx.transaction.1", "vtx.transaction.2"}, func(key string) ([]byte, error) {
		if key == "vtx.transaction.2" {
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

// TestComputeLedgerHistory_PeriodRidesThrough — a recurring charge's recorded
// periodStart/periodEnd/dueAt pass through unchanged; a payment or one-time
// charge without them stays empty (omitempty).
func TestComputeLedgerHistory_PeriodRidesThrough(t *testing.T) {
	entries := map[string]string{
		"vtx.transaction.1": `{"transactionKey":"vtx.transaction.1","accountKey":"vtx.account.lll","leaseAppKey":"vtx.leaseapp.lll","type":"debit","amountCents":212500,"postedAt":"2026-09-13T23:49:30Z","periodStart":"2026-09-06T00:00:00Z","periodEnd":"2026-10-06T00:00:00Z","dueAt":"2026-09-06T00:00:00Z"}`,
		"vtx.transaction.2": `{"transactionKey":"vtx.transaction.2","accountKey":"vtx.account.lll","leaseAppKey":"vtx.leaseapp.lll","type":"credit","amountCents":212500,"postedAt":"2026-09-14T00:00:00Z"}`,
	}
	rows, _ := computeLedgerHistory(keysOf(entries), fakeKV(entries), "vtx.leaseapp.lll")
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if rows[0].PeriodStart != "2026-09-06T00:00:00Z" || rows[0].PeriodEnd != "2026-10-06T00:00:00Z" || rows[0].DueAt != "2026-09-06T00:00:00Z" {
		t.Errorf("charge row period = (%q, %q, due %q), want the projected stamps", rows[0].PeriodStart, rows[0].PeriodEnd, rows[0].DueAt)
	}
	if rows[1].PeriodStart != "" || rows[1].PeriodEnd != "" || rows[1].DueAt != "" {
		t.Errorf("payment row must carry no period, got (%q, %q, %q)", rows[1].PeriodStart, rows[1].PeriodEnd, rows[1].DueAt)
	}
}

// TestComputeLedgerHistory_ClauseProseRidesThrough (Fire V4 "why was I
// charged this?") — a transaction row carrying clauseKey/clauseProse (the
// ledgerHistory lens's optional authorizedBy hop) passes both through
// unchanged; a plain charge with neither field stays empty (omitempty).
func TestComputeLedgerHistory_ClauseProseRidesThrough(t *testing.T) {
	entries := map[string]string{
		"vtx.transaction.1": `{"transactionKey":"vtx.transaction.1","accountKey":"vtx.account.lll","leaseAppKey":"vtx.leaseapp.lll","type":"debit","amountCents":4500,"postedAt":"2026-06-01T00:00:00Z","clauseKey":"vtx.clause.abc","clauseProse":"Tenant agrees to a $45 lockout fee."}`,
		"vtx.transaction.2": `{"transactionKey":"vtx.transaction.2","accountKey":"vtx.account.lll","leaseAppKey":"vtx.leaseapp.lll","type":"credit","amountCents":10000,"postedAt":"2026-06-05T00:00:00Z"}`,
	}
	get := fakeKV(entries)

	rows, _ := computeLedgerHistory(keysOf(entries), get, "vtx.leaseapp.lll")
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d (%+v)", len(rows), rows)
	}
	if rows[0].ClauseKey != "vtx.clause.abc" || rows[0].ClauseProse != "Tenant agrees to a $45 lockout fee." {
		t.Errorf("row 1 clause fields = (%q, %q), want the clause-authorized values", rows[0].ClauseKey, rows[0].ClauseProse)
	}
	if rows[1].ClauseKey != "" || rows[1].ClauseProse != "" {
		t.Errorf("row 2 (plain charge, no clauseRef) clause fields = (%q, %q), want both empty", rows[1].ClauseKey, rows[1].ClauseProse)
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

// TestDeriveRentArrears_RecordedWinsOverDerivedHead — the leaseAccounts
// row's own recorded arrearsDueAt overrides the FIFO head's dueAt whenever a
// debit is still open.
func TestDeriveRentArrears_RecordedWinsOverDerivedHead(t *testing.T) {
	rows := []ledgerEntryRow{
		{TransactionKey: "t1", Type: "debit", AmountCents: 100000, PostedAt: "2026-09-01T00:00:00Z", DueAt: "2026-09-01T00:00:00Z"},
	}
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	got := deriveRentArrears(rows, "2026-09-05T00:00:00Z", "", now)
	if got.DueDate != "2026-09-05T00:00:00Z" {
		t.Errorf("DueDate = %q, want the RECORDED date, not the head's own dueAt (2026-09-01)", got.DueDate)
	}
}

// TestDeriveRentArrears_NoRecorded_UsesHeadDueAt — absent a recorded stamp,
// the head's own recorded dueAt (DebitAccount's stamp) is the fact.
func TestDeriveRentArrears_NoRecorded_UsesHeadDueAt(t *testing.T) {
	rows := []ledgerEntryRow{
		{TransactionKey: "t1", Type: "debit", AmountCents: 100000, PostedAt: "2026-09-01T00:00:00Z", DueAt: "2026-09-06T00:00:00Z"},
	}
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	got := deriveRentArrears(rows, "", "", now)
	if got.DueDate != "2026-09-06T00:00:00Z" {
		t.Errorf("DueDate = %q, want the head's own recorded dueAt", got.DueDate)
	}
}

// TestDeriveRentArrears_HeadWithNoDueAt_UsesPostedAt — a landlord one-off
// (LoftspaceRecordCharge) records no dueAt; it is due on receipt, its own
// postedAt, never re-gridded with an added term.
func TestDeriveRentArrears_HeadWithNoDueAt_UsesPostedAt(t *testing.T) {
	rows := []ledgerEntryRow{
		{TransactionKey: "t1", Type: "debit", AmountCents: 100000, PostedAt: "2026-09-01T00:00:00Z"},
	}
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	got := deriveRentArrears(rows, "", "", now)
	if got.DueDate != "2026-09-01T00:00:00Z" {
		t.Errorf("DueDate = %q, want the head's own postedAt (due on receipt)", got.DueDate)
	}
}

// TestDeriveRentArrears_PartialPaymentMovesHeadToNextOpenDebit — a credit
// that overpays the oldest debit retires it fully and carries the surplus
// forward, partially paying down the NEXT debit — the head moves there, and
// that head's own dueAt is what ages the balance, not the retired debit's.
func TestDeriveRentArrears_PartialPaymentMovesHeadToNextOpenDebit(t *testing.T) {
	rows := []ledgerEntryRow{
		{TransactionKey: "t1", Type: "debit", AmountCents: 100000, PostedAt: "2026-08-01T00:00:00Z", DueAt: "2026-08-01T00:00:00Z"},
		// pays off t1 (100000) with a 20000 surplus that carries forward
		{TransactionKey: "t2", Type: "credit", AmountCents: 120000, PostedAt: "2026-08-05T00:00:00Z"},
		{TransactionKey: "t3", Type: "debit", AmountCents: 50000, PostedAt: "2026-09-01T00:00:00Z", DueAt: "2026-09-01T00:00:00Z"},
	}
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	got := deriveRentArrears(rows, "", "", now)
	if got.DueDate != "2026-09-01T00:00:00Z" {
		t.Errorf("DueDate = %q, want t3's dueAt — t1 fully retired, the carried surplus only partly pays t3", got.DueDate)
	}
	if !got.IsOverdue || got.DaysOverdue != 9 {
		t.Errorf("IsOverdue/DaysOverdue = %v/%d, want true/9 (t3 due 09-01, now 09-10)", got.IsOverdue, got.DaysOverdue)
	}
}

// TestDeriveRentArrears_PaymentToZero_NoDueDate — a payment that fully
// retires every open debit leaves nothing to age.
func TestDeriveRentArrears_PaymentToZero_NoDueDate(t *testing.T) {
	rows := []ledgerEntryRow{
		{TransactionKey: "t1", Type: "debit", AmountCents: 100000, PostedAt: "2026-08-01T00:00:00Z", DueAt: "2026-08-01T00:00:00Z"},
		{TransactionKey: "t2", Type: "credit", AmountCents: 100000, PostedAt: "2026-08-05T00:00:00Z"},
	}
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	got := deriveRentArrears(rows, "", "", now)
	if got.DueDate != "" || got.IsOverdue || got.DaysOverdue != 0 || got.DaysUntilDue != 0 {
		t.Errorf("got %+v, want the zero value — nothing owed, nothing to age", got)
	}
}

// TestDeriveRentArrears_RecordedStampIgnoredWhenPaidOff — a recorded
// arrearsDueAt an evaluation stamped BEFORE a since-posted payment must not
// resurrect arrears once every debit it named is retired; recorded-wins only
// applies over an actually open balance (mirrors cmd/wellness-app's own
// `if balance > 0` gate before recordedOrDerivedDueDate).
func TestDeriveRentArrears_RecordedStampIgnoredWhenPaidOff(t *testing.T) {
	rows := []ledgerEntryRow{
		{TransactionKey: "t1", Type: "debit", AmountCents: 100000, PostedAt: "2026-08-01T00:00:00Z"},
		{TransactionKey: "t2", Type: "credit", AmountCents: 100000, PostedAt: "2026-08-05T00:00:00Z"},
	}
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	got := deriveRentArrears(rows, "2026-08-01T00:00:00Z", "", now)
	if got.DueDate != "" {
		t.Errorf("DueDate = %q, want empty — a stale recorded stamp must not outlive the payment that closed the episode", got.DueDate)
	}
}

// TestDeriveRentArrears_ReminderSentAtThreadsFromSource — reminderSentAt is
// asserted equal to its SOURCE value (the leaseAccounts row's own
// arrearsReminderSentAt), not merely non-empty — a threading bug that
// dropped or overwrote it would still look "sent" if only checked for
// non-emptiness.
func TestDeriveRentArrears_ReminderSentAtThreadsFromSource(t *testing.T) {
	const source = "2026-09-08T00:00:00Z"
	rows := []ledgerEntryRow{
		{TransactionKey: "t1", Type: "debit", AmountCents: 100000, PostedAt: "2026-09-01T00:00:00Z"},
	}
	got := deriveRentArrears(rows, "", source, time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC))
	if got.ReminderSentAt != source {
		t.Errorf("ReminderSentAt = %q, want it equal to the source value %q", got.ReminderSentAt, source)
	}
}

// TestDeriveRentArrears_StaleReminderAcrossEpisodeBoundary_Dropped — a
// reminder sent Sep 6 for the FIRST episode, the account paid to zero Sep
// 10 (closing it), then a new charge posted Sep 12 (opening a SECOND
// episode): the recorded reminderSentAt (and the recorded arrearsDueAt
// riding the same stale evaluation) predate the new episode's own start
// and must not be read as describing it — the statement would otherwise
// say "a reminder was sent" for a charge nothing has ever reminded about,
// ahead of the next EvaluateLoftspaceArrears evaluation clearing them for
// real.
func TestDeriveRentArrears_StaleReminderAcrossEpisodeBoundary_Dropped(t *testing.T) {
	rows := []ledgerEntryRow{
		{TransactionKey: "a", Type: "debit", AmountCents: 100000, PostedAt: "2026-09-01T00:00:00Z", DueAt: "2026-09-01T00:00:00Z"},
		{TransactionKey: "pay", Type: "credit", AmountCents: 100000, PostedAt: "2026-09-10T00:00:00Z"},
		{TransactionKey: "b", Type: "debit", AmountCents: 50000, PostedAt: "2026-09-12T00:00:00Z", DueAt: "2026-09-12T00:00:00Z"},
	}
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	got := deriveRentArrears(rows, "2026-09-01T00:00:00Z", "2026-09-06T00:00:00Z", now)
	if got.ReminderSentAt != "" {
		t.Errorf("ReminderSentAt = %q, want empty — the reminder (Sep 6) predates the new episode's start (Sep 12)", got.ReminderSentAt)
	}
	if got.DueDate != "2026-09-12T00:00:00Z" {
		t.Errorf("DueDate = %q, want the NEW head's own recorded due (Sep 12) — the stale recorded stamp (Sep 1) must not win", got.DueDate)
	}
}

// TestDeriveRentArrears_ReminderKeptWhenAccountNeverSquared — the account
// never closed its episode (charge A only ever partially paid, then charge
// B posts on top of the still-open remainder): the episode's start stays
// A's own postedAt, so a reminder sent well after it is not stale and must
// still be threaded through.
func TestDeriveRentArrears_ReminderKeptWhenAccountNeverSquared(t *testing.T) {
	rows := []ledgerEntryRow{
		{TransactionKey: "a", Type: "debit", AmountCents: 100000, PostedAt: "2026-08-01T00:00:00Z", DueAt: "2026-08-01T00:00:00Z"},
		{TransactionKey: "partial", Type: "credit", AmountCents: 30000, PostedAt: "2026-08-15T00:00:00Z"},
		{TransactionKey: "b", Type: "debit", AmountCents: 20000, PostedAt: "2026-10-01T00:00:00Z", DueAt: "2026-10-01T00:00:00Z"},
	}
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	const reminder = "2026-09-06T00:00:00Z"
	got := deriveRentArrears(rows, "", reminder, now)
	if got.ReminderSentAt != reminder {
		t.Errorf("ReminderSentAt = %q, want it kept (%q) — the account was never square, so this is still the current episode", got.ReminderSentAt, reminder)
	}
}

// TestDeriveRentArrears_ReminderEqualToEpisodeStart_Kept — the boundary is
// STRICTLY earlier than the episode start; a reminder recorded at exactly
// the episode's own opening instant still belongs to it.
func TestDeriveRentArrears_ReminderEqualToEpisodeStart_Kept(t *testing.T) {
	rows := []ledgerEntryRow{
		{TransactionKey: "a", Type: "debit", AmountCents: 100000, PostedAt: "2026-09-01T00:00:00Z", DueAt: "2026-09-01T00:00:00Z"},
	}
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	const reminder = "2026-09-01T00:00:00Z"
	got := deriveRentArrears(rows, "", reminder, now)
	if got.ReminderSentAt != reminder {
		t.Errorf("ReminderSentAt = %q, want it kept (%q) — equal to the episode start is not STRICTLY earlier", got.ReminderSentAt, reminder)
	}
}

// TestComputeRentOverdue_ExactInstantIsOverdue pins the boundary against
// EvaluateLoftspaceArrears' own evaluation (`remindAt <= evaluatedAt`): a
// balance is due AT the due instant, not only strictly past it, and
// daysOverdue floors whole days rather than rounding up like
// cmd/wellness-app's own computeOverdue does — 0 whole days have elapsed
// exactly at the instant.
func TestComputeRentOverdue_ExactInstantIsOverdue(t *testing.T) {
	due := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	dueDate := due.Format(time.RFC3339)

	if overdue, days, until := computeRentOverdue(dueDate, due); !overdue || days != 0 || until != 0 {
		t.Errorf("at the exact due instant: overdue=%v days=%d until=%d, want overdue=true days=0 until=0", overdue, days, until)
	}
	// One second BEFORE the due instant must still read as not overdue —
	// the boundary is at the instant, not a tick earlier.
	if overdue, _, _ := computeRentOverdue(dueDate, due.Add(-time.Second)); overdue {
		t.Errorf("one second before the due instant: overdue=%v, want false", overdue)
	}
}

// TestComputeRentOverdue_OneSecondAfterIsNotOverdue proves a due date one
// second in the FUTURE is not yet overdue and reads daysUntilDue=0 (less
// than a whole day away), and a due date exactly one day away reads
// daysUntilDue=1.
func TestComputeRentOverdue_OneSecondAfterIsNotOverdue(t *testing.T) {
	now := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	future := now.Add(time.Second).Format(time.RFC3339)
	if overdue, _, until := computeRentOverdue(future, now); overdue || until != 0 {
		t.Errorf("due one second in the future: overdue=%v until=%d, want overdue=false until=0", overdue, until)
	}
	dayAway := now.AddDate(0, 0, 1).Format(time.RFC3339)
	if overdue, _, until := computeRentOverdue(dayAway, now); overdue || until != 1 {
		t.Errorf("due exactly one day away: overdue=%v until=%d, want overdue=false until=1", overdue, until)
	}
}

// TestComputeRentOverdue_FloorsWholeDays proves daysOverdue floors rather
// than rounds — 9 days and 23 hours overdue still reads 9, not 10.
func TestComputeRentOverdue_FloorsWholeDays(t *testing.T) {
	due := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	now := due.AddDate(0, 0, 9).Add(23 * time.Hour)
	if overdue, days, _ := computeRentOverdue(due.Format(time.RFC3339), now); !overdue || days != 9 {
		t.Errorf("9 days 23 hours overdue: overdue=%v days=%d, want overdue=true days=9 (floored)", overdue, days)
	}
}

// TestComputeRentOverdue_MalformedDueDateFailsClosed mirrors the FIFO
// derivation's own posture on an unopened head: a due date that fails to
// parse is neither overdue nor upcoming.
func TestComputeRentOverdue_MalformedDueDateFailsClosed(t *testing.T) {
	if overdue, days, until := computeRentOverdue("not-a-date", time.Now()); overdue || days != 0 || until != 0 {
		t.Errorf("malformed dueDate: want fail-closed, got overdue=%v days=%d until=%d", overdue, days, until)
	}
}
