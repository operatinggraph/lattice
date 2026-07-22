package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	clinicledger "github.com/operatinggraph/lattice/packages/clinic-ledger"
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

// patientAccountProjection is one row of the clinic-ledger
// `clinicPatientAccounts` lens — one per patient, AccountKey empty until
// CreateAccount has opened one. The account carries its OWN
// independently-minted NanoID (never derived from the patient's — see
// packages/clinic-ledger/scripts.go), so this lens read is the only way to
// resolve it.
type patientAccountProjection struct {
	PatientKey string `json:"patientKey"`
	AccountKey string `json:"accountKey"`
}

// resolvePatientAccount scans the clinicPatientAccounts lens rows for the
// one matching patientKey, returning its account key ("" if the patient has
// none yet, including when no row projected at all — a patient the Refractor
// hasn't caught up to yet reads the same as one with no account).
func resolvePatientAccount(keys []string, get kvGetter, patientKey string) string {
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p patientAccountProjection
		if json.Unmarshal(raw, &p) != nil || p.PatientKey != patientKey {
			continue
		}
		return p.AccountKey
	}
	return ""
}

// patientVisibleToActor reports whether patientKey is visible to actorID
// under the clinicPatientsRead protected model's RLS policy (selectPatientsSQL
// in patients.go): every roster row carries an EMPTY authz_anchors set, so a
// row here is visible ONLY to an actor holding the reserved WildcardAnchor
// grant (staff). The ledger has no protected model of its own — this reuses
// the already-provisioned clinicPatientsRead table as the ledger's
// authorization gate rather than standing up new schema for a second lens.
func patientVisibleToActor(ctx context.Context, pool pgxBeginner, actorID, patientKey string) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actorID); err != nil {
		return false, err
	}
	var one int
	visible := true
	if err := tx.QueryRow(ctx, "SELECT 1 FROM read_clinic_patients WHERE patient_key = $1 LIMIT 1", patientKey).Scan(&one); err != nil {
		if err != pgx.ErrNoRows {
			return false, err
		}
		visible = false
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return visible, nil
}

// handleLedger implements GET /api/ledger?patientKey= — the billing-history
// view, served from the `clinicLedgerHistory` + `clinicPatientAccounts` lens
// read models (NOT Core KV, P5). It returns the patient's transaction rows,
// the running balance, and the account key (empty if the patient has not
// opened a ledger account yet) the FE needs to post a new charge or payment.
//
// Gated on authenticateRead + patientVisibleToActor (the same class of fix
// D1.5 applied to handleAppointments' old unauthenticated `?patient=`
// vector): unlike that vector this stays a single endpoint rather than
// splitting into a self/staff pair, because clinic-ledger has no self-service
// billing view yet — every caller today is the front-desk staff view
// (cmd/clinic-app/web/app.js loadLedger, authedGetAsStaff).
func (s *server) handleLedger(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticateRead(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "authentication required: "+err.Error())
		return
	}
	patientKey := strings.TrimSpace(r.URL.Query().Get("patientKey"))
	if patientKey == "" {
		s.writeError(w, http.StatusBadRequest, "patientKey query param is required")
		return
	}

	ctx, cancel := s.reqContext(r)
	defer cancel()

	if s.pgPool == nil {
		s.writeError(w, http.StatusBadGateway,
			"protected read model not configured (set CLINIC_APP_PG_DSN and ensure Postgres + the clinic-domain protected lens are up)")
		return
	}
	visible, err := patientVisibleToActor(ctx, s.pgPool, actor.Subject, patientKey)
	if err != nil {
		s.logger.Error("check patient visibility for ledger read", "error", err)
		s.writeError(w, http.StatusBadGateway, "could not verify read access")
		return
	}
	if !visible {
		s.writeError(w, http.StatusForbidden, "patient not visible to this actor")
		return
	}

	conn, ok := s.requireConn(w)
	if !ok {
		return
	}

	acctBucket := clinicledger.PatientAccountsBucket
	acctKeys, err := conn.KVListKeys(ctx, acctBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+acctBucket+": "+err.Error()+" (is clinic-ledger installed and the Refractor projecting?)")
		return
	}
	acctGet := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, acctBucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}
	accountKey := resolvePatientAccount(acctKeys, acctGet, patientKey)

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
