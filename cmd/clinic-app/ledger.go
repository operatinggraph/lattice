package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	clinicledger "github.com/asolgan/lattice/packages/clinic-ledger"
)

// ledgerEntryProjection is one row of the clinic-ledger `ledgerHistory` lens,
// read from its NATS-KV read-model bucket (P5 — never Core KV).
type ledgerEntryProjection struct {
	TransactionKey string   `json:"transactionKey"`
	AccountKey     string   `json:"accountKey"`
	PatientKey     string   `json:"patientKey"`
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

// computeLedgerHistory filters the ledgerHistory lens rows to one patient,
// sorts them chronologically, and derives the running balance in cents (sum
// debits − sum credits) — the ledger itself stores no running total
// (append-only, D5), so the FE-facing balance is always assembled from the
// full transaction set. A row that fails to decode or carries no
// transactionKey (a tombstoned projection entry) is skipped.
func computeLedgerHistory(keys []string, get kvGetter, patientKey string) ([]ledgerEntryRow, int64) {
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
		if p.PatientKey != patientKey {
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

// deriveAccountKey computes a patient's ledger account key without a read:
// CreateAccount mints the account under the SAME bare NanoID as the patient
// (packages/clinic-ledger/scripts.go), so the FE can address the account — to
// post its first charge — before the account necessarily exists yet. Returns
// "" for a key that isn't a vtx.patient.<NanoID>.
func deriveAccountKey(patientKey string) string {
	const prefix = "vtx.patient."
	if !strings.HasPrefix(patientKey, prefix) || patientKey == prefix {
		return ""
	}
	return "vtx.clinicaccount." + strings.TrimPrefix(patientKey, prefix)
}

// handleLedger implements GET /api/ledger?patientKey= — the billing-history
// view, served from the `ledgerHistory` lens read model (NOT Core KV, P5). It
// returns the patient's transaction rows, the running balance, and the
// (derived, possibly not-yet-created) ledger account key the FE needs to post
// a new charge or payment.
func (s *server) handleLedger(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	patientKey := strings.TrimSpace(r.URL.Query().Get("patientKey"))
	if patientKey == "" {
		s.writeError(w, http.StatusBadRequest, "patientKey query param is required")
		return
	}
	accountKey := deriveAccountKey(patientKey)
	if accountKey == "" {
		s.writeError(w, http.StatusBadRequest, "patientKey must be a vtx.patient.<NanoID> key")
		return
	}

	ctx, cancel := s.reqContext(r)
	defer cancel()

	bucket := clinicledger.LedgerHistoryBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bucket+": "+err.Error()+" (is clinic-ledger installed and the Refractor projecting?)")
		return
	}
	get := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, bucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}
	rows, balance := computeLedgerHistory(keys, get, patientKey)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"patientKey":   patientKey,
		"accountKey":   accountKey,
		"transactions": rows,
		"balanceCents": balance,
	})
}
