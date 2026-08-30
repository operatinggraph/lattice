package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	cafedomain "github.com/operatinggraph/lattice/packages/cafe-domain"
	frontdesk "github.com/operatinggraph/lattice/packages/front-desk"
)

// serviceAttachLookbackDays is the "this period" window for the
// service-attach-rate KPI: a lease counts as attached if it used a service
// (a wellness booking, a café tab) any time in the last N days, not only
// right this second — a settled tab or a class already attended/no-showed
// is real usage, not noise. 30 days mirrors the monthly cadence the
// landlord's own one-bill statement already bills on.
const serviceAttachLookbackDays = 30

// Portfolio-pulse (mixed-use-composition-design.md Inc 2 + Inc 3): the
// landlord-facing "how full is my portfolio, and is it being used" view.
//
// Occupancy (Inc 2) reads the protected landlordUnitsRead Postgres model.
// Sibling of handleLandlordApplications — identical verified-JWT -> per-request
// txn -> SET LOCAL lattice.actor_id -> RLS path, but this reads EVERY unit the
// landlord manages, independent of whether it has ever had a lease application
// (landlordLeaseApplicationsRead requires a leaseapp to exist at all, so a
// never-applied-to unit is invisible to it).
//
// Service-attach-rate (Inc 3) is occupancy's cross-package sibling — of the
// landlord's currently-occupied (signed) leases, what fraction used a
// wellness booking or café tab within the last serviceAttachLookbackDays.
// It joins three lens read-models entirely client-side (this app already
// reads landlordLeaseApplicationsRead for the occupied-lease set;
// front-desk-booking-history and cafe-domain's cafeTabSettlement are both
// global NATS-KV buckets keyed/filterable by leaseAppKey, the same join key
// front-desk's own FE already uses, packages/front-desk/lenses.go) — the
// precedent for an app reading a DIFFERENT package's lens bucket already
// exists twice (cmd/cafe-app reads front-desk's bucket; this app already
// reads packages/privacy-base's), so this is applying an established
// pattern, not inventing one. Best-effort: unlike occupancy (which 502s if
// Postgres is down), a missing NATS connection or an unreadable bucket
// degrades attach-rate to zero/omitted rather than failing the whole
// portfolio-pulse response — the same posture front-desk-bookings itself
// takes ("no bucket = no rows, not an error").
//
// "Attached" reads frontDeskBookingHistory (any booking status, not just
// currently-booked) and cafeTabSettlement gated on a startsAt/settledAt
// window, not raw existence — a lease whose class already happened or whose
// tab already settled still used the service; see computeServiceAttachRate.

// portfolioPulseUnit is one row of the occupancy breakdown: a unit the
// landlord manages, plus its coarse listing status. UnitStatus is empty when
// the unit was never listed (landlordUnitsRead projects unit_status null) —
// a distinct bucket from any of the four listed statuses.
type portfolioPulseUnit struct {
	UnitKey    string   `json:"unitKey"`
	UnitStatus string   `json:"unitStatus"`
	UnitRent   *float64 `json:"unitRent"`
}

// portfolioPulseResult is the GET /api/portfolio-pulse response: the flat
// per-unit rows plus the aggregate occupancy counts the FE renders as the
// pulse card. OccupancyRate is leased/total, 0 when the landlord manages no
// units (never divides by zero).
type portfolioPulseResult struct {
	Units         []portfolioPulseUnit `json:"units"`
	TotalUnits    int                  `json:"totalUnits"`
	Leased        int                  `json:"leased"`
	Available     int                  `json:"available"`
	Pending       int                  `json:"pending"`
	Withdrawn     int                  `json:"withdrawn"`
	NotListed     int                  `json:"notListed"`
	OccupancyRate float64              `json:"occupancyRate"`
	// Service-attach-rate (Inc 3): OccupiedLeases is the count this rate is
	// over (the landlord's currently-signed leases, independent of Leased
	// above — a unit can be listed "leased" slightly ahead of/behind its
	// application's signed_at during convergence); ServiceAttached is how
	// many of those used a wellness booking or café tab within
	// serviceAttachLookbackDays. Both are 0, and ServiceAttachRate is 0, when
	// the cross-package read is unavailable — the FE distinguishes "0
	// attached of N" from "no data" by checking OccupiedLeases > 0 first.
	OccupiedLeases    int     `json:"occupiedLeases"`
	ServiceAttached   int     `json:"serviceAttached"`
	ServiceAttachRate float64 `json:"serviceAttachRate"`
}

// weaverTargetsBucket is the shared cross-package Weaver convergence bucket
// every actorAggregate lens projects into, multiplexed by key prefix — the
// same bucket cmd/cafe-app's own weaverTargetsBucket constant names
// (packages/cafe-domain/lenses.go).
const weaverTargetsBucket = "weaver-targets"

// serviceBookingRow is the front-desk-booking-history lens row
// (packages/front-desk), narrowed to the fields the attach-rate joins/windows
// on. Status is any of booked/waitlisted/attended/noShow — Waitlisted is
// excluded at the call site below since the resident never actually got a
// seat, so it isn't service usage.
type serviceBookingRow struct {
	LeaseAppKey string `json:"leaseAppKey"`
	Status      string `json:"status"`
	StartsAt    string `json:"startsAt"`
}

// serviceTabRow is the cafeTabSettlement convergence-lens row
// (packages/cafe-domain), narrowed to the leaseAppKey + status + settledAt
// the attach-rate joins/windows on (mirrors cmd/cafe-app's
// tabSettlementProjection).
type serviceTabRow struct {
	LeaseAppKey string `json:"leaseAppKey"`
	Status      string `json:"status"`
	SettledAt   string `json:"settledAt"`
}

// selectLandlordUnitsSQL reads the protected occupancy model. No auth WHERE —
// RLS scopes the rows to the requesting landlord via the txn-local
// lattice.actor_id session variable, same as selectLandlordApplicationsSQL.
const selectLandlordUnitsSQL = `
SELECT unit_key, COALESCE(unit_status, ''), unit_rent
FROM read_landlord_units
ORDER BY unit_key`

// queryLandlordUnits runs the protected landlord occupancy read inside a
// per-request transaction with a txn-local actor session variable — the same
// pooling-safety pattern as queryLandlordApplications.
func queryLandlordUnits(ctx context.Context, pool pgxBeginner, actorID string) ([]portfolioPulseUnit, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actorID); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, selectLandlordUnitsSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]portfolioPulseUnit, 0)
	for rows.Next() {
		var u portfolioPulseUnit
		if err := rows.Scan(&u.UnitKey, &u.UnitStatus, &u.UnitRent); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// dedupeUnitsByKey collapses a co-managed unit's one-row-per-landlord fan-out
// (landlordUnitsReadSpec MATCH REQUIRES `manages`, packages/loftspace-domain/
// lenses.go) down to one row per distinct unit. Every fanned-out row for the
// same unit carries the same status/rent (they're the same unit), so keeping
// the first occurrence loses no information; the SQL's `ORDER BY unit_key`
// keeps a unit's fanned-out rows adjacent.
func dedupeUnitsByKey(units []portfolioPulseUnit) []portfolioPulseUnit {
	seen := make(map[string]bool, len(units))
	out := make([]portfolioPulseUnit, 0, len(units))
	for _, u := range units {
		if seen[u.UnitKey] {
			continue
		}
		seen[u.UnitKey] = true
		out = append(out, u)
	}
	return out
}

// summarizePortfolioPulse folds the flat per-unit rows into the aggregate
// counts the FE card renders. A pure function of the RLS-scoped rows — no
// auth logic (RLS already guaranteed every row belongs to the requesting
// landlord). Deduplicates by UnitKey first: read_landlord_units fans a
// co-managed unit out to one row per manager (see dedupeUnitsByKey), so
// counting raw rows overstates totalUnits/leased and understates available.
func summarizePortfolioPulse(units []portfolioPulseUnit) portfolioPulseResult {
	units = dedupeUnitsByKey(units)
	res := portfolioPulseResult{Units: units, TotalUnits: len(units)}
	for _, u := range units {
		switch u.UnitStatus {
		case "leased":
			res.Leased++
		case "available":
			res.Available++
		case "pending":
			res.Pending++
		case "withdrawn":
			res.Withdrawn++
		default:
			res.NotListed++
		}
	}
	if res.TotalUnits > 0 {
		res.OccupancyRate = float64(res.Leased) / float64(res.TotalUnits)
	}
	return res
}

// occupiedLeaseAppKeys returns the leaseAppKey (EntityKey) of every SIGNED —
// currently occupying — application among the landlord's RLS-scoped rows.
// An application with no signed lease isn't occupying a unit yet, so it's
// excluded: only an occupying resident can have a booking or a tab to
// attach-rate against.
func occupiedLeaseAppKeys(rows []protectedLandlordRow) []string {
	keys := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.SignedAt != nil && *r.SignedAt != "" {
			keys = append(keys, r.EntityKey)
		}
	}
	return keys
}

// computeServiceAttachRate folds the (global, cross-landlord) front-desk
// booking-history + café tab rows down to the subset touching THIS
// landlord's occupied leases, never surfacing any other landlord's or
// resident's raw row in the response — only the count. A row that fails to
// decode or carries no leaseAppKey is skipped (mirrors front-desk's and
// cafe-app's own tombstoned-entry guards).
//
// "Attached" means used the service within the last serviceAttachLookbackDays
// (this-period, not right-this-second): a booking counts if its status isn't
// "waitlisted" (never got a seat) and its startsAt falls at or after cutoff —
// a currently-booked future class always qualifies, since its startsAt is
// always >= cutoff. A row whose startsAt fails to parse falls back to
// counting by existence (status == "booked") so a session tombstoned out
// from under a still-live booking (OPTIONAL forSession,
// packages/front-desk/lenses.go) doesn't silently drop it. A tab counts if
// it's still open (mirrors cmd/cafe-app's
// own open-tab reasoning — no window needed, it's active by definition) or
// if it settled at or after cutoff.
func computeServiceAttachRate(occupied []string, now time.Time, bookingKeys []string, getBookings kvGetter, tabKeys []string, getTabs kvGetter) (attached, total int) {
	total = len(occupied)
	if total == 0 {
		return 0, 0
	}
	occupiedSet := make(map[string]bool, total)
	for _, k := range occupied {
		occupiedSet[k] = true
	}
	cutoff := now.AddDate(0, 0, -serviceAttachLookbackDays)

	active := make(map[string]bool)
	for _, k := range bookingKeys {
		raw, ok := getBookings(k)
		if !ok {
			continue
		}
		var b serviceBookingRow
		if json.Unmarshal(raw, &b) != nil || b.LeaseAppKey == "" || !occupiedSet[b.LeaseAppKey] || b.Status == "waitlisted" {
			continue
		}
		startsAt, err := time.Parse(time.RFC3339, b.StartsAt)
		if err != nil {
			if b.Status == "booked" {
				active[b.LeaseAppKey] = true
			}
			continue
		}
		if !startsAt.Before(cutoff) {
			active[b.LeaseAppKey] = true
		}
	}

	prefix := cafedomain.TabSettlementTarget + "."
	for _, k := range tabKeys {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		raw, ok := getTabs(k)
		if !ok {
			continue
		}
		var t serviceTabRow
		if json.Unmarshal(raw, &t) != nil || t.LeaseAppKey == "" || !occupiedSet[t.LeaseAppKey] {
			continue
		}
		if t.Status != "settled" {
			active[t.LeaseAppKey] = true
			continue
		}
		if settledAt, err := time.Parse(time.RFC3339, t.SettledAt); err == nil && !settledAt.Before(cutoff) {
			active[t.LeaseAppKey] = true
		}
	}

	return len(active), total
}

func (s *server) handlePortfolioPulse(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticateRead(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "authentication required: "+err.Error())
		return
	}
	if s.pgPool == nil {
		s.logger.Error("portfolio-pulse protected read requested but pgPool is nil (set LOFTSPACE_APP_PG_DSN + ensure Postgres and the loftspace-domain protected lens are up)")
		s.writeError(w, http.StatusBadGateway, "protected read model unavailable")
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	units, err := queryLandlordUnits(ctx, s.pgPool, actor.Subject)
	if err != nil {
		s.logger.Error("read protected landlord units", "error", err)
		s.writeError(w, http.StatusBadGateway, "could not read the protected landlord-units model")
		return
	}
	result := summarizePortfolioPulse(units)

	// Service-attach-rate is additive, best-effort: neither the landlord-
	// applications read nor the NATS-KV lens buckets are load-bearing for
	// occupancy above, so any failure here just leaves the three
	// attach-rate fields at their zero value rather than failing the
	// request front-desk-bookings-style.
	if appRows, err := queryLandlordApplications(ctx, s.pgPool, actor.Subject); err == nil && s.conn != nil {
		occupied := occupiedLeaseAppKeys(appRows)
		conn := s.conn
		bookingKeys, bErr := conn.KVListKeys(ctx, frontdesk.BookingHistoryBucket)
		tabKeys, tErr := conn.KVListKeys(ctx, weaverTargetsBucket)
		if bErr == nil && tErr == nil {
			getBookings := func(key string) ([]byte, bool) {
				entry, err := conn.KVGet(ctx, frontdesk.BookingHistoryBucket, key)
				if err != nil {
					return nil, false
				}
				return entry.Value, true
			}
			getTabs := func(key string) ([]byte, bool) {
				entry, err := conn.KVGet(ctx, weaverTargetsBucket, key)
				if err != nil {
					return nil, false
				}
				return entry.Value, true
			}
			attached, total := computeServiceAttachRate(occupied, time.Now(), bookingKeys, getBookings, tabKeys, getTabs)
			result.OccupiedLeases = total
			result.ServiceAttached = attached
			if total > 0 {
				result.ServiceAttachRate = float64(attached) / float64(total)
			}
		}
	}

	s.writeJSON(w, http.StatusOK, result)
}
