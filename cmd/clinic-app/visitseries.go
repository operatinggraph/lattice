package main

import (
	"context"
	"net/http"
)

// protectedVisitSeriesRow is one row of the visitSeriesRead protected Postgres
// read model (D1.5) — the patient-anchored recurring-visit-series view.
// PatientKey is non-nullable (the anchor walk is REQUIRED, fail-closed); every
// other neighbour/display column is nullable (an OPTIONAL match or a value not
// yet set).
type protectedVisitSeriesRow struct {
	EntityKey         string  `json:"entityKey"`
	PatientKey        string  `json:"patientKey"`
	PatientName       *string `json:"patientName,omitempty"`
	ProviderKey       *string `json:"providerKey,omitempty"`
	ProviderName      *string `json:"providerName,omitempty"`
	ProviderSpecialty *string `json:"providerSpecialty,omitempty"`
	IntervalDays      int     `json:"intervalDays"`
	NextDueAt         string  `json:"nextDueAt"`
	OccurrenceCount   int     `json:"occurrenceCount"`
	Active            bool    `json:"active"`
}

// selectMyVisitSeriesSQL reads the protected model. It carries NO auth WHERE —
// the RLS policy (FORCE ROW LEVEL SECURITY + the set-membership policy)
// injects the actor scope from the txn-local lattice.actor_id session
// variable. Rows sort by next_due_at (then entity_key) so the due-soonest
// floats up, mirroring computeVisitSeries' sort on the unprotected side.
const selectMyVisitSeriesSQL = `
SELECT entity_key, patient_key, patient_name, provider_key, provider_name, provider_specialty,
       COALESCE(interval_days, 0), COALESCE(next_due_at, ''), COALESCE(occurrence_count, 0),
       COALESCE(active, false)
FROM read_visit_series
ORDER BY next_due_at, entity_key`

// queryMyVisitSeries runs the protected read inside a per-request transaction
// with a txn-local actor session variable — the same pooling-safety discipline
// as queryMyAppointments (SET LOCAL is discarded at COMMIT/ROLLBACK, so the
// pooled connection inherits no actor across requests). The query itself
// carries no auth filter; RLS is the scope.
//
// actorID must be the bare identity NanoID (VerifiedActor.Subject), matching
// the patient_key anchor's nanoIdFromKey representation and the actor_id
// column in actor_read_grants.
func queryMyVisitSeries(ctx context.Context, pool pgxBeginner, actorID string) ([]protectedVisitSeriesRow, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actorID); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, selectMyVisitSeriesSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]protectedVisitSeriesRow, 0)
	for rows.Next() {
		var row protectedVisitSeriesRow
		if err := rows.Scan(
			&row.EntityKey, &row.PatientKey, &row.PatientName, &row.ProviderKey, &row.ProviderName, &row.ProviderSpecialty,
			&row.IntervalDays, &row.NextDueAt, &row.OccurrenceCount, &row.Active,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// handleMyVisitSeries implements GET /api/my-visit-series — a patient's own
// recurring visit series, served from the PROTECTED visitSeriesRead Postgres
// read model as an AUTHENTICATED actor (D1.5, mirroring handleMyAppointments).
// The actor comes ONLY from the verified JWT; RLS returns only that patient's
// series rows.
func (s *server) handleMyVisitSeries(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticateRead(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "authentication required: "+err.Error())
		return
	}
	if s.pgPool == nil {
		s.writeError(w, http.StatusBadGateway,
			"protected read model not configured (set CLINIC_APP_PG_DSN and ensure Postgres + the clinic-reminders protected lens are up)")
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	rows, err := queryMyVisitSeries(ctx, s.pgPool, actor.Subject)
	if err != nil {
		s.logger.Error("read protected visit series", "error", err)
		s.writeError(w, http.StatusBadGateway, "could not read the protected visit-series model")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"series": rows, "count": len(rows), "scope": "rls"})
}

// handleStaffVisitSeries implements GET /api/staff/visit-series — the
// clinic-wide recurring-visit-series worklist, PROTECTED and RLS-scoped
// (D1.5, mirroring handleStaffAppointments). It reuses queryMyVisitSeries
// verbatim: the query itself carries no auth filter, so the SAME
// read_visit_series query
// that scopes an ordinary patient to their own rows returns EVERY row for an
// actor holding the reserved WildcardAnchor ("*") grant (internal/refractor/
// adapter.WildcardAnchor) — the bootstrap capabilityReadWildcardGrants lens
// grants it to the kernel-seeded root-equivalent identities only (D1 design
// §3.4 M5). This is still RLS, never a bypass: an all-access read is
// attributable and revocable exactly like any other actor_read_grants row.
func (s *server) handleStaffVisitSeries(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticateRead(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "authentication required: "+err.Error())
		return
	}
	if s.pgPool == nil {
		s.writeError(w, http.StatusBadGateway,
			"protected read model not configured (set CLINIC_APP_PG_DSN and ensure Postgres + the clinic-reminders protected lens are up)")
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	rows, err := queryMyVisitSeries(ctx, s.pgPool, actor.Subject)
	if err != nil {
		s.logger.Error("read protected visit series (staff)", "error", err)
		s.writeError(w, http.StatusBadGateway, "could not read the protected visit-series model")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"series": rows, "count": len(rows), "scope": "rls"})
}
