package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/substrate"
	wellnessdomain "github.com/operatinggraph/lattice/packages/wellness-domain"
	wellnessledger "github.com/operatinggraph/lattice/packages/wellness-ledger"
)

// ledgerEntryProjection is one row of the wellness-ledger `wellnessLedgerHistory`
// lens, read from its NATS-KV read-model bucket (P5 — never Core KV).
// BookingKey/ClassName/ClassStartsAt are empty for the common transaction (a
// payment or a class-price charge settles no booking) — only a no-show
// debit's settles hop populates them, which is what lets the FE tell two
// otherwise-identical "No-show fee" lines apart by the class each one billed.
type ledgerEntryProjection struct {
	TransactionKey string   `json:"transactionKey"`
	AccountKey     string   `json:"accountKey"`
	IdentityKey    string   `json:"identityKey"`
	Type           string   `json:"type"`
	AmountCents    *float64 `json:"amountCents"`
	Memo           string   `json:"memo"`
	PostedAt       string   `json:"postedAt"`
	BookingKey     string   `json:"bookingKey"`
	ClassName      string   `json:"className"`
	ClassStartsAt  string   `json:"classStartsAt"`
	Reason         string   `json:"reason"`
}

// ledgerEntryRow is the billing-history row the FE renders. Reason is empty
// for a debit (charge-only rows never carry it) and "payment"/"waiver" for a
// credit — the FE labels a waiver distinctly so forgiven debt is never
// mistaken for cash collected.
type ledgerEntryRow struct {
	TransactionKey string `json:"transactionKey"`
	Type           string `json:"type"`
	AmountCents    int64  `json:"amountCents"`
	Memo           string `json:"memo,omitempty"`
	PostedAt       string `json:"postedAt"`
	ClassName      string `json:"className,omitempty"`
	ClassStartsAt  string `json:"classStartsAt,omitempty"`
	Reason         string `json:"reason,omitempty"`
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
			ClassName:      p.ClassName,
			ClassStartsAt:  p.ClassStartsAt,
			Reason:         p.Reason,
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

// memberVisibleToHats reports whether targetIdentityKey is a member hats'
// workplace covers — reuses the exact wellnessMembers-lens confinement
// /api/members' picker uses (computeCoveredMembers, residents.go) as the
// ledger's staff-read gate, rather than standing up a second confinement
// mechanism for the same fact. Fails CLOSED throughout that helper: an
// operator sees every member, a staffer sees only the ones their `worksAt`
// location covers, and a caller with no workplace covers nothing.
func (s *server) memberVisibleToHats(ctx context.Context, hats subjectHats, targetIdentityKey string) (bool, error) {
	bucket := wellnessdomain.WellnessMembersBucket
	keys, err := s.conn.KVListKeys(ctx, bucket)
	if err != nil {
		return false, err
	}
	for _, row := range computeCoveredMembers(keys, s.kvGetter(ctx, bucket), hats) {
		if row.BookerKey == targetIdentityKey {
			return true, nil
		}
	}
	return false, nil
}

// handleLedger implements GET /api/ledger — the signed-in member's own
// billing history by default, served from the wellnessLedgerHistory +
// wellnessMemberAccounts lens read models (P5, never Core KV). Whose ledger
// this is comes from the verified session unless the caller supplies
// ?identityKey=, which is gated to staff (isStaff/isOperator) AND checked
// against memberVisibleToHats above — the same confinement /api/members
// already enforces for its picker, not a fresh unauthenticated party filter
// (that vector is the one clinic closed when it deleted
// `/api/appointments?provider=` — the fix there was adding a visibility
// check, not banning party filters outright; clinic's own `/api/ledger?
// patientKey=` proves the pattern, gated by Postgres RLS instead of a lens).
// wellness-app's Roster billing panel needs this to record a front-desk
// charge/payment against a member OTHER than the signed-in session.
//
// Returns the member's transaction rows, the running balance, and the
// account key — empty when no wellnessaccount has been opened for them yet.
// A blank accountKey is the normal, expected shape for a member who hasn't
// been charged yet, not an error.
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
	if target := strings.TrimSpace(r.URL.Query().Get("identityKey")); target != "" && target != identityKey {
		hats, err := s.resolveSubjectHats(r)
		if err != nil {
			s.writeAuthError(w, err)
			return
		}
		if !hats.isFrontDesk() && !hats.isOperator {
			s.writeError(w, http.StatusForbidden,
				"another member's ledger is a staff surface for the place you work at")
			return
		}
		visible, err := s.memberVisibleToHats(ctx, hats, target)
		if err != nil {
			s.logger.Error("check member visibility for ledger read", "error", err)
			s.writeError(w, http.StatusBadGateway, "could not verify read access")
			return
		}
		if !visible {
			s.writeError(w, http.StatusForbidden, "member not visible to this actor")
			return
		}
		identityKey = target
	}

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
		"identityKey":  identityKey,
		"accountKey":   accountKey,
		"transactions": rows,
		"balanceCents": balance,
	})
}
