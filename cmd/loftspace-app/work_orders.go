package main

import (
	"context"
	"net/http"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// The landlord Maintenance panel (docs/reviews/loftspace-maintenance-loop-2026-09-17.md
// decision 5) — GET /api/landlord/work-orders reads the PROTECTED
// landlordWorkOrdersRead Postgres model as an AUTHENTICATED actor. Sibling of
// handlePortfolioPulse's occupancy read (portfolio.go): identical verified-JWT
// -> per-request txn -> SET LOCAL lattice.actor_id -> RLS path against a
// simple, single-lens protected model — no cross-lens join, unlike
// handleUnitApplications. RLS scopes every row to units the signed-in
// landlord manages; there is no client-side filter.

// landlordWorkOrderRow is one row of read_landlord_work_orders, as scanned
// from the RLS-scoped read. Priority/summary/reportedBy are never empty (the
// report aspect is written atomically with the work order); ResolvedAt/
// ResolutionNotes stay empty until ResolveWorkOrder records a resolution.
// ReportedByResident answers "is the reporter a resident of this unit" off
// the lens's own reportedBy/residesIn walk (loftspace-domain/lenses.go) —
// false for a staff reporter and for a legacy order with no reportedBy link
// yet, never null.
// LandlordKey is the row's own managing landlord (landlordWorkOrdersRead
// fans a co-managed unit's order out to one row per landlord) — the FE gates
// its Resolve button on it (resolveOffered, app.js): landlordWorkOrdersRead
// also anchors covering BUILDINGS, so a dual-hat landlord/staffer with a
// building worksAt grant sees every order in the building, and the self leg
// ResolveWorkOrder's script selects can only ever bind THIS landlord's own
// manages link, never a co-landlord's or a bare staffer's.
type landlordWorkOrderRow struct {
	WorkOrderKey       string `json:"workOrderKey"`
	LandlordKey        string `json:"landlordKey"`
	UnitKey            string `json:"unitKey"`
	UnitAddress        string `json:"unitAddress"`
	Summary            string `json:"summary"`
	Priority           string `json:"priority"`
	ReportedAt         string `json:"reportedAt"`
	ReportedBy         string `json:"reportedBy"`
	ReportedByResident bool   `json:"reportedByResident"`
	ResolvedAt         string `json:"resolvedAt,omitempty"`
	ResolutionNotes    string `json:"resolutionNotes,omitempty"`
	OpenTaskCount      int    `json:"openTaskCount"`
}

// selectLandlordWorkOrdersSQL reads the protected maintenance model. No auth
// WHERE — RLS scopes the rows to the requesting landlord via the txn-local
// lattice.actor_id session variable, same as selectLandlordUnitsSQL.
// Newest-reported-first, so the panel reads like a worklist.
const selectLandlordWorkOrdersSQL = `
SELECT work_order_key, landlord_key, unit_key, unit_address, summary, priority,
       reported_at, reported_by, COALESCE(reported_by_resident, false),
       COALESCE(resolved_at, ''), COALESCE(resolution_notes, ''),
       COALESCE(open_task_count, 0)
FROM read_landlord_work_orders
ORDER BY reported_at DESC`

// queryLandlordWorkOrders runs the protected landlord maintenance read inside
// a per-request transaction with a txn-local actor session variable — the
// same pooling-safety pattern as queryLandlordUnits.
func queryLandlordWorkOrders(ctx context.Context, pool pgxBeginner, actorID string) ([]landlordWorkOrderRow, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actorID); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, selectLandlordWorkOrdersSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]landlordWorkOrderRow, 0)
	for rows.Next() {
		var wo landlordWorkOrderRow
		var openTaskCount float64
		if err := rows.Scan(
			&wo.WorkOrderKey, &wo.LandlordKey, &wo.UnitKey, &wo.UnitAddress, &wo.Summary, &wo.Priority,
			&wo.ReportedAt, &wo.ReportedBy, &wo.ReportedByResident, &wo.ResolvedAt, &wo.ResolutionNotes,
			&openTaskCount,
		); err != nil {
			return nil, err
		}
		wo.OpenTaskCount = int(openTaskCount)
		out = append(out, wo)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// reporterWorkOrderRow is one row of maintenance-domain's read_reporter_work_orders
// (docs/reviews/loftspace-maintenance-loop-closes-2026-09-18.md decision 4/7)
// — the reporter's own "My reports" list. NoticeSentAt is empty until
// RecordWorkOrderResolvedNotice tells the reporter their order resolved
// (decision 5); it is never set for a self-resolved order.
type reporterWorkOrderRow struct {
	WorkOrderKey    string `json:"workOrderKey"`
	ReporterKey     string `json:"reporterKey"`
	UnitKey         string `json:"unitKey,omitempty"`
	UnitAddress     string `json:"unitAddress,omitempty"`
	Summary         string `json:"summary"`
	Priority        string `json:"priority"`
	ReportedAt      string `json:"reportedAt"`
	ResolvedAt      string `json:"resolvedAt,omitempty"`
	ResolutionNotes string `json:"resolutionNotes,omitempty"`
	OpenTaskCount   int    `json:"openTaskCount"`
	NoticeSentAt    string `json:"noticeSentAt,omitempty"`
}

// selectReporterWorkOrdersSQL reads the protected reporter-anchored
// maintenance model. No auth WHERE — RLS scopes the rows to the requesting
// reporter via the txn-local lattice.actor_id session variable, same shape
// as selectLandlordWorkOrdersSQL. Newest-reported-first.
const selectReporterWorkOrdersSQL = `
SELECT work_order_key, reporter_key, COALESCE(unit_key, ''), COALESCE(unit_address, ''),
       summary, priority, reported_at, COALESCE(resolved_at, ''), COALESCE(resolution_notes, ''),
       COALESCE(open_task_count, 0), COALESCE(notice_sent_at, '')
FROM read_reporter_work_orders
ORDER BY reported_at DESC`

// queryReporterWorkOrders runs the protected reporter maintenance read inside
// a per-request transaction with a txn-local actor session variable — the
// same pooling-safety pattern as queryLandlordWorkOrders.
func queryReporterWorkOrders(ctx context.Context, pool pgxBeginner, actorID string) ([]reporterWorkOrderRow, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actorID); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, selectReporterWorkOrdersSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]reporterWorkOrderRow, 0)
	for rows.Next() {
		var wo reporterWorkOrderRow
		var openTaskCount float64
		if err := rows.Scan(
			&wo.WorkOrderKey, &wo.ReporterKey, &wo.UnitKey, &wo.UnitAddress,
			&wo.Summary, &wo.Priority, &wo.ReportedAt, &wo.ResolvedAt, &wo.ResolutionNotes,
			&openTaskCount, &wo.NoticeSentAt,
		); err != nil {
			return nil, err
		}
		wo.OpenTaskCount = int(openTaskCount)
		out = append(out, wo)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *server) handleLandlordWorkOrders(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticateRead(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "authentication required: "+err.Error())
		return
	}
	if s.pgPool == nil {
		s.logger.Error("landlord work-orders protected read requested but pgPool is nil (set LOFTSPACE_APP_PG_DSN + ensure Postgres and the loftspace-domain protected lens are up)")
		s.writeError(w, http.StatusBadGateway, "protected read model unavailable")
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	rows, err := queryLandlordWorkOrders(ctx, s.pgPool, actor.Subject)
	if err != nil {
		s.logger.Error("read protected landlord work orders", "error", err)
		s.writeError(w, http.StatusBadGateway, "could not read the protected landlord work-orders model")
		return
	}
	resp := s.withProjectionHealth(ctx, pkgmgr.LensID("loftspace-domain", "landlordWorkOrdersRead"),
		map[string]any{"workOrders": rows, "count": len(rows), "scope": "rls"})
	s.writeJSON(w, http.StatusOK, resp)
}

// handleMyWorkOrders implements GET /api/my/work-orders — the tenant's own
// "My reports" list (decision 7), reading maintenance-domain's protected
// reporterWorkOrdersRead the same authenticated-actor -> per-request txn ->
// SET LOCAL lattice.actor_id -> RLS path as handleLandlordWorkOrders. RLS
// scopes every row to work orders the signed-in identity itself reported;
// there is no client-side filter.
func (s *server) handleMyWorkOrders(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticateRead(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "authentication required: "+err.Error())
		return
	}
	if s.pgPool == nil {
		s.logger.Error("reporter work-orders protected read requested but pgPool is nil (set LOFTSPACE_APP_PG_DSN + ensure Postgres and the maintenance-domain protected lens are up)")
		s.writeError(w, http.StatusBadGateway, "protected read model unavailable")
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	rows, err := queryReporterWorkOrders(ctx, s.pgPool, actor.Subject)
	if err != nil {
		s.logger.Error("read protected reporter work orders", "error", err)
		s.writeError(w, http.StatusBadGateway, "could not read the protected reporter work-orders model")
		return
	}
	resp := s.withProjectionHealth(ctx, pkgmgr.LensID("maintenance-domain", "reporterWorkOrdersRead"),
		map[string]any{"workOrders": rows, "count": len(rows), "scope": "rls"})
	s.writeJSON(w, http.StatusOK, resp)
}
