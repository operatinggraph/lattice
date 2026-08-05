package main

import "testing"

func TestComputeMenu_SortsAndSkipsUndecodable(t *testing.T) {
	keys, get := fakeKV(map[string]any{
		"cafe-menu-catalog.b": map[string]any{"menuItemKey": "vtx.menuitem.b", "name": "Latte", "priceCents": 450},
		"cafe-menu-catalog.a": map[string]any{"menuItemKey": "vtx.menuitem.a", "name": "Croissant", "priceCents": 350},
		"cafe-menu-catalog.n": map[string]any{}, // undecodable (no menuItemKey) — skipped
	})
	rows := computeMenu(keys, get, nil)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d (%+v)", len(rows), rows)
	}
	if rows[0].Name != "Croissant" || rows[1].Name != "Latte" {
		t.Errorf("want sorted by name (Croissant, Latte), got (%s, %s)", rows[0].Name, rows[1].Name)
	}
	if rows[0].MenuItemKey != "vtx.menuitem.a" || rows[0].PriceCents != 350 {
		t.Errorf("unexpected row content: %+v", rows[0])
	}
	if rows[1].MenuItemKey != "vtx.menuitem.b" || rows[1].PriceCents != 450 {
		t.Errorf("unexpected row content: %+v", rows[1])
	}
}

func TestComputeMenu_CoveringFiltersToServedAtLocations(t *testing.T) {
	keys, get := fakeKV(map[string]any{
		"cafe-menu-catalog.a": map[string]any{"menuItemKey": "vtx.menuitem.a", "name": "Croissant", "priceCents": 350, "servedAt": "vtx.location.building1"},
		"cafe-menu-catalog.b": map[string]any{"menuItemKey": "vtx.menuitem.b", "name": "Latte", "priceCents": 450, "servedAt": "vtx.location.building2"},
		"cafe-menu-catalog.c": map[string]any{"menuItemKey": "vtx.menuitem.c", "name": "Muffin", "priceCents": 300}, // no servedAt — excluded under a covering filter
	})
	covering := map[string]bool{"vtx.location.building1": true}
	admit := func(p menuItemProjection) bool { return covering[p.ServedAt] }
	rows := computeMenu(keys, get, admit)
	if len(rows) != 1 || rows[0].MenuItemKey != "vtx.menuitem.a" {
		t.Fatalf("want only the item served at the covered location, got %+v", rows)
	}
}

func TestComputeMenu_NilAdmitLeavesCatalogUnfiltered(t *testing.T) {
	keys, get := fakeKV(map[string]any{
		"cafe-menu-catalog.a": map[string]any{"menuItemKey": "vtx.menuitem.a", "name": "Croissant", "priceCents": 350, "servedAt": "vtx.location.building1"},
		"cafe-menu-catalog.c": map[string]any{"menuItemKey": "vtx.menuitem.c", "name": "Muffin", "priceCents": 300},
	})
	rows := computeMenu(keys, get, nil)
	if len(rows) != 2 {
		t.Fatalf("want both items with no admit filter, got %+v", rows)
	}
}

// TestComputeMenu_WorkplaceAdmitFiltersToCoveringLocationsIntersection proves
// the front-desk grid's own predicate shape: an item is admitted when ANY of
// its coveringLocations (menuCatalogSpec's own ancestor chain) appears in the
// staffer's workplace set — a building-level item (its own servedAt IS the
// staffer's building) matches, and so does a unit-level item whose
// containedIn chain reaches the building, but an item served at an unrelated
// building does not.
func TestComputeMenu_WorkplaceAdmitFiltersToCoveringLocationsIntersection(t *testing.T) {
	keys, get := fakeKV(map[string]any{
		"cafe-menu-catalog.a": map[string]any{
			"menuItemKey": "vtx.menuitem.a", "name": "Croissant", "priceCents": 350,
			"servedAt": "vtx.location.unit4b", "coveringLocations": []string{"vtx.location.unit4b", "vtx.location.riverside"},
		},
		"cafe-menu-catalog.b": map[string]any{
			"menuItemKey": "vtx.menuitem.b", "name": "Latte", "priceCents": 450,
			"servedAt": "vtx.location.otherbuilding", "coveringLocations": []string{"vtx.location.otherbuilding"},
		},
	})
	staffWorkplaces := map[string]bool{"vtx.location.riverside": true}
	admit := func(p menuItemProjection) bool {
		for _, loc := range p.CoveringLocations {
			if staffWorkplaces[loc] {
				return true
			}
		}
		return false
	}
	rows := computeMenu(keys, get, admit)
	if len(rows) != 1 || rows[0].MenuItemKey != "vtx.menuitem.a" {
		t.Fatalf("want only the item whose covering chain reaches the staffer's building, got %+v", rows)
	}
}
