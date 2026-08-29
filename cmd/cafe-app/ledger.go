package main

import (
	"encoding/json"
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
}

// ledgerEntryRow is the posted-charge-history row the resident house-tab view
// renders.
type ledgerEntryRow struct {
	TransactionKey string `json:"transactionKey"`
	Type           string `json:"type"`
	AmountCents    int64  `json:"amountCents"`
	Memo           string `json:"memo,omitempty"`
	PostedAt       string `json:"postedAt"`
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
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].PostedAt != rows[j].PostedAt {
			return rows[i].PostedAt < rows[j].PostedAt
		}
		return rows[i].TransactionKey < rows[j].TransactionKey
	})
	var balance int64
	for _, r := range rows {
		switch r.Type {
		case "debit":
			balance += r.AmountCents
		case "credit":
			balance -= r.AmountCents
		}
	}
	return rows, balance
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
	accountKey := resolveLeaseAccount(acctKeys, s.kvGetter(ctx, acctBucket), leaseAppKey)

	bucket := cafeledger.LedgerHistoryBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bucket+": "+err.Error()+" (is cafe-ledger installed and the Refractor projecting?)")
		return
	}
	rows, balance := computeLedgerHistory(keys, s.kvGetter(ctx, bucket), leaseAppKey)
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
