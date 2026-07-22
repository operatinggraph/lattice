package main

import (
	"encoding/json"
	"net/http"
	"sort"

	cafedomain "github.com/operatinggraph/lattice/packages/cafe-domain"
)

// menuItemProjection is one row of the cafe-domain `menuCatalog` lens, read
// from its NATS-KV read-model bucket (P5: an application reads the lens
// projection, never Core KV).
type menuItemProjection struct {
	MenuItemKey string   `json:"menuItemKey"`
	Name        string   `json:"name"`
	PriceCents  *float64 `json:"priceCents"`
}

// menuItemRow is the self-order picker shape the Resident view renders: the
// menu item key (the Charge{menuItemKey} target) + display name + price.
type menuItemRow struct {
	MenuItemKey string `json:"menuItemKey"`
	Name        string `json:"name"`
	PriceCents  int64  `json:"priceCents"`
}

// computeMenu assembles the self-order picker rows from the `menuCatalog`
// lens read model. A row that fails to decode or carries no menuItemKey (a
// tombstoned projection entry) is skipped.
func computeMenu(keys []string, get kvGetter) []menuItemRow {
	rows := make([]menuItemRow, 0)
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p menuItemProjection
		if json.Unmarshal(raw, &p) != nil || p.MenuItemKey == "" {
			continue
		}
		var price int64
		if p.PriceCents != nil {
			price = int64(*p.PriceCents)
		}
		rows = append(rows, menuItemRow{MenuItemKey: p.MenuItemKey, Name: p.Name, PriceCents: price})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows
}

// handleMenu implements GET /api/menu — the self-order catalog the Resident
// view's item picker renders, served from the cafe-domain menuCatalog lens
// (P5).
func (s *server) handleMenu(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	keys, err := conn.KVListKeys(ctx, cafedomain.MenuCatalogBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+cafedomain.MenuCatalogBucket+": "+err.Error()+" (is cafe-domain installed and the Weaver projecting?)")
		return
	}
	rows := computeMenu(keys, s.kvGetter(ctx, cafedomain.MenuCatalogBucket))
	s.writeJSON(w, http.StatusOK, map[string]any{"menu": rows})
}
