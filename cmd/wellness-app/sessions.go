package main

import (
	"encoding/json"
	"net/http"
	"sort"

	wellnessdomain "github.com/asolgan/lattice/packages/wellness-domain"
)

// sessionProjection is one row of the wellness-domain `wellnessSessions` lens.
type sessionProjection struct {
	SessionKey string   `json:"sessionKey"`
	Name       string   `json:"name"`
	StartsAt   string   `json:"startsAt"`
	EndsAt     string   `json:"endsAt"`
	Capacity   *float64 `json:"capacity"`
	StudioKey  string   `json:"studioKey"`
	StudioName string   `json:"studioName"`
}

// sessionRow is the schedule-grid row the Schedule view renders. BookedCount
// is deliberately NOT part of the wellnessSessions lens (the lens engine has
// no aggregate COUNT, per wellness-vertical-design.md) — this handler derives
// it here from the wellnessBookings lens, the same client-of-the-lens
// aggregation idiom cmd/cafe-app's computeTabs uses for its posted-total.
type sessionRow struct {
	SessionKey  string `json:"sessionKey"`
	Name        string `json:"name"`
	StartsAt    string `json:"startsAt"`
	EndsAt      string `json:"endsAt"`
	Capacity    int64  `json:"capacity"`
	StudioKey   string `json:"studioKey"`
	StudioName  string `json:"studioName"`
	BookedCount int    `json:"bookedCount"`
}

// computeSessions decodes every wellnessSessions row, joins each to its live
// booking count (from bookingKeys, one entry per live booking's sessionKey —
// CancelBooking's tombstone drops the booking row entirely, so a plain count
// of live rows is correct with no status filter needed), and sorts by
// startsAt for a chronological schedule grid. A row that fails to decode or
// carries no sessionKey (a tombstoned projection entry) is skipped.
func computeSessions(keys []string, get kvGetter, bookedCounts map[string]int) []sessionRow {
	rows := make([]sessionRow, 0, len(keys))
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p sessionProjection
		if json.Unmarshal(raw, &p) != nil || p.SessionKey == "" {
			continue
		}
		var capacity int64
		if p.Capacity != nil {
			capacity = int64(*p.Capacity)
		}
		rows = append(rows, sessionRow{
			SessionKey:  p.SessionKey,
			Name:        p.Name,
			StartsAt:    p.StartsAt,
			EndsAt:      p.EndsAt,
			Capacity:    capacity,
			StudioKey:   p.StudioKey,
			StudioName:  p.StudioName,
			BookedCount: bookedCounts[p.SessionKey],
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].StartsAt != rows[j].StartsAt {
			return rows[i].StartsAt < rows[j].StartsAt
		}
		return rows[i].SessionKey < rows[j].SessionKey
	})
	return rows
}

// countBookingsBySession tallies live wellnessBookings rows per sessionKey. A
// row that fails to decode or carries no bookingKey (a tombstoned projection
// entry) is skipped.
func countBookingsBySession(keys []string, get kvGetter) map[string]int {
	counts := make(map[string]int)
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p bookingProjection
		if json.Unmarshal(raw, &p) != nil || p.BookingKey == "" {
			continue
		}
		counts[p.SessionKey]++
	}
	return counts
}

// handleSessions implements GET /api/sessions — the schedule grid: every
// session across every studio, joined to its live seat count, served from the
// wellnessSessions + wellnessBookings lenses (P5).
func (s *server) handleSessions(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	sessionsBucket := wellnessdomain.WellnessSessionsBucket
	sessionKeys, err := conn.KVListKeys(ctx, sessionsBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+sessionsBucket+": "+err.Error()+" (is wellness-domain installed and the Refractor projecting?)")
		return
	}

	bookingsBucket := wellnessdomain.WellnessBookingsBucket
	bookingKeys, err := conn.KVListKeys(ctx, bookingsBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bookingsBucket+": "+err.Error()+" (is wellness-domain installed and the Refractor projecting?)")
		return
	}
	bookedCounts := countBookingsBySession(bookingKeys, s.kvGetter(ctx, bookingsBucket))

	rows := computeSessions(sessionKeys, s.kvGetter(ctx, sessionsBucket), bookedCounts)
	s.writeJSON(w, http.StatusOK, map[string]any{"sessions": rows})
}
