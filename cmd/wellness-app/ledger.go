package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/substrate"
	wellnessledger "github.com/operatinggraph/lattice/packages/wellness-ledger"
)

// statementGraceDays is the net term between a charge posting and it counting
// overdue. Wellness has no existing due-date/grace-period policy of its own
// to contradict, so this adopts cmd/cafe-app/ledger.go's own 15-day term for
// cross-vertical consistency rather than inventing a second number.
const statementGraceDays = 15

// ledgerEntryProjection is one row of the wellness-ledger `wellnessLedgerHistory`
// lens, read from its NATS-KV read-model bucket (P5 — never Core KV).
// BookingKey/ClassName/ClassStartsAt are empty for the common transaction (a
// payment or a class-price charge settles no booking) — only a no-show
// debit's settles hop populates them, which is what lets the FE tell two
// otherwise-identical "No-show fee" lines apart by the class each one billed.
type ledgerEntryProjection struct {
	TransactionKey string   `json:"transactionKey"`
	AccountKey     string   `json:"accountKey"`
	IdentityKey    string   `json:"identityKey"`
	Type           string   `json:"type"`
	AmountCents    *float64 `json:"amountCents"`
	Memo           string   `json:"memo"`
	PostedAt       string   `json:"postedAt"`
	BookingKey     string   `json:"bookingKey"`
	ClassName      string   `json:"className"`
	ClassStartsAt  string   `json:"classStartsAt"`
	Reason         string   `json:"reason"`
}

// ledgerEntryRow is the billing-history row the FE renders. Reason is empty
// for a debit (charge-only rows never carry it) and "payment"/"waiver" for a
// credit — the FE labels a waiver distinctly so forgiven debt is never
// mistaken for cash collected.
type ledgerEntryRow struct {
	TransactionKey string `json:"transactionKey"`
	Type           string `json:"type"`
	AmountCents    int64  `json:"amountCents"`
	Memo           string `json:"memo,omitempty"`
	PostedAt       string `json:"postedAt"`
	ClassName      string `json:"className,omitempty"`
	ClassStartsAt  string `json:"classStartsAt,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

// memberAccountProjection is one row of the wellness-ledger `wellnessMemberAccounts`
// lens. Unlike ledgerHistory (keyed by transaction), this lens is keyed by the
// identity itself (memberAccountsSpec, packages/wellness-ledger/lenses.go), so
// resolving one member's account is a single scoped KVGet, never a bucket scan.
type memberAccountProjection struct {
	IdentityKey string `json:"identityKey"`
	AccountKey  string `json:"accountKey"`
}

// computeLedgerHistory filters the wellnessLedgerHistory lens rows to one
// member, sorts them chronologically, and derives the running balance in
// cents (sum debits − sum credits) — the ledger itself stores no running
// total (append-only, D5), so the FE-facing balance is always assembled from
// the full transaction set. A row that fails to decode or carries no
// transactionKey (a tombstoned projection entry) is skipped.
func computeLedgerHistory(keys []string, get kvGetter, identityKey string) ([]ledgerEntryRow, int64) {
	rows := make([]ledgerEntryRow, 0)
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p ledgerEntryProjection
		if json.Unmarshal(raw, &p) != nil || p.TransactionKey == "" {
			continue
		}
		if p.IdentityKey != identityKey {
			continue
		}
		var amount int64
		if p.AmountCents != nil {
			amount = int64(*p.AmountCents)
		}
		rows = append(rows, ledgerEntryRow{
			TransactionKey: p.TransactionKey,
			Type:           p.Type,
			AmountCents:    amount,
			Memo:           p.Memo,
			PostedAt:       p.PostedAt,
			ClassName:      p.ClassName,
			ClassStartsAt:  p.ClassStartsAt,
			Reason:         p.Reason,
		})
	}
	sortLedgerRows(rows)
	return rows, sumBalance(rows)
}

// sortLedgerRows sorts ledger rows chronologically (postedAt, then
// transactionKey as the tiebreaker) — the order every FIFO-aging computation
// in this file depends on.
func sortLedgerRows(rows []ledgerEntryRow) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].PostedAt != rows[j].PostedAt {
			return rows[i].PostedAt < rows[j].PostedAt
		}
		return rows[i].TransactionKey < rows[j].TransactionKey
	})
}

// sumBalance sums a chronologically-sorted ledger's running balance in cents
// (sum debits − sum credits) — the ledger stores no running total
// (append-only, D5).
func sumBalance(rows []ledgerEntryRow) int64 {
	var balance int64
	for _, r := range rows {
		switch r.Type {
		case "debit":
			balance += r.AmountCents
		case "credit":
			balance -= r.AmountCents
		}
	}
	return balance
}

// balanceRow is one member's balance/due-date/overdue statement for the
// front-desk arrears grid — the same shape deriveStatement computes for the
// member's own ledger view, but for every member the front desk is confined
// to instead of the one identityKey a member names.
type balanceRow struct {
	IdentityKey  string `json:"identityKey"`
	BalanceCents int64  `json:"balanceCents"`
	DueDate      string `json:"dueDate"`
	IsOverdue    bool   `json:"isOverdue"`
	DaysOverdue  int    `json:"daysOverdue"`
}

// computeLedgerBalances groups the wellnessLedgerHistory lens rows by
// identityKey and derives each member's balance/due-date/overdue state in
// one pass — the front-desk arrears grid needs every visible member's
// statement at once, and re-running computeLedgerHistory's per-member scan
// once per member found in the bucket would redecode the same rows N times
// over. A member whose balance settles to zero or credit is left out of the
// result entirely — deriveStatement's own "nothing to age" case for one
// member, applied per group.
func computeLedgerBalances(keys []string, get kvGetter, now time.Time) []balanceRow {
	byIdentity := make(map[string][]ledgerEntryRow)
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p ledgerEntryProjection
		if json.Unmarshal(raw, &p) != nil || p.TransactionKey == "" || p.IdentityKey == "" {
			continue
		}
		var amount int64
		if p.AmountCents != nil {
			amount = int64(*p.AmountCents)
		}
		byIdentity[p.IdentityKey] = append(byIdentity[p.IdentityKey], ledgerEntryRow{
			TransactionKey: p.TransactionKey,
			Type:           p.Type,
			AmountCents:    amount,
			Memo:           p.Memo,
			PostedAt:       p.PostedAt,
			ClassName:      p.ClassName,
			ClassStartsAt:  p.ClassStartsAt,
			Reason:         p.Reason,
		})
	}

	rows := make([]balanceRow, 0, len(byIdentity))
	for identityKey, entries := range byIdentity {
		sortLedgerRows(entries)
		balance := sumBalance(entries)
		if balance <= 0 {
			continue
		}
		dueDate, isOverdue, daysOverdue := deriveStatement(entries, balance, now)
		rows = append(rows, balanceRow{
			IdentityKey:  identityKey,
			BalanceCents: balance,
			DueDate:      dueDate,
			IsOverdue:    isOverdue,
			DaysOverdue:  daysOverdue,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].IdentityKey < rows[j].IdentityKey })
	return rows
}

// deriveStatement turns a chronologically-sorted ledger into a due date and
// overdue state, without the ledger ever storing either: credits offset the
// OLDEST still-open debit first (FIFO aging, mirroring how a real statement
// ages a balance), so the survivor at the front of the queue is the charge
// that has actually been sitting unpaid the longest — not just the most
// recent charge. A zero/credit balance has nothing to age and returns no due
// date. A malformed postedAt on the oldest open debit fails closed (no due
// date) rather than guessing.
func deriveStatement(rows []ledgerEntryRow, balanceCents int64, now time.Time) (dueDate string, isOverdue bool, daysOverdue int) {
	if balanceCents <= 0 {
		return "", false, 0
	}
	type openDebit struct {
		postedAt  string
		remaining int64
	}
	var open []openDebit
	for _, r := range rows {
		switch r.Type {
		case "debit":
			open = append(open, openDebit{postedAt: r.PostedAt, remaining: r.AmountCents})
		case "credit":
			remaining := r.AmountCents
			for remaining > 0 && len(open) > 0 {
				if open[0].remaining > remaining {
					open[0].remaining -= remaining
					remaining = 0
				} else {
					remaining -= open[0].remaining
					open = open[1:]
				}
			}
		}
	}
	if len(open) == 0 {
		return "", false, 0
	}
	oldest, err := time.Parse(time.RFC3339, open[0].postedAt)
	if err != nil {
		return "", false, 0
	}
	due := oldest.AddDate(0, 0, statementGraceDays)
	if !now.After(due) {
		return due.Format(time.RFC3339), false, 0
	}
	days := int(now.Sub(due).Hours()/24) + 1
	return due.Format(time.RFC3339), true, days
}

// rawGetter reads one bucket entry's raw bytes for a key, erroring on a real
// fetch failure (distinct from kvGetter's bool, which conflates "no such
// row" with "the read failed").
type rawGetter func(key string) ([]byte, error)

// readAllOrFail fetches every listed key via get, failing on the first
// error instead of silently treating a fetch failure as "no such row." A
// key just came back from a KVListKeys call on the same bucket, so a
// KVGet error on it is a real projection/network fault, not evidence the
// row doesn't exist — letting it fall through as absent is what silently
// dropped ledger lines and produced a wrong balance.
func readAllOrFail(keys []string, get rawGetter) (map[string][]byte, error) {
	values := make(map[string][]byte, len(keys))
	for _, k := range keys {
		v, err := get(k)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		values[k] = v
	}
	return values, nil
}

// memberVisibleToHats reports whether targetIdentityKey is somebody hats reach
// — reuses the exact directory /api/members' picker is served from
// (coveredMembers, residents.go: the lease rows and the guest rows alike) as
// the ledger's staff-read gate, rather than standing up a second confinement
// mechanism for the same fact. Fails CLOSED throughout that helper: an
// operator sees everyone, a staffer sees only the people their `worksAt`
// location covers, and a caller with no workplace covers nobody.
func (s *server) memberVisibleToHats(ctx context.Context, hats subjectHats, targetIdentityKey string) (bool, error) {
	rows, err := s.coveredMembers(ctx, s.conn, hats)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.BookerKey == targetIdentityKey {
			return true, nil
		}
	}
	return false, nil
}

// handleLedger implements GET /api/ledger — the signed-in member's own
// billing history by default, served from the wellnessLedgerHistory +
// wellnessMemberAccounts lens read models (P5, never Core KV). Whose ledger
// this is comes from the verified session unless the caller supplies
// ?identityKey=, which is gated to staff (isStaff/isOperator) AND checked
// against memberVisibleToHats above — the same confinement /api/members
// already enforces for its picker, not a fresh unauthenticated party filter
// (that vector is the one clinic closed when it deleted
// `/api/appointments?provider=` — the fix there was adding a visibility
// check, not banning party filters outright; clinic's own `/api/ledger?
// patientKey=` proves the pattern, gated by Postgres RLS instead of a lens).
// wellness-app's Roster billing panel needs this to record a front-desk
// charge/payment against a member OTHER than the signed-in session.
//
// Returns the member's transaction rows, the running balance, and the
// account key — empty when no wellnessaccount has been opened for them yet.
// A blank accountKey is the normal, expected shape for a member who hasn't
// been charged yet, not an error.
func (s *server) handleLedger(w http.ResponseWriter, r *http.Request) {
	subject, err := s.authenticateRead(r)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	identityKey := auth.IdentityKeyPrefix + subject
	if target := strings.TrimSpace(r.URL.Query().Get("identityKey")); target != "" && target != identityKey {
		hats, err := s.resolveSubjectHats(r)
		if err != nil {
			s.writeAuthError(w, err)
			return
		}
		if !hats.isFrontDesk() && !hats.isOperator {
			s.writeError(w, http.StatusForbidden,
				"another member's ledger is a staff surface for the place you work at")
			return
		}
		visible, err := s.memberVisibleToHats(ctx, hats, target)
		if err != nil {
			s.logger.Error("check member visibility for ledger read", "error", err)
			s.writeError(w, http.StatusBadGateway, "could not verify read access")
			return
		}
		if !visible {
			s.writeError(w, http.StatusForbidden, "member not visible to this actor")
			return
		}
		identityKey = target
	}

	var accountKey string
	entry, err := conn.KVGet(ctx, wellnessledger.MemberAccountsBucket, identityKey)
	switch {
	case err == nil:
		var acct memberAccountProjection
		if json.Unmarshal(entry.Value, &acct) == nil {
			accountKey = acct.AccountKey
		}
	case errors.Is(err, substrate.ErrKeyNotFound):
		// No row yet — this identity has never booked, or the Refractor
		// hasn't caught up; accountKey stays empty, not an error.
	default:
		s.writeError(w, http.StatusBadGateway,
			"read "+wellnessledger.MemberAccountsBucket+": "+err.Error()+" (is wellness-ledger installed and the Refractor projecting?)")
		return
	}

	bucket := wellnessledger.LedgerHistoryBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bucket+": "+err.Error()+" (is wellness-ledger installed and the Refractor projecting?)")
		return
	}
	ledgerValues, err := readAllOrFail(keys, func(key string) ([]byte, error) {
		entry, err := conn.KVGet(ctx, bucket, key)
		if err != nil {
			return nil, err
		}
		return entry.Value, nil
	})
	if err != nil {
		s.logger.Error("read ledger history", "bucket", bucket, "error", err)
		s.writeError(w, http.StatusBadGateway, "read "+bucket+" incomplete: "+err.Error())
		return
	}
	get := func(key string) ([]byte, bool) { v, ok := ledgerValues[key]; return v, ok }
	rows, balance := computeLedgerHistory(keys, get, identityKey)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"identityKey":  identityKey,
		"accountKey":   accountKey,
		"transactions": rows,
		"balanceCents": balance,
	})
}

// handleFrontDeskArrears implements GET /api/frontdesk-arrears — every
// covered member's balance/due-date/overdue statement for the front desk's
// billing panel, served from the wellnessLedgerHistory lens (P5,
// computeLedgerBalances above). Staff-only and workplace-confined, same gate
// handleMembers uses (coveredMembers, residents.go) — this handler adds no new
// authority, only a new affordance over the people already visible to this
// caller's picker.
//
// A missing directory bucket is a real 502 (it is the confinement source,
// residents.go's own reasoning) — and unlike café's front-desk joins,
// wellness-ledger is a core wellness package
// always installed alongside wellness-domain, not an optional cross-vertical
// join, so a missing LedgerHistoryBucket is also a 502, not a best-effort
// empty answer.
//
// Every key from LedgerHistoryBucket is read via readAllOrFail rather than
// the lossy per-key kvGetter handleMembers uses for its own bucket: a
// balance is money, and a KVGet error on a key that just came back from
// KVListKeys is a real fetch fault, not evidence the row doesn't exist
// (readAllOrFail's own doc comment) — letting it fall through as absent
// would silently understate a member's balance instead of failing the
// request.
//
// Results sort worst-first (isOverdue desc, then daysOverdue desc, then
// balanceCents desc) — staff triage priority, so the most overdue,
// longest-overdue, highest-amount cases surface first in the grid.
func (s *server) handleFrontDeskArrears(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	hats, err := s.resolveSubjectHats(r)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	if !hats.isFrontDesk() && !hats.isOperator {
		s.writeError(w, http.StatusForbidden,
			"the arrears grid is a front-desk surface for the place you work at")
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	members, err := s.coveredMembers(ctx, conn, hats)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	covered := make(map[string]bool, len(members))
	for _, m := range members {
		covered[m.BookerKey] = true
	}

	bucket := wellnessledger.LedgerHistoryBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bucket+": "+err.Error()+" (is wellness-ledger installed and the Refractor projecting?)")
		return
	}
	values, err := readAllOrFail(keys, func(key string) ([]byte, error) {
		entry, err := conn.KVGet(ctx, bucket, key)
		if err != nil {
			return nil, err
		}
		return entry.Value, nil
	})
	if err != nil {
		s.logger.Error("read ledger history for front-desk arrears", "bucket", bucket, "error", err)
		s.writeError(w, http.StatusBadGateway, "read "+bucket+" incomplete: "+err.Error())
		return
	}
	get := func(key string) ([]byte, bool) { v, ok := values[key]; return v, ok }
	rows := computeLedgerBalances(keys, get, time.Now().UTC())
	filtered := make([]balanceRow, 0, len(rows))
	for _, row := range rows {
		if covered[row.IdentityKey] {
			filtered = append(filtered, row)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].IsOverdue != filtered[j].IsOverdue {
			return filtered[i].IsOverdue
		}
		if filtered[i].DaysOverdue != filtered[j].DaysOverdue {
			return filtered[i].DaysOverdue > filtered[j].DaysOverdue
		}
		return filtered[i].BalanceCents > filtered[j].BalanceCents
	})
	s.writeJSON(w, http.StatusOK, map[string]any{"arrears": filtered})
}
