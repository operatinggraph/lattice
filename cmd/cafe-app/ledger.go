package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	cafeledger "github.com/operatinggraph/lattice/packages/cafe-ledger"
)

// statementGraceDays is the net term between a charge posting and it counting
// overdue.
const statementGraceDays = 15

// ledgerEntryProjection is one row of the cafe-ledger `cafeLedgerHistory` lens.
type ledgerEntryProjection struct {
	TransactionKey string   `json:"transactionKey"`
	AccountKey     string   `json:"accountKey"`
	LeaseAppKey    string   `json:"leaseAppKey"`
	Type           string   `json:"type"`
	AmountCents    *float64 `json:"amountCents"`
	Memo           string   `json:"memo"`
	PostedAt       string   `json:"postedAt"`
	ReversesKey    string   `json:"reversesKey"`
	TabKey         string   `json:"tabKey"`
}

// ledgerEntryRow is the posted-charge-history row the resident house-tab view
// renders. ReversesKey is set only on a refund and names the charge it gives
// back — the statement reads it to say a line is a correction rather than a
// payment the resident made, since the entry itself is an ordinary credit.
// TabKey is set only on a charge the tab-settlement playbook posted, and is
// what tells the front desk which debits can be refunded at all.
type ledgerEntryRow struct {
	TransactionKey string `json:"transactionKey"`
	Type           string `json:"type"`
	AmountCents    int64  `json:"amountCents"`
	Memo           string `json:"memo,omitempty"`
	PostedAt       string `json:"postedAt"`
	ReversesKey    string `json:"reversesKey,omitempty"`
	TabKey         string `json:"tabKey,omitempty"`
}

// computeLedgerHistory filters the cafeLedgerHistory lens rows to one lease,
// sorts them chronologically, and derives the running balance in cents (sum
// debits − sum credits) — the ledger stores no running total (append-only,
// D5). A row that fails to decode or carries no transactionKey (a
// tombstoned projection entry) is skipped.
func computeLedgerHistory(keys []string, get kvGetter, leaseAppKey string) ([]ledgerEntryRow, int64) {
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
		if p.LeaseAppKey != leaseAppKey {
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
			ReversesKey:    p.ReversesKey,
			TabKey:         p.TabKey,
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

// balanceRow is one lease's balance/due-date/overdue statement for the
// front-desk grid — the same shape deriveStatement computes for the resident
// ledger view, but for every lease the front desk is confined to instead of
// the one leaseAppKey a resident names.
type balanceRow struct {
	LeaseAppKey  string `json:"leaseAppKey"`
	BalanceCents int64  `json:"balanceCents"`
	DueDate      string `json:"dueDate"`
	IsOverdue    bool   `json:"isOverdue"`
	DaysOverdue  int    `json:"daysOverdue"`
}

// computeLedgerBalances groups the cafeLedgerHistory lens rows by
// leaseAppKey and derives each lease's balance/due-date/overdue state in one
// pass — the front-desk grid needs every visible lease's statement at once,
// and re-running computeLedgerHistory's per-lease scan once per lease found
// in the bucket would redecode the same rows N times over. A lease whose
// balance settles to zero or credit is left out of the result entirely —
// deriveStatement's own "nothing to age" case for one lease, applied per
// group.
func computeLedgerBalances(keys []string, get kvGetter, now time.Time) []balanceRow {
	byLease := make(map[string][]ledgerEntryRow)
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p ledgerEntryProjection
		if json.Unmarshal(raw, &p) != nil || p.TransactionKey == "" || p.LeaseAppKey == "" {
			continue
		}
		var amount int64
		if p.AmountCents != nil {
			amount = int64(*p.AmountCents)
		}
		byLease[p.LeaseAppKey] = append(byLease[p.LeaseAppKey], ledgerEntryRow{
			TransactionKey: p.TransactionKey,
			Type:           p.Type,
			AmountCents:    amount,
			Memo:           p.Memo,
			PostedAt:       p.PostedAt,
		})
	}

	rows := make([]balanceRow, 0, len(byLease))
	for leaseAppKey, entries := range byLease {
		sortLedgerRows(entries)
		balance := sumBalance(entries)
		if balance <= 0 {
			continue
		}
		dueDate, isOverdue, daysOverdue := deriveStatement(entries, balance, now)
		rows = append(rows, balanceRow{
			LeaseAppKey:  leaseAppKey,
			BalanceCents: balance,
			DueDate:      dueDate,
			IsOverdue:    isOverdue,
			DaysOverdue:  daysOverdue,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].LeaseAppKey < rows[j].LeaseAppKey })
	return rows
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

// resolveLeaseAccount scans the cafeLeaseAccounts lens rows for the one
// matching leaseAppKey, returning its account key ("" if the lease has none
// yet).
func resolveLeaseAccount(keys []string, get kvGetter, leaseAppKey string) string {
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p leaseAccountProjection
		if json.Unmarshal(raw, &p) != nil || p.LeaseAppKey != leaseAppKey {
			continue
		}
		return p.AccountKey
	}
	return ""
}

// handleLedger implements GET /api/ledger?leaseAppKey= — the resident
// house-tab's posted charge history, served from the cafeLedgerHistory +
// cafeLeaseAccounts lenses (P5). A `worksAt` staffer may read the ledger of a
// lease their workplace covers (facet-staff-worlds-design.md §9); a resident
// may only name a lease they hold (persona-worlds-design.md Fire W4 §3).
func (s *server) handleLedger(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	hats, err := s.resolveSubjectHats(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	leaseAppKey := strings.TrimSpace(r.URL.Query().Get("leaseAppKey"))
	if leaseAppKey == "" {
		s.writeError(w, http.StatusBadRequest, "leaseAppKey query param is required")
		return
	}

	ctx, cancel := s.reqContext(r)
	defer cancel()

	visible, err := s.visibleLeases(ctx, hats)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if !visible.admits(leaseAppKey) {
		s.writeError(w, http.StatusForbidden, notYourLease(hats))
		return
	}

	acctBucket := cafeledger.LeaseAccountsBucket
	acctKeys, err := conn.KVListKeys(ctx, acctBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+acctBucket+": "+err.Error()+" (is cafe-ledger installed and the Refractor projecting?)")
		return
	}
	acctValues, err := readAllOrFail(acctKeys, func(key string) ([]byte, error) {
		entry, err := conn.KVGet(ctx, acctBucket, key)
		if err != nil {
			return nil, err
		}
		return entry.Value, nil
	})
	if err != nil {
		s.logger.Error("read lease accounts for ledger", "bucket", acctBucket, "error", err)
		s.writeError(w, http.StatusBadGateway, "read "+acctBucket+" incomplete: "+err.Error())
		return
	}
	acctGet := func(key string) ([]byte, bool) { v, ok := acctValues[key]; return v, ok }
	accountKey := resolveLeaseAccount(acctKeys, acctGet, leaseAppKey)

	bucket := cafeledger.LedgerHistoryBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bucket+": "+err.Error()+" (is cafe-ledger installed and the Refractor projecting?)")
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
	rows, balance := computeLedgerHistory(keys, get, leaseAppKey)
	dueDate, isOverdue, daysOverdue := deriveStatement(rows, balance, time.Now().UTC())
	s.writeJSON(w, http.StatusOK, map[string]any{
		"leaseAppKey":  leaseAppKey,
		"accountKey":   accountKey,
		"transactions": rows,
		"balanceCents": balance,
		"dueDate":      dueDate,
		"isOverdue":    isOverdue,
		"daysOverdue":  daysOverdue,
	})
}
