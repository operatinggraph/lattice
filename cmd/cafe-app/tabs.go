package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	cafedomain "github.com/operatinggraph/lattice/packages/cafe-domain"
)

// weaverTargetsBucket is the shared cross-package Weaver convergence bucket
// every actorAggregate lens projects into, multiplexed by key prefix
// (packages/cafe-domain/lenses.go).
const weaverTargetsBucket = "weaver-targets"

// tabSettlementProjection is one row of the cafe-domain `cafeTabSettlement`
// convergence lens.
type tabSettlementProjection struct {
	TabKey         string   `json:"tabKey"`
	LeaseAppKey    string   `json:"leaseAppKey"`
	AccountKey     string   `json:"accountKey"`
	TotalCents     *float64 `json:"totalCents"`
	Status         string   `json:"status"`
	OpenedAt       string   `json:"openedAt"`
	SettledAt      string   `json:"settledAt"`
	MissingAccount bool     `json:"missing_account"`
	MissingCharge  bool     `json:"missing_charge"`
	Violating      bool     `json:"violating"`
}

// tabRow is the tab card the POS/front-desk views render.
type tabRow struct {
	TabKey      string `json:"tabKey"`
	LeaseAppKey string `json:"leaseAppKey"`
	AccountKey  string `json:"accountKey,omitempty"`
	TotalCents  int64  `json:"totalCents"`
	Status      string `json:"status"`
	OpenedAt    string `json:"openedAt"`
	SettledAt   string `json:"settledAt,omitempty"`
	Posted      bool   `json:"posted"`
}

// computeTabs decodes every cafeTabSettlement row keyed under this package's
// TabSettlementTarget prefix, optionally filtered to one lease. "Posted"
// (fully settled and its charge landed on the café ledger) is true exactly
// when the row is settled and neither gap is open — a settled, zero-total tab
// (never violates either gap) counts as posted too, since it never needed a
// posting. A row that fails to decode or carries no tabKey (a tombstoned
// projection entry) is skipped.
func computeTabs(keys []string, get kvGetter, leaseAppKey string) []tabRow {
	prefix := cafedomain.TabSettlementTarget + "."
	rows := make([]tabRow, 0)
	for _, k := range keys {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p tabSettlementProjection
		if json.Unmarshal(raw, &p) != nil || p.TabKey == "" {
			continue
		}
		if leaseAppKey != "" && p.LeaseAppKey != leaseAppKey {
			continue
		}
		var total int64
		if p.TotalCents != nil {
			total = int64(*p.TotalCents)
		}
		rows = append(rows, tabRow{
			TabKey:      p.TabKey,
			LeaseAppKey: p.LeaseAppKey,
			AccountKey:  p.AccountKey,
			TotalCents:  total,
			Status:      p.Status,
			OpenedAt:    p.OpenedAt,
			SettledAt:   p.SettledAt,
			Posted:      p.Status == "settled" && !p.MissingAccount && !p.MissingCharge,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].OpenedAt != rows[j].OpenedAt {
			return rows[i].OpenedAt < rows[j].OpenedAt
		}
		return rows[i].TabKey < rows[j].TabKey
	})
	return rows
}

// handleTabs implements GET /api/tabs[?leaseAppKey=] — the front-desk open-tabs
// list (unfiltered) and the POS/resident view's tab lookup for one lease,
// served from the cafeTabSettlement convergence lens (P5).
func (s *server) handleTabs(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	keys, err := conn.KVListKeys(ctx, weaverTargetsBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+weaverTargetsBucket+": "+err.Error()+" (is cafe-domain installed and the Weaver projecting?)")
		return
	}
	leaseAppKey := strings.TrimSpace(r.URL.Query().Get("leaseAppKey"))
	rows := computeTabs(keys, s.kvGetter(ctx, weaverTargetsBucket), leaseAppKey)
	s.writeJSON(w, http.StatusOK, map[string]any{"tabs": rows})
}
