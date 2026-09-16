package main

import (
	"regexp"
	"testing"
)

// The landlord search surface renders the same applicant rows the landlord
// list does (renderRLSApplicantRow → decisionOffered, the disposition chips),
// off a narrower SELECT list. Every disposition the FE keys a decision or a
// chip on is a boolean column of read_landlord_lease_applications, so a
// terminal state the lens gains (lost_to_rival, qualified, …) reaches the search
// surface only if searchLandlordColumns selects it — an omitted boolean reads
// as undefined in the FE, which is falsy, and the search hit re-offers
// Approve/Decline on an application the list beside it already closed. Every
// boolean column of the lens's pinned column set must be selected here; the
// facts the search surface deliberately omits (dates, doc pointers) are not
// booleans.
func TestSearchLandlordColumns_SelectEveryLensBoolean(t *testing.T) {
	selected := map[string]bool{}
	for _, name := range regexp.MustCompile(`[a-z_]+`).FindAllString(searchLandlordColumns, -1) {
		selected[name] = true
	}
	for _, col := range landlordProtectedColumns() {
		if col.Type != "boolean" {
			continue
		}
		if !selected[col.Name] {
			t.Errorf("read_landlord_lease_applications boolean column %q is not selected by searchLandlordColumns; the search surface's rows would read it as undefined", col.Name)
		}
	}
}

// searchLandlordChipTextColumns are the non-boolean read_landlord_lease_applications
// columns a search-surface render function reads directly (dispChip / a
// disposition line), beyond the boolean sweep above: renderSearchApplicationRow's
// notice chip reads a.noticeMoveOutAt (app.js). Extend this list — and its
// renderer — together whenever a search row grows another text-column chip.
var searchLandlordChipTextColumns = []string{"notice_move_out_at"}

// TestSearchLandlordColumns_SelectsEveryChipTextColumn is
// TestSearchLandlordColumns_SelectEveryLensBoolean's non-boolean sibling: a
// text column a search-row renderer reads for a chip must be selected by
// searchLandlordColumns too, the same "omitted column reads as undefined"
// failure mode the boolean pin guards, just not caught by a Type=="boolean"
// filter.
func TestSearchLandlordColumns_SelectsEveryChipTextColumn(t *testing.T) {
	selected := map[string]bool{}
	for _, name := range regexp.MustCompile(`[a-z_]+`).FindAllString(searchLandlordColumns, -1) {
		selected[name] = true
	}
	known := map[string]bool{}
	for _, col := range landlordProtectedColumns() {
		known[col.Name] = true
	}
	for _, name := range searchLandlordChipTextColumns {
		if !known[name] {
			t.Fatalf("searchLandlordChipTextColumns names %q, which is not a read_landlord_lease_applications column at all — landlordProtectedColumns() is stale or the name is wrong", name)
		}
		if !selected[name] {
			t.Errorf("read_landlord_lease_applications column %q is read by a search-row chip but not selected by searchLandlordColumns; the search surface's rows would read it as undefined", name)
		}
	}
}
