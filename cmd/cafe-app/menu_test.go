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
// TestComputeMenu_PropagatesMissingLocation proves computeMenu carries the
// menuCatalog lens's own missingLocation flag through to the row it returns —
// the Manage Menu grid badges/relocate-button decision (app.js's
// menuItemCard) reads this field, not servedAt's emptiness, so it must
// survive the projection→row translation.
func TestComputeMenu_PropagatesMissingLocation(t *testing.T) {
	keys, get := fakeKV(map[string]any{
		"cafe-menu-catalog.a": map[string]any{"menuItemKey": "vtx.menuitem.a", "name": "Croissant", "priceCents": 350, "missingLocation": true},
		"cafe-menu-catalog.b": map[string]any{"menuItemKey": "vtx.menuitem.b", "name": "Latte", "priceCents": 450, "servedAt": "vtx.location.building1", "missingLocation": false},
	})
	rows := computeMenu(keys, get, nil)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d (%+v)", len(rows), rows)
	}
	if !rows[0].MissingLocation {
		t.Errorf("Croissant: want MissingLocation=true, got %+v", rows[0])
	}
	if rows[1].MissingLocation {
		t.Errorf("Latte: want MissingLocation=false, got %+v", rows[1])
	}
}

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

// TestDedupeMostSpecific_BuildingAndUnitLevelItem_KeepsOnlyTheUnitOne proves
// the live shape the PO found: a building-level "Croissant" and a
// unit-level "Croissant" both admitted for one lease collapse to the more
// specific (unit) row instead of appearing twice with nothing to tell them
// apart.
func TestDedupeMostSpecific_BuildingAndUnitLevelItem_KeepsOnlyTheUnitOne(t *testing.T) {
	rows := []menuItemRow{
		{MenuItemKey: "vtx.menuitem.building", Name: "Croissant", PriceCents: 350,
			ServedAt: "vtx.location.riverside", CoveringLocations: []string{"vtx.location.riverside"}},
		{MenuItemKey: "vtx.menuitem.unit", Name: "Croissant", PriceCents: 350,
			ServedAt: "vtx.location.unit4b", CoveringLocations: []string{"vtx.location.unit4b", "vtx.location.riverside"}},
	}
	out := dedupeMostSpecific(rows)
	if len(out) != 1 || out[0].MenuItemKey != "vtx.menuitem.unit" {
		t.Fatalf("want only the unit-level item, got %+v", out)
	}
}

// TestDedupeMostSpecific_UnrelatedBuildings_KeepsBoth proves two same-named
// items admitted from UNRELATED locations (a staffer covering two separate
// buildings) are left alone — collapsing requires a real ancestor/descendant
// pair, and neither item's location is the other's ancestor here.
func TestDedupeMostSpecific_UnrelatedBuildings_KeepsBoth(t *testing.T) {
	rows := []menuItemRow{
		{MenuItemKey: "vtx.menuitem.a", Name: "Croissant", PriceCents: 350,
			ServedAt: "vtx.location.riverside", CoveringLocations: []string{"vtx.location.riverside"}},
		{MenuItemKey: "vtx.menuitem.b", Name: "Croissant", PriceCents: 375,
			ServedAt: "vtx.location.lakeside", CoveringLocations: []string{"vtx.location.lakeside"}},
	}
	out := dedupeMostSpecific(rows)
	if len(out) != 2 {
		t.Fatalf("want both unrelated items kept, got %+v", out)
	}
}

// TestDedupeMostSpecific_DifferentNames_Untouched proves the filter never
// drops a row over a same-location item with a different name.
func TestDedupeMostSpecific_DifferentNames_Untouched(t *testing.T) {
	rows := []menuItemRow{
		{MenuItemKey: "vtx.menuitem.a", Name: "Croissant", ServedAt: "vtx.location.unit4b", CoveringLocations: []string{"vtx.location.unit4b", "vtx.location.riverside"}},
		{MenuItemKey: "vtx.menuitem.b", Name: "Latte", ServedAt: "vtx.location.riverside", CoveringLocations: []string{"vtx.location.riverside"}},
	}
	out := dedupeMostSpecific(rows)
	if len(out) != 2 {
		t.Fatalf("want both distinct-named items kept, got %+v", out)
	}
}
