package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

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
	Source         string   `json:"source"`
}

// oneBillEntryRow is the statement row the FE renders.
type oneBillEntryRow struct {
	TransactionKey string `json:"transactionKey"`
	Type           string `json:"type"`
	AmountCents    int64  `json:"amountCents"`
	Memo           string `json:"memo,omitempty"`
	PostedAt       string `json:"postedAt"`
	Source         string `json:"source"`
}

// computeOneBillHistory filters the one-bill-history lens rows to one lease,
// sorts them chronologically, and derives the combined rent+café running
// balance in cents — mirrors computeLedgerHistory (ledger.go), minus the
// clause fields the one-bill lens does not project.
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
			Source:         p.Source,
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
	s.writeJSON(w, http.StatusOK, map[string]any{
		"leaseAppKey":  leaseAppKey,
		"entries":      rows,
		"balanceCents": balance,
	})
}
