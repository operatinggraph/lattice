package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	wellnessledger "github.com/operatinggraph/lattice/packages/wellness-ledger"
)

// seedLedgerAccount seeds one wellnessMemberAccounts row — keyed by the
// identity itself (memberAccountsSpec, packages/wellness-ledger/lenses.go).
func seedLedgerAccount(t *testing.T, s *server, identityKey, accountKey string) {
	t.Helper()
	putJSON(t, s.conn, wellnessledger.MemberAccountsBucket, identityKey, map[string]any{
		"identityKey": identityKey,
		"accountKey":  accountKey,
	})
}

// seedLedgerTransaction seeds one wellnessLedgerHistory row for identityKey.
func seedLedgerTransaction(t *testing.T, s *server, transactionKey, accountKey, identityKey, txType string, amountCents float64) {
	t.Helper()
	putJSON(t, s.conn, wellnessledger.LedgerHistoryBucket, transactionKey, map[string]any{
		"transactionKey": transactionKey,
		"accountKey":     accountKey,
		"identityKey":    identityKey,
		"type":           txType,
		"amountCents":    amountCents,
		"postedAt":       "2026-08-01T10:00:00Z",
	})
}

func decodeLedger(t *testing.T, rec *httptest.ResponseRecorder) struct {
	AccountKey   string           `json:"accountKey"`
	Transactions []ledgerEntryRow `json:"transactions"`
	BalanceCents int64            `json:"balanceCents"`
} {
	t.Helper()
	var body struct {
		AccountKey   string           `json:"accountKey"`
		Transactions []ledgerEntryRow `json:"transactions"`
		BalanceCents int64            `json:"balanceCents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode ledger: %v (body=%s)", err, rec.Body.String())
	}
	return body
}

// A member sees only their own charges/payments and the running balance
// derived from them — never another member's, mirroring
// TestHandleBookings_MemberSeesOnlyOwnBookings' scoping proof.
func TestHandleLedger_MemberSeesOnlyOwnHistory(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	identityA := "vtx.identity." + memberA
	identityB := "vtx.identity." + memberB
	seedLedgerAccount(t, s, identityA, "vtx.wellnessaccount.aaaaaaaaaaaaaaaaaaaa")
	seedLedgerAccount(t, s, identityB, "vtx.wellnessaccount.bbbbbbbbbbbbbbbbbbbb")
	seedLedgerTransaction(t, s, "vtx.wellnesstransaction.aaaaaaaaaaaaaaaaaaaa", "vtx.wellnessaccount.aaaaaaaaaaaaaaaaaaaa", identityA, "debit", 1500)
	seedLedgerTransaction(t, s, "vtx.wellnesstransaction.bbbbbbbbbbbbbbbbbbbb", "vtx.wellnessaccount.bbbbbbbbbbbbbbbbbbbb", identityB, "debit", 9900)

	rec := sessionGET(s, s.handleLedger, "/api/ledger", cookieFor(memberA))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := decodeLedger(t, rec)
	if body.AccountKey != "vtx.wellnessaccount.aaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("accountKey = %q, want memberA's own account", body.AccountKey)
	}
	if len(body.Transactions) != 1 || body.Transactions[0].AmountCents != 1500 {
		t.Fatalf("transactions = %+v, want exactly memberA's own $15.00 debit", body.Transactions)
	}
	if body.BalanceCents != 1500 {
		t.Errorf("balanceCents = %d, want 1500 (memberB's $99.00 charge must not leak in)", body.BalanceCents)
	}
}

// A member who has never had a ledger account opened for them (the standing
// gap — CreateAccount is grantsTo-operator-only, the browser has no grant to
// open one itself) gets a clean empty answer, not an error.
func TestHandleLedger_MemberWithNoAccountSeesEmpty(t *testing.T) {
	s, cookieFor := devSessionServer(t)

	rec := sessionGET(s, s.handleLedger, "/api/ledger", cookieFor(memberA))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := decodeLedger(t, rec)
	if body.AccountKey != "" {
		t.Errorf("accountKey = %q, want empty (no account opened yet)", body.AccountKey)
	}
	if len(body.Transactions) != 0 || body.BalanceCents != 0 {
		t.Errorf("got transactions=%+v balanceCents=%d, want empty/zero", body.Transactions, body.BalanceCents)
	}
}
