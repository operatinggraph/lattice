package main

import (
	"encoding/json"
	"net/http"

	frontdesk "github.com/asolgan/lattice/packages/front-desk"
)

// bookingRow is one row of the front-desk `frontDeskBookings` lens
// (packages/front-desk/lenses.go) — decoded straight off the wire and
// served as-is: the "booked class" badge the front-desk grid joins onto a
// resident's open-tab card, client-side, by leaseAppKey — the same
// composition idiom cmd/cafe-app's computeTabs and wellness-domain's
// deliberately-uncounted bookedCount already use.
type bookingRow struct {
	BookingKey  string `json:"bookingKey"`
	LeaseAppKey string `json:"leaseAppKey"`
	SessionName string `json:"sessionName"`
	StartsAt    string `json:"startsAt"`
}

// computeFrontDeskBookings decodes every frontDeskBookings row in the
// front-desk-bookings bucket. A row that fails to decode or carries no
// leaseAppKey is skipped (mirrors computeTabs' tombstoned-entry guard).
func computeFrontDeskBookings(keys []string, get kvGetter) []bookingRow {
	rows := make([]bookingRow, 0)
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p bookingRow
		if json.Unmarshal(raw, &p) != nil || p.LeaseAppKey == "" {
			continue
		}
		rows = append(rows, p)
	}
	return rows
}

// handleFrontDeskBookings implements GET /api/frontdesk-bookings — the
// resident's booked-class badge for the front-desk grid, served from the
// front-desk package's frontDeskBookings lens (P5). A stack without
// front-desk installed simply has no such bucket; that reads back as "no
// rows," not an error, so the front-desk view still renders (just without
// class badges) rather than failing the whole page.
func (s *server) handleFrontDeskBookings(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	keys, err := conn.KVListKeys(ctx, frontdesk.BookingsBucket)
	if err != nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"bookings": []bookingRow{}})
		return
	}
	rows := computeFrontDeskBookings(keys, s.kvGetter(ctx, frontdesk.BookingsBucket))
	s.writeJSON(w, http.StatusOK, map[string]any{"bookings": rows})
}
