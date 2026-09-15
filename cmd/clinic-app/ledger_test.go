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
	_, err := readAllOrFail([]string{"vtx.clinictransaction.1", "vtx.clinictransaction.2"}, func(key string) ([]byte, error) {
		if key == "vtx.clinictransaction.2" {
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
		"vtx.clinictransaction.1": map[string]any{"transactionKey": "vtx.clinictransaction.1", "accountKey": "vtx.clinicaccount.ppp", "patientKey": "vtx.patient.ppp", "type": "debit", "amountCents": 15000, "memo": "Copay", "postedAt": "2026-06-01T00:00:00Z"},
		"vtx.clinictransaction.2": map[string]any{"transactionKey": "vtx.clinictransaction.2", "accountKey": "vtx.clinicaccount.ppp", "patientKey": "vtx.patient.ppp", "type": "credit", "amountCents": 10000, "memo": "Partial payment", "postedAt": "2026-06-05T00:00:00Z"},
		// a different patient's transaction — must not leak into this patient's rows/balance
		"vtx.clinictransaction.3": map[string]any{"transactionKey": "vtx.clinictransaction.3", "accountKey": "vtx.clinicaccount.other", "patientKey": "vtx.patient.other", "type": "debit", "amountCents": 99999, "postedAt": "2026-06-01T00:00:00Z"},
		// a tombstoned / undecodable projection entry — skipped
		"vtx.clinictransaction.4": map[string]any{},
	})

	rows, balance := computeLedgerHistory(keys, get, "vtx.patient.ppp")
	if len(rows) != 2 {
		t.Fatalf("want 2 rows for the patient, got %d (%+v)", len(rows), rows)
	}
	if rows[0].TransactionKey != "vtx.clinictransaction.1" || rows[1].TransactionKey != "vtx.clinictransaction.2" {
		t.Errorf("want chronological order (1, 2), got (%s, %s)", rows[0].TransactionKey, rows[1].TransactionKey)
	}
	if balance != 5000 {
		t.Errorf("balance: want 15000-10000=5000, got %d", balance)
	}
}

func TestComputeLedgerHistory_CarriesAppointmentTie(t *testing.T) {
	keys, get := fakeKV(map[string]any{
		"vtx.clinictransaction.5": map[string]any{"transactionKey": "vtx.clinictransaction.5", "accountKey": "vtx.clinicaccount.ppp", "patientKey": "vtx.patient.ppp", "type": "debit", "amountCents": 2500, "memo": "No-show fee", "postedAt": "2026-08-06T00:00:00Z", "appointmentKey": "vtx.appointment.a1", "visitStartsAt": "2026-08-05T09:00:00Z"},
		// a copay settles no appointment — the tie fields are just empty, not an error
		"vtx.clinictransaction.6": map[string]any{"transactionKey": "vtx.clinictransaction.6", "accountKey": "vtx.clinicaccount.ppp", "patientKey": "vtx.patient.ppp", "type": "debit", "amountCents": 15000, "memo": "Copay", "postedAt": "2026-08-06T00:00:00Z"},
	})

	rows, _ := computeLedgerHistory(keys, get, "vtx.patient.ppp")
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d (%+v)", len(rows), rows)
	}
	var noShow, copay ledgerEntryRow
	for _, r := range rows {
		if r.TransactionKey == "vtx.clinictransaction.5" {
			noShow = r
		} else {
			copay = r
		}
	}
	if noShow.AppointmentKey != "vtx.appointment.a1" || noShow.VisitStartsAt != "2026-08-05T09:00:00Z" {
		t.Errorf("no-show row: want appointment tie, got AppointmentKey=%q VisitStartsAt=%q", noShow.AppointmentKey, noShow.VisitStartsAt)
	}
	if copay.AppointmentKey != "" || copay.VisitStartsAt != "" {
		t.Errorf("copay row: want no appointment tie, got AppointmentKey=%q VisitStartsAt=%q", copay.AppointmentKey, copay.VisitStartsAt)
	}
}

// TestComputeLedgerHistory_CarriesWaiverReason proves a credit's reason
// column round-trips into the FE-facing row, and that balance treats a
// waiver exactly like a payment (both subtract) — reason is a display-only
// distinction, never an arithmetic one.
func TestComputeLedgerHistory_CarriesWaiverReason(t *testing.T) {
	keys, get := fakeKV(map[string]any{
		"vtx.clinictransaction.7": map[string]any{"transactionKey": "vtx.clinictransaction.7", "accountKey": "vtx.clinicaccount.ppp", "patientKey": "vtx.patient.ppp", "type": "debit", "amountCents": 2500, "memo": "No-show fee", "postedAt": "2026-08-01T00:00:00Z"},
		"vtx.clinictransaction.8": map[string]any{"transactionKey": "vtx.clinictransaction.8", "accountKey": "vtx.clinicaccount.ppp", "patientKey": "vtx.patient.ppp", "type": "credit", "amountCents": 2500, "memo": "Waived", "postedAt": "2026-08-02T00:00:00Z", "reason": "waiver"},
	})

	rows, balance := computeLedgerHistory(keys, get, "vtx.patient.ppp")
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d (%+v)", len(rows), rows)
	}
	if rows[1].Reason != "waiver" {
		t.Errorf("row[1].Reason = %q, want waiver", rows[1].Reason)
	}
	if rows[0].Reason != "" {
		t.Errorf("row[0].Reason (debit) = %q, want empty", rows[0].Reason)
	}
	if balance != 0 {
		t.Errorf("balance: want 2500-2500=0 (a waiver subtracts like any credit), got %d", balance)
	}
}

func TestComputeLedgerHistory_NoTransactionsZeroBalance(t *testing.T) {
	rows, balance := computeLedgerHistory(nil, func(string) ([]byte, bool) { return nil, false }, "vtx.patient.fresh")
	if len(rows) != 0 || balance != 0 {
		t.Errorf("want no rows / zero balance, got %d rows, balance=%d", len(rows), balance)
	}
}

func TestResolvePatientAccount_FindsMatchOrEmpty(t *testing.T) {
	keys, get := fakeKV(map[string]any{
		"vtx.patient.ppp":   map[string]any{"patientKey": "vtx.patient.ppp", "accountKey": "vtx.clinicaccount.xyz"},
		"vtx.patient.other": map[string]any{"patientKey": "vtx.patient.other", "accountKey": ""},
		// a tombstoned / undecodable projection entry — skipped
		"vtx.patient.bad": map[string]any{},
	})

	if got := resolvePatientAccount(keys, get, "vtx.patient.ppp"); got != "vtx.clinicaccount.xyz" {
		t.Errorf("resolvePatientAccount(ppp) = %q, want vtx.clinicaccount.xyz", got)
	}
	if got := resolvePatientAccount(keys, get, "vtx.patient.other"); got != "" {
		t.Errorf("resolvePatientAccount(other) = %q, want empty (no account opened yet)", got)
	}
	if got := resolvePatientAccount(keys, get, "vtx.patient.unprojected"); got != "" {
		t.Errorf("resolvePatientAccount(unprojected) = %q, want empty (no row at all)", got)
	}
}

// TestDeriveStatement_SingleOpenDebitPastGraceIsOverdue is the base case: one
// debit past the grace period ages both the statement AND the row itself
// identically, since the row IS the oldest (only) open charge.
func TestDeriveStatement_SingleOpenDebitPastGraceIsOverdue(t *testing.T) {
	rows := []ledgerEntryRow{{TransactionKey: "A", Type: "debit", AmountCents: 4750, PostedAt: "2026-08-01T00:00:00Z"}}
	now := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	due, overdue, days := deriveStatement(rows, 4750, now)
	if due != "2026-08-16T00:00:00Z" || !overdue || days != 14 {
		t.Fatalf("statement: due=%q overdue=%v days=%d, want 2026-08-16T00:00:00Z/true/14", due, overdue, days)
	}
	if rows[0].OpenCents != 4750 {
		t.Errorf("row OpenCents = %d, want 4750 (the whole charge is still open)", rows[0].OpenCents)
	}
	if rows[0].DueAt != "2026-08-16T00:00:00Z" {
		t.Errorf("row DueAt = %q, want 2026-08-16T00:00:00Z", rows[0].DueAt)
	}
	if !rows[0].IsOverdue || rows[0].DaysOverdue != 14 {
		t.Errorf("row IsOverdue/DaysOverdue = %v/%d, want true/14", rows[0].IsOverdue, rows[0].DaysOverdue)
	}
}

// TestDeriveStatement_ReversalRetiresItsDebitOnlyRowLevel is the café
// lockstep rule (ReversalRetiresItsOwnCharge) proven at the ROW level too: a
// reversesKey credit retires the debit it names, and ONLY that debit — an
// older, unrelated debit is untouched by the reversal and is the one that
// ages, both in the statement and in its own row annotation.
func TestDeriveStatement_ReversalRetiresItsDebitOnlyRowLevel(t *testing.T) {
	rows := []ledgerEntryRow{
		{TransactionKey: "A", Type: "debit", AmountCents: 1000, PostedAt: "2026-08-01T00:00:00Z"},
		{TransactionKey: "B", Type: "debit", AmountCents: 1000, PostedAt: "2026-08-20T00:00:00Z"},
		{TransactionKey: "C", Type: "credit", AmountCents: 1000, PostedAt: "2026-08-21T00:00:00Z", ReversesKey: "B"},
	}
	now := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	due, overdue, days := deriveStatement(rows, 1000, now)
	if due != "2026-08-16T00:00:00Z" || !overdue || days != 14 {
		t.Fatalf("statement: due=%q overdue=%v days=%d, want 2026-08-16T00:00:00Z/true/14 (A ages, B was reversed)", due, overdue, days)
	}
	if rows[0].OpenCents != 1000 || !rows[0].IsOverdue || rows[0].DaysOverdue != 14 {
		t.Errorf("row A (unrelated, untouched): OpenCents=%d IsOverdue=%v DaysOverdue=%d, want 1000/true/14",
			rows[0].OpenCents, rows[0].IsOverdue, rows[0].DaysOverdue)
	}
	if rows[1].OpenCents != 0 || rows[1].IsOverdue || rows[1].DaysOverdue != 0 {
		t.Errorf("row B (retired by the reversal): OpenCents=%d IsOverdue=%v DaysOverdue=%d, want 0/false/0",
			rows[1].OpenCents, rows[1].IsOverdue, rows[1].DaysOverdue)
	}
	if rows[1].DueAt != "2026-09-04T00:00:00Z" {
		t.Errorf("row B DueAt = %q, want 2026-09-04T00:00:00Z (a retired charge still HAD a due date)", rows[1].DueAt)
	}
}

// TestDeriveStatement_PartialPaymentFIFOLeavesTheRemainder proves a credit
// smaller than the oldest open charge's remainder retires PART of it: the
// head of the FIFO queue does not move, and OpenCents on that row shows
// exactly what is left owing, not the original face amount.
func TestDeriveStatement_PartialPaymentFIFOLeavesTheRemainder(t *testing.T) {
	rows := []ledgerEntryRow{
		{TransactionKey: "A", Type: "debit", AmountCents: 1000, PostedAt: "2026-08-01T00:00:00Z"},
		{TransactionKey: "B", Type: "debit", AmountCents: 700, PostedAt: "2026-08-20T00:00:00Z"},
		{TransactionKey: "C", Type: "credit", AmountCents: 400, PostedAt: "2026-08-21T00:00:00Z"},
	}
	now := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	due, overdue, days := deriveStatement(rows, 1300, now)
	if due != "2026-08-16T00:00:00Z" || !overdue || days != 14 {
		t.Fatalf("statement: due=%q overdue=%v days=%d, want 2026-08-16T00:00:00Z/true/14", due, overdue, days)
	}
	if rows[0].OpenCents != 600 {
		t.Errorf("row A OpenCents = %d, want 600 (1000 - the 400 partial payment)", rows[0].OpenCents)
	}
	if !rows[0].IsOverdue || rows[0].DaysOverdue != 14 {
		t.Errorf("row A IsOverdue/DaysOverdue = %v/%d, want true/14 (still the aged head)", rows[0].IsOverdue, rows[0].DaysOverdue)
	}
	if rows[1].OpenCents != 700 {
		t.Errorf("row B OpenCents = %d, want 700 (untouched, the payment never reached it)", rows[1].OpenCents)
	}
	if rows[1].IsOverdue {
		t.Errorf("row B: want not overdue yet (within its own 15-day grace from Aug 20)")
	}
}

// TestDeriveStatement_SurplusPrepaysTheNextDebit is café's
// PrepaidCreditCarriesForward proven at the row level: a credit posted
// before any debit exists carries forward as surplus and fully prepays the
// very next debit — that debit's OpenCents is 0 even though its own grace
// period has long since elapsed, because it was never actually left owing.
func TestDeriveStatement_SurplusPrepaysTheNextDebit(t *testing.T) {
	rows := []ledgerEntryRow{
		{TransactionKey: "C0", Type: "credit", AmountCents: 1000, PostedAt: "2026-08-01T00:00:00Z"},
		{TransactionKey: "D1", Type: "debit", AmountCents: 1000, PostedAt: "2026-08-02T00:00:00Z"},
		{TransactionKey: "D2", Type: "debit", AmountCents: 1425, PostedAt: "2026-08-28T23:50:00Z"},
	}
	now := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	due, overdue, days := deriveStatement(rows, 1425, now)
	if due != "2026-09-12T23:50:00Z" || overdue || days != 0 {
		t.Fatalf("statement: due=%q overdue=%v days=%d, want 2026-09-12T23:50:00Z/false/0", due, overdue, days)
	}
	if rows[1].OpenCents != 0 {
		t.Errorf("row D1 OpenCents = %d, want 0 (fully prepaid by the Aug 1 surplus)", rows[1].OpenCents)
	}
	if rows[1].IsOverdue {
		t.Error("row D1: want not overdue — prepaid, even though its own grace period has elapsed")
	}
	if rows[2].OpenCents != 1425 {
		t.Errorf("row D2 OpenCents = %d, want 1425 (the surviving open charge)", rows[2].OpenCents)
	}
}

// TestDeriveStatement_CreditBalanceZeroesEveryDebitButKeepsDueAt proves the
// balanceCents<=0 branch still annotates DueAt on every debit (a paid charge
// still HAD a due date — the render decides whether that matters) while
// zeroing OpenCents/IsOverdue/DaysOverdue, and the statement itself carries
// no due date at all.
func TestDeriveStatement_CreditBalanceZeroesEveryDebitButKeepsDueAt(t *testing.T) {
	rows := []ledgerEntryRow{{TransactionKey: "A", Type: "debit", AmountCents: 1000, PostedAt: "2026-08-01T00:00:00Z"}}
	now := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	due, overdue, days := deriveStatement(rows, -500, now)
	if due != "" || overdue || days != 0 {
		t.Fatalf("statement: due=%q overdue=%v days=%d, want empty/false/0 for a credit balance", due, overdue, days)
	}
	if rows[0].OpenCents != 0 || rows[0].IsOverdue || rows[0].DaysOverdue != 0 {
		t.Errorf("row: OpenCents=%d IsOverdue=%v DaysOverdue=%d, want 0/false/0", rows[0].OpenCents, rows[0].IsOverdue, rows[0].DaysOverdue)
	}
	if rows[0].DueAt != "2026-08-16T00:00:00Z" {
		t.Errorf("row DueAt = %q, want 2026-08-16T00:00:00Z (still annotated, even though nothing is open)", rows[0].DueAt)
	}
}

// TestDeriveStatement_MalformedPostedAtFailsClosedRowAndStatement proves a
// malformed postedAt fails closed at BOTH levels: the statement carries no
// due date (café's existing rule, unchanged), and the row itself gets no
// DueAt and is never marked overdue — even though it is still open (the
// amount is still owed; only the DATE is unknowable).
func TestDeriveStatement_MalformedPostedAtFailsClosedRowAndStatement(t *testing.T) {
	rows := []ledgerEntryRow{{TransactionKey: "A", Type: "debit", AmountCents: 4750, PostedAt: "not-a-date"}}
	due, overdue, days := deriveStatement(rows, 4750, time.Now())
	if due != "" || overdue || days != 0 {
		t.Fatalf("statement: due=%q overdue=%v days=%d, want empty/false/0", due, overdue, days)
	}
	if rows[0].DueAt != "" {
		t.Errorf("row DueAt = %q, want empty (postedAt does not parse)", rows[0].DueAt)
	}
	if rows[0].IsOverdue || rows[0].DaysOverdue != 0 {
		t.Errorf("row IsOverdue/DaysOverdue = %v/%d, want false/0", rows[0].IsOverdue, rows[0].DaysOverdue)
	}
	if rows[0].OpenCents != 4750 {
		t.Errorf("row OpenCents = %d, want 4750 (still owed — only the date failed, not the amount)", rows[0].OpenCents)
	}
}

// TestDeriveStatement_DueAtIsExactlyFifteenDaysAfterPosting pins the grace
// period's arithmetic directly, independent of any overdue/FIFO behavior.
func TestDeriveStatement_DueAtIsExactlyFifteenDaysAfterPosting(t *testing.T) {
	rows := []ledgerEntryRow{{TransactionKey: "A", Type: "debit", AmountCents: 100, PostedAt: "2026-01-01T12:00:00Z"}}
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	if _, _, _ = deriveStatement(rows, 100, now); rows[0].DueAt != "2026-01-16T12:00:00Z" {
		t.Errorf("DueAt = %q, want 2026-01-16T12:00:00Z (postedAt + 15 days exactly)", rows[0].DueAt)
	}
}

// TestComputeArrears_GroupsFiltersAndOrdersWorstFirst is the pure grouping
// function GET /api/staff/arrears renders from: it must (1) confine to the
// visible set (an invisible patient's balance never surfaces, no matter how
// large), (2) drop a patient whose balance nets to zero (nothing is open),
// and (3) sort worst-first — isOverdue desc, then daysOverdue desc, then
// balanceCents desc — so a patient with the largest balance but NOT overdue
// ranks behind every overdue patient regardless of amount.
func TestComputeArrears_GroupsFiltersAndOrdersWorstFirst(t *testing.T) {
	keys, get := fakeKV(map[string]any{
		// p1: overdue 14 days, balance 1000.
		"vtx.clinictransaction.p1a": map[string]any{"transactionKey": "vtx.clinictransaction.p1a", "patientKey": "vtx.patient.p1", "type": "debit", "amountCents": 1000, "postedAt": "2026-08-01T00:00:00Z"},
		// p2: within grace, NOT overdue, balance 5000 (the largest balance of all).
		"vtx.clinictransaction.p2a": map[string]any{"transactionKey": "vtx.clinictransaction.p2a", "patientKey": "vtx.patient.p2", "type": "debit", "amountCents": 5000, "postedAt": "2026-08-20T00:00:00Z"},
		// p3: overdue 45 days, balance 2000 — the most overdue of all.
		"vtx.clinictransaction.p3a": map[string]any{"transactionKey": "vtx.clinictransaction.p3a", "patientKey": "vtx.patient.p3", "type": "debit", "amountCents": 2000, "postedAt": "2026-07-01T00:00:00Z"},
		// p4: NOT in the visible set — must never surface, despite the biggest balance of all.
		"vtx.clinictransaction.p4a": map[string]any{"transactionKey": "vtx.clinictransaction.p4a", "patientKey": "vtx.patient.p4", "type": "debit", "amountCents": 9999999, "postedAt": "2026-01-01T00:00:00Z"},
		// p5: visible, but fully paid — nets to zero, must be dropped.
		"vtx.clinictransaction.p5a": map[string]any{"transactionKey": "vtx.clinictransaction.p5a", "patientKey": "vtx.patient.p5", "type": "debit", "amountCents": 500, "postedAt": "2026-08-01T00:00:00Z"},
		"vtx.clinictransaction.p5b": map[string]any{"transactionKey": "vtx.clinictransaction.p5b", "patientKey": "vtx.patient.p5", "type": "credit", "amountCents": 500, "postedAt": "2026-08-02T00:00:00Z"},
		// a tombstoned / undecodable projection entry — skipped.
		"vtx.clinictransaction.bad": map[string]any{},
	})
	visible := map[string]bool{"vtx.patient.p1": true, "vtx.patient.p2": true, "vtx.patient.p3": true, "vtx.patient.p5": true}
	acctByPatient := map[string]string{
		"vtx.patient.p1": "vtx.clinicaccount.a1",
		"vtx.patient.p2": "vtx.clinicaccount.a2",
		"vtx.patient.p3": "vtx.clinicaccount.a3",
	}
	now := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)

	rows := computeArrears(keys, get, visible, acctByPatient, now)
	if len(rows) != 3 {
		t.Fatalf("want 3 arrears rows (p4 invisible, p5 paid off), got %d: %+v", len(rows), rows)
	}
	want := []string{"vtx.patient.p3", "vtx.patient.p1", "vtx.patient.p2"}
	for i, w := range want {
		if rows[i].PatientKey != w {
			t.Errorf("rows[%d].PatientKey = %q, want %q (worst-first: p3 45d overdue, p1 14d overdue, p2 not overdue despite the largest balance)", i, rows[i].PatientKey, w)
		}
	}
	if rows[0].BalanceCents != 2000 || rows[0].AccountKey != "vtx.clinicaccount.a3" || !rows[0].IsOverdue || rows[0].DaysOverdue != 45 {
		t.Errorf("rows[0] (p3) = %+v, want balance=2000 account=a3 overdue=true days=45", rows[0])
	}
	if rows[2].IsOverdue {
		t.Errorf("rows[2] (p2) IsOverdue = true, want false (within grace)")
	}
}

func TestResolvePatientAccounts_IndexesEveryPatient(t *testing.T) {
	keys, get := fakeKV(map[string]any{
		"vtx.patient.ppp":   map[string]any{"patientKey": "vtx.patient.ppp", "accountKey": "vtx.clinicaccount.xyz"},
		"vtx.patient.other": map[string]any{"patientKey": "vtx.patient.other", "accountKey": ""},
		"vtx.patient.bad":   map[string]any{},
	})
	got := resolvePatientAccounts(keys, get)
	if got["vtx.patient.ppp"] != "vtx.clinicaccount.xyz" {
		t.Errorf("ppp = %q, want vtx.clinicaccount.xyz", got["vtx.patient.ppp"])
	}
	if _, ok := got["vtx.patient.bad"]; ok {
		t.Errorf("want the tombstoned entry absent from the index, got %q", got["vtx.patient.bad"])
	}
}
