package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

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
	// PeriodStart/PeriodEnd/DueAt: a recurring charge's recorded billing period
	// and due date (empty on a payment or a one-time charge).
	PeriodStart string `json:"periodStart"`
	PeriodEnd   string `json:"periodEnd"`
	DueAt       string `json:"dueAt"`
	// ClauseKey/ClauseProse (Fire V4 "why was I charged this?") ride the
	// authorizedBy hop the ledgerHistory lens optionally walks — empty for a
	// plain human-submitted charge/payment that carries no clauseRef.
	ClauseKey   string `json:"clauseKey"`
	ClauseProse string `json:"clauseProse"`
	// ClausePurpose is the authorizing clause's own recorded purpose token
	// (CreateClause's purpose, ridden through the same authorizedBy hop) —
	// "deposit" for a security-deposit charge or its return, empty for rent
	// and for any clause that records no purpose.
	ClausePurpose string `json:"clausePurpose"`
	// Kind is the entry's own recorded provenance (loftspace-ledger's
	// PayOutBalance stamps kind:"payout" on the debit that pays a credit
	// balance out — no clause authorizes it, so no other column would tell
	// it apart); empty on every other entry.
	Kind string `json:"kind"`
}

// ledgerEntryRow is the payment-history row the FE renders.
type ledgerEntryRow struct {
	TransactionKey string `json:"transactionKey"`
	Type           string `json:"type"`
	AmountCents    int64  `json:"amountCents"`
	Memo           string `json:"memo,omitempty"`
	PostedAt       string `json:"postedAt"`
	PeriodStart    string `json:"periodStart,omitempty"`
	PeriodEnd      string `json:"periodEnd,omitempty"`
	DueAt          string `json:"dueAt,omitempty"`
	ClauseKey      string `json:"clauseKey,omitempty"`
	ClauseProse    string `json:"clauseProse,omitempty"`
	ClausePurpose  string `json:"clausePurpose,omitempty"`
	Kind           string `json:"kind,omitempty"`
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
			PeriodStart:    p.PeriodStart,
			PeriodEnd:      p.PeriodEnd,
			DueAt:          p.DueAt,
			ClauseKey:      p.ClauseKey,
			ClauseProse:    p.ClauseProse,
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

// depositSummary is the security-deposit strip a statement renders apart
// from the rent balance: how much of a charged deposit is still held,
// what its charged figure was, and when it was charged / returned.
// DepositChargedAt empty means the lease's ledger carries no deposit row at
// all — a deposit-less unit, or a lease whose deposit clause has not yet
// billed. DepositReturnedAt stays empty while the deposit is still held.
type depositSummary struct {
	DepositHeldCents    int64  `json:"depositHeldCents"`
	DepositChargedCents int64  `json:"depositChargedCents,omitempty"`
	DepositChargedAt    string `json:"depositChargedAt,omitempty"`
	// DepositDeductedCents is the running total taken off the deposit by
	// RecordDepositDeduction ("deduction" rows) — never counted in the
	// balance a rent payment could retire (post_entry's own type test skips
	// it identically).
	DepositDeductedCents int64 `json:"depositDeductedCents,omitempty"`
	// DepositClauseKey is the deposit charge row's own authorizing clause —
	// the key RecordDepositDeduction's payload needs, so the FE's "Deduct
	// from deposit" form can name it without a second lens read.
	DepositClauseKey  string `json:"depositClauseKey,omitempty"`
	DepositReturnedAt string `json:"depositReturnedAt,omitempty"`
}

// computeDepositSummary folds a lease's own ledgerHistory rows (already
// filtered to the lease and sorted chronologically by computeLedgerHistory)
// down to the deposit's own figures — every row whose ClausePurpose is
// anything other than "deposit" (rent, a plain human charge, a non-deposit
// one-time fee) is ignored entirely. A deposit clause posts the charge, zero
// or more deductions and (usually) one credit across its whole lifetime;
// DepositChargedCents is the charge's own amount (not the net), so a
// returned deposit still names what it was, and DepositHeldCents nets
// charged − deducted − returned — custody, never an unpaid figure.
func computeDepositSummary(rows []ledgerEntryRow) depositSummary {
	var s depositSummary
	for _, r := range rows {
		if r.ClausePurpose != "deposit" {
			continue
		}
		switch r.Type {
		case "debit":
			s.DepositHeldCents += r.AmountCents
			if s.DepositChargedAt == "" {
				s.DepositChargedAt = r.PostedAt
				s.DepositChargedCents = r.AmountCents
				s.DepositClauseKey = r.ClauseKey
			}
		case "deduction":
			s.DepositHeldCents -= r.AmountCents
			s.DepositDeductedCents += r.AmountCents
		case "credit":
			s.DepositHeldCents -= r.AmountCents
			if s.DepositReturnedAt == "" {
				s.DepositReturnedAt = r.PostedAt
			}
		}
	}
	return s
}

// rawGetter reads one bucket entry's raw bytes for a key, erroring on a real
// fetch failure (distinct from kvGetter's bool, which conflates "no such
// row" with "the read failed").
type rawGetter func(key string) ([]byte, error)

// readAllOrFail fetches every listed key via get, failing on the first
// error instead of silently treating a fetch failure as "no such row." A
// key just came back from a KVListKeys call on the same bucket, so a
// KVGet error on it is a real projection/network fault,
// not evidence the row doesn't exist — letting it fall through as absent
// is what silently dropped lines and produced a wrong balance.
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

// leaseAccountProjection is one row of the loftspace-ledger `leaseAccounts`
// lens — one per lease, AccountKey empty until LoftspaceCreateAccount has opened one.
// The account carries its OWN independently-minted NanoID (never derived
// from the lease's — see packages/loftspace-ledger/scripts.go), so this lens
// read is the only way to resolve it. The three Arrears columns come
// straight off the account's own `.arrears` aspect (EvaluateLoftspaceArrears'
// stamp) — empty for an account nothing has evaluated yet, or one with
// nothing owed.
type leaseAccountProjection struct {
	LeaseAppKey           string `json:"leaseAppKey"`
	AccountKey            string `json:"accountKey"`
	ArrearsDueAt          string `json:"arrearsDueAt"`
	ArrearsRemindedFor    string `json:"arrearsRemindedFor"`
	ArrearsReminderSentAt string `json:"arrearsReminderSentAt"`
}

// findLeaseAccountRow scans the leaseAccounts lens rows for the one matching
// leaseAppKey, returning its full projection (ok=false if the lease has no
// row at all, including when the Refractor hasn't caught up to it yet —
// that reads the same as a lease with no account opened).
func findLeaseAccountRow(keys []string, get kvGetter, leaseAppKey string) (leaseAccountProjection, bool) {
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p leaseAccountProjection
		if json.Unmarshal(raw, &p) != nil || p.LeaseAppKey != leaseAppKey {
			continue
		}
		return p, true
	}
	return leaseAccountProjection{}, false
}

// indexLeaseAccountsByLease scans the leaseAccounts lens rows once and
// returns them keyed by leaseAppKey — the portfolio handler needs every
// occupied lease's row, and re-scanning the whole bucket once per lease
// (findLeaseAccountRow's own per-lease cost) would redecode the same rows
// N times over.
func indexLeaseAccountsByLease(keys []string, get kvGetter) map[string]leaseAccountProjection {
	out := make(map[string]leaseAccountProjection, len(keys))
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p leaseAccountProjection
		if json.Unmarshal(raw, &p) != nil || p.LeaseAppKey == "" {
			continue
		}
		out[p.LeaseAppKey] = p
	}
	return out
}

// resolveLeaseAccount scans the leaseAccounts lens rows for the one matching
// leaseAppKey, returning its account key ("" if the lease has none yet,
// including when no row projected at all — a lease the Refractor hasn't
// caught up to yet reads the same as one with no account).
func resolveLeaseAccount(keys []string, get kvGetter, leaseAppKey string) string {
	row, _ := findLeaseAccountRow(keys, get, leaseAppKey)
	return row.AccountKey
}

// rentArrearsProjection is one lease's rent-balance age — dueDate empty when
// nothing is owed, isOverdue/daysOverdue/daysUntilDue derived from whichever
// dueDate applies, reminderSentAt threaded verbatim from the leaseAccounts
// row. Shared by /api/ledger, /api/one-bill and /api/portfolio-pulse — the
// three surfaces that render a lease's rent age.
type rentArrearsProjection struct {
	DueDate        string `json:"dueDate"`
	IsOverdue      bool   `json:"isOverdue"`
	DaysOverdue    int    `json:"daysOverdue"`
	DaysUntilDue   int    `json:"daysUntilDue"`
	ReminderSentAt string `json:"reminderSentAt"`
}

// deriveRentArrears computes a lease's rent-balance age from its own
// ledgerHistory rows (rows must already be sorted chronologically —
// postedAt, then transactionKey — the order computeLedgerHistory returns
// them in). It FIFO-ages the ledger exactly as EvaluateLoftspaceArrears
// does: debits open the queue, credits retire the oldest still-open debit
// first, and any credit surplus carries forward to prepay whatever opens
// next. Unlike the wellness/café ledgers this ledger writes no `reverses`
// credit, so there is no netting pre-pass — the FIFO runs plain.
//
// The head of the open-debit queue is the oldest unpaid charge; its due
// date is its own recorded dueAt (DebitAccount's stamp), or its postedAt
// when it records none — a landlord one-off (LoftspaceRecordCharge) is due
// on receipt, never re-gridded with an added term.
//
// A RECORDED due date (recordedDueAt — the leaseAccounts row's own
// arrearsDueAt) wins over this derivation whenever a debit is still open;
// a fully paid lease reports no due date regardless of a stamp an
// evaluation has not yet caught up to clear. reminderSentAt threads through
// as the account's own recorded SEND INTENT, unrelated to this call's own
// balance recomputation — EXCEPT across an episode boundary: while walking
// the FIFO, the EPISODE START is the postedAt of the debit that opens the
// queue from empty (the charge that takes the account from square to
// owing again). A recorded reminderSentAt that predates the CURRENT
// episode's start belongs to a PRIOR episode the account has since paid
// off and reopened — a payment to zero followed by a new charge, ahead of
// the next EvaluateLoftspaceArrears evaluation minting a fresh `.arrears`
// for the new one. Both reminderSentAt and recordedDueAt are dropped
// together in that case (the recorded stamp predates the episode, so the
// derived head's own due wins) — never partway, since both came off the
// same stale evaluation. Equal to the episode start still counts as
// belonging to it (dropped only when STRICTLY earlier).
func deriveRentArrears(rows []ledgerEntryRow, recordedDueAt, reminderSentAt string, now time.Time) rentArrearsProjection {
	type openDebit struct {
		postedAt  string
		dueAt     string
		remaining int64
	}
	var open []openDebit
	var surplus int64
	var episodeStart string
	for _, r := range rows {
		switch r.Type {
		case "debit":
			amount := r.AmountCents
			wasSquare := len(open) == 0
			if surplus >= amount {
				surplus -= amount
				continue
			}
			amount -= surplus
			surplus = 0
			if wasSquare {
				episodeStart = r.PostedAt
			}
			open = append(open, openDebit{postedAt: r.PostedAt, dueAt: r.DueAt, remaining: amount})
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
			surplus += remaining
		}
	}

	var dueDate string
	if len(open) > 0 {
		head := open[0]
		derived := head.dueAt
		if derived == "" {
			derived = head.postedAt
		}
		dueDate = derived

		if reminderSentAt != "" && reminderSentAt < episodeStart {
			reminderSentAt = ""
			recordedDueAt = ""
		}
		if recordedDueAt != "" {
			dueDate = recordedDueAt
		}
	}

	isOverdue, daysOverdue, daysUntilDue := computeRentOverdue(dueDate, now)
	return rentArrearsProjection{
		DueDate:        dueDate,
		IsOverdue:      isOverdue,
		DaysOverdue:    daysOverdue,
		DaysUntilDue:   daysUntilDue,
		ReminderSentAt: reminderSentAt,
	}
}

// computeRentOverdue reports whether dueDate (RFC3339) has been reached by
// now (isOverdue: dueDate <= now, so the exact instant a lease crosses into
// arrears counts as overdue, matching EvaluateLoftspaceArrears' own
// `remindAt <= evaluatedAt` boundary), and by how many whole UTC days in
// whichever direction — floored, not rounded, so a due date the ledger has
// been aging for exactly N days (not N+ε) reads as N, and a due date a
// full day away reads daysUntilDue=1. A due date that fails to parse fails
// closed (neither overdue nor upcoming), the same posture the FIFO
// derivation takes on an unopened head.
func computeRentOverdue(dueDate string, now time.Time) (isOverdue bool, daysOverdue int, daysUntilDue int) {
	if dueDate == "" {
		return false, 0, 0
	}
	due, err := time.Parse(time.RFC3339, dueDate)
	if err != nil {
		return false, 0, 0
	}
	if due.After(now) {
		return false, 0, int(due.Sub(now).Hours() / 24)
	}
	return true, int(now.Sub(due).Hours() / 24), 0
}

// leaseVisibleToActor reports whether the authenticated actor may view
// leaseAppKey's ledger — either as the tenant (queryApplicationByKey, RLS-scoped
// to the caller's own leases, D1.5) or as the managing landlord
// (queryLandlordApplications, RLS-scoped to the caller's own units), mirroring
// the ownership checks handleOneBillStatement and handleUnitApplications already
// apply to the same protected models.
func leaseVisibleToActor(ctx context.Context, pool pgxBeginner, actorID, leaseAppKey string) (bool, error) {
	if _, ok, err := queryApplicationByKey(ctx, pool, actorID, leaseAppKey); err != nil {
		return false, err
	} else if ok {
		return true, nil
	}
	managed, err := queryLandlordApplications(ctx, pool, actorID)
	if err != nil {
		return false, err
	}
	for _, row := range managed {
		if row.EntityKey == leaseAppKey {
			return true, nil
		}
	}
	return false, nil
}

// handleLedger implements GET /api/ledger?leaseAppKey= — the payment-history
// view, served from the `ledgerHistory` + `leaseAccounts` lens read models
// (NOT Core KV, P5). It returns the lease's transaction rows, the running
// balance, and the account key (empty if the lease has not opened a ledger
// account yet) the FE needs to post a new charge or payment. Scoped to a lease
// the caller is party to (tenant or managing landlord) via leaseVisibleToActor
// — unlike the query-param-trusting handler this replaces, a signed-in caller
// cannot pull another lease's balance/accountKey by guessing its key.
func (s *server) handleLedger(w http.ResponseWriter, r *http.Request) {
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

	ctx, cancel := s.reqContext(r)
	defer cancel()

	if s.pgPool == nil {
		s.writeError(w, http.StatusBadGateway,
			"protected read model not configured (set LOFTSPACE_APP_PG_DSN and ensure Postgres + the lease-signing protected lens are up)")
		return
	}
	visible, err := leaseVisibleToActor(ctx, s.pgPool, actor.Subject, leaseAppKey)
	if err != nil {
		s.logger.Error("read protected lease applications", "error", err)
		s.writeError(w, http.StatusBadGateway, "could not read the protected lease-applications model")
		return
	}
	if !visible {
		s.writeError(w, http.StatusForbidden, "not your lease")
		return
	}

	conn, ok := s.requireConn(w)
	if !ok {
		return
	}

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
		s.logger.Error("read lease accounts for ledger", "bucket", acctBucket, "error", err)
		s.writeError(w, http.StatusBadGateway, "read "+acctBucket+" incomplete: "+err.Error())
		return
	}
	acctGet := func(key string) ([]byte, bool) { v, ok := acctValues[key]; return v, ok }
	acctRow, _ := findLeaseAccountRow(acctKeys, acctGet, leaseAppKey)
	accountKey := acctRow.AccountKey

	bucket := loftspaceledger.LedgerHistoryBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bucket+": "+err.Error()+" (is loftspace-ledger installed and the Refractor projecting?)")
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
	arrears := deriveRentArrears(rows, acctRow.ArrearsDueAt, acctRow.ArrearsReminderSentAt, time.Now().UTC())
	deposit := computeDepositSummary(rows)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"leaseAppKey":          leaseAppKey,
		"accountKey":           accountKey,
		"transactions":         rows,
		"balanceCents":         balance,
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
