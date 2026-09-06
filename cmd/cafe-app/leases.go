package main

import (
	"encoding/json"
	"net/http"
	"sort"

	cafeledger "github.com/operatinggraph/lattice/packages/cafe-ledger"
)

// leaseAccountProjection is one row of the cafe-ledger `cafeLeaseAccounts`
// lens — one per lease, AccountKey empty until OpenTab's settlement has
// triggered CreateAccount for the first time. ArrearsDueAt and
// ArrearsReminderSentAt are informational columns off the account's own
// `.arrears` aspect (both empty for a lease with no account, or an account
// nothing has yet aged).
type leaseAccountProjection struct {
	LeaseAppKey           string `json:"leaseAppKey"`
	AccountKey            string `json:"accountKey"`
	ArrearsDueAt          string `json:"arrearsDueAt"`
	ArrearsReminderSentAt string `json:"arrearsReminderSentAt"`
}

// leaseRow is the lease-picker row the POS/front-desk views render.
type leaseRow struct {
	LeaseAppKey string `json:"leaseAppKey"`
	AccountKey  string `json:"accountKey,omitempty"`
}

// computeLeases decodes every cafeLeaseAccounts row, sorted by lease key for a
// stable picker order. A row that fails to decode or carries no
// leaseAppKey (a tombstoned projection entry) is skipped.
func computeLeases(keys []string, get kvGetter) []leaseRow {
	rows := make([]leaseRow, 0, len(keys))
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p leaseAccountProjection
		if json.Unmarshal(raw, &p) != nil || p.LeaseAppKey == "" {
			continue
		}
		rows = append(rows, leaseRow{LeaseAppKey: p.LeaseAppKey, AccountKey: p.AccountKey})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].LeaseAppKey < rows[j].LeaseAppKey })
	return rows
}

// handleLeases implements GET /api/leases — the leases the POS/front-desk
// pickers offer, served from the cafeLeaseAccounts lens (P5). A `worksAt`
// staffer sees the leases their workplace covers
// (facet-staff-worlds-design.md §9); a resident sees only the lease(s) they
// applied for (persona-worlds-design.md Fire W4 §3). This is a picker feeding
// the same grid handleTabs and the front-desk views render, so it has to be
// confined by the same term they are — an unconfined picker would put every
// building's lease keys in one building's staffer's hands, one hop from a read
// those handlers do confine.
func (s *server) handleLeases(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	hats, err := s.resolveSubjectHats(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	bucket := cafeledger.LeaseAccountsBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bucket+": "+err.Error()+" (is cafe-ledger installed and the Refractor projecting?)")
		return
	}
	rows := computeLeases(keys, s.kvGetter(ctx, bucket))

	visible, err := s.visibleLeases(ctx, hats)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	filtered := make([]leaseRow, 0, len(rows))
	for _, row := range rows {
		if visible.admits(row.LeaseAppKey) {
			filtered = append(filtered, row)
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"leases": filtered})
}
