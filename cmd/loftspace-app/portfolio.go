package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	cafedomain "github.com/operatinggraph/lattice/packages/cafe-domain"
	frontdesk "github.com/operatinggraph/lattice/packages/front-desk"
)

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
// landlord's currently-occupied (signed) leases, what fraction have a live
// wellness booking or an open café tab. It joins three lens read-models
// entirely client-side (this app already reads landlordLeaseApplicationsRead
// for the occupied-lease set; front-desk-bookings and cafe-domain's
// cafeTabSettlement are both global NATS-KV buckets keyed/filterable by
// leaseAppKey, the same join key front-desk's own FE already uses,
// packages/front-desk/lenses.go) — the precedent for an app reading a
// DIFFERENT package's lens bucket already exists twice (cmd/cafe-app reads
// front-desk's bucket; this app already reads packages/privacy-base's), so
// this is applying an established pattern, not inventing one. Best-effort:
// unlike occupancy (which 502s if Postgres is down), a missing NATS
// connection or an unreadable bucket degrades attach-rate to zero/omitted
// rather than failing the whole portfolio-pulse response — the same posture
// front-desk-bookings itself takes ("no bucket = no rows, not an error").

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
	// many of those have a live booking or open tab. Both are 0, and
	// ServiceAttachRate is 0, when the cross-package read is unavailable —
	// the FE distinguishes "0 attached of N" from "no data" by checking
	// OccupiedLeases > 0 first.
	OccupiedLeases    int     `json:"occupiedLeases"`
	ServiceAttached   int     `json:"serviceAttached"`
	ServiceAttachRate float64 `json:"serviceAttachRate"`
}

// weaverTargetsBucket is the shared cross-package Weaver convergence bucket
// every actorAggregate lens projects into, multiplexed by key prefix — the
// same bucket cmd/cafe-app's own weaverTargetsBucket constant names
// (packages/cafe-domain/lenses.go).
const weaverTargetsBucket = "weaver-targets"

// serviceBookingRow is the front-desk-bookings lens row (packages/front-desk),
// narrowed to the leaseAppKey the attach-rate joins on.
type serviceBookingRow struct {
	LeaseAppKey string `json:"leaseAppKey"`
}

// serviceTabRow is the cafeTabSettlement convergence-lens row
// (packages/cafe-domain), narrowed to the leaseAppKey + status the
// attach-rate joins on (mirrors cmd/cafe-app's tabSettlementProjection).
type serviceTabRow struct {
	LeaseAppKey string `json:"leaseAppKey"`
	Status      string `json:"status"`
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

// summarizePortfolioPulse folds the flat per-unit rows into the aggregate
// counts the FE card renders. A pure function of the RLS-scoped rows — no
// auth logic (RLS already guaranteed every row belongs to the requesting
// landlord).
func summarizePortfolioPulse(units []portfolioPulseUnit) portfolioPulseResult {
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
// booking + café tab rows down to the subset touching THIS landlord's
// occupied leases, never surfacing any other landlord's or resident's raw
// row in the response — only the count. A row that fails to decode or
// carries no leaseAppKey is skipped (mirrors front-desk's and cafe-app's own
// tombstoned-entry guards). A tab counts as "attached" while it is anything
// other than settled (mirrors cmd/cafe-app's own open-tab reasoning); a
// booking counts simply by existing (the frontDeskBookings lens already
// filters to status='booked' — see packages/front-desk/lenses.go).
func computeServiceAttachRate(occupied []string, bookingKeys []string, getBookings kvGetter, tabKeys []string, getTabs kvGetter) (attached, total int) {
	total = len(occupied)
	if total == 0 {
		return 0, 0
	}
	occupiedSet := make(map[string]bool, total)
	for _, k := range occupied {
		occupiedSet[k] = true
	}

	active := make(map[string]bool)
	for _, k := range bookingKeys {
		raw, ok := getBookings(k)
		if !ok {
			continue
		}
		var b serviceBookingRow
		if json.Unmarshal(raw, &b) != nil || b.LeaseAppKey == "" || !occupiedSet[b.LeaseAppKey] {
			continue
		}
		active[b.LeaseAppKey] = true
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
		bookingKeys, bErr := conn.KVListKeys(ctx, frontdesk.BookingsBucket)
		tabKeys, tErr := conn.KVListKeys(ctx, weaverTargetsBucket)
		if bErr == nil && tErr == nil {
			getBookings := func(key string) ([]byte, bool) {
				entry, err := conn.KVGet(ctx, frontdesk.BookingsBucket, key)
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
			attached, total := computeServiceAttachRate(occupied, bookingKeys, getBookings, tabKeys, getTabs)
			result.OccupiedLeases = total
			result.ServiceAttached = attached
			if total > 0 {
				result.ServiceAttachRate = float64(attached) / float64(total)
			}
		}
	}

	s.writeJSON(w, http.StatusOK, result)
}
