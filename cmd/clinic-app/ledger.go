package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	clinicledger "github.com/operatinggraph/lattice/packages/clinic-ledger"
)

// ledgerEntryProjection is one row of the clinic-ledger `ledgerHistory` lens,
// read from its NATS-KV read-model bucket (P5 — never Core KV). ReversesKey
// names the transaction a credit gives back (empty for anything else — an
// ordinary charge, payment, or a waiver of a general balance); SettlesFee is
// a plain boolean, never null, true only when AppointmentKey is this line's
// OWN fee (the settles hop) rather than merely the visit it is for.
type ledgerEntryProjection struct {
	TransactionKey string   `json:"transactionKey"`
	AccountKey     string   `json:"accountKey"`
	PatientKey     string   `json:"patientKey"`
	Type           string   `json:"type"`
	AmountCents    *float64 `json:"amountCents"`
	Memo           string   `json:"memo"`
	PostedAt       string   `json:"postedAt"`
	AppointmentKey string   `json:"appointmentKey"`
	VisitStartsAt  string   `json:"visitStartsAt"`
	Reason         string   `json:"reason"`
	ReversesKey    string   `json:"reversesKey"`
	SettlesFee     bool     `json:"settlesFee"`
}

// ledgerEntryRow is the billing-history row the FE renders. AppointmentKey /
// VisitStartsAt are empty for a line that names no visit at all; SettlesFee
// says which of the two ways a line can name one applies — true when the
// line IS that visit's no-show fee (the settles hop), false when it is
// merely posted for that visit (a copay or procedure charge, the forVisit
// hop) — which is what lets the FE tell two otherwise-identical "No-show
// fee" lines apart. Reason is empty for a debit (charge-only rows never
// carry it) and "payment"/"waiver" for a credit — the FE labels a waiver
// distinctly so forgiven debt is never mistaken for cash collected.
// ReversesKey names the charge a credit gives back (empty for anything
// else). OpenCents/DueAt/IsOverdue/DaysOverdue are deriveStatement's
// per-charge annotation: OpenCents is the debit's still-unpaid remainder in
// cents (0 once a debit is fully paid or reversed — always present, never
// omitted, so a paid-off charge is distinguishable from one this response
// never annotated); DueAt is that debit's own postedAt plus the grace period
// (a paid charge still had a due date — the FE decides whether to render it);
// IsOverdue/DaysOverdue are set only once OpenCents is still positive. All
// four are the zero value on a credit row — a credit is never itself "open."
type ledgerEntryRow struct {
	TransactionKey string `json:"transactionKey"`
	Type           string `json:"type"`
	AmountCents    int64  `json:"amountCents"`
	Memo           string `json:"memo,omitempty"`
	PostedAt       string `json:"postedAt"`
	AppointmentKey string `json:"appointmentKey,omitempty"`
	VisitStartsAt  string `json:"visitStartsAt,omitempty"`
	Reason         string `json:"reason,omitempty"`
	ReversesKey    string `json:"reversesKey,omitempty"`
	SettlesFee     bool   `json:"settlesFee,omitempty"`
	OpenCents      int64  `json:"openCents"`
	DueAt          string `json:"dueAt,omitempty"`
	IsOverdue      bool   `json:"isOverdue,omitempty"`
	DaysOverdue    int    `json:"daysOverdue,omitempty"`
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

// computeLedgerHistory filters the ledgerHistory lens rows to one patient and
// sorts them chronologically — the FE-facing balance is always assembled
// from the full transaction set, since the ledger itself stores no running
// total (append-only, D5). A row that fails to decode or carries no
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
			AppointmentKey: p.AppointmentKey,
			VisitStartsAt:  p.VisitStartsAt,
			Reason:         p.Reason,
			ReversesKey:    p.ReversesKey,
			SettlesFee:     p.SettlesFee,
		})
	}
	sortLedgerRows(rows)
	return rows, sumBalance(rows)
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

// statementGraceDays is the net term between a charge posting and it
// counting overdue — matches café's cafeledger.ArrearsGraceDays and
// wellness's own statementGraceDays constant, so a patient who is also a
// resident or member sees the same grace period across every vertical.
const statementGraceDays = 15

// deriveStatement turns a chronologically-sorted ledger into a due date and
// overdue state, without the ledger ever storing either, and annotates each
// debit row of rows IN PLACE with its own OpenCents/DueAt/IsOverdue/
// DaysOverdue (rows is mutated by index — the caller's slice and this
// function's see the same backing array, so no return value carries the
// annotation back).
//
// A credit that names the charge it reverses (ReversesKey) retires that
// charge specifically: a pre-pass nets every such credit against the debit
// it names — capped at that debit's own amount, accumulated across however
// many reversing credits name the same debit — before the FIFO walk ever
// runs, so a refund of a NEWER charge does not pay off an OLDER, unrelated
// one. A debit fully retired this way never opens; the rest of a
// partially-retired debit's amount opens as usual. Everything else still
// FIFOs: credits offset the OLDEST still-open debit first (mirroring how a
// real statement ages a balance), so the survivor at the front of the queue
// is the charge that has actually been sitting unpaid the longest — not
// just the most recent charge. A credit posted ahead of any open debit, or
// one that outruns the whole open-debit queue, carries its unapplied
// remainder (net of whatever it retired above) forward as surplus and
// prepays whichever debits arrive next, in order — so a charge that was
// already paid for by an earlier credit never ages as if it were the oldest
// open balance. A zero/credit balance has nothing to age: the statement
// returns no due date and every debit's OpenCents is annotated 0. A
// malformed postedAt on the oldest open debit fails the STATEMENT-level due
// date closed (no due date) rather than guessing.
//
// Every debit row — open or not — is annotated with its own DueAt
// (postedAt + statementGraceDays): a paid-off charge still had a due date,
// the render decides whether that matters. A debit whose own postedAt fails
// to parse gets no DueAt and is never overdue, independent of any other
// row's parse outcome. IsOverdue/DaysOverdue are computed, per row, only
// once that row's own OpenCents is positive — using the same now.After(due)
// / +1-day arithmetic the statement level uses on the oldest open debit.
func deriveStatement(rows []ledgerEntryRow, balanceCents int64, now time.Time) (dueDate string, isOverdue bool, daysOverdue int) {
	for i := range rows {
		if rows[i].Type != "debit" {
			continue
		}
		posted, err := time.Parse(time.RFC3339, rows[i].PostedAt)
		if err != nil {
			rows[i].DueAt = ""
			continue
		}
		rows[i].DueAt = posted.AddDate(0, 0, statementGraceDays).Format(time.RFC3339)
	}

	if balanceCents <= 0 {
		for i := range rows {
			if rows[i].Type == "debit" {
				rows[i].OpenCents = 0
				rows[i].IsOverdue = false
				rows[i].DaysOverdue = 0
			}
		}
		return "", false, 0
	}

	debitAmount := make(map[string]int64)
	for _, r := range rows {
		if r.Type == "debit" {
			debitAmount[r.TransactionKey] = r.AmountCents
		}
	}
	absorbedTotal := make(map[string]int64)
	absorbedByCredit := make(map[string]int64)
	for _, r := range rows {
		if r.Type != "credit" || r.ReversesKey == "" {
			continue
		}
		target, ok := debitAmount[r.ReversesKey]
		if !ok {
			continue
		}
		remaining := target - absorbedTotal[r.ReversesKey]
		absorbed := r.AmountCents
		if absorbed > remaining {
			absorbed = remaining
		}
		absorbedByCredit[r.TransactionKey] = absorbed
		absorbedTotal[r.ReversesKey] += absorbed
	}

	type openDebit struct {
		idx       int
		postedAt  string
		remaining int64
	}
	var open []openDebit
	var surplus int64
	for i, r := range rows {
		switch r.Type {
		case "debit":
			amount := r.AmountCents - absorbedTotal[r.TransactionKey]
			if amount <= 0 {
				continue
			}
			if surplus >= amount {
				surplus -= amount
				continue
			}
			amount -= surplus
			surplus = 0
			open = append(open, openDebit{idx: i, postedAt: r.PostedAt, remaining: amount})
		case "credit":
			remaining := r.AmountCents - absorbedByCredit[r.TransactionKey]
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

	for _, o := range open {
		rows[o.idx].OpenCents = o.remaining
	}
	for i := range rows {
		if rows[i].Type != "debit" || rows[i].OpenCents <= 0 || rows[i].DueAt == "" {
			continue
		}
		due, err := time.Parse(time.RFC3339, rows[i].DueAt)
		if err != nil {
			continue
		}
		if now.After(due) {
			rows[i].IsOverdue = true
			rows[i].DaysOverdue = int(now.Sub(due).Hours()/24) + 1
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

// patientAccountProjection is one row of the clinic-ledger
// `clinicPatientAccounts` lens — one per patient, AccountKey empty until
// ClinicCreateAccount has opened one. The account carries its OWN
// independently-minted NanoID (never derived from the patient's — see
// packages/clinic-ledger/scripts.go), so this lens read is the only way to
// resolve it. ArrearsDueAt/ArrearsRemindedFor/ArrearsReminderSentAt come off
// the account's own `.arrears` aspect (patientAccountsSpec's own doc
// comment, packages/clinic-ledger/lenses.go) — empty for a patient with no
// account, or for an account nothing has yet aged.
type patientAccountProjection struct {
	PatientKey            string `json:"patientKey"`
	AccountKey            string `json:"accountKey"`
	ArrearsDueAt          string `json:"arrearsDueAt"`
	ArrearsRemindedFor    string `json:"arrearsRemindedFor"`
	ArrearsReminderSentAt string `json:"arrearsReminderSentAt"`
}

// resolvePatientAccount scans the clinicPatientAccounts lens rows for the
// one matching patientKey, returning its projection (the zero value if the
// patient has none yet, including when no row projected at all — a patient
// the Refractor hasn't caught up to yet reads the same as one with no
// account).
func resolvePatientAccount(keys []string, get kvGetter, patientKey string) patientAccountProjection {
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p patientAccountProjection
		if json.Unmarshal(raw, &p) != nil || p.PatientKey != patientKey {
			continue
		}
		return p
	}
	return patientAccountProjection{}
}

// resolvePatientAccounts indexes the clinicPatientAccounts lens rows by
// patientKey in one pass — the arrears grid needs every visible patient's
// account projection at once, and calling resolvePatientAccount (a full
// rescan) once per patient found in the ledger bucket would redecode the
// same rows N times over.
func resolvePatientAccounts(keys []string, get kvGetter) map[string]patientAccountProjection {
	out := make(map[string]patientAccountProjection, len(keys))
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p patientAccountProjection
		if json.Unmarshal(raw, &p) != nil || p.PatientKey == "" {
			continue
		}
		out[p.PatientKey] = p
	}
	return out
}

// recordedOrDerivedDueDate prefers the account's own RECORDED arrears due
// date (EvaluateClinicArrears' stamp) over deriveStatement's FIFO-derived
// one — a stamp that names a date reads the recorded one when it exists
// (clinic-arrears-reminder-2026-09-15.md verdict item 5). deriveStatement's
// derivation is the fallback for an account no evaluation has touched yet.
func recordedOrDerivedDueDate(recorded, derived string) string {
	if recorded != "" {
		return recorded
	}
	return derived
}

// computeOverdue reports whether dueDate (RFC3339) has been reached by now,
// and by how many whole days — the same "reached, then +1" rule
// deriveStatement applies. Re-derived here, rather than reused from
// deriveStatement's own return, because the due date actually rendered may
// be the RECORDED one (recordedOrDerivedDueDate), not deriveStatement's own
// FIFO computation — isOverdue/daysOverdue must agree with whichever date is
// shown. A due date that fails to parse fails closed (not overdue), the same
// posture deriveStatement takes on a malformed postedAt.
func computeOverdue(dueDate string, now time.Time) (bool, int) {
	if dueDate == "" {
		return false, 0
	}
	due, err := time.Parse(time.RFC3339, dueDate)
	if err != nil {
		return false, 0
	}
	// Same boundary as clinic-ledger's own evaluation (`due_at <=
	// evaluated_at`, packages/clinic-ledger/scripts.go): due AT the instant
	// counts.
	if now.Before(due) {
		return false, 0
	}
	days := int(now.Sub(due).Hours()/24) + 1
	return true, days
}

// arrearsRow is one patient's balance/due-date/overdue statement for the
// front-desk debtor grid — GET /api/staff/arrears.
type arrearsRow struct {
	PatientKey     string `json:"patientKey"`
	AccountKey     string `json:"accountKey"`
	BalanceCents   int64  `json:"balanceCents"`
	DueDate        string `json:"dueDate"`
	IsOverdue      bool   `json:"isOverdue"`
	DaysOverdue    int    `json:"daysOverdue"`
	ReminderSentAt string `json:"reminderSentAt,omitempty"`
}

// computeArrears groups the ledgerHistory lens rows by patientKey — RESTRICTED
// to the visible set (the RLS-confined roster a caller of GET
// /api/staff/arrears may see: a patient session's own row only, staff the
// whole roster) — sorts and derives each visible patient's statement via
// sortLedgerRows/sumBalance/deriveStatement (the one rule computeLedgerHistory
// also applies, factored so both call sites can't drift apart), and keeps
// only the patients still owed money (balanceCents > 0) — a patient in
// credit or at exactly zero has nothing open and does not belong on a
// debtor grid. A projection row naming a patient outside the visible set, or
// carrying no patientKey/transactionKey (a tombstoned entry), never enters a
// group at all. Sorted worst-first — isOverdue desc, then daysOverdue desc,
// then balanceCents desc, then patientKey asc as a stable tiebreaker so the
// grid's order never depends on Go's randomized map iteration.
//
// Recorded-wins applies per row before the sort, off the same
// recordedOrDerivedDueDate/computeOverdue rule handleLedger uses: a patient
// account carrying a RECORDED arrears due date (EvaluateClinicArrears'
// stamp) shows that date, with isOverdue/daysOverdue recomputed against it,
// rather than deriveStatement's own FIFO derivation — so the grid orders on
// what is rendered, not on a derivation the recorded stamp has superseded.
// ReminderSentAt threads through from the same account projection
// unconditionally, as the account's own recorded SEND INTENT.
func computeArrears(keys []string, get kvGetter, visible map[string]bool, acctByPatient map[string]patientAccountProjection, now time.Time) []arrearsRow {
	byPatient := make(map[string][]ledgerEntryRow)
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p ledgerEntryProjection
		if json.Unmarshal(raw, &p) != nil || p.TransactionKey == "" || p.PatientKey == "" {
			continue
		}
		if !visible[p.PatientKey] {
			continue
		}
		var amount int64
		if p.AmountCents != nil {
			amount = int64(*p.AmountCents)
		}
		byPatient[p.PatientKey] = append(byPatient[p.PatientKey], ledgerEntryRow{
			TransactionKey: p.TransactionKey,
			Type:           p.Type,
			AmountCents:    amount,
			PostedAt:       p.PostedAt,
			ReversesKey:    p.ReversesKey,
		})
	}

	rows := make([]arrearsRow, 0, len(byPatient))
	for patientKey, entries := range byPatient {
		sortLedgerRows(entries)
		balance := sumBalance(entries)
		if balance <= 0 {
			continue
		}
		dueDate, _, _ := deriveStatement(entries, balance, now)
		acct := acctByPatient[patientKey]
		dueDate = recordedOrDerivedDueDate(acct.ArrearsDueAt, dueDate)
		isOverdue, daysOverdue := computeOverdue(dueDate, now)
		rows = append(rows, arrearsRow{
			PatientKey:     patientKey,
			AccountKey:     acct.AccountKey,
			BalanceCents:   balance,
			DueDate:        dueDate,
			IsOverdue:      isOverdue,
			DaysOverdue:    daysOverdue,
			ReminderSentAt: acct.ArrearsReminderSentAt,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].IsOverdue != rows[j].IsOverdue {
			return rows[i].IsOverdue
		}
		if rows[i].DaysOverdue != rows[j].DaysOverdue {
			return rows[i].DaysOverdue > rows[j].DaysOverdue
		}
		if rows[i].BalanceCents != rows[j].BalanceCents {
			return rows[i].BalanceCents > rows[j].BalanceCents
		}
		return rows[i].PatientKey < rows[j].PatientKey
	})
	return rows
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
	acctValues, err := readAllOrFail(acctKeys, func(key string) ([]byte, error) {
		entry, err := conn.KVGet(ctx, acctBucket, key)
		if err != nil {
			return nil, err
		}
		return entry.Value, nil
	})
	if err != nil {
		s.logger.Error("read patient accounts for ledger", "bucket", acctBucket, "error", err)
		s.writeError(w, http.StatusBadGateway, "read "+acctBucket+" incomplete: "+err.Error())
		return
	}
	acctGet := func(key string) ([]byte, bool) { v, ok := acctValues[key]; return v, ok }
	acct := resolvePatientAccount(acctKeys, acctGet, patientKey)
	accountKey := acct.AccountKey

	bucket := clinicledger.LedgerHistoryBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bucket+": "+err.Error()+" (is clinic-ledger installed and the Refractor projecting?)")
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
	rows, balance := computeLedgerHistory(keys, get, patientKey)

	// A due date (recorded or derived) only means something over an actual
	// open balance — mirrors deriveStatement's own "nothing to age" case,
	// applied whichever dueDate ends up rendered. reminderSentAt threads
	// through unconditionally: it is the account's own recorded SEND
	// INTENT, unrelated to whatever this request's own balance
	// recomputation finds. deriveStatement itself still runs unconditionally
	// (its per-row OpenCents/DueAt/IsOverdue/DaysOverdue annotation on rows
	// is needed regardless of the statement-level balance).
	now := time.Now().UTC()
	derivedDue, _, _ := deriveStatement(rows, balance, now)
	var dueDate string
	var isOverdue bool
	var daysOverdue int
	if balance > 0 {
		dueDate = recordedOrDerivedDueDate(acct.ArrearsDueAt, derivedDue)
		isOverdue, daysOverdue = computeOverdue(dueDate, now)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"patientKey":     patientKey,
		"accountKey":     accountKey,
		"transactions":   rows,
		"balanceCents":   balance,
		"dueDate":        dueDate,
		"isOverdue":      isOverdue,
		"daysOverdue":    daysOverdue,
		"reminderSentAt": acct.ArrearsReminderSentAt,
	})
}

// handleStaffArrears implements GET /api/staff/arrears — the front-desk
// debtor grid, served from the clinicLedgerHistory + clinicPatientAccounts
// lens read models (NOT Core KV, P5), mirroring wellness's
// handleFrontDeskArrears (cmd/wellness-app/ledger.go:479). The visible set is
// the RLS-confined roster queryPatients already serves (patients.go) — a
// patient session sees only its own row, staff the whole roster — reused as
// the arrears grid's own confinement rather than standing up new schema for
// a second protected model.
func (s *server) handleStaffArrears(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticateRead(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "authentication required: "+err.Error())
		return
	}
	if s.pgPool == nil {
		s.writeError(w, http.StatusBadGateway,
			"protected read model not configured (set CLINIC_APP_PG_DSN and ensure Postgres + the clinic-domain protected lens are up)")
		return
	}

	ctx, cancel := s.reqContext(r)
	defer cancel()

	patients, err := queryPatients(ctx, s.pgPool, actor.Subject, "")
	if err != nil {
		s.logger.Error("read protected clinic patients (arrears)", "error", err)
		s.writeError(w, http.StatusBadGateway, "could not read the protected patients model")
		return
	}
	visible := make(map[string]bool, len(patients))
	for _, p := range patients {
		visible[p.PatientKey] = true
	}
	if len(visible) == 0 {
		// An actor holding no wildcard grant and identifiedBy no patient sees
		// an empty roster (D1.5's RLS shape, not an error) — nothing to
		// group, so the lens buckets never need reading at all.
		s.writeJSON(w, http.StatusOK, map[string]any{"arrears": []arrearsRow{}, "count": 0})
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
	acctValues, err := readAllOrFail(acctKeys, func(key string) ([]byte, error) {
		entry, err := conn.KVGet(ctx, acctBucket, key)
		if err != nil {
			return nil, err
		}
		return entry.Value, nil
	})
	if err != nil {
		s.logger.Error("read patient accounts for arrears", "bucket", acctBucket, "error", err)
		s.writeError(w, http.StatusBadGateway, "read "+acctBucket+" incomplete: "+err.Error())
		return
	}
	acctGet := func(key string) ([]byte, bool) { v, ok := acctValues[key]; return v, ok }
	acctByPatient := resolvePatientAccounts(acctKeys, acctGet)

	bucket := clinicledger.LedgerHistoryBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bucket+": "+err.Error()+" (is clinic-ledger installed and the Refractor projecting?)")
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
		s.logger.Error("read ledger history for arrears", "bucket", bucket, "error", err)
		s.writeError(w, http.StatusBadGateway, "read "+bucket+" incomplete: "+err.Error())
		return
	}
	get := func(key string) ([]byte, bool) { v, ok := ledgerValues[key]; return v, ok }
	rows := computeArrears(keys, get, visible, acctByPatient, time.Now().UTC())
	s.writeJSON(w, http.StatusOK, map[string]any{"arrears": rows, "count": len(rows)})
}
