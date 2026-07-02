package main

import "testing"

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
