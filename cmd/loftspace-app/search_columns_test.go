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
