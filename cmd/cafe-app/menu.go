package main

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
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
	MenuItemKey       string   `json:"menuItemKey"`
	Name              string   `json:"name"`
	PriceCents        *float64 `json:"priceCents"`
	ServedAt          string   `json:"servedAt"`
	CoveringLocations []string `json:"coveringLocations"`
	MissingLocation   bool     `json:"missingLocation"`
}

// menuItemRow is the self-order picker shape the Resident view renders (menu
// item key — the Charge{menuItemKey} target — + display name + price) and
// also what the staff Manage Menu grid renders: servedAt/coveringLocations/
// missingLocation ride along unused by the resident picker so the grid can
// badge an item that has outlived its place (SetMenuItemLocation's own repair
// target) without a second projection shape.
type menuItemRow struct {
	MenuItemKey       string   `json:"menuItemKey"`
	Name              string   `json:"name"`
	PriceCents        int64    `json:"priceCents"`
	ServedAt          string   `json:"servedAt,omitempty"`
	CoveringLocations []string `json:"coveringLocations,omitempty"`
	MissingLocation   bool     `json:"missingLocation,omitempty"`
}

// computeMenu assembles the self-order picker rows from the `menuCatalog`
// lens read model. A row that fails to decode or carries no menuItemKey (a
// tombstoned projection entry) is skipped.
//
// admit, when non-nil, confines the rows to a caller-specific bound: a
// leaseAppKey-scoped picker offers only what a Charge at that lease would
// actually accept (covering[p.ServedAt], cafe-domain's `location_covers`),
// and the workplace-only front-desk Manage Menu grid offers only what the
// staffer's own worksAt reaches (hats.covers(p.CoveringLocations)) — see
// handleMenu. A nil admit leaves the catalog unfiltered (an operator, or a
// resident/staffer with no scoping question in view).
func computeMenu(keys []string, get kvGetter, admit func(menuItemProjection) bool) []menuItemRow {
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
		if admit != nil && !admit(p) {
			continue
		}
		var price int64
		if p.PriceCents != nil {
			price = int64(*p.PriceCents)
		}
		rows = append(rows, menuItemRow{
			MenuItemKey:       p.MenuItemKey,
			Name:              p.Name,
			PriceCents:        price,
			ServedAt:          p.ServedAt,
			CoveringLocations: p.CoveringLocations,
			MissingLocation:   p.MissingLocation,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows
}

// dedupeMostSpecific collapses same-named rows down to the most specific
// covering item. It applies ONLY to the leaseAppKey offer-picker view (a
// resident or staffer selecting what to charge against one lease), where
// menu coverage is hierarchical: a building-level item and a unit-level item
// can both cover the same lease, and if they share a display name the picker
// shows "Croissant — $3.50" twice with nothing to tell them apart — the
// picker must show one collapsed row, not two indistinguishable ones. row is
// shadowed by other when other's own location is strictly nested inside
// row's — i.e. row's ServedAt shows up in other's ancestor chain
// (CoveringLocations) — so only a genuine ancestor/descendant pair
// collapses. Two same-named rows at unrelated locations (a staffer covering
// two separate buildings) share no such relation and both stay, since a
// real containment match is required.
// It must NOT apply to the staff Manage Menu grid (workplace-confined, no
// leaseAppKey) or the unscoped operator catalog: both of those need to see
// every item independently — including a building-level item that shares a
// name with a unit-level one — so staff can reprice or retire either one.
func dedupeMostSpecific(rows []menuItemRow) []menuItemRow {
	byName := make(map[string][]menuItemRow, len(rows))
	for _, r := range rows {
		byName[r.Name] = append(byName[r.Name], r)
	}
	out := make([]menuItemRow, 0, len(rows))
	for _, r := range rows {
		shadowed := false
		for _, other := range byName[r.Name] {
			if other.MenuItemKey == r.MenuItemKey {
				continue
			}
			if r.ServedAt != other.ServedAt && slices.Contains(other.CoveringLocations, r.ServedAt) {
				shadowed = true
				break
			}
		}
		if !shadowed {
			out = append(out, r)
		}
	}
	return out
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
// Resident self-order picker AND the staff Manage Menu grid both render,
// served from the cafe-domain menuCatalog lens (P5). With a leaseAppKey, a
// caller not admitted to that lease (visibleLeases, mirroring handleTabs —
// admits a staffer's workplace-covered leases too) is refused, and the
// catalog returned is confined to items a Charge against that lease would
// actually accept — the offer side of the same location_covers bound Charge
// already enforces. With no leaseAppKey, a `worksAt` staffer (the front-desk
// Manage Menu grid — loadManageMenu, cafe-app/web/app.js) is confined to
// items their own workplace reaches (menuCatalogSpec's coveringLocations
// intersected against hats.workplaces, the staffCoveredLeases idiom applied
// per item instead of per lease) — the same building/unit relationship
// RetireMenuItem's own operator/frontOfHouse write path would otherwise let
// this grid show an item for and never confine. An operator, or a plain
// resident asking with no lease/workplace question in view at all (the
// self-order picker's own public-catalog case), sees the whole catalog
// unfiltered.
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
	var admit func(menuItemProjection) bool
	dedupe := false
	switch {
	case leaseAppKey != "":
		visible, err := s.visibleLeases(ctx, hats)
		if err != nil {
			s.writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		if !visible.admits(leaseAppKey) {
			s.writeError(w, http.StatusForbidden, notYourLease(hats))
			return
		}
		covering := s.leaseCoveringLocations(ctx, leaseAppKey)
		admit = func(p menuItemProjection) bool { return covering[p.ServedAt] }
		dedupe = true
	case hats.isStaff() && !hats.isOperator:
		admit = func(p menuItemProjection) bool { return hats.covers(p.CoveringLocations) }
	}

	keys, err := conn.KVListKeys(ctx, cafedomain.MenuCatalogBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+cafedomain.MenuCatalogBucket+": "+err.Error()+" (is cafe-domain installed and the Weaver projecting?)")
		return
	}
	rows := computeMenu(keys, s.kvGetter(ctx, cafedomain.MenuCatalogBucket), admit)
	if dedupe {
		rows = dedupeMostSpecific(rows)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"menu": rows})
}
