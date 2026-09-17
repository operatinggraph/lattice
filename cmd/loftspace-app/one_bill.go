package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	loftspaceledger "github.com/operatinggraph/lattice/packages/loftspace-ledger"
	onebill "github.com/operatinggraph/lattice/packages/one-bill"
)

// oneBillEntryProjection is one row of the shared `one-bill-history` lens
// bucket (packages/one-bill) — a rent OR café transaction, tagged by source.
type oneBillEntryProjection struct {
	TransactionKey string   `json:"transactionKey"`
	AccountKey     string   `json:"accountKey"`
	LeaseAppKey    string   `json:"leaseAppKey"`
	Type           string   `json:"type"`
	AmountCents    *float64 `json:"amountCents"`
	Memo           string   `json:"memo"`
	PostedAt       string   `json:"postedAt"`
	// PeriodStart/PeriodEnd/DueAt: a recurring charge's recorded billing period
	// and due date (empty on a payment or a one-time charge).
	PeriodStart string `json:"periodStart"`
	PeriodEnd   string `json:"periodEnd"`
	DueAt       string `json:"dueAt"`
	Source      string `json:"source"`
	// ClausePurpose is the authorizing clause's own recorded purpose token
	// (rentEntriesSpec's authorizedBy hop, packages/one-bill) — "deposit"
	// for a security-deposit charge or its return, empty otherwise
	// (including on every café/clinic/wellness row, which never authorizes
	// off a semantic-contracts clause at all).
	ClausePurpose string `json:"clausePurpose"`
	// Kind is the entry's own recorded provenance (loftspace-ledger's
	// PayOutBalance stamps kind:"payout"); empty on every other entry and on
	// every non-rent source.
	Kind string `json:"kind"`
}

// oneBillEntryRow is the statement row the FE renders.
type oneBillEntryRow struct {
	TransactionKey string `json:"transactionKey"`
	Type           string `json:"type"`
	AmountCents    int64  `json:"amountCents"`
	Memo           string `json:"memo,omitempty"`
	PostedAt       string `json:"postedAt"`
	PeriodStart    string `json:"periodStart,omitempty"`
	PeriodEnd      string `json:"periodEnd,omitempty"`
	DueAt          string `json:"dueAt,omitempty"`
	Source         string `json:"source"`
	ClausePurpose  string `json:"clausePurpose,omitempty"`
	Kind           string `json:"kind,omitempty"`
}

// computeOneBillHistory filters the one-bill-history lens rows to one lease,
// sorts them chronologically, and derives the combined rent+café running
// balance in cents — mirrors computeLedgerHistory (ledger.go); ClauseKey/
// ClauseProse ("why was I charged this?") are the one clause field the
// one-bill lens still does not project, unlike ClausePurpose.
func computeOneBillHistory(keys []string, get kvGetter, leaseAppKey string) ([]oneBillEntryRow, int64) {
	rows := make([]oneBillEntryRow, 0)
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p oneBillEntryProjection
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
		rows = append(rows, oneBillEntryRow{
			TransactionKey: p.TransactionKey,
			Type:           p.Type,
			AmountCents:    amount,
			Memo:           p.Memo,
			PostedAt:       p.PostedAt,
			PeriodStart:    p.PeriodStart,
			PeriodEnd:      p.PeriodEnd,
			DueAt:          p.DueAt,
			Source:         p.Source,
			ClausePurpose:  p.ClausePurpose,
			Kind:           p.Kind,
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

// handleOneBillStatement implements GET /api/one-bill?leaseAppKey= — the
// combined rent + café statement, served from the one-bill package's shared
// `one-bill-history` lens read model (P5, never Core KV). Scoped to the
// caller's own lease: leaseAppKey must appear among the PROTECTED
// lease-applications rows RLS returns for the authenticated actor (the same
// self-anchor handleApplications relies on), so — unlike /api/ledger, which
// today trusts the query param unchecked — a signed-in tenant cannot pull
// another lease's statement by guessing its key.
//
// The combined balance is rent + café, but the AGE (dueDate/isOverdue/
// daysOverdue/daysUntilDue/reminderSentAt) is the RENT account's alone — a
// café tab has no due date of its own — so this handler additionally reads
// the loftspace-ledger `leaseAccounts` + `ledgerHistory` buckets (the same
// pair /api/ledger reads) and derives it the same way (deriveRentArrears,
// ledger.go). rentBalanceCents carries the rent-only balance alongside the
// combined one so the FE can name which balance the age refers to.
func (s *server) handleOneBillStatement(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticateRead(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "authentication required: "+err.Error())
		return
	}
	leaseAppKey := strings.TrimSpace(r.URL.Query().Get("leaseAppKey"))
	if leaseAppKey == "" {
		s.writeError(w, http.StatusBadRequest, "leaseAppKey query param is required")
		return
	}
	if s.pgPool == nil {
		s.writeError(w, http.StatusBadGateway,
			"protected read model not configured (set LOFTSPACE_APP_PG_DSN and ensure Postgres + the lease-signing protected lens are up)")
		return
	}

	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	apps, err := queryApplications(ctx, s.pgPool, actor.Subject)
	if err != nil {
		s.logger.Error("read protected lease applications", "error", err)
		s.writeError(w, http.StatusBadGateway, "could not read the protected lease-applications model")
		return
	}
	owned := false
	for _, a := range apps {
		if a.EntityKey == leaseAppKey {
			owned = true
			break
		}
	}
	if !owned {
		s.writeError(w, http.StatusForbidden, "not your lease")
		return
	}

	bucket := onebill.HistoryBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bucket+": "+err.Error()+" (is one-bill installed and the Refractor projecting?)")
		return
	}
	get := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, bucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}
	rows, balance := computeOneBillHistory(keys, get, leaseAppKey)

	acctBucket := loftspaceledger.LeaseAccountsBucket
	acctKeys, err := conn.KVListKeys(ctx, acctBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+acctBucket+": "+err.Error()+" (is loftspace-ledger installed and the Refractor projecting?)")
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
		s.logger.Error("read lease accounts for one-bill rent age", "bucket", acctBucket, "error", err)
		s.writeError(w, http.StatusBadGateway, "read "+acctBucket+" incomplete: "+err.Error())
		return
	}
	acctGet := func(key string) ([]byte, bool) { v, ok := acctValues[key]; return v, ok }
	acctRow, _ := findLeaseAccountRow(acctKeys, acctGet, leaseAppKey)

	ledgerBucket := loftspaceledger.LedgerHistoryBucket
	ledgerKeys, err := conn.KVListKeys(ctx, ledgerBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+ledgerBucket+": "+err.Error()+" (is loftspace-ledger installed and the Refractor projecting?)")
		return
	}
	ledgerValues, err := readAllOrFail(ledgerKeys, func(key string) ([]byte, error) {
		entry, err := conn.KVGet(ctx, ledgerBucket, key)
		if err != nil {
			return nil, err
		}
		return entry.Value, nil
	})
	if err != nil {
		s.logger.Error("read ledger history for one-bill rent age", "bucket", ledgerBucket, "error", err)
		s.writeError(w, http.StatusBadGateway, "read "+ledgerBucket+" incomplete: "+err.Error())
		return
	}
	ledgerGet := func(key string) ([]byte, bool) { v, ok := ledgerValues[key]; return v, ok }
	rentRows, rentBalance := computeLedgerHistory(ledgerKeys, ledgerGet, leaseAppKey)
	arrears := deriveRentArrears(rentRows, acctRow.ArrearsDueAt, acctRow.ArrearsReminderSentAt, time.Now().UTC())
	// The deposit is a rent-account transaction (DebitAccount/ReturnDeposit
	// both post to the lease's loftspace-ledger account), never a one-bill
	// entry of its own — so its summary is derived from the SAME rentRows
	// read above for the rent age, not from the combined `rows` this
	// endpoint otherwise renders.
	deposit := computeDepositSummary(rentRows)

	s.writeJSON(w, http.StatusOK, map[string]any{
		"leaseAppKey":          leaseAppKey,
		"entries":              rows,
		"balanceCents":         balance,
		"rentBalanceCents":     rentBalance,
		"dueDate":              arrears.DueDate,
		"isOverdue":            arrears.IsOverdue,
		"daysOverdue":          arrears.DaysOverdue,
		"daysUntilDue":         arrears.DaysUntilDue,
		"reminderSentAt":       arrears.ReminderSentAt,
		"depositHeldCents":     deposit.DepositHeldCents,
		"depositChargedCents":  deposit.DepositChargedCents,
		"depositChargedAt":     deposit.DepositChargedAt,
		"depositDeductedCents": deposit.DepositDeductedCents,
		"depositClauseKey":     deposit.DepositClauseKey,
		"depositReturnedAt":    deposit.DepositReturnedAt,
	})
}
