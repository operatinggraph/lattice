package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	wellnessledger "github.com/operatinggraph/lattice/packages/wellness-ledger"
)

// TestReadAllOrFail_FailsLoudOnAnyFetchError proves a KVGet failure on a
// listed key aborts the whole read instead of silently vanishing the row —
// the bug that let a transient fetch failure produce a wrong balance.
func TestReadAllOrFail_FailsLoudOnAnyFetchError(t *testing.T) {
	boom := errors.New("boom")
	_, err := readAllOrFail([]string{"vtx.wellnesstransaction.1", "vtx.wellnesstransaction.2"}, func(key string) ([]byte, error) {
		if key == "vtx.wellnesstransaction.2" {
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

// A no-show fee's className/classStartsAt (wellnessLedgerHistory's settles
// hop, packages/wellness-ledger/lenses.go) carry through to the FE row, so a
// member's billing history can tell two otherwise-identical "No-show fee"
// lines apart by which class each one billed.
func TestHandleLedger_NoShowFeeNamesItsClass(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	identityA := "vtx.identity." + memberA
	seedLedgerAccount(t, s, identityA, "vtx.wellnessaccount.aaaaaaaaaaaaaaaaaaaa")
	putJSON(t, s.conn, wellnessledger.LedgerHistoryBucket, "vtx.wellnesstransaction.aaaaaaaaaaaaaaaaaaaa", map[string]any{
		"transactionKey": "vtx.wellnesstransaction.aaaaaaaaaaaaaaaaaaaa",
		"accountKey":     "vtx.wellnessaccount.aaaaaaaaaaaaaaaaaaaa",
		"identityKey":    identityA,
		"type":           "debit",
		"amountCents":    2500,
		"memo":           "No-show fee",
		"postedAt":       "2026-08-21T00:00:00Z",
		"bookingKey":     "vtx.booking.cccccccccccccccccccc",
		"className":      "Vinyasa Flow",
		"classStartsAt":  "2026-08-19T09:00:00Z",
	})

	rec := sessionGET(s, s.handleLedger, "/api/ledger", cookieFor(memberA))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := decodeLedger(t, rec)
	if len(body.Transactions) != 1 {
		t.Fatalf("transactions = %+v, want exactly one", body.Transactions)
	}
	tx := body.Transactions[0]
	if tx.ClassName != "Vinyasa Flow" || tx.ClassStartsAt != "2026-08-19T09:00:00Z" {
		t.Errorf("got className=%q classStartsAt=%q, want the settled booking's class", tx.ClassName, tx.ClassStartsAt)
	}
}

// A plain payment (no settled booking) carries no class name, not an error.
func TestHandleLedger_PaymentCarriesNoClassName(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	identityA := "vtx.identity." + memberA
	seedLedgerAccount(t, s, identityA, "vtx.wellnessaccount.aaaaaaaaaaaaaaaaaaaa")
	seedLedgerTransaction(t, s, "vtx.wellnesstransaction.aaaaaaaaaaaaaaaaaaaa", "vtx.wellnessaccount.aaaaaaaaaaaaaaaaaaaa", identityA, "credit", 5000)

	rec := sessionGET(s, s.handleLedger, "/api/ledger", cookieFor(memberA))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := decodeLedger(t, rec)
	if len(body.Transactions) != 1 || body.Transactions[0].ClassName != "" {
		t.Fatalf("transactions = %+v, want one row with no className", body.Transactions)
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

// A GUEST — billed for a class, holding no lease — is reachable through the
// booking that billed them. This is the money surface the gap left unsettleable:
// the charge posts, and no hat can open the ledger it posted to.
func TestHandleLedger_StaffReadsCoveredGuestLedger(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedBooker(t, s.conn, guestBookingHere, memberB)
	identityB := "vtx.identity." + memberB
	seedLedgerAccount(t, s, identityB, "vtx.wellnessaccount.bbbbbbbbbbbbbbbbbbbb")
	seedLedgerTransaction(t, s, "vtx.wellnesstransaction.bbbbbbbbbbbbbbbbbbbb", "vtx.wellnessaccount.bbbbbbbbbbbbbbbbbbbb", identityB, "debit", 1500)

	rec := sessionGET(s, s.handleLedger, "/api/ledger?identityKey="+identityB, cookieFor(staffSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the class they booked is at this staffer's building; body=%s",
			rec.Code, rec.Body.String())
	}
}

// The discriminating half of the guest read: a guest whose only booking is at
// another building stays invisible, so the booking widens the directory by
// exactly one person's own class, never platform-wide.
func TestHandleLedger_StaffCannotReadGuestBookedElsewhere(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedBooker(t, s.conn, guestBookingElsewhere, memberB, otherWorkplace)
	identityB := "vtx.identity." + memberB
	seedLedgerAccount(t, s, identityB, "vtx.wellnessaccount.bbbbbbbbbbbbbbbbbbbb")

	rec := sessionGET(s, s.handleLedger, "/api/ledger?identityKey="+identityB, cookieFor(staffSubj))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — this staffer's workplace covers no class of theirs; body=%s",
			rec.Code, rec.Body.String())
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

// ---- GET /api/frontdesk-arrears: the front desk's arrears grid ----

func decodeArrears(t *testing.T, rec *httptest.ResponseRecorder) []balanceRow {
	t.Helper()
	var body struct {
		Arrears []balanceRow `json:"arrears"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode arrears: %v (body=%s)", err, rec.Body.String())
	}
	return body.Arrears
}

// The arrears grid is a per-user read, so it needs a session like every
// other one — mirrors TestHandleMembers_RefusesWithNoSession.
func TestHandleFrontDeskArrears_Unauthenticated_401(t *testing.T) {
	s, _ := devSessionServer(t)

	rec := muxGET(s, "/api/frontdesk-arrears", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 — /api/frontdesk-arrears must not be on the public-read exemption list; body=%s",
			rec.Code, rec.Body.String())
	}
}

// A member reads no arrears grid, not even one they'd appear in — mirrors
// TestHandleMembers_MemberForbidden.
func TestHandleFrontDeskArrears_Resident_403(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedMember(t, s.conn, leaseHere, memberA)

	rec := sessionGET(s, s.handleFrontDeskArrears, "/api/frontdesk-arrears", cookieFor(memberA))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — a member reads no arrears grid; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleFrontDeskArrears_Staff_200(t *testing.T) {
	s, cookieFor := devSessionServer(t)

	rec := sessionGET(s, s.handleFrontDeskArrears, "/api/frontdesk-arrears", cookieFor(staffSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for staff; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleFrontDeskArrears_OverdueOmitPaidAndConfinement proves the
// grouped balance computation: (a) an old unpaid debit ages past the
// statement grace period into an overdue balance with the right
// daysOverdue, (b) a member whose debit is fully offset by a credit —
// balance <= 0 — is omitted from the response entirely rather than coming
// back with balanceCents 0, and (c) staff only see arrears for members their
// workplace covers, mirroring café's
// TestHandleFrontDeskBalances_OverdueOmitPaidAndConfinement.
func TestHandleFrontDeskArrears_OverdueOmitPaidAndConfinement(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	old := time.Now().UTC().AddDate(0, 0, -(statementGraceDays + 5)).Format(time.RFC3339)

	// memberA: covered by staffSubj's own workplace, with an old unpaid debit
	// — must come back overdue.
	seedMember(t, s.conn, leaseHere, memberA)
	identityA := "vtx.identity." + memberA
	putJSON(t, s.conn, wellnessledger.LedgerHistoryBucket, "vtx.wellnesstransaction.aaaaaaaaaaaaaaaaaaaa", map[string]any{
		"transactionKey": "vtx.wellnesstransaction.aaaaaaaaaaaaaaaaaaaa", "accountKey": "vtx.wellnessaccount.aaaaaaaaaaaaaaaaaaaa",
		"identityKey": identityA, "type": "debit", "amountCents": 4500.0, "memo": "Class package", "postedAt": old,
	})

	// memberB: also covered, but an old debit fully offset by a credit —
	// balance <= 0 — must be omitted entirely.
	seedMember(t, s.conn, "vtx.leaseapp.pB4mQtZbXvNqK7wHdYct", memberB)
	identityB := "vtx.identity." + memberB
	putJSON(t, s.conn, wellnessledger.LedgerHistoryBucket, "vtx.wellnesstransaction.bbbbbbbbbbbbbbbbbbbb", map[string]any{
		"transactionKey": "vtx.wellnesstransaction.bbbbbbbbbbbbbbbbbbbb", "accountKey": "vtx.wellnessaccount.bbbbbbbbbbbbbbbbbbbb",
		"identityKey": identityB, "type": "debit", "amountCents": 2000.0, "memo": "Class package", "postedAt": old,
	})
	putJSON(t, s.conn, wellnessledger.LedgerHistoryBucket, "vtx.wellnesstransaction.cccccccccccccccccccc", map[string]any{
		"transactionKey": "vtx.wellnesstransaction.cccccccccccccccccccc", "accountKey": "vtx.wellnessaccount.bbbbbbbbbbbbbbbbbbbb",
		"identityKey": identityB, "type": "credit", "amountCents": 2000.0, "memo": "Paid", "postedAt": time.Now().UTC().Format(time.RFC3339),
	})

	// memberC: an old unpaid debit at a building this staffer does NOT work
	// at — must never appear in their arrears grid.
	otherSubj := "kkkkkkkkkkkkkkkkkkkk"
	seedMember(t, s.conn, "vtx.leaseapp.eR3nKpXvZmBtQ7wHdYcg", otherSubj, otherWorkplace)
	identityC := "vtx.identity." + otherSubj
	putJSON(t, s.conn, wellnessledger.LedgerHistoryBucket, "vtx.wellnesstransaction.dddddddddddddddddddd", map[string]any{
		"transactionKey": "vtx.wellnesstransaction.dddddddddddddddddddd", "accountKey": "vtx.wellnessaccount.dddddddddddddddddddd",
		"identityKey": identityC, "type": "debit", "amountCents": 9900.0, "memo": "Class package", "postedAt": old,
	})

	rec := sessionGET(s, s.handleFrontDeskArrears, "/api/frontdesk-arrears", cookieFor(staffSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	rows := decodeArrears(t, rec)
	if len(rows) != 1 || rows[0].IdentityKey != identityA {
		t.Fatalf("arrears = %+v, want exactly the overdue covered member (paid-off and foreign members omitted)", rows)
	}
	row := rows[0]
	if row.BalanceCents != 4500 {
		t.Fatalf("balanceCents = %d, want 4500", row.BalanceCents)
	}
	if !row.IsOverdue {
		t.Fatalf("isOverdue = false, want true for a debit %d days old (grace period is %d days)",
			statementGraceDays+5, statementGraceDays)
	}
	if row.DaysOverdue < 1 {
		t.Fatalf("daysOverdue = %d, want >= 1", row.DaysOverdue)
	}
}

// TestHandleFrontDeskArrears_IncludesGuestDebtors proves the grid counts a
// debtor reached by BOOKING as well as one reached by lease, and still refuses
// a guest whose class is at another building — the same discriminating pair
// the picker's own tests use. A guest missing from this grid is money the desk
// is never shown.
func TestHandleFrontDeskArrears_IncludesGuestDebtors(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	old := time.Now().UTC().AddDate(0, 0, -(statementGraceDays + 5)).Format(time.RFC3339)

	seedMember(t, s.conn, leaseHere, memberA)
	identityA := "vtx.identity." + memberA
	putJSON(t, s.conn, wellnessledger.LedgerHistoryBucket, "vtx.wellnesstransaction.aaaaaaaaaaaaaaaaaaaa", map[string]any{
		"transactionKey": "vtx.wellnesstransaction.aaaaaaaaaaaaaaaaaaaa", "accountKey": "vtx.wellnessaccount.aaaaaaaaaaaaaaaaaaaa",
		"identityKey": identityA, "type": "debit", "amountCents": 4500.0, "memo": "Class package", "postedAt": old,
	})

	// A guest with no lease, owing on a class at THIS staffer's building.
	seedBooker(t, s.conn, guestBookingHere, memberB)
	identityB := "vtx.identity." + memberB
	putJSON(t, s.conn, wellnessledger.LedgerHistoryBucket, "vtx.wellnesstransaction.bbbbbbbbbbbbbbbbbbbb", map[string]any{
		"transactionKey": "vtx.wellnesstransaction.bbbbbbbbbbbbbbbbbbbb", "accountKey": "vtx.wellnessaccount.bbbbbbbbbbbbbbbbbbbb",
		"identityKey": identityB, "type": "debit", "amountCents": 1500.0, "memo": "Vinyasa Flow", "postedAt": old,
	})

	// A guest owing on a class at another building — never this desk's.
	otherGuest := "kkkkkkkkkkkkkkkkkkkk"
	seedBooker(t, s.conn, guestBookingElsewhere, otherGuest, otherWorkplace)
	identityC := "vtx.identity." + otherGuest
	putJSON(t, s.conn, wellnessledger.LedgerHistoryBucket, "vtx.wellnesstransaction.dddddddddddddddddddd", map[string]any{
		"transactionKey": "vtx.wellnesstransaction.dddddddddddddddddddd", "accountKey": "vtx.wellnessaccount.dddddddddddddddddddd",
		"identityKey": identityC, "type": "debit", "amountCents": 9900.0, "memo": "Class package", "postedAt": old,
	})

	rec := sessionGET(s, s.handleFrontDeskArrears, "/api/frontdesk-arrears", cookieFor(staffSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	rows := decodeArrears(t, rec)
	got := make(map[string]bool, len(rows))
	for _, r := range rows {
		got[r.IdentityKey] = true
	}
	if len(rows) != 2 || !got[identityA] || !got[identityB] {
		t.Fatalf("arrears = %+v, want the covered member AND the covered guest, and only those", rows)
	}
}

// TestHandleFrontDeskArrears_SortsWorstFirst proves the staff-triage sort:
// isOverdue descending, then daysOverdue descending, then balanceCents
// descending.
func TestHandleFrontDeskArrears_SortsWorstFirst(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	now := time.Now().UTC()

	// memberA: barely overdue (grace + 1 day) — the least severe of the two
	// overdue members.
	seedMember(t, s.conn, leaseHere, memberA)
	identityA := "vtx.identity." + memberA
	putJSON(t, s.conn, wellnessledger.LedgerHistoryBucket, "vtx.wellnesstransaction.eeeeeeeeeeeeeeeeeeee", map[string]any{
		"transactionKey": "vtx.wellnesstransaction.eeeeeeeeeeeeeeeeeeee", "accountKey": "vtx.wellnessaccount.eeeeeeeeeeeeeeeeeeee",
		"identityKey": identityA, "type": "debit", "amountCents": 1000.0, "memo": "Class package",
		"postedAt": now.AddDate(0, 0, -(statementGraceDays + 1)).Format(time.RFC3339),
	})

	// memberB: far more overdue (grace + 20 days) and a larger balance — must
	// sort first.
	seedMember(t, s.conn, "vtx.leaseapp.mB8nWpKrXvMqY3LdHcyt", memberB)
	identityB := "vtx.identity." + memberB
	putJSON(t, s.conn, wellnessledger.LedgerHistoryBucket, "vtx.wellnesstransaction.ffffffffffffffffffff", map[string]any{
		"transactionKey": "vtx.wellnesstransaction.ffffffffffffffffffff", "accountKey": "vtx.wellnessaccount.ffffffffffffffffffff",
		"identityKey": identityB, "type": "debit", "amountCents": 8000.0, "memo": "Class package",
		"postedAt": now.AddDate(0, 0, -(statementGraceDays + 20)).Format(time.RFC3339),
	})

	// memberC: owes money but is NOT yet overdue — must sort last.
	notOverdueSubj := "mmmmmmmmmmmmmmmmmmmm"
	seedMember(t, s.conn, "vtx.leaseapp.nT8mWpKrXvMqY3LdHcyg", notOverdueSubj)
	identityNotOverdue := "vtx.identity." + notOverdueSubj
	putJSON(t, s.conn, wellnessledger.LedgerHistoryBucket, "vtx.wellnesstransaction.gggggggggggggggggggg", map[string]any{
		"transactionKey": "vtx.wellnesstransaction.gggggggggggggggggggg", "accountKey": "vtx.wellnessaccount.gggggggggggggggggggg",
		"identityKey": identityNotOverdue, "type": "debit", "amountCents": 500.0, "memo": "Class package",
		"postedAt": now.Format(time.RFC3339),
	})

	rec := sessionGET(s, s.handleFrontDeskArrears, "/api/frontdesk-arrears", cookieFor(staffSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	rows := decodeArrears(t, rec)
	if len(rows) != 3 {
		t.Fatalf("arrears = %+v, want all 3 covered members (2 overdue + 1 not-yet-due)", rows)
	}
	if rows[0].IdentityKey != identityB {
		t.Fatalf("rows[0] = %+v, want memberB (most overdue) first", rows[0])
	}
	if rows[1].IdentityKey != identityA {
		t.Fatalf("rows[1] = %+v, want memberA (less overdue) second", rows[1])
	}
	if rows[2].IdentityKey != identityNotOverdue {
		t.Fatalf("rows[2] = %+v, want the not-yet-overdue member last", rows[2])
	}
}
