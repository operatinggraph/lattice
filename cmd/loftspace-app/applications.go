package main

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// protectedApplicationRow is one row of the PROTECTED lease-applications Postgres read
// model (read_lease_applications, D1.3 Fire 2), returned by the authenticated
// reader. RLS has already scoped the rows to the requesting actor before they
// reach here, so there is no client-side filter.
//
// The protected model carries the application's display scalars (unit / terms /
// signed / landlord-decision) but NOT the Weaver-internal convergence aggregate
// (the missing_*/inflight_*/declined_* gap booleans) — that state is §10.2
// Weaver-only today; D1.5 rolls a protected gap model onto this pattern. The FE
// renders a coarse status from landlordDecision + signedAt until then. The
// derived booleans below are computed from landlord_decision so the existing FE
// keys keep meaning.
//
// Nullable columns (the OPTIONAL unit/terms/signature/decision walks) are
// pointers so an absent value stays absent rather than rendering a misleading
// zero/empty.
type protectedApplicationRow struct {
	EntityKey          string   `json:"entityKey"`
	Applicant          string   `json:"applicant"`
	UnitKey            *string  `json:"unitKey"`
	UnitAddress        *string  `json:"unitAddress"`
	UnitCity           *string  `json:"unitCity"`
	UnitRegion         *string  `json:"unitRegion"`
	UnitRent           *float64 `json:"unitRent"`
	UnitCurrency       *string  `json:"unitCurrency"`
	UnitStatus         *string  `json:"unitStatus"`
	UnitBedrooms       *float64 `json:"unitBedrooms"`
	UnitBathrooms      *float64 `json:"unitBathrooms"`
	UnitAvailableFrom  *string  `json:"unitAvailableFrom"`
	SignedAt           *string  `json:"signedAt"`
	LandlordDecision   *string  `json:"landlordDecision"`
	LandlordApproved   bool     `json:"landlordApproved"`
	LandlordDeclined   bool     `json:"landlordDeclined"`
	Declined           bool     `json:"declined"`
	DeclineReason      *string  `json:"declineReason"`
	TermsMoveInDate    *string  `json:"termsMoveInDate"`
	TermsLeaseTerm     *float64 `json:"termsLeaseTermMonths"`
	TermsRequestedRent *float64 `json:"termsRequestedRent"`
}

// selectApplicationsSQL reads the protected model. It carries NO auth WHERE — the
// RLS policy (FORCE ROW LEVEL SECURITY + the set-membership policy) injects the
// actor scope from the txn-local lattice.actor_id session variable. Rows sort by
// app_id for a stable view.
const selectApplicationsSQL = `
SELECT entity_key, applicant, unit_key, unit_address, unit_city, unit_region,
       unit_rent, unit_currency, unit_status, unit_bedrooms, unit_bathrooms,
       unit_available_from, signed_at, landlord_decision,
       decline_reason, terms_move_in_date, terms_lease_term_months,
       terms_requested_rent
FROM read_lease_applications
ORDER BY app_id`

// queryApplications runs the protected read inside a per-request transaction with
// a txn-local actor session variable. The transaction is the pooling-safety crux:
// set_config(..., is_local=true) is discarded at COMMIT/ROLLBACK, so the pooled
// connection returns clean and the next request inherits no actor (deny) until it
// sets its own. The query itself carries no auth filter — RLS is the scope.
//
// actorID must be the bare identity NanoID (VerifiedActor.Subject), matching the
// actor_id column in actor_read_grants and the §6.14 anchor representation.
func queryApplications(ctx context.Context, pool pgxBeginner, actorID string) ([]protectedApplicationRow, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// set_config(name, value, is_local=true) is the parameterized, injection-safe
	// equivalent of `SET LOCAL` (which cannot take a bind parameter): is_local=true
	// scopes the setting to this transaction.
	if _, err := tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actorID); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, selectApplicationsSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]protectedApplicationRow, 0)
	for rows.Next() {
		var row protectedApplicationRow
		if err := rows.Scan(
			&row.EntityKey, &row.Applicant, &row.UnitKey, &row.UnitAddress,
			&row.UnitCity, &row.UnitRegion, &row.UnitRent, &row.UnitCurrency,
			&row.UnitStatus, &row.UnitBedrooms, &row.UnitBathrooms, &row.UnitAvailableFrom,
			&row.SignedAt, &row.LandlordDecision, &row.DeclineReason,
			&row.TermsMoveInDate, &row.TermsLeaseTerm, &row.TermsRequestedRent,
		); err != nil {
			return nil, err
		}
		deriveLandlordFlags(&row)
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

// selectApplicationByKeySQL is selectApplicationsSQL narrowed to one entity_key
// (D1.5 — the executed-lease document builder needs one application, not the
// caller's whole list). RLS still governs visibility: a key that exists but
// isn't among the caller's authz_anchors returns zero rows, identical to a
// genuinely absent key, so the caller cannot tell "not mine" from "not real."
const selectApplicationByKeySQL = `
SELECT entity_key, applicant, unit_key, unit_address, unit_city, unit_region,
       unit_rent, unit_currency, unit_status, unit_bedrooms, unit_bathrooms,
       unit_available_from, signed_at, landlord_decision, decline_reason,
       terms_move_in_date, terms_lease_term_months, terms_requested_rent
FROM read_lease_applications
WHERE entity_key = $1`

// queryApplicationByKey is queryApplications narrowed to one application (D1.5).
// Same txn-local actor + pooling-safety discipline; returns ok=false (no error)
// when RLS or a genuinely missing key yields no row — the two are
// indistinguishable by design (see selectApplicationByKeySQL).
func queryApplicationByKey(ctx context.Context, pool pgxBeginner, actorID, entityKey string) (protectedApplicationRow, bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return protectedApplicationRow{}, false, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actorID); err != nil {
		return protectedApplicationRow{}, false, err
	}

	var row protectedApplicationRow
	err = tx.QueryRow(ctx, selectApplicationByKeySQL, entityKey).Scan(
		&row.EntityKey, &row.Applicant, &row.UnitKey, &row.UnitAddress,
		&row.UnitCity, &row.UnitRegion, &row.UnitRent, &row.UnitCurrency,
		&row.UnitStatus, &row.UnitBedrooms, &row.UnitBathrooms, &row.UnitAvailableFrom,
		&row.SignedAt, &row.LandlordDecision, &row.DeclineReason,
		&row.TermsMoveInDate, &row.TermsLeaseTerm, &row.TermsRequestedRent,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return protectedApplicationRow{}, false, tx.Commit(ctx)
		}
		return protectedApplicationRow{}, false, err
	}
	deriveLandlordFlags(&row)
	if err := tx.Commit(ctx); err != nil {
		return protectedApplicationRow{}, false, err
	}
	return row, true, nil
}

// deriveLandlordFlags recomputes the landlord-decision booleans the FE keys on
// from the raw landlord_decision string, since the protected model projects the
// raw value (not the booleans the Weaver convergence lens derived).
func deriveLandlordFlags(row *protectedApplicationRow) {
	if row.LandlordDecision == nil {
		return
	}
	switch *row.LandlordDecision {
	case "approved":
		row.LandlordApproved = true
	case "declined":
		row.LandlordDeclined = true
		row.Declined = true
	}
}

// handleApplications implements GET /api/applications — the My Applications
// tracker, served from the PROTECTED lease-applications Postgres read model as an
// AUTHENTICATED actor (D1.3 Fire 3). The actor comes ONLY from the verified JWT;
// RLS returns only that actor's rows, so there is no client-supplied applicant
// filter (a `?applicant=` query param does nothing — RLS keys off the verified
// session var, not the param). The old weaver-targets KVListKeys + client-side
// filter (the read-path leak, §10.2) is removed.
func (s *server) handleApplications(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticateRead(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "authentication required: "+err.Error())
		return
	}
	if s.pgPool == nil {
		s.writeError(w, http.StatusBadGateway,
			"protected read model not configured (set LOFTSPACE_APP_PG_DSN and ensure Postgres + the lease-signing protected lens are up)")
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	rows, err := queryApplications(ctx, s.pgPool, actor.Subject)
	if err != nil {
		// Log the detail (which can carry the failing SQL / schema names) and return
		// a generic message — never echo a raw DB error to the client.
		s.logger.Error("read protected lease applications", "error", err)
		s.writeError(w, http.StatusBadGateway, "could not read the protected lease-applications model")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"applications": rows, "count": len(rows), "scope": "rls"})
}
