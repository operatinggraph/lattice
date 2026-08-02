package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/substrate"
	wellnessledger "github.com/operatinggraph/lattice/packages/wellness-ledger"
)

// ledgerEntryProjection is one row of the wellness-ledger `wellnessLedgerHistory`
// lens, read from its NATS-KV read-model bucket (P5 — never Core KV).
type ledgerEntryProjection struct {
	TransactionKey string   `json:"transactionKey"`
	AccountKey     string   `json:"accountKey"`
	IdentityKey    string   `json:"identityKey"`
	Type           string   `json:"type"`
	AmountCents    *float64 `json:"amountCents"`
	Memo           string   `json:"memo"`
	PostedAt       string   `json:"postedAt"`
}

// ledgerEntryRow is the billing-history row the FE renders.
type ledgerEntryRow struct {
	TransactionKey string `json:"transactionKey"`
	Type           string `json:"type"`
	AmountCents    int64  `json:"amountCents"`
	Memo           string `json:"memo,omitempty"`
	PostedAt       string `json:"postedAt"`
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

// handleLedger implements GET /api/ledger — the signed-in member's own
// billing history, served from the wellnessLedgerHistory + wellnessMemberAccounts
// lens read models (P5, never Core KV). There is deliberately NO identityKey
// parameter, mirroring handleBookings: whose ledger this is comes from the
// verified session and nowhere else — a client-supplied identity filter is
// precisely the vector clinic closed when it deleted `/api/appointments?provider=`.
//
// Returns the member's transaction rows, the running balance, and the
// account key — empty when no wellnessaccount has been opened for them yet,
// which today only a root-submitted op can do (CreateAccount is
// grantsTo-operator-only, packages/wellness-ledger/permissions.go; the
// browser has no grant to call it itself). A blank accountKey is therefore
// the normal, expected shape for most members right now, not an error.
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
	rows, balance := computeLedgerHistory(keys, s.kvGetter(ctx, bucket), identityKey)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"accountKey":   accountKey,
		"transactions": rows,
		"balanceCents": balance,
	})
}
