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
	IdentityKey  string           `json:"identityKey"`
	AccountKey   string           `json:"accountKey"`
	Transactions []ledgerEntryRow `json:"transactions"`
	BalanceCents int64            `json:"balanceCents"`
} {
	t.Helper()
	var body struct {
		IdentityKey  string           `json:"identityKey"`
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

// ---- GET /api/ledger?identityKey=: the front-desk staff read ----
//
// wellness-app's Roster billing panel needs to look up a MEMBER's ledger, not
// just the signed-in session's own. Gated on isStaff/isOperator plus
// memberVisibleToHats (ledger.go) — the same wellnessMembers-lens confinement
// TestHandleMembers_* above already proves for the picker, reused here rather
// than a second mechanism.

// A staffer whose workplace covers the member reads their ledger.
func TestHandleLedger_StaffReadsCoveredMemberLedger(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedMember(t, s.conn, leaseHere, memberA)
	identityA := "vtx.identity." + memberA
	seedLedgerAccount(t, s, identityA, "vtx.wellnessaccount.aaaaaaaaaaaaaaaaaaaa")
	seedLedgerTransaction(t, s, "vtx.wellnesstransaction.aaaaaaaaaaaaaaaaaaaa", "vtx.wellnessaccount.aaaaaaaaaaaaaaaaaaaa", identityA, "debit", 1500)

	rec := sessionGET(s, s.handleLedger, "/api/ledger?identityKey="+identityA, cookieFor(staffSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := decodeLedger(t, rec)
	if body.IdentityKey != identityA {
		t.Errorf("identityKey = %q, want %q", body.IdentityKey, identityA)
	}
	if body.AccountKey != "vtx.wellnessaccount.aaaaaaaaaaaaaaaaaaaa" || body.BalanceCents != 1500 {
		t.Fatalf("got %+v, want memberA's own account + $15.00 balance", body)
	}
}

// A staffer whose workplace does NOT cover the member is refused — the same
// confinement /api/members enforces for its picker, not a fresh open filter.
func TestHandleLedger_StaffCannotReadUncoveredMemberLedger(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedMember(t, s.conn, leaseElsewhere, memberB, otherWorkplace)
	identityB := "vtx.identity." + memberB
	seedLedgerAccount(t, s, identityB, "vtx.wellnessaccount.bbbbbbbbbbbbbbbbbbbb")

	rec := sessionGET(s, s.handleLedger, "/api/ledger?identityKey="+identityB, cookieFor(staffSubj))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — this staffer's workplace does not cover memberB; body=%s", rec.Code, rec.Body.String())
	}
}

// A member — no staff hat at all — cannot read another member's ledger by
// naming it in ?identityKey=, even if that member happens to share no
// workplace confinement at all (the gate is isStaff/isOperator, checked
// before visibility).
func TestHandleLedger_MemberCannotReadAnothersLedger(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedMember(t, s.conn, leaseHere, memberB)
	identityB := "vtx.identity." + memberB
	seedLedgerAccount(t, s, identityB, "vtx.wellnessaccount.bbbbbbbbbbbbbbbbbbbb")

	rec := sessionGET(s, s.handleLedger, "/api/ledger?identityKey="+identityB, cookieFor(memberA))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — a member reads no ledger but their own; body=%s", rec.Code, rec.Body.String())
	}
}

// The operator holds no workplace at all and reads any member's ledger — the
// same confinement exemption TestHandleMembers_OperatorSeesEveryMember proves
// for the picker.
func TestHandleLedger_OperatorReadsAnyMemberLedger(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedMember(t, s.conn, leaseElsewhere, memberB, otherWorkplace)
	identityB := "vtx.identity." + memberB
	seedLedgerAccount(t, s, identityB, "vtx.wellnessaccount.bbbbbbbbbbbbbbbbbbbb")

	rec := sessionGET(s, s.handleLedger, "/api/ledger?identityKey="+identityB, cookieFor(rootSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — root is exempt from workplace confinement; body=%s", rec.Code, rec.Body.String())
	}
	if body := decodeLedger(t, rec); body.AccountKey != "vtx.wellnessaccount.bbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("got %+v, want memberB's account", body)
	}
}

// Naming your OWN identityKey in the query param is equivalent to the plain
// self-service GET, even for a caller with no staff hat — the gate only
// fires when the target differs from the session's own subject.
func TestHandleLedger_MemberNamingOwnIdentityKeyStillSelfService(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	identityA := "vtx.identity." + memberA
	seedLedgerAccount(t, s, identityA, "vtx.wellnessaccount.aaaaaaaaaaaaaaaaaaaa")

	rec := sessionGET(s, s.handleLedger, "/api/ledger?identityKey="+identityA, cookieFor(memberA))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — naming your own identity is still self-service; body=%s", rec.Code, rec.Body.String())
	}
}
