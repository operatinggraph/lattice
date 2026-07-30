package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	cafedomain "github.com/operatinggraph/lattice/packages/cafe-domain"
)

// menuItemProjection is one row of the cafe-domain `menuCatalog` lens, read
// from its NATS-KV read-model bucket (P5: an application reads the lens
// projection, never Core KV). servedAt is the item's own serving-location
// key — empty for an item minted before the column existed or with no
// servedAt link.
type menuItemProjection struct {
	MenuItemKey string   `json:"menuItemKey"`
	Name        string   `json:"name"`
	PriceCents  *float64 `json:"priceCents"`
	ServedAt    string   `json:"servedAt"`
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
//
// covering, when non-nil, confines the rows to what a Charge at this lease
// would actually accept: an item is offered only when its own servedAt key
// appears in the set (cafe-domain's `location_covers` — the tab's own
// building plus every containedIn ancestor covering it). An item with no
// servedAt link is excluded rather than defaulting to visible, the same
// fail-closed answer Charge itself gives an unresolvable item location. A
// nil covering leaves the catalog unfiltered (the front-desk grid view,
// which has no single lease/tab in view to confine against).
func computeMenu(keys []string, get kvGetter, covering map[string]bool) []menuItemRow {
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
		if covering != nil && !covering[p.ServedAt] {
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

// leaseCoveringLocations reads the one cafeLeaseWorkplaces row for leaseAppKey
// (the bucket is keyed by the lease's own key, lenses.go's leaseWorkplacesSpec)
// and returns its coveringLocations as a set. Fails CLOSED the same way
// staffCoveredLeases does: a row that is missing, undecodable, or carries no
// leaseAppKey — an unwired lease, a stack where cafe-domain has not yet
// converged this projection, or the bucket not installed at all — yields an
// empty set, which a self-order picker reads as "nothing is offerable" rather
// than "everything is."
func (s *server) leaseCoveringLocations(ctx context.Context, leaseAppKey string) map[string]bool {
	covering := map[string]bool{}
	raw, ok := s.kvGetter(ctx, cafedomain.LeaseWorkplacesBucket)(leaseAppKey)
	if !ok {
		return covering
	}
	var p leaseWorkplaceProjection
	if json.Unmarshal(raw, &p) != nil || p.LeaseAppKey == "" {
		return covering
	}
	for _, loc := range p.CoveringLocations {
		loc = strings.TrimSpace(loc)
		if loc != "" {
			covering[loc] = true
		}
	}
	return covering
}

// handleMenu implements GET /api/menu[?leaseAppKey=] — the catalog the
// Resident self-order picker AND the staff POS catalog picker both render,
// served from the cafe-domain menuCatalog lens (P5). With no leaseAppKey the
// catalog is unfiltered (the front-desk grid view, which has no single
// lease/tab in view). With one, a caller not admitted to that lease
// (visibleLeases, mirroring handleTabs — admits a staffer's workplace-covered
// leases too) is refused, and the catalog returned is confined to items a
// Charge against that lease would actually accept — the offer side of the same
// location_covers bound Charge already enforces.
func (s *server) handleMenu(w http.ResponseWriter, r *http.Request) {
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

	leaseAppKey := strings.TrimSpace(r.URL.Query().Get("leaseAppKey"))
	var covering map[string]bool
	if leaseAppKey != "" {
		visible, err := s.visibleLeases(ctx, hats)
		if err != nil {
			s.writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		if !visible.admits(leaseAppKey) {
			s.writeError(w, http.StatusForbidden, notYourLease(hats))
			return
		}
		covering = s.leaseCoveringLocations(ctx, leaseAppKey)
	}

	keys, err := conn.KVListKeys(ctx, cafedomain.MenuCatalogBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+cafedomain.MenuCatalogBucket+": "+err.Error()+" (is cafe-domain installed and the Weaver projecting?)")
		return
	}
	rows := computeMenu(keys, s.kvGetter(ctx, cafedomain.MenuCatalogBucket), covering)
	s.writeJSON(w, http.StatusOK, map[string]any{"menu": rows})
}
