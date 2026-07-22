package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	loftspaceledger "github.com/operatinggraph/lattice/packages/loftspace-ledger"
)

// ledgerEntryProjection is one row of the loftspace-ledger `ledgerHistory` lens,
// read from its NATS-KV read-model bucket (P5 — never Core KV).
type ledgerEntryProjection struct {
	TransactionKey string   `json:"transactionKey"`
	AccountKey     string   `json:"accountKey"`
	LeaseAppKey    string   `json:"leaseAppKey"`
	Type           string   `json:"type"`
	AmountCents    *float64 `json:"amountCents"`
	Memo           string   `json:"memo"`
	PostedAt       string   `json:"postedAt"`
	// ClauseKey/ClauseProse (Fire V4 "why was I charged this?") ride the
	// authorizedBy hop the ledgerHistory lens optionally walks — empty for a
	// plain human-submitted charge/payment that carries no clauseRef.
	ClauseKey   string `json:"clauseKey"`
	ClauseProse string `json:"clauseProse"`
}

// ledgerEntryRow is the payment-history row the FE renders.
type ledgerEntryRow struct {
	TransactionKey string `json:"transactionKey"`
	Type           string `json:"type"`
	AmountCents    int64  `json:"amountCents"`
	Memo           string `json:"memo,omitempty"`
	PostedAt       string `json:"postedAt"`
	ClauseKey      string `json:"clauseKey,omitempty"`
	ClauseProse    string `json:"clauseProse,omitempty"`
}

// computeLedgerHistory filters the ledgerHistory lens rows to one lease, sorts
// them chronologically, and derives the running balance in cents (sum debits −
// sum credits) — the ledger itself stores no running total (append-only, D5),
// so the FE-facing balance is always assembled from the full transaction set. A
// row that fails to decode or carries no transactionKey (a tombstoned
// projection entry) is skipped.
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
			ClauseKey:      p.ClauseKey,
			ClauseProse:    p.ClauseProse,
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

// leaseAccountProjection is one row of the loftspace-ledger `leaseAccounts`
// lens — one per lease, AccountKey empty until CreateAccount has opened one.
// The account carries its OWN independently-minted NanoID (never derived
// from the lease's — see packages/loftspace-ledger/scripts.go), so this lens
// read is the only way to resolve it.
type leaseAccountProjection struct {
	LeaseAppKey string `json:"leaseAppKey"`
	AccountKey  string `json:"accountKey"`
}

// resolveLeaseAccount scans the leaseAccounts lens rows for the one matching
// leaseAppKey, returning its account key ("" if the lease has none yet,
// including when no row projected at all — a lease the Refractor hasn't
// caught up to yet reads the same as one with no account).
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

// handleLedger implements GET /api/ledger?leaseAppKey= — the payment-history
// view, served from the `ledgerHistory` + `leaseAccounts` lens read models
// (NOT Core KV, P5). It returns the lease's transaction rows, the running
// balance, and the account key (empty if the lease has not opened a ledger
// account yet) the FE needs to post a new charge or payment.
func (s *server) handleLedger(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	leaseAppKey := strings.TrimSpace(r.URL.Query().Get("leaseAppKey"))
	if leaseAppKey == "" {
		s.writeError(w, http.StatusBadRequest, "leaseAppKey query param is required")
		return
	}

	ctx, cancel := s.reqContext(r)
	defer cancel()

	acctBucket := loftspaceledger.LeaseAccountsBucket
	acctKeys, err := conn.KVListKeys(ctx, acctBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+acctBucket+": "+err.Error()+" (is loftspace-ledger installed and the Refractor projecting?)")
		return
	}
	acctGet := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, acctBucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}
	accountKey := resolveLeaseAccount(acctKeys, acctGet, leaseAppKey)

	bucket := loftspaceledger.LedgerHistoryBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bucket+": "+err.Error()+" (is loftspace-ledger installed and the Refractor projecting?)")
		return
	}
	get := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, bucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}
	rows, balance := computeLedgerHistory(keys, get, leaseAppKey)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"leaseAppKey":  leaseAppKey,
		"accountKey":   accountKey,
		"transactions": rows,
		"balanceCents": balance,
	})
}
