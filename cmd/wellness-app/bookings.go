package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/substrate"
	wellnessdomain "github.com/operatinggraph/lattice/packages/wellness-domain"
)

// bookingProjection is one row of the wellness-domain `wellnessBookings` lens.
type bookingProjection struct {
	BookingKey   string   `json:"bookingKey"`
	Status       string   `json:"status"`
	Rate         string   `json:"rate"`
	WaitlistSlot *float64 `json:"waitlistSlot"`
	SessionKey   string   `json:"sessionKey"`
	SessionName  string   `json:"sessionName"`
	StartsAt     string   `json:"startsAt"`
	EndsAt       string   `json:"endsAt"`
	StudioKey    string   `json:"studioKey"`
	StudioName   string   `json:"studioName"`
	BookerKey    string   `json:"bookerKey"`
}

// bookingRow is the roster / my-classes row a view renders. Status carries
// booked | waitlisted | attended | noShow so the roster can show who the
// instructor has already marked, and My Classes can show a waitlisted
// booker's place in line. WaitlistSlot is only set when Status is
// "waitlisted".
type bookingRow struct {
	BookingKey   string   `json:"bookingKey"`
	Status       string   `json:"status"`
	Rate         string   `json:"rate"`
	WaitlistSlot *float64 `json:"waitlistSlot"`
	SessionKey   string   `json:"sessionKey"`
	SessionName  string   `json:"sessionName"`
	StartsAt     string   `json:"startsAt"`
	EndsAt       string   `json:"endsAt"`
	StudioKey    string   `json:"studioKey"`
	StudioName   string   `json:"studioName"`
	BookerKey    string   `json:"bookerKey"`
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
		// The rendered row mirrors the projection field-for-field; the two stay
		// separate types so a future lens column is a deliberate decision to
		// expose rather than an automatic one.
		rows = append(rows, bookingRow(p))
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].StartsAt != rows[j].StartsAt {
			return rows[i].StartsAt < rows[j].StartsAt
		}
		return rows[i].BookingKey < rows[j].BookingKey
	})
	return rows
}

// handleBookings implements GET /api/bookings[?sessionKey=] — the My Classes
// view's own-class list and the Roster view's per-session seat list, served
// from the wellnessBookings lens (P5) behind the session.
//
// There is deliberately NO bookerKey parameter. Whose classes these are comes
// from the verified session and nowhere else: a client-supplied identity
// filter is precisely the vector clinic closed when it deleted
// `/api/appointments?provider=` (cmd/clinic-app/appointments.go), and here it
// let any caller read any resident's whole class history.
//
// With no sessionKey the answer is the caller's OWN bookings — a member's My
// Classes, and the same for a staffer or instructor, who are also members.
// With a sessionKey it is that session's roster, which is a staff or
// bound-instructor surface: a `worksAt` staffer sees the rosters of sessions
// at a location they work at, an instructor only the rosters of sessions their
// own instructor entity leads (persona-worlds-design.md §7.3), and a plain
// member none.
func (s *server) handleBookings(w http.ResponseWriter, r *http.Request) {
	// The own-bookings answer needs only the session's subject, so it does not
	// consult the Gateway at all — My Classes keeps serving through a /v1/actor
	// outage. Only the roster branch needs the caller's hats, and it is the
	// branch that must fail closed when they cannot be resolved.
	subject, err := s.authenticateRead(r)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	sessionKey := strings.TrimSpace(r.URL.Query().Get("sessionKey"))
	bookerKey := auth.IdentityKeyPrefix + subject
	if sessionKey != "" {
		hats, err := s.resolveSubjectHats(r)
		if err != nil {
			s.writeAuthError(w, err)
			return
		}
		allowed, err := s.mayReadRoster(ctx, hats, sessionKey)
		if err != nil {
			s.logger.Error("resolve whether the caller may read this roster", "error", err)
			s.writeError(w, http.StatusBadGateway,
				"could not read the class schedule to check this roster; try again")
			return
		}
		if !allowed {
			s.writeError(w, http.StatusForbidden,
				"a roster is a staff surface for the place you work at, or an instructor's own class; this session is neither")
			return
		}
		// A roster is every seat in the session, not the caller's own.
		bookerKey = ""
	}

	bucket := wellnessdomain.WellnessBookingsBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bucket+": "+err.Error()+" (is wellness-domain installed and the Refractor projecting?)")
		return
	}
	rows := computeBookings(keys, s.kvGetter(ctx, bucket), sessionKey, bookerKey)
	s.writeJSON(w, http.StatusOK, map[string]any{"bookings": rows})
}

// mayReadRoster reports whether hats may read sessionKey's full seat list. An
// operator reads any, the exemption `require_workplace` gives root on the write
// side. A `worksAt` staffer may read the rosters their workplace covers — the session's
// studio location or a location containing it, the same reach
// `require_workplace` gives that staffer's writes; a bound instructor may read
// a session their own instructor entity is the projected `ledBy` of; nobody
// else may. Both facts come from the wellnessSessions lens (P5) — the same
// projection the schedule renders — so this needs no Core-KV read.
func (s *server) mayReadRoster(ctx context.Context, hats subjectHats, sessionKey string) (bool, error) {
	if hats.isOperator {
		return true, nil
	}
	if !hats.isStaff() && hats.instructorKey == "" {
		return false, nil
	}
	bucket := wellnessdomain.WellnessSessionsBucket
	// Read directly rather than through kvGetter, which collapses "no such
	// key" and "the read failed" into one false. Those must not be conflated:
	// an absent session legitimately denies (nothing establishes that this
	// instructor leads it), but a NATS blip is an outage, and reporting it as
	// a denial would tell an instructor their own class is not theirs.
	entry, err := s.conn.KVGet(ctx, bucket, sessionKey)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("read %s from %s: %w", sessionKey, bucket, err)
	}
	var p sessionProjection
	if json.Unmarshal(entry.Value, &p) != nil {
		return false, fmt.Errorf("decode %s from %s", sessionKey, bucket)
	}
	if hats.covers(p.CoveringLocations) {
		return true, nil
	}
	return p.InstructorKey != "" && p.InstructorKey == hats.instructorKey, nil
}
