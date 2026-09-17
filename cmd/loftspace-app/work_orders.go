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
type landlordWorkOrderRow struct {
	WorkOrderKey    string `json:"workOrderKey"`
	UnitKey         string `json:"unitKey"`
	UnitAddress     string `json:"unitAddress"`
	Summary         string `json:"summary"`
	Priority        string `json:"priority"`
	ReportedAt      string `json:"reportedAt"`
	ReportedBy      string `json:"reportedBy"`
	ResolvedAt      string `json:"resolvedAt,omitempty"`
	ResolutionNotes string `json:"resolutionNotes,omitempty"`
	OpenTaskCount   int    `json:"openTaskCount"`
}

// selectLandlordWorkOrdersSQL reads the protected maintenance model. No auth
// WHERE — RLS scopes the rows to the requesting landlord via the txn-local
// lattice.actor_id session variable, same as selectLandlordUnitsSQL.
// Newest-reported-first, so the panel reads like a worklist.
const selectLandlordWorkOrdersSQL = `
SELECT work_order_key, unit_key, unit_address, summary, priority,
       reported_at, reported_by, COALESCE(resolved_at, ''), COALESCE(resolution_notes, ''),
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
			&wo.WorkOrderKey, &wo.UnitKey, &wo.UnitAddress, &wo.Summary, &wo.Priority,
			&wo.ReportedAt, &wo.ReportedBy, &wo.ResolvedAt, &wo.ResolutionNotes,
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
