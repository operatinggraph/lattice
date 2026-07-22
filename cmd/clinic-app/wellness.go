package main

import (
	"encoding/json"
	"net/http"
	"sort"

	wellnessdomain "github.com/operatinggraph/lattice/packages/wellness-domain"
)

// wellnessSessionProjection is one row of wellness-domain's wellnessSessions lens.
type wellnessSessionProjection struct {
	SessionKey string   `json:"sessionKey"`
	Name       string   `json:"name"`
	StartsAt   string   `json:"startsAt"`
	EndsAt     string   `json:"endsAt"`
	Capacity   *float64 `json:"capacity"`
	StudioName string   `json:"studioName"`
}

// wellnessBookingProjection is the subset of wellness-domain's wellnessBookings
// lens this handler needs to derive a live seat count per session — the lens
// has no aggregate COUNT (wellness-vertical-design.md), the same
// client-of-the-lens aggregation idiom cmd/wellness-app/sessions.go uses.
type wellnessBookingProjection struct {
	BookingKey string `json:"bookingKey"`
	SessionKey string `json:"sessionKey"`
}

// wellnessSessionRow is a bookable class the clinic worklist's Care→Wellness
// referral CTA offers from a completed appointment.
type wellnessSessionRow struct {
	SessionKey  string `json:"sessionKey"`
	Name        string `json:"name"`
	StartsAt    string `json:"startsAt"`
	EndsAt      string `json:"endsAt"`
	Capacity    int64  `json:"capacity"`
	StudioName  string `json:"studioName"`
	BookedCount int    `json:"bookedCount"`
}

// computeWellnessBookedCounts tallies live wellnessBookings rows per
// sessionKey. A row that fails to decode or carries no bookingKey (a
// tombstoned projection entry) is skipped.
func computeWellnessBookedCounts(keys []string, get kvGetter) map[string]int {
	counts := make(map[string]int)
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p wellnessBookingProjection
		if json.Unmarshal(raw, &p) != nil || p.BookingKey == "" {
			continue
		}
		counts[p.SessionKey]++
	}
	return counts
}

// computeWellnessSessions decodes every wellnessSessions row, joins each to
// its live booking count, and sorts by startsAt for a chronological picker. A
// row that fails to decode or carries no sessionKey (a tombstoned projection
// entry) is skipped.
func computeWellnessSessions(keys []string, get kvGetter, bookedCounts map[string]int) []wellnessSessionRow {
	rows := make([]wellnessSessionRow, 0, len(keys))
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p wellnessSessionProjection
		if json.Unmarshal(raw, &p) != nil || p.SessionKey == "" {
			continue
		}
		var capacity int64
		if p.Capacity != nil {
			capacity = int64(*p.Capacity)
		}
		rows = append(rows, wellnessSessionRow{
			SessionKey:  p.SessionKey,
			Name:        p.Name,
			StartsAt:    p.StartsAt,
			EndsAt:      p.EndsAt,
			Capacity:    capacity,
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

// handleWellnessSessions implements GET /api/wellness/sessions — the
// bookable-class picker the clinic worklist's completed-appointment card
// offers (Care→Wellness referral): every wellness-domain session joined to
// its live seat count, read from the wellness-domain package's own
// wellness-sessions / wellness-bookings NATS-KV lens buckets (P5). This is a
// vertical app reading a DIFFERENT package's lens bucket directly — the same
// established cross-package read precedent cmd/cafe-app already uses against
// packages/front-desk and cmd/clinic-app's own handleResidents already uses
// against lease-signing's weaver-targets bucket
// (mixed-use-composition-design.md) — no new primitive.
func (s *server) handleWellnessSessions(w http.ResponseWriter, r *http.Request) {
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
	getSessions := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, sessionsBucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}
	getBookings := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, bookingsBucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}
	bookedCounts := computeWellnessBookedCounts(bookingKeys, getBookings)
	rows := computeWellnessSessions(sessionKeys, getSessions, bookedCounts)
	s.writeJSON(w, http.StatusOK, map[string]any{"sessions": rows})
}
