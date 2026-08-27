package main

import (
	"encoding/json"
	"net/http"
	"sort"

	wellnessdomain "github.com/operatinggraph/lattice/packages/wellness-domain"
)

// sessionProjection is one row of the wellness-domain `wellnessSessions` lens.
type sessionProjection struct {
	SessionKey string   `json:"sessionKey"`
	Name       string   `json:"name"`
	StartsAt   string   `json:"startsAt"`
	EndsAt     string   `json:"endsAt"`
	Capacity   *float64 `json:"capacity"`
	PriceCents *float64 `json:"priceCents"`
	// ResidentPriceCents is nil when the session declares no override (a
	// resident pays PriceCents like a standard booker), distinct from an
	// explicit 0 (a resident class, wellness-domain ddls.go) — the reassign
	// form's diff-only-if-changed edit needs that distinction preserved
	// through to the FE, so it stays a pointer all the way to sessionRow
	// rather than collapsing to 0 the way PriceCents does.
	ResidentPriceCents *float64 `json:"residentPriceCents"`
	StudioKey          string   `json:"studioKey"`
	StudioName         string   `json:"studioName"`
	// MissingStudio names the gap StudioKey/StudioName being empty otherwise
	// leaves ambiguous: CreateSession always writes a live atStudio link, so
	// an empty StudioKey here means the studio was later TombstoneStudio'd
	// out from under the session (verticals.md "retiring a studio strands
	// its classes"), not that the session never had one.
	MissingStudio  bool   `json:"missingStudio"`
	InstructorKey  string `json:"instructorKey"`
	InstructorName string `json:"instructorName"`
	// CoveringLocations is the staff read boundary's term: the studio's own
	// location plus its containedIn ancestors. Consumed by mayReadRoster, not
	// rendered — sessionRow deliberately does not carry it, so the schedule
	// grid keeps publishing no topology.
	CoveringLocations []string `json:"coveringLocations"`
}

// sessionRow is the schedule-grid row the Schedule view renders. BookedCount
// is deliberately NOT part of the wellnessSessions lens (the lens engine has
// no aggregate COUNT, per wellness-vertical-design.md) — this handler derives
// it here from the wellnessBookings lens, the same client-of-the-lens
// aggregation idiom cmd/cafe-app's computeTabs uses for its posted-total.
type sessionRow struct {
	SessionKey string `json:"sessionKey"`
	Name       string `json:"name"`
	StartsAt   string `json:"startsAt"`
	EndsAt     string `json:"endsAt"`
	Capacity   int64  `json:"capacity"`
	PriceCents int64  `json:"priceCents"`
	// ResidentPriceCents mirrors sessionProjection's own field — nil (omitted
	// from the JSON response) when the session declares no override.
	ResidentPriceCents *int64 `json:"residentPriceCents,omitempty"`
	StudioKey          string `json:"studioKey"`
	StudioName         string `json:"studioName"`
	MissingStudio      bool   `json:"missingStudio"`
	InstructorKey      string `json:"instructorKey"`
	InstructorName     string `json:"instructorName"`
	BookedCount        int    `json:"bookedCount"`
}

// computeSessions decodes every wellnessSessions row, joins each to its
// booked seat count (from bookedCounts, which already excludes waitlisted
// rows — see countBookingsBySession), and sorts by startsAt for a
// chronological schedule grid. A row that fails to decode or carries no
// sessionKey (a tombstoned projection entry) is skipped.
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
		var priceCents int64
		if p.PriceCents != nil {
			priceCents = int64(*p.PriceCents)
		}
		var residentPriceCents *int64
		if p.ResidentPriceCents != nil {
			v := int64(*p.ResidentPriceCents)
			residentPriceCents = &v
		}
		rows = append(rows, sessionRow{
			SessionKey:         p.SessionKey,
			Name:               p.Name,
			StartsAt:           p.StartsAt,
			EndsAt:             p.EndsAt,
			Capacity:           capacity,
			PriceCents:         priceCents,
			ResidentPriceCents: residentPriceCents,
			StudioKey:          p.StudioKey,
			StudioName:         p.StudioName,
			MissingStudio:      p.MissingStudio,
			InstructorKey:      p.InstructorKey,
			InstructorName:     p.InstructorName,
			BookedCount:        bookedCounts[p.SessionKey],
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

// countBookingsBySession tallies booked (occupying-a-seat) wellnessBookings
// rows per sessionKey. A waitlisted booking holds no seat cell — it is a
// claim on a waitlist slot, a disjoint dimension entirely (wellness-domain
// ddls.go) — so it is deliberately excluded here; counting it would make the
// schedule grid report a session as fuller than its seats actually are the
// moment anyone joins the waitlist. A row that fails to decode or carries no
// bookingKey (a tombstoned projection entry) is skipped.
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
		if p.Status == "waitlisted" {
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

// computeRosterSessions is computeSessions narrowed to the sessions this
// caller may read — mirroring mayReadRoster's own per-row test exactly
// (bookings.go), the authority behind this picker: an operator reads every
// row, a workplace staffer reads the sessions their workplace covers, and a
// bound instructor reads the sessions their own instructor entity leads, on
// top of any workplace they also hold. A row that fails to decode, or that
// none of the three answers reach, is simply absent rather than answered and
// then refused: the same distinction handleMembers draws for the front
// desk's member picker.
func computeRosterSessions(keys []string, get kvGetter, bookedCounts map[string]int, hats subjectHats) []sessionRow {
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
		leadsIt := p.InstructorKey != "" && p.InstructorKey == hats.instructorKey
		if !hats.isOperator && !hats.covers(p.CoveringLocations) && !leadsIt {
			continue
		}
		var capacity int64
		if p.Capacity != nil {
			capacity = int64(*p.Capacity)
		}
		var priceCents int64
		if p.PriceCents != nil {
			priceCents = int64(*p.PriceCents)
		}
		var residentPriceCents *int64
		if p.ResidentPriceCents != nil {
			v := int64(*p.ResidentPriceCents)
			residentPriceCents = &v
		}
		rows = append(rows, sessionRow{
			SessionKey:         p.SessionKey,
			Name:               p.Name,
			StartsAt:           p.StartsAt,
			EndsAt:             p.EndsAt,
			Capacity:           capacity,
			PriceCents:         priceCents,
			ResidentPriceCents: residentPriceCents,
			StudioKey:          p.StudioKey,
			StudioName:         p.StudioName,
			MissingStudio:      p.MissingStudio,
			InstructorKey:      p.InstructorKey,
			InstructorName:     p.InstructorName,
			BookedCount:        bookedCounts[p.SessionKey],
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

// handleRosterSessions implements GET /api/roster-sessions — the front
// desk's roster picker, narrowed to the sessions this caller may read
// (computeRosterSessions). /api/sessions stays public and unscoped for the
// member-facing schedule grid, which a resident needs to browse
// building-wide; this one is a staff/instructor surface, so a plain member
// gets the same refusal handleMembers gives rather than a topology-bearing
// list they cannot act on. Same rows, same shape as /api/sessions — the
// difference is only which ones this caller is offered.
func (s *server) handleRosterSessions(w http.ResponseWriter, r *http.Request) {
	hats, err := s.resolveSubjectHats(r)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	if !hats.isOperator && !hats.isStaff() && hats.instructorKey == "" {
		s.writeError(w, http.StatusForbidden,
			"the roster is a staff surface for the place you work at, or an instructor's own classes")
		return
	}
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

	rows := computeRosterSessions(sessionKeys, s.kvGetter(ctx, sessionsBucket), bookedCounts, hats)
	s.writeJSON(w, http.StatusOK, map[string]any{"sessions": rows})
}
