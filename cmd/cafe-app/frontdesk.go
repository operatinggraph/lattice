package main

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
	cafeledger "github.com/operatinggraph/lattice/packages/cafe-ledger"
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
// error either time. Every listed key is then read via readAllOrFail rather
// than the lossy per-key kvGetter: a KVGet error on a key that just came
// back from KVListKeys is a real fetch fault, not evidence the row doesn't
// exist (ledger.go's readAllOrFail doc comment) — letting it fall through as
// absent would silently drop a resident's booked-class badge instead of
// failing the request, the same shape handleFrontDeskBalances already
// avoids. Front Desk is a STAFF-ONLY surface (persona-worlds-design.md
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
	values, err := readAllOrFail(keys, func(key string) ([]byte, error) {
		entry, err := conn.KVGet(ctx, frontdesk.BookingsBucket, key)
		if err != nil {
			return nil, err
		}
		return entry.Value, nil
	})
	if err != nil {
		s.logger.Error("read front-desk bookings", "bucket", frontdesk.BookingsBucket, "error", err)
		s.writeError(w, http.StatusBadGateway, "read "+frontdesk.BookingsBucket+" incomplete: "+err.Error())
		return
	}
	get := func(key string) ([]byte, bool) { v, ok := values[key]; return v, ok }
	rows := computeFrontDeskBookings(keys, get)
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
// handleFrontDeskBookings — any other list failure 502s, and every listed
// key is read via readAllOrFail, same as there. Staff-only and
// workplace-confined, same as handleFrontDeskBookings.
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
	values, err := readAllOrFail(keys, func(key string) ([]byte, error) {
		entry, err := conn.KVGet(ctx, frontdesk.LeaseDetailsBucket, key)
		if err != nil {
			return nil, err
		}
		return entry.Value, nil
	})
	if err != nil {
		s.logger.Error("read front-desk lease details", "bucket", frontdesk.LeaseDetailsBucket, "error", err)
		s.writeError(w, http.StatusBadGateway, "read "+frontdesk.LeaseDetailsBucket+" incomplete: "+err.Error())
		return
	}
	get := func(key string) ([]byte, bool) { v, ok := values[key]; return v, ok }
	rows := computeFrontDeskLeaseDetails(keys, get)
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
// handleFrontDeskBookings — any other list failure 502s, and every listed
// key is read via readAllOrFail, same as there. Staff-only and
// workplace-confined, same as handleFrontDeskBookings.
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
	values, err := readAllOrFail(keys, func(key string) ([]byte, error) {
		entry, err := conn.KVGet(ctx, frontdesk.VisitsBucket, key)
		if err != nil {
			return nil, err
		}
		return entry.Value, nil
	})
	if err != nil {
		s.logger.Error("read front-desk visits", "bucket", frontdesk.VisitsBucket, "error", err)
		s.writeError(w, http.StatusBadGateway, "read "+frontdesk.VisitsBucket+" incomplete: "+err.Error())
		return
	}
	get := func(key string) ([]byte, bool) { v, ok := values[key]; return v, ok }
	rows := computeFrontDeskVisits(keys, get)
	filtered := make([]visitRow, 0, len(rows))
	for _, row := range rows {
		if visible.admits(row.LeaseAppKey) {
			filtered = append(filtered, row)
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"visits": filtered})
}

// handleFrontDeskBalances implements GET /api/frontdesk-balances — every
// visible lease's balance/due-date/overdue statement for the front-desk
// grid, served from the cafe-ledger package's cafeLedgerHistory lens (P5,
// cmd/cafe-app/ledger.go's computeLedgerBalances). Staff-only and
// workplace-confined, same as handleFrontDeskBookings. Unlike this file's
// other three joins, cafe-ledger is a core cafe package always installed
// alongside cafe-domain — this bucket going missing is not the ordinary
// "optional package not installed" case — but the read still answers 200
// empty on substrate.IsBucketNotFound rather than inventing a different
// posture for what is otherwise the same failure shape. Every key is read
// via readAllOrFail, same as the other three: a balance is money, and a
// KVGet error on a key that just came back from KVListKeys is a real fetch
// fault, not evidence the row doesn't exist (ledger.go's readAllOrFail doc
// comment) — letting it fall through as absent would silently understate a
// lease's balance instead of failing the request.
func (s *server) handleFrontDeskBalances(w http.ResponseWriter, r *http.Request) {
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

	bucket := cafeledger.LedgerHistoryBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		if substrate.IsBucketNotFound(err) {
			s.writeJSON(w, http.StatusOK, map[string]any{"balances": []balanceRow{}})
			return
		}
		s.logger.Error("list front-desk balances", "bucket", bucket, "error", err)
		s.writeError(w, http.StatusBadGateway, "list "+bucket+": "+err.Error())
		return
	}
	values, err := readAllOrFail(keys, func(key string) ([]byte, error) {
		entry, err := conn.KVGet(ctx, bucket, key)
		if err != nil {
			return nil, err
		}
		return entry.Value, nil
	})
	if err != nil {
		s.logger.Error("read ledger history for front-desk balances", "bucket", bucket, "error", err)
		s.writeError(w, http.StatusBadGateway, "read "+bucket+" incomplete: "+err.Error())
		return
	}
	get := func(key string) ([]byte, bool) { v, ok := values[key]; return v, ok }
	rows := computeLedgerBalances(keys, get, time.Now().UTC())
	filtered := make([]balanceRow, 0, len(rows))
	for _, row := range rows {
		if visible.admits(row.LeaseAppKey) {
			filtered = append(filtered, row)
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"balances": filtered})
}
