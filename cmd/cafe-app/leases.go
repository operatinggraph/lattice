package main

import (
	"encoding/json"
	"net/http"
	"sort"

	cafeledger "github.com/operatinggraph/lattice/packages/cafe-ledger"
)

// leaseAccountProjection is one row of the cafe-ledger `cafeLeaseAccounts`
// lens — one per lease, AccountKey empty until OpenTab's settlement has
// triggered CreateAccount for the first time.
type leaseAccountProjection struct {
	LeaseAppKey string `json:"leaseAppKey"`
	AccountKey  string `json:"accountKey"`
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
		rows = append(rows, leaseRow(p))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].LeaseAppKey < rows[j].LeaseAppKey })
	return rows
}

// handleLeases implements GET /api/leases — every lease the POS/front-desk
// pickers offer, served from the cafeLeaseAccounts lens (P5). A `worksAt`
// staffer sees every lease; a resident sees only the lease(s) they applied
// for (persona-worlds-design.md Fire W4 §3).
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

	if !hats.isStaff {
		own, err := s.residentOwnLeases(ctx, hats.identityID)
		if err != nil {
			s.writeError(w, http.StatusBadGateway, "resolve resident's own leases: "+err.Error())
			return
		}
		filtered := make([]leaseRow, 0, len(rows))
		for _, row := range rows {
			if own[row.LeaseAppKey] {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"leases": rows})
}
