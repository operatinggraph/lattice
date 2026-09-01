package main

import (
	"encoding/json"
	"net/http"

	"github.com/operatinggraph/lattice/internal/substrate"
	frontdesk "github.com/operatinggraph/lattice/packages/front-desk"
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
// front-desk installed has no such bucket; that reads back as "no rows,"
// not an error, so the front-desk view still renders (just without class
// badges) rather than failing the whole page. Any OTHER KVListKeys failure
// (a saturated stack, a transient projection fault) is a real read failure
// and must 502 — silently returning an empty list is indistinguishable from
// "nobody is here today" and was reaching residents as a blank grid with no
// error either time. Front Desk is a STAFF-ONLY surface (persona-worlds-design.md
// Fire W4 §3): a resident is refused rather than served an empty or partial
// grid, and a staffer is served only the leases their workplace covers
// (facet-staff-worlds-design.md §9). The best-effort posture covers THIS
// package's bucket only: a missing cafeLeaseWorkplaces bucket is a 502,
// because that one is the confinement source and an empty grid would read as
// "nobody is here today" rather than "this app cannot tell who you may see."
func (s *server) handleFrontDeskBookings(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	hats, err := s.resolveSubjectHats(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if !hats.isFrontDesk() && !hats.isOperator {
		s.writeError(w, http.StatusForbidden, "front desk is a staff-only view")
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	visible, err := s.visibleLeases(ctx, hats)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	keys, err := conn.KVListKeys(ctx, frontdesk.BookingsBucket)
	if err != nil {
		if substrate.IsBucketNotFound(err) {
			s.writeJSON(w, http.StatusOK, map[string]any{"bookings": []bookingRow{}})
			return
		}
		s.logger.Error("list front-desk bookings", "bucket", frontdesk.BookingsBucket, "error", err)
		s.writeError(w, http.StatusBadGateway, "list "+frontdesk.BookingsBucket+": "+err.Error())
		return
	}
	rows := computeFrontDeskBookings(keys, s.kvGetter(ctx, frontdesk.BookingsBucket))
	filtered := make([]bookingRow, 0, len(rows))
	for _, row := range rows {
		if visible.admits(row.LeaseAppKey) {
			filtered = append(filtered, row)
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"bookings": filtered})
}

// leaseDetailRow is one row of the front-desk `frontDeskLeaseDetails` lens
// (packages/front-desk/lenses.go) — decoded straight off the wire and
// served as-is: the lease term/rent the front-desk grid joins onto a
// resident's open-tab card, client-side, by leaseAppKey, the same
// composition idiom bookingRow above already uses.
type leaseDetailRow struct {
	LeaseAppKey     string  `json:"leaseAppKey"`
	UnitAddress     string  `json:"unitAddress"`
	UnitRent        float64 `json:"unitRent"`
	UnitCurrency    string  `json:"unitCurrency"`
	UnitLeaseTermMo float64 `json:"unitLeaseTermMonths"`
}

// computeFrontDeskLeaseDetails decodes every frontDeskLeaseDetails row in
// the front-desk-lease-details bucket. A row that fails to decode or
// carries no leaseAppKey is skipped (mirrors computeFrontDeskBookings).
func computeFrontDeskLeaseDetails(keys []string, get kvGetter) []leaseDetailRow {
	rows := make([]leaseDetailRow, 0)
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p leaseDetailRow
		if json.Unmarshal(raw, &p) != nil || p.LeaseAppKey == "" {
			continue
		}
		rows = append(rows, p)
	}
	return rows
}

// handleFrontDeskLeaseDetails implements GET /api/frontdesk-lease-details —
// every resident's applied-to unit rent/term for the front-desk grid,
// served from the front-desk package's frontDeskLeaseDetails lens (P5). A
// stack without front-desk installed has no such bucket; that reads back as
// "no rows," not an error, same best-effort posture as
// handleFrontDeskBookings — any other list failure 502s, same as there.
// Staff-only and workplace-confined, same as handleFrontDeskBookings.
func (s *server) handleFrontDeskLeaseDetails(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	hats, err := s.resolveSubjectHats(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if !hats.isFrontDesk() && !hats.isOperator {
		s.writeError(w, http.StatusForbidden, "front desk is a staff-only view")
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	visible, err := s.visibleLeases(ctx, hats)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	keys, err := conn.KVListKeys(ctx, frontdesk.LeaseDetailsBucket)
	if err != nil {
		if substrate.IsBucketNotFound(err) {
			s.writeJSON(w, http.StatusOK, map[string]any{"leaseDetails": []leaseDetailRow{}})
			return
		}
		s.logger.Error("list front-desk lease details", "bucket", frontdesk.LeaseDetailsBucket, "error", err)
		s.writeError(w, http.StatusBadGateway, "list "+frontdesk.LeaseDetailsBucket+": "+err.Error())
		return
	}
	rows := computeFrontDeskLeaseDetails(keys, s.kvGetter(ctx, frontdesk.LeaseDetailsBucket))
	filtered := make([]leaseDetailRow, 0, len(rows))
	for _, row := range rows {
		if visible.admits(row.LeaseAppKey) {
			filtered = append(filtered, row)
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"leaseDetails": filtered})
}

// visitRow is one row of the front-desk `frontDeskVisits` lens
// (packages/front-desk/lenses.go, Inc 5) — decoded straight off the wire and
// served as-is: the "upcoming clinic visit" badge the front-desk grid joins
// onto a resident's open-tab card, client-side, by leaseAppKey, the same
// composition idiom bookingRow above uses. Deliberately carries only
// existence + time — the lens itself never projects the visit reason or any
// clinical content (front-desk's VisitsBucket doc comment).
type visitRow struct {
	AppointmentKey string `json:"appointmentKey"`
	LeaseAppKey    string `json:"leaseAppKey"`
	StartsAt       string `json:"startsAt"`
	EndsAt         string `json:"endsAt"`
}

// computeFrontDeskVisits decodes every frontDeskVisits row in the
// front-desk-visits bucket. A row that fails to decode or carries no
// leaseAppKey is skipped (mirrors computeFrontDeskBookings).
func computeFrontDeskVisits(keys []string, get kvGetter) []visitRow {
	rows := make([]visitRow, 0)
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p visitRow
		if json.Unmarshal(raw, &p) != nil || p.LeaseAppKey == "" {
			continue
		}
		rows = append(rows, p)
	}
	return rows
}

// handleFrontDeskVisits implements GET /api/frontdesk-visits — the
// resident's upcoming-clinic-visit badge for the front-desk grid, served
// from the front-desk package's frontDeskVisits lens (P5). A stack without
// front-desk (or clinic-domain) installed has no such bucket; that reads
// back as "no rows," not an error, same best-effort posture as
// handleFrontDeskBookings — any other list failure 502s, same as there.
// Staff-only and workplace-confined, same as handleFrontDeskBookings.
func (s *server) handleFrontDeskVisits(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	hats, err := s.resolveSubjectHats(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if !hats.isFrontDesk() && !hats.isOperator {
		s.writeError(w, http.StatusForbidden, "front desk is a staff-only view")
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	visible, err := s.visibleLeases(ctx, hats)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	keys, err := conn.KVListKeys(ctx, frontdesk.VisitsBucket)
	if err != nil {
		if substrate.IsBucketNotFound(err) {
			s.writeJSON(w, http.StatusOK, map[string]any{"visits": []visitRow{}})
			return
		}
		s.logger.Error("list front-desk visits", "bucket", frontdesk.VisitsBucket, "error", err)
		s.writeError(w, http.StatusBadGateway, "list "+frontdesk.VisitsBucket+": "+err.Error())
		return
	}
	rows := computeFrontDeskVisits(keys, s.kvGetter(ctx, frontdesk.VisitsBucket))
	filtered := make([]visitRow, 0, len(rows))
	for _, row := range rows {
		if visible.admits(row.LeaseAppKey) {
			filtered = append(filtered, row)
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"visits": filtered})
}
