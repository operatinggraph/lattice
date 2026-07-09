package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	wellnessdomain "github.com/asolgan/lattice/packages/wellness-domain"
)

// bookingProjection is one row of the wellness-domain `wellnessBookings` lens.
type bookingProjection struct {
	BookingKey  string `json:"bookingKey"`
	Status      string `json:"status"`
	Rate        string `json:"rate"`
	SessionKey  string `json:"sessionKey"`
	SessionName string `json:"sessionName"`
	StartsAt    string `json:"startsAt"`
	EndsAt      string `json:"endsAt"`
	BookerKey   string `json:"bookerKey"`
}

// bookingRow is the roster / my-classes row a view renders.
type bookingRow struct {
	BookingKey  string `json:"bookingKey"`
	Rate        string `json:"rate"`
	SessionKey  string `json:"sessionKey"`
	SessionName string `json:"sessionName"`
	StartsAt    string `json:"startsAt"`
	EndsAt      string `json:"endsAt"`
	BookerKey   string `json:"bookerKey"`
}

// computeBookings decodes every wellnessBookings row, optionally filtered to
// one session (the Roster view) or one booker (the My Classes view — at most
// one filter is applied per call), sorted chronologically. A row that fails
// to decode or carries no bookingKey (a tombstoned projection entry) is
// skipped.
func computeBookings(keys []string, get kvGetter, sessionKey, bookerKey string) []bookingRow {
	rows := make([]bookingRow, 0)
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p bookingProjection
		if json.Unmarshal(raw, &p) != nil || p.BookingKey == "" {
			continue
		}
		if sessionKey != "" && p.SessionKey != sessionKey {
			continue
		}
		if bookerKey != "" && p.BookerKey != bookerKey {
			continue
		}
		rows = append(rows, bookingRow{
			BookingKey:  p.BookingKey,
			Rate:        p.Rate,
			SessionKey:  p.SessionKey,
			SessionName: p.SessionName,
			StartsAt:    p.StartsAt,
			EndsAt:      p.EndsAt,
			BookerKey:   p.BookerKey,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].StartsAt != rows[j].StartsAt {
			return rows[i].StartsAt < rows[j].StartsAt
		}
		return rows[i].BookingKey < rows[j].BookingKey
	})
	return rows
}

// handleBookings implements GET /api/bookings[?sessionKey=|bookerKey=] — the
// Roster view's per-session seat list and the My Classes view's per-booker
// class list, served from the wellnessBookings lens (P5).
func (s *server) handleBookings(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	bucket := wellnessdomain.WellnessBookingsBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bucket+": "+err.Error()+" (is wellness-domain installed and the Refractor projecting?)")
		return
	}
	sessionKey := strings.TrimSpace(r.URL.Query().Get("sessionKey"))
	bookerKey := strings.TrimSpace(r.URL.Query().Get("bookerKey"))
	rows := computeBookings(keys, s.kvGetter(ctx, bucket), sessionKey, bookerKey)
	s.writeJSON(w, http.StatusOK, map[string]any{"bookings": rows})
}
